import { Menu, Plus, RefreshCw, Search } from 'lucide-react'
import clsx from 'clsx'
import { useNavigate } from 'react-router-dom'
import { isMockMode } from '../lib/api'
import { useRefreshActivity } from '../lib/useAsync'
import { Button, IconButton } from './primitives'
import { useToast } from './Toast'

export function Topbar({
  title,
  onOpenPalette,
  onOpenSidebar,
}: {
  title: string
  onOpenPalette: () => void
  onOpenSidebar: () => void
}) {
  const navigate = useNavigate()
  const { push } = useToast()
  const refreshing = useRefreshActivity()

  const refresh = () => {
    window.dispatchEvent(new Event('ajs:refresh'))
    push('Refreshing panel data', 'default')
  }

  return (
    <header className="topbar">
      <span className={clsx('topbar__progress', refreshing && 'is-active')} aria-hidden="true" />
      <div className="topbar__left">
        <IconButton
          label="Open navigation"
          icon={<Menu size={17} />}
          onClick={onOpenSidebar}
          className="topbar__menu"
        />
        <span className="topbar__title">{title}</span>
        <span className="topbar__env">{isMockMode() ? 'mock data' : 'live API'}</span>
      </div>

      <div className="topbar__right">
        <button
          type="button"
          id="topbar-search-trigger"
          className="search-trigger"
          onClick={onOpenPalette}
          aria-label="Search or jump to a page"
        >
          <Search size={14} aria-hidden="true" />
          <span className="search-trigger__text">Search or jump to…</span>
          <kbd className="kbd">Ctrl K</kbd>
        </button>
        <IconButton label="Refresh data" icon={<RefreshCw size={16} />} onClick={refresh} />
        <Button
          variant="primary"
          icon={<Plus size={15} />}
          aria-label="New account"
          onClick={() => navigate('/accounts?new=1')}
        >
          <span className="topbar__new-label">New account</span>
        </Button>
        <div className="avatar" title="Operator · local session">
          OP
          <span className="avatar__dot" aria-hidden="true" />
        </div>
      </div>
    </header>
  )
}
