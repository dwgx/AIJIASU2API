import { useEffect, useId, useState } from 'react'
import type { ReactNode } from 'react'
import { api } from '../../lib/api'
import { formatDateTimeSeconds } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type { DevicePoolRecord } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { Drawer } from '../Overlay'
import {
  Badge,
  Button,
  ErrorState,
  Field,
  Input,
  Skeleton,
  Textarea,
} from '../primitives'
import { useToast } from '../Toast'
import { deviceErrorText, deviceLabel, deviceStateTone } from './shared'

interface DeviceDetailDrawerProps {
  open: boolean
  deviceId: string | null
  onClose: () => void
  onChanged: () => void
}

function MetaRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="device-pool__meta-row">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}

/**
 * Read-only detail plus label/notes editing. The daemon never returns raw
 * identity material, so every value here is a hash, a public claim or a
 * timestamp.
 */
export function DeviceDetailDrawer({ open, deviceId, onClose, onChanged }: DeviceDetailDrawerProps) {
  const { t } = useI18n()
  const { push } = useToast()
  const labelId = useId()
  const notesId = useId()
  const detail = useAsync(
    () => (open && deviceId ? api.getDevicePoolRecord(deviceId) : Promise.resolve(null)),
    [open, deviceId],
  )
  const [label, setLabel] = useState('')
  const [notes, setNotes] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  useEffect(() => {
    if (!detail.data || detail.data.id !== deviceId) return
    setLabel(detail.data.label)
    setNotes(detail.data.notes)
    setSaveError('')
  }, [detail.data, deviceId])

  // useAsync keeps the previous payload across deps changes; never show a
  // record that belongs to another id while the new one loads.
  const record: DevicePoolRecord | null =
    detail.data && detail.data.id === deviceId ? detail.data : null

  const save = async () => {
    if (!deviceId) return
    setSaving(true)
    setSaveError('')
    try {
      await api.patchDevicePoolRecord(deviceId, {
        label: label.trim(),
        notes: notes.trim(),
      })
      push(t('devicePool.detail.saved'), 'success')
      detail.reload()
      onChanged()
    } catch (cause) {
      setSaveError(deviceErrorText(t, cause, t('devicePool.detail.saveFailed')))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Drawer
      open={open}
      title={record ? deviceLabel(record) : (deviceId ?? t('devicePool.title'))}
      subtitle={t('devicePool.detail.subtitle')}
      onClose={onClose}
      width={560}
      footer={
        record ? (
          <Button variant="primary" onClick={() => void save()} loading={saving}>
            {t('devicePool.detail.save')}
          </Button>
        ) : undefined
      }
    >
      {detail.status === 'error' ? (
        <ErrorState
          message={detail.error ?? t('devicePool.detail.loadFailed')}
          onRetry={detail.reload}
        />
      ) : !record ? (
        <div className="stack">
          <Skeleton width={160} height={18} />
          <Skeleton height={120} />
          <Skeleton height={120} />
        </div>
      ) : (
        <div className="stack-lg device-pool__detail">
          <div className="device-pool__detail-head">
            <Badge tone={deviceStateTone(record.state)}>
              {t(`devicePool.state.${record.state}`)}
            </Badge>
            <span className="mono">{record.id}</span>
            {!record.materialPresent ? (
              <Badge tone="danger">{t('devicePool.detail.materialMissing')}</Badge>
            ) : null}
          </div>

          <dl className="device-pool__meta">
            <MetaRow label={t('devicePool.columnProfile')}>
              <span>{record.platform}</span>
              <span className="mono cell-sub">{record.profile}</span>
            </MetaRow>
            <MetaRow label={t('devicePool.generate.algorithm')}>
              <span className="mono">{record.algorithm}</span>
            </MetaRow>
            <MetaRow label={t('devicePool.detail.account')}>
              {record.accountId ? (
                <>
                  <span>{record.accountLabel || record.accountId}</span>
                  <span className="mono cell-sub">{record.accountId}</span>
                </>
              ) : (
                <span>{t('devicePool.unbound')}</span>
              )}
            </MetaRow>
            <MetaRow label={t('devicePool.detail.fingerprint')}>
              <span className="mono device-pool__wrap">{record.fingerprint}</span>
            </MetaRow>
            <MetaRow label={t('devicePool.detail.material')}>
              <span>
                {record.materialPresent
                  ? t('devicePool.detail.materialPresent')
                  : t('devicePool.detail.materialMissing')}
              </span>
            </MetaRow>
          </dl>

          <div className="form">
            <Field label={t('devicePool.detail.label')} htmlFor={labelId} hint={t('devicePool.detail.labelHint')}>
              <Input
                id={labelId}
                value={label}
                maxLength={80}
                onChange={(event) => setLabel(event.target.value)}
              />
            </Field>
            <Field label={t('devicePool.detail.notes')} htmlFor={notesId} hint={t('devicePool.detail.notesHint')}>
              <Textarea
                id={notesId}
                rows={3}
                value={notes}
                maxLength={500}
                onChange={(event) => setNotes(event.target.value)}
              />
            </Field>
            {saveError ? (
              <p className="form-error" role="alert">
                {saveError}
              </p>
            ) : null}
          </div>

          <section>
            <h3 className="device-pool__section-title">{t('devicePool.detail.hashes')}</h3>
            <div className="table-wrap">
              <table className="data-table">
                <caption className="sr-only">{t('devicePool.detail.hashes')}</caption>
                <thead>
                  <tr>
                    <th scope="col">{t('devicePool.detail.field')}</th>
                    <th scope="col">{t('devicePool.detail.value')}</th>
                  </tr>
                </thead>
                <tbody>
                  {Object.entries(record.hashes).map(([field, value]) => (
                    <tr key={field}>
                      <td>{field}</td>
                      <td className="mono device-pool__wrap">{value}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>

          <section>
            <h3 className="device-pool__section-title">{t('devicePool.detail.claims')}</h3>
            <div className="table-wrap">
              <table className="data-table">
                <caption className="sr-only">{t('devicePool.detail.claims')}</caption>
                <thead>
                  <tr>
                    <th scope="col">{t('devicePool.detail.field')}</th>
                    <th scope="col">{t('devicePool.detail.value')}</th>
                  </tr>
                </thead>
                <tbody>
                  {Object.entries(record.claims).map(([field, value]) => (
                    <tr key={field}>
                      <td>{field}</td>
                      <td className="mono device-pool__wrap">{value}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>

          <section>
            <h3 className="device-pool__section-title">{t('devicePool.detail.timeline')}</h3>
            <dl className="device-pool__meta">
              <MetaRow label={t('devicePool.detail.createdAt')}>
                <span>{formatDateTimeSeconds(record.createdAt)}</span>
              </MetaRow>
              <MetaRow label={t('devicePool.detail.assignedAt')}>
                <span>{record.assignedAt ? formatDateTimeSeconds(record.assignedAt) : '—'}</span>
              </MetaRow>
              <MetaRow label={t('devicePool.detail.presentedAt')}>
                <span>
                  {record.presentedAt
                    ? formatDateTimeSeconds(record.presentedAt)
                    : t('devicePool.neverPresented')}
                </span>
              </MetaRow>
              <MetaRow label={t('devicePool.detail.loginCount')}>
                <span>{record.loginCount}</span>
              </MetaRow>
              <MetaRow label={t('devicePool.state.retired')}>
                <span>
                  {record.retiredAt
                    ? t('devicePool.detail.retired', {
                        time: formatDateTimeSeconds(record.retiredAt),
                        reason: record.retiredReason || '—',
                      })
                    : '—'}
                </span>
              </MetaRow>
            </dl>
          </section>
        </div>
      )}
    </Drawer>
  )
}
