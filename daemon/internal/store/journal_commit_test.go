package store_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// journalCommitPassword is a fixed local placeholder for the tenant signup
// path. It is not a credential and never leaves the test process.
const journalCommitPassword = "journal-commit-not-a-credential"

// journalFixture is the minimum money path through the public API: one active
// android account with a free LIVE node, one enabled user, one active tenant
// linked to that user and one active plan. Every step is a normal mutation, so
// the same helper works on a recovered store.
type journalFixture struct {
	account model.Account
	user    model.PanelUser
	tenant  model.Tenant
	plan    model.Plan
}

func newJournalFixture(t *testing.T, st *store.Store) journalFixture {
	t.Helper()
	account, err := st.CreateAccount(accountInput("journal-orders", persistPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-journal", AccountID: account.ID, Label: "journal-1", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.91",
		Protocol: model.ProtocolSOCKS5, GatePort: 13091,
	}); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	// A second free node lets a test allocate a direct lease beside an order
	// grant without changing the one-lease-per-user rule.
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-journal-2", AccountID: account.ID, Label: "journal-2", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.92",
		Protocol: model.ProtocolSOCKS5, GatePort: 13092,
	}); err != nil {
		t.Fatalf("PutNode second: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "journal-user", Platform: "android"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := st.SignupTenant(model.TenantSignupInput{
		Email: "journal@example.com", Password: journalCommitPassword, DisplayName: "Journal User",
	}); err != nil {
		t.Fatalf("SignupTenant: %v", err)
	}
	var tenant model.Tenant
	found := false
	for _, candidate := range st.Tenants() {
		if candidate.Email == "journal@example.com" {
			tenant, found = candidate, true
			break
		}
	}
	if !found {
		t.Fatal("SignupTenant did not store the tenant")
	}
	if _, err := st.ApproveTenant(tenant.ID, user.ID, nil); err != nil {
		t.Fatalf("ApproveTenant: %v", err)
	}
	tenant, _ = st.Tenant(tenant.ID)
	plan, err := st.CreatePlan(model.PlanInput{
		Name: "Journal Plan", AmountMinor: 3000, Currency: "CNY",
		LeaseTTLSeconds: 3600, QuotaBytes: 10 << 30,
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	return journalFixture{account: account, user: user, tenant: tenant, plan: plan}
}

// settleOrder runs one manual settlement and requires an active, serving order.
func settleOrder(t *testing.T, st *store.Store, orderID, eventID, reference string) model.Order {
	t.Helper()
	order, err := st.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
		Provider:    model.ProviderManual,
		ID:          eventID,
		Kind:        model.PaymentSettled,
		OrderID:     orderID,
		ProviderRef: reference,
	}, "operator")
	if err != nil {
		t.Fatalf("ApplyPaymentEvent(%s): %v", eventID, err)
	}
	if order.State != model.OrderActive {
		t.Fatalf("settled order state = %s, want %s", order.State, model.OrderActive)
	}
	if order.Grant == nil {
		t.Fatal("settled order has no entitlement grant")
	}
	return order
}

// journalCurrentSeq decodes the committed checkpoint pointer's sequence.
func journalCurrentSeq(t *testing.T, dir string) uint64 {
	t.Helper()
	var current struct {
		Seq uint64 `json:"seq"`
	}
	if err := json.Unmarshal(readJournalCurrent(t, dir), &current); err != nil {
		t.Fatalf("decode CURRENT: %v", err)
	}
	return current.Seq
}

// leaseIDs returns every active lease id, so a duplicate is visible by count.
func leaseIDs(st *store.Store) []string {
	leases := st.Leases()
	out := make([]string, len(leases))
	for i, lease := range leases {
		out[i] = lease.ID
	}
	return out
}

