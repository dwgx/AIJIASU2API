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
  mockAssignDevice,
  mockAuditExportCsv,
  mockBulkDelete,
  mockCreateBackup,
  mockCreateScopedKey,
  mockEventsPage,
  mockExportDevicePool,
  mockGenerateDevices,
  mockGetDevicePoolRecord,
  mockImportAccounts,
  mockImportDevicePool,
  mockInspectBackup,
  mockListDevicePool,
  mockPatchDevicePoolRecord,
  mockPurgeDevice,
  mockQuarantineDevice,
  mockReleaseDevice,
  mockRestoreBackup,
  mockRetireDevice,
  mockRevokeScopedKey,
  resetMockDb,
  syncAccountCounts,
} from './mock'
import { ApiError, DevicePoolError, UnauthorizedError } from './errors'
import { WEBHOOK_EVENTS } from './types'
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
  AuditExportDownload,
  AuditExportQuery,
  BackupDownload,
  BackupInspectView,
  BackupRestoreInput,
  BackupRestoreView,
  BulkDeleteRequest,
  BulkDeleteResult,
  CatalogSelection,
  CatalogSnapshot,
  ConnectResult,
  CreateAccountInput,
  CreateKeyInput,
  CreateKeyResult,
  CreateLeaseInput,
  CreateLeaseResult,
  Delivery,
  DeviceExportEnvelope,
  DeviceGenerateInput,
  DeviceImportConflict,
  DeviceImportResult,
  DevicePoolExportInput,
  DevicePoolImportInput,
  DevicePoolPatchInput,
  DevicePoolQuery,
  DevicePoolRecord,
  DevicePoolView,
  DeviceView,
  DiagnosticsView,
  DisconnectResult,
  EgressCheckResult,
  EgressView,
  EventsPage,
  EventsQuery,
  FlowsView,
  GateView,
  HealthView,
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
  NodeHealth,
  NodeState,
  PanelSettings,
  PanelUser,
  ProxyNode,
  RenewalAccountView,
  RenewalPlan,
  RenewalPlansView,
  RequestLog,
  Schedule,
  ScheduleCreateInput,
  ScheduleGuard,
  SchedulePatchInput,
  SchedulePreview,
  SchedulePreviewInput,
  ScheduleRun,
  ScheduleRunInput,
  ScheduleTrigger,
  SchedulesPayload,
  ScopedKey,
  ScopedKeyCreateInput,
  ScopedKeyCreated,
  ServerCatalogQuery,
  ServerView,
  SessionView,
  UpdateInfo,
  UsageQuery,
  UsageSummary,
  UsageTarget,
  WebhookCreate,
  WebhookCreated,
  WebhookDeliveriesQuery,
  WebhookEndpoint,
  WebhookEvent,
  WebhookPatch,
} from './types'
import {
  SYSTEM_SCHEDULE_ID,
  computeSchedulePreview,
  isDestructiveAction,
  nextRunAtFor,
  parseCronExpression,
} from './schedules'

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
  /** The parsed error document; device-pool import adds a `conflicts` array. */
  body: Record<string, unknown> | null
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

/**
 * A schedule write rejected by the daemon. 409/412/429 carry a machine
 * `reason`; the page maps `confirmation_required`, `in_flight` and
 * `stale_revision` to localized copy and keeps the row disabled for a 429
 * `Retry-After`.
 */
export class ScheduleActionError extends ApiError {
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
    this.name = 'ScheduleActionError'
    this.reason = reason
    this.details = details
    this.retryAfterSec = retryAfterSec
  }
}

function scheduleActionError(context: HttpErrorContext): ApiError {
  const suffix = context.detail ? ` — ${context.detail.slice(0, 200)}` : ''
  return new ScheduleActionError(
    context.status,
    `${context.status} ${context.statusText}${suffix}`,
    context.code,
    context.reason,
    context.details,
    context.retryAfterSec,
  )
}

/**
 * Reads the top-level `conflicts` array of a 409 import rejection. Unknown
 * shapes are dropped rather than rendered as blank rows.
 */
function parseDeviceConflicts(raw: unknown): DeviceImportConflict[] {
  if (!Array.isArray(raw)) return []
  const out: DeviceImportConflict[] = []
  for (const item of raw) {
    if (typeof item !== 'object' || item === null) continue
    const rec = item as Record<string, unknown>
    if (typeof rec.deviceId !== 'string' || typeof rec.reason !== 'string') continue
    out.push({
      deviceId: rec.deviceId,
      reason: rec.reason,
      existingFingerprint:
        typeof rec.existingFingerprint === 'string' ? rec.existingFingerprint : '',
      importedFingerprint:
        typeof rec.importedFingerprint === 'string' ? rec.importedFingerprint : '',
    })
  }
  return out
}

function devicePoolError(context: HttpErrorContext): ApiError {
  const suffix = context.detail ? ` — ${context.detail.slice(0, 200)}` : ''
  return new DevicePoolError(
    context.status,
    `${context.status} ${context.statusText}${suffix}`,
    context.code,
    context.reason,
    parseDeviceConflicts(context.body?.conflicts),
  )
}

