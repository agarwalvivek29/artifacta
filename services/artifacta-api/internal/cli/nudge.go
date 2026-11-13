package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
)

// The daily version nudge (ADR-0029): commands that talk to a deployment check
// its /version at most once per nudgeEvery and, on a mismatch, print one line to
// stderr pointing at `artifacta upgrade`. It never fails or noticeably delays a
// command: the check runs alongside the command and is abandoned after
// nudgeTimeout.
const (
	nudgeEvery   = 24 * time.Hour
	nudgeTimeout = 3 * time.Second
	nudgeFile    = "update-check.json"
)

// Test seams.
var (
	nudgeOut       io.Writer = os.Stderr
	nudgeNow                 = time.Now
	nudgeFetchTime           = nudgeTimeout
)

// nudgeCommands are the commands that talk to a deployment.
var nudgeCommands = map[string]bool{
	"publish": true, "versions": true, "share": true, "visibility": true, "unshare": true,
	"label": true, "edit": true, "comment": true, "ls": true, "whoami": true,
	"sa": true, "serviceaccount": true, "serviceaccounts": true, "audit": true,
}

// updateCheck is the on-disk cache of the last check.
type updateCheck struct {
	BaseURL       string    `json:"base_url"`
	CheckedAt     time.Time `json:"checked_at"`
	ServerVersion string    `json:"server_version,omitempty"`
}

// startNudge begins the check for cmd and returns a func that waits for it
// (bounded) and prints the nudge. Both are no-ops when the check is skipped.
func startNudge(cmd string) (finish func()) {
	base, ok := nudgeTarget(cmd)
	if !ok {
		return func() {}
	}
	done := make(chan string, 1)
	go func() { done <- fetchVersionQuick(base) }()
	return func() {
		var sv string
		select {
		case sv = <-done:
		case <-time.After(nudgeFetchTime):
		}
		// Record the attempt even when it failed, so an unreachable server costs
		// one try a day, not one per command.
		saveUpdateCheck(updateCheck{BaseURL: base, CheckedAt: nudgeNow().UTC(), ServerVersion: sv})
		if msg := nudgeMessage(Version, sv); msg != "" {
			fmt.Fprintln(nudgeOut, msg)
		}
	}
}

// nudgeTarget decides whether cmd should check, returning the deployment URL.
func nudgeTarget(cmd string) (string, bool) {
	if !nudgeCommands[cmd] || Version == "dev" {
		return "", false
	}
	for _, env := range []string{"ARTIFACTA_NO_UPDATE_CHECK", "ARTIFACTA_API_KEY", "CI"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" && v != "0" && !strings.EqualFold(v, "false") {
			return "", false
		}
	}
	c, err := config.Load()
	if err != nil || !remoteTarget(c) || c.BaseURL == "" {
		return "", false
	}
	if last, ok := loadUpdateCheck(); ok && last.BaseURL == c.BaseURL && nudgeNow().Sub(last.CheckedAt) < nudgeEvery {
		return "", false
	}
	return c.BaseURL, true
}

// nudgeMessage is the one-line notice, or "" when nothing should be said.
func nudgeMessage(current, serverVer string) string {
	switch {
	case serverVer == "" || cmpVersion(current, serverVer) == 0:
		return ""
	case cmpVersion(current, serverVer) < 0:
		return fmt.Sprintf("artifacta: your CLI (%s) is older than the deployment (%s). run: artifacta upgrade", current, serverVer)
	default:
		return fmt.Sprintf("artifacta: your CLI (%s) is newer than the deployment (%s). to match it, run: artifacta upgrade", current, serverVer)
	}
}

// fetchVersionQuick reads GET /version with a short timeout; "" on any failure.
func fetchVersionQuick(baseURL string) string {
	client := &http.Client{Timeout: nudgeTimeout}
	resp, err := client.Get(remoteURL(baseURL, "/version"))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out) != nil || out.Version == "dev" {
		return ""
	}
	return out.Version
}

func loadUpdateCheck() (updateCheck, bool) {
	var u updateCheck
	b, err := os.ReadFile(filepath.Join(config.Dir(), nudgeFile))
	if err != nil || json.Unmarshal(b, &u) != nil {
		return updateCheck{}, false
	}
	return u, true
}

func saveUpdateCheck(u updateCheck) {
	b, err := json.Marshal(u)
	if err != nil {
		return
	}
	dir := config.Dir()
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, nudgeFile), b, 0o600)
}
