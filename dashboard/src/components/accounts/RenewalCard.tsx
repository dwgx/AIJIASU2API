import clsx from 'clsx'
import { CalendarClock, ExternalLink } from 'lucide-react'
import { formatDate } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type { RenewalPlan } from '../../lib/types'
import { Badge } from '../primitives'

/**
 * The vendor's own renewal page. `/pricing` is a 302 alias of it, and the
 * desktop client opens the same page. No token, account id or credential is
 * appended — this is a plain outbound link.
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
 * Expiry, countdown, the public plan catalogue and the official renewal link.
 * Deliberately read-only: APC never posts to the vendor's payment endpoints and
 * never carries a login token into the link.
 */
export function RenewalCard({
  accountLabel,
  expiresAt,
  now,
}: {
  accountLabel: string
  expiresAt: string
  /** Clock override so the countdown is stable within one render and testable. */
  now?: number
}) {
  const { t } = useI18n()
  const countdown = renewalCountdown(expiresAt, now)

  return (
    <section className="renewal-card" aria-label={t('accounts.renewal.title')}>
      <header className="renewal-card__head">
        <span className="renewal-card__title">
          <CalendarClock size={14} aria-hidden="true" />
          {t('accounts.renewal.title')}
        </span>
        <Badge tone={countdown.overdue ? 'danger' : countdown.warning ? 'warning' : 'neutral'}>
          {renewalStatusText(t, countdown)}
        </Badge>
      </header>

      {countdown.warning ? (
        <p
          className={clsx('renewal-card__warning', countdown.overdue && 'is-overdue')}
          role="status"
        >
          {countdown.overdue
            ? t('accounts.renewal.overdueWarning')
            : countdown.days === 1
              ? t('accounts.renewal.expiringWarningTomorrow')
              : t('accounts.renewal.expiringWarning', { days: countdown.days })}
        </p>
      ) : null}

      <dl className="facts facts--compact">
        <div>
          <dt>{t('accounts.expires')}</dt>
          <dd>{formatDate(expiresAt)}</dd>
        </div>
        <div>
          <dt>{t('accounts.renewal.daysRemaining')}</dt>
          <dd className="mono">{countdown.overdue ? 0 : countdown.days}</dd>
        </div>
      </dl>

      <div className="renewal-card__plans">
        <div className="renewal-card__plans-head">
          <span className="label">{t('accounts.renewal.plansTitle')}</span>
          <span className="renewal-card__source">{t('accounts.renewal.publicPrices')}</span>
        </div>
        <ul className="renewal-plans" aria-label={t('accounts.renewal.plansAria')}>
          {RENEWAL_PLANS.map((plan) => (
            <li className="renewal-plans__row" key={plan.id}>
              <span className="renewal-plans__period">{t(`accounts.renewal.plan.${plan.id}`)}</span>
              {plan.trial ? <Badge tone="info">{t('accounts.renewal.trial')}</Badge> : null}
              {plan.saveCny ? (
                <span className="renewal-plans__save">
                  {t('accounts.renewal.save', { amount: plan.saveCny })}
                </span>
              ) : null}
              <span className="renewal-plans__price mono">¥{plan.priceCny}</span>
            </li>
          ))}
        </ul>
        <p className="renewal-card__auto">
          <span>{t('accounts.renewal.autoRenew')}</span>{' '}
          <span className="mono">
            {RENEWAL_AUTO_PLANS.map(
              (plan) => `${t(`accounts.renewal.plan.${plan.id}`)} ¥${plan.priceCny}`,
            ).join(' · ')}
          </span>
        </p>
        <p className="renewal-card__source-note">
          {t('accounts.renewal.priceNote', { date: RENEWAL_PRICE_CHECKED_AT })}
        </p>
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
