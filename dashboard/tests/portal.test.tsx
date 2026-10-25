import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { formatNumber } from '../src/lib/format'
import {
  MOCK_PENDING_EMAIL,
  MOCK_PORTAL_EMAIL,
  MOCK_PORTAL_PASSWORD,
  PORTAL_PASSWORD_MIN,
  portalApi,
} from '../src/lib/portal-api'
import type { PortalSession } from '../src/lib/portal-api'

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
  csrfToken: 'csrf-live-1',
}

function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  })
}

function headersOf(init: RequestInit | undefined): Headers {
  return new Headers(init?.headers)
}

/** Forces live mode; the client reads VITE_USE_MOCK per call. */
async function livePortal() {
  vi.stubEnv('VITE_USE_MOCK', 'false')
  return import('../src/lib/portal-api')
}

async function signInMockTenant(): Promise<void> {
  await portalApi.login({ email: MOCK_PORTAL_EMAIL, password: MOCK_PORTAL_PASSWORD })
}

async function openDashboard() {
  window.history.pushState({}, '', '/portal')
  render(<App />)
  return screen.findByRole('heading', { name: 'My portal', level: 1 })
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

describe('portal client — cookie session and CSRF', () => {
  it('uses credentials:include, sends X-CSRF-Token on unsafe calls, and never a bearer', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(LIVE_SESSION))
      .mockResolvedValueOnce(
        jsonResponse(
          {
            key: {
              id: 'key-01',
              userId: 'u-01',
              userName: 'ada.ops',
              label: 'CI',
              prefix: 'ajs_live_secret',
              createdAt: '2026-10-03T00:00:00Z',
              lastUsedAt: null,
              revokedAt: null,
            },
            secret: 'ajs_live_secret_value',
          },
          201,
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    const { portalApi: live } = await livePortal()
    const session = await live.login({ email: 'ada@example.com', password: 'secret' })
    expect(session.csrfToken).toBe('csrf-live-1')
    const created = await live.issueKey('CI')
    expect(created.secret).toBe('ajs_live_secret_value')
    await live.logout()

    expect(fetchMock).toHaveBeenCalledTimes(3)
    const [loginUrl, loginInit] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(loginUrl).toContain('/v1/portal/login')
    expect(loginInit.method).toBe('POST')
    expect(loginInit.credentials).toBe('include')
    expect(headersOf(loginInit).has('Authorization')).toBe(false)
    expect(headersOf(loginInit).has('X-CSRF-Token')).toBe(false)

    const [keyUrl, keyInit] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(keyUrl).toContain('/v1/portal/keys')
    expect(keyInit.method).toBe('POST')
    expect(keyInit.credentials).toBe('include')
    expect(headersOf(keyInit).get('X-CSRF-Token')).toBe('csrf-live-1')
    expect(headersOf(keyInit).has('Authorization')).toBe(false)

    const [logoutUrl, logoutInit] = fetchMock.mock.calls[2] as [string, RequestInit]
    expect(logoutUrl).toContain('/v1/portal/logout')
    expect(headersOf(logoutInit).get('X-CSRF-Token')).toBe('csrf-live-1')

    // The client keeps no token in Web Storage.
    expect(localStorage.length).toBe(0)
    expect(sessionStorage.length).toBe(0)
  })

  it('maps 401 and 429 responses to portal errors without storing the token', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ error: { code: 'UNAUTHORIZED', message: 'no' } }, 401))
      .mockResolvedValueOnce(
        jsonResponse({ error: { code: 'RATE_LIMITED', message: 'slow' } }, 429, {
          'Retry-After': '7',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const { portalApi: live } = await livePortal()
    const unauthorized = await live.session().catch((cause: unknown) => cause)
    expect(unauthorized).toMatchObject({ status: 401, code: 'UNAUTHORIZED' })
    const limited = await live
      .login({ email: 'ada@example.com', password: 'secret' })
      .catch((cause: unknown) => cause)
    expect(limited).toMatchObject({ status: 429, code: 'RATE_LIMITED', retryAfterSec: 7 })
    expect(localStorage.length).toBe(0)
    expect(sessionStorage.length).toBe(0)
  })
})

