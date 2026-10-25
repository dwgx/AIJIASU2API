export type Tone = 'neutral' | 'mint' | 'success' | 'warning' | 'danger' | 'info'

/** Product status vocabulary: one name per meaning, one hue per name. */
export type Status = 'ok' | 'info' | 'warn' | 'error' | 'unavailable'

export type Platform = 'android' | 'ios' | 'windows' | 'linux'
export type AccountState = 'active' | 'paused' | 'auth_failed' | 'expired'
export type NodeState = 'LIVE' | 'READY' | 'STARTING' | 'DRAINING' | 'DOWN' | 'STALE'
export type ProxyProtocol = 'socks5' | 'http'
export type Severity = 'info' | 'warn' | 'error'
export type ThemeName = 'dark' | 'light'
export type Transport = 'httpupgrade' | 'rawtcp'
export type MetricRange = '24h' | '7d'
export type RequestSource = 'proxy' | 'gate' | 'mutation-lab'

export interface Account {
  id: string
  label: string
  username: string
  platform: Platform
  state: AccountState
  /** Manual lease cap enforced by this daemon, 1–2000. */
  concurrency: number
  /**
   * The vendor's purchased 同时连接数 (simultaneous-connection allowance).
   * Absent on daemons that predate the login-result field; treat it as 1.
   */
  useCount?: number
  /**
   * Lease capacity this daemon actually serves, derived as
   * `min(useCount, AJS_ACCOUNT_SLOTS)`. Absent on older daemons; default 1.
   */
  effectiveSlots?: number
  regions: string[]
  nodeCount: number
  liveNodeCount: number
  usageBytes: number
  /** Monthly gate-enforced traffic budget in bytes; null/absent is unlimited. */
  trafficBudgetBytes?: number | null
  expiresAt: string
  lastLoginAt: string
  createdAt: string
}

/**
 * One row of the vendor's public plan catalogue, captured from
 * `https://www.91ajs.com/purchase`. Prices are CNY list prices, not quotes.
 *
 * The live daemon (GET /v1/renewal/plans) also fills `label`, `months` and
 * `notes`; the static card snapshot predates that endpoint and carries only
 * the fields it renders, so the wire-only fields stay optional here.
 */
export interface RenewalPlan {
  /** Vendor period code: M1/M3/M12 months, H6 trial. */
  id: string
  /** Human period label published by the vendor, e.g. "1 month". */
  label?: string
  /** Whole months covered by the term; 0 for the trial. */
  months?: number
  /** Public list price in CNY. */
  priceCny: number
  /** Auto-renew list price when the vendor publishes a variant. */
  autoRenewPriceCny?: number | null
  /** CNY saved against paying month by month. */
  saveCny?: number
  /** A trial window rather than a renewable term. */
  trial?: boolean
  /** Vendor-side notes, e.g. "TrialPackage". */
  notes?: string
}

/** Where a plan catalogue came from: this call, memory, or the built-in table. */
export type RenewalSource = 'live' | 'cache' | 'static'

/** Time-to-expiry bucket for one account. */
export type RenewalUrgency = 'none' | 'soon' | 'due' | 'overdue'

/** GET /v1/renewal/plans — catalogue plus provenance; a failed refresh is soft. */
export interface RenewalPlansView {
  plans: RenewalPlan[]
  /** RFC 3339 instant of the last live fetch; the zero instant for `static`. */
  fetchedAt: string
  source: RenewalSource
  /** True when the snapshot outlived its TTL or is the static fallback. */
  stale: boolean
}

/** GET /v1/renewal/accounts/{id} — expiry state; the URL carries no credential. */
export interface RenewalAccountView {
  accountId: string
  /** RFC 3339 expiry echoed from the account row. */
  expiresAt: string
  /** Whole days to expiry, rounded away from zero; negative once past it. */
  daysLeft: number
  urgency: RenewalUrgency
  /** Always the vendor's public purchase page. */
  renewalUrl: string
}

export interface ProxyNode {
  id: string
  accountId: string
  accountLabel: string
  label: string
  region: string
  country: string
  city: string
  state: NodeState
  latencyMs: number
  successRate: number
  inUse: number
  capacity: number
  egressIp: string
  protocol: ProxyProtocol
  gatePort: number
  lastProbeAt: string
}

export type LeaseStatus = 'active' | 'expired' | 'revoked'

export interface Lease {
  id: string
  userId: string
  userName: string
  accountId: string
  accountLabel: string
  /** Account platform the lease was allocated for. */
  platform: Platform
  nodeId: string
  nodeLabel: string
  region: string
  egressIp: string
  protocol: ProxyProtocol
  gatePort: number
  ttlSeconds: number
  expiresAt: string
  requests: number
  bytes: number
  sticky: boolean
  status: LeaseStatus
  /** The create request's retry key; empty when none was sent. */
  idempotencyKey: string
  createdAt: string
}

/** POST /v1/leases body. ttlSeconds 0/absent uses the user's sticky TTL. */
export interface CreateLeaseInput {
  userId: string
  platform: Platform
  accountId?: string
  ttlSeconds?: number
  idempotencyKey?: string
}

/** POST /v1/leases response: the committed lease plus a publish warning. */
export interface CreateLeaseResult {
  lease: Lease
  /** Set when the lease committed but the optional publish hook failed. */
  warning?: string
  /** True when an Idempotency-Key matched an existing lease. */
  replayed?: boolean
}

/* --------------------------------------------------------------------------
 * Orders and plans (DESIGN-ORDERS.md)
 *
 * A plan is admin catalog configuration; an order stores an immutable priced
 * snapshot of it. `awaiting_payment` means nothing has been paid: only an
 * administrator attesting an offline receipt (or a verified webhook) settles
 * an order, and activation is a separate, retryable step.
 * ------------------------------------------------------------------------ */

export type OrderState =
  | 'awaiting_payment'
  | 'paid'
  | 'active'
  | 'cancelled'
  | 'refunded'
  | 'charged_back'

/** Admin catalog plan; `amountMinor` is integer minor units of `currency`. */
export interface Plan {
  id: string
  active: boolean
  name: string
  amountMinor: number
  currency: string
  leaseTtlSeconds: number
  quotaBytes: number
  createdAt: string
  updatedAt: string
}

