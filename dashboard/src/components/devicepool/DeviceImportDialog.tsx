import { useEffect, useId, useState } from 'react'
import { Upload } from 'lucide-react'
import { api } from '../../lib/api'
import { DevicePoolError } from '../../lib/errors'
import { useI18n } from '../../lib/i18n'
import type { DeviceImportConflict, DeviceImportResult } from '../../lib/types'
import { Modal } from '../Overlay'
import { Button, Checkbox, Field, Input, Textarea } from '../primitives'
import { useToast } from '../Toast'
import { deviceErrorText, shortFingerprint } from './shared'

interface DeviceImportDialogProps {
  open: boolean
  onClose: () => void
  onImported: (count: number) => void
}

export function DeviceImportDialog({ open, onClose, onImported }: DeviceImportDialogProps) {
  const { t } = useI18n()
  const { push } = useToast()
  const envelopeId = useId()
  const passphraseId = useId()
  const [text, setText] = useState('')
  const [filename, setFilename] = useState<string | null>(null)
  const [passphrase, setPassphrase] = useState('')
  const [dryRun, setDryRun] = useState(true)
  const [bind, setBind] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<DeviceImportResult | null>(null)
  const [conflicts, setConflicts] = useState<DeviceImportConflict[]>([])

  useEffect(() => {
    if (!open) return
    setText('')
    setFilename(null)
    setPassphrase('')
    setDryRun(true)
    setBind(false)
    setBusy(false)
    setError('')
    setResult(null)
    setConflicts([])
  }, [open])

  const readFile = async (file: File) => {
    setText(await file.text())
    setFilename(file.name)
    setResult(null)
    setConflicts([])
    setError('')
  }

  const submit = async () => {
    if (!text.trim() || !passphrase) {
      setError(t('devicePool.import.needInput'))
      return
    }
    setBusy(true)
    setError('')
    setResult(null)
    setConflicts([])
    try {
      const imported = await api.importDevicePool({
        passphrase,
        envelope: text,
        dryRun,
        bind,
      })
      setResult(imported)
      if (!imported.dryRun) {
        push(t('devicePool.import.doneApply', { added: imported.added, skipped: imported.skipped }), 'success')
        onImported(imported.added)
      }
    } catch (cause) {
      if (cause instanceof DevicePoolError && cause.conflicts.length > 0) {
        setConflicts(cause.conflicts)
      }
      setError(deviceErrorText(t, cause, t('devicePool.import.failed')))
    } finally {
      setBusy(false)
    }
  }

  const conflictReason = (reason: string) => {
    const key = `devicePool.import.reason.${reason}`
    const copy = t(key)
    return copy === key ? reason : copy
  }

  return (
    <Modal
      open={open}
      title={t('devicePool.import.title')}
      description={t('devicePool.import.desc')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t('common.close')}
          </Button>
          <Button
            variant={dryRun ? 'secondary' : 'danger'}
            icon={<Upload size={14} />}
            onClick={() => void submit()}
            loading={busy}
          >
            {dryRun ? t('devicePool.import.submitDry') : t('devicePool.import.submitApply')}
          </Button>
        </>
      }
    >
      <div className="form">
        <Field label={t('devicePool.import.envelope')} htmlFor={envelopeId}>
          <Textarea
            id={envelopeId}
            rows={6}
            className="mono"
            value={text}
            placeholder={t('devicePool.import.pastePlaceholder')}
            spellCheck={false}
            onChange={(event) => {
              setText(event.target.value)
              setFilename(null)
              setResult(null)
              setConflicts([])
            }}
          />
        </Field>
        <div className="device-pool__file-row">
          <label className="device-pool__file">
            <Upload size={14} aria-hidden="true" />
            <span>{t('devicePool.import.chooseFile')}</span>
            <input
              type="file"
              accept=".json,application/json"
              className="sr-only"
              onChange={(event) => {
                const file = event.target.files?.[0]
                if (file) void readFile(file)
              }}
            />
          </label>
          {filename ? (
            <span className="device-pool__file-name mono">
              {t('devicePool.import.fileLoaded', { name: filename })}
            </span>
          ) : null}
        </div>
        <Field label={t('devicePool.import.passphrase')} htmlFor={passphraseId}>
          <Input
            id={passphraseId}
            type="password"
            value={passphrase}
            autoComplete="current-password"
            onChange={(event) => setPassphrase(event.target.value)}
          />
        </Field>
        <div className="device-pool__checks">
          <Checkbox
            label={t('devicePool.import.dryRun')}
            checked={dryRun}
            onChange={(event) => setDryRun(event.target.checked)}
          />
          <Checkbox
            label={t('devicePool.import.bind')}
            checked={bind}
            onChange={(event) => setBind(event.target.checked)}
          />
        </div>
        {error ? (
          <p className="form-error" role="alert">
            {error}
          </p>
        ) : null}

        {conflicts.length > 0 ? (
          <section className="device-pool__result" aria-label={t('devicePool.import.conflictTitle', { count: conflicts.length })}>
            <h3 className="device-pool__result-title">
              {t('devicePool.import.conflictTitle', { count: conflicts.length })}
            </h3>
            <p className="form-note">{t('devicePool.import.conflictHint')}</p>
            <div className="table-wrap">
              <table className="data-table">
                <caption className="sr-only">{t('devicePool.import.conflictTitle', { count: conflicts.length })}</caption>
                <thead>
                  <tr>
                    <th scope="col">{t('devicePool.import.conflictDevice')}</th>
                    <th scope="col">{t('devicePool.import.conflictExisting')}</th>
                    <th scope="col">{t('devicePool.import.conflictImported')}</th>
                    <th scope="col">{t('devicePool.import.conflictReason')}</th>
                  </tr>
                </thead>
                <tbody>
                  {conflicts.map((conflict) => (
                    <tr key={`${conflict.deviceId}-${conflict.reason}`}>
                      <td className="mono">{conflict.deviceId}</td>
                      <td className="mono">{shortFingerprint(conflict.existingFingerprint)}</td>
                      <td className="mono">{shortFingerprint(conflict.importedFingerprint)}</td>
                      <td>
                        {conflictReason(conflict.reason)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        ) : null}

        {result ? (
          <section className="device-pool__result" aria-label={t('devicePool.import.devices')}>
            <p className="form-note" role="status">
              {result.dryRun
                ? t('devicePool.import.doneDry', { added: result.added, skipped: result.skipped })
                : t('devicePool.import.doneApply', { added: result.added, skipped: result.skipped })}
            </p>
            <p className="device-pool__counts-line">
              {t('devicePool.import.counts', {
                added: result.added,
                skipped: result.skipped,
                bound: result.bound,
                conflicts: result.conflicts,
              })}
            </p>
            <div className="table-wrap">
              <table className="data-table">
                <caption className="sr-only">{t('devicePool.import.devices')}</caption>
                <thead>
                  <tr>
                    <th scope="col">{t('devicePool.import.conflictDevice')}</th>
                    <th scope="col">{t('devicePool.import.conflictReason')}</th>
                    <th scope="col">{t('devicePool.columnState')}</th>
                  </tr>
                </thead>
                <tbody>
                  {result.devices.map((device) => (
                    <tr key={device.deviceId}>
                      <td className="mono">{device.deviceId}</td>
                      <td>
                        {device.action === 'added'
                          ? t('devicePool.import.action.added')
                          : device.action === 'skipped'
                            ? t('devicePool.import.action.skipped')
                            : device.action}
                      </td>
                      <td>{t(`devicePool.state.${device.state}`)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {result.bindingConflicts.length > 0 ? (
              <div className="device-pool__subresult">
                <h4 className="device-pool__result-title">{t('devicePool.import.bindingConflicts')}</h4>
                <ul className="device-pool__list">
                  {result.bindingConflicts.map((conflict) => (
                    <li key={`${conflict.deviceId}-${conflict.reason}`}>
                      <span className="mono">{conflict.deviceId}</span> · {conflict.reason}
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}
            {result.warnings.length > 0 ? (
              <div className="device-pool__subresult">
                <h4 className="device-pool__result-title">{t('devicePool.import.warnings')}</h4>
                <ul className="device-pool__list">
                  {result.warnings.map((warning) => (
                    <li key={warning}>{warning}</li>
                  ))}
                </ul>
              </div>
            ) : null}
          </section>
        ) : null}
      </div>
    </Modal>
  )
}
