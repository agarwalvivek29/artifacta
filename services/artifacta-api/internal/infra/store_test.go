package infra

import (
	"testing"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestFileStoreRoundTrip verifies that metadata persisted by one FileStore is
// reloaded correctly by a fresh FileStore over the same directory — i.e. the
// atomic writeJSON path produces a readable file.
func TestFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()

	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	art := &artifactav1.Artifact{
		Slug:        "hello-world",
		OwnerSub:    "user-123",
		Title:       "Hello World",
		Visibility:  artifactav1.Visibility_VISIBILITY_PRIVATE,
		ContentType: "text/html",
	}
	if err := s.PutArtifact(art); err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	grant := &artifactav1.Grant{
		Slug:       "hello-world",
		GranteeSub: "user-456",
		GrantedBy:  "user-123",
	}
	if err := s.AddGrant(grant); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}

	// Fresh store over the same dir must reload both records from disk.
	reloaded, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("reload NewFileStore: %v", err)
	}

	got, ok, err := reloaded.GetArtifact("hello-world")
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if !ok {
		t.Fatal("artifact not found after reload")
	}
	if got.GetOwnerSub() != "user-123" || got.GetTitle() != "Hello World" ||
		got.GetVisibility() != artifactav1.Visibility_VISIBILITY_PRIVATE ||
		got.GetContentType() != "text/html" {
		t.Fatalf("artifact round-trip mismatch: %+v", got)
	}

	grants, err := reloaded.Grants("hello-world")
	if err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("expected 1 grant after reload, got %d", len(grants))
	}
	if grants[0].GetGranteeSub() != "user-456" || grants[0].GetGrantedBy() != "user-123" {
		t.Fatalf("grant round-trip mismatch: %+v", grants[0])
	}
}

// TestVersionRoundTrip verifies the versioning store path: two appended
// versions come back ascending by n, GetVersion resolves a specific version,
// and everything reloads from disk into a fresh FileStore (ADR-0013).
func TestVersionRoundTrip(t *testing.T) {
	dir := t.TempDir()

	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Append out of order to prove Versions sorts ascending by n.
	if err := s.AddVersion(&artifactav1.ArtifactVersion{
		Slug: "doc", N: 2, ContentType: "text/html", CreatedBy: "user-1", Note: "second",
	}); err != nil {
		t.Fatalf("AddVersion 2: %v", err)
	}
	if err := s.AddVersion(&artifactav1.ArtifactVersion{
		Slug: "doc", N: 1, ContentType: "text/plain", CreatedBy: "user-1", Note: "first",
	}); err != nil {
		t.Fatalf("AddVersion 1: %v", err)
	}
	// A version for an unrelated slug must not leak into doc's list.
	if err := s.AddVersion(&artifactav1.ArtifactVersion{Slug: "other", N: 1, CreatedBy: "user-2"}); err != nil {
		t.Fatalf("AddVersion other: %v", err)
	}

	// Fresh store over the same dir must reload the versions from disk.
	reloaded, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("reload NewFileStore: %v", err)
	}

	got, err := reloaded.Versions("doc")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 versions for doc, got %d", len(got))
	}
	if got[0].GetN() != 1 || got[1].GetN() != 2 {
		t.Fatalf("versions not ascending by n: got n=%d then n=%d", got[0].GetN(), got[1].GetN())
	}
	if got[0].GetNote() != "first" || got[1].GetNote() != "second" {
		t.Fatalf("version notes mismatch: %q, %q", got[0].GetNote(), got[1].GetNote())
	}

	v1, ok, err := reloaded.GetVersion("doc", 1)
	if err != nil || !ok {
		t.Fatalf("GetVersion(doc,1): ok=%v err=%v", ok, err)
	}
	if v1.GetContentType() != "text/plain" || v1.GetCreatedBy() != "user-1" {
		t.Fatalf("GetVersion(doc,1) mismatch: %+v", v1)
	}

	// A version that does not exist is a clean miss, not an error.
	if _, ok, err := reloaded.GetVersion("doc", 3); err != nil || ok {
		t.Fatalf("GetVersion(doc,3): expected miss, ok=%v err=%v", ok, err)
	}
}

