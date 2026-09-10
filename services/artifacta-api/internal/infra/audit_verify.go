package infra

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// VerifyChain checks the integrity of an ordered audit-event slice: for each row
// it verifies that Seq increments by 1 from the prior row, that PrevHash links to
// the prior row's Hash, and that recomputing hashEvent reproduces the stored Hash.
// It is backend-agnostic — the file and postgres stores both feed it via
// AuditEvents — so `artifacta audit verify` behaves identically on either store.
// Returns the number of events verified and a descriptive error (with the
// offending Seq) at the first mismatch. An empty slice verifies as (0, nil).
func VerifyChain(events []*artifactav1.AuditEvent) (verified int, err error) {
	var (
		prevHash string
		prevSeq  int64
	)
	for _, ev := range events {
		if verified > 0 && ev.GetSeq() != prevSeq+1 {
			return verified, fmt.Errorf("audit event seq %d: expected seq %d", ev.GetSeq(), prevSeq+1)
		}
		if ev.GetPrevHash() != prevHash {
			return verified, fmt.Errorf("audit event seq %d: prev-hash mismatch (chain broken)", ev.GetSeq())
		}
		if want := hashEvent(ev); ev.GetHash() != want {
			return verified, fmt.Errorf("audit event seq %d: hash mismatch (row tampered)", ev.GetSeq())
		}
		prevHash = ev.GetHash()
		prevSeq = ev.GetSeq()
		verified++
	}
	return verified, nil
}

// readAuditLog parses the append-only <dir>/audit.log into events in file order.
// A missing or empty log is (nil, nil).
func readAuditLog(dir string) ([]*artifactav1.AuditEvent, error) {
	b, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*artifactav1.AuditEvent
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		ev := &artifactav1.AuditEvent{}
		if err := protojson.Unmarshal(line, ev); err != nil {
			return nil, fmt.Errorf("malformed audit json: %w", err)
		}
		out = append(out, ev)
	}
	return out, nil
}

// VerifyAuditLog re-reads <dir>/audit.log and verifies its hash chain. Retained
// for the file-store path and direct callers; equivalent to VerifyChain over the
// file's events.
func VerifyAuditLog(dir string) (int, error) {
	events, err := readAuditLog(dir)
	if err != nil {
		return 0, err
	}
	return VerifyChain(events)
}
