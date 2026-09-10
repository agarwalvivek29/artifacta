# 0009 — Postgres store adapter for grants & "shared with me"

**Date**: 2026-08-12 (implemented 2026-09-10)
**Status**: Accepted (implemented)
**Deciders**: Vivek Agarwal
**Issue**: N/A
**Relates to**: [0017](./0017-subdomain-artifact-addressing.md) (label uniqueness), [0006](./0006-s3-blob-adapter-backend-only.md) (blobs, the remaining stateless piece)

---

## Context

The v0 metadata store is an in-memory + whole-file-rewrite `FileStore` (single process). The
dashboard's **"Shared with me"** tab (FR18) requires a reverse grant lookup
(`grantee_sub → artifacts`) — a many-to-many join. At team scale this is an indexed query,
not an in-memory scan, and concurrent multi-process access needs real transactions.

---

## Decision

Add a **PostgreSQL store adapter** behind the existing `Store` interface (artifacts, grants,
audit), selectable by config. Fast-follow (not P0).

- Tables: `artifacts`, `grants` (indexed on `grantee_sub` and `slug`), `audit_events`
  (append-only, indexed on `seq`).
- The append-only, hash-chained audit contract (ADR baseline) is preserved: rows are
  insert-only; the chain is computed as today.
- The `FileStore` remains the zero-dependency default for local/self-host-small.

---

## Consequences

### Positive

- Indexed "shared with me" / "org" queries; safe concurrent multi-process access.
- Enables horizontal scaling of the API tier (shared DB state).

### Negative

- A dependency to run and back up. Kept optional — `FileStore` stays the small-deploy default.

### Neutral

- Requires the `Store` interface to stay adapter-agnostic (no file-specific assumptions leak
  into domain/API).

---

## Alternatives Considered

### SQLite

Rejected for multi-process/HA: single-writer; fine for single-node but doesn't unlock the
horizontal scaling Postgres does.

### Keep `FileStore` only

Rejected at scale: whole-file rewrites and in-memory scans don't hold up for large grant sets
or concurrent writers.

---

## Implementation (2026-09-10)

- **Backend selector** — `ARTIFACTA_STORE=file|postgres` (default `file`) + `ARTIFACTA_DATABASE_URL`.
  Both backends satisfy the same `Store` interface; a shared **conformance test suite** runs
  against `FileStore` and real Postgres so behaviour is identical, not just assumed.
- **ORM + auto-migration** — GORM. `NewSQLStore` runs `AutoMigrate` on startup, so a deployment
  that **bumps the binary self-applies additive schema changes** (new columns/indexes) with no
  manual migration step.
- **Schema-first-safe (Rule 12)** — the GORM models are thin **storage envelopes**: a primary
  key, the columns we filter on, and a `jsonb data` column holding the **protojson of the domain
  message**. The domain types remain the generated proto messages; they are never redefined.
  Tables: `artifacts`, `grants`, `artifact_versions`, `comments`, `audit_events`.
- **Uniqueness is the DB's job** — `artifacts.label` carries a **UNIQUE index** (nullable, so
  unlabelled artifacts don't collide). This is the multi-replica-safe replacement for the file
  store's in-process mutex (ADR-0017): a racing double-claim is rejected as a `23505` violation
  and surfaces as HTTP 409.
- **Audit chain preserved** — `Append` runs in a transaction that reads the current chain head
  and writes the next `seq`/`prev_hash`/`hash` (same `hashEvent` as the file store).

### Deployment topology (the StatefulSet vs Deployment question)

- **`file`** — single-instance stateful. Run as a **Deployment `replicas: 1` + RWO PVC +
  `strategy: Recreate`** (or a StatefulSet `replicas: 1`). Never multi-replica: state is in
  local memory/disk behind a process mutex.
- **`postgres`** — the API tier becomes **stateless** → a plain **Deployment with N replicas**;
  uniqueness/consistency come from Postgres, not a mutex. The one remaining piece for fully
  stateless multi-replica is shared blob storage (**S3**, [ADR-0006](./0006-s3-blob-adapter-backend-only.md));
  until then blobs are on local disk and multi-replica needs `ReadWriteMany` or the S3 adapter.
