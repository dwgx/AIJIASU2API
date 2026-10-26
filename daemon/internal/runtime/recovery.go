// Startup recovery: the daemon persists its node rows, but a gate is a process
// resource. A restart therefore reloads nodes that still read LIVE while no
// listener is bound. This file makes that state honest (demote to STALE) and
// optionally reopens the sessions with one guarded login per account.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

// DefaultAutoLoginBackoff is the pause before the next account after a failed
// startup auto-login. It is jittered by ±20%, so a fleet that restarts at the
// same moment does not hit the vendor in lockstep.
const DefaultAutoLoginBackoff = 60 * time.Second

// GateSource is the running-gate view startup recovery compares the persisted
// nodes against. *NodeController implements it. A nil source means this
// process owns no gate yet (a fresh start), so every persisted LIVE/DRAINING
// row is demoted.
type GateSource interface {
	Snapshot() []NodeRuntime
}

// RecoveryResult summarises one startup recovery pass. Demoted and Kept keep
// the store's node order and hold node ids; Revoked holds the lease rows the
// same demotions ended.
type RecoveryResult struct {
	// Demoted holds the nodes that were moved to STALE.
	Demoted []string
	// Kept holds the LIVE/DRAINING nodes a running gate still serves.
	Kept []string
	// Leases is the number of lease rows revoked by the demotions.
	Leases int
	// Revoked holds the actual lease rows revoked in the same locked
	// DemoteNode step. It is the authoritative count; callers must not
	// pre-count leases from a separate read.
	Revoked []model.Lease
}

// RecoverOnStartup makes the persisted node set honest after a restart: every
// LIVE or DRAINING node that no in-process gate serves is demoted to STALE
// (gate port and egress cleared, leases revoked with reason daemon_restart,
// one node.recovered_stale audit row). A node a gate still serves keeps its
// state, so the call is also safe outside the first boot. Repeating it is a
// no-op: only LIVE/DRAINING rows are considered, and a demoted row is STALE.
//
// It first runs the dedicated fulfilment recovery: every pending_publication
// reservation is discarded with fulfilment_interrupted, its node slot released
// and its job requeued. That order matters — the generic demotion below must
// never end a provisional reservation as a customer-serving daemon_restart
// lease or emit a customer revoke event for it.
//
// The caller runs this before the first panel reconcile: the panel renders
// LIVE rows, so demoting afterwards would publish a gate that does not exist.
func RecoverOnStartup(st *store.Store, gates GateSource, logger *slog.Logger) (RecoveryResult, error) {
	if st == nil {
		return RecoveryResult{}, errors.New("runtime: startup recovery requires a store")
	}
	if logger == nil {
		logger = slog.Default()
	}
	var errs []error
	// A pending_publication row is provisional publication work, not a
	// customer-serving lease. Discard it and requeue its job before the
	// generic lease recovery below (the Leases sweep and DemoteNode) can
	// treat it as a daemon_restart lease or emit a customer revoke event.
	if err := st.RecoverFulfilments(); err != nil {
		errs = append(errs, fmt.Errorf("runtime: recover fulfilment reservations: %w", err))
	}
	// Keep the old recovery side effect: a read sweeps expired leases (and
	// fires the expiry hook) before any node is demoted. The rows are not
	// used for counting; DemoteNode reports what it actually revokes.
	_ = st.Leases()
	serving := make(map[string]struct{})
	if gates != nil {
		for _, node := range gates.Snapshot() {
			if node.Port > 0 {
				serving[node.NodeID] = struct{}{}
			}
		}
	}

	var result RecoveryResult
	for _, node := range st.Nodes() {
		if node.State != model.NodeLive && node.State != model.NodeDraining {
			continue
		}
		if _, ok := serving[node.ID]; ok {
			result.Kept = append(result.Kept, node.ID)
			continue
		}
		demoted, revoked, changed, err := st.DemoteNode(node.ID)
		if err != nil {
			// A node removed between the snapshot and the write is not a
			// recovery failure; every other error is.
			if !errors.Is(err, store.ErrNotFound) {
				errs = append(errs, fmt.Errorf("runtime: demote node %s: %w", node.ID, err))
			}
			continue
		}
		if !changed {
			continue
		}
		result.Demoted = append(result.Demoted, demoted.ID)
		result.Leases += len(revoked)
		result.Revoked = append(result.Revoked, revoked...)
		logger.Info("node_recovered_stale", "node_id", demoted.ID, "account_id", demoted.AccountID,
			"label", demoted.Label, "previous_state", string(node.State), "leases_closed", len(revoked))
	}
	if len(result.Demoted) > 0 {
		logger.Info("startup_recovery",
			"demoted", len(result.Demoted), "kept", len(result.Kept), "leases_closed", result.Leases)
	}
	return result, errors.Join(errs...)
}

