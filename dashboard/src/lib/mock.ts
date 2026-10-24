import type {
  Account,
  AccountExportDocument,
  AccountExportRow,
  AccountIOCapabilities,
  AccountIOWarning,
  AccountImportOptions,
  AccountImportRequest,
  AccountImportResult,
  AccountImportRowResult,
  AccountServers,
  AccountState,
  ApiKey,
  AuditEvent,
  AuditExportQuery,
  BackupDiff,
  BackupInspectView,
  BackupManifest,
  BackupRestoreView,
  BulkDeleteItem,
  BulkDeleteResult,
  DeviceView,
  DiagnosticsView,
  EgressCheckResult,
  EgressView,
  EventRecord,
  EventsPage,
  EventsQuery,
  FlagView,
  FlowsView,
  GateView,
  LabFlags,
  LabLogQuery,
  LabLogs,
  LabOverview,
  LabProfiles,
  Lease,
  LogEntry,
  Metrics,
  PanelSettings,
  PanelUser,
  Platform,
  ProfileView,
  ProxyNode,
  RequestLog,
  ScopedKey,
  ScopedKeyCreateInput,
  ScopedKeyCreated,
  Scope,
  ServerCatalogQuery,
  ServerView,
  SessionView,
  TrafficPoint,
  WebhookEvent,
} from './types'
import { SCOPES, WEBHOOK_EVENTS } from './types'
import { ApiError } from './errors'
import { regionName } from './regions'

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

function mulberry32(seed: number): () => number {
  let t = seed
  return () => {
    t += 0x6d2b79f5
    let r = t
    r = Math.imul(r ^ (r >>> 15), r | 1)
    r ^= r + Math.imul(r ^ (r >>> 7), r | 61)
    return ((r ^ (r >>> 14)) >>> 0) / 4294967296
  }
}

function iso(ms: number): string {
  return new Date(ms).toISOString()
}

function pick<T>(rand: () => number, values: readonly T[]): T {
  return values[Math.floor(rand() * values.length)]
}

export interface MockDb {
  accounts: Account[]
  nodes: ProxyNode[]
  leases: Lease[]
  users: PanelUser[]
  keys: ApiKey[]
  scopedKeys: ScopedKey[]
  requests: RequestLog[]
  audit: AuditEvent[]
  settings: PanelSettings
  keySeq: number
  scopedKeySeq: number
  leaseSeq: number
  mutationSeq: number
  /** Epoch ms of the last guarded egress check per nodeId; reset with the dataset. */
  egressCheckedAt: Record<string, number>
}

export const DEFAULT_SETTINGS: PanelSettings = {
  transport: 'httpupgrade',
  healthIntervalSec: 60,
  leaseTtl: '8h',
  subscriptionRefreshSec: 30,
  theme: 'dark',
}

function buildAccounts(now: number): Account[] {
  return [
    {
      id: 'acc-01',
      label: 'Northwind Ops',
      username: 'demo-northwind',
      platform: 'android',
      state: 'active',
      concurrency: 200,
      regions: ['cn-zj', 'cn-sh', 'hk'],
      nodeCount: 3,
      liveNodeCount: 2,
      usageBytes: 82_400_000_000,
      expiresAt: iso(now + 18 * DAY),
      lastLoginAt: iso(now - 12 * MINUTE),
      createdAt: iso(now - 96 * DAY),
    },
    {
      id: 'acc-02',
      label: 'Field Lab',
      username: 'demo-fieldlab',
      platform: 'ios',
      state: 'active',
      concurrency: 80,
      regions: ['jp-tyo', 'sg-01'],
      nodeCount: 2,
      liveNodeCount: 2,
      usageBytes: 31_700_000_000,
      expiresAt: iso(now + 41 * DAY),
      lastLoginAt: iso(now - 3 * HOUR),
      createdAt: iso(now - 74 * DAY),
    },
    {
      id: 'acc-03',
      label: 'Edge APAC',
      username: 'demo-edgeapac',
      platform: 'linux',
      state: 'active',
      concurrency: 120,
      regions: ['hk', 'tw-tpe', 'kr-sel'],
      nodeCount: 3,
      liveNodeCount: 2,
      usageBytes: 57_100_000_000,
      expiresAt: iso(now + 9 * DAY),
      lastLoginAt: iso(now - 22 * MINUTE),
      createdAt: iso(now - 51 * DAY),
    },
    {
      id: 'acc-04',
      label: 'Partner EU',
      username: 'demo-partnereu',
      platform: 'windows',
      state: 'active',
      concurrency: 40,
      regions: ['de-fra', 'uk-lon'],
      nodeCount: 2,
      liveNodeCount: 2,
      usageBytes: 12_900_000_000,
      expiresAt: iso(now + 64 * DAY),
      lastLoginAt: iso(now - 1 * HOUR),
      createdAt: iso(now - 33 * DAY),
    },
    {
      id: 'acc-05',
      label: 'QA Sandbox',
      username: 'demo-qasandbox',
      platform: 'android',
      state: 'paused',
      concurrency: 20,
      regions: ['us-lax', 'ca-yyz'],
      nodeCount: 1,
      liveNodeCount: 0,
      usageBytes: 4_200_000_000,
      expiresAt: iso(now + 120 * DAY),
      lastLoginAt: iso(now - 2 * DAY),
      createdAt: iso(now - 21 * DAY),
    },
    {
      id: 'acc-06',
      label: 'Legacy HK',
      username: 'demo-legacyhk',
      platform: 'android',
      state: 'auth_failed',
      concurrency: 10,
      regions: ['hk'],
      nodeCount: 1,
      liveNodeCount: 0,
      usageBytes: 900_000_000,
      expiresAt: iso(now - 2 * DAY),
      lastLoginAt: iso(now - 5 * DAY),
      createdAt: iso(now - 140 * DAY),
    },
  ]
}

function buildNodes(now: number): ProxyNode[] {
  const seed: Array<Omit<ProxyNode, 'lastProbeAt'>> = [
    { id: 'node-01', accountId: 'acc-01', accountLabel: 'Northwind Ops', label: 'cn-zj-01', region: 'cn-zj', country: 'CN', city: 'Hangzhou', state: 'LIVE', latencyMs: 42, successRate: 99.4, inUse: 3, capacity: 20, egressIp: '203.0.113.11', protocol: 'socks5', gatePort: 13000 },
    { id: 'node-02', accountId: 'acc-01', accountLabel: 'Northwind Ops', label: 'cn-sh-01', region: 'cn-sh', country: 'CN', city: 'Shanghai', state: 'LIVE', latencyMs: 51, successRate: 98.7, inUse: 6, capacity: 20, egressIp: '203.0.113.12', protocol: 'socks5', gatePort: 13001 },
    { id: 'node-03', accountId: 'acc-01', accountLabel: 'Northwind Ops', label: 'hk-01', region: 'hk', country: 'HK', city: 'Hong Kong', state: 'READY', latencyMs: 78, successRate: 97.1, inUse: 0, capacity: 10, egressIp: '203.0.113.13', protocol: 'socks5', gatePort: 13002 },
    { id: 'node-04', accountId: 'acc-02', accountLabel: 'Field Lab', label: 'jp-tyo-01', region: 'jp-tyo', country: 'JP', city: 'Tokyo', state: 'LIVE', latencyMs: 96, successRate: 99.1, inUse: 2, capacity: 8, egressIp: '198.51.100.21', protocol: 'socks5', gatePort: 13003 },
    { id: 'node-05', accountId: 'acc-02', accountLabel: 'Field Lab', label: 'sg-01', region: 'sg-01', country: 'SG', city: 'Singapore', state: 'LIVE', latencyMs: 63, successRate: 99.8, inUse: 1, capacity: 8, egressIp: '198.51.100.22', protocol: 'socks5', gatePort: 13004 },
    { id: 'node-06', accountId: 'acc-03', accountLabel: 'Edge APAC', label: 'hk-02', region: 'hk', country: 'HK', city: 'Hong Kong', state: 'LIVE', latencyMs: 71, successRate: 98.2, inUse: 7, capacity: 12, egressIp: '198.51.100.31', protocol: 'socks5', gatePort: 13005 },
    { id: 'node-07', accountId: 'acc-03', accountLabel: 'Edge APAC', label: 'tw-tpe-01', region: 'tw-tpe', country: 'TW', city: 'Taipei', state: 'LIVE', latencyMs: 88, successRate: 96.4, inUse: 4, capacity: 12, egressIp: '198.51.100.32', protocol: 'socks5', gatePort: 13006 },
    { id: 'node-08', accountId: 'acc-03', accountLabel: 'Edge APAC', label: 'kr-sel-01', region: 'kr-sel', country: 'KR', city: 'Seoul', state: 'DRAINING', latencyMs: 104, successRate: 94.9, inUse: 2, capacity: 12, egressIp: '198.51.100.33', protocol: 'socks5', gatePort: 13007 },
    { id: 'node-09', accountId: 'acc-04', accountLabel: 'Partner EU', label: 'de-fra-01', region: 'de-fra', country: 'DE', city: 'Frankfurt', state: 'LIVE', latencyMs: 132, successRate: 99.0, inUse: 2, capacity: 4, egressIp: '192.0.2.41', protocol: 'http', gatePort: 13008 },
    { id: 'node-10', accountId: 'acc-04', accountLabel: 'Partner EU', label: 'uk-lon-01', region: 'uk-lon', country: 'GB', city: 'London', state: 'LIVE', latencyMs: 141, successRate: 98.5, inUse: 1, capacity: 4, egressIp: '192.0.2.42', protocol: 'http', gatePort: 13009 },
    { id: 'node-11', accountId: 'acc-05', accountLabel: 'QA Sandbox', label: 'us-lax-01', region: 'us-lax', country: 'US', city: 'Los Angeles', state: 'READY', latencyMs: 168, successRate: 95.2, inUse: 0, capacity: 4, egressIp: '192.0.2.51', protocol: 'socks5', gatePort: 13010 },
    { id: 'node-12', accountId: 'acc-06', accountLabel: 'Legacy HK', label: 'hk-03', region: 'hk', country: 'HK', city: 'Hong Kong', state: 'DOWN', latencyMs: 0, successRate: 0, inUse: 0, capacity: 2, egressIp: '192.0.2.61', protocol: 'socks5', gatePort: 13011 },
  ]
  return seed.map((node, index) => ({
    ...node,
    lastProbeAt: iso(now - (index + 1) * 47_000),
  }))
}
function buildLeases(now: number): Lease[] {
  const rows: Array<Omit<Lease, 'createdAt' | 'expiresAt'> & { createdAgo: number; expiresIn: number }> = [
    { id: 'lease-01', userId: 'u-01', userName: 'ada.ops', accountId: 'acc-01', accountLabel: 'Northwind Ops', nodeId: 'node-01', nodeLabel: 'cn-zj-01', region: 'cn-zj', egressIp: '203.0.113.11', protocol: 'socks5', gatePort: 13000, ttlSeconds: 8 * 3600, requests: 18_432, bytes: 4_200_000_000, sticky: true, createdAgo: 2.8 * HOUR, expiresIn: 5.2 * HOUR },
    { id: 'lease-02', userId: 'u-01', userName: 'ada.ops', accountId: 'acc-03', accountLabel: 'Edge APAC', nodeId: 'node-06', nodeLabel: 'hk-02', region: 'hk', egressIp: '198.51.100.31', protocol: 'socks5', gatePort: 13005, ttlSeconds: 8 * 3600, requests: 6_204, bytes: 1_100_000_000, sticky: true, createdAgo: 1.1 * HOUR, expiresIn: 6.9 * HOUR },
    { id: 'lease-03', userId: 'u-02', userName: 'build-bot', accountId: 'acc-02', accountLabel: 'Field Lab', nodeId: 'node-04', nodeLabel: 'jp-tyo-01', region: 'jp-tyo', egressIp: '198.51.100.21', protocol: 'socks5', gatePort: 13003, ttlSeconds: 3600, requests: 3_912, bytes: 780_000_000, sticky: false, createdAgo: 0.4 * HOUR, expiresIn: 0.6 * HOUR },
    { id: 'lease-04', userId: 'u-02', userName: 'build-bot', accountId: 'acc-02', accountLabel: 'Field Lab', nodeId: 'node-05', nodeLabel: 'sg-01', region: 'sg-01', egressIp: '198.51.100.22', protocol: 'socks5', gatePort: 13004, ttlSeconds: 3600, requests: 1_477, bytes: 310_000_000, sticky: false, createdAgo: 0.2 * HOUR, expiresIn: 0.8 * HOUR },
    { id: 'lease-05', userId: 'u-03', userName: 'qa.pipeline', accountId: 'acc-03', accountLabel: 'Edge APAC', nodeId: 'node-07', nodeLabel: 'tw-tpe-01', region: 'tw-tpe', egressIp: '198.51.100.32', protocol: 'socks5', gatePort: 13006, ttlSeconds: 24 * 3600, requests: 22_018, bytes: 6_800_000_000, sticky: true, createdAgo: 7.5 * HOUR, expiresIn: 16.5 * HOUR },
    { id: 'lease-06', userId: 'u-03', userName: 'qa.pipeline', accountId: 'acc-01', accountLabel: 'Northwind Ops', nodeId: 'node-02', nodeLabel: 'cn-sh-01', region: 'cn-sh', egressIp: '203.0.113.12', protocol: 'socks5', gatePort: 13001, ttlSeconds: 24 * 3600, requests: 9_301, bytes: 2_450_000_000, sticky: false, createdAgo: 3.2 * HOUR, expiresIn: 20.8 * HOUR },
    { id: 'lease-07', userId: 'u-04', userName: 'field.lab', accountId: 'acc-04', accountLabel: 'Partner EU', nodeId: 'node-09', nodeLabel: 'de-fra-01', region: 'de-fra', egressIp: '192.0.2.41', protocol: 'http', gatePort: 13008, ttlSeconds: 8 * 3600, requests: 2_884, bytes: 640_000_000, sticky: true, createdAgo: 5.4 * HOUR, expiresIn: 2.6 * HOUR },
    { id: 'lease-08', userId: 'u-04', userName: 'field.lab', accountId: 'acc-04', accountLabel: 'Partner EU', nodeId: 'node-10', nodeLabel: 'uk-lon-01', region: 'uk-lon', egressIp: '192.0.2.42', protocol: 'http', gatePort: 13009, ttlSeconds: 8 * 3600, requests: 1_190, bytes: 205_000_000, sticky: false, createdAgo: 1.9 * HOUR, expiresIn: 6.1 * HOUR },
  ]
  return rows.map(({ createdAgo, expiresIn, ...lease }) => ({
    ...lease,
    createdAt: iso(now - createdAgo),
    expiresAt: iso(now + expiresIn),
  }))
}

