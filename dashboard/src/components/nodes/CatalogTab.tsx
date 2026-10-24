import clsx from 'clsx'
import { Search, Server, ShieldAlert } from 'lucide-react'
import { useEffect, useMemo } from 'react'
import { formatDateTime, formatNumber } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { nodeStateMeta, serverStatusTone } from '../../lib/lab'
import { regionName } from '../../lib/regions'
import { parseServerTitle, resolveServerCity, summarizeCatalogGeo } from '../../lib/geo'
import {
  catalogRowKey,
  filterCatalogRows,
  groupCatalogByRegion,
  sortCatalogRows,
  summarizeCatalogRegions,
} from '../../lib/nodes'
import type { CatalogFilters, CatalogRow } from '../../lib/nodes'
import type { Account, AccountServers, CatalogSnapshot, ServerCatalogSort } from '../../lib/types'
import type { AsyncState } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import { Pagination } from '../Pagination'
import { ChinaMap } from './ChinaMap'
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
  catalog: AsyncState<AccountServers>
  accountId: string
  accountLabel: string
  accounts: Account[]
  accountsLoading: boolean
  /** Server-side query state: offset/limit live here, not in the table. */
  filters: CatalogFilters
  onFiltersChange: (next: CatalogFilters) => void
  query: string
  /** Debounced search actually applied to the rows; the map counts use it too. */
  activeQuery: string
  onQueryChange: (value: string) => void
  /** Every retained row for the account, from `GET /v1/accounts/{id}/servers`. */
  snapshot: AsyncState<CatalogSnapshot>
  /** Selection is owned by the page so switching views does not drop it. */
  selected: string[]
  onSelectedChange: (keys: string[]) => void
}

const SORT_KEYS: ServerCatalogSort[] = ['order', 'title', 'host', 'status', 'region']

/**
 * Read-only vendor catalog. The daemon pages `GET /v1/accounts/{id}/servers`
 * (default 50, max 500), so the page holds one window at a time: `total` and
 * `regions` describe every filtered row, `servers` only this page. Grouping is
 * applied to the page rows; the group header count comes from the server's
 * region summary so it never understates a region that spans pages.
 */
