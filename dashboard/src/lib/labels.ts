import type { AccountState, NodeState, Severity, Tone } from './types'

export const ACCOUNT_STATE: Record<AccountState, { label: string; tone: Tone }> = {
  active: { label: 'Active', tone: 'success' },
  paused: { label: 'Paused', tone: 'warning' },
  auth_failed: { label: 'Auth failed', tone: 'danger' },
  expired: { label: 'Expired', tone: 'danger' },
}

export const NODE_STATE: Record<NodeState, { label: string; tone: Tone }> = {
  LIVE: { label: 'Live', tone: 'success' },
  READY: { label: 'Ready', tone: 'info' },
  STARTING: { label: 'Starting', tone: 'warning' },
  DRAINING: { label: 'Draining', tone: 'warning' },
  DOWN: { label: 'Down', tone: 'danger' },
}

export const SEVERITY: Record<Severity, { label: string; tone: Tone }> = {
  info: { label: 'Info', tone: 'info' },
  warn: { label: 'Warn', tone: 'warning' },
  error: { label: 'Error', tone: 'danger' },
}

export function statusTone(status: number): Tone {
  if (status < 300) return 'success'
  if (status < 400) return 'info'
  if (status < 500) return 'warning'
  return 'danger'
}
