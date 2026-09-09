package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockIDP is a minimal OpenID Connect issuer for tests: it serves discovery, a
// JWKS built from a generated RSA key, and a /token endpoint that returns an
// RS256-signed id_token. It lets us exercise the full OIDC flow with no network
// and no external IdP.
type mockIDP struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	clientID string
	sub      string
	email    string
}

func newMockIDP(t *testing.T, clientID, sub, email string) *mockIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	idp := &mockIDP{key: key, clientID: clientID, sub: sub, email: email}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		iss := idp.server.URL
		writeJSON(w, map[string]any{
			"issuer":                                iss,
			"authorization_endpoint":                iss + "/authorize",
			"token_endpoint":                        iss + "/token",
			"jwks_uri":                              iss + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{idp.jwk()}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"access_token": "mock-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idp.signIDToken(t, time.Now().Add(time.Hour)),
		})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

// jwk renders the public key as a JWK the verifier can match by kid.
func (m *mockIDP) jwk() map[string]any {
	pub := m.key.Public().(*rsa.PublicKey)
	return map[string]any{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": "test-key",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// signIDToken hand-rolls an RS256 JWT so tests need no JWT-signing dependency.
func (m *mockIDP) signIDToken(t *testing.T, exp time.Time) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString(mustJSON(t, map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test-key"}))
	payload := enc.EncodeToString(mustJSON(t, map[string]any{
		"iss":   m.server.URL,
		"sub":   m.sub,
		"aud":   m.clientID,
		"email": m.email,
		"exp":   exp.Unix(),
		"iat":   time.Now().Unix(),
	}))
	signingInput := header + "." + payload
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign id_token: %v", err)
	}
	return signingInput + "." + enc.EncodeToString(sig)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// newTestProvider builds an OIDCProvider pointed at the mock issuer.
func newTestProvider(t *testing.T, idp *mockIDP) *OIDCProvider {
	t.Helper()
	p, err := NewOIDCProvider(context.Background(), idp.server.URL, idp.clientID,
		"test-client-secret", "http://app.test/callback", "session-secret-for-tests", false)
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	return p
}

// TestOIDCHappyPath drives Login → Callback → session cookie → Identify.
func TestOIDCHappyPath(t *testing.T) {
	idp := newMockIDP(t, "test-client", "oidc-sub-42", "alice@example.com")
	p := newTestProvider(t, idp)

	// Login: capture the flow cookies and the state we were issued.
	loginRR := httptest.NewRecorder()
	p.Login(loginRR, httptest.NewRequest(http.MethodGet, "/login", nil))
	if loginRR.Code != http.StatusFound {
		t.Fatalf("Login status = %d, want 302", loginRR.Code)
	}
	flowCookies := readCookies(loginRR)
	state := cookieValueOpened(t, p, flowCookies, stateCookie)

	// Callback: replay state + code with the flow cookies attached.
	cbReq := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=mock-code", nil)
	for _, c := range flowCookies {
		cbReq.AddCookie(c)
	}
	cbRR := httptest.NewRecorder()
	p.Callback(cbRR, cbReq)
	if cbRR.Code != http.StatusFound {
		t.Fatalf("Callback status = %d, want 302 (body: %s)", cbRR.Code, cbRR.Body.String())
	}

	// The callback must set a session cookie that Identify then accepts.
	sess := findCookie(readCookies(cbRR), sessionCookie)
	if sess == nil || sess.Value == "" {
		t.Fatalf("Callback did not set a %s cookie", sessionCookie)
	}
	idReq := httptest.NewRequest(http.MethodGet, "/a/x", nil)
	idReq.AddCookie(sess)
	who, ok := p.Identify(idReq)
	if !ok {
		t.Fatalf("Identify rejected the session cookie minted by Callback")
	}
	if who.GetSub() != "oidc-sub-42" || who.GetEmail() != "alice@example.com" {
		t.Fatalf("Identify = (%q, %q), want (oidc-sub-42, alice@example.com)", who.GetSub(), who.GetEmail())
	}
}

// TestOIDCCallbackRejectsBadState ensures a mismatched state fails CSRF check
// before any token exchange.
func TestOIDCCallbackRejectsBadState(t *testing.T) {
	idp := newMockIDP(t, "test-client", "s", "e@x.co")
	p := newTestProvider(t, idp)

	loginRR := httptest.NewRecorder()
	p.Login(loginRR, httptest.NewRequest(http.MethodGet, "/login", nil))
	flowCookies := readCookies(loginRR)

	// Present a state value that does not match the signed state cookie.
	cbReq := httptest.NewRequest(http.MethodGet, "/callback?state=not-the-issued-state&code=mock-code", nil)
	for _, c := range flowCookies {
		cbReq.AddCookie(c)
	}
	cbRR := httptest.NewRecorder()
	p.Callback(cbRR, cbReq)
	if cbRR.Code != http.StatusBadRequest {
		t.Fatalf("Callback with bad state: status = %d, want 400", cbRR.Code)
	}
}

// TestOIDCCallbackRejectsMissingStateCookie covers a callback with no flow
// cookies at all (e.g. a forged/replayed link).
func TestOIDCCallbackRejectsMissingStateCookie(t *testing.T) {
	idp := newMockIDP(t, "test-client", "s", "e@x.co")
	p := newTestProvider(t, idp)

	cbRR := httptest.NewRecorder()
	p.Callback(cbRR, httptest.NewRequest(http.MethodGet, "/callback?state=x&code=y", nil))
	if cbRR.Code != http.StatusBadRequest {
		t.Fatalf("Callback with no state cookie: status = %d, want 400", cbRR.Code)
	}
}

// TestOIDCIdentifyRejectsBadSessions covers the Auth-interface fail-closed paths.
func TestOIDCIdentifyRejectsBadSessions(t *testing.T) {
	idp := newMockIDP(t, "test-client", "s", "e@x.co")
	p := newTestProvider(t, idp)

	// No cookie at all.
	if _, ok := p.Identify(httptest.NewRequest(http.MethodGet, "/a/x", nil)); ok {
		t.Fatalf("Identify accepted a request with no session cookie")
	}

	// Tampered signature.
	good := p.sessions.NewSession("sub-1", "a@b.co")
	tamperReq := httptest.NewRequest(http.MethodGet, "/a/x", nil)
	tamperReq.AddCookie(&http.Cookie{Name: sessionCookie, Value: good + "x"})
	if _, ok := p.Identify(tamperReq); ok {
		t.Fatalf("Identify accepted a tampered session cookie")
	}

	// Expired session (signed by the same secret but past its expiry).
	expired := (&Sessions{secret: []byte("session-secret-for-tests"), ttl: -time.Minute}).NewSession("sub-1", "a@b.co")
	expReq := httptest.NewRequest(http.MethodGet, "/a/x", nil)
	expReq.AddCookie(&http.Cookie{Name: sessionCookie, Value: expired})
	if _, ok := p.Identify(expReq); ok {
		t.Fatalf("Identify accepted an expired session cookie")
	}
}

// TestOIDCIdentifyAcceptsValidBearer covers the CLI API-client path: a valid
// OIDC id_token presented as `Authorization: Bearer` resolves an identity, with
// no session cookie present.
func TestOIDCIdentifyAcceptsValidBearer(t *testing.T) {
	idp := newMockIDP(t, "test-client", "oidc-sub-99", "bob@example.com")
	p := newTestProvider(t, idp)

	raw := idp.signIDToken(t, time.Now().Add(time.Hour))
	req := httptest.NewRequest(http.MethodPost, "/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+raw)

	who, ok := p.Identify(req)
	if !ok {
		t.Fatalf("Identify rejected a valid bearer id_token")
	}
	if who.GetSub() != "oidc-sub-99" || who.GetEmail() != "bob@example.com" {
		t.Fatalf("Identify = (%q, %q), want (oidc-sub-99, bob@example.com)", who.GetSub(), who.GetEmail())
	}
}

// TestOIDCIdentifyRejectsBadBearer confirms the bearer path fails closed on a
// malformed token and on an expired-but-well-signed token.
func TestOIDCIdentifyRejectsBadBearer(t *testing.T) {
	idp := newMockIDP(t, "test-client", "s", "e@x.co")
	p := newTestProvider(t, idp)

	// Garbage token: not a verifiable JWT.
	garbage := httptest.NewRequest(http.MethodPost, "/artifacts", nil)
	garbage.Header.Set("Authorization", "Bearer not-a-jwt")
	if _, ok := p.Identify(garbage); ok {
		t.Fatalf("Identify accepted a garbage bearer token")
	}

	// Expired token: correctly signed by the issuer but past its exp.
	expired := idp.signIDToken(t, time.Now().Add(-time.Hour))
	expReq := httptest.NewRequest(http.MethodPost, "/artifacts", nil)
	expReq.Header.Set("Authorization", "Bearer "+expired)
	if _, ok := p.Identify(expReq); ok {
		t.Fatalf("Identify accepted an expired bearer id_token")
	}
}

// --- cookie helpers ---

func readCookies(rr *httptest.ResponseRecorder) []*http.Cookie {
	return (&http.Response{Header: rr.Result().Header}).Cookies()
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func cookieValueOpened(t *testing.T, p *OIDCProvider, cookies []*http.Cookie, name string) string {
	t.Helper()
	c := findCookie(cookies, name)
	if c == nil {
		t.Fatalf("missing %s cookie", name)
	}
	v, ok := p.sessions.openValue(c.Value)
	if !ok {
		t.Fatalf("%s cookie failed signature check", name)
	}
	return v
}
