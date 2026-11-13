// Package selfupdate installs an artifacta CLI binary from GitHub Releases
// (ADR-0029). It uses the same assets as install.sh
// (artifacta_<ver>_<os>_<arch>.tar.gz and checksums.txt), so `artifacta upgrade`
// and a fresh curl-install land on byte-identical binaries. It also requires
// checksums.txt.sig, an Ed25519 signature made in the release workflow with a
// key only CI holds, checked against the public keys built into this binary. A
// stolen GitHub token can replace release assets but cannot sign them. Every
// check fails closed, and all three files are verified and extracted from
// memory, so nothing on disk can change between a check and its use. Ported
// from quickwit-cli's selfupdate.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const binaryName = "artifacta"

// Release locations and keys. Plain string vars (not consts) only so a manual
// end-to-end test can point a build at a local release server and test key via
// -ldflags -X; there is deliberately no environment override.
var (
	// releasePublicKeys verifies checksums.txt.sig: comma-separated raw Ed25519
	// public keys, base64. Any one verifying is enough, so a release can ship the
	// next key before the old one is retired (rotation). The first must match
	// /.github/release-signing.pub.pem, which the release workflow checks its own
	// signature against (TestReleaseKeyMatchesPEM).
	releasePublicKeys = "fsSmCoIF4yhobBOqPmoXS/CxO1Gdm402KN6S/Mz9PWE="
	downloadBase      = "https://github.com/agarwalvivek29/artifacta/releases/download"
	latestAPI         = "https://api.github.com/repos/agarwalvivek29/artifacta/releases/latest"
)

const (
	maxBinary = 512 << 20 // bounds a download or extraction
	maxSums   = 1 << 20   // checksums.txt is a few lines
	maxSig    = 1 << 10   // a signature is 64 bytes
)

// versionRe accepts plain release versions only (e.g. 0.2.1), so a value read
// from a server or flag can never smuggle path segments into a download URL.
// Pre-releases are rejected: the CLI's numeric version comparison can't order
// them, and artifacta does not publish them.
var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// ValidVersion reports whether v (with or without a leading "v") is a release version.
func ValidVersion(v string) bool { return versionRe.MatchString(Normalize(v)) }

// ErrUnsupported means this platform has no prebuilt binary or can't replace a
// running executable in place; the caller should fall back to the installer.
var ErrUnsupported = errors.New("self-update is not supported on this platform")

// ErrUnsigned means the release has no checksums.txt.sig at all (a 404). It is
// never downgraded to "install it some other way": an unsigned release is either
// from before signing started or has been tampered with.
var ErrUnsigned = errors.New("release is not signed")

// errNotFound marks a 404 from the release server.
var errNotFound = errors.New("not found")

// Updater downloads and installs release binaries. The fields are exported so
// tests can point it at an httptest server and a fixed platform.
type Updater struct {
	HTTPClient   *http.Client
	GOOS, GOARCH string
	// DownloadBase is the release-download base; assets are fetched from
	// <DownloadBase>/<tag>/<asset>.
	DownloadBase string
	// LatestAPI returns the latest release JSON ({"tag_name": ...}).
	LatestAPI string
	// PublicKeys verify the release's checksums.txt.sig; any one is enough.
	PublicKeys []ed25519.PublicKey
}

func mustPublicKeys(list string) []ed25519.PublicKey {
	var keys []ed25519.PublicKey
	for _, b64 := range strings.Split(list, ",") {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil || len(k) != ed25519.PublicKeySize {
			panic("selfupdate: bad built-in release public key")
		}
		keys = append(keys, ed25519.PublicKey(k))
	}
	return keys
}

// New returns an Updater for the real GitHub repo and this platform.
func New() *Updater {
	return &Updater{
		// No whole-request timeout: a ~25 MB download over a slow VPN must not be
		// cut off. Stalled servers are caught by the header and idle timeouts;
		// the caller's context bounds the total.
		HTTPClient: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		}},
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		DownloadBase: downloadBase,
		LatestAPI:    latestAPI,
		PublicKeys:   mustPublicKeys(releasePublicKeys),
	}
}

// LatestRelease resolves the newest published release: its version (no leading
// "v") and its release notes, which may carry a min-server-version marker.
func (u *Updater) LatestRelease(ctx context.Context) (version, body string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.LatestAPI, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("resolve latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("resolve latest release: %s", resp.Status)
	}
	var doc struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return "", "", fmt.Errorf("decode latest release: %w", err)
	}
	if !ValidVersion(doc.TagName) {
		return "", "", fmt.Errorf("latest release tag %q is not a release version", doc.TagName)
	}
	return Normalize(doc.TagName), doc.Body, nil
}

