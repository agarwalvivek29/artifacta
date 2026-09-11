# Changelog

All notable changes to ArtifactA are documented here. Versions follow [SemVer](https://semver.org).

## 0.0.7 — 2026-09-11

### Added

- **CLI ↔ UI parity for sharing and review** (spec 6): the `artifacta` CLI now reaches the same
  operations the Share UI already had, over the existing, unchanged API endpoints — so a headless
  or scripted workflow no longer has to drop back to the browser:
  - `artifacta visibility <slug> <private|invited|org|link>` — set visibility explicitly
    (`PATCH …/visibility`), instead of only flipping to `invited` as a side effect of `share`.
  - `artifacta share <slug> <email-or-sub>` — now **invites by email** as well as by subject; it
    auto-detects which and posts `{email}` or `{grantee_sub}` (ADR-0019). Still flips visibility to
    `invited` first.
  - `artifacta unshare <slug> <email-or-sub>` — revoke a grant (`DELETE …/grants/{grantee}`).
  - `artifacta label <slug> <label>` — claim a custom `{label}.{root}` subdomain (`PATCH …/label`,
    ADR-0017). On a deployment without a root domain it prints the canonical `/a/{slug}` link and a
    note rather than a blank line.
  - `artifacta comment add <slug> <text> [--reply <parent-id>]`, `comment ls <slug>`,
    `comment resolve <slug> <id>` — review comments and threaded replies (ADR-0014/0016). `ls`
    groups replies under their root and marks resolved/anchored rows.

### Changed

- **BREAKING (local dev only): `artifacta share` is now remote-only.** Its local-dev store path was
  removed so the whole sharing surface (share/unshare/visibility/label/comment) behaves identically
  and always targets a deployment. This removes a latent bug where local `share a@b.com` stored the
  email as a bogus subject, and the asymmetry of "share works locally but unshare does not." In
  local-dev mode `share` now errors with a login prompt; `publish` and `ls` still work locally.

## 0.0.6 — 2026-09-11

### Fixed

- **CLI login against strict-redirect IdPs (Okta)**: `artifacta login` bound a random loopback
  port and sent it as the `redirect_uri`, which IdPs that match redirect URIs exactly (Okta)
  reject with `400 invalid_request` — so login could not complete. The server now advertises a
  fixed loopback redirect URI (`ARTIFACTA_OIDC_CLI_REDIRECT_URI`, e.g.
  `http://127.0.0.1:53682/callback`) via the CLI discovery endpoint, and `artifacta login <url>`
  binds that exact host:port and sends the matching `redirect_uri`. If the port is busy it errors
  clearly; leaving the var unset keeps the previous ephemeral-port behavior for IdPs that allow any
  loopback port. The redirect URI is validated as an http loopback (127.0.0.1 / localhost / ::1)
  with a port, so a non-loopback address is never bound.

## 0.0.5 — 2026-09-11

### Added

- **Production observability** (ADR-0022): structured JSON request logging (`slog`) and a real
  Prometheus **`GET /metrics`** endpoint — `http_requests_total`, `http_request_duration_seconds`,
  `http_requests_in_flight`, plus Go runtime/process series. Route labels use the matched route
  template (`r.Pattern`), so metric cardinality stays bounded regardless of artifact count. Log
  verbosity via `ARTIFACTA_LOG_LEVEL`; request ids honor an inbound `X-Request-Id` or are generated
  and echoed. Query strings are never logged (token/PII safe).
- **Graceful server lifecycle**: `serve` now runs an explicit HTTP server with timeouts
  (`ReadHeaderTimeout`/`ReadTimeout`/`IdleTimeout`) and drains in-flight requests on SIGINT/SIGTERM,
  so a rollout no longer cuts a publish or download mid-write. `WriteTimeout` is intentionally unset
  so a large artifact download to a slow client is never truncated.
- **`artifacta healthcheck`**: a distroless-friendly container liveness probe (`GET /health`).
- **Production deploy manifest** `infra/docker-compose.prod.yml` — API + Postgres + a private MinIO
  bucket with a binary-based healthcheck, durable volumes, and a restart policy (separate from the
  dev-only `infra/docker-compose.yml`).
- Server single-token identity is now configurable via `ARTIFACTA_TOKEN` / `ARTIFACTA_SUB` /
  `ARTIFACTA_EMAIL`, so a containerized local-auth deploy needs no committed `config.json`.

### Fixed

- `serve` fails loud when local (single-token) auth is selected with an empty token, instead of
  booting a server that silently rejects every request.

## 0.0.4 — 2026-09-11

### Added

- **`artifacta upgrade`** (ADR-0021): deployment-anchored version management. The CLI and server
  ship from one binary per release, so the deployment's running version is the compatibility
  anchor — when logged in, `upgrade` recommends installing the deployment's exact version,
  **upgrading or downgrading** to match (so a newer CLI against an older deployment is told to
  downgrade rather than stranded). Not logged in, it checks GitHub for a newer release and
  reasons about a `min-server-version:` dependency. It advises; it never self-updates.
- **Install-by-URL**: `install.sh` accepts `ARTIFACTA_DEPLOYMENT=<url>` (or `--url`) and installs
  the exact CLI version the deployment runs (via a new unauthenticated **`GET /version`** that
  exposes the deployed version + `min_cli_version` + `capabilities`).
- **`artifacta login` and `doctor`** surface a CLI/deployment version mismatch immediately, with
  the exact command to match it (advice only — the CLI never self-updates).

## 0.0.3 — 2026-09-11

### Added

- **CLI login-by-URL** (ADR-0020): `artifacta login <url>` self-configures from a new
  unauthenticated discovery endpoint `GET /.well-known/artifacta-cli` — no hand-set issuer or
  client id. The CLI is a public client (PKCE, no secret).
- **Refresh tokens** (`offline_access`): the CLI renews its session transparently instead of
  forcing repeated re-logins; a refresh that returns no id_token cleanly prompts re-login.
- **`artifacta doctor`** (connectivity + auth health check) and **`artifacta whoami`**, backed
  by a new authenticated `GET /me`. **`GET /artifacts`** lists the caller's own artifacts.

### Fixed

- CLI no longer silently writes the local store when pointed at a deployment: `publish` /
  `share` error with a login prompt when remote-targeted and logged out, `ls` lists the
  deployment's artifacts, and `audit verify` reports it is a local-store operation.

## 0.0.2 — 2026-09-11

### Added

- **S3-compatible blob backend** (ADR-0006), selectable via `ARTIFACTA_BLOB=s3` alongside the filesystem default. Bytes are read and written strictly server-side and streamed through the app after per-view authorization — no presigned URLs — so every byte stays access-controlled and audited. Works with AWS S3, MinIO, Cloudflare R2, Backblaze B2, and Wasabi; MinIO ships as the self-host default in `infra/docker-compose.yml`.
- Compile-time adapter-contract assertions for every blob/store/auth backend, plus `ListByVisibility` coverage in the shared store conformance suite.

### Fixed

- OIDC `/callback` now surfaces the IdP's `error` / `error_description` (and structured token-endpoint rejections) instead of a misleading "token exchange failed".

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
