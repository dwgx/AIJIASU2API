// Package fulfilment drives one order's durable publication work: it reserves
// a serving lease in the store, publishes that exact lease through the same
// Panel/LeasePublishHook boundary a lease create uses, and only then commits the
// entitlement. The store owns every decision that must not be duplicated — the
// single-winner attempt gate, the provisional reservation, the publication
// marker, the commit and the retry schedule — so this package is the external
// half of the transaction and never activates an order itself.
package fulfilment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// DefaultScanInterval is the fallback wake interval of Run. A queued or
// published job is woken at its own NextAttemptAt when that is sooner; the
// fallback covers a schedule the store cannot express through DueFulfilments
// and bounds the delay between a refused attempt and its retry.
const DefaultScanInterval = 15 * time.Second

// Store is the narrow store surface fulfilment needs. Every method is an
// existing store command; the service adds no state of its own, so a restart
// resumes from the durable job exactly where the previous process stopped.
type Store interface {
	// DueFulfilments returns the jobs that need an attempt now: queued work
	// and published attempts waiting for their commit retry.
	DueFulfilments(now time.Time) []model.Order
	// BeginFulfilment is the single-winner attempt gate: it validates the
	// order, increments the attempt count and reserves the provisional lease
	// the caller must publish. A typed FulfilmentRefusal means nothing was
	// reserved.
	BeginFulfilment(orderID string) (store.FulfilmentAttempt, error)
	// MarkFulfilmentPublished durably records that the attempt's lease was
	// accepted by the publisher, before any commit is attempted.
	MarkFulfilmentPublished(orderID, attemptLeaseID string) (model.Order, error)
	// CommitFulfilment promotes the published attempt into the entitlement.
	CommitFulfilment(orderID, attemptLeaseID string) (model.Order, error)
	// RetryPublishedFulfilment schedules another commit for an already
	// published reservation. It refuses when the attempt requires external
	// cleanup instead of dead-lettering it.
	RetryPublishedFulfilment(orderID, attemptLeaseID string, reason model.FulfilmentReason, detail string, retryable bool) (model.Order, error)
	// MarkFulfilmentCleanupPending parks a published attempt whose external
	// withdrawal failed, keeping its identity and a retry schedule.
	MarkFulfilmentCleanupPending(orderID, attemptLeaseID string, reason model.FulfilmentReason, detail string) (model.Order, error)
	// RetainPublishedFulfilment durably records a publication whose marker
	// write failed and whose withdrawal also failed, before any local release.
	RetainPublishedFulfilment(orderID, attemptLeaseID, accountID string, reason model.FulfilmentReason, detail string) (model.Order, error)
	// RecordFulfilmentCleanupOrphan alerts on a publication whose local job is
	// already gone and whose withdrawal failed.
	RecordFulfilmentCleanupOrphan(orderID, leaseID, accountID, detail string) error
	// CompleteFulfilmentCleanup releases a successfully withdrawn reservation
	// and dead-letters the job.
	CompleteFulfilmentCleanup(orderID, attemptLeaseID string, reason model.FulfilmentReason, detail string) (model.Order, error)
	// AbortFulfilment releases an unpublished reservation and schedules or
	// dead-letters the job.
	AbortFulfilment(orderID, attemptLeaseID string, reason model.FulfilmentReason, detail string, retryable bool) (model.Order, error)
	// RecordFulfilmentRefusal accounts for a Begin refusal that reserved
	// nothing, so a persistent blocker still follows the backoff schedule.
	RecordFulfilmentRefusal(orderID string, reason model.FulfilmentReason, detail string, retryable bool) (model.Order, error)
	// RecoverFulfilments reconciles interrupted publication work at boot.
	RecoverFulfilments() error
	// Order reads the current order.
	Order(id string) (model.Order, bool)
	// Lease reads one lease row for the post-commit event payload.
	Lease(id string) (model.Lease, bool)
}

// Publisher makes one reserved lease visible to the serving control plane and
// withdraws it when a published attempt can never commit. The API server
// implements both through the existing Panel and LeasePublishHook boundary.
// Publication is idempotent: publishing the same reservation twice is safe, and
// a reservation that is already active or already released is inspected
// instead of published again. Withdrawal refuses an active row, so a serving
// lease is never removed by the fulfilment failure path.
type Publisher interface {
	PublishReservedLease(ctx context.Context, lease model.Lease) error
	WithdrawReservedLease(ctx context.Context, lease model.Lease) error
}

