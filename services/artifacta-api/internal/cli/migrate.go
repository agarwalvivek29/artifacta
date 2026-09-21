package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/api"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
)

const migrateUsage = `artifacta migrate — copy a file-store deployment onto Postgres + S3 (ADR-0026)

Offline, one-shot, resumable. Reads a file-store data directory (metadata + blob
bundles + the audit log) and copies everything into a Postgres store and an S3
bucket, preserving the audit hash chain verbatim. Safe to re-run: a partial or failed
migration resumes in place without duplicating. The source is only ever read.

Quiesce the source first (stop the server) — a source written to mid-run yields a
partial copy.

Usage:
  artifacta migrate --from-data-dir <path> --to-database-url <dsn> \
                    --to-s3-bucket <bucket> [--to-s3-endpoint <url>] [--to-s3-region <r>] \
                    [--to-s3-access-key-id <id>] [--to-s3-secret-access-key <secret>] \
                    [--to-s3-force-path-style]

The --to-s3-* flags each fall back to the matching ARTIFACTA_S3_* environment variable
when omitted.
`

// migrate copies a FileStore + BlobFS deployment onto the Postgres store + S3 blob
// backend (ADR-0026, spec 10). It is offline, one-shot, and resumable; the source is
// read-only. Backends are built by reusing open() twice (source vs destination).
func migrate(args []string) error {
	var (
		fromDataDir, toDatabaseURL     string
		s3Endpoint, s3Region, s3Bucket string
		s3AccessKey, s3SecretKey       string
		s3ForcePath                    bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("migrate: flag %s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch a {
		case "--from-data-dir":
			fromDataDir, err = next()
		case "--to-database-url":
			toDatabaseURL, err = next()
		case "--to-s3-endpoint":
			s3Endpoint, err = next()
		case "--to-s3-region":
			s3Region, err = next()
		case "--to-s3-bucket":
			s3Bucket, err = next()
		case "--to-s3-access-key-id":
			s3AccessKey, err = next()
		case "--to-s3-secret-access-key":
			s3SecretKey, err = next()
		case "--to-s3-force-path-style":
			s3ForcePath = true
		case "-h", "--help":
			fmt.Print(migrateUsage)
			return nil
		default:
			return fmt.Errorf("migrate: unknown flag %q (try: artifacta migrate --help)", a)
		}
		if err != nil {
			return err
		}
	}

	// S3 destination flags fall back to the ARTIFACTA_S3_* env vars.
	s3Endpoint = orEnv(s3Endpoint, "ARTIFACTA_S3_ENDPOINT")
	s3Region = orEnv(s3Region, "ARTIFACTA_S3_REGION")
	s3Bucket = orEnv(s3Bucket, "ARTIFACTA_S3_BUCKET")
	s3AccessKey = orEnv(s3AccessKey, "ARTIFACTA_S3_ACCESS_KEY_ID")
	s3SecretKey = orEnv(s3SecretKey, "ARTIFACTA_S3_SECRET_ACCESS_KEY")

	switch {
	case fromDataDir == "":
		return fmt.Errorf("migrate: --from-data-dir is required")
	case toDatabaseURL == "":
		return fmt.Errorf("migrate: --to-database-url is required")
	case s3Bucket == "":
		return fmt.Errorf("migrate: --to-s3-bucket (or ARTIFACTA_S3_BUCKET) is required")
	}

	// Source: file store + fs blobs under the given data dir (open() joins meta/
	// and blobs/). Reused verbatim, nothing new.
	srcStore, srcBlob, err := open(config.Config{
		StoreBackend: "file",
		BlobBackend:  "file",
		DataDir:      fromDataDir,
	})
	if err != nil {
		return fmt.Errorf("open source (file store at %s): %w", fromDataDir, err)
	}
	// Destination: Postgres store + S3 blobs. Dest schema self-creates via AutoMigrate.
	dstStore, dstBlob, err := open(config.Config{
		StoreBackend:      "postgres",
		DatabaseURL:       toDatabaseURL,
		BlobBackend:       "s3",
		S3Endpoint:        s3Endpoint,
		S3Region:          s3Region,
		S3Bucket:          s3Bucket,
		S3AccessKeyID:     s3AccessKey,
		S3SecretAccessKey: s3SecretKey,
		S3ForcePathStyle:  s3ForcePath,
	})
	if err != nil {
		return fmt.Errorf("open destination (postgres + s3): %w", err)
	}

	return runMigrate(srcStore, srcBlob, dstStore, dstBlob)
}

// counts tracks copied-vs-skipped per entity type for the final summary.
type counts struct{ copied, skipped int }

func (c counts) String() string { return fmt.Sprintf("%d copied, %d skipped", c.copied, c.skipped) }

