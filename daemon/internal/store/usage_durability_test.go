package store

import (
	"encoding/json"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// TestDurableMonthlyUsage proves the quota counters are durable state, not a
// view over the evictable request log: eviction keeps them, a snapshot carries
// them across a restart, a month rollover resets them, and a legacy snapshot
// without counters is rebuilt from its current-month gate rows.
func TestDurableMonthlyUsage(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	st := newEmptyWithClock(clock, nil)
	account := quotaTestAccount(t, st, "Durable Ops")
	user := mustCreateUser(t, st, "durable-bot")

	appendRow := func(row model.RequestLog) {
		t.Helper()
		st.AppendRequest(row)
	}

	// One attributed gate row advances both counters.
	appendRow(model.RequestLog{
		TS: "2026-10-20T10:00:00Z", UserID: user.ID, UserName: user.Name,
		AccountLabel: account.Label, Source: model.SourceGate, Bytes: 400,
	})
	// A loopback row has no user but still consumes the account budget.
	appendRow(model.RequestLog{
		TS: "2026-10-21T10:00:00Z", AccountLabel: account.Label,
		Source: model.SourceGate, Bytes: 100,
	})
	// A previous-month row and a non-gate row never count.
	appendRow(model.RequestLog{
		TS: "2026-09-30T23:59:59Z", UserID: user.ID, AccountLabel: account.Label,
		Source: model.SourceGate, Bytes: 900,
	})
	appendRow(model.RequestLog{
		TS: "2026-10-22T10:00:00Z", UserID: user.ID, AccountLabel: account.Label,
		Source: model.SourceProxy, Bytes: 900,
	})
	if got := st.MonthlyUsage(account.ID); got != 500 {
		t.Fatalf("MonthlyUsage = %d, want 500", got)
	}
	if got := st.UserMonthlyUsage(user.ID); got != 400 {
		t.Fatalf("UserMonthlyUsage = %d, want 400", got)
	}

	// Fill the request log so both gate rows are evicted. Retention is a
	// display bound: the counters must not move.
	for range RequestRetention {
		appendRow(model.RequestLog{TS: "2026-10-23T00:00:00Z", Source: model.SourceMutationLab})
	}
	if got := len(st.Requests()); got != RequestRetention {
		t.Fatalf("request log = %d rows, want %d", got, RequestRetention)
	}
	if got := st.MonthlyUsage(account.ID); got != 500 {
		t.Fatalf("MonthlyUsage after eviction = %d, want 500", got)
	}
	if got := st.UserMonthlyUsage(user.ID); got != 400 {
		t.Fatalf("UserMonthlyUsage after eviction = %d, want 400", got)
	}

	// The counters are persisted in the snapshot and survive a reload.
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.UsageMonth != "2026-10" || snap.MonthlyUsage[user.ID] != 400 || snap.AccountUsage[account.ID] != 500 {
		t.Fatalf("snapshot counters = %q/%v/%v, want 2026-10/400/500",
			snap.UsageMonth, snap.MonthlyUsage, snap.AccountUsage)
	}
	fresh := newEmptyWithClock(clock, nil)
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := fresh.MonthlyUsage(account.ID); got != 500 {
		t.Fatalf("restored MonthlyUsage = %d, want 500", got)
	}
	if got := fresh.UserMonthlyUsage(user.ID); got != 400 {
		t.Fatalf("restored UserMonthlyUsage = %d, want 400", got)
	}

	// A new UTC month starts from zero even though every row is retained.
	now = time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC)
	if got := fresh.MonthlyUsage(account.ID); got != 0 {
		t.Fatalf("MonthlyUsage after rollover = %d, want 0", got)
	}
	if got := fresh.UserMonthlyUsage(user.ID); got != 0 {
		t.Fatalf("UserMonthlyUsage after rollover = %d, want 0", got)
	}

	// A snapshot written by an older build has no counters. Restore rebuilds
	// them from the current-month gate rows, including the loopback row.
	now = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	legacy := newEmptyWithClock(clock, nil)
	legacyAccount := quotaTestAccount(t, legacy, "Legacy Ops")
	legacyUser := mustCreateUser(t, legacy, "legacy-bot")
	legacy.AppendRequest(model.RequestLog{
		TS: "2026-10-20T10:00:00Z", UserID: legacyUser.ID, AccountLabel: legacyAccount.Label,
		Source: model.SourceGate, Bytes: 300,
	})
	legacy.AppendRequest(model.RequestLog{
		TS: "2026-10-21T10:00:00Z", AccountLabel: legacyAccount.Label,
		Source: model.SourceGate, Bytes: 200,
	})
	legacy.AppendRequest(model.RequestLog{
		TS: "2026-09-30T10:00:00Z", UserID: legacyUser.ID, AccountLabel: legacyAccount.Label,
		Source: model.SourceGate, Bytes: 700,
	})
	raw, err := legacy.Snapshot()
	if err != nil {
		t.Fatalf("legacy Snapshot: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode legacy snapshot: %v", err)
	}
	delete(doc, "usageMonth")
	delete(doc, "monthlyUsage")
	delete(doc, "accountUsage")
	stripped, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode stripped snapshot: %v", err)
	}
	migrated := newEmptyWithClock(clock, nil)
	if err := migrated.Restore(stripped); err != nil {
		t.Fatalf("Restore(legacy): %v", err)
	}
	if got := migrated.MonthlyUsage(legacyAccount.ID); got != 500 {
		t.Fatalf("migrated MonthlyUsage = %d, want 500", got)
	}
	if got := migrated.UserMonthlyUsage(legacyUser.ID); got != 300 {
		t.Fatalf("migrated UserMonthlyUsage = %d, want 300", got)
	}
}

