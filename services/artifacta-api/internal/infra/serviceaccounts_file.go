package infra

import (
	"encoding/json"
	"sort"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service accounts and API keys for the FileStore (ADR-0028). Both live in
// memory, indexed by id plus a secondary index (name / key hash), and are
// persisted whole on every change — the volume is a handful of rows.
//
// Every getter returns a clone so callers can't mutate the store's state.

func (s *FileStore) CreateServiceAccount(sa *artifactav1.ServiceAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.saByName[sa.GetName()]; taken {
		return domain.ErrSAExists
	}
	c := proto.Clone(sa).(*artifactav1.ServiceAccount)
	s.sas[c.GetId()] = c
	s.saByName[c.GetName()] = c.GetId()
	if err := s.persistServiceAccounts(); err != nil {
		delete(s.sas, c.GetId())
		delete(s.saByName, c.GetName())
		return err
	}
	return nil
}

func (s *FileStore) ServiceAccountByName(name string) (*artifactav1.ServiceAccount, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sa, ok := s.sas[s.saByName[name]]
	if !ok {
		return nil, false, nil
	}
	return proto.Clone(sa).(*artifactav1.ServiceAccount), true, nil
}

func (s *FileStore) ServiceAccountByID(id string) (*artifactav1.ServiceAccount, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sa, ok := s.sas[id]
	if !ok {
		return nil, false, nil
	}
	return proto.Clone(sa).(*artifactav1.ServiceAccount), true, nil
}

func (s *FileStore) ListServiceAccounts() ([]*artifactav1.ServiceAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*artifactav1.ServiceAccount, 0, len(s.sas))
	for _, sa := range s.sas {
		out = append(out, proto.Clone(sa).(*artifactav1.ServiceAccount))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out, nil
}

func (s *FileStore) DisableServiceAccount(name, bySub string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sa, ok := s.sas[s.saByName[name]]
	if !ok || sa.GetDisabledAt() != nil {
		return false, nil
	}
	prev := proto.Clone(sa).(*artifactav1.ServiceAccount)
	sa.DisabledAt, sa.DisabledBySub = timestamppb.New(at), bySub
	if err := s.persistServiceAccounts(); err != nil {
		s.sas[sa.GetId()] = prev
		return false, err
	}
	return true, nil
}

func (s *FileStore) PutAPIKey(k *artifactav1.ApiKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := proto.Clone(k).(*artifactav1.ApiKey)
	s.keys[c.GetId()] = c
	s.keyByHash[c.GetKeyHash()] = c.GetId()
	if err := s.persistAPIKeys(); err != nil {
		delete(s.keys, c.GetId())
		delete(s.keyByHash, c.GetKeyHash())
		return err
	}
	return nil
}

func (s *FileStore) APIKeyByHash(hash string) (*artifactav1.ApiKey, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[s.keyByHash[hash]]
	if !ok {
		return nil, false, nil
	}
	return proto.Clone(k).(*artifactav1.ApiKey), true, nil
}

func (s *FileStore) ListAPIKeys(saID string) ([]*artifactav1.ApiKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*artifactav1.ApiKey
	for _, k := range s.keys {
		if k.GetServiceAccountId() == saID {
			out = append(out, proto.Clone(k).(*artifactav1.ApiKey))
		}
	}
	sortKeysNewestFirst(out)
	return out, nil
}

func (s *FileStore) RevokeAPIKey(saID, keyID string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[keyID]
	if !ok || k.GetServiceAccountId() != saID || k.GetRevokedAt() != nil {
		return false, nil
	}
	k.RevokedAt = timestamppb.New(at)
	if err := s.persistAPIKeys(); err != nil {
		k.RevokedAt = nil
		return false, err
	}
	return true, nil
}

func (s *FileStore) TouchAPIKey(keyID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[keyID]
	if !ok {
		return nil
	}
	k.LastUsedAt = timestamppb.New(at)
	return s.persistAPIKeys()
}

// persistServiceAccounts / persistAPIKeys write the whole collection. Callers
// hold s.mu.
func (s *FileStore) persistServiceAccounts() error {
	out := make([]json.RawMessage, 0, len(s.sas))
	for _, sa := range s.sas {
		b, err := protojson.Marshal(sa)
		if err != nil {
			return err
		}
		out = append(out, b)
	}
	return writeJSON(s.sasPath(), out)
}

func (s *FileStore) persistAPIKeys() error {
	out := make([]json.RawMessage, 0, len(s.keys))
	for _, k := range s.keys {
		b, err := protojson.Marshal(k)
		if err != nil {
			return err
		}
		out = append(out, b)
	}
	return writeJSON(s.keysPath(), out)
}

// sortKeysNewestFirst orders keys by creation time, newest first, with the id
// as a stable tie-break. Shared by both store backends.
func sortKeysNewestFirst(ks []*artifactav1.ApiKey) {
	sort.Slice(ks, func(i, j int) bool {
		ti, tj := ks[i].GetCreatedAt().AsTime(), ks[j].GetCreatedAt().AsTime()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return ks[i].GetId() < ks[j].GetId()
	})
}
