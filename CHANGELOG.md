# Changelog

All notable changes to ArtifactA are documented here. Versions follow [SemVer](https://semver.org).

## 0.1.0 — 2026-09-22 — First stable release

ArtifactA graduates from its `0.0.x` prototype line to a **stable** release. The full
`0.0.x` history is preserved below; this entry records the changes made for the stable
cut and then rolls up the capabilities accumulated since the `0.0.1` prototype.

### Added

- **Forms work inside artifacts** (`allow-forms`). The artifact sandbox now permits
  `<form>` submission. `allow-same-origin` is deliberately kept **off** — the frame stays a
  null (opaque) origin, so artifact JS still cannot reach the shell's session cookie,
  storage, or same-origin API routes ([ADR-0008](docs/adr/0008-separate-content-origin.md)).
- **External links open in a new tab** ([ADR-0027](docs/adr/0027-external-artifact-links-new-tab.md)).
  A link to an off-site URL inside an artifact now opens a real new tab
  (`window.open(…, "noopener,noreferrer")`) instead of replacing the artifact in-frame. The
  sandbox gains `allow-popups allow-popups-to-escape-sandbox`, scoped to opened windows
  only; the artifact frame itself stays a null-origin `allow-scripts` sandbox.

### Fixed

- **In-artifact navigation no longer nests the viewer.** An artifact renders in a
  null-origin `srcdoc` iframe whose base URL is inherited from the shell (`/a/<slug>`), so
  any navigation that resolved _relative_ used to target the shell and reload the whole
  viewer inside the frame ("ArtifactA inside ArtifactA", ending in "Couldn't load this
  artifact"). Now: in-page `#id` links resolve against the frame's own `about:srcdoc` and
  scroll (driving `:target`); relative / root-relative / query-only links and an empty
  `href` are contained; and forms with a relative or empty action are cancelled
  (absolute-action forms submit normally).
- **`<meta http-equiv="refresh">` inside an artifact is stripped before framing.** It fires
  during parse, before the viewer bridge can intercept it, and would navigate the frame on
  its own (to the shell, or off-site) — an auto-navigation the viewer never intended.

### Since 0.0.1 (the prototype)

Everything shipped across `0.0.2`–`0.0.16`, by theme:

- **Storage & scale** — S3-compatible blob backend (AWS / MinIO / R2 / B2 / Wasabi),
  streamed server-side with no presigned URLs (0.0.2, [ADR-0006](docs/adr/0006-s3-blob-adapter-backend-only.md));
  AWS default credential chain so keyless EKS Pod Identity / IRSA deployments work (0.0.16);
  selectable file / Postgres store ([ADR-0009](docs/adr/0009-postgres-store-adapter.md)); and
  an offline, **resumable** `artifacta migrate` that moves a file-store deployment onto
  Postgres + S3 with the audit chain copied verbatim and verified (0.0.15,
  [ADR-0026](docs/adr/0026-store-blob-migration.md)).
- **Identity & CLI** — OIDC browser SSO + CLI loopback-PKCE public client, login-by-URL
  discovery, refresh tokens, `doctor` / `whoami` / `me` (0.0.3); deployment-anchored
  `artifacta upgrade` + install-by-URL + `GET /version` (0.0.4,
  [ADR-0021](docs/adr/0021-cli-upgrade-version-check.md)); Okta strict-redirect login fix
  (0.0.6); full CLI↔UI parity for sharing / visibility / label and comments (0.0.7); remote
  audit verify + smarter `healthcheck` + `public` visibility alias (0.0.8).
- **Publishing & rendering** — render parity for React / JSX, Markdown (GFM), Mermaid, and
  SVG, with an air-gapped self-contained bundle mode and an `ARTIFACTA_CDN_EGRESS` posture
  flag (0.0.9, [ADR-0023](docs/adr/0023-flag-based-render-egress-and-multiformat.md));
  browser artifact upload behind a toggle (0.0.10,
  [ADR-0024](docs/adr/0024-browser-artifact-upload.md)) and browser upload of a **new
  version** for upload-created artifacts (0.0.14); CLI content-type detection.
- **Discovery & dashboard** — a searchable dashboard with card and list / table views,
  per-column sort / filter, search, and pagination (0.0.12); artifact **search** across the
  caller's visible set with pagination (0.0.11,
  [ADR-0025](docs/adr/0025-artifact-search-and-pagination.md)); an optional searchable
  `description` and owner-email shown on shared / org artifacts (0.0.13).
- **Operations** — production observability: structured JSON logging + Prometheus
  `/metrics`, graceful lifecycle / drain, container healthcheck, and a production compose
  manifest (0.0.5, [ADR-0022](docs/adr/0022-observability-and-server-lifecycle.md)); a
  one-command local dev stack (Postgres + MinIO + Keycloak) with a version-controlled realm
  import (0.0.12).

## 0.0.16 — 2026-09-22

### Fixed

- **S3 blob backend now works with role-based credentials (EKS Pod Identity / IRSA).** `NewBlobS3`
  previously built a static-credentials provider unconditionally, so with no
  `ARTIFACTA_S3_ACCESS_KEY_ID`/`SECRET` the S3 client failed on first use with `static credentials
are empty` — the backend could only authenticate with explicit keys. It now falls back to the AWS
  SDK default credential chain (environment, EKS Pod Identity, IRSA, shared config/SSO, ...) when no
  access key is set, and keeps static keys when they are provided (MinIO/R2/dev). Unblocks keyless
  deployments of both the server and `artifacta migrate`.

## 0.0.15 — 2026-09-22

### Added

- **`artifacta migrate` — offline file-store → Postgres/S3 data migration** (ADR-0026). A new
  operator-run, one-shot, **resumable** CLI subcommand that copies an existing file-store
  deployment (artifacts, versions, grants, comments, blob bundles, and the hash-chained audit
  trail) onto the Postgres store (ADR-0009) and S3 blob backend (ADR-0006), so operators can
  adopt the horizontally-scalable backends without losing data or history. The audit trail is
  copied **verbatim** — a new migration-only `Store.AppendRaw` preserves each event's
  `seq`/`prev_hash`/`hash` and never re-chains — and the destination chain is verified for
  **completeness** (event count + first/last seq + last hash), not just internal consistency.
  Blobs are sha256-verified on copy; grants dedupe by grantee subject or, for email-only invites
  (ADR-0019, empty `grantee_sub`), by email, so distinct invites are never collapsed. Safe to
  re-run: a partial or interrupted migration resumes in place, and a foreign/divergent
  destination is refused **before any write**. Enumeration uses a new `Store.AllArtifacts`; the
  source is read-only and no runtime/publish paths change.

## 0.0.14 — 2026-09-18

### Added

- **Upload a new version from the browser** (ADR-0024). The dashboard now offers an
  owner-only "upload a new version" action that appends an immutable v(n+1) via a
  multipart `POST /artifacts/{slug}/versions` — same allowlist, size cap, and
  render pipeline as the create-upload; sharing and visibility are unchanged.
  It is deliberately limited to artifacts that were **themselves created via the
  upload UI** (new `Artifact.via_upload`): CLI/API-created artifacts still take new
  versions only from the CLI and the browser path returns 409 for them. Gated like
  the create path (upload toggle on, owner-only, same-origin CSRF check).

## 0.0.13 — 2026-09-18

### Added

- **Optional artifact `description`** — free-text metadata a publisher (or an
  assistant) can set to record what an artifact is for. It is **searchable**
  alongside title / slug / label (`GET /artifacts/search?q=`, the SQL path, and
  the dashboard's client-side search), so an agent can find a past artifact by
  more than its title. Set it via `POST /artifacts?description=`, the browser
  upload form, or `artifacta publish --description "…"`; it's returned by the
  metadata and search endpoints and shown as a subtitle in the dashboard.
- **Owner shown on Shared / Org artifacts** — the dashboard now displays
  "owned by &lt;email&gt;" on artifacts you don't own (Shared-with-me and Org), so
  it's clear who published something shared with you. (Uses the existing
  `owner_email`; not shown on your own artifacts.)

## 0.0.12 — 2026-09-18

### Added

- **Dashboard: card and list views.** A toolbar toggles between the existing card
  grid and a new unified **list/table** view that shows every artifact in one place
  with a **Type** column (Mine / Shared with me / Org). List view has per-column
  **sort** (click a header) and per-column **filters** (Type / Title / Visibility /
  Kind) alongside a **search** box (title + link), plus **pagination** with a
  per-page control (12 / 24 / 48 / All). Card view's three sections are now
  **collapsible**. View, page size, sort, and collapsed-section choices persist
  per browser. Rendered client-side from a data island (no-JS fallback, titles
  JS-escaped); `ArtifactView` gains `Created` + `ContentType` for the new columns.
- **One-command local dev stack** (`docs/LOCAL_DEV.md`): `scripts/dev.sh` +
  `infra/docker-compose.dev.yml` stand up **Postgres + MinIO (S3) + Keycloak**
  with a version-controlled realm import (`infra/keycloak/artifacta-realm.json`:
  client `artifacta-web`, users `demo`/`alice`), wait for health, run the API on
  the host in OIDC mode, and seed sample artifacts — so contributors exercise the
  real store/blob/SSO paths without hand-wiring containers.

## 0.0.11 — 2026-09-15

### Added

- **Artifact search** (ADR-0025, spec 9): `GET /artifacts/search` searches everything the caller can
  see (their own artifacts, ones shared with them, and org-visible ones), filtering by free text
  (title / slug / custom label), by an email tied to the artifact, and by visibility — with
  page-based pagination (`page` / `page_size` / `sort_by` / `sort_order`, reusing the shared
  `common/v1` pagination types). Results are always a strict subset of what the viewer can already
  open, deduped by slug, ordered deterministically for stable paging.
- **`Artifact.owner_email`** persisted at publish (server-derived from the authenticated identity),
  so "shared with me by `alice@x.com`" is answerable. It is display/search metadata only — grants
  still bind to the immutable subject and `CanView` never consults it.

### Security

- The `email` filter matches an artifact's `owner_email` across the visible set, but a **grantee's**
  email is matched only on artifacts the caller **owns** — a viewer can never probe or enumerate the
  share-list of an org artifact they don't own.

### Internal

- New `Store.SearchArtifacts` on both backends: SQL pushdown in Postgres (indexed `owner_email`
  column, jsonb `title` match, `COUNT` + `LIMIT/OFFSET`), in-memory in the file store via a shared
  pure `domain.SearchArtifacts`. The store-conformance suite runs both, verifying identical results.

### Notes

- `owner_email` is a denormalized snapshot: empty on artifacts published before this release (no
  subject→email directory to backfill from) and not refreshed if a user's email later changes, so the
  email filter can under-return on old or renamed rows. Existing `GET /artifacts` and the dashboard
  are unchanged (still unpaginated); search is the paginated surface.

## 0.0.10 — 2026-09-11

### Added

- **Upload an artifact from the browser** (ADR-0024, spec 5), behind `ARTIFACTA_UPLOAD_UI` (default
  off). A floating **Upload** button on the dashboard opens a dialog where a signed-in user picks a
  file — HTML, PDF, or an image (PNG/JPEG/GIF/WebP/SVG), up to 25 MiB — and hosts it as an ordinary
  private-by-default, versioned artifact.
  It content-negotiates the existing `POST /artifacts` (multipart for the browser, raw body for the
  CLI — unchanged), decides the content type server-side against an allowlist (never the client's
  header), runs HTML through the same `render.Prepare` pipeline, and serves it through the same
  `CanView`-gated, sandboxed path. A same-origin check backstops the SameSite session cookie against
  CSRF. Off by default keeps the surface dark until an operator opts in.

### Docs

- Roadmap spec for **multi-file artifacts** (entry document + sibling assets) — the next hosting
  capability, scoped as its own multi-week initiative (storage + schema + srcdoc-viewer rework).

## 0.0.9 — 2026-09-11

### Added

- **Render parity: React/JSX, Markdown, Mermaid, SVG** (ADR-0023, spec 7). The publish-time render
  pipeline now handles the artifact types AI assistants actually emit. React/JSX transpiles and, in
  the default air-gapped mode, bundles its dependencies (vendored React 18 + Tailwind) into a
  self-contained document resolved fully in-memory — no temp dir, so it works on a read-only /
  distroless rootfs. Markdown renders to styled HTML with GFM; fenced ` ```mermaid ` blocks render via
  an inlined runtime. SVG and other file types publish with the correct content type.
- **`ARTIFACTA_CDN_EGRESS=allow|deny` posture flag** (default `deny`). It gates BOTH the render
  strategy and the served Content-Security-Policy — on the viewer shell (which a srcdoc'd artifact
  inherits) and `/raw`. `deny` bundles everything self-contained and forbids external hosts (true
  air-gap: an artifact cannot even `fetch` out); `allow` keeps import maps and a permissive `https:`
  CSP so an artifact resolves any dependency from a CDN at view time.
- **CLI content-type detection.** `artifacta publish` derives an artifact's content type from the
  file extension (was hardcoded `text/html`), so Markdown / SVG / JSON publish correctly.

### Fixed

- Tailwind inlining used regexp `ReplaceAll`, whose `$`-expansion corrupted the minified bundle and
  could break it out of its `<script>`; it now splices by byte index (caught in browser QA).

## 0.0.8 — 2026-09-11

### Added

- **Share from the viewer.** The viewer now has an owner-only **Share** button in the top bar that
  opens the same sharing dialog the dashboard already had — set visibility (private / invited / org /
  link), invite people by email, and claim a custom subdomain — without going back to the artifact
  list first. Previously sharing was only reachable from the dashboard or the CLI. The `GET /artifacts/{slug}`
  metadata now returns an owner-only `owner_email` so the dialog can label the owner's own row.
- **Verify the audit trail from a remote.** `artifacta audit verify` now works when logged in to a
  deployment: it fetches your own audit rows over the new owner-scoped `GET /audit` endpoint and
  verifies each row's hash client-side. (Full hash-chain continuity still runs on the server host —
  a caller only ever sees rows that concern them, never the instance-wide trail.)

### Changed

- **`artifacta healthcheck [url]`** now probes the right server: an explicit URL argument if given,
  else the server you're logged in to, else the local listen address. A remote user gets a real
  answer instead of a bogus `127.0.0.1:8080` "connection refused". A bare `host:port` argument is
  accepted (an `http://` scheme is assumed). The distroless container probe is unchanged.
- **`public` is now accepted as a visibility alias for `link`** in both the API and the CLI, since
  people arriving from other tools reach for "public" for a no-login link. It resolves to the same
  VPN-gated `link` level (ADR-0018) — the canonical name shown everywhere stays `link`. The
  set-visibility 400 now lists the valid values.

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
