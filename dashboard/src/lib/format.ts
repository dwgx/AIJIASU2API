const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB']

export function formatBytes(value: number, digits = 1): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const exponent = Math.min(Math.floor(Math.log(value) / Math.log(1024)), BYTE_UNITS.length - 1)
  const scaled = value / 1024 ** exponent
  return `${scaled.toFixed(exponent === 0 ? 0 : digits)} ${BYTE_UNITS[exponent]}`
}

export function formatNumber(value: number): string {
  return new Intl.NumberFormat('en-US').format(value)
}

export function formatCompact(value: number): string {
  return new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(value)
}

export function formatDateTime(value: string): string {
  return new Intl.DateTimeFormat('en-GB', {
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(new Date(value))
}

export function formatTime(value: string): string {
  return new Intl.DateTimeFormat('en-GB', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(new Date(value))
}

export function formatDate(value: string): string {
  return new Intl.DateTimeFormat('en-GB', { year: 'numeric', month: 'short', day: '2-digit' }).format(
    new Date(value),
  )
}

export function formatRelative(value: string, now = Date.now(), lang: 'zh' | 'en' = 'en'): string {
  const delta = now - new Date(value).getTime()
  const future = delta < 0
  const seconds = Math.abs(delta) / 1000
  let text: string
  if (seconds < 60) text = `${Math.round(seconds)}s`
  else if (seconds < 3600) text = `${Math.round(seconds / 60)}m`
  else if (seconds < 86_400) text = `${Math.round(seconds / 3600)}h`
  else text = `${Math.round(seconds / 86_400)}d`
  if (lang === 'zh') return future ? `${text}后` : `${text}前`
  return future ? `in ${text}` : `${text} ago`
}

export function formatTtl(expiresAt: string, now = Date.now(), expiredLabel = 'expired'): string {
  const seconds = Math.max(0, Math.round((new Date(expiresAt).getTime() - now) / 1000))
  if (seconds === 0) return expiredLabel
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (hours >= 24) return `${Math.floor(hours / 24)}d ${hours % 24}h`
  if (hours > 0) return `${hours}h ${String(minutes).padStart(2, '0')}m`
  return `${minutes}m`
}

export function proxyUrl(gatePort: number, region: string, protocol: 'socks5' | 'http'): string {
  return `${protocol}://127.0.0.1:${gatePort}#${region}`
}

export function percent(value: number, total: number): number {
  if (total <= 0) return 0
  return Math.max(0, Math.min(100, (value / total) * 100))
}
