// Package cli implements the artifacta command-line interface. The CLI is both a
// local tool (talks to the local store directly for zero-dependency dev use)
// and an API client: when OIDC is configured it logs in via a loopback
// Authorization-Code + PKCE flow and publishes to a remote server over HTTP
// using its OIDC id_token as a Bearer token (ADR-0007). `serve` runs the viewer.
package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/api"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const usage = `artifacta — self-hostable host for AI-generated artifacts

Usage:
  artifacta login             set up your local identity + session token
  artifacta publish <file>    publish a new artifact, print its link
  artifacta publish --update <slug> <file>
                            append a new version to an existing artifact
  artifacta versions <slug>   list an artifact's versions
  artifacta share <slug> <grantee-sub>
                            share an artifact with a subject (sets visibility to invited)
  artifacta ls                list your artifacts
  artifacta serve             run the viewer server
  artifacta audit verify      verify the audit-log hash chain

Docs: docs/PLAN.md is legacy; see docs/PRODUCT.md, docs/ARCHITECTURE.md
`

func Run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "login":
		return login()
	case "publish":
		return publish(args[1:])
	case "versions":
		return versions(args[1:])
	case "share":
		return share(args[1:])
	case "ls":
		return ls()
	case "serve":
		return serve()
	case "audit":
		return audit(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q (try: artifacta help)", args[0])
	}
}

func open(c config.Config) (*infra.FileStore, *infra.BlobFS, error) {
	st, err := infra.NewFileStore(filepath.Join(c.DataDir, "meta"))
	if err != nil {
		return nil, nil, err
	}
	bl, err := infra.NewBlobFS(filepath.Join(c.DataDir, "blobs"))
	if err != nil {
		return nil, nil, err
	}
	return st, bl, nil
}

func login() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	// OIDC configured: perform a real loopback browser login and store the
	// id_token as the CLI's Bearer credential.
	if c.OIDCEnabled() {
		return loginOIDC(c)
	}

	// Local single-token fallback (zero-dependency dev mode).
	if c.Sub == "" {
		u := os.Getenv("USER")
		if u == "" {
			u = "local"
		}
		c.Sub = "local:" + u
		c.Email = u + "@localhost"
	}
	if c.Token == "" {
		c.Token = domain.NewSlug() + domain.NewSlug()
	}
	if err := config.Save(c); err != nil {
		return err
	}
	fmt.Printf("logged in as %s\n", c.Email)
	fmt.Printf("data dir:    %s\n", c.DataDir)
	fmt.Printf("browser login (sets session cookie once):\n  %s/login?token=%s\n", c.BaseURL, c.Token)
	return nil
}

// loginOIDC runs the interactive loopback Authorization-Code + PKCE login: it
// discovers the issuer, starts a throwaway HTTP server on 127.0.0.1:<random>,
// opens the browser to the IdP authorize URL (redirect_uri = the loopback URL),
// receives the code on the loopback handler, exchanges it with the PKCE verifier,
// verifies the id_token, stores it via config.Save(), and shuts the server down.
func loginOIDC(c config.Config) error {
	ctx := context.Background()
	provider, err := oidc.NewProvider(ctx, c.OIDCIssuer)
	if err != nil {
		return fmt.Errorf("oidc discovery: %w", err)
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: c.OIDCClientID})

	// Bind an ephemeral loopback port; the bound address is the redirect URI.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind loopback: %w", err)
	}
	oauthCfg := &oauth2.Config{
		ClientID:     c.OIDCClientID,
		ClientSecret: c.OIDCClientSecret,
		RedirectURL:  fmt.Sprintf("http://%s/callback", ln.Addr().String()),
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}

	state, err := randState()
	if err != nil {
		ln.Close()
		return err
	}
	pkce := oauth2.GenerateVerifier()

	// loginResult carries the outcome from the loopback handler back to the
	// waiting command.
	type loginResult struct {
		idToken string
		sub     string
		email   string
		err     error
	}
	resCh := make(chan loginResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "bad state", http.StatusBadRequest)
			resCh <- loginResult{err: fmt.Errorf("state mismatch on callback")}
			return
		}
		raw, sub, email, err := exchangeCode(r.Context(), oauthCfg, verifier, r.URL.Query().Get("code"), pkce)
		if err != nil {
			http.Error(w, "login failed", http.StatusBadGateway)
			resCh <- loginResult{err: err}
			return
		}
		fmt.Fprintln(w, "artifacta login complete — you can close this tab.")
		resCh <- loginResult{idToken: raw, sub: sub, email: email}
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	authURL := oauthCfg.AuthCodeURL(state, oauth2.AccessTypeOnline, oauth2.S256ChallengeOption(pkce))
	fmt.Printf("opening browser to log in:\n  %s\n", authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Printf("(could not open a browser automatically — open the URL above manually)\n")
	}

	res := <-resCh
	if res.err != nil {
		return res.err
	}

	c.AccessToken = res.idToken
	c.Sub = res.sub
	c.Email = res.email
	if err := config.Save(c); err != nil {
		return err
	}
	fmt.Printf("logged in as %s\n", c.Email)
	return nil
}

// exchangeCode swaps an authorization code (with its PKCE verifier) for tokens,
// then verifies the returned id_token against the issuer and extracts sub+email.
// It is separated from the loopback plumbing so the exchange can be unit-tested
// without a browser.
func exchangeCode(ctx context.Context, oauthCfg *oauth2.Config, verifier *oidc.IDTokenVerifier, code, pkce string) (idToken, sub, email string, err error) {
	tok, err := oauthCfg.Exchange(ctx, code, oauth2.VerifierOption(pkce))
	if err != nil {
		return "", "", "", err
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return "", "", "", fmt.Errorf("token response carried no id_token")
	}
	idt, err := verifier.Verify(ctx, raw)
	if err != nil {
		return "", "", "", err
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := idt.Claims(&claims); err != nil {
		return "", "", "", err
	}
	return raw, idt.Subject, claims.Email, nil
}

// randState returns a 256-bit base64url random string for the OAuth state nonce.
func randState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// openBrowser best-effort opens url in the platform's default browser.
func openBrowser(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{target}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default: // linux, bsd, etc.
		name, args = "xdg-open", []string{target}
	}
	return exec.Command(name, args...).Start()
}

func publish(args []string) error {
	// Parse an optional `--update <slug>` flag; it may appear before or after the
	// file path. Everything else is treated as the positional <file> argument.
	var updateSlug, path string
	for i := 0; i < len(args); i++ {
		if args[i] == "--update" {
			if i+1 >= len(args) {
				return fmt.Errorf("usage: artifacta publish --update <slug> <file>")
			}
			updateSlug = args[i+1]
			i++
			continue
		}
		if path == "" {
			path = args[i]
		}
	}
	if path == "" {
		return fmt.Errorf("usage: artifacta publish [--update <slug>] <file>")
	}
	c, err := config.Load()
	if err != nil {
		return err
	}

	// Update mode: append a new immutable version to an existing artifact.
	if updateSlug != "" {
		if c.BaseURL == "" || c.AccessToken == "" {
			return fmt.Errorf("publish --update requires a remote server + login")
		}
		n, link, err := addVersionRemote(c.BaseURL, c.AccessToken, updateSlug, path)
		if err != nil {
			return err
		}
		fmt.Printf("v%d → %s\n", n, link)
		return nil
	}

	// Remote API mode: a server is targeted and we hold an access token — POST
	// the file over HTTP with the id_token as a Bearer credential.
	if c.BaseURL != "" && c.AccessToken != "" {
		link, err := publishRemote(c.BaseURL, c.AccessToken, path)
		if err != nil {
			return err
		}
		fmt.Println(link)
		return nil
	}

	// Backward-compatible dev mode: write directly to the local store.
	if c.Sub == "" {
		return fmt.Errorf("not logged in — run: artifacta login")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, bl, err := open(c)
	if err != nil {
		return err
	}
	now := timestamppb.Now()
	const firstVersion = 1
	art := &artifactav1.Artifact{
		Slug:          domain.NewSlug(),
		OwnerSub:      c.Sub,
		Title:         filepath.Base(path),
		Visibility:    artifactav1.Visibility_VISIBILITY_PRIVATE, // private by default
		ContentType:   "text/html; charset=utf-8",
		CreatedAt:     now,
		LatestVersion: firstVersion,
	}
	if err := bl.Put(art.GetSlug(), firstVersion, f); err != nil {
		return err
	}
	if err := st.AddVersion(&artifactav1.ArtifactVersion{
		Slug:        art.GetSlug(),
		N:           firstVersion,
		ContentType: art.GetContentType(),
		CreatedAt:   now,
		CreatedBy:   c.Sub,
	}); err != nil {
		return err
	}
	if err := st.PutArtifact(art); err != nil {
		return err
	}
	_ = st.Append(&artifactav1.AuditEvent{
		Ts: timestamppb.Now(), PrincipalSub: c.Sub, Slug: art.GetSlug(),
		Action: artifactav1.AuditAction_AUDIT_ACTION_PUBLISH, Allowed: true,
	})
	fmt.Printf("%s/a/%s\n", c.BaseURL, art.GetSlug())
	return nil
}

// publishRemote POSTs the file at path to <baseURL>/artifacts?title=<basename>
// with `Authorization: Bearer <token>` and returns the `url` from the JSON
// response. It is unit-testable against an httptest.Server.
func publishRemote(baseURL, token, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	endpoint := strings.TrimSuffix(baseURL, "/") + "/artifacts?title=" + url.QueryEscape(filepath.Base(path))
	req, err := http.NewRequest(http.MethodPost, endpoint, f)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/html; charset=utf-8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("publish failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out struct {
		Slug string `json:"slug"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode publish response: %w", err)
	}
	return out.URL, nil
}

