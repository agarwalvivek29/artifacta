package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	for v, ok := range map[string]bool{"0.2.1": true, "v1.0.0-rc.1": false, "v1.0.0": true, "dev": false, "0.2": false,
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

// testKey signs the fake releases; the Updater trusts its public half.
var testPub, testPriv, _ = ed25519.GenerateKey(nil)

// release serves /<tag>/<asset>, /<tag>/checksums.txt and a valid
// checksums.txt.sig; sums=="" means no checksums.txt.
func release(t *testing.T, archive []byte, sums string) *Updater {
	t.Helper()
	return releaseSigned(t, archive, sums, ed25519.Sign(testPriv, []byte(sums)))
}

// releaseSigned is release with an explicit signature (nil = no .sig file).
func releaseSigned(t *testing.T, archive []byte, sums string, sig []byte) *Updater {
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
		case "/v0.3.1/checksums.txt.sig":
			switch string(sig) {
			case "":
				http.NotFound(w, r)
				return
			case "503":
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(sig)
		case "/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v0.3.1","body":"min-server-version: 0.3.0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Updater{HTTPClient: srv.Client(), GOOS: "linux", GOARCH: "amd64", DownloadBase: srv.URL, LatestAPI: srv.URL + "/latest", PublicKeys: []ed25519.PublicKey{testPub}}
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
		"symlink entry":   {symlinkArchive(t), fmt.Sprintf("%s  %s\n", sum(symlinkArchive(t)), asset), "0.3.1"},
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

// A release has to be signed by the release key; nothing else gets installed.
func TestInstallRequiresSignature(t *testing.T) {
	archive := makeArchive(t, "artifacta", "evil")
	sums := fmt.Sprintf("%s  %s\n", sum(archive), asset)
	_, otherKey, _ := ed25519.GenerateKey(nil)
	for name, tc := range map[string]struct {
		sig      []byte
		unsigned bool
	}{
		"no signature file":        {nil, true},
		"signed by another key":    {ed25519.Sign(otherKey, []byte(sums)), false},
		"signature of other bytes": {ed25519.Sign(testPriv, []byte(sums+"x")), false},
		"truncated signature":      {ed25519.Sign(testPriv, []byte(sums))[:10], false},
	} {
		t.Run(name, func(t *testing.T) {
			u := releaseSigned(t, archive, sums, tc.sig)
			target := installed(t)
			err := u.Install(t.Context(), "0.3.1", target)
			if err == nil || errors.Is(err, ErrUnsigned) != tc.unsigned {
				t.Fatalf("err = %v (unsigned want %v)", err, tc.unsigned)
			}
			if got, _ := os.ReadFile(target); string(got) != "old" {
				t.Fatalf("installed binary changed to %q", got)
			}
		})
	}
	// A server error fetching the signature is a failure, not "unsigned".
	u := releaseSigned(t, archive, sums, []byte("503"))
	if err := u.Install(t.Context(), "0.3.1", installed(t)); err == nil || errors.Is(err, ErrUnsigned) {
		t.Fatalf("503 on the signature: err = %v, want a non-ErrUnsigned failure", err)
	}
	// Rotation: any configured key verifying is enough.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	good := makeArchive(t, "artifacta", "new")
	u = release(t, good, fmt.Sprintf("%s  %s\n", sum(good), asset))
	u.PublicKeys = []ed25519.PublicKey{otherPub, testPub}
	if err := u.Install(t.Context(), "0.3.1", installed(t)); err != nil {
		t.Fatalf("second key should verify: %v", err)
	}
	u = release(t, archive, sums)
	u.PublicKeys = nil
	if err := u.Install(t.Context(), "0.3.1", installed(t)); err == nil {
		t.Fatal("installed with no public key configured")
	}
}

// The key built into the CLI must be the one the release workflow checks
// its signature against.
func TestReleaseKeyMatchesPEM(t *testing.T) {
	pemBytes, err := os.ReadFile("../../../../.github/release-signing.pub.pem")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("no PEM block")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !New().PublicKeys[0].Equal(k) {
		t.Fatal("releasePublicKey does not match .github/release-signing.pub.pem")
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

func symlinkArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "artifacta", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"})
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// sha256sum binary mode ("*name"), uppercase hex and a nested path all verify.
func TestInstallAcceptsChecksumVariants(t *testing.T) {
	archive := makeArchive(t, "dist/artifacta", "nested")
	u := release(t, archive, fmt.Sprintf("%s *%s\n", strings.ToUpper(sum(archive)), asset))
	target := installed(t)
	if err := os.Chmod(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := u.Install(t.Context(), "0.3.1", target); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "nested" {
		t.Fatalf("target = %q", got)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %v, want the original 0750 kept", fi.Mode().Perm())
	}
}

func TestLatestReleaseRejects(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"rate limited": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", http.StatusForbidden) },
		"bad json":     func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{")) },
		"bad tag":      func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"tag_name":"../nightly"}`)) },
		"pre-release":  func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"tag_name":"v1.0.0-rc.1"}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			u := &Updater{HTTPClient: srv.Client(), LatestAPI: srv.URL}
			if _, _, err := u.LatestRelease(t.Context()); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}
