import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api, CatalogActionError } from '../src/lib/api'

function liveLabels(accountId: string): Promise<string[]> {
  return api.listNodes().then((nodes) =>
    nodes
      .filter((node) => node.accountId === accountId && node.state === 'LIVE')
      .map((node) => node.label),
  )
}

/** The table row whose title cell matches; the strip may repeat the name. */
function rowFor(title: string): HTMLTableRowElement {
  const row = screen
    .getAllByText(title)
    .map((element) => element.closest('tr'))
    .find((element): element is HTMLTableRowElement => element !== null)
  if (!row) throw new Error(`catalog row for ${title} not found`)
  return row
}

async function openCatalog(): Promise<void> {
  render(<App />)
  await screen.findByRole('heading', { name: 'Nodes', level: 1 })
  await screen.findByRole('table', { name: 'Hong Kong vendor servers' })
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/nodes')
  await api.resetMock()
})

describe('catalog target and session contract (mock)', () => {
  it('sets, persists and clears a target without starting a session', async () => {
    const before = await liveLabels('acc-01')
    const selection = await api.setSelection('acc-01', 'hk-01')
    expect(selection).toMatchObject({
      accountId: 'acc-01',
      serverId: 'hk-01',
      title: 'Hong Kong #3',
      updatedBy: 'operator',
    })
    expect(await api.getSelection('acc-01')).toEqual(selection)

    const rows = (await api.getAccountServers('acc-01', { limit: 500 })).servers
    expect(rows.find((row) => row.id === 'hk-01')?.desired).toBe(true)
    expect(rows.filter((row) => row.desired)).toHaveLength(1)
    expect(await liveLabels('acc-01')).toEqual(before)

    await api.clearSelection('acc-01')
    expect(await api.getSelection('acc-01')).toBeNull()
    await api.clearSelection('acc-01')
    expect(await liveLabels('acc-01')).toEqual(before)
  })

  it('switches to exactly one LIVE session and persists the target', async () => {
    const result = await api.connectAccountServer('acc-01', 'hk-01')
    expect(result).toMatchObject({
      accountId: 'acc-01',
      serverId: 'hk-01',
      nodeId: 'node-03',
      gatePort: 13002,
      alreadyConnected: false,
    })
    expect(result.switchedFrom).toBe('cn-zj-01')
    expect(await liveLabels('acc-01')).toEqual(['hk-01'])
    expect((await api.getSelection('acc-01'))?.serverId).toBe('hk-01')
    expect((await api.listAudit(10)).some((event) => event.action === 'catalog.switch')).toBe(true)

    const again = await api.connectAccountServer('acc-01', 'hk-01')
    expect(again.alreadyConnected).toBe(true)
  })

  it('disconnects without dropping the target and reconnects it', async () => {
    await api.setSelection('acc-01', 'hk-01')
    const disconnected = await api.disconnectAccount('acc-01')
    expect(disconnected).toMatchObject({ disconnected: true, alreadyDisconnected: false })
    expect(await liveLabels('acc-01')).toEqual([])
    expect((await api.getSelection('acc-01'))?.serverId).toBe('hk-01')

    const again = await api.disconnectAccount('acc-01')
    expect(again.alreadyDisconnected).toBe(true)

    const reconnected = await api.reconnectAccount('acc-01')
    expect(reconnected).toMatchObject({ serverId: 'hk-01', alreadyConnected: false })
    expect(await liveLabels('acc-01')).toEqual(['hk-01'])
    expect((await api.listAudit(10)).some((event) => event.action === 'catalog.reconnect')).toBe(true)
  })

  it('rejects a reconnect with neither a current session nor a target', async () => {
    await api.disconnectAccount('acc-01')
    await expect(api.reconnectAccount('acc-01')).rejects.toMatchObject({
      status: 409,
      code: 'CONFLICT',
      reason: 'no_target',
    })
  })

  it('rejects an ineligible target with the vendor reason', async () => {
    await expect(api.setSelection('acc-01', 'cn-zj-01')).rejects.toMatchObject({
      status: 409,
      reason: 'server_ineligible',
      details: { ineligibleReason: 'full_load' },
    })
    expect(await api.getSelection('acc-01')).toBeNull()
  })
})

