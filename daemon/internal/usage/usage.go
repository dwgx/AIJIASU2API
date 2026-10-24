// Package usage aggregates request logs into customer-facing usage stats.
//
// It is a pure policy layer: it performs no IO, keeps no mutable package
// state, and treats its inputs as immutable. The caller supplies the window,
// the optional account/user filter and the quota table; the aggregator returns
// a deterministic Summary. All day boundaries are UTC calendar days.
package usage

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"ajsaccountd/internal/model"
)

const (
	// MaxWindowDays is the widest window the aggregator accepts, counted as
	// inclusive UTC calendar days. It bounds both the returned day series and
	// the per-account bucket series.
	MaxWindowDays = 366
	// MaxRows is the number of in-window rows one call will aggregate. Rows
	// outside the window do not count. The Summary reports truncation instead
	// of silently dropping data.
	MaxRows = 200_000
	// DefaultWindowDays is the window used when the caller leaves From and To
	// at their zero values.
	DefaultWindowDays = 30
	// TopTargetsLimit is the number of host labels in Summary.TopTargets.
	TopTargetsLimit = 10
	// UnknownTarget groups rows whose target has no usable host.
	UnknownTarget = "unknown"
	// DefaultRetentionRows is the request-log cap reported when Config leaves
	// RetentionRows at zero. It mirrors store.RequestRetention; the daemon
	// passes the store's constant explicitly so the two cannot drift.
	DefaultRetentionRows = 10_000
)

// Errors returned by Aggregate. Callers use errors.Is.
var (
	ErrInvalidWindow = errors.New("usage: invalid window")
	ErrWindowTooLong = errors.New("usage: window exceeds the maximum span")
)

// Config configures an Aggregator. The zero value is valid and selects the
// package defaults.
type Config struct {
	// Now supplies the current time when Query leaves From or To zero. Nil
	// uses time.Now.
	Now func() time.Time
	// MaxRows overrides MaxRows when positive.
	MaxRows int
	// MaxWindowDays overrides MaxWindowDays when positive.
	MaxWindowDays int
	// RetentionRows is the number of request rows the store retains. The
	// summary echoes it as rowsRetained so a client can label bounded
	// history. Zero selects DefaultRetentionRows.
	RetentionRows int
}

// Aggregator turns request logs into usage summaries. It is immutable after
// New and safe for concurrent use.
type Aggregator struct {
	now           func() time.Time
	maxRows       int
	maxWindowDays int
	retentionRows int
}

// New builds an Aggregator. The zero Config is valid.
func New(cfg Config) *Aggregator {
	agg := &Aggregator{
		now:           cfg.Now,
		maxRows:       cfg.MaxRows,
		maxWindowDays: cfg.MaxWindowDays,
		retentionRows: cfg.RetentionRows,
	}
	if agg.now == nil {
		agg.now = time.Now
	}
	if agg.maxRows <= 0 {
		agg.maxRows = MaxRows
	}
	if agg.maxWindowDays <= 0 {
		agg.maxWindowDays = MaxWindowDays
	}
	if agg.retentionRows <= 0 {
		agg.retentionRows = DefaultRetentionRows
	}
	return agg
}

// Query selects the slice of logs to aggregate.
type Query struct {
	// From and To bound the window inclusively. Zero values default to a
	// trailing DefaultWindowDays window ending at the aggregator clock.
	From time.Time
	To   time.Time
	// Account and UserID are optional exact-match filters. Empty means all.
	Account string
	UserID  string
	// Quotas maps user id to the monthly byte quota. A nil value means "no
	// quota"; a missing key also means "no quota". The pointer is copied into
	// UserUsage unchanged.
	Quotas map[string]*int64
}

// Bucket is one UTC day of usage.
type Bucket struct {
	Day          string `json:"day"`
	Requests     int    `json:"requests"`
	Bytes        int64  `json:"bytes"`
	Errors       int    `json:"errors"`
	AvgLatencyMs int    `json:"avgLatencyMs"`
}

// AccountUsage is the per-account roll-up. Buckets always covers every day in
// the window so a chart can render a continuous series.
type AccountUsage struct {
	Account      string   `json:"account"`
	Requests     int      `json:"requests"`
	Bytes        int64    `json:"bytes"`
	Errors       int      `json:"errors"`
	AvgLatencyMs int      `json:"avgLatencyMs"`
	Buckets      []Bucket `json:"buckets"`
}

// UserUsage is the per-user roll-up. UsedBytes is the byte total inside the
// queried window; QuotaBytesMonth is echoed from Query.Quotas.
type UserUsage struct {
	UserID          string `json:"userId"`
	UserName        string `json:"userName"`
	Requests        int    `json:"requests"`
	Bytes           int64  `json:"bytes"`
	QuotaBytesMonth *int64 `json:"quotaBytesMonth"`
	UsedBytes       int64  `json:"usedBytes"`
}

