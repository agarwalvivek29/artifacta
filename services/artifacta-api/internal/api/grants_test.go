package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// seedArtifact writes an owned artifact straight into the store so the sharing
// endpoints have something to mutate, independent of the publish path.
func seedArtifact(t *testing.T, srv *Server, slug, owner string) {
	t.Helper()
	err := srv.Store.PutArtifact(&artifactav1.Artifact{
		Slug:        slug,
		OwnerSub:    owner,
		Title:       "seeded",
		Visibility:  artifactav1.Visibility_VISIBILITY_PRIVATE,
		ContentType: "text/html; charset=utf-8",
		CreatedAt:   timestamppb.Now(),
	})
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
}

func TestAddGrantOwnerCreatesGrantAndAudits(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const slug = "seededslug1"
	seedArtifact(t, srv, slug, owner.GetSub())

	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/grants",
		strings.NewReader(`{"grantee_sub":"local:friend"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("add grant: got %d, want 201 (body %q)", rr.Code, rr.Body.String())
	}

	// The grant is persisted and queryable for the slug.
	grants, err := srv.Store.Grants(slug)
	if err != nil {
		t.Fatalf("Grants(%q): %v", slug, err)
	}
	found := false
	for _, g := range grants {
		if g.GetGranteeSub() == "local:friend" && g.GetSlug() == slug && g.GetGrantedBy() == owner.GetSub() {
			found = true
		}
	}
	if !found {
		t.Fatalf("grant for local:friend not found in %v", grants)
	}

	// A SHARE audit event was appended.
	logBytes, err := os.ReadFile(filepath.Join(dir, "meta", "audit.log"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if !strings.Contains(string(logBytes), "AUDIT_ACTION_SHARE") {
		t.Fatalf("audit log missing SHARE event: %s", logBytes)
	}
}

func TestAddGrantNonOwnerReturns404(t *testing.T) {
	dir := t.TempDir()
	owner := "local:owner"
	notOwner := &artifactav1.Identity{Sub: "local:intruder", Email: "intruder@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: notOwner, ok: true})
	h := srv.Routes()

	const slug = "seededslug2"
	seedArtifact(t, srv, slug, owner)

	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/grants",
		strings.NewReader(`{"grantee_sub":"local:friend"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("non-owner add grant: got %d, want 404", rr.Code)
	}
	// No grant leaked into the store.
	grants, _ := srv.Store.Grants(slug)
	if len(grants) != 0 {
		t.Fatalf("grant created by non-owner: %v", grants)
	}
}

func TestAddGrantUnknownSlugReturns404(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	req := httptest.NewRequest(http.MethodPost, "/artifacts/doesnotexist/grants",
		strings.NewReader(`{"grantee_sub":"local:friend"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown slug add grant: got %d, want 404", rr.Code)
	}
}

func TestAddGrantUnauthenticatedReturns401(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t, dir, fakeAuth{ok: false})
	h := srv.Routes()

	const slug = "seededslug3"
	seedArtifact(t, srv, slug, "local:owner")

	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/grants",
		strings.NewReader(`{"grantee_sub":"local:friend"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauth add grant: got %d, want 401", rr.Code)
	}
}

func TestSetVisibilityOwnerChangesStoredValue(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const slug = "seededslug4"
	seedArtifact(t, srv, slug, owner.GetSub())

	req := httptest.NewRequest(http.MethodPatch, "/artifacts/"+slug+"/visibility",
		strings.NewReader(`{"visibility":"invited"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("set visibility: got %d, want 200 (body %q)", rr.Code, rr.Body.String())
	}

	art, ok, err := srv.Store.GetArtifact(slug)
	if err != nil || !ok {
		t.Fatalf("GetArtifact(%q): ok=%v err=%v", slug, ok, err)
	}
	if art.GetVisibility() != artifactav1.Visibility_VISIBILITY_INVITED {
		t.Fatalf("visibility: got %v, want INVITED", art.GetVisibility())
	}

	// The mutation was audited as a SHARE event.
	logBytes, err := os.ReadFile(filepath.Join(dir, "meta", "audit.log"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if !strings.Contains(string(logBytes), "AUDIT_ACTION_SHARE") {
		t.Fatalf("audit log missing SHARE event: %s", logBytes)
	}
}

func TestSetVisibilityUnknownValueReturns400(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const slug = "seededslug5"
	seedArtifact(t, srv, slug, owner.GetSub())

	req := httptest.NewRequest(http.MethodPatch, "/artifacts/"+slug+"/visibility",
		strings.NewReader(`{"visibility":"everyone"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown visibility: got %d, want 400", rr.Code)
	}
	// The stored visibility is unchanged (still PRIVATE).
	art, _, _ := srv.Store.GetArtifact(slug)
	if art.GetVisibility() != artifactav1.Visibility_VISIBILITY_PRIVATE {
		t.Fatalf("visibility changed despite 400: got %v", art.GetVisibility())
	}
}

// "public" is an accepted alias for the no-login, VPN-gated "link" level: people
// arriving from other tools reach for "public". It maps to VISIBILITY_LINK.
func TestSetVisibilityPublicAliasesToLink(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const slug = "seededslug6"
	seedArtifact(t, srv, slug, owner.GetSub())

	req := httptest.NewRequest(http.MethodPatch, "/artifacts/"+slug+"/visibility",
		strings.NewReader(`{"visibility":"public"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("public alias: got %d, want 200 (body %q)", rr.Code, rr.Body.String())
	}
	art, ok, err := srv.Store.GetArtifact(slug)
	if err != nil || !ok {
		t.Fatalf("GetArtifact(%q): ok=%v err=%v", slug, ok, err)
	}
	if art.GetVisibility() != artifactav1.Visibility_VISIBILITY_LINK {
		t.Fatalf("visibility: got %v, want LINK (public alias)", art.GetVisibility())
	}
}
