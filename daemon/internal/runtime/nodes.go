package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// DefaultGatePortBase is the first loopback port the controller allocates. It
// matches the seeded nodes' range (13000+) and docs/PANEL.md §1: every gate
// binds 127.0.0.1 only.
const DefaultGatePortBase = 13000

// DefaultGateHTTPOffset is the distance between a node's SOCKS5 gate port and
// its HTTP CONNECT port. The offset keeps the pair derivable from the one gate
// port the node row stores: 13000/14000, 13001/14001, ...
const DefaultGateHTTPOffset = 1000

const (
	// gateFlowLimit is passed to every gate and mirrored onto the node's
	// capacity, so the dashboard reports the admission limit Resin sees.
	gateFlowLimit = 32

	// maxGatePort is the highest TCP port.
	maxGatePort = 65535
)

// DefaultLivenessInterval is how often the controller checks that every running
// session is still the vendor's live session. A slot whose session is gone must
// leave LIVE within one window, so this is the containment latency bound.
//
// Each scan first reads the passive Alive view, then performs one active
// vendor probe per running slot (account.ActiveLiveness): the vendor keeps the
// SID and half-closes the control connection after authentication, so an idle
// session loss is invisible without a probe. The probe is one short data
// channel on the slot's existing SID per window, not a per-second vendor call;
// a responsive vendor loss is contained within one window, inside the 60 s
// requirement.
const DefaultLivenessInterval = 30 * time.Second

// activeProbeTimeout bounds one session's active liveness probe. The probe
// opens a data channel to a sentinel destination that fails fast on the vendor
// side; a slow or unreachable vendor is inconclusive, never a loss, and the
// scan proceeds after this bound.
const activeProbeTimeout = 5 * time.Second

// kickRetryWindow is the minimum wait before a kicked slot may reopen. The
// vendor needs time to release the stolen session, and a quick reopen would
// kick the other connection back (DESIGN-MULTISLOT §2).
const kickRetryWindow = 5 * time.Minute

// errSlotPortUnavailable marks a slot whose reserved listener pair cannot be
// used. The pair is immutable while the slot row owns it, so the slot fails
// down instead of moving to another port: a lease stores the gate port.
var errSlotPortUnavailable = errors.New("runtime: slot port pair is unavailable")

// NodeController errors are matched with errors.Is by the API hook and tests.
var (
	// ErrNoLoginResult is returned when SyncAccount runs for an account with
	// no retained successful login.
	ErrNoLoginResult = errors.New("runtime: no login result for account")
	// ErrControllerStopped is returned by every mutation after Stop.
	ErrControllerStopped = errors.New("runtime: node controller is stopped")
)

// LoginResults is the retained-login view the controller reads.
// *account.LoginService implements it.
type LoginResults interface {
	Result(accountID string) (account.LoginResult, bool)
}

// LoginStarts is the optional login-attempt view the controller reads to tell
// an operator reopen from a login that was already in flight when the account
// was stopped. *account.LoginService implements it; an opener without it keeps
// the FetchedAt comparison.
type LoginStarts interface {
	LoginStarted(accountID string) (time.Time, bool)
}

// SessionOpener opens one authenticated FV session per node.
// *account.Factory implements it.
type SessionOpener interface {
	Open(ctx context.Context, req account.SessionRequest) (account.FlowSession, error)
}

// SessionLiveness is the optional active-liveness view a session may expose.
// Healthy only reports that a SID was issued, and a closed or kicked vendor
// session keeps its SID; Alive answers whether the session can still carry
// flows. The controller type-asserts, so an opener that only implements the
// passive check is never contained on a guess.
type SessionLiveness interface {
	Alive() bool
}

// SessionProber is the optional active-liveness view a session may expose.
// SessionLost performs one cheap vendor data-channel probe and reports true
// only when the vendor refused it as a lost session; a flow-level refusal or a
// transport error is inconclusive and reports false. The production account
// session implements it. The controller runs the probe outside its lock, once
// per liveness window per running slot.
type SessionProber interface {
	SessionLost(ctx context.Context) bool
}

// sessionAlive reports whether one running session can still serve. A nil
// session is dead; a session without the optional view keeps the passive SID
// check.
func sessionAlive(session account.FlowSession) bool {
	if session == nil {
		return false
	}
	if liveness, ok := session.(SessionLiveness); ok {
		return liveness.Alive()
	}
	return session.Healthy()
}

// Gate is one node's SOCKS5 listener. *gate.Server implements it.
type Gate interface {
	Start(ctx context.Context) error
	Drain(ctx context.Context) error
	Undrain() error
	Close() error
	Addr() net.Addr
}

// GateState is the optional counter view a Gate may expose. It is deliberately
// separate from Gate so existing test fakes (and any future gate that only
// needs the listener contract) keep compiling; Snapshot type-asserts.
type GateState interface {
	State() gate.State
}

// GateFlows is the optional bounded flow-record view a Gate may expose. Like
// GateState it is separate from Gate so listener-only fakes keep compiling.
// Implementations must be safe for concurrent use.
type GateFlows interface {
	Flows(limit int) []gate.FlowRecord
	Dropped() uint64
}

// GateHTTPAddr is the optional HTTP-listener view a Gate may expose. The
// controller checks it after Start so a gate that ignored HTTPListen cannot be
// recorded as owning a port pair it does not serve.
type GateHTTPAddr interface {
	HTTPAddr() net.Addr
}

// NodeRuntime is one running node as seen by the read-only Lab view: the
// node's identity, its session summary and, when the gate exposes them, the
// counter snapshot and bounded flow window. It carries no credentials.
type NodeRuntime struct {
	NodeID    string
	AccountID string
	ServerID  string
	Port      int
	EgressIP  string
	Draining  bool
	// Slot is the one-based account slot that owns the gate. Generation is
	// the controlled session-open counter the gate was started under.
	Slot       int
	Generation uint64

	SessionSID     string
	SessionHealthy bool
	SessionPresent bool
	StartedAt      time.Time

	// Gate is the counter snapshot. GateKnown is false for a gate that does
	// not implement GateState.
	Gate      gate.State
	GateKnown bool

	// FlowRing is the gate's bounded flow window, nil when the gate does not
	// implement GateFlows. The runtime reads it only in Flows, so the other
	// views never copy the ring.
	FlowRing GateFlows
}

// GateFactory builds a gate. The zero value in NodeControllerConfig selects
// gate.NewServer.
type GateFactory func(cfg gate.Config, session gate.Session) (Gate, error)

// Reconciler republishes the panel topology after the node set changes.
// *ResinPanel implements it. A nil reconciler (Resin disabled) skips the
// reconcile step.
type Reconciler interface {
	Reconcile(ctx context.Context) (Topology, error)
}

// LeaseWithdrawer is the optional same-operation cleanup behind a failed
// break-before-make switch. *ResinPanel implements it. A reconciler that does
// not implement it (a test double or a store-only wiring) skips the external
// lease step; the local lease revocation still runs.
type LeaseWithdrawer interface {
	WithdrawLease(ctx context.Context, accountID string) error
}

// NodeControllerConfig configures a NodeController. Store, Logins and Sessions
// are required; every other field has a default.
type NodeControllerConfig struct {
	Store    *store.Store
	Logins   LoginResults
	Sessions SessionOpener
	// Gates defaults to gate.NewServer.
	Gates GateFactory
	// Reconciler is optional: without a Resin panel there is nothing to
	// reconcile, so the step is skipped.
	Reconciler Reconciler
	// Profile is the FV profile of the sessions; the zero value selects
	// account.Linux423Binary, matching the login service default.
	Profile account.VersionProfile
	// MaxServers caps how many slots one account starts. It is a ceiling on
	// the account's slot capacity, not a server fan-out switch: zero means no
	// additional cap, so min(UseCount, AJS_ACCOUNT_SLOTS, 8) decides, and a
	// positive value below that capacity starts fewer sessions. Negative is
	// invalid.
	MaxServers int
	Logger     *slog.Logger
	// PortBase is the first port tried; zero selects DefaultGatePortBase.
	PortBase int
	// GateHTTP starts an HTTP CONNECT listener beside every SOCKS5 gate. The
	// zero value keeps it off, so a test double or a direct gate.NewServer
	// caller opts in explicitly; the daemon enables it only when
	// AJS_GATE_HTTP=1.
	GateHTTP bool
	// GateHTTPOffset is the HTTP listener's port offset from the SOCKS5 gate
	// port. Zero selects DefaultGateHTTPOffset; a negative or out-of-range
	// value is invalid.
	GateHTTPOffset int
	// Now overrides the clock used for the connect cooldown and result
	// durations. Nil uses time.Now; tests pin it.
	Now func() time.Time
	// LivenessInterval is how often the controller verifies that every running
	// session is still the vendor's live session. Zero selects
	// DefaultLivenessInterval; a negative value disables the background scan,
	// which only tests do (CheckLiveness still works).
	LivenessInterval time.Duration
	// ConnectCooldown is the minimum interval between two switch attempts for
	// one account. Zero selects DefaultConnectCooldown; a negative value
	// disables the cooldown (tests only).
	ConnectCooldown time.Duration
	// GateAuth selects the SOCKS5 authentication mode of every gate. The zero
	// value keeps the no-auth loopback compatibility mode; userpass requires
	// GateIdentity (a nil one fails every handshake closed).
	GateAuth gate.AuthMode
	// GateIdentity authenticates RFC 1929 credentials and evaluates the
	// per-user allowlist and quota in userpass mode. Nil fails closed.
	GateIdentity *GateIdentity
	// Debug receives the real-time trace entries behind /v1/debug. Nil is a
	// no-op recorder: the instrumentation stays in the code path, but a test
	// controller does not need a sink.
	Debug *DebugRecorder
}

// nodeGate is one running gate plus the session it serves.
type nodeGate struct {
	gate       Gate
	session    account.FlowSession
	accountID  string
	serverID   string
	slot       model.SlotKey
	generation uint64
	port       int
	httpPort   int
	egressIP   string
	draining   bool
	startedAt  time.Time
}