function buildUsers(now: number): PanelUser[] {
  return [
    { id: 'u-01', name: 'ada.ops', platform: 'ajs-ada', stickyTtl: '8h', quotaBytesMonth: 200_000_000_000, usedBytes: 64_000_000_000, enabled: true, allowlist: ['127.0.0.1/32', '10.20.0.0/16'], createdAt: iso(now - 88 * DAY) },
    { id: 'u-02', name: 'build-bot', platform: 'ajs-build', stickyTtl: '1h', quotaBytesMonth: 50_000_000_000, usedBytes: 12_400_000_000, enabled: true, allowlist: ['127.0.0.1/32'], createdAt: iso(now - 61 * DAY) },
    { id: 'u-03', name: 'qa.pipeline', platform: 'ajs-qa', stickyTtl: '24h', quotaBytesMonth: null, usedBytes: 88_000_000_000, enabled: true, allowlist: ['127.0.0.1/32', '10.30.4.0/24'], createdAt: iso(now - 40 * DAY) },
    { id: 'u-04', name: 'field.lab', platform: 'ajs-field', stickyTtl: '8h', quotaBytesMonth: 500_000_000_000, usedBytes: 410_000_000_000, enabled: true, allowlist: ['127.0.0.1/32', '192.168.40.0/24'], createdAt: iso(now - 27 * DAY) },
  ]
}

function buildKeys(now: number): ApiKey[] {
  return [
    { id: 'key-01', userId: 'u-01', userName: 'ada.ops', label: 'CI runner', prefix: 'ajs_live_mock_01', createdAt: iso(now - 30 * DAY), lastUsedAt: iso(now - 2 * HOUR), revokedAt: null },
    { id: 'key-02', userId: 'u-01', userName: 'ada.ops', label: 'Laptop', prefix: 'ajs_live_mock_02', createdAt: iso(now - 22 * DAY), lastUsedAt: iso(now - 18 * MINUTE), revokedAt: null },
    { id: 'key-03', userId: 'u-02', userName: 'build-bot', label: 'Build farm', prefix: 'ajs_live_mock_03', createdAt: iso(now - 19 * DAY), lastUsedAt: iso(now - 6 * MINUTE), revokedAt: null },
    { id: 'key-04', userId: 'u-03', userName: 'qa.pipeline', label: 'QA harness', prefix: 'ajs_live_mock_04', createdAt: iso(now - 14 * DAY), lastUsedAt: iso(now - 44 * MINUTE), revokedAt: null },
    { id: 'key-05', userId: 'u-04', userName: 'field.lab', label: 'Field device', prefix: 'ajs_live_mock_05', createdAt: iso(now - 9 * DAY), lastUsedAt: iso(now - 3 * HOUR), revokedAt: null },
    { id: 'key-06', userId: 'u-04', userName: 'field.lab', label: 'Old tablet', prefix: 'ajs_live_mock_06', createdAt: iso(now - 80 * DAY), lastUsedAt: iso(now - 12 * DAY), revokedAt: iso(now - 3 * DAY) },
  ]
}

/** Three seeded scoped keys: active, account-restricted, and revoked+unlimited. */
function buildScopedKeys(now: number): ScopedKey[] {
  return [
    {
      id: 'sk-01',
      userId: 'u-01',
      userName: 'ada.ops',
      label: 'Grafana read-only',
      prefix: 'ajs_sk_mock_01',
      scopes: ['metrics:read', 'health:read'],
      accountIds: [],
      regions: [],
      rateLimitPerMin: 60,
      createdAt: iso(now - 12 * DAY),
      expiresAt: iso(now + 30 * DAY),
      lastUsedAt: iso(now - 2 * HOUR),
      revokedAt: null,
    },
    {
      id: 'sk-02',
      userId: 'u-02',
      userName: 'build-bot',
      label: 'CI catalog sync',
      prefix: 'ajs_sk_mock_02',
      scopes: ['accounts:read', 'catalog:read'],
      accountIds: ['acc-01', 'acc-02'],
      regions: [],
      rateLimitPerMin: 120,
      createdAt: iso(now - 8 * DAY),
      expiresAt: null,
      lastUsedAt: null,
      revokedAt: null,
    },
    {
      id: 'sk-03',
      userId: 'u-04',
      userName: 'field.lab',
      label: 'Legacy usage feed',
      prefix: 'ajs_sk_mock_03',
      scopes: ['usage:read'],
      accountIds: [],
      regions: [],
      rateLimitPerMin: 0,
      createdAt: iso(now - 40 * DAY),
      expiresAt: iso(now - 2 * DAY),
      lastUsedAt: iso(now - 10 * DAY),
      revokedAt: iso(now - 5 * DAY),
    },
  ]
}

function buildRequests(now: number, users: PanelUser[], nodes: ProxyNode[]): RequestLog[] {
  const rand = mulberry32(20261023)
  const targets = [
    'api.example.com:443',
    'cdn.example.net:443',
    'auth.example.org:443',
    'telemetry.example.dev:443',
    'registry.example.io:443',
    'storage.example.net:443',
    'docs.example.com:443',
  ]
  const methods = ['CONNECT', 'CONNECT', 'CONNECT', 'GET', 'POST', 'HEAD']
  const statuses = [200, 200, 200, 204, 200, 407, 502, 200]
  const liveNodes = nodes.filter((node) => node.state === 'LIVE' || node.state === 'READY')
  return Array.from({ length: 40 }, (_, index) => {
    const user = users[index % users.length]
    const node = liveNodes[index % liveNodes.length]
    const status = pick(rand, statuses)
    const latency = status === 502 ? 900 + Math.round(rand() * 1400) : status === 407 ? 9 + Math.round(rand() * 20) : 38 + Math.round(rand() * 260)
    return {
      id: `req-${String(index + 1).padStart(3, '0')}`,
      ts: iso(now - index * 31 * MINUTE - Math.round(rand() * 9 * MINUTE)),
      userId: user.id,
      userName: user.name,
      accountLabel: node.accountLabel,
      nodeLabel: node.label,
      region: node.region,
      method: pick(rand, methods),
      target: pick(rand, targets),
      status,
      bytes: 1_200 + Math.round(rand() * 8_500_000),
      latencyMs: latency,
      source: 'proxy',
    }
  })
}

