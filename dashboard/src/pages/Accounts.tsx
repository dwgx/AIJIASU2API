import clsx from 'clsx'
import {
  CalendarClock,
  Download,
  ExternalLink,
  LayoutGrid,
  LogOut,
  Pause,
  Play,
  Rows3,
  Search,
  Trash2,
  Upload,
} from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api, isMockMode } from '../lib/api'
import { formatBytes, formatDate } from '../lib/format'
import { useI18n } from '../lib/i18n'
import { ACCOUNT_STATE } from '../lib/labels'
import type {
  Account,
  AccountState,
  BulkDeleteItem,
  BulkDeleteResult,
  CreateAccountInput,
  Platform,
} from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { ConfirmDialog, Drawer, Modal } from '../components/Overlay'
import { useToast } from '../components/Toast'
import { AccountExportDialog } from '../components/accounts/AccountExportDialog'
import { AccountImportDialog } from '../components/accounts/AccountImportDialog'
import {
  OFFICIAL_PURCHASE_URL,
  RenewalCard,
  renewalCountdown,
  renewalStatusText,
} from '../components/accounts/RenewalCard'
import '../components/accounts/accounts-io.css'
import {
  Button,
  Card,
  Checkbox,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatusPill,
  TableSkeleton,
} from '../components/primitives'

/**
 * Seeds the search box from the `q` param and follows later `q` changes (the command palette
 * can navigate here while the page is mounted) without clobbering a query the user typed on
 * re-renders that leave `q` alone — e.g. opening `?new=1` or closing the modal.
 */
function useParamQuery(paramValue: string | null): [string, (value: string) => void] {
  const [query, setQuery] = useState(paramValue ?? '')
  const lastParamRef = useRef(paramValue)
  useEffect(() => {
    if (paramValue !== lastParamRef.current) {
      lastParamRef.current = paramValue
      setQuery(paramValue ?? '')
    }
  }, [paramValue])
  return [query, setQuery]
}

const PLATFORMS: Platform[] = ['android', 'ios', 'windows', 'linux']
/** Mirrors GET /v1/accounts/io/capabilities maxDeleteIDs. */
const MAX_BULK_DELETE = 100
const BULK_DELETE_STATUS_KEYS: Record<BulkDeleteItem['status'], string> = {
  deleted: 'accounts.bulkDeleteStatusDeleted',
  would_delete: 'accounts.bulkDeleteStatusWouldDelete',
  not_found: 'accounts.bulkDeleteStatusNotFound',
  failed: 'accounts.bulkDeleteStatusFailed',
  skipped: 'accounts.bulkDeleteStatusSkipped',
}
const EMPTY_FORM = {
  label: '',
  username: '',
  password: '',
  platform: 'android' as Platform,
  regions: '',
}

type AccountView = 'table' | 'cards'
const ACCOUNT_VIEW_KEY = 'ajs.accounts.view'

function readAccountView(): AccountView {
  try {
    if (typeof localStorage === 'undefined') return 'table'
    return localStorage.getItem(ACCOUNT_VIEW_KEY) === 'cards' ? 'cards' : 'table'
  } catch {
    return 'table'
  }
}

function writeAccountView(view: AccountView) {
  try {
    localStorage.setItem(ACCOUNT_VIEW_KEY, view)
  } catch {
    // Storage can be unavailable (private mode or quota); the switch still works for this session.
  }
}

function AccountViewSwitch({
  value,
  onChange,
}: {
  value: AccountView
  onChange: (view: AccountView) => void
}) {
  const { t } = useI18n()
  return (
    <div className="segmented" role="group" aria-label={t('accounts.viewAria')}>
      <button
        type="button"
        className={clsx('segmented__item', value === 'table' && 'is-active')}
        aria-pressed={value === 'table'}
        onClick={() => onChange('table')}
      >
        <Rows3 size={13} aria-hidden="true" />
        <span>{t('accounts.table')}</span>
      </button>
      <button
        type="button"
        className={clsx('segmented__item', value === 'cards' && 'is-active')}
        aria-pressed={value === 'cards'}
        onClick={() => onChange('cards')}
      >
        <LayoutGrid size={13} aria-hidden="true" />
        <span>{t('accounts.cards')}</span>
      </button>
    </div>
  )
}