export function CatalogTab({
  catalog,
  accountId,
  accountLabel,
  accounts,
  accountsLoading,
  filters,
  onFiltersChange,
  query,
  activeQuery,
  onQueryChange,
  snapshot,
  selected,
  onSelectedChange,
}: CatalogTabProps) {
  const { t, lang } = useI18n()

  const total = catalog.data?.total ?? 0
  const pageRows = catalog.data?.servers ?? []
  const regionCounts = catalog.data?.regions ?? []
  const stale = catalog.data?.stale ?? false
  const page = Math.floor(filters.offset / filters.limit) + 1

  const pageCatalogRows = useMemo<CatalogRow[]>(
    () =>
      pageRows.map((server) => ({
        ...server,
        accountId,
        accountLabel,
        stale,
      })),
    [accountId, accountLabel, pageRows, stale],
  )

  // The map and the province filter read the whole retained catalog. The
  // server keeps paging the table; these rows are only the geo view's source.
  // `useAsync` keeps the last good value while a new account loads, so a
  // mismatched account id must read as "not loaded yet", not as the previous
  // account's geography under the new label.
  const snapshotLoaded = snapshot.data !== undefined && snapshot.data.accountId === accountId
  const snapshotRows = useMemo<CatalogRow[]>(
    () =>
      (snapshotLoaded ? (snapshot.data?.servers ?? []) : []).map((server) => ({
        ...server,
        accountId,
        accountLabel,
        stale: snapshot.data?.stale ?? false,
      })),
    [accountId, accountLabel, snapshot.data, snapshotLoaded],
  )

  const scopeRows = useMemo(
    () =>
      filterCatalogRows(snapshotRows, {
        q: activeQuery,
        status: filters.status,
        region: filters.region,
      }),
    [activeQuery, filters.region, filters.status, snapshotRows],
  )

  // The current node stays marked even when the search or a status filter
  // excludes it from the table: the chip names it, the ring marks its province.
  const currentRow = useMemo(
    () => snapshotRows.find((row) => row.nodeState === 'LIVE') ?? null,
    [snapshotRows],
  )
  const geo = useMemo(() => {
    const summary = summarizeCatalogGeo(scopeRows)
    if (!currentRow) return summary
    const city = resolveServerCity(currentRow.title)
    const provinceCode = city?.provinceCode ?? null
    return {
      ...summary,
      current: currentRow,
      currentCity: city,
      currentProvince: provinceCode,
      provinces: summary.provinces.map((province) => ({
        ...province,
        current: province.code === provinceCode,
      })),
    }
  }, [currentRow, scopeRows])

  const geoActive = filters.province !== '' || filters.city !== ''
  const geoRows = useMemo(
    () =>
      geoActive
        ? sortCatalogRows(
            filterCatalogRows(scopeRows, { province: filters.province, city: filters.city }),
            filters.sort,
          )
        : null,
    [filters.city, filters.province, filters.sort, geoActive, scopeRows],
  )

  // In geo mode the browser is the source of the page envelope: total, region
  // counts and the visible slice all describe the filtered snapshot.
  const displayTotal = geoRows ? geoRows.length : total
  const tableRows = useMemo(
    () =>
      geoRows
        ? geoRows.slice(filters.offset, filters.offset + filters.limit)
        : pageCatalogRows,
    [filters.limit, filters.offset, geoRows, pageCatalogRows],
  )
  const groups = useMemo(() => groupCatalogByRegion(tableRows), [tableRows])
  const effectiveRegionCounts = geoRows ? summarizeCatalogRegions(geoRows) : regionCounts

  const regionOptions = useMemo(() => {
    const options = [
      { value: 'all', label: t('nodes.allRegions') },
      ...regionCounts.map((region) => ({
        value: region.code,
        label: regionName(region.code, lang),
      })),
    ]
    if (filters.region !== 'all' && !regionCounts.some((region) => region.code === filters.region)) {
      options.push({ value: filters.region, label: regionName(filters.region, lang) })
    }
    return options
  }, [filters.region, lang, regionCounts, t])

  const statusOptions = useMemo(() => {
    const statuses = new Set(['ok', 'offline'])
    for (const row of pageRows) if (row.status) statuses.add(row.status)
    if (filters.status !== 'all') statuses.add(filters.status)
    return [
      { value: 'all', label: t('nodes.allStatuses') },
      ...[...statuses].sort().map((status) => ({ value: status, label: status })),
    ]
  }, [filters.status, pageRows, t])

  // The table reads the server page normally, but the filtered snapshot when a
  // province/city filter is active. One state pair drives the error/skeleton
  // branches so a stale account's rows are never shown under a new label.
  const tableError = geoActive
    ? snapshotLoaded
      ? undefined
      : snapshot.error
    : catalog.status === 'error'
      ? (catalog.error ?? t('common.unknownError'))
      : undefined
  const tableLoading = geoActive
    ? !snapshotLoaded && tableError === undefined
    : catalog.status === 'loading' && !catalog.data

  const filtersActive =
    query.trim() !== '' ||
    filters.region !== 'all' ||
    filters.status !== 'all' ||
    filters.province !== '' ||
    filters.city !== ''

  // The server clamps an offset past the end to an empty page; pull the
  // operator back to the last real page instead of showing a blank table.
  useEffect(() => {
    if (displayTotal === 0) return
    if (filters.offset >= displayTotal) {
      onFiltersChange({
        ...filters,
        offset: Math.floor((displayTotal - 1) / filters.limit) * filters.limit,
      })
    }
  }, [displayTotal, filters, onFiltersChange])

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
    onFiltersChange({
      ...filters,
      region: 'all',
      status: 'all',
      province: '',
      city: '',
      offset: 0,
    })
  }

  /** Clicking the selected province again clears the geo filter (design §1.4). */
  const selectProvince = (code: string) => {
    onFiltersChange({
      ...filters,
      province: filters.province === code ? '' : code,
      city: '',
      offset: 0,
    })
  }

  const cityOptions = useMemo(() => {
    if (!filters.province || filters.province === '00') return []
    const cities = new Map<string, { label: string; count: number }>()
    for (const row of scopeRows) {
      const city = resolveServerCity(row.title)
      if (!city || city.provinceCode !== filters.province) continue
      const label = parseServerTitle(row.title)
      const bucket = cities.get(city.code)
      if (bucket) bucket.count += 1
      else cities.set(city.code, { label, count: 1 })
    }
    return [...cities.entries()]
      .sort(([, left], [, right]) => left.label.localeCompare(right.label))
      .map(([code, bucket]) => ({
        value: code,
        label: t('nodes.catalog.cityOption', {
          city: bucket.label,
          count: formatNumber(bucket.count),
        }),
      }))
  }, [filters.province, scopeRows, t])

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
      title={t('nodes.catalog.emptyTitle')}
      description={t('nodes.catalog.emptyDesc')}
    />
  )

  const emptyState =
    accounts.length === 0 ? (
      <EmptyState
        icon={<Server size={20} aria-hidden="true" />}
        title={t('nodes.catalog.noAccountsTitle')}
        description={t('nodes.catalog.noAccountsDesc')}
      />
    ) : filtersActive ? (
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
      emptyCatalog
    )

  return (
    <div className="stack-lg">
      {catalog.data?.enabled === false ? (
        <div className="callout callout--warning">
          <span className="callout__icon">
            <ShieldAlert size={18} aria-hidden="true" />
          </span>
          <div>
            <p className="callout__title">{t('nodes.catalog.disabledTitle')}</p>
            <p className="callout__text">
              {catalog.data.reason || t('nodes.catalog.disabledText')}
            </p>
          </div>
        </div>
      ) : null}

      {stale && catalog.data ? (
        <div className="callout callout--muted">
          <span className="callout__icon">
            <Server size={18} aria-hidden="true" />
          </span>
          <div>
            <p className="callout__title">{t('nodes.catalog.staleTitle')}</p>
            <p className="callout__text">
              {t('nodes.catalog.staleTextSingle', {
                time: catalog.data.fetchedAt
                  ? formatDateTime(catalog.data.fetchedAt)
                  : t('common.never'),
                account: accountLabel || accountId,
              })}
            </p>
          </div>
        </div>
      ) : null}

      <div className="catalog-layout">
        <ChinaMap
          geo={geo}
          selected={filters.province}
          onSelect={selectProvince}
          loading={!snapshotLoaded && snapshot.error === undefined}
          error={!snapshotLoaded ? (snapshot.error ?? null) : null}
          onRetry={snapshot.reload}
          fetchedAt={snapshot.data?.fetchedAt}
          stale={snapshot.data?.stale === true || snapshot.staleAt !== undefined}
        />

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
            variant="region"
            searchable
            aria-label={t('nodes.filterRegion')}
            value={filters.region}
            onChange={(value) => onFiltersChange({ ...filters, region: value, offset: 0 })}
            options={regionOptions}
          />
          {cityOptions.length > 0 ? (
            <Select
              variant="filter"
              searchable
              aria-label={t('nodes.catalog.filterCity')}
              value={filters.city || 'all'}
              onChange={(value) =>
                onFiltersChange({ ...filters, city: value === 'all' ? '' : value, offset: 0 })
              }
              options={[
                { value: 'all', label: t('nodes.catalog.allCities') },
                ...cityOptions,
              ]}
            />
          ) : null}
          <Select
            variant="filter"
            aria-label={t('nodes.catalog.filterStatus')}
            value={filters.status}
            onChange={(value) => onFiltersChange({ ...filters, status: value, offset: 0 })}
            options={statusOptions}
          />
          <Select
            variant="filter"
            aria-label={t('nodes.catalog.filterSort')}
            value={filters.sort}
            onChange={(value) =>
              onFiltersChange({ ...filters, sort: value as ServerCatalogSort, offset: 0 })
            }
            options={SORT_KEYS.map((sort) => ({
              value: sort,
              label: t(`nodes.catalog.sort.${sort}`),
            }))}
          />
          <Button variant="ghost" size="sm" onClick={clearFilters} disabled={!filtersActive}>
            {t('common.clearFilters')}
          </Button>
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

        {tableError !== undefined ? (
          <ErrorState
            message={tableError}
            onRetry={geoActive ? snapshot.reload : catalog.reload}
          />
        ) : tableLoading || accountsLoading ? (
          <TableSkeleton rows={8} columns={9} />
        ) : displayTotal === 0 ? (
          emptyState
        ) : groups.length === 0 ? (
          <EmptyState
            icon={<Server size={20} aria-hidden="true" />}
            title={t('nodes.catalog.pageEmptyTitle')}
            description={t('nodes.catalog.pageEmptyDesc')}
            action={
              <Button
                variant="secondary"
                onClick={() => onFiltersChange({ ...filters, offset: 0 })}
              >
                {t('pagination.first')}
              </Button>
            }
          />
        ) : (
          groups.map((group) => {
            const groupSelected = group.rows.every((row) => selected.includes(catalogRowKey(row)))
            const groupPartial = group.rows.some((row) => selected.includes(catalogRowKey(row)))
            const regionTotal = effectiveRegionCounts.find(
              (region) => region.code === group.region,
            )?.count
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
                      (regionTotal ?? group.rows.length) === 1
                        ? 'nodes.catalog.groupCountOne'
                        : 'nodes.catalog.groupCount',
                      { count: formatNumber(regionTotal ?? group.rows.length) },
                    )}
                    {regionTotal !== undefined && regionTotal > group.rows.length ? (
                      <span className="catalog-group__slice">
                        {' '}
                        · {t('nodes.catalog.pageSlice', { shown: formatNumber(group.rows.length) })}
                      </span>
                    ) : null}
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
                              aria-label={t('nodes.catalog.selectVisibleIn', {
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

        {displayTotal > 0 ? (
          <Pagination
            page={page}
            pageSize={filters.limit}
            total={displayTotal}
            onPageChange={(next) =>
              onFiltersChange({ ...filters, offset: (next - 1) * filters.limit })
            }
            onPageSizeChange={(size) =>
              onFiltersChange({ ...filters, limit: size, offset: 0 })
            }
            label={t('nodes.catalog.pagination')}
          />
        ) : null}
        </Card>
      </div>
    </div>
  )
}
