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
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/api"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Version is the build version, overridden at release time via
// -ldflags "-X …/internal/cli.Version=<v>". Defaults to "dev" for local builds.
var Version = "dev"

const usage = `artifacta — self-hostable host for AI-generated artifacts

Usage:
  artifacta login [<url>]     log in to a server; <url> auto-configures everything
  artifacta doctor            check server connectivity + authentication
  artifacta whoami            print the authenticated identity
  artifacta upgrade           check for a newer CLI (and server compatibility)
  artifacta publish <file>    publish a new artifact, print its link
  artifacta publish --update <slug> <file>
                            append a new version to an existing artifact
  artifacta versions <slug>   list an artifact's versions
  artifacta share <slug> <email-or-sub>
                            share an artifact (invite by email or subject; sets visibility to invited)
  artifacta unshare <slug> <email-or-sub>
                            revoke a person's access to an artifact
  artifacta visibility <slug> <private|invited|org|link>
                            set an artifact's visibility explicitly
  artifacta label <slug> <label>
                            claim a custom subdomain label ({label}.{root})
  artifacta comment add <slug> <text> [--reply <parent-id>]
                            add a comment (or a threaded reply) to an artifact
  artifacta comment ls <slug>
                            list an artifact's comments
  artifacta comment resolve <slug> <id>
                            resolve a comment thread (owner only)
  artifacta ls                list your artifacts
  artifacta serve             run the viewer server
  artifacta healthcheck       probe a running server's /health (exit 0 = healthy)
  artifacta audit verify      verify the audit-log hash chain
  artifacta version           print the build version

Docs: docs/PLAN.md is legacy; see docs/PRODUCT.md, docs/ARCHITECTURE.md
`

func Run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "login":
		return login(args[1:])
	case "doctor":
		return doctor()
	case "whoami":
		return whoami()
	case "upgrade", "--upgrade":
		return upgrade()
	case "publish":
		return publish(args[1:])
	case "versions":
		return versions(args[1:])
	case "share":
		return share(args[1:])
	case "visibility":
		return visibility(args[1:])
	case "unshare":
		return unshare(args[1:])
	case "label":
		return label(args[1:])
	case "comment":
		return comment(args[1:])
	case "ls":
		return ls()
	case "serve":
		return serve()
	case "healthcheck":
		return healthcheck()
	case "audit":
		return audit(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("artifacta %s\n", Version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q (try: artifacta help)", args[0])
	}
}

// open builds the metadata store + blob store from config. Both backends are
// independently selectable. Metadata (ARTIFACTA_STORE): "postgres" for the
// horizontally-scalable GORM store (ADR-0009), else the zero-dependency file
// store. Blobs (ARTIFACTA_BLOB): "s3" for any S3-compatible backend used strictly
// server-side (ADR-0006), else the filesystem store.
func open(c config.Config) (api.Store, api.Blob, error) {
	var st api.Store
	switch strings.ToLower(strings.TrimSpace(c.StoreBackend)) {
	case "postgres", "postgresql", "pg":
		s, err := infra.NewSQLStore(c.DatabaseURL)
		if err != nil {
			return nil, nil, err
		}
		st = s
	case "", "file", "filestore":
		s, err := infra.NewFileStore(filepath.Join(c.DataDir, "meta"))
		if err != nil {
			return nil, nil, err
		}
		st = s
	default:
		return nil, nil, fmt.Errorf("unknown ARTIFACTA_STORE %q (want \"file\" or \"postgres\")", c.StoreBackend)
	}

	var bl api.Blob
	switch strings.ToLower(strings.TrimSpace(c.BlobBackend)) {
	case "s3":
		s, err := infra.NewBlobS3(context.Background(), infra.S3Config{
			Endpoint:        c.S3Endpoint,
			Region:          c.S3Region,
			Bucket:          c.S3Bucket,
			AccessKeyID:     c.S3AccessKeyID,
			SecretAccessKey: c.S3SecretAccessKey,
			ForcePathStyle:  c.S3ForcePathStyle,
		})
		if err != nil {
			return nil, nil, err
		}
		bl = s
	case "", "file", "filestore", "fs":
		s, err := infra.NewBlobFS(filepath.Join(c.DataDir, "blobs"))
		if err != nil {
			return nil, nil, err
		}
		bl = s
	default:
		return nil, nil, fmt.Errorf("unknown ARTIFACTA_BLOB %q (want \"file\" or \"s3\")", c.BlobBackend)
	}
	return st, bl, nil
}

