import { useId, useMemo, useState } from 'react'
import { KeyRound, Plus, RefreshCw, ShieldCheck, Trash2 } from 'lucide-react'
import { api } from '../../lib/api'
import { ApiError } from '../../lib/errors'
import { formatDate, formatRelative } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { SCOPES } from '../../lib/types'
import type {
  Account,
  PanelUser,
  ScopedKey,
  ScopedKeyCreateInput,
  ScopedKeyCreated,
  Scope,
} from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { CopyButton } from '../CopyButton'
import { ConfirmDialog, Modal } from '../Overlay'
import { useToast } from '../Toast'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  Input,
  Select,
  Skeleton,
} from '../primitives'
import './AccessSection.css'

interface AccessData {
  keys: ScopedKey[]
  users: PanelUser[]
  accounts: Account[]
}

const STATUS_LABEL = {
  active: 'access.statusActive',
  expired: 'access.statusExpired',
  revoked: 'access.statusRevoked',
} as const

function statusOf(key: ScopedKey): keyof typeof STATUS_LABEL {
  if (key.revokedAt) return 'revoked'
  if (key.expiresAt && Date.parse(key.expiresAt) <= Date.now()) return 'expired'
  return 'active'
}

/**
 * Scoped API keys live in Settings → Access: one row per key with its scopes,
 * limits and revocation state. The plaintext secret exists only in the create
 * response, so it is rendered once and never written to storage.
 */
