// Package api is the HTTP layer: viewer serving + authorization-gated artifact
// bytes. Security invariants enforced here: identity comes from the auth
// Provider; the per-artifact decision is domain.CanView; the bundle is fetched
// ONLY AFTER an allow; there are no client-reachable pre-signed URLs; and every
// decision (allow and deny) is written to the inbuilt audit trail. Fails closed.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/render"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/web"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Consumer-side interfaces (Go convention). infra.FileStore / infra.BlobFS /
// api.Local satisfy these.
type Store interface {
	GetArtifact(slug string) (*artifactav1.Artifact, bool, error)
	PutArtifact(a *artifactav1.Artifact) error
	Grants(slug string) ([]*artifactav1.Grant, error)
	AddGrant(g *artifactav1.Grant) error
	Append(ev *artifactav1.AuditEvent) error
	// Versioning (ADR-0013): immutable versions per artifact.
	AddVersion(v *artifactav1.ArtifactVersion) error
	Versions(slug string) ([]*artifactav1.ArtifactVersion, error)
	GetVersion(slug string, n int32) (*artifactav1.ArtifactVersion, bool, error)
	// Comments (ADR-0014): view-gated, version-pinned review feedback.
	AddComment(c *artifactav1.Comment) error
	Comments(slug string) ([]*artifactav1.Comment, error)
	ResolveComment(slug, id string) (bool, error)
	// Dashboard listings (FR18): Mine, Shared with me, and Org.
	ListByOwner(sub string) ([]*artifactav1.Artifact, error)
	ListByGrantee(sub string) ([]*artifactav1.Artifact, error)
	ListByVisibility(v artifactav1.Visibility) ([]*artifactav1.Artifact, error)
	// Custom subdomain labels (ADR-0017): claim a globally-unique label for a
	// slug (false = already taken / no such artifact) and resolve label → slug.
	SetLabel(slug, label string) (bool, error)
	SlugForLabel(label string) (string, bool, error)
}

type Blob interface {
	Get(slug string, n int32) (io.ReadCloser, error)
	Put(slug string, n int32, r io.Reader) error
}

type Auth interface {
	Identify(r *http.Request) (*artifactav1.Identity, bool)
}

