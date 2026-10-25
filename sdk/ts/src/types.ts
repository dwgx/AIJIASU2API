/**
 * Wire types mirroring docs/openapi.json (ajs-accountd 0.1.0).
 *
 * Timestamps stay RFC 3339 strings because the daemon sends an empty string
 * when unset; a client must not turn that into an invented Date.
 */

export type AccountState = "active" | "paused" | "auth_failed" | "expired";
export type Platform = "android" | "ios" | "windows" | "linux";
export type ProxyProtocol = "socks5" | "http";
export type NodeState = "LIVE" | "READY" | "STARTING" | "DRAINING" | "DOWN" | "STALE";
export type OperationState = "queued" | "running" | "succeeded" | "failed" | "canceled";
export type LeaseStatus = "active" | "expired" | "revoked";
export type OperationStepState = "ok" | "failed" | "canceled";

export interface Account {
  id: string;
  label: string;
  username: string;
  platform: Platform;
  state: AccountState;
  concurrency: number;
  useCount: number;
  /** Display capacity; the daemon omits it when zero. */
  effectiveSlots?: number;
  regions: string[];
  nodeCount: number;
  liveNodeCount: number;
  usageBytes: number;
  trafficBudgetBytes: number | null;
  expiresAt: string;
  lastLoginAt: string;
  createdAt: string;
}

export interface CreateAccountInput {
  label: string;
  username: string;
  /** Write-only; never returned by the daemon. */
  password: string;
  platform: Platform;
  concurrency: number;
  regions?: string[];
}

/** Partial update: undefined leaves a field unchanged, null clears the budget. */
export interface AccountPatch {
  label?: string;
  username?: string;
  platform?: Platform;
  state?: AccountState;
  concurrency?: number;
  regions?: string[];
  expiresAt?: string;
  trafficBudgetBytes?: number | null;
}

export interface RegionCount {
  code: string;
  count: number;
}

export interface ServerView {
  id: string;
  title: string;
  region: string;
  host: string;
  port: number;
  status: string;
  nodeId: string;
  nodeState: NodeState | "";
  selected: boolean;
  skippedByLimit: boolean;
  memberOnly: boolean | null;
  fullLoad: boolean;
  blockingMarks: string[];
  hasPingSecret: boolean;
  latencyMs: number | null;
}

export interface ServersView {
  enabled: boolean;
  reason?: string;
  accountId: string;
  fetchedAt: string;
  stale: boolean;
  total: number;
  offset: number;
  limit: number;
  nextOffset: number;
  hasMore: boolean;
  regions: RegionCount[];
  servers: ServerView[];
}

export interface OperationTarget {
  serverId?: string;
  nodeId?: string;
  region?: string;
  strategy?: "next" | "first";
}

export interface SelectNodeRequest {
  target?: OperationTarget;
  drainSeconds?: number;
  timeoutSeconds?: number;
  ifCurrent?: "noop" | "error" | "reselect";
  rollback?: boolean;
  wait?: boolean;
}

export interface SelectNodeResult {
  changed: boolean;
  accountId: string;
  nodeId?: string;
  serverId: string;
  gatePort?: number;
  egressIp?: string;
  sessionHealthy: boolean;
  previousNodeId?: string;
  rollbackAttempted?: boolean;
  rollback?: string;
  durationMs?: number;
  warnings?: string[];
}

export interface OperationStep {
  name: string;
  state: OperationStepState;
  durationMs: number;
}

export interface OperationError {
  code: string;
  message: string;
  details?: Record<string, string>;
}

export interface Operation {
  id: string;
  /** Known values: account.switch, account.connect. */
  type: string;
  state: OperationState;
  accountId: string;
  target: OperationTarget;
  requestedBy: string;
  createdAt: string;
  startedAt: string;
  finishedAt: string;
  steps: OperationStep[];
  result: SelectNodeResult | null;
  error: OperationError | null;
}

export interface Lease {
  id: string;
  userId: string;
  userName: string;
  accountId: string;
  accountLabel: string;
  platform: Platform;
  nodeId: string;
  nodeLabel: string;
  region: string;
  egressIp: string;
  protocol: ProxyProtocol;
  gatePort: number;
  ttlSeconds: number;
  expiresAt: string;
  requests: number;
  bytes: number;
  sticky: boolean;
  status: LeaseStatus;
  idempotencyKey: string;
  createdAt: string;
}

export interface CreateLeaseInput {
  userId: string;
  platform: Platform;
  accountId?: string;
  ttlSeconds?: number;
  idempotencyKey?: string;
}

export interface CreateLeaseResult {
  lease: Lease;
  warning?: string;
  replayed?: boolean;
}

export interface UsageBucket {
  day: string;
  requests: number;
  bytes: number;
  errors: number;
  avgLatencyMs: number;
}

export interface UsageAccount {
  account: string;
  requests: number;
  bytes: number;
  errors: number;
  avgLatencyMs: number;
  buckets: UsageBucket[];
  trafficBudgetBytes: number | null;
  usedBytes: number;
  remainingBytes: number | null;
}

export interface UsageUser {
  userId: string;
  userName: string;
  requests: number;
  bytes: number;
  quotaBytesMonth: number | null;
  usedBytes: number;
}

export interface UsageTarget {
  target: string;
  requests: number;
  bytes: number;
}

export interface UsageSummary {
  from: string;
  to: string;
  requests: number;
  bytes: number;
  errors: number;
  accounts: UsageAccount[];
  users: UsageUser[];
  days: UsageBucket[];
  topTargets: UsageTarget[];
  truncated: boolean;
  rowsRetained: number;
  retentionDays: number;
  quotaEnforced: boolean;
}

export interface Event {
  id: string;
  type: string;
  ts: string;
  data: Record<string, unknown>;
}

export interface EventsPage {
  events: Event[];
  nextCursor: string;
  oldestCursor: string;
  dropped: number;
}

export interface ListAccountsOptions {
  state?: AccountState;
  limit?: number;
  offset?: number;
}

export interface ServersOptions {
  q?: string;
  region?: string;
  status?: string;
  selected?: boolean;
  offset?: number;
  limit?: number;
  sort?: "order" | "title" | "host" | "status" | "region";
}

export interface ListOperationsOptions {
  account?: string;
  state?: OperationState;
  type?: string;
  limit?: number;
}

export interface ListLeasesOptions {
  userId?: string;
  limit?: number;
  offset?: number;
}

export interface UsageOptions {
  /** RFC 3339 timestamp or UTC date (YYYY-MM-DD). */
  from?: string;
  /** RFC 3339 timestamp or UTC date (YYYY-MM-DD). */
  to?: string;
  account?: string;
  userId?: string;
}

export interface ListEventsOptions {
  since?: string;
  limit?: number;
  /** Go duration string such as "25s"; the daemon caps it at 25s. */
  wait?: string;
}

export interface StreamEventsOptions {
  since?: string;
  /** Wins over since, mirroring a browser reconnect. */
  lastEventId?: string;
  signal?: AbortSignal;
  /** Bounds the wait for response headers only; the stream itself is untimed. */
  timeoutMs?: number;
}

export interface WaitOptions {
  intervalMs?: number;
  maxIntervalMs?: number;
  timeoutMs?: number;
  signal?: AbortSignal;
}
