package runtime_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsproxy/apifacade"
)

func TestDebugRecorderRetainsBoundedWindow(t *testing.T) {
	rec := runtime.NewDebugRecorder(runtime.DebugRingCapacity)
	for i := 1; i <= runtime.DebugRingCapacity+5; i++ {
		entry := rec.Record(model.DebugTrace{Kind: model.DebugKindSwitchStep, AccountID: "acc-01", Slot: 1, Step: "validate"})
		if entry.Seq != uint64(i) {
			t.Fatalf("record %d: seq = %d, want %d", i, entry.Seq, i)
		}
	}
	if rec.Capacity() != runtime.DebugRingCapacity {
		t.Fatalf("capacity = %d, want %d", rec.Capacity(), runtime.DebugRingCapacity)
	}
	if rec.Len() != runtime.DebugRingCapacity {
		t.Fatalf("len = %d, want %d", rec.Len(), runtime.DebugRingCapacity)
	}
	if rec.Evicted() != 5 {
		t.Fatalf("evicted = %d, want 5", rec.Evicted())
	}
	page := rec.Snapshot("", 0)
	if page.Capacity != runtime.DebugRingCapacity || page.Retained != runtime.DebugRingCapacity {
		t.Fatalf("page bounds = capacity %d retained %d", page.Capacity, page.Retained)
	}
	if page.Evicted != 5 {
		t.Fatalf("page evicted = %d, want 5", page.Evicted)
	}
	if len(page.Entries) != runtime.DebugRingCapacity {
		t.Fatalf("entries = %d, want %d", len(page.Entries), runtime.DebugRingCapacity)
	}
	if page.Entries[0].Seq != 6 || page.Entries[len(page.Entries)-1].Seq != uint64(runtime.DebugRingCapacity+5) {
		t.Fatalf("window = %d..%d, want 6..%d", page.Entries[0].Seq, page.Entries[len(page.Entries)-1].Seq, runtime.DebugRingCapacity+5)
	}
	if page.OldestSeq != 6 || page.NextSeq != uint64(runtime.DebugRingCapacity+5) {
		t.Fatalf("cursors = oldest %d next %d", page.OldestSeq, page.NextSeq)
	}
	last := rec.Snapshot("", 10)
	if len(last.Entries) != 10 || last.Entries[9].Seq != uint64(runtime.DebugRingCapacity+5) {
		t.Fatalf("limit window = %+v", last.Entries)
	}
	if rec.Snapshot("acc-missing", 0).Retained != 0 {
		t.Fatal("an unknown account must filter every entry")
	}
}

func TestDebugRecorderRedactsSecrets(t *testing.T) {
	rec := runtime.NewDebugRecorder(8)
	raw := "cookie=session-cookie sid=abcdef token=ghijkl device_id=device-1 cuid=cuid-1 password=hunter2"
	rec.Record(model.DebugTrace{
		Kind:       model.DebugKindSessionLost,
		AccountID:  "acc-01",
		NodeID:     "acc-01#1:s1",
		ServerID:   "s1",
		Slot:       1,
		Generation: 4,
		Detail:     raw,
		ErrorClass: "token=error-token",
	})
	page := rec.Snapshot("", 0)
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(page.Entries))
	}
	entry := page.Entries[0]
	for _, secret := range []string{"session-cookie", "abcdef", "ghijkl", "device-1", "cuid-1", "hunter2", "error-token"} {
		if strings.Contains(entry.Detail, secret) || strings.Contains(entry.ErrorClass, secret) {
			t.Fatalf("secret %q survived redaction: detail=%q errorClass=%q", secret, entry.Detail, entry.ErrorClass)
		}
	}
	if !strings.Contains(entry.Detail, "[REDACTED]") {
		t.Fatalf("detail = %q, want redaction markers", entry.Detail)
	}
	if entry.AccountID != "acc-01" || entry.NodeID != "acc-01#1:s1" || entry.Slot != 1 || entry.Generation != 4 {
		t.Fatalf("attribution changed: %+v", entry)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal page: %v", err)
	}
	for _, secret := range []string{"session-cookie", "abcdef", "ghijkl", "device-1", "cuid-1", "hunter2", "error-token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("encoded page leaked %q: %s", secret, encoded)
		}
	}
}

func TestDebugRecorderSinceAndSubscription(t *testing.T) {
	rec := runtime.NewDebugRecorder(3)
	live, cancel := rec.Subscribe(4)
	defer cancel()

	first := rec.Record(model.DebugTrace{Kind: model.DebugKindGateOpen, AccountID: "acc-01"})
	second := rec.Record(model.DebugTrace{Kind: model.DebugKindGateClose, AccountID: "acc-01"})
	for _, want := range []uint64{first.Seq, second.Seq} {
		select {
		case got := <-live:
			if got.Seq != want {
				t.Fatalf("live seq = %d, want %d", got.Seq, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("live subscriber received nothing")
		}
	}

	replay, restarted := rec.Since(0)
	if restarted || len(replay) != 2 {
		t.Fatalf("since 0 = %d entries restarted=%v", len(replay), restarted)
	}
	replay, restarted = rec.Since(first.Seq)
	if restarted || len(replay) != 1 || replay[0].Seq != second.Seq {
		t.Fatalf("since %d = %+v restarted=%v", first.Seq, replay, restarted)
	}
	// Fill the 3-entry ring, evicting the first sequence.
	rec.Record(model.DebugTrace{Kind: model.DebugKindLeaseCreate})
	rec.Record(model.DebugTrace{Kind: model.DebugKindLeaseRevoke})
	replay, restarted = rec.Since(1)
	if !restarted {
		t.Fatal("an evicted cursor must report a restart")
	}
	if len(replay) != 3 {
		t.Fatalf("restarted window = %d entries, want 3", len(replay))
	}
}

func TestDebugRecorderConcurrentRecords(t *testing.T) {
	rec := runtime.NewDebugRecorder(500)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				rec.Record(model.DebugTrace{Kind: model.DebugKindSlotState, AccountID: "acc-01"})
			}
		}()
	}
	wg.Wait()
	page := rec.Snapshot("", 0)
	if len(page.Entries) != 500 {
		t.Fatalf("entries = %d, want 500", len(page.Entries))
	}
	for i := 1; i < len(page.Entries); i++ {
		if page.Entries[i].Seq <= page.Entries[i-1].Seq {
			t.Fatalf("sequence is not strictly increasing at %d: %d then %d", i, page.Entries[i-1].Seq, page.Entries[i].Seq)
		}
	}
	if page.NextSeq != 600 {
		t.Fatalf("nextSeq = %d, want 600", page.NextSeq)
	}
}

