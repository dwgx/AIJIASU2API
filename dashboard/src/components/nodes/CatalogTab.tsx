import clsx from 'clsx'
import { Search, Server, ShieldAlert } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { formatDateTime } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { nodeStateMeta, serverStatusTone } from '../../lib/lab'
import { regionName } from '../../lib/regions'
import { catalogRowKey, groupCatalogByRegion } from '../../lib/nodes'
import type { CatalogRow, ServerCatalog } from '../../lib/nodes'
import type { AsyncState } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import {
  Badge,
  Button,
  Card,
  Checkbox,
  EmptyState,
  ErrorState,
  Input,
  Select,
  StatusDot,
  StatusPill,
  TableSkeleton,
} from '../primitives'

interface CatalogTabProps {
  catalog: AsyncState<ServerCatalog>
  query: string
  onQueryChange: (value: string) => void
  /** Selection is owned by the page so switching views does not drop it. */
  selected: string[]
  onSelectedChange: (keys: string[]) => void
}

/**
 * Read-only vendor catalog. Every row is a `ServerView` the daemon returned
 * from `GET /v1/accounts/{id}/servers`; nothing here is invented, so an empty
 * or stale snapshot is explained instead of filled with placeholder servers.
 */
export function CatalogTab({ catalog, query, onQueryChange, selected, onSelectedChange }: CatalogTabProps) {
  const { t, lang } = useI18n()
  const [regionFilter, setRegionFilter] = useState('all')
  const [statusFilter, setStatusFilter] = useState('all')

  const rows = catalog.data?.rows ?? []
  const staleAccounts = catalog.data?.staleAccounts ?? []
  const failedAccounts = catalog.data?.failedAccounts ?? []
  const disabledReason = catalog.data?.disabledReason ?? null

  const regions = useMemo(
    () => Array.from(new Set(rows.map((row) => row.region))).sort(),
    [rows],
  )
  const statuses = useMemo(
    () => Array.from(new Set(rows.map((row) => row.status).filter(Boolean))).sort(),
    [rows],
  )

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return rows.filter((row) => {
      if (regionFilter !== 'all' && row.region !== regionFilter) return false
      if (statusFilter !== 'all' && row.status !== statusFilter) return false
      if (!needle) return true
      return `${row.title} ${row.id} ${row.host} ${row.region} ${regionName(row.region, lang)} ${row.accountLabel}`
        .toLowerCase()
        .includes(needle)
    })
  }, [rows, query, regionFilter, statusFilter, lang])

  const groups = useMemo(() => groupCatalogByRegion(filtered), [filtered])

  // A poll can drop the last server of a region or status; drop the filter too
  // so the select never shows a value it no longer offers.
  useEffect(() => {
    if (regionFilter !== 'all' && !regions.includes(regionFilter)) setRegionFilter('all')
    if (statusFilter !== 'all' && !statuses.includes(statusFilter)) setStatusFilter('all')
  }, [regionFilter, regions, statusFilter, statuses])

  // A poll can drop a server; keep the count honest instead of counting ghosts.
  useEffect(() => {
    if (!catalog.data) return
    const present = new Set(catalog.data.rows.map(catalogRowKey))
    const next = selected.filter((key) => present.has(key))
    if (next.length !== selected.length) onSelectedChange(next)
  }, [catalog.data, selected, onSelectedChange])

  const toggleSelected = (key: string) => {
    onSelectedChange(
      selected.includes(key) ? selected.filter((value) => value !== key) : [...selected, key],
    )
  }

  const toggleGroup = (groupRows: CatalogRow[]) => {
    const keys = groupRows.map(catalogRowKey)
    const allSelected = keys.every((key) => selected.includes(key))
    onSelectedChange(
      allSelected
        ? selected.filter((key) => !keys.includes(key))
        : [...selected, ...keys.filter((key) => !selected.includes(key))],
    )
  }

  const clearFilters = () => {
    onQueryChange('')
    setRegionFilter('all')
    setStatusFilter('all')
  }

  const columns: Array<Column<CatalogRow>> = [
    {
      key: 'select',
      label: '',
      width: '36px',
      render: (row) => (
        <Checkbox
          aria-label={t('nodes.catalog.selectServer', { name: row.title })}
          checked={selected.includes(catalogRowKey(row))}
          onChange={() => toggleSelected(catalogRowKey(row))}
        />
      ),
    },
    {
      key: 'server',
      label: t('nodes.catalog.columnServer'),
      render: (row) => (
        <div className="cell-stack">
          <span className="cell-strong">{row.title}</span>
          <span className="cell-sub mono">{row.id}</span>
        </div>
      ),
    },
    {
      key: 'account',
      label: t('nodes.catalog.columnAccount'),
      render: (row) => <span className="cell-sub">{row.accountLabel}</span>,
    },
    {
      key: 'region',
      label: t('nodes.columnRegion'),
      render: (row) => (
        <div className="cell-stack">
          <span>{regionName(row.region, lang)}</span>
          <span className="cell-sub mono">{row.region}</span>
        </div>
      ),
    },
    {
      key: 'host',
      label: t('nodes.catalog.columnHost'),
      render: (row) => (
        <span className="cell-mono">
          {row.host}:{row.port}
        </span>
      ),
    },
    {
      key: 'status',
      label: t('nodes.columnStatus'),
      render: (row) => <Badge tone={serverStatusTone(row.status)}>{row.status}</Badge>,
    },
    {
      key: 'runtime',
      label: t('nodes.catalog.columnRuntime'),
      render: (row) => {
        if (!row.nodeId) return <span className="cell-sub">{t('nodes.catalog.notLinked')}</span>
        if (row.nodeState === 'LIVE') {
          return (
            <div className="cell-stack">
              <StatusPill status="ok" pulse>
                {t('nodes.catalog.current')}
              </StatusPill>
              <span className="cell-sub mono">{row.nodeId}</span>
            </div>
          )
        }
        const meta = row.nodeState ? nodeStateMeta(row.nodeState) : null
        return (
          <div className="cell-stack">
            {meta ? (
              <span className="state-cell">
                <StatusDot tone={meta.tone} />
                <span className="cell-sub">{meta.labelKey ? t(meta.labelKey) : meta.label}</span>
              </span>
            ) : null}
            <span className="cell-sub mono">{row.nodeId}</span>
          </div>
        )
      },
    },
    {
      key: 'selection',
      label: t('nodes.catalog.columnSelection'),
      render: (row) => (
        <Badge tone={row.skippedByLimit ? 'warning' : row.selected ? 'success' : 'neutral'}>
          {row.skippedByLimit
            ? t('nodes.catalog.skipped')
            : row.selected
              ? t('nodes.catalog.selected')
              : t('nodes.catalog.notSelected')}
        </Badge>
      ),
    },
    {
      key: 'flags',
      label: t('nodes.catalog.columnFlags'),
      render: (row) => (
        <span className="inline-badges">
          {row.fullLoad ? <Badge tone="warning">{t('nodes.catalog.fullLoad')}</Badge> : null}
          {row.memberOnly === true ? (
            <Badge tone="info">{t('nodes.catalog.memberOnly')}</Badge>
          ) : null}
          {row.blockingMarks.length > 0 ? (
            <Badge tone="danger">{row.blockingMarks.join(', ')}</Badge>
          ) : null}
          {row.hasPingSecret ? (
            <Badge tone="neutral" title={t('nodes.catalog.pingSecretHint')}>
              {t('nodes.catalog.pingSecret')}
            </Badge>
          ) : null}
          {row.stale ? <Badge tone="neutral">{t('nodes.catalog.staleBadge')}</Badge> : null}
        </span>
      ),
    },
  ]

  const emptyCatalog = (
    <EmptyState
      icon={<Server size={20} aria-hidden="true" />}
      title={
        catalog.data?.accounts === 0 ? t('nodes.catalog.noAccountsTitle') : t('nodes.catalog.emptyTitle')
      }
      description={
        catalog.data?.accounts === 0
          ? t('nodes.catalog.noAccountsDesc')
          : t('nodes.catalog.emptyDesc')
      }
    />
  )

  return (
    <div className="stack-lg">
      {disabledReason !== null ? (
        <div className="callout callout--warning">
          <span className="callout__icon">
            <ShieldAlert size={18} aria-hidden="true" />
          </span>
          <div>
            <p className="callout__title">{t('nodes.catalog.disabledTitle')}</p>
            <p className="callout__text">{disabledReason || t('nodes.catalog.disabledText')}</p>
          </div>
        </div>
      ) : null}

      {staleAccounts.length > 0 ? (
        <div className="callout callout--muted">
          <span className="callout__icon">
            <Server size={18} aria-hidden="true" />
          </span>
          <div>
            <p className="callout__title">{t('nodes.catalog.staleTitle')}</p>
            <p className="callout__text">
              {t('nodes.catalog.staleText', {
                time: catalog.data?.fetchedAt
                  ? formatDateTime(catalog.data.fetchedAt)
                  : t('common.never'),
                accounts: staleAccounts.join(', '),
              })}
            </p>
          </div>
        </div>
      ) : null}

      {failedAccounts.length > 0 ? (
        <div className="callout callout--warning">
          <span className="callout__icon">
            <ShieldAlert size={18} aria-hidden="true" />
          </span>
          <div>
            <p className="callout__title">{t('nodes.catalog.failedTitle')}</p>
            <p className="callout__text">
              {t('nodes.catalog.failedText', { accounts: failedAccounts.join(', ') })}
            </p>
          </div>
        </div>
      ) : null}

      <Card bodyClassName="card__body--flush">
        <div className="toolbar">
          <div className="toolbar__search">
            <Search size={14} aria-hidden="true" />
            <Input
              aria-label={t('nodes.searchAria')}
              placeholder={t('nodes.catalog.searchPlaceholder')}
              value={query}
              onChange={(event) => onQueryChange(event.target.value)}
            />
          </div>
          <Select
            aria-label={t('nodes.filterRegion')}
            value={regionFilter}
            onChange={(event) => setRegionFilter(event.target.value)}
          >
            <option value="all">{t('nodes.allRegions')}</option>
            {regions.map((region) => (
              <option key={region} value={region}>
                {regionName(region, lang)}
              </option>
            ))}
          </Select>
          <Select
            aria-label={t('nodes.catalog.filterStatus')}
            value={statusFilter}
            onChange={(event) => setStatusFilter(event.target.value)}
          >
            <option value="all">{t('nodes.allStatuses')}</option>
            {statuses.map((status) => (
              <option key={status} value={status}>
                {status}
              </option>
            ))}
          </Select>
          <span className="toolbar__count">
            {t('common.of', { shown: filtered.length, total: rows.length })}
          </span>
        </div>

        {selected.length > 0 ? (
          <div className="bulkbar">
            <span className="bulkbar__count">
              {t('nodes.catalog.selectedCount', { count: selected.length })}
            </span>
            <span className="cell-sub bulkbar__hint">{t('nodes.catalog.selectionLocal')}</span>
            <div className="bulkbar__actions">
              <Button size="sm" variant="ghost" onClick={() => onSelectedChange([])}>
                {t('nodes.catalog.clearSelection')}
              </Button>
            </div>
          </div>
        ) : null}

        {catalog.status === 'error' ? (
          <ErrorState
            message={catalog.error ?? t('common.unknownError')}
            onRetry={catalog.reload}
          />
        ) : catalog.status === 'loading' && !catalog.data ? (
          <TableSkeleton rows={8} columns={9} />
        ) : rows.length === 0 ? (
          emptyCatalog
        ) : filtered.length === 0 ? (
          <EmptyState
            icon={<Server size={20} aria-hidden="true" />}
            title={t('nodes.catalog.noMatchTitle')}
            description={t('nodes.catalog.noMatchDesc')}
            action={
              <Button variant="secondary" onClick={clearFilters}>
                {t('common.clearFilters')}
              </Button>
            }
          />
        ) : (
          groups.map((group) => {
            const groupSelected = group.rows.every((row) => selected.includes(catalogRowKey(row)))
            const groupPartial = group.rows.some((row) => selected.includes(catalogRowKey(row)))
            return (
              <section
                key={group.region}
                className="catalog-group"
                aria-labelledby={`catalog-region-${group.region}`}
              >
                <div className="catalog-group__head">
                  <h2 className="catalog-group__title" id={`catalog-region-${group.region}`}>
                    {regionName(group.region, lang)}
                  </h2>
                  <span className="catalog-group__code mono">{group.region}</span>
                  <span className="catalog-group__count">
                    {t(
                      group.rows.length === 1
                        ? 'nodes.catalog.groupCountOne'
                        : 'nodes.catalog.groupCount',
                      { count: group.rows.length },
                    )}
                  </span>
                </div>
                <DataTable
                  caption={t('nodes.catalog.groupCaption', { region: regionName(group.region, lang) })}
                  columns={columns.map((column) =>
                    column.key === 'select'
                      ? {
                          ...column,
                          label: (
                            <Checkbox
                              aria-label={t('nodes.catalog.selectAllIn', {
                                region: regionName(group.region, lang),
                              })}
                              checked={groupSelected}
                              indeterminate={!groupSelected && groupPartial}
                              onChange={() => toggleGroup(group.rows)}
                            />
                          ),
                        }
                      : column,
                  )}
                  rows={group.rows}
                  rowKey={catalogRowKey}
                  rowClassName={(row) =>
                    clsx(
                      row.nodeState === 'LIVE' && 'is-current',
                      selected.includes(catalogRowKey(row)) && 'is-selected',
                    )
                  }
                />
              </section>
            )
          })
        )}
      </Card>
    </div>
  )
}
