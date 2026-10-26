package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

func mustCreateScopedKey(t *testing.T, ts *httptest.Server, body any) (model.ScopedKeyCreated, []byte) {
	t.Helper()
	resp, data := apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", body)
	requireStatus(t, resp, http.StatusCreated)
	return decodeBody[model.ScopedKeyCreated](t, data), data
}

func scopedCall(t *testing.T, ts *httptest.Server, secret, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	req := newRequest(t, method, ts.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+secret)
	if isUnsafeMethod(method) {
		req.Header.Set(adminCSRFHeader, adminCSRFValue)
	}
	return doRequest(t, ts, req)
}

func TestScopedKeyCRUDShowsSecretOnce(t *testing.T) {
	ts, st := newTestServer(t)

	created, createBody := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     "u-01",
		"label":      "CI read only",
		"scopes":     []string{"accounts:read"},
		"accountIds": []string{"acc-01"},
		"regions":    []string{"cn-zj"},
	})
	if !strings.HasPrefix(created.Secret, "ajs_sk_") {
		t.Fatalf("secret = %q, want the ajs_sk_ prefix", created.Secret)
	}
	if created.Key.ID == "" || created.Key.UserID != "u-01" || created.Key.UserName != "ada.ops" {
		t.Fatalf("created key = %+v", created.Key)
	}
	if created.Key.Prefix != created.Secret[:16] {
		t.Fatalf("prefix = %q, want %q", created.Key.Prefix, created.Secret[:16])
	}
	if created.Key.ExpiresAt != nil || created.Key.LastUsedAt != nil || created.Key.RevokedAt != nil {
		t.Fatalf("new key has optional timestamps set: %+v", created.Key)
	}
	if !bytes.Contains(createBody, []byte(created.Secret)) {
		t.Fatal("create response does not carry the one-time secret")
	}

	// The listing and the detail view must never repeat the secret.
	resp, listBody := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys", nil)
	requireStatus(t, resp, http.StatusOK)
	listed := decodeBody[[]model.ScopedKey](t, listBody)
	if len(listed) != 1 || listed[0].ID != created.Key.ID {
		t.Fatalf("list = %+v, want the created key", listed)
	}
	if bytes.Contains(listBody, []byte(created.Secret)) {
		t.Fatal("list response contains the scoped-key secret")
	}
	resp, detailBody := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if bytes.Contains(detailBody, []byte(created.Secret)) {
		t.Fatal("detail response contains the scoped-key secret")
	}

	// The persisted snapshot carries the hash, never the secret.
	snapshot, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if bytes.Contains(snapshot, []byte(created.Secret)) {
		t.Fatal("snapshot contains the scoped-key secret")
	}

	// Revocation is a delete of the credential, not of the row.
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)
	resp, revokedBody := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	revoked := decodeBody[model.ScopedKey](t, revokedBody)
	if revoked.RevokedAt == nil {
		t.Fatal("revokedAt is nil after DELETE")
	}
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)

	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
}

func TestScopedKeyEnforcementMatrix(t *testing.T) {
	ts, _ := newTestServer(t)

	readKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     "u-01",
		"label":      "accounts reader",
		"scopes":     []string{"accounts:read"},
		"accountIds": []string{"acc-01"},
	})
	resp, body := scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = scopedCall(t, ts, readKey.Secret, http.MethodPost, "/v1/accounts", map[string]any{
		"label": "forbidden", "username": "nope", "password": "x", "platform": "android",
	})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	resp, body = scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/settings", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	resp, _ = scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/metrics", nil)
	requireStatus(t, resp, http.StatusForbidden)

	metricsKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "metrics reader",
		"scopes": []string{"metrics:read"},
	})
	resp, _ = scopedCall(t, ts, metricsKey.Secret, http.MethodGet, "/v1/metrics", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, _ = scopedCall(t, ts, metricsKey.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusForbidden)
}

func TestScopedKeyAdminScopeCanManageKeys(t *testing.T) {
	ts, _ := newTestServer(t)
	adminKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01",
		"label":  "operator",
		"scopes": []string{"admin"},
	})
	resp, body := scopedCall(t, ts, adminKey.Secret, http.MethodGet, "/v1/users", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = scopedCall(t, ts, adminKey.Secret, http.MethodPost, "/v1/scoped-keys", map[string]any{
		"userId":     "u-02",
		"label":      "issued by admin key",
		"scopes":     []string{"accounts:read"},
		"accountIds": []string{"acc-01"},
	})
	requireStatus(t, resp, http.StatusCreated)
	if !strings.HasPrefix(decodeBody[model.ScopedKeyCreated](t, body).Secret, "ajs_sk_") {
		t.Fatal("admin-scoped key could not issue another key")
	}
}

func TestPlainAPIKeyIsNotABearerCredential(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/apikeys", map[string]any{
		"userId": "u-01", "label": "plain key",
	})
	requireStatus(t, resp, http.StatusCreated)
	plain := decodeBody[model.CreateKeyResult](t, body)
	if !strings.HasPrefix(plain.Secret, "ajs_live_") {
		t.Fatalf("plain secret = %q", plain.Secret)
	}
	resp, _ = scopedCall(t, ts, plain.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusUnauthorized)
}

func TestScopedKeyExpiryAnswers401(t *testing.T) {
	ts, _ := newTestServer(t)
	expires := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     "u-01",
		"label":      "short lived",
		"scopes":     []string{"accounts:read"},
		"accountIds": []string{"acc-01"},
		"expiresAt":  expires,
	})
	resp, _ := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	time.Sleep(2100 * time.Millisecond)
	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusUnauthorized)
	requireErrorCode(t, body, CodeUnauthorized)
}

