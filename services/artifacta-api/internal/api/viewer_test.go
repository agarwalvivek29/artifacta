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