func login(args []string) error {
	c, err := config.Load()
	if err != nil {
		return err
	}

	// `artifacta login <url>` bootstraps everything from the URL: point at the
	// server, ask its discovery endpoint how to authenticate, and configure the
	// OIDC issuer + client_id from the response so the user supplies nothing else.
	if len(args) > 0 && args[0] != "" {
		c.BaseURL = args[0]
		disc, err := fetchCLIConfig(c.BaseURL)
		if err != nil {
			return err
		}
		switch disc.Auth {
		case "oidc":
			if disc.Issuer == "" || disc.ClientID == "" {
				return fmt.Errorf("server discovery returned no issuer/client_id")
			}
			c.OIDCIssuer = disc.Issuer
			c.OIDCClientID = disc.ClientID
			c.OIDCClientSecret = ""                 // public client: PKCE only, never a secret
			c.OIDCCLIRedirectURI = disc.RedirectURI // fixed loopback URI the IdP registered (if any)
			return loginOIDC(c)
		case "local", "":
			fmt.Printf("server %s uses local dev auth (no OIDC).\n", c.BaseURL)
			// fall through to the local single-token setup below.
		default:
			return fmt.Errorf("server advertised unknown auth mode %q", disc.Auth)
		}
	} else if c.OIDCEnabled() {
		// No URL given but OIDC is already configured: perform the loopback login.
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

	// Determine the loopback redirect URI. When the server advertises a fixed one
	// (c.OIDCCLIRedirectURI), bind that exact host:port so the redirect_uri matches
	// what the operator registered in the IdP (Okta and others match exactly).
	// Otherwise fall back to an ephemeral port (works only where the IdP allows any
	// loopback port).
	bindAddr, callbackPath, redirectURL := "127.0.0.1:0", "/callback", ""
	if c.OIDCCLIRedirectURI != "" {
		hp, path, err := loopbackTarget(c.OIDCCLIRedirectURI)
		if err != nil {
			return err
		}
		bindAddr, callbackPath, redirectURL = hp, path, c.OIDCCLIRedirectURI
	}
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		if c.OIDCCLIRedirectURI != "" {
			return fmt.Errorf("cannot bind %s for the login callback — is the port free? (%w)", bindAddr, err)
		}
		return fmt.Errorf("bind loopback: %w", err)
	}
	if redirectURL == "" { // ephemeral-port fallback: the bound address is the URI
		redirectURL = fmt.Sprintf("http://%s%s", ln.Addr().String(), callbackPath)
	}
	oauthCfg := &oauth2.Config{
		ClientID:     c.OIDCClientID,
		ClientSecret: c.OIDCClientSecret, // empty for the public CLI client (PKCE only)
		RedirectURL:  redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       cliScopes, // includes offline_access → refresh token
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
		tok   *oauth2.Token
		sub   string
		email string
		err   error
	}
	resCh := make(chan loginResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "bad state", http.StatusBadRequest)
			resCh <- loginResult{err: fmt.Errorf("state mismatch on callback")}
			return
		}
		tok, sub, email, err := exchangeCode(r.Context(), oauthCfg, verifier, r.URL.Query().Get("code"), pkce)
		if err != nil {
			http.Error(w, "login failed", http.StatusBadGateway)
			resCh <- loginResult{err: err}
			return
		}
		fmt.Fprintln(w, "artifacta login complete — you can close this tab.")
		resCh <- loginResult{tok: tok, sub: sub, email: email}
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// AccessTypeOffline asks the IdP to return a refresh token (with offline_access
	// in scope) so the CLI can renew without a new browser login.
	authURL := oauthCfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(pkce))
	fmt.Printf("opening browser to log in:\n  %s\n", authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Printf("(could not open a browser automatically — open the URL above manually)\n")
	}

	res := <-resCh
	if res.err != nil {
		return res.err
	}

	raw, _ := res.tok.Extra("id_token").(string)
	c.AccessToken = raw
	c.RefreshToken = res.tok.RefreshToken
	c.AccessTokenExpiry = idTokenExpiry(raw)
	c.Sub = res.sub
	c.Email = res.email
	c.LoggedIn = true
	if err := config.Save(c); err != nil {
		return err
	}
	if c.RefreshToken == "" {
		fmt.Printf("logged in as %s (no refresh token issued — the IdP may not grant offline_access; you may need to re-login when the session expires)\n", c.Email)
	} else {
		fmt.Printf("logged in as %s\n", c.Email)
	}
	// Surface a CLI/deployment version mismatch now (advice only) so the user can
	// install the matching version rather than hitting surprises later.
	noteVersionMismatch(c.BaseURL)
	return nil
}

