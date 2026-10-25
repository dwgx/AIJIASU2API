import { KeyRound, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { CopyButton } from '../../components/CopyButton'
import { DataTable } from '../../components/DataTable'
import type { Column } from '../../components/DataTable'
import { ConfirmDialog, Modal } from '../../components/Overlay'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  Input,
  TableSkeleton,
} from '../../components/primitives'
import { useToast } from '../../components/Toast'
import { formatDate } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { portalApi } from '../../lib/portal-api'
import type { ApiKey } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { portalErrorText } from './PortalFrame'

/**
 * Key metadata only: the daemon returns the raw `ajs_live_` secret once, in
 * the issue response, and never serializes hashes. Revocation is permanent.
 */
export default function PortalKeys() {
  const { t } = useI18n()
  const { push } = useToast()
  const keys = useAsync(() => portalApi.keys(), [])
  const [issueOpen, setIssueOpen] = useState(false)
  const [label, setLabel] = useState('')
  const [issueBusy, setIssueBusy] = useState(false)
  const [issueError, setIssueError] = useState('')
  const [secret, setSecret] = useState<string | null>(null)
  const [issuedLabel, setIssuedLabel] = useState('')
  const [revokeTarget, setRevokeTarget] = useState<ApiKey | null>(null)
  const [revokeBusy, setRevokeBusy] = useState(false)

  const openIssue = () => {
    setLabel('')
    setIssueError('')
    setIssueOpen(true)
  }

  const submitIssue = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setIssueBusy(true)
    setIssueError('')
    try {
      const created = await portalApi.issueKey(label.trim())
      setIssueOpen(false)
      setIssuedLabel(created.key.label)
      setSecret(created.secret)
      push(t('portal.keys.issued', { label: created.key.label }), 'success')
      keys.reload()
    } catch (cause) {
      setIssueError(portalErrorText(cause, t))
    } finally {
      setIssueBusy(false)
    }
  }

  const confirmRevoke = async () => {
    if (!revokeTarget) return
    setRevokeBusy(true)
    try {
      await portalApi.revokeKey(revokeTarget.id)
      push(t('portal.keys.revokedToast', { label: revokeTarget.label }), 'success')
      keys.reload()
    } catch (cause) {
      push(portalErrorText(cause, t), 'danger')
    } finally {
      setRevokeBusy(false)
      setRevokeTarget(null)
    }
  }

  const columns: Array<Column<ApiKey>> = [
    {
      key: 'label',
      label: t('portal.keys.label'),
      render: (key) => (
        <span className="portal-key-label">
          <KeyRound size={13} aria-hidden="true" />
          {key.label}
        </span>
      ),
    },
    {
      key: 'prefix',
      label: t('portal.keys.prefix'),
      render: (key) => <span className="mono">{key.prefix}…</span>,
    },
    {
      key: 'created',
      label: t('portal.keys.created'),
      render: (key) => formatDate(key.createdAt),
    },
    {
      key: 'lastUsed',
      label: t('portal.keys.lastUsed'),
      render: (key) => (key.lastUsedAt ? formatDate(key.lastUsedAt) : t('portal.keys.neverUsed')),
    },
    {
      key: 'status',
      label: t('portal.leases.status'),
      render: (key) => (
        <Badge tone={key.revokedAt ? 'danger' : 'success'}>
          {key.revokedAt ? t('portal.keys.revoked') : t('portal.keys.active')}
        </Badge>
      ),
    },
    {
      key: 'actions',
      label: '',
      align: 'right',
      render: (key) =>
        key.revokedAt ? null : (
          <Button
            size="sm"
            variant="ghost"
            icon={<Trash2 size={13} />}
            onClick={() => setRevokeTarget(key)}
          >
            {t('portal.keys.revoke')}
          </Button>
        ),
    },
  ]

  return (
    <Card
      title={t('portal.keys.title')}
      subtitle={t('portal.keys.subtitle')}
      actions={
        <Button variant="primary" icon={<Plus size={14} />} onClick={openIssue}>
          {t('portal.keys.issue')}
        </Button>
      }
    >
      {keys.status === 'loading' ? (
        <TableSkeleton rows={2} columns={6} />
      ) : keys.status === 'error' ? (
        <ErrorState message={t('portal.keys.loadFailed')} onRetry={keys.reload} />
      ) : (
        <DataTable
          caption={t('portal.keys.caption')}
          columns={columns}
          rows={keys.data ?? []}
          rowKey={(key) => key.id}
          empty={
            <EmptyState
              title={t('portal.keys.empty')}
              description={t('portal.keys.emptyHint')}
            />
          }
        />
      )}

      <Modal
        open={issueOpen}
        title={t('portal.keys.issueTitle')}
        description={t('portal.keys.issueBody')}
        onClose={() => setIssueOpen(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setIssueOpen(false)} disabled={issueBusy}>
              {t('common.cancel')}
            </Button>
            <Button
              type="submit"
              form="portal-issue-key-form"
              variant="primary"
              loading={issueBusy}
            >
              {issueBusy ? t('portal.keys.issuing') : t('portal.keys.issueConfirm')}
            </Button>
          </>
        }
      >
        <form id="portal-issue-key-form" className="form" onSubmit={submitIssue}>
          <Field
            label={t('portal.keys.label')}
            htmlFor="portal-key-label"
            hint={t('portal.keys.labelHint')}
            error={issueError}
          >
            <Input
              id="portal-key-label"
              value={label}
              onChange={(event) => setLabel(event.target.value)}
              placeholder={t('portal.keys.labelPlaceholder')}
              autoComplete="off"
              maxLength={80}
              autoFocus
            />
          </Field>
        </form>
      </Modal>

      <Modal
        open={secret !== null}
        title={t('portal.keys.secretTitle')}
        description={t('portal.keys.secretBody')}
        onClose={() => setSecret(null)}
        footer={
          <Button variant="primary" onClick={() => setSecret(null)}>
            {t('portal.keys.secretDone')}
          </Button>
        }
      >
        <div className="form">
          <Field label={t('portal.keys.secretLabel')} htmlFor="portal-key-secret">
            <div className="portal-secret__row">
              <code id="portal-key-secret" className="portal-secret mono">
                {secret}
              </code>
              <CopyButton value={secret ?? ''} label={t('common.copy')} />
            </div>
          </Field>
          <p className="callout callout--muted">
            {t('portal.keys.issued', { label: issuedLabel })}
          </p>
        </div>
      </Modal>

      <ConfirmDialog
        open={revokeTarget !== null}
        title={t('portal.keys.revokeTitle')}
        description={t('portal.keys.revokeBody')}
        confirmLabel={t('portal.keys.revoke')}
        busy={revokeBusy}
        onConfirm={confirmRevoke}
        onCancel={() => setRevokeTarget(null)}
      />
    </Card>
  )
}