type Server struct {
	Store Store
	Blob  Blob
	Auth  Auth
	// BaseURL is the public origin (e.g. https://here.now) used to build the
	// absolute artifact link returned by the publish API.
	BaseURL string
	// RootDomain, when non-empty (e.g. "artifacta.genorim.xyz"), enables subdomain
	// hosting (ADR-0017): {slug|label}.{RootDomain} serves the artifact's bytes at
	// its root. It also lets the API report each artifact's subdomain URL. Empty
	// disables subdomain routing; path-based /a/{slug} is unaffected either way.
	RootDomain string
	// OIDC, when non-nil, provides browser SSO (ADR-0007): its Login/Callback
	// handlers replace the dev cookie-setter and it is the request authenticator.
	// When nil, the server uses the dev /login helper and the Local adapter.
	OIDC *OIDCProvider
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// Exempt paths — explicitly allowlisted, never unprotected by default.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# metrics: not yet implemented\n"))
	})

	// Root (FR18, FR19): the dashboard when signed in, else the sign-in landing.
	// {$} matches only the exact "/" path, so it never shadows the routes below.
	mux.HandleFunc("GET /{$}", s.dashboard)

	// Auth routes: real OIDC SSO when configured, else the dev cookie-setter.
	if s.OIDC != nil {
		mux.HandleFunc("GET /login", s.OIDC.Login)
		mux.HandleFunc("GET /callback", s.OIDC.Callback)
	} else {
		mux.HandleFunc("GET /login", s.login) // dev helper: sets the session cookie
	}
	mux.HandleFunc("GET /logout", s.logout) // clears the local session cookie

	// Content routes are rate-limited per client IP (120 req/min) to blunt
	// scraping and brute-force enumeration; /health and /metrics stay unwrapped
	// so probes never trip the limiter. Auth, CanView, audit, and response
	// headers all remain enforced inside the wrapped handlers.
	const contentPerMinute = 120
	mux.Handle("GET /a/{slug}", rateLimit(http.HandlerFunc(s.viewer), contentPerMinute)) // viewer shell (no content)
	// Bundle bytes, authz-gated. /raw serves the latest version; /v/{n}/raw serves
	// a specific version. Both pass the identical CanView gate (ADR-0013).
	mux.Handle("GET /a/{slug}/raw", rateLimit(http.HandlerFunc(s.rawLatest), contentPerMinute))
	mux.Handle("GET /a/{slug}/v/{n}/raw", rateLimit(http.HandlerFunc(s.rawVersion), contentPerMinute))

	// Publish (FR1): authenticated upload. The body is capped at 25 MiB by the
	// maxBytes transport guard (finally wiring the previously-defined ceiling)
	// and rate-limited per client IP like the other content routes.
	const maxPublishBytes = 25 << 20 // 25 MiB
	mux.Handle("POST /artifacts", rateLimit(maxBytes(http.HandlerFunc(s.publish), maxPublishBytes), contentPerMinute))
	// Versioned update (ADR-0013): owner-only append of a new immutable version.
	mux.Handle("POST /artifacts/{slug}/versions", rateLimit(maxBytes(http.HandlerFunc(s.addVersion), maxPublishBytes), contentPerMinute))
	// Metadata (ADR-0013): authorized read of container + version list.
	mux.Handle("GET /artifacts/{slug}", rateLimit(http.HandlerFunc(s.metadata), contentPerMinute))

	// Sharing (FR12, FR13): owner-only mutations. Both fail closed — an
	// unauthenticated caller gets 401 and a non-owner (or unknown slug) gets a
	// 404 that never distinguishes "missing" from "not yours".
	mux.Handle("POST /artifacts/{slug}/grants", rateLimit(http.HandlerFunc(s.addGrant), contentPerMinute))
	mux.Handle("PATCH /artifacts/{slug}/visibility", rateLimit(http.HandlerFunc(s.setVisibility), contentPerMinute))
	// Custom subdomain label (ADR-0017): owner-only claim of a {label}.{root} host.
	mux.Handle("PATCH /artifacts/{slug}/label", rateLimit(http.HandlerFunc(s.setLabel), contentPerMinute))

	// Comments (ADR-0014): view-gated create/list (anyone who CanView), owner-only
	// resolve. Not audited — comments are collaboration content, not access events.
	mux.Handle("POST /artifacts/{slug}/comments", rateLimit(http.HandlerFunc(s.addComment), contentPerMinute))
	mux.Handle("GET /artifacts/{slug}/comments", rateLimit(http.HandlerFunc(s.listComments), contentPerMinute))
	mux.Handle("POST /artifacts/{slug}/comments/{id}/resolve", rateLimit(http.HandlerFunc(s.resolveComment), contentPerMinute))

	// Subdomain hosting (ADR-0017): when RootDomain is configured, wrap the mux so
	// a request to {slug|label}.{RootDomain} is rewritten to the canonical
	// /a/{slug}/raw serving path (which keeps the CanView gate, audit, and CSP).
	// When RootDomain is empty this is a no-op and only path routing is active.
	if s.RootDomain != "" {
		return &hostRouter{root: s.RootDomain, store: s.Store, next: mux}
	}
	return mux
}

// login is a v0 convenience for the local single-user flow: it drops the token
// into an hn_session cookie so a browser can authenticate. Real deploys replace
// this with the OIDC login handler.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: "hn_session", Value: r.URL.Query().Get("token"), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/health", http.StatusFound)
}

// dashboard (FR18, FR19) is the root landing. An unauthenticated caller gets the
// sign-in page at HTTP 200 (never an error) so the front door always renders. An
// authenticated caller sees three sections: Mine (owned), Shared with me
// (grant-based), and Org (org-visible artifacts owned by others). The caller's
// own artifacts are filtered out of Org so they never appear twice.
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	who, ok := s.Auth.Identify(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !ok {
		if err := web.RenderSignin(w); err != nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
		}
		return
	}
	sub := who.GetSub()

	mine, err := s.Store.ListByOwner(sub)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	shared, err := s.Store.ListByGrantee(sub)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	org, err := s.Store.ListByVisibility(artifactav1.Visibility_VISIBILITY_ORG)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	data := web.DashboardData{
		Email:  who.GetEmail(),
		Mine:   toViews(mine),
		Shared: toViews(shared),
	}
	// Org lists org-visible artifacts owned by someone else — the caller's own
	// org artifacts already appear under Mine.
	for _, a := range org {
		if a.GetOwnerSub() == sub {
			continue
		}
		data.Org = append(data.Org, toView(a))
	}

	if err := web.RenderDashboard(w, data); err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}
}