// TestJournalCriticalMutationSurvivesReplayWithoutCheckpoint is the RPO-0
// contract: an acknowledged order and settlement are recoverable from the log
// alone, because the process never wrote a checkpoint.
func TestJournalCriticalMutationSurvivesReplayWithoutCheckpoint(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	fixture := newJournalFixture(t, st)

	order, replayed, err := st.CreateOrder(fixture.tenant.ID, fixture.user.ID, fixture.plan.ID, "journal-order", "operator")
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if replayed {
		t.Fatal("CreateOrder replayed unexpectedly")
	}
	settled := settleOrder(t, st, order.ID, "evt-journal-settled", "ref-journal-settled")
	seq := j.Seq()
	if seq == 0 {
		t.Fatal("critical mutations did not commit a journal frame")
	}
	// A crash: close without a checkpoint, exactly like SIGKILL between the
	// acknowledged response and the 30 s timer.
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestJournal(t, dir)
	recovered := store.NewEmpty()
	report, err := reopened.Recover(recovered)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.LastSeq != seq {
		t.Fatalf("recovered last seq = %d, want %d", report.LastSeq, seq)
	}
	got, ok := recovered.Order(order.ID)
	if !ok {
		t.Fatalf("order %s is missing after replay", order.ID)
	}
	if got.State != model.OrderActive || got.Grant == nil {
		t.Fatalf("replayed order = %+v, want an active order with a grant", got)
	}
	if got.Grant.LeaseID != settled.Grant.LeaseID {
		t.Fatalf("replayed grant lease = %q, want %q", got.Grant.LeaseID, settled.Grant.LeaseID)
	}
	if ids := leaseIDs(recovered); len(ids) != 1 || ids[0] != settled.Grant.LeaseID {
		t.Fatalf("replayed leases = %v, want exactly %q", ids, settled.Grant.LeaseID)
	}
	events := recovered.PaymentEvents()
	if len(events) != 1 || events[0].ID != "evt-journal-settled" {
		t.Fatalf("replayed payment events = %+v, want the settled event", events)
	}
}

