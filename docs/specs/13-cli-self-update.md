# Spec: CLI self-update and version-mismatch nudge

**Issue**: N/A (maintainer-driven)
**Status**: Implemented
**Author**: Vivek Agarwal
**Date**: 2026-11-13
**Services Affected**: `artifacta-api` (CLI only; no server change)

---

## Summary

`artifacta upgrade` now downloads, verifies and installs the right CLI instead of printing an
install command, and remote commands print a one-line stderr nudge (at most daily) when the CLI
and the deployment disagree. See [ADR-0029](../adr/0029-cli-self-update-and-version-nudge.md).
Ported from `qw upgrade` (`~/aspora/quickwit-cli/internal/selfupdate`).

---

## Scope

### In Scope

- `internal/selfupdate`: GitHub-release download, SHA-256 verification against `checksums.txt`
  (fail closed), tar.gz extraction, atomic same-directory replace of the running executable.
- `artifacta upgrade [--check] [--latest] [--version X]`. Default target: the logged-in
  deployment's version, else the latest release.
- Fallback advice (pinned `install.sh` line) when the binary can't be replaced: permission denied,
  Windows, or a dev build with no explicit target.
- Daily nudge on remote commands, cached in `$ARTIFACTA_HOME/update-check.json`; skipped for dev
  builds, `ARTIFACTA_API_KEY`, `CI`, `ARTIFACTA_NO_UPDATE_CHECK=1`.
- `login`/`doctor` mismatch notices point at `artifacta upgrade`.

### Out of Scope

- Background or silent updates.
- Privilege escalation (no built-in sudo).
- Windows self-replace (falls back to advice).

---

## Acceptance Criteria

- [x] `upgrade` installs the deployment's version when logged in (upgrade or downgrade).
- [x] `--version` and `--latest` override the target; `--check` installs nothing.
- [x] A bad checksum, a missing `checksums.txt` or a missing entry aborts without touching the
      installed binary.
- [x] Permission denied on the install directory prints the pinned installer line and exits non-zero.
- [x] Already on the target → "already up to date", no download.
- [x] A remote command prints the nudge to stderr once per 24 h on a mismatch, never on stdout,
      and never fails the command when `/version` is unreachable.
- [x] No nudge for dev builds, API-key bots, `CI`, or `ARTIFACTA_NO_UPDATE_CHECK=1`.

---

## Testing Plan

- Unit (`internal/selfupdate`): asset naming, tag normalisation, download+verify+extract against
  an httptest release server, bad checksum, missing checksums, missing entry, atomic replace.
- CLI: target resolution (flags, deployment, latest), `--check`, up-to-date, permission fallback,
  nudge cadence/skip rules/stderr-only.
- Manual: build 0.2.0 and a fake newer release served locally; run `artifacta upgrade` end to end
  and confirm the binary on disk changes and `artifacta version` reports the new version.
