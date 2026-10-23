import { Gauge, KeyRound, RefreshCw, ShieldCheck, Timer } from 'lucide-react'
import { api } from '../../lib/api'
import { formatDateTime } from '../../lib/format'
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

function enabledBadge(value: boolean, onLabel = 'on', offLabel = 'off') {
  return <Badge tone={value ? 'success' : 'warning'}>{value ? onLabel : offLabel}</Badge>
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
  const flags = useAsync(() => api.getLabFlags(), [], { intervalMs: 15_000 })

  if (!overview) {
    return error ? (
      <Card>
        <ErrorState message={error} onRetry={onRetry} />
      </Card>
    ) : (
      <Card title="Overview">
        <TableSkeleton rows={5} columns={4} />
      </Card>
    )
  }

  if (overview.enabled === false) {
    return (
      <div className="stack-lg">
        <DisabledCallout
          title="Lab telemetry is disabled"
          reason={overview.reason ?? 'The daemon did not wire a Lab reader.'}
        />
        <Card title="Generated at" subtitle="The daemon answered without a telemetry payload.">
          <p className="mono">{overview.generatedAt || 'unknown'}</p>
        </Card>
      </div>
    )
  }

  const flagColumns: Array<Column<FlagView>> = [
    {
      key: 'key',
      label: 'Flag',
      render: (flag) => <span className="cell-mono">{flag.key}</span>,
    },
    {
      key: 'value',
      label: 'Value',
      render: (flag) =>
        flag.secret ? (
          <Badge tone={flag.set ? 'warning' : 'neutral'}>{flagDisplay(flag)}</Badge>
        ) : (
          <span className="cell-mono">{flagDisplay(flag)}</span>
        ),
    },
    {
      key: 'source',
      label: 'Source',
      render: (flag) => <span className="cell-sub">{flag.source ?? '—'}</span>,
    },
    {
      key: 'restart',
      label: 'Restart',
      render: (flag) => (
        <Badge tone={flag.restartRequired ? 'warning' : 'neutral'}>
          {flag.restartRequired ? 'required' : 'no'}
        </Badge>
      ),
    },
  ]

  return (
    <div className="stack-lg">
      <div className="metric-grid">
        <MetricTile label="Accounts" value={overview.counts.accounts} hint="vendor logins in the store" icon={<KeyRound size={16} />} />
        <MetricTile label="Nodes" value={overview.counts.nodes} hint={`${overview.counts.liveNodes} live`} icon={<Gauge size={16} />} tone="info" />
        <MetricTile label="Sessions" value={overview.counts.sessions} hint="vendor FV sessions in memory" icon={<ShieldCheck size={16} />} tone="success" />
        <MetricTile label="Gates" value={overview.counts.gates} hint="listening proxies" icon={<Gauge size={16} />} tone="mint" />
        <MetricTile label="Leases" value={overview.counts.leases} hint="active user assignments" icon={<KeyRound size={16} />} tone="info" />
        <MetricTile label="Uptime" value={formatUptime(overview.uptimeSec)} hint={`generated ${formatDateTime(overview.generatedAt)}`} icon={<Timer size={16} />} tone="neutral" />
      </div>

      <div className="grid-2-1">
        <Card
          title="Daemon posture"
          subtitle="Effective modes from the running process, not build-time guesses."
          actions={<Badge tone="success">enabled</Badge>}
        >
          <LabFacts
            rows={[
              { label: 'Version', value: <span className="mono">{overview.version || 'unknown'}</span> },
              { label: 'Commit', value: <span className="mono">{overview.commit || 'not embedded'}</span> },
              { label: 'Uptime', value: <span className="mono">{formatUptime(overview.uptimeSec)}</span> },
              { label: 'Account login', value: enabledBadge(overview.modes.accountLogin) },
              { label: 'Resin', value: enabledBadge(overview.modes.resin) },
              { label: 'Update check', value: enabledBadge(overview.modes.updateCheck) },
              { label: 'Secrets', value: enabledBadge(overview.modes.secrets, 'sealed', 'unavailable') },
              { label: 'TLS', value: <Badge tone={overview.modes.certPin === 'pinned' ? 'success' : 'warning'}>{certPinLabel(overview.modes.certPin)}</Badge> },
            ]}
          />
        </Card>

        <Card title="Warnings" subtitle="State the operator must resolve before trusting a green dashboard.">
          <WarningList warnings={overview.warnings ?? []} />
        </Card>
      </div>

      <Card
        title="Effective flags"
        subtitle="Secret values are never serialised; only the set/not-set state leaves the daemon."
        actions={
          <Button size="sm" variant="secondary" icon={<RefreshCw size={13} />} onClick={flags.reload}>
            Refresh flags
          </Button>
        }
        bodyClassName="card__body--flush"
      >
        {flags.data?.enabled === false ? (
          <DisabledCallout
            title="Effective flags are unavailable"
            reason={flags.data.reason ?? 'The daemon did not expose its startup configuration.'}
          />
        ) : flags.status === 'error' ? (
          <ErrorState message={flags.error ?? 'Unknown error'} onRetry={flags.reload} />
        ) : flags.status === 'loading' && !flags.data ? (
          <TableSkeleton rows={8} columns={4} />
        ) : (
          <DataTable
            caption="Effective daemon flags"
            columns={flagColumns}
            rows={flags.data?.flags ?? []}
            rowKey={(flag) => flag.key}
          />
        )}
      </Card>

      {loading ? (
        <p className="cell-sub" role="status">
          Refreshing overview…
        </p>
      ) : null}
    </div>
  )
}
