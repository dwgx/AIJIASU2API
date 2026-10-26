import {
  ChevronRight,
  FlaskConical,
  RefreshCw,
  Search,
  Waypoints,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '../../lib/api'
import { formatBytes, formatDateTime, formatDateTimeSeconds, formatNumber } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type { FlowView, MutationLabInput, ProxyNode, ProxyProtocol } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import { Drawer } from '../Overlay'
import { Pagination, usePagination } from '../Pagination'
import { useToast } from '../Toast'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  Input,
  Select,
  StatusPill,
  TableSkeleton,
  Textarea,
} from '../primitives'
import { DisabledCallout } from './shared'

const FLOW_LIMIT = 200
const METHODS = ['CONNECT', 'GET', 'POST', 'PUT', 'DELETE', 'HEAD', 'OPTIONS']
const PROTOCOLS: ProxyProtocol[] = ['socks5', 'http']

type StatusFilter = 'all' | 'ok' | 'rejected'

/** Host and port only: the gate never records a URL, path or credential. */
function formatFlowTarget(flow: Pick<FlowView, 'targetHost' | 'targetPort'>): string {
  if (!flow.targetHost) return '—'
  const bracketed = flow.targetHost.startsWith('[') && flow.targetHost.endsWith(']')
  const host =
    flow.targetHost.includes(':') && !bracketed ? `[${flow.targetHost}]` : flow.targetHost
  return `${host}:${flow.targetPort}`
}

function ProtocolBadge({ protocol }: { protocol: string }) {
  if (!protocol) return <span className="cell-sub">—</span>
  return <Badge tone={protocol === 'socks5' ? 'info' : 'neutral'}>{protocol}</Badge>
}

/**
 * Live gate-flow diagnostics and the mutation lab.
 *
 * The ring is bounded, in-memory telemetry that only exists when lab capture
 * is enabled; it is not the durable request log. Both live here so the
 * Requests page can page the stored log without depending on lab telemetry.
 */
