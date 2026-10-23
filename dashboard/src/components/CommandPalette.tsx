import clsx from 'clsx'
import { Moon, Plus, RefreshCw, Search, Server, Sun, Wallet } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import type { LucideIcon } from 'lucide-react'
import { api } from '../lib/api'
import { NAV_ITEMS } from '../lib/nav'
import { useTheme } from '../lib/theme'
import type { Account, ProxyNode } from '../lib/types'
import { useFocusTrap } from '../lib/useFocusTrap'
import { useToast } from './Toast'

interface PaletteEntry {
  id: string
  label: string
  section: string
  keywords: string
  icon: LucideIcon
  run: () => void
}

function scoreEntry(query: string, entry: PaletteEntry): number {
  const needle = query.trim().toLowerCase()
  if (!needle) return 1
  const label = entry.label.toLowerCase()
  if (label.startsWith(needle)) return 100
  if (label.includes(needle)) return 80
  const haystack = `${label} ${entry.keywords} ${entry.section}`.toLowerCase()
  if (haystack.includes(needle)) return 60
  let cursor = 0
  for (const character of haystack) {
    if (character === needle[cursor]) cursor += 1
  }
  return cursor === needle.length ? 30 : -1
}

interface PaletteData {
  accounts: Account[]
  nodes: ProxyNode[]
}

const NO_DATA: PaletteData = { accounts: [], nodes: [] }

/** At least 2 non-space characters are required before account/node entries join the palette. */
const DATA_QUERY_MIN = 2

