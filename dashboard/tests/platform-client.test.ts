import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ApiError } from '../src/lib/errors'
import type { WebhookEvent } from '../src/lib/types'

/**
 * Client-contract tests for the four platform surfaces: renewal, usage,
 * health and webhooks. Live cases stub fetch and assert the exact URL, query,
 * method and bearer header; mock cases assert deterministic offline data and
 * the one-time webhook secret.
 */

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/** `lib/api.ts` bakes VITE_USE_MOCK at module init, so import after stubbing. */
async function liveApi() {
  vi.stubEnv('VITE_USE_MOCK', 'false')
  return import('../src/lib/api')
}

async function mockApi() {
  vi.stubEnv('VITE_USE_MOCK', 'true')
  const { api } = await import('../src/lib/api')
  return api
}

async function expectApiError(
  promise: Promise<unknown>,
  status: number,
  code: string,
): Promise<void> {
  const error = await promise.then(
    () => null,
    (cause: unknown) => cause,
  )
  // The class identity is per module registry (the suite resets modules to
  // re-import lib/api.ts under a different VITE_USE_MOCK), so assert the
  // ApiError shape instead of instanceof.
  expect(error).toBeInstanceOf(Error)
  expect((error as ApiError).name).toBe('ApiError')
  expect((error as ApiError).status).toBe(status)
  expect((error as ApiError).code).toBe(code)
}

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.resetModules()
  sessionStorage.clear()
})

