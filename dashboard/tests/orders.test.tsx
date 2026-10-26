import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderArchivedApp } from './support/archived-app'
import { api } from '../src/lib/api'
import { ApiError } from '../src/lib/errors'
import {
  MOCK_PORTAL_EMAIL,
  MOCK_PORTAL_PASSWORD,
  portalApi,
} from '../src/lib/portal-api'
import { setMockEpayEnabled } from '../src/lib/order-mock'
import type { Order } from '../src/lib/types'

const GIB = 1024 ** 3

const LIVE_SESSION = {
  tenant: {
    id: 'tenant-01',
    email: 'ada@example.com',
    displayName: 'Ada Ops',
    status: 'active',
    userId: 'u-01',
    createdAt: '2026-10-01T00:00:00Z',
    approvedAt: '2026-10-02T00:00:00Z',
    suspendedAt: null,
  },
  user: { id: 'u-01', name: 'ada.ops' },
  csrfToken: 'csrf-live-orders',
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

async function signIn(): Promise<void> {
  await portalApi.login({ email: MOCK_PORTAL_EMAIL, password: MOCK_PORTAL_PASSWORD })
}

async function openPortal(tab: 'Plans' | 'Orders') {
  window.history.pushState({}, '', '/portal')
  const view = renderArchivedApp()
  await screen.findByRole('heading', { name: 'My portal', level: 1 })
  const user = userEvent.setup()
  await user.click(await screen.findByRole('tab', { name: tab }))
  return { user, unmount: view.unmount }
}

async function openAdmin() {
  window.history.pushState({}, '', '/orders')
  const view = renderArchivedApp()
  await screen.findByRole('heading', { name: 'Orders', level: 1 })
  return { user: userEvent.setup(), unmount: view.unmount }
}

async function findRow(id: string): Promise<HTMLElement> {
  const cell = await screen.findByText(id)
  const row = cell.closest('tr')
  if (!row) throw new Error(`order row ${id} is not mounted`)
  return row
}

beforeEach(async () => {
  localStorage.clear()
  sessionStorage.clear()
  window.history.pushState({}, '', '/portal/login')
  await portalApi.resetMock()
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('portal plans and orders', () => {
  it('lists active plans with price, term and quota, and hides deactivated plans', async () => {
    await signIn()
    await openPortal('Plans')

    expect(await screen.findByText('Starter')).toBeInTheDocument()
    expect(screen.getByText('Standard')).toBeInTheDocument()
    expect(screen.getByText('Team')).toBeInTheDocument()
    expect(screen.queryByText('Legacy')).not.toBeInTheDocument()

    const starter = screen.getByText('Starter').closest('li')
    if (!starter) throw new Error('Starter plan card is not mounted')
    expect(within(starter).getByText(/49\.00/)).toBeInTheDocument()
    expect(within(starter).getByText('30 days')).toBeInTheDocument()
    expect(within(starter).getByText('50.0 GiB')).toBeInTheDocument()
    expect(
      within(starter).getByRole('button', { name: 'Create order for Starter' }),
    ).toBeInTheDocument()
  })

  it('creates an order after confirmation and never shows payment success', async () => {
    await signIn()
    const { user } = await openPortal('Plans')

    await user.click(await screen.findByRole('button', { name: 'Create order for Starter' }))
    const dialog = await screen.findByRole('dialog', { name: 'Create this order?' })
    expect(within(dialog).getByText(/No online payment is taken/)).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Create order' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    const row = await findRow('ord-07')
    expect(within(row).getByText('Awaiting payment')).toBeInTheDocument()
    expect(within(row).queryByText('Paid — activation pending')).not.toBeInTheDocument()
    expect(within(row).queryByText('Active')).not.toBeInTheDocument()
    expect(
      screen.getByText(/No payment is recorded until the provider confirms it/),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/Online payment is available only when the operator enables/),
    ).toBeInTheDocument()
  })

  it('replays an idempotency key, conflicts on a changed plan, and blocks a second open order', async () => {
    await signIn()
    const first = await portalApi.createOrder('plan-01', 'idem-order-1')
    const replay = await portalApi.createOrder('plan-01', 'idem-order-1')
    expect(replay.id).toBe(first.id)

    await expect(portalApi.createOrder('plan-02', 'idem-order-1')).rejects.toMatchObject({
      status: 409,
    })
    await expect(portalApi.createOrder('plan-01', 'idem-order-2')).rejects.toMatchObject({
      status: 409,
    })

    const open = (await portalApi.orders()).filter((order) => order.id === first.id)
    expect(open).toHaveLength(1)
    expect(open[0].state).toBe('awaiting_payment')

    await portalApi.cancelOrder(first.id)
    const next = await portalApi.createOrder('plan-01', 'idem-order-2')
    expect(next.id).not.toBe(first.id)
  })

  it('sends the Idempotency-Key with CSRF and never a bearer token', async () => {
    vi.stubEnv('VITE_USE_MOCK', 'false')
    const created: Order = {
      id: 'ord-live',
      tenantId: 'tenant-01',
      userId: 'u-01',
      planId: 'plan-01',
      plan: {
        id: 'plan-01',
        name: 'Starter',
        amountMinor: 4_900,
        currency: 'CNY',
        leaseTtlSeconds: 2_592_000,
        quotaBytes: 50 * GIB,
      },
      provider: 'manual',
      state: 'awaiting_payment',
      providerRef: null,
      reviewRequired: false,
      createdAt: '2026-10-25T00:00:00Z',
      paidAt: null,
      activatedAt: null,
      cancelledAt: null,
      refundedAt: null,
      grant: null,
    }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(LIVE_SESSION))
      .mockResolvedValueOnce(jsonResponse(created, 201))
    vi.stubGlobal('fetch', fetchMock)

    await portalApi.login({ email: 'ada@example.com', password: 'secret' })
    await portalApi.createOrder('plan-01', 'live-key-1')

    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(url).toContain('/v1/portal/orders')
    expect(init.method).toBe('POST')
    const headers = new Headers(init.headers)
    expect(headers.get('Idempotency-Key')).toBe('live-key-1')
    expect(headers.get('X-CSRF-Token')).toBe('csrf-live-orders')
    expect(headers.get('Authorization')).toBeNull()
    expect(JSON.parse(String(init.body))).toEqual({ planId: 'plan-01' })
  })

  it('renders awaiting, paid, active and refunded statuses honestly', async () => {
    await signIn()
    const order = await portalApi.createOrder('plan-01', 'status-flow')

    let view = await openPortal('Orders')
    let row = await findRow(order.id)
    expect(within(row).getByText('Awaiting payment')).toBeInTheDocument()
    view.unmount()

    await api.settleOrder(order.id, 'RCPT-STATUS-1')
    view = await openPortal('Orders')
    row = await findRow(order.id)
    expect(within(row).getByText('Paid — activation pending')).toBeInTheDocument()
    view.unmount()

    await api.settleOrder(order.id, 'RCPT-STATUS-1')
    view = await openPortal('Orders')
    row = await findRow(order.id)
    expect(within(row).getByText('Active')).toBeInTheDocument()
    expect(within(row).queryByText('Revoked')).not.toBeInTheDocument()
    view.unmount()

    await api.refundOrder(order.id, 'REF-STATUS-1')
    await openPortal('Orders')
    row = await findRow(order.id)
    expect(within(row).getByText('Refunded')).toBeInTheDocument()
    expect(within(row).getByText('Revoked')).toBeInTheDocument()
  })

  it('shows the review-required banner and seeded history states', async () => {
    await signIn()
    await openPortal('Orders')

    expect(await screen.findByText('ord-01')).toBeInTheDocument()
    expect(
      screen.getByText(/An order needs operator review\. Nothing is activated/),
    ).toBeInTheDocument()
    expect(within(await findRow('ord-01')).getByText('Refunded')).toBeInTheDocument()
    expect(within(await findRow('ord-02')).getByText('Review required')).toBeInTheDocument()
    expect(within(await findRow('ord-06')).getByText('Cancelled')).toBeInTheDocument()
  })

  it('cancels an awaiting order from the portal after confirmation', async () => {
    await signIn()
    const order = await portalApi.createOrder('plan-01', 'portal-cancel')
    const { user } = await openPortal('Orders')

    const row = await findRow(order.id)
    await user.click(within(row).getByRole('button', { name: 'Cancel order' }))
    const dialog = await screen.findByRole('dialog', { name: 'Cancel this order?' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel order' }))

    await waitFor(() =>
      expect(within(rowForSync(order.id)).getByText('Cancelled')).toBeInTheDocument(),
    )
  })

  it('refuses to cancel a foreign order and never leaks its existence', async () => {
    await signIn()
    await expect(portalApi.cancelOrder('ord-04')).rejects.toMatchObject({ status: 404 })
  })

  it('opens the 易支付 checkout in a new tab without claiming payment', async () => {
    await signIn()
    const order = await portalApi.createOrder('plan-01', 'portal-pay')
    const openSpy = vi.spyOn(window, 'open').mockReturnValue({} as Window)
    const { user } = await openPortal('Orders')

    const row = await findRow(order.id)
    await user.click(within(row).getByRole('button', { name: 'Pay now' }))

    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1))
    const [payUrl, target] = openSpy.mock.calls[0]
    expect(String(payUrl)).toContain(`out_trade_no=${order.id}`)
    expect(String(payUrl)).toContain('pay.example.com')
    expect(target).toBe('_blank')

    // Opening a hosted page is intent, not payment: the row stays awaiting.
    expect(within(rowForSync(order.id)).getByText('Awaiting payment')).toBeInTheDocument()
    expect(
      within(rowForSync(order.id)).queryByText('Paid — activation pending'),
    ).not.toBeInTheDocument()
  })

  it('labels the pay action 去支付 in the Chinese portal', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    await signIn()
    const order = await portalApi.createOrder('plan-01', 'zh-portal-pay')
    window.history.pushState({}, '', '/portal')
    renderArchivedApp()
    await screen.findByRole('heading', { name: '客户中心', level: 1 })
    const user = userEvent.setup()
    await user.click(await screen.findByRole('tab', { name: '订单' }))

    const row = await findRow(order.id)
    expect(within(row).getByRole('button', { name: '去支付' })).toBeInTheDocument()
  })

  it('shows the offline settlement state when 易支付 is disabled', async () => {
    await signIn()
    const order = await portalApi.createOrder('plan-01', 'portal-pay-disabled')
    setMockEpayEnabled(false)
    const openSpy = vi.spyOn(window, 'open')
    const { user } = await openPortal('Orders')

    const row = await findRow(order.id)
    await user.click(within(row).getByRole('button', { name: 'Pay now' }))

    await waitFor(() =>
      expect(within(rowForSync(order.id)).getByText('Offline settlement')).toBeInTheDocument(),
    )
    expect(openSpy).not.toHaveBeenCalled()
    expect(
      within(rowForSync(order.id)).queryByRole('button', { name: 'Pay now' }),
    ).not.toBeInTheDocument()
    expect(await screen.findByText(/易支付 is not enabled/)).toBeInTheDocument()
  })
  it('shows provisioning copy for a paid order with a job', async () => {
    await signIn()
    const paid: Order = {
      id: 'ord-queued',
      tenantId: 'tenant-01',
      userId: 'u-01',
      planId: 'plan-01',
      plan: {
        id: 'plan-01',
        name: 'Starter',
        amountMinor: 4_900,
        currency: 'CNY',
        leaseTtlSeconds: 2_592_000,
        quotaBytes: 50 * GIB,
      },
      provider: 'epay',
      state: 'paid',
      providerRef: 'EPAY-QUEUED-01',
      reviewRequired: false,
      createdAt: '2026-10-26T00:00:00Z',
      paidAt: '2026-10-26T00:01:00Z',
      activatedAt: null,
      cancelledAt: null,
      refundedAt: null,
      grant: null,
      fulfilment: {
        orderId: 'ord-queued',
        mode: 'initial',
        state: 'queued',
        attempts: 3,
        entitlementExpiresAt: '2026-11-25T00:01:00Z',
        nextAttemptAt: '2026-10-27T00:02:00Z',
        lastReason: 'capacity',
        updatedAt: '2026-10-26T00:01:00Z',
      },
    }
    const orders = vi.spyOn(portalApi, 'orders').mockResolvedValue([paid])

    const queued = await openPortal('Orders')
    expect(
      await screen.findByText(
        'Payment received — we are provisioning your service. No further payment is needed.',
      ),
    ).toBeInTheDocument()
    // A customer never sees attempts, the reason, the schedule or a job id.
    expect(screen.queryByText(/Attempts/)).not.toBeInTheDocument()
    expect(screen.queryByText(/No automatic retry/)).not.toBeInTheDocument()
    expect(screen.queryByText('No free capacity')).not.toBeInTheDocument()
    queued.unmount()

    orders.mockResolvedValue([
      {
        ...paid,
        fulfilment: {
          ...paid.fulfilment!,
          state: 'dead_letter',
          attempts: 8,
          blocked: true,
          lastReason: 'publish_failed',
          nextAttemptAt: null,
        },
      },
    ])
    await openPortal('Orders')
    expect(
      await screen.findByText(
        'Provisioning is delayed and support has been notified. No further payment is needed.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText(/Attempts/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Dead letter/)).not.toBeInTheDocument()
  })

  it('shows an ended-lease reason and restores service from the order row', async () => {
    await signIn()
    const { user } = await openPortal('Orders')

    const row = await findRow('ord-08')
    expect(within(row).getByText('Service interrupted')).toBeInTheDocument()
    expect(within(row).getByText('Lease ended: daemon restarted')).toBeInTheDocument()

    const before = (await portalApi.orders()).find((order) => order.id === 'ord-08')
    expect(before?.grant?.leaseEndedReason).toBe('daemon_restart')
    const oldLeaseID = before?.grant?.leaseId

    await user.click(within(row).getByRole('button', { name: 'Restore service' }))
    expect(
      await screen.findByText('Service restored with a replacement lease.'),
    ).toBeInTheDocument()

    await waitFor(() =>
      expect(
        within(rowForSync('ord-08')).queryByText('Service interrupted'),
      ).not.toBeInTheDocument(),
    )
    const after = (await portalApi.orders()).find((order) => order.id === 'ord-08')
    expect(after?.state).toBe('active')
    expect(after?.grant?.leaseEndedReason).toBeUndefined()
    expect(after?.grant?.leaseId).not.toBe(oldLeaseID)
  })

  it('re-issues an ended same-plan order on create instead of charging again', async () => {
    await signIn()
    const before = await portalApi.orders()
    const reissued = await portalApi.createOrder('plan-02', 'reissue-on-create')

    expect(reissued.id).toBe('ord-08')
    expect(reissued.state).toBe('active')
    expect(reissued.grant?.leaseEndedReason).toBeUndefined()
    expect((await portalApi.orders()).filter((order) => order.id === 'ord-08')).toHaveLength(1)
    expect(await portalApi.orders()).toHaveLength(before.length)
  })

  it('renders the ended-lease reason and restore action in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    await signIn()
    window.history.pushState({}, '', '/portal')
    renderArchivedApp()
    await screen.findByRole('heading', { name: '客户中心', level: 1 })
    const user = userEvent.setup()
    await user.click(await screen.findByRole('tab', { name: '订单' }))

    const row = await findRow('ord-08')
    expect(within(row).getByText('服务已中断')).toBeInTheDocument()
    expect(within(row).getByText('租约已结束：守护进程重启')).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: '恢复服务' })).toBeInTheDocument()
  })
})

