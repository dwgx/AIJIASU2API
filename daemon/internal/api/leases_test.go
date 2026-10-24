package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// leaseHTTPFixture is an empty store with one active account and two free
// nodes, served through the real router.
type leaseHTTPFixture struct {
	ts      *httptest.Server
	st      *store.Store
	account model.Account
	user    model.PanelUser
}

func newLeaseHTTPServer(t *testing.T, cfg Config) leaseHTTPFixture {
	t.Helper()
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "Northwind Ops",
		Username:    "demo-northwind",
		Password:    "local-placeholder",
		Platform:    model.PlatformAndroid,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	for _, node := range []model.ProxyNode{
		{
			ID: "node-live", AccountID: account.ID, Label: "cn-sh-01", Region: "cn-sh",
			State: model.NodeLive, EgressIP: "203.0.113.12", Protocol: model.ProtocolSOCKS5, GatePort: 13001,
		},
		{
			ID: "node-ready", AccountID: account.ID, Label: "cn-zj-01", Region: "cn-zj",
			State: model.NodeReady, EgressIP: "203.0.113.13", Protocol: model.ProtocolSOCKS5, GatePort: 13002,
		},
	} {
		if _, err := st.PutNode(node); err != nil {
			t.Fatalf("PutNode(%q): %v", node.ID, err)
		}
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "ada.ops"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cfg.AdminToken = testToken
	server := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return leaseHTTPFixture{ts: ts, st: st, account: account, user: user}
}

func (f leaseHTTPFixture) addUser(t *testing.T, name string, quota *int64) model.PanelUser {
	t.Helper()
	user, err := f.st.CreateUser(model.CreateUserInput{Name: name, QuotaBytesMonth: quota})
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", name, err)
	}
	return user
}

func createLeaseCall(t *testing.T, ts *httptest.Server, body map[string]any, headerKey string) (*http.Response, []byte) {
	t.Helper()
	req := newRequest(t, http.MethodPost, ts.URL+"/v1/leases", body)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if headerKey != "" {
		req.Header.Set("Idempotency-Key", headerKey)
	}
	return doRequest(t, ts, req)
}

func requireLeaseReason(t *testing.T, body []byte, want string) {
	t.Helper()
	var payload errorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	if payload.Error.Reason != want {
		t.Fatalf("error reason = %q, want %q (body %s)", payload.Error.Reason, want, body)
	}
}

// TestCreateLeaseHTTPIdempotencyAndConflict proves the retry contract over the
// wire: the same Idempotency-Key (body or header) returns the original lease,
// a second key for the same user and platform is 409 lease_conflict, and a
// mismatched header/body pair is 400.
func TestCreateLeaseHTTPIdempotencyAndConflict(t *testing.T) {
	fixture := newLeaseHTTPServer(t, Config{})
	ts := fixture.ts

	resp, body := createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android", "idempotencyKey": "retry-1",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body)
	if created.Replayed {
		t.Fatal("first create reported a replay")
	}
	if created.Lease.ID != "lease-01" || created.Lease.NodeID != "node-live" ||
		created.Lease.Status != model.LeaseActive || created.Lease.Platform != model.PlatformAndroid {
		t.Fatalf("created lease = %+v", created.Lease)
	}

	resp, body = createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android", "idempotencyKey": "retry-1",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	replayed := decodeBody[leaseCreateResult](t, body)
	if !replayed.Replayed || replayed.Lease.ID != created.Lease.ID {
		t.Fatalf("replay = %+v, want %s", replayed, created.Lease.ID)
	}
	if resp.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("replay did not set Idempotency-Replayed")
	}
	if got := len(fixture.st.Leases()); got != 1 {
		t.Fatalf("leases after replay = %d, want 1", got)
	}

	resp, body = createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android", "idempotencyKey": "retry-2",
	}, "")
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)
	requireLeaseReason(t, body, "lease_conflict")

	// The header is the documented retry channel; a second user exercises it
	// without colliding with the first user's platform lease.
	second := fixture.addUser(t, "build-bot", nil)
	resp, body = createLeaseCall(t, ts, map[string]any{
		"userId": second.ID, "platform": "android",
	}, "header-1")
	requireStatus(t, resp, http.StatusCreated)
	headerCreated := decodeBody[leaseCreateResult](t, body)
	resp, body = createLeaseCall(t, ts, map[string]any{
		"userId": second.ID, "platform": "android",
	}, "header-1")
	requireStatus(t, resp, http.StatusCreated)
	if headerReplay := decodeBody[leaseCreateResult](t, body); !headerReplay.Replayed ||
		headerReplay.Lease.ID != headerCreated.Lease.ID {
		t.Fatalf("header replay = %+v, want %s", headerReplay, headerCreated.Lease.ID)
	}

	resp, body = createLeaseCall(t, ts, map[string]any{
		"userId": second.ID, "platform": "android", "idempotencyKey": "body-2",
	}, "header-2")
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
}

