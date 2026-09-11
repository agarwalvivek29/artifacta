package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
)

// uploadServer is a test Server with the browser upload toggle ON.
func uploadServer(t *testing.T, dir string, auth Auth) *Server {
	t.Helper()
	srv := newTestServer(t, dir, auth)
	srv.UploadUI = true
	return srv
}

// multipartUpload builds a browser-style multipart/form-data POST /artifacts.
func multipartUpload(t *testing.T, filename, title string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if title != "" {
		if err := mw.WriteField("title", title); err != nil {
			t.Fatalf("write title: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/artifacts", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func blobCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "blobs"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read blobs dir: %v", err)
	}
	return len(entries)
}

// A signed-in user uploading an allowlisted HTML file with the toggle on gets a
// private v1 artifact they own, served back, and a PUBLISH audit — then a 303 to
// the new artifact.
func TestUploadHTMLCreatesPrivateArtifact(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:tester", Email: "tester@localhost"}
	srv := uploadServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const payload = "<h1>uploaded page</h1>"
	req := multipartUpload(t, "page.html", "My Upload", []byte(payload))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("upload: got %d, want 303 (body %q)", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/a/") {
		t.Fatalf("redirect Location = %q, want /a/<slug>", loc)
	}
	slug := strings.TrimPrefix(loc, "/a/")

	art, ok, err := srv.Store.GetArtifact(slug)
	if err != nil || !ok {
		t.Fatalf("GetArtifact(%q): ok=%v err=%v", slug, ok, err)
	}
	if art.GetOwnerSub() != owner.GetSub() {
		t.Fatalf("owner: got %q, want %q", art.GetOwnerSub(), owner.GetSub())
	}
	if art.GetVisibility() != artifactav1.Visibility_VISIBILITY_PRIVATE {
		t.Fatalf("visibility: got %v, want PRIVATE", art.GetVisibility())
	}
	if art.GetTitle() != "My Upload" {
		t.Fatalf("title: got %q, want %q", art.GetTitle(), "My Upload")
	}
	if !strings.Contains(art.GetContentType(), "text/html") {
		t.Fatalf("content type: got %q, want text/html", art.GetContentType())
	}

	rawRR := httptest.NewRecorder()
	h.ServeHTTP(rawRR, httptest.NewRequest(http.MethodGet, "/a/"+slug+"/raw", nil))
	if rawRR.Code != http.StatusOK || rawRR.Body.String() != payload {
		t.Fatalf("raw: code=%d body=%q, want 200 + payload", rawRR.Code, rawRR.Body.String())
	}

	logBytes, _ := os.ReadFile(filepath.Join(dir, "meta", "audit.log"))
	if !strings.Contains(string(logBytes), "AUDIT_ACTION_PUBLISH") {
		t.Fatalf("audit log missing PUBLISH event")
	}
}

// A PNG upload is stored and served with the server-decided image/png content
// type (never taken from the multipart part header), and the title defaults to
// the filename when omitted.
func TestUploadPNGStoredWithServerDecidedType(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:tester"}
	srv := uploadServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 32))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, multipartUpload(t, "diagram.png", "", png))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("upload png: got %d, want 303", rr.Code)
	}
	slug := strings.TrimPrefix(rr.Header().Get("Location"), "/a/")

	art, ok, _ := srv.Store.GetArtifact(slug)
	if !ok {
		t.Fatal("artifact not stored")
	}
	if art.GetContentType() != "image/png" {
		t.Fatalf("content type: got %q, want image/png", art.GetContentType())
	}
	if art.GetTitle() != "diagram.png" {
		t.Fatalf("title default: got %q, want filename", art.GetTitle())
	}
	rawRR := httptest.NewRecorder()
	h.ServeHTTP(rawRR, httptest.NewRequest(http.MethodGet, "/a/"+slug+"/raw", nil))
	if ct := rawRR.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("served content type: got %q, want image/png", ct)
	}
}

// With the toggle OFF, a multipart upload is refused and nothing is stored — the
// raw CLI path (separate test) still works.
func TestUploadRefusedWhenToggleOff(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:tester"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true}) // UploadUI stays false
	h := srv.Routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, multipartUpload(t, "page.html", "", []byte("<p>x</p>")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("toggle-off upload: got %d, want 400", rr.Code)
	}
	if n := blobCount(t, dir); n != 0 {
		t.Fatalf("blob written while toggle off: %d", n)
	}
}

// A non-allowlisted type is rejected 415 and nothing is stored.
func TestUploadRejectsNonAllowlistedType(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:tester"}
	srv := uploadServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, multipartUpload(t, "malware.exe", "", []byte("MZ...")))
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("bad type upload: got %d, want 415", rr.Code)
	}
	if n := blobCount(t, dir); n != 0 {
		t.Fatalf("blob written for rejected type: %d", n)
	}
}

// An anonymous upload fails closed (401), even with the toggle on.
func TestUploadAnonymousRejected(t *testing.T) {
	dir := t.TempDir()
	srv := uploadServer(t, dir, fakeAuth{ok: false})
	h := srv.Routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, multipartUpload(t, "page.html", "", []byte("<p>x</p>")))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("anon upload: got %d, want 401", rr.Code)
	}
	if n := blobCount(t, dir); n != 0 {
		t.Fatalf("blob written for anon upload: %d", n)
	}
}

// A cross-site Origin on the multipart POST is rejected (CSRF backstop).
func TestUploadCrossOriginRejected(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:tester"}
	srv := uploadServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	req := multipartUpload(t, "page.html", "", []byte("<p>x</p>"))
	req.Header.Set("Origin", "https://evil.example.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin upload: got %d, want 403", rr.Code)
	}
	if n := blobCount(t, dir); n != 0 {
		t.Fatalf("blob written for cross-origin upload: %d", n)
	}
}

// An oversize upload trips the maxBytes guard → 413, nothing stored.
func TestUploadOverLimitRejected(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:tester"}
	srv := uploadServer(t, dir, fakeAuth{id: owner, ok: true})
	const limit = 64
	h := maxBytes(http.HandlerFunc(srv.publish), limit)

	req := multipartUpload(t, "page.html", "", []byte(strings.Repeat("x", limit+256)))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge && rr.Code != http.StatusBadRequest {
		t.Fatalf("over-limit upload: got %d, want 413/400", rr.Code)
	}
	fs := srv.Store.(*infra.FileStore)
	if arts, err := fs.ListByOwner(owner.GetSub()); err != nil || len(arts) != 0 {
		t.Fatalf("metadata stored despite over-limit upload: arts=%d err=%v", len(arts), err)
	}
}
