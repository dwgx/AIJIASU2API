import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import type { UsageQuery, UsageSummary } from '../src/lib/types'

const DAY_MS = 86_400_000

const CURRENT_DAYS = [
  { day: '2026-10-01', requests: 2, bytes: 1000, errors: 1, avgLatencyMs: 100 },
  { day: '2026-10-02', requests: 1, bytes: 500, errors: 0, avgLatencyMs: 250 },
]

const CURRENT: UsageSummary = {
  from: '2026-10-01T00:00:00Z',
  to: '2026-10-02T23:59:59Z',
  requests: 3,
  bytes: 1500,
  errors: 1,
  days: CURRENT_DAYS,
  accounts: [
    {
      account: 'Northwind Ops',
      requests: 2,
      bytes: 1000,
      errors: 1,
      avgLatencyMs: 100,
      buckets: CURRENT_DAYS,
    },
    {
      account: 'Field Lab',
      requests: 1,
      bytes: 500,
      errors: 0,
      avgLatencyMs: 250,
      buckets: CURRENT_DAYS,
    },
  ],
  users: [
    {
      userId: 'u-01',
      userName: 'ada.ops',
      requests: 2,
      bytes: 1000,
      quotaBytesMonth: 2000,
      usedBytes: 1000,
    },
    {
      userId: 'u-03',
      userName: 'qa.pipeline',
      requests: 1,
      bytes: 500,
      quotaBytesMonth: null,
      usedBytes: 500,
    },
  ],
  topTargets: [
    { target: 'api.example.com', requests: 2, bytes: 1000 },
    { target: 'https://registry.example.io:443/v2?x=1', requests: 1, bytes: 500 },
  ],
  truncated: false,
}

const PREVIOUS: UsageSummary = {
  from: '2026-09-29T00:00:00Z',
  to: '2026-09-30T23:59:59Z',
  requests: 2,
  bytes: 1000,
  errors: 2,
  days: [
    { day: '2026-09-29', requests: 1, bytes: 500, errors: 1, avgLatencyMs: 200 },
    { day: '2026-09-30', requests: 1, bytes: 500, errors: 1, avgLatencyMs: 200 },
  ],
  accounts: [],
  users: [],
  topTargets: [],
  truncated: false,
}

const FILTERED: UsageSummary = {
  ...CURRENT,
  requests: 1,
  bytes: 500,
  errors: 0,
  days: [
    { day: '2026-10-01', requests: 0, bytes: 0, errors: 0, avgLatencyMs: 0 },
    { day: '2026-10-02', requests: 1, bytes: 500, errors: 0, avgLatencyMs: 250 },
  ],
  accounts: [CURRENT.accounts[1]],
  users: [CURRENT.users[1]],
  topTargets: [{ target: 'api.example.com', requests: 1, bytes: 500 }],
}

/** A daemon that enforces monthly account budgets. */
const ENFORCED: UsageSummary = {
  ...CURRENT,
  quotaEnforced: true,
  accounts: [
    {
      ...CURRENT.accounts[0],
      trafficBudgetBytes: 2048,
      usedBytes: 1024,
      remainingBytes: 1024,
    },
    {
      ...CURRENT.accounts[1],
      trafficBudgetBytes: null,
      usedBytes: 512,
      remainingBytes: null,
    },
  ],
}

const ZERO_FILL: UsageSummary = {
  from: '2026-10-01T00:00:00Z',
  to: '2026-10-03T23:59:59Z',
  requests: 7,
  bytes: 0,
  errors: 0,
  days: [
    { day: '2026-10-01', requests: 5, bytes: 0, errors: 0, avgLatencyMs: 80 },
    { day: '2026-10-03', requests: 2, bytes: 0, errors: 0, avgLatencyMs: 90 },
  ],
  accounts: [],
  users: [],
  topTargets: [],
  truncated: false,
}

const EMPTY: UsageSummary = {
  from: '2026-10-01T00:00:00Z',
  to: '2026-10-02T23:59:59Z',
  requests: 0,
  bytes: 0,
  errors: 0,
  days: [
    { day: '2026-10-01', requests: 0, bytes: 0, errors: 0, avgLatencyMs: 0 },
    { day: '2026-10-02', requests: 0, bytes: 0, errors: 0, avgLatencyMs: 0 },
  ],
  accounts: [],
  users: [],
  topTargets: [],
  truncated: false,
}

function mockUsage(current: UsageSummary = CURRENT, previous: UsageSummary = PREVIOUS) {
  return vi.spyOn(api, 'getUsage').mockImplementation(async (query: UsageQuery = {}) => {
    if (query.account === 'Field Lab') return FILTERED
    if (query.to && Date.parse(query.to) > Date.parse(current.to)) return current
    return previous
  })
}

async function openUsage() {
  render(<App />)
  return screen.findByRole('heading', { name: 'Usage', level: 1 })
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/usage')
  await api.resetMock()
})

