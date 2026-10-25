import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { UserEvent } from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/leases')
  await api.resetMock()
})

async function renderLeases(): Promise<void> {
  render(<App />)
  await screen.findByRole('heading', { name: 'Leases', level: 1 })
  await screen.findByRole('table', { name: 'Active leases' })
}

async function chooseUser(
  user: UserEvent,
  dialog: HTMLElement,
  name: RegExp,
): Promise<void> {
  // The user picker is searchable, so its trigger is a plain button labelled
  // by aria-label; the combobox role lives on the popup's search input.
  await user.click(within(dialog).getByRole('button', { name: 'User' }))
  const listbox = await screen.findByRole('listbox', { name: 'User' })
  await user.click(within(listbox).getByRole('option', { name }))
}

describe('lease creation', () => {
  it('creates a lease from the empty state and refuses a duplicate user and platform', async () => {
    const user = userEvent.setup()
    for (const lease of await api.listLeases()) await api.releaseLease(lease.id)
    await renderLeases()

    expect(screen.getByText('No active leases')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Create lease' }))

    const dialog = await screen.findByRole('dialog', { name: 'Create lease' })
    await chooseUser(user, dialog, /build-bot/)
    await user.click(within(dialog).getByRole('button', { name: 'Create lease' }))

    expect(await screen.findByText('Lease created · build-bot on cn-zj-01')).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Create lease' })).not.toBeInTheDocument(),
    )
    const leases = await api.listLeases()
    expect(leases).toHaveLength(1)
    expect(leases[0]).toMatchObject({
      userId: 'u-02',
      platform: 'android',
      status: 'active',
      nodeId: 'node-01',
      idempotencyKey: expect.stringMatching(/^[0-9a-f-]{16,}$/i),
    })
    expect(await screen.findByText('cn-zj-01')).toBeInTheDocument()

    // A second create for the same user and platform is refused before it can
    // allocate another node; the dialog stays open with the daemon's reason.
    await user.click(screen.getByRole('button', { name: 'New lease' }))
    const duplicateDialog = await screen.findByRole('dialog', { name: 'Create lease' })
    await chooseUser(user, duplicateDialog, /build-bot/)
    await user.click(within(duplicateDialog).getByRole('button', { name: 'Create lease' }))
    expect(
      await within(duplicateDialog).findByRole('alert'),
    ).toHaveTextContent('already holds an active android lease')
    expect(await api.listLeases()).toHaveLength(1)
  })

  it('replays an idempotency key and rejects a second key for the same user and platform', async () => {
    const key = 'retry-key-lease'
    const first = await api.createLease({
      userId: 'u-02',
      platform: 'android',
      idempotencyKey: key,
    })
    const replay = await api.createLease({
      userId: 'u-02',
      platform: 'android',
      idempotencyKey: key,
    })
    expect(replay.replayed).toBe(true)
    expect(replay.lease.id).toBe(first.lease.id)
    expect(
      (await api.listLeases()).filter((lease) => lease.userId === 'u-02' && lease.platform === 'android'),
    ).toHaveLength(1)

    await expect(
      api.createLease({ userId: 'u-02', platform: 'android', idempotencyKey: 'another-key' }),
    ).rejects.toMatchObject({ status: 409, code: 'CONFLICT' })
  })
})

describe('lease revocation', () => {
  it('revokes only after the confirmation dialog is accepted', async () => {
    const user = userEvent.setup()
    await renderLeases()

    const row = screen.getByText('cn-zj-01').closest('tr')
    expect(row).not.toBeNull()
    await user.click(
      within(row as HTMLElement).getByRole('button', { name: 'Revoke lease for ada.ops' }),
    )

    const dialog = await screen.findByRole('dialog', { name: 'Revoke lease?' })
    expect(await api.listLeases()).toHaveLength(8)
    await user.click(within(dialog).getByRole('button', { name: 'Revoke lease' }))

    expect(await screen.findByText('Lease revoked')).toBeInTheDocument()
    await waitFor(async () => expect(await api.listLeases()).toHaveLength(7))
    await waitFor(() => expect(screen.queryByText('cn-zj-01')).not.toBeInTheDocument())
  })
})

describe('lease page states', () => {
  it('shows the load error and recovers on retry', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'listLeases').mockRejectedValueOnce(new Error('daemon offline'))
    render(<App />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Could not load this view')
    expect(alert).toHaveTextContent('daemon offline')
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('table', { name: 'Active leases' })).toBeInTheDocument()
    expect((await screen.findAllByText('ada.ops')).length).toBeGreaterThan(0)
  })

  it('renders the create dialog in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    const user = userEvent.setup()
    render(<App />)

    expect(await screen.findByRole('heading', { name: '租约', level: 1 })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '新建租约' }))
    const dialog = await screen.findByRole('dialog', { name: '创建租约' })
    expect(within(dialog).getByLabelText('用户')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('平台')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('账号')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('TTL')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '创建租约' })).toBeInTheDocument()
  })
})
