package schedule

import (
	"testing"
	"time"

	_ "time/tzdata"

	"ajsaccountd/internal/model"
)

func mustCron(t *testing.T, expr string) Cron {
	t.Helper()
	cron, err := ParseCron(expr)
	if err != nil {
		t.Fatalf("ParseCron(%q): %v", expr, err)
	}
	return cron
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestScheduleCronParseAndNext(t *testing.T) {
	cases := []struct {
		expr string
		from string
		want string
	}{
		{"*/30 * * * *", "2026-10-24T10:00:00Z", "2026-10-24T10:30:00Z"},
		{"*/30 * * * *", "2026-10-24T10:30:00Z", "2026-10-24T11:00:00Z"},
		{"0 3 * * *", "2026-10-24T03:00:00Z", "2026-10-25T03:00:00Z"},
		{"5,35 2-4 * * *", "2026-10-24T02:06:00Z", "2026-10-24T02:35:00Z"},
		{"0 0 1 * *", "2026-10-24T00:00:00Z", "2026-11-01T00:00:00Z"},
		{"15 14 * * 1-5", "2026-10-24T00:00:00Z", "2026-10-26T14:15:00Z"}, // Saturday -> Monday
		{"@hourly", "2026-10-24T10:00:00Z", "2026-10-24T11:00:00Z"},
		{"@daily", "2026-10-24T10:00:00Z", "2026-10-25T00:00:00Z"},
	}
	for _, tc := range cases {
		cron := mustCron(t, tc.expr)
		got, ok := cron.Next(mustTime(t, tc.from), time.UTC)
		if !ok {
			t.Fatalf("%s: no next run after %s", tc.expr, tc.from)
		}
		if got.Format(time.RFC3339) != tc.want {
			t.Errorf("%s after %s = %s, want %s", tc.expr, tc.from, got.Format(time.RFC3339), tc.want)
		}
	}
}

func TestScheduleCronRejectsUnsupported(t *testing.T) {
	for _, expr := range []string{"", "* * * * * *", "@reboot", "0 0 L * *", "0 0 * * 1#2", "60 * * * *", "* 24 * * *", "* * 32 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *"} {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) succeeded, want error", expr)
		}
	}
}

func TestScheduleCronDayOfMonthOrWeekday(t *testing.T) {
	// Vixie semantics: when both are restricted, either match is enough.
	cron := mustCron(t, "0 0 13 * 5")
	// 2026-11-06 is a Friday; the 13th is also a Friday this month.
	got, ok := cron.Next(mustTime(t, "2026-11-01T00:00:00Z"), time.UTC)
	if !ok || got.Format("2006-01-02") != "2026-11-06" {
		t.Fatalf("first match = %v, want 2026-11-06", got)
	}
}

func TestScheduleCronDSTGapSkips(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	cron := mustCron(t, "30 2 * * *")
	from := time.Date(2026, 3, 8, 0, 0, 0, 0, loc)
	got, ok := cron.Next(from, loc)
	if !ok {
		t.Fatal("no next run")
	}
	if got.Format("2006-01-02 15:04") != "2026-03-09 02:30" {
		t.Fatalf("next = %s, want 2026-03-09 02:30 (gap skipped)", got.Format("2006-01-02 15:04 MST"))
	}
}

func TestScheduleCronDSTFallBackRunsOnceEarlier(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	cron := mustCron(t, "30 1 * * *")
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	got, ok := cron.Next(from, loc)
	if !ok {
		t.Fatal("no next run")
	}
	want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC) // 01:30 EDT, the first occurrence
	if !got.Equal(want) {
		t.Fatalf("next = %s (%s), want %s", got, got.UTC(), want)
	}
}

func TestScheduleCronBruteForceAgreement(t *testing.T) {
	expressions := []string{
		"* * * * *",
		"*/5 * * * *",
		"*/17 3 * * *",
		"0 0 * * 0",
		"0 0 1,15 * *",
		"30 6 * * 1-5",
		"5,35 2-4 * * *",
		"0 12 * 1,7 *",
		"45 23 31 * *",
		"15 14 1 * *",
		"0 9 * * 1",
		"20 4 * * 2,4",
		"0 0 29 2 *",
		"10 10 10 10 *",
		"0 1 * * 6",
		"*/10 8-18 * * 1-5",
		"3 3 * * *",
		"0 0 * * 1-5",
		"25 7 1-7 * *",
		"0 22 * * 0,6",
	}
	from := mustTime(t, "2026-10-24T00:00:00Z")
	for _, expr := range expressions {
		cron := mustCron(t, expr)
		want, wantOK := bruteForceNext(cron, from, 400*24*60)
		got, gotOK := cron.Next(from, time.UTC)
		if wantOK != gotOK {
			t.Errorf("%s: found=%v, brute force found=%v", expr, gotOK, wantOK)
			continue
		}
		if wantOK && !got.Equal(want) {
			t.Errorf("%s: next=%s, brute force=%s", expr, got, want)
		}
	}
}

