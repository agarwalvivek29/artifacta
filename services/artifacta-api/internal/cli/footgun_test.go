package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
)

// TestPublishRemoteTargetWithoutTokenErrors is the critical footgun regression:
// when the CLI is pointed at a deployment but is logged out, publish must ERROR
// and tell the user to log in — never silently write the local store and print a
// localhost link for what the user believes is a remote publish.
func TestPublishRemoteTargetWithoutTokenErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARTIFACTA_HOME", home)

	c := config.Default()
	c.BaseURL = "https://remote.example" // non-localhost → remoteTarget, but no tokens
	if err := config.Save(c); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(file, []byte("<h1>hi</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := publish([]string{file})
	if err == nil {
		t.Fatal("publish against a remote target while logged out: expected an error, got nil (silent local write is the footgun)")
	}
	if !strings.Contains(err.Error(), "login") {
		t.Fatalf("publish error = %q, want a 'run: artifacta login' prompt", err)
	}
	// Nothing was written to a local blob store.
	if entries, _ := os.ReadDir(filepath.Join(c.DataDir, "blobs")); len(entries) != 0 {
		t.Fatalf("publish wrote %d local blobs despite a remote target", len(entries))
	}
}

// TestFetchCLIConfig decodes the discovery endpoint so `login <url>` needs
// nothing else.
func TestFetchCLIConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/artifacta-cli" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth":      "oidc",
			"issuer":    "https://idp.example",
			"client_id": "cli-client",
			"scopes":    []string{"openid", "offline_access"},
		})
	}))
	defer srv.Close()

	cfg, err := fetchCLIConfig(srv.URL)
	if err != nil {
		t.Fatalf("fetchCLIConfig: %v", err)
	}
	if cfg.Auth != "oidc" || cfg.Issuer != "https://idp.example" || cfg.ClientID != "cli-client" {
		t.Fatalf("fetchCLIConfig = %+v, want the advertised oidc config", cfg)
	}
}

// TestAuditVerifyRemoteTargetErrors: audit verify is a local-store operation, so
// pointed at a deployment it must say so, not verify an unrelated local log.
func TestAuditVerifyRemoteTargetErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARTIFACTA_HOME", home)
	c := config.Default()
	c.LoggedIn = true
	c.BaseURL = "https://remote.example"
	if err := config.Save(c); err != nil {
		t.Fatal(err)
	}
	err := audit([]string{"verify"})
	if err == nil || !strings.Contains(err.Error(), "local store") {
		t.Fatalf("audit verify against a remote target = %v, want a 'local store' error", err)
	}
}
