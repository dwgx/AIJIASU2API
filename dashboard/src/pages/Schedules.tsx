import { CalendarClock, History, Pencil, Play, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, ScheduleActionError } from '../lib/api'
import { ApiError } from '../lib/errors'
import { formatDateTimeSeconds, formatRelative } from '../lib/format'
import { useI18n } from '../lib/i18n'
import {
  SYSTEM_SCHEDULE_ID,
  describeTrigger,
  formatInstant,
  isSystemSchedule,
  lastResultTone,
  runReasonKey,
  runStateKey,
  scheduleActionLabel,
} from '../lib/schedules'
import type {
  Account,
  Schedule,
  SchedulePatchInput,
  SchedulePreview,
  SchedulesDisabled,
  SchedulesPayload,
  ScheduleWindow,
} from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import { ConfirmDialog } from '../components/Overlay'
import { ScheduleDialog } from '../components/schedules/ScheduleDialog'
import { ScheduleHistoryDrawer } from '../components/schedules/ScheduleHistoryDrawer'
import { RenewalSweepCard } from '../components/schedules/RenewalSweepCard'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  PageHeader,
  Select,
  Switch,
  TableSkeleton,
} from '../components/primitives'
import { useToast } from '../components/Toast'
import '../components/schedules/schedules.css'

const EMPTY_ACCOUNTS: Account[] = []
const EMPTY_SCHEDULES: Schedule[] = []

function lastResultKey(schedule: Schedule): string {
  if (!schedule.lastRunAt) return 'schedules.last.never'
  const known = ['ok', 'skipped', 'error', 'dry_run', 'unknown']
  return schedule.lastResult && known.includes(schedule.lastResult)
    ? `schedules.last.${schedule.lastResult}`
    : 'schedules.last.unknown'
}

function describeWindow(window: ScheduleWindow | null | undefined, t: (key: string) => string): string {
  if (!window) return ''
  const days =
    window.days.length === 0
      ? t('common.all')
      : window.days.map((day) => t(`schedules.days.${day}`)).join(' ')
  return `${days} · ${window.start}–${window.end}`
}

function previewWarning(warning: string, t: (key: string) => string): string {
  const key = `schedules.preview.warning.${warning}`
  const value = t(key)
  return value === key ? warning : value
}

function pausedReasonLabel(reason: string, t: (key: string) => string): string {
  const key = runReasonKey(reason)
  const value = t(key)
  return value === key ? reason : value
}

/**
 * Schedules: list/create/edit/delete, a pure next-run preview, run history and
 * the daemon-owned renewal sweep. Writes carry the row revision as If-Match and
 * retry exactly once on a 412; a 503 flips the page into an honest disabled
 * state because the daemon's schedule engine is off.
 */