// toViews projects a slice of artifacts into the dashboard presentation model.
func toViews(arts []*artifactav1.Artifact) []web.ArtifactView {
	out := make([]web.ArtifactView, 0, len(arts))
	for _, a := range arts {
		out = append(out, toView(a))
	}
	return out
}

func toView(a *artifactav1.Artifact) web.ArtifactView {
	return web.ArtifactView{
		Slug:       a.GetSlug(),
		Title:      a.GetTitle(),
		Visibility: visibilityLabel(a.GetVisibility()),
		Version:    a.GetLatestVersion(),
	}
}

// visibilityLabel maps the Visibility enum to the lower-case wire label shown in
// the dashboard (the inverse of parseVisibility).
func visibilityLabel(v artifactav1.Visibility) string {
	switch v {
	case artifactav1.Visibility_VISIBILITY_PRIVATE:
		return "private"
	case artifactav1.Visibility_VISIBILITY_INVITED:
		return "invited"
	case artifactav1.Visibility_VISIBILITY_ORG:
		return "org"
	case artifactav1.Visibility_VISIBILITY_LINK:
		return "link"
	default:
		return "unknown"
	}
}

// logout clears the local hn_session cookie and returns to the root, which then
// renders the sign-in page. NOTE: this ends the ArtifactA session only; the OIDC
// provider's own SSO session persists, so the next sign-in may not re-prompt.
// Full single-logout (redirect to the IdP end-session endpoint) is a follow-up.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: "hn_session", Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) viewer(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy",
		// The srcdoc'd artifact inherits this policy. Egress is allowed (the
		// deployment is private on ingress, not egress): artifacts may pull
		// fonts/styles/scripts/data from https hosts. connect-src 'self' covers
		// the shell's own fetch of /a/{slug}/raw. frame-src 'self' keeps the
		// srcdoc iframe. The sandbox (no allow-same-origin) still isolates the
		// artifact from the app origin and its cookies.
		"default-src 'none'; connect-src 'self' https:; frame-src 'self'; "+
			"img-src https: data:; font-src https: data:; "+
			"style-src 'unsafe-inline' https:; script-src 'unsafe-inline' https:")
	_, _ = w.Write([]byte(web.ViewerHTML))
}

// rawLatest serves the latest version of an artifact's bundle.
func (s *Server) rawLatest(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	who, _ := s.Auth.Identify(r)

	// Resolve the artifact first so we know which version is latest. The full
	// authorization gate runs inside serveVersion; here we only need the number.
	art, ok, err := s.Store.GetArtifact(slug)
	if err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !ok {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_VIEW, false)
		http.NotFound(w, r) // don't leak existence
		return
	}
	s.serveVersion(w, r, slug, art.GetLatestVersion())
}

