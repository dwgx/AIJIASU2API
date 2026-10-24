import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { UserEvent } from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import { formatDateTime } from '../src/lib/format'
import type { ProxyNode } from '../src/lib/types'

const COPY = {
  en: { heading: 'Nodes', tab: 'Connected', caption: 'Proxy nodes', drawer: 'Node' },
  zh: { heading: '节点', tab: '已连接', caption: '代理节点', drawer: '节点' },
} as const

async function openConnected(user: UserEvent, lang: 'en' | 'zh' = 'en') {
  const copy = COPY[lang]
  window.history.pushState({}, '', '/nodes')
  render(<App />)
  await screen.findByRole('heading', { name: copy.heading, level: 1 })
  await user.click(screen.getByRole('button', { name: copy.tab }))
  await screen.findByRole('table', { name: copy.caption })
}

async function openDrawer(user: UserEvent, label: string, lang: 'en' | 'zh' = 'en') {
  await user.click(await screen.findByText(label))
  return screen.findByRole('dialog', { name: `${COPY[lang].drawer} ${label}` })
}

function mockNode(nodes: ProxyNode[], id: string): ProxyNode {
  const node = nodes.find((item) => item.id === id)
  if (!node) throw new Error(`mock node ${id} missing`)
  return node
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/nodes')
  await api.resetMock()
})

