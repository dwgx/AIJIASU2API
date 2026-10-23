import {
  DEFAULT_SETTINGS,
  buildAccountServers,
  buildLabDevice,
  buildLabDiagnostics,
  buildLabEgress,
  buildLabEgressCheck,
  buildLabFlags,
  buildLabFlows,
  buildLabGates,
  buildLabLogs,
  buildLabOverview,
  buildLabProfiles,
  buildLabSessions,
  buildMetrics,
  getDb,
  resetMockDb,
  syncAccountCounts,
} from './mock'
import { ApiError, UnauthorizedError } from './errors'
import type {
  Account,
  AccountServers,
  AccountState,
  ApiKey,
  AuditEvent,
  CreateAccountInput,
  CreateKeyInput,
  CreateKeyResult,
  DeviceView,
  DiagnosticsView,
  EgressCheckResult,
  EgressView,
  FlowsView,
  GateView,
  LabFlags,
  LabDisabled,
  LabLogQuery,
  LabLogs,
  LabOverview,
  LabProfiles,
  Lease,
  Metrics,
  MetricRange,
  MutationLabInput,
  NodeState,
  PanelSettings,
  PanelUser,
  ProxyNode,
  RequestLog,
  SessionView,
  UpdateInfo,
} from './types'

const USE_MOCK = import.meta.env.VITE_USE_MOCK !== 'false'
const API_BASE = (import.meta.env.VITE_API_BASE ?? '').replace(/\/$/, '')
const MOCK_DELAY = import.meta.env.MODE === 'test' ? 0 : 220
const SETTINGS_KEY = 'ajs.settings'
const THEME_KEY = 'ajs.theme'
const TOKEN_KEY = 'ajs.panelToken'

function readSessionToken(): string | null {
  try {
    if (typeof sessionStorage === 'undefined') return null
    return sessionStorage.getItem(TOKEN_KEY)
  } catch {
    return null
  }
}

let panelToken: string | null = import.meta.env.VITE_PANEL_TOKEN ?? readSessionToken()
let accountSeq = 0

/** Deterministic simulated release for the mock-mode update check. */
const MOCK_UPDATE: UpdateInfo = {
  enabled: false,
  current: '0.1.0',
}

export function isMockMode(): boolean {
  return USE_MOCK
}

/** Stores the panel token for this tab only; never renders or logs the value. */
export function setPanelToken(token: string | null): void {
  const value = token?.trim() ? token.trim() : null
  panelToken = value
  try {
    if (typeof sessionStorage === 'undefined') return
    if (value) sessionStorage.setItem(TOKEN_KEY, value)
    else sessionStorage.removeItem(TOKEN_KEY)
  } catch {
    // Private mode or a quota error still leaves the in-memory token usable.
  }
}

export function hasPanelToken(): boolean {
  return panelToken !== null && panelToken.length > 0
}