// NodeController owns one SOCKS5 gate per enabled account slot. After a
// successful login, SyncAccount opens one session and one loopback gate per
// slot within the account's capacity, persists the slot row and node, and
// reconciles Resin. It is the production caller of account.Factory and
// gate.NewServer that B0 was missing.
//
// Every mutation is serialized: a login sync holds the lock across its vendor
// calls, so a concurrent drain or shutdown cannot interleave with a half-built
// node set. The controller never logs a credential; gate and session failures
// name the node and server only.
type NodeController struct {
	store      *store.Store
	logins     LoginResults
	sessions   SessionOpener
	gates      GateFactory
	reconciler Reconciler
	profile    account.VersionProfile
	logger     *slog.Logger
	portBase   int
	maxServers int

	// ctx is the gate lifetime. Stop cancels it and closes every gate.
	ctx    context.Context
	cancel context.CancelFunc

	// now and cooldown drive the per-account connect cooldown.
	now      func() time.Time
	cooldown time.Duration

	// switchMu guards the per-slot switch markers, the account-wide mutation
	// marker and last-attempt times. It is separate from mu so a second switch
	// for the same slot gets 409 switch_in_progress instead of blocking behind
	// the first one.
	switchMu    sync.Mutex
	switching   map[model.SlotKey]chan struct{}
	lastAttempt map[model.SlotKey]time.Time
	// accountBusy is the account-wide mutation claim. The channel closes when
	// the claim is released, so a stop can wait for a running sync instead of
	// failing its caller.
	accountBusy map[string]chan struct{}

	// stopMu guards the account stop fence. It is separate from switchMu and
	// mu because SyncAccount reads it while holding mu and StopAccount writes
	// it before taking mu; a single mutex would deadlock those two orders.
	stopMu sync.Mutex
	// closedAccounts fences one account after its stop hook ran. Every later
	// start refuses until a login result fetched after the fence arrives, so a
	// delete/logout cannot leave an orphan gate behind a committed mutation.
	closedAccounts map[string]time.Time
	// liveness is the background scan interval; zero means disabled.
	liveness time.Duration

	mu      sync.Mutex
	running map[string]*nodeGate
	// slotGates maps one slot to its running node id. At most one gate per
	// slot is the runtime invariant the switch predicate and lease picker
	// rely on.
	slotGates map[model.SlotKey]string
	nextPort  int
	stopped   bool

	// gateAuth and identity configure every gate the controller starts. In
	// loopback mode identity is unused; in userpass mode a nil identity makes
	// every handshake fail closed rather than falling back to no-auth.
	gateAuth gate.AuthMode
	identity *GateIdentity

	// debug is the optional real-time trace sink. A nil recorder is a no-op.
	debug *DebugRecorder

	// httpEnabled and httpOffset configure the HTTP CONNECT listener that runs
	// beside every SOCKS5 gate. The offset is validated once so the port pair
	// arithmetic in the allocation loop cannot overflow the TCP range.
	httpEnabled bool
	httpOffset  int
}

