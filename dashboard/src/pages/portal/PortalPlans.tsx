import { useState } from 'react'
import { formatBytes } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { PortalError, newOrderIdempotencyKey, portalApi } from '../../lib/portal-api'
import type { Plan } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { Modal } from '../../components/Overlay'
import { formatLeaseTerm, formatOrderMoney } from '../../components/orders/OrderState'
import { useToast } from '../../components/Toast'
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  Skeleton,
} from '../../components/primitives'

/**
 * Plan catalogue and order creation. Creating an order records a request only:
 * the dialog says so, and the success toast never claims a payment happened.
 * One idempotency key is kept per open dialog so a retry replays the order.
 */
export default function PortalPlans({ onCreated }: { onCreated: () => void }) {
  const { t, lang } = useI18n()
  const { push } = useToast()
  const plans = useAsync(() => portalApi.plans(), [])
  const [selected, setSelected] = useState<Plan | null>(null)
  const [idempotencyKey, setIdempotencyKey] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')

  const open = (plan: Plan) => {
    setSelected(plan)
    setIdempotencyKey(newOrderIdempotencyKey())
    setError('')
  }

  const close = () => {
    if (creating) return
    setSelected(null)
    setError('')
  }

  const submit = async () => {
    if (!selected) return
    setCreating(true)
    setError('')
    try {
      await portalApi.createOrder(selected.id, idempotencyKey)
      push(t('portal.plans.created'), 'success')
      setSelected(null)
      onCreated()
    } catch (cause) {
      setError(
        cause instanceof PortalError && cause.status === 409
          ? t('portal.plans.openOrder')
          : t('portal.plans.createFailed'),
      )
    } finally {
      setCreating(false)
    }
  }

  return (
    <Card title={t('portal.plans.title')} subtitle={t('portal.plans.subtitle')}>
      {plans.status === 'loading' ? (
        <div className="portal-loading" aria-busy="true">
          <Skeleton height={92} />
          <Skeleton height={92} />
        </div>
      ) : plans.status === 'error' ? (
        <ErrorState message={t('portal.plans.loadFailed')} onRetry={plans.reload} />
      ) : (plans.data ?? []).length === 0 ? (
        <EmptyState
          title={t('portal.plans.empty')}
          description={t('portal.plans.emptyHint')}
        />
      ) : (
        <ul className="portal-plan-grid" aria-label={t('portal.plans.caption')}>
          {(plans.data ?? []).map((plan) => (
            <li className="portal-plan" key={plan.id}>
              <div className="portal-plan__head">
                <h3 className="portal-plan__name">{plan.name}</h3>
                <span className="portal-plan__price">
                  {formatOrderMoney(plan.amountMinor, plan.currency, lang)}
                </span>
              </div>
              <dl className="portal-plan__meta">
                <div>
                  <dt>{t('portal.plans.term')}</dt>
                  <dd>{formatLeaseTerm(plan.leaseTtlSeconds, t)}</dd>
                </div>
                <div>
                  <dt>{t('portal.plans.quota')}</dt>
                  <dd>{formatBytes(plan.quotaBytes)}</dd>
                </div>
              </dl>
              <Button
                variant="primary"
                aria-label={t('portal.plans.createFor', { name: plan.name })}
                onClick={() => open(plan)}
              >
                {t('portal.plans.create')}
              </Button>
            </li>
          ))}
        </ul>
      )}
      <p className="portal-note__fine">{t('portal.plans.onlineUnavailable')}</p>

      <Modal
        open={selected !== null}
        title={t('portal.plans.createTitle')}
        description={t('portal.plans.createBody')}
        onClose={close}
        footer={
          <>
            <Button variant="ghost" onClick={close} disabled={creating}>
              {t('common.cancel')}
            </Button>
            <Button variant="primary" loading={creating} onClick={submit}>
              {creating ? t('portal.plans.creating') : t('portal.plans.createConfirm')}
            </Button>
          </>
        }
      >
        {selected ? (
          <dl className="portal-plan__summary">
            <div>
              <dt>{t('portal.plans.name')}</dt>
              <dd>{selected.name}</dd>
            </div>
            <div>
              <dt>{t('portal.plans.price')}</dt>
              <dd>{formatOrderMoney(selected.amountMinor, selected.currency, lang)}</dd>
            </div>
            <div>
              <dt>{t('portal.plans.term')}</dt>
              <dd>{formatLeaseTerm(selected.leaseTtlSeconds, t)}</dd>
            </div>
            <div>
              <dt>{t('portal.plans.quota')}</dt>
              <dd>{formatBytes(selected.quotaBytes)}</dd>
            </div>
          </dl>
        ) : null}
        {error ? (
          <p className="portal-alert" role="alert">
            {error}
          </p>
        ) : null}
      </Modal>
    </Card>
  )
}
