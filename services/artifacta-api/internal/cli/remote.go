package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// cliScopes are what the CLI requests during login. offline_access asks the IdP
// for a refresh token so the CLI can mint fresh id_tokens without a new browser
// login. Mirrors the server's advertised scopes (api.cliScopes).
var cliScopes = []string{oidc.ScopeOpenID, "email", "profile", "offline_access"}

// cliAuthConfig is the decoded body of GET /.well-known/artifacta-cli — the info
// `artifacta login <url>` needs to bootstrap the loopback PKCE flow.
type cliAuthConfig struct {
	Auth     string   `json:"auth"` // "oidc" or "local"
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

// fetchCLIConfig GETs the discovery endpoint so login needs only the base URL.
func fetchCLIConfig(baseURL string) (cliAuthConfig, error) {
	var cfg cliAuthConfig
	endpoint := strings.TrimSuffix(baseURL, "/") + "/.well-known/artifacta-cli"
	resp, err := http.Get(endpoint)
	if err != nil {
		return cfg, fmt.Errorf("reach %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return cfg, fmt.Errorf("discovery failed: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode discovery response: %w", err)
	}
	return cfg, nil
}

// doRemote performs one authenticated JSON request against the server and is the
// single place request-building, the Bearer header, status checking, and error
// decoding live — every remote CLI call routes through it. When out is non-nil
// and the status matches wantStatus, the response body is decoded into it.
func doRemote(method, endpoint, token, contentType string, body io.Reader, wantStatus int, out any) error {
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// remoteURL joins the base URL and a path, trimming a trailing slash on the base.
func remoteURL(baseURL, path string) string {
	return strings.TrimSuffix(baseURL, "/") + path
}

// remoteTarget reports whether CLI commands should talk to a deployed server
// rather than the local store. It keys off the explicit LoggedIn marker set by
// `login` (the reliable signal) or a non-localhost base URL, so a logged-out CLI
// pointed at a real deployment errors instead of silently writing locally.
func remoteTarget(c config.Config) bool {
	return c.LoggedIn || (c.BaseURL != "" && !isLocalhost(c.BaseURL))
}

// isLocalhost reports whether rawURL's host is a loopback name/address.
func isLocalhost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1", "":
		return true
	default:
		return false
	}
}

// freshToken returns a currently-valid OIDC id_token for the Bearer credential,
// refreshing via the stored refresh token when the cached id_token is expired. It
// persists any rotated refresh token + new id_token + expiry back to the config.
// It fails closed with an explicit "run: artifacta login <url>" when there is no
// usable credential — never silently. Not-logged-in and a refresh that returns no
// id_token both clear stored creds so the next command re-logs in cleanly (D2).
func freshToken(c *config.Config) (string, error) {
	// Fast path: a still-valid cached id_token needs no network round trip.
	if c.AccessToken != "" && !tokenExpired(c.AccessTokenExpiry) {
		return c.AccessToken, nil
	}
	if c.RefreshToken == "" {
		return "", fmt.Errorf("not logged in — run: artifacta login <url>")
	}
	if c.OIDCIssuer == "" || c.OIDCClientID == "" {
		return "", fmt.Errorf("login is incomplete (missing issuer/client) — run: artifacta login <url>")
	}

	ctx := context.Background()
	provider, err := oidc.NewProvider(ctx, c.OIDCIssuer)
	if err != nil {
		return "", fmt.Errorf("oidc discovery: %w", err)
	}
	// Public client: no secret, PKCE-only. TokenSource refreshes on demand.
	cfg := &oauth2.Config{ClientID: c.OIDCClientID, Endpoint: provider.Endpoint(), Scopes: cliScopes}
	tok, err := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: c.RefreshToken}).Token()
	if err != nil {
		clearCreds(c)
		_ = config.Save(*c)
		return "", fmt.Errorf("session expired — run: artifacta login <url> (%v)", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		// The IdP refreshed the access_token but returned no id_token; our Bearer
		// scheme needs an id_token, so clear creds and ask for a fresh login
		// rather than looping on an unusable token (D2).
		clearCreds(c)
		_ = config.Save(*c)
		return "", fmt.Errorf("session expired — run: artifacta login <url>")
	}

	c.AccessToken = raw
	if tok.RefreshToken != "" { // refresh tokens may rotate
		c.RefreshToken = tok.RefreshToken
	}
	c.AccessTokenExpiry = idTokenExpiry(raw)
	if err := config.Save(*c); err != nil {
		return "", err
	}
	return raw, nil
}

// clearCreds wipes the stored session so the next command prompts a fresh login.
func clearCreds(c *config.Config) {
	c.AccessToken = ""
	c.RefreshToken = ""
	c.AccessTokenExpiry = ""
	c.LoggedIn = false
}

// tokenExpired reports whether an RFC3339 expiry is empty, unparseable, or within
// a 60s skew of now — any of which means "refresh before using".
func tokenExpired(expiry string) bool {
	if expiry == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, expiry)
	if err != nil {
		return true
	}
	return time.Now().Add(60 * time.Second).After(t)
}

// idTokenExpiry decodes a JWT's `exp` claim (no verification — the server always
// re-verifies) and returns it as RFC3339, or "" if it can't be read (which makes
// tokenExpired treat it as expired, forcing a refresh next time).
func idTokenExpiry(rawJWT string) string {
	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return ""
	}
	return time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339)
}

// meRemote GETs /me and returns the authenticated identity, proving the token is
// accepted by the server (the auth half of `artifacta doctor`).
func meRemote(baseURL, token string) (sub, email string, err error) {
	var out struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := doRemote(http.MethodGet, remoteURL(baseURL, "/me"), token, "", nil, http.StatusOK, &out); err != nil {
		return "", "", err
	}
	return out.Sub, out.Email, nil
}

// artifactRow is one row of the GET /artifacts listing (remote `ls`).
type artifactRow struct {
	Slug          string `json:"slug"`
	Title         string `json:"title"`
	Visibility    string `json:"visibility"`
	LatestVersion int    `json:"latest_version"`
	URL           string `json:"url"`
	CreatedAt     string `json:"created_at"`
}

// listRemote GETs the caller's own artifacts from the deployment.
func listRemote(baseURL, token string) ([]artifactRow, error) {
	var rows []artifactRow
	if err := doRemote(http.MethodGet, remoteURL(baseURL, "/artifacts"), token, "", nil, http.StatusOK, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}