// NewNodeController validates cfg and returns a controller with no gates. It
// performs no I/O and contacts no host.
func NewNodeController(cfg NodeControllerConfig) (*NodeController, error) {
	if cfg.Store == nil {
		return nil, errors.New("runtime: node controller requires a store")
	}
	if cfg.Logins == nil {
		return nil, errors.New("runtime: node controller requires login results")
	}
	if cfg.Sessions == nil {
		return nil, errors.New("runtime: node controller requires a session opener")
	}
	if cfg.MaxServers < 0 {
		return nil, fmt.Errorf("runtime: node controller MaxServers %d is negative", cfg.MaxServers)
	}
	gates := cfg.Gates
	if gates == nil {
		gates = func(cfg gate.Config, session gate.Session) (Gate, error) {
			return gate.NewServer(cfg, session)
		}
	}
	profile := cfg.Profile
	if profile == (account.VersionProfile{}) {
		profile = account.Linux423Binary
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	portBase := cfg.PortBase
	if portBase < 1 || portBase > maxGatePort {
		portBase = DefaultGatePortBase
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	cooldown := cfg.ConnectCooldown
	switch {
	case cooldown == 0:
		cooldown = DefaultConnectCooldown
	case cooldown < 0:
		cooldown = 0
	}
	gateAuth, err := gate.ParseAuthMode(string(cfg.GateAuth))
	if err != nil {
		return nil, err
	}
	httpOffset := cfg.GateHTTPOffset
	switch {
	case httpOffset == 0:
		httpOffset = DefaultGateHTTPOffset
	case httpOffset < 0 || httpOffset >= maxGatePort:
		return nil, fmt.Errorf("runtime: gate HTTP offset %d is outside 1..%d", cfg.GateHTTPOffset, maxGatePort-1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	liveness := cfg.LivenessInterval
	switch {
	case liveness == 0:
		liveness = DefaultLivenessInterval
	case liveness < 0:
		liveness = 0
	}
	controller := &NodeController{
		store:       cfg.Store,
		logins:      cfg.Logins,
		sessions:    cfg.Sessions,
		gates:       gates,
		reconciler:  cfg.Reconciler,
		profile:     profile,
		logger:      logger,
		portBase:    portBase,
		ctx:         ctx,
		cancel:      cancel,
		running:     make(map[string]*nodeGate),
		slotGates:   make(map[model.SlotKey]string),
		nextPort:    portBase,
		maxServers:  cfg.MaxServers,
		now:         now,
		cooldown:    cooldown,
		switching:   make(map[model.SlotKey]chan struct{}),
		lastAttempt: make(map[model.SlotKey]time.Time),
		accountBusy: make(map[string]chan struct{}),

		closedAccounts: make(map[string]time.Time),
		liveness:       liveness,

		gateAuth:    gateAuth,
		identity:    cfg.GateIdentity,
		httpEnabled: cfg.GateHTTP,
		httpOffset:  httpOffset,
		debug:       cfg.Debug,
	}
	if liveness > 0 {
		go controller.watchLiveness()
	}
	return controller, nil
}

// SyncAccount opens or reuses one gate per enabled account slot, writes each
// slot row and node, and reconciles the panel once.
//
// Capacity is min(account.SlotCapacity(UseCount, AJS_ACCOUNT_SLOTS), MaxServers
// when positive). Slots above capacity are disabled and their nodes deleted;
// a slot that still carries a customer lease is left serving and the sync
// reports slot_capacity_blocked instead of revoking access. Slots above the
// capacity are never planned, so a disabled row cannot be restarted by the same
// sync.
//
// It is idempotent: a slot that already owns a gate keeps it (and its port),
// and a slot that no longer resolves a server is marked DOWN while its node row
// is kept. A per-slot failure marks that slot DOWN and does not stop the
// remaining slots; the aggregate error is returned only when no slot started.
// A reconcile failure is always returned because Resin may now disagree with
// the store.
//
// A persisted selection fixes slot 1's target and makes no other slot change
// (DESIGN-CATALOG §3.6). A selection that no longer resolves is cleared with a
// catalog.selection_lost audit and the default assignment is used.
//
// The whole sync holds the account-wide mutation claim. A concurrent sync,
// switch or disconnect waits for it; a logout/delete claim makes the sync wait
// and then re-check the committed state, so it cannot install a gate behind a
// paused or deleted account.
func (c *NodeController) SyncAccount(ctx context.Context, accountID string) error {
	// Join the account-wide mutation exclusion. A sync opens sessions and
	// gates, so it must not interleave with a stop (logout/delete) that is
	// closing them, nor with a disconnect-all: the stop handler holds the claim
	// across its store mutation, so a sync that lands between the fence and the
	// pause waits and then refuses instead of installing a listener behind it.
	// Concurrent syncs and switches are serialized by waiting, not refused.
	release, err := c.beginAccountMutationWait(ctx, accountID)
	if err != nil {
		return err
	}
	defer release()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		return ErrControllerStopped
	}
	result, ok := c.logins.Result(accountID)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoLoginResult, accountID)
	}
	if since, fenced := c.accountStopped(accountID); fenced {
		// A login that began after the stop is the operator reopening the
		// account. A login that was already in flight when the stop ran is the
		// late sync that must not reopen a slot the delete/logout closed, even
		// though its result was retained with a fresh FetchedAt.
		if c.loginBeganAfter(accountID, result, since) {
			c.clearAccountStopped(accountID)
		} else {
			return &SwitchError{
				Reason:  ReasonAccountState,
				Message: "account is stopped; log in again",
				Details: map[string]string{"stoppedSince": since.UTC().Format(time.RFC3339)},
			}
		}
	}
	acct, ok := c.store.Account(accountID)
	if !ok {
		return &store.Error{Kind: store.ErrNotFound, Message: fmt.Sprintf("account %q not found", accountID)}
	}
	if acct.State != model.AccountActive {
		// A login hook that lands while the account is paused (a logout or
		// delete committed first) must not install a listener behind it.
		return &SwitchError{
			Reason:  ReasonAccountState,
			Message: "account is not active; log in again",
			Details: map[string]string{"state": string(acct.State)},
		}
	}

	capacity := c.slotCapacity(acct)
	blocked, reduceErrs := c.reduceSlotsLocked(ctx, accountID, capacity)
	if len(reduceErrs) > 0 {
		// A slot above the new capacity still carries a lease: keep serving it
		// and report. Never revoke a customer lease to shrink.
		c.logger.Warn("slot_capacity_blocked", "account_id", accountID, "capacity", capacity,
			"error", errors.Join(reduceErrs...))
	}
	if capacity > c.store.SlotCount(accountID) {
		if _, err := c.store.EnsureAccountSlots(accountID, capacity); err != nil {
			return fmt.Errorf("runtime: account %s: slots: %w", accountID, err)
		}
	}
	slots := make([]model.AccountSlot, 0, capacity)
	for _, slot := range c.store.Slots(accountID) {
		if slot.Index > capacity {
			continue
		}
		slots = append(slots, slot)
	}

	servers := usableServers(result.Servers)
	plans := c.planSlotsLocked(accountID, slots, servers, result)
	desired := make(map[string]struct{}, len(plans))
	for i, plan := range plans {
		if plan.srv.id != "" {
			desired[nodeID(plan.key, plan.srv.id)] = struct{}{}
			// A target change is not an undesired gate: keep the running node
			// so the switch path below can close it break-before-make and
			// commit the replacement under one guarded transition.
			if current, ok := c.slotGates[plan.key]; ok && current != nodeID(plan.key, plan.srv.id) {
				desired[current] = struct{}{}
			}
			continue
		}
		// No server resolved: keep the slot's existing DOWN row instead of
		// letting removeUndesiredLocked delete it.
		if slots[i].ServerID != "" {
			desired[nodeID(plan.key, slots[i].ServerID)] = struct{}{}
		}
	}
	// A blocked slot still serves its lease, so its node and gate are desired
	// even though the slot is above the capacity.
	for key := range blocked {
		if id, ok := c.slotGates[key]; ok {
			desired[id] = struct{}{}
		}
		if slot, ok := c.store.Slot(key); ok && slot.ServerID != "" {
			desired[nodeID(key, slot.ServerID)] = struct{}{}
		}
	}
	failures := c.removeUndesiredLocked(accountID, desired)

	started := 0
	for _, plan := range plans {
		if plan.srv.id == "" {
			// A slot that still carries an active lease keeps serving: a plan
			// with no target is not a reason to close a customer's endpoint or
			// delete its node. The lease must be revoked explicitly first.
			if c.slotLeasedLocked(plan.key) {
				failures = append(failures, fmt.Errorf("slot %s: kept, an active lease still pins it", plan.key.String()))
				c.logger.Warn("slot_removal_blocked_by_lease", "account_id", accountID, "slot", plan.key.String())
				continue
			}
			// The check above ran before the close, which can block. Capture
			// the gate id first: a lease created in that window belongs to a
			// listener that is already gone, so it is revoked explicitly
			// (audited with slot/generation) instead of left as a dead
			// endpoint.
			closedID, hadGate := c.slotGates[plan.key]
			if errs := c.closeSlotLocked(plan.key); len(errs) > 0 {
				failures = append(failures, fmt.Errorf("slot %s: %w", plan.key.String(), errors.Join(errs...)))
			}
			if hadGate {
				c.revokeLeasesAfterCloseLocked(plan.key, closedID, "no_server")
			}
			c.markSlotDownLocked(plan.key, acct, plan.srv, "no_server")
			continue
		}
		id := nodeID(plan.key, plan.srv.id)
		// One gate per slot, always. A slot already serving another server is
		// a target change: route it through the guarded slot switch, which
		// closes the old gate before opening the new one (the vendor allows
		// one session per identity) and commits the slot row atomically.
		if current, ok := c.slotGates[plan.key]; ok && current != id {
			if running, ok := c.running[current]; ok {
				res, switchErr := c.switchSlotLocked(ctx, acct, result, plan.srv, running, plan.key, "login", c.now())
				if switchErr != nil {
					failures = append(failures, fmt.Errorf("node %s: %w", id, switchErr))
					continue
				}
				if len(res.Warnings) > 0 {
					c.logger.Warn("catalog_login_switch_warnings", "account_id", accountID,
						"slot", plan.key.String(), "warnings", res.Warnings)
				}
				started++
				continue
			}
			delete(c.slotGates, plan.key)
		}
		if running, ok := c.running[id]; ok { // reuse: never a second session
			// The binding is re-read for the commit: a release between the
			// start and this reuse must not re-commit the slot as LIVE.
			recordID, _ := c.store.SlotDeviceRecordID(plan.key)
			if err := c.activateSlotLocked(plan.key, acct, plan.srv, running, recordID); err != nil {
				failures = append(failures, fmt.Errorf("node %s: %w", id, err))
				c.logger.Warn("node_state_write_failed", "node_id", id, "account_id", accountID, "state", string(model.NodeLive), "error", err)
				continue
			}
			started++
			continue
		}
		if err := c.startSlotLocked(ctx, acct, result, plan); err != nil {
			failures = append(failures, fmt.Errorf("node %s: %w", id, err))
			continue
		}
		started++
	}
	c.logger.Info("slot_sync", "account_id", accountID, "capacity", capacity,
		"planned", len(plans), "started", started)

	reconcileErr := c.reconcileLocked(ctx)
	if started > 0 {
		return reconcileErr
	}
	errs := make([]error, 0, len(failures)+2)
	errs = append(errs, fmt.Errorf("runtime: account %s: no slot started for %d planned slots", accountID, len(plans)))
	errs = append(errs, failures...)
	if reconcileErr != nil {
		errs = append(errs, reconcileErr)
	}
	return errors.Join(errs...)
}

// slotCapacity is the number of sessions one account may run: the account's
// slot allowance further capped by MaxServers when the operator set one.
func (c *NodeController) slotCapacity(acct model.Account) int {
	capacity := account.SlotCapacity(acct.UseCount, account.ConfiguredAccountSlots())
	if c.maxServers > 0 && c.maxServers < capacity {
		capacity = c.maxServers
	}
	if capacity < 1 {
		capacity = 1
	}
	return capacity
}

// slotPlan is one slot's target for the current sync. A zero srv.id means no
// server resolved: the slot is marked down and keeps any existing node row.
type slotPlan struct {
	key model.SlotKey
	srv serverRef
}

// planSlotsLocked assigns at most one server per slot. A server is never
// reused across slots: every slot's persisted and currently running server is
// reserved before the first fallback is chosen, including slots above the
// capacity that still serve a lease. Without that pre-reservation slot N's
// fallback can pick slot M's server, which either kicks M's vendor session or
// closes it when M's own plan then finds its server taken.
//
// The persisted selection only fixes slot 1's target. A target another slot
// owns is refused (empty plan) instead of stolen.
func (c *NodeController) planSlotsLocked(accountID string, slots []model.AccountSlot, servers []serverRef, result account.LoginResult) []slotPlan {
	plans := make([]slotPlan, len(slots))
	owners := make(map[string]map[model.SlotKey]struct{})
	own := func(serverID string, key model.SlotKey) {
		if serverID == "" {
			return
		}
		set := owners[serverID]
		if set == nil {
			set = make(map[model.SlotKey]struct{}, 1)
			owners[serverID] = set
		}
		set[key] = struct{}{}
	}
	for _, slot := range c.store.Slots(accountID) {
		own(slot.ServerID, slot.SlotKey)
	}
	for _, running := range c.running {
		if running.accountID != accountID {
			continue
		}
		own(running.serverID, running.slot)
	}
	used := make(map[string]struct{}, len(slots))
	take := func(id string) { used[id] = struct{}{} }
	// freeFor reports whether srv may serve key in this pass: the slot owns
	// it, or no slot owns it and no earlier slot took it.
	freeFor := func(srv serverRef, key model.SlotKey) bool {
		if srv.id == "" {
			return false
		}
		if _, taken := used[srv.id]; taken {
			return false
		}
		if set, owned := owners[srv.id]; owned {
			_, mine := set[key]
			return mine
		}
		return true
	}
	nextUnused := func(key model.SlotKey) serverRef {
		for _, srv := range servers {
			if freeFor(srv, key) {
				return srv
			}
		}
		return serverRef{}
	}
	selection, hasSelection := c.store.Selection(accountID)
	for i, slot := range slots {
		key := slot.SlotKey
		var target serverRef
		if slot.Index == 1 && hasSelection {
			selected, found := findServer(result.Servers, selection.ServerID)
			if !found || !selected.server.OK() {
				reason := "server_missing"
				if found {
					reason = "server_ineligible"
				}
				if _, err := c.store.LoseSelection(accountID, reason); err != nil {
					c.logger.Warn("catalog_selection_lost_audit_failed", "account_id", accountID, "error", err)
				}
				c.logger.Info("catalog_selection_lost", "account_id", accountID,
					"server_id", selection.ServerID, "reason", reason)
				hasSelection = false
			} else if freeFor(selected, key) {
				target = selected
			} else {
				// Another slot owns the selected server: keep the selection
				// (the sibling may move later) and fail this slot down rather
				// than stealing the session.
				c.logger.Warn("catalog_selection_conflict", "account_id", accountID,
					"server_id", selected.id, "slot", key.String())
			}
		}
		if target.id == "" && slot.ServerID != "" {
			if previous, found := findServer(result.Servers, slot.ServerID); found && previous.server.OK() {
				if freeFor(previous, key) {
					target = previous
				}
			}
		}
		if target.id == "" {
			target = nextUnused(key)
		}
		if target.id != "" {
			take(target.id)
		}
		plans[i] = slotPlan{key: key, srv: target}
	}
	return plans
}

// startSlotLocked resolves one slot's device material and port pair, opens its
// session and gate, and commits the LIVE node with its slot row. The pair is
// claimed before the session opens and released only when this attempt claimed
// it, so a pre-existing pair survives a failed attempt.
func (c *NodeController) startSlotLocked(ctx context.Context, acct model.Account, result account.LoginResult, plan slotPlan) error {
	key, srv := plan.key, plan.srv
	slot, ok := c.store.Slot(key)
	if !ok {
		return fmt.Errorf("runtime: slot %s not found", key.String())
	}
	// A kicked slot stays closed for its retry window, and a quarantined one
	// waits for an operator: reopening sooner would steal the session back and
	// start a kick loop. The row keeps its kick marker; no state is written.
	if wait, blocked := slotRetryBlocked(slot, c.now()); blocked {
		message := "slot is waiting out its vendor-kick retry window"
		if slot.State == model.SlotQuarantined {
			message = "slot is quarantined after repeated vendor kicks; connect it manually"
		}
		return &SwitchError{
			Reason:     ReasonConnectCooldown,
			Message:    message,
			Details:    map[string]string{"slot": key.String()},
			RetryAfter: wait,
			Cause:      fmt.Errorf("runtime: slot %s is not restartable yet", key.String()),
		}
	}
	if !c.store.SlotDeviceBound(key) {
		if !c.store.DevicePoolEnabled() {
			// Without a pool only slot 1 has the legacy in-memory identity.
			if key.Index != 1 {
				c.markSlotDownLocked(key, acct, srv, "device_material_missing")
				return fmt.Errorf("runtime: device pool is disabled for slot %s", key.String())
			}
		} else if _, err := c.store.BindForLoginSlot(key, c.profile.Name); err != nil {
			c.markSlotDownLocked(key, acct, srv, "device_bind_failed")
			return err
		}
	}
	device, recordID, err := c.slotDevice(key)
	if err != nil {
		c.markSlotDownLocked(key, acct, srv, "device_material_missing")
		return err
	}
	addrs, claimed, err := c.reserveSlotPairLocked(key, slot.GatePort)
	if err != nil {
		c.markSlotDownLocked(key, acct, srv, "port_unavailable")
		return err
	}
	openStart := c.now()
	session, err := c.sessions.Open(ctx, account.SessionRequest{
		AccountID:       acct.ID,
		Slot:            key,
		Server:          srv.server,
		Device:          device,
		UserName:        account.SlotUserName(baseUserName(result, acct), key.Index),
		ConnectPassword: result.ConnectPassword,
		Profile:         c.profile,
	})
	if err != nil {
		if claimed {
			_ = c.store.SetSlotPorts(key, 0, 0)
		}
		c.markSlotDownLocked(key, acct, srv, "session_open_failed")
		c.debug.Record(newDebugTrace(model.DebugKindSessionOpen, key, slot.Generation+1,
			nodeID(key, srv.id), srv.id, "open", model.DebugStateFailed, "could not open the session", classifyConnectError(err), c.now().Sub(openStart)))
		return err
	}
	if session.SID() == "" { // never LIVE without a session id
		_ = session.Close()
		if claimed {
			_ = c.store.SetSlotPorts(key, 0, 0)
		}
		c.markSlotDownLocked(key, acct, srv, "session_unverified")
		c.debug.Record(newDebugTrace(model.DebugKindSessionOpen, key, slot.Generation+1,
			nodeID(key, srv.id), srv.id, "open", model.DebugStateFailed, "the vendor session has no id", "auth", c.now().Sub(openStart)))
		return &SwitchError{Reason: ReasonSessionUnverified, Message: "session has no SID"}
	}
	c.debug.Record(newDebugTrace(model.DebugKindSessionOpen, key, slot.Generation+1,
		nodeID(key, srv.id), srv.id, "open", model.DebugStateOpen, "", "", c.now().Sub(openStart)))
	g, bound, err := c.startGateLocked(key, srv, session, addrs)
	if err != nil {
		_ = session.Close()
		if claimed {
			_ = c.store.SetSlotPorts(key, 0, 0)
		}
		c.markSlotDownLocked(key, acct, srv, "gate_start_failed")
		return err
	}
	running := &nodeGate{
		gate: g, session: session, accountID: acct.ID, serverID: srv.id,
		slot: key, generation: slot.Generation + 1,
		port: bound.socks, httpPort: bound.http,
		egressIP: sessionEgress(session), startedAt: c.now(),
	}
	id := nodeID(key, srv.id)
	c.running[id] = running
	if err := c.activateSlotLocked(key, acct, srv, running, recordID); err != nil {
		errs := c.closeGateLocked(id, running)
		delete(c.running, id)
		if claimed {
			_ = c.store.SetSlotPorts(key, 0, 0)
		}
		// A conflict here is the device release that landed while the session
		// was opening: the identity is gone, so the start fails closed.
		reason := "store_failed"
		if errors.Is(err, store.ErrConflict) {
			reason = "device_released"
		}
		c.markSlotDownLocked(key, acct, srv, reason)
		return errors.Join(append(errs, err)...)
	}
	c.slotGates[key] = id
	c.logger.Info("node_started", "node_id", id, "account_id", acct.ID, "server_id", srv.id,
		"slot", key.String(), "port", bound.socks, "http_port", bound.http, "egress_ip", running.egressIP)
	return nil
}

// slotDevice returns the identity a slot must present plus the device record
// it came from. With the pool enabled it is the slot's own sealed material;
// without a pool only slot 1 exists and the account-level in-memory identity is
// the same record, so there is no record id. It never generates.
//
// The record id travels to the LIVE commit, which re-checks the binding under
// the store lock: a release between this read and the commit fails the start.
func (c *NodeController) slotDevice(key model.SlotKey) (apifacade.Device, string, error) {
	material, materialErr := c.store.AccountDeviceMaterialSlot(key)
	if materialErr == nil {
		device, err := account.DeviceForIdentity(material.Identity)
		if err != nil {
			return apifacade.Device{}, "", err
		}
		recordID, _ := c.store.SlotDeviceRecordID(key)
		return device, recordID, nil
	}
	if c.store.DevicePoolEnabled() {
		return apifacade.Device{}, "", materialErr
	}
	identity, ok := c.store.AccountDeviceSlot(key)
	if !ok {
		return apifacade.Device{}, "", materialErr
	}
	device, err := account.DeviceForIdentity(identity)
	if err != nil {
		return apifacade.Device{}, "", err
	}
	return device, "", nil
}

// baseUserName is the vendor tunnel name every slot derives from: the login
// result's user name, else the account's own username.
func baseUserName(result account.LoginResult, acct model.Account) string {
	if userName := strings.TrimSpace(result.UserName); userName != "" {
		return userName
	}
	return acct.Username
}

// activateSlotLocked writes a running slot as LIVE and its slot row atomically.
// An operator drain survives a login refresh, and a reused gate keeps its
// generation instead of bumping it. deviceRecordID is the identity the session
// was opened with; the commit re-checks it, so a reused gate whose binding was
// released in the meantime cannot be re-committed as LIVE.
func (c *NodeController) activateSlotLocked(key model.SlotKey, acct model.Account, srv serverRef, running *nodeGate, deviceRecordID string) error {
	id := nodeID(key, srv.id)
	state := model.NodeLive
	if existing, ok := c.store.Node(id); ok && existing.State == model.NodeDraining {
		state = model.NodeDraining
	}
	if running.draining {
		state = model.NodeDraining
	}
	slot, ok := c.store.Slot(key)
	if !ok {
		return fmt.Errorf("runtime: slot %s not found", key.String())
	}
	node, err := c.store.CommitSlotStart(store.SlotStartCommit{
		Slot: model.AccountSlot{
			SlotKey: key, DeviceID: slot.DeviceID, ServerID: srv.id,
			GatePort: running.port, HTTPPort: running.httpPort,
			State: model.SlotLive, Health: model.SlotHealthHealthy,
			Generation: running.generation, // set at start, never bumped on reuse
			// The kick counter is history, not state: an automatic restart must
			// not forget it, or a repeat offender would never quarantine. A
			// manual reconnect (CommitSwitch) clears it instead.
			KickCount: slot.KickCount,
		},
		Node: applyServerToNode(model.ProxyNode{ID: id, Capacity: gateFlowLimit},
			acct, key, srv, state, running.port, running.egressIP),
		DeviceRecordID: deviceRecordID,
	})
	if err != nil {
		return err
	}
	c.slotGates[key] = node.ID
	c.debug.Record(newDebugTrace(model.DebugKindSlotState, key, running.generation, node.ID, srv.id,
		"activate", string(model.SlotLive), "node="+string(state), "", 0))
	return nil
}

// revokeLeasesAfterCloseLocked is the containment step for a lease that
// appeared between a slot's lease check and its gate close. The listener is
// already gone, so the lease is a dead endpoint: it is revoked explicitly (one
// audited lease.revoke row per lease, with slot/generation), the node leaves
// service without a port and one slot.leases_revoked row names the ids. A slot
// with no lease in that window is untouched.
func (c *NodeController) revokeLeasesAfterCloseLocked(key model.SlotKey, nodeID, reason string) {
	if len(c.store.NodeLeases(nodeID)) == 0 {
		return
	}
	revoked := c.store.InvalidateNodeLeases(nodeID, "slot_closed:"+reason)
	if node, ok := c.store.Node(nodeID); ok {
		node.State = model.NodeDown
		node.GatePort = 0
		node.EgressIP = ""
		if _, err := c.store.PutNode(node); err != nil {
			c.logger.Warn("slot_close_revoke_node_failed", "account_id", key.AccountID,
				"node_id", nodeID, "error", err)
		}
	}
	meta := map[string]string{
		"slot":   key.String(),
		"nodeId": nodeID,
		"reason": reason,
		"leases": strconv.Itoa(len(revoked)),
	}
	if slot, ok := c.store.Slot(key); ok {
		meta["generation"] = strconv.FormatUint(slot.Generation, 10)
	}
	if len(revoked) > 0 {
		ids := make([]string, 0, len(revoked))
		for _, lease := range revoked {
			ids = append(ids, lease.ID)
		}
		meta["leaseIds"] = strings.Join(ids, ",")
	}
	c.store.WriteAudit(model.SeverityWarn, "system", "slot.leases_revoked", nodeID,
		"Slot closed with no target · lease that appeared during close revoked", meta)
	c.logger.Warn("slot_leases_revoked_after_close", "account_id", key.AccountID,
		"slot", key.String(), "node_id", nodeID, "leases", len(revoked), "reason", reason)
}

// markSlotDownLocked records a slot that could not be served: the node loses
// its gate port and the slot row keeps its device, ports and generation through
// SetSlotFailure. With no resolved server the slot's existing node row is the
// one that must go DOWN, so a dead gate never keeps showing as LIVE.
func (c *NodeController) markSlotDownLocked(key model.SlotKey, acct model.Account, srv serverRef, reason string) {
	if srv.id == "" {
		if slot, ok := c.store.Slot(key); ok && slot.ServerID != "" {
			id := nodeID(key, slot.ServerID)
			if node, ok := c.store.Node(id); ok {
				node.State = model.NodeDown
				node.GatePort = 0
				// A DOWN node has no listener, so it must not keep
				// advertising an egress address either.
				node.EgressIP = ""
				if _, err := c.store.PutNode(node); err != nil {
					c.logger.Warn("node_state_write_failed", "node_id", id,
						"account_id", acct.ID, "state", string(model.NodeDown), "error", err)
				}
			}
		}
	} else if _, err := c.writeNodeLocked(key, acct, srv, model.NodeDown, 0, ""); err != nil {
		c.logger.Warn("node_state_write_failed", "node_id", nodeID(key, srv.id),
			"account_id", acct.ID, "state", string(model.NodeDown), "error", err)
	}
	if err := c.store.SetSlotFailure(key, srv.id, reason); err != nil {
		c.logger.Warn("slot_state_write_failed", "slot", key.String(), "reason", reason, "error", err)
	}
	generation := uint64(0)
	node := nodeID(key, srv.id)
	if slot, ok := c.store.Slot(key); ok {
		generation = slot.Generation
		if srv.id == "" && slot.ServerID != "" {
			node = nodeID(key, slot.ServerID)
		}
	}
	c.debug.Record(newDebugTrace(model.DebugKindSlotState, key, generation, node, srv.id,
		"down", string(model.SlotDown), reason, "", 0))
	delete(c.slotGates, key)
}

// reduceSlotsLocked disables and closes the slots above capacity. DisableSlot
// refuses while a customer lease pins the slot, so a config change never
// revokes access; the caller reports the refusal and retries on the next sync.
func (c *NodeController) reduceSlotsLocked(ctx context.Context, accountID string, capacity int) (map[model.SlotKey]struct{}, []error) {
	var errs []error
	blocked := make(map[model.SlotKey]struct{})
	slots := c.store.Slots(accountID)
	for i := len(slots) - 1; i >= 0; i-- {
		slot := slots[i]
		if slot.Index <= capacity {
			continue
		}
		if err := c.store.DisableSlot(slot.SlotKey); err != nil {
			// Keep the slot, its gate and its node: the lease pins them.
			blocked[slot.SlotKey] = struct{}{}
			errs = append(errs, fmt.Errorf("slot %s: %w", slot.SlotKey.String(), err))
			continue
		}
		if running, err := c.slotGateLocked(slot.SlotKey); err == nil && running != nil {
			_ = c.store.SetNodeStateQuiet(nodeID(slot.SlotKey, running.serverID), model.NodeDraining)
			drainCtx, cancel := context.WithTimeout(ctx, switchDrainGrace)
			_ = running.gate.Drain(drainCtx)
			cancel()
		}
		errs = append(errs, c.closeSlotLocked(slot.SlotKey)...)
		// Every node row of the slot is out of capacity now. A target change
		// may have left the running gate on another server, so sweep by slot
		// rather than by the persisted server id.
		for _, node := range c.store.Nodes() {
			if node.AccountID != accountID || node.Slot != slot.Index {
				continue
			}
			if err := c.store.DeleteNodeQuiet(node.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				errs = append(errs, err)
			}
		}
	}
	return blocked, errs
}

// DrainNode drains one node: the store moves it to DRAINING, the controller's
// gate (when it owns one) stops admitting new flows, and the panel is
// reconciled so the node leaves the subscription. A node the controller does
// not own (a seeded node) is still drained in the store and reconciled.
func (c *NodeController) DrainNode(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		return ErrControllerStopped
	}
	if _, err := c.store.DrainNode(id); err != nil {
		return err
	}
	running, ok := c.running[id]
	if ok {
		if err := running.gate.Drain(ctx); err != nil {
			return fmt.Errorf("runtime: drain node %s: %w", id, err)
		}
		running.draining = true
	}
	return c.reconcileLocked(ctx)
}

