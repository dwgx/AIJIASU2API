package store

import (
	"errors"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// TestJournalSizeTriggerCompacts pins the log-size half of the checkpoint
// contract. The threshold is lowered for the test so one frame crosses it; the
// production value is 64 MiB.
func TestJournalSizeTriggerCompacts(t *testing.T) {
	previous := journalCheckpointLogBytes
	journalCheckpointLogBytes = 1
	t.Cleanup(func() { journalCheckpointLogBytes = previous })

	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	st := NewEmpty()
	if _, err := j.Recover(st); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	st.AttachJournal(j)

	if _, err := st.CreateUser(model.CreateUserInput{Name: "size-trigger"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// The mutation returned only after the size-triggered checkpoint, so the
	// active log is back to its header and CURRENT names the new sequence.
	if got := j.LogBytes(); got != journalHeaderBytes {
		t.Fatalf("active log = %d bytes, want the %d-byte header after a size-triggered checkpoint",
			got, journalHeaderBytes)
	}
	if j.Seq() != 1 {
		t.Fatalf("journal seq = %d, want 1", j.Seq())
	}
	if j.checkpointSeq != 1 {
		t.Fatalf("checkpoint seq = %d, want 1", j.checkpointSeq)
	}
}

// TestJournalSweepCommitsBeforeEarlyReturn pins the expiry-sweep path: a
// CreateLease call sweeps an expired lease and then fails validation. The
// release and its audit row must already be durable when the error returns.
func TestJournalSweepCommitsBeforeEarlyReturn(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	st := newEmptyWithClock(clock, nil)
	if _, err := j.Recover(st); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	st.AttachJournal(j)

	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "sweep-ops", Username: "sweep-ops", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 10,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-sweep", AccountID: account.ID, Label: "sweep-1", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.95",
		Protocol: model.ProtocolSOCKS5, GatePort: 13095,
	}); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "sweep-user", Platform: "android"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid,
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	// The lease expires, then a call that sweeps it fails validation before its
	// own mutation: the sweep is the only durable effect.
	now = now.Add(48 * time.Hour)
	before := j.Seq()
	if _, _, err := st.CreateLease(model.CreateLeaseInput{}); err == nil {
		t.Fatal("CreateLease with no user succeeded")
	}
	if got := j.Seq(); got != before+1 {
		t.Fatalf("the sweep before the validation error committed %d frames, want exactly 1", got-before)
	}
	if leases := st.Leases(); len(leases) != 0 {
		t.Fatalf("active leases = %d, want the expired lease swept", len(leases))
	}

	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := OpenJournal(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered := NewEmpty()
	if _, err := reopened.Recover(recovered); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if leases := recovered.Leases(); len(leases) != 0 {
		t.Fatalf("replayed leases = %d, want the expired lease released", len(leases))
	}
	found := false
	for _, event := range recovered.Audit(50) {
		if event.Action == "lease.expired" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("the lease.expired audit row was not recovered")
	}
}

// TestStoreAuditHookMayCallBackIntoTheStore pins the reentrancy contract: the
// production sink persists the webhook outbox, which takes s.mu. Delivery runs
// after the write lock is released, so the call must complete instead of
// deadlocking.
func TestStoreAuditHookMayCallBackIntoTheStore(t *testing.T) {
	st := NewEmpty()
	done := make(chan struct{}, 1)
	st.SetAuditHook(func(model.AuditEvent) {
		// The production path: the webhook sink writes the outbox.
		_ = st.Audit(1)
		done <- struct{}{}
	})
	go func() {
		_, _ = st.WriteAudit(model.SeverityCritical, "operator", "secret.rotate", "key-1",
			"master key rotated", nil)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("audit hook re-entered the store while the write lock was held")
	}
}

// TestStoreExpiryHookMayCallBackIntoTheStore is the lease-expiry half of the
// same contract.
func TestStoreExpiryHookMayCallBackIntoTheStore(t *testing.T) {
	fixture := newOrderFixture(t)
	st := fixture.st
	done := make(chan struct{}, 4)
	st.SetLeaseExpiryHook(func(model.Lease) {
		_ = st.Audit(1)
		done <- struct{}{}
	})
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid,
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	*fixture.now = fixture.now.Add(48 * time.Hour)
	go st.Leases()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lease expiry hook re-entered the store while the write lock was held")
	}
}

// journalSweepFixture builds one account, one live node and two enabled users
// on a controllable clock, with an attached journal.
func journalSweepFixture(t *testing.T) (*Journal, *Store, *time.Time, model.PanelUser, model.PanelUser) {
	t.Helper()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	j, err := OpenJournal(t.TempDir())
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	st := newEmptyWithClock(clock, nil)
	if _, err := j.Recover(st); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	st.AttachJournal(j)
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "sweep-ops", Username: "sweep-ops", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 10,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-sweep", AccountID: account.ID, Label: "sweep-1", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.95",
		Protocol: model.ProtocolSOCKS5, GatePort: 13095,
	}); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	first, err := st.CreateUser(model.CreateUserInput{Name: "sweep-first", Platform: "android"})
	if err != nil {
		t.Fatalf("CreateUser first: %v", err)
	}
	second, err := st.CreateUser(model.CreateUserInput{Name: "sweep-second", Platform: "android"})
	if err != nil {
		t.Fatalf("CreateUser second: %v", err)
	}
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: first.ID, Platform: model.PlatformAndroid,
	}); err != nil {
		t.Fatalf("CreateLease first: %v", err)
	}
	return j, st, &now, first, second
}

