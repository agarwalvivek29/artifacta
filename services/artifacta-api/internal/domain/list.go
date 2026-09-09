package domain

import (
	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

// ListVisibleTo returns the subset of arts that who may view, reusing CanView
// for the per-artifact decision. For each artifact it passes only the grants
// whose slug matches that artifact, so cross-slug grants never leak. Input
// order is preserved; the result is nil when nothing is visible.
func ListVisibleTo(who *artifactav1.Identity, arts []*artifactav1.Artifact, grants []*artifactav1.Grant) []*artifactav1.Artifact {
	var out []*artifactav1.Artifact
	for _, a := range arts {
		var scoped []*artifactav1.Grant
		for _, g := range grants {
			if g.GetSlug() == a.GetSlug() {
				scoped = append(scoped, g)
			}
		}
		if CanView(a, who, scoped) {
			out = append(out, a)
		}
	}
	return out
}
