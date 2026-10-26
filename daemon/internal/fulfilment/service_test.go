package fulfilment

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// fixture is one empty store with an android account, a free node, an enabled
// user, an active tenant linked to that user and one active plan — the minimum
// a paid order needs to reserve a serving lease. It uses only exported store
// commands, so the service tests exercise the same surface main wires.
type fixture struct {
	st     *store.Store
	tenant model.Tenant
	user   model.PanelUser
	plan   model.Plan
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Fulfilment Ops", Username: "fulfilment-ops", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 10,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-fulfilment", AccountID: account.ID, Label: "fulfilment-1", Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.91",
		Protocol: model.ProtocolSOCKS5, GatePort: 13091,
	}); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	plan, err := st.CreatePlan(model.PlanInput{
		Name: "Monthly 10 GiB", AmountMinor: 3000, Currency: "CNY",
		LeaseTTLSeconds: 3600, QuotaBytes: 10 << 30,
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	f := &fixture{st: st, plan: plan}
	f.tenant, f.user = f.addBuyer(t, "main")
	return f
}

// addBuyer creates one active tenant linked to a fresh enabled user, so a test
// can hold several orders in different states without tripping the one-open-
// order-per-user rule.
func (f *fixture) addBuyer(t *testing.T, key string) (model.Tenant, model.PanelUser) {
	t.Helper()
	user, err := f.st.CreateUser(model.CreateUserInput{
		Name: "fulfilment-user-" + key, Platform: "android", QuotaBytesMonth: new(int64(1000)),
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", key, err)
	}
	email := "fulfilment-" + key + "@example.com"
	if err := f.st.SignupTenant(model.TenantSignupInput{
		Email: email, Password: "correct horse battery staple", DisplayName: "Fulfilment " + key,
	}); err != nil {
		t.Fatalf("SignupTenant(%s): %v", key, err)
	}
	var tenant model.Tenant
	for _, candidate := range f.st.Tenants() {
		if candidate.Email == email {
			tenant = candidate
			break
		}
	}
	if tenant.ID == "" {
		t.Fatalf("signup %s did not store a tenant", email)
	}
	approved, err := f.st.ApproveTenant(tenant.ID, user.ID, nil)
	if err != nil {
		t.Fatalf("ApproveTenant(%s): %v", key, err)
	}
	return approved, user
}

// paidWithoutGrant settles one order while the buyer is suspended, so the
// legacy settlement path records the money but cannot activate. Re-enabling the
// buyer afterwards leaves exactly the state an initial fulfilment job starts
// from: paid, no grant, quota untouched.
func (f *fixture) paidWithoutGrant(t *testing.T, key string) model.Order {
	t.Helper()
	return f.paidWithoutGrantFor(t, f.tenant, f.user, key)
}

func (f *fixture) paidWithoutGrantFor(t *testing.T, tenant model.Tenant, user model.PanelUser, key string) model.Order {
	t.Helper()
	order, replayed, err := f.st.CreateOrder(tenant.ID, user.ID, f.plan.ID, key, "test")
	if err != nil {
		t.Fatalf("CreateOrder(%s): %v", key, err)
	}
	if replayed {
		t.Fatalf("CreateOrder(%s) replayed unexpectedly", key)
	}
	if _, err := f.st.SetUserEnabled(user.ID, false); err != nil {
		t.Fatalf("suspend user: %v", err)
	}
	paid, err := f.st.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
		Provider: model.ProviderManual, ID: "evt-" + key, Kind: model.PaymentSettled,
		OrderID: order.ID, ProviderRef: "receipt-" + key,
	}, "test")
	if err != nil {
		t.Fatalf("ApplyPaymentEvent(%s): %v", key, err)
	}
	if _, err := f.st.SetUserEnabled(user.ID, true); err != nil {
		t.Fatalf("resume user: %v", err)
	}
	if paid.State != model.OrderPaid || paid.Grant != nil {
		t.Fatalf("settled order = %+v, want paid without a grant", paid)
	}
	return paid
}

// queuedInitial is paidWithoutGrant plus the durable initial job.
func (f *fixture) queuedInitial(t *testing.T, key string) model.Order {
	t.Helper()
	return f.queuedInitialFor(t, f.tenant, f.user, key)
}

func (f *fixture) queuedInitialFor(t *testing.T, tenant model.Tenant, user model.PanelUser, key string) model.Order {
	t.Helper()
	paid := f.paidWithoutGrantFor(t, tenant, user, key)
	queued, err := f.st.EnqueueFulfilment(paid.ID, model.FulfilmentInitial)
	if err != nil {
		t.Fatalf("EnqueueFulfilment(%s): %v", key, err)
	}
	if queued.Fulfilment == nil || queued.Fulfilment.State != model.FulfilmentQueued {
		t.Fatalf("job = %+v, want one queued initial job", queued.Fulfilment)
	}
	return queued
}

