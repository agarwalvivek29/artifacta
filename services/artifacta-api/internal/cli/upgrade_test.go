package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// versionServer returns an httptest server whose /version reports ver.
func versionServer(t *testing.T, ver string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"` + ver + `","min_cli_version":"0.0.1","capabilities":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// captureStdout runs f and returns whatever it wrote to os.Stdout.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	f()
	_ = w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestNoteVersionMismatch(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	// Matched → silent.
	Version = "0.0.5"
	if out := captureStdout(t, func() { noteVersionMismatch(versionServer(t, "0.0.5").URL) }); out != "" {
		t.Fatalf("matched version should be silent, got: %q", out)
	}
	// CLI newer than deployment → advises a downgrade with the pinned version.
	Version = "0.0.6"
	out := captureStdout(t, func() { noteVersionMismatch(versionServer(t, "0.0.4").URL) })
	if !strings.Contains(out, "newer than your deployment (0.0.4)") || !strings.Contains(out, "downgrade") || !strings.Contains(out, "artifacta upgrade") {
		t.Fatalf("newer CLI should advise a downgrade to the deployment version, got: %q", out)
	}
	// Unreachable server → silent (never fails the caller).
	if out := captureStdout(t, func() { noteVersionMismatch("http://127.0.0.1:0") }); out != "" {
		t.Fatalf("unreachable server should be silent, got: %q", out)
	}
}

func TestCmpVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.0.3", "0.0.3", 0},
		{"0.0.2", "0.0.3", -1},
		{"0.1.0", "0.0.9", 1},
		{"v0.0.3", "0.0.3", 0}, // leading v ignored
		{"0.0", "0.0.0", 0},    // missing segments are zero
		{"1.2.0", "1.2", 0},
		{"0.10.0", "0.9.0", 1}, // numeric, not lexical
	}
	for _, tc := range cases {
		if got := cmpVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("cmpVersion(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestParseMinServerVersion(t *testing.T) {
	cases := []struct {
		body, want string
	}{
		{"nothing here", ""},
		{"min-server-version: 0.0.3", "0.0.3"},
		{"line one\nMin-Server-Version:  0.1.0 \nline three", "0.1.0"}, // case-insensitive, trimmed
		{"> min-server-version: 0.0.5", "0.0.5"},                       // blockquote marker
		{"changelog\n- did things", ""},
	}
	for _, tc := range cases {
		if got := parseMinServerVersion(tc.body); got != tc.want {
			t.Errorf("parseMinServerVersion(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestDecideMatch(t *testing.T) {
	for _, tc := range []struct{ cli, server, want string }{
		{"0.0.3", "0.0.3", "matches your deployment"},
		{"0.0.2", "0.0.3", "older than your deployment (0.0.3). to install the matching version:\n  artifacta upgrade"},
		{"0.0.4", "0.0.3", "a downgrade"},
		{"dev", "0.0.3", "artifacta upgrade --version 0.0.3"},
	} {
		if got := decideMatch(tc.cli, tc.server); !strings.Contains(got, tc.want) {
			t.Errorf("decideMatch(%s, %s) = %q, want it to contain %q", tc.cli, tc.server, got, tc.want)
		}
	}
}

func TestServerVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"0.0.3","min_cli_version":"0.0.1","capabilities":["cli-login"]}`))
	}))
	defer srv.Close()

	ver, minCLI, caps, err := serverVersion(srv.URL)
	if err != nil {
		t.Fatalf("serverVersion: %v", err)
	}
	if ver != "0.0.3" || minCLI != "0.0.1" || len(caps) != 1 || caps[0] != "cli-login" {
		t.Fatalf("serverVersion = (%q,%q,%v), want the advertised values", ver, minCLI, caps)
	}
}
