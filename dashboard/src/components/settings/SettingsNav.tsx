import clsx from 'clsx'
import { useI18n } from '../../lib/i18n'
import {
  SETTINGS_GROUPS,
  sectionsForGroup,
} from '../../lib/settingsSections'
import type { SettingsSectionId } from '../../lib/settingsSections'

interface SettingsNavProps {
  activeId: SettingsSectionId
  dirtySections: ReadonlySet<SettingsSectionId>
  onSelect: (id: SettingsSectionId) => void
}

/**
 * Sticky table of contents for Settings. Desktop: grouped vertical rail with
 * a dirty dot per section. Under 900px the same markup becomes a horizontal
 * scroll strip; group labels are hidden by CSS, not by a second component.
 */
export function SettingsNav({ activeId, dirtySections, onSelect }: SettingsNavProps) {
  const { t } = useI18n()

  return (
    <nav className="settings-nav" aria-label={t('settings.nav.label')}>
      {SETTINGS_GROUPS.map((group) => (
        <div className="settings-nav__group" key={group.id}>
          <p className="settings-nav__group-label">{t(group.labelKey)}</p>
          <ul className="settings-nav__list">
            {sectionsForGroup(group.id).map((section) => {
              const active = section.id === activeId
              const dirty = dirtySections.has(section.id)
              return (
                <li key={section.id}>
                  <a
                    href={`#${section.id}`}
                    className={clsx('settings-nav__item', active && 'is-active')}
                    aria-current={active ? 'location' : undefined}
                    data-dirty={dirty ? 'true' : undefined}
                    data-section-id={section.id}
                    title={dirty ? t('settings.unsavedShort') : undefined}
                    onClick={(event) => {
                      event.preventDefault()
                      onSelect(section.id)
                    }}
                  >
                    <span>{t(section.labelKey)}</span>
                    {dirty ? <span className="settings-nav__dot" aria-hidden="true" /> : null}
                  </a>
                </li>
              )
            })}
          </ul>
        </div>
      ))}
    </nav>
  )
}