func (f *fixture) jobOf(t *testing.T, orderID string) model.FulfilmentJob {
	t.Helper()
	order, ok := f.st.Order(orderID)
	if !ok {
		t.Fatalf("order %q missing", orderID)
	}
	if order.Fulfilment == nil {
		t.Fatalf("order %q has no fulfilment job", orderID)
	}
	return *order.Fulfilment
}

func (f *fixture) auditCount(action string) int {
	count := 0
	for _, event := range f.st.Audit(500) {
		if event.Action == action {
			count++
		}
	}
	return count
}

// fakePublisher records every publication and can fail, block until the
// attempt's deadline, wait for an explicit release, or mutate the store after a
// successful publication (the commit-conflict race).
type fakePublisher struct {
	mu           sync.Mutex
	calls        []model.Lease
	withdrawals  []model.Lease
	err          error
	withdrawErr  error
	block        bool
	started      chan struct{}
	release      chan struct{}
	afterPublish func(model.Lease) error
}

func (p *fakePublisher) PublishReservedLease(ctx context.Context, lease model.Lease) error {
	p.mu.Lock()
	p.calls = append(p.calls, lease)
	err, block, after := p.err, p.block, p.afterPublish
	started, release := p.started, p.release
	p.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if after != nil {
		return after(lease)
	}
	return nil
}

func (p *fakePublisher) WithdrawReservedLease(_ context.Context, lease model.Lease) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.withdrawals = append(p.withdrawals, lease)
	return p.withdrawErr
}

func (p *fakePublisher) withdrawCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.withdrawals)
}

func (p *fakePublisher) setWithdrawErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.withdrawErr = err
}

func (p *fakePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *fakePublisher) published(t *testing.T) []model.Lease {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]model.Lease(nil), p.calls...)
}

// fakeEvents records the post-commit transitions.
type fakeEvents struct {
	mu        sync.Mutex
	activated []model.Order
	created   []model.Lease
	reissued  []model.Lease
}

func (e *fakeEvents) OrderActivated(order model.Order) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activated = append(e.activated, order)
}

func (e *fakeEvents) LeaseCreated(lease model.Lease) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.created = append(e.created, lease)
}

func (e *fakeEvents) LeaseReissued(lease model.Lease, _ string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reissued = append(e.reissued, lease)
}

func (e *fakeEvents) counts() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.activated), len(e.created)
}

func testService(st Store, publisher Publisher) *Service {
	return New(Config{
		Store:        st,
		Publisher:    publisher,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ScanInterval: 10 * time.Millisecond,
	})
}

// waitFor polls a condition with a short interval. It is a test-only deadline,
// never a production sleep.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestFulfilNowPublishesThenCommits is the success row: one reserved lease is
// published, the store promotes it, and the order ends active with exactly one
// grant, no job, and one post-commit event pair.
func TestFulfilNowPublishesThenCommits(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "success")
	publisher := &fakePublisher{}
	events := &fakeEvents{}
	svc := testService(f.st, publisher)
	svc.events = events

	order, err := svc.FulfilNow(context.Background(), queued.ID)
	if err != nil {
		t.Fatalf("FulfilNow: %v", err)
	}
	if order.State != model.OrderActive || order.Grant == nil || order.Fulfilment != nil {
		t.Fatalf("order after fulfil = %+v, want active with a grant and no job", order)
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want 1", publisher.count())
	}
	published := publisher.published(t)[0]
	if published.ID != order.Grant.LeaseID {
		t.Fatalf("published lease %q, grant lease %q", published.ID, order.Grant.LeaseID)
	}
	if published.Status != model.LeasePendingPublication {
		t.Fatalf("published lease status = %s, want %s",
			published.Status, model.LeasePendingPublication)
	}
	leases := f.st.Leases()
	if len(leases) != 1 || leases[0].Status != model.LeaseActive ||
		leases[0].ID != order.Grant.LeaseID {
		t.Fatalf("leases = %+v, want the single promoted reservation", leases)
	}
	if grants := f.st.Grants(); len(grants) != 1 || grants[0].LeaseID != order.Grant.LeaseID {
		t.Fatalf("grants = %+v, want exactly one", grants)
	}
	if activated, created := events.counts(); activated != 1 || created != 1 {
		t.Fatalf("events = %d activated, %d lease.created; want one each", activated, created)
	}
	if attempts := svc.FulfilmentAttempts()["committed"][""]; attempts != 1 {
		t.Fatalf("committed counter = %d, want 1", attempts)
	}
}

