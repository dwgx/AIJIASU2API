import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'

function openPalette(meta = false) {
  act(() => {
    window.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'k', ctrlKey: !meta, metaKey: meta }),
    )
  })
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

describe('app shell', () => {
  it('renders every page from the sidebar', async () => {
    const user = userEvent.setup()
    render(<App />)

    expect(await screen.findByRole('heading', { name: 'Overview', level: 1 })).toBeInTheDocument()
    const sidebar = screen.getByRole('complementary', { name: 'Primary navigation' })

    const pages: Array<[string, string]> = [
      ['Accounts', 'Accounts'],
      ['Nodes', 'Nodes'],
      ['Leases', 'Leases'],
      ['Users', 'Users'],
      ['Requests', 'Requests'],
      ['Logs', 'Logs'],
      ['Settings', 'Settings'],
      ['Overview', 'Overview'],
    ]

    for (const [link, heading] of pages) {
      await user.click(within(sidebar).getByRole('link', { name: link }))
      expect(
        await screen.findByRole('heading', { name: heading, level: 1 }, { timeout: 5_000 }),
      ).toBeInTheDocument()
    }
  })

  it('opens the command palette with Ctrl+K and navigates with the keyboard', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })

    openPalette()
    const dialog = await screen.findByRole('dialog', { name: 'Command palette' })
    const input = within(dialog).getByRole('textbox', { name: 'Command palette search' })

    await user.type(input, 'leas')
    expect(within(dialog).getByRole('option', { name: /Leases/ })).toBeInTheDocument()

    await user.keyboard('{Enter}')
    expect(await screen.findByRole('heading', { name: 'Leases', level: 1 })).toBeInTheDocument()
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).not.toBeInTheDocument()
  })

  it('closes the command palette with Escape', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })

    openPalette(true)
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeInTheDocument()

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).not.toBeInTheDocument()
  })

  it('filters the palette and shows an empty result message', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })

    openPalette()
    const dialog = await screen.findByRole('dialog', { name: 'Command palette' })
    await user.type(within(dialog).getByRole('textbox'), 'zzzzz')

    expect(within(dialog).getByText(/No pages or actions match/)).toBeInTheDocument()
  })
})

describe('environment labels', () => {
  it('marks the default build as mock and keeps the dataset reset', async () => {
    const user = userEvent.setup()
    render(<App />)

    await screen.findByRole('heading', { name: 'Overview', level: 1 })
    expect(screen.getByText('mock data', { selector: '.topbar__env' })).toBeInTheDocument()
    expect(screen.queryByText(/v0\.1\.0/)).not.toBeInTheDocument()

    await user.click(screen.getByRole('link', { name: 'Settings' }))
    await screen.findByRole('heading', { name: 'Settings', level: 1 })
    expect(screen.getByRole('button', { name: 'Reset dataset' })).toBeInTheDocument()
  })
})

describe('theme sync', () => {
  it('keeps Settings appearance in sync with the sidebar and persists the current theme', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/settings')
    render(<App />)

    const appearance = await screen.findByRole('group', { name: 'Theme' })
    expect(within(appearance).getByRole('button', { name: 'Dark' })).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', { name: 'Switch to light theme' }))
    expect(within(appearance).getByRole('button', { name: 'Light' })).toHaveAttribute('aria-pressed', 'true')
    expect(document.documentElement.dataset.theme).toBe('light')

    await user.click(screen.getByRole('button', { name: 'Switch to dark theme' }))
    expect(within(appearance).getByRole('button', { name: 'Dark' })).toHaveAttribute('aria-pressed', 'true')
    expect(document.documentElement.dataset.theme).toBe('dark')

    await user.click(screen.getByRole('button', { name: 'Switch to light theme' }))
    const save = screen.getByRole('button', { name: 'Save changes' })
    await waitFor(() => expect(save).toBeEnabled())
    await user.click(save)
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe('light'))
    expect(JSON.parse(localStorage.getItem('ajs.settings') ?? '{}').theme).toBe('light')

    await user.click(screen.getByRole('link', { name: 'Overview' }))
    await screen.findByRole('heading', { name: 'Overview', level: 1 })
    await user.click(screen.getByRole('link', { name: 'Settings' }))
    const reappeared = await screen.findByRole('group', { name: 'Theme' })
    expect(within(reappeared).getByRole('button', { name: 'Light' })).toHaveAttribute('aria-pressed', 'true')
    expect(document.documentElement.dataset.theme).toBe('light')
  })
})
