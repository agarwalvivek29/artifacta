package infra

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
)

// BlobS3 is an S3-compatible blob store (ADR-0006). Like BlobFS it makes no
// access decisions: the API layer runs domain.CanView and appends the audit
// event, then streams bytes THROUGH the server. The bucket is private and the
// browser never talks to S3 — no presigned URLs are ever issued, so every byte
// stays gated by CanView and logged. One object per (slug, version), keyed
// "<slug>.v<n>.bundle" to match the filesystem adapter and the single-bundle
// format (ADR-0004, ADR-0013).
//
// The adapter targets the S3 API generically (AWS SDK v2, configurable endpoint
// + path-style), so AWS S3, MinIO, Cloudflare R2, Backblaze B2, and Wasabi all
// work behind the same interface.
type BlobS3 struct {
	client *s3.Client
	bucket string
}

// S3Config configures the S3-compatible blob backend. Endpoint is left empty for
// AWS S3 and set to the service URL for MinIO/R2/etc; ForcePathStyle is required
// by MinIO and some R2 setups that do not support virtual-hosted-style buckets.
type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
}

// NewBlobS3 builds the adapter from an endpoint plus either static credentials or
// the AWS default credential chain. When AccessKeyID is set, those static keys are
// used (MinIO/R2/dev, or an explicit access-key deployment); when it is empty, the
// SDK's default chain is loaded (environment, EKS Pod Identity, IRSA, shared
// config/SSO, ...), so the adapter works with a role and no keys. It performs no
// network call, so a misconfigured endpoint surfaces on first Put/Get rather than
// at construction.
func NewBlobS3(ctx context.Context, cfg S3Config) (*BlobS3, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3 blob: bucket is required (set ARTIFACTA_S3_BUCKET)")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1" // MinIO/R2 ignore the region but the signer requires one
	}
	var awsCfg aws.Config
	if cfg.AccessKeyID != "" {
		awsCfg = aws.Config{
			Region:      region,
			Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		}
	} else {
		loaded, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
		if err != nil {
			return nil, fmt.Errorf("s3 blob: load aws config: %w", err)
		}
		awsCfg = loaded
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	})
	return &BlobS3{client: client, bucket: cfg.Bucket}, nil
}

// key is the object name for one version's bundle: "<slug>.v<n>.bundle".
func (b *BlobS3) key(slug string, n int32) string {
	return fmt.Sprintf("%s.v%d.bundle", slug, n)
}

// Put uploads version n's bundle. Artifacts are small (ADR-0006), so the reader
// is drained into a seekable buffer: S3 PutObject then computes Content-Length
// and the payload checksum locally, which keeps the adapter dependency-light and
// compatible across S3 implementations (some MinIO/R2 builds reject streamed
// trailing checksums).
func (b *BlobS3) Put(slug string, n int32, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	_, err = b.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.key(slug, n)),
		Body:   bytes.NewReader(body),
	})
	return err
}

// Get opens version n's bundle for streaming; the caller owns closing the
// reader. A missing object is returned as an fs.ErrNotExist-wrapping error so the
// API layer's os.IsNotExist check treats it as a not-leaking 404, identical to
// the filesystem adapter's behaviour.
func (b *BlobS3) Get(slug string, n int32) (io.ReadCloser, error) {
	out, err := b.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.key(slug, n)),
	})
	if err != nil {
		if isS3NotFound(err) {
			// A *fs.PathError wrapping fs.ErrNotExist is what os.Open returns, so a
			// missing S3 object is detected by BOTH os.IsNotExist (the check the
			// viewer uses) and errors.Is — identical to the filesystem adapter. A
			// plain fmt.Errorf("%w", fs.ErrNotExist) would NOT satisfy os.IsNotExist,
			// which only unwraps *PathError.
			return nil, &fs.PathError{Op: "get", Path: b.key(slug, n), Err: fs.ErrNotExist}
		}
		return nil, err
	}
	return out.Body, nil
}

// isS3NotFound reports whether err is an object-not-found from any S3-compatible
// backend. AWS returns typed NoSuchKey/NotFound errors; MinIO and R2 sometimes
// surface a generic smithy APIError carrying a 404-class code instead.
func isS3NotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}
