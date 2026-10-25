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

	// maxGatePortAttempts bounds how many bind failures one gate may see
	// before the controller gives up. A Start failure is retried on the next
	// port instead of classifying the OS error: Windows reports
	// WSAEADDRINUSE (10048), which is not syscall.EADDRINUSE, and every other
	// Start failure (a stopped controller, an invalid listen address) fails
	// every port the same way.
	maxGatePortAttempts = 32
)

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

// SessionOpener opens one authenticated FV session per node.
// *account.Factory implements it.
type SessionOpener interface {
	Open(ctx context.Context, req account.SessionRequest) (account.FlowSession, error)
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
	// MaxServers caps how many usable servers one account starts: after the
	// deterministic sort only the first MaxServers server IDs get a session
	// and a gate. Zero starts every usable server (a live login can expose
	// 100+); negative is invalid.
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
}

// nodeGate is one running gate plus the session it serves.
type nodeGate struct {
	gate      Gate
	session   account.FlowSession
	accountID string
	serverID  string
	port      int
	httpPort  int
	egressIP  string
	draining  bool
	startedAt time.Time
}

// NodeController owns one SOCKS5 gate per usable vendor server. After a
// successful login, SyncAccount opens a session and a loopback gate for every
// server the login returned, persists the node, and reconciles Resin. It is
// the production caller of account.Factory and gate.NewServer that B0 was
// missing.
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

	// switchMu guards the per-account switch markers and last-attempt times.
	// It is separate from mu so a second switch for the same account gets
	// 409 switch_in_progress instead of blocking behind the first one.
	switchMu    sync.Mutex
	switching   map[string]struct{}
	lastAttempt map[string]time.Time

	mu       sync.Mutex
	running  map[string]*nodeGate
	nextPort int
	stopped  bool

	// gateAuth and identity configure every gate the controller starts. In
	// loopback mode identity is unused; in userpass mode a nil identity makes
	// every handshake fail closed rather than falling back to no-auth.
	gateAuth gate.AuthMode
	identity *GateIdentity

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
	return &NodeController{
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
		nextPort:    portBase,
		maxServers:  cfg.MaxServers,
		now:         now,
		cooldown:    cooldown,
		switching:   make(map[string]struct{}),
		lastAttempt: make(map[string]time.Time),
		gateAuth:    gateAuth,
		identity:    cfg.GateIdentity,
		httpEnabled: cfg.GateHTTP,
		httpOffset:  httpOffset,
	}, nil
}