// addVersionRemote POSTs the file at path to <baseURL>/artifacts/<slug>/versions
// with `Authorization: Bearer <token>` and returns the new version number and
// URL from the JSON response {slug, version, url}. Mirrors publishRemote and is
// unit-testable against an httptest.Server.
func addVersionRemote(baseURL, token, slug, path string) (int, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()

	endpoint := strings.TrimSuffix(baseURL, "/") + "/artifacts/" + url.PathEscape(slug) + "/versions"
	req, err := http.NewRequest(http.MethodPost, endpoint, f)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/html; charset=utf-8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, "", fmt.Errorf("add-version failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out struct {
		Slug    string `json:"slug"`
		Version int    `json:"version"`
		URL     string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, "", fmt.Errorf("decode add-version response: %w", err)
	}
	return out.Version, out.URL, nil
}

// metadata mirrors the authorized metadata projection served by
// GET /artifacts/<slug>: enough for the CLI to list versions.
type metadata struct {
	Slug          string `json:"slug"`
	Title         string `json:"title"`
	Visibility    string `json:"visibility"`
	LatestVersion int    `json:"latest_version"`
	IsOwner       bool   `json:"is_owner"`
	Versions      []struct {
		N         int    `json:"n"`
		CreatedAt string `json:"created_at"`
		CreatedBy string `json:"created_by"`
		Note      string `json:"note"`
	} `json:"versions"`
}

