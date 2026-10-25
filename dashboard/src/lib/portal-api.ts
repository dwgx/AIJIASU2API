import { ApiError } from './errors'
import { getDb } from './mock'
import {
  mockCancelOrder,
  mockCheckoutOrder,
  mockCreateOrder,
  mockListOrders,
  mockListPlans,
  resetOrderMock,
} from './order-mock'
import type { ApiKey, AuditEvent, Lease, Order, Plan, UsageBucket, UsageQuery, UsageSummary } from './types'

/* --------------------------------------------------------------------------
 * Customer portal client (DESIGN-TENANT.md)
 *
 * A tenant authenticates with an opaque HttpOnly cookie; this module never
 * stores a bearer token and never writes Web Storage. Unsafe calls replay the
 * session's CSRF token in `X-CSRF-Token`; the token lives in memory only and
 * is refreshed from `/v1/portal/session` on reload.
 *
 * Mock mode is deterministic and offline: one active tenant (linked to the
 * seeded `u-01` user), one pending applicant, and one suspended tenant.
 * ------------------------------------------------------------------------ */

export type PortalTenantStatus = 'pending' | 'active' | 'suspended'

/** `Tenant` schema: the login identity linked 1:1 to a PanelUser. */
export interface PortalTenant {
  id: string
  email: string
  displayName: string
  status: PortalTenantStatus
  userId: string | null
  createdAt: string
  approvedAt: string | null
  suspendedAt: string | null
}

/** The linked PanelUser, already stripped of secrets by the daemon. */
export interface PortalUser {
  id: string
  name: string
  platform?: string
  enabled?: boolean
  quotaBytesMonth?: number | null
  usedBytes?: number
}

/** `{tenant,user,csrfToken}` — login and session return the same shape. */
export interface PortalSession {
  tenant: PortalTenant
  user: PortalUser | null
  csrfToken: string
}

export interface PortalSignupInput {
  email: string
  password: string
  displayName: string
}

export interface PortalLoginInput {
  email: string
  password: string
}

export interface PortalSignupResult {
  status: 'pending'
}

export interface PortalKeyCreated {
  key: ApiKey
  secret: string
}

/** POST /v1/portal/orders/{id}/checkout — a hosted URL, never a payment fact. */
export interface PortalCheckoutResult {
  payUrl: string
}

/** Query for the tenant usage view; the daemon ignores owner filters. */
export type PortalUsageQuery = Pick<UsageQuery, 'from' | 'to'>

/**
 * Cursor query for the tenant audit view. `before` is the id of the last row
 * already rendered, never an index: the daemon ring drops old rows, and a
 * numeric offset would then repeat or skip activity.
 */
export interface PortalAuditQuery {
  limit?: number
  before?: string | null
}

export class PortalError extends ApiError {
  readonly retryAfterSec: number | null

  constructor(status: number, message: string, code: string | null = null, retryAfterSec: number | null = null) {
    super(status, message, code)
    this.name = 'PortalError'
    this.retryAfterSec = retryAfterSec
  }
}

/** Read per call so tests can stub VITE_USE_MOCK without reloading modules. */
function mockEnabled(): boolean {
  return import.meta.env.VITE_USE_MOCK !== 'false'
}
const API_BASE = (import.meta.env.VITE_API_BASE ?? '').replace(/\/$/, '')
const MOCK_DELAY = import.meta.env.MODE === 'test' ? 0 : 220

/** In-memory CSRF token for this tab; never persisted, never rendered. */
let csrfToken: string | null = null

async function wait(): Promise<void> {
  if (MOCK_DELAY <= 0) return
  const { promise, resolve } = Promise.withResolvers<void>()
  setTimeout(resolve, MOCK_DELAY)
  await promise
}

function clone<T>(value: T): T {
  return structuredClone(value)
}

