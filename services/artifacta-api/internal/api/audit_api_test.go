package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
	"google.golang.org/protobuf/encoding/protojson"
)

// GET /audit is owner-scoped: a caller sees only audit rows that concern them —
// their own actions, or events about artifacts they own — never the instance-wide
// trail. Every returned row verifies its own hash client-side (the remote
// `artifacta audit verify` guarantee). Unauthenticated callers get 401.
func TestAuditEndpointOwnerScopedAndVerifiable(t *testing.T) {
	owner := &artifactav1.Identity{Sub: "local:owner", Email: "owner@corp.com"}
	sam := fakeAuth{id: &artifactav1.Identity{Sub: "sam-sub", Email: "sam@corp.com"}, ok: true}
	srv := newTestServer(t, t.TempDir(), fakeAuth{id: owner, ok: true})

	// A LINK artifact owned by owner: anyone may view, so views by both principals
	// produce allowed VIEW audit rows (seed bypasses publish, so views are what
	// populate the trail here).
	seed(t, srv, "doc", owner.GetSub(), artifactav1.Visibility_VISIBILITY_LINK, "<h1>hi</h1>")

	get := func(auth Auth, target string) *httptest.ResponseRecorder {
		srv.Auth = auth
		rr := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		return rr
	}

	// Generate audit rows: owner views doc, then sam views doc.
	if rr := get(fakeAuth{id: owner, ok: true}, "/a/doc/raw"); rr.Code != http.StatusOK {
		t.Fatalf("owner view: got %d, want 200", rr.Code)
	}
	if rr := get(sam, "/a/doc/raw"); rr.Code != http.StatusOK {
		t.Fatalf("sam view: got %d, want 200", rr.Code)
	}

	decode := func(rr *httptest.ResponseRecorder) []*artifactav1.AuditEvent {
		t.Helper()
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /audit: got %d, want 200 (body %q)", rr.Code, rr.Body.String())
		}
		var raw []json.RawMessage
		if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode array: %v", err)
		}
		out := make([]*artifactav1.AuditEvent, 0, len(raw))
		for _, r := range raw {
			ev := &artifactav1.AuditEvent{}
			if err := protojson.Unmarshal(r, ev); err != nil {
				t.Fatalf("decode event: %v", err)
			}
			out = append(out, ev)
		}
		return out
	}

	// Owner sees events about their artifact (doc), including sam's view.
	ownerEvents := decode(get(fakeAuth{id: owner, ok: true}, "/audit"))
	if n, err := infra.VerifyRowHashes(ownerEvents); err != nil || n != len(ownerEvents) {
		t.Fatalf("owner rows verify: n=%d err=%v", n, err)
	}
	sawSamView := false
	for _, ev := range ownerEvents {
		if ev.GetPrincipalSub() == sam.id.GetSub() && ev.GetSlug() == "doc" {
			sawSamView = true
		}
	}
	if !sawSamView {
		t.Fatalf("owner should see sam's view of their artifact; events=%+v", ownerEvents)
	}

	// Sam sees only their own rows — never the owner's events about doc.
	samEvents := decode(get(sam, "/audit"))
	if n, err := infra.VerifyRowHashes(samEvents); err != nil || n != len(samEvents) {
		t.Fatalf("sam rows verify: n=%d err=%v", n, err)
	}
	for _, ev := range samEvents {
		if ev.GetPrincipalSub() == owner.GetSub() {
			t.Fatalf("sam must not see owner's audit rows; got %+v", ev)
		}
	}

	// Unauthenticated: fail closed.
	if rr := get(fakeAuth{ok: false}, "/audit"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /audit: got %d, want 401", rr.Code)
	}
}