// exchangeCode swaps an authorization code (with its PKCE verifier) for tokens,
// then verifies the returned id_token against the issuer and extracts sub+email.
// It returns the full oauth2 token so the caller can persist the refresh token
// (offline_access) alongside the id_token. Separated from the loopback plumbing
// so the exchange can be unit-tested without a browser.
func exchangeCode(ctx context.Context, oauthCfg *oauth2.Config, verifier *oidc.IDTokenVerifier, code, pkce string) (tok *oauth2.Token, sub, email string, err error) {
	tok, err = oauthCfg.Exchange(ctx, code, oauth2.VerifierOption(pkce))
	if err != nil {
		return nil, "", "", err
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, "", "", fmt.Errorf("token response carried no id_token")
	}
	idt, err := verifier.Verify(ctx, raw)
	if err != nil {
		return nil, "", "", err
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := idt.Claims(&claims); err != nil {
		return nil, "", "", err
	}
	return tok, idt.Subject, claims.Email, nil
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

	// Update mode: append a new immutable version to an existing artifact. This is
	// inherently a remote operation.
	if updateSlug != "" {
		if !remoteTarget(c) {
			return fmt.Errorf("publish --update requires a remote server — run: artifacta login <url>")
		}
		token, err := freshToken(&c)
		if err != nil {
			return err
		}
		n, link, err := addVersionRemote(c.BaseURL, token, updateSlug, path)
		if err != nil {
			return err
		}
		fmt.Printf("v%d → %s\n", n, link)
		return nil
	}

	// Remote mode: a deployment is targeted (logged in, or a non-localhost URL).
	// Publish over HTTP with a fresh id_token; a missing/expired credential is a
	// hard error — we never silently fall back to writing the local store, which
	// would print a localhost link for what the user believes is a remote publish.
	if remoteTarget(c) {
		token, err := freshToken(&c)
		if err != nil {
			return err
		}
		link, err := publishRemote(c.BaseURL, token, path)
		if err != nil {
			return err
		}
		fmt.Println(link)
		return nil
	}

	// Local dev mode (not logged in, no remote target): write to the local store
	// and label the link so it's unmistakably local.
	if c.Sub == "" {
		return fmt.Errorf("not logged in — run: artifacta login <url>")
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
	fmt.Printf("published locally → %s/a/%s\n", c.BaseURL, art.GetSlug())
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

	endpoint := remoteURL(baseURL, "/artifacts?title="+url.QueryEscape(filepath.Base(path)))
	var out struct {
		Slug string `json:"slug"`
		URL  string `json:"url"`
	}
	if err := doRemote(http.MethodPost, endpoint, token, "text/html; charset=utf-8", f, http.StatusCreated, &out); err != nil {
		return "", fmt.Errorf("publish failed: %w", err)
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

	endpoint := remoteURL(baseURL, "/artifacts/"+url.PathEscape(slug)+"/versions")
	var out struct {
		Slug    string `json:"slug"`
		Version int    `json:"version"`
		URL     string `json:"url"`
	}
	if err := doRemote(http.MethodPost, endpoint, token, "text/html; charset=utf-8", f, http.StatusCreated, &out); err != nil {
		return 0, "", fmt.Errorf("add-version failed: %w", err)
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
	endpoint := remoteURL(baseURL, "/artifacts/"+url.PathEscape(slug))
	if err := doRemote(http.MethodGet, endpoint, token, "", nil, http.StatusOK, &m); err != nil {
		return m, fmt.Errorf("metadata failed: %w", err)
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
	if !remoteTarget(c) {
		return fmt.Errorf("artifacta versions requires a remote server — run: artifacta login <url>")
	}
	token, err := freshToken(&c)
	if err != nil {
		return err
	}
	m, err := fetchMetadata(c.BaseURL, token, slug)
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

// share grants a person access to an artifact (FR12, FR13). A PRIVATE artifact
// ignores grants, so sharing first flips visibility to INVITED and then records
// the grant, driving the two owner-only API endpoints with the stored Bearer
// token. The grantee may be an email (invite-by-email, ADR-0019) or a known
// subject — addGrantRemote auto-detects which. Remote-only: like the other
// sharing commands it requires a deployment so its behavior is identical
// everywhere and never silently mutates a local store.
func share(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: artifacta share <slug> <email-or-sub>")
	}
	slug, grantee := args[0], args[1]
	c, err := config.Load()
	if err != nil {
		return err
	}
	if !remoteTarget(c) {
		return fmt.Errorf("artifacta share requires a remote server — run: artifacta login <url>")
	}
	token, err := freshToken(&c)
	if err != nil {
		return err
	}
	if err := setVisibilityRemote(c.BaseURL, token, slug, "invited"); err != nil {
		return err
	}
	if err := addGrantRemote(c.BaseURL, token, slug, grantee); err != nil {
		return err
	}
	fmt.Printf("shared %s with %s\n", slug, grantee)
	return nil
}

// visibility sets an artifact's visibility explicitly (PATCH …/visibility). The
// level is validated client-side before any network call so a typo never reaches
// the server. Remote-only, like the other sharing commands.
func visibility(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: artifacta visibility <slug> <private|invited|org|link>")
	}
	slug, level := args[0], args[1]
	switch level {
	case "private", "invited", "org", "link":
	default:
		return fmt.Errorf("invalid visibility %q (want private, invited, org, or link)", level)
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	if !remoteTarget(c) {
		return fmt.Errorf("artifacta visibility requires a remote server — run: artifacta login <url>")
	}
	token, err := freshToken(&c)
	if err != nil {
		return err
	}
	if err := setVisibilityRemote(c.BaseURL, token, slug, level); err != nil {
		return err
	}
	fmt.Printf("%s is now %s\n", slug, level)
	return nil
}

// unshare revokes a person's access to an artifact (DELETE …/grants/{grantee}).
// The grantee may be an email or a subject. Remote-only.
func unshare(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: artifacta unshare <slug> <email-or-sub>")
	}
	slug, grantee := args[0], args[1]
	c, err := config.Load()
	if err != nil {
		return err
	}
	if !remoteTarget(c) {
		return fmt.Errorf("artifacta unshare requires a remote server — run: artifacta login <url>")
	}
	token, err := freshToken(&c)
	if err != nil {
		return err
	}
	if err := removeGrantRemote(c.BaseURL, token, slug, grantee); err != nil {
		return err
	}
	fmt.Printf("revoked %s access to %s\n", grantee, slug)
	return nil
}

// label claims a custom subdomain label for an artifact (PATCH …/label), making
// it reachable at {label}.{root-domain}. Remote-only. When the deployment has no
// RootDomain configured the server returns an empty subdomain_url; rather than
// print a blank line we fall back to the canonical path link and say so.
func label(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: artifacta label <slug> <label>")
	}
	slug, lbl := args[0], args[1]
	c, err := config.Load()
	if err != nil {
		return err
	}
	if !remoteTarget(c) {
		return fmt.Errorf("artifacta label requires a remote server — run: artifacta login <url>")
	}
	token, err := freshToken(&c)
	if err != nil {
		return err
	}
	subdomainURL, err := setLabelRemote(c.BaseURL, token, slug, lbl)
	if err != nil {
		return err
	}
	if subdomainURL != "" {
		fmt.Printf("labeled %s → %s\n", slug, subdomainURL)
	} else {
		fmt.Printf("labeled %s as %q → %s/a/%s (subdomain hosting not enabled on this server)\n",
			slug, lbl, strings.TrimSuffix(c.BaseURL, "/"), slug)
	}
	return nil
}

const commentUsage = `usage: artifacta comment <add|ls|resolve> ...
  artifacta comment add <slug> <text> [--reply <parent-id>]
  artifacta comment ls <slug>
  artifacta comment resolve <slug> <id>
`

// comment is the two-level sub-router for review comments (ADR-0014/0016). All
// subcommands are remote-only. `add` posts a page-level comment, or a threaded
// reply with --reply <parent-id>; `ls` lists them (grouping replies under their
// root and marking resolved rows); `resolve` marks a thread resolved (owner-only).
func comment(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", commentUsage)
	}
	switch args[0] {
	case "add":
		return commentAdd(args[1:])
	case "ls", "list":
		return commentLs(args[1:])
	case "resolve":
		return commentResolve(args[1:])
	default:
		return fmt.Errorf("unknown comment subcommand %q\n%s", args[0], commentUsage)
	}
}

func commentAdd(args []string) error {
	// Parse <slug> <text> with an optional --reply <parent-id> anywhere in the args.
	var parentID string
	var positional []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--reply" {
			if i+1 >= len(args) {
				return fmt.Errorf("usage: artifacta comment add <slug> <text> [--reply <parent-id>]")
			}
			parentID = args[i+1]
			i++
			continue
		}
		positional = append(positional, args[i])
	}
	if len(positional) < 2 {
		return fmt.Errorf("usage: artifacta comment add <slug> <text> [--reply <parent-id>]")
	}
	slug, text := positional[0], positional[1]
	c, err := config.Load()
	if err != nil {
		return err
	}
	if !remoteTarget(c) {
		return fmt.Errorf("artifacta comment requires a remote server — run: artifacta login <url>")
	}
	token, err := freshToken(&c)
	if err != nil {
		return err
	}
	id, err := addCommentRemote(c.BaseURL, token, slug, text, parentID)
	if err != nil {
		return err
	}
	fmt.Printf("comment %s added\n", id)
	return nil
}

