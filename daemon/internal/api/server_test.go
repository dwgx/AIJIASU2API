package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

const testToken = "local-test-token"

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st := store.New()
	server := New(Config{AdminToken: testToken}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, st
}

func newRequest(t *testing.T, method, url string, body any) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func doRequest(t *testing.T, ts *httptest.Server, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		t.Fatalf("read response: %v", err)
	}
	resp.Body.Close()
	return resp, data
}

func apiCall(t *testing.T, ts *httptest.Server, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	req := newRequest(t, method, ts.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+testToken)
	return doRequest(t, ts, req)
}

func requireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status = %d, want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want)
	}
}

func requireErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var payload errorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	if payload.Error.Code != want {
		t.Fatalf("error code = %q, want %q (body %s)", payload.Error.Code, want, body)
	}
	if payload.Error.Message == "" {
		t.Fatalf("error message is empty (body %s)", body)
	}
}

func decodeBody[T any](t *testing.T, body []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode body %q: %v", body, err)
	}
	return out
}

func assertKeys(t *testing.T, label string, object map[string]json.RawMessage, want ...string) {
	t.Helper()
	if len(object) != len(want) {
		t.Fatalf("%s has %d fields, want %d (%v)", label, len(object), len(want), keysOf(object))
	}
	for _, key := range want {
		if _, ok := object[key]; !ok {
			t.Errorf("%s is missing field %q (has %v)", label, key, keysOf(object))
		}
	}
}

func keysOf(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}

func firstObject(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("list is empty")
	}
	return rows[0]
}

func TestHealthzAndAuth(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/healthz", nil))
	requireStatus(t, resp, http.StatusOK)
	health := decodeBody[healthResponse](t, body)
	if health.Status != "ok" || health.Service != "ajs-accountd" {
		t.Fatalf("unexpected health payload: %+v", health)
	}
	if health.Accounts["active"] != 4 || health.Accounts["paused"] != 1 || health.Accounts["auth_failed"] != 1 {
		t.Fatalf("unexpected account summary: %+v", health.Accounts)
	}
	if _, err := time.Parse(time.RFC3339, health.Time); err != nil {
		t.Fatalf("health time is not RFC3339: %v", err)
	}

	resp, body = doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/v1/accounts", nil))
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)

	req := newRequest(t, http.MethodGet, ts.URL+"/v1/accounts", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)

	resp, _ = apiCall(t, ts, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
}

func TestInsecureModeSkipsAuth(t *testing.T) {
	st := store.New()
	server := New(Config{AllowInsecure: true}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, _ := doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/v1/accounts", nil))
	requireStatus(t, resp, http.StatusOK)
}

func TestSeedCounts(t *testing.T) {
	ts, _ := newTestServer(t)
	cases := []struct {
		path string
		want int
	}{
		{"/v1/accounts", 6},
		{"/v1/nodes", 12},
		{"/v1/leases", 8},
		{"/v1/users", 4},
		{"/v1/apikeys", 6},
		{"/v1/requests", 40},
		{"/v1/audit?limit=100", 20},
	}
	for _, tc := range cases {
		resp, body := apiCall(t, ts, http.MethodGet, tc.path, nil)
		requireStatus(t, resp, http.StatusOK)
		var rows []json.RawMessage
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("%s: decode list: %v", tc.path, err)
		}
		if len(rows) != tc.want {
			t.Fatalf("%s: %d rows, want %d", tc.path, len(rows), tc.want)
		}
	}
}

func TestJSONFieldNames(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	assertKeys(t, "Account", firstObject(t, body),
		"id", "label", "username", "platform", "state", "concurrency", "regions",
		"nodeCount", "liveNodeCount", "usageBytes", "expiresAt", "lastLoginAt", "createdAt")

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/nodes", nil)
	requireStatus(t, resp, http.StatusOK)
	assertKeys(t, "ProxyNode", firstObject(t, body),
		"id", "accountId", "accountLabel", "label", "region", "country", "city", "state",
		"latencyMs", "successRate", "inUse", "capacity", "egressIp", "protocol", "gatePort", "lastProbeAt")

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/leases", nil)
	requireStatus(t, resp, http.StatusOK)
	assertKeys(t, "Lease", firstObject(t, body),
		"id", "userId", "userName", "accountId", "accountLabel", "nodeId", "nodeLabel",
		"region", "egressIp", "protocol", "gatePort", "ttlSeconds", "expiresAt", "requests",
		"bytes", "sticky", "createdAt")

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/users", nil)
	requireStatus(t, resp, http.StatusOK)
	assertKeys(t, "PanelUser", firstObject(t, body),
		"id", "name", "platform", "stickyTtl", "quotaBytesMonth", "usedBytes", "enabled", "allowlist", "createdAt")

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/apikeys", nil)
	requireStatus(t, resp, http.StatusOK)
	assertKeys(t, "ApiKey", firstObject(t, body),
		"id", "userId", "userName", "label", "prefix", "createdAt", "lastUsedAt", "revokedAt")

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/requests", nil)
	requireStatus(t, resp, http.StatusOK)
	assertKeys(t, "RequestLog", firstObject(t, body),
		"id", "ts", "userId", "userName", "accountLabel", "nodeLabel", "region", "method",
		"target", "status", "bytes", "latencyMs", "source")

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/audit?limit=1", nil)
	requireStatus(t, resp, http.StatusOK)
	assertKeys(t, "AuditEvent", firstObject(t, body),
		"id", "ts", "severity", "actor", "action", "target", "detail")
}

