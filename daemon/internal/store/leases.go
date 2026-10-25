package store

import (
	"fmt"
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
//	no_capacity           no active account of the platform has a free node
//
// One lease per node is enforced by excluding every node that already has an
// active lease. LIVE nodes are preferred over READY ones because a LIVE node
// has a bound gate; READY is the seeded/demo state that still carries the
// node's recorded egress and port.
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
	}
	s.leases = append(s.leases, lease)
	s.nodes[nodeIdx].InUse++
	s.addAuditLocked(model.SeverityInfo, "operator", "lease.create", user.Name,
		fmt.Sprintf("%s · %s · node %s · ttl %s", platform, account.Label, node.Label,
			(time.Duration(ttl)*time.Second).String()),
		map[string]string{
			"leaseId":   lease.ID,
			"accountId": account.ID,
			"nodeId":    node.ID,
			"platform":  string(platform),
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
		map[string]string{"leaseId": lease.ID, "nodeId": lease.NodeID})
	return lease, nil
}

// ReleaseLease is the historical name for RevokeLease. The panel and the
// rotation paths still call it; both mean "free the node now".
func (s *Store) ReleaseLease(id string) error {
	_, err := s.RevokeLease(id)
	return err
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
		if s.accountLeaseCountLocked(account.ID) >= account.Concurrency {
			return 0, 0, leaseErrorf(ErrQuota, "concurrency_exceeded",
				"account %q reached its concurrency of %d active leases",
				account.Label, account.Concurrency)
		}
		nodeIdx := s.pickLeaseNodeLocked(account.ID)
		if nodeIdx < 0 {
			return 0, 0, leaseErrorf(ErrQuota, "no_capacity",
				"account %q has no free node", account.Label)
		}
		return idx, nodeIdx, nil
	}

	blockedByConcurrency := false
	blockedByNode := false
	for idx, account := range s.accounts {
		if account.Platform != platform {
			continue
		}
		if leaseAccountAvailable(account, now) != nil {
			continue
		}
		if s.accountLeaseCountLocked(account.ID) >= account.Concurrency {
			blockedByConcurrency = true
			continue
		}
		if nodeIdx := s.pickLeaseNodeLocked(account.ID); nodeIdx >= 0 {
			return idx, nodeIdx, nil
		}
		blockedByNode = true
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

// pickLeaseNodeLocked returns the index of the best free node of an account:
// no active lease, state LIVE or READY, LIVE first and seed order otherwise.
func (s *Store) pickLeaseNodeLocked(accountID string) int {
	used := make(map[string]struct{}, len(s.leases))
	for _, lease := range s.leases {
		used[lease.NodeID] = struct{}{}
	}
	best, bestRank := -1, 3
	for idx, node := range s.nodes {
		if node.AccountID != accountID {
			continue
		}
		if _, taken := used[node.ID]; taken {
			continue
		}
		rank := 3
		switch node.State {
		case model.NodeLive:
			rank = 0
		case model.NodeReady:
			rank = 1
		default:
			continue
		}
		if rank < bestRank {
			best, bestRank = idx, rank
		}
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
// It writes one lease.expired audit row per lease and returns the count.
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
	}
	s.leases = kept
	return closed
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
