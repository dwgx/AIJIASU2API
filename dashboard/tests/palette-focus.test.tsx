import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

async function renderApp() {
  render(<App />)
  await screen.findByRole('heading', { name: 'Overview', level: 1 })
  return screen.getByRole('button', { name: 'Search or jump to a page' })
}

describe('command palette focus restoration', () => {
  it('returns focus to the search trigger after Escape', async () => {
    const user = userEvent.setup()
    const trigger = await renderApp()

    trigger.focus()
    expect(trigger).toHaveFocus()

    await user.click(trigger)
    const dialog = await screen.findByRole('dialog', { name: 'Command palette' })
    expect(within(dialog).getByRole('textbox', { name: 'Command palette search' })).toHaveFocus()

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).not.toBeInTheDocument()
    await waitFor(() => expect(trigger).toHaveFocus())
  })

  it('falls back to the search trigger when opened from Ctrl+K with body focus', async () => {
    const user = userEvent.setup()
    const trigger = await renderApp()
    expect(document.activeElement).toBe(document.body)

    await user.keyboard('{Control>}k{/Control}')
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeInTheDocument()

    await user.keyboard('{Escape}')
    await waitFor(() => expect(trigger).toHaveFocus())
  })

  it('restores focus to the element that opened the palette', async () => {
    const user = userEvent.setup()
    await renderApp()
    const sidebar = screen.getByRole('complementary', { name: 'Primary navigation' })
    const link = within(sidebar).getByRole('link', { name: 'Leases' })

    link.focus()
    expect(link).toHaveFocus()

    await user.keyboard('{Control>}k{/Control}')
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeInTheDocument()

    await user.keyboard('{Escape}')
    await waitFor(() => expect(link).toHaveFocus())
  })
})
