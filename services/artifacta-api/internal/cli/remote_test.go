package cli

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/config"
)

// unverifiedJWT builds header.payload.sig with the given exp. freshToken only
// decodes the payload's exp (the server re-verifies the real signature), so the
// signature here is a placeholder — no signing key needed.
func unverifiedJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	enc := base64.RawURLEncoding
	hdr := enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"sub": "cli-sub", "email": "cli@x.co", "exp": exp.Unix()})
	return hdr + "." + enc.EncodeToString(payload) + ".sig"
}

// mockIssuer is a minimal OIDC issuer for freshToken: discovery + a /token
// endpoint that honors the refresh_token grant. withIDToken=false reproduces an
// IdP that returns only an access_token on refresh (the D2 edge case).
func mockIssuer(t *testing.T, withIDToken bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"access_token":  "new-access",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "rotated-refresh",
		}
		if withIDToken {
			resp["id_token"] = unverifiedJWT(t, time.Now().Add(time.Hour))
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv.Config.Handler = mux
	t.Cleanup(srv.Close)
	return srv
}

func TestFreshTokenReturnsValidCachedToken(t *testing.T) {
	t.Setenv("ARTIFACTA_HOME", t.TempDir())
	c := config.Config{
		AccessToken:       "cached-id-token",
		AccessTokenExpiry: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	tok, err := freshToken(&c)
	if err != nil {
		t.Fatalf("freshToken: %v", err)
	}
	if tok != "cached-id-token" {
		t.Fatalf("freshToken returned %q, want the cached token (no refresh when still valid)", tok)
	}
}

func TestFreshTokenRefreshesExpired(t *testing.T) {
	t.Setenv("ARTIFACTA_HOME", t.TempDir())
	idp := mockIssuer(t, true)
	c := config.Config{
		OIDCIssuer:        idp.URL,
		OIDCClientID:      "cli",
		RefreshToken:      "old-refresh",
		AccessToken:       "", // expired/absent → must refresh
		AccessTokenExpiry: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		LoggedIn:          true,
	}
	tok, err := freshToken(&c)
	if err != nil {
		t.Fatalf("freshToken refresh: %v", err)
	}
	if tok == "" {
		t.Fatal("freshToken returned an empty token after refresh")
	}
	// The rotated refresh token + new expiry are persisted for next time.
	if c.RefreshToken != "rotated-refresh" {
		t.Fatalf("refresh token not rotated: %q", c.RefreshToken)
	}
	if c.AccessToken != tok || c.AccessTokenExpiry == "" {
		t.Fatalf("new id_token/expiry not persisted: %+v", c)
	}
}

// D2: a refresh that returns no id_token must clear creds and error, not loop.
func TestFreshTokenRefreshWithoutIDTokenClearsCreds(t *testing.T) {
	t.Setenv("ARTIFACTA_HOME", t.TempDir())
	idp := mockIssuer(t, false) // token endpoint omits id_token
	c := config.Config{
		OIDCIssuer:   idp.URL,
		OIDCClientID: "cli",
		RefreshToken: "old-refresh",
		LoggedIn:     true,
	}
	if _, err := freshToken(&c); err == nil {
		t.Fatal("freshToken: expected an error when the refresh returns no id_token")
	}
	if c.RefreshToken != "" || c.AccessToken != "" || c.LoggedIn {
		t.Fatalf("creds not cleared after unusable refresh: %+v", c)
	}
}

func TestFreshTokenNotLoggedIn(t *testing.T) {
	t.Setenv("ARTIFACTA_HOME", t.TempDir())
	c := config.Config{}
	if _, err := freshToken(&c); err == nil {
		t.Fatal("freshToken: expected 'not logged in' error with no refresh token")
	}
}

func TestRemoteTargetAndLocalhost(t *testing.T) {
	cases := []struct {
		name string
		c    config.Config
		want bool
	}{
		{"logged in", config.Config{LoggedIn: true, BaseURL: "http://localhost:8080"}, true},
		{"remote url, logged out", config.Config{BaseURL: "https://here.now"}, true},
		{"localhost, logged out", config.Config{BaseURL: "http://localhost:8080"}, false},
		{"127.0.0.1, logged out", config.Config{BaseURL: "http://127.0.0.1:9000"}, false},
		{"empty, logged out", config.Config{}, false},
	}
	for _, tc := range cases {
		if got := remoteTarget(tc.c); got != tc.want {
			t.Errorf("%s: remoteTarget = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTokenExpired(t *testing.T) {
	if !tokenExpired("") {
		t.Error("empty expiry should be treated as expired")
	}
	if !tokenExpired("not-a-time") {
		t.Error("unparseable expiry should be treated as expired")
	}
	if !tokenExpired(time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339)) {
		t.Error("within-skew expiry should be treated as expired")
	}
	if tokenExpired(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)) {
		t.Error("far-future expiry should be valid")
	}
}
