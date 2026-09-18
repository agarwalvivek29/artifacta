# Spec: Artifact search + pagination

**Issue**: N/A (maintainer-driven)
**Status**: Approved
**Author**: Vivek Agarwal
**Date**: 2026-09-15
**Services Affected**: `artifacta-api` (proto + server + both store backends)

---

## Summary

Add an authorization-safe, paginated **search** endpoint (`GET /artifacts/search`) over everything
the caller can see (owned + shared-with-me + org). It filters by free text (title / slug / label),
by an email associated with the artifact (the publisher's `owner_email`, or — on the caller's own
artifacts only — a grantee's email), and by visibility, with page-based pagination. It also fills a
real gap: the service has **no pagination anywhere** today.

---

## Background and Motivation

`GET /artifacts` lists a caller's owned artifacts as a bare, unsorted, unpaginated JSON array (it
powers `artifacta ls`), and the dashboard renders three full lists (Mine / Shared / Org). Neither is
searchable, and nothing paginates. As artifact counts grow this becomes unusable — you cannot find
"that page Alice shared with me" or "the deck titled Q3" without scrolling everything.

Two facts shaped the design:

- The only email persisted on the domain today is the grant **recipient** (`Grant.grantee_email`).
  Neither the artifact owner's nor the sharer's email is stored — only opaque subjects. To answer
  "shared with me by `alice@x.com`" we must persist the owner's email.
- Grants are owner-only (`addGrant` gate), so the person who shares any artifact is always its
  owner. `Artifact.owner_email` alone therefore answers the "who shared it" case — no separate
  `granter_email` is needed.

---

## Scope

### In Scope

- Proto: `Artifact.owner_email`; new `ArtifactSummary` + `SearchArtifactsResponse` (reusing the
  existing `common/v1` pagination types).
- `GET /artifacts/search?q=&email=&visibility=&page=&page_size=&sort_by=&sort_order=`.
- A new `Store.SearchArtifacts` method: SQL-pushdown in the Postgres backend, in-memory in the file
  backend (via a shared pure domain function). Adopt page-based pagination (`common/v1`).
- `owner_email` captured at publish; promoted to an indexed column in the SQL backend.
- Unit + store-conformance + e2e tests, including a **grant-list-leak regression** test.

### Out of Scope

- CLI `artifacta search` (follow-up) and any viewer/dashboard search UI.
- Full-text/ranked/fuzzy search; cursor pagination (page-based per `common/v1`).
- Retrofitting pagination onto `GET /artifacts` / the dashboard (kept for CLI back-compat).
- Backfilling `owner_email` onto pre-existing artifacts (no `sub`→email directory exists).
- `Grant.granter_email` (redundant — granter is always the owner).

---

## Acceptance Criteria

- [ ] Given an authenticated caller, `GET /artifacts/search` returns a `SearchArtifactsResponse`
      `{items, page}` where `items` are drawn only from the caller's visible set (owned ∪
      shared-with-me ∪ org), deduped by slug, and `page` is a `common.v1.PaginationMeta`.
- [ ] `?q=` matches (case-insensitively) title, slug, or label; `?visibility=org` restricts to org.
- [ ] `?email=` matches `owner_email` across the visible set, AND `grantee_email` **only** on
      artifacts the caller owns.
- [ ] **Security**: a non-owner searching `?email=<a grantee of an org artifact they don't own>`
      does NOT surface that artifact — grantee emails of non-owned artifacts are never matched.
- [ ] Pagination: `page`/`page_size` window the results; `page_size` clamps to `[1,100]` (default
      20); `page` defaults to 1; `total`/`total_pages`/`has_next`/`has_prev` are correct.
- [ ] `sort_by` accepts `created_at` (default) or `title`; unknown values fall back to `created_at`
      (never a silent no-op); ties break by slug for stable paging.
- [ ] Unauthenticated → 401 (fail closed).
- [ ] The SQL backend and the file backend return identical items + total for the same inputs
      (store-conformance parity).

---

## Technical Design

### API Changes

New endpoint:

