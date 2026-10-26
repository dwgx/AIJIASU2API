import {
  ArrowDownUp,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  RefreshCw,
  Search,
  Timer,
  Waypoints,
  XCircle,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../lib/api'
import { scopeDenial } from '../lib/errors'
import { formatBytes, formatDateTime, formatDateTimeSeconds, formatNumber, percent } from '../lib/format'
import { useI18n } from '../lib/i18n'
import type { ProxyProtocol, RequestLog, RequestSource } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { Drawer } from '../components/Overlay'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  Input,
  MetricTile,
  PageHeader,
  Select,
  Skeleton,
  StatusPill,
  TableSkeleton,
} from '../components/primitives'

const PROTOCOLS: ProxyProtocol[] = ['socks5', 'http']
const PAGE_SIZES = [25, 50, 100, 200] as const
const DEFAULT_PAGE_SIZE = 50

type StatusClass = 'all' | 'ok' | 'rejected'
type SourceFilter = 'all' | RequestSource

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

/**
 * Splits a stored target into host and port without keeping the path, query
 * or credentials the request may have carried. A target is never a URL here.
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

/** Host and port only: a target is never rendered as a URL. */
function formatTarget(request: RequestLog): string {
  const { host, port } = splitTarget(request.target)
  if (!host) return '—'
  const bracketed = host.startsWith('[') && host.endsWith(']')
  const rendered = host.includes(':') && !bracketed ? `[${host}]` : host
  return `${rendered}:${port}`
}

/** Same tone convention as the Leases table: socks5 is the info tone. */
function ProtocolBadge({ protocol }: { protocol: string }) {
  if (!protocol) return <span className="cell-sub">—</span>
  return <Badge tone={protocol === 'socks5' ? 'info' : 'neutral'}>{protocol}</Badge>
}

function statusTone(status: number): 'ok' | 'info' | 'warn' | 'error' {
  if (status < 300) return 'ok'
  if (status < 400) return 'info'
  if (status < 500) return 'warn'
  return 'error'
}

function p95(values: number[]): number {
  if (values.length === 0) return 0
  const sorted = [...values].sort((left, right) => left - right)
  const index = Math.min(sorted.length - 1, Math.ceil(sorted.length * 0.95) - 1)
  return sorted[index] ?? 0
}

function sourceLabel(source: RequestSource, t: (key: string) => string): string {
  switch (source) {
    case 'gate':
      return t('requests.sourceGate')
    case 'mutation-lab':
      return t('requests.sourceMutation')
    default:
      return t('requests.sourceProxy')
  }
}

/**
 * The durable request log (`GET /v1/requests?limit=&offset=`). It is a real
 * page of the daemon's stored rows, not a slice of the in-memory gate ring:
 * live flow telemetry stays on Lab → Flows. The daemon sends no total, so a
 * short page is the only end-of-log signal and the pager never invents one.
 */
