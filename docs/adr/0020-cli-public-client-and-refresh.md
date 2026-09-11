# 0020 — CLI public-client login (discovery + PKCE) and refresh tokens

**Date**: 2026-09-11
**Status**: Accepted (implemented 2026-09-11)
**Deciders**: Vivek Agarwal
**Extends**: [ADR-0007 — Identity and Auth](0007-identity-and-auth.md)

---

## Context

ADR-0007 established a pluggable identity provider with browser OIDC SSO and a CLI that sends
an OIDC `id_token` as a Bearer credential. In practice the CLI was hard to adopt against a
deployment: `login` required hand-configured issuer + client id, no refresh token was ever
requested (so the id_token expired and forced repeated re-logins), there was no way to check
connectivity/auth, and several commands silently operated on the local store when pointed at
a remote. We want `artifacta login <url>` to configure everything from the URL, sessions to
persist, and remote-vs-local intent to be explicit.

---

## Decision

1. **Unauthenticated discovery endpoint** `GET /.well-known/artifacta-cli` returns the OIDC
   issuer, the **public** client_id, and the requested scopes (or `{"auth":"local"}` when
   OIDC is not configured). It exposes only public values — never the client secret.
2. **CLI is a public client using PKCE only.** It reuses the web client_id and sends no
   secret. This relies on the deployment running its OIDC client with an **empty client
   secret** (effectively public). An operator whose web client is strictly confidential must
   register a separate public client and expose it (a future optional
   `ARTIFACTA_OIDC_CLI_CLIENT_ID` override); this is the documented escape hatch, not built now.
3. **Refresh tokens.** The CLI requests `offline_access` with `AccessTypeOffline` and renews
   the id_token transparently via an oauth2 `TokenSource`. The Bearer credential stays the
   **id_token** (the server's verifier expects one). If a refresh returns no id_token, the CLI
   clears its stored creds and prompts a fresh login rather than looping on an unusable token.
4. **`GET /me`** returns the authenticated identity (the auth check behind `artifacta doctor`
   / `whoami`); **`GET /artifacts`** returns the caller's own artifacts (remote `ls`).
5. **Explicit remote intent.** Commands route to the server when the CLI is logged in or the
   base URL is non-localhost; otherwise local dev mode. `publish`/`share` fail closed (error +
   login prompt) when remote-targeted and unauthenticated — never a silent local write.

---

## Consequences

### Positive

- `artifacta login <url>` needs nothing else; sessions survive via refresh; `doctor` makes
  misconfiguration obvious; the silent-local-write footgun is gone.
- No new dependencies or auth mechanisms — reuses OIDC + PKCE + refresh, all standard.

### Negative / Neutral

- Depends on the IdP returning an `id_token` on the refresh grant and permitting public/PKCE
  loopback with `offline_access`. Documented as an operator requirement; the no-id_token path
  degrades to a clear re-login rather than a failure.
- Tokens (incl. refresh) remain in the `0600` config file — unchanged from ADR-0007's
  `TODO(hardening)` to move them to the OS keychain.
- **Fixed CLI redirect URI (update 2026-09-11).** A random loopback port cannot be pre-registered
  in IdPs that match redirect URIs exactly (Okta rejects it with `invalid_request`). The server
  therefore advertises a fixed loopback redirect URI via the discovery endpoint
  (`ARTIFACTA_OIDC_CLI_REDIRECT_URI`, e.g. `http://127.0.0.1:53682/callback`); the CLI binds that
  exact port and sends that exact `redirect_uri`, and the operator registers the same one URI in
  the IdP app. Empty falls back to an ephemeral port (only for IdPs that allow any loopback port).

---

## Alternatives Considered

- **Accept an access_token as the Bearer** (broaden server verification): rejected — access
  tokens are not always JWTs/verifiable, and it widens the auth surface. id_token + graceful
  re-login is smaller and matches the existing contract.
- **Server-issued CLI tokens** (mint our own long-lived token + refresh endpoint): rejected
  for now — a whole new auth mechanism (token store, revocation) for little gain over refresh.
- **Dedicated CLI client id**: unnecessary given the empty-secret deployment; kept as the
  documented escape hatch for confidential-client operators.
