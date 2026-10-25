package api

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"ajsaccountd/internal/model"
)

// TestUserAllowlistAcceptsHostnamesAndWildcards covers the write path through
// the HTTP layer: hostnames, `*.domain` wildcards, IPs and CIDRs are accepted
// and echoed normalized, while a malformed entry is a 400 that leaves the
// stored list untouched.
func TestUserAllowlistAcceptsHostnamesAndWildcards(t *testing.T) {
	ts, st := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/users", map[string]any{
		"name":      "edge.bot",
		"allowlist": []string{"API.Example.COM.", "*.api.example.com", "10.1.2.3/8", "192.168.1.5"},
	})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[model.PanelUser](t, body)
	want := []string{"api.example.com", "*.api.example.com", "10.0.0.0/8", "192.168.1.5"}
	if !slices.Equal(created.Allowlist, want) {
		t.Fatalf("created allowlist = %v, want %v", created.Allowlist, want)
	}
	stored, ok := st.User(created.ID)
	if !ok || !slices.Equal(stored.Allowlist, want) {
		t.Fatalf("stored allowlist = %v (ok=%v), want %v", stored.Allowlist, ok, want)
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/users/"+created.ID+"/allowlist",
		map[string]any{"allowlist": []string{"*.Example.com"}})
	requireStatus(t, resp, http.StatusOK)
	replaced := decodeBody[model.PanelUser](t, body)
	if !slices.Equal(replaced.Allowlist, []string{"*.example.com"}) {
		t.Fatalf("replaced allowlist = %v, want [*.example.com]", replaced.Allowlist)
	}

	// PATCH /v1/users carries the allowlist through the same normalization.
	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/users/"+created.ID,
		map[string]any{"allowlist": []string{"API.Example.com."}})
	requireStatus(t, resp, http.StatusOK)
	patched := decodeBody[model.PanelUser](t, body)
	if !slices.Equal(patched.Allowlist, []string{"api.example.com"}) {
		t.Fatalf("patched allowlist = %v, want [api.example.com]", patched.Allowlist)
	}

	for _, entry := range []string{"*", "**.example.com", "https://example.com", "example.com/path", "example.com:443"} {
		resp, body = apiCall(t, ts, http.MethodPatch, "/v1/users/"+created.ID+"/allowlist",
			map[string]any{"allowlist": []string{entry}})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	}
	if stored, _ := st.User(created.ID); !slices.Equal(stored.Allowlist, []string{"api.example.com"}) {
		t.Fatalf("stored allowlist after refused writes = %v, want [api.example.com]", stored.Allowlist)
	}

	// The 64-entry cap is enforced at the API too.
	tooMany := make([]string, 0, 65)
	for i := range 65 {
		tooMany = append(tooMany, fmt.Sprintf("host-%02d.example.com", i))
	}
	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/users/"+created.ID+"/allowlist",
		map[string]any{"allowlist": tooMany})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	// An empty list is a valid (if useless) policy: it authorizes nothing.
	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/users/"+created.ID+"/allowlist",
		map[string]any{"allowlist": []string{}})
	requireStatus(t, resp, http.StatusOK)
	emptied := decodeBody[model.PanelUser](t, body)
	if len(emptied.Allowlist) != 0 {
		t.Fatalf("emptied allowlist = %v, want []", emptied.Allowlist)
	}
}