/** The priced plan fields frozen onto an order at creation time. */
export interface OrderPlanSnapshot {
  id: string
  name: string
  amountMinor: number
  currency: string
  leaseTtlSeconds: number
  quotaBytes: number
}

/** One grant per order; a reversal sets `revokedAt` instead of deleting it. */
export interface EntitlementGrant {
  orderId: string
  leaseId: string
  quotaBytes: number
  previousQuotaBytes: number
  expiresAt: string
  revokedAt: string | null
}

/** The payment providers the daemon can name on an order. */
export type PaymentProvider = 'manual' | 'stripe' | 'epay'

/**
 * Admin-visible provider state. `unknown` means the daemon does not expose a
 * status field (older builds): the UI must not claim either state.
 */
export type EpayStatus = 'enabled' | 'disabled' | 'unknown'

export interface PaymentStatus {
  epay: EpayStatus
  reason?: string
}

/**
 * `reviewRequired` is a flag, not a state: a mismatch (late settlement,
 * conflicting reference, refund without recorded payment) quarantines the
 * order for offline reconciliation without granting anything.
 */
export interface Order {
  id: string
  tenantId: string
  userId: string
  planId: string
  plan: OrderPlanSnapshot
  provider: string
  state: OrderState
  providerRef: string | null
  reviewRequired: boolean
  createdAt: string
  paidAt: string | null
  activatedAt: string | null
  cancelledAt: string | null
  refundedAt: string | null
  grant: EntitlementGrant | null
}

export interface PanelUser {
  id: string
  name: string
  platform: string
  stickyTtl: string
  quotaBytesMonth: number | null
  usedBytes: number
  enabled: boolean
  allowlist: string[]
  createdAt: string
}

export interface ApiKey {
  id: string
  userId: string
  userName: string
  label: string
  prefix: string
  createdAt: string
  lastUsedAt: string | null
  revokedAt: string | null
}

export interface RequestLog {
  id: string
  ts: string
  userId: string
  userName: string
  accountLabel: string
  nodeLabel: string
  region: string
  /** Gate listener that relayed the request; absent when the daemon omits it. */
  protocol?: ProxyProtocol
  method: string
  target: string
  status: number
  bytes: number
  latencyMs: number
  source: RequestSource
}

export interface AuditEvent {
  id: string
  ts: string
  severity: Severity
  actor: string
  action: string
  target: string
  detail: string
}

export interface TrafficPoint {
  t: string
  ingress: number
  egress: number
}

export interface AccountHealth {
  accountId: string
  label: string
  score: number
  state: AccountState
}

export interface Metrics {
  range: MetricRange
  series: TrafficPoint[]
  totals: {
    requests: number
    egressBytes: number
    ingressBytes: number
    p95LatencyMs: number
  }
  accountHealth: AccountHealth[]
  /** False when the daemon has no traffic meter: series and totals are empty/zero. */
  telemetryAvailable: boolean
  generatedAt: string
}

export interface PanelSettings {
  transport: Transport
  healthIntervalSec: number
  leaseTtl: string
  subscriptionRefreshSec: number
  theme: ThemeName
  /** Optional additive flag: newer daemons may report 易支付 availability. */
  epayEnabled?: boolean
}

export interface CreateAccountInput {
  label: string
  username: string
  password: string
  platform: Platform
  concurrency: number
  regions: string[]
}

/* --------------------------------------------------------------------------
 * Account import / export / bulk delete — DESIGN-ACCOUNT-IO v1
 * ------------------------------------------------------------------------ */

export type ExportFormat = 'encrypted' | 'json' | 'csv'
export type ImportSourceKind = 'json' | 'csv' | 'encrypted'
export type ImportMode = 'atomic' | 'partial'
export type ImportMatchBy = 'label' | 'username_platform' | 'id'
export type ImportConflict = 'skip' | 'update' | 'rename' | 'fail'
export type ImportInitialState = 'paused' | 'active' | 'from_file'
export type ImportIdPolicy = 'new' | 'preserve'
export type ImportDevicePolicy = 'keep' | 'restore_if_missing' | 'replace'
export type AccountImportAction = 'create' | 'update' | 'skip' | 'rename' | 'fail'
export type BulkDeleteStatus = 'deleted' | 'would_delete' | 'not_found' | 'failed' | 'skipped'

/** Structured, non-secret notes attached to export rows and import results. */
export type AccountIOWarning =
  | 'password_kept'
  | 'password_unavailable'
  | 'device_kept'
  | 'device_restored'
  | 'device_generated_on_login'
  | 'device_replaced'
  | 'id_ignored'
  | 'platform_device_mismatch'
  | 'counts_mismatch'
  | 'unknown_column'
  | 'secret_orphaned'

/** Device identity as stored by the daemon; non-secret but vendor-visible. */
export interface AccountDevice {
  uid: string
  osdid: string
  cuid: string
  cseid: string
  osp: string
  osd: string
  osv: string
  dim: string
  version: string
  site: string
  hw?: string
  osLang?: string
  tzMin?: number
  lang?: string
  instMkt?: string
  vrf?: string
}

export interface AccountExportRow {
  id?: string
  label: string
  username: string
  password: string | null
  platform: Platform
  concurrency?: number
  regions?: string[]
  state?: AccountState
  expiresAt?: string
  lastLoginAt?: string
  createdAt?: string
  device?: AccountDevice | null
  warnings?: AccountIOWarning[]
}

export interface AccountExportDocument {
  format: 'ajs-accounts'
  version: 1
  exportedAt: string
  source?: { daemonVersion?: string; secretsSealed?: boolean }
  counts?: { accounts: number; passwords: number; devices: number }
  accounts: AccountExportRow[]
}

export interface AccountIOCapabilities {
  encryptedExport: boolean
  plaintextExport: boolean
  maxImportRows: number
  maxExportAccounts: number
  maxDeleteIDs: number
  maxBodyBytes: number
  maxImportBodyBytes: number
}

export interface AccountExportRequest {
  ids?: string[]
  filter?: { state?: AccountState; platform?: Platform; q?: string }
  format: ExportFormat
  passphrase?: string
  includeDevice?: boolean
  confirmPlaintext?: 'EXPORT'
}

export interface AccountExportDownload {
  blob: Blob
  filename: string
  counts: { accounts: number; missingSecrets: number; devices: number }
}