// fetchMetadata GETs <baseURL>/artifacts/<slug> with `Authorization: Bearer
// <token>` and decodes the authorized metadata projection. Unit-testable
// against an httptest.Server.
func fetchMetadata(baseURL, token, slug string) (metadata, error) {
	var m metadata
	endpoint := strings.TrimSuffix(baseURL, "/") + "/artifacts/" + url.PathEscape(slug)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return m, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return m, fmt.Errorf("metadata failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return m, fmt.Errorf("decode metadata response: %w", err)
	}
	return m, nil
}

// versions lists an artifact's versions (newest first as returned by the
// server), printing `v<n>  <created_at>  <note>` per line.
func versions(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: artifacta versions <slug>")
	}
	slug := args[0]
	c, err := config.Load()
	if err != nil {
		return err
	}
	if c.BaseURL == "" || c.AccessToken == "" {
		return fmt.Errorf("artifacta versions requires a remote server + login")
	}
	m, err := fetchMetadata(c.BaseURL, c.AccessToken, slug)
	if err != nil {
		return err
	}
	if len(m.Versions) == 0 {
		fmt.Printf("no versions for %s\n", slug)
		return nil
	}
	for _, v := range m.Versions {
		fmt.Printf("v%d  %s  %s\n", v.N, v.CreatedAt, v.Note)
	}
	return nil
}

