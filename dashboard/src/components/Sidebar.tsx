import clsx from 'clsx'
import { ChevronDown, Moon, Sun, Waypoints } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { NavLink } from 'react-router-dom'
import { isMockMode, api, apiEndpointLabel } from '../lib/api'
import { useI18n } from '../lib/i18n'
import { NAV_SECTIONS } from '../lib/nav'
import { useTheme } from '../lib/theme'
import { useAsync } from '../lib/useAsync'
import { IconButton, StatusDot } from './primitives'

export function Sidebar({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { theme, toggle } = useTheme()
  const { lang, setLang, t } = useI18n()
  const [workspaceOpen, setWorkspaceOpen] = useState(false)
  const workspaceRef = useRef<HTMLDivElement>(null)
  const runtime = useAsync(() => api.getLabOverview(), [])

  useEffect(() => {
    if (!workspaceOpen) return
    const onPointerDown = (event: MouseEvent) => {
      if (!workspaceRef.current?.contains(event.target as Node)) setWorkspaceOpen(false)
    }
    document.addEventListener('mousedown', onPointerDown)
    return () => document.removeEventListener('mousedown', onPointerDown)
  }, [workspaceOpen])

  return (
    <aside className={clsx('sidebar', open && 'is-open')} aria-label={t('shell.primaryNav')}>
      <div className="sidebar__brand">
        <span className="brand__mark" aria-hidden="true">
          <Waypoints size={18} />
        </span>
        <span className="brand__text">
          <span className="brand__name">AJiaSu</span>
          <span className="brand__sub">{t('shell.brandSub')}</span>
        </span>
      </div>

      <div className="workspace" ref={workspaceRef}>
        <button
          type="button"
          className="workspace__trigger"
          aria-expanded={workspaceOpen}
          aria-haspopup="menu"
          onClick={() => setWorkspaceOpen((value) => !value)}
        >
          <span className="workspace__avatar">PW</span>
          <span className="workspace__text">
            <span className="workspace__name">{t('shell.workspaceName')}</span>
            <span className="workspace__meta">{t('shell.workspaceMeta')}</span>
          </span>
          <ChevronDown size={14} className="workspace__chevron" aria-hidden="true" />
        </button>
        {workspaceOpen ? (
          <div className="workspace__menu" role="menu">
            <button type="button" role="menuitem" className="workspace__item is-active" onClick={() => setWorkspaceOpen(false)}>
              {t('shell.workspaceName')}
              <span className="workspace__tag">{t('shell.workspaceCurrent')}</span>
            </button>
            {isMockMode() ? (
              <button type="button" role="menuitem" className="workspace__item" disabled>
                {t('shell.workspaceLab')}
                <span className="workspace__tag">{t('shell.workspaceMockOnly')}</span>
              </button>
            ) : null}
          </div>
        ) : null}
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
                  <item.icon size={17} aria-hidden="true" />
                  <span className="nav-item__label sidebar__label">{label}</span>
                </NavLink>
              )
            })}
          </div>
        ))}
      </nav>

      <footer className="sidebar__foot">
        <div className="daemon">
          <StatusDot tone="success" pulse />
          <div className="daemon__text">
            <span className="daemon__name">ajs-accountd</span>
            <span className="daemon__addr">{apiEndpointLabel()}</span>
          </div>
        </div>
        <div className="sidebar__meta">
          <span className="sidebar__version">
            v{runtime.data?.version || '0.1.0'} ·{' '}
            {isMockMode() ? t('shell.modeMock') : t('shell.modeLive')}
          </span>
          <div className="lang-switch" role="group" aria-label={t('shell.language')}>
            <button
              type="button"
              className={clsx('lang-switch__item', lang === 'zh' && 'is-active')}
              aria-pressed={lang === 'zh'}
              title={t('shell.languageZh')}
              onClick={() => setLang('zh')}
            >
              中
            </button>
            <button
              type="button"
              className={clsx('lang-switch__item', lang === 'en' && 'is-active')}
              aria-pressed={lang === 'en'}
              title={t('shell.languageEn')}
              onClick={() => setLang('en')}
            >
              EN
            </button>
          </div>
          <IconButton
            label={theme === 'dark' ? t('shell.toLightTheme') : t('shell.toDarkTheme')}
            icon={theme === 'dark' ? <Sun size={15} /> : <Moon size={15} />}
            onClick={toggle}
            size="sm"
          />
        </div>
      </footer>
    </aside>
  )
}
