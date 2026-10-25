import { RefreshCw } from 'lucide-react'
import { useEffect, useId, useMemo, useState } from 'react'
import { api } from '../lib/api'
import { formatDateTimeSeconds } from '../lib/format'
import { useI18n } from '../lib/i18n'
import type { TranslationKey, TranslationParams } from '../lib/i18n'
import type {
  Operation,
  OperationState,
  OperationTarget,
  SelectNodeResult,
  Status,
} from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { Pagination, usePagination } from '../components/Pagination'
import { useToast } from '../components/Toast'
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  Input,
  PageHeader,
  Select,
  StatusPill,
  TableSkeleton,
} from '../components/primitives'

/** The list refresh cadence while at least one operation is in flight. */
const ACTIVE_POLL_MS = 2_000
/** The follow cadence for one accepted operation. */
const FOLLOW_POLL_MS = 1_000
const FOLLOW_RETRY_MS = 2_000

const OPERATION_META: Record<
  OperationState,
  { status: Status; labelKey: TranslationKey; pulse?: boolean }
> = {
  queued: { status: 'info', labelKey: 'operations.state.queued' },
  running: { status: 'info', labelKey: 'operations.state.running', pulse: true },
  succeeded: { status: 'ok', labelKey: 'operations.state.succeeded' },
  failed: { status: 'error', labelKey: 'operations.state.failed' },
  canceled: { status: 'warn', labelKey: 'operations.state.canceled' },
}

function isActive(operation: Operation): boolean {
  return operation.state === 'queued' || operation.state === 'running'
}

function targetLabel(target: OperationTarget): string {
  if (target.serverId) return target.serverId
  if (target.nodeId) return target.nodeId
  if (target.region) return `${target.region} · ${target.strategy ?? 'next'}`
  return '—'
}

/**
 * The operation's wall time: the executor's own duration when it finished,
 * otherwise the timestamp span while it is still running. A queued record has
 * no start yet, so it has no duration rather than a fabricated zero.
 */
function operationDurationMs(operation: Operation): number | null {
  if (operation.result?.durationMs) return operation.result.durationMs
  if (!operation.startedAt) return null
  const started = Date.parse(operation.startedAt)
  if (Number.isNaN(started)) return null
  const finished = operation.finishedAt
    ? Date.parse(operation.finishedAt)
    : isActive(operation)
      ? Date.now()
      : Number.NaN
  if (Number.isNaN(finished)) return null
  return Math.max(0, finished - started)
}

type Translate = (key: string, params?: TranslationParams) => string

function durationLabel(operation: Operation, t: Translate): string {
  const ms = operationDurationMs(operation)
  if (ms === null) return '—'
  if (ms < 1_000) return t('operations.durationMs', { ms })
  return t('operations.durationSec', { seconds: (ms / 1_000).toFixed(1) })
}

function followMessage(operation: Operation, t: Translate): string {
  switch (operation.state) {
    case 'queued':
      return t('operations.follow.queued')
    case 'running':
      return t('operations.follow.running')
    case 'succeeded':
      if (operation.result) {
        return t(
          operation.result.changed ? 'operations.follow.changed' : 'operations.follow.unchanged',
          { server: operation.result.serverId },
        )
      }
      return t('operations.follow.succeeded')
    case 'failed':
      return t('operations.follow.failed')
    case 'canceled':
      return t('operations.follow.canceled')
  }
}

