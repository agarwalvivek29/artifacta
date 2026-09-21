# Spec: `artifacta migrate` — offline file-store → Postgres/S3 data migration

**Issue**: #10 (TBD — GitHub issue not yet created)
**Status**: Review
**Author**: Vivek Agarwal
**Date**: 2026-09-22
**Services Affected**: artifacta-api

---

## Summary

A new `artifacta migrate` subcommand that copies a live `FileStore` + `BlobFS` deployment's
data — artifacts, versions, grants, comments, the hash-chained audit trail, and blob bundles —
into the Postgres store (ADR-0009) and S3 blob backend (ADR-0006), preserving the audit chain
verbatim, so operators can adopt the scale backends **without data loss**.

---

## Background and Motivation

ArtifactA ships file-store/fs-blob (the zero-dependency default) _and_ Postgres/S3 (the
horizontal-scale topology), but nothing to move data between them. GORM `AutoMigrate` creates
the destination schema only. Without a migrator, the scale backends are unreachable for any
existing deployment: switching means starting fresh and abandoning history — including the
audit trail that exists for compliance. This closes that gap.

---

## Scope

### In Scope

- A new `artifacta migrate` subcommand: **offline, operator-run, one-shot**.
- Source = file store (`meta/`) + fs blobs; destination = SQL store (DSN) + S3, addressed via
  **explicit source/dest flags** (config is a single flat struct and cannot express both ends
  via env at once).
- Copy order: **artifacts → versions → grants → comments → audit (verbatim) → blobs**. Each
  entity is **existence-checked before write** (only `PutArtifact` upserts natively; versions,
  comments, grants are checked by the command), so the copy is idempotent.
- Two new `Store` methods per ADR-0026: `AllArtifacts()` and `AppendRaw()`, implemented on
  `FileStore` + `SQLStore` and covered by the conformance suite.
- **Resumable, not empty-only:** a re-run after a partial/failed migration continues in place
  without duplicating or erroring; the destination may be empty or a prior partial of the same
  source.
- **Source audit chain verified up front** (fail fast) before any copy begins.
- **Completeness gate:** post-migration `VerifyChain` on the destination audit table **plus**
  source↔dest event-count + first-seq/last-seq/last-hash equality (`VerifyChain` alone does not
  pin the first seq, so it passes on a truncated chain). Blobs are **checksum-verified**
  (sha256 read-back). Per-entity count summary; non-zero exit on any failure.

### Out of Scope

- Online / live / dual-write migration.
- Reverse migration (Postgres→file): enabled by the same primitives, but not built or tested
  here.
- Schema-migration tooling (GORM `AutoMigrate` already self-creates the destination schema).
- Kubernetes Job manifests — infra is docker-compose; the migration runs as a one-shot
  `docker compose -f infra/docker-compose.prod.yml run --rm artifacta-api artifacta migrate …`.
- Any change to the runtime `Append` / publish paths.
- Proto / domain-type changes (Rule 12: reuse existing generated messages).

---

## Acceptance Criteria

- [ ] Given a populated file store + fs blobs, when `artifacta migrate` runs against an empty
      Postgres + S3 destination, then every artifact, version, grant, and comment is present in
      the destination and reads identically through the `Store` API. Grants are deduped by
      `grantee_sub` when non-empty, else `grantee_email` (email-only invites have an empty
      `grantee_sub`).
- [ ] Given the source audit log, when migration completes, then the destination audit table
      has rows with identical `seq` / `prev_hash` / `hash`, `VerifyChain(dest)` passes, **and**
      the destination event count + first-seq + last-seq + last-hash equal the source.
- [ ] Given a source whose own audit chain does not verify, when `migrate` runs, then it aborts
      up front (before copying anything) and exits non-zero.
- [ ] Given each source blob `<slug>.v<n>.bundle`, when migration completes, then the same key
      exists in the S3 bucket with byte-identical contents, verified by sha256 read-back.