// rawVersion serves a specific version {n} of an artifact's bundle. A malformed
// version number is a 404 (never leaks whether the artifact exists).
func (s *Server) rawVersion(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	n, err := parseVersion(r.PathValue("n"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.serveVersion(w, r, slug, n)
}

// serveVersion is the shared authz-gated serving path for both /raw and
// /v/{n}/raw. It runs the identical gate — GetArtifact → Grants → CanView, fail
// closed with a not-leaking 404 — then, only after an allow, fetches version n's
// blob, sets the strict CSP + nosniff headers, streams the bytes, and audits a
// VIEW unless the caller passed ?preview=1.
func (s *Server) serveVersion(w http.ResponseWriter, r *http.Request, slug string, n int32) {
	who, _ := s.Auth.Identify(r)

	art, ok, err := s.Store.GetArtifact(slug)
	if err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !ok {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_VIEW, false)
		http.NotFound(w, r) // don't leak existence
		return
	}
	grants, err := s.Store.Grants(slug)
	if err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !domain.CanView(art, who, grants) {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.NotFound(w, r) // 404-as-not-found: forbidden and missing look identical
		return
	}

	// Invariant: the blob is fetched ONLY after CanView allows above. A request
	// for a version that does not exist is a not-leaking 404.
	rc, err := s.Blob.Get(slug, n)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	defer rc.Close()
	// Dashboard thumbnails fetch with ?preview=1: still CanView-gated, but not
	// logged as a VIEW so a glance at the grid doesn't flood the audit trail.
	// A deliberate open (no preview flag) is audited as a real view.
	if r.URL.Query().Get("preview") != "1" {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_VIEW, true)
	}

	// Prefer the served version's own content type; fall back to the artifact's.
	ct := art.GetContentType()
	if v, ok, err := s.Store.GetVersion(slug, n); err == nil && ok && v.GetContentType() != "" {
		ct = v.GetContentType()
	}
	if ct == "" {
		ct = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Security-Policy",
		// Egress allowed (private on ingress, not egress); sandbox (no
		// allow-same-origin) keeps the artifact a null origin, isolated from the
		// app origin + cookies. Applies when /raw is opened as a document directly.
		"sandbox allow-scripts; default-src 'none'; img-src https: data:; font-src https: data:; "+
			"style-src 'unsafe-inline' https:; script-src 'unsafe-inline' https:; connect-src https:")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, rc)
}

// parseVersion parses the {n} path segment into a positive 1-based version
// number. Anything non-numeric or < 1 is rejected.
func parseVersion(raw string) (int32, error) {
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 0, fmt.Errorf("invalid version %q", raw)
	}
	return int32(v), nil
}

// publish accepts an authenticated upload and creates a private artifact. The
// body is streamed to the blob store; metadata is recorded; a PUBLISH audit
// event is written. Fails closed: an unauthenticated caller gets 401 and
// nothing is stored.
func (s *Server) publish(w http.ResponseWriter, r *http.Request) {
	who, ok := s.Auth.Identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized) // don't leak anything
		return
	}
	owner := who.GetSub()

	title := r.URL.Query().Get("title")
	if title == "" {
		title = "untitled"
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/html; charset=utf-8"
	}

	slug := domain.NewSlug()

	// Read the body up to the maxBytes ceiling. Unlike the earlier
	// stream-to-blob path, publish now buffers the payload so the render
	// pipeline can bundle inline module scripts before storage (FR15,
	// ADR-0010). Buffering the whole body is acceptable under the 25 MiB cap.
	// An over-limit body still trips the maxBytes guard on read and surfaces as
	// a MaxBytesError, which we translate to 413.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	// Render-parity pipeline: bundle inline ES modules into a self-contained
	// artifact that renders under the strict /raw CSP. Fail-soft — Bundle
	// returns the original bytes (plus warnings) rather than erroring, so a
	// publish is never blocked by a bundling problem.
	bundled, bundledCT, _, _ := render.Bundle(body, ct)
	ct = bundledCT

	// A new artifact starts at version 1 (ADR-0013).
	const firstVersion = 1
	if err := s.Blob.Put(slug, firstVersion, bytes.NewReader(bundled)); err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	now := timestamppb.Now()
	art := &artifactav1.Artifact{
		Slug:          slug,
		OwnerSub:      owner,
		Title:         title,
		Visibility:    artifactav1.Visibility_VISIBILITY_PRIVATE, // private by default
		ContentType:   ct,
		CreatedAt:     now,
		LatestVersion: firstVersion,
	}
	if err := s.Store.AddVersion(&artifactav1.ArtifactVersion{
		Slug:        slug,
		N:           firstVersion,
		ContentType: ct,
		CreatedAt:   now,
		CreatedBy:   owner,
	}); err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if err := s.Store.PutArtifact(art); err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_PUBLISH, true)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"slug": slug,
		"url":  s.BaseURL + "/a/" + slug,
	})
}

// ownedArtifact resolves the artifact at slug for an owner-only mutation. It
// enforces the two fail-closed gates shared by the sharing endpoints: the
// caller must be authenticated (else 401) and must own the artifact (else 404).
// A missing artifact and a non-owner caller are deliberately indistinguishable —
// the 404 leaks neither existence nor authorization, mirroring the raw handler.
// The returned bool reports whether the caller may proceed; when false the
// response has already been written.
func (s *Server) ownedArtifact(w http.ResponseWriter, r *http.Request, slug string) (*artifactav1.Artifact, *artifactav1.Identity, bool) {
	who, ok := s.Auth.Identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized) // don't leak anything
		return nil, nil, false
	}
	art, found, err := s.Store.GetArtifact(slug)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return nil, nil, false
	}
	if !found || art.GetOwnerSub() != who.GetSub() {
		http.NotFound(w, r) // missing and forbidden look identical
		return nil, nil, false
	}
	return art, who, true
}

