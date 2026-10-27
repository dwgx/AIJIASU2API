import clsx from 'clsx'
import { Menu, Moon, Search, Sun, X } from 'lucide-react'
import { Suspense, useEffect, useMemo, useState } from 'react'
import { Link, NavLink, Outlet, useLocation } from 'react-router-dom'
import { ApcBrandMark } from '../../components/icons/ApcIcons'
import { DocsSearch } from '../../components/docs/DocsSearch'
import { docsHref, docsPageBySlug, docsPages } from '../../docs/content'
import { customerTags, safeDecode, tagAnchor, tagSlug } from '../../components/docs/select'
import { useI18n } from '../../lib/i18n'

const DOCS_THEME_KEY = 'ajs.docs.theme'

type DocsTheme = 'light' | 'dark'

function readDocsTheme(): DocsTheme {
  try {
    return localStorage.getItem(DOCS_THEME_KEY) === 'dark' ? 'dark' : 'light'
  } catch {
    return 'light'
  }
}

interface TocItem {
  id: string
  label: string
  href: string
  active: boolean
}

function DocsNav({ onNavigate }: { onNavigate: () => void }) {
  const { lang, t } = useI18n()

  return (
    <nav className="docs-rail__nav" aria-label={t('docs.nav')}>
      <p className="docs-rail__label">{t('docs.guide')}</p>
      {docsPages.map((page) => (
        <NavLink
          key={page.slug}
          to={docsHref(page.slug)}
          end={page.slug === 'overview'}
          className={({ isActive }) => clsx('docs-rail__link', isActive && 'is-active')}
          onClick={onNavigate}
        >
          {page.title[lang]}
        </NavLink>
      ))}

      <p className="docs-rail__label">{t('docs.reference')}</p>
      <NavLink
        to="/docs/reference"
        end
        className={({ isActive }) => clsx('docs-rail__link', isActive && 'is-active')}
        onClick={onNavigate}
      >
        {t('docs.referenceAll')}
      </NavLink>
      {customerTags.map((tag) => (
        <NavLink
          key={tag.name}
          to={`/docs/reference/${tagSlug(tag.name)}`}
          className={({ isActive }) =>
            clsx('docs-rail__link', 'docs-rail__link--tag', isActive && 'is-active')
          }
          onClick={onNavigate}
        >
          {tag.name}
        </NavLink>
      ))}
    </nav>
  )
}