func TestScopedKeyAccountRestrictions(t *testing.T) {
	ts, _ := newTestServer(t)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     "u-01",
		"label":      "one account",
		"scopes":     []string{"accounts:read", "leases:read", "catalog:read"},
		"accountIds": []string{"acc-01"},
	})
	resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	accounts := decodeBody[[]model.Account](t, body)
	if len(accounts) != 1 || accounts[0].ID != "acc-01" {
		t.Fatalf("restricted account list = %+v, want only acc-01", accounts)
	}
	resp, _ = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts/acc-01", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts/acc-02", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)

	resp, body = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/leases", nil)
	requireStatus(t, resp, http.StatusOK)
	for _, lease := range decodeBody[[]model.Lease](t, body) {
		if lease.AccountID != "acc-01" {
			t.Fatalf("restricted lease list leaked account %q", lease.AccountID)
		}
	}
	resp, body = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/nodes", nil)
	requireStatus(t, resp, http.StatusOK)
	for _, node := range decodeBody[[]model.ProxyNode](t, body) {
		if node.AccountID != "acc-01" {
			t.Fatalf("restricted node list leaked account %q", node.AccountID)
		}
	}
	resp, _ = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/nodes?account_id=acc-02", nil)
	requireStatus(t, resp, http.StatusForbidden)

	regionKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":  "u-01",
		"label":   "one region",
		"scopes":  []string{"accounts:read"},
		"regions": []string{"de-fra"},
	})
	resp, body = scopedCall(t, ts, regionKey.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	regional := decodeBody[[]model.Account](t, body)
	if len(regional) != 1 || regional[0].ID != "acc-04" {
		t.Fatalf("region-restricted list = %+v, want only acc-04", regional)
	}
	resp, _ = scopedCall(t, ts, regionKey.Secret, http.MethodGet, "/v1/accounts/acc-01", nil)
	requireStatus(t, resp, http.StatusForbidden)
}

func TestScopedKeyRecordsLastUsedAt(t *testing.T) {
	ts, _ := newTestServer(t)
	created, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     "u-01",
		"label":      "used key",
		"scopes":     []string{"accounts:read"},
		"accountIds": []string{"acc-01"},
	})
	// Start with enough room before the minute rolls so both requests land in
	// the same throttle window. The store-level test proves the minute reset.
	for time.Now().Second() > 56 {
		time.Sleep(50 * time.Millisecond)
	}
	resp, _ := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	first := decodeBody[model.ScopedKey](t, body)
	if first.LastUsedAt == nil {
		t.Fatal("lastUsedAt is nil after an authenticated request")
	}

	resp, _ = scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	second := decodeBody[model.ScopedKey](t, body)
	if second.LastUsedAt == nil || *second.LastUsedAt != *first.LastUsedAt {
		t.Fatalf("lastUsedAt changed inside one minute: %v -> %v", first.LastUsedAt, second.LastUsedAt)
	}
}

