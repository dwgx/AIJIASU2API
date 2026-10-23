import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'

function openPalette() {
  act(() => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true }))
  })
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

describe('command palette data search', () => {
  it('finds an account by label, navigates and prefills the Accounts search', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })

    openPalette()
    const dialog = await screen.findByRole('dialog', { name: 'Command palette' })
    await user.type(within(dialog).getByRole('textbox', { name: 'Command palette search' }), 'Northwind')

    const option = within(dialog).getByRole('option', { name: /Northwind Ops/ })
    expect(within(option).getByText('Account')).toBeInTheDocument()

    await user.click(option)
    expect(await screen.findByRole('heading', { name: 'Accounts', level: 1 })).toBeInTheDocument()
    expect(window.location.search).toBe('?q=Northwind%20Ops')

    const search = screen.getByRole('textbox', { name: 'Search accounts' })
    expect(search).toHaveValue('Northwind Ops')

    await waitFor(() => {
      expect(screen.getByText('1 of 6')).toBeInTheDocument()
    })
    expect(screen.getByText('Northwind Ops')).toBeInTheDocument()
    expect(screen.queryByText('Field Lab')).not.toBeInTheDocument()
  })

  it('finds a node by region keyword, navigates and filters the Nodes table', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })

    openPalette()
    const dialog = await screen.findByRole('dialog', { name: 'Command palette' })
    await user.type(within(dialog).getByRole('textbox', { name: 'Command palette search' }), 'kr-sel')

    const option = within(dialog).getByRole('option', { name: /kr-sel-01/ })
    expect(within(option).getByText('Node')).toBeInTheDocument()

    await user.click(option)
    expect(await screen.findByRole('heading', { name: 'Nodes', level: 1 })).toBeInTheDocument()
    expect(window.location.search).toBe('?q=kr-sel-01')

    const search = screen.getByRole('textbox', { name: 'Search nodes' })
    expect(search).toHaveValue('kr-sel-01')

    await waitFor(() => {
      expect(screen.getByText('1 of 12')).toBeInTheDocument()
    })
    expect(screen.getByText('kr-sel-01')).toBeInTheDocument()
    expect(screen.queryByText('cn-zj-01')).not.toBeInTheDocument()
  })

  it('keeps data entries out for empty and one-character queries', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })

    openPalette()
    const dialog = await screen.findByRole('dialog', { name: 'Command palette' })
    const input = within(dialog).getByRole('textbox', { name: 'Command palette search' })

    const sections = () =>
      within(dialog)
        .getAllByRole('option')
        .map((option) => within(option).getByText(/^(Navigate|Action|Account|Node)$/).textContent)

    expect(within(dialog).getAllByRole('option').length).toBe(12)
    expect(new Set(sections())).toEqual(new Set(['Navigate', 'Action']))

    await user.type(input, 'c')
    expect(new Set(sections())).toEqual(new Set(['Navigate', 'Action']))
    expect(within(dialog).queryByRole('option', { name: /cn-zj-01/ })).not.toBeInTheDocument()
  })

  it('keeps pages and actions reachable when the data APIs fail', async () => {
    const user = userEvent.setup()
    const listAccounts = vi.spyOn(api, 'listAccounts').mockRejectedValue(new Error('accounts down'))
    const listNodes = vi.spyOn(api, 'listNodes').mockRejectedValue(new Error('nodes down'))

    render(<App />)
    await screen.findByRole('heading', { name: 'Overview', level: 1 })

    openPalette()
    const dialog = await screen.findByRole('dialog', { name: 'Command palette' })
    await user.type(within(dialog).getByRole('textbox', { name: 'Command palette search' }), 'lease')

    await waitFor(() => {
      expect(listAccounts).toHaveBeenCalled()
      expect(listNodes).toHaveBeenCalled()
    })
    expect(within(dialog).getByRole('option', { name: /Leases/ })).toBeInTheDocument()
    expect(within(dialog).queryByRole('option', { name: /Northwind Ops/ })).not.toBeInTheDocument()

    await user.clear(within(dialog).getByRole('textbox', { name: 'Command palette search' }))
    await user.type(within(dialog).getByRole('textbox', { name: 'Command palette search' }), 'zzzzz')
    expect(await within(dialog).findByText(/No pages or actions match/)).toBeInTheDocument()
  })
})

describe('search param prefill on pages', () => {
  it('Accounts updates the query when the palette navigates while it is mounted', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/accounts')
    render(<App />)
    await screen.findByRole('heading', { name: 'Accounts', level: 1 })
    expect(screen.getByRole('textbox', { name: 'Search accounts' })).toHaveValue('')

    openPalette()
    const dialog = await screen.findByRole('dialog', { name: 'Command palette' })
    await user.type(within(dialog).getByRole('textbox', { name: 'Command palette search' }), 'Field Lab')
    await user.click(within(dialog).getByRole('option', { name: /Field Lab/ }))

    expect(await screen.findByRole('heading', { name: 'Accounts', level: 1 })).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Search accounts' })).toHaveValue('Field Lab')
    })
    await waitFor(() => {
      expect(screen.getByText('1 of 6')).toBeInTheDocument()
    })
  })

  it('Nodes updates the query when the palette navigates while it is mounted', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/nodes')
    render(<App />)
    await screen.findByRole('heading', { name: 'Nodes', level: 1 })
    expect(screen.getByRole('textbox', { name: 'Search nodes' })).toHaveValue('')

    openPalette()
    const dialog = await screen.findByRole('dialog', { name: 'Command palette' })
    await user.type(within(dialog).getByRole('textbox', { name: 'Command palette search' }), 'jp-tyo')
    await user.click(within(dialog).getByRole('option', { name: /jp-tyo-01/ }))

    expect(await screen.findByRole('heading', { name: 'Nodes', level: 1 })).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Search nodes' })).toHaveValue('jp-tyo-01')
    })
    await waitFor(() => {
      expect(screen.getByText('1 of 12')).toBeInTheDocument()
    })
  })

  it('does not clobber a typed query when unrelated params change', async () => {
    const user = userEvent.setup()
    window.history.pushState({}, '', '/accounts')
    render(<App />)
    await screen.findByRole('heading', { name: 'Accounts', level: 1 })

    const search = screen.getByRole('textbox', { name: 'Search accounts' })
    await user.type(search, 'Edge')
    expect(search).toHaveValue('Edge')

    await user.click(screen.getByRole('button', { name: 'New account' }))
    expect(await screen.findByRole('dialog', { name: 'New account' })).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Search accounts' })).toHaveValue('Edge')

    await user.click(within(screen.getByRole('dialog', { name: 'New account' })).getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('textbox', { name: 'Search accounts' })).toHaveValue('Edge')
    expect(window.location.search).toBe('')
  })
})