// TestCreateLeaseHTTPQuotaAndConcurrency proves both 429 guards keep their
// machine reasons, so the dashboard can explain the refusal.
func TestCreateLeaseHTTPQuotaAndConcurrency(t *testing.T) {
	fixture := newLeaseHTTPServer(t, Config{})
	ts := fixture.ts

	quotaUser := fixture.addUser(t, "quota-zero", func() *int64 { v := int64(0); return &v }())
	resp, body := createLeaseCall(t, ts, map[string]any{
		"userId": quotaUser.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeQuotaExceeded)
	requireLeaseReason(t, body, "quota_exceeded")

	// Shrink the account to one active lease: the fixture user takes it, the
	// next user of the same platform is refused.
	if _, err := fixture.st.UpdateAccount(fixture.account.ID, model.AccountPatch{
		Concurrency: func() *int { v := 1; return &v }(),
	}); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	resp, body = createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusCreated)

	second := fixture.addUser(t, "build-bot", nil)
	resp, body = createLeaseCall(t, ts, map[string]any{
		"userId": second.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeQuotaExceeded)
	requireLeaseReason(t, body, "concurrency_exceeded")
}

// TestCreateLeaseValidation covers the 400/404/409 refusals that are not
// capacity related.
func TestCreateLeaseValidation(t *testing.T) {
	fixture := newLeaseHTTPServer(t, Config{})
	ts := fixture.ts

	cases := []struct {
		name   string
		body   map[string]any
		status int
		code   string
		reason string
	}{
		{
			name:   "unknown user",
			body:   map[string]any{"userId": "u-404", "platform": "android"},
			status: http.StatusNotFound,
			code:   CodeNotFound,
			reason: "user_not_found",
		},
		{
			name:   "invalid platform",
			body:   map[string]any{"userId": fixture.user.ID, "platform": "solaris"},
			status: http.StatusBadRequest,
			code:   CodeInvalidArgument,
			reason: "invalid_lease",
		},
		{
			name:   "short ttl",
			body:   map[string]any{"userId": fixture.user.ID, "platform": "android", "ttlSeconds": 1},
			status: http.StatusBadRequest,
			code:   CodeInvalidArgument,
			reason: "invalid_lease",
		},
		{
			name:   "pinned account platform mismatch",
			body:   map[string]any{"userId": fixture.user.ID, "platform": "ios", "accountId": fixture.account.ID},
			status: http.StatusBadRequest,
			code:   CodeInvalidArgument,
			reason: "invalid_lease",
		},
		{
			name:   "unknown account",
			body:   map[string]any{"userId": fixture.user.ID, "platform": "android", "accountId": "acc-404"},
			status: http.StatusNotFound,
			code:   CodeNotFound,
			reason: "account_not_found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := createLeaseCall(t, ts, tc.body, "")
			requireStatus(t, resp, tc.status)
			requireErrorCode(t, body, tc.code)
			requireLeaseReason(t, body, tc.reason)
		})
	}
	if got := len(fixture.st.Leases()); got != 0 {
		t.Fatalf("refused creates wrote %d leases, want 0", got)
	}
}