export interface AccountImportOptions {
  mode?: ImportMode
  matchBy?: ImportMatchBy
  onConflict?: ImportConflict
  initialState?: ImportInitialState
  idPolicy?: ImportIdPolicy
  devicePolicy?: ImportDevicePolicy
  preserveTimestamps?: boolean
  dryRun?: boolean
}

export interface AccountImportSource {
  kind: ImportSourceKind
  content: unknown
  passphrase?: string
}

export interface AccountImportRequest {
  source: AccountImportSource
  options: AccountImportOptions
}

export interface AccountImportCounts {
  total: number
  created: number
  updated: number
  skipped: number
  renamed: number
  failed: number
}

export interface AccountImportRowResult {
  row: number
  action: AccountImportAction
  id: string | null
  label: string
  existingId: string | null
  renamedTo: string | null
  warnings?: AccountIOWarning[]
  code: string | null
  message: string | null
}

export interface AccountImportResult {
  format: string
  version: number
  dryRun: boolean
  applied: boolean
  mode: ImportMode
  counts: AccountImportCounts
  results: AccountImportRowResult[]
  warnings?: AccountIOWarning[]
}

export interface BulkDeleteRequest {
  ids: string[]
  mode?: 'continue' | 'stop'
  dryRun?: boolean
}

export interface BulkDeleteCounts {
  total: number
  deleted: number
  wouldDelete: number
  notFound: number
  failed: number
  skipped: number
}

export interface BulkDeleteItem {
  id: string
  label: string | null
  status: BulkDeleteStatus
  nodes: number
  leases: number
  code: string | null
  message: string | null
  warnings?: AccountIOWarning[]
}

export interface BulkDeleteResult {
  format: string
  version: number
  dryRun: boolean
  mode: string
  counts: BulkDeleteCounts
  results: BulkDeleteItem[]
}

export interface CreateKeyInput {
  userId: string
  label: string
}

export interface CreateKeyResult {
  key: ApiKey
  secret: string
}

/* --------------------------------------------------------------------------
 * Scoped keys — daemon/internal/model/model.go
 * ------------------------------------------------------------------------ */

/**
 * The OpenAPI Scope enum. `accounts:write` is part of the daemon contract even
 * though the first published enum only listed `accounts:read`; the platform
 * reads are separate grants so a monitoring key never needs account access.
 */
export const SCOPES = [
  'accounts:read',
  'accounts:write',
  'catalog:read',
  'catalog:write',
  'leases:read',
  'leases:write',
  'requests:read',
  'audit:read',
  'metrics:read',
  'lab:read',
  'settings:read',
  'settings:write',
  'usage:read',
  'renewal:read',
  'health:read',
  'events:read',
  'admin',
] as const

export type Scope = (typeof SCOPES)[number]

/** One least-privilege key; the secret is never part of a read. */
export interface ScopedKey {
  id: string
  userId: string
  userName: string
  label: string
  prefix: string
  scopes: Scope[]
  /** Empty means every account. */
  accountIds: string[]
  /** Empty means every region. */
  regions: string[]
  /** 0 means unlimited. */
  rateLimitPerMin: number
  createdAt: string
  expiresAt: string | null
  lastUsedAt: string | null
  revokedAt: string | null
}

/**
 * POST /v1/scoped-keys body. An absent rate limit takes the daemon default
 * (60/min); an explicit 0 selects unlimited. An empty expiresAt means the key
 * does not expire.
 */
export interface ScopedKeyCreateInput {
  userId: string
  label: string
  scopes: Scope[]
  accountIds?: string[]
  regions?: string[]
  rateLimitPerMin?: number
  expiresAt?: string
}

/** The one-time create response; `secret` is returned exactly once. */
export interface ScopedKeyCreated {
  key: ScopedKey
  secret: string
}

export interface MutationLabInput {
  method: string
  target: string
  nodeId: string
  body: string
}

export interface UpdateInfo {
  enabled: boolean
  current: string
  latest?: string
  updateAvailable?: boolean
  url?: string
  name?: string
  publishedAt?: string
  checkedAt?: string
}

/* --------------------------------------------------------------------------
 * Lab — read-only telemetry contracts (DASHBOARD-GAPS §6.3)
 *
 * Every object carries optional `enabled`/`reason` so a disabled daemon
 * dependency can answer 200 with an explanation instead of an error.
 * ------------------------------------------------------------------------ */

export type LabSeverity = 'info' | 'warn' | 'error'

export interface LabWarning {
  code: string
  severity: LabSeverity
  message: string
}

export interface LabDisabled {
  enabled: false
  reason: string
}

export interface LabModes {
  accountLogin: boolean
  resin: boolean
  updateCheck: boolean
  secrets: boolean
  certPin: string
}

export interface LabCounts {
  accounts: number
  nodes: number
  liveNodes: number
  sessions: number
  gates: number
  leases: number
}

export interface LabOverview {
  enabled?: boolean
  reason?: string
  generatedAt: string
  version: string
  commit: string
  uptimeSec: number
  modes: LabModes
  counts: LabCounts
  warnings: LabWarning[]
}

export interface GateView {
  nodeId: string
  accountId: string
  state: NodeState | string
  port: number
  active: number
  limit: number
  accepted: number
  rejected: number
  ingressBytes: number
  egressBytes: number
  draining: boolean
  startedAt: string | null
}

/**
 * One finished flow from the local gate ring. Targets are host plus port only:
 * the gate records no URL, headers, credentials or full session id.
 */
export interface FlowView {
  id: string
  nodeId: string
  accountId: string
  /** Redacted server-side to "first4…(len)". */
  sid?: string
  startedAt: string
  durationMs: number
  method: string
  targetHost: string
  targetPort: number
  /** Gate vocabulary: "ok" or "rejected". Never an HTTP status. */
  status: string
  bytesIn: number
  bytesOut: number
  errorClass?: string
  /** Gate listener protocol; absent when the daemon does not report it. */
  protocol?: ProxyProtocol
  /** Operator whose lease the gate relayed; absent for unattributed flows. */
  userId?: string
  userName?: string
}

/** GET /v1/lab/flows — bounded per-gate rings joined to their nodes. */
export interface FlowsView {
  enabled: boolean
  reason?: string
  flows: FlowView[]
  dropped: number
}

