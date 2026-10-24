import clsx from 'clsx'
import {
  Archive,
  Download,
  Eye,
  Link2,
  Plus,
  Search,
  ShieldAlert,
  Trash2,
  Unlink,
  Upload,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { api } from '../lib/api'
import { formatDate } from '../lib/format'
import { useI18n } from '../lib/i18n'
import { DEVICE_POOL_STATES } from '../lib/types'
import type {
  DevicePoolConfirmAction,
  DevicePoolRecord,
  DevicePoolState,
  Platform,
} from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { Modal } from '../components/Overlay'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  Input,
  PageHeader,
  Select,
  TableSkeleton,
} from '../components/primitives'
import { useToast } from '../components/Toast'
import { DeviceDetailDrawer } from '../components/devicepool/DeviceDetailDrawer'
import { DeviceExportDialog } from '../components/devicepool/DeviceExportDialog'
import { DeviceImportDialog } from '../components/devicepool/DeviceImportDialog'
import { GenerateDeviceDialog } from '../components/devicepool/GenerateDeviceDialog'
import { LifecycleDialog } from '../components/devicepool/LifecycleDialog'
import {
  deviceActions,
  deviceErrorText,
  deviceLabel,
  deviceStateTone,
  shortHash,
} from '../components/devicepool/shared'
import '../components/devicepool/device-pool.css'

const PLATFORM_FILTERS: Platform[] = ['android', 'ios', 'windows', 'linux']

const ACTION_ICONS: Record<'assign' | 'release' | 'retire' | 'quarantine' | 'purge', ReactNode> = {
  assign: <Link2 size={14} />,
  release: <Unlink size={14} />,
  retire: <Archive size={14} />,
  quarantine: <ShieldAlert size={14} />,
  purge: <Trash2 size={14} />,
}

/** Delays the server-side search so typing does not issue one request per key. */
function useDebounced<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delayMs)
    return () => window.clearTimeout(timer)
  }, [value, delayMs])
  return debounced
}

