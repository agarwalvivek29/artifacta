// SQLStore is the horizontally-scalable metadata store (ADR-0009): a GORM-backed
// adapter that satisfies the same interface as FileStore, so the deployer picks
// the backend and the rest of the app is unchanged. It is schema-first-safe: the
// GORM models below are thin STORAGE ENVELOPES (a primary key, the columns we
// filter on, and a jsonb `data` column holding the protojson of the domain
// message) — the domain types stay the generated proto messages, never redefined.
//
// Migrations auto-apply: NewSQLStore runs GORM AutoMigrate on startup, so a
// deployment that bumps the binary picks up new columns/indexes without a manual
// migration step.
package infra

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// ── storage envelopes (NOT domain types) ────────────────────────────────────

type artifactRow struct {
	Slug       string  `gorm:"primaryKey;size:64"`
	OwnerSub   string  `gorm:"index;size:255"`
	Visibility int32   `gorm:"index"`
	Label      *string `gorm:"uniqueIndex;size:63"` // nil = no label; NULLs don't collide, so the unique index IS the anti-double-assign guard
	Data       string  `gorm:"type:jsonb"`          // protojson(Artifact)
}

func (artifactRow) TableName() string { return "artifacts" }

type grantRow struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	Slug         string `gorm:"index;size:64"`
	GranteeSub   string `gorm:"index;size:255"`
	GranteeEmail string `gorm:"index;size:255"`
	Data         string `gorm:"type:jsonb"` // protojson(Grant)
}

func (grantRow) TableName() string { return "grants" }

type versionRow struct {
	Slug string `gorm:"primaryKey;size:64"`
	N    int32  `gorm:"primaryKey"`
	Data string `gorm:"type:jsonb"` // protojson(ArtifactVersion)
}

func (versionRow) TableName() string { return "artifact_versions" }

type commentRow struct {
	ID      string `gorm:"primaryKey;size:64"`
	Slug    string `gorm:"index;size:64"`
	Version int32  `gorm:"index"`
	Data    string `gorm:"type:jsonb"` // protojson(Comment)
}

func (commentRow) TableName() string { return "comments" }

type auditRow struct {
	Seq  int64  `gorm:"primaryKey"` // assigned explicitly (chain order), not autoincrement
	Hash string `gorm:"size:64"`    // chain head lookup
	Data string `gorm:"type:jsonb"` // protojson(AuditEvent)
}

func (auditRow) TableName() string { return "audit_events" }

// ── store ───────────────────────────────────────────────────────────────────

type SQLStore struct{ db *gorm.DB }

// NewSQLStore opens the postgres database at dsn and auto-migrates the schema.
// AutoMigrate is additive (creates tables, adds missing columns/indexes) so an
// upgraded binary self-applies its schema changes on boot.
func NewSQLStore(dsn string) (*SQLStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres store selected but ARTIFACTA_DATABASE_URL is empty")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.AutoMigrate(&artifactRow{}, &grantRow{}, &versionRow{}, &commentRow{}, &auditRow{}); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// isUniqueViolation reports whether err is a postgres unique-constraint violation
// (SQLSTATE 23505) — the label double-assign backstop when two claims race.
func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// ── artifacts ────────────────────────────────────────────────────────────────

func (s *SQLStore) PutArtifact(a *artifactav1.Artifact) error {
	data, err := protojson.Marshal(a)
	if err != nil {
		return err
	}
	row := artifactRow{Slug: a.GetSlug(), OwnerSub: a.GetOwnerSub(), Visibility: int32(a.GetVisibility()), Data: string(data)}
	if a.GetLabel() != "" {
		l := a.GetLabel()
		row.Label = &l
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "slug"}},
		DoUpdates: clause.AssignmentColumns([]string{"owner_sub", "visibility", "label", "data"}),
	}).Create(&row).Error
}

func (s *SQLStore) GetArtifact(slug string) (*artifactav1.Artifact, bool, error) {
	var row artifactRow
	err := s.db.First(&row, "slug = ?", slug).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	a := &artifactav1.Artifact{}
	if err := protojson.Unmarshal([]byte(row.Data), a); err != nil {
		return nil, false, err
	}
	return a, true, nil
}

func (s *SQLStore) listArtifacts(where string, args ...any) ([]*artifactav1.Artifact, error) {
	var rows []artifactRow
	if err := s.db.Where(where, args...).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*artifactav1.Artifact, 0, len(rows))
	for _, r := range rows {
		a := &artifactav1.Artifact{}
		if err := protojson.Unmarshal([]byte(r.Data), a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *SQLStore) ListByOwner(sub string) ([]*artifactav1.Artifact, error) {
	return s.listArtifacts("owner_sub = ?", sub)
}

func (s *SQLStore) ListByVisibility(v artifactav1.Visibility) ([]*artifactav1.Artifact, error) {
	return s.listArtifacts("visibility = ?", int32(v))
}

// ListByGrantee mirrors FileStore: strictly subject-based "shared with me"
// (grantee_sub), joined to still-existing artifacts, deduped.
func (s *SQLStore) ListByGrantee(sub string) ([]*artifactav1.Artifact, error) {
	var slugs []string
	if err := s.db.Model(&grantRow{}).Distinct().Where("grantee_sub = ? AND grantee_sub <> ''", sub).Pluck("slug", &slugs).Error; err != nil {
		return nil, err
	}
	if len(slugs) == 0 {
		return nil, nil
	}
	return s.listArtifacts("slug IN ?", slugs)
}

// ── labels (ADR-0017) ────────────────────────────────────────────────────────

// SetLabel atomically claims label for slug. Uniqueness is enforced by the
// unique index on artifacts.label; the in-transaction check handles the common
// path and a racing claim is caught as a 23505 unique violation. Returns false
// (nothing written) when the label is held by another artifact or slug is absent.
func (s *SQLStore) SetLabel(slug, label string) (bool, error) {
	var claimed bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row artifactRow
		if e := tx.First(&row, "slug = ?", slug).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return nil // slug absent → claimed stays false
			}
			return e
		}
		var other artifactRow
		e := tx.First(&other, "label = ?", label).Error
		if e == nil && other.Slug != slug {
			return nil // taken by another → claimed stays false
		}
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		a := &artifactav1.Artifact{}
		if e := protojson.Unmarshal([]byte(row.Data), a); e != nil {
			return e
		}
		a.Label = label
		data, e := protojson.Marshal(a)
		if e != nil {
			return e
		}
		lbl := label
		if e := tx.Model(&artifactRow{}).Where("slug = ?", slug).
			Updates(map[string]any{"label": &lbl, "data": string(data)}).Error; e != nil {
			return e
		}
		claimed = true
		return nil
	})
	if isUniqueViolation(err) {
		return false, nil // lost a race → not claimed, no error
	}
	return claimed, err
}

