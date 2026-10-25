// Package publisher renders the Resin subscription from local nodes and
// performs the admin-API writes ajs-accountd needs: coalesced publishes,
// filter-exclusion rotation, and lease cleanup for open circuits.
//
// The package is deliberately offline: it only talks to a resin.Client, and
// every decision is testable with a fake clock and sleep hook.
package publisher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"ajsaccountd/internal/resin"
)

// Defaults for the publisher. MinPublishInterval matches docs/PANEL.md §4:
// Resin coalesces nothing, so the caller must.
const (
	DefaultMinPublishInterval = 5 * time.Second
	DefaultRefreshAttempts    = 3
	DefaultRefreshBackoff     = 250 * time.Millisecond
	DefaultRotationInterval   = 10 * time.Second
	DefaultRestoreAttempts    = 3
	DefaultRestoreBackoff     = 250 * time.Millisecond
)

// ProbeConfigKey and ProbeConfigValue are the M1-mandated runtime setting: the
// default max_latency_test_interval is 1h, which leaves a restarted node
// unroutable until the next probe.
const (
	ProbeConfigKey   = "max_latency_test_interval"
	ProbeConfigValue = "30s"
)

// Errors returned by the publisher. Callers use errors.Is.
var (
	ErrEmptyPublish      = errors.New("publisher: refusing to publish an empty subscription; Resin rejects empty content, and a parsed empty body would evict the pool")
	ErrDuplicateTag      = errors.New("publisher: duplicate node tag")
	ErrInvalidTag        = errors.New("publisher: invalid node tag")
	ErrInvalidPort       = errors.New("publisher: invalid gate port")
	ErrRotationRateLimit = errors.New("publisher: rotation rate limited")
)

// Client is the subset of *resin.Client the publisher uses. It exists so the
// publisher can be unit-tested without an HTTP server; production code passes
// the real client.
type Client interface {
	PatchSubscription(ctx context.Context, id, content string) (resin.Subscription, error)
	DeleteSubscription(ctx context.Context, id string) error
	RefreshSubscription(ctx context.Context, id string) error
	GetPlatform(ctx context.Context, id string) (resin.Platform, error)
	PatchPlatformFilters(ctx context.Context, platformID string, filters []string) (resin.Platform, error)
	DeleteLease(ctx context.Context, platformID, account string) error
	PatchSystemConfig(ctx context.Context, key string, value any) error
}

// Node is one publishable node: an alive, non-draining gate with its stable
// tag. The caller filters DRAINING/DOWN nodes out; Resin must never see them.
type Node struct {
	Tag      string
	GatePort int
}

// NodeHealth is the publisher's view of a Resin node plus the local lifecycle
// state Resin does not know about.
type NodeHealth struct {
	Hash             string
	CircuitOpenSince string
	Draining         bool
}

// LeaseID identifies a deleted lease.
type LeaseID struct {
	PlatformID string
	Account    string
}

// String renders the lease key as "<platform>/<account>".
func (l LeaseID) String() string { return l.PlatformID + "/" + l.Account }

// Config is the publisher configuration. Zero values select the defaults.
type Config struct {
	SubscriptionID       string
	MinPublishInterval   time.Duration
	RefreshAttempts      int
	RefreshBackoff       time.Duration
	RotationMinInterval  time.Duration
	RotationRestoreDelay time.Duration
	RestoreAttempts      int
	RestoreBackoff       time.Duration
	// Now and Sleep are injectable for deterministic tests. Sleep must let
	// simulated time pass (a fake should advance the same clock Now reads).
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration) error
	Logger *slog.Logger
}

// Publisher renders and publishes the subscription. It is safe for concurrent
// use.
type Publisher struct {
	client Client
	cfg    Config
	logger *slog.Logger

	mu            sync.Mutex
	publishMu     sync.Mutex
	rotateMu      sync.Mutex
	subscription  string
	lastGood      string
	lastPublishAt time.Time
	needsRefresh  bool
	rotations     map[string]time.Time
}

// New builds a publisher. A nil client is a programming error and is reported
// by every method that needs it.
func New(client Client, cfg Config) *Publisher {
	if cfg.MinPublishInterval <= 0 {
		cfg.MinPublishInterval = DefaultMinPublishInterval
	}
	if cfg.RefreshAttempts <= 0 {
		cfg.RefreshAttempts = DefaultRefreshAttempts
	}
	if cfg.RefreshBackoff <= 0 {
		cfg.RefreshBackoff = DefaultRefreshBackoff
	}
	if cfg.RotationMinInterval <= 0 {
		cfg.RotationMinInterval = DefaultRotationInterval
	}
	if cfg.RestoreAttempts <= 0 {
		cfg.RestoreAttempts = DefaultRestoreAttempts
	}
	if cfg.RestoreBackoff <= 0 {
		cfg.RestoreBackoff = DefaultRestoreBackoff
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepContext
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Publisher{
		client:       client,
		cfg:          cfg,
		logger:       cfg.Logger,
		subscription: cfg.SubscriptionID,
		rotations:    make(map[string]time.Time),
	}
}

// SetSubscriptionID sets the Resin subscription id used by Publish. It is
// called after EnsureSubscription when the id was not known at construction.
func (p *Publisher) SetSubscriptionID(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subscription = id
}

// LastGood returns the last content Resin accepted, or "" before the first
// successful publish.
func (p *Publisher) LastGood() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastGood
}

