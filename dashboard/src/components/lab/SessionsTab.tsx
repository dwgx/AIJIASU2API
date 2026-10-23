import { Activity, Gauge, RefreshCw } from 'lucide-react'
import { api } from '../../lib/api'
import { formatBytes, formatDateTime, formatNumber, formatRelative } from '../../lib/format'
import { formatAge, nodeStateMeta } from '../../lib/lab'
import type { GateView, SessionView } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  StatusDot,
  TableSkeleton,
} from '../primitives'
import { DisabledCallout } from './shared'

function GateTable({ gates }: { gates: GateView[] }) {
  const columns: Array<Column<GateView>> = [
    {
      key: 'node',
      label: 'Node',
      render: (gate) => (
        <div className="cell-stack">
          <span className="cell-strong mono">{gate.nodeId}</span>
          <span className="cell-sub mono">{gate.accountId}</span>
        </div>
      ),
    },
    {
      key: 'state',
      label: 'State',
      render: (gate) => {
        const meta = nodeStateMeta(gate.state)
        return (
          <span className="state-cell">
            <StatusDot tone={meta.tone} pulse={gate.state === 'LIVE'} />
            <Badge tone={meta.tone}>{meta.label}</Badge>
            {gate.draining ? <Badge tone="warning">draining</Badge> : null}
          </span>
        )
      },
    },
    {
      key: 'active',
      label: 'Active / limit',
      align: 'right',
      render: (gate) => (
        <span className="cell-mono">
          {formatNumber(gate.active)}/{formatNumber(gate.limit)}
        </span>
      ),
    },
    {
      key: 'accepted',
      label: 'Accepted',
      align: 'right',
      render: (gate) => <span className="cell-mono">{formatNumber(gate.accepted)}</span>,
    },
    {
      key: 'rejected',
      label: 'Rejected',
      align: 'right',
      render: (gate) => (
        <span className="cell-mono">
          <Badge tone={gate.rejected > 0 ? 'warning' : 'neutral'}>{formatNumber(gate.rejected)}</Badge>
        </span>
      ),
    },
    {
      key: 'ingress',
      label: 'Ingress',
      align: 'right',
      render: (gate) => <span className="cell-mono">{formatBytes(gate.ingressBytes)}</span>,
    },
    {
      key: 'egress',
      label: 'Egress',
      align: 'right',
      render: (gate) => <span className="cell-mono">{formatBytes(gate.egressBytes)}</span>,
    },
    {
      key: 'started',
      label: 'Started',
      render: (gate) =>
        gate.startedAt ? (
          <span className="cell-sub" title={formatDateTime(gate.startedAt)}>
            {formatRelative(gate.startedAt)}
          </span>
        ) : (
          <span className="cell-sub">—</span>
        ),
    },
  ]

  return (
    <DataTable
      caption="Gate counters per node"
      columns={columns}
      rows={gates}
      rowKey={(gate) => gate.nodeId}
      empty={<EmptyState title="No listening gates" description="A gate appears after a node reaches LIVE or DRAINING." />}
    />
  )
}

function SessionTable({ sessions }: { sessions: SessionView[] }) {
  const columns: Array<Column<SessionView>> = [
    {
      key: 'node',
      label: 'Node',
      render: (session) => (
        <div className="cell-stack">
          <span className="cell-strong mono">{session.nodeId}</span>
          <span className="cell-sub mono">{session.accountId}</span>
        </div>
      ),
    },
    {
      key: 'server',
      label: 'Server',
      render: (session) => <span className="cell-mono">{session.serverId}</span>,
    },
    {
      key: 'health',
      label: 'Health',
      render: (session) => (
        <span className="state-cell">
          <StatusDot tone={session.healthy ? 'success' : 'danger'} pulse={session.healthy} />
          <Badge tone={session.healthy ? 'success' : 'danger'}>{session.healthy ? 'healthy' : 'unhealthy'}</Badge>
          {session.errorClass ? <Badge tone="warning">{session.errorClass}</Badge> : null}
        </span>
      ),
    },
    {
      key: 'sid',
      label: 'SID (redacted)',
      render: (session) => <span className="cell-mono">{session.sid}</span>,
    },
    {
      key: 'age',
      label: 'Age',
      align: 'right',
      render: (session) => <span className="cell-mono">{formatAge(session.ageSec)}</span>,
    },
    {
      key: 'egress',
      label: 'Egress claim',
      render: (session) => (
        <span className="inline-badges">
          <span className="cell-mono">{session.egressClaimed || '—'}</span>
          <Badge tone="warning">unverified</Badge>
        </span>
      ),
    },
    {
      key: 'keepalive',
      label: 'Keepalive',
      render: (session) => (
        <Badge tone={session.keepalive.supported ? 'info' : 'neutral'}>
          {session.keepalive.supported ? 'supported' : 'not implemented'}
        </Badge>
      ),
    },
    {
      key: 'lastFlow',
      label: 'Last flow',
      render: (session) =>
        session.lastFlowAt ? (
          <span className="cell-sub" title={formatDateTime(session.lastFlowAt)}>
            {formatRelative(session.lastFlowAt)}
          </span>
        ) : (
          <span className="cell-sub">—</span>
        ),
    },
  ]

  return (
    <DataTable
      caption="Vendor sessions"
      columns={columns}
      rows={sessions}
      rowKey={(session) => session.nodeId}
      empty={
        <EmptyState
          title="No vendor session in memory"
          description="Sessions exist only after a successful login and are lost on daemon restart."
        />
      }
    />
  )
}

