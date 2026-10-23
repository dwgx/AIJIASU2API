import type { FlagView, LabDisabled, LabLogLevel, LabSeverity, ServerView } from './types'
import type { Tone } from './types'
import { NODE_STATE } from './labels'

/** Narrows the daemon's `{enabled:false, reason}` disabled body. */
export function isLabDisabled<T extends object>(value: T | LabDisabled): value is LabDisabled {
  return 'enabled' in value && value.enabled === false
}

/** Collapses a bare-array endpoint or its disabled body into a renderable list. */
export function labList<T>(value: T[] | LabDisabled): T[] {
  return Array.isArray(value) ? value : []
}

export function disabledReason(value: unknown, fallback: string): string {
  if (value && typeof value === 'object' && 'reason' in value) {
    const reason: unknown = value.reason
    if (typeof reason === 'string' && reason.trim()) return reason
  }
  return fallback
}

export function formatAge(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—'
  if (seconds < 60) return `${Math.round(seconds)}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`
  if (seconds < 86_400) return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
  return `${Math.floor(seconds / 86_400)}d ${Math.floor((seconds % 86_400) / 3600)}h`
}

export function formatUptime(seconds: number): string {
  return formatAge(seconds)
}

const SEVERITY_TONE: Record<LabSeverity, Tone> = {
  info: 'info',
  warn: 'warning',
  error: 'danger',
}

export function severityTone(severity: string): Tone {
  return SEVERITY_TONE[severity as LabSeverity] ?? 'neutral'
}

const LOG_TONE: Record<string, Tone> = {
  DEBUG: 'neutral',
  INFO: 'info',
  WARN: 'warning',
  ERROR: 'danger',
}

export function logTone(level: string): Tone {
  return LOG_TONE[level.toUpperCase()] ?? 'neutral'
}

export function isLogLevel(value: string): value is LabLogLevel {
  return value === 'DEBUG' || value === 'INFO' || value === 'WARN' || value === 'ERROR'
}

/**
 * Secret flags never expose their value. `set` is the only state the daemon
 * serialises for them, and the UI must not invent anything else.
 */
export function flagDisplay(flag: FlagView, t: (key: string) => string): string {
  if (flag.secret) {
    if (flag.set === true) return t('common.set')
    if (flag.set === false) return t('common.notSet')
    return t('common.redacted')
  }
  if (flag.value !== undefined && flag.value !== '') return flag.value
  if (flag.set === true) return t('common.set')
  if (flag.set === false) return t('common.notSet')
  return '—'
}

export function serverStatusTone(status: string): Tone {
  switch (status.toLowerCase()) {
    case 'ok':
    case 'live':
      return 'success'
    case 'offline':
    case 'down':
      return 'danger'
    case 'full':
      return 'warning'
    default:
      return 'neutral'
  }
}

export function serverFlagLabel(server: ServerView, t: (key: string) => string): string {
  if (server.skippedByLimit) return t('lab.server.skippedByLimit')
  if (server.selected) return t('lab.server.selected')
  return t('lab.server.notSelected')
}

export function certPinLabel(mode: string, t: (key: string) => string): string {
  if (mode === 'pinned') return t('lab.certPin.pinned')
  if (mode === 'default') return t('lab.certPin.default')
  return mode || t('lab.certPin.unknown')
}

const NODE_STATE_BY_NAME: Partial<Record<string, { labelKey: string; tone: Tone }>> = NODE_STATE

/** Known node states carry a translation key; unknown states surface the raw value. */
export function nodeStateMeta(state: string): { labelKey: string | null; label: string; tone: Tone } {
  const known = NODE_STATE_BY_NAME[state]
  if (known) return { labelKey: known.labelKey, label: state, tone: known.tone }
  return { labelKey: null, label: state || 'unknown', tone: 'neutral' }
}

export function healthTone(value: string): Tone {
  const normalized = value.toLowerCase()
  if (normalized === 'ok' || normalized === 'running' || normalized === 'pinned' || normalized === 'sealed') {
    return 'success'
  }
  if (normalized === 'disabled' || normalized === 'unavailable' || normalized === 'none') return 'warning'
  if (normalized === 'error' || normalized === 'failed' || normalized === 'down') return 'danger'
  return 'neutral'
}
