package cli

import (
	"bytes"
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
	Auth        string   `json:"auth"` // "oidc" or "local"
	Issuer      string   `json:"issuer"`
	ClientID    string   `json:"client_id"`
	Scopes      []string `json:"scopes"`
	RedirectURI string   `json:"redirect_uri"` // fixed loopback URI to bind, if the server sets one
}

// loopbackTarget splits a fixed loopback redirect URI into the host:port to bind
// and the callback path, so `artifacta login` can bind the exact port the IdP has
// registered. It requires an http loopback URI (127.0.0.1 / localhost / ::1);
// anything else is rejected so we never bind a non-loopback address.
func loopbackTarget(redirectURI string) (hostport, path string, err error) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return "", "", fmt.Errorf("bad redirect uri %q: %w", redirectURI, err)
	}
	if u.Scheme != "http" {
		return "", "", fmt.Errorf("redirect uri %q must be http loopback", redirectURI)
	}
	if u.Port() == "" {
		return "", "", fmt.Errorf("redirect uri %q must include a port", redirectURI)
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
	default:
		return "", "", fmt.Errorf("redirect uri %q must be a loopback host", redirectURI)
	}
	path = u.Path
	if path == "" {
		path = "/callback"
	}
	return u.Host, path, nil
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

// looksLikeEmail reports whether s should be sent as an invite-by-email grant
// rather than a subject. Deliberately permissive — a single '@' with a dot in
// the domain part — mirroring the server's api.validEmail; the server re-validates,
// so this only picks the request shape ({"email"} vs {"grantee_sub"}).
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at != strings.LastIndexByte(s, '@') || at == len(s)-1 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return strings.Contains(s[at+1:], ".")
}

// removeGrantRemote DELETEs a grant at <baseURL>/artifacts/<slug>/grants/<grantee>
// with `Authorization: Bearer <token>`. grantee is a subject or an email; both
// path segments are escaped so an email's '@' stays within one segment (the
// server url.PathUnescape-es it back). Unit-testable against an httptest.Server.
func removeGrantRemote(baseURL, token, slug, grantee string) error {
	endpoint := remoteURL(baseURL, "/artifacts/"+url.PathEscape(slug)+"/grants/"+url.PathEscape(grantee))
	if err := doRemote(http.MethodDelete, endpoint, token, "", nil, http.StatusOK, nil); err != nil {
		return fmt.Errorf("unshare failed: %w", err)
	}
	return nil
}

// setLabelRemote PATCHes <baseURL>/artifacts/<slug>/label to claim a custom
// subdomain label, returning the server's subdomain_url (which is "" when the
// deployment has no RootDomain configured). Unit-testable against an httptest.Server.
func setLabelRemote(baseURL, token, slug, label string) (string, error) {
	endpoint := remoteURL(baseURL, "/artifacts/"+url.PathEscape(slug)+"/label")
	body, err := json.Marshal(map[string]string{"label": label})
	if err != nil {
		return "", err
	}
	var out struct {
		Slug         string `json:"slug"`
		Label        string `json:"label"`
		SubdomainURL string `json:"subdomain_url"`
	}
	if err := doRemote(http.MethodPatch, endpoint, token, "application/json", bytes.NewReader(body), http.StatusOK, &out); err != nil {
		return "", fmt.Errorf("label failed: %w", err)
	}
	return out.SubdomainURL, nil
}

// commentRow is one row of the GET /artifacts/<slug>/comments listing. Its JSON
// tags mirror api.commentView (which is unexported, so it can't be imported):
// id, version, author_email, created_at, body, resolved, parent_id.
type commentRow struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	AuthorEmail string `json:"author_email"`
	CreatedAt   string `json:"created_at"`
	Body        string `json:"body"`
	Resolved    bool   `json:"resolved"`
	ParentID    string `json:"parent_id"`
	Anchor      *struct {
		Quote string `json:"quote"`
	} `json:"anchor"`
}

// addCommentRemote POSTs a comment to <baseURL>/artifacts/<slug>/comments with
// `Authorization: Bearer <token>` and returns the new comment's id. A non-empty
// parentID posts a reply within that thread (ADR-0016). Unit-testable against an
// httptest.Server.
func addCommentRemote(baseURL, token, slug, body, parentID string) (string, error) {
	endpoint := remoteURL(baseURL, "/artifacts/"+url.PathEscape(slug)+"/comments")
	payload := map[string]string{"body": body}
	if parentID != "" {
		payload["parent_id"] = parentID
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var out commentRow
	if err := doRemote(http.MethodPost, endpoint, token, "application/json", bytes.NewReader(b), http.StatusCreated, &out); err != nil {
		return "", fmt.Errorf("comment add failed: %w", err)
	}
	return out.ID, nil
}

// listCommentsRemote GETs an artifact's comments (any authorized viewer). The
// server returns a flat list including replies (parent_id set), anchored, and
// resolved comments. Unit-testable against an httptest.Server.
func listCommentsRemote(baseURL, token, slug string) ([]commentRow, error) {
	var rows []commentRow
	endpoint := remoteURL(baseURL, "/artifacts/"+url.PathEscape(slug)+"/comments")
	if err := doRemote(http.MethodGet, endpoint, token, "", nil, http.StatusOK, &rows); err != nil {
		return nil, fmt.Errorf("comment ls failed: %w", err)
	}
	return rows, nil
}

// resolveCommentRemote POSTs to <baseURL>/artifacts/<slug>/comments/<id>/resolve
// with `Authorization: Bearer <token>` (owner-only, enforced server-side).
// Unit-testable against an httptest.Server.
func resolveCommentRemote(baseURL, token, slug, id string) error {
	endpoint := remoteURL(baseURL, "/artifacts/"+url.PathEscape(slug)+"/comments/"+url.PathEscape(id)+"/resolve")
	if err := doRemote(http.MethodPost, endpoint, token, "", nil, http.StatusOK, nil); err != nil {
		return fmt.Errorf("comment resolve failed: %w", err)
	}
	return nil
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
