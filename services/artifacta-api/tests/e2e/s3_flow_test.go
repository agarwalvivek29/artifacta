// Package e2e — S3-backed variant. This drives the SAME black-box HTTP flow as
// flow_test.go, but with the artifact bytes living in an S3-compatible store
// (ADR-0006) instead of the filesystem. It proves the end-to-end invariant that
// matters for the S3 backend: bytes are written to and streamed back FROM S3
// only through the app's CanView gate + audit — never directly — and that the
// fail-closed 404 semantics are identical to the filesystem backend.
//
// It runs only when ARTIFACTA_TEST_S3_BUCKET (+ endpoint/keys) points at a
// reachable S3-compatible endpoint (e.g. a local MinIO). Plain CI without one
// skips it, exactly like the store's postgres conformance suite.
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
)

func newS3Blob(t *testing.T) *infra.BlobS3 {
	t.Helper()
	bucket := os.Getenv("ARTIFACTA_TEST_S3_BUCKET")
	if bucket == "" {
		t.Skip("set ARTIFACTA_TEST_S3_BUCKET (+ endpoint/keys) to run the S3-backed e2e flow")
	}
	bl, err := infra.NewBlobS3(context.Background(), infra.S3Config{
		Endpoint:        os.Getenv("ARTIFACTA_TEST_S3_ENDPOINT"),
		Region:          os.Getenv("ARTIFACTA_TEST_S3_REGION"),
		Bucket:          bucket,
		AccessKeyID:     os.Getenv("ARTIFACTA_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("ARTIFACTA_TEST_S3_SECRET_ACCESS_KEY"),
		ForcePathStyle:  true,
	})
	if err != nil {
		t.Fatalf("NewBlobS3: %v", err)
	}
	return bl
}

func TestEndToEndPublishViewShareFlow_S3Backend(t *testing.T) {
	h := newHarnessWithBlob(t, newS3Blob(t))

	// --- 1. Publish: bytes are written to S3 via the app, never by the client. --
	const payload = `<!doctype html><html><body><h1>S3 Report</h1><p>s3-marker-77</p></body></html>`
	code, body := h.do(t, http.MethodPost, "/artifacts?title=S3Report", alice, "text/html; charset=utf-8", payload)
	if code != http.StatusCreated {
		t.Fatalf("publish as alice: got %d, want 201 (body %q)", code, body)
	}
	var pub struct {
		Slug string `json:"slug"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal([]byte(body), &pub); err != nil {
		t.Fatalf("decode publish response: %v (body %q)", err, body)
	}
	if pub.Slug == "" {
		t.Fatal("publish returned empty slug")
	}
	slug := pub.Slug
	rawPath := "/a/" + slug + "/raw"

	// --- 2. Owner streams the exact bytes back THROUGH the app from S3. --------
	code, raw := h.do(t, http.MethodGet, rawPath, alice, "", "")
	if code != http.StatusOK {
		t.Fatalf("raw as owner alice: got %d, want 200", code)
	}
	if raw != payload {
		t.Fatalf("raw body as owner (from S3): got %q, want %q", raw, payload)
	}

	// --- 3. Fail-closed authorization is identical to the FS backend. ----------
	// Anonymous and non-owner both get 404 while PRIVATE — the object exists in
	// S3, but existence is never leaked because CanView runs before the fetch.
	if code, _ := h.do(t, http.MethodGet, rawPath, "", "", ""); code != http.StatusNotFound {
		t.Fatalf("raw as anonymous: got %d, want 404", code)
	}
	if code, _ := h.do(t, http.MethodGet, rawPath, bob, "", ""); code != http.StatusNotFound {
		t.Fatalf("raw as non-owner bob (private): got %d, want 404", code)
	}
	// A version that does not exist in the bucket is a not-leaking 404, not a 500
	// — this is the os.IsNotExist translation in BlobS3.Get, end to end.
	if code, _ := h.do(t, http.MethodGet, "/a/"+slug+"/v/99/raw", alice, "", ""); code != http.StatusNotFound {
		t.Fatalf("owner raw of missing version 99: got %d, want 404", code)
	}

	// --- 4. Add an immutable version: a second object lands in S3 (ADR-0013). --
	const payload2 = `<!doctype html><html><body><h1>S3 Report v2</h1><p>s3-marker-88</p></body></html>`
	code, vbody := h.do(t, http.MethodPost, "/artifacts/"+slug+"/versions", alice, "text/html; charset=utf-8", payload2)
	if code != http.StatusCreated {
		t.Fatalf("add version as alice: got %d, want 201 (body %q)", code, vbody)
	}
	var ver struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal([]byte(vbody), &ver); err != nil {
		t.Fatalf("decode add-version response: %v (body %q)", err, vbody)
	}
	if ver.Version != 2 {
		t.Fatalf("add version returned version %d, want 2", ver.Version)
	}
	// /raw now serves v2; the pinned v1 and v2 both stream their own S3 object.
	if code, got := h.do(t, http.MethodGet, rawPath, alice, "", ""); code != http.StatusOK || got != payload2 {
		t.Fatalf("latest raw after add-version: code=%d body=%q, want 200 and v2 payload", code, got)
	}
	if code, got := h.do(t, http.MethodGet, "/a/"+slug+"/v/1/raw", alice, "", ""); code != http.StatusOK || got != payload {
		t.Fatalf("pinned v1 raw: code=%d body=%q, want 200 and v1 payload (version isolation in S3)", code, got)
	}
	if code, got := h.do(t, http.MethodGet, "/a/"+slug+"/v/2/raw", alice, "", ""); code != http.StatusOK || got != payload2 {
		t.Fatalf("pinned v2 raw: code=%d body=%q, want 200 and v2 payload", code, got)
	}

	// --- 5. Share → grantee can stream from S3; ungranted cannot. --------------
	if code, r := h.do(t, http.MethodPatch, "/artifacts/"+slug+"/visibility", alice, "application/json", `{"visibility":"invited"}`); code != http.StatusOK {
		t.Fatalf("owner set-visibility invited: got %d, want 200 (body %q)", code, r)
	}
	if code, r := h.do(t, http.MethodPost, "/artifacts/"+slug+"/grants", alice, "application/json", `{"grantee_sub":"`+bob+`"}`); code != http.StatusCreated {
		t.Fatalf("owner add-grant bob: got %d, want 201 (body %q)", code, r)
	}
	if code, got := h.do(t, http.MethodGet, rawPath, bob, "", ""); code != http.StatusOK || got != payload2 {
		t.Fatalf("granted bob raw: code=%d body=%q, want 200 and v2 payload", code, got)
	}
	if code, _ := h.do(t, http.MethodGet, rawPath, carol, "", ""); code != http.StatusNotFound {
		t.Fatalf("ungranted carol raw (invited): got %d, want 404", code)
	}

	// --- 6. The audit hash chain is intact after the full S3-backed flow. ------
	verified, err := infra.VerifyAuditLog(h.metaDir)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if verified < 6 {
		t.Fatalf("audit chain verified %d events, want >= 6 (publish + version + views + shares)", verified)
	}
	t.Logf("S3-backed audit chain verified: %d events", verified)
}
