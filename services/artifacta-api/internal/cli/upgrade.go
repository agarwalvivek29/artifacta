package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/selfupdate"
	"golang.org/x/term"
)

// installBase is the install-script URL, used to build version-pinned commands
// for when the CLI can't replace itself (no write access, Windows).
const installBase = "https://raw.githubusercontent.com/agarwalvivek29/artifacta/main/install.sh"

// pinnedInstall returns the install one-liner pinned to an exact version.
func pinnedInstall(ver string) string {
	return fmt.Sprintf("curl -fsSL %s | ARTIFACTA_VERSION=%s sh", installBase, ver)
}

// Test seams: the updater, the path of the binary being replaced, and the
// y/N prompt.
var (
	newUpdater     = selfupdate.New
	executablePath = selfupdate.Executable
	confirm        = confirmOnTerminal
)

// confirmOnTerminal asks a y/N question on stdin. interactive is false when
// stdin is not a terminal, so scripts never block on a prompt.
func confirmOnTerminal(question string) (yes, interactive bool) {
	// A real terminal, not just any character device (/dev/null is one too).
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, false
	}
	fmt.Fprint(os.Stderr, question) // stays visible when stdout is redirected
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", true
}

// parseMinServerVersion extracts a `min-server-version: X.Y.Z` marker from a
// release body (case-insensitive). Absent → "" (the enhancement is independent of
// the deployed server version). This is the convention the release process uses
// to declare a server-version dependency (ADR-0021).
func parseMinServerVersion(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ">"))
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "min-server-version:") {
			return strings.TrimSpace(line[len("min-server-version:"):])
		}
	}
	return ""
}

// errNotRelease means /version answered but with something that isn't a plain
// release version (a dev build, or anything a spoofed server sends).
var errNotRelease = errors.New("not a release version")

// versionClient bounds GET /version: it is unauthenticated, so a hanging or
// hostile server must not hang the CLI.
var versionClient = &http.Client{Timeout: 10 * time.Second}