export function AccessSection() {
  const { t, lang } = useI18n()
  const { push } = useToast()
  const data = useAsync<AccessData>(async () => {
    const [keys, users, accounts] = await Promise.all([
      api.listScopedKeys(),
      api.listUsers(),
      api.listAccounts(),
    ])
    // A misrouted or older daemon can answer a non-array here; render the
    // honest empty state instead of throwing inside the settings shell.
    return {
      keys: Array.isArray(keys) ? keys : [],
      users: Array.isArray(users) ? users : [],
      accounts: Array.isArray(accounts) ? accounts : [],
    }
  }, [])
  const [createOpen, setCreateOpen] = useState(false)
  const [revokeTarget, setRevokeTarget] = useState<ScopedKey | null>(null)
  const [revoking, setRevoking] = useState(false)

  const keys = data.data?.keys ?? []
  const accountLabels = useMemo(() => {
    const labels = new Map<string, string>()
    for (const account of data.data?.accounts ?? []) labels.set(account.id, account.label)
    return labels
  }, [data.data])

  const confirmRevoke = async () => {
    if (!revokeTarget) return
    setRevoking(true)
    try {
      await api.revokeScopedKey(revokeTarget.id)
      push(t('access.revokedToast', { label: revokeTarget.label }), 'success')
      setRevokeTarget(null)
      data.reload()
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        push(t('access.revokeConflict'), 'danger')
        setRevokeTarget(null)
        data.reload()
      } else if (cause instanceof ApiError && cause.status === 404) {
        push(t('access.revokeNotFound'), 'danger')
        setRevokeTarget(null)
        data.reload()
      } else {
        push(cause instanceof Error ? cause.message : t('access.revokeFailed'), 'danger')
      }
    } finally {
      setRevoking(false)
    }
  }

  return (
    <Card
      title={t('access.cardTitle')}
      subtitle={t('access.cardSubtitle')}
      actions={
        <div className="scoped-head-actions">
          {data.data ? (
            <span className="scoped-count">
              {t(keys.length === 1 ? 'access.countOne' : 'access.count', {
                count: keys.length,
              })}
            </span>
          ) : null}
          <IconButton
            label={t('access.refresh')}
            icon={<RefreshCw size={15} />}
            onClick={data.reload}
          />
          <Button
            size="sm"
            variant="primary"
            icon={<Plus size={14} />}
            onClick={() => setCreateOpen(true)}
            disabled={!data.data || data.data.users.length === 0}
          >
            {t('access.new')}
          </Button>
        </div>
      }
    >
      {data.status === 'loading' && !data.data ? (
        <div className="scoped-skeleton" aria-busy="true">
          <Skeleton height={76} />
          <Skeleton height={76} />
          <Skeleton height={76} />
        </div>
      ) : data.status === 'error' && !data.data ? (
        <ErrorState message={data.error ?? t('access.loadFailed')} onRetry={data.reload} />
      ) : keys.length === 0 ? (
        <EmptyState
          title={t('access.emptyTitle')}
          description={t('access.emptyDesc')}
          icon={<KeyRound size={20} aria-hidden="true" />}
          action={
            <Button
              variant="primary"
              icon={<Plus size={14} />}
              onClick={() => setCreateOpen(true)}
              disabled={(data.data?.users.length ?? 0) === 0}
            >
              {t('access.new')}
            </Button>
          }
        />
      ) : (
        <>
          {data.error ? (
            <p className="form-error" role="alert">
              {data.error}
            </p>
          ) : null}
          <ul className="scoped-list" aria-label={t('access.cardTitle')}>
            {keys.map((key) => {
              const status = statusOf(key)
              const accounts =
                key.accountIds.length === 0
                  ? t('access.allAccounts')
                  : t(
                      key.accountIds.length === 1
                        ? 'access.accountCountOne'
                        : 'access.accountCount',
                      { count: key.accountIds.length },
                    )
              return (
                <li className="scoped-row" key={key.id}>
                  <div className="scoped-row__main">
                    <div className="scoped-row__head">
                      <span className="scoped-row__label">{key.label}</span>
                      <Badge
                        tone={
                          status === 'active' ? 'success' : status === 'expired' ? 'warning' : 'danger'
                        }
                      >
                        {t(STATUS_LABEL[status])}
                      </Badge>
                      <code className="scoped-row__prefix mono">{key.prefix}…</code>
                    </div>
                    <p className="scoped-row__user">
                      {key.userName} · {accounts}
                    </p>
                    <div className="scoped-row__chips">
                      {key.scopes.map((scope) => (
                        <span className="scoped-chip" key={scope}>
                          {scope}
                        </span>
                      ))}
                    </div>
                    <dl className="scoped-row__facts">
                      <div>
                        <dt>{t('access.rateLimit')}</dt>
                        <dd className="mono">
                          {key.rateLimitPerMin === 0
                            ? t('access.unlimited')
                            : t('access.perMin', { count: key.rateLimitPerMin })}
                        </dd>
                      </div>
                      <div>
                        <dt>{t('access.expiry')}</dt>
                        <dd>{key.expiresAt ? formatDate(key.expiresAt) : t('access.neverExpires')}</dd>
                      </div>
                      <div>
                        <dt>{t('access.lastUsed')}</dt>
                        <dd>
                          {key.lastUsedAt
                            ? formatRelative(key.lastUsedAt, Date.now(), lang)
                            : t('common.never')}
                        </dd>
                      </div>
                    </dl>
                    {key.accountIds.length > 0 ? (
                      <p className="scoped-row__accounts">
                        {key.accountIds
                          .map((id) => accountLabels.get(id) ?? id)
                          .join(' · ')}
                      </p>
                    ) : null}
                  </div>
                  {key.revokedAt ? null : (
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<Trash2 size={14} />}
                      aria-label={t('access.revokeFor', { label: key.label })}
                      onClick={() => setRevokeTarget(key)}
                    >
                      {t('access.revoke')}
                    </Button>
                  )}
                </li>
              )
            })}
          </ul>
        </>
      )}

      {createOpen && data.data ? (
        <ScopedKeyDialog
          users={data.data.users}
          accounts={data.data.accounts}
          onClose={() => setCreateOpen(false)}
          onCreated={data.reload}
        />
      ) : null}

      <ConfirmDialog
        open={revokeTarget !== null}
        title={t('access.revokeTitle')}
        description={
          revokeTarget
            ? t('access.revokeDesc', {
                label: revokeTarget.label,
                prefix: revokeTarget.prefix,
                user: revokeTarget.userName,
              })
            : ''
        }
        confirmLabel={t('access.revokeConfirm')}
        requireText={revokeTarget?.prefix}
        requireTextLabel={t('access.prefix')}
        busy={revoking}
        onConfirm={() => void confirmRevoke()}
        onCancel={() => setRevokeTarget(null)}
      />
    </Card>
  )
}

