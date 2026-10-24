import { Activity, Gauge, RefreshCw } from 'lucide-react'
import { api } from '../../lib/api'
import { formatBytes, formatDateTime, formatNumber, formatRelative } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { formatAge, nodeStateMeta } from '../../lib/lab'
import type { GateView, SessionView } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import { Pagination, usePagination } from '../Pagination'
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
  const { lang, t } = useI18n()
  const pagination = usePagination({ total: gates.length, initialPageSize: 25 })
  const columns: Array<Column<GateView>> = [
    {
      key: 'node',
      label: t('lab.sessions.node'),
      render: (gate) => (
        <div className="cell-stack">
          <span className="cell-strong mono">{gate.nodeId}</span>
          <span className="cell-sub mono">{gate.accountId}</span>
        </div>
      ),
    },
    {
      key: 'state',
      label: t('lab.sessions.state'),
      render: (gate) => {
        const meta = nodeStateMeta(gate.state)
        return (
          <span className="state-cell">
            <StatusDot tone={meta.tone} pulse={gate.state === 'LIVE'} />
            <Badge tone={meta.tone}>{meta.labelKey ? t(meta.labelKey) : meta.label}</Badge>
            {gate.draining ? <Badge tone="warning">{t('lab.draining')}</Badge> : null}
          </span>
        )
      },
    },
    {
      key: 'active',
      label: t('lab.sessions.activeLimit'),
      align: 'right',
      render: (gate) => (
        <span className="cell-mono">
          {formatNumber(gate.active)}/{formatNumber(gate.limit)}
        </span>
      ),
    },
    {
      key: 'accepted',
      label: t('lab.sessions.accepted'),
      align: 'right',
      render: (gate) => <span className="cell-mono">{formatNumber(gate.accepted)}</span>,
    },
    {
      key: 'rejected',
      label: t('lab.sessions.rejected'),
      align: 'right',
      render: (gate) => (
        <span className="cell-mono">
          <Badge tone={gate.rejected > 0 ? 'warning' : 'neutral'}>{formatNumber(gate.rejected)}</Badge>
        </span>
      ),
    },
    {
      key: 'ingress',
      label: t('lab.sessions.ingress'),
      align: 'right',
      render: (gate) => <span className="cell-mono">{formatBytes(gate.ingressBytes)}</span>,
    },
    {
      key: 'egress',
      label: t('lab.sessions.egress'),
      align: 'right',
      render: (gate) => <span className="cell-mono">{formatBytes(gate.egressBytes)}</span>,
    },
    {
      key: 'started',
      label: t('lab.sessions.started'),
      render: (gate) =>
        gate.startedAt ? (
          <span className="cell-sub" title={formatDateTime(gate.startedAt)}>
            {formatRelative(gate.startedAt, Date.now(), lang)}
          </span>
        ) : (
          <span className="cell-sub">—</span>
        ),
    },
  ]

  return (
    <>
      <DataTable
        caption={t('lab.sessions.gateCaption')}
        columns={columns}
        rows={pagination.slice(gates)}
        rowKey={(gate) => gate.nodeId}
        empty={<EmptyState title={t('lab.sessions.gateEmptyTitle')} description={t('lab.sessions.gateEmptyDesc')} />}
      />
      <Pagination
        page={pagination.page}
        pageSize={pagination.pageSize}
        total={gates.length}
        onPageChange={pagination.setPage}
        onPageSizeChange={pagination.setPageSize}
        label={t('lab.sessions.gatePagination')}
      />
    </>
  )
}

