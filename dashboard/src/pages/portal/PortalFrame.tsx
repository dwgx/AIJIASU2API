import clsx from 'clsx'
import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { ApcBrandMark } from '../../components/icons/ApcIcons'
import { useI18n } from '../../lib/i18n'
import type { TranslationParams } from '../../lib/i18n'
import { PortalError } from '../../lib/portal-api'

type Translate = (key: string, params?: TranslationParams) => string

/**
 * Maps a portal failure to copy the customer can act on. Server messages are
 * not shown for 401 (uniform credential refusal) and are replaced with stable
 * portal copy everywhere else, so the UI never leaks daemon internals.
 */
export function portalErrorText(
  error: unknown,
  t: Translate,
  context: 'login' | 'signup' | 'action' = 'action',
): string {
  if (error instanceof PortalError) {
    if (error.status === 401) {
      return context === 'login' ? t('portal.login.invalid') : t('portal.error.sessionExpired')
    }
    if (error.status === 429) {
      return t('portal.error.rateLimited', { seconds: error.retryAfterSec ?? 30 })
    }
    if (error.status === 503) return t('portal.error.unavailable')
    if (error.status === 403) {
      if (error.code === 'TENANT_PENDING') return t('portal.login.pending')
      if (error.code === 'TENANT_SUSPENDED') return t('portal.login.suspended')
      if (error.code === 'TENANT_DISABLED') return t('portal.login.disabled')
      // The daemon reports one FORBIDDEN code for both pending and suspended
      // tenants, so the login page names both possibilities without guessing.
      if (context === 'login') return t('portal.login.inactive')
      return t('portal.error.forbidden')
    }
  }
  return t('portal.error.generic')
}

/**
 * The customer shell: brand bar with the language switch, one centered panel,
 * and no admin navigation. Admin chrome (sidebar, token gate, palette) is
 * deliberately absent so a tenant can never reach an operator surface.
 */
export function PortalFrame({
  title,
  subtitle,
  actions,
  children,
  wide = false,
}: {
  title: string
  subtitle?: string
  actions?: ReactNode
  children: ReactNode
  wide?: boolean
}) {
  const { lang, setLang, t } = useI18n()
  return (
    <div className="portal-shell">
      <header className="portal-bar">
        <Link className="portal-brand" to="/portal">
          <span className="portal-brand__mark" aria-hidden="true">
            <ApcBrandMark size={18} />
          </span>
          <span className="portal-brand__text">
            <span className="portal-brand__name">AJiaSu Proxy</span>
            <span className="portal-brand__sub">{t('portal.brand')}</span>
          </span>
        </Link>
        <div className="portal-bar__actions">
          <div className="lang-switch" role="group" aria-label={t('portal.language')}>
            <button
              type="button"
              className="lang-switch__item"
              aria-label={lang === 'zh' ? 'EN' : '中'}
              title={lang === 'zh' ? t('shell.languageEn') : t('shell.languageZh')}
              onClick={() => setLang(lang === 'zh' ? 'en' : 'zh')}
            >
              {lang === 'zh' ? 'EN' : '中'}
            </button>
          </div>
          {actions}
        </div>
      </header>
      <main className={clsx('portal-main', wide && 'portal-main--wide')}>
        <div className="portal-panel">
          <header className="portal-panel__head">
            <h1 className="portal-title">{title}</h1>
            {subtitle ? <p className="portal-subtitle">{subtitle}</p> : null}
          </header>
          {children}
        </div>
      </main>
      <footer className="portal-foot">{t('portal.footer')}</footer>
    </div>
  )
}