export interface KeepaliveView {
  supported: boolean
  ka: string | null
  kat: string | null
  kapf: string | null
}

export interface SessionView {
  nodeId: string
  accountId: string
  serverId: string
  /** Redacted server-side: a short prefix plus the original length. */
  sid: string
  healthy: boolean
  ageSec: number
  lastFlowAt: string | null
  egressClaimed: string
  keepalive: KeepaliveView
  errorClass?: string | null
}

export interface EgressView {
  nodeId: string
  claimed: string
  verified: string
  match: boolean | null
  source: string
  checkedAt: string | null
}

/**
 * One guarded egress observation from POST /v1/lab/egress-check. `match` is
 * null when either side is empty; a probe failure still returns 200 with
 * `error` set and `verified` empty.
 */
export interface EgressCheckResult {
  nodeId: string
  claimed: string
  verified: string
  match: boolean | null
  latencyMs: number
  checkedAt: string
  source: string
  error?: string
}

export interface ProfileView {
  name: string
  clientVersion: string
  coreVersion: string
  protocolVersion: number
  oaepHash: string
  signingMode: string
  requestShape: string
  transport: string
}

export interface SampleProfileView {
  platform: string
  version: string
  core: string
  source: string
}

export interface LabProfiles {
  enabled?: boolean
  reason?: string
  active: string
  supported: ProfileView[]
  samples: SampleProfileView[]
}

export interface DeviceView {
  enabled?: boolean
  reason?: string
  accountId: string
  present: boolean
  hashes: Record<string, string>
  claims: Record<string, string>
  stable: boolean
  firstSeenAt: string | null
  rawAvailable: boolean
}

export interface FlagView {
  key: string
  value?: string
  set?: boolean
  source?: string
  secret: boolean
  redacted?: boolean
  restartRequired: boolean
}

export interface LabFlags {
  enabled?: boolean
  reason?: string
  flags: FlagView[]
}

export type LabLogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'

export interface LogEntry {
  ts: string
  level: LabLogLevel | string
  component: string
  msg: string
  accountId?: string
  nodeId?: string
  errorClass?: string
}

export interface LabLogs {
  enabled?: boolean
  reason?: string
  entries: LogEntry[]
  dropped: number
}

export interface LabLogQuery {
  level?: string
  component?: string
  limit?: number
  since?: string
}

export interface DiagnosticsView {
  enabled?: boolean
  reason?: string
  health: Record<string, string>
  state: {
    path: string
    lastSavedAt: string | null
    sizeBytes: number | null
  }
  build: {
    version: string
    goVersion: string
    commit: string
  }
  warnings: LabWarning[]
}

export interface ServerView {
  id: string
  title: string
  region: string
  host: string
  port: number
  status: string
  nodeId: string | null
  nodeState: NodeState | string | null
  /**
   * P0 field: "a node exists for this server", not the operator's target.
   * P1 reads {@link desired} for the persisted target.
   */
  selected: boolean
  skippedByLimit: boolean
  memberOnly: boolean | null
  /** `null` when the vendor payload carries no marks/load (P1 backend). */
  fullLoad: boolean | null
  blockingMarks: string[]
  hasPingSecret: boolean
  latencyMs: number | null
  /** P1 overlay: the runtime has a gate for this server (LIVE or DRAINING). */
  connected?: boolean
  /** P1 overlay: the gate is LIVE and not draining. */
  serving?: boolean
  /** P1 overlay: this server is the persisted operator target. */
  desired?: boolean
  /** P1 overlay: derived by the daemon from the fields above. */
  eligible?: boolean
  ineligibleReason?: string
  /** In the effective `AJS_NODE_LIMIT` set; not the same as eligible. */
  autoStarted?: boolean
  runtimeWarning?: 'gate_missing' | 'orphan_gate' | 'runtime_unavailable' | string
}

/**
 * One persisted target per account: the server the operator wants the next
 * login sync to use. A target is not a session; it never starts a connect.
 */
export interface CatalogSelection {
  accountId: string
  serverId: string
  title: string
  updatedAt: string
  updatedBy: string
}

/** 200 response for POST /v1/accounts/{id}/actions/connect and /reconnect. */
export interface ConnectResult {
  accountId: string
  serverId: string
  nodeId: string
  nodeState: NodeState | string
  gatePort: number
  egressIp: string
  switchedFrom?: string
  durationMs: number
  warnings: string[]
  auditId?: string
  alreadyConnected?: boolean
}

/** 200 response for POST /v1/accounts/{id}/actions/disconnect. */
export interface DisconnectResult {
  accountId: string
  disconnected: boolean
  alreadyDisconnected?: boolean
  warnings?: string[]
  auditId?: string
}

/** One entry of the catalog's per-region summary (all filtered rows). */
export interface RegionCount {
  code: string
  count: number
}

export type ServerCatalogSort = 'order' | 'title' | 'host' | 'status' | 'region'

/**
 * Query accepted by `GET /v1/accounts/{id}/servers`. The daemon defaults to
 * limit 50 (max 500); an empty field means "no filter".
 */
export interface ServerCatalogQuery {
  q?: string
  region?: string
  status?: string
  selected?: boolean
  /** GB/T 2260 two-digit province code, `00` for the unresolved bucket. */
  province?: string
  /** GB/T 2260 city code from the bundled geo asset. */
  city?: string
  offset?: number
  limit?: number
  sort?: ServerCatalogSort
}

/** One resolved city inside a server-side province bucket. */
export interface CatalogGeoCity {
  code: string
  /** The vendor title prefix, e.g. `济南 - 莱芜`. */
  label: string
  count: number
}

/** Per-province aggregate over the rows matching the search/region/status. */
export interface CatalogGeoProvince {
  code: string
  count: number
  connected: number
  cities: CatalogGeoCity[]
}

/** A running catalog row named by the geo summary. */
export interface CatalogGeoRow {
  id: string
  title: string
  nodeState: ServerView['nodeState']
  cityCode?: string
  provinceCode?: string
}

/**
 * Server-side geography summary carried by every catalog page. The dashboard
 * draws the map from this instead of downloading the retained catalog; the
 * `snapshot` path stays as the P0 fallback for daemons without it.
 */
