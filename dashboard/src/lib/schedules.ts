/* Schedule math shared by the page and the mock API.
 *
 * The live daemon owns cron parsing, timezone handling and window semantics
 * (`daemon/internal/schedule`). The dashboard still needs a small pure mirror
 * for two things: the in-browser mock (so the suite runs without a daemon) and
 * a client-side sanity check before a request leaves the dialog.
 *
 * The mock evaluates cron in UTC. The live daemon applies the IANA timezone
 * and returns the authoritative instants; the UI never recomputes a run that
 * the daemon already returned.
 */

import type {
  Schedule,
  ScheduleAction,
  SchedulePreviewRun,
  ScheduleResult,
  ScheduleTrigger,
  ScheduleWindow,
} from './types'

const MINUTE_MS = 60_000
const DAY_MS = 86_400_000
const MAX_DAYS = 366
/** Bound for advancing an interval through a narrow window. */
const MAX_WINDOW_STEPS = 100_000

const CRON_ALIASES: Record<string, string> = {
  '@hourly': '0 * * * *',
  '@daily': '0 0 * * *',
  '@midnight': '0 0 * * *',
  '@weekly': '0 0 * * 0',
  '@monthly': '0 0 1 * *',
  '@yearly': '0 0 1 1 *',
  '@annually': '0 0 1 1 *',
}

export interface CronSpec {
  minutes: boolean[]
  hours: boolean[]
  dom: boolean[]
  months: boolean[]
  dow: boolean[]
  domRestricted: boolean
  dowRestricted: boolean
}

function parseCronField(
  field: string,
  min: number,
  max: number,
  set: (value: number) => void,
): void {
  for (const part of field.split(',')) {
    if (part === '') throw new Error(`empty list item in “${field}”`)
    let step = 1
    let rangePart = part
    const slash = part.indexOf('/')
    if (slash >= 0) {
      rangePart = part.slice(0, slash)
      const parsed = Number(part.slice(slash + 1))
      if (!Number.isInteger(parsed) || parsed < 1) {
        throw new Error(`invalid step in “${part}”`)
      }
      step = parsed
    }
    let start = min
    let end = max
    if (rangePart === '*') {
      // Full range.
    } else if (rangePart.includes('-')) {
      const [rawStart, rawEnd] = rangePart.split('-', 2)
      start = Number(rawStart)
      end = Number(rawEnd)
      if (!Number.isInteger(start) || !Number.isInteger(end)) {
        throw new Error(`invalid range in “${part}”`)
      }
    } else {
      start = Number(rangePart)
      end = start
      if (!Number.isInteger(start)) throw new Error(`invalid value “${part}”`)
      if (slash >= 0) end = max
    }
    if (start < min || end > max || start > end) {
      throw new Error(`value “${part}” is outside ${min}-${max}`)
    }
    for (let value = start; value <= end; value += step) set(value)
  }
}

