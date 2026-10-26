import {
  ApiError,
  NetworkError,
  OperationFailure,
  OperationTimeoutError,
  TimeoutError,
  isApiError,
} from "./errors.js";
import {
  DEFAULT_RETRY_POLICY,
  isRetryStatus,
  normalizeRetryPolicy,
  parseRetryAfter,
  retryDelayMs,
  sleep,
  type RetryPolicy,
} from "./retry.js";
import type {
  Account,
  AccountPatch,
  CreateAccountInput,
  CreateLeaseInput,
  CreateLeaseResult,
  Event,
  EventsPage,
  Lease,
  ListAccountsOptions,
  ListEventsOptions,
  ListLeasesOptions,
  ListOperationsOptions,
  Operation,
  SelectNodeRequest,
  SelectNodeResult,
  ServersOptions,
  ServersView,
  StreamEventsOptions,
  UsageOptions,
  UsageSummary,
  WaitOptions,
} from "./types.js";

export const VERSION = "0.1.0";

export interface ClientOptions {
  /** Daemon origin, for example http://127.0.0.1:18080. */
  baseUrl: string;
  /** AJS_ADMIN_TOKEN or an ajs_sk_ scoped key. Never logged. */
  token: string;
  /** Custom fetch (tests, proxies). Defaults to the global fetch. */
  fetch?: typeof fetch;
  retry?: Partial<RetryPolicy>;
  userAgent?: string;
  /** Wait for response headers on unary calls. Default 60_000; 0 disables. */
  timeoutMs?: number;
}

export interface CreateLeaseOptions {
  /** Explicit retry-safe key; generated when absent. */
  idempotencyKey?: string;
}

type QueryValue = string | number | boolean | undefined | null;

interface RequestOptions {
  query?: string;
  body?: unknown;
  headers?: Record<string, string>;
  timeoutMs?: number;
  signal?: AbortSignal;
}

function buildQuery(params: Record<string, QueryValue>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    search.set(key, String(value));
  }
  return search.toString();
}

async function discard(res: Response): Promise<void> {
  try {
    await res.body?.cancel();
  } catch {
    // The connection is already gone; the retry does not depend on this body.
  }
}

const MAX_ERROR_BYTES = 1 << 20;

/** Reads an error body up to a bound, so a huge proxy page cannot exhaust memory. */
async function readErrorText(res: Response): Promise<string> {
  if (!res.body) return "";
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let text = "";
  let total = 0;
  try {
    while (total < MAX_ERROR_BYTES) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      text += decoder.decode(value, { stream: true });
    }
  } catch {
    // Keep whatever arrived before the failure; the status still classifies it.
  } finally {
    try {
      await reader.cancel();
    } catch {
      // Already closed.
    }
  }
  return text;
}

/**
 * Dependency-free client for the ajs-accountd admin API.
 *
 * Retries: a transport error, 429 and 5xx are retried with exponential
 * backoff. POST is retried only when an Idempotency-Key header is present;
 * select-node is not retried because a queued operation already has a durable
 * id the caller can poll.
 */
export class Client {
  private readonly baseUrl: string;
  readonly #token: string;
  private readonly fetchImpl: typeof fetch;
  private readonly retry: RetryPolicy;
  private readonly userAgent: string;
  private readonly timeoutMs: number;

