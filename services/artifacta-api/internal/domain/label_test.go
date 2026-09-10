package domain

import (
	"strings"
	"testing"
)

func TestValidLabel_accepts(t *testing.T) {
	valid := []string{
		"a", "ab", "app1", "my-app", "my-cool-page", "x2", "team-dashboard",
		"a1b2c3", strings.Repeat("a", 63), // 63 is the max DNS label length
	}
	for _, s := range valid {
		if !ValidLabel(s) {
			t.Errorf("ValidLabel(%q) = false, want true", s)
		}
	}
}

func TestValidLabel_rejects(t *testing.T) {
	invalid := []string{
		"",                      // empty
		"-lead",                 // leading hyphen
		"trail-",                // trailing hyphen
		"UPPER",                 // uppercase (callers must lower-case first)
		"has_underscore",        // underscore not allowed
		"has.dot",               // dot would be a second subdomain level
		"has space",             // whitespace
		"héllo",                 // non-ascii
		strings.Repeat("a", 64), // 64 > 63 max DNS label length
	}
	for _, s := range invalid {
		if ValidLabel(s) {
			t.Errorf("ValidLabel(%q) = true, want false", s)
		}
	}
}

func TestValidLabel_rejectsReserved(t *testing.T) {
	// Platform-reserved labels must never be claimable as custom artifact labels.
	for _, s := range []string{"www", "api", "app", "admin", "login", "logout", "health", "metrics", "artifacta", "a" + "pi"} {
		if ValidLabel(s) {
			t.Errorf("ValidLabel(%q) = true, want false (reserved)", s)
		}
	}
}