export default function Schedules() {
  const { t, lang } = useI18n()
  const toast = useToast()
  const schedulesState = useAsync(() => api.listSchedules(), [], { intervalMs: 15_000 })
  const accountsState = useAsync(() => api.listAccounts(), [], { intervalMs: 30_000 })

  const [schedulerOff, setSchedulerOff] = useState(false)
  const [dialog, setDialog] = useState<{ mode: 'create' | 'edit'; schedule?: Schedule } | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Schedule | null>(null)
  const [runTarget, setRunTarget] = useState<Schedule | null>(null)
  const [historyTarget, setHistoryTarget] = useState<Schedule | null>(null)
  const [pendingId, setPendingId] = useState<string | null>(null)
  const [retryAt, setRetryAt] = useState<number | null>(null)
  const [retrySeconds, setRetrySeconds] = useState(0)
  const [staleNotice, setStaleNotice] = useState<string | null>(null)
  const [previewId, setPreviewId] = useState('')
  const [historyVersion, setHistoryVersion] = useState(0)

  const payload: SchedulesPayload | undefined = schedulesState.data
  const disabledInfo: SchedulesDisabled | null =
    payload && !Array.isArray(payload) && payload.enabled === false ? payload : null
  const allSchedules = Array.isArray(payload) ? payload : (payload?.schedules ?? EMPTY_SCHEDULES)
  const systemSchedule = allSchedules.find((schedule) => schedule.id === SYSTEM_SCHEDULE_ID)
  const schedules = allSchedules.filter((schedule) => !isSystemSchedule(schedule))
  const accounts = accountsState.data ?? EMPTY_ACCOUNTS
  const off = schedulerOff || disabledInfo !== null
  const busy = pendingId !== null || retrySeconds > 0

  const scheduleIds = schedules.map((schedule) => schedule.id).join('|')
  useEffect(() => {
    if (schedules.length === 0) {
      if (previewId) setPreviewId('')
      return
    }
    if (!schedules.some((schedule) => schedule.id === previewId)) {
      setPreviewId(schedules[0].id)
    }
    // scheduleIds covers membership; previewId is the current selection.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scheduleIds, previewId])

  const selectedPreview = schedules.find((schedule) => schedule.id === previewId) ?? schedules[0]
  const previewState = useAsync<{
    scheduleId: string
    revision: number
    preview: SchedulePreview
  } | null>(
    () =>
      selectedPreview
        ? api
            .previewSchedule(
              {
                action: selectedPreview.action,
                target: selectedPreview.target,
                trigger: selectedPreview.trigger,
                window: selectedPreview.window ?? undefined,
              },
              5,
            )
            .then((preview) => ({
              scheduleId: selectedPreview.id,
              revision: selectedPreview.revision,
              preview,
            }))
        : Promise.resolve(null),
    [selectedPreview?.id, selectedPreview?.revision],
    { intervalMs: 60_000 },
  )
  const previewData =
    previewState.data &&
    selectedPreview &&
    previewState.data.scheduleId === selectedPreview.id &&
    previewState.data.revision === selectedPreview.revision
      ? previewState.data.preview
      : undefined

  useEffect(() => {
    if (retryAt === null) {
      setRetrySeconds(0)
      return
    }
    const update = () => {
      const left = Math.max(0, Math.ceil((retryAt - Date.now()) / 1000))
      setRetrySeconds(left)
      if (left === 0) setRetryAt(null)
    }
    update()
    const timer = window.setInterval(update, 250)
    return () => window.clearInterval(timer)
  }, [retryAt])

  const describeError = (error: unknown): string => {
    if (error instanceof ScheduleActionError) {
      if (error.status === 412) return t('schedules.staleFailed')
      if (error.reason === 'confirmation_required') return t('schedules.confirmRequired')
      if (error.reason === 'in_flight') return t('schedules.inFlight')
      if (error.status === 503) {
        setSchedulerOff(true)
        return t('schedules.schedulerOff')
      }
      if (error.status === 429 && error.retryAfterSec) {
        setRetryAt(Date.now() + error.retryAfterSec * 1000)
        return `${error.message} ${t('schedules.retryAfter', { seconds: error.retryAfterSec })}`
      }
      return error.message
    }
    if (error instanceof ApiError) {
      if (error.status === 503) {
        setSchedulerOff(true)
        return t('schedules.schedulerOff')
      }
      return error.message
    }
    return error instanceof Error ? error.message : t('common.unknownError')
  }

  const patchWithRetry = async (
    schedule: Schedule,
    input: SchedulePatchInput,
  ): Promise<{ schedule: Schedule; retried: boolean }> => {
    try {
      return { schedule: await api.patchSchedule(schedule.id, schedule.revision, input), retried: false }
    } catch (error) {
      if (!(error instanceof ScheduleActionError) || error.status !== 412) throw error
      const latest = await api.getSchedule(schedule.id)
      return {
        schedule: await api.patchSchedule(latest.id, latest.revision, input),
        retried: true,
      }
    }
  }

  const toggleEnabled = async (schedule: Schedule, enabled: boolean) => {
    setPendingId(schedule.id)
    try {
      const result = await patchWithRetry(schedule, { enabled })
      if (result.retried) setStaleNotice(t('schedules.staleDesc'))
      toast.push(
        t(enabled ? 'schedules.toastEnabled' : 'schedules.toastDisabled', {
          name: result.schedule.name,
        }),
        'success',
      )
      schedulesState.reload()
    } catch (error) {
      const message = describeError(error)
      toast.push(message, 'danger')
      if (error instanceof ScheduleActionError && error.status === 412) schedulesState.reload()
    } finally {
      setPendingId(null)
    }
  }

  const runNow = async () => {
    if (!runTarget) return
    const target = runTarget
    setPendingId(target.id)
    try {
      const run = await api.runSchedule(target.id, { confirm: 'RUN' })
      toast.push(
        t('schedules.toastRun', { state: t(runStateKey(run.state)) }),
        run.state === 'failed' ? 'danger' : 'success',
      )
      setRunTarget(null)
      schedulesState.reload()
      if (historyTarget?.id === target.id) setHistoryVersion((value) => value + 1)
    } catch (error) {
      toast.push(describeError(error), 'danger')
    } finally {
      setPendingId(null)
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    const target = deleteTarget
    setPendingId(target.id)
    try {
      await api.deleteSchedule(target.id)
      toast.push(t('schedules.toastDeleted', { name: target.name }), 'success')
      setDeleteTarget(null)
      if (previewId === target.id) setPreviewId('')
      schedulesState.reload()
    } catch (error) {
      toast.push(describeError(error), 'danger')
    } finally {
      setPendingId(null)
    }
  }

  const enabledCount = schedules.filter(
    (schedule) => schedule.enabled && !schedule.pausedReason && !schedule.degraded,
  ).length
  const pausedCount = schedules.length - enabledCount
  const nextRunAt = schedules
    .filter((schedule) => schedule.enabled && !schedule.pausedReason)
    .map((schedule) => schedule.nextRunAt)
    .filter((value): value is string => Boolean(value))
    .sort()[0]

  const previewOptions = schedules.map((schedule) => ({ value: schedule.id, label: schedule.name }))

  const rowDisabled = (schedule: Schedule) => off || busy || pendingId === schedule.id

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('schedules.title')}
        subtitle={t('schedules.subtitle')}
        refresh={summarizeRefresh([schedulesState, accountsState, previewState])}
        actions={
          <>
            <Button
              variant="secondary"
              icon={<RefreshCw size={14} />}
              onClick={() => {
                schedulesState.reload()
                accountsState.reload()
                previewState.reload()
              }}
            >
              {t('common.refresh')}
            </Button>
            <Button
              variant="primary"
              icon={<Plus size={14} />}
              onClick={() => setDialog({ mode: 'create' })}
              disabled={off || busy}
            >
              {t('schedules.new')}
            </Button>
          </>
        }
      />

      {disabledInfo ? (
        <section className="schedule-banner" role="status">
          <Badge tone="warning">{t('schedules.disabledTitle')}</Badge>
          <p>{t('schedules.disabledDesc', { reason: disabledInfo.reason })}</p>
        </section>
      ) : null}
      {schedulerOff && !disabledInfo ? (
        <section className="schedule-banner" role="status">
          <Badge tone="warning">{t('schedules.disabledTitle')}</Badge>
          <p>{t('schedules.offBanner')}</p>
        </section>
      ) : null}
      {staleNotice ? (
        <section className="schedule-banner schedule-banner--info" role="status">
          <Badge tone="info">{t('schedules.staleTitle')}</Badge>
          <p>{staleNotice}</p>
        </section>
      ) : null}
      {retrySeconds > 0 ? (
        <section className="schedule-banner schedule-banner--info" role="status">
          <p>{t('schedules.retryAfter', { seconds: retrySeconds })}</p>
        </section>
      ) : null}

      <div className="schedule-summary">
        <div className="schedule-summary__item">
          <span className="label">{t('schedules.summary.total')}</span>
          <strong className="mono">{schedules.length}</strong>
        </div>
        <div className="schedule-summary__item">
          <span className="label">{t('schedules.summary.enabled')}</span>
          <strong className="mono">{enabledCount}</strong>
        </div>
        <div className="schedule-summary__item">
          <span className="label">{t('schedules.summary.paused')}</span>
          <strong className="mono">{pausedCount}</strong>
        </div>
        <div className="schedule-summary__item">
          <span className="label">{t('schedules.summary.next')}</span>
          <strong className="mono">
            {nextRunAt ? formatDateTimeSeconds(nextRunAt) : t('schedules.noNextRun')}
          </strong>
        </div>
      </div>

      <RenewalSweepCard accounts={accounts} systemSchedule={systemSchedule} />

      <Card
        title={t('schedules.title')}
        subtitle={t('schedules.caption')}
        bodyClassName="card__body--flush"
      >
        {schedulesState.status === 'loading' && !schedulesState.data ? (
          <TableSkeleton rows={5} columns={7} />
        ) : null}
        {schedulesState.status === 'error' && !schedulesState.data ? (
          <ErrorState message={schedulesState.error ?? ''} onRetry={schedulesState.reload} />
        ) : null}
        {schedulesState.data !== undefined ? (
          <DataTable<Schedule>
            caption={t('schedules.caption')}
            rows={schedules}
            rowKey={(schedule) => schedule.id}
            empty={
              <EmptyState
                title={t('schedules.emptyTitle')}
                description={
                  off
                    ? disabledInfo
                      ? t('schedules.disabledDesc', { reason: disabledInfo.reason })
                      : t('schedules.offBanner')
                    : `${t('schedules.emptyDesc')} ${t('schedules.emptyOffHint')}`
                }
                action={
                  <Button
                    variant="primary"
                    icon={<Plus size={14} />}
                    onClick={() => setDialog({ mode: 'create' })}
                    disabled={off || busy}
                  >
                    {t('schedules.new')}
                  </Button>
                }
              />
            }
            columns={[
              {
                key: 'name',
                label: t('schedules.columnName'),
                render: (schedule) => (
                  <div className="schedule-name">
                    <strong>{schedule.name}</strong>
                    <span className="schedule-name__meta">
                      {scheduleActionLabel(schedule.action, t)} · {schedule.owner}
                    </span>
                    <span className="inline-badges">
                      {!schedule.enabled ? <Badge tone="neutral">{t('schedules.paused')}</Badge> : null}
                      {schedule.pausedReason ? (
                        <Badge tone="warning">
                          {t('schedules.pausedReason', { reason: pausedReasonLabel(schedule.pausedReason, t) })}
                        </Badge>
                      ) : null}
                      {schedule.degraded ? (
                        <Badge tone="danger">
                          {t('schedules.degraded', { count: schedule.consecutiveFailures })}
                        </Badge>
                      ) : null}
                    </span>
                  </div>
                ),
              },
              {
                key: 'trigger',
                label: t('schedules.columnTrigger'),
                render: (schedule) => (
                  <div className="schedule-trigger">
                    <span>{describeTrigger(schedule.trigger, t)}</span>
                    {schedule.window ? (
                      <span className="schedule-trigger__window">
                        {describeWindow(schedule.window, t)}
                      </span>
                    ) : null}
                  </div>
                ),
              },
              {
                key: 'next',
                label: t('schedules.columnNext'),
                render: (schedule) =>
                  schedule.nextRunAt ? (
                    <div className="schedule-when">
                      <time dateTime={schedule.nextRunAt}>
                        {formatDateTimeSeconds(schedule.nextRunAt)}
                      </time>
                      <span className="schedule-when__relative">
                        {formatRelative(schedule.nextRunAt, Date.now(), lang)}
                      </span>
                    </div>
                  ) : (
                    <span className="muted">{t('schedules.noNextRun')}</span>
                  ),
              },
              {
                key: 'last',
                label: t('schedules.columnLast'),
                render: (schedule) => (
                  <div className="schedule-last">
                    <Badge tone={lastResultTone(schedule.lastResult)}>{t(lastResultKey(schedule))}</Badge>
                    {schedule.lastRunAt ? (
                      <span className="schedule-when__relative">
                        {formatDateTimeSeconds(schedule.lastRunAt)}
                      </span>
                    ) : null}
                    {schedule.lastError ? (
                      <span className="schedule-last__error" title={schedule.lastError}>
                        {schedule.lastError}
                      </span>
                    ) : null}
                  </div>
                ),
              },
              {
                key: 'runsToday',
                label: t('schedules.columnRunsToday'),
                align: 'right',
                render: (schedule) => (
                  <span className="mono">
                    {schedule.runCountToday} / {schedule.guard.maxRunsPerDay}
                  </span>
                ),
              },
              {
                key: 'enabled',
                label: t('schedules.columnEnabled'),
                render: (schedule) => (
                  <Switch
                    checked={schedule.enabled}
                    disabled={rowDisabled(schedule)}
                    label={t(schedule.enabled ? 'schedules.disable' : 'schedules.enable', {
                      name: schedule.name,
                    })}
                    onChange={(enabled) => void toggleEnabled(schedule, enabled)}
                  />
                ),
              },
              {
                key: 'actions',
                label: '',
                align: 'right',
                render: (schedule) => (
                  <div className="row-actions">
                    <IconButton
                      label={t('schedules.runNowFor', { name: schedule.name })}
                      icon={<Play size={15} />}
                      disabled={rowDisabled(schedule)}
                      onClick={() => setRunTarget(schedule)}
                    />
                    <IconButton
                      label={t('schedules.historyFor', { name: schedule.name })}
                      icon={<History size={15} />}
                      onClick={() => setHistoryTarget(schedule)}
                    />
                    <IconButton
                      label={t('schedules.editFor', { name: schedule.name })}
                      icon={<Pencil size={15} />}
                      disabled={rowDisabled(schedule)}
                      onClick={() => setDialog({ mode: 'edit', schedule })}
                    />
                    <IconButton
                      label={t('schedules.deleteFor', { name: schedule.name })}
                      icon={<Trash2 size={15} />}
                      disabled={rowDisabled(schedule)}
                      onClick={() => setDeleteTarget(schedule)}
                    />
                  </div>
                ),
              },
            ]}
          />
        ) : null}
      </Card>

      <Card
        title={
          <span className="schedule-preview__title">
            <CalendarClock size={16} aria-hidden="true" />
            {t('schedules.preview.title')}
          </span>
        }
        subtitle={t('schedules.preview.desc')}
        actions={
          <>
            <Select
              size="sm"
              variant="form"
              aria-label={t('schedules.preview.select')}
              value={selectedPreview?.id ?? ''}
              options={previewOptions}
              onChange={setPreviewId}
              disabled={schedules.length === 0}
            />
            <Button
              size="sm"
              variant="ghost"
              icon={<RefreshCw size={14} />}
              onClick={previewState.reload}
              disabled={!selectedPreview}
            >
              {t('common.refresh')}
            </Button>
          </>
        }
      >
        {!selectedPreview ? (
          <p className="schedule-preview__hint">{t('schedules.emptyTitle')}</p>
        ) : null}
        {previewState.status === 'loading' && !previewData ? (
          <p className="schedule-preview__hint" role="status">
            {t('schedules.preview.loading')}
          </p>
        ) : null}
        {previewState.status === 'error' ? (
          <ErrorState
            message={t('schedules.preview.error', { message: previewState.error ?? '' })}
            onRetry={previewState.reload}
          />
        ) : null}
        {previewData ? (
          <>
            {previewData.runs.length === 0 ? (
              <p className="schedule-preview__hint">{t('schedules.preview.empty')}</p>
            ) : (
              <ol className="schedule-preview__list schedule-preview__list--page" aria-label={t('schedules.preview.title')}>
                {previewData.runs.map((run, index) => (
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
            {previewData.warnings.length > 0 ? (
              <ul className="schedule-preview__warnings" aria-label={t('schedules.preview.warnings')}>
                {previewData.warnings.map((warning) => (
                  <li key={warning}>{previewWarning(warning, t)}</li>
                ))}
              </ul>
            ) : null}
            <p className="schedule-preview__server-time">
              {t('schedules.preview.serverTime', {
                time: formatDateTimeSeconds(previewData.serverTime),
              })}
            </p>
          </>
        ) : null}
      </Card>

      <ScheduleDialog
        open={dialog !== null}
        mode={dialog?.mode ?? 'create'}
        schedule={dialog?.schedule}
        accounts={accounts}
        onClose={() => setDialog(null)}
        onSchedulerOff={() => setSchedulerOff(true)}
        onSaved={(saved, mode, retried) => {
          if (retried) setStaleNotice(t('schedules.staleDesc'))
          toast.push(
            t(mode === 'create' ? 'schedules.toastCreated' : 'schedules.toastUpdated', {
              name: saved.name,
            }),
            'success',
          )
          setDialog(null)
          schedulesState.reload()
        }}
      />

      <ScheduleHistoryDrawer
        open={historyTarget !== null}
        schedule={historyTarget}
        reloadToken={historyVersion}
        onClose={() => setHistoryTarget(null)}
      />

      <ConfirmDialog
        open={runTarget !== null}
        title={t('schedules.runTitle')}
        description={
          runTarget
            ? `${t('schedules.runDesc', { name: runTarget.name })}${
                runTarget.guard.dryRun ? ` ${t('schedules.runDryRunNote')}` : ''
              }`
            : ''
        }
        confirmLabel={t('schedules.runConfirm')}
        tone="primary"
        busy={pendingId === runTarget?.id}
        onConfirm={() => void runNow()}
        onCancel={() => {
          if (pendingId === null) setRunTarget(null)
        }}
      />

      <ConfirmDialog
        open={deleteTarget !== null}
        title={t('schedules.deleteTitle')}
        description={
          deleteTarget ? t('schedules.deleteDesc', { name: deleteTarget.name }) : ''
        }
        confirmLabel={t('schedules.deleteConfirm')}
        tone="danger"
        busy={pendingId === deleteTarget?.id}
        onConfirm={() => void confirmDelete()}
        onCancel={() => {
          if (pendingId === null) setDeleteTarget(null)
        }}
      />
    </div>
  )
}
