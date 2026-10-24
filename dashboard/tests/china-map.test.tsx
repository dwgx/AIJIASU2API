import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import type {
  AccountServers,
  CatalogSnapshot,
  ServerCatalogQuery,
  ServerView,
} from '../src/lib/types'

const ACCOUNT = 'acc-01'

/** One resolvable city per province, used to spread the fixture over 31. */
const PROVINCE_CITIES: ReadonlyArray<readonly [string, string]> = [
  ['11', '北京'],
  ['12', '天津'],
  ['13', '石家庄'],
  ['14', '太原'],
  ['15', '呼和浩特'],
  ['21', '沈阳'],
  ['22', '长春'],
  ['23', '哈尔滨'],
  ['31', '上海'],
  ['32', '镇江'],
  ['33', '杭州'],
  ['34', '合肥'],
  ['35', '福州'],
  ['36', '南昌'],
  ['37', '济南'],
  ['41', '郑州'],
  ['42', '武汉'],
  ['43', '长沙'],
  ['44', '广州'],
  ['45', '南宁'],
  ['46', '海口'],
  ['50', '重庆'],
  ['51', '成都'],
  ['52', '贵阳'],
  ['53', '昆明'],
  ['54', '拉萨'],
  ['61', '西安'],
  ['62', '兰州'],
  ['63', '西宁'],
  ['64', '银川'],
  ['65', '乌鲁木齐'],
]

function serverView(index: number, title: string, overrides: Partial<ServerView> = {}): ServerView {
  return {
    id: `srv-${index}`,
    title,
    region: 'cn',
    host: `222.186.57.${index % 200}`,
    port: 12180,
    status: 'ok',
    nodeId: null,
    nodeState: null,
    selected: false,
    skippedByLimit: true,
    memberOnly: null,
    fullLoad: false,
    blockingMarks: [],
    hasPingSecret: true,
    latencyMs: null,
    ...overrides,
  }
}

/**
 * 2,814 rows: 200 in Jiangsu across five cities, 87 in each of the other 30
 * provinces, and four labels no offline table can resolve (the Other bucket).
 */
function buildCatalog(): ServerView[] {
  const rows: ServerView[] = []
  const push = (title: string, overrides: Partial<ServerView> = {}) =>
    rows.push(serverView(rows.length, title, overrides))

  const jiangsu: ReadonlyArray<readonly [string, number]> = [
    ['南京', 61],
    ['镇江', 37],
    ['苏州', 42],
    ['无锡', 30],
    ['常州', 30],
  ]
  for (const [city, count] of jiangsu) {
    for (let index = 0; index < count; index += 1) push(`${city} #${index + 1}`)
  }
  for (const [code, city] of PROVINCE_CITIES) {
    if (code === '32') continue
    for (let index = 0; index < 87; index += 1) push(`${city} #${index + 1}`)
  }
  for (let index = 0; index < 4; index += 1) push(`Offshore #${index + 1}`)

  // The live catalog has exactly one LIVE node; 镇江 #1 is its title.
  const current = rows.findIndex((row) => row.title === '镇江 #1')
  rows[current] = {
    ...rows[current],
    nodeId: `${ACCOUNT}:${rows[current].id}`,
    nodeState: 'LIVE',
    selected: true,
    skippedByLimit: false,
  }
  return rows
}

function pagedCatalog(all: ServerView[], query: ServerCatalogQuery = {}): AccountServers {
  const needle = (query.q ?? '').trim().toLowerCase()
  const filtered = all.filter((server) => {
    if (query.region && query.region !== server.region) return false
    if (query.status && query.status !== server.status) return false
    if (needle && !`${server.id} ${server.title} ${server.host} ${server.region}`.toLowerCase().includes(needle)) {
      return false
    }
    return true
  })
  const offset = Math.max(0, query.offset ?? 0)
  const limit = Math.max(1, query.limit ?? 50)
  const servers = filtered.slice(offset, offset + limit)
  const counts = new Map<string, number>()
  for (const server of filtered) counts.set(server.region, (counts.get(server.region) ?? 0) + 1)
  return {
    enabled: true,
    accountId: ACCOUNT,
    fetchedAt: '2026-10-24T09:00:00.000Z',
    stale: false,
    total: filtered.length,
    offset,
    limit,
    nextOffset: offset + servers.length,
    hasMore: offset + servers.length < filtered.length,
    regions: [...counts.entries()]
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([code, count]) => ({ code, count })),
    servers,
  }
}

function snapshotOf(all: ServerView[]): CatalogSnapshot {
  return {
    enabled: true,
    accountId: ACCOUNT,
    fetchedAt: '2026-10-24T09:00:00.000Z',
    stale: false,
    total: all.length,
    servers: all,
  }
}

const CATALOG = buildCatalog()

function mockCatalog(all: ServerView[]) {
  vi.spyOn(api, 'getAccountServers').mockImplementation(
    async (_accountId: string, query: ServerCatalogQuery = {}) => pagedCatalog(all, query),
  )
  vi.spyOn(api, 'getCatalogSnapshot').mockResolvedValue(snapshotOf(all))
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/nodes')
  await api.resetMock()
})

