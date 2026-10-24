package store

import (
	"errors"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// quotaTestAccount creates one account in an empty store. The store has no
// sealer, so CreateAccount validates and discards the password.
func quotaTestAccount(t *testing.T, st *Store, label string) model.Account {
	t.Helper()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       label,
		Username:    "user-" + label,
		Password:    "quota-test-password",
		Platform:    model.PlatformLinux,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("CreateAccount(%q): %v", label, err)
	}
	return account
}

// TestMonthlyUsageSumsGateRowsForCurrentMonth pins the quota helper: only
// gate-source rows of the current UTC month for the requested account count,
// the scan stops at the month boundary, and a clock rollover resets the total.
func TestMonthlyUsageSumsGateRowsForCurrentMonth(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	account := quotaTestAccount(t, st, "Northwind Ops")
	other := quotaTestAccount(t, st, "Field Lab")

	// AppendRequest prepends, so append oldest first to keep the log
	// newest-first exactly as production does.
	rows := []model.RequestLog{
		{TS: "2026-09-30T23:59:59Z", AccountLabel: account.Label, Source: model.SourceGate, Bytes: 999},
		{TS: "2026-10-02T10:00:00Z", AccountLabel: account.Label, Source: model.SourceGate, Bytes: 50},
		{TS: "2026-10-03T10:00:00Z", AccountLabel: account.Label, Source: model.SourceProxy, Bytes: 900},
		{TS: "2026-10-03T11:00:00Z", AccountLabel: other.Label, Source: model.SourceGate, Bytes: 800},
		{TS: "2026-10-14T10:00:00Z", AccountLabel: account.Label, Source: model.SourceGate, Bytes: 100},
		{TS: "not-a-timestamp", AccountLabel: account.Label, Source: model.SourceGate, Bytes: 7},
	}
	for _, row := range rows {
		st.AppendRequest(row)
	}

	if got := st.MonthlyUsage(account.ID); got != 150 {
		t.Fatalf("MonthlyUsage = %d, want 150 (gate rows for October only)", got)
	}
	if got := st.MonthlyUsage(other.ID); got != 800 {
		t.Fatalf("MonthlyUsage(other) = %d, want 800", got)
	}
	if got := st.MonthlyUsage("acc-unknown"); got != 0 {
		t.Fatalf("MonthlyUsage(unknown) = %d, want 0", got)
	}

	// A new UTC month starts from zero even though October's rows remain.
	now = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if got := st.MonthlyUsage(account.ID); got != 0 {
		t.Fatalf("MonthlyUsage after month rollover = %d, want 0", got)
	}
}

// TestUpdateAccountTrafficBudget covers validation, clearing and the snapshot
// round trip: nil is unlimited, zero is a real budget, and a negative value is
// refused before any mutation.
func TestUpdateAccountTrafficBudget(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	account := quotaTestAccount(t, st, "Northwind Ops")

	budget := int64(1_000)
	updated, err := st.UpdateAccount(account.ID, model.AccountPatch{
		TrafficBudgetSet:   true,
		TrafficBudgetBytes: &budget,
	})
	if err != nil {
		t.Fatalf("UpdateAccount(budget): %v", err)
	}
	if updated.TrafficBudgetBytes == nil || *updated.TrafficBudgetBytes != 1_000 {
		t.Fatalf("budget = %v, want 1000", updated.TrafficBudgetBytes)
	}

	negative := int64(-1)
	if _, err := st.UpdateAccount(account.ID, model.AccountPatch{
		TrafficBudgetSet:   true,
		TrafficBudgetBytes: &negative,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative budget error = %v, want ErrInvalid", err)
	}
	// A supplied pointer is validated even when the caller omits the Set flag.
	if _, err := st.UpdateAccount(account.ID, model.AccountPatch{
		TrafficBudgetBytes: &negative,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("implicit negative budget error = %v, want ErrInvalid", err)
	}
	stored, _ := st.Account(account.ID)
	if stored.TrafficBudgetBytes == nil || *stored.TrafficBudgetBytes != 1_000 {
		t.Fatalf("budget after refused patch = %v, want 1000", stored.TrafficBudgetBytes)
	}

	zero := int64(0)
	if _, err := st.UpdateAccount(account.ID, model.AccountPatch{
		TrafficBudgetSet:   true,
		TrafficBudgetBytes: &zero,
	}); err != nil {
		t.Fatalf("UpdateAccount(zero): %v", err)
	}

	if _, err := st.UpdateAccount(account.ID, model.AccountPatch{
		TrafficBudgetSet:   true,
		TrafficBudgetBytes: nil,
	}); err != nil {
		t.Fatalf("UpdateAccount(clear): %v", err)
	}
	stored, _ = st.Account(account.ID)
	if stored.TrafficBudgetBytes != nil {
		t.Fatalf("cleared budget = %v, want nil", *stored.TrafficBudgetBytes)
	}

	// A snapshot keeps the budget, and a negative one is rejected on restore.
	if _, err := st.UpdateAccount(account.ID, model.AccountPatch{
		TrafficBudgetSet:   true,
		TrafficBudgetBytes: &budget,
	}); err != nil {
		t.Fatalf("UpdateAccount(budget again): %v", err)
	}
	snapshot, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return now }, nil)
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, ok := restored.Account(account.ID)
	if !ok || got.TrafficBudgetBytes == nil || *got.TrafficBudgetBytes != 1_000 {
		t.Fatalf("restored budget = %v (ok=%v), want 1000", got.TrafficBudgetBytes, ok)
	}

	// The caller's pointer must not alias store state.
	*got.TrafficBudgetBytes = 7
	again, _ := restored.Account(account.ID)
	if *again.TrafficBudgetBytes != 1_000 {
		t.Fatalf("snapshot aliases the caller's pointer: %d", *again.TrafficBudgetBytes)
	}
}

// TestRestoreRejectsNegativeTrafficBudget proves the state validator refuses a
// hand-edited snapshot instead of arming a nonsensical budget.
func TestRestoreRejectsNegativeTrafficBudget(t *testing.T) {
	st := newEmptyWithClock(func() time.Time { return time.Now() }, nil)
	data := []byte(`{"version":1,"accounts":[{"id":"acc-01","label":"Bad","trafficBudgetBytes":-5}]}`)
	if err := st.Restore(data); err == nil {
		t.Fatal("Restore accepted a negative traffic budget")
	}
}