func TestScopedKeyCreateValidation(t *testing.T) {
	ts, _ := newTestServer(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing label", map[string]any{"userId": "u-01", "scopes": []string{"accounts:read"}}},
		{"unknown scope", map[string]any{"userId": "u-01", "label": "x", "scopes": []string{"root"}}},
		{"unknown user", map[string]any{"userId": "u-99", "label": "x", "scopes": []string{"accounts:read"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", tc.body)
			want := http.StatusBadRequest
			if tc.name == "unknown user" {
				want = http.StatusNotFound
			}
			requireStatus(t, resp, want)
			if resp.StatusCode == http.StatusBadRequest {
				requireErrorCode(t, body, CodeInvalidArgument)
			}
		})
	}
}

func TestScopedKeyGetUnknownAnswers404(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/scoped-keys/sk-99", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
	resp, body = apiCall(t, ts, http.MethodDelete, "/v1/scoped-keys/sk-99", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

// mustIssueScopedKey issues a key through the admin API with an optional
// account/region restriction. Nil lists mean unrestricted, which is the
// compatibility case; an empty non-nil list is normalized away by the store to
// the same meaning.
func mustIssueScopedKey(t *testing.T, ts *httptest.Server, scopes []string, accountIDs, regions []string) model.ScopedKeyCreated {
	t.Helper()
	body := map[string]any{
		"userId": "u-01",
		"label":  "restricted " + strings.Join(scopes, "+"),
		"scopes": scopes,
	}
	if accountIDs != nil {
		body["accountIds"] = accountIDs
	}
	if regions != nil {
		body["regions"] = regions
	}
	created, _ := mustCreateScopedKey(t, ts, body)
	return created
}

// TestScopedKeyPerUserRequests pins AJS-451: a non-admin scoped key reads only
// rows whose exact RequestLog.UserID matches the key's user, filtered before
// pagination, and a query naming another user is refused. The account label is
// ignored even when it names an account the key may use, an empty grant is
// still a per-user view, and operator callers keep the global log.
func TestScopedKeyPerUserRequests(t *testing.T) {
	ts, st := newTestServer(t)
	const (
		privateTarget = "tenant-b-private.example:443"
		secretID      = "tenant-b-secret-request"
	)
	// The row has no UserID but carries the label of the account the key may
	// use: a label filter would hand it to u-01, an exact UserID filter does
	// not. It stays readable to an operator, so absence is not mistaken for a
	// missing row.
	st.AppendRequest(model.RequestLog{
		ID:           secretID,
		AccountLabel: "Northwind Ops",
		NodeLabel:    "cn-zj-01",
		Region:       "cn-zj",
		Target:       privateTarget,
		Source:       model.SourceProxy,
	})
	wantRows := 0
	for _, row := range st.Requests() {
		if row.UserID == "u-01" {
			wantRows++
		}
	}
	if wantRows == 0 || wantRows == len(st.Requests()) {
		t.Fatalf("seed has %d u-01 rows of %d; the fixture cannot tell per-user from global",
			wantRows, len(st.Requests()))
	}

	key := mustIssueScopedKey(t, ts, []string{"requests:read"}, []string{"acc-01"}, nil)
	resp, body := scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/requests?limit=0", nil)
	requireStatus(t, resp, http.StatusOK)
	rows := decodeBody[[]model.RequestLog](t, body)
	if len(rows) != wantRows {
		t.Fatalf("scoped request log = %d rows, want %d", len(rows), wantRows)
	}
	for _, row := range rows {
		if row.UserID != "u-01" {
			t.Fatalf("scoped request log leaked user %q: %+v", row.UserID, row)
		}
	}
	for _, name := range []string{"build-bot", "qa.pipeline", "field.lab"} {
		if strings.Contains(string(body), name) {
			t.Fatalf("scoped request log leaked user %q: %s", name, body)
		}
	}
	if strings.Contains(string(body), privateTarget) || strings.Contains(string(body), secretID) {
		t.Fatalf("scoped request log leaked a row with no matching UserID: %s", body)
	}

	// The page size applies after the filter, not to the global log.
	resp, body = scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/requests?limit=3", nil)
	requireStatus(t, resp, http.StatusOK)
	page := decodeBody[[]model.RequestLog](t, body)
	if len(page) != 3 {
		t.Fatalf("?limit=3 = %d rows, want 3", len(page))
	}
	for _, row := range page {
		if row.UserID != "u-01" {
			t.Fatalf("paged row belongs to %q", row.UserID)
		}
	}

	// Naming another user is refused; the key's own user is the same as
	// omitting the filter.
	for _, path := range []string{"/v1/requests?user_id=u-02", "/v1/requests?userId=u-02"} {
		resp, body = scopedCall(t, ts, key.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorReason(t, body, ReasonScopedKeyRestricted)
	}
	resp, body = scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/requests?user_id=u-01&limit=0", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.RequestLog](t, body); len(got) != wantRows {
		t.Fatalf("own-user filter = %d rows, want %d", len(got), wantRows)
	}

	// An empty grant keeps the per-user view; it never widens to the log.
	open := mustIssueScopedKey(t, ts, []string{"requests:read"}, nil, nil)
	resp, body = scopedCall(t, ts, open.Secret, http.MethodGet, "/v1/requests?limit=0", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.RequestLog](t, body); len(got) != wantRows {
		t.Fatalf("ungranted key = %d rows, want its own %d", len(got), wantRows)
	}

	// Operator callers keep the global log, including the unattributed row.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/requests?limit=0", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.RequestLog](t, body); len(got) != len(st.Requests()) {
		t.Fatalf("admin log = %d rows, want %d", len(got), len(st.Requests()))
	}
	if !strings.Contains(string(body), privateTarget) {
		t.Fatalf("admin log lost the appended row: %s", body)
	}
}

// TestScopedKeyRestrictedMetricsAndTopology covers the attributable global
// reads: metrics contributes only the visible accountHealth rows, and the
// topology map is built from the visible nodes rather than filtered tags.
func TestScopedKeyRestrictedMetricsAndTopology(t *testing.T) {
	ts, _ := newTestServer(t)
	key := mustIssueScopedKey(t, ts, []string{"metrics:read", "catalog:read"}, []string{"acc-01"}, nil)

	resp, body := scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/metrics", nil)
	requireStatus(t, resp, http.StatusOK)
	metrics := decodeBody[model.Metrics](t, body)
	if metrics.Range != model.Range24h {
		t.Fatalf("range = %q, want 24h", metrics.Range)
	}
	if len(metrics.AccountHealth) != 1 ||
		metrics.AccountHealth[0].AccountID != "acc-01" ||
		metrics.AccountHealth[0].Label != "Northwind Ops" {
		t.Fatalf("restricted accountHealth = %+v", metrics.AccountHealth)
	}
	if strings.Contains(string(body), "acc-02") || strings.Contains(string(body), "Field Lab") {
		t.Fatalf("restricted metrics leaked another tenant: %s", body)
	}

	resp, body = scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/topology", nil)
	requireStatus(t, resp, http.StatusOK)
	topology := decodeBody[model.Topology](t, body)
	want := map[string]string{
		"node-01": "cn-zj-01",
		"node-02": "cn-sh-01",
		"node-03": "hk-01",
	}
	if len(topology.Nodes) != len(want) {
		t.Fatalf("restricted topology = %v, want %v", topology.Nodes, want)
	}
	for id, tag := range want {
		if topology.Nodes[id] != tag {
			t.Fatalf("restricted topology[%q] = %q, want %q", id, topology.Nodes[id], tag)
		}
	}
	if topology.Subscription != "ajs-live" || topology.GeneratedAt == "" {
		t.Fatalf("restricted topology header = %+v", topology)
	}
	if strings.Contains(string(body), "node-04") || strings.Contains(string(body), "jp-tyo-01") {
		t.Fatalf("restricted topology leaked another tenant: %s", body)
	}
}

// scopedTopologyPanel is the minimal runtime.Panel for the panel-mode topology
// projection: it serves one fixed last-good view.
type scopedTopologyPanel struct {
	topology runtime.Topology
}

func (p *scopedTopologyPanel) Topology(context.Context) (runtime.Topology, error) {
	return p.topology, nil
}
func (p *scopedTopologyPanel) Reconcile(context.Context) (runtime.Topology, error) {
	return p.topology, nil
}
func (p *scopedTopologyPanel) PublishLease(context.Context, model.Lease) error { return nil }
func (p *scopedTopologyPanel) WithdrawLease(context.Context, string) error     { return nil }
func (p *scopedTopologyPanel) RotateLease(context.Context, string) (model.Lease, error) {
	return model.Lease{}, nil
}
func (p *scopedTopologyPanel) ReleaseLease(context.Context, string) error { return nil }
func (p *scopedTopologyPanel) DrainNode(context.Context, string) (model.ProxyNode, error) {
	return model.ProxyNode{}, nil
}
func (p *scopedTopologyPanel) UndrainNode(context.Context, string) (model.ProxyNode, error) {
	return model.ProxyNode{}, nil
}

// TestScopedKeyRestrictedPanelTopology checks the Resin path: the raw global
// Content and Nodes are never forwarded to a restricted key; both are rebuilt
// from the same visible live nodes, so ids and subscription lines agree.
func TestScopedKeyRestrictedPanelTopology(t *testing.T) {
	st := store.New()
	panel := &scopedTopologyPanel{topology: runtime.Topology{
		SubscriptionID: "sub-1",
		PlatformID:     "plat-1",
		Content:        "socks5://127.0.0.1:13000#cn-zj-01\nsocks5://127.0.0.1:13003#jp-tyo-01\n",
		Nodes:          map[string]string{"node-01": "cn-zj-01", "node-04": "jp-tyo-01"},
		LastGood:       "2026-10-26T00:00:00Z",
		ResinVersion:   "1.2.3",
	}}
	server := New(Config{AdminToken: testToken, Panel: panel}, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	key := mustIssueScopedKey(t, ts, []string{"catalog:read"}, []string{"acc-01"}, nil)
	resp, body := scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/topology", nil)
	requireStatus(t, resp, http.StatusOK)
	topology := decodeBody[runtime.Topology](t, body)
	if len(topology.Nodes) != 2 || topology.Nodes["node-01"] != "cn-zj-01" || topology.Nodes["node-02"] != "cn-sh-01" {
		t.Fatalf("projected panel nodes = %v", topology.Nodes)
	}
	if _, ok := topology.Nodes["node-03"]; ok {
		t.Fatal("projected panel nodes include a READY node; the panel publishes LIVE only")
	}
	if _, ok := topology.Nodes["node-04"]; ok {
		t.Fatal("projected panel nodes leaked another tenant")
	}
	if !strings.Contains(topology.Content, "#cn-zj-01") || !strings.Contains(topology.Content, "#cn-sh-01") {
		t.Fatalf("projected content = %q, want the visible live tags", topology.Content)
	}
	if strings.Contains(topology.Content, "jp-tyo-01") || strings.Contains(topology.Content, "node-04") {
		t.Fatalf("projected content leaked another tenant: %q", topology.Content)
	}
	if topology.SubscriptionID != "sub-1" || topology.ResinVersion != "1.2.3" || topology.LastGood != "2026-10-26T00:00:00Z" {
		t.Fatalf("projected panel metadata = %+v", topology)
	}

	// The admin token keeps the raw panel view. A non-admin catalog key cannot
	// be issued without an account grant, so there is no unbounded customer
	// view to compare against.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/topology", nil)
	requireStatus(t, resp, http.StatusOK)
	raw := decodeBody[runtime.Topology](t, body)
	if raw.Nodes["node-04"] != "jp-tyo-01" || !strings.Contains(raw.Content, "jp-tyo-01") {
		t.Fatalf("admin panel topology = %+v", raw)
	}
}

// TestScopedKeyRestrictedGlobalReadsAreForbidden covers the surfaces that
// cannot be partitioned honestly: audit (free-text attribution), the audit CSV
// export, and the Prometheus exposition.
func TestScopedKeyRestrictedGlobalReadsAreForbidden(t *testing.T) {
	ts, _ := newTestServer(t)
	restricted := mustIssueScopedKey(t, ts, []string{"audit:read", "metrics:read"}, []string{"acc-01"}, nil)
	for _, path := range []string{"/v1/audit", "/v1/audit/export.csv", "/metrics"} {
		resp, body := scopedCall(t, ts, restricted.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
	}

	// Unrestricted scoped keys keep the existing 200 contract.
	open := mustIssueScopedKey(t, ts, []string{"audit:read", "metrics:read"}, nil, nil)
	for _, path := range []string{"/v1/audit", "/v1/audit/export.csv", "/metrics"} {
		resp, _ := scopedCall(t, ts, open.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusOK)
	}
}

// TestScopedKeyRestrictedAccountExportIsForbidden covers every body shape of
// POST /v1/accounts/export: the router refuses the route before parsing, so an
// explicit foreign id, no ids and a filter-only body all answer 403 with no
// export audit row and unchanged accounts.
func TestScopedKeyRestrictedAccountExportIsForbidden(t *testing.T) {
	ts, st := newTestServer(t)
	key := mustIssueScopedKey(t, ts, []string{"accounts:read"}, []string{"acc-01"}, nil)
	auditBefore := len(st.Audit(store.AuditRetention))
	accountsBefore := len(st.Accounts())

	// Encrypted bodies would succeed if the route were ever re-enabled, so the
	// 403 here is the only thing preventing the disclosure.
	bodies := []map[string]any{
		{"format": "encrypted", "passphrase": "test-passphrase", "ids": []string{"acc-02"}},
		{"format": "encrypted", "passphrase": "test-passphrase"},
		{"format": "encrypted", "passphrase": "test-passphrase", "filter": map[string]any{"q": "Field"}},
	}
	for _, body := range bodies {
		resp, payload := scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/accounts/export", body)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, payload, CodeForbidden)
	}
	if got := len(st.Audit(store.AuditRetention)); got != auditBefore {
		t.Fatalf("audit rows = %d, want %d: a refused export wrote an audit row", got, auditBefore)
	}
	if got := len(st.Accounts()); got != accountsBefore {
		t.Fatalf("accounts = %d, want %d", got, accountsBefore)
	}
	account, ok := st.Account("acc-02")
	if !ok || account.Label != "Field Lab" {
		t.Fatalf("acc-02 = %+v (ok=%v), want it unchanged", account, ok)
	}
}

// TestScopedKeyRestrictedAccountImportAndBulkDeleteAreForbidden covers the
// destructive account IO routes, including import dry-run, which is not a
// read-only exception.
func TestScopedKeyRestrictedAccountImportAndBulkDeleteAreForbidden(t *testing.T) {
	ts, st := newTestServer(t)
	key := mustIssueScopedKey(t, ts, []string{"accounts:write"}, []string{"acc-01"}, nil)
	auditBefore := len(st.Audit(store.AuditRetention))
	accountsBefore := len(st.Accounts())
	importRequest := func(dryRun bool) map[string]any {
		return map[string]any{
			"source": map[string]any{"kind": "json", "content": map[string]any{
				"format":  "ajs-accounts",
				"version": 1,
				"accounts": []map[string]any{{
					"label": "Imported", "username": "imported", "platform": "linux",
					"concurrency": 1, "regions": []string{"cn"},
				}},
			}},
			"options": map[string]any{"dryRun": dryRun},
		}
	}
	cases := []struct {
		path string
		body map[string]any
	}{
		{"/v1/accounts/import", importRequest(true)},
		{"/v1/accounts/import", importRequest(false)},
		{"/v1/accounts/bulk-delete", map[string]any{"ids": []string{"acc-02"}}},
	}
	for _, tc := range cases {
		resp, payload := scopedCall(t, ts, key.Secret, http.MethodPost, tc.path, tc.body)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, payload, CodeForbidden)
	}
	if got := len(st.Audit(store.AuditRetention)); got != auditBefore {
		t.Fatalf("audit rows = %d, want %d: a refused account IO call wrote an audit row", got, auditBefore)
	}
	if got := len(st.Accounts()); got != accountsBefore {
		t.Fatalf("accounts = %d, want %d", got, accountsBefore)
	}
	account, ok := st.Account("acc-02")
	if !ok || account.Label != "Field Lab" {
		t.Fatalf("acc-02 = %+v (ok=%v), want it unchanged", account, ok)
	}
	if _, ok := st.Account("acc-imported"); ok {
		t.Fatal("a refused import still created an account")
	}
}

// TestScopedKeyAdminAndUnrestrictedCompatibility covers the two bypasses: an
// admin token or admin-scoped key reaches account IO and the global reads, and
// a non-admin key with both restriction lists empty sees every row while still
// needing each route scope.
func TestScopedKeyAdminAndUnrestrictedCompatibility(t *testing.T) {
	ts, st := newTestServer(t)
	importRequest := func(dryRun bool) map[string]any {
		return map[string]any{
			"source": map[string]any{"kind": "json", "content": map[string]any{
				"format":  "ajs-accounts",
				"version": 1,
				"accounts": []map[string]any{{
					"label": "Imported", "username": "imported", "platform": "linux",
					"concurrency": 1, "regions": []string{"cn"},
				}},
			}},
			"options": map[string]any{"dryRun": dryRun},
		}
	}

	resp, _ := apiCall(t, ts, http.MethodPost, "/v1/accounts/export",
		map[string]any{"format": "encrypted", "passphrase": "test-passphrase", "ids": []string{"acc-01"}})
	requireStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("X-AJS-Export-Count"); got != "1" {
		t.Fatalf("admin export count = %q, want 1", got)
	}
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/accounts/import", importRequest(true))
	requireStatus(t, resp, http.StatusOK)
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/accounts/bulk-delete", map[string]any{"ids": []string{"acc-06"}})
	requireStatus(t, resp, http.StatusOK)
	if _, ok := st.Account("acc-06"); ok {
		t.Fatal("admin bulk-delete did not delete acc-06")
	}

	// An admin-scoped key stays admin even with a restriction attached: it must
	// not be narrowed by the account filter.
	adminKey := mustIssueScopedKey(t, ts, []string{"admin"}, []string{"acc-01"}, nil)
	for _, path := range []string{"/v1/audit", "/v1/audit/export.csv", "/metrics"} {
		resp, _ = scopedCall(t, ts, adminKey.Secret, http.MethodGet, path, nil)
		requireStatus(t, resp, http.StatusOK)
	}
	resp, _ = scopedCall(t, ts, adminKey.Secret, http.MethodPost, "/v1/accounts/export",
		map[string]any{"format": "encrypted", "passphrase": "test-passphrase"})
	requireStatus(t, resp, http.StatusOK)
	resp, _ = scopedCall(t, ts, adminKey.Secret, http.MethodPost, "/v1/accounts/import", importRequest(true))
	requireStatus(t, resp, http.StatusOK)
	resp, _ = scopedCall(t, ts, adminKey.Secret, http.MethodPost, "/v1/accounts/bulk-delete", map[string]any{"ids": []string{"acc-05"}})
	requireStatus(t, resp, http.StatusOK)
	if _, ok := st.Account("acc-05"); ok {
		t.Fatal("admin-scoped key bulk-delete did not delete acc-05")
	}

	// Both lists empty means all accounts, not none, but each route still needs
	// its own scope. The request log stays per-user: the key reads its own
	// subject's rows, never the global log.
	open := mustIssueScopedKey(t, ts, []string{"requests:read"}, nil, nil)
	resp, body := scopedCall(t, ts, open.Secret, http.MethodGet, "/v1/requests?limit=0", nil)
	requireStatus(t, resp, http.StatusOK)
	visible := decodeBody[[]model.RequestLog](t, body)
	wantRows := 0
	for _, row := range st.Requests() {
		if row.UserID == "u-01" {
			wantRows++
		}
	}
	if len(visible) != wantRows {
		t.Fatalf("ungranted request rows = %d, want its own %d", len(visible), wantRows)
	}
	resp, body = scopedCall(t, ts, open.Secret, http.MethodGet, "/v1/metrics", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	resp, _ = scopedCall(t, ts, open.Secret, http.MethodGet, "/v1/schedules", nil)
	requireStatus(t, resp, http.StatusForbidden)
}

// TestScopedKeyRegionAndCombinedRestrictions pins the existing AND/overlap
// semantics: every nonempty dimension must match, an id+region pair is ANDed,
// and the region dimension matches on overlap with the account's own regions.
func TestScopedKeyRegionAndCombinedRestrictions(t *testing.T) {
	ts, _ := newTestServer(t)

	regionKey := mustIssueScopedKey(t, ts, []string{"accounts:read"}, nil, []string{"hk"})
	resp, body := scopedCall(t, ts, regionKey.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	ids := make([]string, 0, 3)
	for _, account := range decodeBody[[]model.Account](t, body) {
		ids = append(ids, account.ID)
	}
	if got := strings.Join(ids, ","); got != "acc-01,acc-03,acc-06" {
		t.Fatalf("region-restricted accounts = %q, want acc-01,acc-03,acc-06", got)
	}

	// The id matches but no region overlaps: nothing is visible.
	andKey := mustIssueScopedKey(t, ts, []string{"accounts:read", "metrics:read", "catalog:read"},
		[]string{"acc-01"}, []string{"jp-tyo"})
	resp, body = scopedCall(t, ts, andKey.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Account](t, body); len(got) != 0 {
		t.Fatalf("AND-restricted accounts = %+v, want none", got)
	}
	resp, body = scopedCall(t, ts, andKey.Secret, http.MethodGet, "/v1/metrics", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Metrics](t, body).AccountHealth; len(got) != 0 {
		t.Fatalf("AND-restricted accountHealth = %+v, want none", got)
	}
	resp, body = scopedCall(t, ts, andKey.Secret, http.MethodGet, "/v1/topology", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.Topology](t, body).Nodes; len(got) != 0 {
		t.Fatalf("AND-restricted topology = %v, want none", got)
	}

	// The id matches and one of its regions overlaps: it stays visible.
	overlapKey := mustIssueScopedKey(t, ts, []string{"accounts:read"}, []string{"acc-01"}, []string{"hk"})
	resp, body = scopedCall(t, ts, overlapKey.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	overlap := decodeBody[[]model.Account](t, body)
	if len(overlap) != 1 || overlap[0].ID != "acc-01" {
		t.Fatalf("overlap-restricted accounts = %+v, want acc-01", overlap)
	}
}

// TestScopedKeyAdminExportUnknownIDKeepsNotFound pins the pre-existing admin
// contract that the restriction work must not change.
func TestScopedKeyAdminExportUnknownIDKeepsNotFound(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/export",
		map[string]any{"format": "encrypted", "passphrase": "test-passphrase", "ids": []string{"acc-missing"}})
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

// restrictedWriteKey issues a key that may read and write the account surface
// but only for the accounts or regions it names.
func restrictedWriteKey(t *testing.T, ts *httptest.Server, accountIDs, regions []string) model.ScopedKeyCreated {
	t.Helper()
	body := map[string]any{
		"userId": "u-01",
		"label":  "restricted writer",
		"scopes": []string{
			"accounts:read", "accounts:write", "catalog:read", "catalog:write",
			"leases:read", "leases:write",
		},
	}
	if accountIDs != nil {
		body["accountIds"] = accountIDs
	}
	if regions != nil {
		body["regions"] = regions
	}
	created, _ := mustCreateScopedKey(t, ts, body)
	return created
}

// TestScopedKeyRestrictedWritesAreAccountScoped pins AJS-447 (SEC-1): the
// accountIds/regions restriction covers writes, not just reads. Every mutation
// that targets another tenant's account, lease, node or operation answers 403
// scoped_key_restricted and leaves the target untouched, while the key's own
// account keeps working.
func TestScopedKeyRestrictedWritesAreAccountScoped(t *testing.T) {
	ts, st := newTestServer(t)
	key := restrictedWriteKey(t, ts, []string{"acc-01"}, nil)

	// A queued switch on acc-02 gives the cancel route a foreign target.
	queued, busy, err := st.BeginOperation(model.Operation{
		Type: model.OperationTypeSwitch, AccountID: "acc-02",
		Target: model.OperationTarget{ServerID: "srv-b"}, RequestedBy: "admin",
	})
	if err != nil || busy != nil {
		t.Fatalf("BeginOperation: busy=%+v err=%v", busy, err)
	}

	denied := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPatch, "/v1/accounts/acc-02", map[string]any{"label": "hijacked"}},
		{http.MethodDelete, "/v1/accounts/acc-02", nil},
		{http.MethodPost, "/v1/accounts/acc-02/actions/login", nil},
		{http.MethodPost, "/v1/accounts/acc-02/actions/logout", nil},
		{http.MethodPost, "/v1/accounts/acc-02/actions/connect", map[string]any{"serverId": "srv-b", "confirm": true}},
		{http.MethodPost, "/v1/accounts/acc-02/actions/disconnect", map[string]any{"confirm": true}},
		{http.MethodPost, "/v1/accounts/acc-02/actions/reconnect", map[string]any{"confirm": true}},
		{http.MethodPost, "/v1/accounts/acc-02/actions/select-node", map[string]any{"target": map[string]any{"serverId": "srv-b"}}},
		{http.MethodPut, "/v1/accounts/acc-02/selection", map[string]any{"serverId": "srv-b"}},
		{http.MethodDelete, "/v1/accounts/acc-02/selection", nil},
		{http.MethodPost, "/v1/leases", map[string]any{"userId": "u-01", "platform": "ios", "accountId": "acc-02"}},
		{http.MethodPost, "/v1/leases", map[string]any{"userId": "u-01", "platform": "ios"}},
		{http.MethodPost, "/v1/nodes/node-04/actions/drain", nil},
		{http.MethodPost, "/v1/nodes/node-04/actions/undrain", nil},
		{http.MethodPost, "/v1/nodes/node-04/actions/probe", nil},
		{http.MethodDelete, "/v1/operations/" + queued.ID, nil},
	}
	for _, tc := range denied {
		resp, body := scopedCall(t, ts, key.Secret, tc.method, tc.path, tc.body)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
		requireErrorReason(t, body, ReasonScopedKeyRestricted)
		if bytes.Contains(body, []byte("acc-02")) || bytes.Contains(body, []byte("Field Lab")) {
			t.Fatalf("%s %s refusal leaked the foreign tenant: %s", tc.method, tc.path, body)
		}
	}

	// A lease owned by another user is answered exactly like a missing id: 404,
	// before the panel or the store is touched. A same-user lease on a foreign
	// account stays 403 (the account grant), which is the only lease the key's
	// own subject can address.
	leaseDenied := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodDelete, "/v1/leases/lease-03", map[string]any{"confirm": true}},
		{http.MethodPost, "/v1/leases/lease-03/actions/rotate", nil},
		{http.MethodDelete, "/v1/leases/lease-99", map[string]any{"confirm": true}},
		{http.MethodPost, "/v1/leases/lease-99/actions/rotate", nil},
	}
	for _, tc := range leaseDenied {
		resp, body := scopedCall(t, ts, key.Secret, tc.method, tc.path, tc.body)
		requireStatus(t, resp, http.StatusNotFound)
		requireErrorCode(t, body, CodeNotFound)
	}
	resp, body := scopedCall(t, ts, key.Secret, http.MethodDelete, "/v1/leases/lease-02",
		map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)

	// Nothing was mutated by a refused write.
	if account, ok := st.Account("acc-02"); !ok || account.Label != "Field Lab" {
		t.Fatalf("acc-02 changed under a refused write: %+v (exists=%v)", account, ok)
	}
	if lease, ok := st.Lease("lease-03"); !ok || lease.AccountID != "acc-02" || lease.Status == model.LeaseRevoked {
		t.Fatalf("lease-03 changed under a refused write: %+v (exists=%v)", lease, ok)
	}
	if node, ok := st.Node("node-04"); !ok || node.State != model.NodeLive {
		t.Fatalf("node-04 changed under a refused write: %+v (exists=%v)", node, ok)
	}
	if operation, ok := st.Operation(queued.ID); !ok || operation.State != model.OperationQueued {
		t.Fatalf("operation changed under a refused cancel: %+v (exists=%v)", operation, ok)
	}
	for _, lease := range st.Leases() {
		if lease.UserID == "u-01" && lease.AccountID == "acc-02" {
			t.Fatalf("a refused lease create allocated %+v", lease)
		}
	}

	// The same key still acts on its own account.
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPatch, "/v1/accounts/acc-01",
		map[string]any{"label": "Northwind Ops (restricted write)"})
	requireStatus(t, resp, http.StatusOK)
	if updated := decodeBody[model.Account](t, body); updated.Label != "Northwind Ops (restricted write)" {
		t.Fatalf("own-account patch = %+v", updated)
	}
	resp, _ = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/accounts/acc-01/actions/login", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/accounts/acc-01/actions/connect",
		map[string]any{"serverId": "srv-a", "confirm": true})
	requireStatus(t, resp, http.StatusConflict)
	requireErrorReason(t, body, runtime.ReasonLoginDisabled)
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/accounts/acc-01/actions/select-node",
		map[string]any{"target": map[string]any{"serverId": "srv-a"}})
	requireStatus(t, resp, http.StatusConflict)
	requireErrorReason(t, body, runtime.ReasonLoginDisabled)
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/nodes/node-01/actions/drain", nil)
	requireStatus(t, resp, http.StatusOK)
	if drained := decodeBody[model.ProxyNode](t, body); drained.State != model.NodeDraining {
		t.Fatalf("own-account drain = %+v", drained)
	}
	resp, _ = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/nodes/node-01/actions/undrain", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, _ = scopedCall(t, ts, key.Secret, http.MethodDelete, "/v1/leases/lease-01", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	if _, ok := st.Lease("lease-01"); ok {
		t.Fatal("lease-01 survived its own key's revoke")
	}
	// The account has one serving slot in the seed and both are taken. The
	// second row belongs to u-03, so the key cannot free it; the operator does,
	// and the same key then allocates for its own subject.
	if _, err := st.RevokeLease("lease-06"); err != nil {
		t.Fatalf("RevokeLease(lease-06): %v", err)
	}
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/leases", map[string]any{
		"userId": "u-01", "platform": "android", "accountId": "acc-01",
	})
	requireStatus(t, resp, http.StatusCreated)
	if created := decodeBody[leaseCreateResult](t, body); created.Lease.AccountID != "acc-01" || created.Lease.UserID != "u-01" {
		t.Fatalf("own-account lease = %+v", created.Lease)
	}
}

