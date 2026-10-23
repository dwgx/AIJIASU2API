import { KeyRound, Plus, ShieldCheck, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '../lib/api'
import { formatBytes, formatDate, formatRelative, percent } from '../lib/format'
import type { ApiKey, CreateKeyResult, PanelUser } from '../lib/types'
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
  Input,
  PageHeader,
  ProgressBar,
  Select,
  Switch,
  TableSkeleton,
  Textarea,
} from '../components/primitives'

export default function Users() {
  const users = useAsync(() => api.listUsers(), [], { intervalMs: 15_000 })
  const keys = useAsync(() => api.listKeys(), [], { intervalMs: 15_000 })
  const { push } = useToast()
  const [createOpen, setCreateOpen] = useState(false)
  const [createUserId, setCreateUserId] = useState('')
  const [createLabel, setCreateLabel] = useState('')
  const [created, setCreated] = useState<CreateKeyResult | null>(null)
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState('')
  const [revokeId, setRevokeId] = useState<string | null>(null)
  const [revoking, setRevoking] = useState(false)
  const [allowlistUser, setAllowlistUser] = useState<PanelUser | null>(null)
  const [allowlistText, setAllowlistText] = useState('')
  const [savingAllowlist, setSavingAllowlist] = useState(false)

  const revokeTarget = keys.data?.find((key) => key.id === revokeId) ?? null
  const usersReady = users.status === 'ready' && (users.data?.length ?? 0) > 0

  useEffect(() => {
    if (!createOpen || created) return
    const list = users.data ?? []
    if (list.length === 0) return
    if (!list.some((user) => user.id === createUserId)) {
      setCreateUserId(list[0].id)
    }
  }, [createOpen, created, createUserId, users.data])

  const openCreate = () => {
    if (!usersReady) return
    setCreateUserId(users.data?.[0]?.id ?? '')
    setCreateLabel('')
    setCreateError('')
    setCreated(null)
    setCreateOpen(true)
  }

  const closeCreate = () => {
    setCreateOpen(false)
    setCreated(null)
    setCreateError('')
  }

  const submitCreate = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!createUserId) {
      setCreateError('Choose a user for this key')
      return
    }
    setCreating(true)
    setCreateError('')
    try {
      const result = await api.createKey({ userId: createUserId, label: createLabel })
      setCreated(result)
      keys.reload()
      push('API key created · copy it now', 'success')
    } catch (cause) {
      setCreateError(cause instanceof Error ? cause.message : 'Could not create key')
    } finally {
      setCreating(false)
    }
  }

  const toggleUser = async (user: PanelUser) => {
    try {
      await api.setUserEnabled(user.id, !user.enabled)
      users.reload()
      push(`${user.name} ${user.enabled ? 'suspended' : 'enabled'}`, 'success')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Could not update user', 'danger')
    }
  }

  const confirmRevoke = async () => {
    if (!revokeId) return
    setRevoking(true)
    try {
      await api.revokeKey(revokeId)
      keys.reload()
      push('API key revoked', 'success')
      setRevokeId(null)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Revoke failed', 'danger')
    } finally {
      setRevoking(false)
    }
  }

  const openAllowlist = (user: PanelUser) => {
    setAllowlistUser(user)
    setAllowlistText(user.allowlist.join('\n'))
  }

  const saveAllowlist = async () => {
    if (!allowlistUser) return
    setSavingAllowlist(true)
    try {
      const entries = allowlistText
        .split('\n')
        .map((line) => line.trim())
        .filter(Boolean)
      await api.updateAllowlist(allowlistUser.id, entries)
      users.reload()
      push(`Allowlist updated for ${allowlistUser.name}`, 'success')
      setAllowlistUser(null)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Could not update allowlist', 'danger')
    } finally {
      setSavingAllowlist(false)
    }
  }

  const columns: Array<Column<ApiKey>> = [
    {
      key: 'label',
      label: 'Label',
      render: (key) => (
        <div className="cell-stack">
          <span className="cell-strong">{key.label}</span>
          <span className="cell-sub mono">{key.prefix}…</span>
        </div>
      ),
    },
    {
      key: 'user',
      label: 'User',
      render: (key) => <span>{key.userName}</span>,
    },
    {
      key: 'created',
      label: 'Created',
      render: (key) => <span className="cell-sub">{formatDate(key.createdAt)}</span>,
    },
    {
      key: 'used',
      label: 'Last used',
      render: (key) => (
        <span className="cell-sub">{key.lastUsedAt ? formatRelative(key.lastUsedAt) : 'never'}</span>
      ),
    },
    {
      key: 'status',
      label: 'Status',
      render: (key) =>
        key.revokedAt ? (
          <Badge tone="danger">Revoked</Badge>
        ) : (
          <Badge tone="success">Active</Badge>
        ),
    },
    {
      key: 'actions',
      label: '',
      align: 'right',
      width: '64px',
      render: (key) =>
        key.revokedAt ? null : (
          <IconButton
            label={`Revoke key ${key.label}`}
            icon={<Trash2 size={14} />}
            onClick={() => setRevokeId(key.id)}
          />
        ),
    },
  ]

  const loading = (users.status === 'loading' && !users.data) || (keys.status === 'loading' && !keys.data)
  const failure = users.error ?? keys.error

  return (
    <div className="stack-lg">
      <PageHeader
        title="Users"
        subtitle="Panel identities, API keys and source allowlists."
        refresh={summarizeRefresh([users, keys])}
        actions={
          <Button
            variant="primary"
            icon={<Plus size={15} />}
            onClick={openCreate}
            disabled={!usersReady}
            aria-disabled={!usersReady}
            title={usersReady ? undefined : 'Waiting for users to load'}
          >
            Create API key
          </Button>
        }
      />

      {failure ? (
        <Card>
          <ErrorState
            message={failure}
            onRetry={() => {
              users.reload()
              keys.reload()
            }}
          />
        </Card>
      ) : loading ? (
        <div className="user-grid">
          {Array.from({ length: 4 }, (_, index) => (
            <Card key={index}>
              <TableSkeleton rows={3} columns={2} />
            </Card>
          ))}
        </div>
      ) : (
        <div className="user-grid">
          {users.data?.map((user) => {
            const keyCount = keys.data?.filter((key) => key.userId === user.id && !key.revokedAt).length ?? 0
            const usage = user.quotaBytesMonth ? percent(user.usedBytes, user.quotaBytesMonth) : 0
            return (
              <Card key={user.id} className="user-card">
                <div className="user-card__head">
                  <span className="user-card__avatar" aria-hidden="true">
                    {user.name.slice(0, 2).toUpperCase()}
                  </span>
                  <div className="user-card__id">
                    <p className="user-card__name">{user.name}</p>
                    <p className="user-card__platform mono">{user.platform}</p>
                  </div>
                  <Switch
                    checked={user.enabled}
                    onChange={() => void toggleUser(user)}
                    label={`${user.enabled ? 'Suspend' : 'Enable'} ${user.name}`}
                  />
                </div>

                <div className="user-card__usage">
                  <div className="user-card__usage-head">
                    <span className="label">Monthly usage</span>
                    <span className="mono">
                      {formatBytes(user.usedBytes)}
                      {user.quotaBytesMonth ? ` / ${formatBytes(user.quotaBytesMonth, 0)}` : ' · no quota'}
                    </span>
                  </div>
                  {user.quotaBytesMonth ? (
                    <ProgressBar
                      value={usage}
                      tone={usage > 90 ? 'danger' : usage > 75 ? 'warning' : 'mint'}
                      label={`${user.name} monthly quota`}
                    />
                  ) : (
                    <p className="user-card__unlimited">Unlimited · advisory only</p>
                  )}
                </div>

                <dl className="facts facts--compact">
                  <div>
                    <dt>Sticky TTL</dt>
                    <dd className="mono">{user.stickyTtl}</dd>
                  </div>
                  <div>
                    <dt>API keys</dt>
                    <dd className="mono">{keyCount}</dd>
                  </div>
                  <div>
                    <dt>Allowlist</dt>
                    <dd className="mono">{user.allowlist.length} CIDR</dd>
                  </div>
                  <div>
                    <dt>Created</dt>
                    <dd>{formatDate(user.createdAt)}</dd>
                  </div>
                </dl>

                <div className="user-card__foot">
                  <Button
                    size="sm"
                    variant="secondary"
                    icon={<ShieldCheck size={13} />}
                    onClick={() => openAllowlist(user)}
                  >
                    Allowlist
                  </Button>
                  <Badge tone={user.enabled ? 'success' : 'warning'}>
                    {user.enabled ? 'Enabled' : 'Suspended'}
                  </Badge>
                </div>
              </Card>
            )
          })}
        </div>
      )}

      <Card
        title="API keys"
        subtitle="Hashed at rest by ajs-accountd. Secrets are shown once at creation."
        bodyClassName="card__body--flush"
      >
        {keys.status === 'error' ? (
          <ErrorState message={keys.error ?? 'Unknown error'} onRetry={keys.reload} />
        ) : keys.status === 'loading' && !keys.data ? (
          <TableSkeleton rows={5} columns={6} />
        ) : (
          <DataTable
            caption="Panel API keys"
            columns={columns}
            rows={keys.data ?? []}
            rowKey={(key) => key.id}
            empty={
              <EmptyState
                title="No API keys yet"
                description="Create a key to let a client talk to the panel API."
                icon={<KeyRound size={20} aria-hidden="true" />}
                action={
                  <Button variant="primary" icon={<Plus size={14} />} onClick={openCreate}>
                    Create API key
                  </Button>
                }
              />
            }
          />
        )}
      </Card>

      <Modal
        open={createOpen}
        title={created ? 'API key created' : 'Create API key'}
        description={
          created
            ? 'This secret is shown once. Store it before closing this dialog.'
            : 'The key inherits the user’s platform, sticky TTL and quota.'
        }
        onClose={closeCreate}
        footer={
          created ? (
            <Button variant="primary" onClick={closeCreate}>
              Done
            </Button>
          ) : (
            <>
              <Button variant="ghost" onClick={closeCreate} disabled={creating}>
                Cancel
              </Button>
              <Button type="submit" form="create-key-form" variant="primary" loading={creating}>
                Create key
              </Button>
            </>
          )
        }
      >
        {created ? (
          <div className="stack">
            <div className="secret">
              <code className="secret__value mono">{created.secret}</code>
              <CopyButton
                value={created.secret}
                label="Copy API key"
                toastMessage="API key copied"
              />
            </div>
            <p className="callout callout--warning">
              <ShieldCheck size={14} aria-hidden="true" /> Copy this value now. The panel stores only a
              hash and prefix; it cannot be recovered.
            </p>
            <dl className="facts facts--compact">
              <div>
                <dt>Label</dt>
                <dd>{created.key.label}</dd>
              </div>
              <div>
                <dt>User</dt>
                <dd>{created.key.userName}</dd>
              </div>
              <div>
                <dt>Prefix</dt>
                <dd className="mono">{created.key.prefix}</dd>
              </div>
            </dl>
          </div>
        ) : (
          <form id="create-key-form" className="form" onSubmit={submitCreate}>
            <Field label="User" htmlFor="key-user">
              <Select
                id="key-user"
                value={createUserId}
                onChange={(event) => setCreateUserId(event.target.value)}
              >
                {users.data?.map((user) => (
                  <option key={user.id} value={user.id}>
                    {user.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Label" htmlFor="key-label" hint="Where will this key be used?">
              <Input
                id="key-label"
                value={createLabel}
                onChange={(event) => setCreateLabel(event.target.value)}
                placeholder="e.g. CI runner"
                autoComplete="off"
              />
            </Field>
            {createError ? (
              <p className="form-error" role="alert">
                {createError}
              </p>
            ) : null}
          </form>
        )}
      </Modal>

      <Modal
        open={allowlistUser !== null}
        title={allowlistUser ? `Allowlist · ${allowlistUser.name}` : 'Allowlist'}
        description="One CIDR per line. Loopback is implied; empty means deny all remote sources."
        onClose={() => setAllowlistUser(null)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setAllowlistUser(null)} disabled={savingAllowlist}>
              Cancel
            </Button>
            <Button variant="primary" onClick={() => void saveAllowlist()} loading={savingAllowlist}>
              Save allowlist
            </Button>
          </>
        }
      >
        <Field label="Allowed CIDRs" htmlFor="allowlist-input">
          <Textarea
            id="allowlist-input"
            rows={6}
            value={allowlistText}
            onChange={(event) => setAllowlistText(event.target.value)}
            placeholder={'127.0.0.1/32\n10.20.0.0/16'}
            className="mono"
          />
        </Field>
      </Modal>

      <ConfirmDialog
        open={revokeTarget !== null}
        title="Revoke API key?"
        description={
          revokeTarget
            ? `${revokeTarget.label} (${revokeTarget.prefix}…) for ${revokeTarget.userName} stops working immediately.`
            : ''
        }
        confirmLabel="Revoke key"
        busy={revoking}
        onConfirm={() => void confirmRevoke()}
        onCancel={() => setRevokeId(null)}
      />
    </div>
  )
}
