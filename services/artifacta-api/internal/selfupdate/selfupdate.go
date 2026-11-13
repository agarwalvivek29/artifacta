// Package selfupdate installs an artifacta CLI binary from GitHub Releases
// (ADR-0029). It mirrors install.sh: the same asset naming
// (artifacta_<ver>_<os>_<arch>.tar.gz) and checksums.txt, so `artifacta upgrade`
// and a fresh curl-install land on byte-identical binaries. Unlike the installer
// it fails closed when checksums are missing: a self-updater must never replace
// itself with an unverified binary. Ported from quickwit-cli's selfupdate.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
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

// Release locations. Plain string vars (not consts) only so a manual end-to-end
// test can point a build at a local release server via -ldflags -X; there is
// deliberately no environment override.
var (
	downloadBase = "https://github.com/agarwalvivek29/artifacta/releases/download"
	latestAPI    = "https://api.github.com/repos/agarwalvivek29/artifacta/releases/latest"
)

// maxBinary bounds extraction so a malformed archive can't fill the disk.
const maxBinary = 512 << 20

// versionRe accepts release versions only (e.g. 0.2.1, 1.0.0-rc.1), so a value
// read from a server or flag can never smuggle path segments into a download URL.
var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// ValidVersion reports whether v (with or without a leading "v") is a release version.
func ValidVersion(v string) bool { return versionRe.MatchString(Normalize(v)) }

// ErrUnsupported means this platform has no prebuilt binary or can't replace a
// running executable in place; the caller should fall back to the installer.
var ErrUnsupported = errors.New("self-update is not supported on this platform")

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
}

// New returns an Updater for the real GitHub repo and this platform.
func New() *Updater {
	return &Updater{
		HTTPClient:   &http.Client{Timeout: 2 * time.Minute},
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		DownloadBase: downloadBase,
		LatestAPI:    latestAPI,
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

// Install downloads version, verifies it, and replaces the executable at target
// (normally Executable()). It never touches target unless verification passed.
func (u *Updater) Install(ctx context.Context, version, target string) error {
	binPath, cleanup, err := u.download(ctx, version)
	if err != nil {
		return err
	}
	defer cleanup()
	return replaceBinary(binPath, target)
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

// download fetches and verifies the release asset, returning the extracted
// binary's path and a cleanup func for its temp dir.
func (u *Updater) download(ctx context.Context, version string) (string, func(), error) {
	if !ValidVersion(version) {
		return "", nil, fmt.Errorf("%q is not a release version (want e.g. 0.2.1)", version)
	}
	tag, ver := TagAndVersion(version)
	asset, err := u.AssetName(ver)
	if err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp("", "artifacta-upgrade-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	fail := func(err error) (string, func(), error) { cleanup(); return "", nil, err }

	assetPath := filepath.Join(tmp, asset)
	if err := u.get(ctx, u.DownloadBase+"/"+tag+"/"+asset, assetPath); err != nil {
		return fail(fmt.Errorf("download %s: %w", asset, err))
	}
	sumsPath := filepath.Join(tmp, "checksums.txt")
	if err := u.get(ctx, u.DownloadBase+"/"+tag+"/checksums.txt", sumsPath); err != nil {
		return fail(fmt.Errorf("no checksums.txt for %s (%v); refusing to install an unverified binary", tag, err))
	}
	if err := verifyChecksum(assetPath, sumsPath, asset); err != nil {
		return fail(err)
	}
	binPath, err := extractBinary(assetPath, tmp)
	if err != nil {
		return fail(err)
	}
	return binPath, cleanup, nil
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

func (u *Updater) get(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxBinary)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// verifyChecksum requires assetPath's SHA-256 to match its line in a
// sha256sum/GoReleaser-style "<hex>  <name>" file.
func verifyChecksum(assetPath, sumsPath, assetName string) error {
	f, err := os.Open(assetPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))

	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
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

// extractBinary pulls the artifacta binary out of the .tar.gz into dir.
func extractBinary(archivePath, dir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read archive: %w", err)
		}
		if filepath.Base(hdr.Name) != binaryName || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out := filepath.Join(dir, binaryName+"-new")
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(w, io.LimitReader(tr, maxBinary)); err != nil {
			_ = w.Close()
			return "", err
		}
		if err := w.Close(); err != nil {
			return "", err
		}
		return out, nil
	}
	return "", fmt.Errorf("archive did not contain an %q binary", binaryName)
}

// replaceBinary stages newBin next to target (same filesystem) and renames it
// over target, which is atomic and works while target is running on Linux and
// macOS. A permission failure is returned wrapping os.ErrPermission so the
// caller can suggest sudo or the installer.
func replaceBinary(newBin, target string) error {
	dir := filepath.Dir(target)
	staged, err := os.CreateTemp(dir, ".artifacta-upgrade-*")
	if err != nil {
		return fmt.Errorf("stage new binary in %s: %w", dir, err)
	}
	stagedPath := staged.Name()
	in, err := os.Open(newBin)
	if err != nil {
		_ = staged.Close()
		_ = os.Remove(stagedPath)
		return err
	}
	_, err = io.Copy(staged, in)
	_ = in.Close()
	if cerr := staged.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(stagedPath, 0o755)
	}
	if err == nil {
		err = os.Rename(stagedPath, target)
	}
	if err != nil {
		_ = os.Remove(stagedPath)
		return fmt.Errorf("replace %s: %w", target, err)
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