// TestScopedKeyRegionRestrictedWrites pins the region dimension on writes: a
// key restricted to de-fra may mutate acc-04 and nothing else, and only for its
// own user subject.
func TestScopedKeyRegionRestrictedWrites(t *testing.T) {
	ts, st := newTestServer(t)
	key := restrictedWriteKey(t, ts, nil, []string{"de-fra"})

	// lease-01 belongs to the key's user but sits on acc-01 (cn-zj), so the
	// region grant refuses it with the account reason.
	resp, body := scopedCall(t, ts, key.Secret, http.MethodDelete, "/v1/leases/lease-01", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPatch, "/v1/accounts/acc-02", map[string]any{"label": "hijacked"})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)
	resp, _ = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/nodes/node-04/actions/drain", nil)
	requireStatus(t, resp, http.StatusForbidden)
	if _, ok := st.Lease("lease-01"); !ok {
		t.Fatal("a region-refused revoke removed lease-01")
	}

	// acc-04 is in de-fra, so the same verbs succeed there.
	resp, _ = scopedCall(t, ts, key.Secret, http.MethodPatch, "/v1/accounts/acc-04",
		map[string]any{"label": "Partner EU (regional write)"})
	requireStatus(t, resp, http.StatusOK)
	resp, _ = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/nodes/node-09/actions/drain", nil)
	requireStatus(t, resp, http.StatusOK)

	// The seed has no lease for this key's user on acc-04, so the operator frees
	// the two u-04 rows and creates one for u-01. The region key may then revoke
	// its own subject's lease on its own region.
	for _, id := range []string{"lease-07", "lease-08"} {
		if _, err := st.RevokeLease(id); err != nil {
			t.Fatalf("RevokeLease(%s): %v", id, err)
		}
	}
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/leases", map[string]any{
		"userId": "u-01", "platform": "windows", "accountId": "acc-04",
	})
	requireStatus(t, resp, http.StatusCreated)
	regional := decodeBody[leaseCreateResult](t, body).Lease
	resp, _ = scopedCall(t, ts, key.Secret, http.MethodDelete, "/v1/leases/"+regional.ID, map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
}

