package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// Sessions mints and verifies stateless, signed session tokens. A token carries
// the principal's `sub`, `email`, and an expiry, authenticated by an HMAC-SHA256
// signature over a server-held secret. Because the secret never leaves the
// server, a client cannot forge or tamper with a token: any change breaks the
// signature. This lets the viewer authenticate a browser from a self-contained
// cookie with no server-side session store.
type Sessions struct {
	secret []byte
	ttl    time.Duration
}

// defaultSessionTTL bounds how long a minted session cookie stays valid.
const defaultSessionTTL = 12 * time.Hour

// NewSessions builds a signer from the raw session secret. TTL defaults to
// defaultSessionTTL.
func NewSessions(secret string) *Sessions {
	return &Sessions{secret: []byte(secret), ttl: defaultSessionTTL}
}

var b64 = base64.RawURLEncoding

// sign returns the base64url HMAC-SHA256 of msg under the secret.
func (s *Sessions) sign(msg string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(msg))
	return b64.EncodeToString(m.Sum(nil))
}

// mint appends a signature to msg, producing "<msg>.<sig>".
func (s *Sessions) mint(msg string) string {
	return msg + "." + s.sign(msg)
}

// open verifies a "<msg>.<sig>" token and returns the payload msg. The HMAC
// comparison is constant-time to avoid leaking the signature via timing.
func (s *Sessions) open(tok string) (string, bool) {
	i := strings.LastIndexByte(tok, '.')
	if i < 0 {
		return "", false
	}
	msg, sig := tok[:i], tok[i+1:]
	if !hmac.Equal([]byte(sig), []byte(s.sign(msg))) {
		return "", false
	}
	return msg, true
}

// NewSession mints a signed session token carrying sub|email and an expiry
// `ttl` from now. sub and email are base64url-encoded so that neither can
// contain the '|' field delimiter and break parsing.
func (s *Sessions) NewSession(sub, email string) string {
	exp := time.Now().Add(s.ttl).Unix()
	msg := b64.EncodeToString([]byte(sub)) + "|" +
		b64.EncodeToString([]byte(email)) + "|" +
		strconv.FormatInt(exp, 10)
	return s.mint(msg)
}

// ParseSession verifies a session token's signature and expiry and returns the
// carried sub and email. ok is false on any signature mismatch, malformed
// payload, or expired token.
func (s *Sessions) ParseSession(tok string) (sub, email string, ok bool) {
	msg, ok := s.open(tok)
	if !ok {
		return "", "", false
	}
	parts := strings.Split(msg, "|")
	if len(parts) != 3 {
		return "", "", false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() >= exp {
		return "", "", false
	}
	subB, err := b64.DecodeString(parts[0])
	if err != nil {
		return "", "", false
	}
	emailB, err := b64.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}
	return string(subB), string(emailB), true
}

// signValue signs an opaque short-lived value (e.g. an OAuth `state` nonce or a
// PKCE verifier) for storage in a cookie, so a tampered cookie is rejected.
func (s *Sessions) signValue(v string) string { return s.mint(v) }

// openValue verifies a value produced by signValue and returns the original.
func (s *Sessions) openValue(tok string) (string, bool) { return s.open(tok) }