interface AccountCardProps {
  account: Account
  selected: boolean
  onToggleSelected: (id: string) => void
  onTest: (account: Account) => void
  onTogglePause: (account: Account) => void
  onLogout: (account: Account) => void
  onDelete: (id: string) => void
  onRenewal: (account: Account) => void
}

function AccountCard({
  account,
  selected,
  onToggleSelected,
  onTest,
  onTogglePause,
  onLogout,
  onDelete,
  onRenewal,
}: AccountCardProps) {
  const { t } = useI18n()
  const state = ACCOUNT_STATE[account.state]
  const capacity = account.nodeCount > 0 ? Math.min(1, Math.max(0, account.liveNodeCount / account.nodeCount)) : 0
  const capacityPct = Math.round(capacity * 100)
  const countdown = renewalCountdown(account.expiresAt)
  const useCount = account.useCount ?? 1

  return (
    <article role="listitem" className={clsx('account-card', selected && 'is-selected')}>
      <div className="account-card__head">
        <Checkbox
          aria-label={t('accounts.selectAccount', { name: account.label })}
          checked={selected}
          onChange={() => onToggleSelected(account.id)}
        />
        <div className="account-card__identity">
          <span className="account-card__label" title={account.label}>
            {account.label}
          </span>
          <span className="account-card__username" title={account.username}>
            {account.username}
          </span>
        </div>
        <span className="account-card__state">
          <StatusPill status={state.status} pulse={account.state === 'active'}>
            {t(state.labelKey)}
          </StatusPill>
        </span>
      </div>

      <div className="account-card__meta">
        <span className="account-card__platform">{account.platform}</span>
        <span className="account-card__regions">
          {account.regions.length > 0 ? account.regions.join(' · ') : t('common.anyRegion')}
        </span>
      </div>

      <dl className="account-card__metrics">
        <div className="account-card__metric">
          <dt>{t('accounts.columnNodes')}</dt>
          <dd>
            <span>
              {account.liveNodeCount}/{account.nodeCount}
            </span>
            <span
              className={clsx('account-card__meter', account.liveNodeCount === 0 && 'is-empty')}
              role="progressbar"
              aria-label={t('accounts.nodeCapacity', { name: account.label })}
              aria-valuenow={capacityPct}
              aria-valuemin={0}
              aria-valuemax={100}
            >
              <span className="account-card__meter-fill" style={{ width: `${capacityPct}%` }} />
            </span>
          </dd>
        </div>
        <div className="account-card__metric">
          <dt>{t('accounts.columnUseCount')}</dt>
          <dd>
            <span>{useCount}</span>
            {useCount > 1 ? (
              <span className="account-card__note" title={t('accounts.useCountNote', { count: useCount })}>
                {t('accounts.useCountPending')}
              </span>
            ) : null}
          </dd>
        </div>
        <div className="account-card__metric">
          <dt>{t('accounts.columnUsage')}</dt>
          <dd>{formatBytes(account.usageBytes)}</dd>
        </div>
      </dl>

      <div className="account-card__foot">
        <span className="account-card__expiry">
          <span className="account-card__expiry-label">{t('accounts.expires')}</span>
          <span className="account-card__expiry-date">{formatDate(account.expiresAt)}</span>
          <span
            className={clsx(
              'account-card__expiry-rel',
              countdown.warning && 'renewal-status--warning',
              countdown.overdue && 'renewal-status--overdue',
            )}
          >
            {renewalStatusText(t, countdown)}
          </span>
          {countdown.warning ? (
            <a
              className="account-card__renew"
              href={OFFICIAL_PURCHASE_URL}
              target="_blank"
              rel="noreferrer noopener"
              aria-label={t('accounts.renewal.openOfficialFor', { name: account.label })}
            >
              {t('accounts.renewal.renew')}
              <ExternalLink size={11} aria-hidden="true" />
            </a>
          ) : null}
        </span>
        <span className="account-card__actions">
          <IconButton
            label={t('accounts.testLogin', { name: account.label })}
            icon={<Play size={14} />}
            onClick={() => onTest(account)}
          />
          <IconButton
            label={t(account.state === 'paused' ? 'accounts.resumeAccount' : 'accounts.pauseAccount', {
              name: account.label,
            })}
            icon={account.state === 'paused' ? <Play size={14} /> : <Pause size={14} />}
            onClick={() => onTogglePause(account)}
          />
          <IconButton
            label={t('accounts.renewal.open', { name: account.label })}
            icon={<CalendarClock size={14} />}
            onClick={() => onRenewal(account)}
          />
          <IconButton
            label={t('accounts.logoutAccount', { name: account.label })}
            icon={<LogOut size={14} />}
            onClick={() => onLogout(account)}
          />
          <IconButton
            label={t('accounts.deleteAccount', { name: account.label })}
            icon={<Trash2 size={14} />}
            variant="ghost"
            onClick={() => onDelete(account.id)}
          />
        </span>
      </div>
    </article>
  )
}

