/** Retry policy and small timing helpers. */

export interface RetryPolicy {
  /** Total attempts, including the first. */
  maxAttempts: number;
  /** First backoff step; doubles per attempt. */
  baseDelayMs: number;
  /** Cap for the exponential backoff. */
  maxDelayMs: number;
  /** Cap for a server-supplied Retry-After. */
  retryAfterCapMs: number;
  /** Fraction of the delay randomized into [1-jitter, 1]. */
  jitter: number;
}

export const DEFAULT_RETRY_POLICY: RetryPolicy = {
  maxAttempts: 3,
  baseDelayMs: 200,
  maxDelayMs: 5000,
  retryAfterCapMs: 30000,
  jitter: 0.2,
};

export function normalizeRetryPolicy(policy?: Partial<RetryPolicy>): RetryPolicy {
  const merged = { ...DEFAULT_RETRY_POLICY, ...policy };
  return {
    maxAttempts: Math.max(1, Math.trunc(merged.maxAttempts)),
    baseDelayMs: Math.max(1, merged.baseDelayMs),
    maxDelayMs: Math.max(1, merged.maxDelayMs),
    retryAfterCapMs: Math.max(1, merged.retryAfterCapMs),
    jitter: Math.min(1, Math.max(0, merged.jitter)),
  };
}

export function isRetryStatus(status: number): boolean {
  return status === 429 || (status >= 500 && status <= 599);
}

/** Parses Retry-After as delta-seconds or an HTTP date into milliseconds. */
export function parseRetryAfter(value: string | null): number | undefined {
  if (!value) return undefined;
  const trimmed = value.trim();
  if (trimmed === "") return undefined;
  if (/^\d+$/.test(trimmed)) {
    return Number(trimmed) * 1000;
  }
  const when = Date.parse(trimmed);
  if (Number.isNaN(when)) return undefined;
  return Math.max(0, when - Date.now());
}

export function retryDelayMs(attempt: number, retryAfterMs: number | undefined, policy: RetryPolicy): number {
  if (retryAfterMs !== undefined) {
    return Math.min(retryAfterMs, policy.retryAfterCapMs);
  }
  let delay = policy.baseDelayMs;
  for (let i = 1; i < attempt; i++) {
    delay *= 2;
    if (delay >= policy.maxDelayMs) {
      delay = policy.maxDelayMs;
      break;
    }
  }
  delay = Math.min(delay, policy.maxDelayMs);
  if (policy.jitter > 0) {
    delay = delay * (1 - policy.jitter * Math.random());
  }
  return delay;
}

export function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  if (ms <= 0) return Promise.resolve();
  if (signal?.aborted) {
    return Promise.reject(signal.reason ?? new DOMException("aborted", "AbortError"));
  }
  const { promise, resolve, reject } = Promise.withResolvers<void>();
  let timer: ReturnType<typeof setTimeout>;
  const onAbort = () => {
    clearTimeout(timer);
    reject(signal?.reason ?? new DOMException("aborted", "AbortError"));
  };
  timer = setTimeout(() => {
    signal?.removeEventListener("abort", onAbort);
    resolve();
  }, ms);
  signal?.addEventListener("abort", onAbort, { once: true });
  return promise;
}