function parseRetryAfter(header: string | null): number | null {
  if (!header) return null
  const seconds = Number(header)
  if (Number.isFinite(seconds) && seconds >= 0) return Math.ceil(seconds)
  const at = Date.parse(header)
  if (!Number.isFinite(at)) return null
  return Math.max(0, Math.ceil((at - Date.now()) / 1000))
}

function parseErrorBody(raw: string): { code: string | null; message: string } {
  if (!raw) return { code: null, message: '' }
  try {
    const body = JSON.parse(raw) as { error?: { code?: unknown; message?: unknown } }
    const error = body?.error
    return {
      code: typeof error?.code === 'string' ? error.code : null,
      message: typeof error?.message === 'string' ? error.message : '',
    }
  } catch {
    return { code: null, message: raw.slice(0, 200) }
  }
}

function requireCsrf(): string {
  if (!csrfToken) {
    throw new PortalError(403, 'Missing CSRF token; reload the portal.', 'CSRF_MISSING')
  }
  return csrfToken
}

async function request<T>(
  path: string,
  options: {
    method?: 'GET' | 'POST' | 'DELETE'
    body?: unknown
    csrf?: boolean
    headers?: Record<string, string>
  } = {},
): Promise<T> {
  const method = options.method ?? 'GET'
  const headers = new Headers({ Accept: 'application/json' })
  for (const [name, value] of Object.entries(options.headers ?? {})) headers.set(name, value)
  if (options.body !== undefined) headers.set('Content-Type', 'application/json')
  if (method !== 'GET' && options.csrf !== false) headers.set('X-CSRF-Token', requireCsrf())

  let response: Response
  try {
    response = await fetch(`${API_BASE}${path}`, {
      method,
      headers,
      credentials: 'include',
      cache: 'no-store',
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
    })
  } catch (cause) {
    throw new PortalError(0, cause instanceof Error ? cause.message : 'Network error', 'NETWORK')
  }

  if (!response.ok) {
    const raw = await response.text().catch(() => '')
    const { code, message } = parseErrorBody(raw)
    if (response.status === 401) csrfToken = null
    throw new PortalError(
      response.status,
      message || `HTTP ${response.status} ${response.statusText}`.trim(),
      code,
      parseRetryAfter(response.headers.get('Retry-After')),
    )
  }

  if (response.status === 204) return undefined as T
  const raw = await response.text()
  if (!raw) return undefined as T
  return JSON.parse(raw) as T
}

function rememberSession(session: PortalSession): PortalSession {
  csrfToken = session.csrfToken
  return session
}

/* --------------------------------------------------------------------------
 * Deterministic mock backend
 * ------------------------------------------------------------------------ */

interface MockTenant extends PortalTenant {
  /** Mock-only plaintext; the daemon stores Argon2id and never returns it. */
  password: string
}

interface MockPortalState {
  tenants: MockTenant[]
  sessionTenantId: string | null
  sessionSeq: number
  signupSeq: number
  keys: ApiKey[]
  keySeq: number
}

export const MOCK_PORTAL_EMAIL = 'ada@example.com'
export const MOCK_PORTAL_PASSWORD = 'portal-demo-2026'
export const MOCK_PENDING_EMAIL = 'pending@example.com'
export const MOCK_SUSPENDED_EMAIL = 'suspended@example.com'
const MOCK_PORTAL_USER_ID = 'u-01'
const MOCK_DAY_MS = 86_400_000
const MOCK_USAGE_DEFAULT_DAYS = 30
const MOCK_USAGE_MAX_DAYS = 366
export const PORTAL_PASSWORD_MIN = 10

/**
 * A fresh key per create dialog. Retrying a failed submit with the same key is
 * safe: the daemon replays the original order instead of creating a second one.
 * Never persisted or rendered.
 */
