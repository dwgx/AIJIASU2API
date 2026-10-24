import { Upload } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import type { DragEvent } from 'react'
import { api, isMockMode } from '../../lib/api'
import { formatBytes } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type {
  AccountImportResult,
  AccountImportRowResult,
  AccountIOWarning,
  ImportConflict,
  ImportDevicePolicy,
  ImportIdPolicy,
  ImportInitialState,
  ImportMatchBy,
  ImportMode,
  ImportSourceKind,
} from '../../lib/types'
import { ConfirmDialog, Modal } from '../Overlay'
import { Button, Checkbox, Field, Input, Select } from '../primitives'
import { useToast } from '../Toast'
import './accounts-io.css'

const MAX_IMPORT_BYTES = 4 * 1024 * 1024

interface AccountImportDialogProps {
  open: boolean
  onClose: () => void
  onImported: () => void
}

interface ImportFormOptions {
  mode: ImportMode
  matchBy: ImportMatchBy
  onConflict: ImportConflict
  initialState: ImportInitialState
  idPolicy: ImportIdPolicy
  devicePolicy: ImportDevicePolicy
  preserveTimestamps: boolean
}

const DEFAULT_OPTIONS: ImportFormOptions = {
  mode: 'atomic',
  matchBy: 'label',
  onConflict: 'skip',
  initialState: 'paused',
  idPolicy: 'new',
  devicePolicy: 'restore_if_missing',
  preserveTimestamps: true,
}

type ImportStep = 'source' | 'preview' | 'result'

interface SelectedFile {
  name: string
  size: number
  kind: ImportSourceKind
  text: string
}

const WARNING_KEYS: Record<AccountIOWarning, string> = {
  password_kept: 'accounts.warnPasswordKept',
  password_unavailable: 'accounts.warnPasswordUnavailable',
  device_kept: 'accounts.warnDeviceKept',
  device_restored: 'accounts.warnDeviceRestored',
  device_generated_on_login: 'accounts.warnDeviceGenerated',
  device_replaced: 'accounts.warnDeviceReplaced',
  id_ignored: 'accounts.warnIdIgnored',
  platform_device_mismatch: 'accounts.warnPlatformDeviceMismatch',
  counts_mismatch: 'accounts.warnCountsMismatch',
  unknown_column: 'accounts.warnUnknownColumn',
  secret_orphaned: 'accounts.warnSecretOrphaned',
}

function kindForFile(name: string): ImportSourceKind | null {
  const lower = name.toLowerCase()
  if (lower.endsWith('.ajsx')) return 'encrypted'
  if (lower.endsWith('.json')) return 'json'
  if (lower.endsWith('.csv')) return 'csv'
  return null
}

/** `Blob.text` is missing in older jsdom; FileReader is equivalent and universal. */
function readFileText(file: File): Promise<string> {
  if (typeof file.text === 'function') return file.text()
  const { promise, resolve, reject } = Promise.withResolvers<string>()
  const reader = new FileReader()
  reader.onload = () => resolve(String(reader.result ?? ''))
  reader.onerror = () => reject(reader.error)
  reader.readAsText(file)
  return promise
}

