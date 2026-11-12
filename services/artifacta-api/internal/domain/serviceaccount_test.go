package domain

import (
	"strings"
	"testing"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestValidSAName(t *testing.T) {
	for _, s := range []string{"ci", "ci-reports", "a1", "9bot", strings.Repeat("a", 63)} {
		if !ValidSAName(s) {
			t.Errorf("ValidSAName(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "a", "-ci", "CI", "ci_reports", "ci.reports", "sa:ci", strings.Repeat("a", 64)} {
		if ValidSAName(s) {
			t.Errorf("ValidSAName(%q) = true, want false", s)
		}
	}
}

func TestNewAPIKey(t *testing.T) {
	raw, hash, prefix := NewAPIKey()
	if !strings.HasPrefix(raw, APIKeyPrefix) {
		t.Fatalf("raw %q lacks %q", raw, APIKeyPrefix)
	}
	if len(raw) != len(APIKeyPrefix)+43 { // 32 bytes base64url, no padding
		t.Fatalf("raw length = %d", len(raw))
	}
	if hash != HashAPIKey(raw) || len(hash) != 64 {
		t.Fatalf("hash mismatch or wrong length: %q", hash)
	}
	if prefix != raw[:12] || KeyPrefix(raw) != prefix {
		t.Fatalf("prefix = %q", prefix)
	}
	raw2, _, _ := NewAPIKey()
	if raw2 == raw {
		t.Fatal("two keys are equal")
	}
}

func TestParseTTL(t *testing.T) {
	allowed := []string{"30d", "90d", "never"}
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"30d", 30 * 24 * time.Hour, false},
		{" 90D ", 90 * 24 * time.Hour, false},
		{"never", 0, false},
		{"7d", 0, true}, // not in the allowed list
		{"", 0, true},   // empty
		{"30", 0, true}, // not allowed
		{"forever", 0, true},
	}
	for _, c := range cases {
		got, err := ParseTTL(c.in, allowed)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ParseTTL(%q) = %v, %v; want %v, err=%v", c.in, got, err, c.want, c.wantErr)
		}
	}
	if _, err := ParseTTL("x", []string{"x"}); err == nil {
		t.Error("ParseTTL accepted a malformed allowed entry")
	}
}

func TestKeyUsableAndStatus(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	ts := timestamppb.New
	cases := []struct {
		name   string
		k      *artifactav1.ApiKey
		usable bool
		status string
	}{
		{"fresh", &artifactav1.ApiKey{CreatedAt: ts(now.Add(-time.Hour))}, true, "active"},
		{"never-expiring used recently", &artifactav1.ApiKey{CreatedAt: ts(now.AddDate(-1, 0, 0)), LastUsedAt: ts(now.Add(-24 * time.Hour))}, true, "active"},
		{"revoked", &artifactav1.ApiKey{CreatedAt: ts(now.Add(-time.Hour)), RevokedAt: ts(now)}, false, "revoked"},
		{"expired", &artifactav1.ApiKey{CreatedAt: ts(now.AddDate(0, 0, -31)), ExpiresAt: ts(now.Add(-time.Second))}, false, "expired"},
		{"expires exactly now", &artifactav1.ApiKey{CreatedAt: ts(now.AddDate(0, 0, -30)), ExpiresAt: ts(now)}, false, "expired"},
		{"never used for 91d", &artifactav1.ApiKey{CreatedAt: ts(now.AddDate(0, 0, -91))}, true, "stale"},
		{"last used 91d ago", &artifactav1.ApiKey{CreatedAt: ts(now.AddDate(-1, 0, 0)), LastUsedAt: ts(now.AddDate(0, 0, -91))}, true, "stale"},
	}
	for _, c := range cases {
		if got := KeyUsable(c.k, now); got != c.usable {
			t.Errorf("%s: KeyUsable = %v, want %v", c.name, got, c.usable)
		}
		if got := KeyStatus(c.k, now); got != c.status {
			t.Errorf("%s: KeyStatus = %q, want %q", c.name, got, c.status)
		}
	}
}

func TestIsAdmin(t *testing.T) {
	groups := []string{"DevOps-Engineering-L1", "DevOps-Engineering-L2"}
	emails := []string{"Admin@Example.com"}
	id := func(sub, email string, g ...string) *artifactav1.Identity {
		return &artifactav1.Identity{Sub: sub, Email: email, Groups: g, GroupsClaimPresent: g != nil}
	}
	cases := []struct {
		name           string
		who            *artifactav1.Identity
		groups, emails []string
		want           bool
	}{
		{"group member", id("u1", "x@example.com", "Eng", "DevOps-Engineering-L2"), groups, emails, true},
		{"email allow-listed (case-insensitive)", id("u2", "admin@example.com"), groups, emails, true},
		{"neither", id("u3", "x@example.com", "Eng"), groups, emails, false},
		{"no groups claim, not allow-listed", id("u4", "x@example.com"), groups, emails, false},
		{"service account in admin group", id("sa:ci", "", "DevOps-Engineering-L1"), groups, emails, false},
		{"both lists empty", id("u1", "admin@example.com", "DevOps-Engineering-L1"), nil, nil, false},
		{"empty admin entries match nothing", id("u5", "", ""), []string{""}, []string{""}, false},
		{"nil identity", nil, groups, emails, false},
		{"empty sub", id("", "admin@example.com"), groups, emails, false},
	}
	for _, c := range cases {
		if got := IsAdmin(c.who, c.groups, c.emails); got != c.want {
			t.Errorf("%s: IsAdmin = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsServiceAccount(t *testing.T) {
	if !IsServiceAccount(SASub("ci")) || IsServiceAccount("00u123") || IsServiceAccount("") {
		t.Fatal("IsServiceAccount misclassifies")
	}
}