// Events receives the customer-visible transitions a committed attempt emits.
// It is implemented by the API server so the worker reuses the same event bus
// and payload helpers as the request path. Nil emits nothing.
type Events interface {
	OrderActivated(order model.Order)
	LeaseCreated(lease model.Lease)
	LeaseReissued(lease model.Lease, previousLeaseID string)
}

// Config is the service's wiring. Store and Publisher are required; the rest
// have production defaults.
type Config struct {
	Store     Store
	Publisher Publisher
	// Events receives the post-commit transitions. Nil drops them.
	Events Events
	// Now is the clock used to decide which jobs are due. Nil uses time.Now.
	Now func() time.Time
	// Logger receives one warn line per failed attempt. Nil uses
	// slog.Default.
	Logger *slog.Logger
	// ScanInterval is the fallback wake interval. Zero uses
	// DefaultScanInterval; tests shorten it so the loop can be observed
	// without waiting for a real retry schedule.
	ScanInterval time.Duration
}

// Service attempts publication work. It is safe for concurrent use: the store's
// begin/commit/abort gate is the only serialization point, so an immediate
// notify attempt and the background loop can race without double-granting.
type Service struct {
	store     Store
	publisher Publisher
	events    Events
	now       func() time.Time
	logger    *slog.Logger
	scan      time.Duration

	// stats is the in-process counter snapshot behind GET /metrics. It is
	// deliberately small: attempt outcomes by result/reason and the cumulative
	// dead-letter count.
	statsMu    sync.Mutex
	stats      map[string]map[string]int
	deadLetter int
}

// New builds one service. It never starts work by itself; call Run for the
// durable queue and FulfilNow for an immediate attempt.
func New(cfg Config) *Service {
	svc := &Service{
		store:     cfg.Store,
		publisher: cfg.Publisher,
		events:    cfg.Events,
		now:       cfg.Now,
		logger:    cfg.Logger,
		scan:      cfg.ScanInterval,
		stats:     make(map[string]map[string]int),
	}
	if svc.now == nil {
		svc.now = time.Now
	}
	if svc.logger == nil {
		svc.logger = slog.Default()
	}
	if svc.scan <= 0 {
		svc.scan = DefaultScanInterval
	}
	return svc
}