// addVersion (ADR-0013) appends a new immutable version to an existing artifact.
// Owner-only via the same fail-closed gate as the sharing endpoints (401 for an
// unauthenticated caller, 404 for a non-owner or missing artifact). The new
// version number is latest+1; nothing is overwritten. The body is bundled like
// publish, stored at (slug, n), recorded as a version, and the artifact's
// latest_version + content_type are advanced. A PUBLISH audit event is written.
// Responds 201 with {slug, version, url}.
func (s *Server) addVersion(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	art, who, ok := s.ownedArtifact(w, r, slug)
	if !ok {
		return
	}

	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/html; charset=utf-8"
	}
	note := r.URL.Query().Get("note")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	// Render-parity bundling, identical to publish (fail-soft).
	bundled, bundledCT, _, _ := render.Bundle(body, ct)
	ct = bundledCT

	n := art.GetLatestVersion() + 1
	if err := s.Blob.Put(slug, n, bytes.NewReader(bundled)); err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	now := timestamppb.Now()
	if err := s.Store.AddVersion(&artifactav1.ArtifactVersion{
		Slug:        slug,
		N:           n,
		ContentType: ct,
		CreatedAt:   now,
		CreatedBy:   who.GetSub(),
		Note:        note,
	}); err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	art.LatestVersion = n
	art.ContentType = ct
	if err := s.Store.PutArtifact(art); err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_PUBLISH, true)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"slug":    slug,
		"version": n,
		"url":     s.BaseURL + "/a/" + slug,
	})
}

// metadata (ADR-0013) returns the artifact container plus its version list to any
// caller authorized to view it. It runs the identical CanView gate as serving:
// an unauthorized or unknown artifact is a not-leaking 404. is_owner is true when
// the caller is the artifact's owner. Powers the viewer version switcher and the
// owner Share panel.
func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	who, _ := s.Auth.Identify(r)

	art, ok, err := s.Store.GetArtifact(slug)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r) // don't leak existence
		return
	}
	grants, err := s.Store.Grants(slug)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !domain.CanView(art, who, grants) {
		http.NotFound(w, r) // forbidden and missing look identical
		return
	}

	versions, err := s.Store.Versions(slug)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	type versionView struct {
		N         int32  `json:"n"`
		CreatedAt string `json:"created_at,omitempty"`
		CreatedBy string `json:"created_by,omitempty"`
		Note      string `json:"note,omitempty"`
	}
	vs := make([]versionView, 0, len(versions))
	for _, v := range versions {
		vv := versionView{N: v.GetN(), CreatedBy: v.GetCreatedBy(), Note: v.GetNote()}
		if v.GetCreatedAt() != nil {
			vv.CreatedAt = v.GetCreatedAt().AsTime().UTC().Format(time.RFC3339)
		}
		vs = append(vs, vv)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"slug":           art.GetSlug(),
		"title":          art.GetTitle(),
		"visibility":     visibilityLabel(art.GetVisibility()),
		"latest_version": art.GetLatestVersion(),
		"is_owner":       who != nil && who.GetSub() == art.GetOwnerSub(),
		"versions":       vs,
		// Subdomain hosting (ADR-0017): the custom label (if any) and the absolute
		// subdomain URL. subdomain_url is "" when RootDomain is not configured.
		"label":         art.GetLabel(),
		"subdomain_url": s.subdomainURL(subdomainHost(art)),
	})
}

