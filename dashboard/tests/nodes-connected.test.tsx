import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { UserEvent } from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import type { ProxyNode } from '../src/lib/types'

const COPY = {
  en: { heading: 'Nodes', tab: 'Connected', caption: 'Proxy nodes', filter: 'Filter by node state' },
  zh: { heading: '节点', tab: '已连接', caption: '代理节点', filter: '按节点状态筛选' },
} as const

async function openConnected(user: UserEvent, lang: 'en' | 'zh' = 'en') {
  const copy = COPY[lang]
  window.history.pushState({}, '', '/nodes')
  render(<App />)
  await screen.findByRole('heading', { name: copy.heading, level: 1 })
  await user.click(screen.getByRole('button', { name: copy.tab }))
  return screen.findByRole('table', { name: copy.caption })
}

/**
 * A restart demotes the row instead of deleting it: STALE with no gate and no
 * egress (`runtime.RecoverOnStartup` in the daemon). That is the row an
 * operator opens this filter for, so the fixture mirrors it exactly.
 */
async function withStaleNode(): Promise<ProxyNode[]> {
  const nodes = await api.listNodes()
  const stale = nodes.find((node) => node.id === 'node-02')
  if (!stale) throw new Error('mock node node-02 missing')
  stale.state = 'STALE'
  stale.gatePort = 0
  stale.egressIp = ''
  vi.spyOn(api, 'listNodes').mockResolvedValue(nodes)
  return nodes
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/nodes')
  await api.resetMock()
})

describe('nodes connected state filter', () => {
  it('offers STALE between draining and down and filters to the stale rows', async () => {
    const nodes = await withStaleNode()
    const user = userEvent.setup()
    const table = await openConnected(user)

    expect(within(table).getByText('cn-sh-01')).toBeInTheDocument()

    const filter = screen.getByRole('combobox', { name: COPY.en.filter })
    await user.click(filter)
    const listbox = screen.getByRole('listbox', { name: COPY.en.filter })
    expect(within(listbox).getAllByRole('option').map((option) => option.textContent)).toEqual([
      'All statuses',
      'Live',
      'Ready',
      'Draining',
      'Stale',
      'Down',
    ])

    await user.click(within(listbox).getByRole('option', { name: 'Stale' }))

    // Header row plus exactly the demoted row: LIVE, READY and DRAINING all drop out.
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(2))
    expect(within(table).getByText('cn-sh-01')).toBeInTheDocument()
    expect(within(table).getByText('Stale')).toBeInTheDocument()
    expect(within(table).queryByText('cn-zj-01')).not.toBeInTheDocument()
    expect(within(table).queryByText('hk-01')).not.toBeInTheDocument()
    expect(within(table).queryByText('kr-sel-01')).not.toBeInTheDocument()
    expect(screen.getByText(`1 of ${nodes.length}`)).toBeInTheDocument()
  })

  it('localizes the STALE option', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    await withStaleNode()
    const user = userEvent.setup()
    const table = await openConnected(user, 'zh')

    await user.click(screen.getByRole('combobox', { name: COPY.zh.filter }))
    const listbox = screen.getByRole('listbox', { name: COPY.zh.filter })
    await user.click(within(listbox).getByRole('option', { name: '失联' }))

    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(2))
    expect(within(table).getByText('cn-sh-01')).toBeInTheDocument()
    expect(within(table).getByText('失联')).toBeInTheDocument()
  })
})
