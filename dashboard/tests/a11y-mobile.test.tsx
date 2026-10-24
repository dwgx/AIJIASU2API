import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import appCss from '../src/styles/app.css?raw'
import accountsIo from '../src/components/accounts/accounts-io.css?raw'

/**
 * AJS-254 — accessibility and mobile regressions that the CDP audit at
 * 390x844 / 1440x900 turned up. The audit itself needs a real layout engine,
 * so these tests pin the parts jsdom can see: accessible names, the focusable
 * scroll region around every table, the compact row-card path that replaces
 * the table below 760px, and the CSS contract for touch targets, focus rings
 * and the overflow guard on the tab strip.
 *
 * AJS-259 — residual findings: `<html lang>` follows the UI language, the
 * import dialog's table lane is a labelled focusable region, and the tab strip
 * is a single tab stop that moves focus and selection with Arrow/Home/End.
 */

const TOUCH_BLOCK = /@media \(max-width: 760px\), \(pointer: coarse\) \{([\s\S]*?)\n\}/

function touchTargets(): string {
  const match = appCss.match(TOUCH_BLOCK)
  if (!match) throw new Error('app.css lost the touch-target media block')
  return match[1]
}

/** jsdom has no layout; compact mode is driven by matchMedia in the app. */
function mockViewport(compact: boolean): () => void {
  const original = window.matchMedia
  window.matchMedia = ((query: string) => ({
    matches: compact && query.includes('max-width: 760px'),
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
  return () => {
    window.matchMedia = original
  }
}

async function renderAt(path: string) {
  window.history.pushState({}, '', path)
  const utils = render(<App />)
  await screen.findByRole('heading', { level: 1 })
  return utils
}

/** Minimal `ajs-accounts` export the import dialog will preview. */
function importDocument(labels: string[]): string {
  return JSON.stringify({
    format: 'ajs-accounts',
    version: 1,
    exportedAt: '2026-10-24T09:00:00Z',
    accounts: labels.map((label) => ({
      label,
      username: label.toLowerCase().replace(/\s+/g, '-'),
      password: 'fake-pass-123',
      platform: 'android',
      concurrency: 1,
      regions: [],
      state: 'active',
    })),
  })
}

beforeEach(async () => {
  localStorage.clear()
  document.documentElement.lang = ''
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('icon-only controls', () => {
  it('gives every button an accessible name', async () => {
    await renderAt('/')

    const buttons = screen.getAllByRole('button')
    expect(buttons.length).toBeGreaterThan(0)
    for (const button of buttons) {
      expect(button).toHaveAccessibleName()
    }
  })

  it('names the shell actions that show only an icon', async () => {
    await renderAt('/')

    const iconOnly = screen
      .getAllByRole('button')
      .filter((button) => (button.textContent ?? '').trim() === '')
    expect(iconOnly.length).toBeGreaterThan(0)
    for (const button of iconOnly) {
      expect(button).toHaveAttribute('aria-label')
    }

    expect(screen.getByRole('button', { name: 'Open navigation' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Collapse sidebar' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh data' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Switch to light theme' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Search or jump to a page' })).toBeInTheDocument()
  })

  it('keeps the skip link as the first tab stop', async () => {
    const user = userEvent.setup()
    await renderAt('/')

    await user.tab()
    expect(screen.getByRole('link', { name: 'Skip to content' })).toHaveFocus()
  })
})

describe('table scroll containers', () => {
  it('renders the table in a labelled, keyboard-reachable scroll region', async () => {
    const user = userEvent.setup()
    await renderAt('/')
    await user.click(screen.getByRole('link', { name: 'Accounts' }))

    const region = await screen.findByRole('region', { name: 'Vendor accounts' })
    expect(region).toHaveClass('table-wrap')
    expect(region).toHaveAttribute('tabindex', '0')

    region.focus()
    expect(region).toHaveFocus()

    const table = within(region).getByRole('table', { name: 'Vendor accounts' })
    const headers = within(table).getAllByRole('columnheader')
    expect(headers.length).toBeGreaterThan(0)
    for (const header of headers) {
      expect(header).toHaveAttribute('scope', 'col')
    }
  })

  it('wraps the health table in the same kind of region', async () => {
    const user = userEvent.setup()
    await renderAt('/')
    await user.click(screen.getByRole('link', { name: 'Nodes' }))
    const views = await screen.findByRole('group', { name: 'Node views' })
    await user.click(within(views).getByRole('button', { name: 'Connected' }))

    const region = await screen.findByRole('region', { name: 'Node health probes' })
    expect(region).toHaveClass('health-table-wrap')
    expect(region).toHaveAttribute('tabindex', '0')
    expect(within(region).getByRole('table')).toBeInTheDocument()
  })

  it('gives the scroll lanes their own overflow and focus ring', () => {
    expect(appCss).toMatch(/\.table-wrap\s*\{[^}]*overflow-x:\s*auto/)
    expect(appCss).toMatch(/\.health-table-wrap\s*\{[^}]*overflow-x:\s*auto/)
    expect(appCss).toMatch(
      /\.table-wrap:focus-visible,\s*\n\.health-table-wrap:focus-visible\s*\{[^}]*outline:\s*2px solid var\(--mint\)[^}]*outline-offset:\s*-2px/,
    )
  })
})

describe('compact viewport (390px)', () => {
  let restoreViewport: () => void

  beforeEach(() => {
    restoreViewport = mockViewport(true)
  })

  afterEach(() => {
    restoreViewport()
  })

  it('replaces the table with row cards that keep every header as a term', async () => {
    const user = userEvent.setup()
    await renderAt('/')
    await user.click(screen.getByRole('link', { name: 'Accounts' }))

    const list = await screen.findByRole('list', { name: 'Vendor accounts' })
    expect(list).toHaveClass('row-cards')
    expect(within(list).getAllByRole('listitem').length).toBeGreaterThan(0)
    expect(screen.queryByRole('table')).not.toBeInTheDocument()

    const terms = Array.from(list.querySelectorAll('dt')).map((term) => term.textContent?.trim())
    expect(terms).toContain('Platform')
    expect(terms).toContain('State')
  })

  it('leaves no table wrapper behind on the card path', async () => {
    const user = userEvent.setup()
    await renderAt('/')
    await user.click(screen.getByRole('link', { name: 'Accounts' }))
    await screen.findByRole('list', { name: 'Vendor accounts' })

    expect(document.querySelector('.table-wrap')).toBeNull()
  })
})

describe('touch targets', () => {
  it('raises every control to a 40px hit area on narrow or coarse pointers', () => {
    const block = touchTargets()

    expect(block).toMatch(/\.btn,\s*\n\s*\.btn--sm\s*\{\s*\n\s*height:\s*40px;\s*\n\s*min-width:\s*40px;/)
    expect(block).toMatch(/\.icon-btn,\s*\n\s*\.icon-btn--sm\s*\{\s*\n\s*width:\s*40px;\s*\n\s*height:\s*40px;/)
    expect(block).toMatch(/\.input,\s*\n\s*\.sel \.sel__trigger\s*\{\s*\n\s*height:\s*40px;/)
    expect(block).toMatch(/\.nav-item\s*\{\s*\n\s*height:\s*40px;/)
    expect(block).toMatch(/\.lang-switch__item\s*\{\s*\n\s*min-width:\s*40px;\s*\n\s*height:\s*40px;/)
    expect(block).toMatch(/\.segmented__item\s*\{[^}]*height:\s*40px;[^}]*min-width:\s*40px;/)
    expect(block).toMatch(/\.tabs__item\s*\{\s*\n\s*height:\s*40px;/)
    expect(block).toMatch(/\.skip-link\s*\{[^}]*min-height:\s*40px;/)
  })

  it('grows the toggle into a 44x40 box without moving the track', () => {
    const block = touchTargets()
    expect(block).toMatch(
      /\.switch,\s*\n\s*\.switch\.is-on\s*\{\s*\n\s*width:\s*44px;\s*\n\s*height:\s*40px;[^}]*background:\s*transparent;/,
    )
    expect(block).toMatch(/\.switch::before\s*\{[^}]*inset:\s*10px 0;[^}]*border-radius:\s*999px;/)
    expect(appCss).toMatch(/\.switch\s*\{[^}]*width:\s*34px;[^}]*height:\s*20px;/)
  })

  it('sizes popover options and swatch chips for touch too', () => {
    const block = touchTargets()
    expect(block).toMatch(/\.sel__opt\s*\{\s*\n\s*min-height:\s*40px;/)
    expect(block).toMatch(/\.accent-picker__item\s*\{\s*\n\s*height:\s*40px;/)
    expect(block).toMatch(/\.link\s*\{[^}]*min-height:\s*40px;/)
  })

  it('lets row action groups wrap instead of pushing the card wide', () => {
    expect(touchTargets()).toMatch(/\.row-card__actions\s*\{\s*\n\s*flex-wrap:\s*wrap;/)
  })
})

describe('horizontal overflow guards', () => {
  it('scrolls the tab strip inside its own lane', () => {
    expect(appCss).toMatch(/\.tabs\s*\{[^}]*overflow-x:\s*auto/)
    expect(appCss).toMatch(/\.tabs__item\s*\{\s*\n\s*flex:\s*none;/)
  })

  it('renders the lab tab set through that guarded strip', async () => {
    const user = userEvent.setup()
    await renderAt('/')
    await user.click(screen.getByRole('link', { name: 'Lab' }))

    const tablist = await screen.findByRole('tablist')
    expect(tablist).toHaveClass('tabs')
    expect(within(tablist).getAllByRole('tab').length).toBeGreaterThan(1)
  })

  it('keeps the page shell inside a full-width column', () => {
    expect(appCss).toMatch(/\.page\s*\{[^}]*width:\s*100%;/)
    expect(appCss).toMatch(/\.page\s*\{[^}]*max-width:\s*1480px;/)
  })
})

describe('focus visibility', () => {
  it('keeps a 2px mint ring on every focusable control', () => {
    expect(appCss).toMatch(/:focus-visible\s*\{\s*\n\s*outline:\s*2px solid var\(--mint\);\s*\n\s*outline-offset:\s*2px;/)
  })

  it('pulls the ring inside clipped containers', () => {
    expect(appCss).toMatch(/\.tabs__item:focus-visible\s*\{\s*\n\s*outline-offset:\s*-2px;/)
    expect(appCss).toMatch(
      /\.table-wrap:focus-visible,\s*\n\.health-table-wrap:focus-visible\s*\{[^}]*outline-offset:\s*-2px/,
    )
  })
})

describe('document language', () => {
  it('mirrors the chosen UI language onto <html lang>', async () => {
    const user = userEvent.setup()
    await renderAt('/')

    const sidebar = screen.getByRole('complementary', { name: 'Primary navigation' })
    expect(document.documentElement.lang).toBe('en')

    await user.click(within(sidebar).getByRole('button', { name: '中' }))
    await waitFor(() => expect(document.documentElement.lang).toBe('zh-CN'))

    await user.click(within(sidebar).getByRole('button', { name: 'EN' }))
    await waitFor(() => expect(document.documentElement.lang).toBe('en'))
  })
})

describe('import dialog table region', () => {
  it('wraps the preview table in a labelled, focusable scroll region', async () => {
    const user = userEvent.setup()
    await renderAt('/')
    await user.click(screen.getByRole('link', { name: 'Accounts' }))
    await screen.findByRole('table', { name: 'Vendor accounts' })

    await user.click(screen.getByRole('button', { name: 'Import' }))
    const dialog = await screen.findByRole('dialog', { name: 'Import accounts' })

    const preview = within(dialog).getByRole('button', { name: 'Preview' })
    await user.upload(
      within(dialog).getByLabelText('File'),
      new File([importDocument(['Region Alpha'])], 'accounts.json', { type: 'application/json' }),
    )
    await waitFor(() => expect(preview).toBeEnabled())
    await user.click(preview)

    const region = await within(dialog).findByRole('region', { name: 'Import preview' })
    expect(region).toHaveClass('account-io-table-wrap')
    expect(region).toHaveAttribute('tabindex', '0')
    region.focus()
    expect(region).toHaveFocus()
    expect(within(region).getByRole('table', { name: 'Import preview' })).toBeInTheDocument()

    expect(accountsIo).toMatch(
      /\.account-io-table-wrap:focus-visible\s*\{[^}]*outline:\s*2px solid var\(--mint\)[^}]*outline-offset:\s*-2px/,
    )
  })
})

describe('tab strip keyboard support', () => {
  it('keeps one tab stop and moves focus plus selection with arrows, Home and End', async () => {
    const user = userEvent.setup()
    await renderAt('/')
    await user.click(screen.getByRole('link', { name: 'Lab' }))

    const tablist = await screen.findByRole('tablist')
    const tabs = within(tablist).getAllByRole('tab')
    expect(tabs.length).toBeGreaterThan(1)

    const active = tabs.filter((tab) => tab.getAttribute('aria-selected') === 'true')
    expect(active).toHaveLength(1)
    expect(active[0]).toHaveAttribute('tabindex', '0')
    for (const tab of tabs) {
      if (tab !== active[0]) expect(tab).toHaveAttribute('tabindex', '-1')
    }

    await user.click(active[0])
    await user.keyboard('{ArrowRight}')
    expect(tabs[1]).toHaveFocus()
    expect(tabs[1]).toHaveAttribute('aria-selected', 'true')
    expect(tabs[1]).toHaveAttribute('tabindex', '0')
    expect(tabs[0]).toHaveAttribute('tabindex', '-1')

    await user.keyboard('{ArrowLeft}')
    expect(tabs[0]).toHaveFocus()
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true')

    await user.keyboard('{ArrowLeft}')
    expect(tabs[tabs.length - 1]).toHaveFocus()
    expect(tabs[tabs.length - 1]).toHaveAttribute('aria-selected', 'true')

    await user.keyboard('{Home}')
    expect(tabs[0]).toHaveFocus()
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true')

    await user.keyboard('{End}')
    expect(tabs[tabs.length - 1]).toHaveFocus()
    expect(tabs[tabs.length - 1]).toHaveAttribute('aria-selected', 'true')
  })
})
