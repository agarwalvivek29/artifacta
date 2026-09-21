package infra_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/api"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The metadata store has two interchangeable backends (file, postgres). This
// suite runs the identical behaviour checks against whichever store is passed,
// so "works consistently on both" is verified, not asserted. It uses a unique
// per-run key prefix and only relative assertions, so it is safe to re-run
// against a non-empty database.
func runStoreConformance(t *testing.T, st api.Store) {
	t.Helper()
	k := fmt.Sprintf("t%d", time.Now().UnixNano()) // unique prefix for this run
	slug := k + "art"
	owner := k + "owner"

	// PutArtifact / GetArtifact roundtrip.
	art := &artifactav1.Artifact{
		Slug: slug, OwnerSub: owner, Title: "Q3", Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE,
		ContentType: "text/html", LatestVersion: 1, CreatedAt: timestamppb.Now(),
	}
	if err := st.PutArtifact(art); err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	got, ok, err := st.GetArtifact(slug)
	if err != nil || !ok {
		t.Fatalf("GetArtifact: ok=%v err=%v", ok, err)
	}
	if got.GetOwnerSub() != owner || got.GetTitle() != "Q3" || got.GetLatestVersion() != 1 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if _, ok, _ := st.GetArtifact(k + "nope"); ok {
		t.Fatal("GetArtifact of unknown slug returned ok")
	}

	// Listings include our artifact.
	owned, err := st.ListByOwner(owner)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSlug(owned, slug) {
		t.Fatal("ListByOwner missing artifact")
	}

	// Labels: claim, uniqueness, idempotency, release, lookup.
	label := k + "lbl"
	if okc, err := st.SetLabel(slug, label); err != nil || !okc {
		t.Fatalf("SetLabel claim = %v,%v want true", okc, err)
	}
	if got, ok, _ := st.SlugForLabel(label); !ok || got != slug {
		t.Fatalf("SlugForLabel = %q,%v want %s,true", got, ok, slug)
	}
	if okc, _ := st.SetLabel(slug, label); !okc {
		t.Fatal("re-claim same label by same slug should be idempotent true")
	}
	// A second artifact cannot take the label.
	slug2 := k + "art2"
	if err := st.PutArtifact(&artifactav1.Artifact{Slug: slug2, OwnerSub: owner, LatestVersion: 1, CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	if okc, err := st.SetLabel(slug2, label); err != nil || okc {
		t.Fatalf("SetLabel of taken label = %v,%v want false (no double-assign)", okc, err)
	}
	// Renaming releases the old label.
	if okc, _ := st.SetLabel(slug, label+"2"); !okc {
		t.Fatal("rename claim failed")
	}
	if _, ok, _ := st.SlugForLabel(label); ok {
		t.Fatal("old label still resolves after rename")
	}
	if okc, _ := st.SetLabel(k+"ghost", label); okc {
		t.Fatal("SetLabel on unknown slug returned true")
	}

	// Grants: add by email, list, remove by email; add by sub, remove by sub.
	if err := st.AddGrant(&artifactav1.Grant{Slug: slug, GranteeEmail: "sam@x.co", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddGrant(&artifactav1.Grant{Slug: slug, GranteeSub: owner + "b", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	g, err := st.Grants(slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 2 {
		t.Fatalf("Grants = %d want 2", len(g))
	}
	if removed, err := st.RemoveGrant(slug, "SAM@x.co"); err != nil || !removed { // case-insensitive email
		t.Fatalf("RemoveGrant email = %v,%v want true", removed, err)
	}
	if removed, _ := st.RemoveGrant(slug, "nobody@x.co"); removed {
		t.Fatal("RemoveGrant of unknown returned true")
	}
	if g, err := st.Grants(slug); err != nil || len(g) != 1 {
		t.Fatalf("after remove Grants = %d (err %v) want 1", len(g), err)
	}
	// ListByGrantee is subject-based.
	shared, err := st.ListByGrantee(owner + "b")
	if err != nil {
		t.Fatal(err)
	}
	if !containsSlug(shared, slug) {
		t.Fatal("ListByGrantee missing artifact for granted subject")
	}

	// ListByVisibility backs the dashboard's Org tab. Use two dedicated artifacts —
	// one ORG, one explicitly PRIVATE — with relative assertions so this holds on a
	// shared, non-empty store: each visibility's listing must include its own
	// artifact and exclude the other's (visibility is the filter, and it is exact —
	// e.g. the PRIVATE listing must not sweep in the ORG one).
	slugOrg, slugPriv := k+"org", k+"priv"
	if err := st.PutArtifact(&artifactav1.Artifact{
		Slug: slugOrg, OwnerSub: owner, Visibility: artifactav1.Visibility_VISIBILITY_ORG,
		LatestVersion: 1, CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutArtifact(&artifactav1.Artifact{
		Slug: slugPriv, OwnerSub: owner, Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE,
		LatestVersion: 1, CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	orgList, err := st.ListByVisibility(artifactav1.Visibility_VISIBILITY_ORG)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSlug(orgList, slugOrg) {
		t.Fatal("ListByVisibility(ORG) missing the ORG artifact")
	}
	if containsSlug(orgList, slugPriv) {
		t.Fatal("ListByVisibility(ORG) leaked a PRIVATE artifact")
	}
	privList, err := st.ListByVisibility(artifactav1.Visibility_VISIBILITY_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSlug(privList, slugPriv) {
		t.Fatal("ListByVisibility(PRIVATE) missing the PRIVATE artifact")
	}
	if containsSlug(privList, slugOrg) {
		t.Fatal("ListByVisibility(PRIVATE) leaked an ORG artifact")
	}

	// AllArtifacts (ADR-0026): the full, unscoped enumeration migration drives from.
	// Relative assertion (contains our three) so it holds on a shared, non-empty store.
	all, err := st.AllArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{slug, slugOrg, slugPriv} {
		if !containsSlug(all, want) {
			t.Fatalf("AllArtifacts missing %s", want)
		}
	}

	// Search (ADR-0025): identical behaviour on both backends. Uses a unique token
	// so assertions are absolute even on a shared, non-empty store. Covers the text
	// filter, owner_email match, owner-scoped grantee match (the leak defense),
	// visibility filter, and pagination.
	tok := k + "srch"
	searcher := k + "searcher"
	for i, name := range []string{"a", "b", "c"} {
		if err := st.PutArtifact(&artifactav1.Artifact{
			Slug: tok + name, OwnerSub: searcher, OwnerEmail: searcher + "@x.co",
			Title: tok + " " + name, Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE,
			LatestVersion: 1, CreatedAt: timestamppb.New(time.Unix(int64(1_700_000_000+i), 0)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A grantee on the searcher's OWN artifact (matchable) and a foreign org
	// artifact with a grantee bearing the same token (must NOT be matchable).
	if err := st.AddGrant(&artifactav1.Grant{Slug: tok + "a", GranteeEmail: tok + "own@x.co"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutArtifact(&artifactav1.Artifact{
		Slug: tok + "foreign", OwnerSub: k + "stranger", OwnerEmail: k + "stranger@x.co",
		Title: "unrelated", Visibility: artifactav1.Visibility_VISIBILITY_ORG,
		LatestVersion: 1, CreatedAt: timestamppb.New(time.Unix(1_700_000_009, 0)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddGrant(&artifactav1.Grant{Slug: tok + "foreign", GranteeEmail: tok + "foreign@x.co"}); err != nil {
		t.Fatal(err)
	}

	mkQ := func(text, email string) domain.SearchQuery {
		q := domain.SearchQuery{ViewerSub: searcher, ViewerEmail: searcher + "@x.co", Text: text, Email: email}
		q.Normalize()
		return q
	}
	// Text filter: the three token artifacts (searcher owns them, so they are visible).
	if items, total, err := st.SearchArtifacts(mkQ(tok+" ", "")); err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("SearchArtifacts text = %d items/%d total (err %v), want 3/3", len(items), total, err)
	}
	// owner_email match (unique prefix → exactly the searcher's three).
	if _, total, err := st.SearchArtifacts(mkQ("", searcher+"@x.co")); err != nil || total != 3 {
		t.Fatalf("SearchArtifacts owner_email total = %d (err %v), want 3", total, err)
	}
	// grantee_email on the searcher's OWN artifact matches exactly one.
	if items, total, err := st.SearchArtifacts(mkQ("", tok+"own@x.co")); err != nil || total != 1 || items[0].GetSlug() != tok+"a" {
		t.Fatalf("SearchArtifacts own grantee = %v/%d (err %v), want [%sa]/1", func() []string {
			var s []string
			for _, a := range items {
				s = append(s, a.GetSlug())
			}
			return s
		}(), total, err, tok)
	}
	// CRITICAL: a foreign org artifact's grantee email must NOT be matchable even
	// though the searcher can see the artifact (grant-list leak defense, ADR-0025).
	if _, total, err := st.SearchArtifacts(mkQ("", tok+"foreign@x.co")); err != nil || total != 0 {
		t.Fatalf("SearchArtifacts foreign grantee total = %d (err %v), want 0 (no leak)", total, err)
	}
	// Pagination: token text, page_size 2 → page 1 has 2, page 2 has 1, total 3 both.
	q1 := mkQ(tok+" ", "")
	q1.PageSize = 2
	q1.Page = 1
	if items, total, err := st.SearchArtifacts(q1); err != nil || total != 3 || len(items) != 2 {
		t.Fatalf("SearchArtifacts page1 = %d items/%d total (err %v), want 2/3", len(items), total, err)
	}
	q2 := q1
	q2.Page = 2
	if items, total, err := st.SearchArtifacts(q2); err != nil || total != 3 || len(items) != 1 {
		t.Fatalf("SearchArtifacts page2 = %d items/%d total (err %v), want 1/3", len(items), total, err)
	}

	// Versions: append + ordered list + get.
	for _, n := range []int32{1, 2, 3} {
		if err := st.AddVersion(&artifactav1.ArtifactVersion{Slug: slug, N: n, ContentType: "text/html", CreatedAt: timestamppb.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	vs, err := st.Versions(slug)
	if err != nil || len(vs) != 3 || vs[0].GetN() != 1 || vs[2].GetN() != 3 {
		t.Fatalf("Versions = %v (err %v), want [1 2 3]", vs, err)
	}
	if v, ok, _ := st.GetVersion(slug, 2); !ok || v.GetN() != 2 {
		t.Fatal("GetVersion(2) failed")
	}
	if _, ok, _ := st.GetVersion(slug, 99); ok {
		t.Fatal("GetVersion(99) returned ok")
	}

	// Comments: add + list + resolve.
	cid := k + "c1"
	if err := st.AddComment(&artifactav1.Comment{Id: cid, Slug: slug, Version: 1, Body: "hi", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	cs, err := st.Comments(slug)
	if err != nil || len(cs) != 1 || cs[0].GetBody() != "hi" || cs[0].GetResolved() {
		t.Fatalf("Comments = %v (err %v)", cs, err)
	}
	if found, err := st.ResolveComment(slug, cid); err != nil || !found {
		t.Fatalf("ResolveComment = %v,%v want true", found, err)
	}
	cs, _ = st.Comments(slug)
	if !cs[0].GetResolved() {
		t.Fatal("comment not resolved after ResolveComment")
	}

	// Audit hash chain: seq increments by 1 and prev_hash links (relative, so it
	// holds even on a non-empty store).
	e1 := &artifactav1.AuditEvent{Ts: timestamppb.Now(), Slug: slug, Action: artifactav1.AuditAction_AUDIT_ACTION_PUBLISH, Allowed: true}
	if err := st.Append(e1); err != nil {
		t.Fatal(err)
	}
	e2 := &artifactav1.AuditEvent{Ts: timestamppb.Now(), Slug: slug, Action: artifactav1.AuditAction_AUDIT_ACTION_VIEW, Allowed: true}
	if err := st.Append(e2); err != nil {
		t.Fatal(err)
	}
	if e2.GetSeq() != e1.GetSeq()+1 {
		t.Fatalf("audit seq did not increment: %d then %d", e1.GetSeq(), e2.GetSeq())
	}
	if e2.GetPrevHash() != e1.GetHash() || e2.GetHash() == "" {
		t.Fatal("audit chain not linked")
	}
	// AuditEvents + VerifyChain must agree the persisted chain is intact (this is
	// what `artifacta audit verify` runs, on either backend).
	events, err := st.AuditEvents()
	if err != nil {
		t.Fatalf("AuditEvents: %v", err)
	}
	if n, err := infra.VerifyChain(events); err != nil {
		t.Fatalf("VerifyChain over %d events: %v", n, err)
	}
}

// runAppendRawConformance verifies the verbatim, resumable audit-insert path
// (ADR-0026) on either backend. It uses a unique, high seq so it never collides with
// the contiguous seqs the rest of the suite writes via Append — safe on a shared DB.
func runAppendRawConformance(t *testing.T, st api.Store) {
	t.Helper()
	seq := time.Now().UnixNano() // unique + far above any Append-assigned seq
	ev := &artifactav1.AuditEvent{
		Seq: seq, PrevHash: "SENTINEL_PREV", Hash: "SENTINEL_HASH",
		Ts: timestamppb.Now(), Slug: "raw", Action: artifactav1.AuditAction_AUDIT_ACTION_VIEW, Allowed: true,
	}
	if err := st.AppendRaw(ev); err != nil {
		t.Fatalf("AppendRaw: %v", err)
	}
	// Verbatim: the stored row keeps the source seq/prev_hash/hash exactly (Append
	// would have overwritten them with computed values).
	events, err := st.AuditEvents()
	if err != nil {
		t.Fatal(err)
	}
	var got *artifactav1.AuditEvent
	for _, e := range events {
		if e.GetSeq() == seq {
			got = e
			break
		}
	}
	if got == nil {
		t.Fatalf("AppendRaw event seq %d not found", seq)
	}
	if got.GetHash() != "SENTINEL_HASH" || got.GetPrevHash() != "SENTINEL_PREV" {
		t.Fatalf("AppendRaw did not preserve verbatim: hash=%q prev=%q", got.GetHash(), got.GetPrevHash())
	}
	// Idempotent resume: same seq + identical hash is a no-op.
	if err := st.AppendRaw(ev); err != nil {
		t.Fatalf("AppendRaw re-insert (identical) should be a no-op, got: %v", err)
	}
	// Divergence: same seq + different hash is refused (never overwritten).
	diverged := &artifactav1.AuditEvent{
		Seq: seq, PrevHash: "SENTINEL_PREV", Hash: "DIFFERENT_HASH",
		Ts: timestamppb.Now(), Slug: "raw", Action: artifactav1.AuditAction_AUDIT_ACTION_VIEW, Allowed: true,
	}
	if err := st.AppendRaw(diverged); err == nil {
		t.Fatal("AppendRaw of a divergent hash at the same seq should error, got nil")
	}
}

func TestFileStore_Conformance(t *testing.T) {
	st, err := infra.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runStoreConformance(t, st)
	runAppendRawConformance(t, st)
}

// Runs only when ARTIFACTA_TEST_DATABASE_URL points at a reachable postgres
// (e.g. an ephemeral cluster). CI without a database skips it.
func TestSQLStore_Conformance(t *testing.T) {
	dsn := os.Getenv("ARTIFACTA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ARTIFACTA_TEST_DATABASE_URL to run the postgres conformance suite")
	}
	st, err := infra.NewSQLStore(dsn)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	runStoreConformance(t, st)
	runAppendRawConformance(t, st)
}

func containsSlug(arts []*artifactav1.Artifact, slug string) bool {
	for _, a := range arts {
		if a.GetSlug() == slug {
			return true
		}
	}
	return false
}
