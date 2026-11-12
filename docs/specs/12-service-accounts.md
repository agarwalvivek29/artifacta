# Spec: Service accounts (API-key identities for agents, bots and CI)

**Issue**: N/A (maintainer-driven)
**Status**: Implemented
**Author**: Vivek Agarwal
**Date**: 2026-11-02
**Services Affected**: `artifacta-api` (server, CLI, both store adapters), `packages/schema`

---

## Summary

Add service accounts: named non-human principals (`sa:<name>`) that own API keys minted by
admins. A bot or CI job publishes with `ARTIFACTA_API_KEY=afa_…` and owns what it
publishes, so it can set visibility to `org` without a human login. See
[ADR-0028](../adr/0028-service-accounts-and-api-keys.md). The design mirrors qwproxy's
service accounts (`~/aspora/quickwit-cli`).

---

## Background and Motivation

Today a headless publisher must ride a human's CLI session (ADR-0007). That needs an
interactive login on the runner, dies with the human's session, and attributes the bot's
output to that person. Teams want scheduled reports and CI summaries on artifacta under a
durable identity.

---

## Scope

### In Scope

- Proto: `ServiceAccount`, `ApiKey` messages; new `AuditAction` values.
- Store: SA and key CRUD in `FileStore` and `SQLStore`, covered by the conformance suite.
- Auth: `APIKeyAuth` provider decorator (`Bearer afa_…`), `sa:<name>` identity.
- Admin gate: `groups` claim (`ARTIFACTA_SA_ADMIN_GROUPS`) or email allow-list
  (`ARTIFACTA_SA_ADMIN_EMAILS`). `artifacta login` requests the `groups` scope when the
  issuer advertises it. Admin checks use the CLI bearer token only (the admin API is CLI-only).
- Admin API on `/serviceaccounts` (single path, query params).
- CLI: `artifacta sa create|list|disable`, `artifacta sa key create|list|revoke`; every
  existing remote command accepts `ARTIFACTA_API_KEY` (env only, so keys stay out of shell
  history) in place of the stored login.
- `GET /me` reports `kind: "service_account"` for an SA.
- Docs: README section, `.env.example`, AGENTS.md, ARCHITECTURE.md (Rule 15).

### Out of Scope

