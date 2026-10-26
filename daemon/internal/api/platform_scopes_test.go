package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/health"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/renewal"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/usage"
)

// newScopedPlatformServer builds the platform surface the scoped-read matrix
// needs: usage aggregation, the static renewal catalogue and a health sampler
// with a closed-pipe dialer (no real gate is probed).
func newScopedPlatformServer(t *testing.T) (*httptest.Server, *store.Store, *health.Sampler) {
	t.Helper()
	sampler := health.New(health.Config{
		Interval: time.Hour,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sampler.Start(ctx)
	t.Cleanup(sampler.Stop)

	ts, st := newPlatformServer(t, Config{
		Usage:   usage.New(usage.Config{}),
		Renewal: renewal.New(renewal.Config{BaseURL: "http://127.0.0.1:1", TTL: time.Hour}),
		Health:  sampler,
	})
	return ts, st, sampler
}

// TestScopedPlatformReadScopeMatrix pins the route → scope table: each new read
// scope reaches exactly its routes, every other scoped key gets 403 FORBIDDEN,
// and webhooks/backup stay admin-only. An admin-scoped key bypasses the table.
func TestScopedPlatformReadScopeMatrix(t *testing.T) {
	ts, _, sampler := newScopedPlatformServer(t)
	for id, port := range map[string]int{"node-01": 13000, "node-04": 13003} {
		if err := sampler.Watch(id, port); err != nil {
			t.Fatalf("watch %s: %v", id, err)
		}
	}

	routes := []struct {
		method string
		path   string
		scope  model.Scope
	}{
		{http.MethodGet, "/v1/usage", model.ScopeUsageRead},
		{http.MethodGet, "/v1/renewal/plans", model.ScopeRenewalRead},
		{http.MethodGet, "/v1/renewal/accounts/acc-01", model.ScopeRenewalRead},
		{http.MethodGet, "/v1/health/nodes", model.ScopeHealthRead},
		// Admin-only routes: no read scope may reach them.
		{http.MethodPost, "/v1/webhooks", ""},
		{http.MethodPost, "/v1/backup/inspect", ""},
	}
	for _, scope := range []model.Scope{model.ScopeUsageRead, model.ScopeRenewalRead, model.ScopeHealthRead} {
		t.Run(string(scope), func(t *testing.T) {
			created, _ := mustCreateScopedKey(t, ts, map[string]any{
				"userId": "u-01",
				"label":  "read " + string(scope),
				"scopes": []string{string(scope)},
			})
			for _, route := range routes {
				resp, body := scopedCall(t, ts, created.Secret, route.method, route.path, nil)
				if route.scope == scope {
					requireStatus(t, resp, http.StatusOK)
					continue
				}
				requireStatus(t, resp, http.StatusForbidden)
				requireErrorCode(t, body, CodeForbidden)
			}
		})
	}

	t.Run("admin scope bypasses the table", func(t *testing.T) {
		created, _ := mustCreateScopedKey(t, ts, map[string]any{
			"userId": "u-01",
			"label":  "admin key",
			"scopes": []string{"admin"},
		})
		for _, route := range routes[:4] {
			resp, _ := scopedCall(t, ts, created.Secret, route.method, route.path, nil)
			requireStatus(t, resp, http.StatusOK)
		}
	})

	// A neighbouring read scope must not leak onto the platform routes.
	t.Run("catalog:read stays on the catalog routes", func(t *testing.T) {
		created, _ := mustCreateScopedKey(t, ts, map[string]any{
			"userId":     "u-01",
			"label":      "catalog reader",
			"scopes":     []string{"catalog:read"},
			"accountIds": []string{"acc-01"},
		})
		for _, route := range routes {
			resp, body := scopedCall(t, ts, created.Secret, route.method, route.path, nil)
			requireStatus(t, resp, http.StatusForbidden)
			requireErrorCode(t, body, CodeForbidden)
		}
		resp, _ := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/nodes", nil)
		requireStatus(t, resp, http.StatusOK)
	})
}

// TestScopedPlatformReadsRespectAccountRestriction pins the least-privilege
// half of the new scopes: a key restricted to acc-01 reads its own renewal
// state and health rows, and is refused everywhere else. Usage is per-user
// (AJS-451): the key reads its own rows, and an account filter is 400 because
// the log has no stable account id.
func TestScopedPlatformReadsRespectAccountRestriction(t *testing.T) {
	ts, _, sampler := newScopedPlatformServer(t)
	for id, port := range map[string]int{"node-01": 13000, "node-04": 13003} {
		if err := sampler.Watch(id, port); err != nil {
			t.Fatalf("watch %s: %v", id, err)
		}
	}
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     "u-01",
		"label":      "northwind only",
		"scopes":     []string{"usage:read", "renewal:read", "health:read"},
		"accountIds": []string{"acc-01"},
	})

	// Renewal: own account 200, another account 403, unknown id keeps 404.
	resp, _ := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/renewal/accounts/acc-01", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/renewal/accounts/acc-02", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	resp, _ = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/renewal/accounts/acc-unknown", nil)
	requireStatus(t, resp, http.StatusNotFound)

	// Usage: the key is served its own rows only. An account filter is
	// INVALID_ARGUMENT for every value, because the log stores a recyclable
	// label rather than a stable account id.
	resp, _ = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/usage", nil)
	requireStatus(t, resp, http.StatusOK)
	for _, path := range []string{
		"/v1/usage?account=acc-01",
		"/v1/usage?account=acc-02",
		"/v1/usage?account=Field+Lab",
		"/v1/usage?account=nobody",
	} {
		resp, body = scopedCall(t, ts, created.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
	}

	// Health: only the visible account's watched node is reported.
	resp, body = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/health/nodes", nil)
	requireStatus(t, resp, http.StatusOK)
	var healthPayload struct {
		Enabled bool             `json:"enabled"`
		Nodes   []healthNodeView `json:"nodes"`
	}
	if err := json.Unmarshal(body, &healthPayload); err != nil {
		t.Fatalf("decode health body %q: %v", body, err)
	}
	if len(healthPayload.Nodes) != 1 || healthPayload.Nodes[0].NodeID != "node-01" {
		t.Fatalf("restricted health nodes = %+v, want only node-01", healthPayload.Nodes)
	}
}

// TestScopedKeyLabIsAdminOnly pins the AJS-452 boundary: /v1/lab/* is operator
// diagnostics, not a customer surface. A key carrying every remaining
// non-admin scope is refused 403 scope_required on every lab route, the
// removed lab:read grant cannot be issued any more, and the admin scope keeps
// the operator view.
func TestScopedKeyLabIsAdminOnly(t *testing.T) {
	ts, _ := newTestServer(t)

	key, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "everything but admin",
		"scopes": []string{
			"accounts:read", "accounts:write", "catalog:read", "catalog:write",
			"leases:read", "leases:write", "proxy:connect",
			"requests:read", "audit:read", "metrics:read",
			"settings:read", "settings:write", "usage:read", "renewal:read",
			"health:read", "events:read",
		},
		"accountIds": []string{"acc-01"},
	})

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/lab/overview"},
		{http.MethodGet, "/v1/lab/flags"},
		{http.MethodGet, "/v1/lab/gates"},
		{http.MethodGet, "/v1/lab/flows"},
		{http.MethodGet, "/v1/lab/sessions"},
		{http.MethodGet, "/v1/lab/egress"},
		{http.MethodPost, "/v1/lab/egress-check"},
		{http.MethodGet, "/v1/lab/profiles"},
		{http.MethodGet, "/v1/lab/logs"},
		{http.MethodGet, "/v1/lab/diagnostics"},
	}
	for _, route := range routes {
		resp, body := scopedCall(t, ts, key.Secret, route.method, route.path, nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
		requireErrorReason(t, body, ReasonScopeRequired)
	}

	// lab:read is no longer part of the vocabulary, so it cannot be granted.
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", map[string]any{
		"userId": "u-01", "label": "lab reader", "scopes": []string{"lab:read"},
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	// The admin grant still reaches the same routes.
	adminKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01", "label": "operator", "scopes": []string{"admin"},
	})
	resp, body = scopedCall(t, ts, adminKey.Secret, http.MethodGet, "/v1/lab/overview", nil)
	requireStatus(t, resp, http.StatusOK)
	if !strings.Contains(string(body), `"enabled":false`) {
		t.Fatalf("admin lab overview = %s, want the disabled-but-served body", body)
	}
}
