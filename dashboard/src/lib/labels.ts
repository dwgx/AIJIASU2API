import type { TranslationKey } from './i18n'
import type { AccountState, NodeState, Severity, Status, Tone } from './types'

interface StateMeta {
  labelKey: TranslationKey
  /** Fill for meters and dots that predate the status vocabulary. */
  tone: Tone
  status: Status
}

export const ACCOUNT_STATE: Record<AccountState, StateMeta> = {
  active: { labelKey: 'state.active', tone: 'success', status: 'ok' },
  paused: { labelKey: 'state.paused', tone: 'warning', status: 'warn' },
  auth_failed: { labelKey: 'state.auth_failed', tone: 'danger', status: 'error' },
  expired: { labelKey: 'state.expired', tone: 'danger', status: 'unavailable' },
}

export const NODE_STATE: Record<NodeState, StateMeta> = {
  LIVE: { labelKey: 'state.LIVE', tone: 'success', status: 'ok' },
  READY: { labelKey: 'state.READY', tone: 'info', status: 'info' },
  STARTING: { labelKey: 'state.STARTING', tone: 'warning', status: 'info' },
  DRAINING: { labelKey: 'state.DRAINING', tone: 'warning', status: 'warn' },
  DOWN: { labelKey: 'state.DOWN', tone: 'danger', status: 'unavailable' },
}

export const SEVERITY: Record<Severity, StateMeta> = {
  info: { labelKey: 'state.info', tone: 'info', status: 'info' },
  warn: { labelKey: 'state.warn', tone: 'warning', status: 'warn' },
  error: { labelKey: 'state.error', tone: 'danger', status: 'error' },
}

export function statusTone(status: number): Tone {
  if (status < 300) return 'success'
  if (status < 400) return 'info'
  if (status < 500) return 'warning'
  return 'danger'
}
