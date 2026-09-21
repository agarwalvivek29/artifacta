# 0026 — Offline store+blob migration: verbatim audit insert & full-store enumeration

**Date**: 2026-09-22
**Status**: Proposed
**Deciders**: Vivek Agarwal
**Issue**: TBD (needs GitHub issue)
**Relates to**: [0009](./0009-postgres-store-adapter.md) (Postgres store), [0006](./0006-s3-blob-adapter-backend-only.md) (S3 blob), [0013](./0013-artifact-versioning.md) (immutable versions), [0014](./0014-artifact-comments.md) (comments)

---

## Context

We ship a Postgres store adapter (ADR-0009) and an S3-compatible blob adapter (ADR-0006),
both selectable by config. We do **not** ship anything that moves an existing
`FileStore` + `BlobFS` deployment's data into them. GORM `AutoMigrate` (`sqlstore.go`)
creates the destination **schema** on startup — it moves **zero data**. So the scale
backends are, today, unreachable for any operator who started on the file-store default:
adopting them means abandoning all existing artifacts, versions, grants, comments, and the
hash-chained audit trail.

Building a faithful migrator runs into two invariants the current `Store` interface
(`internal/api/server.go`) does not accommodate:

1. **No full enumeration.** `Store` exposes only `ListByOwner` / `ListByGrantee` /
   `ListByVisibility` / `SearchArtifacts` — all scoped. Grants, versions, and comments are
   all read per-slug. A migration must visit **every** artifact, and there is no method that
   yields them.
2. **The audit chain is write-derived.** Both `Append` implementations (`store.go`,
   `sqlstore.go`) unconditionally overwrite `seq` / `prev_hash` / `hash` on every call — they
   re-chain. Copying audit events through `Append` re-derives the chain from the destination's
   state; it cannot preserve the source's exact `seq` + `hash`, which is precisely what audit
   verification and compliance depend on (ADR-0009: "rows are insert-only; the chain is
   computed as today").

---

## Decision

We will add two backend-agnostic primitives to the `Store` interface and build migration on
top of them as an **offline, operator-run, one-shot** command (`artifacta migrate`, see
spec 10).

1. **`AllArtifacts() ([]*Artifact, error)`** on the `Store` interface, implemented by both
   `FileStore` and `SQLStore` and covered by the shared conformance suite. This is the
   backend-agnostic full enumeration that migration drives grants/versions/comments/blobs from
   (each of which is slug-scoped).

2. **`AppendRaw(*AuditEvent) error`** — a verbatim audit-insert path that inserts an event
   **preserving its `seq` / `prev_hash` / `hash` exactly, without re-chaining**. This is a
   deliberate, narrow carve-out of the append-only `Append` contract, and is used **only** by
   migration:
   - **Insert-only** — never update or delete.
   - **Resumable, not empty-only** — on a pre-existing `seq`, `AppendRaw` compares the stored
     `hash`: **identical** ⇒ skip (idempotent resume of a partial migration); **different** ⇒
     error (a foreign/divergent trail is refused, never overwritten). So migration does **not**
     require an empty destination audit table; it may resume into a partial of the same source.
   - **Not exposed** on any API or CLI _publish_ surface — the runtime audit path stays
     `Append`-only and unchanged.
   - **Verified for completeness, not just consistency** — after migration, `VerifyChain`
     (`audit_verify.go`) runs against the destination audit table, **and** the migration asserts
     source↔destination event **count** equality plus **first-seq / last-seq / last-hash**
     equality. `VerifyChain` alone does not pin the first seq to 1, so it passes on a truncated
     chain; the count + endpoint checks are what prove the whole trail arrived. Any failure fails
     the migration.

3. Migration is **offline, one-shot, and resumable** — a new `artifacta migrate` subcommand run
   by an operator against a quiesced source. It is **not** an online/dual-write/runtime path.
   Because it is resumable (writes into a possibly-non-empty destination), the migration owns
   idempotency itself: the source audit chain is **verified up front** (fail fast); metadata is
   copied read-before-write (only `PutArtifact` upserts natively — versions, comments, and grants
   are existence-checked by the command, since `AddVersion`/`AddComment` collide on their PKs and
   `AddGrant` has no unique key); and grant identity for that check is `grantee_sub` when
   non-empty, **else** `grantee_email` — email-only invites (ADR-0019) carry an empty
   `grantee_sub`, so a `grantee_sub`-only key would collapse distinct invites. Before any copy,
   the migration also refuses a **foreign/divergent destination up front**: the destination audit
   trail must be a prefix of the source chain (empty, or an equal prefix = a partial of the same
   source), so a destination holding another source's data is rejected before a single artifact
   is written.

No new domain types are introduced; migration reuses the generated proto messages
end-to-end (Rule 12 preserved). No new tables or columns — the destination schema
self-creates via `AutoMigrate`.

---

## Consequences

### Positive

- Operators can adopt Postgres + S3 (the horizontal-scale topology of ADR-0009) **without data
  loss**, including a verifiable, byte-identical audit trail.
- `AllArtifacts()` is a reusable enumeration primitive (future export/backup/reverse-migration
  build on it).
- The audit-preservation guarantee is **explicit and testable** (`VerifyChain` on the
  destination), not an emergent property of replay determinism.

### Negative

- `AppendRaw` is a second write path into the audit table that **skips chaining** — a footgun
  if ever wired to untrusted input. Mitigations: migration-only (no API/CLI publish surface),
  insert-only, same-`seq`/different-`hash` refused (divergence errors, never overwrites), and the
  mandatory post-migration `VerifyChain` + count/endpoint completeness gate.
- Resumability means writing into a possibly-non-empty destination. Guard: before any write the
  migration requires the destination audit trail to be a **prefix** of the source chain (empty,
  or an equal prefix = a partial of the same source); a destination holding a different source's
  audit is refused **up front**, so its artifacts are never touched. Residual: a foreign
  destination that holds artifacts but **no** audit rows can't be distinguished (an empty prefix
  matches anything), so a colliding artifact slug would be upserted. v1 accepts this narrow
  residual (the operator points at an empty dest or a same-source partial); a stored provenance
  marker would close it. Verified live: a foreign source is refused before any artifact write.

### Neutral

- `AllArtifacts()` on `FileStore` is an in-memory slice copy (fine — the file store is by
  definition small / single-node). On `SQLStore` it is a full-table scan, acceptable for a
  one-shot command off the hot path.

---

## Alternatives Considered

### Copy audit through `Append` and rely on determinism

`hashEvent` is deterministic and SQL `seq` restarts at 1 on an empty table, so an **in-order**
replay into an **empty** destination reproduces an identical chain. Rejected as the contract:
it is silently fragile — any reordering, retry, or pre-existing row diverges the chain with
**no error** — and it couples migration correctness to an implementation coincidence.
`AppendRaw` makes the guarantee explicit and verifiable.

### Enumerate via `ListByVisibility` over every `Visibility` value + dedupe

Rejected: relies on the visibility enum being exhaustive and stable, does redundant work, and
only avoids missing artifacts by luck. An explicit, conformance-tested `AllArtifacts()` is
clearer and safer.

### Reach into the concrete `*FileStore` from the `migrate` command

Rejected: leaks a file-specific assumption into a command that should be backend-agnostic
(ADR-0009 neutral clause), and would not support a future Postgres→file or Postgres→Postgres
move.

### `pg_dump` / raw filesystem copy

Rejected: does not translate the file store's JSON layout into the SQL envelope schema, does
not move blobs into S3, and bypasses the store APIs entirely — coupling the migration to
storage internals that the envelope schema is explicitly designed to hide.
