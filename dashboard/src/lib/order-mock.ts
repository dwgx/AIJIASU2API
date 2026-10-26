import { ApiError } from './errors'
import { getDb } from './mock'
import type { Order, OrderPlanSnapshot, Plan } from './types'

/* --------------------------------------------------------------------------
 * Deterministic in-browser order backend (DESIGN-ORDERS.md)
 *
 * The mock mirrors the daemon's visible behaviour: create is idempotent per
 * (tenant,user,key) and rejects a changed payload or a second open order;
 * cancellation is awaiting-only and terminal; settlement records an offline
 * receipt and leaves activation retryable; refunds are full-only and revoke
 * the grant; mismatches quarantine the order in `reviewRequired` without
 * granting. Seeded orders cover every state, and only terminal ones belong to
 * the portal demo tenant so the demo can still create an order.
 * ------------------------------------------------------------------------ */

const DAY_MS = 86_400_000
const GIB = 1024 ** 3

interface MockOrderState {
  plans: Plan[]
  orders: Order[]
  planSeq: number
  orderSeq: number
  /** Whether the mock 易支付 adapter is configured; tests toggle this. */
  epayEnabled: boolean
  /** `${tenantId}:${userId}:${key}` → the request it first answered. */
  requests: Map<string, { requestHash: string; orderId: string }>
}

let state: MockOrderState | null = null

function iso(ms: number): string {
  return new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')
}

function clone<T>(value: T): T {
  return structuredClone(value)
}

function snapshot(plan: Plan): OrderPlanSnapshot {
  return {
    id: plan.id,
    name: plan.name,
    amountMinor: plan.amountMinor,
    currency: plan.currency,
    leaseTtlSeconds: plan.leaseTtlSeconds,
    quotaBytes: plan.quotaBytes,
  }
}

function seedPlans(now: number): Plan[] {
  const createdAt = iso(now - 120 * DAY_MS)
  return [
    {
      id: 'plan-01',
      active: true,
      name: 'Starter',
      amountMinor: 4_900,
      currency: 'CNY',
      leaseTtlSeconds: 30 * 86_400,
      quotaBytes: 50 * GIB,
      createdAt,
      updatedAt: iso(now - 30 * DAY_MS),
    },
    {
      id: 'plan-02',
      active: true,
      name: 'Standard',
      amountMinor: 12_900,
      currency: 'CNY',
      leaseTtlSeconds: 30 * 86_400,
      quotaBytes: 200 * GIB,
      createdAt,
      updatedAt: iso(now - 21 * DAY_MS),
    },
    {
      id: 'plan-03',
      active: true,
      name: 'Team',
      amountMinor: 34_900,
      currency: 'CNY',
      leaseTtlSeconds: 90 * 86_400,
      quotaBytes: 1024 * GIB,
      createdAt,
      updatedAt: iso(now - 9 * DAY_MS),
    },
    {
      id: 'plan-04',
      active: false,
      name: 'Legacy',
      amountMinor: 2_900,
      currency: 'CNY',
      leaseTtlSeconds: 30 * 86_400,
      quotaBytes: 20 * GIB,
      createdAt,
      updatedAt: iso(now - 60 * DAY_MS),
    },
  ]
}

function grantFor(
  order: Pick<Order, 'id' | 'plan' | 'paidAt' | 'userId'>,
  expiresAt: string,
  revokedAt: string | null,
) {
  const user = getDb().users.find((candidate) => candidate.id === order.userId)
  return {
    orderId: order.id,
    leaseId: `lease-ord-${order.id.slice(-2)}`,
    quotaBytes: order.plan.quotaBytes,
    previousQuotaBytes: user?.quotaBytesMonth ?? 0,
    expiresAt,
    revokedAt,
  }
}

