package domain

import (
	"strings"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
)

// artifact is a terse constructor for the table cases below.
func artifact(owner string, vis artifactav1.Visibility) *artifactav1.Artifact {
	return &artifactav1.Artifact{Slug: "doc", OwnerSub: owner, Visibility: vis}
}

// grant to a grantee on a slug. CanView keys on both grantee and slug.
func grant(slug, grantee string) *artifactav1.Grant {
	return &artifactav1.Grant{Slug: slug, GranteeSub: grantee}
}

func TestCanView(t *testing.T) {
	const owner = "sub-owner"
	const viewer = "sub-viewer"

	tests := []struct {
		name   string
		art    *artifactav1.Artifact
		who    *artifactav1.Identity
		grants []*artifactav1.Grant
		want   bool
	}{
		// Fail-closed: no authenticated subject can ever view.
		{"nil identity denied", artifact(owner, artifactav1.Visibility_VISIBILITY_ORG), nil, nil, false},
		{"empty sub denied", artifact(owner, artifactav1.Visibility_VISIBILITY_ORG), &artifactav1.Identity{Sub: ""}, nil, false},
		{"org nil identity denied", artifact(owner, artifactav1.Visibility_VISIBILITY_ORG), nil, nil, false},
		{"org empty sub denied", artifact(owner, artifactav1.Visibility_VISIBILITY_ORG), &artifactav1.Identity{Sub: ""}, nil, false},

		// Owner always wins, regardless of visibility.
		{"owner sees private", artifact(owner, artifactav1.Visibility_VISIBILITY_PRIVATE), &artifactav1.Identity{Sub: owner}, nil, true},
		{"owner sees invited", artifact(owner, artifactav1.Visibility_VISIBILITY_INVITED), &artifactav1.Identity{Sub: owner}, nil, true},
		{"owner sees org", artifact(owner, artifactav1.Visibility_VISIBILITY_ORG), &artifactav1.Identity{Sub: owner}, nil, true},
		{"owner sees unspecified", artifact(owner, artifactav1.Visibility_VISIBILITY_UNSPECIFIED), &artifactav1.Identity{Sub: owner}, nil, true},

		// PRIVATE: only the owner, never a non-owner.
		{"private non-owner denied", artifact(owner, artifactav1.Visibility_VISIBILITY_PRIVATE), &artifactav1.Identity{Sub: viewer}, nil, false},
		{"private non-owner with stray grant denied", artifact(owner, artifactav1.Visibility_VISIBILITY_PRIVATE), &artifactav1.Identity{Sub: viewer}, []*artifactav1.Grant{grant("doc", viewer)}, false},

		// INVITED: non-owner needs a grant whose grantee matches.
		{"invited matching grant allowed", artifact(owner, artifactav1.Visibility_VISIBILITY_INVITED), &artifactav1.Identity{Sub: viewer}, []*artifactav1.Grant{grant("doc", viewer)}, true},
		{"invited no grants denied", artifact(owner, artifactav1.Visibility_VISIBILITY_INVITED), &artifactav1.Identity{Sub: viewer}, nil, false},
		{"invited grant for other grantee denied", artifact(owner, artifactav1.Visibility_VISIBILITY_INVITED), &artifactav1.Identity{Sub: viewer}, []*artifactav1.Grant{grant("doc", "sub-someone-else")}, false},
		{"invited matching grantee among many allowed", artifact(owner, artifactav1.Visibility_VISIBILITY_INVITED), &artifactav1.Identity{Sub: viewer}, []*artifactav1.Grant{grant("doc", "sub-a"), grant("doc", viewer), grant("doc", "sub-b")}, true},

		// ORG: any authenticated non-owner is allowed.
		{"org non-owner allowed", artifact(owner, artifactav1.Visibility_VISIBILITY_ORG), &artifactav1.Identity{Sub: viewer}, nil, true},

		// UNSPECIFIED: closed to non-owners (no case in the switch).
		{"unspecified non-owner denied", artifact(owner, artifactav1.Visibility_VISIBILITY_UNSPECIFIED), &artifactav1.Identity{Sub: viewer}, nil, false},
		{"unspecified non-owner with grant denied", artifact(owner, artifactav1.Visibility_VISIBILITY_UNSPECIFIED), &artifactav1.Identity{Sub: viewer}, []*artifactav1.Grant{grant("doc", viewer)}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanView(tt.art, tt.who, tt.grants); got != tt.want {
				t.Errorf("CanView() = %v, want %v", got, tt.want)
			}
		})
	}
}

// CanView scopes grants by slug (FR29, F-1): it matches on both grantee AND
// slug, so a grant for a DIFFERENT slug never authorizes even if the grantee
// matches. This is defense-in-depth — the decision holds even when the caller
// passes an unfiltered grants list.
func TestCanView_slugScoped(t *testing.T) {
	art := artifact("sub-owner", artifactav1.Visibility_VISIBILITY_INVITED)
	who := &artifactav1.Identity{Sub: "sub-viewer"}
	grants := []*artifactav1.Grant{grant("a-different-slug", "sub-viewer")}
	if CanView(art, who, grants) {
		t.Errorf("CanView() = true; expected false — a wrong-slug grant must not authorize")
	}
}

func TestNewSlug(t *testing.T) {
	s := NewSlug()

	if s == "" {
		t.Fatal("NewSlug() returned empty string")
	}
	if s != strings.ToLower(s) {
		t.Errorf("NewSlug() = %q, expected all lower-case", s)
	}

	// URL-safe base32 (no padding), lower-cased: a-z and 2-7 only.
	const allowed = "abcdefghijklmnopqrstuvwxyz234567"
	for _, r := range s {
		if !strings.ContainsRune(allowed, r) {
			t.Errorf("NewSlug() = %q contains disallowed rune %q", s, r)
		}
	}

	// 16 random bytes → 26 base32 chars with no padding.
	if len(s) != 26 {
		t.Errorf("NewSlug() length = %d, want 26", len(s))
	}
}

func TestNewSlug_unique(t *testing.T) {
	// Two separate calls, compared via variables: NewSlug is random, so this is a
	// real uniqueness check (not the identical-expression pattern staticcheck warns
	// about when both sides are written inline).
	a, b := NewSlug(), NewSlug()
	if a == b {
		t.Error("two successive NewSlug() calls returned the same value")
	}
}