// TargetUsage is one host-only target label. It never contains a scheme, path,
// query string or port.
type TargetUsage struct {
	Target   string `json:"target"`
	Requests int    `json:"requests"`
	Bytes    int64  `json:"bytes"`
}

// Summary is the aggregate returned to the API layer.
type Summary struct {
	From       string         `json:"from"`
	To         string         `json:"to"`
	Requests   int            `json:"requests"`
	Bytes      int64          `json:"bytes"`
	Errors     int            `json:"errors"`
	Accounts   []AccountUsage `json:"accounts"`
	Users      []UserUsage    `json:"users"`
	Days       []Bucket       `json:"days"`
	TopTargets []TargetUsage  `json:"topTargets"`
	// Truncated reports that the input held more in-window rows than the
	// configured cap, so the summary covers only the first MaxRows of them.
	Truncated bool `json:"truncated"`
	// RowsRetained is the request-log cap the store enforces. A client uses
	// it with RetentionDays to label how much history actually exists; the
	// window may be wider than the retained rows.
	RowsRetained int `json:"rowsRetained"`
	// RetentionDays is the number of UTC calendar days the retained rows
	// cover, counted inclusively from the oldest parseable row through the
	// aggregator clock. Zero means the store holds no request rows. It is a
	// property of the store, so the query window and filters do not narrow it.
	RetentionDays int `json:"retentionDays"`
}

// totals is the mutable accumulator shared by the summary and one group.
type totals struct {
	requests int
	bytes    int64
	errors   int
	latency  int64
}

func (t *totals) add(row model.RequestLog) {
	t.requests++
	t.bytes += row.Bytes
	if row.Status >= 400 {
		t.errors++
	}
	t.latency += int64(row.LatencyMs)
}

func (t *totals) avgLatency() int {
	if t.requests == 0 {
		return 0
	}
	// Round half up with integer arithmetic; latencies are non-negative.
	return int((t.latency + int64(t.requests)/2) / int64(t.requests))
}

// accountAccum keeps insertion-independent state for one account label.
type accountAccum struct {
	totals
	days map[string]*totals
}

// userAccum keeps the first non-empty display name seen for a user id.
type userAccum struct {
	totals
	name string
}