// TestCommentRoundTrip verifies the comments store path (ADR-0014): comments
// pinned to different versions round-trip, Comments returns them ascending by
// created_at, ResolveComment flips resolved for the right id only, and all of it
// reloads from disk into a fresh FileStore.
func TestCommentRoundTrip(t *testing.T) {
	dir := t.TempDir()

	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	base := time.Now().UTC()
	// Insert out of chronological order to prove Comments sorts by created_at.
	seed := []*artifactav1.Comment{
		{Id: "c-v2", Slug: "doc", Version: 2, AuthorSub: "u1", AuthorEmail: "u1@x", Body: "on v2", CreatedAt: timestamppb.New(base.Add(2 * time.Minute))},
		{Id: "c-v1-a", Slug: "doc", Version: 1, AuthorSub: "u2", AuthorEmail: "u2@x", Body: "first on v1", CreatedAt: timestamppb.New(base)},
		{Id: "c-v1-b", Slug: "doc", Version: 1, AuthorSub: "u1", AuthorEmail: "u1@x", Body: "second on v1", CreatedAt: timestamppb.New(base.Add(1 * time.Minute))},
		{Id: "c-other", Slug: "other", Version: 1, AuthorSub: "u1", AuthorEmail: "u1@x", Body: "elsewhere", CreatedAt: timestamppb.New(base)},
	}
	for _, c := range seed {
		if err := s.AddComment(c); err != nil {
			t.Fatalf("AddComment(%s): %v", c.GetId(), err)
		}
	}

	// Resolve one comment; the miss case must report found=false without error.
	if found, err := s.ResolveComment("doc", "c-v1-a"); err != nil || !found {
		t.Fatalf("ResolveComment(doc,c-v1-a): found=%v err=%v", found, err)
	}
	if found, err := s.ResolveComment("doc", "nope"); err != nil || found {
		t.Fatalf("ResolveComment(doc,nope): expected miss, found=%v err=%v", found, err)
	}

	// Fresh store over the same dir must reload the comments from disk.
	reloaded, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("reload NewFileStore: %v", err)
	}

	got, err := reloaded.Comments("doc")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 comments for doc, got %d", len(got))
	}
	// Ascending by created_at, and 'other' never leaked in.
	wantOrder := []string{"c-v1-a", "c-v1-b", "c-v2"}
	for i, id := range wantOrder {
		if got[i].GetId() != id {
			t.Fatalf("comment order[%d]: got %q, want %q", i, got[i].GetId(), id)
		}
	}
	// The resolved flag persisted for the right comment only.
	for _, c := range got {
		want := c.GetId() == "c-v1-a"
		if c.GetResolved() != want {
			t.Fatalf("resolved(%s): got %v, want %v", c.GetId(), c.GetResolved(), want)
		}
	}
}

// TestListByGrantee verifies the grants→artifacts join: a grantee sees a
// granted artifact, a non-grantee sees nothing, and multiple grants for the
// same slug dedupe to a single artifact.
func TestListByGrantee(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	art := &artifactav1.Artifact{
		Slug:       "shared-doc",
		OwnerSub:   "owner-1",
		Visibility: artifactav1.Visibility_VISIBILITY_INVITED,
	}
	if err := s.PutArtifact(art); err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	// Two grants to the same grantee on the same slug — must dedupe.
	for i := 0; i < 2; i++ {
		if err := s.AddGrant(&artifactav1.Grant{Slug: "shared-doc", GranteeSub: "grantee-1", GrantedBy: "owner-1"}); err != nil {
			t.Fatalf("AddGrant: %v", err)
		}
	}
	// A dangling grant to a nonexistent artifact must be skipped.
	if err := s.AddGrant(&artifactav1.Grant{Slug: "ghost", GranteeSub: "grantee-1", GrantedBy: "owner-1"}); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}

	got, err := s.ListByGrantee("grantee-1")
	if err != nil {
		t.Fatalf("ListByGrantee: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("grantee expected 1 deduped artifact, got %d", len(got))
	}
	if got[0].GetSlug() != "shared-doc" {
		t.Fatalf("expected shared-doc, got %q", got[0].GetSlug())
	}

	// A user with no grants sees nothing.
	none, err := s.ListByGrantee("stranger")
	if err != nil {
		t.Fatalf("ListByGrantee stranger: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("non-grantee expected 0 artifacts, got %d", len(none))
	}
}

// TestListByVisibility verifies the Org-tab query returns only artifacts whose
// visibility matches, regardless of owner.
func TestListByVisibility(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	seed := []*artifactav1.Artifact{
		{Slug: "org-a", OwnerSub: "owner-1", Visibility: artifactav1.Visibility_VISIBILITY_ORG},
		{Slug: "org-b", OwnerSub: "owner-2", Visibility: artifactav1.Visibility_VISIBILITY_ORG},
		{Slug: "priv", OwnerSub: "owner-1", Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE},
		{Slug: "inv", OwnerSub: "owner-1", Visibility: artifactav1.Visibility_VISIBILITY_INVITED},
	}
	for _, a := range seed {
		if err := s.PutArtifact(a); err != nil {
			t.Fatalf("PutArtifact(%q): %v", a.GetSlug(), err)
		}
	}

	org, err := s.ListByVisibility(artifactav1.Visibility_VISIBILITY_ORG)
	if err != nil {
		t.Fatalf("ListByVisibility: %v", err)
	}
	if len(org) != 2 {
		t.Fatalf("expected 2 org artifacts, got %d", len(org))
	}
	for _, a := range org {
		if a.GetVisibility() != artifactav1.Visibility_VISIBILITY_ORG {
			t.Fatalf("non-org artifact leaked: %q (%v)", a.GetSlug(), a.GetVisibility())
		}
	}

	priv, err := s.ListByVisibility(artifactav1.Visibility_VISIBILITY_PRIVATE)
	if err != nil {
		t.Fatalf("ListByVisibility private: %v", err)
	}
	if len(priv) != 1 || priv[0].GetSlug() != "priv" {
		t.Fatalf("expected only 'priv' as private, got %v", priv)
	}
}