// TestFulfilNowPublishFailureQueuesWithBackoff is the failure row: the
// publication error aborts the attempt, releases the reservation, records the
// typed reason and a future retry, and never grants service. The returned error
// carries only the reason code, never the publisher's text.
func TestFulfilNowPublishFailureQueuesWithBackoff(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "publish-fail")
	cause := errors.New("resin unavailable token=SECRET")
	publisher := &fakePublisher{err: cause}
	svc := testService(f.st, publisher)
	before := time.Now()

	order, err := svc.FulfilNow(context.Background(), queued.ID)
	if err == nil {
		t.Fatal("FulfilNow succeeded, want a publication error")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error lost its cause for classification: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "resin unavailable") {
		t.Fatalf("returned error leaked the publisher text: %v", err)
	}
	if !strings.Contains(err.Error(), string(model.FulfilmentReasonPublishFailed)) {
		t.Fatalf("error = %v, want the publish_failed reason", err)
	}
	if order.State != model.OrderPaid || order.Grant != nil {
		t.Fatalf("order after failed fulfil = %+v, want paid without a grant", order)
	}
	job := f.jobOf(t, queued.ID)
	if job.State != model.FulfilmentQueued || job.Attempts != 1 ||
		job.LastReason != model.FulfilmentReasonPublishFailed || job.LastError == "" {
		t.Fatalf("job = %+v, want one failed queued attempt", job)
	}
	if strings.Contains(job.LastError, "SECRET") {
		t.Fatalf("durable detail kept the secret: %q", job.LastError)
	}
	if job.NextAttemptAt == nil {
		t.Fatalf("job = %+v, want a retry schedule", job)
	}
	next, ok := parseTimestamp(*job.NextAttemptAt)
	if !ok || !next.After(before) {
		t.Fatalf("nextAttemptAt = %q, want a future time", *job.NextAttemptAt)
	}
	if delay := next.Sub(before); delay > 10*time.Second {
		t.Fatalf("retry delay = %s, want the 5s first backoff", delay)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("failed attempt left %+v, want the reservation released", f.st.Leases())
	}
	if grants := f.st.Grants(); len(grants) != 0 {
		t.Fatalf("failed attempt granted %+v", grants)
	}
}

// TestFulfilNowPublishTimeoutQueues proves an attempt that does not answer
// before its deadline is aborted as a timeout instead of being left publishing
// or treated as success.
func TestFulfilNowPublishTimeoutQueues(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "timeout")
	publisher := &fakePublisher{block: true}
	svc := testService(f.st, publisher)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	order, err := svc.FulfilNow(ctx, queued.ID)
	if err == nil {
		t.Fatal("FulfilNow succeeded, want a timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if order.State != model.OrderPaid || order.Grant != nil {
		t.Fatalf("order after timeout = %+v, want paid without a grant", order)
	}
	job := f.jobOf(t, queued.ID)
	if job.State != model.FulfilmentQueued || job.Attempts != 1 ||
		job.LastReason != model.FulfilmentReasonTimeout || job.NextAttemptAt == nil {
		t.Fatalf("job = %+v, want a queued timeout with a retry", job)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("timed-out attempt left %+v, want the reservation released", f.st.Leases())
	}
}

// TestFulfilNowDuplicateAttemptsGrantOnce runs two FulfilNow calls while the
// first publication is in flight. The second must find the job publishing and
// return without a second publication, so the order still ends with exactly
// one grant and one lease.
func TestFulfilNowDuplicateAttemptsGrantOnce(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "duplicate")
	publisher := &fakePublisher{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc := testService(f.st, publisher)

	done := make(chan error, 1)
	go func() {
		_, err := svc.FulfilNow(context.Background(), queued.ID)
		done <- err
	}()
	select {
	case <-publisher.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first attempt never reached the publisher")
	}

	second, err := svc.FulfilNow(context.Background(), queued.ID)
	if err != nil {
		t.Fatalf("duplicate FulfilNow: %v", err)
	}
	if second.State != model.OrderPaid {
		t.Fatalf("duplicate saw %s, want the in-flight paid order", second.State)
	}
	close(publisher.release)
	if err := <-done; err != nil {
		t.Fatalf("first FulfilNow: %v", err)
	}

	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want exactly one", publisher.count())
	}
	order, ok := f.st.Order(queued.ID)
	if !ok || order.State != model.OrderActive || order.Grant == nil || order.Fulfilment != nil {
		t.Fatalf("order = %+v, want one committed fulfilment", order)
	}
	if grants := f.st.Grants(); len(grants) != 1 {
		t.Fatalf("grants = %+v, want exactly one", grants)
	}
	if leases := f.st.Leases(); len(leases) != 1 {
		t.Fatalf("leases = %+v, want exactly one", leases)
	}
}

