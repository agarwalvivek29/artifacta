package config

import "testing"

// ARTIFACTA_ROOT_DOMAIN feeds Config.RootDomain (ADR-0017), and is empty by
// default so subdomain routing stays off unless explicitly enabled.
func TestRootDomainFromEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARTIFACTA_HOME", home) // no config.json here → defaults + env only

	// Default: unset env leaves RootDomain empty (subdomain hosting disabled).
	t.Setenv("ARTIFACTA_ROOT_DOMAIN", "")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.RootDomain != "" {
		t.Errorf("default RootDomain = %q, want empty", c.RootDomain)
	}

	// Set: the env value wins.
	t.Setenv("ARTIFACTA_ROOT_DOMAIN", "artifacta.genorim.xyz")
	c, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.RootDomain != "artifacta.genorim.xyz" {
		t.Errorf("RootDomain = %q, want artifacta.genorim.xyz", c.RootDomain)
	}
}
