import { ArrowUpRight, Plus, RefreshCw, ScrollText, Server, Ticket, Waypoints } from 'lucide-react'
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
import { formatBytes, formatCompact, formatDateTime, formatNumber, formatRelative, formatTime } from '../lib/format'
import { ACCOUNT_STATE, SEVERITY } from '../lib/labels'
import type { MetricRange } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import {
  Badge,
  Card,
  ErrorState,
  IconButton,
  MetricTile,
  PageHeader,
  ProgressBar,
  SegmentedControl,
  Skeleton,
  StatusDot,
} from '../components/primitives'

function TrafficChart({ range, series }: { range: MetricRange; series: Array<{ t: string; ingress: number; egress: number }> }) {
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
            formatter={(value, name) => [formatBytes(Number(value)), name === 'egress' ? 'Egress' : 'Ingress']}
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
        <Card title="Traffic">
          <Skeleton height={196} />
        </Card>
        <Card title="Account health">
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
  const metrics = useAsync(() => api.getMetrics(range), [range], { intervalMs: 10_000 })
  const accounts = useAsync(() => api.listAccounts(), [], { intervalMs: 15_000 })
  const nodes = useAsync(() => api.listNodes(), [], { intervalMs: 15_000 })
  const leases = useAsync(() => api.listLeases(), [], { intervalMs: 15_000 })
  const audit = useAsync(() => api.listAudit(4), [], { intervalMs: 15_000 })

  const states = [metrics, accounts, nodes, leases, audit]
  const failure = states.find((state) => state.status === 'error')
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
  const spark = metrics.data?.series.slice(-16).map((point) => point.egress) ?? []

  const quickActions = [
    { label: 'Add account', hint: 'Login + concurrency', icon: Plus, to: '/accounts?new=1' },
    { label: 'Review leases', hint: 'Sticky egress', icon: Ticket, to: '/leases' },
    { label: 'Inspect requests', hint: 'Targets & latency', icon: Waypoints, to: '/requests' },
    { label: 'Open logs', hint: 'Audit trail', icon: ScrollText, to: '/logs' },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title="Overview"
        subtitle="Fleet health, traffic and recent operator activity."
        refresh={summarizeRefresh(states)}
        actions={
          <SegmentedControl
            label="Traffic range"
            value={range}
            onChange={setRange}
            options={[
              { value: '24h', label: '24h' },
              { value: '7d', label: '7d' },
            ]}
          />
        }
      />

      {failure ? (
        <Card>
          <ErrorState message={failure.error ?? 'Unknown error'} onRetry={reloadAll} />
        </Card>
      ) : loading ? (
        <OverviewSkeleton />
      ) : (
        <>
          <div className="metric-grid">
            <MetricTile
              label="Live nodes"
              value={`${liveNodes}/${totalNodes}`}
              hint={`${nodes.data?.filter((node) => node.state === 'DRAINING').length ?? 0} draining · ${nodes.data?.filter((node) => node.state === 'READY').length ?? 0} ready`}
              icon={<Server size={15} />}
              spark={spark}
              tone="mint"
            />
            <MetricTile
              label="Active leases"
              value={formatNumber(leases.data?.length ?? 0)}
              hint={`${stickyLeases} sticky · IP-anchored`}
              delta="2 new"
              trend="up"
              icon={<Ticket size={15} />}
              spark={spark.map((value) => value * 0.92)}
              tone="info"
            />
            <MetricTile
              label={`Requests · ${range}`}
              value={formatCompact(metrics.data?.totals.requests ?? 0)}
              hint={`p95 ${metrics.data?.totals.p95LatencyMs ?? 0} ms`}
              delta="12.4%"
              trend="up"
              icon={<Waypoints size={15} />}
              spark={spark.map((value) => value * 0.7)}
              tone="success"
            />
            <MetricTile
              label={`Egress · ${range}`}
              value={formatBytes(metrics.data?.totals.egressBytes ?? 0)}
              hint={`${formatBytes(metrics.data?.totals.ingressBytes ?? 0)} ingress`}
              delta="8.1%"
              trend="up"
              icon={<ArrowUpRight size={15} />}
              spark={spark.map((value) => value * 1.05)}
              tone="warning"
            />
          </div>

          <div className="grid-2-1">
            <Card
              title="Traffic"
              subtitle="Ingress and egress through the proxy gates."
              actions={
                <div className="legend">
                  <span className="legend__item">
                    <span className="legend__swatch legend__swatch--mint" /> Egress
                  </span>
                  <span className="legend__item">
                    <span className="legend__swatch legend__swatch--info" /> Ingress
                  </span>
                </div>
              }
            >
              <TrafficChart range={range} series={metrics.data?.series ?? []} />
            </Card>

            <Card
              title="Account health"
              subtitle="Composite of login state and node liveness."
              actions={
                <Link className="link" to="/accounts">
                  Manage
                </Link>
              }
            >
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
                          label={`${entry.label} health`}
                        />
                        <Badge tone={state.tone}>{state.label}</Badge>
                      </div>
                    </li>
                  )
                })}
              </ul>
            </Card>
          </div>

          <div className="grid-2-1">
            <Card
              title="Activity"
              subtitle="Latest control-plane events."
              actions={
                <Link className="link" to="/logs">
                  View all
                </Link>
              }
            >
              <ul className="activity-list">
                {audit.data?.map((event) => (
                  <li key={event.id} className="activity-row">
                    <StatusDot tone={SEVERITY[event.severity].tone} />
                    <div className="activity-row__body">
                      <span className="activity-row__action mono">{event.action}</span>
                      <span className="activity-row__target">{event.target}</span>
                    </div>
                    <span className="activity-row__time">{formatRelative(event.ts)}</span>
                  </li>
                ))}
              </ul>
            </Card>

            <Card
              title="Quick actions"
              subtitle="Common operator paths."
              actions={
                <IconButton
                  label="Refresh snapshot"
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
