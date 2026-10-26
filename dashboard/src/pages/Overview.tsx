import { Activity, AlertTriangle, ArrowUpRight, Plus, RefreshCw, ScrollText, Server, Ticket, Waypoints } from 'lucide-react'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { api } from '../lib/api'
import { scopeDenial } from '../lib/errors'
import { formatBytes, formatCompact, formatDateTime, formatNumber, formatRelative, formatTime } from '../lib/format'
import { useI18n } from '../lib/i18n'
import { ACCOUNT_STATE, SEVERITY } from '../lib/labels'
import type { MetricRange } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { HealthPanel } from '../components/nodes/HealthPanel'
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  MetricTile,
  PageHeader,
  ProgressBar,
  SegmentedControl,
  Skeleton,
  StatusDot,
  StatusPill,
} from '../components/primitives'

function TrafficChart({ range, series }: { range: MetricRange; series: Array<{ t: string; ingress: number; egress: number }> }) {
  const { t } = useI18n()
  return (
    <div className="chart">
      <ResponsiveContainer width="100%" height={196}>
        <AreaChart data={series} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
          <defs>
            <linearGradient id="traffic-egress" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="var(--mint)" stopOpacity={0.32} />
              <stop offset="100%" stopColor="var(--mint)" stopOpacity={0.02} />
            </linearGradient>
            <linearGradient id="traffic-ingress" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="var(--info)" stopOpacity={0.18} />
              <stop offset="100%" stopColor="var(--info)" stopOpacity={0.01} />
            </linearGradient>
          </defs>
          <CartesianGrid stroke="var(--border)" vertical={false} />
          <XAxis
            dataKey="t"
            tickLine={false}
            axisLine={false}
            minTickGap={36}
            tick={{ fill: 'var(--muted)', fontSize: 11 }}
            tickFormatter={(value: string) =>
              range === '24h'
                ? formatTime(value).slice(0, 5)
                : new Intl.DateTimeFormat('en-GB', { weekday: 'short', hour: '2-digit' }).format(new Date(value))
            }
          />
          <YAxis
            width={58}
            tickLine={false}
            axisLine={false}
            tick={{ fill: 'var(--muted)', fontSize: 11 }}
            tickFormatter={(value: number) => formatBytes(value, 0)}
          />
          <Tooltip
            cursor={{ stroke: 'var(--border-strong)' }}
            contentStyle={{
              background: 'var(--elevated)',
              border: '1px solid var(--border-strong)',
              borderRadius: 8,
              fontSize: 12,
              color: 'var(--text)',
              boxShadow: 'var(--shadow-pop)',
            }}
            labelStyle={{ color: 'var(--text-2)', marginBottom: 4 }}
            labelFormatter={(value) => formatDateTime(String(value))}
            formatter={(value, name) => [
              formatBytes(Number(value)),
              name === 'egress' ? t('overview.egress') : t('overview.ingress'),
            ]}
          />
          <Area
            type="monotone"
            dataKey="ingress"
            stroke="var(--info)"
            strokeWidth={1.5}
            fill="url(#traffic-ingress)"
            name="ingress"
            isAnimationActive={false}
          />
          <Area
            type="monotone"
            dataKey="egress"
            stroke="var(--mint)"
            strokeWidth={2}
            fill="url(#traffic-egress)"
            name="egress"
            isAnimationActive={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )
}

function OverviewSkeleton() {
  const { t } = useI18n()
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
      <div className="grid-2-1">
        <Card title={t('overview.traffic')}>
          <Skeleton height={196} />
        </Card>
        <Card title={t('overview.accountHealth')}>
          <div className="stack">
            {Array.from({ length: 5 }, (_, index) => (
              <Skeleton key={index} height={32} />
            ))}
          </div>
        </Card>
      </div>
    </div>
  )
}