function seedOrders(now: number, plans: Plan[]): Order[] {
  const planOf = (id: string) => snapshot(plans.find((plan) => plan.id === id)!)
  const starter = planOf('plan-01')
  const standard = planOf('plan-02')
  const team = planOf('plan-03')

  const refundedPaidAt = now - 19 * DAY_MS
  const refundedExpiry = refundedPaidAt + starter.leaseTtlSeconds * 1000
  const refundedOrder: Order = {
    id: 'ord-01',
    tenantId: 'tenant-01',
    userId: 'u-01',
    planId: starter.id,
    plan: starter,
    provider: 'manual',
    state: 'refunded',
    providerRef: 'OFFLINE-2026-0912-01',
    reviewRequired: false,
    createdAt: iso(now - 20 * DAY_MS),
    paidAt: iso(refundedPaidAt),
    activatedAt: iso(refundedPaidAt + 60_000),
    cancelledAt: null,
    refundedAt: iso(now - 12 * DAY_MS),
    grant: null,
  }
  refundedOrder.grant = grantFor(refundedOrder, iso(refundedExpiry), iso(now - 12 * DAY_MS))

  const activePaidAt = now - 9 * DAY_MS
  const activeExpiry = activePaidAt + team.leaseTtlSeconds * 1000
  const activeOrder: Order = {
    id: 'ord-04',
    tenantId: 'tenant-03',
    userId: 'u-02',
    planId: team.id,
    plan: team,
    provider: 'manual',
    state: 'active',
    providerRef: 'OFFLINE-2026-1016-04',
    reviewRequired: false,
    createdAt: iso(now - 10 * DAY_MS),
    paidAt: iso(activePaidAt),
    activatedAt: iso(activePaidAt + 45_000),
    cancelledAt: null,
    refundedAt: null,
    grant: null,
  }
  activeOrder.grant = grantFor(activeOrder, iso(activeExpiry), null)

  const chargedBackOrder: Order = {
    id: 'ord-05',
    tenantId: 'tenant-03',
    userId: 'u-02',
    planId: standard.id,
    plan: standard,
    provider: 'manual',
    state: 'charged_back',
    providerRef: 'OFFLINE-2026-0916-05',
    reviewRequired: true,
    createdAt: iso(now - 40 * DAY_MS),
    paidAt: iso(now - 39 * DAY_MS),
    activatedAt: iso(now - 39 * DAY_MS + 30_000),
    cancelledAt: null,
    refundedAt: null,
    grant: null,
  }
  chargedBackOrder.grant = grantFor(chargedBackOrder, iso(now - 39 * DAY_MS + standard.leaseTtlSeconds * 1000), iso(now - 3 * DAY_MS))

  // One active order whose grant lease was ended by a daemon restart. The
  // paid term is still unexpired, so the portal can offer the restore action;
  // the reason is the same machine value the daemon records in leaseEnds.
  const interruptedPaidAt = now - 4 * DAY_MS
  const interruptedOrder: Order = {
    id: 'ord-08',
    tenantId: 'tenant-01',
    userId: 'u-01',
    planId: standard.id,
    plan: standard,
    provider: 'manual',
    state: 'active',
    providerRef: 'OFFLINE-2026-1022-08',
    reviewRequired: false,
    createdAt: iso(now - 5 * DAY_MS),
    paidAt: iso(interruptedPaidAt),
    activatedAt: iso(interruptedPaidAt + 30_000),
    cancelledAt: null,
    refundedAt: null,
    grant: null,
  }
  interruptedOrder.grant = grantFor(
    interruptedOrder,
    iso(interruptedPaidAt + standard.leaseTtlSeconds * 1000),
    null,
  )
  interruptedOrder.grant.leaseEndedReason = 'daemon_restart'
  interruptedOrder.grant.leaseEndedAt = iso(now - 2 * DAY_MS)

  return [
    {
      id: 'ord-03',
      tenantId: 'tenant-03',
      userId: 'u-02',
      planId: starter.id,
      plan: starter,
      provider: 'epay',
      state: 'paid',
      providerRef: 'EPAY-2026-1024-03',
      reviewRequired: false,
      createdAt: iso(now - DAY_MS),
      paidAt: iso(now - DAY_MS + 3_600_000),
      activatedAt: null,
      cancelledAt: null,
      refundedAt: null,
      grant: null,
      // Queued durable work: the admin table must show the schedule and must
      // not offer the legacy settlement replay beside it.
      fulfilment: {
        orderId: 'ord-03',
        mode: 'initial',
        state: 'queued',
        attempts: 0,
        entitlementExpiresAt: iso(now - DAY_MS + 3_600_000 + starter.leaseTtlSeconds * 1000),
        nextAttemptAt: iso(now + 60_000),
        lastReason: 'capacity',
        updatedAt: iso(now - 60_000),
      },
    },
    {
      id: 'ord-09',
      tenantId: 'tenant-03',
      userId: 'u-02',
      planId: starter.id,
      plan: starter,
      provider: 'epay',
      state: 'paid',
      providerRef: 'EPAY-2026-1018-09',
      reviewRequired: false,
      createdAt: iso(now - 3 * DAY_MS),
      paidAt: iso(now - 3 * DAY_MS + 3_600_000),
      activatedAt: null,
      cancelledAt: null,
      refundedAt: null,
      grant: null,
      // Terminal dead letter: no automatic retry, so an operator must resolve
      // it offline. The portal tenant never sees this row.
      fulfilment: {
        orderId: 'ord-09',
        mode: 'initial',
        state: 'dead_letter',
        attempts: 8,
        entitlementExpiresAt: iso(now - 3 * DAY_MS + 3_600_000 + starter.leaseTtlSeconds * 1000),
        lastReason: 'publish_failed',
        blocked: true,
        updatedAt: iso(now - 2 * DAY_MS),
      },
    },
    {
      id: 'ord-10',
      tenantId: 'tenant-03',
      userId: 'u-02',
      planId: starter.id,
      plan: starter,
      provider: 'epay',
      state: 'paid',
      providerRef: 'EPAY-2026-1026-10',
      reviewRequired: true,
      createdAt: iso(now - 2 * DAY_MS),
      paidAt: iso(now - 2 * DAY_MS + 3_600_000),
      activatedAt: null,
      cancelledAt: null,
      refundedAt: null,
      grant: null,
      // A published attempt whose withdrawal failed: cleanup pending outranks
      // the publishing state in the badge.
      fulfilment: {
        orderId: 'ord-10',
        mode: 'initial',
        state: 'publishing',
        attempts: 2,
        entitlementExpiresAt: iso(now - 2 * DAY_MS + 3_600_000 + starter.leaseTtlSeconds * 1000),
        nextAttemptAt: iso(now + 120_000),
        lastReason: 'publish_failed',
        cleanupPending: true,
        updatedAt: iso(now - 60_000),
      },
    },
    {
      id: 'ord-02',
      tenantId: 'tenant-01',
      userId: 'u-01',
      planId: standard.id,
      plan: standard,
      provider: 'manual',
      state: 'cancelled',
      providerRef: 'OFFLINE-LATE-7781',
      reviewRequired: true,
      createdAt: iso(now - 6 * DAY_MS),
      paidAt: null,
      activatedAt: null,
      cancelledAt: iso(now - 5 * DAY_MS),
      refundedAt: null,
      grant: null,
    },
    refundedOrder,
    activeOrder,
    chargedBackOrder,
    interruptedOrder,
    {
      id: 'ord-06',
      tenantId: 'tenant-01',
      userId: 'u-01',
      planId: starter.id,
      plan: starter,
      provider: 'manual',
      state: 'cancelled',
      providerRef: null,
      reviewRequired: false,
      createdAt: iso(now - 60 * DAY_MS),
      paidAt: null,
      activatedAt: null,
      cancelledAt: iso(now - 59 * DAY_MS),
      refundedAt: null,
      grant: null,
    },
  ]
}