describe('portal client — mock parity', () => {
  it('scopes leases, usage and keys to the signed-in tenant', async () => {
    await signInMockTenant()
    const leases = await portalApi.leases()
    expect(leases.map((lease) => lease.id).sort()).toEqual(['lease-01', 'lease-02'])
    expect(leases.every((lease) => lease.userId === 'u-01')).toBe(true)

    const usage = await portalApi.usage()
    expect(usage.days).toHaveLength(30)
    expect(usage.requests).toBe(usage.days.reduce((total, day) => total + day.requests, 0))
    expect(usage.requests).toBeGreaterThan(0)

    const keys = await portalApi.keys()
    expect(keys.map((key) => key.id)).toEqual(['key-01', 'key-02'])
    expect(keys.every((key) => key.userId === 'u-01')).toBe(true)
  })

  it('issues a one-time secret and revokes only owned keys', async () => {
    await signInMockTenant()
    const created = await portalApi.issueKey('Portal test')
    expect(created.secret.startsWith('ajs_live_')).toBe(true)
    expect(JSON.stringify(await portalApi.keys())).not.toContain(created.secret)

    const untitled = await portalApi.issueKey('   ')
    expect(untitled.key.label).toBe('Untitled key')

    await portalApi.revokeKey(created.key.id)
    expect((await portalApi.keys()).find((key) => key.id === created.key.id)?.revokedAt).toBeTruthy()
    await expect(portalApi.revokeKey('key-03')).rejects.toMatchObject({ status: 404 })
  })

  it('refuses pending and suspended logins and keeps signup pending-only', async () => {
    await expect(
      portalApi.login({ email: MOCK_PENDING_EMAIL, password: MOCK_PORTAL_PASSWORD }),
    ).rejects.toMatchObject({ status: 403, code: 'TENANT_PENDING' })
    await expect(
      portalApi.login({ email: 'suspended@example.com', password: MOCK_PORTAL_PASSWORD }),
    ).rejects.toMatchObject({ status: 403, code: 'TENANT_SUSPENDED' })
    await expect(
      portalApi.login({ email: MOCK_PORTAL_EMAIL, password: 'wrong-password' }),
    ).rejects.toMatchObject({ status: 401, code: 'UNAUTHORIZED' })

    const result = await portalApi.signup({
      email: 'new.customer@example.com',
      password: 'long-enough-pass',
      displayName: 'New Customer',
    })
    expect(result).toEqual({ status: 'pending' })
    await expect(
      portalApi.login({ email: 'new.customer@example.com', password: 'long-enough-pass' }),
    ).rejects.toMatchObject({ status: 403, code: 'TENANT_PENDING' })
  })
})

describe('portal UI — authentication', () => {
  it('signs in with the cookie session and writes nothing to Web Storage', async () => {
    const user = userEvent.setup()
    render(<App />)
    expect(
      await screen.findByRole('heading', { name: 'Sign in to your portal', level: 1 }),
    ).toBeInTheDocument()
    // Language and theme providers persist their own preferences on mount;
    // clear those so only the login path is measured.
    localStorage.clear()
    sessionStorage.clear()

    await user.type(screen.getByLabelText('Email'), MOCK_PORTAL_EMAIL)
    await user.type(screen.getByLabelText('Password'), MOCK_PORTAL_PASSWORD)
    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByRole('heading', { name: 'My portal', level: 1 })).toBeInTheDocument()
    expect(await screen.findByText('Signed in as ada.ops')).toBeInTheDocument()
    expect(screen.queryByRole('complementary', { name: 'Primary navigation' })).not.toBeInTheDocument()
    expect(localStorage.length).toBe(0)
    expect(sessionStorage.length).toBe(0)
  })

  it('rejects bad credentials with one uniform message', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Sign in to your portal', level: 1 })

    await user.type(screen.getByLabelText('Email'), MOCK_PORTAL_EMAIL)
    await user.type(screen.getByLabelText('Password'), 'wrong-password')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Email or password is incorrect.')
    expect(screen.getByRole('heading', { name: 'Sign in to your portal', level: 1 })).toBeInTheDocument()
  })

  it('refuses a pending application at login', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Sign in to your portal', level: 1 })

    await user.type(screen.getByLabelText('Email'), MOCK_PENDING_EMAIL)
    await user.type(screen.getByLabelText('Password'), MOCK_PORTAL_PASSWORD)
    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'This application is still awaiting approval.',
    )
  })

  it('submits a signup application and shows the pending state', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/portal/signup')
    render(<App />)
    await screen.findByRole('heading', { name: 'Apply for portal access', level: 1 })

    await user.type(screen.getByLabelText('Display name'), 'New Customer')
    await user.type(screen.getByLabelText('Email'), 'new.customer@example.com')
    await user.type(screen.getByLabelText('Password'), 'long-enough-pass')
    await user.type(screen.getByLabelText('Confirm password'), 'long-enough-pass')
    await user.click(screen.getByRole('button', { name: 'Submit application' }))

    expect(await screen.findByRole('heading', { name: 'Application received', level: 1 })).toBeInTheDocument()
    expect(screen.getByText('Signing up created no user, lease or key.')).toBeInTheDocument()
    // The new address is pending, not an account: login stays refused.
    await expect(
      portalApi.login({ email: 'new.customer@example.com', password: 'long-enough-pass' }),
    ).rejects.toMatchObject({ status: 403, code: 'TENANT_PENDING' })
  })

  it('validates the signup form before calling the daemon', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/portal/signup')
    render(<App />)
    await screen.findByRole('heading', { name: 'Apply for portal access', level: 1 })

    await user.type(screen.getByLabelText('Display name'), 'New Customer')
    await user.type(screen.getByLabelText('Email'), 'new.customer@example.com')
    await user.type(screen.getByLabelText('Password'), 'short')
    await user.type(screen.getByLabelText('Confirm password'), 'short')
    await user.click(screen.getByRole('button', { name: 'Submit application' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      `Use at least ${PORTAL_PASSWORD_MIN} characters.`,
    )
    expect(screen.getByRole('heading', { name: 'Apply for portal access', level: 1 })).toBeInTheDocument()
  })

  it('redirects a visitor without a session to the login page', async () => {
    window.history.pushState({}, '', '/portal')
    render(<App />)
    expect(
      await screen.findByRole('heading', { name: 'Sign in to your portal', level: 1 }),
    ).toBeInTheDocument()
  })
})