/** Parses a standard five-field expression; seconds and L/W/# are rejected. */
export function parseCronExpression(expr: string): CronSpec {
  const trimmed = expr.trim()
  if (!trimmed) throw new Error('cron expression is required')
  const normalized = CRON_ALIASES[trimmed.toLowerCase()] ?? trimmed
  const fields = normalized.split(/\s+/)
  if (fields.length !== 5) {
    throw new Error(`cron expression must have 5 fields, got ${fields.length}`)
  }
  for (const field of fields) {
    if (/[?LW#]/.test(field)) throw new Error(`cron field “${field}” uses an unsupported extension`)
  }
  const spec: CronSpec = {
    minutes: new Array<boolean>(60).fill(false),
    hours: new Array<boolean>(24).fill(false),
    dom: new Array<boolean>(32).fill(false),
    months: new Array<boolean>(13).fill(false),
    dow: new Array<boolean>(7).fill(false),
    domRestricted: fields[2] !== '*',
    dowRestricted: fields[4] !== '*',
  }
  try {
    parseCronField(fields[0], 0, 59, (value) => (spec.minutes[value] = true))
    parseCronField(fields[1], 0, 23, (value) => (spec.hours[value] = true))
    parseCronField(fields[2], 1, 31, (value) => (spec.dom[value] = true))
    parseCronField(fields[3], 1, 12, (value) => (spec.months[value] = true))
    parseCronField(fields[4], 0, 7, (value) => (spec.dow[value === 7 ? 0 : value] = true))
  } catch (cause) {
    throw new Error(cause instanceof Error ? cause.message : String(cause))
  }
  return spec
}

function cronDayMatches(spec: CronSpec, dayOfMonth: number, weekday: number): boolean {
  const domMatch = spec.dom[dayOfMonth]
  const dowMatch = spec.dow[weekday]
  if (spec.domRestricted && spec.dowRestricted) return domMatch || dowMatch
  if (spec.domRestricted) return domMatch
  if (spec.dowRestricted) return dowMatch
  return true
}

/** First matching instant strictly after `after`, evaluated in UTC. */
function nextCronOccurrence(spec: CronSpec, after: Date): Date | null {
  const afterMs = after.getTime()
  const start = new Date(afterMs)
  const dayStart = Date.UTC(
    start.getUTCFullYear(),
    start.getUTCMonth(),
    start.getUTCDate(),
  )
  for (let offset = 0; offset <= MAX_DAYS; offset += 1) {
    const day = new Date(dayStart + offset * DAY_MS)
    const year = day.getUTCFullYear()
    const month = day.getUTCMonth() + 1
    const dayOfMonth = day.getUTCDate()
    const weekday = day.getUTCDay()
    if (!spec.months[month]) continue
    if (!cronDayMatches(spec, dayOfMonth, weekday)) continue
    let firstHour = 0
    let firstMinute = 0
    if (offset === 0) {
      firstHour = start.getUTCHours()
      firstMinute = start.getUTCMinutes() + 1
      if (firstMinute === 60) {
        firstMinute = 0
        firstHour += 1
      }
    }
    for (let hour = firstHour; hour < 24; hour += 1) {
      if (!spec.hours[hour]) continue
      for (let minute = 0; minute < 60; minute += 1) {
        if (hour === firstHour && minute < firstMinute) continue
        if (!spec.minutes[minute]) continue
        const candidate = Date.UTC(year, month - 1, dayOfMonth, hour, minute)
        if (candidate > afterMs) return new Date(candidate)
      }
    }
  }
  return null
}

function parseClock(value: string): number | null {
  const match = /^([01]\d|2[0-3]):([0-5]\d)$/.exec(value.trim())
  if (!match) return null
  return Number(match[1]) * 60 + Number(match[2])
}

/** Mirrors the daemon's cross-midnight window semantics in UTC. */
function windowContains(window: ScheduleWindow | null | undefined, at: Date): boolean {
  if (!window) return true
  const start = parseClock(window.start)
  const end = parseClock(window.end)
  if (start === null || end === null) return false
  const minutes = at.getUTCHours() * 60 + at.getUTCMinutes()
  const day = at.getUTCDay()
  const dayOk = (value: number) => window.days.length === 0 || window.days.includes(value)
  if (start < end) return dayOk(day) && minutes >= start && minutes < end
  if (dayOk(day) && minutes >= start) return true
  const yesterday = new Date(at.getTime() - DAY_MS)
  return dayOk(yesterday.getUTCDay()) && minutes < end
}

function previewRun(at: Date, window: ScheduleWindow | null | undefined): SchedulePreviewRun {
  const iso = at.toISOString()
  return { at: iso, utc: iso, inWindow: windowContains(window, at) }
}

export interface PreviewComputation {
  runs: SchedulePreviewRun[]
  warnings: string[]
}

/**
 * Pure preview mirroring `GET/POST /v1/schedules/preview`. Cron and windows are
 * evaluated in UTC; `once` and `interval` are absolute instants.
 */
export function computeSchedulePreview(
  trigger: ScheduleTrigger,
  window: ScheduleWindow | null | undefined,
  count = 5,
  from = Date.now(),
): PreviewComputation {
  const limit = Math.max(1, Math.min(50, Math.trunc(count) || 5))
  const runs: SchedulePreviewRun[] = []
  const warnings: string[] = []
  const fromMs = from

  if (trigger.kind === 'cron') {
    let spec: CronSpec
    try {
      spec = parseCronExpression(trigger.expr ?? '')
    } catch (cause) {
      throw new Error(cause instanceof Error ? cause.message : String(cause))
    }
    let cursor = new Date(fromMs)
    for (let index = 0; index < limit; index += 1) {
      let next = nextCronOccurrence(spec, cursor)
      if (!next) break
      if (window) {
        let steps = 0
        while (next && !windowContains(window, next) && steps < MAX_WINDOW_STEPS) {
          next = nextCronOccurrence(spec, next)
          steps += 1
        }
      }
      if (!next || !windowContains(window, next)) {
        if (runs.length === 0) warnings.push('window_unreachable')
        break
      }
      runs.push(previewRun(next, window))
      cursor = next
    }
    return { runs, warnings }
  }

  if (trigger.kind === 'interval') {
    const everyMs = Math.max(0, trigger.everySec ?? 0) * 1000
    if (everyMs <= 0) return { runs, warnings }
    let nextMs = fromMs + everyMs
    const horizon = fromMs + MAX_DAYS * DAY_MS
    for (let index = 0; index < limit; index += 1) {
      if (window) {
        let steps = 0
        while (!windowContains(window, new Date(nextMs)) && nextMs <= horizon && steps < MAX_WINDOW_STEPS) {
          nextMs += everyMs
          steps += 1
        }
      }
      if (nextMs > horizon || !windowContains(window, new Date(nextMs))) {
        if (runs.length === 0) warnings.push('window_unreachable')
        break
      }
      runs.push(previewRun(new Date(nextMs), window))
      nextMs += everyMs
    }
    return { runs, warnings }
  }

  if (trigger.kind === 'once') {
    const at = Date.parse(trigger.at ?? '')
    if (!Number.isFinite(at) || at <= fromMs) return { runs, warnings }
    if (window && !windowContains(window, new Date(at))) {
      warnings.push('window_unreachable')
      return { runs, warnings }
    }
    if (at <= fromMs + MAX_DAYS * DAY_MS) runs.push(previewRun(new Date(at), window))
    else warnings.push('window_unreachable')
    return { runs, warnings }
  }

  return { runs, warnings }
}

/** First future instant for a trigger, or undefined when nothing is reachable. */
export function nextRunAtFor(
  trigger: ScheduleTrigger,
  window: ScheduleWindow | null | undefined,
  from = Date.now(),
): string | undefined {
  try {
    return computeSchedulePreview(trigger, window, 1, from).runs[0]?.at
  } catch {
    return undefined
  }
}

/** Human interval unit; 3600 becomes "1h", 90 stays "90s". */
export function formatEvery(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '0s'
  if (seconds % 86_400 === 0) return `${seconds / 86_400}d`
  if (seconds % 3600 === 0) return `${seconds / 3600}h`
  if (seconds % 60 === 0) return `${seconds / 60}m`
  return `${seconds}s`
}

type Translate = (key: string, params?: Record<string, string | number>) => string

const ACTION_KEYS: Record<ScheduleAction, string> = {
  'account.switch': 'schedules.action.account.switch',
  'account.rotate': 'schedules.action.account.rotate',
  'account.reconnect': 'schedules.action.account.reconnect',
  'account.login': 'schedules.action.account.login',
  'account.logout': 'schedules.action.account.logout',
  'node.drain': 'schedules.action.node.drain',
  'node.undrain': 'schedules.action.node.undrain',
  'lease.rotate': 'schedules.action.lease.rotate',
  'subscription.refresh': 'schedules.action.subscription.refresh',
  'egress.check': 'schedules.action.egress.check',
  'account.renewal_reminder': 'schedules.action.account.renewal_reminder',
}

export function scheduleActionLabel(action: ScheduleAction, t: Translate): string {
  const key = ACTION_KEYS[action]
  return key ? t(key) : t('schedules.action.other', { action })
}

/** The actions the P0 daemon accepts on POST/PATCH. */
export const CREATABLE_ACTIONS: ScheduleAction[] = ['account.rotate', 'account.reconnect']

/** Destructive actions require `confirm:"ARM"` when dryRun is off. */
export function isDestructiveAction(action: ScheduleAction): boolean {
  return action !== 'account.renewal_reminder'
}

export function describeTrigger(trigger: ScheduleTrigger, t: Translate): string {
  const zone = trigger.timezone && trigger.timezone !== 'UTC' ? ` · ${trigger.timezone}` : ''
  if (trigger.kind === 'cron') {
    return `${t('schedules.trigger.cron')} · ${trigger.expr ?? ''}${zone}`
  }
  if (trigger.kind === 'interval') {
    return `${t('schedules.trigger.every', { value: formatEvery(trigger.everySec ?? 0) })}${zone}`
  }
  if (trigger.kind === 'once') {
    const at = trigger.at ? new Date(trigger.at).toISOString() : ''
    return `${t('schedules.trigger.at', { time: at ? formatInstant(at) : '—' })}${zone}`
  }
  return t('schedules.trigger.unknown')
}

/** Stable local date/time without the year; ISO strings are the API shape. */
export function formatInstant(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('en-GB', {
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

export function lastResultTone(result: ScheduleResult | undefined): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  switch (result) {
    case 'ok':
      return 'success'
    case 'skipped':
      return 'neutral'
    case 'error':
      return 'danger'
    case 'dry_run':
      return 'info'
    default:
      return 'warning'
  }
}

export function runStateTone(state: string): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  switch (state) {
    case 'succeeded':
      return 'success'
    case 'skipped':
      return 'neutral'
    case 'failed':
      return 'danger'
    case 'dry_run':
      return 'info'
    case 'running':
      return 'info'
    default:
      return 'warning'
  }
}

export function runStateKey(state: string): string {
  const known = ['running', 'succeeded', 'failed', 'skipped', 'dry_run', 'unknown']
  return known.includes(state) ? `schedules.history.state.${state}` : 'schedules.history.state.unknown'
}

export function errorClassKey(errorClass: string | undefined): string {
  if (!errorClass) return ''
  return `schedules.history.errorClass.${errorClass}`
}

export function runReasonKey(reason: string | undefined): string {
  if (!reason) return ''
  return `schedules.history.reason.${reason}`
}

/** Duration between startedAt and finishedAt, rendered in s or ms. */
export function runDuration(run: { startedAt: string; finishedAt?: string }): string {
  if (!run.finishedAt) return '—'
  const ms = new Date(run.finishedAt).getTime() - new Date(run.startedAt).getTime()
  if (!Number.isFinite(ms) || ms < 0) return '—'
  return ms < 1000 ? `${Math.round(ms)} ms` : `${(ms / 1000).toFixed(1)} s`
}

export function triggerKindLabel(kind: ScheduleTrigger['kind'], t: Translate): string {
  if (kind === 'cron') return t('schedules.trigger.cron')
  if (kind === 'interval') return t('schedules.trigger.interval')
  if (kind === 'once') return t('schedules.trigger.once')
  return t('schedules.trigger.unknown')
}

/** True when a schedule's target is a system-owned pseudo schedule. */
export function isSystemSchedule(schedule: Pick<Schedule, 'id'>): boolean {
  return schedule.id.startsWith('system:')
}

export const SYSTEM_SCHEDULE_ID = 'system:renewal-sweep'

/** Renewal window used by the daemon's reminder sweep (five days). */
export const RENEWAL_WINDOW_DAYS = 5

export interface RenewalReminder {
  accountId: string
  label: string
  expiresAt: string
  /** Whole days; negative when overdue. */
  days: number
  overdue: boolean
}

/** Read-only reminder marks derived from account expiry, never written back. */
export function renewalReminders(
  accounts: ReadonlyArray<{ id: string; label: string; expiresAt: string }>,
  now = Date.now(),
): RenewalReminder[] {
  return accounts
    .map((account) => {
      const expires = Date.parse(account.expiresAt)
      if (!Number.isFinite(expires)) return null
      const remaining = expires - now
      if (remaining > RENEWAL_WINDOW_DAYS * DAY_MS) return null
      const overdue = remaining <= 0
      const days = overdue
        ? Math.floor((now - expires) / DAY_MS)
        : Math.ceil(remaining / DAY_MS)
      return {
        accountId: account.id,
        label: account.label,
        expiresAt: account.expiresAt,
        days: days === 0 ? 0 : days,
        overdue,
      }
    })
    .filter((item): item is RenewalReminder => item !== null)
    .sort((left, right) => left.days - right.days)
}
