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
  schema?: JsonSchema
}

interface ResponseObject {
  description?: string
  content?: Record<string, { schema?: JsonSchema }>
  headers?: Record<string, unknown>
}

interface Operation {
  operationId: string
  summary?: string
  tags?: string[]
  'x-implementation-phase'?: string
  security?: unknown[]
  responses: Record<string, ResponseObject>
  parameters?: Parameter[]
  requestBody?: unknown
}

interface OpenAPIDocument {
  openapi: string
  info: { title: string; version: string }
  servers: Array<{ url: string }>
  security: Array<Record<string, string[]>>
  paths: Record<string, Record<string, Operation>>
  components: {
    securitySchemes: Record<string, { type?: string; scheme?: string }>
    schemas: Record<string, JsonSchema>
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

/** The exact route table from server.go: "get /v1/accounts", ... */
function serverRoutes(): string[] {
  const pattern = /rt\.handle\(http\.Method(Get|Post|Put|Patch|Delete),\s*"([^"]+)"/g
  return [...serverSource.matchAll(pattern)].map(
    ([, method, path]) => `${method.toLowerCase()} ${path}`,
  )
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
    expect(String(doc.servers[0]?.url)).toContain('127.0.0.1:2261')
  })

  it('documents every route registered in server.go and invents no P0 route', () => {
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
    expect(Object.keys(doc.paths)).toHaveLength(79)
    expect(entries).toHaveLength(98)
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
    expect(create.responses['429']).toEqual({ $ref: '#/components/responses/RateLimited' })

    const input = schema('CreateLeaseInput')
    expect(input.required).toEqual(expect.arrayContaining(['userId', 'platform']))
    expect(input.properties?.platform).toEqual({ $ref: '#/components/schemas/Platform' })
    expect(input.properties?.ttlSeconds).toMatchObject({ minimum: 60, maximum: 604800 })

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

  it('requires bearer auth everywhere except /healthz', () => {
    expect(doc.security).toEqual([{ bearerAuth: [] }])
    expect(doc.components.securitySchemes.bearerAuth).toMatchObject({ type: 'http', scheme: 'bearer' })
    const health = doc.paths['/healthz']?.get
    expect(health).toBeDefined()
    expect(health?.security).toEqual([])
    for (const { method, path, operation } of entries) {
      if (path === '/healthz') continue
      expect(operation.security, `${method} ${path}`).toBeUndefined()
    }
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
    expect(entries.filter(({ operation }) => operation['x-implementation-phase'] === 'P1')).toHaveLength(19)
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
      'WebhookEndpoint', 'WebhookCreate', 'WebhookCreated', 'WebhookPatch', 'Delivery',
      'ErrorResponse',
    ]
    for (const name of names) {
      expect(schema(name).properties, name).toBeTruthy()
    }
    expect(schema('Account').required).toEqual(expect.arrayContaining([
      'id', 'label', 'username', 'platform', 'state', 'concurrency', 'regions', 'expiresAt',
    ]))
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
    for (const name of ['q', 'region', 'status', 'selected', 'offset', 'limit', 'sort']) {
      expect(names, name).toContain(`query:${name}`)
    }
    expect(findParameter(operation, 'limit')?.schema)
      .toMatchObject({ type: 'integer', minimum: 1, maximum: 500, default: 50 })
    expect(findParameter(operation, 'sort')?.schema?.enum)
      .toEqual(['order', 'title', 'host', 'status', 'region'])

    expect(responseSchema(operation, '200')).toEqual({ $ref: '#/components/schemas/ServersView' })
    const view = schema('ServersView')
    expect(view.required).toEqual(expect.arrayContaining([
      'total', 'offset', 'limit', 'nextOffset', 'hasMore', 'regions', 'servers',
    ]))
    expect(view.properties?.servers?.items).toEqual({ $ref: '#/components/schemas/ServerView' })
    expect(view.properties?.regions?.items).toEqual({ $ref: '#/components/schemas/RegionCount' })
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
      'QUOTA_EXCEEDED', 'RATE_LIMITED', 'UNAUTHORIZED', 'METHOD_NOT_ALLOWED', 'INTERNAL',
    ]))
  })
})