function stateValue(): MockOrderState {
  if (state) return state
  const now = Date.now()
  const plans = seedPlans(now)
  state = {
    plans,
    orders: seedOrders(now, plans),
    planSeq: plans.length,
    orderSeq: 6,
    epayEnabled: true,
    requests: new Map(),
  }
  return state
}

/** Resets plans, orders and idempotency records (tests call this). */
export function resetOrderMock(): void {
  state = null
}

/** Toggles the mock 易支付 adapter so the disabled UI path is testable. */
export function setMockEpayEnabled(enabled: boolean): void {
  stateValue().epayEnabled = enabled
}

/** Read-only view of the mock adapter state for the admin status chip. */
export function mockEpayEnabled(): boolean {
  return stateValue().epayEnabled
}

function sortedOrders(orders: Order[]): Order[] {
  return [...orders].sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt))
}

/** Active catalog for the portal; deactivated plans stay admin-only. */
export function mockListPlans(): Plan[] {
  return clone(stateValue().plans.filter((plan) => plan.active))
}

/** Full catalog for the admin page, including deactivated plans. */
export function mockAdminPlans(): Plan[] {
  return clone(stateValue().plans)
}

/** Own orders only: the daemon derives the owner from the session. */
export function mockListOrders(tenantId: string, userId: string): Order[] {
  return clone(
    sortedOrders(
      stateValue().orders.filter(
        (order) => order.tenantId === tenantId && order.userId === userId,
      ),
    ),
  )
}

export function mockAdminOrders(): Order[] {
  return clone(sortedOrders(stateValue().orders))
}

