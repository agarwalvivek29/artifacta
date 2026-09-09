package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

// postComment posts a comment body on slug through srv and returns the recorder.
func postComment(t *testing.T, srv *Server, slug, jsonBody string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/comments", strings.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	return rr
}

// TestAddCommentViewerCreates verifies the owner and an invited grantee (both
// CanView) can post a comment and get 201 with the created comment echoed back,
// version defaulting to latest when omitted.
func TestAddCommentViewerCreates(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})

	// v1 then v2 so "latest" is a meaningful default.
	slug := publishAs(t, srv.Routes(), "<h1>one</h1>")
	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/versions", strings.NewReader("<h1>two</h1>"))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("append v2: got %d, want 201", rr.Code)
	}

	// Owner comments with no version → defaults to latest (2).
	rr = postComment(t, srv, slug, `{"body":"looks good"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("owner comment: got %d, want 201 (body %q)", rr.Code, rr.Body.String())
	}
	var got commentView
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode comment: %v", err)
	}
	if got.ID == "" || got.Version != 2 || got.AuthorEmail != "owner@localhost" || got.Body != "looks good" || got.Resolved {
		t.Fatalf("comment response: %+v, want version=2 author=owner@localhost body=looks good resolved=false", got)
	}

	// Make the artifact invited-only and grant a friend, then the friend comments on v1.
	if err := srv.Store.AddGrant(&artifactav1.Grant{Slug: slug, GranteeSub: "local:friend", GrantedBy: owner.GetSub()}); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	art, _, _ := srv.Store.GetArtifact(slug)
	art.Visibility = artifactav1.Visibility_VISIBILITY_INVITED
	if err := srv.Store.PutArtifact(art); err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	friend := &artifactav1.Identity{Sub: "local:friend", Email: "friend@localhost"}
	friendSrv := &Server{Store: srv.Store, Blob: srv.Blob, Auth: fakeAuth{id: friend, ok: true}, BaseURL: "https://here.now"}
	rr = postComment(t, friendSrv, slug, `{"body":"tweak the intro","version":1}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("grantee comment: got %d, want 201 (body %q)", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode grantee comment: %v", err)
	}
	if got.Version != 1 || got.AuthorEmail != "friend@localhost" {
		t.Fatalf("grantee comment: %+v, want version=1 author=friend@localhost", got)
	}
}

// TestAddCommentDeniedNotLeak verifies an anonymous caller and a non-grantee on
// a private artifact both get a not-leaking 404 (never 401/403), and nothing is
// stored.
func TestAddCommentDeniedNotLeak(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	slug := publishAs(t, srv.Routes(), "<h1>private</h1>")

	// Anonymous → 404.
	anonSrv := &Server{Store: srv.Store, Blob: srv.Blob, Auth: fakeAuth{ok: false}, BaseURL: "https://here.now"}
	if rr := postComment(t, anonSrv, slug, `{"body":"hi"}`); rr.Code != http.StatusNotFound {
		t.Fatalf("anon comment: got %d, want 404", rr.Code)
	}

	// Authenticated non-grantee on a private artifact → 404.
	stranger := &artifactav1.Identity{Sub: "local:stranger", Email: "stranger@localhost"}
	strangerSrv := &Server{Store: srv.Store, Blob: srv.Blob, Auth: fakeAuth{id: stranger, ok: true}, BaseURL: "https://here.now"}
	if rr := postComment(t, strangerSrv, slug, `{"body":"hi"}`); rr.Code != http.StatusNotFound {
		t.Fatalf("non-grantee comment: got %d, want 404", rr.Code)
	}

	// Unknown slug → 404 too.
	if rr := postComment(t, srv, "doesnotexist", `{"body":"hi"}`); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown slug comment: got %d, want 404", rr.Code)
	}

	// Nothing leaked into the store.
	if cs, _ := srv.Store.Comments(slug); len(cs) != 0 {
		t.Fatalf("comments stored despite denial: %v", cs)
	}
}

