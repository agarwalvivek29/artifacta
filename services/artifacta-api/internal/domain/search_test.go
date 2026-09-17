package domain

import (
	"testing"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func art(slug, title string, vis artifactav1.Visibility, ownerEmail string, created time.Time) *artifactav1.Artifact {
	return &artifactav1.Artifact{
		Slug:       slug,
		Title:      title,
		Visibility: vis,
		OwnerEmail: ownerEmail,
		CreatedAt:  timestamppb.New(created),
	}
}

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func slugs(arts []*artifactav1.Artifact) []string {
	out := make([]string, 0, len(arts))
	for _, a := range arts {
		out = append(out, a.GetSlug())
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNormalizeClampsAndDefaults(t *testing.T) {
	cases := []struct {
		in                 SearchQuery
		wantPage, wantSize int32
		wantSortBy         string
	}{
		{SearchQuery{}, 1, DefaultPageSize, SortByCreatedAt},
		{SearchQuery{Page: 0, PageSize: 0}, 1, DefaultPageSize, SortByCreatedAt},
		{SearchQuery{Page: -5, PageSize: -1}, 1, DefaultPageSize, SortByCreatedAt},
		{SearchQuery{PageSize: 1000}, 1, MaxPageSize, SortByCreatedAt},
		{SearchQuery{Page: 3, PageSize: 50, SortBy: "title"}, 3, 50, SortByTitle},
		{SearchQuery{SortBy: "bogus"}, 1, DefaultPageSize, SortByCreatedAt}, // unknown → created_at, not a no-op
	}
	for i, c := range cases {
		q := c.in
		q.Normalize()
		if q.Page != c.wantPage || q.PageSize != c.wantSize || q.SortBy != c.wantSortBy {
			t.Fatalf("case %d: Normalize = {page:%d size:%d sortBy:%q}, want {%d %d %q}",
				i, q.Page, q.PageSize, q.SortBy, c.wantPage, c.wantSize, c.wantSortBy)
		}
	}
}

func TestSearchTextMatchesTitleSlugLabelCaseInsensitive(t *testing.T) {
	arts := []*artifactav1.Artifact{
		art("alpha", "Quarterly Report", artifactav1.Visibility_VISIBILITY_PRIVATE, "", base),
		{Slug: "beta", Title: "Notes", Label: "my-DECK", CreatedAt: timestamppb.New(base)},
		art("gamma-report", "misc", artifactav1.Visibility_VISIBILITY_PRIVATE, "", base),
	}
	// Title match, case-insensitive.
	if got, total := SearchArtifacts(arts, nil, SearchQuery{Text: "quarterly"}); total != 1 || got[0].GetSlug() != "alpha" {
		t.Fatalf("title match = %v (total %d), want [alpha]", slugs(got), total)
	}
	// Label match, case-insensitive.
	if got, _ := SearchArtifacts(arts, nil, SearchQuery{Text: "deck"}); len(got) != 1 || got[0].GetSlug() != "beta" {
		t.Fatalf("label match = %v, want [beta]", slugs(got))
	}
	// Slug match.
	if got, _ := SearchArtifacts(arts, nil, SearchQuery{Text: "REPORT"}); len(got) != 2 {
		t.Fatalf("slug/title match 'REPORT' = %v, want alpha+gamma-report", slugs(got))
	}
}

func TestSearchTextMatchesDescription(t *testing.T) {
	arts := []*artifactav1.Artifact{
		{Slug: "a", Title: "Untitled", Description: "Quarterly REVENUE analysis and forecast", Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE, CreatedAt: timestamppb.New(base)},
		{Slug: "b", Title: "Roadmap", Description: "engineering plan", Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE, CreatedAt: timestamppb.New(base)},
	}
	// A word that appears only in the description (case-insensitive) still matches.
	if got, total := SearchArtifacts(arts, nil, SearchQuery{Text: "revenue"}); total != 1 || got[0].GetSlug() != "a" {
		t.Fatalf("description match = %v (total %d), want [a]", slugs(got), total)
	}
}

func TestSearchEmailMatchesOwnerAndOwnGranteeOnly(t *testing.T) {
	arts := []*artifactav1.Artifact{
		art("mine", "t", artifactav1.Visibility_VISIBILITY_PRIVATE, "alice@x.co", base),    // owner_email match
		art("theirs", "t", artifactav1.Visibility_VISIBILITY_ORG, "bob@x.co", base),        // org, someone else's
		art("myshare", "t", artifactav1.Visibility_VISIBILITY_INVITED, "alice@x.co", base), // mine, shared to carol
	}
	// grants supplied are ONLY the viewer's own artifacts' grants (handler/store guarantees this).
	ownGrants := map[string][]*artifactav1.Grant{
		"myshare": {{Slug: "myshare", GranteeEmail: "carol@x.co"}},
	}

	// owner_email substring, case-insensitive.
	if got, _ := SearchArtifacts(arts, ownGrants, SearchQuery{Email: "ALICE@x.co"}); !eq(slugs(got), []string{"mine", "myshare"}) {
		t.Fatalf("owner_email match = %v, want [mine myshare] (sorted by slug tie-break)", slugs(got))
	}
	// grantee_email on the viewer's own artifact matches.
	if got, _ := SearchArtifacts(arts, ownGrants, SearchQuery{Email: "carol@"}); !eq(slugs(got), []string{"myshare"}) {
		t.Fatalf("grantee match = %v, want [myshare]", slugs(got))
	}
	// bob's owner_email matches the org artifact (it IS in the visible set), fine.
	if got, _ := SearchArtifacts(arts, ownGrants, SearchQuery{Email: "bob@"}); !eq(slugs(got), []string{"theirs"}) {
		t.Fatalf("owner_email bob = %v, want [theirs]", slugs(got))
	}
}

// The pure function must NOT match a grantee email that isn't in ownGrantsBySlug —
// this is the domain-level half of the grant-list leak defense (ADR-0025). If the
// store ever passed a foreign artifact's grants, that would be the bug; here we
// assert that with no own-grants for a slug, only owner_email can match it.
func TestSearchEmailNeverMatchesForeignGrantee(t *testing.T) {
	arts := []*artifactav1.Artifact{
		art("foreign", "t", artifactav1.Visibility_VISIBILITY_ORG, "owner@x.co", base),
	}
	// carol is a grantee of "foreign", but the caller does NOT own it, so the store
	// passes no grants for it. Searching carol's email must not surface it.
	if got, total := SearchArtifacts(arts, nil, SearchQuery{Email: "carol@x.co"}); total != 0 || len(got) != 0 {
		t.Fatalf("foreign grantee email leaked: %v (total %d), want none", slugs(got), total)
	}
}

func TestSearchVisibilityFilter(t *testing.T) {
	arts := []*artifactav1.Artifact{
		art("p", "t", artifactav1.Visibility_VISIBILITY_PRIVATE, "", base),
		art("o", "t", artifactav1.Visibility_VISIBILITY_ORG, "", base),
	}
	org := artifactav1.Visibility_VISIBILITY_ORG
	if got, _ := SearchArtifacts(arts, nil, SearchQuery{Visibility: &org}); !eq(slugs(got), []string{"o"}) {
		t.Fatalf("visibility filter = %v, want [o]", slugs(got))
	}
}

func TestSearchDedupsBySlug(t *testing.T) {
	a := art("dup", "t", artifactav1.Visibility_VISIBILITY_ORG, "", base)
	arts := []*artifactav1.Artifact{a, a} // same slug reaches the set via two arms
	got, total := SearchArtifacts(arts, nil, SearchQuery{})
	if total != 1 || len(got) != 1 {
		t.Fatalf("dedup: got %d rows total %d, want 1/1", len(got), total)
	}
}

func TestSearchSortAndTieBreak(t *testing.T) {
	arts := []*artifactav1.Artifact{
		art("b", "t", artifactav1.Visibility_VISIBILITY_PRIVATE, "", base.Add(2*time.Hour)),
		art("a", "t", artifactav1.Visibility_VISIBILITY_PRIVATE, "", base.Add(2*time.Hour)), // same time as b → slug tie-break
		art("c", "t", artifactav1.Visibility_VISIBILITY_PRIVATE, "", base),                  // oldest
	}
	// Default: created_at DESC, slug ASC tie-break.
	got, _ := SearchArtifacts(arts, nil, SearchQuery{SortDesc: true})
	if !eq(slugs(got), []string{"a", "b", "c"}) {
		t.Fatalf("created_at desc = %v, want [a b c] (a,b tie by slug, c oldest last)", slugs(got))
	}
	// Ascending.
	got, _ = SearchArtifacts(arts, nil, SearchQuery{SortDesc: false})
	if !eq(slugs(got), []string{"c", "a", "b"}) {
		t.Fatalf("created_at asc = %v, want [c a b]", slugs(got))
	}
	// Title sort with slug tie-break (all titles equal → slug asc).
	got, _ = SearchArtifacts(arts, nil, SearchQuery{SortBy: SortByTitle})
	if !eq(slugs(got), []string{"a", "b", "c"}) {
		t.Fatalf("title sort tie-break = %v, want [a b c]", slugs(got))
	}
}

func TestSearchPagination(t *testing.T) {
	var arts []*artifactav1.Artifact
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		arts = append(arts, art(s, "t", artifactav1.Visibility_VISIBILITY_PRIVATE, "", base))
	}
	// page 1, size 2 → a,b ; total 5
	got, total := SearchArtifacts(arts, nil, SearchQuery{Page: 1, PageSize: 2})
	if total != 5 || !eq(slugs(got), []string{"a", "b"}) {
		t.Fatalf("page1 = %v total %d, want [a b] total 5", slugs(got), total)
	}
	// page 3, size 2 → e (last, partial)
	got, total = SearchArtifacts(arts, nil, SearchQuery{Page: 3, PageSize: 2})
	if total != 5 || !eq(slugs(got), []string{"e"}) {
		t.Fatalf("page3 = %v total %d, want [e] total 5", slugs(got), total)
	}
	// page past end → empty, total still 5
	got, total = SearchArtifacts(arts, nil, SearchQuery{Page: 99, PageSize: 2})
	if total != 5 || len(got) != 0 {
		t.Fatalf("page past end = %v total %d, want [] total 5", slugs(got), total)
	}
}