func TestDebugRecorderNilIsNoop(t *testing.T) {
	var rec *runtime.DebugRecorder
	if entry := rec.Record(model.DebugTrace{Kind: model.DebugKindSwitch}); entry.Seq != 0 {
		t.Fatalf("nil recorder returned %+v", entry)
	}
	if page := rec.Snapshot("", 0); page.Enabled || page.Entries == nil {
		t.Fatalf("nil recorder page = %+v", page)
	}
	ch, cancel := rec.Subscribe(1)
	if _, ok := <-ch; ok {
		t.Fatal("nil recorder subscription must be closed")
	}
	cancel()
	rec.Close()
}

// TestNodeControllerEmitsDebugTrace proves the instrumentation is wired, not
// just the ring: one sync and one switch must produce session, gate, slot and
// switch-step entries with slot/generation/node attribution.
func TestNodeControllerEmitsDebugTrace(t *testing.T) {
	st := newNodeTestPoolStore(t)
	acct := newNodeTestAccount(t, st, "debug-trace")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers: map[string]apifacade.Server{
			"s1": testServer("s1"),
			"s2": testServer("s2"),
		},
		FetchedAt: time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC),
	})
	rec := runtime.NewDebugRecorder(0)
	opener := newThreatOpener()
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:            st,
		Logins:           logins,
		Sessions:         opener,
		Gates:            newSwitchGateFactory().build,
		MaxServers:       1,
		Now:              newSwitchClock().Now,
		LivenessInterval: -1,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Debug:            rec,
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = controller.Stop(stopCtx)
	})
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	page := rec.Snapshot(acct.ID, 0)
	for _, kind := range []string{model.DebugKindSessionOpen, model.DebugKindGateOpen, model.DebugKindSlotState} {
		entries := debugEntriesOfKind(page.Entries, kind)
		if len(entries) == 0 {
			t.Fatalf("sync produced no %s entry (kinds: %v)", kind, debugKinds(page.Entries))
		}
		entry := entries[len(entries)-1]
		if entry.AccountID != acct.ID || entry.Slot != 1 || entry.Generation == 0 || entry.NodeID == "" {
			t.Fatalf("%s attribution = %+v", kind, entry)
		}
	}

	if _, err := controller.ConnectSlot(context.Background(), model.SlotKey{AccountID: acct.ID, Index: 1}, "s2", "", "manual"); err != nil {
		t.Fatalf("ConnectSlot: %v", err)
	}
	page = rec.Snapshot(acct.ID, 0)
	steps := map[string]bool{}
	for _, entry := range debugEntriesOfKind(page.Entries, model.DebugKindSwitchStep) {
		steps[entry.Step] = true
		if entry.Slot != 1 || entry.Generation == 0 || entry.NodeID == "" {
			t.Fatalf("switch step attribution = %+v", entry)
		}
	}
	for _, step := range []string{"validate", "drain", "start_gate", "commit"} {
		if !steps[step] {
			t.Fatalf("missing switch step %q (steps: %v)", step, steps)
		}
	}
	switches := debugEntriesOfKind(page.Entries, model.DebugKindSwitch)
	if len(switches) != 1 || switches[0].State != model.DebugStateOK {
		t.Fatalf("switch entries = %+v", switches)
	}
	if switches[0].Slot != 1 || switches[0].Generation == 0 || switches[0].NodeID == "" {
		t.Fatalf("switch attribution = %+v", switches[0])
	}
	// The old gate must have been closed with its own generation.
	closes := debugEntriesOfKind(page.Entries, model.DebugKindGateClose)
	if len(closes) == 0 || closes[0].Generation == 0 || closes[0].Slot != 1 {
		t.Fatalf("gate close entries = %+v", closes)
	}

	// A vendor kick is a session loss: kill the live fake session, run one
	// liveness scan and assert the trace names the slot and generation.
	opener.session("s2").kill()
	if closed := controller.CheckLiveness(); closed != 1 {
		t.Fatalf("CheckLiveness closed %d gates, want 1", closed)
	}
	page = rec.Snapshot(acct.ID, 0)
	lost := debugEntriesOfKind(page.Entries, model.DebugKindSessionLost)
	if len(lost) != 1 {
		t.Fatalf("session lost entries = %+v", lost)
	}
	if lost[0].Slot != 1 || lost[0].Generation == 0 || lost[0].NodeID == "" || lost[0].State != model.DebugStateLost {
		t.Fatalf("session lost attribution = %+v", lost[0])
	}
}

func debugEntriesOfKind(entries []model.DebugTrace, kind string) []model.DebugTrace {
	out := make([]model.DebugTrace, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind == kind {
			out = append(out, entry)
		}
	}
	return out
}

func debugKinds(entries []model.DebugTrace) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Kind)
	}
	return out
}
