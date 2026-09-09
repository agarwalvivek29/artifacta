# 0013 — Artifact versioning: immutable versions, explicit update

**Date**: 2026-08-25
**Status**: Accepted
**Deciders**: Vivek Agarwal
**Issue**: N/A
**Relates to**: [0004](./0004-artifact-bundle-format.md) (bundle is now per-version)

---

## Context

Artifacts were single-shot: one slug, one blob, no history. But the artifacts ArtifactA
hosts are living documents — a report or dashboard gets revised. Without versions there is
no evolution: re-publishing would overwrite, losing the prior state, and there is nothing for
review comments to pin to. Versioning was deferred from P0 (the "collaboration loop"); it is
now pulled in as foundational, ahead of comments (which pin to a version).

---

## Decision

Model an artifact as a **container with an ordered list of immutable versions**.

- **Types** (schema-first, `packages/schema/proto/artifacta/v1`):
  - `Artifact` gains `int32 latest_version`. It stays the container: slug, owner, title,
    visibility, latest_version.
  - New `ArtifactVersion { slug, n (1-based), content_type, created_at, created_by, note }`.
    Versions are **immutable** — never mutated or deleted in normal operation.
- **Blob keying**: one bundle per version, keyed by `(slug, n)` → `<slug>.v<n>.bundle`. The
  blob store interface takes the version number.
- **Publish semantics** (explicit update, chosen over auto/overwrite):
  - `POST /artifacts` (new artifact) creates version **1**.
  - `POST /artifacts/{slug}/versions` (owner-only) appends version **n+1**; CLI surface is
    `artifacta publish --update <slug> <file>`. Nothing is overwritten.
  - Each version append writes a `PUBLISH` audit event.
- **Serving**: `/a/{slug}/raw` serves the **latest** version; `/a/{slug}/v/{n}/raw` serves a
  specific version. Both pass the identical `CanView` gate — versions inherit the artifact's
  visibility/grants (access is per-artifact, not per-version).
- **Metadata**: `GET /artifacts/{slug}` (authorized) returns title, visibility,
  latest_version, the version list, and `isOwner`. It powers the viewer's version switcher
  now and the owner Share panel later.
- **Comments** (later) store the version they were made on.

---

## Consequences

### Positive

- Documents evolve; prior versions stay viewable and are a stable target for comments.
- Immutability keeps the audit + comment story coherent (a version never changes underfoot).

### Negative

- Blob storage grows with every version — bounded later by the TTL/retention reaper (v2).
- The blob interface and every call site take a version number (one-time ripple).

### Neutral

- Access control stays per-artifact; versions do not get independent visibility.
- The single-file bundle (ADR-0004) is now the shape of each _version_.

---

## Alternatives Considered

### Auto-version by title + owner

Re-publishing a same-titled file auto-appends a version. Rejected: implicit, and risks
accidentally merging unrelated artifacts or splitting one across renames.

### Overwrite in place with history snapshots

Publish overwrites "current", server keeps snapshots. Rejected: weakens the immutable-version
guarantee that comments and audit rely on; "current" mutating underfoot is the problem we're
removing.
