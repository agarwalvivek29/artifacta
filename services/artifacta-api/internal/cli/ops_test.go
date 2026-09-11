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
	if err := healthcheck(nil); err != nil {
		t.Fatalf("healthcheck against a healthy server: %v", err)
	}
}

func TestHealthcheckFailsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	saveAddr(t, strings.TrimPrefix(srv.URL, "http://"))
	if err := healthcheck(nil); err == nil {
		t.Fatal("healthcheck against a 503 server: expected an error, got nil")
	}
}

func TestHealthcheckFailsWhenUnreachable(t *testing.T) {
	// Nothing is listening on this port.
	saveAddr(t, "127.0.0.1:0")
	if err := healthcheck(nil); err == nil {
		t.Fatal("healthcheck against an unreachable server: expected an error, got nil")
	}
}

// healthyHealthServer is a test server that answers /health with 200.
func healthyHealthServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestHealthcheckUsesExplicitURLArg: an explicit URL argument is probed even when
// the local listen address points nowhere.
func TestHealthcheckUsesExplicitURLArg(t *testing.T) {
	srv := healthyHealthServer(t)
	saveAddr(t, "127.0.0.1:0") // local addr is dead; the arg must win
	if err := healthcheck([]string{srv.URL}); err != nil {
		t.Fatalf("healthcheck with explicit URL arg: %v", err)
	}
}

// A scheme-less arg (bare host:port) is accepted: healthcheck prepends http://
// so it matches the format the container-probe path already synthesizes.
func TestHealthcheckAcceptsSchemelessArg(t *testing.T) {
	srv := healthyHealthServer(t)
	saveAddr(t, "127.0.0.1:0")
	hostport := strings.TrimPrefix(srv.URL, "http://")
	if err := healthcheck([]string{hostport}); err != nil {
		t.Fatalf("healthcheck with scheme-less arg %q: %v", hostport, err)
	}
}

// TestHealthcheckProbesRemoteBaseURL is the bug-fix guard: a CLI logged into a
// remote probes that remote's /health, not a bogus loopback address.
func TestHealthcheckProbesRemoteBaseURL(t *testing.T) {
	srv := healthyHealthServer(t)
	t.Setenv("ARTIFACTA_HOME", t.TempDir())
	c := config.Default()
	c.Addr = "127.0.0.1:0" // loopback probe would fail — proves it isn't used
	c.BaseURL = srv.URL
	c.LoggedIn = true
	if err := config.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := healthcheck(nil); err != nil {
		t.Fatalf("healthcheck against logged-in remote: %v", err)
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
