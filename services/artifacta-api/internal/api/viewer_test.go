package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestViewerCSPAllowsRawFetch guards a bug caught in live browser QA: the viewer
// shell fetches /a/{slug}/raw, so its CSP must permit that connection. With only
// `default-src 'none'` the fetch falls back to a blocked connect-src and the
// viewer renders "Could not load this artifact." The shell CSP must carry
// `connect-src 'self'`.
func TestViewerCSPAllowsRawFetch(t *testing.T) {
	srv := httptest.NewServer((&Server{}).Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/a/anyslug")
	if err != nil {
		t.Fatalf("GET viewer: %v", err)
	}
	defer resp.Body.Close()

	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self'") {
		t.Fatalf("viewer CSP must allow the shell to fetch /raw; got %q", csp)
	}
}

// TestViewerCSPPosture pins the flag-gated Content-Security-Policy on the viewer
// shell (the header a srcdoc'd artifact inherits — where the air-gap posture is
// actually enforced, ADR-0023). Default (deny) must forbid external hosts; allow
// must permit https: so a CDN-import artifact renders.
func TestViewerCSPPosture(t *testing.T) {
	get := func(egress bool) string {
		srv := httptest.NewServer((&Server{Egress: egress}).Routes())
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/a/anyslug")
		if err != nil {
			t.Fatalf("GET viewer: %v", err)
		}
		defer resp.Body.Close()
		return resp.Header.Get("Content-Security-Policy")
	}

	deny := get(false)
	if strings.Contains(deny, "https:") {
		t.Fatalf("air-gap (deny) viewer CSP must forbid external https:; got %q", deny)
	}
	if !strings.Contains(deny, "frame-src 'self'") {
		t.Fatalf("viewer CSP must keep frame-src 'self' for the srcdoc iframe; got %q", deny)
	}

	allow := get(true)
	if !strings.Contains(allow, "script-src 'unsafe-inline' https:") {
		t.Fatalf("allow-egress viewer CSP must permit https: scripts; got %q", allow)
	}
}

// TestRawCSPPosture pins the flag-gated CSP on the direct /raw document path,
// which must always carry the sandbox and match the egress posture.
func TestRawCSPPosture(t *testing.T) {
	denyCSP := (&Server{Egress: false}).contentCSP(true, "")
	if !strings.HasPrefix(denyCSP, "sandbox allow-scripts;") {
		t.Fatalf("/raw CSP must sandbox the artifact; got %q", denyCSP)
	}
	if strings.Contains(denyCSP, "https:") {
		t.Fatalf("air-gap /raw CSP must forbid https:; got %q", denyCSP)
	}
	allowCSP := (&Server{Egress: true}).contentCSP(true, "")
	if !strings.Contains(allowCSP, "https:") {
		t.Fatalf("allow-egress /raw CSP must permit https:; got %q", allowCSP)
	}
}
