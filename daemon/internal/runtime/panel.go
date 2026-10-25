// Package runtime wires the Resin admin client and publisher into ajs-accountd.
// It owns the startup reconciliation (probe config, subscription, platform,
// publish), the on-demand re-render behind POST /v1/reconcile, the read-only
// view behind GET /v1/topology, and the four runtime mutations: lease rotation
// and release, node drain and undrain.
//
// Mutations are only routed here when a Resin panel is configured. Without
// one, the API keeps calling the store directly and the store's synthetic
// next-IP rotation remains the fallback.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/publisher"
	"ajsaccountd/internal/resin"
	"ajsaccountd/internal/store"
)

// Defaults for the Resin wiring. The subscription name, interval and line
// format mirror docs/PANEL.md §4–§5; the platform defaults mirror the M1
// spike (research/77-m1-spike/REPORT.md).
const (
	DefaultSubscriptionName = "ajs-live"
	DefaultUpdateInterval   = "30s"
	DefaultStickyTTL        = "8h"
	DefaultAllocationPolicy = "BALANCED"
)

// ErrNotStarted is returned by Topology before the first successful
// reconciliation. The API maps it to 503 UPSTREAM_UNAVAILABLE.
var ErrNotStarted = errors.New("runtime: panel has not completed a reconcile yet")

// ErrRotationRateLimited is publisher.ErrRotationRateLimit re-exported so the
// API can map it to 409 without importing the publisher. The error text names
// the account and the remaining window.
var ErrRotationRateLimited = publisher.ErrRotationRateLimit

// Panel is the control-plane view the API exposes. ResinPanel implements it;
// the API needs exactly these methods, so tests can substitute a fake.
type Panel interface {
	Topology(ctx context.Context) (Topology, error)
	Reconcile(ctx context.Context) (Topology, error)
	RotateLease(ctx context.Context, leaseID string) (model.Lease, error)
	ReleaseLease(ctx context.Context, leaseID string) error
	DrainNode(ctx context.Context, nodeID string) (model.ProxyNode, error)
	UndrainNode(ctx context.Context, nodeID string) (model.ProxyNode, error)
}

// Topology is the GET /v1/topology (panel mode) and POST /v1/reconcile body.
// It carries no credentials: ids, the rendered subscription text and node
// id → tag entries only.
type Topology struct {
	SubscriptionID string            `json:"subscription_id"`
	PlatformID     string            `json:"platform_id"`
	Content        string            `json:"content"`
	Nodes          map[string]string `json:"nodes"`
	LastGood       string            `json:"last_good"`
	ResinVersion   string            `json:"resin_version"`
}

// Config is the Resin panel configuration. Zero values select the defaults.
type Config struct {
	SubscriptionName string
	PlatformName     string
	UpdateInterval   string
	StickyTTL        string
	AllocationPolicy string
	Logger           *slog.Logger
}

// ResinPanel renders the store's LIVE nodes into the Resin local subscription
// and keeps the last successful view. It is safe for concurrent use.
type ResinPanel struct {
	store  *store.Store
	client *resin.Client
	pub    *publisher.Publisher
	cfg    Config
	logger *slog.Logger

	// opMu serialises Start and Reconcile: both render, write and publish, so
	// two concurrent runs would race the subscription content.
	opMu sync.Mutex

	mu             sync.Mutex
	started        bool
	subscriptionID string
	platformID     string
	content        string
	nodes          map[string]string
	resinVersion   string
}

