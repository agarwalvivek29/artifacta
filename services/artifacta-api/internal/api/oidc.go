package api

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"unicode"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// randToken returns a 256-bit cryptographically random base64url string, used
// for the OAuth `state` nonce.
func randToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b64.EncodeToString(buf), nil
}

// OIDCProvider is an Auth implementation backed by an OpenID Connect IdP. It
// implements the browser SSO half of ADR-0007 (FR6): Authorization-Code + PKCE.
//
// Flow: Login redirects the browser to the IdP with a `state` nonce and a PKCE
// challenge; Callback validates the returned state, exchanges the code (proving
// possession of the PKCE verifier), verifies the id_token against the issuer
// JWKS, and mints a stateless signed session cookie. Identify then authenticates
// subsequent requests from that cookie alone — no server-side session store.
type OIDCProvider struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
	sessions *Sessions
	// issuer is the OIDC issuer URL, retained so the CLI discovery endpoint can
	// hand it back to `artifacta login <url>` (the CLI re-discovers from it).
	issuer string
	// secure marks auth cookies Secure so browsers only send them over HTTPS.
	secure bool
}

// cookie names for the short-lived, single-use flow nonces and the session.
const (
	sessionCookie  = "hn_session"
	stateCookie    = "hn_oidc_state"
	verifierCookie = "hn_oidc_verifier"
)

// cliScopes are the scopes the CLI requests. offline_access asks the IdP for a
// refresh token so the CLI can mint fresh id_tokens without a new browser login
// (the browser Login mints its own session cookie and does not request it).
var cliScopes = []string{oidc.ScopeOpenID, "email", "profile", "offline_access"}

// CLIAuthConfig is the body of GET /.well-known/artifacta-cli: everything
// `artifacta login <url>` needs to bootstrap the loopback PKCE flow with no
// hand-configuration. client_id is public (safe to expose); no secret is ever
// sent — the CLI is a public client authenticating with PKCE alone.
type CLIAuthConfig struct {
	Auth     string   `json:"auth"` // "oidc" or "local"
	Issuer   string   `json:"issuer,omitempty"`
	ClientID string   `json:"client_id,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
}

// CLILoginConfig returns the OIDC discovery info the CLI needs to log in.
func (p *OIDCProvider) CLILoginConfig() CLIAuthConfig {
	return CLIAuthConfig{
		Auth:     "oidc",
		Issuer:   p.issuer,
		ClientID: p.oauth.ClientID,
		Scopes:   cliScopes,
	}
}

// NewOIDCProvider discovers the issuer's endpoints/JWKS and builds a provider.
// clientSecret and redirectURL come from config/env; sessionSecret keys the
// signed session + flow cookies. secure controls the Secure cookie attribute
// (true in real HTTPS deploys).
func NewOIDCProvider(ctx context.Context, issuer, clientID, clientSecret, redirectURL, sessionSecret string, secure bool) (*OIDCProvider, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &OIDCProvider{
		oauth: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
		sessions: NewSessions(sessionSecret),
		issuer:   issuer,
		secure:   secure,
	}, nil
}

// Identify authenticates a request. It prefers the browser hn_session cookie;
// failing that it accepts a CLI-supplied OIDC id_token as `Authorization: Bearer
// <id_token>`, verified against the SAME issuer JWKS/audience the browser flow
// uses (a self-contained single-app system per ADR-0007). It fails closed: a
// missing, malformed, tampered, or expired credential yields (nil, false).
func (p *OIDCProvider) Identify(r *http.Request) (*artifactav1.Identity, bool) {
	// Cookie path first (browser SSO).
	if c, err := r.Cookie(sessionCookie); err == nil {
		if sub, email, ok := p.sessions.ParseSession(c.Value); ok && sub != "" {
			return &artifactav1.Identity{Sub: sub, Email: email}, true
		}
	}
	// Bearer path (CLI API client): verify the id_token against the issuer.
	if tok := bearer(r); tok != "" {
		if id, ok := p.identifyBearer(r.Context(), tok); ok {
			return id, true
		}
	}
	return nil, false
}

// identifyBearer verifies a raw OIDC id_token (signature via JWKS, plus
// iss/aud/exp) and returns the Identity carried by its sub/email claims. It
// fails closed on any verification or claim-extraction error.
func (p *OIDCProvider) identifyBearer(ctx context.Context, raw string) (*artifactav1.Identity, bool) {
	idToken, err := p.verifier.Verify(ctx, raw)
	if err != nil || idToken.Subject == "" {
		return nil, false
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, false
	}
	return &artifactav1.Identity{Sub: idToken.Subject, Email: claims.Email}, true
}

// Login begins the Authorization-Code + PKCE flow: it generates a state nonce
// and a PKCE verifier, stashes both in short-lived signed HttpOnly cookies, and
// redirects the browser to the IdP authorize endpoint carrying the state and
// the S256 code challenge.
func (p *OIDCProvider) Login(w http.ResponseWriter, r *http.Request) {
	state, err := randToken()
	if err != nil {
		http.Error(w, "login unavailable", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()

	p.setFlowCookie(w, stateCookie, p.sessions.signValue(state))
	p.setFlowCookie(w, verifierCookie, p.sessions.signValue(verifier))

	url := p.oauth.AuthCodeURL(state, oauth2.AccessTypeOnline, oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, url, http.StatusFound)
}

// Callback completes the flow. It validates the returned state against the
// signed state cookie, exchanges the code together with the PKCE verifier,
// verifies the id_token against the issuer JWKS and its claims (iss/aud/exp),
// extracts sub+email, mints the session cookie, and redirects to "/".
func (p *OIDCProvider) Callback(w http.ResponseWriter, r *http.Request) {
	// Validate state: the query value must match the value we signed into the
	// state cookie. This defends against CSRF on the callback.
	sc, err := r.Cookie(stateCookie)
	if err != nil {
		http.Error(w, "bad state", http.StatusBadRequest)
		return
	}
	wantState, ok := p.sessions.openValue(sc.Value)
	if !ok || r.URL.Query().Get("state") != wantState {
		http.Error(w, "bad state", http.StatusBadRequest)
		return
	}

	// An IdP that refuses the request (user denied consent, invalid scope, IdP
	// outage, …) redirects back with ?error=<code>, usually ?error_description=
	// <text>, and NO authorization code (RFC 6749 §4.1.2.1). Surface those —
	// otherwise the flow falls through to exchanging an absent code and reports a
	// misleading "token exchange failed". State is validated above first, so this
	// only trusts an error returned on our own CSRF-checked flow.
	if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
		http.Error(w, "login failed: "+oauthErrorMessage(oauthErr, r.URL.Query().Get("error_description")), http.StatusUnauthorized)
		return
	}

	vc, err := r.Cookie(verifierCookie)
	if err != nil {
		http.Error(w, "bad verifier", http.StatusBadRequest)
		return
	}
	verifier, ok := p.sessions.openValue(vc.Value)
	if !ok {
		http.Error(w, "bad verifier", http.StatusBadRequest)
		return
	}

	// Exchange the code; the PKCE verifier proves this client started the flow.
	tok, err := p.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		http.Error(w, "login failed: "+exchangeErrorMessage(err), http.StatusBadGateway)
		return
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		http.Error(w, "no id_token", http.StatusBadGateway)
		return
	}

	// Verify signature (JWKS), issuer, audience, and expiry.
	idToken, err := p.verifier.Verify(r.Context(), rawID)
	if err != nil {
		http.Error(w, "invalid id_token", http.StatusUnauthorized)
		return
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "invalid claims", http.StatusUnauthorized)
		return
	}

	// Success: mint the session cookie and clear the single-use flow cookies.
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: p.sessions.NewSession(idToken.Subject, claims.Email),
		Path: "/", HttpOnly: true, Secure: p.secure, SameSite: http.SameSiteLaxMode,
	})
	p.clearFlowCookie(w, stateCookie)
	p.clearFlowCookie(w, verifierCookie)
	http.Redirect(w, r, "/", http.StatusFound)
}

