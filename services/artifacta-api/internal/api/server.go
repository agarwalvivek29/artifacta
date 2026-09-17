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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	commonv1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/common/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/render"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/web"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Consumer-side interfaces (Go convention). infra.FileStore / infra.BlobFS /
// api.Local satisfy these.
type Store interface {
	GetArtifact(slug string) (*artifactav1.Artifact, bool, error)
	PutArtifact(a *artifactav1.Artifact) error
	Grants(slug string) ([]*artifactav1.Grant, error)
	AddGrant(g *artifactav1.Grant) error
	// RemoveGrant revokes a grant by grantee subject or email (FR13, ADR-0019).
	RemoveGrant(slug, id string) (bool, error)
	Append(ev *artifactav1.AuditEvent) error
	// AuditEvents lists the audit trail in seq order (for `audit verify`).
	AuditEvents() ([]*artifactav1.AuditEvent, error)
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
	// SearchArtifacts (ADR-0025) returns one page of the caller's visible set
	// (owned ∪ shared-with-me ∪ org, deduped) matching q, plus the total match
	// count. Visibility is enforced in the query — results are always a subset of
	// what CanView permits — and grantee emails are matched only on the viewer's
	// own artifacts, never a foreign share-list.
	SearchArtifacts(q domain.SearchQuery) (items []*artifactav1.Artifact, total int, err error)
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
	// OrgName and LogoURL let an operator brand the dashboard/viewer chrome for
	// their organization (cosmetic). Empty falls back to the ArtifactA wordmark.
	OrgName string
	LogoURL string
	// OIDC, when non-nil, provides browser SSO (ADR-0007): its Login/Callback
	// handlers replace the dev cookie-setter and it is the request authenticator.
	// When nil, the server uses the dev /login helper and the Local adapter.
	OIDC *OIDCProvider
	// Version is the deployed server's build version, exposed at GET /version so
	// the CLI's `upgrade` command can check compatibility before advising a bump.
	Version string
	// Metrics, when non-nil, backs GET /metrics with the real Prometheus registry
	// and is recorded by the Observe middleware. Nil keeps the not-implemented stub
	// so a bare Server (e.g. in tests) needs no metrics wiring.
	Metrics *Metrics
	// Egress reports whether third-party CDN egress is permitted at view time
	// (ARTIFACTA_CDN_EGRESS=allow). It drives BOTH how artifacts are rendered
	// (render.Options) AND the served Content-Security-Policy: false (the default,
	// air-gapped) tightens the CSP to forbid external hosts so the no-egress
	// posture is actually enforced; true keeps the permissive https: CSP so a
	// CDN-import artifact renders. See ADR-0023.
	Egress bool
	// UploadUI, when true, renders the dashboard "Upload an artifact" form and
	// accepts the multipart POST /artifacts branch (ADR-0024). Default false keeps
	// the surface dark; the raw-body CLI publish path is unaffected either way.
	UploadUI bool
}

// uploadAllowlist maps a lower-case file extension to the canonical Content-Type
// the browser-upload path will store. Content type is SERVER-decided from the
// extension (validated here), never taken from the client's multipart header, so
// an upload can't assert an active type that would escape the sandbox. Anything
// not in this map is rejected 415. (ADR-0024, spec 5.)
var uploadAllowlist = map[string]string{
	".html": "text/html; charset=utf-8",
	".htm":  "text/html; charset=utf-8",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
}

