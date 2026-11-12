package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
)

// switchAuth lets a test change who the inner provider resolves between calls.
type switchAuth struct{ id *artifactav1.Identity }

func (s *switchAuth) Identify(*http.Request) (*artifactav1.Identity, bool) {
	if s.id == nil {
		return nil, false
	}
	return s.id, true
}

var (
	opsAdmin = &artifactav1.Identity{Sub: "00uadmin", Email: "ops@example.com", Groups: []string{"Everyone", "DevOps-Engineering-L1"}, GroupsClaimPresent: true}
	nonAdmin = &artifactav1.Identity{Sub: "00udev", Email: "dev@example.com", Groups: []string{"Everyone"}, GroupsClaimPresent: true}
	oldLogin = &artifactav1.Identity{Sub: "00uold", Email: "old@example.com"} // no groups claim
)

type saHarness struct {
	t     *testing.T
	srv   *Server
	inner *switchAuth
	h     http.Handler
}

func newSAHarness(t *testing.T) *saHarness {
	t.Helper()
	srv := newTestServer(t, t.TempDir(), nil)
	inner := &switchAuth{}
	srv.Auth = &APIKeyAuth{Inner: inner, Store: srv.Store}
	srv.SAAdminGroups = []string{"DevOps-Engineering-L1", "DevOps-Engineering-L2"}
	srv.SAAdminEmails = []string{"breakglass@example.com"}
	return &saHarness{t: t, srv: srv, inner: inner, h: srv.Routes()}
}

// do sends a request as the given identity (nil = unauthenticated) or, when
// key is non-empty, with that API key as the bearer.
func (h *saHarness) do(as *artifactav1.Identity, key, method, target, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	h.inner.id = as
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rr := httptest.NewRecorder()
	h.h.ServeHTTP(rr, req)
	return rr
}

func (h *saHarness) mustStatus(rr *httptest.ResponseRecorder, want int) {
	h.t.Helper()
	if rr.Code != want {
		h.t.Fatalf("status = %d, want %d (body %q)", rr.Code, want, rr.Body.String())
	}
}

func TestServiceAccountsDisabledWhenUnconfigured(t *testing.T) {
	h := newSAHarness(t)
	h.srv.SAAdminGroups, h.srv.SAAdminEmails = nil, nil
	h.mustStatus(h.do(opsAdmin, "", http.MethodGet, "/serviceaccounts", ""), http.StatusNotFound)
}

func TestServiceAccountsRefusals(t *testing.T) {
	h := newSAHarness(t)
	h.mustStatus(h.do(nil, "", http.MethodGet, "/serviceaccounts", ""), http.StatusUnauthorized)

	rr := h.do(nonAdmin, "", http.MethodPost, "/serviceaccounts", `{"name":"ci"}`)
	h.mustStatus(rr, http.StatusForbidden)
	if strings.Contains(rr.Body.String(), "artifacta login") {
		t.Fatalf("non-admin with a groups claim got the re-login hint: %q", rr.Body.String())
	}

	// D3: a token with no groups claim at all gets told to log in again.
	rr = h.do(oldLogin, "", http.MethodGet, "/serviceaccounts", "")
	h.mustStatus(rr, http.StatusForbidden)
	if !strings.Contains(rr.Body.String(), "artifacta login") {
		t.Fatalf("missing-claim 403 lacks the re-login hint: %q", rr.Body.String())
	}

	// A service account is never an admin, even for its own account.
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts", `{"name":"bot"}`), http.StatusCreated)
	var key struct {
		APIKey string `json:"api_key"`
	}
	rr = h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts?name=bot", `{"ttl":"30d"}`)
	h.mustStatus(rr, http.StatusCreated)
	_ = json.Unmarshal(rr.Body.Bytes(), &key)
	rr = h.do(nil, key.APIKey, http.MethodGet, "/serviceaccounts", "")
	h.mustStatus(rr, http.StatusForbidden)
	if strings.Contains(rr.Body.String(), "artifacta login") {
		t.Fatal("service account got the human re-login hint")
	}

	// Denials are audited.
	evs, _ := h.srv.Store.AuditEvents()
	denied := 0
	for _, ev := range evs {
		if ev.GetAction() == artifactav1.AuditAction_AUDIT_ACTION_DENY && !ev.GetAllowed() && strings.HasPrefix(ev.GetSlug(), "sa:") {
			denied++
		}
	}
	if denied != 3 {
		t.Fatalf("audited denials = %d, want 3", denied)
	}
}