function SessionTable({ sessions }: { sessions: SessionView[] }) {
  const { lang, t } = useI18n()
  const pagination = usePagination({ total: sessions.length, initialPageSize: 25 })
  const columns: Array<Column<SessionView>> = [
    {
      key: 'node',
      label: t('lab.sessions.node'),
      render: (session) => (
        <div className="cell-stack">
          <span className="cell-strong mono">{session.nodeId}</span>
          <span className="cell-sub mono">{session.accountId}</span>
        </div>
      ),
    },
    {
      key: 'server',
      label: t('lab.sessions.server'),
      render: (session) => <span className="cell-mono">{session.serverId}</span>,
    },
    {
      key: 'health',
      label: t('lab.sessions.health'),
      render: (session) => (
        <span className="state-cell">
          <StatusDot tone={session.healthy ? 'success' : 'danger'} pulse={session.healthy} />
          <Badge tone={session.healthy ? 'success' : 'danger'}>
            {session.healthy ? t('lab.healthy') : t('lab.unhealthy')}
          </Badge>
          {session.errorClass ? <Badge tone="warning">{session.errorClass}</Badge> : null}
        </span>
      ),
    },
    {
      key: 'sid',
      label: t('lab.sessions.sid'),
      render: (session) => <span className="cell-mono">{session.sid}</span>,
    },
    {
      key: 'age',
      label: t('lab.sessions.age'),
      align: 'right',
      render: (session) => <span className="cell-mono">{formatAge(session.ageSec)}</span>,
    },
    {
      key: 'egress',
      label: t('lab.sessions.egressClaim'),
      render: (session) => (
        <span className="inline-badges">
          <span className="cell-mono">{session.egressClaimed || '—'}</span>
          <Badge tone="warning">{t('lab.unverified')}</Badge>
        </span>
      ),
    },
    {
      key: 'keepalive',
      label: t('lab.sessions.keepalive'),
      render: (session) => (
        <Badge tone={session.keepalive.supported ? 'info' : 'neutral'}>
          {session.keepalive.supported ? t('lab.supported') : t('lab.notImplemented')}
        </Badge>
      ),
    },
    {
      key: 'lastFlow',
      label: t('lab.sessions.lastFlow'),
      render: (session) =>
        session.lastFlowAt ? (
          <span className="cell-sub" title={formatDateTime(session.lastFlowAt)}>
            {formatRelative(session.lastFlowAt, Date.now(), lang)}
          </span>
        ) : (
          <span className="cell-sub">—</span>
        ),
    },
  ]

  return (
    <>
      <DataTable
        caption={t('lab.sessions.sessionCaption')}
        columns={columns}
        rows={pagination.slice(sessions)}
        rowKey={(session) => session.nodeId}
        empty={
          <EmptyState
            title={t('lab.sessions.sessionEmptyTitle')}
            description={t('lab.sessions.sessionEmptyDesc')}
          />
        }
      />
      <Pagination
        page={pagination.page}
        pageSize={pagination.pageSize}
        total={sessions.length}
        onPageChange={pagination.setPage}
        onPageSizeChange={pagination.setPageSize}
        label={t('lab.sessions.sessionPagination')}
      />
    </>
  )
}

export function SessionsTab() {
  const { t } = useI18n()
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
          <h2 className="lab-tab-title">{t('lab.sessions.title')}</h2>
          <p className="lab-tab-subtitle">{t('lab.sessions.subtitle')}</p>
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
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      {disabled ? (
        <DisabledCallout
          title={t('lab.sessions.disabled')}
          reason={String(reason)}
          flag="AJS_ACCOUNT_LOGIN=1"
        />
      ) : (
        <>
      <Card
        title={t('lab.sessions.gates')}
        subtitle={t('lab.sessions.gatesSubtitle')}
        actions={<Badge tone="info">{t('lab.gatesCount', { count: gateRows.length })}</Badge>}
        bodyClassName="card__body--flush"
      >
        {gates.status === 'error' ? (
          <ErrorState message={gates.error ?? t('common.unknownError')} onRetry={gates.reload} />
        ) : gates.status === 'loading' && !gates.data ? (
          <TableSkeleton rows={5} columns={8} />
        ) : (
          <GateTable gates={gateRows} />
        )}
      </Card>

      <Card
        title={t('lab.sessions.sessions')}
        subtitle={t('lab.sessions.sessionsSubtitle')}
        actions={<Badge tone="info">{t('lab.sessionsCount', { count: sessionRows.length })}</Badge>}
        bodyClassName="card__body--flush"
      >
        {sessions.status === 'error' ? (
          <ErrorState message={sessions.error ?? t('common.unknownError')} onRetry={sessions.reload} />
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
          <p className="callout__title">{t('lab.sessions.keepaliveTitle')}</p>
          <p className="callout__text">
            <code className="mono">ka</code>, <code className="mono">kat</code> and{' '}
            <code className="mono">kapf</code> {t('lab.sessions.keepaliveText')}
          </p>
        </div>
      </div>

      <p className="cell-sub">
        <Gauge size={12} aria-hidden="true" /> {t('lab.sessions.resetNote')}
      </p>
        </>
      )}
    </div>
  )
}