// contentCSP returns the Content-Security-Policy for served artifact bytes,
// applied to BOTH the viewer shell (whose srcdoc'd artifact inherits it) and the
// direct /raw document. In deny mode (default) it forbids external hosts — the
// true air-gap; in allow mode it permits https: so CDN-import artifacts load. In
// both, `sandbox allow-scripts` keeps the artifact a null origin isolated from
// the app origin and its cookies. shellExtra carries the directives only the
// viewer shell needs (frame-src for its srcdoc iframe); it is empty for /raw.
func (s *Server) contentCSP(includeSandbox bool, shellExtra string) string {
	var b strings.Builder
	if includeSandbox {
		b.WriteString("sandbox allow-scripts; ")
	}
	if s.Egress {
		// Permissive: artifacts may pull styles/scripts/fonts/images/data from
		// https hosts (deployment is private on ingress, not egress).
		b.WriteString("default-src 'none'; connect-src 'self' https:; img-src https: data:; " +
			"font-src https: data:; style-src 'unsafe-inline' https:; script-src 'unsafe-inline' https:")
	} else {
		// Air-gap: no external hosts. Self-contained artifacts (bundled JS,
		// inlined Tailwind/Mermaid, inline styles, data: images) render fully;
		// anything reaching for a CDN is blocked, enforcing the no-egress posture.
		b.WriteString("default-src 'none'; connect-src 'self'; img-src 'self' data:; " +
			"font-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	}
	if shellExtra != "" {
		b.WriteString("; ")
		b.WriteString(shellExtra)
	}
	return b.String()
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// Exempt paths — explicitly allowlisted, never unprotected by default.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if s.Metrics != nil {
		mux.Handle("GET /metrics", s.Metrics.Handler())
	} else {
		mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("# metrics: not yet implemented\n"))
		})
	}
	// CLI login discovery (exempt, like /health): lets `artifacta login <url>`
	// bootstrap the loopback PKCE flow from the URL alone — no hand-configured
	// issuer/client_id. Unwrapped (no auth, no rate limit) and registered here so
	// it resolves on the apex host before any subdomain rewrite. Exposes only
	// public values (issuer, client_id, scopes); never the client secret.
	mux.HandleFunc("GET /.well-known/artifacta-cli", s.cliLoginConfig)
	// Deployed version + compatibility (exempt, unauthenticated): lets the CLI's
	// `upgrade` command decide whether a newer CLI is compatible with this server.
	mux.HandleFunc("GET /version", s.version)

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
	// Caller's own artifacts (powers remote `artifacta ls`) and the authenticated
	// identity probe (powers `artifacta doctor` / `whoami`). Both fail closed 401.
	mux.Handle("GET /artifacts", rateLimit(http.HandlerFunc(s.listArtifacts), contentPerMinute))
	// Paginated search over the caller's visible set (ADR-0025). The literal path
	// takes precedence over GET /artifacts/{slug} in the Go 1.22 mux, so it never
	// shadows the metadata route. Fail-closed 401.
	mux.Handle("GET /artifacts/search", rateLimit(http.HandlerFunc(s.searchArtifacts), contentPerMinute))
	mux.Handle("GET /me", rateLimit(http.HandlerFunc(s.me), contentPerMinute))
	// Audit (remote `artifacta audit verify`): the caller's OWN audit rows so they
	// can verify per-row integrity client-side. Owner-scoped, fail-closed 401.
	mux.Handle("GET /audit", rateLimit(http.HandlerFunc(s.auditEvents), contentPerMinute))

	// Sharing (FR12, FR13): owner-only mutations. Both fail closed — an
	// unauthenticated caller gets 401 and a non-owner (or unknown slug) gets a
	// 404 that never distinguishes "missing" from "not yours".
	mux.Handle("POST /artifacts/{slug}/grants", rateLimit(http.HandlerFunc(s.addGrant), contentPerMinute))
	mux.Handle("DELETE /artifacts/{slug}/grants/{grantee}", rateLimit(http.HandlerFunc(s.removeGrant), contentPerMinute))
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
		Email:         who.GetEmail(),
		OrgName:       s.OrgName,
		LogoURL:       s.LogoURL,
		Mine:          toViews(mine),
		Shared:        toViews(shared),
		UploadEnabled: s.UploadUI,
	}
	// Mine artifacts are owned by the caller — mark them so the dashboard shows the
	// owner-only Share control on those cards.
	for i := range data.Mine {
		data.Mine[i].IsOwner = true
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
	created := ""
	if a.GetCreatedAt() != nil {
		created = a.GetCreatedAt().AsTime().UTC().Format("2006-01-02")
	}
	return web.ArtifactView{
		Slug:        a.GetSlug(),
		Title:       a.GetTitle(),
		Visibility:  visibilityLabel(a.GetVisibility()),
		Version:     a.GetLatestVersion(),
		Created:     created,
		ContentType: a.GetContentType(),
		Description: a.GetDescription(),
		OwnerEmail:  a.GetOwnerEmail(),
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
	// The srcdoc'd artifact INHERITS this shell CSP (a srcdoc document takes its
	// embedder's policy, not the /raw header), so this is where the air-gap
	// posture is actually enforced for the primary viewing path. frame-src 'self'
	// keeps the srcdoc/thumbnail iframes; the shell is the app UI so it is not
	// itself sandboxed. See ADR-0023.
	w.Header().Set("Content-Security-Policy", s.contentCSP(false, "frame-src 'self'"))
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
	// Applies when /raw is opened as a document directly (thumbnails, direct
	// links). sandbox (no allow-same-origin) keeps the artifact a null origin,
	// isolated from the app origin + cookies; the egress posture flag decides
	// whether external hosts are reachable. See ADR-0023.
	w.Header().Set("Content-Security-Policy", s.contentCSP(true, ""))
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

	// Browser upload branch (ADR-0024): a multipart/form-data body is the
	// dashboard upload form. The raw-body path below (CLI, Bearer) is unchanged.
	if isMultipartForm(r) {
		s.publishUpload(w, r, who)
		return
	}

	title := r.URL.Query().Get("title")
	if title == "" {
		title = "untitled"
	}
	description := r.URL.Query().Get("description") // optional publisher metadata (ADR-0025)
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/html; charset=utf-8"
	}

	slug := domain.NewSlug()

	// Read the body up to the maxBytes ceiling. publish buffers the payload so the
	// render pipeline can bundle inline module scripts before storage (FR15,
	// ADR-0010). An over-limit body trips the maxBytes guard on read and surfaces
	// as a MaxBytesError, which we translate to 413.
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

	// Render-parity pipeline (fail-soft): Markdown → self-contained HTML, inline
	// ES modules bundled, under the egress posture. Never blocks a publish.
	bundled, bundledCT, _, _ := render.Prepare(body, ct, render.Options{Egress: s.Egress})

	if err := s.storeArtifact(who, slug, title, description, bundledCT, bundled); err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"slug": slug,
		"url":  s.BaseURL + "/a/" + slug,
	})
}

