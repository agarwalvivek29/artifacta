package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/api"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
	"github.com/coreos/go-oidc/v3/oidc"
)

func TestFreshTokenPrefersAPIKey(t *testing.T) {
	t.Setenv("ARTIFACTA_HOME", t.TempDir())
	// A still-valid human login is present; the API key must win anyway.
	c := config.Config{AccessToken: "human-id-token", AccessTokenExpiry: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	t.Setenv("ARTIFACTA_API_KEY", " afa_abc123 ")
	if tok, err := freshToken(&c); err != nil || tok != "afa_abc123" {
		t.Fatalf("freshToken = %q, %v; want the API key", tok, err)
	}
	t.Setenv("ARTIFACTA_API_KEY", "not-a-key")
	if _, err := freshToken(&c); err == nil {
		t.Fatal("freshToken accepted an ARTIFACTA_API_KEY without the afa_ prefix")
	}
}

func TestLoginScopesAddsGroupsOnlyWhenAdvertised(t *testing.T) {
	for _, advertised := range []bool{true, false} {
		srv := httptest.NewServer(nil)
		mux := http.NewServeMux()
		mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
			scopes := []string{"openid", "email", "profile", "offline_access"}
			if advertised {
				scopes = append(scopes, "groups")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": srv.URL, "authorization_endpoint": srv.URL + "/a", "token_endpoint": srv.URL + "/t",
				"jwks_uri": srv.URL + "/j", "scopes_supported": scopes,
			})
		})
		srv.Config.Handler = mux
		p, err := oidc.NewProvider(context.Background(), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(loginScopes(p), " ")
		if hasGroups := strings.Contains(got, "groups"); hasGroups != advertised {
			t.Errorf("advertised=%v: scopes = %q", advertised, got)
		}
		if !strings.Contains(got, "offline_access") {
			t.Errorf("scopes lost offline_access: %q", got)
		}
		srv.Close()
	}
	if len(cliScopes) != 4 {
		t.Fatalf("loginScopes mutated cliScopes: %v", cliScopes)
	}
}

// tokenAuth stands in for OIDC: the bearer "admin-token" is an admin.
type tokenAuth struct{}

func (tokenAuth) Identify(r *http.Request) (*artifactav1.Identity, bool) {
	if r.Header.Get("Authorization") == "Bearer admin-token" {
		return &artifactav1.Identity{Sub: "00uadmin", Email: "ops@example.com", Groups: []string{"DevOps-Engineering-L1"}, GroupsClaimPresent: true}, true
	}
	return nil, false
}

// run executes a CLI command, returning its stdout and error (reuses the
// package's captureStdout helper from upgrade_test.go).
func run(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = fn() })
	return out, err
}

// The admin + bot journey through the real CLI commands against a real server.
func TestServiceAccountCommandsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	st, err := infra.NewFileStore(filepath.Join(dir, "meta"))
	if err != nil {
		t.Fatal(err)
	}
	bl, err := infra.NewBlobFS(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{Store: st, Blob: bl, Auth: &api.APIKeyAuth{Inner: tokenAuth{}, Store: st},
		SAAdminGroups: []string{"DevOps-Engineering-L1"}}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	srv.BaseURL = ts.URL

	t.Setenv("ARTIFACTA_HOME", t.TempDir())
	if err := config.Save(config.Config{BaseURL: ts.URL, LoggedIn: true, AccessToken: "admin-token",
		AccessTokenExpiry: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, func() error { return Run([]string{"sa", "create", "ci", "--description", "nightly"}) }); err != nil {
		t.Fatalf("sa create: %v", err)
	}
	out, err := run(t, func() error { return Run([]string{"sa", "key", "create", "ci", "--ttl", "30d", "--json"}) })
	if err != nil {
		t.Fatalf("sa key create: %v", err)
	}
	var key struct {
		ID     string `json:"id"`
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal([]byte(out), &key); err != nil || !strings.HasPrefix(key.APIKey, "afa_") {
		t.Fatalf("key create output %q (%v)", out, err)
	}
	out, err = run(t, func() error { return Run([]string{"sa", "list"}) })
	if err != nil || !strings.Contains(out, "ci") || !strings.Contains(out, "nightly") {
		t.Fatalf("sa list = %q, %v", out, err)
	}

	// The bot: the API key overrides the stored admin login.
	t.Setenv("ARTIFACTA_API_KEY", key.APIKey)
	out, err = run(t, whoami)
	if err != nil || !strings.Contains(out, "sa:ci") {
		t.Fatalf("whoami as bot = %q, %v", out, err)
	}
	page := filepath.Join(t.TempDir(), "report.html")
	_ = os.WriteFile(page, []byte("<h1>nightly</h1>"), 0o600)
	if _, err := run(t, func() error { return Run([]string{"publish", page}) }); err != nil {
		t.Fatalf("publish as bot: %v", err)
	}
	arts, _ := st.ListByOwner("sa:ci")
	if len(arts) != 1 {
		t.Fatalf("bot owns %d artifacts, want 1", len(arts))
	}

	// The admin revokes the key; the bot is cut off.
	t.Setenv("ARTIFACTA_API_KEY", "")
	out, err = run(t, func() error { return Run([]string{"sa", "key", "list", "ci"}) })
	if err != nil || !strings.Contains(out, key.ID) || strings.Contains(out, key.APIKey) {
		t.Fatalf("sa key list = %q, %v", out, err)
	}
	if _, err := run(t, func() error { return Run([]string{"sa", "key", "revoke", "ci", key.ID}) }); err != nil {
		t.Fatalf("sa key revoke: %v", err)
	}
	t.Setenv("ARTIFACTA_API_KEY", key.APIKey)
	if _, err := run(t, func() error { return Run([]string{"publish", page}) }); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("publish with a revoked key: err = %v, want 401", err)
	}

	t.Setenv("ARTIFACTA_API_KEY", "")
	if _, err := run(t, func() error { return Run([]string{"sa", "disable", "ci"}) }); err != nil {
		t.Fatalf("sa disable: %v", err)
	}
	if err := Run([]string{"sa", "key", "create", "ci"}); err == nil || !strings.Contains(err.Error(), "--ttl") {
		t.Fatalf("key create without --ttl: err = %v", err)
	}
}

func TestAPIKeyForcesRemoteTarget(t *testing.T) {
	c := config.Config{BaseURL: "http://localhost:8080"} // not logged in, localhost
	t.Setenv("ARTIFACTA_API_KEY", "")
	if remoteTarget(c) {
		t.Fatal("logged-out localhost config treated as remote")
	}
	t.Setenv("ARTIFACTA_API_KEY", "afa_x")
	if !remoteTarget(c) {
		t.Fatal("ARTIFACTA_API_KEY set but the CLI would publish to the local store")
	}
}
