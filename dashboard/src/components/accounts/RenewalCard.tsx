import clsx from 'clsx'
import { CalendarClock, ExternalLink, RefreshCw } from 'lucide-react'
import { api } from '../../lib/api'
import { formatDate, formatDateTime, formatRelative } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { useAsync } from '../../lib/useAsync'
import type {
  RenewalAccountView,
  RenewalPlan,
  RenewalSource,
  RenewalUrgency,
} from '../../lib/types'
import { Badge } from '../primitives'

/**
 * The vendor's own renewal page. `/pricing` is a 302 alias of it, and the
 * desktop client opens the same page. No token, account id or credential is
 * appended — this is a plain outbound link.
 *
 * The live account read also carries a `renewalUrl`, but the card deliberately
 * ignores it: the constant below is the only href the panel ever renders, so a
 * compromised or redirected daemon response cannot smuggle a token into the
 * outbound link.
 */
export const OFFICIAL_PURCHASE_URL = 'https://www.91ajs.com/purchase'

/** Snapshot date of the catalogue below; prices are not fetched at runtime. */
export const RENEWAL_PRICE_CHECKED_AT = '2026-10-24'

/** Days before expiry at which the vendor client starts prompting renewal. */
export const RENEWAL_WARNING_DAYS = 5

const DAY_MS = 86_400_000

/**
 * Anonymous public catalogue from the official purchase page. APC keeps a
 * snapshot instead of scraping: the page's `pageData` blob is not a documented
 * API, and the official checkout always shows the authoritative amount.
 *
 * The live daemon serves the same table through GET /v1/renewal/plans; this
 * snapshot is the fallback when that read fails.
 */
export const RENEWAL_PLANS: readonly RenewalPlan[] = [
  { id: 'M1', priceCny: 20, saveCny: 5 },
  { id: 'M3', priceCny: 53, saveCny: 22 },
  { id: 'M12', priceCny: 198, saveCny: 102 },
  { id: 'H6', priceCny: 1, trial: true },
]

/** Auto-renew variants of the same catalogue; shown as a note, never purchased here. */
export const RENEWAL_AUTO_PLANS: readonly RenewalPlan[] = [
  { id: 'M1', priceCny: 20 },
  { id: 'M3', priceCny: 53 },
]

export interface RenewalCountdown {
  /** Whole days to expiry, rounded up; negative once past it. */
  days: number
  overdue: boolean
  /** Inside the vendor's five-day renewal window. */
  warning: boolean
}

export function renewalCountdown(expiresAt: string, now = Date.now()): RenewalCountdown {
  const remaining = new Date(expiresAt).getTime() - now
  if (!Number.isFinite(remaining)) return { days: 0, overdue: false, warning: false }
  const overdue = remaining <= 0
  const raw = overdue ? Math.trunc(remaining / DAY_MS) : Math.ceil(remaining / DAY_MS)
  // Normalize -0 so a just-expired membership reports day zero, not a sign.
  const days = raw === 0 ? 0 : raw
  return { days, overdue, warning: remaining <= RENEWAL_WARNING_DAYS * DAY_MS }
}

type Translate = (key: string, params?: Record<string, string | number>) => string

export function renewalStatusText(t: Translate, countdown: RenewalCountdown): string {
  if (countdown.overdue) {
    if (countdown.days === 0) return t('accounts.renewal.expired')
    if (countdown.days === -1) return t('accounts.renewal.expiredDayAgo')
    return t('accounts.renewal.expiredDaysAgo', { days: Math.abs(countdown.days) })
  }
  return countdown.days === 1
    ? t('accounts.renewal.dayLeft')
    : t('accounts.renewal.daysLeft', { days: countdown.days })
}

/**
 * Time-to-expiry bucket for a live `daysLeft` from GET /v1/renewal/accounts.
 * Mirrors the daemon's rules: negative is overdue, zero is due today, and the
 * vendor's five-day warning window is `soon`.
 */
function liveUrgency(daysLeft: number): RenewalUrgency {
  if (!Number.isFinite(daysLeft)) return 'none'
  if (daysLeft < 0) return 'overdue'
  if (daysLeft === 0) return 'due'
  return daysLeft <= RENEWAL_WARNING_DAYS ? 'soon' : 'none'
}

