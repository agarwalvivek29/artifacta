# 0025 — Artifact search + page-based pagination

**Date**: 2026-09-15
**Status**: Accepted
**Deciders**: Vivek Agarwal
**Issue**: N/A (maintainer-driven)
**Relates to**: [0009](./0009-postgres-store-adapter.md) (store adapter + conformance suite),
[0019](./0019-invite-by-email-grants.md) (grantee_email), [0018](./0018-anonymous-vpn-gated-visibility.md),
[0002](./0002-auth-model.md) (grants bind to sub, not email), [0011](./0011-vpn-fronted-threat-model.md)

---

## Context

The service can list artifacts (`GET /artifacts`, the dashboard) but cannot **search** them, and
**nothing paginates** — every listing returns the full set as a bare JSON array. That does not hold
as artifact counts grow. Users want to search by title, by link (slug/label), and by the email of a
person an artifact involves ("shared with me by alice@x.com" / "what did I share with bob@x.com").

Two constraints forced real decisions:

- The only email persisted today is the grant **recipient** (`Grant.grantee_email`, ADR-0019).
  Neither the owner's nor the sharer's email is stored (only opaque `sub`). "Shared with me by X"
  needs the owner's email persisted.
- `Store.Grants(slug)` has **no owner gate** — today only owners read grants. A naive email filter
  that reads grants across the whole visible set would let a caller enumerate the share-list (and
  grantee emails) of an org artifact they don't own.

---

## Decision

1. **New endpoint** `GET /artifacts/search` over the caller's visible set (owned ∪ shared-with-me ∪
   org, deduped by slug), filtering by `q` (title/slug/label), `email`, and `visibility`, with
   page-based pagination. Existing `GET /artifacts` is unchanged (CLI back-compat); search is the
   paginated surface.

2. **Adopt the existing `common/v1` pagination model** (`PaginationRequest`/`PaginationMeta`,
   1-based page + page_size, offset-style) as the service's first paginated endpoint. Not cursor
   pagination — page-based is what the shared proto already defines and it fits a bounded,
   VPN-internal corpus.

3. **Persist `Artifact.owner_email`** (proto field 9), set at publish from the authenticated
   identity. No `granter_email`: grants are owner-only, so the granter is always the owner, and
   `owner_email` alone answers "who shared it". `owner_email` is display/search metadata — grants
   still bind to the immutable `sub` (ADR-0002) and `CanView` never consults it.

4. **New `Store.SearchArtifacts` method with SQL pushdown.** The Postgres backend filters, sorts,
   and paginates inside one query (visibility union + `COUNT` + `LIMIT/OFFSET`); `owner_email` is
   promoted to an indexed column. The file backend does the equivalent in memory via a shared
   **pure** `domain.SearchArtifacts` function. The store-conformance suite (ADR-0009) runs both, so
   the two paths are verified identical, not assumed.

5. **Close the grant-list leak**: `email` matches `owner_email` across the visible set, but
   `grantee_email` **only on artifacts the caller owns**. Non-owners can never match or enumerate a
   foreign artifact's share-list. Union results are deduped by slug so `total` and page windows are
   correct.

6. **Return `owner_email` on every row** — accepted org-wide publisher-email disclosure. The
   deployment is VPN-internal and org-visible already means org-readable; the grant-list stays
   owner-only.

---

## Consequences

### Positive

- Searchable, paginated artifact discovery; first paginated endpoint sets the `common/v1` precedent.
- SQL pushdown keeps cost bounded at scale (indexed `owner_email`, `grantee_email`; `LIMIT/OFFSET`).
- Authz-safe by construction: results are a subset of what `CanView` already permits; no share-list
  leak; parity-tested across both backends.

### Negative

- `owner_email` is a denormalized snapshot: empty for pre-existing artifacts (no `sub`→email
  directory to backfill) and stale if a user's email later changes. The `email` filter therefore
  silently under-returns on old/renamed rows — documented in the spec so it is expected behavior.
- Title/created_at live inside the `jsonb data` blob, so the SQL path reads them via `data->>` —
  couples the query to Postgres JSON operators (fine; SQLite is not a supported backend, ADR-0009).

### Neutral

- `GET /artifacts` and the dashboard stay unpaginated for now; retrofitting them is deferred to
  preserve CLI back-compat.

---

## Alternatives Considered

- **Extend `GET /artifacts` with `?q=`/pagination** instead of a new route — rejected: it would
  change that endpoint's response shape (bare array → envelope) and break `artifacta ls`.
- **In-memory filtering only (no SQL pushdown)** — rejected: full org-corpus load + per-slug grant
  reads (N+1) per request; does not scale, and the N+1 was itself the vector for the grant leak.
- **Cut email search from v1** (ship q + visibility only) — considered; it removes the proto change,
  the backfill mess, and the leak. Rejected because "search by email" is a core ask; instead the
  leak is closed by owner-scoping the grantee match.
- **Persist `granter_email` too** — rejected as redundant: `addGrant` is owner-only, so granter ==
  owner always.
- **Cursor pagination** — rejected: `common/v1` already defines page-based; corpus is bounded.

---

## Implementation

- `Artifact.owner_email = 9`; new `search.proto` (`ArtifactSummary`, `SearchArtifactsResponse` with
  `common.v1.PaginationMeta`). Regenerated Go committed with the proto.
- `Store.SearchArtifacts(domain.SearchQuery) ([]*Artifact, int, error)` on both backends; SQLStore
  adds an indexed `owner_email` column set in `PutArtifact` (AutoMigrate, additive).
- `domain.SearchArtifacts` (pure) + `SearchQuery.Normalize` (page≥1, page_size∈[1,100] default 20,
  `sort_by` whitelist `{created_at,title}`, default `created_at desc`, slug tiebreak).
- `publish` sets `owner_email = who.GetEmail()`. Handler `GET /artifacts/search` fails closed (401),
  rate-limited like other content routes.
- Tests: domain unit, handler unit (incl. the grant-leak regression), store conformance parity, e2e.

**Follow-up (0.0.13):** added an optional `Artifact.description` — free-text publisher
metadata included in the `q` substring match (both store paths) so an agent can find a past
artifact by more than its title. Captured via `POST /artifacts?description=`, the upload form, and
`artifacta publish --description`; returned by metadata + search; shown as a dashboard subtitle.
Like `owner_email` it is display/search metadata only — never consulted by `CanView`.
