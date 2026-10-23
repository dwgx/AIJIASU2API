import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import type { AccountServers } from '../src/lib/types'

function emptyCatalog(accountId: string, overrides: Partial<AccountServers> = {}): AccountServers {
  return {
    enabled: true,
    accountId,
    fetchedAt: null,
    stale: true,
    servers: [],
    ...overrides,
  }
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/nodes')
  await api.resetMock()
})

describe('nodes catalog', () => {
  it('groups the real vendor catalog by region and marks the live gate', async () => {
    render(<App />)

    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    expect(await screen.findByRole('heading', { name: 'China · Zhejiang', level: 2 })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'South Korea · Seoul', level: 2 })).toBeInTheDocument()

    const zhejiang = screen.getByRole('table', { name: 'China · Zhejiang vendor servers' })
    expect(within(zhejiang).getByText('China · Zhejiang #1')).toBeInTheDocument()
    expect(within(zhejiang).getByText('cn-zj-01.edge.example.net:8443')).toBeInTheDocument()
    expect(within(zhejiang).getByText('Northwind Ops')).toBeInTheDocument()
    expect(within(zhejiang).getByText('Current')).toBeInTheDocument()

    // The live gate owns the row highlight; a READY catalog row does not.
    expect(within(zhejiang).getByText('China · Zhejiang #1').closest('tr')).toHaveClass('is-current')

    // Twelve `ServerView` rows in the mock catalog, all accounted for.
    expect(screen.getByText('12 of 12')).toBeInTheDocument()

    // A retained-but-unrefreshed snapshot is labelled instead of hidden.
    expect(screen.getByText('Catalog snapshot is stale')).toBeInTheDocument()
    expect(screen.getByText(/Stale accounts: QA Sandbox, Legacy HK/)).toBeInTheDocument()
  })

  it('renders localized region names and keeps the raw code secondary', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    render(<App />)

    expect(await screen.findByRole('heading', { name: '中国 · 浙江', level: 2 })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'cn-zj', level: 2 })).not.toBeInTheDocument()

    // Every raw region code in the catalog is secondary mono text.
    const codes = screen.getAllByText('cn-zj')
    expect(codes.length).toBeGreaterThan(0)
    for (const code of codes) expect(code).toHaveClass('mono')

    const regionFilter = screen.getByRole('combobox', { name: '按区域筛选' })
    expect(within(regionFilter).getByRole('option', { name: '中国 · 浙江' })).toBeInTheDocument()
  })

  it('renders only the rows getAccountServers returned', async () => {
    vi.spyOn(api, 'getAccountServers').mockImplementation(async (accountId: string) =>
      accountId === 'acc-01'
        ? {
            enabled: true,
            accountId,
            fetchedAt: '2026-10-24T09:00:00.000Z',
            stale: false,
            servers: [
              {
                id: 'srv-9',
                title: 'Vendor Edge',
                region: 'nl-ams',
                host: 'edge.example.org',
                port: 443,
                status: 'ok',
                nodeId: 'node-01',
                nodeState: 'LIVE',
                selected: true,
                skippedByLimit: false,
                memberOnly: true,
                fullLoad: true,
                blockingMarks: ['region-locked'],
                hasPingSecret: true,
                latencyMs: 12,
              },
            ],
          }
        : emptyCatalog(accountId),
    )

    render(<App />)

    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    expect(await screen.findByText('Vendor Edge')).toBeInTheDocument()
    expect(screen.getByText('edge.example.org:443')).toBeInTheDocument()
    expect(screen.getByText('region-locked')).toBeInTheDocument()
    expect(screen.getByText('member only')).toBeInTheDocument()
    expect(screen.getByText('full load')).toBeInTheDocument()
    expect(screen.getByText('ping secret')).toBeInTheDocument()
    expect(screen.getByText('Current')).toBeInTheDocument()
    expect(screen.getByText('1 of 1')).toBeInTheDocument()

    // No mock catalog row leaks in when the daemon returns something else.
    expect(screen.queryByText('China · Zhejiang #1')).not.toBeInTheDocument()
    expect(screen.queryByText('cn-zj-01')).not.toBeInTheDocument()
    expect(screen.queryByText('12 of 12')).not.toBeInTheDocument()
  })

  it('filters by search, region and vendor status', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('table', { name: 'China · Zhejiang vendor servers' })

    await user.type(screen.getByRole('textbox', { name: 'Search nodes' }), 'tokyo')
    await waitFor(() => expect(screen.getByText('1 of 12')).toBeInTheDocument())
    expect(screen.getByRole('table', { name: 'Japan · Tokyo vendor servers' })).toBeInTheDocument()
    expect(
      screen.queryByRole('table', { name: 'China · Zhejiang vendor servers' }),
    ).not.toBeInTheDocument()

    await user.clear(screen.getByRole('textbox', { name: 'Search nodes' }))
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by region' }), 'hk')
    await waitFor(() => expect(screen.getByText('3 of 12')).toBeInTheDocument())

    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Filter by vendor status' }),
      'offline',
    )
    await waitFor(() => expect(screen.getByText('1 of 12')).toBeInTheDocument())
    expect(
      within(screen.getByRole('table', { name: 'Hong Kong vendor servers' })).getByText('offline'),
    ).toBeInTheDocument()
  })

  it('multi-selects catalog rows, shows the count and clears it', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('table', { name: 'Hong Kong vendor servers' })

    await user.click(screen.getByRole('checkbox', { name: 'Select China · Zhejiang #1' }))
    expect(await screen.findByText('1 selected')).toBeInTheDocument()
    expect(screen.getByText('Local selection only — the daemon is not changed.')).toBeInTheDocument()

    const hongKong = screen.getByRole('table', { name: 'Hong Kong vendor servers' })
    const selectGroup = within(hongKong).getByRole('checkbox', {
      name: 'Select all servers in Hong Kong',
    })
    await user.click(selectGroup)
    expect(await screen.findByText('4 selected')).toBeInTheDocument()

    await user.click(selectGroup)
    expect(await screen.findByText('1 selected')).toBeInTheDocument()

    // Selection is page state, so inspecting the runtime view keeps it.
    await user.click(screen.getByRole('button', { name: 'Connected' }))
    await screen.findByRole('table', { name: 'Proxy nodes' })
    await user.click(screen.getByRole('button', { name: 'Catalog' }))
    expect(await screen.findByText('1 selected')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Clear selection' }))
    await waitFor(() => expect(screen.queryByText('1 selected')).not.toBeInTheDocument())
  })

  it('shows an honest empty state when the daemon retains no catalog', async () => {
    vi.spyOn(api, 'getAccountServers').mockImplementation(async (accountId: string) =>
      emptyCatalog(accountId),
    )

    render(<App />)

    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    expect(await screen.findByText('No retained catalog')).toBeInTheDocument()
    expect(screen.getByText(/never invents servers/)).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('explains a disabled catalog with the daemon reason', async () => {
    vi.spyOn(api, 'getAccountServers').mockImplementation(async (accountId: string) =>
      emptyCatalog(accountId, { enabled: false, reason: 'AJS_ACCOUNT_LOGIN is not 1' }),
    )

    render(<App />)

    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    expect(await screen.findByText('Server catalog is disabled')).toBeInTheDocument()
    expect(screen.getByText('AJS_ACCOUNT_LOGIN is not 1')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('keeps the runtime table and actions on the Connected tab', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Nodes', level: 1 })

    await user.click(screen.getByRole('button', { name: 'Connected' }))

    expect(await screen.findByRole('table', { name: 'Proxy nodes' })).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Search connected nodes' })).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Drain' }).length).toBeGreaterThan(0)
    expect(screen.getByText('cn-zj-01')).toBeInTheDocument()
    expect(screen.queryByRole('table', { name: 'China · Zhejiang vendor servers' })).not.toBeInTheDocument()
  })
})
