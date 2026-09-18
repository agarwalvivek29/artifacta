package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// putArtifact seeds an artifact directly into the store for dashboard tests.
func putArtifact(t *testing.T, srv *Server, slug, owner, title string, vis artifactav1.Visibility) {
	t.Helper()
	err := srv.Store.PutArtifact(&artifactav1.Artifact{
		Slug:        slug,
		OwnerSub:    owner,
		Title:       title,
		Visibility:  vis,
		ContentType: "text/html; charset=utf-8",
		CreatedAt:   timestamppb.Now(),
	})
	if err != nil {
		t.Fatalf("seed artifact %q: %v", slug, err)
	}
}

// entryFor returns the JS object literal for slug from the dashboard's
// window.__ARTIFACTS__ data island (each artifact is one {slug:"…",…,type:"…"}
// entry, rendered client-side into the card / list views). The test asserts an
// artifact carries the intended section type, replacing the old server-rendered
// <h2> section check.
func entryFor(t *testing.T, html, slug string) string {
	t.Helper()
	key := `slug:"` + slug + `"`
	i := strings.Index(html, key)
	if i < 0 {
		t.Fatalf("artifact %q not present in dashboard data island", slug)
	}
	start := strings.LastIndex(html[:i], "{")
	end := strings.Index(html[i:], "}")
	if start < 0 || end < 0 {
		t.Fatalf("could not bound data-island entry for %q", slug)
	}
	return html[start : i+end+1]
}

func TestDashboardAuthenticatedRendersThreeSections(t *testing.T) {
	dir := t.TempDir()
	caller := &artifactav1.Identity{Sub: "local:me", Email: "me@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: caller, ok: true})
	h := srv.Routes()

	// Mine: an owned PRIVATE artifact, title carries markup to prove escaping.
	putArtifact(t, srv, "mineslug1", caller.GetSub(), "<script>alert(1)</script>", artifactav1.Visibility_VISIBILITY_PRIVATE)
	// Shared with me: an INVITED artifact owned by someone else, granted to caller.
	putArtifact(t, srv, "sharedslug1", "local:other", "Shared Doc", artifactav1.Visibility_VISIBILITY_INVITED)
	if err := srv.Store.AddGrant(&artifactav1.Grant{
		Slug: "sharedslug1", GranteeSub: caller.GetSub(), GrantedBy: "local:other", CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatalf("add grant: %v", err)
	}
	// Org: an ORG artifact owned by someone else.
	putArtifact(t, srv, "orgslug1", "local:other", "Org Doc", artifactav1.Visibility_VISIBILITY_ORG)
	// An ORG artifact owned by the caller must NOT be duplicated into Org.
	putArtifact(t, srv, "myorgslug", caller.GetSub(), "My Org Doc", artifactav1.Visibility_VISIBILITY_ORG)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("dashboard: got %d, want 200 (body %q)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()

	// Each artifact lands in the data island tagged with its section type; the
	// controller renders that into the Mine / Shared / Org views.
	if e := entryFor(t, body, "mineslug1"); !strings.Contains(e, `type:"mine"`) {
		t.Fatalf("owned artifact not tagged type:mine:\n%s", e)
	}
	if e := entryFor(t, body, "sharedslug1"); !strings.Contains(e, `type:"shared"`) {
		t.Fatalf("granted artifact not tagged type:shared:\n%s", e)
	}
	if e := entryFor(t, body, "orgslug1"); !strings.Contains(e, `type:"org"`) {
		t.Fatalf("org artifact not tagged type:org:\n%s", e)
	}
	// The caller's own org artifact appears under Mine, not duplicated in Org.
	if e := entryFor(t, body, "myorgslug"); !strings.Contains(e, `type:"mine"`) {
		t.Fatalf("caller's own org artifact not tagged type:mine:\n%s", e)
	}
	if got := strings.Count(body, `slug:"myorgslug"`); got != 1 {
		t.Fatalf("caller's own org artifact appears %d times, want exactly 1 (no Org duplicate)", got)
	}

	// html/template must escape the user-controlled title in the JS string
	// context — the raw <script> tag must NOT appear; the title content survives
	// only in escaped form.
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("artifact title was NOT escaped (raw <script> present):\n%s", body)
	}
	if !strings.Contains(body, `alert(1)`) {
		t.Fatalf("escaped title content not found in dashboard data island:\n%s", body)
	}
}

func TestDashboardUnauthenticatedRendersSignin(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t, dir, fakeAuth{ok: false})
	h := srv.Routes()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("sign-in: got %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "ArtifactA") {
		t.Fatalf("sign-in landing not rendered (brand missing):\n%s", body)
	}
	if !strings.Contains(body, `href="/login"`) {
		t.Fatalf("sign-in page missing link to /login:\n%s", body)
	}
	// The dashboard must not render for an unauthenticated caller.
	if strings.Contains(body, "__ARTIFACTS__") || strings.Contains(body, `id="board"`) {
		t.Fatalf("dashboard leaked to unauthenticated caller:\n%s", body)
	}
}

// TestDashboardUploadFormToggle verifies the upload form appears only when the
// ARTIFACTA_UPLOAD_UI toggle is on (ADR-0024), and never for an anonymous caller.
func TestDashboardUploadFormToggle(t *testing.T) {
	caller := &artifactav1.Identity{Sub: "local:me", Email: "me@localhost"}
	const marker = `enctype="multipart/form-data"`

	get := func(srv *Server) string {
		rr := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		return rr.Body.String()
	}

	on := newTestServer(t, t.TempDir(), fakeAuth{id: caller, ok: true})
	on.UploadUI = true
	if body := get(on); !strings.Contains(body, marker) || !strings.Contains(body, `id="uploadFab"`) || !strings.Contains(body, "Upload an artifact") {
		t.Fatalf("upload FAB/dialog missing with toggle on")
	}

	off := newTestServer(t, t.TempDir(), fakeAuth{id: caller, ok: true})
	if body := get(off); strings.Contains(body, marker) || strings.Contains(body, `id="uploadFab"`) {
		t.Fatalf("upload FAB/dialog rendered with toggle off")
	}

	anon := newTestServer(t, t.TempDir(), fakeAuth{ok: false})
	anon.UploadUI = true
	if body := get(anon); strings.Contains(body, marker) {
		t.Fatalf("upload form leaked to anonymous caller")
	}
}
