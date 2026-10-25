import { DataTable } from '../../components/DataTable'
import type { Column } from '../../components/DataTable'
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  TableSkeleton,
} from '../../components/primitives'
import type { Tone } from '../../components/primitives'
import { formatBytes, formatDate, formatNumber, formatTtl } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { portalApi } from '../../lib/portal-api'
import type { Lease, LeaseStatus } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'

function leaseTone(status: LeaseStatus): Tone {
  if (status === 'active') return 'success'
  if (status === 'revoked') return 'danger'
  return 'neutral'
}

/** Own rows only: the daemon filters by the session's immutable user id. */
export default function PortalLeases() {
  const { t } = useI18n()
  const leases = useAsync(() => portalApi.leases(), [])

  const columns: Array<Column<Lease>> = [
    {
      key: 'platform',
      label: t('portal.leases.platform'),
      render: (lease) => <Badge tone="neutral">{lease.platform}</Badge>,
    },
    {
      key: 'node',
      label: t('portal.leases.node'),
      render: (lease) => (
        <div className="portal-lease-node">
          <span className="mono">{lease.nodeLabel}</span>
          <span className="portal-lease-node__region">{lease.region}</span>
        </div>
      ),
    },
    {
      key: 'gate',
      label: t('portal.leases.gate'),
      render: (lease) => (
        <span className="mono">
          {lease.protocol}://127.0.0.1:{lease.gatePort}
        </span>
      ),
    },
    {
      key: 'status',
      label: t('portal.leases.status'),
      render: (lease) => (
        <Badge tone={leaseTone(lease.status)}>
          {t(`portal.leases.${lease.status}`)}
        </Badge>
      ),
    },
    {
      key: 'expires',
      label: t('portal.leases.expires'),
      render: (lease) => (
        <span title={formatDate(lease.expiresAt)}>
          {formatTtl(lease.expiresAt, Date.now(), t('portal.leases.expired'))}
        </span>
      ),
    },
    {
      key: 'requests',
      label: t('portal.leases.requests'),
      align: 'right',
      render: (lease) => formatNumber(lease.requests),
    },
    {
      key: 'bytes',
      label: t('portal.leases.bytes'),
      align: 'right',
      render: (lease) => formatBytes(lease.bytes),
    },
  ]

  return (
    <Card title={t('portal.leases.title')} subtitle={t('portal.leases.subtitle')}>
      {leases.status === 'loading' ? (
        <TableSkeleton rows={3} columns={7} />
      ) : leases.status === 'error' ? (
        <ErrorState message={t('portal.leases.loadFailed')} onRetry={leases.reload} />
      ) : (
        <DataTable
          caption={t('portal.leases.caption')}
          columns={columns}
          rows={leases.data ?? []}
          rowKey={(lease) => lease.id}
          empty={
            <EmptyState
              title={t('portal.leases.empty')}
              description={t('portal.leases.emptyHint')}
            />
          }
        />
      )}
    </Card>
  )
}
