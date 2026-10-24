import { useEffect, useId, useState } from 'react'
import { api } from '../../lib/api'
import { useI18n } from '../../lib/i18n'
import { DEVICE_POOL_GENERATORS } from '../../lib/types'
import type { DevicePoolProfile, Platform } from '../../lib/types'
import { Modal } from '../Overlay'
import { Button, Field, Input, Select } from '../primitives'
import { useToast } from '../Toast'
import { deviceErrorText } from './shared'

const PROFILE_FAMILY: Record<DevicePoolProfile, Platform> = {
  'linux-4.2.3.0-binary': 'linux',
  'linux-4.3.0.1-binary': 'linux',
  'openajs-4.11.10.0-rawtcp': 'android',
}

const PLATFORM_VALUES: Platform[] = ['linux', 'android', 'ios', 'windows']

/** Only the compiled profiles can be generated; the rest have no profile. */
const PROFILES_BY_PLATFORM: Partial<Record<Platform, DevicePoolProfile[]>> = {
  linux: ['linux-4.2.3.0-binary', 'linux-4.3.0.1-binary'],
  android: ['openajs-4.11.10.0-rawtcp'],
}

interface GenerateDeviceDialogProps {
  open: boolean
  onClose: () => void
  onGenerated: (count: number) => void
}

export function GenerateDeviceDialog({ open, onClose, onGenerated }: GenerateDeviceDialogProps) {
  const { t } = useI18n()
  const { push } = useToast()
  const countId = useId()
  const prefixId = useId()
  const [count, setCount] = useState('1')
  const [platform, setPlatform] = useState<Platform>('linux')
  const [profile, setProfile] = useState<DevicePoolProfile | ''>('linux-4.2.3.0-binary')
  const [algorithm, setAlgorithm] = useState<string>(DEVICE_POOL_GENERATORS[0])
  const [prefix, setPrefix] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [created, setCreated] = useState<number | null>(null)

  useEffect(() => {
    if (!open) return
    setCount('1')
    setPlatform('linux')
    setProfile('linux-4.2.3.0-binary')
    setAlgorithm(DEVICE_POOL_GENERATORS[0])
    setPrefix('')
    setBusy(false)
    setError('')
    setCreated(null)
  }, [open])

  const choosePlatform = (next: string) => {
    const value = next as Platform
    setPlatform(value)
    const profiles = PROFILES_BY_PLATFORM[value] ?? []
    setProfile(profiles[0] ?? '')
    setError('')
  }

  const chooseProfile = (next: string) => {
    const value = next as DevicePoolProfile
    setProfile(value)
    setPlatform(PROFILE_FAMILY[value])
    setError('')
  }

  const submit = async () => {
    const parsed = Number(count)
    if (!Number.isInteger(parsed) || parsed < 1 || parsed > 100) {
      setError(t('devicePool.generate.countInvalid'))
      return
    }
    if (prefix.trim().length > 80) {
      setError(t('devicePool.generate.prefixTooLong'))
      return
    }
    if (!profile) {
      setError(t('devicePool.generate.noPlatform'))
      return
    }
    setBusy(true)
    setError('')
    try {
      const devices = await api.generateDevices({
        count: parsed,
        platform,
        profile,
        algorithm: algorithm as (typeof DEVICE_POOL_GENERATORS)[number],
        labelPrefix: prefix.trim() || undefined,
      })
      setCreated(devices.length)
      push(t('devicePool.generate.done', { count: devices.length }), 'success')
      onGenerated(devices.length)
    } catch (cause) {
      setError(deviceErrorText(t, cause, t('devicePool.generate.failed')))
    } finally {
      setBusy(false)
    }
  }

  const profileOptions = (PROFILES_BY_PLATFORM[platform] ?? []).map((value) => ({
    value,
    label: value,
  }))

  return (
    <Modal
      open={open}
      title={t('devicePool.generate.title')}
      description={t('devicePool.generate.desc')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t('common.close')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} loading={busy}>
            {t('devicePool.generate.submit')}
          </Button>
        </>
      }
    >
      <div className="form">
        <div className="form-grid">
          <Field label={t('devicePool.generate.count')} htmlFor={countId} hint={t('devicePool.generate.countHint')}>
            <Input
              id={countId}
              type="number"
              min={1}
              max={100}
              inputMode="numeric"
              value={count}
              onChange={(event) => setCount(event.target.value)}
            />
          </Field>
          <Field label={t('devicePool.generate.platform')}>
            <Select
              aria-label={t('devicePool.generate.platform')}
              value={platform}
              options={PLATFORM_VALUES.map((value) => ({
                value,
                label: value,
                description: PROFILES_BY_PLATFORM[value] ? undefined : t('devicePool.generate.noPlatform'),
                disabled: !PROFILES_BY_PLATFORM[value],
              }))}
              onChange={choosePlatform}
            />
          </Field>
          <Field label={t('devicePool.generate.profile')}>
            <Select
              aria-label={t('devicePool.generate.profile')}
              value={profile}
              placeholder={t('devicePool.generate.noPlatform')}
              options={profileOptions}
              onChange={chooseProfile}
            />
          </Field>
          <Field label={t('devicePool.generate.algorithm')}>
            <Select
              aria-label={t('devicePool.generate.algorithm')}
              value={algorithm}
              options={DEVICE_POOL_GENERATORS.map((value) => ({ value, label: value }))}
              onChange={setAlgorithm}
            />
          </Field>
        </div>
        <Field
          label={t('devicePool.generate.labelPrefix')}
          htmlFor={prefixId}
          hint={t('devicePool.generate.labelHint')}
        >
          <Input
            id={prefixId}
            value={prefix}
            maxLength={80}
            autoComplete="off"
            onChange={(event) => setPrefix(event.target.value)}
          />
        </Field>
        {error ? (
          <p className="form-error" role="alert">
            {error}
          </p>
        ) : null}
        {created !== null ? (
          <p className="form-note" role="status">
            {t('devicePool.generate.result', { count: created })}
          </p>
        ) : null}
      </div>
    </Modal>
  )
}
