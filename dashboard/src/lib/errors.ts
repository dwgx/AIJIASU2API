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

  constructor(status: number, message: string, code: string | null = null) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
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
