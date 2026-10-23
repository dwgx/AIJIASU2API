import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import type { UpdateInfo } from '../src/lib/types'

const release: UpdateInfo = {
  enabled: true,
  current: '0.1.0',
  latest: 'v0.2.0',
  updateAvailable: true,
  url: 'https://github.com/example/ajs/releases/tag/v0.2.0',
  name: 'v0.2.0 — gate hardening',
  publishedAt: '2026-10-20T00:00:00Z',
  checkedAt: '2026-10-23T12:00:00Z',
}

async function openSettings() {
  window.history.pushState({}, '', '/settings')
  render(<App />)
  await screen.findByRole('heading', { name: 'Settings', level: 1 })
  // The update card renders after the settings draft resolves; waiting here
  // keeps every test from racing the extra runtime-label fetch.
  await screen.findByRole('button', { name: 'Check' })
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
  vi.restoreAllMocks()
})

describe('updates card', () => {
  it('does not fetch the update check on page load', async () => {
    const spy = vi.spyOn(api, 'getUpdate').mockResolvedValue(release)
    await openSettings()

    // The card renders with current facts only; nothing was fetched.
    expect(await screen.findByRole('button', { name: 'Check' })).toBeInTheDocument()
    expect(spy).not.toHaveBeenCalled()
    expect(screen.queryByText('v0.2.0')).not.toBeInTheDocument()
  })

  it('explains AJS_UPDATE_REPO when the check is disabled', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'getUpdate').mockResolvedValue({ enabled: false, current: '0.1.0' })
    await openSettings()

    await user.click(screen.getByRole('button', { name: 'Check' }))

    expect(await screen.findByText(/AJS_UPDATE_REPO/)).toBeInTheDocument()
    expect(screen.getByText(/not configured on the daemon/)).toBeInTheDocument()
    expect(screen.queryByText('Update available')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Copy deploy command/ })).not.toBeInTheDocument()
  })

  it('shows the update-available state and copies the deploy command', async () => {
    const user = userEvent.setup()
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    vi.spyOn(api, 'getUpdate').mockResolvedValue(release)
    await openSettings()

    await user.click(screen.getByRole('button', { name: 'Check' }))

    expect(await screen.findByText('Update available')).toBeInTheDocument()
    expect(screen.getByText('v0.2.0')).toBeInTheDocument()

    const copy = await screen.findByRole('button', { name: 'Copy deploy command' })
    await user.click(copy)

    expect(writeText).toHaveBeenCalledWith('cd deploy && ./update.sh --tag v0.2.0')
    expect(await screen.findByText('Deploy command copied')).toBeInTheDocument()
  })

  it('links to the release notes', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'getUpdate').mockResolvedValue(release)
    await openSettings()

    await user.click(screen.getByRole('button', { name: 'Check' }))
    const link = await screen.findByRole('link', { name: /Release notes/ })
    expect(link).toHaveAttribute('href', 'https://github.com/example/ajs/releases/tag/v0.2.0')
    expect(link).toHaveAttribute('target', '_blank')
  })

  it('reports up to date when the latest tag matches', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'getUpdate').mockResolvedValue({ ...release, latest: 'v0.1.0', updateAvailable: false })
    await openSettings()

    await user.click(screen.getByRole('button', { name: 'Check' }))

    expect(await screen.findByText('Up to date')).toBeInTheDocument()
    expect(screen.queryByText('Update available')).not.toBeInTheDocument()
  })

  it('shows the error state when the check fails', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'getUpdate').mockRejectedValue(new Error('502 Bad Gateway — upstream unavailable'))
    await openSettings()

    await user.click(screen.getByRole('button', { name: 'Check' }))

    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText(/502 Bad Gateway/)).toBeInTheDocument()
    expect(screen.queryByText('Update available')).not.toBeInTheDocument()
    expect(screen.queryByText(/AJS_UPDATE_REPO/)).not.toBeInTheDocument()

    // A later successful check clears the error.
    vi.mocked(api.getUpdate).mockResolvedValue(release)
    await user.click(screen.getByRole('button', { name: 'Check' }))
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
    expect(await screen.findByText('Update available')).toBeInTheDocument()
  })
})
