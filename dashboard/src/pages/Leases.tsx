import { Pin, RotateCw, ShieldAlert, ShieldCheck, Ticket, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { api } from '../lib/api'
import { formatDate, formatNumber, formatTtl, proxyUrl } from '../lib/format'
import { useI18n } from '../lib/i18n'
import type { Lease } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { ConfirmDialog } from '../components/Overlay'
import { useToast } from '../components/Toast'
import { CopyButton } from '../components/CopyButton'
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  PageHeader,
  TableSkeleton,
} from '../components/primitives'

export default function Leases() {
  const leases = useAsync(() => api.listLeases(), [], { intervalMs: 15_000 })
  const lab = useAsync(() => api.getLabOverview(), [], { intervalMs: 30_000 })
  const { push } = useToast()
  const { t } = useI18n()
  const [releaseId, setReleaseId] = useState<string | null>(null)
  const [releasing, setReleasing] = useState(false)

  const releaseTarget = leases.data?.find((lease) => lease.id === releaseId) ?? null
  const stickyCount = leases.data?.filter((lease) => lease.sticky).length ?? 0
  // Rotation is only real when the daemon has a Resin admin token. Without it
  // the store rewrites the recorded IP and adds 17 synthetic requests. A
  // disabled lab overview also leaves the posture unknown, so treat it as off.
  const rotateDisabled =
    lab.data !== undefined && (lab.data.enabled === false || !lab.data.modes?.resin)

  const rotate = async (lease: Lease) => {
    if (rotateDisabled) {
      push(t('leases.toastRotateDisabled'), 'danger')
      return
    }
    try {
      const updated = await api.rotateLease(lease.id)
      leases.reload()
      push(t('leases.toastRotated', { name: lease.userName, ip: updated.egressIp }), 'success')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('leases.toastRotateFailed'), 'danger')
    }
  }

  const confirmRelease = async () => {
    if (!releaseId) return
    setReleasing(true)
    try {
      await api.releaseLease(releaseId)
      leases.reload()
      push(t('leases.toastReleased'), 'success')
      setReleaseId(null)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('leases.toastReleaseFailed'), 'danger')
    } finally {
      setReleasing(false)
    }
  }

  const columns: Array<Column<Lease>> = [
    {
      key: 'user',
      label: t('leases.columnUser'),
      render: (lease) => (
        <div className="cell-stack">
          <span className="cell-strong">{lease.userName}</span>
          <span className="cell-sub mono">{lease.userId}</span>
        </div>
      ),
    },
    {
      key: 'node',
      label: t('leases.columnNode'),
      render: (lease) => (
        <div className="cell-stack">
          <span>{lease.accountLabel}</span>
          <span className="cell-sub mono">{lease.nodeLabel}</span>
        </div>
      ),
    },
    {
      key: 'egress',
      label: t('leases.columnEgress'),
      render: (lease) => (
        <span className="inline-copy">
          <span className="cell-mono">{lease.egressIp}</span>
          <CopyButton
            value={lease.egressIp}
            label={t('leases.copyEgress', { name: lease.userName })}
            toastMessage={t('leases.egressCopied')}
          />
        </span>
      ),
    },
    {
      key: 'protocol',
      label: t('leases.columnProtocol'),
      render: (lease) => <Badge tone={lease.protocol === 'socks5' ? 'info' : 'neutral'}>{lease.protocol}</Badge>,
    },
    {
      key: 'ttl',
      label: t('leases.columnTtl'),
      render: (lease) => (
        <div className="cell-stack">
          <span className="cell-mono">{formatTtl(lease.expiresAt, Date.now(), t('leases.expired'))}</span>
          <span className="cell-sub">{formatDate(lease.expiresAt)}</span>
        </div>
      ),
    },
    {
      key: 'requests',
      label: t('leases.columnRequests'),
      align: 'right',
      render: (lease) => <span className="cell-mono">{formatNumber(lease.requests)}</span>,
    },
    {
      key: 'sticky',
      label: t('leases.columnMode'),
      render: (lease) =>
        lease.sticky ? (
          <Badge tone="mint">
            <Pin size={10} aria-hidden="true" /> {t('leases.sticky')}
          </Badge>
        ) : (
          <Badge tone="neutral">{t('leases.rotating')}</Badge>
        ),
    },
    {
      key: 'actions',
      label: '',
      align: 'right',
      width: '176px',
      render: (lease) => (
        <span className="row-actions">
          <CopyButton
            value={proxyUrl(lease.gatePort, lease.region, lease.protocol)}
            label={t('leases.copyProxy', { name: lease.userName })}
            toastMessage={t('leases.proxyCopied')}
            withLabel
          />
          <IconButton
            label={
              rotateDisabled
                ? t('leases.rotateDisabledLabel')
                : t('leases.rotateLease', { name: lease.userName })
            }
            icon={<RotateCw size={14} />}
            disabled={rotateDisabled}
            onClick={() => void rotate(lease)}
          />
          <IconButton
            label={t('leases.releaseLease', { name: lease.userName })}
            icon={<Trash2 size={14} />}
            onClick={() => setReleaseId(lease.id)}
          />
        </span>
      ),
    },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('leases.title')}
        subtitle={t('leases.subtitle', {
          count: leases.data?.length ?? 0,
          sticky: stickyCount,
        })}
        refresh={summarizeRefresh([leases])}
      />

      <div className="callout callout--info">
        <span className="callout__icon">
          <ShieldCheck size={18} aria-hidden="true" />
        </span>
        <div>
          <p className="callout__title">{t('leases.stickyCalloutTitle')}</p>
          <p className="callout__text">{t('leases.stickyCalloutText')}</p>
        </div>
      </div>

      {rotateDisabled ? (
        <div className="callout callout--warning" role="status">
          <span className="callout__icon">
            <ShieldAlert size={18} aria-hidden="true" />
          </span>
          <div>
            <p className="callout__title">{t('leases.rotateDisabledTitle')}</p>
            <p className="callout__text">{t('leases.rotateDisabledText')}</p>
          </div>
        </div>
      ) : null}

      <Card bodyClassName="card__body--flush">
        {leases.status === 'error' ? (
          <ErrorState message={leases.error ?? t('common.unknownError')} onRetry={leases.reload} />
        ) : leases.status === 'loading' && !leases.data ? (
          <TableSkeleton rows={7} columns={7} />
        ) : (
          <DataTable
            caption={t('leases.caption')}
            columns={columns}
            rows={leases.data ?? []}
            rowKey={(lease) => lease.id}
            empty={
              <EmptyState
                title={t('leases.emptyTitle')}
                description={t('leases.emptyDesc')}
                icon={<Ticket size={20} aria-hidden="true" />}
              />
            }
          />
        )}
      </Card>

      <ConfirmDialog
        open={releaseTarget !== null}
        title={t('leases.releaseTitle')}
        description={
          releaseTarget
            ? t('leases.releaseDesc', {
                name: releaseTarget.userName,
                node: releaseTarget.nodeLabel,
                ip: releaseTarget.egressIp,
              })
            : ''
        }
        confirmLabel={t('leases.releaseConfirm')}
        busy={releasing}
        onConfirm={() => void confirmRelease()}
        onCancel={() => setReleaseId(null)}
      />
    </div>
  )
}
