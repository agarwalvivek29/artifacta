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
// path with the Bearer token, and auto-detects the grantee shape: a subject is
// sent as {"grantee_sub"} and an email as {"email"} (invite-by-email, ADR-0019).
func TestAddGrantRemoteSendsBearerAndPost(t *testing.T) {
	const token = "id-token-xyz"
	const slug = "abc123"

	cases := []struct {
		name      string
		grantee   string
		wantSub   string
		wantEmail string
	}{
		{name: "subject", grantee: "local:friend", wantSub: "local:friend"},
		{name: "email", grantee: "friend@example.com", wantEmail: "friend@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotAuth string
			var gotBody struct {
				GranteeSub string `json:"grantee_sub"`
				Email      string `json:"email"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(http.StatusCreated)
			}))
			defer srv.Close()

			if err := addGrantRemote(srv.URL, token, slug, tc.grantee); err != nil {
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
			if gotBody.GranteeSub != tc.wantSub {
				t.Fatalf("grantee_sub = %q, want %q", gotBody.GranteeSub, tc.wantSub)
			}
			if gotBody.Email != tc.wantEmail {
				t.Fatalf("email = %q, want %q", gotBody.Email, tc.wantEmail)
			}
		})
	}
}

// TestLooksLikeEmail covers the request-shape heuristic that routes a grantee to
// {"email"} vs {"grantee_sub"}.
func TestLooksLikeEmail(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"friend@example.com", true},
		{"a.b+tag@sub.example.co", true},
		{"local:friend", false},
		{"a@b", false}, // no dot in domain
		{"@example.com", false},
		{"friend@", false},
		{"two@@example.com", false},
		{"has space@example.com", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := looksLikeEmail(tc.in); got != tc.want {
			t.Errorf("looksLikeEmail(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestRemoveGrantRemoteSendsBearerAndDelete verifies the unshare helper DELETEs
// the right escaped path with the Bearer token (grantee is an email, whose '@'
// must stay within one path segment).
func TestRemoveGrantRemoteSendsBearerAndDelete(t *testing.T) {
	const token = "id-token-xyz"
	const slug = "abc123"
	const grantee = "friend@example.com"

	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path // net/http decodes %40 back to @
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := removeGrantRemote(srv.URL, token, slug, grantee); err != nil {
		t.Fatalf("removeGrantRemote: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %q, want DELETE", gotMethod)
	}
	if want := "/artifacts/" + slug + "/grants/" + grantee; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer "+token)
	}
}

// TestRemoveGrantRemoteErrorsOnNon200 confirms a non-200 (e.g. unknown grantee
// 404) surfaces as an error.
func TestRemoveGrantRemoteErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	if err := removeGrantRemote(srv.URL, "tok", "slug", "who"); err == nil {
		t.Fatalf("removeGrantRemote: expected error on 404, got nil")
	}
}

// TestSetLabelRemoteSendsBearerAndPatch verifies the label helper PATCHes the
// right path with the label body and returns the server's subdomain_url.
func TestSetLabelRemoteSendsBearerAndPatch(t *testing.T) {
	const token = "id-token-xyz"
	const slug = "abc123"
	const wantURL = "https://my-app.here.now"

	var gotMethod, gotPath, gotAuth string
	var gotBody struct {
		Label string `json:"label"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"slug": slug, "label": "my-app", "subdomain_url": wantURL})
	}))
	defer srv.Close()

	got, err := setLabelRemote(srv.URL, token, slug, "my-app")
	if err != nil {
		t.Fatalf("setLabelRemote: %v", err)
	}
	if got != wantURL {
		t.Fatalf("subdomain_url = %q, want %q", got, wantURL)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %q, want PATCH", gotMethod)
	}
	if want := "/artifacts/" + slug + "/label"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer "+token)
	}
	if gotBody.Label != "my-app" {
		t.Fatalf("label = %q, want my-app", gotBody.Label)
	}
}