/** Same buckets derived from the local countdown when the live read is absent. */
function staticUrgency(countdown: RenewalCountdown): RenewalUrgency {
  if (countdown.overdue) return 'overdue'
  return countdown.warning ? 'soon' : 'none'
}

/**
 * Period label. Known vendor ids keep their translated label even when the
 * live catalogue ships an English one; unknown ids fall back to the wire label
 * (data, not copy) and finally the id itself.
 */
function planLabel(t: Translate, plan: RenewalPlan): string {
  const key = `accounts.renewal.plan.${plan.id}`
  const translated = t(key)
  if (translated !== key) return translated
  if (plan.label) return plan.label
  if (plan.months != null && plan.months > 0) {
    return t('platform.renewal.months', { months: plan.months })
  }
  return plan.id
}

function urgencyTone(urgency: RenewalUrgency): 'danger' | 'warning' | 'neutral' {
  if (urgency === 'overdue') return 'danger'
  if (urgency === 'due' || urgency === 'soon') return 'warning'
  return 'neutral'
}

function sourceTone(source: RenewalSource): 'success' | 'warning' | 'neutral' {
  if (source === 'live') return 'success'
  if (source === 'cache') return 'warning'
  return 'neutral'
}

/**
 * Expiry, countdown, the public plan catalogue and the official renewal link.
 *
 * Read-only by construction: it fetches the live catalogue and the account's
 * expiry state in parallel, falls back to the built-in snapshot when either
 * read fails, and never posts to the vendor. The purchase link is always the
 * constant public page — the live `renewalUrl` is never rendered.
 */