// UndrainNode returns one node to service in the store, reopens its gate and
// reconciles the panel so the node returns to the subscription.
func (c *NodeController) UndrainNode(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		return ErrControllerStopped
	}
	if _, err := c.store.UndrainNode(id); err != nil {
		return err
	}
	running, ok := c.running[id]
	if ok {
		if err := running.gate.Undrain(); err != nil {
			return fmt.Errorf("runtime: undrain node %s: %w", id, err)
		}
		running.draining = false
	}
	return c.reconcileLocked(ctx)
}

// Stop closes every gate and session, marks the controller stopped, and
// reconciles once. It is idempotent; later mutations return
// ErrControllerStopped. Nodes stay in the store as they were, so the caller's
// final snapshot keeps the last runtime view.
func (c *NodeController) Stop(ctx context.Context) error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.stopped = true
	errs := make([]error, 0, len(c.running))
	for id, running := range c.running {
		errs = append(errs, c.closeGateLocked(id, running)...)
		delete(c.running, id)
		delete(c.slotGates, running.slot)
	}
	c.cancel()
	c.mu.Unlock()

	if err := c.reconcileLocked(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// StopAccount closes every gate this controller runs for one account. It is
// the account-delete/logout seam: the store cascade removes the nodes and
// leases, so the gates and their authenticated FV sessions must be gone before
// that cascade runs or a deleted account would keep listening and relaying.
// Gates of other accounts are untouched, and the store nodes are deliberately
// left to the caller (store.DeleteAccount owns them).
//
// It first waits for the account's in-flight slot switches to settle and then
// fences the account: the API calls this hook before it mutates the store, so a
// start that begins after this returns would otherwise commit a gate behind a
// deleted or paused account. Every later start refuses until a login result
// fetched after the fence arrives (SyncAccount). A switch already past the
// marker is not cancelled; it finishes and this call closes what it committed.
//
// It does not reconcile the panel. At this point the account's nodes are still
// LIVE in the store, so a reconcile here would republish the gates that were
// just closed; the API handler reconciles after the store mutation instead.
//
// It is idempotent: with no running gate for the account nothing changed, so
// it returns nil. Close failures are aggregated; a failed close still removes
// the gate from the running set, so a retry cannot double-close it.
func (c *NodeController) StopAccount(ctx context.Context, accountID string) error {
	release, err := c.BeginAccountStop(ctx, accountID)
	if release != nil {
		defer release()
	}
	return err
}

// BeginAccountStop is StopAccount with the account-wide exclusion kept until
// the returned release runs: it waits for the account's in-flight slot
// switches and syncs, claims the account, fences it, and closes its gates.
//
// The API logout/delete handlers hold the claim across their store mutation and
// call release afterwards. Until then every SyncAccount, connect and switch
// for the account is refused, so a login that lands between the fence and the
// pause/delete commit cannot install a gate behind the committed state. A
// caller that does not need the window (a plain hook) uses StopAccount.
//
// release is non-nil whenever the claim was taken, even when closing a gate
// failed: the caller must call it once the mutation (or its abort) is done.
// Close failures are returned as an aggregate after the gates were removed
// from the running set, so a retry cannot double-close them.
func (c *NodeController) BeginAccountStop(ctx context.Context, accountID string) (func(), error) {
	release, err := c.beginAccountStop(ctx, accountID)
	if err != nil {
		return nil, err
	}
	c.fenceAccount(accountID)

	c.mu.Lock()
	var errs []error
	for id, running := range c.running {
		if running.accountID != accountID {
			continue
		}
		errs = append(errs, c.closeGateLocked(id, running)...)
		delete(c.running, id)
		delete(c.slotGates, running.slot)
		c.logger.Info("account_gates_stopped", "account_id", accountID, "node_id", id, "server_id", running.serverID)
	}
	c.mu.Unlock()
	return release, errors.Join(errs...)
}

// fenceAccount marks one account stopped. Every start for it refuses until a
// login result fetched after this instant arrives; the instant is the fence the
// late-start check compares against.
func (c *NodeController) fenceAccount(accountID string) {
	c.stopMu.Lock()
	c.closedAccounts[accountID] = c.now()
	c.stopMu.Unlock()
}

// accountStopped reports whether the account is fenced and when.
func (c *NodeController) accountStopped(accountID string) (time.Time, bool) {
	c.stopMu.Lock()
	defer c.stopMu.Unlock()
	since, fenced := c.closedAccounts[accountID]
	return since, fenced
}

// clearAccountStopped removes the fence: a login that began after the stop is
// the operator reopening the account.
func (c *NodeController) clearAccountStopped(accountID string) {
	c.stopMu.Lock()
	delete(c.closedAccounts, accountID)
	c.stopMu.Unlock()
}

// loginBeganAfter reports whether the retained login attempt for accountID
// began strictly after the stop fence. The attempt start is the right witness:
// a login that was in flight when the account was stopped finishes with a
// fresh FetchedAt, but it is not an operator reopen. An opener that does not
// expose the attempt start (a test fake) falls back to the fetch time; a
// provider that tracks neither keeps the legacy reopen behaviour.
func (c *NodeController) loginBeganAfter(accountID string, result account.LoginResult, since time.Time) bool {
	if starts, ok := c.logins.(LoginStarts); ok {
		started, ok := starts.LoginStarted(accountID)
		return ok && started.After(since)
	}
	return result.FetchedAt.IsZero() || result.FetchedAt.After(since)
}

// CheckLiveness scans every running gate once and contains the ones that can no
// longer serve: a session whose vendor connection is gone is kicked out of
// service, and a gate whose account is no longer in the store (the delete
// cascade committed) is closed. It returns the number of gates it closed.
//
// The scan reads the passive Alive view, then probes every remaining session
// that exposes SessionProber. The probes run outside the controller lock, in
// parallel, each bounded by activeProbeTimeout: a slow vendor cannot stall a
// mutation and one dead slot cannot delay its sibling. A probe verdict is only
// applied to the same running gate it was taken from; a gate closed or
// replaced while the probe ran is left alone.
//
// The background scan calls it once per DefaultLivenessInterval, so a vendor
// kick leaves the slot out of LIVE within one window. It is exported so an
// operator and the tests can force one scan.
func (c *NodeController) CheckLiveness() int {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return 0
	}
	type runningProbe struct {
		id      string
		running *nodeGate
	}
	snapshot := make([]runningProbe, 0, len(c.running))
	for id, running := range c.running {
		snapshot = append(snapshot, runningProbe{id: id, running: running})
	}
	c.mu.Unlock()

	// lost[i] is written by exactly one goroutine and read after the wait.
	lost := make([]bool, len(snapshot))
	probe := func(i int) {
		running := snapshot[i].running
		if !sessionAlive(running.session) {
			lost[i] = true
			return
		}
		prober, ok := running.session.(SessionProber)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(c.ctx, activeProbeTimeout)
		defer cancel()
		lost[i] = prober.SessionLost(ctx)
	}
	if len(snapshot) > 1 {
		var wg sync.WaitGroup
		for i := range snapshot {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				probe(i)
			}(i)
		}
		wg.Wait()
	} else {
		for i := range snapshot {
			probe(i)
		}
	}

	c.mu.Lock()
	closed := 0
	for i, item := range snapshot {
		if !lost[i] {
			continue
		}
		current, ok := c.running[item.id]
		if !ok || current != item.running {
			// The gate was closed or replaced while the probe ran; the verdict
			// belongs to a session that is no longer the running one.
			continue
		}
		c.containKickLocked(item.id, current, "vendor_kick")
		closed++
	}
	for id, running := range c.running {
		if _, ok := c.store.Account(running.accountID); ok {
			continue
		}
		// The account was deleted while this gate was starting. No session may
		// outlive the account row it was opened for.
		c.closeOrphanLocked(id, running)
		closed++
	}
	c.mu.Unlock()

	if closed > 0 {
		if err := c.reconcileLocked(c.ctx); err != nil {
			c.logger.Warn("liveness_reconcile_failed", "error", err)
		}
	}
	return closed
}