// NewResinPanel builds the panel. Nil store, client or publisher is a
// programming error; every zero Config field falls back to its default, and
// an empty platform name defaults to the subscription name.
func NewResinPanel(st *store.Store, client *resin.Client, pub *publisher.Publisher, cfg Config) (*ResinPanel, error) {
	if st == nil {
		return nil, errors.New("runtime: store is required")
	}
	if client == nil {
		return nil, errors.New("runtime: resin client is required")
	}
	if pub == nil {
		return nil, errors.New("runtime: publisher is required")
	}
	if cfg.SubscriptionName == "" {
		cfg.SubscriptionName = DefaultSubscriptionName
	}
	if cfg.PlatformName == "" {
		cfg.PlatformName = cfg.SubscriptionName
	}
	if cfg.UpdateInterval == "" {
		cfg.UpdateInterval = DefaultUpdateInterval
	}
	if cfg.StickyTTL == "" {
		cfg.StickyTTL = DefaultStickyTTL
	}
	if cfg.AllocationPolicy == "" {
		cfg.AllocationPolicy = DefaultAllocationPolicy
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &ResinPanel{
		store:  st,
		client: client,
		pub:    pub,
		cfg:    cfg,
		logger: cfg.Logger,
		nodes:  make(map[string]string),
	}, nil
}

// Start performs the startup reconciliation in the M1-proven order:
//
//  1. PATCH max_latency_test_interval=30s (404/405 tolerated);
//  2. render the LIVE nodes; with none, skip the subscription entirely;
//  3. EnsureSubscription(name, content, interval);
//  4. EnsurePlatform(sticky_ttl, regex_filters, allocation_policy);
//  5. Publish the content (the publisher coalesces and refuses empty).
//
// A failure stops at the failing step; the panel stays unstarted so Topology
// reports ErrNotStarted until a later Reconcile succeeds.
func (p *ResinPanel) Start(ctx context.Context) error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	return p.reconcileLocked(ctx)
}

// Reconcile re-renders the LIVE nodes, publishes them and returns the same
// topology GET /v1/topology serves.
func (p *ResinPanel) Reconcile(ctx context.Context) (Topology, error) {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	if err := p.reconcileLocked(ctx); err != nil {
		return Topology{}, err
	}
	return p.Topology(ctx)
}

// Topology returns the last successful view. It never performs a network call.
func (p *ResinPanel) Topology(ctx context.Context) (Topology, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return Topology{}, ErrNotStarted
	}
	nodes := make(map[string]string, len(p.nodes))
	for id, tag := range p.nodes {
		nodes[id] = tag
	}
	return Topology{
		SubscriptionID: p.subscriptionID,
		PlatformID:     p.platformID,
		Content:        p.content,
		Nodes:          nodes,
		LastGood:       p.pub.LastGood(),
		ResinVersion:   p.resinVersion,
	}, nil
}

// SubscriptionID returns the Resin subscription id recorded by the last
// successful reconciliation, or "" before the first one.
func (p *ResinPanel) SubscriptionID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.subscriptionID
}

// PlatformID returns the Resin platform id recorded by the last successful
// reconciliation, or "" before the first one.
func (p *ResinPanel) PlatformID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.platformID
}

// Close releases the panel. The admin client owns no long-lived resources
// (net/http keeps idle connections only), so this is a no-op that exists for
// the daemon's shutdown sequence.
func (p *ResinPanel) Close() error { return nil }

// RotateLease moves a leased account off its current node through the
// M1-proven filter-exclusion path and records the egress IP/expiry Resin
// reports for the new lease.
//
// The exclusion tag is the full Resin tag "<subscription>/<node tag>" taken
// from the last published topology; delete-only rotation would re-pick the
// same node (research/77-m1-spike/REPORT.md). A missing store lease maps to
// 404; a missing platform or topology tag maps to 409; Resin failures wrap
// store.ErrUpstream (503). The publisher's per-account rate limit is returned
// unchanged so the API can answer 409.
//
// The whole sequence holds opMu, so a concurrent Reconcile (whose
// EnsurePlatform PATCHes the same regex_filters) or Drain/Undrain cannot reset
// the exclusion filter between the PATCH and the lease delete.
func (p *ResinPanel) RotateLease(ctx context.Context, leaseID string) (model.Lease, error) {
	p.opMu.Lock()
	defer p.opMu.Unlock()

	lease, ok := p.store.Lease(leaseID)
	if !ok {
		return model.Lease{}, fmt.Errorf("%w: lease %q not found", store.ErrNotFound, leaseID)
	}
	platformID := p.PlatformID()
	if platformID == "" {
		return model.Lease{}, fmt.Errorf("%w: the Resin panel has no platform yet; POST /v1/reconcile first", store.ErrConflict)
	}
	tag, ok := p.nodeTag(lease.NodeID)
	if !ok {
		return model.Lease{}, fmt.Errorf("%w: node %s is not in the published topology; reconcile before rotating", store.ErrConflict, lease.NodeID)
	}
	if err := p.pub.Rotate(ctx, platformID, lease.AccountID, p.cfg.SubscriptionName+"/"+tag, true); err != nil {
		if errors.Is(err, publisher.ErrRotationRateLimit) {
			return model.Lease{}, err
		}
		return model.Lease{}, fmt.Errorf("%w: rotate lease %q: %w", store.ErrUpstream, leaseID, err)
	}
	rotated, err := p.client.GetLease(ctx, platformID, lease.AccountID)
	if err != nil {
		return model.Lease{}, fmt.Errorf("%w: read rotated lease %q: %w", store.ErrUpstream, leaseID, err)
	}
	// Requests is not part of Resin's lease; the local counter is preserved
	// rather than incremented with a synthetic value.
	return p.store.ApplyLeaseRotation(lease.ID, rotated.EgressIP, rotated.Expiry, lease.Requests)
}