func TestSeedValues(t *testing.T) {
	ts, _ := newTestServer(t)

	_, body := apiCall(t, ts, http.MethodGet, "/v1/accounts", nil)
	accounts := decodeBody[[]model.Account](t, body)
	if accounts[0].ID != "acc-01" || accounts[0].Label != "Northwind Ops" ||
		accounts[0].Platform != model.PlatformAndroid || accounts[0].State != model.AccountActive ||
		accounts[0].Concurrency != 200 || accounts[0].NodeCount != 3 || accounts[0].LiveNodeCount != 2 ||
		accounts[0].UsageBytes != 82_400_000_000 || len(accounts[0].Regions) != 3 {
		t.Fatalf("unexpected first account: %+v", accounts[0])
	}
	if accounts[5].State != model.AccountAuthFailed {
		t.Fatalf("last account state = %q, want auth_failed", accounts[5].State)
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/nodes", nil)
	nodes := decodeBody[[]model.ProxyNode](t, body)
	if nodes[0].ID != "node-01" || nodes[0].Label != "cn-zj-01" || nodes[0].GatePort != 13000 ||
		nodes[0].Protocol != model.ProtocolSOCKS5 || nodes[0].EgressIP != "203.0.113.11" {
		t.Fatalf("unexpected first node: %+v", nodes[0])
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/leases", nil)
	leases := decodeBody[[]model.Lease](t, body)
	if leases[0].ID != "lease-01" || leases[0].TTLSeconds != 28_800 || leases[0].Requests != 18_432 ||
		!leases[0].Sticky {
		t.Fatalf("unexpected first lease: %+v", leases[0])
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/users", nil)
	users := decodeBody[[]model.PanelUser](t, body)
	if users[0].QuotaBytesMonth == nil || *users[0].QuotaBytesMonth != 200_000_000_000 {
		t.Fatalf("unexpected first user quota: %+v", users[0])
	}
	if users[2].QuotaBytesMonth != nil {
		t.Fatalf("u-03 quota should be null, got %v", *users[2].QuotaBytesMonth)
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/requests", nil)
	requests := decodeBody[[]model.RequestLog](t, body)
	if requests[0].ID != "req-001" || requests[0].Source != model.SourceProxy {
		t.Fatalf("unexpected first request: %+v", requests[0])
	}
}

func TestAccountCreateConflictDelete(t *testing.T) {
	ts, _ := newTestServer(t)

	input := model.CreateAccountInput{
		Label:       "Test Account",
		Username:    "test-user",
		Password:    "local-placeholder",
		Platform:    model.PlatformLinux,
		Concurrency: 50,
		Regions:     []string{"hk"},
	}
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts", input)
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[model.Account](t, body)
	if created.ID != "acc-07" || created.State != model.AccountActive || created.NodeCount != 0 ||
		created.LiveNodeCount != 0 || len(created.Regions) != 1 || created.Regions[0] != "hk" {
		t.Fatalf("unexpected created account: %+v", created)
	}
	if _, err := time.Parse(time.RFC3339, created.ExpiresAt); err != nil {
		t.Fatalf("expiresAt is not RFC3339: %v", err)
	}

	input.Label = "test account"
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts", input)
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)

	input.Label = "   "
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts", input)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/accounts?state=active", nil)
	requireStatus(t, resp, http.StatusOK)
	active := decodeBody[[]model.Account](t, body)
	if len(active) != 5 {
		t.Fatalf("active accounts = %d, want 5", len(active))
	}

	resp, _ = apiCall(t, ts, http.MethodPatch, "/v1/accounts/acc-07", map[string]any{"state": "paused", "concurrency": 64})
	requireStatus(t, resp, http.StatusOK)

	resp, body = apiCall(t, ts, http.MethodDelete, "/v1/accounts/acc-07", nil)
	requireStatus(t, resp, http.StatusNoContent)
	if len(body) != 0 {
		t.Fatalf("DELETE body = %q, want empty", body)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/accounts/acc-07", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)

	_, body = apiCall(t, ts, http.MethodGet, "/v1/audit?limit=50", nil)
	events := decodeBody[[]model.AuditEvent](t, body)
	actions := make(map[string]bool, len(events))
	for _, event := range events {
		actions[event.Action] = true
	}
	if !actions["account.create"] || !actions["account.delete"] || !actions["account.update"] {
		t.Fatalf("missing audit actions: %v", actions)
	}
}

func TestAccountLoginLogout(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-06/actions/login", nil)
	requireStatus(t, resp, http.StatusOK)
	account := decodeBody[model.Account](t, body)
	if account.State != model.AccountActive {
		t.Fatalf("login state = %q, want active", account.State)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/logout", nil)
	requireStatus(t, resp, http.StatusOK)
	account = decodeBody[model.Account](t, body)
	if account.State != model.AccountPaused || account.LiveNodeCount != 0 || account.NodeCount != 3 {
		t.Fatalf("logout account = %+v, want paused with 0/3 live nodes", account)
	}
	_, body = apiCall(t, ts, http.MethodGet, "/v1/nodes?account_id=acc-01", nil)
	nodes := decodeBody[[]model.ProxyNode](t, body)
	if len(nodes) != 3 {
		t.Fatalf("account nodes after logout = %d, want 3", len(nodes))
	}
	for _, node := range nodes {
		if node.State != model.NodeDraining {
			t.Fatalf("node %s state = %q after logout, want DRAINING", node.ID, node.State)
		}
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-06/actions/logout", nil)
	requireStatus(t, resp, http.StatusOK)
	account = decodeBody[model.Account](t, body)
	if account.State != model.AccountPaused {
		t.Fatalf("logout state = %q, want paused", account.State)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/missing/actions/login", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

// stubLoginService is the api.LoginService seam without a vendor call.
type stubLoginService struct {
	account model.Account
	err     error
	lastID  string
	forgot  string
}

func (s *stubLoginService) Login(_ context.Context, accountID string) (model.Account, error) {
	s.lastID = accountID
	if s.err != nil {
		return model.Account{}, s.err
	}
	out := s.account
	if out.ID == "" {
		out.ID = accountID
	}
	return out, nil
}

func (s *stubLoginService) Forget(accountID string) { s.forgot = accountID }

func TestAccountLoginServiceStatusMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "success", status: http.StatusOK},
		{name: "missing secret", err: fmt.Errorf("wrapped: %w", account.ErrNoSecret), status: http.StatusConflict, code: CodeConflict},
		{name: "unknown account", err: fmt.Errorf("wrapped: %w", store.ErrNotFound), status: http.StatusNotFound, code: CodeNotFound},
		{name: "rejected", err: fmt.Errorf("wrapped: %w", account.ErrAuthRejected), status: http.StatusUnauthorized, code: CodeUnauthorized},
		{name: "upstream", err: fmt.Errorf("wrapped: %w", account.ErrUpstream), status: http.StatusServiceUnavailable, code: CodeUpstreamUnavailable},
		{name: "disabled", err: account.ErrDisabled, status: http.StatusServiceUnavailable, code: CodeUpstreamUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubLoginService{
				account: model.Account{ID: "acc-01", Label: "Northwind Ops", State: model.AccountActive},
				err:     tc.err,
			}
			server := New(Config{AdminToken: testToken, LoginService: svc}, store.New(),
				slog.New(slog.NewTextHandler(io.Discard, nil)))
			ts := httptest.NewServer(server)
			t.Cleanup(ts.Close)

			resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/login", nil)
			requireStatus(t, resp, tc.status)
			if svc.lastID != "acc-01" {
				t.Fatalf("Login account id = %q, want acc-01", svc.lastID)
			}
			if tc.code == "" {
				account := decodeBody[model.Account](t, body)
				if account.ID != "acc-01" || account.State != model.AccountActive {
					t.Fatalf("login body = %+v, want the service account", account)
				}
				return
			}
			requireErrorCode(t, body, tc.code)
		})
	}
}

func TestAccountLoginHook(t *testing.T) {
	var logs bytes.Buffer
	var hookID string
	hookErr := errors.New("node sync failed")
	server := New(Config{
		AdminToken: testToken,
		LoginService: &stubLoginService{
			account: model.Account{ID: "acc-01", Label: "Northwind Ops", State: model.AccountActive},
		},
		LoginHook: func(_ context.Context, accountID string) error {
			hookID = accountID
			return hookErr
		},
	}, store.New(), slog.New(slog.NewTextHandler(&logs, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/login", nil)
	requireStatus(t, resp, http.StatusOK)
	account := decodeBody[model.Account](t, body)
	if account.ID != "acc-01" || account.State != model.AccountActive {
		t.Fatalf("login body = %+v, want the successful login", account)
	}
	if hookID != "acc-01" {
		t.Fatalf("hook account = %q, want acc-01", hookID)
	}
	if !strings.Contains(logs.String(), "account_login_hook_error") {
		t.Fatalf("logs = %q, want the hook failure", logs.String())
	}
}

func TestAccountLoginHookSkippedOnFailure(t *testing.T) {
	called := false
	server := New(Config{
		AdminToken: testToken,
		LoginService: &stubLoginService{
			err: fmt.Errorf("wrapped: %w", account.ErrAuthRejected),
		},
		LoginHook: func(context.Context, string) error {
			called = true
			return nil
		},
	}, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, _ := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/login", nil)
	requireStatus(t, resp, http.StatusUnauthorized)
	if called {
		t.Fatal("hook ran after a failed login")
	}
}

func TestAccountDeleteHook(t *testing.T) {
	t.Run("hook error aborts the delete", func(t *testing.T) {
		var logs bytes.Buffer
		st := store.New()
		var hookID string
		var hookSawAccount bool
		hookErr := errors.New("stop gates failed")
		server := New(Config{
			AdminToken: testToken,
			AccountDeleteHook: func(_ context.Context, accountID string) error {
				hookID = accountID
				_, hookSawAccount = st.Account(accountID)
				return hookErr
			},
		}, st, slog.New(slog.NewTextHandler(&logs, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		nodesBefore := len(st.Nodes())
		resp, body := apiCall(t, ts, http.MethodDelete, "/v1/accounts/acc-01", nil)
		requireStatus(t, resp, http.StatusInternalServerError)
		requireErrorCode(t, body, CodeInternal)
		if hookID != "acc-01" {
			t.Fatalf("hook account = %q, want acc-01", hookID)
		}
		if !hookSawAccount {
			t.Fatal("hook ran after the account was already deleted")
		}
		if _, ok := st.Account("acc-01"); !ok {
			t.Fatal("a failed hook still deleted the account")
		}
		if got := len(st.Nodes()); got != nodesBefore {
			t.Fatalf("nodes after the failed delete = %d, want %d", got, nodesBefore)
		}
		if _, ok := st.Node("node-01"); !ok {
			t.Fatal("a failed hook still removed the account's nodes")
		}
		if !strings.Contains(logs.String(), "account_delete_hook_error") {
			t.Fatalf("logs = %q, want the hook failure", logs.String())
		}
	})

	t.Run("success deletes after the hook", func(t *testing.T) {
		st := store.New()
		var hookID string
		var hookSawAccount bool
		server := New(Config{
			AdminToken: testToken,
			AccountDeleteHook: func(_ context.Context, accountID string) error {
				hookID = accountID
				_, hookSawAccount = st.Account(accountID)
				return nil
			},
		}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodDelete, "/v1/accounts/acc-01", nil)
		requireStatus(t, resp, http.StatusNoContent)
		if len(body) != 0 {
			t.Fatalf("DELETE body = %q, want empty", body)
		}
		if hookID != "acc-01" || !hookSawAccount {
			t.Fatalf("hook = %q sawAccount=%v, want acc-01/true", hookID, hookSawAccount)
		}
		if _, ok := st.Account("acc-01"); ok {
			t.Fatal("account survived a successful delete")
		}
		for _, node := range st.Nodes() {
			if node.AccountID == "acc-01" {
				t.Fatalf("node %s survived the account delete", node.ID)
			}
		}
	})

	t.Run("nil hook keeps the store-only delete", func(t *testing.T) {
		ts, st := newTestServer(t)
		resp, body := apiCall(t, ts, http.MethodDelete, "/v1/accounts/acc-01", nil)
		requireStatus(t, resp, http.StatusNoContent)
		if len(body) != 0 {
			t.Fatalf("DELETE body = %q, want empty", body)
		}
		if _, ok := st.Account("acc-01"); ok {
			t.Fatal("account survived the store-only delete")
		}
	})

	t.Run("missing account skips the hook", func(t *testing.T) {
		called := false
		server := New(Config{
			AdminToken: testToken,
			AccountDeleteHook: func(context.Context, string) error {
				called = true
				return nil
			},
		}, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodDelete, "/v1/accounts/acc-missing", nil)
		requireStatus(t, resp, http.StatusNotFound)
		requireErrorCode(t, body, CodeNotFound)
		if called {
			t.Fatal("hook ran for a missing account")
		}
	})
}

// TestAccountLogoutHook pins the B0 logout seam at the API layer: the hook
// runs while the account is still active and its nodes are not yet draining,
// a hook error answers 500 without touching the store, and the nil hook keeps
// the store-only logout of a daemon without AJS_ACCOUNT_LOGIN=1.
func TestAccountLogoutHook(t *testing.T) {
	t.Run("hook runs before the store logout", func(t *testing.T) {
		st := store.New()
		var hookID string
		var stateAtHook model.AccountState
		var nodesAtHook []model.ProxyNode
		server := New(Config{
			AdminToken: testToken,
			AccountLogoutHook: func(_ context.Context, accountID string) error {
				hookID = accountID
				account, ok := st.Account(accountID)
				if !ok {
					t.Fatalf("hook ran for an account the store does not know")
				}
				stateAtHook = account.State
				for _, node := range st.Nodes() {
					if node.AccountID == accountID {
						nodesAtHook = append(nodesAtHook, node)
					}
				}
				return nil
			},
		}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/logout", nil)
		requireStatus(t, resp, http.StatusOK)
		account := decodeBody[model.Account](t, body)
		if hookID != "acc-01" {
			t.Fatalf("hook account = %q, want acc-01", hookID)
		}
		if stateAtHook != model.AccountActive {
			t.Fatalf("account state at hook = %q, want active (hook ran after the store logout)", stateAtHook)
		}
		if len(nodesAtHook) != 3 {
			t.Fatalf("account nodes at hook = %d, want 3", len(nodesAtHook))
		}
		for _, node := range nodesAtHook {
			if node.State == model.NodeDraining {
				t.Fatalf("node %s was already DRAINING when the hook ran", node.ID)
			}
		}
		if account.State != model.AccountPaused {
			t.Fatalf("logout state = %q, want paused", account.State)
		}
	})

	t.Run("hook error aborts the logout", func(t *testing.T) {
		var logs bytes.Buffer
		st := store.New()
		var hookID string
		hookErr := errors.New("stop gates failed")
		server := New(Config{
			AdminToken: testToken,
			AccountLogoutHook: func(_ context.Context, accountID string) error {
				hookID = accountID
				return hookErr
			},
		}, st, slog.New(slog.NewTextHandler(&logs, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		before := st.Nodes()
		resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/logout", nil)
		requireStatus(t, resp, http.StatusInternalServerError)
		requireErrorCode(t, body, CodeInternal)
		if hookID != "acc-01" {
			t.Fatalf("hook account = %q, want acc-01", hookID)
		}
		account, ok := st.Account("acc-01")
		if !ok {
			t.Fatal("a failed hook removed the account")
		}
		if account.State != model.AccountActive {
			t.Fatalf("account state after the failed hook = %q, want active", account.State)
		}
		nodes := st.Nodes()
		if len(nodes) != len(before) {
			t.Fatalf("nodes after the failed hook = %d, want %d", len(nodes), len(before))
		}
		for _, node := range nodes {
			if node.AccountID != "acc-01" {
				continue
			}
			if node.State == model.NodeDraining {
				t.Fatalf("node %s became DRAINING despite the hook failure", node.ID)
			}
		}
		if !strings.Contains(logs.String(), "account_logout_hook_error") {
			t.Fatalf("logs = %q, want the hook failure", logs.String())
		}
	})

	t.Run("nil hook keeps the store-only logout", func(t *testing.T) {
		ts, st := newTestServer(t)
		resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/logout", nil)
		requireStatus(t, resp, http.StatusOK)
		account := decodeBody[model.Account](t, body)
		if account.State != model.AccountPaused || account.LiveNodeCount != 0 {
			t.Fatalf("logout account = %+v, want paused with 0 live nodes", account)
		}
		for _, node := range st.Nodes() {
			if node.AccountID == "acc-01" && node.State != model.NodeDraining {
				t.Fatalf("node %s state = %q, want DRAINING", node.ID, node.State)
			}
		}
	})

	t.Run("missing account skips the hook", func(t *testing.T) {
		called := false
		server := New(Config{
			AdminToken: testToken,
			AccountLogoutHook: func(context.Context, string) error {
				called = true
				return nil
			},
		}, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-missing/actions/logout", nil)
		requireStatus(t, resp, http.StatusNotFound)
		requireErrorCode(t, body, CodeNotFound)
		if called {
			t.Fatal("hook ran for a missing account")
		}
	})
}

// drainPanel records whether the API hook had already run when a panel
// mutation started. Only the two node mutations are implemented: any other
// call panics on the embedded nil interface, which is the desired test failure.
type drainPanel struct {
	runtime.Panel
	hookRan        bool
	hookRanDrain   bool
	hookRanUndrain bool
	drainCalls     int
	undrainCalls   int
}

func (p *drainPanel) DrainNode(_ context.Context, nodeID string) (model.ProxyNode, error) {
	p.drainCalls++
	p.hookRanDrain = p.hookRan
	return model.ProxyNode{ID: nodeID, State: model.NodeDraining}, nil
}

func (p *drainPanel) UndrainNode(_ context.Context, nodeID string) (model.ProxyNode, error) {
	p.undrainCalls++
	p.hookRanUndrain = p.hookRan
	return model.ProxyNode{ID: nodeID, State: model.NodeLive}, nil
}

func TestNodeDrainHook(t *testing.T) {
	t.Run("hook runs before the store and drain succeeds", func(t *testing.T) {
		st := store.New()
		var hookNode string
		var hookDrain bool
		var stateAtHook model.NodeState
		server := New(Config{
			AdminToken: testToken,
			NodeDrainHook: func(_ context.Context, nodeID string, drain bool) error {
				hookNode = nodeID
				hookDrain = drain
				if node, ok := st.Node(nodeID); ok {
					stateAtHook = node.State
				}
				return nil
			},
		}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-01/actions/drain", nil)
		requireStatus(t, resp, http.StatusOK)
		node := decodeBody[model.ProxyNode](t, body)
		if node.State != model.NodeDraining {
			t.Fatalf("drained state = %q, want DRAINING", node.State)
		}
		if hookNode != "node-01" || !hookDrain {
			t.Fatalf("hook = %q drain=%v, want node-01/true", hookNode, hookDrain)
		}
		if stateAtHook != model.NodeLive {
			t.Fatalf("hook saw state %q, want LIVE before the store changed", stateAtHook)
		}
	})

	t.Run("hook runs before the store and undrain succeeds", func(t *testing.T) {
		st := store.New()
		var hookDrain bool
		var stateAtHook model.NodeState
		server := New(Config{
			AdminToken: testToken,
			NodeDrainHook: func(_ context.Context, nodeID string, drain bool) error {
				hookDrain = drain
				if node, ok := st.Node(nodeID); ok {
					stateAtHook = node.State
				}
				return nil
			},
		}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-08/actions/undrain", nil)
		requireStatus(t, resp, http.StatusOK)
		node := decodeBody[model.ProxyNode](t, body)
		if node.State != model.NodeLive {
			t.Fatalf("undrained state = %q, want LIVE", node.State)
		}
		if hookDrain {
			t.Fatal("undrain hook received drain=true")
		}
		if stateAtHook != model.NodeDraining {
			t.Fatalf("hook saw state %q, want DRAINING before the store changed", stateAtHook)
		}
	})

	t.Run("hook runs before the panel", func(t *testing.T) {
		st := store.New()
		panel := &drainPanel{}
		server := New(Config{
			AdminToken: testToken,
			Panel:      panel,
			NodeDrainHook: func(context.Context, string, bool) error {
				panel.hookRan = true
				return nil
			},
		}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-01/actions/drain", nil)
		requireStatus(t, resp, http.StatusOK)
		if node := decodeBody[model.ProxyNode](t, body); node.State != model.NodeDraining {
			t.Fatalf("panel drain response state = %q, want DRAINING", node.State)
		}
		if !panel.hookRanDrain {
			t.Fatal("panel.DrainNode ran before the node drain hook")
		}

		resp, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-08/actions/undrain", nil)
		requireStatus(t, resp, http.StatusOK)
		if node := decodeBody[model.ProxyNode](t, body); node.State != model.NodeLive {
			t.Fatalf("panel undrain response state = %q, want LIVE", node.State)
		}
		if !panel.hookRanUndrain {
			t.Fatal("panel.UndrainNode ran before the node drain hook")
		}
	})

	t.Run("drain hook error aborts before the panel and the store", func(t *testing.T) {
		var logs bytes.Buffer
		st := store.New()
		panel := &drainPanel{}
		server := New(Config{
			AdminToken: testToken,
			Panel:      panel,
			NodeDrainHook: func(context.Context, string, bool) error {
				return errors.New("gate drain failed")
			},
		}, st, slog.New(slog.NewTextHandler(&logs, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-01/actions/drain", nil)
		requireStatus(t, resp, http.StatusInternalServerError)
		requireErrorCode(t, body, CodeInternal)
		if panel.drainCalls != 0 {
			t.Fatalf("panel.DrainNode calls = %d, want 0 after a hook failure", panel.drainCalls)
		}
		node, ok := st.Node("node-01")
		if !ok || node.State != model.NodeLive {
			t.Fatalf("node after the failed hook = %+v, want LIVE", node)
		}
		if !strings.Contains(logs.String(), "node_drain_hook_error") {
			t.Fatalf("logs = %q, want node_drain_hook_error", logs.String())
		}
	})

	t.Run("store refusal from the hook keeps its status", func(t *testing.T) {
		st := store.New()
		server := New(Config{
			AdminToken: testToken,
			NodeDrainHook: func(context.Context, string, bool) error {
				// The wired controller forwards the store's refusal, so a DOWN
				// node keeps the 409 contract instead of becoming a 500.
				_, err := st.DrainNode("node-12")
				return err
			},
		}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-12/actions/drain", nil)
		requireStatus(t, resp, http.StatusConflict)
		requireErrorCode(t, body, CodeConflict)
		if node, _ := st.Node("node-12"); node.State != model.NodeDown {
			t.Fatalf("node state = %q, want DOWN", node.State)
		}
	})

	t.Run("undrain hook error aborts before the panel and the store", func(t *testing.T) {
		var logs bytes.Buffer
		st := store.New()
		panel := &drainPanel{}
		server := New(Config{
			AdminToken: testToken,
			Panel:      panel,
			NodeDrainHook: func(context.Context, string, bool) error {
				return errors.New("gate undrain failed")
			},
		}, st, slog.New(slog.NewTextHandler(&logs, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-08/actions/undrain", nil)
		requireStatus(t, resp, http.StatusInternalServerError)
		requireErrorCode(t, body, CodeInternal)
		if panel.undrainCalls != 0 {
			t.Fatalf("panel.UndrainNode calls = %d, want 0 after a hook failure", panel.undrainCalls)
		}
		node, ok := st.Node("node-08")
		if !ok || node.State != model.NodeDraining {
			t.Fatalf("node after the failed hook = %+v, want DRAINING", node)
		}
		if !strings.Contains(logs.String(), "node_drain_hook_error") {
			t.Fatalf("logs = %q, want node_drain_hook_error", logs.String())
		}
	})

	t.Run("missing node skips the hook and the panel", func(t *testing.T) {
		called := false
		panel := &drainPanel{}
		server := New(Config{
			AdminToken: testToken,
			Panel:      panel,
			NodeDrainHook: func(context.Context, string, bool) error {
				called = true
				return nil
			},
		}, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		for _, action := range []string{"drain", "undrain"} {
			resp, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/missing/actions/"+action, nil)
			requireStatus(t, resp, http.StatusNotFound)
			requireErrorCode(t, body, CodeNotFound)
		}
		if called {
			t.Fatal("hook ran for a missing node")
		}
		if panel.drainCalls != 0 || panel.undrainCalls != 0 {
			t.Fatalf("panel calls = %d/%d, want 0/0 for a missing node", panel.drainCalls, panel.undrainCalls)
		}
	})

	t.Run("nil hook keeps the store-only path", func(t *testing.T) {
		ts, st := newTestServer(t)

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-01/actions/drain", nil)
		requireStatus(t, resp, http.StatusOK)
		if node := decodeBody[model.ProxyNode](t, body); node.State != model.NodeDraining {
			t.Fatalf("drained state = %q, want DRAINING", node.State)
		}
		if node, _ := st.Node("node-01"); node.State != model.NodeDraining {
			t.Fatalf("store state = %q, want DRAINING", node.State)
		}

		resp, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-01/actions/undrain", nil)
		requireStatus(t, resp, http.StatusOK)
		if node := decodeBody[model.ProxyNode](t, body); node.State != model.NodeLive {
			t.Fatalf("undrained state = %q, want LIVE", node.State)
		}
	})
}

func TestNodeDrainUndrainProbe(t *testing.T) {
	ts, _ := newTestServer(t)

	_, body := apiCall(t, ts, http.MethodGet, "/v1/nodes?state=DRAINING", nil)
	draining := decodeBody[[]model.ProxyNode](t, body)
	if len(draining) != 1 || draining[0].ID != "node-08" {
		t.Fatalf("unexpected DRAINING nodes: %+v", draining)
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/nodes", nil)
	nodes := decodeBody[[]model.ProxyNode](t, body)
	originalProbe := nodes[0].LastProbeAt
	originalLatency := nodes[0].LatencyMs

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-01/actions/drain", nil)
	requireStatus(t, resp, http.StatusOK)
	node := decodeBody[model.ProxyNode](t, body)
	if node.State != model.NodeDraining {
		t.Fatalf("drained state = %q, want DRAINING", node.State)
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/accounts", nil)
	accounts := decodeBody[[]model.Account](t, body)
	if accounts[0].LiveNodeCount != 1 || accounts[0].NodeCount != 3 {
		t.Fatalf("account counts after drain = %d/%d, want 1/3", accounts[0].LiveNodeCount, accounts[0].NodeCount)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-01/actions/undrain", nil)
	requireStatus(t, resp, http.StatusOK)
	node = decodeBody[model.ProxyNode](t, body)
	if node.State != model.NodeLive {
		t.Fatalf("undrained state = %q, want LIVE", node.State)
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/accounts", nil)
	accounts = decodeBody[[]model.Account](t, body)
	if accounts[0].LiveNodeCount != 2 {
		t.Fatalf("liveNodeCount after undrain = %d, want 2", accounts[0].LiveNodeCount)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-01/actions/probe", nil)
	requireStatus(t, resp, http.StatusOK)
	node = decodeBody[model.ProxyNode](t, body)
	if node.LastProbeAt == originalProbe {
		t.Fatalf("probe did not update lastProbeAt (%q)", originalProbe)
	}
	if node.LatencyMs != originalLatency {
		t.Fatalf("probe changed latency from %d to %d", originalLatency, node.LatencyMs)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-12/actions/probe", nil)
	requireStatus(t, resp, http.StatusServiceUnavailable)
	requireErrorCode(t, body, CodeUpstreamUnavailable)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-12/actions/drain", nil)
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/missing/actions/probe", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

func TestLeaseRotateRelease(t *testing.T) {
	ts, _ := newTestServer(t)

	_, body := apiCall(t, ts, http.MethodGet, "/v1/leases", nil)
	leases := decodeBody[[]model.Lease](t, body)
	original := leases[0]
	if original.EgressIP != "203.0.113.11" {
		t.Fatalf("unexpected seed egress IP %q", original.EgressIP)
	}

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/leases/lease-01/actions/rotate", nil)
	requireStatus(t, resp, http.StatusOK)
	rotated := decodeBody[model.Lease](t, body)
	if rotated.EgressIP != "203.0.113.24" {
		t.Fatalf("rotated egress IP = %q, want 203.0.113.24", rotated.EgressIP)
	}
	if rotated.ExpiresAt == original.ExpiresAt {
		t.Fatalf("rotate did not refresh expiresAt (%q)", rotated.ExpiresAt)
	}
	if rotated.TTLSeconds != original.TTLSeconds {
		t.Fatalf("rotate changed ttlSeconds from %d to %d", original.TTLSeconds, rotated.TTLSeconds)
	}
	if rotated.Requests != original.Requests+17 {
		t.Fatalf("rotate requests = %d, want %d", rotated.Requests, original.Requests+17)
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/nodes", nil)
	nodes := decodeBody[[]model.ProxyNode](t, body)
	if nodes[0].EgressIP != rotated.EgressIP {
		t.Fatalf("node egress IP = %q, want %q", nodes[0].EgressIP, rotated.EgressIP)
	}

	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/leases/lease-01", nil)
	requireStatus(t, resp, http.StatusNoContent)

	_, body = apiCall(t, ts, http.MethodGet, "/v1/leases", nil)
	remaining := decodeBody[[]model.Lease](t, body)
	if len(remaining) != 7 {
		t.Fatalf("leases after release = %d, want 7", len(remaining))
	}
	for _, lease := range remaining {
		if lease.ID == "lease-01" {
			t.Fatal("lease-01 still present after release")
		}
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/nodes", nil)
	nodes = decodeBody[[]model.ProxyNode](t, body)
	if nodes[0].InUse != 2 {
		t.Fatalf("node inUse after release = %d, want 2", nodes[0].InUse)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/leases/lease-01/actions/rotate", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

func TestUserSuspendResumeAllowlist(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/users/u-01/suspend", nil)
	requireStatus(t, resp, http.StatusOK)
	user := decodeBody[model.PanelUser](t, body)
	if user.Enabled {
		t.Fatal("suspend left enabled=true")
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/users/u-01/resume", nil)
	requireStatus(t, resp, http.StatusOK)
	user = decodeBody[model.PanelUser](t, body)
	if !user.Enabled {
		t.Fatal("resume left enabled=false")
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/users/u-01", map[string]any{
		"allowlist": []string{"10.0.0.0/8", "192.168.1.5"},
	})
	requireStatus(t, resp, http.StatusOK)
	user = decodeBody[model.PanelUser](t, body)
	if len(user.Allowlist) != 2 || user.Allowlist[0] != "10.0.0.0/8" || user.Allowlist[1] != "192.168.1.5" {
		t.Fatalf("allowlist = %v", user.Allowlist)
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/users/u-01", map[string]any{
		"allowlist": []string{"not-a-cidr"},
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/users/u-01/allowlist", map[string]any{
		"allowlist": []string{"127.0.0.1/32"},
	})
	requireStatus(t, resp, http.StatusOK)
	user = decodeBody[model.PanelUser](t, body)
	if len(user.Allowlist) != 1 || user.Allowlist[0] != "127.0.0.1/32" {
		t.Fatalf("allowlist = %v", user.Allowlist)
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/users/u-01", map[string]any{"quotaBytesMonth": nil})
	requireStatus(t, resp, http.StatusOK)
	user = decodeBody[model.PanelUser](t, body)
	if user.QuotaBytesMonth != nil {
		t.Fatalf("quota = %d, want null", *user.QuotaBytesMonth)
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/users/u-01", map[string]any{"quotaBytesMonth": 12_345})
	requireStatus(t, resp, http.StatusOK)
	user = decodeBody[model.PanelUser](t, body)
	if user.QuotaBytesMonth == nil || *user.QuotaBytesMonth != 12_345 {
		t.Fatalf("quota = %v, want 12345", user.QuotaBytesMonth)
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/audit?limit=50", nil)
	events := decodeBody[[]model.AuditEvent](t, body)
	actions := make(map[string]bool, len(events))
	for _, event := range events {
		actions[event.Action] = true
	}
	if !actions["user.suspend"] || !actions["user.resume"] || !actions["user.allowlist"] {
		t.Fatalf("missing user audit actions: %v", actions)
	}
}

func TestUserCreateDeleteCascade(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/users", map[string]any{
		"name":      "release.bot",
		"stickyTtl": "1h",
	})
	requireStatus(t, resp, http.StatusCreated)
	user := decodeBody[model.PanelUser](t, body)
	if user.ID != "u-05" || user.Platform != "release-bot" || !user.Enabled ||
		len(user.Allowlist) != 1 || user.Allowlist[0] != "127.0.0.1/32" {
		t.Fatalf("unexpected created user: %+v", user)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/users", map[string]any{"name": "release.bot"})
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/users", map[string]any{"name": "bad.ttl", "stickyTtl": "soon"})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/users/u-02", nil)
	requireStatus(t, resp, http.StatusNoContent)

	_, body = apiCall(t, ts, http.MethodGet, "/v1/leases", nil)
	leases := decodeBody[[]model.Lease](t, body)
	if len(leases) != 6 {
		t.Fatalf("leases after user delete = %d, want 6", len(leases))
	}
	for _, lease := range leases {
		if lease.UserID == "u-02" {
			t.Fatalf("lease for deleted user remains: %s", lease.ID)
		}
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/apikeys", nil)
	keys := decodeBody[[]model.APIKey](t, body)
	for _, key := range keys {
		if key.UserID == "u-02" && key.RevokedAt == nil {
			t.Fatalf("key %s for deleted user was not revoked", key.ID)
		}
	}

	resp, body = apiCall(t, ts, http.MethodDelete, "/v1/users/u-02", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

func TestKeyCreateShowOnceRevoke(t *testing.T) {
	ts, st := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/apikeys", map[string]any{
		"userId": "u-01",
		"label":  "Test key",
	})
	requireStatus(t, resp, http.StatusCreated)
	result := decodeBody[model.CreateKeyResult](t, body)
	if !strings.HasPrefix(result.Secret, "ajs_live_") {
		t.Fatalf("secret prefix = %q", result.Secret)
	}
	if result.Key.Prefix != result.Secret[:16] {
		t.Fatalf("key prefix = %q, want %q", result.Key.Prefix, result.Secret[:16])
	}
	if result.Key.ID != "key-07" || result.Key.UserID != "u-01" || result.Key.Label != "Test key" {
		t.Fatalf("unexpected key: %+v", result.Key)
	}
	if result.Key.LastUsedAt != nil || result.Key.RevokedAt != nil {
		t.Fatalf("new key timestamps must be null: %+v", result.Key)
	}

	hash, ok := st.SecretHash(result.Key.ID)
	if !ok || hash == "" || hash == result.Secret || strings.Contains(hash, result.Secret) {
		t.Fatalf("stored hash is not a hash: ok=%v hash=%q", ok, hash)
	}

	_, listBody := apiCall(t, ts, http.MethodGet, "/v1/apikeys", nil)
	if bytes.Contains(listBody, []byte(result.Secret)) {
		t.Fatal("list response contains the plaintext secret")
	}
	keys := decodeBody[[]model.APIKey](t, listBody)
	if len(keys) != 7 || keys[0].ID != "key-07" {
		t.Fatalf("keys after create: len=%d first=%+v", len(keys), keys[0])
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/apikeys", map[string]any{"userId": "u-01"})
	requireStatus(t, resp, http.StatusCreated)
	untitled := decodeBody[model.CreateKeyResult](t, body)
	if untitled.Key.Label != "Untitled key" {
		t.Fatalf("default label = %q", untitled.Key.Label)
	}

	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/apikeys/key-07", nil)
	requireStatus(t, resp, http.StatusNoContent)

	_, body = apiCall(t, ts, http.MethodGet, "/v1/apikeys", nil)
	keys = decodeBody[[]model.APIKey](t, body)
	var revoked *model.APIKey
	for i := range keys {
		if keys[i].ID == "key-07" {
			revoked = &keys[i]
		}
	}
	if revoked == nil || revoked.RevokedAt == nil {
		t.Fatalf("key-07 not revoked: %+v", revoked)
	}
	if _, err := time.Parse(time.RFC3339, *revoked.RevokedAt); err != nil {
		t.Fatalf("revokedAt is not RFC3339: %v", err)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/apikeys", map[string]any{"userId": "missing"})
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

func TestMutationAppendsLog(t *testing.T) {
	ts, _ := newTestServer(t)

	_, body := apiCall(t, ts, http.MethodGet, "/v1/requests", nil)
	before := decodeBody[[]model.RequestLog](t, body)
	if len(before) != 40 {
		t.Fatalf("seed requests = %d, want 40", len(before))
	}

	input := model.MutationLabInput{
		Method: "GET",
		Target: "api.example.com:443",
		NodeID: "node-01",
		Body:   `{"ping":true}`,
	}
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/requests/mutations", input)
	requireStatus(t, resp, http.StatusCreated)
	entry := decodeBody[model.RequestLog](t, body)
	if entry.ID != "req-mut-1" || entry.Source != model.SourceMutationLab || entry.Status != 200 {
		t.Fatalf("unexpected mutation row: %+v", entry)
	}
	if entry.Bytes != 1024+int64(len(input.Body)) {
		t.Fatalf("mutation bytes = %d, want %d", entry.Bytes, 1024+len(input.Body))
	}
	if entry.LatencyMs != 54 {
		t.Fatalf("mutation latency = %d, want 54", entry.LatencyMs)
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/requests", nil)
	after := decodeBody[[]model.RequestLog](t, body)
	if len(after) != 41 || after[0].ID != "req-mut-1" {
		t.Fatalf("requests after mutation: len=%d first=%+v", len(after), after[0])
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/audit?limit=50", nil)
	events := decodeBody[[]model.AuditEvent](t, body)
	found := false
	for _, event := range events {
		if event.Action == "request.mutation" {
			found = true
		}
	}
	if !found {
		t.Fatal("mutation did not append an audit event")
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/requests/mutations", map[string]any{
		"method": "GET", "target": "api.example.com:443", "nodeId": "missing", "body": "",
	})
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/requests/mutations", map[string]any{
		"method": "GET", "target": " ", "nodeId": "node-01", "body": "",
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
}

func TestSettingsPatchRoundtrip(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/settings", nil)
	requireStatus(t, resp, http.StatusOK)
	settings := decodeBody[model.PanelSettings](t, body)
	if settings.Transport != model.TransportHTTPUpgrade || settings.HealthIntervalSec != 60 ||
		settings.LeaseTTL != "8h" || settings.SubscriptionRefreshSec != 30 || settings.Theme != model.ThemeDark {
		t.Fatalf("unexpected defaults: %+v", settings)
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/settings", map[string]any{
		"healthIntervalSec": 90,
		"theme":             "light",
		"leaseTtl":          "7d",
	})
	requireStatus(t, resp, http.StatusOK)
	settings = decodeBody[model.PanelSettings](t, body)
	if settings.HealthIntervalSec != 90 || settings.Theme != model.ThemeLight || settings.LeaseTTL != "7d" {
		t.Fatalf("patch did not apply: %+v", settings)
	}

	_, body = apiCall(t, ts, http.MethodGet, "/v1/settings", nil)
	persisted := decodeBody[model.PanelSettings](t, body)
	if persisted != settings {
		t.Fatalf("settings not persisted: %+v vs %+v", persisted, settings)
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/settings", map[string]any{"bogus": 1})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/settings", map[string]any{"theme": "neon"})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/settings", map[string]any{"healthIntervalSec": 0})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	_, body = apiCall(t, ts, http.MethodGet, "/v1/audit?limit=50", nil)
	events := decodeBody[[]model.AuditEvent](t, body)
	found := false
	for _, event := range events {
		if event.Action == "settings.update" {
			found = true
		}
	}
	if !found {
		t.Fatal("settings patch did not append an audit event")
	}
}

func TestCORSPreflight(t *testing.T) {
	ts, _ := newTestServer(t)

	req := newRequest(t, http.MethodOptions, ts.URL+"/v1/accounts", nil)
	req.Header.Set("Origin", "http://127.0.0.1:18081")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	resp, body := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusNoContent)
	if len(body) != 0 {
		t.Fatalf("preflight body = %q", body)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:18081" {
		t.Fatalf("allow origin = %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "DELETE") || !strings.Contains(got, "PATCH") {
		t.Fatalf("allow methods = %q", got)
	}
	if got := strings.ToLower(resp.Header.Get("Access-Control-Allow-Headers")); !strings.Contains(got, "authorization") {
		t.Fatalf("allow headers = %q", got)
	}

	req = newRequest(t, http.MethodOptions, ts.URL+"/v1/accounts", nil)
	req.Header.Set("Origin", "http://evil.example")
	resp, _ = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusNoContent)
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin received allow-origin %q", got)
	}

	req = newRequest(t, http.MethodGet, ts.URL+"/healthz", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	resp, _ = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
		t.Fatalf("healthz allow origin = %q", got)
	}

	req = newRequest(t, http.MethodGet, ts.URL+"/v1/accounts", nil)
	req.Header.Set("Origin", "http://127.0.0.1:18081")
	resp, _ = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:18081" {
		t.Fatalf("401 allow origin = %q", got)
	}
}

func TestRoutingErrors(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/does-not-exist", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)

	resp, body = apiCall(t, ts, http.MethodPut, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusMethodNotAllowed)
	requireErrorCode(t, body, CodeMethodNotAllowed)
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "GET") || !strings.Contains(allow, "POST") {
		t.Fatalf("Allow = %q", allow)
	}

	resp, body = doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/nope", nil))
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/accounts/", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

func TestPagination(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/accounts?limit=2&offset=1", nil)
	requireStatus(t, resp, http.StatusOK)
	accounts := decodeBody[[]model.Account](t, body)
	if len(accounts) != 2 || accounts[0].ID != "acc-02" || accounts[1].ID != "acc-03" {
		t.Fatalf("pagination result: %+v", accounts)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/accounts?limit=-1", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/accounts?offset=99", nil)
	requireStatus(t, resp, http.StatusOK)
	empty := decodeBody[[]model.Account](t, body)
	if len(empty) != 0 || string(bytes.TrimSpace(body)) != "[]" {
		t.Fatalf("empty page = %q", body)
	}

	// offset+limit used to overflow int and panic the handler on a negative
	// slice bound; every list route must clamp instead.
	for _, path := range []string{
		"/v1/accounts?limit=9223372036854775807&offset=7",
		"/v1/nodes?limit=9223372036854775807&offset=7",
		"/v1/leases?limit=9223372036854775807&offset=7",
		"/v1/users?limit=9223372036854775807&offset=7",
		"/v1/apikeys?limit=9223372036854775807&offset=7",
		"/v1/requests?limit=9223372036854775807&offset=7",
	} {
		resp, body := apiCall(t, ts, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusOK)
		var page []json.RawMessage
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("%s: invalid page %q: %v", path, body, err)
		}
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/accounts?limit=9223372036854775807", nil)
	requireStatus(t, resp, http.StatusOK)
	all := decodeBody[[]model.Account](t, body)
	if len(all) == 0 {
		t.Fatalf("unbounded page = %q, want every account", body)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/audit?limit=0", nil)
	requireStatus(t, resp, http.StatusOK)
	events := decodeBody[[]model.AuditEvent](t, body)
	if len(events) != 0 {
		t.Fatalf("audit limit=0 returned %d events", len(events))
	}
}

func TestUnknownJSONFieldRejected(t *testing.T) {
	ts, st := newTestServer(t)
	beforeAccounts := len(st.Accounts())
	beforeUsers := len(st.Users())

	// A typo must not create the account with a default concurrency.
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts", map[string]any{
		"label":       "Typo Co",
		"username":    "typo",
		"password":    "pw-not-a-credential",
		"platform":    "android",
		"concurrency": 8,
		"regions":     []string{},
		"concurency":  8,
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
	if after := len(st.Accounts()); after != beforeAccounts {
		t.Fatalf("rejected body created an account (%d -> %d)", beforeAccounts, after)
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/users", map[string]any{
		"name":     "Typo User",
		"platform": "typo",
		"extra":    true,
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
	if after := len(st.Users()); after != beforeUsers {
		t.Fatalf("rejected body created a user (%d -> %d)", beforeUsers, after)
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/accounts/acc-01", map[string]any{
		"state": "paused",
		"stat":  "paused",
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
	account, ok := st.Account("acc-01")
	if !ok || account.State == model.AccountPaused {
		t.Fatalf("rejected patch mutated the account: %+v (ok=%v)", account, ok)
	}
}

func TestMetricsContract(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/metrics?range=24h", nil)
	requireStatus(t, resp, http.StatusOK)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode metrics object: %v", err)
	}
	assertKeys(t, "Metrics", object, "range", "series", "totals", "accountHealth", "telemetryAvailable", "generatedAt")
	if string(object["series"]) != "[]" {
		t.Fatalf("series without telemetry = %s, want []", object["series"])
	}
	if string(object["telemetryAvailable"]) != "false" {
		t.Fatalf("telemetryAvailable = %s, want false", object["telemetryAvailable"])
	}

	metrics := decodeBody[model.Metrics](t, body)
	if metrics.Range != model.Range24h || len(metrics.Series) != 0 {
		t.Fatalf("24h metrics: range=%q points=%d", metrics.Range, len(metrics.Series))
	}
	if metrics.TelemetryAvailable {
		t.Fatal("telemetryAvailable = true without a traffic meter")
	}
	if metrics.Totals != (model.MetricsTotals{}) {
		t.Fatalf("totals without telemetry = %+v, want zero", metrics.Totals)
	}
	if len(metrics.AccountHealth) != 6 || metrics.AccountHealth[0].AccountID != "acc-01" ||
		metrics.AccountHealth[0].Score != 100 {
		t.Fatalf("unexpected account health: %+v", metrics.AccountHealth)
	}
	if metrics.AccountHealth[4].Score != 40 || metrics.AccountHealth[5].Score != 0 {
		t.Fatalf("paused/auth_failed health = %d/%d, want 40/0",
			metrics.AccountHealth[4].Score, metrics.AccountHealth[5].Score)
	}
	if _, err := time.Parse(time.RFC3339, metrics.GeneratedAt); err != nil {
		t.Fatalf("generatedAt is not RFC3339: %v", err)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/metrics?range=7d", nil)
	requireStatus(t, resp, http.StatusOK)
	metrics = decodeBody[model.Metrics](t, body)
	if metrics.Range != model.Range7d || len(metrics.Series) != 0 || metrics.TelemetryAvailable {
		t.Fatalf("7d metrics: range=%q points=%d telemetry=%v",
			metrics.Range, len(metrics.Series), metrics.TelemetryAvailable)
	}
	if metrics.Totals != (model.MetricsTotals{}) {
		t.Fatalf("7d totals = %+v, want zero", metrics.Totals)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/metrics?range=30d", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/metrics", nil)
	requireStatus(t, resp, http.StatusOK)
	metrics = decodeBody[model.Metrics](t, body)
	if metrics.Range != model.Range24h {
		t.Fatalf("default range = %q, want 24h", metrics.Range)
	}
}

func TestTopologyContract(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/topology", nil)
	requireStatus(t, resp, http.StatusOK)
	topology := decodeBody[model.Topology](t, body)
	if topology.Subscription != "ajs-live" || len(topology.Nodes) != 12 {
		t.Fatalf("unexpected topology: %+v", topology)
	}
	if topology.Nodes["node-01"] != "cn-zj-01" || topology.Nodes["node-12"] != "hk-03" {
		t.Fatalf("unexpected node tags: %v", topology.Nodes)
	}
	if len(topology.Platforms) != 4 || topology.Platforms[0] != "ajs-ada" {
		t.Fatalf("unexpected platforms: %v", topology.Platforms)
	}
	if _, err := time.Parse(time.RFC3339, topology.GeneratedAt); err != nil {
		t.Fatalf("generatedAt is not RFC3339: %v", err)
	}
}

func TestBodyLimit(t *testing.T) {
	ts, _ := newTestServer(t)

	payload := map[string]any{
		"label":       strings.Repeat("x", (1<<20)+1),
		"username":    "oversized",
		"password":    "local-placeholder",
		"platform":    "linux",
		"concurrency": 1,
		"regions":     []string{},
	}
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts", payload)
	requireStatus(t, resp, http.StatusRequestEntityTooLarge)
	requireErrorCode(t, body, CodeInvalidArgument)
}

func TestLoggingRedactsAuthorization(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	server := New(Config{AdminToken: testToken}, store.New(), logger)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	req := newRequest(t, http.MethodGet, ts.URL+"/v1/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, _ := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusOK)

	if requestID := resp.Header.Get("X-Request-Id"); len(requestID) != 16 {
		t.Fatalf("X-Request-Id = %q, want 16 hex chars", requestID)
	}
	logged := logBuffer.String()
	if !strings.Contains(logged, "http_request") || !strings.Contains(logged, "request_id") {
		t.Fatalf("request was not logged: %q", logged)
	}
	if strings.Contains(logged, testToken) {
		t.Fatalf("log leaked the bearer token: %q", logged)
	}
}

func TestPrometheusMetricsAuth(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/metrics", nil))
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)

	req := newRequest(t, http.MethodGet, ts.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, body = doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)

	resp, body = apiCall(t, ts, http.MethodGet, "/metrics", nil)
	requireStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("content type = %q, want Prometheus text format", ct)
	}
	if len(body) == 0 {
		t.Fatal("/metrics body is empty")
	}

	// /healthz stays open while /metrics requires the bearer token.
	resp, _ = doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/healthz", nil))
	requireStatus(t, resp, http.StatusOK)
}

func TestPrometheusMetricsCounts(t *testing.T) {
	ts, st := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodGet, "/metrics", nil)
	requireStatus(t, resp, http.StatusOK)

	wantAccounts := map[string]int{}
	for _, account := range st.Accounts() {
		wantAccounts[string(account.State)]++
	}
	wantNodes := map[string]int{}
	for _, node := range st.Nodes() {
		wantNodes[string(node.State)]++
	}
	lines := strings.Split(string(body), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "" {
		t.Fatalf("body must end with a newline: %q", body)
	}
	var sawBuild, sawUptime bool
	for _, line := range lines {
		if strings.HasPrefix(line, "ajs_build_info{version=") {
			sawBuild = true
			if line != `ajs_build_info{version="0.1.0"} 1` {
				t.Fatalf("build info line = %q", line)
			}
		}
		if strings.HasPrefix(line, "ajs_uptime_seconds ") {
			sawUptime = true
			fields := strings.Fields(line)
			uptime, err := strconv.ParseFloat(fields[len(fields)-1], 64)
			if err != nil || uptime < 0 {
				t.Fatalf("uptime line = %q: %v", line, err)
			}
		}
		if value, ok := metricValue(t, line, "ajs_accounts{state="); ok {
			state := metricLabel(t, line)
			if value != wantAccounts[state] {
				t.Fatalf("ajs_accounts{state=%q} = %d, want %d", state, value, wantAccounts[state])
			}
		}
		if value, ok := metricValue(t, line, "ajs_nodes{state="); ok {
			state := metricLabel(t, line)
			if value != wantNodes[state] {
				t.Fatalf("ajs_nodes{state=%q} = %d, want %d", state, value, wantNodes[state])
			}
		}
	}
	if !sawBuild || !sawUptime {
		t.Fatalf("body lacks build info or uptime:\n%s", body)
	}
	for _, line := range []string{
		`ajs_accounts{state="expired"} 0`,
		`ajs_nodes{state="STARTING"} 0`,
	} {
		if !strings.Contains(string(body), line) {
			t.Fatalf("body lacks zero-state line %q:\n%s", line, body)
		}
	}
	for _, tc := range []struct {
		name string
		want int
	}{
		{"ajs_leases_total", len(st.Leases())},
		{"ajs_users_total", len(st.Users())},
		{"ajs_api_keys_total", len(st.Keys())},
		{"ajs_requests_total", len(st.Requests())},
	} {
		var got int
		var found bool
		for _, line := range lines {
			if value, ok := metricValue(t, line, tc.name+" "); ok {
				got = value
				found = true
			}
		}
		if !found {
			t.Fatalf("body lacks %s:\n%s", tc.name, body)
		}
		if got != tc.want {
			t.Fatalf("%s = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func metricValue(t *testing.T, line, prefix string) (int, bool) {
	t.Helper()
	if !strings.HasPrefix(line, prefix) {
		return 0, false
	}
	fields := strings.Fields(line)
	value, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		t.Fatalf("metric line %q: %v", line, err)
	}
	return value, true
}

func metricLabel(t *testing.T, line string) string {
	t.Helper()
	open := strings.Index(line, "{")
	close := strings.Index(line, "}")
	if open < 0 || close < open {
		t.Fatalf("metric line %q has no labels", line)
	}
	value := strings.TrimPrefix(line[open+1:close], "state=")
	return strings.Trim(value, `"`)
}

func TestPrometheusMetricsNoLeakage(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := apiCall(t, ts, http.MethodGet, "/metrics", nil)
	requireStatus(t, resp, http.StatusOK)

	for _, secret := range []string{
		"username", "password", "egress", "node-", "acc-", "u-01", "lease-",
		"203.0.113.", "198.51.100.", "192.0.2.", "demo-", "ada.ops",
	} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("/metrics leaked %q:\n%s", secret, body)
		}
	}
}

func TestPrometheusMetricsInsecureMode(t *testing.T) {
	server := New(Config{AllowInsecure: true}, store.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, body := doRequest(t, ts, newRequest(t, http.MethodGet, ts.URL+"/metrics", nil))
	requireStatus(t, resp, http.StatusOK)
	if !strings.Contains(string(body), "ajs_build_info") {
		t.Fatal("/metrics body lacks ajs_build_info")
	}
}
