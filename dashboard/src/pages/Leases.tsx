import { Pin, Plus, RotateCw, ShieldAlert, ShieldCheck, Ticket, Trash2 } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { api } from '../lib/api'
import { formatDate, formatNumber, formatTtl, proxyUrl } from '../lib/format'
import { useI18n } from '../lib/i18n'
import type { Account, Lease, PanelUser, Platform } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { ConfirmDialog, Modal } from '../components/Overlay'
import { useToast } from '../components/Toast'
import { CopyButton } from '../components/CopyButton'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  PageHeader,
  Select,
  TableSkeleton,
} from '../components/primitives'

const PLATFORMS: Platform[] = ['android', 'ios', 'windows', 'linux']

const TTL_OPTIONS = [
  { value: '3600', labelKey: 'leases.ttl1h' },
  { value: '28800', labelKey: 'leases.ttl8h' },
  { value: '86400', labelKey: 'leases.ttl24h' },
  { value: '604800', labelKey: 'leases.ttl7d' },
] as const

interface CreateForm {
  userId: string
  platform: Platform
  accountId: string
  ttlSeconds: number
}

function emptyForm(): CreateForm {
  return { userId: '', platform: 'android', accountId: '', ttlSeconds: 28_800 }
}

/**
 * A fresh key per dialog open. Retrying a failed submit with the same key is
 * safe: the daemon returns the original lease instead of allocating a second
 * node. The key is never persisted or rendered.
 */
