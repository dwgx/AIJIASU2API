/**
 * Typed errors. None of them carries a request header, body or token, so an
 * error can be logged verbatim.
 */

export interface ApiErrorInit {
  status: number;
  code: string;
  message: string;
  reason?: string;
  details?: Record<string, string>;
  retryAfterMs?: number;
  method: string;
  path: string;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly reason?: string;
  readonly details?: Record<string, string>;
  readonly retryAfterMs?: number;
  readonly method: string;
  readonly path: string;

  constructor(init: ApiErrorInit) {
    super(
      `ajs: ${init.method} ${init.path}: HTTP ${init.status} ${init.code}` +
        (init.message && init.message !== init.code ? `: ${init.message}` : "") +
        (init.reason ? ` (${init.reason})` : ""),
    );
    this.name = "ApiError";
    this.status = init.status;
    this.code = init.code;
    this.reason = init.reason;
    this.details = init.details;
    this.retryAfterMs = init.retryAfterMs;
    this.method = init.method;
    this.path = init.path;
  }

  get retryable(): boolean {
    return (
      this.status === 429 ||
      this.status === 500 ||
      this.status === 502 ||
      this.status === 503 ||
      this.status === 504
    );
  }
}

export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError;
}

export function isApiErrorCode(err: unknown, code: string): boolean {
  return isApiError(err) && err.code === code;
}

/** A transport failure: DNS, connect, TLS, reset or a caller abort. */
export class NetworkError extends Error {
  readonly method: string;
  readonly path: string;
  override readonly cause?: unknown;

  constructor(method: string, path: string, cause: unknown) {
    super(`ajs: ${method} ${path}: ${cause instanceof Error ? cause.message : String(cause)}`);
    this.name = "NetworkError";
    this.method = method;
    this.path = path;
    this.cause = cause;
  }
}

/** The request did not receive response headers within timeoutMs. */
export class TimeoutError extends Error {
  readonly method: string;
  readonly path: string;
  readonly timeoutMs: number;

  constructor(method: string, path: string, timeoutMs: number) {
    super(`ajs: ${method} ${path}: timed out after ${timeoutMs}ms`);
    this.name = "TimeoutError";
    this.method = method;
    this.path = path;
    this.timeoutMs = timeoutMs;
  }
}

/** A terminal failed or canceled operation. */
export class OperationFailure extends Error {
  readonly operationId: string;
  readonly state: string;
  readonly code: string;
  readonly details?: Record<string, string>;

  constructor(init: {
    operationId: string;
    state: string;
    code: string;
    message: string;
    details?: Record<string, string>;
  }) {
    super(`ajs: operation ${init.operationId} ${init.state}: ${init.code}`);
    this.name = "OperationFailure";
    this.operationId = init.operationId;
    this.state = init.state;
    this.code = init.code;
    this.details = init.details;
  }
}

/** The local poll budget expired; the operation may still be running. */
export class OperationTimeoutError extends Error {
  readonly operationId: string;
  readonly timeoutMs: number;

  constructor(operationId: string, timeoutMs: number) {
    super(`ajs: operation ${operationId} did not finish within ${timeoutMs}ms`);
    this.name = "OperationTimeoutError";
    this.operationId = operationId;
    this.timeoutMs = timeoutMs;
  }
}
