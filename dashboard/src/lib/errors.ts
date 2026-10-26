import type { DeviceImportConflict } from './types'

/**
 * HTTP error with the daemon's `{error:{code,message}}` envelope unpacked.
 * `message` stays human-readable so list views can render it directly, while
 * `status`/`code` let guarded actions distinguish 409 CONFLICT from 429
 * QUOTA_EXCEEDED without parsing strings.
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string | null
  /** Stable machine reason from the error envelope; null when the daemon omits it. */
  readonly reason: string | null

  constructor(
    status: number,
    message: string,
    code: string | null = null,
    reason: string | null = null,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.reason = reason
  }
}

/** Raised for HTTP 401 so the shell can open the runtime token gate. */
export class UnauthorizedError extends ApiError {
  constructor(message = '401 Unauthorized') {
    super(401, message, 'UNAUTHORIZED')
    this.name = 'UnauthorizedError'
  }
}

/**
 * A device-pool write rejected by the daemon. `reason` is the stable DEVICE_*
 * machine code; an import conflict also carries the per-record `conflicts`
 * array that the generic error envelope would drop.
 */
export class DevicePoolError extends ApiError {
  readonly reason: string | null
  readonly conflicts: DeviceImportConflict[]

  constructor(
    status: number,
    message: string,
    code: string | null = null,
    reason: string | null = null,
    conflicts: DeviceImportConflict[] = [],
  ) {
    super(status, message, code)
    this.name = 'DevicePoolError'
    this.reason = reason
    this.conflicts = conflicts
  }
}

type ScopeTranslate = (key: string, params?: Record<string, string | number>) => string

/**
 * The localized sentence for a scoped-key refusal, or null when `cause` is not
 * a 403 carrying one of the scoped-key reasons. A daemon that predates the
 * reason field leaves `reason` null, so every caller keeps its generic message
 * instead of guessing.
 */
export function scopeDenial(
  cause: unknown,
  t: ScopeTranslate,
  surfaceKey: string,
  scope: string,
): string | null {
  if (!(cause instanceof ApiError) || cause.status !== 403) return null
  if (cause.reason !== 'scoped_key_restricted' && cause.reason !== 'scope_required') {
    return null
  }
  return t('error.scopeDenied', { surface: t(surfaceKey), scope })
}
