package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// searchResp mirrors the protojson (snake_case) shape of SearchArtifactsResponse.
type searchResp struct {
	Items []struct {
		Slug       string `json:"slug"`
		Title      string `json:"title"`
		Visibility string `json:"visibility"`
		OwnerEmail string `json:"owner_email"`
		URL        string `json:"url"`
	} `json:"items"`
	Page struct {
		Page       int  `json:"page"`
		PageSize   int  `json:"page_size"`
		Total      int  `json:"total"`
		TotalPages int  `json:"total_pages"`
		HasNext    bool `json:"has_next"`
		HasPrev    bool `json:"has_prev"`
	} `json:"page"`
}

func doSearch(t *testing.T, srv *Server, query string) (int, searchResp) {
	t.Helper()
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/artifacts/search"+query, nil))
	var out searchResp
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode search response: %v (body %s)", err, rr.Body.String())
		}
	}
	return rr.Code, out
}

func hasSlug(rows searchResp, slug string) bool {
	for _, r := range rows.Items {
		if r.Slug == slug {
			return true
		}
	}
	return false
}

// seedSearchWorld sets up a viewer with owned/shared/org artifacts plus a private
// one owned by someone else (must never appear) and a foreign org artifact with a
// grantee (the leak-regression fixture).
func seedSearchWorld(t *testing.T, srv *Server, viewer *artifactav1.Identity) {
	t.Helper()
	put := func(slug, ownerSub, ownerEmail, title string, vis artifactav1.Visibility, min int) {
		if err := srv.Store.PutArtifact(&artifactav1.Artifact{
			Slug: slug, OwnerSub: ownerSub, OwnerEmail: ownerEmail, Title: title, Visibility: vis,
			LatestVersion: 1, CreatedAt: timestamppb.New(time.Date(2026, 1, 1, 0, min, 0, 0, time.UTC)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(slug, granteeSub, granteeEmail string) {
		if err := srv.Store.AddGrant(&artifactav1.Grant{Slug: slug, GranteeSub: granteeSub, GranteeEmail: granteeEmail}); err != nil {
			t.Fatal(err)
		}
	}

	put("own-deck", viewer.GetSub(), viewer.GetEmail(), "My Deck", artifactav1.Visibility_VISIBILITY_PRIVATE, 1)
	put("own-budget", viewer.GetSub(), viewer.GetEmail(), "Budget", artifactav1.Visibility_VISIBILITY_INVITED, 2)
	grant("own-budget", "", "carol@x.co") // viewer shared their own artifact with carol

	put("shared-in", "local:alice", "alice@x.co", "Alice Doc", artifactav1.Visibility_VISIBILITY_INVITED, 3)
	grant("shared-in", viewer.GetSub(), "") // shared with the viewer by subject

	put("org-doc", "local:bob", "bob@x.co", "Bob Org", artifactav1.Visibility_VISIBILITY_ORG, 4)

	put("secret", "local:alice", "alice@x.co", "Secret", artifactav1.Visibility_VISIBILITY_PRIVATE, 5) // not viewer's, not shared

	// Foreign org artifact with a grantee — the leak fixture. The viewer can SEE it
	// (it's org), but must never be able to match it by its grantee's email.
	put("org-foreign", "local:alice", "alice@x.co", "Foreign", artifactav1.Visibility_VISIBILITY_ORG, 6)
	grant("org-foreign", "", "dave@x.co")
}

func TestSearchRejectsUnauthenticated(t *testing.T) {
	srv := newTestServer(t, t.TempDir(), fakeAuth{ok: false})
	code, _ := doSearch(t, srv, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("search anon: got %d, want 401", code)
	}
}

func TestSearchVisibleSetOnly(t *testing.T) {
	viewer := &artifactav1.Identity{Sub: "local:viewer", Email: "viewer@x.co"}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: viewer, ok: true})
	seedSearchWorld(t, srv, viewer)

	code, resp := doSearch(t, srv, "?page_size=100")
	if code != http.StatusOK {
		t.Fatalf("search: got %d, want 200", code)
	}
	// Visible: own-deck, own-budget, shared-in, org-doc, org-foreign. NOT secret.
	for _, want := range []string{"own-deck", "own-budget", "shared-in", "org-doc", "org-foreign"} {
		if !hasSlug(resp, want) {
			t.Fatalf("search missing visible artifact %q; got %+v", want, resp.Items)
		}
	}
	if hasSlug(resp, "secret") {
		t.Fatal("search leaked a private artifact the caller cannot view")
	}
	if resp.Page.Total != 5 {
		t.Fatalf("total = %d, want 5", resp.Page.Total)
	}
}

func TestSearchTextFilter(t *testing.T) {
	viewer := &artifactav1.Identity{Sub: "local:viewer", Email: "viewer@x.co"}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: viewer, ok: true})
	seedSearchWorld(t, srv, viewer)

	_, resp := doSearch(t, srv, "?q=budget")
	if len(resp.Items) != 1 || resp.Items[0].Slug != "own-budget" {
		t.Fatalf("q=budget = %+v, want [own-budget]", resp.Items)
	}
}