// TestAddCommentEmptyBodyRejected verifies a blank/whitespace body is a 400 and
// nothing is stored.
func TestAddCommentEmptyBodyRejected(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	slug := publishAs(t, srv.Routes(), "<h1>doc</h1>")

	if rr := postComment(t, srv, slug, `{"body":"   "}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("whitespace body: got %d, want 400", rr.Code)
	}
	if cs, _ := srv.Store.Comments(slug); len(cs) != 0 {
		t.Fatalf("empty comment stored: %v", cs)
	}
}

// TestListCommentsAndVersionFilter verifies list returns posted comments
// ascending, and ?version=n filters to that version only.
func TestListCommentsAndVersionFilter(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})

	slug := publishAs(t, srv.Routes(), "<h1>one</h1>")
	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/versions", strings.NewReader("<h1>two</h1>"))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("append v2: got %d, want 201", rr.Code)
	}

	if rr := postComment(t, srv, slug, `{"body":"on v1","version":1}`); rr.Code != http.StatusCreated {
		t.Fatalf("comment v1: got %d", rr.Code)
	}
	if rr := postComment(t, srv, slug, `{"body":"on v2","version":2}`); rr.Code != http.StatusCreated {
		t.Fatalf("comment v2: got %d", rr.Code)
	}

	list := func(query string) []commentView {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/artifacts/"+slug+"/comments"+query, nil)
		rr := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("list%s: got %d, want 200 (body %q)", query, rr.Code, rr.Body.String())
		}
		var out []commentView
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		return out
	}

	all := list("")
	if len(all) != 2 || all[0].Body != "on v1" || all[1].Body != "on v2" {
		t.Fatalf("list all: %+v, want [on v1, on v2]", all)
	}
	v1 := list("?version=1")
	if len(v1) != 1 || v1[0].Body != "on v1" {
		t.Fatalf("list v1: %+v, want [on v1]", v1)
	}
	v2 := list("?version=2")
	if len(v2) != 1 || v2[0].Body != "on v2" {
		t.Fatalf("list v2: %+v, want [on v2]", v2)
	}
}

// TestResolveCommentOwnerOnly verifies resolve is owner-gated: the owner gets
// 200 and the comment becomes resolved; a non-owner gets 404 and the comment
// stays unresolved; an unknown id is 404.
func TestResolveCommentOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	slug := publishAs(t, srv.Routes(), "<h1>doc</h1>")

	rr := postComment(t, srv, slug, `{"body":"please fix"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed comment: got %d", rr.Code)
	}
	var c commentView
	if err := json.Unmarshal(rr.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode: %v", err)
	}

	resolve := func(srv *Server, id string) int {
		req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/comments/"+id+"/resolve", nil)
		rr := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rr, req)
		return rr.Code
	}

	// Non-owner (a grantee, so they CanView but do not own) → 404.
	if err := srv.Store.AddGrant(&artifactav1.Grant{Slug: slug, GranteeSub: "local:friend", GrantedBy: owner.GetSub()}); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	friend := &artifactav1.Identity{Sub: "local:friend", Email: "friend@localhost"}
	friendSrv := &Server{Store: srv.Store, Blob: srv.Blob, Auth: fakeAuth{id: friend, ok: true}, BaseURL: "https://here.now"}
	if code := resolve(friendSrv, c.ID); code != http.StatusNotFound {
		t.Fatalf("non-owner resolve: got %d, want 404", code)
	}
	if cs, _ := srv.Store.Comments(slug); cs[0].GetResolved() {
		t.Fatal("comment resolved by non-owner")
	}

	// Owner resolves → 200 and the flag flips.
	if code := resolve(srv, c.ID); code != http.StatusOK {
		t.Fatalf("owner resolve: got %d, want 200", code)
	}
	if cs, _ := srv.Store.Comments(slug); !cs[0].GetResolved() {
		t.Fatal("comment not resolved after owner resolve")
	}

	// Unknown id → 404.
	if code := resolve(srv, "no-such-id"); code != http.StatusNotFound {
		t.Fatalf("resolve unknown id: got %d, want 404", code)
	}
}

// TestResolveCommentUnauthenticated verifies an anonymous caller gets 401 on the
// owner-only resolve endpoint.
func TestResolveCommentUnauthenticated(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	slug := publishAs(t, srv.Routes(), "<h1>doc</h1>")
	rr := postComment(t, srv, slug, `{"body":"fix"}`)
	var c commentView
	_ = json.Unmarshal(rr.Body.Bytes(), &c)

	anonSrv := &Server{Store: srv.Store, Blob: srv.Blob, Auth: fakeAuth{ok: false}, BaseURL: "https://here.now"}
	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/comments/"+c.ID+"/resolve", nil)
	rr = httptest.NewRecorder()
	anonSrv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("anon resolve: got %d, want 401", rr.Code)
	}
}