// SyncAccount opens or reuses one gate per usable server of the account's last
// successful login, writes each node, and reconciles the panel once.
//
// It is idempotent: a server that already owns a gate keeps it (and its port),
// and a server that disappeared from the login has its gate closed and its
// node deleted. A per-server failure marks that node DOWN and does not stop
// the remaining servers; the aggregate error is returned only when no node
// started at all. A reconcile failure is always returned because Resin may now
// disagree with the store.
//
// When MaxServers is positive only the first MaxServers server IDs of the
// deterministic sort are desired: the rest are skipped, and on a later sync
// with a lower cap their gates close and their nodes are deleted like any
// server the login no longer lists.
//
// A persisted selection overrides MaxServers and makes exactly one server
// desired (DESIGN-CATALOG §3.6). A selection that no longer resolves is cleared
// with a catalog.selection_lost audit and the default desired set is used. If
// the account is already serving a different server, the move runs through the
// guarded switch so a failed open leaves the old session live.
func (c *NodeController) SyncAccount(ctx context.Context, accountID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		return ErrControllerStopped
	}
	result, ok := c.logins.Result(accountID)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoLoginResult, accountID)
	}
	acct, ok := c.store.Account(accountID)
	if !ok {
		return &store.Error{Kind: store.ErrNotFound, Message: fmt.Sprintf("account %q not found", accountID)}
	}
	identity, ok := c.store.AccountDevice(accountID)
	if !ok {
		return fmt.Errorf("runtime: account %s has no device identity", accountID)
	}
	device, err := account.DeviceForIdentity(identity)
	if err != nil {
		return fmt.Errorf("runtime: account %s: %w", accountID, err)
	}

	servers := usableServers(result.Servers)
	selection, hasSelection := c.store.Selection(accountID)
	selectionApplied := false
	if hasSelection {
		selected, found := findServer(result.Servers, selection.ServerID)
		switch {
		case found && selected.server.OK():
			// The operator's target is the whole desired set, regardless of
			// MaxServers: exactly one session, never a sorted fallback.
			servers = []serverRef{selected}
			selectionApplied = true
			if c.maxServers > 1 {
				c.logger.Info("catalog_selection_limits_desired", "account_id", accountID,
					"server_id", selected.id, "node_limit", c.maxServers)
			}
		default:
			reason := "server_missing"
			if found {
				reason = "server_ineligible"
			}
			if _, err := c.store.LoseSelection(accountID, reason); err != nil {
				c.logger.Warn("catalog_selection_lost_audit_failed", "account_id", accountID, "error", err)
			}
			c.logger.Info("catalog_selection_lost", "account_id", accountID,
				"server_id", selection.ServerID, "reason", reason)
		}
	}
	if !selectionApplied && c.maxServers > 0 && len(servers) > c.maxServers {
		skipped := len(servers) - c.maxServers
		servers = servers[:c.maxServers]
		c.logger.Info("node_limit_skipped", "account_id", accountID, "limit", c.maxServers, "skipped", skipped)
	}
	desired := make(map[string]struct{}, len(servers))
	for _, srv := range servers {
		desired[nodeID(accountID, srv.id)] = struct{}{}
	}

	userName := strings.TrimSpace(result.UserName)
	if userName == "" {
		userName = acct.Username
	}

	// A selection changed while the account was offline must move the session
	// through the same guarded switch as a manual connect, not close the old
	// gate first: an open failure has to keep the old session serving.
	if selectionApplied && len(servers) == 1 {
		if old, err := c.accountGateLocked(accountID); err == nil && old != nil && old.serverID != servers[0].id {
			if _, switchErr := c.switchLocked(ctx, acct, result, servers[0], old, "login", c.now()); switchErr != nil {
				c.logger.Warn("catalog_login_switch_failed", "account_id", accountID, "error", switchErr)
				if reconcileErr := c.reconcileLocked(ctx); reconcileErr != nil {
					return errors.Join(switchErr, reconcileErr)
				}
				return switchErr
			}
		}
	}

	failures := c.removeUndesiredLocked(accountID, desired)

	started := 0
	for _, srv := range servers {
		id := nodeID(accountID, srv.id)
		if running, ok := c.running[id]; ok {
			if err := c.activateLocked(id, acct, srv, running); err != nil {
				failures = append(failures, fmt.Errorf("node %s: %w", id, err))
				c.logger.Warn("node_state_write_failed", "node_id", id, "account_id", accountID, "state", string(model.NodeLive), "error", err)
				continue
			}
			started++
			continue
		}

		session, err := c.sessions.Open(ctx, account.SessionRequest{
			AccountID:       accountID,
			Server:          srv.server,
			Device:          device,
			UserName:        userName,
			ConnectPassword: result.ConnectPassword,
			Profile:         c.profile,
		})
		if err != nil {
			failures = append(failures, fmt.Errorf("node %s: open session: %w", id, err))
			c.logger.Warn("node_session_failed", "node_id", id, "account_id", accountID, "server_id", srv.id, "error", err)
			c.markDownLocked(id, acct, srv)
			continue
		}
		g, addrs, err := c.startGateLocked(id, accountID, srv.id, session)
		if err != nil {
			_ = session.Close()
			failures = append(failures, fmt.Errorf("node %s: start gate: %w", id, err))
			c.logger.Warn("node_gate_failed", "node_id", id, "account_id", accountID, "server_id", srv.id, "error", err)
			c.markDownLocked(id, acct, srv)
			continue
		}
		running := &nodeGate{
			gate:      g,
			session:   session,
			accountID: accountID,
			serverID:  srv.id,
			port:      addrs.socks,
			httpPort:  addrs.http,
			egressIP:  sessionEgress(session),
			startedAt: time.Now(),
		}
		c.running[id] = running
		if err := c.activateLocked(id, acct, srv, running); err != nil {
			failures = append(failures, fmt.Errorf("node %s: %w", id, err))
			c.logger.Warn("node_state_write_failed", "node_id", id, "account_id", accountID, "state", string(model.NodeLive), "error", err)
			failures = append(failures, c.closeGateLocked(id, running)...)
			delete(c.running, id)
			continue
		}
		started++
		c.logger.Info("node_started", "node_id", id, "account_id", accountID, "server_id", srv.id,
			"port", addrs.socks, "http_port", addrs.http, "egress_ip", running.egressIP)
	}

	reconcileErr := c.reconcileLocked(ctx)
	if started > 0 {
		return reconcileErr
	}
	errs := make([]error, 0, len(failures)+2)
	errs = append(errs, fmt.Errorf("runtime: account %s: no node started for %d usable servers", accountID, len(servers)))
	errs = append(errs, failures...)
	if reconcileErr != nil {
		errs = append(errs, reconcileErr)
	}
	return errors.Join(errs...)
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
// It does not reconcile the panel. At this point the account's nodes are still
// LIVE in the store, so a reconcile here would republish the gates that were
// just closed; the API handler reconciles after the store mutation instead.
//
// It is idempotent: with no running gate for the account nothing changed, so
// it returns nil. Close failures are aggregated; a failed close still removes
// the gate from the running set, so a retry cannot double-close it.
func (c *NodeController) StopAccount(_ context.Context, accountID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error
	for id, running := range c.running {
		if running.accountID != accountID {
			continue
		}
		errs = append(errs, c.closeGateLocked(id, running)...)
		delete(c.running, id)
		c.logger.Info("account_gates_stopped", "account_id", accountID, "node_id", id, "server_id", running.serverID)
	}
	return errors.Join(errs...)
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
			NodeID:    id,
			AccountID: running.accountID,
			ServerID:  running.serverID,
			Port:      running.port,
			EgressIP:  running.egressIP,
			Draining:  running.draining,
			StartedAt: running.startedAt,
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

// activateLocked writes a running node. An operator drain survives a login
// refresh: the node stays DRAINING instead of being forced back to LIVE.
func (c *NodeController) activateLocked(id string, acct model.Account, srv serverRef, running *nodeGate) error {
	state := model.NodeLive
	if existing, ok := c.store.Node(id); ok && existing.State == model.NodeDraining {
		state = model.NodeDraining
	}
	if running.draining {
		state = model.NodeDraining
	}
	_, err := c.writeNodeLocked(id, acct, srv, state, running.port, running.egressIP)
	return err
}

// markDownLocked records a server that could not be served. The node keeps its
// lease/probe fields but loses the gate port because no listener is bound.
func (c *NodeController) markDownLocked(id string, acct model.Account, srv serverRef) {
	if _, err := c.writeNodeLocked(id, acct, srv, model.NodeDown, 0, ""); err != nil {
		c.logger.Warn("node_state_write_failed", "node_id", id, "account_id", acct.ID, "state", string(model.NodeDown), "error", err)
	}
}

// writeNodeLocked creates or refreshes one store node from the vendor server.
// An existing node keeps its lease/probe fields through store.PutNode.
func (c *NodeController) writeNodeLocked(id string, acct model.Account, srv serverRef, state model.NodeState, port int, egressIP string) (model.ProxyNode, error) {
	node, ok := c.store.Node(id)
	if !ok {
		node = model.ProxyNode{
			ID:       id,
			Capacity: gateFlowLimit,
			Protocol: model.ProtocolSOCKS5,
		}
	}
	node = applyServerToNode(node, acct, srv, state, port, egressIP)
	return c.store.PutNode(node)
}

// applyServerToNode overlays one vendor server onto a node row. It is shared by
// the login sync (PutNode) and the switch commit (CommitSwitch), so both paths
// write the same label, region and gate fields.
func applyServerToNode(node model.ProxyNode, acct model.Account, srv serverRef, state model.NodeState, port int, egressIP string) model.ProxyNode {
	node.AccountID = acct.ID
	node.AccountLabel = acct.Label
	node.Label = nodeTag(srv, acct.ID)
	node.Region = nodeRegion(srv.server)
	node.Country = serverCountry(srv.server)
	node.City = serverCity(srv.server)
	node.State = state
	node.Protocol = model.ProtocolSOCKS5
	node.GatePort = port
	node.ServerID = srv.id
	if egressIP != "" {
		node.EgressIP = egressIP
	}
	return node
}

// removeUndesiredLocked closes the gates of servers the login no longer lists
// and deletes their nodes, including nodes this controller never owned.
func (c *NodeController) removeUndesiredLocked(accountID string, desired map[string]struct{}) []error {
	var failures []error
	for id, running := range c.running {
		if running.accountID != accountID {
			continue
		}
		if _, keep := desired[id]; keep {
			continue
		}
		failures = append(failures, c.closeGateLocked(id, running)...)
		delete(c.running, id)
		if err := c.store.DeleteNode(id); err != nil && !errors.Is(err, store.ErrNotFound) {
			failures = append(failures, fmt.Errorf("node %s: delete: %w", id, err))
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
		if err := c.store.DeleteNode(node.ID); err != nil {
			failures = append(failures, fmt.Errorf("node %s: delete: %w", node.ID, err))
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

// startGateLocked binds the first free loopback port pair at or above the
// controller's cursor and starts a gate on it. The gate lives on the
// controller's context, never on the login request's. With the HTTP listener
// enabled the candidate must free both p and p+httpOffset; a bind failure on
// either half retries the next base port.
func (c *NodeController) startGateLocked(id, accountID, serverID string, session account.FlowSession) (Gate, gateAddresses, error) {
	reserved := c.reservedPortsLocked()
	attempts := 0
	var lastErr error
	for port := c.nextPort; port <= maxGatePort && attempts < maxGatePortAttempts; port++ {
		if _, used := reserved[port]; used {
			continue
		}
		httpPort := 0
		if c.httpEnabled {
			httpPort = port + c.httpOffset
			if httpPort > maxGatePort {
				// No base at or above the cursor leaves room for the pair.
				break
			}
			if _, used := reserved[httpPort]; used {
				continue
			}
		}
		cfg := gate.Config{
			NodeID:    id,
			AccountID: accountID,
			Listen:    net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			Limit:     gateFlowLimit,
			Logger:    c.logger,
			AuthMode:  c.gateAuth,
		}
		if httpPort != 0 {
			cfg.HTTPListen = net.JoinHostPort("127.0.0.1", strconv.Itoa(httpPort))
		}
		if c.gateAuth == gate.AuthUserPass && c.identity != nil {
			cfg.Authenticator = c.identity
			cfg.UserPolicy = c.identity
		}
		g, err := c.gates(cfg, session)
		if err != nil {
			return nil, gateAddresses{}, err
		}
		if err := g.Start(c.ctx); err != nil {
			_ = g.Close()
			lastErr = err
			attempts++
			continue
		}
		// The configured port is the default; a factory that binds something
		// else (port 0, for example) still reports the real one to the node.
		bound := port
		if addr := g.Addr(); addr != nil {
			if tcp, ok := addr.(*net.TCPAddr); ok && tcp.Port > 0 {
				bound = tcp.Port
			}
		}
		c.nextPort = bound + 1
		return g, gateAddresses{socks: bound, http: httpPort}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("runtime: no free loopback gate port")
	}
	return nil, gateAddresses{}, lastErr
}

// reservedPortsLocked returns the ports already claimed by a running gate or a
// store node, so a new gate never takes another node's port. With the HTTP
// listener enabled the pair's second port is reserved too, and a stored node
// reserves gatePort+offset by derivation because the node row stores one port.
func (c *NodeController) reservedPortsLocked() map[int]struct{} {
	nodes := c.store.Nodes()
	used := make(map[int]struct{}, 2*(len(c.running)+len(nodes)))
	for _, running := range c.running {
		used[running.port] = struct{}{}
		if running.httpPort > 0 {
			used[running.httpPort] = struct{}{}
		}
	}
	for _, node := range nodes {
		if node.GatePort <= 0 {
			continue
		}
		used[node.GatePort] = struct{}{}
		if c.httpEnabled {
			if httpPort := node.GatePort + c.httpOffset; httpPort <= maxGatePort {
				used[httpPort] = struct{}{}
			}
		}
	}
	return used
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

// nodeID is the stable node id for one account/server pair.
func nodeID(accountID, serverID string) string {
	return accountID + ":" + serverID
}

// nodeTag derives the node label, which is also the Resin subscription tag:
// "<country>-<account>-<server>" in the lowercase alphanumeric alphabet
// publisher.ValidTag accepts. The account is part of the tag because vendor
// server ids are shared by every account, so "<country>-<server>" would
// collide across accounts.
func nodeTag(srv serverRef, accountID string) string {
	country := tagPart(serverCountry(srv.server))
	if country == "" {
		country = "xx"
	}
	account := tagPart(accountID)
	if account == "" {
		account = "acct"
	}
	server := tagPart(srv.id)
	if server == "" {
		server = "srv"
	}
	return country + "-" + account + "-" + server
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
// authentication and every policy check closed.
func NewGateIdentity(st *store.Store) *GateIdentity {
	return &GateIdentity{
		store:           st,
		now:             time.Now,
		cacheTTL:        defaultUserQuotaCacheTTL,
		rebuildInterval: defaultDigestRebuildInterval,
		digest:          make(map[[sha256.Size]byte]string),
		policies:        make(map[string]*gateUserPolicy),
		usage:           make(map[string]gateUserUsage),
	}
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
// it from the request log on a miss or after the month changed.
func (g *GateIdentity) monthlyUsage(userID string) int64 {
	now := g.now()
	month := now.UTC().Format("2006-01")

	g.mu.Lock()
	if entry, ok := g.usage[userID]; ok && entry.month == month && now.Before(entry.expires) {
		g.mu.Unlock()
		return entry.bytes
	}
	g.mu.Unlock()

	used := g.storeUserUsage(userID, now)

	g.mu.Lock()
	g.usage[userID] = gateUserUsage{month: month, bytes: used, expires: now.Add(g.cacheTTL)}
	g.mu.Unlock()
	return used
}

// AddUsage advances the cached monthly total for one authenticated user after
// the meter appended an accepted flow. An absent entry stays absent: the next
// admission recomputes from the request log, which already holds that row.
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

// storeUserUsage sums one user's gate-metered bytes in the current UTC month.
// The scan is bounded by the request-log cap and stops at the first row older
// than the month start; rows with an unparseable timestamp are skipped rather
// than ending the scan. It runs on a cache miss only.
func (g *GateIdentity) storeUserUsage(userID string, now time.Time) int64 {
	utc := now.UTC()
	monthStart := time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)

	var total int64
	for _, row := range g.store.Requests() {
		if row.Source != model.SourceGate || row.UserID != userID {
			continue
		}
		ts, err := time.Parse(time.RFC3339, row.TS)
		if err != nil {
			continue
		}
		if ts.UTC().Before(monthStart) {
			break
		}
		total += row.Bytes
	}
	return total
}
