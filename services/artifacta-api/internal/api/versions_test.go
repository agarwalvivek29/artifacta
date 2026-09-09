package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

// publishAs publishes payload as the server's authenticated identity and returns
// the new artifact's slug.
func publishAs(t *testing.T, h http.Handler, payload string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/artifacts?title=Doc", strings.NewReader(payload))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("publish: got %d, want 201 (body %q)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode publish response: %v", err)
	}
	if resp.Slug == "" {
		t.Fatal("publish returned empty slug")
	}
	return resp.Slug
}

// getRaw fetches a raw path as the given subject (empty = anonymous) and returns
// the status code and body.
func getRaw(t *testing.T, h http.Handler, path, sub string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if sub != "" {
		req.Header.Set("X-Test-Sub", sub)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code, rr.Body.String()
}

// TestPublishCreatesVersionOne verifies a fresh publish records latest_version=1
// and serves that version at both /raw and /v/1/raw.
func TestPublishCreatesVersionOne(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const v1 = "<h1>version one</h1>"
	slug := publishAs(t, h, v1)

	art, ok, err := srv.Store.GetArtifact(slug)
	if err != nil || !ok {
		t.Fatalf("GetArtifact: ok=%v err=%v", ok, err)
	}
	if art.GetLatestVersion() != 1 {
		t.Fatalf("latest_version: got %d, want 1", art.GetLatestVersion())
	}

	if code, body := getRaw(t, h, "/a/"+slug+"/raw", ""); code != http.StatusOK || body != v1 {
		t.Fatalf("/raw: got (%d, %q), want (200, %q)", code, body, v1)
	}
	if code, body := getRaw(t, h, "/a/"+slug+"/v/1/raw", ""); code != http.StatusOK || body != v1 {
		t.Fatalf("/v/1/raw: got (%d, %q), want (200, %q)", code, body, v1)
	}
}

// TestAddVersionAppendsAndServes verifies the owner can append v2, that /raw
// then serves v2 while /v/1/raw still serves the original v1 bytes (immutability),
// and that unauthenticated and non-owner callers are rejected 401/404.
func TestAddVersionAppendsAndServes(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	h := srv.Routes()

	const v1 = "<h1>one</h1>"
	const v2 = "<h1>two</h1>"
	slug := publishAs(t, h, v1)

	// Append v2 as the owner.
	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/versions?note=update", strings.NewReader(v2))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add version: got %d, want 201 (body %q)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Slug    string `json:"slug"`
		Version int32  `json:"version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode add-version response: %v", err)
	}
	if resp.Version != 2 || resp.Slug != slug {
		t.Fatalf("add-version response: got %+v, want version=2 slug=%q", resp, slug)
	}

	// latest_version advanced to 2.
	art, _, _ := srv.Store.GetArtifact(slug)
	if art.GetLatestVersion() != 2 {
		t.Fatalf("latest_version after append: got %d, want 2", art.GetLatestVersion())
	}

	// /raw now serves v2; /v/2/raw serves v2; /v/1/raw still serves the immutable v1.
	if code, body := getRaw(t, h, "/a/"+slug+"/raw", ""); code != http.StatusOK || body != v2 {
		t.Fatalf("/raw after append: got (%d, %q), want (200, %q)", code, body, v2)
	}
	if code, body := getRaw(t, h, "/a/"+slug+"/v/2/raw", ""); code != http.StatusOK || body != v2 {
		t.Fatalf("/v/2/raw: got (%d, %q), want (200, %q)", code, body, v2)
	}
	if code, body := getRaw(t, h, "/a/"+slug+"/v/1/raw", ""); code != http.StatusOK || body != v1 {
		t.Fatalf("/v/1/raw (immutable): got (%d, %q), want (200, %q)", code, body, v1)
	}

	// A version that does not exist is a 404.
	if code, _ := getRaw(t, h, "/a/"+slug+"/v/9/raw", ""); code != http.StatusNotFound {
		t.Fatalf("/v/9/raw: got %d, want 404", code)
	}
}

// TestAddVersionAuthGates verifies the owner-only fail-closed gate on the
// version-append endpoint: unauthenticated → 401, non-owner → 404, and neither
// mutates state.
func TestAddVersionAuthGates(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner"}

	// Publish v1 as the owner.
	ownerSrv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	slug := publishAs(t, ownerSrv.Routes(), "<h1>one</h1>")

	// Unauthenticated append → 401 (reuse the same on-disk store).
	anonSrv := &Server{Store: ownerSrv.Store, Blob: ownerSrv.Blob, Auth: fakeAuth{ok: false}, BaseURL: "https://here.now"}
	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/versions", strings.NewReader("x"))
	rr := httptest.NewRecorder()
	anonSrv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauth append: got %d, want 401", rr.Code)
	}

	// Non-owner append → 404 (never distinguishes missing from not-yours).
	other := &artifactav1.Identity{Sub: "local:other"}
	otherSrv := &Server{Store: ownerSrv.Store, Blob: ownerSrv.Blob, Auth: fakeAuth{id: other, ok: true}, BaseURL: "https://here.now"}
	req = httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/versions", strings.NewReader("x"))
	rr = httptest.NewRecorder()
	otherSrv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("non-owner append: got %d, want 404", rr.Code)
	}

	// State unchanged: still at version 1.
	art, _, _ := ownerSrv.Store.GetArtifact(slug)
	if art.GetLatestVersion() != 1 {
		t.Fatalf("latest_version after rejected appends: got %d, want 1", art.GetLatestVersion())
	}
}

// TestMetadataEndpoint verifies the authorized metadata read: the owner sees the
// full version list with is_owner=true, and unauthorized/anonymous callers get a
// not-leaking 404 on a private artifact.
func TestMetadataEndpoint(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})

	slug := publishAs(t, srv.Routes(), "<h1>one</h1>")
	// Append a second version so the metadata list has both.
	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/versions?note=v2", strings.NewReader("<h1>two</h1>"))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("append v2: got %d, want 201", rr.Code)
	}

	// Owner metadata read.
	req = httptest.NewRequest(http.MethodGet, "/artifacts/"+slug, nil)
	rr = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("metadata as owner: got %d, want 200 (body %q)", rr.Code, rr.Body.String())
	}
	var meta struct {
		Slug          string `json:"slug"`
		Visibility    string `json:"visibility"`
		LatestVersion int32  `json:"latest_version"`
		IsOwner       bool   `json:"is_owner"`
		Versions      []struct {
			N    int32  `json:"n"`
			Note string `json:"note"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if meta.Slug != slug || meta.LatestVersion != 2 || !meta.IsOwner {
		t.Fatalf("metadata: got %+v, want slug=%q latest=2 is_owner=true", meta, slug)
	}
	if meta.Visibility != "private" {
		t.Fatalf("metadata visibility: got %q, want private", meta.Visibility)
	}
	if len(meta.Versions) != 2 || meta.Versions[0].N != 1 || meta.Versions[1].N != 2 {
		t.Fatalf("metadata versions: got %+v, want [1,2]", meta.Versions)
	}

	// Anonymous caller on a private artifact → 404 (not-leak).
	anonSrv := &Server{Store: srv.Store, Blob: srv.Blob, Auth: fakeAuth{ok: false}, BaseURL: "https://here.now"}
	req = httptest.NewRequest(http.MethodGet, "/artifacts/"+slug, nil)
	rr = httptest.NewRecorder()
	anonSrv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("metadata as anon (private): got %d, want 404", rr.Code)
	}

	// Non-owner metadata → 404 while private, and (per not-leak) never reveals is_owner.
	other := &artifactav1.Identity{Sub: "local:other"}
	otherSrv := &Server{Store: srv.Store, Blob: srv.Blob, Auth: fakeAuth{id: other, ok: true}, BaseURL: "https://here.now"}
	req = httptest.NewRequest(http.MethodGet, "/artifacts/"+slug, nil)
	rr = httptest.NewRecorder()
	otherSrv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("metadata as non-owner (private): got %d, want 404", rr.Code)
	}
}
