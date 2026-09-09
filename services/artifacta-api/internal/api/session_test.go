package api

import (
	"strings"
	"testing"
	"time"
)

func TestSessionMintAndParseRoundTrip(t *testing.T) {
	s := NewSessions("test-secret-please-ignore")
	tok := s.NewSession("oidc|abc123", "user@example.com")

	sub, email, ok := s.ParseSession(tok)
	if !ok {
		t.Fatalf("ParseSession rejected a freshly minted token")
	}
	if sub != "oidc|abc123" {
		t.Errorf("sub = %q, want %q", sub, "oidc|abc123")
	}
	if email != "user@example.com" {
		t.Errorf("email = %q, want %q", email, "user@example.com")
	}
}

func TestSessionRejectsTamperedSignature(t *testing.T) {
	s := NewSessions("test-secret-please-ignore")
	tok := s.NewSession("sub-1", "a@b.co")

	// Flip the last character of the signature segment.
	i := strings.LastIndexByte(tok, '.')
	last := tok[len(tok)-1]
	swapped := byte('A')
	if last == 'A' {
		swapped = 'B'
	}
	tampered := tok[:len(tok)-1] + string(swapped)
	if tampered == tok || i < 0 {
		t.Fatalf("failed to construct a tampered token")
	}

	if _, _, ok := s.ParseSession(tampered); ok {
		t.Fatalf("ParseSession accepted a token with a tampered signature")
	}
}

func TestSessionRejectsForeignSecret(t *testing.T) {
	tok := NewSessions("secret-A").NewSession("sub-1", "a@b.co")
	if _, _, ok := NewSessions("secret-B").ParseSession(tok); ok {
		t.Fatalf("ParseSession accepted a token signed with a different secret")
	}
}

func TestSessionRejectsExpired(t *testing.T) {
	// TTL in the past → token is already expired at mint time.
	s := &Sessions{secret: []byte("test-secret"), ttl: -time.Minute}
	tok := s.NewSession("sub-1", "a@b.co")
	if _, _, ok := s.ParseSession(tok); ok {
		t.Fatalf("ParseSession accepted an expired token")
	}
}

func TestSessionRejectsMalformed(t *testing.T) {
	s := NewSessions("test-secret")
	for _, bad := range []string{"", "no-dot", "a.b.c.d", "notbase64.####"} {
		if _, _, ok := s.ParseSession(bad); ok {
			t.Errorf("ParseSession accepted malformed token %q", bad)
		}
	}
}

func TestSignValueRoundTripAndTamper(t *testing.T) {
	s := NewSessions("test-secret")
	signed := s.signValue("state-nonce-xyz")

	v, ok := s.openValue(signed)
	if !ok || v != "state-nonce-xyz" {
		t.Fatalf("openValue = (%q, %v), want (state-nonce-xyz, true)", v, ok)
	}
	if _, ok := s.openValue(signed + "x"); ok {
		t.Fatalf("openValue accepted a tampered value")
	}
}
