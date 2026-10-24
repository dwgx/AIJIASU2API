package schedule

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(now time.Time) *testClock { return &testClock{now: now} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeCatalog struct {
	mu             sync.Mutex
	servers        map[string][]runtime.CatalogServer
	connectCalls   int
	reconnectCalls int
	lastServer     string
	err            error
	started        chan struct{}
	startOnce      sync.Once
	release        chan struct{}
}

func newFakeCatalog(accountID string) *fakeCatalog {
	return &fakeCatalog{
		servers: map[string][]runtime.CatalogServer{
			accountID: {
				{ID: "srv-a", Title: "A", Region: "cn", Eligible: true},
				{ID: "srv-b", Title: "B", Region: "cn", Eligible: true},
				{ID: "srv-c", Title: "C", Region: "us", Eligible: true},
			},
		},
	}
}

func (f *fakeCatalog) CatalogServers(accountID string) ([]runtime.CatalogServer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	servers, ok := f.servers[accountID]
	if !ok {
		return nil, false
	}
	return append([]runtime.CatalogServer(nil), servers...), true
}

func (f *fakeCatalog) ConnectServer(ctx context.Context, accountID, serverID, snapshotID, trigger string) (runtime.SwitchResult, error) {
	f.mu.Lock()
	f.connectCalls++
	f.lastServer = serverID
	err := f.err
	started, release := f.started, f.release
	f.mu.Unlock()
	if started != nil {
		f.startOnce.Do(func() { close(started) })
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return runtime.SwitchResult{}, ctx.Err()
		}
	}
	if err != nil {
		return runtime.SwitchResult{}, err
	}
	return runtime.SwitchResult{AccountID: accountID, ServerID: serverID, NodeID: "node-" + serverID,
		NodeState: model.NodeLive, SwitchedFrom: "srv-old"}, nil
}

func (f *fakeCatalog) ReconnectAccount(ctx context.Context, accountID, snapshotID, trigger string) (runtime.SwitchResult, error) {
	f.mu.Lock()
	f.reconnectCalls++
	err := f.err
	started, release := f.started, f.release
	f.mu.Unlock()
	if started != nil {
		f.startOnce.Do(func() { close(started) })
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return runtime.SwitchResult{}, ctx.Err()
		}
	}
	if err != nil {
		return runtime.SwitchResult{}, err
	}
	return runtime.SwitchResult{AccountID: accountID, ServerID: "srv-a", NodeID: "node-srv-a",
		NodeState: model.NodeLive}, nil
}