function rowForSync(id: string): HTMLElement {
  const cell = screen.getByText(id)
  const row = cell.closest('tr')
  if (!row) throw new Error(`order row ${id} is not mounted`)
  return row
}

function planRow(name: string): HTMLElement {
  const list = screen.getByRole('list', { name: 'Plans' })
  const row = within(list).getByText(name).closest('li')
  if (!row) throw new Error(`plan row ${name} is not mounted`)
  return row
}

describe('admin orders', () => {
  it('lists every tenant’s orders and filters by state and review flag', async () => {
    const { user } = await openAdmin()

    expect(await screen.findByText('ord-03')).toBeInTheDocument()
    expect(screen.getByText('ord-01')).toBeInTheDocument()
    expect(screen.getByText('ord-04')).toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: 'Filter by state' }))
    const listbox = await screen.findByRole('listbox')
    await user.click(within(listbox).getByRole('option', { name: 'Review required' }))

    await waitFor(() => expect(screen.queryByText('ord-01')).not.toBeInTheDocument())
    expect(screen.getByText('ord-02')).toBeInTheDocument()
    expect(screen.getByText('ord-05')).toBeInTheDocument()
    expect(screen.getAllByText('Reconcile this order offline.').length).toBeGreaterThan(0)
  })

  it('shows each order’s payment provider and the 易支付 status chip', async () => {
    await openAdmin()

    expect(await screen.findByText('Epay enabled')).toBeInTheDocument()
    expect(within(await findRow('ord-03')).getByText('易支付')).toBeInTheDocument()
    expect(within(await findRow('ord-01')).getByText('Manual (offline)')).toBeInTheDocument()
  })

  it('shows durable fulfilment states and filters dead-lettered work', async () => {
    const { user } = await openAdmin()

    const queued = await findRow('ord-03')
    expect(within(queued).getByText('Queued')).toBeInTheDocument()
    expect(within(queued).getByText(/Next attempt/)).toBeInTheDocument()
    // The legacy settlement replay would race the durable publisher.
    expect(
      within(queued).queryByRole('button', { name: 'Retry activation' }),
    ).not.toBeInTheDocument()

    const dead = await findRow('ord-09')
    expect(within(dead).getByText('Dead letter')).toBeInTheDocument()
    expect(within(dead).getByText(/No automatic retry/)).toBeInTheDocument()
    expect(
      within(dead).getByText(/Publication is dead-lettered; no automatic retry will run/),
    ).toBeInTheDocument()

    const cleanup = await findRow('ord-10')
    expect(within(cleanup).getByText('Cleanup pending')).toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: 'Filter by fulfilment' }))
    const listbox = await screen.findByRole('listbox')
    await user.click(within(listbox).getByRole('option', { name: 'Dead letter' }))

    await waitFor(() => expect(screen.queryByText('ord-03')).not.toBeInTheDocument())
    expect(screen.getByText('ord-09')).toBeInTheDocument()
    expect(screen.queryByText('ord-10')).not.toBeInTheDocument()
  })

  it('marks 易支付 disabled when the adapter is off', async () => {
    setMockEpayEnabled(false)
    await openAdmin()

    expect(await screen.findByText('Epay disabled')).toBeInTheDocument()
  })

  it('refuses an offline refund affordance for epay and keeps it for manual and stripe', async () => {
    const seed = await api.listOrders()
    const epay = seed.find((order) => order.id === 'ord-03')!
    const manual = seed.find((order) => order.id === 'ord-04')!
    // Stripe keeps the offline path: a verified provider refund is recorded by
    // the daemon, and the operator may still attest an offline reversal.
    const stripe: Order = { ...manual, id: 'ord-stripe', provider: 'stripe', providerRef: 'pi_3N' }
    vi.spyOn(api, 'listOrders').mockResolvedValue([epay, manual, stripe])

    await openAdmin()

    const epayRow = await findRow('ord-03')
    expect(
      within(epayRow).queryByRole('button', { name: 'Record offline refund' }),
    ).not.toBeInTheDocument()
    expect(
      within(epayRow).getByText('Epay has no refund API — refund at the provider.'),
    ).toBeInTheDocument()

    expect(
      within(await findRow('ord-04')).getByRole('button', { name: 'Record offline refund' }),
    ).toBeInTheDocument()
    expect(
      within(await findRow('ord-stripe')).getByRole('button', { name: 'Record offline refund' }),
    ).toBeInTheDocument()
  })

  it('requeues a dead-lettered job and reloads the row', async () => {
    const { user } = await openAdmin()

    const dead = await findRow('ord-09')
    expect(within(dead).getByText('Dead letter')).toBeInTheDocument()
    await user.click(within(dead).getByRole('button', { name: 'Retry fulfilment' }))

    expect(
      await screen.findByText('Fulfilment retry queued for order ord-09.'),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(within(rowForSync('ord-09')).getByText('Queued')).toBeInTheDocument(),
    )
    expect(
      within(rowForSync('ord-09')).queryByRole('button', { name: 'Retry fulfilment' }),
    ).not.toBeInTheDocument()
    expect(within(rowForSync('ord-09')).queryByText('Dead letter')).not.toBeInTheDocument()
  })

  it('keeps the dead letter and shows the daemon refusal when a retry is rejected', async () => {
    vi.spyOn(api, 'retryFulfilment').mockRejectedValue(
      new ApiError(
        409,
        '409 Conflict — order "ord-09" attempt "lease-ord-09" is awaiting external cleanup',
        'CONFLICT',
      ),
    )
    const { user } = await openAdmin()

    const dead = await findRow('ord-09')
    await user.click(within(dead).getByRole('button', { name: 'Retry fulfilment' }))

    expect(
      await screen.findByText(
        /Retry refused: order "ord-09" attempt "lease-ord-09" is awaiting external cleanup/,
      ),
    ).toBeInTheDocument()
    expect(within(rowForSync('ord-09')).getByText('Dead letter')).toBeInTheDocument()
  })

  it('offers no retry for queued, publishing or cleanup-pending jobs', async () => {
    const seed = await api.listOrders()
    const queued = seed.find((order) => order.id === 'ord-03')!
    const cleanup = seed.find((order) => order.id === 'ord-10')!
    const publishing: Order = {
      ...seed.find((order) => order.id === 'ord-04')!,
      id: 'ord-publishing',
      state: 'paid',
      grant: null,
      fulfilment: {
        orderId: 'ord-publishing',
        mode: 'initial',
        state: 'publishing',
        attempts: 1,
        entitlementExpiresAt: '2026-11-25T00:01:00Z',
        updatedAt: '2026-10-27T00:00:00Z',
      },
    }
    vi.spyOn(api, 'listOrders').mockResolvedValue([queued, publishing, cleanup])

    await openAdmin()

    for (const id of ['ord-03', 'ord-publishing', 'ord-10']) {
      const row = await findRow(id)
      expect(
        within(row).queryByRole('button', { name: 'Retry fulfilment' }),
      ).not.toBeInTheDocument()
    }
  })

  it('requires a receipt reference, then attests, activates and refunds', async () => {
    await signIn()
    const order = await portalApi.createOrder('plan-01', 'admin-flow')
    const settleSpy = vi.spyOn(api, 'settleOrder')
    const { user } = await openAdmin()

    let row = await findRow(order.id)
    await user.click(within(row).getByRole('button', { name: 'Mark offline payment received' }))
    const dialog = await screen.findByRole('dialog', { name: 'Mark offline payment received' })
    expect(within(dialog).getByText(/does not charge a card or move money/)).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Record payment' }))
    expect(await within(dialog).findByText('Enter the receipt reference.')).toBeInTheDocument()
    expect(settleSpy).not.toHaveBeenCalled()

    await user.type(within(dialog).getByLabelText('Receipt reference'), 'RCPT-0007')
    await user.click(within(dialog).getByRole('button', { name: 'Record payment' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(settleSpy).toHaveBeenCalledWith(order.id, 'RCPT-0007')

    await waitFor(() =>
      expect(
        within(rowForSync(order.id)).getByText('Paid — activation pending'),
      ).toBeInTheDocument(),
    )

    await user.click(within(rowForSync(order.id)).getByRole('button', { name: 'Retry activation' }))
    const retry = await screen.findByRole('dialog', { name: 'Mark offline payment received' })
    expect(within(retry).getByLabelText('Receipt reference')).toHaveValue('RCPT-0007')
    await user.click(within(retry).getByRole('button', { name: 'Record payment' }))

    await waitFor(() =>
      expect(within(rowForSync(order.id)).getByText('Active')).toBeInTheDocument(),
    )

    await user.click(
      within(rowForSync(order.id)).getByRole('button', { name: 'Record offline refund' }),
    )
    const refund = await screen.findByRole('dialog', { name: 'Record an offline refund?' })
    expect(within(refund).getByText(/does not transfer money/)).toBeInTheDocument()
    await user.click(within(refund).getByRole('button', { name: 'Record refund' }))
    expect(
      await within(refund).findByText('Enter the refund reference.'),
    ).toBeInTheDocument()
    await user.type(within(refund).getByLabelText('Refund reference'), 'REF-0007')
    await user.click(within(refund).getByRole('button', { name: 'Record refund' }))

    await waitFor(() =>
      expect(within(rowForSync(order.id)).getByText('Refunded')).toBeInTheDocument(),
    )
    expect(within(rowForSync(order.id)).getByText('Revoked')).toBeInTheDocument()
  })

  it('cancels an awaiting order after confirmation', async () => {
    await signIn()
    const order = await portalApi.createOrder('plan-01', 'admin-cancel')
    const { user } = await openAdmin()

    const row = await findRow(order.id)
    await user.click(within(row).getByRole('button', { name: 'Cancel' }))
    const dialog = await screen.findByRole('dialog', { name: 'Cancel this order?' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel order' }))

    await waitFor(() =>
      expect(within(rowForSync(order.id)).getByText('Cancelled')).toBeInTheDocument(),
    )
  })

  it('deactivates and reactivates a plan, keeping existing order snapshots priced', async () => {
    const { user } = await openAdmin()
    const planList = await screen.findByRole('list', { name: 'Plans' })
    const starter = within(planList).getByText('Starter').closest('li')
    if (!starter) throw new Error('Starter plan row is not mounted')

    await user.click(
      within(starter).getByRole('switch', { name: 'Activate or deactivate Starter' }),
    )
    let dialog = await screen.findByRole('dialog', { name: /Deactivate plan/ })
    await user.click(within(dialog).getByRole('button', { name: 'Deactivate plan' }))

    await waitFor(() => expect(within(planRow('Starter')).getByText('Inactive')).toBeInTheDocument())
    expect(
      within(planRow('Starter')).getByRole('switch', {
        name: 'Activate or deactivate Starter',
      }),
    ).not.toBeChecked()

    await user.click(
      within(planRow('Starter')).getByRole('switch', {
        name: 'Activate or deactivate Starter',
      }),
    )
    dialog = await screen.findByRole('dialog', { name: /Activate plan/ })
    await user.click(within(dialog).getByRole('button', { name: 'Activate plan' }))
    await waitFor(() => expect(within(planRow('Starter')).getByText('Active')).toBeInTheDocument())
  })

  it('hides a deactivated plan from new orders but keeps existing snapshots', async () => {
    await signIn()
    await api.setPlanActive('plan-01', false)

    expect((await portalApi.plans()).map((plan) => plan.id)).not.toContain('plan-01')
    await expect(portalApi.createOrder('plan-01', 'inactive-plan')).rejects.toMatchObject({
      status: 404,
    })
    const refunded = (await portalApi.orders()).find((order) => order.id === 'ord-01')
    expect(refunded?.plan.name).toBe('Starter')
    expect(refunded?.plan.amountMinor).toBe(4_900)
  })

  it('renders portal plans and the admin order surface in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    await signIn()
    window.history.pushState({}, '', '/portal')
    const portalView = renderArchivedApp()
    await screen.findByRole('heading', { name: '客户中心', level: 1 })
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: '套餐' }))

    expect(await screen.findByText('Starter')).toBeInTheDocument()
    const starter = screen.getByText('Starter').closest('li')
    if (!starter) throw new Error('Starter plan card is not mounted')
    expect(within(starter).getByText('每月流量额度')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '为 Starter 创建订单' })).toBeInTheDocument()
    portalView.unmount()

    await portalApi.createOrder('plan-01', 'zh-admin-flow')
    window.history.pushState({}, '', '/orders')
    renderArchivedApp()
    await screen.findByRole('heading', { name: '订单', level: 1 })
    expect(await screen.findByText('标记已收到线下付款')).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '按状态筛选' })).toBeInTheDocument()
    expect(screen.getByText('全部订单')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '投递状态' })).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '按投递状态筛选' })).toBeInTheDocument()
    expect(screen.getAllByText('死信').length).toBeGreaterThan(0)
    expect(screen.getAllByText('需人工处理').length).toBeGreaterThan(0)
  })
})

