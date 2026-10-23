package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"

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
	Logger  *slog.Logger
	// PortBase is the first port tried; zero selects DefaultGatePortBase.
	PortBase int
}

// nodeGate is one running gate plus the session it serves.
type nodeGate struct {
	gate      Gate
	session   account.FlowSession
	accountID string
	serverID  string
	port      int
	egressIP  string
	draining  bool
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

	// ctx is the gate lifetime. Stop cancels it and closes every gate.
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	running  map[string]*nodeGate
	nextPort int
	stopped  bool
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
	ctx, cancel := context.WithCancel(context.Background())
	return &NodeController{
		store:      cfg.Store,
		logins:     cfg.Logins,
		sessions:   cfg.Sessions,
		gates:      gates,
		reconciler: cfg.Reconciler,
		profile:    profile,
		logger:     logger,
		portBase:   portBase,
		ctx:        ctx,
		cancel:     cancel,
		running:    make(map[string]*nodeGate),
		nextPort:   portBase,
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
	desired := make(map[string]struct{}, len(servers))
	for _, srv := range servers {
		desired[nodeID(accountID, srv.id)] = struct{}{}
	}

	failures := c.removeUndesiredLocked(accountID, desired)

	userName := strings.TrimSpace(result.UserName)
	if userName == "" {
		userName = acct.Username
	}

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
		g, port, err := c.startGateLocked(id, accountID, srv.id, session)
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
			port:      port,
			egressIP:  sessionEgress(session),
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
			"port", port, "egress_ip", running.egressIP)
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
	return c.store.PutNode(node)
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

// startGateLocked binds the first free loopback port at or above the
// controller's cursor and starts a gate on it. The gate lives on the
// controller's context, never on the login request's.
func (c *NodeController) startGateLocked(id, accountID, serverID string, session account.FlowSession) (Gate, int, error) {
	reserved := c.reservedPortsLocked()
	attempts := 0
	var lastErr error
	for port := c.nextPort; port <= maxGatePort && attempts < maxGatePortAttempts; port++ {
		if _, used := reserved[port]; used {
			continue
		}
		cfg := gate.Config{
			NodeID:    id,
			AccountID: accountID,
			Listen:    net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			Limit:     gateFlowLimit,
			Logger:    c.logger,
		}
		g, err := c.gates(cfg, session)
		if err != nil {
			return nil, 0, err
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
		return g, bound, nil
	}
	if lastErr == nil {
		lastErr = errors.New("runtime: no free loopback gate port")
	}
	return nil, 0, lastErr
}

// reservedPortsLocked returns the ports already claimed by a running gate or a
// store node, so a new gate never takes another node's port.
func (c *NodeController) reservedPortsLocked() map[int]struct{} {
	nodes := c.store.Nodes()
	used := make(map[int]struct{}, len(c.running)+len(nodes))
	for _, running := range c.running {
		used[running.port] = struct{}{}
	}
	for _, node := range nodes {
		if node.GatePort > 0 {
			used[node.GatePort] = struct{}{}
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