// serverVersion reads the deployed server's version (and compatibility
// contract) from GET /version. Nothing the server sends is echoed: a non-200
// reports only the status, and a version that isn't a plain release is
// errNotRelease, so it never reaches the terminal or becomes an install target.
func serverVersion(baseURL string) (version, minCLI string, capabilities []string, err error) {
	resp, err := versionClient.Get(remoteURL(baseURL, "/version"))
	if err != nil {
		return "", "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", nil, fmt.Errorf("GET /version: %s", resp.Status)
	}
	var out struct {
		Version      string   `json:"version"`
		MinCLI       string   `json:"min_cli_version"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return "", "", nil, fmt.Errorf("decode /version: %w", err)
	}
	if !selfupdate.ValidVersion(out.Version) {
		return "", "", nil, errNotRelease
	}
	return selfupdate.Normalize(out.Version), out.MinCLI, out.Capabilities, nil
}

// cmpVersion compares dotted numeric versions (e.g. "0.0.3" vs "0.1.0"),
// returning -1, 0, or 1. A leading "v" is ignored; non-numeric or missing
// segments count as 0, so "0.0" == "0.0.0".
func cmpVersion(a, b string) int {
	as := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bs := strings.Split(strings.TrimPrefix(b, "v"), ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		ai, bi := 0, 0
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	return 0
}

// decideMatch is the deployment-anchored advice: the safest CLI to run against a
// deployment is the CLI of the same version (server and CLI ship from one
// binary per release, ADR-0021). login and doctor print it on a mismatch.
func decideMatch(current, serverVer string) string {
	switch {
	case current != "dev" && cmpVersion(current, serverVer) == 0:
		return fmt.Sprintf("artifacta %s matches your deployment — compatible.", current)
	case current == "dev":
		return fmt.Sprintf("you are on a dev build; your deployment runs %s. to install the matching release:\n  artifacta upgrade --version %s", serverVer, serverVer)
	case cmpVersion(current, serverVer) < 0:
		return fmt.Sprintf("your CLI (%s) is older than your deployment (%s). to install the matching version:\n  artifacta upgrade", current, serverVer)
	default:
		return fmt.Sprintf("your CLI (%s) is newer than your deployment (%s). to install the deployment's version (a downgrade):\n  artifacta upgrade\nor upgrade the deployment to %s.", current, serverVer, current)
	}
}

// noteVersionMismatch best-effort warns when the running CLI's version differs
// from the deployment it talks to. It prints nothing when they match and never
// fails the caller when the server can't be reached. Used right after login.
func noteVersionMismatch(baseURL string) {
	sv, _, _, err := serverVersion(baseURL)
	if err != nil {
		return
	}
	if Version != "dev" && cmpVersion(Version, sv) == 0 {
		return
	}
	fmt.Println(decideMatch(Version, sv))
}

const upgradeUsage = `usage: artifacta upgrade [--check] [--latest | --version <x.y.z>]

Installs the artifacta CLI that matches your deployment (upgrade or downgrade),
or the latest release when you are not logged in. The release is downloaded from
GitHub, its signature and SHA-256 are verified, and only then does it replace
this binary.

  --check            show the current and target versions; install nothing
  --latest           install the newest release instead of the deployment's version
  --version <x.y.z>  install exactly this version
  --yes              don't ask before installing an older version than this one

Installing an older version because the deployment runs it asks first (y/N);
without a terminal it needs --yes or an explicit --version.
`

// upgrade replaces the running CLI with the target release (ADR-0029). Running
// the command is the consent; it never updates anything in the background.
func upgrade(args []string) error {
	var check, latest, yes bool
	var pin string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--check":
			check = true
		case a == "--latest":
			latest = true
		case a == "--yes" || a == "-y":
			yes = true
		case a == "--version":
			if i+1 >= len(args) {
				return errors.New("--version needs a value, e.g. --version 0.2.1")
			}
			i++
			pin = args[i]
		case strings.HasPrefix(a, "--version="):
			pin = strings.TrimPrefix(a, "--version=")
		case a == "-h" || a == "--help" || a == "help":
			fmt.Print(upgradeUsage)
			return nil
		default:
			return fmt.Errorf("unknown upgrade argument %q\n%s", a, upgradeUsage)
		}
	}
	if latest && pin != "" {
		return errors.New("use either --latest or --version, not both")
	}
	if pin != "" && !selfupdate.ValidVersion(pin) {
		return fmt.Errorf("%q is not a release version (want e.g. 0.2.1)", pin)
	}

	ctx := context.Background()
	up := newUpdater()
	target, source, err := upgradeTarget(ctx, up, pin, latest)
	if err != nil {
		return err
	}
	fmt.Printf("current: artifacta %s\n", Version)
	fmt.Printf("target:  artifacta %s (%s)\n", target, source)

	if Version != "dev" && cmpVersion(Version, target) == 0 {
		fmt.Println("already up to date.")
		return nil
	}
	downgrade := Version != "dev" && cmpVersion(target, Version) < 0
	if check {
		if downgrade && pin == "" {
			fmt.Printf("would install %s, older than this CLI (asks first; run without --check).\n", target)
		} else {
			fmt.Printf("would install %s (run without --check to install).\n", target)
		}
		return nil
	}
	// A downgrade the user didn't name explicitly needs consent: the target came
	// from the deployment's unauthenticated /version (or GitHub), and an older
	// release may carry fixed bugs.
	if downgrade && pin == "" && !yes {
		ok, interactive := confirm(fmt.Sprintf("install artifacta %s, older than this %s? [y/N] ", target, Version))
		if !interactive {
			return fmt.Errorf("refusing to install older %s over %s without confirmation; rerun with --yes, or: artifacta upgrade --version %s", target, Version, target)
		}
		if !ok {
			fmt.Println("not changed.")
			return nil
		}
	}
	// A dev build is usually a `go build`/`go install` binary; don't overwrite it
	// unless the user named a version on purpose.
	if Version == "dev" && pin == "" && !latest {
		fmt.Printf("this is a dev build; not replacing it. to install the release anyway:\n  artifacta upgrade --version %s\n", target)
		return nil
	}

	exe, err := executablePath()
	if err != nil {
		return fmt.Errorf("find the running binary: %w\ninstall instead:\n  %s", err, pinnedInstall(target))
	}
	fmt.Printf("downloading and verifying artifacta %s…\n", target)
	if err := up.Install(ctx, target, exe); err != nil {
		switch {
		case errors.Is(err, os.ErrPermission):
			// Name the binary by path: sudo's secure_path may not find "artifacta",
			// and the version stays explicit because sudo has a different login.
			return fmt.Errorf("%v\nno write access to %s. rerun with sudo:\n  sudo %s upgrade --version %s\nor install with:\n  %s", err, exe, exe, target, pinnedInstall(target))
		case errors.Is(err, selfupdate.ErrUnsigned):
			// Never point at another install path: a missing signature is either
			// a pre-signing release (before 0.2.1) or tampering.
			return fmt.Errorf("%v\n%s is unchanged. releases are signed from 0.2.1 on; an unsigned one may have been tampered with", err, exe)
		case errors.Is(err, selfupdate.ErrUnsupported), selfupdate.IsReadOnlyFS(err):
			return fmt.Errorf("%v\ninstall instead:\n  %s", err, pinnedInstall(target))
		default:
			return fmt.Errorf("upgrade failed, %s is unchanged: %w", exe, err)
		}
	}
	// Prove the swap took: the binary now on disk must report the target.
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, verr := exec.CommandContext(vctx, exe, "version").Output()
	got := strings.TrimSpace(string(out))
	if verr != nil || got != "artifacta "+target {
		return fmt.Errorf("installed %s at %s, but it reports %q (%v); reinstall with:\n  %s", target, exe, got, verr, pinnedInstall(target))
	}
	fmt.Printf("upgraded %s → %s\n", exe, got)
	return nil
}

// upgradeTarget picks the version to install and says where it came from:
// --version, then --latest, then the logged-in deployment's /version, then the
// latest GitHub release.
func upgradeTarget(ctx context.Context, up *selfupdate.Updater, pin string, latest bool) (target, source string, err error) {
	if pin != "" {
		return selfupdate.Normalize(pin), "requested", nil
	}
	if !latest {
		c, err := config.Load()
		if err != nil {
			return "", "", err
		}
		if remoteTarget(c) {
			sv, _, _, verr := serverVersion(c.BaseURL)
			// The deployment is the compatibility anchor (ADR-0021): never guess.
			choose := "choose explicitly:\n  artifacta upgrade --latest\n  artifacta upgrade --version <x.y.z>"
			if errors.Is(verr, errNotRelease) {
				return "", "", fmt.Errorf("deployment %s isn't running a release build, so there's no matching CLI to install; %s", c.BaseURL, choose)
			}
			if verr != nil {
				return "", "", fmt.Errorf("can't read the deployment's version from %s (%v).\nare you on the VPN? rerun, or %s", c.BaseURL, verr, choose)
			}
			return sv, "matches your deployment " + c.BaseURL, nil
		}
	}
	ver, notes, err := up.LatestRelease(ctx)
	if err != nil {
		return "", "", fmt.Errorf("%w\n(pin one instead: artifacta upgrade --version <x.y.z>)", err)
	}
	source = "latest release"
	if min := parseMinServerVersion(notes); min != "" {
		source += ", needs a deployment ≥ " + min
	}
	return ver, source, nil
}
