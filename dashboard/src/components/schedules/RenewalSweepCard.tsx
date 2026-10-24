import { CalendarClock } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api } from '../../lib/api'
import { formatDate, formatDateTimeSeconds } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import {
  RENEWAL_WINDOW_DAYS,
  SYSTEM_SCHEDULE_ID,
  renewalReminders,
  scheduleActionLabel,
} from '../../lib/schedules'
import type { Account, Schedule, ScheduleRun } from '../../lib/types'
import { Badge, Card } from '../primitives'
import './schedules.css'

/**
 * The daemon owns the renewal sweep: it writes one reminder mark per account,
 * threshold and expiry, and exposes no mark API. This card is deliberately
 * read-only. When the schedule row is not in GET /v1/schedules (the deployed
 * daemon does not list it), the marks are derived from account expiry and the
 * state says so instead of inventing a next run.
 */
export function RenewalSweepCard({
  accounts,
  systemSchedule,
}: {
  accounts: Account[]
  systemSchedule?: Schedule
}) {
  const { t } = useI18n()
  const [lastRun, setLastRun] = useState<ScheduleRun | null>(null)
  const reminders = renewalReminders(accounts)

  useEffect(() => {
    let cancelled = false
    api
      .listScheduleRuns(SYSTEM_SCHEDULE_ID, 1)
      .then((runs) => {
        if (!cancelled) setLastRun(runs[0] ?? null)
      })
      .catch(() => {
        // The deployed daemon answers 404 for a pseudo schedule; the card then
        // falls back to the expiry-derived marks.
        if (!cancelled) setLastRun(null)
      })
    return () => {
      cancelled = true
    }
  }, [systemSchedule?.revision])

  const lastRunAt = systemSchedule?.lastRunAt ?? lastRun?.startedAt

  return (
    <Card
      className="schedule-renewal"
      title={
        <span className="schedule-renewal__title">
          <CalendarClock size={16} aria-hidden="true" />
          {t('schedules.renewal.title')}
        </span>
      }
      subtitle={t('schedules.renewal.subtitle')}
      actions={<Badge tone="info">{t('schedules.systemReadOnly')}</Badge>}
    >
      <div className="schedule-renewal__grid">
        <dl className="facts facts--compact schedule-renewal__facts">
          <div>
            <dt>{t('schedules.renewal.systemSchedule')}</dt>
            <dd className="mono">{SYSTEM_SCHEDULE_ID}</dd>
          </div>
          <div>
            <dt>{t('schedules.renewal.owner')}</dt>
            <dd>{systemSchedule?.owner ?? t('schedules.renewal.owner')}</dd>
          </div>
          <div>
            <dt>{t('schedules.renewal.action')}</dt>
            <dd>{scheduleActionLabel('account.renewal_reminder', t)}</dd>
          </div>
          <div>
            <dt>{t('schedules.renewal.state')}</dt>
            <dd>
              {systemSchedule ? (
                <Badge tone={systemSchedule.enabled ? 'success' : 'warning'}>
                  {systemSchedule.enabled ? t('common.enabled') : t('schedules.paused')}
                </Badge>
              ) : (
                <span className="muted">{t('schedules.renewal.stateUnavailable')}</span>
              )}
            </dd>
          </div>
          <div>
            <dt>{t('schedules.columnTrigger')}</dt>
            <dd>{systemSchedule?.trigger.expr ?? t('schedules.renewal.trigger')}</dd>
          </div>
          <div>
            <dt>{t('schedules.renewal.lastRun')}</dt>
            <dd className="mono">
              {lastRunAt
                ? formatDateTimeSeconds(lastRunAt)
                : t('schedules.renewal.neverRun')}
            </dd>
          </div>
        </dl>

        <div className="schedule-renewal__marks">
          <div className="schedule-renewal__marks-head">
            <span className="label">{t('schedules.renewal.reminders')}</span>
            <Badge tone={reminders.length > 0 ? 'warning' : 'neutral'}>
              {t('schedules.renewal.reminderCount', { count: reminders.length })}
            </Badge>
          </div>
          {reminders.length === 0 ? (
            <p className="schedule-renewal__empty">{t('schedules.renewal.empty')}</p>
          ) : (
            <ul className="schedule-renewal__list" aria-label={t('schedules.renewal.reminders')}>
              {reminders.map((reminder) => (
                <li className="schedule-renewal__row" key={`${reminder.accountId}-${reminder.expiresAt}`}>
                  <span className="schedule-renewal__account">{reminder.label}</span>
                  <span className="schedule-renewal__expiry">
                    {t('schedules.renewal.expiresAt', { date: formatDate(reminder.expiresAt) })}
                  </span>
                  <Badge tone={reminder.overdue ? 'danger' : 'warning'}>
                    {reminder.overdue
                      ? t('schedules.renewal.overdue', { days: Math.abs(reminder.days) })
                      : t('schedules.renewal.due', { days: reminder.days })}
                  </Badge>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
      <p className="schedule-renewal__note">
        {systemSchedule
          ? t('schedules.renewal.readOnlyNote')
          : `${t('schedules.renewal.unavailableHint')} ${t('schedules.renewal.readOnlyNote')}`}
      </p>
      <p className="schedule-renewal__note muted">
        {t('schedules.renewal.windowHint', { days: RENEWAL_WINDOW_DAYS })}
      </p>
    </Card>
  )
}
