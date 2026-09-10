// Package domain holds here.now's business logic. It has zero framework
// dependencies and operates only on the generated schema types.
package domain

import (
	"crypto/rand"
	"encoding/base32"
	"strings"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

// CanView is the per-artifact authorization decision. Runs on every view.
//
// Fails closed for every level EXCEPT VISIBILITY_LINK: an unauthenticated caller
// (nil/empty subject) can never view a private/invited/org artifact. The single
// carve-out is VISIBILITY_LINK (ADR-0018): "no login required" artifacts whose
// perimeter is the network (the VPN-fronted deployment, ADR-0011) plus the
// unguessable slug, not an authenticated identity. That check is deliberately
// placed BEFORE the anonymous fail-closed guard, and it is the only path by which
// an anonymous caller reaches an allow — keep it that way.
func CanView(a *artifactav1.Artifact, who *artifactav1.Identity, grants []*artifactav1.Grant) bool {
	// LINK: no-login, network-gated. Anonymous callers are admitted here and ONLY
	// here. Any authenticated caller is admitted too (they can also reach it).
	if a.GetVisibility() == artifactav1.Visibility_VISIBILITY_LINK {
		return true
	}
	// Every other level requires an authenticated principal. Fail closed.
	if who == nil || who.GetSub() == "" {
		return false
	}
	if who.GetSub() == a.GetOwnerSub() {
		return true
	}
	switch a.GetVisibility() {
	case artifactav1.Visibility_VISIBILITY_ORG:
		return true // any authenticated user; group scoping arrives with OIDC
	case artifactav1.Visibility_VISIBILITY_INVITED:
		for _, g := range grants {
			// Match grantee AND slug: defense-in-depth so the decision holds
			// even if the caller passes an unfiltered grants list.
			if g.GetGranteeSub() == who.GetSub() && g.GetSlug() == a.GetSlug() {
				return true
			}
		}
	}
	return false
}

// NewSlug returns a random, high-entropy, URL-safe slug (128 bits). Slugs are
// defense-in-depth, NOT the access control — authorization is CanView.
func NewSlug() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}