describe('portal UI — own data', () => {
  it('lists only the tenant’s leases with platform, status and expiry', async () => {
    await signInMockTenant()
    await openDashboard()

    const table = await screen.findByRole('region', { name: 'Your active leases' })
    expect(within(table).getByText('cn-zj-01')).toBeInTheDocument()
    expect(within(table).getByText('hk-02')).toBeInTheDocument()
    expect(within(table).getByText('linux')).toBeInTheDocument()
    expect(within(table).getAllByText('Active').length).toBeGreaterThan(0)
    expect(within(table).getByText('Expires')).toBeInTheDocument()
    // Another user's lease is never rendered.
    expect(within(table).queryByText('jp-tyo-01')).not.toBeInTheDocument()
    expect(within(table).queryByText('build-bot')).not.toBeInTheDocument()
  })

  it('renders own usage totals and the zero-filled daily chart', async () => {
    await signInMockTenant()
    const expected = await portalApi.usage()
    await openDashboard()
    const user = userEvent.setup()

    await user.click(await screen.findByRole('tab', { name: 'Usage' }))
    const chart = await screen.findByRole('img', { name: /Daily requests over 30 days/ })
    expect(chart).toBeInTheDocument()
    expect(screen.getByText('Daily requests')).toBeInTheDocument()

    const requestsTile = screen.getByText('Requests').closest('article')
    expect(requestsTile).not.toBeNull()
    expect(within(requestsTile as HTMLElement).getByText(formatNumber(expected.requests))).toBeInTheDocument()
  })

  it('issues a key with a one-time secret and revokes it after confirmation', async () => {
    await signInMockTenant()
    await openDashboard()
    const user = userEvent.setup()

    await user.click(await screen.findByRole('tab', { name: 'API keys' }))
    expect(await screen.findByText('CI runner')).toBeInTheDocument()
    expect(screen.getByText('Laptop')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Issue key' }))
    const issueDialog = await screen.findByRole('dialog', { name: 'Issue an API key' })
    await user.type(within(issueDialog).getByLabelText('Label'), 'Portal test')
    await user.click(within(issueDialog).getByRole('button', { name: 'Issue' }))

    const secretDialog = await screen.findByRole('dialog', { name: 'Copy your new secret' })
    const secret = within(secretDialog).getByText(/^ajs_live_mock_portal_/).textContent ?? ''
    expect(secret.startsWith('ajs_live_')).toBe(true)
    await user.click(within(secretDialog).getByRole('button', { name: 'I stored it' }))

    const row = (await screen.findByText('Portal test')).closest('tr')
    expect(row).not.toBeNull()
    await user.click(within(row as HTMLElement).getByRole('button', { name: 'Revoke' }))
    const confirm = await screen.findByRole('dialog', { name: 'Revoke this key?' })
    await user.click(within(confirm).getByRole('button', { name: 'Revoke' }))

    await waitFor(() => {
      expect(within(row as HTMLElement).getByText('Revoked')).toBeInTheDocument()
    })
    expect(within(row as HTMLElement).queryByRole('button', { name: 'Revoke' })).not.toBeInTheDocument()
  })

  it('renders the portal in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    render(<App />)
    expect(
      await screen.findByRole('heading', { name: '登录客户门户', level: 1 }),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '登录' })).toBeInTheDocument()
    expect(screen.getByText('还没有账户？')).toBeInTheDocument()
    expect(document.documentElement.lang).toBe('zh-CN')
  })
})
