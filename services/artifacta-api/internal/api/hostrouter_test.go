package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const testRoot = "artifacta.genorim.xyz"

// subdomainURL derives scheme + any non-default port from BaseURL so the reported
// URL is actually reachable behind a non-443 proxy or in local dev.
func TestSubdomainURL(t *testing.T) {
	cases := []struct {
		baseURL, root, sub, want string
	}{
		{"https://artifacta.genorim.xyz", testRoot, "demo", "https://demo.artifacta.genorim.xyz"},
		{"https://artifacta.genorim.xyz:443", testRoot, "demo", "https://demo.artifacta.genorim.xyz"}, // std port dropped
		{"http://gridlords.dev:8080", "gridlords.dev", "demo", "http://demo.gridlords.dev:8080"},      // non-std port kept
		{"http://gridlords.dev:80", "gridlords.dev", "demo", "http://demo.gridlords.dev"},             // std http port dropped
		{"", testRoot, "demo", "https://demo.artifacta.genorim.xyz"},                                  // unparseable → https default
		{"https://x", "", "demo", ""},   // hosting disabled
		{"https://x", testRoot, "", ""}, // no sub
	}
	for _, c := range cases {
		s := &Server{BaseURL: c.baseURL, RootDomain: c.root}
		if got := s.subdomainURL(c.sub); got != c.want {
			t.Errorf("subdomainURL(base=%q root=%q sub=%q) = %q, want %q", c.baseURL, c.root, c.sub, got, c.want)
		}
	}
}

// newSubdomainServer is newTestServer with subdomain hosting (ADR-0017) enabled.
func newSubdomainServer(t *testing.T, auth Auth) *Server {
	t.Helper()
	srv := newTestServer(t, t.TempDir(), auth)
	srv.RootDomain = testRoot
	return srv
}

