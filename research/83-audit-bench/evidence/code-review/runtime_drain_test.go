package runtime_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsproxy/apifacade"
)

type drainErrGate struct{ fakeGate }

func (g *drainErrGate) Drain(context.Context) error { return errors.New("drain failed") }

// TestOverlayDrainErrorLeavesPanelStale proves DrainNode mutates the store
// before the gate and skips the reconcile when the gate refuses.
func TestOverlayDrainErrorLeavesPanelStale(t *testing.T) {
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "overlay-drain")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	rec := &fakeReconciler{}
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:      st,
		Logins:     logins,
		Sessions:   newFakeSessionOpener(),
		Gates:      func(gate.Config, gate.Session) (runtime.Gate, error) { return &drainErrGate{}, nil },
		Reconciler: rec,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() { _ = controller.Stop(context.Background()) })
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	node := nodeByServer(t, st, acct.ID, "s1")
	before := rec.callCount()

	err = controller.DrainNode(context.Background(), node.ID)
	after, _ := st.Node(node.ID)
	t.Logf("DrainNode err=%v store state=%q reconciles before=%d after=%d", err, after.State, before, rec.callCount())
	if err == nil {
		t.Fatal("DrainNode returned nil despite the gate failure")
	}
	if after.State != model.NodeDraining {
		t.Fatalf("store state = %q, want DRAINING", after.State)
	}
	if rec.callCount() != before {
		t.Fatalf("reconciles after the failed drain = %d, want %d", rec.callCount(), before)
	}
}