// TestFulfilNowCommitConflictKeepsPublishedLease is the P0 fence: a successful
// publication followed by a refused commit must keep the reservation and the
// durable marker, schedule a commit retry, and never republish. Once the
// blocker clears, the retry commits the same lease.
func TestFulfilNowCommitConflictKeepsPublishedLease(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "commit-conflict")
	publisher := &fakePublisher{afterPublish: func(model.Lease) error {
		// Make the store refuse the commit after the external publication
		// succeeded: the buyer is suspended between the two phases.
		_, err := f.st.SetUserEnabled(f.user.ID, false)
		return err
	}}
	svc := testService(f.st, publisher)

	order, err := svc.FulfilNow(context.Background(), queued.ID)
	if err == nil {
		t.Fatal("FulfilNow succeeded, want the commit refusal")
	}
	if order.State != model.OrderPaid || order.Grant != nil {
		t.Fatalf("order after refused commit = %+v, want paid without a grant", order)
	}
	job := f.jobOf(t, queued.ID)
	if job.State != model.FulfilmentPublishing || job.Attempts != 2 ||
		job.PublishedLeaseID == "" || job.PublishedAt == nil ||
		job.PublishedLeaseID != job.AttemptLeaseID || job.NextAttemptAt == nil {
		t.Fatalf("job = %+v, want a published attempt awaiting its commit retry", job)
	}
	if leases := f.st.Leases(); len(leases) != 1 ||
		leases[0].ID != job.PublishedLeaseID || leases[0].Status != model.LeasePendingPublication {
		t.Fatalf("leases = %+v, want the published reservation kept", leases)
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want one", publisher.count())
	}

	// The commit retry must not publish again; clearing the blocker lets the
	// same published lease commit.
	if _, err := f.st.SetUserEnabled(f.user.ID, true); err != nil {
		t.Fatalf("resume user: %v", err)
	}
	committed, err := svc.FulfilNow(context.Background(), queued.ID)
	if err != nil {
		t.Fatalf("commit retry: %v", err)
	}
	if committed.State != model.OrderActive || committed.Grant == nil ||
		committed.Grant.LeaseID != job.PublishedLeaseID || committed.Fulfilment != nil {
		t.Fatalf("order after commit retry = %+v, want the published lease granted", committed)
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d after commit retry, want no republish", publisher.count())
	}
}

// TestFulfilNowCommitFailureDeadLettersAndReleases proves a published attempt
// that cannot commit is escalated to an operator item after the attempt budget,
// and that its reservation is released instead of being left externally
// accepted and locally queued.
func TestFulfilNowCommitFailureDeadLettersAndReleases(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "commit-dead")
	publisher := &fakePublisher{afterPublish: func(model.Lease) error {
		_, err := f.st.SetUserEnabled(f.user.ID, false)
		return err
	}}
	svc := testService(f.st, publisher)

	for i := range store.MaxFulfilmentAttempts {
		if _, err := svc.FulfilNow(context.Background(), queued.ID); err == nil {
			t.Fatalf("attempt %d succeeded, want a commit refusal", i+1)
		}
	}
	job := f.jobOf(t, queued.ID)
	if job.State != model.FulfilmentDeadLetter || !job.Blocked ||
		job.Attempts != store.MaxFulfilmentAttempts ||
		job.PublishedLeaseID != "" || job.AttemptLeaseID != "" || job.NextAttemptAt != nil {
		t.Fatalf("job = %+v, want a dead-lettered job without a reservation", job)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("leases = %+v, want the published reservation released", f.st.Leases())
	}
	if grants := f.st.Grants(); len(grants) != 0 {
		t.Fatalf("grants = %+v, want none", grants)
	}
	if f.auditCount("fulfilment.dead_letter") != 1 {
		t.Fatalf("dead-letter audits = %d, want 1", f.auditCount("fulfilment.dead_letter"))
	}
	if svc.FulfilmentDeadLetters() != 1 {
		t.Fatalf("dead-letter counter = %d, want 1", svc.FulfilmentDeadLetters())
	}
	if publisher.withdrawCount() != 1 {
		t.Fatalf("withdrawals = %d, want the published reservation withdrawn once", publisher.withdrawCount())
	}
	var dead *model.AuditEvent
	for _, event := range f.st.Audit(500) {
		if event.Action == "fulfilment.dead_letter" {
			copied := event
			dead = &copied
		}
	}
	if dead == nil || dead.Meta["paidAgeSeconds"] == "" {
		t.Fatalf("dead-letter audit = %+v, want the paid age metadata", dead)
	}
}

