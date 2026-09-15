package domain

import (
	"sort"
	"strings"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

// Search parameter defaults and bounds (ADR-0025). page_size is clamped so a
// single request can never ask the store for an unbounded page.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
	SortByCreatedAt = "created_at"
	SortByTitle     = "title"
)

// SearchQuery is the normalized input to artifact search, shared by the pure
// in-memory path (FileStore) and the SQL-pushdown path (SQLStore) so both agree.
// ViewerSub/ViewerEmail scope the visible set; the store enforces visibility, so
// results can only ever be artifacts the caller could already view (CanView).
type SearchQuery struct {
	ViewerSub   string
	ViewerEmail string

	// Text is the free-text filter, matched (case-insensitively) against title,
	// slug, and label. Empty = no text filter.
	Text string
	// Email is matched (case-insensitively) against an artifact's owner_email
	// everywhere, and — only on the viewer's OWN artifacts — its grantees' emails.
	// The grant-list of a non-owned artifact is never consulted (ADR-0025). Empty
	// = no email filter.
	Email string
	// Visibility, when non-nil, restricts results to that visibility level.
	Visibility *artifactav1.Visibility

	Page     int32
	PageSize int32
	// SortBy is one of SortByCreatedAt / SortByTitle (Normalize whitelists it).
	SortBy string
	// SortDesc sorts descending on the primary key; the slug tie-break is always
	// ascending so paging is stable regardless of direction.
	SortDesc bool
}

// Normalize clamps and defaults the query so both store backends apply identical
// bounds: page>=1, 1<=page_size<=MaxPageSize (default DefaultPageSize), and a
// whitelisted sort_by (an unknown value falls back to created_at rather than
// silently no-op'ing). Idempotent.
func (q *SearchQuery) Normalize() {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = DefaultPageSize
	}
	if q.PageSize > MaxPageSize {
		q.PageSize = MaxPageSize
	}
	switch q.SortBy {
	case SortByCreatedAt, SortByTitle:
		// allowed
	default:
		q.SortBy = SortByCreatedAt
	}
}

// SearchArtifacts filters, sorts, and paginates arts in memory — the reference
// implementation the FileStore uses and the SQL path is checked against
// (store-conformance parity). arts is the caller's visible set; ownGrantsBySlug
// holds grants ONLY for the viewer's own artifacts (empty/nil when the email
// filter is off, or for a non-owner). It returns one page of matches plus the
// total match count across all pages.
//
// Invariants:
//   - dedup by slug (an artifact can reach the visible set via more than one arm,
//     e.g. it's both org-visible and owned), so total and the page window are right;
//   - case-insensitive substring matching (mirrors CanView's EqualFold);
//   - deterministic order (primary sort key, then slug ascending) for stable paging.
func SearchArtifacts(arts []*artifactav1.Artifact, ownGrantsBySlug map[string][]*artifactav1.Grant, q SearchQuery) (items []*artifactav1.Artifact, total int) {
	q.Normalize()

	text := strings.ToLower(q.Text)
	email := strings.ToLower(q.Email)

	seen := make(map[string]struct{}, len(arts))
	matched := make([]*artifactav1.Artifact, 0, len(arts))
	for _, a := range arts {
		if a == nil {
			continue
		}
		slug := a.GetSlug()
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}

		if q.Visibility != nil && a.GetVisibility() != *q.Visibility {
			continue
		}
		if text != "" && !matchText(a, text) {
			continue
		}
		if email != "" && !matchEmail(a, ownGrantsBySlug[slug], email) {
			continue
		}
		matched = append(matched, a)
	}

	total = len(matched)
	sortArtifacts(matched, q)

	start := int((q.Page - 1) * q.PageSize)
	if start >= total {
		return nil, total
	}
	end := start + int(q.PageSize)
	if end > total {
		end = total
	}
	return matched[start:end], total
}

// matchText reports whether text (already lower-cased) is a substring of the
// artifact's title, slug, or label.
func matchText(a *artifactav1.Artifact, text string) bool {
	return strings.Contains(strings.ToLower(a.GetTitle()), text) ||
		strings.Contains(strings.ToLower(a.GetSlug()), text) ||
		strings.Contains(strings.ToLower(a.GetLabel()), text)
}

// matchEmail reports whether email (already lower-cased) is a substring of the
// artifact's owner_email, or of any of the supplied grants' grantee_email. grants
// must be ONLY the viewer's own artifacts' grants — the caller guarantees a
// non-owned artifact contributes no grants here, so a foreign share-list is never
// probed (ADR-0025).
func matchEmail(a *artifactav1.Artifact, grants []*artifactav1.Grant, email string) bool {
	if oe := a.GetOwnerEmail(); oe != "" && strings.Contains(strings.ToLower(oe), email) {
		return true
	}
	for _, g := range grants {
		if ge := g.GetGranteeEmail(); ge != "" && strings.Contains(strings.ToLower(ge), email) {
			return true
		}
	}
	return false
}

// sortArtifacts orders in place by the primary key (created_at or title) then by
// slug ascending as a stable tie-break. SortDesc flips only the primary key.
func sortArtifacts(arts []*artifactav1.Artifact, q SearchQuery) {
	sort.SliceStable(arts, func(i, j int) bool {
		a, b := arts[i], arts[j]
		var cmp int
		switch q.SortBy {
		case SortByTitle:
			cmp = strings.Compare(strings.ToLower(a.GetTitle()), strings.ToLower(b.GetTitle()))
		default: // created_at
			ta, tb := a.GetCreatedAt().AsTime(), b.GetCreatedAt().AsTime()
			switch {
			case ta.Before(tb):
				cmp = -1
			case ta.After(tb):
				cmp = 1
			}
		}
		if cmp == 0 {
			return a.GetSlug() < b.GetSlug() // stable tie-break, always ascending
		}
		if q.SortDesc {
			return cmp > 0
		}
		return cmp < 0
	})
}