// TestJournalAddUsageIsDurable pins the quota path: a metered chunk is on disk
// before AddUsage returns, so a crash cannot under-count a customer's month.
func TestJournalAddUsageIsDurable(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	fixture := newJournalFixture(t, st)

	if err := st.AddUsage(fixture.user.ID, fixture.account.ID, 4096); err != nil {
		t.Fatalf("AddUsage: %v", err)
	}
	if err := st.AddUsage(fixture.user.ID, fixture.account.ID, 2048); err != nil {
		t.Fatalf("AddUsage: %v", err)
	}
	seq := j.Seq()
	if seq == 0 {
		t.Fatal("AddUsage did not commit a journal frame")
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestJournal(t, dir)
	recovered := store.NewEmpty()
	report, err := reopened.Recover(recovered)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.LastSeq != seq {
		t.Fatalf("recovered last seq = %d, want %d", report.LastSeq, seq)
	}
	if got := recovered.UserMonthlyUsage(fixture.user.ID); got != 6144 {
		t.Fatalf("replayed user usage = %d, want 6144", got)
	}
	if got := recovered.MonthlyUsage(fixture.account.ID); got != 6144 {
		t.Fatalf("replayed account usage = %d, want 6144", got)
	}
}

// TestJournalCommitFailurePoisonsReadiness proves a failed commit is never
// silent: the mutation returns ErrDurability, readiness sees the poison and
// every later commit is refused.
func TestJournalCommitFailurePoisonsReadiness(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	// A closed journal is the deterministic form of "the disk stopped
	// accepting appends": Append refuses and cannot repair itself.
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := st.CreateUser(model.CreateUserInput{Name: "poison-user"})
	if err == nil {
		t.Fatal("CreateUser succeeded after the journal closed")
	}
	if !errors.Is(err, store.ErrDurability) {
		t.Fatalf("CreateUser error = %v, want ErrDurability", err)
	}
	if st.DurabilityError() == nil {
		t.Fatal("DurabilityError is nil after a failed commit")
	}
	select {
	case <-st.DurabilityFault():
	default:
		t.Fatal("DurabilityFault is not closed after a failed commit")
	}
	if _, err := st.CreatePlan(model.PlanInput{
		Name: "Poison Plan", AmountMinor: 100, Currency: "CNY",
		LeaseTTLSeconds: 3600, QuotaBytes: 1,
	}); !errors.Is(err, store.ErrDurability) {
		t.Fatalf("CreatePlan after poison error = %v, want ErrDurability", err)
	}
	if err := st.Checkpoint(); !errors.Is(err, store.ErrDurability) {
		t.Fatalf("Checkpoint after poison error = %v, want ErrDurability", err)
	}
}

// TestJournalCheckpointTimerCompacts runs the same tick main's 30 s timer runs:
// Checkpoint must publish CURRENT, archive the active log and leave an empty
// log whose first frame continues the sequence.
func TestJournalCheckpointTimerCompacts(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	fixture := newJournalFixture(t, st)
	before := j.Seq()
	if before == 0 {
		t.Fatal("fixture mutations did not commit")
	}

	// A short interval stands in for the 30 s ticker; the call is identical.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(2 * time.Second)
	compacted := false
	for !compacted {
		select {
		case <-ticker.C:
			if err := st.Checkpoint(); err != nil {
				t.Fatalf("Checkpoint: %v", err)
			}
		case <-deadline:
			t.Fatal("checkpoint timer did not compact the log")
		}
		compacted = len(readJournalLog(t, dir)) == journalTestHeader
	}

	if seq := journalCurrentSeq(t, dir); seq != before {
		t.Fatalf("CURRENT seq = %d, want %d", seq, before)
	}
	archives, err := filepath.Glob(filepath.Join(dir, "state.log.*"))
	if err != nil {
		t.Fatalf("glob archives: %v", err)
	}
	if len(archives) == 0 {
		t.Fatal("checkpoint did not archive the active log")
	}
	checkpoints, err := filepath.Glob(filepath.Join(dir, "checkpoints", "*.json"))
	if err != nil {
		t.Fatalf("glob checkpoints: %v", err)
	}
	if len(checkpoints) == 0 {
		t.Fatal("checkpoint did not write an immutable checkpoint")
	}

	// The log continues at the next sequence after compaction.
	if _, err := st.CreateUser(model.CreateUserInput{Name: "after-compact"}); err != nil {
		t.Fatalf("CreateUser after checkpoint: %v", err)
	}
	if j.Seq() != before+1 {
		t.Fatalf("journal seq = %d, want %d", j.Seq(), before+1)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestJournal(t, dir)
	recovered := store.NewEmpty()
	if _, err := reopened.Recover(recovered); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	found := false
	for _, user := range recovered.Users() {
		if user.Name == "after-compact" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("the post-checkpoint mutation is missing after replay")
	}
	if _, ok := recovered.Account(fixture.account.ID); !ok {
		t.Fatal("the checkpoint did not carry the pre-compaction account")
	}
}

// TestJournalCrashDrillKeepsAcknowledgedMutation tears the final frame of the
// log, which is what a crash during an append leaves behind: recovery keeps the
// acknowledged mutation and discards only the incomplete tail.
func TestJournalCrashDrillKeepsAcknowledgedMutation(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)

	if _, err := st.CreateUser(model.CreateUserInput{Name: "acknowledged-user"}); err != nil {
		t.Fatalf("CreateUser acknowledged: %v", err)
	}
	acknowledged := j.Seq()
	if _, err := st.CreateUser(model.CreateUserInput{Name: "torn-user"}); err != nil {
		t.Fatalf("CreateUser torn: %v", err)
	}
	torn := j.Seq()
	if acknowledged == 0 || torn != acknowledged+1 {
		t.Fatalf("journal seq = %d then %d, want two consecutive frames", acknowledged, torn)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	log := readJournalLog(t, dir)
	frames := journalTestFrames(t, log)
	if len(frames) != 2 {
		t.Fatalf("log holds %d frames, want 2", len(frames))
	}
	last := frames[1]
	// Drop the last three bytes: the frame length and CRC survive, the body is
	// short, which is the exact shape of a torn final append.
	writeJournalLog(t, dir, log[:last.start+8+last.size-3])

	reopened := openTestJournal(t, dir)
	recovered := store.NewEmpty()
	report, err := reopened.Recover(recovered)
	if err != nil {
		t.Fatalf("Recover after torn frame: %v", err)
	}
	if report.LastSeq != acknowledged {
		t.Fatalf("recovered last seq = %d, want %d (the acknowledged frame)", report.LastSeq, acknowledged)
	}
	names := make(map[string]bool)
	for _, user := range recovered.Users() {
		names[user.Name] = true
	}
	if !names["acknowledged-user"] {
		t.Fatal("the acknowledged mutation was lost")
	}
	if names["torn-user"] {
		t.Fatal("the torn, unacknowledged mutation was applied")
	}
}

// TestJournalReplayDoesNotDuplicateGrantOrLease replays a settled order and a
// direct lease, then re-delivers the same provider event and the same lease
// idempotency key: the durable dedupe keys must keep one grant, one lease per
// identity and one payment event.
func TestJournalReplayDoesNotDuplicateGrantOrLease(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	fixture := newJournalFixture(t, st)

	order, _, err := st.CreateOrder(fixture.tenant.ID, fixture.user.ID, fixture.plan.ID, "journal-dedupe", "operator")
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	settled := settleOrder(t, st, order.ID, "evt-journal-dedupe", "ref-journal-dedupe")
	grantLeaseID := settled.Grant.LeaseID

	leaseUser, err := st.CreateUser(model.CreateUserInput{Name: "journal-lease-user", Platform: "android"})
	if err != nil {
		t.Fatalf("CreateUser lease: %v", err)
	}
	// The order's account already spends its one slot on the grant lease, so
	// the direct lease needs a second serving account.
	leaseAccount, err := st.CreateAccount(accountInput("journal-lease-ops", persistPassword))
	if err != nil {
		t.Fatalf("CreateAccount lease: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-journal-3", AccountID: leaseAccount.ID, Label: "journal-3", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.93",
		Protocol: model.ProtocolSOCKS5, GatePort: 13093,
	}); err != nil {
		t.Fatalf("PutNode lease: %v", err)
	}
	directLease, replayed, err := st.CreateLease(model.CreateLeaseInput{
		UserID: leaseUser.ID, Platform: model.PlatformAndroid, IdempotencyKey: "journal-lease-key",
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if replayed {
		t.Fatal("CreateLease replayed unexpectedly")
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestJournal(t, dir)
	recovered := store.NewEmpty()
	if _, err := reopened.Recover(recovered); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	recovered.AttachJournal(reopened)

	// The provider retries the same event after the crash. It must be a no-op,
	// not a second grant or lease.
	again, err := recovered.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
		Provider:    model.ProviderManual,
		ID:          "evt-journal-dedupe",
		Kind:        model.PaymentSettled,
		OrderID:     order.ID,
		ProviderRef: "ref-journal-dedupe",
	}, "operator")
	if err != nil {
		t.Fatalf("duplicate ApplyPaymentEvent: %v", err)
	}
	if again.Grant == nil || again.Grant.LeaseID != grantLeaseID {
		t.Fatalf("duplicate settlement changed the grant: %+v", again.Grant)
	}
	if events := recovered.PaymentEvents(); len(events) != 1 {
		t.Fatalf("payment events after duplicate settlement = %d, want 1", len(events))
	}

	// The client retries the same idempotency key: the recovered lease is
	// returned and no second row is allocated.
	replayedLease, replayed, err := recovered.CreateLease(model.CreateLeaseInput{
		UserID: leaseUser.ID, Platform: model.PlatformAndroid, IdempotencyKey: "journal-lease-key",
	})
	if err != nil {
		t.Fatalf("replayed CreateLease: %v", err)
	}
	if !replayed {
		t.Fatal("CreateLease after replay did not report an idempotent replay")
	}
	if replayedLease.ID != directLease.ID {
		t.Fatalf("replayed lease = %q, want %q", replayedLease.ID, directLease.ID)
	}
	grantLeases, directLeases := 0, 0
	for _, id := range leaseIDs(recovered) {
		switch id {
		case grantLeaseID:
			grantLeases++
		case directLease.ID:
			directLeases++
		}
	}
	if grantLeases != 1 || directLeases != 1 {
		t.Fatalf("leases after replay = %v, want one grant lease and one direct lease", leaseIDs(recovered))
	}
}

// journalOutboxEntry builds a valid pending delivery with the digest the store
// recomputes.
func journalOutboxEntry(id, endpoint, payload, at string) store.WebhookOutboxEntry {
	sum := sha256.Sum256([]byte(payload))
	return store.WebhookOutboxEntry{
		ID:            id,
		EndpointID:    endpoint,
		Event:         "account.login",
		PayloadHash:   hex.EncodeToString(sum[:]),
		Payload:       json.RawMessage(payload),
		CreatedAt:     at,
		NextAttemptAt: at,
	}
}

// journalWireSchedule is one valid schedule for the commit-coverage checklist.
func journalWireSchedule(name string) model.Schedule {
	return model.Schedule{
		Name:    name,
		Enabled: true,
		Action:  model.ActionAccountReconnect,
		Target:  model.ScheduleTarget{Kind: "account", ID: "acc-01"},
		Trigger: model.ScheduleTrigger{Kind: "interval", EverySec: 300},
	}
}

// TestJournalPersistedMutationsCommitExactlyOnce is the coverage guard for
// AJS-433: every persisted mutation the design lists advances the journal by
// exactly one frame, so a checkpoint can never label an uncommitted change.
func TestJournalPersistedMutationsCommitExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	fixture := newJournalFixture(t, st)

	// A second serving account for the direct lease; the order grant spends
	// the first account's single slot.
	leaseAccount, err := st.CreateAccount(accountInput("journal-wire-ops", persistPassword))
	if err != nil {
		t.Fatalf("CreateAccount lease: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-wire", AccountID: leaseAccount.ID, Label: "wire-1", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.94",
		Protocol: model.ProtocolSOCKS5, GatePort: 13094,
	}); err != nil {
		t.Fatalf("PutNode lease: %v", err)
	}
	leaseUser, err := st.CreateUser(model.CreateUserInput{Name: "journal-wire-lease", Platform: "android"})
	if err != nil {
		t.Fatalf("CreateUser lease: %v", err)
	}
	order, _, err := st.CreateOrder(fixture.tenant.ID, fixture.user.ID, fixture.plan.ID, "wire-order", "operator")
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	schedule, err := st.CreateSchedule(journalWireSchedule("wire-schedule"), "operator")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	scopedKey, _, err := st.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: fixture.user.ID, Label: "wire-key", Scopes: []model.Scope{model.ScopeAccountsRead},
	}, "operator")
	if err != nil {
		t.Fatalf("CreateScopedKey: %v", err)
	}
	cookie, _, err := st.CreatePortalSession(fixture.tenant.ID)
	if err != nil {
		t.Fatalf("CreatePortalSession: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)

	steps := []struct {
		name string
		run  func() error
	}{
		{"mark-checkout", func() error {
			_, err := st.MarkOrderCheckout(fixture.tenant.ID, order.ID, model.ProviderEpay)
			return err
		}},
		{"apply-payment-event", func() error {
			_, err := st.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
				Provider: model.ProviderManual, ID: "wire-settled", Kind: model.PaymentSettled,
				OrderID: order.ID, ProviderRef: "wire-ref",
			}, "operator")
			return err
		}},
		{"create-lease", func() error {
			_, _, err := st.CreateLease(model.CreateLeaseInput{
				UserID: leaseUser.ID, Platform: model.PlatformAndroid, IdempotencyKey: "wire-lease",
			})
			return err
		}},
		{"append-request", func() error {
			_, err := st.AppendRequest(model.RequestLog{
				Source: model.SourceGate, AccountLabel: "journal-orders",
				Target: "example.com:443", Status: 200, Bytes: 1,
			})
			return err
		}},
		{"add-usage", func() error {
			return st.AddUsage(fixture.user.ID, fixture.account.ID, 1)
		}},
		{"create-portal-session", func() error {
			_, _, err := st.CreatePortalSession(fixture.tenant.ID)
			return err
		}},
		{"authenticate-portal-session", func() error {
			_, _, _, err := st.AuthenticatePortalSession(cookie, time.Now().UTC())
			return err
		}},
		{"put-outbox", func() error {
			return st.PutWebhookOutbox(journalOutboxEntry("wire-evt", "wire-hook", `{"accountId":"acc-01"}`, now))
		}},
		{"remove-outbox", func() error {
			return st.RemoveWebhookOutbox("wire-hook", "wire-evt")
		}},
		{"append-delivery", func() error {
			return st.AppendWebhookDelivery(model.WebhookDelivery{
				ID: "wire-evt:1", EndpointID: "wire-hook", Event: "account.login",
				Attempt: 1, Status: "success", HTTPStatus: 200, At: now,
			})
		}},
		{"next-run-id", func() error {
			id, err := st.NextRunID()
			if err != nil {
				return err
			}
			if id == "" {
				return errors.New("NextRunID returned an empty id")
			}
			return nil
		}},
		{"append-run", func() error {
			st.AppendScheduleRun(model.ScheduleRun{
				ScheduleID: schedule.ID, StartedAt: now, State: "succeeded", Result: "ok",
			})
			return nil
		}},
		{"mutate-schedule", func() error {
			_, err := st.MutateSchedule(schedule.ID, func(s *model.Schedule) { s.Cursor = 7 })
			return err
		}},
		{"record-fire", func() error {
			st.RecordScheduleFire(model.ScheduleFire{
				Key: schedule.ID + "@1", ScheduleID: schedule.ID, State: "reserved", ReservedAt: now,
			})
			return nil
		}},
		{"set-reminder-mark", func() error {
			st.SetReminderMark(model.ReminderMark{
				AccountID: fixture.account.ID, Kind: "threshold", Threshold: 30,
				ExpiresAt: "2027-01-01T00:00:00Z", State: "sent",
			})
			return nil
		}},
		{"prune-reminder-marks", func() error {
			st.PruneReminderMarks(map[string]string{})
			return nil
		}},
		{"mark-scoped-key-used", func() error {
			st.MarkScopedKeyUsed(scopedKey.ID)
			return nil
		}},
	}
	for _, step := range steps {
		before := j.Seq()
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := j.Seq(); got != before+1 {
			t.Fatalf("%s advanced the journal by %d frames, want exactly 1", step.name, got-before)
		}
	}
}

