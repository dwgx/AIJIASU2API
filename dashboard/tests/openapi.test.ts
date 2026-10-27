// @ts-expect-error Node built-in; this project does not depend on @types/node.
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

declare const process: { cwd(): string }

/**
 * Contract tests for docs/openapi.json.
 *
 * The document is the source of truth for the daemon's HTTP surface, so these
 * tests fail when it stops parsing, drifts from the route table registered in
 * daemon/internal/api/server.go, leaves a $ref dangling, or reuses an
 * operationId. Proposed interfaces are allowed to be absent from the route
 * table, but they must say so through x-implementation-phase.
 */

interface JsonSchema {
  type?: string
  properties?: Record<string, JsonSchema>
  required?: string[]
  items?: JsonSchema
  enum?: string[]
  oneOf?: JsonSchema[]
  minimum?: number
  maximum?: number
  default?: unknown
  [keyword: string]: unknown
}

interface Parameter {
  name?: string
  in?: string
  $ref?: string
  description?: string
  schema?: JsonSchema
}

interface ExampleObject {
  summary?: string
  value?: unknown
}

interface MediaType {
  schema?: JsonSchema
  examples?: Record<string, ExampleObject>
}

interface ResponseObject {
  $ref?: string
  description?: string
  content?: Record<string, MediaType>
  headers?: Record<string, unknown>
}

interface Operation {
  operationId: string
  summary?: string
  tags?: string[]
  'x-implementation-phase'?: string
  'x-required-scope'?: string
  security?: unknown[]
  responses: Record<string, ResponseObject>
  parameters?: Parameter[]
  requestBody?: { content?: Record<string, MediaType> }
}

interface OpenAPIDocument {
  openapi: string
  info: { title: string; version: string; description?: string }
  servers: Array<{ url: string; description?: string }>
  security: Array<Record<string, string[]>>
  paths: Record<string, Record<string, Operation>>
  components: {
    securitySchemes: Record<string, { type?: string; scheme?: string; in?: string; name?: string }>
    schemas: Record<string, JsonSchema>
    responses: Record<string, ResponseObject>
    headers: Record<string, unknown>
  }
}

interface OperationEntry {
  method: string
  path: string
  operation: Operation
}

// Vitest's import.meta.url is not a file: URL under jsdom, so resolve from the
// package root that npm sets as the working directory.
const cwd = process.cwd()
// The named type describes the surface this suite consumes; the assertions
// below are the runtime validation of the parsed document.
const doc = JSON.parse(
  readFileSync(`${cwd}/../docs/openapi.json`, 'utf8'),
) as OpenAPIDocument
const serverSource = readFileSync(
  `${cwd}/../daemon/internal/api/server.go`,
  'utf8',
)
// The reissue actions are registered beside their handlers instead of in the
// central router table, so the parity scan must read both files or those
// routes drift out of the document unnoticed.
const ordersSource = readFileSync(
  `${cwd}/../daemon/internal/api/orders.go`,
  'utf8',
)
// routeScopes is the daemon's least-privilege table. The document must mirror
// it exactly, so the suite parses the Go map instead of trusting a copy.
const scopedKeysSource = readFileSync(
  `${cwd}/../daemon/internal/api/scoped_keys.go`,
  'utf8',
)

function operations(): OperationEntry[] {
  const entries: OperationEntry[] = []
  for (const [path, item] of Object.entries(doc.paths)) {
    for (const [method, operation] of Object.entries(item)) {
      entries.push({ method, path, operation })
    }
  }
  return entries
}

/** Resolve one local JSON pointer, throwing on a missing segment. */
function resolvePointer(root: unknown, ref: string): unknown {
  if (!ref.startsWith('#/')) throw new Error(`non-local $ref: ${ref}`)
  let current: unknown = root
  for (const raw of ref.slice(2).split('/')) {
    const token = decodeURIComponent(raw).replace(/~1/g, '/').replace(/~0/g, '~')
    if (Array.isArray(current)) {
      const index = Number(token)
      if (!Number.isInteger(index) || index < 0 || index >= current.length) {
        throw new Error(`unresolved $ref: ${ref} (missing index ${token})`)
      }
      current = current[index]
      continue
    }
    if (typeof current !== 'object' || current === null) {
      throw new Error(`unresolved $ref: ${ref} (not an object at ${token})`)
    }
    const container = current as Record<string, unknown>
    if (!(token in container)) throw new Error(`unresolved $ref: ${ref} (missing ${token})`)
    current = container[token]
  }
  return current
}

function collectRefs(node: unknown, at: string, out: Array<{ at: string; ref: string }>): void {
  if (Array.isArray(node)) {
    node.forEach((value, index) => collectRefs(value, `${at}/${index}`, out))
    return
  }
  if (typeof node !== 'object' || node === null) return
  for (const [key, value] of Object.entries(node)) {
    if (key === '$ref' && typeof value === 'string') {
      out.push({ at, ref: value })
      continue
    }
    collectRefs(value, `${at}/${key}`, out)
  }
}

/** The exact route table from server.go and orders.go: "get /v1/accounts", ... */
function serverRoutes(): string[] {
  const pattern = /rt\.handle\(http\.Method(Get|Post|Put|Patch|Delete),\s*"([^"]+)"/g
  return [serverSource, ordersSource].flatMap((source) =>
    [...source.matchAll(pattern)].map(
      ([, method, path]) => `${method.toLowerCase()} ${path}`,
    ),
  )
}

/** The routeScopes map from scoped_keys.go as "get /v1/accounts" → "accounts:read". */
function scopedRouteScopes(): Array<[string, string]> {
  const pattern =
    /http\.Method(Get|Post|Put|Patch|Delete)\s*\+\s*"([^"]+)":\s*model\.Scope([A-Za-z]+)/g
  return [...scopedKeysSource.matchAll(pattern)].map(([, method, path, scope]) => [
    `${method.toLowerCase()} ${path.trim()}`,
    scopeName(scope),
  ])
}