func commentLs(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: artifacta comment ls <slug>")
	}
	slug := args[0]
	c, err := config.Load()
	if err != nil {
		return err
	}
	if !remoteTarget(c) {
		return fmt.Errorf("artifacta comment requires a remote server — run: artifacta login <url>")
	}
	token, err := freshToken(&c)
	if err != nil {
		return err
	}
	rows, err := listCommentsRemote(c.BaseURL, token, slug)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Printf("no comments on %s\n", slug)
		return nil
	}
	// The server returns a flat list; group replies under their root so a thread
	// reads top-to-bottom. Roots print in server order; each root is followed by
	// its replies (indented). Orphan replies (root not in the list) print at the
	// end so nothing is silently dropped.
	replies := map[string][]commentRow{}
	var roots []commentRow
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.ID] = true
		if r.ParentID == "" {
			roots = append(roots, r)
		} else {
			replies[r.ParentID] = append(replies[r.ParentID], r)
		}
	}
	printRow := func(r commentRow, reply bool) {
		prefix := ""
		if reply {
			prefix = "  ↳ "
		}
		var tags string
		if r.Resolved {
			tags += " [resolved]"
		}
		if r.Anchor != nil && r.Anchor.Quote != "" {
			tags += " [anchored]"
		}
		fmt.Printf("%s%s  v%d  %s%s  %s\n", prefix, r.ID, r.Version, r.AuthorEmail, tags, r.Body)
	}
	for _, root := range roots {
		printRow(root, false)
		for _, rep := range replies[root.ID] {
			printRow(rep, true)
		}
	}
	for pid, reps := range replies {
		if seen[pid] {
			continue // already printed under its root above
		}
		for _, rep := range reps {
			printRow(rep, true)
		}
	}
	return nil
}