// TestJournalPersistedStateSurvivesReplay proves the paths the gate listed are
// not only committed but recoverable: sessions, outbox, delivery history,
// schedule state, scoped-key use and the request log all come back from the
// log with no checkpoint.
func TestJournalPersistedStateSurvivesReplay(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	fixture := newJournalFixture(t, st)

	schedule, err := st.CreateSchedule(journalWireSchedule("replay-schedule"), "operator")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	scopedKey, _, err := st.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: fixture.user.ID, Label: "replay-key", Scopes: []model.Scope{model.ScopeAccountsRead},
	}, "operator")
	if err != nil {
		t.Fatalf("CreateScopedKey: %v", err)
	}
	cookie, _, err := st.CreatePortalSession(fixture.tenant.ID)
	if err != nil {
		t.Fatalf("CreatePortalSession: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := st.PutWebhookOutbox(journalOutboxEntry("replay-evt", "replay-hook", `{"accountId":"acc-01"}`, now)); err != nil {
		t.Fatalf("PutWebhookOutbox: %v", err)
	}
	if err := st.AppendWebhookDelivery(model.WebhookDelivery{
		ID: "replay-evt:1", EndpointID: "replay-hook", Event: "account.login",
		Attempt: 1, Status: "success", HTTPStatus: 200, At: now,
	}); err != nil {
		t.Fatalf("AppendWebhookDelivery: %v", err)
	}
	if _, err := st.AppendRequest(model.RequestLog{
		Source: model.SourceGate, AccountLabel: "journal-orders",
		Target: "example.com:443", Status: 200, Bytes: 77,
	}); err != nil {
		t.Fatalf("AppendRequest: %v", err)
	}
	st.AppendScheduleRun(model.ScheduleRun{
		ScheduleID: schedule.ID, StartedAt: now, State: "succeeded", Result: "ok",
	})
	st.RecordScheduleFire(model.ScheduleFire{
		Key: schedule.ID + "@9", ScheduleID: schedule.ID, State: "succeeded", ReservedAt: now,
	})
	if _, err := st.MutateSchedule(schedule.ID, func(s *model.Schedule) { s.Cursor = 11 }); err != nil {
		t.Fatalf("MutateSchedule: %v", err)
	}
	st.MarkScopedKeyUsed(scopedKey.ID)
	seq := j.Seq()
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestJournal(t, dir)
	recovered := store.NewEmpty()
	report, err := reopened.Recover(recovered)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.LastSeq != seq {
		t.Fatalf("recovered last seq = %d, want %d", report.LastSeq, seq)
	}
	if _, _, _, err := recovered.AuthenticatePortalSession(cookie, time.Now().UTC()); err != nil {
		t.Fatalf("replayed portal session: %v", err)
	}
	outbox := recovered.WebhookOutbox()
	if len(outbox) != 1 || outbox[0].ID != "replay-evt" {
		t.Fatalf("replayed outbox = %+v, want the pending entry", outbox)
	}
	if deliveries := recovered.WebhookDeliveries("replay-hook"); len(deliveries) != 1 {
		t.Fatalf("replayed deliveries = %+v, want one row", deliveries)
	}
	if rows := recovered.Requests(); len(rows) != 1 || rows[0].Bytes != 77 {
		t.Fatalf("replayed request rows = %+v, want the metered row", rows)
	}
	if runs := recovered.ScheduleRuns(schedule.ID, 0); len(runs) != 1 {
		t.Fatalf("replayed schedule runs = %+v, want one row", runs)
	}
	if fires := recovered.ScheduleFires(); len(fires) != 1 {
		t.Fatalf("replayed schedule fires = %+v, want one row", fires)
	}
	replayedSchedule, ok := recovered.Schedule(schedule.ID)
	if !ok || replayedSchedule.Cursor != 11 {
		t.Fatalf("replayed schedule = %+v, want cursor 11", replayedSchedule)
	}
	replayedKey, ok := recovered.ScopedKey(scopedKey.ID)
	if !ok || replayedKey.LastUsedAt == nil {
		t.Fatalf("replayed scoped key = %+v, want lastUsedAt set", replayedKey)
	}
}

