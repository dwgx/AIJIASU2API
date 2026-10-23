import { Pin, RotateCw, ShieldAlert, ShieldCheck, Ticket, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { api } from '../lib/api'
import { formatDate, formatNumber, formatTtl, proxyUrl } from '../lib/format'
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
      push('Rotate is disabled: Resin is not configured on the daemon', 'danger')
      return
    }
    try {
      const updated = await api.rotateLease(lease.id)
      leases.reload()
      push(`Lease rotated · ${lease.userName} now egresses ${updated.egressIp}`, 'success')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Rotation failed', 'danger')
    }
  }

  const confirmRelease = async () => {
    if (!releaseId) return
    setReleasing(true)
    try {
      await api.releaseLease(releaseId)
      leases.reload()
      push('Lease released', 'success')
      setReleaseId(null)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Release failed', 'danger')
    } finally {
      setReleasing(false)
    }
  }

  const columns: Array<Column<Lease>> = [
    {
      key: 'user',
      label: 'User',
      render: (lease) => (
        <div className="cell-stack">
          <span className="cell-strong">{lease.userName}</span>
          <span className="cell-sub mono">{lease.userId}</span>
        </div>
      ),
    },
    {
      key: 'node',
      label: 'Account / node',
      render: (lease) => (
        <div className="cell-stack">
          <span>{lease.accountLabel}</span>
          <span className="cell-sub mono">{lease.nodeLabel}</span>
        </div>
      ),
    },
    {
      key: 'egress',
      label: 'Egress IP',
      render: (lease) => (
        <span className="inline-copy">
          <span className="cell-mono">{lease.egressIp}</span>
          <CopyButton
            value={lease.egressIp}
            label={`Copy egress IP for ${lease.userName}`}
            toastMessage="Egress IP copied"
          />
        </span>
      ),
    },
    {
      key: 'protocol',
      label: 'Protocol',
      render: (lease) => <Badge tone={lease.protocol === 'socks5' ? 'info' : 'neutral'}>{lease.protocol}</Badge>,
    },
    {
      key: 'ttl',
      label: 'TTL',
      render: (lease) => (
        <div className="cell-stack">
          <span className="cell-mono">{formatTtl(lease.expiresAt)}</span>
          <span className="cell-sub">{formatDate(lease.expiresAt)}</span>
        </div>
      ),
    },
    {
      key: 'requests',
      label: 'Requests',
      align: 'right',
      render: (lease) => <span className="cell-mono">{formatNumber(lease.requests)}</span>,
    },
    {
      key: 'sticky',
      label: 'Mode',
      render: (lease) =>
        lease.sticky ? (
          <Badge tone="mint">
            <Pin size={10} aria-hidden="true" /> sticky
          </Badge>
        ) : (
          <Badge tone="neutral">rotating</Badge>
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
            label={`Copy proxy URL for ${lease.userName}`}
            toastMessage="Proxy URL copied"
            withLabel
          />
          <IconButton
            label={
              rotateDisabled
                ? 'Rotate disabled: Resin is not configured on the daemon'
                : `Rotate lease for ${lease.userName}`
            }
            icon={<RotateCw size={14} />}
            disabled={rotateDisabled}
            onClick={() => void rotate(lease)}
          />
          <IconButton
            label={`Release lease for ${lease.userName}`}
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
        title="Leases"
        subtitle={`${leases.data?.length ?? 0} active assignments · ${stickyCount} sticky on egress IP.`}
        refresh={summarizeRefresh([leases])}
      />

      <div className="callout callout--info">
        <span className="callout__icon">
          <ShieldCheck size={18} aria-hidden="true" />
        </span>
        <div>
          <p className="callout__title">Sticky leases are anchored on egress IP</p>
          <p className="callout__text">
            A lease keeps one user pinned to one node until its TTL expires. Rotating picks a new
            egress IP and restarts the TTL; releasing drops the assignment immediately. Proxy URLs
            are loopback-only and carry no credentials.
          </p>
        </div>
      </div>

      {rotateDisabled ? (
        <div className="callout callout--warning" role="status">
          <span className="callout__icon">
            <ShieldAlert size={18} aria-hidden="true" />
          </span>
          <div>
            <p className="callout__title">Rotate is disabled — Resin is not configured</p>
            <p className="callout__text">
              Without <code className="mono">AJS_RESIN_ADMIN_TOKEN</code> the daemon has no way to
              change the real egress. The store would only rewrite the recorded IP and add 17
              synthetic requests, so the button is disabled instead of reporting a rotation that did
              not happen.
            </p>
          </div>
        </div>
      ) : null}

      <Card bodyClassName="card__body--flush">
        {leases.status === 'error' ? (
          <ErrorState message={leases.error ?? 'Unknown error'} onRetry={leases.reload} />
        ) : leases.status === 'loading' && !leases.data ? (
          <TableSkeleton rows={7} columns={7} />
        ) : (
          <DataTable
            caption="Active leases"
            columns={columns}
            rows={leases.data ?? []}
            rowKey={(lease) => lease.id}
            empty={
              <EmptyState
                title="No active leases"
                description="Leases appear here after a user platform requests a node."
                icon={<Ticket size={20} aria-hidden="true" />}
              />
            }
          />
        )}
      </Card>

      <ConfirmDialog
        open={releaseTarget !== null}
        title="Release lease?"
        description={
          releaseTarget
            ? `${releaseTarget.userName} loses the assignment to ${releaseTarget.nodeLabel} (${releaseTarget.egressIp}). The next request will be allocated a new node.`
            : ''
        }
        confirmLabel="Release lease"
        busy={releasing}
        onConfirm={() => void confirmRelease()}
        onCancel={() => setReleaseId(null)}
      />
    </div>
  )
}
