package store

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// Scheduler retention caps (DESIGN-SCHEDULE §3.8, §6.4). They are separate
// from the audit cap so a noisy schedule cannot evict audit history.
const (
	MaxSchedules            = 50
	ScheduleRunRetention    = 1000
	ScheduleRunsPerSchedule = 200
	ScheduleFireRetention   = 1000
	ReminderMarkRetention   = 2000
)

// Schedules returns a deep copy in creation order.
func (s *Store) Schedules() []model.Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Schedule, len(s.schedules))
	for i, schedule := range s.schedules {
		out[i] = cloneSchedule(schedule)
	}
	return out
}

// Schedule returns one schedule by id.
func (s *Store) Schedule(id string) (model.Schedule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.scheduleIndexLocked(id)
	if idx < 0 {
		return model.Schedule{}, false
	}
	return cloneSchedule(s.schedules[idx]), true
}

// CreateSchedule inserts a schedule, assigning the id and revision. The caller
// has already validated the action, trigger and target; the store enforces the
// structural invariants the snapshot depends on.
func (s *Store) CreateSchedule(schedule model.Schedule, actor ...string) (model.Schedule, error) {
	name := strings.TrimSpace(schedule.Name)
	switch {
	case name == "":
		return model.Schedule{}, invalidf("name is required")
	case len([]rune(name)) > 60:
		return model.Schedule{}, invalidf("name must be at most 60 characters")
	case !validScheduleAction(schedule.Action):
		return model.Schedule{}, invalidf("action %q is not supported", schedule.Action)
	case !validTriggerKind(schedule.Trigger.Kind):
		return model.Schedule{}, invalidf("trigger.kind must be cron, interval or once")
	}
	schedule.Name = name

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scheduleNameTakenLocked(name, "") {
		return model.Schedule{}, conflictf("a schedule named %q already exists", name)
	}
	if len(s.schedules) >= MaxSchedules {
		return model.Schedule{}, conflictf("at most %d schedules are allowed", MaxSchedules)
	}
	if schedule.ID == "" {
		s.scheduleSeq++
		schedule.ID = fmt.Sprintf("sch-%03d", s.scheduleSeq)
	} else {
		if s.scheduleIndexLocked(schedule.ID) >= 0 {
			return model.Schedule{}, conflictf("schedule %q already exists", schedule.ID)
		}
		s.scheduleSeq = max(s.scheduleSeq, sequenceOf(schedule.ID, "sch-"))
	}
	now := iso(s.now())
	schedule.Revision = 1
	schedule.CreatedAt = now
	schedule.UpdatedAt = now
	schedule.Degraded = schedule.ConsecutiveFailures >= 3
	s.schedules = append(s.schedules, cloneSchedule(schedule))
	s.addAuditLocked(model.SeverityInfo, auditActorArg(actor), "schedule.created", schedule.ID,
		fmt.Sprintf("%s · %s", schedule.Name, schedule.Action), map[string]string{
			"scheduleId": schedule.ID,
			"action":     string(schedule.Action),
			"targetKind": schedule.Target.Kind,
			"targetId":   schedule.Target.ID,
			"enabled":    fmt.Sprintf("%t", schedule.Enabled),
			"dryRun":     fmt.Sprintf("%t", schedule.Guard.DryRun),
		})
	return cloneSchedule(schedule), nil
}