describe('catalog row actions', () => {
  it('switches only after the one-session confirmation and shows the result', async () => {
    const user = userEvent.setup()
    const listNodes = vi.spyOn(api, 'listNodes')
    await openCatalog()

    await user.click(within(rowFor('Hong Kong #3')).getByRole('button', { name: 'Switch' }))
    const dialog = await screen.findByRole('dialog', { name: 'Switch to Hong Kong #3?' })
    expect(within(dialog).getByText(/one FV session per account/)).toBeInTheDocument()
    expect(within(dialog).getByText(/2 running sessions/)).toBeInTheDocument()

    const confirm = within(dialog).getByRole('button', { name: 'Switch' })
    expect(confirm).toBeDisabled()
    await user.click(
      within(dialog).getByRole('checkbox', {
        name: 'I understand the current session will be replaced',
      }),
    )
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    expect(
      await screen.findByText('Connected to Hong Kong #3 on gate 13002.'),
    ).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    await waitFor(() =>
      expect(within(rowFor('Hong Kong #3')).getByText('Current')).toBeInTheDocument(),
    )
    expect(within(rowFor('Hong Kong #3')).getByText('Target')).toBeInTheDocument()
    expect(within(rowFor('China · Zhejiang #1')).queryByText('Current')).not.toBeInTheDocument()
    expect(await screen.findByText('Current: Hong Kong #3')).toBeInTheDocument()
    expect(await liveLabels('acc-01')).toEqual(['hk-01'])
    expect((await api.getSelection('acc-01'))?.serverId).toBe('hk-01')
    expect((await api.listAudit(10)).some((event) => event.action === 'catalog.switch')).toBe(true)
    // The write refreshes the page through `ajs:refresh`, not a manual reload.
    await waitFor(() => expect(listNodes.mock.calls.length).toBeGreaterThan(1))
  })

  it('maps a 409 ineligible reason into the connect dialog', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'connectAccountServer').mockRejectedValue(
      new CatalogActionError(
        409,
        '409 Conflict — server is not eligible',
        'CONFLICT',
        'server_ineligible',
        { ineligibleReason: 'full_load' },
      ),
    )
    await openCatalog()

    await user.click(within(rowFor('Hong Kong #3')).getByRole('button', { name: 'Switch' }))
    const dialog = await screen.findByRole('dialog', { name: 'Switch to Hong Kong #3?' })
    await user.click(
      within(dialog).getByRole('checkbox', {
        name: 'I understand the current session will be replaced',
      }),
    )
    await user.click(within(dialog).getByRole('button', { name: 'Switch' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'The vendor reports full load.',
    )
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(await liveLabels('acc-01')).toEqual(['cn-zj-01', 'cn-sh-01'])
  })

  it('surfaces a classified warning on a successful connect', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'connectAccountServer').mockResolvedValue({
      accountId: 'acc-01',
      serverId: 'hk-01',
      nodeId: 'node-03',
      nodeState: 'LIVE',
      gatePort: 13002,
      egressIp: '203.0.113.13',
      switchedFrom: 'cn-zj-01',
      durationMs: 1200,
      warnings: ['reconcile_failed'],
    })
    await openCatalog()

    await user.click(within(rowFor('Hong Kong #3')).getByRole('button', { name: 'Switch' }))
    const dialog = await screen.findByRole('dialog', { name: 'Switch to Hong Kong #3?' })
    await user.click(
      within(dialog).getByRole('checkbox', {
        name: 'I understand the current session will be replaced',
      }),
    )
    await user.click(within(dialog).getByRole('button', { name: 'Switch' }))

    expect(
      await screen.findByText(
        'Connected to Hong Kong #3 on gate 13002 — panel reconciliation failed.',
      ),
    ).toBeInTheDocument()
  })

  it('shows Retry-After for a 429 and keeps the actions disabled', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'connectAccountServer').mockRejectedValue(
      new CatalogActionError(
        429,
        '429 Too Many Requests — slow down',
        'RATE_LIMITED',
        'connect_cooldown',
        null,
        30,
      ),
    )
    await openCatalog()

    await user.click(within(rowFor('Hong Kong #3')).getByRole('button', { name: 'Switch' }))
    const dialog = await screen.findByRole('dialog', { name: 'Switch to Hong Kong #3?' })
    await user.click(
      within(dialog).getByRole('checkbox', {
        name: 'I understand the current session will be replaced',
      }),
    )
    await user.click(within(dialog).getByRole('button', { name: 'Switch' }))

    expect(await within(dialog).findByText(/Rate limited — retry in \d+s\./)).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Switch' })).toBeDisabled()

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    const strip = screen.getByRole('group', { name: 'Catalog actions for Northwind Ops' })
    expect(within(strip).getByRole('button', { name: 'Reconnect' })).toBeDisabled()
    expect(screen.getByRole('alert')).toHaveTextContent(/Rate limited — retry in \d+s\./)
  })

  it('disconnects behind a danger confirm and reconnects the target', async () => {
    const user = userEvent.setup()
    await openCatalog()
    const strip = screen.getByRole('group', { name: 'Catalog actions for Northwind Ops' })

    await user.click(within(strip).getByRole('button', { name: 'Disconnect' }))
    const confirm = await screen.findByRole('dialog', { name: 'Disconnect this account?' })
    expect(
      within(confirm).getByText(/Every active flow through Northwind Ops stops/),
    ).toBeInTheDocument()
    await user.click(within(confirm).getByRole('button', { name: 'Disconnect' }))

    expect(
      await screen.findByText('Disconnected Northwind Ops. The target is kept.'),
    ).toBeInTheDocument()
    await waitFor(async () => expect(await liveLabels('acc-01')).toEqual([]))

    await user.click(within(rowFor('Hong Kong #3')).getByRole('button', { name: 'Set target' }))
    expect(
      await screen.findByText('Target set to Hong Kong #3. No session was started.'),
    ).toBeInTheDocument()

    await user.click(within(strip).getByRole('button', { name: 'Reconnect' }))
    expect(await screen.findByText('Reconnected Hong Kong #3.')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(async () => expect(await liveLabels('acc-01')).toEqual(['hk-01']))
  })

  it('forwards the catalog snapshotId on target writes', async () => {
    const user = userEvent.setup()
    const original = api.getAccountServers.bind(api)
    vi.spyOn(api, 'getAccountServers').mockImplementation(async (id, query = {}) => ({
      ...(await original(id, query)),
      snapshotId: 'cat-live',
    }))
    const setSelection = vi.spyOn(api, 'setSelection')
    await openCatalog()

    await user.click(within(rowFor('Hong Kong #3')).getByRole('button', { name: 'Set target' }))
    await waitFor(() =>
      expect(setSelection).toHaveBeenCalledWith('acc-01', 'hk-01', 'cat-live'),
    )
  })

  it('disables writes for an ineligible row and a stale account', async () => {
    const user = userEvent.setup()
    await openCatalog()

    const ineligible = within(rowFor('China · Zhejiang #1')).getByRole('button', {
      name: 'Set target',
    })
    expect(ineligible).toBeDisabled()
    expect(ineligible).toHaveAttribute('title', 'The vendor reports full load.')

    await user.click(screen.getByRole('button', { name: 'Catalog account' }))
    await user.click(screen.getByRole('option', { name: 'Legacy HK' }))

    await waitFor(() => expect(rowFor('Hong Kong #1')).toBeInTheDocument())
    const stale = rowFor('Hong Kong #1')
    expect(within(stale).getByRole('button', { name: 'Set target' })).toBeDisabled()
    expect(within(stale).getByRole('button', { name: 'Connect' })).toBeDisabled()
    expect(screen.getByRole('status')).toHaveTextContent(
      'Catalog actions are disabled while the snapshot is stale.',
    )
  })

  it('renders the target controls in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    render(<App />)

    await screen.findByRole('heading', { name: '节点', level: 1 })
    expect(await screen.findByText('未设置目标')).toBeInTheDocument()
    expect(
      within(rowFor('Hong Kong #3')).getByRole('button', { name: '设为目标' }),
    ).toBeInTheDocument()
  })
})