```
GET /artifacts/search?q=&email=&visibility=&page=&page_size=&sort_by=&sort_order=
  200 SearchArtifactsResponse { items: ArtifactSummary[], page: PaginationMeta }
  401 unauthenticated
ArtifactSummary { slug, title, visibility, latest_version, url, label, owner_email, created_at }
```

`owner_email` is returned on every row (accepted org-wide publisher-email disclosure; the deployment
is VPN-internal and org-visible already means org-readable). Existing `GET /artifacts` is unchanged.

### Data Model Changes

`Artifact.owner_email` (proto field 9), set at publish from the authenticated identity
(server-derived, never client-asserted). In the SQL backend it is also promoted to an indexed
`owner_email` column (GORM `AutoMigrate`, additive, no migration file). Grants still bind to the
immutable `sub`; `owner_email` is display/search metadata and is never used by `domain.CanView`.

### Authorization

Visibility is enforced _inside_ the query/union, never post-filtered: results can only be artifacts
the caller could already open. Grantee-email matching is restricted to the caller's own artifacts,
closing a would-be leak where a caller could probe the share-list of an org artifact they don't own
(`Store.Grants` has no owner gate — so search must not read grants for non-owned slugs).

### owner_email limitations (documented, not bugs)

1. **Backfill**: existing artifacts have empty `owner_email` until re-published — no `sub`→email
   directory to backfill from. The `email` filter won't match old rows on `owner_email`.
2. **Staleness**: `sub` is the stable key; a later email change leaves `owner_email` stale (no
   refresh path). Acceptable for v1; a directory would fix both.

---

## Security Considerations

- Fail-closed 401 for unauthenticated callers, mirroring `listArtifacts`.
- Grantee emails are matched/returned only for artifacts the caller owns (no cross-owner share-list
  enumeration). `owner_email` disclosure is an accepted, documented org-wide behavior.
- Search terms ride in the query string; per ADR-0022 query strings are never logged (PII-safe).
- Rate-limited like the other content routes (`contentPerMinute`).

---

## Observability

- **Logs**: structured request logs via `api.Observe` (route pattern `/artifacts/search` is the
  metric label; query string not logged).
- **Metrics**: inherits `http_requests_total` / `_duration_seconds` with the matched route label.

---

## Testing Plan

### Unit Tests

- `domain.SearchArtifacts` / `SearchQuery.Normalize`: each filter dimension, combined filters,
  visibility, case-insensitivity, dedup-by-slug, sort determinism + `sort_by` whitelist, pagination
  edges (page<1, size 0/101, page past end).
- Handler: 401; owned+shared+org rows; `owner_email` matched everywhere + `grantee_email` only on
  owned; **CRITICAL** non-owner cannot match a foreign org artifact's grantee_email; envelope shape.

### Integration Tests

- Store conformance (`runStoreConformance`, runs on file + postgres): `SearchArtifacts` filtering,
  dedup, owner-scoped grantee match, pagination + total — identical on both backends (parity).
- E2E (`flow_test.go`): alice publishes 3, shares one with bob by email; bob's `?q=`/`?email=` see
  only what bob can see; alice's `?email=bob@` finds the shared one; pagination across 2 pages.

---

## Migration / Rollout Plan

- **Database migrations**: additive `owner_email` column via GORM `AutoMigrate` on boot (no file).
- **Breaking changes**: none. Proto field is additive; `GET /artifacts` + dashboard unchanged.
- **Feature flag**: none.
- **Rollback plan**: revert the PR — additive field/column/endpoint touch no existing shape or auth.

---

## References

- ADR: [0025-artifact-search-and-pagination.md](../adr/0025-artifact-search-and-pagination.md)
- Related ADRs: [0009](../adr/0009-postgres-store-adapter.md) (store adapter),
  [0018](../adr/0018-anonymous-vpn-gated-visibility.md),
  [0019](../adr/0019-invite-by-email-grants.md) (grantee_email), [0002](../adr/0002-auth-model.md)
- Reuses: `packages/schema/proto/common/v1/pagination.proto`
