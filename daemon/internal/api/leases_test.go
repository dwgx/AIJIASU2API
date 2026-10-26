package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
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
	// The shared fixture exercises allocation, not the default one-slot
	// guard; individual slot tests set AJS_ACCOUNT_SLOTS after construction.
	t.Setenv("AJS_ACCOUNT_SLOTS", "8")
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
	if _, err := st.SetAccountUseCount(account.ID, 8); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	for _, node := range []model.ProxyNode{
		{
			ID: "node-live", AccountID: account.ID, Label: "cn-sh-01", Region: "cn-sh",
			State: model.NodeLive, EgressIP: "203.0.113.12", Protocol: model.ProtocolSOCKS5, GatePort: 13001,
		},
		{
			ID: "node-live-2", AccountID: account.ID, Label: "cn-zj-01", Region: "cn-zj",
			State: model.NodeLive, EgressIP: "203.0.113.13", Protocol: model.ProtocolSOCKS5, GatePort: 13002,
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
	req.Header.Set(adminCSRFHeader, adminCSRFValue)
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
	// without colliding with the first user's platform lease. Admission is
	// one lease per account, so the second user needs a second account.
	secondAccount, err := fixture.st.CreateAccount(model.CreateAccountInput{
		Label: "Build Ops", Username: "build-ops", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("CreateAccount(build): %v", err)
	}
	if _, err := fixture.st.PutNode(model.ProxyNode{
		ID: "node-build", AccountID: secondAccount.ID, Label: "cn-zj-02", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.14", Protocol: model.ProtocolSOCKS5, GatePort: 13003,
	}); err != nil {
		t.Fatalf("PutNode(node-build): %v", err)
	}
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

// leaseEventSink records the webhook events one test server emitted.
type leaseEventSink struct {
	mu     sync.Mutex
	events []webhook.EventType
}

func (s *leaseEventSink) Emit(event webhook.EventType, _ map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *leaseEventSink) recorded() []webhook.EventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]webhook.EventType(nil), s.events...)
}

// findAuditAction returns the first audit row with this action.
func findAuditAction(rows []model.AuditEvent, action string) (model.AuditEvent, bool) {
	for _, row := range rows {
		if row.Action == action {
			return row, true
		}
	}
	return model.AuditEvent{}, false
}

// TestCreateLeasePublishFailureRollsBack is acceptance row 22: create is
// fail-closed on publication. A hook failure is not a warning — the lease row
// is rolled back, the node's slot is free, one error audit row names the
// reason, no lease.created event is emitted, and the retry allocates again
// instead of replaying a lease the caller never received.
func TestCreateLeasePublishFailureRollsBack(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	sink := &leaseEventSink{}
	fixture := newLeaseHTTPServer(t, Config{
		Events: sink,
		LeasePublishHook: func(_ context.Context, action LeaseAction, lease model.Lease) error {
			if action != LeaseActionCreated {
				return nil
			}
			if lease.ID == "" {
				t.Errorf("hook received an empty lease")
			}
			if fail.Load() {
				return errors.New("resin is unreachable")
			}
			return nil
		},
	})
	ts := fixture.ts

	resp, body := createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android", "idempotencyKey": "publish-1",
	}, "")
	requireStatus(t, resp, http.StatusServiceUnavailable)
	requireErrorCode(t, body, CodeUpstreamUnavailable)
	requireLeaseReason(t, body, "lease_publish_failed")
	if strings.Contains(string(body), "resin is unreachable") {
		t.Fatalf("error body %s leaked the publisher cause", body)
	}

	if leases := fixture.st.Leases(); len(leases) != 0 {
		t.Fatalf("leases after a failed publication = %+v, want none", leases)
	}
	node, ok := fixture.st.Node("node-live")
	if !ok {
		t.Fatal("node-live missing")
	}
	if node.InUse != 0 {
		t.Fatalf("node-live inUse = %d, want 0 after rollback", node.InUse)
	}
	row, ok := findAuditAction(fixture.st.Audit(50), "lease.publish_failed")
	if !ok {
		t.Fatal("no lease.publish_failed audit row")
	}
	if row.Severity != model.SeverityError || row.Meta["leaseId"] == "" || row.Meta["reason"] == "" {
		t.Fatalf("audit row = %+v, want error severity with leaseId and reason", row)
	}
	if events := sink.recorded(); len(events) != 0 {
		t.Fatalf("events after a failed publication = %v, want none", events)
	}

	// The retry allocates again: the failed create left no idempotency record,
	// so the same key is not a replay of a lease the caller never received.
	fail.Store(false)
	resp, body = createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android", "idempotencyKey": "publish-1",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body)
	if created.Replayed {
		t.Fatal("the retry replayed the rolled-back lease")
	}
	if len(fixture.st.Leases()) != 1 {
		t.Fatalf("leases after the retry = %d, want 1", len(fixture.st.Leases()))
	}
	if events := sink.recorded(); len(events) != 1 || events[0] != webhook.EventLeaseCreated {
		t.Fatalf("events after the retry = %v, want one lease.created", events)
	}
}

