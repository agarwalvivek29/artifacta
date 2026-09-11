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

// TestMeReturnsIdentityWhenAuthed: GET /me echoes the caller's identity so the
// CLI's `doctor`/`whoami` can prove the token is accepted.
func TestMeReturnsIdentityWhenAuthed(t *testing.T) {
	who := &artifactav1.Identity{Sub: "local:me", Email: "me@localhost"}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: who, ok: true})
	h := srv.Routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /me authed: got %d, want 200", rr.Code)
	}
	var out struct{ Sub, Email string }
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	if out.Sub != "local:me" || out.Email != "me@localhost" {
		t.Fatalf("/me = %+v, want sub/email of the caller", out)
	}
}

// TestMeRejectsUnauthenticated: GET /me fails closed with 401.
func TestMeRejectsUnauthenticated(t *testing.T) {
	srv := newTestServer(t, t.TempDir(), fakeAuth{ok: false})
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /me anon: got %d, want 401", rr.Code)
	}
}

// TestListArtifactsOwnerScoped: GET /artifacts returns the caller's own
// artifacts (and only those), powering remote `ls`.
func TestListArtifactsOwnerScoped(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})

	// Two of the caller's own, one someone else's.
	for _, s := range []string{"mine1", "mine2"} {
		if err := srv.Store.PutArtifact(&artifactav1.Artifact{
			Slug: s, OwnerSub: owner.GetSub(), Title: s, Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE,
			LatestVersion: 1, CreatedAt: timestamppb.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.Store.PutArtifact(&artifactav1.Artifact{
		Slug: "theirs", OwnerSub: "local:other", Title: "theirs", LatestVersion: 1, CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/artifacts", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /artifacts: got %d, want 200", rr.Code)
	}
	var rows []struct {
		Slug string `json:"slug"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode /artifacts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("GET /artifacts returned %d rows, want 2 (owner-scoped)", len(rows))
	}
	for _, r := range rows {
		if r.Slug == "theirs" {
			t.Fatal("GET /artifacts leaked another owner's artifact")
		}
		if r.URL != "https://here.now/a/"+r.Slug {
			t.Fatalf("row url = %q, want absolute artifact link", r.URL)
		}
	}
}

func TestListArtifactsRejectsUnauthenticated(t *testing.T) {
	srv := newTestServer(t, t.TempDir(), fakeAuth{ok: false})
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/artifacts", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /artifacts anon: got %d, want 401", rr.Code)
	}
}

// TestCLILoginConfigLocalMode: with no OIDC provider, discovery advertises the
// local dev auth mode, unauthenticated.
func TestCLILoginConfigLocalMode(t *testing.T) {
	srv := newTestServer(t, t.TempDir(), fakeAuth{ok: false}) // OIDC nil
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/.well-known/artifacta-cli", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("discovery (local): got %d, want 200", rr.Code)
	}
	var cfg CLIAuthConfig
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	if cfg.Auth != "local" {
		t.Fatalf("discovery auth = %q, want local", cfg.Auth)
	}
}

// TestCLILoginConfigOIDCMode: with an OIDC provider, discovery exposes issuer +
// client_id + scopes (incl. offline_access) and NEVER a secret.
func TestCLILoginConfigOIDCMode(t *testing.T) {
	idp := newMockIDP(t, "test-client", "s", "e@x.co")
	p := newTestProvider(t, idp)
	srv := newTestServer(t, t.TempDir(), p)
	srv.OIDC = p

	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/.well-known/artifacta-cli", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("discovery (oidc): got %d, want 200", rr.Code)
	}
	// The raw body must not carry the client secret.
	if body := rr.Body.String(); strings.Contains(body, "secret") {
		t.Fatalf("discovery body leaked a secret: %s", body)
	}
	var cfg CLIAuthConfig
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	if cfg.Auth != "oidc" || cfg.Issuer != idp.server.URL || cfg.ClientID != "test-client" {
		t.Fatalf("discovery = %+v, want oidc/issuer/client-id from the provider", cfg)
	}
	// The fixed CLI redirect URI (set by newTestProvider) is advertised so the CLI
	// binds the exact port the IdP registered.
	if cfg.RedirectURI != "http://127.0.0.1:53682/callback" {
		t.Fatalf("discovery redirect_uri = %q, want the provider's fixed loopback URI", cfg.RedirectURI)
	}
	hasOffline := false
	for _, s := range cfg.Scopes {
		if s == "offline_access" {
			hasOffline = true
		}
	}
	if !hasOffline {
		t.Fatalf("discovery scopes %v missing offline_access (needed for refresh tokens)", cfg.Scopes)
	}
}

// TestVersionEndpoint: GET /version is unauthenticated and exposes the deployed
// version + compatibility contract for the CLI's upgrade check.
func TestVersionEndpoint(t *testing.T) {
	srv := newTestServer(t, t.TempDir(), fakeAuth{ok: false}) // no auth needed
	srv.Version = "1.2.3"

	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/version", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /version: got %d, want 200 (unauthenticated)", rr.Code)
	}
	var out struct {
		Version      string   `json:"version"`
		MinCLI       string   `json:"min_cli_version"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode /version: %v", err)
	}
	if out.Version != "1.2.3" || out.MinCLI == "" || len(out.Capabilities) == 0 {
		t.Fatalf("/version = %+v, want the deployed version + a compatibility contract", out)
	}
}

// A Server with no Version set still reports a value (dev), never empty.
func TestVersionEndpointDefaultsToDev(t *testing.T) {
	srv := newTestServer(t, t.TempDir(), fakeAuth{ok: false})
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/version", nil))
	if !strings.Contains(rr.Body.String(), `"version":"dev"`) {
		t.Fatalf("unset version should default to dev: %s", rr.Body.String())
	}
}

// CLILoginConfig is reachable via the provider directly too (unit-level).
func TestCLILoginConfigAccessor(t *testing.T) {
	idp := newMockIDP(t, "cid", "s", "e@x.co")
	p := newTestProvider(t, idp)
	if got := p.CLILoginConfig().ClientID; got != "cid" {
		t.Fatalf("CLILoginConfig client id = %q, want cid", got)
	}
}