describe('account capacity', () => {
  it('shows the vendor 同时连接数 allowance on the account card with an honest note', async () => {
    window.history.pushState({}, '', '/accounts')
    renderArchivedApp()
    await screen.findByRole('heading', { name: 'Accounts', level: 1 })
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: 'Cards' }))
    const list = await screen.findByRole('list', { name: 'Vendor accounts' })
    const card = within(list).getAllByRole('listitem')[0]

    expect(within(card).getByText('Simultaneous connections')).toBeInTheDocument()
    expect(within(card).getByText('200')).toBeInTheDocument()
    expect(within(card).getByText('Multi-slot pending')).toBeInTheDocument()
  })
})

describe('admin fulfilment retry route', () => {
  it('posts the retry route with CSRF and surfaces the daemon 409 detail', async () => {
    const seed = await api.listOrders()
    const dead = seed.find((order) => order.id === 'ord-09')!
    const queued: Order = {
      ...dead,
      fulfilment: {
        ...dead.fulfilment!,
        state: 'queued',
        attempts: 0,
        blocked: false,
        nextAttemptAt: '2026-10-27T00:00:00Z',
      },
    }
    vi.stubEnv('VITE_USE_MOCK', 'false')
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(queued, 202))
      .mockResolvedValueOnce(
        jsonResponse(
          {
            error: {
              code: 'CONFLICT',
              message: 'order "ord-09" attempt "lease-ord-09" is awaiting external cleanup',
            },
          },
          409,
        ),
      )
    vi.stubGlobal('fetch', fetchMock)

    // `lib/api.ts` reads VITE_USE_MOCK at module init; a fresh module graph is
    // the only way to exercise the live branch from this mock-mode file.
    vi.resetModules()
    const live = await import('../src/lib/api')

    const accepted = await live.api.retryFulfilment('ord-09')
    expect(accepted.fulfilment?.state).toBe('queued')

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/v1/admin/orders/ord-09/actions/retry-fulfilment')
    expect(init.method).toBe('POST')
    const headers = new Headers(init.headers)
    expect(headers.get('X-AJS-CSRF')).toBe('1')
    expect(headers.get('Authorization')).toBeNull()
    expect(init.body).toBeUndefined()

    await expect(live.api.retryFulfilment('ord-09')).rejects.toMatchObject({
      status: 409,
      code: 'CONFLICT',
      message: expect.stringContaining('awaiting external cleanup'),
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
})
