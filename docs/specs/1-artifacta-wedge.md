# Spec 1 — here.now wedge: publish + private-by-default viewer

Issue: #1
Status: implemented (v0)
Service: `services/artifacta-api`
ADRs: [0001](../adr/0001-monorepo-structure.md), [0002](../adr/0002-auth-model.md)

## Summary

The smallest thing that replaces a third-party artifact share link with an
access-controlled link on infra the operator owns: a single Go binary that publishes an
artifact and serves it through an authorization-gated, sandboxed viewer.

## Acceptance Criteria

- [x] `artifacta login` establishes a local identity + session token.
- [x] `artifacta publish <file>` stores the bundle + metadata and prints `<base>/a/<slug>`.
- [x] Artifacts are **private by default** (owner-only).
- [x] `artifacta ls` lists the owner's artifacts.
- [x] `artifacta serve` serves `/a/{slug}` (viewer shell) and `/a/{slug}/raw` (bytes).
- [x] `/a/{slug}/raw` returns bytes ONLY after `domain.CanView` allows; anonymous → 404.
- [x] Existence is not leaked (404, not 403); store errors fail closed (deny).
- [x] Every publish/view/deny is written to the inbuilt append-only, hash-chained audit trail.
- [x] `GET /health` and `GET /metrics` are exempt and explicitly allowlisted.
- [x] Domain types (Artifact, Grant, Visibility, AuditEvent) are defined in
      `packages/schema/proto/artifacta/v1` and imported (schema-first, Rule 12).

## Design

See [ARCHITECTURE.md](../../ARCHITECTURE.md). Domain logic in `internal/domain`
(framework-free); persistence in `internal/infra` (file store + fs blob, protojson +
hash-chain); HTTP in `internal/api`; CLI in `internal/cli`; single binary at
`cmd/artifacta`.

## Out of Scope (follow-up issues)

- `artifacta share` + owner Share UI (invited/org visibility end to end).
- OIDC + forward-auth adapters.
- Rendering-parity viewer fork (`apps/`) + golden-artifact CI.
- Postgres store + S3 blob adapters, Helm chart.

## Rollback

v0 is additive (new service). Rollback = remove `services/artifacta-api` + its proto; no
migrations, no shared-state changes.