function buildAudit(now: number): AuditEvent[] {
  const rows: Array<Pick<AuditEvent, 'severity' | 'actor' | 'action' | 'target' | 'detail'>> = [
    { severity: 'info', actor: 'operator', action: 'account.login', target: 'Northwind Ops', detail: 'Session refreshed · token valid for 10m' },
    { severity: 'info', actor: 'ajs-accountd', action: 'subscription.publish', target: 'ajs-live', detail: '8 nodes rendered · hash 7c1e…9a2' },
    { severity: 'warn', actor: 'ajs-accountd', action: 'node.drain', target: 'kr-sel-01', detail: 'Drain requested · 2 in-flight flows' },
    { severity: 'info', actor: 'operator', action: 'lease.rotate', target: 'ada.ops', detail: '203.0.113.11 → 203.0.113.24' },
    { severity: 'error', actor: 'ajs-accountd', action: 'account.auth_failed', target: 'Legacy HK', detail: 'WrongPassword · 3 attempts in 15m' },
    { severity: 'info', actor: 'operator', action: 'key.create', target: 'build-bot', detail: 'Key prefix ajs_live_mock_03' },
    { severity: 'info', actor: 'system', action: 'health.probe', target: 'cn-zj-01', detail: 'Canary ok · 42 ms · egress verified' },
    { severity: 'warn', actor: 'ajs-accountd', action: 'node.probe', target: 'us-lax-01', detail: 'Probe skipped · node is READY' },
    { severity: 'info', actor: 'operator', action: 'settings.update', target: 'health interval', detail: '60s → 90s' },
    { severity: 'info', actor: 'operator', action: 'account.test', target: 'Field Lab', detail: 'Login round-trip ok · 812 ms' },
    { severity: 'info', actor: 'system', action: 'health.probe', target: 'jp-tyo-01', detail: 'Canary ok · 96 ms · egress verified' },
    { severity: 'info', actor: 'operator', action: 'account.pause', target: 'QA Sandbox', detail: 'All nodes unpublished' },
    { severity: 'info', actor: 'operator', action: 'lease.release', target: 'qa.pipeline', detail: 'Lease on cn-sh-01 released' },
    { severity: 'warn', actor: 'ajs-accountd', action: 'publish.coalesced', target: 'ajs-live', detail: 'PATCH deferred · 5s window' },
    { severity: 'info', actor: 'system', action: 'health.probe', target: 'de-fra-01', detail: 'Canary ok · 132 ms · egress verified' },
    { severity: 'error', actor: 'ajs-accountd', action: 'gate.timeout', target: 'hk-03', detail: 'Gate did not answer within 30s' },
    { severity: 'info', actor: 'operator', action: 'key.revoke', target: 'field.lab', detail: 'Key prefix ajs_live_mock_06 revoked' },
    { severity: 'info', actor: 'operator', action: 'node.undrain', target: 'tw-tpe-01', detail: 'Node returned to service' },
    { severity: 'info', actor: 'system', action: 'usage.rollup', target: '2026-10-22', detail: '40 requests · 18.2 GB egress' },
    { severity: 'info', actor: 'operator', action: 'account.resume', target: 'Edge APAC', detail: 'Concurrency raised to 120' },
  ]
  return rows.map((row, index) => ({
    ...row,
    id: `audit-${String(index + 1).padStart(3, '0')}`,
    ts: iso(now - index * 23 * MINUTE - (index % 4) * 3 * MINUTE),
  }))
}

export function buildMetrics(range: '24h' | '7d', now: number, accounts: Account[]): Metrics {
  const points = range === '24h' ? 48 : 56
  const step = range === '24h' ? 30 * MINUTE : 3 * HOUR
  const rand = mulberry32(range === '24h' ? 7 : 11)
  const start = now - points * step
  const series: TrafficPoint[] = Array.from({ length: points }, (_, index) => {
    const wave = Math.sin(index / 5) * 0.35 + Math.sin(index / 11) * 0.2
    const ingress = Math.round(180_000_000 + 120_000_000 * (0.6 + wave) + rand() * 40_000_000)
    const egress = Math.round(ingress * (0.72 + rand() * 0.18))
    return { t: iso(start + index * step), ingress, egress }
  })
  const ingressBytes = series.reduce((sum, point) => sum + point.ingress, 0)
  const egressBytes = series.reduce((sum, point) => sum + point.egress, 0)
  const requests = Math.round(egressBytes / 480_000)
  return {
    range,
    series,
    totals: {
      requests,
      egressBytes,
      ingressBytes,
      p95LatencyMs: range === '24h' ? 284 : 311,
    },
    accountHealth: accounts.map((account, index) => ({
      accountId: account.id,
      label: account.label,
      score: account.state === 'active' ? 98 - index * 3 : account.state === 'paused' ? 62 : 24,
      state: account.state,
    })),
    telemetryAvailable: true,
    generatedAt: iso(now),
  }
}

export function createDb(now = Date.now()): MockDb {
  const users = buildUsers(now)
  const nodes = buildNodes(now)
  const accounts = buildAccounts(now)
  return {
    accounts,
    nodes,
    leases: buildLeases(now),
    users,
    keys: buildKeys(now),
    scopedKeys: buildScopedKeys(now),
    requests: buildRequests(now, users, nodes),
    audit: buildAudit(now),
    settings: { ...DEFAULT_SETTINGS },
    keySeq: 6,
    scopedKeySeq: 3,
    leaseSeq: 8,
    mutationSeq: 0,
    egressCheckedAt: {},
  }
}

let db = createDb()

export function getDb(): MockDb {
  return db
}

export function resetMockDb(): MockDb {
  db = createDb()
  return db
}

export function syncAccountCounts(): void {
  for (const account of db.accounts) {
    const nodes = db.nodes.filter((node) => node.accountId === account.id)
    account.nodeCount = nodes.length
    account.liveNodeCount = nodes.filter((node) => node.state === 'LIVE').length
  }
}

/* --------------------------------------------------------------------------
 * Account import / export / bulk delete — mock parity
 *
 * The mock has no sealed-secret store and no crypto path, so JSON/CSV export
 * emits `password: null` with a warning and import discards any password it
 * reads. Encrypted files are a live-daemon-only feature; capabilities reports
 * `encryptedExport: false` and the dialogs disable that format.
 * ------------------------------------------------------------------------ */

export const MOCK_IO_CAPABILITIES: AccountIOCapabilities = {
  encryptedExport: false,
  plaintextExport: true,
  maxImportRows: 1000,
  maxExportAccounts: 1000,
  maxDeleteIDs: 100,
  maxBodyBytes: 1_048_576,
  maxImportBodyBytes: 4_194_304,
}

const MOCK_PLATFORMS: Platform[] = ['android', 'ios', 'windows', 'linux']
const MOCK_STATES: AccountState[] = ['active', 'paused', 'auth_failed', 'expired']
const IMPORT_LABEL_SUFFIX = ' (imported)'

export interface MockExportFile {
  content: string
  mime: string
  extension: 'json' | 'csv'
  counts: { accounts: number; passwords: number; devices: number }
}

function utf8Length(value: string): number {
  return new TextEncoder().encode(value).length
}

function truncateUtf8(value: string, maxBytes: number): string {
  let out = ''
  let bytes = 0
  for (const char of value) {
    const size = utf8Length(char)
    if (bytes + size > maxBytes) break
    out += char
    bytes += size
  }
  return out
}

function hasControlChars(value: string): boolean {
  return /[\u0000-\u001f\u007f]/.test(value)
}

function csvCell(value: string): string {
  return /[",\r\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value
}

function toExportRow(account: Account): AccountExportRow {
  return {
    id: account.id,
    label: account.label,
    username: account.username,
    // The mock never holds a vendor password; say so instead of inventing one.
    password: null,
    platform: account.platform,
    concurrency: account.concurrency,
    regions: [...account.regions],
    state: account.state,
    expiresAt: account.expiresAt,
    lastLoginAt: account.lastLoginAt,
    createdAt: account.createdAt,
    warnings: ['password_unavailable'],
  }
}

/** Builds a plaintext export exactly as the live daemon would for a mock DB. */
export function buildMockExport(ids: string[], format: 'json' | 'csv'): MockExportFile {
  const db = getDb()
  const rows = ids.map((id) => {
    const account = db.accounts.find((item) => item.id === id)
    if (!account) throw new ApiError(404, `account ${id} not found`, 'NOT_FOUND')
    return toExportRow(account)
  })
  const counts = { accounts: rows.length, passwords: 0, devices: 0 }
  if (format === 'csv') {
    const header = 'label,username,password,platform,concurrency,regions,state,expiresAt'
    const lines = rows.map((row) => {
      const regions = row.regions ?? []
      if (regions.some((region) => /[|\r\n]/.test(region))) {
        throw new ApiError(
          409,
          'CSV cannot represent a region containing "|", CR or LF; use JSON instead',
          'CONFLICT',
        )
      }
      return [
        row.label,
        row.username,
        '',
        row.platform,
        String(row.concurrency ?? 1),
        regions.join('|'),
        row.state ?? 'paused',
        row.expiresAt ?? '',
      ]
        .map(csvCell)
        .join(',')
    })
    return {
      content: `${header}\n${lines.join('\n')}\n`,
      mime: 'text/csv; charset=utf-8',
      extension: 'csv',
      counts,
    }
  }
  const doc: AccountExportDocument = {
    format: 'ajs-accounts',
    version: 1,
    exportedAt: new Date().toISOString(),
    source: { daemonVersion: '0.1.0-mock', secretsSealed: false },
    counts,
    accounts: rows,
  }
  return {
    content: JSON.stringify(doc, null, 2),
    mime: 'application/json; charset=utf-8',
    extension: 'json',
    counts,
  }
}

interface ParsedImport {
  rows: Array<AccountExportRow | null>
  /** 1-based data-row structural failures (CSV field count). */
  rowIssues: Map<number, { code: string; message: string }>
  warnings: AccountIOWarning[]
}

function splitCsvRows(text: string): string[][] {
  const rows: string[][] = []
  let row: string[] = []
  let field = ''
  let quoted = false
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index]
    if (quoted) {
      if (char === '"') {
        if (text[index + 1] === '"') {
          field += '"'
          index += 1
        } else {
          quoted = false
        }
      } else {
        field += char
      }
      continue
    }
    if (char === '"') {
      quoted = true
    } else if (char === ',') {
      row.push(field)
      field = ''
    } else if (char === '\n' || char === '\r') {
      if (char === '\r' && text[index + 1] === '\n') index += 1
      row.push(field)
      field = ''
      if (row.length > 1 || row[0] !== '') rows.push(row)
      row = []
    } else {
      field += char
    }
  }
  row.push(field)
  if (row.length > 1 || row[0] !== '') rows.push(row)
  return rows
}

function parseMockCsv(text: string): ParsedImport {
  const table = splitCsvRows(text.replace(/^\uFEFF/, ''))
  if (table.length === 0) throw new ApiError(400, 'file has no accounts', 'INVALID_ARGUMENT')
  const headers = table[0].map((header) => header.trim().toLowerCase())
  if (new Set(headers).size !== headers.length) {
    throw new ApiError(400, 'CSV header has duplicate columns', 'INVALID_ARGUMENT')
  }
  for (const required of ['label', 'username', 'password', 'platform']) {
    if (!headers.includes(required)) {
      throw new ApiError(400, `CSV header is missing "${required}"`, 'INVALID_ARGUMENT')
    }
  }
  const warnings: AccountIOWarning[] = []
  const known = new Set(['label', 'username', 'password', 'platform', 'concurrency', 'regions', 'state', 'expiresAt'])
  if (headers.some((header) => !known.has(header))) warnings.push('unknown_column')
  const data = table.slice(1)
  if (data.length > MOCK_IO_CAPABILITIES.maxImportRows) {
    throw new ApiError(400, 'at most 1000 account rows can be imported', 'INVALID_ARGUMENT')
  }
  const rows: Array<AccountExportRow | null> = []
  const rowIssues = new Map<number, { code: string; message: string }>()
  data.forEach((cells, index) => {
    const rowNumber = index + 1
    if (cells.length !== headers.length) {
      rows.push(null)
      rowIssues.set(rowNumber, {
        code: 'INVALID_ARGUMENT',
        message: `row ${rowNumber}: field count does not match the header`,
      })
      return
    }
    const value = (key: string) => {
      const column = headers.indexOf(key)
      return column >= 0 ? cells[column] : ''
    }
    const password = value('password')
    const concurrency = value('concurrency').trim()
    rows.push({
      label: value('label'),
      username: value('username'),
      password: password === '' ? null : password,
      platform: value('platform').trim() as Platform,
      concurrency: concurrency === '' ? 1 : Number(concurrency),
      regions: value('regions')
        .split('|')
        .map((region) => region.trim())
        .filter(Boolean),
      state: (value('state').trim() || undefined) as AccountState | undefined,
      expiresAt: value('expiresAt').trim() || undefined,
    })
  })
  return { rows, rowIssues, warnings }
}

