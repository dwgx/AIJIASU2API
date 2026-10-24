import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import { formatNumber } from '../src/lib/format'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/requests')
  await api.resetMock()
})

async function openRequests() {
  render(<App />)
  await screen.findByRole('heading', { name: 'Requests', level: 1 })
  return screen.findByRole('table', { name: 'Finished gate flows' })
}

describe('requests page', () => {
  it('renders real gate flow rows and an animated summary', async () => {
    const view = await api.getLabFlows(200)
    const table = await openRequests()

    expect(within(table).getAllByText('gate flow')).toHaveLength(view.flows.length)
    expect(within(table).getAllByText('rejected').length).toBeGreaterThan(0)
    expect(within(table).getAllByText('ok').length).toBeGreaterThan(0)

    const flowsTile = screen.getByText('Flows').closest('.metric')
    expect(flowsTile).not.toBeNull()
    await waitFor(
      () => expect(within(flowsTile as HTMLElement).getByText('200')).toBeInTheDocument(),
      { timeout: 2_000 },
    )
    expect(table.textContent).toMatch(/:\d+/)
    expect(table.textContent).not.toMatch(/https?:\/\//)
  })

  it('filters by status and source, then clears every filter', async () => {
    const user = userEvent.setup()
    const table = await openRequests()

    await user.click(screen.getByRole('combobox', { name: 'Filter by status' }))
    await user.click(screen.getByRole('option', { name: 'rejected' }))
    await waitFor(() => expect(within(table).queryAllByText('ok')).toHaveLength(0))
    expect(within(table).getAllByText('rejected').length).toBeGreaterThan(0)

    await user.click(screen.getByRole('combobox', { name: 'Filter by status' }))
    await user.click(screen.getByRole('option', { name: 'All statuses' }))
    await user.click(screen.getByRole('combobox', { name: 'Filter by source' }))
    await user.click(screen.getByRole('option', { name: 'Mutation lab' }))
    expect(await screen.findByText('No flows match these filters')).toBeInTheDocument()

    await user.click(screen.getAllByRole('button', { name: 'Clear filters' })[0])
    await waitFor(() =>
      expect(within(table).getAllByRole('row').length).toBeGreaterThan(1),
    )
  })

  it('filters by host search and narrows the time range', async () => {
    const user = userEvent.setup()
    const view = await api.getLabFlows(200)
    const host = view.flows.find((flow) => flow.targetHost)?.targetHost
    expect(host).toBeDefined()
    const table = await openRequests()

    await user.type(screen.getByRole('textbox', { name: 'Search flows' }), host as string)
    await waitFor(() => {
      const rows = within(table).getAllByRole('row').slice(1)
      expect(rows.length).toBeGreaterThan(0)
      for (const row of rows) expect(row.textContent).toContain(host)
    })

    await user.clear(screen.getByRole('textbox', { name: 'Search flows' }))
    const before = within(table).getAllByRole('row').length
    await user.click(
      within(screen.getByRole('group', { name: 'Time range' })).getByRole('button', {
        name: '15m',
      }),
    )
    await waitFor(() => expect(within(table).getAllByRole('row').length).toBeLessThan(before))
  })

  it('opens a detail drawer with the request id, redacted SID and host-only target', async () => {
    const user = userEvent.setup()
    const view = await api.getLabFlows(200)
    const flow = view.flows.find((row) => row.status === 'ok' && row.targetHost)
    expect(flow).toBeDefined()
    if (!flow) throw new Error('mock must include an accepted flow with a target')
    const target = `${flow.targetHost}:${flow.targetPort}`
    await openRequests()

    await user.click(screen.getByRole('button', { name: `View flow ${flow.id}` }))
    const drawer = await screen.findByRole('dialog', { name: 'Flow detail' })

    expect(within(drawer).getByText(flow.id)).toBeInTheDocument()
    expect(within(drawer).getByText(flow.sid as string)).toBeInTheDocument()
    expect(within(drawer).getByText(target)).toBeInTheDocument()
    expect(within(drawer).getByText(`${formatNumber(flow.durationMs)} ms`)).toBeInTheDocument()
    expect(drawer.textContent).toMatch(/…\(\d+\)/)
    expect(drawer.textContent).not.toMatch(/\b[0-9a-f]{32}\b/)
    expect(drawer.textContent).not.toMatch(/https?:\/\//)
  })

  it('explains the empty ring instead of inventing rows', async () => {
    vi.spyOn(api, 'getLabFlows').mockResolvedValue({ enabled: true, flows: [], dropped: 0 })
    await openRequests()

    expect(await screen.findByText('No finished gate flows yet')).toBeInTheDocument()
    expect(screen.getByText(/bounded ring/)).toBeInTheDocument()
  })

  it('shows the daemon reason when flow capture is disabled', async () => {
    vi.spyOn(api, 'getLabFlows').mockResolvedValue({
      enabled: false,
      reason: 'AJS_ACCOUNT_LOGIN is not 1',
      flows: [],
      dropped: 0,
    })
    await openRequests()

    expect(await screen.findByText('Gate flow capture is unavailable')).toBeInTheDocument()
    expect(screen.getByText(/AJS_ACCOUNT_LOGIN is not 1/)).toBeInTheDocument()
  })

  it('surfaces a load error and retries into the empty ring', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'getLabFlows')
      .mockRejectedValueOnce(new Error('gate ring offline'))
      .mockResolvedValue({ enabled: true, flows: [], dropped: 0 })
    render(<App />)
    await screen.findByRole('heading', { name: 'Requests', level: 1 })

    expect(await screen.findByText('Could not load this view')).toBeInTheDocument()
    expect(screen.getByText('gate ring offline')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('No finished gate flows yet')).toBeInTheDocument()
  })

  it('never renders a full URL or a credential in the table', async () => {
    const table = await openRequests()

    expect(table.textContent).not.toMatch(/https?:\/\//)
    expect(table.textContent).not.toMatch(/password|bearer|authorization/i)
    expect(document.body.textContent).not.toMatch(/https?:\/\//)
  })
})
