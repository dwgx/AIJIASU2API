import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import {
  MOCK_PORTAL_EMAIL,
  MOCK_PORTAL_PASSWORD,
  portalApi,
} from '../src/lib/portal-api'
import type { PortalSession } from '../src/lib/portal-api'
import type { AuditEvent } from '../src/lib/types'

const LIVE_SESSION: PortalSession = {
  tenant: {
    id: 'tenant-01',
    email: 'ada@example.com',
    displayName: 'Ada Ops',
    status: 'active',
    userId: 'u-01',
    createdAt: '2026-10-01T00:00:00Z',
    approvedAt: '2026-10-02T00:00:00Z',
    suspendedAt: null,
  },
  user: { id: 'u-01', name: 'ada.ops' },
  csrfToken: 'csrf-live-activity',
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function auditRow(index: number): AuditEvent {
  return {
    id: `audit-${String(100 - index).padStart(3, '0')}`,
    ts: new Date(Date.UTC(2026, 9, 25, 12, 0, index)).toISOString(),
    severity: 'info',
    actor: 'operator',
    action: 'key.create',
    target: 'ada.ops',
    detail: index === 26 ? 'LAST-PAGE-ROW' : `ROW-${index}`,
  }
}

async function signIn(): Promise<void> {
  await portalApi.login({ email: MOCK_PORTAL_EMAIL, password: MOCK_PORTAL_PASSWORD })
}

async function openPortal(tab: string, heading = 'My portal') {
  window.history.pushState({}, '', '/portal')
  const view = render(<App />)
  await screen.findByRole('heading', { name: heading, level: 1 })
  const user = userEvent.setup()
  await user.click(await screen.findByRole('tab', { name: tab }))
  return { user, unmount: view.unmount }
}

beforeEach(async () => {
  localStorage.clear()
  sessionStorage.clear()
  window.history.pushState({}, '', '/portal/login')
  await portalApi.resetMock()
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('portal activity', () => {
  it('renders only the tenant rows and hides foreign and admin activity', async () => {
    await signIn()
    await openPortal('Activity')

    expect(
      await screen.findByRole('heading', { name: 'Your activity', level: 2 }),
    ).toBeInTheDocument()
    expect(await screen.findByText('Key prefix ajs_live_mock_01')).toBeInTheDocument()
    expect(screen.getByText('203.0.113.11 → 203.0.113.24')).toBeInTheDocument()
    expect(screen.getByText('Portal session started')).toBeInTheDocument()
    expect(screen.getByText('session.login')).toBeInTheDocument()

    // Seeded rows for build-bot and field.lab are attributable to other users.
    expect(screen.queryByText('Key prefix ajs_live_mock_03')).not.toBeInTheDocument()
    expect(screen.queryByText('Key prefix ajs_live_mock_06 revoked')).not.toBeInTheDocument()
    
  })

  it('loads the next page with the last rendered id as the cursor', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname === '/v1/portal/session') return jsonResponse(LIVE_SESSION)
      if (url.pathname === '/v1/portal/leases') return jsonResponse([])
      if (url.pathname === '/v1/portal/audit') {
        if (url.searchParams.get('before')) return jsonResponse([auditRow(25), auditRow(26)])
        return jsonResponse(Array.from({ length: 26 }, (_, index) => auditRow(index)))
      }
      throw new Error(`unexpected request ${url.pathname}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { user } = await openPortal('Activity')

    expect(await screen.findByText('ROW-0')).toBeInTheDocument()
    expect(screen.getByText('ROW-24')).toBeInTheDocument()
    expect(screen.queryByText('ROW-25')).not.toBeInTheDocument()
    expect(screen.queryByText('LAST-PAGE-ROW')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByText('LAST-PAGE-ROW')).toBeInTheDocument()
    expect(screen.getByText('ROW-25')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()

    const auditCalls = fetchMock.mock.calls
      .map(([input]) => new URL(String(input), 'http://localhost'))
      .filter((url) => url.pathname === '/v1/portal/audit')
    expect(auditCalls).toHaveLength(2)
    expect(auditCalls[0].searchParams.get('limit')).toBe('26')
    expect(auditCalls[0].searchParams.get('before')).toBeNull()
    expect(auditCalls[1].searchParams.get('limit')).toBe('26')
    expect(auditCalls[1].searchParams.get('before')).toBe('audit-076')
  })

  it('shows the empty state when the tenant has no activity', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost')
        if (url.pathname === '/v1/portal/session') return jsonResponse(LIVE_SESSION)
        if (url.pathname === '/v1/portal/leases') return jsonResponse([])
        if (url.pathname === '/v1/portal/audit') return jsonResponse([])
        throw new Error(`unexpected request ${url.pathname}`)
      }),
    )

    await openPortal('Activity')
    expect(await screen.findByText('No activity yet')).toBeInTheDocument()
    expect(
      screen.getByText('Lease, order and key events will appear here.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
  })

  it('renders the tab, table and empty copy in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    await signIn()
    await openPortal('活动', '我的门户')

    expect(
      await screen.findByRole('heading', { name: '我的活动', level: 2 }),
    ).toBeInTheDocument()
    for (const header of ['时间', '操作', '对象', '详情']) {
      expect(screen.getByRole('columnheader', { name: header })).toBeInTheDocument()
    }
    expect(await screen.findByText('Key prefix ajs_live_mock_01')).toBeInTheDocument()
    expect(screen.getByText('您账户的租约、订单、密钥与会话记录。')).toBeInTheDocument()
  })
})