// TestAddCommentAnchorRoundTrip verifies a text anchor (ADR-0015) is stored on
// create, echoed back, and returned by list — and that an over-cap quote
// gracefully degrades to a page-level note rather than being rejected.
func TestAddCommentAnchorRoundTrip(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})
	slug := publishAs(t, srv.Routes(), "<p>the quick brown fox jumps</p>")

	// Anchored comment: the anchor should round-trip verbatim.
	rr := postComment(t, srv, slug,
		`{"body":"why brown?","anchor":{"quote":"brown fox","prefix":"quick ","suffix":" jumps","start":10,"end":19}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("anchored comment: got %d, want 201 (body %q)", rr.Code, rr.Body.String())
	}
	var got commentView
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Anchor == nil {
		t.Fatalf("anchor missing in create response: %+v", got)
	}
	if got.Anchor.Quote != "brown fox" || got.Anchor.Prefix != "quick " || got.Anchor.Suffix != " jumps" ||
		got.Anchor.Start != 10 || got.Anchor.End != 19 {
		t.Fatalf("anchor mismatch: %+v", got.Anchor)
	}

	// A comment with no anchor is a page-level note (anchor omitted).
	rr = postComment(t, srv, slug, `{"body":"general note"}`)
	var pageLevel commentView
	if err := json.Unmarshal(rr.Body.Bytes(), &pageLevel); err != nil {
		t.Fatalf("decode page-level: %v", err)
	}
	if pageLevel.Anchor != nil {
		t.Fatalf("page-level comment should have no anchor, got %+v", pageLevel.Anchor)
	}

	// An over-cap quote degrades to a page-level note (201, anchor dropped).
	huge := strings.Repeat("x", maxQuoteRunes+1)
	rr = postComment(t, srv, slug, `{"body":"big","anchor":{"quote":"`+huge+`"}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("over-cap quote: got %d, want 201", rr.Code)
	}
	var overCap commentView
	if err := json.Unmarshal(rr.Body.Bytes(), &overCap); err != nil {
		t.Fatalf("decode over-cap: %v", err)
	}
	if overCap.Anchor != nil {
		t.Fatalf("over-cap quote should degrade to page-level, got anchor %+v", overCap.Anchor)
	}

	// List returns the anchored comment with its anchor intact.
	req := httptest.NewRequest(http.MethodGet, "/artifacts/"+slug+"/comments", nil)
	rr = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200", rr.Code)
	}
	var list []commentView
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	anchored := 0
	for _, c := range list {
		if c.Anchor != nil {
			anchored++
			if c.Anchor.Quote != "brown fox" {
				t.Fatalf("listed anchor quote = %q, want %q", c.Anchor.Quote, "brown fox")
			}
		}
	}
	if anchored != 1 {
		t.Fatalf("want exactly 1 anchored comment in list, got %d (of %d)", anchored, len(list))
	}
}

// TestReplyThread verifies threaded replies (ADR-0016): a reply carries its
// parent_id, inherits the root's version, ignores an anchor, and can't parent
// another reply; resolving a reply (or an unknown id) is a 404.
func TestReplyThread(t *testing.T) {
	dir := t.TempDir()
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@localhost"}
	srv := newTestServer(t, dir, fakeAuth{id: owner, ok: true})

	// v1 then v2 so we can prove a reply inherits the ROOT's version, not latest.
	slug := publishAs(t, srv.Routes(), "<h1>one</h1>")
	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/versions", strings.NewReader("<h1>two</h1>"))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)

	// Root comment pinned to v1.
	rr = postComment(t, srv, slug, `{"body":"root question","version":1}`)
	var root commentView
	_ = json.Unmarshal(rr.Body.Bytes(), &root)
	if root.ID == "" || root.Version != 1 {
		t.Fatalf("root: %+v, want version=1", root)
	}

	// Reply: parent_id set, version + anchor in the body are ignored (inherits v1,
	// stays unanchored).
	rr = postComment(t, srv, slug,
		`{"body":"a reply","parent_id":"`+root.ID+`","version":2,"anchor":{"quote":"x"}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("reply: got %d, want 201 (body %q)", rr.Code, rr.Body.String())
	}
	var reply commentView
	_ = json.Unmarshal(rr.Body.Bytes(), &reply)
	if reply.ParentID != root.ID {
		t.Fatalf("reply parent_id = %q, want %q", reply.ParentID, root.ID)
	}
	if reply.Version != 1 {
		t.Fatalf("reply version = %d, want 1 (inherit root)", reply.Version)
	}
	if reply.Anchor != nil {
		t.Fatalf("reply should never be anchored, got %+v", reply.Anchor)
	}

	// A reply can't parent another reply (one level only) → 400.
	rr = postComment(t, srv, slug, `{"body":"nested","parent_id":"`+reply.ID+`"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("reply-to-reply: got %d, want 400", rr.Code)
	}

	// Unknown parent → 400.
	rr = postComment(t, srv, slug, `{"body":"orphan","parent_id":"nope"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown parent: got %d, want 400", rr.Code)
	}

	// Resolving a reply is a 404 (resolve applies to threads/roots only).
	req = httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/comments/"+reply.ID+"/resolve", nil)
	rr = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("resolve reply: got %d, want 404", rr.Code)
	}

	// Resolving the root works.
	req = httptest.NewRequest(http.MethodPost, "/artifacts/"+slug+"/comments/"+root.ID+"/resolve", nil)
	rr = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve root: got %d, want 200", rr.Code)
	}
}
