# 0004 — Artifact bundle format: single-file now, zip+manifest later

**Date**: 2026-08-09
**Status**: Accepted
**Deciders**: Vivek Agarwal
**Issue**: N/A

---

## Context

An artifact's bytes are stored as one opaque blob (`blobs/{slug}.bundle`) and served at
`/a/{slug}/raw`. The blob store and `ARCHITECTURE.md` already call this a "bundle",
anticipating multiple files, but v0 stores exactly one file (`artifacta publish <file>`).

Real artifacts can be multi-file (an `index.html` plus `assets/*.js`, `*.css`, images).
The question was whether to build multi-file support now, and if so, how — without
breaking the security invariants (one `CanView` decision per view, one integrity hash,
atomic publish, a trivial future S3 adapter).

---

## Decision

**Keep artifacts single-file for the v2 wedge.** Do not build multi-file support yet.

When a real multi-file artifact forces the issue, implement it as a **single opaque
`zip` bundle + a manifest** (entry list + entry-point path) stored in `Artifact`
metadata — **not** as a multi-object store:

- One blob per artifact stays true → `CanView`, audit, integrity hash, and the future
  S3 adapter (one object per slug) are all unchanged.
- `zip` (not `tar`) is the container: its central directory gives random access to a
  single entry without scanning the whole archive.
- Serving becomes a sub-path resolver (`/a/{slug}/raw/<path>`): one `CanView` per
  request (authz is per-slug), per-entry Content-Type, path-traversal-safe lookup.
- The single-file path is preserved (archive of one entry, or a fast path).

This keeps multi-file a **serve-handler change**, never a data-model migration.

---

## Consequences

### Positive

- Ships the wedge faster; defers real complexity until a concrete need appears.
- The eventual upgrade path touches only publish (archive) + serve (sub-path), leaving
  the store/authz/audit/S3 contracts intact.

### Negative

- Until then, artifacts that reference sibling files (external JS/CSS/images) are not
  supported — only self-contained documents.

### Neutral

- The blob contract stays "one opaque bundle per slug" for both formats, so adapters
  written now (streaming, S3) remain valid after the zip upgrade.

---

## Alternatives Considered

### Multi-object store (`blobs/{slug}/{path}`)

One stored object per file. Rejected: breaks single-hash integrity and atomic publish
(partial upload = broken artifact), and complicates the S3 adapter (list/multi-get).

### Build zip bundles now

Rejected as premature: the wedge's target artifacts (reports, dashboards) are commonly
self-contained HTML; single-file proves the wedge with less surface area.
