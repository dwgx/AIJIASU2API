import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { api, ScheduleActionError } from '../../lib/api'
import { ApiError } from '../../lib/errors'
import { useI18n } from '../../lib/i18n'
import {
  CREATABLE_ACTIONS,
  formatInstant,
  isDestructiveAction,
  scheduleActionLabel,
  triggerKindLabel,
} from '../../lib/schedules'
import type {
  Account,
  ConcurrencyPolicy,
  MisfirePolicy,
  Schedule,
  ScheduleAction,
  ScheduleCreateInput,
  SchedulePatchInput,
  SchedulePreview,
  ScheduleTarget,
  ScheduleTrigger,
  ScheduleTriggerKind,
  ScheduleWindow,
} from '../../lib/types'
import { Modal } from '../Overlay'
import {
  Badge,
  Button,
  Checkbox,
  Field,
  Input,
  SegmentedControl,
  Select,
} from '../primitives'
import './schedules.css'

type DialogMode = 'create' | 'edit'

interface ScheduleFormState {
  name: string
  action: ScheduleAction
  accountId: string
  strategy: 'pool' | 'same-region' | 'next-server' | 'random'
  serverIds: string[]
  region: string
  excludeCurrent: boolean
  drainSeconds: string
  timeoutSeconds: string
  triggerKind: ScheduleTriggerKind
  cronExpr: string
  everySec: string
  jitterSec: string
  atLocal: string
  timezone: string
  windowEnabled: boolean
  windowDays: number[]
  windowStart: string
  windowEnd: string
  cooldownSec: string
  maxRunsPerDay: string
  dryRun: boolean
  misfirePolicy: MisfirePolicy
  misfireGraceSec: string
  concurrencyPolicy: ConcurrencyPolicy
  enabled: boolean
}

type Translate = (key: string, params?: Record<string, string | number>) => string

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