// share grants a subject access to an artifact (FR12, FR13). A PRIVATE artifact
// ignores grants, so sharing first flips visibility to INVITED and then records
// the grant. Remote mode drives the two owner-only API endpoints with the stored
// Bearer token; local mode does the equivalent directly against the local store.
func share(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: artifacta share <slug> <grantee-sub>")
	}
	slug, grantee := args[0], args[1]
	c, err := config.Load()
	if err != nil {
		return err
	}

	// Remote API mode: PATCH visibility→invited, then POST the grant.
	if c.BaseURL != "" && c.AccessToken != "" {
		if err := setVisibilityRemote(c.BaseURL, c.AccessToken, slug, "invited"); err != nil {
			return err
		}
		if err := addGrantRemote(c.BaseURL, c.AccessToken, slug, grantee); err != nil {
			return err
		}
		fmt.Printf("shared %s with %s\n", slug, grantee)
		return nil
	}

	// Local dev mode: mutate the local store directly.
	if c.Sub == "" {
		return fmt.Errorf("not logged in — run: artifacta login")
	}
	st, _, err := open(c)
	if err != nil {
		return err
	}
	art, ok, err := st.GetArtifact(slug)
	if err != nil {
		return err
	}
	if !ok || art.GetOwnerSub() != c.Sub {
		return fmt.Errorf("no such artifact: %s", slug)
	}
	art.Visibility = artifactav1.Visibility_VISIBILITY_INVITED
	if err := st.PutArtifact(art); err != nil {
		return err
	}
	if err := st.AddGrant(&artifactav1.Grant{
		Slug: slug, GranteeSub: grantee, GrantedBy: c.Sub, CreatedAt: timestamppb.Now(),
	}); err != nil {
		return err
	}
	_ = st.Append(&artifactav1.AuditEvent{
		Ts: timestamppb.Now(), PrincipalSub: c.Sub, Slug: slug,
		Action: artifactav1.AuditAction_AUDIT_ACTION_SHARE, Allowed: true,
	})
	fmt.Printf("shared %s with %s\n", slug, grantee)
	return nil
}

// setVisibilityRemote PATCHes <baseURL>/artifacts/<slug>/visibility with the
// requested visibility and `Authorization: Bearer <token>`. Unit-testable
// against an httptest.Server.
func setVisibilityRemote(baseURL, token, slug, visibility string) error {
	endpoint := strings.TrimSuffix(baseURL, "/") + "/artifacts/" + url.PathEscape(slug) + "/visibility"
	body, err := json.Marshal(map[string]string{"visibility": visibility})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPatch, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("set-visibility failed: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// addGrantRemote POSTs a grant to <baseURL>/artifacts/<slug>/grants with
// `Authorization: Bearer <token>`. Unit-testable against an httptest.Server.
func addGrantRemote(baseURL, token, slug, grantee string) error {
	endpoint := strings.TrimSuffix(baseURL, "/") + "/artifacts/" + url.PathEscape(slug) + "/grants"
	body, err := json.Marshal(map[string]string{"grantee_sub": grantee})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("add-grant failed: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func ls() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	st, _, err := open(c)
	if err != nil {
		return err
	}
	arts, err := st.ListByOwner(c.Sub)
	if err != nil {
		return err
	}
	if len(arts) == 0 {
		fmt.Println("no artifacts yet — artifacta publish <file>")
		return nil
	}
	for _, a := range arts {
		fmt.Printf("%s  %-20s  %s/a/%s  %s\n",
			a.GetCreatedAt().AsTime().Format("2006-01-02 15:04"),
			a.GetVisibility().String(), c.BaseURL, a.GetSlug(), a.GetTitle())
	}
	return nil
}

func audit(args []string) error {
	if len(args) < 1 || args[0] != "verify" {
		return fmt.Errorf("usage: artifacta audit verify")
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	n, err := infra.VerifyAuditLog(filepath.Join(c.DataDir, "meta"))
	if err != nil {
		return err
	}
	fmt.Printf("audit chain OK (%d events)\n", n)
	return nil
}

func serve() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	st, bl, err := open(c)
	if err != nil {
		return err
	}
	srv := &api.Server{Store: st, Blob: bl, BaseURL: c.BaseURL, RootDomain: c.RootDomain}
	// Config-driven auth selection: OIDC browser SSO when configured (ADR-0007),
	// otherwise the Local single-token adapter for zero-dependency/dev deploys.
	if c.OIDCEnabled() {
		if c.SessionSecret == "" {
			return fmt.Errorf("OIDC configured but ARTIFACTA_SESSION_SECRET is empty — set it to key session cookies")
		}
		secure := strings.HasPrefix(strings.ToLower(c.BaseURL), "https://")
		p, err := api.NewOIDCProvider(context.Background(),
			c.OIDCIssuer, c.OIDCClientID, c.OIDCClientSecret, c.OIDCRedirectURL, c.SessionSecret, secure)
		if err != nil {
			return fmt.Errorf("oidc setup: %w", err)
		}
		srv.Auth = p
		srv.OIDC = p
		fmt.Printf("auth: oidc browser sso (issuer %s)\n", c.OIDCIssuer)
	} else {
		srv.Auth = &api.Local{Token: c.Token, ID: c.Identity()}
		fmt.Printf("auth: local single-token adapter\n")
	}
	fmt.Printf("artifacta serving on %s  (base URL %s)\n", c.Addr, c.BaseURL)
	return http.ListenAndServe(c.Addr, srv.Routes())
}
