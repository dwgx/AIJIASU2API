package runtime_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/resin"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// The tests in this file are the AJS-364 reopen falsifiers of
// REVIEW-MULTISLOT-RESIDUALS findings 5 and 10:
//
//  5. a restart at zero LIVE nodes had no subscription id in memory, so the
//     empty-render Withdraw no-op'd and Resin kept advertising the previous
//     run's dead port until a login republished; and a metadata-only failed
//     switch revoked the local lease but never the external Resin lease;
// 10. the post-switch reconcile failure was log-only: no retained audit row
//     attributed to the slot/generation, and the failure result's warnings
//     never reached the caller.

// TestRestartEmptyRenderWithdrawsPersistedSubscription is the restart half of
// finding 5. A fresh panel over an empty store has no subscription id; the
// subscription the previous process published still exists in Resin. Start
// must discover it by name and delete it without any login or LIVE node.
func TestRestartEmptyRenderWithdrawsPersistedSubscription(t *testing.T) {
	st := store.NewEmpty()
	panel, fake, _ := newTestPanel(t, st)
	fake.subscription = &resin.Subscription{
		ID: "sub-persisted", Name: runtime.DefaultSubscriptionName,
		SourceType: "local", Content: "socks5://127.0.0.1:13000#dead-node\n", Enabled: true,
	}

	if err := panel.Start(context.Background()); err != nil {
		t.Fatalf("Start on an empty store: %v", err)
	}
	if sub := fake.subscriptionValue(); sub != nil {
		t.Fatalf("restart left the previous dead subscription advertised: %q", sub.Content)
	}
	if got := fake.countExact(http.MethodDelete, "/api/v1/subscriptions/sub-persisted"); got != 1 {
		t.Fatalf("subscription deletes = %d, want 1 (calls: %v)", got, fake.paths())
	}
	// The lookup is a GET; no create/patch/refresh may run without a login or
	// a LIVE node.
	for _, call := range fake.recorded() {
		if !strings.HasPrefix(call.Path, "/api/v1/subscriptions") {
			continue
		}
		if call.Method != http.MethodGet && call.Method != http.MethodDelete {
			t.Fatalf("empty restart wrote to Resin: %s %s", call.Method, call.Path)
		}
	}
	if topology, err := panel.Topology(context.Background()); err != nil {
		t.Fatalf("Topology: %v", err)
	} else if topology.SubscriptionID != "" || topology.Content != "" {
		t.Fatalf("topology after restart withdrawal = %+v, want empty", topology)
	}
}

// residualReconciler is a publication double. Reconcile fails when the test
// sets reconcileErr; WithdrawLease records the same-operation cleanup.
type residualReconciler struct {
	reconcileErr error
	withdrawErr  error
	reconciled   int
	withdrawn    []string
}

func (r *residualReconciler) Reconcile(context.Context) (runtime.Topology, error) {
	r.reconciled++
	return runtime.Topology{}, r.reconcileErr
}

func (r *residualReconciler) WithdrawLease(_ context.Context, accountID string) error {
	r.withdrawn = append(r.withdrawn, accountID)
	return r.withdrawErr
}

// TestFailedSwitchWithdrawsExternalLeaseAndRetainsReconcileLoss is the
// metadata-only half of findings 5 and 10. The old session is closed before
// the new open fails, so the same call must delete the account's external
// lease; the reconcile failure must leave an audit row naming the slot and
// generation and reach the caller's error details.
func TestFailedSwitchWithdrawsExternalLeaseAndRetainsReconcileLoss(t *testing.T) {
	rec := &residualReconciler{}
	f := newSwitchFixtureWith(t, func(f *switchFixture) { f.reconciler = rec })
	f.sync(t)
	oldNodeID := slot1NodeID(f.acct.ID, "s1")

	// Fail only the switch: the sync's reconcile already succeeded.
	rec.reconcileErr = errors.New("resin admin unreachable")
	f.opener.fail["s2"] = errors.New("vendor session refused")

	_, err := f.controller.ConnectServer(context.Background(), f.acct.ID, "s2", "", "manual")
	switchErr := requireSwitchReason(t, err, runtime.ReasonSessionOpen)
	if switchErr.Details["rollback"] != runtime.ReasonMetadataOnly {
		t.Fatalf("details = %v, want rollback %s", switchErr.Details, runtime.ReasonMetadataOnly)
	}
	if switchErr.Details["warnings"] != runtime.ReasonReconcileFailed {
		t.Fatalf("details warnings = %q, want %q", switchErr.Details["warnings"], runtime.ReasonReconcileFailed)
	}
	if switchErr.Details["slot"] != "1" || switchErr.Details["generation"] != "1" {
		t.Fatalf("details attribution = %v, want slot 1 generation 1", switchErr.Details)
	}
	if switchErr.Details["prevNodeId"] != oldNodeID {
		t.Fatalf("details prevNodeId = %q, want %q", switchErr.Details["prevNodeId"], oldNodeID)
	}
	if len(rec.withdrawn) != 1 || rec.withdrawn[0] != f.acct.ID {
		t.Fatalf("external lease withdrawals = %v, want [%s]", rec.withdrawn, f.acct.ID)
	}

	event := f.lastAudit(t, "catalog.reconcile_failed")
	if event.Meta["slot"] != "acc-01#1" || event.Meta["generation"] != "1" ||
		event.Meta["prevNodeId"] != oldNodeID || event.Meta["result"] != "failed" ||
		event.Meta["errorClass"] != "reconcile" {
		t.Fatalf("reconcile-loss audit = %+v, want attributed slot/generation/prevNodeId", event)
	}
	old := nodeByServer(t, f.st, f.acct.ID, "s1")
	if old.State != model.NodeDown || old.GatePort != 0 {
		t.Fatalf("closed node = %+v, want DOWN without a port", old)
	}
}