export function newOrderIdempotencyKey(): string {
  const cryptoApi = globalThis.crypto
  if (cryptoApi && typeof cryptoApi.randomUUID === 'function') return cryptoApi.randomUUID()
  return `order-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

let mockState: MockPortalState | null = null

function mockRFC3339(ms: number): string {
  return new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')
}

function mockTenant(
  id: string,
  email: string,
  displayName: string,
  status: PortalTenantStatus,
  userId: string | null,
  now: number,
): MockTenant {
  return {
    id,
    email,
    displayName,
    status,
    userId,
    createdAt: mockRFC3339(now - 12 * MOCK_DAY_MS),
    approvedAt: status === 'pending' ? null : mockRFC3339(now - 10 * MOCK_DAY_MS),
    suspendedAt: status === 'suspended' ? mockRFC3339(now - 2 * MOCK_DAY_MS) : null,
    password: MOCK_PORTAL_PASSWORD,
  }
}

function mockStateValue(): MockPortalState {
  if (mockState) return mockState
  const now = Date.now()
  mockState = {
    tenants: [
      mockTenant('tenant-01', MOCK_PORTAL_EMAIL, 'Ada Ops', 'active', MOCK_PORTAL_USER_ID, now),
      mockTenant('tenant-02', MOCK_PENDING_EMAIL, 'Pending Applicant', 'pending', null, now),
      mockTenant('tenant-03', MOCK_SUSPENDED_EMAIL, 'Suspended Tenant', 'suspended', 'u-02', now),
    ],
    sessionTenantId: null,
    sessionSeq: 0,
    signupSeq: 0,
    keys: getDb().keys.filter((key) => key.userId === MOCK_PORTAL_USER_ID).map((key) => clone(key)),
    keySeq: 0,
  }
  return mockState
}

/** Resets mock tenants, sessions and portal-issued keys (tests call this). */
export function resetPortalMock(): void {
  const db = getDb()
  db.keys = db.keys.filter((key) => !key.id.startsWith('key-p'))
  resetOrderMock()
  mockState = null
  csrfToken = null
}

function mockTenantView(tenant: MockTenant): PortalTenant {
  const { password: _password, ...view } = tenant
  return clone(view)
}

function mockUserView(userId: string | null): PortalUser | null {
  if (!userId) return null
  const user = getDb().users.find((candidate) => candidate.id === userId)
  if (!user) return null
  return {
    id: user.id,
    name: user.name,
    platform: user.platform,
    enabled: user.enabled,
    quotaBytesMonth: user.quotaBytesMonth,
    usedBytes: user.usedBytes,
  }
}

function mockSession(tenant: MockTenant): PortalSession {
  mockStateValue().sessionSeq += 1
  return {
    tenant: mockTenantView(tenant),
    user: mockUserView(tenant.userId),
    csrfToken: `csrf_mock_${mockStateValue().sessionSeq}`,
  }
}

function mockLogin(input: PortalLoginInput): PortalSession {
  const state = mockStateValue()
  const email = input.email.trim().toLowerCase()
  const tenant = state.tenants.find((candidate) => candidate.email === email)
  if (!tenant || tenant.password !== input.password) {
    throw new PortalError(401, 'Invalid email or password', 'UNAUTHORIZED')
  }
  if (tenant.status === 'pending') {
    throw new PortalError(403, 'This account is awaiting approval', 'TENANT_PENDING')
  }
  if (tenant.status === 'suspended') {
    throw new PortalError(403, 'This account is suspended', 'TENANT_SUSPENDED')
  }
  const user = mockUserView(tenant.userId)
  if (!user || user.enabled === false) {
    throw new PortalError(403, 'The linked user is disabled', 'TENANT_DISABLED')
  }
  state.sessionTenantId = tenant.id
  return mockSession(tenant)
}

function mockSignup(input: PortalSignupInput): PortalSignupResult {
  const state = mockStateValue()
  const email = input.email.trim().toLowerCase()
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
    throw new PortalError(400, 'Enter a valid email address', 'INVALID_ARGUMENT')
  }
  if (input.password.length < PORTAL_PASSWORD_MIN) {
    throw new PortalError(400, `Password must be at least ${PORTAL_PASSWORD_MIN} characters`, 'INVALID_ARGUMENT')
  }
  if (!input.displayName.trim()) {
    throw new PortalError(400, 'Display name is required', 'INVALID_ARGUMENT')
  }
  // Duplicate emails receive the same pending answer; nothing is revealed.
  if (!state.tenants.some((tenant) => tenant.email === email)) {
    state.signupSeq += 1
    const now = Date.now()
    state.tenants.push({
      id: `tenant-p${String(state.signupSeq).padStart(2, '0')}`,
      email,
      displayName: input.displayName.trim(),
      status: 'pending',
      userId: null,
      createdAt: mockRFC3339(now),
      approvedAt: null,
      suspendedAt: null,
      password: input.password,
    })
  }
  return { status: 'pending' }
}

function mockCurrentSession(): PortalSession {
  const state = mockStateValue()
  const tenant = state.tenants.find((candidate) => candidate.id === state.sessionTenantId)
  if (!tenant) throw new PortalError(401, 'Not signed in', 'UNAUTHORIZED')
  if (tenant.status === 'suspended') {
    throw new PortalError(403, 'This account is suspended', 'TENANT_SUSPENDED')
  }
  if (tenant.status !== 'active') {
    throw new PortalError(403, 'This account is not active', 'TENANT_PENDING')
  }
  const user = mockUserView(tenant.userId)
  if (!user || user.enabled === false) {
    throw new PortalError(403, 'The linked user is disabled', 'TENANT_DISABLED')
  }
  return {
    tenant: mockTenantView(tenant),
    user,
    csrfToken: csrfToken ?? `csrf_mock_${state.sessionSeq || 1}`,
  }
}

function mockActiveTenant(): MockTenant {
  const state = mockStateValue()
  const tenant = state.tenants.find((candidate) => candidate.id === state.sessionTenantId)
  if (!tenant || tenant.status !== 'active' || !tenant.userId) {
    throw new PortalError(401, 'Not signed in', 'UNAUTHORIZED')
  }
  return tenant
}

function mockLeases(): Lease[] {
  const tenant = mockActiveTenant()
  return clone(
    getDb().leases.filter((lease) => lease.userId === tenant.userId && lease.status === 'active'),
  )
}

interface MockUsageWindow {
  days: string[]
  startMs: number
  endMs: number
}

function mockUsageWindow(query: PortalUsageQuery): MockUsageWindow {
  const now = Date.now()
  const toMs = query.to ? Date.parse(query.to) : now
  const fromMs = query.from ? Date.parse(query.from) : toMs - (MOCK_USAGE_DEFAULT_DAYS - 1) * MOCK_DAY_MS
  const startDay = Number.isFinite(fromMs) ? Math.floor(fromMs / MOCK_DAY_MS) * MOCK_DAY_MS : now
  const endDay = Number.isFinite(toMs) ? Math.floor(toMs / MOCK_DAY_MS) * MOCK_DAY_MS : now
  const span = Math.round((endDay - startDay) / MOCK_DAY_MS) + 1
  const count = Math.max(1, Math.min(span, MOCK_USAGE_MAX_DAYS))
  const start = Math.min(startDay, endDay - (count - 1) * MOCK_DAY_MS)
  const days: string[] = []
  for (let index = 0; index < count; index += 1) {
    days.push(new Date(start + index * MOCK_DAY_MS).toISOString().slice(0, 10))
  }
  return { days, startMs: start, endMs: endDay + MOCK_DAY_MS - 1 }
}

function mockHostOnly(target: string): string {
  const value = target.trim()
  if (!value) return '—'
  const authority = value.includes('://') ? (() => {
    try {
      return new URL(value).hostname
    } catch {
      return value
    }
  })() : value.split(/[/?#]/, 1)[0]
  if (authority.includes('@')) return authority.slice(authority.lastIndexOf('@') + 1).split(':')[0]
  if (authority.startsWith('[')) return authority
  return authority.split(':')[0]
}

function mockUsage(query: PortalUsageQuery): UsageSummary {
  const tenant = mockActiveTenant()
  const window = mockUsageWindow(query)
  const byDay = new Map(window.days.map((day) => [day, { requests: 0, bytes: 0, errors: 0, latency: 0 }]))
  const byAccount = new Map<string, { requests: number; bytes: number; errors: number; latency: number; buckets: Map<string, { requests: number; bytes: number; errors: number; latency: number }> }>()
  const byTarget = new Map<string, { requests: number; bytes: number }>()
  const user = getDb().users.find((candidate) => candidate.id === tenant.userId)
  let requests = 0
  let bytes = 0
  let errors = 0

  for (const row of getDb().requests) {
    if (row.userId !== tenant.userId) continue
    const ts = Date.parse(row.ts)
    if (!Number.isFinite(ts) || ts < window.startMs || ts > window.endMs) continue
    const day = new Date(Math.floor(ts / MOCK_DAY_MS) * MOCK_DAY_MS).toISOString().slice(0, 10)
    const bucket = byDay.get(day)
    if (!bucket) continue
    requests += 1
    bytes += row.bytes
    const failed = row.status >= 400
    if (failed) errors += 1
    bucket.requests += 1
    bucket.bytes += row.bytes
    bucket.errors += failed ? 1 : 0
    bucket.latency += row.latencyMs

    const account = byAccount.get(row.accountLabel) ?? {
      requests: 0,
      bytes: 0,
      errors: 0,
      latency: 0,
      buckets: new Map(),
    }
    const accountDay = account.buckets.get(day) ?? { requests: 0, bytes: 0, errors: 0, latency: 0 }
    account.requests += 1
    account.bytes += row.bytes
    account.errors += failed ? 1 : 0
    account.latency += row.latencyMs
    accountDay.requests += 1
    accountDay.bytes += row.bytes
    accountDay.errors += failed ? 1 : 0
    accountDay.latency += row.latencyMs
    account.buckets.set(day, accountDay)
    byAccount.set(row.accountLabel, account)

    const target = mockHostOnly(row.target)
    const targetRow = byTarget.get(target) ?? { requests: 0, bytes: 0 }
    targetRow.requests += 1
    targetRow.bytes += row.bytes
    byTarget.set(target, targetRow)
  }

  const dayBuckets: UsageBucket[] = window.days.map((day) => {
    const bucket = byDay.get(day) ?? { requests: 0, bytes: 0, errors: 0, latency: 0 }
    return {
      day,
      requests: bucket.requests,
      bytes: bucket.bytes,
      errors: bucket.errors,
      avgLatencyMs: bucket.requests === 0 ? 0 : Math.round(bucket.latency / bucket.requests),
    }
  })

  const accounts = [...byAccount.entries()]
    .map(([account, value]) => ({
      account,
      requests: value.requests,
      bytes: value.bytes,
      errors: value.errors,
      avgLatencyMs: value.requests === 0 ? 0 : Math.round(value.latency / value.requests),
      buckets: window.days.map((day) => {
        const bucket = value.buckets.get(day) ?? { requests: 0, bytes: 0, errors: 0, latency: 0 }
        return {
          day,
          requests: bucket.requests,
          bytes: bucket.bytes,
          errors: bucket.errors,
          avgLatencyMs: bucket.requests === 0 ? 0 : Math.round(bucket.latency / bucket.requests),
        }
      }),
      trafficBudgetBytes: null,
      usedBytes: value.bytes,
      remainingBytes: null,
    }))
    .sort((left, right) => right.bytes - left.bytes)

  const topTargets = [...byTarget.entries()]
    .map(([target, value]) => ({ target, requests: value.requests, bytes: value.bytes }))
    .sort((left, right) => right.requests - left.requests)
    .slice(0, 10)

  return {
    from: new Date(window.startMs).toISOString(),
    to: new Date(window.endMs).toISOString(),
    requests,
    bytes,
    errors,
    days: dayBuckets,
    accounts,
    users: [
      {
        userId: tenant.userId ?? '',
        userName: user?.name ?? '',
        requests,
        bytes,
        quotaBytesMonth: user?.quotaBytesMonth ?? null,
        usedBytes: bytes,
      },
    ],
    topTargets,
    truncated: false,
    rowsRetained: requests,
    retentionDays: window.days.length,
    quotaEnforced: false,
  }
}

function mockKeys(): ApiKey[] {
  const tenant = mockActiveTenant()
  return clone(mockStateValue().keys.filter((key) => key.userId === tenant.userId))
}

function mockIssueKey(label: string): PortalKeyCreated {
  const tenant = mockActiveTenant()
  const trimmed = label.trim()
  if (trimmed.length > 80) {
    throw new PortalError(400, 'Label must be at most 80 characters', 'INVALID_ARGUMENT')
  }
  const state = mockStateValue()
  state.keySeq += 1
  const suffix = state.keySeq.toString(16).padStart(24, '0')
  const secret = `ajs_live_mock_portal_${suffix}`
  const key: ApiKey = {
    id: `key-p${String(state.keySeq).padStart(2, '0')}`,
    userId: tenant.userId ?? '',
    userName: mockUserView(tenant.userId)?.name ?? '',
    label: trimmed || 'Untitled key',
    prefix: secret.slice(0, 16),
    createdAt: mockRFC3339(Date.now()),
    lastUsedAt: null,
    revokedAt: null,
  }
  state.keys.unshift(key)
  getDb().keys.unshift(clone(key))
  return { key: clone(key), secret }
}

function mockRevokeKey(id: string): void {
  const tenant = mockActiveTenant()
  const key = mockStateValue().keys.find((candidate) => candidate.id === id)
  if (!key || key.userId !== tenant.userId) {
    throw new PortalError(404, 'Key not found', 'NOT_FOUND')
  }
  if (key.revokedAt) return
  key.revokedAt = mockRFC3339(Date.now())
  const shared = getDb().keys.find((candidate) => candidate.id === id)
  if (shared) shared.revokedAt = key.revokedAt
}

/**
 * Own-only audit stream for the mock. It mirrors the daemon's attribution:
 * admin-seeded rows that name the linked user, plus the customer actions the
 * mock itself can observe (keys, orders, leases, the current session). Rows
 * for another tenant or another user never enter the list.
 */
function mockAudit(query: PortalAuditQuery = {}): AuditEvent[] {
  const tenant = mockActiveTenant()
  const userID = tenant.userId ?? ''
  const userName = mockUserView(userID)?.name ?? ''
  const state = mockStateValue()
  const events: AuditEvent[] = []

  for (const event of getDb().audit) {
    if (event.target === userName || event.target === userID) events.push(clone(event))
  }

  for (const key of state.keys) {
    if (key.userId !== userID) continue
    events.push({
      id: `portal-${key.id}-create`,
      ts: key.createdAt,
      severity: 'info',
      actor: 'operator',
      action: 'key.create',
      target: userName,
      detail: `Key prefix ${key.prefix}`,
    })
    if (key.revokedAt) {
      events.push({
        id: `portal-${key.id}-revoke`,
        ts: key.revokedAt,
        severity: 'info',
        actor: 'portal',
        action: 'key.revoke',
        target: userName,
        detail: `Key prefix ${key.prefix} revoked`,
      })
    }
  }

  for (const order of mockListOrders(tenant.id, userID)) {
    events.push({
      id: `portal-${order.id}-created`,
      ts: order.createdAt,
      severity: 'info',
      actor: `tenant:${tenant.id}`,
      action: 'order.created',
      target: order.id,
      detail: `${order.plan.name} · ${order.state}`,
    })
    if (order.cancelledAt) {
      events.push({
        id: `portal-${order.id}-cancelled`,
        ts: order.cancelledAt,
        severity: 'info',
        actor: `tenant:${tenant.id}`,
        action: 'order.cancelled',
        target: order.id,
        detail: 'awaiting payment cancelled',
      })
    }
  }

  for (const lease of getDb().leases) {
    if (lease.userId !== userID) continue
    events.push({
      id: `portal-${lease.id}-create`,
      ts: lease.createdAt,
      severity: 'info',
      actor: 'operator',
      action: 'lease.create',
      target: userName,
      detail: `${lease.platform} · ${lease.accountLabel} · node ${lease.nodeLabel}`,
    })
  }

  events.push({
    id: `portal-${tenant.id}-session`,
    ts: mockRFC3339(Date.now()),
    severity: 'info',
    actor: `tenant:${tenant.id}`,
    action: 'session.login',
    target: userName,
    detail: 'Portal session started',
  })

  const unique = new Map(events.map((event) => [event.id, event]))
  const ordered = [...unique.values()].sort(
    (left, right) => Date.parse(right.ts) - Date.parse(left.ts) || right.id.localeCompare(left.id),
  )
  const found = query.before ? ordered.findIndex((event) => event.id === query.before) : -1
  const start = found >= 0 ? found + 1 : 0
  const limit = query.limit && query.limit > 0 ? query.limit : 50
  return ordered.slice(start, start + limit)
}

/* --------------------------------------------------------------------------
 * Public client
 * ------------------------------------------------------------------------ */

export const portalApi = {
  async signup(input: PortalSignupInput): Promise<PortalSignupResult> {
    if (mockEnabled()) {
      await wait()
      return mockSignup(input)
    }
    return request<PortalSignupResult>('/v1/portal/signup', {
      method: 'POST',
      body: input,
      csrf: false,
    })
  },

  async login(input: PortalLoginInput): Promise<PortalSession> {
    if (mockEnabled()) {
      await wait()
      return rememberSession(mockLogin(input))
    }
    return rememberSession(
      await request<PortalSession>('/v1/portal/login', {
        method: 'POST',
        body: input,
        csrf: false,
      }),
    )
  },

  async session(): Promise<PortalSession> {
    if (mockEnabled()) {
      await wait()
      return rememberSession(mockCurrentSession())
    }
    return rememberSession(await request<PortalSession>('/v1/portal/session'))
  },

  async logout(): Promise<void> {
    if (mockEnabled()) {
      await wait()
      mockStateValue().sessionTenantId = null
      csrfToken = null
      return
    }
    try {
      await request<void>('/v1/portal/logout', { method: 'POST' })
    } finally {
      csrfToken = null
    }
  },

  async leases(): Promise<Lease[]> {
    if (mockEnabled()) {
      await wait()
      return mockLeases()
    }
    return request<Lease[]>('/v1/portal/leases')
  },

  async usage(query: PortalUsageQuery = {}): Promise<UsageSummary> {
    if (mockEnabled()) {
      await wait()
      return mockUsage(query)
    }
    const params = new URLSearchParams()
    if (query.from) params.set('from', query.from)
    if (query.to) params.set('to', query.to)
    const search = params.toString()
    return request<UsageSummary>(`/v1/portal/usage${search ? `?${search}` : ''}`)
  },

  async keys(): Promise<ApiKey[]> {
    if (mockEnabled()) {
      await wait()
      return mockKeys()
    }
    return request<ApiKey[]>('/v1/portal/keys')
  },

  /**
   * GET /v1/portal/audit — the session owner's own audit rows, newest first.
   * Pass the last rendered id as `before` to fetch the next page.
   */
  async audit(query: PortalAuditQuery = {}): Promise<AuditEvent[]> {
    if (mockEnabled()) {
      await wait()
      return mockAudit(query)
    }
    const params = new URLSearchParams()
    if (query.limit !== undefined) params.set('limit', String(query.limit))
    if (query.before) params.set('before', query.before)
    const search = params.toString()
    return request<AuditEvent[]>(`/v1/portal/audit${search ? `?${search}` : ''}`)
  },

  async issueKey(label: string): Promise<PortalKeyCreated> {
    if (mockEnabled()) {
      await wait()
      return mockIssueKey(label)
    }
    return request<PortalKeyCreated>('/v1/portal/keys', {
      method: 'POST',
      body: { label },
    })
  },

  async revokeKey(id: string): Promise<void> {
    if (mockEnabled()) {
      await wait()
      mockRevokeKey(id)
      return
    }
    await request<void>(`/v1/portal/keys/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  /** GET /v1/portal/plans — active catalog only. */
  async plans(): Promise<Plan[]> {
    if (mockEnabled()) {
      await wait()
      return mockListPlans()
    }
    return request<Plan[]>('/v1/portal/plans')
  },

  /** GET /v1/portal/orders — the session owner's orders, newest first. */
  async orders(): Promise<Order[]> {
    if (mockEnabled()) {
      await wait()
      const tenant = mockActiveTenant()
      return mockListOrders(tenant.id, tenant.userId ?? '')
    }
    return request<Order[]>('/v1/portal/orders')
  },

  /**
   * POST /v1/portal/orders — a request, never a payment. The same key with the
   * same plan replays the original order; a different plan conflicts (409).
   */
  async createOrder(planId: string, idempotencyKey: string): Promise<Order> {
    if (mockEnabled()) {
      await wait()
      const tenant = mockActiveTenant()
      return mockCreateOrder(tenant.id, tenant.userId ?? '', planId, idempotencyKey)
    }
    return request<Order>('/v1/portal/orders', {
      method: 'POST',
      body: { planId },
      headers: { 'Idempotency-Key': idempotencyKey },
    })
  },

  /**
   * POST /v1/portal/orders/{id}/checkout — returns the 易支付 hosted URL.
   * This is intent, not payment: the order stays awaiting_payment until a
   * verified provider notify or reconciliation settles it.
   */
  async checkoutOrder(
    id: string,
    type: 'alipay' | 'wxpay' = 'alipay',
  ): Promise<PortalCheckoutResult> {
    if (mockEnabled()) {
      await wait()
      const tenant = mockActiveTenant()
      try {
        return mockCheckoutOrder(id, type, { tenantId: tenant.id, userId: tenant.userId ?? '' })
      } catch (cause) {
        if (cause instanceof ApiError) {
          throw new PortalError(cause.status, cause.message, cause.code)
        }
        throw cause
      }
    }
    return request<PortalCheckoutResult>(
      `/v1/portal/orders/${encodeURIComponent(id)}/checkout`,
      { method: 'POST', body: { type } },
    )
  },

  /** POST /v1/portal/orders/{id}/cancel — awaiting-payment orders only. */
  async cancelOrder(id: string): Promise<Order> {
    if (mockEnabled()) {
      await wait()
      const tenant = mockActiveTenant()
      return mockCancelOrder(id, { tenantId: tenant.id, userId: tenant.userId ?? '' })
    }
    return request<Order>(`/v1/portal/orders/${encodeURIComponent(id)}/cancel`, {
      method: 'POST',
    })
  },

  /** Test hook: reset the in-browser portal backend. */
  async resetMock(): Promise<void> {
    if (!mockEnabled()) return
    await wait()
    resetPortalMock()
  },
}

/** True when the daemon answered 503 because AJS_TENANT_PORTAL is off. */
export function isPortalDisabled(error: unknown): boolean {
  return error instanceof PortalError && error.status === 503
}

/** True when the error means the session is gone and the UI must sign in. */
export function isPortalAuthError(error: unknown): boolean {
  return error instanceof PortalError && (error.status === 401 || error.status === 403)
}
