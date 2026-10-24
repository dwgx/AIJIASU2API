import { useId, useState } from 'react'
import { Download, FileSearch, Lock, Upload } from 'lucide-react'
import { api } from '../../lib/api'
import { downloadBlob } from '../accounts/AccountExportDialog'
import { formatBytes, formatDateTimeSeconds } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type {
  BackupInspectView,
  BackupManifestEntry,
  BackupRestoreView,
} from '../../lib/types'
import { useToast } from '../Toast'
import {
  Badge,
  Button,
  Card,
  Field,
  Input,
  Textarea,
} from '../primitives'
import './BackupPanel.css'

const MIN_PASSPHRASE = 12

function errorText(cause: unknown, fallback: string): string {
  return cause instanceof Error ? cause.message : fallback
}

/**
 * Backup console: create a sealed envelope, inspect a pasted one, and dry-run a
 * restore. The apply path is deliberately unreachable here — it needs the
 * daemon's AJS_BACKUP_ALLOW_RESTORE and no LIVE node, so the button explains
 * that instead of offering a one-click overwrite.
 */
export function BackupPanel() {
  const { t } = useI18n()
  const { push } = useToast()
  const baseId = useId()
  const [passphrase, setPassphrase] = useState('')
  const [confirmPassphrase, setConfirmPassphrase] = useState('')
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [created, setCreated] = useState<{
    filename: string
    entries: BackupManifestEntry[]
    bytes: number
  } | null>(null)

  const [envelopeText, setEnvelopeText] = useState('')
  const [envelopeName, setEnvelopeName] = useState<string | null>(null)
  const [envelopePassphrase, setEnvelopePassphrase] = useState('')
  const [inspecting, setInspecting] = useState(false)
  const [inspectError, setInspectError] = useState<string | null>(null)
  const [inspectResult, setInspectResult] = useState<BackupInspectView | null>(null)
  const [restoring, setRestoring] = useState(false)
  const [restoreError, setRestoreError] = useState<string | null>(null)
  const [restoreResult, setRestoreResult] = useState<BackupRestoreView | null>(null)

  const create = async () => {
    if (passphrase.length < MIN_PASSPHRASE) {
      setCreateError(t('backup.create.tooShort'))
      return
    }
    if (passphrase !== confirmPassphrase) {
      setCreateError(t('backup.create.mismatch'))
      return
    }
    setCreating(true)
    setCreateError(null)
    setCreated(null)
    try {
      const download = await api.createBackup(passphrase)
      downloadBlob(download.blob, download.filename)
      // The create response is a file, not JSON: read the envelope back to
      // list the blobs the operator just downloaded. The manifest lives inside
      // the ciphertext, so this is one extra inspect round-trip by design.
      let entries: BackupManifestEntry[] = []
      let bytes = 0
      try {
        const text = await download.blob.text()
        const inspected = await api.inspectBackup(text, passphrase)
        entries = inspected.manifest?.entries ?? []
        bytes = inspected.envelopeBytes
      } catch {
        entries = []
      }
      setCreated({ filename: download.filename, entries, bytes })
      setPassphrase('')
      setConfirmPassphrase('')
      push(t('backup.create.done'), 'success')
    } catch (cause) {
      setCreateError(errorText(cause, t('backup.create.failed')))
    } finally {
      setCreating(false)
    }
  }

  const readFile = async (file: File) => {
    setEnvelopeText(await file.text())
    setEnvelopeName(file.name)
    setInspectResult(null)
    setRestoreResult(null)
    setInspectError(null)
    setRestoreError(null)
  }

  const inspect = async () => {
    if (!envelopeText.trim()) {
      setInspectError(t('backup.inspect.emptyEnvelope'))
      return
    }
    setInspecting(true)
    setInspectError(null)
    setRestoreError(null)
    setRestoreResult(null)
    try {
      setInspectResult(await api.inspectBackup(envelopeText, envelopePassphrase))
    } catch (cause) {
      setInspectResult(null)
      setInspectError(errorText(cause, t('backup.inspect.failed')))
    } finally {
      setInspecting(false)
    }
  }

  const dryRun = async () => {
    if (!envelopeText.trim() || !envelopePassphrase) {
      setRestoreError(t('backup.restore.needInput'))
      return
    }
    setRestoring(true)
    setRestoreError(null)
    setRestoreResult(null)
    try {
      setRestoreResult(
        await api.restoreBackup({
          envelope: envelopeText,
          passphrase: envelopePassphrase,
          dryRun: true,
        }),
      )
    } catch (cause) {
      setRestoreError(errorText(cause, t('backup.restore.failed')))
    } finally {
      setRestoring(false)
    }
  }

  const applyNoteId = `${baseId}-apply-note`
  const diff = restoreResult?.diff

  return (
    <div className="stack-lg">
      <Card title={t('backup.create.title')} subtitle={t('backup.create.subtitle')}>
        <div className="form backup-form">
          <div className="form-grid">
            <Field
              label={t('backup.create.passphrase')}
              htmlFor={`${baseId}-passphrase`}
              hint={t('backup.create.passphraseHint')}
            >
              <Input
                id={`${baseId}-passphrase`}
                type="password"
                value={passphrase}
                autoComplete="new-password"
                onChange={(event) => setPassphrase(event.target.value)}
              />
            </Field>
            <Field
              label={t('backup.create.confirm')}
              htmlFor={`${baseId}-confirm`}
            >
              <Input
                id={`${baseId}-confirm`}
                type="password"
                value={confirmPassphrase}
                autoComplete="new-password"
                onChange={(event) => setConfirmPassphrase(event.target.value)}
              />
            </Field>
          </div>
          {createError ? (
            <p className="form-error" role="alert">
              {createError}
            </p>
          ) : null}
          <div className="backup-actions">
            <Button
              variant="primary"
              icon={<Download size={14} />}
              onClick={() => void create()}
              loading={creating}
              disabled={!passphrase || !confirmPassphrase}
            >
              {t('backup.create.action')}
            </Button>
          </div>
        </div>

        {created ? (
          <div className="backup-result">
            <p className="backup-result__line">
              <Lock size={13} aria-hidden="true" />
              <span className="mono">{created.filename}</span>
              {created.bytes > 0 ? (
                <span className="backup-result__meta">{formatBytes(created.bytes)}</span>
              ) : null}
            </p>
            {created.entries.length > 0 ? (
              <>
                <h3 className="backup-subhead">{t('backup.create.blobsTitle')}</h3>
                <ManifestTable entries={created.entries} />
              </>
            ) : (
              <p className="setting-note">{t('backup.create.noManifest')}</p>
            )}
          </div>
        ) : null}
      </Card>

      <Card title={t('backup.inspect.title')} subtitle={t('backup.inspect.subtitle')}>
        <div className="form backup-form">
          <Field
            label={t('backup.inspect.envelope')}
            htmlFor={`${baseId}-envelope`}
            hint={t('backup.inspect.envelopeHint')}
          >
            <Textarea
              id={`${baseId}-envelope`}
              rows={7}
              className="mono"
              value={envelopeText}
              placeholder={t('backup.inspect.pastePlaceholder')}
              spellCheck={false}
              onChange={(event) => {
                setEnvelopeText(event.target.value)
                setEnvelopeName(null)
                setInspectResult(null)
                setRestoreResult(null)
              }}
            />
          </Field>
          <div className="backup-file-row">
            <label className="btn btn--secondary btn--sm backup-file">
              <Upload size={14} aria-hidden="true" />
              <span>{t('backup.inspect.chooseFile')}</span>
              <input
                type="file"
                accept=".ajsb,application/json"
                className="sr-only"
                onChange={(event) => {
                  const file = event.target.files?.[0]
                  if (file) void readFile(file)
                }}
              />
            </label>
            {envelopeName ? (
              <span className="backup-file__name mono">
                {t('backup.inspect.fileLoaded', { name: envelopeName })}
              </span>
            ) : null}
          </div>
          <Field
            label={t('backup.inspect.passphrase')}
            htmlFor={`${baseId}-envelope-passphrase`}
            hint={t('backup.inspect.passphraseHint')}
          >
            <Input
              id={`${baseId}-envelope-passphrase`}
              type="password"
              value={envelopePassphrase}
              autoComplete="off"
              onChange={(event) => setEnvelopePassphrase(event.target.value)}
            />
          </Field>
          {inspectError ? (
            <p className="form-error" role="alert">
              {inspectError}
            </p>
          ) : null}
          <div className="backup-actions">
            <Button
              variant="secondary"
              icon={<FileSearch size={14} />}
              onClick={() => void inspect()}
              loading={inspecting}
              disabled={!envelopeText.trim()}
            >
              {t('backup.inspect.action')}
            </Button>
          </div>
        </div>

        {inspectResult ? (
          <div className="backup-result">
            <dl className="facts facts--compact">
              <div>
                <dt>{t('backup.inspect.createdAt')}</dt>
                <dd>{formatDateTimeSeconds(inspectResult.createdAt)}</dd>
              </div>
              <div>
                <dt>{t('backup.inspect.app')}</dt>
                <dd className="mono">{inspectResult.app}</dd>
              </div>
              <div>
                <dt>{t('backup.inspect.version')}</dt>
                <dd>
                  <span className="mono">{inspectResult.version}</span>{' '}
                  <Badge tone={inspectResult.supported ? 'success' : 'warning'}>
                    {inspectResult.supported
                      ? t('backup.inspect.supported')
                      : t('backup.inspect.unsupported')}
                  </Badge>
                </dd>
              </div>
              <div>
                <dt>{t('backup.inspect.bytes')}</dt>
                <dd className="mono">{formatBytes(inspectResult.envelopeBytes)}</dd>
              </div>
            </dl>
            {inspectResult.manifest ? (
              <>
                <h3 className="backup-subhead">{t('backup.inspect.manifest')}</h3>
                <ManifestTable entries={inspectResult.manifest.entries} />
              </>
            ) : (
              <p className="setting-note">{t('backup.inspect.manifestHint')}</p>
            )}
          </div>
        ) : null}
      </Card>

      <Card title={t('backup.restore.title')} subtitle={t('backup.restore.subtitle')}>
        <div className="backup-actions">
          <Button
            variant="secondary"
            icon={<FileSearch size={14} />}
            onClick={() => void dryRun()}
            loading={restoring}
            disabled={!envelopeText.trim() || !envelopePassphrase}
          >
            {t('backup.restore.action')}
          </Button>
          <Button
            variant="danger"
            disabled
            aria-describedby={applyNoteId}
            title={t('backup.restore.applyBlocked')}
          >
            {t('backup.restore.apply')}
          </Button>
        </div>
        <p className="setting-note" id={applyNoteId}>
          <Lock size={12} aria-hidden="true" /> {t('backup.restore.applyBlocked')}
        </p>

        {restoreError ? (
          <p className="form-error" role="alert">
            {restoreError}
          </p>
        ) : null}

        {restoreResult && diff ? (
          <div className="backup-result">
            <p className="backup-result__line" role="status">
              {t('backup.restore.result', {
                added: diff.added,
                removed: diff.removed,
                changed: diff.changed,
                same: diff.same,
              })}
            </p>
            {restoreResult.warning ? (
              <p className="callout callout--warning">
                <Lock size={14} aria-hidden="true" />{' '}
                <strong>{t('backup.restore.warning')}:</strong> {restoreResult.warning}
              </p>
            ) : null}
            <div className="table-wrap">
              <table className="data-table backup-diff">
                <caption>{t('backup.restore.diffCaption')}</caption>
                <thead>
                  <tr>
                    <th scope="col">{t('backup.restore.category')}</th>
                    <th scope="col">{t('backup.restore.fileCount')}</th>
                    <th scope="col">{t('backup.blob.size')}</th>
                    <th scope="col">{t('backup.restore.names')}</th>
                  </tr>
                </thead>
                <tbody>
                  {[
                  {
                    key: 'added',
                    label: t('backup.restore.added'),
                    count: diff.added,
                    bytes: diff.addedBytes,
                    names: diff.addedNames,
                  },
                  {
                    key: 'removed',
                    label: t('backup.restore.removed'),
                    count: diff.removed,
                    bytes: diff.removedBytes,
                    names: diff.removedNames,
                  },
                  {
                    key: 'changed',
                    label: t('backup.restore.changed'),
                    count: diff.changed,
                    bytes: diff.changedBytes,
                    names: diff.changedNames,
                  },
                  {
                    key: 'same',
                    label: t('backup.restore.same'),
                    count: diff.same,
                    bytes: diff.sameBytes,
                    names: diff.sameNames,
                  },
                  ].map((row) => (
                    <tr key={row.key}>
                      <th scope="row">{row.label}</th>
                      <td className="mono">{row.count}</td>
                      <td className="mono">{formatBytes(row.bytes)}</td>
                      <td className="mono backup-diff__names">
                        {row.names.length > 0 ? row.names.join(', ') : '—'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        ) : null}
      </Card>
    </div>
  )
}

function ManifestTable({ entries }: { entries: BackupManifestEntry[] }) {
  const { t } = useI18n()
  return (
    <div className="table-wrap">
      <table className="data-table backup-table">
        <caption className="sr-only">{t('backup.inspect.manifest')}</caption>
        <thead>
          <tr>
            <th scope="col">{t('backup.blob.name')}</th>
            <th scope="col">{t('backup.blob.size')}</th>
            <th scope="col">{t('backup.blob.required')}</th>
            <th scope="col">{t('backup.blob.digest')}</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((entry) => (
            <tr key={entry.name}>
              <th scope="row" className="mono">
                {entry.name}
              </th>
              <td className="mono">{formatBytes(entry.size)}</td>
              <td>
                <Badge tone={entry.required ? 'info' : 'neutral'}>
                  {entry.required
                    ? t('backup.blob.requiredYes')
                    : t('backup.blob.requiredNo')}
                </Badge>
              </td>
              <td className="mono backup-table__digest" title={entry.sha256}>
                {entry.sha256.slice(0, 12)}…
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