func TestServiceAccountsEmailAdmin(t *testing.T) {
	h := newSAHarness(t)
	breakGlass := &artifactav1.Identity{Sub: "00ubg", Email: "BreakGlass@example.com"} // no groups claim
	h.mustStatus(h.do(breakGlass, "", http.MethodPost, "/serviceaccounts", `{"name":"ci"}`), http.StatusCreated)
}

func TestServiceAccountsValidation(t *testing.T) {
	h := newSAHarness(t)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts", `not json`), http.StatusBadRequest)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts", `{"name":"Bad_Name"}`), http.StatusBadRequest)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts", `{"name":"ci"}`), http.StatusCreated)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts", `{"name":"ci"}`), http.StatusConflict)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts?name=ci", `{"ttl":"7d"}`), http.StatusBadRequest)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts?name=ci", `{}`), http.StatusBadRequest)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts?name=nope", `{"ttl":"30d"}`), http.StatusNotFound)
	h.mustStatus(h.do(opsAdmin, "", http.MethodGet, "/serviceaccounts?name=nope", ""), http.StatusNotFound)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPut, "/serviceaccounts", ""), http.StatusMethodNotAllowed)
}

// The whole lifecycle an admin and a bot go through.
func TestServiceAccountsLifecycle(t *testing.T) {
	h := newSAHarness(t)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts", `{"name":"ci-reports","description":"nightly"}`), http.StatusCreated)

	rr := h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts?name=ci-reports", `{"ttl":"90d","description":"gh actions"}`)
	h.mustStatus(rr, http.StatusCreated)
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("key-create response is cacheable")
	}
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	raw, _ := created["api_key"].(string)
	if !strings.HasPrefix(raw, domain.APIKeyPrefix) || created["expires_at"] == nil || created["key_hash"] != nil {
		t.Fatalf("key create response = %v", created)
	}
	keyID, _ := created["id"].(string)

	// The bot publishes, owns the artifact and can make it org-visible.
	pub := h.do(nil, raw, http.MethodPost, "/artifacts?title=Nightly", "<h1>report</h1>")
	h.mustStatus(pub, http.StatusCreated)
	var p struct{ Slug string }
	_ = json.Unmarshal(pub.Body.Bytes(), &p)
	h.mustStatus(h.do(nil, raw, http.MethodPatch, "/artifacts/"+p.Slug+"/visibility", `{"visibility":"org"}`), http.StatusOK)
	art, _, _ := h.srv.Store.GetArtifact(p.Slug)
	if art.GetOwnerSub() != "sa:ci-reports" || art.GetVisibility() != artifactav1.Visibility_VISIBILITY_ORG {
		t.Fatalf("artifact owner/visibility = %q/%v", art.GetOwnerSub(), art.GetVisibility())
	}

	// Listings show the key without any secret material.
	rr = h.do(opsAdmin, "", http.MethodGet, "/serviceaccounts?name=ci-reports", "")
	h.mustStatus(rr, http.StatusOK)
	if body := rr.Body.String(); strings.Contains(body, raw) || strings.Contains(body, `"key_hash"`) || strings.Contains(body, `"api_key":`) {
		t.Fatalf("key list leaks secret material: %s", body)
	}
	var keys struct {
		APIKeys []map[string]any `json:"api_keys"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &keys)
	if len(keys.APIKeys) != 1 || keys.APIKeys[0]["status"] != "active" || keys.APIKeys[0]["last_used_at"] == nil {
		t.Fatalf("keys = %v", keys.APIKeys)
	}
	rr = h.do(opsAdmin, "", http.MethodGet, "/serviceaccounts", "")
	var list struct {
		ServiceAccounts []map[string]any `json:"service_accounts"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.ServiceAccounts) != 1 || list.ServiceAccounts[0]["active_keys"] != float64(1) || list.ServiceAccounts[0]["created_by"] != "ops@example.com" {
		t.Fatalf("accounts = %v", list.ServiceAccounts)
	}

	// Revoke cuts the bot off immediately.
	h.mustStatus(h.do(opsAdmin, "", http.MethodDelete, "/serviceaccounts?name=ci-reports&key_id="+keyID, ""), http.StatusNoContent)
	h.mustStatus(h.do(opsAdmin, "", http.MethodDelete, "/serviceaccounts?name=ci-reports&key_id="+keyID, ""), http.StatusNotFound)
	h.mustStatus(h.do(nil, raw, http.MethodPost, "/artifacts", "<p>x</p>"), http.StatusUnauthorized)

	// Disable is permanent and blocks new keys; the name stays taken.
	h.mustStatus(h.do(opsAdmin, "", http.MethodDelete, "/serviceaccounts?name=ci-reports", ""), http.StatusNoContent)
	h.mustStatus(h.do(opsAdmin, "", http.MethodDelete, "/serviceaccounts?name=ci-reports", ""), http.StatusNotFound)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts?name=ci-reports", `{"ttl":"30d"}`), http.StatusNotFound)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts", `{"name":"ci-reports"}`), http.StatusConflict)

	// Every mutation is audited against sa:<name> by the acting admin.
	want := map[artifactav1.AuditAction]bool{
		artifactav1.AuditAction_AUDIT_ACTION_SA_CREATE:     false,
		artifactav1.AuditAction_AUDIT_ACTION_SA_KEY_CREATE: false,
		artifactav1.AuditAction_AUDIT_ACTION_SA_KEY_REVOKE: false,
		artifactav1.AuditAction_AUDIT_ACTION_SA_DISABLE:    false,
	}
	evs, _ := h.srv.Store.AuditEvents()
	for _, ev := range evs {
		if _, ok := want[ev.GetAction()]; ok && ev.GetSlug() == "sa:ci-reports" && ev.GetPrincipalSub() == opsAdmin.GetSub() && ev.GetAllowed() {
			want[ev.GetAction()] = true
		}
	}
	for a, seen := range want {
		if !seen {
			t.Errorf("no audit row for %v", a)
		}
	}
}

func TestServiceAccountsNeverExpiringKey(t *testing.T) {
	h := newSAHarness(t)
	h.mustStatus(h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts", `{"name":"ci"}`), http.StatusCreated)
	rr := h.do(opsAdmin, "", http.MethodPost, "/serviceaccounts?name=ci", `{"ttl":"never"}`)
	h.mustStatus(rr, http.StatusCreated)
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created["expires_at"] != nil {
		t.Fatalf("never key has expires_at %v", created["expires_at"])
	}
}

// A rejected key is logged by its prefix only, never in full.
func TestRejectedKeyLogsPrefixOnly(t *testing.T) {
	var buf bytes.Buffer
	a := &APIKeyAuth{Inner: &switchAuth{}, Store: newKeyStore(t), Logger: slog.New(slog.NewTextHandler(&buf, nil))}
	raw, _, _ := domain.NewAPIKey()
	if _, ok := a.Identify(bearerReq(raw)); ok {
		t.Fatal("unknown key accepted")
	}
	if strings.Contains(buf.String(), raw) || !strings.Contains(buf.String(), domain.KeyPrefix(raw)) {
		t.Fatalf("log line = %q", buf.String())
	}
}