export function CommandPalette({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate()
  const { push } = useToast()
  const { theme, toggle } = useTheme()
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState(0)
  const [data, setData] = useState<PaletteData>(NO_DATA)
  const containerRef = useFocusTrap<HTMLDivElement>(open, onClose)
  const restoreTargetRef = useRef<HTMLElement | null>(null)
  const wasOpenRef = useRef(false)
  const hasOpenedRef = useRef(false)

  // Capture the pre-open focus target during render: the palette's autoFocus moves focus to the
  // search input in the same commit, before any effect could read document.activeElement.
  if (open && !wasOpenRef.current) {
    const active = document.activeElement
    restoreTargetRef.current =
      active instanceof HTMLElement && active !== document.body && active !== document.documentElement
        ? active
        : null
  }
  wasOpenRef.current = open

  useEffect(() => {
    if (open) {
      hasOpenedRef.current = true
      return
    }
    if (!hasOpenedRef.current) return
    hasOpenedRef.current = false
    const target = restoreTargetRef.current
    restoreTargetRef.current = null
    if (target?.isConnected) {
      target.focus()
      if (document.activeElement === target) return
    }
    document.getElementById('topbar-search-trigger')?.focus()
  }, [open])

  useEffect(() => {
    if (open) {
      setQuery('')
      setSelected(0)
    }
  }, [open])

  useEffect(() => {
    setSelected(0)
  }, [query])

  // Fetch accounts and nodes once per open, in parallel. Failures are ignored: navigation and
  // action entries keep working. Results that land after close are dropped via the cancelled flag.
  useEffect(() => {
    if (!open) return
    let cancelled = false
    Promise.all([
      api.listAccounts().catch(() => [] as Account[]),
      api.listNodes().catch(() => [] as ProxyNode[]),
    ]).then(([accounts, nodes]) => {
      if (!cancelled) setData({ accounts, nodes })
    })
    return () => {
      cancelled = true
    }
  }, [open])

  const entries = useMemo<PaletteEntry[]>(() => {
    const navigation: PaletteEntry[] = NAV_ITEMS.map((item) => ({
      id: `nav-${item.to}`,
      label: item.label,
      section: 'Navigate',
      keywords: item.keywords,
      icon: item.icon,
      run: () => navigate(item.to),
    }))
    const actions: PaletteEntry[] = [
      {
        id: 'action-new-account',
        label: 'New account',
        section: 'Action',
        keywords: 'create add vendor login',
        icon: Plus,
        run: () => navigate('/accounts?new=1'),
      },
      {
        id: 'action-refresh',
        label: 'Refresh data',
        section: 'Action',
        keywords: 'reload sync fetch',
        icon: RefreshCw,
        run: () => {
          window.dispatchEvent(new Event('ajs:refresh'))
          push('Refreshing panel data')
        },
      },
      {
        id: 'action-theme',
        label: theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme',
        section: 'Action',
        keywords: 'appearance dark light',
        icon: theme === 'dark' ? Sun : Moon,
        run: toggle,
      },
    ]
    if (query.replace(/\s/g, '').length < DATA_QUERY_MIN) return [...navigation, ...actions]
    const accountEntries: PaletteEntry[] = data.accounts.map((account) => ({
      id: `account-${account.id}`,
      label: account.label,
      section: 'Account',
      keywords: `${account.username} ${account.platform} ${account.regions.join(' ')}`,
      icon: Wallet,
      run: () => navigate(`/accounts?q=${encodeURIComponent(account.label)}`),
    }))
    const nodeEntries: PaletteEntry[] = data.nodes.map((node) => ({
      id: `node-${node.id}`,
      label: node.label,
      section: 'Node',
      keywords: `${node.accountLabel} ${node.region} ${node.city} ${node.egressIp}`,
      icon: Server,
      run: () => navigate(`/nodes?q=${encodeURIComponent(node.label)}`),
    }))
    return [...navigation, ...actions, ...accountEntries, ...nodeEntries]
  }, [data, navigate, push, query, theme, toggle])

  const filtered = useMemo(
    () =>
      entries
        .map((entry) => ({ entry, score: scoreEntry(query, entry) }))
        .filter((candidate) => candidate.score >= 0)
        .sort((left, right) => right.score - left.score)
        .map((candidate) => candidate.entry),
    [entries, query],
  )

  useEffect(() => {
    if (!open || filtered.length === 0) return
    document.getElementById(`palette-option-${selected}`)?.scrollIntoView?.({ block: 'nearest' })
  }, [filtered.length, open, selected])

  if (!open) return null

  const runEntry = (entry: PaletteEntry) => {
    onClose()
    entry.run()
  }

  const onInputKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setSelected((current) => (filtered.length ? (current + 1) % filtered.length : 0))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setSelected((current) => (filtered.length ? (current - 1 + filtered.length) % filtered.length : 0))
    } else if (event.key === 'Enter') {
      event.preventDefault()
      const entry = filtered[selected]
      if (entry) runEntry(entry)
    }
  }

  return (
    <div
      className="overlay overlay--palette"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div className="overlay__scrim" aria-hidden="true" />
      <div
        ref={containerRef}
        className="palette"
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        tabIndex={-1}
      >
        <div className="palette__search">
          <Search size={16} aria-hidden="true" />
          <input
            className="palette__input"
            placeholder="Search pages and actions…"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={onInputKeyDown}
            aria-label="Command palette search"
            aria-controls="palette-list"
            aria-activedescendant={filtered.length ? `palette-option-${selected}` : undefined}
            autoFocus
          />
          <kbd className="kbd">Esc</kbd>
        </div>

        <ul className="palette__list" id="palette-list" role="listbox" aria-label="Results">
          {filtered.map((entry, index) => (
            <li
              key={entry.id}
              id={`palette-option-${index}`}
              role="option"
              aria-selected={index === selected}
              className={clsx('palette__item', index === selected && 'is-selected')}
              onMouseMove={() => setSelected(index)}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => runEntry(entry)}
            >
              <span className="palette__icon">
                <entry.icon size={15} aria-hidden="true" />
              </span>
              <span className="palette__label">{entry.label}</span>
              <span className="palette__section">{entry.section}</span>
            </li>
          ))}
          {filtered.length === 0 ? (
            <li className="palette__empty">No pages or actions match “{query}”.</li>
          ) : null}
        </ul>

        <footer className="palette__foot">
          <span>
            <kbd className="kbd">↑</kbd>
            <kbd className="kbd">↓</kbd> navigate
          </span>
          <span>
            <kbd className="kbd">↵</kbd> open
          </span>
          <span>
            <kbd className="kbd">Esc</kbd> close
          </span>
        </footer>
      </div>
    </div>
  )
}