describe('china map', () => {
  it('renders 31 provinces, the 2,814 total and the current marker', async () => {
    mockCatalog(CATALOG)
    render(<App />)

    expect(await screen.findByRole('heading', { name: 'Mainland China' })).toBeInTheDocument()
    expect(screen.getByText('31 provinces')).toBeInTheDocument()
    expect(await screen.findByText('Servers: 2,814')).toBeInTheDocument()
    expect(screen.getByText('Current: 镇江 #1')).toBeInTheDocument()

    // 31 province shapes and the same 31 buttons, plus the Other bucket.
    expect(screen.getByTestId('china-map-svg').querySelectorAll('path[data-code]')).toHaveLength(31)
    const provinces = await screen.findByRole('list', { name: 'Provinces' })
    expect(within(provinces).getAllByRole('button')).toHaveLength(32)
    expect(within(provinces).getByRole('button', { name: 'Jiangsu, 200 servers' })).toBeInTheDocument()
    expect(
      within(provinces).getByRole('button', { name: 'Heilongjiang, 87 servers' }),
    ).toBeInTheDocument()
    expect(within(provinces).getByRole('button', { name: 'Other, 4 servers' })).toBeInTheDocument()

    // The current node gets a city dot and a province ring; the map is SVG only.
    expect(screen.getByTestId('catalog-map-current-dot')).toBeInTheDocument()
    expect(screen.getByTestId('catalog-map-current-ring')).toBeInTheDocument()
    const svg = screen.getByTestId('china-map-svg')
    expect(svg.querySelectorAll('image, use, foreignObject')).toHaveLength(0)
    expect(svg.innerHTML).not.toMatch(/https?:/)
  })

  it('filters the table to a province, resets to page 1 and clears on a second click', async () => {
    const user = userEvent.setup()
    mockCatalog(CATALOG)
    render(<App />)
    await screen.findByRole('heading', { name: 'Mainland China' })

    const nav = await screen.findByRole('navigation', { name: 'Catalog pagination' })
    await user.click(within(nav).getByRole('button', { name: 'Next page' }))
    await waitFor(() => expect(within(nav).getByText('51–100 of 2,814')).toBeInTheDocument())

    const jiangsu = await screen.findByRole('button', { name: 'Jiangsu, 200 servers' })
    await user.click(jiangsu)

    await waitFor(() => expect(within(nav).getByText('1–50 of 200')).toBeInTheDocument())
    expect(jiangsu).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText('200 servers')).toBeInTheDocument()
    expect(screen.getByText('南京 #1')).toBeInTheDocument()
    expect(screen.queryByText('哈尔滨 #1')).not.toBeInTheDocument()

    // Pagination now walks the filtered snapshot, not the server page.
    await user.click(within(nav).getByRole('button', { name: 'Next page' }))
    await waitFor(() => expect(within(nav).getByText('51–100 of 200')).toBeInTheDocument())

    await user.click(jiangsu)
    await waitFor(() => expect(within(nav).getByText('1–50 of 2,814')).toBeInTheDocument())
    expect(jiangsu).toHaveAttribute('aria-pressed', 'false')
  })

  it('narrows a selected province to a city and restores all cities', async () => {
    const user = userEvent.setup()
    mockCatalog(CATALOG)
    render(<App />)
    await screen.findByRole('heading', { name: 'Mainland China' })

    await user.click(await screen.findByRole('button', { name: 'Jiangsu, 200 servers' }))

    const nav = screen.getByRole('navigation', { name: 'Catalog pagination' })
    await waitFor(() => expect(within(nav).getByText('1–50 of 200')).toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: 'Filter by city' }))
    await user.click(await screen.findByRole('option', { name: '镇江 (37)' }))
    await waitFor(() => expect(within(nav).getByText('1–37 of 37')).toBeInTheDocument())
    expect(screen.getByText('镇江 #37')).toBeInTheDocument()
    expect(screen.queryByText('南京 #1')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Filter by city' }))
    await user.click(await screen.findByRole('option', { name: 'All cities' }))
    await waitFor(() => expect(within(nav).getByText('1–50 of 200')).toBeInTheDocument())
  })

  it('groups unresolved titles under Other instead of inventing a province', async () => {
    const user = userEvent.setup()
    mockCatalog(CATALOG)
    render(<App />)
    await screen.findByRole('heading', { name: 'Mainland China' })

    const other = await screen.findByRole('button', { name: 'Other, 4 servers' })
    await user.click(other)

    const nav = screen.getByRole('navigation', { name: 'Catalog pagination' })
    await waitFor(() => expect(within(nav).getByText('1–4 of 4')).toBeInTheDocument())
    expect(screen.getByText('Offshore #1')).toBeInTheDocument()
    expect(screen.getByText('Offshore #4')).toBeInTheDocument()
    expect(other).toHaveAttribute('aria-pressed', 'true')
  })

  it('honours prefers-reduced-motion', async () => {
    const original = window.matchMedia
    window.matchMedia = ((query: string) => ({
      matches: query.includes('prefers-reduced-motion'),
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })) as unknown as typeof window.matchMedia
    try {
      mockCatalog(CATALOG)
      render(<App />)
      await screen.findByRole('heading', { name: 'Mainland China' })
      expect(document.querySelector('.catalog-map')).toHaveAttribute('data-motion', 'reduced')
    } finally {
      window.matchMedia = original
    }
  })

  it('localizes the map, province buttons and current marker in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    mockCatalog(CATALOG)
    render(<App />)

    expect(await screen.findByRole('heading', { name: '中国大陆' })).toBeInTheDocument()
    expect(await screen.findByText('服务器：2,814')).toBeInTheDocument()
    expect(screen.getByText('31 个省级行政区')).toBeInTheDocument()
    expect(screen.getByText('当前：镇江 #1')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: '江苏，200 台服务器' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '其他，4 台服务器' })).toBeInTheDocument()
  })
})