// ReleaseLease deletes the Resin lease for the store row's account (404 is
// idempotent in the client) and then removes the store row. A missing store
// lease is 404; a missing platform is 409; Resin failures wrap
// store.ErrUpstream.
func (p *ResinPanel) ReleaseLease(ctx context.Context, leaseID string) error {
	lease, ok := p.store.Lease(leaseID)
	if !ok {
		return fmt.Errorf("%w: lease %q not found", store.ErrNotFound, leaseID)
	}
	platformID := p.PlatformID()
	if platformID == "" {
		return fmt.Errorf("%w: the Resin panel has no platform yet; POST /v1/reconcile first", store.ErrConflict)
	}
	if err := p.client.DeleteLease(ctx, platformID, lease.AccountID); err != nil {
		return fmt.Errorf("%w: delete lease %q: %w", store.ErrUpstream, leaseID, err)
	}
	return p.store.ReleaseLease(leaseID)
}

// WithdrawLease deletes the external Resin lease for one account. It is the
// same-operation cleanup of a failed break-before-make switch: the old
// listener is gone, so Resin must not keep leasing the account to a dead port.
// A missing lease is a no-op (DeleteLease treats 404 as success). Without a
// known platform there is nothing Resin can lease, so the call is a no-op.
func (p *ResinPanel) WithdrawLease(ctx context.Context, accountID string) error {
	platformID := p.PlatformID()
	if platformID == "" {
		return nil
	}
	if err := p.client.DeleteLease(ctx, platformID, accountID); err != nil {
		return fmt.Errorf("%w: withdraw lease for account %q: %w", store.ErrUpstream, accountID, err)
	}
	return nil
}

// DrainNode marks the node DRAINING in the store and republishes the
// remaining LIVE nodes. When the publish fails the node is already DRAINING
// and the error is returned alongside it, so the API can answer 503 without
// losing the state transition.
func (p *ResinPanel) DrainNode(ctx context.Context, nodeID string) (model.ProxyNode, error) {
	node, err := p.store.DrainNode(nodeID)
	if err != nil {
		return model.ProxyNode{}, err
	}
	if _, err := p.Reconcile(ctx); err != nil {
		p.logger.Warn("resin: publish after drain failed", "node", nodeID, "error", err)
		return node, err
	}
	return node, nil
}

// UndrainNode returns the node to service in the store and republishes the
// LIVE set, mirroring DrainNode's partial-success behaviour.
func (p *ResinPanel) UndrainNode(ctx context.Context, nodeID string) (model.ProxyNode, error) {
	node, err := p.store.UndrainNode(nodeID)
	if err != nil {
		return model.ProxyNode{}, err
	}
	if _, err := p.Reconcile(ctx); err != nil {
		p.logger.Warn("resin: publish after undrain failed", "node", nodeID, "error", err)
		return node, err
	}
	return node, nil
}

// nodeTag returns the published tag for a node id. Only LIVE nodes rendered by
// the last reconciliation are present.
func (p *ResinPanel) nodeTag(nodeID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tag, ok := p.nodes[nodeID]
	return tag, ok
}