function parseMockJson(content: unknown): ParsedImport {
  if (typeof content !== 'object' || content === null || Array.isArray(content)) {
    throw new ApiError(400, 'JSON export must be an object', 'INVALID_ARGUMENT')
  }
  const doc = content as { format?: unknown; version?: unknown; accounts?: unknown }
  if (doc.format !== 'ajs-accounts' || doc.version !== 1) {
    throw new ApiError(400, 'unsupported account export format or version', 'INVALID_ARGUMENT')
  }
  if (!Array.isArray(doc.accounts) || doc.accounts.length === 0) {
    throw new ApiError(400, 'file has no accounts', 'INVALID_ARGUMENT')
  }
  if (doc.accounts.length > MOCK_IO_CAPABILITIES.maxImportRows) {
    throw new ApiError(400, 'at most 1000 account rows can be imported', 'INVALID_ARGUMENT')
  }
  const rows: Array<AccountExportRow | null> = []
  const rowIssues = new Map<number, { code: string; message: string }>()
  doc.accounts.forEach((entry, index) => {
    const rowNumber = index + 1
    if (typeof entry !== 'object' || entry === null || Array.isArray(entry)) {
      rows.push(null)
      rowIssues.set(rowNumber, {
        code: 'INVALID_ARGUMENT',
        message: `row ${rowNumber}: account must be an object`,
      })
      return
    }
    rows.push(entry as AccountExportRow)
  })
  return { rows, rowIssues, warnings: [] }
}

function parseMockImport(req: AccountImportRequest): ParsedImport {
  switch (req.source.kind) {
    case 'json':
      return parseMockJson(req.source.content)
    case 'csv':
      if (typeof req.source.content !== 'string') {
        throw new ApiError(400, 'CSV content must be a string', 'INVALID_ARGUMENT')
      }
      return parseMockCsv(req.source.content)
    case 'encrypted':
      throw new ApiError(
        409,
        'Encrypted files require the live daemon',
        'CONFLICT',
      )
    default:
      throw new ApiError(400, 'unsupported import source kind', 'INVALID_ARGUMENT')
  }
}

interface MockImportOptions {
  mode: 'atomic' | 'partial'
  matchBy: 'label' | 'username_platform' | 'id'
  onConflict: 'skip' | 'update' | 'rename' | 'fail'
  initialState: 'paused' | 'active' | 'from_file'
  idPolicy: 'new' | 'preserve'
  devicePolicy: 'keep' | 'restore_if_missing' | 'replace'
  preserveTimestamps: boolean
  dryRun: boolean
}

function normalizeMockOptions(options: AccountImportOptions): MockImportOptions {
  const result: MockImportOptions = {
    mode: options.mode ?? 'atomic',
    matchBy: options.matchBy ?? 'label',
    onConflict: options.onConflict ?? 'skip',
    initialState: options.initialState ?? 'paused',
    idPolicy: options.idPolicy ?? 'new',
    devicePolicy: options.devicePolicy ?? 'restore_if_missing',
    preserveTimestamps: options.preserveTimestamps ?? true,
    dryRun: options.dryRun ?? false,
  }
  if (result.mode !== 'atomic' && result.mode !== 'partial') {
    throw new ApiError(400, 'mode must be atomic or partial', 'INVALID_ARGUMENT')
  }
  if (!['label', 'username_platform', 'id'].includes(result.matchBy)) {
    throw new ApiError(400, 'unsupported matchBy value', 'INVALID_ARGUMENT')
  }
  if (!['skip', 'update', 'rename', 'fail'].includes(result.onConflict)) {
    throw new ApiError(400, 'unsupported onConflict value', 'INVALID_ARGUMENT')
  }
  if (!['paused', 'active', 'from_file'].includes(result.initialState)) {
    throw new ApiError(400, 'unsupported initialState value', 'INVALID_ARGUMENT')
  }
  if (result.idPolicy !== 'new' && result.idPolicy !== 'preserve') {
    throw new ApiError(400, 'idPolicy must be new or preserve', 'INVALID_ARGUMENT')
  }
  if (!['keep', 'restore_if_missing', 'replace'].includes(result.devicePolicy)) {
    throw new ApiError(400, 'unsupported devicePolicy value', 'INVALID_ARGUMENT')
  }
  if (result.onConflict === 'rename' && result.idPolicy === 'preserve') {
    throw new ApiError(400, 'rename requires idPolicy "new"', 'INVALID_ARGUMENT')
  }
  return result
}

interface ValidatedRow {
  id?: string
  label: string
  username: string
  password: string | null
  platform: Platform
  concurrency: number
  regions: string[]
  state?: AccountState
  expiresAt?: string
  createdAt?: string
  lastLoginAt?: string
  hasDevice: boolean
}

function validateMockRow(row: AccountExportRow): { row?: ValidatedRow; code?: string; message?: string } {
  const label = typeof row.label === 'string' ? row.label.trim() : ''
  if (!label || utf8Length(label) > 80 || hasControlChars(label)) {
    return { code: 'INVALID_ARGUMENT', message: 'label must be 1–80 bytes without control characters' }
  }
  const username = typeof row.username === 'string' ? row.username.trim() : ''
  if (!username || utf8Length(username) > 80 || hasControlChars(username)) {
    return {
      code: 'INVALID_ARGUMENT',
      message: 'username must be 1–80 bytes without control characters',
    }
  }
  const password = row.password == null ? null : row.password
  if (password !== null) {
    if (typeof password !== 'string') {
      return { code: 'INVALID_ARGUMENT', message: 'password must be a string or null' }
    }
    if (utf8Length(password) > 512 || password.includes('\u0000')) {
      return { code: 'INVALID_ARGUMENT', message: 'password must be 1–512 bytes' }
    }
    if (password !== '' && password.trim() === '') {
      return { code: 'INVALID_ARGUMENT', message: 'password must contain a non-whitespace character' }
    }
  }
  const platform = row.platform
  if (!MOCK_PLATFORMS.includes(platform)) {
    return { code: 'INVALID_ARGUMENT', message: 'platform must be android, ios, windows or linux' }
  }
  const rawConcurrency = row.concurrency ?? 0
  if (
    typeof rawConcurrency !== 'number' ||
    !Number.isInteger(rawConcurrency) ||
    rawConcurrency < 0 ||
    rawConcurrency > 2000
  ) {
    return { code: 'INVALID_ARGUMENT', message: 'concurrency must be an integer from 1 to 2000' }
  }
  const regions = Array.isArray(row.regions) ? row.regions : []
  if (regions.length > 64) {
    return { code: 'INVALID_ARGUMENT', message: 'at most 64 regions are allowed' }
  }
  const cleanRegions: string[] = []
  for (const region of regions) {
    if (typeof region !== 'string') {
      return { code: 'INVALID_ARGUMENT', message: 'region tags must be strings' }
    }
    const tag = region.trim()
    if (!tag || utf8Length(tag) > 64 || hasControlChars(tag)) {
      return { code: 'INVALID_ARGUMENT', message: 'region tags must be 1–64 bytes' }
    }
    if (!cleanRegions.includes(tag)) cleanRegions.push(tag)
  }
  if (row.state !== undefined && !MOCK_STATES.includes(row.state)) {
    return { code: 'INVALID_ARGUMENT', message: 'state is not a known account state' }
  }
  for (const field of ['expiresAt', 'createdAt', 'lastLoginAt'] as const) {
    const value = row[field]
    if (value !== undefined && value !== '' && Number.isNaN(Date.parse(value))) {
      return { code: 'INVALID_ARGUMENT', message: `${field} must be an RFC 3339 timestamp` }
    }
  }
  return {
    row: {
      id: typeof row.id === 'string' ? row.id.trim() : undefined,
      label,
      username,
      password: password === '' ? null : password,
      platform,
      concurrency: rawConcurrency === 0 ? 1 : rawConcurrency,
      regions: cleanRegions,
      state: row.state,
      expiresAt: row.expiresAt || undefined,
      createdAt: row.createdAt || undefined,
      lastLoginAt: row.lastLoginAt || undefined,
      hasDevice: typeof row.device === 'object' && row.device !== null,
    },
  }
}

function mockAccountKey(row: ValidatedRow, matchBy: MockImportOptions['matchBy']): string | null {
  if (matchBy === 'label') return row.label.toLowerCase()
  if (matchBy === 'username_platform') return `${row.username}\u0000${row.platform}`
  return row.id ?? null
}

function findMockMatches(
  db: MockDb,
  row: ValidatedRow,
  matchBy: MockImportOptions['matchBy'],
): Account[] {
  if (matchBy === 'label') {
    return db.accounts.filter((account) => account.label.toLowerCase() === row.label.toLowerCase())
  }
  if (matchBy === 'username_platform') {
    return db.accounts.filter(
      (account) => account.username === row.username && account.platform === row.platform,
    )
  }
  return row.id ? db.accounts.filter((account) => account.id === row.id) : []
}

function renameMockLabel(base: string, taken: Set<string>): string {
  let suffix = IMPORT_LABEL_SUFFIX
  let candidate = truncateUtf8(base, 80 - utf8Length(suffix)) + suffix
  let attempt = 2
  while (taken.has(candidate.toLowerCase())) {
    suffix = ` (imported ${attempt})`
    candidate = truncateUtf8(base, 80 - utf8Length(suffix)) + suffix
    attempt += 1
    if (attempt > 1000) throw new ApiError(409, 'could not find a free label', 'CONFLICT')
  }
  return candidate
}

function nextMockAccountId(db: MockDb, reserved: Set<string>): string {
  let highest = 0
  for (const account of db.accounts) {
    const match = /^acc-(\d+)$/.exec(account.id)
    if (match) highest = Math.max(highest, Number(match[1]))
  }
  for (const id of reserved) {
    const match = /^acc-(\d+)$/.exec(id)
    if (match) highest = Math.max(highest, Number(match[1]))
  }
  let next = highest + 1
  while (reserved.has(`acc-${String(next).padStart(2, '0')}`)) next += 1
  return `acc-${String(next).padStart(2, '0')}`
}

interface MockCreatePlan {
  kind: 'create'
  row: ValidatedRow
  label: string
  id: string | null
  warnings: AccountIOWarning[]
}
interface MockUpdatePlan {
  kind: 'update'
  row: ValidatedRow
  accountId: string
  warnings: AccountIOWarning[]
}
type MockPlan = MockCreatePlan | MockUpdatePlan

