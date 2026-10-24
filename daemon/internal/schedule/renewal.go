package schedule

import (
	"fmt"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

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
		if !now.Before(expires) {
			key := store.ReminderMarkKey(account.ID, "overdue", 0, account.ExpiresAt)
			if _, seen := marks[key]; !seen {
				e.store.SetReminderMark(model.ReminderMark{
					AccountID: account.ID, Kind: "overdue", Threshold: 0,
					ExpiresAt: account.ExpiresAt, State: "sent",
					MarkedAt: now.UTC().Format(time.RFC3339),
				})
				overdueCount++
			}
			continue
		}
		daysLeft := int(expires.Sub(now).Hours() / 24)
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

// RenewalSweepNow runs the read-only sweep immediately. Tests use it; the
// engine itself calls it once per local day at RenewalHour.
func (e *Engine) RenewalSweepNow(now time.Time) {
	e.renewalSweep(now)
}
