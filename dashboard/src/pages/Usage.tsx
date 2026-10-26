import clsx from 'clsx'
import { Activity, AlertTriangle, ArrowUpRight, RefreshCw, Timer, Waypoints } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { UsageChart, fillUsageDays } from '../components/usage/UsageChart'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  MetricTile,
  PageHeader,
  ProgressBar,
  SegmentedControl,
  Select,
  Skeleton,
  TableSkeleton,
} from '../components/primitives'
import { api } from '../lib/api'
import { scopeDenial } from '../lib/errors'
import { formatBytes, formatNumber } from '../lib/format'
import { useI18n } from '../lib/i18n'
import type { TranslationKey, TranslationParams } from '../lib/i18n'
import type { UsageAccount, UsageBucket, UsageQuery, UsageSummary, UsageUser } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'

const DAY_MS = 86_400_000

type WindowKey = '7' | '30' | '90' | '366'
type SortKey = 'requests' | 'bytes'
type SortDirection = 'asc' | 'desc'

interface SortState {
  key: SortKey
  dir: SortDirection
}

const WINDOWS: Array<{ value: WindowKey; days: number; labelKey: TranslationKey }> = [
  { value: '7', days: 7, labelKey: 'usage.range7' },
  { value: '30', days: 30, labelKey: 'usage.range30' },
  { value: '90', days: 90, labelKey: 'usage.range90' },
  { value: '366', days: 366, labelKey: 'usage.range366' },
]

/** Rolling UTC-day window: the daemon floors both bounds to calendar days. */
function windowQuery(days: number): UsageQuery {
  const now = new Date()
  const endOfToday = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate())
  const start = endOfToday - (days - 1) * DAY_MS
  return { from: new Date(start).toISOString(), to: now.toISOString() }
}

/**
 * The equal-length window immediately before `summary`, in whole UTC days.
 * Bounds are RFC 3339 with an explicit end-of-day `to`: a date-only `to` is
 * read as midnight by the daemon, which would drop the baseline's last day.
 */
function previousWindow(summary: UsageSummary): UsageQuery | null {
  const fromMs = Date.parse(summary.from)
  const toMs = Date.parse(summary.to)
  if (!Number.isFinite(fromMs) || !Number.isFinite(toMs) || toMs < fromMs) return null
  const start = Math.floor(fromMs / DAY_MS) * DAY_MS
  const end = Math.floor(toMs / DAY_MS) * DAY_MS
  const count = Math.round((end - start) / DAY_MS) + 1
  if (count < 1) return null
  return {
    from: new Date(start - count * DAY_MS).toISOString(),
    to: new Date(start - 1).toISOString(),
  }
}

type Translate = (key: string, params?: TranslationParams) => string

/**
 * Honest history label: the store keeps a bounded, newest-first history, so a
 * wide window is never full coverage. Null when the daemon reports no cap.
 */
function retentionNoteFor(summary: UsageSummary, t: Translate): string | null {
  const rows = summary.rowsRetained ?? 0
  if (rows <= 0) return null
  const days = summary.retentionDays ?? 0
  const params = { rows: formatNumber(rows) }
  if (days > 1) return t('usage.retentionNote', { ...params, days: formatNumber(days) })
  if (days === 1) return t('usage.retentionNoteOne', params)
  return t('usage.retentionNoteNoDays', params)
}

/** Request-weighted mean, matching the daemon's per-day averages. */
function weightedLatency(days: UsageBucket[]): number {
  const requests = days.reduce((total, day) => total + day.requests, 0)
  if (requests === 0) return 0
  const weighted = days.reduce((total, day) => total + day.avgLatencyMs * day.requests, 0)
  return Math.round(weighted / requests)
}

function changePercent(current: number, previous: number | undefined): number | null {
  if (previous === undefined || previous === 0) return null
  return ((current - previous) / previous) * 100
}