// Render builds the subscription body:
//
//	socks5://127.0.0.1:<gatePort>#<tag>
//
// one line per node, sorted by tag, with a trailing newline. Duplicate tags,
// invalid tags and invalid ports are rejected. With no nodes the result is the
// empty string, which is the only case where an empty publish is legitimate.
func Render(nodes []Node) (string, error) {
	sorted := append([]Node(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Tag < sorted[j].Tag })

	lines := make([]string, 0, len(sorted))
	seen := make(map[string]struct{}, len(sorted))
	for _, node := range sorted {
		if !ValidTag(node.Tag) {
			return "", fmt.Errorf("%w: %q", ErrInvalidTag, node.Tag)
		}
		if node.GatePort < 1 || node.GatePort > 65535 {
			return "", fmt.Errorf("%w: %d", ErrInvalidPort, node.GatePort)
		}
		if _, dup := seen[node.Tag]; dup {
			return "", fmt.Errorf("%w: %q", ErrDuplicateTag, node.Tag)
		}
		seen[node.Tag] = struct{}{}
		lines = append(lines, fmt.Sprintf("socks5://127.0.0.1:%d#%s", node.GatePort, node.Tag))
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// ValidTag reports whether tag is lowercase ASCII "<part>[-<part>...]" with
// alphanumeric parts, i.e. the "<cc>-<subdiv>-<seq>" shape.
func ValidTag(tag string) bool {
	if tag == "" {
		return false
	}
	partLen := 0
	for i := range len(tag) {
		c := tag[i]
		switch {
		case c == '-':
			if partLen == 0 {
				return false
			}
			partLen = 0
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			partLen++
		default:
			return false
		}
	}
	return partLen > 0
}

// TagAllocator derives stable tags from vendor server metadata. The sequence
// number is assigned once per (account, server), so reordering the server list
// never changes a tag.
type TagAllocator struct {
	mu   sync.Mutex
	seqs map[string]int
	next int
}

// NewTagAllocator returns an empty allocator.
func NewTagAllocator() *TagAllocator {
	return &TagAllocator{seqs: make(map[string]int)}
}

// Tag returns "<cc>[-<subdiv>]-<seq>" for one server. cc/subdiv are normalised
// to lowercase ASCII; an empty country becomes "xx". The same (account,
// server) pair always yields the same tag from the same allocator.
func (a *TagAllocator) Tag(cc, subdiv, account, server string) string {
	key := account + "\x00" + server
	a.mu.Lock()
	seq, ok := a.seqs[key]
	if !ok {
		a.next++
		seq = a.next
		a.seqs[key] = seq
	}
	a.mu.Unlock()

	parts := make([]string, 0, 3)
	country := normalizePart(cc)
	if country == "" {
		country = "xx"
	}
	parts = append(parts, country)
	if region := normalizePart(subdiv); region != "" {
		parts = append(parts, region)
	}
	parts = append(parts, fmt.Sprintf("%02d", seq))
	return strings.Join(parts, "-")
}

// normalizePart lowercases and keeps ASCII letters and digits only.
func normalizePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	b.Grow(len(value))
	for i := range len(value) {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Publish renders nodes and PATCHes the subscription when the content changed.
// Empty content is always refused: Resin v1.3.0 rejects it on both create and
// PATCH ("content: must be a non-empty string"), and a valid empty body would
// diff every node away. A fully drained pool must delete or disable the
// subscription instead. Publishes are coalesced to at most one per
// MinPublishInterval; identical content is a no-op unless a previous refresh
// failed. The refresh retries with exponential backoff and never rolls back
// the accepted content.
func (p *Publisher) Publish(ctx context.Context, nodes []Node) error {
	p.publishMu.Lock()
	defer p.publishMu.Unlock()

	content, err := Render(nodes)
	if err != nil {
		return err
	}
	if content == "" {
		return ErrEmptyPublish
	}

	p.mu.Lock()
	subscription := p.subscription
	unchanged := content == p.lastGood && !p.needsRefresh
	lastPublish := p.lastPublishAt
	p.mu.Unlock()

	if subscription == "" {
		return errors.New("publisher: subscription id is not set")
	}
	if unchanged {
		return nil
	}

	if !lastPublish.IsZero() {
		if elapsed := p.cfg.Now().Sub(lastPublish); elapsed < p.cfg.MinPublishInterval {
			if err := p.cfg.Sleep(ctx, p.cfg.MinPublishInterval-elapsed); err != nil {
				return err
			}
		}
	}

	p.mu.Lock()
	alreadyAccepted := content == p.lastGood
	p.mu.Unlock()
	if !alreadyAccepted {
		if p.client == nil {
			return errors.New("publisher: nil resin client")
		}
		if _, err := p.client.PatchSubscription(ctx, subscription, content); err != nil {
			return fmt.Errorf("publisher: patch subscription: %w", err)
		}
		p.mu.Lock()
		p.lastGood = content
		p.lastPublishAt = p.cfg.Now()
		p.mu.Unlock()
	}
	return p.refresh(ctx, subscription)
}

// Withdraw removes the published subscription so Resin stops advertising a
// pool that has no LIVE nodes. It is the empty-render path: Resin rejects an
// empty content PATCH, so the subscription's absence is the explicit cleared
// marker. Before the first publish there is nothing to withdraw and it is a
// no-op; a subscription Resin already deleted (NOT_FOUND) is also a no-op. The
// panel recreates the subscription through EnsureSubscription on the next
// render with a LIVE node.
func (p *Publisher) Withdraw(ctx context.Context) error {
	p.publishMu.Lock()
	defer p.publishMu.Unlock()

	p.mu.Lock()
	subscription := p.subscription
	p.mu.Unlock()
	if subscription == "" {
		return nil
	}
	if p.client == nil {
		return errors.New("publisher: nil resin client")
	}
	if err := p.client.DeleteSubscription(ctx, subscription); err != nil && !resin.IsNotFound(err) {
		return fmt.Errorf("publisher: delete subscription: %w", err)
	}
	p.mu.Lock()
	p.subscription = ""
	p.lastGood = ""
	p.lastPublishAt = time.Time{}
	p.needsRefresh = false
	p.mu.Unlock()
	return nil
}

// refresh forces a subscription update, retrying with backoff. On total
// failure the accepted content stays last-known-good and the next Publish
// retries the refresh without a second PATCH.
func (p *Publisher) refresh(ctx context.Context, subscription string) error {
	if p.client == nil {
		return errors.New("publisher: nil resin client")
	}
	var err error
	for attempt := range p.cfg.RefreshAttempts {
		if attempt > 0 {
			backoff := p.cfg.RefreshBackoff << (attempt - 1)
			if sleepErr := p.cfg.Sleep(ctx, backoff); sleepErr != nil {
				err = sleepErr
				break
			}
		}
		err = p.client.RefreshSubscription(ctx, subscription)
		if err == nil {
			p.mu.Lock()
			p.needsRefresh = false
			p.lastPublishAt = p.cfg.Now()
			p.mu.Unlock()
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		p.logger.Warn("resin refresh failed", "attempt", attempt+1, "error", err)
	}
	p.mu.Lock()
	p.needsRefresh = true
	p.mu.Unlock()
	return fmt.Errorf("publisher: refresh subscription: %w", err)
}

// Rotate moves an account off its current node. When avoidCurrentIP is true it
// uses the M1-proven filter-exclusion path (delete-lease alone re-picks the
// same node): read filters, add "!^<currentTag>$", PATCH, delete the lease,
// then restore the original filters. Restoration runs even when the delete
// fails and is retried with backoff. currentTag is the full Resin tag,
// "<subscription>/<tag>".
//
// The whole read-modify-write is serialised by rotateMu: rotations for
// different accounts are allowed by the per-account rate limit, and without
// the lock one rotation's restore would drop another's exclusion before its
// lease delete. ResinPanel.RotateLease additionally holds its opMu, so a
// concurrent reconcile cannot reset the filters mid-rotation.
func (p *Publisher) Rotate(ctx context.Context, platformID, account, currentTag string, avoidCurrentIP bool) error {
	if platformID == "" || account == "" {
		return errors.New("publisher: platform id and account are required")
	}
	if avoidCurrentIP && currentTag == "" {
		return errors.New("publisher: current tag is required for exclusion rotation")
	}
	if p.client == nil {
		return errors.New("publisher: nil resin client")
	}
	p.rotateMu.Lock()
	defer p.rotateMu.Unlock()

	if err := p.reserveRotation(account); err != nil {
		return err
	}
	if !avoidCurrentIP {
		return p.client.DeleteLease(ctx, platformID, account)
	}

	platform, err := p.client.GetPlatform(ctx, platformID)
	if err != nil {
		return fmt.Errorf("publisher: read platform filters: %w", err)
	}
	original := append([]string(nil), platform.RegexFilters...)
	exclusion := "!^" + currentTag + "$"
	if !containsString(original, exclusion) {
		excluded := append(append([]string(nil), original...), exclusion)
		if _, err := p.client.PatchPlatformFilters(ctx, platformID, excluded); err != nil {
			return fmt.Errorf("publisher: exclude current tag: %w", err)
		}
	}

	deleteErr := p.client.DeleteLease(ctx, platformID, account)
	if deleteErr != nil {
		deleteErr = fmt.Errorf("publisher: delete lease: %w", deleteErr)
	}
	restoreErr := p.restoreFilters(ctx, platformID, original)
	return errors.Join(deleteErr, restoreErr)
}

// reserveRotation enforces one rotation per account per RotationMinInterval.
// The timestamp is recorded before the attempt so failures are also limited.
func (p *Publisher) reserveRotation(account string) error {
	now := p.cfg.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if last, ok := p.rotations[account]; ok {
		if elapsed := now.Sub(last); elapsed < p.cfg.RotationMinInterval {
			return fmt.Errorf("%w: account %s, retry in %s",
				ErrRotationRateLimit, account, p.cfg.RotationMinInterval-elapsed)
		}
	}
	p.rotations[account] = now
	return nil
}

// restoreFilters PATCHes the original filter list, retrying with backoff. It
// waits RotationRestoreDelay first when configured.
func (p *Publisher) restoreFilters(ctx context.Context, platformID string, original []string) error {
	if p.cfg.RotationRestoreDelay > 0 {
		if err := p.cfg.Sleep(ctx, p.cfg.RotationRestoreDelay); err != nil {
			return err
		}
	}
	var err error
	for attempt := range p.cfg.RestoreAttempts {
		if attempt > 0 {
			backoff := p.cfg.RestoreBackoff << (attempt - 1)
			if sleepErr := p.cfg.Sleep(ctx, backoff); sleepErr != nil {
				return errors.Join(err, sleepErr)
			}
		}
		if _, err = p.client.PatchPlatformFilters(ctx, platformID, original); err == nil {
			return nil
		}
		p.logger.Warn("resin filter restore failed", "attempt", attempt+1, "error", err)
	}
	return fmt.Errorf("publisher: restore platform filters: %w", err)
}

// DropLeasesOnOpenCircuits deletes leases pinned to nodes whose circuit is
// open, so Resin re-selects instead of waiting for the probe cycle. DRAINING
// nodes are excluded: their leases are released by the drain path, not here.
func (p *Publisher) DropLeasesOnOpenCircuits(ctx context.Context, platformID string, nodes []NodeHealth, leases []resin.Lease) ([]LeaseID, error) {
	open := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if node.Draining || node.CircuitOpenSince == "" {
			continue
		}
		open[node.Hash] = struct{}{}
	}
	if len(open) == 0 {
		return nil, nil
	}
	if p.client == nil {
		return nil, errors.New("publisher: nil resin client")
	}

	deleted := make([]LeaseID, 0, len(leases))
	seen := make(map[LeaseID]struct{}, len(leases))
	var errs []error
	for _, lease := range leases {
		if _, ok := open[lease.NodeHash]; !ok {
			continue
		}
		if lease.PlatformID != "" && lease.PlatformID != platformID {
			continue
		}
		id := LeaseID{PlatformID: platformID, Account: lease.Account}
		if _, dup := seen[id]; dup {
			continue
		}
		if err := p.client.DeleteLease(ctx, platformID, lease.Account); err != nil {
			errs = append(errs, fmt.Errorf("delete lease %s: %w", id, err))
			continue
		}
		seen[id] = struct{}{}
		deleted = append(deleted, id)
	}
	return deleted, errors.Join(errs...)
}

// EnsureProbeConfig sets max_latency_test_interval=30s. Older Resin builds
// without the runtime-config route (404/405) are tolerated with a warning:
// node recovery then depends on the 1h default.
func (p *Publisher) EnsureProbeConfig(ctx context.Context) error {
	if p.client == nil {
		return errors.New("publisher: nil resin client")
	}
	err := p.client.PatchSystemConfig(ctx, ProbeConfigKey, ProbeConfigValue)
	if err == nil {
		return nil
	}
	if resin.IsNotFound(err) || resin.IsStatus(err, 405) {
		p.logger.Warn("resin does not support runtime config; node recovery may take up to 1h",
			"key", ProbeConfigKey, "error", err)
		return nil
	}
	return fmt.Errorf("publisher: set %s: %w", ProbeConfigKey, err)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// sleepContext waits for d or until ctx is done.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
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
