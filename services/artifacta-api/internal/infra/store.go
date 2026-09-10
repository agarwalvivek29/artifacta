package infra

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// FileStore is a file-backed metadata + audit store for single-binary local
// deploys. State is held in memory and persisted as protojson; the audit log is
// an append-only, hash-chained JSONL file. Not for concurrent multi-process use —
// Postgres is that adapter.
type FileStore struct {
	mu       sync.Mutex
	dir      string
	arts     map[string]*artifactav1.Artifact
	labels   map[string]string // custom subdomain label → slug (ADR-0017), derived from arts
	grants   []*artifactav1.Grant
	versions []*artifactav1.ArtifactVersion
	comments []*artifactav1.Comment
	last     string // chain head
	seq      int64
}

func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &FileStore{dir: dir, arts: map[string]*artifactav1.Artifact{}, labels: map[string]string{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileStore) artsPath() string     { return filepath.Join(s.dir, "artifacts.json") }
func (s *FileStore) grantsPath() string   { return filepath.Join(s.dir, "grants.json") }
func (s *FileStore) versionsPath() string { return filepath.Join(s.dir, "versions.json") }
func (s *FileStore) commentsPath() string { return filepath.Join(s.dir, "comments.json") }
func (s *FileStore) auditPath() string    { return filepath.Join(s.dir, "audit.log") }

func (s *FileStore) load() error {
	if b, err := os.ReadFile(s.artsPath()); err == nil {
		raw := map[string]json.RawMessage{}
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
		for slug, msg := range raw {
			a := &artifactav1.Artifact{}
			if err := protojson.Unmarshal(msg, a); err != nil {
				return err
			}
			s.arts[slug] = a
		}
	}
	if b, err := os.ReadFile(s.grantsPath()); err == nil {
		var raw []json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
		for _, msg := range raw {
			g := &artifactav1.Grant{}
			if err := protojson.Unmarshal(msg, g); err != nil {
				return err
			}
			s.grants = append(s.grants, g)
		}
	}
	if b, err := os.ReadFile(s.versionsPath()); err == nil {
		var raw []json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
		for _, msg := range raw {
			v := &artifactav1.ArtifactVersion{}
			if err := protojson.Unmarshal(msg, v); err != nil {
				return err
			}
			s.versions = append(s.versions, v)
		}
	}
	if b, err := os.ReadFile(s.commentsPath()); err == nil {
		var raw []json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
		for _, msg := range raw {
			c := &artifactav1.Comment{}
			if err := protojson.Unmarshal(msg, c); err != nil {
				return err
			}
			s.comments = append(s.comments, c)
		}
	}
	if b, err := os.ReadFile(s.auditPath()); err == nil {
		for _, line := range bytes.Split(b, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			ev := &artifactav1.AuditEvent{}
			if err := protojson.Unmarshal(line, ev); err != nil {
				return err
			}
			s.last = ev.GetHash()
			s.seq = ev.GetSeq()
		}
	}
	// Rebuild the label→slug index from the artifacts (art.Label is authoritative,
	// ADR-0017), so the reverse lookup never drifts from the stored artifacts.
	s.labels = map[string]string{}
	for slug, a := range s.arts {
		if a.GetLabel() != "" {
			s.labels[a.GetLabel()] = slug
		}
	}
	return nil
}

// SetLabel atomically claims a custom subdomain label (ADR-0017) for slug. It
// enforces global uniqueness: the return bool is false (nothing written) when the
// label is already held by a different artifact, or when slug does not exist. A
// label already held by this same slug is idempotent. Claiming a new label
// releases the artifact's previous label. The caller is responsible for having
// validated the label with domain.ValidLabel first.
func (s *FileStore) SetLabel(slug, label string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.arts[slug]
	if !ok {
		return false, nil // no such artifact
	}
	if cur, taken := s.labels[label]; taken && cur != slug {
		return false, nil // label already claimed by another artifact
	}
	if prev := a.GetLabel(); prev != "" && prev != label {
		delete(s.labels, prev) // release the artifact's previous label
	}
	a.Label = label
	s.labels[label] = slug
	return true, s.persistArtifacts()
}

// SlugForLabel resolves a custom subdomain label to its slug (ADR-0017). The bool
// is false when no artifact holds that label (a miss, not an error).
func (s *FileStore) SlugForLabel(label string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	slug, ok := s.labels[label]
	return slug, ok, nil
}

func (s *FileStore) PutArtifact(a *artifactav1.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.arts[a.GetSlug()] = a
	return s.persistArtifacts()
}

func (s *FileStore) persistArtifacts() error {
	out := map[string]json.RawMessage{}
	for slug, a := range s.arts {
		b, err := protojson.Marshal(a)
		if err != nil {
			return err
		}
		out[slug] = b
	}
	return writeJSON(s.artsPath(), out)
}

func (s *FileStore) GetArtifact(slug string) (*artifactav1.Artifact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.arts[slug]
	return a, ok, nil
}

func (s *FileStore) ListByOwner(sub string) ([]*artifactav1.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*artifactav1.Artifact
	for _, a := range s.arts {
		if a.GetOwnerSub() == sub {
			out = append(out, a)
		}
	}
	return out, nil
}

// ListByGrantee returns the deduped set of artifacts shared with sub via a
// Grant. It joins grants→artifacts: a slug is included only if a grant names
// sub as grantee AND the artifact still exists. This is strictly grant-based
// "shared with me" — the caller's own artifacts are NOT included implicitly.
func (s *FileStore) ListByGrantee(sub string) ([]*artifactav1.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*artifactav1.Artifact
	seen := map[string]struct{}{}
	for _, g := range s.grants {
		if g.GetGranteeSub() != sub {
			continue
		}
		slug := g.GetSlug()
		if _, dup := seen[slug]; dup {
			continue
		}
		a, ok := s.arts[slug]
		if !ok {
			continue // dangling grant — artifact deleted
		}
		seen[slug] = struct{}{}
		out = append(out, a)
	}
	return out, nil
}

// ListByVisibility returns every artifact whose visibility equals v. It backs
// the dashboard's Org tab (ListByVisibility(ORG)); the caller is responsible for
// any further filtering (e.g. excluding its own artifacts).
func (s *FileStore) ListByVisibility(v artifactav1.Visibility) ([]*artifactav1.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*artifactav1.Artifact
	for _, a := range s.arts {
		if a.GetVisibility() == v {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *FileStore) AddGrant(g *artifactav1.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants = append(s.grants, g)
	out := make([]json.RawMessage, 0, len(s.grants))
	for _, gr := range s.grants {
		b, err := protojson.Marshal(gr)
		if err != nil {
			return err
		}
		out = append(out, b)
	}
	return writeJSON(s.grantsPath(), out)
}

func (s *FileStore) Grants(slug string) ([]*artifactav1.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*artifactav1.Grant
	for _, g := range s.grants {
		if g.GetSlug() == slug {
			out = append(out, g)
		}
	}
	return out, nil
}

// RemoveGrant revokes access on slug for the grantee identified by id, matching
// either the grantee's subject or their email (case-insensitive). The bool
// reports whether any grant was removed (a miss is not an error). Revoking a
// grant is the inverse of AddGrant; both persist the full grant list.
func (s *FileStore) RemoveGrant(slug, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.grants[:0:0]
	removed := false
	for _, g := range s.grants {
		match := g.GetSlug() == slug &&
			((g.GetGranteeSub() != "" && g.GetGranteeSub() == id) ||
				(g.GetGranteeEmail() != "" && strings.EqualFold(g.GetGranteeEmail(), id)))
		if match {
			removed = true
			continue
		}
		kept = append(kept, g)
	}
	if !removed {
		return false, nil
	}
	s.grants = kept
	out := make([]json.RawMessage, 0, len(s.grants))
	for _, gr := range s.grants {
		b, err := protojson.Marshal(gr)
		if err != nil {
			return false, err
		}
		out = append(out, b)
	}
	return true, writeJSON(s.grantsPath(), out)
}

// AddVersion appends one immutable artifact version and persists the version
// list atomically. Versions are never mutated or deleted (ADR-0013).
func (s *FileStore) AddVersion(v *artifactav1.ArtifactVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versions = append(s.versions, v)
	return s.persistVersions()
}

func (s *FileStore) persistVersions() error {
	out := make([]json.RawMessage, 0, len(s.versions))
	for _, v := range s.versions {
		b, err := protojson.Marshal(v)
		if err != nil {
			return err
		}
		out = append(out, b)
	}
	return writeJSON(s.versionsPath(), out)
}

// Versions returns every version of slug, ascending by version number n.
func (s *FileStore) Versions(slug string) ([]*artifactav1.ArtifactVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*artifactav1.ArtifactVersion
	for _, v := range s.versions {
		if v.GetSlug() == slug {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetN() < out[j].GetN() })
	return out, nil
}

// GetVersion returns version n of slug. The bool is false when no such version
// exists (missing, not an error).
func (s *FileStore) GetVersion(slug string, n int32) (*artifactav1.ArtifactVersion, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.versions {
		if v.GetSlug() == slug && v.GetN() == n {
			return v, true, nil
		}
	}
	return nil, false, nil
}

// AddComment appends one review comment and persists the comment list
// atomically. Comments are collaboration content, never audited (ADR-0014).
func (s *FileStore) AddComment(c *artifactav1.Comment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comments = append(s.comments, c)
	return s.persistComments()
}

func (s *FileStore) persistComments() error {
	out := make([]json.RawMessage, 0, len(s.comments))
	for _, c := range s.comments {
		b, err := protojson.Marshal(c)
		if err != nil {
			return err
		}
		out = append(out, b)
	}
	return writeJSON(s.commentsPath(), out)
}

// Comments returns every comment on slug, ascending by created_at (oldest
// first). Callers filter by version at the handler layer.
func (s *FileStore) Comments(slug string) ([]*artifactav1.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*artifactav1.Comment
	for _, c := range s.comments {
		if c.GetSlug() == slug {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GetCreatedAt().AsTime().Before(out[j].GetCreatedAt().AsTime())
	})
	return out, nil
}

// ResolveComment marks the comment with the given id on slug as resolved and
// persists the change. The bool reports whether such a comment was found (a
// miss is not an error).
func (s *FileStore) ResolveComment(slug, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.comments {
		if c.GetSlug() == slug && c.GetId() == id {
			c.Resolved = true
			return true, s.persistComments()
		}
	}
	return false, nil
}

// Append writes one hash-chained, append-only audit row.
func (s *FileStore) Append(ev *artifactav1.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	ev.Seq = s.seq
	ev.PrevHash = s.last
	ev.Hash = hashEvent(ev)
	s.last = ev.GetHash()

	line, err := protojson.Marshal(ev)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(s.auditPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = fh.Write(append(line, '\n'))
	return err
}

func hashEvent(ev *artifactav1.AuditEvent) string {
	payload := fmt.Sprintf("%d|%s|%s|%s|%s|%t|%s",
		ev.GetSeq(), ev.GetTs().AsTime().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		ev.GetPrincipalSub(), ev.GetSlug(), ev.GetAction().String(), ev.GetAllowed(), ev.GetPrevHash())
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// writeJSON atomically replaces path with the JSON encoding of v. It writes to a
// temp file in the same directory (so os.Rename stays atomic on one filesystem),
// then renames over the target. A crash leaves either the old or new file intact,
// never a partial write.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir, base := filepath.Split(path)
	tmp, err := os.CreateTemp(dir, base+".tmp-*")
	if err != nil {
		return err
	}
	// Clean up the temp file on any error before the rename succeeds.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