/** ScopeAccountsRead → accounts:read, ScopeProxyConnect → proxy:connect. */
function scopeName(scope: string): string {
  const parts = scope.replace(/^Scope/, '').match(/[A-Z][a-z0-9]*/g) ?? []
  return parts.map((part) => part.toLowerCase()).join(':')
}

function parameterNames(operation: Operation): string[] {
  return (operation.parameters ?? []).map((parameter) => {
    const resolved = parameter.$ref
      ? (resolvePointer(doc, parameter.$ref) as Parameter)
      : parameter
    return `${resolved.in}:${resolved.name}`
  })
}

function findParameter(operation: Operation, name: string): Parameter | undefined {
  return (operation.parameters ?? [])
    .map((parameter) =>
      parameter.$ref ? (resolvePointer(doc, parameter.$ref) as Parameter) : parameter,
    )
    .find((parameter) => parameter.name === name)
}

function schema(name: string): JsonSchema {
  const value = doc.components.schemas[name]
  if (!value) throw new Error(`missing schema ${name}`)
  return value
}

function responseSchema(operation: Operation, status: string): JsonSchema {
  const value = operation.responses[status]?.content?.['application/json']?.schema
  if (!value) throw new Error(`missing ${status} application/json schema`)
  return value
}

const entries = operations()
const routes = serverRoutes()
const operationIds = entries.map(({ operation }) => operation.operationId)

