import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { CodeBlock } from '../src/components/docs/CodeBlock'
import { customerOperations } from '../src/components/docs/select'
import { ToastProvider } from '../src/components/Toast'
import { LanguageProvider } from '../src/lib/i18n'

beforeEach(() => {
  localStorage.clear()
  document.documentElement.lang = ''
  window.history.pushState({}, '', '/docs')
  vi.stubEnv('VITE_CONSOLE_URL', '')
})

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('public docs surface', () => {
  it('renders the overview with no token gate and no admin chrome', async () => {
    render(<App />)

    expect(
      await screen.findByRole('heading', { name: 'Overview and quickstart', level: 1 }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByRole('complementary', { name: 'Primary navigation' })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Console' })).toBeNull()
    expect(
      screen.getAllByText(/curl -sS http:\/\/127\.0\.0\.1:2261\/healthz/).length,
    ).toBeGreaterThan(0)
  })

  it('renders the customer-callable reference grouped by tag', async () => {
    const user = userEvent.setup()
    render(<App />)

    await screen.findByRole('heading', { name: 'Overview and quickstart', level: 1 })
    await user.click(screen.getByRole('link', { name: 'All endpoints' }))

    expect(await screen.findByRole('heading', { name: 'API reference', level: 1 })).toBeInTheDocument()
    expect(document.querySelectorAll('.docs-endpoint')).toHaveLength(customerOperations.length)
    expect(customerOperations).toHaveLength(56)

    const nodes = document.getElementById('listNodes')
    expect(nodes).not.toBeNull()
    expect(within(nodes as HTMLElement).getByText('GET')).toBeInTheDocument()
    expect(within(nodes as HTMLElement).getByText('/v1/nodes')).toBeInTheDocument()
    expect(within(nodes as HTMLElement).getByText('SCOPED catalog:read')).toBeInTheDocument()
    expect(screen.getByText('56 customer-callable')).toBeInTheDocument()
    expect(screen.getByText('92 operator-only')).toBeInTheDocument()
    expect(screen.getByText('41 closed omitted')).toBeInTheDocument()
  })

  it('opens search with Ctrl+K and finds a lease endpoint', async () => {
    const user = userEvent.setup()
    render(<App />)

    await screen.findByRole('heading', { name: 'Overview and quickstart', level: 1 })
    await user.keyboard('{Control>}k{/Control}')

    const dialog = await screen.findByRole('dialog', { name: 'Search documentation' })
    const input = within(dialog).getByRole('combobox', {
      name: 'Search pages and endpoints…',
    })
    await user.type(input, 'leases')

    expect(
      await within(dialog).findByRole('option', { name: /^GET \/v1\/leases Endpoint/ }),
    ).toBeInTheDocument()

    await user.keyboard('{Enter}')
    expect(await screen.findByRole('heading', { name: 'API reference', level: 1 })).toBeInTheDocument()
    expect(screen.queryByRole('dialog', { name: 'Search documentation' })).toBeNull()
  })

  it('toggles a docs-local theme without touching the console theme', async () => {
    localStorage.setItem('ajs.theme', 'light')
    const user = userEvent.setup()
    render(<App />)

    await screen.findByRole('heading', { name: 'Overview and quickstart', level: 1 })
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe('light'))

    const root = document.querySelector('.docs-root')
    expect(root).toHaveAttribute('data-docs-theme', 'light')

    await user.click(screen.getByRole('button', { name: 'Switch to dark theme' }))

    expect(root).toHaveAttribute('data-docs-theme', 'dark')
    expect(localStorage.getItem('ajs.docs.theme')).toBe('dark')
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  it('copies the active code block', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText, readText: vi.fn().mockResolvedValue('') },
    })
    render(<App />)

    await screen.findByRole('heading', { name: 'Overview and quickstart', level: 1 })
    fireEvent.click(screen.getAllByRole('button', { name: 'Copy code' })[0])

    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        expect.stringContaining('curl -sS http://127.0.0.1:2261/healthz'),
      ),
    )
  })

  it('shows a docs 404 with a link back for an unknown slug', async () => {
    window.history.pushState({}, '', '/docs/not-a-page')
    render(<App />)

    expect(await screen.findByRole('heading', { name: 'Page not found', level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to overview' })).toHaveAttribute('href', '/docs')
  })

  it('links to the console only when VITE_CONSOLE_URL is configured', async () => {
    vi.stubEnv('VITE_CONSOLE_URL', 'https://console.example.test')
    render(<App />)

    await screen.findByRole('heading', { name: 'Overview and quickstart', level: 1 })
    expect(screen.getByRole('link', { name: 'Console' })).toHaveAttribute(
      'href',
      'https://console.example.test',
    )
  })

  it('switches the docs language without leaving the page', async () => {
    const user = userEvent.setup()
    render(<App />)

    await screen.findByRole('heading', { name: 'Overview and quickstart', level: 1 })
    await user.click(screen.getByRole('button', { name: '中' }))

    expect(await screen.findByRole('heading', { name: '概览与快速上手', level: 1 })).toBeInTheDocument()
    await waitFor(() => expect(document.documentElement.lang).toBe('zh-CN'))
    expect(localStorage.getItem('ajs.lang')).toBe('zh')
  })
})

describe('docs code plate', () => {
  it('switches the curl/Go/TS tabs and persists the choice', async () => {
    const user = userEvent.setup()
    render(
      <LanguageProvider>
        <ToastProvider>
          <CodeBlock
            code="curl example"
            tabs={[
              { id: 'curl', label: 'curl', code: 'curl example' },
              { id: 'go', label: 'Go', code: 'go example' },
              { id: 'ts', label: 'TS', code: 'ts example' },
            ]}
          />
        </ToastProvider>
      </LanguageProvider>,
    )

    expect(screen.getByText('curl example')).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: 'Go' }))
    expect(screen.getByText('go example')).toBeInTheDocument()
    expect(localStorage.getItem('ajs.docs.codeLang')).toBe('go')
  })
})