function AccountCards({
  accounts,
  selected,
  empty,
  onToggleSelected,
  onTest,
  onTogglePause,
  onLogout,
  onDelete,
  onRenewal,
}: {
  accounts: Account[]
  selected: string[]
  empty: ReactNode
  onToggleSelected: (id: string) => void
  onTest: (account: Account) => void
  onTogglePause: (account: Account) => void
  onLogout: (account: Account) => void
  onDelete: (id: string) => void
  onRenewal: (account: Account) => void
}) {
  const { t } = useI18n()
  if (accounts.length === 0) {
    return <div className="account-grid account-grid--empty">{empty}</div>
  }

  return (
    <div className="account-grid" role="list" aria-label={t('accounts.listAria')}>
      {accounts.map((account) => (
        <AccountCard
          key={account.id}
          account={account}
          selected={selected.includes(account.id)}
          onToggleSelected={onToggleSelected}
          onTest={onTest}
          onTogglePause={onTogglePause}
          onLogout={onLogout}
          onDelete={onDelete}
          onRenewal={onRenewal}
        />
      ))}
    </div>
  )
}

function AccountCardsSkeleton() {
  const { t } = useI18n()
  return (
    <div className="account-grid" aria-busy="true" aria-label={t('accounts.loadingAria')}>
      {Array.from({ length: 6 }, (_, index) => (
        <div className="account-card account-card--skeleton" key={index}>
          <div className="account-card__head">
            <Skeleton width={16} height={16} />
            <Skeleton width="55%" height={12} />
          </div>
          <Skeleton width="42%" height={10} />
          <Skeleton height={32} />
          <Skeleton width="68%" height={10} />
        </div>
      ))}
    </div>
  )
}

