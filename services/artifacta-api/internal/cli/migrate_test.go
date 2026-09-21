package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/api"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// migrate targets Postgres + S3 in production, but runMigrate is written against the
// Store/Blob interfaces, so we exercise the whole copy path with file-backed stores
// and blobs on both ends. That covers enumeration, per-entity existence checks, the
// grant email-only dedupe (the C3 fix), verbatim audit + completeness gate, blob
// checksum, and resumability — no live Postgres/S3 needed. The Postgres/S3-specific
// AllArtifacts/AppendRaw behaviour is covered by the store conformance suite.

func newFileSide(t *testing.T) (api.Store, api.Blob) {
	t.Helper()
	st, err := infra.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	bl, err := infra.NewBlobFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewBlobFS: %v", err)
	}
	return st, bl
}

func putBlob(t *testing.T, b api.Blob, slug string, n int32, data string) {
	t.Helper()
	if err := b.Put(slug, n, bytes.NewReader([]byte(data))); err != nil {
		t.Fatalf("put blob %s v%d: %v", slug, n, err)
	}
}

func readBlob(t *testing.T, b api.Blob, slug string, n int32) string {
	t.Helper()
	rc, err := b.Get(slug, n)
	if err != nil {
		t.Fatalf("get blob %s v%d: %v", slug, n, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read blob %s v%d: %v", slug, n, err)
	}
	return string(data)
}

// seedSource populates a source with one artifact (2 versions + blobs), three grants
// (two email-only invites with empty grantee_sub + one by subject), a comment, and a
// valid audit chain.
func seedSource(t *testing.T, st api.Store, bl api.Blob) {
	t.Helper()
	if err := st.PutArtifact(&artifactav1.Artifact{
		Slug: "alpha", OwnerSub: "owner-1", OwnerEmail: "owner@x.co", Title: "Alpha",
		Visibility: artifactav1.Visibility_VISIBILITY_INVITED, LatestVersion: 2, CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int32{1, 2} {
		if err := st.AddVersion(&artifactav1.ArtifactVersion{Slug: "alpha", N: n, ContentType: "text/html", CreatedAt: timestamppb.Now()}); err != nil {
			t.Fatal(err)
		}
		putBlob(t, bl, "alpha", n, "bundle-alpha-v"+string(rune('0'+n)))
	}
	// Two email-only invites (empty grantee_sub) + one by subject. The C3 fix: these
	// must NOT collapse onto (slug, "").
	for _, email := range []string{"a@x.co", "b@x.co"} {
		if err := st.AddGrant(&artifactav1.Grant{Slug: "alpha", GranteeEmail: email, CreatedAt: timestamppb.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddGrant(&artifactav1.Grant{Slug: "alpha", GranteeSub: "friend-1", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddComment(&artifactav1.Comment{Id: "c1", Slug: "alpha", Version: 1, Body: "nice", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := st.Append(&artifactav1.AuditEvent{Ts: timestamppb.Now(), Slug: "alpha", Action: artifactav1.AuditAction_AUDIT_ACTION_VIEW, Allowed: true}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunMigrate_CopiesEverythingAndIsResumable(t *testing.T) {
	srcStore, srcBlob := newFileSide(t)
	dstStore, dstBlob := newFileSide(t)
	seedSource(t, srcStore, srcBlob)

	if err := runMigrate(srcStore, srcBlob, dstStore, dstBlob); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}

	// Artifact + versions.
	if _, ok, _ := dstStore.GetArtifact("alpha"); !ok {
		t.Fatal("artifact not migrated")
	}
	if vs, _ := dstStore.Versions("alpha"); len(vs) != 2 {
		t.Fatalf("versions = %d want 2", len(vs))
	}
	// Grants: all three survive — the two email-only invites are NOT collapsed (C3).
	if g, _ := dstStore.Grants("alpha"); len(g) != 3 {
		t.Fatalf("grants = %d want 3 (two email invites must not collapse)", len(g))
	}
	if c, _ := dstStore.Comments("alpha"); len(c) != 1 {
		t.Fatalf("comments = %d want 1", len(c))
	}
	// Blobs copied byte-for-byte.
	for _, n := range []int32{1, 2} {
		if got, want := readBlob(t, dstBlob, "alpha", n), readBlob(t, srcBlob, "alpha", n); got != want {
			t.Fatalf("blob v%d = %q want %q", n, got, want)
		}
	}
	// Audit: verbatim + complete.
	src, _ := srcStore.AuditEvents()
	dst, _ := dstStore.AuditEvents()
	if len(dst) != len(src) || len(dst) != 3 {
		t.Fatalf("audit events = %d want %d(=3)", len(dst), len(src))
	}
	for i := range src {
		if dst[i].GetSeq() != src[i].GetSeq() || dst[i].GetHash() != src[i].GetHash() || dst[i].GetPrevHash() != src[i].GetPrevHash() {
			t.Fatalf("audit event %d not verbatim: src seq=%d hash=%s / dst seq=%d hash=%s",
				i, src[i].GetSeq(), src[i].GetHash(), dst[i].GetSeq(), dst[i].GetHash())
		}
	}
	if n, err := infra.VerifyChain(dst); err != nil {
		t.Fatalf("dest chain does not verify (%d ok): %v", n, err)
	}

	// Resumability: an identical re-run copies nothing and does not duplicate.
	if err := runMigrate(srcStore, srcBlob, dstStore, dstBlob); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if g, _ := dstStore.Grants("alpha"); len(g) != 3 {
		t.Fatalf("after re-run grants = %d want 3 (no duplication)", len(g))
	}
	if vs, _ := dstStore.Versions("alpha"); len(vs) != 2 {
		t.Fatalf("after re-run versions = %d want 2", len(vs))
	}
	if dst2, _ := dstStore.AuditEvents(); len(dst2) != 3 {
		t.Fatalf("after re-run audit events = %d want 3 (no duplication)", len(dst2))
	}
}

func TestRunMigrate_ResumesMissingBlob(t *testing.T) {
	srcStore, srcBlob := newFileSide(t)
	dstStore, dstBlob := newFileSide(t)
	seedSource(t, srcStore, srcBlob)

	// First run copies everything.
	if err := runMigrate(srcStore, srcBlob, dstStore, dstBlob); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	// Simulate a lost destination blob (partial migration), then re-run.
	dstStore2, dstBlob2 := dstStore, dstBlob
	// Overwrite one dest blob with wrong bytes to force a re-copy on resume.
	putBlob(t, dstBlob2, "alpha", 1, "corrupted")
	if err := runMigrate(srcStore, srcBlob, dstStore2, dstBlob2); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got, want := readBlob(t, dstBlob2, "alpha", 1), readBlob(t, srcBlob, "alpha", 1); got != want {
		t.Fatalf("resume did not repair blob: got %q want %q", got, want)
	}
}

func TestRunMigrate_RefusesForeignDestUpFront(t *testing.T) {
	// A destination already holding a DIFFERENT source's audit trail must be refused
	// before any artifact is written (the up-front prefix check), not partway through.
	srcA, srcABlob := newFileSide(t)
	dstStore, dstBlob := newFileSide(t)
	seedSource(t, srcA, srcABlob)
	if err := runMigrate(srcA, srcABlob, dstStore, dstBlob); err != nil {
		t.Fatalf("seed migrate A: %v", err)
	}

	// Source B: a foreign instance with its own (different) audit chain and a unique
	// artifact. Its seq-1 hash differs from A's seq-1, so the dest is not a prefix.
	srcB, srcBBlob := newFileSide(t)
	if err := srcB.PutArtifact(&artifactav1.Artifact{
		Slug: "zulu", OwnerSub: "other", Visibility: artifactav1.Visibility_VISIBILITY_PRIVATE,
		LatestVersion: 1, CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := srcB.AddVersion(&artifactav1.ArtifactVersion{Slug: "zulu", N: 1, ContentType: "text/html", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	putBlob(t, srcBBlob, "zulu", 1, "zulu-bytes")
	// Same event count as A (3) but different content → divergence shows up as a
	// hash mismatch at seq 1, not a length difference.
	for i := 0; i < 3; i++ {
		if err := srcB.Append(&artifactav1.AuditEvent{Ts: timestamppb.Now(), Slug: "zulu", Action: artifactav1.AuditAction_AUDIT_ACTION_PUBLISH, Allowed: true}); err != nil {
			t.Fatal(err)
		}
	}

	err := runMigrate(srcB, srcBBlob, dstStore, dstBlob)
	if err == nil || !strings.Contains(err.Error(), "refusing before any write") {
		t.Fatalf("expected up-front foreign-dest refusal, got: %v", err)
	}
	// Crucially, the foreign artifact must NOT have been written before the abort.
	if _, ok, _ := dstStore.GetArtifact("zulu"); ok {
		t.Fatal("foreign artifact zulu was written before the abort (not fail-fast)")
	}
}

func TestRunMigrate_RefusesBrokenSourceChain(t *testing.T) {
	srcStore, srcBlob := newFileSide(t)
	dstStore, dstBlob := newFileSide(t)
	seedSource(t, srcStore, srcBlob)

	// Inject a chain-breaking event into the source (a seq far ahead with a bogus
	// prev_hash) so the source chain no longer verifies.
	if err := srcStore.AppendRaw(&artifactav1.AuditEvent{
		Seq: 999, PrevHash: "not-the-real-prev", Hash: "whatever",
		Ts: timestamppb.Now(), Slug: "alpha", Action: artifactav1.AuditAction_AUDIT_ACTION_VIEW, Allowed: true,
	}); err != nil {
		t.Fatal(err)
	}
	err := runMigrate(srcStore, srcBlob, dstStore, dstBlob)
	if err == nil || !strings.Contains(err.Error(), "source audit chain does not verify") {
		t.Fatalf("expected fail-fast on broken source chain, got: %v", err)
	}
	// And it must have failed BEFORE writing anything to the destination.
	if _, ok, _ := dstStore.GetArtifact("alpha"); ok {
		t.Fatal("destination was written despite a broken source chain (not fail-fast)")
	}
}