// TestScopedKeyRestrictedWriteUnknownTargetIsIndistinguishable pins the probe
// rule on writes: an id that does not exist and an id the caller may not act on
// answer the same body byte for byte, so a mutation cannot be used to learn
// whether a lease, node or account exists. A lease id is answered 404 (unknown
// and another user's lease are the same); an account-shaped id is answered 403
// scoped_key_restricted. An admin token keeps its 404.
func TestScopedKeyRestrictedWriteUnknownTargetIsIndistinguishable(t *testing.T) {
	ts, st := newTestServer(t)
	key := restrictedWriteKey(t, ts, []string{"acc-01"}, nil)
	queued, busy, err := st.BeginOperation(model.Operation{
		Type: model.OperationTypeSwitch, AccountID: "acc-02",
		Target: model.OperationTarget{ServerID: "srv-b"}, RequestedBy: "admin",
	})
	if err != nil || busy != nil {
		t.Fatalf("BeginOperation: busy=%+v err=%v", busy, err)
	}

	cases := []struct {
		method         string
		unknown, other string
		status         int
		reason         string
	}{
		{http.MethodDelete, "/v1/leases/lease-99", "/v1/leases/lease-03", http.StatusNotFound, ""},
		{http.MethodPost, "/v1/leases/lease-99/actions/rotate", "/v1/leases/lease-03/actions/rotate", http.StatusNotFound, ""},
		{http.MethodPost, "/v1/nodes/node-99/actions/drain", "/v1/nodes/node-04/actions/drain", http.StatusForbidden, ReasonScopedKeyRestricted},
		{http.MethodPost, "/v1/nodes/node-99/actions/probe", "/v1/nodes/node-04/actions/probe", http.StatusForbidden, ReasonScopedKeyRestricted},
		{http.MethodDelete, "/v1/accounts/acc-99", "/v1/accounts/acc-02", http.StatusForbidden, ReasonScopedKeyRestricted},
		{http.MethodPatch, "/v1/accounts/acc-99", "/v1/accounts/acc-02", http.StatusForbidden, ReasonScopedKeyRestricted},
		{http.MethodDelete, "/v1/operations/op-9999", "/v1/operations/" + queued.ID, http.StatusForbidden, ReasonScopedKeyRestricted},
	}
	for _, tc := range cases {
		var body any
		if tc.method == http.MethodDelete && strings.HasPrefix(tc.other, "/v1/leases/") {
			body = map[string]any{"confirm": true}
		}
		if tc.method == http.MethodPatch {
			body = map[string]any{"label": "hijacked"}
		}
		respUnknown, unknownBody := scopedCall(t, ts, key.Secret, tc.method, tc.unknown, body)
		requireStatus(t, respUnknown, tc.status)
		respOther, otherBody := scopedCall(t, ts, key.Secret, tc.method, tc.other, body)
		requireStatus(t, respOther, tc.status)
		if tc.reason != "" {
			requireErrorReason(t, unknownBody, tc.reason)
			requireErrorReason(t, otherBody, tc.reason)
		}
		if !bytes.Equal(unknownBody, otherBody) {
			t.Fatalf("%s %s and %s answer differently: %s vs %s",
				tc.method, tc.unknown, tc.other, unknownBody, otherBody)
		}
	}
	if operation, ok := st.Operation(queued.ID); !ok || operation.State != model.OperationQueued {
		t.Fatalf("a refused cancel changed the operation: %+v (exists=%v)", operation, ok)
	}

	// An admin token sees the plain 404 contract for the same unknown ids.
	for _, path := range []string{"/v1/leases/lease-99", "/v1/accounts/acc-99", "/v1/operations/op-9999"} {
		var body any
		if strings.HasPrefix(path, "/v1/leases/") {
			body = map[string]any{"confirm": true}
		}
		resp, payload := apiCall(t, ts, http.MethodDelete, path, body)
		requireStatus(t, resp, http.StatusNotFound)
		requireErrorCode(t, payload, CodeNotFound)
	}
	resp, payload := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-99/actions/drain", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, payload, CodeNotFound)
}

