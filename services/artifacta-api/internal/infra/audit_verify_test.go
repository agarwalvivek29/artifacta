package infra

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func appendEvents(t *testing.T, s *FileStore, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ev := &artifactav1.AuditEvent{
			Ts:           timestamppb.Now(),
			PrincipalSub: "user-123",
			Slug:         "slug-a",
			Action:       artifactav1.AuditAction_AUDIT_ACTION_PUBLISH,
			Allowed:      true,
		}
		if err := s.Append(ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// TestVerifyAuditLogOK builds a real hash-chained log and asserts it verifies.
func TestVerifyAuditLogOK(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	appendEvents(t, s, 3)

	n, err := VerifyAuditLog(dir)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if n != 3 {
		t.Fatalf("verified = %d, want 3", n)
	}
}

// TestVerifyRowHashes checks per-row verification (the owner-scoped remote
// path): real rows verify, and altering one row's hashed content is detected
// even though the slice is not a contiguous chain.
func TestVerifyRowHashes(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	appendEvents(t, s, 3)
	events, err := s.AuditEvents()
	if err != nil {
		t.Fatalf("AuditEvents: %v", err)
	}

	// A non-contiguous subset (drop the middle row) still verifies per-row: unlike
	// VerifyChain, VerifyRowHashes does not require seq continuity.
	subset := []*artifactav1.AuditEvent{events[0], events[2]}
	if n, err := VerifyRowHashes(subset); err != nil || n != 2 {
		t.Fatalf("VerifyRowHashes(subset): n=%d err=%v, want 2/nil", n, err)
	}

	// Tamper with a hashed field on one row: it must be caught.
	events[0].Slug = "tampered"
	if _, err := VerifyRowHashes(events); err == nil {
		t.Fatal("VerifyRowHashes: expected error on tampered row, got nil")
	}
}

// TestVerifyAuditLogEmpty verifies a missing log is (0, nil).
func TestVerifyAuditLogEmpty(t *testing.T) {
	n, err := VerifyAuditLog(t.TempDir())
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if n != 0 {
		t.Fatalf("verified = %d, want 0", n)
	}
}

// TestVerifyAuditLogTampered corrupts a hashed field on disk and asserts the
// chain no longer verifies.
func TestVerifyAuditLogTampered(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	appendEvents(t, s, 3)

	path := filepath.Join(dir, "audit.log")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))

	// Flip a hashed field on the middle row without touching its stored Hash,
	// so the recomputed hash no longer matches.
	ev := &artifactav1.AuditEvent{}
	if err := protojson.Unmarshal(lines[1], ev); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	ev.PrincipalSub = "attacker"
	tampered, err := protojson.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	lines[1] = tampered
	if err := os.WriteFile(path, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := VerifyAuditLog(dir); err == nil {
		t.Fatal("VerifyAuditLog: expected error on tampered log, got nil")
	}
}
