# ArtifactA v2 — Build Log

> Durable record of the multi-agent build. **Target branch: `main`** (ADR-0003).
> PRD: `_bmad-output/planning-artifacts/prds/prd-ArtifactA-2026-08-09/prd.md`
> Task backlog: [tasks.md](tasks.md) · Dependency tree: [task-tree.md](task-tree.md)

## Orchestration model

- Each task = a worktree branch off `main` → PR into `main` → I review (FR criteria + tests
  green + clean/modular + concise comments) → auto-merge after review (per user, 2026-08-12).
  Dependent tasks re-branch off updated `main`. Infra-gated items = mocks/fixtures + flagged.
- Worktree agents symlink `node_modules` + `.husky/_` so hooks run (commitlint = lower-case
  subjects; never `--no-verify`). `main` always builds; every wave integration-tested.

## Status — P0 substantially COMPLETE (16 PRs merged, suite green)

**Built & merged:** streaming/atomic storage · `CanView` (slug-scoped) + full test matrix ·
`ListByGrantee`/`ListVisibleTo` · audit-integrity verifier · rate-limit + body-size guard ·
env config · OIDC browser SSO + CLI loopback login + `Bearer` API auth · publish API ·
`artifacta-publish` Skill · share/grants + set-visibility · dashboard (Mine/Shared/Org) +
sign-in · render-parity pipeline (esbuild, increment 1) · end-to-end flow test.

**Remaining:**

- **Render increment 2 (GATED on user)** — vendor the CDN deps (react/etc.) esbuild bundles
  against; validate real parity. Needs a real Claude artifact from the user. (FR15)
- **Content origin (FR16)** — DEFERRED (user chose C): keep same-origin null-origin sandbox;
  signed-per-view-token hardening is a follow-up.
- **Deploy (FR25–27)** — DEFERRED (needs infra approval): docker-compose + TLS + metrics.
- **Security gate (FR32)** — offered: a consolidated `/review` or `/cso` pass over the merged
  auth surface (each auth PR was already diff-reviewed for fail-closed + JWKS verify).

## Decisions (source of truth: PRD memlog + ADRs 0003–0012)

- **ArtifactA** = productization of `here.now`. P0: any AI assistant can publish (ADR-0012).
  OIDC SSO + CLI loopback-PKCE; CLI sends id_token as `Bearer` (JWKS-verified); token in 0600
  config, keychain = follow-up (ADR-0007). VPN-fronted; RBAC + audit retained (ADR-0011).
  Visibility PRIVATE/INVITED/ORG (ORG = any employee). Single-file bundle (ADR-0004); streaming
  Blob (ADR-0005); S3 backend-only later (ADR-0006); Postgres later (ADR-0009). Separate
  content origin (ADR-0008, deferred); render harness bundle-at-publish (ADR-0010).

## Findings

