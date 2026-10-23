import { RefreshCw, Server } from 'lucide-react'
import { api } from '../../lib/api'
import { formatDateTime } from '../../lib/format'
import { nodeStateMeta, serverFlagLabel, serverStatusTone } from '../../lib/lab'
import type { ServerView } from '../../lib/types'
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

export function ServersTab({ accountId, accountLabel }: { accountId: string; accountLabel: string }) {
  const servers = useAsync(() => api.getAccountServers(accountId), [accountId], { intervalMs: 15_000 })

  const disabled = servers.data?.enabled === false
  const rows = servers.data?.servers ?? []

  const columns: Array<Column<ServerView>> = [
    {
      key: 'server',
      label: 'Server',
      render: (server) => (
        <div className="cell-stack">
          <span className="cell-strong">{server.title}</span>
          <span className="cell-sub mono">{server.id}</span>
        </div>
      ),
    },
    {
      key: 'region',
      label: 'Region',
      render: (server) => <span className="cell-mono">{server.region}</span>,
    },
    {
      key: 'host',
      label: 'Endpoint',
      render: (server) => (
        <span className="cell-mono">
          {server.host}:{server.port}
        </span>
      ),
    },
    {
      key: 'status',
      label: 'Status',
      render: (server) => <Badge tone={serverStatusTone(server.status)}>{server.status}</Badge>,
    },
    {
      key: 'node',
      label: 'Node',
      render: (server) => {
        if (!server.nodeId) return <span className="cell-sub">not linked</span>
        const meta = server.nodeState ? nodeStateMeta(server.nodeState) : null
        return (
          <div className="cell-stack">
            <span className="cell-mono">{server.nodeId}</span>
            {meta ? (
              <span className="state-cell">
                <StatusDot tone={meta.tone} pulse={meta.tone === 'success'} />
                <span className="cell-sub">{meta.label}</span>
              </span>
            ) : null}
          </div>
        )
      },
    },
    {
      key: 'selection',
      label: 'Selection',
      render: (server) => (
        <Badge tone={server.skippedByLimit ? 'warning' : server.selected ? 'success' : 'neutral'}>
          {serverFlagLabel(server)}
        </Badge>
      ),
    },
    {
      key: 'flags',
      label: 'Flags',
      render: (server) => (
        <span className="inline-badges">
          {server.fullLoad ? <Badge tone="warning">full load</Badge> : null}
          {server.memberOnly === true ? <Badge tone="info">member only</Badge> : null}
          {server.blockingMarks.length > 0 ? (
            <Badge tone="danger">{server.blockingMarks.join(', ')}</Badge>
          ) : null}
          {server.hasPingSecret ? <Badge tone="neutral">ping secret</Badge> : null}
        </span>
      ),
    },
    {
      key: 'latency',
      label: 'Latency',
      align: 'right',
      render: (server) =>
        server.latencyMs === null ? (
          <span className="cell-sub">—</span>
        ) : (
          <span className="cell-mono">{server.latencyMs} ms</span>
        ),
    },
  ]

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">Server catalog</h2>
          <p className="lab-tab-subtitle">
            Vendor catalog snapshot for {accountLabel || accountId}. Read-only; no vendor call is made here.
          </p>
        </div>
        <div className="lab-tab-actions">
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={servers.reload}>
            Refresh
          </Button>
        </div>
      </div>

      {disabled ? (
        <DisabledCallout
          title="Server catalog is disabled"
          reason={servers.data?.reason ?? 'Account login is not enabled.'}
          flag="AJS_ACCOUNT_LOGIN=1"
        />
      ) : (
        <>
          {servers.data ? (
            <div className="callout callout--muted">
              <span className="callout__icon">
                <Server size={18} aria-hidden="true" />
              </span>
              <div>
                <p className="callout__title">
                  {servers.data.stale ? 'Catalog snapshot is stale' : 'Catalog snapshot is fresh'}
                </p>
                <p className="callout__text">
                  {servers.data.fetchedAt
                    ? `Last login result held in memory since ${formatDateTime(servers.data.fetchedAt)}.`
                    : 'No login result is held in memory.'}{' '}
                  A daemon restart clears this catalog; this slice does not persist it and does not log in on
                  your behalf.
                </p>
              </div>
            </div>
          ) : null}

          <Card bodyClassName="card__body--flush">
            {servers.status === 'error' ? (
              <ErrorState message={servers.error ?? 'Unknown error'} onRetry={servers.reload} />
            ) : servers.status === 'loading' && !servers.data ? (
              <TableSkeleton rows={6} columns={8} />
            ) : (
              <DataTable
                caption="Vendor server catalog"
                columns={columns}
                rows={rows}
                rowKey={(server) => server.id}
                empty={
                  <EmptyState
                    title="No catalog in memory"
                    description="A server catalog exists only after a successful login. This read-only slice never triggers one; use the Accounts page to test a login."
                    icon={<Server size={20} aria-hidden="true" />}
                  />
                }
              />
            )}
          </Card>
        </>
      )}
    </div>
  )
}