/** Dry-run or apply a JSON/CSV import against the in-memory mock database. */
export function mockImportAccounts(req: AccountImportRequest): AccountImportResult {
  const options = normalizeMockOptions(req.options)
  const parsed = parseMockImport(req)
  const db = getDb()
  const results: AccountImportRowResult[] = []
  const plan: MockPlan[] = []
  const plannedKeys = new Set<string>()
  const plannedLabels = new Set(db.accounts.map((account) => account.label.toLowerCase()))
  const usedIds = new Set(db.accounts.map((account) => account.id))
  const reservedIds = new Set<string>()
  let nextId = nextMockAccountId(db, reservedIds)

  const fail = (
    row: number,
    label: string,
    code: string,
    message: string,
    existingId: string | null = null,
  ) => {
    results.push({
      row,
      action: 'fail',
      id: null,
      label,
      existingId,
      renamedTo: null,
      code,
      message,
    })
  }

  parsed.rows.forEach((input, index) => {
    const rowNumber = index + 1
    const structural = parsed.rowIssues.get(rowNumber)
    if (structural || !input) {
      fail(rowNumber, '', structural?.code ?? 'INVALID_ARGUMENT', structural?.message ?? 'invalid row')
      return
    }
    const validated = validateMockRow(input)
    if (!validated.row) {
      fail(rowNumber, typeof input.label === 'string' ? input.label.trim() : '', validated.code ?? 'INVALID_ARGUMENT', validated.message ?? 'invalid row')
      return
    }
    const row = validated.row
    const key = mockAccountKey(row, options.matchBy)
    if (options.matchBy === 'id' && !key) {
      fail(rowNumber, row.label, 'INVALID_ARGUMENT', 'id is required when matching by id')
      return
    }
    if (key !== null && plannedKeys.has(key)) {
      fail(rowNumber, row.label, 'DUPLICATE_ROW', 'duplicate row in file')
      return
    }
    if (key !== null) plannedKeys.add(key)

    const matches = findMockMatches(db, row, options.matchBy)
    if (matches.length > 1) {
      fail(rowNumber, row.label, 'CONFLICT', 'multiple accounts match this row')
      return
    }
    const existing = matches[0]
    const baseWarnings: AccountIOWarning[] = []
    if (row.hasDevice && options.devicePolicy !== 'keep') {
      // The mock has no device store; be explicit instead of pretending.
      baseWarnings.push('device_generated_on_login')
    }

    if (!existing) {
      let finalId: string | null = null
      const warnings = [...baseWarnings]
      if (options.idPolicy === 'preserve' && row.id) {
        if (/^acc-\d{2,}$/.test(row.id) && !usedIds.has(row.id) && !reservedIds.has(row.id)) {
          finalId = row.id
          reservedIds.add(finalId)
        } else if (/^acc-\d{2,}$/.test(row.id)) {
          fail(rowNumber, row.label, 'CONFLICT', `id ${row.id} is already in use`)
          return
        } else {
          warnings.push('id_ignored')
        }
      } else if (options.idPolicy === 'new' && row.id) {
        warnings.push('id_ignored')
      }
      if (!row.password) warnings.push('password_unavailable')
      plan.push({ kind: 'create', row, label: row.label, id: finalId, warnings })
      results.push({
        row: rowNumber,
        action: 'create',
        id: finalId,
        label: row.label,
        existingId: null,
        renamedTo: null,
        warnings,
        code: null,
        message: null,
      })
      plannedLabels.add(row.label.toLowerCase())
      return
    }

    if (options.onConflict === 'skip') {
      results.push({
        row: rowNumber,
        action: 'skip',
        id: existing.id,
        label: row.label,
        existingId: existing.id,
        renamedTo: null,
        warnings: baseWarnings,
        code: null,
        message: null,
      })
      return
    }
    if (options.onConflict === 'update') {
      const warnings = [...baseWarnings]
      if (!row.password) warnings.push('password_kept')
      plan.push({ kind: 'update', row, accountId: existing.id, warnings })
      results.push({
        row: rowNumber,
        action: 'update',
        id: existing.id,
        label: row.label,
        existingId: existing.id,
        renamedTo: null,
        warnings,
        code: null,
        message: null,
      })
      return
    }
    if (options.onConflict === 'rename') {
      const renamed = renameMockLabel(row.label, plannedLabels)
      const warnings = [...baseWarnings]
      if (!row.password) warnings.push('password_unavailable')
      plan.push({ kind: 'create', row, label: renamed, id: null, warnings })
      results.push({
        row: rowNumber,
        action: 'rename',
        id: null,
        label: row.label,
        existingId: existing.id,
        renamedTo: renamed,
        warnings,
        code: null,
        message: null,
      })
      plannedLabels.add(renamed.toLowerCase())
      return
    }
    fail(
      rowNumber,
      row.label,
      'CONFLICT',
      `account ${existing.id} already uses this key`,
      existing.id,
    )
  })

  const counts = {
    total: results.length,
    created:
      results.filter((row) => row.action === 'create' || row.action === 'rename').length,
    updated: results.filter((row) => row.action === 'update').length,
    skipped: results.filter((row) => row.action === 'skip').length,
    renamed: results.filter((row) => row.action === 'rename').length,
    failed: results.filter((row) => row.action === 'fail').length,
  }
  const atomicFailure = options.mode === 'atomic' && counts.failed > 0
  const applied =
    !options.dryRun && !atomicFailure && counts.created + counts.updated > 0

  if (!options.dryRun && !atomicFailure) {
    const now = Date.now()
    for (const item of plan) {
      if (item.kind === 'update') {
        const account = db.accounts.find((entry) => entry.id === item.accountId)
        if (!account) continue
        account.label = item.row.label
        account.username = item.row.username
        account.platform = item.row.platform
        account.concurrency = item.row.concurrency
        account.regions = [...item.row.regions]
        if (item.row.expiresAt) account.expiresAt = item.row.expiresAt
        if (options.initialState === 'from_file' && item.row.state) account.state = item.row.state
        continue
      }
      let id = item.id
      if (!id) {
        id = nextId
        let candidate = id
        while (usedIds.has(candidate) || reservedIds.has(candidate)) {
          const numeric = Number(/^acc-(\d+)$/.exec(candidate)?.[1] ?? 0) + 1
          candidate = `acc-${String(numeric).padStart(2, '0')}`
        }
        id = candidate
        reservedIds.add(id)
        nextId = `acc-${String(Number(/^acc-(\d+)$/.exec(id)?.[1] ?? 0) + 1).padStart(2, '0')}`
      }
      const state: AccountState =
        options.initialState === 'from_file'
          ? item.row.state ?? 'paused'
          : options.initialState
      const created: Account = {
        id,
        label: item.label,
        username: item.row.username,
        platform: item.row.platform,
        state,
        concurrency: item.row.concurrency,
        regions: [...item.row.regions],
        nodeCount: 0,
        liveNodeCount: 0,
        usageBytes: 0,
        expiresAt: item.row.expiresAt ?? new Date(now + 30 * 86_400_000).toISOString(),
        lastLoginAt: options.preserveTimestamps ? item.row.lastLoginAt ?? '' : '',
        createdAt: options.preserveTimestamps
          ? item.row.createdAt ?? new Date(now).toISOString()
          : new Date(now).toISOString(),
      }
      db.accounts.unshift(created)
      usedIds.add(id)
    }
    syncAccountCounts()
  }

  return {
    format: 'ajs-accounts-import-result',
    version: 1,
    dryRun: options.dryRun,
    applied,
    mode: options.mode,
    counts,
    results,
    warnings: parsed.warnings,
  }
}

/** Sequential per-item bulk delete, mirroring the daemon's result shape. */
export function mockBulkDelete(
  ids: string[],
  mode: 'continue' | 'stop' = 'continue',
  dryRun = false,
): BulkDeleteResult {
  if (ids.length === 0) throw new ApiError(400, 'ids is required', 'INVALID_ARGUMENT')
  if (ids.length > MOCK_IO_CAPABILITIES.maxDeleteIDs) {
    throw new ApiError(400, 'at most 100 accounts can be deleted at once', 'INVALID_ARGUMENT')
  }
  if (mode !== 'continue' && mode !== 'stop') {
    throw new ApiError(400, 'mode must be continue or stop', 'INVALID_ARGUMENT')
  }
  const db = getDb()
  const seen = new Set<string>()
  const unique = ids.filter((id) => {
    if (seen.has(id)) return false
    seen.add(id)
    return true
  })
  const results: BulkDeleteItem[] = []
  let stopped = false
  for (const id of unique) {
    if (stopped) {
      results.push({
        id,
        label: null,
        status: 'skipped',
        nodes: 0,
        leases: 0,
        code: null,
        message: null,
      })
      continue
    }
    const account = db.accounts.find((item) => item.id === id)
    if (!account) {
      results.push({
        id,
        label: null,
        status: 'not_found',
        nodes: 0,
        leases: 0,
        code: 'NOT_FOUND',
        message: 'account not found',
      })
      continue
    }
    const nodes = db.nodes.filter((node) => node.accountId === id).length
    const leases = db.leases.filter((lease) => lease.accountId === id).length
    if (dryRun) {
      results.push({
        id,
        label: account.label,
        status: 'would_delete',
        nodes,
        leases,
        code: null,
        message: null,
      })
      continue
    }
    db.accounts = db.accounts.filter((item) => item.id !== id)
    db.nodes = db.nodes.filter((node) => node.accountId !== id)
    db.leases = db.leases.filter((lease) => lease.accountId !== id)
    results.push({
      id,
      label: account.label,
      status: 'deleted',
      nodes,
      leases,
      code: null,
      message: null,
    })
  }
  syncAccountCounts()
  const count = (status: BulkDeleteItem['status']) =>
    results.filter((item) => item.status === status).length
  return {
    format: 'ajs-accounts-bulk-delete-result',
    version: 1,
    dryRun,
    mode,
    counts: {
      total: results.length,
      deleted: count('deleted'),
      wouldDelete: count('would_delete'),
      notFound: count('not_found'),
      failed: count('failed'),
      skipped: count('skipped'),
    },
    results,
  }
}

/* --------------------------------------------------------------------------
 * Lab fixtures — deterministic, derived from the same mock database so the
 * counters, sessions and catalog stay consistent with the other pages.
 * ------------------------------------------------------------------------ */

function hashHex(seed: string, length = 64): string {
  let h1 = 0x811c9dc5
  let h2 = 0x01000193
  const parts: string[] = []
  for (let index = 0; index < length / 8; index += 1) {
    const code = seed.charCodeAt(index % Math.max(seed.length, 1)) ^ index
    h1 = Math.imul(h1 ^ code, 0x01000193) >>> 0
    h2 = Math.imul(h2 ^ (code + index), 0x85ebca6b) >>> 0
    parts.push(((h1 ^ h2) >>> 0).toString(16).padStart(8, '0'))
  }
  return parts.join('').slice(0, length)
}

function redactedSid(nodeId: string): string {
  return `${hashHex(nodeId, 8)}…(32)`
}

