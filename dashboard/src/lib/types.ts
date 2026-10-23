export type Tone = 'neutral' | 'mint' | 'success' | 'warning' | 'danger' | 'info'

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
  selected: boolean
  skippedByLimit: boolean
  memberOnly: boolean | null
  fullLoad: boolean
  blockingMarks: string[]
  hasPingSecret: boolean
  latencyMs: number | null
}

export interface AccountServers {
  enabled?: boolean
  reason?: string
  accountId: string
  fetchedAt: string | null
  stale: boolean
  servers: ServerView[]
}
