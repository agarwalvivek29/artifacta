package infra

import (
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

func putArt(t *testing.T, s *FileStore, slug string) {
	t.Helper()
	if err := s.PutArtifact(&artifactav1.Artifact{Slug: slug, OwnerSub: "o", LatestVersion: 1}); err != nil {
		t.Fatalf("PutArtifact(%s): %v", slug, err)
	}
}

func TestSetLabel_uniquenessAndResolve(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	putArt(t, s, "slug1")
	putArt(t, s, "slug2")

	// Claim a label for slug1.
	if ok, err := s.SetLabel("slug1", "myapp"); err != nil || !ok {
		t.Fatalf("SetLabel(slug1, myapp) = %v, %v; want true, nil", ok, err)
	}
	if got, ok, _ := s.SlugForLabel("myapp"); !ok || got != "slug1" {
		t.Fatalf("SlugForLabel(myapp) = %q, %v; want slug1, true", got, ok)
	}

	// Same label for a different artifact is rejected (no write).
	if ok, err := s.SetLabel("slug2", "myapp"); err != nil || ok {
		t.Fatalf("SetLabel(slug2, myapp) = %v, %v; want false, nil (taken)", ok, err)
	}
	if got, _, _ := s.SlugForLabel("myapp"); got != "slug1" {
		t.Fatalf("after rejected claim, SlugForLabel(myapp) = %q; want slug1 (unchanged)", got)
	}

	// Re-claiming the same label for the same artifact is idempotent.
	if ok, err := s.SetLabel("slug1", "myapp"); err != nil || !ok {
		t.Fatalf("idempotent SetLabel = %v, %v; want true, nil", ok, err)
	}

	// Setting a NEW label releases the old one.
	if ok, _ := s.SetLabel("slug1", "renamed"); !ok {
		t.Fatal("SetLabel(slug1, renamed) = false; want true")
	}
	if _, ok, _ := s.SlugForLabel("myapp"); ok {
		t.Error("old label myapp still resolves after rename; want released")
	}
	if got, ok, _ := s.SlugForLabel("renamed"); !ok || got != "slug1" {
		t.Errorf("SlugForLabel(renamed) = %q, %v; want slug1, true", got, ok)
	}

	// Unknown slug can't claim a label.
	if ok, _ := s.SetLabel("nope", "free"); ok {
		t.Error("SetLabel(nonexistent slug) = true; want false")
	}
}

// The label index is rebuilt from the persisted artifacts on load, so it survives
// a restart without a separate index file drifting out of sync.
func TestSetLabel_persistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	putArt(t, s, "slugX")
	if ok, err := s.SetLabel("slugX", "keepme"); err != nil || !ok {
		t.Fatalf("SetLabel: %v, %v", ok, err)
	}

	reloaded, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if got, ok, _ := reloaded.SlugForLabel("keepme"); !ok || got != "slugX" {
		t.Fatalf("after reload SlugForLabel(keepme) = %q, %v; want slugX, true", got, ok)
	}
}
