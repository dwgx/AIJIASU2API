import { RefreshCw } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { ConfirmDialog, Modal } from '../components/Overlay'
import {
  OrderStateBadge,
  formatLeaseTerm,
  formatOrderMoney,
} from '../components/orders/OrderState'
import { useToast } from '../components/Toast'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  Input,
  PageHeader,
  Select,
  Switch,
  TableSkeleton,
} from '../components/primitives'
import { api } from '../lib/api'
import { formatBytes, formatDate } from '../lib/format'
import { useI18n } from '../lib/i18n'
import type { Order, OrderState, Plan } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'

type OrderFilter = 'all' | 'review_required' | OrderState

const ORDER_STATES: OrderState[] = [
  'awaiting_payment',
  'paid',
  'active',
  'cancelled',
  'refunded',
  'charged_back',
]

interface PlanTarget {
  plan: Plan
  next: boolean
}

/**
 * Operator settlement surface. Every write records something that already
 * happened offline — an attested receipt, a completed refund, a cancellation —
 * and never claims to charge a card or move money.
 */
export default function Orders() {
  const { t, lang } = useI18n()
  const { push } = useToast()
  const orders = useAsync(() => api.listOrders(), [])
  const plans = useAsync(() => api.listPlans(), [])
  const payment = useAsync(() => api.providerStatus(), [])
  const [filter, setFilter] = useState<OrderFilter>('all')
  const [busy, setBusy] = useState<string | null>(null)
  const [attestTarget, setAttestTarget] = useState<Order | null>(null)
  const [reference, setReference] = useState('')
  const [attestError, setAttestError] = useState('')
  const [cancelTarget, setCancelTarget] = useState<Order | null>(null)
  const [refundTarget, setRefundTarget] = useState<Order | null>(null)
  const [refundReference, setRefundReference] = useState('')
  const [refundError, setRefundError] = useState('')
  const [planTarget, setPlanTarget] = useState<PlanTarget | null>(null)
  const referenceFieldId = useId()
  const refundFieldId = useId()

  const visible = useMemo(() => {
    const rows = orders.data ?? []
    if (filter === 'all') return rows
    if (filter === 'review_required') return rows.filter((order) => order.reviewRequired)
    return rows.filter((order) => order.state === filter)
  }, [orders.data, filter])

  const filterOptions = useMemo(
    () => [
      { value: 'all', label: t('orders.filterAll') },
      { value: 'review_required', label: t('orders.filterReview') },
      ...ORDER_STATES.map((state) => ({
        value: state,
        label: t(`order.state.${state}`),
      })),
    ],
    [t],
  )

  const openAttest = (order: Order) => {
    setAttestTarget(order)
    setReference(order.providerRef ?? '')
    setAttestError('')
  }

  const openRefund = (order: Order) => {
    setRefundTarget(order)
    setRefundReference('')
    setRefundError('')
  }

  const closeRefund = () => {
    if (busy === 'refund') return
    setRefundTarget(null)
    setRefundError('')
  }

  const closeAttest = () => {
    if (busy === 'settle') return
    setAttestTarget(null)
    setAttestError('')
  }

  const submitAttest = async () => {
    if (!attestTarget) return
    const value = reference.trim()
    if (!value) {
      setAttestError(t('orders.referenceRequired'))
      return
    }
    const retrying = attestTarget.state === 'paid'
    setBusy('settle')
    setAttestError('')
    try {
      await api.settleOrder(attestTarget.id, value)
      push(
        t(retrying ? 'orders.retriedToast' : 'orders.settledToast', { id: attestTarget.id }),
        'success',
      )
      setAttestTarget(null)
      orders.reload()
    } catch {
      setAttestError(t(retrying ? 'orders.retryFailed' : 'orders.settleFailed'))
    } finally {
      setBusy(null)
    }
  }

  const submitCancel = async () => {
    if (!cancelTarget) return
    setBusy('cancel')
    try {
      await api.cancelOrder(cancelTarget.id)
      push(t('orders.cancelledToast', { id: cancelTarget.id }), 'success')
      setCancelTarget(null)
      orders.reload()
    } catch {
      push(t('orders.cancelFailed'), 'danger')
    } finally {
      setBusy(null)
    }
  }

  const submitRefund = async () => {
    if (!refundTarget) return
    const value = refundReference.trim()
    if (!value) {
      setRefundError(t('orders.refundReferenceRequired'))
      return
    }
    setBusy('refund')
    setRefundError('')
    try {
      await api.refundOrder(refundTarget.id, value)
      push(t('orders.refundedToast', { id: refundTarget.id }), 'success')
      setRefundTarget(null)
      orders.reload()
    } catch {
      setRefundError(t('orders.refundFailed'))
    } finally {
      setBusy(null)
    }
  }

  const submitPlan = async () => {
    if (!planTarget) return
    setBusy('plan')
    try {
      await api.setPlanActive(planTarget.plan.id, planTarget.next)
      push(
        t(planTarget.next ? 'orders.planActivatedToast' : 'orders.planDeactivatedToast', {
          name: planTarget.plan.name,
        }),
        'success',
      )
      setPlanTarget(null)
      plans.reload()
    } catch {
      push(t('orders.planUpdateFailed'), 'danger')
    } finally {
      setBusy(null)
    }
  }

  const columns: Array<Column<Order>> = [
    {
      key: 'id',
      label: t('orders.id'),
      render: (order) => <span className="mono">{order.id}</span>,
    },
    {
      key: 'tenant',
      label: t('orders.tenant'),
      render: (order) => (
        <div className="order-owner">
          <span>{order.tenantId}</span>
          <span className="order-plan__meta">{order.userId}</span>
        </div>
      ),
    },
    {
      key: 'plan',
      label: t('orders.plan'),
      render: (order) => (
        <div className="order-plan">
          <span>{order.plan.name}</span>
          <span className="order-plan__meta">{order.planId}</span>
        </div>
      ),
    },
    {
      key: 'provider',
      label: t('orders.provider'),
      render: (order) => (
        <div className="order-plan">
          <Badge
            tone={order.provider === 'epay' ? 'success' : order.provider === 'stripe' ? 'info' : 'neutral'}
          >
            {t(
              order.provider === 'manual' || order.provider === 'stripe' || order.provider === 'epay'
                ? `orders.provider.${order.provider}`
                : 'orders.provider.unknown',
            )}
          </Badge>
          <span className="order-plan__meta mono">{order.provider}</span>
        </div>
      ),
    },
    {
      key: 'amount',
      label: t('orders.amount'),
      align: 'right',
      render: (order) => formatOrderMoney(order.plan.amountMinor, order.plan.currency, lang),
    },
    {
      key: 'state',
      label: t('orders.state'),
      render: (order) => (
        <div className="order-state">
          <OrderStateBadge order={order} />
          {order.reviewRequired ? (
            <span className="order-hint">{t('orders.reviewHint')}</span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'created',
      label: t('orders.created'),
      render: (order) => formatDate(order.createdAt),
    },
    {
      key: 'reference',
      label: t('orders.reference'),
      render: (order) =>
        order.providerRef ? <span className="mono order-ref">{order.providerRef}</span> : t('common.none'),
    },
    {
      key: 'expires',
      label: t('orders.grantExpires'),
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
      render: (order) => (
        <div className="order-actions">
          {order.state === 'awaiting_payment' ? (
            <>
              <Button variant="primary" onClick={() => openAttest(order)}>
                {t('orders.settle')}
              </Button>
              <Button variant="ghost" onClick={() => setCancelTarget(order)}>
                {t('orders.cancel')}
              </Button>
            </>
          ) : null}
          {order.state === 'paid' ? (
            <>
              <Button variant="primary" onClick={() => openAttest(order)}>
                {t('orders.retryActivation')}
              </Button>
              <Button variant="ghost" onClick={() => openRefund(order)}>
                {t('orders.refund')}
              </Button>
            </>
          ) : null}
          {order.state === 'active' ? (
            <Button variant="ghost" onClick={() => openRefund(order)}>
              {t('orders.refund')}
            </Button>
          ) : null}
        </div>
      ),
    },
  ]

  const planRows = plans.data ?? []

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('orders.title')}
        subtitle={t('orders.subtitle')}
        refresh={summarizeRefresh([orders, plans, payment])}
        actions={
          <div className="order-actions">
            <Badge
              tone={
                payment.data?.epay === 'enabled'
                  ? 'success'
                  : payment.data?.epay === 'disabled'
                    ? 'neutral'
                    : 'warning'
              }
              title={payment.data?.reason}
            >
              {t(`orders.epay.${payment.data?.epay ?? 'unknown'}`)}
            </Badge>
            <Button
              icon={<RefreshCw size={14} />}
              onClick={() => {
                orders.reload()
                plans.reload()
                payment.reload()
              }}
            >
              {t('common.refresh')}
            </Button>
          </div>
        }
      />

      <div className="orders-grid">
        <Card title={t('orders.plansTitle')} subtitle={t('orders.plansSubtitle')}>
          {plans.status === 'loading' ? (
            <TableSkeleton rows={3} columns={2} />
          ) : plans.status === 'error' ? (
            <ErrorState message={t('orders.plansLoadFailed')} onRetry={plans.reload} />
          ) : planRows.length === 0 ? (
            <EmptyState title={t('orders.plansTitle')} description={t('orders.plansEmpty')} />
          ) : (
            <ul className="order-plan-list" aria-label={t('orders.plansTitle')}>
              {planRows.map((plan) => (
                <li className="order-plan-row" key={plan.id}>
                  <div className="order-plan-row__body">
                    <span className="order-plan-row__name">{plan.name}</span>
                    <span className="order-plan-row__meta">
                      {formatOrderMoney(plan.amountMinor, plan.currency, lang)} ·{' '}
                      {formatLeaseTerm(plan.leaseTtlSeconds, t)} · {formatBytes(plan.quotaBytes)}
                    </span>
                  </div>
                  <Badge tone={plan.active ? 'success' : 'neutral'}>
                    {t(plan.active ? 'orders.planActive' : 'orders.planInactive')}
                  </Badge>
                  <Switch
                    checked={plan.active}
                    label={t('orders.planToggle', { name: plan.name })}
                    disabled={busy !== null}
                    onChange={(next) => setPlanTarget({ plan, next })}
                  />
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card
          title={t('orders.listTitle')}
          actions={
            <Select
              aria-label={t('orders.filter')}
              variant="filter"
              options={filterOptions}
              value={filter}
              onChange={(value) => setFilter(value as OrderFilter)}
            />
          }
        >
          {orders.status === 'loading' ? (
            <TableSkeleton rows={5} columns={10} />
          ) : orders.status === 'error' ? (
            <ErrorState message={t('orders.loadFailed')} onRetry={orders.reload} />
          ) : (
            <DataTable
              caption={t('orders.caption')}
              columns={columns}
              rows={visible}
              rowKey={(order) => order.id}
              empty={
                <EmptyState
                  title={t('orders.empty')}
                  description={
                    filter === 'all' ? t('orders.emptyHint') : t('orders.emptyFiltered')
                  }
                />
              }
            />
          )}
        </Card>
      </div>

      <Modal
        open={attestTarget !== null}
        title={t('orders.settleTitle')}
        description={t('orders.settleBody')}
        onClose={closeAttest}
        footer={
          <>
            <Button variant="ghost" onClick={closeAttest} disabled={busy === 'settle'}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="primary"
              loading={busy === 'settle'}
              onClick={submitAttest}
            >
              {busy === 'settle' ? t('orders.settling') : t('orders.settleConfirm')}
            </Button>
          </>
        }
      >
        <div className="form">
          <Field
            label={t('orders.referenceLabel')}
            htmlFor={referenceFieldId}
            hint={t('orders.referenceHint')}
          >
            <Input
              id={referenceFieldId}
              value={reference}
              onChange={(event) => setReference(event.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </Field>
          {attestError ? (
            <p className="form-error" role="alert">
              {attestError}
            </p>
          ) : null}
        </div>
      </Modal>

      <ConfirmDialog
        open={cancelTarget !== null}
        title={t('orders.cancelTitle')}
        description={t('orders.cancelBody')}
        confirmLabel={t('orders.cancelConfirm')}
        busy={busy === 'cancel'}
        onConfirm={submitCancel}
        onCancel={() => {
          if (busy !== 'cancel') setCancelTarget(null)
        }}
      />

      <Modal
        open={refundTarget !== null}
        title={t('orders.refundTitle')}
        description={t('orders.refundBody')}
        onClose={closeRefund}
        footer={
          <>
            <Button variant="ghost" onClick={closeRefund} disabled={busy === 'refund'}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="danger"
              loading={busy === 'refund'}
              onClick={submitRefund}
            >
              {busy === 'refund' ? t('orders.refunding') : t('orders.refundConfirm')}
            </Button>
          </>
        }
      >
        <div className="form">
          <Field
            label={t('orders.refundReferenceLabel')}
            htmlFor={refundFieldId}
            hint={t('orders.refundReferenceHint')}
          >
            <Input
              id={refundFieldId}
              value={refundReference}
              onChange={(event) => setRefundReference(event.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </Field>
          {refundError ? (
            <p className="form-error" role="alert">
              {refundError}
            </p>
          ) : null}
        </div>
      </Modal>

      <ConfirmDialog
        open={planTarget !== null}
        title={
          planTarget
            ? t(planTarget.next ? 'orders.planActivateTitle' : 'orders.planDeactivateTitle', {
                name: planTarget.plan.name,
              })
            : ''
        }
        description={t(
          planTarget?.next ? 'orders.planActivateBody' : 'orders.planDeactivateBody',
        )}
        confirmLabel={t(
          planTarget?.next ? 'orders.planActivateConfirm' : 'orders.planDeactivateConfirm',
        )}
        tone={planTarget?.next ? 'primary' : 'danger'}
        busy={busy === 'plan'}
        onConfirm={submitPlan}
        onCancel={() => {
          if (busy !== 'plan') setPlanTarget(null)
        }}
      />
    </div>
  )
}