// TestScopedKeyRestrictedLeaseReplayIsRefused pins the replay guard: the store
// answers an idempotent replay before it reads the requested accountId, so the
// lease that comes back is checked again. A key restricted to acc-01 must not
// receive the acc-02 lease an admin created under the same idempotency key.
func TestScopedKeyRestrictedLeaseReplayIsRefused(t *testing.T) {
	ts, st := newTestServer(t)
	// acc-02 is at its one-slot capacity in the seed; free both rows so the
	// admin create below can allocate.
	for _, id := range []string{"lease-03", "lease-04"} {
		if _, err := st.RevokeLease(id); err != nil {
			t.Fatalf("RevokeLease(%s): %v", id, err)
		}
	}
	req := newRequest(t, http.MethodPost, ts.URL+"/v1/leases", map[string]any{
		"userId": "u-01", "platform": "ios", "accountId": "acc-02", "idempotencyKey": "tenant-b-only",
	})
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
	resp, body := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body)
	if created.Lease.AccountID != "acc-02" {
		t.Fatalf("admin lease = %+v, want acc-02", created.Lease)
	}

	key := restrictedWriteKey(t, ts, []string{"acc-01"}, nil)
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/leases", map[string]any{
		"userId": "u-01", "platform": "ios", "accountId": "acc-01", "idempotencyKey": "tenant-b-only",
	})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)
	if bytes.Contains(body, []byte(created.Lease.ID)) || bytes.Contains(body, []byte("acc-02")) {
		t.Fatalf("refused replay leaked the foreign lease: %s", body)
	}
	if lease, ok := st.Lease(created.Lease.ID); !ok || lease.AccountID != "acc-02" {
		t.Fatalf("refused replay changed the lease: %+v (exists=%v)", lease, ok)
	}
}

