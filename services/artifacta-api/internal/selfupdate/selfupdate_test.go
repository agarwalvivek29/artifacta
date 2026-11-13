package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const asset = "artifacta_0.3.1_linux_amd64.tar.gz"

func TestAssetName(t *testing.T) {
	got, err := (&Updater{GOOS: "darwin", GOARCH: "arm64"}).AssetName("v0.3.1")
	if err != nil || got != "artifacta_0.3.1_darwin_arm64.tar.gz" {
		t.Fatalf("AssetName = %q, %v", got, err)
	}
	for _, u := range []*Updater{{GOOS: "windows", GOARCH: "amd64"}, {GOOS: "linux", GOARCH: "mips"}} {
		if _, err := u.AssetName("1.0.0"); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s/%s: err = %v, want ErrUnsupported", u.GOOS, u.GOARCH, err)
		}
	}
}

func TestVersionHelpers(t *testing.T) {
	for _, in := range []string{"0.3.1", "v0.3.1", " v0.3.1 "} {
		if tag, ver := TagAndVersion(in); tag != "v0.3.1" || ver != "0.3.1" {
			t.Errorf("TagAndVersion(%q) = %q, %q", in, tag, ver)
		}
	}
	for v, ok := range map[string]bool{"0.2.1": true, "v1.0.0-rc.1": true, "dev": false, "0.2": false,
		"../../evil": false, "0.2.1/../x": false, "": false} {
		if ValidVersion(v) != ok {
			t.Errorf("ValidVersion(%q) = %v, want %v", v, !ok, ok)
		}
	}
}

func makeArchive(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(content))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// release serves /<tag>/<asset> and /<tag>/checksums.txt; sums=="" means 404.
func release(t *testing.T, archive []byte, sums string) *Updater {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0.3.1/" + asset:
			_, _ = w.Write(archive)
		case "/v0.3.1/checksums.txt":
			if sums == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(sums))
		case "/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v0.3.1","body":"min-server-version: 0.3.0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Updater{HTTPClient: srv.Client(), GOOS: "linux", GOARCH: "amd64", DownloadBase: srv.URL, LatestAPI: srv.URL + "/latest"}
}

// installed writes a fake current binary and returns its path.
func installed(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "artifacta")
	if err := os.WriteFile(p, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInstallReplacesVerifiedBinary(t *testing.T) {
	archive := makeArchive(t, "artifacta", "new-binary")
	u := release(t, archive, fmt.Sprintf("%s  other.tar.gz\n%s  %s\n", sum([]byte("x")), sum(archive), asset))
	target := installed(t)
	if err := u.Install(t.Context(), "v0.3.1", target); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new-binary" {
		t.Fatalf("target = %q, want the new binary", got)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("new binary is not executable: %v", fi.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".artifacta-upgrade-*")); len(left) != 0 {
		t.Fatalf("staging files left behind: %v", left)
	}
	if v, body, err := u.LatestRelease(t.Context()); err != nil || v != "0.3.1" || body != "min-server-version: 0.3.0" {
		t.Fatalf("LatestRelease = %q, %q, %v", v, body, err)
	}
}

// Every verification failure must leave the installed binary untouched.
func TestInstallFailsClosed(t *testing.T) {
	archive := makeArchive(t, "artifacta", "evil")
	for name, tc := range map[string]struct {
		archive []byte
		sums    string
		version string
	}{
		"bad checksum":     {archive, fmt.Sprintf("%064d  %s\n", 0, asset), "0.3.1"},
		"no checksums.txt": {archive, "", "0.3.1"},
		"no entry":         {archive, fmt.Sprintf("%s  something-else.tar.gz\n", sum(archive)), "0.3.1"},
		"wrong binary name": {makeArchive(t, "qw", "x"),
			fmt.Sprintf("%s  %s\n", sum(makeArchive(t, "qw", "x")), asset), "0.3.1"},
		"path in version": {archive, fmt.Sprintf("%s  %s\n", sum(archive), asset), "../v0.3.1"},
	} {
		t.Run(name, func(t *testing.T) {
			u := release(t, tc.archive, tc.sums)
			target := installed(t)
			if err := u.Install(t.Context(), tc.version, target); err == nil {
				t.Fatal("Install succeeded, want failure")
			}
			if got, _ := os.ReadFile(target); string(got) != "old" {
				t.Fatalf("installed binary changed to %q", got)
			}
		})
	}
}

func TestInstallPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	archive := makeArchive(t, "artifacta", "new")
	u := release(t, archive, fmt.Sprintf("%s  %s\n", sum(archive), asset))
	target := installed(t)
	dir := filepath.Dir(target)
	_ = os.Chmod(dir, 0o555)
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	err := u.Install(t.Context(), "0.3.1", target)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v, want os.ErrPermission", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("installed binary changed to %q", got)
	}
}
