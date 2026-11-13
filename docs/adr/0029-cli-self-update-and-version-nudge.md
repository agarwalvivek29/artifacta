# 0029 — CLI self-update and a daily version-mismatch nudge

**Date**: 2026-11-13
**Status**: Accepted
**Deciders**: Vivek Agarwal
**Related**: supersedes decision 5 ("advise, never self-update") of
[ADR-0021](0021-cli-upgrade-version-check.md); the rest of ADR-0021 stands.

---

## Context

ADR-0021 made `artifacta upgrade` deployment-aware but advice-only: it prints a `curl … | sh`
line and the user runs it. In practice nobody runs `upgrade` unprompted, and the only automatic
mismatch notice is right after `artifacta login` and in `doctor`. A user who logged in once never
hears that the deployment moved on (0.1.1 → 0.2.0 shipped service accounts and a new audit event
shape that older CLIs cannot verify). Our sibling CLI, `qw` (quickwit-cli), already replaces its
own binary from GitHub Releases with SHA-256 verification, and that has worked well.

---

## Decision

1. **`artifacta upgrade` installs.** It resolves a target version, downloads
   `artifacta_<ver>_<os>_<arch>.tar.gz` and `checksums.txt` from the GitHub release, verifies the
   SHA-256 (fails closed when the checksum file or entry is missing, unlike `install.sh`'s best
   effort), extracts the binary and atomically renames it over the running executable (staged in
   the same directory). Running the command is the consent; nothing updates in the background.
   - Target precedence: `--version X` > `--latest` > the logged-in deployment's `/version`
     (match it, up or down, as ADR-0021) > the latest GitHub release.
   - `--check` prints current and target and installs nothing (the old advice behaviour).
   - When the binary cannot be replaced (no write access to its directory, e.g. `/usr/local/bin`
     installed with sudo; Windows; a dev build without an explicit target), it prints why and the
     version-pinned `install.sh` one-liner instead of failing silently.
2. **A daily nudge on normal commands.** Commands that talk to a deployment check its `/version`
   at most once every 24 hours (cached in `$ARTIFACTA_HOME/update-check.json`, 3 s timeout) and,
   on a mismatch, print one line to **stderr**: `run: artifacta upgrade`. The check never fails or
   delays the command beyond the timeout. It is skipped for dev builds, when `ARTIFACTA_API_KEY`
   or `CI` is set (bots and pipelines pin their own version), and when
   `ARTIFACTA_NO_UPDATE_CHECK=1`.

---

## Consequences

### Positive

- One command to get compatible again, and users actually learn they need it.
- Same artifact and checksum the installer uses, so a self-update and a fresh install produce
  identical binaries.

### Negative / Neutral

- A binary installed under a root-owned directory still needs `sudo artifacta upgrade` (or the
  printed installer line); we do not escalate privileges ourselves.
- Package-manager or container installs can be overwritten out from under the manager; those
  users should keep using their manager (the GHCR image is pinned by tag anyway).
- One extra unauthenticated `GET /version` per user per day.
- 0.2.0 and older CLIs have no self-update; their users upgrade by hand one last time.

---

## Alternatives Considered

- **Keep advice-only, just add the nudge**: still leaves a copy-paste step with a sudo prompt,
  the thing users skip.
- **Silent background auto-update**: rejected for the reasons in ADR-0021; replacing a binary
  without the user asking is a footgun.
- **Nudge on stdout or on every command**: stdout breaks scripts that parse output (`--json`),
  and every-command nags train people to ignore them.
