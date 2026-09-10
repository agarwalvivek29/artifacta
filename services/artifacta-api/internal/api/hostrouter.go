package api

import (
	"net/http"
	"strings"
)

// hostRouter maps requests addressed to an artifact subdomain
// ({slug|label}.{root}) onto the canonical /a/{slug}/... serving routes, so the
// existing CanView-gated handlers serve subdomain traffic unchanged (ADR-0017).
//
// Only the paths that make sense for a *hosted page* are accepted on a subdomain:
//
//	/            → /a/{slug}/raw        (the page itself, served at the root)
//	/raw         → /a/{slug}/raw
//	/v/{n}       → /a/{slug}/v/{n}/raw  (a specific immutable version)
//	/v/{n}/raw   → /a/{slug}/v/{n}/raw
//
// Any other path on a subdomain is a 404 — subdomains host the artifact bytes,
// not the app UI or the JSON API. A request to the apex/root domain, an IP, or
// any host that is not a single-level subdomain of root passes through to next
// unchanged (so the dashboard, auth, publish API, and health checks still work).
//
// Security: the subdomain is only an address, never a bypass. Rewritten requests
// still flow through serveVersion → domain.CanView, so a PRIVATE artifact on its
// subdomain stays a not-leaking 404 to an anonymous caller; only VISIBILITY_LINK
// (ADR-0018) admits them.
type hostRouter struct {
	root  string // e.g. "artifacta.genorim.xyz"
	store Store
	next  http.Handler
}

func (h *hostRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sub, ok := h.subdomain(r.Host)
	if !ok {
		h.next.ServeHTTP(w, r) // apex / IP / unrelated host → normal path routing
		return
	}
	slug, ok := h.resolve(sub)
	if !ok {
		http.NotFound(w, r) // unknown subdomain — don't leak which slugs/labels exist
		return
	}
	target, ok := rewritePath(r.URL.Path, slug)
	if !ok {
		http.NotFound(w, r) // subdomains serve the artifact only
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = target
	r2.URL.RawPath = "" // force Path to be re-encoded from the clean value
	h.next.ServeHTTP(w, r2)
}

// subdomain extracts the single-level subdomain label of r.Host under root. It
// strips any port, lower-cases, and requires exactly one level below root:
// "x.root" → ("x", true); "root", "a.b.root", an IP, or an unrelated host → false.
func (h *hostRouter) subdomain(host string) (string, bool) {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i] // strip port
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	suffix := "." + h.root
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	sub := strings.TrimSuffix(host, suffix)
	if sub == "" || strings.Contains(sub, ".") {
		return "", false // apex or multi-level host
	}
	return sub, true
}

// resolve turns a subdomain label into an artifact slug. It checks the slug space
// first so a custom label can never shadow an existing artifact's slug, then the
// custom-label index.
func (h *hostRouter) resolve(sub string) (string, bool) {
	if _, found, err := h.store.GetArtifact(sub); err == nil && found {
		return sub, true
	}
	if slug, found, err := h.store.SlugForLabel(sub); err == nil && found {
		return slug, true
	}
	return "", false
}

// rewritePath maps an accepted subdomain path to its canonical /a/{slug}/... form,
// returning ok=false for any path a subdomain does not serve.
func rewritePath(path, slug string) (string, bool) {
	base := "/a/" + slug
	switch path {
	case "", "/", "/raw":
		return base + "/raw", true
	}
	if rest, ok := strings.CutPrefix(path, "/v/"); ok {
		rest = strings.TrimSuffix(rest, "/raw")
		rest = strings.TrimSuffix(rest, "/")
		if isVersionNumber(rest) {
			return base + "/v/" + rest + "/raw", true
		}
	}
	return "", false
}

// isVersionNumber reports whether s is a non-empty run of ASCII digits (a version
// segment). The serving handler still parses and range-checks it.
func isVersionNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
