import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('sidebar collapse', () => {
  it('collapses to an icon rail and restores the stored choice', async () => {
    const user = userEvent.setup()
    const { unmount } = render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })

    const shell = document.querySelector('.app-shell')
    const sidebar = screen.getByRole('complementary', { name: 'Primary navigation' })
    expect(shell).not.toHaveClass('is-sidebar-collapsed')
    expect(screen.getByRole('button', { name: 'Collapse sidebar' })).toHaveAttribute(
      'aria-expanded',
      'true',
    )

    await user.click(screen.getByRole('button', { name: 'Collapse sidebar' }))

    expect(shell).toHaveClass('is-sidebar-collapsed')
    expect(sidebar).toHaveClass('is-collapsed')
    expect(screen.getByRole('button', { name: 'Expand sidebar' })).toHaveAttribute(
      'aria-expanded',
      'false',
    )
    expect(localStorage.getItem('ajs.sidebar')).toBe('collapsed')

    unmount()
    render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })
    expect(document.querySelector('.app-shell')).toHaveClass('is-sidebar-collapsed')
    expect(screen.getByRole('button', { name: 'Expand sidebar' })).toBeInTheDocument()
  })
})

describe('shell branding', () => {
  it('drops the workspace switcher and operator avatar while showing APC branding', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })

    expect(screen.queryByText('PW')).not.toBeInTheDocument()
    expect(screen.queryByText('Personal workspace')).not.toBeInTheDocument()
    expect(screen.queryByText('Self-use · local')).not.toBeInTheDocument()
    expect(screen.queryByText('Operator · local session')).not.toBeInTheDocument()
    expect(document.querySelector('.avatar')).toBeNull()
    expect(screen.queryByText('ajs-accountd')).not.toBeInTheDocument()
    expect(screen.queryByText(/v0\.1\.0/)).not.toBeInTheDocument()

    const sidebar = screen.getByRole('complementary', { name: 'Primary navigation' })
    expect(within(sidebar).getByText('APC', { selector: '.brand__name' })).toBeInTheDocument()
    expect(within(sidebar).getByText('AJiaSu Proxy Console')).toBeInTheDocument()
    expect(within(sidebar).getByText('powered by dwgx')).toBeInTheDocument()
  })
})

describe('add account modal', () => {
  it('opens a centered modal, fixes concurrency at one and shows the vendor note', async () => {
    const user = userEvent.setup()
    const createAccount = vi.spyOn(api, 'createAccount')
    window.history.pushState({}, '', '/accounts')
    render(<App />)
    await screen.findByRole('heading', { name: 'Accounts', level: 1 })

    await user.click(screen.getByRole('button', { name: 'Add account' }))

    const modal = await screen.findByRole('dialog', { name: 'Add account' })
    expect(modal).toHaveClass('modal')
    expect(modal.closest('.overlay--modal')).not.toBeNull()
    expect(document.querySelector('.drawer')).toBeNull()
    expect(within(modal).queryByLabelText('Concurrency')).not.toBeInTheDocument()
    expect(
      within(modal).getByText('Vendor allows one active session per account.'),
    ).toBeInTheDocument()

    await user.type(within(modal).getByLabelText('Label'), 'Fixed Concurrency')
    await user.type(within(modal).getByLabelText('Username'), 'demo-fixed')
    await user.type(within(modal).getByLabelText('Password'), 'placeholder-password')
    await user.click(within(modal).getByRole('button', { name: 'Create account' }))

    await waitFor(() => {
      expect(createAccount).toHaveBeenCalledWith(
        expect.objectContaining({ label: 'Fixed Concurrency', concurrency: 1 }),
      )
    })
  })
})
