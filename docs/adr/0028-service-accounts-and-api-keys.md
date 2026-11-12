# 0028 — Service accounts with admin-minted API keys

**Date**: 2026-11-02
**Status**: Proposed
**Deciders**: Vivek Agarwal
**Spec**: [docs/specs/12-service-accounts.md](../specs/12-service-accounts.md)
**Amends**: [0007](./0007-identity-and-auth.md) (the "Static API keys — rejected" alternative)

---

## Context

ADR-0007 gives artifacta two programmatic identities: a human's CLI session (loopback-PKCE)
and the `local` dev token. A headless agent publishes by **riding a human's stored CLI
session**. That breaks down for non-human publishers — a CI job, a scheduled report, a bot:

- Someone has to log in interactively on the runner, and the refresh token dies with that
  person's SSO session (or their employment).
- Everything the bot publishes is owned by, and audited as, that human.

ADR-0007 rejected "static API keys" because a key **baked into a distributed binary** is
extractable and carries no per-user identity. Neither objection applies to a key that an
admin mints for one named identity, that is stored hashed, expires, can be revoked, and is
delivered once into a secret store. The same model already runs in production for qwproxy
(`quickwit-cli`, `internal/apikey`, k8s-manifests PR #1881), so we reuse it.

---

## Decision

1. **Service account (SA)** = a named, non-human principal (`^[a-z0-9][a-z0-9-]{1,62}$`)
   with subject `sa:<name>`. OIDC subjects never start with `sa:`, so the two namespaces
   cannot collide. Names are never reused, even after the SA is disabled.
2. **The SA owns what it publishes.** `owner_sub = "sa:<name>"`. It can set any visibility
   its key is allowed to use (in particular `org`), share, version, label, and so on,
   exactly as a human owner can. No on-behalf-of identity.
3. **API keys are the only SA credential** (no OIDC client-credentials). Format
   `afa_<43 base64url chars>` (256 bits). Only `sha256(key)` and a 12-character display
   prefix (`afa_` plus 8 characters) are stored. The key is shown once at mint time. TTL is one of `30d`, `90d`,
   `never` (the server's allow-list, `ARTIFACTA_SA_KEY_TTLS`). `last_used_at` is tracked.
   Disabling an SA invalidates all of its keys immediately.
4. **Auth chain.** A new `APIKeyAuth` decorator wraps the existing provider (OIDC or
   local). It checks API keys first, but **only** for `Authorization: Bearer afa_…`. Any
   other bearer value or a cookie falls through unchanged. A malformed or invalid `afa_`
   key fails closed and is never retried as an OIDC token. SA keys are never accepted
   from a cookie, so an SA cannot hold a browser session.
5. **Admin gate.** Only admins can manage SAs and keys. A caller is an admin if:
   - its OIDC id_token's `groups` claim contains an entry of `ARTIFACTA_SA_ADMIN_GROUPS`
     (prod: `DevOps-Engineering-L1,L2,L3`, the same groups as qwproxy), **or**
   - its verified email is in `ARTIFACTA_SA_ADMIN_EMAILS`. This is a break-glass fallback
     for when the IdP does not emit groups.

   `artifacta login` requests the `groups` scope **only when the issuer advertises it**
   in discovery (as qwproxy does). Admin checks use the CLI bearer token only; the browser
   session is unchanged. Some IdPs reject unknown scopes. On Okta's org server,
   the claim also needs the app's groups claim filter. With both lists empty, SA
   management is disabled (404). An SA can never be an admin.

6. **Admin API** is one path, `/serviceaccounts`, with query parameters (create, list,
   disable, key create/list/revoke), mirroring `/qwproxy/serviceaccounts`. Every
   operation writes a hash-chained audit event.
7. **Schema-first.** `ServiceAccount` and `ApiKey` are defined in
   `packages/schema/proto/artifacta/v1/`. Audit actions for SA lifecycle are added to the
   `AuditAction` enum. Both store adapters (FileStore, SQLStore) implement the new methods
   and pass the shared conformance suite.

---

## Consequences

### Positive

- CI and bots publish under their own durable identity. Their output survives staff
  changes, and the audit trail records `sa:<name>` rather than a person.
- No login on the runner: one env var (`ARTIFACTA_API_KEY`).
- The design matches qwproxy, so there is one mental model, one admin group and one
  runbook for the org.

### Negative

- A bearer secret now exists. It is mitigated by hashing, TTLs, revocation, a stale-key
  report (90 days unused) and `last_used_at`, but a leaked `never` key is valid until
  someone revokes it.
- **Artifacts owned by an SA can only be changed through that SA's key.** A human cannot
  edit or delete them unless the SA grants access. If an SA is disabled, its artifacts
  keep their current visibility but become read-only (ownerless in practice). This is
  accepted for now; ownership transfer is out of scope (see spec 12).
- The admin check depends on IdP configuration (the groups claim) that lives outside this
  repo. The email fallback trusts the IdP's `email` claim; an id_token that says
  `email_verified=false` never matches it.
- Unsetting both admin lists only hides admin management (`/serviceaccounts` → 404). Keys
  already minted keep authenticating, and can't be listed or revoked until an admin list is
  set again. To cut every bot off, disable the accounts first.
- New audit actions (`SA_CREATE`, `SA_DISABLE`, `SA_KEY_CREATE`, `SA_KEY_REVOKE`) are
  part of the hash chain. A CLI older than 0.2.0 can't parse them, so `artifacta audit
verify` needs the new CLI, and rolling the server back below 0.2.0 makes the chain fail
  verification at the first service-account event.

### Neutral

- SAs can view `org` artifacts like any authenticated principal, since `CanView` treats
  them as a normal identity.
- The admin API is CLI-only: `/serviceaccounts` requires an `Authorization: Bearer` header,
  so a browser session cookie is never enough. This also keeps a cross-site page from
  driving it with an admin's cookie.

---

## Alternatives Considered

### OIDC client-credentials per bot

Rejected for now. It needs a confidential Okta app per bot and an IdP admin to make each
one. API keys are self-service for artifacta admins. It can be added later behind the
same `sa:` namespace.

### Keep riding a human session

This is the status quo, and the cause of the problem above.

### Personal API keys (keys owned by a human)

Out of scope. The need is non-human identity. A personal key would also bypass SSO
session lifetime.