// runMigrate performs the copy. Order: source-chain pre-verify (fail fast) →
// per-artifact metadata (existence-checked) → audit (verbatim) → blobs (checksummed)
// → completeness gate. Every step is idempotent so the whole run is resumable.
func runMigrate(srcStore api.Store, srcBlob api.Blob, dstStore api.Store, dstBlob api.Blob) error {
	// 1. Fail fast: refuse a source whose own audit chain does not verify, before
	//    copying anything.
	srcAudit, err := srcStore.AuditEvents()
	if err != nil {
		return fmt.Errorf("read source audit log: %w", err)
	}
	if _, err := infra.VerifyChain(srcAudit); err != nil {
		return fmt.Errorf("source audit chain does not verify (refusing to migrate a broken source): %w", err)
	}

	// 1b. Refuse a foreign/divergent destination BEFORE writing anything. The dest
	//     audit trail must be a prefix of this source's chain: empty (a fresh dest)
	//     or an equal-prefix (a resumable partial of the SAME source). This promotes
	//     the per-event AppendRaw divergence guard to an up-front check, so a dest
	//     holding another source's audit is never polluted with this source's
	//     artifacts. (Residual: a foreign dest with NO audit rows can't be told apart
	//     here — see the ADR-0026 provenance note.)
	dstAuditBefore, err := dstStore.AuditEvents()
	if err != nil {
		return fmt.Errorf("read destination audit log: %w", err)
	}
	if len(dstAuditBefore) > len(srcAudit) {
		return fmt.Errorf("destination has %d audit events but the source has %d — destination holds foreign data; refusing before any write", len(dstAuditBefore), len(srcAudit))
	}
	for i, de := range dstAuditBefore {
		if de.GetSeq() != srcAudit[i].GetSeq() || de.GetHash() != srcAudit[i].GetHash() {
			return fmt.Errorf("destination audit event %d (seq %d) does not match the source — destination holds foreign or divergent data; refusing before any write", i, de.GetSeq())
		}
	}

	arts, err := srcStore.AllArtifacts()
	if err != nil {
		return fmt.Errorf("enumerate source artifacts: %w", err)
	}

	var cArt, cVer, cGrant, cComment, cAudit, cBlob counts

	for _, a := range arts {
		slug := a.GetSlug()
		if err := dstStore.PutArtifact(a); err != nil { // upsert → idempotent
			return fmt.Errorf("artifact %q: %w", slug, err)
		}
		cArt.copied++

		vs, err := srcStore.Versions(slug)
		if err != nil {
			return fmt.Errorf("artifact %q: read versions: %w", slug, err)
		}
		for _, v := range vs {
			_, ok, err := dstStore.GetVersion(slug, v.GetN())
			if err != nil {
				return fmt.Errorf("artifact %q: version %d lookup: %w", slug, v.GetN(), err)
			}
			if ok {
				cVer.skipped++
				continue
			}
			if err := dstStore.AddVersion(v); err != nil {
				return fmt.Errorf("artifact %q: version %d: %w", slug, v.GetN(), err)
			}
			cVer.copied++
		}

		if err := copyGrants(srcStore, dstStore, slug, &cGrant); err != nil {
			return err
		}
		if err := copyComments(srcStore, dstStore, slug, &cComment); err != nil {
			return err
		}

		for _, v := range vs {
			wrote, err := copyBlob(srcBlob, dstBlob, slug, v.GetN())
			if err != nil {
				return fmt.Errorf("artifact %q: blob v%d: %w", slug, v.GetN(), err)
			}
			if wrote {
				cBlob.copied++
			} else {
				cBlob.skipped++
			}
		}
	}

	// 2. Audit — verbatim, in seq order. Count copied vs skipped from the dest's
	//    pre-run snapshot taken in step 1b (AppendRaw is a no-op on an already-present
	//    seq).
	present := map[int64]bool{}
	for _, e := range dstAuditBefore {
		present[e.GetSeq()] = true
	}
	for _, e := range srcAudit {
		if err := dstStore.AppendRaw(e); err != nil {
			return fmt.Errorf("audit event seq %d: %w", e.GetSeq(), err)
		}
		if present[e.GetSeq()] {
			cAudit.skipped++
		} else {
			cAudit.copied++
		}
	}

	// 3. Completeness gate: VerifyChain plus source↔dest count + endpoint equality
	//    (VerifyChain alone passes on a truncated chain — it does not pin the first
	//    seq to 1).
	dstAudit, err := dstStore.AuditEvents()
	if err != nil {
		return fmt.Errorf("re-read destination audit log: %w", err)
	}
	if _, err := infra.VerifyChain(dstAudit); err != nil {
		return fmt.Errorf("destination audit chain does not verify after migration: %w", err)
	}
	if err := assertAuditComplete(srcAudit, dstAudit); err != nil {
		return err
	}

	fmt.Println("migration complete:")
	fmt.Printf("  artifacts:     %d\n", cArt.copied)
	fmt.Printf("  versions:      %s\n", cVer)
	fmt.Printf("  grants:        %s\n", cGrant)
	fmt.Printf("  comments:      %s\n", cComment)
	fmt.Printf("  audit events:  %s\n", cAudit)
	fmt.Printf("  blobs:         %s\n", cBlob)
	fmt.Printf("  audit chain verified and complete (%d events)\n", len(dstAudit))
	return nil
}