export default function Overview() {
  const [range, setRange] = useState<MetricRange>('24h')
  const navigate = useNavigate()
  const { lang, t } = useI18n()
  const metrics = useAsync(() => api.getMetrics(range), [range], { intervalMs: 10_000 })
  const accounts = useAsync(() => api.listAccounts(), [], { intervalMs: 15_000 })
  const nodes = useAsync(() => api.listNodes(), [], { intervalMs: 15_000 })
  const leases = useAsync(() => api.listLeases(), [], { intervalMs: 15_000 })
  const audit = useAsync(() => api.listAudit(4), [], { intervalMs: 15_000 })

  const states = [metrics, accounts, nodes, leases, audit]
  // A scoped key that may read metrics/accounts/nodes/leases but not the audit
  // log must not blank the dashboard: only the Activity card degrades.
  const auditDenial = scopeDenial(audit.failure, t, 'surface.audit', 'audit:read')
  const failure = states.find(
    (state) =>
      state.status === 'error' &&
      scopeDenial(state.failure, t, 'surface.audit', 'audit:read') === null,
  )
  const loading = states.some((state) => state.status === 'loading')

  const reloadAll = () => {
    metrics.reload()
    accounts.reload()
    nodes.reload()
    leases.reload()
    audit.reload()
  }

  const liveNodes = nodes.data?.filter((node) => node.state === 'LIVE').length ?? 0
  const totalNodes = nodes.data?.length ?? 0
  const stickyLeases = leases.data?.filter((lease) => lease.sticky).length ?? 0
  const series = metrics.data?.series ?? []
  const telemetryAvailable = metrics.data?.telemetryAvailable === true
  const hasTraffic = telemetryAvailable && series.length > 0
  const spark = hasTraffic ? series.slice(-16).map((point) => point.egress) : []

  const quickActions = [
    { label: t('overview.addAccount'), hint: t('overview.addAccountHint'), icon: Plus, to: '/accounts?new=1' },
    { label: t('overview.reviewLeases'), hint: t('overview.reviewLeasesHint'), icon: Ticket, to: '/leases' },
    { label: t('overview.inspectRequests'), hint: t('overview.inspectRequestsHint'), icon: Waypoints, to: '/requests' },
    { label: t('overview.openLogs'), hint: t('overview.openLogsHint'), icon: ScrollText, to: '/logs' },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('overview.title')}
        subtitle={t('overview.subtitle')}
        refresh={summarizeRefresh(states)}
        actions={
          telemetryAvailable ? (
            <SegmentedControl
              label={t('overview.range')}
              value={range}
              onChange={setRange}
              options={[
                { value: '24h', label: '24h' },
                { value: '7d', label: '7d' },
              ]}
            />
          ) : undefined
        }
      />

      {failure ? (
        <Card>
          <ErrorState message={failure.error ?? t('common.unknownError')} onRetry={reloadAll} />
        </Card>
      ) : loading ? (
        <OverviewSkeleton />
      ) : (
        <>
          <div className={telemetryAvailable ? 'metric-grid' : 'metric-grid metric-grid--2'}>
            <MetricTile
              label={t('overview.liveNodes')}
              value={`${liveNodes}/${totalNodes}`}
              hint={t('overview.drainingReady', {
                draining: nodes.data?.filter((node) => node.state === 'DRAINING').length ?? 0,
                ready: nodes.data?.filter((node) => node.state === 'READY').length ?? 0,
              })}
              icon={<Server size={15} />}
              spark={hasTraffic ? spark : undefined}
              tone="mint"
            />
            <MetricTile
              label={t('overview.activeLeases')}
              value={formatNumber(leases.data?.length ?? 0)}
              hint={t('overview.stickyHint', { count: stickyLeases })}
              icon={<Ticket size={15} />}
              spark={hasTraffic ? spark.map((value) => value * 0.92) : undefined}
              tone="info"
            />
            {telemetryAvailable ? (
              <>
                <MetricTile
                  label={t('overview.requestsRange', { range })}
                  value={formatCompact(metrics.data?.totals.requests ?? 0)}
                  hint={`p95 ${metrics.data?.totals.p95LatencyMs ?? 0} ms`}
                  icon={<Waypoints size={15} />}
                  spark={hasTraffic ? spark.map((value) => value * 0.7) : undefined}
                  tone="success"
                />
                <MetricTile
                  label={t('overview.egressRange', { range })}
                  value={formatBytes(metrics.data?.totals.egressBytes ?? 0)}
                  hint={t('overview.ingressHint', { bytes: formatBytes(metrics.data?.totals.ingressBytes ?? 0) })}
                  icon={<ArrowUpRight size={15} />}
                  spark={hasTraffic ? spark.map((value) => value * 1.05) : undefined}
                  tone="warning"
                />
              </>
            ) : null}
          </div>

          <HealthPanel variant="compact" />

          <div className="grid-2-1">
            <Card
              title={t('overview.traffic')}
              subtitle={t('overview.trafficSubtitle')}
              actions={
                hasTraffic ? (
                  <div className="legend">
                    <span className="legend__item">
                      <span className="legend__swatch legend__swatch--mint" /> {t('overview.egress')}
                    </span>
                    <span className="legend__item">
                      <span className="legend__swatch legend__swatch--info" /> {t('overview.ingress')}
                    </span>
                  </div>
                ) : (
                  <Badge tone="warning">{t('overview.notMeasured')}</Badge>
                )
              }
            >
              {hasTraffic ? (
                <TrafficChart range={range} series={series} />
              ) : (
                <EmptyState
                  title={t('overview.telemetryEmptyTitle')}
                  description={t('overview.telemetryEmptyDesc')}
                  icon={<Activity size={20} aria-hidden="true" />}
                />
              )}
            </Card>

            <Card
              title={t('overview.accountHealth')}
              subtitle={t('overview.accountHealthSubtitle')}
              actions={
                <Link className="link" to="/accounts">
                  {t('overview.manage')}
                </Link>
              }
            >
              {(metrics.data?.accountHealth.length ?? 0) === 0 ? (
                <EmptyState title={t('overview.accountHealthEmpty')} />
              ) : (
                <ul className="health-list">
                  {metrics.data?.accountHealth.map((entry) => {
                    const state = ACCOUNT_STATE[entry.state]
                    return (
                      <li key={entry.accountId} className="health-row">
                        <div className="health-row__top">
                          <span className="health-row__label">{entry.label}</span>
                          <span className="health-row__score mono">{entry.score}</span>
                        </div>
                        <div className="health-row__bottom">
                          <ProgressBar
                            value={entry.score}
                            tone={state.tone}
                            label={t('overview.healthLabel', { name: entry.label })}
                          />
                          <StatusPill status={state.status}>{t(state.labelKey)}</StatusPill>
                        </div>
                      </li>
                    )
                  })}
                </ul>
              )}
            </Card>
          </div>

          <div className="grid-2-1">
            <Card
              title={t('overview.activity')}
              subtitle={t('overview.activitySubtitle')}
              actions={
                <Link className="link" to="/logs">
                  {t('overview.viewAll')}
                </Link>
              }
            >
              {auditDenial ? (
                <div className="callout callout--warning" role="note">
                  <span className="callout__icon">
                    <AlertTriangle size={18} aria-hidden="true" />
                  </span>
                  <div>
                    <p className="callout__title">{t('common.couldNotLoad')}</p>
                    <p className="callout__text">{auditDenial}</p>
                  </div>
                </div>
              ) : (
                <ul className="activity-list">
                  {audit.data?.map((event) => (
                    <li key={event.id} className="activity-row">
                      <StatusDot tone={SEVERITY[event.severity].tone} />
                      <div className="activity-row__body">
                        <span className="activity-row__action mono">{event.action}</span>
                        <span className="activity-row__target">{event.target}</span>
                      </div>
                      <span className="activity-row__time">{formatRelative(event.ts, Date.now(), lang)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </Card>

            <Card
              title={t('overview.quickActions')}
              subtitle={t('overview.quickActionsSubtitle')}
              actions={
                <IconButton
                  label={t('overview.refreshSnapshot')}
                  icon={<RefreshCw size={14} />}
                  onClick={reloadAll}
                />
              }
            >
              <div className="quick-grid">
                {quickActions.map((action) => (
                  <button
                    key={action.label}
                    type="button"
                    className="quick"
                    onClick={() => navigate(action.to)}
                  >
                    <span className="quick__icon">
                      <action.icon size={16} aria-hidden="true" />
                    </span>
                    <span className="quick__text">
                      <span className="quick__label">{action.label}</span>
                      <span className="quick__hint">{action.hint}</span>
                    </span>
                  </button>
                ))}
              </div>
            </Card>
          </div>
        </>
      )}
    </div>
  )
}
