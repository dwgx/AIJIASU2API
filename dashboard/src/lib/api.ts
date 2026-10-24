import {
  DEFAULT_SETTINGS,
  MOCK_IO_CAPABILITIES,
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
  buildMockExport,
  getDb,
  mockBulkDelete,
  mockImportAccounts,
  resetMockDb,
  syncAccountCounts,
} from './mock'
import { ApiError, UnauthorizedError } from './errors'
import type {
  Account,
  AccountExportDownload,
  AccountExportRequest,
  AccountIOCapabilities,
  AccountImportRequest,
  AccountImportResult,
  AccountServers,
  AccountState,
  ApiKey,
  AuditEvent,
  BulkDeleteRequest,
  BulkDeleteResult,
  CatalogSelection,
  CatalogSnapshot,
  ConnectResult,
  CreateAccountInput,
  CreateKeyInput,
  CreateKeyResult,
  DeviceView,
  DiagnosticsView,
  DisconnectResult,
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
  ServerCatalogQuery,
  ServerView,
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

/**
 * A catalog write rejected by the daemon. P1 routes answer 409/429/502 with a
 * machine `reason` (and sometimes `details`); the dashboard maps that reason to
 * localized copy instead of showing the raw server message. `Retry-After` is
 * parsed for 429 so the row actions can stay disabled for the countdown.
 */
export class CatalogActionError extends ApiError {
  readonly reason: string | null
  readonly details: Record<string, unknown> | null
  readonly retryAfterSec: number | null

  constructor(
    status: number,
    message: string,
    code: string | null = null,
    reason: string | null = null,
    details: Record<string, unknown> | null = null,
    retryAfterSec: number | null = null,
  ) {
    super(status, message, code)
    this.name = 'CatalogActionError'
    this.reason = reason
    this.details = details
    this.retryAfterSec = retryAfterSec
  }
}

interface HttpErrorContext {
  status: number
  statusText: string
  code: string | null
  detail: string
  reason: string | null
  details: Record<string, unknown> | null
  retryAfterSec: number | null
}

function defaultHttpError(context: HttpErrorContext): ApiError {
  const suffix = context.detail ? ` — ${context.detail.slice(0, 200)}` : ''
  return new ApiError(
    context.status,
    `${context.status} ${context.statusText}${suffix}`,
    context.code,
  )
}

function catalogActionError(context: HttpErrorContext): ApiError {
  const suffix = context.detail ? ` — ${context.detail.slice(0, 200)}` : ''
  return new CatalogActionError(
    context.status,
    `${context.status} ${context.statusText}${suffix}`,
    context.code,
    context.reason,
    context.details,
    context.retryAfterSec,
  )
}

async function request(
  path: string,
  init: RequestInit = {},
  errorFactory: (context: HttpErrorContext) => ApiError = defaultHttpError,
): Promise<Response> {
  const headers = new Headers(init.headers)
  if (!headers.has('Accept')) headers.set('Accept', 'application/json')
  if (init.body) headers.set('Content-Type', 'application/json')
  if (panelToken) headers.set('Authorization', `Bearer ${panelToken}`)
  const response = await fetch(`${API_BASE}${path}`, { ...init, headers })
  if (!response.ok) {
    const raw = await response.text().catch(() => '')
    const { code, detail, reason, details } = parseErrorBody(raw)
    const suffix = detail ? ` — ${detail.slice(0, 200)}` : ''
    if (response.status === 401) {
      // A vendor credential rejection on the account login action is also a
      // 401; it must surface as a failed login, not as an admin token gate.
      if (typeof window !== 'undefined' && !path.includes('/actions/login')) {
        window.dispatchEvent(new Event('ajs:unauthorized'))
      }
      throw new UnauthorizedError(`401 Unauthorized${suffix}`)
    }
    throw errorFactory({
      status: response.status,
      statusText: response.statusText,
      code,
      detail,
      reason,
      details,
      retryAfterSec: parseRetryAfter(response.headers.get('Retry-After')),
    })
  }
  return response
}

async function http<T>(
  path: string,
  init: RequestInit = {},
  errorFactory: (context: HttpErrorContext) => ApiError = defaultHttpError,
): Promise<T> {
  const response = await request(path, init, errorFactory)
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

/** UTC `ajs-accounts-YYYYMMDD-HHMMSS` — the daemon's export filename shape. */
function exportTimestamp(now = new Date()): string {
  const pad = (value: number) => String(value).padStart(2, '0')
  return (
    `${now.getUTCFullYear()}${pad(now.getUTCMonth() + 1)}${pad(now.getUTCDate())}` +
    `-${pad(now.getUTCHours())}${pad(now.getUTCMinutes())}${pad(now.getUTCSeconds())}`
  )
}

function exportFilename(header: string | null, extension: string): string {
  if (header) {
    const match = /filename\*?=(?:UTF-8'')?"?([^";]+)"?/i.exec(header)
    if (match?.[1]) {
      try {
        return decodeURIComponent(match[1])
      } catch {
        return match[1]
      }
    }
  }
  return `ajs-accounts-${exportTimestamp()}.${extension}`
}

