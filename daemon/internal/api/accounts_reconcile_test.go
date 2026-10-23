package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// mutationPanel records the store state at every Reconcile. It embeds the rest
// of runtime.Panel as a nil interface: the account handlers must call only
// Reconcile, so any other panel call panics and fails the test.
type mutationPanel struct {
	runtime.Panel
	st        *store.Store
	accountID string
	err       error

	calls     int
	liveAt    []int
	accountAt []bool
	stateAt   []model.AccountState
}

func (p *mutationPanel) Reconcile(context.Context) (runtime.Topology, error) {
	p.calls++
	live := 0
	for _, node := range p.st.Nodes() {
		if node.AccountID == p.accountID && node.State == model.NodeLive {
			live++
		}
	}
	p.liveAt = append(p.liveAt, live)
	account, ok := p.st.Account(p.accountID)
	p.accountAt = append(p.accountAt, ok)
	state := model.AccountState("")
	if ok {
		state = account.State
	}
	p.stateAt = append(p.stateAt, state)
	return runtime.Topology{}, p.err
}

// TestAccountDeleteReconcilesAfterStoreMutation is the P1-3 regression: the
// pre-mutation hook only stops gates, and the panel reconcile must run after
// store.DeleteAccount so it never republishes the closed gates and does remove
// the account's tags.
func TestAccountDeleteReconcilesAfterStoreMutation(t *testing.T) {
	st := store.New()
	panel := &mutationPanel{st: st, accountID: "acc-01"}
	var hookSawAccount bool
	server := New(Config{
		AdminToken: testToken,
		Panel:      panel,
		AccountDeleteHook: func(_ context.Context, accountID string) error {
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
	if !hookSawAccount {
		t.Fatal("the delete hook did not run before the store mutation")
	}
	if panel.calls != 1 {
		t.Fatalf("reconciles after DELETE = %d, want 1", panel.calls)
	}
	if got := panel.liveAt[0]; got != 0 {
		t.Fatalf("LIVE nodes at the post-delete reconcile = %d, want 0", got)
	}
	if panel.accountAt[0] {
		t.Fatal("the reconcile ran before store.DeleteAccount removed the account")
	}
	for _, node := range st.Nodes() {
		if node.AccountID == "acc-01" {
			t.Fatalf("node %s survived the delete", node.ID)
		}
	}
}

// TestAccountLogoutReconcilesAfterStoreMutation pins the same ordering for
// logout: the reconcile runs once store.LogoutAccount has paused the account
// and drained its nodes, so the panel publishes zero LIVE nodes.
func TestAccountLogoutReconcilesAfterStoreMutation(t *testing.T) {
	st := store.New()
	panel := &mutationPanel{st: st, accountID: "acc-01"}
	server := New(Config{
		AdminToken: testToken,
		Panel:      panel,
		AccountLogoutHook: func(context.Context, string) error {
			return nil
		},
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/acc-01/actions/logout", nil)
	requireStatus(t, resp, http.StatusOK)
	account := decodeBody[model.Account](t, body)
	if account.State != model.AccountPaused || account.LiveNodeCount != 0 {
		t.Fatalf("logout account = %+v, want paused with 0 live nodes", account)
	}
	if panel.calls != 1 {
		t.Fatalf("reconciles after logout = %d, want 1", panel.calls)
	}
	if got := panel.liveAt[0]; got != 0 {
		t.Fatalf("LIVE nodes at the post-logout reconcile = %d, want 0", got)
	}
	if !panel.accountAt[0] || panel.stateAt[0] != model.AccountPaused {
		t.Fatalf("state at the reconcile = (present %v, %q), want (true, paused)",
			panel.accountAt[0], panel.stateAt[0])
	}
	for _, node := range st.Nodes() {
		if node.AccountID == "acc-01" && node.State == model.NodeLive {
			t.Fatalf("node %s is still LIVE after logout", node.ID)
		}
	}
}

// TestAccountMutationReconcileFailureIsLogged pins the best-effort contract:
// the store mutation already succeeded, so a panel failure is logged and the
// response stays successful.
func TestAccountMutationReconcileFailureIsLogged(t *testing.T) {
	st := store.New()
	var logs bytes.Buffer
	panel := &mutationPanel{st: st, accountID: "acc-01", err: errors.New("resin unavailable")}
	server := New(Config{
		AdminToken: testToken,
		Panel:      panel,
		AccountDeleteHook: func(context.Context, string) error {
			return nil
		},
	}, st, slog.New(slog.NewTextHandler(&logs, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, body := apiCall(t, ts, http.MethodDelete, "/v1/accounts/acc-01", nil)
	requireStatus(t, resp, http.StatusNoContent)
	if len(body) != 0 {
		t.Fatalf("DELETE body = %q, want empty", body)
	}
	if panel.calls != 1 {
		t.Fatalf("reconciles = %d, want 1", panel.calls)
	}
	if _, ok := st.Account("acc-01"); ok {
		t.Fatal("the store delete was rolled back by the panel failure")
	}
	if !strings.Contains(logs.String(), "account_reconcile_failed") {
		t.Fatalf("logs = %q, want the reconcile failure", logs.String())
	}
}