// addGrant (FR12) grants a subject access to an artifact. Owner-only. The
// grantee subject comes from the JSON body {"grantee_sub":"..."} or a ?grantee=
// query param. A SHARE audit event is written on success. Returns 201.
func (s *Server) addGrant(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_, who, ok := s.ownedArtifact(w, r, slug)
	if !ok {
		return
	}

	grantee := r.URL.Query().Get("grantee")
	if grantee == "" {
		var body struct {
			GranteeSub string `json:"grantee_sub"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			grantee = body.GranteeSub
		}
	}
	if grantee == "" {
		http.Error(w, "missing grantee", http.StatusBadRequest)
		return
	}

	g := &artifactav1.Grant{
		Slug:       slug,
		GranteeSub: grantee,
		GrantedBy:  who.GetSub(),
		CreatedAt:  timestamppb.Now(),
	}
	if err := s.Store.AddGrant(g); err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_SHARE, true)
	w.WriteHeader(http.StatusCreated)
}

// setVisibility (FR13) changes an artifact's visibility. Owner-only. The body is
// {"visibility":"private|invited|org"}; an unknown value is rejected with 400. A
// SHARE audit event is written on success. Returns 200.
func (s *Server) setVisibility(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	art, who, ok := s.ownedArtifact(w, r, slug)
	if !ok {
		return
	}

	var body struct {
		Visibility string `json:"visibility"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	vis, ok := parseVisibility(body.Visibility)
	if !ok {
		http.Error(w, "unknown visibility", http.StatusBadRequest)
		return
	}

	art.Visibility = vis
	if err := s.Store.PutArtifact(art); err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_SHARE, true)
	w.WriteHeader(http.StatusOK)
}

// setLabel (ADR-0017) claims a custom subdomain label for an artifact, making it
// reachable at {label}.{root-domain}. Owner-only via the same fail-closed gate as
// the other mutations (401 unauthenticated, 404 non-owner/missing). The body is
// {"label":"my-app"}: it is lower-cased, must be a valid non-reserved DNS label
// (else 400), and must be globally unique (else 409). On success the artifact's
// label is updated and a SHARE audit event is written. Returns 200 with the URL.
func (s *Server) setLabel(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_, who, ok := s.ownedArtifact(w, r, slug)
	if !ok {
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Validate the raw input: ValidLabel is the single source of truth (it rejects
	// upper-case and other non-DNS-safe input) rather than silently normalizing,
	// which would let "MyApp" quietly re-label an artifact under "myapp".
	label := strings.TrimSpace(body.Label)
	if !domain.ValidLabel(label) {
		http.Error(w, "invalid label", http.StatusBadRequest)
		return
	}
	claimed, err := s.Store.SetLabel(slug, label)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !claimed {
		http.Error(w, "label taken", http.StatusConflict)
		return
	}
	s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_SHARE, true)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"slug":          slug,
		"label":         label,
		"subdomain_url": s.subdomainURL(label),
	})
}

// subdomainURL returns the absolute URL at which a subdomain host (a slug or a
// custom label) serves an artifact, or "" when subdomain hosting is disabled
// (RootDomain empty) or sub is empty. The scheme mirrors BaseURL — https in real
// deploys, http only when BaseURL is explicitly http (local dev).
func (s *Server) subdomainURL(sub string) string {
	if s.RootDomain == "" || sub == "" {
		return ""
	}
	scheme := "https"
	if strings.HasPrefix(strings.ToLower(s.BaseURL), "http://") {
		scheme = "http"
	}
	return scheme + "://" + sub + "." + s.RootDomain
}

// subdomainHost returns the preferred subdomain host for an artifact: its custom
// label when set, otherwise its slug (which is always a valid DNS label).
func subdomainHost(a *artifactav1.Artifact) string {
	if a.GetLabel() != "" {
		return a.GetLabel()
	}
	return a.GetSlug()
}

// parseVisibility maps the wire strings accepted by the set-visibility endpoint
// to the Visibility enum. The unspecified/zero value is never a valid target, so
// an unrecognized string returns ok=false and the caller rejects it with 400.
func parseVisibility(s string) (artifactav1.Visibility, bool) {
	switch s {
	case "private":
		return artifactav1.Visibility_VISIBILITY_PRIVATE, true
	case "invited":
		return artifactav1.Visibility_VISIBILITY_INVITED, true
	case "org":
		return artifactav1.Visibility_VISIBILITY_ORG, true
	case "link":
		// No-login, VPN-gated hosting (ADR-0018). Owner opts in explicitly.
		return artifactav1.Visibility_VISIBILITY_LINK, true
	default:
		return artifactav1.Visibility_VISIBILITY_UNSPECIFIED, false
	}
}