// TestSetLabelRemoteEmptySubdomainURL covers a server with no RootDomain: the
// helper returns "" and the caller is responsible for the fallback message.
func TestSetLabelRemoteEmptySubdomainURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"slug": "s", "label": "my-app", "subdomain_url": ""})
	}))
	defer srv.Close()
	got, err := setLabelRemote(srv.URL, "tok", "s", "my-app")
	if err != nil {
		t.Fatalf("setLabelRemote: %v", err)
	}
	if got != "" {
		t.Fatalf("subdomain_url = %q, want empty", got)
	}
}

// TestSetLabelRemoteErrorsOnConflict confirms a 409 (label taken) surfaces as an error.
func TestSetLabelRemoteErrorsOnConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "label taken", http.StatusConflict)
	}))
	defer srv.Close()
	if _, err := setLabelRemote(srv.URL, "tok", "slug", "taken"); err == nil {
		t.Fatalf("setLabelRemote: expected error on 409, got nil")
	}
}

// TestAddCommentRemoteSendsBodyAndReturnsID verifies the comment-add helper POSTs
// {body} (plus parent_id when set) and returns the new id.
func TestAddCommentRemoteSendsBodyAndReturnsID(t *testing.T) {
	const token = "id-token-xyz"
	const slug = "abc123"

	cases := []struct {
		name       string
		parentID   string
		wantParent string
	}{
		{name: "root", parentID: "", wantParent: ""},
		{name: "reply", parentID: "root-1", wantParent: "root-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody struct {
				Body     string `json:"body"`
				ParentID string `json:"parent_id"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "cmt-9", "body": gotBody.Body})
			}))
			defer srv.Close()

			id, err := addCommentRemote(srv.URL, token, slug, "looks good", tc.parentID)
			if err != nil {
				t.Fatalf("addCommentRemote: %v", err)
			}
			if id != "cmt-9" {
				t.Fatalf("id = %q, want cmt-9", id)
			}
			if gotMethod != http.MethodPost {
				t.Fatalf("method = %q, want POST", gotMethod)
			}
			if want := "/artifacts/" + slug + "/comments"; gotPath != want {
				t.Fatalf("path = %q, want %q", gotPath, want)
			}
			if gotBody.Body != "looks good" {
				t.Fatalf("body = %q, want 'looks good'", gotBody.Body)
			}
			if gotBody.ParentID != tc.wantParent {
				t.Fatalf("parent_id = %q, want %q", gotBody.ParentID, tc.wantParent)
			}
		})
	}
}

// TestListCommentsRemoteDecodes verifies the comment-list helper GETs and decodes
// the flat comment rows (including a reply and a resolved row).
func TestListCommentsRemoteDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "root-1", "version": 1, "author_email": "a@x.com", "body": "root", "resolved": true},
			{"id": "rep-1", "version": 1, "author_email": "b@x.com", "body": "reply", "parent_id": "root-1"},
		})
	}))
	defer srv.Close()

	rows, err := listCommentsRemote(srv.URL, "tok", "slug")
	if err != nil {
		t.Fatalf("listCommentsRemote: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if !rows[0].Resolved || rows[0].ID != "root-1" {
		t.Fatalf("rows[0] = %+v, want resolved root-1", rows[0])
	}
	if rows[1].ParentID != "root-1" {
		t.Fatalf("rows[1].parent_id = %q, want root-1", rows[1].ParentID)
	}
}

// TestResolveCommentRemoteSendsPost verifies the resolve helper POSTs the right
// path with the Bearer token.
func TestResolveCommentRemoteSendsPost(t *testing.T) {
	const token = "id-token-xyz"
	const slug = "abc123"
	const id = "cmt-9"

	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := resolveCommentRemote(srv.URL, token, slug, id); err != nil {
		t.Fatalf("resolveCommentRemote: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if want := "/artifacts/" + slug + "/comments/" + id + "/resolve"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer "+token)
	}
}

// TestResolveCommentRemoteErrorsOnNon200 confirms a non-200 surfaces as an error.
func TestResolveCommentRemoteErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	if err := resolveCommentRemote(srv.URL, "tok", "slug", "id"); err == nil {
		t.Fatalf("resolveCommentRemote: expected error on 404, got nil")
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
