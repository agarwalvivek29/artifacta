package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
)

const (
	// releasesAPI is the GitHub latest-release endpoint for the CLI.
	releasesAPI = "https://api.github.com/repos/agarwalvivek29/artifacta/releases/latest"
	// installCmd is the canonical one-liner that installs/upgrades the CLI.
	installCmd = "curl -fsSL https://raw.githubusercontent.com/agarwalvivek29/artifacta/main/install.sh | sh"
)

// latestRelease fetches the latest CLI release from GitHub, returning its version
// (tag without a leading "v") and the minimum server version it requires (parsed
// from a `min-server-version:` marker in the release body, empty when the release
// is independent of the deployed server version).
func latestRelease() (version, minServer string, err error) {
	resp, err := http.Get(releasesAPI)
	if err != nil {
		return "", "", fmt.Errorf("reach GitHub releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("github releases: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", fmt.Errorf("decode release: %w", err)
	}
	return strings.TrimPrefix(rel.TagName, "v"), parseMinServerVersion(rel.Body), nil
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

// serverVersion reads the deployed server's version (and compatibility contract)
// from GET /version.
func serverVersion(baseURL string) (version, minCLI string, capabilities []string, err error) {
	var out struct {
		Version      string   `json:"version"`
		MinCLI       string   `json:"min_cli_version"`
		Capabilities []string `json:"capabilities"`
	}
	if err := doRemote(http.MethodGet, remoteURL(baseURL, "/version"), "", "", nil, http.StatusOK, &out); err != nil {
		return "", "", nil, err
	}
	return out.Version, out.MinCLI, out.Capabilities, nil
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

// upgradeAdvice is the outcome of the version check.
type upgradeAdvice struct {
	upToDate        bool // already on the latest release
	blockedByServer bool // a newer CLI exists but the deployment is too old
	message         string
}

// decideUpgrade is the pure decision at the heart of `artifacta upgrade`. It
// branches exactly as specified: an enhancement independent of the server
// (minServer == "") prompts an upgrade unconditionally; one that depends on the
// server checks the deployed version and only prompts when the deployment is new
// enough, otherwise it says to upgrade the server first. haveServer is false when
// the CLI is not logged in to a deployment (so no server version is known).
func decideUpgrade(current, latest, minServer, serverVer string, haveServer bool) upgradeAdvice {
	if current != "dev" && cmpVersion(latest, current) <= 0 {
		return upgradeAdvice{upToDate: true, message: fmt.Sprintf("artifacta %s is up to date.", current)}
	}
	newer := fmt.Sprintf("a newer artifacta CLI is available: %s (you have %s).", latest, current)

	// Independent of the deployed server version → prompt unconditionally.
	if minServer == "" {
		return upgradeAdvice{message: newer + "\nupgrade:\n  " + installCmd}
	}

	// Depends on the server being >= minServer.
	if !haveServer {
		return upgradeAdvice{message: newer +
			fmt.Sprintf("\nit needs a deployed server >= %s. log in to a deployment (artifacta login <url>) to check compatibility, or upgrade:\n  %s", minServer, installCmd)}
	}
	if cmpVersion(serverVer, minServer) >= 0 {
		return upgradeAdvice{message: newer +
			fmt.Sprintf("\ncompatible with your deployment (%s ≥ required %s). upgrade:\n  %s", serverVer, minServer, installCmd)}
	}
	return upgradeAdvice{blockedByServer: true, message: newer +
		fmt.Sprintf("\nit needs a deployed server >= %s, but your deployment is %s.\nupgrade the server first, then upgrade the CLI.", minServer, serverVer)}
}

// upgrade checks GitHub for a newer CLI and advises whether to upgrade, taking the
// deployed server's version into account when the new release depends on it. It
// only advises — it never self-updates the binary.
func upgrade() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	latest, minServer, err := latestRelease()
	if err != nil {
		return err
	}

	serverVer := ""
	haveServer := false
	// Only bother reading the deployed version when the new release depends on it
	// and we're actually logged in to a deployment.
	if minServer != "" && remoteTarget(c) {
		sv, _, _, verr := serverVersion(c.BaseURL)
		if verr != nil {
			fmt.Printf("(could not read the deployed version from %s: %v)\n", c.BaseURL, verr)
		} else {
			serverVer, haveServer = sv, true
		}
	}

	adv := decideUpgrade(Version, latest, minServer, serverVer, haveServer)
	fmt.Println(adv.message)
	return nil
}
