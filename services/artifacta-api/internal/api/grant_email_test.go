package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

// End-to-end of the Share panel's invite-by-email path against the real store:
// owner invites by email → a caller with that email can view → owner revokes →
// the caller is denied again. Uses seed() and fakeAuth from hostrouter_test.go.
func TestInviteByEmailFlow(t *testing.T) {
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@corp.com"}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: owner, ok: true})
	seed(t, srv, "doc", "local:owner", artifactav1.Visibility_VISIBILITY_INVITED, "<h1>hi</h1>")

	do := func(auth Auth, method, target, body string) *httptest.ResponseRecorder {
		srv.Auth = auth
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, target, nil)
		} else {
			r = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		rr := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rr, r)
		return rr
	}
	sam := fakeAuth{id: &artifactav1.Identity{Sub: "sam-sub", Email: "sam@corp.com"}, ok: true}
	stranger := fakeAuth{id: &artifactav1.Identity{Sub: "x", Email: "eve@corp.com"}, ok: true}
	viewRaw := func(auth Auth) int { return do(auth, http.MethodGet, "/a/doc/raw", "").Code }

	// Before any grant: sam cannot view an INVITED artifact.
	if code := viewRaw(sam); code != http.StatusNotFound {
		t.Fatalf("pre-invite view: got %d, want 404", code)
	}
	// Owner invites sam by email.
	if rr := do(fakeAuth{id: owner, ok: true}, http.MethodPost, "/artifacts/doc/grants", `{"email":"sam@corp.com"}`); rr.Code != http.StatusCreated {
		t.Fatalf("invite: got %d, want 201", rr.Code)
	}
	// Duplicate invite is an idempotent 201.
	if rr := do(fakeAuth{id: owner, ok: true}, http.MethodPost, "/artifacts/doc/grants", `{"email":"SAM@corp.com"}`); rr.Code != http.StatusCreated {
		t.Fatalf("duplicate invite: got %d, want 201", rr.Code)
	}
	// Now sam (a different subject, matching email) can view; a stranger cannot.
	if code := viewRaw(sam); code != http.StatusOK {
		t.Fatalf("invited view: got %d, want 200", code)
	}
	if code := viewRaw(stranger); code != http.StatusNotFound {
		t.Fatalf("stranger view: got %d, want 404", code)
	}
	// Grantees + owner_email show in metadata for the owner (the Share panel's
	// people list needs both), but not for a viewer.
	var ownerMeta struct {
		Grantees   []struct{ ID, Email string } `json:"grantees"`
		OwnerEmail string                       `json:"owner_email"`
	}
	_ = json.Unmarshal(do(fakeAuth{id: owner, ok: true}, http.MethodGet, "/artifacts/doc", "").Body.Bytes(), &ownerMeta)
	if len(ownerMeta.Grantees) != 1 || ownerMeta.Grantees[0].Email != "sam@corp.com" {
		t.Fatalf("owner metadata grantees = %+v, want one sam@corp.com", ownerMeta.Grantees)
	}
	if ownerMeta.OwnerEmail != "owner@corp.com" {
		t.Fatalf("owner metadata owner_email = %q, want owner@corp.com", ownerMeta.OwnerEmail)
	}
	var viewerMeta struct {
		Grantees   []any  `json:"grantees"`
		OwnerEmail string `json:"owner_email"`
	}
	_ = json.Unmarshal(do(sam, http.MethodGet, "/artifacts/doc", "").Body.Bytes(), &viewerMeta)
	if len(viewerMeta.Grantees) != 0 {
		t.Errorf("viewer should not see grantee list, got %+v", viewerMeta.Grantees)
	}
	if viewerMeta.OwnerEmail != "" {
		t.Errorf("viewer should not see owner_email, got %q", viewerMeta.OwnerEmail)
	}
	// Owner revokes; sam is denied again.
	if rr := do(fakeAuth{id: owner, ok: true}, http.MethodDelete, "/artifacts/doc/grants/"+url.PathEscape("sam@corp.com"), ""); rr.Code != http.StatusOK {
		t.Fatalf("revoke: got %d, want 200", rr.Code)
	}
	if code := viewRaw(sam); code != http.StatusNotFound {
		t.Fatalf("post-revoke view: got %d, want 404", code)
	}
	// Revoking an unknown grantee is a 404.
	if rr := do(fakeAuth{id: owner, ok: true}, http.MethodDelete, "/artifacts/doc/grants/nobody@corp.com", ""); rr.Code != http.StatusNotFound {
		t.Errorf("revoke unknown: got %d, want 404", rr.Code)
	}
}

func TestAddGrant_rejectsJunkEmailAndEmptyGrantee(t *testing.T) {
	owner := &artifactav1.Identity{Sub: "o", Email: "o@corp.com"}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: owner, ok: true})
	seed(t, srv, "doc", "o", artifactav1.Visibility_VISIBILITY_INVITED, "<h1>hi</h1>")
	post := func(body string) int {
		r := httptest.NewRequest(http.MethodPost, "/artifacts/doc/grants", strings.NewReader(body))
		rr := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rr, r)
		return rr.Code
	}
	if post(`{"email":"not-an-email"}`) != http.StatusBadRequest {
		t.Error("junk email should be 400")
	}
	if post(`{}`) != http.StatusBadRequest {
		t.Error("no grantee should be 400")
	}
}
