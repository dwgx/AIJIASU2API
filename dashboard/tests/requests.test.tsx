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
  return screen.findByRole('table', { name: 'Request log' })
}

describe('requests page', () => {
  it('renders durable rows from GET /v1/requests and a page summary', async () => {
    const list = vi.spyOn(api, 'listRequests')
    const table = await openRequests()

    expect(list).toHaveBeenCalledWith({ limit: 50, offset: 0 })
    const stored = await api.listRequests({ limit: 50, offset: 0 })
    expect(within(table).getAllByRole('row')).toHaveLength(stored.length + 1)
    expect(within(table).getAllByText('proxy').length).toBeGreaterThan(0)

    const rowsTile = screen.getByText('Rows').closest('.metric')
    expect(rowsTile).not.toBeNull()
    expect(within(rowsTile as HTMLElement).getByText(formatNumber(stored.length))).toBeInTheDocument()
    expect(table.textContent).toMatch(/:\d+/)
    expect(table.textContent).not.toMatch(/https?:\/\//)
  })

  it('filters the loaded page by status, protocol and source, then clears', async () => {
    const user = userEvent.setup()
    const table = await openRequests()

    await user.click(screen.getByRole('combobox', { name: 'Filter by status' }))
    await user.click(screen.getByRole('option', { name: 'rejected' }))
    await waitFor(() => {
      const rows = within(table).getAllByRole('row').slice(1)
      expect(rows.length).toBeGreaterThan(0)
      for (const row of rows) {
        expect(row.querySelector('.status-pill')?.textContent).not.toBe('200')
        expect(row.querySelector('.status-pill')?.textContent).not.toBe('204')
      }
    })

    await user.click(screen.getByRole('combobox', { name: 'Filter by status' }))
    await user.click(screen.getByRole('option', { name: 'All statuses' }))

    await user.click(screen.getByRole('combobox', { name: 'Filter by protocol' }))
    await user.click(screen.getByRole('option', { name: 'http' }))
    await waitFor(() => {
      const rows = within(table).getAllByRole('row').slice(1)
      expect(rows.length).toBeGreaterThan(0)
      for (const row of rows) expect(row.textContent).toContain('http')
    })

    await user.click(screen.getByRole('combobox', { name: 'Filter by protocol' }))
    await user.click(screen.getByRole('option', { name: 'All protocols' }))

    await user.click(screen.getByRole('combobox', { name: 'Filter by source' }))
    await user.click(screen.getByRole('option', { name: 'Mutation lab' }))
    expect(await screen.findByText('No rows match these filters')).toBeInTheDocument()

    await user.click(screen.getAllByRole('button', { name: 'Clear filters' })[0])
    await waitFor(() => expect(within(table).getAllByRole('row').length).toBeGreaterThan(1))
  })

  it('searches the loaded page by host and account', async () => {
    const user = userEvent.setup()
    const stored = await api.listRequests({ limit: 50, offset: 0 })
    const account = stored[0].accountLabel
    const table = await openRequests()

    await user.type(screen.getByRole('textbox', { name: 'Search this page' }), account)
    await waitFor(() => {
      const rows = within(table).getAllByRole('row').slice(1)
      expect(rows.length).toBeGreaterThan(0)
      for (const row of rows) expect(row.textContent).toContain(account)
    })
  })

  it('opens a detail drawer with the request id and a host-only target', async () => {
    const user = userEvent.setup()
    const stored = await api.listRequests({ limit: 50, offset: 0 })
    const request = stored.find((row) => row.target.includes(':') && row.status === 200)
    expect(request).toBeDefined()
    if (!request) throw new Error('mock must include an accepted request with a target')
    await openRequests()

    await user.click(screen.getByRole('button', { name: `View request ${request.id}` }))
    const drawer = await screen.findByRole('dialog', { name: 'Request detail' })

    expect(within(drawer).getByText(request.id)).toBeInTheDocument()
    expect(within(drawer).getByText(request.accountLabel)).toBeInTheDocument()
    expect(within(drawer).getByText(request.nodeLabel)).toBeInTheDocument()
    expect(within(drawer).getByText(request.region)).toBeInTheDocument()
    expect(drawer.textContent).toMatch(/:\d+/)
    expect(drawer.textContent).not.toMatch(/https?:\/\//)
  })

  it('explains an empty durable log instead of inventing rows', async () => {
    vi.spyOn(api, 'listRequests').mockResolvedValue([])
    await openRequests()

    expect(await screen.findByText('No request records yet')).toBeInTheDocument()
    expect(screen.getByText(/stores a row when a proxy or gate request completes/)).toBeInTheDocument()
  })

  it('surfaces a load error and retries into the log', async () => {
    const user = userEvent.setup()
    const real = api.listRequests.bind(api)
    vi.spyOn(api, 'listRequests')
      .mockRejectedValueOnce(new Error('request log offline'))
      .mockImplementation(real)
    render(<App />)
    await screen.findByRole('heading', { name: 'Requests', level: 1 })

    expect(await screen.findByText('Could not load this view')).toBeInTheDocument()
    expect(screen.getByText('request log offline')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('table', { name: 'Request log' })).toBeInTheDocument()
  })

  it('pages the durable log with limit and offset', async () => {
    const user = userEvent.setup()
    const list = vi.spyOn(api, 'listRequests')
    const table = await openRequests()

    await user.click(screen.getByRole('combobox', { name: 'Rows per page' }))
    await user.click(screen.getByRole('option', { name: '25' }))
    await waitFor(() => expect(list).toHaveBeenLastCalledWith({ limit: 25, offset: 0 }))
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(26))
    expect(screen.getByText('Rows 1–25')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Next page' }))
    await waitFor(() => expect(list).toHaveBeenLastCalledWith({ limit: 25, offset: 25 }))
    expect(screen.getByText('Rows 26–40')).toBeInTheDocument()
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(16))
    expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Previous page' }))
    await waitFor(() => expect(list).toHaveBeenLastCalledWith({ limit: 25, offset: 0 }))
  })

  it('never renders a full URL or a credential in the table', async () => {
    const table = await openRequests()

    expect(table.textContent).not.toMatch(/https?:\/\//)
    expect(table.textContent).not.toMatch(/password|bearer|authorization/i)
    expect(document.body.textContent).not.toMatch(/https?:\/\//)
  })
})
