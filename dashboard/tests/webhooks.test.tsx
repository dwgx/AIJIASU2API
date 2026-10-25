import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import { ApiError } from '../src/lib/errors'
import type { Delivery, WebhookCreated } from '../src/lib/types'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

async function openWebhooks(): Promise<HTMLElement> {
  window.history.pushState({}, '', '/settings')
  render(<App />)
  await screen.findByRole('heading', { name: 'Settings', level: 1 })
  await screen.findByRole('heading', { name: 'Integrations', level: 2 })
  const section = document.getElementById('integrations')
  if (!section) throw new Error('integrations section is not mounted')
  await within(section).findByText('Ops relay')
  return section
}

function webhookRow(name: string): HTMLElement {
  const row = screen.getByText(name).closest('li')
  if (!row) throw new Error(`row for ${name} not found`)
  return row as HTMLElement
}

describe('webhook settings section', () => {
  it('lists a receiver with its URL, events, last delivery and actions', async () => {
    await openWebhooks()

    const row = webhookRow('Ops relay')
    expect(within(row).getByText('https://hooks.example.com/ajs')).toBeInTheDocument()
    expect(within(row).getByText('node.connected')).toBeInTheDocument()
    expect(within(row).getByText('node.disconnected')).toBeInTheDocument()
    expect(within(row).getByText('Delivered')).toBeInTheDocument()
    expect(within(row).getByRole('switch', { name: 'Disable Ops relay' })).toBeChecked()
    expect(
      within(row).getByRole('button', { name: 'Deliveries for Ops relay' }),
    ).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: 'Edit Ops relay' })).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: 'Delete Ops relay' })).toBeInTheDocument()
  })

  it('offers the lease lifecycle events in the subscription list', async () => {
    const user = userEvent.setup()
    await openWebhooks()

    await user.click(screen.getByRole('button', { name: 'New webhook' }))
    const dialog = await screen.findByRole('dialog', { name: 'New webhook' })
    await user.click(within(dialog).getByRole('button', { name: 'Events' }))
    const listbox = await screen.findByRole('listbox', { name: 'Events' })

    for (const event of ['lease.created', 'lease.revoked', 'lease.expired']) {
      expect(within(listbox).getByRole('option', { name: event })).toBeInTheDocument()
    }
  })

  it('offers the order lifecycle events in the subscription list', async () => {
    const user = userEvent.setup()
    await openWebhooks()

    await user.click(screen.getByRole('button', { name: 'New webhook' }))
    const dialog = await screen.findByRole('dialog', { name: 'New webhook' })
    await user.click(within(dialog).getByRole('button', { name: 'Events' }))
    const listbox = await screen.findByRole('listbox', { name: 'Events' })

    // The daemon accepts exactly these six order events; a receiver created
    // from the panel must be able to subscribe to each of them.
    for (const event of [
      'order.created',
      'order.cancelled',
      'order.paid',
      'order.activated',
      'order.refunded',
      'order.charged_back',
    ]) {
      expect(within(listbox).getByRole('option', { name: event })).toBeInTheDocument()
    }
  })

  it('creates a receiver and shows the signing secret exactly once', async () => {
    const user = userEvent.setup()
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    const create = vi.spyOn(api, 'createWebhook')
    await openWebhooks()

    await user.click(screen.getByRole('button', { name: 'New webhook' }))
    const dialog = await screen.findByRole('dialog', { name: 'New webhook' })
    await user.type(
      within(dialog).getByLabelText('Endpoint URL'),
      'https://hooks.example.com/new',
    )
    await user.type(within(dialog).getByLabelText('Label'), 'Build relay')

    await user.click(within(dialog).getByRole('button', { name: 'Events' }))
    const listbox = await screen.findByRole('listbox', { name: 'Events' })
    await user.click(within(listbox).getByRole('option', { name: 'account.login' }))
    await user.click(within(listbox).getByRole('option', { name: 'account.logout' }))
    await user.click(screen.getByRole('button', { name: 'Done' }))

    await user.click(within(dialog).getByRole('button', { name: 'Create webhook' }))

    const created = (await create.mock.results[0]?.value) as WebhookCreated
    expect(created.secret).toMatch(/^[0-9a-f]{64}$/)

    const secretDialog = await screen.findByRole('dialog', { name: 'Signing secret' })
    expect(within(secretDialog).queryByText(created.secret)).not.toBeInTheDocument()
    const masked = within(secretDialog).getByLabelText('Signing secret')
    expect(masked.textContent).not.toBe(created.secret)
    expect(masked.textContent).toContain('•')

    await user.click(
      within(secretDialog).getByRole('button', { name: 'Copy signing secret' }),
    )
    expect(writeText).toHaveBeenCalledWith(created.secret)

    await user.click(within(secretDialog).getByRole('button', { name: 'I saved the secret' }))
    expect(
      screen.queryByRole('dialog', { name: 'Signing secret' }),
    ).not.toBeInTheDocument()

    expect(await screen.findByText('Build relay')).toBeInTheDocument()
    const row = webhookRow('Build relay')
    expect(within(row).getByText('account.login')).toBeInTheDocument()
    expect(within(row).getByText('account.logout')).toBeInTheDocument()

    // The secret never reaches web storage, even after the panel closes.
    for (let index = 0; index < localStorage.length; index += 1) {
      const key = localStorage.key(index)
      if (key) expect(localStorage.getItem(key)).not.toContain(created.secret)
    }
  })

  it('edits enabled in place and never shows a secret', async () => {
    const user = userEvent.setup()
    const update = vi.spyOn(api, 'updateWebhook')
    await openWebhooks()

    await user.click(within(webhookRow('Ops relay')).getByRole('button', { name: 'Edit Ops relay' }))
    const dialog = await screen.findByRole('dialog', { name: 'Edit webhook' })
    expect(within(dialog).getByLabelText('Endpoint URL')).toHaveValue(
      'https://hooks.example.com/ajs',
    )
    const enabled = within(dialog).getByRole('switch', { name: 'Enabled' })
    expect(enabled).toBeChecked()
    await user.click(enabled)
    expect(within(dialog).queryByText(/deletes and recreates this receiver/)).not.toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))

    expect(await screen.findByText('Webhook “Ops relay” updated')).toBeInTheDocument()
    expect(update).toHaveBeenCalledWith('wh-001', { enabled: false })
    await waitFor(() =>
      expect(screen.getByRole('switch', { name: 'Enable Ops relay' })).not.toBeChecked(),
    )
    expect(screen.queryByRole('dialog', { name: 'Signing secret' })).not.toBeInTheDocument()
  })

  it('replaces the receiver when the URL changes and rotates the secret', async () => {
    const user = userEvent.setup()
    const create = vi.spyOn(api, 'createWebhook')
    const remove = vi.spyOn(api, 'deleteWebhook')
    await openWebhooks()

    await user.click(within(webhookRow('Ops relay')).getByRole('button', { name: 'Edit Ops relay' }))
    const dialog = await screen.findByRole('dialog', { name: 'Edit webhook' })
    const url = within(dialog).getByLabelText('Endpoint URL')
    await user.clear(url)
    await user.type(url, 'https://hooks.example.com/v2')

    expect(
      await within(dialog).findByText(/deletes and recreates this receiver/),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))

    const created = (await create.mock.results[0]?.value) as WebhookCreated
    expect(created.secret).toMatch(/^[0-9a-f]{64}$/)
    expect(remove).toHaveBeenCalledWith('wh-001')
    expect(create).toHaveBeenCalledWith(
      expect.objectContaining({
        url: 'https://hooks.example.com/v2',
        events: ['node.connected', 'node.disconnected'],
      }),
    )

    const secretDialog = await screen.findByRole('dialog', { name: 'Signing secret' })
    expect(within(secretDialog).queryByText(created.secret)).not.toBeInTheDocument()
    await user.click(within(secretDialog).getByRole('button', { name: 'I saved the secret' }))

    await waitFor(() =>
      expect(screen.getByText('https://hooks.example.com/v2')).toBeInTheDocument(),
    )
    expect(screen.queryByText('https://hooks.example.com/ajs')).not.toBeInTheDocument()
  })

  it('deletes a receiver only after the typed confirmation', async () => {
    const user = userEvent.setup()
    await openWebhooks()

    await user.click(
      within(webhookRow('Ops relay')).getByRole('button', { name: 'Delete Ops relay' }),
    )
    const dialog = await screen.findByRole('dialog', { name: 'Delete webhook?' })
    const confirm = within(dialog).getByRole('button', { name: 'Delete webhook' })
    expect(confirm).toBeDisabled()

    await user.type(within(dialog).getByLabelText('Ops relay'), 'Ops relay')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    expect(await screen.findByText('Webhook “Ops relay” deleted')).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByText('Ops relay')).not.toBeInTheDocument())
    expect(screen.getByText('No webhook receiver is configured.')).toBeInTheDocument()
  })

  it('reads the last delivery from index 0 and pages the newest 50', async () => {
    const now = Date.now()
    const newest: Delivery = {
      id: 'dlv-new',
      endpointId: 'wh-001',
      event: 'node.disconnected',
      attempt: 3,
      status: 'dead_letter',
      httpStatus: 503,
      error: 'HTTP 503',
      at: new Date(now - 60_000).toISOString(),
      durationMs: 41,
    }
    const oldest: Delivery = {
      ...newest,
      id: 'dlv-old',
      attempt: 1,
      status: 'success',
      httpStatus: 200,
      error: undefined,
      at: new Date(now - 2 * 3_600_000).toISOString(),
    }
    const list = vi.spyOn(api, 'listWebhookDeliveries').mockResolvedValue([newest, oldest])
    await openWebhooks()

    // Newest first: index 0 is the dead letter, the tail is the older success.
    const row = webhookRow('Ops relay')
    expect(within(row).getByText('Dead letter')).toBeInTheDocument()
    expect(within(row).queryByText('Delivered')).not.toBeInTheDocument()
    expect(list).toHaveBeenCalledWith('wh-001', { limit: 50 })
  })

  it('opens the delivery drawer with attempts, statuses, error classes and durations', async () => {
    const user = userEvent.setup()
    await openWebhooks()

    await user.click(
      within(webhookRow('Ops relay')).getByRole('button', { name: 'Deliveries for Ops relay' }),
    )
    const drawer = await screen.findByRole('dialog', { name: 'Deliveries' })

    expect(await within(drawer).findByText('HTTP 202')).toBeInTheDocument()
    expect(within(drawer).getByText('HTTP 503')).toBeInTheDocument()
    expect(within(drawer).getByText('HTTP 200')).toBeInTheDocument()
    expect(within(drawer).getByText('http_5xx')).toBeInTheDocument()
    expect(within(drawer).getAllByText('Attempt 1').length).toBeGreaterThan(0)
    expect(within(drawer).getByText('Attempt 2')).toBeInTheDocument()
    expect(within(drawer).getByText('118 ms')).toBeInTheDocument()
    expect(within(drawer).getByText('240 ms')).toBeInTheDocument()
    expect(within(drawer).getByText('96 ms')).toBeInTheDocument()
    expect(within(drawer).getAllByText('node.connected').length).toBeGreaterThan(0)
    expect(within(drawer).getAllByText('node.disconnected').length).toBeGreaterThan(0)

    // Newest first: the first body row is the latest attempt, the last the oldest.
    expect(within(drawer).getByText(/newest first/)).toBeInTheDocument()
    const rows = within(drawer).getAllByRole('row')
    expect(within(rows[1]).getByText('Attempt 2')).toBeInTheDocument()
    expect(within(rows[1]).getByText('96 ms')).toBeInTheDocument()
    expect(within(rows[rows.length - 1]).getByText('Attempt 1')).toBeInTheDocument()
    expect(within(rows[rows.length - 1]).getByText('118 ms')).toBeInTheDocument()
  })

  it('shows an honest empty state when a receiver has no deliveries', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'listWebhookDeliveries').mockResolvedValue([])
    await openWebhooks()

    expect(within(webhookRow('Ops relay')).getByText('No deliveries yet')).toBeInTheDocument()
    await user.click(
      within(webhookRow('Ops relay')).getByRole('button', { name: 'Deliveries for Ops relay' }),
    )
    const drawer = await screen.findByRole('dialog', { name: 'Deliveries' })
    expect(
      await within(drawer).findByText('No delivery attempt has been recorded.'),
    ).toBeInTheDocument()
  })

  it('surfaces a 409 when enabling a receiver without a signing secret', async () => {
    const user = userEvent.setup()
    await openWebhooks()

    await user.click(screen.getByRole('switch', { name: 'Disable Ops relay' }))
    await waitFor(() =>
      expect(screen.getByRole('switch', { name: 'Enable Ops relay' })).toBeInTheDocument(),
    )

    vi.spyOn(api, 'updateWebhook').mockRejectedValueOnce(
      new ApiError(
        409,
        'webhook signing secret is unavailable; recreate the endpoint',
        'CONFLICT',
      ),
    )
    await user.click(screen.getByRole('switch', { name: 'Enable Ops relay' }))

    expect(
      await screen.findByText(
        'The signing secret is unavailable; recreate the receiver before enabling it.',
      ),
    ).toBeInTheDocument()
  })

  it('rejects a non-loopback http endpoint before calling the API', async () => {
    const user = userEvent.setup()
    const create = vi.spyOn(api, 'createWebhook')
    await openWebhooks()

    await user.click(screen.getByRole('button', { name: 'New webhook' }))
    const dialog = await screen.findByRole('dialog', { name: 'New webhook' })
    await user.type(
      within(dialog).getByLabelText('Endpoint URL'),
      'http://hooks.example.com/ajs',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Create webhook' }))

    expect(
      within(dialog).getByText('http is only accepted for localhost or loopback receivers.'),
    ).toBeInTheDocument()
    expect(create).not.toHaveBeenCalled()
  })

  it('shows the honest empty state when no receiver exists', async () => {
    vi.spyOn(api, 'listWebhooks').mockResolvedValue([])
    window.history.pushState({}, '', '/settings')
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings', level: 1 })
    const section = document.getElementById('integrations')
    if (!section) throw new Error('integrations section is not mounted')

    expect(
      await within(section).findByText('No webhook receiver is configured.'),
    ).toBeInTheDocument()
    expect(within(section).getAllByRole('button', { name: 'New webhook' }).length).toBeGreaterThan(0)
  })

  it('renders the webhook section in Chinese', async () => {
    const user = userEvent.setup()
    localStorage.setItem('ajs.lang', 'zh')
    window.history.pushState({}, '', '/settings')
    render(<App />)
    await screen.findByRole('heading', { name: '设置', level: 1 })

    const section = document.getElementById('integrations')
    if (!section) throw new Error('integrations section is not mounted')
    expect(await within(section).findByText('Ops relay')).toBeInTheDocument()
    await user.click(within(section).getByRole('button', { name: '新建 Webhook' }))

    const dialog = await screen.findByRole('dialog', { name: '新建 Webhook' })
    expect(within(dialog).getByLabelText('端点 URL')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('标签')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '创建 Webhook' })).toBeInTheDocument()
  })
})
