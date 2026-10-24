package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

func testStoreAccount(t *testing.T, st *Store) model.Account {
	t.Helper()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Schedule Store", Username: "user", Password: "secret",
		Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return account
}

func testScheduleValue(accountID, name string) model.Schedule {
	return model.Schedule{
		Name:              name,
		Enabled:           true,
		Action:            model.ActionAccountReconnect,
		Target:            model.ScheduleTarget{Kind: "account", ID: accountID},
		Trigger:           model.ScheduleTrigger{Kind: "interval", EverySec: 300},
		Guard:             model.ScheduleGuard{CooldownSec: 0, MaxRunsPerDay: 24},
		MisfirePolicy:     "skip",
		ConcurrencyPolicy: "forbid",
	}
}

func TestScheduleStoreCRUD(t *testing.T) {
	st := NewEmpty()
	account := testStoreAccount(t, st)
	created, err := st.CreateSchedule(testScheduleValue(account.ID, "Nightly"))
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if created.ID != "sch-001" || created.Revision != 1 {
		t.Fatalf("created = %+v", created)
	}
	if _, err := st.CreateSchedule(testScheduleValue(account.ID, "nightly")); err == nil {
		t.Fatal("duplicate case-insensitive name accepted")
	}

	next := created
	next.Name = "Nightly rename"
	next.NextRunAt = "2026-10-25T03:00:00Z"
	updated, err := st.UpdateSchedule(next, created.Revision)
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if updated.Revision != 2 || updated.Name != "Nightly rename" {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := st.UpdateSchedule(next, created.Revision); err == nil {
		t.Fatal("stale revision accepted")
	}
	// A no-op update keeps the revision.
	same, err := st.UpdateSchedule(updated, updated.Revision)
	if err != nil {
		t.Fatalf("no-op update: %v", err)
	}
	if same.Revision != updated.Revision {
		t.Fatalf("no-op bumped revision to %d", same.Revision)
	}
	runtimeUpdated, err := st.MutateSchedule(created.ID, func(s *model.Schedule) {
		s.LastResult = "ok"
		s.Cursor = 3
	})
	if err != nil {
		t.Fatalf("MutateSchedule: %v", err)
	}
	if runtimeUpdated.Revision != updated.Revision || runtimeUpdated.LastResult != "ok" {
		t.Fatalf("runtime update = %+v", runtimeUpdated)
	}

	if err := st.DeleteSchedule(created.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	if _, ok := st.Schedule(created.ID); ok {
		t.Fatal("schedule still present after delete")
	}
	if err := st.DeleteSchedule(created.ID); err == nil {
		t.Fatal("second delete succeeded")
	}
}

func TestScheduleStoreRunCaps(t *testing.T) {
	st := NewEmpty()
	account := testStoreAccount(t, st)
	created, err := st.CreateSchedule(testScheduleValue(account.ID, "Runs"))
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for i := range ScheduleRunsPerSchedule + 5 {
		st.AppendScheduleRun(model.ScheduleRun{
			ScheduleID: created.ID,
			StartedAt:  start.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			State:      "succeeded",
			Result:     "ok",
		})
	}
	runs := st.ScheduleRuns(created.ID, 0)
	if len(runs) != ScheduleRunsPerSchedule {
		t.Fatalf("runs = %d, want %d", len(runs), ScheduleRunsPerSchedule)
	}
	if runs[0].StartedAt != start.Add(time.Duration(ScheduleRunsPerSchedule+4)*time.Minute).Format(time.RFC3339) {
		t.Fatalf("newest run = %q", runs[0].StartedAt)
	}
	if got := st.ScheduleRunCount(created.ID, start, start.Add(24*time.Hour)); got != ScheduleRunsPerSchedule {
		t.Fatalf("run count = %d, want %d", got, ScheduleRunsPerSchedule)
	}
	// Skips are retained but not counted toward maxRunsPerDay.
	skips, err := st.CreateSchedule(testScheduleValue(account.ID, "Skips"))
	if err != nil {
		t.Fatalf("CreateSchedule skips: %v", err)
	}
	for range 5 {
		st.AppendScheduleRun(model.ScheduleRun{ScheduleID: skips.ID, StartedAt: start.Format(time.RFC3339),
			State: "succeeded", Result: "ok"})
	}
	st.AppendScheduleRun(model.ScheduleRun{ScheduleID: skips.ID, StartedAt: start.Format(time.RFC3339),
		State: "skipped", Result: "skipped"})
	if got := st.ScheduleRunCount(skips.ID, start, start.Add(24*time.Hour)); got != 5 {
		t.Fatalf("skip counted: %d", got)
	}
}

func TestScheduleStoreFiresAndMarks(t *testing.T) {
	st := NewEmpty()
	account := testStoreAccount(t, st)
	for i := range 3 {
		st.RecordScheduleFire(model.ScheduleFire{
			Key:        "sch-001@" + string(rune('1'+i)),
			ScheduleID: "sch-001",
			State:      "reserved",
			ReservedAt: "2026-10-24T10:00:00Z",
		})
	}
	if got := len(st.ScheduleFires()); got != 3 {
		t.Fatalf("fires = %d", got)
	}
	st.RecordScheduleFire(model.ScheduleFire{Key: "sch-001@1", ScheduleID: "sch-001",
		State: "succeeded", ReservedAt: "2026-10-24T10:00:00Z"})
	fires := st.ScheduleFires()
	if len(fires) != 3 || fires[len(fires)-1].State != "succeeded" {
		t.Fatalf("fire upsert = %+v", fires)
	}

	oldExpiry := "2026-11-22T16:35:33Z"
	newExpiry := "2027-11-22T16:35:33Z"
	st.SetReminderMark(model.ReminderMark{AccountID: account.ID, Kind: "threshold", Threshold: 30, ExpiresAt: oldExpiry, State: "sent"})
	st.SetReminderMark(model.ReminderMark{AccountID: account.ID, Kind: "threshold", Threshold: 30, ExpiresAt: newExpiry, State: "sent"})
	if len(st.ReminderMarks()) != 2 {
		t.Fatalf("marks = %+v", st.ReminderMarks())
	}
	st.PruneReminderMarks(map[string]string{account.ID: newExpiry})
	marks := st.ReminderMarks()
	if len(marks) != 1 {
		t.Fatalf("pruned marks = %+v", marks)
	}
	for key := range marks {
		if key != ReminderMarkKey(account.ID, "threshold", 30, newExpiry) {
			t.Fatalf("wrong surviving mark %q", key)
		}
	}
}

func TestScheduleSnapshotRoundTripAndAliasing(t *testing.T) {
	st := NewEmpty()
	account := testStoreAccount(t, st)
	created, err := st.CreateSchedule(testScheduleValue(account.ID, "Snapshot"))
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	params := []byte(`{"strategy":"pool","serverIds":["srv-a","srv-b"]}`)
	if _, err := st.MutateSchedule(created.ID, func(s *model.Schedule) {
		s.Target.Params = params
		s.Window = &model.ScheduleWindow{Days: []int{1, 2}, Start: "01:00", End: "06:00"}
	}); err != nil {
		t.Fatalf("MutateSchedule: %v", err)
	}
	st.AppendScheduleRun(model.ScheduleRun{ScheduleID: created.ID, StartedAt: "2026-10-24T10:00:00Z",
		State: "succeeded", Result: "ok"})
	st.RecordScheduleFire(model.ScheduleFire{Key: created.ID + "@100", ScheduleID: created.ID,
		State: "succeeded", ReservedAt: "2026-10-24T10:00:00Z"})
	st.SetReminderMark(model.ReminderMark{AccountID: account.ID, Kind: "threshold", Threshold: 30,
		ExpiresAt: account.ExpiresAt, State: "sent"})

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := NewEmpty()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	scheduleValue, ok := restored.Schedule(created.ID)
	if !ok {
		t.Fatal("restored schedule missing")
	}
	var restoredParams, originalParams map[string]any
	if err := json.Unmarshal(scheduleValue.Target.Params, &restoredParams); err != nil {
		t.Fatalf("restored params: %v", err)
	}
	if err := json.Unmarshal(params, &originalParams); err != nil {
		t.Fatalf("original params: %v", err)
	}
	if !reflect.DeepEqual(restoredParams, originalParams) || scheduleValue.Window == nil || len(scheduleValue.Window.Days) != 2 {
		t.Fatalf("restored schedule = %+v", scheduleValue)
	}
	if len(restored.ScheduleRuns(created.ID, 0)) != 1 {
		t.Fatal("restored runs missing")
	}
	if len(restored.ScheduleFires()) != 1 {
		t.Fatal("restored fires missing")
	}
	if len(restored.ReminderMarks()) != 1 {
		t.Fatal("restored marks missing")
	}

	// Mutating a returned value must not alias the store.
	scheduleValue.Window.Days[0] = 6
	scheduleValue.Target.Params[0] = '['
	again, _ := restored.Schedule(created.ID)
	var againParams map[string]any
	_ = json.Unmarshal(again.Target.Params, &againParams)
	if again.Window.Days[0] != 1 || !reflect.DeepEqual(againParams, originalParams) {
		t.Fatalf("store aliased a returned schedule: %+v", again)
	}

	// A second restore allocates a non-colliding id.
	next, err := restored.CreateSchedule(testScheduleValue(account.ID, "Second"))
	if err != nil {
		t.Fatalf("CreateSchedule after restore: %v", err)
	}
	if next.ID == created.ID {
		t.Fatalf("id collided after restore: %s", next.ID)
	}
}

func TestScheduleSnapshotRejectsBadRows(t *testing.T) {
	st := NewEmpty()
	account := testStoreAccount(t, st)
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	bad := []byte(`{"version":2,"accounts":[{"id":"acc-01"}],"schedules":[{"id":"sch-001","name":"x","action":"account.rotate","trigger":{"kind":"bogus"}}]}`)
	if err := st.Restore(bad); err == nil {
		t.Fatal("restore accepted an unknown trigger kind")
	}
	if err := st.Restore(data); err != nil {
		t.Fatalf("original snapshot no longer restores: %v", err)
	}
	_ = account
}
