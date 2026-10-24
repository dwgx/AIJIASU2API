import { useEffect, useState } from 'react'
import { api } from '../../lib/api'
import { formatDateTimeSeconds } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import {
  errorClassKey,
  runDuration,
  runReasonKey,
  runStateKey,
  runStateTone,
} from '../../lib/schedules'
import type { Schedule, ScheduleRun } from '../../lib/types'
import { DataTable } from '../DataTable'
import { Drawer } from '../Overlay'
import { Badge, ErrorState, TableSkeleton } from '../primitives'
import './schedules.css'

type LoadState =
  | { status: 'loading' }
  | { status: 'ready'; runs: ScheduleRun[] }
  | { status: 'error'; message: string }

function failureTone(errorClass: string | undefined): 'danger' | 'warning' | 'neutral' {
  if (errorClass === 'permanent' || errorClass === 'persist' || errorClass === 'invariant') return 'danger'
  if (errorClass === 'transient' || errorClass === 'internal') return 'warning'
  return 'neutral'
}

export function ScheduleHistoryDrawer({
  open,
  schedule,
  reloadToken = 0,
  onClose,
}: {
  open: boolean
  schedule: Schedule | null
  /** Bumped by the page after a manual run so the drawer refetches. */
  reloadToken?: number
  onClose: () => void
}) {
  const { t } = useI18n()
  const [state, setState] = useState<LoadState>({ status: 'loading' })

  useEffect(() => {
    if (!open || !schedule) return
    let cancelled = false
    setState({ status: 'loading' })
    api
      .listScheduleRuns(schedule.id, 100)
      .then((runs) => {
        if (!cancelled) setState({ status: 'ready', runs })
      })
      .catch((cause: unknown) => {
        if (cancelled) return
        setState({
          status: 'error',
          message: cause instanceof Error ? cause.message : String(cause),
        })
      })
    return () => {
      cancelled = true
    }
  }, [open, schedule?.id, schedule?.revision, reloadToken])

  const translateMaybe = (key: string, fallback: string | undefined): string => {
    if (!key) return fallback ?? '—'
    const value = t(key)
    return value === key ? (fallback ?? '—') : value
  }

  return (
    <Drawer
      open={open}
      title={t('schedules.history.title')}
      subtitle={schedule ? t('schedules.history.desc', { name: schedule.name }) : undefined}
      onClose={onClose}
      width={780}
    >
      {state.status === 'loading' ? <TableSkeleton rows={5} columns={5} /> : null}
      {state.status === 'error' ? (
        <ErrorState message={t('schedules.history.error', { message: state.message })} />
      ) : null}
      {state.status === 'ready' ? (
        <DataTable<ScheduleRun>
          caption={t('schedules.history.title')}
          rows={state.runs}
          rowKey={(run) => run.id}
          empty={<p className="schedule-history__empty">{t('schedules.history.empty')}</p>}
          columns={[
            {
              key: 'started',
              label: t('schedules.history.columnStarted'),
              render: (run) => (
                <div className="schedule-history__when">
                  <time dateTime={run.startedAt}>{formatDateTimeSeconds(run.startedAt)}</time>
                  <span className="schedule-history__trigger">{run.trigger}</span>
                </div>
              ),
            },
            {
              key: 'state',
              label: t('schedules.history.columnState'),
              render: (run) => (
                <Badge tone={runStateTone(run.state)}>{t(runStateKey(run.state))}</Badge>
              ),
            },
            {
              key: 'duration',
              label: t('schedules.history.columnDuration'),
              align: 'right',
              render: (run) => <span className="mono">{runDuration(run)}</span>,
            },
            {
              key: 'error',
              label: t('schedules.history.columnError'),
              render: (run) =>
                run.errorClass || run.reason ? (
                  <div className="schedule-history__failure">
                    {run.errorClass ? (
                      <Badge tone={failureTone(run.errorClass)}>
                        {translateMaybe(errorClassKey(run.errorClass), run.errorClass)}
                      </Badge>
                    ) : null}
                    {run.reason ? (
                      <span className="schedule-history__reason">
                        {translateMaybe(runReasonKey(run.reason), run.reason)}
                      </span>
                    ) : null}
                  </div>
                ) : (
                  <span className="muted">—</span>
                ),
            },
            {
              key: 'detail',
              label: t('schedules.history.columnDetail'),
              render: (run) => (
                <div className="schedule-history__detail">
                  <span>{run.detail || '—'}</span>
                  {run.actor ? (
                    <span className="schedule-history__actor">
                      {t('schedules.history.columnActor')}: {run.actor}
                    </span>
                  ) : null}
                </div>
              ),
            },
          ]}
        />
      ) : null}
    </Drawer>
  )
}
