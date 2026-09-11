package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
)

// saveAddr writes a config pointing artifacta at addr (host:port) under an
// isolated ARTIFACTA_HOME.
func saveAddr(t *testing.T, addr string) {
	t.Helper()
	t.Setenv("ARTIFACTA_HOME", t.TempDir())
	c := config.Default()
	c.Addr = addr
	if err := config.Save(c); err != nil {
		t.Fatal(err)
	}
}

func TestHealthcheckPassesOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	saveAddr(t, strings.TrimPrefix(srv.URL, "http://"))
	if err := healthcheck(); err != nil {
		t.Fatalf("healthcheck against a healthy server: %v", err)
	}
}

func TestHealthcheckFailsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	saveAddr(t, strings.TrimPrefix(srv.URL, "http://"))
	if err := healthcheck(); err == nil {
		t.Fatal("healthcheck against a 503 server: expected an error, got nil")
	}
}

func TestHealthcheckFailsWhenUnreachable(t *testing.T) {
	// Nothing is listening on this port.
	saveAddr(t, "127.0.0.1:0")
	if err := healthcheck(); err == nil {
		t.Fatal("healthcheck against an unreachable server: expected an error, got nil")
	}
}

// TestServeFailsLoudOnEmptyLocalToken guards the silent-misconfig footgun: with
// local auth selected and no token, serve must error before binding rather than
// boot a server that rejects every request.
func TestServeFailsLoudOnEmptyLocalToken(t *testing.T) {
	t.Setenv("ARTIFACTA_HOME", t.TempDir())
	c := config.Default() // no OIDC, no token
	if err := config.Save(c); err != nil {
		t.Fatal(err)
	}
	err := serve()
	if err == nil {
		t.Fatal("serve with empty local token: expected an error, got nil (silent-reject footgun)")
	}
	if !strings.Contains(err.Error(), "ARTIFACTA_TOKEN") {
		t.Fatalf("serve error = %q, want a hint to set ARTIFACTA_TOKEN", err)
	}
}