export function AccountImportDialog({ open, onClose, onImported }: AccountImportDialogProps) {
  const { t } = useI18n()
  const { push } = useToast()
  const fileInputId = useId()
  const [step, setStep] = useState<ImportStep>('source')
  const [file, setFile] = useState<SelectedFile | null>(null)
  const [content, setContent] = useState<unknown>(null)
  const [passphrase, setPassphrase] = useState('')
  const [passphraseError, setPassphraseError] = useState('')
  const [options, setOptions] = useState<ImportFormOptions>(DEFAULT_OPTIONS)
  const [preview, setPreview] = useState<AccountImportResult | null>(null)
  const [result, setResult] = useState<AccountImportResult | null>(null)
  const [error, setError] = useState('')
  const [previewing, setPreviewing] = useState(false)
  const [importing, setImporting] = useState(false)
  const [partialOpen, setPartialOpen] = useState(false)
  const [dragging, setDragging] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    setStep('source')
    setFile(null)
    setContent(null)
    setPassphrase('')
    setPassphraseError('')
    setOptions(DEFAULT_OPTIONS)
    setPreview(null)
    setResult(null)
    setError('')
    setPreviewing(false)
    setImporting(false)
    setPartialOpen(false)
    setDragging(false)
  }, [open])

  const invalidatePreview = () => {
    setPreview(null)
    setContent(null)
    setResult(null)
    setStep('source')
    setError('')
  }

  const backToSource = () => {
    setPreview(null)
    setContent(null)
    setStep('source')
    setError('')
  }

  const updateOption = <K extends keyof ImportFormOptions>(
    key: K,
    value: ImportFormOptions[K],
  ) => {
    setOptions((current) => ({ ...current, [key]: value }))
    invalidatePreview()
  }

  const acceptFile = async (next: File) => {
    setError('')
    setPassphraseError('')
    if (next.size > MAX_IMPORT_BYTES) {
      setError(t('accounts.importFileTooLarge'))
      return
    }
    const kind = kindForFile(next.name)
    if (!kind) {
      setError(t('accounts.importUnsupported'))
      return
    }
    if (kind === 'encrypted' && isMockMode()) {
      setError(t('accounts.importEncryptedMockHint'))
      return
    }
    let text: string
    try {
      text = await readFileText(next)
    } catch {
      setError(t('accounts.importReadError'))
      return
    }
    setFile({ name: next.name, size: next.size, kind, text })
    setContent(null)
    setPassphrase('')
    setPreview(null)
    setResult(null)
    setStep('source')
  }

  const handleDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault()
    setDragging(false)
    const dropped = event.dataTransfer.files?.[0]
    if (dropped) void acceptFile(dropped)
  }

  const runPreview = async () => {
    if (!file) return
    if (file.kind === 'encrypted') {
      if (!passphrase) {
        setPassphraseError(t('accounts.importPassphraseRequired'))
        return
      }
      if (passphrase.length < 12) {
        setPassphraseError(t('accounts.importPassphraseTooShort'))
        return
      }
    }
    let parsed: unknown = file.text
    if (file.kind === 'json' || file.kind === 'encrypted') {
      try {
        parsed = JSON.parse(file.text)
      } catch {
        setError(t('accounts.importParseError'))
        return
      }
    }
    setPassphraseError('')
    setError('')
    setPreviewing(true)
    try {
      const next = await api.importAccounts({
        source: {
          kind: file.kind,
          content: parsed,
          ...(file.kind === 'encrypted' ? { passphrase } : {}),
        },
        options: { ...options, dryRun: true },
      })
      setContent(parsed)
      setPreview(next)
      setStep('preview')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('accounts.importFailed'))
    } finally {
      setPreviewing(false)
    }
  }

  const runApply = async () => {
    if (!file || !preview) return
    setError('')
    setImporting(true)
    try {
      const applied = await api.importAccounts({
        source: {
          kind: file.kind,
          content,
          ...(file.kind === 'encrypted' ? { passphrase } : {}),
        },
        options: { ...options, dryRun: false },
      })
      setResult(applied)
      setPreview(null)
      setPassphrase('')
      setFile(null)
      setContent(null)
      setStep('result')
      onImported()
      if (applied.applied) {
        push(
          t('accounts.importSuccess', {
            count: applied.counts.created + applied.counts.updated,
          }),
          'success',
        )
      } else {
        push(t('accounts.importNoChanges'), 'default')
      }
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : t('accounts.importFailed')
      setError(message)
      push(message, 'danger')
    } finally {
      setImporting(false)
    }
  }

  const requestApply = () => {
    if (!preview) return
    if (options.mode === 'partial') {
      setPartialOpen(true)
      return
    }
    void runApply()
  }

  const reset = () => {
    setStep('source')
    setFile(null)
    setContent(null)
    setPassphrase('')
    setPassphraseError('')
    setOptions(DEFAULT_OPTIONS)
    setPreview(null)
    setResult(null)
    setError('')
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  const close = () => {
    reset()
    onClose()
  }

  const importable = preview ? preview.counts.created + preview.counts.updated : 0
  const atomicFailure = preview ? preview.mode === 'atomic' && preview.counts.failed > 0 : false
  const applyDisabled = !preview || importable === 0 || atomicFailure || importing

  const warningsText = (row: AccountImportRowResult) => {
    const notes = (row.warnings ?? []).map((code) => t(WARNING_KEYS[code] ?? 'accounts.warnUnknown'))
    if (row.code) notes.push(row.message ? `${row.code}: ${row.message}` : row.code)
    return notes.join(' · ')
  }

  const resultTable = (data: AccountImportResult, caption: string) => (
    <div className="account-io-table-wrap">
      <table className="account-io-table" aria-label={caption}>
        <caption className="account-io-sr-only">{caption}</caption>
        <thead>
          <tr>
            <th scope="col">{t('accounts.importRow')}</th>
            <th scope="col">{t('accounts.label')}</th>
            <th scope="col">{t('accounts.importAction')}</th>
            <th scope="col">{t('accounts.importExisting')}</th>
            <th scope="col">{t('accounts.importId')}</th>
            <th scope="col">{t('accounts.importWarnings')}</th>
          </tr>
        </thead>
        <tbody>
          {data.results.map((row) => (
            <tr key={`${row.row}-${row.action}`}>
              <td className="mono">{row.row}</td>
              <td>{row.renamedTo ?? row.label}</td>
              <td>{t(`accounts.importAction${row.action[0].toUpperCase()}${row.action.slice(1)}`)}</td>
              <td className="mono">{row.existingId ?? '—'}</td>
              <td className="mono">{row.id ?? '—'}</td>
              <td>{warningsText(row)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )

  const footer =
    step === 'source' ? (
      <>
        <Button variant="ghost" onClick={close} disabled={previewing}>
          {t('common.cancel')}
        </Button>
        <Button
          variant="primary"
          loading={previewing}
          disabled={!file || previewing}
          onClick={() => void runPreview()}
        >
          {t('accounts.importPreview')}
        </Button>
      </>
    ) : step === 'preview' ? (
      <>
        <Button variant="ghost" onClick={backToSource} disabled={importing}>
          {t('accounts.importBack')}
        </Button>
        <Button
          variant="primary"
          loading={importing}
          disabled={applyDisabled}
          onClick={requestApply}
        >
          {t('accounts.importApply', { count: importable })}
        </Button>
      </>
    ) : (
      <>
        <Button variant="ghost" onClick={close}>
          {t('common.close')}
        </Button>
        <Button variant="primary" onClick={reset}>
          {t('accounts.importAnother')}
        </Button>
      </>
    )

  return (
    <>
      <Modal
        open={open}
        title={t('accounts.importTitle')}
        description={t('accounts.importSubtitle')}
        onClose={close}
        size="md"
        footer={footer}
      >
        <div className="form account-io-form">
          {step === 'source' ? (
            <>
              <div
                className={`account-io-drop${dragging ? ' is-over' : ''}`}
                onDragOver={(event) => {
                  event.preventDefault()
                  setDragging(true)
                }}
                onDragLeave={() => setDragging(false)}
                onDrop={handleDrop}
                aria-busy={previewing}
              >
                <Upload size={20} aria-hidden="true" />
                <p className="account-io-drop__hint">{t('accounts.importDropHint')}</p>
                <label className="btn btn--secondary btn--sm" htmlFor={fileInputId}>
                  {t('accounts.importChooseFile')}
                </label>
                <input
                  ref={fileInputRef}
                  id={fileInputId}
                  className="account-io-file"
                  type="file"
                  accept=".ajsx,.json,.csv"
                  aria-label={t('accounts.importFile')}
                  onChange={(event) => {
                    const picked = event.target.files?.[0]
                    if (picked) void acceptFile(picked)
                  }}
                />
                <p className="field__hint">{t('accounts.importFileHint')}</p>
                {file ? (
                  <p className="account-io-file-meta" data-testid="import-file-meta">
                    {file.name} · {formatBytes(file.size)}
                  </p>
                ) : null}
              </div>

              {file?.kind === 'encrypted' ? (
                <Field
                  label={t('accounts.importPassphrase')}
                  htmlFor="account-import-passphrase"
                  hint={t('accounts.importPassphraseHint')}
                  error={passphraseError}
                >
                  <Input
                    id="account-import-passphrase"
                    type="password"
                    value={passphrase}
                    onChange={(event) => {
                      setPassphrase(event.target.value)
                      invalidatePreview()
                    }}
                    autoComplete="new-password"
                    spellCheck={false}
                  />
                </Field>
              ) : null}

              <details className="account-io-advanced">
                <summary>{t('accounts.importAdvanced')}</summary>
                <div className="account-io-options">
                  <Field label={t('accounts.importMode')} htmlFor="account-import-mode">
                    <Select
                      id="account-import-mode"
                      variant="form"
                      value={options.mode}
                      onChange={(value) => updateOption('mode', value as ImportMode)}
                      options={[
                        { value: 'atomic', label: t('accounts.importModeAtomic') },
                        { value: 'partial', label: t('accounts.importModePartial') },
                      ]}
                    />
                  </Field>
                  <Field label={t('accounts.importMatchBy')} htmlFor="account-import-match">
                    <Select
                      id="account-import-match"
                      variant="form"
                      value={options.matchBy}
                      onChange={(value) => updateOption('matchBy', value as ImportMatchBy)}
                      options={[
                        { value: 'label', label: t('accounts.importMatchLabel') },
                        {
                          value: 'username_platform',
                          label: t('accounts.importMatchUsernamePlatform'),
                        },
                        { value: 'id', label: t('accounts.importMatchId') },
                      ]}
                    />
                  </Field>
                  <Field label={t('accounts.importOnConflict')} htmlFor="account-import-conflict">
                    <Select
                      id="account-import-conflict"
                      variant="form"
                      value={options.onConflict}
                      onChange={(value) => updateOption('onConflict', value as ImportConflict)}
                      options={[
                        { value: 'skip', label: t('accounts.importConflictSkip') },
                        { value: 'update', label: t('accounts.importConflictUpdate') },
                        { value: 'rename', label: t('accounts.importConflictRename') },
                        { value: 'fail', label: t('accounts.importConflictFail') },
                      ]}
                    />
                  </Field>
                  <Field label={t('accounts.importInitialState')} htmlFor="account-import-state">
                    <Select
                      id="account-import-state"
                      variant="form"
                      value={options.initialState}
                      onChange={(value) =>
                        updateOption('initialState', value as ImportInitialState)
                      }
                      options={[
                        { value: 'paused', label: t('accounts.importStatePaused') },
                        { value: 'active', label: t('accounts.importStateActive') },
                        { value: 'from_file', label: t('accounts.importStateFromFile') },
                      ]}
                    />
                  </Field>
                  <Field label={t('accounts.importIdPolicy')} htmlFor="account-import-id">
                    <Select
                      id="account-import-id"
                      variant="form"
                      value={options.idPolicy}
                      onChange={(value) => updateOption('idPolicy', value as ImportIdPolicy)}
                      options={[
                        { value: 'new', label: t('accounts.importIdNew') },
                        { value: 'preserve', label: t('accounts.importIdPreserve') },
                      ]}
                    />
                  </Field>
                  <Field label={t('accounts.importDevicePolicy')} htmlFor="account-import-device">
                    <Select
                      id="account-import-device"
                      variant="form"
                      value={options.devicePolicy}
                      onChange={(value) =>
                        updateOption('devicePolicy', value as ImportDevicePolicy)
                      }
                      options={[
                        { value: 'restore_if_missing', label: t('accounts.importDeviceRestore') },
                        { value: 'keep', label: t('accounts.importDeviceKeep') },
                        { value: 'replace', label: t('accounts.importDeviceReplace') },
                      ]}
                    />
                  </Field>
                </div>
                <Checkbox
                  label={t('accounts.importPreserveTimestamps')}
                  checked={options.preserveTimestamps}
                  onChange={(event) =>
                    updateOption('preserveTimestamps', event.target.checked)
                  }
                />
              </details>

              {error ? (
                <p className="form-error" role="alert">
                  {error}
                </p>
              ) : null}
              <p className="form-note">{t('accounts.importFootnote')}</p>
            </>
          ) : null}

          {step === 'preview' && preview ? (
            <>
              <p className="account-io-counts">
                {t('accounts.importCounts', {
                  total: preview.counts.total,
                  created: preview.counts.created,
                  updated: preview.counts.updated,
                  skipped: preview.counts.skipped,
                  failed: preview.counts.failed,
                })}
              </p>
              {resultTable(preview, t('accounts.importPreviewTitle'))}
              {(preview.warnings ?? []).length > 0 ? (
                <p className="form-note">
                  {(preview.warnings ?? [])
                    .map((code) => t(WARNING_KEYS[code] ?? 'accounts.warnUnknown'))
                    .join(' · ')}
                </p>
              ) : null}
              {atomicFailure ? (
                <p className="form-error" role="alert">
                  {t('accounts.importAtomicFailed')}
                </p>
              ) : null}
              {options.mode === 'partial' ? (
                <p className="form-note">{t('accounts.importPartialWarning')}</p>
              ) : null}
              {error ? (
                <p className="form-error" role="alert">
                  {error}
                </p>
              ) : null}
            </>
          ) : null}

          {step === 'result' && result ? (
            <>
              <p className="account-io-counts">
                {t('accounts.importAppliedCounts', {
                  created: result.counts.created,
                  updated: result.counts.updated,
                  skipped: result.counts.skipped,
                  failed: result.counts.failed,
                })}
              </p>
              {resultTable(result, t('accounts.importResultTitle'))}
              {result.counts.failed > 0 ? (
                <p className="form-error" role="alert">
                  {t('accounts.importSomeFailed')}
                </p>
              ) : null}
            </>
          ) : null}
        </div>
      </Modal>

      <ConfirmDialog
        open={partialOpen}
        title={t('accounts.importPartialConfirmTitle')}
        description={t('accounts.importPartialConfirmDesc')}
        confirmLabel={t('accounts.importPartialConfirmAction')}
        tone="primary"
        busy={importing}
        onConfirm={() => {
          setPartialOpen(false)
          void runApply()
        }}
        onCancel={() => setPartialOpen(false)}
      />
    </>
  )
}