  constructor(options: ClientOptions) {
    if (!options || typeof options !== "object") {
      throw new TypeError("ajs: options are required");
    }
    const raw = (options.baseUrl ?? "").trim();
    if (raw === "") throw new TypeError("ajs: base URL is required");
    let parsed: URL;
    try {
      parsed = new URL(raw);
    } catch {
      throw new TypeError(`ajs: invalid base URL: ${raw}`);
    }
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      throw new TypeError(`ajs: base URL scheme must be http or https, got ${parsed.protocol}`);
    }
    if (parsed.username !== "" || parsed.password !== "") {
      throw new TypeError("ajs: base URL must not carry credentials");
    }
    if (parsed.search !== "" || parsed.hash !== "") {
      throw new TypeError("ajs: base URL must not carry a query or fragment");
    }
    if (typeof options.token !== "string" || options.token.trim() === "") {
      throw new TypeError("ajs: bearer token is required");
    }
    this.baseUrl = parsed.toString().replace(/\/+$/, "");
    this.#token = options.token;
    this.fetchImpl = options.fetch ?? globalThis.fetch;
    if (typeof this.fetchImpl !== "function") {
      throw new TypeError("ajs: no fetch implementation available");
    }
    this.retry = normalizeRetryPolicy(options.retry);
    this.userAgent = options.userAgent ?? `ajs-sdk-ts/${VERSION}`;
    this.timeoutMs = options.timeoutMs ?? 60_000;
  }

  /** Redacts the bearer token when a client is stringified. */
  toString(): string {
    return `ajs.Client{${this.baseUrl}}`;
  }

  // ---------------------------------------------------------------- transport

  private async send(
    method: string,
    path: string,
    options: RequestOptions = {},
  ): Promise<Response> {
    const payload = options.body === undefined ? undefined : JSON.stringify(options.body);
    const hasKey = options.headers?.["Idempotency-Key"] !== undefined;
    const canRetry =
      method === "GET" || method === "HEAD" || method === "PUT" || method === "DELETE" || hasKey;
    const url = this.baseUrl + path + (options.query ? `?${options.query}` : "");
    const timeoutMs = options.timeoutMs ?? this.timeoutMs;
    let lastError: unknown;

    for (let attempt = 1; attempt <= this.retry.maxAttempts; attempt++) {
      const controller = new AbortController();
      let timedOut = false;
      const onAbort = () => controller.abort();
      if (options.signal?.aborted) controller.abort();
      options.signal?.addEventListener("abort", onAbort, { once: true });
      const timer =
        timeoutMs > 0
          ? setTimeout(() => {
              timedOut = true;
              controller.abort();
            }, timeoutMs)
          : undefined;

      const headers: Record<string, string> = {
        Authorization: `Bearer ${this.#token}`,
        Accept: "application/json",
        "User-Agent": this.userAgent,
        ...(payload !== undefined ? { "Content-Type": "application/json" } : {}),
        ...(method !== "GET" && method !== "HEAD" && method !== "OPTIONS"
          ? { "X-AJS-CSRF": "1" }
          : {}),
        ...options.headers,
      };

      let res: Response;
      try {
        res = await this.fetchImpl(url, {
          method,
          headers,
          body: payload,
          signal: controller.signal,
        });
      } catch (cause) {
        if (timedOut) {
          lastError = new TimeoutError(method, path, timeoutMs);
        } else if (options.signal?.aborted) {
          throw options.signal.reason ?? new NetworkError(method, path, cause);
        } else {
          lastError = new NetworkError(method, path, cause);
        }
        if (!canRetry || attempt === this.retry.maxAttempts) throw lastError;
        await sleep(retryDelayMs(attempt, undefined, this.retry), options.signal);
        continue;
      } finally {
        clearTimeout(timer);
        options.signal?.removeEventListener("abort", onAbort);
      }

      if (res.ok) return res;
      const retryAfter = parseRetryAfter(res.headers.get("Retry-After"));
      if (canRetry && isRetryStatus(res.status) && attempt < this.retry.maxAttempts) {
        await discard(res);
        await sleep(retryDelayMs(attempt, retryAfter, this.retry), options.signal);
        continue;
      }
      throw await this.apiError(res, method, path);
    }
    throw lastError;
  }

  private async apiError(res: Response, method: string, path: string): Promise<ApiError> {
    let code = `HTTP_${res.status}`;
    let message = res.statusText || code;
    let reason: string | undefined;
    let details: Record<string, string> | undefined;
    try {
      const text = await readErrorText(res);
      if (text !== "") {
        const envelope = JSON.parse(text) as {
          error?: { code?: string; message?: string; reason?: string; details?: Record<string, string> };
        };
        const err = envelope.error;
        if (err?.code) {
          code = err.code;
          message = err.message ?? code;
          reason = err.reason;
          details = err.details;
        }
      }
    } catch {
      // A non-JSON body (proxy page, empty 502) keeps the status-derived code.
    }
    return new ApiError({
      status: res.status,
      code,
      message,
      reason,
      details,
      retryAfterMs: parseRetryAfter(res.headers.get("Retry-After")),
      method,
      path,
    });
  }

  private async request<T>(method: string, path: string, options: RequestOptions = {}): Promise<T> {
    const res = await this.send(method, path, options);
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    if (text === "") return undefined as T;
    return JSON.parse(text) as T;
  }

  // ----------------------------------------------------------------- accounts

  async listAccounts(options: ListAccountsOptions = {}): Promise<Account[]> {
    return this.request<Account[]>("GET", "/v1/accounts", {
      query: buildQuery({ state: options.state, limit: options.limit, offset: options.offset }),
    });
  }

  async createAccount(input: CreateAccountInput): Promise<Account> {
    return this.request<Account>("POST", "/v1/accounts", { body: input });
  }

  async getAccount(id: string): Promise<Account> {
    return this.request<Account>("GET", `/v1/accounts/${encodeURIComponent(id)}`);
  }

  async patchAccount(id: string, patch: AccountPatch): Promise<Account> {
    return this.request<Account>("PATCH", `/v1/accounts/${encodeURIComponent(id)}`, { body: patch });
  }

  async deleteAccount(id: string): Promise<void> {
    await this.request<void>("DELETE", `/v1/accounts/${encodeURIComponent(id)}`);
  }

  async loginAccount(id: string): Promise<Account> {
    return this.request<Account>("POST", `/v1/accounts/${encodeURIComponent(id)}/actions/login`);
  }

  async logoutAccount(id: string): Promise<Account> {
    return this.request<Account>("POST", `/v1/accounts/${encodeURIComponent(id)}/actions/logout`);
  }

  async listAccountServers(id: string, options: ServersOptions = {}): Promise<ServersView> {
    return this.request<ServersView>("GET", `/v1/accounts/${encodeURIComponent(id)}/servers`, {
      query: buildQuery({
        q: options.q,
        region: options.region,
        status: options.status,
        selected: options.selected,
        offset: options.offset,
        limit: options.limit,
        sort: options.sort,
      }),
    });
  }

  // --------------------------------------------------------------- operations

  /**
   * Selects the account's live node. A 200 means the target was already
   * current (changed:false). A 202 is polled to a terminal operation; a
   * failed or canceled operation throws OperationFailure. A 504 that names a
   * durable operationId is polled instead of losing the operation.
   */
  async selectNode(
    accountId: string,
    request: SelectNodeRequest,
    wait: WaitOptions = {},
  ): Promise<SelectNodeResult> {
    const path = `/v1/accounts/${encodeURIComponent(accountId)}/actions/select-node`;
    let res: Response;
    try {
      res = await this.send("POST", path, {
        body: request,
        timeoutMs: wait.timeoutMs,
        signal: wait.signal,
      });
    } catch (err) {
      if (isApiError(err) && err.status === 504) {
        const operationId = err.details?.["operationId"];
        if (operationId) return this.finishSelectNode(operationId, wait);
      }
      throw err;
    }
    if (res.status === 200) {
      return (await res.json()) as SelectNodeResult;
    }
    if (res.status !== 202) {
      throw await this.apiError(res, "POST", path);
    }
    const location = res.headers.get("Location");
    const operation = (await res.json()) as Operation;
    const id = location?.split("/").filter(Boolean).pop() ?? operation.id;
    if (!id) throw new Error("ajs: select-node accepted without an operation id");
    const retryAfter = parseRetryAfter(res.headers.get("Retry-After"));
    return this.finishSelectNode(id, {
      ...wait,
      intervalMs: wait.intervalMs ?? retryAfter,
    });
  }

  private async finishSelectNode(id: string, wait: WaitOptions): Promise<SelectNodeResult> {
    const operation = await this.waitOperation(id, wait);
    if (operation.state === "succeeded") {
      if (!operation.result) {
        throw new Error(`ajs: operation ${id} succeeded without a result`);
      }
      return operation.result;
    }
    if (operation.state === "canceled") {
      throw new OperationFailure({
        operationId: id,
        state: operation.state,
        code: "OPERATION_CANCELED",
        message: "operation was canceled",
      });
    }
    throw new OperationFailure({
      operationId: id,
      state: operation.state,
      code: operation.error?.code ?? "OPERATION_FAILED",
      message: operation.error?.message ?? "operation failed",
      details: operation.error?.details,
    });
  }

  /**
   * Polls an operation until it is terminal. A failed operation is returned
   * (inspect state and error); only a transport failure, timeout or abort
   * throws.
   */
  async waitOperation(id: string, options: WaitOptions = {}): Promise<Operation> {
    const first = options.intervalMs ?? 1000;
    const maxInterval = Math.max(options.maxIntervalMs ?? 5000, first);
    const deadline = options.timeoutMs !== undefined ? Date.now() + options.timeoutMs : undefined;
    let delay = first;
    for (;;) {
      const operation = await this.getOperation(id, options.signal);
      if (
        operation.state === "succeeded" ||
        operation.state === "failed" ||
        operation.state === "canceled"
      ) {
        return operation;
      }
      if (deadline !== undefined) {
        const remaining = deadline - Date.now();
        if (remaining <= 0) {
          throw new OperationTimeoutError(id, options.timeoutMs ?? 0);
        }
        await sleep(Math.min(delay, remaining), options.signal);
      } else {
        await sleep(delay, options.signal);
      }
      delay = Math.min(delay * 2, maxInterval);
    }
  }

  async listOperations(options: ListOperationsOptions = {}): Promise<Operation[]> {
    return this.request<Operation[]>("GET", "/v1/operations", {
      query: buildQuery({
        account: options.account,
        state: options.state,
        type: options.type,
        limit: options.limit,
      }),
    });
  }

  async getOperation(id: string, signal?: AbortSignal): Promise<Operation> {
    return this.request<Operation>("GET", `/v1/operations/${encodeURIComponent(id)}`, { signal });
  }

  async cancelOperation(id: string): Promise<Operation> {
    return this.request<Operation>("DELETE", `/v1/operations/${encodeURIComponent(id)}`);
  }

  // ------------------------------------------------------------------- leases

  async listLeases(options: ListLeasesOptions = {}): Promise<Lease[]> {
    return this.request<Lease[]>("GET", "/v1/leases", {
      query: buildQuery({ user_id: options.userId, limit: options.limit, offset: options.offset }),
    });
  }

  /**
   * Creates an idempotent lease. A key is generated when neither the body nor
   * the option supplies one, and is sent as both the body field and the
   * Idempotency-Key header so a transient 5xx is retried safely.
   */
  async createLease(
    input: CreateLeaseInput,
    options: CreateLeaseOptions = {},
  ): Promise<CreateLeaseResult> {
    const generated =
      typeof crypto.randomUUID === "function"
        ? crypto.randomUUID()
        : Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
            b.toString(16).padStart(2, "0"),
          ).join("");
    const explicit = options.idempotencyKey ?? input.idempotencyKey;
    const key = explicit && explicit.trim() !== "" ? explicit : `sdk-${generated}`;
    return this.request<CreateLeaseResult>("POST", "/v1/leases", {
      body: { ...input, idempotencyKey: key },
      headers: { "Idempotency-Key": key },
    });
  }

  async rotateLease(id: string): Promise<Lease> {
    return this.request<Lease>("POST", `/v1/leases/${encodeURIComponent(id)}/actions/rotate`);
  }

  async releaseLease(id: string): Promise<void> {
    await this.request<void>("DELETE", `/v1/leases/${encodeURIComponent(id)}`, {
      body: { confirm: true },
    });
  }

  // -------------------------------------------------------------------- usage

  async getUsage(options: UsageOptions = {}): Promise<UsageSummary> {
    return this.request<UsageSummary>("GET", "/v1/usage", {
      query: buildQuery({
        from: options.from,
        to: options.to,
        account: options.account,
        userId: options.userId,
      }),
    });
  }

  // ------------------------------------------------------------------- events

  async listEvents(options: ListEventsOptions = {}): Promise<EventsPage> {
    return this.request<EventsPage>("GET", "/v1/events", {
      query: buildQuery({ since: options.since, limit: options.limit, wait: options.wait }),
    });
  }

  /**
   * Opens the retained-plus-live SSE stream. Consume it with `for await`.
   * The generator ends cleanly when the daemon closes the stream; abort it
   * with options.signal. A cursor error is thrown as ApiError before the
   * first event.
   */
  async *streamEvents(options: StreamEventsOptions = {}): AsyncGenerator<Event, void, void> {
    const headers: Record<string, string> = { Accept: "text/event-stream" };
    if (options.lastEventId) headers["Last-Event-ID"] = options.lastEventId;
    // The caller's signal is bridged to a controller that stays attached to the
    // response for the generator's whole lifetime, so an abort cancels body
    // reads rather than only the wait for headers.
    const controller = new AbortController();
    const onAbort = () => controller.abort(options.signal?.reason);
    if (options.signal?.aborted) controller.abort(options.signal.reason);
    else options.signal?.addEventListener("abort", onAbort, { once: true });
    try {
      const res = await this.send("GET", "/v1/events/stream", {
        query: buildQuery({ since: options.since }),
        headers,
        timeoutMs: options.timeoutMs,
        signal: controller.signal,
      });
      if (!res.body) {
        throw new NetworkError("GET", "/v1/events/stream", new Error("response has no body"));
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      let id = "";
      let name = "";
      let data: string[] = [];
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          for (;;) {
            const newline = buffer.indexOf("\n");
            if (newline < 0) break;
            let line = buffer.slice(0, newline);
            buffer = buffer.slice(newline + 1);
            if (line.endsWith("\r")) line = line.slice(0, -1);
            if (line === "") {
              if (data.length > 0) {
                const event = JSON.parse(data.join("\n")) as Event;
                if (!event.id) event.id = id;
                if (!event.type) event.type = name;
                yield event;
              }
              id = "";
              name = "";
              data = [];
              continue;
            }
            if (line.startsWith(":")) continue; // keepalive comment
            const colon = line.indexOf(":");
            if (colon < 0) continue;
            const field = line.slice(0, colon);
            let valueText = line.slice(colon + 1);
            if (valueText.startsWith(" ")) valueText = valueText.slice(1);
            if (field === "id") id = valueText;
            else if (field === "event") name = valueText;
            else if (field === "data") data.push(valueText);
          }
        }
      } finally {
        try {
          await reader.cancel();
        } catch {
          // The stream may already be closed or aborted.
        }
      }
    } finally {
      options.signal?.removeEventListener("abort", onAbort);
    }
  }
}

export { DEFAULT_RETRY_POLICY, isRetryStatus, normalizeRetryPolicy, parseRetryAfter, retryDelayMs };
export type { RetryPolicy };