describe('usage page', () => {
  it('renders summary math, deltas and per-account roll-ups from the daemon', async () => {
    mockUsage()
    await openUsage()

    const tiles = await screen.findAllByRole('article')
    expect(tiles).toHaveLength(4)
    expect(within(tiles[0]).getByText('3')).toBeInTheDocument()
    expect(within(tiles[0]).getByText(/\+50\.0%/)).toBeInTheDocument()
    expect(within(tiles[1]).getByText('1.5 KiB')).toBeInTheDocument()
    expect(within(tiles[1]).getByText(/\+50\.0%/)).toBeInTheDocument()
    expect(within(tiles[2]).getByText('1')).toBeInTheDocument()
    expect(within(tiles[2]).getByText('33.3% of requests')).toBeInTheDocument()
    expect(within(tiles[3]).getByText('150 ms')).toBeInTheDocument()
    expect(within(tiles[3]).getByText(/-25\.0%/)).toBeInTheDocument()
    expect(
      screen.getByText('Change is versus the previous 2-day window.'),
    ).toBeInTheDocument()

    const table = screen.getByRole('table', { name: 'Usage by account' })
    const row = within(table).getByText('Northwind Ops').closest('tr')
    expect(row).not.toBeNull()
    expect(within(row as HTMLElement).getByText('1000 B')).toBeInTheDocument()
    expect(within(row as HTMLElement).getByText('100 ms')).toBeInTheDocument()
  })

  it('zero-fills missing days in the accessible chart series', async () => {
    mockUsage(ZERO_FILL, EMPTY)
    await openUsage()

    const chart = await screen.findByRole('img', { name: /Daily requests over 3 days/ })
    expect(chart).toHaveAccessibleName(/7 requests/)
    expect(chart).toHaveAccessibleName(/Zero-request days: 1/)
  })

  it('issues a new query when the window changes', async () => {
    const spy = vi.spyOn(api, 'getUsage')
    await openUsage()
    await screen.findByRole('img', { name: /Daily requests over 30 days/ })

    const user = userEvent.setup()
    const group = screen.getByRole('group', { name: 'Usage window' })
    await user.click(within(group).getByRole('button', { name: '90d' }))

    await waitFor(() => {
      const spans = spy.mock.calls.map(([query]) => {
        const value = query as UsageQuery
        if (!value.from || !value.to) return -1
        const fromDay = Math.floor(Date.parse(value.from) / DAY_MS)
        const toDay = Math.floor(Date.parse(value.to) / DAY_MS)
        return toDay - fromDay + 1
      })
      expect(spans).toContain(30)
      expect(spans).toContain(90)
    })
  })

  it('filters by account and keeps the filter menu populated', async () => {
    const spy = mockUsage()
    await openUsage()

    const table = await screen.findByRole('table', { name: 'Usage by account' })
    expect(within(table).getByText('Northwind Ops')).toBeInTheDocument()

    const user = userEvent.setup()
    await user.click(screen.getByRole('combobox', { name: 'Filter by account' }))
    await user.click(screen.getByRole('option', { name: 'Field Lab' }))

    await waitFor(() =>
      expect(within(table).queryByText('Northwind Ops')).not.toBeInTheDocument(),
    )
    expect(within(table).getByText('Field Lab')).toBeInTheDocument()
    expect(
      spy.mock.calls.some(([query]) => (query as UsageQuery).account === 'Field Lab'),
    ).toBe(true)

    await user.click(screen.getByRole('combobox', { name: 'Filter by account' }))
    expect(screen.getByRole('option', { name: 'Northwind Ops' })).toBeInTheDocument()
  })

  it('draws a quota bar only for users with a monthly quota', async () => {
    mockUsage()
    await openUsage()

    const table = await screen.findByRole('table', { name: 'Usage by user' })
    const bar = within(table).getByRole('progressbar', { name: 'ada.ops monthly quota' })
    expect(bar).toHaveAttribute('aria-valuenow', '50')
    expect(within(table).getByText('No monthly quota')).toBeInTheDocument()
  })

  it('sorts account rows by requests and bytes', async () => {
    mockUsage()
    await openUsage()

    const table = await screen.findByRole('table', { name: 'Usage by account' })
    const user = userEvent.setup()

    await user.click(within(table).getByRole('button', { name: 'Sort by Requests' }))
    await user.click(within(table).getByRole('button', { name: 'Sort by Requests' }))
    await waitFor(() => {
      const first = within(table).getAllByRole('row')[1]
      expect(first).toHaveTextContent('Field Lab')
    })

    await user.click(within(table).getByRole('button', { name: 'Sort by Bytes' }))
    await waitFor(() => {
      const first = within(table).getAllByRole('row')[1]
      expect(first).toHaveTextContent('Northwind Ops')
    })
  })

  it('shows an honest empty state when the window has no requests', async () => {
    mockUsage(EMPTY, EMPTY)
    await openUsage()

    expect(
      await screen.findByText('No requests were recorded in this window.'),
    ).toBeInTheDocument()
    expect(screen.getByText(/Widen the window or clear the account filter/)).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: /Daily requests/ })).not.toBeInTheDocument()
  })

  it('labels a truncated aggregate instead of hiding the row cap', async () => {
    mockUsage({ ...CURRENT, truncated: true })
    await openUsage()

    expect(await screen.findByText('Aggregate clipped by the row cap')).toBeInTheDocument()
    expect(
      screen.getByText('The row cap clipped this window; totals cover the first rows only.'),
    ).toBeInTheDocument()
    expect(screen.getByRole('img', { name: /Daily requests/ })).toBeInTheDocument()
  })

  it('requests the baseline with an explicit end-of-day RFC 3339 bound', async () => {
    const spy = mockUsage()
    await openUsage()
    await screen.findByRole('img', { name: /Daily requests over 2 days/ })

    const queries = spy.mock.calls.map(([query]) => query as UsageQuery)
    const baseline = queries.find((query) => query.to?.endsWith('T23:59:59.999Z'))
    expect(baseline).toBeDefined()
    expect(baseline?.from).toBe('2026-09-29T00:00:00.000Z')
    expect(baseline?.to).toBe('2026-09-30T23:59:59.999Z')
    expect(baseline?.to).not.toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(Date.parse(baseline!.to!) - Date.parse(baseline!.from!)).toBe(2 * DAY_MS - 1)
  })

  it('labels the retained history cap instead of implying full coverage', async () => {
    mockUsage({ ...CURRENT, rowsRetained: 10_000, retentionDays: 7 })
    await openUsage()

    expect(
      await screen.findByText('History is capped at the newest 10,000 requests (~7 days).'),
    ).toBeInTheDocument()
  })

  it('uses the singular day when only one day is retained', async () => {
    mockUsage({ ...CURRENT, rowsRetained: 10_000, retentionDays: 1 })
    await openUsage()

    expect(
      await screen.findByText('History is capped at the newest 10,000 requests (~1 day).'),
    ).toBeInTheDocument()
  })

  it('omits the day estimate when the store reports no retained days', async () => {
    mockUsage({ ...CURRENT, rowsRetained: 10_000, retentionDays: 0 })
    await openUsage()

    expect(
      await screen.findByText('History is capped at the newest 10,000 requests.'),
    ).toBeInTheDocument()
  })

  it('omits the retention note when the daemon reports no cap', async () => {
    mockUsage()
    await openUsage()
    await screen.findAllByRole('article')

    expect(screen.queryByText(/History is capped/)).not.toBeInTheDocument()
  })

  it('marks the quota column as advisory until the gate enforces quotas', async () => {
    mockUsage()
    await openUsage()

    const table = await screen.findByRole('table', { name: 'Usage by user' })
    const badge = within(table).getByText('Budget (advisory)')
    expect(badge).toHaveAttribute('title', 'Quotas are not enforced by the gate yet')
    expect(within(table).queryByText('Quota')).not.toBeInTheDocument()
  })

  it('drops the advisory badge once the daemon enforces quotas', async () => {
    mockUsage({ ...CURRENT, quotaEnforced: true })
    await openUsage()

    const table = await screen.findByRole('table', { name: 'Usage by user' })
    expect(within(table).getByText('Quota')).toBeInTheDocument()
    expect(within(table).queryByText('Budget (advisory)')).not.toBeInTheDocument()
    expect(within(table).queryByTitle('Quotas are not enforced by the gate yet')).toBeNull()
  })

  it('renders per-account monthly meters when quotas are enforced', async () => {
    mockUsage(ENFORCED)
    await openUsage()

    const table = await screen.findByRole('table', { name: 'Usage by account' })
    expect(within(table).getByText('Quota')).toBeInTheDocument()
    const meter = within(table).getByRole('progressbar', {
      name: 'Northwind Ops monthly quota',
    })
    expect(meter).toHaveAttribute('aria-valuenow', '50')
    expect(within(table).getByText('1.0 KiB / 2.0 KiB')).toBeInTheDocument()
    expect(within(table).getByText('No monthly quota')).toBeInTheDocument()
  })

  it('renders the usage surface in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    mockUsage({ ...CURRENT, rowsRetained: 10_000, retentionDays: 7 })
    render(<App />)

    expect(await screen.findByRole('heading', { name: '用量', level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('group', { name: '用量窗口' })).toBeInTheDocument()
    expect(screen.getByRole('table', { name: '按账号统计用量' })).toBeInTheDocument()
    expect(screen.getAllByText('请求数').length).toBeGreaterThan(0)
    expect(screen.getByRole('progressbar', { name: 'ada.ops 每月流量额度' })).toBeInTheDocument()
    expect(screen.getByText('历史只保留最新 10,000 个请求（约 7 天）。')).toBeInTheDocument()
    expect(screen.getByText('预算（仅供参考）')).toHaveAttribute(
      'title',
      '网关暂未强制执行流量额度',
    )
  })

  it('renders the in-browser mock data without fabricating URLs', async () => {
    render(<App />)

    const chart = await screen.findByRole('img', { name: /Daily requests over 30 days/ })
    expect(chart).toHaveAccessibleName(/Zero-request days: 0/)
    const table = screen.getByRole('table', { name: 'Usage by account' })
    expect(within(table).getAllByRole('row').length).toBeGreaterThan(2)
    expect(within(table).getByText('Northwind Ops')).toBeInTheDocument()
    expect(document.body.textContent).not.toMatch(/https?:\/\//)
  })
})
