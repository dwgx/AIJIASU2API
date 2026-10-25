package runtime

import (
	"fmt"
	"sync"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

const (
	// defaultQuotaCacheTTL bounds how stale a cached monthly total can be.
	// The meter invalidates the entry as soon as it appends a new row, so the
	// TTL only covers rows written by another path (a restore, for example).
	defaultQuotaCacheTTL = 30 * time.Second
	// defaultQuotaAuditInterval throttles quota.exceeded audit rows to one
	// per account per hour. A busy gate would otherwise fill the audit ring.
	defaultQuotaAuditInterval = time.Hour
)

// Quota is the gate's per-account monthly traffic budget admission policy.
// It reads the budget from the store on every check (so an operator patch is
// effective immediately) and caches only the month's durable gate-metered
// total.
//
// The meter advances a populated cache entry after every relayed chunk and
// invalidates it after a completed row written by another path. A cache entry
// also carries its UTC month key, so a daemon that crosses midnight into a new
// month recomputes instead of refusing flows with the previous month's total.
type Quota struct {
	store         *store.Store
	now           func() time.Time
	cacheTTL      time.Duration
	auditInterval time.Duration

	mu      sync.Mutex
	usage   map[string]quotaUsage
	audited map[string]time.Time
}

// quotaUsage is one cached monthly total for one account.
type quotaUsage struct {
	month   string
	bytes   int64
	expires time.Time
}

// NewQuota builds the admission policy over st. A nil store makes every check
// permissive, which keeps a test double without a store working.
func NewQuota(st *store.Store) *Quota {
	return newQuota(st, time.Now, defaultQuotaCacheTTL, defaultQuotaAuditInterval)
}

func newQuota(st *store.Store, now func() time.Time, cacheTTL, auditInterval time.Duration) *Quota {
	if now == nil {
		now = time.Now
	}
	if cacheTTL <= 0 {
		cacheTTL = defaultQuotaCacheTTL
	}
	if auditInterval <= 0 {
		auditInterval = defaultQuotaAuditInterval
	}
	return &Quota{
		store:         st,
		now:           now,
		cacheTTL:      cacheTTL,
		auditInterval: auditInterval,
		usage:         make(map[string]quotaUsage),
		audited:       make(map[string]time.Time),
	}
}

// Check reports whether accountID may open another flow. A nil budget means
// unlimited and returns ok with zero used/budget. A configured budget refuses
// once the month's gate-metered bytes reach it, so a zero budget refuses every
// flow. used and budget are returned for the gate's rejection log only.
//
// Check is called from the gate's flow goroutine: it takes two short store
// reads on a cache miss and never blocks on IO.
func (q *Quota) Check(accountID string) (ok bool, used, budget int64) {
	if q == nil || q.store == nil {
		return true, 0, 0
	}
	account, found := q.store.Account(accountID)
	if !found || account.TrafficBudgetBytes == nil {
		return true, 0, 0
	}
	budget = *account.TrafficBudgetBytes
	used = q.monthlyUsage(accountID)
	if used < budget {
		return true, used, budget
	}
	q.auditExceeded(account, used, budget)
	return false, used, budget
}

// Invalidate drops the cached monthly total for one account. The flow meter
// calls it after appending a row, so the next admission sees the new bytes.
func (q *Quota) Invalidate(accountID string) {
	if q == nil {
		return
	}
	q.mu.Lock()
	delete(q.usage, accountID)
	q.mu.Unlock()
}

// advance moves a populated cache entry forward by one relayed chunk. The store
// already advanced the durable counter; this only keeps the cached total in
// step so the live budget check does not need a store read. An absent or
// previous-month entry is left alone: Check recomputes it from the store.
func (q *Quota) advance(accountID string, n int64) {
	if q == nil || accountID == "" || n <= 0 {
		return
	}
	now := q.now()
	month := now.UTC().Format("2006-01")
	q.mu.Lock()
	if entry, ok := q.usage[accountID]; ok && entry.month == month {
		entry.bytes += n
		entry.expires = now.Add(q.cacheTTL)
		q.usage[accountID] = entry
	}
	q.mu.Unlock()
}

// monthlyUsage returns the cached total for the current UTC month, computing
// it from the store on a miss or after the month changed.
func (q *Quota) monthlyUsage(accountID string) int64 {
	now := q.now()
	month := now.UTC().Format("2006-01")

	q.mu.Lock()
	if entry, ok := q.usage[accountID]; ok && entry.month == month && now.Before(entry.expires) {
		q.mu.Unlock()
		return entry.bytes
	}
	q.mu.Unlock()

	used := q.store.MonthlyUsage(accountID)

	q.mu.Lock()
	q.usage[accountID] = quotaUsage{
		month:   month,
		bytes:   used,
		expires: now.Add(q.cacheTTL),
	}
	q.mu.Unlock()
	return used
}

// auditExceeded writes one quota.exceeded audit row per account per interval.
// The throttle stamp is taken before the store write, so concurrent refusals
// cannot double-audit. The detail carries byte counts only.
func (q *Quota) auditExceeded(account model.Account, used, budget int64) {
	now := q.now()
	q.mu.Lock()
	last, seen := q.audited[account.ID]
	if seen && now.Sub(last) < q.auditInterval {
		q.mu.Unlock()
		return
	}
	q.audited[account.ID] = now
	q.mu.Unlock()

	q.store.RecordAudit(model.SeverityWarn, "gate", "quota.exceeded", account.Label,
		fmt.Sprintf("%d of %d bytes used this month", used, budget))
}
