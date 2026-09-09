package config

import "testing"

// isolateHome points ARTIFACTA_HOME at an empty temp dir so Load has no
// config.json and falls back to defaults before applying env overrides.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("ARTIFACTA_HOME", t.TempDir())
}

func mustLoad(t *testing.T) Config {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	return c
}

func TestLoadEnvOverridesDefaults(t *testing.T) {
	isolateHome(t)
	t.Setenv("ARTIFACTA_ADDR", ":9090")
	t.Setenv("ARTIFACTA_BASE_URL", "https://artifacta.example.com")
	t.Setenv("ARTIFACTA_DATA_DIR", "/custom/data")
	t.Setenv("ARTIFACTA_OIDC_ISSUER", "https://issuer.example.com")
	t.Setenv("ARTIFACTA_OIDC_CLIENT_ID", "client-123")

	c := mustLoad(t)

	if c.Addr != ":9090" {
		t.Errorf("Addr = %q, want %q", c.Addr, ":9090")
	}
	if c.BaseURL != "https://artifacta.example.com" {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL, "https://artifacta.example.com")
	}
	if c.DataDir != "/custom/data" {
		t.Errorf("DataDir = %q, want %q", c.DataDir, "/custom/data")
	}
	if c.OIDCIssuer != "https://issuer.example.com" {
		t.Errorf("OIDCIssuer = %q, want %q", c.OIDCIssuer, "https://issuer.example.com")
	}
	if c.OIDCClientID != "client-123" {
		t.Errorf("OIDCClientID = %q, want %q", c.OIDCClientID, "client-123")
	}
}

func TestLoadWithoutEnvKeepsDefaults(t *testing.T) {
	isolateHome(t)
	// Ensure no override leaks in from the ambient environment.
	t.Setenv("ARTIFACTA_ADDR", "")
	t.Setenv("ARTIFACTA_BASE_URL", "")
	t.Setenv("ARTIFACTA_DATA_DIR", "")

	// Default() must be computed after ARTIFACTA_HOME is set, since DataDir
	// derives from it.
	def := Default()
	c := mustLoad(t)

	if c.Addr != def.Addr {
		t.Errorf("Addr = %q, want default %q", c.Addr, def.Addr)
	}
	if c.BaseURL != def.BaseURL {
		t.Errorf("BaseURL = %q, want default %q", c.BaseURL, def.BaseURL)
	}
	if c.DataDir != def.DataDir {
		t.Errorf("DataDir = %q, want default %q", c.DataDir, def.DataDir)
	}
	if c.OIDCIssuer != "" {
		t.Errorf("OIDCIssuer = %q, want empty", c.OIDCIssuer)
	}
	if c.OIDCClientID != "" {
		t.Errorf("OIDCClientID = %q, want empty", c.OIDCClientID)
	}
}

// TestSetFromEnvIgnoresEmpty verifies the helper leaves the destination
// untouched when the env var is unset or empty.
func TestSetFromEnvIgnoresEmpty(t *testing.T) {
	t.Setenv("ARTIFACTA_TEST_UNSET", "")
	got := "original"
	setFromEnv("ARTIFACTA_TEST_UNSET", &got)
	if got != "original" {
		t.Errorf("setFromEnv overwrote with empty env: got %q", got)
	}

	t.Setenv("ARTIFACTA_TEST_SET", "new")
	setFromEnv("ARTIFACTA_TEST_SET", &got)
	if got != "new" {
		t.Errorf("setFromEnv did not apply non-empty env: got %q", got)
	}
}
