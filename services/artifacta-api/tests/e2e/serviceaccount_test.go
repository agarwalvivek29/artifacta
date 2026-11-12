package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/api"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
)

// TestEndToEndServiceAccountFlow drives the service-account journey (ADR-0028)
// over a live server: an admin creates an account and mints a key; the bot
// publishes with the key, owns the artifact and makes it org-visible; another
// user views it; revoking the key cuts the bot off; the audit chain verifies.
func TestEndToEndServiceAccountFlow(t *testing.T) {
	blob, err := infra.NewBlobFS(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	metaDir := filepath.Join(t.TempDir(), "meta")
	store, err := infra.NewFileStore(metaDir)
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{
		Store: store, Blob: blob, BaseURL: "https://here.now",
		Auth:          &api.APIKeyAuth{Inner: headerAuth{}, Store: store},
		SAAdminEmails: []string{alice + "@corp.example"}, // headerAuth emails are <sub>@corp.example
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	h := &harness{ts: ts, client: ts.Client(), metaDir: metaDir}

	// bot issues a request authenticated by the API key alone.
	bot := func(method, path, contentType, body, key string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Admin (alice, email allow-listed) creates the account and mints a key.
	if code, body := h.do(t, http.MethodPost, "/serviceaccounts", alice, "application/json", `{"name":"ci-reports"}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	if code, _ := h.do(t, http.MethodPost, "/serviceaccounts", bob, "application/json", `{"name":"sneaky"}`); code != http.StatusForbidden {
		t.Fatalf("non-admin create: got %d, want 403", code)
	}
	code, body := h.do(t, http.MethodPost, "/serviceaccounts?name=ci-reports", alice, "application/json", `{"ttl":"90d"}`)
	if code != http.StatusCreated {
		t.Fatalf("key create: %d %s", code, body)
	}
	var key struct {
		ID     string `json:"id"`
		APIKey string `json:"api_key"`
	}
	_ = json.Unmarshal([]byte(body), &key)

	// The bot publishes, owns it, and opens it to the org.
	code, body = bot(http.MethodPost, "/artifacts?title=Nightly", "text/html", "<h1>nightly</h1>", key.APIKey)
	if code != http.StatusCreated {
		t.Fatalf("bot publish: %d %s", code, body)
	}
	var pub struct{ Slug string }
	_ = json.Unmarshal([]byte(body), &pub)
	rawPath := "/a/" + pub.Slug + "/raw"
	if code, _ := h.do(t, http.MethodGet, rawPath, carol, "", ""); code != http.StatusNotFound {
		t.Fatalf("carol before org: got %d, want 404 (private)", code)
	}
	if code, body := bot(http.MethodPatch, "/artifacts/"+pub.Slug+"/visibility", "application/json", `{"visibility":"org"}`, key.APIKey); code != http.StatusOK {
		t.Fatalf("bot set org: %d %s", code, body)
	}
	if code, body := h.do(t, http.MethodGet, rawPath, carol, "", ""); code != http.StatusOK || !strings.Contains(body, "nightly") {
		t.Fatalf("carol after org: %d %q", code, body)
	}
	// The human admin is not the owner and can't change the bot's artifact.
	if code, _ := h.do(t, http.MethodPatch, "/artifacts/"+pub.Slug+"/visibility", alice, "application/json", `{"visibility":"private"}`); code != http.StatusNotFound {
		t.Fatalf("admin editing bot artifact: got %d, want 404 (owner only)", code)
	}

	// Revoke: the very next bot call is refused.
	if code, body := h.do(t, http.MethodDelete, "/serviceaccounts?name=ci-reports&key_id="+key.ID, alice, "", ""); code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", code, body)
	}
	if code, _ := bot(http.MethodPost, "/artifacts", "text/html", "<p>again</p>", key.APIKey); code != http.StatusUnauthorized {
		t.Fatalf("publish with revoked key: got %d, want 401", code)
	}

	if verified, err := infra.VerifyAuditLog(metaDir); err != nil || verified < 5 {
		t.Fatalf("audit chain: verified %d, err %v", verified, err)
	}
}