// maxCommentRunes caps a comment body so a single comment can't be used to
// exhaust storage or the viewer. Measured in runes, not bytes.
const maxCommentRunes = 4000

// Anchor caps (ADR-0015): bound the stored quote and its surrounding context so
// a client can't bloat storage through the anchor. Over-cap quotes degrade to a
// page-level note rather than a rejection.
const (
	maxQuoteRunes         = 2000
	maxAnchorContextRunes = 64
)

// clampRunes truncates s to at most n runes (rune-safe, not byte-safe).
func clampRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// rootComment returns the root comment with id on slug, and false if no such
// comment exists or it is itself a reply (ADR-0016 allows only one thread level,
// so a reply can never be a parent).
func (s *Server) rootComment(slug, id string) (*artifactav1.Comment, bool) {
	comments, err := s.Store.Comments(slug)
	if err != nil {
		return nil, false
	}
	for _, c := range comments {
		if c.GetId() == id {
			if c.GetParentId() != "" {
				return nil, false
			}
			return c, true
		}
	}
	return nil, false
}

// viewableArtifact runs the shared CanView gate for the comment read/create
// endpoints: GetArtifact → Grants → CanView, fail closed with a not-leaking 404.
// Unlike ownedArtifact it admits any authorized viewer (owner or grantee), which
// is exactly who ADR-0014 lets comment. The returned bool reports whether the
// caller may proceed; when false the response has already been written. Comment
// endpoints are deliberately NOT audited (they are collaboration, not access).
func (s *Server) viewableArtifact(w http.ResponseWriter, r *http.Request, slug string) (*artifactav1.Artifact, *artifactav1.Identity, bool) {
	who, _ := s.Auth.Identify(r)
	art, ok, err := s.Store.GetArtifact(slug)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return nil, nil, false
	}
	if !ok {
		http.NotFound(w, r) // don't leak existence
		return nil, nil, false
	}
	grants, err := s.Store.Grants(slug)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return nil, nil, false
	}
	if !domain.CanView(art, who, grants) {
		http.NotFound(w, r) // forbidden and missing look identical
		return nil, nil, false
	}
	return art, who, true
}

