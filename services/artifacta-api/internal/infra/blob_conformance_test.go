package infra_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/api"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
)

// The blob store has two interchangeable backends (filesystem, S3-compatible).
// This suite runs the identical behaviour checks against whichever is passed, so
// "bytes round-trip and a missing object fails closed the same way on both" is
// verified, not asserted. Keys are unique per run so it is safe to re-run against
// a shared bucket. Every case is a named subtest so a failure localizes to the
// exact behaviour and the exact backend.
func runBlobConformance(t *testing.T, bl api.Blob) {
	t.Helper()
	run := fmt.Sprintf("t%d", time.Now().UnixNano())

	// putGet is a helper: Put payload at (slug, n), read it back, and return the
	// bytes. It fails the subtest on any error and always closes the reader.
	putGet := func(t *testing.T, slug string, n int32, payload []byte) []byte {
		t.Helper()
		if err := bl.Put(slug, n, bytes.NewReader(payload)); err != nil {
			t.Fatalf("Put(%s,%d): %v", slug, n, err)
		}
		rc, err := bl.Get(slug, n)
		if err != nil {
			t.Fatalf("Get(%s,%d): %v", slug, n, err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read(%s,%d): %v", slug, n, err)
		}
		return got
	}

	t.Run("round_trip_distinct_versions", func(t *testing.T) {
		slug := run + "rt"
		v1 := []byte("<h1>version one</h1>")
		v2 := []byte("<h1>version two — different bytes</h1>")
		if got := putGet(t, slug, 1, v1); !bytes.Equal(got, v1) {
			t.Fatalf("v1 = %q, want %q", got, v1)
		}
		if got := putGet(t, slug, 2, v2); !bytes.Equal(got, v2) {
			t.Fatalf("v2 = %q, want %q", got, v2)
		}
		// v1 is untouched by the v2 write — versions are isolated objects.
		rc, err := bl.Get(slug, 1)
		if err != nil {
			t.Fatalf("Get v1 after v2 write: %v", err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if !bytes.Equal(got, v1) {
			t.Fatalf("v1 after v2 write = %q, want %q (version isolation broken)", got, v1)
		}
	})

	t.Run("missing_version_is_not_exist", func(t *testing.T) {
		slug := run + "mv"
		if err := bl.Put(slug, 1, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatalf("Put: %v", err)
		}
		// A version that was never written on an EXISTING slug fails closed.
		if _, err := bl.Get(slug, 99); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Get missing version = %v; want an os.IsNotExist error", err)
		}
	})

	t.Run("missing_slug_is_not_exist", func(t *testing.T) {
		// A slug that was never written at all fails closed the same way — the API
		// layer maps this to a not-leaking 404.
		if _, err := bl.Get(run+"neverwritten", 1); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Get unknown slug = %v; want an os.IsNotExist error", err)
		}
	})

	t.Run("empty_payload", func(t *testing.T) {
		slug := run + "empty"
		if got := putGet(t, slug, 1, []byte{}); len(got) != 0 {
			t.Fatalf("empty payload round-trip = %d bytes, want 0", len(got))
		}
		// A zero-byte object still EXISTS — Get must succeed, not report not-exist.
		rc, err := bl.Get(slug, 1)
		if err != nil {
			t.Fatalf("Get empty object: %v (want a real, present, zero-byte object)", err)
		}
		rc.Close()
	})

	t.Run("binary_payload_all_byte_values", func(t *testing.T) {
		slug := run + "bin"
		payload := make([]byte, 256)
		for i := range payload {
			payload[i] = byte(i) // 0x00..0xFF, incl. NULs and non-UTF8
		}
		if got := putGet(t, slug, 1, payload); !bytes.Equal(got, payload) {
			t.Fatalf("binary payload corrupted: got %d bytes, first mismatch matters", len(got))
		}
	})

	t.Run("large_payload_integrity", func(t *testing.T) {
		slug := run + "large"
		payload := make([]byte, 1<<20) // 1 MiB
		for i := range payload {
			payload[i] = byte((i*31 + 7) % 251) // deterministic, non-trivial
		}
		if got := putGet(t, slug, 1, payload); !bytes.Equal(got, payload) {
			t.Fatalf("1MiB payload corrupted: got %d bytes want %d", len(got), len(payload))
		}
	})

	t.Run("overwrite_same_version_replaces", func(t *testing.T) {
		slug := run + "ow"
		first := []byte("first write")
		second := []byte("second write, wholly different length and content")
		putGet(t, slug, 1, first)
		if got := putGet(t, slug, 1, second); !bytes.Equal(got, second) {
			t.Fatalf("overwrite (slug,n) = %q, want %q (second write must win)", got, second)
		}
	})

	t.Run("high_version_number", func(t *testing.T) {
		slug := run + "hv"
		payload := []byte("v one million")
		if got := putGet(t, slug, 1_000_000, payload); !bytes.Equal(got, payload) {
			t.Fatalf("high version round-trip = %q, want %q", got, payload)
		}
	})

	t.Run("concurrent_distinct_versions", func(t *testing.T) {
		slug := run + "conc"
		const workers = 8
		var wg sync.WaitGroup
		errs := make([]error, workers)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				payload := []byte(fmt.Sprintf("concurrent version %d payload", n))
				if err := bl.Put(slug, int32(n+1), bytes.NewReader(payload)); err != nil {
					errs[n] = err
				}
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("concurrent Put v%d: %v", i+1, err)
			}
		}
		// Every version is independently readable with its own bytes.
		for i := 0; i < workers; i++ {
			want := []byte(fmt.Sprintf("concurrent version %d payload", i))
			rc, err := bl.Get(slug, int32(i+1))
			if err != nil {
				t.Fatalf("Get concurrent v%d: %v", i+1, err)
			}
			got, _ := io.ReadAll(rc)
			rc.Close()
			if !bytes.Equal(got, want) {
				t.Fatalf("concurrent v%d = %q, want %q", i+1, got, want)
			}
		}
	})
}

func TestBlobFS_Conformance(t *testing.T) {
	bl, err := infra.NewBlobFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runBlobConformance(t, bl)
}

// Runs only when ARTIFACTA_TEST_S3_BUCKET points at a reachable S3-compatible
// endpoint (e.g. a local MinIO). CI without one skips it. Configure via
// ARTIFACTA_TEST_S3_{ENDPOINT,REGION,BUCKET,ACCESS_KEY_ID,SECRET_ACCESS_KEY}.
func TestBlobS3_Conformance(t *testing.T) {
	bucket := os.Getenv("ARTIFACTA_TEST_S3_BUCKET")
	if bucket == "" {
		t.Skip("set ARTIFACTA_TEST_S3_BUCKET (+ endpoint/keys) to run the S3 blob conformance suite")
	}
	bl, err := infra.NewBlobS3(context.Background(), infra.S3Config{
		Endpoint:        os.Getenv("ARTIFACTA_TEST_S3_ENDPOINT"),
		Region:          os.Getenv("ARTIFACTA_TEST_S3_REGION"),
		Bucket:          bucket,
		AccessKeyID:     os.Getenv("ARTIFACTA_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("ARTIFACTA_TEST_S3_SECRET_ACCESS_KEY"),
		ForcePathStyle:  true, // MinIO/R2 default in local test setups
	})
	if err != nil {
		t.Fatalf("NewBlobS3: %v", err)
	}
	runBlobConformance(t, bl)
}
