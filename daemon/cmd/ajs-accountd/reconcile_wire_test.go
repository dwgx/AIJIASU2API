package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/publisher"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

func TestResinReconcileInterval(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "unset", raw: "", want: 5 * time.Minute},
		{name: "duration", raw: "90s", want: 90 * time.Second},
		{name: "minutes", raw: "2m", want: 2 * time.Minute},
		{name: "bare seconds", raw: "3600", want: time.Hour},
		{name: "zero", raw: "0", wantErr: true},
		{name: "negative", raw: "-1m", wantErr: true},
		{name: "garbage", raw: "soon", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AJS_RESIN_RECONCILE_INTERVAL", tc.raw)
			got, err := resinReconcileInterval()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resinReconcileInterval(%q) = %s, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resinReconcileInterval(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("resinReconcileInterval(%q) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

// TestDesiredResinStateTracksLiveNodes pins the hash input: only LIVE nodes
// reach Resin, the node label is the published tag, and demoting a node
// changes the digest so the next tick reconciles.
func TestDesiredResinStateTracksLiveNodes(t *testing.T) {
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:    "Reconcile",
		Username: "user",
		Password: "secret",
		Platform: model.PlatformLinux,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	for _, node := range []model.ProxyNode{
		{ID: "node-live", AccountID: account.ID, Label: "us-ca-01", Region: "us", State: model.NodeLive, GatePort: 13001},
		{ID: "node-draining", AccountID: account.ID, Label: "us-ca-02", Region: "us", State: model.NodeDraining, GatePort: 13002},
	} {
		if _, err := st.PutNode(node); err != nil {
			t.Fatalf("PutNode(%s): %v", node.ID, err)
		}
	}

	state := desiredResinState(st)
	if len(state.Nodes) != 1 {
		t.Fatalf("nodes = %+v, want only the LIVE node", state.Nodes)
	}
	if state.Nodes[0].Tag != "us-ca-01" || state.Nodes[0].GatePort != 13001 {
		t.Fatalf("node = %+v, want the label and gate port", state.Nodes[0])
	}
	if len(state.Accounts) != 1 || state.Accounts[0] != account.ID {
		t.Fatalf("accounts = %v, want [%s]", state.Accounts, account.ID)
	}
	if len(state.Leases) != 0 {
		t.Fatalf("leases = %v, want none", state.Leases)
	}

	before := state.Hash()
	if _, err := st.DrainNode("node-live"); err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if after := desiredResinState(st).Hash(); after == before {
		t.Fatal("demoting the LIVE node did not change the desired-state hash")
	}
}

func TestResinReconcileAuditRows(t *testing.T) {
	st := store.NewEmpty()
	audit := resinReconcileAudit(st)
	audit(publisher.AuditReconcileFailed, "error=boom retry_in=1m0s")
	audit(publisher.AuditReconcileRecovered, "reconcile succeeded after 1 failed attempt(s)")

	rows := st.Audit(10)
	if len(rows) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(rows))
	}
	if rows[0].Action != publisher.AuditReconcileRecovered || rows[0].Severity != model.SeverityInfo {
		t.Fatalf("newest row = %+v, want the info recovery", rows[0])
	}
	if rows[1].Action != publisher.AuditReconcileFailed || rows[1].Severity != model.SeverityWarn {
		t.Fatalf("oldest row = %+v, want the warn failure", rows[1])
	}
	for _, row := range rows {
		if row.Actor != "system" || row.Target != "resin" {
			t.Fatalf("row actor/target = %q/%q, want system/resin", row.Actor, row.Target)
		}
		if row.Detail == "" {
			t.Fatal("audit row has an empty detail")
		}
	}
}

func TestStartResinReconcilerDisabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewEmpty()

	t.Setenv("AJS_RESIN_RECONCILE", "")
	stop, err := startResinReconciler(context.Background(), st, nil, logger)
	if err != nil || stop != nil {
		t.Fatalf("default config: stop=%v err=%v, want nil/nil", stop != nil, err)
	}

	t.Setenv("AJS_RESIN_RECONCILE", "1")
	stop, err = startResinReconciler(context.Background(), st, nil, logger)
	if err != nil || stop != nil {
		t.Fatalf("enabled without a panel: stop=%v err=%v, want nil/nil", stop != nil, err)
	}
}

// fakeReconcilePanel implements api.Panel; only Reconcile is meaningful.
type fakeReconcilePanel struct {
	once       sync.Once
	reconciled chan struct{}
}

func (p *fakeReconcilePanel) Topology(context.Context) (runtime.Topology, error) {
	return runtime.Topology{}, nil
}

func (p *fakeReconcilePanel) Reconcile(context.Context) (runtime.Topology, error) {
	p.once.Do(func() { close(p.reconciled) })
	return runtime.Topology{}, nil
}

func (p *fakeReconcilePanel) RotateLease(context.Context, string) (model.Lease, error) {
	return model.Lease{}, nil
}

func (p *fakeReconcilePanel) ReleaseLease(context.Context, string) error { return nil }

func (p *fakeReconcilePanel) DrainNode(context.Context, string) (model.ProxyNode, error) {
	return model.ProxyNode{}, nil
}

func (p *fakeReconcilePanel) UndrainNode(context.Context, string) (model.ProxyNode, error) {
	return model.ProxyNode{}, nil
}

// TestStartResinReconcilerRunsAndStops pins the enabled path: AJS_RESIN_RECONCILE=1
// with a panel starts the loop, the first tick reconciles (the digest starts
// empty), and the returned stop function waits for the goroutine.
func TestStartResinReconcilerRunsAndStops(t *testing.T) {
	t.Setenv("AJS_RESIN_RECONCILE", "1")
	// The default jitter is capped at the interval, so a 1s interval keeps
	// the test's wait short without disabling the jitter.
	t.Setenv("AJS_RESIN_RECONCILE_INTERVAL", "1s")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	panel := &fakeReconcilePanel{reconciled: make(chan struct{})}
	stop, err := startResinReconciler(context.Background(), store.NewEmpty(), panel, logger)
	if err != nil {
		t.Fatalf("startResinReconciler: %v", err)
	}
	if stop == nil {
		t.Fatal("startResinReconciler returned a nil stop with a panel and the flag on")
	}
	select {
	case <-panel.reconciled:
	case <-time.After(5 * time.Second):
		t.Fatal("reconciler did not reconcile within the jitter window")
	}
	stop()
}
