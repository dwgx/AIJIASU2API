import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

const LIVE_SETTINGS = {
  transport: 'httpupgrade',
  healthIntervalSec: 60,
  leaseTtl: '4h',
  subscriptionRefreshSec: 30,
  theme: 'dark',
}

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  localStorage.clear()
})

describe('live mode', () => {
  it('labels the shell as live and does not offer the mock dataset reset', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify(LIVE_SETTINGS), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })),
    )

    window.history.pushState({}, '', '/settings')
    // Imported after the env stub so lib/api.ts bakes in the live mode.
    const { default: App } = await import('../src/App')
    render(<App />)

    expect(await screen.findByRole('heading', { name: 'Settings', level: 1 })).toBeInTheDocument()
    expect(screen.getByText('live API', { selector: '.topbar__env' })).toBeInTheDocument()
    expect(screen.getByText('v0.1.0 · live')).toBeInTheDocument()
    expect(screen.queryByText('Reset mock data')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Reset dataset' })).not.toBeInTheDocument()
  })
})