// Aggregate folds logs into a Summary. Rows with an unparseable timestamp or
// outside the window are ignored. The result is deterministic: slices are
// sorted by bytes descending then name ascending, and the day series is
// chronological and zero-filled.
func (a *Aggregator) Aggregate(logs []model.RequestLog, q Query) (Summary, error) {
	now := a.now
	if now == nil {
		now = time.Now
	}
	current := now()
	from, to, days, err := a.window(q, current)
	if err != nil {
		return Summary{}, err
	}
	retentionRows := a.retentionRows
	if retentionRows <= 0 {
		retentionRows = DefaultRetentionRows
	}

	summary := Summary{
		From:          from.UTC().Format(time.RFC3339),
		To:            to.UTC().Format(time.RFC3339),
		RowsRetained:  retentionRows,
		RetentionDays: retentionDays(logs, current),
	}
	dayIndex := make(map[string]int, len(days))
	dayLatency := make([]int64, len(days))
	summary.Days = make([]Bucket, len(days))
	for i, day := range days {
		key := day.Format(dayLayout)
		dayIndex[key] = i
		summary.Days[i].Day = key
	}

	accounts := make(map[string]*accountAccum)
	users := make(map[string]*userAccum)
	targets := make(map[string]*TargetUsage)
	var total totals

	processed := 0
	maxRows := a.maxRows
	if maxRows <= 0 {
		maxRows = MaxRows
	}
	for _, row := range logs {
		if q.Account != "" && row.AccountLabel != q.Account {
			continue
		}
		if q.UserID != "" && row.UserID != q.UserID {
			continue
		}
		ts, err := time.Parse(time.RFC3339, row.TS)
		if err != nil {
			continue
		}
		if ts.Before(from) || ts.After(to) {
			continue
		}
		if processed == maxRows {
			summary.Truncated = true
			break
		}
		processed++

		key := ts.UTC().Format(dayLayout)
		idx, ok := dayIndex[key]
		if !ok {
			continue
		}
		day := &summary.Days[idx]

		total.add(row)
		day.Requests++
		day.Bytes += row.Bytes
		dayLatency[idx] += int64(row.LatencyMs)
		if row.Status >= 400 {
			day.Errors++
		}

		acc := accounts[row.AccountLabel]
		if acc == nil {
			acc = &accountAccum{days: make(map[string]*totals, len(days))}
			accounts[row.AccountLabel] = acc
		}
		acc.add(row)
		bucket := acc.days[key]
		if bucket == nil {
			bucket = &totals{}
			acc.days[key] = bucket
		}
		bucket.add(row)

		user := users[row.UserID]
		if user == nil {
			user = &userAccum{}
			users[row.UserID] = user
		}
		if user.name == "" && row.UserName != "" {
			user.name = row.UserName
		}
		user.add(row)

		label := hostLabel(row.Target)
		target := targets[label]
		if target == nil {
			target = &TargetUsage{Target: label}
			targets[label] = target
		}
		target.Requests++
		target.Bytes += row.Bytes
	}

	summary.Requests = total.requests
	summary.Bytes = total.bytes
	summary.Errors = total.errors

	summary.Accounts = make([]AccountUsage, 0, len(accounts))
	for label, acc := range accounts {
		usage := AccountUsage{
			Account:      label,
			Requests:     acc.requests,
			Bytes:        acc.bytes,
			Errors:       acc.errors,
			AvgLatencyMs: acc.avgLatency(),
			Buckets:      make([]Bucket, len(days)),
		}
		for i, day := range days {
			key := day.Format(dayLayout)
			bucket := Bucket{Day: key}
			if got := acc.days[key]; got != nil {
				bucket.Requests = got.requests
				bucket.Bytes = got.bytes
				bucket.Errors = got.errors
				bucket.AvgLatencyMs = got.avgLatency()
			}
			usage.Buckets[i] = bucket
		}
		summary.Accounts = append(summary.Accounts, usage)
	}
	sort.SliceStable(summary.Accounts, func(i, j int) bool {
		left, right := summary.Accounts[i], summary.Accounts[j]
		if left.Bytes != right.Bytes {
			return left.Bytes > right.Bytes
		}
		return left.Account < right.Account
	})

	summary.Users = make([]UserUsage, 0, len(users))
	for id, user := range users {
		summary.Users = append(summary.Users, UserUsage{
			UserID:          id,
			UserName:        user.name,
			Requests:        user.requests,
			Bytes:           user.bytes,
			QuotaBytesMonth: q.Quotas[id],
			UsedBytes:       user.bytes,
		})
	}
	sort.SliceStable(summary.Users, func(i, j int) bool {
		left, right := summary.Users[i], summary.Users[j]
		if left.Bytes != right.Bytes {
			return left.Bytes > right.Bytes
		}
		if left.UserName != right.UserName {
			return left.UserName < right.UserName
		}
		return left.UserID < right.UserID
	})

	summary.TopTargets = make([]TargetUsage, 0, len(targets))
	for _, target := range targets {
		summary.TopTargets = append(summary.TopTargets, *target)
	}
	sort.SliceStable(summary.TopTargets, func(i, j int) bool {
		left, right := summary.TopTargets[i], summary.TopTargets[j]
		if left.Requests != right.Requests {
			return left.Requests > right.Requests
		}
		if left.Bytes != right.Bytes {
			return left.Bytes > right.Bytes
		}
		return left.Target < right.Target
	})
	if len(summary.TopTargets) > TopTargetsLimit {
		summary.TopTargets = summary.TopTargets[:TopTargetsLimit]
	}

	for i := range summary.Days {
		if summary.Days[i].Requests > 0 {
			n := int64(summary.Days[i].Requests)
			summary.Days[i].AvgLatencyMs = int((dayLatency[i] + n/2) / n)
		}
	}
	return summary, nil
}

const dayLayout = "2006-01-02"

// retentionDays counts the UTC calendar days from the oldest parseable row
// through now, inclusively. It reads every retained row, not just the queried
// window: the cap is a store property, so an account-filtered request still
// learns how much history exists. Zero means no row could be parsed.
func retentionDays(logs []model.RequestLog, now time.Time) int {
	var oldest time.Time
	for _, row := range logs {
		ts, err := time.Parse(time.RFC3339, row.TS)
		if err != nil {
			continue
		}
		if oldest.IsZero() || ts.Before(oldest) {
			oldest = ts
		}
	}
	if oldest.IsZero() {
		return 0
	}
	days := int(now.UTC().Truncate(24*time.Hour).Sub(oldest.UTC().Truncate(24*time.Hour))/(24*time.Hour)) + 1
	if days < 1 {
		return 1
	}
	return days
}

// window resolves the query bounds and enumerates the UTC days they cover.
// now is the single aggregator clock reading for this call, so the window and
// the retention span cannot straddle a midnight.
func (a *Aggregator) window(q Query, now time.Time) (time.Time, time.Time, []time.Time, error) {
	maxWindowDays := a.maxWindowDays
	if maxWindowDays <= 0 {
		maxWindowDays = MaxWindowDays
	}
	from, to := q.From, q.To
	if from.IsZero() && to.IsZero() {
		to = now
	}
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.AddDate(0, 0, -DefaultWindowDays)
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("%w: to %s is before from %s", ErrInvalidWindow, to.Format(time.RFC3339), from.Format(time.RFC3339))
	}
	fromDay := from.UTC().Truncate(24 * time.Hour)
	toDay := to.UTC().Truncate(24 * time.Hour)
	count := int(toDay.Sub(fromDay)/(24*time.Hour)) + 1
	if count > maxWindowDays {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("%w: %d days, maximum %d", ErrWindowTooLong, count, maxWindowDays)
	}
	days := make([]time.Time, count)
	for i := range days {
		days[i] = fromDay.AddDate(0, 0, i)
	}
	return from, to, days, nil
}
