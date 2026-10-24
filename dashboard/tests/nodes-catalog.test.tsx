import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import type {
  AccountServers,
  ServerCatalogQuery,
  ServerView,
} from '../src/lib/types'

function emptyCatalog(accountId: string, overrides: Partial<AccountServers> = {}): AccountServers {
  return {
    enabled: true,
    accountId,
    fetchedAt: null,
    stale: true,
    total: 0,
    offset: 0,
    limit: 50,
    nextOffset: 0,
    hasMore: false,
    regions: [],
    servers: [],
    ...overrides,
  }
}

function serverView(index: number, overrides: Partial<ServerView> = {}): ServerView {
  return {
    id: `srv-${index}`,
    title: `Vendor Edge ${index}`,
    region: index % 2 === 0 ? 'cn-zj' : 'hk',
    host: `edge-${index}.example.net`,
    port: 443,
    status: index % 3 === 0 ? 'offline' : 'ok',
    nodeId: null,
    nodeState: null,
    selected: false,
    skippedByLimit: false,
    memberOnly: null,
    fullLoad: false,
    blockingMarks: [],
    hasPingSecret: false,
    latencyMs: null,
    ...overrides,
  }
}

/** Mirrors the daemon's page envelope for a fixed fixture. */
function pagedCatalog(
  accountId: string,
  all: ServerView[],
  query: ServerCatalogQuery = {},
): AccountServers {
  const offset = Math.max(0, query.offset ?? 0)
  const limit = Math.max(1, query.limit ?? 50)
  const servers = all.slice(offset, offset + limit)
  const counts = new Map<string, number>()
  for (const server of all) counts.set(server.region, (counts.get(server.region) ?? 0) + 1)
  return {
    enabled: true,
    accountId,
    fetchedAt: '2026-10-24T09:00:00.000Z',
    stale: false,
    total: all.length,
    offset,
    limit,
    nextOffset: offset + servers.length,
    hasMore: offset + servers.length < all.length,
    regions: [...counts.entries()]
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([code, count]) => ({ code, count })),
    servers,
  }
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/nodes')
  await api.resetMock()
})

/** The table row whose title cell matches; the strip may repeat the name. */
function rowFor(title: string): HTMLTableRowElement {
  const row = screen
    .getAllByText(title)
    .map((element) => element.closest('tr'))
    .find((element): element is HTMLTableRowElement => element !== null)
  if (!row) throw new Error(`catalog row for ${title} not found`)
  return row
}

