import { RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { DataTable } from '../../components/DataTable'
import type { Column } from '../../components/DataTable'
import { ConfirmDialog } from '../../components/Overlay'
import { OrderStateBadge, formatOrderMoney } from '../../components/orders/OrderState'
import { useToast } from '../../components/Toast'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  SegmentedControl,
  TableSkeleton,
} from '../../components/primitives'
import { formatDate } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type { TranslationParams } from '../../lib/i18n'
import { PortalError, portalApi } from '../../lib/portal-api'
import type { Order } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { portalErrorText } from './PortalFrame'

type Translate = (key: string, params?: TranslationParams) => string

/** Machine reasons the daemon records in its durable leaseEnds log. */
const LEASE_REASON_KEYS: Record<string, string> = {
  daemon_restart: 'portal.orders.reason.daemon_restart',
  lease_ended: 'portal.orders.reason.lease_ended',
  node_unavailable: 'portal.orders.reason.node_unavailable',
  operator_revoke: 'portal.orders.reason.operator_revoke',
  expired: 'portal.orders.reason.expired',
}

/**
 * Hosted-checkout channels the daemon accepts. Stripe is never offered: the
 * backend answers 503 until a secret is configured.
 */
type CheckoutChannel = 'alipay' | 'wxpay'

const CHECKOUT_CHANNELS: ReadonlyArray<CheckoutChannel> = ['alipay', 'wxpay']

const CHANNEL_LABEL_KEYS: Record<CheckoutChannel, string> = {
  alipay: 'portal.orders.channel.alipay',
  wxpay: 'portal.orders.channel.wxpay',
}

const DEFAULT_CHANNEL: CheckoutChannel = 'alipay'

/**
 * True when the order is still paid/active but the lease backing its grant
 * has ended. The daemon computes `leaseEndedReason` on read from the durable
 * leaseEnds log; absence means the grant lease is live (or an older daemon
 * does not project the outcome, in which case the UI must not guess).
 */
function serviceInterrupted(order: Order): boolean {
  return (
    order.state === 'active' &&
    order.grant !== null &&
    order.grant.revokedAt === null &&
    Boolean(order.grant.leaseEndedReason)
  )
}

/** A replacement lease can only be issued while the paid term is unexpired. */
function serviceRestorable(order: Order): boolean {
  return (
    serviceInterrupted(order) &&
    Date.parse(order.grant!.expiresAt) > Date.now()
  )
}

function leaseReasonText(order: Order, t: Translate): string {
  const reason = order.grant?.leaseEndedReason ?? ''
  const key = LEASE_REASON_KEYS[reason]
  return key ? t(key) : t('portal.orders.reason.other', { reason })
}

/**
 * Own orders only. The banner is always present because no order here is
 * evidence of payment: `paid`/`active` can only come from an administrator
 * attesting an offline receipt (or a verified webhook), never from this page.
 */
