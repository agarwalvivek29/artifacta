package domain

import "regexp"

// dnsLabel matches a single, lower-case DNS label (RFC 1123): 1–63 characters of
// [a-z0-9-], not starting or ending with a hyphen. Custom subdomain labels
// (ADR-0017) must satisfy this so {label}.{root-domain} is a valid hostname.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// reservedLabels are subdomain labels the platform keeps for itself (app UI,
// auth, infra, ops). A user may not claim one as a custom artifact label — doing
// so could shadow or impersonate a platform surface at {label}.{root-domain}.
var reservedLabels = map[string]struct{}{
	"www": {}, "api": {}, "app": {}, "admin": {}, "root": {},
	"login": {}, "logout": {}, "auth": {}, "callback": {}, "sso": {},
	"health": {}, "metrics": {}, "status": {}, "dashboard": {},
	"static": {}, "assets": {}, "cdn": {}, "docs": {}, "help": {},
	"mail": {}, "smtp": {}, "imap": {}, "ftp": {}, "ns": {}, "dns": {},
	"artifacta": {},
}

// ValidLabel reports whether s is an acceptable custom subdomain label: a valid
// DNS label (ValidLabel is case-sensitive — callers should lower-case first) that
// is not reserved for the platform. Uniqueness is enforced separately by the store.
func ValidLabel(s string) bool {
	if !dnsLabel.MatchString(s) {
		return false
	}
	if _, reserved := reservedLabels[s]; reserved {
		return false
	}
	return true
}