export function SessionsTab() {
  const sessions = useAsync(() => api.listLabSessions(), [], { intervalMs: 5_000 })
  const gates = useAsync(() => api.listLabGates(), [], { intervalMs: 5_000 })

  const sessionsDisabled = sessions.data !== undefined && !Array.isArray(sessions.data)
  const gatesDisabled = gates.data !== undefined && !Array.isArray(gates.data)

  const sessionRows = Array.isArray(sessions.data) ? sessions.data : []
  const gateRows = Array.isArray(gates.data) ? gates.data : []

  const disabled = sessionsDisabled || gatesDisabled
  const reason =
    (sessionsDisabled && sessions.data && 'reason' in sessions.data && sessions.data.reason) ||
    (gatesDisabled && gates.data && 'reason' in gates.data && gates.data.reason) ||
    'AJS_ACCOUNT_LOGIN is not 1'

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">Sessions &amp; gates</h2>
          <p className="lab-tab-subtitle">Read-only counters and vendor session state, joined by nodeId.</p>
        </div>
        <div className="lab-tab-actions">
          <Button
            variant="secondary"
            icon={<RefreshCw size={14} />}
            onClick={() => {
              sessions.reload()
              gates.reload()
            }}
          >
            Refresh
          </Button>
        </div>
      </div>

      {disabled ? (
        <DisabledCallout
          title="Session telemetry is disabled"
          reason={String(reason)}
          flag="AJS_ACCOUNT_LOGIN=1"
        />
      ) : (
        <>
      <Card
        title="Gates"
        subtitle="Accepted/rejected flows and byte counters from the in-memory gate state."
        actions={<Badge tone="info">{gateRows.length} gates</Badge>}
        bodyClassName="card__body--flush"
      >
        {gates.status === 'error' ? (
          <ErrorState message={gates.error ?? 'Unknown error'} onRetry={gates.reload} />
        ) : gates.status === 'loading' && !gates.data ? (
          <TableSkeleton rows={5} columns={8} />
        ) : (
          <GateTable gates={gateRows} />
        )}
      </Card>

      <Card
        title="Sessions"
        subtitle="SIDs are redacted server-side; the browser never receives the full value."
        actions={<Badge tone="info">{sessionRows.length} sessions</Badge>}
        bodyClassName="card__body--flush"
      >
        {sessions.status === 'error' ? (
          <ErrorState message={sessions.error ?? 'Unknown error'} onRetry={sessions.reload} />
        ) : sessions.status === 'loading' && !sessions.data ? (
          <TableSkeleton rows={5} columns={8} />
        ) : (
          <SessionTable sessions={sessionRows} />
        )}
      </Card>

      <div className="callout callout--muted">
        <span className="callout__icon">
          <Activity size={18} aria-hidden="true" />
        </span>
        <div>
          <p className="callout__title">Keepalive fields are not implemented</p>
          <p className="callout__text">
            <code className="mono">ka</code>, <code className="mono">kat</code> and{' '}
            <code className="mono">kapf</code> are reported as <code className="mono">null</code> with{' '}
            <code className="mono">supported:false</code>. The UI does not invent values.
          </p>
        </div>
      </div>

      <p className="cell-sub">
        <Gauge size={12} aria-hidden="true" /> Gate counters reset when a node restarts.
      </p>
        </>
      )}
    </div>
  )
}