- OIDC client-credentials for SAs.
- Personal (human-owned) API keys.
- Transferring artifact ownership between an SA and a human.
- Per-key scopes or permissions (a key carries the SA's full owner powers).
- A dashboard UI for SA management (CLI and API only).
- The k8s-manifests change that sets the env vars (a separate PR in that repo, needs
  approval).

---

## Acceptance Criteria

- [ ] Given an admin (groups claim matches or email allow-listed), when they run
      `artifacta sa create ci-reports`, then the SA exists and an audit event is written.
- [ ] Given a non-admin, when they call any `/serviceaccounts` operation, then the response
      is 403 and an audit event with `allowed=false` is written.
- [ ] Given a caller whose token carries no `groups` claim at all (a login from before this
      feature), the 403 says to run `artifacta login` again; a caller whose claim is present
      but doesn't match gets a plain 403.
- [ ] Given both admin lists are empty, when anyone calls `/serviceaccounts`, then the
      response is 404 (management off; existing keys keep working).
- [ ] Given an admin authenticated only by a browser session cookie, when they call
      `/serviceaccounts`, then the response is 401 (CLI bearer required).
- [ ] Given an SA, when an admin runs `sa key create ci-reports --ttl 90d`, then the raw
      key is returned once, and only its hash and prefix are persisted.
- [ ] A TTL outside `ARTIFACTA_SA_KEY_TTLS` is rejected with 400.
- [ ] Given a valid key, when the CLI publishes with `ARTIFACTA_API_KEY`, then the artifact's
      `owner_sub` is `sa:ci-reports` and the audit principal is `sa:ci-reports`.
- [ ] Given an SA-owned artifact, the SA can set visibility to `org`, add a version,
      grant/unshare, set a label, and resolve comments.
- [ ] A revoked, expired, or disabled-SA key returns 401, and is not retried as OIDC.
- [ ] A non-`afa_` bearer token still authenticates through OIDC unchanged (no regression).
- [ ] An `afa_` key sent as the `hn_session` cookie is not accepted.
- [ ] An SA calling `/serviceaccounts` gets 403 (an SA is never an admin).
- [ ] Disabling an SA invalidates all of its keys immediately; its name cannot be reused.
- [ ] `sa key list` shows prefix, created-by, expiry, last-used and status
      (`active|expired|revoked|stale`, where stale = unused for 90d).
- [ ] FileStore and SQLStore pass the same SA/key conformance tests.

---

## Technical Design

### Architecture Overview

```
Request ──► APIKeyAuth.Identify
              │ Bearer afa_… ? ── yes ─► Store.APIKeyByHash(sha256) ─► KeyUsable? ─► Store.ServiceAccountByID
              │                          ─► active? ─► Identity{sub:"sa:<name>"} (+ throttled TouchAPIKey) | 401
              │ no
              ▼
            inner provider (OIDCProvider | Local)  — unchanged
```

`OIDCProvider.identifyBearer` additionally returns the `groups` claim and whether it was
present (`Identity.groups`, `Identity.groups_claim_present`), so `IsAdmin(who)` can decide.
The browser session cookie is unchanged. The identity carries groups at runtime only;
they are never persisted.

### API Changes

#### New Endpoints

All need an authenticated admin, are rate-limited, and are audited. There is one path,
so a fronting gateway needs a single route.

```
POST   /serviceaccounts                         {name, description?}            → ServiceAccount
GET    /serviceaccounts                                                         → {service_accounts:[…]}
DELETE /serviceaccounts?name=N                                                  → 204  (disable)
POST   /serviceaccounts?name=N                  {ttl, description?}             → {id, api_key, expires_at}
GET    /serviceaccounts?name=N                                                  → {api_keys:[…]}
DELETE /serviceaccounts?name=N&key_id=K                                         → 204  (revoke)
```

#### Modified Endpoints

- All authenticated endpoints accept `Authorization: Bearer afa_…`.
- `GET /me` adds `kind` (`user` | `service_account`).
- `artifacta login` adds `groups` to its requested scopes when the issuer's discovery
  document lists it in `scopes_supported`. Token refresh sends no scope, so existing logins
  keep working without `groups` until the next login.

### Data Model Changes

Proto (`packages/schema/proto/artifacta/v1/service_account.proto`):

```proto
message ServiceAccount {
  string id = 1; string name = 2; string description = 3;
  string created_by_sub = 4; string created_by_email = 5;
  google.protobuf.Timestamp created_at = 6;
  google.protobuf.Timestamp disabled_at = 7;  // unset = active
  string disabled_by_sub = 8;
}
message ApiKey {
  string id = 1; string service_account_id = 2;
  string key_hash = 3;   // hex sha256; never returned by the API
  string prefix = 4; string description = 5;
  google.protobuf.Timestamp created_at = 6;
  google.protobuf.Timestamp expires_at = 7;   // unset = never
  google.protobuf.Timestamp revoked_at = 8;
  google.protobuf.Timestamp last_used_at = 9;
  string created_by_sub = 10; string created_by_email = 11;
}
```

`AuditAction` gains `SA_CREATE`, `SA_DISABLE`, `SA_KEY_CREATE`, `SA_KEY_REVOKE`. SA audit rows
set `slug = "sa:<name>"` so they can't be mistaken for an artifact slug.

`Identity` (artifact.proto) gains runtime-only `repeated string groups = 3` and
`bool groups_claim_present = 4`.

SQLStore: two gorm-automigrated tables, `service_accounts` (unique `name`) and `api_keys`
(unique `key_hash`, index on `service_account_id`). FileStore: two protojson maps
persisted with the rest of its state. `last_used_at` writes are throttled (at most once
per minute per key) so publish traffic does not turn into write traffic.

### Configuration

| Env                         | Default         | Meaning                                          |
| --------------------------- | --------------- | ------------------------------------------------ |
| `ARTIFACTA_SA_ADMIN_GROUPS` | _(empty)_       | Comma-separated IdP groups allowed to manage SAs |
| `ARTIFACTA_SA_ADMIN_EMAILS` | _(empty)_       | Comma-separated break-glass admin emails         |
| `ARTIFACTA_SA_KEY_TTLS`     | `30d,90d,never` | Lifetimes a key may be minted with               |
| `ARTIFACTA_API_KEY` (CLI)   | —               | Use this SA key instead of the stored login      |

Prod values (k8s-manifests, separate PR): groups `DevOps-Engineering-L1,DevOps-Engineering-L2,DevOps-Engineering-L3`.

### Dependencies

None new.

---

## Security Considerations

- Keys hold 256 bits of entropy and are stored as sha256. Lookup is by hash, so there is
  no timing side channel on the raw key. Raw keys never appear in logs (the logging
  middleware already redacts `Authorization`; verify this in tests).
- Fail closed: an invalid `afa_` key returns 401 without OIDC fallback. A key in a cookie
  is ignored.
- SA admin is never granted to an SA identity. The `sa:` prefix is reserved: an OIDC token
  whose `sub` starts with `sa:` is rejected.
- Admin gate failure modes: a missing groups claim means not-admin, never admin.
- Every SA lifecycle operation, allowed or denied, is audited.
- Rate limiting reuses `contentPerMinute`.

---

## Observability

- **Logs**: SA lifecycle at info (name, actor, never the key); auth failures for `afa_`
  keys at warn with the prefix only.
- **Metrics**: `artifacta_sa_key_auth_total{result}` with result `ok|unknown|revoked|expired|disabled|error`.
  Only API-key attempts are counted: `Identify` runs several times per request on some paths, so
  counting OIDC/local calls would overstate traffic.
- **Alerts**: none in v1. The stale-key status in `sa key list` covers hygiene.

---

## Testing Plan

**gstack test plan**: produced by `/plan-eng-review` (path to be linked).

### Unit Tests

- Key generation, hashing, prefix; TTL parsing and allow-list; admin decision (groups,
  emails, both empty, SA caller); `keyStatus`.
- `APIKeyAuth`: valid, revoked, expired, disabled SA, malformed, non-`afa_` passthrough,
  cookie rejection.
- Conformance: SA/key CRUD on FileStore and SQLStore.

### Integration / E2E Tests

- Admin creates SA, mints key → CLI publishes with `ARTIFACTA_API_KEY` → set `org` →
  a second user views it → admin revokes → next publish is 401.
- Non-admin 403; feature-off 404.

---

## Migration / Rollout Plan

- **Database migrations**: yes, gorm automigrate adds two tables (additive).
- **Breaking changes**: none. Non-`afa_` bearer and cookie paths are unchanged.
- **Feature flag**: implicit. SA management stays off until an admin list is set.
- **Rollback**: unset the admin env vars (management off). Existing keys keep working
  until revoked, or roll back the image (the tables are ignored).
- **IdP prerequisite**: the Okta artifacta app (`0oa26v98964ehrPjR1d8`) must emit
  `groups` for the `groups` scope. Until then, use `ARTIFACTA_SA_ADMIN_EMAILS`.

---

## Open Questions

| Question                                                                       | Owner | Status   |
| ------------------------------------------------------------------------------ | ----- | -------- |
| Does the artifacta Okta app have a groups claim filter configured?             | Vivek | Open     |
| Ownership transfer / "human co-owner" for SA artifacts when an SA is disabled? | Vivek | Deferred |

---

## References

- Related ADR: [0028](../adr/0028-service-accounts-and-api-keys.md), [0007](../adr/0007-identity-and-auth.md)
- Prior art: `~/aspora/quickwit-cli` `internal/apikey/serviceaccount.go`, `internal/cli/serviceaccount.go`; k8s-manifests PR #1881
