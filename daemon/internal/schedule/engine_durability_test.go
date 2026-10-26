package schedule

import (
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// TestFinishRunDoesNotNotifyWhenTheRunCannotBeCommitted pins the completion
// contract: the customer webhook is the one step that leaves the process, so
// it fires only after every row of the run is durable. The journal is closed,
// AppendScheduleRun fails, and the sink must never be called.
func TestFinishRunDoesNotNotifyWhenTheRunCannotBeCommitted(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	scheduleValue := createTestSchedule(t, st, account.ID, nil)

	journal, err := store.OpenJournal(t.TempDir())
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	st.AttachJournal(journal)
	if err := journal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	notified := 0
	engine, err := NewEngine(Config{
		Store:         st,
		Catalog:       newFakeCatalog(account.ID),
		Logger:        testLogger(),
		Now:           clock.Now,
		Tick:          time.Second,
		MaxConcurrent: 1,
		QueueSize:     10,
		NodeLimit:     1,
		OnRun: func(model.Schedule, model.ScheduleRun) {
			notified++
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	engine.finishRun(scheduleValue, runJob{scheduleID: scheduleValue.ID, scheduled: now},
		"run-0001", "fire-0001", now, actionOutcome{Result: "ok"}, now)

	if notified != 0 {
		t.Fatalf("OnRun fired %d times for a run the journal could not hold", notified)
	}
	if err := st.DurabilityError(); err == nil {
		t.Fatal("the failed commit did not poison the store")
	}
}

// TestSkipDoesNotAdvanceWhenTheRunCannotBeCommitted is the skip half: a skip
// the journal cannot hold must not rewrite the schedule or emit an audit row.
func TestSkipDoesNotAdvanceWhenTheRunCannotBeCommitted(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	clock := newTestClock(now)
	st := store.NewEmpty()
	account := testAccount(t, st)
	scheduleValue := createTestSchedule(t, st, account.ID, nil)

	journal, err := store.OpenJournal(t.TempDir())
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	st.AttachJournal(journal)
	if err := journal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	engine, err := NewEngine(Config{
		Store:         st,
		Catalog:       newFakeCatalog(account.ID),
		Logger:        testLogger(),
		Now:           clock.Now,
		Tick:          time.Second,
		MaxConcurrent: 1,
		QueueSize:     10,
		NodeLimit:     1,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	before, _ := st.Schedule(scheduleValue.ID)
	engine.skip(scheduleValue, now, now, "cooldown", false, false)
	after, _ := st.Schedule(scheduleValue.ID)
	if after.LastResult != before.LastResult || after.NextRunAt != before.NextRunAt {
		t.Fatalf("skip advanced the schedule after a failed commit: %+v -> %+v", before, after)
	}
	if err := st.DurabilityError(); err == nil {
		t.Fatal("the failed commit did not poison the store")
	}
}