function ScopedKeyDialog({
  users,
  accounts,
  onClose,
  onCreated,
}: {
  users: PanelUser[]
  accounts: Account[]
  onClose: () => void
  onCreated: () => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const baseId = useId()
  const [userId, setUserId] = useState(users[0]?.id ?? '')
  const [label, setLabel] = useState('')
  const [scopes, setScopes] = useState<Scope[]>([])
  const [accountIds, setAccountIds] = useState<string[]>([])
  const [expiresAt, setExpiresAt] = useState('')
  const [rateLimit, setRateLimit] = useState('60')
  const [formError, setFormError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<ScopedKeyCreated | null>(null)

  const scopeOptions = useMemo(
    () => SCOPES.map((scope) => ({ value: scope, label: scope })),
    [],
  )
  const accountOptions = useMemo(
    () =>
      accounts.map((account) => ({
        value: account.id,
        label: account.label,
        description: account.id,
      })),
    [accounts],
  )

  const submit = async () => {
    if (!label.trim()) {
      setFormError(t('access.labelRequired'))
      return
    }
    if (scopes.length === 0) {
      setFormError(t('access.scopesRequired'))
      return
    }
    const rate = rateLimit.trim()
    const rateValue = rate === '' ? undefined : Number(rate)
    if (rateValue !== undefined && (!Number.isInteger(rateValue) || rateValue < 0)) {
      setFormError(t('access.rateLimitInvalid'))
      return
    }
    let expires = ''
    if (expiresAt) {
      const parsed = new Date(expiresAt)
      if (Number.isNaN(parsed.getTime()) || parsed.getTime() <= Date.now()) {
        setFormError(t('access.expiresAtInvalid'))
        return
      }
      expires = parsed.toISOString()
    }
    const input: ScopedKeyCreateInput = {
      userId,
      label: label.trim(),
      scopes,
      accountIds,
      ...(rateValue === undefined ? {} : { rateLimitPerMin: rateValue }),
      ...(expires ? { expiresAt: expires } : {}),
    }
    setFormError(null)
    setCreating(true)
    try {
      const result = await api.createScopedKey(input)
      setCreated(result)
      onCreated()
      push(t('access.createdToast', { label: result.key.label }), 'success')
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : t('access.createFailed'))
    } finally {
      setCreating(false)
    }
  }

  return (
    <Modal
      open
      title={created ? t('access.createdTitle') : t('access.createTitle')}
      description={created ? t('access.createdDesc') : t('access.createDesc')}
      onClose={onClose}
      footer={
        created ? (
          <Button variant="primary" onClick={onClose}>
            {t('access.done')}
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose} disabled={creating}>
              {t('common.cancel')}
            </Button>
            <Button
              type="submit"
              form={`${baseId}-form`}
              variant="primary"
              loading={creating}
            >
              {t('access.createAction')}
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
              label={t('access.secretCopy')}
              toastMessage={t('access.secretCopied')}
            />
          </div>
          <p className="callout callout--warning" role="alert">
            <ShieldCheck size={14} aria-hidden="true" /> {t('access.secretWarning')}
          </p>
          <dl className="facts facts--compact">
            <div>
              <dt>{t('access.label')}</dt>
              <dd>{created.key.label}</dd>
            </div>
            <div>
              <dt>{t('access.user')}</dt>
              <dd>{created.key.userName}</dd>
            </div>
            <div>
              <dt>{t('access.prefix')}</dt>
              <dd className="mono">{created.key.prefix}</dd>
            </div>
          </dl>
        </div>
      ) : (
        <form
          id={`${baseId}-form`}
          className="form scoped-form"
          onSubmit={(event) => {
            event.preventDefault()
            void submit()
          }}
        >
          {formError ? (
            <p className="form-error" role="alert">
              {formError}
            </p>
          ) : null}
          <Field label={t('access.user')} htmlFor={`${baseId}-user`}>
            <Select
              variant="form"
              id={`${baseId}-user`}
              value={userId}
              onChange={setUserId}
              options={users.map((user) => ({ value: user.id, label: user.name }))}
            />
          </Field>
          <Field
            label={t('access.label')}
            htmlFor={`${baseId}-label`}
            hint={t('access.labelHint')}
          >
            <Input
              id={`${baseId}-label`}
              value={label}
              maxLength={80}
              placeholder={t('access.labelPlaceholder')}
              autoComplete="off"
              onChange={(event) => setLabel(event.target.value)}
            />
          </Field>
          <Field
            label={t('access.scopes')}
            htmlFor={`${baseId}-scopes`}
            hint={t('access.scopesHint')}
          >
            <Select
              multiple
              searchable
              variant="form"
              id={`${baseId}-scopes`}
              aria-label={t('access.scopes')}
              value={scopes}
              options={scopeOptions}
              placeholder={t('access.scopes')}
              onChange={(values) => setScopes(values as Scope[])}
            />
          </Field>
          <Field
            label={t('access.accounts')}
            htmlFor={`${baseId}-accounts`}
            hint={t('access.accountsHint')}
          >
            <Select
              multiple
              searchable
              variant="form"
              id={`${baseId}-accounts`}
              aria-label={t('access.accounts')}
              value={accountIds}
              options={accountOptions}
              placeholder={t('access.accountsPlaceholder')}
              onChange={setAccountIds}
            />
          </Field>
          <div className="form-grid">
            <Field
              label={t('access.expiresAt')}
              htmlFor={`${baseId}-expires`}
              hint={t('access.expiresAtHint')}
            >
              <Input
                id={`${baseId}-expires`}
                type="datetime-local"
                value={expiresAt}
                onChange={(event) => setExpiresAt(event.target.value)}
              />
            </Field>
            <Field
              label={t('access.rateLimit')}
              htmlFor={`${baseId}-rate`}
              hint={t('access.rateLimitHint')}
            >
              <Input
                id={`${baseId}-rate`}
                type="number"
                min={0}
                step={1}
                value={rateLimit}
                onChange={(event) => setRateLimit(event.target.value)}
              />
            </Field>
          </div>
        </form>
      )}
    </Modal>
  )
}