export function RenewalCard({
  accountId,
  accountLabel,
  expiresAt,
  now,
}: {
  /** Account row id; when present the card also reads live expiry urgency. */
  accountId?: string
  accountLabel: string
  expiresAt: string
  /** Clock override so the countdown and freshness are stable in tests. */
  now?: number
}) {
  const { t, lang } = useI18n()
  const clock = now ?? Date.now()

  const plansState = useAsync(() => api.getRenewalPlans(), [])
  const accountState = useAsync<RenewalAccountView | null>(
    () => (accountId ? api.getRenewalAccount(accountId) : Promise.resolve(null)),
    [accountId],
  )

  const catalogue = plansState.data
  const source: RenewalSource = catalogue?.source ?? 'static'
  const plans = catalogue?.plans ?? RENEWAL_PLANS
  const liveFetchedAt =
    catalogue && catalogue.source !== 'static' && Number.isFinite(Date.parse(catalogue.fetchedAt))
      ? catalogue.fetchedAt
      : null
  const plansFailed = plansState.status === 'error'
  const refreshFailed = catalogue !== undefined && plansState.staleAt !== undefined
  const showRetry = plansFailed || refreshFailed
  const showStale =
    !showRetry &&
    source !== 'static' &&
    liveFetchedAt !== null &&
    (catalogue?.stale === true || source === 'cache')

  const liveAccount =
    accountState.data && accountState.data.accountId === accountId ? accountState.data : null
  const effectiveExpiresAt = liveAccount?.expiresAt ?? expiresAt
  const countdown = renewalCountdown(effectiveExpiresAt, now)
  const urgency = liveAccount ? liveUrgency(liveAccount.daysLeft) : staticUrgency(countdown)
  const daysRemaining = liveAccount
    ? Math.max(0, liveAccount.daysLeft)
    : countdown.overdue
      ? 0
      : countdown.days
  const soonDays = liveAccount ? liveAccount.daysLeft : countdown.days

  const statusText = liveAccount
    ? t(`platform.renewal.urgency.${urgency}`)
    : renewalStatusText(t, countdown)
  const statusTone = liveAccount
    ? urgencyTone(urgency)
    : countdown.overdue
      ? 'danger'
      : countdown.warning
        ? 'warning'
        : 'neutral'

  const warning =
    urgency === 'overdue'
      ? t('accounts.renewal.overdueWarning')
      : urgency === 'due'
        ? t('platform.renewal.dueToday')
        : urgency === 'soon'
          ? soonDays === 1
            ? t('accounts.renewal.expiringWarningTomorrow')
            : t('accounts.renewal.expiringWarning', { days: soonDays })
          : null

  const hasLiveAutoPrices = plans.some((plan) => plan.autoRenewPriceCny != null)

  const retry = () => {
    plansState.reload()
    accountState.reload()
  }

  return (
    <section
      className="renewal-card"
      aria-label={t('accounts.renewal.title')}
      aria-busy={plansState.status === 'loading' || accountState.status === 'loading'}
    >
      <header className="renewal-card__head">
        <span className="renewal-card__title">
          <CalendarClock size={14} aria-hidden="true" />
          {t('accounts.renewal.title')}
        </span>
        <Badge tone={statusTone}>{statusText}</Badge>
      </header>

      {warning ? (
        <p
          className={clsx('renewal-card__warning', urgency === 'overdue' && 'is-overdue')}
          role="status"
        >
          {warning}
        </p>
      ) : null}

      <dl className="facts facts--compact">
        <div>
          <dt>{t('accounts.expires')}</dt>
          <dd>{formatDate(effectiveExpiresAt)}</dd>
        </div>
        <div>
          <dt>{t('accounts.renewal.daysRemaining')}</dt>
          <dd className="mono">{daysRemaining}</dd>
        </div>
      </dl>

      <div className="renewal-card__plans">
        <div className="renewal-card__plans-head">
          <span className="label">{t('accounts.renewal.plansTitle')}</span>
          <span className="renewal-card__meta">
            {liveFetchedAt ? (
              <span className="renewal-card__freshness">
                {t('platform.renewal.updated', { time: formatRelative(liveFetchedAt, clock, lang) })}
              </span>
            ) : null}
            <Badge tone={sourceTone(source)}>{t(`platform.renewal.source.${source}`)}</Badge>
          </span>
        </div>

        {showRetry ? (
          <div className="renewal-card__error" role="status">
            <span>
              {plansFailed || !liveFetchedAt
                ? t('platform.renewal.error.unavailable')
                : t('platform.renewal.stale', { time: formatDateTime(liveFetchedAt) })}
            </span>
            <button type="button" className="btn btn--ghost btn--sm" onClick={retry}>
              <RefreshCw size={12} aria-hidden="true" />
              {t('common.retry')}
            </button>
          </div>
        ) : null}

        {showStale ? (
          <p className="renewal-card__stale">
            {t('platform.renewal.stale', { time: formatDateTime(liveFetchedAt as string) })}
          </p>
        ) : null}

        {plans.length === 0 ? (
          <p className="renewal-card__empty">{t('platform.renewal.empty')}</p>
        ) : (
          <ul className="renewal-plans" aria-label={t('accounts.renewal.plansAria')}>
            {plans.map((plan) => (
              <li className="renewal-plans__row" key={plan.id}>
                <span className="renewal-plans__period">
                  {planLabel(t, plan)}
                  {plan.trial ? <Badge tone="info">{t('accounts.renewal.trial')}</Badge> : null}
                </span>
                {plan.saveCny ? (
                  <span className="renewal-plans__save">
                    {t('accounts.renewal.save', { amount: plan.saveCny })}
                  </span>
                ) : null}
                {plan.autoRenewPriceCny != null ? (
                  <span className="renewal-plans__auto">
                    {t('accounts.renewal.autoRenew')} ¥{plan.autoRenewPriceCny}
                  </span>
                ) : null}
                <span className="renewal-plans__price mono">¥{plan.priceCny}</span>
              </li>
            ))}
          </ul>
        )}

        {hasLiveAutoPrices ? null : (
          <p className="renewal-card__auto">
            <span>{t('accounts.renewal.autoRenew')}</span>{' '}
            <span className="mono">
              {RENEWAL_AUTO_PLANS.map(
                (plan) => `${t(`accounts.renewal.plan.${plan.id}`)} ¥${plan.priceCny}`,
              ).join(' · ')}
            </span>
          </p>
        )}

        {source === 'static' ? (
          <p className="renewal-card__source-note">
            {t('accounts.renewal.priceNote', { date: RENEWAL_PRICE_CHECKED_AT })}
          </p>
        ) : null}
      </div>

      <div className="renewal-card__actions">
        <a
          className="btn btn--primary"
          href={OFFICIAL_PURCHASE_URL}
          target="_blank"
          rel="noreferrer noopener"
          aria-label={t('accounts.renewal.openOfficialFor', { name: accountLabel })}
        >
          <ExternalLink size={13} aria-hidden="true" />
          <span>{t('accounts.renewal.openOfficial')}</span>
        </a>
        <p className="renewal-card__note">{t('accounts.renewal.noPayment')}</p>
      </div>
    </section>
  )
}
