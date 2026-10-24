import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'

const SECTION_NAMES = [
  'General',
  'Appearance',
  'Language',
  'Connection',
  'Leases',
  'Updates',
  'Integrations',
  'Audit',
]

async function openSettings() {
  window.history.pushState({}, '', '/settings')
  render(<App />)
  await screen.findByRole('heading', { name: 'Settings', level: 1 })
  const nav = await screen.findByRole('navigation', { name: 'Settings sections' })
  // Sections render after the settings request resolves.
  await screen.findByRole('heading', { level: 2, name: 'General' })
  return nav
}

function section(id: string): HTMLElement {
  const element = document.getElementById(id)
  if (!element) throw new Error(`settings section #${id} is not mounted`)
  return element
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

describe('settings sections', () => {
  it('renders a left rail with the eight sections and their headers', async () => {
    const nav = await openSettings()

    const links = within(nav).getAllByRole('link')
    expect(links.map((link) => link.textContent)).toEqual(SECTION_NAMES)

    for (const name of SECTION_NAMES) {
      expect(screen.getByRole('heading', { level: 2, name })).toBeInTheDocument()
    }
  })

  it('marks the current section and follows a rail click', async () => {
    const user = userEvent.setup()
    const nav = await openSettings()

    const general = within(nav).getByRole('link', { name: 'General' })
    const connection = within(nav).getByRole('link', { name: 'Connection' })
    expect(general).toHaveAttribute('aria-current', 'location')

    await user.click(connection)

    expect(connection).toHaveAttribute('aria-current', 'location')
    expect(general).not.toHaveAttribute('aria-current')
    expect(window.location.hash).toBe('#connection')
  })

  it('disables stored-only controls and states why', async () => {
    await openSettings()

    const stored = document.querySelectorAll('[data-effective="stored"]')
    expect(stored).toHaveLength(3)
    for (const badge of stored) {
      expect(badge.getAttribute('data-reason')?.length ?? 0).toBeGreaterThan(20)
    }

    const future = document.querySelectorAll('[data-effective="later"]')
    expect(future.length).toBeGreaterThan(0)
    for (const badge of future) {
      expect(badge.getAttribute('data-reason')?.length ?? 0).toBeGreaterThan(20)
    }

    const connection = section('connection')
    expect(within(connection).getAllByText('Stored only')).toHaveLength(2)
    expect(within(connection).getByLabelText('Interval')).toBeDisabled()
    const transport = within(connection).getByRole('group', { name: 'Transport' })
    for (const button of within(transport).getAllByRole('button')) {
      expect(button).toBeDisabled()
    }

    const leases = section('leases')
    expect(within(leases).getByLabelText('Refresh interval')).toBeDisabled()
    expect(within(leases).getByText('Stored only')).toHaveAttribute(
      'data-effective',
      'stored',
    )
  })

  it('tracks unsaved changes, saves, and resets to the saved baseline', async () => {
    const user = userEvent.setup()
    const nav = await openSettings()
    const save = screen.getByRole('button', { name: 'Save changes' })
    expect(save).toBeDisabled()

    const appearance = section('appearance')
    await user.click(within(appearance).getByRole('button', { name: 'Light' }))

    expect(save).toBeEnabled()
    expect(screen.getByText('1 unsaved')).toBeInTheDocument()
    expect(within(nav).getByRole('link', { name: 'Appearance' })).toHaveAttribute(
      'data-dirty',
      'true',
    )

    await user.click(save)
    expect(await screen.findByText('Settings saved')).toBeInTheDocument()
    await waitFor(() => expect(save).toBeDisabled())
    expect(JSON.parse(localStorage.getItem('ajs.settings') ?? '{}').theme).toBe('light')

    await user.click(within(appearance).getByRole('button', { name: 'Dark' }))
    await waitFor(() => expect(save).toBeEnabled())

    await user.click(screen.getByRole('button', { name: 'Reset changes' }))

    expect(document.documentElement.dataset.theme).toBe('light')
    await waitFor(() => expect(save).toBeDisabled())
    expect(screen.queryByText('1 unsaved')).not.toBeInTheDocument()
  })

  it('keeps integrations read-only and points at the audit trail', async () => {
    await openSettings()

    const integrations = section('integrations')
    expect(within(integrations).getAllByText('Not configured')).toHaveLength(2)
    expect(within(integrations).getByText('Coming later')).toBeInTheDocument()
    expect(within(integrations).queryByRole('button')).not.toBeInTheDocument()
    expect(within(integrations).getByText('docs/PANEL.md')).toBeInTheDocument()

    const audit = section('audit')
    expect(
      within(audit).getByText('Last 1,000 events, bounded by the daemon'),
    ).toBeInTheDocument()
    expect(within(audit).getByText('Last 10,000 rows')).toBeInTheDocument()
    expect(within(audit).getByRole('link', { name: 'Open Logs' })).toHaveAttribute(
      'href',
      '/logs',
    )
  })

  it('translates the rail and section headers in Chinese', async () => {
    const user = userEvent.setup()
    localStorage.setItem('ajs.lang', 'zh')
    window.history.pushState({}, '', '/settings')
    render(<App />)

    await screen.findByRole('heading', { name: '设置', level: 1 })
    const nav = await screen.findByRole('navigation', { name: '设置分区' })
    expect(within(nav).getByRole('link', { name: '通用' })).toBeInTheDocument()
    expect(within(nav).getByRole('link', { name: '连接' })).toBeInTheDocument()

    await waitFor(() => expect(document.getElementById('language')).not.toBeNull())
    const language = section('language')
    await user.click(within(language).getByRole('button', { name: 'English' }))

    await screen.findByRole('heading', { name: 'Settings', level: 1 })
    const englishNav = screen.getByRole('navigation', { name: 'Settings sections' })
    expect(within(englishNav).getByRole('link', { name: 'Connection' })).toBeInTheDocument()
  })
})
