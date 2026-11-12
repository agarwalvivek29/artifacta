package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// recordingAuth is an inner provider that records whether it was consulted.
type recordingAuth struct {
	id     *artifactav1.Identity
	called bool
}

func (r *recordingAuth) Identify(*http.Request) (*artifactav1.Identity, bool) {
	r.called = true
	if r.id == nil {
		return nil, false
	}
	return r.id, true
}

// seedSA creates a service account with one key and returns the raw key.
func seedSA(t *testing.T, st Store, name string, now time.Time, mutate func(*artifactav1.ApiKey)) (raw string, key *artifactav1.ApiKey) {
	t.Helper()
	sa := &artifactav1.ServiceAccount{Id: domain.NewSlug(), Name: name, CreatedAt: timestamppb.New(now), CreatedBySub: "admin"}
	if err := st.CreateServiceAccount(sa); err != nil {
		t.Fatal(err)
	}
	raw, hash, prefix := domain.NewAPIKey()
	key = &artifactav1.ApiKey{Id: domain.NewSlug(), ServiceAccountId: sa.GetId(), KeyHash: hash, Prefix: prefix, CreatedAt: timestamppb.New(now)}
	if mutate != nil {
		mutate(key)
	}
	if err := st.PutAPIKey(key); err != nil {
		t.Fatal(err)
	}
	return raw, key
}