// watchLiveness runs CheckLiveness on the configured interval until Stop
// cancels the controller context.
func (c *NodeController) watchLiveness() {
	ticker := time.NewTicker(c.liveness)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.CheckLiveness()
		}
	}
}

// containKickLocked takes one dead session out of service. The gate closes
// before the session so no flow outlives the connection it relays through; the
// node goes DOWN without a port, the slot row records the kick with its retry
// window (and quarantines a repeat offender), and every lease on the node is
// revoked so no customer keeps a dead endpoint.
func (c *NodeController) containKickLocked(id string, running *nodeGate, detail string) {
	if errs := c.closeGateLocked(id, running); len(errs) > 0 {
		c.logger.Warn("slot_kick_close_failed", "node_id", id, "slot", running.slot.String(),
			"error", errors.Join(errs...))
	}
	delete(c.running, id)
	if current, ok := c.slotGates[running.slot]; ok && current == id {
		delete(c.slotGates, running.slot)
	}
	// Only the generation that owns the slot may transition it. A stale gate
	// left over from an earlier generation (a close that raced a newer start)
	// must not mark the newer session DOWN, revoke its leases or overwrite its
	// node; closing its own listener above is all it may do.
	if slot, ok := c.store.Slot(running.slot); ok && slot.Generation > running.generation {
		c.logger.Warn("slot_kick_stale_generation", "node_id", id, "account_id", running.accountID,
			"slot", running.slot.String(), "generation", running.generation,
			"current_generation", slot.Generation)
		return
	}
	if node, ok := c.store.Node(id); ok {
		node.State = model.NodeDown
		node.GatePort = 0
		// A DOWN node has no listener, so it must not keep advertising an
		// egress address either.
		node.EgressIP = ""
		if _, err := c.store.PutNode(node); err != nil {
			c.logger.Warn("node_state_write_failed", "node_id", id, "account_id", running.accountID,
				"state", string(model.NodeDown), "error", err)
		}
	}
	slot, err := c.store.RecordSlotKick(store.SlotKick{
		Key:        running.slot,
		ServerID:   running.serverID,
		Detail:     detail,
		RetryAfter: c.now().Add(kickRetryWindow),
	})
	if err != nil {
		c.logger.Warn("slot_state_write_failed", "slot", running.slot.String(),
			"reason", detail, "error", err)
	}
	revoked := c.store.InvalidateNodeLeases(id, detail)
	c.debug.Record(newDebugTrace(model.DebugKindSessionLost, running.slot, running.generation,
		id, running.serverID, "liveness", model.DebugStateLost, detail, "", 0))
	if err == nil {
		c.debug.Record(newDebugTrace(model.DebugKindSlotState, running.slot, running.generation,
			id, running.serverID, "kick", string(slot.State), detail, "", 0))
	}
	c.logger.Warn("slot_vendor_kick", "node_id", id, "account_id", running.accountID,
		"slot", running.slot.String(), "server_id", running.serverID,
		"generation", running.generation,
		"kick_count", slot.KickCount, "state", string(slot.State),
		"retry_after", slot.RetryAfter, "leases_revoked", len(revoked))
}

