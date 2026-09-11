# 0021 — CLI upgrade check with server version/compatibility endpoint

**Date**: 2026-09-11
**Status**: Accepted (implemented 2026-09-11)
**Deciders**: Vivek Agarwal
**Related**: [ADR-0020 — CLI public-client login + refresh](0020-cli-public-client-and-refresh.md)

---

## Context

`artifacta upgrade` should tell a user when a newer CLI exists and whether it is safe to
install. Some CLI enhancements are self-contained; others depend on a matching server version
(a new endpoint, a changed response). Advising "upgrade" blindly can leave a user with a CLI
that talks to a too-old deployment. To decide, the CLI needs two facts it does not have today:
the **deployed server version** and whether the **new CLI release requires** a newer server.

---

## Decision

1. **Server version endpoint** `GET /version` (unauthenticated, exempt like `/health`) returns
   `{ version, min_cli_version, capabilities[] }` — the deployed build version, the oldest CLI
   the server accepts, and capability flags for future feature-based checks.
2. **Release declares its server dependency.** Each CLI release may carry a
   `min-server-version: X.Y.Z` marker in its GitHub release body. Absent = the release is
   independent of the deployed server version. The CLI reads this from the GitHub
   latest-release API.
3. **`artifacta upgrade` decision** (pure function `decideUpgrade`):
   - Not newer than the running CLI → "up to date".
   - Newer and **independent** (no marker) → prompt the upgrade unconditionally.
   - Newer and **server-dependent**: if logged in to a deployment, read `GET /version`; prompt
     the upgrade only when `server_version >= min-server-version`, otherwise advise upgrading
     the server first. If not logged in, advise logging in to check (or upgrading at the user's
     discretion).
4. **Advise, never self-update.** `upgrade` prints guidance + the canonical install one-liner;
   it does not replace the binary (install method varies: script, GHCR, package managers).

---

## Consequences

### Positive

- Users get a correct, deployment-aware upgrade recommendation instead of a blind "update".
- The release process controls compatibility via a simple, visible marker — no code change to
  declare a new dependency, and no server oracle for future CLI versions.
- `capabilities` leaves room for capability-based checks later without another endpoint.

### Negative / Neutral

- The upgrade check requires network access to GitHub (unauthenticated, rate-limited); it fails
  soft with a clear error.
- The `min-server-version` marker is a release-process convention that must be kept accurate
  when a CLI change genuinely needs a newer server.

---

## Alternatives Considered

- **Server declares the newest compatible CLI**: rejected — the server cannot know about CLI
  versions released after it. Expressing the dependency on the release side (min-server-version)
  plus the server's own version is sufficient and future-proof.
- **Auto-self-update the binary**: rejected for now — install methods differ and silent binary
  replacement is risky; advising the user keeps them in control.
