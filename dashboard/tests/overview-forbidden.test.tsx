import { render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import { ApiError } from '../src/lib/errors'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

describe('overview scoped-key degradation', () => {
  it('keeps the dashboard when only the audit read is refused', async () => {
    vi.spyOn(api, 'listAudit').mockRejectedValue(
      new ApiError(
        403,
        '403 Forbidden — scoped key is restricted to named accounts',
        'FORBIDDEN',
        'scoped_key_restricted',
      ),
    )
    render(<App />)

    // The fatal branch would replace every tile with one ErrorState; the real
    // counts stay on screen because only the Activity card is refused.
    expect(await screen.findByText('Live nodes')).toBeInTheDocument()
    expect(screen.getByText('Active leases')).toBeInTheDocument()
    expect(screen.getByText('Requests · 24h')).toBeInTheDocument()
    expect(screen.getByText('Egress · 24h')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Account health' })).toBeInTheDocument()

    const activity = screen.getByRole('heading', { name: 'Activity' }).closest('section')
    expect(activity).not.toBeNull()
    expect(
      within(activity as HTMLElement).getByText(
        'This token cannot read the audit log. A scoped key needs the audit:read scope; sign in with an admin token or use a key that has it.',
      ),
    ).toBeInTheDocument()
  })
})