// TestFulfilNowBeginRefusalSchedulesAndDeadLetters proves a refusal that
// reserved nothing still consumes attempts, follows the backoff schedule, and
// reaches dead letter. The first blocker is a critical audit; repeats are
// warnings.
func TestFulfilNowBeginRefusalSchedulesAndDeadLetters(t *testing.T) {
	f := newFixture(t)
	publisher := &fakePublisher{}
	svc := testService(f.st, publisher)

	// Order A takes the account's only slot.
	first := f.queuedInitial(t, "slot-holder")
	if _, err := svc.FulfilNow(context.Background(), first.ID); err != nil {
		t.Fatalf("fulfil slot holder: %v", err)
	}
	// A second buyer's paid order now has nowhere to reserve.
	tenant, user := f.addBuyer(t, "blocked")
	blocked := f.queuedInitialFor(t, tenant, user, "blocked")
	before := time.Now()

	order, err := svc.FulfilNow(context.Background(), blocked.ID)
	if err == nil {
		t.Fatal("FulfilNow succeeded, want a capacity refusal")
	}
	if order.State != model.OrderPaid || order.Grant != nil {
		t.Fatalf("order after refusal = %+v, want paid without a grant", order)
	}
	job := f.jobOf(t, blocked.ID)
	if job.State != model.FulfilmentQueued || job.Attempts != 1 ||
		job.LastReason != model.FulfilmentReasonCapacity || job.NextAttemptAt == nil {
		t.Fatalf("job = %+v, want one scheduled refusal", job)
	}
	next, ok := parseTimestamp(*job.NextAttemptAt)
	if !ok || !next.After(before) {
		t.Fatalf("nextAttemptAt = %q, want a future retry", *job.NextAttemptAt)
	}
	if f.auditCount("fulfilment.blocked") != 1 {
		t.Fatalf("blocked audits = %d, want 1 for the first blocker", f.auditCount("fulfilment.blocked"))
	}
	if f.auditCount("fulfilment.retry") != 0 {
		t.Fatalf("retry audits = %d, want none yet", f.auditCount("fulfilment.retry"))
	}

	// Each further refusal consumes an attempt; the eighth dead-letters.
	for i := 1; i < store.MaxFulfilmentAttempts; i++ {
		if _, err := svc.FulfilNow(context.Background(), blocked.ID); err == nil {
			t.Fatalf("refusal %d succeeded", i+1)
		}
	}
	job = f.jobOf(t, blocked.ID)
	if job.State != model.FulfilmentDeadLetter || !job.Blocked ||
		job.Attempts != store.MaxFulfilmentAttempts || job.NextAttemptAt != nil {
		t.Fatalf("job = %+v, want a dead-lettered refusal", job)
	}
	if f.auditCount("fulfilment.blocked") != 1 {
		t.Fatalf("blocked audits = %d, want the first blocker only", f.auditCount("fulfilment.blocked"))
	}
	if f.auditCount("fulfilment.retry") != store.MaxFulfilmentAttempts-2 {
		t.Fatalf("retry audits = %d, want %d",
			f.auditCount("fulfilment.retry"), store.MaxFulfilmentAttempts-2)
	}
	if f.auditCount("fulfilment.dead_letter") != 1 {
		t.Fatalf("dead-letter audits = %d, want 1", f.auditCount("fulfilment.dead_letter"))
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want only the slot holder", publisher.count())
	}
}

// TestFulfilNowSanitizesPublisherErrors proves the publisher's raw text never
// reaches the returned error, the worker log, or the durable job.
func TestFulfilNowSanitizesPublisherErrors(t *testing.T) {
	const secret = "GATE_SECRET"
	const url = "https://user:pass@example.test/x"
	cause := errors.New("upstream token=" + secret + " signed=" + url)

	f := newFixture(t)
	queued := f.queuedInitial(t, "sanitize")
	svc := testService(f.st, &fakePublisher{err: cause})
	_, err := svc.FulfilNow(context.Background(), queued.ID)
	if err == nil {
		t.Fatal("FulfilNow succeeded, want an error")
	}
	for _, needle := range []string{secret, "user:pass", "example.test"} {
		if strings.Contains(err.Error(), needle) {
			t.Fatalf("returned error leaked %q: %v", needle, err)
		}
	}
	if job := f.jobOf(t, queued.ID); strings.Contains(job.LastError, secret) ||
		strings.Contains(job.LastError, "user:pass") {
		t.Fatalf("durable detail leaked the publisher text: %q", job.LastError)
	}

	// The worker log is the other sink: run the real loop and inspect the
	// production log line.
	g := newFixture(t)
	logged := new(bytes.Buffer)
	worker := New(Config{
		Store:        g.st,
		Publisher:    &fakePublisher{err: cause},
		Logger:       slog.New(slog.NewTextHandler(logged, nil)),
		ScanInterval: 10 * time.Millisecond,
	})
	g.queuedInitial(t, "sanitize-log")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	waitFor(t, "the failing attempt to be logged", func() bool {
		return worker.FulfilmentAttempts()["publish_failed"] != nil
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	output := logged.String()
	if !strings.Contains(output, "fulfilment_attempt_failed") {
		t.Fatalf("missing failure log: %s", output)
	}
	for _, needle := range []string{secret, "user:pass", "example.test"} {
		if strings.Contains(output, needle) {
			t.Fatalf("worker log leaked %q: %s", needle, output)
		}
	}
}

// TestFulfilNowTerminalRaceCleansUpPublishedAttempt proves a terminal reversal
// that lands between publication and commit leaves no grant, no job and no
// reservation, and the attempt reports failure instead of claiming service.
func TestFulfilNowTerminalRaceCleansUpPublishedAttempt(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "terminal-race")
	publisher := &fakePublisher{afterPublish: func(model.Lease) error {
		_, err := f.st.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
			Provider: model.ProviderManual, ID: "evt-terminal-race-cb",
			Kind: model.PaymentChargedBack, OrderID: queued.ID,
			ProviderRef: "cb-terminal-race", AmountMinor: f.plan.AmountMinor,
			Currency: f.plan.Currency,
		}, "test")
		return err
	}}
	svc := testService(f.st, publisher)

	order, err := svc.FulfilNow(context.Background(), queued.ID)
	if err == nil {
		t.Fatal("FulfilNow succeeded after a terminal reversal")
	}
	if order.State != model.OrderChargedBack || order.Grant != nil || order.Fulfilment != nil {
		t.Fatalf("order after terminal race = %+v, want charged_back without work", order)
	}
	if leases := f.st.Leases(); len(leases) != 0 {
		t.Fatalf("leases = %+v, want the reservation released", leases)
	}
	if grants := f.st.Grants(); len(grants) != 0 {
		t.Fatalf("grants = %+v, want none", grants)
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want one", publisher.count())
	}
	// The publication is withdrawn before the local identity is dropped.
	if publisher.withdrawCount() != 1 {
		t.Fatalf("withdrawals = %d, want the published lease withdrawn", publisher.withdrawCount())
	}
}

