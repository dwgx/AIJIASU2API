import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import { ApiError } from '../src/lib/errors'

beforeEach(async () => {
  localStorage.clear()
  sessionStorage.clear()
  window.history.pushState({}, '', '/lab')
  await api.resetMock()
})

async function openLabTab(name: string) {
  const user = userEvent.setup()
  render(<App />)
  await screen.findByRole('heading', { name: 'Lab', level: 1 })
  await user.click(screen.getByRole('tab', { name }))
  return user
}

describe('lab page', () => {
  it('renders the overview, posture badges and effective flags', async () => {
    render(<App />)

    expect(await screen.findByRole('heading', { name: 'Lab', level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getAllByText('login on').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Resin on').length).toBeGreaterThan(0)
    expect(screen.getAllByText('pin · pinned').length).toBeGreaterThan(0)

    expect(await screen.findByText('Effective flags')).toBeInTheDocument()
    expect(screen.getByText('AJS_ACCOUNT_LOGIN')).toBeInTheDocument()
    expect(screen.getByText('AJS_ADMIN_TOKEN')).toBeInTheDocument()
    // Secret values never reach the browser: only the set state is rendered.
    expect(screen.queryByText(/supersecret/i)).not.toBeInTheDocument()
    expect(screen.getAllByText('set').length).toBeGreaterThan(0)
  })

  it('shows gate counters and redacted sessions', async () => {
    await openLabTab('Gates & sessions')

    expect(await screen.findByRole('table', { name: 'Gate counters per node' })).toBeInTheDocument()
    expect(await screen.findByRole('table', { name: 'Vendor sessions' })).toBeInTheDocument()
    expect(screen.getAllByText(/…\(32\)/).length).toBeGreaterThan(0)
    expect(screen.getAllByText('unverified').length).toBeGreaterThan(0)
  })

  it('explains disabled session telemetry instead of rendering empty tables', async () => {
    const disabled = { enabled: false as const, reason: 'AJS_ACCOUNT_LOGIN is not 1' }
    vi.spyOn(api, 'listLabSessions').mockResolvedValue(disabled)
    vi.spyOn(api, 'listLabGates').mockResolvedValue(disabled)
    vi.spyOn(api, 'listLabEgress').mockResolvedValue(disabled)

    await openLabTab('Gates & sessions')

    expect(await screen.findByText('Session telemetry is disabled')).toBeInTheDocument()
    expect(screen.getByText(/AJS_ACCOUNT_LOGIN=1/)).toBeInTheDocument()
  })

  it('explains a disabled server catalog with the exact flag', async () => {
    vi.spyOn(api, 'getAccountServers').mockResolvedValue({
      enabled: false,
      reason: 'AJS_ACCOUNT_LOGIN is not 1',
      accountId: 'acc-01',
      fetchedAt: null,
      stale: true,
      servers: [],
    })

    await openLabTab('Servers')

    expect(await screen.findByText('Server catalog is disabled')).toBeInTheDocument()
    expect(screen.getByText(/AJS_ACCOUNT_LOGIN=1/)).toBeInTheDocument()
  })

  it('shows the vendor server catalog for the selected account', async () => {
    await openLabTab('Servers')

    expect(await screen.findByRole('table', { name: 'Vendor server catalog' })).toBeInTheDocument()
    expect(screen.getByText('Catalog snapshot is fresh')).toBeInTheDocument()
    expect(screen.getAllByText('China · Zhejiang #1').length).toBeGreaterThan(0)
    expect(screen.getAllByText('selected').length).toBeGreaterThan(0)
  })

  it('shows the active profile, supported list and sample drift', async () => {
    await openLabTab('Protocol')

    expect(await screen.findByText('Active profile')).toBeInTheDocument()
    expect(screen.getAllByText('linux-4.2.3.0-binary').length).toBeGreaterThan(0)
    expect(screen.getByText('Sample baseline differs from the active profile')).toBeInTheDocument()
    expect(screen.getByRole('table', { name: 'Sample client baselines' })).toBeInTheDocument()
  })

  it('shows only hashed device identifiers', async () => {
    await openLabTab('Device')

    expect(await screen.findByText('Sealed identity')).toBeInTheDocument()
    expect(screen.getAllByText(/^sha256:[0-9a-f]{64}$/).length).toBeGreaterThan(0)
    expect(screen.getByText('Raw available')).toBeInTheDocument()
    expect(screen.getByText('no')).toBeInTheDocument()
  })

  it('polls runtime logs and pauses on demand', async () => {
    const user = await openLabTab('Logs')

    expect(await screen.findByRole('table', { name: 'Runtime log entries' })).toBeInTheDocument()
    const pause = screen.getByRole('button', { name: 'Pause' })
    await user.click(pause)
    expect(screen.getByText('paused')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Resume' })).toBeInTheDocument()
  })

  it('renders diagnostics health, state and build facts', async () => {
    await openLabTab('Diagnostics')

    expect(await screen.findByText('Component health')).toBeInTheDocument()
    expect(screen.getByText('State snapshot')).toBeInTheDocument()
    expect(screen.getByText('Build')).toBeInTheDocument()
    expect(screen.getAllByText('ok').length).toBeGreaterThan(0)
  })
})

describe('lab egress check', () => {
  it('checks the selected live node and shows claimed, verified, match and latency', async () => {
    const user = await openLabTab('Egress')
    const spy = vi.spyOn(api, 'checkLabEgress')

    const table = await screen.findByRole('table', { name: 'Egress claimed versus verified' })
    expect(within(table).getAllByText('never checked').length).toBeGreaterThan(0)

    const select = screen.getByRole('combobox', { name: 'Egress check node' })
    await waitFor(() => expect(select).toHaveTextContent('node-01'))

    const check = screen.getByRole('button', { name: 'Check egress' })
    await waitFor(() => expect(check).toBeEnabled())
    await user.click(check)

    const result = await screen.findByRole('region', { name: 'Selected node egress' })
    expect(spy).toHaveBeenCalledWith('node-01')
    expect(within(result).getAllByText('203.0.113.11')).toHaveLength(2)
    expect(within(result).getByText('42 ms')).toBeInTheDocument()
    expect(within(result).getByText('match')).toBeInTheDocument()
    expect(within(result).getByText('echo')).toBeInTheDocument()

    // The POST observation is merged into the claim-only GET row immediately.
    const checkedRow = () => {
      const cell = within(
        screen.getByRole('table', { name: 'Egress claimed versus verified' }),
      ).getByText('node-01')
      return cell.closest('tr') as HTMLElement
    }
    expect(within(checkedRow()).getAllByText('203.0.113.11')).toHaveLength(2)
    expect(within(checkedRow()).getByText('42 ms')).toBeInTheDocument()
    expect(within(checkedRow()).getByText('match')).toBeInTheDocument()
    expect(within(checkedRow()).queryByText('never checked')).not.toBeInTheDocument()

    // A refresh re-reads the claim-only GET panel; the local observation must
    // survive instead of reverting the row to "never checked". The page header
    // also has a Refresh button, so use the tab-local one (last match).
    const refreshButtons = screen.getAllByRole('button', { name: 'Refresh' })
    await user.click(refreshButtons[refreshButtons.length - 1])
    await waitFor(() => {
      expect(within(checkedRow()).getAllByText('203.0.113.11')).toHaveLength(2)
      expect(within(checkedRow()).queryByText('never checked')).not.toBeInTheDocument()
    })

    // The 5 s quota is visible: an immediate second click is disabled.
    expect(check).toBeDisabled()
  })

  it('keeps each node observation while switching the picker', async () => {
    const user = await openLabTab('Egress')
    const check = screen.getByRole('button', { name: 'Check egress' })
    await waitFor(() => expect(check).toBeEnabled())
    await user.click(check)

    const select = screen.getByRole('combobox', { name: 'Egress check node' })
    await user.click(select)
    await user.click(screen.getByRole('option', { name: /node-02/ }))
    const other = await screen.findByRole('region', { name: 'Selected node egress' })
    expect(within(other).getByText('203.0.113.12')).toBeInTheDocument()
    expect(within(other).getByText('not observed')).toBeInTheDocument()

    await user.click(select)
    await user.click(screen.getByRole('option', { name: /node-01/ }))
    const first = await screen.findByRole('region', { name: 'Selected node egress' })
    expect(within(first).getAllByText('203.0.113.11')).toHaveLength(2)
    expect(within(first).getByText('match')).toBeInTheDocument()
    expect(within(first).getByText('42 ms')).toBeInTheDocument()
  })

  it('disables the check when no gate is running', async () => {
    vi.spyOn(api, 'listLabGates').mockResolvedValue([])
    vi.spyOn(api, 'listLabEgress').mockResolvedValue([])

    await openLabTab('Egress')

    expect(
      await screen.findByText('A node must be LIVE or DRAINING before the daemon can dial its SOCKS5 gate.'),
    ).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Egress check node' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Check egress' })).toBeDisabled()
  })

  it('surfaces a 409 conflict and leaves the button usable', async () => {
    const user = await openLabTab('Egress')
    const check = screen.getByRole('button', { name: 'Check egress' })
    await waitFor(() => expect(check).toBeEnabled())
    vi.spyOn(api, 'checkLabEgress').mockRejectedValue(
      new ApiError(409, '409 Conflict — node has no running gate', 'CONFLICT'),
    )

    await user.click(check)

    expect(await screen.findByText('Egress check failed')).toBeInTheDocument()
    expect(screen.getByText(/node has no running gate/)).toBeInTheDocument()
    await waitFor(() => expect(check).toBeEnabled())
  })

  it('renders a mismatch instead of a success', async () => {
    const user = await openLabTab('Egress')
    const check = screen.getByRole('button', { name: 'Check egress' })
    await waitFor(() => expect(check).toBeEnabled())
    vi.spyOn(api, 'checkLabEgress').mockResolvedValue({
      nodeId: 'node-01',
      claimed: '203.0.113.11',
      verified: '198.51.100.99',
      match: false,
      latencyMs: 250,
      checkedAt: new Date().toISOString(),
      source: 'echo',
    })

    await user.click(check)

    const result = await screen.findByRole('region', { name: 'Selected node egress' })
    expect(within(result).getByText('198.51.100.99')).toBeInTheDocument()
    expect(within(result).getByText('mismatch')).toBeInTheDocument()
    expect(within(result).getByText('250 ms')).toBeInTheDocument()
    expect(screen.getByText('Egress mismatch')).toBeInTheDocument()
    expect(screen.getByText(/differs from the vendor claim/)).toBeInTheDocument()
  })

  it('shows a probe error and never backfills verified from the claim', async () => {
    const user = await openLabTab('Egress')
    const check = screen.getByRole('button', { name: 'Check egress' })
    await waitFor(() => expect(check).toBeEnabled())
    vi.spyOn(api, 'checkLabEgress').mockResolvedValue({
      nodeId: 'node-01',
      claimed: '203.0.113.11',
      verified: '',
      match: null,
      latencyMs: 10_000,
      checkedAt: new Date().toISOString(),
      source: 'echo',
      error: 'dial tcp 127.0.0.1:13000: i/o timeout',
    })

    await user.click(check)

    const result = await screen.findByRole('region', { name: 'Selected node egress' })
    expect(within(result).getByText('not observed')).toBeInTheDocument()
    expect(within(result).getByText('unknown')).toBeInTheDocument()
    expect(screen.getByText('Probe failed')).toBeInTheDocument()
    expect(screen.getByText('dial tcp 127.0.0.1:13000: i/o timeout')).toBeInTheDocument()

    // The row records the attempt (checkedAt/source) but never backfills the
    // claim as the verified address.
    const row = within(
      screen.getByRole('table', { name: 'Egress claimed versus verified' }),
    ).getByText('node-01').closest('tr') as HTMLElement
    expect(within(row).getByText('not observed')).toBeInTheDocument()
    expect(within(row).queryByText('never checked')).not.toBeInTheDocument()
  })
})

describe('honesty fixes', () => {
  it('labels the Requests table as real gate flows, not a simulated dataset', async () => {
    window.history.pushState({}, '', '/requests')
    render(<App />)

    await screen.findByRole('heading', { name: 'Requests', level: 1 })
    expect(screen.getByText('gate flow ring')).toBeInTheDocument()
    expect(screen.getByText('Captured by the gate, not the vendor')).toBeInTheDocument()
    const table = await screen.findByRole('table', { name: 'Finished gate flows' })
    expect(within(table).getAllByText('gate flow').length).toBeGreaterThan(0)
    expect(screen.queryByText('simulated dataset')).not.toBeInTheDocument()
  })

  it('relabels the node probe as a timestamp touch', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/nodes')
    render(<App />)

    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    await user.click(screen.getByRole('button', { name: 'Connected' }))
    await user.click(await screen.findByText('cn-zj-01'))
    const drawer = await screen.findByRole('dialog', { name: 'Node cn-zj-01' })
    expect(within(drawer).getByRole('button', { name: 'Touch probe timestamp' })).toBeInTheDocument()
    expect(within(drawer).queryByRole('button', { name: 'Probe now' })).not.toBeInTheDocument()
  })

  it('disables lease rotation when Resin is not configured', async () => {
    const overview = await api.getLabOverview()
    vi.spyOn(api, 'getLabOverview').mockResolvedValue({
      ...overview,
      modes: { ...overview.modes, resin: false },
    })
    window.history.pushState({}, '', '/leases')
    render(<App />)

    await screen.findByRole('heading', { name: 'Leases', level: 1 })
    const disabled = await screen.findAllByRole('button', {
      name: 'Rotate disabled: Resin is not configured on the daemon',
    })
    expect(disabled.length).toBeGreaterThan(0)
    for (const button of disabled) expect(button).toBeDisabled()
    expect(screen.getByText('Rotate is disabled — Resin is not configured')).toBeInTheDocument()
  })

  it('adds logout and explains that pause does not stop gates', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/accounts')
    render(<App />)

    await screen.findByRole('heading', { name: 'Accounts', level: 1 })
    expect(screen.getByText('Pause does not stop gates or the vendor session')).toBeInTheDocument()

    await user.click(await screen.findByRole('button', { name: 'Logout Northwind Ops' }))
    const dialog = await screen.findByRole('dialog', { name: 'Logout account?' })
    await user.click(within(dialog).getByRole('button', { name: 'Logout and drain' }))

    expect(await screen.findByText('Northwind Ops logged out · nodes drained')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: 'Logout account?' })).not.toBeInTheDocument()
    })
  })
})
