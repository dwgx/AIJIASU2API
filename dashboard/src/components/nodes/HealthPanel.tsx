import { RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../../lib/api'
import { formatNumber, formatTime } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type {
  HealthErrorClass,
  HealthSample,
  HealthView,
  NodeHealth,
  NodeHealthState,
  Status,
} from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import type { AsyncState } from '../../lib/useAsync'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  Skeleton,
  Sparkline,
  StatusPill,
} from '../primitives'

/** The daemon probes every 30 s; a 15 s read keeps the panel one probe behind at most. */
const HEALTH_POLL_MS = 15_000
/** The daemon retains 120 samples; the panel keeps the last hour at its own cadence. */
const SPARK_SAMPLES = 60

const HEALTH_STATE: Record<NodeHealthState, { status: Status; labelKey: string }> = {
  ok: { status: 'ok', labelKey: 'health.state.ok' },
  degraded: { status: 'warn', labelKey: 'health.state.degraded' },
  down: { status: 'unavailable', labelKey: 'health.state.down' },
}

const WORST_RANK: Record<NodeHealthState, number> = { down: 0, degraded: 1, ok: 2 }

const ERROR_CLASS_KEY: Record<HealthErrorClass, string> = {
  timeout: 'health.error.timeout',
  refused: 'health.error.refused',
  unreachable: 'health.error.unreachable',
  other: 'health.error.other',
}

interface NodeHistory {
  /** Identity of the newest retained sample; a repeated poll must not add a point. */
  key: string
  values: number[]
}

interface HealthPolling {
  health: AsyncState<HealthView>
  history: Record<string, NodeHistory>
}

/** A measured zero is still a measurement; it reads as `0%`, not `0.0%`. */
function successPercent(node: NodeHealth): string {
  const value = node.successRate * 100
  return `${value === 0 ? '0' : value.toFixed(1)}%`
}

function countStates(nodes: NodeHealth[]): Record<NodeHealthState, number> {
  const counts: Record<NodeHealthState, number> = { ok: 0, degraded: 0, down: 0 }
  for (const node of nodes) counts[node.state] += 1
  return counts
}

/** The node an operator should look at first: down before degraded, then the worst rate. */
function worstNode(nodes: NodeHealth[]): NodeHealth | null {
  let worst: NodeHealth | null = null
  for (const node of nodes) {
    if (node.state === 'ok') continue
    if (
      worst === null ||
      WORST_RANK[node.state] < WORST_RANK[worst.state] ||
      (WORST_RANK[node.state] === WORST_RANK[worst.state] &&
        (node.successRate < worst.successRate ||
          (node.successRate === worst.successRate &&
            node.consecutiveFailures > worst.consecutiveFailures)))
    ) {
      worst = node
    }
  }
  return worst
}

/**
 * The API reports one aggregate plus its newest sample per node, so the panel
 * builds its own short series from successive polls. Failed probes are left
 * out: a timeout's duration is not a latency measurement.
 */
function appendSample(
  history: Record<string, NodeHistory>,
  node: NodeHealth,
): Record<string, NodeHistory> {
  const sample: HealthSample | undefined = node.last
  if (!sample || !sample.ok || !(sample.latencyMs > 0)) return history
  const key = `${sample.at}|${sample.latencyMs}`
  const current = history[node.nodeId]
  if (current?.key === key) return history
  return {
    ...history,
    [node.nodeId]: {
      key,
      values: [...(current?.values ?? []), sample.latencyMs].slice(-SPARK_SAMPLES),
    },
  }
}

function useHealthPolling(): HealthPolling {
  const [polling, setPolling] = useState(true)
  const health = useAsync(() => api.getNodeHealth(), [], {
    intervalMs: HEALTH_POLL_MS,
    enabled: polling,
  })
  const [history, setHistory] = useState<Record<string, NodeHistory>>({})

  useEffect(() => {
    if (!health.data) return
    // A disabled sampler has no cadence to keep; the next manual refresh (or
    // the global refresh event) is what notices it being switched back on.
    setPolling(health.data.enabled)
    setHistory((current) => {
      let next = current
      for (const node of health.data?.nodes ?? []) next = appendSample(next, node)
      return next
    })
  }, [health.data])

  return { health, history }
}

function HealthSkeleton() {
  return (
    <div className="health-loading" aria-busy="true">
      <Skeleton height={30} />
      <Skeleton height={30} />
      <Skeleton height={30} />
    </div>
  )
}