function exportCount(header: string | null): number {
  const value = Number(header)
  return Number.isFinite(value) && value >= 0 ? value : 0
}

/** `Retry-After` is either delta-seconds or an HTTP date; both become seconds. */
function parseRetryAfter(header: string | null): number | null {
  if (!header) return null
  const seconds = Number(header)
  if (Number.isFinite(seconds) && seconds >= 0) return Math.ceil(seconds)
  const at = Date.parse(header)
  if (!Number.isNaN(at)) return Math.max(0, Math.ceil((at - Date.now()) / 1000))
  return null
}

/**
 * Reads `{error:{code,message,reason,details}}`; a non-JSON body stays the
 * detail text. `reason`/`details` are additive P1 fields, absent on P0 routes.
 */
function parseErrorBody(raw: string): {
  code: string | null
  detail: string
  reason: string | null
  details: Record<string, unknown> | null
} {
  if (!raw) return { code: null, detail: '', reason: null, details: null }
  try {
    const parsed = JSON.parse(raw) as {
      error?: { code?: unknown; message?: unknown; reason?: unknown; details?: unknown }
    }
    const rawDetails = parsed.error?.details
    return {
      code: typeof parsed.error?.code === 'string' ? parsed.error.code : null,
      detail: typeof parsed.error?.message === 'string' ? parsed.error.message : raw,
      reason: typeof parsed.error?.reason === 'string' ? parsed.error.reason : null,
      details:
        typeof rawDetails === 'object' && rawDetails !== null
          ? (rawDetails as Record<string, unknown>)
          : null,
    }
  } catch {
    return { code: null, detail: raw, reason: null, details: null }
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

const SNAPSHOT_PAGE_SIZE = 500
const SNAPSHOT_MAX_PAGES = 50
const snapshotCache = new Map<string, CatalogSnapshot>()
const snapshotInFlight = new Map<string, Promise<CatalogSnapshot>>()
/** Bumped whenever a cached/in-flight snapshot is invalidated. */
let snapshotEpoch = 0

/* --------------------------------------------------------------------------
 * P1 catalog actions — mock state
 *
 * The daemon keeps one persisted target and one FV session per account. The
 * browser mock mirrors that contract on the fixture nodes so the catalog can
 * be reviewed offline: a switch leaves exactly one LIVE node, a disconnect
 * leaves the row in place without a gate, and the target survives both.
 * ------------------------------------------------------------------------ */

const mockSelections = new Map<string, CatalogSelection>()
let mockCatalogSeq = 0

function invalidateSnapshotCache(id: string): void {
  snapshotEpoch += 1
  snapshotCache.delete(id)
  snapshotInFlight.delete(id)
}

interface MockRowEligibility {
  eligible: boolean
  ineligibleReason?: string
}

/** Mirrors DESIGN-CATALOG §2.1 eligibility priority, minus a live vendor call. */
function mockEligibility(
  server: ServerView,
  enabled: boolean,
  stale: boolean,
  accountState: AccountState | undefined,
): MockRowEligibility {
  if (!enabled) return { eligible: false, ineligibleReason: 'login_disabled' }
  if (accountState !== undefined && accountState !== 'active') {
    return { eligible: false, ineligibleReason: 'account_state' }
  }
  if (stale) return { eligible: false, ineligibleReason: 'stale' }
  if (server.status !== 'ok') return { eligible: false, ineligibleReason: 'vendor_status' }
  if (server.fullLoad === true) return { eligible: false, ineligibleReason: 'full_load' }
  if (server.blockingMarks.length > 0) return { eligible: false, ineligibleReason: 'blocked' }
  if (server.memberOnly === true) return { eligible: false, ineligibleReason: 'member_only' }
  return { eligible: true }
}

/** Adds the P1 runtime/selection overlay the daemon will send on each row. */
function mockCatalogOverlay(
  servers: ServerView[],
  accountId: string,
  enabled: boolean,
  stale: boolean,
): ServerView[] {
  const account = getDb().accounts.find((item) => item.id === accountId)
  const selection = mockSelections.get(accountId)
  return servers.map((server) => ({
    ...server,
    desired: selection?.serverId === server.id,
    connected: server.nodeState === 'LIVE' || server.nodeState === 'DRAINING',
    serving: server.nodeState === 'LIVE',
    autoStarted: server.nodeState === 'LIVE' || server.nodeState === 'DRAINING',
    ...mockEligibility(server, enabled, stale, account?.state),
  }))
}

function mockCatalogError(
  status: number,
  reason: string | null,
  message: string,
  details: Record<string, unknown> | null = null,
  retryAfterSec: number | null = null,
): CatalogActionError {
  const code =
    status === 404
      ? 'NOT_FOUND'
      : status === 409
        ? 'CONFLICT'
        : status === 429
          ? 'RATE_LIMITED'
          : status >= 500
            ? 'INTERNAL'
            : 'INVALID_ARGUMENT'
  return new CatalogActionError(status, `${status} ${message}`, code, reason, details, retryAfterSec)
}

function mockCatalogAudit(
  action: string,
  target: string,
  detail: string,
  now: number,
): string {
  mockCatalogSeq += 1
  const id = `audit-cat-${mockCatalogSeq}`
  getDb().audit.unshift({
    id,
    ts: new Date(now).toISOString(),
    severity: 'info',
    actor: 'operator',
    action,
    target,
    detail,
  })
  return id
}

/** Validates the same preconditions the P1 daemon enforces before a write. */
function mockCatalogTarget(
  accountId: string,
  serverId: string,
  now: number,
): { account: Account; server: ServerView } {
  const db = getDb()
  const account = db.accounts.find((item) => item.id === accountId)
  if (!account) throw mockCatalogError(404, null, `account ${accountId} not found`)
  const page = buildAccountServers(db, accountId, now, { offset: 0, limit: 500 })
  if (page.enabled === false) {
    throw mockCatalogError(409, 'login_disabled', 'account login is disabled')
  }
  if (account.state !== 'active') {
    throw mockCatalogError(409, 'account_state', `account ${accountId} is ${account.state}`)
  }
  if (page.stale) {
    throw mockCatalogError(409, 'login_required', 'account has no retained login')
  }
  const server = page.servers.find((row) => row.id === serverId)
  if (!server) {
    throw mockCatalogError(404, 'unknown_server', `server ${serverId} is not in the retained catalog`)
  }
  const eligibility = mockEligibility(server, true, page.stale, account.state)
  if (!eligibility.eligible) {
    throw mockCatalogError(
      409,
      'server_ineligible',
      `server ${server.title} is not eligible`,
      eligibility.ineligibleReason ? { ineligibleReason: eligibility.ineligibleReason } : null,
    )
  }
  return { account, server }
}

function mockSelection(accountId: string, server: ServerView, now: number): CatalogSelection {
  return {
    accountId,
    serverId: server.id,
    title: server.title,
    updatedAt: new Date(now).toISOString(),
    updatedBy: 'operator',
  }
}

function mockConnect(
  accountId: string,
  serverId: string,
  now: number,
  trigger: 'connect' | 'reconnect',
): ConnectResult {
  const { account, server } = mockCatalogTarget(accountId, serverId, now)
  const db = getDb()
  const running = db.nodes.filter(
    (node) =>
      node.accountId === accountId && (node.state === 'LIVE' || node.state === 'DRAINING'),
  )
  const target = db.nodes.find((node) => node.accountId === accountId && node.label === serverId)
  if (!target) {
    throw mockCatalogError(404, 'unknown_server', `server ${serverId} has no runtime node`)
  }
  if (running.length === 1 && running[0]?.id === target.id) {
    return {
      accountId,
      serverId,
      nodeId: target.id,
      nodeState: target.state,
      gatePort: target.gatePort,
      egressIp: target.egressIp,
      durationMs: 0,
      warnings: [],
      alreadyConnected: true,
    }
  }

  // Commit like the daemon: one node survives, every other gate closes. The
  // fixture seeds demo accounts with more than one LIVE node; a real daemon
  // answers 409 multiple_sessions there. The mock commits instead so the P1
  // flow stays reviewable offline; tests inject that 409 through the API spy.
  const previous = running.find((node) => node.id !== target.id)
  for (const node of running) {
    if (node.id === target.id) continue
    node.state = 'READY'
    node.lastProbeAt = new Date(now).toISOString()
    db.leases = db.leases.filter((lease) => lease.nodeId !== node.id)
  }
  target.state = 'LIVE'
  target.lastProbeAt = new Date(now).toISOString()
  syncAccountCounts()

  // A manual connect persists the operator's target; reconnect keeps it.
  const selection = mockSelection(accountId, server, now)
  mockSelections.set(accountId, selection)

  const durationMs = trigger === 'reconnect' ? 900 : 1200
  const auditId = mockCatalogAudit(
    trigger === 'reconnect' ? 'catalog.reconnect' : previous ? 'catalog.switch' : 'catalog.connect',
    `${account.label} · ${server.title}`,
    `manual · from ${previous?.label ?? 'none'} · ${durationMs}ms`,
    now,
  )
  invalidateSnapshotCache(accountId)
  return {
    accountId,
    serverId,
    nodeId: target.id,
    nodeState: 'LIVE',
    gatePort: target.gatePort,
    egressIp: target.egressIp,
    switchedFrom: previous?.label,
    durationMs,
    warnings: [],
    auditId,
    alreadyConnected: false,
  }
}

function mockDisconnect(accountId: string, now: number): DisconnectResult {
  const db = getDb()
  const account = db.accounts.find((item) => item.id === accountId)
  if (!account) throw mockCatalogError(404, null, `account ${accountId} not found`)
  const running = db.nodes.filter(
    (node) =>
      node.accountId === accountId && (node.state === 'LIVE' || node.state === 'DRAINING'),
  )
  if (running.length === 0) {
    return { accountId, disconnected: false, alreadyDisconnected: true }
  }
  for (const node of running) {
    node.state = 'READY'
    node.lastProbeAt = new Date(now).toISOString()
    db.leases = db.leases.filter((lease) => lease.nodeId !== node.id)
  }
  syncAccountCounts()
  const auditId = mockCatalogAudit(
    'catalog.disconnect',
    `${account.label} · ${running.map((node) => node.label).join(', ')}`,
    `manual · closed ${running.length} · target kept`,
    now,
  )
  invalidateSnapshotCache(accountId)
  return { accountId, disconnected: true, alreadyDisconnected: false, warnings: [], auditId }
}

function mockReconnect(accountId: string, now: number): ConnectResult {
  const db = getDb()
  const account = db.accounts.find((item) => item.id === accountId)
  if (!account) throw mockCatalogError(404, null, `account ${accountId} not found`)
  if (account.state !== 'active') {
    throw mockCatalogError(409, 'account_state', `account ${accountId} is ${account.state}`)
  }
  const running = db.nodes.filter(
    (node) =>
      node.accountId === accountId && (node.state === 'LIVE' || node.state === 'DRAINING'),
  )
  if (running.length > 1) {
    throw mockCatalogError(
      409,
      'multiple_sessions',
      `account ${accountId} has ${running.length} running sessions`,
    )
  }
  if (running.length === 1) {
    const node = running[0]!
    // Same preconditions as a connect to the same server (design §3.5).
    mockCatalogTarget(accountId, node.label, now)
    node.state = 'LIVE'
    node.lastProbeAt = new Date(now).toISOString()
    syncAccountCounts()
    const auditId = mockCatalogAudit(
      'catalog.reconnect',
      `${account.label} · ${node.label}`,
      'manual · reopen · 900ms',
      now,
    )
    invalidateSnapshotCache(accountId)
    return {
      accountId,
      serverId: node.label,
      nodeId: node.id,
      nodeState: 'LIVE',
      gatePort: node.gatePort,
      egressIp: node.egressIp,
      durationMs: 900,
      warnings: [],
      auditId,
      alreadyConnected: false,
    }
  }
  const selection = mockSelections.get(accountId)
  if (!selection) {
    throw mockCatalogError(409, 'no_target', `account ${accountId} has no current server or target`)
  }
  return mockConnect(accountId, selection.serverId, now, 'reconnect')
}

async function requestAccountServers(
  id: string,
  query: ServerCatalogQuery = {},
): Promise<AccountServers> {
  const params = new URLSearchParams()
  if (query.q?.trim()) params.set('q', query.q.trim())
  if (query.region) params.set('region', query.region)
  if (query.status) params.set('status', query.status)
  if (query.selected !== undefined) params.set('selected', String(query.selected))
  if (query.province) params.set('province', query.province)
  if (query.city) params.set('city', query.city)
  params.set('offset', String(Math.max(0, Math.trunc(query.offset ?? 0))))
  params.set('limit', String(Math.min(500, Math.max(1, Math.trunc(query.limit ?? 50)))))
  if (query.sort) params.set('sort', query.sort)
  if (!USE_MOCK) return http<AccountServers>(`/v1/accounts/${id}/servers?${params.toString()}`)
  await wait()
  const db = getDb()
  const page = buildAccountServers(db, id, Date.now(), query)
  return clone({
    ...page,
    servers: mockCatalogOverlay(page.servers, id, page.enabled !== false, page.stale),
  })
}

/**
 * Reads every page of one account's retained catalog. The map and the
 * province filter need the whole set, while the table keeps using the server's
 * page envelope. A cached snapshot is reused when it is at least as new as the
 * caller's known `fetchedAt`, so a catalog poll that did not refresh the vendor
 * snapshot costs no extra requests; concurrent callers share one in-flight
 * fetch, and an invalidation epoch stops a stale pass from repopulating the
 * cache after a reset or an explicit refresh.
 */
async function requestCatalogSnapshot(
  id: string,
  knownFetchedAt?: string | null,
): Promise<CatalogSnapshot> {
  const cached = snapshotCache.get(id)
  if (cached) {
    // `undefined` means "the caller has not read the catalog timestamp yet".
    if (knownFetchedAt === undefined) return cached
    if (knownFetchedAt === null) {
      // An explicit null means the daemon reports no snapshot; only a cached
      // no-snapshot may answer it, never an older map.
      if (cached.fetchedAt === null) return cached
    } else if (cached.fetchedAt !== null) {
      // The snapshot pass can observe a newer daemon snapshot than the
      // catalog response that triggered it; newer-or-equal is current enough.
      // Refetch only when the cache is older than the caller's known snapshot.
      const cachedAt = Date.parse(cached.fetchedAt)
      const knownAt = Date.parse(knownFetchedAt)
      if (Number.isNaN(cachedAt) || Number.isNaN(knownAt)) {
        if (cached.fetchedAt === knownFetchedAt) return cached
      } else if (cachedAt >= knownAt) {
        return cached
      }
    }
  }
  const inFlight = snapshotInFlight.get(id)
  if (inFlight) return inFlight

  const epoch = snapshotEpoch
  let request: Promise<CatalogSnapshot>
  request = (async (): Promise<CatalogSnapshot> => {
    const first = await requestAccountServers(id, { offset: 0, limit: SNAPSHOT_PAGE_SIZE })
    const servers = [...first.servers]
    let offset = first.nextOffset
    let hasMore = first.hasMore
    let pages = 1
    while (hasMore) {
      if (pages >= SNAPSHOT_MAX_PAGES) {
        throw new Error(
          `catalog snapshot for ${id} exceeds ${SNAPSHOT_MAX_PAGES * SNAPSHOT_PAGE_SIZE} servers`,
        )
      }
      const page = await requestAccountServers(id, { offset, limit: SNAPSHOT_PAGE_SIZE })
      if (page.servers.length === 0 || page.nextOffset <= offset) break
      servers.push(...page.servers)
      offset = page.nextOffset
      hasMore = page.hasMore
      pages += 1
    }
    // The catalog can change between pages. Rather than showing counts that
    // silently miss or duplicate rows, fail the read: the map card offers a
    // retry and the table keeps its server-paged view.
    const unique = new Map(servers.map((server) => [server.id, server]))
    if (unique.size !== first.total) {
      throw new Error(
        `catalog snapshot for ${id} returned ${unique.size} of ${first.total} servers`,
      )
    }
    const snapshot: CatalogSnapshot = {
      enabled: first.enabled !== false,
      reason: first.reason,
      accountId: id,
      snapshotId: first.snapshotId,
      fetchedAt: first.fetchedAt,
      stale: first.stale,
      total: first.total,
      servers: [...unique.values()],
    }
    // A reset/invalidate during the pass makes this result stale; do not let
    // it repopulate the cache or evict a newer in-flight request.
    if (epoch === snapshotEpoch) snapshotCache.set(id, snapshot)
    return snapshot
  })().finally(() => {
    if (snapshotInFlight.get(id) === request) snapshotInFlight.delete(id)
  })

  snapshotInFlight.set(id, request)
  return request
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
    invalidateSnapshotCache(id)
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
    mockSelections.delete(id)
    invalidateSnapshotCache(id)
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
    invalidateSnapshotCache(id)
    return clone(account)
  },

  /** GET /v1/accounts/io/capabilities — limits and plaintext/encrypted gates. */
  async accountIOCapabilities(): Promise<AccountIOCapabilities> {
    if (!USE_MOCK) return http<AccountIOCapabilities>('/v1/accounts/io/capabilities')
    await wait()
    return clone(MOCK_IO_CAPABILITIES)
  },

  /**
   * POST /v1/accounts/export — returns the file as a Blob plus the
   * server-supplied filename and counts. The caller downloads it; nothing is
   * written to storage and the token never appears in a URL.
   */
  async exportAccounts(input: AccountExportRequest): Promise<AccountExportDownload> {
    if (!USE_MOCK) {
      const response = await request('/v1/accounts/export', {
        method: 'POST',
        body: JSON.stringify(input),
      })
      const blob = await response.blob()
      return {
        blob,
        filename: exportFilename(
          response.headers.get('Content-Disposition'),
          input.format === 'encrypted' ? 'ajsx' : input.format,
        ),
        counts: {
          accounts: exportCount(response.headers.get('X-AJS-Export-Count')),
          missingSecrets: exportCount(response.headers.get('X-AJS-Export-Missing-Secrets')),
          devices: exportCount(response.headers.get('X-AJS-Export-Devices')),
        },
      }
    }
    await wait()
    if (input.format === 'encrypted') {
      throw new ApiError(409, 'Encrypted export requires the live daemon', 'CONFLICT')
    }
    const db = getDb()
    const ids = input.ids?.length ? input.ids : db.accounts.map((account) => account.id)
    if (ids.length > MOCK_IO_CAPABILITIES.maxExportAccounts) {
      throw new ApiError(400, 'too many accounts to export', 'INVALID_ARGUMENT')
    }
    const file = buildMockExport(ids, input.format)
    return {
      blob: new Blob([file.content], { type: file.mime }),
      filename: `ajs-accounts-${exportTimestamp()}.${file.extension}`,
      counts: { ...file.counts, missingSecrets: file.counts.accounts },
    }
  },

  /** POST /v1/accounts/import — dry-run preview or apply. */
  async importAccounts(input: AccountImportRequest): Promise<AccountImportResult> {
    if (!USE_MOCK) {
      return http<AccountImportResult>('/v1/accounts/import', {
        method: 'POST',
        body: JSON.stringify(input),
      })
    }
    await wait()
    return mockImportAccounts(input)
  },

  /** POST /v1/accounts/bulk-delete — one result per id, sequentially applied. */
  async bulkDeleteAccounts(input: BulkDeleteRequest): Promise<BulkDeleteResult> {
    if (!USE_MOCK) {
      return http<BulkDeleteResult>('/v1/accounts/bulk-delete', {
        method: 'POST',
        body: JSON.stringify(input),
      })
    }
    await wait()
    const result = mockBulkDelete(input.ids, input.mode, input.dryRun)
    for (const item of result.results) {
      if (item.status !== 'deleted') continue
      mockSelections.delete(item.id)
      invalidateSnapshotCache(item.id)
    }
    return result
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

  async getAccountServers(id: string, query: ServerCatalogQuery = {}): Promise<AccountServers> {
    return requestAccountServers(id, query)
  },

  /**
   * The whole retained catalog for one account, paged 500 rows per request.
   * `knownFetchedAt` lets a caller reuse the cached snapshot when the catalog
   * has not changed. Not the table path: the table stays server-paged.
   */
  async getCatalogSnapshot(
    id: string,
    knownFetchedAt?: string | null,
  ): Promise<CatalogSnapshot> {
    return requestCatalogSnapshot(id, knownFetchedAt)
  },

  /** Drops the cached full catalog so the next read fetches every page again. */
  invalidateCatalogSnapshot(id: string): void {
    invalidateSnapshotCache(id)
  },

  /**
   * GET /v1/accounts/{id}/selection. A P0 daemon has no route yet, so a 404
   * reads as "no target" instead of blocking the catalog.
   */
  async getSelection(id: string): Promise<CatalogSelection | null> {
    if (!USE_MOCK) {
      try {
        const selection = await http<CatalogSelection | null>(
          `/v1/accounts/${encodeURIComponent(id)}/selection`,
        )
        return selection ?? null
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return null
        throw error
      }
    }
    await wait()
    const selection = mockSelections.get(id)
    return selection ? clone(selection) : null
  },

  /** PUT /v1/accounts/{id}/selection — pins the next login's desired server. */
  async setSelection(
    id: string,
    serverId: string,
    snapshotId?: string,
  ): Promise<CatalogSelection> {
    if (!USE_MOCK) {
      return http<CatalogSelection>(
        `/v1/accounts/${encodeURIComponent(id)}/selection`,
        {
          method: 'PUT',
          body: JSON.stringify({ serverId, ...(snapshotId ? { snapshotId } : {}) }),
        },
        catalogActionError,
      )
    }
    await wait()
    const now = Date.now()
    const { account, server } = mockCatalogTarget(id, serverId, now)
    const previous = mockSelections.get(id)
    const selection = mockSelection(id, server, now)
    mockSelections.set(id, selection)
    mockCatalogAudit(
      'catalog.select',
      `${account.label} · ${server.title}`,
      `manual · from ${previous?.serverId ?? 'none'}`,
      now,
    )
    invalidateSnapshotCache(id)
    return clone(selection)
  },

  /** DELETE /v1/accounts/{id}/selection — idempotent; keeps the session. */
  async clearSelection(id: string): Promise<void> {
    if (!USE_MOCK) {
      await http<void>(
        `/v1/accounts/${encodeURIComponent(id)}/selection`,
        { method: 'DELETE' },
        catalogActionError,
      )
      return
    }
    await wait()
    const previous = mockSelections.get(id)
    if (!previous) return
    mockSelections.delete(id)
    const account = getDb().accounts.find((item) => item.id === id)
    mockCatalogAudit(
      'catalog.unselect',
      `${account?.label ?? id} · ${previous.title}`,
      `manual · from ${previous.serverId}`,
      Date.now(),
    )
    invalidateSnapshotCache(id)
  },

  /** POST /v1/accounts/{id}/actions/connect — guarded single-session switch. */
  async connectAccountServer(
    id: string,
    serverId: string,
    snapshotId?: string,
  ): Promise<ConnectResult> {
    if (!USE_MOCK) {
      return http<ConnectResult>(
        `/v1/accounts/${encodeURIComponent(id)}/actions/connect`,
        {
          method: 'POST',
          body: JSON.stringify({
            serverId,
            ...(snapshotId ? { snapshotId } : {}),
            confirm: true,
          }),
        },
        catalogActionError,
      )
    }
    await wait()
    return clone(mockConnect(id, serverId, Date.now(), 'connect'))
  },

  /** POST /v1/accounts/{id}/actions/disconnect — closes the account's session. */
  async disconnectAccount(id: string): Promise<DisconnectResult> {
    if (!USE_MOCK) {
      return http<DisconnectResult>(
        `/v1/accounts/${encodeURIComponent(id)}/actions/disconnect`,
        { method: 'POST', body: JSON.stringify({ confirm: true }) },
        catalogActionError,
      )
    }
    await wait()
    return clone(mockDisconnect(id, Date.now()))
  },

  /** POST /v1/accounts/{id}/actions/reconnect — reopens current or target. */
  async reconnectAccount(id: string): Promise<ConnectResult> {
    if (!USE_MOCK) {
      return http<ConnectResult>(
        `/v1/accounts/${encodeURIComponent(id)}/actions/reconnect`,
        { method: 'POST', body: JSON.stringify({ confirm: true }) },
        catalogActionError,
      )
    }
    await wait()
    return clone(mockReconnect(id, Date.now()))
  },

  async resetMock(): Promise<void> {
    if (!USE_MOCK) return
    await wait()
    resetMockDb()
    mockSelections.clear()
    mockCatalogSeq = 0
    snapshotEpoch += 1
    snapshotCache.clear()
    snapshotInFlight.clear()
    getDb().settings = readSettings()
  },
}
