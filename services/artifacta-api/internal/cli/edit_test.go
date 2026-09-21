package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEditRemoteSendsBearerPatchAndOnlySetFields verifies the edit helper PATCHes
// the bare /artifacts/<slug> path with the Bearer token, and sends ONLY the fields
// that were set — so editing one field never blanks the other, while an explicit
// empty string is sent (to clear a description).
func TestEditRemoteSendsBearerPatchAndOnlySetFields(t *testing.T) {
	const token = "id-token-xyz"
	const slug = "abc123"
	str := func(s string) *string { return &s }

	cases := []struct {
		name        string
		title       *string
		description *string
		want        map[string]string // expected present keys → values
		absent      []string          // keys that must NOT be present
	}{
		{name: "both", title: str("New Title"), description: str("desc"),
			want: map[string]string{"title": "New Title", "description": "desc"}},
		{name: "title only", title: str("Only Title"),
			want: map[string]string{"title": "Only Title"}, absent: []string{"description"}},
		{name: "clear description", description: str(""),
			want: map[string]string{"description": ""}, absent: []string{"title"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotAuth string
			var raw map[string]json.RawMessage
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
				_ = json.NewDecoder(r.Body).Decode(&raw)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			if err := editRemote(srv.URL, token, slug, tc.title, tc.description); err != nil {
				t.Fatalf("editRemote: %v", err)
			}
			if gotMethod != http.MethodPatch {
				t.Fatalf("method = %q, want PATCH", gotMethod)
			}
			if want := "/artifacts/" + slug; gotPath != want {
				t.Fatalf("path = %q, want %q", gotPath, want)
			}
			if gotAuth != "Bearer "+token {
				t.Fatalf("Authorization = %q, want Bearer <token>", gotAuth)
			}
			for k, v := range tc.want {
				rawv, ok := raw[k]
				if !ok {
					t.Fatalf("body missing key %q (body %v)", k, raw)
				}
				var got string
				if err := json.Unmarshal(rawv, &got); err != nil || got != v {
					t.Fatalf("body[%q] = %q (err %v), want %q", k, got, err, v)
				}
			}
			for _, k := range tc.absent {
				if _, ok := raw[k]; ok {
					t.Fatalf("body should not contain key %q (body %v)", k, raw)
				}
			}
		})
	}
}