export interface CatalogGeo {
  total: number
  unknown: number
  provinces: CatalogGeoProvince[]
  current: CatalogGeoRow | null
  live: CatalogGeoRow[]
}

export interface AccountServers {
  enabled?: boolean
  reason?: string
  accountId: string
  /** Changes only on login/forget; sent back with target/connect writes. */
  snapshotId?: string
  fetchedAt: string | null
  stale: boolean
  /** Every row matching the filters, not just this page. */
  total: number
  offset: number
  limit: number
  nextOffset: number
  hasMore: boolean
  /** Counts over all filtered rows, sorted by region code. */
  regions: RegionCount[]
  /** Map aggregate; absent on a P0 daemon, which falls back to the snapshot. */
  geo?: CatalogGeo
  /** One page of the filtered catalog. */
  servers: ServerView[]
}

/**
 * Every retained row for one account, assembled from the paged endpoint by the
 * dashboard. The China map and province filters need the whole catalog; the
 * table keeps using the server's page envelope.
 */
export interface CatalogSnapshot {
  enabled: boolean
  reason?: string
  accountId: string
  snapshotId?: string
  fetchedAt: string | null
  stale: boolean
  total: number
  servers: ServerView[]
}

/* --------------------------------------------------------------------------
 * Schedules — daemon/internal/model/schedule.go
 *
 * The live daemon is the contract: action values are the model's closed set,
 * previews carry `{at,utc,inWindow}`, and a run carries `state`/`result`/
 * `errorClass`. `system:renewal-sweep` is a daemon-owned pseudo schedule: it
 * has runs but no row in GET /v1/schedules on this daemon, so the page derives
 * its reminder card from account expiry and marks it read-only.
 * ------------------------------------------------------------------------ */

export type ScheduleAction =
  | 'account.switch'
  | 'account.rotate'
  | 'account.reconnect'
  | 'account.login'
  | 'account.logout'
  | 'node.drain'
  | 'node.undrain'
  | 'lease.rotate'
  | 'subscription.refresh'
  | 'egress.check'
  | 'account.renewal_reminder'

export type ScheduleTriggerKind = 'cron' | 'interval' | 'once'
export type MisfirePolicy = 'skip' | 'run_once'
export type ConcurrencyPolicy = 'forbid' | 'queue'
export type ScheduleRunState =
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'skipped'
  | 'dry_run'
  | 'unknown'
export type ScheduleResult = 'ok' | 'skipped' | 'error' | 'dry_run' | 'unknown'

export interface ScheduleTarget {
  kind: string
  id?: string
  /** Action-specific body; strict-decoded by the daemon. */
  params?: Record<string, unknown> | null
}

export interface ScheduleTrigger {
  kind: ScheduleTriggerKind
  /** Five-field cron when kind=cron. */
  expr?: string
  /** 60..2592000 when kind=interval. */
  everySec?: number
  jitterSec?: number
  /** RFC 3339 instant when kind=once. */
  at?: string
  /** IANA name; empty means UTC. */
  timezone?: string
}

/** 0=Sunday..6=Saturday; an empty `days` means every day. */
export interface ScheduleWindow {
  days: number[]
  start: string
  end: string
}

export interface ScheduleGuard {
  cooldownSec: number
  maxRunsPerDay: number
  /** Daemon default: true. A destructive action only arms with confirm:"ARM". */
  dryRun: boolean
}

export interface Schedule {
  id: string
  revision: number
  name: string
  enabled: boolean
  owner: string
  action: ScheduleAction
  target: ScheduleTarget
  trigger: ScheduleTrigger
  window?: ScheduleWindow | null
  guard: ScheduleGuard
  misfirePolicy?: MisfirePolicy
  misfireGraceSec?: number
  concurrencyPolicy?: ConcurrencyPolicy
  createdAt: string
  updatedAt: string
  lastRunAt?: string
  lastScheduledAt?: string
  lastResult?: ScheduleResult
  lastError?: string
  nextRunAt?: string
  runCountToday: number
  consecutiveFailures: number
  pausedReason?: string
  degraded: boolean
  cursor?: number
}

export interface ScheduleRun {
  id: string
  scheduleId: string
  fireKey?: string
  /** cron|interval|once|manual */
  trigger: string
  scheduledFor?: string
  startedAt: string
  finishedAt?: string
  state: ScheduleRunState
  result?: ScheduleResult
  reason?: string
  errorClass?: string
  detail?: string
  auditId?: string
  operationId?: string
  actor?: string
}

export interface SchedulePreviewRun {
  at: string
  utc: string
  inWindow: boolean
}

export interface SchedulePreview {
  serverTime: string
  runs: SchedulePreviewRun[]
  warnings: string[]
}

export interface ScheduleCreateInput {
  name: string
  enabled?: boolean
  action: ScheduleAction
  target: ScheduleTarget
  trigger: ScheduleTrigger
  window?: ScheduleWindow
  guard?: Partial<ScheduleGuard>
  misfirePolicy?: MisfirePolicy
  misfireGraceSec?: number
  concurrencyPolicy?: ConcurrencyPolicy
  /** Deliberate-arm token; the daemon requires it for an armed destructive job. */
  confirm?: 'ARM'
}

export interface SchedulePatchInput {
  name?: string
  enabled?: boolean
  action?: ScheduleAction
  target?: ScheduleTarget
  trigger?: ScheduleTrigger
  window?: ScheduleWindow
  guard?: Partial<ScheduleGuard>
  misfirePolicy?: MisfirePolicy
  misfireGraceSec?: number
  concurrencyPolicy?: ConcurrencyPolicy
  confirm?: 'ARM'
}

export interface ScheduleRunInput {
  /** Required for a manual run of an armed destructive action. */
  confirm?: 'RUN'
  dryRun?: boolean
  force?: boolean
}

export interface SchedulePreviewInput {
  action?: ScheduleAction
  target?: ScheduleTarget
  trigger: ScheduleTrigger
  window?: ScheduleWindow
}

/** A daemon that reports the scheduler off; reads stay available. */
export interface SchedulesDisabled {
  enabled: false
  reason: string
  schedules?: Schedule[]
}

/**
 * GET /v1/schedules is a bare array on the deployed daemon. A newer daemon may
 * wrap it as `{enabled,reason,schedules}`; the page accepts both so an off
 * scheduler can explain itself instead of looking like an empty list.
 */