// copyGrants copies a slug's grants, deduping by grantee identity (grantKey).
func copyGrants(src, dst api.Store, slug string, c *counts) error {
	srcGrants, err := src.Grants(slug)
	if err != nil {
		return fmt.Errorf("artifact %q: read grants: %w", slug, err)
	}
	if len(srcGrants) == 0 {
		return nil
	}
	dstGrants, err := dst.Grants(slug)
	if err != nil {
		return fmt.Errorf("artifact %q: read dest grants: %w", slug, err)
	}
	have := map[string]bool{}
	for _, g := range dstGrants {
		have[grantKey(g)] = true
	}
	for _, g := range srcGrants {
		k := grantKey(g)
		if have[k] {
			c.skipped++
			continue
		}
		if err := dst.AddGrant(g); err != nil {
			return fmt.Errorf("artifact %q: grant %s: %w", slug, k, err)
		}
		have[k] = true
		c.copied++
	}
	return nil
}

// copyComments copies a slug's comments, deduping by comment id.
func copyComments(src, dst api.Store, slug string, c *counts) error {
	srcComments, err := src.Comments(slug)
	if err != nil {
		return fmt.Errorf("artifact %q: read comments: %w", slug, err)
	}
	if len(srcComments) == 0 {
		return nil
	}
	dstComments, err := dst.Comments(slug)
	if err != nil {
		return fmt.Errorf("artifact %q: read dest comments: %w", slug, err)
	}
	have := map[string]bool{}
	for _, cm := range dstComments {
		have[cm.GetId()] = true
	}
	for _, cm := range srcComments {
		if have[cm.GetId()] {
			c.skipped++
			continue
		}
		if err := dst.AddComment(cm); err != nil {
			return fmt.Errorf("artifact %q: comment %s: %w", slug, cm.GetId(), err)
		}
		c.copied++
	}
	return nil
}

// grantKey is the migration dedupe identity for a grant: grantee_sub when non-empty,
// else grantee_email (lower-cased) — mirroring the app's own share dedupe and authz
// match. A sub-only key would collapse distinct email-only invites (ADR-0019), which
// carry an empty grantee_sub.
func grantKey(g *artifactav1.Grant) string {
	if s := g.GetGranteeSub(); s != "" {
		return "sub:" + s
	}
	return "email:" + strings.ToLower(g.GetGranteeEmail())
}

// copyBlob copies one version's bundle src→dst and verifies byte-equality by sha256.
// It returns wrote=true when it (re)wrote the bundle, false when the destination
// already held a byte-identical copy (resume skip).
func copyBlob(src, dst api.Blob, slug string, n int32) (wrote bool, err error) {
	srcSum, err := blobSum(src, slug, n)
	if err != nil {
		return false, fmt.Errorf("read source blob: %w", err)
	}
	// Skip when the destination already has a byte-identical copy.
	switch dstSum, derr := blobSum(dst, slug, n); {
	case derr == nil && dstSum == srcSum:
		return false, nil
	case derr != nil && !errors.Is(derr, fs.ErrNotExist):
		return false, fmt.Errorf("read destination blob: %w", derr)
	}

	rc, err := src.Get(slug, n)
	if err != nil {
		return false, fmt.Errorf("open source blob: %w", err)
	}
	defer rc.Close()
	if err := dst.Put(slug, n, rc); err != nil {
		return false, fmt.Errorf("write destination blob: %w", err)
	}
	// Read-back checksum: prove the bytes landed intact.
	dstSum, err := blobSum(dst, slug, n)
	if err != nil {
		return false, fmt.Errorf("verify destination blob: %w", err)
	}
	if dstSum != srcSum {
		return false, fmt.Errorf("checksum mismatch after copy (source %s…, dest %s…)", srcSum[:12], dstSum[:12])
	}
	return true, nil
}

// blobSum streams a blob through sha256 and returns the hex digest.
func blobSum(b api.Blob, slug string, n int32) (string, error) {
	rc, err := b.Get(slug, n)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// assertAuditComplete proves the destination trail is not just internally consistent
// (VerifyChain) but the WHOLE source trail: equal count, and equal first/last seq +
// last hash. Both slices are seq-ascending (AuditEvents contract).
func assertAuditComplete(src, dst []*artifactav1.AuditEvent) error {
	if len(src) != len(dst) {
		return fmt.Errorf("audit incomplete: source has %d events, destination has %d", len(src), len(dst))
	}
	if len(src) == 0 {
		return nil
	}
	if src[0].GetSeq() != dst[0].GetSeq() {
		return fmt.Errorf("audit incomplete: first seq differs (source %d, dest %d)", src[0].GetSeq(), dst[0].GetSeq())
	}
	sl, dl := src[len(src)-1], dst[len(dst)-1]
	if sl.GetSeq() != dl.GetSeq() || sl.GetHash() != dl.GetHash() {
		return fmt.Errorf("audit incomplete: last event differs (source seq %d, dest seq %d)", sl.GetSeq(), dl.GetSeq())
	}
	return nil
}

func orEnv(v, key string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return os.Getenv(key)
}