export default function Operations() {
  const { t } = useI18n()
  const { push } = useToast()
  const timeoutId = useId()

  const operations = useAsync(() => api.listOperations({ limit: 200 }), [], { intervalMs: 0 })
  const accounts = useAsync(() => api.listAccounts(), [])

  const [stateFilter, setStateFilter] = useState<'all' | OperationState>('all')
  const [accountId, setAccountId] = useState('')
  const [serverId, setServerId] = useState('')
  const [ifCurrent, setIfCurrent] = useState<'noop' | 'error' | 'reselect'>('noop')
  const [timeoutValue, setTimeoutValue] = useState('60')
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [fieldErrors, setFieldErrors] = useState<{
    account?: string
    server?: string
    timeout?: string
  }>({})
  const [followed, setFollowed] = useState<Operation | null>(null)
  const [following, setFollowing] = useState(false)
  const [followError, setFollowError] = useState<string | null>(null)
  const [immediate, setImmediate] = useState<SelectNodeResult | null>(null)

  const servers = useAsync(
    () =>
      accountId
        ? api.getAccountServers(accountId, { offset: 0, limit: 500 })
        : Promise.resolve(null),
    [accountId],
  )

  const active = useMemo(() => (operations.data ?? []).some(isActive), [operations.data])

  // Poll only while a record is queued or running. The effect tears the timer
  // down as soon as a page of terminal records arrives, so a finished history
  // costs no further requests.
  useEffect(() => {
    if (!active) return
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'hidden') return
      operations.reload()
    }, ACTIVE_POLL_MS)
    return () => window.clearInterval(timer)
  }, [active, operations.reload])

  // Follow the 202 operation to a terminal state with GET /v1/operations/{id}.
  // Each answer replaces the record; the list reloads once it is terminal.
  const followedId = followed?.id
  useEffect(() => {
    if (!following || !followedId) return
    let cancelled = false
    let timer: number | undefined

    const tick = async () => {
      try {
        const next = await api.getOperation(followedId)
        if (cancelled) return
        setFollowed(next)
        setFollowError(null)
        if (next.state === 'queued' || next.state === 'running') {
          timer = window.setTimeout(() => void tick(), FOLLOW_POLL_MS)
          return
        }
        setFollowing(false)
        operations.reload()
        if (next.state === 'succeeded' && next.result) {
          push(
            t(
              next.result.changed ? 'operations.follow.changed' : 'operations.follow.unchanged',
              { server: next.result.serverId },
            ),
            'success',
          )
        } else if (next.state === 'failed') {
          push(t('operations.follow.failed'), 'danger')
        }
      } catch (cause) {
        if (cancelled) return
        setFollowError(cause instanceof Error ? cause.message : t('common.unknownError'))
        timer = window.setTimeout(() => void tick(), FOLLOW_RETRY_MS)
      }
    }

    void tick()
    return () => {
      cancelled = true
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [following, followedId, operations.reload, push, t])

  const accountOptions = useMemo(
    () =>
      (accounts.data ?? []).map((account) => ({
        value: account.id,
        label: account.label,
        description: `${account.username} · ${account.platform}`,
      })),
    [accounts.data],
  )

  const accountLabels = useMemo(
    () => new Map((accounts.data ?? []).map((account) => [account.id, account.label])),
    [accounts.data],
  )

  const serverOptions = useMemo(
    () =>
      (servers.data?.servers ?? []).map((server) => ({
        value: server.id,
        label: server.title,
        description: [server.id, server.region, server.ineligibleReason]
          .filter((part): part is string => Boolean(part))
          .join(' · '),
        disabled: server.eligible === false,
      })),
    [servers.data],
  )

  const filtered = useMemo(
    () =>
      (operations.data ?? []).filter(
        (operation) => stateFilter === 'all' || operation.state === stateFilter,
      ),
    [operations.data, stateFilter],
  )
  const pagination = usePagination({
    total: filtered.length,
    initialPageSize: 25,
    resetKey: stateFilter,
  })
  const rows = pagination.slice(filtered)

  const stateOptions = [
    { value: 'all', label: t('operations.allStates') },
    ...(Object.keys(OPERATION_META) as OperationState[]).map((state) => ({
      value: state,
      label: t(OPERATION_META[state].labelKey),
    })),
  ]

  const ifCurrentOptions: Array<{
    value: 'noop' | 'error' | 'reselect'
    label: string
  }> = [
    { value: 'noop', label: t('operations.select.ifCurrent.noop') },
    { value: 'error', label: t('operations.select.ifCurrent.error') },
    { value: 'reselect', label: t('operations.select.ifCurrent.reselect') },
  ]

  const submit = async () => {
    const errors: typeof fieldErrors = {}
    if (!accountId) errors.account = t('common.required')
    if (!serverId) errors.server = t('common.required')
    const seconds = Number(timeoutValue)
    if (!Number.isInteger(seconds) || seconds < 10 || seconds > 120) {
      errors.timeout = t('operations.select.timeoutError')
    }
    setFieldErrors(errors)
    if (Object.keys(errors).length > 0) return

    setSubmitting(true)
    setSubmitError(null)
    setFollowError(null)
    setFollowed(null)
    setImmediate(null)
    setFollowing(false)
    try {
      const response = await api.selectNode(accountId, {
        target: { serverId },
        ifCurrent,
        timeoutSeconds: seconds,
        wait: false,
      })
      if (response.kind === 'result') {
        setImmediate(response.result)
        push(
          t(
            response.result.changed
              ? 'operations.follow.changed'
              : 'operations.follow.unchanged',
            { server: response.result.serverId },
          ),
          'success',
        )
        operations.reload()
        return
      }
      setFollowed(response.operation)
      setFollowing(true)
      operations.reload()
    } catch (cause) {
      setSubmitError(cause instanceof Error ? cause.message : t('common.unknownError'))
    } finally {
      setSubmitting(false)
    }
  }

  const columns: Array<Column<Operation>> = [
    {
      key: 'id',
      label: t('operations.columnId'),
      width: '96px',
      render: (operation) => <span className="mono">{operation.id}</span>,
    },
    {
      key: 'state',
      label: t('operations.columnState'),
      width: '110px',
      render: (operation) => {
        const meta = OPERATION_META[operation.state]
        return (
          <StatusPill status={meta.status} pulse={meta.pulse}>
            {t(meta.labelKey)}
          </StatusPill>
        )
      },
    },
    {
      key: 'account',
      label: t('operations.columnAccount'),
      render: (operation) => (
        <span title={operation.accountId}>
          {accountLabels.get(operation.accountId) ?? t('operations.accountUnknown')}
          <span className="mono" style={{ color: 'var(--muted)' }}>
            {' '}
            · {operation.accountId}
          </span>
        </span>
      ),
    },
    {
      key: 'target',
      label: t('operations.columnTarget'),
      render: (operation) => (
        <span className="mono" title={operation.target.nodeId ?? operation.target.strategy}>
          {targetLabel(operation.target)}
          {operation.target.serverId && operation.target.nodeId ? (
            <span style={{ color: 'var(--muted)' }}> · {operation.target.nodeId}</span>
          ) : null}
        </span>
      ),
    },
    {
      key: 'started',
      label: t('operations.columnStarted'),
      render: (operation) =>
        operation.startedAt ? (
          <span className="mono">{formatDateTimeSeconds(operation.startedAt)}</span>
        ) : (
          '—'
        ),
    },
    {
      key: 'finished',
      label: t('operations.columnFinished'),
      render: (operation) =>
        operation.finishedAt ? (
          <span className="mono">{formatDateTimeSeconds(operation.finishedAt)}</span>
        ) : (
          '—'
        ),
    },
    {
      key: 'duration',
      label: t('operations.columnDuration'),
      align: 'right',
      width: '92px',
      render: (operation) => (
        <span className="mono">{durationLabel(operation, t)}</span>
      ),
    },
    {
      key: 'error',
      label: t('operations.columnError'),
      render: (operation) =>
        operation.error ? (
          <span className="mono" title={operation.error.message}>
            {operation.error.code}
          </span>
        ) : (
          '—'
        ),
    },
  ]

  const followedMeta = followed ? OPERATION_META[followed.state] : null

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('operations.title')}
        subtitle={t('operations.subtitle')}
        refresh={summarizeRefresh([operations])}
        actions={
          <Button
            variant="secondary"
            icon={<RefreshCw size={14} />}
            onClick={operations.reload}
          >
            {t('common.refresh')}
          </Button>
        }
      />

      <Card title={t('operations.select.title')} subtitle={t('operations.select.subtitle')}>
        <div className="form">
          <div className="form-grid">
            <Field label={t('operations.select.account')} error={fieldErrors.account}>
              <Select
                aria-label={t('operations.select.account')}
                value={accountId}
                placeholder={t('operations.select.accountPlaceholder')}
                searchable
                options={accountOptions}
                onChange={(value) => {
                  setAccountId(value)
                  setServerId('')
                  setFieldErrors((current) => ({ ...current, account: undefined, server: undefined }))
                }}
              />
            </Field>
            <Field label={t('operations.select.server')} error={fieldErrors.server}>
              <Select
                aria-label={t('operations.select.server')}
                value={serverId}
                placeholder={t('operations.select.serverPlaceholder')}
                searchable
                disabled={!accountId || servers.status === 'loading'}
                options={serverOptions}
                onChange={(value) => {
                  setServerId(value)
                  setFieldErrors((current) => ({ ...current, server: undefined }))
                }}
              />
            </Field>
          </div>

          <div className="form-grid">
            <Field label={t('operations.select.ifCurrent')}>
              <Select
                aria-label={t('operations.select.ifCurrent')}
                value={ifCurrent}
                options={ifCurrentOptions}
                onChange={(value) =>
                  setIfCurrent(value as 'noop' | 'error' | 'reselect')
                }
              />
            </Field>
            <Field
              label={t('operations.select.timeout')}
              htmlFor={timeoutId}
              hint={t('operations.select.timeoutHint')}
              error={fieldErrors.timeout}
            >
              <Input
                id={timeoutId}
                type="number"
                min={10}
                max={120}
                step={1}
                value={timeoutValue}
                onChange={(event) => {
                  setTimeoutValue(event.target.value)
                  setFieldErrors((current) => ({ ...current, timeout: undefined }))
                }}
              />
            </Field>
          </div>

          {accounts.status === 'error' ? (
            <p className="form-error" role="alert">
              {t('common.couldNotLoad')} {accounts.error ?? ''}
            </p>
          ) : null}
          {servers.status === 'error' ? (
            <p className="form-error" role="alert">
              {t('operations.select.loadFailed')} {servers.error ?? ''}
            </p>
          ) : null}
          {accountId && servers.status === 'ready' && serverOptions.length === 0 ? (
            <p className="form-note">{t('operations.select.serverEmpty')}</p>
          ) : null}
          {submitError ? (
            <p className="form-error" role="alert">
              {t('operations.select.failed')} {submitError}
            </p>
          ) : null}

          <div>
            <Button
              variant="primary"
              loading={submitting}
              disabled={!accountId || !serverId}
              onClick={() => void submit()}
            >
              {t(submitting ? 'operations.select.busy' : 'operations.select.submit')}
            </Button>
          </div>

          {followed && followedMeta ? (
            <div className="form-note" role="status" aria-live="polite">
              <div className="inline-badges">
                <StatusPill status={followedMeta.status} pulse={followedMeta.pulse}>
                  {t(followedMeta.labelKey)}
                </StatusPill>
                <span className="mono">
                  {t('operations.follow.title', { id: followed.id })}
                </span>
                {followed.finishedAt ? (
                  <span className="mono">{durationLabel(followed, t)}</span>
                ) : null}
              </div>
              <p>{followMessage(followed, t)}</p>
              {followed.result ? (
                <p className="mono">
                  {followed.result.egressIp && followed.result.gatePort
                    ? `${t('operations.follow.egress', {
                        ip: followed.result.egressIp,
                        port: followed.result.gatePort,
                      })} · `
                    : ''}
                  {followed.result.sessionHealthy ? t('operations.follow.session') : ''}
                </p>
              ) : null}
              {followed.error ? (
                <p className="mono">
                  {t('operations.follow.error', { code: followed.error.code })} —{' '}
                  {followed.error.message}
                </p>
              ) : null}
              {followError ? <p>{t('operations.follow.retry')}</p> : null}
            </div>
          ) : null}

          {immediate ? (
            <div className="form-note" role="status" aria-live="polite">
              <div className="inline-badges">
                <StatusPill status="ok">{t('operations.follow.succeeded')}</StatusPill>
                <span className="mono">{immediate.serverId}</span>
              </div>
              <p>
                {t(
                  immediate.changed
                    ? 'operations.follow.changed'
                    : 'operations.follow.unchanged',
                  { server: immediate.serverId },
                )}
              </p>
              {immediate.egressIp && immediate.gatePort ? (
                <p className="mono">
                  {t('operations.follow.egress', {
                    ip: immediate.egressIp,
                    port: immediate.gatePort,
                  })}
                </p>
              ) : null}
            </div>
          ) : null}
        </div>
      </Card>

      <Card
        title={t('operations.list.title')}
        subtitle={t('operations.list.subtitle')}
        actions={
          <Select
            aria-label={t('operations.filterState')}
            variant="filter"
            value={stateFilter}
            options={stateOptions}
            onChange={(value) => setStateFilter(value as 'all' | OperationState)}
          />
        }
        bodyClassName="card__body--flush"
      >
        {operations.status === 'loading' ? (
          <TableSkeleton rows={6} columns={8} />
        ) : operations.status === 'error' ? (
          <ErrorState
            message={operations.error ?? t('common.unknownError')}
            onRetry={operations.reload}
          />
        ) : (
          <>
            <DataTable
              caption={t('operations.caption')}
              columns={columns}
              rows={rows}
              rowKey={(operation) => operation.id}
              empty={
                stateFilter === 'all' ? (
                  <EmptyState
                    title={t('operations.emptyTitle')}
                    description={t('operations.emptyDesc')}
                  />
                ) : (
                  <EmptyState
                    title={t('operations.emptyFilteredTitle')}
                    description={t('operations.emptyFilteredDesc')}
                    action={
                      <Button variant="secondary" onClick={() => setStateFilter('all')}>
                        {t('common.clearFilters')}
                      </Button>
                    }
                  />
                )
              }
            />
            <Pagination
              page={pagination.page}
              pageSize={pagination.pageSize}
              total={filtered.length}
              onPageChange={pagination.setPage}
              onPageSizeChange={pagination.setPageSize}
              label={t('operations.pagination')}
            />
          </>
        )}
      </Card>
    </div>
  )
}
