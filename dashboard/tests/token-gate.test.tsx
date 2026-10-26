import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

const LIVE_SETTINGS = {
  transport: 'httpupgrade',
  healthIntervalSec: 60,
  leaseTtl: '4h',
  subscriptionRefreshSec: 30,
  theme: 'dark',
}

const LAB_OVERVIEW = {
  enabled: true,
  generatedAt: '2026-10-24T00:00:00Z',
  version: '9.9.9',
  commit: 'deadbeef',
  uptimeSec: 42,
  modes: { accountLogin: true, resin: true, updateCheck: false, secrets: true, certPin: 'pinned' },
  counts: { accounts: 0, nodes: 0, liveNodes: 0, sessions: 0, gates: 0, leases: 0 },
  warnings: [],
}

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.resetModules()
  localStorage.clear()
  sessionStorage.clear()
})

describe('runtime token gate', () => {
  it('asks for the admin token on 401 and retries with a bearer header', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    const calls: Array<{ url: string; auth: string | null }> = []

    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input)
        const headers = new Headers(init?.headers)
        const auth = headers.get('Authorization')
        calls.push({ url, auth })
        if (!auth) {
          return new Response(JSON.stringify({ error: 'unauthorized' }), {
            status: 401,
            headers: { 'Content-Type': 'application/json' },
          })
        }
        const body = url.endsWith('/v1/lab/overview')
          ? LAB_OVERVIEW
          : url.endsWith('/v1/whoami')
            ? {
                name: 'operator-1',
                role: 'admin',
                kind: 'operator',
                strictRbac: true,
                scopes: ['settings:read'],
              }
            : LIVE_SETTINGS
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )

    window.history.pushState({}, '', '/settings')
    // The env stub is read once at module load, so this test intentionally
    // imports App after stubbing rather than at file scope.
    vi.resetModules()
    const { default: App } = await import('../src/App')
    const user = userEvent.setup()
    render(<App />)

    const dialog = await screen.findByRole('dialog', { name: 'Panel token required' })
    await user.type(within(dialog).getByLabelText('Admin token'), 'test-token')
    await user.click(within(dialog).getByRole('button', { name: 'Use token' }))

    await waitFor(() => expect(sessionStorage.getItem('ajs.panelToken')).toBe('test-token'))
    await waitFor(() => expect(calls.some((call) => call.auth === 'Bearer test-token')).toBe(true))
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Panel token required' })).not.toBeInTheDocument(),
    )
    expect(screen.getByText('set (session)')).toBeInTheDocument()
    expect(await screen.findByText('operator-1')).toBeInTheDocument()
    expect(screen.getByText('strict RBAC')).toBeInTheDocument()
    expect(screen.queryByText('test-token')).not.toBeInTheDocument()
    expect(screen.queryByText(/v9\.9\.9/)).not.toBeInTheDocument()
    expect(screen.queryByText('127.0.0.1:2261')).not.toBeInTheDocument()
  })
})