// leasePanel is the minimal panel double for the create path: only
// PublishLease is implemented, so any other panel call panics and fails the
// test.
type leasePanel struct {
	runtime.Panel
	publishes   int
	leaseIDs    []string
	err         error
	withdrawals int
	withdrawErr error
}

func (p *leasePanel) PublishLease(_ context.Context, lease model.Lease) error {
	p.publishes++
	p.leaseIDs = append(p.leaseIDs, lease.ID)
	return p.err
}

func (p *leasePanel) WithdrawLease(_ context.Context, accountID string) error {
	p.withdrawals++
	return p.withdrawErr
}

// TestCreateLeasePanelPublishFailureRollsBack proves the Resin path uses the
// same fail-closed contract: a publish failure is 503, a panel refusal is 409,
// both carry lease_publish_failed, and neither leaves a row behind. A healthy
// publish answers 201 with no warning.
func TestCreateLeasePanelPublishFailureRollsBack(t *testing.T) {
	panel := &leasePanel{err: errors.New("resin: publish subscription: 502")}
	fixture := newLeaseHTTPServer(t, Config{Panel: panel})

	resp, body := createLeaseCall(t, fixture.ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusServiceUnavailable)
	requireErrorCode(t, body, CodeUpstreamUnavailable)
	requireLeaseReason(t, body, "lease_publish_failed")
	if panel.publishes != 1 || panel.leaseIDs[0] == "" {
		t.Fatalf("panel publishes = %v, want one publish of a named lease", panel.leaseIDs)
	}
	if leases := fixture.st.Leases(); len(leases) != 0 {
		t.Fatalf("leases after a failed panel publish = %+v, want none", leases)
	}

	// A panel conflict (no platform yet, a node that is not published) keeps
	// the CONFLICT code and the same reason.
	panel.err = fmt.Errorf("%w: node node-live is not in the published topology", store.ErrConflict)
	resp, body = createLeaseCall(t, fixture.ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)
	requireLeaseReason(t, body, "lease_publish_failed")
	if leases := fixture.st.Leases(); len(leases) != 0 {
		t.Fatalf("leases after a panel conflict = %+v, want none", leases)
	}

	panel.err = nil
	resp, body = createLeaseCall(t, fixture.ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	if created := decodeBody[leaseCreateResult](t, body); created.Lease.ID == "" {
		t.Fatalf("created lease = %+v, want an id", created.Lease)
	}
	if len(fixture.st.Leases()) != 1 {
		t.Fatalf("leases after a healthy publish = %d, want 1", len(fixture.st.Leases()))
	}
}

// TestRevokeLeasePublishHookWarns pins the revoke half of the seam: the row is
// already gone, so a hook failure is reported in the Warning header and the
// revoke still answers 204.
func TestRevokeLeasePublishHookWarns(t *testing.T) {
	var actions []LeaseAction
	fixture := newLeaseHTTPServer(t, Config{
		LeasePublishHook: func(_ context.Context, action LeaseAction, lease model.Lease) error {
			actions = append(actions, action)
			if action == LeaseActionRevoked {
				return errors.New("resin is unreachable")
			}
			return nil
		},
	})
	ts := fixture.ts

	resp, body := createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body)
	if strings.Contains(created.Warning, "publish hook") {
		t.Fatalf("create warning = %q, want no publish-hook warning", created.Warning)
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
		"userId":     fixture.user.ID,
		"label":      "lease read",
		"scopes":     []string{"leases:read"},
		"accountIds": []string{fixture.account.ID},
	})
	resp, body := scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/leases", nil)
	requireStatus(t, resp, http.StatusOK)
	if leases := decodeBody[[]model.Lease](t, body); len(leases) != 0 {
		t.Fatalf("empty store listed leases: %+v", leases)
	}
	resp, body = scopedCall(t, ts, readKey.Secret, http.MethodPost, "/v1/leases",
		map[string]any{"userId": fixture.user.ID, "platform": "android", "accountId": fixture.account.ID})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)

	writeKey, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId":     fixture.user.ID,
		"label":      "lease write",
		"scopes":     []string{"leases:read", "leases:write"},
		"accountIds": []string{fixture.account.ID},
	})
	resp, body = scopedCall(t, ts, writeKey.Secret, http.MethodPost, "/v1/leases",
		map[string]any{"userId": fixture.user.ID, "platform": "android", "accountId": fixture.account.ID})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body)

	// leases:read reaches the detail route; the read key sees the same lease.
	resp, body = scopedCall(t, ts, readKey.Secret, http.MethodGet, "/v1/leases/"+created.Lease.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.LeaseDetail](t, body); got.Lease.ID != created.Lease.ID ||
		got.Connection.Username != fixture.user.ID {
		t.Fatalf("lease detail = %+v, want %s for %s", got, created.Lease.ID, fixture.user.ID)
	}

	resp, _ = scopedCall(t, ts, writeKey.Secret, http.MethodDelete,
		"/v1/leases/"+created.Lease.ID, map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
}

