package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// fakeCatalog records the guarded-connect calls so the handlers can be tested
// without a node controller or a vendor host.
type fakeCatalog struct {
	mu            sync.Mutex
	snapshotID    string
	lookup        runtime.ServerLookup
	connect       runtime.SwitchResult
	connectErr    error
	disconnect    runtime.DisconnectResult
	disconnectErr error
	reconnect     runtime.SwitchResult
	reconnectErr  error

	lookupCalls  []string
	connectCalls []string
}

func (f *fakeCatalog) SnapshotID() string { return f.snapshotID }

func (f *fakeCatalog) LookupServer(accountID, serverID string) runtime.ServerLookup {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookupCalls = append(f.lookupCalls, accountID+":"+serverID)
	return f.lookup
}

func (f *fakeCatalog) ConnectServer(_ context.Context, accountID, serverID, snapshotID, trigger string) (runtime.SwitchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls = append(f.connectCalls, accountID+"|"+serverID+"|"+snapshotID+"|"+trigger)
	return f.connect, f.connectErr
}

func (f *fakeCatalog) ReconnectAccount(context.Context, string, string, string) (runtime.SwitchResult, error) {
	return f.reconnect, f.reconnectErr
}

func (f *fakeCatalog) DisconnectAccount(context.Context, string) (runtime.DisconnectResult, error) {
	return f.disconnect, f.disconnectErr
}

func newCatalogTestServer(t *testing.T, connector CatalogConnector) (*httptest.Server, *store.Store) {
	t.Helper()
	st := store.New()
	server := New(Config{AdminToken: testToken, Catalog: connector}, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, st
}

func decodeCatalogError(t *testing.T, body []byte) errorResponse {
	t.Helper()
	var payload errorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return payload
}

func TestCatalogSelectionEndpoints(t *testing.T) {
	connector := &fakeCatalog{
		snapshotID: "cat-1",
		lookup: runtime.ServerLookup{
			Title: "Shanghai 01", Found: true, Eligible: true, LoginAvailable: true,
		},
	}
	ts, st := newCatalogTestServer(t, connector)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/accounts/acc-01/selection", nil)
	requireStatus(t, resp, http.StatusNotFound)
	if payload := decodeCatalogError(t, body); payload.Error.Reason != runtime.ReasonNoTarget {
		t.Fatalf("reason = %q, want no_target", payload.Error.Reason)
	}

	resp, body = apiCall(t, ts, http.MethodPut, "/v1/accounts/acc-01/selection", map[string]any{
		"serverId": "srv-a", "snapshotId": "cat-1",
	})
	requireStatus(t, resp, http.StatusOK)
	selection := decodeBody[model.CatalogSelection](t, body)
	if selection.AccountID != "acc-01" || selection.ServerID != "srv-a" || selection.Title != "Shanghai 01" {
		t.Fatalf("selection = %+v", selection)
	}
	if stored, ok := st.Selection("acc-01"); !ok || stored.ServerID != "srv-a" {
		t.Fatalf("stored selection = %+v/%v", stored, ok)
	}
	if event := st.Audit(1000)[0]; event.Action != "catalog.select" || event.Meta["serverId"] != "srv-a" {
		t.Fatalf("audit = %+v, want catalog.select", event)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/accounts/acc-01/selection", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.CatalogSelection](t, body); got.ServerID != "srv-a" || got.Title != "Shanghai 01" {
		t.Fatalf("GET selection = %+v", got)
	}

	resp, body = apiCall(t, ts, http.MethodPut, "/v1/accounts/acc-01/selection", map[string]any{
		"serverId": "srv-a", "snapshotId": "cat-stale",
	})
	requireStatus(t, resp, http.StatusConflict)
	payload := decodeCatalogError(t, body)
	if payload.Error.Reason != runtime.ReasonSnapshotChanged || payload.Error.Details["snapshotId"] != "cat-1" {
		t.Fatalf("snapshot error = %+v", payload.Error)
	}

	connector.lookup = runtime.ServerLookup{LoginAvailable: true}
	resp, body = apiCall(t, ts, http.MethodPut, "/v1/accounts/acc-01/selection", map[string]any{"serverId": "gone"})
	requireStatus(t, resp, http.StatusNotFound)
	if payload := decodeCatalogError(t, body); payload.Error.Reason != runtime.ReasonUnknownServer {
		t.Fatalf("reason = %q, want unknown_server", payload.Error.Reason)
	}

	connector.lookup = runtime.ServerLookup{Found: true, LoginAvailable: true, IneligibleReason: "full_load"}
	resp, body = apiCall(t, ts, http.MethodPut, "/v1/accounts/acc-01/selection", map[string]any{"serverId": "busy"})
	requireStatus(t, resp, http.StatusConflict)
	payload = decodeCatalogError(t, body)
	if payload.Error.Reason != runtime.ReasonServerIneligible || payload.Error.Details["ineligibleReason"] != "full_load" {
		t.Fatalf("ineligible error = %+v", payload.Error)
	}

	connector.lookup = runtime.ServerLookup{}
	resp, body = apiCall(t, ts, http.MethodPut, "/v1/accounts/acc-01/selection", map[string]any{"serverId": "srv-a"})
	requireStatus(t, resp, http.StatusConflict)
	if payload := decodeCatalogError(t, body); payload.Error.Reason != runtime.ReasonLoginRequired {
		t.Fatalf("reason = %q, want login_required", payload.Error.Reason)
	}

	resp, body = apiCall(t, ts, http.MethodPut, "/v1/accounts/acc-01/selection", map[string]any{"snapshotId": "cat-1"})
	requireStatus(t, resp, http.StatusBadRequest)
	if payload := decodeCatalogError(t, body); payload.Error.Code != CodeInvalidArgument {
		t.Fatalf("code = %q, want INVALID_ARGUMENT", payload.Error.Code)
	}

	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/accounts/acc-01/selection", nil)
	requireStatus(t, resp, http.StatusNoContent)
	if _, ok := st.Selection("acc-01"); ok {
		t.Fatalf("DELETE kept the selection")
	}
	if event := st.Audit(1000)[0]; event.Action != "catalog.unselect" || event.Meta["previousServerId"] != "srv-a" {
		t.Fatalf("audit = %+v, want catalog.unselect", event)
	}
	audits := len(st.Audit(1000))
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/accounts/acc-01/selection", nil)
	requireStatus(t, resp, http.StatusNoContent)
	if got := len(st.Audit(1000)); got != audits {
		t.Fatalf("idempotent DELETE wrote an audit")
	}

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		resp, _ = apiCall(t, ts, method, "/v1/accounts/nope/selection", nil)
		requireStatus(t, resp, http.StatusNotFound)
	}

	// A daemon without AJS_ACCOUNT_LOGIN has no connector: selection writes
	// answer login_disabled instead of pretending.
	disabled, _ := newCatalogTestServer(t, nil)
	resp, body = apiCall(t, disabled, http.MethodPut, "/v1/accounts/acc-01/selection", map[string]any{"serverId": "srv-a"})
	requireStatus(t, resp, http.StatusConflict)
	if payload := decodeCatalogError(t, body); payload.Error.Reason != runtime.ReasonLoginDisabled {
		t.Fatalf("reason = %q, want login_disabled", payload.Error.Reason)
	}
}