// closeOrphanLocked closes a gate whose account row is gone. The node rows went
// with the account, so only the listener and the session remain to be released.
func (c *NodeController) closeOrphanLocked(id string, running *nodeGate) {
	if errs := c.closeGateLocked(id, running); len(errs) > 0 {
		c.logger.Warn("orphan_gate_close_failed", "node_id", id, "account_id", running.accountID,
			"error", errors.Join(errs...))
	}
	delete(c.running, id)
	if current, ok := c.slotGates[running.slot]; ok && current == id {
		delete(c.slotGates, running.slot)
	}
	c.store.WriteAudit(model.SeverityWarn, "system", "slot.orphan_closed", id,
		"Gate closed · account row is gone", map[string]string{
			"nodeId":    id,
			"accountId": running.accountID,
			"slot":      running.slot.String(),
		})
	c.logger.Warn("orphan_gate_closed", "node_id", id, "account_id", running.accountID,
		"slot", running.slot.String(), "server_id", running.serverID)
}

// slotRetryBlocked reports whether a slot must stay closed: a quarantined slot
// waits for an operator, and a kicked slot waits out its retry window. The
// duration is the remaining wait; a quarantined slot reports zero because no
// automatic retry is allowed at all.
func slotRetryBlocked(slot model.AccountSlot, now time.Time) (time.Duration, bool) {
	if slot.State == model.SlotQuarantined {
		return 0, true
	}
	window, blocked := slotKickWindow(slot, now)
	if blocked {
		return window, true
	}
	return 0, false
}

// slotKickWindow reports whether a kicked slot is still inside its retry
// window. It is the manual-reconnect check: an operator may reopen a
// quarantined slot, but never before the window the vendor needs.
func slotKickWindow(slot model.AccountSlot, now time.Time) (time.Duration, bool) {
	if slot.Health != model.SlotHealthVendorKick || slot.RetryAfter == "" {
		return 0, false
	}
	retry, err := time.Parse(time.RFC3339, slot.RetryAfter)
	if err != nil {
		// The snapshot validator rejects a malformed timestamp, so this is a
		// programming error: fail closed rather than reopen a kicked slot.
		return 0, true
	}
	if retry.After(now) {
		return retry.Sub(now), true
	}
	return 0, false
}

// Snapshot returns a copy of the running nodes for the read-only Lab view.
// It takes the controller lock while copying, so a login sync, drain or stop
// cannot interleave; the returned values are safe to use after it returns.
// A gate that does not implement GateState leaves GateKnown false.
func (c *NodeController) Snapshot() []NodeRuntime {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]NodeRuntime, 0, len(c.running))
	for id, running := range c.running {
		node := NodeRuntime{
			NodeID:     id,
			AccountID:  running.accountID,
			ServerID:   running.serverID,
			Port:       running.port,
			EgressIP:   running.egressIP,
			Draining:   running.draining,
			Slot:       running.slot.Index,
			Generation: running.generation,
			StartedAt:  running.startedAt,
		}
		if running.session != nil {
			node.SessionPresent = true
			node.SessionSID = running.session.SID()
			node.SessionHealthy = running.session.Healthy()
		}
		if stateful, ok := running.gate.(GateState); ok {
			node.Gate = stateful.State()
			node.GateKnown = true
		}
		if flows, ok := running.gate.(GateFlows); ok {
			node.FlowRing = flows
		}
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// writeNodeLocked creates or refreshes one store node from the vendor server.
// An existing node keeps its lease/probe fields through store.PutNode.
func (c *NodeController) writeNodeLocked(key model.SlotKey, acct model.Account, srv serverRef, state model.NodeState, port int, egressIP string) (model.ProxyNode, error) {
	id := nodeID(key, srv.id)
	node, ok := c.store.Node(id)
	if !ok {
		node = model.ProxyNode{
			ID:       id,
			Capacity: gateFlowLimit,
			Protocol: model.ProtocolSOCKS5,
		}
	}
	node = applyServerToNode(node, acct, key, srv, state, port, egressIP)
	return c.store.PutNode(node)
}

// applyServerToNode overlays one vendor server onto a node row. It is shared by
// the login sync (PutNode) and the switch commit (CommitSwitch), so both paths
// write the same label, region, slot and gate fields.
func applyServerToNode(node model.ProxyNode, acct model.Account, key model.SlotKey, srv serverRef, state model.NodeState, port int, egressIP string) model.ProxyNode {
	node.AccountID = acct.ID
	node.AccountLabel = acct.Label
	node.Label = nodeTag(srv, key)
	node.Region = nodeRegion(srv.server)
	node.Country = serverCountry(srv.server)
	node.City = serverCity(srv.server)
	node.State = state
	node.Protocol = model.ProtocolSOCKS5
	node.GatePort = port
	node.ServerID = srv.id
	node.Slot = key.Index
	if egressIP != "" {
		node.EgressIP = egressIP
	}
	return node
}

// slotLeasedLocked reports whether any node the slot currently serves (its
// running gate or a persisted node row) carries an active lease. Cleanup and
// target-loss paths read it so a customer entitlement is never closed or
// deleted silently.
func (c *NodeController) slotLeasedLocked(key model.SlotKey) bool {
	if id, ok := c.slotGates[key]; ok {
		if len(c.store.NodeLeases(id)) > 0 {
			return true
		}
	}
	for _, node := range c.store.Nodes() {
		if node.AccountID != key.AccountID || node.Slot != key.Index {
			continue
		}
		if len(c.store.NodeLeases(node.ID)) > 0 {
			return true
		}
	}
	return false
}

// removeUndesiredLocked closes the gates of servers the login no longer lists
// and deletes their nodes, including nodes this controller never owned. A node
// that still carries an active lease is kept: cleanup may not silently revoke a
// customer entitlement, so the lease must be revoked explicitly (a failed
// switch, a kick or an operator disconnect) before the row can go.
func (c *NodeController) removeUndesiredLocked(accountID string, desired map[string]struct{}) []error {
	var failures []error
	for id, running := range c.running {
		if running.accountID != accountID {
			continue
		}
		if _, keep := desired[id]; keep {
			continue
		}
		if leases := c.store.NodeLeases(id); len(leases) > 0 {
			failures = append(failures, fmt.Errorf("node %s: kept, %d active lease(s) still pin it", id, len(leases)))
			c.logger.Warn("node_removal_blocked_by_lease", "node_id", id, "account_id", accountID,
				"server_id", running.serverID, "leases", len(leases))
			continue
		}
		failures = append(failures, c.closeGateLocked(id, running)...)
		delete(c.running, id)
		delete(c.slotGates, running.slot)
		// The lease check above ran before the gate close, which can block.
		// DeleteNodeUnleased repeats the check under the store's write lock, so
		// a lease created in that window keeps its node instead of being
		// silently deleted with it.
		removed, err := c.store.DeleteNodeUnleased(id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			failures = append(failures, fmt.Errorf("node %s: delete: %w", id, err))
			continue
		}
		if !removed {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			// A customer lease appeared while the listener was closing. The
			// gate is already gone, so the row must not keep advertising the
			// dead port; the lease row stays visible for its owner, because
			// cleanup never revokes a customer entitlement on its own.
			if node, ok := c.store.Node(id); ok {
				node.State = model.NodeDown
				node.GatePort = 0
				node.EgressIP = ""
				if _, err := c.store.PutNode(node); err != nil {
					failures = append(failures, fmt.Errorf("node %s: mark down: %w", id, err))
				}
			}
			// Keep the row for the lease's owner and skip the second sweep, so
			// one kept node is reported once.
			desired[id] = struct{}{}
			failures = append(failures, fmt.Errorf("node %s: kept, a lease appeared while its gate was closing", id))
			c.logger.Warn("node_removal_blocked_by_lease", "node_id", id, "account_id", accountID,
				"server_id", running.serverID, "slot", running.slot.String(), "generation", running.generation)
			continue
		}
		c.logger.Info("node_removed", "node_id", id, "account_id", accountID, "server_id", running.serverID)
	}
	for _, node := range c.store.Nodes() {
		if node.AccountID != accountID {
			continue
		}
		if _, keep := desired[node.ID]; keep {
			continue
		}
		if _, owned := c.running[node.ID]; owned {
			continue
		}
		removed, err := c.store.DeleteNodeUnleased(node.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("node %s: delete: %w", node.ID, err))
			continue
		}
		if !removed {
			failures = append(failures, fmt.Errorf("node %s: kept, an active lease still pins it", node.ID))
			c.logger.Warn("node_removal_blocked_by_lease", "node_id", node.ID, "account_id", accountID,
				"server_id", node.ServerID)
			continue
		}
		c.logger.Info("node_removed", "node_id", node.ID, "account_id", accountID, "server_id", node.ServerID)
	}
	return failures
}

// gateAddresses is one started gate's loopback ports. http is zero when the
// HTTP CONNECT listener is disabled; otherwise it is the port the gate config
// requested (the real gate binds exactly that pair).
type gateAddresses struct {
	socks int
	http  int
}