// FulfilmentRecovery summarises one boot scan of paid orders that never
// received an entitlement. Queued holds the order ids that received their
// initial fulfilment job in this pass; DeadLettered holds the ids whose paid
// term had already ended, so the job was recorded as an operator item instead
// of being queued.
type FulfilmentRecovery struct {
	Queued       []string
	DeadLettered []string
}

// RecoverPaidOrders attaches the durable initial fulfilment job to every paid
// order that has no entitlement and no job. It is the boot half of the notify
// contract: money recorded while publication was unavailable must not be
// stranded just because the process restarted. The scan is idempotent — the
// store keeps exactly one job per order, so a second pass attaches nothing —
// and it never retries dead-lettered or review-required work, because those
// are operator decisions.
func RecoverPaidOrders(st *store.Store, logger *slog.Logger) (FulfilmentRecovery, error) {
	if st == nil {
		return FulfilmentRecovery{}, errors.New("runtime: paid order recovery requires a store")
	}
	if logger == nil {
		logger = slog.Default()
	}
	var result FulfilmentRecovery
	var errs []error
	for _, order := range st.Orders() {
		if order.State != model.OrderPaid || order.Grant != nil ||
			order.ReviewRequired || order.Fulfilment != nil {
			continue
		}
		after, err := st.EnqueueFulfilment(order.ID, model.FulfilmentInitial)
		if err != nil {
			if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
				// The row changed between the scan and the write (an operator
				// quarantined it, or it was evicted); that command owns the
				// outcome.
				continue
			}
			errs = append(errs, fmt.Errorf("runtime: enqueue fulfilment for %s: %w", order.ID, err))
			continue
		}
		if after.Fulfilment == nil {
			continue
		}
		if after.Fulfilment.State == model.FulfilmentDeadLetter {
			result.DeadLettered = append(result.DeadLettered, order.ID)
			continue
		}
		result.Queued = append(result.Queued, order.ID)
	}
	if len(result.Queued) > 0 || len(result.DeadLettered) > 0 {
		logger.Info("paid_order_recovery",
			"queued", len(result.Queued), "dead_lettered", len(result.DeadLettered))
	}
	return result, errors.Join(errs...)
}

// LoginCall is the vendor login the startup auto-login performs.
// *account.LoginService implements it.
type LoginCall interface {
	Login(ctx context.Context, accountID string) (model.Account, error)
}

// AutoLoginEvents is the event sink the startup auto-login publishes to. It is
// the same sink the HTTP login handler uses, so the account.login event cannot
// drift between the two paths. *ringSink in cmd/ajs-accountd implements it.
type AutoLoginEvents interface {
	Emit(event webhook.EventType, data map[string]any)
}

