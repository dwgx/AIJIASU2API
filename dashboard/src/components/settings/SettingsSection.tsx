import { useI18n } from '../../lib/i18n'
import { Button } from '../primitives'
import type { ReactNode } from 'react'
import type { SettingsSectionId } from '../../lib/settingsSections'

interface SettingsSectionProps {
  id: SettingsSectionId
  title: string
  description: string
  children: ReactNode
  /** Only sections with editable fields pass this; others stay read-only. */
  onReset?: () => void
  dirty?: boolean
}

/**
 * One anchored settings section: heading, one-line job description and an
 * optional section-scoped reset. The id is the deep-link target.
 */
export function SettingsSection({
  id,
  title,
  description,
  children,
  onReset,
  dirty = false,
}: SettingsSectionProps) {
  const { t } = useI18n()
  const headingId = `settings-section-${id}-title`

  return (
    <section
      id={id}
      className="settings-section"
      aria-labelledby={headingId}
      data-section-id={id}
    >
      <header className="settings-section__head">
        <div className="settings-section__heading">
          <h2 className="settings-section__title" id={headingId}>
            {title}
          </h2>
          <p className="settings-section__description">{description}</p>
        </div>
        {onReset ? (
          <Button size="sm" variant="ghost" onClick={onReset} disabled={!dirty}>
            {t('settings.section.reset')}
          </Button>
        ) : null}
      </header>
      <div className="settings-section__body">{children}</div>
    </section>
  )
}
