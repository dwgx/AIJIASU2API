import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import type { PanelUser } from '../src/lib/types'

const IP_PATTERN = /^\d{1,3}(\.\d{1,3}){3}$/

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

describe('leases', () => {
  it('copies a proxy URL and confirms with a toast', async () => {
    const user = userEvent.setup()
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    window.history.pushState({}, '', '/leases')
    render(<App />)

    await screen.findByRole('heading', { name: 'Leases', level: 1 })
    const copyButtons = await screen.findAllByRole('button', { name: /Copy proxy URL for/ })
    await user.click(copyButtons[0])

    expect(await screen.findByText('Proxy URL copied')).toBeInTheDocument()
    expect(writeText).toHaveBeenCalledWith(expect.stringMatching(/^socks5:\/\/127\.0\.0\.1:\d+#/))
  })

  it('rotates a lease to a new egress IP', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/leases')
    render(<App />)

    await screen.findByRole('heading', { name: 'Leases', level: 1 })
    const rotateButtons = await screen.findAllByRole('button', { name: 'Rotate lease for ada.ops' })
    const row = rotateButtons[0].closest('tr')
    expect(row).not.toBeNull()
    const before = within(row as HTMLElement).getByText(IP_PATTERN).textContent

    await user.click(rotateButtons[0])

    await waitFor(() => {
      expect(within(row as HTMLElement).queryByText(before as string)).not.toBeInTheDocument()
    })
    expect(await screen.findByText(/Lease rotated/)).toBeInTheDocument()
  })

  it('releases a lease after confirmation', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/leases')
    render(<App />)

    await screen.findByRole('heading', { name: 'Leases', level: 1 })
    const releaseButtons = await screen.findAllByRole('button', { name: 'Release lease for ada.ops' })
    const row = releaseButtons[0].closest('tr')
    expect(row).not.toBeNull()
    const before = within(row as HTMLElement).getByText(IP_PATTERN).textContent

    await user.click(releaseButtons[0])
    const dialog = await screen.findByRole('dialog', { name: 'Release lease?' })
    await user.click(within(dialog).getByRole('button', { name: 'Release lease' }))

    expect(await screen.findByText('Lease released')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText(before as string)).not.toBeInTheDocument()
    })
  })
})

describe('accounts', () => {
  it('creates an account from the modal', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/accounts?new=1')
    render(<App />)

    const modal = await screen.findByRole('dialog', { name: 'Add account' })
    await user.type(within(modal).getByLabelText('Label'), 'Test Account')
    await user.type(within(modal).getByLabelText('Username'), 'demo-test')
    await user.type(within(modal).getByLabelText('Password'), 'placeholder-password')
    await user.click(within(modal).getByRole('button', { name: 'Create account' }))

    expect(await screen.findByText('Account Test Account created')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getAllByText('Test Account').length).toBeGreaterThan(0)
    })
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: 'Add account' })).not.toBeInTheDocument()
    })
  })

  it('selects all visible accounts and pauses them in bulk', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/accounts')
    render(<App />)

    await screen.findByRole('heading', { name: 'Accounts', level: 1 })
    await user.click(await screen.findByRole('checkbox', { name: 'Select all visible accounts' }))
    expect(await screen.findByText('6 selected')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Pause' }))
    expect(await screen.findByText('6 accounts paused')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('6 selected')).not.toBeInTheDocument()
    })
  })
})

describe('users', () => {
  it('waits for users before opening the key modal and defaults to the first user', async () => {
    const user = userEvent.setup()
    const originalListUsers = api.listUsers.bind(api)
    const deferred = Promise.withResolvers<PanelUser[]>()
    vi.spyOn(api, 'listUsers').mockReturnValue(deferred.promise)
    window.history.pushState({}, '', '/users')
    render(<App />)

    const button = await screen.findByRole('button', { name: 'Create API key' })
    expect(button).toBeDisabled()

    deferred.resolve(await originalListUsers())
    await waitFor(() => expect(button).toBeEnabled())

    await user.click(button)
    const dialog = await screen.findByRole('dialog', { name: 'Create API key' })
    expect(within(dialog).getByRole('combobox', { name: 'User' })).toHaveTextContent('ada.ops')
  })

  it('creates an API key and shows the secret once', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/users')
    render(<App />)

    await screen.findByRole('heading', { name: 'Users', level: 1 })
    const createButton = screen.getByRole('button', { name: 'Create API key' })
    await waitFor(() => expect(createButton).toBeEnabled())
    await user.click(createButton)
    const dialog = await screen.findByRole('dialog', { name: 'Create API key' })
    await user.type(within(dialog).getByLabelText('Label'), 'Test key')
    await user.click(within(dialog).getByRole('button', { name: 'Create key' }))

    expect(await screen.findByRole('dialog', { name: 'API key created' })).toBeInTheDocument()
    expect(screen.getByText(/ajs_live_mock_[0-9a-f]{20,}/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Done' }))
    expect(await screen.findByText('Test key')).toBeInTheDocument()
  })
})

describe('mobile table mode', () => {
  it('renders accounts as cards below 760px', async () => {
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
    window.history.pushState({}, '', '/accounts')
    render(<App />)

    const list = await screen.findByRole('list', { name: 'Vendor accounts' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(6)
    expect(screen.queryByRole('table', { name: 'Vendor accounts' })).not.toBeInTheDocument()
    expect(within(list).getByRole('button', { name: 'Test login for Northwind Ops' })).toBeInTheDocument()
  })
})

describe('requests', () => {
  it('adds a labeled mutation-lab row with a host-only target', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/requests')
    render(<App />)

    await screen.findByRole('heading', { name: 'Requests', level: 1 })
    await user.click(await screen.findByRole('button', { name: 'Open mutation lab' }))
    const drawer = await screen.findByRole('dialog', { name: 'Mutation lab' })
    const target = within(drawer).getByLabelText('Target host:port')
    await user.clear(target)
    await user.type(target, 'https://api.example.com/v1/status')
    await user.click(within(drawer).getByRole('button', { name: 'Send request' }))

    expect(await screen.findByText(/Mutation lab request completed/)).toBeInTheDocument()
    const table = await screen.findByRole('table', { name: 'Finished gate flows' })
    await waitFor(() => {
      expect(within(table).getAllByText('mutation lab').length).toBeGreaterThan(0)
    })
    expect(within(table).getAllByText('api.example.com:443').length).toBeGreaterThan(0)
    expect(within(table).queryByText('https://api.example.com/v1/status')).not.toBeInTheDocument()
  })
})
