import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import { renderArchivedApp } from './support/archived-app'
import { api } from '../src/lib/api'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/tenants')
  await api.resetMock()
})

describe('tenants admin — one-time invitations', () => {
  it('creates an invite and shows the activation code exactly once', async () => {
    const user = userEvent.setup()
    renderArchivedApp()

    expect(await screen.findByRole('heading', { name: 'Tenants', level: 1 })).toBeInTheDocument()
    expect(await screen.findByText('pending@example.com')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Invite tenant' }))
    const inviteDialog = await screen.findByRole('dialog', { name: 'Invite a tenant' })
    await user.type(within(inviteDialog).getByLabelText('Email'), 'new.customer@example.com')
    await user.type(within(inviteDialog).getByLabelText('Display name'), 'New Customer')
    await user.click(within(inviteDialog).getByRole('button', { name: 'Create invite' }))

    const codeDialog = await screen.findByRole('dialog', { name: 'Activation code' })
    const code = (within(codeDialog).getByLabelText('One-time activation code') as HTMLInputElement).value
    expect(code).toMatch(/^ajs_inv_mock_/)
    const link = (within(codeDialog).getByLabelText('Activation link') as HTMLInputElement).value
    expect(link).toContain('/portal/activate?code=')
    expect(link).toContain(encodeURIComponent(code))
    expect(within(codeDialog).getByText(/shown only once/i)).toBeInTheDocument()

    await user.click(within(codeDialog).getByRole('button', { name: 'I stored it' }))
    expect(await screen.findByText('new.customer@example.com')).toBeInTheDocument()
    const invites = await api.listTenantInvites()
    expect(invites[0]).toMatchObject({ email: 'new.customer@example.com', status: 'pending' })
    // Creating an invite does not create a tenant; activation does.
    expect((await api.listTenants()).some((tenant) => tenant.email === 'new.customer@example.com')).toBe(false)
  })

  it('rejects a duplicate invitation without inventing a second code', async () => {
    await api.createTenantInvite({ email: 'dup@example.com', displayName: 'First' })
    await expect(api.createTenantInvite({ email: 'DUP@example.com' })).rejects.toMatchObject({
      status: 409,
      code: 'CONFLICT',
    })
  })

  it('renders the tenant page in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    renderArchivedApp()
    expect(await screen.findByRole('heading', { name: '租户', level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '邀请租户' })).toBeInTheDocument()
    expect(screen.getByText('待审核')).toBeInTheDocument()
  })
})
