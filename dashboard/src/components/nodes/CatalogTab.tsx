import clsx from 'clsx'
import { AlertTriangle, Search, Server, ShieldAlert } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, CatalogActionError } from '../../lib/api'
import { ApiError } from '../../lib/errors'
import { formatDateTime, formatNumber } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { nodeStateMeta, serverStatusTone } from '../../lib/lab'
import { regionName } from '../../lib/regions'
import { parseServerTitle, resolveServerCity, summarizeCatalogGeo, summarizeServerGeo } from '../../lib/geo'
import {
  catalogRowKey,
  filterCatalogRows,
  groupCatalogByRegion,
  sortCatalogRows,
  summarizeCatalogRegions,
} from '../../lib/nodes'
import type { CatalogFilters, CatalogRow } from '../../lib/nodes'
import type {
  Account,
  AccountServers,
  CatalogSelection,
  CatalogSnapshot,
  ConnectResult,
  ServerCatalogSort,
} from '../../lib/types'
import type { AsyncState } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import { ConfirmDialog, Modal } from '../Overlay'
import { Pagination } from '../Pagination'
import { useToast } from '../Toast'
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

type Translate = (key: string, params?: Record<string, string | number>) => string

type CatalogActionKind = 'target' | 'clear-target' | 'connect' | 'disconnect' | 'reconnect'

interface PendingAction {
  kind: CatalogActionKind
  serverId?: string
}

/** The running row the strip names; a server-geo daemon sends only these fields. */
interface CurrentRow {
  id: string
  title: string
  nodeState: CatalogRow['nodeState']
}

/** What a disconnect dialog needs to name its target. */
interface DisconnectTarget {
  id: string
  title: string
}

interface ActionFailure {
  message: string
  retryAfterSec: number | null
}

/** `error.reason` → localized copy (DESIGN-CATALOG §3.10). */
const REASON_KEYS: Record<string, string> = {
  login_required: 'nodes.catalog.reason.login_required',
  login_disabled: 'nodes.catalog.reason.login_disabled',
  account_state: 'nodes.catalog.reason.account_state',
  unknown_server: 'nodes.catalog.reason.unknown_server',
  snapshot_changed: 'nodes.catalog.reason.snapshot_changed',
  server_ineligible: 'nodes.catalog.reason.server_ineligible',
  switch_in_progress: 'nodes.catalog.reason.switch_in_progress',
  connect_cooldown: 'nodes.catalog.reason.connect_cooldown',
  multiple_sessions: 'nodes.catalog.reason.multiple_sessions',
  session_open_failed: 'nodes.catalog.reason.session_open_failed',
  gate_start_failed: 'nodes.catalog.reason.gate_start_failed',
  store_failed: 'nodes.catalog.reason.store_failed',
  reconcile_failed: 'nodes.catalog.reason.reconcile_failed',
  no_target: 'nodes.catalog.reason.no_target',
  rotation_disabled: 'nodes.catalog.reason.rotation_disabled',
  rotation_invalid: 'nodes.catalog.reason.rotation_invalid',
  rotation_conflict: 'nodes.catalog.reason.rotation_conflict',
}

const INELIGIBLE_KEYS: Record<string, string> = {
  login_disabled: 'nodes.catalog.ineligible.login_disabled',
  account_state: 'nodes.catalog.ineligible.account_state',
  stale: 'nodes.catalog.ineligible.stale',
  vendor_status: 'nodes.catalog.ineligible.vendor_status',
  full_load: 'nodes.catalog.ineligible.full_load',
  blocked: 'nodes.catalog.ineligible.blocked',
  member_only: 'nodes.catalog.ineligible.member_only',
}

/** Classified warnings on a 200 connect result (never raw vendor text). */
const WARNING_KEYS: Record<string, string> = {
  reconcile_failed: 'nodes.catalog.warning.reconcile_failed',
  switch_cleanup_failed: 'nodes.catalog.warning.switch_cleanup_failed',
  gate_cleanup_failed: 'nodes.catalog.warning.switch_cleanup_failed',
}

function warningCopy(warnings: string[] | undefined, t: Translate): string | null {
  if (!warnings || warnings.length === 0) return null
  const key = WARNING_KEYS[warnings[0] ?? '']
  return key ? t(key) : t('nodes.catalog.warning.unknown')
}

