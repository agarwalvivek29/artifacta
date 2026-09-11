# Spec: CLI upgrade check with server-aware compatibility

**Issue**: N/A (maintainer-driven)
**Status**: Implemented
**Author**: Vivek Agarwal
**Date**: 2026-09-11
**Services Affected**: `artifacta-api` (server `GET /version` + CLI `upgrade`)

---

## Summary

`artifacta upgrade` checks GitHub for a newer CLI and advises whether to install it. When the
new release depends on a newer server (declared by a `min-server-version:` marker), the CLI
reads the deployment's version from a new `GET /version` endpoint and only recommends the
upgrade if the server is new enough; otherwise it says to upgrade the server first.

---

## Scope

### In Scope

- `GET /version` (unauthenticated): `{ version, min_cli_version, capabilities[] }`.
- `artifacta upgrade` (alias `--upgrade`): GitHub latest-release check + server-aware decision.
- `min-server-version:` release-body convention for declaring a server dependency.

### Out of Scope

- Self-updating the binary (advise only; install methods vary).
- Capability-based (vs version-based) gating — `capabilities` is exposed for future use.

---

## Acceptance Criteria

- [x] `GET /version` returns the deployed version + `min_cli_version` + `capabilities`,
      unauthenticated, and defaults `version` to `dev` when unset.
- [x] `upgrade` reports "up to date" when the running CLI is >= the latest release.
- [x] A newer, server-independent release (no marker) prompts the upgrade unconditionally.
- [x] A newer, server-dependent release prompts the upgrade only when the deployment's version >= `min-server-version`; otherwise it advises upgrading the server first.
- [x] Not logged in to a deployment → advise logging in to check, or upgrading at discretion.

---

## Technical Design

See [ADR-0021](../adr/0021-cli-upgrade-version-check.md). The decision is a pure function
(`decideUpgrade`) unit-tested across every branch; `cmpVersion` does numeric dotted-version
comparison; `parseMinServerVersion` reads the release-body marker; `serverVersion` reads
`GET /version`.

---

## Testing Plan

- Server: `GET /version` (fields + unauthenticated + dev default).
- CLI: `cmpVersion` table, `parseMinServerVersion` (present/absent/blockquote/case), `serverVersion`
  via httptest, and `decideUpgrade` across all branches (up-to-date, dev, independent,
  dependent-no-server, dependent-compatible, dependent-blocked).

---

## References

- Related ADR: [0021](../adr/0021-cli-upgrade-version-check.md); builds on
  [0020](../adr/0020-cli-public-client-and-refresh.md).