// recordedEvents copies the sink's events under its lock; the handlers emit
// synchronously, but -race still sees a helper that never races.
func recordedEvents(sink *recordingSink) []recordedEvent {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]recordedEvent(nil), sink.events...)
}

// TestLeaseLifecycleEmitsWebhookEvents pins the emission matrix: a committed
// create emits lease.created once even when the same Idempotency-Key replays,
// a refused create emits nothing, a confirmed revoke emits lease.revoked once
// and a repeated revoke (404) emits nothing. Every payload carries the five
// contract fields and survives the scrubber unchanged.
func TestLeaseLifecycleEmitsWebhookEvents(t *testing.T) {
	sink := &recordingSink{}
	fixture := newLeaseHTTPServer(t, Config{Events: sink})
	ts := fixture.ts

	resp, body := createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android", "idempotencyKey": "emit-1",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body)

	resp, body = createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android", "idempotencyKey": "emit-1",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	if !decodeBody[leaseCreateResult](t, body).Replayed {
		t.Fatal("the second create with the same Idempotency-Key did not replay")
	}

	wantData := map[string]any{
		"leaseId":   created.Lease.ID,
		"userId":    fixture.user.ID,
		"platform":  string(model.PlatformAndroid),
		"accountId": fixture.account.ID,
		"expiresAt": created.Lease.ExpiresAt,
	}
	events := recordedEvents(sink)
	if len(events) != 1 || events[0].event != webhook.EventLeaseCreated {
		t.Fatalf("events after create+replay = %+v, want exactly one lease.created", events)
	}
	if !reflect.DeepEqual(events[0].data, wantData) {
		t.Fatalf("lease.created payload = %#v, want %#v", events[0].data, wantData)
	}
	if scrubbed := webhook.Scrub(events[0].data); !reflect.DeepEqual(scrubbed, wantData) {
		t.Fatalf("lease.created payload was redacted: %#v", scrubbed)
	}

	// A second active lease for the same user and platform is a 409 refusal;
	// the failed create must not fan out an event.
	resp, body = createLeaseCall(t, ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusConflict)
	requireLeaseReason(t, body, "lease_conflict")
	if got := len(recordedEvents(sink)); got != 1 {
		t.Fatalf("a refused create emitted an event: %d events", got)
	}

	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/leases/"+created.Lease.ID,
		map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/leases/"+created.Lease.ID,
		map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNotFound)

	events = recordedEvents(sink)
	if len(events) != 2 || events[1].event != webhook.EventLeaseRevoked {
		t.Fatalf("events after revoke = %+v, want one lease.revoked", events)
	}
	if !reflect.DeepEqual(events[1].data, wantData) {
		t.Fatalf("lease.revoked payload = %#v, want %#v", events[1].data, wantData)
	}
}

