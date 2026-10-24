import { KeyRound, Plus, ShieldCheck, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '../lib/api'
import { formatBytes, formatDate, formatRelative, percent } from '../lib/format'
import { useI18n } from '../lib/i18n'
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
  const { lang, t } = useI18n()
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
      setCreateError(t('users.chooseUserError'))
      return
    }
    setCreating(true)
    setCreateError('')
    try {
      const result = await api.createKey({ userId: createUserId, label: createLabel })
      setCreated(result)
      keys.reload()
      push(t('users.toastCreated'), 'success')
    } catch (cause) {
      setCreateError(cause instanceof Error ? cause.message : t('users.createError'))
    } finally {
      setCreating(false)
    }
  }

  const toggleUser = async (user: PanelUser) => {
    try {
      await api.setUserEnabled(user.id, !user.enabled)
      users.reload()
      push(
        t(user.enabled ? 'users.toastUserSuspended' : 'users.toastUserEnabled', { name: user.name }),
        'success',
      )
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('users.toastUserFailed'), 'danger')
    }
  }

  const confirmRevoke = async () => {
    if (!revokeId) return
    setRevoking(true)
    try {
      await api.revokeKey(revokeId)
      keys.reload()
      push(t('users.toastRevoked'), 'success')
      setRevokeId(null)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('users.toastRevokeFailed'), 'danger')
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
      push(t('users.toastAllowlist', { name: allowlistUser.name }), 'success')
      setAllowlistUser(null)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('users.toastAllowlistFailed'), 'danger')
    } finally {
      setSavingAllowlist(false)
    }
  }

  const columns: Array<Column<ApiKey>> = [
    {
      key: 'label',
      label: t('users.columnLabel'),
      render: (key) => (
        <div className="cell-stack">
          <span className="cell-strong">{key.label}</span>
          <span className="cell-sub mono">{key.prefix}…</span>
        </div>
      ),
    },
    {
      key: 'user',
      label: t('users.columnUser'),
      render: (key) => <span>{key.userName}</span>,
    },
    {
      key: 'created',
      label: t('users.columnCreated'),
      render: (key) => <span className="cell-sub">{formatDate(key.createdAt)}</span>,
    },
    {
      key: 'used',
      label: t('users.columnLastUsed'),
      render: (key) => (
        <span className="cell-sub">
          {key.lastUsedAt ? formatRelative(key.lastUsedAt, Date.now(), lang) : t('common.never')}
        </span>
      ),
    },
    {
      key: 'status',
      label: t('users.columnStatus'),
      render: (key) =>
        key.revokedAt ? (
          <Badge tone="danger">{t('users.revoked')}</Badge>
        ) : (
          <Badge tone="success">{t('users.active')}</Badge>
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
            label={t('users.revokeKey', { label: key.label })}
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
        title={t('users.title')}
        subtitle={t('users.subtitle')}
        refresh={summarizeRefresh([users, keys])}
        actions={
          <Button
            variant="primary"
            icon={<Plus size={15} />}
            onClick={openCreate}
            disabled={!usersReady}
            aria-disabled={!usersReady}
            title={usersReady ? undefined : t('users.waitingForUsers')}
          >
            {t('users.createKey')}
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
                    label={t(user.enabled ? 'users.suspend' : 'users.enable', { name: user.name })}
                  />
                </div>

                <div className="user-card__usage">
                  <div className="user-card__usage-head">
                    <span className="label">{t('users.monthlyUsage')}</span>
                    <span className="mono">
                      {formatBytes(user.usedBytes)}
                      {user.quotaBytesMonth
                        ? ` / ${formatBytes(user.quotaBytesMonth, 0)}`
                        : t('users.noQuota')}
                    </span>
                  </div>
                  {user.quotaBytesMonth ? (
                    <ProgressBar
                      value={usage}
                      tone={usage > 90 ? 'danger' : usage > 75 ? 'warning' : 'mint'}
                      label={t('users.monthlyQuota', { name: user.name })}
                    />
                  ) : (
                    <p className="user-card__unlimited">{t('users.unlimited')}</p>
                  )}
                </div>

                <dl className="facts facts--compact">
                  <div>
                    <dt>{t('users.stickyTtl')}</dt>
                    <dd className="mono">{user.stickyTtl}</dd>
                  </div>
                  <div>
                    <dt>{t('users.apiKeys')}</dt>
                    <dd className="mono">{keyCount}</dd>
                  </div>
                  <div>
                    <dt>{t('users.allowlist')}</dt>
                    <dd className="mono">{user.allowlist.length} CIDR</dd>
                  </div>
                  <div>
                    <dt>{t('users.created')}</dt>
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
                    {t('users.allowlistButton')}
                  </Button>
                  <Badge tone={user.enabled ? 'success' : 'warning'}>
                    {user.enabled ? t('users.enabled') : t('users.suspended')}
                  </Badge>
                </div>
              </Card>
            )
          })}
        </div>
      )}

      <Card
        title={t('users.keysTitle')}
        subtitle={t('users.keysSubtitle')}
        bodyClassName="card__body--flush"
      >
        {keys.status === 'error' ? (
          <ErrorState message={keys.error ?? t('common.unknownError')} onRetry={keys.reload} />
        ) : keys.status === 'loading' && !keys.data ? (
          <TableSkeleton rows={5} columns={6} />
        ) : (
          <DataTable
            caption={t('users.keysCaption')}
            columns={columns}
            rows={keys.data ?? []}
            rowKey={(key) => key.id}
            empty={
              <EmptyState
                title={t('users.emptyKeysTitle')}
                description={t('users.emptyKeysDesc')}
                icon={<KeyRound size={20} aria-hidden="true" />}
                action={
                  <Button variant="primary" icon={<Plus size={14} />} onClick={openCreate}>
                    {t('users.createKey')}
                  </Button>
                }
              />
            }
          />
        )}
      </Card>

      <Modal
        open={createOpen}
        title={created ? t('users.createdTitle') : t('users.createTitle')}
        description={created ? t('users.createdDesc') : t('users.createDesc')}
        onClose={closeCreate}
        footer={
          created ? (
            <Button variant="primary" onClick={closeCreate}>
              {t('users.done')}
            </Button>
          ) : (
            <>
              <Button variant="ghost" onClick={closeCreate} disabled={creating}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" form="create-key-form" variant="primary" loading={creating}>
                {t('users.createKeyButton')}
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
                label={t('users.copyKey')}
                toastMessage={t('users.keyCopied')}
              />
            </div>
            <p className="callout callout--warning">
              <ShieldCheck size={14} aria-hidden="true" /> {t('users.secretWarning')}
            </p>
            <dl className="facts facts--compact">
              <div>
                <dt>{t('users.label')}</dt>
                <dd>{created.key.label}</dd>
              </div>
              <div>
                <dt>{t('users.columnUser')}</dt>
                <dd>{created.key.userName}</dd>
              </div>
              <div>
                <dt>{t('users.prefix')}</dt>
                <dd className="mono">{created.key.prefix}</dd>
              </div>
            </dl>
          </div>
        ) : (
          <form id="create-key-form" className="form" onSubmit={submitCreate}>
            <Field label={t('users.columnUser')} htmlFor="key-user">
              <Select
                variant="form"
                id="key-user"
                value={createUserId}
                onChange={setCreateUserId}
                options={(users.data ?? []).map((user) => ({ value: user.id, label: user.name }))}
              />
            </Field>
            <Field label={t('users.label')} htmlFor="key-label" hint={t('users.labelHint')}>
              <Input
                id="key-label"
                value={createLabel}
                onChange={(event) => setCreateLabel(event.target.value)}
                placeholder={t('users.labelPlaceholder')}
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
        title={
          allowlistUser
            ? t('users.allowlistTitle', { name: allowlistUser.name })
            : t('users.allowlistButton')
        }
        description={t('users.allowlistDesc')}
        onClose={() => setAllowlistUser(null)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setAllowlistUser(null)} disabled={savingAllowlist}>
              {t('common.cancel')}
            </Button>
            <Button variant="primary" onClick={() => void saveAllowlist()} loading={savingAllowlist}>
              {t('users.saveAllowlist')}
            </Button>
          </>
        }
      >
        <Field label={t('users.allowedCidrs')} htmlFor="allowlist-input">
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
        title={t('users.revokeTitle')}
        description={
          revokeTarget
            ? t('users.revokeDesc', {
                label: revokeTarget.label,
                prefix: revokeTarget.prefix,
                user: revokeTarget.userName,
              })
            : ''
        }
        confirmLabel={t('users.revokeConfirm')}
        busy={revoking}
        onConfirm={() => void confirmRevoke()}
        onCancel={() => setRevokeId(null)}
      />
    </div>
  )
}
