package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
)

// Service-account management (ADR-0028). Every command talks to the server's
// single admin endpoint with the caller's own login; the server decides who is
// an admin. Output mirrors qwproxy's `qw sa` so the two tools feel the same.

const saPath = "/serviceaccounts"

const saUsage = `usage:
  artifacta sa create <name> [--description <text>]
  artifacta sa list [--json]
  artifacta sa disable <name>                  all of its keys stop working; permanent
  artifacta sa key create <name> --ttl 30d|90d|never [--description <text>] [--json]
  artifacta sa key list <name> [--json]
  artifacta sa key revoke <name> <key-id>

A service account is a non-human identity (CI job, bot, agent) that owns API keys.
It publishes as sa:<name> and owns what it publishes. A bot authenticates with:
  ARTIFACTA_BASE_URL=<server> ARTIFACTA_API_KEY=<key> artifacta publish report.html
Only admins (the server's ARTIFACTA_SA_ADMIN_GROUPS / _EMAILS) can run these.`

func serviceAccount(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", saUsage)
	}
	switch args[0] {
	case "create":
		return saCreate(args[1:])
	case "list", "ls":
		return saList(args[1:])
	case "disable":
		return saDisable(args[1:])
	case "key", "keys":
		return saKey(args[1:])
	case "help", "-h", "--help":
		fmt.Println(saUsage)
		return nil
	default:
		return fmt.Errorf("unknown sa subcommand %q\n%s", args[0], saUsage)
	}
}

func saKey(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", saUsage)
	}
	switch args[0] {
	case "create":
		return saKeyCreate(args[1:])
	case "list", "ls":
		return saKeyList(args[1:])
	case "revoke":
		return saKeyRevoke(args[1:])
	default:
		return fmt.Errorf("unknown sa key subcommand %q\n%s", args[0], saUsage)
	}
}

// saArgs splits positional args from --flag value pairs and boolean flags, so
// flags may appear anywhere (matching `comment add --reply`).
func saArgs(args []string, valueFlags, boolFlags []string) (pos []string, vals map[string]string, bools map[string]bool, err error) {
	vals, bools = map[string]string{}, map[string]bool{}
	isIn := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		switch {
		case isIn(boolFlags, name):
			bools[name] = true
		case isIn(valueFlags, name):
			if !hasVal {
				if i+1 >= len(args) {
					return nil, nil, nil, fmt.Errorf("--%s needs a value", name)
				}
				val = args[i+1]
				i++
			}
			vals[name] = val
		default:
			return nil, nil, nil, fmt.Errorf("unknown flag --%s", name)
		}
	}
	return pos, vals, bools, nil
}

// saRequest sends one admin request with the caller's login and decodes the
// JSON response into out (when non-nil).
func saRequest(method string, params url.Values, body any, wantStatus int, out any) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	if !remoteTarget(c) {
		return fmt.Errorf("artifacta sa requires a remote server — run: artifacta login <url>")
	}
	token, err := freshToken(&c)
	if err != nil {
		return err
	}
	endpoint := remoteURL(c.BaseURL, saPath)
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	var rdr io.Reader
	contentType := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr, contentType = bytes.NewReader(b), "application/json"
	}
	return doRemote(method, endpoint, token, contentType, rdr, wantStatus, out)
}

type saRow struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	DisabledAt  *time.Time `json:"disabled_at"`
	ActiveKeys  int        `json:"active_keys"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

type saKeyRow struct {
	ID          string     `json:"id"`
	Prefix      string     `json:"prefix"`
	Description string     `json:"description"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	Status      string     `json:"status"`
	APIKey      string     `json:"api_key,omitempty"` // only on create
}

func saCreate(args []string) error {
	pos, vals, _, err := saArgs(args, []string{"description"}, nil)
	if err != nil || len(pos) != 1 {
		return fmt.Errorf("usage: artifacta sa create <name> [--description <text>]")
	}
	var sa saRow
	body := map[string]string{"name": pos[0], "description": vals["description"]}
	if err := saRequest(http.MethodPost, nil, body, http.StatusCreated, &sa); err != nil {
		return err
	}
	fmt.Printf("created service account %s (id %s)\n", sa.Name, sa.ID)
	fmt.Fprintf(os.Stderr, "Mint a key with: artifacta sa key create %s --ttl 30d|90d|never\n", sa.Name)
	return nil
}