// publishUpload handles the dashboard's multipart upload form (ADR-0024, spec 5).
// It is gated by the UploadUI toggle and a same-origin check, accepts a single
// file of a server-decided allowlisted type, and creates an ordinary
// private-by-default v1 artifact — identical lifecycle to a CLI publish — then
// redirects the browser to the new artifact. The raw-body CLI path is untouched.
func (s *Server) publishUpload(w http.ResponseWriter, r *http.Request, who *artifactav1.Identity) {
	if !s.UploadUI {
		http.Error(w, "upload disabled", http.StatusBadRequest)
		return
	}
	// CSRF: a cookie-authed, state-changing browser POST. The session cookie is
	// already SameSite; additionally require the Origin/Referer (when present) to
	// match this deployment's host so a cross-site form can't drive an upload.
	if !s.sameOrigin(r) {
		http.Error(w, "bad origin", http.StatusForbidden)
		return
	}

	f, hdr, err := r.FormFile("file")
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "file too large (max 25 MiB)", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "no file provided", http.StatusBadRequest)
		return
	}
	defer f.Close()

	// Content type is SERVER-decided from the extension against the allowlist,
	// never the client's part header — an upload can't assert an active type.
	ct, ok := uploadContentType(hdr.Filename)
	if !ok {
		http.Error(w, "unsupported file type — allowed: HTML, PDF, PNG, JPEG, GIF, WebP, SVG", http.StatusUnsupportedMediaType)
		return
	}

	body, err := io.ReadAll(f)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "file too large (max 25 MiB)", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = hdr.Filename
	}
	description := strings.TrimSpace(r.FormValue("description")) // optional (ADR-0025)

	slug := domain.NewSlug()
	// HTML runs the render-parity pipeline (fail-soft); non-HTML is stored as-is.
	bundled, storedCT, _, _ := render.Prepare(body, ct, render.Options{Egress: s.Egress})

	if err := s.storeArtifact(who, slug, title, description, storedCT, bundled); err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	// Full-page form POST → redirect the browser to the new artifact (PRG).
	http.Redirect(w, r, "/a/"+slug, http.StatusSeeOther)
}