/** `datetime-local` wants a local wall clock; the API wants RFC 3339. */
function toLocalInput(iso: string | undefined): string {
  if (!iso) return ''
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ''
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`
}

function defaultAtLocal(): string {
  return toLocalInput(new Date(Date.now() + 3_600_000).toISOString())
}

function numberValue(value: string, fallback = 0): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}

function createForm(accounts: Account[]): ScheduleFormState {
  return {
    name: '',
    action: 'account.rotate',
    accountId: accounts[0]?.id ?? '',
    strategy: 'same-region',
    serverIds: [],
    region: '',
    excludeCurrent: true,
    drainSeconds: '0',
    timeoutSeconds: '0',
    triggerKind: 'cron',
    cronExpr: '0 3 * * *',
    everySec: '3600',
    jitterSec: '0',
    atLocal: defaultAtLocal(),
    timezone: 'UTC',
    windowEnabled: false,
    windowDays: [],
    windowStart: '08:00',
    windowEnd: '18:00',
    cooldownSec: '600',
    maxRunsPerDay: '24',
    dryRun: true,
    misfirePolicy: 'skip',
    misfireGraceSec: '300',
    concurrencyPolicy: 'forbid',
    enabled: false,
  }
}

function formFromSchedule(schedule: Schedule): ScheduleFormState {
  const params = (schedule.target.params ?? {}) as Record<string, unknown>
  const strategy =
    params.strategy === 'pool' ||
    params.strategy === 'same-region' ||
    params.strategy === 'next-server' ||
    params.strategy === 'random'
      ? params.strategy
      : 'same-region'
  const serverIds = Array.isArray(params.serverIds)
    ? params.serverIds.filter((value): value is string => typeof value === 'string')
    : []
  return {
    name: schedule.name,
    action: schedule.action,
    accountId: schedule.target.id ?? '',
    strategy,
    serverIds,
    region: typeof params.region === 'string' ? params.region : '',
    excludeCurrent: params.excludeCurrent !== false,
    drainSeconds: String(params.drainSeconds ?? 0),
    timeoutSeconds: String(params.timeoutSeconds ?? 0),
    triggerKind: schedule.trigger.kind,
    cronExpr: schedule.trigger.expr ?? '0 3 * * *',
    everySec: String(schedule.trigger.everySec ?? 3600),
    jitterSec: String(schedule.trigger.jitterSec ?? 0),
    atLocal: toLocalInput(schedule.trigger.at) || defaultAtLocal(),
    timezone: schedule.trigger.timezone || 'UTC',
    windowEnabled: Boolean(schedule.window),
    windowDays: schedule.window?.days ?? [],
    windowStart: schedule.window?.start ?? '08:00',
    windowEnd: schedule.window?.end ?? '18:00',
    cooldownSec: String(schedule.guard.cooldownSec),
    maxRunsPerDay: String(schedule.guard.maxRunsPerDay),
    dryRun: schedule.guard.dryRun,
    misfirePolicy: schedule.misfirePolicy ?? 'skip',
    misfireGraceSec: String(schedule.misfireGraceSec ?? 300),
    concurrencyPolicy: schedule.concurrencyPolicy ?? 'forbid',
    enabled: schedule.enabled,
  }
}

function buildTrigger(form: ScheduleFormState): ScheduleTrigger {
  const timezone = form.timezone.trim()
  if (form.triggerKind === 'cron') {
    return {
      kind: 'cron',
      expr: form.cronExpr.trim(),
      ...(timezone ? { timezone } : {}),
      ...(numberValue(form.jitterSec) > 0 ? { jitterSec: numberValue(form.jitterSec) } : {}),
    }
  }
  if (form.triggerKind === 'interval') {
    return {
      kind: 'interval',
      everySec: numberValue(form.everySec),
      ...(timezone ? { timezone } : {}),
      ...(numberValue(form.jitterSec) > 0 ? { jitterSec: numberValue(form.jitterSec) } : {}),
    }
  }
  const at = form.atLocal ? new Date(form.atLocal) : new Date(Number.NaN)
  if (Number.isNaN(at.getTime())) throw new Error('invalid once time')
  return { kind: 'once', at: at.toISOString(), ...(timezone ? { timezone } : {}) }
}

function buildTarget(form: ScheduleFormState): ScheduleTarget {
  if (form.action === 'account.reconnect') {
    const timeout = numberValue(form.timeoutSeconds)
    return {
      kind: 'account',
      id: form.accountId,
      params: timeout > 0 ? { timeoutSeconds: timeout } : {},
    }
  }
  const params: Record<string, unknown> = {
    strategy: form.strategy,
    excludeCurrent: form.excludeCurrent,
    rollback: true,
  }
  if (form.strategy === 'pool') params.serverIds = [...form.serverIds]
  if (form.strategy === 'same-region' && form.region.trim()) params.region = form.region.trim()
  const drain = numberValue(form.drainSeconds)
  if (drain > 0) params.drainSeconds = drain
  const timeout = numberValue(form.timeoutSeconds)
  if (timeout > 0) params.timeoutSeconds = timeout
  return { kind: 'account', id: form.accountId, params }
}

function buildWindow(form: ScheduleFormState): ScheduleWindow | undefined {
  if (!form.windowEnabled) return undefined
  return {
    days: [...form.windowDays].sort((left, right) => left - right),
    start: form.windowStart,
    end: form.windowEnd,
  }
}

function validateForm(form: ScheduleFormState, t: Translate): string | null {
  if (!form.name.trim()) return t('schedules.dialog.nameRequired')
  if (!form.accountId) return t('schedules.dialog.accountRequired')
  if (form.triggerKind === 'cron' && !form.cronExpr.trim()) {
    return t('schedules.dialog.cronRequired')
  }
  if (form.triggerKind === 'interval') {
    const every = numberValue(form.everySec)
    if (every < 60 || every > 2_592_000) return t('schedules.dialog.everyInvalid')
    const maxJitter = Math.min(300, Math.floor(every / 2))
    const jitter = numberValue(form.jitterSec)
    if (jitter < 0 || jitter > maxJitter) return t('schedules.dialog.jitterInvalid')
  }
  if (form.triggerKind === 'cron') {
    const jitter = numberValue(form.jitterSec)
    if (jitter < 0 || jitter > 300) return t('schedules.dialog.jitterInvalid')
  }
  if (form.triggerKind === 'once') {
    const at = form.atLocal ? new Date(form.atLocal).getTime() : Number.NaN
    if (!Number.isFinite(at) || at <= Date.now() || at > Date.now() + 366 * 86_400_000) {
      return t('schedules.dialog.atInvalid')
    }
  }
  if (form.action === 'account.rotate' && form.strategy === 'pool' && form.serverIds.length < 2) {
    return t('schedules.dialog.poolRequired')
  }
  if (form.action === 'account.rotate') {
    const drain = numberValue(form.drainSeconds)
    if (drain < 0 || drain > 60) return t('schedules.dialog.drainInvalid')
  }
  const timeout = numberValue(form.timeoutSeconds)
  if (timeout !== 0 && (timeout < 10 || timeout > 120)) {
    return t('schedules.dialog.timeoutInvalid')
  }
  if (form.windowEnabled && form.windowStart === form.windowEnd) {
    return t('schedules.dialog.windowInvalid')
  }
  const cooldown = numberValue(form.cooldownSec)
  if (cooldown < 0 || cooldown > 86_400) return t('schedules.dialog.cooldownInvalid')
  const maxRuns = numberValue(form.maxRunsPerDay)
  if (maxRuns < 1 || maxRuns > 1_000) return t('schedules.dialog.maxRunsInvalid')
  const grace = numberValue(form.misfireGraceSec)
  if (grace < 0 || grace > 3_600) return t('schedules.dialog.graceInvalid')
  return null
}

export interface ScheduleDialogProps {
  open: boolean
  mode: DialogMode
  schedule?: Schedule
  accounts: Account[]
  onClose: () => void
  onSaved: (schedule: Schedule, mode: DialogMode, retried: boolean) => void
  onSchedulerOff: () => void
}

/**
 * Create/edit form. The preview runs against the daemon's pure endpoint on a
 * short debounce; saving sends If-Match and retries exactly once on a 412.
 */
export function ScheduleDialog({
  open,
  mode,
  schedule,
  accounts,
  onClose,
  onSaved,
  onSchedulerOff,
}: ScheduleDialogProps) {
  const { t } = useI18n()
  const baseId = useId()
  const [form, setForm] = useState<ScheduleFormState>(() => createForm(accounts))
  const [formError, setFormError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [preview, setPreview] = useState<{
    status: 'idle' | 'loading' | 'ready' | 'error'
    data?: SchedulePreview
    error?: string
  }>({ status: 'idle' })
  const previewEpoch = useRef(0)
  const [serverCatalog, setServerCatalog] = useState<{
    status: 'idle' | 'loading' | 'ready' | 'error'
    rows: Array<{ id: string; title: string; region: string; host: string; eligible: boolean }>
    error?: string
    reason?: string
  }>({ status: 'idle', rows: [] })

  useEffect(() => {
    if (!open) return
    setForm(schedule ? formFromSchedule(schedule) : createForm(accounts))
    setFormError(null)
    setSaving(false)
    setPreview({ status: 'idle' })
    // Accounts are read through the render closure; polling must not reset an open form.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, mode, schedule?.id, schedule?.revision])

  // Accounts can arrive after the dialog mounts in a deep link.
  useEffect(() => {
    if (!open) return
    setForm((current) => (current.accountId ? current : { ...current, accountId: accounts[0]?.id ?? '' }))
  }, [open, accounts])

  useEffect(() => {
    if (!open || form.action !== 'account.rotate' || !form.accountId) {
      setServerCatalog({ status: 'idle', rows: [] })
      return
    }
    let cancelled = false
    setServerCatalog({ status: 'loading', rows: [] })
    api
      .getAccountServers(form.accountId, { limit: 500 })
      .then((catalog) => {
        if (cancelled) return
        setServerCatalog({
          status: 'ready',
          reason: catalog.enabled === false ? catalog.reason : undefined,
          rows: catalog.servers.map((row) => ({
            id: row.id,
            title: row.title,
            region: row.region,
            host: row.host,
            eligible:
              row.eligible ??
              (row.status === 'ok' &&
                row.fullLoad !== true &&
                row.blockingMarks.length === 0 &&
                row.memberOnly !== true),
          })),
        })
      })
      .catch((cause: unknown) => {
        if (cancelled) return
        setServerCatalog({
          status: 'error',
          rows: [],
          error: cause instanceof Error ? cause.message : String(cause),
        })
      })
    return () => {
      cancelled = true
    }
  }, [open, form.action, form.accountId])

  const serverOptions = useMemo(() => {
    const options = serverCatalog.rows.map((row) => ({
      value: row.id,
      label: row.title,
      description: `${row.region} · ${row.host}${row.eligible ? '' : ` · ${t('schedules.dialog.ineligible')}`}`,
      disabled: !row.eligible,
    }))
    for (const id of form.serverIds) {
      if (!options.some((option) => option.value === id)) {
        options.push({ value: id, label: id, description: t('schedules.dialog.staleServer'), disabled: true })
      }
    }
    return options
  }, [serverCatalog.rows, form.serverIds, t])

  const previewKey = [
    form.action,
    form.accountId,
    form.triggerKind,
    form.cronExpr,
    form.everySec,
    form.jitterSec,
    form.atLocal,
    form.timezone,
    form.windowEnabled,
    form.windowDays.join(','),
    form.windowStart,
    form.windowEnd,
  ].join('|')

  useEffect(() => {
    if (!open) return
    const epoch = ++previewEpoch.current
    const timer = window.setTimeout(() => {
      let trigger: ScheduleTrigger
      try {
        trigger = buildTrigger(form)
      } catch {
        if (epoch === previewEpoch.current) setPreview({ status: 'idle' })
        return
      }
      if (trigger.kind === 'cron' && !trigger.expr) {
        if (epoch === previewEpoch.current) setPreview({ status: 'idle' })
        return
      }
      setPreview({ status: 'loading' })
      api
        .previewSchedule(
          { action: form.action, target: buildTarget(form), trigger, window: buildWindow(form) },
          5,
        )
        .then((data) => {
          if (epoch === previewEpoch.current) setPreview({ status: 'ready', data })
        })
        .catch((cause: unknown) => {
          if (epoch !== previewEpoch.current) return
          setPreview({
            status: 'error',
            error: cause instanceof Error ? cause.message : String(cause),
          })
        })
    }, 250)
    return () => window.clearTimeout(timer)
    // The key covers every input the preview reads.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, previewKey])

  const update = <K extends keyof ScheduleFormState>(key: K, value: ScheduleFormState[K]) => {
    setForm((current) => ({ ...current, [key]: value }))
  }

  const changeAccount = (accountId: string) => {
    setForm((current) => ({ ...current, accountId, serverIds: [] }))
  }

  const changeTriggerKind = (kind: ScheduleTriggerKind) => {
    setForm((current) => ({
      ...current,
      triggerKind: kind,
      misfirePolicy: kind === 'once' && current.misfirePolicy === 'run_once' ? 'skip' : current.misfirePolicy,
      atLocal: kind === 'once' && !current.atLocal ? defaultAtLocal() : current.atLocal,
    }))
  }

  const toggleDay = (day: number, checked: boolean) => {
    setForm((current) => ({
      ...current,
      windowDays: checked
        ? [...current.windowDays, day].sort((left, right) => left - right)
        : current.windowDays.filter((value) => value !== day),
    }))
  }

  const describeSaveError = (error: unknown): string => {
    if (error instanceof ScheduleActionError) {
      if (error.status === 412) return t('schedules.staleFailed')
      if (error.reason === 'confirmation_required') return t('schedules.confirmRequired')
      if (error.reason === 'in_flight') return t('schedules.inFlight')
      if (error.status === 503) {
        onSchedulerOff()
        return t('schedules.schedulerOff')
      }
      if (error.status === 429 && error.retryAfterSec) {
        return `${error.message} ${t('schedules.retryAfter', { seconds: error.retryAfterSec })}`
      }
      return error.message
    }
    if (error instanceof ApiError) {
      if (error.status === 503) {
        onSchedulerOff()
        return t('schedules.schedulerOff')
      }
      return error.message
    }
    return error instanceof Error ? error.message : t('common.unknownError')
  }

  const save = async () => {
    const validation = validateForm(form, t)
    if (validation) {
      setFormError(validation)
      return
    }
    setSaving(true)
    setFormError(null)
    try {
      const trigger = buildTrigger(form)
      const target = buildTarget(form)
      const window = buildWindow(form)
      const guard = {
        cooldownSec: numberValue(form.cooldownSec),
        maxRunsPerDay: numberValue(form.maxRunsPerDay),
        dryRun: form.dryRun,
      }
      const common = {
        name: form.name.trim(),
        action: form.action,
        target,
        trigger,
        ...(window ? { window } : {}),
        guard,
        misfirePolicy: form.misfirePolicy,
        misfireGraceSec: numberValue(form.misfireGraceSec),
        concurrencyPolicy: form.concurrencyPolicy,
        ...(isDestructiveAction(form.action) && !form.dryRun ? { confirm: 'ARM' as const } : {}),
      }
      if (mode === 'create') {
        const created = await api.createSchedule({ ...common, enabled: form.enabled } as ScheduleCreateInput)
        onSaved(created, 'create', false)
        return
      }
      if (!schedule) return
      let saved: Schedule | null = null
      let retried = false
      try {
        saved = await api.patchSchedule(schedule.id, schedule.revision, {
          ...common,
          enabled: form.enabled,
        } as SchedulePatchInput)
      } catch (error) {
        if (!(error instanceof ScheduleActionError) || error.status !== 412) throw error
        const latest = await api.getSchedule(schedule.id)
        saved = await api.patchSchedule(latest.id, latest.revision, {
          ...common,
          enabled: form.enabled,
        } as SchedulePatchInput)
        retried = true
      }
      if (saved) onSaved(saved, 'edit', retried)
    } catch (error) {
      setFormError(describeSaveError(error))
    } finally {
      setSaving(false)
    }
  }

  const actionOptions = useMemo(() => {
    const values = [...CREATABLE_ACTIONS]
    if (!values.includes(form.action)) values.unshift(form.action)
    return values.map((value) => ({ value, label: scheduleActionLabel(value, t) }))
  }, [form.action, t])

  const accountOptions = useMemo(
    () =>
      accounts.map((account) => ({
        value: account.id,
        label: account.label,
        description: account.username,
      })),
    [accounts],
  )

  const windowLocked = mode === 'edit' && Boolean(schedule?.window)
  const armed = isDestructiveAction(form.action) && !form.dryRun
  const title = mode === 'create' ? t('schedules.dialog.createTitle') : t('schedules.dialog.editTitle')
  const description =
    mode === 'create' ? t('schedules.dialog.createDesc') : t('schedules.dialog.editDesc')

  return (
    <Modal
      open={open}
      title={title}
      description={description}
      onClose={saving ? () => {} : onClose}
      size="md"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" onClick={() => void save()} loading={saving}>
            {mode === 'create' ? t('schedules.dialog.save') : t('schedules.dialog.saveChanges')}
          </Button>
        </>
      }
    >
      <div className="form schedule-form">
        {formError ? (
          <p className="form-error" role="alert">
            {formError}
          </p>
        ) : null}

        <div className="form-grid">
          <Field
            label={t('schedules.dialog.name')}
            htmlFor={`${baseId}-name`}
            hint={t('schedules.dialog.nameHint')}
          >
            <Input
              id={`${baseId}-name`}
              value={form.name}
              maxLength={60}
              autoComplete="off"
              onChange={(event) => update('name', event.target.value)}
            />
          </Field>
          <Field label={t('schedules.dialog.action')} htmlFor={`${baseId}-action`}>
            <Select
              id={`${baseId}-action`}
              aria-label={t('schedules.dialog.action')}
              value={form.action}
              options={actionOptions}
              onChange={(value) => update('action', value as ScheduleAction)}
            />
          </Field>
        </div>

        <Field label={t('schedules.dialog.account')} htmlFor={`${baseId}-account`}>
          <Select
            id={`${baseId}-account`}
            aria-label={t('schedules.dialog.account')}
            value={form.accountId}
            options={accountOptions}
            placeholder={t('schedules.dialog.accountRequired')}
            onChange={changeAccount}
            disabled={accounts.length === 0}
          />
        </Field>

        {form.action === 'account.rotate' ? (
          <section className="schedule-form__section" aria-label={t('schedules.dialog.strategy')}>
            <h3 className="schedule-form__heading">{t('schedules.dialog.strategy')}</h3>
            <div className="form-grid">
              <Field label={t('schedules.dialog.strategy')} htmlFor={`${baseId}-strategy`}>
                <Select
                  id={`${baseId}-strategy`}
                  aria-label={t('schedules.dialog.strategy')}
                  value={form.strategy}
                  onChange={(value) =>
                    update('strategy', value as ScheduleFormState['strategy'])
                  }
                  options={[
                    { value: 'same-region', label: t('schedules.dialog.strategy.same-region') },
                    { value: 'pool', label: t('schedules.dialog.strategy.pool') },
                    { value: 'next-server', label: t('schedules.dialog.strategy.next-server') },
                    { value: 'random', label: t('schedules.dialog.strategy.random') },
                  ]}
                />
              </Field>
              <Field label={t('schedules.dialog.region')} htmlFor={`${baseId}-region`}>
                <Input
                  id={`${baseId}-region`}
                  value={form.region}
                  placeholder="cn-zj"
                  onChange={(event) => update('region', event.target.value)}
                  disabled={form.strategy !== 'same-region'}
                />
              </Field>
            </div>
            {form.strategy === 'pool' ? (
              <Field
                label={t('schedules.dialog.serverPool')}
                hint={
                  serverCatalog.status === 'error'
                    ? serverCatalog.error
                    : (serverCatalog.reason ?? t('schedules.dialog.serverPoolHint'))
                }
              >
                <Select
                  multiple
                  searchable
                  aria-label={t('schedules.dialog.serverPool')}
                  value={form.serverIds}
                  options={serverOptions}
                  onChange={(value) => update('serverIds', value)}
                  disabled={serverCatalog.status === 'loading'}
                />
              </Field>
            ) : null}
            <div className="form-grid">
              <Field label={t('schedules.dialog.drainSeconds')} htmlFor={`${baseId}-drain`}>
                <Input
                  id={`${baseId}-drain`}
                  type="number"
                  min={0}
                  max={60}
                  value={form.drainSeconds}
                  onChange={(event) => update('drainSeconds', event.target.value)}
                />
              </Field>
              <Field label={t('schedules.dialog.timeoutSeconds')} htmlFor={`${baseId}-timeout`}>
                <Input
                  id={`${baseId}-timeout`}
                  type="number"
                  min={0}
                  max={120}
                  value={form.timeoutSeconds}
                  onChange={(event) => update('timeoutSeconds', event.target.value)}
                />
              </Field>
            </div>
            <Checkbox
              label={t('schedules.dialog.excludeCurrent')}
              checked={form.excludeCurrent}
              onChange={(event) => update('excludeCurrent', event.target.checked)}
            />
          </section>
        ) : (
          <Field label={t('schedules.dialog.timeoutSeconds')} htmlFor={`${baseId}-timeout-reconnect`}>
            <Input
              id={`${baseId}-timeout-reconnect`}
              type="number"
              min={0}
              max={120}
              value={form.timeoutSeconds}
              onChange={(event) => update('timeoutSeconds', event.target.value)}
            />
          </Field>
        )}

        <section className="schedule-form__section" aria-label={t('schedules.dialog.triggerMode')}>
          <h3 className="schedule-form__heading">{t('schedules.dialog.triggerMode')}</h3>
          <SegmentedControl
            label={t('schedules.dialog.triggerMode')}
            value={form.triggerKind}
            onChange={changeTriggerKind}
            options={[
              { value: 'cron', label: triggerKindLabel('cron', t) },
              { value: 'interval', label: triggerKindLabel('interval', t) },
              { value: 'once', label: triggerKindLabel('once', t) },
            ]}
          />
          <div className="form-grid">
            {form.triggerKind === 'cron' ? (
              <Field
                label={t('schedules.dialog.cron')}
                htmlFor={`${baseId}-cron`}
                hint={t('schedules.dialog.cronHint')}
              >
                <Input
                  id={`${baseId}-cron`}
                  value={form.cronExpr}
                  spellCheck={false}
                  className="mono"
                  onChange={(event) => update('cronExpr', event.target.value)}
                />
              </Field>
            ) : null}
            {form.triggerKind === 'interval' ? (
              <Field
                label={t('schedules.dialog.every')}
                htmlFor={`${baseId}-every`}
                hint={t('schedules.dialog.everyHint')}
              >
                <Input
                  id={`${baseId}-every`}
                  type="number"
                  min={60}
                  max={2_592_000}
                  value={form.everySec}
                  onChange={(event) => update('everySec', event.target.value)}
                />
              </Field>
            ) : null}
            {form.triggerKind === 'once' ? (
              <Field
                label={t('schedules.dialog.at')}
                htmlFor={`${baseId}-at`}
                hint={t('schedules.dialog.atHint')}
              >
                <Input
                  id={`${baseId}-at`}
                  type="datetime-local"
                  value={form.atLocal}
                  onChange={(event) => update('atLocal', event.target.value)}
                />
              </Field>
            ) : null}
            <Field
              label={t('schedules.dialog.timezone')}
              htmlFor={`${baseId}-timezone`}
              hint={t('schedules.dialog.timezoneHint')}
            >
              <Input
                id={`${baseId}-timezone`}
                value={form.timezone}
                placeholder="UTC"
                spellCheck={false}
                onChange={(event) => update('timezone', event.target.value)}
              />
            </Field>
            {form.triggerKind !== 'once' ? (
              <Field
                label={t('schedules.dialog.jitter')}
                htmlFor={`${baseId}-jitter`}
                hint={t('schedules.dialog.jitterHint')}
              >
                <Input
                  id={`${baseId}-jitter`}
                  type="number"
                  min={0}
                  max={300}
                  value={form.jitterSec}
                  onChange={(event) => update('jitterSec', event.target.value)}
                />
              </Field>
            ) : null}
          </div>
        </section>

        <section className="schedule-form__section" aria-label={t('schedules.dialog.window')}>
          <h3 className="schedule-form__heading">{t('schedules.dialog.window')}</h3>
          <Checkbox
            label={t('schedules.dialog.windowEnable')}
            checked={form.windowEnabled}
            disabled={windowLocked}
            onChange={(event) => update('windowEnabled', event.target.checked)}
          />
          {windowLocked ? <p className="form-note">{t('schedules.dialog.windowLocked')}</p> : null}
          {form.windowEnabled ? (
            <>
              <div className="schedule-form__days" role="group" aria-label={t('schedules.dialog.windowDays')}>
                {[0, 1, 2, 3, 4, 5, 6].map((day) => (
                  <Checkbox
                    key={day}
                    label={t(`schedules.days.${day}`)}
                    checked={form.windowDays.includes(day)}
                    onChange={(event) => toggleDay(day, event.target.checked)}
                  />
                ))}
              </div>
              <div className="form-grid">
                <Field label={t('schedules.dialog.windowStart')} htmlFor={`${baseId}-window-start`}>
                  <Input
                    id={`${baseId}-window-start`}
                    type="time"
                    value={form.windowStart}
                    onChange={(event) => update('windowStart', event.target.value)}
                  />
                </Field>
                <Field label={t('schedules.dialog.windowEnd')} htmlFor={`${baseId}-window-end`}>
                  <Input
                    id={`${baseId}-window-end`}
                    type="time"
                    value={form.windowEnd}
                    onChange={(event) => update('windowEnd', event.target.value)}
                  />
                </Field>
              </div>
              <p className="form-note">{t('schedules.dialog.windowHint')}</p>
            </>
          ) : null}
        </section>

        <section className="schedule-form__section" aria-label={t('schedules.dialog.guard')}>
          <h3 className="schedule-form__heading">{t('schedules.dialog.guard')}</h3>
          <div className="form-grid">
            <Field label={t('schedules.dialog.cooldown')} htmlFor={`${baseId}-cooldown`}>
              <Input
                id={`${baseId}-cooldown`}
                type="number"
                min={0}
                max={86_400}
                value={form.cooldownSec}
                onChange={(event) => update('cooldownSec', event.target.value)}
              />
            </Field>
            <Field label={t('schedules.dialog.maxRuns')} htmlFor={`${baseId}-max-runs`}>
              <Input
                id={`${baseId}-max-runs`}
                type="number"
                min={1}
                max={1_000}
                value={form.maxRunsPerDay}
                onChange={(event) => update('maxRunsPerDay', event.target.value)}
              />
            </Field>
            <Field label={t('schedules.dialog.misfire')} htmlFor={`${baseId}-misfire`}>
              <Select
                id={`${baseId}-misfire`}
                aria-label={t('schedules.dialog.misfire')}
                value={form.misfirePolicy}
                onChange={(value) => update('misfirePolicy', value as MisfirePolicy)}
                options={[
                  { value: 'skip', label: t('schedules.dialog.misfire.skip') },
                  {
                    value: 'run_once',
                    label: t('schedules.dialog.misfire.run_once'),
                    disabled: form.triggerKind === 'once',
                  },
                ]}
              />
            </Field>
            <Field label={t('schedules.dialog.misfireGrace')} htmlFor={`${baseId}-grace`}>
              <Input
                id={`${baseId}-grace`}
                type="number"
                min={0}
                max={3_600}
                value={form.misfireGraceSec}
                onChange={(event) => update('misfireGraceSec', event.target.value)}
              />
            </Field>
            <Field label={t('schedules.dialog.concurrency')} htmlFor={`${baseId}-concurrency`}>
              <Select
                id={`${baseId}-concurrency`}
                aria-label={t('schedules.dialog.concurrency')}
                value={form.concurrencyPolicy}
                onChange={(value) => update('concurrencyPolicy', value as ConcurrencyPolicy)}
                options={[
                  { value: 'forbid', label: t('schedules.dialog.concurrency.forbid') },
                  { value: 'queue', label: t('schedules.dialog.concurrency.queue') },
                ]}
              />
            </Field>
          </div>
          <Checkbox
            label={t('schedules.dialog.dryRun')}
            checked={form.dryRun}
            onChange={(event) => update('dryRun', event.target.checked)}
          />
          <p className="form-note">{t('schedules.dialog.dryRunHint')}</p>
          {armed ? (
            <p className="form-note schedule-form__arm" role="status">
              {t('schedules.dialog.armWarning')}
            </p>
          ) : null}
        </section>

        <section className="schedule-form__section" aria-label={t('schedules.dialog.preview')}>
          <h3 className="schedule-form__heading">{t('schedules.dialog.preview')}</h3>
          <p className="schedule-form__hint">{t('schedules.dialog.previewDesc')}</p>
          {preview.status === 'loading' ? (
            <p className="schedule-form__hint" role="status">
              {t('schedules.dialog.previewLoading')}
            </p>
          ) : null}
          {preview.status === 'error' ? (
            <p className="form-error" role="alert">
              {t('schedules.dialog.previewError', { message: preview.error ?? '' })}
            </p>
          ) : null}
          {preview.status === 'ready' && preview.data ? (
            <>
              {preview.data.runs.length === 0 ? (
                <p className="schedule-form__hint">{t('schedules.dialog.previewEmpty')}</p>
              ) : (
                <ol className="schedule-preview__list" aria-label={t('schedules.dialog.preview')}>
                  {preview.data.runs.map((run, index) => (
                    <li className="schedule-preview__row" key={`${run.utc}-${index}`}>
                      <span className="schedule-preview__index mono">
                        {String(index + 1).padStart(2, '0')}
                      </span>
                      <time className="schedule-preview__time" dateTime={run.at}>
                        {formatInstant(run.at)}
                      </time>
                      <span className="schedule-preview__utc mono">{run.utc}</span>
                      <Badge tone={run.inWindow ? 'success' : 'warning'}>
                        {run.inWindow
                          ? t('schedules.dialog.previewInWindow')
                          : t('schedules.dialog.previewOutWindow')}
                      </Badge>
                    </li>
                  ))}
                </ol>
              )}
              {preview.data.warnings.length > 0 ? (
                <ul className="schedule-preview__warnings" aria-label={t('schedules.preview.warnings')}>
                  {preview.data.warnings.map((warning) => (
                    <li key={warning}>
                      {t(`schedules.dialog.previewWarning.${warning}`) ===
                      `schedules.dialog.previewWarning.${warning}`
                        ? warning
                        : t(`schedules.dialog.previewWarning.${warning}`)}
                    </li>
                  ))}
                </ul>
              ) : null}
            </>
          ) : null}
        </section>

        <Checkbox
          label={t('schedules.dialog.enabled')}
          checked={form.enabled}
          onChange={(event) => update('enabled', event.target.checked)}
        />
      </div>
    </Modal>
  )
}
