// Package e2e drives the here.now API as a black box: a real api.Server wired to
// real infra.FileStore + infra.BlobFS, served over a live httptest server, and
// exercised with genuine HTTP calls. It asserts the full FR30 flow —
// publish → view-gating → share → org-visibility → dashboard → audit — including
// the fail-closed authorization invariants (existence is never leaked) and the
// integrity of the hash-chained audit log after the flow.
package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/api"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
)

// headerAuth is a test Auth provider that selects the caller's identity from
// request headers, so one live server can be driven as different users and as an
// anonymous caller. X-Test-Sub is the subject; absent header means anonymous and
// yields (nil, false) — the same fail-closed signal a real provider returns for
// an unauthenticated request.
type headerAuth struct{}

func (headerAuth) Identify(r *http.Request) (*artifactav1.Identity, bool) {
	sub := r.Header.Get("X-Test-Sub")
	if sub == "" {
		return nil, false
	}
	return &artifactav1.Identity{Sub: sub, Email: r.Header.Get("X-Test-Email")}, true
}

// harness bundles the live server, the HTTP client, and the on-disk data dir so
// helpers can issue requests and later inspect the persisted audit log.
type harness struct {
	ts      *httptest.Server
	client  *http.Client
	metaDir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	blob, err := infra.NewBlobFS(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("new blob fs: %v", err)
	}
	return newHarnessWithBlob(t, blob)
}

// newHarnessWithBlob wires the live server over a real FileStore (for metadata +
// the auditable hash chain) and the given blob backend, so the identical HTTP
// flow can be driven against either the filesystem or the S3-compatible blob
// store. The metadata store is always the file store so the audit log can be
// verified on disk afterwards.
func newHarnessWithBlob(t *testing.T, blob api.Blob) *harness {
	t.Helper()
	metaDir := filepath.Join(t.TempDir(), "meta")
	store, err := infra.NewFileStore(metaDir)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	srv := &api.Server{Store: store, Blob: blob, Auth: headerAuth{}, BaseURL: "https://here.now"}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return &harness{ts: ts, client: ts.Client(), metaDir: metaDir}
}

// do issues an HTTP request against the live server. A non-empty sub authenticates
// as that user (via the test header); an empty sub is an anonymous caller. It
// returns the status code and the fully-read body, and never leaves the response
// body open.
func (h *harness) do(t *testing.T, method, path, sub, contentType, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	if sub != "" {
		req.Header.Set("X-Test-Sub", sub)
		req.Header.Set("X-Test-Email", sub+"@corp.example")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s %s: %v", method, path, err)
	}
	return resp.StatusCode, string(b)
}

const (
	alice = "user:alice"
	bob   = "user:bob"
	carol = "user:carol"
)