export default function PortalOrders() {
  const { t, lang } = useI18n()
  const { push } = useToast()
  const orders = useAsync(() => portalApi.orders(), [])
  const [cancelTarget, setCancelTarget] = useState<Order | null>(null)
  const [cancelling, setCancelling] = useState(false)
  const [paying, setPaying] = useState<string | null>(null)
  const [channels, setChannels] = useState<Record<string, CheckoutChannel>>({})
  const [epayDisabled, setEpayDisabled] = useState(false)
  const [restoring, setRestoring] = useState<string | null>(null)

  const reviewRequired = (orders.data ?? []).some((order) => order.reviewRequired)
  const interrupted = (orders.data ?? []).filter(serviceRestorable)

  const pay = async (order: Order) => {
    setPaying(order.id)
    try {
      const { payUrl } = await portalApi.checkoutOrder(
        order.id,
        channels[order.id] ?? DEFAULT_CHANNEL,
      )
      const opened = window.open(payUrl, '_blank')
      if (!opened) {
        push(t('portal.orders.payBlocked'), 'default')
        return
      }
      // The payment page is cross-origin; drop the opener handle immediately.
      try {
        opened.opener = null
      } catch {
        // A browser may expose a read-only opener; the tab still opened.
      }
      // Opening the hosted page is not a payment fact; reload so the recorded
      // provider intent (epay) is reflected, while the state stays awaiting.
      push(t('portal.orders.payOpened'), 'success')
      orders.reload()
    } catch (cause) {
      if (cause instanceof PortalError && cause.status === 503) {
        setEpayDisabled(true)
        push(t('portal.orders.epayDisabled'), 'default')
        return
      }
      push(t('portal.orders.payFailed'), 'danger')
    } finally {
      setPaying(null)
    }
  }

  const cancel = async () => {
    if (!cancelTarget) return
    setCancelling(true)
    try {
      await portalApi.cancelOrder(cancelTarget.id)
      push(t('portal.orders.cancelledToast'), 'success')
      setCancelTarget(null)
      orders.reload()
    } catch {
      push(t('portal.orders.cancelFailed'), 'danger')
    } finally {
      setCancelling(false)
    }
  }

  const restoreService = async (order: Order) => {
    setRestoring(order.id)
    try {
      await portalApi.reissueOrder(order.id)
      push(t('portal.orders.restoredToast'), 'success')
      orders.reload()
    } catch (cause) {
      // A 409 means another tab (or a concurrent retry) already restored the
      // lease, or the paid term expired. Re-read instead of guessing which.
      if (cause instanceof PortalError && cause.status === 409) {
        push(t('portal.orders.restoreConflict'), 'default')
        orders.reload()
        return
      }
      push(cause instanceof PortalError ? portalErrorText(cause, t) : t('portal.orders.restoreFailed'), 'danger')
    } finally {
      setRestoring(null)
    }
  }

  const columns: Array<Column<Order>> = [
    {
      key: 'id',
      label: t('portal.orders.id'),
      render: (order) => <span className="mono">{order.id}</span>,
    },
    {
      key: 'plan',
      label: t('portal.orders.plan'),
      render: (order) => (
        <div className="order-plan">
          <span>{order.plan.name}</span>
          <span className="order-plan__meta">{order.planId}</span>
        </div>
      ),
    },
    {
      key: 'amount',
      label: t('portal.orders.amount'),
      align: 'right',
      render: (order) => formatOrderMoney(order.plan.amountMinor, order.plan.currency, lang),
    },
    {
      key: 'status',
      label: t('portal.orders.status'),
      render: (order) => (
        <div className="order-state">
          <OrderStateBadge order={order} />
          {serviceInterrupted(order) ? (
            <Badge tone="warning" title={t('portal.orders.serviceInterruptedHint')}>
              {t('portal.orders.serviceInterrupted')}
            </Badge>
          ) : null}
          {order.state === 'awaiting_payment' && !order.reviewRequired ? (
            <span className="order-hint">{t('portal.orders.pendingHint')}</span>
          ) : null}
          {order.state === 'paid' && order.fulfilment ? (
            <span className="order-hint">
              {t(
                order.fulfilment.blocked
                  ? 'portal.orders.fulfilmentDelayed'
                  : 'portal.orders.fulfilmentQueued',
              )}
            </span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'created',
      label: t('portal.orders.created'),
      render: (order) => formatDate(order.createdAt),
    },
    {
      key: 'reference',
      label: t('portal.orders.reference'),
      render: (order) =>
        order.providerRef ? <span className="mono order-ref">{order.providerRef}</span> : t('common.none'),
    },
    {
      key: 'expires',
      label: t('portal.orders.expires'),
      render: (order) => {
        if (!order.grant) return t('common.none')
        if (order.grant.revokedAt) return t('portal.orders.grantRevoked')
        return (
          <div className="order-grant">
            <span>{formatDate(order.grant.expiresAt)}</span>
            {serviceInterrupted(order) ? (
              <span
                className="order-hint"
                data-reason={order.grant.leaseEndedReason}
                data-ended-at={order.grant.leaseEndedAt || undefined}
                title={order.grant.leaseEndedAt ? formatDate(order.grant.leaseEndedAt) : undefined}
              >
                {t('portal.orders.leaseEnded', { reason: leaseReasonText(order, t) })}
              </span>
            ) : null}
          </div>
        )
      },
    },
    {
      key: 'actions',
      label: '',
      render: (order) => {
        if (order.state === 'awaiting_payment' && !order.reviewRequired) {
          return (
            <div className="order-actions">
              {epayDisabled ? (
                <span className="order-hint" title={t('portal.orders.epayDisabled')}>
                  {t('portal.orders.offlineSettlement')}
                </span>
              ) : (
                <>
                  <SegmentedControl
                    label={t('portal.orders.channelFor', { id: order.id })}
                    value={channels[order.id] ?? DEFAULT_CHANNEL}
                    options={CHECKOUT_CHANNELS.map((channel) => ({
                      value: channel,
                      label: t(CHANNEL_LABEL_KEYS[channel]),
                    }))}
                    onChange={(next) =>
                      setChannels((current) => ({ ...current, [order.id]: next }))
                    }
                  />
                  <Button
                    variant="primary"
                    loading={paying === order.id}
                    onClick={() => pay(order)}
                  >
                    {paying === order.id ? t('portal.orders.paying') : t('portal.orders.pay')}
                  </Button>
                </>
              )}
              <Button variant="ghost" onClick={() => setCancelTarget(order)}>
                {t('portal.orders.cancel')}
              </Button>
            </div>
          )
        }
        if (serviceRestorable(order)) {
          return (
            <div className="order-actions">
              <Button
                variant="primary"
                icon={<RefreshCw size={14} />}
                loading={restoring === order.id}
                onClick={() => restoreService(order)}
              >
                {restoring === order.id
                  ? t('portal.orders.restoring')
                  : t('portal.orders.restore')}
              </Button>
            </div>
          )
        }
        return null
      },
    },
  ]

  return (
    <div className="stack-lg">
      <div className="portal-note">
        <p>{t('portal.orders.offlineBanner')}</p>
        <p className="portal-note__fine">{t('portal.plans.onlineUnavailable')}</p>
      </div>
      {reviewRequired ? (
        <p className="portal-alert portal-alert--warning" role="status">
          {t('portal.orders.reviewBanner')}
        </p>
      ) : null}
      {interrupted.length > 0 ? (
        <p className="portal-alert portal-alert--warning" role="status">
          {t('portal.orders.serviceInterruptedBanner', { count: interrupted.length })}
        </p>
      ) : null}
      <Card title={t('portal.orders.title')} subtitle={t('portal.orders.subtitle')}>
        {orders.status === 'loading' ? (
          <TableSkeleton rows={3} columns={8} />
        ) : orders.status === 'error' ? (
          <ErrorState message={t('portal.orders.loadFailed')} onRetry={orders.reload} />
        ) : (
          <DataTable
            caption={t('portal.orders.caption')}
            columns={columns}
            rows={orders.data ?? []}
            rowKey={(order) => order.id}
            empty={
              <EmptyState
                title={t('portal.orders.empty')}
                description={t('portal.orders.emptyHint')}
              />
            }
          />
        )}
      </Card>

      <ConfirmDialog
        open={cancelTarget !== null}
        title={t('portal.orders.cancelTitle')}
        description={t('portal.orders.cancelBody')}
        confirmLabel={cancelling ? t('portal.orders.cancelling') : t('portal.orders.cancel')}
        busy={cancelling}
        onConfirm={cancel}
        onCancel={() => {
          if (!cancelling) setCancelTarget(null)
        }}
      />
    </div>
  )
}
