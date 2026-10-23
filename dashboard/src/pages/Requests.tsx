import { FlaskConical, RefreshCw, Search, Waypoints } from 'lucide-react'
import { useMemo, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '../lib/api'
import { formatBytes, formatDateTime, formatNumber } from '../lib/format'
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
      setLabError('Target is required')
      return
    }
    if (!lab.nodeId) {
      setLabError('Choose a live node')
      return
    }
    setSending(true)
    setLabError('')
    try {
      const entry = await api.createMutationRequest({ ...lab, target: lab.target.trim() })
      requests.reload()
      push(`Mutation lab request completed · ${entry.status}`, 'success')
      setLabOpen(false)
    } catch (cause) {
      setLabError(cause instanceof Error ? cause.message : 'Mutation request failed')
    } finally {
      setSending(false)
    }
  }

  const columns: Array<Column<RequestLog>> = [
    {
      key: 'time',
      label: 'Time',
      render: (request) => <span className="cell-mono">{formatDateTime(request.ts)}</span>,
    },
    {
      key: 'user',
      label: 'User',
      render: (request) => <span>{request.userName}</span>,
    },
    {
      key: 'node',
      label: 'Account / node',
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
      label: 'Method',
      render: (request) => <span className="cell-mono">{request.method}</span>,
    },
    {
      key: 'target',
      label: 'Target',
      render: (request) => <span className="cell-mono cell-truncate">{request.target}</span>,
    },
    {
      key: 'status',
      label: 'Status',
      render: (request) => <Badge tone={statusTone(request.status)}>{request.status}</Badge>,
    },
    {
      key: 'bytes',
      label: 'Bytes',
      align: 'right',
      render: (request) => <span className="cell-mono">{formatBytes(request.bytes)}</span>,
    },
    {
      key: 'latency',
      label: 'Latency',
      align: 'right',
      render: (request) => (
        <span className="cell-mono">{formatNumber(request.latencyMs)} ms</span>
      ),
    },
    {
      key: 'source',
      label: 'Source',
      render: (request) =>
        request.source === 'mutation-lab' ? (
          <Badge tone="mint">mutation lab</Badge>
        ) : (
          <Badge tone="neutral">proxy</Badge>
        ),
    },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title="Requests"
        subtitle="Per-request telemetry from the gate and the mutation lab."
        refresh={summarizeRefresh([requests])}
        actions={
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={requests.reload}>
            Refresh
          </Button>
        }
      />

      <Card className="lab-card">
        <div className="lab-card__inner">
          <span className="lab-card__icon">
            <FlaskConical size={20} aria-hidden="true" />
          </span>
          <div className="lab-card__text">
            <p className="lab-card__title">Mutation lab</p>
            <p className="lab-card__desc">
              Send a controlled request through a live node to observe status, byte count and
              latency. Entries land at the top of the table with the <code className="mono">mutation lab</code>{' '}
              source tag.
            </p>
          </div>
          <Button variant="primary" icon={<Waypoints size={14} />} onClick={openLab} disabled={liveNodes.length === 0}>
            Open mutation lab
          </Button>
        </div>
      </Card>

      <Card bodyClassName="card__body--flush">
        <div className="toolbar">
          <div className="toolbar__search">
            <Search size={14} aria-hidden="true" />
            <Input
              aria-label="Search requests"
              placeholder="Search target, user, account or node"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <Select
            aria-label="Filter by status class"
            value={statusFilter}
            onChange={(event) => setStatusFilter(event.target.value)}
          >
            <option value="all">All statuses</option>
            <option value="2xx">2xx</option>
            <option value="3xx">3xx</option>
            <option value="4xx">4xx</option>
            <option value="5xx">5xx</option>
          </Select>
          <Select
            aria-label="Filter by method"
            value={methodFilter}
            onChange={(event) => setMethodFilter(event.target.value)}
          >
            <option value="all">All methods</option>
            {METHODS.map((method) => (
              <option key={method} value={method}>
                {method}
              </option>
            ))}
          </Select>
          <Select
            aria-label="Filter by source"
            value={sourceFilter}
            onChange={(event) => setSourceFilter(event.target.value)}
          >
            <option value="all">All sources</option>
            <option value="proxy">Proxy</option>
            <option value="mutation-lab">Mutation lab</option>
          </Select>
          <span className="toolbar__count">
            {filtered.length} of {requests.data?.length ?? 0}
          </span>
        </div>

        {requests.status === 'error' ? (
          <ErrorState message={requests.error ?? 'Unknown error'} onRetry={requests.reload} />
        ) : requests.status === 'loading' && !requests.data ? (
          <TableSkeleton rows={9} columns={8} />
        ) : (
          <DataTable
            caption="Proxy request log"
            columns={columns}
            rows={filtered}
            rowKey={(request) => request.id}
            empty={
              <EmptyState
                title="No requests match these filters"
                description="Clear the filters or send a request from the mutation lab."
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
                    Clear filters
                  </Button>
                }
              />
            }
          />
        )}
      </Card>

      <Drawer
        open={labOpen}
        title="Mutation lab"
        subtitle="Simulated execution — no vendor call leaves this machine."
        onClose={() => setLabOpen(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setLabOpen(false)} disabled={sending}>
              Cancel
            </Button>
            <Button type="submit" form="mutation-form" variant="primary" loading={sending}>
              Send request
            </Button>
          </>
        }
      >
        <form id="mutation-form" className="form" onSubmit={submitLab}>
          <div className="form-grid">
            <Field label="Method" htmlFor="mutation-method">
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
            <Field label="Node" htmlFor="mutation-node">
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
          <Field label="Target URL" htmlFor="mutation-target" error={labError}>
            <Input
              id="mutation-target"
              value={lab.target}
              onChange={(event) => setLab({ ...lab, target: event.target.value })}
              placeholder="https://api.example.com/v1/status"
              className="mono"
            />
          </Field>
          <Field label="Body" htmlFor="mutation-body" hint="Sent as JSON when the method allows a body.">
            <Textarea
              id="mutation-body"
              rows={6}
              value={lab.body}
              onChange={(event) => setLab({ ...lab, body: event.target.value })}
              className="mono"
            />
          </Field>
          <p className="callout callout--muted">
            The request is recorded against the chosen node and answered with{' '}
            <code className="mono">200</code>. The target is never contacted.
          </p>
        </form>
      </Drawer>
    </div>
  )
}
