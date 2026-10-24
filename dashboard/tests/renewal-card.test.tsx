import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import { LanguageProvider, translate } from '../src/lib/i18n'
import {
  OFFICIAL_PURCHASE_URL,
  RENEWAL_AUTO_PLANS,
  RENEWAL_PLANS,
  RenewalCard,
  renewalCountdown,
  renewalStatusText,
} from '../src/components/accounts/RenewalCard'

/** Fixed clock so every countdown assertion is deterministic. */
const NOW = Date.parse('2026-10-24T12:00:00Z')
const DAY_MS = 86_400_000
const atDays = (days: number) => new Date(NOW + days * DAY_MS).toISOString()

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('renewalCountdown', () => {
  it('counts whole days, rounds up, and warns only inside five days', () => {
    expect(renewalCountdown(atDays(18), NOW)).toEqual({ days: 18, overdue: false, warning: false })
    expect(renewalCountdown(atDays(6), NOW)).toEqual({ days: 6, overdue: false, warning: false })
    expect(renewalCountdown(atDays(5), NOW)).toEqual({ days: 5, overdue: false, warning: true })
    expect(renewalCountdown(atDays(2.5), NOW)).toEqual({ days: 3, overdue: false, warning: true })
    expect(renewalCountdown(atDays(0.25), NOW)).toEqual({ days: 1, overdue: false, warning: true })
  })

  it('keeps elapsed days once the membership is overdue', () => {
    expect(renewalCountdown(atDays(-2), NOW)).toEqual({ days: -2, overdue: true, warning: true })
    expect(renewalCountdown(atDays(-0.25), NOW)).toEqual({ days: 0, overdue: true, warning: true })
  })

  it('treats an unparseable timestamp as no countdown', () => {
    expect(renewalCountdown('not-a-date', NOW)).toEqual({ days: 0, overdue: false, warning: false })
  })

  it('uses singular copy for a one-day countdown', () => {
    const en = (key: string, params?: Record<string, string | number>) => translate('en', key, params)
    const zh = (key: string, params?: Record<string, string | number>) => translate('zh', key, params)
    expect(renewalStatusText(en, { days: 1, overdue: false, warning: true })).toBe('1 day left')
    expect(renewalStatusText(en, { days: -1, overdue: true, warning: true })).toBe(
      'Expired 1 day ago',
    )
    expect(renewalStatusText(zh, { days: 1, overdue: false, warning: true })).toBe('剩余 1 天')
  })
})

describe('public renewal catalogue', () => {
  it('carries the verified public list prices and the official purchase URL', () => {
    expect(RENEWAL_PLANS).toEqual([
      { id: 'M1', priceCny: 20, saveCny: 5 },
      { id: 'M3', priceCny: 53, saveCny: 22 },
      { id: 'M12', priceCny: 198, saveCny: 102 },
      { id: 'H6', priceCny: 1, trial: true },
    ])
    expect(RENEWAL_AUTO_PLANS).toEqual([
      { id: 'M1', priceCny: 20 },
      { id: 'M3', priceCny: 53 },
    ])
    expect(OFFICIAL_PURCHASE_URL).toBe('https://www.91ajs.com/purchase')
  })

  it('exposes no payment, purchase or renewal call on the API client', () => {
    expect(Object.keys(api).filter((key) => /pay|purchase|renew|checkout/i.test(key))).toEqual([])
  })
})

