import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { UserEvent } from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import appCss from '../src/styles/app.css?raw'
import { api } from '../src/lib/api'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/accounts')
  await api.resetMock()
})

async function openAccounts() {
  render(<App />)
  await screen.findByRole('heading', { name: 'Accounts', level: 1 })
}

async function showCards(user: UserEvent) {
  await user.click(screen.getByRole('button', { name: 'Cards' }))
  return screen.findByRole('list', { name: 'Vendor accounts' })
}

describe('accounts view switch', () => {
  it('defaults to the table and switches between table and cards', async () => {
    const user = userEvent.setup()
    await openAccounts()

    expect(await screen.findByRole('table', { name: 'Vendor accounts' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Table' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Cards' })).toHaveAttribute('aria-pressed', 'false')

    const list = await showCards(user)
    expect(within(list).getAllByRole('listitem')).toHaveLength(6)
    expect(screen.queryByRole('table', { name: 'Vendor accounts' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Cards' })).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', { name: 'Table' }))
    expect(await screen.findByRole('table', { name: 'Vendor accounts' })).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Vendor accounts' })).not.toBeInTheDocument()
  })

  it('renders the card anatomy from the filtered account', async () => {
    const user = userEvent.setup()
    await openAccounts()
    const list = await showCards(user)
    const card = within(list).getAllByRole('listitem')[0]

    expect(within(card).getByText('Northwind Ops')).toBeInTheDocument()
    expect(within(card).getByText('demo-northwind')).toBeInTheDocument()
    expect(within(card).getByText('Active')).toBeInTheDocument()
    expect(within(card).getByText('android')).toBeInTheDocument()
    expect(within(card).getByText('cn-zj · cn-sh · hk')).toBeInTheDocument()
    expect(within(card).getByText('2/3')).toBeInTheDocument()
    expect(within(card).getByText('200')).toBeInTheDocument()
    expect(within(card).getByText('76.7 GiB')).toBeInTheDocument()
    expect(within(card).getByText('Expires')).toBeInTheDocument()
    expect(
      within(card).getByRole('progressbar', { name: 'Node capacity for Northwind Ops' }),
    ).toHaveAttribute('aria-valuenow', '67')
    expect(within(card).getByRole('button', { name: 'Test login for Northwind Ops' })).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Pause Northwind Ops' })).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Delete Northwind Ops' })).toBeInTheDocument()
  })

  it('persists the cards view in localStorage across remounts', async () => {
    const user = userEvent.setup()
    const first = render(<App />)
    await screen.findByRole('heading', { name: 'Accounts', level: 1 })
    await showCards(user)
    expect(localStorage.getItem('ajs.accounts.view')).toBe('cards')

    first.unmount()
    window.history.pushState({}, '', '/accounts')
    render(<App />)

    const list = await screen.findByRole('list', { name: 'Vendor accounts' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(6)
    expect(screen.getByRole('button', { name: 'Cards' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('falls back to the table for an invalid stored view', async () => {
    localStorage.setItem('ajs.accounts.view', 'grid')
    await openAccounts()

    expect(await screen.findByRole('table', { name: 'Vendor accounts' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Table' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('keeps bulk selection and card actions wired to the same API', async () => {
    const user = userEvent.setup()
    const testAccount = vi.spyOn(api, 'testAccount')
    await openAccounts()
    const list = await showCards(user)
    const card = within(list).getAllByRole('listitem')[0]

    await user.click(within(card).getByRole('checkbox', { name: 'Select Northwind Ops' }))
    expect(await screen.findByText('1 selected')).toBeInTheDocument()
    expect(card).toHaveClass('is-selected')

    await user.click(within(card).getByRole('button', { name: 'Test login for Northwind Ops' }))
    expect(await screen.findByText('Login test passed for Northwind Ops')).toBeInTheDocument()
    expect(testAccount).toHaveBeenCalledWith('acc-01')
  })

  it('renders the explicit card view on mobile without a table', async () => {
    const media = {
      matches: true,
      media: '(max-width: 760px)',
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }
    vi.spyOn(window, 'matchMedia').mockImplementation(() => media as unknown as MediaQueryList)
    localStorage.setItem('ajs.accounts.view', 'cards')
    await openAccounts()

    const list = await screen.findByRole('list', { name: 'Vendor accounts' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(6)
    expect(screen.queryByRole('table', { name: 'Vendor accounts' })).not.toBeInTheDocument()
    expect(document.querySelector('.table-wrap')).toBeNull()
    expect(list).toHaveClass('account-grid')
  })

  it('defines a 280px auto-fill grid that collapses to one column below 760px', () => {
    expect(appCss).toMatch(
      /\.account-grid\s*\{[^}]*grid-template-columns:\s*repeat\(auto-fill, minmax\(280px, 1fr\)\)[^}]*gap:\s*12px/s,
    )
    expect(appCss).toMatch(
      /@media \(max-width: 760px\)[\s\S]*?\.account-grid\s*\{[^}]*grid-template-columns:\s*1fr/s,
    )
  })
})
