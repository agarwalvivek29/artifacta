# Spec: CLI login-by-URL, health check, and refresh tokens

**Issue**: N/A (maintainer-driven)
**Status**: Implemented
**Author**: Vivek Agarwal
**Date**: 2026-09-11
**Services Affected**: `artifacta-api` (server endpoints + CLI)

---

## Summary

Make the `artifacta` CLI a first-class client of a deployed server: `artifacta login <url>`
bootstraps everything from the URL alone (no hand-configured issuer/client id), sessions
survive via OIDC refresh tokens, `artifacta doctor` verifies connectivity + auth, and the
CLI stops silently falling back to the local store when it is pointed at a deployment.

---

## Background and Motivation

The CLI previously required the user to hand-set `ARTIFACTA_OIDC_ISSUER` +
`ARTIFACTA_OIDC_CLIENT_ID` before `login` worked, requested no `offline_access` (so the
id_token expired and forced repeated re-logins), had no way to check connectivity/auth, and
silently wrote the local filesystem store — printing a `localhost` link — when `publish` was
run against a configured remote without a token. Each of these is a real adoption blocker for
"install the CLI, point it at our server, publish."

---

## Scope

### In Scope

- Unauthenticated discovery endpoint `GET /.well-known/artifacta-cli` (issuer, public
  client_id, scopes) so the CLI configures itself from the base URL.
- `GET /me` (authenticated identity) and `GET /artifacts` (caller's own artifacts).
- `artifacta login [<url>]`, `artifacta doctor`, `artifacta whoami`.
- OIDC refresh tokens (`offline_access` + `AccessTypeOffline`) with proactive, transparent
  renewal in the CLI.
- Footgun fixes: `publish`/`share` error (never silently write local) when remote-targeted
  and logged out; `ls` lists the deployment's artifacts; `audit verify` errors against a remote.

### Out of Scope

- Slim CLI-only build that compiles `serve` out (separate follow-up).
- OS-keychain token storage (existing `TODO(hardening)`).
- A dedicated CLI OIDC client id — the deployment runs its OIDC client with an empty secret
  (public), so the CLI reuses the web client_id via PKCE.

---

## Acceptance Criteria

- [x] Given only `artifacta login <url>`, when the server has OIDC configured, the CLI reads
      issuer/client_id from discovery and completes a loopback PKCE login with no secret.
- [x] Given a valid refresh token, when the id_token has expired, the next command mints a
      fresh id_token transparently (no re-login).
- [x] Given a refresh that returns no id_token, the CLI clears creds and prints
      `session expired — run: artifacta login <url>` (never loops).
- [x] Given a remote target and no token, `publish`/`share` error with a login prompt and do
      NOT write the local store.
- [x] Given a logged-in CLI, `artifacta doctor` reports ✓ connect, ✓ discovery, ✓ auth.
- [x] `GET /me` returns the identity (200) or 401; `GET /artifacts` is owner-scoped and 401 for anon.
- [x] `GET /.well-known/artifacta-cli` is reachable without credentials and never exposes a secret.

---

## Technical Design

### API Changes — New Endpoints

```
GET /.well-known/artifacta-cli   (unauthenticated, exempt like /health)
  → { "auth":"oidc", "issuer":..., "client_id":..., "scopes":[...] }  | { "auth":"local" }

GET /me                          (authenticated)
  → 200 { "sub":..., "email":... }  | 401

GET /artifacts                   (authenticated, owner-scoped)
  → 200 [ { slug, title, visibility, latest_version, url, created_at } ]  | 401
```

Responses are inline JSON, matching the existing publish/metadata convention (no proto).

### CLI

```
login <url>  → GET discovery → set issuer/client → loopback PKCE (offline_access) → persist
               id_token + refresh_token + expiry + LoggedIn
freshToken() → valid cached id_token, else refresh via TokenSource, else error (login prompt)
doctor       → GET /health → discovery → freshToken + GET /me
publish/...  → remoteTarget(logged in OR non-localhost URL) ? remote(freshToken) : local dev
```

### Dependencies

None new — reuses `golang.org/x/oauth2` and `github.com/coreos/go-oidc/v3`.

---

## Security Considerations

- Public client + PKCE: no client secret is stored in or sent by the CLI. The discovery
  endpoint exposes only public values (issuer, client_id, scopes).
- Refresh + id_token are stored in the `0600` config file (existing `TODO(hardening)`: keychain).
- `/me` and `/artifacts` are fail-closed (401) and owner-scoped; the discovery endpoint is
  read-only and unauthenticated by design.
- Operator requirement: the IdP client must permit the loopback redirect URIs, public/PKCE,
  `offline_access`, and return an `id_token` on the refresh grant.

---

## Testing Plan

### Unit / Integration Tests

- Server: `/me` (200/401), `/artifacts` (owner-scoped/401), discovery (oidc/local, no secret leak).
- CLI: `freshToken` (valid cached / refresh→new id_token / refresh→no id_token clears creds /
  not-logged-in), `remoteTarget`/`isLocalhost`/`tokenExpired`, footgun (`publish`/`audit`
  error on remote-target-logged-out), discovery fetch.

---

## Migration / Rollout Plan

- **Breaking changes**: none — local dev mode and the browser SSO flow are unchanged; new
  config fields default to empty. `login` without a URL keeps prior behavior.
- **Rollback plan**: revert the PR; stored refresh tokens are ignored by older binaries.

---

## References

- Related ADR: [0020](../adr/0020-cli-public-client-and-refresh.md) (extends
  [0007](../adr/0007-identity-and-auth.md))