/** Empty filters are omitted so the daemon applies its own defaults. */
function devicePoolQueryString(query: DevicePoolQuery): string {
  const params = new URLSearchParams()
  if (query.state) params.set('state', query.state)
  if (query.platform) params.set('platform', query.platform)
  if (query.profile) params.set('profile', query.profile)
  if (query.accountId) params.set('accountId', query.accountId)
  if (query.q) params.set('q', query.q)
  if (query.limit !== undefined) params.set('limit', String(query.limit))
  if (query.offset !== undefined) params.set('offset', String(query.offset))
  const search = params.toString()
  return search ? `?${search}` : ''
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
    const { code, detail, reason, details, body } = parseErrorBody(raw)
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
      body,
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

/** Reads the server's Content-Disposition filename, or uses the fallback. */
function attachmentFilename(header: string | null, fallback: string): string {
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
  return fallback
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
 * `body` is the whole document: device-pool import adds a top-level
 * `conflicts` array next to the error envelope.
 */
function parseErrorBody(raw: string): {
  code: string | null
  detail: string
  reason: string | null
  details: Record<string, unknown> | null
  body: Record<string, unknown> | null
} {
  if (!raw) return { code: null, detail: '', reason: null, details: null, body: null }
  try {
    const parsed = JSON.parse(raw) as {
      error?: { code?: unknown; message?: unknown; reason?: unknown; details?: unknown }
      conflicts?: unknown
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
      body:
        typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
          ? (parsed as Record<string, unknown>)
          : null,
    }
  } catch {
    return { code: null, detail: raw, reason: null, details: null, body: null }
  }
}

function clone<T>(value: T): T {
  return structuredClone(value)
}

/**
 * A pasted envelope is JSON text; the daemon expects the document itself, not
 * a quoted string. Parsing here keeps that mistake out of the request body.
 */
function parseEnvelopeDocument(envelope: string | object): Record<string, unknown> {
  if (typeof envelope !== 'string') return envelope as Record<string, unknown>
  let parsed: unknown
  try {
    parsed = JSON.parse(envelope)
  } catch {
    throw new ApiError(400, 'envelope must be a JSON object', 'INVALID_ARGUMENT')
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new ApiError(400, 'envelope must be a JSON object', 'INVALID_ARGUMENT')
  }
  return parsed as Record<string, unknown>
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

/** Parses the panel's sticky TTL vocabulary ("8h", "24h", "7d") to seconds. */
function leaseTtlSeconds(value: string): number | null {
  const trimmed = value.trim()
  if (!trimmed) return null
  if (trimmed.endsWith('d')) {
    const days = Number(trimmed.slice(0, -1))
    return Number.isInteger(days) && days > 0 ? days * 86_400 : null
  }
  const match = /^(\d+)(h|m|s)$/.exec(trimmed)
  if (!match) return null
  const amount = Number(match[1])
  if (!Number.isFinite(amount) || amount <= 0) return null
  return amount * (match[2] === 'h' ? 3600 : match[2] === 'm' ? 60 : 1)
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

/* --------------------------------------------------------------------------
 * Schedules — mock parity with daemon/internal/model/schedule.go
 *
 * Mirrors the parts the UI can observe: revision/If-Match, dry-run defaults,
 * ARM/RUN confirm tokens, trigger validation, preview and run history. Cron is
 * evaluated in UTC; the live daemon owns IANA timezone math.
 * ------------------------------------------------------------------------ */

interface MockScheduleState {
  schedules: Schedule[]
  runs: ScheduleRun[]
  seq: number
  runSeq: number
}

let mockScheduleState: MockScheduleState | null = null

function mockDefaultGuard(): ScheduleGuard {
  return { cooldownSec: 600, maxRunsPerDay: 24, dryRun: true }
}

function buildMockScheduleState(now = Date.now()): MockScheduleState {
  const iso = (ms: number) => new Date(ms).toISOString()
  const nightlyTrigger: ScheduleTrigger = { kind: 'cron', expr: '0 3 * * *', timezone: 'UTC' }
  const watchdogTrigger: ScheduleTrigger = {
    kind: 'interval',
    everySec: 3600,
    jitterSec: 120,
    timezone: 'UTC',
  }
  const weekendTrigger: ScheduleTrigger = { kind: 'cron', expr: '0 6 * * 6', timezone: 'UTC' }
  const onceTrigger: ScheduleTrigger = { kind: 'once', at: iso(now + 2 * 3_600_000), timezone: 'UTC' }
  const nightly: Schedule = {
    id: 'sch-001',
    revision: 1,
    name: 'Nightly rotate',
    enabled: true,
    owner: 'admin',
    action: 'account.rotate',
    target: {
      kind: 'account',
      id: 'acc-01',
      params: {
        strategy: 'pool',
        serverIds: ['cn-sh-01', 'hk-01'],
        excludeCurrent: true,
        rollback: true,
      },
    },
    trigger: nightlyTrigger,
    guard: { cooldownSec: 600, maxRunsPerDay: 6, dryRun: false },
    misfirePolicy: 'skip',
    misfireGraceSec: 300,
    concurrencyPolicy: 'forbid',
    createdAt: iso(now - 20 * 86_400_000),
    updatedAt: iso(now - 2 * 86_400_000),
    lastRunAt: iso(now - 21 * 3_600_000),
    lastScheduledAt: iso(now - 21 * 3_600_000),
    lastResult: 'ok',
    nextRunAt: nextRunAtFor(nightlyTrigger, undefined, now),
    runCountToday: 1,
    consecutiveFailures: 0,
    degraded: false,
    cursor: 1,
  }
  const watchdog: Schedule = {
    id: 'sch-002',
    revision: 1,
    name: 'Reconnect watchdog',
    enabled: true,
    owner: 'admin',
    action: 'account.reconnect',
    target: { kind: 'account', id: 'acc-03', params: { timeoutSeconds: 30 } },
    trigger: watchdogTrigger,
    guard: { cooldownSec: 600, maxRunsPerDay: 24, dryRun: true },
    misfirePolicy: 'skip',
    misfireGraceSec: 300,
    concurrencyPolicy: 'forbid',
    createdAt: iso(now - 9 * 86_400_000),
    updatedAt: iso(now - 86_400_000),
    lastRunAt: iso(now - 20 * 60_000),
    lastScheduledAt: iso(now - 20 * 60_000),
    lastResult: 'dry_run',
    nextRunAt: nextRunAtFor(watchdogTrigger, undefined, now),
    runCountToday: 2,
    consecutiveFailures: 0,
    degraded: false,
  }
  const weekend: Schedule = {
    id: 'sch-003',
    revision: 3,
    name: 'Weekend rotate',
    enabled: true,
    owner: 'admin',
    action: 'account.rotate',
    target: { kind: 'account', id: 'acc-02', params: { strategy: 'same-region', region: 'jp-tyo' } },
    trigger: weekendTrigger,
    window: { days: [6], start: '06:00', end: '08:00' },
    guard: { cooldownSec: 0, maxRunsPerDay: 4, dryRun: true },
    misfirePolicy: 'skip',
    misfireGraceSec: 300,
    concurrencyPolicy: 'queue',
    createdAt: iso(now - 40 * 86_400_000),
    updatedAt: iso(now - 6 * 3_600_000),
    lastRunAt: iso(now - 6 * 3_600_000),
    lastScheduledAt: iso(now - 6 * 3_600_000),
    lastResult: 'error',
    lastError: 'transient · network',
    nextRunAt: nextRunAtFor(weekendTrigger, { days: [6], start: '06:00', end: '08:00' }, now),
    runCountToday: 0,
    consecutiveFailures: 3,
    pausedReason: 'network',
    degraded: true,
  }
  const oneShot: Schedule = {
    id: 'sch-004',
    revision: 1,
    name: 'One-shot rotate',
    enabled: true,
    owner: 'admin',
    action: 'account.rotate',
    target: { kind: 'account', id: 'acc-04', params: { strategy: 'next-server' } },
    trigger: onceTrigger,
    guard: { cooldownSec: 0, maxRunsPerDay: 1, dryRun: true },
    misfirePolicy: 'skip',
    misfireGraceSec: 300,
    concurrencyPolicy: 'forbid',
    createdAt: iso(now - 3 * 3_600_000),
    updatedAt: iso(now - 3 * 3_600_000),
    nextRunAt: nextRunAtFor(onceTrigger, undefined, now),
    runCountToday: 0,
    consecutiveFailures: 0,
    degraded: false,
  }
  const runs: ScheduleRun[] = [
    {
      id: 'run-0008',
      scheduleId: 'sch-001',
      trigger: 'cron',
      startedAt: iso(now - 21 * 3_600_000),
      finishedAt: iso(now - 21 * 3_600_000 + 2_400),
      state: 'succeeded',
      result: 'ok',
      detail: 'rotated to hk-01',
      actor: 'system',
    },
    {
      id: 'run-0007',
      scheduleId: 'sch-002',
      trigger: 'interval',
      startedAt: iso(now - 20 * 60_000),
      finishedAt: iso(now - 20 * 60_000 + 180),
      state: 'dry_run',
      result: 'dry_run',
      detail: 'dry run · reconnect acc-03',
      actor: 'system',
    },
    {
      id: 'run-0006',
      scheduleId: 'sch-003',
      trigger: 'cron',
      startedAt: iso(now - 6 * 3_600_000),
      finishedAt: iso(now - 6 * 3_600_000 + 540),
      state: 'failed',
      result: 'error',
      reason: 'no_eligible_server',
      errorClass: 'permanent',
      detail: 'catalog has no eligible jp-tyo server',
      actor: 'system',
    },
    {
      id: 'run-0005',
      scheduleId: 'sch-002',
      trigger: 'interval',
      startedAt: iso(now - 1 * 3_600_000),
      finishedAt: iso(now - 1 * 3_600_000 + 220),
      state: 'dry_run',
      result: 'dry_run',
      detail: 'dry run · reconnect acc-03',
      actor: 'system',
    },
    {
      id: 'run-0004',
      scheduleId: 'sch-003',
      trigger: 'cron',
      startedAt: iso(now - 12 * 3_600_000),
      finishedAt: iso(now - 12 * 3_600_000 + 610),
      state: 'failed',
      result: 'error',
      reason: 'no_eligible_server',
      errorClass: 'permanent',
      detail: 'catalog has no eligible jp-tyo server',
      actor: 'system',
    },
    {
      id: 'run-0003',
      scheduleId: 'sch-001',
      trigger: 'cron',
      startedAt: iso(now - 2 * 86_400_000),
      finishedAt: iso(now - 2 * 86_400_000 + 1_900),
      state: 'failed',
      result: 'error',
      reason: 'network',
      errorClass: 'transient',
      detail: 'upstream timeout after 30s',
      actor: 'system',
    },
    {
      id: 'run-0002',
      scheduleId: 'sch-001',
      trigger: 'cron',
      startedAt: iso(now - 3 * 86_400_000),
      finishedAt: iso(now - 3 * 86_400_000 + 120),
      state: 'skipped',
      result: 'skipped',
      reason: 'cooldown',
      detail: 'cooldown guard',
      actor: 'system',
    },
    {
      id: 'run-0001',
      scheduleId: SYSTEM_SCHEDULE_ID,
      trigger: 'cron',
      startedAt: iso(now - 4 * 3_600_000),
      finishedAt: iso(now - 4 * 3_600_000 + 40),
      state: 'succeeded',
      result: 'ok',
      detail: 'due 1 · overdue 1',
      actor: 'system',
    },
  ]
  return { schedules: [nightly, watchdog, weekend, oneShot], runs, seq: 4, runSeq: 8 }
}

function mockSchedules(): MockScheduleState {
  if (!mockScheduleState) mockScheduleState = buildMockScheduleState()
  return mockScheduleState
}

function scheduleFieldError(field: string, message: string): never {
  throw new ApiError(400, `${field}: ${message}`, 'INVALID_ARGUMENT')
}

function confirmationRequired(token: 'ARM' | 'RUN'): ScheduleActionError {
  return new ScheduleActionError(
    409,
    `confirm:"${token}" is required for a destructive schedule action`,
    'CONFLICT',
    'confirmation_required',
  )
}

function validateMockSchedule(schedule: Schedule, state: MockScheduleState): void {
  const name = schedule.name.trim()
  if (!name) scheduleFieldError('name', 'is required')
  if ([...name].length > 60) scheduleFieldError('name', 'must be at most 60 characters')
  const duplicate = state.schedules.find(
    (candidate) => candidate.id !== schedule.id && candidate.name.toLowerCase() === name.toLowerCase(),
  )
  if (duplicate) throw new ApiError(409, `a schedule named “${name}” already exists`, 'CONFLICT')
  if (schedule.action !== 'account.rotate' && schedule.action !== 'account.reconnect') {
    scheduleFieldError('action', `“${schedule.action}” is not supported in P0`)
  }
  if (schedule.target.kind !== 'account') {
    scheduleFieldError('target.kind', `must be account for ${schedule.action}`)
  }
  const accountId = schedule.target.id?.trim() ?? ''
  if (!accountId) scheduleFieldError('target.id', 'is required')
  if (!getDb().accounts.some((account) => account.id === accountId)) {
    scheduleFieldError('target.id', `account “${accountId}” not found`)
  }
  const params = (schedule.target.params ?? {}) as Record<string, unknown>
  const allowedParams =
    schedule.action === 'account.rotate'
      ? new Set([
          'strategy',
          'serverIds',
          'region',
          'excludeCurrent',
          'drainSeconds',
          'timeoutSeconds',
          'rollback',
        ])
      : new Set(['timeoutSeconds'])
  for (const key of Object.keys(params)) {
    if (!allowedParams.has(key)) scheduleFieldError('target.params', `unknown field “${key}”`)
  }
  const drainSeconds = params.drainSeconds
  if (drainSeconds !== undefined) {
    if (typeof drainSeconds !== 'number' || drainSeconds < 0 || drainSeconds > 60) {
      scheduleFieldError('target.params.drainSeconds', 'must be between 0 and 60')
    }
  }
  const timeoutSeconds = params.timeoutSeconds
  if (timeoutSeconds !== undefined) {
    if (
      typeof timeoutSeconds !== 'number' ||
      (timeoutSeconds !== 0 && (timeoutSeconds < 10 || timeoutSeconds > 120))
    ) {
      scheduleFieldError('target.params.timeoutSeconds', 'must be between 10 and 120')
    }
  }
  if (params.region !== undefined && typeof params.region !== 'string') {
    scheduleFieldError('target.params.region', 'must be a string')
  }
  if (params.serverIds !== undefined) {
    if (!Array.isArray(params.serverIds)) {
      scheduleFieldError('target.params.serverIds', 'must be an array of server ids')
    }
    if (params.serverIds.some((value) => typeof value !== 'string')) {
      scheduleFieldError('target.params.serverIds', 'must contain only string ids')
    }
  }
  if (params.strategy !== undefined && typeof params.strategy !== 'string') {
    scheduleFieldError('target.params.strategy', 'must be a string')
  }
  if (params.excludeCurrent !== undefined && typeof params.excludeCurrent !== 'boolean') {
    scheduleFieldError('target.params.excludeCurrent', 'must be a boolean')
  }
  if (params.rollback !== undefined && typeof params.rollback !== 'boolean') {
    scheduleFieldError('target.params.rollback', 'must be a boolean')
  }
  if (schedule.action === 'account.rotate') {
    const strategy =
      typeof params.strategy === 'string' && params.strategy ? params.strategy : 'same-region'
    if (!['pool', 'same-region', 'next-server', 'random'].includes(strategy)) {
      scheduleFieldError('target.params.strategy', 'must be pool, same-region, next-server or random')
    }
    if (strategy === 'pool') {
      const ids = Array.isArray(params.serverIds)
        ? params.serverIds.filter((value): value is string => typeof value === 'string')
        : []
      if (ids.length < 2) scheduleFieldError('target.params.serverIds', 'a pool needs at least 2 server ids')
      if (new Set(ids).size !== ids.length) scheduleFieldError('target.params.serverIds', 'must not contain duplicates')
      const catalog = buildAccountServers(getDb(), accountId)
      const rows = new Map(catalog.servers.map((row) => [row.id, row]))
      for (const id of ids) {
        const row = rows.get(id)
        if (!row) scheduleFieldError('target.params.serverIds', `server “${id}” is not in the retained catalog`)
        if (
          row.eligible === false ||
          row.status !== 'ok' ||
          row.fullLoad === true ||
          row.blockingMarks.length > 0 ||
          row.memberOnly === true
        ) {
          scheduleFieldError('target.params.serverIds', `server “${id}” is not eligible`)
        }
      }
    }
  }

  const trigger = schedule.trigger
  if (trigger.timezone) {
    try {
      new Intl.DateTimeFormat('en-US', { timeZone: trigger.timezone })
    } catch {
      scheduleFieldError('trigger.timezone', `“${trigger.timezone}” is not an IANA timezone`)
    }
  }
  if (trigger.kind === 'cron') {
    if (!trigger.expr?.trim()) scheduleFieldError('trigger.expr', 'is required')
    try {
      parseCronExpression(trigger.expr ?? '')
    } catch (cause) {
      scheduleFieldError('trigger.expr', cause instanceof Error ? cause.message : String(cause))
    }
    if ((trigger.everySec ?? 0) !== 0 || trigger.at) {
      scheduleFieldError('trigger', 'cron accepts only expr, timezone and jitterSec')
    }
    const jitter = trigger.jitterSec ?? 0
    if (jitter < 0 || jitter > 300) scheduleFieldError('trigger.jitterSec', 'must be between 0 and 300 for cron')
  } else if (trigger.kind === 'interval') {
    const every = trigger.everySec ?? 0
    if (every < 60 || every > 2_592_000) {
      scheduleFieldError('trigger.everySec', 'must be between 60 and 2592000')
    }
    const maxJitter = Math.min(300, Math.floor(every / 2))
    const jitter = trigger.jitterSec ?? 0
    if (jitter < 0 || jitter > maxJitter) {
      scheduleFieldError('trigger.jitterSec', `must be between 0 and ${maxJitter}`)
    }
    if (trigger.expr || trigger.at) {
      scheduleFieldError('trigger', 'interval accepts only everySec, timezone and jitterSec')
    }
  } else if (trigger.kind === 'once') {
    if (!trigger.at) scheduleFieldError('trigger.at', 'is required')
    if (trigger.expr || (trigger.everySec ?? 0) !== 0 || (trigger.jitterSec ?? 0) !== 0) {
      scheduleFieldError('trigger', 'once accepts only at and timezone')
    }
    const at = Date.parse(trigger.at ?? '')
    if (!Number.isFinite(at)) scheduleFieldError('trigger.at', 'must be an RFC 3339 timestamp')
    if (at <= Date.now()) scheduleFieldError('trigger.at', 'must be in the future')
    if (at > Date.now() + 366 * 86_400_000) scheduleFieldError('trigger.at', 'must be at most 366 days away')
  } else {
    scheduleFieldError('trigger.kind', 'must be cron, interval or once')
  }

  if (schedule.window) {
    const { days, start, end } = schedule.window
    if (!Array.isArray(days)) scheduleFieldError('window.days', 'must be an array')
    if (new Set(days).size !== days.length) scheduleFieldError('window.days', 'contains a duplicate day')
    for (const day of days) {
      if (!Number.isInteger(day) || day < 0 || day > 6) scheduleFieldError('window.days', 'must be 0-6')
    }
    if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(start)) scheduleFieldError('window.start', 'must be HH:MM')
    if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(end)) scheduleFieldError('window.end', 'must be HH:MM')
    if (start === end) scheduleFieldError('window', 'start and end must differ')
  }

  const guard = schedule.guard
  if (guard.cooldownSec < 0 || guard.cooldownSec > 86_400) {
    scheduleFieldError('guard.cooldownSec', 'must be between 0 and 86400')
  }
  if (guard.maxRunsPerDay < 1 || guard.maxRunsPerDay > 1_000) {
    scheduleFieldError('guard.maxRunsPerDay', 'must be between 1 and 1000')
  }
  if (schedule.misfirePolicy !== 'skip' && schedule.misfirePolicy !== 'run_once') {
    scheduleFieldError('misfirePolicy', 'must be skip or run_once')
  }
  if ((schedule.misfireGraceSec ?? 0) < 0 || (schedule.misfireGraceSec ?? 0) > 3_600) {
    scheduleFieldError('misfireGraceSec', 'must be between 0 and 3600')
  }
  if (schedule.trigger.kind === 'once' && schedule.misfirePolicy === 'run_once') {
    scheduleFieldError('misfirePolicy', 'run_once is not allowed for a once trigger')
  }
  if (schedule.concurrencyPolicy !== 'forbid' && schedule.concurrencyPolicy !== 'queue') {
    scheduleFieldError('concurrencyPolicy', 'must be forbid or queue')
  }
  if (schedule.enabled && !nextRunAtFor(schedule.trigger, schedule.window)) {
    scheduleFieldError('trigger', 'has no reachable next run')
  }
}

function mockCreateSchedule(input: ScheduleCreateInput): Schedule {
  const state = mockSchedules()
  if (state.schedules.length >= 50) throw new ApiError(409, 'at most 50 schedules are allowed', 'CONFLICT')
  const now = Date.now()
  const createdAt = new Date(now).toISOString()
  const schedule: Schedule = {
    id: `sch-${String(state.seq + 1).padStart(3, '0')}`,
    revision: 1,
    name: input.name.trim(),
    enabled: input.enabled === true,
    owner: 'admin',
    action: input.action,
    target: input.target,
    trigger: input.trigger,
    window: input.window,
    guard: { ...mockDefaultGuard(), ...input.guard },
    misfirePolicy: input.misfirePolicy ?? 'skip',
    misfireGraceSec: input.misfireGraceSec ?? 300,
    concurrencyPolicy: input.concurrencyPolicy ?? 'forbid',
    createdAt,
    updatedAt: createdAt,
    runCountToday: 0,
    consecutiveFailures: 0,
    degraded: false,
  }
  if (schedule.enabled) schedule.nextRunAt = nextRunAtFor(schedule.trigger, schedule.window)
  validateMockSchedule(schedule, state)
  if (isDestructiveAction(schedule.action) && !schedule.guard.dryRun && input.confirm !== 'ARM') {
    throw confirmationRequired('ARM')
  }
  state.seq += 1
  state.schedules.push(schedule)
  return clone(schedule)
}

function mockPatchSchedule(id: string, expectedRevision: number, input: SchedulePatchInput): Schedule {
  const state = mockSchedules()
  const index = state.schedules.findIndex((schedule) => schedule.id === id)
  if (index < 0) throw new ApiError(404, `schedule “${id}” not found`, 'NOT_FOUND')
  const current = state.schedules[index]
  if (current.revision !== expectedRevision) {
    throw new ScheduleActionError(
      412,
      `schedule “${id}” revision is ${current.revision}`,
      'PRECONDITION_FAILED',
      'stale_revision',
      { currentRevision: String(current.revision) },
    )
  }
  const next = clone(current)
  if (input.name !== undefined) next.name = input.name.trim()
  if (input.enabled !== undefined) next.enabled = input.enabled
  if (input.action !== undefined) next.action = input.action
  if (input.target !== undefined) next.target = input.target
  if (input.trigger !== undefined) next.trigger = input.trigger
  if (input.window !== undefined) next.window = input.window
  if (input.guard !== undefined) next.guard = { ...next.guard, ...input.guard }
  if (input.misfirePolicy !== undefined) next.misfirePolicy = input.misfirePolicy
  if (input.misfireGraceSec !== undefined) next.misfireGraceSec = input.misfireGraceSec
  if (input.concurrencyPolicy !== undefined) next.concurrencyPolicy = input.concurrencyPolicy
  if (input.enabled === true) {
    next.pausedReason = ''
    next.consecutiveFailures = 0
    next.degraded = false
  }
  validateMockSchedule(next, state)
  const actionChanged = input.action !== undefined
  const dryRunFlipped = input.guard?.dryRun === false && current.guard.dryRun
  if (
    (actionChanged || dryRunFlipped) &&
    isDestructiveAction(next.action) &&
    !next.guard.dryRun &&
    input.confirm !== 'ARM'
  ) {
    throw confirmationRequired('ARM')
  }
  next.revision += 1
  next.updatedAt = new Date().toISOString()
  if (input.enabled === false) next.nextRunAt = undefined
  else if (next.enabled) next.nextRunAt = nextRunAtFor(next.trigger, next.window)
  state.schedules[index] = next
  return clone(next)
}

function mockDeleteSchedule(id: string): void {
  const state = mockSchedules()
  const index = state.schedules.findIndex((schedule) => schedule.id === id)
  if (index < 0) throw new ApiError(404, `schedule “${id}” not found`, 'NOT_FOUND')
  state.schedules.splice(index, 1)
}

function mockRunSchedule(id: string, input: ScheduleRunInput): ScheduleRun {
  const state = mockSchedules()
  const schedule = state.schedules.find((candidate) => candidate.id === id)
  if (!schedule) throw new ApiError(404, `schedule “${id}” not found`, 'NOT_FOUND')
  const dryRun = input.dryRun ?? schedule.guard.dryRun
  if (!dryRun && isDestructiveAction(schedule.action) && input.confirm !== 'RUN') {
    throw confirmationRequired('RUN')
  }
  const now = Date.now()
  const startedAt = new Date(now).toISOString()
  const run: ScheduleRun = {
    id: `run-${String(state.runSeq + 1).padStart(4, '0')}`,
    scheduleId: id,
    trigger: 'manual',
    startedAt,
    finishedAt: new Date(now + 140).toISOString(),
    state: dryRun ? 'dry_run' : 'succeeded',
    result: dryRun ? 'dry_run' : 'ok',
    detail: dryRun ? 'dry run · manual run' : 'manual run',
    actor: 'admin',
    operationId: `op-mock-${state.runSeq + 1}`,
  }
  state.runSeq += 1
  state.runs.unshift(run)
  schedule.lastRunAt = startedAt
  schedule.lastResult = run.result
  schedule.runCountToday += 1
  if (schedule.action === 'account.rotate') schedule.cursor = (schedule.cursor ?? 0) + 1
  schedule.nextRunAt = nextRunAtFor(schedule.trigger, schedule.window)
  return clone(run)
}

function mockScheduleRuns(id: string, limit = 50): ScheduleRun[] {
  const state = mockSchedules()
  if (id !== SYSTEM_SCHEDULE_ID && !state.schedules.some((schedule) => schedule.id === id)) {
    throw new ApiError(404, `schedule “${id}” not found`, 'NOT_FOUND')
  }
  const cap = Math.max(0, Math.min(200, Math.trunc(limit)))
  return clone(state.runs.filter((run) => run.scheduleId === id).slice(0, cap))
}

function mockSchedulePreview(input: SchedulePreviewInput, count = 5): SchedulePreview {
  try {
    const { runs, warnings } = computeSchedulePreview(input.trigger, input.window, count)
    return { serverTime: new Date().toISOString(), runs, warnings }
  } catch (cause) {
    throw new ApiError(400, cause instanceof Error ? cause.message : String(cause), 'INVALID_ARGUMENT')
  }
}

/* --------------------------------------------------------------------------
 * Platform surfaces — renewal / usage / health / webhooks
 *
 * The daemon owns the four contracts (daemon/internal/api/platform.go). The
 * browser mock mirrors the wire shapes with deterministic data so the panel
 * and the suite can run without a daemon: a static plan table, a seeded usage
 * series aggregated with the same window rules, a health mix of ok/degraded/
 * down nodes, and one webhook receiver with a bounded delivery ring.
 * ------------------------------------------------------------------------ */

const MOCK_RENEWAL_FETCHED_AT = '2026-10-24T00:00:00Z'
const MOCK_RENEWAL_URL = 'https://www.91ajs.com/purchase'
const MOCK_RENEWAL_SOON_DAYS = 5
const MOCK_DAY_MS = 86_400_000
const MOCK_USAGE_MAX_DAYS = 366
const MOCK_USAGE_DEFAULT_DAYS = 30
const MOCK_USAGE_TOP_TARGETS = 10
const MOCK_WEBHOOK_MAX = 32

/** The daemon's built-in fallback table (renewal.StaticPlans), verbatim. */
const MOCK_RENEWAL_PLANS: readonly RenewalPlan[] = [
  { id: 'M1', label: '1 month', months: 1, priceCny: 20 },
  { id: 'M3', label: '3 months', months: 3, priceCny: 53 },
  { id: 'M12', label: '12 months', months: 12, priceCny: 198 },
  { id: 'H6', label: '6-hour trial', months: 0, priceCny: 1, trial: true, notes: 'TrialPackage' },
]

function platformError(status: number, code: string, message: string): ApiError {
  return new ApiError(status, message, code)
}

function mockRFC3339(ms: number): string {
  return new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')
}

/** Whole days to expiry, rounded away from zero like renewal.daysLeft. */
function mockDaysLeft(expiresMs: number, now: number): number {
  const remaining = expiresMs - now
  if (remaining === 0) return 0
  let days = Math.trunc(remaining / MOCK_DAY_MS)
  const rest = remaining % MOCK_DAY_MS
  if (remaining > 0 && rest > 0) days += 1
  if (remaining < 0 && rest < 0) days -= 1
  return days
}

function mockRenewalAccount(id: string): RenewalAccountView {
  const account = getDb().accounts.find((candidate) => candidate.id === id)
  if (!account) throw platformError(404, 'NOT_FOUND', `account "${id}" not found`)
  const expiresMs = Date.parse(account.expiresAt)
  if (!Number.isFinite(expiresMs)) {
    throw platformError(400, 'INVALID_ARGUMENT', 'account expiry is invalid')
  }
  const daysLeft = mockDaysLeft(expiresMs, Date.now())
  const urgency =
    daysLeft < 0 ? 'overdue' : daysLeft === 0 ? 'due' : daysLeft <= MOCK_RENEWAL_SOON_DAYS ? 'soon' : 'none'
  return {
    accountId: account.id,
    expiresAt: account.expiresAt,
    daysLeft,
    urgency,
    renewalUrl: MOCK_RENEWAL_URL,
  }
}

/** FNV-1a over the seed text; the usage series must not depend on Math.random. */
function mockSeed(text: string): number {
  let hash = 2166136261
  for (let index = 0; index < text.length; index += 1) {
    hash ^= text.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return hash >>> 0
}

/** mulberry32 — a tiny deterministic PRNG for the seeded usage series. */
function mockRandom(seed: number): () => number {
  let state = seed >>> 0
  return () => {
    state = (state + 0x6d2b79f5) >>> 0
    let value = state
    value = Math.imul(value ^ (value >>> 15), value | 1)
    value ^= value + Math.imul(value ^ (value >>> 7), value | 61)
    return ((value ^ (value >>> 14)) >>> 0) / 4294967296
  }
}

const MOCK_USAGE_TARGETS = [
  'api.example.com',
  'cdn.example.net',
  'auth.example.org',
  'telemetry.example.dev',
  'registry.example.io',
]

/** Accepts RFC 3339 or a bare UTC date, mirroring parseUsageTime. */
function parseMockUsageTime(raw: string | undefined, field: string): number | null {
  const value = raw?.trim()
  if (!value) return null
  const dateOnly = /^\d{4}-\d{2}-\d{2}$/.test(value)
  const timestamp = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value)
  const parsed = dateOnly || timestamp ? Date.parse(value) : Number.NaN
  if (!Number.isFinite(parsed)) {
    throw platformError(
      400,
      'INVALID_ARGUMENT',
      `${field} must be an RFC 3339 timestamp or a UTC date (YYYY-MM-DD)`,
    )
  }
  return parsed
}

interface MockUsageWindow {
  from: number
  to: number
  days: string[]
}

/** Mirrors usage.Aggregator.window: inclusive UTC days, 30-day default, 366 cap. */
function mockUsageWindow(query: UsageQuery): MockUsageWindow {
  const now = Date.now()
  let from = parseMockUsageTime(query.from, 'from')
  let to = parseMockUsageTime(query.to, 'to')
  if (from === null && to === null) to = now
  if (to === null) to = now
  if (from === null) {
    const start = new Date(to)
    start.setUTCDate(start.getUTCDate() - MOCK_USAGE_DEFAULT_DAYS)
    from = start.getTime()
  }
  if (to < from) {
    throw platformError(
      400,
      'INVALID_ARGUMENT',
      `usage: invalid window: to ${mockRFC3339(to)} is before from ${mockRFC3339(from)}`,
    )
  }
  const fromDay = Math.floor(from / MOCK_DAY_MS) * MOCK_DAY_MS
  const toDay = Math.floor(to / MOCK_DAY_MS) * MOCK_DAY_MS
  const count = Math.round((toDay - fromDay) / MOCK_DAY_MS) + 1
  if (count > MOCK_USAGE_MAX_DAYS) {
    throw platformError(
      400,
      'INVALID_ARGUMENT',
      `usage: window exceeds the maximum span: ${count} days, maximum ${MOCK_USAGE_MAX_DAYS}`,
    )
  }
  const days = Array.from({ length: count }, (_, index) =>
    new Date(fromDay + index * MOCK_DAY_MS).toISOString().slice(0, 10),
  )
  return { from, to, days }
}

/**
 * One deterministic request row per (day, account, user, slot). The seed is
 * the day and ids, so the same window always aggregates to the same summary.
 */
function mockUsageRows(days: string[]): RequestLog[] {
  const db = getDb()
  const rows: RequestLog[] = []
  for (const day of days) {
    for (const account of db.accounts) {
      for (const user of db.users) {
        const rand = mockRandom(mockSeed(`${day}|${account.id}|${user.id}`))
        const node = db.nodes.find((candidate) => candidate.accountId === account.id)
        const count = 1 + Math.floor(rand() * 3)
        for (let slot = 0; slot < count; slot += 1) {
          const failed = rand() < 0.06
          const throttled = !failed && rand() < 0.03
          const status = failed ? 502 : throttled ? 407 : 200
          const latencyMs = failed
            ? 900 + Math.round(rand() * 1400)
            : throttled
              ? 9 + Math.round(rand() * 20)
              : 38 + Math.round(rand() * 260)
          rows.push({
            id: `usage-${day}-${account.id}-${user.id}-${slot}`,
            ts: `${day}T00:${String(5 + slot).padStart(2, '0')}:00Z`,
            userId: user.id,
            userName: user.name,
            accountLabel: account.label,
            nodeLabel: node?.label ?? '',
            region: node?.region ?? '',
            method: 'CONNECT',
            target: MOCK_USAGE_TARGETS[Math.floor(rand() * MOCK_USAGE_TARGETS.length)] ?? 'unknown',
            status,
            bytes: 1_200 + Math.round(rand() * 8_500_000),
            latencyMs,
            source: 'proxy',
          })
        }
      }
    }
  }
  return rows
}

interface MockUsageTotals {
  requests: number
  bytes: number
  errors: number
  latency: number
}

function mockUsageTotals(): MockUsageTotals {
  return { requests: 0, bytes: 0, errors: 0, latency: 0 }
}

function mockUsageAdd(total: MockUsageTotals, row: RequestLog): void {
  total.requests += 1
  total.bytes += row.bytes
  if (row.status >= 400) total.errors += 1
  total.latency += row.latencyMs
}

/** Round half up with integer arithmetic, matching usage.totals.avgLatency. */
function mockUsageAvg(total: MockUsageTotals): number {
  if (total.requests === 0) return 0
  return Math.floor((total.latency + Math.floor(total.requests / 2)) / total.requests)
}

function mockUsageSummary(query: UsageQuery): UsageSummary {
  const { from, to, days } = mockUsageWindow(query)
  const db = getDb()
  const accountQuery = query.account?.trim() ?? ''
  const accountFilter =
    db.accounts.find((account) => account.id === accountQuery)?.label ?? accountQuery
  const userFilter = query.userId?.trim() ?? ''
  const dayIndex = new Map(days.map((day, index) => [day, index]))
  const quotas = new Map<string, number>()
  for (const user of db.users) {
    if (user.quotaBytesMonth !== null) quotas.set(user.id, user.quotaBytesMonth)
  }

  const total = mockUsageTotals()
  const dayTotals = days.map(() => mockUsageTotals())
  const accounts = new Map<string, { total: MockUsageTotals; days: Map<string, MockUsageTotals> }>()
  const users = new Map<string, { name: string; total: MockUsageTotals }>()
  const targets = new Map<string, UsageTarget>()

  for (const row of mockUsageRows(days)) {
    if (accountFilter && row.accountLabel !== accountFilter) continue
    if (userFilter && row.userId !== userFilter) continue
    const ts = Date.parse(row.ts)
    if (!Number.isFinite(ts) || ts < from || ts > to) continue
    const day = row.ts.slice(0, 10)
    const index = dayIndex.get(day)
    if (index === undefined) continue

    mockUsageAdd(total, row)
    mockUsageAdd(dayTotals[index], row)

    let account = accounts.get(row.accountLabel)
    if (!account) {
      account = { total: mockUsageTotals(), days: new Map() }
      accounts.set(row.accountLabel, account)
    }
    mockUsageAdd(account.total, row)
    let accountDay = account.days.get(day)
    if (!accountDay) {
      accountDay = mockUsageTotals()
      account.days.set(day, accountDay)
    }
    mockUsageAdd(accountDay, row)

    let user = users.get(row.userId)
    if (!user) {
      user = { name: row.userName, total: mockUsageTotals() }
      users.set(row.userId, user)
    }
    if (!user.name && row.userName) user.name = row.userName
    mockUsageAdd(user.total, row)

    const target = targets.get(row.target)
    if (target) {
      target.requests += 1
      target.bytes += row.bytes
    } else {
      targets.set(row.target, { target: row.target, requests: 1, bytes: row.bytes })
    }
  }

  const summary: UsageSummary = {
    from: mockRFC3339(from),
    to: mockRFC3339(to),
    requests: total.requests,
    bytes: total.bytes,
    errors: total.errors,
    accounts: [],
    users: [],
    days: days.map((day, index) => ({
      day,
      requests: dayTotals[index].requests,
      bytes: dayTotals[index].bytes,
      errors: dayTotals[index].errors,
      avgLatencyMs: mockUsageAvg(dayTotals[index]),
    })),
    topTargets: [],
    truncated: false,
    quotaEnforced: false,
  }

  for (const [label, account] of accounts) {
    summary.accounts.push({
      account: label,
      requests: account.total.requests,
      bytes: account.total.bytes,
      errors: account.total.errors,
      avgLatencyMs: mockUsageAvg(account.total),
      buckets: days.map((day) => {
        const bucket = account.days.get(day)
        return {
          day,
          requests: bucket?.requests ?? 0,
          bytes: bucket?.bytes ?? 0,
          errors: bucket?.errors ?? 0,
          avgLatencyMs: bucket ? mockUsageAvg(bucket) : 0,
        }
      }),
    })
  }
  summary.accounts.sort(
    (left, right) => right.bytes - left.bytes || left.account.localeCompare(right.account),
  )

  for (const [userId, user] of users) {
    summary.users.push({
      userId,
      userName: user.name,
      requests: user.total.requests,
      bytes: user.total.bytes,
      quotaBytesMonth: quotas.get(userId) ?? null,
      usedBytes: user.total.bytes,
    })
  }
  summary.users.sort(
    (left, right) =>
      right.bytes - left.bytes ||
      left.userName.localeCompare(right.userName) ||
      left.userId.localeCompare(right.userId),
  )

  summary.topTargets = [...targets.values()]
    .sort(
      (left, right) =>
        right.requests - left.requests ||
        right.bytes - left.bytes ||
        left.target.localeCompare(right.target),
    )
    .slice(0, MOCK_USAGE_TOP_TARGETS)

  return summary
}

/** Deterministic loopback health: one down node and a degraded every fifth. */
function mockNodeHealth(node: ProxyNode, index: number): NodeHealth {
  const count = 120
  const down = node.state === 'DOWN'
  const degraded = !down && index % 5 === 2
  const successRate = down ? 0 : degraded ? 0.87 : Math.min(0.999, node.successRate / 100)
  const ok = Math.round(count * successRate)
  const p50 = down ? 0 : node.latencyMs > 0 ? node.latencyMs : 40
  return {
    nodeId: node.id,
    accountId: node.accountId,
    label: node.label,
    state: down ? 'down' : degraded ? 'degraded' : 'ok',
    count,
    ok,
    successRate,
    p50Ms: p50,
    p95Ms: down ? 0 : degraded ? 940 : Math.round(p50 * 1.6),
    minMs: down ? 0 : Math.round(p50 * 0.8),
    maxMs: down ? 0 : Math.round(p50 * 2.4),
    consecutiveFailures: down ? 5 : 0,
    last: {
      at: node.lastProbeAt,
      latencyMs: down ? 3_000 : p50,
      ok: !down,
      ...(down ? { errorClass: 'timeout' } : {}),
    },
  }
}

interface MockWebhookState {
  endpoints: WebhookEndpoint[]
  secrets: Map<string, string>
  deliveries: Map<string, Delivery[]>
  seq: number
}

let mockWebhookState: MockWebhookState | null = null

/** 32 deterministic bytes as hex — the same one-time secret shape as the daemon. */
function mockWebhookSecret(seq: number): string {
  let secret = ''
  for (let block = 0; block < 8; block += 1) {
    const rand = mockRandom(mockSeed(`ajs-webhook-secret:${seq}:${block}`))
    secret += Math.floor(rand() * 0x1_0000_0000)
      .toString(16)
      .padStart(8, '0')
  }
  return secret
}

function mockWebhookStateValue(): MockWebhookState {
  if (mockWebhookState) return mockWebhookState
  const now = Date.now()
  const endpoint: WebhookEndpoint = {
    id: 'wh-001',
    url: 'https://hooks.example.com/ajs',
    events: ['node.connected', 'node.disconnected'],
    enabled: true,
    description: 'Ops relay',
    createdAt: new Date(now - 12 * MOCK_DAY_MS).toISOString(),
    secretAvailable: true,
  }
  const deliveries: Delivery[] = [
    {
      id: 'dlv-001',
      endpointId: 'wh-001',
      event: 'node.connected',
      attempt: 1,
      status: 'success',
      httpStatus: 202,
      at: new Date(now - 3 * 3_600_000).toISOString(),
      durationMs: 118,
    },
    {
      id: 'dlv-002',
      endpointId: 'wh-001',
      event: 'node.disconnected',
      attempt: 1,
      status: 'failed',
      httpStatus: 503,
      error: 'HTTP 503',
      at: new Date(now - 2 * 3_600_000).toISOString(),
      durationMs: 240,
    },
    {
      id: 'dlv-003',
      endpointId: 'wh-001',
      event: 'node.disconnected',
      attempt: 2,
      status: 'success',
      httpStatus: 200,
      at: new Date(now - 2 * 3_600_000 + 30_000).toISOString(),
      durationMs: 96,
    },
  ]
  mockWebhookState = {
    endpoints: [endpoint],
    secrets: new Map([['wh-001', mockWebhookSecret(1)]]),
    deliveries: new Map([['wh-001', deliveries]]),
    seq: 1,
  }
  return mockWebhookState
}

function mockWebhookView(endpoint: WebhookEndpoint): WebhookEndpoint {
  return { ...clone(endpoint), secretAvailable: mockWebhookStateValue().secrets.has(endpoint.id) }
}

function mockWebhookURL(raw: string): string {
  const value = raw.trim()
  if (!value) throw platformError(400, 'INVALID_ARGUMENT', 'webhook url is required')
  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    throw platformError(400, 'INVALID_ARGUMENT', 'webhook: invalid endpoint: url is not absolute')
  }
  if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') {
    throw platformError(400, 'INVALID_ARGUMENT', 'webhook: invalid endpoint: scheme must be https')
  }
  if (!parsed.hostname) {
    throw platformError(400, 'INVALID_ARGUMENT', 'webhook: invalid endpoint: host is required')
  }
  if (parsed.username || parsed.password) {
    throw platformError(
      400,
      'INVALID_ARGUMENT',
      'webhook: invalid endpoint: credentials must not be embedded in the URL',
    )
  }
  if (parsed.protocol === 'http:' && !mockLoopbackHost(parsed.hostname)) {
    throw platformError(
      400,
      'INVALID_ARGUMENT',
      'webhook: invalid endpoint: http is only allowed for loopback receivers',
    )
  }
  return value
}