// startGateLocked starts a gate on a pair the slot already reserved. The gate
// lives on the controller's context, never on the login request's. A pair that
// was just closed may lose a short close-to-bind race on Windows, so one
// immediate retry of the same pair is allowed; after that the slot fails down
// (a lease stores GatePort, so there is no fallback port).
func (c *NodeController) startGateLocked(key model.SlotKey, srv serverRef, session account.FlowSession, addrs gateAddresses) (Gate, gateAddresses, error) {
	id := nodeID(key, srv.id)
	// The gate stamps the generation its flows belong to. Both callers open a
	// session for slot.Generation+1 and hold the controller lock, so the row
	// read here is the same generation the commit will write.
	generation := uint64(1)
	if slot, ok := c.store.Slot(key); ok {
		generation = slot.Generation + 1
	}
	cfg := gate.Config{
		NodeID:     id,
		AccountID:  key.AccountID,
		Slot:       key.Index,
		Generation: generation,
		Listen:     net.JoinHostPort("127.0.0.1", strconv.Itoa(addrs.socks)),
		Limit:      gateFlowLimit,
		Logger:     c.logger,
		AuthMode:   c.gateAuth,
	}
	if addrs.http > 0 {
		cfg.HTTPListen = net.JoinHostPort("127.0.0.1", strconv.Itoa(addrs.http))
	}
	if c.gateAuth == gate.AuthUserPass && c.identity != nil {
		cfg.Authenticator = c.identity
		cfg.UserPolicy = c.identity
	}
	var lastErr error
	for range 2 {
		g, err := c.gates(cfg, session)
		if err != nil {
			c.debug.Record(newDebugTrace(model.DebugKindGateOpen, key, generation, id, srv.id,
				"start", model.DebugStateFailed, "gate factory failed", "gate", 0))
			return nil, gateAddresses{}, err
		}
		if err := g.Start(c.ctx); err != nil {
			_ = g.Close()
			lastErr = err
			continue
		}
		// The reservation is a claim, not a bind: check what the listener
		// really got.
		if addr := g.Addr(); addr != nil {
			if tcp, ok := addr.(*net.TCPAddr); ok && tcp.Port > 0 && tcp.Port != addrs.socks {
				_ = g.Close()
				c.debug.Record(newDebugTrace(model.DebugKindGateOpen, key, generation, id, srv.id,
					"start", model.DebugStateFailed, "listener bound a different port", "gate", 0))
				return nil, gateAddresses{}, fmt.Errorf("runtime: gate bound %d, slot reserved %d", tcp.Port, addrs.socks)
			}
		}
		if addrs.http > 0 {
			verifier, ok := g.(GateHTTPAddr)
			if !ok {
				_ = g.Close()
				c.debug.Record(newDebugTrace(model.DebugKindGateOpen, key, generation, id, srv.id,
					"start", model.DebugStateFailed, "gate cannot report its HTTP listener", "gate", 0))
				return nil, gateAddresses{}, errors.New("runtime: gate cannot report its HTTP listener")
			}
			tcp, ok := verifier.HTTPAddr().(*net.TCPAddr)
			if !ok || tcp.Port != addrs.http {
				_ = g.Close()
				c.debug.Record(newDebugTrace(model.DebugKindGateOpen, key, generation, id, srv.id,
					"start", model.DebugStateFailed, "HTTP listener bound a different port", "gate", 0))
				return nil, gateAddresses{}, fmt.Errorf("runtime: gate HTTP listener is not %d", addrs.http)
			}
		}
		c.debug.Record(newDebugTrace(model.DebugKindGateOpen, key, generation, id, srv.id,
			"start", model.DebugStateOpen, fmt.Sprintf("socks=%d http=%d", addrs.socks, addrs.http), "", 0))
		return g, addrs, nil
	}
	c.debug.Record(newDebugTrace(model.DebugKindGateOpen, key, generation, id, srv.id,
		"start", model.DebugStateFailed, "gate did not start", "gate", 0))
	return nil, gateAddresses{}, lastErr
}

// reservedPortsLocked returns the ports claimed by every running gate, every
// store node, and every slot row except the caller's own slot. AllSlots is what
// makes a reservation survive a restart: the node row may be gone (slot down)
// while the slot still owns its pair.
func (c *NodeController) reservedPortsLocked(except model.SlotKey) map[int]struct{} {
	used := make(map[int]struct{})
	for _, running := range c.running {
		if running.slot == except {
			continue
		}
		used[running.port] = struct{}{}
		if running.httpPort > 0 {
			used[running.httpPort] = struct{}{}
		}
	}
	for _, node := range c.store.Nodes() {
		if node.GatePort <= 0 || node.Slot == except.Index && node.AccountID == except.AccountID {
			continue
		}
		used[node.GatePort] = struct{}{}
		if c.httpEnabled {
			if p := node.GatePort + c.httpOffset; p <= maxGatePort {
				used[p] = struct{}{}
			}
		}
	}
	for _, slot := range c.store.AllSlots() {
		if slot.SlotKey == except {
			continue
		}
		if slot.GatePort > 0 {
			used[slot.GatePort] = struct{}{}
		}
		if slot.HTTPPort > 0 {
			used[slot.HTTPPort] = struct{}{}
		}
	}
	return used
}

// reserveSlotPairLocked claims a pair for key BEFORE the session opens.
// preferred > 0 is the slot's existing pair and is immutable: if it is taken,
// the slot fails down instead of moving (a lease stores GatePort). The bool
// reports whether this call claimed a NEW pair; only those are released on a
// failed attempt.
func (c *NodeController) reserveSlotPairLocked(key model.SlotKey, preferred int) (gateAddresses, bool, error) {
	reserved := c.reservedPortsLocked(key)
	pairFree := func(port int) (gateAddresses, bool) {
		if port < 1 || port > maxGatePort {
			return gateAddresses{}, false
		}
		if _, used := reserved[port]; used {
			return gateAddresses{}, false
		}
		addrs := gateAddresses{socks: port}
		if c.httpEnabled {
			addrs.http = port + c.httpOffset
			if addrs.http > maxGatePort {
				return gateAddresses{}, false
			}
			if _, used := reserved[addrs.http]; used {
				return gateAddresses{}, false
			}
		}
		return addrs, true
	}
	if preferred > 0 {
		addrs, ok := pairFree(preferred)
		if !ok {
			return gateAddresses{}, false, fmt.Errorf("%w: slot %s pair %d is not free",
				errSlotPortUnavailable, key.String(), preferred)
		}
		if err := c.store.SetSlotPorts(key, addrs.socks, addrs.http); err != nil {
			return gateAddresses{}, false, err
		}
		return addrs, false, nil
	}
	for port := c.nextPort; port <= maxGatePort; port++ {
		addrs, ok := pairFree(port)
		if !ok {
			continue
		}
		if err := c.store.SetSlotPorts(key, addrs.socks, addrs.http); err != nil {
			return gateAddresses{}, false, err
		}
		c.nextPort = port + 1
		return addrs, true, nil
	}
	return gateAddresses{}, false, errors.New("runtime: no free loopback gate port pair")
}

// closeSlotLocked closes every gate a slot currently serves. The id normally
// comes from slotGates; a missing entry falls back to the running scan so a
// stale map can never leak a listener. The gate closes before its session so no
// flow can outlive the connection it relays through.
func (c *NodeController) closeSlotLocked(key model.SlotKey) []error {
	ids := make([]string, 0, 1)
	if id, ok := c.slotGates[key]; ok {
		ids = append(ids, id)
	} else {
		for id, running := range c.running {
			if running.slot == key {
				ids = append(ids, id)
			}
		}
	}
	delete(c.slotGates, key)
	var errs []error
	for _, id := range ids {
		running, ok := c.running[id]
		if !ok {
			continue
		}
		delete(c.running, id)
		errs = append(errs, c.closeGateLocked(id, running)...)
	}
	return errs
}

// closeGateLocked stops a gate before its session, so no flow can outlive the
// connection it relays through.
func (c *NodeController) closeGateLocked(id string, running *nodeGate) []error {
	var errs []error
	if running.gate != nil {
		if err := running.gate.Close(); err != nil {
			errs = append(errs, fmt.Errorf("node %s: close gate: %w", id, err))
		}
	}
	if running.session != nil {
		if err := running.session.Close(); err != nil {
			errs = append(errs, fmt.Errorf("node %s: close session: %w", id, err))
		}
	}
	state := model.DebugStateClosed
	if len(errs) > 0 {
		state = model.DebugStateFailed
	}
	c.debug.Record(newDebugTrace(model.DebugKindGateClose, running.slot, running.generation,
		id, running.serverID, "close", state, "", "", 0))
	return errs
}

// reconcileLocked republishes the panel after a node-set change. Without a
// reconciler (Resin disabled) the step is a no-op.
func (c *NodeController) reconcileLocked(ctx context.Context) error {
	if c.reconciler == nil {
		return nil
	}
	if _, err := c.reconciler.Reconcile(ctx); err != nil {
		return fmt.Errorf("runtime: reconcile after node sync: %w", err)
	}
	return nil
}

// serverRef is one usable vendor server and the id its node is keyed on.
type serverRef struct {
	id     string
	server apifacade.Server
}

// usableServers returns the servers marked OK, keyed by their stable id and
// sorted so a sync is deterministic. The map key is the fallback when the
// server object carries no id.
func usableServers(servers map[string]apifacade.Server) []serverRef {
	keys := make([]string, 0, len(servers))
	for key := range servers {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]serverRef, 0, len(servers))
	seen := make(map[string]struct{}, len(servers))
	for _, key := range keys {
		server := servers[key]
		if !server.OK() {
			continue
		}
		id := strings.TrimSpace(server.ID)
		if id == "" {
			id = strings.TrimSpace(key)
		}
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, serverRef{id: id, server: server})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// nodeID is the stable node id of one slot: "<account>#<N>:<server>". It is
// the only runtime producer of the format; model.SlotKey.NodeID owns it.
func nodeID(key model.SlotKey, serverID string) string {
	return key.NodeID(serverID)
}

// nodeTag derives the node label, which is also the Resin subscription tag:
// "<country>-<account>-<server>" for slot 1 and
// "<country>-<account>-s<N>-<server>" for the other slots, in the lowercase
// alphanumeric alphabet publisher.ValidTag accepts. The account is part of the
// tag because vendor server ids are shared by every account, so
// "<country>-<server>" would collide across accounts. tagPart keeps only
// [a-z0-9], so no part can contain a hyphen: a three-part slot-1 label can
// never equal a four-part slot-N label.
func nodeTag(srv serverRef, key model.SlotKey) string {
	country := tagPart(serverCountry(srv.server))
	if country == "" {
		country = "xx"
	}
	account := tagPart(key.AccountID)
	if account == "" {
		account = "acct"
	}
	server := tagPart(srv.id)
	if server == "" {
		server = "srv"
	}
	if key.Index <= 1 {
		return country + "-" + account + "-" + server
	}
	return country + "-" + account + "-s" + strconv.Itoa(key.Index) + "-" + server
}

// tagPart keeps only ASCII letters and digits, lowercased.
func tagPart(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for i := range len(value) {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + 'a' - 'A')
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
		}
	}
	return b.String()
}

// serverCountry returns the vendor country claim, falling back to the first
// location entry.
func serverCountry(server apifacade.Server) string {
	if country := strings.TrimSpace(server.Ctry); country != "" {
		return country
	}
	if len(server.Loc) > 0 {
		return strings.TrimSpace(server.Loc[0].C)
	}
	return ""
}

// serverCity returns the first location name.
func serverCity(server apifacade.Server) string {
	if len(server.Loc) > 0 {
		return strings.TrimSpace(server.Loc[0].N)
	}
	return ""
}

// nodeRegion is the lowercase country the dashboard filters on; the vendor
// server carries no subdivision, so the country is the region.
func nodeRegion(server apifacade.Server) string {
	if country := serverCountry(server); country != "" {
		return strings.ToLower(country)
	}
	return "unknown"
}

// sessionEgress returns the vendor-reported egress address of a session:
// sip, then cip, then ip, the field names the FV auth response uses.
func sessionEgress(session account.FlowSession) string {
	if session == nil {
		return ""
	}
	fields := session.Fields()
	for _, key := range []string{"sip", "cip", "ip"} {
		if value := strings.TrimSpace(fields[key]); value != "" {
			return value
		}
	}
	return ""
}