func bruteForceNext(cron Cron, after time.Time, minutes int) (time.Time, bool) {
	candidate := after.Truncate(time.Minute).Add(time.Minute)
	for range minutes {
		if cron.months[int(candidate.Month())] && cron.dayMatches(candidate.Day(), candidate.Weekday()) &&
			cron.hours[candidate.Hour()] && cron.minutes[candidate.Minute()] {
			return candidate, true
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, false
}

func TestScheduleWindowCrossesMidnight(t *testing.T) {
	win, err := parseWindow(&model.ScheduleWindow{Days: []int{1}, Start: "22:00", End: "06:00"})
	if err != nil {
		t.Fatalf("parseWindow: %v", err)
	}
	loc := time.UTC
	mondayLate := time.Date(2026, 10, 26, 23, 0, 0, 0, loc)
	tuesdayEarly := time.Date(2026, 10, 27, 2, 0, 0, 0, loc)
	tuesdayLate := time.Date(2026, 10, 27, 22, 0, 0, 0, loc)
	if !win.contains(mondayLate, loc) {
		t.Error("Monday 23:00 should be inside the window")
	}
	if !win.contains(tuesdayEarly, loc) {
		t.Error("Tuesday 02:00 belongs to Monday's window")
	}
	if win.contains(tuesdayLate, loc) {
		t.Error("Tuesday 22:00 belongs to Tuesday's window, not Monday's")
	}
}

func TestScheduleNextRunIntervalAndOnce(t *testing.T) {
	now := mustTime(t, "2026-10-24T10:00:00Z")
	interval := model.Schedule{
		Trigger:   model.ScheduleTrigger{Kind: "interval", EverySec: 300},
		CreatedAt: now.Format(time.RFC3339),
	}
	got, ok := NextRun(interval, now, 0)
	if !ok || got.Format(time.RFC3339) != "2026-10-24T10:05:00Z" {
		t.Fatalf("interval next = %v, want 10:05", got)
	}
	once := model.Schedule{Trigger: model.ScheduleTrigger{Kind: "once", At: "2026-10-25T00:00:00Z"}}
	got, ok = NextRun(once, now, 0)
	if !ok || got.Format(time.RFC3339) != "2026-10-25T00:00:00Z" {
		t.Fatalf("once next = %v", got)
	}
	once.LastRunAt = now.Format(time.RFC3339)
	if _, ok := NextRun(once, now, 0); ok {
		t.Fatal("a fired once schedule must have no next run")
	}
}

func TestSchedulePreviewNextRuns(t *testing.T) {
	now := mustTime(t, "2026-10-24T10:00:00Z")
	preview := model.Schedule{
		Action:  model.ActionAccountReconnect,
		Trigger: model.ScheduleTrigger{Kind: "cron", Expr: "0 3 * * *", Timezone: "UTC"},
	}
	runs, warnings, err := Preview(preview, now, 3)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(runs) != 3 || len(warnings) != 0 {
		t.Fatalf("runs=%d warnings=%v", len(runs), warnings)
	}
	if runs[0].At != "2026-10-25T03:00:00Z" || runs[2].At != "2026-10-27T03:00:00Z" {
		t.Fatalf("preview runs = %+v", runs)
	}
}

func TestScheduleValidateSchedule(t *testing.T) {
	now := mustTime(t, "2026-10-24T10:00:00Z")
	valid := model.Schedule{
		Action:  model.ActionAccountReconnect,
		Target:  model.ScheduleTarget{Kind: "account", ID: "acc-01"},
		Trigger: model.ScheduleTrigger{Kind: "interval", EverySec: 300},
		Guard:   model.ScheduleGuard{MaxRunsPerDay: 24},
	}
	ApplyDefaults(&valid)
	if err := Validate(&valid, now); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
	bad := valid
	bad.Trigger.EverySec = 30
	if err := Validate(&bad, now); err == nil {
		t.Fatal("30-second interval accepted")
	}
	bad = valid
	bad.Trigger = model.ScheduleTrigger{Kind: "once", At: "2026-01-01T00:00:00Z"}
	if err := Validate(&bad, now); err == nil {
		t.Fatal("past once.at accepted")
	}
	bad = valid
	bad.MisfirePolicy = "run_once"
	bad.Trigger = model.ScheduleTrigger{Kind: "once", At: "2026-10-25T00:00:00Z"}
	if err := Validate(&bad, now); err == nil {
		t.Fatal("run_once accepted for a once trigger")
	}
	bad = valid
	bad.Trigger.Timezone = "Local"
	if err := Validate(&bad, now); err == nil {
		t.Fatal("Local timezone accepted")
	}
}
