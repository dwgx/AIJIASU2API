package runtime_test

import (
	"context"
	"testing"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

type capturingReconciler struct {
	st     *store.Store
	live   []int
	lastAt []model.NodeState
}

func (r *capturingReconciler) Reconcile(context.Context) (runtime.Topology, error) {
	live := 0
	var states []model.NodeState
	for _, node := range r.st.Nodes() {
		states = append(states, node.State)
		if node.State == model.NodeLive {
			live++
		}
	}
	r.live = append(r.live, live)
	r.lastAt = states
	return runtime.Topology{}, nil
}

// TestOverlayStopAccountReconcilesBeforeDelete proves the account-delete seam
// republishes the still-LIVE nodes and never reconciles after the cascade.
func TestOverlayStopAccountReconcilesBeforeDelete(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "overlay-delete")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	rec := &capturingReconciler{st: st}
	controller := newNodeController(t, st, logins, newFakeSessionOpener(), rec)
	ctx := context.Background()

	if err := controller.SyncAccount(ctx, acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	if err := controller.StopAccount(ctx, acct.ID); err != nil {
		t.Fatalf("StopAccount: %v", err)
	}
	calls := len(rec.live)
	if got := rec.live[calls-1]; got != 1 {
		t.Fatalf("LIVE nodes at StopAccount's reconcile = %d, want 1 (the stopped node)", got)
	}
	if err := st.DeleteAccount(acct.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	t.Logf("reconciles=%d live-at-last-reconcile=%d nodes-after-delete=%d reconciles-after-delete=%d",
		calls, rec.live[calls-1], len(st.Nodes()), len(rec.live)-calls)
	if len(rec.live) != calls {
		t.Fatalf("a reconcile ran after the cascade: %d -> %d", calls, len(rec.live))
	}
}
