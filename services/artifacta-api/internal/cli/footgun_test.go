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

// TestShareLocalModeErrors is the D3 regression guard: `share` is now remote-only,
// so in pure local-dev mode (localhost, logged out → not a remote target) it must
// ERROR with a login prompt and never write the local store — the local-dev share
// path (which wrongly stored an email as a subject) was removed.
func TestShareLocalModeErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARTIFACTA_HOME", home)

	c := config.Default()
	c.Sub = "local:me" // logged into local dev, but no remote target
	if err := config.Save(c); err != nil {
		t.Fatal(err)
	}

	err := share([]string{"some-slug", "friend@example.com"})
	if err == nil {
		t.Fatal("share in local-dev mode: expected an error (remote-only), got nil")
	}
	if !strings.Contains(err.Error(), "remote server") {
		t.Fatalf("share error = %q, want a 'requires a remote server' prompt", err)
	}
	// The removed local path must not have written a grant/artifact to the store.
	if entries, _ := os.ReadDir(filepath.Join(c.DataDir, "meta")); len(entries) != 0 {
		t.Fatalf("share wrote %d local metadata entries despite being remote-only", len(entries))
	}
}

// TestSharingCommandsRequireRemoteTarget: every sharing/mutation command errors
// with the login prompt when there is no remote target, matching `versions`.
func TestSharingCommandsRequireRemoteTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARTIFACTA_HOME", home)
	c := config.Default()
	c.Sub = "local:me" // local dev, not a remote target
	if err := config.Save(c); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		run  func() error
	}{
		{"visibility", func() error { return visibility([]string{"s", "org"}) }},
		{"unshare", func() error { return unshare([]string{"s", "who"}) }},
		{"label", func() error { return label([]string{"s", "my-app"}) }},
		{"comment add", func() error { return comment([]string{"add", "s", "hi"}) }},
		{"comment ls", func() error { return comment([]string{"ls", "s"}) }},
		{"comment resolve", func() error { return comment([]string{"resolve", "s", "id"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), "remote server") {
				t.Fatalf("%s = %v, want a 'requires a remote server' error", tc.name, err)
			}
		})
	}
}

// TestVisibilityRejectsInvalidLevel: an invalid level is rejected before any
// network call (so the guard order is: validate → remoteTarget).
func TestVisibilityRejectsInvalidLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARTIFACTA_HOME", home)
	if err := config.Save(config.Default()); err != nil {
		t.Fatal(err)
	}
	err := visibility([]string{"some-slug", "bogus"})
	if err == nil || !strings.Contains(err.Error(), "invalid visibility") {
		t.Fatalf("visibility bogus = %v, want an 'invalid visibility' error", err)
	}
}

// TestCommentUnknownSubcommand: the comment sub-router rejects an unknown or
// missing subcommand with a usage error before touching config.
func TestCommentUnknownSubcommand(t *testing.T) {
	if err := comment([]string{"frobnicate", "s"}); err == nil || !strings.Contains(err.Error(), "unknown comment subcommand") {
		t.Fatalf("comment frobnicate = %v, want an 'unknown comment subcommand' error", err)
	}
	if err := comment(nil); err == nil {
		t.Fatalf("comment with no subcommand: expected usage error, got nil")
	}
}

// TestAuditVerifyRemoteTakesRemotePath: audit verify against a deployment now
// verifies the caller's own rows over the network (GET /audit) instead of
// checking an unrelated local log. Without usable credentials it fails on the
// auth step — proving it took the remote path, not the old "local store" bail.
func TestAuditVerifyRemoteTakesRemotePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARTIFACTA_HOME", home)
	c := config.Default()
	c.LoggedIn = true
	c.BaseURL = "https://remote.example"
	if err := config.Save(c); err != nil {
		t.Fatal(err)
	}
	err := audit([]string{"verify"})
	if err == nil {
		t.Fatal("audit verify against a remote target with no creds: expected an error, got nil")
	}
	if strings.Contains(err.Error(), "local store") {
		t.Fatalf("audit verify should take the remote path now, got the old local-store error: %v", err)
	}
}