// TestLeasePublishHookWarningDoesNotRollBack proves the Resin seam: a hook
// failure is a warning on a committed lease, never a failed create or a
// rolled-back revoke.
func TestLeasePublishHookWarningDoesNotRollBack(t *testing.T) {
	var actions []LeaseAction
	fixture := newLeaseHTTPServer(t, Config{
		LeasePublishHook: func(_ context.Context, action LeaseAction, lease model.Lease) error {
			actions = append(actions, action)
			if lease.ID == "" {
				t.Errorf("hook received an empty lease for %s", action)
			}
			return errors.New("resin is unreachable")
		},
	})
	ts := fixture.ts

	resp, body := createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body)
	if !strings.Contains(created.Warning, "publish hook failed") {
		t.Fatalf("warning = %q, want the publish-hook wording", created.Warning)
	}
	if leases := fixture.st.Leases(); len(leases) != 1 || leases[0].ID != created.Lease.ID {
		t.Fatalf("lease was rolled back after a hook failure: %+v", leases)
	}

	resp, body = apiCall(t, ts, http.MethodDelete, "/v1/leases/"+created.Lease.ID,
		map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	if len(body) != 0 {
		t.Fatalf("revoke body = %q, want empty", body)
	}
	if warning := resp.Header.Get("Warning"); !strings.Contains(warning, "publish hook failed") {
		t.Fatalf("Warning header = %q, want the publish-hook wording", warning)
	}
	if len(fixture.st.Leases()) != 0 {
		t.Fatal("revoke did not remove the lease")
	}
	if len(actions) != 2 || actions[0] != LeaseActionCreated || actions[1] != LeaseActionRevoked {
		t.Fatalf("hook actions = %v, want created then revoked", actions)
	}
}

// TestRevokeLeaseRequiresConfirm pins the destructive contract: no body, a
// false flag and an unknown field are 400; a confirmed revoke is 204 and the
// row disappears; a repeated revoke is 404.
func TestRevokeLeaseRequiresConfirm(t *testing.T) {
	fixture := newLeaseHTTPServer(t, Config{})
	ts := fixture.ts

	resp, body := createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body)

	resp, body = apiCall(t, ts, http.MethodDelete, "/v1/leases/"+created.Lease.ID, nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodDelete, "/v1/leases/"+created.Lease.ID,
		map[string]any{"confirm": false})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodDelete, "/v1/leases/"+created.Lease.ID,
		map[string]any{"confirm": true, "typo": 1})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	if leases := fixture.st.Leases(); len(leases) != 1 {
		t.Fatalf("lease disappeared before a confirmed revoke: %+v", leases)
	}
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/leases/"+created.Lease.ID,
		map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	if leases := fixture.st.Leases(); len(leases) != 0 {
		t.Fatalf("leases after revoke = %+v, want none", leases)
	}
	node, _ := fixture.st.Node(created.Lease.NodeID)
	if node.InUse != 0 {
		t.Fatalf("node inUse after revoke = %d, want 0", node.InUse)
	}

	resp, body = apiCall(t, ts, http.MethodDelete, "/v1/leases/"+created.Lease.ID,
		map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

// TestLeaseScopes pins the least-privilege mapping: leases:read lists, while
// create and revoke need leases:write.
func TestLeaseScopes(t *testing.T) {
	fixture := newLeaseHTTPServer(t, Config{})
	ts := fixture.ts
	readKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": fixture.user.ID,
		"label":  "lease read",
		"scopes": []string{"leases:read"},
	})
	resp, body := scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/leases", nil)
	requireStatus(t, resp, http.StatusOK)
	if leases := decodeBody[[]model.Lease](t, body); len(leases) != 0 {
		t.Fatalf("empty store listed leases: %+v", leases)
	}
	resp, body = scopedCall(t, ts, readKey.Secret, http.MethodPost, "/v1/leases",
		map[string]any{"userId": fixture.user.ID, "platform": "android"})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)

	writeKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": fixture.user.ID,
		"label":  "lease write",
		"scopes": []string{"leases:read", "leases:write"},
	})
	resp, body = scopedCall(t, ts, writeKey.Secret, http.MethodPost, "/v1/leases",
		map[string]any{"userId": fixture.user.ID, "platform": "android"})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body)
	resp, _ = scopedCall(t, ts, writeKey.Secret, http.MethodDelete,
		"/v1/leases/"+created.Lease.ID, map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
}