func saList(args []string) error {
	pos, _, bools, err := saArgs(args, nil, []string{"json"})
	if err != nil || len(pos) != 0 {
		return fmt.Errorf("usage: artifacta sa list [--json]")
	}
	var resp struct {
		ServiceAccounts []saRow `json:"service_accounts"`
	}
	if err := saRequest(http.MethodGet, nil, nil, http.StatusOK, &resp); err != nil {
		return err
	}
	if bools["json"] {
		return writeJSONOut(resp.ServiceAccounts)
	}
	if len(resp.ServiceAccounts) == 0 {
		fmt.Fprintln(os.Stderr, "no service accounts")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDESCRIPTION\tCREATED BY\tACTIVE KEYS\tLAST USED\tSTATUS")
	for _, sa := range resp.ServiceAccounts {
		status := "active"
		if sa.DisabledAt != nil {
			status = "disabled"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
			sa.Name, dashIfEmpty(sa.Description), dashIfEmpty(sa.CreatedBy), sa.ActiveKeys, fmtTimePtr(sa.LastUsedAt), status)
	}
	return tw.Flush()
}

func saDisable(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: artifacta sa disable <name>")
	}
	if err := saRequest(http.MethodDelete, url.Values{"name": {args[0]}}, nil, http.StatusNoContent, nil); err != nil {
		return err
	}
	fmt.Printf("disabled service account %s; all of its keys are now invalid\n", args[0])
	return nil
}

func saKeyCreate(args []string) error {
	pos, vals, bools, err := saArgs(args, []string{"ttl", "description"}, []string{"json"})
	if err != nil || len(pos) != 1 || vals["ttl"] == "" {
		return fmt.Errorf("usage: artifacta sa key create <name> --ttl 30d|90d|never [--description <text>] [--json]")
	}
	name := pos[0]
	var k saKeyRow
	body := map[string]string{"ttl": vals["ttl"], "description": vals["description"]}
	if err := saRequest(http.MethodPost, url.Values{"name": {name}}, body, http.StatusCreated, &k); err != nil {
		return err
	}
	if bools["json"] {
		return writeJSONOut(k)
	}
	fmt.Printf("API key created for service account %s.\n", name)
	fmt.Printf("  id:      %s\n", k.ID)
	fmt.Printf("  expires: %s\n", fmtExpiry(k.ExpiresAt))
	fmt.Printf("\nThe key is shown once — store it in the bot's secret store now:\n\n  %s\n\n", k.APIKey)
	fmt.Fprintln(os.Stderr, "The bot authenticates with ARTIFACTA_API_KEY=<key> (plus ARTIFACTA_BASE_URL). It is not saved locally.")
	return nil
}

func saKeyList(args []string) error {
	pos, _, bools, err := saArgs(args, nil, []string{"json"})
	if err != nil || len(pos) != 1 {
		return fmt.Errorf("usage: artifacta sa key list <name> [--json]")
	}
	var resp struct {
		APIKeys []saKeyRow `json:"api_keys"`
	}
	if err := saRequest(http.MethodGet, url.Values{"name": {pos[0]}}, nil, http.StatusOK, &resp); err != nil {
		return err
	}
	if bools["json"] {
		return writeJSONOut(resp.APIKeys)
	}
	if len(resp.APIKeys) == 0 {
		fmt.Fprintf(os.Stderr, "service account %s has no keys\n", pos[0])
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPREFIX\tDESCRIPTION\tCREATED BY\tEXPIRES\tLAST USED\tSTATUS")
	for _, k := range resp.APIKeys {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			k.ID, k.Prefix, dashIfEmpty(k.Description), dashIfEmpty(k.CreatedBy),
			fmtExpiry(k.ExpiresAt), fmtTimePtr(k.LastUsedAt), k.Status)
	}
	return tw.Flush()
}

func saKeyRevoke(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: artifacta sa key revoke <name> <key-id>")
	}
	params := url.Values{"name": {args[0]}, "key_id": {args[1]}}
	if err := saRequest(http.MethodDelete, params, nil, http.StatusNoContent, nil); err != nil {
		return err
	}
	fmt.Printf("revoked key %s of service account %s\n", args[1], args[0])
	return nil
}

func writeJSONOut(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func fmtExpiry(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Local().Format("2006-01-02")
}
