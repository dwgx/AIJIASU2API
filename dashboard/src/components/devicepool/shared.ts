import { DevicePoolError } from '../../lib/errors'
import type { DevicePoolRecord, DevicePoolState, Tone } from '../../lib/types'

export type Translate = (key: string, params?: Record<string, string | number>) => string

/** Every stable DEVICE_* reason the daemon can return, mapped to copy. */
const REASON_KEYS: Record<string, string> = {
  DEVICE_POOL_DISABLED: 'devicePool.reason.poolDisabled',
  DEVICE_NOT_FOUND: 'devicePool.reason.notFound',
  DEVICE_STATE_INVALID: 'devicePool.reason.stateInvalid',
  DEVICE_ALREADY_BOUND: 'devicePool.reason.alreadyBound',
  DEVICE_ACTIVE_SESSION: 'devicePool.reason.activeSession',
  DEVICE_MATERIAL_MISSING: 'devicePool.reason.materialMissing',
  DEVICE_PROFILE_MISMATCH: 'devicePool.reason.profileMismatch',
  DEVICE_PLATFORM_MISMATCH: 'devicePool.reason.platformMismatch',
  DEVICE_ALGORITHM_UNKNOWN: 'devicePool.reason.algorithmUnknown',
  DEVICE_DUPLICATE: 'devicePool.reason.duplicate',
  DEVICE_POOL_FULL: 'devicePool.reason.poolFull',
  DEVICE_EXPORT_EMPTY: 'devicePool.reason.exportEmpty',
  DEVICE_EXPORT_FORMAT: 'devicePool.reason.exportFormat',
  DEVICE_IMPORT_FORMAT: 'devicePool.reason.importFormat',
  DEVICE_IMPORT_DECRYPT: 'devicePool.reason.importDecrypt',
  DEVICE_IMPORT_CONFLICT: 'devicePool.reason.importConflict',
  DEVICE_CONFIRM_REQUIRED: 'devicePool.reason.confirmRequired',
}

/**
 * Localizes a device-pool failure. A known machine reason wins over the
 * daemon's English message; anything else keeps the raw message so an
 * unmapped failure is still actionable.
 */
export function deviceErrorText(t: Translate, cause: unknown, fallback: string): string {
  if (cause instanceof DevicePoolError) {
    const key = cause.reason ? REASON_KEYS[cause.reason] : undefined
    if (key) return t(key)
    return cause.message || fallback
  }
  return cause instanceof Error ? cause.message : fallback
}

/** A `sha256:<hex>` value trimmed to its algorithm plus eight hex digits. */
export function shortHash(value: string | undefined | null): string {
  if (!value) return '—'
  const separator = value.indexOf(':')
  if (separator < 0) return value
  const prefix = value.slice(0, separator + 1)
  const hex = value.slice(separator + 1)
  return hex.length > 8 ? `${prefix}${hex.slice(0, 8)}…` : value
}

/** A full fingerprint trimmed for table cells; the drawer shows it whole. */
export function shortFingerprint(value: string | undefined | null): string {
  if (!value) return '—'
  const separator = value.indexOf(':')
  if (separator < 0) return value
  const prefix = value.slice(0, separator + 1)
  const hex = value.slice(separator + 1)
  return hex.length > 12 ? `${prefix}${hex.slice(0, 12)}…` : value
}

export function deviceLabel(record: DevicePoolRecord): string {
  return record.label || record.id
}

const STATE_TONES: Record<DevicePoolState, Tone> = {
  available: 'info',
  assigned: 'mint',
  retired: 'neutral',
  quarantined: 'danger',
}

export function deviceStateTone(state: DevicePoolState): Tone {
  return STATE_TONES[state] ?? 'neutral'
}

/** The state a record can move to from where it is now. */
export function deviceActions(record: DevicePoolRecord): Array<
  'assign' | 'release' | 'retire' | 'quarantine' | 'purge'
> {
  switch (record.state) {
    case 'available':
      return ['assign', 'retire', 'quarantine', 'purge']
    case 'assigned':
      return ['release', 'retire']
    case 'retired':
      return ['assign', 'purge']
    case 'quarantined':
      return ['purge']
    default:
      return []
  }
}

/** `ajs-device-pool-YYYYMMDD-HHMMSS.json`, matching the other exports. */
export function deviceExportFilename(now = new Date()): string {
  const pad = (value: number) => String(value).padStart(2, '0')
  const stamp =
    `${now.getUTCFullYear()}${pad(now.getUTCMonth() + 1)}${pad(now.getUTCDate())}` +
    `-${pad(now.getUTCHours())}${pad(now.getUTCMinutes())}${pad(now.getUTCSeconds())}`
  return `ajs-device-pool-${stamp}.json`
}