/** Mirrors webhook.isLoopbackHost: localhost, 127.0.0.0/8 or ::1. */
function mockLoopbackHost(host: string): boolean {
  const value = host.toLowerCase()
  return (
    value === 'localhost' ||
    value === '::1' ||
    value === '[::1]' ||
    /^127(?:\.\d{1,3}){3}$/.test(value)
  )
}

function mockWebhookEvents(raw: WebhookEvent[] | undefined): WebhookEvent[] {
  const seen = new Set<string>()
  const events: WebhookEvent[] = []
  for (const candidate of raw ?? []) {
    const value = String(candidate).trim()
    if (!value || seen.has(value)) continue
    if (!(WEBHOOK_EVENTS as readonly string[]).includes(value)) {
      throw platformError(
        400,
        'INVALID_ARGUMENT',
        `webhook: invalid endpoint: unknown event type "${value}"`,
      )
    }
    seen.add(value)
    events.push(value as WebhookEvent)
  }
  return events
}

function mockCreateWebhook(input: WebhookCreate): WebhookCreated {
  const state = mockWebhookStateValue()
  const url = mockWebhookURL(input.url)
  const events = mockWebhookEvents(input.events)
  if (state.endpoints.length >= MOCK_WEBHOOK_MAX) {
    throw platformError(
      400,
      'INVALID_ARGUMENT',
      `at most ${MOCK_WEBHOOK_MAX} webhook endpoints are allowed`,
    )
  }
  state.seq += 1
  const id = `wh-${String(state.seq).padStart(3, '0')}`
  const description = input.description?.trim() ?? ''
  const endpoint: WebhookEndpoint = {
    id,
    url,
    enabled: input.enabled ?? true,
    createdAt: new Date().toISOString(),
    secretAvailable: true,
    ...(events.length > 0 ? { events } : {}),
    ...(description ? { description } : {}),
  }
  const secret = mockWebhookSecret(state.seq)
  state.endpoints.push(endpoint)
  state.secrets.set(id, secret)
  state.deliveries.set(id, [])
  return { ...clone(endpoint), secret }
}

