import type {
  Account,
  AccountServers,
  ApiKey,
  AuditEvent,
  DeviceView,
  DiagnosticsView,
  EgressCheckResult,
  EgressView,
  FlagView,
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
  ProfileView,
  ProxyNode,
  RequestLog,
  ServerView,
  SessionView,
  TrafficPoint,
} from './types'
import { ApiError } from './errors'

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
  requests: RequestLog[]
  audit: AuditEvent[]
  settings: PanelSettings
  keySeq: number
  leaseSeq: number
  mutationSeq: number
  /** Epoch ms of the last guarded egress check per nodeId; reset with the dataset. */
  egressCheckedAt: Record<string, number>
}

const REGION_NAMES: Record<string, string> = {
  'cn-zj': 'China · Zhejiang',
  'cn-sh': 'China · Shanghai',
  hk: 'Hong Kong',
  'jp-tyo': 'Japan · Tokyo',
  'sg-01': 'Singapore',
  'tw-tpe': 'Taiwan · Taipei',
  'kr-sel': 'South Korea · Seoul',
  'de-fra': 'Germany · Frankfurt',
  'uk-lon': 'United Kingdom · London',
  'us-lax': 'United States · Los Angeles',
  'ca-yyz': 'Canada · Toronto',
}

export function regionName(region: string): string {
  return REGION_NAMES[region] ?? region
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
    requests: buildRequests(now, users, nodes),
    audit: buildAudit(now),
    settings: { ...DEFAULT_SETTINGS },
    keySeq: 6,
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

export function buildAccountServers(db: MockDb, accountId: string, now = Date.now()): AccountServers {
  const account = db.accounts.find((item) => item.id === accountId)
  const nodes = db.nodes.filter((node) => node.accountId === accountId)
  if (!account) {
    return { enabled: true, accountId, fetchedAt: null, stale: true, servers: [] }
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
    fetchedAt: account.state === 'auth_failed' ? null : iso(now - 10 * MINUTE),
    stale: account.state === 'auth_failed' || account.state === 'paused',
    servers,
  }
}
