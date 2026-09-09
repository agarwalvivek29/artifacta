package infra

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// VerifyAuditLog re-reads the append-only audit log at <dir>/audit.log and
// checks the integrity of its hash chain. For each row it verifies that Seq
// increments by 1 from the prior row, that PrevHash links to the prior row's
// Hash, and that recomputing hashEvent over the row reproduces the stored Hash.
// It returns the number of events verified and a descriptive error (including
// the offending Seq) at the first mismatch. A missing or empty log verifies as
// (0, nil).
func VerifyAuditLog(dir string) (verified int, err error) {
	b, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var (
		prevHash string
		prevSeq  int64
	)
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		ev := &artifactav1.AuditEvent{}
		if err := protojson.Unmarshal(line, ev); err != nil {
			return verified, fmt.Errorf("audit event %d: malformed json: %w", prevSeq+1, err)
		}

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