// UpdateSchedule applies an already-validated replacement and bumps the
// revision. expectedRevision is the revision the caller read; a mismatch is a
// conflict. ID, createdAt and revision are server-owned.
func (s *Store) UpdateSchedule(next model.Schedule, expectedRevision int64, actor ...string) (model.Schedule, error) {
	name := strings.TrimSpace(next.Name)
	if name == "" {
		return model.Schedule{}, invalidf("name is required")
	}
	next.Name = name
	if !validScheduleAction(next.Action) {
		return model.Schedule{}, invalidf("action %q is not supported", next.Action)
	}
	if !validTriggerKind(next.Trigger.Kind) {
		return model.Schedule{}, invalidf("trigger.kind must be cron, interval or once")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.scheduleIndexLocked(next.ID)
	if idx < 0 {
		return model.Schedule{}, notFoundf("schedule %q not found", next.ID)
	}
	current := s.schedules[idx]
	if expectedRevision != current.Revision {
		return model.Schedule{}, conflictf("schedule %q revision is %d", next.ID, current.Revision)
	}
	if s.scheduleNameTakenLocked(name, next.ID) {
		return model.Schedule{}, conflictf("a schedule named %q already exists", name)
	}
	next.Revision = current.Revision + 1
	next.CreatedAt = current.CreatedAt
	next.UpdatedAt = iso(s.now())
	if reflect.DeepEqual(stripServerFields(next), stripServerFields(current)) {
		return cloneSchedule(current), nil
	}
	next.Degraded = next.ConsecutiveFailures >= 3
	s.schedules[idx] = cloneSchedule(next)
	s.addAuditLocked(model.SeverityInfo, auditActorArg(actor), "schedule.updated", next.ID,
		next.Name, map[string]string{
			"scheduleId": next.ID,
			"action":     string(next.Action),
			"targetKind": next.Target.Kind,
			"targetId":   next.Target.ID,
			"enabled":    fmt.Sprintf("%t", next.Enabled),
			"dryRun":     fmt.Sprintf("%t", next.Guard.DryRun),
		})
	return cloneSchedule(next), nil
}

// MutateSchedule applies a runtime-only update (lastRunAt, nextRunAt, cursor,
// failure counters) without bumping the revision or writing an audit row. The
// engine owns these fields; a client edit cannot race them because the API
// always writes through UpdateSchedule with a revision check.
func (s *Store) MutateSchedule(id string, mutate func(*model.Schedule)) (model.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.scheduleIndexLocked(id)
	if idx < 0 {
		return model.Schedule{}, notFoundf("schedule %q not found", id)
	}
	next := cloneSchedule(s.schedules[idx])
	mutate(&next)
	next.ID = s.schedules[idx].ID
	next.Revision = s.schedules[idx].Revision
	next.CreatedAt = s.schedules[idx].CreatedAt
	next.Degraded = next.ConsecutiveFailures >= 3
	s.schedules[idx] = cloneSchedule(next)
	return cloneSchedule(next), nil
}

// DeleteSchedule removes one schedule and writes schedule.deleted. Runs and
// fire records stay until retention evicts them, matching the audit contract.
func (s *Store) DeleteSchedule(id string, actor ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.scheduleIndexLocked(id)
	if idx < 0 {
		return notFoundf("schedule %q not found", id)
	}
	name := s.schedules[idx].Name
	s.schedules = append(s.schedules[:idx], s.schedules[idx+1:]...)
	s.addAuditLocked(model.SeverityInfo, auditActorArg(actor), "schedule.deleted", id, name,
		map[string]string{"scheduleId": id})
	return nil
}

// NextRunID reserves the next run identifier. The engine reserves it before
// the fire journal write so a crash record and the eventual run row share one
// id.
func (s *Store) NextRunID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runSeq++
	return fmt.Sprintf("run-%04d", s.runSeq)
}

// AppendScheduleRun records one attempt, assigning run-NNNN. Runs are newest
// first and bounded both globally and per schedule.
func (s *Store) AppendScheduleRun(run model.ScheduleRun) model.ScheduleRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.ID == "" {
		s.runSeq++
		run.ID = fmt.Sprintf("run-%04d", s.runSeq)
	} else {
		s.runSeq = max(s.runSeq, sequenceOf(run.ID, "run-"))
	}
	s.scheduleRuns = prependCapped(s.scheduleRuns, run, ScheduleRunRetention)
	s.scheduleRuns = s.trimRunsPerScheduleLocked(s.scheduleRuns, run.ScheduleID)
	return run
}

// ScheduleRuns returns one schedule's runs newest first, capped at limit
// (0 means every retained row).
func (s *Store) ScheduleRuns(scheduleID string, limit int) []model.ScheduleRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.ScheduleRun, 0, limit)
	for _, run := range s.scheduleRuns {
		if run.ScheduleID != scheduleID {
			continue
		}
		out = append(out, run)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// ScheduleRunCount counts executed runs (ok, error, dry_run) whose startedAt
// falls in [from, to). Skips are not counted, matching guard.maxRunsPerDay.
func (s *Store) ScheduleRunCount(scheduleID string, from, to time.Time) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, run := range s.scheduleRuns {
		if run.ScheduleID != scheduleID {
			continue
		}
		switch run.Result {
		case "ok", "error", "dry_run":
		default:
			continue
		}
		started, err := time.Parse(time.RFC3339, run.StartedAt)
		if err != nil {
			continue
		}
		if !started.Before(from) && started.Before(to) {
			count++
		}
	}
	return count
}

// RecordScheduleFire upserts one fire record by key. The engine writes the
// reservation and then the terminal state; the snapshot carries the result.
func (s *Store) RecordScheduleFire(fire model.ScheduleFire) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.scheduleFires {
		if s.scheduleFires[i].Key == fire.Key {
			s.scheduleFires[i] = fire
			return
		}
	}
	s.scheduleFires = prependCapped(s.scheduleFires, fire, ScheduleFireRetention)
}

