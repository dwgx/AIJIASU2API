import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import { ApiError } from '../src/lib/errors'
import { LanguageProvider, translate } from '../src/lib/i18n'
import type { RenewalAccountView, RenewalPlansView } from '../src/lib/types'
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

/** Two hours before the fixed clock: `formatRelative` renders "2h ago". */
const LIVE_FETCHED_AT = new Date(NOW - 2 * 3_600_000).toISOString()

const LIVE_PLANS: RenewalPlansView = {
  plans: [
    { id: 'M1', label: '1 month', months: 1, priceCny: 20, autoRenewPriceCny: 20 },
    { id: 'M3', label: '3 months', months: 3, priceCny: 53, autoRenewPriceCny: 53 },
    { id: 'M12', label: '12 months', months: 12, priceCny: 198 },
    { id: 'H6', label: '6-hour trial', months: 0, priceCny: 1, trial: true },
  ],
  fetchedAt: LIVE_FETCHED_AT,
  source: 'live',
  stale: false,
}

function liveAccount(daysLeft: number): RenewalAccountView {
  const urgency: RenewalAccountView['urgency'] =
    daysLeft < 0 ? 'overdue' : daysLeft === 0 ? 'due' : daysLeft <= 5 ? 'soon' : 'none'
  return {
    accountId: 'acc-01',
    expiresAt: atDays(daysLeft),
    daysLeft,
    urgency,
    // A hostile daemon response must never reach the outbound link.
    renewalUrl: 'https://www.91ajs.com/purchase?_client_login=leaked-token',
  }
}

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

  it('exposes no payment or purchase call on the API client', () => {
    expect(Object.keys(api).filter((key) => /pay|purchase|checkout/i.test(key))).toEqual([])
    // Renewal reads are catalogue/expiry lookups only: the panel never posts
    // to a vendor payment or renewal endpoint.
    expect(Object.keys(api).filter((key) => /renew/i.test(key)).sort()).toEqual([
      'getRenewalAccount',
      'getRenewalPlans',
    ])
  })
})