function newIdempotencyKey(): string {
  const cryptoApi = globalThis.crypto
  if (cryptoApi && typeof cryptoApi.randomUUID === 'function') return cryptoApi.randomUUID()
  return `lease-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

function accountOptionLabel(account: Account): string {
  return `${account.label} · ${account.id}`
}

function userOptionLabel(user: PanelUser): string {
  return `${user.name} · ${user.id}`
}

export default function Leases() {
  const leases = useAsync(() => api.listLeases(), [], { intervalMs: 15_000 })
  const lab = useAsync(() => api.getLabOverview(), [], { intervalMs: 30_000 })
  const users = useAsync(() => api.listUsers(), [], { intervalMs: 60_000 })
  const accounts = useAsync(() => api.listAccounts(), [], { intervalMs: 60_000 })
  const { push } = useToast()
  const { t } = useI18n()
  const [revokeId, setRevokeId] = useState<string | null>(null)
  const [revoking, setRevoking] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState('')
  const [idempotencyKey, setIdempotencyKey] = useState('')
  const [form, setForm] = useState<CreateForm>(emptyForm)
  const userFieldId = useId()
  const platformFieldId = useId()
  const accountFieldId = useId()
  const ttlFieldId = useId()

  const revokeTarget = leases.data?.find((lease) => lease.id === revokeId) ?? null
  const stickyCount = leases.data?.filter((lease) => lease.sticky).length ?? 0
  // Rotation is only real when the daemon has a Resin admin token. Without it
  // the store rewrites the recorded IP and adds 17 synthetic requests. A
  // disabled lab overview also leaves the posture unknown, so treat it as off.
  const rotateDisabled =
    lab.data !== undefined && (lab.data.enabled === false || !lab.data.modes?.resin)

  const userOptions = useMemo(
    () =>
      (users.data ?? []).map((user) => ({
        value: user.id,
        label: userOptionLabel(user),
        description: user.platform,
      })),
    [users.data],
  )

  const accountOptions = useMemo(() => {
    const available = (accounts.data ?? []).filter(
      (account) => account.platform === form.platform && account.state === 'active',
    )
    return [
      { value: '', label: t('leases.anyAccount') },
      ...available.map((account) => ({
        value: account.id,
        label: accountOptionLabel(account),
        description: account.username,
      })),
    ]
  }, [accounts.data, form.platform, t])

  const openCreate = () => {
    setForm(emptyForm())
    setCreateError('')
    setIdempotencyKey(newIdempotencyKey())
    setCreateOpen(true)
  }

  const changePlatform = (platform: Platform) => {
    setForm((current) => {
      const account = accounts.data?.find((item) => item.id === current.accountId)
      return {
        ...current,
        platform,
        accountId: account && account.platform === platform ? current.accountId : '',
      }
    })
  }

  const submitCreate = async () => {
    if (!form.userId) {
      setCreateError(t('leases.createUserRequired'))
      return
    }
    setCreating(true)
    setCreateError('')
    try {
      const result = await api.createLease({
        userId: form.userId,
        platform: form.platform,
        accountId: form.accountId || undefined,
        ttlSeconds: form.ttlSeconds,
        idempotencyKey,
      })
      leases.reload()
      push(
        t('leases.toastCreated', {
          name: result.lease.userName,
          node: result.lease.nodeLabel,
        }),
        'success',
      )
      if (result.warning) {
        push(t('leases.toastCreatedWarning', { warning: result.warning }), 'danger')
      }
      setCreateOpen(false)
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : t('leases.createFailed')
      setCreateError(message)
      push(message, 'danger')
    } finally {
      setCreating(false)
    }
  }

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

  const confirmRevoke = async () => {
    if (!revokeId) return
    setRevoking(true)
    try {
      await api.revokeLease(revokeId)
      leases.reload()
      push(t('leases.toastReleased'), 'success')
      setRevokeId(null)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('leases.toastReleaseFailed'), 'danger')
    } finally {
      setRevoking(false)
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
            onClick={() => setRevokeId(lease.id)}
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
        actions={
          <Button variant="primary" icon={<Plus size={14} />} onClick={openCreate}>
            {t('leases.newLease')}
          </Button>
        }
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
                action={
                  <Button variant="secondary" icon={<Plus size={14} />} onClick={openCreate}>
                    {t('leases.emptyAction')}
                  </Button>
                }
              />
            }
          />
        )}
      </Card>

      <Modal
        open={createOpen}
        title={t('leases.createTitle')}
        description={t('leases.createDesc')}
        onClose={() => {
          if (!creating) setCreateOpen(false)
        }}
        size="md"
        footer={
          <>
            <Button variant="ghost" onClick={() => setCreateOpen(false)} disabled={creating}>
              {t('common.cancel')}
            </Button>
            <Button variant="primary" onClick={() => void submitCreate()} loading={creating}>
              {t('leases.createConfirm')}
            </Button>
          </>
        }
      >
        <div className="form">
          <Field
            label={t('leases.fieldUser')}
            htmlFor={userFieldId}
            hint={users.data && users.data.length === 0 ? t('leases.noUsers') : undefined}
          >
            <Select
              id={userFieldId}
              aria-label={t('leases.fieldUser')}
              options={userOptions}
              value={form.userId}
              onChange={(value) => setForm((current) => ({ ...current, userId: value }))}
              placeholder={t('leases.selectUser')}
              searchable
            />
          </Field>
          <Field label={t('leases.fieldPlatform')} htmlFor={platformFieldId}>
            <Select
              id={platformFieldId}
              aria-label={t('leases.fieldPlatform')}
              options={PLATFORMS.map((platform) => ({ value: platform, label: platform }))}
              value={form.platform}
              onChange={(value) => changePlatform(value as Platform)}
            />
          </Field>
          <Field label={t('leases.fieldAccount')} htmlFor={accountFieldId}>
            <Select
              id={accountFieldId}
              aria-label={t('leases.fieldAccount')}
              options={accountOptions}
              value={form.accountId}
              onChange={(value) => setForm((current) => ({ ...current, accountId: value }))}
            />
          </Field>
          <Field label={t('leases.fieldTtl')} htmlFor={ttlFieldId}>
            <Select
              id={ttlFieldId}
              aria-label={t('leases.fieldTtl')}
              options={TTL_OPTIONS.map((option) => ({
                value: option.value,
                label: t(option.labelKey),
              }))}
              value={String(form.ttlSeconds)}
              onChange={(value) =>
                setForm((current) => ({ ...current, ttlSeconds: Number(value) }))
              }
            />
          </Field>
          {createError ? (
            <p className="form-error" role="alert">
              {createError}
            </p>
          ) : null}
        </div>
      </Modal>

      <ConfirmDialog
        open={revokeTarget !== null}
        title={t('leases.releaseTitle')}
        description={
          revokeTarget
            ? t('leases.releaseDesc', {
                name: revokeTarget.userName,
                node: revokeTarget.nodeLabel,
                ip: revokeTarget.egressIp,
              })
            : ''
        }
        confirmLabel={t('leases.releaseConfirm')}
        busy={revoking}
        onConfirm={() => void confirmRevoke()}
        onCancel={() => setRevokeId(null)}
      />
    </div>
  )
}
