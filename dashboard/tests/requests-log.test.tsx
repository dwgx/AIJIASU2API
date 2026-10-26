import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'

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

/**
 * The durable log is its own surface: it must render from `/v1/requests` even
 * when the in-memory lab ring is disabled, and it must never read the ring.
 */
describe('durable request log', () => {
  it('reads /v1/requests with limit and offset and never calls the lab ring', async () => {
    const list = vi.spyOn(api, 'listRequests')
    const lab = vi.spyOn(api, 'getLabFlows')
    const table = await openRequests()

    expect(list).toHaveBeenCalledWith({ limit: 50, offset: 0 })
    expect(lab).not.toHaveBeenCalled()
    expect(within(table).getAllByRole('row').length).toBeGreaterThan(1)
    expect(screen.queryByText('Gate flow capture is unavailable')).toBeNull()
  })

  it('keeps rendering stored rows when lab telemetry is disabled', async () => {
    vi.spyOn(api, 'getLabFlows').mockResolvedValue({
      enabled: false,
      reason: 'AJS_ACCOUNT_LOGIN is not 1',
      flows: [],
      dropped: 0,
    })
    const table = await openRequests()

    expect(within(table).getAllByRole('row').length).toBeGreaterThan(1)
    expect(screen.queryByText('Gate flow capture is unavailable')).toBeNull()
    expect(screen.queryByText(/AJS_ACCOUNT_LOGIN is not 1/)).toBeNull()
  })

  it('moves through the log by offset instead of slicing a lab ring', async () => {
    const user = userEvent.setup()
    const stored = await api.listRequests({ limit: 25, offset: 0 })
    expect(stored).toHaveLength(25)
    const list = vi.spyOn(api, 'listRequests').mockImplementation(async (query = {}) =>
      (query.offset ?? 0) === 0 ? stored : [],
    )
    const lab = vi.spyOn(api, 'getLabFlows')
    const table = await openRequests()

    await user.click(screen.getByRole('combobox', { name: 'Rows per page' }))
    await user.click(screen.getByRole('option', { name: '25' }))
    await waitFor(() => expect(list).toHaveBeenLastCalledWith({ limit: 25, offset: 0 }))
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(26))

    await user.click(screen.getByRole('button', { name: 'Next page' }))
    await waitFor(() => expect(list).toHaveBeenLastCalledWith({ limit: 25, offset: 25 }))
    expect(await screen.findByText('No records on this page')).toBeInTheDocument()
    expect(lab).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Load the previous page' }))
    await waitFor(() => expect(list).toHaveBeenLastCalledWith({ limit: 25, offset: 0 }))
  })
})