func TestCatalogConnectEndpoints(t *testing.T) {
	connector := &fakeCatalog{
		connect: runtime.SwitchResult{
			AccountID: "acc-01", ServerID: "srv-a", NodeID: "acc-01:srv-a",
			NodeState: model.NodeLive, GatePort: 13002, EgressIP: "198.51.100.7",
			SwitchedFrom: "srv-old", DurationMs: 1840, Warnings: []string{}, AuditID: "audit-9",
		},
	}
	ts, _ := newCatalogTestServer(t, connector)

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/connect", map[string]any{"serverId": "srv-a"})
	requireStatus(t, resp, http.StatusBadRequest)
	if payload := decodeCatalogError(t, body); payload.Error.Code != CodeInvalidArgument {
		t.Fatalf("code = %q, want INVALID_ARGUMENT", payload.Error.Code)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/connect", map[string]any{
		"serverId": "srv-a", "snapshotId": "cat-1", "confirm": true,
	})
	requireStatus(t, resp, http.StatusOK)
	result := decodeBody[runtime.SwitchResult](t, body)
	if result.NodeID != "acc-01:srv-a" || result.GatePort != 13002 || result.AuditID != "audit-9" {
		t.Fatalf("result = %+v", result)
	}
	if len(connector.connectCalls) != 1 || connector.connectCalls[0] != "acc-01|srv-a|cat-1|manual" {
		t.Fatalf("connect calls = %v", connector.connectCalls)
	}

	cases := []struct {
		name       string
		err        error
		status     int
		code       string
		reason     string
		retryAfter string
	}{
		{name: "in progress", err: &runtime.SwitchError{Reason: runtime.ReasonSwitchInProgress, Message: "busy"}, status: http.StatusConflict, code: CodeConflict, reason: runtime.ReasonSwitchInProgress},
		{name: "ineligible", err: &runtime.SwitchError{Reason: runtime.ReasonServerIneligible, Message: "no", Details: map[string]string{"ineligibleReason": "full_load"}}, status: http.StatusConflict, code: CodeConflict, reason: runtime.ReasonServerIneligible},
		{name: "cooldown", err: &runtime.SwitchError{Reason: runtime.ReasonConnectCooldown, Message: "wait", RetryAfter: 3200 * time.Millisecond}, status: http.StatusTooManyRequests, code: CodeRateLimited, reason: runtime.ReasonConnectCooldown, retryAfter: "4"},
		{name: "open failed", err: &runtime.SwitchError{Reason: runtime.ReasonSessionOpen, Message: "open", Details: map[string]string{"rollback": "restored"}}, status: http.StatusBadGateway, code: CodeUpstreamUnavailable, reason: runtime.ReasonSessionOpen},
		{name: "store failed", err: &runtime.SwitchError{Reason: runtime.ReasonStoreFailed, Message: "store"}, status: http.StatusInternalServerError, code: CodeInternal, reason: runtime.ReasonStoreFailed},
		{name: "unknown server", err: &runtime.SwitchError{Reason: runtime.ReasonUnknownServer, Message: "unknown"}, status: http.StatusNotFound, code: CodeNotFound, reason: runtime.ReasonUnknownServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			connector.connectErr = tc.err
			resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/connect", map[string]any{
				"serverId": "srv-a", "confirm": true,
			})
			requireStatus(t, resp, tc.status)
			payload := decodeCatalogError(t, body)
			if payload.Error.Code != tc.code || payload.Error.Reason != tc.reason {
				t.Fatalf("error = %+v, want %s/%s", payload.Error, tc.code, tc.reason)
			}
			if tc.retryAfter != "" && resp.Header.Get("Retry-After") != tc.retryAfter {
				t.Fatalf("Retry-After = %q, want %q", resp.Header.Get("Retry-After"), tc.retryAfter)
			}
		})
	}

	connector.connectErr = nil
	connector.disconnect = runtime.DisconnectResult{
		AccountID: "acc-01", ServerID: "srv-a", NodeID: "acc-01:srv-a", AuditID: "audit-10",
		Warnings: []string{},
	}
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/disconnect", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[runtime.DisconnectResult](t, body); got.NodeID != "acc-01:srv-a" {
		t.Fatalf("disconnect = %+v", got)
	}

	connector.disconnect = runtime.DisconnectResult{AccountID: "acc-01", AlreadyDisconnected: true, Warnings: []string{}}
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/disconnect", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[runtime.DisconnectResult](t, body); !got.AlreadyDisconnected {
		t.Fatalf("disconnect = %+v, want alreadyDisconnected", got)
	}

	connector.reconnect = runtime.SwitchResult{
		AccountID: "acc-01", ServerID: "srv-a", NodeID: "acc-01:srv-a", NodeState: model.NodeLive,
		GatePort: 13002, Warnings: []string{}, AuditID: "audit-11",
	}
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/reconnect", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[runtime.SwitchResult](t, body); got.AuditID != "audit-11" {
		t.Fatalf("reconnect = %+v", got)
	}

	connector.reconnectErr = &runtime.SwitchError{Reason: runtime.ReasonNoTarget, Message: "none"}
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/reconnect", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNotFound)
	if payload := decodeCatalogError(t, body); payload.Error.Reason != runtime.ReasonNoTarget {
		t.Fatalf("reason = %q, want no_target", payload.Error.Reason)
	}

	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/accounts/nope/actions/connect", map[string]any{
		"serverId": "srv-a", "confirm": true,
	})
	requireStatus(t, resp, http.StatusNotFound)

	disabled, _ := newCatalogTestServer(t, nil)
	resp, body = apiCall(t, disabled, http.MethodPost, "/v1/accounts/acc-01/actions/connect", map[string]any{
		"serverId": "srv-a", "confirm": true,
	})
	requireStatus(t, resp, http.StatusConflict)
	if payload := decodeCatalogError(t, body); payload.Error.Reason != runtime.ReasonLoginDisabled {
		t.Fatalf("reason = %q, want login_disabled", payload.Error.Reason)
	}
}
