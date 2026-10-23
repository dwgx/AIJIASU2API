import clsx from 'clsx'
import { ChevronDown, Moon, Sun, Waypoints } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { NavLink } from 'react-router-dom'
import { isMockMode } from '../lib/api'
import { NAV_SECTIONS } from '../lib/nav'
import { useTheme } from '../lib/theme'
import { IconButton, StatusDot } from './primitives'

export function Sidebar({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { theme, toggle } = useTheme()
  const [workspaceOpen, setWorkspaceOpen] = useState(false)
  const workspaceRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!workspaceOpen) return
    const onPointerDown = (event: MouseEvent) => {
      if (!workspaceRef.current?.contains(event.target as Node)) setWorkspaceOpen(false)
    }
    document.addEventListener('mousedown', onPointerDown)
    return () => document.removeEventListener('mousedown', onPointerDown)
  }, [workspaceOpen])

  return (
    <aside className={clsx('sidebar', open && 'is-open')} aria-label="Primary navigation">
      <div className="sidebar__brand">
        <span className="brand__mark" aria-hidden="true">
          <Waypoints size={18} />
        </span>
        <span className="brand__text">
          <span className="brand__name">AJiaSu</span>
          <span className="brand__sub">Proxy Console</span>
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
            <span className="workspace__name">Personal workspace</span>
            <span className="workspace__meta">Self-use · local</span>
          </span>
          <ChevronDown size={14} className="workspace__chevron" aria-hidden="true" />
        </button>
        {workspaceOpen ? (
          <div className="workspace__menu" role="menu">
            <button type="button" role="menuitem" className="workspace__item is-active" onClick={() => setWorkspaceOpen(false)}>
              Personal workspace
              <span className="workspace__tag">current</span>
            </button>
            {isMockMode() ? (
              <button type="button" role="menuitem" className="workspace__item" disabled>
                Lab workspace
                <span className="workspace__tag">mock only</span>
              </button>
            ) : null}
          </div>
        ) : null}
      </div>

      <nav className="sidebar__nav">
        {NAV_SECTIONS.map((section) => (
          <div className="sidebar__section" key={section.label}>
            <p className="sidebar__section-label">{section.label}</p>
            {section.items.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.to === '/'}
                className={({ isActive }) => clsx('nav-item', isActive && 'is-active')}
                onClick={onClose}
                title={item.label}
              >
                <item.icon size={17} aria-hidden="true" />
                <span className="nav-item__label sidebar__label">{item.label}</span>
              </NavLink>
            ))}
          </div>
        ))}
      </nav>

      <footer className="sidebar__foot">
        <div className="daemon">
          <StatusDot tone="success" pulse />
          <div className="daemon__text">
            <span className="daemon__name">ajs-accountd</span>
            <span className="daemon__addr">127.0.0.1:2261</span>
          </div>
        </div>
        <div className="sidebar__meta">
          <span className="sidebar__version">v0.1.0 · {isMockMode() ? 'mock' : 'live'}</span>
          <IconButton
            label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
            icon={theme === 'dark' ? <Sun size={15} /> : <Moon size={15} />}
            onClick={toggle}
            size="sm"
          />
        </div>
      </footer>
    </aside>
  )
}