export default function Accounts() {
  const accounts = useAsync(() => api.listAccounts(), [], { intervalMs: 15_000 })
  const [params, setParams] = useSearchParams()
  const { push } = useToast()
  const { t } = useI18n()
  const [query, setQuery] = useParamQuery(params.get('q'))
  const [stateFilter, setStateFilter] = useState<'all' | AccountState>('all')
  const [platformFilter, setPlatformFilter] = useState<'all' | Platform>('all')
  const [view, setView] = useState<AccountView>(readAccountView)
  const [selected, setSelected] = useState<string[]>([])
  const [deleteIds, setDeleteIds] = useState<string[]>([])
  const [deleting, setDeleting] = useState(false)
  const [deleteResult, setDeleteResult] = useState<BulkDeleteResult | null>(null)
  const [exportOpen, setExportOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [logoutId, setLogoutId] = useState<string | null>(null)
  const [loggingOut, setLoggingOut] = useState(false)
  const [renewalId, setRenewalId] = useState<string | null>(null)
  const [form, setForm] = useState(EMPTY_FORM)
  const [formErrors, setFormErrors] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState('')
  const [saving, setSaving] = useState(false)

  const createOpen = params.get('new') === '1'

  const changeView = (next: AccountView) => {
    setView(next)
    writeAccountView(next)
  }

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return (accounts.data ?? []).filter((account) => {
      if (stateFilter !== 'all' && account.state !== stateFilter) return false
      if (platformFilter !== 'all' && account.platform !== platformFilter) return false
      if (!needle) return true
      return `${account.label} ${account.username} ${account.regions.join(' ')}`.toLowerCase().includes(needle)
    })
  }, [accounts.data, platformFilter, query, stateFilter])

  const allVisibleSelected = filtered.length > 0 && filtered.every((account) => selected.includes(account.id))
  const someVisibleSelected = filtered.some((account) => selected.includes(account.id))
  const renewalAccount = accounts.data?.find((account) => account.id === renewalId) ?? null

  const closeCreate = () => {
    setParams({}, { replace: true })
    setForm(EMPTY_FORM)
    setFormErrors({})
    setFormError('')
  }

  const toggleSelected = (id: string) => {
    setSelected((current) =>
      current.includes(id) ? current.filter((value) => value !== id) : [...current, id],
    )
  }

  const toggleAllVisible = () => {
    if (allVisibleSelected) {
      const visible = new Set(filtered.map((account) => account.id))
      setSelected((current) => current.filter((id) => !visible.has(id)))
    } else {
      setSelected((current) => Array.from(new Set([...current, ...filtered.map((account) => account.id)])))
    }
  }

  const runTest = async (account: Account) => {
    try {
      await api.testAccount(account.id)
      accounts.reload()
      push(t('accounts.toastTestPassed', { name: account.label }), 'success')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('accounts.toastTestFailed'), 'danger')
    }
  }

  const togglePause = async (account: Account) => {
    const next: AccountState = account.state === 'paused' ? 'active' : 'paused'
    try {
      await api.setAccountState(account.id, next)
      accounts.reload()
      push(
        t(next === 'paused' ? 'accounts.toastPaused' : 'accounts.toastResumed', { name: account.label }),
        'success',
      )
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('accounts.errorUpdate'), 'danger')
    }
  }

  const bulkPause = async () => {
    try {
      await Promise.all(selected.map((id) => api.setAccountState(id, 'paused')))
      accounts.reload()
      push(t('accounts.toastBulkPaused', { count: selected.length }), 'success')
      setSelected([])
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('accounts.errorBulkPause'), 'danger')
    }
  }

  const confirmDelete = async () => {
    setDeleting(true)
    setDeleteResult(null)
    try {
      const result = await api.bulkDeleteAccounts({ ids: deleteIds })
      const removed = result.results
        .filter((item) => item.status === 'deleted')
        .map((item) => item.id)
      setSelected((current) => current.filter((id) => !removed.includes(id)))
      setDeleteResult(result)
      setDeleteIds([])
      accounts.reload()
      if (result.counts.failed + result.counts.notFound + result.counts.skipped > 0) {
        push(t('accounts.bulkDeletePartial'), 'danger')
      } else {
        push(t('accounts.toastDeleted', { count: result.counts.deleted }), 'success')
      }
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('accounts.errorDelete'), 'danger')
    } finally {
      setDeleting(false)
    }
  }

  const confirmLogout = async () => {
    if (!logoutId) return
    const account = accounts.data?.find((item) => item.id === logoutId)
    setLoggingOut(true)
    try {
      await api.logoutAccount(logoutId)
      accounts.reload()
      push(t('accounts.toastLoggedOut', { name: account?.label ?? logoutId }), 'success')
      setLogoutId(null)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('accounts.errorLogout'), 'danger')
    } finally {
      setLoggingOut(false)
    }
  }

  const submitForm = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const errors: Record<string, string> = {}
    if (!form.label.trim()) errors.label = t('accounts.errorLabel')
    if (!form.username.trim()) errors.username = t('accounts.errorUsername')
    if (!form.password.trim()) errors.password = t('accounts.errorPassword')
    if ((accounts.data ?? []).some((account) => account.label.toLowerCase() === form.label.trim().toLowerCase())) {
      errors.label = t('accounts.errorDuplicate')
    }
    setFormErrors(errors)
    if (Object.keys(errors).length > 0) return

    const input: CreateAccountInput = {
      label: form.label.trim(),
      username: form.username.trim(),
      password: form.password,
      platform: form.platform,
      concurrency: 1,
      regions: form.regions
        .split(',')
        .map((region) => region.trim())
        .filter(Boolean),
    }
    setSaving(true)
    setFormError('')
    try {
      await api.createAccount(input)
      accounts.reload()
      push(t('accounts.toastCreated', { name: input.label }), 'success')
      closeCreate()
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : t('accounts.errorCreate'))
    } finally {
      setSaving(false)
    }
  }

  const columns: Array<Column<Account>> = [
    {
      key: 'select',
      label: (
        <Checkbox
          aria-label={t('accounts.selectAll')}
          checked={allVisibleSelected}
          indeterminate={!allVisibleSelected && someVisibleSelected}
          onChange={toggleAllVisible}
        />
      ),
      width: '36px',
      render: (account) => (
        <Checkbox
          aria-label={t('accounts.selectAccount', { name: account.label })}
          checked={selected.includes(account.id)}
          onChange={() => toggleSelected(account.id)}
        />
      ),
    },
    {
      key: 'account',
      label: t('accounts.columnAccount'),
      render: (account) => (
        <div className="cell-stack">
          <span className="cell-strong">{account.label}</span>
          <span className="cell-sub mono">{account.username}</span>
        </div>
      ),
    },
    {
      key: 'platform',
      label: t('accounts.columnPlatform'),
      render: (account) => <span className="cell-mono">{account.platform}</span>,
    },
    {
      key: 'state',
      label: t('accounts.columnState'),
      render: (account) => {
        const state = ACCOUNT_STATE[account.state]
        return (
          <span className="state-cell">
            <StatusPill status={state.status} pulse={account.state === 'active'}>
              {t(state.labelKey)}
            </StatusPill>
          </span>
        )
      },
    },
    {
      key: 'nodes',
      label: t('accounts.columnNodes'),
      align: 'right',
      render: (account) => (
        <span className="cell-mono">
          {account.liveNodeCount}/{account.nodeCount}
        </span>
      ),
    },
    {
      key: 'concurrency',
      label: t('accounts.columnConcurrency'),
      align: 'right',
      render: (account) => <span className="cell-mono">{account.concurrency}</span>,
    },
    {
      key: 'useCount',
      label: t('accounts.columnUseCount'),
      align: 'right',
      render: (account) => <span className="cell-mono">{account.useCount ?? 1}</span>,
    },
    {
      key: 'usage',
      label: t('accounts.columnUsage'),
      align: 'right',
      render: (account) => <span className="cell-mono">{formatBytes(account.usageBytes)}</span>,
    },
    {
      key: 'expiry',
      label: t('accounts.columnExpires'),
      render: (account) => {
        const countdown = renewalCountdown(account.expiresAt)
        const status = renewalStatusText(t, countdown)
        return (
          <span className="cell-stack">
            <span>{formatDate(account.expiresAt)}</span>
            {countdown.warning ? (
              <a
                className={clsx(
                  'cell-sub',
                  'renewal-status__link',
                  countdown.overdue ? 'renewal-status--overdue' : 'renewal-status--warning',
                )}
                href={OFFICIAL_PURCHASE_URL}
                target="_blank"
                rel="noreferrer noopener"
              >
                <span>{status}</span>
                <span className="sr-only"> — {t('accounts.renewal.openOfficial')}</span>
              </a>
            ) : (
              <span className="cell-sub">{status}</span>
            )}
          </span>
        )
      },
    },
    {
      key: 'actions',
      label: '',
      align: 'right',
      width: '196px',
      render: (account) => (
        <span className="row-actions">
          <IconButton
            label={t('accounts.testLogin', { name: account.label })}
            icon={<Play size={14} />}
            onClick={() => void runTest(account)}
          />
          <IconButton
            label={t(account.state === 'paused' ? 'accounts.resumeAccount' : 'accounts.pauseAccount', {
              name: account.label,
            })}
            icon={account.state === 'paused' ? <Play size={14} /> : <Pause size={14} />}
            onClick={() => void togglePause(account)}
          />
          <IconButton
            label={t('accounts.renewal.open', { name: account.label })}
            icon={<CalendarClock size={14} />}
            onClick={() => setRenewalId(account.id)}
          />
          <IconButton
            label={t('accounts.logoutAccount', { name: account.label })}
            icon={<LogOut size={14} />}
            onClick={() => setLogoutId(account.id)}
          />
          <IconButton
            label={t('accounts.deleteAccount', { name: account.label })}
            icon={<Trash2 size={14} />}
            variant="ghost"
            onClick={() => setDeleteIds([account.id])}
          />
        </span>
      ),
    },
  ]

  const emptyState = (
    <EmptyState
      title={t('accounts.emptyTitle')}
      description={t('accounts.emptyDesc')}
      action={
        <Button
          variant="secondary"
          onClick={() => {
            setQuery('')
            setStateFilter('all')
            setPlatformFilter('all')
          }}
        >
          {t('common.clearFilters')}
        </Button>
      }
    />
  )

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('accounts.title')}
        subtitle={t('accounts.subtitle')}
        refresh={summarizeRefresh([accounts])}
      />

      {deleteResult &&
      deleteResult.counts.failed + deleteResult.counts.notFound + deleteResult.counts.skipped >
        0 ? (
        <div className="callout callout--warning" role="status">
          <div>
            <p className="callout__title">{t('accounts.bulkDeleteResult')}</p>
            <p className="callout__text">
              {t('accounts.bulkDeleteCounts', {
                deleted: deleteResult.counts.deleted,
                notFound: deleteResult.counts.notFound,
                failed: deleteResult.counts.failed,
                skipped: deleteResult.counts.skipped,
              })}
            </p>
            <ul className="account-io-result-list">
              {deleteResult.results
                .filter((item) => item.status !== 'deleted')
                .map((item) => (
                  <li key={item.id}>
                    <span className="mono">{item.id}</span>
                    {item.label ? ` ${item.label}` : ''} —{' '}
                    {t(BULK_DELETE_STATUS_KEYS[item.status])}
                    {item.message ? `: ${item.message}` : ''}
                  </li>
                ))}
            </ul>
            <Button size="sm" variant="ghost" onClick={() => setDeleteResult(null)}>
              {t('accounts.bulkDeleteDismiss')}
            </Button>
          </div>
        </div>
      ) : null}

      <div className="callout callout--warning" role="note">
        <span className="callout__icon">
          <Pause size={18} aria-hidden="true" />
        </span>
        <div>
          <p className="callout__title">{t('accounts.pauseCalloutTitle')}</p>
          <p className="callout__text">{t('accounts.pauseCalloutText')}</p>
        </div>
      </div>

      <Card bodyClassName="card__body--flush">
        <div className="toolbar">
          <div className="toolbar__search">
            <Search size={14} aria-hidden="true" />
            <Input
              aria-label={t('accounts.searchAria')}
              placeholder={t('accounts.searchPlaceholder')}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <Select
            variant="filter"
            aria-label={t('accounts.filterState')}
            value={stateFilter}
            onChange={(value) => setStateFilter(value as 'all' | AccountState)}
            options={[
              { value: 'all', label: t('accounts.allStates') },
              { value: 'active', label: t('state.active') },
              { value: 'paused', label: t('state.paused') },
              { value: 'auth_failed', label: t('state.auth_failed') },
              { value: 'expired', label: t('state.expired') },
            ]}
          />
          <Select
            variant="filter"
            aria-label={t('accounts.filterPlatform')}
            value={platformFilter}
            onChange={(value) => setPlatformFilter(value as 'all' | Platform)}
            options={[
              { value: 'all', label: t('accounts.allPlatforms') },
              ...PLATFORMS.map((platform) => ({ value: platform, label: platform })),
            ]}
          />
          <AccountViewSwitch value={view} onChange={changeView} />
          <span className="toolbar__count">
            {t('common.of', { shown: filtered.length, total: accounts.data?.length ?? 0 })}
          </span>
          <div className="account-io-toolbar">
            <Button
              size="sm"
              variant="secondary"
              icon={<Upload size={13} />}
              onClick={() => setImportOpen(true)}
            >
              {t('accounts.import')}
            </Button>
            <Button
              size="sm"
              variant="secondary"
              icon={<Download size={13} />}
              onClick={() => setExportOpen(true)}
            >
              {t('accounts.export')}
            </Button>
          </div>
        </div>

        {selected.length > 0 ? (
          <div className="bulkbar">
            <span className="bulkbar__count">{t('accounts.selected', { count: selected.length })}</span>
            {selected.length > MAX_BULK_DELETE ? (
              <span className="bulkbar__hint">
                {t('accounts.bulkDeleteTooMany', { max: MAX_BULK_DELETE })}
              </span>
            ) : null}
            <div className="bulkbar__actions">
              <Button size="sm" variant="secondary" icon={<Pause size={13} />} onClick={() => void bulkPause()}>
                {t('accounts.pause')}
              </Button>
              <Button
                size="sm"
                variant="secondary"
                icon={<Download size={13} />}
                onClick={() => setExportOpen(true)}
              >
                {t('accounts.exportSelected')}
              </Button>
              <Button
                size="sm"
                variant="danger"
                icon={<Trash2 size={13} />}
                disabled={selected.length > MAX_BULK_DELETE}
                onClick={() => setDeleteIds(selected)}
              >
                {t('accounts.delete')}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setSelected([])}>
                {t('accounts.clear')}
              </Button>
            </div>
          </div>
        ) : null}

        {accounts.status === 'error' ? (
          <ErrorState message={accounts.error ?? t('common.unknownError')} onRetry={accounts.reload} />
        ) : accounts.status === 'loading' && !accounts.data ? (
          view === 'cards' ? (
            <AccountCardsSkeleton />
          ) : (
            <TableSkeleton rows={6} columns={9} />
          )
        ) : view === 'cards' ? (
          <AccountCards
            accounts={filtered}
            selected={selected}
            empty={emptyState}
            onToggleSelected={toggleSelected}
            onTest={(account) => void runTest(account)}
            onTogglePause={(account) => void togglePause(account)}
            onLogout={(account) => setLogoutId(account.id)}
            onDelete={(id) => setDeleteIds([id])}
            onRenewal={(account) => setRenewalId(account.id)}
          />
        ) : (
          <DataTable
            caption={t('accounts.caption')}
            columns={columns}
            rows={filtered}
            rowKey={(account) => account.id}
            empty={emptyState}
            density="comfy"
          />
        )}
      </Card>

      <Modal
        open={createOpen}
        title={t('accounts.newTitle')}
        description={
          isMockMode() ? t('accounts.newSubtitleMock') : t('accounts.newSubtitleLive')
        }
        onClose={closeCreate}
        size="md"
        footer={
          <>
            <Button variant="ghost" onClick={closeCreate} disabled={saving}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="create-account-form" variant="primary" loading={saving}>
              {t('accounts.create')}
            </Button>
          </>
        }
      >
        <form id="create-account-form" className="form" onSubmit={submitForm} noValidate>
          <Field label={t('accounts.label')} htmlFor="account-label" error={formErrors.label}>
            <Input
              id="account-label"
              value={form.label}
              onChange={(event) => setForm({ ...form, label: event.target.value })}
              placeholder={t('accounts.labelPlaceholder')}
              autoComplete="off"
            />
          </Field>
          <Field label={t('accounts.username')} htmlFor="account-username" error={formErrors.username}>
            <Input
              id="account-username"
              value={form.username}
              onChange={(event) => setForm({ ...form, username: event.target.value })}
              placeholder={t('accounts.usernamePlaceholder')}
              autoComplete="off"
            />
          </Field>
          <Field
            label={t('accounts.password')}
            htmlFor="account-password"
            error={formErrors.password}
            hint={
              isMockMode() ? t('accounts.passwordHintMock') : t('accounts.passwordHintLive')
            }
          >
            <Input
              id="account-password"
              type="password"
              value={form.password}
              onChange={(event) => setForm({ ...form, password: event.target.value })}
              placeholder={t('accounts.passwordPlaceholder')}
              autoComplete="new-password"
            />
          </Field>
          <Field label={t('accounts.platform')} htmlFor="account-platform">
            <Select
              variant="form"
              id="account-platform"
              value={form.platform}
              onChange={(value) => setForm({ ...form, platform: value as Platform })}
              options={PLATFORMS.map((platform) => ({ value: platform, label: platform }))}
            />
          </Field>
          <p className="form-note" role="note">
            {t('accounts.concurrencyNote')}
          </p>
          <Field label={t('accounts.regions')} htmlFor="account-regions" hint={t('accounts.regionsHint')}>
            <Input
              id="account-regions"
              value={form.regions}
              onChange={(event) => setForm({ ...form, regions: event.target.value })}
              placeholder={t('accounts.regionsPlaceholder')}
            />
          </Field>
          {formError ? (
            <p className="form-error" role="alert">
              {formError}
            </p>
          ) : null}
        </form>
      </Modal>

      <ConfirmDialog
        open={deleteIds.length > 0}
        title={
          deleteIds.length > 1
            ? t('accounts.deleteTitleMany', { count: deleteIds.length })
            : t('accounts.deleteTitleOne')
        }
        description={t('accounts.deleteDesc')}
        confirmLabel={
          deleteIds.length > 1
            ? t('accounts.deleteConfirmMany', { count: deleteIds.length })
            : t('accounts.deleteConfirmOne')
        }
        busy={deleting}
        requireText={deleteIds.length > 10 ? 'DELETE' : undefined}
        requireTextLabel={t('accounts.bulkDeleteConfirmLabel')}
        onConfirm={() => void confirmDelete()}
        onCancel={() => setDeleteIds([])}
      />

      <AccountExportDialog
        open={exportOpen}
        accounts={accounts.data ?? []}
        filtered={filtered}
        selected={selected}
        onClose={() => setExportOpen(false)}
      />

      <AccountImportDialog
        open={importOpen}
        onClose={() => setImportOpen(false)}
        onImported={() => accounts.reload()}
      />

      <Drawer
        open={renewalAccount !== null}
        title={
          renewalAccount
            ? t('accounts.renewal.drawerTitle', { name: renewalAccount.label })
            : t('accounts.renewal.title')
        }
        subtitle={t('accounts.renewal.drawerSubtitle')}
        onClose={() => setRenewalId(null)}
      >
        {renewalAccount ? (
          <RenewalCard
            accountId={renewalAccount.id}
            accountLabel={renewalAccount.label}
            expiresAt={renewalAccount.expiresAt}
          />
        ) : null}
      </Drawer>

      <ConfirmDialog
        open={logoutId !== null}
        title={t('accounts.logoutTitle')}
        description={
          logoutId
            ? t('accounts.logoutDesc', {
                name: accounts.data?.find((account) => account.id === logoutId)?.label ?? logoutId,
              })
            : ''
        }
        confirmLabel={t('accounts.logoutConfirm')}
        tone="primary"
        busy={loggingOut}
        onConfirm={() => void confirmLogout()}
        onCancel={() => setLogoutId(null)}
      />
    </div>
  )
}