// commentView is the wire shape returned to clients. It deliberately omits
// author_sub (the internal subject id) and slug (implied by the path).
type anchorView struct {
	Quote  string `json:"quote"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
	Start  int32  `json:"start"`
	End    int32  `json:"end"`
}

type commentView struct {
	ID          string      `json:"id"`
	Version     int32       `json:"version"`
	AuthorEmail string      `json:"author_email"`
	CreatedAt   string      `json:"created_at,omitempty"`
	Body        string      `json:"body"`
	Resolved    bool        `json:"resolved"`
	Anchor      *anchorView `json:"anchor,omitempty"`
	ParentID    string      `json:"parent_id,omitempty"`
}

func toCommentView(c *artifactav1.Comment) commentView {
	cv := commentView{
		ID:          c.GetId(),
		Version:     c.GetVersion(),
		AuthorEmail: c.GetAuthorEmail(),
		Body:        c.GetBody(),
		Resolved:    c.GetResolved(),
		ParentID:    c.GetParentId(),
	}
	if c.GetCreatedAt() != nil {
		cv.CreatedAt = c.GetCreatedAt().AsTime().UTC().Format(time.RFC3339)
	}
	if a := c.GetAnchor(); a != nil && a.GetQuote() != "" {
		cv.Anchor = &anchorView{
			Quote:  a.GetQuote(),
			Prefix: a.GetPrefix(),
			Suffix: a.GetSuffix(),
			Start:  a.GetStart(),
			End:    a.GetEnd(),
		}
	}
	return cv
}

// addComment (ADR-0014) records a review comment. View-gated: anyone who CanView
// the artifact may comment (owner or invited grantee), so an unauthorized or
// unknown artifact is a not-leaking 404. The body is JSON {body, version}: the
// body is trimmed and must be non-empty (400) and at most maxCommentRunes (400);
// version defaults to the artifact's latest_version when omitted or 0. The author
// is taken from the session, never the client. Responds 201 with the comment.
// Deliberately NOT audited (ADR-0014).
func (s *Server) addComment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	art, who, ok := s.viewableArtifact(w, r, slug)
	if !ok {
		return
	}

	var body struct {
		Body     string `json:"body"`
		Version  int32  `json:"version"`
		ParentID string `json:"parent_id"`
		Anchor   *struct {
			Quote  string `json:"quote"`
			Prefix string `json:"prefix"`
			Suffix string `json:"suffix"`
			Start  int32  `json:"start"`
			End    int32  `json:"end"`
		} `json:"anchor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Body)
	if text == "" {
		http.Error(w, "empty comment", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(text) > maxCommentRunes {
		http.Error(w, "comment too long", http.StatusBadRequest)
		return
	}
	version := body.Version
	if version <= 0 {
		version = art.GetLatestVersion()
	}

	c := &artifactav1.Comment{
		Id:          domain.NewSlug(),
		Slug:        slug,
		Version:     version,
		AuthorSub:   who.GetSub(),
		AuthorEmail: who.GetEmail(),
		CreatedAt:   timestamppb.Now(),
		Body:        text,
		Resolved:    false,
	}

	// Replies (ADR-0016): a comment with parent_id is a reply within that thread.
	// The parent must be an existing ROOT comment on this artifact (no reply-to-a-
	// reply); the reply inherits the root's version and is never anchored.
	if pid := strings.TrimSpace(body.ParentID); pid != "" {
		parent, ok := s.rootComment(slug, pid)
		if !ok {
			http.Error(w, "unknown parent comment", http.StatusBadRequest)
			return
		}
		c.ParentId = pid
		c.Version = parent.GetVersion()
	} else if a := body.Anchor; a != nil {
		// Optional text anchor (ADR-0015), roots only: stored verbatim as display
		// metadata, never trusted for authz. A quote that is absent or over the cap
		// yields a page-level note rather than a rejected request; context is
		// bounded so a hostile client can't bloat storage through prefix/suffix.
		q := strings.TrimSpace(a.Quote)
		if q != "" && utf8.RuneCountInString(q) <= maxQuoteRunes {
			c.Anchor = &artifactav1.TextAnchor{
				Quote:  q,
				Prefix: clampRunes(a.Prefix, maxAnchorContextRunes),
				Suffix: clampRunes(a.Suffix, maxAnchorContextRunes),
				Start:  a.Start,
				End:    a.End,
			}
		}
	}
	if err := s.Store.AddComment(c); err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(toCommentView(c))
}

// listComments (ADR-0014) returns an artifact's comments, ascending by
// created_at, to any authorized viewer (not-leaking 404 otherwise). An optional
// ?version=n filters to comments made on that version.
func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, _, ok := s.viewableArtifact(w, r, slug); !ok {
		return
	}

	var filter int32 // 0 = all versions
	if raw := r.URL.Query().Get("version"); raw != "" {
		n, err := parseVersion(raw)
		if err != nil {
			http.Error(w, "bad version", http.StatusBadRequest)
			return
		}
		filter = n
	}

	comments, err := s.Store.Comments(slug)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	out := make([]commentView, 0, len(comments))
	for _, c := range comments {
		if filter != 0 && c.GetVersion() != filter {
			continue
		}
		out = append(out, toCommentView(c))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// resolveComment (ADR-0014) marks a comment resolved. Owner-only via the same
// fail-closed gate as the sharing endpoints (401 unauthenticated, 404 for a
// non-owner or missing artifact). An unknown comment id is a 404. Responds 200.
func (s *Server) resolveComment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, _, ok := s.ownedArtifact(w, r, slug); !ok {
		return
	}
	id := r.PathValue("id")
	// Resolve applies to a thread, i.e. a root comment. A reply id (or unknown id)
	// is a not-leaking 404 (ADR-0016).
	if _, ok := s.rootComment(slug, id); !ok {
		http.NotFound(w, r)
		return
	}
	found, err := s.Store.ResolveComment(slug, id)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) audit(who *artifactav1.Identity, slug string, action artifactav1.AuditAction, allowed bool) {
	sub := ""
	if who != nil {
		sub = who.GetSub()
	}
	_ = s.Store.Append(&artifactav1.AuditEvent{
		Ts: timestamppb.Now(), PrincipalSub: sub, Slug: slug, Action: action, Allowed: allowed,
	})
}
