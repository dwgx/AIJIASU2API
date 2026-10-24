export type Tone = 'neutral' | 'mint' | 'success' | 'warning' | 'danger' | 'info'

/** Product status vocabulary: one name per meaning, one hue per name. */
export type Status = 'ok' | 'info' | 'warn' | 'error' | 'unavailable'

export type Platform = 'android' | 'ios' | 'windows' | 'linux'
export type AccountState = 'active' | 'paused' | 'auth_failed' | 'expired'
export type NodeState = 'LIVE' | 'READY' | 'STARTING' | 'DRAINING' | 'DOWN'
export type ProxyProtocol = 'socks5' | 'http'
export type Severity = 'info' | 'warn' | 'error'
export type ThemeName = 'dark' | 'light'
export type Transport = 'httpupgrade' | 'rawtcp'
export type MetricRange = '24h' | '7d'
export type RequestSource = 'proxy' | 'mutation-lab'

export interface Account {
  id: string
  label: string
  username: string
  platform: Platform
  state: AccountState
  concurrency: number
  regions: string[]
  nodeCount: number
  liveNodeCount: number
  usageBytes: number
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

export interface Lease {
  id: string
  userId: string
  userName: string
  accountId: string
  accountLabel: string
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
  createdAt: string
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
 * GET /v1/health/nodes samples loopback gates only. Percentiles, min and max
 * are computed over successful probes; `last` is the newest sample regardless
 * of outcome.
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
  'node.connected',
  'node.disconnected',
  'node.switched',
  'schedule.fired',
  'schedule.failed',
  'device.retired',
  'audit.critical',
] as const

export type WebhookEvent = (typeof WEBHOOK_EVENTS)[number]

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

/** One delivery attempt from the dispatcher's bounded ring. */
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