// AutoLoginConfig configures the optional startup auto-login.
type AutoLoginConfig struct {
	// Store and Logins are required.
	Store  *store.Store
	Logins LoginCall
	// Sync runs after each successful login; *NodeController.SyncAccount is
	// the production value. A nil Sync leaves the node rows untouched (the
	// login is still recorded and the event emitted).
	Sync func(ctx context.Context, accountID string) error
	// Events receives one account.login per successful login. Nil skips the
	// emission.
	Events AutoLoginEvents
	Logger *slog.Logger
	// Backoff is the jittered pause before the next account after a failed
	// login. Zero selects DefaultAutoLoginBackoff; a negative value disables
	// the pause (tests only).
	Backoff time.Duration
	// Jitter maps Backoff onto the actual delay. Nil keeps it within ±20% of
	// Backoff. Tests inject a deterministic value.
	Jitter func(time.Duration) time.Duration
	// Sleep waits for the backoff or until ctx ends. Nil uses a timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

// AutoLoginResult summarises one Run. Every slice holds account ids.
type AutoLoginResult struct {
	Attempted []string
	LoggedIn  []string
	Failed    []string
}

// AutoLogin performs at most one login per eligible account per process
// start. Accounts are tried one at a time in store order; a failed login
// pauses Backoff (jittered) before the next account and is never retried in
// this process. Run is safe to call more than once: an account already tried
// is skipped, and concurrent calls serialize on one lock.
type AutoLogin struct {
	store   *store.Store
	logins  LoginCall
	sync    func(context.Context, string) error
	events  AutoLoginEvents
	logger  *slog.Logger
	backoff time.Duration
	jitter  func(time.Duration) time.Duration
	sleep   func(context.Context, time.Duration) error

	mu        sync.Mutex
	attempted map[string]struct{}
}

// NewAutoLogin validates cfg and returns a runner that has contacted no host.
func NewAutoLogin(cfg AutoLoginConfig) (*AutoLogin, error) {
	if cfg.Store == nil {
		return nil, errors.New("runtime: auto-login requires a store")
	}
	if cfg.Logins == nil {
		return nil, errors.New("runtime: auto-login requires a login service")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	backoff := cfg.Backoff
	switch {
	case backoff == 0:
		backoff = DefaultAutoLoginBackoff
	case backoff < 0:
		backoff = 0
	}
	jitter := cfg.Jitter
	if jitter == nil {
		jitter = jitteredBackoff
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	return &AutoLogin{
		store:     cfg.Store,
		logins:    cfg.Logins,
		sync:      cfg.Sync,
		events:    cfg.Events,
		logger:    logger,
		backoff:   backoff,
		jitter:    jitter,
		sleep:     sleep,
		attempted: make(map[string]struct{}),
	}, nil
}

// Run logs in every eligible account once. An eligible account has a sealed
// password and either a persisted node or a selection, and is neither paused
// (the operator logged it out) nor expired (the vendor would reject it).
// Context cancellation stops the pass before the next account.
func (a *AutoLogin) Run(ctx context.Context) AutoLoginResult {
	a.mu.Lock()
	defer a.mu.Unlock()

	candidates := a.candidates()
	var result AutoLoginResult
	if len(candidates) == 0 {
		a.logger.Info("auto_login_nothing_to_do",
			"reason", "no account has a sealed password and a current node or selection")
		return result
	}
	for i, acct := range candidates {
		if ctx.Err() != nil {
			break
		}
		a.attempted[acct.ID] = struct{}{}
		result.Attempted = append(result.Attempted, acct.ID)
		updated, err := a.logins.Login(ctx, acct.ID)
		if err != nil {
			result.Failed = append(result.Failed, acct.ID)
			a.logger.Warn("auto_login_failed", "account_id", acct.ID, "error", err)
			if i == len(candidates)-1 {
				break
			}
			if err := a.sleep(ctx, a.jitter(a.backoff)); err != nil {
				break
			}
			continue
		}
		result.LoggedIn = append(result.LoggedIn, acct.ID)
		a.logger.Info("auto_login_ok", "account_id", acct.ID, "state", string(updated.State))
		if a.sync != nil {
			if err := a.sync(ctx, acct.ID); err != nil {
				a.logger.Warn("auto_login_sync_failed", "account_id", acct.ID, "error", err)
			}
		}
		if a.events != nil {
			a.events.Emit(webhook.EventAccountLogin, map[string]any{
				"accountId": updated.ID,
				"label":     updated.Label,
				"state":     string(updated.State),
			})
		}
	}
	a.logger.Info("auto_login_finished",
		"attempted", len(result.Attempted), "logged_in", len(result.LoggedIn), "failed", len(result.Failed))
	return result
}

// candidates returns the accounts this process may still try, in store order.
// The caller holds a.mu, so the attempted set cannot change under it.
func (a *AutoLogin) candidates() []model.Account {
	accounts := a.store.Accounts()
	current := make(map[string]struct{}, len(accounts))
	for _, node := range a.store.Nodes() {
		current[node.AccountID] = struct{}{}
	}
	out := make([]model.Account, 0, len(accounts))
	for _, account := range accounts {
		if _, done := a.attempted[account.ID]; done {
			continue
		}
		switch account.State {
		case model.AccountPaused:
			a.logger.Debug("auto_login_skipped", "account_id", account.ID, "reason", "paused")
			continue
		case model.AccountExpired:
			a.logger.Debug("auto_login_skipped", "account_id", account.ID, "reason", "expired")
			continue
		}
		if _, ok := current[account.ID]; !ok {
			if _, ok := a.store.Selection(account.ID); !ok {
				continue
			}
		}
		// AccountSecret is the authoritative "a sealed password exists" check:
		// a store without a sealer answers ErrNotFound for every account.
		secret, err := a.store.AccountSecret(account.ID)
		if err != nil {
			a.logger.Debug("auto_login_skipped", "account_id", account.ID, "reason", "no_sealed_password")
			continue
		}
		clear(secret)
		out = append(out, account)
	}
	return out
}

// jitteredBackoff spreads base by ±20%. math/rand/v2's global source is
// goroutine-safe and self-seeded.
func jitteredBackoff(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	span := base * 2 / 5 // 40% window, centred on base
	if span <= 0 {
		return base
	}
	return base*4/5 + rand.N(span)
}

// sleepContext waits for d or until ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
