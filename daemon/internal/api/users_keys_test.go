package api

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

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

// usageUserRows mirrors the per-user fields this suite asserts.
type usageUserRows struct {
	Users []struct {
		UserID          string `json:"userId"`
		Bytes           int64  `json:"bytes"`
		QuotaBytesMonth *int64 `json:"quotaBytesMonth"`
		UsedBytes       int64  `json:"usedBytes"`
	} `json:"users"`
}

// TestUsageReportsDerivedUserUsedBytes proves GET /v1/usage reports the same
// derived current-month gate total that GET /v1/users does: usedBytes is the
// monthly figure a quota is measured against, not the window byte total, and a
// proxy-sourced row inside the window never inflates it.
func TestUsageReportsDerivedUserUsedBytes(t *testing.T) {
	ts, st := newPlatformServer(t, Config{Usage: usageAggregator()})
	now := time.Now().UTC()

	quota := int64(1_000)
	user, err := st.CreateUser(model.CreateUserInput{Name: "usage.bot", QuotaBytesMonth: &quota})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	st.AppendRequest(model.RequestLog{
		TS: now.Format(time.RFC3339), UserID: user.ID, UserName: user.Name,
		AccountLabel: "Northwind Ops", Target: "example.com:443",
		Status: http.StatusOK, Bytes: 700, Source: model.SourceGate,
	})
	st.AppendRequest(model.RequestLog{
		TS: now.Format(time.RFC3339), UserID: user.ID, UserName: user.Name,
		AccountLabel: "Northwind Ops", Target: "example.com:443",
		Status: http.StatusOK, Bytes: 5_000, Source: model.SourceProxy,
	})

	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/usage?from="+from+"&to="+to, nil)
	requireStatus(t, resp, http.StatusOK)
	summary := decodeBody[usageUserRows](t, body)
	found := false
	for _, row := range summary.Users {
		if row.UserID != user.ID {
			continue
		}
		found = true
		if row.UsedBytes != 700 {
			t.Fatalf("usage user usedBytes = %d, want 700 (gate rows for the month only)", row.UsedBytes)
		}
		if row.Bytes != 5_700 {
			t.Fatalf("usage user bytes = %d, want 5700 (the window total)", row.Bytes)
		}
		if row.QuotaBytesMonth == nil || *row.QuotaBytesMonth != quota {
			t.Fatalf("usage user quota = %v, want %d", row.QuotaBytesMonth, quota)
		}
	}
	if !found {
		t.Fatalf("usage summary has no row for %s: %s", user.ID, body)
	}

	// The panel list reports the same figure, so both surfaces agree.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/users", nil)
	requireStatus(t, resp, http.StatusOK)
	listed := false
	for _, panelUser := range decodeBody[[]model.PanelUser](t, body) {
		if panelUser.ID != user.ID {
			continue
		}
		listed = true
		if panelUser.UsedBytes != 700 {
			t.Fatalf("panel user usedBytes = %d, want 700", panelUser.UsedBytes)
		}
	}
	if !listed {
		t.Fatalf("GET /v1/users has no row for %s: %s", user.ID, body)
	}
}