// seed writes an artifact + its v1 bytes straight into the store/blob, bypassing
// the publish pipeline so a test can pin visibility and content precisely.
func seed(t *testing.T, srv *Server, slug, owner string, vis artifactav1.Visibility, body string) {
	t.Helper()
	if err := srv.Blob.Put(slug, 1, strings.NewReader(body)); err != nil {
		t.Fatalf("blob put: %v", err)
	}
	if err := srv.Store.AddVersion(&artifactav1.ArtifactVersion{Slug: slug, N: 1, ContentType: "text/html; charset=utf-8", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatalf("add version: %v", err)
	}
	if err := srv.Store.PutArtifact(&artifactav1.Artifact{
		Slug: slug, OwnerSub: owner, Visibility: vis,
		ContentType: "text/html; charset=utf-8", LatestVersion: 1, CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatalf("put artifact: %v", err)
	}
}

func getHost(h http.Handler, host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "https://"+host+path, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// A LINK artifact is served, no login, at its slug subdomain root.
func TestSubdomain_linkServedAnonymously(t *testing.T) {
	srv := newSubdomainServer(t, fakeAuth{ok: false}) // anonymous
	seed(t, srv, "sluglink", "owner", artifactav1.Visibility_VISIBILITY_LINK, "<h1>hosted</h1>")
	h := srv.Routes()

	rr := getHost(h, "sluglink."+testRoot, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("anon GET slug subdomain: got %d, want 200 (body %q)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "hosted") {
		t.Errorf("body = %q, want the artifact bytes", rr.Body.String())
	}
}

// A PRIVATE artifact on its subdomain stays a not-leaking 404 to an anonymous
// caller — the subdomain is an address, never an authorization bypass.
func TestSubdomain_privateStillClosedToAnon(t *testing.T) {
	srv := newSubdomainServer(t, fakeAuth{ok: false}) // anonymous
	seed(t, srv, "slugpriv", "owner", artifactav1.Visibility_VISIBILITY_PRIVATE, "<h1>secret</h1>")
	h := srv.Routes()

	rr := getHost(h, "slugpriv."+testRoot, "/")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("anon GET private subdomain: got %d, want 404", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret") {
		t.Error("private artifact bytes leaked to an anonymous subdomain request")
	}
}

// A custom label resolves to its artifact's subdomain.
func TestSubdomain_customLabelResolves(t *testing.T) {
	srv := newSubdomainServer(t, fakeAuth{ok: false})
	seed(t, srv, "slugc", "owner", artifactav1.Visibility_VISIBILITY_LINK, "<h1>labelled</h1>")
	if ok, err := srv.Store.SetLabel("slugc", "coolpage"); err != nil || !ok {
		t.Fatalf("SetLabel: %v %v", ok, err)
	}
	h := srv.Routes()

	rr := getHost(h, "coolpage."+testRoot, "/")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "labelled") {
		t.Fatalf("GET custom-label subdomain: got %d body %q", rr.Code, rr.Body.String())
	}
}

func TestSubdomain_unknownAndApexAndBadPath(t *testing.T) {
	srv := newSubdomainServer(t, fakeAuth{ok: false})
	seed(t, srv, "slugok", "owner", artifactav1.Visibility_VISIBILITY_LINK, "<h1>ok</h1>")
	h := srv.Routes()

	// Unknown subdomain → 404 (don't leak which slugs/labels exist).
	if rr := getHost(h, "doesnotexist."+testRoot, "/"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown subdomain: got %d, want 404", rr.Code)
	}
	// Apex host passes through to normal path routing (health probe still works).
	if rr := getHost(h, testRoot, "/health"); rr.Code != http.StatusOK {
		t.Errorf("apex /health: got %d, want 200 (passthrough)", rr.Code)
	}
	// A non-hosting path on a valid subdomain → 404 (subdomains serve bytes only).
	if rr := getHost(h, "slugok."+testRoot, "/artifacts/slugok"); rr.Code != http.StatusNotFound {
		t.Errorf("subdomain app path: got %d, want 404", rr.Code)
	}
	// Specific version over the subdomain works.
	if rr := getHost(h, "slugok."+testRoot, "/v/1/raw"); rr.Code != http.StatusOK {
		t.Errorf("subdomain /v/1/raw: got %d, want 200", rr.Code)
	}
}

// setLabel: owner claims a valid label; invalid/reserved rejected 400; a taken
// label is 409; a non-owner is a not-leaking 404; anonymous is 401.
func TestSetLabelEndpoint(t *testing.T) {
	owner := &artifactav1.Identity{Sub: "owner", Email: "o@x"}
	srv := newSubdomainServer(t, fakeAuth{id: owner, ok: true})
	seed(t, srv, "s1", "owner", artifactav1.Visibility_VISIBILITY_LINK, "<h1>a</h1>")
	seed(t, srv, "s2", "owner", artifactav1.Visibility_VISIBILITY_LINK, "<h1>b</h1>")

	patch := func(auth Auth, slug, jsonBody string) *httptest.ResponseRecorder {
		srv.Auth = auth
		req := httptest.NewRequest(http.MethodPatch, "/artifacts/"+slug+"/label", strings.NewReader(jsonBody))
		rr := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rr, req)
		return rr
	}

	// Valid claim → 200 + subdomain_url.
	rr := patch(fakeAuth{id: owner, ok: true}, "s1", `{"label":"my-app"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid setLabel: got %d, want 200 (body %q)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Label        string `json:"label"`
		SubdomainURL string `json:"subdomain_url"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Label != "my-app" || resp.SubdomainURL != "https://my-app."+testRoot {
		t.Errorf("setLabel resp = %+v, want label my-app and subdomain_url https://my-app.%s", resp, testRoot)
	}

	// Reserved + malformed labels → 400.
	for _, bad := range []string{`{"label":"www"}`, `{"label":"UPPER"}`, `{"label":"-bad"}`, `{"label":"a.b"}`} {
		if rr := patch(fakeAuth{id: owner, ok: true}, "s1", bad); rr.Code != http.StatusBadRequest {
			t.Errorf("setLabel %s: got %d, want 400", bad, rr.Code)
		}
	}

	// Taken by another artifact → 409.
	if rr := patch(fakeAuth{id: owner, ok: true}, "s2", `{"label":"my-app"}`); rr.Code != http.StatusConflict {
		t.Errorf("taken label: got %d, want 409", rr.Code)
	}

	// Non-owner → 404 (not-leaking); anonymous → 401.
	if rr := patch(fakeAuth{id: &artifactav1.Identity{Sub: "someone-else"}, ok: true}, "s1", `{"label":"other"}`); rr.Code != http.StatusNotFound {
		t.Errorf("non-owner setLabel: got %d, want 404", rr.Code)
	}
	if rr := patch(fakeAuth{ok: false}, "s1", `{"label":"other"}`); rr.Code != http.StatusUnauthorized {
		t.Errorf("anon setLabel: got %d, want 401", rr.Code)
	}
}