func (s *SQLStore) SlugForLabel(label string) (string, bool, error) {
	var row artifactRow
	err := s.db.Select("slug").First(&row, "label = ?", label).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.Slug, true, nil
}

// ── grants ───────────────────────────────────────────────────────────────────

func (s *SQLStore) AddGrant(g *artifactav1.Grant) error {
	data, err := protojson.Marshal(g)
	if err != nil {
		return err
	}
	return s.db.Create(&grantRow{Slug: g.GetSlug(), GranteeSub: g.GetGranteeSub(), GranteeEmail: g.GetGranteeEmail(), Data: string(data)}).Error
}

func (s *SQLStore) Grants(slug string) ([]*artifactav1.Grant, error) {
	var rows []grantRow
	if err := s.db.Where("slug = ?", slug).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*artifactav1.Grant, 0, len(rows))
	for _, r := range rows {
		g := &artifactav1.Grant{}
		if err := protojson.Unmarshal([]byte(r.Data), g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func (s *SQLStore) RemoveGrant(slug, id string) (bool, error) {
	res := s.db.Where("slug = ? AND (grantee_sub = ? OR lower(grantee_email) = lower(?))", slug, id, id).Delete(&grantRow{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ── versions ─────────────────────────────────────────────────────────────────

func (s *SQLStore) AddVersion(v *artifactav1.ArtifactVersion) error {
	data, err := protojson.Marshal(v)
	if err != nil {
		return err
	}
	return s.db.Create(&versionRow{Slug: v.GetSlug(), N: v.GetN(), Data: string(data)}).Error
}

func (s *SQLStore) Versions(slug string) ([]*artifactav1.ArtifactVersion, error) {
	var rows []versionRow
	if err := s.db.Where("slug = ?", slug).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*artifactav1.ArtifactVersion, 0, len(rows))
	for _, r := range rows {
		v := &artifactav1.ArtifactVersion{}
		if err := protojson.Unmarshal([]byte(r.Data), v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetN() < out[j].GetN() })
	return out, nil
}

func (s *SQLStore) GetVersion(slug string, n int32) (*artifactav1.ArtifactVersion, bool, error) {
	var row versionRow
	err := s.db.First(&row, "slug = ? AND n = ?", slug, n).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	v := &artifactav1.ArtifactVersion{}
	if err := protojson.Unmarshal([]byte(row.Data), v); err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// ── comments ─────────────────────────────────────────────────────────────────

func (s *SQLStore) AddComment(c *artifactav1.Comment) error {
	data, err := protojson.Marshal(c)
	if err != nil {
		return err
	}
	return s.db.Create(&commentRow{ID: c.GetId(), Slug: c.GetSlug(), Version: c.GetVersion(), Data: string(data)}).Error
}

func (s *SQLStore) Comments(slug string) ([]*artifactav1.Comment, error) {
	var rows []commentRow
	if err := s.db.Where("slug = ?", slug).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*artifactav1.Comment, 0, len(rows))
	for _, r := range rows {
		c := &artifactav1.Comment{}
		if err := protojson.Unmarshal([]byte(r.Data), c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GetCreatedAt().AsTime().Before(out[j].GetCreatedAt().AsTime())
	})
	return out, nil
}

func (s *SQLStore) ResolveComment(slug, id string) (bool, error) {
	var found bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row commentRow
		e := tx.First(&row, "slug = ? AND id = ?", slug, id).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		c := &artifactav1.Comment{}
		if e := protojson.Unmarshal([]byte(row.Data), c); e != nil {
			return e
		}
		c.Resolved = true
		data, e := protojson.Marshal(c)
		if e != nil {
			return e
		}
		found = true
		return tx.Model(&commentRow{}).Where("id = ?", id).Update("data", string(data)).Error
	})
	return found, err
}

// ── audit (hash-chained) ─────────────────────────────────────────────────────

// Append writes one hash-chained audit row. Serialized in a transaction so the
// seq/prev_hash it chains onto cannot be raced by a concurrent append.
func (s *SQLStore) Append(ev *artifactav1.AuditEvent) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var last auditRow
		e := tx.Order("seq DESC").First(&last).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		ev.Seq = last.Seq + 1
		ev.PrevHash = last.Hash
		ev.Hash = hashEvent(ev)
		data, e := protojson.Marshal(ev)
		if e != nil {
			return e
		}
		return tx.Create(&auditRow{Seq: ev.GetSeq(), Hash: ev.GetHash(), Data: string(data)}).Error
	})
}