describe('nodes catalog', () => {
  it('loads the selected account catalog, groups by region and marks the live gate', async () => {
    render(<App />)

    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    expect(await screen.findByRole('heading', { name: 'China · Zhejiang', level: 2 })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'China · Shanghai', level: 2 })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Hong Kong', level: 2 })).toBeInTheDocument()

    const zhejiang = screen.getByRole('table', { name: 'China · Zhejiang vendor servers' })
    expect(within(zhejiang).getByText('China · Zhejiang #1')).toBeInTheDocument()
    expect(within(zhejiang).getByText('cn-zj-01.edge.example.net:8443')).toBeInTheDocument()
    expect(within(zhejiang).getByText('Northwind Ops')).toBeInTheDocument()
    expect(within(zhejiang).getByText('Current')).toBeInTheDocument()
    expect(within(zhejiang).getByText('China · Zhejiang #1').closest('tr')).toHaveClass('is-current')

    const nav = screen.getByRole('navigation', { name: 'Catalog pagination' })
    expect(within(nav).getByText('1–3 of 3')).toBeInTheDocument()

    // The catalog is account-scoped now: another account's regions do not leak in.
    expect(screen.queryByRole('heading', { name: 'South Korea · Seoul', level: 2 })).not.toBeInTheDocument()
  })

  it('pages with offset, shows the server total and groups page rows with the region summary', async () => {
    const user = userEvent.setup()
    // The live account retains 2,814 vendor servers, all in one region.
    const all = Array.from({ length: 2814 }, (_, index) => serverView(index, { region: 'cn' }))
    const spy = vi
      .spyOn(api, 'getAccountServers')
      .mockImplementation(async (accountId: string, query: ServerCatalogQuery = {}) =>
        pagedCatalog(accountId, all, query),
      )

    render(<App />)
    await screen.findByRole('heading', { name: 'Nodes', level: 1 })

    const nav = await screen.findByRole('navigation', { name: 'Catalog pagination' })
    expect(within(nav).getByText('1–50 of 2,814')).toBeInTheDocument()
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith('acc-01', expect.objectContaining({ offset: 0, limit: 50 })),
    )

    // The group count comes from regions[] (all 2,814), not the 50 rows on the page.
    expect(screen.getByText(/2,814 servers/)).toBeInTheDocument()
    expect(screen.getByText(/showing 50 on this page/)).toBeInTheDocument()

    await user.click(within(nav).getByRole('button', { name: 'Next page' }))
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith('acc-01', expect.objectContaining({ offset: 50, limit: 50 })),
    )
    expect(within(nav).getByText('51–100 of 2,814')).toBeInTheDocument()

    await user.click(within(nav).getByRole('button', { name: 'Last page' }))
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith('acc-01', expect.objectContaining({ offset: 2800, limit: 50 })),
    )
    expect(within(nav).getByText('2,801–2,814 of 2,814')).toBeInTheDocument()

    await user.click(within(nav).getByRole('combobox', { name: 'Rows per page' }))
    await user.click(screen.getByRole('option', { name: '25' }))
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith('acc-01', expect.objectContaining({ offset: 0, limit: 25 })),
    )
    expect(within(nav).getByText('1–25 of 2,814')).toBeInTheDocument()
  })

  it('sends q, region, status and sort to the daemon and resets the offset', async () => {
    const user = userEvent.setup()
    const all = Array.from({ length: 120 }, (_, index) => serverView(index))
    const spy = vi
      .spyOn(api, 'getAccountServers')
      .mockImplementation(async (accountId: string, query: ServerCatalogQuery = {}) =>
        pagedCatalog(accountId, all, query),
      )

    render(<App />)
    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    const nav = await screen.findByRole('navigation', { name: 'Catalog pagination' })

    await user.click(within(nav).getByRole('button', { name: 'Next page' }))
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith('acc-01', expect.objectContaining({ offset: 50 })),
    )

    await user.type(screen.getByRole('textbox', { name: 'Search nodes' }), 'tokyo')
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith(
        'acc-01',
        expect.objectContaining({ q: 'tokyo', offset: 0 }),
      ),
    )

    await user.click(screen.getByRole('button', { name: 'Filter by region' }))
    await user.click(screen.getByRole('option', { name: 'Hong Kong' }))
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith(
        'acc-01',
        expect.objectContaining({ region: 'hk', offset: 0 }),
      ),
    )

    await user.click(screen.getByRole('combobox', { name: 'Filter by vendor status' }))
    await user.click(screen.getByRole('option', { name: 'offline' }))
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith(
        'acc-01',
        expect.objectContaining({ status: 'offline', offset: 0 }),
      ),
    )

    await user.click(screen.getByRole('combobox', { name: 'Sort catalog' }))
    await user.click(screen.getByRole('option', { name: 'Host' }))
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith(
        'acc-01',
        expect.objectContaining({ sort: 'host', offset: 0 }),
      ),
    )
  })

  it('scopes the catalog to the selected account and returns to the first page', async () => {
    const user = userEvent.setup()
    const spy = vi
      .spyOn(api, 'getAccountServers')
      .mockImplementation(async (accountId: string, query: ServerCatalogQuery = {}) => {
        const all = Array.from(
          { length: accountId === 'acc-03' ? 60 : 120 },
          (_, index) => serverView(index),
        )
        return pagedCatalog(accountId, all, query)
      })

    render(<App />)
    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    const nav = await screen.findByRole('navigation', { name: 'Catalog pagination' })

    await user.click(within(nav).getByRole('button', { name: 'Next page' }))
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith('acc-01', expect.objectContaining({ offset: 50 })),
    )

    await user.click(screen.getByRole('button', { name: 'Catalog account' }))
    await user.click(screen.getByRole('option', { name: 'Edge APAC' }))
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith('acc-03', expect.objectContaining({ offset: 0 })),
    )
    expect(
      within(screen.getByRole('navigation', { name: 'Catalog pagination' })).getByText('1–50 of 60'),
    ).toBeInTheDocument()
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

    const user = userEvent.setup()
    const regionFilter = screen.getByRole('button', { name: '按区域筛选' })
    await user.click(regionFilter)
    expect(await screen.findByRole('option', { name: '中国 · 浙江' })).toBeInTheDocument()
  })

  it('renders only the rows getAccountServers returned', async () => {
    vi.spyOn(api, 'getAccountServers').mockImplementation(async (accountId: string) =>
      accountId === 'acc-01'
        ? emptyCatalog(accountId, {
            fetchedAt: '2026-10-24T09:00:00.000Z',
            stale: false,
            total: 1,
            nextOffset: 1,
            regions: [{ code: 'nl-ams', count: 1 }],
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
                skippedByLimit: true,
                memberOnly: true,
                fullLoad: true,
                blockingMarks: ['region-locked'],
                hasPingSecret: true,
                latencyMs: 12,
              },
            ],
          })
        : emptyCatalog(accountId),
    )

    render(<App />)

    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    expect(await screen.findByText('Vendor Edge')).toBeInTheDocument()
    expect(screen.getByText('edge.example.org:443')).toBeInTheDocument()
    expect(screen.getByText('region-locked')).toBeInTheDocument()
    expect(screen.getByText('member only')).toBeInTheDocument()
    expect(screen.getByText('full load')).toBeInTheDocument()
    // Density contract (§4.5): ping-secret and skipped are not row badges.
    expect(screen.queryByText('ping secret')).not.toBeInTheDocument()
    expect(screen.queryByText('skipped by limit')).not.toBeInTheDocument()
    expect(screen.getByText('Current')).toBeInTheDocument()
    expect(
      within(screen.getByRole('navigation', { name: 'Catalog pagination' })).getByText('1–1 of 1'),
    ).toBeInTheDocument()

    // No mock catalog row leaks in when the daemon returns something else.
    expect(screen.queryByText('China · Zhejiang #1')).not.toBeInTheDocument()
    expect(screen.queryByText('cn-zj-01')).not.toBeInTheDocument()
  })

  it('filters by search, region and vendor status on the daemon', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('table', { name: 'China · Zhejiang vendor servers' })

    await user.type(screen.getByRole('textbox', { name: 'Search nodes' }), 'shanghai')
    await waitFor(() => expect(screen.getByText('1–1 of 1')).toBeInTheDocument())
    expect(screen.getByRole('table', { name: 'China · Shanghai vendor servers' })).toBeInTheDocument()
    expect(
      screen.queryByRole('table', { name: 'China · Zhejiang vendor servers' }),
    ).not.toBeInTheDocument()

    await user.clear(screen.getByRole('textbox', { name: 'Search nodes' }))
    await waitFor(() => expect(screen.getByText('1–3 of 3')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: 'Filter by region' }))
    await user.click(screen.getByRole('option', { name: 'Hong Kong' }))
    await waitFor(() => expect(screen.getByText('1–1 of 1')).toBeInTheDocument())
    expect(screen.getByRole('table', { name: 'Hong Kong vendor servers' })).toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: 'Filter by vendor status' }))
    await user.click(screen.getByRole('option', { name: 'ok' }))
    await waitFor(() => expect(screen.getByText('1–1 of 1')).toBeInTheDocument())
    expect(
      within(screen.getByRole('table', { name: 'Hong Kong vendor servers' })).getByText('ok'),
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
      name: 'Select visible servers in Hong Kong',
    })
    await user.click(selectGroup)
    expect(await screen.findByText('2 selected')).toBeInTheDocument()

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

  it('marks the persisted target separately from the current node', async () => {
    const user = userEvent.setup()
    const view = render(<App />)

    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    expect(await screen.findByText('No target set')).toBeInTheDocument()

    const current = rowFor('China · Zhejiang #1')
    expect(within(current).getByText('Current')).toBeInTheDocument()
    expect(within(current).queryByText('Target')).not.toBeInTheDocument()

    const target = rowFor('Hong Kong #3')
    expect(within(target).queryByText('Current')).not.toBeInTheDocument()
    await user.click(within(target).getByRole('button', { name: 'Set target' }))

    await waitFor(() => expect(within(rowFor('Hong Kong #3')).getByText('Target')).toBeInTheDocument())
    expect(within(rowFor('Hong Kong #3')).queryByText('Current')).not.toBeInTheDocument()
    expect(within(rowFor('China · Zhejiang #1')).queryByText('Target')).not.toBeInTheDocument()
    expect(await screen.findByText('Target set to Hong Kong #3. No session was started.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Audit trail' })).toHaveAttribute('href', '/logs')

    // A target is not a session: the two existing LIVE nodes are untouched.
    expect(await api.getSelection('acc-01')).toMatchObject({ serverId: 'hk-01' })
    const live = (await api.listNodes())
      .filter((node) => node.accountId === 'acc-01' && node.state === 'LIVE')
      .map((node) => node.label)
    expect(live).toEqual(['cn-zj-01', 'cn-sh-01'])

    // The target survives a reload; the catalog reads it from the daemon.
    view.unmount()
    render(<App />)
    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    await waitFor(() => expect(within(rowFor('Hong Kong #3')).getByText('Target')).toBeInTheDocument())

    const reloadedStrip = screen.getByRole('group', {
      name: 'Catalog actions for Northwind Ops',
    })
    await user.click(within(reloadedStrip).getByRole('button', { name: 'Clear target' }))
    expect(
      await screen.findByText('Target cleared. The current session is unchanged.'),
    ).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('No target set')).toBeInTheDocument())
    expect(await api.getSelection('acc-01')).toBeNull()
  })

  it('shows an honest empty state when the daemon retains no catalog', async () => {
    vi.spyOn(api, 'getAccountServers').mockImplementation(async (accountId: string) =>
      emptyCatalog(accountId),
    )

    render(<App />)

    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    expect(await screen.findByText('No retained catalog')).toBeInTheDocument()
    expect(screen.getAllByText(/never invents servers/).length).toBeGreaterThan(0)
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
