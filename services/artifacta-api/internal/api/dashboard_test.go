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

// sectionBody returns the slice of html between the <h2>heading</h2> marker and
// the next <h2> (or end of doc), so a test can assert an artifact lands in the
// intended dashboard section.
func sectionBody(t *testing.T, html, heading string) string {
	t.Helper()
	marker := "<h2>" + heading + "</h2>"
	i := strings.Index(html, marker)
	if i < 0 {
		t.Fatalf("section heading %q not found in dashboard", heading)
	}
	rest := html[i+len(marker):]
	if j := strings.Index(rest, "<h2>"); j >= 0 {
		return rest[:j]
	}
	return rest
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

	mine := sectionBody(t, body, "Mine")
	shared := sectionBody(t, body, "Shared with me")
	org := sectionBody(t, body, "Org")

	if !strings.Contains(mine, `href="/a/mineslug1"`) {
		t.Fatalf("owned artifact missing from Mine section:\n%s", mine)
	}
	if !strings.Contains(shared, `href="/a/sharedslug1"`) {
		t.Fatalf("granted artifact missing from Shared section:\n%s", shared)
	}
	if !strings.Contains(org, `href="/a/orgslug1"`) {
		t.Fatalf("org artifact missing from Org section:\n%s", org)
	}
	// The caller's own org artifact appears under Mine, not duplicated in Org.
	if !strings.Contains(mine, `href="/a/myorgslug"`) {
		t.Fatalf("caller's own org artifact missing from Mine:\n%s", mine)
	}
	if strings.Contains(org, `href="/a/myorgslug"`) {
		t.Fatalf("caller's own org artifact duplicated into Org:\n%s", org)
	}

	// html/template must escape the user-controlled title — the raw tag must NOT
	// appear, only its escaped form.
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("artifact title was NOT escaped (raw <script> present):\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("escaped title not found in dashboard:\n%s", body)
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
	if strings.Contains(body, "<h2>Mine</h2>") {
		t.Fatalf("dashboard leaked to unauthenticated caller:\n%s", body)
	}
}