function openOrderFor(state: MockOrderState, tenantId: string, userId: string): Order | undefined {
  const now = Date.now()
  return state.orders.find(
    (order) =>
      order.tenantId === tenantId &&
      order.userId === userId &&
      (order.state === 'awaiting_payment' ||
        order.state === 'paid' ||
        (order.state === 'active' &&
          order.grant !== null &&
          order.grant.revokedAt === null &&
          !order.grant.leaseEndedReason &&
          Date.parse(order.grant.expiresAt) > now)),
  )
}

export function mockCreateOrder(
  tenantId: string,
  userId: string,
  planId: string,
  idempotencyKey: string,
): Order {
  const state = stateValue()
  const plan = state.plans.find((candidate) => candidate.id === planId)
  if (!plan || !plan.active) throw new ApiError(404, 'Plan not found', 'NOT_FOUND')
  const key = idempotencyKey.trim()
  if (!key) throw new ApiError(400, 'Idempotency-Key is required', 'INVALID_ARGUMENT')

  const mapKey = `${tenantId}:${userId}:${key}`
  const prior = state.requests.get(mapKey)
  if (prior) {
    if (prior.requestHash !== planId) {
      throw new ApiError(409, 'Idempotency-Key reused with a different request', 'CONFLICT')
    }
    const existing = state.orders.find((order) => order.id === prior.orderId)
    if (existing) return clone(existing)
  }

  // Mirror the daemon's create path: an active order for the same plan whose
  // grant lease ended is re-issued, never charged a second time.
  const ended = state.orders.find(
    (order) =>
      order.tenantId === tenantId &&
      order.userId === userId &&
      order.state === 'active' &&
      order.planId === planId &&
      order.grant !== null &&
      order.grant.revokedAt === null &&
      Boolean(order.grant.leaseEndedReason) &&
      Date.parse(order.grant.expiresAt) > Date.now(),
  )
  if (ended) return mockReissueOrder(ended.id)

  if (openOrderFor(state, tenantId, userId)) {
    throw new ApiError(409, 'An open order already exists for this user', 'CONFLICT')
  }

  const now = Date.now()
  state.orderSeq += 1
  const order: Order = {
    id: `ord-${String(state.orderSeq).padStart(2, '0')}`,
    tenantId,
    userId,
    planId: plan.id,
    plan: snapshot(plan),
    provider: 'manual',
    state: 'awaiting_payment',
    providerRef: null,
    reviewRequired: false,
    createdAt: iso(now),
    paidAt: null,
    activatedAt: null,
    cancelledAt: null,
    refundedAt: null,
    grant: null,
  }
  state.orders.unshift(order)
  state.requests.set(mapKey, { requestHash: planId, orderId: order.id })
  return clone(order)
}

function findOrder(id: string): Order {
  const order = stateValue().orders.find((candidate) => candidate.id === id)
  if (!order) throw new ApiError(404, 'Order not found', 'NOT_FOUND')
  return order
}

export function mockCancelOrder(
  id: string,
  owner?: { tenantId: string; userId: string },
): Order {
  const order = findOrder(id)
  if (owner && (order.tenantId !== owner.tenantId || order.userId !== owner.userId)) {
    throw new ApiError(404, 'Order not found', 'NOT_FOUND')
  }
  if (order.state === 'cancelled') return clone(order)
  if (order.state !== 'awaiting_payment') {
    throw new ApiError(409, 'Only an awaiting-payment order can be cancelled', 'CONFLICT')
  }
  order.state = 'cancelled'
  order.cancelledAt = iso(Date.now())
  return clone(order)
}

/**
 * POST /v1/portal/orders/{id}/reissue — restore service for an own active
 * order whose grant lease ended. Mirrors the daemon's single-grant contract:
 * the order and payment stay untouched, and the same grant points at one
 * replacement lease with its original paid expiry.
 */
export function mockReissueOrder(
  id: string,
  owner?: { tenantId: string; userId: string },
): Order {
  const state = stateValue()
  const order = findOrder(id)
  if (owner && (order.tenantId !== owner.tenantId || order.userId !== owner.userId)) {
    throw new ApiError(404, 'Order not found', 'NOT_FOUND')
  }
  if (order.state !== 'active' || !order.grant) {
    throw new ApiError(409, 'Only an active order can restore service', 'CONFLICT')
  }
  if (order.grant.revokedAt !== null) {
    throw new ApiError(409, 'The entitlement was revoked', 'CONFLICT')
  }
  if (!order.grant.leaseEndedReason) {
    throw new ApiError(409, 'The entitlement lease is still active', 'CONFLICT')
  }
  if (Date.parse(order.grant.expiresAt) <= Date.now()) {
    throw new ApiError(409, 'The entitlement has expired', 'CONFLICT')
  }

  const used = new Set<string>()
  for (const candidate of state.orders) {
    if (candidate.grant) used.add(candidate.grant.leaseId)
  }
  let seq = 1
  let leaseId = `lease-${order.id}-r${seq}`
  while (used.has(leaseId)) {
    seq += 1
    leaseId = `lease-${order.id}-r${seq}`
  }
  order.grant.leaseId = leaseId
  delete order.grant.leaseEndedReason
  delete order.grant.leaseEndedAt
  return clone(order)
}

