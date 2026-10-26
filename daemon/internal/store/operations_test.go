package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

func beginTestOperation(t *testing.T, st *Store, accountID, serverID string) model.Operation {
	t.Helper()
	op, busy, err := st.BeginOperation(model.Operation{
		Type:        model.OperationTypeSwitch,
		AccountID:   accountID,
		Target:      model.OperationTarget{ServerID: serverID},
		RequestedBy: "key:test",
		Steps: []model.OperationStep{{
			Name: "validate", State: model.OperationStepOK, DurationMs: 3,
		}},
	})
	if err != nil {
		t.Fatalf("BeginOperation: %v", err)
	}
	if busy != nil {
		t.Fatalf("BeginOperation returned busy %q", busy.ID)
	}
	return op
}

func finishTestOperation(t *testing.T, st *Store, id string, result *model.SelectNodeResult) model.Operation {
	t.Helper()
	if started, err := st.MarkOperationRunning(id); err != nil || !started {
		t.Fatalf("MarkOperationRunning(%q) = %v, %v", id, started, err)
	}
	op, ok, err := st.FinishOperation(id, model.OperationSucceeded, []model.OperationStep{
		{Name: "validate", State: model.OperationStepOK, DurationMs: 3},
		{Name: "switch", State: model.OperationStepOK, DurationMs: 40},
	}, result, nil)
	if err != nil || !ok {
		t.Fatalf("FinishOperation(%q) = %v, %v", id, ok, err)
	}
	return op
}

func TestOperationStoreLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 26, 2, 30, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	account := testStoreAccount(t, st)

	op := beginTestOperation(t, st, account.ID, "srv-a")
	if op.ID != "op-0001" || op.State != model.OperationQueued {
		t.Fatalf("created operation = %+v", op)
	}
	if op.CreatedAt == "" || op.StartedAt != "" || op.FinishedAt != "" {
		t.Fatalf("queued timestamps = created %q started %q finished %q",
			op.CreatedAt, op.StartedAt, op.FinishedAt)
	}
	if op.RequestedBy != "key:test" || op.Target.ServerID != "srv-a" {
		t.Fatalf("queued metadata = %+v", op)
	}

	if _, busy, err := st.BeginOperation(model.Operation{
		Type: model.OperationTypeSwitch, AccountID: account.ID,
	}); err != nil || busy == nil || busy.ID != op.ID {
		t.Fatalf("second BeginOperation busy = %+v err = %v, want op %q", busy, err, op.ID)
	}

	finished := finishTestOperation(t, st, op.ID, &model.SelectNodeResult{
		Changed: true, AccountID: account.ID, ServerID: "srv-a",
		NodeID: "acc-01#1:srv-a", GatePort: 13002, SessionHealthy: true,
	})
	if finished.State != model.OperationSucceeded || finished.FinishedAt == "" {
		t.Fatalf("finished operation = %+v", finished)
	}
	if finished.Result == nil || finished.Result.GatePort != 13002 || !finished.Result.Changed {
		t.Fatalf("result = %+v", finished.Result)
	}
	if finished.Error != nil {
		t.Fatalf("succeeded operation carries an error: %+v", finished.Error)
	}

	stored, ok := st.Operation(op.ID)
	if !ok || stored.State != model.OperationSucceeded {
		t.Fatalf("Operation(%q) = %+v/%v", op.ID, stored, ok)
	}
	listed := st.Operations(OperationFilter{AccountID: account.ID, State: model.OperationSucceeded, Limit: 10})
	if len(listed) != 1 || listed[0].ID != op.ID {
		t.Fatalf("Operations(filter) = %+v", listed)
	}
	if empty := st.Operations(OperationFilter{State: model.OperationFailed}); len(empty) != 0 {
		t.Fatalf("failed filter returned %+v", empty)
	}

	audits := st.Audit(20)
	actions := make(map[string]bool, len(audits))
	for _, audit := range audits {
		actions[audit.Action] = true
	}
	if !actions["operation.create"] || !actions["operation.finish"] {
		t.Fatalf("audit actions = %v, want operation.create and operation.finish", actions)
	}
}

func TestOperationStoreCancel(t *testing.T) {
	st := NewEmpty()
	account := testStoreAccount(t, st)
	op := beginTestOperation(t, st, account.ID, "srv-a")

	canceled, err := st.CancelOperation(op.ID)
	if err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	if canceled.State != model.OperationCanceled || canceled.FinishedAt == "" {
		t.Fatalf("canceled operation = %+v", canceled)
	}
	if len(canceled.Steps) != 2 || canceled.Steps[1].State != model.OperationStepCanceled {
		t.Fatalf("canceled steps = %+v", canceled.Steps)
	}
	if started, _ := st.MarkOperationRunning(op.ID); started {
		t.Fatal("MarkOperationRunning succeeded after cancel")
	}
	if _, ok, _ := st.FinishOperation(op.ID, model.OperationSucceeded, nil, nil, nil); ok {
		t.Fatal("FinishOperation overwrote a canceled record")
	}
	if _, err := st.CancelOperation(op.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second CancelOperation error = %v, want ErrConflict", err)
	}
	if _, err := st.CancelOperation("op-9999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown CancelOperation error = %v, want ErrNotFound", err)
	}

	running := beginTestOperation(t, st, account.ID, "srv-b")
	if started, err := st.MarkOperationRunning(running.ID); err != nil || !started {
		t.Fatalf("MarkOperationRunning(running) = %v, %v", started, err)
	}
	if _, err := st.CancelOperation(running.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("running cancel error = %v, want ErrConflict", err)
	}
}

