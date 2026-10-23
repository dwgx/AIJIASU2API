import { Gauge, KeyRound, RefreshCw, ShieldCheck, Timer } from 'lucide-react'
import { api } from '../../lib/api'
import { formatDateTime } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { certPinLabel, flagDisplay, formatUptime } from '../../lib/lab'
import type { FlagView, LabOverview } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import {
  Badge,
  Button,
  Card,
  ErrorState,
  MetricTile,
  TableSkeleton,
} from '../primitives'
import { LabFacts, WarningList, DisabledCallout } from './shared'

function enabledBadge(value: boolean, t: (key: string) => string, onKey = 'lab.on', offKey = 'lab.off') {
  return <Badge tone={value ? 'success' : 'warning'}>{value ? t(onKey) : t(offKey)}</Badge>
}

export function OverviewTab({
  overview,
  loading,
  error,
  onRetry,
}: {
  overview: LabOverview | undefined
  loading: boolean
  error: string | undefined
  onRetry: () => void
}) {
  const { t } = useI18n()
  const flags = useAsync(() => api.getLabFlags(), [], { intervalMs: 15_000 })

  if (!overview) {
    return error ? (
      <Card>
        <ErrorState message={error} onRetry={onRetry} />
      </Card>
    ) : (
      <Card title={t('lab.overview.loadingTitle')}>
        <TableSkeleton rows={5} columns={4} />
      </Card>
    )
  }

  if (overview.enabled === false) {
    return (
      <div className="stack-lg">
        <DisabledCallout
          title={t('lab.overview.disabledTitle')}
          reason={overview.reason ?? t('lab.overview.disabledReason')}
        />
        <Card title={t('lab.overview.generatedAt')} subtitle={t('lab.overview.generatedSubtitle')}>
          <p className="mono">{overview.generatedAt || t('common.unknown')}</p>
        </Card>
      </div>
    )
  }

  const flagColumns: Array<Column<FlagView>> = [
    {
      key: 'key',
      label: t('lab.overview.flag'),
      render: (flag) => <span className="cell-mono">{flag.key}</span>,
    },
    {
      key: 'value',
      label: t('lab.overview.value'),
      render: (flag) =>
        flag.secret ? (
          <Badge tone={flag.set ? 'warning' : 'neutral'}>{flagDisplay(flag, t)}</Badge>
        ) : (
          <span className="cell-mono">{flagDisplay(flag, t)}</span>
        ),
    },
    {
      key: 'source',
      label: t('lab.overview.source'),
      render: (flag) => <span className="cell-sub">{flag.source ?? '—'}</span>,
    },
    {
      key: 'restart',
      label: t('lab.overview.restart'),
      render: (flag) => (
        <Badge tone={flag.restartRequired ? 'warning' : 'neutral'}>
          {flag.restartRequired ? t('common.required') : t('common.no')}
        </Badge>
      ),
    },
  ]

  return (
    <div className="stack-lg">
      <div className="metric-grid">
        <MetricTile label={t('lab.overview.accounts')} value={overview.counts.accounts} hint={t('lab.overview.accountsHint')} icon={<KeyRound size={16} />} />
        <MetricTile label={t('lab.overview.nodes')} value={overview.counts.nodes} hint={t('lab.overview.nodesHint', { count: overview.counts.liveNodes })} icon={<Gauge size={16} />} tone="info" />
        <MetricTile label={t('lab.overview.sessions')} value={overview.counts.sessions} hint={t('lab.overview.sessionsHint')} icon={<ShieldCheck size={16} />} tone="success" />
        <MetricTile label={t('lab.overview.gates')} value={overview.counts.gates} hint={t('lab.overview.gatesHint')} icon={<Gauge size={16} />} tone="mint" />
        <MetricTile label={t('lab.overview.leases')} value={overview.counts.leases} hint={t('lab.overview.leasesHint')} icon={<KeyRound size={16} />} tone="info" />
        <MetricTile label={t('lab.overview.uptime')} value={formatUptime(overview.uptimeSec)} hint={t('lab.overview.uptimeHint', { time: formatDateTime(overview.generatedAt) })} icon={<Timer size={16} />} tone="neutral" />
      </div>

      <div className="grid-2-1">
        <Card
          title={t('lab.overview.posture')}
          subtitle={t('lab.overview.postureSubtitle')}
          actions={<Badge tone="success">{t('common.enabled')}</Badge>}
        >
          <LabFacts
            rows={[
              { label: t('lab.overview.version'), value: <span className="mono">{overview.version || t('common.unknown')}</span> },
              { label: t('lab.overview.commit'), value: <span className="mono">{overview.commit || t('common.notEmbedded')}</span> },
              { label: t('lab.overview.uptime'), value: <span className="mono">{formatUptime(overview.uptimeSec)}</span> },
              { label: t('lab.overview.accountLogin'), value: enabledBadge(overview.modes.accountLogin, t) },
              { label: t('lab.overview.resin'), value: enabledBadge(overview.modes.resin, t) },
              { label: t('lab.overview.updateCheck'), value: enabledBadge(overview.modes.updateCheck, t) },
              { label: t('lab.overview.secrets'), value: enabledBadge(overview.modes.secrets, t, 'lab.sealed', 'lab.unavailable') },
              { label: t('lab.overview.tls'), value: <Badge tone={overview.modes.certPin === 'pinned' ? 'success' : 'warning'}>{certPinLabel(overview.modes.certPin, t)}</Badge> },
            ]}
          />
        </Card>

        <Card title={t('lab.overview.warnings')} subtitle={t('lab.overview.warningsSubtitle')}>
          <WarningList warnings={overview.warnings ?? []} />
        </Card>
      </div>

      <Card
        title={t('lab.overview.flags')}
        subtitle={t('lab.overview.flagsSubtitle')}
        actions={
          <Button size="sm" variant="secondary" icon={<RefreshCw size={13} />} onClick={flags.reload}>
            {t('lab.overview.refreshFlags')}
          </Button>
        }
        bodyClassName="card__body--flush"
      >
        {flags.data?.enabled === false ? (
          <DisabledCallout
            title={t('lab.overview.flagsUnavailable')}
            reason={flags.data.reason ?? t('lab.overview.flagsUnavailableReason')}
          />
        ) : flags.status === 'error' ? (
          <ErrorState message={flags.error ?? t('common.unknownError')} onRetry={flags.reload} />
        ) : flags.status === 'loading' && !flags.data ? (
          <TableSkeleton rows={8} columns={4} />
        ) : (
          <DataTable
            caption={t('lab.overview.flagsCaption')}
            columns={flagColumns}
            rows={flags.data?.flags ?? []}
            rowKey={(flag) => flag.key}
          />
        )}
      </Card>

      {loading ? (
        <p className="cell-sub" role="status">
          {t('lab.refreshingOverview')}
        </p>
      ) : null}
    </div>
  )
}
