import { Badge } from '../primitives'
import type { Tone } from '../primitives'
import { formatRelative } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type { Language, TranslationParams } from '../../lib/i18n'
import type { FulfilmentJob, FulfilmentState, Order, OrderState } from '../../lib/types'

/** A review flag outranks the stored state: the order needs an operator. */
export function orderStateKey(order: Pick<Order, 'state' | 'reviewRequired'>): string {
  return order.reviewRequired ? 'order.state.review_required' : `order.state.${order.state}`
}

const TONES: Record<OrderState, Tone> = {
  awaiting_payment: 'warning',
  paid: 'info',
  active: 'success',
  cancelled: 'neutral',
  refunded: 'warning',
  charged_back: 'danger',
}

/**
 * Tones by label key, not by state alone: `cleanup_pending` is a modifier on a
 * publishing job and outranks its state the same way `blocked` does.
 */
export const FULFILMENT_TONES: Record<FulfilmentState | 'cleanup_pending', Tone> = {
  queued: 'info',
  publishing: 'info',
  dead_letter: 'danger',
  cleanup_pending: 'danger',
}

/** The badge label, in precedence order: cleanup pending, blocked, state. */
export function fulfilmentLabelKey(job: FulfilmentJob): string {
  if (job.cleanupPending) return 'order.fulfilment.cleanup_pending'
  if (job.blocked) return 'order.fulfilment.blocked'
  return `order.fulfilment.${job.state}`
}

/**
 * Operator detail for one job. A dead-lettered (or blocked) job never retries
 * automatically; a publishing job has no `nextAttemptAt` while its attempt is
 * in flight, so the state is the only evidence of progress.
 */
export function fulfilmentDetail(job: FulfilmentJob, t: Translate, lang: Language = 'en'): string {
  const parts = [t('order.fulfilment.attempts', { count: job.attempts })]
  if (job.blocked || job.state === 'dead_letter') {
    parts.push(t('order.fulfilment.noRetry'))
  } else if (job.nextAttemptAt) {
    parts.push(
      t('order.fulfilment.nextAttempt', {
        time: formatRelative(job.nextAttemptAt, Date.now(), lang),
      }),
    )
  } else if (job.state === 'publishing') {
    parts.push(t('order.fulfilment.inFlight'))
  }
  return parts.join(' · ')
}

/** The durable job as a badge; a null/absent job renders nothing at all. */
export function FulfilmentBadge({ job }: { job?: FulfilmentJob | null }) {
  const { lang, t } = useI18n()
  if (!job) return null
  const titleParts = [fulfilmentDetail(job, t, lang)]
  if (job.lastReason) titleParts.push(t(`order.fulfilment.reason.${job.lastReason}`))
  if (job.lastError) titleParts.push(job.lastError)
  return (
    <Badge
      tone={FULFILMENT_TONES[job.cleanupPending ? 'cleanup_pending' : job.state]}
      title={titleParts.join(' · ')}
    >
      {t(fulfilmentLabelKey(job))}
    </Badge>
  )
}

export function OrderStateBadge({ order }: { order: Pick<Order, 'state' | 'reviewRequired'> }) {
  const { t } = useI18n()
  return (
    <Badge
      tone={order.reviewRequired ? 'danger' : TONES[order.state]}
      title={order.reviewRequired ? t('portal.orders.reviewHint') : undefined}
    >
      {t(orderStateKey(order))}
    </Badge>
  )
}

/** `amountMinor` is integer minor units; the currency comes from the snapshot. */
export function formatOrderMoney(
  amountMinor: number,
  currency: string,
  lang: Language,
): string {
  try {
    return new Intl.NumberFormat(lang === 'zh' ? 'zh-CN' : 'en-US', {
      style: 'currency',
      currency,
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    }).format(amountMinor / 100)
  } catch {
    return `${(amountMinor / 100).toFixed(2)} ${currency}`
  }
}

type Translate = (key: string, params?: TranslationParams) => string

/** Whole days when possible, then whole hours, then minutes. */
export function formatLeaseTerm(seconds: number, t: Translate): string {
  if (seconds % 86_400 === 0) return t('order.termDays', { count: seconds / 86_400 })
  if (seconds % 3_600 === 0) return t('order.termHours', { count: seconds / 3_600 })
  return t('order.termMinutes', { count: Math.max(1, Math.round(seconds / 60)) })
}