// TestFulfilNowTerminalRaceWithdrawalFailureAlertsOrphan proves a terminal
// transition before the marker, with a failing withdrawal, leaves a critical
// orphan alert carrying the published lease/account identity.
func TestFulfilNowTerminalRaceWithdrawalFailureAlertsOrphan(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "terminal-orphan")
	publisher := &fakePublisher{
		afterPublish: func(model.Lease) error {
			_, err := f.st.ApplyPaymentEvent(model.ProviderManual, model.PaymentEvent{
				Provider: model.ProviderManual, ID: "evt-terminal-orphan-cb",
				Kind: model.PaymentChargedBack, OrderID: queued.ID,
				ProviderRef: "cb-terminal-orphan", AmountMinor: f.plan.AmountMinor,
				Currency: f.plan.Currency,
			}, "test")
			return err
		},
		withdrawErr: errors.New("panel unavailable"),
	}
	svc := testService(f.st, publisher)

	if _, err := svc.FulfilNow(context.Background(), queued.ID); err == nil {
		t.Fatal("FulfilNow succeeded after a terminal reversal")
	}
	if f.auditCount("fulfilment.cleanup_orphaned") != 1 {
		t.Fatalf("orphan alerts = %d, want 1", f.auditCount("fulfilment.cleanup_orphaned"))
	}
	order, _ := f.st.Order(queued.ID)
	if order.State != model.OrderChargedBack || order.Grant != nil || order.Fulfilment != nil {
		t.Fatalf("order = %+v, want charged_back without work", order)
	}
	if publisher.count() != 1 || publisher.withdrawCount() != 1 {
		t.Fatalf("publications/withdrawals = %d/%d, want 1/1",
			publisher.count(), publisher.withdrawCount())
	}
	// A terminal order with no job must never mint another publication.
	if _, err := svc.FulfilNow(context.Background(), queued.ID); err != nil {
		t.Fatalf("second FulfilNow: %v", err)
	}
	if publisher.count() != 1 {
		t.Fatalf("terminal order published again: %d", publisher.count())
	}
}

// TestFulfilNowMarkFailureDoesNotCommit proves a failed publication marker can
// never fall through to a commit: the attempt is aborted and retried, and no
// grant is created.
func TestFulfilNowMarkFailureDoesNotCommit(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "mark-failure")
	publisher := &fakePublisher{afterPublish: func(lease model.Lease) error {
		// Drop the reservation between publication and the marker write.
		return f.st.ReleaseLease(lease.ID)
	}}
	svc := testService(f.st, publisher)

	order, err := svc.FulfilNow(context.Background(), queued.ID)
	if err == nil {
		t.Fatal("FulfilNow committed without a publication marker")
	}
	if order.State != model.OrderPaid || order.Grant != nil {
		t.Fatalf("order after mark failure = %+v, want paid without a grant", order)
	}
	if grants := f.st.Grants(); len(grants) != 0 {
		t.Fatalf("mark failure created grants: %+v", grants)
	}
	job := f.jobOf(t, queued.ID)
	if job.State != model.FulfilmentQueued || job.Attempts != 1 || job.PublishedLeaseID != "" {
		t.Fatalf("job after mark failure = %+v, want a queued unmarked retry", job)
	}
	if publisher.count() != 1 || publisher.withdrawCount() != 1 {
		t.Fatalf("publications/withdrawals = %d/%d, want 1/1",
			publisher.count(), publisher.withdrawCount())
	}
}

