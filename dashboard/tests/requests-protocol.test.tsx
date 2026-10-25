import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import type { FlowView } from '../src/lib/types'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/requests')
  await api.resetMock()
})

async function openRequests(lang: 'en' | 'zh' = 'en') {
  render(<App />)
  await screen.findByRole('heading', { name: lang === 'zh' ? '请求' : 'Requests', level: 1 })
  return screen.findByRole('table', {
    name: lang === 'zh' ? '已完成的网关流' : 'Finished gate flows',
  })
}

/** One bare flow with no protocol and no attribution, as an old daemon emits. */
const BARE_FLOW: FlowView = {
  id: 'flow-bare-1',
  nodeId: 'node-01',
  accountId: 'acc-01',
  startedAt: new Date().toISOString(),
  durationMs: 12,
  method: 'CONNECT',
  targetHost: 'api.example.com',
  targetPort: 443,
  status: 'ok',
  bytesIn: 10,
  bytesOut: 20,
}

describe('requests protocol and user attribution', () => {
  it('renders protocol badges and user names from the mock ring', async () => {
    const view = await api.getLabFlows(200)
    expect(new Set(view.flows.map((flow) => flow.protocol))).toEqual(new Set(['socks5', 'http']))
    expect(view.flows.some((flow) => flow.userName === 'ada.ops')).toBe(true)

    const table = await openRequests()

    expect(within(table).getByRole('columnheader', { name: 'Protocol' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: 'User' })).toBeInTheDocument()
    expect(within(table).getAllByText('socks5').length).toBeGreaterThan(0)
    expect(within(table).getAllByText('http').length).toBeGreaterThan(0)
    expect(within(table).getAllByText('ada.ops').length).toBeGreaterThan(0)
  })

  it('filters rows by protocol and restores them', async () => {
    const user = userEvent.setup()
    const table = await openRequests()

    await user.click(screen.getByRole('combobox', { name: 'Filter by protocol' }))
    await user.click(screen.getByRole('option', { name: 'http' }))

    await waitFor(() => {
      const rows = within(table).getAllByRole('row').slice(1)
      expect(rows.length).toBeGreaterThan(0)
      for (const row of rows) expect(row.textContent).toContain('http')
    })
    expect(within(table).queryByText('socks5')).not.toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: 'Filter by protocol' }))
    await user.click(screen.getByRole('option', { name: 'All protocols' }))
    await waitFor(() => expect(within(table).getAllByText('socks5').length).toBeGreaterThan(0))
  })

  it('filters rows by the attributed user', async () => {
    const user = userEvent.setup()
    const table = await openRequests()

    await user.click(screen.getByRole('combobox', { name: 'Filter by user' }))
    await user.click(screen.getByRole('option', { name: 'field.lab' }))

    await waitFor(() => {
      const rows = within(table).getAllByRole('row').slice(1)
      expect(rows.length).toBeGreaterThan(0)
      for (const row of rows) expect(row.textContent).toContain('field.lab')
    })
  })

  it('renders an em dash when protocol and user are absent', async () => {
    vi.spyOn(api, 'getLabFlows').mockResolvedValue({
      enabled: true,
      dropped: 0,
      flows: [BARE_FLOW],
    })
    vi.spyOn(api, 'listRequests').mockResolvedValue([])

    const table = await openRequests()
    expect(screen.getByRole('combobox', { name: 'Filter by protocol' })).toBeInTheDocument()
    expect(screen.queryByRole('combobox', { name: 'Filter by user' })).not.toBeInTheDocument()

    const cells = within(within(table).getAllByRole('row')[1]).getAllByRole('cell')
    expect(cells[2]).toHaveTextContent('—')
    expect(cells[4]).toHaveTextContent('—')
  })

  it('falls back to the user id when the name is missing', async () => {
    vi.spyOn(api, 'getLabFlows').mockResolvedValue({
      enabled: true,
      dropped: 0,
      flows: [{ ...BARE_FLOW, protocol: 'http', userId: 'u-42' }],
    })
    vi.spyOn(api, 'listRequests').mockResolvedValue([])

    const user = userEvent.setup()
    const table = await openRequests()
    const row = within(table).getAllByRole('row')[1]
    expect(within(row).getByText('u-42')).toBeInTheDocument()
    expect(within(row).getByText('http')).toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: 'Filter by user' }))
    expect(screen.getByRole('option', { name: 'u-42' })).toBeInTheDocument()
  })

  it('shows protocol and user in the detail drawer', async () => {
    const user = userEvent.setup()
    const view = await api.getLabFlows(200)
    const flow = view.flows.find((row) => row.userName && row.protocol === 'http')
    expect(flow).toBeDefined()
    if (!flow) throw new Error('mock must include an http flow with a user')
    await openRequests()

    await user.click(screen.getByRole('button', { name: `View flow ${flow.id}` }))
    const drawer = await screen.findByRole('dialog', { name: 'Flow detail' })
    expect(within(drawer).getByText(flow.userName as string)).toBeInTheDocument()
    expect(within(drawer).getByText('http')).toBeInTheDocument()
  })

  it('translates the new columns and filters', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    const table = await openRequests('zh')

    expect(within(table).getByRole('columnheader', { name: '用户' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: '协议' })).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '按协议筛选' })).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '按用户筛选' })).toBeInTheDocument()
  })
})