func TestOperationStoreWait(t *testing.T) {
	st := NewEmpty()
	account := testStoreAccount(t, st)
	op := beginTestOperation(t, st, account.ID, "srv-a")

	done := make(chan model.Operation, 1)
	go func() {
		final, err := st.WaitOperation(context.Background(), op.ID)
		if err != nil {
			t.Errorf("WaitOperation: %v", err)
		}
		done <- final
	}()
	time.Sleep(5 * time.Millisecond)
	finishTestOperation(t, st, op.ID, nil)
	select {
	case final := <-done:
		if final.State != model.OperationSucceeded {
			t.Fatalf("waiter saw %+v", final)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitOperation did not wake")
	}

	// A waiter that arrives after the terminal transition returns at once.
	final, err := st.WaitOperation(context.Background(), op.ID)
	if err != nil || final.State != model.OperationSucceeded {
		t.Fatalf("late WaitOperation = %+v err = %v", final, err)
	}
	if _, err := st.WaitOperation(context.Background(), "op-9999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown WaitOperation error = %v, want ErrNotFound", err)
	}
}

func TestOperationStoreDurability(t *testing.T) {
	now := time.Date(2026, 10, 26, 2, 30, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	account := testStoreAccount(t, st)

	done := beginTestOperation(t, st, account.ID, "srv-a")
	finishTestOperation(t, st, done.ID, &model.SelectNodeResult{
		Changed: true, AccountID: account.ID, ServerID: "srv-a", GatePort: 13002,
	})
	interrupted := beginTestOperation(t, st, account.ID, "srv-b")
	if started, err := st.MarkOperationRunning(interrupted.ID); err != nil || !started {
		t.Fatalf("MarkOperationRunning = %v, %v", started, err)
	}

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return now.Add(time.Minute) }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	terminal, ok := restored.Operation(done.ID)
	if !ok || terminal.State != model.OperationSucceeded || terminal.FinishedAt == "" {
		t.Fatalf("restored terminal operation = %+v/%v", terminal, ok)
	}
	if terminal.Result == nil || terminal.Result.GatePort != 13002 {
		t.Fatalf("restored result = %+v", terminal.Result)
	}

	lost, ok := restored.Operation(interrupted.ID)
	if !ok {
		t.Fatalf("restored interrupted operation missing")
	}
	if lost.State != model.OperationFailed || lost.Error == nil || lost.Error.Code != OperationUnknownCode {
		t.Fatalf("restored interrupted operation = %+v", lost)
	}
	if lost.FinishedAt == "" {
		t.Fatal("interrupted operation has no finishedAt")
	}
	if len(lost.Steps) == 0 || lost.Steps[len(lost.Steps)-1].Name != "interrupted" {
		t.Fatalf("interrupted steps = %+v", lost.Steps)
	}
	foundAudit := false
	for _, audit := range restored.Audit(20) {
		if audit.Action == "operation.unknown" && audit.Target == interrupted.ID {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("no operation.unknown audit for %q", interrupted.ID)
	}

	// The sequence continues after the restored ids instead of colliding.
	next := beginTestOperation(t, restored, account.ID, "srv-c")
	if next.ID != "op-0003" {
		t.Fatalf("next operation id = %q, want op-0003", next.ID)
	}
}

func TestOperationStoreRetention(t *testing.T) {
	now := time.Date(2026, 10, 26, 2, 30, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	account := testStoreAccount(t, st)

	for range OperationRetention + 5 {
		op := beginTestOperation(t, st, account.ID, "srv-a")
		finishTestOperation(t, st, op.ID, nil)
	}
	all := st.Operations(OperationFilter{AccountID: account.ID})
	if len(all) != OperationRetention {
		t.Fatalf("retained %d operations, want %d", len(all), OperationRetention)
	}
	if _, ok := st.Operation("op-0001"); ok {
		t.Fatal("oldest operation survived retention")
	}
	if _, ok := st.Operation("op-0006"); !ok {
		t.Fatal("newest 200 operations do not start at op-0006")
	}

	// A terminal record older than 24 h is dropped on the next write.
	now = now.Add(OperationMaxAge + time.Hour)
	op := beginTestOperation(t, st, account.ID, "srv-b")
	finishTestOperation(t, st, op.ID, nil)
	if _, ok := st.Operation("op-0006"); ok {
		t.Fatal("operation older than 24 h survived pruning")
	}
}

func TestOperationStoreSurvivesAccountDelete(t *testing.T) {
	st := NewEmpty()
	account := testStoreAccount(t, st)
	op := beginTestOperation(t, st, account.ID, "srv-a")
	finishTestOperation(t, st, op.ID, nil)
	if err := st.DeleteAccount(account.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := NewEmpty()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore after account delete: %v", err)
	}
	stored, ok := restored.Operation(op.ID)
	if !ok || stored.State != model.OperationSucceeded {
		t.Fatalf("operation after account delete = %+v/%v", stored, ok)
	}
}

func TestOperationStoreRestoreRejectsMalformedRows(t *testing.T) {
	now := time.Date(2026, 10, 26, 2, 30, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	account := testStoreAccount(t, st)
	op := beginTestOperation(t, st, account.ID, "srv-a")
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	snap.Operations[0].State = model.OperationFailed
	snap.Operations[0].Error = nil
	broken, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return now }, nil)
	if err := restored.Restore(broken); err == nil {
		t.Fatalf("Restore accepted failed operation %q without an error", op.ID)
	}
}
