import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import { renderArchivedApp } from './support/archived-app'
import {
  MOCK_PORTAL_INVITE_CODE,
  MOCK_PORTAL_INVITE_EMAIL,
  PORTAL_INVITE_PASSWORD_MIN,
  portalApi,
} from '../src/lib/portal-api'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', `/portal/activate?code=${MOCK_PORTAL_INVITE_CODE}`)
  await portalApi.resetMock()
})

describe('portal activation page', () => {
  it('activates an invited tenant and leaves it pending approval', async () => {
    const user = userEvent.setup()
    renderArchivedApp()

    expect(await screen.findByRole('heading', { name: 'Activate your account', level: 1 })).toBeInTheDocument()
    expect(await screen.findByLabelText('Email')).toHaveValue(MOCK_PORTAL_INVITE_EMAIL)
    expect(screen.getByLabelText('Display name')).toHaveValue('Invited Customer')

    const password = 'activation-pass-2026'
    await user.type(screen.getByLabelText('Password'), password)
    await user.type(screen.getByLabelText('Confirm password'), password)
    await user.click(screen.getByRole('button', { name: 'Activate account' }))

    expect(await screen.findByRole('heading', { name: 'Account activated', level: 1 })).toBeInTheDocument()
    expect(screen.getByText(/pending operator approval/i)).toBeInTheDocument()
    await expect(
      portalApi.login({ email: MOCK_PORTAL_INVITE_EMAIL, password }),
    ).rejects.toMatchObject({ status: 403, code: 'TENANT_PENDING' })

    // The one-time code is now dead.
    await expect(portalApi.previewInvite(MOCK_PORTAL_INVITE_CODE)).rejects.toMatchObject({
      status: 409,
      code: 'INVITE_REDEEMED',
    })
  })

  it('refuses a wrong or already-used code with one stable message', async () => {
    window.history.pushState({}, '', '/portal/activate?code=not-a-real-code')
    renderArchivedApp()
    expect(await screen.findByRole('heading', { name: 'Activate your account', level: 1 })).toBeInTheDocument()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'This invitation is invalid, expired, or already used.',
    )
  })

  it('validates the password locally before calling the daemon', async () => {
    const user = userEvent.setup()
    renderArchivedApp()
    await screen.findByRole('heading', { name: 'Activate your account', level: 1 })
    await screen.findByLabelText('Password')

    await user.type(screen.getByLabelText('Password'), 'short')
    await user.type(screen.getByLabelText('Confirm password'), 'short')
    await user.click(screen.getByRole('button', { name: 'Activate account' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      `Use at least ${PORTAL_INVITE_PASSWORD_MIN} characters.`,
    )
  })

  it('renders the activation page in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    renderArchivedApp()
    expect(await screen.findByRole('heading', { name: '激活账户', level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '激活账户' })).toBeInTheDocument()
  })
})