// storeArtifact persists a rendered artifact as a private v1 owned by who, then
// audits PUBLISH. Shared by the CLI raw path and the browser upload path so both
// converge on one storage + audit code path. On any store error it audits DENY
// and returns the error for the caller to surface as a 500.
func (s *Server) storeArtifact(who *artifactav1.Identity, slug, title, description, ct string, bundled []byte) error {
	const firstVersion = 1
	if err := s.Blob.Put(slug, firstVersion, bytes.NewReader(bundled)); err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		return err
	}
	now := timestamppb.Now()
	art := &artifactav1.Artifact{
		Slug:          slug,
		OwnerSub:      who.GetSub(),
		OwnerEmail:    who.GetEmail(), // denormalized for search (ADR-0025); server-derived, never client-asserted
		Title:         title,
		Description:   description,                               // optional publisher metadata, searchable (ADR-0025)
		Visibility:    artifactav1.Visibility_VISIBILITY_PRIVATE, // private by default
		ContentType:   ct,
		CreatedAt:     now,
		LatestVersion: firstVersion,
	}
	if err := s.Store.AddVersion(&artifactav1.ArtifactVersion{
		Slug: slug, N: firstVersion, ContentType: ct, CreatedAt: now, CreatedBy: who.GetSub(),
	}); err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		return err
	}
	if err := s.Store.PutArtifact(art); err != nil {
		s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		return err
	}
	s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_PUBLISH, true)
	return nil
}

// isMultipartForm reports whether the request body is a browser multipart form.
func isMultipartForm(r *http.Request) bool {
	return strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data")
}

// uploadContentType maps an uploaded filename to its canonical stored Content-Type
// via the allowlist, or (,"",false) if the extension is not allowed.
func uploadContentType(filename string) (string, bool) {
	ct, ok := uploadAllowlist[strings.ToLower(filepath.Ext(filename))]
	return ct, ok
}

// sameOrigin checks the browser POST's Origin/Referer against this deployment's
// host as a CSRF backstop. A missing Origin AND Referer passes (the SameSite
// session cookie is the primary defense); a present-but-mismatched one fails.
func (s *Server) sameOrigin(r *http.Request) bool {
	expected := hostOf(s.BaseURL)
	if expected == "" {
		expected = r.Host
	}
	if o := r.Header.Get("Origin"); o != "" {
		return hostOf(o) == expected
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return hostOf(ref) == expected
	}
	return true
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// ownedArtifact resolves the artifact at slug for an owner-only mutation. It
// enforces the two fail-closed gates shared by the sharing endpoints: the
// caller must be authenticated (else 401) and must own the artifact (else 404).
// A missing artifact and a non-owner caller are deliberately indistinguishable —
// the 404 leaks neither existence nor authorization, mirroring the raw handler.
// The returned bool reports whether the caller may proceed; when false the
// cliLoginConfig backs GET /.well-known/artifacta-cli. It returns the OIDC
// discovery info the CLI needs to log in (issuer, public client_id, scopes), or
// {"auth":"local"} when the server runs the dev single-token adapter. No secret
// is ever included — the CLI is a public client and uses PKCE alone.
func (s *Server) cliLoginConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.OIDC == nil {
		_ = json.NewEncoder(w).Encode(CLIAuthConfig{Auth: "local"})
		return
	}
	_ = json.NewEncoder(w).Encode(s.OIDC.CLILoginConfig())
}

// minCLIVersion is the oldest CLI this server accepts; serverCapabilities are the
// feature flags it advertises. Both are surfaced at GET /version so a CLI can
// reason about compatibility (version floor + capability presence).
const minCLIVersion = "0.0.1"

var serverCapabilities = []string{"cli-login", "refresh-tokens", "artifact-list"}

