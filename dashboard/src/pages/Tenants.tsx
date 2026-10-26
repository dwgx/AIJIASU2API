import { Check, Pause, Play, Plus, UserCheck } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { CopyButton } from '../components/CopyButton'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { Modal } from '../components/Overlay'
import { useToast } from '../components/Toast'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  Input,
  PageHeader,
  Select,
  TableSkeleton,
} from '../components/primitives'
import { api } from '../lib/api'
import { formatDate } from '../lib/format'
import { useI18n } from '../lib/i18n'
import type { Tenant, TenantInvite, TenantInviteCreated, TenantStatus } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'

type TenantFilter = 'all' | TenantStatus

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

function tenantTone(status: TenantStatus): 'success' | 'warning' | 'danger' {
  if (status === 'active') return 'success'
  if (status === 'pending') return 'warning'
  return 'danger'
}

function inviteTone(status: TenantInvite['status']): 'success' | 'warning' | 'neutral' {
  if (status === 'pending') return 'warning'
  if (status === 'redeemed') return 'success'
  return 'neutral'
}

/**
 * Operator tenant directory. Invitations are the onboarding path: the admin
 * creates a one-time code, hands it over out of band, and the invitee sets a
 * password at /portal/activate. The daemon never stores or re-shows the code.
 */
