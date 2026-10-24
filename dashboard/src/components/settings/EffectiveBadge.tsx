import clsx from 'clsx'
import { useI18n } from '../../lib/i18n'

/**
 * Provenance label for a settings value. `stored` and `later` are the honest
 * states this build uses most: the value is persisted but nothing reads it
 * yet, or the capability does not exist. The text is always visible — colour
 * is never the only signal — and `data-reason` carries the same sentence for
 * tests and assistive tech.
 */
export type EffectiveKind = 'applied' | 'stored' | 'locked' | 'later'

const BADGE_KEY: Record<EffectiveKind, string> = {
  applied: 'settings.badge.applied',
  stored: 'settings.badge.stored',
  locked: 'settings.badge.locked',
  later: 'settings.badge.later',
}

export function EffectiveBadge({
  kind,
  reason,
}: {
  kind: EffectiveKind
  reason?: string
}) {
  const { t } = useI18n()
  return (
    <span
      className={clsx('effective-badge', `effective-badge--${kind}`)}
      data-effective={kind}
      data-reason={reason ?? ''}
      title={reason}
    >
      <span className="effective-badge__dot" aria-hidden="true" />
      {t(BADGE_KEY[kind])}
    </span>
  )
}
