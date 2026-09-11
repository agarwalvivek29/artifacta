package infra_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
)

// fakeS3 is a minimal in-memory, path-style S3 endpoint: just enough of
// PutObject and GetObject (including a NoSuchKey 404) to drive BlobS3 through the
// real AWS SDK request/response and signing path without Docker or a live MinIO.
// The full-backend conformance suite (TestBlobS3_Conformance) still runs against
// a real S3 when ARTIFACTA_TEST_S3_BUCKET is set; this keeps the adapter's
// round-trip and fail-closed behaviour under test in plain CI.
func fakeS3(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/") // path-style: "<bucket>/<object>"
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read", http.StatusBadRequest)
				return
			}
			mu.Lock()
			objects[key] = body
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			mu.Lock()
			b, ok := objects[key]
			mu.Unlock()
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+
					`<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(b)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBlobS3_AgainstFakeEndpoint(t *testing.T) {
	srv := fakeS3(t)
	bl, err := infra.NewBlobS3(context.Background(), infra.S3Config{
		Endpoint:        srv.URL,
		Region:          "us-east-1",
		Bucket:          "artifacta-test",
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		ForcePathStyle:  true,
	})
	if err != nil {
		t.Fatalf("NewBlobS3: %v", err)
	}
	runBlobConformance(t, bl)
}