func newKeyStore(t *testing.T) *infra.FileStore {
	t.Helper()
	st, err := infra.NewFileStore(filepath.Join(t.TempDir(), "meta"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func bearerReq(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

func TestAPIKeyAuthResolvesServiceAccount(t *testing.T) {
	st := newKeyStore(t)
	now := time.Now().UTC()
	raw, key := seedSA(t, st, "ci", now, nil)
	inner := &recordingAuth{id: &artifactav1.Identity{Sub: "human"}}
	a := &APIKeyAuth{Inner: inner, Store: st, Now: func() time.Time { return now }}

	who, ok := a.Identify(bearerReq(raw))
	if !ok || who.GetSub() != "sa:ci" {
		t.Fatalf("Identify = %v, %v; want sa:ci", who, ok)
	}
	if inner.called {
		t.Fatal("inner provider consulted for an afa_ key")
	}
	got, _, _ := st.APIKeyByHash(key.GetKeyHash())
	if !got.GetLastUsedAt().AsTime().Equal(now) {
		t.Fatalf("last_used_at = %v, want %v", got.GetLastUsedAt().AsTime(), now)
	}
}

func TestAPIKeyAuthFailsClosedWithoutFallthrough(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name  string
		setup func(t *testing.T, st *infra.FileStore) string
	}{
		{"unknown key", func(*testing.T, *infra.FileStore) string { raw, _, _ := domain.NewAPIKey(); return raw }},
		{"malformed afa_ value", func(*testing.T, *infra.FileStore) string { return "afa_x" }},
		{"revoked", func(t *testing.T, st *infra.FileStore) string {
			raw, _ := seedSA(t, st, "rev", now, func(k *artifactav1.ApiKey) { k.RevokedAt = timestamppb.New(now.Add(-time.Minute)) })
			return raw
		}},
		{"expired", func(t *testing.T, st *infra.FileStore) string {
			raw, _ := seedSA(t, st, "exp", now, func(k *artifactav1.ApiKey) { k.ExpiresAt = timestamppb.New(now.Add(-time.Second)) })
			return raw
		}},
		{"service account disabled", func(t *testing.T, st *infra.FileStore) string {
			raw, _ := seedSA(t, st, "dis", now, nil)
			if ok, err := st.DisableServiceAccount("dis", "admin", now); err != nil || !ok {
				t.Fatalf("disable: %v %v", ok, err)
			}
			return raw
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newKeyStore(t)
			raw := c.setup(t, st)
			// The inner provider would accept anything: a fallthrough would show up as ok.
			inner := &recordingAuth{id: &artifactav1.Identity{Sub: "human"}}
			a := &APIKeyAuth{Inner: inner, Store: st, Now: func() time.Time { return now }}
			if who, ok := a.Identify(bearerReq(raw)); ok {
				t.Fatalf("Identify accepted %s as %v", c.name, who)
			}
			if inner.called {
				t.Fatal("inner provider consulted after an afa_ key failed")
			}
		})
	}
}

// failingKeyStore makes the key lookup error, standing in for a store outage.
type failingKeyStore struct{ Store }

func (failingKeyStore) APIKeyByHash(string) (*artifactav1.ApiKey, bool, error) {
	return nil, false, errors.New("db down")
}

func TestAPIKeyAuthStoreErrorFailsClosed(t *testing.T) {
	st := newKeyStore(t)
	raw, _ := seedSA(t, st, "ci", time.Now(), nil)
	a := &APIKeyAuth{Inner: &recordingAuth{id: &artifactav1.Identity{Sub: "human"}}, Store: failingKeyStore{st}}
	if _, ok := a.Identify(bearerReq(raw)); ok {
		t.Fatal("Identify succeeded while the store errored")
	}
}

func TestAPIKeyAuthPassesOtherCredentialsThrough(t *testing.T) {
	st := newKeyStore(t)
	human := &artifactav1.Identity{Sub: "00u123", Email: "a@example.com"}

	// Regression: a non-afa_ bearer (an OIDC id_token) still reaches the provider.
	inner := &recordingAuth{id: human}
	a := &APIKeyAuth{Inner: inner, Store: st}
	if who, ok := a.Identify(bearerReq("eyJhbGciOi.payload.sig")); !ok || who != human || !inner.called {
		t.Fatalf("OIDC bearer: got %v, %v (inner called %v)", who, ok, inner.called)
	}

	// A raw key in the session cookie is not an API-key login: it goes to the
	// inner provider, which can't parse it.
	raw, _ := seedSA(t, st, "ci", time.Now(), nil)
	inner = &recordingAuth{}
	a = &APIKeyAuth{Inner: inner, Store: st}
	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: raw})
	if who, ok := a.Identify(r); ok {
		t.Fatalf("key in cookie authenticated as %v", who)
	}
	if !inner.called {
		t.Fatal("cookie request did not reach the inner provider")
	}
}

func TestAPIKeyAuthRejectsReservedSubjectFromInner(t *testing.T) {
	a := &APIKeyAuth{Inner: &recordingAuth{id: &artifactav1.Identity{Sub: "sa:ci"}}, Store: newKeyStore(t)}
	if who, ok := a.Identify(bearerReq("eyJ.not.akey")); ok {
		t.Fatalf("inner identity with sa: subject accepted as %v", who)
	}
}

func TestAPIKeyAuthThrottlesLastUsedWrites(t *testing.T) {
	st := newKeyStore(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	raw, key := seedSA(t, st, "ci", t0, nil)
	now := t0
	a := &APIKeyAuth{Inner: &recordingAuth{}, Store: st, Now: func() time.Time { return now }}
	lastUsed := func() time.Time {
		k, _, _ := st.APIKeyByHash(key.GetKeyHash())
		return k.GetLastUsedAt().AsTime()
	}

	a.Identify(bearerReq(raw))
	now = t0.Add(30 * time.Second)
	a.Identify(bearerReq(raw))
	if got := lastUsed(); !got.Equal(t0) {
		t.Fatalf("within %v: last_used_at = %v, want first use %v", touchEvery, got, t0)
	}
	now = t0.Add(touchEvery + time.Second)
	a.Identify(bearerReq(raw))
	if got := lastUsed(); !got.Equal(now) {
		t.Fatalf("after %v: last_used_at = %v, want %v", touchEvery, got, now)
	}
}

// End to end through the router: a service account publishes, owns the
// artifact, and /me reports it as a service account.
func TestServiceAccountPublishesAndOwns(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t, dir, nil)
	raw, _ := seedSA(t, srv.Store, "ci-reports", time.Now(), nil)
	srv.Auth = &APIKeyAuth{Inner: fakeAuth{}, Store: srv.Store}
	h := srv.Routes()

	req := httptest.NewRequest(http.MethodPost, "/artifacts?title=Nightly", strings.NewReader("<h1>report</h1>"))
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "text/html")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rr.Code, rr.Body.String())
	}
	var pub struct{ Slug string }
	_ = json.Unmarshal(rr.Body.Bytes(), &pub)
	art, ok, _ := srv.Store.GetArtifact(pub.Slug)
	if !ok || art.GetOwnerSub() != "sa:ci-reports" {
		t.Fatalf("owner = %q, want sa:ci-reports", art.GetOwnerSub())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, bearerReq(raw))
	var me map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &me)
	if me["sub"] != "sa:ci-reports" || me["kind"] != "service_account" {
		t.Fatalf("/me = %v", me)
	}
}