- [ ] Given a completed migration, when `migrate` is re-run with the same arguments, then it
      copies nothing, skips everything (including audit and blobs), verifies the chain, and exits
      zero; a run resuming after an interruption copies only what is missing.
- [ ] Given a destination whose audit trail is not a prefix of the source chain (a foreign or
      divergent destination), when `migrate` runs, then it refuses **before writing any artifact**
      (up-front prefix check) — the destination is not polluted with the source's data. A
      same-`seq`/different-`hash` row is one such case; so is a dest with more events than the source.
- [ ] Given `AppendRaw`, then it is reachable only from the migrate path — no API or CLI
      _publish_ surface can call it.
- [ ] Given any per-entity failure, then `migrate` exits non-zero and reports which entity/slug
      failed. The source data directory is never modified by any run.

---

## Technical Design

### Architecture Overview

```
artifacta migrate  (offline, one-shot, resumable)
  ├─ open(SRC) → srcStore (FileStore), srcBlob (BlobFS)      # parameterized open()
  ├─ open(DST) → dstStore (SQLStore),  dstBlob (BlobS3)      # dest schema self-creates (AutoMigrate)
  ├─ VerifyChain(srcStore.AuditEvents())  → fail fast if source chain is broken
  ├─ for a := range srcStore.AllArtifacts():                 # NEW enumeration (ADR-0026)
  │     dstStore.PutArtifact(a)                              # upsert (idempotent)
  │     for v := range srcStore.Versions(a.Slug):  if !dst.GetVersion(slug,v.N): AddVersion(v)
  │     for g := range srcStore.Grants(a.Slug):    if !dstHasGrant(slug, key(g)): AddGrant(g)
  │     for c := range srcStore.Comments(a.Slug):  if !dstHasComment(slug, c.Id): AddComment(c)
  │        # key(g) = grantee_sub if non-empty else grantee_email  (email invites: empty sub)
  ├─ for e := range srcStore.AuditEvents():  dstStore.AppendRaw(e)   # verbatim; skip-if-identical-seq, error-on-divergent-hash
  ├─ for a, v: copyBlob(src, dst, slug, v.N)                 # sha256 read-back; skip if dst matches
  └─ gate: VerifyChain(dst) AND count/first-seq/last-seq/last-hash == source; print per-entity counts
```

Blobs are driven off `Versions(slug)` through the `Blob` interface — **not** by globbing
`/data/blobs/*.bundle`. Keys are identical (`<slug>.v<n>.bundle`, no path prefix) on both fs
and S3, so no key translation is needed.

### API Changes

None (no HTTP endpoints). New CLI subcommand only.

**CLI shape (draft — finalized in plan):**

```
artifacta migrate \
  --from-data-dir <path>        # source file store + fs blobs
  --to-store postgres --to-database-url <dsn> \
  --to-blob s3   --to-s3-endpoint … --to-s3-bucket … (etc.)
# No empty-dest guard flag: the command is resumable and idempotent by design, so re-running
# against a partially-migrated destination (of the same source) is the normal recovery path.
```

### Store interface changes (ADR-0026)

```go
// added to api.Store, implemented by FileStore and SQLStore, conformance-tested
AllArtifacts() ([]*Artifact, error)       // full backend-agnostic enumeration
AppendRaw(ev *AuditEvent) error           // verbatim insert; preserves seq/prev_hash/hash; no re-chain.
                                          // Resumable: existing seq + identical hash → skip; existing seq + different hash → error.
```

### Data Model Changes

None. No new tables/columns; the destination Postgres schema self-creates via `AutoMigrate`.
No proto changes.

### Dependencies

None new — reuses the existing SQL store, S3 adapter, and audit-verify helpers.

---

## Security Considerations

- **`AppendRaw` is the sensitive surface** (ADR-0026): insert-only, migration-only; a same-`seq`
  row with a **different** `hash` is refused (never overwritten), so a foreign/divergent trail
  errors instead of corrupting; and the mandatory post-migration `VerifyChain` + completeness
  gate must pass. It must not be reachable from any API/CLI publish path.