// TestFulfilNowMarkFailureWithdrawalFailureBlocksRetry proves an unconfirmed
// publication whose withdrawal fails is retained as a cleanup-pending identity:
// the next FulfilNow retries the withdrawal and never mints a second lease.
func TestFulfilNowMarkFailureWithdrawalFailureBlocksRetry(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "mark-cleanup")
	publisher := &fakePublisher{
		afterPublish: func(lease model.Lease) error {
			// The marker write fails because the reservation vanished.
			return f.st.ReleaseLease(lease.ID)
		},
		withdrawErr: errors.New("panel unavailable token=SECRET"),
	}
	svc := testService(f.st, publisher)

	if _, err := svc.FulfilNow(context.Background(), queued.ID); err == nil {
		t.Fatal("FulfilNow succeeded without a marker")
	}
	job := f.jobOf(t, queued.ID)
	if !job.CleanupPending || job.State != model.FulfilmentPublishing ||
		job.PublishedLeaseID == "" || job.PublishedAccountID == "" ||
		job.PublishedAt == nil || job.NextAttemptAt == nil {
		t.Fatalf("job = %+v, want a retained cleanup-pending identity", job)
	}
	order, _ := f.st.Order(queued.ID)
	if !order.ReviewRequired || order.State != model.OrderPaid || order.Grant != nil {
		t.Fatalf("order = %+v, want paid under review without a grant", order)
	}
	if strings.Contains(job.LastError, "SECRET") {
		t.Fatalf("retained detail leaked the publisher text: %q", job.LastError)
	}

	// The retry must not publish a second identity while cleanup is pending.
	if _, err := svc.FulfilNow(context.Background(), queued.ID); err == nil {
		t.Fatal("cleanup retry succeeded with the panel still unavailable")
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want the retry to reuse the retained identity", publisher.count())
	}
	if current := f.jobOf(t, queued.ID); current.PublishedLeaseID != job.PublishedLeaseID {
		t.Fatalf("retry minted a new identity: %q -> %q",
			job.PublishedLeaseID, current.PublishedLeaseID)
	}

	// Once the panel accepts the withdrawal, the retained identity is released
	// and the job becomes an operator item.
	publisher.setWithdrawErr(nil)
	completed, err := svc.FulfilNow(context.Background(), queued.ID)
	if err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if completed.Fulfilment == nil || completed.Fulfilment.State != model.FulfilmentDeadLetter {
		t.Fatalf("order after cleanup = %+v, want a dead letter", completed.Fulfilment)
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want no new identity", publisher.count())
	}
}

// TestFulfilNowWithdrawalFailureKeepsCleanupPending proves a failed external
// withdrawal never releases the published identity or dead-letters the job as
// if cleanup succeeded: the job parks as cleanup-pending with a retry schedule,
// and a later successful withdrawal completes the cleanup.
func TestFulfilNowWithdrawalFailureKeepsCleanupPending(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "cleanup-pending")
	publisher := &fakePublisher{
		afterPublish: func(model.Lease) error {
			_, err := f.st.SetUserEnabled(f.user.ID, false)
			return err
		},
		withdrawErr: errors.New("panel unavailable token=SECRET"),
	}
	svc := testService(f.st, publisher)

	for i := range store.MaxFulfilmentAttempts {
		if _, err := svc.FulfilNow(context.Background(), queued.ID); err == nil {
			t.Fatalf("attempt %d succeeded, want a commit refusal", i+1)
		}
	}
	job := f.jobOf(t, queued.ID)
	if job.State != model.FulfilmentPublishing || !job.CleanupPending ||
		job.PublishedLeaseID == "" || job.PublishedAt == nil || job.NextAttemptAt == nil {
		t.Fatalf("job = %+v, want a cleanup-pending published attempt", job)
	}
	order, _ := f.st.Order(queued.ID)
	if !order.ReviewRequired || order.State != model.OrderPaid || order.Grant != nil {
		t.Fatalf("order = %+v, want paid under review without a grant", order)
	}
	if leases := f.st.Leases(); len(leases) != 1 ||
		leases[0].ID != job.PublishedLeaseID || leases[0].Status != model.LeasePendingPublication {
		t.Fatalf("leases = %+v, want the published reservation retained", leases)
	}
	if f.auditCount("fulfilment.dead_letter") != 0 {
		t.Fatal("failed withdrawal dead-lettered the job")
	}
	if f.auditCount("fulfilment.cleanup_pending") != 1 {
		t.Fatalf("cleanup-pending alerts = %d, want 1", f.auditCount("fulfilment.cleanup_pending"))
	}
	if strings.Contains(job.LastError, "SECRET") {
		t.Fatalf("cleanup detail leaked the publisher text: %q", job.LastError)
	}

	// Once the publisher accepts the withdrawal, the retained identity is
	// released and the job becomes an operator item.
	publisher.setWithdrawErr(nil)
	completed, err := svc.FulfilNow(context.Background(), queued.ID)
	if err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if completed.Fulfilment == nil || completed.Fulfilment.State != model.FulfilmentDeadLetter {
		t.Fatalf("order after cleanup = %+v, want a dead letter", completed.Fulfilment)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("leases = %+v, want the reservation released after cleanup", f.st.Leases())
	}
	if f.auditCount("fulfilment.dead_letter") != 1 {
		t.Fatalf("dead-letter audits = %d, want 1", f.auditCount("fulfilment.dead_letter"))
	}
}

// TestRunProcessesDueJobOnce proves the loop wakes for a due job, attempts it
// once, and does not repeat work the store already committed.
func TestRunProcessesDueJobOnce(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "run-once")
	publisher := &fakePublisher{}
	svc := testService(f.st, publisher)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(ctx)
	}()
	waitFor(t, "the due job to commit", func() bool {
		order, ok := f.st.Order(queued.ID)
		return ok && order.State == model.OrderActive && order.Fulfilment == nil
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want one attempt", publisher.count())
	}
	if grants := f.st.Grants(); len(grants) != 1 {
		t.Fatalf("grants = %+v, want exactly one", grants)
	}
}

