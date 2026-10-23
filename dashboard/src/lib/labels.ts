import type { TranslationKey } from './i18n'
import type { AccountState, NodeState, Severity, Tone } from './types'

export const ACCOUNT_STATE: Record<AccountState, { labelKey: TranslationKey; tone: Tone }> = {
  active: { labelKey: 'state.active', tone: 'success' },
  paused: { labelKey: 'state.paused', tone: 'warning' },
  auth_failed: { labelKey: 'state.auth_failed', tone: 'danger' },
  expired: { labelKey: 'state.expired', tone: 'danger' },
}

export const NODE_STATE: Record<NodeState, { labelKey: TranslationKey; tone: Tone }> = {
  LIVE: { labelKey: 'state.LIVE', tone: 'success' },
  READY: { labelKey: 'state.READY', tone: 'info' },
  STARTING: { labelKey: 'state.STARTING', tone: 'warning' },
  DRAINING: { labelKey: 'state.DRAINING', tone: 'warning' },
  DOWN: { labelKey: 'state.DOWN', tone: 'danger' },
}

export const SEVERITY: Record<Severity, { labelKey: TranslationKey; tone: Tone }> = {
  info: { labelKey: 'state.info', tone: 'info' },
  warn: { labelKey: 'state.warn', tone: 'warning' },
  error: { labelKey: 'state.error', tone: 'danger' },
}

export function statusTone(status: number): Tone {
  if (status < 300) return 'success'
  if (status < 400) return 'info'
  if (status < 500) return 'warning'
  return 'danger'
}