function formatChange(value: number | null): string | undefined {
  if (value === null) return undefined
  const rounded = Math.abs(value) < 0.05 ? 0 : value
  return `${rounded > 0 ? '+' : ''}${rounded.toFixed(1)}%`
}

/** `goodWhen` says which direction should read as positive on a metric. */
function trendFor(value: number | null, goodWhen: 'up' | 'down'): 'up' | 'down' | 'flat' {
  if (value === null || Math.abs(value) < 0.05) return 'flat'
  return (goodWhen === 'up') === value > 0 ? 'up' : 'down'
}

function sortRows<T extends { requests: number; bytes: number }>(rows: T[], sort: SortState): T[] {
  return [...rows].sort(
    (left, right) =>
      (left[sort.key] - right[sort.key]) * (sort.dir === 'asc' ? 1 : -1),
  )
}

function nextSort(current: SortState, key: SortKey): SortState {
  if (current.key === key) {
    return { key, dir: current.dir === 'desc' ? 'asc' : 'desc' }
  }
  return { key, dir: 'desc' }
}

/** Defensive host-only label: never a scheme, path, query, port or userinfo. */
function hostOnly(target: string): string {
  const value = target.trim()
  if (!value) return '—'
  if (value.includes('://')) {
    try {
      return new URL(value).hostname || '—'
    } catch {
      // Fall through to the authority parser.
    }
  }
  const authority = value.split(/[/?#]/, 1)[0] ?? value
  const withoutUser = authority.includes('@')
    ? authority.slice(authority.lastIndexOf('@') + 1)
    : authority
  if (withoutUser.startsWith('[')) {
    const close = withoutUser.indexOf(']')
    return close > 0 ? withoutUser.slice(0, close + 1) : withoutUser
  }
  return withoutUser.split(':')[0] || '—'
}

function SortHeader({
  label,
  sortKey,
  sort,
  onSort,
}: {
  label: string
  sortKey: SortKey
  sort: SortState
  onSort: (key: SortKey) => void
}) {
  const { t } = useI18n()
  const active = sort.key === sortKey
  return (
    <button
      type="button"
      className={clsx('sort-btn', active && 'is-active')}
      onClick={() => onSort(sortKey)}
      aria-label={t('usage.sortBy', { column: label })}
    >
      {label}
      <span className="sort-btn__mark" aria-hidden="true">
        {active ? (sort.dir === 'desc' ? '▼' : '▲') : '↕'}
      </span>
    </button>
  )
}

function UsageSkeleton() {
  return (
    <div className="stack-lg" aria-busy="true">
      <div className="metric-grid">
        {Array.from({ length: 4 }, (_, index) => (
          <div className="metric" key={index}>
            <Skeleton width={90} height={10} />
            <Skeleton width={120} height={28} />
            <Skeleton width={70} height={10} />
          </div>
        ))}
      </div>
      <Card>
        <Skeleton height={220} />
      </Card>
      <TableSkeleton rows={5} columns={5} />
    </div>
  )
}

export default function Usage() {
  const { t } = useI18n()
  const [windowKey, setWindowKey] = useState<WindowKey>('30')
  const [account, setAccount] = useState('all')
  const [accountLabels, setAccountLabels] = useState<string[]>([])
  const [accountSort, setAccountSort] = useState<SortState>({ key: 'bytes', dir: 'desc' })
  const [userSort, setUserSort] = useState<SortState>({ key: 'bytes', dir: 'desc' })

  const days = WINDOWS.find((entry) => entry.value === windowKey)?.days ?? 30

  const usage = useAsync(
    async () => {
      const filter = account === 'all' ? {} : { account }
      const summary = await api.getUsage({ ...windowQuery(days), ...filter })
      let previous: UsageSummary | undefined
      const previousQuery = previousWindow(summary)
      if (previousQuery) {
        try {
          previous = await api.getUsage({ ...previousQuery, ...filter })
        } catch {
          // The current window is still useful without a baseline.
          previous = undefined
        }
      }
      return { summary, previous }
    },
    [days, account],
  )

  const summary = usage.data?.summary
  const previous = usage.data?.previous

  // Keep the filter menu stable while a filtered query only returns one account.
  useEffect(() => {
    if (account !== 'all' || !summary) return
    setAccountLabels((current) => {
      const next = new Set(current)
      for (const entry of summary.accounts) next.add(entry.account)
      if (next.size === current.length) return current
      return [...next].sort((left, right) => left.localeCompare(right))
    })
  }, [account, summary])

  const requests = summary?.requests ?? 0
  const bytes = summary?.bytes ?? 0
  const errors = summary?.errors ?? 0
  const avgLatency = weightedLatency(summary?.days ?? [])
  const errorRate = requests > 0 ? (errors / requests) * 100 : 0

  // The store keeps a bounded history, so a wide window is not full coverage.
  const retentionNote = summary ? retentionNoteFor(summary, t) : null
  const quotaEnforced = summary?.quotaEnforced === true

  const deltaRequests = changePercent(requests, previous?.requests)
  const deltaBytes = changePercent(bytes, previous?.bytes)
  const deltaErrors = changePercent(errors, previous?.errors)
  const deltaLatency = changePercent(avgLatency, previous ? weightedLatency(previous.days) : undefined)

  const chartPoints = useMemo(
    () => (summary ? fillUsageDays(summary.days, summary.from, summary.to) : []),
    [summary],
  )
  const accountRows = useMemo(
    () => sortRows(summary?.accounts ?? [], accountSort),
    [summary, accountSort],
  )
  const userRows = useMemo(
    () => sortRows(summary?.users ?? [], userSort),
    [summary, userSort],
  )

  const accountColumns: Array<Column<UsageAccount>> = [
    {
      key: 'account',
      label: t('platform.usage.accounts'),
      render: (row) => <span className="cell-strong">{row.account}</span>,
    },
    {
      key: 'requests',
      label: (
        <SortHeader
          label={t('platform.usage.requests')}
          sortKey="requests"
          sort={accountSort}
          onSort={(key) => setAccountSort((current) => nextSort(current, key))}
        />
      ),
      align: 'right',
      render: (row) => <span className="cell-mono">{formatNumber(row.requests)}</span>,
    },
    {
      key: 'bytes',
      label: (
        <SortHeader
          label={t('platform.usage.bytes')}
          sortKey="bytes"
          sort={accountSort}
          onSort={(key) => setAccountSort((current) => nextSort(current, key))}
        />
      ),
      align: 'right',
      render: (row) => <span className="cell-mono">{formatBytes(row.bytes)}</span>,
    },
    {
      key: 'errors',
      label: t('platform.usage.errors'),
      align: 'right',
      render: (row) => (
        <span className={clsx('cell-mono', row.errors > 0 && 'usage-error-count')}>
          {formatNumber(row.errors)}
        </span>
      ),
    },
    {
      key: 'latency',
      label: t('platform.usage.avgLatency'),
      align: 'right',
      render: (row) => <span className="cell-mono">{formatNumber(row.avgLatencyMs)} ms</span>,
    },
  ]

  // Enforced budgets add a live monthly meter per account. The wire numbers
  // are month-to-date gate bytes, not the queried window, so the text is a
  // plain used/budget pair rather than the window-labelled user copy.
  if (quotaEnforced) {
    accountColumns.push({
      key: 'quota',
      label: t('usage.quotaEnforced'),
      render: (row) => {
        if (row.trafficBudgetBytes == null) {
          return <span className="cell-sub">{t('platform.usage.noQuota')}</span>
        }
        const used = row.usedBytes ?? 0
        const percentUsed = (used / row.trafficBudgetBytes) * 100
        return (
          <div className="meter-cell">
            <ProgressBar
              value={used}
              max={row.trafficBudgetBytes}
              tone={percentUsed >= 90 ? 'danger' : percentUsed >= 70 ? 'warning' : 'mint'}
              label={t('usage.quotaLabel', { name: row.account })}
            />
            <span className="cell-sub">
              {formatBytes(used)} / {formatBytes(row.trafficBudgetBytes)}
            </span>
          </div>
        )
      },
    })
  }

  const userColumns: Array<Column<UsageUser>> = [
    {
      key: 'user',
      label: t('platform.usage.users'),
      render: (row) => (
        <div className="cell-stack">
          <span className="cell-strong">{row.userName}</span>
          <span className="cell-sub mono">{row.userId}</span>
        </div>
      ),
    },
    {
      key: 'requests',
      label: (
        <SortHeader
          label={t('platform.usage.requests')}
          sortKey="requests"
          sort={userSort}
          onSort={(key) => setUserSort((current) => nextSort(current, key))}
        />
      ),
      align: 'right',
      render: (row) => <span className="cell-mono">{formatNumber(row.requests)}</span>,
    },
    {
      key: 'bytes',
      label: (
        <SortHeader
          label={t('platform.usage.bytes')}
          sortKey="bytes"
          sort={userSort}
          onSort={(key) => setUserSort((current) => nextSort(current, key))}
        />
      ),
      align: 'right',
      render: (row) => <span className="cell-mono">{formatBytes(row.bytes)}</span>,
    },
    {
      key: 'quota',
      label: quotaEnforced ? (
        t('usage.quotaEnforced')
      ) : (
        <Badge tone="warning" title={t('usage.quotaAdvisoryTooltip')}>
          {t('usage.quotaAdvisory')}
        </Badge>
      ),
      render: (row) => {
        if (row.quotaBytesMonth === null || row.quotaBytesMonth <= 0) {
          return <span className="cell-sub">{t('platform.usage.noQuota')}</span>
        }
        const percentUsed = (row.usedBytes / row.quotaBytesMonth) * 100
        return (
          <div className="meter-cell">
            <ProgressBar
              value={row.usedBytes}
              max={row.quotaBytesMonth}
              tone={percentUsed >= 90 ? 'danger' : percentUsed >= 70 ? 'warning' : 'mint'}
              label={t('usage.quotaLabel', { name: row.userName })}
            />
            <span className="cell-sub">
              {t('usage.quotaUsage', {
                used: formatBytes(row.usedBytes),
                quota: formatBytes(row.quotaBytesMonth),
              })}
            </span>
          </div>
        )
      },
    },
  ]

  const reload = usage.reload
  const windowOptions = WINDOWS.map((entry) => ({
    value: entry.value,
    label: t(entry.labelKey),
  }))

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('platform.usage.title')}
        subtitle={t('usage.subtitle')}
        refresh={summarizeRefresh([usage])}
        actions={
          <>
            <SegmentedControl
              label={t('usage.window')}
              value={windowKey}
              onChange={(value) => setWindowKey(value as WindowKey)}
              options={windowOptions}
            />
            <Select
              variant="filter"
              aria-label={t('usage.accountFilter')}
              value={account}
              onChange={setAccount}
              options={[
                { value: 'all', label: t('usage.allAccounts') },
                ...accountLabels.map((label) => ({ value: label, label })),
              ]}
            />
            <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={reload}>
              {t('common.refresh')}
            </Button>
          </>
        }
      />

      {usage.status === 'error' && !usage.data ? (
        <Card>
          <ErrorState
            message={
              scopeDenial(usage.failure, t, 'surface.usage', 'usage:read') ??
              usage.error ??
              t('common.unknownError')
            }
            onRetry={reload}
          />
        </Card>
      ) : usage.status === 'loading' && !usage.data ? (
        <UsageSkeleton />
      ) : summary ? (
        <>
          {retentionNote ? (
            <p className="page-note" role="note">
              {retentionNote}
            </p>
          ) : null}

          {summary.truncated ? (
            <div className="callout callout--warning" role="note">
              <span className="callout__icon">
                <AlertTriangle size={18} aria-hidden="true" />
              </span>
              <div>
                <p className="callout__title">{t('usage.truncatedTitle')}</p>
                <p className="callout__text">{t('platform.usage.truncated')}</p>
              </div>
            </div>
          ) : null}

          <div className="metric-grid">
            <MetricTile
              label={t('platform.usage.requests')}
              value={formatNumber(requests)}
              hint={t('usage.requestsHint')}
              delta={formatChange(deltaRequests)}
              trend={trendFor(deltaRequests, 'up')}
              icon={<Waypoints size={15} />}
              tone="mint"
            />
            <MetricTile
              label={t('platform.usage.bytes')}
              value={formatBytes(bytes)}
              hint={t('usage.bytesHint')}
              delta={formatChange(deltaBytes)}
              trend={trendFor(deltaBytes, 'up')}
              icon={<ArrowUpRight size={15} />}
              tone="info"
            />
            <MetricTile
              label={t('platform.usage.errors')}
              value={formatNumber(errors)}
              hint={t('usage.errorRateHint', { rate: errorRate.toFixed(1) })}
              delta={formatChange(deltaErrors)}
              trend={trendFor(deltaErrors, 'down')}
              icon={<AlertTriangle size={15} />}
              tone="warning"
            />
            <MetricTile
              label={t('platform.usage.avgLatency')}
              value={`${formatNumber(avgLatency)} ms`}
              hint={t('usage.latencyHint', { count: formatNumber(requests) })}
              delta={formatChange(deltaLatency)}
              trend={trendFor(deltaLatency, 'down')}
              icon={<Timer size={15} />}
              tone="success"
            />
          </div>

          {previous ? (
            <p className="page-note">
              {t('usage.deltaNote', { days: chartPoints.length || days })}
            </p>
          ) : null}

          {requests === 0 ? (
            <Card>
              <EmptyState
                title={t('platform.usage.empty')}
                description={t('usage.emptyDesc')}
                icon={<Activity size={20} aria-hidden="true" />}
              />
            </Card>
          ) : (
            <>
              <Card
                title={t('platform.usage.days')}
                subtitle={t('usage.chartSubtitle')}
              >
                <UsageChart points={chartPoints} />
              </Card>

              <div className="grid-2-1">
                <Card
                  title={t('platform.usage.accounts')}
                  subtitle={t('usage.accountsSubtitle')}
                  bodyClassName="card__body--flush"
                >
                  <DataTable
                    caption={t('usage.captionAccounts')}
                    columns={accountColumns}
                    rows={accountRows}
                    rowKey={(row) => row.account}
                    empty={<EmptyState title={t('usage.accountsEmpty')} />}
                  />
                </Card>

                <Card title={t('platform.usage.topTargets')} subtitle={t('usage.targetsSubtitle')}>
                  {(summary.topTargets.length ?? 0) === 0 ? (
                    <EmptyState title={t('usage.targetsEmpty')} />
                  ) : (
                    <ul className="health-list" aria-label={t('usage.captionTargets')}>
                      {summary.topTargets.map((target) => (
                        <li className="usage-target" key={target.target}>
                          <div className="health-row__top">
                            <span className="health-row__label mono">{hostOnly(target.target)}</span>
                            <span className="health-row__score mono">
                              {formatBytes(target.bytes)}
                            </span>
                          </div>
                          <div className="health-row__bottom">
                            <span className="cell-sub">
                              {t('usage.targetRequests', {
                                count: formatNumber(target.requests),
                              })}
                            </span>
                          </div>
                        </li>
                      ))}
                    </ul>
                  )}
                </Card>
              </div>

              <Card
                title={t('platform.usage.users')}
                subtitle={t('usage.usersSubtitle')}
                bodyClassName="card__body--flush"
              >
                <DataTable
                  caption={t('usage.captionUsers')}
                  columns={userColumns}
                  rows={userRows}
                  rowKey={(row) => row.userId}
                  empty={<EmptyState title={t('usage.usersEmpty')} />}
                />
              </Card>
            </>
          )}
        </>
      ) : null}
    </div>
  )
}