/**
 * Build a deterministic hosted-checkout URL for an own awaiting order. A
 * checkout records provider intent only; it never changes the state to paid.
 */
export function mockCheckoutOrder(
  id: string,
  type: 'alipay' | 'wxpay',
  owner?: { tenantId: string; userId: string },
): { payUrl: string } {
  const state = stateValue()
  const order = findOrder(id)
  if (owner && (order.tenantId !== owner.tenantId || order.userId !== owner.userId)) {
    throw new ApiError(404, 'Order not found', 'NOT_FOUND')
  }
  if (order.state !== 'awaiting_payment') {
    throw new ApiError(409, 'Only awaiting-payment orders can be checked out', 'CONFLICT')
  }
  if (!state.epayEnabled) {
    throw new ApiError(503, 'epay checkout is not configured', 'UPSTREAM_UNAVAILABLE')
  }
  order.provider = 'epay'
  const money = `${Math.floor(order.plan.amountMinor / 100)}.${String(order.plan.amountMinor % 100).padStart(2, '0')}`
  return {
    payUrl:
      `https://pay.example.com/submit.php?pid=1001&type=${encodeURIComponent(type)}` +
      `&out_trade_no=${encodeURIComponent(order.id)}&money=${money}&sign=demo`,
  }
}

function activate(order: Order, now: number): void {
  const paidAt = order.paidAt ? Date.parse(order.paidAt) : now
  order.state = 'active'
  order.activatedAt = iso(now)
  order.grant = {
    ...grantFor(order, iso(paidAt + order.plan.leaseTtlSeconds * 1000), null),
    orderId: order.id,
  }
}

/**
 * Attest an offline receipt. A first attestation records payment only; the
 * next call retries activation, mirroring the daemon's retryable `paid` state.
 */
export function mockSettleOrder(id: string, reference: string): Order {
  const order = findOrder(id)
  const ref = reference.trim()
  if (!ref) throw new ApiError(400, 'A receipt reference is required', 'INVALID_ARGUMENT')
  const now = Date.now()

  if (order.state === 'awaiting_payment') {
    order.state = 'paid'
    order.paidAt = iso(now)
    order.providerRef = ref
    order.reviewRequired = false
  } else if (order.state === 'paid') {
    if (order.providerRef !== null && order.providerRef !== ref) {
      throw new ApiError(409, 'Receipt reference conflicts with the recorded settlement', 'CONFLICT')
    }
    order.providerRef = ref
    activate(order, now)
  } else if (order.state === 'active') {
    // Duplicate settlement is a recorded no-op; the terminal result stands.
  } else {
    // Late settlement on a terminal order never resumes it.
    order.reviewRequired = true
  }
  return clone(order)
}

/** Record an offline refund the operator already completed; full-only. */
export function mockRefundOrder(id: string, reference: string): Order {
  const order = findOrder(id)
  if (!reference.trim()) throw new ApiError(400, 'A refund reference is required', 'INVALID_ARGUMENT')
  const now = Date.now()
  if (order.state === 'paid' || order.state === 'active') {
    order.state = 'refunded'
    order.refundedAt = iso(now)
    order.reviewRequired = false
    // Mirrors cancelFulfilmentLocked: a reversal removes the durable work so a
    // later poll can never resurrect a publication for a refunded order.
    order.fulfilment = null
    if (order.grant && order.grant.revokedAt === null) order.grant.revokedAt = iso(now)
  } else if (order.state === 'awaiting_payment') {
    // Refund evidence without a recorded payment quarantines the order.
    order.reviewRequired = true
  }
  return clone(order)
}

/** Activate/deactivate a plan; existing orders keep their priced snapshot. */
export function mockSetPlanActive(id: string, active: boolean): Plan {
  const plan = stateValue().plans.find((candidate) => candidate.id === id)
  if (!plan) throw new ApiError(404, 'Plan not found', 'NOT_FOUND')
  plan.active = active
  plan.updatedAt = iso(Date.now())
  return clone(plan)
}
