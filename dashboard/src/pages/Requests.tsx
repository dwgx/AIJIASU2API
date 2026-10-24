import {
  ArrowDownUp,
  CheckCircle2,
  ChevronRight,
  FlaskConical,
  RefreshCw,
  Search,
  Timer,
  Waypoints,
  XCircle,
} from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '../lib/api'
import {
  formatBytes,
  formatDateTime,
  formatDateTimeSeconds,
  formatNumber,
  percent,
} from '../lib/format'
import { useI18n } from '../lib/i18n'
import type { FlowView, MutationLabInput, ProxyNode, RequestLog, Status } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { Drawer } from '../components/Overlay'
import { Pagination, usePagination } from '../components/Pagination'
import { useToast } from '../components/Toast'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  Input,
  MetricTile,
  PageHeader,
  SegmentedControl,
  Select,
  Skeleton,
  StatusPill,
  TableSkeleton,
  Textarea,
} from '../components/primitives'

const METHODS = ['CONNECT', 'GET', 'POST', 'PUT', 'DELETE', 'HEAD', 'OPTIONS']
const UNKNOWN_METHOD = '__unknown__'
const FLOW_LIMIT = 200
const RANGE_MS: Record<Exclude<TimeRange, 'all'>, number> = {
  '15m': 15 * 60_000,
  '1h': 60 * 60_000,
  '24h': 24 * 60 * 60_000,
}

type FlowSource = 'gate' | 'mutation-lab'
type StatusClass = 'all' | 'ok' | 'rejected'
type TimeRange = '15m' | '1h' | '24h' | 'all'

/** Normalized row: gate flows and mutation-lab records share one table. */
interface AuditRow {
  id: string
  startedAt: string
  accountId: string
  accountLabel: string
  nodeId: string
  nodeLabel: string
  method: string
  targetHost: string
  targetPort: number
  /** Gate vocabulary ("ok" / "rejected") or the mutation HTTP status. */
  status: string
  /** Numeric status for mutation rows; null for gate flows. */
  statusCode: number | null
  bytesIn: number
  bytesOut: number
  durationMs: number
  errorClass: string
  /** Already redacted server-side; never a full session id. */
  sid: string
  source: FlowSource
}

/** Eases a summary number from its previous value to the new one. */
function useAnimatedNumber(target: number, duration = 420): number {
  const [value, setValue] = useState(0)
  const currentRef = useRef(0)

  useEffect(() => {
    const from = currentRef.current
    const reduced =
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches

    if (reduced || from === target || typeof window.requestAnimationFrame !== 'function') {
      currentRef.current = target
      setValue(target)
      return
    }

    let frame = 0
    const startedAt = performance.now()
    const tick = (now: number) => {
      const progress = Math.min(1, (now - startedAt) / duration)
      const eased = 1 - (1 - progress) ** 3
      const next = from + (target - from) * eased
      currentRef.current = next
      setValue(next)
      if (progress < 1) {
        frame = window.requestAnimationFrame(tick)
      } else {
        currentRef.current = target
        setValue(target)
      }
    }
    frame = window.requestAnimationFrame(tick)
    return () => window.cancelAnimationFrame(frame)
  }, [duration, target])

  return value
}

function AnimatedNumber({
  value,
  format,
}: {
  value: number
  format: (value: number) => string
}) {
  const animated = useAnimatedNumber(value)
  return <>{format(animated)}</>
}

function SummarySkeleton() {
  return (
    <div className="metric-grid metric-grid--5" aria-busy="true">
      {Array.from({ length: 5 }, (_, index) => (
        <div className="metric" key={index}>
          <Skeleton width={90} height={10} />
          <Skeleton width={120} height={28} />
          <Skeleton width={70} height={10} />
        </div>
      ))}
    </div>
  )
}

/** Host and port only: a target is never rendered as a URL. */
function formatTarget(row: Pick<AuditRow, 'targetHost' | 'targetPort'>): string {
  if (!row.targetHost) return '—'
  const bracketed = row.targetHost.startsWith('[') && row.targetHost.endsWith(']')
  const host = row.targetHost.includes(':') && !bracketed ? `[${row.targetHost}]` : row.targetHost
  return `${host}:${row.targetPort}`
}

