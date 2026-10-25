package store

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// Lease allocation bounds. A lease must last long enough to be useful and no
// longer than a week, which is also the widest TTL the panel offers.
const (
	MinLeaseTTLSeconds     = 60
	MaxLeaseTTLSeconds     = 7 * 24 * 3600
	DefaultLeaseTTLSeconds = 8 * 3600
	maxIdempotencyKeyLen   = 128
)

// Account slot bounds. The vendor sells UseCount between 1 and 2000; the
// operator cap AJS_ACCOUNT_SLOTS uses the same range and defaults to one.
const (
	defaultAccountSlots = 1
	maxAccountSlots     = 2000
	accountSlotsEnv     = "AJS_ACCOUNT_SLOTS"
)

// normalizeAccountUseCount keeps the vendor's UseCount in its documented
// range. A missing field (zero) means one simultaneous connection.
func normalizeAccountUseCount(useCount int) int {
	if useCount < 1 {
		return defaultAccountSlots
	}
	if useCount > maxAccountSlots {
		return maxAccountSlots
	}
	return useCount
}

// configuredAccountSlots reads AJS_ACCOUNT_SLOTS. This package cannot import
// internal/account (that package imports store), so the small parse lives
// here; invalid or out-of-range values fall back to the safe default of one
// rather than widening the cap.
func configuredAccountSlots() int {
	raw := strings.TrimSpace(os.Getenv(accountSlotsEnv))
	if raw == "" {
		return defaultAccountSlots
	}
	slots, err := strconv.Atoi(raw)
	if err != nil || slots < 1 || slots > maxAccountSlots {
		return defaultAccountSlots
	}
	return slots
}

// LeaseError is a create/revoke refusal carrying the machine-readable reason
// the API copies into the error envelope. Kind is one of ErrInvalid,
// ErrNotFound, ErrConflict or ErrQuota and is matched with errors.Is.
type LeaseError struct {
	Kind    error
	Reason  string
	Message string
}

func (e *LeaseError) Error() string { return e.Message }

func (e *LeaseError) Unwrap() error { return e.Kind }