describe('docs/openapi.json', () => {
  it('parses as JSON and declares OpenAPI 3.1', () => {
    expect(doc.openapi).toMatch(/^3\.1\.\d+$/)
    expect(doc.info.title).toBeTruthy()
    expect(doc.info.version).toBeTruthy()
    expect(doc.servers.map((server) => server.url)).toEqual([
      'https://ajs.dwgx.top:2055',
      'http://127.0.0.1:2261',
    ])
    expect(doc.servers[0]?.description).toContain('Live public scoped-key API')
    expect(doc.info.description).toContain('gate.dwgx.top')
    expect(doc.info.description).not.toContain('placeholder')
  })

  it('documents every route registered in server.go and orders.go and invents no P0 route', () => {
    expect(routes.length).toBeGreaterThan(50)
    const documented = new Set(entries.map(({ method, path }) => `${method} ${path}`))
    expect(routes.filter((route) => !documented.has(route))).toEqual([])

    const p0 = entries.filter(({ operation }) => operation['x-implementation-phase'] === 'P0')
    expect(p0.length).toBeGreaterThan(40)
    const invented = p0
      .map(({ method, path }) => `${method} ${path}`)
      .filter((route) => !routes.includes(route))
    expect(invented).toEqual([])
  })

  it('keeps the expected path and operation counts', () => {
    expect(Object.keys(doc.paths)).toHaveLength(120)
    expect(entries).toHaveLength(148)
  })

  it('documents the real-time debugger trace and stream', () => {
    const trace = doc.paths['/v1/debug/trace']?.get
    expect(trace).toBeDefined()
    if (!trace) return
    expect(trace['x-implementation-phase']).toBe('P0')
    expect(trace.tags).toContain('Debug')
    expect(parameterNames(trace)).toEqual(expect.arrayContaining(['query:account', 'query:limit']))
    expect(responseSchema(trace, '200')).toEqual({
      $ref: '#/components/schemas/DebugTracePage',
    })
    expect(trace.responses['503']).toEqual({
      $ref: '#/components/responses/ServiceUnavailable',
    })

    const stream = doc.paths['/v1/debug/stream']?.get
    expect(stream).toBeDefined()
    if (!stream) return
    expect(stream['x-implementation-phase']).toBe('P0')
    expect(parameterNames(stream)).toEqual(
      expect.arrayContaining(['query:account', 'query:since', 'header:Last-Event-ID']),
    )
    expect(stream.responses['200']?.content?.['text/event-stream']).toBeDefined()
    expect(stream.responses['200']?.headers?.['X-Debug-Restarted']).toBeDefined()

    const traceSchema = schema('DebugTrace')
    expect(traceSchema.required).toEqual(['seq', 'at', 'kind'])
    expect(traceSchema.properties?.kind?.enum).toEqual(
      expect.arrayContaining([
        'switch.step',
        'switch',
        'gate.open',
        'gate.close',
        'session.open',
        'session.lost',
        'lease.create',
        'lease.revoke',
        'slot.state',
      ]),
    )
    expect(schema('DebugTracePage').required).toEqual(
      expect.arrayContaining(['enabled', 'capacity', 'retained', 'evicted', 'nextSeq', 'entries']),
    )
  })

  it('documents idempotent lease creation and confirmed revocation', () => {
    const create = doc.paths['/v1/leases']?.post
    expect(create).toBeDefined()
    if (!create) return
    expect(create['x-implementation-phase']).toBe('P0')
    expect(create.parameters?.map((parameter) => parameter.$ref)).toContain(
      '#/components/parameters/IdempotencyKey',
    )
    expect(responseSchema(create, '201')).toEqual({
      $ref: '#/components/schemas/CreateLeaseResult',
    })
    expect(create.responses['409']).toEqual({ $ref: '#/components/responses/Conflict' })
    expect(create.responses['429']).toEqual({ $ref: '#/components/responses/ScopedKeyRateLimited' })

    const input = schema('CreateLeaseInput')
    expect(input.required).toEqual(expect.arrayContaining(['userId', 'platform']))
    expect(input.properties?.platform).toEqual({ $ref: '#/components/schemas/Platform' })
    // 0 is the sticky-TTL sentinel; 1-59 is refused, so the branch starts at 60.
    const ttl = input.properties?.ttlSeconds
    expect(String(ttl?.description)).toContain('sticky TTL')
    expect(ttl?.oneOf).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ type: 'integer', const: 0 }),
        expect.objectContaining({ type: 'integer', minimum: 60, maximum: 604800 }),
      ]),
    )

    const revoke = doc.paths['/v1/leases/{id}']?.delete
    expect(revoke).toBeDefined()
    if (!revoke) return
    expect(revoke.requestBody).toBeDefined()
    expect(revoke.responses['204']?.headers?.Warning).toBeDefined()
    expect(revoke.responses['400']).toEqual({ $ref: '#/components/responses/BadRequest' })
    expect(schema('RevokeLeaseInput').required).toEqual(['confirm'])
    expect(schema('LeaseStatus').enum).toEqual(['active', 'expired', 'revoked'])
  })

  it('resolves every $ref', () => {
    const refs: Array<{ at: string; ref: string }> = []
    collectRefs(doc, '#', refs)
    expect(refs.length).toBeGreaterThan(200)
    const unresolved = refs.filter(({ ref }) => {
      try {
        resolvePointer(doc, ref)
        return false
      } catch {
        return true
      }
    })
    expect(unresolved).toEqual([])
  })

  it('has unique operationIds and complete operation metadata', () => {
    expect(new Set(operationIds).size).toBe(operationIds.length)
    for (const { method, path, operation } of entries) {
      const where = `${method.toUpperCase()} ${path}`
      expect(operation.operationId, where).toBeTruthy()
      expect(operation.summary, where).toBeTruthy()
      expect(operation.tags?.length, where).toBeGreaterThan(0)
      expect(['P0', 'P1'], where).toContain(operation['x-implementation-phase'])
      expect(Object.keys(operation.responses).length, where).toBeGreaterThan(0)
    }
  })

  it('declares every path parameter used by a template', () => {
    for (const { method, path, operation } of entries) {
      const names = parameterNames(operation)
      const unique = new Set(names)
      expect(unique.size, `${method} ${path} has duplicate parameters`).toBe(names.length)
      for (const match of path.matchAll(/\{([^}]+)\}/g)) {
        expect(unique, `${method} ${path} is missing {${match[1]}}`).toContain(`path:${match[1]}`)
      }
    }
  })

  it('requires bearer auth everywhere except /healthz and the portal cookie chain', () => {
    expect(doc.security).toEqual([{ bearerAuth: [] }])
    expect(doc.components.securitySchemes.bearerAuth).toMatchObject({ type: 'http', scheme: 'bearer' })
    const health = doc.paths['/healthz']?.get
    expect(health).toBeDefined()
    expect(health?.security).toEqual([])
    expect(doc.paths['/readyz']?.get.security).toEqual([])
    // The portal is its own authentication chain: it authenticates the opaque
    // cookie, never the admin bearer, and overrides the global requirement.
    const portalPaths: Record<string, true> = {
      '/v1/portal/signup': true,
      '/v1/portal/login': true,
      '/v1/portal/invites/{code}': true,
      '/v1/portal/invites/{code}/activate': true,
      '/v1/portal/session': true,
      '/v1/portal/logout': true,
      '/v1/portal/leases': true,
      '/v1/portal/usage': true,
      '/v1/portal/audit': true,
      '/v1/portal/keys': true,
      '/v1/portal/keys/{id}': true,
      '/v1/portal/plans': true,
      '/v1/portal/orders': true,
      '/v1/portal/orders/{id}': true,
      '/v1/portal/orders/{id}/checkout': true,
      '/v1/portal/orders/{id}/cancel': true,
      '/v1/portal/orders/{id}/reissue': true,
      // The payment webhooks authenticate their provider signature over the
      // raw body; a provider cannot send a bearer token.
      '/v1/payments/stripe': true,
      '/v1/payments/epay/notify': true,
    }
    for (const { method, path, operation } of entries) {
      if (path === '/healthz' || path === '/readyz' || portalPaths[path]) continue
      expect(operation.security, `${method} ${path}`).toBeUndefined()
    }
  })

  it('documents the tenant portal cookie chain and admin tenant lifecycle', () => {
    expect(doc.components.securitySchemes.portalCookie).toMatchObject({
      type: 'apiKey',
      in: 'cookie',
      name: '__Host-ajs_portal',
    })

    // Signup and login are the public protocol: no cookie yet.
    expect(doc.paths['/v1/portal/signup']?.post.security).toEqual([])
    expect(doc.paths['/v1/portal/login']?.post.security).toEqual([])
    // Every authenticated portal route names the cookie scheme explicitly.
    for (const [method, path] of [
      ['get', '/v1/portal/session'],
      ['post', '/v1/portal/logout'],
      ['get', '/v1/portal/leases'],
      ['get', '/v1/portal/usage'],
      ['get', '/v1/portal/audit'],
      ['get', '/v1/portal/keys'],
      ['post', '/v1/portal/keys'],
      ['delete', '/v1/portal/keys/{id}'],
    ] as Array<[string, string]>) {
      expect(doc.paths[path]?.[method]?.security, `${method} ${path}`).toEqual([
        { portalCookie: [] },
      ])
    }

    // The customer audit read is cursor-paged, cookie-authenticated and
    // returns the shared AuditEvent row shape without a meta block.
    const audit = doc.paths['/v1/portal/audit']?.get
    expect(audit).toBeDefined()
    expect(audit?.['x-implementation-phase']).toBe('P0')
    expect(parameterNames(audit!)).toEqual(
      expect.arrayContaining(['query:limit', 'query:before']),
    )
    expect(responseSchema(audit!, '200')).toMatchObject({
      type: 'array',
      items: { $ref: '#/components/schemas/AuditEvent' },
    })

    // Unsafe portal methods require the per-session header.
    for (const [method, path] of [
      ['post', '/v1/portal/logout'],
      ['post', '/v1/portal/keys'],
      ['delete', '/v1/portal/keys/{id}'],
    ] as Array<[string, string]>) {
      expect(parameterNames(doc.paths[path]![method]!), `${method} ${path}`).toContain(
        'header:X-CSRF-Token',
      )
    }

    // The tenant lifecycle is admin-only, so it inherits the global bearer.
    for (const [method, path] of [
      ['get', '/v1/admin/tenants'],
      ['post', '/v1/admin/tenants/{id}/approve'],
      ['post', '/v1/admin/tenants/{id}/suspend'],
      ['post', '/v1/admin/tenants/{id}/resume'],
    ] as Array<[string, string]>) {
      const operation = doc.paths[path]?.[method]
      expect(operation, `${method} ${path}`).toBeDefined()
      expect(operation?.security, `${method} ${path}`).toBeUndefined()
      expect(operation?.['x-implementation-phase']).toBe('P0')
    }

    for (const name of [
      'Tenant',
      'TenantSignupInput',
      'TenantSignupAccepted',
      'TenantLoginInput',
      'TenantSessionView',
      'TenantKeyCreateRequest',
      'TenantApproveRequest',
    ]) {
      expect(schema(name).properties, name).toBeTruthy()
    }
    expect(schema('TenantStatus').enum).toEqual(['pending', 'active', 'suspended'])
    expect(schema('Tenant').required).toEqual(expect.arrayContaining([
      'id', 'email', 'displayName', 'status', 'userId', 'createdAt',
    ]))
    expect(schema('TenantSignupInput').properties?.password).toMatchObject({
      writeOnly: true,
      minLength: 12,
    })
    expect(schema('TenantSessionView').properties?.csrfToken).toMatchObject({ type: 'string' })
    expect(schema('TenantSignupAccepted').properties?.status?.enum).toEqual(['pending'])
  })

  it('documents the order and activation contract', () => {
    // Customer order routes use the portal cookie, never a bearer token.
    for (const [method, path] of [
      ['get', '/v1/portal/plans'],
      ['get', '/v1/portal/orders'],
      ['post', '/v1/portal/orders'],
      ['get', '/v1/portal/orders/{id}'],
      ['post', '/v1/portal/orders/{id}/checkout'],
      ['post', '/v1/portal/orders/{id}/cancel'],
    ] as Array<[string, string]>) {
      const operation = doc.paths[path]?.[method]
      expect(operation, `${method} ${path}`).toBeDefined()
      expect(operation?.security, `${method} ${path}`).toEqual([{ portalCookie: [] }])
      expect(operation?.['x-implementation-phase'], `${method} ${path}`).toBe('P0')
    }
    for (const [method, path] of [
      ['post', '/v1/portal/orders'],
      ['post', '/v1/portal/orders/{id}/checkout'],
      ['post', '/v1/portal/orders/{id}/cancel'],
    ] as Array<[string, string]>) {
      expect(parameterNames(doc.paths[path]![method]!), `${method} ${path}`).toContain(
        'header:X-CSRF-Token',
      )
    }
    expect(parameterNames(doc.paths['/v1/portal/orders']!.post!)).toContain(
      'header:Idempotency-Key',
    )
    expect(responseSchema(doc.paths['/v1/portal/orders']!.post!, '201')).toEqual({
      $ref: '#/components/schemas/Order',
    })

    // Admin order/plan routes inherit the global bearer requirement and are
    // absent from routeScopes, so only an admin principal reaches them.
    for (const [method, path] of [
      ['get', '/v1/admin/plans'],
      ['post', '/v1/admin/plans'],
      ['get', '/v1/admin/plans/{id}'],
      ['patch', '/v1/admin/plans/{id}'],
      ['delete', '/v1/admin/plans/{id}'],
      ['get', '/v1/admin/orders'],
      ['get', '/v1/admin/orders/{id}'],
      ['post', '/v1/admin/orders/{id}/settle'],
      ['post', '/v1/admin/orders/{id}/refund'],
      ['post', '/v1/admin/orders/{id}/chargeback'],
      ['post', '/v1/admin/orders/{id}/cancel'],
    ] as Array<[string, string]>) {
      const operation = doc.paths[path]?.[method]
      expect(operation, `${method} ${path}`).toBeDefined()
      expect(operation?.security, `${method} ${path}`).toBeUndefined()
      expect(operation?.['x-implementation-phase'], `${method} ${path}`).toBe('P0')
    }
    const listOrders = doc.paths['/v1/admin/orders']!.get!
    for (const name of ['tenantId', 'userId', 'state']) {
      expect(parameterNames(listOrders), name).toContain(`query:${name}`)
    }
    expect(doc.paths['/v1/admin/plans/{id}']!.delete!.responses['204']).toBeDefined()

    // The webhook is signature-only; it overrides the global bearer rule and
    // never falls back to manual settlement while disabled.
    const webhook = doc.paths['/v1/payments/stripe']?.post
    expect(webhook).toBeDefined()
    expect(webhook?.security).toEqual([])
    expect(parameterNames(webhook!)).toContain('header:Stripe-Signature')
    expect(webhook?.responses['503']).toEqual({
      $ref: '#/components/responses/ServiceUnavailable',
    })

    const epay = doc.paths['/v1/payments/epay/notify']?.post
    expect(epay).toBeDefined()
    expect(epay?.security).toEqual([])
    expect(epay?.['x-implementation-phase']).toBe('P0')
    expect(epay?.responses['503']).toEqual({
      $ref: '#/components/responses/ServiceUnavailable',
    })

    // Standard 易支付 notify is a GET with query parameters; the same
    // signature/verification contract applies as the POST form variant.
    const epayGet = doc.paths['/v1/payments/epay/notify']?.get
    expect(epayGet).toBeDefined()
    expect(epayGet?.security).toEqual([])
    expect(epayGet?.['x-implementation-phase']).toBe('P0')
    expect(parameterNames(epayGet!)).toEqual(
      expect.arrayContaining([
        'query:pid',
        'query:trade_no',
        'query:out_trade_no',
        'query:money',
        'query:trade_status',
        'query:sign',
      ]),
    )
    expect(schema('OrderCheckoutRequest').properties?.type).toMatchObject({
      enum: ['alipay', 'wxpay'],
    })
    expect(schema('OrderCheckoutResponse').required).toEqual(['payUrl'])

    // The state machine and the honest settlement inputs are part of the
    // contract: a reference alone is not an attestation.
    expect(schema('OrderState').enum).toEqual([
      'awaiting_payment',
      'paid',
      'active',
      'cancelled',
      'refunded',
      'charged_back',
    ])
    expect(schema('Order').required).toEqual(expect.arrayContaining([
      'id', 'tenantId', 'userId', 'planId', 'plan', 'provider', 'state',
      'providerRef', 'reviewRequired', 'grant',
    ]))
    expect(schema('Order').properties?.grant?.oneOf).toBeDefined()
    expect(schema('OrderSettleRequest').required).toEqual([
      'reference',
      'offlineFundsReceived',
    ])
    expect(schema('OrderSettleRequest').properties?.offlineFundsReceived).toMatchObject({
      const: true,
    })
    expect(schema('Plan').properties?.amountMinor).toMatchObject({ minimum: 1 })
    expect(schema('Plan').properties?.leaseTtlSeconds).toMatchObject({
      minimum: 60,
      maximum: 604800,
    })
    expect(schema('EntitlementGrant').required).toEqual(expect.arrayContaining([
      'orderId', 'leaseId', 'quotaBytes', 'expiresAt', 'revokedAt',
    ]))
    expect(schema('WebhookEvent').enum).toEqual(expect.arrayContaining([
      'order.created',
      'order.paid',
      'order.activated',
      'order.refunded',
    ]))
  })

  it('documents the order fulfilment job and the reissue actions', () => {
    // The durable job is the contract the admin and portal surfaces read. Its
    // state is closed at three values: cleanup-pending is a modifier on a
    // publishing job, never a fourth state.
    const job = schema('FulfilmentJob')
    expect(job.properties?.state?.enum).toEqual(['queued', 'publishing', 'dead_letter'])
    expect(job.properties?.mode?.enum).toEqual(['initial', 'reissue'])
    expect(job.properties?.lastReason?.enum).toEqual([
      'capacity',
      'tenant_suspended',
      'target_changed',
      'publish_failed',
      'timeout',
      'internal',
      'interrupted',
      'entitlement_expired',
    ])
    expect(job.required).toEqual([
      'orderId',
      'mode',
      'state',
      'attempts',
      'entitlementExpiresAt',
      'updatedAt',
    ])
    expect(job.properties?.nextAttemptAt?.oneOf).toBeDefined()
    expect(job.properties?.blocked).toMatchObject({ type: 'boolean' })
    expect(job.properties?.cleanupPending).toMatchObject({ type: 'boolean' })
    // Attempt fencing identifiers are internal to the publisher; they must
    // never become part of the document a browser builds against.
    for (const name of [
      'attemptLeaseId',
      'publishedAt',
      'publishedLeaseId',
      'publishedAccountId',
    ]) {
      expect(job.properties?.[name], name).toBeUndefined()
    }
    expect(schema('Order').properties?.fulfilment?.oneOf).toEqual(
      expect.arrayContaining([{ $ref: '#/components/schemas/FulfilmentJob' }]),
    )

    // The reissue actions are registered in orders.go beside their handlers,
    // not in the central router table, so they drifted out of the document
    // once already. The route scan must see them and the document must match.
    expect(routes).toEqual(
      expect.arrayContaining([
        'post /v1/admin/orders/{id}/reissue',
        'post /v1/portal/orders/{id}/reissue',
      ]),
    )
    const admin = doc.paths['/v1/admin/orders/{id}/reissue']?.post
    expect(admin).toBeDefined()
    expect(admin?.['x-implementation-phase']).toBe('P0')
    expect(admin?.security).toBeUndefined()
    expect(parameterNames(admin!)).toEqual(
      expect.arrayContaining(['path:id', 'header:X-AJS-CSRF']),
    )
    expect(responseSchema(admin!, '200')).toEqual({ $ref: '#/components/schemas/Order' })
    expect(admin?.responses['409']).toEqual({ $ref: '#/components/responses/Conflict' })
    expect(admin?.responses['503']).toEqual({
      $ref: '#/components/responses/ServiceUnavailable',
    })

    const portal = doc.paths['/v1/portal/orders/{id}/reissue']?.post
    expect(portal).toBeDefined()
    expect(portal?.['x-implementation-phase']).toBe('P0')
    expect(portal?.security).toEqual([{ portalCookie: [] }])
    expect(parameterNames(portal!)).toEqual(
      expect.arrayContaining(['path:id', 'header:X-CSRF-Token']),
    )
    expect(responseSchema(portal!, '200')).toEqual({ $ref: '#/components/schemas/Order' })
    expect(portal?.responses['429']).toEqual({ $ref: '#/components/responses/RateLimited' })
  })

  it('marks the proposed P1 interfaces', () => {
    const expected: Array<[string, string]> = [
      ['get', '/v1/accounts/io/capabilities'],
      ['post', '/v1/accounts/export'],
      ['post', '/v1/accounts/import'],
      ['post', '/v1/accounts/bulk-delete'],
      ['get', '/v1/accounts/{id}/selection'],
      ['put', '/v1/accounts/{id}/selection'],
      ['delete', '/v1/accounts/{id}/selection'],
      ['post', '/v1/accounts/{id}/actions/connect'],
      ['post', '/v1/accounts/{id}/actions/disconnect'],
      ['post', '/v1/accounts/{id}/actions/reconnect'],
      ['post', '/v1/accounts/{id}/actions/select-node'],
      ['get', '/v1/operations'],
      ['get', '/v1/operations/{id}'],
      ['delete', '/v1/operations/{id}'],
      ['get', '/v1/schedules'],
      ['post', '/v1/schedules'],
      ['get', '/v1/schedules/{id}'],
      ['patch', '/v1/schedules/{id}'],
      ['delete', '/v1/schedules/{id}'],
      ['post', '/v1/schedules/preview'],
      ['get', '/v1/schedules/preview'],
      ['post', '/v1/schedules/{id}/actions/run'],
      ['get', '/v1/schedules/{id}/runs'],
    ]
    const byRoute = new Map(entries.map((entry) => [`${entry.method} ${entry.path}`, entry.operation]))
    for (const [method, path] of expected) {
      expect(byRoute.get(`${method} ${path}`)?.['x-implementation-phase'], `${method} ${path}`).toBe('P1')
    }
    // The paged catalog read is P0 even though selection/connect are P1.
    expect(byRoute.get('get /v1/accounts/{id}/servers')?.['x-implementation-phase']).toBe('P0')
    expect(entries.filter(({ operation }) => operation['x-implementation-phase'] === 'P1')).toHaveLength(23)
  })

  it('documents the schemas named by the contract', () => {
    const names = [
      'Account', 'CreateAccountInput', 'AccountPatch', 'ProxyNode', 'Lease', 'PanelUser',
      'ApiKey', 'CreateKeyResult', 'RequestLog', 'AuditEvent', 'Metrics', 'PanelSettings',
      'Topology', 'UpdateInfo', 'LabOverview', 'GateView', 'FlowView', 'SessionView',
      'EgressView', 'EgressCheckView', 'ProfileView', 'DeviceView', 'FlagsView', 'LogsView',
      'DiagnosticsView', 'ServersView', 'RegionCount', 'CatalogSelection', 'ConnectResult',
      'DisconnectResult', 'AccountIOCapabilities', 'AccountExport', 'AccountImportRequest',
      'AccountImportResult', 'BulkDeleteRequest', 'BulkDeleteResult', 'Schedule', 'ScopedKey',
      'RenewalPlan', 'RenewalPlansView', 'RenewalAccountView', 'UsageBucket', 'UsageAccount',
      'UsageUser', 'UsageTarget', 'UsageSummary', 'HealthSample', 'NodeHealth', 'HealthView',
      'EgressHealth', 'EgressSample',
      'WebhookEndpoint', 'WebhookCreate', 'WebhookCreated', 'WebhookPatch', 'Delivery',
      'ErrorResponse',
    ]
    for (const name of names) {
      expect(schema(name).properties, name).toBeTruthy()
    }
    expect(schema('Account').required).toEqual(expect.arrayContaining([
      'id', 'label', 'username', 'platform', 'state', 'concurrency', 'useCount', 'effectiveSlots',
      'regions', 'expiresAt',
    ]))
    expect(schema('Account').properties?.useCount).toMatchObject({
      type: 'integer', minimum: 1, maximum: 2000,
    })
    expect(schema('Account').properties?.effectiveSlots).toMatchObject({
      type: 'integer', minimum: 1, maximum: 2000,
    })
    expect(String(schema('CreateLeaseResult').properties?.warning?.description)).toContain(
      'multi_session_unimplemented',
    )
    expect(schema('ProxyNode').required).toEqual(expect.arrayContaining([
      'id', 'accountId', 'state', 'egressIp', 'protocol', 'gatePort',
    ]))
    expect(schema('Lease').required).toEqual(expect.arrayContaining([
      'id', 'userId', 'platform', 'nodeId', 'ttlSeconds', 'expiresAt', 'status', 'idempotencyKey',
    ]))
    expect(schema('PanelUser').properties?.quotaBytesMonth?.oneOf).toBeDefined()
    expect(schema('ApiKey').properties?.revokedAt?.oneOf).toBeDefined()
    expect(schema('CreateAccountInput').properties?.password?.writeOnly).toBe(true)
  })

  it('attributes flows to a protocol and, when known, a user', () => {
    const flow = schema('FlowView')
    // Both gate listeners report their protocol, so the dashboard can filter
    // on the same vocabulary the lease surfaces use.
    expect(flow.properties?.protocol).toEqual({ $ref: '#/components/schemas/ProxyProtocol' })
    expect(flow.properties?.userId).toMatchObject({ type: 'string' })
    expect(flow.properties?.userName).toMatchObject({ type: 'string' })
    // Attribution is optional on the wire: a loopback or anonymous flow omits
    // it instead of sending an empty string.
    const required = flow.required ?? []
    for (const name of ['protocol', 'userId', 'userName']) {
      expect(required, name).not.toContain(name)
    }
    expect(schema('ProxyProtocol').enum).toEqual(['socks5', 'http'])
  })

  it('keeps the node state vocabulary in sync with the daemon', () => {
    // Startup recovery demotes a persisted node whose gate did not survive
    // the last process; the console needs STALE in the same enum the daemon
    // writes, or a stale row renders without a badge.
    expect(schema('NodeState').enum).toEqual([
      'LIVE',
      'READY',
      'STARTING',
      'DRAINING',
      'DOWN',
      'STALE',
    ])
  })

  it('documents catalog pagination query and response', () => {
    const operation = doc.paths['/v1/accounts/{id}/servers']?.get
    expect(operation).toBeDefined()
    if (!operation) return
    const names = parameterNames(operation)
    for (const name of [
      'q', 'region', 'status', 'province', 'city', 'selected', 'offset', 'limit', 'sort',
    ]) {
      expect(names, name).toContain(`query:${name}`)
    }
    expect(findParameter(operation, 'limit')?.schema)
      .toMatchObject({ type: 'integer', minimum: 1, maximum: 500, default: 50 })
    expect(findParameter(operation, 'sort')?.schema?.enum)
      .toEqual(['order', 'title', 'host', 'status', 'region'])
    // 00 is the unresolved province bucket the handler documents.
    expect(String(findParameter(operation, 'province')?.description)).toContain('00')

    expect(responseSchema(operation, '200')).toEqual({ $ref: '#/components/schemas/ServersView' })
    const view = schema('ServersView')
    expect(view.required).toEqual(expect.arrayContaining([
      'total', 'offset', 'limit', 'nextOffset', 'hasMore', 'regions', 'geo', 'servers',
    ]))
    // snapshotId is the value the guard compares; it is omitted when no catalog
    // connector is configured, so it must stay optional.
    expect(view.properties?.snapshotId).toMatchObject({ type: 'string' })
    expect(view.required).not.toContain('snapshotId')
    expect(view.properties?.servers?.items).toEqual({ $ref: '#/components/schemas/ServerView' })
    expect(view.properties?.regions?.items).toEqual({ $ref: '#/components/schemas/RegionCount' })
    expect(view.properties?.geo).toEqual({ $ref: '#/components/schemas/CatalogGeo' })

    const geo = schema('CatalogGeo')
    expect(geo.required).toEqual(['total', 'unknown', 'provinces', 'current', 'live'])
    expect(geo.properties?.provinces?.items).toEqual({
      $ref: '#/components/schemas/CatalogGeoProvince',
    })
    expect(geo.properties?.current?.oneOf).toEqual(
      expect.arrayContaining([
        { $ref: '#/components/schemas/CatalogGeoRow' },
        { type: 'null' },
      ]),
    )
    expect(schema('CatalogGeoProvince').required).toEqual(['code', 'count', 'connected', 'cities'])
    expect(schema('CatalogGeoCity').required).toEqual(['code', 'label', 'count'])
    expect(schema('CatalogGeoRow').required).toEqual(['id', 'title', 'nodeState'])
  })

  it('documents the operation result and error as nullable', () => {
    const operation = schema('Operation')
    for (const name of ['result', 'error']) {
      // Both keys are always serialized; only their value may be null.
      expect(operation.required, name).toContain(name)
      expect(operation.properties?.[name]?.oneOf, name).toEqual(
        expect.arrayContaining([{ type: 'null' }]),
      )
    }
    expect(operation.properties?.result?.oneOf).toEqual(
      expect.arrayContaining([{ $ref: '#/components/schemas/SelectNodeResult' }]),
    )
    expect(operation.properties?.error?.oneOf).toEqual(
      expect.arrayContaining([{ $ref: '#/components/schemas/OperationError' }]),
    )
  })

  it('mirrors routeScopes as x-required-scope with a scoped 403, 429 and rate headers', () => {
    const scoped = scopedRouteScopes()
    expect(scoped.length).toBeGreaterThan(40)
    const byRoute = new Map(
      entries.map((entry) => [`${entry.method} ${entry.path}`, entry.operation]),
    )
    const scopeEnum = schema('Scope').enum ?? []
    for (const [route, scope] of scoped) {
      expect(scopeEnum, route).toContain(scope)
      const operation = byRoute.get(route)
      expect(operation, route).toBeDefined()
      if (!operation) continue
      expect(operation['x-required-scope'], route).toBe(scope)
      expect(operation.responses['403'], route).toEqual({
        $ref: '#/components/responses/ScopedKeyForbidden',
      })
      expect(operation.responses['429'], route).toEqual({
        $ref: '#/components/responses/ScopedKeyRateLimited',
      })
      for (const [status, response] of Object.entries(operation.responses)) {
        if (!status.startsWith('2')) continue
        for (const header of [
          'X-RateLimit-Limit', 'X-RateLimit-Remaining', 'X-RateLimit-Reset',
        ]) {
          expect(response.headers?.[header], `${route} ${status} ${header}`).toEqual({
            $ref: `#/components/headers/${header.slice(2).replace(/-/g, '')}`,
          })
        }
      }
    }
    // An invented grant would tell a generator it may reach an operator route.
    const scopedRoutes = new Set(scoped.map(([route]) => route))
    for (const { method, path, operation } of entries) {
      if (!operation['x-required-scope']) continue
      expect(scopedRoutes.has(`${method} ${path}`), `${method} ${path}`).toBe(true)
    }
    expect(doc.components.responses.ScopedKeyRateLimited?.headers?.['Retry-After']).toEqual({
      $ref: '#/components/headers/RetryAfter',
    })
  })

  it('carries a worked example for every customer journey call', () => {
    const cases: Array<[string, string, string]> = [
      ['post', '/v1/scoped-keys', 'request'],
      ['post', '/v1/scoped-keys', '201'],
      ['get', '/v1/nodes', '200'],
      ['post', '/v1/leases', 'request'],
      ['post', '/v1/leases', '201'],
      ['get', '/v1/leases/{id}', '200'],
      ['post', '/v1/accounts/{id}/actions/connect', 'request'],
      ['post', '/v1/accounts/{id}/actions/connect', '200'],
      ['post', '/v1/accounts/{id}/actions/select-node', '202'],
      ['get', '/v1/usage', '200'],
    ]
    for (const [method, path, where] of cases) {
      const operation = doc.paths[path]?.[method]
      expect(operation, `${method} ${path}`).toBeDefined()
      if (!operation) continue
      const media = where === 'request'
        ? operation.requestBody?.content?.['application/json']
        : operation.responses[where]?.content?.['application/json']
      const examples = media?.examples ?? {}
      expect(Object.keys(examples).length, `${method} ${path} ${where}`).toBeGreaterThan(0)
      for (const [name, example] of Object.entries(examples)) {
        expect(example.summary, `${method} ${path} ${where} ${name}`).toBeTruthy()
        expect(example.value, `${method} ${path} ${where} ${name}`).toBeDefined()
      }
    }

    // The queued 202 is the nullability contract in one object.
    const queued = doc.paths['/v1/accounts/{id}/actions/select-node']?.post
      ?.responses['202']?.content?.['application/json']?.examples
    const value = Object.values(queued ?? {})[0]?.value as Record<string, unknown> | undefined
    expect(value?.result).toBeNull()
    expect(value?.error).toBeNull()

    // Both scoped-key denial reasons are worked examples on the shared response.
    const forbidden = doc.components.responses.ScopedKeyForbidden
      ?.content?.['application/json']?.examples ?? {}
    expect(Object.keys(forbidden).sort()).toEqual(['account-restricted', 'scope-required'])
  })

  it('documents the platform surfaces as P0 with one-time webhook secrets', () => {
    const expected: Array<[string, string]> = [
      ['get', '/v1/renewal/plans'],
      ['get', '/v1/renewal/accounts/{id}'],
      ['get', '/v1/usage'],
      ['get', '/v1/health/nodes'],
      ['get', '/v1/webhooks'],
      ['post', '/v1/webhooks'],
      ['patch', '/v1/webhooks/{id}'],
      ['delete', '/v1/webhooks/{id}'],
      ['get', '/v1/webhooks/{id}/deliveries'],
    ]
    for (const [method, path] of expected) {
      const operation = doc.paths[path]?.[method]
      expect(operation, `${method} ${path}`).toBeDefined()
      expect(operation?.['x-implementation-phase'], `${method} ${path}`).toBe('P0')
    }

    const usage = doc.paths['/v1/usage']?.get
    expect(usage).toBeDefined()
    if (usage) {
      expect(parameterNames(usage)).toEqual(
        expect.arrayContaining(['query:from', 'query:to', 'query:account', 'query:userId']),
      )
      expect(responseSchema(usage, '200')).toEqual({ $ref: '#/components/schemas/UsageSummary' })
    }
    const summary = schema('UsageSummary')
    expect(summary.properties?.quotaEnforced).toMatchObject({ type: 'boolean' })
    expect(summary.required).toContain('quotaEnforced')

    const health = doc.paths['/v1/health/nodes']?.get
    expect(health && responseSchema(health, '200')).toEqual({ $ref: '#/components/schemas/HealthView' })
    // Egress is optional per node: a daemon without the through-gate probe
    // omits the block, and one with it reports the switch when disabled.
    expect(schema('NodeHealth').properties?.egress).toEqual({
      $ref: '#/components/schemas/EgressHealth',
    })
    const egress = schema('EgressHealth')
    expect(egress.required).toEqual(
      expect.arrayContaining([
        'enabled',
        'count',
        'ok',
        'successRate',
        'p50Ms',
        'p95Ms',
        'consecutiveFailures',
      ]),
    )
    expect(egress.properties?.state?.enum).toEqual(['ok', 'degraded', 'down'])
    expect(egress.properties?.reason).toMatchObject({ type: 'string' })
    expect(schema('EgressSample').properties?.errorClass?.enum).toEqual([
      'timeout',
      'refused',
      'unreachable',
      'proxy',
      'http',
      'other',
    ])

    const create = doc.paths['/v1/webhooks']?.post
    expect(create).toBeDefined()
    if (create) {
      expect(responseSchema(create, '201')).toEqual({ $ref: '#/components/schemas/WebhookCreated' })
      expect(create.responses['400']).toEqual({ $ref: '#/components/responses/BadRequest' })
    }
    // The signing secret exists only in the create response.
    expect(schema('WebhookCreated').properties?.secret).toBeTruthy()
    expect(schema('WebhookEndpoint').properties?.secret).toBeUndefined()
    expect(schema('WebhookEndpoint').required).toEqual(
      expect.arrayContaining(['id', 'url', 'enabled', 'createdAt', 'secretAvailable']),
    )
    expect(schema('Delivery').properties?.status?.enum).toEqual([
      'success',
      'failed',
      'dead_letter',
    ])
  })

  it('documents newest-first delivery history with a bounded limit', () => {
    const operation = doc.paths['/v1/webhooks/{id}/deliveries']?.get
    expect(operation).toBeDefined()
    if (!operation) return

    expect(operation.summary).toMatch(/newest first/i)
    expect(operation.responses['200']?.description).toMatch(/newest first/i)
    expect(parameterNames(operation)).toEqual(
      expect.arrayContaining(['path:id', 'query:limit']),
    )
    expect(findParameter(operation, 'limit')?.schema).toMatchObject({
      type: 'integer',
      minimum: 1,
      maximum: 500,
      default: 200,
    })
    expect(operation.responses['400']).toEqual({ $ref: '#/components/responses/BadRequest' })
    expect(responseSchema(operation, '200')).toEqual({
      type: 'array',
      items: { $ref: '#/components/schemas/Delivery' },
    })
  })

  it('shares the error envelope across failure responses', () => {
    const envelope = schema('ErrorResponse')
    expect(envelope.required).toEqual(['error'])
    expect(envelope.properties?.error).toEqual({ $ref: '#/components/schemas/ErrorDetail' })
    const detail = schema('ErrorDetail')
    expect(detail.required).toEqual(['code', 'message'])
    expect(detail.properties?.code?.enum).toEqual(expect.arrayContaining([
      'INVALID_ARGUMENT', 'NOT_FOUND', 'CONFLICT', 'UPSTREAM_UNAVAILABLE',
      'QUOTA_EXCEEDED', 'RATE_LIMITED', 'UNAUTHORIZED', 'FORBIDDEN',
      'METHOD_NOT_ALLOWED', 'INTERNAL',
      // Emitted by the tenant-invite chain and the destructive schedule paths.
      'INVITE_EXPIRED', 'INVITE_REDEEMED', 'CONFIRMATION_REQUIRED',
      // Emitted by the event ring, the waited switch and the schedule patch.
      'CURSOR_EXPIRED', 'OPERATION_TIMEOUT', 'PRECONDITION_FAILED',
    ]))
    // A scoped-key denial is machine-readable: the dashboard branches on these
    // two reasons instead of parsing the message.
    const reason = String(detail.properties?.reason?.description)
    expect(reason).toContain('scope_required')
    expect(reason).toContain('scoped_key_restricted')

    // 412 is emitted by the conditional schedule patch (stale_revision) and by
    // a waited switch whose operation ended with a changed snapshot.
    for (const [method, path] of [
      ['patch', '/v1/schedules/{id}'],
      ['post', '/v1/accounts/{id}/actions/select-node'],
    ] as Array<[string, string]>) {
      expect(doc.paths[path]?.[method]?.responses['412'], `${method} ${path}`).toEqual({
        $ref: '#/components/responses/PreconditionFailed',
      })
    }
    expect(doc.paths['/v1/events']?.get?.responses['410']).toBeDefined()

    // Creating a key for an unknown user is a documented 404, not a 400.
    expect(doc.paths['/v1/scoped-keys']?.post?.responses['404']?.$ref).toBe(
      '#/components/responses/NotFound',
    )
  })
})