export type SchedulesPayload = Schedule[] | SchedulesDisabled

/* --------------------------------------------------------------------------
 * Usage — daemon/internal/usage/usage.go
 *
 * GET /v1/usage aggregates the request log over an inclusive UTC window. All
 * day boundaries are UTC calendar days and every account bucket covers the
 * whole window, so a chart can render a continuous series.
 * ------------------------------------------------------------------------ */

/** One UTC day of usage. */
export interface UsageBucket {
  /** `YYYY-MM-DD` in UTC. */
  day: string
  requests: number
  bytes: number
  errors: number
  avgLatencyMs: number
}

/** Per-account roll-up; `buckets` covers every day in the window. */
export interface UsageAccount {
  /** Account label; the daemon maps an account id to its label. */
  account: string
  requests: number
  bytes: number
  errors: number
  avgLatencyMs: number
  buckets: UsageBucket[]
  /**
   * Monthly gate-enforced traffic budget in bytes; null means unlimited.
   * Sent by a quota-aware daemon; absent on older ones.
   */
  trafficBudgetBytes?: number | null
  /** Gate-metered bytes for the current UTC month, independent of the window. */
  usedBytes?: number
  /** Budget headroom for the current UTC month, clamped at zero; null if unlimited. */
  remainingBytes?: number | null
}

/** Per-user roll-up; `usedBytes` is the byte total inside the window. */
export interface UsageUser {
  userId: string
  userName: string
  requests: number
  bytes: number
  quotaBytesMonth: number | null
  usedBytes: number
}

/** One host-only target label; never carries a scheme, path, query or port. */
export interface UsageTarget {
  target: string
  requests: number
  bytes: number
}

/** GET /v1/usage. `truncated` reports that the row cap clipped the window. */
export interface UsageSummary {
  /** RFC 3339 window bounds after the daemon applies its defaults. */
  from: string
  to: string
  requests: number
  bytes: number
  errors: number
  accounts: UsageAccount[]
  users: UsageUser[]
  days: UsageBucket[]
  topTargets: UsageTarget[]
  truncated: boolean
  /**
   * Rows the daemon keeps for aggregation (newest first). Windows wider than
   * this history read as partially empty; the UI must say so instead of
   * implying the requested span is fully covered.
   */
  rowsRetained?: number
  /** Whole days the retained rows cover, derived from the oldest retained row. */
  retentionDays?: number
  /**
   * True when the gate enforces a monthly traffic budget for at least one
   * account in the response. The account rows then carry live monthly meters.
   */
  quotaEnforced?: boolean
}

/** Query for GET /v1/usage; empty fields are omitted from the request. */
export interface UsageQuery {
  /** RFC 3339 timestamp or `YYYY-MM-DD`; defaults to 30 days before `to`. */
  from?: string
  /** RFC 3339 timestamp or `YYYY-MM-DD`; defaults to the daemon clock. */
  to?: string
  /** Account id or label; the daemon resolves an id to its label. */
  account?: string
  /** Exact user id. */
  userId?: string
}

/* --------------------------------------------------------------------------
 * Health — daemon/internal/health/health.go
 *
 * GET /v1/health/nodes samples loopback gates and, when AJS_EGRESS_PROBE=1,
 * measures HTTP egress through each live gate. Percentiles are computed over
 * successful probes; `last` is the newest sample regardless of outcome.
 * ------------------------------------------------------------------------ */

export type NodeHealthState = 'ok' | 'degraded' | 'down'
export type HealthErrorClass = 'timeout' | 'refused' | 'unreachable' | 'other'

/** One loopback probe. `at` is the instant the probe started. */
export interface HealthSample {
  /** RFC 3339 with nanoseconds. */
  at: string
  latencyMs: number
  ok: boolean
  /** Empty/absent when `ok` is true. */
  errorClass?: HealthErrorClass | string
}

/** One watched node's retained-window aggregate plus its newest sample. */
export interface NodeHealth {
  nodeId: string
  accountId?: string
  serverId?: string
  label?: string
  state: NodeHealthState
  count: number
  ok: number
  /** OK/Count over the whole window, 0..1. */
  successRate: number
  p50Ms: number
  p95Ms: number
  minMs: number
  maxMs: number
  consecutiveFailures: number
  last?: HealthSample
  /** Absent when the daemon does not run the through-gate probe. */
  egress?: NodeEgressHealth
}

/** Through-gate failure classes; `http` means the target answered outside 2xx. */
export type EgressErrorClass =
  | 'timeout'
  | 'refused'
  | 'unreachable'
  | 'proxy'
  | 'http'
  | 'other'

/** One HTTP exchange through the node's gate. `at` is when the probe started. */
export interface EgressSample {
  /** RFC 3339 with nanoseconds. */
  at: string
  /** SOCKS5 handshake, TLS and response, end to end. */
  latencyMs: number
  ok: boolean
  /** Target HTTP status; absent when no response was read. */
  status?: number
  /** Empty/absent when `ok` is true. */
  errorClass?: EgressErrorClass | string
}

/**
 * One node's egress aggregate. `enabled:false` names the switch that turned
 * sampling off instead of pretending a zero aggregate was measured.
 */
export interface NodeEgressHealth {
  enabled: boolean
  reason?: string
  /** Absent when `enabled` is false. */
  state?: NodeHealthState
  count: number
  ok: number
  /** OK/Count over the whole window, 0..1. */
  successRate: number
  p50Ms: number
  p95Ms: number
  consecutiveFailures: number
  last?: EgressSample
}

/** GET /v1/health/nodes; `enabled:false` carries the daemon's reason. */
export interface HealthView {
  enabled: boolean
  reason?: string
  nodes: NodeHealth[]
}

/* --------------------------------------------------------------------------
 * Webhooks — daemon/internal/webhook/webhook.go
 *
 * The signing secret exists only in the create response. A read reports
 * `secretAvailable` so the panel can explain why an endpoint cannot be
 * re-enabled, and every delivery attempt is kept in a bounded ring.
 * ------------------------------------------------------------------------ */

