import clsx from 'clsx'
import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { useI18n } from '../lib/i18n'
import { navItemFor } from '../lib/nav'
import { CommandPalette } from './CommandPalette'
import { Sidebar } from './Sidebar'
import { TokenGate } from './TokenGate'
import { Topbar } from './Topbar'

const SIDEBAR_STORAGE_KEY = 'ajs.sidebar'

function readSidebarCollapsed(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_STORAGE_KEY) === 'collapsed'
  } catch {
    // Storage can be unavailable (private mode); start expanded for this session.
    return false
  }
}

function writeSidebarCollapsed(collapsed: boolean) {
  try {
    localStorage.setItem(SIDEBAR_STORAGE_KEY, collapsed ? 'collapsed' : 'expanded')
  } catch {
    // The in-memory choice still applies for this session.
  }
}

export function AppShell({ children }: { children: ReactNode }) {
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(readSidebarCollapsed)
  const location = useLocation()
  const { t } = useI18n()
  const titleKey = navItemFor(location.pathname)?.labelKey ?? 'nav.overview'
  const title = t(titleKey)

  useEffect(() => {
    setSidebarOpen(false)
  }, [location.pathname])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPaletteOpen((value) => !value)
        return
      }
      if (event.key === 'Escape') setSidebarOpen(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  const toggleSidebar = () => {
    setSidebarCollapsed((value) => {
      const next = !value
      writeSidebarCollapsed(next)
      return next
    })
  }

  return (
    <div className={clsx('app-shell', sidebarCollapsed && 'is-sidebar-collapsed')}>
      <a className="skip-link" href="#main">
        {t('shell.skipToContent')}
      </a>
      <Sidebar open={sidebarOpen} collapsed={sidebarCollapsed} onClose={() => setSidebarOpen(false)} />
      <button
        type="button"
        className={clsx('sidebar-scrim', sidebarOpen && 'is-visible')}
        aria-label={t('shell.closeNavigation')}
        tabIndex={sidebarOpen ? 0 : -1}
        onClick={() => setSidebarOpen(false)}
      />
      <div className="app-main">
        <Topbar
          title={title}
          collapsed={sidebarCollapsed}
          onToggleSidebar={toggleSidebar}
          onOpenPalette={() => setPaletteOpen(true)}
          onOpenSidebar={() => setSidebarOpen(true)}
        />
        <main className="page" id="main">
          {children}
        </main>
      </div>
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />
      <TokenGate />
    </div>
  )
}
