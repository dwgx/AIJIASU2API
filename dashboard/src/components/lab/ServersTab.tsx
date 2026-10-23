import { RefreshCw, Server } from 'lucide-react'
import { api } from '../../lib/api'
import { formatDateTime } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
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
  const { t } = useI18n()
  const servers = useAsync(() => api.getAccountServers(accountId), [accountId], { intervalMs: 15_000 })

  const disabled = servers.data?.enabled === false
  const rows = servers.data?.servers ?? []

  const columns: Array<Column<ServerView>> = [
    {
      key: 'server',
      label: t('lab.sessions.server'),
      render: (server) => (
        <div className="cell-stack">
          <span className="cell-strong">{server.title}</span>
          <span className="cell-sub mono">{server.id}</span>
        </div>
      ),
    },
    {
      key: 'region',
      label: t('lab.servers.region'),
      render: (server) => <span className="cell-mono">{server.region}</span>,
    },
    {
      key: 'host',
      label: t('lab.servers.endpoint'),
      render: (server) => (
        <span className="cell-mono">
          {server.host}:{server.port}
        </span>
      ),
    },
    {
      key: 'status',
      label: t('lab.servers.status'),
      render: (server) => <Badge tone={serverStatusTone(server.status)}>{server.status}</Badge>,
    },
    {
      key: 'node',
      label: t('lab.servers.node'),
      render: (server) => {
        if (!server.nodeId) return <span className="cell-sub">{t('lab.servers.notLinked')}</span>
        const meta = server.nodeState ? nodeStateMeta(server.nodeState) : null
        return (
          <div className="cell-stack">
            <span className="cell-mono">{server.nodeId}</span>
            {meta ? (
              <span className="state-cell">
                <StatusDot tone={meta.tone} pulse={meta.tone === 'success'} />
                <span className="cell-sub">{meta.labelKey ? t(meta.labelKey) : meta.label}</span>
              </span>
            ) : null}
          </div>
        )
      },
    },
    {
      key: 'selection',
      label: t('lab.servers.selection'),
      render: (server) => (
        <Badge tone={server.skippedByLimit ? 'warning' : server.selected ? 'success' : 'neutral'}>
          {serverFlagLabel(server, t)}
        </Badge>
      ),
    },
    {
      key: 'flags',
      label: t('lab.servers.flags'),
      render: (server) => (
        <span className="inline-badges">
          {server.fullLoad ? <Badge tone="warning">{t('lab.servers.fullLoad')}</Badge> : null}
          {server.memberOnly === true ? <Badge tone="info">{t('lab.servers.memberOnly')}</Badge> : null}
          {server.blockingMarks.length > 0 ? (
            <Badge tone="danger">{server.blockingMarks.join(', ')}</Badge>
          ) : null}
          {server.hasPingSecret ? <Badge tone="neutral">{t('lab.servers.pingSecret')}</Badge> : null}
        </span>
      ),
    },
    {
      key: 'latency',
      label: t('lab.servers.latency'),
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
          <h2 className="lab-tab-title">{t('lab.servers.title')}</h2>
          <p className="lab-tab-subtitle">
            {t('lab.servers.subtitle', { account: accountLabel || accountId })}
          </p>
        </div>
        <div className="lab-tab-actions">
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={servers.reload}>
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      {disabled ? (
        <DisabledCallout
          title={t('lab.servers.disabled')}
          reason={servers.data?.reason ?? t('lab.servers.disabledReason')}
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
                  {servers.data.stale ? t('lab.servers.stale') : t('lab.servers.fresh')}
                </p>
                <p className="callout__text">
                  {servers.data.fetchedAt
                    ? t('lab.servers.fetched', { time: formatDateTime(servers.data.fetchedAt) })
                    : t('lab.servers.notFetched')}{' '}
                  {t('lab.servers.restartNote')}
                </p>
              </div>
            </div>
          ) : null}

          <Card bodyClassName="card__body--flush">
            {servers.status === 'error' ? (
              <ErrorState message={servers.error ?? t('common.unknownError')} onRetry={servers.reload} />
            ) : servers.status === 'loading' && !servers.data ? (
              <TableSkeleton rows={6} columns={8} />
            ) : (
              <DataTable
                caption={t('lab.servers.caption')}
                columns={columns}
                rows={rows}
                rowKey={(server) => server.id}
                empty={
                  <EmptyState
                    title={t('lab.servers.emptyTitle')}
                    description={t('lab.servers.emptyDesc')}
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