- **F-1 (RESOLVED, PR #5)** — `CanView` now matches grantee AND slug (defense-in-depth).
- **F-2 (RESOLVED, 5368488)** — viewer shell CSP was `default-src 'none'` with no
  `connect-src`, so the shell's `fetch('/a/{slug}/raw')` was blocked → "Could not load this
  artifact." Missed by unit/e2e (they hit `/raw` directly, never rendered the shell under CSP).
  Caught in **live browser QA** against Keycloak. Fixed: added `connect-src 'self'` + a
  regression test (`viewer_test.go`).

## Waves (all MERGED ✓, integration green)

- **Wave 1** #1–#4 — streaming Blob, atomic writes, CanView/NewSlug tests, env config.
- **Wave 2** #5–#8 — slug-scope CanView (F-1), ListByGrantee/ListVisibleTo, audit verifier,
  rate-limit + body-size guard.
- **Wave 3** #9–#12 — publish Skill, OIDC SSO, publish API (+maxBytes), CLI Bearer client.
- **Wave 4** #13–#14 — share/grants + set-visibility, dashboard + sign-in.
- **Wave 5** #15–#16 — render pipeline (esbuild, increment 1), end-to-end flow test.
- Docs: ADRs 0003–0012; ARCHITECTURE.md VPN update (ADR-0011).

## Log

- **2026-08-12** — Waves 1–2 (#1–#8) merged. F-1 recorded + resolved (#5).
- **2026-08-12** — Wave 3 (#9–#12): identity + publish core. Each auth diff independently
  built + reviewed. maxBytes debt cleared (#11).
- **2026-08-12** — Wave 4 (#13–#14): UI + RBAC (html/template escaping). Content-origin
  revealed a cross-origin-auth decision → user chose defer (C).
- **2026-08-12** — Wave 5 (#15–#16): render bundle-at-publish increment 1 (fail-soft) + full
  e2e flow (no defects). **P0 build at finish line.** Render increment 2 awaits a real artifact.

## Post-P0 feature waves (collaboration loop)

- **Console redesign** (trusted-infra-console): sign-in, dashboard, viewer + quick wins
  (preview cards, profile menu + `/logout`, icon copy). `artifacta-design` skill added
  (skills/) for authoring artifacts.
- **Versioning** (ADR-0013) — MERGED: PR #17 (backend+schema: immutable versions, blob keyed
  (slug,n), `POST /artifacts/{slug}/versions`, `/a/{slug}/v/{n}/raw`, `GET /artifacts/{slug}`
  metadata) + PR #18 (CLI `publish --update` + `versions`, viewer version switcher, dashboard
  vN badge). Proven live: publish v1 → `--update` v2 → switcher flips between immutable versions.
- **Comments** (ADR-0014) — MERGED: PR #19. Proto `Comment`, `comments.json` store,
  `POST/GET /artifacts/{slug}/comments` (CanView-gated, author from session, version defaults
  to latest) + owner-only `POST .../{id}/resolve`. Viewer comments drawer (version-pinned,
  owner Resolve, all bodies rendered via textContent/createElement — no innerHTML). Proven live:
  bob (invited recipient) commented on the cohort report v2 → alice replied → owner resolved
  bob's note (green `resolved` pill); anon list → 404 not-leak.
- **Anchored comments** (ADR-0015) — MERGED (PR #20). Comment on selected text, Notion-style. Schema: `TextAnchor` +
  `Comment.anchor` (optional → unanchored = page-level note; both allowed). No store change
  (protojson auto-persists). Server: anchor decoded/validated in `addComment` (quote cap 2000
  runes → over-cap degrades to page-level; context clamped 64), echoed via `commentView`.
  Viewer: a postMessage BRIDGE injected into the sandboxed frame (the only code that can read
  the null-origin selection) reports selections + highlight clicks and paints anchor highlights
  via the CSS Custom Highlight API (no DOM mutation); shell shows a floating ＋Comment over a
  selection, a quote strip in the composer + each anchored comment, and cross-links
  highlight⇄comment. Inbound frame messages are display-only (validated by source, never authz).
  Proven live: user selected text on the SRE-agent artifact and left an anchored comment with
  correct computed offsets; seeded quote "contains customer PII" highlights in the cohort report.
- **Comment threads / replies** (ADR-0016) — MERGED (PR #20). A reply = a comment with
  `parent_id` (reuses the create/list/store path; no new type/route). One level only
  (reply-to-reply → 400); replies inherit the root's version, are never anchored; resolve is
  thread-level (root only; reply/unknown id → 404); count in the bar = threads (roots). Viewer
  groups roots + replies into conversations with an inline Reply box. Tests: `TestReplyThread`.
  Proven live: posted a reply through the UI on the cohort report; thread refreshed in place.
- **CI pipeline repair** (PR #21 + #20) — the Go CI had gone red repo-wide (env drift; also red on
  main's head) and had in fact never run golangci successfully. Fixed: run go from
  `services/artifacta-api` (the workspace root isn't a module); adopt **golangci-lint v2** (v1's
  type-checker can't read go 1.27 export data) installed from source; add `.golangci.yml`
  (std-error-handling preset) + clear the 2 real findings it surfaced; replace the unresolvable
  commitlint action with a direct `npx commitlint` run. main CI now green (lint-go + test-go).
- **Next:** share/invite (invite-by-email, pending grant resolved at first SSO login).

## Findings

- **F-3 (open) — legacy artifacts predate versioning.** Artifacts published before ADR-0013
  have `latest_version=0` and their blob is at the old `<slug>.bundle` key, so post-upgrade
  they 404 on view and `--update` starts at v1. Needs a one-time migration: backfill a v1
  version record + rename `<slug>.bundle` → `<slug>.v1.bundle` + set latest_version=1. Non-issue
  for greenfield deploys (no pre-versioning data); relevant only when upgrading an instance
  that already has artifacts.