const (
	// defaultUserQuotaCacheTTL bounds how stale a cached monthly user total
	// can be. The flow meter advances the entry after every accepted flow, so
	// the TTL only covers rows written by another path (a restore, say).
	defaultUserQuotaCacheTTL = 30 * time.Second

	// defaultDigestRebuildInterval rate-limits rebuilding the key digest index
	// when an unknown digest arrives, so a flood of wrong passwords cannot
	// turn every handshake into a key-list snapshot. Revocation never needs a
	// rebuild: the live key read sees it on the next handshake.
	defaultDigestRebuildInterval = time.Second
)

// GateIdentity is the store-backed RFC 1929 authenticator and per-user policy
// the node controller attaches to every gate in userpass mode. One instance is
// shared by every gate, so the key digest index, the compiled allowlists and
// the monthly usage cache are built once per process.
//
// Credentials: the username is the exact PanelUser.ID and the password is one
// of that user's API-key secrets. Only SHA-256 digests are indexed; the
// plaintext lives for the duration of one Authenticate call and is never
// stored, logged or attached to a flow record. Every rejection is identical,
// so an unknown user, a wrong secret, a revoked key and a key issued to another
// user share one wire result.
//
// Freshness: the key row, the user row and the digest index are read live, so
// revoking a key, suspending a user or replacing the allowlist affects the
// next new connection while admitted flows finish with their identity. The
// only lag is a key created after the index was built, discovered within
// rebuildInterval.
type GateIdentity struct {
	store *store.Store
	now   func() time.Time

	cacheTTL        time.Duration
	rebuildInterval time.Duration

	// flowCap bounds concurrently admitted flows per user (AJS_GATE_USER_FLOWS,
	// default 8). active counts the flows the gate has admitted but not yet
	// finished. Both are guarded by mu and are process-wide, so the cap holds
	// across every gate the controller starts.
	flowCap int
	active  map[string]int

	mu       sync.Mutex
	digest   map[[sha256.Size]byte]string
	policies map[string]*gateUserPolicy
	usage    map[string]gateUserUsage
	rebuilt  time.Time
}

// gateUserPolicy is the immutable part of one user's policy: the compiled
// allowlist plus a fingerprint of the entries it was built from.
type gateUserPolicy struct {
	fingerprint uint64
	allowlist   gate.Allowlist
}

// gateUserUsage is one cached monthly total for one user.
type gateUserUsage struct {
	month   string
	bytes   int64
	expires time.Time
}

// NewGateIdentity builds the gate identity over st. A nil store fails every
// authentication and every policy check closed. The per-user concurrent-flow
// cap comes from AJS_GATE_USER_FLOWS (default 8).
func NewGateIdentity(st *store.Store) *GateIdentity {
	return &GateIdentity{
		store:           st,
		now:             time.Now,
		cacheTTL:        defaultUserQuotaCacheTTL,
		rebuildInterval: defaultDigestRebuildInterval,
		flowCap:         userFlowCapFromEnv(),
		active:          make(map[string]int),
		digest:          make(map[[sha256.Size]byte]string),
		policies:        make(map[string]*gateUserPolicy),
		usage:           make(map[string]gateUserUsage),
	}
}

// AcquireFlow reserves one concurrent flow slot for userID. It returns false
// when the user already holds flowCap flows, which refuses the new flow with
// user_flow_limit before a gate slot or vendor flow is taken. An empty user id
// (loopback mode) is always admitted.
func (g *GateIdentity) AcquireFlow(userID string) bool {
	if g == nil || userID == "" {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.flowCap > 0 && g.active[userID] >= g.flowCap {
		return false
	}
	g.active[userID]++
	return true
}

// ReleaseFlow returns one concurrent flow slot reserved by AcquireFlow. It is
// idempotent-safe for a missing slot: a double release cannot drive the count
// negative.
func (g *GateIdentity) ReleaseFlow(userID string) {
	if g == nil || userID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active[userID] <= 1 {
		delete(g.active, userID)
		return
	}
	g.active[userID]--
}

// ActiveFlows returns the number of admitted flows for userID. It exists for
// diagnostics and tests.
func (g *GateIdentity) ActiveFlows(userID string) int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active[userID]
}

// Authenticate verifies one RFC 1929 credential pair. It returns ok only for an
// active key whose secret matches pass and whose UserID equals user.
func (g *GateIdentity) Authenticate(user, pass string) (gate.Principal, bool) {
	if g == nil || g.store == nil || user == "" || pass == "" {
		return gate.Principal{}, false
	}
	sum := sha256.Sum256([]byte(pass))
	keyID, ok := g.keyForDigest(sum)
	if !ok {
		return gate.Principal{}, false
	}
	key, ok := g.store.Key(keyID)
	if !ok || key.UserID != user || key.RevokedAt != nil {
		return gate.Principal{}, false
	}
	panelUser, ok := g.store.User(user)
	if !ok {
		return gate.Principal{}, false
	}
	return gate.Principal{UserID: panelUser.ID, UserName: panelUser.Name}, true
}

// keyForDigest resolves a secret digest to its key id. A miss rebuilds the
// index at most once per rebuildInterval, which is how a key created after
// startup becomes usable without a restart.
func (g *GateIdentity) keyForDigest(sum [sha256.Size]byte) (string, bool) {
	g.mu.Lock()
	keyID, ok := g.digest[sum]
	rebuild := !ok && g.now().Sub(g.rebuilt) >= g.rebuildInterval
	g.mu.Unlock()
	if ok {
		return keyID, true
	}
	if !rebuild {
		return "", false
	}
	g.rebuildDigests()
	g.mu.Lock()
	keyID, ok = g.digest[sum]
	g.mu.Unlock()
	return keyID, ok
}

// rebuildDigests replaces the digest index from the store's key rows. Seeded
// keys have no stored hash and are skipped: their secrets never existed in
// this process, so they cannot authenticate.
func (g *GateIdentity) rebuildDigests() {
	keys := g.store.Keys()
	next := make(map[[sha256.Size]byte]string, len(keys))
	for _, key := range keys {
		if key.RevokedAt != nil {
			continue
		}
		hash, ok := g.store.SecretHash(key.ID)
		if !ok {
			continue
		}
		raw, err := hex.DecodeString(hash)
		if err != nil || len(raw) != sha256.Size {
			continue
		}
		var sum [sha256.Size]byte
		copy(sum[:], raw)
		next[sum] = key.ID
	}
	g.mu.Lock()
	g.digest = next
	g.rebuilt = g.now()
	g.mu.Unlock()
}

// Admit evaluates the authenticated user's policy for one canonical host, in
// the order the design fixes: enabled state, allowlist, user quota. The
// account quota is the gate's separate, later check.
func (g *GateIdentity) Admit(principal gate.Principal, host string) (bool, string, int64, int64) {
	if g == nil || g.store == nil || principal.UserID == "" {
		return false, gate.FlowErrorPolicyUnavailable, 0, 0
	}
	panelUser, ok := g.store.User(principal.UserID)
	if !ok {
		return false, gate.FlowErrorPolicyUnavailable, 0, 0
	}
	if !panelUser.Enabled {
		return false, gate.FlowErrorUserDisabled, 0, 0
	}
	if !g.policyFor(panelUser).allowlist.Match(host) {
		return false, gate.FlowErrorAllowlist, 0, 0
	}
	if panelUser.QuotaBytesMonth == nil {
		return true, "", 0, 0
	}
	budget := *panelUser.QuotaBytesMonth
	used := g.monthlyUsage(panelUser.ID)
	if used >= budget {
		return false, gate.FlowErrorUserQuota, used, budget
	}
	return true, "", used, budget
}

// policyFor returns the compiled allowlist for user, recompiling only when the
// stored entries changed. The fingerprint keeps the per-flow cost to one small
// scan, never a parse or an allocation.
func (g *GateIdentity) policyFor(panelUser model.PanelUser) *gateUserPolicy {
	fingerprint := allowlistFingerprint(panelUser.Allowlist)
	g.mu.Lock()
	cached, ok := g.policies[panelUser.ID]
	g.mu.Unlock()
	if ok && cached.fingerprint == fingerprint {
		return cached
	}
	compiled := &gateUserPolicy{
		fingerprint: fingerprint,
		allowlist:   gate.CompileAllowlist(panelUser.Allowlist),
	}
	g.mu.Lock()
	g.policies[panelUser.ID] = compiled
	g.mu.Unlock()
	return compiled
}

// allowlistFingerprint hashes the stored entries so a changed list is
// recompiled and an unchanged one is reused.
func allowlistFingerprint(entries []string) uint64 {
	hash := fnv.New64a()
	for _, entry := range entries {
		_, _ = hash.Write([]byte(entry))
		_, _ = hash.Write([]byte{0})
	}
	return hash.Sum64()
}

// monthlyUsage returns the cached current-month total for one user, computing
// it from the store's durable counter on a miss or after the month changed.
func (g *GateIdentity) monthlyUsage(userID string) int64 {
	now := g.now()
	month := now.UTC().Format("2006-01")

	g.mu.Lock()
	if entry, ok := g.usage[userID]; ok && entry.month == month && now.Before(entry.expires) {
		g.mu.Unlock()
		return entry.bytes
	}
	g.mu.Unlock()

	used := g.store.UserMonthlyUsage(userID)

	g.mu.Lock()
	g.usage[userID] = gateUserUsage{month: month, bytes: used, expires: now.Add(g.cacheTTL)}
	g.mu.Unlock()
	return used
}

// AddUsage advances the cached monthly total for one authenticated user after
// the relay reported a chunk. The store's durable counter is advanced by the
// meter first; this only keeps a populated cache entry in step so the next
// admission does not need a store read. An absent entry stays absent: the next
// admission recomputes from the durable counter.
func (g *GateIdentity) AddUsage(userID string, bytes int64) {
	if g == nil || g.store == nil || userID == "" || bytes <= 0 {
		return
	}
	now := g.now()
	month := now.UTC().Format("2006-01")
	g.mu.Lock()
	if entry, ok := g.usage[userID]; ok && entry.month == month {
		entry.bytes += bytes
		entry.expires = now.Add(g.cacheTTL)
		g.usage[userID] = entry
	}
	g.mu.Unlock()
}

// overQuota reports whether userID has reached the month's byte quota. It is
// the live counterpart of Admit's quota stage: the relay calls it after every
// 256 KiB so a flow is cut off before it can multiply the admission pre-check.
func (g *GateIdentity) overQuota(userID string) bool {
	if g == nil || g.store == nil || userID == "" {
		return false
	}
	user, ok := g.store.User(userID)
	if !ok || user.QuotaBytesMonth == nil {
		return false
	}
	return g.monthlyUsage(userID) >= *user.QuotaBytesMonth
}
