# Changelog

All notable changes to ArtifactA are documented here. Versions follow [SemVer](https://semver.org).

## 0.0.1 — 2026-09-10

First tagged release of **ArtifactA** — a self-hostable host for AI-generated artifacts.

### Features

- **Publish → access-controlled link.** Private-by-default artifacts with per-artifact RBAC (`private` / `invited` / `org` / `link`) and an inbuilt, tamper-evident hash-chained audit trail.
- **Subdomain hosting** — reach an artifact at `{slug|label}.{root-domain}` (ADR-0017); no-login, VPN-gated `link` visibility (ADR-0018).
- **Owner Share UI** + per-deployment org navbar branding; **invite by email** (ADR-0019).
- **Selectable metadata store** — `file` (zero-dep default) or **Postgres** via GORM with schema **auto-migration** on startup (ADR-0009).
- **One binary, two roles** — `artifacta serve` (server) and the `artifacta` CLI (`publish`, `login`, `ls`, `share`, `versions`, `audit verify`, `version`).

### Distribution

- Multi-arch **Docker images** on GHCR: `ghcr.io/agarwalvivek29/artifacta` (server; also contains the CLI) and `ghcr.io/agarwalvivek29/artifacta-cli` (slim, CLI-only, for CI/scripts).
- Cross-platform **CLI binaries** (macOS/Linux/Windows, amd64/arm64) attached to the GitHub Release, with checksums.