// TestJournalSweepAndSuccessCommitOnce pins the exactly-once rule for a
// compound call: the expiry and the new lease share one frame.
func TestJournalSweepAndSuccessCommitOnce(t *testing.T) {
	j, st, now, _, second := journalSweepFixture(t)
	*now = now.Add(48 * time.Hour)
	before := j.Seq()
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: second.ID, Platform: model.PlatformAndroid,
	}); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if got := j.Seq(); got != before+1 {
		t.Fatalf("sweep plus success advanced the journal by %d frames, want exactly 1", got-before)
	}
	if leases := st.Leases(); len(leases) != 1 || leases[0].UserID != second.ID {
		t.Fatalf("leases = %+v, want only the new lease", leases)
	}
}

// TestJournalSweepErrorPropagatesOnEarlyReturn pins the sweep's durability
// error: a caller that returns before its own mutation still propagates the
// failed sweep commit.
func TestJournalSweepErrorPropagatesOnEarlyReturn(t *testing.T) {
	j, st, now, _, _ := journalSweepFixture(t)
	*now = now.Add(48 * time.Hour)
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The sweep runs first; the validation error would normally win, but the
	// failed durability commit is the error the caller must see.
	if _, _, err := st.CreateLease(model.CreateLeaseInput{}); !errors.Is(err, ErrDurability) {
		t.Fatalf("CreateLease error = %v, want ErrDurability from the sweep flush", err)
	}
}

// TestOperationStartPropagatesDurabilityError pins the async executor's first
// write: a queued record that cannot be made running must report the
// durability failure instead of collapsing it into "already canceled".
func TestOperationStartPropagatesDurabilityError(t *testing.T) {
	j, st, _, _, _ := journalSweepFixture(t)
	account := st.Accounts()[0]
	op, busy, err := st.BeginOperation(model.Operation{
		Type:        model.OperationTypeSwitch,
		AccountID:   account.ID,
		Target:      model.OperationTarget{ServerID: "node-sweep"},
		RequestedBy: "key:test",
	})
	if err != nil || busy != nil {
		t.Fatalf("BeginOperation: busy=%+v err=%v", busy, err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if started, err := st.MarkOperationRunning(op.ID); err == nil || started {
		t.Fatalf("MarkOperationRunning = %v, %v; want a durability error", started, err)
	}
}

// TestLeaseReadFailsClosedWhenSweepCannotCommit pins the getter contract: a
// release that cannot be committed is never presented as a healthy snapshot,
// and the store is poisoned so readiness and main see the failure.
func TestLeaseReadFailsClosedWhenSweepCannotCommit(t *testing.T) {
	j, st, now, _, _ := journalSweepFixture(t)
	*now = now.Add(48 * time.Hour)
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if leases := st.Leases(); leases != nil {
		t.Fatalf("Leases() returned %d rows after a failed sweep commit", len(leases))
	}
	if err := st.DurabilityError(); err == nil {
		t.Fatal("the failed sweep did not poison the store")
	}
}
