import { FlaskConical, RefreshCw, Search, Waypoints } from 'lucide-react'
import { useMemo, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '../lib/api'
import { formatBytes, formatDateTime, formatNumber } from '../lib/format'
import { useI18n } from '../lib/i18n'
import { statusTone } from '../lib/labels'
import type { MutationLabInput, RequestLog } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { Drawer } from '../components/Overlay'
import { useToast } from '../components/Toast'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  Input,
  PageHeader,
  Select,
  TableSkeleton,
  Textarea,
} from '../components/primitives'

const METHODS = ['GET', 'POST', 'PUT', 'DELETE', 'CONNECT']

export default function Requests() {
  const requests = useAsync(() => api.listRequests(), [], { intervalMs: 10_000 })
  const nodes = useAsync(() => api.listNodes(), [])
  const { push } = useToast()
  const { t } = useI18n()
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState('all')
  const [methodFilter, setMethodFilter] = useState('all')
  const [sourceFilter, setSourceFilter] = useState('all')
  const [labOpen, setLabOpen] = useState(false)
  const [lab, setLab] = useState<MutationLabInput>({
    method: 'GET',
    target: 'https://api.example.com/v1/status',
    nodeId: '',
    body: '{}',
  })
  const [labError, setLabError] = useState('')
  const [sending, setSending] = useState(false)

  const liveNodes = (nodes.data ?? []).filter((node) => node.state === 'LIVE')

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return (requests.data ?? []).filter((request) => {
      if (statusFilter !== 'all' && `${Math.floor(request.status / 100)}xx` !== statusFilter) return false
      if (methodFilter !== 'all' && request.method !== methodFilter) return false
      if (sourceFilter !== 'all' && request.source !== sourceFilter) return false
      if (!needle) return true
      return `${request.target} ${request.userName} ${request.accountLabel} ${request.nodeLabel}`
        .toLowerCase()
        .includes(needle)
    })
  }, [methodFilter, query, requests.data, sourceFilter, statusFilter])

  const openLab = () => {
    setLab({
      method: 'GET',
      target: 'https://api.example.com/v1/status',
      nodeId: liveNodes[0]?.id ?? '',
      body: '{}',
    })
    setLabError('')
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
      requests.reload()
      push(t('requests.toastCompleted', { status: entry.status }), 'success')
      setLabOpen(false)
    } catch (cause) {
      setLabError(cause instanceof Error ? cause.message : t('requests.failed'))
    } finally {
      setSending(false)
    }
  }

  const columns: Array<Column<RequestLog>> = [
    {
      key: 'time',
      label: t('requests.columnTime'),
      render: (request) => <span className="cell-mono">{formatDateTime(request.ts)}</span>,
    },
    {
      key: 'user',
      label: t('requests.columnUser'),
      render: (request) => <span>{request.userName}</span>,
    },
    {
      key: 'node',
      label: t('requests.columnNode'),
      render: (request) => (
        <div className="cell-stack">
          <span>{request.accountLabel}</span>
          <span className="cell-sub mono">
            {request.nodeLabel} · {request.region}
          </span>
        </div>
      ),
    },
    {
      key: 'method',
      label: t('requests.columnMethod'),
      render: (request) => <span className="cell-mono">{request.method}</span>,
    },
    {
      key: 'target',
      label: t('requests.columnTarget'),
      render: (request) => <span className="cell-mono cell-truncate">{request.target}</span>,
    },
    {
      key: 'status',
      label: t('requests.columnStatus'),
      render: (request) => <Badge tone={statusTone(request.status)}>{request.status}</Badge>,
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
      render: (request) => (
        <span className="cell-mono">{formatNumber(request.latencyMs)} ms</span>
      ),
    },
    {
      key: 'source',
      label: t('requests.columnSource'),
      render: (request) =>
        request.source === 'mutation-lab' ? (
          <Badge tone="mint">{t('requests.sourceMutation')}</Badge>
        ) : (
          <Badge tone="warning">{t('requests.sourceSeeded')}</Badge>
        ),
    },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('requests.title')}
        subtitle={t('requests.subtitle')}
        refresh={summarizeRefresh([requests])}
        actions={
          <>
            <Badge tone="warning">{t('requests.simulated')}</Badge>
            <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={requests.reload}>
              {t('common.refresh')}
            </Button>
          </>
        }
      />

      <div className="callout callout--warning" role="note">
        <span className="callout__icon">
          <FlaskConical size={18} aria-hidden="true" />
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
          <Button variant="primary" icon={<Waypoints size={14} />} onClick={openLab} disabled={liveNodes.length === 0}>
            {t('requests.openLab')}
          </Button>
        </div>
      </Card>

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
            aria-label={t('requests.filterStatus')}
            value={statusFilter}
            onChange={(event) => setStatusFilter(event.target.value)}
          >
            <option value="all">{t('requests.allStatuses')}</option>
            <option value="2xx">2xx</option>
            <option value="3xx">3xx</option>
            <option value="4xx">4xx</option>
            <option value="5xx">5xx</option>
          </Select>
          <Select
            aria-label={t('requests.filterMethod')}
            value={methodFilter}
            onChange={(event) => setMethodFilter(event.target.value)}
          >
            <option value="all">{t('requests.allMethods')}</option>
            {METHODS.map((method) => (
              <option key={method} value={method}>
                {method}
              </option>
            ))}
          </Select>
          <Select
            aria-label={t('requests.filterSource')}
            value={sourceFilter}
            onChange={(event) => setSourceFilter(event.target.value)}
          >
            <option value="all">{t('requests.allSources')}</option>
            <option value="proxy">{t('requests.seeded')}</option>
            <option value="mutation-lab">{t('requests.mutationLab')}</option>
          </Select>
          <span className="toolbar__count">
            {t('common.of', { shown: filtered.length, total: requests.data?.length ?? 0 })}
          </span>
        </div>

        {requests.status === 'error' ? (
          <ErrorState message={requests.error ?? t('common.unknownError')} onRetry={requests.reload} />
        ) : requests.status === 'loading' && !requests.data ? (
          <TableSkeleton rows={9} columns={8} />
        ) : (
          <DataTable
            caption={t('requests.caption')}
            columns={columns}
            rows={filtered}
            rowKey={(request) => request.id}
            empty={
              <EmptyState
                title={t('requests.emptyTitle')}
                description={t('requests.emptyDesc')}
                action={
                  <Button
                    variant="secondary"
                    onClick={() => {
                      setQuery('')
                      setStatusFilter('all')
                      setMethodFilter('all')
                      setSourceFilter('all')
                    }}
                  >
                    {t('common.clearFilters')}
                  </Button>
                }
              />
            }
          />
        )}
      </Card>

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
                id="mutation-method"
                value={lab.method}
                onChange={(event) => setLab({ ...lab, method: event.target.value })}
              >
                {METHODS.map((method) => (
                  <option key={method} value={method}>
                    {method}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label={t('requests.node')} htmlFor="mutation-node">
              <Select
                id="mutation-node"
                value={lab.nodeId}
                onChange={(event) => setLab({ ...lab, nodeId: event.target.value })}
              >
                {liveNodes.map((node) => (
                  <option key={node.id} value={node.id}>
                    {node.label} · {node.egressIp}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          <Field label={t('requests.targetUrl')} htmlFor="mutation-target" error={labError}>
            <Input
              id="mutation-target"
              value={lab.target}
              onChange={(event) => setLab({ ...lab, target: event.target.value })}
              placeholder="https://api.example.com/v1/status"
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
