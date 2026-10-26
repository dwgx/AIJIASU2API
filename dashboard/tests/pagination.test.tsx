import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { Pagination, usePagination } from '../src/components/Pagination'
import { api } from '../src/lib/api'

const originalMatchMedia = window.matchMedia

function setCompactViewport(compact: boolean) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => ({
      matches: compact && query.includes('max-width: 760px'),
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }),
  })
}

afterEach(() => {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: originalMatchMedia,
  })
})

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

function Harness({
  total,
  initialPageSize,
  resetKey = 'filters:initial',
}: {
  total: number
  initialPageSize?: number
  resetKey?: string
}) {
  const pagination = usePagination({ total, initialPageSize, resetKey })
  const rows = Array.from({ length: total }, (_, index) => index + 1)
  return (
    <div>
      <output data-testid="slice">{pagination.slice(rows).join(',')}</output>
      <Pagination
        page={pagination.page}
        pageSize={pagination.pageSize}
        total={total}
        onPageChange={pagination.setPage}
        onPageSizeChange={pagination.setPageSize}
        label="Test pagination"
      />
    </div>
  )
}

function sliceSize() {
  const value = screen.getByTestId('slice').textContent ?? ''
  return value === '' ? 0 : value.split(',').length
}

describe('pagination footer', () => {
  it('shows x–y of z, disables edges and steps through pages', async () => {
    const user = userEvent.setup()
    render(<Harness total={120} />)

    const nav = screen.getByRole('navigation', { name: 'Test pagination' })
    expect(within(nav).getByText('1–25 of 120')).toBeInTheDocument()
    expect(within(nav).getByText('Page 1 of 5')).toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: 'First page' })).toBeDisabled()
    expect(within(nav).getByRole('button', { name: 'Previous page' })).toBeDisabled()
    expect(within(nav).getByRole('button', { name: 'Next page' })).toBeEnabled()
    expect(sliceSize()).toBe(25)

    await user.click(within(nav).getByRole('button', { name: 'Next page' }))
    expect(within(nav).getByText('26–50 of 120')).toBeInTheDocument()
    expect(within(nav).getByText('Page 2 of 5')).toBeInTheDocument()

    await user.click(within(nav).getByRole('button', { name: 'Last page' }))
    expect(within(nav).getByText('101–120 of 120')).toBeInTheDocument()
    expect(within(nav).getByText('Page 5 of 5')).toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: 'Next page' })).toBeDisabled()
    expect(within(nav).getByRole('button', { name: 'Last page' })).toBeDisabled()

    await user.click(within(nav).getByRole('button', { name: 'First page' }))
    expect(within(nav).getByText('1–25 of 120')).toBeInTheDocument()
  })

  it('changes page size through the custom Select and returns to page 1', async () => {
    const user = userEvent.setup()
    render(<Harness total={120} />)

    const nav = screen.getByRole('navigation', { name: 'Test pagination' })
    await user.click(within(nav).getByRole('button', { name: 'Next page' }))
    expect(within(nav).getByText('Page 2 of 5')).toBeInTheDocument()

    await user.click(within(nav).getByRole('combobox', { name: 'Rows per page' }))
    await user.click(screen.getByRole('option', { name: '50' }))

    expect(within(nav).getByText('1–50 of 120')).toBeInTheDocument()
    expect(within(nav).getByText('Page 1 of 3')).toBeInTheDocument()
    expect(sliceSize()).toBe(50)
  })

  it('jumps to a typed page, rejects out-of-range values and explains why', async () => {
    const user = userEvent.setup()
    render(<Harness total={120} />)

    const nav = screen.getByRole('navigation', { name: 'Test pagination' })
    const input = within(nav).getByRole('textbox', { name: 'Go to page' })

    await user.clear(input)
    await user.type(input, '4{Enter}')
    expect(within(nav).getByText('76–100 of 120')).toBeInTheDocument()
    expect(within(nav).getByText('Page 4 of 5')).toBeInTheDocument()

    await user.clear(input)
    await user.type(input, '99')
    expect(within(nav).getByText('Enter a page between 1 and 5')).toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: 'Go' })).toBeDisabled()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(within(nav).getByText('Page 4 of 5')).toBeInTheDocument()

    await user.clear(input)
    await user.type(input, '0')
    expect(within(nav).getByRole('button', { name: 'Go' })).toBeDisabled()
    expect(within(nav).getByText('Page 4 of 5')).toBeInTheDocument()
  })

  it('keeps the page on a data refresh and resets only when the filter key changes', async () => {
    const user = userEvent.setup()
    const { rerender } = render(<Harness total={120} resetKey="query:api" />)

    const nav = screen.getByRole('navigation', { name: 'Test pagination' })
    await user.click(within(nav).getByRole('button', { name: 'Next page' }))
    expect(within(nav).getByText('Page 2 of 5')).toBeInTheDocument()

    // Same filters, new data: the operator stays on page 2.
    rerender(<Harness total={120} resetKey="query:api" />)
    expect(within(nav).getByText('Page 2 of 5')).toBeInTheDocument()

    // Filter change: back to page 1.
    rerender(<Harness total={120} resetKey="query:cdn" />)
    expect(within(nav).getByText('Page 1 of 5')).toBeInTheDocument()

    // A shrinking live result clamps instead of showing an empty page.
    await user.click(within(nav).getByRole('button', { name: 'Last page' }))
    expect(within(nav).getByText('Page 5 of 5')).toBeInTheDocument()
    rerender(<Harness total={30} resetKey="query:cdn" />)
    expect(within(nav).getByText('Page 2 of 2')).toBeInTheDocument()
    expect(within(nav).getByText('26–30 of 30')).toBeInTheDocument()
  })

  it('renders no footer for an empty result set', () => {
    render(<Harness total={0} />)
    expect(screen.queryByRole('navigation', { name: 'Test pagination' })).not.toBeInTheDocument()
  })

  it('drops first/last and the visible size label in the compact layout', () => {
    setCompactViewport(true)
    render(<Harness total={120} />)

    const nav = screen.getByRole('navigation', { name: 'Test pagination' })
    expect(nav).toHaveClass('pagination--compact')
    expect(within(nav).queryByRole('button', { name: 'First page' })).not.toBeInTheDocument()
    expect(within(nav).queryByRole('button', { name: 'Last page' })).not.toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: 'Previous page' })).toBeDisabled()
    expect(within(nav).getByRole('button', { name: 'Next page' })).toBeEnabled()
    expect(within(nav).queryByText('Rows per page')).not.toBeInTheDocument()
    expect(within(nav).getByRole('combobox', { name: 'Rows per page' })).toBeInTheDocument()
    expect(within(nav).getByText('1–25 of 120')).toBeInTheDocument()
  })
})

