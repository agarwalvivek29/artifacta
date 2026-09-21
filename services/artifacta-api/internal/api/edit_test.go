package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// patch is a small helper: PATCH /artifacts/<slug> with a JSON body.
func patch(h http.Handler, slug, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, "/artifacts/"+slug, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestUpdateArtifactOwnerEditsAndAudits(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const slug = "editslug1"
	seedArtifact(t, srv, slug, owner.GetSub()) // Title "seeded", empty description

	rr := patch(h, slug, `{"title":"New Title","description":"what it is"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("edit: got %d, want 200 (body %q)", rr.Code, rr.Body.String())
	}

	art, ok, err := srv.Store.GetArtifact(slug)
	if err != nil || !ok {
		t.Fatalf("GetArtifact: ok=%v err=%v", ok, err)
	}
	if art.GetTitle() != "New Title" || art.GetDescription() != "what it is" {
		t.Fatalf("edit not persisted: title=%q desc=%q", art.GetTitle(), art.GetDescription())
	}

	logBytes, err := os.ReadFile(filepath.Join(dir, "meta", "audit.log"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if !strings.Contains(string(logBytes), "AUDIT_ACTION_EDIT") {
		t.Fatalf("audit log missing EDIT event: %s", logBytes)
	}
}

func TestUpdateArtifactPartialLeavesOtherField(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const slug = "editslug2"
	if err := srv.Store.PutArtifact(&artifactav1.Artifact{
		Slug: slug, OwnerSub: owner.GetSub(), Title: "Orig", Description: "orig desc",
		Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE, ContentType: "text/html; charset=utf-8",
		CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// description-only edit leaves the title untouched.
	if rr := patch(h, slug, `{"description":"new desc"}`); rr.Code != http.StatusOK {
		t.Fatalf("desc-only edit: got %d (%q)", rr.Code, rr.Body.String())
	}
	art, _, _ := srv.Store.GetArtifact(slug)
	if art.GetTitle() != "Orig" || art.GetDescription() != "new desc" {
		t.Fatalf("desc-only: title=%q desc=%q", art.GetTitle(), art.GetDescription())
	}

	// title-only edit leaves the description untouched.
	if rr := patch(h, slug, `{"title":"New"}`); rr.Code != http.StatusOK {
		t.Fatalf("title-only edit: got %d (%q)", rr.Code, rr.Body.String())
	}
	art, _, _ = srv.Store.GetArtifact(slug)
	if art.GetTitle() != "New" || art.GetDescription() != "new desc" {
		t.Fatalf("title-only: title=%q desc=%q", art.GetTitle(), art.GetDescription())
	}

	// an explicit empty description clears it.
	if rr := patch(h, slug, `{"description":""}`); rr.Code != http.StatusOK {
		t.Fatalf("clear desc: got %d (%q)", rr.Code, rr.Body.String())
	}
	art, _, _ = srv.Store.GetArtifact(slug)
	if art.GetDescription() != "" {
		t.Fatalf("clear desc: desc=%q, want empty", art.GetDescription())
	}
}

func TestUpdateArtifactRejectsEmptyTitleAndEmptyBody(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const slug = "editslug3"
	seedArtifact(t, srv, slug, owner.GetSub())

	for _, body := range []string{`{"title":"   "}`, `{}`} {
		if rr := patch(h, slug, body); rr.Code != http.StatusBadRequest {
			t.Fatalf("body %s: got %d, want 400 (%q)", body, rr.Code, rr.Body.String())
		}
	}
	art, _, _ := srv.Store.GetArtifact(slug)
	if art.GetTitle() != "seeded" {
		t.Fatalf("title changed on a rejected edit: %q", art.GetTitle())
	}
}

func TestUpdateArtifactNonOwner404AndUnauth401(t *testing.T) {
	const slug = "editslug4"

	// A signed-in non-owner gets a 404 that never distinguishes missing from not-yours.
	dir := t.TempDir()
	srv := newTestServer(t, dir, fakeAuth{id: &artifactav1.Identity{Sub: "local:intruder"}, ok: true})
	seedArtifact(t, srv, slug, "local:owner")
	if rr := patch(srv.Routes(), slug, `{"title":"x"}`); rr.Code != http.StatusNotFound {
		t.Fatalf("non-owner: got %d, want 404", rr.Code)
	}

	// An unauthenticated caller gets a 401.
	dir2 := t.TempDir()
	srv2 := newTestServer(t, dir2, fakeAuth{ok: false})
	seedArtifact(t, srv2, slug, "local:owner")
	if rr := patch(srv2.Routes(), slug, `{"title":"x"}`); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauth: got %d, want 401", rr.Code)
	}
}

func TestMetadataIncludesUploadFields(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	srv.UploadUI = true

	const slug = "metaupload1"
	if err := srv.Store.PutArtifact(&artifactav1.Artifact{
		Slug: slug, OwnerSub: owner.GetSub(), Title: "seeded", ViaUpload: true,
		Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE, ContentType: "text/html; charset=utf-8",
		CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/"+slug, nil)
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("metadata: got %d (%q)", rr.Code, rr.Body.String())
	}
	var m struct {
		ViaUpload     bool `json:"via_upload"`
		UploadEnabled bool `json:"upload_enabled"`
		IsOwner       bool `json:"is_owner"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !m.ViaUpload || !m.UploadEnabled || !m.IsOwner {
		t.Fatalf("metadata fields: via_upload=%v upload_enabled=%v is_owner=%v", m.ViaUpload, m.UploadEnabled, m.IsOwner)
	}
}