function DisabledHealth({ view, compact }: { view: HealthView; compact: boolean }) {
  const { t } = useI18n()
  return (
    <div className="health-disabled" role="status">
      <p className="health-disabled__title">
        {t(compact ? 'health.disabledShort' : 'health.disabledTitle')}
      </p>
      {compact ? null : <p className="health-disabled__desc">{t('health.disabledDesc')}</p>}
      {view.reason ? (
        <p className="health-disabled__reason mono">
          {t('health.disabledReason', { reason: view.reason })}
        </p>
      ) : null}
    </div>
  )
}

function HealthTable({
  view,
  history,
}: {
  view: HealthView
  history: Record<string, NodeHistory>
}) {
  const { t } = useI18n()
  return (
    <div className="health-table-wrap">
      <table className="health-table">
        <caption className="sr-only">{t('health.tableCaption')}</caption>
        <thead>
          <tr>
            <th scope="col">{t('health.columnNode')}</th>
            <th scope="col">{t('health.columnState')}</th>
            <th scope="col" className="is-num">
              {t('health.columnLast')}
            </th>
            <th scope="col" className="is-num">
              {t('health.columnP50')}
            </th>
            <th scope="col" className="is-num">
              {t('health.columnP95')}
            </th>
            <th scope="col" className="is-num">
              {t('health.columnSuccess')}
            </th>
            <th scope="col" className="is-num">
              {t('health.columnFailures')}
            </th>
            <th scope="col" className="is-num">
              {t('health.columnSamples')}
            </th>
          </tr>
        </thead>
        <tbody>
          {view.nodes.map((node) => {
            const state = HEALTH_STATE[node.state]
            const samples = history[node.nodeId]?.values ?? []
            const measured = node.count > 0
            // Percentiles are computed over successful probes only; a node with
            // none must not present the daemon's zero placeholder as a latency.
            const hasSuccess = node.ok > 0
            const name = node.label?.trim() ? node.label.trim() : node.nodeId
            const errorKey = node.last?.errorClass
              ? ERROR_CLASS_KEY[node.last.errorClass as HealthErrorClass] ?? 'health.error.other'
              : null
            return (
              <tr key={node.nodeId} className={`health-row health-row--${node.state}`}>
                <th scope="row">
                  <span className="cell-strong mono">
                    {node.label?.trim() ? `${name} · ${node.nodeId}` : name}
                  </span>
                </th>
                <td>
                  <StatusPill status={state.status}>{t(state.labelKey)}</StatusPill>
                </td>
                <td className="is-num">
                  {node.last ? (
                    <span className="health-last">
                      <span className="mono">
                        {formatNumber(Math.round(node.last.latencyMs))} ms
                      </span>
                      {node.last.ok ? null : (
                        <Badge tone="danger">{errorKey ? t(errorKey) : t('health.failed')}</Badge>
                      )}
                    </span>
                  ) : (
                    <span className="cell-sub">{t('health.noSample')}</span>
                  )}
                </td>
                <td className="is-num mono">
                  {hasSuccess ? `${formatNumber(Math.round(node.p50Ms))} ms` : '—'}
                </td>
                <td
                  className="is-num mono"
                  title={hasSuccess ? undefined : t('health.noSuccessfulSamples')}
                >
                  {hasSuccess ? `${formatNumber(Math.round(node.p95Ms))} ms` : '—'}
                </td>
                <td className="is-num mono">{measured ? successPercent(node) : '—'}</td>
                <td className="is-num">
                  {node.consecutiveFailures > 0 ? (
                    <Badge tone="danger">{formatNumber(node.consecutiveFailures)}</Badge>
                  ) : (
                    <span className="mono">0</span>
                  )}
                </td>
                <td className="is-num">
                  <span
                    className={`health-spark health-spark--${node.state}`}
                    role="img"
                    aria-label={t('health.sparklineLabel', { name, count: samples.length })}
                    title={t('health.sparklineHint')}
                  >
                    {samples.length >= 2 ? (
                      <Sparkline data={samples} width={120} height={24} />
                    ) : (
                      <span className="health-spark__pending" aria-hidden="true">
                        {samples.length === 0
                          ? '—'
                          : t('health.sampleCountOne', { count: samples.length })}
                      </span>
                    )}
                  </span>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function FullHealthPanel({
  health,
  history,
}: {
  health: AsyncState<HealthView>
  history: Record<string, NodeHistory>
}) {
  const { t } = useI18n()
  const view = health.data
  const nodes = view?.nodes ?? []
  const counts = countStates(nodes)
  const allOk = nodes.length > 0 && counts.ok === nodes.length
  const staleText =
    health.staleAt !== undefined
      ? t(health.error ? 'common.staleWithError' : 'common.stale', {
          time: formatTime(new Date(health.updatedAt ?? health.staleAt).toISOString()),
          error: health.error ?? '',
        })
      : null

  return (
    <Card
      title={t('health.title')}
      subtitle={t('health.subtitle')}
      actions={
        <>
          {view?.enabled && nodes.length > 0 ? (
            <Badge tone={allOk ? 'success' : counts.down > 0 ? 'danger' : 'warning'}>
              {t('health.counts', counts)}
            </Badge>
          ) : null}
          <IconButton
            label={t('health.refreshAria')}
            icon={<RefreshCw size={14} />}
            onClick={health.reload}
          />
        </>
      }
    >
      {health.status === 'error' && !view ? (
        <ErrorState message={health.error ?? t('common.unknownError')} onRetry={health.reload} />
      ) : health.status === 'loading' && !view ? (
        <HealthSkeleton />
      ) : !view ? (
        <HealthSkeleton />
      ) : !view.enabled ? (
        <DisabledHealth view={view} compact={false} />
      ) : nodes.length === 0 ? (
        <EmptyState title={t('health.emptyTitle')} description={t('health.emptyDesc')} />
      ) : (
        <>
          {allOk ? <p className="callout callout--muted">{t('health.allOk')}</p> : null}
          {staleText ? <p className="callout callout--warning">{staleText}</p> : null}
          <HealthTable view={view} history={history} />
        </>
      )}
    </Card>
  )
}

function CompactHealthPanel({
  health,
  history,
}: {
  health: AsyncState<HealthView>
  history: Record<string, NodeHistory>
}) {
  const { t } = useI18n()
  const view = health.data
  const nodes = view?.nodes ?? []
  const counts = countStates(nodes)
  const worst = worstNode(nodes)
  const samples = worst ? history[worst.nodeId]?.values ?? [] : []
  const worstName = worst?.label?.trim() ? worst.label.trim() : worst?.nodeId

  return (
    <Card
      title={t('health.title')}
      subtitle={t('health.compactSubtitle')}
      actions={
        <Link className="link" to="/nodes">
          {t('health.viewNodes')}
        </Link>
      }
    >
      {health.status === 'error' && !view ? (
        <div className="stack">
          <p className="callout callout--warning" role="alert">
            {health.error ?? t('common.unknownError')}
          </p>
          <div>
            <Button variant="secondary" size="sm" onClick={health.reload}>
              {t('common.retry')}
            </Button>
          </div>
        </div>
      ) : health.status === 'loading' && !view ? (
        <HealthSkeleton />
      ) : !view ? (
        <HealthSkeleton />
      ) : !view.enabled ? (
        <DisabledHealth view={view} compact />
      ) : nodes.length === 0 ? (
        <p className="cell-sub">{t('health.emptyTitle')}</p>
      ) : (
        <div className="health-compact">
          <div className="health-compact__counts">
            <StatusPill status="ok">{t('health.countOk', { count: counts.ok })}</StatusPill>
            <StatusPill status="warn">
              {t('health.countDegraded', { count: counts.degraded })}
            </StatusPill>
            <StatusPill status="unavailable">
              {t('health.countDown', { count: counts.down })}
            </StatusPill>
          </div>
          {worst ? (
            <div className="health-compact__worst">
              <span className="label">{t('health.worst')}</span>
              <span className="mono">{worstName}</span>
              <StatusPill status={HEALTH_STATE[worst.state].status}>
                {t(HEALTH_STATE[worst.state].labelKey)}
              </StatusPill>
              <span className="mono">{worst.count > 0 ? successPercent(worst) : '—'}</span>
              <span
                className={`health-spark health-spark--${worst.state}`}
                role="img"
                aria-label={t('health.sparklineLabel', { name: worstName ?? '', count: samples.length })}
                title={t('health.sparklineHint')}
              >
                {samples.length >= 2 ? <Sparkline data={samples} width={96} height={22} /> : null}
              </span>
            </div>
          ) : (
            <p className="health-compact__ok">{t('health.allOk')}</p>
          )}
        </div>
      )}
    </Card>
  )
}

/**
 * Node health from the daemon's loopback sampler. The full variant is the
 * Connected-tab panel; `compact` is the Overview summary. Both poll every
 * 15 s, pause while the tab is hidden and stop when the sampler is disabled.
 */
export function HealthPanel({ variant = 'full' }: { variant?: 'full' | 'compact' }) {
  const { health, history } = useHealthPolling()
  return variant === 'compact' ? (
    <CompactHealthPanel health={health} history={history} />
  ) : (
    <FullHealthPanel health={health} history={history} />
  )
}