// version backs GET /version (unauthenticated): the deployed build version plus
// the compatibility contract (oldest supported CLI + capability flags). The CLI's
// `upgrade` command reads this to decide whether a newer CLI is compatible before
// advising the user to upgrade.
func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	ver := s.Version
	if ver == "" {
		ver = "dev"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version":         ver,
		"min_cli_version": minCLIVersion,
		"capabilities":    serverCapabilities,
	})
}

// me backs GET /me: the authenticated caller's identity, or 401. It is the auth
// half of `artifacta doctor` — a 200 here proves the CLI's token is accepted.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	who, ok := s.Auth.Identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"sub":   who.GetSub(),
		"email": who.GetEmail(),
	})
}

// listArtifacts backs GET /artifacts: the caller's own artifacts (the dashboard
// "Mine" set), powering remote `artifacta ls`. Owner-scoped and fail-closed 401.
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	who, ok := s.Auth.Identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	arts, err := s.Store.ListByOwner(who.GetSub())
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(arts))
	for _, a := range arts {
		out = append(out, map[string]any{
			"slug":           a.GetSlug(),
			"title":          a.GetTitle(),
			"visibility":     visibilityLabel(a.GetVisibility()),
			"latest_version": a.GetLatestVersion(),
			"url":            s.BaseURL + "/a/" + a.GetSlug(),
			"created_at":     a.GetCreatedAt().AsTime().Format(time.RFC3339),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// searchArtifacts backs GET /artifacts/search (ADR-0025): a paginated, filtered
// view over the caller's visible set (owned ∪ shared-with-me ∪ org). Fail-closed
// 401. Visibility is enforced in the store query, so results are always a subset
// of what CanView permits; grantee-email matching is scoped to the caller's own
// artifacts, never a foreign share-list.
func (s *Server) searchArtifacts(w http.ResponseWriter, r *http.Request) {
	who, ok := s.Auth.Identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	qv := r.URL.Query()

	var vis *artifactav1.Visibility
	if v := qv.Get("visibility"); v != "" {
		parsed, valid := parseVisibility(v)
		if !valid {
			http.Error(w, "invalid visibility", http.StatusBadRequest)
			return
		}
		vis = &parsed
	}

	// sort_order defaults to descending (newest/last first) — the useful default
	// for a created_at sort. An explicit "asc" flips it.
	sortDesc := qv.Get("sort_order") != "asc"

	q := domain.SearchQuery{
		ViewerSub:   who.GetSub(),
		ViewerEmail: who.GetEmail(),
		Text:        qv.Get("q"),
		Email:       qv.Get("email"),
		Visibility:  vis,
		Page:        atoiDefault(qv.Get("page"), 0),
		PageSize:    atoiDefault(qv.Get("page_size"), 0),
		SortBy:      qv.Get("sort_by"),
		SortDesc:    sortDesc,
	}
	q.Normalize()

	arts, total, err := s.Store.SearchArtifacts(q)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}

	resp := &artifactav1.SearchArtifactsResponse{
		Items: make([]*artifactav1.ArtifactSummary, 0, len(arts)),
		Page:  pageMeta(q.Page, q.PageSize, total),
	}
	for _, a := range arts {
		resp.Items = append(resp.Items, &artifactav1.ArtifactSummary{
			Slug:          a.GetSlug(),
			Title:         a.GetTitle(),
			Visibility:    visibilityLabel(a.GetVisibility()),
			LatestVersion: a.GetLatestVersion(),
			Url:           s.BaseURL + "/a/" + a.GetSlug(),
			Label:         a.GetLabel(),
			OwnerEmail:    a.GetOwnerEmail(),
			CreatedAt:     a.GetCreatedAt(),
			Description:   a.GetDescription(),
		})
	}

	// EmitUnpopulated keeps the pagination meta (and empty items array) fully
	// present so clients get a stable shape; UseProtoNames keeps snake_case field
	// names consistent with the other JSON endpoints.
	body, err := (protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: true}).Marshal(resp)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// pageMeta builds the common.v1 pagination meta for a result set of `total` items
// windowed at (page, pageSize). total_pages is ceil(total/pageSize).
func pageMeta(page, pageSize int32, total int) *commonv1.PaginationMeta {
	var totalPages int32
	if pageSize > 0 {
		totalPages = int32((total + int(pageSize) - 1) / int(pageSize))
	}
	return &commonv1.PaginationMeta{
		Page:       page,
		PageSize:   pageSize,
		Total:      int32(total),
		TotalPages: totalPages,
		HasNext:    page < totalPages,
		HasPrev:    page > 1,
	}
}

// atoiDefault parses s as a base-10 int32, returning def when s is empty or
// invalid. Range/clamping is the caller's job (SearchQuery.Normalize).
func atoiDefault(s string, def int32) int32 {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return int32(n)
}

// auditEvents backs GET /audit: the caller's OWN audit rows — events where the
// caller is the principal, or the event is about an artifact they own — in seq
// order, each re-marshaled to protojson so the CLI can recompute the hash and
// verify per-row integrity client-side (remote `artifacta audit verify`).
//
// Owner-scoped and fail-closed 401. The instance-wide trail records who viewed
// what across every artifact, so a caller is only ever shown rows that concern
// them; verifying the full chain's continuity stays a server-host operation.
//
// v0 scope: this loads the whole trail and filters in-process. It's bounded by
// the per-IP rate limiter and fine at current scale; pushing the owner filter
// into the store with pagination is a follow-up (see TODOS.md).
func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	who, ok := s.Auth.Identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sub := who.GetSub()

	arts, err := s.Store.ListByOwner(sub)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	owned := make(map[string]bool, len(arts))
	for _, a := range arts {
		owned[a.GetSlug()] = true
	}

	events, err := s.Store.AuditEvents()
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	out := make([]json.RawMessage, 0, len(events))
	for _, ev := range events {
		if ev.GetPrincipalSub() != sub && !owned[ev.GetSlug()] {
			continue // not the caller's action, not about their artifact
		}
		b, err := protojson.Marshal(ev)
		if err != nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		out = append(out, json.RawMessage(b))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

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

	// Render-parity, identical to publish (fail-soft), under the same egress mode.
	bundled, bundledCT, _, _ := render.Prepare(body, ct, render.Options{Egress: s.Egress})
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

	isOwner := who != nil && who.GetSub() == art.GetOwnerSub()

	// Grantees power the Share panel's "people with access" list. Owner-only: the
	// list of who an artifact is shared with is itself sensitive. `id` is the
	// stable handle the revoke endpoint takes (email preferred, else subject).
	type granteeView struct {
		ID    string `json:"id"`
		Email string `json:"email,omitempty"`
		Sub   string `json:"sub,omitempty"`
	}
	var grantees []granteeView
	var ownerEmail string
	if isOwner {
		ownerEmail = who.GetEmail() // the caller is the owner here
		grantees = make([]granteeView, 0, len(grants))
		for _, g := range grants {
			id := g.GetGranteeEmail()
			if id == "" {
				id = g.GetGranteeSub()
			}
			grantees = append(grantees, granteeView{ID: id, Email: g.GetGranteeEmail(), Sub: g.GetGranteeSub()})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"slug":           art.GetSlug(),
		"title":          art.GetTitle(),
		"description":    art.GetDescription(),
		"visibility":     visibilityLabel(art.GetVisibility()),
		"latest_version": art.GetLatestVersion(),
		"is_owner":       isOwner,
		"versions":       vs,
		// Subdomain hosting (ADR-0017): the custom label (if any) and the absolute
		// subdomain URL. subdomain_url is "" when RootDomain is not configured.
		"label":         art.GetLabel(),
		"subdomain_url": s.subdomainURL(subdomainHost(art)),
		"root_domain":   s.RootDomain, // "" when subdomain hosting is disabled
		// People with access (owner-only; nil/omitted for non-owners).
		"grantees": grantees,
		// owner_email labels the owner's own row in the Share panel's people list.
		// Owner-only (the caller is the owner when isOwner) and omitted otherwise,
		// so a non-owner never learns who owns an artifact they can merely view.
		"owner_email": ownerEmail,
	})
}

// addGrant (FR12) grants a person access to an artifact. Owner-only. The grantee
// is taken from the JSON body: {"email":"..."} to invite by verified email
// (ADR-0019, the Share-UI path) or {"grantee_sub":"..."} for a known subject; a
// ?grantee= query param is still accepted as a subject. A duplicate invite is a
// no-op success. A SHARE audit event is written on success. Returns 201.
func (s *Server) addGrant(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_, who, ok := s.ownedArtifact(w, r, slug)
	if !ok {
		return
	}

	var body struct {
		Email      string `json:"email"`
		GranteeSub string `json:"grantee_sub"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	sub := strings.TrimSpace(body.GranteeSub)
	if sub == "" {
		sub = strings.TrimSpace(r.URL.Query().Get("grantee"))
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email != "" && !validEmail(email) {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}
	if sub == "" && email == "" {
		http.Error(w, "missing grantee", http.StatusBadRequest)
		return
	}

	// Idempotent: if this subject/email is already granted, succeed without a
	// duplicate row so the Share UI's "invite" is safe to retry.
	if grants, err := s.Store.Grants(slug); err == nil {
		for _, g := range grants {
			if (sub != "" && g.GetGranteeSub() == sub) ||
				(email != "" && strings.EqualFold(g.GetGranteeEmail(), email)) {
				w.WriteHeader(http.StatusCreated)
				return
			}
		}
	}

	g := &artifactav1.Grant{
		Slug:         slug,
		GranteeSub:   sub,
		GranteeEmail: email,
		GrantedBy:    who.GetSub(),
		CreatedAt:    timestamppb.Now(),
	}
	if err := s.Store.AddGrant(g); err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_SHARE, true)
	w.WriteHeader(http.StatusCreated)
}

// removeGrant (FR13) revokes a person's access. Owner-only via the same
// fail-closed gate. The {grantee} path segment is a subject or an email; an
// unknown grantee is a 404. A SHARE audit event is written on success. Returns 200.
func (s *Server) removeGrant(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_, who, ok := s.ownedArtifact(w, r, slug)
	if !ok {
		return
	}
	grantee, err := url.PathUnescape(r.PathValue("grantee"))
	if err != nil || grantee == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	found, err := s.Store.RemoveGrant(slug, grantee)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	s.audit(who, slug, artifactav1.AuditAction_AUDIT_ACTION_SHARE, true)
	w.WriteHeader(http.StatusOK)
}

// validEmail is a deliberately permissive check: a single @ with non-empty,
// dot-bearing local and domain parts and no spaces. The IdP is the real
// authority on address validity; this only rejects obvious junk.
func validEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at != strings.LastIndexByte(s, '@') || at == len(s)-1 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return strings.Contains(s[at+1:], ".")
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
		http.Error(w, `unknown visibility (want private, invited, org, or link; "public" is an alias for link)`, http.StatusBadRequest)
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
// (RootDomain empty) or sub is empty. Scheme and any non-default port are taken
// from BaseURL: https by default, http when BaseURL is http, and a non-standard
// port (e.g. :8080 in local/dev or behind a non-443 proxy) is preserved so the
// URL is actually reachable. The standard port for the scheme (80/443) is omitted.
func (s *Server) subdomainURL(sub string) string {
	if s.RootDomain == "" || sub == "" {
		return ""
	}
	scheme, port := "https", ""
	if u, err := url.Parse(s.BaseURL); err == nil {
		if u.Scheme != "" {
			scheme = u.Scheme
		}
		port = u.Port()
	}
	host := sub + "." + s.RootDomain
	isDefaultPort := (scheme == "https" && port == "443") || (scheme == "http" && port == "80")
	if port != "" && !isDefaultPort {
		host += ":" + port
	}
	return scheme + "://" + host
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
	case "link", "public":
		// No-login, VPN-gated hosting (ADR-0018). Owner opts in explicitly.
		// "public" is an accepted alias for "link": people arriving from other
		// tools reach for "public" for a no-login link. It is NOT internet-public
		// (still VPN-gated); the canonical name stays "link" everywhere it's shown.
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