describe('catalog writes (live contract)', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
    vi.unstubAllGlobals()
    vi.resetModules()
  })

  it('maps 409 reason/details and 429 Retry-After from the daemon', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    const fetchMock = vi.fn(async (input: unknown, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/actions/connect') && !String(init?.body ?? '').includes('srv-2')) {
        return new Response(
          JSON.stringify({
            error: {
              code: 'CONFLICT',
              reason: 'server_ineligible',
              message: 'server X is not eligible',
              details: { ineligibleReason: 'full_load' },
            },
          }),
          { status: 409, headers: { 'Content-Type': 'application/json' } },
        )
      }
      return new Response(
        JSON.stringify({
          error: { code: 'RATE_LIMITED', reason: 'connect_cooldown', message: 'slow down' },
        }),
        {
          status: 429,
          headers: { 'Content-Type': 'application/json', 'Retry-After': '7' },
        },
      )
    })
    vi.stubGlobal('fetch', fetchMock)
    // USE_MOCK is read when lib/api.ts loads, so the env stub must precede the
    // import; a static import would bake in the test-mode mock.
    vi.resetModules()

    const live = await import('../src/lib/api')
    await expect(live.api.connectAccountServer('acc-07', 'srv-1', 'cat-1')).rejects.toMatchObject({
      status: 409,
      code: 'CONFLICT',
      reason: 'server_ineligible',
      details: { ineligibleReason: 'full_load' },
    })
    await expect(live.api.connectAccountServer('acc-07', 'srv-2')).rejects.toMatchObject({
      status: 429,
      code: 'RATE_LIMITED',
      reason: 'connect_cooldown',
      retryAfterSec: 7,
    })

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining('/v1/accounts/acc-07/actions/connect'),
      expect.objectContaining({ method: 'POST' }),
    )
    const body = JSON.parse(
      String((fetchMock.mock.calls[0]?.[1] as RequestInit | undefined)?.body),
    )
    expect(body).toEqual({ serverId: 'srv-1', snapshotId: 'cat-1', confirm: true })
  })

  it('sends the connect body and reads the live result', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    const fetchMock = vi.fn(
      async (_input: unknown, _init?: RequestInit) =>
        new Response(
          JSON.stringify({
            accountId: 'acc-07',
            serverId: 'srv-1',
            nodeId: 'acc-07:srv-1',
            nodeState: 'LIVE',
            gatePort: 13001,
            egressIp: '10.158.182.1',
            switchedFrom: 'srv-0',
            durationMs: 1840,
            warnings: [],
            auditId: 'audit-123',
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
    )
    vi.stubGlobal('fetch', fetchMock)
    // Same module-load boundary: the live flag is baked at import.
    vi.resetModules()

    const live = await import('../src/lib/api')
    await expect(live.api.connectAccountServer('acc-07', 'srv-1', 'cat-1')).resolves.toMatchObject({
      nodeId: 'acc-07:srv-1',
      nodeState: 'LIVE',
      gatePort: 13001,
      switchedFrom: 'srv-0',
      durationMs: 1840,
      auditId: 'audit-123',
    })
    const body = JSON.parse(
      String((fetchMock.mock.calls[0]?.[1] as RequestInit | undefined)?.body),
    )
    expect(body).toEqual({ serverId: 'srv-1', snapshotId: 'cat-1', confirm: true })
  })

  it('reads a missing selection route as no target', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error: { code: 'NOT_FOUND', message: 'not found' } }), {
            status: 404,
            headers: { 'Content-Type': 'application/json' },
          }),
      ),
    )
    // Same module-load boundary as above: the live flag is baked at import.
    vi.resetModules()

    const live = await import('../src/lib/api')
    await expect(live.api.getSelection('acc-07')).resolves.toBeNull()
  })
})
