import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AccountServers, ServerView } from '../src/lib/types'

function serverView(index: number): ServerView {
  return {
    id: `srv-${index}`,
    title: `镇江 #${index + 1}`,
    region: 'cn',
    host: `222.186.57.${index % 200}`,
    port: 12180,
    status: 'ok',
    nodeId: null,
    nodeState: null,
    selected: false,
    skippedByLimit: true,
    memberOnly: null,
    fullLoad: false,
    blockingMarks: [],
    hasPingSecret: true,
    latencyMs: null,
  }
}

function page(offset: number, limit: number, total: number): AccountServers {
  const servers = Array.from(
    { length: Math.max(0, Math.min(limit, total - offset)) },
    (_, index) => serverView(offset + index),
  )
  const nextOffset = offset + servers.length
  return {
    enabled: true,
    accountId: 'acc-01',
    fetchedAt: '2026-10-24T09:00:00.000Z',
    stale: false,
    total,
    offset,
    limit,
    nextOffset,
    hasMore: nextOffset < total,
    regions: [{ code: 'cn', count: total }],
    servers,
  }
}

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('catalog snapshot', () => {
  it('reads every 500-row page and reuses the cache until the snapshot changes', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      const offset = Number(url.searchParams.get('offset') ?? 0)
      const limit = Number(url.searchParams.get('limit') ?? 50)
      return new Response(JSON.stringify(page(offset, limit, 1200)), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    })
    vi.stubGlobal('fetch', fetchMock)

    // Dynamic import is the point of this test: `lib/api.ts` reads
    // `VITE_USE_MOCK` at module init, so a static import would bake in mock
    // mode before `vi.stubEnv` runs (same pattern as live-mode.test.tsx).
    const { api } = await import('../src/lib/api')
    const snapshot = await api.getCatalogSnapshot('acc-01')

    expect(snapshot.servers).toHaveLength(1200)
    expect(snapshot.total).toBe(1200)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    const urls = fetchMock.mock.calls.map(([input]) => new URL(String(input), 'http://localhost'))
    expect(urls.map((url) => url.searchParams.get('offset'))).toEqual(['0', '500', '1000'])
    expect(urls.every((url) => url.searchParams.get('limit') === '500')).toBe(true)
    expect(urls.every((url) => url.pathname === '/v1/accounts/acc-01/servers')).toBe(true)

    const cached = await api.getCatalogSnapshot('acc-01', '2026-10-24T09:00:00.000Z')
    expect(cached).toBe(snapshot)
    expect(fetchMock).toHaveBeenCalledTimes(3)

    // A caller that knows an older snapshot timestamp still gets the newer
    // cached pass instead of downloading six pages again.
    const older = await api.getCatalogSnapshot('acc-01', '2026-10-24T08:00:00.000Z')
    expect(older).toBe(snapshot)
    expect(fetchMock).toHaveBeenCalledTimes(3)

    // A new daemon snapshot timestamp invalidates the cache.
    await api.getCatalogSnapshot('acc-01', '2026-10-24T10:00:00.000Z')
    expect(fetchMock).toHaveBeenCalledTimes(6)

    // An explicit null means the daemon holds no snapshot: never serve the
    // previous map from cache.
    await api.getCatalogSnapshot('acc-01', null)
    expect(fetchMock).toHaveBeenCalledTimes(9)
  })

  it('shares one in-flight pass between cold and timestamped callers', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      const offset = Number(url.searchParams.get('offset') ?? 0)
      const limit = Number(url.searchParams.get('limit') ?? 50)
      return new Response(JSON.stringify(page(offset, limit, 1200)), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    })
    vi.stubGlobal('fetch', fetchMock)

    const { api } = await import('../src/lib/api')
    const [cold, timestamped] = await Promise.all([
      api.getCatalogSnapshot('acc-03'),
      api.getCatalogSnapshot('acc-03', '2026-10-24T09:00:00.000Z'),
    ])

    expect(cold).toBe(timestamped)
    expect(cold.servers).toHaveLength(1200)
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('rejects a snapshot whose pages do not add up to the reported total', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost')
        const offset = Number(url.searchParams.get('offset') ?? 0)
        // Every page repeats the first page: 500 unique rows for a 1,200 total.
        const body = page(offset >= 500 ? 0 : offset, 500, 1200)
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )

    const { api } = await import('../src/lib/api')
    await expect(api.getCatalogSnapshot('acc-02')).rejects.toThrow(
      'returned 500 of 1200 servers',
    )
  })
})