// TestGetLeaseConnection pins the lease detail contract: the owner reads its
// lease plus the loopback gate it currently points at, a foreign id and an
// unknown id are the same 404, an account outside the grant is 403, the port
// follows a switch, and the scoped-key secret never appears in the response.
func TestGetLeaseConnection(t *testing.T) {
	ts, _ := newTestServer(t)
	key := mustIssueScopedKey(t, ts, []string{"leases:read"}, []string{"acc-01"}, nil)

	resp, body := scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/leases/lease-01", nil)
	requireStatus(t, resp, http.StatusOK)
	detail := decodeBody[model.LeaseDetail](t, body)
	if detail.Lease.ID != "lease-01" || detail.Lease.UserID != "u-01" {
		t.Fatalf("lease = %+v, want lease-01 for u-01", detail.Lease)
	}
	want := model.LeaseConnection{
		Host: "127.0.0.1", Port: 13000, Protocol: model.ProtocolSOCKS5,
		Username: "u-01", CredentialKind: model.LeaseCredentialKindScopedKey,
	}
	if detail.Connection != want {
		t.Fatalf("connection = %+v, want %+v", detail.Connection, want)
	}
	if strings.Contains(string(body), key.Secret) {
		t.Fatalf("lease detail returned the scoped-key secret: %s", body)
	}
	if strings.Contains(strings.ToLower(string(body)), "secret") || strings.Contains(string(body), "hash") {
		t.Fatalf("lease detail carries credential material: %s", body)
	}

	// Another user's lease on the same granted account and an unknown id are
	// the same 404, so the id cannot be probed.
	for _, id := range []string{"lease-06", "lease-missing"} {
		resp, body = scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/leases/"+id, nil)
		requireStatus(t, resp, http.StatusNotFound)
		requireErrorCode(t, body, CodeNotFound)
	}
	// The key's own lease on an account outside its grant is 403.
	resp, body = scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/leases/lease-02", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)

	// An operator keeps the global contract.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/leases/lease-06", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[model.LeaseDetail](t, body); got.Lease.ID != "lease-06" {
		t.Fatalf("admin lease = %+v, want lease-06", got.Lease)
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/leases/lease-missing", nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)

	// A switch rebinds the lease row, so the detail read must report the new
	// gate rather than the port it was created with.
	t.Run("port follows a switch", func(t *testing.T) {
		st := store.NewEmpty()
		account, err := st.CreateAccount(model.CreateAccountInput{
			Label: "Switch Ops", Username: "switch-ops", Password: "local-placeholder",
			Platform: model.PlatformAndroid, Concurrency: 2,
		})
		if err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		if _, err := st.SetAccountUseCount(account.ID, 2); err != nil {
			t.Fatalf("SetAccountUseCount: %v", err)
		}
		slot := model.SlotKey{AccountID: account.ID, Index: 1}
		if err := st.UpsertSlot(model.AccountSlot{
			SlotKey: slot, ServerID: "s1", GatePort: 14000,
			State: model.SlotLive, Health: model.SlotHealthHealthy, Generation: 1,
		}); err != nil {
			t.Fatalf("UpsertSlot: %v", err)
		}
		if _, err := st.PutNode(model.ProxyNode{
			ID: slot.NodeID("s1"), AccountID: account.ID, Label: "switch-1", Region: "cn",
			State: model.NodeLive, GatePort: 14000, ServerID: "s1", Slot: 1,
			Capacity: 4, Protocol: model.ProtocolSOCKS5,
		}); err != nil {
			t.Fatalf("PutNode(s1): %v", err)
		}
		user, err := st.CreateUser(model.CreateUserInput{Name: "switch-user"})
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		lease, _, err := st.CreateLease(model.CreateLeaseInput{
			UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
		})
		if err != nil {
			t.Fatalf("CreateLease: %v", err)
		}
		server := New(Config{AdminToken: testToken}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)
		created, _ := mustCreateScopedKey(t, ts, map[string]any{
			"userId": user.ID, "label": "switch leases", "scopes": []string{"leases:read"},
			"accountIds": []string{account.ID},
		})

		read := func() model.LeaseDetail {
			t.Helper()
			resp, body := scopedCall(t, ts, created.Secret, http.MethodGet, "/v1/leases/"+lease.ID, nil)
			requireStatus(t, resp, http.StatusOK)
			return decodeBody[model.LeaseDetail](t, body)
		}
		if got := read(); got.Connection.Port != 14000 {
			t.Fatalf("pre-switch port = %d, want 14000", got.Connection.Port)
		}
		if _, _, err := st.CommitSwitch(store.SwitchCommit{
			Node: model.ProxyNode{
				ID: slot.NodeID("s2"), AccountID: account.ID, Label: "switch-2", Region: "cn",
				State: model.NodeLive, GatePort: 14001, ServerID: "s2", Slot: 1,
				Capacity: 4, Protocol: model.ProtocolSOCKS5,
			},
			Slot: slot,
			SlotState: &model.AccountSlot{
				SlotKey: slot, ServerID: "s2", GatePort: 14001,
				State: model.SlotLive, Health: model.SlotHealthHealthy, Generation: 2,
			},
			Actor: "operator", Action: "catalog.switch", Target: slot.NodeID("s2"),
			Detail: "Switched · one session live",
		}); err != nil {
			t.Fatalf("CommitSwitch: %v", err)
		}
		after := read()
		if after.Connection.Port != 14001 || after.Lease.NodeID != slot.NodeID("s2") {
			t.Fatalf("post-switch detail = %+v, want port 14001 on %s", after, slot.NodeID("s2"))
		}
	})
}

