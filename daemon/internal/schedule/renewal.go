package schedule

import (
	"fmt"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// Renewal reminder kinds. They are the schedule package's own vocabulary, not
// webhook event names: the daemon maps them onto account.renewal_due and
// account.expired, so this package never imports the webhook catalogue.
const (
	ReminderKindRenewalDue     = "renewal_due"
	ReminderKindRenewalOverdue = "renewal_overdue"
)

// ReminderEvent is one renewal notice produced by a sweep. A sweep arms at
// most one event per (account, threshold, expiresAt) mark, so the daemon sees
// each notice exactly once even when sweeps repeat. DaysLeft is the truncated
// whole-day distance between now and the expiry — the same value the sweep
// uses to pick a threshold — so it is zero on the expiry instant and negative
// once the membership has lapsed. It carries no credential.
type ReminderEvent struct {
	AccountID string
	Kind      string
	Threshold int
	ExpiresAt string
	DaysLeft  int
}

func (e *Engine) maybeRenewalSweep(now time.Time) {
	if !e.cfg.RenewalEnabled {
		return
	}
	loc, err := LoadTimezone(e.cfg.RenewalTimezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	day := local.Format("2006-01-02")
	e.mu.Lock()
	if e.lastSweepDay == day || local.Hour() < e.cfg.RenewalHour {
		e.mu.Unlock()
		return
	}
	e.lastSweepDay = day
	e.mu.Unlock()
	e.renewalSweep(now)
}

func (e *Engine) renewalSweep(now time.Time) {
	marks := e.store.ReminderMarks()
	valid := make(map[string]string)
	dueCount, overdueCount := 0, 0
	for _, account := range e.store.Accounts() {
		if account.ExpiresAt == "" || account.State == model.AccountExpired {
			continue
		}
		valid[account.ID] = account.ExpiresAt
		expires, err := time.Parse(time.RFC3339, account.ExpiresAt)
		if err != nil {
			continue
		}
		daysLeft := int(expires.Sub(now).Hours() / 24)
		if !now.Before(expires) {
			key := store.ReminderMarkKey(account.ID, "overdue", 0, account.ExpiresAt)
			if _, seen := marks[key]; !seen {
				e.store.SetReminderMark(model.ReminderMark{
					AccountID: account.ID, Kind: "overdue", Threshold: 0,
					ExpiresAt: account.ExpiresAt, State: "sent",
					MarkedAt: now.UTC().Format(time.RFC3339),
				})
				overdueCount++
				e.notifyReminder(ReminderEvent{
					AccountID: account.ID, Kind: ReminderKindRenewalOverdue,
					Threshold: 0, ExpiresAt: account.ExpiresAt, DaysLeft: daysLeft,
				})
			}
			continue
		}
		for _, threshold := range e.cfg.RenewalLeadDays {
			if daysLeft > threshold {
				continue
			}
			key := store.ReminderMarkKey(account.ID, "threshold", threshold, account.ExpiresAt)
			if _, seen := marks[key]; seen {
				continue
			}
			e.store.SetReminderMark(model.ReminderMark{
				AccountID: account.ID, Kind: "threshold", Threshold: threshold,
				ExpiresAt: account.ExpiresAt, State: "sent",
				MarkedAt: now.UTC().Format(time.RFC3339),
			})
			dueCount++
			e.notifyReminder(ReminderEvent{
				AccountID: account.ID, Kind: ReminderKindRenewalDue,
				Threshold: threshold, ExpiresAt: account.ExpiresAt, DaysLeft: daysLeft,
			})
		}
	}
	e.store.PruneReminderMarks(valid)
	if dueCount+overdueCount == 0 {
		return
	}
	e.store.WriteAudit(model.SeverityInfo, "system", "account.renewal_sweep", "system",
		fmt.Sprintf("due %d · overdue %d", dueCount, overdueCount), map[string]string{
			"dueCount":     fmt.Sprintf("%d", dueCount),
			"overdueCount": fmt.Sprintf("%d", overdueCount),
		})
	_ = e.store.AppendScheduleRun(model.ScheduleRun{
		ID:         e.store.NextRunID(),
		ScheduleID: "system:renewal-sweep",
		Trigger:    "cron",
		StartedAt:  now.UTC().Format(time.RFC3339),
		FinishedAt: now.UTC().Format(time.RFC3339),
		State:      "succeeded",
		Result:     "ok",
		Detail:     fmt.Sprintf("due %d · overdue %d", dueCount, overdueCount),
		Actor:      "system",
	})
}

// notifyReminder hands one newly armed mark to the daemon. A nil hook is the
// normal case for tests and for a daemon with no webhook sink.
func (e *Engine) notifyReminder(event ReminderEvent) {
	if e.cfg.OnReminder != nil {
		e.cfg.OnReminder(event)
	}
}

// RenewalSweepNow runs the read-only sweep immediately. Tests use it; the
// engine itself calls it once per local day at RenewalHour.
func (e *Engine) RenewalSweepNow(now time.Time) {
	e.renewalSweep(now)
}