// reconcileLocked runs the startup/reconcile sequence. The caller holds opMu.
func (p *ResinPanel) reconcileLocked(ctx context.Context) error {
	if err := p.pub.EnsureProbeConfig(ctx); err != nil {
		return fmt.Errorf("runtime: ensure probe config: %w", err)
	}

	nodes, content, index, err := p.render()
	if err != nil {
		return err
	}

	subscriptionID := ""
	if content == "" {
		// Resin rejects empty content on create and PATCH, and a parsed empty
		// body would evict every node. A pool with zero LIVE nodes must not
		// keep advertising its last (now dead) port either, so the subscription
		// is withdrawn: absence is the explicit cleared marker, and the next
		// LIVE render recreates it through EnsureSubscription.
		//
		// A restarted process has no in-memory subscription id, while Resin
		// still persists the subscription from the previous run. Resolve the
		// id by exact name before withdrawing; without this lookup the
		// publisher's empty-id no-op leaves the dead port advertised until a
		// login republishes.
		if p.pub.SubscriptionID() == "" {
			existing, found, err := p.client.FindSubscription(ctx, p.cfg.SubscriptionName)
			if err != nil {
				return fmt.Errorf("runtime: find subscription %q to withdraw: %w", p.cfg.SubscriptionName, err)
			}
			if found {
				p.pub.SetSubscriptionID(existing.ID)
			}
		}
		if err := p.pub.Withdraw(ctx); err != nil {
			return fmt.Errorf("runtime: withdraw empty subscription: %w", err)
		}
		p.logger.Warn("resin: no LIVE nodes; subscription withdrawn",
			"subscription", p.cfg.SubscriptionName)
	} else {
		subscription, _, err := p.client.EnsureSubscription(ctx, p.cfg.SubscriptionName, content, p.cfg.UpdateInterval)
		if err != nil {
			return fmt.Errorf("runtime: ensure subscription %q: %w", p.cfg.SubscriptionName, err)
		}
		subscriptionID = subscription.ID
		p.pub.SetSubscriptionID(subscription.ID)
	}

	platform, _, err := p.client.EnsurePlatform(ctx, p.platformSpec())
	if err != nil {
		return fmt.Errorf("runtime: ensure platform %q: %w", p.cfg.PlatformName, err)
	}

	if content != "" {
		if err := p.pub.Publish(ctx, nodes); err != nil {
			return fmt.Errorf("runtime: publish subscription: %w", err)
		}
	}

	// The build identity is informational: an older Resin that lacks the
	// route must not stop the subscription from serving.
	version := p.version()
	if info, err := p.client.SystemInfo(ctx); err != nil {
		p.logger.Warn("resin: system info unavailable", "error", err)
	} else {
		version = info.Version
	}

	p.mu.Lock()
	p.started = true
	// Unconditional: an empty render has just withdrawn the subscription, so
	// the recorded id must not survive the withdrawal.
	p.subscriptionID = subscriptionID
	p.platformID = platform.ID
	p.content = content
	p.nodes = index
	p.resinVersion = version
	p.mu.Unlock()
	return nil
}

// render turns the store's LIVE nodes into publisher nodes, the subscription
// body and the node-id → tag map Topology serves. DRAINING, DOWN, READY and
// STARTING nodes never reach Resin (docs/PANEL.md §8).
func (p *ResinPanel) render() ([]publisher.Node, string, map[string]string, error) {
	storeNodes := p.store.Nodes()
	live := make([]publisher.Node, 0, len(storeNodes))
	index := make(map[string]string, len(storeNodes))
	for _, node := range storeNodes {
		if node.State != model.NodeLive {
			continue
		}
		live = append(live, publisher.Node{Tag: node.Label, GatePort: node.GatePort})
		index[node.ID] = node.Label
	}
	content, err := publisher.Render(live)
	if err != nil {
		return nil, "", nil, fmt.Errorf("runtime: render subscription: %w", err)
	}
	return live, content, index, nil
}

// platformSpec is the desired Platform. sticky_ttl is the panel setting the
// dashboard edits (model.PanelSettings.LeaseTTL, default "8h");
// allocation_policy has no store setting yet, so it comes from Config
// (default BALANCED). RegexFilters are always derived from the subscription
// name, matching "ajs-live/.*" in docs/PANEL.md §4.
func (p *ResinPanel) platformSpec() resin.PlatformSpec {
	return resin.PlatformSpec{
		Name:             p.cfg.PlatformName,
		StickyTTL:        p.stickyTTL(),
		RegexFilters:     []string{p.cfg.SubscriptionName + "/.*"},
		AllocationPolicy: p.cfg.AllocationPolicy,
	}
}

// stickyTTL reads the panel setting and falls back to the configured default
// when the store has none.
func (p *ResinPanel) stickyTTL() string {
	if ttl := p.store.Settings().LeaseTTL; ttl != "" {
		return ttl
	}
	return p.cfg.StickyTTL
}

// version returns the last recorded Resin build identity.
func (p *ResinPanel) version() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resinVersion
}
