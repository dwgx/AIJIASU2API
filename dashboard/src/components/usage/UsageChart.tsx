import {
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { formatBytes, formatCompact, formatNumber } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type { UsageBucket } from '../../lib/types'

const DAY_MS = 86_400_000
const MAX_FILL_DAYS = 400

export interface UsageDayPoint {
  day: string
  requests: number
  bytes: number
  errors: number
  avgLatencyMs: number
}

/**
 * Builds a continuous UTC-day series for the chart. The daemon already emits
 * every day in the window, but a missing bucket must render as zero rather
 * than collapsing the axis, so the range comes from the window bounds.
 */
export function fillUsageDays(
  days: UsageBucket[],
  from: string,
  to: string,
): UsageDayPoint[] {
  const fromMs = Date.parse(from)
  const toMs = Date.parse(to)
  if (!Number.isFinite(fromMs) || !Number.isFinite(toMs) || toMs < fromMs) {
    return [...days].sort((left, right) => left.day.localeCompare(right.day))
  }

  const start = Math.floor(fromMs / DAY_MS) * DAY_MS
  const end = Math.floor(toMs / DAY_MS) * DAY_MS
  const count = Math.round((end - start) / DAY_MS) + 1
  if (count < 1 || count > MAX_FILL_DAYS) {
    return [...days].sort((left, right) => left.day.localeCompare(right.day))
  }

  const byDay = new Map(days.map((bucket) => [bucket.day, bucket]))
  const points: UsageDayPoint[] = []
  for (let index = 0; index < count; index += 1) {
    const day = new Date(start + index * DAY_MS).toISOString().slice(0, 10)
    const bucket = byDay.get(day)
    points.push(
      bucket
        ? {
            day: bucket.day,
            requests: bucket.requests,
            bytes: bucket.bytes,
            errors: bucket.errors,
            avgLatencyMs: bucket.avgLatencyMs,
          }
        : { day, requests: 0, bytes: 0, errors: 0, avgLatencyMs: 0 },
    )
  }
  return points
}

function UsageTooltip({
  active,
  payload,
}: {
  active?: boolean
  payload?: ReadonlyArray<{ payload?: UsageDayPoint }>
}) {
  const { t } = useI18n()
  const point = payload?.[0]?.payload
  if (!active || !point) return null
  return (
    <div className="usage-tip">
      <p className="usage-tip__day mono">{point.day}</p>
      <dl className="usage-tip__rows">
        <div>
          <dt>{t('platform.usage.requests')}</dt>
          <dd>{formatNumber(point.requests)}</dd>
        </div>
        <div>
          <dt>{t('platform.usage.bytes')}</dt>
          <dd>{formatBytes(point.bytes)}</dd>
        </div>
        <div>
          <dt>{t('platform.usage.errors')}</dt>
          <dd>{formatNumber(point.errors)}</dd>
        </div>
        <div>
          <dt>{t('platform.usage.avgLatency')}</dt>
          <dd>{formatNumber(point.avgLatencyMs)} ms</dd>
        </div>
      </dl>
    </div>
  )
}

export function UsageChart({ points }: { points: UsageDayPoint[] }) {
  const { t } = useI18n()
  const requests = points.reduce((total, point) => total + point.requests, 0)
  const bytes = points.reduce((total, point) => total + point.bytes, 0)
  const zeroDays = points.filter((point) => point.requests === 0).length
  const ariaLabel = t('usage.chartAria', {
    days: points.length,
    requests: formatNumber(requests),
    bytes: formatBytes(bytes),
    zero: zeroDays,
  })

  return (
    <div className="chart" role="img" aria-label={ariaLabel}>
      <ResponsiveContainer width="100%" height={220}>
        <BarChart data={points} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
          <CartesianGrid stroke="var(--border)" vertical={false} />
          <XAxis
            dataKey="day"
            tickLine={false}
            axisLine={false}
            minTickGap={28}
            tick={{ fill: 'var(--muted)', fontSize: 11 }}
            tickFormatter={(value: string) => value.slice(5)}
          />
          <YAxis
            width={52}
            tickLine={false}
            axisLine={false}
            allowDecimals={false}
            tick={{ fill: 'var(--muted)', fontSize: 11 }}
            tickFormatter={(value: number) => formatCompact(value)}
          />
          <Tooltip
            cursor={{ fill: 'var(--wash)' }}
            content={<UsageTooltip />}
          />
          <Bar
            dataKey="requests"
            fill="var(--mint)"
            radius={[2, 2, 0, 0]}
            maxBarSize={28}
            isAnimationActive={false}
          />
        </BarChart>
      </ResponsiveContainer>
    </div>
  )
}