export function buildLabOverview(db: MockDb, now = Date.now()): LabOverview {
  const liveNodes = db.nodes.filter((node) => node.state === 'LIVE')
  const gates = db.nodes.filter((node) => node.state === 'LIVE' || node.state === 'DRAINING')
  const warnings: LabOverview['warnings'] = []

  const pausedWithLive = db.accounts.filter(
    (account) =>
      account.state === 'paused' && db.nodes.some((node) => node.accountId === account.id && node.state === 'LIVE'),
  )
  if (pausedWithLive.length > 0) {
    warnings.push({
      code: 'paused_account_live_nodes',
      severity: 'warn',
      message: `${pausedWithLive.map((account) => account.label).join(', ')} paused but still owns LIVE nodes — pause does not stop gates.`,
    })
  }
  const authFailed = db.accounts.filter((account) => account.state === 'auth_failed')
  if (authFailed.length > 0) {
    warnings.push({
      code: 'account_auth_failed',
      severity: 'error',
      message: `${authFailed.map((account) => account.label).join(', ')} cannot authenticate; its nodes stay DOWN.`,
    })
  }

  return {
    enabled: true,
    generatedAt: iso(now),
    version: '0.1.0',
    commit: '',
    uptimeSec: 3 * 3600 + 17 * 60,
    modes: { accountLogin: true, resin: true, updateCheck: false, secrets: true, certPin: 'pinned' },
    counts: {
      accounts: db.accounts.length,
      nodes: db.nodes.length,
      liveNodes: liveNodes.length,
      sessions: gates.length,
      gates: gates.length,
      leases: db.leases.length,
    },
    warnings,
  }
}

export function buildLabGates(db: MockDb, now = Date.now()): GateView[] {
  return db.nodes
    .filter((node) => node.state === 'LIVE' || node.state === 'DRAINING')
    .map((node, index) => ({
      nodeId: node.id,
      accountId: node.accountId,
      state: node.state,
      port: node.gatePort,
      active: node.inUse,
      limit: node.capacity,
      accepted: 37 * (index + 1) + node.inUse,
      rejected: index % 4,
      ingressBytes: 12_400_000 * (index + 1),
      egressBytes: 9_800_000 * (index + 1),
      draining: node.state === 'DRAINING',
      startedAt: iso(now - (index + 1) * 40 * MINUTE),
    }))
}

export function buildLabFlows(db: MockDb, limit = 200, now = Date.now()): FlowsView {
  const gates = db.nodes.filter((node) => node.state === 'LIVE' || node.state === 'DRAINING')
  const rand = mulberry32(20261024)
  const hosts = [
    'api.example.com',
    'cdn.example.net',
    'auth.example.org',
    'telemetry.example.dev',
    'registry.example.io',
    'storage.example.net',
    'docs.example.com',
    '203.0.113.9',
    '198.51.100.4',
    'example.net',
  ]
  const ports = [443, 443, 443, 443, 80, 8080, 53]
  const errorClasses = ['handshake', 'closed', 'draining', 'admission_timeout', 'open_flow', 'reply']
  const wanted = Math.min(Math.max(Math.trunc(limit), 0), 500)
  const flows: FlowsView['flows'] = []

  for (let index = 0; index < wanted && gates.length > 0; index += 1) {
    const node = gates[index % gates.length]
    const rejected = rand() < 0.18
    // A handshake refusal may never learn a destination; the real gate then
    // records an empty method, host and port instead of inventing one.
    const handshakeWithoutTarget = rejected && rand() < 0.2
    const targetHost = handshakeWithoutTarget ? '' : pick(rand, hosts)
    const targetPort = targetHost ? pick(rand, ports) : 0
    const errorClass = rejected ? pick(rand, errorClasses) : ''
    flows.push({
      id: `${node.id}#${index + 1}`,
      nodeId: node.id,
      accountId: node.accountId,
      sid: redactedSid(node.id),
      startedAt: iso(now - index * 7_300 - Math.round(rand() * 3_000)),
      durationMs: rejected ? 1 + Math.round(rand() * 40) : 18 + Math.round(rand() * 1_800),
      method: targetHost ? 'CONNECT' : '',
      targetHost,
      targetPort,
      status: rejected ? 'rejected' : 'ok',
      bytesIn: rejected ? 0 : 400 + Math.round(rand() * 2_400_000),
      bytesOut: rejected ? 0 : 800 + Math.round(rand() * 8_000_000),
      ...(errorClass ? { errorClass } : {}),
    })
  }

  flows.sort((left, right) => {
    if (left.startedAt !== right.startedAt) return right.startedAt.localeCompare(left.startedAt)
    return right.id.localeCompare(left.id)
  })

  return { enabled: true, flows, dropped: 3 }
}

export function buildLabSessions(db: MockDb, now = Date.now()): SessionView[] {
  return db.nodes
    .filter((node) => node.state === 'LIVE' || node.state === 'DRAINING')
    .map((node, index) => ({
      nodeId: node.id,
      accountId: node.accountId,
      serverId: node.label,
      sid: redactedSid(node.id),
      healthy: node.state === 'LIVE',
      ageSec: (index + 1) * 420,
      lastFlowAt: iso(now - (index + 1) * 37_000),
      egressClaimed: node.egressIp,
      keepalive: { supported: false, ka: null, kat: null, kapf: null },
      errorClass: null,
    }))
}

export function buildLabEgress(db: MockDb): EgressView[] {
  return db.nodes.map((node) => ({
    nodeId: node.id,
    claimed: node.egressIp,
    verified: '',
    match: null,
    source: 'fv-claim',
    checkedAt: null,
  }))
}

const EGRESS_CHECK_COOLDOWN_MS = 5_000

/**
 * Mock parity for POST /v1/lab/egress-check: only a node with a running gate
 * (LIVE or DRAINING) may be dialed, a repeat inside 5 s answers 429, and the
 * observed IP is the seeded exit. The result is returned, not stored, so the
 * GET egress panel stays claim-only. The shell labels the build as mock, so
 * this fixture demonstrates the flow without pretending to contact the network.
 */
export function buildLabEgressCheck(db: MockDb, nodeId: string, now = Date.now()): EgressCheckResult {
  const node = db.nodes.find((item) => item.id === nodeId)
  if (!node) {
    throw new ApiError(404, '404 Not Found — node not found', 'NOT_FOUND')
  }
  if (node.state !== 'LIVE' && node.state !== 'DRAINING') {
    throw new ApiError(409, '409 Conflict — node has no running gate', 'CONFLICT')
  }
  const previous = db.egressCheckedAt[nodeId]
  if (previous !== undefined && now - previous < EGRESS_CHECK_COOLDOWN_MS) {
    throw new ApiError(
      429,
      '429 Too Many Requests — one egress check per node every 5 seconds',
      'QUOTA_EXCEEDED',
    )
  }
  db.egressCheckedAt[nodeId] = now
  return {
    nodeId,
    claimed: node.egressIp,
    verified: node.egressIp,
    match: true,
    latencyMs: node.latencyMs > 0 ? node.latencyMs : 40,
    checkedAt: iso(now),
    source: 'echo',
  }
}

export function buildLabProfiles(): LabProfiles {
  const active: ProfileView = {
    name: 'linux-4.2.3.0-binary',
    clientVersion: '4.2.3.0',
    coreVersion: '20241105-1',
    protocolVersion: 2,
    oaepHash: 'SHA-1',
    signingMode: 'none',
    requestShape: 'linux423',
    transport: 'httpupgrade',
  }
  return {
    enabled: true,
    active: active.name,
    supported: [active],
    samples: [
      { platform: 'windows', version: '4.9.6.0', core: '20250708-1', source: 'research/40-protocol/PROTOCOL.md:532-542' },
      { platform: 'linux', version: '4.3.0.1', core: '20260525-1', source: 'research/30-linux-static/REPORT.md' },
      { platform: 'linux', version: '4.2.3.0', core: '20241105-1', source: 'research/40-protocol/PROTOCOL.md:532-542' },
      { platform: 'android', version: '4.12.2.0', core: '20260525-1', source: 'apktool.yml versionName; TRIAGE.md:362-395' },
      { platform: 'macos', version: '4.9.6', core: '20250708-1', source: 'Info.plist; TRIAGE.md:362-395' },
    ],
  }
}

export function buildLabDevice(db: MockDb, accountId: string, now = Date.now()): DeviceView {
  const account = db.accounts.find((item) => item.id === accountId)
  if (!account) {
    return {
      enabled: true,
      accountId,
      present: false,
      hashes: {},
      claims: {},
      stable: false,
      firstSeenAt: null,
      rawAvailable: false,
    }
  }
  return {
    enabled: true,
    accountId,
    present: true,
    hashes: {
      uid: `sha256:${hashHex(`${accountId}:uid`)}`,
      osdid: `sha256:${hashHex(`${accountId}:osdid`)}`,
      cuid: `sha256:${hashHex(`${accountId}:cuid`)}`,
    },
    claims: {
      cseid: 'linux',
      osp: 'linux',
      osd: 'x86_64',
      osv: 'Linux 6.8.0-45-generic',
      dim: `lid=${hashHex(`${accountId}:lid`, 16)}`,
    },
    stable: account.state !== 'auth_failed',
    firstSeenAt: iso(now - 96 * DAY),
    rawAvailable: false,
  }
}

export function buildLabFlags(): LabFlags {
  const flags: FlagView[] = [
    { key: 'AJS_ACCOUNT_LOGIN', value: '1', source: 'env', secret: false, restartRequired: true },
    { key: 'AJS_NODE_LIMIT', value: '1', source: 'default', secret: false, restartRequired: true },
    { key: 'AJS_LOG_LEVEL', value: 'info', source: 'default', secret: false, restartRequired: true },
    { key: 'AJS_CERT_PIN', value: 'pinned', source: 'env', secret: false, restartRequired: true },
    { key: 'AJS_UPDATE_REPO', set: false, source: 'env', secret: false, restartRequired: true },
    { key: 'AJS_ADMIN_TOKEN', set: true, source: 'env', secret: true, redacted: true, restartRequired: true },
    { key: 'AJS_MASTER_KEY', set: true, source: 'env', secret: true, redacted: true, restartRequired: true },
    { key: 'AJS_RESIN_ADMIN_TOKEN', set: true, source: 'env', secret: true, redacted: true, restartRequired: true },
  ]
  return { enabled: true, flags }
}

const LOG_COMPONENTS = ['api', 'gate', 'runtime', 'store', 'login', 'publisher'] as const

export function buildLabLogs(now = Date.now(), query: LabLogQuery = {}): LabLogs {
  const messages = [
    'gate flow accepted',
    'gate flow rejected: vendor auth',
    'node session established',
    'node drain requested',
    'subscription publish coalesced',
    'login round-trip ok',
    'state snapshot saved',
    'audit event recorded',
    'lease allocated',
    'lease released',
  ]
  const levels = ['INFO', 'INFO', 'INFO', 'WARN', 'INFO', 'INFO', 'DEBUG', 'ERROR'] as const
  const entries: LogEntry[] = Array.from({ length: 60 }, (_, index) => ({
    ts: iso(now - index * 19_000),
    level: levels[index % levels.length],
    component: LOG_COMPONENTS[index % LOG_COMPONENTS.length],
    msg: messages[index % messages.length],
    accountId: index % 3 === 0 ? 'acc-01' : undefined,
    nodeId: index % 4 === 0 ? 'node-01' : undefined,
    errorClass: index % 8 === 7 ? 'vendor_rejected' : undefined,
  }))

  const level = query.level?.toUpperCase()
  const filtered = entries.filter((entry) => {
    if (level && level !== 'ALL' && entry.level !== level) return false
    if (query.component && query.component !== 'all' && entry.component !== query.component) return false
    return true
  })
  const limit = Math.max(1, Math.min(query.limit ?? 200, 2000))
  return { enabled: true, entries: filtered.slice(0, limit), dropped: 0 }
}