// rotatePanel is the minimal runtime.Panel double for the rotation path: it
// records the call and answers a rotated lease without touching the store, so
// the test can prove the API routed the request to the panel.
type rotatePanel struct {
	runtime.Panel
	calls   int
	leaseID string
}

func (p *rotatePanel) RotateLease(_ context.Context, leaseID string) (model.Lease, error) {
	p.calls++
	p.leaseID = leaseID
	return model.Lease{ID: leaseID, EgressIP: "198.51.100.7", Status: model.LeaseActive}, nil
}

// TestLeaseRotateRequiresPanel pins the honesty contract of
// POST /v1/leases/{id}/actions/rotate: a rotation is a Resin action, so a
// daemon without a configured panel answers 503 UPSTREAM_UNAVAILABLE before
// any mutation instead of rewriting the recorded egress IP and request
// counter. A configured panel is still called exactly once and its answer is
// served unchanged.
func TestLeaseRotateRequiresPanel(t *testing.T) {
	t.Run("no panel refuses before mutation", func(t *testing.T) {
		ts, st := newTestServer(t)
		before, ok := st.Lease("lease-01")
		if !ok {
			t.Fatal("seed lease-01 is missing")
		}
		nodeBefore, ok := st.Node(before.NodeID)
		if !ok {
			t.Fatalf("node %s is missing", before.NodeID)
		}
		auditBefore := len(st.Audit(1000))

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/leases/lease-01/actions/rotate", nil)
		requireStatus(t, resp, http.StatusServiceUnavailable)
		requireErrorCode(t, body, CodeUpstreamUnavailable)

		after, ok := st.Lease("lease-01")
		if !ok || after.EgressIP != before.EgressIP || after.Requests != before.Requests ||
			after.ExpiresAt != before.ExpiresAt || after.Status != before.Status {
			t.Fatalf("refused rotate changed the lease: %+v (exists=%v)", after, ok)
		}
		if nodeAfter, ok := st.Node(before.NodeID); !ok || nodeAfter.EgressIP != nodeBefore.EgressIP {
			t.Fatalf("refused rotate changed the node egress: %+v (exists=%v)", nodeAfter, ok)
		}
		if got := len(st.Audit(1000)); got != auditBefore {
			t.Fatalf("refused rotate wrote %d audit rows", got-auditBefore)
		}
	})

	t.Run("configured panel still rotates", func(t *testing.T) {
		st := store.New()
		panel := &rotatePanel{}
		server := New(Config{AdminToken: testToken, Panel: panel}, st,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		ts := httptest.NewServer(server)
		t.Cleanup(ts.Close)

		resp, body := apiCall(t, ts, http.MethodPost, "/v1/leases/lease-01/actions/rotate", nil)
		requireStatus(t, resp, http.StatusOK)
		if panel.calls != 1 || panel.leaseID != "lease-01" {
			t.Fatalf("panel rotations = %d for %q, want one for lease-01", panel.calls, panel.leaseID)
		}
		if rotated := decodeBody[model.Lease](t, body); rotated.ID != "lease-01" || rotated.EgressIP != "198.51.100.7" {
			t.Fatalf("panel answer = %+v, want the panel's rotated lease", rotated)
		}
	})
}
