import { Menu, Plus, RefreshCw, Search } from 'lucide-react'
import clsx from 'clsx'
import { useNavigate } from 'react-router-dom'
import { isMockMode } from '../lib/api'
import { useI18n } from '../lib/i18n'
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
  const { t } = useI18n()
  const refreshing = useRefreshActivity()

  const refresh = () => {
    window.dispatchEvent(new Event('ajs:refresh'))
    push(t('shell.refreshing'), 'default')
  }

  return (
    <header className="topbar">
      <span className={clsx('topbar__progress', refreshing && 'is-active')} aria-hidden="true" />
      <div className="topbar__left">
        <IconButton
          label={t('shell.openNavigation')}
          icon={<Menu size={17} />}
          onClick={onOpenSidebar}
          className="topbar__menu"
        />
        <span className="topbar__title">{title}</span>
        <span className="topbar__env">{isMockMode() ? t('shell.envMock') : t('shell.envLive')}</span>
      </div>

      <div className="topbar__right">
        <button
          type="button"
          id="topbar-search-trigger"
          className="search-trigger"
          onClick={onOpenPalette}
          aria-label={t('shell.searchAria')}
        >
          <Search size={14} aria-hidden="true" />
          <span className="search-trigger__text">{t('shell.searchOrJump')}</span>
          <kbd className="kbd">Ctrl K</kbd>
        </button>
        <IconButton label={t('shell.refreshData')} icon={<RefreshCw size={16} />} onClick={refresh} />
        <Button
          variant="primary"
          icon={<Plus size={15} />}
          aria-label={t('shell.newAccount')}
          onClick={() => navigate('/accounts?new=1')}
        >
          <span className="topbar__new-label">{t('shell.newAccount')}</span>
        </Button>
        <div className="avatar" title={t('shell.avatarTitle')}>
          OP
          <span className="avatar__dot" aria-hidden="true" />
        </div>
      </div>
    </header>
  )
}