func TestSearchEmailMatchesOwnerAndOwnGrantee(t *testing.T) {
	viewer := &artifactav1.Identity{Sub: "local:viewer", Email: "viewer@x.co"}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: viewer, ok: true})
	seedSearchWorld(t, srv, viewer)

	// owner_email of an org artifact owned by bob is visible + matchable.
	_, resp := doSearch(t, srv, "?email=bob@x.co")
	if len(resp.Items) != 1 || resp.Items[0].Slug != "org-doc" {
		t.Fatalf("email=bob = %+v, want [org-doc]", resp.Items)
	}
	// grantee_email on the viewer's OWN artifact matches.
	_, resp = doSearch(t, srv, "?email=carol@x.co")
	if len(resp.Items) != 1 || resp.Items[0].Slug != "own-budget" {
		t.Fatalf("email=carol = %+v, want [own-budget]", resp.Items)
	}
}

// CRITICAL (ADR-0025): searching the email of a grantee on a foreign org artifact
// must NOT surface it — the caller can view the artifact but must never be able to
// probe/enumerate its share-list.
func TestSearchDoesNotLeakForeignGrantee(t *testing.T) {
	viewer := &artifactav1.Identity{Sub: "local:viewer", Email: "viewer@x.co"}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: viewer, ok: true})
	seedSearchWorld(t, srv, viewer)

	_, resp := doSearch(t, srv, "?email=dave@x.co")
	if len(resp.Items) != 0 {
		t.Fatalf("email=dave leaked foreign grantee: %+v, want no matches", resp.Items)
	}
}

func TestSearchPaginationMeta(t *testing.T) {
	viewer := &artifactav1.Identity{Sub: "local:viewer", Email: "viewer@x.co"}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: viewer, ok: true})
	seedSearchWorld(t, srv, viewer) // 5 visible

	_, p1 := doSearch(t, srv, "?page=1&page_size=2&sort_by=created_at&sort_order=asc")
	if p1.Page.Total != 5 || p1.Page.TotalPages != 3 || len(p1.Items) != 2 || !p1.Page.HasNext || p1.Page.HasPrev {
		t.Fatalf("page1 meta = %+v (items %d), want total5/pages3/hasNext/!hasPrev/2items", p1.Page, len(p1.Items))
	}
	_, p3 := doSearch(t, srv, "?page=3&page_size=2&sort_by=created_at&sort_order=asc")
	if len(p3.Items) != 1 || p3.Page.HasNext || !p3.Page.HasPrev {
		t.Fatalf("page3 meta = %+v (items %d), want 1 item/!hasNext/hasPrev", p3.Page, len(p3.Items))
	}
	// page_size over the cap clamps to 100 (still returns all 5 here).
	_, big := doSearch(t, srv, "?page_size=99999")
	if big.Page.PageSize != 100 {
		t.Fatalf("page_size clamp = %d, want 100", big.Page.PageSize)
	}
}

func TestSearchInvalidVisibilityRejected(t *testing.T) {
	viewer := &artifactav1.Identity{Sub: "local:viewer", Email: "viewer@x.co"}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: viewer, ok: true})
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/artifacts/search?visibility=bogus", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bogus visibility: got %d, want 400", rr.Code)
	}
}

// The /artifacts/search literal route must not be shadowed by /artifacts/{slug}.
func TestSearchRouteNotShadowedByMetadata(t *testing.T) {
	viewer := &artifactav1.Identity{Sub: "local:viewer", Email: "viewer@x.co"}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: viewer, ok: true})
	code, resp := doSearch(t, srv, "")
	if code != http.StatusOK {
		t.Fatalf("search route: got %d, want 200 (not the metadata handler)", code)
	}
	// Empty world → empty items, well-formed meta.
	if len(resp.Items) != 0 || resp.Page.Total != 0 {
		t.Fatalf("empty search = %+v, want no items/total 0", resp)
	}
}