describe('platform client — live requests', () => {
  it('calls every platform path with the documented shape and bearer auth', async () => {
    const plans = {
      plans: [{ id: 'M1', label: '1 month', months: 1, priceCny: 20 }],
      fetchedAt: '2026-10-24T00:00:00Z',
      source: 'static',
      stale: true,
    }
    const account = {
      accountId: 'acc-01',
      expiresAt: '2026-11-11T00:00:00Z',
      daysLeft: 18,
      urgency: 'none',
      renewalUrl: 'https://www.91ajs.com/purchase',
    }
    const usage = {
      from: '2026-10-01T00:00:00Z',
      to: '2026-10-24T23:59:59Z',
      requests: 1,
      bytes: 1,
      errors: 0,
      accounts: [],
      users: [],
      days: [],
      topTargets: [],
      truncated: false,
    }
    const health = { enabled: true, nodes: [] }
    const endpoint = {
      id: 'wh-001',
      url: 'https://hooks.example.com/ajs',
      enabled: true,
      createdAt: '2026-10-01T00:00:00Z',
      secretAvailable: true,
    }
    const created = { ...endpoint, id: 'wh-002', secret: 'a'.repeat(64) }
    const delivery = {
      id: 'dlv-001',
      endpointId: 'wh-001',
      event: 'node.connected',
      attempt: 1,
      status: 'success',
      httpStatus: 202,
      at: '2026-10-24T00:00:00Z',
      durationMs: 42,
    }

    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost')
      const method = init?.method ?? 'GET'
      if (url.pathname === '/v1/renewal/plans') return jsonResponse(plans)
      if (url.pathname === '/v1/renewal/accounts/acc-01') return jsonResponse(account)
      if (url.pathname === '/v1/usage') return jsonResponse(usage)
      if (url.pathname === '/v1/health/nodes') return jsonResponse(health)
      if (url.pathname === '/v1/webhooks' && method === 'GET') return jsonResponse([endpoint])
      if (url.pathname === '/v1/webhooks' && method === 'POST') return jsonResponse(created, 201)
      if (url.pathname === '/v1/webhooks/wh-001' && method === 'PATCH') return jsonResponse(endpoint)
      if (url.pathname === '/v1/webhooks/wh-001' && method === 'DELETE') {
        return new Response(null, { status: 204 })
      }
      if (url.pathname === '/v1/webhooks/wh-001/deliveries') return jsonResponse([delivery])
      throw new Error(`unexpected ${method} ${url.pathname}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { api, setPanelToken } = await liveApi()
    setPanelToken('tok-platform')

    await expect(api.getRenewalPlans()).resolves.toEqual(plans)
    await expect(api.getRenewalAccount('acc-01')).resolves.toEqual(account)
    await api.getUsage({
      from: '2026-10-01',
      to: '2026-10-24',
      account: 'Northwind Ops',
      userId: 'u-01',
    })
    await expect(api.getNodeHealth()).resolves.toEqual(health)
    await expect(api.listWebhooks()).resolves.toEqual([endpoint])
    await expect(
      api.createWebhook({
        url: 'https://hooks.example.com/new',
        events: ['node.connected'],
        enabled: false,
      }),
    ).resolves.toEqual(created)
    await expect(api.updateWebhook('wh-001', { enabled: false })).resolves.toEqual(endpoint)
    await expect(api.deleteWebhook('wh-001')).resolves.toBeUndefined()
    await expect(api.listWebhookDeliveries('wh-001')).resolves.toEqual([delivery])

    const calls = fetchMock.mock.calls.map(([input, init]) => ({
      url: new URL(String(input), 'http://localhost'),
      method: init?.method ?? 'GET',
      headers: new Headers(init?.headers),
      body: init?.body,
    }))
    expect(calls.map((call) => `${call.method} ${call.url.pathname}`)).toEqual([
      'GET /v1/renewal/plans',
      'GET /v1/renewal/accounts/acc-01',
      'GET /v1/usage',
      'GET /v1/health/nodes',
      'GET /v1/webhooks',
      'POST /v1/webhooks',
      'PATCH /v1/webhooks/wh-001',
      'DELETE /v1/webhooks/wh-001',
      'GET /v1/webhooks/wh-001/deliveries',
    ])
    for (const call of calls) {
      expect(call.headers.get('Authorization')).toBe('Bearer tok-platform')
      expect(call.headers.get('Accept')).toBe('application/json')
    }

    const usageCall = calls[2]
    expect([...usageCall.url.searchParams.keys()]).toEqual(['from', 'to', 'account', 'userId'])
    expect(usageCall.url.searchParams.get('from')).toBe('2026-10-01')
    expect(usageCall.url.searchParams.get('to')).toBe('2026-10-24')
    expect(usageCall.url.searchParams.get('account')).toBe('Northwind Ops')
    expect(usageCall.url.searchParams.get('userId')).toBe('u-01')
    expect(usageCall.url.search).not.toContain('undefined')

    expect(JSON.parse(String(calls[5].body))).toEqual({
      url: 'https://hooks.example.com/new',
      events: ['node.connected'],
      enabled: false,
    })
    expect(JSON.parse(String(calls[6].body))).toEqual({ enabled: false })
    expect(calls[7].body).toBeUndefined()
  })

  it('omits unset usage filters from the query string', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL) =>
      jsonResponse({
        from: '2026-09-24T00:00:00Z',
        to: '2026-10-24T00:00:00Z',
        requests: 0,
        bytes: 0,
        errors: 0,
        accounts: [],
        users: [],
        days: [],
        topTargets: [],
        truncated: false,
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const { api } = await liveApi()
    await api.getUsage({ userId: 'u-02' })

    const url = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost')
    expect(url.pathname).toBe('/v1/usage')
    expect(url.search).toBe('?userId=u-02')
  })

  it('maps 400, 404 and 409 bodies to ApiError status and code', async () => {
    const responses: Array<[number, unknown]> = [
      [400, { error: { code: 'INVALID_ARGUMENT', message: 'from must be RFC 3339' } }],
      [404, { error: { code: 'NOT_FOUND', message: 'account "missing" not found' } }],
      [409, { error: { code: 'CONFLICT', message: 'signing secret is unavailable' } }],
    ]
    let call = 0
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        const [status, body] = responses[call]
        call += 1
        return jsonResponse(body, status)
      }),
    )

    const { api } = await liveApi()
    await expectApiError(api.getUsage({ from: 'bad' }), 400, 'INVALID_ARGUMENT')
    await expectApiError(api.getRenewalAccount('missing'), 404, 'NOT_FOUND')
    await expectApiError(api.updateWebhook('wh-001', { enabled: true }), 409, 'CONFLICT')
    expect(call).toBe(3)
  })
})

describe('platform client — mock parity', () => {
  it('serves the static renewal table and derives account urgency', async () => {
    const api = await mockApi()
    await api.resetMock()

    const plans = await api.getRenewalPlans()
    expect(plans.source).toBe('static')
    expect(plans.stale).toBe(true)
    expect(plans.plans.map((plan) => plan.id)).toEqual(['M1', 'M3', 'M12', 'H6'])
    expect(plans.plans.map((plan) => plan.priceCny)).toEqual([20, 53, 198, 1])
    expect(await api.getRenewalPlans()).toEqual(plans)

    const account = await api.getRenewalAccount('acc-01')
    expect(account.accountId).toBe('acc-01')
    expect(Number.isFinite(Date.parse(account.expiresAt))).toBe(true)
    expect(account.renewalUrl).toBe('https://www.91ajs.com/purchase')
    expect(['none', 'soon', 'due', 'overdue']).toContain(account.urgency)
    expect(Number.isInteger(account.daysLeft)).toBe(true)

    await expectApiError(api.getRenewalAccount('acc-missing'), 404, 'NOT_FOUND')
  })

  it('aggregates a deterministic 30-day usage series and honours filters', async () => {
    const api = await mockApi()
    await api.resetMock()

    const window = { from: '2026-10-01T00:00:00Z', to: '2026-10-31T23:59:59Z' }
    const summary = await api.getUsage(window)
    expect(summary.days).toHaveLength(31)
    expect(summary.requests).toBeGreaterThan(0)
    expect(summary.bytes).toBeGreaterThan(0)
    expect(summary.accounts.length).toBeGreaterThan(0)
    expect(summary.users.length).toBeGreaterThan(0)
    expect(summary.topTargets.length).toBeGreaterThan(0)
    expect(summary.truncated).toBe(false)
    expect(summary.days.every((day) => /^\d{4}-\d{2}-\d{2}$/.test(day.day))).toBe(true)
    expect(summary.accounts.every((account) => account.buckets.length === summary.days.length)).toBe(true)
    // Every account bucket covers the window, and the totals add up.
    expect(summary.accounts.reduce((total, account) => total + account.requests, 0)).toBe(
      summary.requests,
    )
    expect(summary.days.reduce((total, day) => total + day.requests, 0)).toBe(summary.requests)

    // The seed is the day and ids, so the same window is byte-for-byte stable.
    expect(await api.getUsage(window)).toEqual(summary)

    const byUser = await api.getUsage({ ...window, userId: 'u-01' })
    expect(byUser.users.map((user) => user.userId)).toEqual(['u-01'])
    expect(byUser.requests).toBeLessThan(summary.requests)
    expect(byUser.users[0].quotaBytesMonth).toBe(200_000_000_000)

    const byAccount = await api.getUsage({ ...window, account: 'acc-01' })
    expect(byAccount.accounts.map((account) => account.account)).toEqual(['Northwind Ops'])
    expect(byAccount.requests).toBeLessThan(summary.requests)

    const defaultWindow = await api.getUsage()
    expect(defaultWindow.days).toHaveLength(31)

    await expectApiError(
      api.getUsage({ from: '2026-10-25', to: '2026-10-24' }),
      400,
      'INVALID_ARGUMENT',
    )
    await expectApiError(api.getUsage({ from: 'not-a-date' }), 400, 'INVALID_ARGUMENT')
    await expectApiError(
      api.getUsage({ from: '2020-01-01', to: '2026-10-24' }),
      400,
      'INVALID_ARGUMENT',
    )
  })

  it('reports an ok/degraded/down health mix with retained-window stats', async () => {
    const api = await mockApi()
    await api.resetMock()

    const health = await api.getNodeHealth()
    expect(health.enabled).toBe(true)
    expect(health.nodes.length).toBeGreaterThan(0)
    expect(new Set(health.nodes.map((node) => node.state))).toEqual(
      new Set(['ok', 'degraded', 'down']),
    )
    for (const node of health.nodes) {
      expect(node.count).toBe(120)
      expect(node.ok).toBeLessThanOrEqual(node.count)
      expect(node.successRate).toBeGreaterThanOrEqual(0)
      expect(node.successRate).toBeLessThanOrEqual(1)
      expect(node.p95Ms).toBeGreaterThanOrEqual(node.p50Ms)
      expect(node.last?.at).toBeTruthy()
    }
    const down = health.nodes.find((node) => node.state === 'down')
    expect(down?.last?.ok).toBe(false)
    expect(down?.last?.errorClass).toBe('timeout')
    expect(await api.getNodeHealth()).toEqual(health)
  })

  it('returns the webhook secret only from create and keeps list reads clean', async () => {
    const api = await mockApi()
    await api.resetMock()

    const initial = await api.listWebhooks()
    expect(initial).toHaveLength(1)
    expect(initial[0]).toMatchObject({
      id: 'wh-001',
      enabled: true,
      secretAvailable: true,
    })
    expect('secret' in initial[0]).toBe(false)

    const created = await api.createWebhook({
      url: 'https://hooks.example.com/new',
      events: ['node.connected', 'node.connected'],
      description: 'CI relay',
    })
    expect(created.secret).toMatch(/^[0-9a-f]{64}$/)
    expect(created.events).toEqual(['node.connected'])
    expect(created.enabled).toBe(true)
    expect(created.secretAvailable).toBe(true)

    const after = await api.listWebhooks()
    expect(after).toHaveLength(2)
    expect(after.every((endpoint) => !('secret' in endpoint))).toBe(true)
    expect(JSON.stringify(after)).not.toContain(created.secret)
    expect(after.find((endpoint) => endpoint.id === created.id)?.secretAvailable).toBe(true)

    const updated = await api.updateWebhook(created.id, { enabled: false })
    expect(updated.enabled).toBe(false)
    expect('secret' in updated).toBe(false)
    expect(JSON.stringify(updated)).not.toContain(created.secret)

    const deliveries = await api.listWebhookDeliveries('wh-001')
    expect(deliveries.map((delivery) => delivery.status)).toEqual([
      'success',
      'failed',
      'success',
    ])
    expect(JSON.stringify(deliveries)).not.toContain(created.secret)
    expect(await api.listWebhookDeliveries(created.id)).toEqual([])

    await api.deleteWebhook(created.id)
    expect(await api.listWebhooks()).toHaveLength(1)
    await expectApiError(api.listWebhookDeliveries(created.id), 404, 'NOT_FOUND')
    await expectApiError(api.deleteWebhook(created.id), 404, 'NOT_FOUND')
    await expectApiError(api.updateWebhook('wh-missing', { enabled: false }), 404, 'NOT_FOUND')
  })

  it('validates webhook receivers like the daemon and seeds the secret deterministically', async () => {
    const api = await mockApi()
    await api.resetMock()

    await expectApiError(api.createWebhook({ url: 'ftp://example.com/hook' }), 400, 'INVALID_ARGUMENT')
    await expectApiError(
      api.createWebhook({ url: 'http://example.com/hook' }),
      400,
      'INVALID_ARGUMENT',
    )
    await expectApiError(
      api.createWebhook({ url: 'https://user:pass@example.com/hook' }),
      400,
      'INVALID_ARGUMENT',
    )
    await expectApiError(
      api.createWebhook({
        url: 'https://example.com/hook',
        events: ['node.exploded'] as unknown as WebhookEvent[],
      }),
      400,
      'INVALID_ARGUMENT',
    )

    const loopback = await api.createWebhook({ url: 'http://127.0.0.1:9000/hook' })
    expect(loopback.id).toBe('wh-002')
    expect(loopback.events).toBeUndefined()
    expect(loopback.enabled).toBe(true)

    await api.resetMock()
    const first = await api.createWebhook({ url: 'https://hooks.example.com/same' })
    await api.resetMock()
    const second = await api.createWebhook({ url: 'https://hooks.example.com/same' })
    expect(second.secret).toBe(first.secret)
  })
})
