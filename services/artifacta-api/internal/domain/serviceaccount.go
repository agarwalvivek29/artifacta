package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

// Service accounts (ADR-0028): named non-human principals that own API keys.

// SASubPrefix marks a service account's subject ("sa:<name>"). OIDC subjects
// never start with it, so the two namespaces cannot collide; an OIDC token
// whose sub carries it is rejected by the auth layer.
const SASubPrefix = "sa:"

// APIKeyPrefix starts every raw API key. The auth layer routes a bearer token
// to the key store only when it carries this prefix.
const APIKeyPrefix = "afa_"

// keyDisplayLen is how much of the raw key is stored for display: the "afa_"
// marker plus 8 random characters — enough to tell keys apart, far too little
// to guess the rest.
const keyDisplayLen = len(APIKeyPrefix) + 8

// StaleAfter is when an active key unused for this long is reported "stale".
const StaleAfter = 90 * 24 * time.Hour

// saName: 2–63 chars of [a-z0-9-], starting with a letter or digit.
var saName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// ValidSAName reports whether name is an acceptable service-account name.
func ValidSAName(name string) bool { return saName.MatchString(name) }

// SASub returns the subject of the named service account.
func SASub(name string) string { return SASubPrefix + name }

// IsServiceAccount reports whether sub is a service account's subject.
func IsServiceAccount(sub string) bool { return strings.HasPrefix(sub, SASubPrefix) }

// NewAPIKey returns a fresh raw key (256 random bits), its storage hash and its
// display prefix. Only the hash and prefix may be persisted.
func NewAPIKey() (raw, hash, prefix string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	raw = APIKeyPrefix + base64.RawURLEncoding.EncodeToString(b)
	return raw, HashAPIKey(raw), raw[:keyDisplayLen]
}

// HashAPIKey is the storage form of a raw key: hex sha256. Lookups are by hash,
// so comparing keys never touches the raw value.
func HashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// KeyPrefix returns the display prefix of a raw key, safe to log.
func KeyPrefix(raw string) string {
	if len(raw) < keyDisplayLen {
		return raw
	}
	return raw[:keyDisplayLen]
}

// ParseTTL parses a key lifetime ("30d", "90d", "never", or any "<n>d") and
// checks it against the allowed list. 0 means the key never expires.
func ParseTTL(s string, allowed []string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	ok := false
	for _, a := range allowed {
		if strings.TrimSpace(strings.ToLower(a)) == s {
			ok = true
			break
		}
	}
	if !ok {
		return 0, fmt.Errorf("ttl %q not allowed (allowed: %s)", s, strings.Join(allowed, ", "))
	}
	if s == "never" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
	if !strings.HasSuffix(s, "d") || err != nil || n <= 0 {
		return 0, fmt.Errorf("ttl %q: use <days>d (e.g. 30d) or never", s)
	}
	return time.Duration(n) * 24 * time.Hour, nil
}

// KeyUsable reports whether k may authenticate at now: not revoked, not expired.
// (Whether its service account is active is checked separately.)
func KeyUsable(k *artifactav1.ApiKey, now time.Time) bool {
	if k.GetRevokedAt() != nil {
		return false
	}
	if e := k.GetExpiresAt(); e != nil && !e.AsTime().After(now) {
		return false
	}
	return true
}

// KeyStatus is a key's display status: revoked, expired, stale (active but
// unused for StaleAfter — a never-used key counts from creation) or active.
func KeyStatus(k *artifactav1.ApiKey, now time.Time) string {
	switch {
	case k.GetRevokedAt() != nil:
		return "revoked"
	case k.GetExpiresAt() != nil && !k.GetExpiresAt().AsTime().After(now):
		return "expired"
	}
	since := k.GetCreatedAt().AsTime()
	if k.GetLastUsedAt() != nil {
		since = k.GetLastUsedAt().AsTime()
	}
	if now.Sub(since) > StaleAfter {
		return "stale"
	}
	return "active"
}

// IsAdmin decides whether who may manage service accounts: a member of any of
// adminGroups (the token's groups claim) or one of adminEmails. A service
// account is never an admin, and empty lists admit no one.
func IsAdmin(who *artifactav1.Identity, adminGroups, adminEmails []string) bool {
	if who == nil || who.GetSub() == "" || IsServiceAccount(who.GetSub()) {
		return false
	}
	for _, g := range who.GetGroups() {
		for _, a := range adminGroups {
			if a != "" && g == a {
				return true
			}
		}
	}
	if email := strings.ToLower(strings.TrimSpace(who.GetEmail())); email != "" {
		for _, a := range adminEmails {
			if strings.ToLower(strings.TrimSpace(a)) == email {
				return true
			}
		}
	}
	return false
}

// ErrSAExists is returned by a store when a service-account name is already
// taken — including by a disabled account, since names are never reused.
var ErrSAExists = errors.New("service account already exists")
