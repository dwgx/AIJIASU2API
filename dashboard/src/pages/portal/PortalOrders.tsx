import { useState } from 'react'
import { DataTable } from '../../components/DataTable'
import type { Column } from '../../components/DataTable'
import { ConfirmDialog } from '../../components/Overlay'
import { OrderStateBadge, formatOrderMoney } from '../../components/orders/OrderState'
import { useToast } from '../../components/Toast'
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  TableSkeleton,
} from '../../components/primitives'
import { formatDate } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { portalApi } from '../../lib/portal-api'
import type { Order } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'

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

  const reviewRequired = (orders.data ?? []).some((order) => order.reviewRequired)

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
          {order.state === 'awaiting_payment' && !order.reviewRequired ? (
            <span className="order-hint">{t('portal.orders.pendingHint')}</span>
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
      render: (order) =>
        order.grant
          ? order.grant.revokedAt
            ? t('portal.orders.grantRevoked')
            : formatDate(order.grant.expiresAt)
          : t('common.none'),
    },
    {
      key: 'actions',
      label: '',
      render: (order) =>
        order.state === 'awaiting_payment' && !order.reviewRequired ? (
          <Button
            variant="ghost"
            onClick={() => setCancelTarget(order)}
          >
            {t('portal.orders.cancel')}
          </Button>
        ) : null,
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
