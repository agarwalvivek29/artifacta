package api

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
)

// touchEvery bounds how often a key's last_used_at is written: at most once per
// key per interval, so a busy bot's publishes don't turn into store writes.
const touchEvery = time.Minute

// APIKeyAuth authenticates service-account API keys (ADR-0028) in front of
// another Auth provider (OIDC or Local). It wraps the provider rather than
// adding a branch to it, so every handler that calls Server.Auth.Identify gets
// service-account support with no changes.
//
//   - `Authorization: Bearer afa_…` is looked up in the key store and NEVER
//     falls through to Inner: a revoked, expired or unknown key fails closed.
//   - Anything else (other bearer tokens, session cookies) goes to Inner
//     unchanged. A key placed in a cookie therefore reaches Inner, which can't
//     parse it, so a service account cannot hold a browser session.
//   - The `sa:` subject prefix is reserved: an Inner identity carrying it is
//     rejected, so no IdP user can impersonate a service account.
type APIKeyAuth struct {
	Inner   Auth
	Store   Store
	Metrics *Metrics     // nil = no metrics
	Logger  *slog.Logger // nil = slog.Default()
	Now     func() time.Time

	mu      sync.Mutex
	touched map[string]time.Time // key id → last last_used_at write
}

func (a *APIKeyAuth) Identify(r *http.Request) (*artifactav1.Identity, bool) {
	if raw := bearer(r); strings.HasPrefix(raw, domain.APIKeyPrefix) {
		who, result := a.identifyKey(raw)
		a.Metrics.recordSAKeyAuth(result)
		if who == nil {
			a.logger().Warn("api key rejected", "prefix", domain.KeyPrefix(raw), "result", result)
			return nil, false
		}
		return who, true
	}
	who, ok := a.Inner.Identify(r)
	if ok && domain.IsServiceAccount(who.GetSub()) {
		a.logger().Warn("rejected non-api-key identity with reserved sa: subject")
		return nil, false
	}
	return who, ok
}

// identifyKey resolves a raw key to its service account's identity. result is
// the bounded outcome label recorded as a metric.
func (a *APIKeyAuth) identifyKey(raw string) (*artifactav1.Identity, string) {
	k, ok, err := a.Store.APIKeyByHash(domain.HashAPIKey(raw))
	if err != nil {
		return nil, "error"
	}
	if !ok {
		return nil, "unknown"
	}
	now := a.now()
	if !domain.KeyUsable(k, now) {
		if k.GetRevokedAt() != nil {
			return nil, "revoked"
		}
		return nil, "expired"
	}
	sa, ok, err := a.Store.ServiceAccountByID(k.GetServiceAccountId())
	if err != nil {
		return nil, "error"
	}
	if !ok || sa.GetDisabledAt() != nil {
		return nil, "disabled"
	}
	a.touch(k.GetId(), now)
	return &artifactav1.Identity{Sub: domain.SASub(sa.GetName())}, "ok"
}

// touch records a key's use, throttled to touchEvery per key. Best-effort: a
// failed write is logged and never fails the request.
func (a *APIKeyAuth) touch(keyID string, now time.Time) {
	a.mu.Lock()
	if a.touched == nil {
		a.touched = map[string]time.Time{}
	}
	if last, ok := a.touched[keyID]; ok && now.Sub(last) < touchEvery {
		a.mu.Unlock()
		return
	}
	a.touched[keyID] = now
	a.mu.Unlock()
	if err := a.Store.TouchAPIKey(keyID, now); err != nil {
		a.logger().Warn("api key last_used_at update failed", "key_id", keyID, "err", err)
	}
}

func (a *APIKeyAuth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *APIKeyAuth) logger() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.Default()
}