// TestJournalCheckpointSkipsWithoutNewFrame pins the no-op half of the
// checkpoint contract: a timer tick with no newer frame must not rewrite
// CURRENT or archive another log.
func TestJournalCheckpointSkipsWithoutNewFrame(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	if _, err := st.CreateUser(model.CreateUserInput{Name: "skip-user"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := st.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	current := readJournalCurrent(t, dir)
	archives, err := filepath.Glob(filepath.Join(dir, "state.log.*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if err := st.Checkpoint(); err != nil {
		t.Fatalf("second Checkpoint: %v", err)
	}
	if got := readJournalCurrent(t, dir); !bytes.Equal(got, current) {
		t.Fatalf("a skipped checkpoint rewrote CURRENT (%s -> %s)", current, got)
	}
	after, err := filepath.Glob(filepath.Join(dir, "state.log.*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(after) != len(archives) {
		t.Fatalf("a skipped checkpoint archived %d logs, want %d", len(after), len(archives))
	}
}

// TestJournalCheckpointStageFailurePoisons makes the checkpoint directory
// unusable, which is the deterministic form of a mid-checkpoint disk failure:
// the store must poison, publish no CURRENT and close the fault channel.
func TestJournalCheckpointStageFailurePoisons(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	if _, err := st.CreateUser(model.CreateUserInput{Name: "checkpoint-fault"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	checkpointDir := filepath.Join(dir, "checkpoints")
	if err := os.RemoveAll(checkpointDir); err != nil {
		t.Fatalf("remove checkpoint dir: %v", err)
	}
	if err := os.WriteFile(checkpointDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("replace checkpoint dir: %v", err)
	}
	if err := st.Checkpoint(); err == nil {
		t.Fatal("Checkpoint succeeded with an unusable checkpoint directory")
	}
	if !errors.Is(st.DurabilityError(), store.ErrDurability) {
		t.Fatalf("DurabilityError = %v, want ErrDurability", st.DurabilityError())
	}
	select {
	case <-st.DurabilityFault():
	default:
		t.Fatal("DurabilityFault is not closed after a failed checkpoint")
	}
	if seq := journalCurrentSeq(t, dir); seq != 0 {
		t.Fatalf("CURRENT seq = %d after a failed checkpoint, want the previous 0", seq)
	}
}

// TestJournalCheckpointRefusesRestoredState proves a checkpoint cannot publish
// state that no frame committed. A restore marks the store diverged; the next
// mutation commits the whole post-state and clears the refusal.
func TestJournalCheckpointRefusesRestoredState(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	if _, err := st.CreateUser(model.CreateUserInput{Name: "before-restore"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	other := store.NewEmpty()
	if _, err := other.CreateUser(model.CreateUserInput{Name: "restored-user"}); err != nil {
		t.Fatalf("CreateUser on restore source: %v", err)
	}
	if err := st.Restore(mustSnapshot(t, other)); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if err := st.Checkpoint(); err == nil {
		t.Fatal("Checkpoint published restored state under the last committed sequence")
	}
	if _, err := st.CreateUser(model.CreateUserInput{Name: "after-restore"}); err != nil {
		t.Fatalf("CreateUser after restore: %v", err)
	}
	if err := st.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint after the post-restore commit: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestJournal(t, dir)
	recovered := store.NewEmpty()
	if _, err := reopened.Recover(recovered); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	names := make(map[string]bool)
	for _, user := range recovered.Users() {
		names[user.Name] = true
	}
	if !names["restored-user"] || !names["after-restore"] {
		t.Fatalf("recovered users = %v, want the restored state plus the new mutation", names)
	}
	if names["before-restore"] {
		t.Fatal("the pre-restore user survived a restore")
	}
}

// TestJournalRecoverRefusesHooks proves replay cannot publish an event: a store
// with a live sink installed is refused before any snapshot is applied.
func TestJournalRecoverRefusesHooks(t *testing.T) {
	dir := t.TempDir()
	j := openTestJournal(t, dir)
	st := store.NewEmpty()
	st.SetAuditHook(func(model.AuditEvent) {})
	if _, err := j.Recover(st); err == nil {
		t.Fatal("Recover accepted a store with a hook installed")
	}
}

// TestJournalConcurrentMutationAndCheckpoint runs the timer's checkpoint beside
// mutations under -race and proves recovery sees the last committed state.
func TestJournalConcurrentMutationAndCheckpoint(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 25 {
			if _, err := st.CreateUser(model.CreateUserInput{Name: fmt.Sprintf("race-%02d", i)}); err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 25 {
			if err := st.Checkpoint(); err != nil {
				errCh <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent checkpoint/mutation: %v", err)
	}
	seq := j.Seq()
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestJournal(t, dir)
	recovered := store.NewEmpty()
	report, err := reopened.Recover(recovered)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.LastSeq != seq {
		t.Fatalf("recovered last seq = %d, want %d", report.LastSeq, seq)
	}
	names := make(map[string]bool)
	for _, user := range recovered.Users() {
		names[user.Name] = true
	}
	for i := range 25 {
		if !names[fmt.Sprintf("race-%02d", i)] {
			t.Fatalf("recovered users are missing race-%02d after concurrent checkpoints", i)
		}
	}
}

// TestJournalCheckpointRefusesRestoreAfterCompaction is the gate probe for the
// guard order: a completed checkpoint leaves checkpointedSeq == Seq, and a
// restore right after it must not be exported through the no-new-frame skip.
func TestJournalCheckpointRefusesRestoreAfterCompaction(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	if _, err := st.CreateUser(model.CreateUserInput{Name: "committed"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := st.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	other := store.NewEmpty()
	if _, err := other.CreateUser(model.CreateUserInput{Name: "restored"}); err != nil {
		t.Fatalf("CreateUser on restore source: %v", err)
	}
	if err := st.Restore(mustSnapshot(t, other)); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if err := st.Checkpoint(); err == nil {
		t.Fatal("Checkpoint exported restored state through the no-new-frame skip")
	}
	if _, err := st.CreateUser(model.CreateUserInput{Name: "after-restore"}); err != nil {
		t.Fatalf("CreateUser after the refusal: %v", err)
	}
	if err := st.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint after the post-restore commit: %v", err)
	}
}

// TestScopedKeySameMinuteThrottleRefusesPoisonedStore pins the fast path: the
// throttle returns before the write lock, so it must consult the poison
// itself instead of reporting success for the rest of the minute.
func TestScopedKeySameMinuteThrottleRefusesPoisonedStore(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	st.AttachJournal(j)
	user, err := st.CreateUser(model.CreateUserInput{Name: "wire-scope"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	first, _, err := st.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: user.ID, Label: "wire-a", Scopes: []model.Scope{model.ScopeAccountsRead},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey first: %v", err)
	}
	second, _, err := st.CreateScopedKey(model.ScopedKeyCreateRequest{
		UserID: user.ID, Label: "wire-b", Scopes: []model.Scope{model.ScopeAccountsRead},
	})
	if err != nil {
		t.Fatalf("CreateScopedKey second: %v", err)
	}
	if err := st.MarkScopedKeyUsed(first.ID); err != nil {
		t.Fatalf("MarkScopedKeyUsed first: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := st.MarkScopedKeyUsed(second.ID); err == nil {
		t.Fatal("MarkScopedKeyUsed succeeded with a closed journal")
	}
	// first was already marked this minute: the throttle must not mask poison.
	if err := st.MarkScopedKeyUsed(first.ID); !errors.Is(err, store.ErrDurability) {
		t.Fatalf("same-minute MarkScopedKeyUsed error = %v, want ErrDurability", err)
	}
}
