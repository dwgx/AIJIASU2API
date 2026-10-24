import { useEffect, useId, useState } from 'react'
import { Download } from 'lucide-react'
import { api } from '../../lib/api'
import { useI18n } from '../../lib/i18n'
import { DEVICE_EXPORT_CONFIRM } from '../../lib/types'
import type { DevicePoolExportScope } from '../../lib/types'
import { downloadBlob } from '../accounts/AccountExportDialog'
import { Modal } from '../Overlay'
import { Button, Checkbox, Field, Input, Select } from '../primitives'
import { useToast } from '../Toast'
import { deviceErrorText, deviceExportFilename } from './shared'

interface DeviceExportDialogProps {
  open: boolean
  onClose: () => void
  onExported: (count: number) => void
}

const SCOPES: DevicePoolExportScope[] = ['assigned', 'retired', 'all']

export function DeviceExportDialog({ open, onClose, onExported }: DeviceExportDialogProps) {
  const { t } = useI18n()
  const { push } = useToast()
  const passphraseId = useId()
  const confirmId = useId()
  const tokenId = useId()
  const [scope, setScope] = useState<DevicePoolExportScope>('assigned')
  const [passphrase, setPassphrase] = useState('')
  const [confirm, setConfirm] = useState('')
  const [includeKeys, setIncludeKeys] = useState(true)
  const [includeLabels, setIncludeLabels] = useState(true)
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [exported, setExported] = useState<{ count: number; filename: string } | null>(null)

  useEffect(() => {
    if (!open) return
    setScope('assigned')
    setPassphrase('')
    setConfirm('')
    setIncludeKeys(true)
    setIncludeLabels(true)
    setToken('')
    setBusy(false)
    setError('')
    setExported(null)
  }, [open])

  const submit = async () => {
    const bytes = new TextEncoder().encode(passphrase).length
    if (bytes < 12) {
      setError(t('devicePool.export.tooShort'))
      return
    }
    if (bytes > 1024) {
      setError(t('devicePool.export.tooLong'))
      return
    }
    if (passphrase !== confirm) {
      setError(t('devicePool.export.mismatch'))
      return
    }
    if (token !== DEVICE_EXPORT_CONFIRM) {
      setError(t('devicePool.reason.confirmRequired'))
      return
    }
    setBusy(true)
    setError('')
    try {
      const envelope = await api.exportDevicePool({
        passphrase,
        scope,
        includeAccountKeys: includeKeys,
        includeLabels,
        confirm: DEVICE_EXPORT_CONFIRM,
      })
      const filename = deviceExportFilename()
      downloadBlob(
        new Blob([JSON.stringify(envelope, null, 2)], { type: 'application/json' }),
        filename,
      )
      setExported({ count: envelope.deviceCount, filename })
      setPassphrase('')
      setConfirm('')
      setToken('')
      push(t('devicePool.export.done', { count: envelope.deviceCount }), 'success')
      onExported(envelope.deviceCount)
    } catch (cause) {
      setError(deviceErrorText(t, cause, t('devicePool.export.failed')))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('devicePool.export.title')}
      description={t('devicePool.export.desc')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t('common.close')}
          </Button>
          <Button
            variant="primary"
            icon={<Download size={14} />}
            onClick={() => void submit()}
            loading={busy}
          >
            {t('devicePool.export.submit')}
          </Button>
        </>
      }
    >
      <div className="form">
        <Field label={t('devicePool.export.scope')}>
          <Select
            aria-label={t('devicePool.export.scope')}
            value={scope}
            options={SCOPES.map((value) => ({
              value,
              label: t(`devicePool.export.scope.${value}`),
            }))}
            onChange={(value) => setScope(value as DevicePoolExportScope)}
          />
        </Field>
        <div className="form-grid">
          <Field
            label={t('devicePool.export.passphrase')}
            htmlFor={passphraseId}
            hint={t('devicePool.export.passphraseHint')}
          >
            <Input
              id={passphraseId}
              type="password"
              value={passphrase}
              autoComplete="new-password"
              onChange={(event) => setPassphrase(event.target.value)}
            />
          </Field>
          <Field label={t('devicePool.export.confirm')} htmlFor={confirmId}>
            <Input
              id={confirmId}
              type="password"
              value={confirm}
              autoComplete="new-password"
              onChange={(event) => setConfirm(event.target.value)}
            />
          </Field>
        </div>
        <div className="device-pool__checks">
          <Checkbox
            label={t('devicePool.export.includeKeys')}
            checked={includeKeys}
            onChange={(event) => setIncludeKeys(event.target.checked)}
          />
          <Checkbox
            label={t('devicePool.export.includeLabels')}
            checked={includeLabels}
            onChange={(event) => setIncludeLabels(event.target.checked)}
          />
        </div>
        <Field
          label={t('devicePool.export.token')}
          htmlFor={tokenId}
          hint={t('common.typeToConfirm', { text: DEVICE_EXPORT_CONFIRM })}
        >
          <Input
            id={tokenId}
            value={token}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => setToken(event.target.value)}
          />
        </Field>
        {error ? (
          <p className="form-error" role="alert">
            {error}
          </p>
        ) : null}
        {exported ? (
          <p className="form-note" role="status">
            {t('devicePool.export.done', { count: exported.count })} ·{' '}
            <span className="mono">{exported.filename}</span>
          </p>
        ) : null}
      </div>
    </Modal>
  )
}
