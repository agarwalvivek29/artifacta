# 0014 — Artifact comments (version-pinned, view-gated)

**Date**: 2026-08-25
**Status**: Accepted
**Deciders**: Vivek Agarwal
**Issue**: N/A
**Relates to**: [0013](./0013-artifact-versioning.md) (comments pin to a version)

---

## Context

The collaboration loop needs feedback: a reviewer leaves a note, the creator acts on it.
Comments are the second half of that loop (versioning was the first). They must attach to a
specific revision — feedback on v1 shouldn't look like feedback on v3 — which versioning
(ADR-0013) now makes possible.

---

## Decision

Add a `Comment` type and a view-gated comment API.

- **Type** (schema-first, `packages/schema/proto/artifacta/v1`):
  `Comment { id, slug, version, author_sub, author_email, created_at, body, resolved }`.
- **Version-pinned**: a comment records the version it was made on. The viewer shows the
  comments for the version currently being viewed.
- **Who can comment**: anyone who can _view_ the artifact (authenticated + `CanView`) — not
  just the owner. Recipients giving feedback is the point. Flat threads for v1 (no replies).
- **Who can resolve**: the artifact **owner** resolves a comment (they act on the feedback).
- **Not audited**: comments are collaboration content, not access-control events, so they do
  NOT go in the hash-chained audit trail (which stays publish/view/share/deny).
- **Endpoints** (all not-leaking 404 when unauthorized, like the rest):
  - `POST /artifacts/{slug}/comments` — CanView-gated; body `{body, version}`; author from the
    session; returns the created comment.
  - `GET /artifacts/{slug}/comments[?version=n]` — CanView-gated; lists comments (optionally
    for one version).
  - `POST /artifacts/{slug}/comments/{id}/resolve` — owner-only (the `ownedArtifact` gate).

---

## Consequences

### Positive

- Closes the review loop: reviewers comment, the owner sees and resolves — per version.
- Reuses the existing CanView gate and not-leak 404; no new access model.

### Negative

- Comment storage grows unbounded (pair with the TTL/retention reaper later).

### Neutral

- Comment bodies are user content → escape on render (the viewer renders via the DOM/text, not
  innerHTML). No third-party mentions/notifications yet.

## Alternatives Considered

### Threaded replies + @mentions + notifications

Deferred: flat comments deliver the loop now; threading/mentions/notifications are a later
increment.

### Owner-only commenting

Rejected: the people a doc is shared with are exactly who should be able to give feedback.

### Audit comments into the hash chain

Rejected: the audit trail is the access-control record; mixing collaboration content dilutes it.