func (f *fakeCatalog) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectCalls, f.reconnectCalls
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testAccount(t *testing.T, st *store.Store) model.Account {
	t.Helper()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Schedule Test", Username: "user", Password: "secret",
		Platform: model.PlatformLinux, Concurrency: 1, Regions: []string{"cn"},
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.SetSelection(account.ID, "srv-a", "test"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	return account
}

var scheduleNameSeq int
var scheduleNameMu sync.Mutex

func createTestSchedule(t *testing.T, st *store.Store, accountID string, mutate func(*model.Schedule)) model.Schedule {
	t.Helper()
	scheduleNameMu.Lock()
	scheduleNameSeq++
	name := fmt.Sprintf("%s-%d", t.Name(), scheduleNameSeq)
	scheduleNameMu.Unlock()
	scheduleValue := model.Schedule{
		Name:              name,
		Enabled:           true,
		Action:            model.ActionAccountReconnect,
		Target:            model.ScheduleTarget{Kind: "account", ID: accountID},
		Trigger:           model.ScheduleTrigger{Kind: "interval", EverySec: 60},
		Guard:             model.ScheduleGuard{CooldownSec: 0, MaxRunsPerDay: 24},
		MisfirePolicy:     "skip",
		MisfireGraceSec:   300,
		ConcurrencyPolicy: "forbid",
	}
	if mutate != nil {
		mutate(&scheduleValue)
	}
	created, err := st.CreateSchedule(scheduleValue)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	return created
}

func newTestEngine(t *testing.T, st *store.Store, catalog Catalog, clock *testClock) *Engine {
	t.Helper()
	engine, err := NewEngine(Config{
		Store:           st,
		Catalog:         catalog,
		Logger:          testLogger(),
		Now:             clock.Now,
		Tick:            time.Second,
		MaxConcurrent:   4,
		QueueSize:       100,
		NodeLimit:       1,
		RenewalEnabled:  true,
		RenewalLeadDays: []int{30, 14, 7, 3, 1},
		RenewalHour:     9,
		RenewalTimezone: "UTC",
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = engine.Stop(stopCtx)
	})
	return engine
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestScheduleEngineFiresExactlyOnce(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	catalog := newFakeCatalog(account.ID)
	scheduleValue := createTestSchedule(t, st, account.ID, func(s *model.Schedule) {
		s.NextRunAt = now.Add(time.Second).Format(time.RFC3339)
	})
	engine := newTestEngine(t, st, catalog, clock)

	clock.Advance(time.Second)
	engine.Tick(clock.Now())
	engine.Tick(clock.Now()) // a second tick in the same second must not enqueue again
	waitFor(t, 2*time.Second, func() bool {
		updated, ok := st.Schedule(scheduleValue.ID)
		return len(st.ScheduleRuns(scheduleValue.ID, 0)) == 1 && ok && updated.NextRunAt != scheduleValue.NextRunAt
	})

	runs := st.ScheduleRuns(scheduleValue.ID, 0)
	if len(runs) != 1 || runs[0].Result != "ok" || runs[0].State != "succeeded" {
		t.Fatalf("runs = %+v", runs)
	}
	if _, reconnects := catalog.counts(); reconnects != 1 {
		t.Fatalf("reconnect calls = %d, want 1", reconnects)
	}
	updated, _ := st.Schedule(scheduleValue.ID)
	next, err := time.Parse(time.RFC3339, updated.NextRunAt)
	if err != nil || !next.After(clock.Now()) {
		t.Fatalf("nextRunAt = %q, want a future slot", updated.NextRunAt)
	}

	clock.Advance(30 * time.Second)
	engine.Tick(clock.Now())
	time.Sleep(50 * time.Millisecond)
	if runs := st.ScheduleRuns(scheduleValue.ID, 0); len(runs) != 1 {
		t.Fatalf("second tick produced another run: %+v", runs)
	}
}

func TestScheduleEngineManualDryRunKeepsOnceTarget(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	catalog := newFakeCatalog(account.ID)
	at := now.Add(time.Hour)
	scheduleValue := createTestSchedule(t, st, account.ID, func(s *model.Schedule) {
		s.Trigger = model.ScheduleTrigger{Kind: "once", At: at.Format(time.RFC3339)}
		s.NextRunAt = at.Format(time.RFC3339)
	})
	engine := newTestEngine(t, st, catalog, clock)

	run, err := engine.RunNow(context.Background(), scheduleValue.ID, true, false)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if run.Result != "dry_run" {
		t.Fatalf("run = %+v", run)
	}
	afterDryRun, _ := st.Schedule(scheduleValue.ID)
	if !afterDryRun.Enabled || afterDryRun.NextRunAt != at.Format(time.RFC3339) {
		t.Fatalf("dry run consumed the one-shot target: %+v", afterDryRun)
	}
	if _, reconnects := catalog.counts(); reconnects != 0 {
		t.Fatalf("dry run called reconnect %d times", reconnects)
	}

	clock.Advance(time.Hour + time.Second)
	engine.Tick(clock.Now())
	waitFor(t, 2*time.Second, func() bool { return len(st.ScheduleRuns(scheduleValue.ID, 0)) == 2 })
	afterRun, _ := st.Schedule(scheduleValue.ID)
	if afterRun.Enabled || afterRun.NextRunAt != "" {
		t.Fatalf("once schedule still armed after the real run: %+v", afterRun)
	}
	if _, reconnects := catalog.counts(); reconnects != 1 {
		t.Fatalf("reconnect calls = %d, want 1", reconnects)
	}
}

func TestScheduleEngineMisfireSkipDoesNotReplay(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	catalog := newFakeCatalog(account.ID)
	scheduleValue := createTestSchedule(t, st, account.ID, func(s *model.Schedule) {
		s.Trigger.EverySec = 300
		s.NextRunAt = now.Add(-time.Hour).Format(time.RFC3339)
		s.LastScheduledAt = now.Add(-time.Hour).Format(time.RFC3339)
		s.Guard.DryRun = true
	})
	engine := newTestEngine(t, st, catalog, clock)

	engine.Tick(clock.Now())
	time.Sleep(50 * time.Millisecond)
	if runs := st.ScheduleRuns(scheduleValue.ID, 0); len(runs) != 0 {
		t.Fatalf("misfire skip produced runs: %+v", runs)
	}
	updated, _ := st.Schedule(scheduleValue.ID)
	next, err := time.Parse(time.RFC3339, updated.NextRunAt)
	if err != nil || !next.After(now) {
		t.Fatalf("nextRunAt = %q, want a future slot", updated.NextRunAt)
	}
	found := false
	for _, event := range st.Audit(50) {
		if event.Action == "schedule.skip" && event.Meta["reason"] == "missed_window" {
			found = true
		}
	}
	if !found {
		t.Fatal("missed_window audit not written")
	}
}

func TestScheduleEngineRunOnceCatchUpIsBounded(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	catalog := newFakeCatalog(account.ID)
	scheduleValue := createTestSchedule(t, st, account.ID, func(s *model.Schedule) {
		s.Trigger.EverySec = 300
		s.NextRunAt = now.Add(-10 * time.Second).Format(time.RFC3339)
		s.LastScheduledAt = now.Add(-time.Hour).Format(time.RFC3339)
		s.MisfirePolicy = "run_once"
		s.MisfireGraceSec = 300
		s.Guard.DryRun = true
	})
	engine := newTestEngine(t, st, catalog, clock)

	waitFor(t, 2*time.Second, func() bool { return len(st.ScheduleRuns(scheduleValue.ID, 0)) == 1 })
	runs := st.ScheduleRuns(scheduleValue.ID, 0)
	if runs[0].Result != "dry_run" {
		t.Fatalf("catch-up run = %+v", runs)
	}
	updated, _ := st.Schedule(scheduleValue.ID)
	next, err := time.Parse(time.RFC3339, updated.NextRunAt)
	if err != nil || !next.After(clock.Now()) {
		t.Fatalf("nextRunAt = %q, want a future slot after one catch-up", updated.NextRunAt)
	}
	engine.Tick(clock.Now())
	time.Sleep(50 * time.Millisecond)
	if runs := st.ScheduleRuns(scheduleValue.ID, 0); len(runs) != 1 {
		t.Fatalf("catch-up chained a second run: %+v", runs)
	}
}

func TestScheduleEngineOneSessionPerAccount(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	catalog := newFakeCatalog(account.ID)
	catalog.started = make(chan struct{})
	catalog.release = make(chan struct{})
	first := createTestSchedule(t, st, account.ID, func(s *model.Schedule) {
		s.NextRunAt = now.Add(time.Second).Format(time.RFC3339)
	})
	second := createTestSchedule(t, st, account.ID, func(s *model.Schedule) {
		s.NextRunAt = now.Add(time.Second).Format(time.RFC3339)
	})
	engine := newTestEngine(t, st, catalog, clock)

	clock.Advance(time.Second)
	engine.Tick(clock.Now())
	select {
	case <-catalog.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not start")
	}
	waitFor(t, 2*time.Second, func() bool {
		runs := append(st.ScheduleRuns(first.ID, 0), st.ScheduleRuns(second.ID, 0)...)
		for _, run := range runs {
			if run.State == "skipped" {
				return true
			}
		}
		return false
	})
	close(catalog.release)
	waitFor(t, 2*time.Second, func() bool {
		return len(st.ScheduleRuns(first.ID, 0))+len(st.ScheduleRuns(second.ID, 0)) == 2
	})
	if _, reconnects := catalog.counts(); reconnects != 1 {
		t.Fatalf("reconnect calls = %d, want 1 (one-session)", reconnects)
	}
	skipped := 0
	for _, id := range []string{first.ID, second.ID} {
		for _, run := range st.ScheduleRuns(id, 0) {
			if run.State == "skipped" {
				skipped++
				if run.Reason != "in_flight" {
					t.Fatalf("skip reason = %q, want in_flight", run.Reason)
				}
			}
		}
	}
	if skipped != 1 {
		t.Fatalf("skipped runs = %d, want 1", skipped)
	}
}

func TestScheduleEngineDryRunDoesNotCallCatalog(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	catalog := newFakeCatalog(account.ID)
	scheduleValue := createTestSchedule(t, st, account.ID, func(s *model.Schedule) {
		s.Action = model.ActionAccountRotate
		s.Target.Params = []byte(`{"strategy":"pool","serverIds":["srv-a","srv-b"]}`)
		s.Guard.DryRun = true
		s.NextRunAt = now.Add(time.Second).Format(time.RFC3339)
	})
	engine := newTestEngine(t, st, catalog, clock)

	clock.Advance(time.Second)
	engine.Tick(clock.Now())
	waitFor(t, 2*time.Second, func() bool { return len(st.ScheduleRuns(scheduleValue.ID, 0)) == 1 })
	runs := st.ScheduleRuns(scheduleValue.ID, 0)
	if runs[0].Result != "dry_run" {
		t.Fatalf("run = %+v, want dry_run", runs[0])
	}
	if connects, _ := catalog.counts(); connects != 0 {
		t.Fatalf("dry run called ConnectServer %d times", connects)
	}
}

func TestScheduleEngineCircuitBreakerSkips(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	catalog := newFakeCatalog(account.ID)
	catalog.err = &runtime.SwitchError{Reason: runtime.ReasonSessionOpen, Message: "open failed"}
	scheduleValue := createTestSchedule(t, st, account.ID, func(s *model.Schedule) {
		s.Guard.DryRun = false
		s.NextRunAt = now.Add(time.Second).Format(time.RFC3339)
	})
	engine := newTestEngine(t, st, catalog, clock)

	for range 3 {
		run, err := engine.RunNow(context.Background(), scheduleValue.ID, false, false)
		if err != nil {
			t.Fatalf("RunNow: %v", err)
		}
		if run.Result != "error" {
			t.Fatalf("run = %+v, want error", run)
		}
	}
	if _, reconnects := catalog.counts(); reconnects != 3 {
		t.Fatalf("reconnect calls = %d, want 3", reconnects)
	}
	updated, _ := st.Schedule(scheduleValue.ID)
	updated.NextRunAt = clock.Now().Add(time.Second).Format(time.RFC3339)
	if _, err := st.MutateSchedule(scheduleValue.ID, func(s *model.Schedule) { s.NextRunAt = updated.NextRunAt }); err != nil {
		t.Fatalf("MutateSchedule: %v", err)
	}
	clock.Advance(time.Second)
	engine.Tick(clock.Now())
	waitFor(t, 2*time.Second, func() bool {
		for _, run := range st.ScheduleRuns(scheduleValue.ID, 0) {
			if run.Reason == "circuit_open" {
				return true
			}
		}
		return false
	})
	if _, reconnects := catalog.counts(); reconnects != 3 {
		t.Fatalf("circuit did not stop the call: reconnects = %d", reconnects)
	}
}

func TestScheduleEnginePausesAfterFiveFailures(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	catalog := newFakeCatalog(account.ID)
	catalog.err = &runtime.SwitchError{Reason: runtime.ReasonSessionOpen, Message: "open failed"}
	scheduleValue := createTestSchedule(t, st, account.ID, func(s *model.Schedule) {
		s.Guard.DryRun = false
	})
	engine := newTestEngine(t, st, catalog, clock)

	for range 5 {
		if _, err := engine.RunNow(context.Background(), scheduleValue.ID, false, false); err != nil {
			t.Fatalf("RunNow: %v", err)
		}
	}
	updated, _ := st.Schedule(scheduleValue.ID)
	if updated.Enabled {
		t.Fatal("schedule still enabled after 5 failures")
	}
	if updated.PausedReason != runtime.ReasonSessionOpen {
		t.Fatalf("pausedReason = %q, want %q", updated.PausedReason, runtime.ReasonSessionOpen)
	}
	if !updated.Degraded {
		t.Fatal("schedule not degraded after 5 failures")
	}
}

func TestScheduleRenewalSweepIsIdempotent(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	expiresAt := now.Add(29 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := st.UpdateAccount(account.ID, model.AccountPatch{ExpiresAt: &expiresAt}); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	engine := newTestEngine(t, st, nil, clock)

	engine.RenewalSweepNow(now)
	marks := st.ReminderMarks()
	if len(marks) != 1 {
		t.Fatalf("marks = %+v, want one 30-day mark", marks)
	}
	engine.RenewalSweepNow(now.Add(time.Hour))
	if len(st.ReminderMarks()) != 1 {
		t.Fatalf("second sweep duplicated the mark: %+v", st.ReminderMarks())
	}
	runs := st.ScheduleRuns("system:renewal-sweep", 0)
	if len(runs) != 1 {
		t.Fatalf("sweep runs = %d, want 1", len(runs))
	}

	renewed := now.Add(20 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := st.UpdateAccount(account.ID, model.AccountPatch{ExpiresAt: &renewed}); err != nil {
		t.Fatalf("UpdateAccount renewed: %v", err)
	}
	engine.RenewalSweepNow(now.Add(2 * time.Hour))
	marks = st.ReminderMarks()
	if len(marks) != 1 {
		t.Fatalf("marks after renewal = %+v, want one re-armed mark", marks)
	}
	for key := range marks {
		if !strings.Contains(key, renewed) {
			t.Fatalf("mark key %q does not name the renewed expiry %q", key, renewed)
		}
	}
}
