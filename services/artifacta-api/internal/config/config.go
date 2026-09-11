// Package config loads and persists local artifacta configuration under
// $ARTIFACTA_HOME (default ~/.artifacta).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

type Config struct {
	Addr    string `json:"addr"`
	BaseURL string `json:"base_url"`
	DataDir string `json:"data_dir"`
	// RootDomain enables subdomain artifact hosting (ADR-0017). When set (e.g.
	// "artifacta.genorim.xyz"), requests to {slug|label}.{RootDomain} resolve to
	// that artifact and serve its bytes at the subdomain root. Empty disables
	// subdomain routing entirely — path-based /a/{slug} is unaffected either way.
	RootDomain string `json:"root_domain"`
	// OrgName and LogoURL brand the dashboard/viewer chrome for the operator's
	// organization (cosmetic). LogoURL is an image URL shown in the navbar; OrgName
	// is the wordmark beside it. Both empty falls back to the ArtifactA wordmark.
	OrgName string `json:"org_name"`
	LogoURL string `json:"logo_url"`
	// CDNEgress selects the render/serve posture (ARTIFACTA_CDN_EGRESS=allow|deny,
	// ADR-0023). false (default, "deny") is air-gapped: artifacts are bundled
	// self-contained at publish and served under a strict no-external-hosts CSP.
	// true ("allow") keeps import maps and a permissive https: CSP so artifacts
	// resolve any dependency from a CDN at view time. The default is deny to match
	// the product's no-third-party-egress posture (ADR-0010/0011).
	CDNEgress bool `json:"cdn_egress"`
	// StoreBackend selects the metadata store: "file" (default; JSON on local disk,
	// zero dependencies, single-instance) or "postgres" (ADR-0009; horizontally
	// scalable, uniqueness enforced by the DB). DatabaseURL is the postgres DSN,
	// required when StoreBackend is "postgres".
	StoreBackend string `json:"store_backend"`
	DatabaseURL  string `json:"database_url"`
	// BlobBackend selects the artifact-bytes store (ADR-0006): "file" (default;
	// bundles on local disk under DataDir/blobs) or "s3" (any S3-compatible
	// backend — AWS S3, MinIO, R2, B2, Wasabi). The S3 backend is used strictly
	// server-side: the app performs Get/Put with its own credentials and streams
	// bytes through itself after CanView — no presigned URLs reach the client. The
	// S3* fields configure the "s3" backend and are ignored for "file".
	BlobBackend       string `json:"blob_backend"`
	S3Endpoint        string `json:"s3_endpoint"`
	S3Region          string `json:"s3_region"`
	S3Bucket          string `json:"s3_bucket"`
	S3AccessKeyID     string `json:"s3_access_key_id"`
	S3SecretAccessKey string `json:"s3_secret_access_key"`
	S3ForcePathStyle  bool   `json:"s3_force_path_style"`
	Token             string `json:"token"`
	Sub               string `json:"sub"`
	Email             string `json:"email"`
	// OIDC browser-SSO settings (ADR-0007, FR6). When OIDCIssuer + OIDCClientID
	// are set, the server wires the OIDC provider and its /login + /callback
	// handlers; otherwise it falls back to the Local single-token adapter.
	OIDCIssuer       string `json:"oidc_issuer"`
	OIDCClientID     string `json:"oidc_client_id"`
	OIDCClientSecret string `json:"oidc_client_secret"`
	OIDCRedirectURL  string `json:"oidc_redirect_url"`
	// OIDCCLIRedirectURI is the fixed loopback redirect URI the CLI login uses
	// (e.g. http://127.0.0.1:53682/callback). The server advertises it via the CLI
	// discovery endpoint so `artifacta login` binds this exact port instead of a
	// random one — required by IdPs (Okta, …) that match redirect URIs exactly. The
	// operator registers the same URI in the IdP app. Empty = the CLI falls back to
	// an ephemeral loopback port (only works where the IdP allows any loopback port).
	OIDCCLIRedirectURI string `json:"oidc_cli_redirect_uri"`
	// SessionSecret keys the HMAC signature on stateless session cookies. Never
	// commit a real value — supply via ARTIFACTA_SESSION_SECRET.
	SessionSecret string `json:"session_secret"`
	// AccessToken holds the OIDC id_token obtained by `artifacta login` and sent as
	// `Authorization: Bearer <id_token>` when publishing to a remote API (ADR-0007).
	// RefreshToken (offline_access) lets the CLI mint a fresh id_token without a new
	// browser login; AccessTokenExpiry (RFC3339) is the id_token's exp, so the CLI
	// refreshes proactively instead of on a failed request. Both are stored in the
	// 0600 config file for now.
	// TODO(hardening): move tokens to OS keychain (ADR-0007).
	AccessToken       string `json:"access_token"`
	RefreshToken      string `json:"refresh_token"`
	AccessTokenExpiry string `json:"access_token_expiry"`
	// LoggedIn records that `artifacta login` completed against a remote server, so
	// the CLI routes commands to that server explicitly rather than inferring intent
	// from the base URL. Cleared when a refresh fails and re-login is required.
	LoggedIn bool `json:"logged_in"`
	// LogLevel sets the server's structured-log verbosity: debug/info/warn/error
	// (default info). Overridable via ARTIFACTA_LOG_LEVEL.
	LogLevel string `json:"log_level"`
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
	setFromEnv("ARTIFACTA_ROOT_DOMAIN", &c.RootDomain)
	setFromEnv("ARTIFACTA_ORG_NAME", &c.OrgName)
	setFromEnv("ARTIFACTA_LOGO_URL", &c.LogoURL)
	// ARTIFACTA_CDN_EGRESS is worded allow|deny (not a bare bool): "allow" opts
	// into CDN egress; anything else (incl. "deny" and the unset default) is
	// air-gapped. Only override when the var is set so a config.json value stands.
	if v := os.Getenv("ARTIFACTA_CDN_EGRESS"); v != "" {
		c.CDNEgress = strings.EqualFold(v, "allow")
	}
	setFromEnv("ARTIFACTA_STORE", &c.StoreBackend)
	setFromEnv("ARTIFACTA_DATABASE_URL", &c.DatabaseURL)
	setFromEnv("ARTIFACTA_BLOB", &c.BlobBackend)
	setFromEnv("ARTIFACTA_S3_ENDPOINT", &c.S3Endpoint)
	setFromEnv("ARTIFACTA_S3_REGION", &c.S3Region)
	setFromEnv("ARTIFACTA_S3_BUCKET", &c.S3Bucket)
	setFromEnv("ARTIFACTA_S3_ACCESS_KEY_ID", &c.S3AccessKeyID)
	setFromEnv("ARTIFACTA_S3_SECRET_ACCESS_KEY", &c.S3SecretAccessKey)
	setBoolFromEnv("ARTIFACTA_S3_FORCE_PATH_STYLE", &c.S3ForcePathStyle)
	setFromEnv("ARTIFACTA_OIDC_ISSUER", &c.OIDCIssuer)
	setFromEnv("ARTIFACTA_OIDC_CLIENT_ID", &c.OIDCClientID)
	setFromEnv("ARTIFACTA_OIDC_CLIENT_SECRET", &c.OIDCClientSecret)
	setFromEnv("ARTIFACTA_OIDC_REDIRECT_URL", &c.OIDCRedirectURL)
	setFromEnv("ARTIFACTA_OIDC_CLI_REDIRECT_URI", &c.OIDCCLIRedirectURI)
	setFromEnv("ARTIFACTA_SESSION_SECRET", &c.SessionSecret)
	setFromEnv("ARTIFACTA_ACCESS_TOKEN", &c.AccessToken)
	setFromEnv("ARTIFACTA_REFRESH_TOKEN", &c.RefreshToken)
	setFromEnv("ARTIFACTA_LOG_LEVEL", &c.LogLevel)
	// The server's own single-token identity (Local auth). Lets a containerized
	// local-auth deploy inject the token/identity via env instead of shipping a
	// pre-written config.json, and makes the serve() empty-token guard actionable.
	setFromEnv("ARTIFACTA_TOKEN", &c.Token)
	setFromEnv("ARTIFACTA_SUB", &c.Sub)
	setFromEnv("ARTIFACTA_EMAIL", &c.Email)
}

// setFromEnv writes the value of env var key into dst only when it is non-empty.
func setFromEnv(key string, dst *string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

// setBoolFromEnv sets dst from a truthy env var ("1", "true", "yes", any case).
// It only overrides when the var is set and non-empty, matching setFromEnv.
func setBoolFromEnv(key string, dst *bool) {
	if v := os.Getenv(key); v != "" {
		*dst = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
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
