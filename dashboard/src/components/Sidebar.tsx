import clsx from 'clsx'
import { NavLink } from 'react-router-dom'
import { useI18n } from '../lib/i18n'
import { NAV_SECTIONS } from '../lib/nav'
import { useTheme } from '../lib/theme'
import { ApcBrandMark } from './icons/ApcIcons'
import { MorphIcon } from './icons/MorphIcon'
import { HAN_GLYPH, LATIN_GLYPH, MOON_GLYPH, SUN_GLYPH } from './icons/morphGlyphs'
import { IconButton } from './primitives'

export function Sidebar({
  open,
  collapsed,
  onClose,
}: {
  open: boolean
  collapsed: boolean
  onClose: () => void
}) {
  const { theme, toggle } = useTheme()
  const { lang, setLang, t } = useI18n()

  return (
    <aside
      id="app-sidebar"
      className={clsx('sidebar', open && 'is-open', collapsed && 'is-collapsed')}
      aria-label={t('shell.primaryNav')}
    >
      <div className="sidebar__brand" title="AJiaSu Proxy Console">
        <span className="brand__mark" aria-hidden="true">
          <ApcBrandMark size={18} />
        </span>
        <span className="brand__text">
          <span className="brand__name">APC</span>
          <span className="brand__sub">AJiaSu Proxy Console</span>
        </span>
      </div>

      <nav className="sidebar__nav">
        {NAV_SECTIONS.map((section) => (
          <div className="sidebar__section" key={section.labelKey}>
            <p className="sidebar__section-label">{t(section.labelKey)}</p>
            {section.items.map((item) => {
              const label = t(item.labelKey)
              return (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.to === '/'}
                  className={({ isActive }) => clsx('nav-item', isActive && 'is-active')}
                  onClick={onClose}
                  title={label}
                >
                  {item.glyph ? (
                    <MorphIcon icon={item.glyph} size={17} strokeWidth={1.6} />
                  ) : (
                    <item.icon size={17} aria-hidden="true" />
                  )}
                  <span className="nav-item__label sidebar__label">{label}</span>
                </NavLink>
              )
            })}
          </div>
        ))}
      </nav>

      <footer className="sidebar__foot">
        <div className="sidebar__meta">
          <div className="lang-switch" role="group" aria-label={t('shell.language')}>
            <button
              type="button"
              className="lang-switch__item"
              aria-label={lang === 'zh' ? 'EN' : '中'}
              title={lang === 'zh' ? t('shell.languageEn') : t('shell.languageZh')}
              onClick={() => setLang(lang === 'zh' ? 'en' : 'zh')}
              style={{ display: 'inline-flex', alignItems: 'center', justifyContent: 'center' }}
            >
              <MorphIcon
                icon={lang === 'zh' ? LATIN_GLYPH : HAN_GLYPH}
                size={14}
                strokeWidth={1.8}
              />
            </button>
          </div>
          <IconButton
            label={theme === 'dark' ? t('shell.toLightTheme') : t('shell.toDarkTheme')}
            icon={
              <MorphIcon
                icon={theme === 'dark' ? SUN_GLYPH : MOON_GLYPH}
                size={15}
                strokeWidth={1.6}
              />
            }
            onClick={toggle}
            size="sm"
          />
        </div>
        <p className="sidebar__credit" title="AJiaSu Proxy Console">
          <span className="sidebar__credit-mark">APC</span>
          <span className="sidebar__credit-text">powered by dwgx</span>
        </p>
      </footer>
    </aside>
  )
}
