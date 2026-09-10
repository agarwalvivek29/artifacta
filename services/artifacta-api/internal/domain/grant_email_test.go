package domain

import (
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

// Invite-by-email (ADR-0019): an INVITED artifact admits a caller whose verified
// email matches a grant's grantee_email, case-insensitively, even when the grant
// carries no subject yet. It must NOT match on empty emails or the wrong email,
// and it stays slug-scoped.
func TestCanView_invitedByEmail(t *testing.T) {
	art := artifact("owner", artifactav1.Visibility_VISIBILITY_INVITED) // slug "doc"
	emailGrant := &artifactav1.Grant{Slug: "doc", GranteeEmail: "sam@corp.com"}

	cases := []struct {
		name   string
		who    *artifactav1.Identity
		grants []*artifactav1.Grant
		want   bool
	}{
		{"exact email match", &artifactav1.Identity{Sub: "new", Email: "sam@corp.com"}, []*artifactav1.Grant{emailGrant}, true},
		{"case-insensitive email match", &artifactav1.Identity{Sub: "new", Email: "SAM@Corp.com"}, []*artifactav1.Grant{emailGrant}, true},
		{"wrong email denied", &artifactav1.Identity{Sub: "new", Email: "eve@corp.com"}, []*artifactav1.Grant{emailGrant}, false},
		{"empty caller email never matches empty grant email",
			&artifactav1.Identity{Sub: "new", Email: ""}, []*artifactav1.Grant{{Slug: "doc"}}, false},
		{"email grant for another slug denied",
			&artifactav1.Identity{Sub: "new", Email: "sam@corp.com"}, []*artifactav1.Grant{{Slug: "other", GranteeEmail: "sam@corp.com"}}, false},
		{"anonymous denied", nil, []*artifactav1.Grant{emailGrant}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanView(art, tc.who, tc.grants); got != tc.want {
				t.Errorf("CanView() = %v, want %v", got, tc.want)
			}
		})
	}
}