func commentResolve(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: artifacta comment resolve <slug> <id>")
	}
	slug, id := args[0], args[1]
	c, err := config.Load()
	if err != nil {
		return err
	}
	if !remoteTarget(c) {
		return fmt.Errorf("artifacta comment requires a remote server — run: artifacta login <url>")
	}
	token, err := freshToken(&c)
	if err != nil {
		return err
	}
	if err := resolveCommentRemote(c.BaseURL, token, slug, id); err != nil {
		return err
	}
	fmt.Printf("resolved comment %s\n", id)
	return nil
}

// setVisibilityRemote PATCHes <baseURL>/artifacts/<slug>/visibility with the
// requested visibility and `Authorization: Bearer <token>`. Unit-testable
// against an httptest.Server.
func setVisibilityRemote(baseURL, token, slug, visibility string) error {
	endpoint := remoteURL(baseURL, "/artifacts/"+url.PathEscape(slug)+"/visibility")
	body, err := json.Marshal(map[string]string{"visibility": visibility})
	if err != nil {
		return err
	}
	if err := doRemote(http.MethodPatch, endpoint, token, "application/json", bytes.NewReader(body), http.StatusOK, nil); err != nil {
		return fmt.Errorf("set-visibility failed: %w", err)
	}
	return nil
}