describe('RenewalCard', () => {
  it('shows expiry, countdown, the five-day warning, prices and the official link', () => {
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)
    render(<RenewalCard accountLabel="Northwind Ops" expiresAt={atDays(3)} now={NOW} />)

    const card = screen.getByRole('region', { name: 'Renewal' })
    expect(within(card).getByText('3 days left')).toBeInTheDocument()
    expect(
      within(card).getByText('Expires in 3 days — renew now to keep nodes online.'),
    ).toBeInTheDocument()
    expect(within(card).getByText('Days remaining')).toBeInTheDocument()
    expect(within(card).getByText('Expires')).toBeInTheDocument()

    for (const price of ['¥20', '¥53', '¥198', '¥1']) {
      expect(within(card).getByText(price)).toBeInTheDocument()
    }
    expect(within(card).getByText('1 month')).toBeInTheDocument()
    expect(within(card).getByText('12 months')).toBeInTheDocument()
    expect(within(card).getByText('6-hour trial')).toBeInTheDocument()
    expect(within(card).getByText('save ¥102')).toBeInTheDocument()
    expect(within(card).getByText('Public list prices')).toBeInTheDocument()

    const link = within(card).getByRole('link', {
      name: 'Open the official renewal page for Northwind Ops',
    })
    expect(link).toHaveAttribute('href', 'https://www.91ajs.com/purchase')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noreferrer noopener')
    expect(link.getAttribute('href')).not.toContain('_client_login')
    expect(card.innerHTML).not.toContain('Payment/Pay')
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('warns that an overdue membership needs renewal', () => {
    render(<RenewalCard accountLabel="Legacy HK" expiresAt={atDays(-2)} now={NOW} />)

    expect(screen.getByText('Expired 2 days ago')).toBeInTheDocument()
    expect(
      screen.getByText('Membership expired — renew at 91ajs.com to restore access.'),
    ).toBeInTheDocument()
  })

  it('stays quiet outside the warning window', () => {
    render(<RenewalCard accountLabel="Field Lab" expiresAt={atDays(41)} now={NOW} />)

    expect(screen.getByText('41 days left')).toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('renders the Chinese copy', () => {
    localStorage.setItem('ajs.lang', 'zh')
    render(
      <LanguageProvider>
        <RenewalCard accountLabel="Northwind Ops" expiresAt={atDays(3)} now={NOW} />
      </LanguageProvider>,
    )

    expect(screen.getByText('剩余 3 天')).toBeInTheDocument()
    expect(screen.getByText('还有 3 天到期 — 立即续费以保持节点在线。')).toBeInTheDocument()
    expect(screen.getByText('1 个月')).toBeInTheDocument()
    expect(screen.getByText('前往 91ajs.com 续费')).toBeInTheDocument()
  })
})

describe('accounts renewal surface', () => {
  beforeEach(async () => {
    window.history.pushState({}, '', '/accounts')
    await api.resetMock()
  })

  it('shows the countdown in rows and opens the official link from the drawer', async () => {
    const user = userEvent.setup()
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)
    render(<App />)
    await screen.findByRole('heading', { name: 'Accounts', level: 1 })

    const row = (await screen.findByText('Legacy HK')).closest('tr')
    expect(row).not.toBeNull()
    expect(within(row as HTMLElement).getByText('Expired 2 days ago')).toBeInTheDocument()
    const rowLink = within(row as HTMLElement).getByRole('link', { name: /Renew at 91ajs\.com/ })
    expect(rowLink).toHaveAttribute('href', OFFICIAL_PURCHASE_URL)
    expect(rowLink).toHaveAttribute('target', '_blank')
    expect(rowLink.getAttribute('href')).not.toContain('_client_login')

    await user.click(screen.getByRole('button', { name: 'Renewal details for Legacy HK' }))
    const drawer = await screen.findByRole('dialog', { name: 'Renewal · Legacy HK' })
    expect(
      within(drawer).getByText('Membership expired — renew at 91ajs.com to restore access.'),
    ).toBeInTheDocument()
    expect(within(drawer).getByText('¥198')).toBeInTheDocument()
    const link = within(drawer).getByRole('link', {
      name: 'Open the official renewal page for Legacy HK',
    })
    expect(link).toHaveAttribute('href', OFFICIAL_PURCHASE_URL)
    expect(link).toHaveAttribute('target', '_blank')
    expect(link.getAttribute('href')).not.toContain('_client_login')
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('flags an account that expires inside five days in the row and card', async () => {
    const user = userEvent.setup()
    const [first] = await api.listAccounts()
    const soon = { ...first, expiresAt: new Date(Date.now() + 3 * DAY_MS).toISOString() }
    vi.spyOn(api, 'listAccounts').mockResolvedValue([soon])
    render(<App />)
    await screen.findByRole('heading', { name: 'Accounts', level: 1 })

    expect(await screen.findByText('3 days left')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Cards' }))
    const list = await screen.findByRole('list', { name: 'Vendor accounts' })
    const card = within(list).getAllByRole('listitem')[0]
    expect(within(card).getByText('3 days left')).toBeInTheDocument()
    expect(
      within(card).getByRole('link', {
        name: `Open the official renewal page for ${first.label}`,
      }),
    ).toHaveAttribute('href', OFFICIAL_PURCHASE_URL)
  })
})
