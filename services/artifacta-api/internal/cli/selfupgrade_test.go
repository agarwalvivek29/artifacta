package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/selfupdate"
)

// fakeRelease serves any version's asset (a shell script that prints that
// version, standing in for the real binary) plus checksums.txt, and a latest
// release. It points newUpdater at itself for the test.
func fakeRelease(t *testing.T, latest string) {
	t.Helper()
	archive := func(ver string) []byte {
		body := fmt.Sprintf("#!/bin/sh\necho 'artifacta %s'\n", ver)
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(&tar.Header{Name: "artifacta", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
		_ = tw.Close()
		_ = gz.Close()
		return buf.Bytes()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			fmt.Fprintf(w, `{"tag_name":"v%s","body":""}`, latest)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/") // v<ver>/<file>
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		ver := strings.TrimPrefix(parts[0], "v")
		asset := fmt.Sprintf("artifacta_%s_%s_%s.tar.gz", ver, runtime.GOOS, runtime.GOARCH)
		a := archive(ver)
		switch parts[1] {
		case asset:
			_, _ = w.Write(a)
		case "checksums.txt":
			s := sha256.Sum256(a)
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(s[:]), asset)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	orig := newUpdater
	newUpdater = func() *selfupdate.Updater {
		u := orig()
		u.DownloadBase, u.LatestAPI = srv.URL, srv.URL+"/latest"
		return u
	}
	t.Cleanup(func() { newUpdater = orig })
}

// fakeInstall puts a stand-in "installed" binary in a temp dir and points
// executablePath at it.
func fakeInstall(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "artifacta")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho 'artifacta old'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := executablePath
	executablePath = func() (string, error) { return p, nil }
	t.Cleanup(func() { executablePath = orig })
	return p
}

func setVersion(t *testing.T, v string) {
	t.Helper()
	orig := Version
	Version = v
	t.Cleanup(func() { Version = orig })
}

// loginTo saves a logged-in config pointed at baseURL in a fresh ARTIFACTA_HOME.
func loginTo(t *testing.T, baseURL string) {
	t.Helper()
	t.Setenv("ARTIFACTA_HOME", t.TempDir())
	t.Setenv("ARTIFACTA_API_KEY", "")
	if baseURL == "" {
		return
	}
	if err := config.Save(config.Config{BaseURL: baseURL, LoggedIn: true}); err != nil {
		t.Fatal(err)
	}
}

func installedContent(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestUpgradeInstalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-update falls back to the installer on Windows")
	}
	fakeRelease(t, "0.4.0")

	t.Run("matches the deployment", func(t *testing.T) {
		setVersion(t, "0.2.0")
		loginTo(t, versionServer(t, "0.3.1").URL)
		exe := fakeInstall(t)
		out, err := run(t, func() error { return Run([]string{"upgrade"}) })
		if err != nil {
			t.Fatalf("upgrade: %v\n%s", err, out)
		}
		if !strings.Contains(out, "target:  artifacta 0.3.1 (matches your deployment") ||
			!strings.Contains(out, "→ artifacta 0.3.1") || !strings.Contains(installedContent(t, exe), "artifacta 0.3.1") {
			t.Fatalf("did not install the deployment's version:\n%s", out)
		}
	})
	t.Run("downgrades to match", func(t *testing.T) {
		setVersion(t, "0.5.0")
		loginTo(t, versionServer(t, "0.3.1").URL)
		exe := fakeInstall(t)
		if out, err := run(t, func() error { return Run([]string{"upgrade"}) }); err != nil || !strings.Contains(installedContent(t, exe), "0.3.1") {
			t.Fatalf("downgrade: %v\n%s", err, out)
		}
	})
	t.Run("not logged in uses latest", func(t *testing.T) {
		setVersion(t, "0.2.0")
		loginTo(t, "")
		exe := fakeInstall(t)
		out, err := run(t, func() error { return Run([]string{"upgrade"}) })
		if err != nil || !strings.Contains(out, "(latest release)") || !strings.Contains(installedContent(t, exe), "0.4.0") {
			t.Fatalf("latest: %v\n%s", err, out)
		}
	})
	t.Run("--latest beats the deployment", func(t *testing.T) {
		setVersion(t, "0.2.0")
		loginTo(t, versionServer(t, "0.3.1").URL)
		exe := fakeInstall(t)
		if out, err := run(t, func() error { return Run([]string{"upgrade", "--latest"}) }); err != nil || !strings.Contains(installedContent(t, exe), "0.4.0") {
			t.Fatalf("--latest: %v\n%s", err, out)
		}
	})
	t.Run("--version pins", func(t *testing.T) {
		setVersion(t, "0.2.0")
		loginTo(t, versionServer(t, "0.3.1").URL)
		exe := fakeInstall(t)
		if out, err := run(t, func() error { return Run([]string{"upgrade", "--version", "v0.2.9"}) }); err != nil || !strings.Contains(installedContent(t, exe), "0.2.9") {
			t.Fatalf("--version: %v\n%s", err, out)
		}
	})
	t.Run("dev build with explicit version", func(t *testing.T) {
		setVersion(t, "dev")
		loginTo(t, "")
		exe := fakeInstall(t)
		if out, err := run(t, func() error { return Run([]string{"upgrade", "--version=0.3.0"}) }); err != nil || !strings.Contains(installedContent(t, exe), "0.3.0") {
			t.Fatalf("dev --version: %v\n%s", err, out)
		}
	})
}

func TestUpgradeLeavesBinaryAlone(t *testing.T) {
	fakeRelease(t, "0.4.0")
	for name, tc := range map[string]struct {
		version, deployment string
		args                []string
		want                string
	}{
		"already up to date": {"0.3.1", "0.3.1", nil, "already up to date"},
		"--check":            {"0.2.0", "0.3.1", []string{"--check"}, "would install 0.3.1"},
		"dev build":          {"dev", "0.3.1", nil, "dev build; not replacing it"},
	} {
		t.Run(name, func(t *testing.T) {
			setVersion(t, tc.version)
			loginTo(t, versionServer(t, tc.deployment).URL)
			exe := fakeInstall(t)
			out, err := run(t, func() error { return Run(append([]string{"upgrade"}, tc.args...)) })
			if err != nil || !strings.Contains(out, tc.want) {
				t.Fatalf("err=%v, out:\n%s\nwant %q", err, out, tc.want)
			}
			if !strings.Contains(installedContent(t, exe), "artifacta old") {
				t.Fatal("installed binary was replaced")
			}
		})
	}
}

func TestUpgradeArgumentErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--latest", "--version", "0.3.0"},
		{"--version"},
		{"--version", "../../x"},
		{"--bogus"},
	} {
		if err := upgrade(args); err == nil {
			t.Errorf("upgrade(%q) succeeded, want an error", args)
		}
	}
}