// addGrantRemote POSTs a grant to <baseURL>/artifacts/<slug>/grants with
// `Authorization: Bearer <token>`. The grantee is auto-detected: an address that
// looks like an email is sent as {"email":...} (invite-by-email, ADR-0019),
// otherwise as {"grantee_sub":...} for a known subject. The server re-validates
// either way. Unit-testable against an httptest.Server.
func addGrantRemote(baseURL, token, slug, grantee string) error {
	endpoint := remoteURL(baseURL, "/artifacts/"+url.PathEscape(slug)+"/grants")
	field := "grantee_sub"
	if looksLikeEmail(grantee) {
		field = "email"
	}
	body, err := json.Marshal(map[string]string{field: grantee})
	if err != nil {
		return err
	}
	if err := doRemote(http.MethodPost, endpoint, token, "application/json", bytes.NewReader(body), http.StatusCreated, nil); err != nil {
		return fmt.Errorf("add-grant failed: %w", err)
	}
	return nil
}

func ls() error {
	c, err := config.Load()
	if err != nil {
		return err
	}

	// Remote mode: list the deployment's artifacts (the caller's own), not the
	// local store — the previous always-local behavior silently showed the wrong
	// data when the CLI was pointed at a server.
	if remoteTarget(c) {
		token, err := freshToken(&c)
		if err != nil {
			return err
		}
		rows, err := listRemote(c.BaseURL, token)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("no artifacts yet — artifacta publish <file>")
			return nil
		}
		for _, r := range rows {
			fmt.Printf("%-20s  %s  %s\n", r.Visibility, r.URL, r.Title)
		}
		return nil
	}

	// Local dev mode: the on-disk store.
	st, _, err := open(c)
	if err != nil {
		return err
	}
	arts, err := st.ListByOwner(c.Sub)
	if err != nil {
		return err
	}
	if len(arts) == 0 {
		fmt.Println("no local artifacts — artifacta publish <file> (or: artifacta login <url>)")
		return nil
	}
	for _, a := range arts {
		fmt.Printf("(local) %s  %-20s  %s/a/%s  %s\n",
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
	// The audit chain lives in the server's own store; the CLI can only verify a
	// local store. Pointed at a deployment, say so instead of verifying an empty
	// or unrelated local log.
	if remoteTarget(c) {
		return fmt.Errorf("audit verify runs against a local store; run it on the server host (this CLI is logged in to %s)", c.BaseURL)
	}
	st, _, err := open(c)
	if err != nil {
		return err
	}
	events, err := st.AuditEvents()
	if err != nil {
		return err
	}
	n, err := infra.VerifyChain(events)
	if err != nil {
		return err
	}
	fmt.Printf("audit chain OK (%d events)\n", n)
	return nil
}

// doctor checks that the CLI can reach the configured server and that its stored
// credentials authenticate — the "is everything wired up?" health command. It
// walks connectivity (GET /health) → server auth mode (discovery) → the CLI's own
// authentication (fresh token + GET /me), printing a ✓/✗ line per step.
func doctor() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	if c.BaseURL == "" {
		return fmt.Errorf("no server configured — run: artifacta login <url>")
	}
	fmt.Printf("server: %s\n", c.BaseURL)

	if err := doRemote(http.MethodGet, remoteURL(c.BaseURL, "/health"), "", "", nil, http.StatusOK, nil); err != nil {
		fmt.Printf("  ✗ connect: %v\n", err)
		return fmt.Errorf("cannot reach server")
	}
	fmt.Println("  ✓ connect")

	disc, err := fetchCLIConfig(c.BaseURL)
	if err != nil {
		fmt.Printf("  ✗ discovery: %v\n", err)
		return err
	}
	fmt.Printf("  ✓ discovery: auth=%s\n", disc.Auth)

	// Auth check only applies to OIDC deployments — a local dev-auth server has no
	// CLI token to verify, so skip it there instead of reporting a spurious ✗.
	if disc.Auth == "local" {
		fmt.Println("  • auth: local dev mode — no CLI token to check")
	} else {
		token, err := freshToken(&c)
		if err != nil {
			fmt.Printf("  ✗ auth: %v\n", err)
			return err
		}
		sub, email, err := meRemote(c.BaseURL, token)
		if err != nil {
			fmt.Printf("  ✗ auth: %v\n", err)
			return err
		}
		fmt.Printf("  ✓ auth: %s <%s>\n", sub, email)
	}

	// Version compatibility: matched is a ✓; a mismatch prints the exact command
	// to install the deployment's version (advice only — doctor never changes it).
	if sv, _, _, verr := serverVersion(c.BaseURL); verr == nil {
		if Version != "dev" && cmpVersion(Version, sv) == 0 {
			fmt.Printf("  ✓ version: cli %s matches deployment\n", Version)
		} else {
			fmt.Printf("  ! version: cli %s vs deployment %s\n", Version, sv)
			fmt.Println(decideMatch(Version, sv).message)
		}
	}
	fmt.Println("all checks passed")
	return nil
}