// ScheduleFires returns a copy of the retained fire records newest first.
func (s *Store) ScheduleFires() []model.ScheduleFire {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.ScheduleFire(nil), s.scheduleFires...)
}

// ReminderMarks returns a copy of the persisted reminder marks.
func (s *Store) ReminderMarks() map[string]model.ReminderMark {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]model.ReminderMark, len(s.reminderMarks))
	for key, mark := range s.reminderMarks {
		out[key] = mark
	}
	return out
}

// SetReminderMark stores one notice mark, pruning the oldest entries once the
// cap is reached. The key includes expiresAt, so a renewed account re-arms
// every threshold without deleting anything.
func (s *Store) SetReminderMark(mark model.ReminderMark) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := ReminderMarkKey(mark.AccountID, mark.Kind, mark.Threshold, mark.ExpiresAt)
	if mark.MarkedAt == "" {
		mark.MarkedAt = iso(s.now())
	}
	if len(s.reminderMarks) >= ReminderMarkRetention {
		s.pruneReminderMarksLocked()
	}
	s.reminderMarks[key] = mark
}

// PruneReminderMarks drops marks that no longer match an account's current
// expiry. It runs after an account update or delete so a changed expiresAt
// re-arms the thresholds without keeping stale rows.
func (s *Store) PruneReminderMarks(valid map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, mark := range s.reminderMarks {
		expiresAt, ok := valid[mark.AccountID]
		if !ok || expiresAt != mark.ExpiresAt {
			delete(s.reminderMarks, key)
		}
	}
}

// ReminderMarkKey is the persisted key of one notice:
// accountID|kind|threshold|expiresAt.
func ReminderMarkKey(accountID, kind string, threshold int, expiresAt string) string {
	return fmt.Sprintf("%s|%s|%d|%s", accountID, kind, threshold, expiresAt)
}

func (s *Store) pruneReminderMarksLocked() {
	// Map iteration is unordered, so drop the lexicographically smallest
	// MarkedAt entries until there is room. Marks are tiny; this runs only at
	// the 2,000-row cap.
	type entry struct{ key, at string }
	entries := make([]entry, 0, len(s.reminderMarks))
	for key, mark := range s.reminderMarks {
		entries = append(entries, entry{key: key, at: mark.MarkedAt})
	}
	for len(s.reminderMarks) >= ReminderMarkRetention && len(entries) > 0 {
		oldest := 0
		for i := 1; i < len(entries); i++ {
			if entries[i].at < entries[oldest].at {
				oldest = i
			}
		}
		delete(s.reminderMarks, entries[oldest].key)
		entries = append(entries[:oldest], entries[oldest+1:]...)
	}
}

func (s *Store) trimRunsPerScheduleLocked(runs []model.ScheduleRun, scheduleID string) []model.ScheduleRun {
	seen := 0
	kept := runs[:0]
	for _, run := range runs {
		if run.ScheduleID == scheduleID {
			seen++
			if seen > ScheduleRunsPerSchedule {
				continue
			}
		}
		kept = append(kept, run)
	}
	return kept
}

func (s *Store) scheduleIndexLocked(id string) int {
	for i, schedule := range s.schedules {
		if schedule.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) scheduleNameTakenLocked(name, exceptID string) bool {
	for _, schedule := range s.schedules {
		if schedule.ID != exceptID && strings.EqualFold(schedule.Name, name) {
			return true
		}
	}
	return false
}

// stripServerFields drops the fields UpdateSchedule overwrites so an
// unchanged PATCH does not bump the revision or write an audit row.
func stripServerFields(schedule model.Schedule) model.Schedule {
	schedule.Revision = 0
	schedule.UpdatedAt = ""
	return schedule
}

func cloneSchedule(schedule model.Schedule) model.Schedule {
	schedule.Target.Params = append([]byte(nil), schedule.Target.Params...)
	if schedule.Window != nil {
		window := *schedule.Window
		window.Days = append([]int(nil), schedule.Window.Days...)
		schedule.Window = &window
	}
	return schedule
}

func validScheduleAction(action model.ScheduleAction) bool {
	switch action {
	case model.ActionAccountSwitch, model.ActionAccountRotate, model.ActionAccountReconnect,
		model.ActionAccountLogin, model.ActionAccountLogout, model.ActionNodeDrain,
		model.ActionNodeUndrain, model.ActionLeaseRotate, model.ActionSubscriptionRefresh,
		model.ActionEgressCheck, model.ActionRenewalReminder:
		return true
	default:
		return false
	}
}

func validTriggerKind(kind string) bool {
	switch kind {
	case "cron", "interval", "once":
		return true
	default:
		return false
	}
}