func TestUpgradePermissionDeniedAdvises(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root unix user")
	}
	fakeRelease(t, "0.4.0")
	setVersion(t, "0.2.0")
	loginTo(t, versionServer(t, "0.3.1").URL)
	exe := fakeInstall(t)
	dir := filepath.Dir(exe)
	_ = os.Chmod(dir, 0o555)
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := run(t, func() error { return Run([]string{"upgrade"}) })
	if err == nil || !strings.Contains(err.Error(), "sudo artifacta upgrade --version 0.3.1") ||
		!strings.Contains(err.Error(), "ARTIFACTA_VERSION=0.3.1") {
		t.Fatalf("err = %v, want sudo + installer advice", err)
	}
	if !strings.Contains(installedContent(t, exe), "artifacta old") {
		t.Fatal("installed binary was replaced")
	}
}

// countingVersionServer is a /version server that counts requests.
func countingVersionServer(t *testing.T, ver string, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			hits.Add(1)
			time.Sleep(delay)
			fmt.Fprintf(w, `{"version":%q}`, ver)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// nudgeEnv captures the nudge output and pins the clock.
func nudgeEnv(t *testing.T) (*bytes.Buffer, *time.Time) {
	t.Helper()
	for _, k := range []string{"CI", "ARTIFACTA_NO_UPDATE_CHECK"} {
		t.Setenv(k, "")
	}
	var buf bytes.Buffer
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	origOut, origNow := nudgeOut, nudgeNow
	nudgeOut, nudgeNow = &buf, func() time.Time { return now }
	t.Cleanup(func() { nudgeOut, nudgeNow = origOut, origNow })
	return &buf, &now
}

func TestNudgeOncePerDay(t *testing.T) {
	buf, now := nudgeEnv(t)
	setVersion(t, "0.2.0")
	srv, hits := countingVersionServer(t, "0.2.1", 0)
	loginTo(t, srv.URL)

	startNudge("ls")()
	want := "artifacta: your CLI (0.2.0) is older than the deployment (0.2.1). run: artifacta upgrade\n"
	if buf.String() != want {
		t.Fatalf("nudge = %q, want %q", buf.String(), want)
	}
	buf.Reset()
	*now = now.Add(23 * time.Hour)
	startNudge("publish")()
	if buf.Len() != 0 || hits.Load() != 1 {
		t.Fatalf("second check within 24h: out=%q hits=%d", buf.String(), hits.Load())
	}
	*now = now.Add(2 * time.Hour)
	startNudge("publish")()
	if !strings.Contains(buf.String(), "artifacta upgrade") || hits.Load() != 2 {
		t.Fatalf("check after 24h: out=%q hits=%d", buf.String(), hits.Load())
	}
}

func TestNudgeQuietCases(t *testing.T) {
	for name, tc := range map[string]struct {
		version, server, cmd, env string
		wantHits                  int32
	}{
		"versions match":    {"0.2.1", "0.2.1", "ls", "", 1},
		"local command":     {"0.2.0", "0.2.1", "version", "", 0},
		"upgrade itself":    {"0.2.0", "0.2.1", "upgrade", "", 0},
		"dev build":         {"dev", "0.2.1", "ls", "", 0},
		"ci":                {"0.2.0", "0.2.1", "ls", "CI=true", 0},
		"bot api key":       {"0.2.0", "0.2.1", "ls", "ARTIFACTA_API_KEY=afa_x", 0},
		"opted out":         {"0.2.0", "0.2.1", "ls", "ARTIFACTA_NO_UPDATE_CHECK=1", 0},
		"dev deployment":    {"0.2.0", "dev", "ls", "", 1},
		"CI=false still on": {"0.2.1", "0.2.1", "ls", "CI=false", 1},
	} {
		t.Run(name, func(t *testing.T) {
			buf, _ := nudgeEnv(t)
			setVersion(t, tc.version)
			srv, hits := countingVersionServer(t, tc.server, 0)
			loginTo(t, srv.URL)
			if k, v, ok := strings.Cut(tc.env, "="); ok {
				t.Setenv(k, v)
			}
			startNudge(tc.cmd)()
			if buf.Len() != 0 || hits.Load() != tc.wantHits {
				t.Fatalf("out=%q hits=%d, want silent with %d hits", buf.String(), hits.Load(), tc.wantHits)
			}
		})
	}
}

func TestNudgeNeverBlocksOrFails(t *testing.T) {
	buf, _ := nudgeEnv(t)
	setVersion(t, "0.2.0")
	orig := nudgeFetchTime
	nudgeFetchTime = 50 * time.Millisecond
	t.Cleanup(func() { nudgeFetchTime = orig })

	slow, _ := countingVersionServer(t, "0.2.1", time.Second)
	loginTo(t, slow.URL)
	start := time.Now()
	startNudge("ls")()
	if d := time.Since(start); d > 500*time.Millisecond || buf.Len() != 0 {
		t.Fatalf("slow server: took %v, out=%q", d, buf.String())
	}

	loginTo(t, "http://127.0.0.1:1") // nothing listens here
	startNudge("ls")()
	if buf.Len() != 0 {
		t.Fatalf("unreachable server printed %q", buf.String())
	}
	if last, ok := loadUpdateCheck(); !ok || last.ServerVersion != "" {
		t.Fatalf("failed check not recorded: %+v %v", last, ok)
	}
}

// The nudge goes to stderr only, so --json output on stdout stays parseable.
func TestNudgeNotOnStdout(t *testing.T) {
	for _, k := range []string{"CI", "ARTIFACTA_NO_UPDATE_CHECK"} {
		t.Setenv(k, "")
	}
	setVersion(t, "0.2.0")
	srv, hits := countingVersionServer(t, "0.2.1", 0)
	loginTo(t, srv.URL)
	stdout := captureStdout(t, func() { startNudge("ls")() })
	if stdout != "" || hits.Load() != 1 {
		t.Fatalf("stdout = %q (hits %d), want nothing on stdout", stdout, hits.Load())
	}
}