/**
 * Splits a mutation-lab target into host and port without keeping the path,
 * query or credentials that the operator typed into the form.
 */
function splitTarget(target: string): { host: string; port: number } {
  const trimmed = target.trim()
  try {
    const url = new URL(trimmed)
    const fallback = url.protocol === 'https:' ? 443 : 80
    const port = url.port ? Number(url.port) : fallback
    return {
      host: url.hostname.replace(/^\[/, '').replace(/\]$/, ''),
      port: Number.isFinite(port) ? port : fallback,
    }
  } catch {
    const authority = trimmed.split(/[/?#]/, 1)[0] ?? ''
    const withoutUser = authority.includes('@')
      ? authority.slice(authority.lastIndexOf('@') + 1)
      : authority
    const [host, rawPort] = withoutUser.split(':')
    const port = rawPort ? Number(rawPort) : 443
    return { host: host ?? '', port: Number.isFinite(port) ? port : 443 }
  }
}

function toGateRow(flow: FlowView, node: ProxyNode | undefined): AuditRow {
  return {
    id: flow.id,
    startedAt: flow.startedAt,
    accountId: flow.accountId,
    accountLabel: node?.accountLabel ?? flow.accountId,
    nodeId: flow.nodeId,
    nodeLabel: node?.label ?? flow.nodeId,
    method: flow.method,
    targetHost: flow.targetHost,
    targetPort: flow.targetPort,
    status: flow.status,
    statusCode: null,
    bytesIn: flow.bytesIn,
    bytesOut: flow.bytesOut,
    durationMs: flow.durationMs,
    errorClass: flow.errorClass ?? '',
    sid: flow.sid ?? '',
    source: 'gate',
  }
}

function toMutationRow(request: RequestLog): AuditRow {
  const { host, port } = splitTarget(request.target)
  return {
    id: request.id,
    startedAt: request.ts,
    accountId: '',
    accountLabel: request.accountLabel,
    nodeId: '',
    nodeLabel: request.nodeLabel,
    method: request.method,
    targetHost: host,
    targetPort: port,
    status: String(request.status),
    statusCode: request.status,
    bytesIn: 0,
    bytesOut: request.bytes,
    durationMs: request.latencyMs,
    errorClass: '',
    sid: '',
    source: 'mutation-lab',
  }
}

function rowStatus(row: AuditRow): Status {
  if (row.statusCode !== null) {
    if (row.statusCode < 300) return 'ok'
    if (row.statusCode < 400) return 'info'
    if (row.statusCode < 500) return 'warn'
    return 'error'
  }
  return row.status === 'ok' ? 'ok' : 'error'
}

function matchesStatusClass(row: AuditRow, filter: StatusClass): boolean {
  if (filter === 'all') return true
  const ok = row.statusCode !== null ? row.statusCode < 400 : row.status === 'ok'
  return filter === 'ok' ? ok : !ok
}

function p95(values: number[]): number {
  if (values.length === 0) return 0
  const sorted = [...values].sort((left, right) => left - right)
  const index = Math.min(sorted.length - 1, Math.ceil(sorted.length * 0.95) - 1)
  return sorted[index] ?? 0
}

export default function Requests() {
  const flows = useAsync(() => api.getLabFlows(FLOW_LIMIT), [], { intervalMs: 5_000 })
  const mutations = useAsync(() => api.listRequests(), [], { intervalMs: 10_000 })
  const nodes = useAsync(() => api.listNodes(), [], { intervalMs: 15_000 })
  const { push } = useToast()
  const { t } = useI18n()

  const [query, setQuery] = useState('')
  const [range, setRange] = useState<TimeRange>('1h')
  const [statusFilter, setStatusFilter] = useState<StatusClass>('all')
  const [methodFilter, setMethodFilter] = useState('all')
  const [nodeFilter, setNodeFilter] = useState('all')
  const [sourceFilter, setSourceFilter] = useState<'all' | FlowSource>('all')
  const [errorFilter, setErrorFilter] = useState('all')
  const [selected, setSelected] = useState<AuditRow | null>(null)
  const [sent, setSent] = useState<RequestLog[]>([])
  const [labOpen, setLabOpen] = useState(false)
  const [lab, setLab] = useState<MutationLabInput>({
    method: 'GET',
    target: 'api.example.com:443',
    nodeId: '',
    body: '{}',
  })
  const [labError, setLabError] = useState('')
  const [sending, setSending] = useState(false)

  const liveNodes = (nodes.data ?? []).filter((node) => node.state === 'LIVE')

  const mutationRequests = useMemo(() => {
    const byId = new Map<string, RequestLog>()
    for (const request of [...sent, ...(mutations.data ?? [])]) {
      if (request.source === 'mutation-lab') byId.set(request.id, request)
    }
    return [...byId.values()]
  }, [mutations.data, sent])

  const allRows = useMemo(() => {
    const nodeById = new Map((nodes.data ?? []).map((node) => [node.id, node]))
    const rows = [
      ...(flows.data?.flows ?? []).map((flow) => toGateRow(flow, nodeById.get(flow.nodeId))),
      ...mutationRequests.map(toMutationRow),
    ]
    rows.sort(
      (left, right) =>
        right.startedAt.localeCompare(left.startedAt) || right.id.localeCompare(left.id),
    )
    return rows
  }, [flows.data, mutationRequests, nodes.data])

  const methodOptions = useMemo(() => {
    const values = new Set(allRows.map((row) => row.method || UNKNOWN_METHOD))
    return [...values].sort((left, right) => left.localeCompare(right))
  }, [allRows])

  const nodeOptions = useMemo(() => {
    const values = new Map<string, string>()
    for (const row of allRows) {
      const key = `${row.accountLabel}|${row.nodeLabel}`
      if (!values.has(key)) values.set(key, `${row.accountLabel} · ${row.nodeLabel}`)
    }
    return [...values.entries()].sort((left, right) => left[1].localeCompare(right[1]))
  }, [allRows])

  const errorClassOptions = useMemo(() => {
    const values = new Set(allRows.map((row) => row.errorClass).filter(Boolean))
    return [...values].sort((left, right) => left.localeCompare(right))
  }, [allRows])

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    const now = Date.now()
    const cutoff = range === 'all' ? Number.NEGATIVE_INFINITY : now - RANGE_MS[range]

    return allRows.filter((row) => {
      if (new Date(row.startedAt).getTime() < cutoff) return false
      if (!matchesStatusClass(row, statusFilter)) return false
      if (methodFilter !== 'all' && (row.method || UNKNOWN_METHOD) !== methodFilter) return false
      if (nodeFilter !== 'all' && `${row.accountLabel}|${row.nodeLabel}` !== nodeFilter) return false
      if (sourceFilter !== 'all' && row.source !== sourceFilter) return false
      if (errorFilter !== 'all' && row.errorClass !== errorFilter) return false
      if (!needle) return true
      const haystack = [
        row.id,
        row.accountId,
        row.accountLabel,
        row.nodeId,
        row.nodeLabel,
        row.method,
        row.targetHost,
        String(row.targetPort),
        row.status,
        row.errorClass,
        row.source,
      ]
        .join(' ')
        .toLowerCase()
      return haystack.includes(needle)
    })
  }, [allRows, errorFilter, methodFilter, nodeFilter, query, range, sourceFilter, statusFilter])

  const summary = useMemo(() => {
    let accepted = 0
    let rejected = 0
    let bytes = 0
    const durations: number[] = []
    for (const row of filtered) {
      if (matchesStatusClass(row, 'ok')) accepted += 1
      else rejected += 1
      bytes += row.bytesIn + row.bytesOut
      durations.push(row.durationMs)
    }
    return { total: filtered.length, accepted, rejected, bytes, p95: p95(durations) }
  }, [filtered])

  // The gate ring itself is capped at FLOW_LIMIT (200), so the default page
  // shows the whole ring; operators can still step down to 25/50/100.
  const pagination = usePagination({
    total: filtered.length,
    initialPageSize: 200,
    resetKey: JSON.stringify([
      query,
      range,
      statusFilter,
      methodFilter,
      nodeFilter,
      sourceFilter,
      errorFilter,
    ]),
  })

  const filtersActive =
    query.trim() !== '' ||
    range !== '1h' ||
    statusFilter !== 'all' ||
    methodFilter !== 'all' ||
    nodeFilter !== 'all' ||
    sourceFilter !== 'all' ||
    errorFilter !== 'all'

  const clearFilters = () => {
    setQuery('')
    setRange('1h')
    setStatusFilter('all')
    setMethodFilter('all')
    setNodeFilter('all')
    setSourceFilter('all')
    setErrorFilter('all')
  }

  const reloadAll = () => {
    flows.reload()
    mutations.reload()
    nodes.reload()
  }

  const openLab = () => {
    setLab({
      method: 'GET',
      target: 'api.example.com:443',
      nodeId: liveNodes[0]?.id ?? '',
      body: '{}',
    })
    setLabError('')
    setSelected(null)
    setLabOpen(true)
  }

  const submitLab = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!lab.target.trim()) {
      setLabError(t('requests.targetRequired'))
      return
    }
    if (!lab.nodeId) {
      setLabError(t('requests.chooseLiveNode'))
      return
    }
    setSending(true)
    setLabError('')
    try {
      const entry = await api.createMutationRequest({ ...lab, target: lab.target.trim() })
      setSent((previous) => [entry, ...previous.filter((item) => item.id !== entry.id)])
      mutations.reload()
      push(t('requests.toastCompleted', { status: entry.status }), 'success')
      setLabOpen(false)
    } catch (cause) {
      setLabError(cause instanceof Error ? cause.message : t('requests.failed'))
    } finally {
      setSending(false)
    }
  }

  const sourceLabel = (source: FlowSource) =>
    source === 'gate' ? t('requests.sourceGate') : t('requests.sourceMutation')

  const columns: Array<Column<AuditRow>> = [
    {
      key: 'time',
      label: t('requests.columnTime'),
      width: '148px',
      render: (row) => <span className="cell-mono">{formatDateTime(row.startedAt)}</span>,
    },
    {
      key: 'node',
      label: t('requests.columnNode'),
      render: (row) => (
        <div className="cell-stack">
          <span>{row.accountLabel || t('common.unknown')}</span>
          <span className="cell-sub mono">{row.nodeLabel || row.nodeId || t('common.unknown')}</span>
        </div>
      ),
    },
    {
      key: 'method',
      label: t('requests.columnMethod'),
      render: (row) => <span className="cell-mono">{row.method || '—'}</span>,
    },
    {
      key: 'target',
      label: t('requests.columnTarget'),
      render: (row) => <span className="cell-mono cell-truncate">{formatTarget(row)}</span>,
    },
    {
      key: 'status',
      label: t('requests.columnStatus'),
      render: (row) => <StatusPill status={rowStatus(row)}>{row.status}</StatusPill>,
    },
    {
      key: 'bytes',
      label: t('requests.columnBytes'),
      align: 'right',
      render: (row) => (
        <span className="cell-mono">{formatBytes(row.bytesIn + row.bytesOut)}</span>
      ),
    },
    {
      key: 'latency',
      label: t('requests.columnLatency'),
      align: 'right',
      render: (row) => <span className="cell-mono">{formatNumber(row.durationMs)} ms</span>,
    },
    {
      key: 'source',
      label: t('requests.columnSource'),
      render: (row) => (
        <Badge tone={row.source === 'gate' ? 'mint' : 'warning'}>{sourceLabel(row.source)}</Badge>
      ),
    },
    {
      key: 'error',
      label: t('requests.columnError'),
      render: (row) =>
        row.errorClass ? (
          <span className="cell-mono cell-truncate">{row.errorClass}</span>
        ) : (
          <span className="cell-sub">—</span>
        ),
    },
    {
      key: 'detail',
      label: '',
      width: '44px',
      render: (row) => (
        <IconButton
          size="sm"
          label={t('requests.viewDetail', { id: row.id })}
          icon={<ChevronRight size={14} />}
          onClick={() => setSelected(row)}
        />
      ),
    },
  ]

  const flowsDisabled = flows.data !== undefined && !flows.data.enabled

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('requests.title')}
        subtitle={t('requests.subtitle')}
        refresh={summarizeRefresh([flows, mutations, nodes])}
        actions={
          <>
            <Badge tone="mint">{t('requests.liveBadge')}</Badge>
            <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={reloadAll}>
              {t('common.refresh')}
            </Button>
          </>
        }
      />

      <div className="callout callout--info" role="note">
        <span className="callout__icon">
          <Waypoints size={18} aria-hidden="true" />
        </span>
        <div>
          <p className="callout__title">{t('requests.calloutTitle')}</p>
          <p className="callout__text">{t('requests.calloutText')}</p>
        </div>
      </div>

      <Card className="lab-card">
        <div className="lab-card__inner">
          <span className="lab-card__icon">
            <FlaskConical size={20} aria-hidden="true" />
          </span>
          <div className="lab-card__text">
            <p className="lab-card__title">{t('requests.labCardTitle')}</p>
            <p className="lab-card__desc">
              {t('requests.labCardDescPrefix')}{' '}
              <code className="mono">{t('requests.sourceMutation')}</code>{' '}
              {t('requests.labCardDescSuffix')}
            </p>
          </div>
          <Button
            variant="primary"
            icon={<Waypoints size={14} />}
            onClick={openLab}
            disabled={liveNodes.length === 0}
          >
            {t('requests.openLab')}
          </Button>
        </div>
      </Card>

      {flows.status === 'loading' && !flows.data ? (
        <SummarySkeleton />
      ) : flows.status === 'error' && !flows.data ? null : (
        <div className="metric-grid metric-grid--5">
          <MetricTile
            label={t('requests.summaryTotal')}
            icon={<Waypoints size={14} />}
            value={
              <AnimatedNumber
                value={summary.total}
                format={(value) => formatNumber(Math.round(value))}
              />
            }
            hint={t('requests.summaryHint', {
              shown: summary.total,
              total: allRows.length,
              dropped: flows.data?.dropped ?? 0,
            })}
          />
          <MetricTile
            label={t('requests.summaryAccepted')}
            tone="success"
            icon={<CheckCircle2 size={14} />}
            value={
              <AnimatedNumber
                value={summary.accepted}
                format={(value) => formatNumber(Math.round(value))}
              />
            }
            hint={t('requests.summaryShare', {
              pct: Math.round(percent(summary.accepted, summary.total)),
            })}
          />
          <MetricTile
            label={t('requests.summaryRejected')}
            tone="danger"
            icon={<XCircle size={14} />}
            value={
              <AnimatedNumber
                value={summary.rejected}
                format={(value) => formatNumber(Math.round(value))}
              />
            }
            hint={t('requests.summaryShare', {
              pct: Math.round(percent(summary.rejected, summary.total)),
            })}
          />
          <MetricTile
            label={t('requests.summaryP95')}
            tone="info"
            icon={<Timer size={14} />}
            value={
              <AnimatedNumber
                value={summary.p95}
                format={(value) => `${formatNumber(Math.round(value))} ms`}
              />
            }
            hint={t('requests.summaryP95Hint')}
          />
          <MetricTile
            label={t('requests.summaryBytes')}
            tone="neutral"
            icon={<ArrowDownUp size={14} />}
            value={<AnimatedNumber value={summary.bytes} format={(value) => formatBytes(value)} />}
            hint={t('requests.summaryBytesHint')}
          />
        </div>
      )}

      <Card bodyClassName="card__body--flush">
        <div className="toolbar">
          <div className="toolbar__search">
            <Search size={14} aria-hidden="true" />
            <Input
              aria-label={t('requests.searchAria')}
              placeholder={t('requests.searchPlaceholder')}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <SegmentedControl
            label={t('requests.filterRange')}
            value={range}
            onChange={setRange}
            options={[
              { value: '15m', label: t('requests.range15m') },
              { value: '1h', label: t('requests.range1h') },
              { value: '24h', label: t('requests.range24h') },
              { value: 'all', label: t('requests.rangeAll') },
            ]}
          />
          <Select
            variant="filter"
            aria-label={t('requests.filterStatus')}
            value={statusFilter}
            onChange={(value) => setStatusFilter(value as StatusClass)}
            options={[
              { value: 'all', label: t('requests.allStatuses') },
              { value: 'ok', label: t('requests.statusOk') },
              { value: 'rejected', label: t('requests.statusRejected') },
            ]}
          />
          <Select
            variant="log"
            aria-label={t('requests.filterMethod')}
            value={methodFilter}
            onChange={setMethodFilter}
            options={[
              { value: 'all', label: t('requests.allMethods') },
              ...methodOptions.map((method) => ({
                value: method,
                label: method === UNKNOWN_METHOD ? t('requests.methodUnknown') : method,
              })),
            ]}
          />
          <Select
            variant="log"
            aria-label={t('requests.filterNode')}
            value={nodeFilter}
            onChange={setNodeFilter}
            options={[
              { value: 'all', label: t('requests.allNodes') },
              ...nodeOptions.map(([value, label]) => ({ value, label })),
            ]}
          />
          <Select
            variant="filter"
            aria-label={t('requests.filterSource')}
            value={sourceFilter}
            onChange={(value) => setSourceFilter(value as 'all' | FlowSource)}
            options={[
              { value: 'all', label: t('requests.allSources') },
              { value: 'gate', label: t('requests.gateFlows') },
              { value: 'mutation-lab', label: t('requests.mutationLab') },
            ]}
          />
          <Select
            variant="log"
            aria-label={t('requests.filterErrorClass')}
            value={errorFilter}
            onChange={setErrorFilter}
            options={[
              { value: 'all', label: t('requests.allErrorClasses') },
              ...errorClassOptions.map((errorClass) => ({ value: errorClass, label: errorClass })),
            ]}
          />
          <Button variant="ghost" size="sm" onClick={clearFilters} disabled={!filtersActive}>
            {t('common.clearFilters')}
          </Button>
        </div>

        {flows.status === 'error' && !flows.data ? (
          <ErrorState message={flows.error ?? t('common.unknownError')} onRetry={flows.reload} />
        ) : flows.status === 'loading' && !flows.data ? (
          <TableSkeleton rows={9} columns={9} />
        ) : (
          <>
            {flowsDisabled ? (
              <div className="card__body">
                <div className="callout callout--warning" role="note">
                  <span className="callout__icon">
                    <Waypoints size={18} aria-hidden="true" />
                  </span>
                  <div>
                    <p className="callout__title">{t('requests.disabledTitle')}</p>
                    <p className="callout__text">
                      {t('requests.disabledText', {
                        reason: flows.data?.reason ?? t('common.unknown'),
                      })}
                    </p>
                  </div>
                </div>
              </div>
            ) : null}
            <DataTable
              caption={t('requests.caption')}
              columns={columns}
              rows={pagination.slice(filtered)}
              rowKey={(row) => `${row.source}:${row.id}`}
              onRowClick={(row) => setSelected(row)}
              empty={
                allRows.length === 0 ? (
                  <EmptyState
                    title={t('requests.emptyTitle')}
                    description={t('requests.emptyRing')}
                    icon={<Waypoints size={20} />}
                  />
                ) : (
                  <EmptyState
                    title={t('requests.emptyFilteredTitle')}
                    description={t('requests.emptyFilteredDesc')}
                    action={
                      <Button variant="secondary" onClick={clearFilters}>
                        {t('common.clearFilters')}
                      </Button>
                    }
                  />
                )
              }
            />
            <Pagination
              page={pagination.page}
              pageSize={pagination.pageSize}
              total={filtered.length}
              onPageChange={pagination.setPage}
              onPageSizeChange={pagination.setPageSize}
              label={t('requests.pagination')}
            />
          </>
        )}
      </Card>

      <Drawer
        open={selected !== null}
        title={t('requests.detailTitle')}
        subtitle={
          selected ? t('requests.detailSubtitle', { source: sourceLabel(selected.source) }) : undefined
        }
        onClose={() => setSelected(null)}
        width={480}
      >
        {selected ? (
          <div className="stack">
            <div className="cell-stack">
              <StatusPill status={rowStatus(selected)}>{selected.status}</StatusPill>
              <span className="cell-sub mono">{formatDateTimeSeconds(selected.startedAt)}</span>
            </div>
            <dl className="facts">
              <div>
                <dt>{t('requests.detailRequestId')}</dt>
                <dd className="mono">{selected.id}</dd>
              </div>
              <div>
                <dt>{t('requests.detailSession')}</dt>
                <dd className="mono">{selected.sid || t('requests.detailNoSession')}</dd>
              </div>
              <div>
                <dt>{t('requests.detailAccount')}</dt>
                <dd>{selected.accountLabel || selected.accountId || '—'}</dd>
              </div>
              <div>
                <dt>{t('requests.detailNode')}</dt>
                <dd>{selected.nodeLabel || selected.nodeId || '—'}</dd>
              </div>
              <div>
                <dt>{t('requests.detailMethod')}</dt>
                <dd className="mono">{selected.method || '—'}</dd>
              </div>
              <div>
                <dt>{t('requests.detailTarget')}</dt>
                <dd className="mono">{formatTarget(selected)}</dd>
              </div>
              <div>
                <dt>{t('requests.detailDuration')}</dt>
                <dd className="mono">{formatNumber(selected.durationMs)} ms</dd>
              </div>
              {selected.source === 'gate' ? (
                <>
                  <div>
                    <dt>{t('requests.detailBytesIn')}</dt>
                    <dd className="mono">{formatBytes(selected.bytesIn)}</dd>
                  </div>
                  <div>
                    <dt>{t('requests.detailBytesOut')}</dt>
                    <dd className="mono">{formatBytes(selected.bytesOut)}</dd>
                  </div>
                </>
              ) : (
                <div>
                  <dt>{t('requests.detailBytes')}</dt>
                  <dd className="mono">{formatBytes(selected.bytesIn + selected.bytesOut)}</dd>
                </div>
              )}
              <div>
                <dt>{t('requests.detailStarted')}</dt>
                <dd className="mono">{formatDateTimeSeconds(selected.startedAt)}</dd>
              </div>
              <div>
                <dt>{t('requests.detailSource')}</dt>
                <dd>{sourceLabel(selected.source)}</dd>
              </div>
              {selected.errorClass ? (
                <div>
                  <dt>{t('requests.detailErrorClass')}</dt>
                  <dd className="mono">{selected.errorClass}</dd>
                </div>
              ) : null}
            </dl>
            <p className="page-note">{t('requests.detailNote')}</p>
          </div>
        ) : null}
      </Drawer>

      <Drawer
        open={labOpen}
        title={t('requests.drawerTitle')}
        subtitle={t('requests.drawerSubtitle')}
        onClose={() => setLabOpen(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setLabOpen(false)} disabled={sending}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="mutation-form" variant="primary" loading={sending}>
              {t('requests.send')}
            </Button>
          </>
        }
      >
        <form id="mutation-form" className="form" onSubmit={submitLab}>
          <div className="form-grid">
            <Field label={t('requests.method')} htmlFor="mutation-method">
              <Select
                variant="form"
                id="mutation-method"
                value={lab.method}
                onChange={(value) => setLab({ ...lab, method: value })}
                options={METHODS.map((method) => ({ value: method, label: method }))}
              />
            </Field>
            <Field label={t('requests.node')} htmlFor="mutation-node">
              <Select
                variant="form"
                id="mutation-node"
                value={lab.nodeId}
                onChange={(value) => setLab({ ...lab, nodeId: value })}
                options={liveNodes.map((node) => ({
                  value: node.id,
                  label: `${node.label} · ${node.egressIp}`,
                }))}
              />
            </Field>
          </div>
          <Field label={t('requests.targetHostPort')} htmlFor="mutation-target" error={labError}>
            <Input
              id="mutation-target"
              value={lab.target}
              onChange={(event) => setLab({ ...lab, target: event.target.value })}
              placeholder="api.example.com:443"
              className="mono"
            />
          </Field>
          <Field label={t('requests.body')} htmlFor="mutation-body" hint={t('requests.bodyHint')}>
            <Textarea
              id="mutation-body"
              rows={6}
              value={lab.body}
              onChange={(event) => setLab({ ...lab, body: event.target.value })}
              className="mono"
            />
          </Field>
          <p className="callout callout--muted">{t('requests.note')}</p>
        </form>
      </Drawer>
    </div>
  )
}