function mockPatchWebhook(id: string, patch: WebhookPatch): WebhookEndpoint {
  const state = mockWebhookStateValue()
  const endpoint = state.endpoints.find((candidate) => candidate.id === id)
  if (!endpoint) throw platformError(404, 'NOT_FOUND', `webhook "${id}" not found`)
  if (typeof patch?.enabled !== 'boolean') {
    throw platformError(400, 'INVALID_ARGUMENT', 'enabled is required')
  }
  if (patch.enabled && !state.secrets.has(id)) {
    throw platformError(
      409,
      'CONFLICT',
      'webhook signing secret is unavailable; recreate the endpoint',
    )
  }
  endpoint.enabled = patch.enabled
  return mockWebhookView(endpoint)
}

function mockDeleteWebhook(id: string): void {
  const state = mockWebhookStateValue()
  const index = state.endpoints.findIndex((candidate) => candidate.id === id)
  if (index < 0) throw platformError(404, 'NOT_FOUND', `webhook "${id}" not found`)
  state.endpoints.splice(index, 1)
  state.secrets.delete(id)
  state.deliveries.delete(id)
}

function mockWebhookDeliveries(id: string, query: WebhookDeliveriesQuery): Delivery[] {
  const state = mockWebhookStateValue()
  if (!state.endpoints.some((candidate) => candidate.id === id)) {
    throw platformError(404, 'NOT_FOUND', `webhook "${id}" not found`)
  }
  // Mirror the daemon: newest first, then the page limit.
  const rows = [...(state.deliveries.get(id) ?? [])].sort(
    (left, right) => Date.parse(right.at) - Date.parse(left.at),
  )
  return clone(query.limit === undefined ? rows : rows.slice(0, query.limit))
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
        filename: attachmentFilename(
          response.headers.get('Content-Disposition'),
          `ajs-accounts-${exportTimestamp()}.${input.format === 'encrypted' ? 'ajsx' : input.format}`,
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

  /**
   * POST /v1/leases — allocate one free node. The idempotency key is sent as
   * the header the daemon documents; a replay returns the original lease with
   * `replayed: true` instead of taking a second node.
   */
  async createLease(input: CreateLeaseInput): Promise<CreateLeaseResult> {
    if (!USE_MOCK) {
      const headers: Record<string, string> = {}
      if (input.idempotencyKey) headers['Idempotency-Key'] = input.idempotencyKey
      return http<CreateLeaseResult>('/v1/leases', {
        method: 'POST',
        headers,
        body: JSON.stringify({
          userId: input.userId,
          platform: input.platform,
          ...(input.accountId ? { accountId: input.accountId } : {}),
          ...(input.ttlSeconds ? { ttlSeconds: input.ttlSeconds } : {}),
        }),
      })
    }
    await wait()
    const db = getDb()
    const key = input.idempotencyKey?.trim() ?? ''
    if (key) {
      const existing = db.leases.find((lease) => lease.idempotencyKey === key)
      if (existing) {
        if (existing.userId !== input.userId || existing.platform !== input.platform) {
          throw new ApiError(409, 'idempotency key already used for another request', 'CONFLICT')
        }
        return { lease: clone(existing), replayed: true }
      }
    }

    const user = db.users.find((item) => item.id === input.userId)
    if (!user) throw new ApiError(404, `user ${input.userId} not found`, 'NOT_FOUND')
    if (!user.enabled) throw new ApiError(409, `user ${user.name} is suspended`, 'CONFLICT')
    if (user.quotaBytesMonth !== null && user.usedBytes >= user.quotaBytesMonth) {
      throw new ApiError(429, `user ${user.name} has exhausted its monthly quota`, 'QUOTA_EXCEEDED')
    }
    if (db.leases.some((lease) => lease.userId === user.id && lease.platform === input.platform)) {
      throw new ApiError(
        409,
        `user ${user.name} already holds an active ${input.platform} lease`,
        'CONFLICT',
      )
    }

    const pickNode = (accountId: string): ProxyNode | undefined => {
      const leased = new Set(db.leases.map((lease) => lease.nodeId))
      return db.nodes
        .filter(
          (node) =>
            node.accountId === accountId &&
            !leased.has(node.id) &&
            (node.state === 'LIVE' || node.state === 'READY'),
        )
        .sort((a, b) => (a.state === 'LIVE' ? 0 : 1) - (b.state === 'LIVE' ? 0 : 1))[0]
    }

    let account: Account | undefined
    let node: ProxyNode | undefined
    if (input.accountId) {
      account = db.accounts.find((item) => item.id === input.accountId)
      if (!account) throw new ApiError(404, `account ${input.accountId} not found`, 'NOT_FOUND')
      if (account.platform !== input.platform) {
        throw new ApiError(
          400,
          `account ${account.label} is ${account.platform}, not ${input.platform}`,
          'INVALID_ARGUMENT',
        )
      }
      if (account.state !== 'active') {
        throw new ApiError(409, `account ${account.label} is ${account.state}`, 'CONFLICT')
      }
      if (db.leases.filter((lease) => lease.accountId === account!.id).length >= account.concurrency) {
        throw new ApiError(
          429,
          `account ${account.label} reached its concurrency of ${account.concurrency} active leases`,
          'QUOTA_EXCEEDED',
        )
      }
      node = pickNode(account.id)
      if (!node) throw new ApiError(429, `account ${account.label} has no free node`, 'QUOTA_EXCEEDED')
    } else {
      for (const candidate of db.accounts) {
        if (candidate.platform !== input.platform || candidate.state !== 'active') continue
        if (db.leases.filter((lease) => lease.accountId === candidate.id).length >= candidate.concurrency) {
          continue
        }
        const free = pickNode(candidate.id)
        if (free) {
          account = candidate
          node = free
          break
        }
      }
      if (!account || !node) {
        throw new ApiError(
          429,
          `no active ${input.platform} account has a free node`,
          'QUOTA_EXCEEDED',
        )
      }
    }

    const ttl =
      input.ttlSeconds && input.ttlSeconds > 0
        ? input.ttlSeconds
        : (leaseTtlSeconds(user.stickyTtl) ?? 8 * 3600)
    db.leaseSeq += 1
    const now = new Date()
    const lease: Lease = {
      id: `lease-${String(db.leaseSeq).padStart(2, '0')}`,
      userId: user.id,
      userName: user.name,
      accountId: account.id,
      accountLabel: account.label,
      platform: input.platform,
      nodeId: node.id,
      nodeLabel: node.label,
      region: node.region,
      egressIp: node.egressIp,
      protocol: node.protocol,
      gatePort: node.gatePort,
      ttlSeconds: ttl,
      expiresAt: new Date(now.getTime() + ttl * 1000).toISOString(),
      requests: 0,
      bytes: 0,
      sticky: true,
      status: 'active',
      idempotencyKey: key,
      createdAt: now.toISOString(),
    }
    db.leases.push(lease)
    node.inUse += 1
    return { lease: clone(lease) }
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

  /** DELETE /v1/leases/{id} — revoke after an explicit confirmation. */
  async revokeLease(id: string): Promise<void> {
    if (!USE_MOCK) {
      await http<void>(`/v1/leases/${id}`, {
        method: 'DELETE',
        body: JSON.stringify({ confirm: true }),
      })
      return
    }
    await wait()
    const db = getDb()
    const lease = db.leases.find((item) => item.id === id)
    if (!lease) throw new ApiError(404, `lease ${id} not found`, 'NOT_FOUND')
    const node = db.nodes.find((item) => item.id === lease.nodeId)
    if (node) node.inUse = Math.max(0, node.inUse - 1)
    db.leases = db.leases.filter((item) => item.id !== id)
  },

  /** Historical name kept for callers that predate the revoke wording. */
  async releaseLease(id: string): Promise<void> {
    await api.revokeLease(id)
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

  /**
   * GET /v1/events — replay the retained ring and long-poll for the next
   * event. `wait` holds the request server-side; pass the previous
   * `nextCursor` as `since` so a reconnect never skips an event.
   */
  async listEvents(
    query: EventsQuery = {},
    options: { signal?: AbortSignal } = {},
  ): Promise<EventsPage> {
    if (!USE_MOCK) {
      const params = new URLSearchParams()
      if (query.since) params.set('since', query.since)
      if (query.limit) params.set('limit', String(query.limit))
      if (query.wait) params.set('wait', query.wait)
      const search = params.toString()
      return http<EventsPage>(`/v1/events${search ? `?${search}` : ''}`, {
        signal: options.signal,
      })
    }
    await wait()
    if (options.signal?.aborted) throw new DOMException('Aborted', 'AbortError')
    return clone(mockEventsPage(query))
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

  /* ------------------------------------------------------------------------
   * Schedules — live daemon contract (daemon/internal/api/schedules.go)
   * ---------------------------------------------------------------------- */

  /**
   * GET /v1/schedules. The deployed daemon returns a bare array; a daemon that
   * reports the engine off may wrap it as `{enabled:false, reason, schedules}`.
   */
  async listSchedules(): Promise<SchedulesPayload> {
    if (!USE_MOCK) return http<SchedulesPayload>('/v1/schedules')
    await wait()
    return clone(mockSchedules().schedules)
  },

  async getSchedule(id: string): Promise<Schedule> {
    if (!USE_MOCK) return http<Schedule>(`/v1/schedules/${encodeURIComponent(id)}`)
    await wait()
    const schedule = mockSchedules().schedules.find((candidate) => candidate.id === id)
    if (!schedule) throw new ApiError(404, `schedule “${id}” not found`, 'NOT_FOUND')
    return clone(schedule)
  },

  /** POST /v1/schedules. The daemon returns 201 + ETag. */
  async createSchedule(input: ScheduleCreateInput): Promise<Schedule> {
    if (!USE_MOCK) {
      return http<Schedule>(
        '/v1/schedules',
        { method: 'POST', body: JSON.stringify(input) },
        scheduleActionError,
      )
    }
    await wait()
    return mockCreateSchedule(input)
  },

  /**
   * PATCH /v1/schedules/{id}. The daemon requires `If-Match: "id:revision"`;
   * a mismatch is 412 PRECONDITION_FAILED / stale_revision.
   */
  async patchSchedule(
    id: string,
    revision: number,
    input: SchedulePatchInput,
  ): Promise<Schedule> {
    if (!USE_MOCK) {
      return http<Schedule>(
        `/v1/schedules/${encodeURIComponent(id)}`,
        {
          method: 'PATCH',
          headers: { 'If-Match': `"${id}:${revision}"` },
          body: JSON.stringify(input),
        },
        scheduleActionError,
      )
    }
    await wait()
    return mockPatchSchedule(id, revision, input)
  },

  async deleteSchedule(id: string): Promise<void> {
    if (!USE_MOCK) {
      await http<void>(
        `/v1/schedules/${encodeURIComponent(id)}`,
        { method: 'DELETE' },
        scheduleActionError,
      )
      return
    }
    await wait()
    mockDeleteSchedule(id)
  },

  /** POST /v1/schedules/{id}/actions/run — 409 in_flight / confirmation_required. */
  async runSchedule(id: string, input: ScheduleRunInput = { confirm: 'RUN' }): Promise<ScheduleRun> {
    if (!USE_MOCK) {
      const response = await http<{ run: ScheduleRun }>(
        `/v1/schedules/${encodeURIComponent(id)}/actions/run`,
        { method: 'POST', body: JSON.stringify(input) },
        scheduleActionError,
      )
      return response.run
    }
    await wait()
    return mockRunSchedule(id, input)
  },

  /** GET /v1/schedules/{id}/runs — newest first, daemon caps limit at 200. */
  async listScheduleRuns(id: string, limit = 50): Promise<ScheduleRun[]> {
    if (!USE_MOCK) {
      return http<ScheduleRun[]>(
        `/v1/schedules/${encodeURIComponent(id)}/runs?limit=${limit}`,
      )
    }
    await wait()
    return mockScheduleRuns(id, limit)
  },

  /** POST /v1/schedules/preview — pure; nothing is written. */
  async previewSchedule(input: SchedulePreviewInput, count = 5): Promise<SchedulePreview> {
    if (!USE_MOCK) {
      return http<SchedulePreview>(
        `/v1/schedules/preview?count=${count}`,
        { method: 'POST', body: JSON.stringify(input) },
        scheduleActionError,
      )
    }
    await wait()
    return mockSchedulePreview(input, count)
  },

  /* --------------------------------------------------------------------
   * Platform surfaces — daemon/internal/api/platform.go
   * ------------------------------------------------------------------ */

  /** GET /v1/renewal/plans — catalogue plus provenance; a failed refresh is soft. */
  async getRenewalPlans(): Promise<RenewalPlansView> {
    if (!USE_MOCK) return http<RenewalPlansView>('/v1/renewal/plans')
    await wait()
    return clone({
      plans: [...MOCK_RENEWAL_PLANS],
      fetchedAt: MOCK_RENEWAL_FETCHED_AT,
      source: 'static' as const,
      stale: true,
    })
  },

  /** GET /v1/renewal/accounts/{id} — expiry state; the URL is the public page. */
  async getRenewalAccount(id: string): Promise<RenewalAccountView> {
    if (!USE_MOCK) {
      return http<RenewalAccountView>(`/v1/renewal/accounts/${encodeURIComponent(id)}`)
    }
    await wait()
    return clone(mockRenewalAccount(id))
  },

  /**
   * GET /v1/usage — inclusive UTC window aggregated over the request log.
   * Empty query fields are omitted; the daemon owns the window defaults.
   */
  async getUsage(query: UsageQuery = {}): Promise<UsageSummary> {
    if (!USE_MOCK) {
      const params = new URLSearchParams()
      if (query.from) params.set('from', query.from)
      if (query.to) params.set('to', query.to)
      if (query.account) params.set('account', query.account)
      if (query.userId) params.set('userId', query.userId)
      const suffix = params.toString()
      return http<UsageSummary>(`/v1/usage${suffix ? `?${suffix}` : ''}`)
    }
    await wait()
    return mockUsageSummary(query)
  },

  /** GET /v1/health/nodes — loopback gate aggregates; `enabled:false` explains itself. */
  async getNodeHealth(): Promise<HealthView> {
    if (!USE_MOCK) return http<HealthView>('/v1/health/nodes')
    await wait()
    return clone({
      enabled: true,
      nodes: getDb().nodes.map((node, index) => mockNodeHealth(node, index)),
    })
  },

  /** GET /v1/webhooks — receivers only; the signing secret is never returned. */
  async listWebhooks(): Promise<WebhookEndpoint[]> {
    if (!USE_MOCK) return http<WebhookEndpoint[]>('/v1/webhooks')
    await wait()
    return clone(mockWebhookStateValue().endpoints.map((endpoint) => mockWebhookView(endpoint)))
  },

  /** POST /v1/webhooks — returns the signing secret exactly once. */
  async createWebhook(input: WebhookCreate): Promise<WebhookCreated> {
    if (!USE_MOCK) {
      return http<WebhookCreated>('/v1/webhooks', {
        method: 'POST',
        body: JSON.stringify(input),
      })
    }
    await wait()
    return clone(mockCreateWebhook(input))
  },

  /** PATCH /v1/webhooks/{id} — only `enabled` is mutable; 409 without a secret. */
  async updateWebhook(id: string, patch: WebhookPatch): Promise<WebhookEndpoint> {
    if (!USE_MOCK) {
      return http<WebhookEndpoint>(`/v1/webhooks/${encodeURIComponent(id)}`, {
        method: 'PATCH',
        body: JSON.stringify(patch),
      })
    }
    await wait()
    return clone(mockPatchWebhook(id, patch))
  },

  /** DELETE /v1/webhooks/{id} — 204, and the sealed secret goes with the row. */
  async deleteWebhook(id: string): Promise<void> {
    if (!USE_MOCK) {
      await http<void>(`/v1/webhooks/${encodeURIComponent(id)}`, { method: 'DELETE' })
      return
    }
    await wait()
    mockDeleteWebhook(id)
  },

  /**
   * GET /v1/webhooks/{id}/deliveries — persisted delivery history merged with
   * the live ring, newest first. `limit` pages the merged list; the daemon
   * defaults to 200 and clamps a larger value to 500.
   */
  async listWebhookDeliveries(
    id: string,
    query: WebhookDeliveriesQuery = {},
  ): Promise<Delivery[]> {
    if (!USE_MOCK) {
      const params = new URLSearchParams()
      if (query.limit !== undefined) params.set('limit', String(query.limit))
      const search = params.toString()
      return http<Delivery[]>(
        `/v1/webhooks/${encodeURIComponent(id)}/deliveries${search ? `?${search}` : ''}`,
      )
    }
    await wait()
    return clone(mockWebhookDeliveries(id, query))
  },

  /** GET /v1/scoped-keys — least-privilege keys; the secret is never read back. */
  async listScopedKeys(): Promise<ScopedKey[]> {
    if (!USE_MOCK) return http<ScopedKey[]>('/v1/scoped-keys')
    await wait()
    return clone(getDb().scopedKeys)
  },

  /** POST /v1/scoped-keys — returns the plaintext secret exactly once. */
  async createScopedKey(input: ScopedKeyCreateInput): Promise<ScopedKeyCreated> {
    if (!USE_MOCK) {
      return http<ScopedKeyCreated>('/v1/scoped-keys', {
        method: 'POST',
        body: JSON.stringify(input),
      })
    }
    await wait()
    return clone(mockCreateScopedKey(input))
  },

  /** DELETE /v1/scoped-keys/{id} — revoking an already-revoked key is a no-op. */
  async revokeScopedKey(id: string): Promise<void> {
    if (!USE_MOCK) {
      await http<void>(`/v1/scoped-keys/${encodeURIComponent(id)}`, { method: 'DELETE' })
      return
    }
    await wait()
    mockRevokeScopedKey(id)
  },

  /**
   * GET /v1/audit/export.csv — returns the CSV as a Blob. The bearer header is
   * added by `request`, so the token never appears in the URL.
   */
  async exportAuditCsv(query: AuditExportQuery = {}): Promise<AuditExportDownload> {
    const params = new URLSearchParams()
    if (query.from) params.set('from', query.from)
    if (query.to) params.set('to', query.to)
    if (query.severity) params.set('severity', query.severity)
    if (!USE_MOCK) {
      const search = params.toString()
      const response = await request(
        `/v1/audit/export.csv${search ? `?${search}` : ''}`,
        { headers: { Accept: 'text/csv' } },
      )
      return {
        blob: await response.blob(),
        filename: attachmentFilename(
          response.headers.get('Content-Disposition'),
          `apc-audit-${exportTimestamp()}.csv`,
        ),
        truncated: response.headers.get('X-Truncated') === 'true',
      }
    }
    await wait()
    const file = mockAuditExportCsv(query)
    return {
      blob: new Blob([file.content], { type: 'text/csv;charset=utf-8' }),
      filename: file.filename,
      truncated: file.truncated,
    }
  },

  /** POST /v1/backup — seals the panel files and serves the envelope. */
  async createBackup(passphrase: string): Promise<BackupDownload> {
    if (!USE_MOCK) {
      const response = await request('/v1/backup', {
        method: 'POST',
        body: JSON.stringify({ passphrase }),
        headers: { Accept: 'application/octet-stream' },
      })
      return {
        blob: await response.blob(),
        filename: attachmentFilename(
          response.headers.get('Content-Disposition'),
          `ajs-backup-${exportTimestamp()}.ajsb`,
        ),
        blobs: exportCount(response.headers.get('X-AJS-Backup-Blobs')),
      }
    }
    await wait()
    const file = mockCreateBackup(passphrase)
    return {
      blob: new Blob([file.content], { type: 'application/octet-stream' }),
      filename: file.filename,
      blobs: file.blobs,
    }
  },

  /**
   * POST /v1/backup/inspect — header only without a passphrase, header plus
   * manifest with one. Neither form touches the state directory.
   */
  async inspectBackup(
    envelope: string | Record<string, unknown>,
    passphrase = '',
  ): Promise<BackupInspectView> {
    const document = parseEnvelopeDocument(envelope)
    if (!USE_MOCK) {
      return http<BackupInspectView>('/v1/backup/inspect', {
        method: 'POST',
        body: JSON.stringify({ envelope: document, passphrase }),
      })
    }
    await wait()
    return clone(mockInspectBackup(document, passphrase))
  },

  /**
   * POST /v1/backup/restore — a dry run unless `dryRun` is explicitly false.
   * The console only ever asks for a dry run; an apply also needs the daemon's
   * AJS_BACKUP_ALLOW_RESTORE and no LIVE node.
   */
  async restoreBackup(input: BackupRestoreInput): Promise<BackupRestoreView> {
    const document = parseEnvelopeDocument(input.envelope)
    if (!USE_MOCK) {
      return http<BackupRestoreView>('/v1/backup/restore', {
        method: 'POST',
        body: JSON.stringify({ ...input, envelope: document }),
      })
    }
    await wait()
    return clone(mockRestoreBackup({ ...input, envelope: document }))
  },

  /**
   * GET /v1/device-pool — filters are server-side; an empty query is the
   * daemon's first page (50). A disabled pool is still a 200 with
   * `enabled:false`, never an error.
   */
  async listDevicePool(query: DevicePoolQuery = {}): Promise<DevicePoolView> {
    if (!USE_MOCK) {
      return http<DevicePoolView>(
        `/v1/device-pool${devicePoolQueryString(query)}`,
        {},
        devicePoolError,
      )
    }
    await wait()
    return mockListDevicePool(query)
  },

  /** POST /v1/device-pool/generate — one all-or-nothing batch. */
  async generateDevices(input: DeviceGenerateInput): Promise<DevicePoolRecord[]> {
    if (!USE_MOCK) {
      const result = await http<{ devices: DevicePoolRecord[] }>(
        '/v1/device-pool/generate',
        { method: 'POST', body: JSON.stringify(input) },
        devicePoolError,
      )
      return result.devices
    }
    await wait()
    return mockGenerateDevices(input)
  },

  /** GET /v1/device-pool/{id} — hashes only, never the raw identity. */
  async getDevicePoolRecord(id: string): Promise<DevicePoolRecord> {
    if (!USE_MOCK) {
      return http<DevicePoolRecord>(
        `/v1/device-pool/${encodeURIComponent(id)}`,
        {},
        devicePoolError,
      )
    }
    await wait()
    return mockGetDevicePoolRecord(id)
  },

  /** PATCH /v1/device-pool/{id} — operator label and notes only. */
  async patchDevicePoolRecord(
    id: string,
    input: DevicePoolPatchInput,
  ): Promise<DevicePoolRecord> {
    if (!USE_MOCK) {
      return http<DevicePoolRecord>(
        `/v1/device-pool/${encodeURIComponent(id)}`,
        { method: 'PATCH', body: JSON.stringify(input) },
        devicePoolError,
      )
    }
    await wait()
    return mockPatchDevicePoolRecord(id, input)
  },

  /** POST /v1/device-pool/{id}/assign — bind one account. */
  async assignDevice(id: string, accountId: string): Promise<DevicePoolRecord> {
    if (!USE_MOCK) {
      return http<DevicePoolRecord>(
        `/v1/device-pool/${encodeURIComponent(id)}/assign`,
        { method: 'POST', body: JSON.stringify({ accountId }) },
        devicePoolError,
      )
    }
    await wait()
    return mockAssignDevice(id, accountId)
  },

  /** POST /v1/device-pool/{id}/release — requires the exact confirm token. */
  async releaseDevice(id: string): Promise<DevicePoolRecord> {
    if (!USE_MOCK) {
      return http<DevicePoolRecord>(
        `/v1/device-pool/${encodeURIComponent(id)}/release`,
        { method: 'POST', body: JSON.stringify({ confirm: 'RELEASE' }) },
        devicePoolError,
      )
    }
    await wait()
    return mockReleaseDevice(id, 'RELEASE')
  },

  /** POST /v1/device-pool/{id}/retire — requires the exact confirm token. */
  async retireDevice(id: string, reason = ''): Promise<DevicePoolRecord> {
    if (!USE_MOCK) {
      return http<DevicePoolRecord>(
        `/v1/device-pool/${encodeURIComponent(id)}/retire`,
        { method: 'POST', body: JSON.stringify({ confirm: 'RETIRE', reason }) },
        devicePoolError,
      )
    }
    await wait()
    return mockRetireDevice(id, 'RETIRE', reason)
  },

  /** POST /v1/device-pool/{id}/quarantine — requires the exact confirm token. */
  async quarantineDevice(id: string, reason = ''): Promise<DevicePoolRecord> {
    if (!USE_MOCK) {
      return http<DevicePoolRecord>(
        `/v1/device-pool/${encodeURIComponent(id)}/quarantine`,
        { method: 'POST', body: JSON.stringify({ confirm: 'QUARANTINE', reason }) },
        devicePoolError,
      )
    }
    await wait()
    return mockQuarantineDevice(id, 'QUARANTINE', reason)
  },

  /** POST /v1/device-pool/{id}/purge — requires the exact confirm token. */
  async purgeDevice(id: string): Promise<void> {
    if (!USE_MOCK) {
      await http<void>(
        `/v1/device-pool/${encodeURIComponent(id)}/purge`,
        { method: 'POST', body: JSON.stringify({ confirm: 'PURGE' }) },
        devicePoolError,
      )
      return
    }
    await wait()
    mockPurgeDevice(id, 'PURGE')
  },

  /**
   * POST /v1/device-pool/export — the daemon answers with the encrypted
   * envelope as JSON, not a file; the page downloads it as `.json`.
   */
  async exportDevicePool(input: DevicePoolExportInput): Promise<DeviceExportEnvelope> {
    if (!USE_MOCK) {
      return http<DeviceExportEnvelope>(
        '/v1/device-pool/export',
        { method: 'POST', body: JSON.stringify(input) },
        devicePoolError,
      )
    }
    await wait()
    return mockExportDevicePool(input)
  },

  /**
   * POST /v1/device-pool/import — dry run unless `dryRun` is explicitly false.
   * A 409 rejects with `DevicePoolError.conflicts` populated.
   */
  async importDevicePool(input: DevicePoolImportInput): Promise<DeviceImportResult> {
    const document = parseEnvelopeDocument(input.envelope)
    if (!USE_MOCK) {
      return http<DeviceImportResult>(
        '/v1/device-pool/import',
        {
          method: 'POST',
          body: JSON.stringify({
            passphrase: input.passphrase,
            envelope: document,
            dryRun: input.dryRun ?? false,
            bind: input.bind ?? false,
            ...(input.confirm ? { confirm: input.confirm } : {}),
          }),
        },
        devicePoolError,
      )
    }
    await wait()
    // The mock re-validates the document at runtime; the cast only bridges the
    // parsed JSON object to the envelope type.
    return mockImportDevicePool({
      ...input,
      envelope: document as unknown as DeviceExportEnvelope,
    })
  },

  async resetMock(): Promise<void> {
    if (!USE_MOCK) return
    await wait()
    resetMockDb()
    mockSelections.clear()
    mockScheduleState = null
    mockWebhookState = null
    mockCatalogSeq = 0
    snapshotEpoch += 1
    snapshotCache.clear()
    snapshotInFlight.clear()
    getDb().settings = readSettings()
  },
}