// TestScopedKeyAdminWritesStillWork pins the compatibility side of AJS-448:
// admin tokens and admin-scoped keys (even when they carry a list) keep writing
// across every account, and an operator may still mutate another user's lease.
// An unbounded non-admin key cannot be issued any more, so there is no
// customer-side unrestricted write to preserve.
func TestScopedKeyAdminWritesStillWork(t *testing.T) {
	ts, st := newTestServer(t)

	resp, _ := apiCall(t, ts, http.MethodDelete, "/v1/leases/lease-03", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/leases/lease-04", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/leases", map[string]any{
		"userId": "u-02", "platform": "ios", "accountId": "acc-02",
	})
	requireStatus(t, resp, http.StatusCreated)
	if created := decodeBody[leaseCreateResult](t, body); created.Lease.AccountID != "acc-02" {
		t.Fatalf("admin lease = %+v", created.Lease)
	}
	// Draining removes a serving slot, so it runs after the create.
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-05/actions/drain", nil)
	requireStatus(t, resp, http.StatusOK)

	adminKey := mustIssueScopedKey(t, ts, []string{"admin"}, []string{"acc-01"}, nil)
	resp, _ = scopedCall(t, ts, adminKey.Secret, http.MethodDelete, "/v1/leases/lease-05", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	resp, _ = scopedCall(t, ts, adminKey.Secret, http.MethodPost, "/v1/nodes/node-06/actions/drain", nil)
	requireStatus(t, resp, http.StatusOK)
	// The admin grant is not narrowed by the list it carries.
	resp, _ = scopedCall(t, ts, adminKey.Secret, http.MethodPatch, "/v1/accounts/acc-02",
		map[string]any{"label": "Field Lab (admin key)"})
	requireStatus(t, resp, http.StatusOK)
	if account, ok := st.Account("acc-02"); !ok || account.Label != "Field Lab (admin key)" {
		t.Fatalf("admin-key patch = %+v (exists=%v)", account, ok)
	}
}