function ineligibleCopy(reason: string | null | undefined, t: Translate): string {
  const key = reason ? INELIGIBLE_KEYS[reason] : undefined
  return t(key ?? 'nodes.catalog.ineligible.unknown')
}

/**
 * Prefers the daemon's derived eligibility. A P0 daemon sends no `eligible`
 * field, so the same priority is derived locally from the raw vendor flags.
 */
function rowEligibility(
  row: CatalogRow,
  enabled: boolean,
  stale: boolean,
): { eligible: boolean; reason: string | null } {
  if (typeof row.eligible === 'boolean') {
    return { eligible: row.eligible, reason: row.ineligibleReason ?? null }
  }
  if (!enabled) return { eligible: false, reason: 'login_disabled' }
  if (stale || row.stale) return { eligible: false, reason: 'stale' }
  if (row.status !== 'ok') return { eligible: false, reason: 'vendor_status' }
  if (row.fullLoad === true) return { eligible: false, reason: 'full_load' }
  if (row.blockingMarks.length > 0) return { eligible: false, reason: 'blocked' }
  if (row.memberOnly === true) return { eligible: false, reason: 'member_only' }
  return { eligible: true, reason: null }
}

function describeActionError(error: unknown, t: Translate): ActionFailure {
  if (error instanceof CatalogActionError) {
    const retryAfterSec = error.retryAfterSec
    if (error.reason === 'server_ineligible') {
      const detail =
        typeof error.details?.ineligibleReason === 'string'
          ? error.details.ineligibleReason
          : null
      return { message: ineligibleCopy(detail, t), retryAfterSec }
    }
    const key = error.reason ? REASON_KEYS[error.reason] : undefined
    return { message: key ? t(key) : error.message, retryAfterSec }
  }
  if (error instanceof ApiError) return { message: error.message, retryAfterSec: null }
  return {
    message: error instanceof Error ? error.message : t('common.unknownError'),
    retryAfterSec: null,
  }
}

