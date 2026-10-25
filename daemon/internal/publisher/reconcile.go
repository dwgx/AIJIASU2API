package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync"
	"time"
)

// Defaults for the interval reconciler (NEXT-8 #5). The 5-minute cadence keeps
// a converged daemon off the Resin admin API; the 1m→15m backoff recovers a
// failed publish quickly without hammering a Resin that is down.
const (
	DefaultReconcileInterval       = 5 * time.Minute
	DefaultReconcileBackoffInitial = time.Minute
	DefaultReconcileBackoffMax     = 15 * time.Minute
	DefaultReconcileJitter         = 30 * time.Second
)

// Audit actions emitted by the reconciler on a failure transition and on the
// recovery that follows it. The daemon maps them onto store audit rows.
const (
	AuditReconcileFailed    = "resin.reconcile_failed"
	AuditReconcileRecovered = "resin.reconcile_recovered"
)

// DesiredState is the panel state the reconciler watches: the LIVE gates that
// Resin should serve, the active lease identities and the account ids. Only
// stable identities belong here; volatile counters (requests, bytes, latency)
// would make a busy gate look like drift and publish on every tick.
type DesiredState struct {
	Nodes    []Node
	Leases   []LeaseID
	Accounts []string
}

// Hash returns a stable SHA-256 over the state. Lines are sorted, so the
// order of the store snapshots never matters; adding or removing any node,
// lease or account changes the digest.
func (s DesiredState) Hash() string {
	lines := make([]string, 0, len(s.Nodes)+len(s.Leases)+len(s.Accounts))
	for _, node := range s.Nodes {
		lines = append(lines, fmt.Sprintf("node\x00%s\x00%d", node.Tag, node.GatePort))
	}
	for _, lease := range s.Leases {
		lines = append(lines, "lease\x00"+lease.PlatformID+"\x00"+lease.Account)
	}
	for _, account := range s.Accounts {
		lines = append(lines, "account\x00"+account)
	}
	sort.Strings(lines)

	digest := sha256.New()
	for _, line := range lines {
		_, _ = digest.Write([]byte(line))
		_, _ = digest.Write([]byte{'\n'})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// ReconcileConfig configures a Reconciler. Zero values select the defaults;
// Snapshot and Reconcile are required.
type ReconcileConfig struct {
	// Interval is the cadence of a converged loop.
	Interval time.Duration
	// BackoffInitial and BackoffMax bound the retry schedule after a failed
	// reconcile: 1m, 2m, 4m, 8m, then 15m with the defaults.
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	// Jitter bounds the random start delay. It exists so a fleet of daemons
	// restarted together does not reconcile in lockstep.
	Jitter time.Duration

	// Snapshot returns the desired panel state. It must be cheap and must not
	// call Resin.
	Snapshot func() DesiredState
	// Reconcile performs the publish (runtime.ResinPanel.Reconcile). A failure
	// is retried with backoff and never stops the loop.
	Reconcile func(ctx context.Context) error
	// Audit records one transition. It receives a stable action and a
	// credential-free detail; nil disables audit rows.
	Audit  func(action, detail string)
	Logger *slog.Logger

	// wait and rand are injectable for deterministic tests.
	wait waitFunc
	rand func() float64
}

// waitFunc blocks for d, until trigger fires, or until ctx is done. A nil
// error means "reconcile now"; a non-nil error stops the loop.
type waitFunc func(ctx context.Context, d time.Duration, trigger <-chan struct{}) error

// Reconciler keeps the Resin view converged: it hashes the desired state,
// publishes only when the hash changed, and retries a failed publish with
// exponential backoff. It is safe for concurrent use.
type Reconciler struct {
	cfg     ReconcileConfig
	logger  *slog.Logger
	trigger chan struct{}

	mu       sync.Mutex
	lastHash string
	failures int
	inflight *reconcileCall
}

// reconcileCall is one in-flight reconciliation shared by every concurrent
// caller: a caller that arrives while one runs waits for the same result
// instead of starting a second publish.
type reconcileCall struct {
	done chan struct{}
	err  error
}

// NewReconciler validates the config and fills in the defaults.
func NewReconciler(cfg ReconcileConfig) (*Reconciler, error) {
	if cfg.Snapshot == nil {
		return nil, errors.New("publisher: reconciler snapshot is required")
	}
	if cfg.Reconcile == nil {
		return nil, errors.New("publisher: reconciler function is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultReconcileInterval
	}
	if cfg.BackoffInitial <= 0 {
		cfg.BackoffInitial = DefaultReconcileBackoffInitial
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = DefaultReconcileBackoffMax
	}
	if cfg.BackoffMax < cfg.BackoffInitial {
		cfg.BackoffMax = cfg.BackoffInitial
	}
	if cfg.Jitter <= 0 {
		cfg.Jitter = DefaultReconcileJitter
		if cfg.Interval < cfg.Jitter {
			cfg.Jitter = cfg.Interval
		}
	}
	if cfg.Audit == nil {
		cfg.Audit = func(string, string) {}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.wait == nil {
		cfg.wait = waitContext
	}
	if cfg.rand == nil {
		cfg.rand = rand.Float64
	}
	return &Reconciler{
		cfg:     cfg,
		logger:  cfg.Logger,
		trigger: make(chan struct{}, 1),
	}, nil
}

// Trigger asks the loop to reconcile at the next opportunity. It never blocks
// and coalesces: at most one extra run is queued while another is waiting or
// in flight.
func (r *Reconciler) Trigger() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Run reconciles until ctx is done. The first run waits a jittered start
// delay; after that a converged loop waits Interval and a failing one waits
// the backoff schedule. Run never returns an error: a failed reconcile is
// logged, audited on the transition and retried.
func (r *Reconciler) Run(ctx context.Context) {
	if err := r.cfg.wait(ctx, r.startDelay(), r.trigger); err != nil {
		return
	}
	for {
		_ = r.ReconcileNow(ctx)
		if ctx.Err() != nil {
			return
		}
		wait := r.cfg.Interval
		if failures := r.failureCount(); failures > 0 {
			wait = r.backoff(failures)
		}
		if err := r.cfg.wait(ctx, wait, r.trigger); err != nil {
			return
		}
	}
}

// ReconcileNow runs one reconciliation and returns its result. Concurrent
// callers share the in-flight run (single-flight), so two triggers can never
// publish at the same time. It is a no-op when the desired hash equals the
// last successfully published hash.
func (r *Reconciler) ReconcileNow(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	if call := r.inflight; call != nil {
		r.mu.Unlock()
		select {
		case <-call.done:
			return call.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := &reconcileCall{done: make(chan struct{})}
	r.inflight = call
	r.mu.Unlock()

	err := r.reconcileLocked(ctx)

	r.mu.Lock()
	call.err = err
	r.inflight = nil
	close(call.done)
	r.mu.Unlock()
	return err
}

// reconcileLocked hashes the desired state and publishes only when it differs
// from the last successful publish.
func (r *Reconciler) reconcileLocked(ctx context.Context) error {
	hash := r.cfg.Snapshot().Hash()

	r.mu.Lock()
	unchanged := hash == r.lastHash
	failures := r.failures
	r.mu.Unlock()

	if unchanged {
		// A revert to the last published state closes a failure episode
		// without a network call: Resin already serves the desired content.
		if failures > 0 {
			r.mu.Lock()
			r.failures = 0
			r.mu.Unlock()
			r.logger.Info("resin reconcile recovered",
				"reason", "desired state matches the last published hash",
				"after_failures", failures)
			r.cfg.Audit(AuditReconcileRecovered,
				fmt.Sprintf("desired state matches the last published hash after %d failed attempt(s)", failures))
		} else {
			r.logger.Debug("resin reconcile skipped", "reason", "unchanged")
		}
		return nil
	}

	if err := r.cfg.Reconcile(ctx); err != nil {
		if ctx.Err() != nil {
			// Shutdown, not drift: leave the failure counter and the audit
			// stream untouched.
			return err
		}
		r.mu.Lock()
		r.failures++
		failures = r.failures
		r.mu.Unlock()
		next := r.backoff(failures)
		r.logger.Warn("resin reconcile failed",
			"failures", failures, "retry_in", next.String(), "error", err)
		if failures == 1 {
			r.cfg.Audit(AuditReconcileFailed,
				fmt.Sprintf("error=%v retry_in=%s", err, next))
		}
		return err
	}

	r.mu.Lock()
	recovered := r.failures
	r.failures = 0
	r.lastHash = hash
	r.mu.Unlock()

	if recovered > 0 {
		r.logger.Info("resin reconcile recovered", "after_failures", recovered)
		r.cfg.Audit(AuditReconcileRecovered,
			fmt.Sprintf("reconcile succeeded after %d failed attempt(s)", recovered))
	} else {
		r.logger.Info("resin reconciled", "hash", shortHash(hash))
	}
	return nil
}

// startDelay returns a random delay in [0, Jitter).
func (r *Reconciler) startDelay() time.Duration {
	if r.cfg.Jitter <= 0 {
		return 0
	}
	f := r.cfg.rand()
	switch {
	case f <= 0:
		return 0
	case f >= 1:
		return r.cfg.Jitter
	}
	return time.Duration(f * float64(r.cfg.Jitter))
}

// backoff returns the wait after the given number of consecutive failures:
// initial, 2×initial, 4×initial, … capped at BackoffMax.
func (r *Reconciler) backoff(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	delay := r.cfg.BackoffInitial
	for attempt := 1; attempt < failures && delay < r.cfg.BackoffMax; attempt++ {
		if delay >= r.cfg.BackoffMax/2 {
			return r.cfg.BackoffMax
		}
		delay *= 2
	}
	if delay > r.cfg.BackoffMax {
		return r.cfg.BackoffMax
	}
	return delay
}

// failureCount reports the current consecutive failure count.
func (r *Reconciler) failureCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failures
}

// shortHash keeps a log line readable; the full digest is only needed to
// compare state.
func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// waitContext waits for d, a trigger, or ctx. A non-positive duration returns
// immediately: the loop is already due.
func waitContext(ctx context.Context, d time.Duration, trigger <-chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-trigger:
		return nil
	case <-timer.C:
		return nil
	}
}