- Migration reads **all** data and requires both source filesystem access and destination
  DSN/S3 credentials — an operator/admin action, never user-facing. No new authz surface.

---

## Observability

- **Logs**: per-entity progress and final counts (artifacts / versions / grants / comments /
  audit events / blobs); the `VerifyChain` result; the slug of any failure.
- **Metrics**: none (one-shot CLI, not a long-running server path).
- **Alerts**: none; success/failure is the process exit code.

---

## Testing Plan

> Run `/plan-eng-review` before implementation begins.

### Affected Pages / Routes

- None (CLI subcommand; no viewer routes).

### Unit Tests

- `AllArtifacts()` and `AppendRaw()` on both `FileStore` and `SQLStore` via the shared
  conformance suite: `AppendRaw` preserves seq/prev_hash/hash verbatim; a re-insert of the same
  seq+hash is skipped; a same-seq/different-hash insert errors. (Verbatim-seq cases run on a
  fresh/isolated store, since the shared re-runnable conformance DB is populated.)
- Grant dedupe key: `grantee_sub` when present, else `grantee_email` (email-only invite case).
- Completeness gate: count + first/last seq/hash equality; a truncated dest chain that still
  `VerifyChain`-passes is caught by the count/endpoint check.
- Copy-loop ordering and per-entity failure surfacing.

### Integration / E2E Tests

- Round-trip: populate a file store + fs blobs (artifacts with versions, grants incl. an
  email-only invite, comments, audit events) → `migrate` into Postgres (test container /
  sqlite-in-CI as available) + a fake/MinIO S3 → assert every entity matches through the
  `Store` API, blob bytes match by checksum, and the audit completeness gate passes.
- Resumability: re-run the same migration → zero duplicates, everything skipped, exit zero; and
  a resume after a simulated mid-run interruption copies only what was missing.
- Source pre-verify: a deliberately broken source chain aborts before any copy.

---

## Migration / Rollout Plan

- **Database migrations**: none authored — destination schema self-creates via `AutoMigrate`.
- **Breaking changes**: none (purely additive; runtime paths untouched).
- **Feature flag**: n/a (opt-in subcommand).
- **Rollout**: ships in the next released image (semver tag → `release.yml` → GHCR); operators
  run it once as a one-shot container against a quiesced source.
- **Rollback**: the destination is additive and separate; on failure, drop the destination DB /
  empty the bucket and re-run. The source is read-only throughout.

---

## Open Questions

| Question                                                                                                             | Owner         | Status                                                   |
| -------------------------------------------------------------------------------------------------------------------- | ------------- | -------------------------------------------------------- |
| Idempotency: refuse-on-nonempty vs resumable/upsert re-run?                                                          | Vivek Agarwal | **Resolved → resumable**                                 |
| Support arbitrary source/dest combos, or only file → Postgres+S3 for v1?                                             | Vivek Agarwal | **Resolved → file→PG+S3 only** (primitives stay generic) |
| Verify blob byte-equality (checksum) inline, or trust `Put`?                                                         | Vivek Agarwal | **Resolved → checksum each blob**                        |
| Build reverse (Postgres→file) now or defer?                                                                          | Vivek Agarwal | **Resolved → defer**                                     |
| Write a provenance marker so resume refuses a foreign destination, or rely on operator + the audit divergence abort? | Vivek Agarwal | Open (lean: rely on divergence abort for v1)             |

---

## References

- Related ADR: [0026](../adr/0026-store-blob-migration.md) (this feature's decision),
  [0009](../adr/0009-postgres-store-adapter.md), [0006](../adr/0006-s3-blob-adapter-backend-only.md),
  [0013](../adr/0013-artifact-versioning.md), [0014](../adr/0014-artifact-comments.md)
- Related issues: #10 (TBD)