/**
 * Vendor catalog for one account. The daemon pages
 * `GET /v1/accounts/{id}/servers` (default 50, max 500), so the page holds one
 * window at a time: `total` and `regions` describe every filtered row,
 * `servers` only this page. Grouping is applied to the page rows; the group
 * header count comes from the server's region summary so it never understates
 * a region that spans pages.
 *
 * P1 adds the persisted single target and the guarded writes. Current (a
 * running node) and target (the server the next login should use) are separate
 * row states: setting a target starts no session, and a switch moves only the
 * current badge.
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
  const toast = useToast()

  const [selection, setSelection] = useState<CatalogSelection | null>(null)
  const [selectionError, setSelectionError] = useState<string | null>(null)
  const [pending, setPending] = useState<PendingAction | null>(null)
  const [actionError, setActionError] = useState<ActionFailure | null>(null)
  const [retryAt, setRetryAt] = useState<number | null>(null)
  const [retrySeconds, setRetrySeconds] = useState(0)
  const [connectTarget, setConnectTarget] = useState<CatalogRow | null>(null)
  const [connectAck, setConnectAck] = useState(false)
  const [disconnectTarget, setDisconnectTarget] = useState<DisconnectTarget | null>(null)
  const selectionEpoch = useRef(0)

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

  // The map and the province filter read the whole retained catalog only on a
  // daemon without the `geo` aggregate (the P0 fallback). With it, the map,
  // city counts and the geo table filters all come from the catalog page.
  // `useAsync` keeps the last good value while a new account loads, so a
  // mismatched account id must read as "not loaded yet", not as the previous
  // account's geography under the new label.
  const serverGeo =
    catalog.data?.accountId === accountId ? (catalog.data.geo ?? undefined) : undefined
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
  // The strip names every LIVE row, not just the first: the daemon refuses to
  // guess when more than one session is reported, and the operator must see it.
  const currentRows = useMemo<CurrentRow[]>(
    () =>
      serverGeo
        ? serverGeo.live.map((row) => ({ id: row.id, title: row.title, nodeState: row.nodeState }))
        : (snapshotLoaded ? snapshotRows : pageCatalogRows).filter(
            (row) => row.nodeState === 'LIVE',
          ),
    [pageCatalogRows, serverGeo, snapshotLoaded, snapshotRows],
  )
  const targetId = selection?.serverId ?? null
  const actionsStale = stale || (snapshotLoaded && snapshot.data?.stale === true)
  const catalogEnabled = catalog.data?.enabled !== false
  const actionBlockedHint = !catalogEnabled
    ? t('nodes.catalog.actionsDisabledLogin')
    : actionsStale
      ? t('nodes.catalog.actionsDisabledStale')
      : null
  const actionsLocked = pending !== null || retryAt !== null || actionBlockedHint !== null
  // Disconnecting is a safety action: a stale snapshot must not trap a session.
  // Login being off is the one case that disables every write.
  const disconnectLocked = pending !== null || retryAt !== null || !catalogEnabled
  const geo = useMemo(() => {
    if (serverGeo) return summarizeServerGeo(serverGeo)
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
  }, [currentRow, scopeRows, serverGeo])

  const geoActive = filters.province !== '' || filters.city !== ''
  const geoRows = useMemo(
    () =>
      !serverGeo && geoActive
        ? sortCatalogRows(
            filterCatalogRows(scopeRows, { province: filters.province, city: filters.city }),
            filters.sort,
          )
        : null,
    [filters.city, filters.province, filters.sort, geoActive, scopeRows, serverGeo],
  )

  // The server filters province/city in place; only the fallback pages the
  // filtered snapshot in the browser.
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

  // The table is server-paged in both modes. With the server geo aggregate,
  // province/city are query parameters and the page envelope already describes
  // the filtered set; the fallback filters the retained snapshot in the
  // browser. One state pair drives the error/skeleton branches so a stale
  // account's rows are never shown under a new label.
  const tableError = serverGeo
    ? catalog.status === 'error'
      ? (catalog.error ?? t('common.unknownError'))
      : undefined
    : geoActive
      ? snapshotLoaded
        ? undefined
        : snapshot.error
      : catalog.status === 'error'
        ? (catalog.error ?? t('common.unknownError'))
        : undefined
  const tableLoading = serverGeo
    ? catalog.status === 'loading' && !catalog.data
    : geoActive
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
    if (serverGeo) {
      const bucket = serverGeo.provinces.find(
        (province) => province.code === filters.province,
      )
      for (const city of bucket?.cities ?? []) {
        cities.set(city.code, { label: city.label, count: city.count })
      }
    } else {
      for (const row of scopeRows) {
        const city = resolveServerCity(row.title)
        if (!city || city.provinceCode !== filters.province) continue
        const label = parseServerTitle(row.title)
        const bucket = cities.get(city.code)
        if (bucket) bucket.count += 1
        else cities.set(city.code, { label, count: 1 })
      }
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
  }, [filters.province, scopeRows, serverGeo, t])

  const loadSelection = useCallback(async () => {
    if (!accountId) {
      setSelection(null)
      setSelectionError(null)
      return
    }
    const epoch = ++selectionEpoch.current
    try {
      const next = await api.getSelection(accountId)
      if (epoch !== selectionEpoch.current) return
      setSelection(next)
      setSelectionError(null)
    } catch (error) {
      if (epoch !== selectionEpoch.current) return
      setSelection(null)
      setSelectionError(error instanceof Error ? error.message : null)
    }
  }, [accountId])

  useEffect(() => {
    void loadSelection()
  }, [loadSelection])

  // A different account owns a different target and a different runtime.
  useEffect(() => {
    setPending(null)
    setActionError(null)
    setRetryAt(null)
    setConnectTarget(null)
    setConnectAck(false)
    setDisconnectTarget(null)
  }, [accountId])

  // 429 responses carry Retry-After; keep the buttons disabled until it ends.
  useEffect(() => {
    if (retryAt === null) {
      setRetrySeconds(0)
      return
    }
    const update = () => {
      const left = Math.max(0, Math.ceil((retryAt - Date.now()) / 1000))
      setRetrySeconds(left)
      if (left === 0) setRetryAt(null)
    }
    update()
    const timer = window.setInterval(update, 250)
    return () => window.clearInterval(timer)
  }, [retryAt])

  /**
   * A write changed the runtime or the target. Invalidate the full-catalog
   * cache first, then let every `useAsync` on the page (nodes, catalog,
   * snapshot, accounts) refetch through the existing `ajs:refresh` channel.
   */
  const refreshAfterAction = useCallback(() => {
    if (accountId) api.invalidateCatalogSnapshot(accountId)
    window.dispatchEvent(new Event('ajs:refresh'))
  }, [accountId])

  const runAction = useCallback(
    async <T,>(
      action: PendingAction,
      task: () => Promise<T>,
      success: (result: T) => string,
    ): Promise<T | null> => {
      setPending(action)
      setActionError(null)
      try {
        const result = await task()
        toast.push(success(result), 'success')
        await loadSelection()
        refreshAfterAction()
        return result
      } catch (error) {
        const failure = describeActionError(error, t)
        setActionError(failure)
        if (failure.retryAfterSec !== null && failure.retryAfterSec > 0) {
          setRetryAt(Date.now() + failure.retryAfterSec * 1000)
        }
        if (error instanceof CatalogActionError && error.reason === 'snapshot_changed') {
          refreshAfterAction()
        }
        toast.push(failure.message, 'danger')
        return null
      } finally {
        setPending(null)
      }
    },
    [loadSelection, refreshAfterAction, t, toast],
  )

  const setTarget = (row: CatalogRow) => {
    void runAction(
      { kind: 'target', serverId: row.id },
      () => api.setSelection(accountId, row.id, catalog.data?.snapshotId),
      () => t('nodes.catalog.toastTarget', { name: row.title }),
    )
  }

  const clearTarget = () => {
    void runAction(
      { kind: 'clear-target' },
      () => api.clearSelection(accountId),
      () => t('nodes.catalog.toastTargetCleared'),
    )
  }

  const openConnect = (row: CatalogRow) => {
    setActionError(null)
    setConnectAck(false)
    setConnectTarget(row)
  }

  const closeConnect = () => {
    if (pending !== null) return
    setConnectTarget(null)
    setConnectAck(false)
  }

  const submitConnect = async () => {
    const target = connectTarget
    if (!target) return
    const result = await runAction(
      { kind: 'connect', serverId: target.id },
      () => api.connectAccountServer(accountId, target.id, catalog.data?.snapshotId),
      (value) => {
        const warning = warningCopy(value.warnings, t)
        return warning
          ? t('nodes.catalog.toastConnectedWarning', {
              name: target.title,
              port: value.gatePort,
              warning,
            })
          : t('nodes.catalog.toastConnected', { name: target.title, port: value.gatePort })
      },
    )
    if (result) {
      setConnectTarget(null)
      setConnectAck(false)
    }
  }

  const submitDisconnect = async () => {
    const target = disconnectTarget
    const name =
      currentRows.length > 1 ? accountLabel || accountId : (target?.title ?? accountId)
    await runAction(
      { kind: 'disconnect', serverId: target?.id },
      () => api.disconnectAccount(accountId),
      () => t('nodes.catalog.toastDisconnected', { name }),
    )
    setDisconnectTarget(null)
  }

  const reconnect = async (target?: CatalogRow) => {
    await runAction(
      { kind: 'reconnect', serverId: target?.id },
      () => api.reconnectAccount(accountId),
      (result: ConnectResult) => {
        const name =
          snapshotRows.find((row) => row.id === result.serverId)?.title ??
          serverGeo?.live.find((row) => row.id === result.serverId)?.title ??
          selection?.title ??
          result.serverId
        const warning = warningCopy(result.warnings, t)
        return warning
          ? t('nodes.catalog.toastReconnectedWarning', { name, warning })
          : t('nodes.catalog.toastReconnected', { name })
      },
    )
  }

  const renderRowActions = (row: CatalogRow) => {
    const isTarget = targetId === row.id
    const isRunning = row.nodeState === 'LIVE' || row.nodeState === 'DRAINING'
    const eligibility = rowEligibility(row, catalogEnabled, actionsStale)
    const reasonCopy = eligibility.reason ? ineligibleCopy(eligibility.reason, t) : undefined
    const busyKind = pending?.serverId === row.id ? pending.kind : null
    return (
      <span
        className="row-actions"
        role="group"
        aria-label={t('nodes.catalog.rowActionsAria', { name: row.title })}
      >
        {isTarget ? null : (
          <Button
            size="sm"
            variant="ghost"
            disabled={actionsLocked || !eligibility.eligible}
            title={eligibility.eligible ? undefined : reasonCopy}
            loading={busyKind === 'target'}
            onClick={() => setTarget(row)}
          >
            {t('nodes.catalog.setTarget')}
          </Button>
        )}
        {isRunning ? (
          <>
            <Button
              size="sm"
              variant="ghost"
              disabled={disconnectLocked}
              loading={busyKind === 'disconnect'}
              onClick={() => setDisconnectTarget(row)}
            >
              {t('nodes.catalog.disconnect')}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={actionsLocked}
              loading={busyKind === 'reconnect'}
              onClick={() => void reconnect(row)}
            >
              {pending?.kind === 'reconnect'
                ? t('nodes.catalog.reconnecting')
                : t('nodes.catalog.reconnect')}
            </Button>
          </>
        ) : (
          <Button
            size="sm"
            variant="secondary"
            disabled={actionsLocked || !eligibility.eligible}
            title={eligibility.eligible ? undefined : reasonCopy}
            loading={busyKind === 'connect'}
            onClick={() => openConnect(row)}
          >
            {currentRows.length > 0
              ? t('nodes.catalog.switch')
              : t('nodes.catalog.connect')}
          </Button>
        )}
      </span>
    )
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
      key: 'target',
      label: t('nodes.catalog.columnTarget'),
      render: (row) =>
        targetId === row.id ? (
          <Badge tone="mint">{t('nodes.catalog.target')}</Badge>
        ) : null,
    },
    {
      key: 'flags',
      label: t('nodes.catalog.columnFlags'),
      render: (row) => (
        <span className="inline-badges">
          {row.fullLoad === true ? (
            <Badge tone="warning">{t('nodes.catalog.fullLoad')}</Badge>
          ) : null}
          {row.memberOnly === true ? (
            <Badge tone="info">{t('nodes.catalog.memberOnly')}</Badge>
          ) : null}
          {row.blockingMarks.length > 0 ? (
            <Badge tone="danger">{row.blockingMarks.join(', ')}</Badge>
          ) : null}
          {row.stale ? <Badge tone="neutral">{t('nodes.catalog.staleBadge')}</Badge> : null}
        </span>
      ),
    },
    {
      key: 'actions',
      label: '',
      align: 'right',
      render: renderRowActions,
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
          loading={
            serverGeo
              ? catalog.status === 'loading' && !catalog.data
              : !snapshotLoaded && snapshot.error === undefined
          }
          error={
            serverGeo
              ? catalog.status === 'error' && !catalog.data
                ? (catalog.error ?? null)
                : null
              : !snapshotLoaded
                ? (snapshot.error ?? null)
                : null
          }
          onRetry={serverGeo ? catalog.reload : snapshot.reload}
          fetchedAt={serverGeo ? catalog.data?.fetchedAt : snapshot.data?.fetchedAt}
          stale={
            serverGeo
              ? stale || catalog.staleAt !== undefined
              : snapshot.data?.stale === true || snapshot.staleAt !== undefined
          }
        />

        <Card bodyClassName="card__body--flush">
          <div
            className="toolbar"
            role="group"
            aria-label={t('nodes.catalog.actionsAria', { account: accountLabel || accountId })}
          >
            <span className="cell-sub">{t('nodes.catalog.currentSession')}</span>
            {currentRows.length > 0 ? (
              <span className="cell-strong">
                {currentRows.map((row) => row.title).join(', ')}
              </span>
            ) : (
              <span className="cell-sub">{t('nodes.catalog.noCurrent')}</span>
            )}
            <span className="cell-sub">{t('nodes.catalog.targetLabel')}</span>
            {selection ? (
              <>
                <Badge tone="mint" title={t('nodes.catalog.targetHint')}>
                  {selection.title || selection.serverId}
                </Badge>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={actionsLocked}
                  loading={pending?.kind === 'clear-target'}
                  onClick={clearTarget}
                >
                  {t('nodes.catalog.clearTarget')}
                </Button>
              </>
            ) : (
              <span className="cell-sub" title={t('nodes.catalog.targetHint')}>
                {t('nodes.catalog.noTarget')}
              </span>
            )}
            {currentRows.length > 0 ? (
              <Button
                size="sm"
                variant="ghost"
                disabled={disconnectLocked}
                loading={pending?.kind === 'disconnect'}
                onClick={() => setDisconnectTarget(currentRows[0] ?? null)}
              >
                {t('nodes.catalog.disconnect')}
              </Button>
            ) : null}
            <Button
              size="sm"
              variant="ghost"
              disabled={actionsLocked || (currentRows.length === 0 && selection === null)}
              loading={pending?.kind === 'reconnect'}
              onClick={() => void reconnect()}
            >
              {pending?.kind === 'reconnect'
                ? t('nodes.catalog.reconnecting')
                : t('nodes.catalog.reconnect')}
            </Button>
            <Link className="cell-sub" to="/logs">
              {t('nodes.catalog.auditLink')}
            </Link>
            {actionBlockedHint ? (
              <span className="cell-sub" role="status">
                {actionBlockedHint}
              </span>
            ) : null}
            {selectionError ? (
              <span className="cell-sub" title={selectionError} role="status">
                {t('nodes.catalog.targetUnavailable')}
              </span>
            ) : null}
          </div>

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

        {actionError && connectTarget === null ? (
          <div className="callout callout--warning" role="alert">
            <span className="callout__icon">
              <AlertTriangle size={18} aria-hidden="true" />
            </span>
            <div>
              <p className="callout__title">{t('nodes.catalog.actionFailed')}</p>
              <p className="callout__text">{actionError.message}</p>
              {retrySeconds > 0 ? (
                <p className="callout__text">
                  {t('nodes.catalog.retryAfter', { seconds: retrySeconds })}
                </p>
              ) : null}
            </div>
            <Button size="sm" variant="ghost" onClick={() => setActionError(null)}>
              {t('common.close')}
            </Button>
          </div>
        ) : null}

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
            onRetry={geoActive && !serverGeo ? snapshot.reload : catalog.reload}
          />
        ) : tableLoading || accountsLoading ? (
          <TableSkeleton rows={8} columns={10} />
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

      <Modal
        open={connectTarget !== null}
        title={
          connectTarget
            ? currentRows.length > 0
              ? t('nodes.catalog.switchTitle', { name: connectTarget.title })
              : t('nodes.catalog.connectTitle', { name: connectTarget.title })
            : ''
        }
        description={t('nodes.catalog.connectDesc', {
          account: accountLabel || accountId,
        })}
        onClose={closeConnect}
        footer={
          <>
            <Button variant="ghost" onClick={closeConnect} disabled={pending !== null}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="primary"
              loading={pending?.kind === 'connect'}
              disabled={(currentRows.length > 0 && !connectAck) || retryAt !== null}
              onClick={() => void submitConnect()}
            >
              {pending?.kind === 'connect'
                ? t('nodes.catalog.connecting')
                : currentRows.length > 0
                  ? t('nodes.catalog.switchConfirm')
                  : t('nodes.catalog.connectConfirm')}
            </Button>
          </>
        }
      >
        <p className="cell-sub">
          {currentRows.length === 0
            ? t('nodes.catalog.connectNoCurrent')
            : currentRows.length === 1
              ? t('nodes.catalog.connectOneSession', {
                  current: currentRows[0]?.title ?? '',
                })
              : t('nodes.catalog.connectManySessions', { count: currentRows.length })}
        </p>
        {currentRows.length > 0 ? (
          <Checkbox
            checked={connectAck}
            disabled={pending !== null}
            onChange={(event) => setConnectAck(event.target.checked)}
            label={t('nodes.catalog.connectAck')}
          />
        ) : null}
        {actionError ? (
          <div role="alert">
            <p className="cell-sub">{actionError.message}</p>
            {retrySeconds > 0 ? (
              <p className="cell-sub">
                {t('nodes.catalog.retryAfter', { seconds: retrySeconds })}
              </p>
            ) : null}
          </div>
        ) : null}
      </Modal>

      <ConfirmDialog
        open={disconnectTarget !== null}
        title={
          currentRows.length > 1
            ? t('nodes.catalog.disconnectAccountTitle')
            : t('nodes.catalog.disconnectTitle', {
                name: disconnectTarget?.title ?? (accountLabel || accountId),
              })
        }
        description={
          currentRows.length > 1
            ? t('nodes.catalog.disconnectAccountDesc', {
                account: accountLabel || accountId,
              })
            : t('nodes.catalog.disconnectDesc')
        }
        confirmLabel={
          pending?.kind === 'disconnect'
            ? t('nodes.catalog.disconnecting')
            : t('nodes.catalog.disconnectConfirm')
        }
        busy={pending?.kind === 'disconnect'}
        onConfirm={() => void submitDisconnect()}
        onCancel={() => {
          if (pending === null) setDisconnectTarget(null)
        }}
      />
    </div>
  )
}