// Install downloads version, verifies its signature and checksum, and replaces
// the executable at target (normally Executable()). target is never touched
// unless every check passed.
func (u *Updater) Install(ctx context.Context, version, target string) error {
	if !ValidVersion(version) {
		return fmt.Errorf("%q is not a release version (want e.g. 0.2.1)", version)
	}
	tag, ver := TagAndVersion(version)
	asset, err := u.AssetName(ver)
	if err != nil {
		return err
	}
	base := u.DownloadBase + "/" + tag + "/"

	// Signature first: nothing else is trusted until checksums.txt verifies.
	sums, err := u.get(ctx, base+"checksums.txt", maxSums)
	if err != nil {
		return fmt.Errorf("download checksums.txt for %s: %w; refusing to install an unverified binary", tag, err)
	}
	sig, err := u.get(ctx, base+"checksums.txt.sig", maxSig)
	if errors.Is(err, errNotFound) {
		return fmt.Errorf("%w: %s has no checksums.txt.sig; refusing to install it", ErrUnsigned, tag)
	}
	if err != nil {
		return fmt.Errorf("download checksums.txt.sig for %s: %w", tag, err)
	}
	if err := verifySignature(u.PublicKeys, sums, sig); err != nil {
		return err
	}
	archive, err := u.get(ctx, base+asset, maxBinary)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	if err := verifyChecksum(archive, sums, asset); err != nil {
		return err
	}
	bin, err := extractBinary(archive)
	if err != nil {
		return err
	}
	return replaceBinary(bin, target)
}

// Executable returns the resolved path of the running binary (symlinks followed,
// so a Homebrew-style symlink updates the real file).
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// AssetName is the release asset for ver on this platform. Windows ships a .zip
// and can't rename over a running .exe, so it is ErrUnsupported (use install.sh
// or download the zip).
func (u *Updater) AssetName(ver string) (string, error) {
	switch u.GOOS {
	case "linux", "darwin":
	default:
		return "", fmt.Errorf("%w: %s (prebuilt self-update binaries: linux, darwin)", ErrUnsupported, u.GOOS)
	}
	switch u.GOARCH {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("%w: architecture %s (prebuilt binaries: amd64, arm64)", ErrUnsupported, u.GOARCH)
	}
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", binaryName, Normalize(ver), u.GOOS, u.GOARCH), nil
}

// get fetches url into memory, refusing bodies over limit.
func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: larger than %d bytes", url, limit)
	}
	return b, nil
}

// verifySignature requires sig to be a valid Ed25519 signature of sums under
// one of keys.
func verifySignature(keys []ed25519.PublicKey, sums, sig []byte) error {
	if len(keys) == 0 {
		return errors.New("no release public key configured; refusing to install")
	}
	if len(sig) == ed25519.SignatureSize {
		for _, k := range keys {
			if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, sums, sig) {
				return nil
			}
		}
	}
	return errors.New("checksums.txt signature does not verify against the built-in release key; refusing to install")
}

// verifyChecksum requires archive's SHA-256 to match its line in a
// sha256sum-style "<hex>  <name>" file.
func verifyChecksum(archive, sums []byte, assetName string) error {
	s := sha256.Sum256(archive)
	got := hex.EncodeToString(s[:])
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		// sha256sum's binary mode prefixes the name with '*'.
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == assetName {
			if strings.EqualFold(fields[0], got) {
				return nil
			}
			return fmt.Errorf("checksum mismatch for %s; refusing to install", assetName)
		}
	}
	return fmt.Errorf("checksums.txt has no entry for %s; refusing to install", assetName)
}

// extractBinary returns the regular-file artifacta entry of the .tar.gz.
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if filepath.Base(hdr.Name) != binaryName || hdr.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxBinary+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxBinary {
			return nil, errors.New("binary in archive is too large")
		}
		return b, nil
	}
	return nil, fmt.Errorf("archive did not contain an %q binary", binaryName)
}

// replaceBinary writes bin to a staged file next to target (same filesystem),
// fsyncs it, and renames it over target, which is atomic and works while
// target is running on Linux and macOS. The new file keeps target's permission
// bits (never group/world-writable) and, when we can (running as root), its
// owner, so a `sudo artifacta upgrade` doesn't leave a user's binary
// root-owned. A permission failure wraps os.ErrPermission so the caller can
// suggest sudo.
func replaceBinary(bin []byte, target string) error {
	mode := os.FileMode(0o755)
	var orig os.FileInfo
	if fi, err := os.Stat(target); err == nil {
		orig, mode = fi, (fi.Mode().Perm()|0o100)&^0o022
	}
	dir := filepath.Dir(target)
	staged, err := os.CreateTemp(dir, ".artifacta-upgrade-*")
	if err != nil {
		return fmt.Errorf("stage new binary in %s: %w", dir, err)
	}
	stagedPath := staged.Name()
	_, err = staged.Write(bin)
	if err == nil {
		err = staged.Sync()
	}
	if cerr := staged.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(stagedPath, mode)
	}
	if err == nil && orig != nil {
		preserveOwner(stagedPath, orig)
	}
	if err == nil {
		err = os.Rename(stagedPath, target)
	}
	if err != nil {
		_ = os.Remove(stagedPath)
		return fmt.Errorf("replace %s: %w", target, err)
	}
	if d, derr := os.Open(dir); derr == nil { // persist the rename itself
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// TagAndVersion turns "0.3.1" or "v0.3.1" into the git tag ("v0.3.1") and the
// bare version used in asset names ("0.3.1").
func TagAndVersion(v string) (tag, version string) {
	version = Normalize(v)
	return "v" + version, version
}

// Normalize strips whitespace and a leading "v".
func Normalize(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}