func TestEndToEndPublishViewShareAuditFlow(t *testing.T) {
	h := newHarness(t)

	// --- 0. Exempt paths: reachable without auth. --------------------------
	if code, _ := h.do(t, http.MethodGet, "/health", "", "", ""); code != http.StatusOK {
		t.Fatalf("GET /health (anon): got %d, want 200", code)
	}

	// --- 1. Publish. -------------------------------------------------------
	// A plain-HTML document with no inline module script passes through the
	// render bundler unchanged, so the raw bytes round-trip exactly.
	const payload = `<!doctype html><html><body><h1>Report</h1><p>alpha-marker-42</p></body></html>`

	code, body := h.do(t, http.MethodPost, "/artifacts?title=Report", alice, "text/html; charset=utf-8", payload)
	if code != http.StatusCreated {
		t.Fatalf("publish as alice: got %d, want 201 (body %q)", code, body)
	}
	var pub struct {
		Slug string `json:"slug"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal([]byte(body), &pub); err != nil {
		t.Fatalf("decode publish response: %v (body %q)", err, body)
	}
	if pub.Slug == "" {
		t.Fatal("publish returned empty slug")
	}
	if want := "https://here.now/a/" + pub.Slug; pub.URL != want {
		t.Fatalf("publish url: got %q, want %q", pub.URL, want)
	}
	slug := pub.Slug
	rawPath := "/a/" + slug + "/raw"

	// Anonymous publish is rejected — fails closed with 401.
	if code, _ := h.do(t, http.MethodPost, "/artifacts?title=Nope", "", "text/html; charset=utf-8", payload); code != http.StatusUnauthorized {
		t.Fatalf("anonymous publish: got %d, want 401", code)
	}

	// --- 2. View gating on /a/{slug}/raw (artifact is PRIVATE). ------------
	// Owner sees the bytes.
	code, raw := h.do(t, http.MethodGet, rawPath, alice, "", "")
	if code != http.StatusOK {
		t.Fatalf("raw as owner alice: got %d, want 200", code)
	}
	if raw != payload {
		t.Fatalf("raw body as owner: got %q, want %q", raw, payload)
	}
	// Anonymous caller: 404 — existence is not leaked.
	if code, _ := h.do(t, http.MethodGet, rawPath, "", "", ""); code != http.StatusNotFound {
		t.Fatalf("raw as anonymous: got %d, want 404", code)
	}
	// Non-owner while PRIVATE: 404 — indistinguishable from missing.
	if code, _ := h.do(t, http.MethodGet, rawPath, bob, "", ""); code != http.StatusNotFound {
		t.Fatalf("raw as non-owner bob (private): got %d, want 404", code)
	}

	// --- 3. Share: owner-only mutations, then grant bob access. ------------
	// A non-owner must not be able to change visibility or add grants — both
	// fail closed with 404 (never distinguishing missing from not-yours).
	if code, _ := h.do(t, http.MethodPatch, "/artifacts/"+slug+"/visibility", bob, "application/json", `{"visibility":"org"}`); code != http.StatusNotFound {
		t.Fatalf("non-owner bob set-visibility: got %d, want 404", code)
	}
	if code, _ := h.do(t, http.MethodPost, "/artifacts/"+slug+"/grants", bob, "application/json", `{"grantee_sub":"`+carol+`"}`); code != http.StatusNotFound {
		t.Fatalf("non-owner bob add-grant: got %d, want 404", code)
	}
	// bob still cannot view — the failed mutations changed nothing.
	if code, _ := h.do(t, http.MethodGet, rawPath, bob, "", ""); code != http.StatusNotFound {
		t.Fatalf("raw as bob after failed non-owner mutations: got %d, want 404", code)
	}

	// Owner switches to invited and grants bob.
	if code, resp := h.do(t, http.MethodPatch, "/artifacts/"+slug+"/visibility", alice, "application/json", `{"visibility":"invited"}`); code != http.StatusOK {
		t.Fatalf("owner set-visibility invited: got %d, want 200 (body %q)", code, resp)
	}
	if code, resp := h.do(t, http.MethodPost, "/artifacts/"+slug+"/grants", alice, "application/json", `{"grantee_sub":"`+bob+`"}`); code != http.StatusCreated {
		t.Fatalf("owner add-grant bob: got %d, want 201 (body %q)", code, resp)
	}
	// Now bob can view the bytes.
	code, raw = h.do(t, http.MethodGet, rawPath, bob, "", "")
	if code != http.StatusOK {
		t.Fatalf("raw as granted bob: got %d, want 200", code)
	}
	if raw != payload {
		t.Fatalf("raw body as granted bob: got %q, want %q", raw, payload)
	}
	// carol (neither owner nor grantee) is still denied while invited-only.
	if code, _ := h.do(t, http.MethodGet, rawPath, carol, "", ""); code != http.StatusNotFound {
		t.Fatalf("raw as ungranted carol (invited): got %d, want 404", code)
	}

	// --- 4. Org visibility: any authenticated user may view. ---------------
	if code, resp := h.do(t, http.MethodPatch, "/artifacts/"+slug+"/visibility", alice, "application/json", `{"visibility":"org"}`); code != http.StatusOK {
		t.Fatalf("owner set-visibility org: got %d, want 200 (body %q)", code, resp)
	}
	code, raw = h.do(t, http.MethodGet, rawPath, carol, "", "")
	if code != http.StatusOK {
		t.Fatalf("raw as carol (org): got %d, want 200", code)
	}
	if raw != payload {
		t.Fatalf("raw body as carol (org): got %q, want %q", raw, payload)
	}

	// --- 5. Dashboard: authed shows the artifact under Mine; anon gets sign-in.
	code, dash := h.do(t, http.MethodGet, "/", alice, "", "")
	if code != http.StatusOK {
		t.Fatalf("dashboard as alice: got %d, want 200", code)
	}
	if !strings.Contains(dash, `id="board"`) {
		t.Fatalf("dashboard as alice did not render (no board): %s", dash)
	}
	// The published artifact is in the data island the controller renders from,
	// tagged as the caller's own (type:"mine").
	if !strings.Contains(dash, `slug:"`+slug+`"`) || !strings.Contains(dash, `title:"Report"`) {
		t.Fatalf("dashboard as alice missing published artifact %q: %s", slug, dash)
	}

	code, landing := h.do(t, http.MethodGet, "/", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("root as anonymous: got %d, want 200", code)
	}
	// The sign-in page's stable contract: branded, with a sign-in affordance
	// pointing at /login. Assert that, not the marketing copy (which changes).
	if !strings.Contains(landing, `href="/login"`) || !strings.Contains(landing, "ArtifactA") {
		t.Fatalf("anonymous root is not the sign-in page: %s", landing)
	}
	if strings.Contains(landing, "__ARTIFACTS__") || strings.Contains(landing, `id="board"`) {
		t.Fatalf("anonymous root leaked the dashboard: %s", landing)
	}

	// --- 6. Audit integrity: the hash chain verifies with a positive count.
	verified, err := infra.VerifyAuditLog(h.metaDir)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	// The flow records: publish (1), views/denies on raw (5), and shares (3).
	// Assert a positive, non-trivial count without pinning the exact total.
	if verified < 6 {
		t.Fatalf("audit chain verified %d events, want >= 6 (publish + views + shares)", verified)
	}
	t.Logf("audit chain verified: %d events", verified)
}

// TestEndToEndSearch drives GET /artifacts/search end-to-end (ADR-0024): a caller
// searches over their visible set (owned + shared-with-me + org) with text, email,
// and pagination filters, and the grant-list-leak defense holds — a grantee's email
// on an artifact the caller does not own is never matchable.
func TestEndToEndSearch(t *testing.T) {
	h := newHarness(t)

	// searchPage is the protojson (snake_case) shape of SearchArtifactsResponse.
	type searchPage struct {
		Items []struct {
			Slug       string `json:"slug"`
			Title      string `json:"title"`
			OwnerEmail string `json:"owner_email"`
		} `json:"items"`
		Page struct {
			Total    int  `json:"total"`
			HasNext  bool `json:"has_next"`
			PageSize int  `json:"page_size"`
		} `json:"page"`
	}
	search := func(sub, query string) searchPage {
		code, body := h.do(t, http.MethodGet, "/artifacts/search"+query, sub, "", "")
		if code != http.StatusOK {
			t.Fatalf("search %q as %s: got %d, want 200 (body %q)", query, sub, code, body)
		}
		var p searchPage
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatalf("decode search: %v (body %s)", err, body)
		}
		return p
	}
	hasTitle := func(p searchPage, title string) bool {
		for _, it := range p.Items {
			if it.Title == title {
				return true
			}
		}
		return false
	}

	// alice publishes three artifacts.
	publish := func(title string) string {
		code, body := h.do(t, http.MethodPost, "/artifacts?title="+title, alice, "text/html; charset=utf-8", "<p>"+title+"</p>")
		if code != http.StatusCreated {
			t.Fatalf("publish %q: got %d (body %q)", title, code, body)
		}
		var pub struct {
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal([]byte(body), &pub); err != nil {
			t.Fatalf("decode publish: %v", err)
		}
		return pub.Slug
	}
	budgetSlug := publish("Budget")
	publish("Q3Deck")
	publish("Roadmap")

	// alice shares Budget (flip to invited, then invite bob AND carol by email).
	if code, b := h.do(t, http.MethodPatch, "/artifacts/"+budgetSlug+"/visibility", alice, "application/json", `{"visibility":"invited"}`); code != http.StatusOK {
		t.Fatalf("set visibility invited: got %d (%s)", code, b)
	}
	for _, invitee := range []string{bob, carol} {
		if code, b := h.do(t, http.MethodPost, "/artifacts/"+budgetSlug+"/grants", alice, "application/json", `{"email":"`+invitee+`@corp.example"}`); code != http.StatusCreated {
			t.Fatalf("grant to %s: got %d (%s)", invitee, code, b)
		}
	}

	// bob sees Budget (shared with him by email) but not alice's private ones.
	if p := search(bob, "?q=budget"); !hasTitle(p, "Budget") || len(p.Items) != 1 {
		t.Fatalf("bob ?q=budget = %+v, want just [Budget]", p.Items)
	}
	if p := search(bob, "?q=q3"); len(p.Items) != 0 {
		t.Fatalf("bob ?q=q3 = %+v, want none (not shared with bob)", p.Items)
	}
	// bob can match Budget by its owner's email (alice published it).
	if p := search(bob, "?email=user:alice@corp.example"); !hasTitle(p, "Budget") {
		t.Fatalf("bob ?email=alice = %+v, want [Budget]", p.Items)
	}

	// CRITICAL leak defense: bob is a grantee of Budget, but does NOT own it, so he
	// must not be able to match Budget by another grantee's (carol's) email.
	if p := search(bob, "?email=user:carol@corp.example"); len(p.Items) != 0 {
		t.Fatalf("bob ?email=carol leaked a foreign share-list: %+v, want none", p.Items)
	}

	// alice (the owner) CAN match Budget by bob's grantee email.
	if p := search(alice, "?email=user:bob@corp.example"); !hasTitle(p, "Budget") {
		t.Fatalf("alice ?email=bob = %+v, want [Budget] (she owns it)", p.Items)
	}

	// Pagination over alice's three artifacts.
	p1 := search(alice, "?page=1&page_size=2")
	if p1.Page.Total != 3 || len(p1.Items) != 2 || !p1.Page.HasNext {
		t.Fatalf("alice page1 = %+v (items %d), want total3/2items/hasNext", p1.Page, len(p1.Items))
	}
	p2 := search(alice, "?page=2&page_size=2")
	if len(p2.Items) != 1 || p2.Page.HasNext {
		t.Fatalf("alice page2 = %+v (items %d), want 1 item/!hasNext", p2.Page, len(p2.Items))
	}

	// Fail closed: anonymous search is 401.
	if code, _ := h.do(t, http.MethodGet, "/artifacts/search", "", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("anon search: got %d, want 401", code)
	}
}

// TestEndToEndSearchByDescription: an artifact published with a ?description= is
// findable by a word that appears only in the description (ADR-0025) — the point
// of the field, so an agent can locate a past artifact by more than its title.
func TestEndToEndSearchByDescription(t *testing.T) {
	h := newHarness(t)

	code, body := h.do(t, http.MethodPost,
		"/artifacts?title=Meeting+Notes&description=quarterly+revenue+analysis+and+forecast",
		alice, "text/html; charset=utf-8", "<p>notes</p>")
	if code != http.StatusCreated {
		t.Fatalf("publish with description: got %d (%s)", code, body)
	}
	var pub struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal([]byte(body), &pub); err != nil {
		t.Fatalf("decode publish: %v", err)
	}

	// A word only in the description matches; the metadata echoes the description.
	code, body = h.do(t, http.MethodGet, "/artifacts/search?q=revenue", alice, "", "")
	if code != http.StatusOK || !strings.Contains(body, `"slug":"`+pub.Slug+`"`) {
		t.Fatalf("search by description word: got %d, body %s", code, body)
	}
	code, meta := h.do(t, http.MethodGet, "/artifacts/"+pub.Slug, alice, "", "")
	if code != http.StatusOK || !strings.Contains(meta, "quarterly revenue analysis and forecast") {
		t.Fatalf("metadata missing description: got %d, body %s", code, meta)
	}
}