/** The daemon's closed event catalogue; an empty subscription means every event. */
export const WEBHOOK_EVENTS = [
  'account.login',
  'account.logout',
  'account.expired',
  'account.renewal_due',
  'lease.created',
  'lease.revoked',
  'lease.expired',
  'node.connected',
  'node.disconnected',
  'node.switched',
  'schedule.fired',
  'schedule.failed',
  'device.retired',
  'order.created',
  'order.cancelled',
  'order.paid',
  'order.activated',
  'order.refunded',
  'order.charged_back',
  'audit.critical',
] as const

export type WebhookEvent = (typeof WEBHOOK_EVENTS)[number]

/**
 * One retained event from GET /v1/events. The daemon scrubs credential-shaped
 * data before it enters the ring; `ts` is an RFC 3339 instant.
 */
export interface EventRecord {
  /** Monotonic cursor such as evt-000123; pass it back as `since`. */
  id: string
  type: WebhookEvent
  ts: string
  data?: Record<string, unknown>
}

/** GET /v1/events — one replay/long-poll page of the retained ring. */
export interface EventsPage {
  /** Oldest-first; a client prepends the reversed page to a newest-first list. */
  events: EventRecord[]
  /** Cursor of the newest returned event, or the request cursor when empty. */
  nextCursor: string
  /** Oldest cursor the ring still retains; the restart point after CURSOR_EXPIRED. */
  oldestCursor: string
  /** Events this subscriber lost to a full buffer since it connected. */
  dropped: number
}

/** Query for GET /v1/events; empty fields are omitted from the request. */
export interface EventsQuery {
  /** Retained event id to replay after; empty starts at the oldest retained event. */
  since?: string
  /** Page size; the daemon defaults to 100 and clamps at 500. */
  limit?: number
  /** Go duration such as `20s`; absent means no long poll. */
  wait?: string
}

export type WebhookDeliveryStatus = 'success' | 'failed' | 'dead_letter'

/** One persisted receiver. The signing secret is never part of a read. */
export interface WebhookEndpoint {
  id: string
  url: string
  /** Empty/absent subscribes to every event. */
  events?: WebhookEvent[]
  enabled: boolean
  description?: string
  /** RFC 3339 timestamp. */
  createdAt: string
  /** Whether the sealed signing secret can still be opened. */
  secretAvailable: boolean
}

/** POST /v1/webhooks body. An absent `enabled` defaults to true. */
export interface WebhookCreate {
  url: string
  /** Empty/absent subscribes to every event. */
  events?: WebhookEvent[]
  description?: string
  enabled?: boolean
}

/** POST /v1/webhooks response; `secret` is returned exactly once. */
export interface WebhookCreated extends WebhookEndpoint {
  secret: string
}

/** PATCH /v1/webhooks/{id} body; only `enabled` is mutable. */
export interface WebhookPatch {
  enabled: boolean
}

/** One delivery attempt from the persisted history merged with the live ring. */
export interface Delivery {
  id: string
  endpointId: string
  event: WebhookEvent
  attempt: number
  status: WebhookDeliveryStatus
  /** Receiver HTTP status; absent when the attempt never got a response. */
  httpStatus?: number
  error?: string
  /** RFC 3339 timestamp. */
  at: string
  durationMs: number
}

/** Query for GET /v1/webhooks/{id}/deliveries; the daemon pages newest first. */
export interface WebhookDeliveriesQuery {
  /** Page size; the daemon defaults to 200 and clamps a larger value to 500. */
  limit?: number
}

/* --------------------------------------------------------------------------
 * Audit export — daemon/internal/api/audit_export.go
 * ------------------------------------------------------------------------ */

/**
 * Query for GET /v1/audit/export.csv. Both bounds are inclusive RFC 3339
 * timestamps; the daemon defaults to the last seven days and refuses a window
 * longer than 366 days.
 */
export interface AuditExportQuery {
  from?: string
  to?: string
  severity?: Severity
}

/** One downloaded CSV. `truncated` mirrors the X-Truncated response header. */
export interface AuditExportDownload {
  blob: Blob
  filename: string
  truncated: boolean
}

/* --------------------------------------------------------------------------
 * Backup — daemon/internal/backup/backup.go and daemon/internal/api/backup.go
 * ------------------------------------------------------------------------ */

export interface BackupKdfParams {
  algo: string
  iter: number
  /** Base64 salt. */
  salt: string
}

/** The unencrypted envelope header. `payload` is AES-256-GCM ciphertext. */
export interface BackupEnvelope {
  version: number
  createdAt: string
  app: string
  kdf: BackupKdfParams
  cipher: string
  nonce: string
  payload: string
}

export interface BackupManifestEntry {
  name: string
  size: number
  sha256: string
  /** A required blob must be present when the envelope is opened. */
  required: boolean
}

export interface BackupManifest {
  entries: BackupManifestEntry[]
}

/** POST /v1/backup/inspect — the manifest exists only with a passphrase. */
export interface BackupInspectView {
  version: number
  supported: boolean
  app: string
  createdAt: string
  envelopeBytes: number
  manifest?: BackupManifest
}

/** POST /v1/backup — the file plus its server-supplied name and blob count. */
export interface BackupDownload {
  blob: Blob
  filename: string
  blobs: number
}

/** The dry-run comparison counts; names are sorted. */
export interface BackupDiff {
  added: number
  removed: number
  changed: number
  same: number
  addedBytes: number
  removedBytes: number
  changedBytes: number
  sameBytes: number
  addedNames: string[]
  removedNames: string[]
  changedNames: string[]
  sameNames: string[]
}

/** POST /v1/backup/restore body. `dryRun` defaults to true on the daemon. */
export interface BackupRestoreInput {
  envelope: string | Record<string, unknown>
  passphrase: string
  dryRun?: boolean
  /** Must be "RESTORE" for an apply. */
  confirm?: string
}

export interface BackupRestoreView {
  dryRun: boolean
  applied: boolean
  /** True after an apply: the running daemon must be restarted. */
  restartRequired: boolean
  manifest: BackupManifest
  diff: BackupDiff
  /** Blobs an apply wrote; empty for a dry run. */
  files?: string[]
  warning?: string
}

/* --------------------------------------------------------------------------
 * Device pool — daemon/internal/store/device_pool.go and api/device_pool.go
 *
 * A record carries hashes only: machineCode and the raw identity never leave
 * the daemon. `hashes` is keyed uid / osdid / cuid / cseid / machine with
 * sha256 prefixes; `claims` is public vendor metadata (site, version, osp…).
 * ------------------------------------------------------------------------ */