export default function Tenants() {
  const { t } = useI18n()
  const { push } = useToast()
  const tenants = useAsync(() => api.listTenants(), [], { intervalMs: 15_000 })
  const invites = useAsync(() => api.listTenantInvites(), [], { intervalMs: 30_000 })
  const users = useAsync(() => api.listUsers(), [], { intervalMs: 60_000 })
  const [filter, setFilter] = useState<TenantFilter>('all')
  const [inviteOpen, setInviteOpen] = useState(false)
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteName, setInviteName] = useState('')
  const [inviteBusy, setInviteBusy] = useState(false)
  const [inviteError, setInviteError] = useState('')
  const [created, setCreated] = useState<TenantInviteCreated | null>(null)
  const [approveTarget, setApproveTarget] = useState<Tenant | null>(null)
  const [approveUserId, setApproveUserId] = useState('')
  const [approveBusy, setApproveBusy] = useState(false)
  const [approveError, setApproveError] = useState('')
  const [actionBusy, setActionBusy] = useState<string | null>(null)
  const inviteEmailId = useId()
  const inviteNameId = useId()
  const approveUserFieldId = useId()

  const visible = useMemo(() => {
    const rows = tenants.data ?? []
    return filter === 'all' ? rows : rows.filter((tenant) => tenant.status === filter)
  }, [filter, tenants.data])

  const statusOptions = useMemo(
    () => [
      { value: 'all', label: t('common.all') },
      { value: 'pending', label: t('tenants.statusPending') },
      { value: 'active', label: t('tenants.statusActive') },
      { value: 'suspended', label: t('tenants.statusSuspended') },
    ],
    [t],
  )

  const userOptions = useMemo(
    () => (users.data ?? []).map((user) => ({ value: user.id, label: `${user.name} · ${user.id}` })),
    [users.data],
  )

  const openInvite = () => {
    setInviteEmail('')
    setInviteName('')
    setInviteError('')
    setInviteOpen(true)
  }

  const closeInvite = () => {
    if (inviteBusy) return
    setInviteOpen(false)
    setInviteError('')
  }

  const submitInvite = async () => {
    const email = inviteEmail.trim()
    if (!email) {
      setInviteError(t('tenants.inviteEmailRequired'))
      return
    }
    if (!EMAIL_PATTERN.test(email)) {
      setInviteError(t('tenants.inviteEmailInvalid'))
      return
    }
    setInviteBusy(true)
    setInviteError('')
    try {
      const result = await api.createTenantInvite({
        email,
        displayName: inviteName.trim() || undefined,
      })
      setCreated(result)
      setInviteOpen(false)
      invites.reload()
      push(t('tenants.inviteCreated', { email: result.invite.email }), 'success')
    } catch (cause) {
      setInviteError(cause instanceof Error ? cause.message : t('tenants.inviteFailed'))
    } finally {
      setInviteBusy(false)
    }
  }

  const openApprove = (tenant: Tenant) => {
    const linked = new Set((tenants.data ?? []).map((row) => row.userId).filter((id): id is string => Boolean(id)))
    const available = (users.data ?? []).find((user) => !linked.has(user.id))
    setApproveTarget(tenant)
    setApproveUserId(available?.id ?? users.data?.[0]?.id ?? '')
    setApproveError('')
  }

  const submitApprove = async () => {
    if (!approveTarget) return
    if (!approveUserId) {
      setApproveError(t('tenants.approveUserRequired'))
      return
    }
    setApproveBusy(true)
    setApproveError('')
    try {
      await api.approveTenant(approveTarget.id, { userId: approveUserId })
      push(t('tenants.toastApproved', { email: approveTarget.email }), 'success')
      setApproveTarget(null)
      tenants.reload()
    } catch (cause) {
      setApproveError(cause instanceof Error ? cause.message : t('tenants.approveFailed'))
    } finally {
      setApproveBusy(false)
    }
  }

  const setTenantState = async (tenant: Tenant, action: 'suspend' | 'resume') => {
    setActionBusy(tenant.id)
    try {
      if (action === 'suspend') {
        await api.suspendTenant(tenant.id)
        push(t('tenants.toastSuspended', { email: tenant.email }), 'success')
      } else {
        await api.resumeTenant(tenant.id)
        push(t('tenants.toastResumed', { email: tenant.email }), 'success')
      }
      tenants.reload()
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('tenants.actionFailed'), 'danger')
    } finally {
      setActionBusy(null)
    }
  }

  const columns: Array<Column<Tenant>> = [
    {
      key: 'tenant',
      label: t('tenants.columnTenant'),
      render: (tenant) => (
        <div className="stack-xs">
          <strong>{tenant.displayName}</strong>
          <span className="mono-sm">{tenant.email}</span>
        </div>
      ),
    },
    {
      key: 'status',
      label: t('tenants.columnStatus'),
      width: '130px',
      render: (tenant) => (
        <Badge tone={tenantTone(tenant.status)}>
          {t(`tenants.status${tenant.status.charAt(0).toUpperCase()}${tenant.status.slice(1)}`)}
        </Badge>
      ),
    },
    {
      key: 'user',
      label: t('tenants.columnUser'),
      render: (tenant) => (tenant.userId ? <span className="mono-sm">{tenant.userId}</span> : '—'),
    },
    {
      key: 'created',
      label: t('tenants.columnCreated'),
      width: '170px',
      render: (tenant) => <span className="mono-sm">{formatDate(tenant.createdAt)}</span>,
    },
    {
      key: 'actions',
      label: '',
      width: '220px',
      render: (tenant) => (
        <div className="row-actions">
          {tenant.status === 'pending' ? (
            <Button size="sm" variant="ghost" icon={<UserCheck size={14} />} onClick={() => openApprove(tenant)}>
              {t('tenants.approve')}
            </Button>
          ) : null}
          {tenant.status === 'active' ? (
            <Button
              size="sm"
              variant="ghost"
              icon={<Pause size={14} />}
              loading={actionBusy === tenant.id}
              onClick={() => void setTenantState(tenant, 'suspend')}
            >
              {t('tenants.suspend')}
            </Button>
          ) : null}
          {tenant.status === 'suspended' ? (
            <Button
              size="sm"
              variant="ghost"
              icon={<Play size={14} />}
              loading={actionBusy === tenant.id}
              onClick={() => void setTenantState(tenant, 'resume')}
            >
              {t('tenants.resume')}
            </Button>
          ) : null}
        </div>
      ),
    },
  ]

  const inviteColumns: Array<Column<TenantInvite>> = [
    {
      key: 'email',
      label: t('tenants.columnTenant'),
      render: (invite) => (
        <div className="stack-xs">
          <strong>{invite.email}</strong>
          {invite.displayName ? <span className="mono-sm">{invite.displayName}</span> : null}
        </div>
      ),
    },
    {
      key: 'status',
      label: t('tenants.columnStatus'),
      width: '120px',
      render: (invite) => (
        <Badge tone={inviteTone(invite.status)}>
          {t(
            `tenants.inviteStatus${invite.status.charAt(0).toUpperCase()}${invite.status.slice(1)}`,
          )}
        </Badge>
      ),
    },
    {
      key: 'expires',
      label: t('tenants.inviteExpires'),
      width: '180px',
      render: (invite) => <span className="mono-sm">{formatDate(invite.expiresAt)}</span>,
    },
    {
      key: 'createdBy',
      label: t('tenants.inviteCreatedBy'),
      width: '140px',
      render: (invite) => <span className="mono-sm">{invite.createdBy}</span>,
    },
  ]

  const loading = (tenants.status === 'loading' && !tenants.data) || (invites.status === 'loading' && !invites.data)
  const failure = tenants.error ?? invites.error
  const activationLink = created
    ? `${window.location.origin}/portal/activate?code=${encodeURIComponent(created.code)}`
    : ''

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('tenants.title')}
        subtitle={t('tenants.subtitle')}
        refresh={summarizeRefresh([tenants, invites])}
        actions={
          <Button variant="primary" icon={<Plus size={16} />} onClick={openInvite}>
            {t('tenants.invite')}
          </Button>
        }
      />

      {failure ? <ErrorState message={failure} onRetry={() => { tenants.reload(); invites.reload() }} /> : null}

      <Card
        title={t('tenants.listTitle')}
        actions={
          <Select
            aria-label={t('tenants.filterLabel')}
            value={filter}
            options={statusOptions}
            onChange={(value) => setFilter(value as TenantFilter)}
          />
        }
      >
        {loading ? (
          <TableSkeleton rows={5} />
        ) : (
          <DataTable
            caption={t('tenants.caption')}
            columns={columns}
            rows={visible}
            rowKey={(tenant) => tenant.id}
            empty={
              <EmptyState
                icon={<UserCheck size={20} />}
                title={t('tenants.emptyTitle')}
                description={t('tenants.emptyDesc')}
                action={
                  <Button variant="primary" icon={<Plus size={16} />} onClick={openInvite}>
                    {t('tenants.invite')}
                  </Button>
                }
              />
            }
          />
        )}
      </Card>

      <Card title={t('tenants.invitesTitle')} subtitle={t('tenants.invitesSubtitle')}>
        {invites.status === 'loading' && !invites.data ? (
          <TableSkeleton rows={3} />
        ) : (
          <DataTable
            caption={t('tenants.invitesCaption')}
            columns={inviteColumns}
            rows={invites.data ?? []}
            rowKey={(invite) => invite.id}
            empty={
              <EmptyState
                icon={<Check size={20} />}
                title={t('tenants.noInvites')}
                description={t('tenants.noInvitesHint')}
              />
            }
          />
        )}
      </Card>

      <Modal
        open={inviteOpen}
        title={t('tenants.inviteTitle')}
        description={t('tenants.inviteBody')}
        onClose={closeInvite}
        footer={
          <>
            <Button variant="ghost" onClick={closeInvite} disabled={inviteBusy}>
              {t('common.cancel')}
            </Button>
            <Button variant="primary" loading={inviteBusy} onClick={() => void submitInvite()}>
              {inviteBusy ? t('tenants.inviting') : t('tenants.inviteSubmit')}
            </Button>
          </>
        }
      >
        <div className="stack-md">
          <Field label={t('tenants.email')} htmlFor={inviteEmailId}>
            <Input
              id={inviteEmailId}
              type="email"
              value={inviteEmail}
              onChange={(event) => setInviteEmail(event.target.value)}
              placeholder="customer@example.com"
              autoComplete="off"
              autoFocus
              required
            />
          </Field>
          <Field
            label={t('tenants.displayName')}
            htmlFor={inviteNameId}
            hint={t('tenants.displayNameHint')}
          >
            <Input
              id={inviteNameId}
              value={inviteName}
              onChange={(event) => setInviteName(event.target.value)}
              autoComplete="off"
            />
          </Field>
          {inviteError ? (
            <p className="form-error" role="alert">
              {inviteError}
            </p>
          ) : null}
        </div>
      </Modal>

      <Modal
        open={created !== null}
        title={t('tenants.inviteCodeTitle')}
        description={t('tenants.inviteCodeBody')}
        onClose={() => setCreated(null)}
        size="md"
        footer={
          <Button variant="primary" onClick={() => setCreated(null)}>
            {t('tenants.inviteCodeDone')}
          </Button>
        }
      >
        {created ? (
          <div className="stack-md">
            <Field label={t('tenants.inviteCodeLabel')} htmlFor="tenant-invite-code">
              <div className="copy-row">
                <Input id="tenant-invite-code" value={created.code} readOnly className="mono-sm" />
                <CopyButton value={created.code} label={t('tenants.copyCode')} />
              </div>
            </Field>
            <Field label={t('tenants.inviteLinkLabel')} htmlFor="tenant-invite-link">
              <div className="copy-row">
                <Input id="tenant-invite-link" value={activationLink} readOnly className="mono-sm" />
                <CopyButton value={activationLink} label={t('tenants.copyLink')} />
              </div>
            </Field>
            <p className="form-hint">{t('tenants.inviteCodeWarning')}</p>
          </div>
        ) : null}
      </Modal>

      <Modal
        open={approveTarget !== null}
        title={t('tenants.approveTitle')}
        description={t('tenants.approveBody')}
        onClose={() => {
          if (approveBusy) return
          setApproveTarget(null)
        }}
        footer={
          <>
            <Button variant="ghost" onClick={() => setApproveTarget(null)} disabled={approveBusy}>
              {t('common.cancel')}
            </Button>
            <Button variant="primary" loading={approveBusy} onClick={() => void submitApprove()}>
              {approveBusy ? t('tenants.approving') : t('tenants.approveSubmit')}
            </Button>
          </>
        }
      >
        <div className="stack-md">
          <p className="mono-sm">{approveTarget?.email}</p>
          <Field label={t('tenants.approveUser')} htmlFor={approveUserFieldId}>
            <Select
              aria-label={t('tenants.approveUser')}
              value={approveUserId}
              options={userOptions}
              onChange={setApproveUserId}
            />
          </Field>
          {approveError ? (
            <p className="form-error" role="alert">
              {approveError}
            </p>
          ) : null}
        </div>
      </Modal>
    </div>
  )
}
