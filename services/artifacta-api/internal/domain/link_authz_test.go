package domain

import (
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

// VISIBILITY_LINK (ADR-0018) is the one level that admits an anonymous caller:
// the perimeter is the network (VPN) + the unguessable slug, not an identity.
// These cases lock that carve-out down — and confirm it does NOT leak into any
// other level (the fail-closed guarantee for private/invited/org must survive).
func TestCanView_link(t *testing.T) {
	const owner = "sub-owner"
	const stranger = "sub-stranger"
	link := func() *artifactav1.Artifact { return artifact(owner, artifactav1.Visibility_VISIBILITY_LINK) }

	cases := []struct {
		name string
		who  *artifactav1.Identity
		want bool
	}{
		{"link anonymous (nil) allowed", nil, true},
		{"link empty-sub allowed", &artifactav1.Identity{Sub: ""}, true},
		{"link owner allowed", &artifactav1.Identity{Sub: owner}, true},
		{"link stranger allowed", &artifactav1.Identity{Sub: stranger}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanView(link(), tc.who, nil); got != tc.want {
				t.Errorf("CanView(LINK) = %v, want %v", got, tc.want)
			}
		})
	}

	// Regression guard: the anonymous carve-out must be unique to LINK. Every other
	// level must still deny a nil identity.
	for _, vis := range []artifactav1.Visibility{
		artifactav1.Visibility_VISIBILITY_UNSPECIFIED,
		artifactav1.Visibility_VISIBILITY_PRIVATE,
		artifactav1.Visibility_VISIBILITY_INVITED,
		artifactav1.Visibility_VISIBILITY_ORG,
	} {
		if CanView(artifact(owner, vis), nil, nil) {
			t.Errorf("CanView(%v) admitted an anonymous caller — fail-closed broken", vis)
		}
	}
}