// TestRunMultipleWorkersGrantOnce runs two workers over the same store. The
// store gate must leave exactly one publication and one grant.
func TestRunMultipleWorkersGrantOnce(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "multi-worker")
	publisher := &fakePublisher{}
	first := testService(f.st, publisher)
	second := testService(f.st, publisher)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	for _, svc := range []*Service{first, second} {
		go func(svc *Service) {
			svc.Run(ctx)
			done <- struct{}{}
		}(svc)
	}
	waitFor(t, "the job to commit", func() bool {
		order, ok := f.st.Order(queued.ID)
		return ok && order.State == model.OrderActive && order.Fulfilment == nil
	})
	cancel()
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a worker did not stop after cancellation")
		}
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want exactly one", publisher.count())
	}
	if grants := f.st.Grants(); len(grants) != 1 {
		t.Fatalf("grants = %+v, want exactly one", grants)
	}
	if leases := f.st.Leases(); len(leases) != 1 {
		t.Fatalf("leases = %+v, want exactly one", leases)
	}
}

// TestRunCancellationDuringPublish proves a shutdown mid-publication aborts the
// reservation and leaves a scheduled retry instead of a stuck publishing job.
func TestRunCancellationDuringPublish(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "cancel-publish")
	publisher := &fakePublisher{block: true, started: make(chan struct{})}
	svc := testService(f.st, publisher)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(ctx)
	}()
	select {
	case <-publisher.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never reached the publisher")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	job := f.jobOf(t, queued.ID)
	if job.State != model.FulfilmentQueued || job.Attempts != 1 ||
		job.LastReason != model.FulfilmentReasonTimeout || job.NextAttemptAt == nil {
		t.Fatalf("job = %+v, want a queued timeout after cancellation", job)
	}
	if len(f.st.Leases()) != 0 {
		t.Fatalf("leases = %+v, want the reservation released", f.st.Leases())
	}
	if grants := f.st.Grants(); len(grants) != 0 {
		t.Fatalf("grants = %+v, want none", grants)
	}
}

// TestRunRecoversInterruptedAttempt is the boot row: a job left publishing by a
// previous process is requeued without counting a new attempt, then published
// and committed by the loop.
func TestRunRecoversInterruptedAttempt(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "recovery")
	attempt, err := f.st.BeginFulfilment(queued.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	if attempt.Number != 1 || attempt.Lease.Status != model.LeasePendingPublication {
		t.Fatalf("attempt = %+v, want one pending reservation", attempt)
	}
	publisher := &fakePublisher{}
	svc := testService(f.st, publisher)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(ctx)
	}()
	waitFor(t, "the interrupted attempt to commit", func() bool {
		order, ok := f.st.Order(queued.ID)
		return ok && order.State == model.OrderActive && order.Fulfilment == nil
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if publisher.count() != 1 {
		t.Fatalf("publications = %d, want one attempt after recovery", publisher.count())
	}
	order, _ := f.st.Order(queued.ID)
	if order.Grant == nil {
		t.Fatalf("order = %+v, want a grant", order)
	}
	if order.Grant.LeaseID == attempt.LeaseID {
		t.Fatalf("grant kept the interrupted reservation %q", attempt.LeaseID)
	}
	leases := f.st.Leases()
	if len(leases) != 1 || leases[0].Status != model.LeaseActive ||
		leases[0].ID != order.Grant.LeaseID {
		t.Fatalf("leases = %+v, want the interrupted row released and one active row", leases)
	}
}

// TestRunRecoversPublishedAttemptWithoutRepublishing proves a published
// reservation survives a restart with its marker and is committed, not
// republished.
func TestRunRecoversPublishedAttemptWithoutRepublishing(t *testing.T) {
	f := newFixture(t)
	queued := f.queuedInitial(t, "recover-published")
	attempt, err := f.st.BeginFulfilment(queued.ID)
	if err != nil {
		t.Fatalf("BeginFulfilment: %v", err)
	}
	if _, err := f.st.MarkFulfilmentPublished(queued.ID, attempt.LeaseID); err != nil {
		t.Fatalf("MarkFulfilmentPublished: %v", err)
	}
	publisher := &fakePublisher{}
	svc := testService(f.st, publisher)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(ctx)
	}()
	waitFor(t, "the published attempt to commit", func() bool {
		order, ok := f.st.Order(queued.ID)
		return ok && order.State == model.OrderActive && order.Fulfilment == nil
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if publisher.count() != 0 {
		t.Fatalf("publications = %d, want none for a published attempt", publisher.count())
	}
	order, _ := f.st.Order(queued.ID)
	if order.Grant == nil || order.Grant.LeaseID != attempt.LeaseID {
		t.Fatalf("order = %+v, want the published lease committed", order)
	}
}