describe('nodes drawer', () => {
  it('leads with the localized region and keeps the vendor code secondary', async () => {
    const user = userEvent.setup()
    await openConnected(user)
    const drawer = await openDrawer(user, 'cn-zj-01')

    expect(within(drawer).getByText('China · Zhejiang')).toBeInTheDocument()
    expect(within(drawer).getByText('cn-zj')).toHaveClass('mono')

    // The egress address is a vendor claim with a copy affordance, not a fact
    // this panel observed.
    expect(within(drawer).getByText('Vendor claim')).toBeInTheDocument()
    expect(within(drawer).getByText('not verified')).toBeInTheDocument()
    expect(
      within(drawer).getByRole('button', { name: 'Copy egress IP for cn-zj-01' }),
    ).toBeInTheDocument()
  })

  it('falls back to city and country when the vendor code is unknown', async () => {
    const nodes = await api.listNodes()
    mockNode(nodes, 'node-01').region = 'zz-99'
    vi.spyOn(api, 'listNodes').mockResolvedValue(nodes)

    const user = userEvent.setup()
    await openConnected(user)
    const drawer = await openDrawer(user, 'cn-zj-01')

    expect(within(drawer).getByText('Hangzhou, CN')).toBeInTheDocument()
    expect(within(drawer).getByText('zz-99')).toHaveClass('mono')
  })

  it('never shows an unmeasured success rate as 0.0%', async () => {
    const user = userEvent.setup()
    await openConnected(user)

    // The DOWN node carries successRate: 0 because nothing measured it.
    expect(screen.queryByText('0.0%')).not.toBeInTheDocument()

    const drawer = await openDrawer(user, 'hk-03')
    expect(within(drawer).getByText('Not measured')).toBeInTheDocument()
    expect(within(drawer).queryByText('0.0%')).not.toBeInTheDocument()

    await user.click(within(drawer).getByRole('button', { name: 'Close' }))
    const measured = await openDrawer(user, 'cn-zj-01')
    expect(within(measured).getByText('99.4%')).toBeInTheDocument()
  })

  it('reads a slightly future probe as "just now" and flags a far-future clock', async () => {
    const nodes = await api.listNodes()
    const nearFuture = Date.now() + 30_000
    const farFuture = Date.now() + 6 * 3_600_000
    const first = mockNode(nodes, 'node-01')
    const second = mockNode(nodes, 'node-02')
    first.lastProbeAt = new Date(nearFuture).toISOString()
    second.lastProbeAt = new Date(farFuture).toISOString()
    vi.spyOn(api, 'listNodes').mockResolvedValue(nodes)

    const user = userEvent.setup()
    await openConnected(user)

    const nearDrawer = await openDrawer(user, 'cn-zj-01')
    const stamp = within(nearDrawer).getByText('just now').closest('dd')
    expect(stamp).toHaveAttribute('title', formatDateTime(first.lastProbeAt))
    expect(within(nearDrawer).queryByText('clock skew')).not.toBeInTheDocument()

    await user.click(within(nearDrawer).getByRole('button', { name: 'Close' }))
    const farDrawer = await openDrawer(user, 'cn-sh-01')
    expect(within(farDrawer).getByText(formatDateTime(second.lastProbeAt))).toBeInTheDocument()
    expect(within(farDrawer).getByText('clock skew')).toBeInTheDocument()
  })

  it('verifies egress and shows claimed, observed, match and latency', async () => {
    vi.spyOn(api, 'checkLabEgress').mockResolvedValue({
      nodeId: 'node-01',
      claimed: '203.0.113.11',
      verified: '198.51.100.99',
      match: false,
      latencyMs: 250,
      checkedAt: new Date().toISOString(),
      source: 'echo',
    })

    const user = userEvent.setup()
    await openConnected(user)
    const drawer = await openDrawer(user, 'cn-zj-01')
    await user.click(within(drawer).getByRole('button', { name: 'Verify egress' }))

    const result = await screen.findByRole('region', { name: 'Egress verification' })
    expect(within(result).getByText('198.51.100.99')).toBeInTheDocument()
    expect(within(result).getByText('203.0.113.11')).toBeInTheDocument()
    expect(within(result).getByText('mismatch')).toBeInTheDocument()
    expect(within(result).getByText('250 ms')).toBeInTheDocument()

    // A verified claim is no longer labelled "not verified".
    expect(within(drawer).queryByText('not verified')).not.toBeInTheDocument()
  })

  it('disables the egress check unless the node is LIVE', async () => {
    const user = userEvent.setup()
    await openConnected(user)
    const drawer = await openDrawer(user, 'hk-01')

    const check = within(drawer).getByRole('button', { name: 'Verify egress' })
    expect(check).toBeDisabled()
    expect(check).toHaveAttribute('title', 'Node must be LIVE to verify egress')
  })

  it('surfaces a rejected egress check in the drawer and as a toast', async () => {
    const failure = '429 Too Many Requests — one egress check per node every 5 seconds'
    vi.spyOn(api, 'checkLabEgress').mockRejectedValue(new Error(failure))

    const user = userEvent.setup()
    await openConnected(user)
    const drawer = await openDrawer(user, 'cn-zj-01')
    await user.click(within(drawer).getByRole('button', { name: 'Verify egress' }))

    await waitFor(() => expect(screen.getAllByText(failure)).toHaveLength(2))
    expect(within(drawer).getByRole('alert')).toHaveTextContent(failure)
  })

  it('marks a timestamp touch as touch-only without inventing a measurement', async () => {
    const user = userEvent.setup()
    await openConnected(user)
    const drawer = await openDrawer(user, 'cn-zj-01')
    const note = 'Touch only — no latency or egress check.'
    expect(within(drawer).queryByText(note)).not.toBeInTheDocument()

    await user.click(within(drawer).getByRole('button', { name: 'Touch probe timestamp' }))
    expect(await within(drawer).findByText(note)).toBeInTheDocument()
    expect(within(drawer).getByText('99.4%')).toBeInTheDocument()
  })

  it('localizes the drawer in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    const user = userEvent.setup()
    await openConnected(user, 'zh')
    const drawer = await openDrawer(user, 'cn-zj-01', 'zh')

    expect(within(drawer).getByText('中国 · 浙江')).toBeInTheDocument()
    expect(within(drawer).getByText('厂商声称')).toBeInTheDocument()
    expect(within(drawer).getByText('未校验')).toBeInTheDocument()
    expect(within(drawer).getByRole('button', { name: '校验出口' })).toBeInTheDocument()

    await user.click(within(drawer).getByRole('button', { name: '关闭' }))
    const unmeasured = await openDrawer(user, 'hk-03', 'zh')
    expect(within(unmeasured).getByText('未测量')).toBeInTheDocument()
  })
})
