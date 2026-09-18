package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// multipartVersion builds a browser-style multipart POST /artifacts/<slug>/versions.
func multipartVersion(t *testing.T, slug, filename string, content []byte) *http.Request {
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
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/versions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// seedOwnedArtifact puts an artifact owned by owner directly into the store.
func seedOwnedArtifact(t *testing.T, srv *Server, slug, owner string, viaUpload bool) {
	t.Helper()
	if err := srv.Store.PutArtifact(&artifactav1.Artifact{
		Slug: slug, OwnerSub: owner, Title: slug, Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE,
		ContentType: "text/html; charset=utf-8", LatestVersion: 1, CreatedAt: timestamppb.Now(), ViaUpload: viaUpload,
	}); err != nil {
		t.Fatalf("seed %q: %v", slug, err)
	}
}

// The happy path: an artifact created via the upload UI accepts a new version
// uploaded from the browser — a v2 is appended and latest_version advances.
func TestUploadNewVersionOnUploadArtifact(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@x.co"}
	srv := uploadServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	// Create v1 through the upload UI (sets via_upload).
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, multipartUpload(t, "page.html", "My Page", []byte("<p>v1</p>")))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("upload create: got %d, want 303", rr.Code)
	}
	slug := strings.TrimPrefix(rr.Header().Get("Location"), "/a/")
	if slug == "" {
		t.Fatal("no slug in redirect")
	}

	// Upload a new version through the browser.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, multipartVersion(t, slug, "page.html", []byte("<p>v2</p>")))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("upload new version: got %d, want 303 (body %q)", rr.Code, rr.Body.String())
	}
	art, ok, _ := srv.Store.GetArtifact(slug)
	if !ok || art.GetLatestVersion() != 2 {
		t.Fatalf("latest_version = %d (ok=%v), want 2", art.GetLatestVersion(), ok)
	}
	if vs, _ := srv.Store.Versions(slug); len(vs) != 2 {
		t.Fatalf("versions = %d, want 2", len(vs))
	}
}

// A new-version upload is refused (409) for an artifact NOT created via upload —
// CLI/API artifacts are not version-editable from the browser.
func TestUploadNewVersionRejectedForNonUploadArtifact(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@x.co"}
	srv := uploadServer(t, dir, fakeAuth{id: owner, ok: true})
	seedOwnedArtifact(t, srv, "clislug", owner.GetSub(), false) // via_upload = false

	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, multipartVersion(t, "clislug", "page.html", []byte("<p>v2</p>")))
	if rr.Code != http.StatusConflict {
		t.Fatalf("version-upload on non-upload artifact: got %d, want 409", rr.Code)
	}
	if art, _, _ := srv.Store.GetArtifact("clislug"); art.GetLatestVersion() != 1 {
		t.Fatalf("latest_version = %d, want 1 (unchanged)", art.GetLatestVersion())
	}
}

// A non-owner is refused with the fail-closed 404 (ownedArtifact gate) even for an
// upload-created artifact.
func TestUploadNewVersionRejectedForNonOwner(t *testing.T) {
	dir := t.TempDir()
	caller := &artifactav1.Identity{Sub: "local:caller", Email: "caller@x.co"}
	srv := uploadServer(t, dir, fakeAuth{id: caller, ok: true})
	seedOwnedArtifact(t, srv, "theirs", "local:someone-else", true)

	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, multipartVersion(t, "theirs", "page.html", []byte("<p>v2</p>")))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("version-upload by non-owner: got %d, want 404", rr.Code)
	}
}

// With the upload toggle OFF, the browser version-upload path is refused.
func TestUploadNewVersionRejectedWhenToggleOff(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@x.co"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true}) // UploadUI stays false
	seedOwnedArtifact(t, srv, "up", owner.GetSub(), true)

	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, multipartVersion(t, "up", "page.html", []byte("<p>v2</p>")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("version-upload with toggle off: got %d, want 400", rr.Code)
	}
}