export function FlowsTab() {
  const { t } = useI18n()
  const { push } = useToast()
  const flows = useAsync(() => api.getLabFlows(FLOW_LIMIT), [], { intervalMs: 5_000 })
  const nodes = useAsync(() => api.listNodes(), [], { intervalMs: 15_000 })

  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [protocolFilter, setProtocolFilter] = useState<'all' | ProxyProtocol>('all')
  const [selected, setSelected] = useState<FlowView | null>(null)
  const [labOpen, setLabOpen] = useState(false)
  const [sending, setSending] = useState(false)
  const [labError, setLabError] = useState('')
  const [lab, setLab] = useState<MutationLabInput>({
    method: 'GET',
    target: 'api.example.com:443',
    nodeId: '',
    body: '{}',
  })

  const liveNodes = (nodes.data ?? []).filter((node) => node.state === 'LIVE')
  const nodeById = useMemo(
    () => new Map((nodes.data ?? []).map((node) => [node.id, node] as const)),
    [nodes.data],
  )

  const rows = flows.data?.flows ?? []
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return rows.filter((flow) => {
      if (statusFilter !== 'all' && flow.status !== statusFilter) return false
      if (protocolFilter !== 'all' && flow.protocol !== protocolFilter) return false
      if (!needle) return true
      const haystack = [
        flow.id,
        flow.accountId,
        flow.nodeId,
        flow.userId ?? '',
        flow.userName ?? '',
        flow.method,
        flow.protocol ?? '',
        flow.targetHost,
        String(flow.targetPort),
        flow.status,
        flow.errorClass ?? '',
      ]
        .join(' ')
        .toLowerCase()
      return haystack.includes(needle)
    })
  }, [protocolFilter, query, rows, statusFilter])

  const pagination = usePagination({
    total: filtered.length,
    initialPageSize: 50,
    resetKey: JSON.stringify([query, statusFilter, protocolFilter]),
  })

  const filtersActive = query.trim() !== '' || statusFilter !== 'all' || protocolFilter !== 'all'

  const clearFilters = () => {
    setQuery('')
    setStatusFilter('all')
    setProtocolFilter('all')
  }

  const openLab = () => {
    setLab({
      method: 'GET',
      target: 'api.example.com:443',
      nodeId: liveNodes[0]?.id ?? '',
      body: '{}',
    })
    setLabError('')
    setLabOpen(true)
  }

  const submitLab = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!lab.target.trim()) {
      setLabError(t('lab.flows.targetRequired'))
      return
    }
    if (!lab.nodeId) {
      setLabError(t('lab.flows.chooseLiveNode'))
      return
    }
    setSending(true)
    setLabError('')
    try {
      const entry = await api.createMutationRequest({ ...lab, target: lab.target.trim() })
      push(t('lab.flows.labToast', { status: entry.status }), 'success')
      setLabOpen(false)
    } catch (cause) {
      setLabError(cause instanceof Error ? cause.message : t('lab.flows.labFailed'))
    } finally {
      setSending(false)
    }
  }

  const columns: Array<Column<FlowView>> = [
    {
      key: 'time',
      label: t('requests.columnTime'),
      width: '148px',
      render: (flow) => <span className="cell-mono">{formatDateTime(flow.startedAt)}</span>,
    },
    {
      key: 'node',
      label: t('requests.columnNode'),
      render: (flow) => {
        const node: ProxyNode | undefined = nodeById.get(flow.nodeId)
        return (
          <div className="cell-stack">
            <span>{node?.accountLabel ?? flow.accountId}</span>
            <span className="cell-sub mono">{node?.label ?? flow.nodeId}</span>
          </div>
        )
      },
    },
    {
      key: 'user',
      label: t('requests.columnUser'),
      render: (flow) => {
        const label = flow.userName || flow.userId
        return label ? (
          <span className="cell-truncate" title={flow.userId || undefined}>
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
      render: (flow) => <span className="cell-mono">{flow.method || '—'}</span>,
    },
    {
      key: 'protocol',
      label: t('requests.columnProtocol'),
      render: (flow) => <ProtocolBadge protocol={flow.protocol ?? ''} />,
    },
    {
      key: 'target',
      label: t('requests.columnTarget'),
      render: (flow) => (
        <span className="cell-mono cell-truncate">{formatFlowTarget(flow)}</span>
      ),
    },
    {
      key: 'status',
      label: t('requests.columnStatus'),
      render: (flow) => (
        <StatusPill status={flow.status === 'ok' ? 'ok' : 'error'}>{flow.status}</StatusPill>
      ),
    },
    {
      key: 'bytes',
      label: t('requests.columnBytes'),
      align: 'right',
      render: (flow) => (
        <span className="cell-mono">{formatBytes(flow.bytesIn + flow.bytesOut)}</span>
      ),
    },
    {
      key: 'latency',
      label: t('requests.columnLatency'),
      align: 'right',
      render: (flow) => <span className="cell-mono">{formatNumber(flow.durationMs)} ms</span>,
    },
    {
      key: 'error',
      label: t('requests.columnError'),
      render: (flow) =>
        flow.errorClass ? (
          <span className="cell-mono cell-truncate">{flow.errorClass}</span>
        ) : (
          <span className="cell-sub">—</span>
        ),
    },
    {
      key: 'detail',
      label: '',
      width: '44px',
      render: (flow) => (
        <IconButton
          size="sm"
          label={t('lab.flows.viewDetail', { id: flow.id })}
          icon={<ChevronRight size={14} />}
          onClick={() => setSelected(flow)}
        />
      ),
    },
  ]

  const disabled = flows.data?.enabled === false

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">{t('lab.flows.title')}</h2>
          <p className="lab-tab-subtitle">{t('lab.flows.subtitle')}</p>
        </div>
        <div className="lab-tab-actions">
          <Button
            variant="secondary"
            icon={<RefreshCw size={14} />}
            onClick={() => {
              flows.reload()
              nodes.reload()
            }}
          >
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      <Card className="lab-card">
        <div className="lab-card__inner">
          <span className="lab-card__icon">
            <FlaskConical size={20} aria-hidden="true" />
          </span>
          <div className="lab-card__text">
            <p className="lab-card__title">{t('lab.flows.labTitle')}</p>
            <p className="lab-card__desc">{t('lab.flows.labDesc')}</p>
          </div>
          <Button
            variant="primary"
            icon={<Waypoints size={14} />}
            onClick={openLab}
            disabled={liveNodes.length === 0}
          >
            {t('lab.flows.openLab')}
          </Button>
        </div>
      </Card>

      <Card bodyClassName="card__body--flush">
        {disabled ? (
          <div className="card__body">
            <DisabledCallout
              title={t('lab.flows.disabledTitle')}
              reason={flows.data?.reason ?? t('common.unknown')}
            />
          </div>
        ) : flows.status === 'error' && !flows.data ? (
          <ErrorState
            message={flows.error ?? t('common.unknownError')}
            onRetry={flows.reload}
          />
        ) : flows.status === 'loading' && !flows.data ? (
          <TableSkeleton rows={9} columns={10} />
        ) : (
          <>
            <div className="toolbar">
              <div className="toolbar__search">
                <Search size={14} aria-hidden="true" />
                <Input
                  aria-label={t('lab.flows.searchAria')}
                  placeholder={t('lab.flows.searchPlaceholder')}
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </div>
              <Select
                variant="filter"
                aria-label={t('requests.filterStatus')}
                value={statusFilter}
                onChange={(value) => setStatusFilter(value as StatusFilter)}
                options={[
                  { value: 'all', label: t('requests.allStatuses') },
                  { value: 'ok', label: t('requests.statusOk') },
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
              <Button variant="ghost" size="sm" onClick={clearFilters} disabled={!filtersActive}>
                {t('common.clearFilters')}
              </Button>
              <span className="toolbar__count">
                {t('lab.flows.ringCount', {
                  shown: filtered.length,
                  total: rows.length,
                  dropped: flows.data?.dropped ?? 0,
                })}
              </span>
            </div>
            <DataTable
              caption={t('lab.flows.caption')}
              columns={columns}
              rows={pagination.slice(filtered)}
              rowKey={(flow) => flow.id}
              onRowClick={(flow) => setSelected(flow)}
              empty={
                rows.length === 0 ? (
                  <EmptyState
                    title={t('lab.flows.emptyTitle')}
                    description={t('lab.flows.emptyDesc')}
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
              label={t('lab.flows.pagination')}
            />
          </>
        )}
      </Card>

      <Drawer
        open={selected !== null}
        title={t('lab.flows.detailTitle')}
        subtitle={t('lab.flows.detailSubtitle')}
        onClose={() => setSelected(null)}
        width={480}
      >
        {selected ? (
          <div className="stack">
            <div className="cell-stack">
              <StatusPill status={selected.status === 'ok' ? 'ok' : 'error'}>
                {selected.status}
              </StatusPill>
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
                <dd>{nodeById.get(selected.nodeId)?.accountLabel ?? selected.accountId}</dd>
              </div>
              <div>
                <dt>{t('requests.detailNode')}</dt>
                <dd>{nodeById.get(selected.nodeId)?.label ?? selected.nodeId}</dd>
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
                <dd className="mono">{formatFlowTarget(selected)}</dd>
              </div>
              <div>
                <dt>{t('requests.detailDuration')}</dt>
                <dd className="mono">{formatNumber(selected.durationMs)} ms</dd>
              </div>
              <div>
                <dt>{t('requests.detailBytesIn')}</dt>
                <dd className="mono">{formatBytes(selected.bytesIn)}</dd>
              </div>
              <div>
                <dt>{t('requests.detailBytesOut')}</dt>
                <dd className="mono">{formatBytes(selected.bytesOut)}</dd>
              </div>
              <div>
                <dt>{t('requests.detailStarted')}</dt>
                <dd className="mono">{formatDateTimeSeconds(selected.startedAt)}</dd>
              </div>
              {selected.errorClass ? (
                <div>
                  <dt>{t('requests.detailErrorClass')}</dt>
                  <dd className="mono">{selected.errorClass}</dd>
                </div>
              ) : null}
            </dl>
            <p className="page-note">{t('lab.flows.detailNote')}</p>
          </div>
        ) : null}
      </Drawer>

      <Drawer
        open={labOpen}
        title={t('lab.flows.drawerTitle')}
        subtitle={t('lab.flows.drawerSubtitle')}
        onClose={() => setLabOpen(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setLabOpen(false)} disabled={sending}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="mutation-form" variant="primary" loading={sending}>
              {t('lab.flows.send')}
            </Button>
          </>
        }
      >
        <form id="mutation-form" className="form" onSubmit={submitLab}>
          <div className="form-grid">
            <Field label={t('lab.flows.method')} htmlFor="mutation-method">
              <Select
                variant="form"
                id="mutation-method"
                value={lab.method}
                onChange={(value) => setLab({ ...lab, method: value })}
                options={METHODS.map((method) => ({ value: method, label: method }))}
              />
            </Field>
            <Field label={t('lab.flows.node')} htmlFor="mutation-node">
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
          <Field
            label={t('lab.flows.targetHostPort')}
            htmlFor="mutation-target"
            error={labError}
          >
            <Input
              id="mutation-target"
              value={lab.target}
              onChange={(event) => setLab({ ...lab, target: event.target.value })}
              placeholder="api.example.com:443"
              className="mono"
            />
          </Field>
          <Field
            label={t('lab.flows.body')}
            htmlFor="mutation-body"
            hint={t('lab.flows.bodyHint')}
          >
            <Textarea
              id="mutation-body"
              rows={6}
              value={lab.body}
              onChange={(event) => setLab({ ...lab, body: event.target.value })}
              className="mono"
            />
          </Field>
          <p className="callout callout--muted">{t('lab.flows.note')}</p>
        </form>
      </Drawer>
    </div>
  )
}
