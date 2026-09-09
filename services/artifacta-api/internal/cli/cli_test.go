package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestPublishRemoteSendsBearerAndReturnsURL exercises the remote-publish helper
// against a stub server: it must POST the file body, carry the id_token as a
// Bearer credential and a title query, and return the `url` from the response.
func TestPublishRemoteSendsBearerAndReturnsURL(t *testing.T) {
	const token = "id-token-xyz"
	const wantURL = "https://here.now/a/abc123"
	const payload = "<h1>hi here.now</h1>"

	var gotMethod, gotAuth, gotTitle, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotTitle = r.URL.Query().Get("title")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"slug": "abc123", "url": wantURL})
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	gotURL, err := publishRemote(srv.URL, token, path)
	if err != nil {
		t.Fatalf("publishRemote: %v", err)
	}
	if gotURL != wantURL {
		t.Fatalf("url = %q, want %q", gotURL, wantURL)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer "+token)
	}
	if gotTitle != "index.html" {
		t.Fatalf("title = %q, want %q", gotTitle, "index.html")
	}
	if gotBody != payload {
		t.Fatalf("uploaded body = %q, want %q", gotBody, payload)
	}
}

// TestPublishRemoteErrorsOnNon201 confirms the helper surfaces a non-201 server
// response (e.g. a rejected token) as an error rather than a bogus URL.
func TestPublishRemoteErrorsOnNon201(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	if _, err := publishRemote(srv.URL, "tok", path); err == nil {
		t.Fatalf("publishRemote: expected error on 401, got nil")
	}
}

// TestAddVersionRemoteSendsBearerAndReturnsVersion exercises the update helper
// against a stub server: it must POST the file body to
// /artifacts/<slug>/versions, carry the id_token as a Bearer credential, and
// return the version number + url from the response.
func TestAddVersionRemoteSendsBearerAndReturnsVersion(t *testing.T) {
	const token = "id-token-xyz"
	const slug = "abc123"
	const wantURL = "https://here.now/a/abc123"
	const payload = "<h1>v2 here.now</h1>"

	var gotMethod, gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"slug": slug, "version": 2, "url": wantURL})
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	gotN, gotURL, err := addVersionRemote(srv.URL, token, slug, path)
	if err != nil {
		t.Fatalf("addVersionRemote: %v", err)
	}
	if gotN != 2 {
		t.Fatalf("version = %d, want 2", gotN)
	}
	if gotURL != wantURL {
		t.Fatalf("url = %q, want %q", gotURL, wantURL)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if want := "/artifacts/" + slug + "/versions"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer "+token)
	}
	if gotBody != payload {
		t.Fatalf("uploaded body = %q, want %q", gotBody, payload)
	}
}

// TestAddVersionRemoteErrorsOnNon201 confirms the helper surfaces a non-201
// server response as an error rather than a bogus version.
func TestAddVersionRemoteErrorsOnNon201(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	if _, _, err := addVersionRemote(srv.URL, "tok", "slug", path); err == nil {
		t.Fatalf("addVersionRemote: expected error on 404, got nil")
	}
}

// TestFetchMetadataSendsBearerAndDecodes verifies the metadata helper GETs the
// right path with the Bearer token and decodes the versions projection.
func TestFetchMetadataSendsBearerAndDecodes(t *testing.T) {
	const token = "id-token-xyz"
	const slug = "abc123"

	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"slug": slug, "title": "Doc", "visibility": "private",
			"latest_version": 2, "is_owner": true,
			"versions": []map[string]any{
				{"n": 2, "created_at": "2026-08-24T00:00:00Z", "note": "second"},
				{"n": 1, "created_at": "2026-08-23T00:00:00Z", "note": ""},
			},
		})
	}))
	defer srv.Close()

	m, err := fetchMetadata(srv.URL, token, slug)
	if err != nil {
		t.Fatalf("fetchMetadata: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if want := "/artifacts/" + slug; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer "+token)
	}
	if m.LatestVersion != 2 || len(m.Versions) != 2 {
		t.Fatalf("metadata = %+v, want latest 2 with 2 versions", m)
	}
	if m.Versions[0].N != 2 || m.Versions[0].Note != "second" {
		t.Fatalf("versions[0] = %+v, want n=2 note=second", m.Versions[0])
	}
}

// TestSetVisibilityRemoteSendsBearerAndPatch verifies the visibility helper
// PATCHes the right path with the Bearer token and the requested visibility.
func TestSetVisibilityRemoteSendsBearerAndPatch(t *testing.T) {
	const token = "id-token-xyz"
	const slug = "abc123"

	var gotMethod, gotPath, gotAuth string
	var gotBody struct {
		Visibility string `json:"visibility"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := setVisibilityRemote(srv.URL, token, slug, "invited"); err != nil {
		t.Fatalf("setVisibilityRemote: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %q, want PATCH", gotMethod)
	}
	if want := "/artifacts/" + slug + "/visibility"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer "+token)
	}
	if gotBody.Visibility != "invited" {
		t.Fatalf("visibility = %q, want invited", gotBody.Visibility)
	}
}

// TestAddGrantRemoteSendsBearerAndPost verifies the grant helper POSTs the right
// path with the Bearer token and the grantee subject.
func TestAddGrantRemoteSendsBearerAndPost(t *testing.T) {
	const token = "id-token-xyz"
	const slug = "abc123"

	var gotMethod, gotPath, gotAuth string
	var gotBody struct {
		GranteeSub string `json:"grantee_sub"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	if err := addGrantRemote(srv.URL, token, slug, "local:friend"); err != nil {
		t.Fatalf("addGrantRemote: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if want := "/artifacts/" + slug + "/grants"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer "+token)
	}
	if gotBody.GranteeSub != "local:friend" {
		t.Fatalf("grantee_sub = %q, want local:friend", gotBody.GranteeSub)
	}
}

// TestSetVisibilityRemoteErrorsOnNon200 confirms a non-200 response surfaces as
// an error rather than being silently swallowed.
func TestSetVisibilityRemoteErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	if err := setVisibilityRemote(srv.URL, "tok", "slug", "invited"); err == nil {
		t.Fatalf("setVisibilityRemote: expected error on 404, got nil")
	}
}
