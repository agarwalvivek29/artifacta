# Product — ArtifactA

> The product source of truth. Agents read this to understand _what_ is being built
> and _why_, before deciding _how_.

## Vision

We are building ArtifactA so that **teams can share AI-generated artifacts that contain
sensitive data** without handing storage, access control, or audit of that data to a
third-party vendor.

## Problem Statement

AI assistants (Claude and others) generate artifacts — HTML pages, reports, dashboards —
that routinely contain sensitive data: customer PII, internal analytics, credentials in
dashboards. Today those artifacts are persisted on and served from a third-party vendor's
infrastructure (share links, hosted CDNs). For any team with a compliance posture, that
is an ungoverned dependency: the data lives, is served, and is (not) audited on infra they
do not control. ArtifactA removes the **persistence + sharing** dependency — the artifact
lives, is access-controlled, audited, and expired entirely on infra the operator controls.

**Key insight (do not overstate):** generation-time content still passes through whatever
AI vendor produced it — this does not solve that. It solves storage + serving + access
control + audit. The defensible claim is "never stored on, nor retrievable from,
third-party infra," not "PII never touches the vendor."

## Target Users

| User type                    | Description                                                     | Primary needs                                                                     | Frequency        |
| ---------------------------- | --------------------------------------------------------------- | --------------------------------------------------------------------------------- | ---------------- |
| Engineer / analyst (primary) | Generates artifacts via an AI assistant, shares with colleagues | One-command publish; a link that just works; render quality equal to the vendor's | Weekly           |
| Platform / security owner    | Runs the self-hosted instance                                   | Auth integration, RBAC, an inbuilt audit trail, self-host simplicity              | Occasional (ops) |
| Recipient                    | Opens a shared artifact                                         | Fast, clean render; only sees what they're allowed to                             | Weekly           |

**Primary user**: the engineer/analyst — optimize for their time-to-first-shared-link.

## Core Features (v1 Must Have)

- **Publish → access-controlled link** (effort: M): `artifacta publish <file>` → private-by-
  default link on infra you own. Trigger: user/agent runs it. Success: a `/a/{slug}` link
  that only authorized viewers can open.
- **Private-by-default RBAC** (effort: M): private / invited / org visibility; the
  per-artifact decision runs in the app; every allow and deny is audited.
- **Sandboxed viewer with render quality ≈ vendor's** (effort: L): artifacts render cleanly
  and fast in a sandboxed iframe. The adoption make-or-break.
- **Inbuilt hash-chained audit** (effort: S): who-viewed-what, tamper-evident, in the app's
  own store — never routed to an external system.

## v1 Complete (Should Have)

- `artifacta share` + owner Share UI (invite by email/subject, org visibility).
- Pluggable auth adapters: generic OIDC (any issuer) + trusted forward-auth header.

## v2 and Beyond (Could Have)

- Remote MCP connector (one Streamable-HTTP + OAuth connector serving web + desktop assistants).
- S3-compatible blob + Postgres store adapters; Helm chart; TTL/expiry reaper.
- Rendering-parity fork of an artifact-runtime with a golden-artifact CI check.

## Non-Goals

- Not a generic file host or CDN — it hosts AI-generated artifacts with access control.
- Not an AI-generation product — it hosts what other tools generate.
- v1 does not implement external (non-account) public sharing.

## Success Metrics

- Time-to-first-shared-link < ~2 minutes (install → login → publish → link).
- Render quality: common artifacts (reports, dashboards) render indistinguishably from the vendor viewer.
- At least one team replaces vendor share links with ArtifactA links for sensitive content.

## Roadmap

### Now (v1)

- [ ] Publish + private-by-default viewer (the wedge) — dogfood with real users.

### Next (v2)

- [ ] Share/RBAC UI + OIDC/forward-auth adapters.

### Later

- [ ] Remote MCP connector, scale adapters (S3/Postgres/Helm), rendering-parity fork.
