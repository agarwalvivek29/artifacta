# Architecture Decision Records (ADRs)

ADRs capture significant architectural decisions made in this project, along with the context and consequences of each decision.

---

## What is an ADR?

An ADR is a short document that records:

- **The context**: What problem were we solving? What constraints existed?
- **The decision**: What did we choose to do?
- **The consequences**: What are the trade-offs? What becomes easier or harder?

ADRs are written **before** implementing the decision, not after.

---

## When to Write an ADR

Write an ADR when making a decision that:

- Affects more than one service
- Changes or establishes a data layer (database choice, schema strategy)
- Changes infrastructure (new AWS service, changing queue broker)
- Changes API protocol or communication pattern between services
- Adds a new shared package or cross-service dependency
- Changes authentication/authorization strategy
- Is something a future contributor would ask "why did we do it this way?"

**Exempt**: Implementation details within a single service that don't affect others.

---

## ADR Lifecycle

```
Proposed → Accepted → Deprecated → Superseded
              ↓
           (in use)
```

- **Proposed**: Written, under review
- **Accepted**: Approved and in effect
- **Deprecated**: No longer recommended but still technically in use
- **Superseded**: Replaced by a newer ADR (link to the superseding ADR)

ADRs are **never deleted**. They are a historical record.

---

## How to Write an ADR

1. Find the next sequential number: look at the highest number in this directory
2. Copy the template format from `0001-monorepo-structure.md`
3. Name it: `[NNNN]-[short-kebab-case-title].md`
4. Fill in the sections
5. Set status to `Proposed`
6. Submit as part of the PR that implements the decision
7. Update status to `Accepted` when the PR is merged

---

## Index

| #                                                     | Title                                                                                     | Status   | Date       |
| ----------------------------------------------------- | ----------------------------------------------------------------------------------------- | -------- | ---------- |
| [0001](./0001-monorepo-structure.md)                  | Monorepo with per-service isolation                                                       | Accepted | 2026-03-06 |
| [0002](./0002-auth-model.md)                          | Pluggable OIDC/local/forward-auth + per-artifact RBAC (diverges from JWT+API-key default) | Accepted | 2026-07-07 |
| [0003](./0003-branch-strategy.md)                     | Build ArtifactA v2 on `main` (trunk-based; astra-v2 abandoned)                            | Accepted | 2026-08-12 |
| [0004](./0004-artifact-bundle-format.md)              | Artifact bundle format: single-file now, zip+manifest later                               | Accepted | 2026-08-09 |
| [0005](./0005-blob-streaming-interface.md)            | Blob store interface is streaming (`io.ReadCloser`)                                       | Proposed | 2026-08-09 |
| [0006](./0006-s3-blob-adapter-backend-only.md)        | S3-compatible blob adapter, backend-only (no presigned URLs)                              | Proposed | 2026-08-09 |
| [0007](./0007-identity-and-auth.md)                   | OIDC browser SSO + CLI loopback-PKCE + assistant-rides-session                            | Accepted | 2026-08-12 |
| [0008](./0008-separate-content-origin.md)             | Serve artifact bytes from a separate, cookieless content origin                           | Accepted | 2026-08-12 |
| [0009](./0009-postgres-store-adapter.md)              | Postgres store adapter for grants & "shared with me"                                      | Proposed | 2026-08-12 |
| [0010](./0010-render-parity-harness.md)               | Viewer render-parity harness (bundle-at-publish leading)                                  | Proposed | 2026-08-12 |
| [0011](./0011-vpn-fronted-threat-model.md)            | VPN-fronted deployment threat model (amends "no VPN assumed")                             | Accepted | 2026-08-12 |
| [0012](./0012-assistant-agnostic-publish-surfaces.md) | Assistant-agnostic publish surfaces (API + CLI + MCP + Skill)                             | Accepted | 2026-08-12 |
| [0013](./0013-artifact-versioning.md)                 | Artifact versioning: immutable versions, explicit `--update`                              | Accepted | 2026-08-25 |
| [0014](./0014-artifact-comments.md)                   | Artifact comments (version-pinned, view-gated)                                            | Accepted | 2026-08-25 |
| [0015](./0015-anchored-comments.md)                   | Anchored comments (comment on selected text, sandbox postMessage bridge)                  | Accepted | 2026-08-25 |
| [0016](./0016-comment-threads.md)                     | Comment threads (replies via parent_id, one level, thread-level resolve)                  | Accepted | 2026-08-25 |
| [0028](./0028-service-accounts-and-api-keys.md)       | Service accounts with admin-minted API keys (amends 0007)                                 | Proposed | 2026-11-02 |

---

## Template

```markdown
# [NNNN] [Title]

**Date**: YYYY-MM-DD
**Status**: Proposed | Accepted | Deprecated | Superseded by [NNNN]
**Deciders**: [names or roles]
**Issue**: [GitHub issue link]

## Context

[Describe the problem, constraints, and forces at play]

## Decision

[State the decision clearly. "We will..."]

## Consequences

### Positive

- [benefit 1]

### Negative

- [drawback 1]

### Neutral

- [trade-off 1]

## Alternatives Considered

### Option A: [Name]

[Brief description and why it was rejected]

### Option B: [Name]

[Brief description and why it was rejected]
```