export default function Requests() {
  const { t } = useI18n()
  const [pageSize, setPageSize] = useState<number>(DEFAULT_PAGE_SIZE)
  const [offset, setOffset] = useState(0)
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<StatusClass>('all')
  const [protocolFilter, setProtocolFilter] = useState<'all' | ProxyProtocol>('all')
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>('all')
  const [selected, setSelected] = useState<RequestLog | null>(null)

  const requests = useAsync(() => api.listRequests({ limit: pageSize, offset }), [pageSize, offset], {
    intervalMs: 30_000,
  })

  const rows = requests.data ?? []
  const page = Math.floor(offset / pageSize) + 1
  const hasPrev = offset > 0
  const hasNext = rows.length === pageSize

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return rows.filter((request) => {
      const ok = request.status < 400
      if (statusFilter !== 'all' && ok !== (statusFilter === 'ok')) return false
      if (protocolFilter !== 'all' && request.protocol !== protocolFilter) return false
      if (sourceFilter !== 'all' && request.source !== sourceFilter) return false
      if (!needle) return true
      const haystack = [
        request.id,
        request.userId,
        request.userName,
        request.accountLabel,
        request.nodeLabel,
        request.region,
        request.method,
        request.protocol ?? '',
        request.target,
        String(request.status),
        request.source,
      ]
        .join(' ')
        .toLowerCase()
      return haystack.includes(needle)
    })
  }, [protocolFilter, query, rows, sourceFilter, statusFilter])

  const summary = useMemo(() => {
    let accepted = 0
    let rejected = 0
    let bytes = 0
    const durations: number[] = []
    for (const request of filtered) {
      if (request.status < 400) accepted += 1
      else rejected += 1
      bytes += request.bytes
      durations.push(request.latencyMs)
    }
    return { total: filtered.length, accepted, rejected, bytes, p95: p95(durations) }
  }, [filtered])

  const filtersActive =
    query.trim() !== '' ||
    statusFilter !== 'all' ||
    protocolFilter !== 'all' ||
    sourceFilter !== 'all'

  const clearFilters = () => {
    setQuery('')
    setStatusFilter('all')
    setProtocolFilter('all')
    setSourceFilter('all')
  }

  const changePageSize = (value: string) => {
    setPageSize(Number(value))
    setOffset(0)
  }

  const columns: Array<Column<RequestLog>> = [
    {
      key: 'time',
      label: t('requests.columnTime'),
      width: '148px',
      render: (request) => <span className="cell-mono">{formatDateTime(request.ts)}</span>,
    },
    {
      key: 'node',
      label: t('requests.columnNode'),
      render: (request) => (
        <div className="cell-stack">
          <span>{request.accountLabel || t('common.unknown')}</span>
          <span className="cell-sub mono">{request.nodeLabel || t('common.unknown')}</span>
        </div>
      ),
    },
    {
      key: 'user',
      label: t('requests.columnUser'),
      render: (request) => {
        const label = request.userName || request.userId
        return label ? (
          <span className="cell-truncate" title={request.userId || undefined}>
            {label}
          </span>
        ) : (
          <span className="cell-sub">—</span>
        )
      },
    },
    {
      key: 'method',
      label: t('requests.columnMethod'),
      render: (request) => <span className="cell-mono">{request.method || '—'}</span>,
    },
    {
      key: 'protocol',
      label: t('requests.columnProtocol'),
      render: (request) => <ProtocolBadge protocol={request.protocol ?? ''} />,
    },
    {
      key: 'target',
      label: t('requests.columnTarget'),
      render: (request) => (
        <span className="cell-mono cell-truncate">{formatTarget(request)}</span>
      ),
    },
    {
      key: 'status',
      label: t('requests.columnStatus'),
      render: (request) => (
        <StatusPill status={statusTone(request.status)}>{String(request.status)}</StatusPill>
      ),
    },
    {
      key: 'bytes',
      label: t('requests.columnBytes'),
      align: 'right',
      render: (request) => <span className="cell-mono">{formatBytes(request.bytes)}</span>,
    },
    {
      key: 'latency',
      label: t('requests.columnLatency'),
      align: 'right',
      render: (request) => <span className="cell-mono">{formatNumber(request.latencyMs)} ms</span>,
    },
    {
      key: 'source',
      label: t('requests.columnSource'),
      render: (request) => (
        <Badge
          tone={
            request.source === 'gate'
              ? 'mint'
              : request.source === 'mutation-lab'
                ? 'warning'
                : 'neutral'
          }
        >
          {sourceLabel(request.source, t)}
        </Badge>
      ),
    },
    {
      key: 'detail',
      label: '',
      width: '44px',
      render: (request) => (
        <IconButton
          size="sm"
          label={t('requests.viewDetail', { id: request.id })}
          icon={<ChevronRight size={14} />}
          onClick={() => setSelected(request)}
        />
      ),
    },
  ]

  const loading = requests.status === 'loading' && !requests.data
  const failed = requests.status === 'error' && !requests.data

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('requests.title')}
        subtitle={t('requests.subtitle')}
        refresh={summarizeRefresh([requests])}
        actions={
          <>
            <Badge tone="info">{t('requests.logBadge')}</Badge>
            <Button
              variant="secondary"
              icon={<RefreshCw size={14} />}
              onClick={requests.reload}
            >
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
          <p className="callout__text">
            {t('requests.calloutText')}{' '}
            <Link className="callout__link" to="/lab">
              {t('requests.calloutLink')}
            </Link>
          </p>
        </div>
      </div>

      {loading ? (
        <SummarySkeleton />
      ) : failed ? null : (
        <div className="metric-grid metric-grid--5">
          <MetricTile
            label={t('requests.summaryTotal')}
            icon={<Waypoints size={14} />}
            value={formatNumber(summary.total)}
            hint={t('requests.summaryHintPage', {
              shown: summary.total,
              total: rows.length,
            })}
          />
          <MetricTile
            label={t('requests.summaryAccepted')}
            tone="success"
            icon={<CheckCircle2 size={14} />}
            value={formatNumber(summary.accepted)}
            hint={t('requests.summaryShare', {
              pct: Math.round(percent(summary.accepted, summary.total)),
            })}
          />
          <MetricTile
            label={t('requests.summaryRejected')}
            tone="danger"
            icon={<XCircle size={14} />}
            value={formatNumber(summary.rejected)}
            hint={t('requests.summaryShare', {
              pct: Math.round(percent(summary.rejected, summary.total)),
            })}
          />
          <MetricTile
            label={t('requests.summaryP95')}
            tone="info"
            icon={<Timer size={14} />}
            value={`${formatNumber(Math.round(summary.p95))} ms`}
            hint={t('requests.summaryP95Hint')}
          />
          <MetricTile
            label={t('requests.summaryBytes')}
            tone="neutral"
            icon={<ArrowDownUp size={14} />}
            value={formatBytes(summary.bytes)}
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
          <Select
            variant="filter"
            aria-label={t('requests.filterStatus')}
            value={statusFilter}
            onChange={(value) => setStatusFilter(value as StatusClass)}
            options={[
              { value: 'all', label: t('requests.allStatuses') },
              { value: 'ok', label: t('requests.statusAccepted') },
              { value: 'rejected', label: t('requests.statusRejected') },
            ]}
          />
          <Select
            variant="log"
            aria-label={t('requests.filterProtocol')}
            value={protocolFilter}
            onChange={(value) => setProtocolFilter(value as 'all' | ProxyProtocol)}
            options={[
              { value: 'all', label: t('requests.allProtocols') },
              ...PROTOCOLS.map((protocol) => ({ value: protocol, label: protocol })),
            ]}
          />
          <Select
            variant="filter"
            aria-label={t('requests.filterSource')}
            value={sourceFilter}
            onChange={(value) => setSourceFilter(value as SourceFilter)}
            options={[
              { value: 'all', label: t('requests.allSources') },
              { value: 'proxy', label: t('requests.sourceProxy') },
              { value: 'gate', label: t('requests.gateFlows') },
              { value: 'mutation-lab', label: t('requests.mutationLab') },
            ]}
          />
          <Button variant="ghost" size="sm" onClick={clearFilters} disabled={!filtersActive}>
            {t('common.clearFilters')}
          </Button>
          <Select
            variant="log"
            aria-label={t('requests.pageSize')}
            value={String(pageSize)}
            onChange={changePageSize}
            options={PAGE_SIZES.map((size) => ({ value: String(size), label: String(size) }))}
          />
        </div>
        <p className="page-note page-note--inset">{t('requests.pageScopeNote')}</p>

        {failed ? (
          <ErrorState
            message={
              scopeDenial(requests.failure, t, 'surface.requests', 'requests:read') ??
              requests.error ??
              t('common.unknownError')
            }
            onRetry={requests.reload}
          />
        ) : loading ? (
          <TableSkeleton rows={9} columns={11} />
        ) : (
          <>
            <DataTable
              caption={t('requests.caption')}
              columns={columns}
              rows={filtered}
              rowKey={(request) => request.id}
              onRowClick={(request) => setSelected(request)}
              empty={
                rows.length === 0 ? (
                  offset > 0 ? (
                    <EmptyState
                      title={t('requests.emptyPageTitle')}
                      description={t('requests.emptyPageDesc')}
                      icon={<Waypoints size={20} />}
                      action={
                        <Button
                          variant="secondary"
                          onClick={() => setOffset(Math.max(0, offset - pageSize))}
                        >
                          {t('requests.emptyPageAction')}
                        </Button>
                      }
                    />
                  ) : (
                    <EmptyState
                      title={t('requests.emptyTitle')}
                      description={t('requests.emptyDesc')}
                      icon={<Waypoints size={20} />}
                    />
                  )
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
            <nav className="pagination" aria-label={t('requests.pagination')}>
              <span className="pagination__range" aria-live="polite">
                {rows.length === 0
                  ? t('requests.pagerEmpty')
                  : t('requests.pagerRange', {
                      start: formatNumber(offset + 1),
                      end: formatNumber(offset + rows.length),
                    })}
              </span>
              <div className="pagination__controls">
                <IconButton
                  size="sm"
                  label={t('pagination.previous')}
                  icon={<ChevronLeft size={14} aria-hidden="true" />}
                  disabled={!hasPrev}
                  onClick={() => setOffset(Math.max(0, offset - pageSize))}
                />
                <span className="pagination__page">
                  {t('requests.pagerPage', { page: formatNumber(page) })}
                </span>
                <IconButton
                  size="sm"
                  label={t('pagination.next')}
                  icon={<ChevronRight size={14} aria-hidden="true" />}
                  disabled={!hasNext}
                  onClick={() => setOffset(offset + pageSize)}
                />
              </div>
            </nav>
          </>
        )}
      </Card>

      <Drawer
        open={selected !== null}
        title={t('requests.detailTitle')}
        subtitle={
          selected
            ? t('requests.detailSubtitle', { source: sourceLabel(selected.source, t) })
            : undefined
        }
        onClose={() => setSelected(null)}
        width={480}
      >
        {selected ? (
          <div className="stack">
            <div className="cell-stack">
              <StatusPill status={statusTone(selected.status)}>{String(selected.status)}</StatusPill>
              <span className="cell-sub mono">{formatDateTimeSeconds(selected.ts)}</span>
            </div>
            <dl className="facts">
              <div>
                <dt>{t('requests.detailRequestId')}</dt>
                <dd className="mono">{selected.id}</dd>
              </div>
              <div>
                <dt>{t('requests.detailAccount')}</dt>
                <dd>{selected.accountLabel || '—'}</dd>
              </div>
              <div>
                <dt>{t('requests.detailNode')}</dt>
                <dd>{selected.nodeLabel || '—'}</dd>
              </div>
              <div>
                <dt>{t('requests.detailRegion')}</dt>
                <dd className="mono">{selected.region || '—'}</dd>
              </div>
              <div>
                <dt>{t('requests.detailUser')}</dt>
                <dd>{selected.userName || selected.userId || '—'}</dd>
              </div>
              <div>
                <dt>{t('requests.detailMethod')}</dt>
                <dd className="mono">{selected.method || '—'}</dd>
              </div>
              <div>
                <dt>{t('requests.detailProtocol')}</dt>
                <dd>
                  <ProtocolBadge protocol={selected.protocol ?? ''} />
                </dd>
              </div>
              <div>
                <dt>{t('requests.detailTarget')}</dt>
                <dd className="mono">{formatTarget(selected)}</dd>
              </div>
              <div>
                <dt>{t('requests.detailDuration')}</dt>
                <dd className="mono">{formatNumber(selected.latencyMs)} ms</dd>
              </div>
              <div>
                <dt>{t('requests.detailBytes')}</dt>
                <dd className="mono">{formatBytes(selected.bytes)}</dd>
              </div>
              <div>
                <dt>{t('requests.detailStarted')}</dt>
                <dd className="mono">{formatDateTimeSeconds(selected.ts)}</dd>
              </div>
              <div>
                <dt>{t('requests.detailSource')}</dt>
                <dd>{sourceLabel(selected.source, t)}</dd>
              </div>
            </dl>
            <p className="page-note">{t('requests.detailNote')}</p>
          </div>
        ) : null}
      </Drawer>
    </div>
  )
}
