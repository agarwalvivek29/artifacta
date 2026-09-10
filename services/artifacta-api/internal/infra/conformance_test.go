package infra_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/api"
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
}

func TestFileStore_Conformance(t *testing.T) {
	st, err := infra.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runStoreConformance(t, st)
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
}

func containsSlug(arts []*artifactav1.Artifact, slug string) bool {
	for _, a := range arts {
		if a.GetSlug() == slug {
			return true
		}
	}
	return false
}