export function buildLabDiagnostics(db: MockDb, now = Date.now()): DiagnosticsView {
  return {
    enabled: true,
    health: {
      api: 'ok',
      store: 'ok',
      secrets: 'ok',
      certPin: 'pinned',
      resin: 'ok',
      update: 'disabled',
      controller: 'running',
      logRing: 'ok',
      pcap: 'unavailable',
    },
    state: { path: 'state.json', lastSavedAt: iso(now - 5 * MINUTE), sizeBytes: 148_912 },
    build: { version: '0.1.0', goVersion: 'go1.26.0', commit: '' },
    warnings: [
      {
        code: 'pcap_unavailable',
        severity: 'info',
        message: 'PCAP capture is a phase-2 host action; the daemon exposes no capture endpoint.',
      },
      ...buildLabOverview(db, now).warnings,
    ],
  }
}

/** Mirrors the daemon: empty region reads as "unknown", lowercase. */
function catalogRegion(region: string): string {
  return region.trim().toLowerCase() || 'unknown'
}

function catalogPage(
  servers: ServerView[],
  query: ServerCatalogQuery,
): Pick<AccountServers, 'total' | 'offset' | 'limit' | 'nextOffset' | 'hasMore' | 'regions' | 'servers'> {
  const limit = Math.min(500, Math.max(1, Math.trunc(query.limit ?? 50)))
  const offset = Math.max(0, Math.trunc(query.offset ?? 0))
  const needle = (query.q ?? '').trim().toLowerCase()
  const region = (query.region ?? '').trim().toLowerCase()
  const status = (query.status ?? '').trim().toLowerCase()

  const filtered = servers.filter((server) => {
    if (needle) {
      const haystack = `${server.id} ${server.title} ${server.host} ${catalogRegion(server.region)}`.toLowerCase()
      if (!haystack.includes(needle)) return false
    }
    if (region && catalogRegion(server.region) !== region) return false
    if (status && server.status.trim().toLowerCase() !== status) return false
    if (query.selected !== undefined && server.selected !== query.selected) return false
    return true
  })

  const sort = query.sort ?? 'order'
  const sorted =
    sort === 'order'
      ? filtered
      : [...filtered].sort((left, right) => {
          const field = (server: ServerView) => {
            switch (sort) {
              case 'title':
                return server.title
              case 'host':
                return server.host
              case 'status':
                return server.status
              case 'region':
                return catalogRegion(server.region)
              default:
                return ''
            }
          }
          return (
            field(left).toLowerCase().localeCompare(field(right).toLowerCase()) ||
            left.id.localeCompare(right.id)
          )
        })

  const counts = new Map<string, number>()
  for (const server of sorted) {
    const code = catalogRegion(server.region)
    counts.set(code, (counts.get(code) ?? 0) + 1)
  }
  const regions = [...counts.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([code, count]) => ({ code, count }))

  const start = Math.min(offset, sorted.length)
  const end = Math.min(start + limit, sorted.length)
  const page = sorted.slice(start, end)

  return {
    total: sorted.length,
    offset,
    limit,
    nextOffset: start + page.length,
    hasMore: start + page.length < sorted.length,
    regions,
    servers: page,
  }
}

export function buildAccountServers(
  db: MockDb,
  accountId: string,
  now = Date.now(),
  query: ServerCatalogQuery = {},
): AccountServers {
  const account = db.accounts.find((item) => item.id === accountId)
  const nodes = db.nodes.filter((node) => node.accountId === accountId)
  const limit = Math.min(500, Math.max(1, Math.trunc(query.limit ?? 50)))
  const offset = Math.max(0, Math.trunc(query.offset ?? 0))
  if (!account) {
    return {
      enabled: true,
      accountId,
      fetchedAt: null,
      stale: true,
      total: 0,
      offset,
      limit,
      nextOffset: 0,
      hasMore: false,
      regions: [],
      servers: [],
    }
  }
  const servers: ServerView[] = nodes.map((node, index) => ({
    id: node.label,
    title: `${regionName(node.region)} #${index + 1}`,
    region: node.region,
    host: `${node.label}.edge.example.net`,
    port: 8443,
    status: node.state === 'DOWN' ? 'offline' : 'ok',
    nodeId: node.id,
    nodeState: node.state,
    selected: node.state === 'LIVE' || node.state === 'DRAINING',
    skippedByLimit: node.state === 'READY',
    memberOnly: null,
    fullLoad: index % 4 === 0,
    blockingMarks: [],
    hasPingSecret: true,
    latencyMs: node.latencyMs > 0 ? node.latencyMs : null,
  }))
  return {
    enabled: true,
    accountId,
    // Stable per retained login, like the daemon's snapshot timestamp: a poll
    // must not look like a new vendor snapshot (the map cache keys on it).
    fetchedAt: account.state === 'auth_failed' ? null : account.lastLoginAt,
    stale: account.state === 'auth_failed' || account.state === 'paused',
    ...catalogPage(servers, query),
  }
}

/* --------------------------------------------------------------------------
 * Scoped keys — mock of daemon/internal/store/scoped_keys.go
 * ------------------------------------------------------------------------ */

function invalidArgument(message: string): ApiError {
  return new ApiError(400, message, 'INVALID_ARGUMENT')
}

function notFound(message: string): ApiError {
  return new ApiError(404, message, 'NOT_FOUND')
}

/** Mirrors the daemon's normalization: trim, reject blanks, dedupe. */
function normalizeMockList(values: readonly string[], field: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const raw of values) {
    const value = raw.trim()
    if (!value) throw invalidArgument(`${field} must not contain empty values`)
    if (value.length > 128) throw invalidArgument(`${field} entry is too long`)
    if (seen.has(value)) continue
    seen.add(value)
    out.push(value)
  }
  return out
}

export function mockCreateScopedKey(input: ScopedKeyCreateInput): ScopedKeyCreated {
  const db = getDb()
  const userId = input.userId.trim()
  if (!userId) throw invalidArgument('userId is required')
  const user = db.users.find((candidate) => candidate.id === userId)
  if (!user) throw notFound(`user ${userId} not found`)
  const label = input.label.trim()
  if (!label) throw invalidArgument('label is required')
  if (label.length > 80) throw invalidArgument('label must be at most 80 characters')

  const scopes: Scope[] = []
  const seenScopes = new Set<Scope>()
  for (const raw of input.scopes) {
    if (!SCOPES.includes(raw)) throw invalidArgument(`unknown scope ${raw}`)
    if (seenScopes.has(raw)) continue
    seenScopes.add(raw)
    scopes.push(raw)
  }
  if (scopes.length === 0) throw invalidArgument('scopes must contain at least one scope')

  const accountIds = normalizeMockList(input.accountIds ?? [], 'accountIds')
  const regions = normalizeMockList(input.regions ?? [], 'regions')

  let rateLimit = 60
  if (input.rateLimitPerMin !== undefined) {
    if (!Number.isInteger(input.rateLimitPerMin) || input.rateLimitPerMin < 0) {
      throw invalidArgument('rateLimitPerMin must be zero (unlimited) or positive')
    }
    rateLimit = input.rateLimitPerMin
  }

  let expiresAt: string | null = null
  if (input.expiresAt?.trim()) {
    const parsed = new Date(input.expiresAt)
    if (Number.isNaN(parsed.getTime())) throw invalidArgument('expiresAt must be an RFC 3339 timestamp')
    if (parsed.getTime() <= Date.now()) throw invalidArgument('expiresAt must be in the future')
    expiresAt = parsed.toISOString()
  }

  db.scopedKeySeq += 1
  const suffix = db.scopedKeySeq.toString(16).padStart(24, '0')
  const secret = `ajs_sk_mock_${suffix}`
  const key: ScopedKey = {
    id: `sk-${String(db.scopedKeySeq).padStart(2, '0')}`,
    userId: user.id,
    userName: user.name,
    label,
    prefix: secret.slice(0, 16),
    scopes,
    accountIds,
    regions,
    rateLimitPerMin: rateLimit,
    createdAt: new Date().toISOString(),
    expiresAt,
    lastUsedAt: null,
    revokedAt: null,
  }
  db.scopedKeys.unshift(key)
  db.audit.unshift({
    id: `audit-sk-${db.scopedKeySeq}`,
    ts: new Date().toISOString(),
    severity: 'info',
    actor: 'operator',
    action: 'key.create',
    target: user.name,
    detail: `Scoped key prefix ${key.prefix}`,
  })
  return { key: structuredClone(key), secret }
}

/** Revoking is idempotent, exactly like the daemon's store. */
export function mockRevokeScopedKey(id: string): void {
  const key = getDb().scopedKeys.find((candidate) => candidate.id === id)
  if (!key) throw notFound(`scoped key ${id} not found`)
  if (key.revokedAt) return
  key.revokedAt = new Date().toISOString()
}

/* --------------------------------------------------------------------------
 * Audit CSV — mock of daemon/internal/api/audit_export.go
 * ------------------------------------------------------------------------ */

const AUDIT_EXPORT_COLUMNS = ['ts', 'severity', 'actor', 'action', 'target', 'detail', 'meta']
const AUDIT_EXPORT_DEFAULT_WINDOW_MS = 7 * 24 * 60 * 60 * 1000
const AUDIT_EXPORT_MAX_WINDOW_MS = 366 * 24 * 60 * 60 * 1000
const AUDIT_EXPORT_ROW_CAP = 50_000

// A spreadsheet treats these leading characters as the start of a formula;
// prefixing an apostrophe forces the cell to render as text (the daemon does
// the same in audit_export.go).
const CSV_FORMULA_PREFIX = /^[=+\-@\t\r]/

