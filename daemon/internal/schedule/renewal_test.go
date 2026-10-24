package schedule

import (
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// newSweepEngine builds an engine that starts no goroutine: renewal tests call
// RenewalSweepNow directly, exactly as the daily tick does.
func newSweepEngine(t *testing.T, st *store.Store, leadDays []int, hook func(ReminderEvent)) *Engine {
	t.Helper()
	engine, err := NewEngine(Config{
		Store:           st,
		Logger:          testLogger(),
		Tick:            time.Second,
		MaxConcurrent:   1,
		QueueSize:       10,
		NodeLimit:       1,
		RenewalEnabled:  true,
		RenewalLeadDays: leadDays,
		RenewalHour:     9,
		RenewalTimezone: "UTC",
		OnReminder:      hook,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

func createRenewalAccount(t *testing.T, st *store.Store, label string) model.Account {
	t.Helper()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: label, Username: "user", Password: "secret",
		Platform: model.PlatformLinux, Concurrency: 1, Regions: []string{"cn"},
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return account
}

// setExpiry stores an expiry and returns the canonical string the store kept.
func setExpiry(t *testing.T, st *store.Store, accountID string, expires time.Time) string {
	t.Helper()
	value := expires.UTC().Format(time.RFC3339)
	updated, err := st.UpdateAccount(accountID, model.AccountPatch{ExpiresAt: &value})
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	return updated.ExpiresAt
}

type reminderCollector struct {
	mu     sync.Mutex
	events []ReminderEvent
}

func (c *reminderCollector) add(event ReminderEvent) {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
}

func (c *reminderCollector) snapshot() []ReminderEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ReminderEvent(nil), c.events...)
}

// TestRenewalSweepEmitsOneEventPerThresholdAndOverdue walks one membership
// through 5/3/1 days left and past expiry. Each mark must produce exactly one
// event even though sweeps repeat at every step.
func TestRenewalSweepEmitsOneEventPerThresholdAndOverdue(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	st := store.NewEmpty()
	account := createRenewalAccount(t, st, "Renewal Thresholds")
	expiresAt := setExpiry(t, st, account.ID, now.Add(5*24*time.Hour))
	collector := &reminderCollector{}
	engine := newSweepEngine(t, st, []int{5, 3, 1}, collector.add)

	for _, at := range []time.Time{
		now, now.Add(time.Hour), // same-day repeats must not duplicate
		now.Add(2 * 24 * time.Hour), now.Add(2 * 24 * time.Hour),
		now.Add(4 * 24 * time.Hour),
		now.Add(5 * 24 * time.Hour), // expiry instant -> overdue
		now.Add(6 * 24 * time.Hour),
	} {
		engine.RenewalSweepNow(at)
	}

	want := []ReminderEvent{
		{AccountID: account.ID, Kind: ReminderKindRenewalDue, Threshold: 5, ExpiresAt: expiresAt, DaysLeft: 5},
		{AccountID: account.ID, Kind: ReminderKindRenewalDue, Threshold: 3, ExpiresAt: expiresAt, DaysLeft: 3},
		{AccountID: account.ID, Kind: ReminderKindRenewalDue, Threshold: 1, ExpiresAt: expiresAt, DaysLeft: 1},
		{AccountID: account.ID, Kind: ReminderKindRenewalOverdue, Threshold: 0, ExpiresAt: expiresAt, DaysLeft: 0},
	}
	got := collector.snapshot()
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %d (one per mark)", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	marks := st.ReminderMarks()
	if len(marks) != len(want) {
		t.Fatalf("marks = %+v, want %d", marks, len(want))
	}
	for _, event := range want {
		kind := "threshold"
		if event.Kind == ReminderKindRenewalOverdue {
			kind = "overdue"
		}
		key := store.ReminderMarkKey(event.AccountID, kind, event.Threshold, event.ExpiresAt)
		if _, ok := marks[key]; !ok {
			t.Fatalf("missing mark %q in %+v", key, marks)
		}
	}
}

// TestRenewalSweepEmitsPerAccount pins the account dimension of the mark key:
// two accounts on the same expiry each get their own event.
func TestRenewalSweepEmitsPerAccount(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	st := store.NewEmpty()
	first := createRenewalAccount(t, st, "Renewal One")
	second := createRenewalAccount(t, st, "Renewal Two")
	setExpiry(t, st, first.ID, now.Add(3*24*time.Hour))
	setExpiry(t, st, second.ID, now.Add(3*24*time.Hour))
	collector := &reminderCollector{}
	engine := newSweepEngine(t, st, []int{3}, collector.add)

	engine.RenewalSweepNow(now)
	engine.RenewalSweepNow(now.Add(time.Minute))

	got := collector.snapshot()
	if len(got) != 2 {
		t.Fatalf("events = %+v, want one per account", got)
	}
	seen := make(map[string]bool, 2)
	for _, event := range got {
		if event.Kind != ReminderKindRenewalDue || event.Threshold != 3 || event.DaysLeft != 3 {
			t.Fatalf("event = %+v, want a 3-day renewal_due", event)
		}
		seen[event.AccountID] = true
	}
	if !seen[first.ID] || !seen[second.ID] {
		t.Fatalf("events = %+v, want both %s and %s", got, first.ID, second.ID)
	}
}

func TestRenewalSweepSkipsMissingExpiryAndExpiredAccounts(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	st := store.NewEmpty()
	createRenewalAccount(t, st, "No Expiry")
	expired := createRenewalAccount(t, st, "Already Expired")
	expiresAt := now.Add(-48 * time.Hour).Format(time.RFC3339)
	state := model.AccountExpired
	if _, err := st.UpdateAccount(expired.ID, model.AccountPatch{ExpiresAt: &expiresAt, State: &state}); err != nil {
		t.Fatalf("UpdateAccount expired: %v", err)
	}
	collector := &reminderCollector{}
	engine := newSweepEngine(t, st, []int{5}, collector.add)

	engine.RenewalSweepNow(now)

	if got := collector.snapshot(); len(got) != 0 {
		t.Fatalf("events = %+v, want none for missing expiry or an expired account", got)
	}
	if marks := st.ReminderMarks(); len(marks) != 0 {
		t.Fatalf("marks = %+v, want none", marks)
	}
}

func TestRenewalSweepOverdueEventCarriesLapsedDays(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	st := store.NewEmpty()
	account := createRenewalAccount(t, st, "Renewal Lapsed")
	expiresAt := setExpiry(t, st, account.ID, now.Add(-2*24*time.Hour))
	collector := &reminderCollector{}
	engine := newSweepEngine(t, st, []int{1}, collector.add)

	engine.RenewalSweepNow(now)
	engine.RenewalSweepNow(now.Add(time.Hour))

	want := []ReminderEvent{{
		AccountID: account.ID, Kind: ReminderKindRenewalOverdue,
		Threshold: 0, ExpiresAt: expiresAt, DaysLeft: -2,
	}}
	got := collector.snapshot()
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}

func TestRenewalSweepNilHookStillMarks(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	st := store.NewEmpty()
	account := createRenewalAccount(t, st, "Renewal No Hook")
	setExpiry(t, st, account.ID, now.Add(5*24*time.Hour))
	engine := newSweepEngine(t, st, []int{5}, nil)

	engine.RenewalSweepNow(now) // must not panic
	engine.RenewalSweepNow(now.Add(time.Hour))

	if marks := st.ReminderMarks(); len(marks) != 1 {
		t.Fatalf("marks = %+v, want the 5-day mark", marks)
	}
}

// TestRenewalSweepRearmsAfterRenewal proves the mark key names the expiry: a
// renewed membership starts a fresh set of thresholds instead of inheriting
// the old expiry's marks.
func TestRenewalSweepRearmsAfterRenewal(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	st := store.NewEmpty()
	account := createRenewalAccount(t, st, "Renewal Renewed")
	firstExpiry := setExpiry(t, st, account.ID, now.Add(5*24*time.Hour))
	collector := &reminderCollector{}
	engine := newSweepEngine(t, st, []int{5, 3, 1}, collector.add)

	engine.RenewalSweepNow(now)
	renewedExpiry := setExpiry(t, st, account.ID, now.Add(10*24*time.Hour))
	engine.RenewalSweepNow(now)                         // 10 days left: no threshold
	engine.RenewalSweepNow(now.Add(5 * 24 * time.Hour)) // 5 days left on the new expiry
	engine.RenewalSweepNow(now.Add(7 * 24 * time.Hour)) // 3 days left on the new expiry

	got := collector.snapshot()
	want := []ReminderEvent{
		{AccountID: account.ID, Kind: ReminderKindRenewalDue, Threshold: 5, ExpiresAt: firstExpiry, DaysLeft: 5},
		{AccountID: account.ID, Kind: ReminderKindRenewalDue, Threshold: 5, ExpiresAt: renewedExpiry, DaysLeft: 5},
		{AccountID: account.ID, Kind: ReminderKindRenewalDue, Threshold: 3, ExpiresAt: renewedExpiry, DaysLeft: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
