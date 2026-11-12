package infra

import (
	"errors"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Service accounts and API keys for the SQLStore (ADR-0028). Same envelope
// pattern as the other tables: indexed lookup columns plus a jsonb protojson
// `data` column holding the generated message.

type serviceAccountRow struct {
	ID   string `gorm:"primaryKey;size:64"`
	Name string `gorm:"uniqueIndex;size:63"` // names are never reused, disabled rows included
	Data string `gorm:"type:jsonb"`          // protojson(ServiceAccount)
}

func (serviceAccountRow) TableName() string { return "service_accounts" }

type apiKeyRow struct {
	ID               string `gorm:"primaryKey;size:64"`
	ServiceAccountID string `gorm:"index;size:64"`
	KeyHash          string `gorm:"uniqueIndex;size:64"` // hex sha256 of the raw key; the auth lookup
	Data             string `gorm:"type:jsonb"`          // protojson(ApiKey)
}

func (apiKeyRow) TableName() string { return "api_keys" }

func (s *SQLStore) CreateServiceAccount(sa *artifactav1.ServiceAccount) error {
	data, err := protojson.Marshal(sa)
	if err != nil {
		return err
	}
	err = s.db.Create(&serviceAccountRow{ID: sa.GetId(), Name: sa.GetName(), Data: string(data)}).Error
	if isUniqueViolation(err) {
		return domain.ErrSAExists
	}
	return err
}

func (s *SQLStore) serviceAccountWhere(where string, arg any) (*artifactav1.ServiceAccount, bool, error) {
	var row serviceAccountRow
	err := s.db.First(&row, where, arg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	sa := &artifactav1.ServiceAccount{}
	if err := protojson.Unmarshal([]byte(row.Data), sa); err != nil {
		return nil, false, err
	}
	return sa, true, nil
}

func (s *SQLStore) ServiceAccountByName(name string) (*artifactav1.ServiceAccount, bool, error) {
	return s.serviceAccountWhere("name = ?", name)
}

func (s *SQLStore) ServiceAccountByID(id string) (*artifactav1.ServiceAccount, bool, error) {
	return s.serviceAccountWhere("id = ?", id)
}

func (s *SQLStore) ListServiceAccounts() ([]*artifactav1.ServiceAccount, error) {
	var rows []serviceAccountRow
	if err := s.db.Order(`name COLLATE "C"`).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*artifactav1.ServiceAccount, 0, len(rows))
	for _, r := range rows {
		sa := &artifactav1.ServiceAccount{}
		if err := protojson.Unmarshal([]byte(r.Data), sa); err != nil {
			return nil, err
		}
		out = append(out, sa)
	}
	return out, nil
}

// DisableServiceAccount marks an active account disabled. The read-modify-write
// runs in a row-locked transaction so two concurrent disables can't both win.
func (s *SQLStore) DisableServiceAccount(name, bySub string, at time.Time) (bool, error) {
	done := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row serviceAccountRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "name = ?", name).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		sa := &artifactav1.ServiceAccount{}
		if err := protojson.Unmarshal([]byte(row.Data), sa); err != nil {
			return err
		}
		if sa.GetDisabledAt() != nil {
			return nil
		}
		sa.DisabledAt, sa.DisabledBySub = timestamppb.New(at), bySub
		data, err := protojson.Marshal(sa)
		if err != nil {
			return err
		}
		if err := tx.Model(&serviceAccountRow{}).Where("id = ?", row.ID).Update("data", string(data)).Error; err != nil {
			return err
		}
		done = true
		return nil
	})
	return done, err
}

func (s *SQLStore) PutAPIKey(k *artifactav1.ApiKey) error {
	data, err := protojson.Marshal(k)
	if err != nil {
		return err
	}
	return s.db.Create(&apiKeyRow{ID: k.GetId(), ServiceAccountID: k.GetServiceAccountId(), KeyHash: k.GetKeyHash(), Data: string(data)}).Error
}

func (s *SQLStore) APIKeyByHash(hash string) (*artifactav1.ApiKey, bool, error) {
	var row apiKeyRow
	err := s.db.First(&row, "key_hash = ?", hash).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	k := &artifactav1.ApiKey{}
	if err := protojson.Unmarshal([]byte(row.Data), k); err != nil {
		return nil, false, err
	}
	return k, true, nil
}

func (s *SQLStore) ListAPIKeys(saID string) ([]*artifactav1.ApiKey, error) {
	var rows []apiKeyRow
	if err := s.db.Where("service_account_id = ?", saID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*artifactav1.ApiKey, 0, len(rows))
	for _, r := range rows {
		k := &artifactav1.ApiKey{}
		if err := protojson.Unmarshal([]byte(r.Data), k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	sortKeysNewestFirst(out)
	return out, nil
}

// updateKey applies fn to one key under a row lock and saves it. ok is false
// when the key does not exist or fn declines (returns false).
func (s *SQLStore) updateKey(where string, args []any, fn func(*artifactav1.ApiKey) bool) (bool, error) {
	done := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row apiKeyRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(where, args...).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		k := &artifactav1.ApiKey{}
		if err := protojson.Unmarshal([]byte(row.Data), k); err != nil {
			return err
		}
		if !fn(k) {
			return nil
		}
		data, err := protojson.Marshal(k)
		if err != nil {
			return err
		}
		if err := tx.Model(&apiKeyRow{}).Where("id = ?", row.ID).Update("data", string(data)).Error; err != nil {
			return err
		}
		done = true
		return nil
	})
	return done, err
}

func (s *SQLStore) RevokeAPIKey(saID, keyID string, at time.Time) (bool, error) {
	return s.updateKey("id = ? AND service_account_id = ?", []any{keyID, saID}, func(k *artifactav1.ApiKey) bool {
		if k.GetRevokedAt() != nil {
			return false
		}
		k.RevokedAt = timestamppb.New(at)
		return true
	})
}

func (s *SQLStore) TouchAPIKey(keyID string, at time.Time) error {
	_, err := s.updateKey("id = ?", []any{keyID}, func(k *artifactav1.ApiKey) bool {
		k.LastUsedAt = timestamppb.New(at)
		return true
	})
	return err
}
