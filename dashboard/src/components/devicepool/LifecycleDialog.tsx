import { useEffect, useId, useState } from 'react'
import { useI18n } from '../../lib/i18n'
import { DEVICE_CONFIRM_TOKENS } from '../../lib/types'
import type { DevicePoolConfirmAction, DevicePoolRecord } from '../../lib/types'
import { Modal } from '../Overlay'
import { Button, Field, Input } from '../primitives'
import { deviceLabel } from './shared'

interface LifecycleDialogProps {
  action: DevicePoolConfirmAction | null
  record: DevicePoolRecord | null
  busy: boolean
  error: string
  onCancel: () => void
  onConfirm: (reason: string) => void
}

/**
 * The typed-token gate for release / retire / quarantine / purge. The token is
 * the daemon's wire contract, so it is never translated; only the copy around
 * it is.
 */
export function LifecycleDialog({
  action,
  record,
  busy,
  error,
  onCancel,
  onConfirm,
}: LifecycleDialogProps) {
  const { t } = useI18n()
  const tokenId = useId()
  const reasonId = useId()
  const [typed, setTyped] = useState('')
  const [reason, setReason] = useState('')

  useEffect(() => {
    setTyped('')
    setReason('')
  }, [action, record?.id])

  if (!action || !record) return null
  const token = DEVICE_CONFIRM_TOKENS[action]
  const needsReason = action === 'retire' || action === 'quarantine'

  return (
    <Modal
      open
      title={t(`devicePool.lifecycle.${action}Title`)}
      description={t(`devicePool.lifecycle.${action}Desc`, { id: deviceLabel(record) })}
      onClose={onCancel}
      footer={
        <>
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="danger"
            onClick={() => onConfirm(reason)}
            loading={busy}
            disabled={typed !== token}
          >
            {t(`devicePool.action.${action}`)}
          </Button>
        </>
      }
    >
      <div className="form">
        {needsReason ? (
          <Field label={t('devicePool.lifecycle.reason')} htmlFor={reasonId}>
            <Input
              id={reasonId}
              value={reason}
              maxLength={200}
              placeholder={t('devicePool.lifecycle.reasonPlaceholder')}
              onChange={(event) => setReason(event.target.value)}
            />
          </Field>
        ) : null}
        <Field
          label={t('devicePool.lifecycle.token')}
          htmlFor={tokenId}
          hint={t('common.typeToConfirm', { text: token })}
        >
          <Input
            id={tokenId}
            value={typed}
            onChange={(event) => setTyped(event.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </Field>
        {error ? (
          <p className="form-error" role="alert">
            {error}
          </p>
        ) : null}
      </div>
    </Modal>
  )
}