describe('RenewalCard static snapshot', () => {
  it('shows expiry, countdown, the five-day warning, prices and the official link', async () => {
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

    // The browser mock serves the daemon's built-in table, so the card marks
    // it as such instead of claiming a live catalogue.
    expect(await within(card).findByText('Built-in catalogue')).toBeInTheDocument()
    for (const price of ['¥20', '¥53', '¥198', '¥1']) {
      expect(within(card).getByText(price)).toBeInTheDocument()
    }
    expect(within(card).getByText('1 month')).toBeInTheDocument()
    expect(within(card).getByText('12 months')).toBeInTheDocument()
    expect(within(card).getByText('6-hour trial')).toBeInTheDocument()

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

  it('warns that an overdue membership needs renewal', async () => {
    render(<RenewalCard accountLabel="Legacy HK" expiresAt={atDays(-2)} now={NOW} />)

    expect(screen.getByText('Expired 2 days ago')).toBeInTheDocument()
    expect(
      screen.getByText('Membership expired — renew at 91ajs.com to restore access.'),
    ).toBeInTheDocument()
    expect(await screen.findByText('Built-in catalogue')).toBeInTheDocument()
  })

  it('stays quiet outside the warning window', async () => {
    render(<RenewalCard accountLabel="Field Lab" expiresAt={atDays(41)} now={NOW} />)

    expect(screen.getByText('41 days left')).toBeInTheDocument()
    await screen.findByText('Built-in catalogue')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('renders the Chinese copy', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    render(
      <LanguageProvider>
        <RenewalCard accountLabel="Northwind Ops" expiresAt={atDays(3)} now={NOW} />
      </LanguageProvider>,
    )

    expect(screen.getByText('剩余 3 天')).toBeInTheDocument()
    expect(screen.getByText('还有 3 天到期 — 立即续费以保持节点在线。')).toBeInTheDocument()
    expect(await screen.findByText('内置目录')).toBeInTheDocument()
    expect(screen.getByText('1 个月')).toBeInTheDocument()
    expect(screen.getByText('前往 91ajs.com 续费')).toBeInTheDocument()
  })
})

describe('RenewalCard live data', () => {
  it('prefers the live catalogue and live account urgency, and marks provenance', async () => {
    const plansSpy = vi.spyOn(api, 'getRenewalPlans').mockResolvedValue(LIVE_PLANS)
    const accountSpy = vi.spyOn(api, 'getRenewalAccount').mockResolvedValue(liveAccount(3))

    render(
      <RenewalCard
        accountId="acc-01"
        accountLabel="Northwind Ops"
        expiresAt={atDays(18)}
        now={NOW}
      />,
    )

    const card = screen.getByRole('region', { name: 'Renewal' })
    expect(await within(card).findByText('Live catalogue')).toBeInTheDocument()
    expect(within(card).getByText('Updated 2h ago')).toBeInTheDocument()
    expect(await within(card).findByText('Renew soon')).toBeInTheDocument()
    expect(
      within(card).getByText('Expires in 3 days — renew now to keep nodes online.'),
    ).toBeInTheDocument()
    expect(within(card).getByText('¥198')).toBeInTheDocument()
    expect(within(card).getByText('Auto-renew ¥20')).toBeInTheDocument()
    expect(within(card).getByText('Auto-renew ¥53')).toBeInTheDocument()
    expect(within(card).queryByText('Built-in catalogue')).not.toBeInTheDocument()
    expect(within(card).queryByText(/Prices captured from the official/)).not.toBeInTheDocument()
    expect(plansSpy).toHaveBeenCalledTimes(1)
    expect(accountSpy).toHaveBeenCalledTimes(1)
    expect(accountSpy).toHaveBeenCalledWith('acc-01')
  })

  it('falls back to the built-in snapshot on a 500 and recovers through Retry', async () => {
    const user = userEvent.setup()
    const plansSpy = vi
      .spyOn(api, 'getRenewalPlans')
      .mockRejectedValueOnce(new ApiError(500, 'catalogue exploded', 'INTERNAL'))
      .mockResolvedValue(LIVE_PLANS)
    const accountSpy = vi
      .spyOn(api, 'getRenewalAccount')
      .mockRejectedValueOnce(new ApiError(500, 'account exploded', 'INTERNAL'))
      .mockResolvedValue(liveAccount(3))

    render(
      <RenewalCard
        accountId="acc-01"
        accountLabel="Northwind Ops"
        expiresAt={atDays(18)}
        now={NOW}
      />,
    )

    const card = screen.getByRole('region', { name: 'Renewal' })
    expect(
      await within(card).findByText('The renewal catalogue is unavailable.'),
    ).toBeInTheDocument()
    expect(within(card).getByText('Built-in catalogue')).toBeInTheDocument()
    expect(within(card).getByText('save ¥102')).toBeInTheDocument()
    expect(
      within(card).getByText(
        'Prices captured from the official purchase page on 2026-10-24. They are public list prices and may change; the official checkout shows the final amount.',
      ),
    ).toBeInTheDocument()
    // The account read failed too, so the chip falls back to the row expiry.
    expect(within(card).getByText('18 days left')).toBeInTheDocument()

    await user.click(within(card).getByRole('button', { name: 'Retry' }))

    expect(await within(card).findByText('Live catalogue')).toBeInTheDocument()
    expect(
      within(card).queryByText('The renewal catalogue is unavailable.'),
    ).not.toBeInTheDocument()
    expect(within(card).getByText('Updated 2h ago')).toBeInTheDocument()
    expect(await within(card).findByText('Renew soon')).toBeInTheDocument()
    expect(plansSpy).toHaveBeenCalledTimes(2)
    expect(accountSpy).toHaveBeenCalledTimes(2)
  })

  const urgencyCases: Array<[number, string]> = [
    [-1, 'Overdue'],
    [0, 'Due today'],
    [5, 'Renew soon'],
    [6, 'Active'],
  ]

  it.each(urgencyCases)('maps live daysLeft %i to the %s chip', async (daysLeft, label) => {
    vi.spyOn(api, 'getRenewalPlans').mockResolvedValue(LIVE_PLANS)
    vi.spyOn(api, 'getRenewalAccount').mockResolvedValue(liveAccount(daysLeft))

    render(
      <RenewalCard
        accountId="acc-01"
        accountLabel="Northwind Ops"
        expiresAt={atDays(18)}
        now={NOW}
      />,
    )

    expect(await screen.findByText(label)).toBeInTheDocument()
  })

  it('uses the wire month count when a plan has no label', async () => {
    vi.spyOn(api, 'getRenewalPlans').mockResolvedValue({
      plans: [{ id: 'M6', months: 6, priceCny: 99 }],
      fetchedAt: LIVE_FETCHED_AT,
      source: 'live',
      stale: false,
    })
    vi.spyOn(api, 'getRenewalAccount').mockResolvedValue(liveAccount(3))

    render(
      <RenewalCard
        accountId="acc-01"
        accountLabel="Northwind Ops"
        expiresAt={atDays(18)}
        now={NOW}
      />,
    )

    expect(await screen.findByText('6 mo')).toBeInTheDocument()
    expect(screen.getByText('¥99')).toBeInTheDocument()
  })

  it('never carries a token into the official link', async () => {
    vi.spyOn(api, 'getRenewalPlans').mockResolvedValue(LIVE_PLANS)
    const accountSpy = vi.spyOn(api, 'getRenewalAccount').mockResolvedValue(liveAccount(3))

    render(
      <RenewalCard
        accountId="acc-01"
        accountLabel="Northwind Ops"
        expiresAt={atDays(18)}
        now={NOW}
      />,
    )

    const card = screen.getByRole('region', { name: 'Renewal' })
    await within(card).findByText('Live catalogue')
    expect(accountSpy).toHaveBeenCalledWith('acc-01')

    const link = within(card).getByRole('link', {
      name: 'Open the official renewal page for Northwind Ops',
    })
    expect(link).toHaveAttribute('href', OFFICIAL_PURCHASE_URL)
    expect(link.getAttribute('href')).not.toContain('?')
    expect(link.getAttribute('href')).not.toContain('_client_login')
    expect(card.innerHTML).not.toContain('_client_login')
    expect(card.innerHTML).not.toContain('leaked-token')
  })

  it('renders live provenance and urgency in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    vi.spyOn(api, 'getRenewalPlans').mockResolvedValue(LIVE_PLANS)
    vi.spyOn(api, 'getRenewalAccount').mockResolvedValue(liveAccount(3))

    render(
      <LanguageProvider>
        <RenewalCard
          accountId="acc-01"
          accountLabel="Northwind Ops"
          expiresAt={atDays(18)}
          now={NOW}
        />
      </LanguageProvider>,
    )

    const card = screen.getByRole('region', { name: '续费' })
    expect(await within(card).findByText('实时目录')).toBeInTheDocument()
    expect(within(card).getByText('更新于 2h前')).toBeInTheDocument()
    expect(await within(card).findByText('即将到期')).toBeInTheDocument()
    expect(within(card).getByText('1 个月')).toBeInTheDocument()
    expect(within(card).getByText('自动续费 ¥20')).toBeInTheDocument()
    expect(within(card).getByText('还有 3 天到期 — 立即续费以保持节点在线。')).toBeInTheDocument()
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
    // The drawer card reads the live account state; the mock daemon reports
    // the expired membership as overdue.
    expect(await within(drawer).findByText('Overdue')).toBeInTheDocument()
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