// setFlowCookie writes a short-lived (300s) HttpOnly cookie for a flow nonce.
func (p *OIDCProvider) setFlowCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: 300,
		HttpOnly: true, Secure: p.secure, SameSite: http.SameSiteLaxMode,
	})
}

// clearFlowCookie expires a flow cookie once it has served its purpose.
func (p *OIDCProvider) clearFlowCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: p.secure, SameSite: http.SameSiteLaxMode,
	})
}

// oauthErrorMessage renders an OAuth 2.0 error (RFC 6749) for a text/plain
// response: the error code plus its human-readable description when present. Both
// values originate from the IdP; the caller writes them via http.Error (a
// text/plain body, never HTML), and this clamps and de-controls them so a hostile
// or oversized value can neither bloat the response nor inject control characters.
func oauthErrorMessage(code, desc string) string {
	code = sanitizeOAuthField(code, 64)
	desc = sanitizeOAuthField(desc, 256)
	switch {
	case code == "" && desc == "":
		return "unspecified error"
	case desc == "":
		return code
	case code == "":
		return desc
	default:
		return code + ": " + desc
	}
}

// exchangeErrorMessage surfaces the token endpoint's own error/description when
// the code exchange is rejected — oauth2 parses that JSON body (RFC 6749 §5.2)
// into *oauth2.RetrieveError — and falls back to the generic message for
// transport-level failures that carry no structured OAuth error.
func exchangeErrorMessage(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && (re.ErrorCode != "" || re.ErrorDescription != "") {
		return "token exchange rejected: " + oauthErrorMessage(re.ErrorCode, re.ErrorDescription)
	}
	return "token exchange failed"
}

// sanitizeOAuthField trims an IdP-supplied string, collapses any control
// characters (including CR/LF) to spaces, and truncates it on a rune boundary so
// it is safe and tidy to echo back to the browser.
func sanitizeOAuthField(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max])) + "…"
	}
	return s
}
