import { Activity, AlertTriangle, ArrowDownUp, Timer } from 'lucide-react'
import { useMemo, useState } from 'react'
import {
  Card,
  ErrorState,
  MetricTile,
  SegmentedControl,
  Skeleton,
} from '../../components/primitives'
import { UsageChart, fillUsageDays } from '../../components/usage/UsageChart'
import { formatBytes, formatNumber } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type { TranslationKey } from '../../lib/i18n'
import { portalApi } from '../../lib/portal-api'
import { useAsync } from '../../lib/useAsync'

const DAY_MS = 86_400_000

type WindowKey = '7' | '30' | '90'

const WINDOWS: Array<{ value: WindowKey; days: number; labelKey: TranslationKey }> = [
  { value: '7', days: 7, labelKey: 'portal.usage.range7' },
  { value: '30', days: 30, labelKey: 'portal.usage.range30' },
  { value: '90', days: 90, labelKey: 'portal.usage.range90' },
]

/** Rolling UTC-day window, mirroring the admin usage page's bounds. */
function windowQuery(days: number): { from: string; to: string } {
  const now = new Date()
  const endOfToday = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate())
  const start = endOfToday - (days - 1) * DAY_MS
  return { from: new Date(start).toISOString(), to: now.toISOString() }
}

/**
 * Own totals and a zero-filled daily series. The daemon derives the owner
 * from the session, so this view never sends an account or user filter.
 */
export default function PortalUsage() {
  const { t } = useI18n()
  const [windowKey, setWindowKey] = useState<WindowKey>('30')
  const query = useMemo(
    () => windowQuery(WINDOWS.find((item) => item.value === windowKey)?.days ?? 30),
    [windowKey],
  )
  const usage = useAsync(() => portalApi.usage(query), [query.from, query.to])
  const summary = usage.data
  const days = summary?.days ?? []
  const requests = summary?.requests ?? 0
  const latency =
    requests > 0
      ? Math.round(
          days.reduce((total, day) => total + day.avgLatencyMs * day.requests, 0) / requests,
        )
      : 0
  const points = summary ? fillUsageDays(summary.days, summary.from, summary.to) : []

  const rangeLabel = t(WINDOWS.find((item) => item.value === windowKey)?.labelKey ?? 'portal.usage.range30')

  return (
    <div className="stack-lg">
      {usage.status === 'loading' ? (
        <div className="stack-lg" aria-busy="true" aria-label={t('portal.dashboard.loading')}>
          <div className="metric-grid">
            {Array.from({ length: 4 }, (_, index) => (
              <div className="metric" key={index}>
                <Skeleton width={90} height={10} />
                <Skeleton width={120} height={28} />
              </div>
            ))}
          </div>
          <Card>
            <Skeleton height={220} />
          </Card>
        </div>
      ) : usage.status === 'error' ? (
        <ErrorState message={t('portal.usage.loadFailed')} onRetry={usage.reload} />
      ) : (
        <>
          <div className="metric-grid">
            <MetricTile
              label={t('portal.usage.requests')}
              value={formatNumber(requests)}
              hint={rangeLabel}
              icon={<Activity size={15} />}
            />
            <MetricTile
              label={t('portal.usage.bytes')}
              value={formatBytes(summary?.bytes ?? 0)}
              hint={rangeLabel}
              icon={<ArrowDownUp size={15} />}
            />
            <MetricTile
              label={t('portal.usage.errors')}
              value={formatNumber(summary?.errors ?? 0)}
              hint={rangeLabel}
              icon={<AlertTriangle size={15} />}
            />
            <MetricTile
              label={t('portal.usage.latency')}
              value={`${formatNumber(latency)} ms`}
              hint={rangeLabel}
              icon={<Timer size={15} />}
            />
          </div>
          <Card
            title={t('portal.usage.chartTitle')}
            subtitle={t('portal.usage.chartSubtitle')}
            actions={
              <SegmentedControl
                label={t('portal.usage.range')}
                options={WINDOWS.map((item) => ({
                  value: item.value,
                  label: t(item.labelKey),
                }))}
                value={windowKey}
                onChange={setWindowKey}
              />
            }
          >
            <UsageChart points={points} />
          </Card>
        </>
      )}
    </div>
  )
}
