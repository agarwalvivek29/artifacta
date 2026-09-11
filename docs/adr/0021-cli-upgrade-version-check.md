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

0. **Co-release invariant.** The CLI and server are one binary; every release tag builds both
   (server image + CLI binaries) at the identical version from the same commit
   (`.github/workflows/release.yml`). So a server version always ships with each CLI version,
   and `cli.Version == server.Version` for a given release. **The deployment's running version
   is therefore the compatibility anchor: the guaranteed-compatible CLI is the one whose version
   matches the deployment.**

1. **Server version endpoint** `GET /version` (unauthenticated, exempt like `/health`) returns
   `{ version, min_cli_version, capabilities[] }` — the deployed build version, the oldest CLI
   the server accepts, and capability flags for future feature-based checks.

2. **Match the deployment (primary path).** When the CLI is logged in to a deployment,
   `artifacta upgrade` anchors on its `/version` and recommends installing that exact version —
   **upgrading or downgrading** — so a user on the latest CLI whose deployment is older is told
   to match it rather than being stranded. `install.sh` accepts a deployment URL
   (`ARTIFACTA_DEPLOYMENT=<url>` or `--url <url>`), queries `<url>/version`, and installs the
   matching CLI. This is the robust default: the CLI always tracks the deployment it talks to.

3. **Release declares its server dependency (fallback signal).** A CLI release may carry a
   `min-server-version: X.Y.Z` marker in its GitHub release body; absent = independent of the
   deployed server version. Used only in the not-logged-in path.

4. **`artifacta upgrade` decision.** Logged in → the deployment-match path (`decideMatch`). Not
   logged in, or the server is unreachable → `decideUpgrade` against the latest GitHub release:
   not newer → "up to date"; newer + independent → prompt the upgrade; newer + server-dependent
   → advise logging in to a deployment to verify compatibility.

5. **Advise, never self-update.** `upgrade` prints guidance + a (version-pinned) install
   one-liner; it does not replace the binary (install method varies: script, GHCR, packages).

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