export default function DocsLayout() {
  const { lang, setLang, t } = useI18n()
  const location = useLocation()
  const [theme, setTheme] = useState<DocsTheme>(readDocsTheme)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [searchOpen, setSearchOpen] = useState(false)

  const pathname = location.pathname
  const hash = location.hash
  const rawConsoleUrl = import.meta.env.VITE_CONSOLE_URL
  const consoleUrl = typeof rawConsoleUrl === 'string' ? rawConsoleUrl.trim() : ''

  useEffect(() => {
    try {
      localStorage.setItem(DOCS_THEME_KEY, theme)
    } catch {
      // The in-memory choice still applies for this session.
    }
  }, [theme])

  useEffect(() => {
    setDrawerOpen(false)
  }, [pathname])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setSearchOpen((open) => !open)
        return
      }
      if (event.key === 'Escape') setDrawerOpen(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  const toc = useMemo<TocItem[]>(() => {
    const isReference = pathname === '/docs/reference' || pathname.startsWith('/docs/reference/')
    if (isReference) {
      const raw = pathname.slice('/docs/reference'.length).replace(/^\//, '')
      const activeTag = raw ? tagSlug(safeDecode(raw)) : ''
      return customerTags.map((tag) => {
        const anchor = tagAnchor(tag.name)
        return {
          id: anchor,
          label: tag.name,
          href: activeTag ? `/docs/reference/${tagSlug(tag.name)}` : `#${anchor}`,
          active: activeTag ? tagSlug(tag.name) === activeTag : hash === `#${anchor}`,
        }
      })
    }

    const slug = pathname === '/docs' ? 'overview' : pathname.replace(/^\/docs\/?/, '')
    const page = docsPageBySlug[slug]
    if (!page) return []
    return page.sections.map((section) => ({
      id: section.id,
      label: section.title[lang],
      href: `#${section.id}`,
      active: hash === `#${section.id}`,
    }))
  }, [pathname, hash, lang])

  const toggleTheme = () => setTheme((current) => (current === 'light' ? 'dark' : 'light'))

  return (
    <div className="docs-root" data-docs-theme={theme} style={{ colorScheme: theme }}>
      <a className="docs-skip" href="#docs-main">
        {t('shell.skipToContent')}
      </a>

      <header className="docs-header">
        <button
          type="button"
          className="docs-icon-btn docs-header__menu"
          aria-label={t('docs.openNav')}
          aria-expanded={drawerOpen}
          aria-controls="docs-rail"
          onClick={() => setDrawerOpen(true)}
        >
          <Menu size={18} aria-hidden="true" />
        </button>

        <Link to="/docs" className="docs-wordmark">
          <span className="docs-wordmark__mark" aria-hidden="true">
            <ApcBrandMark size={18} />
          </span>
          <span className="docs-wordmark__name">AIJIASU2API</span>
          <span className="docs-wordmark__tag">{t('docs.wordmarkTag')}</span>
        </Link>

        <div className="docs-header__actions">
          <button
            type="button"
            className="docs-search-trigger"
            aria-label={t('docs.search')}
            onClick={() => setSearchOpen(true)}
          >
            <Search size={14} aria-hidden="true" />
            <span className="docs-search-trigger__text">{t('docs.searchPlaceholder')}</span>
            <kbd className="kbd">Ctrl K</kbd>
          </button>

          <button
            type="button"
            className="docs-icon-btn"
            aria-label={theme === 'light' ? t('shell.toDarkTheme') : t('shell.toLightTheme')}
            title={theme === 'light' ? t('shell.toDarkTheme') : t('shell.toLightTheme')}
            onClick={toggleTheme}
          >
            {theme === 'light' ? (
              <Moon size={16} aria-hidden="true" />
            ) : (
              <Sun size={16} aria-hidden="true" />
            )}
          </button>

          <div className="docs-lang" role="group" aria-label={t('shell.language')}>
            <button
              type="button"
              className={clsx('docs-lang__item', lang === 'en' && 'is-active')}
              aria-pressed={lang === 'en'}
              title={t('shell.languageEn')}
              onClick={() => setLang('en')}
            >
              EN
            </button>
            <button
              type="button"
              className={clsx('docs-lang__item', lang === 'zh' && 'is-active')}
              aria-pressed={lang === 'zh'}
              title={t('shell.languageZh')}
              onClick={() => setLang('zh')}
            >
              中
            </button>
          </div>

          {consoleUrl && (
            <a className="docs-console" href={consoleUrl}>
              {t('docs.console')}
            </a>
          )}
        </div>
      </header>

      <div className="docs-shell">
        <aside
          id="docs-rail"
          className={clsx('docs-rail', drawerOpen && 'is-open')}
        >
          <div className="docs-rail__head">
            <span className="docs-rail__title">{t('docs.wordmarkTag')}</span>
            <button
              type="button"
              className="docs-icon-btn docs-rail__close"
              aria-label={t('docs.closeNav')}
              onClick={() => setDrawerOpen(false)}
            >
              <X size={16} aria-hidden="true" />
            </button>
          </div>
          <DocsNav onNavigate={() => setDrawerOpen(false)} />
        </aside>

        <button
          type="button"
          className={clsx('docs-scrim', drawerOpen && 'is-visible')}
          aria-label={t('docs.closeNav')}
          tabIndex={drawerOpen ? 0 : -1}
          onClick={() => setDrawerOpen(false)}
        />

        <main className="docs-main" id="docs-main" tabIndex={-1}>
          <Suspense
            fallback={
              <div className="docs-loading" aria-busy="true">
                <span className="docs-loading__rule" />
              </div>
            }
          >
            <Outlet />
          </Suspense>
        </main>

        {toc.length > 0 && (
          <aside className="docs-toc" aria-label={t('docs.onThisPage')}>
            <p className="docs-toc__label">{t('docs.onThisPage')}</p>
            <nav>
              {toc.map((item) =>
                item.href.startsWith('#') ? (
                  <a
                    key={item.id}
                    href={item.href}
                    className={clsx('docs-toc__link', item.active && 'is-active')}
                  >
                    {item.label}
                  </a>
                ) : (
                  <Link
                    key={item.id}
                    to={item.href}
                    className={clsx('docs-toc__link', item.active && 'is-active')}
                  >
                    {item.label}
                  </Link>
                ),
              )}
            </nav>
          </aside>
        )}
      </div>

      <DocsSearch open={searchOpen} onClose={() => setSearchOpen(false)} />
    </div>
  )
}