// whoami prints the authenticated identity as the server sees it.
func whoami() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	if !remoteTarget(c) {
		return fmt.Errorf("not logged in to a server — run: artifacta login <url>")
	}
	token, err := freshToken(&c)
	if err != nil {
		return err
	}
	sub, email, err := meRemote(c.BaseURL, token)
	if err != nil {
		return err
	}
	fmt.Printf("%s <%s>  @ %s\n", sub, email, c.BaseURL)
	return nil
}

// Server timeouts (see ADR-0022). Pragmatic posture for a streaming file server:
// ReadHeaderTimeout kills header-Slowloris; ReadTimeout fits a 25 MiB upload on a
// slow-but-real link; WriteTimeout is 0 so a large artifact download to a slow VPN
// client is never cut mid-stream; IdleTimeout reaps idle keep-alives.
const (
	serveReadHeaderTimeout = 10 * time.Second
	serveReadTimeout       = 120 * time.Second
	serveWriteTimeout      = 0
	serveIdleTimeout       = 120 * time.Second
	serveShutdownGrace     = 20 * time.Second
)

func serve() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	st, bl, err := open(c)
	if err != nil {
		return err
	}
	logger := api.NewLogger(c.LogLevel)
	metrics := api.NewMetrics()
	srv := &api.Server{Store: st, Blob: bl, BaseURL: c.BaseURL, RootDomain: c.RootDomain, OrgName: c.OrgName, LogoURL: c.LogoURL, Version: Version, Metrics: metrics}
	// Config-driven auth selection: OIDC browser SSO when configured (ADR-0007),
	// otherwise the Local single-token adapter for zero-dependency/dev deploys.
	if c.OIDCEnabled() {
		if c.SessionSecret == "" {
			return fmt.Errorf("OIDC configured but ARTIFACTA_SESSION_SECRET is empty — set it to key session cookies")
		}
		secure := strings.HasPrefix(strings.ToLower(c.BaseURL), "https://")
		p, err := api.NewOIDCProvider(context.Background(),
			c.OIDCIssuer, c.OIDCClientID, c.OIDCClientSecret, c.OIDCRedirectURL, c.OIDCCLIRedirectURI, c.SessionSecret, secure)
		if err != nil {
			return fmt.Errorf("oidc setup: %w", err)
		}
		srv.Auth = p
		srv.OIDC = p
		fmt.Printf("auth: oidc browser sso (issuer %s)\n", c.OIDCIssuer)
	} else {
		// Fail loud rather than boot a server that silently rejects every request:
		// with an empty token the Local adapter can never match a caller. Mirrors
		// the OIDC SessionSecret guard above.
		if c.Token == "" {
			return fmt.Errorf("local auth selected but no token is set — set ARTIFACTA_TOKEN (or configure OIDC) so the server can authenticate a caller")
		}
		srv.Auth = &api.Local{Token: c.Token, ID: c.Identity()}
		fmt.Printf("auth: local single-token adapter\n")
	}
	fmt.Printf("artifacta %s serving on %s  (base URL %s)\n", Version, c.Addr, c.BaseURL)

	httpSrv := &http.Server{
		Addr:              c.Addr,
		Handler:           api.Observe(srv.Routes(), logger, metrics),
		ReadHeaderTimeout: serveReadHeaderTimeout,
		ReadTimeout:       serveReadTimeout,
		WriteTimeout:      serveWriteTimeout,
		IdleTimeout:       serveIdleTimeout,
	}

	// Run the listener in the background and drain gracefully on SIGINT/SIGTERM so
	// a rollout never cuts an in-flight publish or download mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		stop() // restore default signal handling so a second Ctrl-C force-quits
		logger.Info("shutting down", "grace", serveShutdownGrace.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serveShutdownGrace)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// healthcheck is the container-native liveness probe: it GETs /health on the
// configured (or ARTIFACTA_ADDR) address and exits 0 on 200, non-zero otherwise.
// The prod image is distroless (no shell/curl), so the compose/orchestrator
// healthcheck invokes this subcommand instead.
func healthcheck() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	addr := c.Addr
	// Turn a bare listen address (":8080") into a loopback URL to dial.
	host, port, splitErr := net.SplitHostPort(strings.TrimSpace(addr))
	if splitErr != nil {
		return fmt.Errorf("invalid address %q: %w", addr, splitErr)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/health", net.JoinHostPort(host, port)))
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
	}
	return nil
}