function csvField(value: string): string {
  const cell = CSV_FORMULA_PREFIX.test(value) ? `'${value}` : value
  return /[",\n\r]/.test(cell) ? `"${cell.replace(/"/g, '""')}"` : cell
}

function exportStamp(ms: number): string {
  return new Date(ms).toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
}

export interface MockAuditExport {
  content: string
  filename: string
  truncated: boolean
}

export function mockAuditExportCsv(query: AuditExportQuery = {}, now = Date.now()): MockAuditExport {
  const to = query.to ? Date.parse(query.to) : now
  if (Number.isNaN(to)) throw invalidArgument('to must be an RFC 3339 timestamp')
  const from = query.from ? Date.parse(query.from) : to - AUDIT_EXPORT_DEFAULT_WINDOW_MS
  if (Number.isNaN(from)) throw invalidArgument('from must be an RFC 3339 timestamp')
  if (from > to) throw invalidArgument('from must not be after to')
  if (to - from > AUDIT_EXPORT_MAX_WINDOW_MS) throw invalidArgument('range must not exceed 366 days')

  const rows = getDb()
    .audit.filter((event) => {
      const at = Date.parse(event.ts)
      if (at < from || at > to) return false
      return query.severity === undefined || event.severity === query.severity
    })
    .sort((left, right) => Date.parse(left.ts) - Date.parse(right.ts))

  const kept = rows.slice(-AUDIT_EXPORT_ROW_CAP)
  const truncated = rows.length > kept.length
  const lines = [AUDIT_EXPORT_COLUMNS.join(',')]
  for (const event of kept) {
    lines.push(
      [
        event.ts,
        event.severity,
        event.actor,
        event.action,
        event.target,
        event.detail,
        '',
      ]
        .map(csvField)
        .join(','),
    )
  }
  return {
    content: `${lines.join('\r\n')}\r\n`,
    filename: `apc-audit-${exportStamp(from)}-${exportStamp(to)}.csv`,
    truncated,
  }
}

/* --------------------------------------------------------------------------
 * Event stream — mock of daemon/internal/events and GET /v1/events
 *
 * The stream is deterministic for a given mock database: the newest audit
 * rows become oldest-first events with monotonic evt-NNNNNN ids, so a test
 * can replay from nextCursor and see the same page twice.
 * ------------------------------------------------------------------------ */

const MOCK_EVENT_LIMIT = 12
const MOCK_EVENT_PAGE_LIMIT = 100
const MOCK_EVENT_MAX_LIMIT = 500

/** Fallback types for audit actions that are not webhook events themselves. */
const MOCK_EVENT_TYPES: WebhookEvent[] = [
  'node.connected',
  'schedule.fired',
  'node.disconnected',
  'device.retired',
  'audit.critical',
  'account.renewal_due',
  'node.switched',
  'schedule.failed',
]

function isWebhookEvent(value: string): value is WebhookEvent {
  return (WEBHOOK_EVENTS as readonly string[]).includes(value)
}

export function mockEventsPage(query: EventsQuery = {}): EventsPage {
  const rows = getDb().audit.slice(0, MOCK_EVENT_LIMIT).reverse()
  const events: EventRecord[] = rows.map((row, index) => ({
    id: `evt-${String(index + 1).padStart(6, '0')}`,
    type: isWebhookEvent(row.action)
      ? row.action
      : MOCK_EVENT_TYPES[index % MOCK_EVENT_TYPES.length],
    ts: row.ts,
    data: { actor: row.actor, target: row.target, detail: row.detail },
  }))
  const ids = events.map((event) => event.id)
  const since = query.since?.trim() ?? ''
  let start = 0
  if (since) {
    const index = ids.indexOf(since)
    if (index === -1) {
      throw invalidArgument(
        `invalid cursor ${JSON.stringify(since)}: expected a retained event id`,
      )
    }
    start = index + 1
  }
  const limit =
    query.limit && query.limit > 0
      ? Math.min(query.limit, MOCK_EVENT_MAX_LIMIT)
      : MOCK_EVENT_PAGE_LIMIT
  const page = events.slice(start, start + limit)
  return {
    events: page,
    nextCursor: page.length > 0 ? page[page.length - 1].id : since,
    oldestCursor: ids[0] ?? '',
    dropped: 0,
  }
}

/* --------------------------------------------------------------------------
 * Backup — mock of daemon/internal/backup (deterministic, not encrypted)
 * ------------------------------------------------------------------------ */

const MOCK_BACKUP_MIN_PASSPHRASE = 12
const MOCK_BACKUP_MAX_PASSPHRASE = 1024
const MOCK_BACKUP_APP = 'apc'
const MOCK_BACKUP_VERSION = 1

/** FNV-1a over the UTF-8 code units; a stable stand-in for a real digest. */
function mockDigest(value: string): string {
  let hash = 0x811c9dc5
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 0x01000193)
  }
  const first = (hash >>> 0).toString(16).padStart(8, '0')
  const second = mockDigestTail(value)
  return `${first}${second}`
}

function mockDigestTail(value: string): string {
  let hash = 0x9e3779b9
  for (let index = value.length - 1; index >= 0; index -= 1) {
    hash = Math.imul(hash ^ value.charCodeAt(index), 0x85ebca6b)
  }
  return (hash >>> 0).toString(16).padStart(8, '0')
}

function encodeBase64(value: string): string {
  const bytes = new TextEncoder().encode(value)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary)
}

function decodeBase64(value: string): string {
  const binary = atob(value)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
  return new TextDecoder().decode(bytes)
}

function mockPanelBlobs(db: MockDb): Record<string, string> {
  return {
    'state.json': JSON.stringify({
      version: 1,
      accounts: db.accounts,
      nodes: db.nodes,
      leases: db.leases,
      users: db.users,
      settings: db.settings,
    }),
    'secrets.json': JSON.stringify({ sealed: true, accounts: db.accounts.length }),
    'device-secrets.json': JSON.stringify({ sealed: true, devices: db.nodes.length }),
  }
}

interface MockBackupPayload {
  check: string
  blobs: Record<string, string>
}

function validateMockPassphrase(passphrase: string): void {
  if (
    passphrase.length < MOCK_BACKUP_MIN_PASSPHRASE ||
    passphrase.length > MOCK_BACKUP_MAX_PASSPHRASE
  ) {
    throw invalidArgument(
      `passphrase must be between ${MOCK_BACKUP_MIN_PASSPHRASE} and ${MOCK_BACKUP_MAX_PASSPHRASE} characters`,
    )
  }
}

function mockManifest(blobs: Record<string, string>): BackupManifest {
  return {
    entries: Object.keys(blobs)
      .sort()
      .map((name) => ({
        name,
        size: new TextEncoder().encode(blobs[name]).length,
        sha256: mockDigest(blobs[name]),
        required: name === 'state.json',
      })),
  }
}

function parseMockEnvelope(envelope: unknown): {
  header: Record<string, unknown>
  payload: MockBackupPayload | null
  bytes: number
} {
  if (envelope === null || typeof envelope !== 'object' || Array.isArray(envelope)) {
    throw invalidArgument('backup: malformed envelope')
  }
  const header = envelope as Record<string, unknown>
  const bytes = new TextEncoder().encode(JSON.stringify(header)).length
  if (header.app !== MOCK_BACKUP_APP) {
    throw invalidArgument('envelope is not an apc panel backup')
  }
  if (typeof header.payload !== 'string' || typeof header.version !== 'number') {
    throw invalidArgument('backup: malformed envelope')
  }
  if (typeof header.createdAt !== 'string' || Number.isNaN(Date.parse(header.createdAt))) {
    throw invalidArgument('backup: malformed envelope')
  }
  let payload: MockBackupPayload
  try {
    payload = JSON.parse(decodeBase64(header.payload)) as MockBackupPayload
  } catch {
    throw invalidArgument('backup: malformed envelope')
  }
  if (
    payload === null ||
    typeof payload !== 'object' ||
    typeof payload.check !== 'string' ||
    typeof payload.blobs !== 'object' ||
    payload.blobs === null
  ) {
    throw invalidArgument('backup: malformed envelope')
  }
  return { header, payload, bytes }
}

function openMockEnvelope(envelope: unknown, passphrase: string): {
  header: Record<string, unknown>
  payload: MockBackupPayload
  manifest: BackupManifest
} {
  const parsed = parseMockEnvelope(envelope)
  if (parsed.header.version !== MOCK_BACKUP_VERSION) {
    throw invalidArgument('backup: unsupported envelope version')
  }
  if (!passphrase) throw invalidArgument('backup: invalid passphrase')
  validateMockPassphrase(passphrase)
  if (parsed.payload === null || parsed.payload.check !== mockDigest(`backup:${passphrase}`)) {
    throw invalidArgument('backup: wrong passphrase or corrupted envelope')
  }
  return {
    header: parsed.header,
    payload: parsed.payload,
    manifest: mockManifest(parsed.payload.blobs),
  }
}

export function mockCreateBackup(
  passphrase: string,
  now = Date.now(),
): { content: string; filename: string; blobs: number } {
  validateMockPassphrase(passphrase)
  const blobs = mockPanelBlobs(getDb())
  const payload: MockBackupPayload = {
    check: mockDigest(`backup:${passphrase}`),
    blobs,
  }
  const header: Record<string, unknown> = {
    version: MOCK_BACKUP_VERSION,
    createdAt: new Date(now).toISOString(),
    app: MOCK_BACKUP_APP,
    kdf: { algo: 'pbkdf2-sha256', iter: 600000, salt: '' },
    cipher: 'aes-256-gcm',
    nonce: '',
    payload: encodeBase64(JSON.stringify(payload)),
  }
  return {
    content: JSON.stringify(header),
    filename: `ajs-backup-${new Date(now)
      .toISOString()
      .replace(/[-:]/g, '')
      .replace(/\.\d{3}Z$/, '')
      .replace('T', '-')}.ajsb`,
    blobs: Object.keys(blobs).length,
  }
}

export function mockInspectBackup(
  envelope: unknown,
  passphrase = '',
): BackupInspectView {
  const parsed = parseMockEnvelope(envelope)
  const view: BackupInspectView = {
    version: parsed.header.version as number,
    supported: parsed.header.version === MOCK_BACKUP_VERSION,
    app: parsed.header.app as string,
    createdAt: parsed.header.createdAt as string,
    envelopeBytes: parsed.bytes,
  }
  if (passphrase) {
    view.manifest = openMockEnvelope(envelope, passphrase).manifest
  }
  return view
}

export function mockRestoreBackup(input: {
  envelope: unknown
  passphrase: string
  dryRun?: boolean
  confirm?: string
}): BackupRestoreView {
  const opened = openMockEnvelope(input.envelope, input.passphrase)
  const live = mockPanelBlobs(getDb())
  const restored = opened.payload.blobs

  const diff: BackupDiff = {
    added: 0,
    removed: 0,
    changed: 0,
    same: 0,
    addedBytes: 0,
    removedBytes: 0,
    changedBytes: 0,
    sameBytes: 0,
    addedNames: [],
    removedNames: [],
    changedNames: [],
    sameNames: [],
  }
  for (const name of Object.keys(restored).sort()) {
    const restoredBytes = new TextEncoder().encode(restored[name]).length
    if (!(name in live)) {
      diff.added += 1
      diff.addedBytes += restoredBytes
      diff.addedNames.push(name)
    } else if (mockDigest(live[name]) !== mockDigest(restored[name])) {
      diff.changed += 1
      diff.changedBytes += restoredBytes
      diff.changedNames.push(name)
    } else {
      diff.same += 1
      diff.sameBytes += new TextEncoder().encode(live[name]).length
      diff.sameNames.push(name)
    }
  }
  for (const name of Object.keys(live).sort()) {
    if (name in restored) continue
    diff.removed += 1
    diff.removedBytes += new TextEncoder().encode(live[name]).length
    diff.removedNames.push(name)
  }

  const dryRun = input.dryRun ?? true
  if (!dryRun) {
    if (input.confirm !== 'RESTORE') {
      throw invalidArgument('confirm must be "RESTORE" when dryRun is false')
    }
    // The mock daemon always runs: without AJS_BACKUP_ALLOW_RESTORE an apply
    // is refused, and the console never sends one anyway.
    throw new ApiError(
      409,
      'restore must run while the daemon is stopped: set AJS_BACKUP_ALLOW_RESTORE=1 to write the files while it runs and restart afterwards',
      'BACKUP_DAEMON_RUNNING',
    )
  }
  return {
    dryRun: true,
    applied: false,
    restartRequired: false,
    manifest: opened.manifest,
    diff,
  }
}
