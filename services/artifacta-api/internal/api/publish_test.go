package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
)

// fakeAuth is a test double for the Auth interface: it resolves a fixed identity
// when ok is true, and refuses (nil, false) when ok is false.
type fakeAuth struct {
	id *artifactav1.Identity
	ok bool
}

func (f fakeAuth) Identify(*http.Request) (*artifactav1.Identity, bool) {
	if !f.ok {
		return nil, false
	}
	return f.id, true
}

// newTestServer wires a Server over a real FileStore + BlobFS rooted at dir, so
// the test exercises the true persistence path (not a mock).
func newTestServer(t *testing.T, dir string, auth Auth) *Server {
	t.Helper()
	st, err := infra.NewFileStore(filepath.Join(dir, "meta"))
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	bl, err := infra.NewBlobFS(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("new blob fs: %v", err)
	}
	return &Server{Store: st, Blob: bl, Auth: auth, BaseURL: "https://here.now"}
}

func TestPublishAuthedStoresAndReturns201(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:tester", Email: "tester@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const payload = "<h1>hello here.now</h1>"
	req := httptest.NewRequest(http.MethodPost, "/artifacts?title=My+Page", strings.NewReader(payload))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("publish: got %d, want 201 (body %q)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Slug string `json:"slug"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Slug == "" {
		t.Fatal("response slug is empty")
	}
	if want := "https://here.now/a/" + resp.Slug; resp.URL != want {
		t.Fatalf("url: got %q, want %q", resp.URL, want)
	}

	// Metadata is actually stored, with the private default + supplied title/type.
	art, ok, err := srv.Store.GetArtifact(resp.Slug)
	if err != nil || !ok {
		t.Fatalf("GetArtifact(%q): ok=%v err=%v", resp.Slug, ok, err)
	}
	if art.GetOwnerSub() != owner.GetSub() {
		t.Fatalf("owner: got %q, want %q", art.GetOwnerSub(), owner.GetSub())
	}
	if art.GetVisibility() != artifactav1.Visibility_VISIBILITY_PRIVATE {
		t.Fatalf("visibility: got %v, want PRIVATE", art.GetVisibility())
	}
	if art.GetTitle() != "My Page" {
		t.Fatalf("title: got %q, want %q", art.GetTitle(), "My Page")
	}

	// The blob bytes round-trip: GET raw as the owner returns the payload.
	rawReq := httptest.NewRequest(http.MethodGet, "/a/"+resp.Slug+"/raw", nil)
	rawRR := httptest.NewRecorder()
	h.ServeHTTP(rawRR, rawReq)
	if rawRR.Code != http.StatusOK {
		t.Fatalf("raw: got %d, want 200", rawRR.Code)
	}
	if got := rawRR.Body.String(); got != payload {
		t.Fatalf("raw body: got %q, want %q", got, payload)
	}

	// A PUBLISH audit event was appended.
	logBytes, err := os.ReadFile(filepath.Join(dir, "meta", "audit.log"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if !strings.Contains(string(logBytes), "AUDIT_ACTION_PUBLISH") {
		t.Fatalf("audit log missing PUBLISH event: %s", logBytes)
	}
}

func TestPublishUnauthenticatedReturns401AndStoresNothing(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t, dir, fakeAuth{ok: false})
	h := srv.Routes()

	req := httptest.NewRequest(http.MethodPost, "/artifacts", strings.NewReader("data"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("publish: got %d, want 401", rr.Code)
	}

	// Nothing persisted: no metadata for any owner, no blob files.
	fs := srv.Store.(*infra.FileStore)
	if arts, err := fs.ListByOwner(""); err != nil || len(arts) != 0 {
		t.Fatalf("store not empty after 401: arts=%d err=%v", len(arts), err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("read blobs dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("blob written after 401: %d entries", len(entries))
	}
}

func TestPublishOverLimitBodyRejected(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:tester", Email: "tester@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})

	// Wrap the real publish handler with a tiny maxBytes ceiling to prove the
	// guard end-to-end (25 MiB is impractical to stream in a unit test).
	const limit = 16
	h := maxBytes(http.HandlerFunc(srv.publish), limit)

	body := strings.Repeat("x", limit+64)
	req := httptest.NewRequest(http.MethodPost, "/artifacts", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge && rr.Code != http.StatusBadRequest {
		t.Fatalf("over-limit publish: got %d, want 413 or 400", rr.Code)
	}
	// The over-limit read aborts before metadata is stored.
	fs := srv.Store.(*infra.FileStore)
	if arts, err := fs.ListByOwner(owner.GetSub()); err != nil || len(arts) != 0 {
		t.Fatalf("metadata stored despite over-limit body: arts=%d err=%v", len(arts), err)
	}
}

// publishWithVisibility posts a page with ?visibility=<vis> and returns the response.
func publishWithVisibility(t *testing.T, h http.Handler, vis string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/artifacts?title=Report&visibility="+vis, strings.NewReader("<h1>r</h1>"))
	req.Header.Set("Content-Type", "text/html")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestPublishWithInitialVisibility(t *testing.T) {
	cases := []struct {
		param string
		want  artifactav1.Visibility
		label string
	}{
		{"org", artifactav1.Visibility_VISIBILITY_ORG, "org"},
		{"link", artifactav1.Visibility_VISIBILITY_LINK, "link"},
		{"public", artifactav1.Visibility_VISIBILITY_LINK, "link"}, // alias
		{"invited", artifactav1.Visibility_VISIBILITY_INVITED, "invited"},
		{"private", artifactav1.Visibility_VISIBILITY_PRIVATE, "private"},
		{"", artifactav1.Visibility_VISIBILITY_PRIVATE, "private"}, // default unchanged
	}
	for _, tc := range cases {
		t.Run("vis="+tc.param, func(t *testing.T) {
			srv := newTestServer(t, t.TempDir(), fakeAuth{id: &artifactav1.Identity{Sub: "sa:ci"}, ok: true})
			rr := publishWithVisibility(t, srv.Routes(), tc.param)
			if rr.Code != http.StatusCreated {
				t.Fatalf("publish: %d %s", rr.Code, rr.Body.String())
			}
			var resp struct{ Slug, Visibility string }
			_ = json.Unmarshal(rr.Body.Bytes(), &resp)
			if resp.Visibility != tc.label {
				t.Fatalf("echoed visibility = %q, want %q", resp.Visibility, tc.label)
			}
			art, _, _ := srv.Store.GetArtifact(resp.Slug)
			if art.GetVisibility() != tc.want {
				t.Fatalf("stored visibility = %v, want %v", art.GetVisibility(), tc.want)
			}
			// Creating it exposed is audited as a SHARE, like a later PATCH would be.
			evs, _ := srv.Store.AuditEvents()
			shared := false
			for _, ev := range evs {
				if ev.GetSlug() == resp.Slug && ev.GetAction() == artifactav1.AuditAction_AUDIT_ACTION_SHARE {
					shared = true
				}
			}
			if wantShare := tc.want != artifactav1.Visibility_VISIBILITY_PRIVATE; shared != wantShare {
				t.Fatalf("SHARE audited = %v, want %v", shared, wantShare)
			}
		})
	}
}

func TestPublishRejectsUnknownVisibilityAndStoresNothing(t *testing.T) {
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: &artifactav1.Identity{Sub: "local:tester"}, ok: true})
	rr := publishWithVisibility(t, srv.Routes(), "everyone")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("publish: got %d, want 400", rr.Code)
	}
	if arts, _ := srv.Store.ListByOwner("local:tester"); len(arts) != 0 {
		t.Fatalf("a rejected publish stored %d artifacts", len(arts))
	}
}
