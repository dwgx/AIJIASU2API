import clsx from 'clsx'
import { useNavigate } from 'react-router-dom'
import { isMockMode } from '../lib/api'
import { useI18n } from '../lib/i18n'
import { useRefreshActivity } from '../lib/useAsync'
import {
  ApcMenuIcon,
  ApcPanelCollapseIcon,
  ApcPanelExpandIcon,
  ApcPlusIcon,
  ApcRefreshIcon,
  ApcSearchIcon,
} from './icons/ApcIcons'
import { Button, IconButton } from './primitives'
import { useToast } from './Toast'

export function Topbar({
  title,
  collapsed,
  onToggleSidebar,
  onOpenPalette,
  onOpenSidebar,
}: {
  title: string
  collapsed: boolean
  onToggleSidebar: () => void
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
          icon={<ApcMenuIcon size={17} />}
          onClick={onOpenSidebar}
          className="topbar__menu"
        />
        <IconButton
          label={t(collapsed ? 'shell.expandSidebar' : 'shell.collapseSidebar')}
          icon={collapsed ? <ApcPanelExpandIcon size={17} /> : <ApcPanelCollapseIcon size={17} />}
          onClick={onToggleSidebar}
          className="topbar__collapse"
          aria-expanded={!collapsed}
          aria-controls="app-sidebar"
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
          <ApcSearchIcon size={14} />
          <span className="search-trigger__text">{t('shell.searchOrJump')}</span>
          <kbd className="kbd">Ctrl K</kbd>
        </button>
        <IconButton label={t('shell.refreshData')} icon={<ApcRefreshIcon size={16} />} onClick={refresh} />
        <Button
          variant="primary"
          icon={<ApcPlusIcon size={15} />}
          aria-label={t('shell.newAccount')}
          onClick={() => navigate('/accounts?new=1')}
        >
          <span className="topbar__new-label">{t('shell.newAccount')}</span>
        </Button>
      </div>
    </header>
  )
}
