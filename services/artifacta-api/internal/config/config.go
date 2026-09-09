// Package config loads and persists local artifacta configuration under
// $ARTIFACTA_HOME (default ~/.artifacta).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

type Config struct {
	Addr    string `json:"addr"`
	BaseURL string `json:"base_url"`
	DataDir string `json:"data_dir"`
	Token   string `json:"token"`
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	// OIDC browser-SSO settings (ADR-0007, FR6). When OIDCIssuer + OIDCClientID
	// are set, the server wires the OIDC provider and its /login + /callback
	// handlers; otherwise it falls back to the Local single-token adapter.
	OIDCIssuer       string `json:"oidc_issuer"`
	OIDCClientID     string `json:"oidc_client_id"`
	OIDCClientSecret string `json:"oidc_client_secret"`
	OIDCRedirectURL  string `json:"oidc_redirect_url"`
	// SessionSecret keys the HMAC signature on stateless session cookies. Never
	// commit a real value — supply via ARTIFACTA_SESSION_SECRET.
	SessionSecret string `json:"session_secret"`
	// AccessToken holds the OIDC id_token obtained by `artifacta login` and sent as
	// `Authorization: Bearer <id_token>` when publishing to a remote API (ADR-0007).
	// Stored in the 0600 config file for now.
	// TODO(hardening): move token to OS keychain (ADR-0007).
	AccessToken string `json:"access_token"`
}

// OIDCEnabled reports whether enough OIDC config is present to wire browser SSO.
func (c Config) OIDCEnabled() bool {
	return c.OIDCIssuer != "" && c.OIDCClientID != ""
}

func (c Config) Identity() *artifactav1.Identity {
	return &artifactav1.Identity{Sub: c.Sub, Email: c.Email}
}

func Dir() string {
	if d := os.Getenv("ARTIFACTA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".artifacta")
}

func Path() string { return filepath.Join(Dir(), "config.json") }

func Default() Config {
	return Config{
		Addr:    ":8080",
		BaseURL: "http://localhost:8080",
		DataDir: filepath.Join(Dir(), "data"),
	}
}

func Load() (Config, error) {
	c := Default()
	b, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			applyEnv(&c)
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	applyEnv(&c)
	return c, nil
}

// applyEnv overrides config fields from ARTIFACTA_-prefixed environment
// variables. An env var only wins when it is set and non-empty, so unset
// vars leave the file/default values untouched.
func applyEnv(c *Config) {
	setFromEnv("ARTIFACTA_ADDR", &c.Addr)
	setFromEnv("ARTIFACTA_BASE_URL", &c.BaseURL)
	setFromEnv("ARTIFACTA_DATA_DIR", &c.DataDir)
	setFromEnv("ARTIFACTA_OIDC_ISSUER", &c.OIDCIssuer)
	setFromEnv("ARTIFACTA_OIDC_CLIENT_ID", &c.OIDCClientID)
	setFromEnv("ARTIFACTA_OIDC_CLIENT_SECRET", &c.OIDCClientSecret)
	setFromEnv("ARTIFACTA_OIDC_REDIRECT_URL", &c.OIDCRedirectURL)
	setFromEnv("ARTIFACTA_SESSION_SECRET", &c.SessionSecret)
	setFromEnv("ARTIFACTA_ACCESS_TOKEN", &c.AccessToken)
}

// setFromEnv writes the value of env var key into dst only when it is non-empty.
func setFromEnv(key string, dst *string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func Save(c Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), b, 0o600)
}
