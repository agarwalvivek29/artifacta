# Architecture — ArtifactA

> The technical source of truth. Update when architecture changes; reference in every ADR.

## System Overview

ArtifactA is a self-hostable host for AI-generated artifacts. A single Go binary
(`artifacta`) provides both the CLI (`publish`/`login`/`ls`) and the viewer server
(`serve`). Artifacts are stored as opaque bundles in a blob store the operator controls;
metadata, grants, and an inbuilt hash-chained audit trail live in the operator's own
store. Every view passes the app's own authorization decision before any bytes are served.
Defining constraint (ArtifactA, see [ADR-0011](docs/adr/0011-vpn-fronted-threat-model.md)):
the instance is deployed **behind the corporate VPN** (the network perimeter); the app's own
auth + RBAC + audit are retained as **defense-in-depth and for compliance**, never the sole
boundary.

## Service Map

| Service         | Language | Type       | Responsibility                                                   | Primary DB                 | Queue |
| --------------- | -------- | ---------- | ---------------------------------------------------------------- | -------------------------- | ----- |
| `artifacta-api` | Go       | REST + CLI | Publish, authorization-gated viewer serving, RBAC, inbuilt audit | File store (v0) → Postgres | —     |

Frontend: v0 embeds a minimal sandboxed-iframe viewer in the binary; a forked
artifact-runtime lands in `apps/` for render parity (v2).

## Data Flow

```
[AI assistant / human] ── artifacta publish ──▶ artifacta-api (CLI path)
                                                 │ write bytes → BlobStore (fs)
                                                 │ write metadata → Store (file)  + audit
[Viewer / recipient] ── GET /a/{slug} ─────────▶ artifacta-api (HTTP)
                          GET /a/{slug}/raw ────▶ Auth.Identify → domain.CanView
                                                   allow → BlobStore.Get → stream + audit(view)
                                                   deny  → 404 + audit(deny)   (fails closed)
```

## Core Domain Model

| Entity     | Proto file                                          | Key fields                                           | Lifecycle                                                                              | Events                |
| ---------- | --------------------------------------------------- | ---------------------------------------------------- | -------------------------------------------------------------------------------------- | --------------------- |
| Artifact   | `packages/schema/proto/artifacta/v1/artifact.proto` | slug, owner_sub, visibility, content_type            | visibility: PRIVATE→INVITED→ORG→LINK (owner-set; LINK = no-login, VPN-gated, ADR-0018) | audit: PUBLISH / VIEW |
| Grant      | `packages/schema/proto/artifacta/v1/artifact.proto` | slug, grantee_sub (immutable), granted_by            | created / revoked                                                                      | audit: SHARE          |
| AuditEvent | `packages/schema/proto/artifacta/v1/audit.proto`    | seq, principal_sub, action, allowed, prev_hash, hash | append-only hash chain                                                                 | —                     |

All domain types are schema-first (Rule 12): defined in proto, generated to Go, imported
by the service — never redefined in service code.

## Technology Stack

| Layer   | Technology                                   | Reason                                                                               |
| ------- | -------------------------------------------- | ------------------------------------------------------------------------------------ |
| Backend | Go                                           | Single static binary, trivial self-host, low footprint; excellent for a CLI + server |
| Schema  | Protobuf + buf (Go codegen)                  | Single source of truth for domain types                                              |
| Store   | File (v0) → PostgreSQL                       | Relational many-to-many for grants + "shared with me"; file store for zero-dep local |
| Blob    | Filesystem (v0) → S3-compatible              | Operator-controlled bundle storage                                                   |
| Viewer  | Embedded HTML (v0) → forked artifact-runtime | Render parity with the vendor viewer                                                 |
| Infra   | Docker Compose (default) → Helm              | Self-host simplicity first                                                           |

## Auth Strategy

ArtifactA **diverges** from the template's JWT + shared-API-key default — see
[ADR 0002](docs/adr/0002-auth-model.md). It uses a pluggable identity `Provider`
(local now; OIDC + trusted-forward-auth later) and makes the **per-artifact** decision
(`domain.CanView`) in the app. Identity is never client-asserted; grants bind to the
immutable subject. Exempt paths: `GET /health`, `GET /metrics`.

## Architectural Constraints

- All domain types defined in `packages/schema/proto/` before service code (Rule 12).
- Bundle bytes served ONLY after an authorization allow; no client-reachable pre-signed URLs.
- Fail closed: missing artifact or store error → deny (404, don't leak existence).
- Private by default; identity from a verified token/session; grants bind to immutable subject.
- Audit is inbuilt (app's own store, hash-chained) — never routed to an external system.
- Deployed behind the VPN ([ADR-0011](docs/adr/0011-vpn-fronted-threat-model.md)); app auth +
  RBAC + audit are defense-in-depth, not the sole boundary. Per-artifact authz still fails
  closed and existence is not leaked (kept as cheap defense-in-depth).

## Key ADRs

| ADR                                                     | Decision                                                                                  | Status   |
| ------------------------------------------------------- | ----------------------------------------------------------------------------------------- | -------- |
| [0001](docs/adr/0001-monorepo-structure.md)             | Monorepo with per-service isolation                                                       | Accepted |
| [0002](docs/adr/0002-auth-model.md)                     | Pluggable OIDC/local/forward-auth + per-artifact RBAC (diverges from JWT+API-key default) | Accepted |
| [0017](docs/adr/0017-subdomain-artifact-addressing.md)  | Subdomain artifact addressing (`{slug\|label}.{root}`), host-router rewrite               | Accepted |
| [0018](docs/adr/0018-anonymous-vpn-gated-visibility.md) | Anonymous, VPN-gated `LINK` visibility (the one `CanView` carve-out)                      | Accepted |