export type DevicePoolState = 'available' | 'assigned' | 'retired' | 'quarantined'

/** The three compiled-in client profiles; the family decides the identity format. */
export type DevicePoolProfile =
  | 'linux-4.2.3.0-binary'
  | 'linux-4.3.0.1-binary'
  | 'openajs-4.11.10.0-rawtcp'

/** `import/v1` and `legacy/v1` label records the daemon did not generate. */
export type DevicePoolAlgorithm =
  | 'random/v1'
  | 'hmac-sha256/v1'
  | 'import/v1'
  | 'legacy/v1'

export type DevicePoolExportScope = 'assigned' | 'retired' | 'all'

/** The exact confirm token each lifecycle route requires. */
export type DevicePoolConfirmAction = 'release' | 'retire' | 'quarantine' | 'purge'

export type DeviceImportConflictReason = 'duplicate_identity' | 'fingerprint_mismatch'

export const DEVICE_POOL_STATES: readonly DevicePoolState[] = [
  'available',
  'assigned',
  'retired',
  'quarantined',
]

export const DEVICE_POOL_PROFILES: readonly DevicePoolProfile[] = [
  'linux-4.2.3.0-binary',
  'linux-4.3.0.1-binary',
  'openajs-4.11.10.0-rawtcp',
]

/** Generators an operator may select; import/legacy records are not new batches. */
export const DEVICE_POOL_GENERATORS: readonly Exclude<
  DevicePoolAlgorithm,
  'import/v1' | 'legacy/v1'
>[] = ['random/v1', 'hmac-sha256/v1']

export const DEVICE_CONFIRM_TOKENS: Record<DevicePoolConfirmAction, string> = {
  release: 'RELEASE',
  retire: 'RETIRE',
  quarantine: 'QUARANTINE',
  purge: 'PURGE',
}

/** The export dialog's confirm token; the daemon accepts it when supplied. */
export const DEVICE_EXPORT_CONFIRM = 'EXPORT'

/** One hash-only pool record (GET /v1/device-pool). */
export interface DevicePoolRecord {
  id: string
  label: string
  notes: string
  state: DevicePoolState
  platform: Platform
  profile: string
  algorithm: DevicePoolAlgorithm | string
  /** Full sha256:<64 hex> identity fingerprint. */
  fingerprint: string
  /** sha256:<16 hex> per field; raw values never leave the daemon. */
  hashes: Record<string, string>
  claims: Record<string, string>
  accountId?: string
  accountLabel?: string
  accountKey?: string
  createdAt: string
  assignedAt: string | null
  presentedAt: string | null
  firstSeenAt: string | null
  lastSeenAt: string | null
  loginCount: number
  retiredAt: string | null
  retiredReason: string
  materialPresent: boolean
  warnings: string[]
}

/**
 * GET /v1/device-pool. A disabled pool is still a 200: `enabled:false` carries
 * the stable reason so the page can render the setup hint. `total` and
 * `counts` describe the whole pool, not the page.
 */
export interface DevicePoolView {
  enabled: boolean
  reason?: string
  total: number
  counts: Record<DevicePoolState, number> | Partial<Record<DevicePoolState, number>>
  devices: DevicePoolRecord[]
  warnings?: string[]
}

/** Query for GET /v1/device-pool; empty fields are omitted from the request. */
export interface DevicePoolQuery {
  state?: DevicePoolState
  platform?: Platform
  profile?: string
  accountId?: string
  q?: string
  limit?: number
  offset?: number
}

/** POST /v1/device-pool/generate — one all-or-nothing batch. */
export interface DeviceGenerateInput {
  count: number
  platform: Platform
  profile: DevicePoolProfile
  algorithm: Exclude<DevicePoolAlgorithm, 'import/v1' | 'legacy/v1'>
  labelPrefix?: string
}

export interface DeviceGenerateResult {
  devices: DevicePoolRecord[]
}

/** PATCH /v1/device-pool/{id} — free-text operator metadata only. */
export interface DevicePoolPatchInput {
  label?: string
  notes?: string
}

/** POST /v1/device-pool/export — the passphrase-encrypted envelope. */
export interface DevicePoolExportInput {
  passphrase: string
  scope?: DevicePoolExportScope
  deviceIds?: string[]
  includeAccountKeys?: boolean
  includeLabels?: boolean
  /** Optional; when supplied it must be exactly EXPORT. */
  confirm?: typeof DEVICE_EXPORT_CONFIRM
}

export interface DeviceEnvelopeKDF {
  name: string
  iterations: number
  /** Base64 salt. */
  salt: string
}

export interface DeviceEnvelopeCipher {
  name: string
  /** Base64 nonce. */
  nonce: string
}

/** The only device-pool representation that leaves the daemon. */
export interface DeviceExportEnvelope {
  format: 'ajs-device-pool' | string
  version: number
  createdAt: string
  kdf: DeviceEnvelopeKDF
  cipher: DeviceEnvelopeCipher
  payloadSha256: string
  ciphertext: string
  deviceCount: number
}

/** POST /v1/device-pool/import — dry run unless `dryRun` is false. */
export interface DevicePoolImportInput {
  passphrase: string
  envelope: string | DeviceExportEnvelope
  dryRun?: boolean
  bind?: boolean
  confirm?: string
}

/** One per-record import outcome. */
export interface DeviceImportDevice {
  deviceId: string
  state: DevicePoolState
  fingerprint: string
  action: 'added' | 'skipped' | string
}

/** An imported record whose account binding could not be resolved. */
export interface DeviceBindingConflict {
  deviceId: string
  accountId?: string
  reason: string
}

/** The dry-run and commit summary. */
export interface DeviceImportResult {
  dryRun: boolean
  format: string
  version: number
  added: number
  skipped: number
  conflicts: number
  bound: number
  devices: DeviceImportDevice[]
  bindingConflicts: DeviceBindingConflict[]
  warnings: string[]
}

/** One all-or-nothing import conflict; no record was mutated. */
export interface DeviceImportConflict {
  deviceId: string
  existingFingerprint: string
  importedFingerprint: string
  reason: DeviceImportConflictReason | string
}