// TestQuotaRollover pins the month boundary on the durable counters: a new
// month resets both maps, live-metered rows do not double count after the
// reset, ordinary rows advance the new month, and a snapshot keeps the month it
// was written in.
func TestQuotaRollover(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	st := newEmptyWithClock(clock, nil)
	account := quotaTestAccount(t, st, "Rollover Ops")
	user := mustCreateUser(t, st, "rollover-bot")

	st.AddUsage(user.ID, account.ID, 1_000)
	if got := st.MonthlyUsage(account.ID); got != 1_000 {
		t.Fatalf("October MonthlyUsage = %d, want 1000", got)
	}
	if got := st.UserMonthlyUsage(user.ID); got != 1_000 {
		t.Fatalf("October UserMonthlyUsage = %d, want 1000", got)
	}

	now = time.Date(2026, 11, 1, 0, 0, 5, 0, time.UTC)
	if got := st.MonthlyUsage(account.ID); got != 0 {
		t.Fatalf("November MonthlyUsage = %d, want 0", got)
	}
	if got := st.UserMonthlyUsage(user.ID); got != 0 {
		t.Fatalf("November UserMonthlyUsage = %d, want 0", got)
	}

	st.AddUsage(user.ID, account.ID, 250)
	if got := st.MonthlyUsage(account.ID); got != 250 {
		t.Fatalf("November MonthlyUsage after progress = %d, want 250", got)
	}

	// The completed row for the flow whose bytes were metered live is a log
	// entry only: it must not add its total a second time.
	st.AppendRequest(model.RequestLog{
		UserID: user.ID, UserName: user.Name, AccountLabel: account.Label,
		Source: model.SourceGate, Bytes: 1_000, MeteredLive: true,
	})
	if got := st.MonthlyUsage(account.ID); got != 250 {
		t.Fatalf("MonthlyUsage after live-metered row = %d, want 250", got)
	}

	// A row appended without live metering still advances both counters.
	st.AppendRequest(model.RequestLog{
		TS: "2026-11-01T00:00:06Z", UserID: user.ID, UserName: user.Name,
		AccountLabel: account.Label, Source: model.SourceGate, Bytes: 50,
	})
	if got := st.MonthlyUsage(account.ID); got != 300 {
		t.Fatalf("MonthlyUsage after ordinary row = %d, want 300", got)
	}
	if got := st.UserMonthlyUsage(user.ID); got != 300 {
		t.Fatalf("UserMonthlyUsage after ordinary row = %d, want 300", got)
	}

	// The snapshot keeps the November counters.
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	fresh := newEmptyWithClock(clock, nil)
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := fresh.MonthlyUsage(account.ID); got != 300 {
		t.Fatalf("restored MonthlyUsage = %d, want 300", got)
	}

	// A snapshot restored in a later month resets rather than carrying
	// November's total into December.
	now = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("Restore in December: %v", err)
	}
	if got := fresh.MonthlyUsage(account.ID); got != 0 {
		t.Fatalf("December MonthlyUsage = %d, want 0", got)
	}
}

// TestLongFlowCrossMonth pins the long-flow accounting: bytes are charged to
// the month in which they cross the tunnel, so a flow started in October and
// finished in November does not carry its October bytes into November. The
// completion row is stamped in November and marked live, so it neither adds the
// total again nor hides the current-month counter.
func TestLongFlowCrossMonth(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 59, 50, 0, time.UTC)
	clock := func() time.Time { return now }
	st := newEmptyWithClock(clock, nil)
	account := quotaTestAccount(t, st, "Long Flow Ops")
	user := mustCreateUser(t, st, "long-flow-bot")

	// The first chunks cross before midnight.
	st.AddUsage(user.ID, account.ID, 700)
	if got := st.MonthlyUsage(account.ID); got != 700 {
		t.Fatalf("October MonthlyUsage = %d, want 700", got)
	}

	// The rest cross after midnight and belong to November only.
	now = time.Date(2026, 11, 1, 0, 0, 10, 0, time.UTC)
	st.AddUsage(user.ID, account.ID, 300)
	if got := st.MonthlyUsage(account.ID); got != 300 {
		t.Fatalf("November MonthlyUsage = %d, want 300 (October bytes must not carry over)", got)
	}
	if got := st.UserMonthlyUsage(user.ID); got != 300 {
		t.Fatalf("November UserMonthlyUsage = %d, want 300", got)
	}

	// The flow completes in November. Its row is stamped at completion and
	// reports the full 1000 bytes for the usage log, but the counters already
	// hold the live total.
	st.AppendRequest(model.RequestLog{
		UserID: user.ID, UserName: user.Name, AccountLabel: account.Label,
		Source: model.SourceGate, Bytes: 1_000, MeteredLive: true,
	})
	if got := st.MonthlyUsage(account.ID); got != 300 {
		t.Fatalf("MonthlyUsage after completion = %d, want 300", got)
	}
	rows := st.Requests()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	ts, err := time.Parse(time.RFC3339, rows[0].TS)
	if err != nil {
		t.Fatalf("row timestamp %q: %v", rows[0].TS, err)
	}
	if ts.UTC().Month() != time.November {
		t.Fatalf("completion row month = %v, want November", ts.UTC().Month())
	}

	// A restart in November keeps the November counter.
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	fresh := newEmptyWithClock(clock, nil)
	if err := fresh.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := fresh.MonthlyUsage(account.ID); got != 300 {
		t.Fatalf("restored MonthlyUsage = %d, want 300", got)
	}
	if got := fresh.UserMonthlyUsage(user.ID); got != 300 {
		t.Fatalf("restored UserMonthlyUsage = %d, want 300", got)
	}
}