describe('paginated pages', () => {
  it('pages the durable request log by offset and keeps page filters', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/requests')
    render(<App />)

    await screen.findByRole('heading', { name: 'Requests', level: 1 })
    const table = await screen.findByRole('table', { name: 'Request log' })
    const nav = await screen.findByRole('navigation', { name: 'Request log pagination' })

    // The mock log holds 40 stored rows; the default page is 50.
    expect(within(nav).getByText('Rows 1–40')).toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: 'Next page' })).toBeDisabled()

    await user.click(screen.getByRole('combobox', { name: 'Rows per page' }))
    await user.click(screen.getByRole('option', { name: '25' }))
    await waitFor(() => expect(within(nav).getByText('Rows 1–25')).toBeInTheDocument())
    expect(within(table).getAllByRole('row')).toHaveLength(26)

    const search = screen.getByRole('textbox', { name: 'Search this page' })
    await user.type(search, 'example')
    expect(within(nav).getByText('Rows 1–25')).toBeInTheDocument()

    await user.click(within(nav).getByRole('button', { name: 'Next page' }))
    await waitFor(() => expect(within(nav).getByText('Rows 26–40')).toBeInTheDocument())
    expect(search).toHaveValue('example')
    expect(within(nav).getByRole('button', { name: 'Next page' })).toBeDisabled()

    await user.click(screen.getByRole('combobox', { name: 'Filter by status' }))
    await user.click(screen.getByRole('option', { name: 'rejected' }))
    expect(within(nav).getByText('Rows 26–40')).toBeInTheDocument()
    expect(search).toHaveValue('example')
  })

  it('renders the Logs footer with edge navigation disabled for a single page', async () => {
    window.history.pushState({}, '', '/logs')
    render(<App />)

    await screen.findByRole('heading', { name: 'Logs', level: 1 })
    const nav = await screen.findByRole('navigation', { name: 'Logs pagination' })
    expect(within(nav).getByText('1–20 of 20')).toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: 'First page' })).toBeDisabled()
    expect(within(nav).getByRole('button', { name: 'Next page' })).toBeDisabled()
    expect(within(nav).getByRole('button', { name: 'Last page' })).toBeDisabled()
  })

  it('paginates the Lab server catalog and runtime logs', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/lab')
    render(<App />)

    await screen.findByRole('heading', { name: 'Lab', level: 1 })

    await user.click(screen.getByRole('tab', { name: 'Servers' }))
    const serversNav = await screen.findByRole('navigation', { name: 'Server catalog pagination' })
    // The default account's mock catalog has three servers, one page.
    expect(within(serversNav).getByText('1–3 of 3')).toBeInTheDocument()
    expect(within(serversNav).getByRole('button', { name: 'Next page' })).toBeDisabled()

    await user.click(screen.getByRole('tab', { name: 'Logs' }))
    const logsNav = await screen.findByRole('navigation', { name: 'Runtime logs pagination' })
    expect(within(logsNav).getByText('1–25 of 60')).toBeInTheDocument()

    await user.click(within(logsNav).getByRole('button', { name: 'Next page' }))
    expect(within(logsNav).getByText('26–50 of 60')).toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: 'Filter by log level' }))
    await user.click(screen.getByRole('option', { name: 'ERROR' }))
    await waitFor(() => expect(within(logsNav).getByText('Page 1 of 1')).toBeInTheDocument())
    expect(within(logsNav).getByText(/^1–\d+ of \d+$/)).toBeInTheDocument()
  })

  it('paginates the Lab gates and sessions tables', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/lab')
    render(<App />)

    await screen.findByRole('heading', { name: 'Lab', level: 1 })
    await user.click(screen.getByRole('tab', { name: 'Gates & sessions' }))

    const gatesNav = await screen.findByRole('navigation', { name: 'Gate counters pagination' })
    const sessionsNav = await screen.findByRole('navigation', { name: 'Vendor sessions pagination' })
    expect(within(gatesNav).getByText('1–9 of 9')).toBeInTheDocument()
    expect(within(sessionsNav).getByText('1–9 of 9')).toBeInTheDocument()
  })

  it('labels the Lab device identity pool as read-only with pending import/export', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/lab')
    render(<App />)

    await screen.findByRole('heading', { name: 'Lab', level: 1 })
    await user.click(screen.getByRole('tab', { name: 'Device' }))

    expect(await screen.findByText('UID hash (read-only)')).toBeInTheDocument()
    expect(screen.getByText('OSDID hash (read-only)')).toBeInTheDocument()
    expect(screen.getByText('CUID hash (read-only)')).toBeInTheDocument()
    expect(screen.getByText(/Custom pool import\/export is pending/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /import/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /export/i })).not.toBeInTheDocument()
  })
})
