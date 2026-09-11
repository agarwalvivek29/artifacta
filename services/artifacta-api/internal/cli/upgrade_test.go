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
	if !strings.Contains(out, "downgrade") || !strings.Contains(out, "ARTIFACTA_VERSION=0.0.4") {
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

func TestDecideUpgrade(t *testing.T) {
	// Already latest.
	if a := decideUpgrade("0.0.3", "0.0.3", "", "", false); !a.upToDate {
		t.Errorf("same version should be up to date: %+v", a)
	}
	// A dev build is never "up to date" — it should still report the latest.
	if a := decideUpgrade("dev", "0.0.3", "", "", false); a.upToDate {
		t.Errorf("dev build should not be reported up to date: %+v", a)
	}

	// Newer, independent of server → prompt upgrade unconditionally.
	a := decideUpgrade("0.0.3", "0.0.4", "", "", false)
	if a.upToDate || a.blockedByServer || !strings.Contains(a.message, installCmd) {
		t.Errorf("independent newer release should prompt an upgrade: %+v", a)
	}

	// Newer, depends on server, not logged in → advise logging in / upgrade.
	a = decideUpgrade("0.0.3", "0.1.0", "0.1.0", "", false)
	if a.blockedByServer || !strings.Contains(a.message, "log in") {
		t.Errorf("server-dependent release, no server, should mention logging in: %+v", a)
	}

	// Newer, depends on server, deployment new enough → compatible, prompt upgrade.
	a = decideUpgrade("0.0.3", "0.1.0", "0.1.0", "0.1.0", true)
	if a.blockedByServer || a.upToDate || !strings.Contains(a.message, installCmd) {
		t.Errorf("server-dependent release with a new-enough deployment should prompt upgrade: %+v", a)
	}

	// Newer, depends on server, deployment too old → blocked, upgrade server first.
	a = decideUpgrade("0.0.3", "0.1.0", "0.1.0", "0.0.3", true)
	if !a.blockedByServer || !strings.Contains(a.message, "server first") {
		t.Errorf("server-dependent release with an old deployment should be blocked: %+v", a)
	}
}

func TestDecideMatch(t *testing.T) {
	// CLI matches the deployment → compatible, nothing to do.
	if a := decideMatch("0.0.3", "0.0.3"); a.blockedByServer || !strings.Contains(a.message, "matches your deployment") {
		t.Errorf("equal versions should be a clean match: %+v", a)
	}
	// CLI older than deployment → install the (newer) matching version.
	a := decideMatch("0.0.2", "0.0.3")
	if a.blockedByServer || !strings.Contains(a.message, "older") || !strings.Contains(a.message, "ARTIFACTA_VERSION=0.0.3") {
		t.Errorf("older CLI should be told to install the deployment version: %+v", a)
	}
	// CLI newer than deployment → downgrade to match (the user's key scenario).
	a = decideMatch("0.0.4", "0.0.3")
	if !a.blockedByServer || !strings.Contains(a.message, "downgrade") || !strings.Contains(a.message, "ARTIFACTA_VERSION=0.0.3") {
		t.Errorf("newer CLI should be offered a downgrade to the deployment version: %+v", a)
	}
	// dev build → install the deployment's release to match.
	a = decideMatch("dev", "0.0.3")
	if !strings.Contains(a.message, "dev build") || !strings.Contains(a.message, "ARTIFACTA_VERSION=0.0.3") {
		t.Errorf("dev build should be told to install the matching release: %+v", a)
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