// FulfilmentAttempts returns a copy of the attempt counter: result -> reason ->
// count. Results are "committed", "publish_failed", "commit_failed",
// "refused" and "dead_letter"; the empty reason labels a committed attempt.
func (s *Service) FulfilmentAttempts() map[string]map[string]int {
	if s == nil {
		return nil
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	out := make(map[string]map[string]int, len(s.stats))
	for result, reasons := range s.stats {
		copied := make(map[string]int, len(reasons))
		for reason, count := range reasons {
			copied[reason] = count
		}
		out[result] = copied
	}
	return out
}

// FulfilmentDeadLetters returns the cumulative number of jobs this process
// dead-lettered.
func (s *Service) FulfilmentDeadLetters() int {
	if s == nil {
		return 0
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.deadLetter
}

func (s *Service) recordAttempt(result string, reason model.FulfilmentReason) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	reasons := s.stats[result]
	if reasons == nil {
		reasons = make(map[string]int)
		s.stats[result] = reasons
	}
	reasons[string(reason)]++
}

func (s *Service) recordDeadLetter() {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.deadLetter++
}

// Run recovers interrupted attempts once, then processes due jobs until ctx is
// cancelled. Each pass handles every due order exactly once and sequentially,
// so one order never has two attempts in flight; the loop then sleeps until the
// earliest NextAttemptAt, bounded by the scan interval.
func (s *Service) Run(ctx context.Context) {
	if s == nil || s.store == nil || s.publisher == nil {
		return
	}
	if err := s.store.RecoverFulfilments(); err != nil {
		s.logger.Error("fulfilment_recovery_failed", "error", err)
	}
	for {
		// The boot pass runs before the first sleep: a job recovery just
		// requeued is due now and must not wait for the fallback interval.
		s.runDue(ctx)
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(s.nextDelay())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// runDue attempts every job the store reports due. One failure is logged and
// never stops the pass: the store has already rescheduled or dead-lettered that
// job, and the next order still deserves its attempt.
func (s *Service) runDue(ctx context.Context) {
	for _, order := range s.store.DueFulfilments(s.now()) {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.FulfilNow(ctx, order.ID); err != nil {
			s.logger.Warn("fulfilment_attempt_failed", "order_id", order.ID, "error", err)
		}
	}
}

// nextDelay returns the time until the earliest scheduled attempt, capped by
// the scan interval. A job that is already due after a pass keeps the loop at
// the fallback interval instead of spinning: the store reports it due because
// the attempt was refused without a new schedule (for example capacity).
func (s *Service) nextDelay() time.Duration {
	now := s.now()
	horizon := now.Add(s.scan)
	earliest := horizon
	for _, order := range s.store.DueFulfilments(horizon) {
		job := order.Fulfilment
		if job == nil || job.NextAttemptAt == nil {
			continue
		}
		at, ok := parseTimestamp(*job.NextAttemptAt)
		if !ok || at.Before(earliest) {
			earliest = at
		}
	}
	delay := earliest.Sub(now)
	if delay <= 0 {
		return s.scan
	}
	return delay
}

// FulfilNow attempts one order's job immediately. A published attempt is
// committed without republishing: the publication marker is the durable fence
// between the two phases, so a commit retry can never mint a second identity.
// It returns the order's current state after the attempt; an error reports a
// real attempt that failed, with only a bounded reason in its text.
func (s *Service) FulfilNow(ctx context.Context, orderID string) (model.Order, error) {
	if s == nil || s.store == nil || s.publisher == nil {
		return model.Order{}, errors.New("fulfilment: service is not configured")
	}
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return model.Order{}, errors.New("fulfilment: order id is required")
	}
	order, ok := s.store.Order(orderID)
	if !ok {
		return model.Order{}, fmt.Errorf("fulfilment: order %q not found", orderID)
	}
	if job := order.Fulfilment; job != nil && job.CleanupPending {
		// A published attempt whose external withdrawal failed: retry the
		// withdrawal, never the commit.
		return s.retryCleanup(ctx, order, job)
	}
	if job := order.Fulfilment; job != nil && job.State == model.FulfilmentPublishing &&
		job.PublishedLeaseID != "" {
		return s.commitPublished(ctx, order, job.PublishedLeaseID)
	}
	if !runnable(order) {
		// Nothing to attempt: the job already committed, an attempt owns it,
		// an operator owns the dead letter, or the order is no longer paid or
		// active.
		return order, nil
	}

	attempt, err := s.store.BeginFulfilment(orderID)
	if err != nil {
		return s.recordRefusal(orderID, err)
	}

	if err := s.publisher.PublishReservedLease(ctx, attempt.Lease); err != nil {
		return s.failAttempt(attempt, err)
	}
	if _, err := s.store.MarkFulfilmentPublished(attempt.OrderID, attempt.LeaseID); err != nil {
		// The publication reached the control plane but the marker did not
		// commit. Confirm the external side effect before any local release:
		// a successful withdrawal lets the unmarked attempt be retried, while
		// a failed one retains the published identity and blocks retries.
		return s.handleMarkFailure(ctx, attempt, err)
	}
	committed, err := s.store.CommitFulfilment(attempt.OrderID, attempt.LeaseID)
	if err != nil {
		return s.retryPublished(ctx, attempt.OrderID, attempt.Lease, err)
	}
	s.commitSucceeded(committed, attempt.LeaseID, attempt.Mode, attempt.PreviousLeaseID)
	return committed, nil
}

// commitPublished retries the commit of an attempt whose publication is
// already durably recorded. It never calls the publisher.
func (s *Service) commitPublished(ctx context.Context, order model.Order, leaseID string) (model.Order, error) {
	job := order.Fulfilment
	mode := job.Mode
	previousLeaseID := ""
	if mode == model.FulfilmentReissue && order.Grant != nil {
		previousLeaseID = order.Grant.LeaseID
	}
	committed, err := s.store.CommitFulfilment(order.ID, leaseID)
	if err != nil {
		lease, ok := s.store.Lease(leaseID)
		if !ok {
			lease = model.Lease{ID: leaseID, AccountID: job.PublishedAccountID}
		}
		return s.retryPublished(ctx, order.ID, lease, err)
	}
	s.commitSucceeded(committed, leaseID, mode, previousLeaseID)
	return committed, nil
}

// commitSucceeded emits the post-commit events and records the counter. The
// store's commit is the single winner, so this runs exactly once per grant.
func (s *Service) commitSucceeded(order model.Order, leaseID string, mode model.FulfilmentMode, previousLeaseID string) {
	s.recordAttempt("committed", "")
	if s.events == nil {
		return
	}
	switch mode {
	case model.FulfilmentInitial:
		s.events.OrderActivated(order)
		if lease, ok := s.store.Lease(leaseID); ok {
			s.events.LeaseCreated(lease)
		}
	case model.FulfilmentReissue:
		if lease, ok := s.store.Lease(leaseID); ok {
			s.events.LeaseReissued(lease, previousLeaseID)
		}
	}
}

// recordRefusal accounts for a Begin refusal that reserved nothing. The store
// schedules the next attempt from the typed reason, so a persistent capacity or
// suspension blocker still reaches dead letter.
func (s *Service) recordRefusal(orderID string, cause error) (model.Order, error) {
	current, ok := s.store.Order(orderID)
	if !ok {
		return model.Order{}, newAttemptError("begin", "", model.FulfilmentReasonInternal, cause)
	}
	if !runnable(current) {
		return current, nil
	}
	reason := model.FulfilmentReasonInternal
	var refusal *store.FulfilmentRefusal
	if errors.As(cause, &refusal) {
		reason = refusal.Reason
	}
	retryable := reason != model.FulfilmentReasonEntitlementExpired
	updated, err := s.store.RecordFulfilmentRefusal(orderID, reason, cause.Error(), retryable)
	s.recordAttempt("refused", reason)
	if err != nil {
		s.logger.Warn("fulfilment_refusal_record_failed", "order_id", orderID, "error", err)
	} else {
		if isDeadLetter(updated) {
			s.recordDeadLetter()
		}
		current = updated
	}
	return current, newAttemptError("begin", "", reason, cause)
}

// failAttempt releases an unpublished reservation and schedules or
// dead-letters the job. The store scrubs and bounds the detail, so a signed URL
// or reference in an upstream error never reaches the durable job.
func (s *Service) failAttempt(attempt store.FulfilmentAttempt, cause error) (model.Order, error) {
	reason, retryable := classifyPublishFailure(cause)
	aborted, err := s.store.AbortFulfilment(attempt.OrderID, attempt.LeaseID, reason,
		cause.Error(), retryable)
	s.recordAttempt("publish_failed", reason)
	if err != nil {
		s.logger.Warn("fulfilment_abort_failed", "order_id", attempt.OrderID, "error", err)
		current, _ := s.store.Order(attempt.OrderID)
		return current, newAttemptError("publish", attempt.LeaseID, reason, cause)
	}
	if isDeadLetter(aborted) {
		s.recordDeadLetter()
	}
	return aborted, newAttemptError("publish", attempt.LeaseID, reason, cause)
}

// retryPublished schedules the next commit for a reservation the publisher
// already accepted. When the failure is terminal, the paid term ended, or the
// attempts are exhausted, it withdraws the published reservation first: only a
// successful withdrawal releases the local identity and dead-letters the job.
// A failed withdrawal parks the job as cleanup-pending with the published
// identity intact and a retry schedule.
func (s *Service) retryPublished(ctx context.Context, orderID string, lease model.Lease, cause error) (model.Order, error) {
	current, ok := s.store.Order(orderID)
	reason, retryable := model.FulfilmentReasonTargetChanged, true
	attempts := 0
	if ok {
		if current.ReviewRequired ||
			(current.State != model.OrderPaid && current.State != model.OrderActive) {
			retryable = false
		}
		if job := current.Fulfilment; job != nil {
			attempts = job.Attempts
			if expires, parsed := parseTimestamp(job.EntitlementExpiresAt); parsed &&
				!expires.After(s.now()) {
				reason, retryable = model.FulfilmentReasonEntitlementExpired, false
			}
		}
	}
	if !retryable || attempts >= store.MaxFulfilmentAttempts {
		if err := s.withdraw(ctx, lease); err != nil {
			return s.parkCleanup(orderID, lease, reason, err)
		}
		completed, err := s.store.CompleteFulfilmentCleanup(orderID, lease.ID, reason, cause.Error())
		s.recordAttempt("cleanup_completed", reason)
		if err != nil {
			s.logger.Warn("fulfilment_cleanup_complete_failed", "order_id", orderID, "error", err)
			return current, newAttemptError("commit", lease.ID, reason, cause)
		}
		s.recordDeadLetter()
		return completed, newAttemptError("commit", lease.ID, reason, cause)
	}
	updated, err := s.store.RetryPublishedFulfilment(orderID, lease.ID, reason,
		cause.Error(), retryable)
	s.recordAttempt("commit_failed", reason)
	if err != nil {
		s.logger.Warn("fulfilment_retry_failed", "order_id", orderID, "error", err)
		if !ok {
			current, _ = s.store.Order(orderID)
		}
		return current, newAttemptError("commit", lease.ID, reason, cause)
	}
	return updated, newAttemptError("commit", lease.ID, reason, cause)
}

// retryCleanup retries the external withdrawal of a cleanup-pending attempt.
// Success releases the reservation and dead-letters the job; another failure
// keeps the identity and reschedules.
func (s *Service) retryCleanup(ctx context.Context, order model.Order, job *model.FulfilmentJob) (model.Order, error) {
	leaseID := job.PublishedLeaseID
	lease, ok := s.store.Lease(leaseID)
	if !ok {
		lease = model.Lease{ID: leaseID, AccountID: job.PublishedAccountID}
	}
	if err := s.withdraw(ctx, lease); err != nil {
		return s.parkCleanup(order.ID, lease, job.LastReason, err)
	}
	completed, err := s.store.CompleteFulfilmentCleanup(order.ID, leaseID, job.LastReason,
		"external withdrawal completed")
	s.recordAttempt("cleanup_completed", job.LastReason)
	if err != nil {
		s.logger.Warn("fulfilment_cleanup_complete_failed", "order_id", order.ID, "error", err)
		return order, newAttemptError("withdraw", leaseID, job.LastReason, err)
	}
	s.recordDeadLetter()
	return completed, nil
}

// parkCleanup retains a published attempt whose withdrawal failed. The identity
// and reservation stay durable, the order is flagged for review, and the
// bounded retry schedule makes the worker try again; the alert fires on the
// first transition only.
func (s *Service) parkCleanup(orderID string, lease model.Lease, reason model.FulfilmentReason, cause error) (model.Order, error) {
	updated, err := s.store.MarkFulfilmentCleanupPending(orderID, lease.ID, reason, cause.Error())
	s.recordAttempt("cleanup_pending", reason)
	if err != nil {
		s.logger.Warn("fulfilment_cleanup_park_failed", "order_id", orderID, "error", err)
		current, _ := s.store.Order(orderID)
		return current, newAttemptError("withdraw", lease.ID, reason, cause)
	}
	return updated, newAttemptError("withdraw", lease.ID, reason, cause)
}

// failMark aborts an attempt whose publication was withdrawn after the marker
// write failed. The external side effect is gone, so the attempt can be
// released and retried from scratch.
func (s *Service) failMark(attempt store.FulfilmentAttempt, cause error) (model.Order, error) {
	reason := model.FulfilmentReasonInternal
	aborted, err := s.store.AbortFulfilment(attempt.OrderID, attempt.LeaseID, reason,
		cause.Error(), true)
	s.recordAttempt("mark_failed", reason)
	if err != nil {
		s.logger.Warn("fulfilment_mark_abort_failed", "order_id", attempt.OrderID, "error", err)
		current, _ := s.store.Order(attempt.OrderID)
		return current, newAttemptError("mark published", attempt.LeaseID, reason, cause)
	}
	if isDeadLetter(aborted) {
		s.recordDeadLetter()
	}
	return aborted, newAttemptError("mark published", attempt.LeaseID, reason, cause)
}

// handleMarkFailure confirms a publication whose marker could not be written.
// A successful withdrawal makes the unmarked attempt safe to abort and retry.
// A failed withdrawal must never release the local identity: the published
// lease and account are retained as cleanup-pending (or, when a terminal
// transition already dropped the job, a critical orphan alert is written), and
// BeginFulfilment refuses the job, so no retry can mint a second identity.
func (s *Service) handleMarkFailure(ctx context.Context, attempt store.FulfilmentAttempt, cause error) (model.Order, error) {
	reason := model.FulfilmentReasonInternal
	if werr := s.withdraw(ctx, attempt.Lease); werr != nil {
		updated, retainErr := s.store.RetainPublishedFulfilment(attempt.OrderID, attempt.LeaseID,
			attempt.Lease.AccountID, reason, werr.Error())
		s.recordAttempt("cleanup_pending", reason)
		if retainErr == nil {
			return updated, newAttemptError("mark published", attempt.LeaseID, reason, werr)
		}
		if auditErr := s.store.RecordFulfilmentCleanupOrphan(attempt.OrderID, attempt.LeaseID,
			attempt.Lease.AccountID, werr.Error()); auditErr != nil {
			s.logger.Warn("fulfilment_cleanup_orphan_audit_failed",
				"order_id", attempt.OrderID, "error", auditErr)
		}
		current, _ := s.store.Order(attempt.OrderID)
		return current, newAttemptError("mark published", attempt.LeaseID, reason, werr)
	}
	return s.failMark(attempt, cause)
}

// withdraw asks the publisher to withdraw a reservation that will never
// commit. It returns the publisher's error so the caller can retain the
// published identity and retry; it never logs the raw text (a hook error can
// embed a URL or token).
func (s *Service) withdraw(ctx context.Context, lease model.Lease) error {
	return s.publisher.WithdrawReservedLease(ctx, lease)
}

// attemptError is the only error shape the service returns or logs for a failed
// attempt. Its text carries the operation, the lease id and the closed reason
// code — never the publisher's message, which can embed a URL, token or
// provider reference. Unwrap keeps the original cause available to errors.Is
// without exposing its text.
type attemptError struct {
	op      string
	leaseID string
	reason  model.FulfilmentReason
	cause   error
}

func newAttemptError(op, leaseID string, reason model.FulfilmentReason, cause error) error {
	return &attemptError{op: op, leaseID: leaseID, reason: reason, cause: cause}
}

func (e *attemptError) Error() string {
	if e.leaseID == "" {
		return fmt.Sprintf("fulfilment: %s failed: %s", e.op, e.reason)
	}
	return fmt.Sprintf("fulfilment: %s lease %q failed: %s", e.op, e.leaseID, e.reason)
}

func (e *attemptError) Unwrap() error { return e.cause }

// runnable reports whether the order still owns queued work an automatic
// attempt may run. A publishing job belongs to the attempt in flight (or to the
// published commit path), a dead-lettered job to an operator, and a review or
// terminal order to neither.
func runnable(order model.Order) bool {
	if order.ReviewRequired {
		return false
	}
	if order.State != model.OrderPaid && order.State != model.OrderActive {
		return false
	}
	job := order.Fulfilment
	return job != nil && job.State == model.FulfilmentQueued && !job.Blocked
}

// isDeadLetter reports whether an order's job is now an operator item.
func isDeadLetter(order model.Order) bool {
	return order.Fulfilment != nil && order.Fulfilment.State == model.FulfilmentDeadLetter
}

// classifyPublishFailure maps a publication error to the closed reason
// vocabulary. A deadline or cancellation is a timeout; a panel conflict means
// the reserved target is no longer publishable; everything else is a plain
// publication failure. All of them are retryable: the job keeps its attempts
// and the store's backoff, and the eighth failure dead-letters it.
func classifyPublishFailure(err error) (model.FulfilmentReason, bool) {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return model.FulfilmentReasonTimeout, true
	case errors.Is(err, store.ErrConflict):
		return model.FulfilmentReasonTargetChanged, true
	default:
		return model.FulfilmentReasonPublishFailed, true
	}
}

// parseTimestamp reads one store timestamp (RFC 3339 with nanoseconds).
func parseTimestamp(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