func leaseErrorf(kind error, reason, format string, args ...any) error {
	return &LeaseError{Kind: kind, Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// SetAccountUseCount records the vendor's purchased simultaneous-connection
// allowance (UseCount) for one account. It is a login-result field, not an
// operator setting: zero or negative means the vendor omitted it and is stored
// as one, and values above 2000 are clamped. The value persists with the next
// state snapshot.
func (s *Store) SetAccountUseCount(id string, useCount int) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.accountIndexLocked(id)
	if idx < 0 {
		return model.Account{}, notFoundf("account %q not found", id)
	}
	s.accounts[idx].UseCount = normalizeAccountUseCount(useCount)
	return cloneAccount(s.accounts[idx]), nil
}

// CreateLease allocates one free node to a user. The bool reports an
// idempotent replay: the same IdempotencyKey already produced the returned
// lease, so nothing was allocated a second time.
//
// Refusals are LeaseErrors with these reasons:
//
//	lease_conflict        the user already holds an active lease for platform
//	user_disabled         the user is suspended
//	account_unavailable   the pinned account is paused, failed or expired
//	quota_exceeded        the user's monthly quota is exhausted
//	concurrency_exceeded  the account already runs its concurrency limit
//	account_slots_exhausted the account already holds its slot capacity
//	                      (slot allowance capped by the nodes that serve)
//	no_capacity           no active account of the platform has a free node
//
// One lease per node is enforced by excluding every node that already has an
// active lease. Only a LIVE node with a bound gate port is a target: READY is
// the seeded/demo state and still carries the node's recorded egress and port,
// but no listener, so leasing it would advertise a dead endpoint.
func (s *Store) CreateLease(in model.CreateLeaseInput) (model.Lease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.sweepExpiredLocked(now)

	userID := strings.TrimSpace(in.UserID)
	if userID == "" {
		return model.Lease{}, false, leaseErrorf(ErrInvalid, "invalid_lease", "userId is required")
	}
	if !validPlatform(in.Platform) {
		return model.Lease{}, false, leaseErrorf(ErrInvalid, "invalid_lease",
			"platform must be one of android, ios, windows, linux")
	}
	platform := in.Platform
	key := strings.TrimSpace(in.IdempotencyKey)
	if len(key) > maxIdempotencyKeyLen {
		return model.Lease{}, false, leaseErrorf(ErrInvalid, "invalid_lease",
			"idempotency key must be at most %d characters", maxIdempotencyKeyLen)
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return model.Lease{}, false, leaseErrorf(ErrInvalid, "invalid_lease",
			"idempotency key must not contain whitespace")
	}
	if in.TTLSeconds != 0 && (in.TTLSeconds < MinLeaseTTLSeconds || in.TTLSeconds > MaxLeaseTTLSeconds) {
		return model.Lease{}, false, leaseErrorf(ErrInvalid, "invalid_lease",
			"ttlSeconds must be between %d and %d", MinLeaseTTLSeconds, MaxLeaseTTLSeconds)
	}

	// A replay returns the original lease before any current-state check: the
	// retry that follows a lost response must not be refused because the
	// user's quota or the pool changed in between.
	if key != "" {
		for _, lease := range s.leases {
			if lease.IdempotencyKey != key {
				continue
			}
			if lease.UserID != userID || s.leasePlatformLocked(lease) != platform {
				return model.Lease{}, false, leaseErrorf(ErrConflict, "idempotency_conflict",
					"idempotency key %q was already used for another lease", key)
			}
			s.normalizeLeaseLocked(&lease)
			return lease, true, nil
		}
	}

	userIdx := s.userIndexLocked(userID)
	if userIdx < 0 {
		return model.Lease{}, false, leaseErrorf(ErrNotFound, "user_not_found", "user %q not found", userID)
	}
	user := s.users[userIdx]
	if !user.Enabled {
		return model.Lease{}, false, leaseErrorf(ErrConflict, "user_disabled",
			"user %q is suspended", user.Name)
	}
	// The quota guard reads the derived current-month gate total, not the
	// stored field: PanelUser.UsedBytes is never persisted, so the stored copy
	// is stale by definition (see userUsageLocked).
	if user.QuotaBytesMonth != nil && s.userUsageLocked()[user.ID] >= *user.QuotaBytesMonth {
		return model.Lease{}, false, leaseErrorf(ErrQuota, "quota_exceeded",
			"user %q has exhausted its monthly quota", user.Name)
	}
	for _, lease := range s.leases {
		if lease.UserID == userID && s.leasePlatformLocked(lease) == platform {
			return model.Lease{}, false, leaseErrorf(ErrConflict, "lease_conflict",
				"user %q already holds an active %s lease", user.Name, platform)
		}
	}

	accountIdx, nodeIdx, err := s.pickLeaseTargetLocked(in.AccountID, platform, now)
	if err != nil {
		return model.Lease{}, false, err
	}
	account := s.accounts[accountIdx]
	node := s.nodes[nodeIdx]
	// The slot generation is read from the slot row, never from the node (a
	// node has no generation). A legacy node (Slot 0) stamps zero.
	generation := s.slotGenerationLocked(node)

	ttl := in.TTLSeconds
	if ttl == 0 {
		ttl = defaultLeaseTTLSeconds(user, s.settings)
	}

	lease := model.Lease{
		ID:             s.nextLeaseIDLocked(),
		UserID:         user.ID,
		UserName:       user.Name,
		AccountID:      account.ID,
		AccountLabel:   account.Label,
		Platform:       platform,
		NodeID:         node.ID,
		NodeLabel:      node.Label,
		Region:         node.Region,
		EgressIP:       node.EgressIP,
		Protocol:       node.Protocol,
		GatePort:       node.GatePort,
		TTLSeconds:     ttl,
		ExpiresAt:      iso(now.Add(time.Duration(ttl) * time.Second)),
		Sticky:         true,
		Status:         model.LeaseActive,
		IdempotencyKey: key,
		CreatedAt:      iso(now),
		Slot:           node.Slot,
		Generation:     generation,
	}
	s.leases = append(s.leases, lease)
	s.nodes[nodeIdx].InUse++
	s.addAuditLocked(model.SeverityInfo, "operator", "lease.create", user.Name,
		fmt.Sprintf("%s · %s · node %s · ttl %s", platform, account.Label, node.Label,
			(time.Duration(ttl)*time.Second).String()),
		map[string]string{
			"leaseId":    lease.ID,
			"accountId":  account.ID,
			"nodeId":     node.ID,
			"platform":   string(platform),
			"slot":       strconv.Itoa(lease.Slot),
			"generation": strconv.FormatUint(lease.Generation, 10),
		})
	return lease, false, nil
}

// RevokeLease removes a lease and returns its slot to the node. The returned
// row carries Status=revoked; only active rows are stored, so the row is gone
// from later reads.
func (s *Store) RevokeLease(id string) (model.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sweepExpiredLocked(s.now())
	idx := s.leaseIndexLocked(id)
	if idx < 0 {
		return model.Lease{}, notFoundf("lease %q not found", id)
	}
	lease := s.leases[idx]
	lease.Status = model.LeaseRevoked
	s.normalizeLeaseLocked(&lease)
	if nodeIdx := s.nodeIndexLocked(lease.NodeID); nodeIdx >= 0 && s.nodes[nodeIdx].InUse > 0 {
		s.nodes[nodeIdx].InUse--
	}
	s.leases = append(s.leases[:idx], s.leases[idx+1:]...)
	s.addAuditLocked(model.SeverityInfo, "operator", "lease.revoke", lease.UserName,
		fmt.Sprintf("Lease on %s revoked", lease.NodeLabel),
		map[string]string{
			"leaseId":    lease.ID,
			"accountId":  lease.AccountID,
			"nodeId":     lease.NodeID,
			"slot":       strconv.Itoa(lease.Slot),
			"generation": strconv.FormatUint(lease.Generation, 10),
		})
	return lease, nil
}

// ReleaseLease is the historical name for RevokeLease. The panel and the
// rotation paths still call it; both mean "free the node now".
func (s *Store) ReleaseLease(id string) error {
	_, err := s.RevokeLease(id)
	return err
}

// NodeLeases returns the active leases that point at nodeID, in store order.
// The cleanup and audit paths read it so a node that still carries a customer
// entitlement is never deleted or closed silently.
func (s *Store) NodeLeases(nodeID string) []model.Lease {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []model.Lease
	for _, lease := range s.leases {
		if lease.NodeID != nodeID {
			continue
		}
		s.normalizeLeaseLocked(&lease)
		out = append(out, lease)
	}
	return out
}

// InvalidateNodeLeases revokes every active lease that points at nodeID and
// returns the revoked rows. It is the containment path for a node that can no
// longer serve (a vendor kick): the customer's endpoint must stop being
// advertised now, with an explicit audit row, instead of disappearing silently
// on a later cascade.
//
// The hook installed by SetLeaseExpiryHook is deliberately not called: this is
// a revocation, not an expiry, and the lease.expired event would mislabel it.
func (s *Store) InvalidateNodeLeases(nodeID, reason string) []model.Lease {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	detail := clientMessage(reason)
	if detail == "" {
		detail = "node_unavailable"
	}
	kept := s.leases[:0]
	var revoked []model.Lease
	for _, lease := range s.leases {
		if lease.NodeID != nodeID {
			kept = append(kept, lease)
			continue
		}
		lease.Status = model.LeaseRevoked
		s.normalizeLeaseLocked(&lease)
		revoked = append(revoked, lease)
		s.addAuditLocked(model.SeverityWarn, "system", "lease.revoke", lease.UserName,
			fmt.Sprintf("Lease on %s revoked · %s", lease.NodeLabel, detail),
			map[string]string{
				"leaseId":    lease.ID,
				"accountId":  lease.AccountID,
				"nodeId":     lease.NodeID,
				"slot":       strconv.Itoa(lease.Slot),
				"generation": strconv.FormatUint(lease.Generation, 10),
				"reason":     detail,
			})
	}
	s.leases = kept
	// InUse is derived from the active rows, so it cannot drift after a partial
	// removal: recount the leases that still point at the node.
	inUse := 0
	for _, lease := range s.leases {
		if lease.NodeID == nodeID {
			inUse++
		}
	}
	if idx := s.nodeIndexLocked(nodeID); idx >= 0 {
		s.nodes[idx].InUse = inUse
	}
	return revoked
}

// slotGenerationLocked is the serving generation a lease must stamp: the slot
// row's generation for a slot node, zero for a legacy node. A node has no
// generation field, so it is never inferred from the node.
func (s *Store) slotGenerationLocked(node model.ProxyNode) uint64 {
	if node.Slot <= 0 {
		return 0
	}
	slot, ok := s.accountSlots[model.SlotKey{AccountID: node.AccountID, Index: node.Slot}]
	if !ok {
		return 0
	}
	return slot.Generation
}

// pickLeaseTargetLocked resolves the account and node for a new lease. A
// pinned account is validated strictly; without a pin the first active account
// of the platform with spare concurrency and a free node wins. Account
// concurrency counts active leases, not flows, because a lease is the unit a
// customer is sold.
func (s *Store) pickLeaseTargetLocked(accountID string, platform model.Platform, now time.Time) (int, int, error) {
	pinned := strings.TrimSpace(accountID)
	if pinned != "" {
		idx := s.accountIndexLocked(pinned)
		if idx < 0 {
			return 0, 0, leaseErrorf(ErrNotFound, "account_not_found", "account %q not found", pinned)
		}
		account := s.accounts[idx]
		if account.Platform != platform {
			return 0, 0, leaseErrorf(ErrInvalid, "invalid_lease",
				"account %q is %s, not %s", account.ID, account.Platform, platform)
		}
		if err := leaseAccountAvailable(account, now); err != nil {
			return 0, 0, err
		}
		active := s.accountLeaseCountLocked(account.ID)
		if active >= account.Concurrency {
			return 0, 0, leaseErrorf(ErrQuota, "concurrency_exceeded",
				"account %q reached its concurrency of %d active leases",
				account.Label, account.Concurrency)
		}
		if capacity := s.accountCapacityLocked(account, now); capacity > 0 && active >= capacity {
			return 0, 0, leaseErrorf(ErrQuota, "account_slots_exhausted",
				"account %q has no free lease slot (admission slots %d)",
				account.Label, capacity)
		}
		nodeIdx := s.pickLeaseNodeLocked(account.ID)
		if nodeIdx < 0 {
			return 0, 0, leaseErrorf(ErrQuota, "no_capacity",
				"account %q has no free node", account.Label)
		}
		return idx, nodeIdx, nil
	}

	blockedByConcurrency := false
	blockedBySlots := false
	blockedByNode := false
	for idx, account := range s.accounts {
		if account.Platform != platform {
			continue
		}
		if leaseAccountAvailable(account, now) != nil {
			continue
		}
		active := s.accountLeaseCountLocked(account.ID)
		if active >= account.Concurrency {
			blockedByConcurrency = true
			continue
		}
		if capacity := s.accountCapacityLocked(account, now); capacity > 0 && active >= capacity {
			blockedBySlots = true
			continue
		}
		if nodeIdx := s.pickLeaseNodeLocked(account.ID); nodeIdx >= 0 {
			return idx, nodeIdx, nil
		}
		blockedByNode = true
	}
	if blockedBySlots && !blockedByNode {
		return 0, 0, leaseErrorf(ErrQuota, "account_slots_exhausted",
			"every active %s account has reached its lease slot cap", platform)
	}
	if blockedByConcurrency && !blockedByNode {
		return 0, 0, leaseErrorf(ErrQuota, "concurrency_exceeded",
			"every active %s account has reached its lease concurrency", platform)
	}
	return 0, 0, leaseErrorf(ErrQuota, "no_capacity",
		"no active %s account has a free node", platform)
}

// leaseAccountAvailable reports whether an account may receive a new lease.
func leaseAccountAvailable(account model.Account, now time.Time) error {
	if account.State != model.AccountActive {
		return leaseErrorf(ErrConflict, "account_unavailable",
			"account %q is %s", account.Label, account.State)
	}
	if expires, ok := leaseExpiry(account.ExpiresAt); ok && !expires.After(now) {
		return leaseErrorf(ErrConflict, "account_unavailable",
			"account %q expired at %s", account.Label, account.ExpiresAt)
	}
	return nil
}

// pickLeaseNodeLocked returns the index of the first free LIVE node of an
// account in seed order. READY, STARTING, DRAINING, DOWN and STALE rows carry
// no bound listener, so they are never a lease target: a lease advertises the
// gate port it copies from the node, and a READY row's remembered port is a
// dead endpoint. A slot node is leasable only while its slot row is live and
// its listener is bound, and a slot can back at most one lease: a duplicate
// node row for a slot that already serves a lease must not widen admission.
// A legacy node (Slot 0) follows the same bound-listener rule.
func (s *Store) pickLeaseNodeLocked(accountID string) int {
	used := make(map[string]struct{}, len(s.leases))
	// A slot is owned by (account, index), never by the index alone: two
	// accounts may each serve their own slot 1, and a lease on A#1 must not
	// consume B#1's capacity.
	usedSlots := make(map[model.SlotKey]struct{}, len(s.leases))
	for _, lease := range s.leases {
		used[lease.NodeID] = struct{}{}
		if lease.Slot > 0 {
			usedSlots[model.SlotKey{AccountID: lease.AccountID, Index: lease.Slot}] = struct{}{}
		}
	}
	best := -1
	for idx, node := range s.nodes {
		if node.AccountID != accountID {
			continue
		}
		if _, taken := used[node.ID]; taken {
			continue
		}
		// Only a LIVE row has a bound listener. Slot 0 (legacy) is no
		// exception: refusing only the slot shape would leave the same dead
		// READY port leasable on a pre-slot installation.
		if node.State != model.NodeLive || node.GatePort <= 0 {
			continue
		}
		// A slot node is leasable only while its slot row is live, has not
		// been marked by a vendor kick, and reports the listener the row
		// reserved.
		if node.Slot > 0 {
			key := model.SlotKey{AccountID: node.AccountID, Index: node.Slot}
			if _, taken := usedSlots[key]; taken {
				continue
			}
			slot, ok := s.accountSlots[key]
			if !ok || slot.State != model.SlotLive || slot.Health == model.SlotHealthVendorKick {
				continue
			}
			if slot.GatePort != node.GatePort {
				continue
			}
		}
		best = idx
		break
	}
	return best
}

// accountLeaseCountLocked counts the active leases of one account.
func (s *Store) accountLeaseCountLocked(accountID string) int {
	count := 0
	for _, lease := range s.leases {
		if lease.AccountID == accountID {
			count++
		}
	}
	return count
}

// nextLeaseIDLocked returns the next "lease-NN" id. Ids are not persisted
// counters; the highest existing suffix plus one is unique because allocation
// runs under the store lock and revoked rows leave the slice.
func (s *Store) nextLeaseIDLocked() string {
	max := 0
	for _, lease := range s.leases {
		raw, ok := strings.CutPrefix(lease.ID, "lease-")
		if !ok {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err == nil && value > max {
			max = value
		}
	}
	return fmt.Sprintf("lease-%02d", max+1)
}

// defaultLeaseTTLSeconds prefers the user's sticky TTL, then the panel lease
// TTL, then eight hours. A configured value outside the lease bounds is
// clamped so a valid create never fails on a settings default.
func defaultLeaseTTLSeconds(user model.PanelUser, settings model.PanelSettings) int {
	for _, candidate := range []string{user.StickyTTL, settings.LeaseTTL} {
		seconds, ok := ttlSeconds(candidate)
		if !ok {
			continue
		}
		if seconds < MinLeaseTTLSeconds {
			return MinLeaseTTLSeconds
		}
		if seconds > MaxLeaseTTLSeconds {
			return MaxLeaseTTLSeconds
		}
		return seconds
	}
	return DefaultLeaseTTLSeconds
}

// ttlSeconds parses the panel's duration vocabulary ("8h", "24h", "7d").
func ttlSeconds(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		count, err := strconv.Atoi(days)
		if err != nil || count <= 0 {
			return 0, false
		}
		return count * 24 * 3600, true
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, false
	}
	return int(duration / time.Second), true
}

// leaseExpiry parses a lease/account timestamp. An empty value means "no
// expiry"; an unparsable one is also treated as unset rather than silently
// expiring a row on a corrupt snapshot.
func leaseExpiry(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// sweepExpiredLocked releases every lease whose ExpiresAt is at or before now.
// It writes one lease.expired audit row per lease, calls the expiry hook for
// each released row and returns the count. A released row leaves s.leases, so
// a later sweep cannot see it again: the hook fires at most once per lease.
func (s *Store) sweepExpiredLocked(now time.Time) int {
	if len(s.leases) == 0 {
		return 0
	}
	kept := s.leases[:0]
	closed := 0
	for _, lease := range s.leases {
		expires, ok := leaseExpiry(lease.ExpiresAt)
		if !ok || expires.After(now) {
			kept = append(kept, lease)
			continue
		}
		lease.Status = model.LeaseExpired
		if nodeIdx := s.nodeIndexLocked(lease.NodeID); nodeIdx >= 0 && s.nodes[nodeIdx].InUse > 0 {
			s.nodes[nodeIdx].InUse--
		}
		closed++
		s.addAuditLocked(model.SeverityInfo, "system", "lease.expired", lease.UserName,
			fmt.Sprintf("Lease on %s expired", lease.NodeLabel),
			map[string]string{"leaseId": lease.ID, "nodeId": lease.NodeID})
		// Normalize before the hook so a seed row that predates Platform
		// still reports the account's platform on the event.
		s.normalizeLeaseLocked(&lease)
		if s.leaseExpiryHook != nil {
			s.leaseExpiryHook(lease)
		}
	}
	s.leases = kept
	return closed
}

// SetLeaseExpiryHook installs the sink for leases the expiry sweep releases.
// It is meant to be called once during startup, before requests are served.
// The hook runs with the store lock held and must not block or call back into
// the Store; the webhook sink only enqueues. Passing nil removes the hook.
func (s *Store) SetLeaseExpiryHook(hook func(model.Lease)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaseExpiryHook = hook
}

// normalizeLeaseLocked fills the fields an older snapshot or seed row predates,
// so every response carries the full lease contract.
func (s *Store) normalizeLeaseLocked(lease *model.Lease) {
	if lease.Status == "" {
		lease.Status = model.LeaseActive
	}
	if lease.Platform == "" {
		lease.Platform = s.leasePlatformLocked(*lease)
	}
}

// leasePlatformLocked returns the platform a lease was allocated for. Seed
// rows predate the field, so it falls back to the account's platform.
func (s *Store) leasePlatformLocked(lease model.Lease) model.Platform {
	if lease.Platform != "" {
		return lease.Platform
	}
	if idx := s.accountIndexLocked(lease.AccountID); idx >= 0 {
		return s.accounts[idx].Platform
	}
	return ""
}