/** Human-readable daemon endpoint for read-only labels. Never includes the token. */
export function apiEndpointLabel(): string {
  if (USE_MOCK) return 'in-browser mock'
  if (API_BASE) return API_BASE.replace(/^https?:\/\//, '')
  if (typeof window !== 'undefined') return window.location.origin.replace(/^https?:\/\//, '')
  return 'same origin'
}

async function wait(): Promise<void> {
  if (MOCK_DELAY <= 0) return
  const { promise, resolve } = Promise.withResolvers<void>()
  setTimeout(resolve, MOCK_DELAY)
  await promise
}

async function http<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body) headers.set('Content-Type', 'application/json')
  if (panelToken) headers.set('Authorization', `Bearer ${panelToken}`)
  const response = await fetch(`${API_BASE}${path}`, { ...init, headers })
  if (!response.ok) {
    const raw = await response.text().catch(() => '')
    const { code, detail } = parseErrorBody(raw)
    const suffix = detail ? ` — ${detail.slice(0, 200)}` : ''
    if (response.status === 401) {
      // A vendor credential rejection on the account login action is also a
      // 401; it must surface as a failed login, not as an admin token gate.
      if (typeof window !== 'undefined' && !path.includes('/actions/login')) {
        window.dispatchEvent(new Event('ajs:unauthorized'))
      }
      throw new UnauthorizedError(`401 Unauthorized${suffix}`)
    }
    throw new ApiError(response.status, `${response.status} ${response.statusText}${suffix}`, code)
  }
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

/** Reads `{error:{code,message}}`; a non-JSON body stays the detail text. */
function parseErrorBody(raw: string): { code: string | null; detail: string } {
  if (!raw) return { code: null, detail: '' }
  try {
    const parsed = JSON.parse(raw) as { error?: { code?: unknown; message?: unknown } }
    return {
      code: typeof parsed.error?.code === 'string' ? parsed.error.code : null,
      detail: typeof parsed.error?.message === 'string' ? parsed.error.message : raw,
    }
  } catch {
    return { code: null, detail: raw }
  }
}

function clone<T>(value: T): T {
  return structuredClone(value)
}

function readSettings(): PanelSettings {
  if (typeof localStorage === 'undefined') return { ...DEFAULT_SETTINGS }
  try {
    const raw = localStorage.getItem(SETTINGS_KEY)
    if (!raw) return { ...DEFAULT_SETTINGS }
    const parsed = JSON.parse(raw) as Partial<PanelSettings>
    return { ...DEFAULT_SETTINGS, ...parsed }
  } catch {
    return { ...DEFAULT_SETTINGS }
  }
}

function nextEgressIp(current: string): string {
  const pool = ['203.0.113.24', '198.51.100.44', '192.0.2.77', '203.0.113.88', '198.51.100.91']
  const index = pool.indexOf(current)
  return pool[(index + 1) % pool.length] ?? pool[0]
}

export const api = {
  async listAccounts(): Promise<Account[]> {
    if (!USE_MOCK) return http<Account[]>('/v1/accounts')
    await wait()
    return clone(getDb().accounts)
  },

  async createAccount(input: CreateAccountInput): Promise<Account> {
    if (!USE_MOCK) {
      return http<Account>('/v1/accounts', { method: 'POST', body: JSON.stringify(input) })
    }
    await wait()
    const db = getDb()
    const label = input.label.trim()
    if (db.accounts.some((account) => account.label.toLowerCase() === label.toLowerCase())) {
      throw new Error(`An account named “${label}” already exists`)
    }
    const now = Date.now()
    const account: Account = {
      id: `acc-new-${++accountSeq}`,
      label,
      username: input.username.trim(),
      platform: input.platform,
      state: 'active',
      concurrency: input.concurrency,
      regions: input.regions,
      nodeCount: 0,
      liveNodeCount: 0,
      usageBytes: 0,
      expiresAt: new Date(now + 30 * 86_400_000).toISOString(),
      lastLoginAt: new Date(now).toISOString(),
      createdAt: new Date(now).toISOString(),
    }
    db.accounts.unshift(account)
    return clone(account)
  },

  async testAccount(id: string): Promise<Account> {
    if (!USE_MOCK) return http<Account>(`/v1/accounts/${id}/actions/login`, { method: 'POST' })
    await wait()
    const account = getDb().accounts.find((item) => item.id === id)
    if (!account) throw new Error('Account not found')
    account.lastLoginAt = new Date().toISOString()
    if (account.state === 'auth_failed') account.state = 'active'
    return clone(account)
  },

  /**
   * Logout is the only account action that drains nodes and drops the vendor
   * session. Pause is store-only and leaves gates running.
   */
  async logoutAccount(id: string): Promise<Account> {
    if (!USE_MOCK) return http<Account>(`/v1/accounts/${id}/actions/logout`, { method: 'POST' })
    await wait()
    const db = getDb()
    const account = db.accounts.find((item) => item.id === id)
    if (!account) throw new Error('Account not found')
    account.state = 'paused'
    for (const node of db.nodes) {
      if (node.accountId === id && node.state !== 'DOWN') node.state = 'READY'
    }
    db.leases = db.leases.filter((lease) => lease.accountId !== id)
    syncAccountCounts()
    return clone(account)
  },

  async setAccountState(id: string, state: AccountState): Promise<Account> {
    if (!USE_MOCK) {
      return http<Account>(`/v1/accounts/${id}`, { method: 'PATCH', body: JSON.stringify({ state }) })
    }
    await wait()
    const account = getDb().accounts.find((item) => item.id === id)
    if (!account) throw new Error('Account not found')
    account.state = state
    return clone(account)
  },

  async deleteAccounts(ids: string[]): Promise<void> {
    if (!USE_MOCK) {
      await Promise.all(ids.map((id) => http<void>(`/v1/accounts/${id}`, { method: 'DELETE' })))
      return
    }
    await wait()
    const db = getDb()
    db.accounts = db.accounts.filter((account) => !ids.includes(account.id))
    db.nodes = db.nodes.filter((node) => !ids.includes(node.accountId))
    db.leases = db.leases.filter((lease) => !ids.includes(lease.accountId))
    syncAccountCounts()
  },

  async listNodes(): Promise<ProxyNode[]> {
    if (!USE_MOCK) return http<ProxyNode[]>('/v1/nodes')
    await wait()
    return clone(getDb().nodes)
  },

  async setNodeState(id: string, state: NodeState): Promise<ProxyNode> {
    if (!USE_MOCK) {
      const action = state === 'DRAINING' ? 'drain' : 'undrain'
      return http<ProxyNode>(`/v1/nodes/${id}/actions/${action}`, { method: 'POST' })
    }
    await wait()
    const db = getDb()
    const node = db.nodes.find((item) => item.id === id)
    if (!node) throw new Error('Node not found')
    node.state = state
    node.lastProbeAt = new Date().toISOString()
    syncAccountCounts()
    return clone(node)
  },

  async probeNode(id: string): Promise<ProxyNode> {
    if (!USE_MOCK) return http<ProxyNode>(`/v1/nodes/${id}/actions/probe`, { method: 'POST' })
    await wait()
    const node = getDb().nodes.find((item) => item.id === id)
    if (!node) throw new Error('Node not found')
    // Matches the daemon's store action: touch the timestamp only. Latency and
    // egress are unchanged because nothing is dialed.
    node.lastProbeAt = new Date().toISOString()
    return clone(node)
  },

  async listLeases(): Promise<Lease[]> {
    if (!USE_MOCK) return http<Lease[]>('/v1/leases')
    await wait()
    return clone(getDb().leases)
  },

  async rotateLease(id: string): Promise<Lease> {
    if (!USE_MOCK) return http<Lease>(`/v1/leases/${id}/actions/rotate`, { method: 'POST' })
    await wait()
    const db = getDb()
    const lease = db.leases.find((item) => item.id === id)
    if (!lease) throw new Error('Lease not found')
    lease.egressIp = nextEgressIp(lease.egressIp)
    lease.expiresAt = new Date(Date.now() + lease.ttlSeconds * 1000).toISOString()
    lease.requests += 17
    const node = db.nodes.find((item) => item.id === lease.nodeId)
    if (node) node.egressIp = lease.egressIp
    return clone(lease)
  },

  async releaseLease(id: string): Promise<void> {
    if (!USE_MOCK) {
      await http<void>(`/v1/leases/${id}`, { method: 'DELETE' })
      return
    }
    await wait()
    const db = getDb()
    const lease = db.leases.find((item) => item.id === id)
    if (lease) {
      const node = db.nodes.find((item) => item.id === lease.nodeId)
      if (node) node.inUse = Math.max(0, node.inUse - 1)
    }
    db.leases = db.leases.filter((item) => item.id !== id)
  },

  async listUsers(): Promise<PanelUser[]> {
    if (!USE_MOCK) return http<PanelUser[]>('/v1/users')
    await wait()
    return clone(getDb().users)
  },

  async setUserEnabled(id: string, enabled: boolean): Promise<PanelUser> {
    if (!USE_MOCK) {
      return http<PanelUser>(`/v1/users/${id}/${enabled ? 'resume' : 'suspend'}`, { method: 'POST' })
    }
    await wait()
    const user = getDb().users.find((item) => item.id === id)
    if (!user) throw new Error('User not found')
    user.enabled = enabled
    return clone(user)
  },

  async updateAllowlist(id: string, allowlist: string[]): Promise<PanelUser> {
    if (!USE_MOCK) {
      return http<PanelUser>(`/v1/users/${id}`, { method: 'PATCH', body: JSON.stringify({ allowlist }) })
    }
    await wait()
    const user = getDb().users.find((item) => item.id === id)
    if (!user) throw new Error('User not found')
    user.allowlist = allowlist
    return clone(user)
  },

  async listKeys(): Promise<ApiKey[]> {
    if (!USE_MOCK) return http<ApiKey[]>('/v1/apikeys')
    await wait()
    return clone(getDb().keys)
  },

  async createKey(input: CreateKeyInput): Promise<CreateKeyResult> {
    if (!USE_MOCK) {
      return http<CreateKeyResult>('/v1/apikeys', { method: 'POST', body: JSON.stringify(input) })
    }
    await wait()
    const db = getDb()
    const user = db.users.find((item) => item.id === input.userId)
    if (!user) throw new Error('User not found')
    db.keySeq += 1
    const suffix = db.keySeq.toString(16).padStart(24, '0')
    const secret = `ajs_live_mock_${suffix}`
    const key: ApiKey = {
      id: `key-${String(db.keySeq).padStart(2, '0')}`,
      userId: user.id,
      userName: user.name,
      label: input.label.trim() || 'Untitled key',
      prefix: secret.slice(0, 16),
      createdAt: new Date().toISOString(),
      lastUsedAt: null,
      revokedAt: null,
    }
    db.keys.unshift(key)
    return { key: clone(key), secret }
  },

  async revokeKey(id: string): Promise<void> {
    if (!USE_MOCK) {
      await http<void>(`/v1/apikeys/${id}`, { method: 'DELETE' })
      return
    }
    await wait()
    const key = getDb().keys.find((item) => item.id === id)
    if (key) key.revokedAt = new Date().toISOString()
  },

  async listRequests(): Promise<RequestLog[]> {
    if (!USE_MOCK) return http<RequestLog[]>('/v1/requests')
    await wait()
    return clone(getDb().requests)
  },

  async createMutationRequest(input: MutationLabInput): Promise<RequestLog> {
    if (!USE_MOCK) {
      return http<RequestLog>('/v1/requests/mutations', {
        method: 'POST',
        body: JSON.stringify(input),
      })
    }
    await wait()
    const db = getDb()
    const node = db.nodes.find((item) => item.id === input.nodeId)
    if (!node) throw new Error('Node not found')
    db.mutationSeq += 1
    const entry: RequestLog = {
      id: `req-mut-${db.mutationSeq}`,
      ts: new Date().toISOString(),
      userId: 'u-01',
      userName: 'ada.ops',
      accountLabel: node.accountLabel,
      nodeLabel: node.label,
      region: node.region,
      method: input.method,
      target: input.target,
      status: 200,
      bytes: 1_024 + input.body.length,
      latencyMs: Math.max(18, node.latencyMs + 12),
      source: 'mutation-lab',
    }
    db.requests.unshift(entry)
    return clone(entry)
  },

  async listAudit(limit = 20): Promise<AuditEvent[]> {
    if (!USE_MOCK) return http<AuditEvent[]>(`/v1/audit?limit=${limit}`)
    await wait()
    return clone(getDb().audit.slice(0, limit))
  },

  async getMetrics(range: MetricRange): Promise<Metrics> {
    if (!USE_MOCK) return http<Metrics>(`/v1/metrics?range=${range}`)
    await wait()
    return buildMetrics(range, Date.now(), getDb().accounts)
  },

  async getSettings(): Promise<PanelSettings> {
    if (!USE_MOCK) return http<PanelSettings>('/v1/settings')
    await wait()
    return { ...readSettings() }
  },

  async saveSettings(settings: PanelSettings): Promise<PanelSettings> {
    if (!USE_MOCK) {
      return http<PanelSettings>('/v1/settings', { method: 'PATCH', body: JSON.stringify(settings) })
    }
    await wait()
    getDb().settings = { ...settings }
    localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings))
    localStorage.setItem(THEME_KEY, settings.theme)
    return { ...settings }
  },

  async getUpdate(): Promise<UpdateInfo> {
    if (!USE_MOCK) return http<UpdateInfo>('/v1/system/update')
    await wait()
    return clone(MOCK_UPDATE)
  },

  async getLabOverview(): Promise<LabOverview> {
    if (!USE_MOCK) return http<LabOverview>('/v1/lab/overview')
    await wait()
    return buildLabOverview(getDb())
  },

  async listLabGates(): Promise<GateView[] | LabDisabled> {
    if (!USE_MOCK) return http<GateView[] | LabDisabled>('/v1/lab/gates')
    await wait()
    return clone(buildLabGates(getDb()))
  },

  /**
   * Finished gate flows, newest first. The daemon bounds ?limit to 1..500;
   * the page asks for 200 so one poll stays a single small GET.
   */
  async getLabFlows(limit = 200): Promise<FlowsView> {
    if (!USE_MOCK) return http<FlowsView>(`/v1/lab/flows?limit=${limit}`)
    await wait()
    return clone(buildLabFlows(getDb(), limit))
  },

  async listLabSessions(): Promise<SessionView[] | LabDisabled> {
    if (!USE_MOCK) return http<SessionView[] | LabDisabled>('/v1/lab/sessions')
    await wait()
    return clone(buildLabSessions(getDb()))
  },

  async listLabEgress(): Promise<EgressView[] | LabDisabled> {
    if (!USE_MOCK) return http<EgressView[] | LabDisabled>('/v1/lab/egress')
    await wait()
    return clone(buildLabEgress(getDb()))
  },

  /**
   * Guarded observation: the daemon dials the node's loopback SOCKS5 gate and
   * reads https://ifconfig.me/ip. 409 means no running gate, 429 means the
   * per-node 5 s quota is exhausted. Mock mode mirrors both guards.
   */
  async checkLabEgress(nodeId: string): Promise<EgressCheckResult> {
    if (!USE_MOCK) {
      return http<EgressCheckResult>('/v1/lab/egress-check', {
        method: 'POST',
        body: JSON.stringify({ nodeId }),
      })
    }
    await wait()
    return clone(buildLabEgressCheck(getDb(), nodeId))
  },

  async getLabProfiles(): Promise<LabProfiles> {
    if (!USE_MOCK) return http<LabProfiles>('/v1/lab/profiles')
    await wait()
    return clone(buildLabProfiles())
  },

  async getLabFlags(): Promise<LabFlags> {
    if (!USE_MOCK) return http<LabFlags>('/v1/lab/flags')
    await wait()
    return clone(buildLabFlags())
  },

  async getLabLogs(query: LabLogQuery = {}): Promise<LabLogs> {
    if (!USE_MOCK) {
      const params = new URLSearchParams()
      if (query.level && query.level !== 'all') params.set('level', query.level)
      if (query.component && query.component !== 'all') params.set('component', query.component)
      if (query.limit) params.set('limit', String(query.limit))
      if (query.since) params.set('since', query.since)
      const suffix = params.toString()
      return http<LabLogs>(`/v1/lab/logs${suffix ? `?${suffix}` : ''}`)
    }
    await wait()
    return clone(buildLabLogs(Date.now(), query))
  },

  async getLabDiagnostics(): Promise<DiagnosticsView> {
    if (!USE_MOCK) return http<DiagnosticsView>('/v1/lab/diagnostics')
    await wait()
    return clone(buildLabDiagnostics(getDb()))
  },

  async getAccountDevice(id: string): Promise<DeviceView> {
    if (!USE_MOCK) return http<DeviceView>(`/v1/accounts/${id}/device`)
    await wait()
    return clone(buildLabDevice(getDb(), id))
  },

  async getAccountServers(id: string): Promise<AccountServers> {
    if (!USE_MOCK) return http<AccountServers>(`/v1/accounts/${id}/servers`)
    await wait()
    return clone(buildAccountServers(getDb(), id))
  },

  async resetMock(): Promise<void> {
    if (!USE_MOCK) return
    await wait()
    resetMockDb()
    getDb().settings = readSettings()
  },
}
