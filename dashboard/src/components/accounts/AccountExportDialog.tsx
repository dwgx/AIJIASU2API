import clsx from 'clsx'
import { Download, Eye, EyeOff } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { api, isMockMode } from '../../lib/api'
import { useI18n } from '../../lib/i18n'
import type {
  Account,
  AccountExportRequest,
  AccountIOCapabilities,
  ExportFormat,
} from '../../lib/types'
import { Modal } from '../Overlay'
import { Button, Checkbox, Field, IconButton, Input } from '../primitives'
import { useToast } from '../Toast'
import './accounts-io.css'

export type ExportScope = 'selected' | 'filtered' | 'all'

interface AccountExportDialogProps {
  open: boolean
  accounts: Account[]
  filtered: Account[]
  selected: string[]
  onClose: () => void
  onExported?: () => void
}

/** Starts a browser download from an in-memory response; never touches storage. */
export function downloadBlob(blob: Blob, filename: string): void {
  if (typeof URL === 'undefined' || typeof URL.createObjectURL !== 'function') return
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.rel = 'noopener'
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

function utf8Bytes(value: string): number {
  return new TextEncoder().encode(value).length
}

export function AccountExportDialog({
  open,
  accounts,
  filtered,
  selected,
  onClose,
  onExported,
}: AccountExportDialogProps) {
  const { t } = useI18n()
  const { push } = useToast()
  const scopeName = useId()
  const formatName = useId()
  const [caps, setCaps] = useState<AccountIOCapabilities | null>(null)
  const [capsError, setCapsError] = useState(false)
  const [scope, setScope] = useState<ExportScope>('all')
  const [format, setFormat] = useState<ExportFormat>('encrypted')
  const [passphrase, setPassphrase] = useState('')
  const [confirm, setConfirm] = useState('')
  const [showPassphrase, setShowPassphrase] = useState(false)
  const [plaintextConfirm, setPlaintextConfirm] = useState('')
  const [includeDevice, setIncludeDevice] = useState(true)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) {
      setPassphrase('')
      setConfirm('')
      setPlaintextConfirm('')
      setShowPassphrase(false)
      return
    }
    setScope(selected.length > 0 ? 'selected' : filtered.length !== accounts.length ? 'filtered' : 'all')
    setFormat(isMockMode() ? 'json' : 'encrypted')
    setPassphrase('')
    setConfirm('')
    setShowPassphrase(false)
    setPlaintextConfirm('')
    setIncludeDevice(!isMockMode())
    setErrors({})
    setError('')
    setBusy(false)
    setCaps(null)
    setCapsError(false)
    // Reset only on the closed → open transition; live refreshes must not
    // clobber a scope or passphrase the operator is editing.
  }, [open])

  useEffect(() => {
    if (!open) return
    let cancelled = false
    api
      .accountIOCapabilities()
      .then((value) => {
        if (cancelled) return
        setCaps(value)
        setFormat((current) => {
          if (current === 'encrypted' && !value.encryptedExport) return 'json'
          if (current !== 'encrypted' && !value.plaintextExport) return 'encrypted'
          return current
        })
      })
      .catch(() => {
        if (!cancelled) setCapsError(true)
      })
    return () => {
      cancelled = true
    }
  }, [open])

  const scopeCount =
    scope === 'selected' ? selected.length : scope === 'filtered' ? filtered.length : accounts.length
  const maxAccounts = caps?.maxExportAccounts ?? 1000
  const tooMany = scopeCount > maxAccounts
  const plaintext = format === 'json' || format === 'csv'

  const chooseFormat = (next: ExportFormat) => {
    setFormat(next)
    setIncludeDevice(next === 'encrypted')
    setErrors({})
  }

  const submit = async () => {
    const nextErrors: Record<string, string> = {}
    if (scopeCount === 0) nextErrors.scope = t('accounts.exportEmpty')
    if (tooMany) nextErrors.scope = t('accounts.exportTooMany', { max: maxAccounts })
    if (format === 'encrypted') {
      if (passphrase.length < 12) nextErrors.passphrase = t('accounts.exportPassphraseTooShort')
      else if (utf8Bytes(passphrase) > 1024) nextErrors.passphrase = t('accounts.exportPassphraseTooLong')
      if (confirm !== passphrase) nextErrors.confirm = t('accounts.exportPassphraseMismatch')
    } else if (plaintextConfirm !== 'EXPORT') {
      nextErrors.plaintext = t('accounts.exportPlaintextMismatch')
    }
    setErrors(nextErrors)
    if (Object.keys(nextErrors).length > 0) return

    const request: AccountExportRequest = {
      format,
      ...(scope === 'all'
        ? {}
        : { ids: scope === 'selected' ? selected : filtered.map((account) => account.id) }),
      ...(format === 'encrypted' ? { passphrase } : {}),
      ...(format === 'csv' ? {} : { includeDevice }),
      ...(plaintext ? { confirmPlaintext: 'EXPORT' as const } : {}),
    }
    setBusy(true)
    setError('')
    try {
      const download = await api.exportAccounts(request)
      downloadBlob(download.blob, download.filename)
      push(t('accounts.exportSuccess', { count: download.counts.accounts }), 'success')
      if (download.counts.missingSecrets > 0) {
        push(
          t('accounts.exportMissingSecrets', { count: download.counts.missingSecrets }),
          'default',
        )
      }
      onExported?.()
      onClose()
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : t('accounts.exportFailed')
      setError(message)
      push(message, 'danger')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('accounts.exportTitle')}
      description={t('accounts.exportSubtitle')}
      onClose={onClose}
      size="md"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="primary"
            icon={<Download size={14} />}
            loading={busy}
            disabled={scopeCount === 0 || tooMany}
            onClick={() => void submit()}
          >
            {t('accounts.exportAction')}
          </Button>
        </>
      }
    >
      <div className="form account-io-form">
        <fieldset className="account-io-group">
          <legend className="field__label">{t('accounts.exportScope')}</legend>
          <div className="account-io-choices">
            {(
              [
                {
                  value: 'selected',
                  label: t('accounts.exportScopeSelected', { count: selected.length }),
                  disabled: selected.length === 0,
                },
                {
                  value: 'filtered',
                  label: t('accounts.exportScopeFiltered', { count: filtered.length }),
                  disabled: filtered.length === 0,
                },
                {
                  value: 'all',
                  label: t('accounts.exportScopeAll', { count: accounts.length }),
                  disabled: accounts.length === 0,
                },
              ] as const
            ).map((option) => (
              <label
                key={option.value}
                className={clsx(
                  'account-io-choice',
                  scope === option.value && 'is-active',
                  option.disabled && 'is-disabled',
                )}
              >
                <input
                  type="radio"
                  name={scopeName}
                  value={option.value}
                  checked={scope === option.value}
                  disabled={option.disabled}
                  onChange={() => setScope(option.value)}
                />
                <span>{option.label}</span>
              </label>
            ))}
          </div>
          {errors.scope ? (
            <p className="form-error" role="alert">
              {errors.scope}
            </p>
          ) : null}
        </fieldset>

        <fieldset className="account-io-group">
          <legend className="field__label">{t('accounts.exportFormat')}</legend>
          <div className="account-io-choices">
            {(
              [
                {
                  value: 'encrypted',
                  label: t('accounts.exportFormatEncrypted'),
                  disabled: caps ? !caps.encryptedExport : false,
                },
                {
                  value: 'json',
                  label: t('accounts.exportFormatJson'),
                  disabled: !caps?.plaintextExport,
                },
                {
                  value: 'csv',
                  label: t('accounts.exportFormatCsv'),
                  disabled: !caps?.plaintextExport,
                },
              ] as const
            ).map((option) => (
              <label
                key={option.value}
                className={clsx(
                  'account-io-choice',
                  format === option.value && 'is-active',
                  option.disabled && 'is-disabled',
                )}
              >
                <input
                  type="radio"
                  name={formatName}
                  value={option.value}
                  checked={format === option.value}
                  disabled={option.disabled}
                  onChange={() => chooseFormat(option.value)}
                />
                <span>{option.label}</span>
              </label>
            ))}
          </div>
          {caps && !caps.encryptedExport ? (
            <p className="field__hint">{t('accounts.exportEncryptedMockHint')}</p>
          ) : null}
          {caps && !caps.plaintextExport ? (
            <p className="field__hint">{t('accounts.exportPlaintextDisabled')}</p>
          ) : null}
          {capsError ? (
            <p className="form-error" role="alert">
              {t('accounts.exportCapsFailed')}
            </p>
          ) : null}
        </fieldset>

        {format === 'encrypted' ? (
          <>
            <Field
              label={t('accounts.exportPassphrase')}
              htmlFor="account-export-passphrase"
              hint={t('accounts.exportPassphraseHint')}
              error={errors.passphrase}
            >
              <div className="account-io-secret">
                <Input
                  id="account-export-passphrase"
                  type={showPassphrase ? 'text' : 'password'}
                  value={passphrase}
                  onChange={(event) => setPassphrase(event.target.value)}
                  autoComplete="new-password"
                  spellCheck={false}
                />
                <IconButton
                  label={t(
                    showPassphrase
                      ? 'accounts.exportPassphraseHide'
                      : 'accounts.exportPassphraseShow',
                  )}
                  icon={showPassphrase ? <EyeOff size={14} /> : <Eye size={14} />}
                  onClick={() => setShowPassphrase((current) => !current)}
                />
              </div>
            </Field>
            <Field
              label={t('accounts.exportPassphraseConfirm')}
              htmlFor="account-export-passphrase-confirm"
              error={errors.confirm}
            >
              <Input
                id="account-export-passphrase-confirm"
                type="password"
                value={confirm}
                onChange={(event) => setConfirm(event.target.value)}
                autoComplete="new-password"
                spellCheck={false}
              />
            </Field>
          </>
        ) : null}

        {plaintext ? (
          <>
            <div className="callout callout--warning" role="note">
              <p className="callout__text">{t('accounts.exportPlaintextWarning')}</p>
            </div>
            <Field
              label={t('accounts.exportPlaintextConfirm')}
              htmlFor="account-export-plaintext-confirm"
              hint={t('accounts.exportPlaintextHint')}
              error={errors.plaintext}
            >
              <Input
                id="account-export-plaintext-confirm"
                value={plaintextConfirm}
                onChange={(event) => setPlaintextConfirm(event.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
            </Field>
          </>
        ) : null}

        {format !== 'csv' ? (
          <Checkbox
            label={t('accounts.exportIncludeDevice')}
            checked={includeDevice}
            onChange={(event) => setIncludeDevice(event.target.checked)}
          />
        ) : null}

        <p className="form-note">{t('accounts.exportSummary', { count: scopeCount })}</p>
        {error ? (
          <p className="form-error" role="alert">
            {error}
          </p>
        ) : null}
      </div>
    </Modal>
  )
}
