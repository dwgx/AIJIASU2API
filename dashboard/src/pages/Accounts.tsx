import clsx from 'clsx'
import { LayoutGrid, Pause, Play, Rows3, Search, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api, isMockMode } from '../lib/api'
import { formatBytes, formatDate, formatRelative } from '../lib/format'
import { ACCOUNT_STATE } from '../lib/labels'
import type { Account, AccountState, CreateAccountInput, Platform } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { ConfirmDialog, Drawer } from '../components/Overlay'
import { useToast } from '../components/Toast'
import {
  Badge,
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
  StatusDot,
  TableSkeleton,
} from '../components/primitives'

/**
 * Seeds the search box from the `q` param and follows later `q` changes (the command palette
 * can navigate here while the page is mounted) without clobbering a query the user typed on
 * re-renders that leave `q` alone — e.g. opening `?new=1` or closing the drawer.
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
const EMPTY_FORM = {
  label: '',
  username: '',
  password: '',
  platform: 'android' as Platform,
  concurrency: '50',
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
  return (
    <div className="segmented" role="group" aria-label="Account view">
      <button
        type="button"
        className={clsx('segmented__item', value === 'table' && 'is-active')}
        aria-pressed={value === 'table'}
        onClick={() => onChange('table')}
      >
        <Rows3 size={13} aria-hidden="true" />
        <span>Table</span>
      </button>
      <button
        type="button"
        className={clsx('segmented__item', value === 'cards' && 'is-active')}
        aria-pressed={value === 'cards'}
        onClick={() => onChange('cards')}
      >
        <LayoutGrid size={13} aria-hidden="true" />
        <span>Cards</span>
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
  onDelete: (id: string) => void
}

function AccountCard({ account, selected, onToggleSelected, onTest, onTogglePause, onDelete }: AccountCardProps) {
  const state = ACCOUNT_STATE[account.state]
  const capacity = account.nodeCount > 0 ? Math.min(1, Math.max(0, account.liveNodeCount / account.nodeCount)) : 0
  const capacityPct = Math.round(capacity * 100)

  return (
    <article role="listitem" className={clsx('account-card', selected && 'is-selected')}>
      <div className="account-card__head">
        <Checkbox
          aria-label={`Select ${account.label}`}
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
          <StatusDot tone={state.tone} pulse={account.state === 'active'} />
          <Badge tone={state.tone}>{state.label}</Badge>
        </span>
      </div>

      <div className="account-card__meta">
        <span className="account-card__platform">{account.platform}</span>
        <span className="account-card__regions">
          {account.regions.length > 0 ? account.regions.join(' · ') : 'any region'}
        </span>
      </div>

      <dl className="account-card__metrics">
        <div className="account-card__metric">
          <dt>Nodes</dt>
          <dd>
            <span>
              {account.liveNodeCount}/{account.nodeCount}
            </span>
            <span
              className={clsx('account-card__meter', account.liveNodeCount === 0 && 'is-empty')}
              role="progressbar"
              aria-label={`Node capacity for ${account.label}`}
              aria-valuenow={capacityPct}
              aria-valuemin={0}
              aria-valuemax={100}
            >
              <span className="account-card__meter-fill" style={{ width: `${capacityPct}%` }} />
            </span>
          </dd>
        </div>
        <div className="account-card__metric">
          <dt>Concurrency</dt>
          <dd>{account.concurrency}</dd>
        </div>
        <div className="account-card__metric">
          <dt>Usage</dt>
          <dd>{formatBytes(account.usageBytes)}</dd>
        </div>
      </dl>

      <div className="account-card__foot">
        <span className="account-card__expiry">
          <span className="account-card__expiry-label">Expires</span>
          <span className="account-card__expiry-date">{formatDate(account.expiresAt)}</span>
          <span className="account-card__expiry-rel">{formatRelative(account.expiresAt)}</span>
        </span>
        <span className="account-card__actions">
          <IconButton
            label={`Test login for ${account.label}`}
            icon={<Play size={14} />}
            onClick={() => onTest(account)}
          />
          <IconButton
            label={`${account.state === 'paused' ? 'Resume' : 'Pause'} ${account.label}`}
            icon={account.state === 'paused' ? <Play size={14} /> : <Pause size={14} />}
            onClick={() => onTogglePause(account)}
          />
          <IconButton
            label={`Delete ${account.label}`}
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
  onDelete,
}: {
  accounts: Account[]
  selected: string[]
  empty: ReactNode
  onToggleSelected: (id: string) => void
  onTest: (account: Account) => void
  onTogglePause: (account: Account) => void
  onDelete: (id: string) => void
}) {
  if (accounts.length === 0) {
    return <div className="account-grid account-grid--empty">{empty}</div>
  }

  return (
    <div className="account-grid" role="list" aria-label="Vendor accounts">
      {accounts.map((account) => (
        <AccountCard
          key={account.id}
          account={account}
          selected={selected.includes(account.id)}
          onToggleSelected={onToggleSelected}
          onTest={onTest}
          onTogglePause={onTogglePause}
          onDelete={onDelete}
        />
      ))}
    </div>
  )
}

function AccountCardsSkeleton() {
  return (
    <div className="account-grid" aria-busy="true" aria-label="Loading accounts">
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
  const [query, setQuery] = useParamQuery(params.get('q'))
  const [stateFilter, setStateFilter] = useState<'all' | AccountState>('all')
  const [platformFilter, setPlatformFilter] = useState<'all' | Platform>('all')
  const [view, setView] = useState<AccountView>(readAccountView)
  const [selected, setSelected] = useState<string[]>([])
  const [deleteIds, setDeleteIds] = useState<string[]>([])
  const [deleting, setDeleting] = useState(false)
  const [form, setForm] = useState(EMPTY_FORM)
  const [formErrors, setFormErrors] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState('')
  const [saving, setSaving] = useState(false)

  const drawerOpen = params.get('new') === '1'

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
      push(`Login test passed for ${account.label}`, 'success')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Login test failed', 'danger')
    }
  }

  const togglePause = async (account: Account) => {
    const next: AccountState = account.state === 'paused' ? 'active' : 'paused'
    try {
      await api.setAccountState(account.id, next)
      accounts.reload()
      push(`${account.label} ${next === 'paused' ? 'paused' : 'resumed'}`, 'success')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Could not update account', 'danger')
    }
  }

  const bulkPause = async () => {
    try {
      await Promise.all(selected.map((id) => api.setAccountState(id, 'paused')))
      accounts.reload()
      push(`${selected.length} account${selected.length === 1 ? '' : 's'} paused`, 'success')
      setSelected([])
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Bulk pause failed', 'danger')
    }
  }

  const confirmDelete = async () => {
    setDeleting(true)
    try {
      await api.deleteAccounts(deleteIds)
      accounts.reload()
      setSelected((current) => current.filter((id) => !deleteIds.includes(id)))
      push(`${deleteIds.length} account${deleteIds.length === 1 ? '' : 's'} deleted`, 'success')
      setDeleteIds([])
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Delete failed', 'danger')
    } finally {
      setDeleting(false)
    }
  }

  const submitForm = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const errors: Record<string, string> = {}
    if (!form.label.trim()) errors.label = 'Label is required'
    if (!form.username.trim()) errors.username = 'Username is required'
    if (!form.password.trim()) errors.password = 'Password is required'
    const concurrency = Number(form.concurrency)
    if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 2000) {
      errors.concurrency = 'Use a whole number between 1 and 2000'
    }
    if ((accounts.data ?? []).some((account) => account.label.toLowerCase() === form.label.trim().toLowerCase())) {
      errors.label = 'This label is already in use'
    }
    setFormErrors(errors)
    if (Object.keys(errors).length > 0) return

    const input: CreateAccountInput = {
      label: form.label.trim(),
      username: form.username.trim(),
      password: form.password,
      platform: form.platform,
      concurrency,
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
      push(`Account ${input.label} created`, 'success')
      closeCreate()
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : 'Could not create account')
    } finally {
      setSaving(false)
    }
  }

  const columns: Array<Column<Account>> = [
    {
      key: 'select',
      label: (
        <Checkbox
          aria-label="Select all visible accounts"
          checked={allVisibleSelected}
          indeterminate={!allVisibleSelected && someVisibleSelected}
          onChange={toggleAllVisible}
        />
      ),
      width: '36px',
      render: (account) => (
        <Checkbox
          aria-label={`Select ${account.label}`}
          checked={selected.includes(account.id)}
          onChange={() => toggleSelected(account.id)}
        />
      ),
    },
    {
      key: 'account',
      label: 'Account',
      render: (account) => (
        <div className="cell-stack">
          <span className="cell-strong">{account.label}</span>
          <span className="cell-sub mono">{account.username}</span>
        </div>
      ),
    },
    {
      key: 'platform',
      label: 'Platform',
      render: (account) => <span className="cell-mono">{account.platform}</span>,
    },
    {
      key: 'state',
      label: 'State',
      render: (account) => {
        const state = ACCOUNT_STATE[account.state]
        return (
          <span className="state-cell">
            <StatusDot tone={state.tone} pulse={account.state === 'active'} />
            <Badge tone={state.tone}>{state.label}</Badge>
          </span>
        )
      },
    },
    {
      key: 'nodes',
      label: 'Nodes',
      align: 'right',
      render: (account) => (
        <span className="cell-mono">
          {account.liveNodeCount}/{account.nodeCount}
        </span>
      ),
    },
    {
      key: 'concurrency',
      label: 'Concurrency',
      align: 'right',
      render: (account) => <span className="cell-mono">{account.concurrency}</span>,
    },
    {
      key: 'usage',
      label: 'Usage',
      align: 'right',
      render: (account) => <span className="cell-mono">{formatBytes(account.usageBytes)}</span>,
    },
    {
      key: 'expiry',
      label: 'Expires',
      render: (account) => (
        <span className="cell-stack">
          <span>{formatDate(account.expiresAt)}</span>
          <span className="cell-sub">{formatRelative(account.expiresAt)}</span>
        </span>
      ),
    },
    {
      key: 'actions',
      label: '',
      align: 'right',
      width: '124px',
      render: (account) => (
        <span className="row-actions">
          <IconButton
            label={`Test login for ${account.label}`}
            icon={<Play size={14} />}
            onClick={() => void runTest(account)}
          />
          <IconButton
            label={`${account.state === 'paused' ? 'Resume' : 'Pause'} ${account.label}`}
            icon={account.state === 'paused' ? <Play size={14} /> : <Pause size={14} />}
            onClick={() => void togglePause(account)}
          />
          <IconButton
            label={`Delete ${account.label}`}
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
      title="No accounts match these filters"
      description="Adjust the search or clear the state and platform filters."
      action={
        <Button
          variant="secondary"
          onClick={() => {
            setQuery('')
            setStateFilter('all')
            setPlatformFilter('all')
          }}
        >
          Clear filters
        </Button>
      }
    />
  )

  return (
    <div className="stack-lg">
      <PageHeader
        title="Accounts"
        subtitle="Vendor logins, concurrency ceilings and entitlement windows."
        refresh={summarizeRefresh([accounts])}
      />

      <Card bodyClassName="card__body--flush">
        <div className="toolbar">
          <div className="toolbar__search">
            <Search size={14} aria-hidden="true" />
            <Input
              aria-label="Search accounts"
              placeholder="Search label, username or region"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <Select
            aria-label="Filter by state"
            value={stateFilter}
            onChange={(event) => setStateFilter(event.target.value as 'all' | AccountState)}
          >
            <option value="all">All states</option>
            <option value="active">Active</option>
            <option value="paused">Paused</option>
            <option value="auth_failed">Auth failed</option>
            <option value="expired">Expired</option>
          </Select>
          <Select
            aria-label="Filter by platform"
            value={platformFilter}
            onChange={(event) => setPlatformFilter(event.target.value as 'all' | Platform)}
          >
            <option value="all">All platforms</option>
            {PLATFORMS.map((platform) => (
              <option key={platform} value={platform}>
                {platform}
              </option>
            ))}
          </Select>
          <AccountViewSwitch value={view} onChange={changeView} />
          <span className="toolbar__count">
            {filtered.length} of {accounts.data?.length ?? 0}
          </span>
        </div>

        {selected.length > 0 ? (
          <div className="bulkbar">
            <span className="bulkbar__count">{selected.length} selected</span>
            <div className="bulkbar__actions">
              <Button size="sm" variant="secondary" icon={<Pause size={13} />} onClick={() => void bulkPause()}>
                Pause
              </Button>
              <Button
                size="sm"
                variant="danger"
                icon={<Trash2 size={13} />}
                onClick={() => setDeleteIds(selected)}
              >
                Delete
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setSelected([])}>
                Clear
              </Button>
            </div>
          </div>
        ) : null}

        {accounts.status === 'error' ? (
          <ErrorState message={accounts.error ?? 'Unknown error'} onRetry={accounts.reload} />
        ) : accounts.status === 'loading' && !accounts.data ? (
          view === 'cards' ? (
            <AccountCardsSkeleton />
          ) : (
            <TableSkeleton rows={6} columns={8} />
          )
        ) : view === 'cards' ? (
          <AccountCards
            accounts={filtered}
            selected={selected}
            empty={emptyState}
            onToggleSelected={toggleSelected}
            onTest={(account) => void runTest(account)}
            onTogglePause={(account) => void togglePause(account)}
            onDelete={(id) => setDeleteIds([id])}
          />
        ) : (
          <DataTable
            caption="Vendor accounts"
            columns={columns}
            rows={filtered}
            rowKey={(account) => account.id}
            empty={emptyState}
          />
        )}
      </Card>

      <Drawer
        open={drawerOpen}
        title="New account"
        subtitle={
          isMockMode()
            ? 'Mock mode creates an in-memory account; no vendor call is made.'
            : 'ajs-accountd validates the credentials and adds the account to its store.'
        }
        onClose={closeCreate}
        footer={
          <>
            <Button variant="ghost" onClick={closeCreate} disabled={saving}>
              Cancel
            </Button>
            <Button type="submit" form="create-account-form" variant="primary" loading={saving}>
              Create account
            </Button>
          </>
        }
      >
        <form id="create-account-form" className="form" onSubmit={submitForm} noValidate>
          <Field label="Label" htmlFor="account-label" error={formErrors.label}>
            <Input
              id="account-label"
              value={form.label}
              onChange={(event) => setForm({ ...form, label: event.target.value })}
              placeholder="e.g. Northwind Ops"
              autoComplete="off"
            />
          </Field>
          <Field label="Username" htmlFor="account-username" error={formErrors.username}>
            <Input
              id="account-username"
              value={form.username}
              onChange={(event) => setForm({ ...form, username: event.target.value })}
              placeholder="demo-account"
              autoComplete="off"
            />
          </Field>
          <Field
            label="Password"
            htmlFor="account-password"
            error={formErrors.password}
            hint={
              isMockMode()
                ? 'Mock only — never stored, logged or sent.'
                : 'Sent to ajs-accountd over the loopback API; never kept in this browser.'
            }
          >
            <Input
              id="account-password"
              type="password"
              value={form.password}
              onChange={(event) => setForm({ ...form, password: event.target.value })}
              placeholder="placeholder-password"
              autoComplete="new-password"
            />
          </Field>
          <div className="form-grid">
            <Field label="Platform" htmlFor="account-platform">
              <Select
                id="account-platform"
                value={form.platform}
                onChange={(event) => setForm({ ...form, platform: event.target.value as Platform })}
              >
                {PLATFORMS.map((platform) => (
                  <option key={platform} value={platform}>
                    {platform}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Concurrency" htmlFor="account-concurrency" error={formErrors.concurrency}>
              <Input
                id="account-concurrency"
                inputMode="numeric"
                value={form.concurrency}
                onChange={(event) => setForm({ ...form, concurrency: event.target.value })}
              />
            </Field>
          </div>
          <Field label="Regions" htmlFor="account-regions" hint="Comma-separated tags, e.g. cn-zj, hk">
            <Input
              id="account-regions"
              value={form.regions}
              onChange={(event) => setForm({ ...form, regions: event.target.value })}
              placeholder="cn-zj, hk"
            />
          </Field>
          {formError ? (
            <p className="form-error" role="alert">
              {formError}
            </p>
          ) : null}
        </form>
      </Drawer>

      <ConfirmDialog
        open={deleteIds.length > 0}
        title={deleteIds.length > 1 ? `Delete ${deleteIds.length} accounts?` : 'Delete account?'}
        description="Nodes and leases tied to the selected account are removed with it. This cannot be undone."
        confirmLabel={deleteIds.length > 1 ? `Delete ${deleteIds.length} accounts` : 'Delete account'}
        busy={deleting}
        onConfirm={() => void confirmDelete()}
        onCancel={() => setDeleteIds([])}
      />
    </div>
  )
}
