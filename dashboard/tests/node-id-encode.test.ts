import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ProxyNode } from '../src/lib/types'

/**
 * Node ids are slot-scoped: `<account>#<N>:<server>` (model.SlotKey.NodeID).
 * An unencoded '#' starts a URL fragment, so the browser drops everything
 * after it and `/v1/nodes/acc-07#1:…` reaches the daemon as `/v1/nodes/acc-07`
 * and 404s. The id segment is encoded in `lib/api.ts`, not per page; these
 * cases pin that contract for every node action path the console calls.
 */

const SLOT_NODE_ID = 'acc-07#1:vvn-5593-7566'
const ENCODED_ID = 'acc-07%231%3Avvn-5593-7566'

const NODE: ProxyNode = {
  id: SLOT_NODE_ID,
  accountId: 'acc-07',
  accountLabel: 'live-ajs-01',
  label: 'vvn-5593-7566',
  region: 'cn-zj',
  country: 'cn',
  city: 'zhenjiang',
  state: 'LIVE',
  latencyMs: 42,
  successRate: 1,
  inUse: 1,
  capacity: 1,
  egressIp: '203.0.113.9',
  protocol: 'socks5',
  gatePort: 13000,
  lastProbeAt: '2026-10-26T00:00:00Z',
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.resetModules()
  sessionStorage.clear()
})

describe('node id encoding', () => {
  it('percent-encodes the slot-scoped id in every node action path', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      jsonResponse(NODE),
    )
    vi.stubGlobal('fetch', fetchMock)

    // Imported after the env stub so lib/api.ts bakes in live mode.
    const { api } = await import('../src/lib/api')

    await expect(api.probeNode(SLOT_NODE_ID)).resolves.toMatchObject({ id: SLOT_NODE_ID })
    await expect(api.setNodeState(SLOT_NODE_ID, 'DRAINING')).resolves.toMatchObject({
      id: SLOT_NODE_ID,
    })
    await expect(api.setNodeState(SLOT_NODE_ID, 'LIVE')).resolves.toMatchObject({
      id: SLOT_NODE_ID,
    })

    const calls = fetchMock.mock.calls.map(([input, init]) => ({
      url: new URL(String(input), 'http://localhost'),
      method: init?.method ?? 'GET',
    }))

    expect(calls.map((call) => `${call.method} ${call.url.pathname}`)).toEqual([
      `POST /v1/nodes/${ENCODED_ID}/actions/probe`,
      `POST /v1/nodes/${ENCODED_ID}/actions/drain`,
      `POST /v1/nodes/${ENCODED_ID}/actions/undrain`,
    ])

    // The regression guard itself: '#' and ':' must survive as escapes, and no
    // raw '#' may appear — that is the character that truncated the live path.
    for (const call of calls) {
      const url = call.url.pathname
      expect(url).toContain('%23')
      expect(url).toContain('%3A')
      expect(url).not.toContain('#')
      expect(url).not.toContain(SLOT_NODE_ID)
    }
  })
})