export default function DevicePool() {
  const { t } = useI18n()
  const { push } = useToast()
  const [stateFilter, setStateFilter] = useState<'all' | DevicePoolState>('all')
  const [platformFilter, setPlatformFilter] = useState<'all' | Platform>('all')
  const [searchInput, setSearchInput] = useState('')
  const search = useDebounced(searchInput.trim(), 250)

  const [generateOpen, setGenerateOpen] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [detailId, setDetailId] = useState<string | null>(null)

  const [assignTarget, setAssignTarget] = useState<DevicePoolRecord | null>(null)
  const [assignAccount, setAssignAccount] = useState('')
  const [assignBusy, setAssignBusy] = useState(false)
  const [assignError, setAssignError] = useState('')

  const [action, setAction] = useState<{
    kind: DevicePoolConfirmAction
    record: DevicePoolRecord
  } | null>(null)
  const [actionBusy, setActionBusy] = useState(false)
  const [actionError, setActionError] = useState('')

  const pool = useAsync(
    () =>
      api.listDevicePool({
        ...(stateFilter === 'all' ? {} : { state: stateFilter }),
        ...(platformFilter === 'all' ? {} : { platform: platformFilter }),
        ...(search ? { q: search } : {}),
      }),
    [stateFilter, platformFilter, search],
    { intervalMs: 30_000 },
  )
  const accounts = useAsync(() => api.listAccounts(), [])

  const view = pool.data
  const enabled = view?.enabled !== false
  const counts = view?.counts ?? {}
  const filtering = stateFilter !== 'all' || platformFilter !== 'all' || search !== ''

  const openAction = (kind: DevicePoolConfirmAction, record: DevicePoolRecord) => {
    setAction({ kind, record })
    setActionError('')
  }

  const openAssign = (record: DevicePoolRecord) => {
    setAssignTarget(record)
    setAssignAccount('')
    setAssignError('')
  }

  const submitAssign = async () => {
    if (!assignTarget) return
    if (!assignAccount) {
      setAssignError(t('devicePool.assign.needAccount'))
      return
    }
    setAssignBusy(true)
    setAssignError('')
    try {
      const updated = await api.assignDevice(assignTarget.id, assignAccount)
      push(
        t('devicePool.assign.done', { account: updated.accountLabel ?? assignAccount }),
        'success',
      )
      setAssignTarget(null)
      setAssignAccount('')
      pool.reload()
    } catch (cause) {
      setAssignError(deviceErrorText(t, cause, t('devicePool.assign.failed')))
    } finally {
      setAssignBusy(false)
    }
  }

  const submitAction = async (reason: string) => {
    if (!action) return
    const { kind, record } = action
    setActionBusy(true)
    setActionError('')
    try {
      if (kind === 'release') await api.releaseDevice(record.id)
      else if (kind === 'retire') await api.retireDevice(record.id, reason)
      else if (kind === 'quarantine') await api.quarantineDevice(record.id, reason)
      else await api.purgeDevice(record.id)
      push(t(`devicePool.lifecycle.done.${kind}`), 'success')
      setAction(null)
      if (detailId === record.id) setDetailId(null)
      pool.reload()
    } catch (cause) {
      setActionError(deviceErrorText(t, cause, t('devicePool.lifecycle.failed')))
    } finally {
      setActionBusy(false)
    }
  }

  const columns: Array<Column<DevicePoolRecord>> = [
    {
      key: 'device',
      label: t('devicePool.columnDevice'),
      render: (record) => (
        <div className="cell-stack">
          <span className="cell-strong">{deviceLabel(record)}</span>
          <span className="cell-sub mono">{record.id}</span>
          {record.warnings.length > 0 ? (
            <Badge tone="warning">{t('devicePool.detail.materialMissing')}</Badge>
          ) : null}
        </div>
      ),
    },
    {
      key: 'profile',
      label: t('devicePool.columnProfile'),
      render: (record) => (
        <div className="cell-stack">
          <span>{record.platform}</span>
          <span className="cell-sub mono">{record.profile}</span>
        </div>
      ),
    },
    {
      key: 'state',
      label: t('devicePool.columnState'),
      render: (record) => (
        <Badge tone={deviceStateTone(record.state)}>{t(`devicePool.state.${record.state}`)}</Badge>
      ),
    },
    {
      key: 'account',
      label: t('devicePool.columnAccount'),
      render: (record) =>
        record.accountId ? (
          <div className="cell-stack">
            <span>{record.accountLabel || record.accountId}</span>
            <span className="cell-sub mono">{record.accountId}</span>
          </div>
        ) : (
          <span className="cell-sub">{t('devicePool.unbound')}</span>
        ),
    },
    {
      key: 'presented',
      label: t('devicePool.columnCreated'),
      render: (record) => (
        <div className="cell-stack">
          <span>{formatDate(record.createdAt)}</span>
          <span className="cell-sub">
            {record.presentedAt
              ? formatDate(record.presentedAt)
              : t('devicePool.neverPresented')}
          </span>
        </div>
      ),
    },
    {
      key: 'hashes',
      label: t('devicePool.columnHashes'),
      render: (record) => {
        const entries = Object.entries(record.hashes)
        if (entries.length === 0) {
          return <span className="cell-sub">{t('devicePool.noHashes')}</span>
        }
        return (
          <div className="cell-stack">
            {entries.slice(0, 2).map(([field, value]) => (
              <span key={field} className="cell-sub mono">
                {field} {shortHash(value)}
              </span>
            ))}
            {entries.length > 2 ? (
              <span className="cell-sub">
                {t('devicePool.hashesMore', { count: entries.length - 2 })}
              </span>
            ) : null}
          </div>
        )
      },
    },
    {
      key: 'actions',
      label: '',
      align: 'right',
      width: '200px',
      render: (record) => (
        <span className="row-actions" onClick={(event) => event.stopPropagation()}>
          {deviceActions(record).map((kind) => (
            <IconButton
              key={kind}
              label={t(`devicePool.action.${kind}For`, { id: record.id })}
              icon={ACTION_ICONS[kind]}
              disabled={!enabled}
              onClick={() =>
                kind === 'assign' ? openAssign(record) : openAction(kind, record)
              }
            />
          ))}
          <IconButton
            label={t('devicePool.action.detailsFor', { id: record.id })}
            icon={<Eye size={14} />}
            onClick={() => setDetailId(record.id)}
          />
        </span>
      ),
    },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('devicePool.title')}
        subtitle={t('devicePool.subtitle', {
          count: view?.total ?? 0,
          available: counts.available ?? 0,
          assigned: counts.assigned ?? 0,
          retired: counts.retired ?? 0,
        })}
        refresh={summarizeRefresh([pool])}
        actions={
          <>
            <Button
              variant="secondary"
              icon={<Download size={14} />}
              disabled={!enabled}
              onClick={() => setExportOpen(true)}
            >
              {t('devicePool.export')}
            </Button>
            <Button
              variant="secondary"
              icon={<Upload size={14} />}
              disabled={!enabled}
              onClick={() => setImportOpen(true)}
            >
              {t('devicePool.import')}
            </Button>
            <Button
              variant="primary"
              icon={<Plus size={14} />}
              disabled={!enabled}
              onClick={() => setGenerateOpen(true)}
            >
              {t('devicePool.generate')}
            </Button>
          </>
        }
      />

      {view && !view.enabled ? (
        <>
          <div className="callout callout--warning" role="status">
            <span className="callout__icon">
              <ShieldAlert size={18} aria-hidden="true" />
            </span>
            <div>
              <p className="callout__title">{t('devicePool.disabledTitle')}</p>
              <p className="callout__text">{t('devicePool.disabledText')}</p>
              {view.reason ? (
                <p className="callout__text mono">
                  {t('devicePool.disabledReason', { reason: view.reason })}
                </p>
              ) : null}
            </div>
          </div>
          <Card>
            <EmptyState
              title={t('devicePool.disabledTitle')}
              description={t('devicePool.disabledText')}
              icon={<ShieldAlert size={20} aria-hidden="true" />}
            />
          </Card>
        </>
      ) : (
        <>
          {view?.warnings?.length ? (
            <div className="callout callout--warning" role="status">
              <span className="callout__icon">
                <ShieldAlert size={18} aria-hidden="true" />
              </span>
              <div>
                <p className="callout__title">{t('devicePool.warningsTitle')}</p>
                <ul className="device-pool__list">
                  {view.warnings.map((warning) => (
                    <li key={warning}>{warning}</li>
                  ))}
                </ul>
              </div>
            </div>
          ) : null}

          <div className="device-pool__counts" role="group" aria-label={t('devicePool.filterState')}>
            <button
              type="button"
              className={clsx('device-pool__count', stateFilter === 'all' && 'is-active')}
              aria-pressed={stateFilter === 'all'}
              onClick={() => setStateFilter('all')}
            >
              <span className="device-pool__count-value">{view?.total ?? 0}</span>
              <span className="device-pool__count-label">{t('devicePool.counts.total')}</span>
            </button>
            {DEVICE_POOL_STATES.map((state) => (
              <button
                key={state}
                type="button"
                className={clsx('device-pool__count', stateFilter === state && 'is-active')}
                aria-pressed={stateFilter === state}
                onClick={() => setStateFilter(stateFilter === state ? 'all' : state)}
              >
                <span className="device-pool__count-value">{counts[state] ?? 0}</span>
                <span className="device-pool__count-label">{t(`devicePool.counts.${state}`)}</span>
              </button>
            ))}
          </div>

          <Card bodyClassName="card__body--flush">
            <div className="toolbar device-pool__toolbar">
              <div className="toolbar__search">
                <Search size={14} aria-hidden="true" />
                <Input
                  aria-label={t('devicePool.searchLabel')}
                  placeholder={t('devicePool.searchPlaceholder')}
                  value={searchInput}
                  onChange={(event) => setSearchInput(event.target.value)}
                />
              </div>
              <Select
                aria-label={t('devicePool.filterState')}
                value={stateFilter}
                options={[
                  { value: 'all', label: t('devicePool.allStates') },
                  ...DEVICE_POOL_STATES.map((state) => ({
                    value: state,
                    label: t(`devicePool.state.${state}`),
                  })),
                ]}
                onChange={(value) => setStateFilter(value as 'all' | DevicePoolState)}
              />
              <Select
                aria-label={t('devicePool.filterPlatform')}
                value={platformFilter}
                options={[
                  { value: 'all', label: t('devicePool.allPlatforms') },
                  ...PLATFORM_FILTERS.map((platform) => ({ value: platform, label: platform })),
                ]}
                onChange={(value) => setPlatformFilter(value as 'all' | Platform)}
              />
              <span className="toolbar__count">
                {t('common.of', {
                  shown: view?.devices.length ?? 0,
                  total: view?.total ?? 0,
                })}
              </span>
            </div>

            {pool.status === 'error' && !view ? (
              <ErrorState
                message={pool.error ?? t('common.unknownError')}
                onRetry={pool.reload}
              />
            ) : !view ? (
              <TableSkeleton rows={7} columns={7} />
            ) : (
              <DataTable
                caption={t('devicePool.caption')}
                columns={columns}
                rows={view.devices}
                rowKey={(record) => record.id}
                onRowClick={(record) => setDetailId(record.id)}
                empty={
                  <EmptyState
                    title={
                      filtering
                        ? t('devicePool.emptyFilteredTitle')
                        : t('devicePool.emptyTitle')
                    }
                    description={
                      filtering
                        ? t('devicePool.emptyFilteredDesc')
                        : t('devicePool.emptyDesc')
                    }
                    icon={<Link2 size={20} aria-hidden="true" />}
                    action={
                      filtering ? (
                        <Button
                          variant="ghost"
                          onClick={() => {
                            setStateFilter('all')
                            setPlatformFilter('all')
                            setSearchInput('')
                          }}
                        >
                          {t('common.clearFilters')}
                        </Button>
                      ) : (
                        <Button
                          variant="primary"
                          icon={<Plus size={14} />}
                          onClick={() => setGenerateOpen(true)}
                        >
                          {t('devicePool.generate')}
                        </Button>
                      )
                    }
                  />
                }
              />
            )}
          </Card>
        </>
      )}

      <GenerateDeviceDialog
        open={generateOpen}
        onClose={() => setGenerateOpen(false)}
        onGenerated={() => pool.reload()}
      />
      <DeviceExportDialog
        open={exportOpen}
        onClose={() => setExportOpen(false)}
        onExported={() => pool.reload()}
      />
      <DeviceImportDialog
        open={importOpen}
        onClose={() => setImportOpen(false)}
        onImported={() => pool.reload()}
      />
      <DeviceDetailDrawer
        open={detailId !== null}
        deviceId={detailId}
        onClose={() => setDetailId(null)}
        onChanged={() => pool.reload()}
      />

      <Modal
        open={assignTarget !== null}
        title={t('devicePool.assign.title')}
        description={
          assignTarget
            ? t('devicePool.assign.desc', { id: deviceLabel(assignTarget) })
            : ''
        }
        onClose={() => setAssignTarget(null)}
        footer={
          <>
            <Button
              variant="ghost"
              onClick={() => setAssignTarget(null)}
              disabled={assignBusy}
            >
              {t('common.cancel')}
            </Button>
            <Button
              variant="primary"
              onClick={() => void submitAssign()}
              loading={assignBusy}
              disabled={!assignAccount}
            >
              {t('devicePool.assign.submit')}
            </Button>
          </>
        }
      >
        <div className="form">
          <Field label={t('devicePool.assign.account')}>
            <Select
              aria-label={t('devicePool.assign.account')}
              value={assignAccount}
              placeholder={t('devicePool.assign.placeholder')}
              searchable
              options={(accounts.data ?? []).map((account) => ({
                value: account.id,
                label: account.label,
                description: `${account.username} · ${account.platform}`,
              }))}
              onChange={setAssignAccount}
            />
          </Field>
          {accounts.status === 'error' ? (
            <p className="form-error" role="alert">
              {accounts.error ?? t('common.unknownError')}
            </p>
          ) : null}
          {accounts.data && accounts.data.length === 0 ? (
            <p className="form-note">{t('devicePool.assign.noAccounts')}</p>
          ) : null}
          {assignError ? (
            <p className="form-error" role="alert">
              {assignError}
            </p>
          ) : null}
        </div>
      </Modal>

      <LifecycleDialog
        action={action?.kind ?? null}
        record={action?.record ?? null}
        busy={actionBusy}
        error={actionError}
        onCancel={() => setAction(null)}
        onConfirm={(reason) => void submitAction(reason)}
      />
    </div>
  )
}
