import { Power, RefreshCw, Search, Server, ShieldCheck } from 'lucide-react'
import { useMemo, useState } from 'react'
import { api } from '../lib/api'
import { formatDateTime, formatRelative, percent } from '../lib/format'
import { NODE_STATE } from '../lib/labels'
import { regionName } from '../lib/mock'
import type { NodeState, ProxyNode } from '../lib/types'
import { useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { Drawer } from '../components/Overlay'
import { useToast } from '../components/Toast'
import { CopyButton } from '../components/CopyButton'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Input,
  PageHeader,
  ProgressBar,
  Select,
  Sparkline,
  StatusDot,
  TableSkeleton,
} from '../components/primitives'

function latencyHistory(latency: number): number[] {
  return Array.from({ length: 14 }, (_, index) => {
    const wave = Math.sin(index / 2.4) * 0.18 + Math.sin(index / 5.1) * 0.1
    return Math.max(8, Math.round(latency * (1 + wave)))
  })
}

export default function Nodes() {
  const nodes = useAsync(() => api.listNodes(), [])
  const { push } = useToast()
  const [query, setQuery] = useState('')
  const [regionFilter, setRegionFilter] = useState('all')
  const [stateFilter, setStateFilter] = useState<'all' | NodeState>('all')
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const regions = useMemo(
    () => Array.from(new Set((nodes.data ?? []).map((node) => node.region))).sort(),
    [nodes.data],
  )

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return (nodes.data ?? []).filter((node) => {
      if (regionFilter !== 'all' && node.region !== regionFilter) return false
      if (stateFilter !== 'all' && node.state !== stateFilter) return false
      if (!needle) return true
      return `${node.label} ${node.accountLabel} ${node.city} ${node.egressIp}`.toLowerCase().includes(needle)
    })
  }, [nodes.data, query, regionFilter, stateFilter])

  const selected = nodes.data?.find((node) => node.id === selectedId) ?? null

  const toggleNode = async (node: ProxyNode) => {
    const next: NodeState = node.state === 'LIVE' ? 'DRAINING' : 'LIVE'
    try {
      await api.setNodeState(node.id, next)
      nodes.reload()
      push(`${node.label} ${next === 'DRAINING' ? 'draining' : 'live'}`, 'success')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Could not update node', 'danger')
    }
  }

  const probe = async (node: ProxyNode) => {
    try {
      const updated = await api.probeNode(node.id)
      nodes.reload()
      push(`Probe ok · ${node.label} · ${updated.latencyMs} ms`, 'success')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Probe failed', 'danger')
    }
  }

  const columns: Array<Column<ProxyNode>> = [
    {
      key: 'node',
      label: 'Node',
      render: (node) => (
        <div className="cell-stack">
          <span className="cell-strong mono">{node.label}</span>
          <span className="cell-sub">{node.accountLabel}</span>
        </div>
      ),
    },
    {
      key: 'region',
      label: 'Region',
      render: (node) => (
        <div className="cell-stack">
          <span>{regionName(node.region)}</span>
          <span className="cell-sub mono">{node.region}</span>
        </div>
      ),
    },
    {
      key: 'state',
      label: 'Status',
      render: (node) => {
        const state = NODE_STATE[node.state]
        return (
          <span className="state-cell">
            <StatusDot tone={state.tone} pulse={node.state === 'LIVE'} />
            <Badge tone={state.tone}>{state.label}</Badge>
          </span>
        )
      },
    },
    {
      key: 'latency',
      label: 'Latency',
      align: 'right',
      render: (node) => (
        <span className="latency-cell">
          <span className="cell-mono">{node.latencyMs > 0 ? `${node.latencyMs} ms` : '—'}</span>
          {node.latencyMs > 0 ? (
            <span className="latency-cell__spark">
              <Sparkline data={latencyHistory(Math.max(node.latencyMs, 30))} width={64} height={20} />
            </span>
          ) : null}
        </span>
      ),
    },
    {
      key: 'success',
      label: 'Success',
      render: (node) => (
        <span className="meter-cell">
          <span className="cell-mono">{node.successRate.toFixed(1)}%</span>
          <ProgressBar
            value={node.successRate}
            tone={node.successRate >= 98 ? 'success' : node.successRate >= 95 ? 'warning' : 'danger'}
            label={`${node.label} success rate`}
          />
        </span>
      ),
    },
    {
      key: 'capacity',
      label: 'Capacity',
      render: (node) => (
        <span className="meter-cell">
          <span className="cell-mono">
            {node.inUse}/{node.capacity}
          </span>
          <ProgressBar
            value={percent(node.inUse, node.capacity)}
            tone={node.inUse / node.capacity > 0.8 ? 'warning' : 'mint'}
            label={`${node.label} capacity`}
          />
        </span>
      ),
    },
    {
      key: 'egress',
      label: 'Egress IP',
      render: (node) => (
        <span className="inline-copy">
          <span className="cell-mono">{node.egressIp}</span>
          <CopyButton value={node.egressIp} label={`Copy egress IP for ${node.label}`} toastMessage="Egress IP copied" />
        </span>
      ),
    },
    {
      key: 'toggle',
      label: '',
      align: 'right',
      width: '112px',
      render: (node) => (
        <span onClick={(event) => event.stopPropagation()}>
          <Button
            size="sm"
            variant={node.state === 'LIVE' ? 'ghost' : 'secondary'}
            icon={<Power size={13} />}
            disabled={node.state === 'DOWN' || node.state === 'STARTING'}
            onClick={() => void toggleNode(node)}
          >
            {node.state === 'LIVE' ? 'Drain' : node.state === 'DRAINING' ? 'Undrain' : 'Activate'}
          </Button>
        </span>
      ),
    },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title="Nodes"
        subtitle="One gate per (account, server). Drain before rotating a vendor session."
        actions={
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={nodes.reload}>
            Refresh
          </Button>
        }
      />

      <Card bodyClassName="card__body--flush">
        <div className="toolbar">
          <div className="toolbar__search">
            <Search size={14} aria-hidden="true" />
            <Input
              aria-label="Search nodes"
              placeholder="Search node, account, city or IP"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <Select aria-label="Filter by region" value={regionFilter} onChange={(event) => setRegionFilter(event.target.value)}>
            <option value="all">All regions</option>
            {regions.map((region) => (
              <option key={region} value={region}>
                {regionName(region)}
              </option>
            ))}
          </Select>
          <Select
            aria-label="Filter by node state"
            value={stateFilter}
            onChange={(event) => setStateFilter(event.target.value as 'all' | NodeState)}
          >
            <option value="all">All statuses</option>
            <option value="LIVE">Live</option>
            <option value="READY">Ready</option>
            <option value="DRAINING">Draining</option>
            <option value="DOWN">Down</option>
          </Select>
          <span className="toolbar__count">
            {filtered.length} of {nodes.data?.length ?? 0}
          </span>
        </div>

        {nodes.status === 'error' ? (
          <ErrorState message={nodes.error ?? 'Unknown error'} onRetry={nodes.reload} />
        ) : nodes.status === 'loading' && !nodes.data ? (
          <TableSkeleton rows={8} columns={7} />
        ) : (
          <DataTable
            caption="Proxy nodes"
            columns={columns}
            rows={filtered}
            rowKey={(node) => node.id}
            onRowClick={(node) => setSelectedId(node.id)}
            empty={
              <EmptyState
                title="No nodes match these filters"
                description="Change the region or status filter, or clear the search."
                action={
                  <Button
                    variant="secondary"
                    onClick={() => {
                      setQuery('')
                      setRegionFilter('all')
                      setStateFilter('all')
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
        open={selected !== null}
        title={selected ? `Node ${selected.label}` : 'Node'}
        subtitle={selected ? `${selected.accountLabel} · ${regionName(selected.region)}` : undefined}
        onClose={() => setSelectedId(null)}
        footer={
          selected ? (
            <>
              <Button variant="ghost" onClick={() => setSelectedId(null)}>
                Close
              </Button>
              <Button
                variant="secondary"
                icon={<ShieldCheck size={14} />}
                onClick={() => void probe(selected)}
              >
                Probe now
              </Button>
            </>
          ) : null
        }
      >
        {selected ? (
          <div className="stack">
            <div className="drawer-hero">
              <span className="drawer-hero__icon">
                <Server size={20} />
              </span>
              <div>
                <p className="drawer-hero__title mono">{selected.label}</p>
                <p className="drawer-hero__meta">
                  {selected.city}, {selected.country} · gate :{selected.gatePort}
                </p>
              </div>
              <Badge tone={NODE_STATE[selected.state].tone}>{NODE_STATE[selected.state].label}</Badge>
            </div>

            <div className="spark-panel">
              <div className="spark-panel__head">
                <span className="label">Latency · last 14 probes</span>
                <span className="mono">{selected.latencyMs} ms</span>
              </div>
              <Sparkline data={latencyHistory(Math.max(selected.latencyMs, 30))} width={360} height={56} />
            </div>

            <dl className="facts">
              <div>
                <dt>Region</dt>
                <dd className="mono">{selected.region}</dd>
              </div>
              <div>
                <dt>Egress IP</dt>
                <dd className="inline-copy">
                  <span className="mono">{selected.egressIp}</span>
                  <CopyButton
                    value={selected.egressIp}
                    label={`Copy egress IP for ${selected.label}`}
                    toastMessage="Egress IP copied"
                  />
                </dd>
              </div>
              <div>
                <dt>Protocol</dt>
                <dd className="mono">{selected.protocol}</dd>
              </div>
              <div>
                <dt>Success rate</dt>
                <dd className="mono">{selected.successRate.toFixed(1)}%</dd>
              </div>
              <div>
                <dt>Capacity</dt>
                <dd className="mono">
                  {selected.inUse}/{selected.capacity} flows
                </dd>
              </div>
              <div>
                <dt>Last probe</dt>
                <dd title={formatDateTime(selected.lastProbeAt)}>{formatRelative(selected.lastProbeAt)}</dd>
              </div>
            </dl>

            <div className="drawer-actions">
              <Button
                variant="secondary"
                icon={<Power size={14} />}
                disabled={selected.state === 'DOWN'}
                onClick={() => void toggleNode(selected)}
              >
                {selected.state === 'LIVE' ? 'Drain node' : selected.state === 'DRAINING' ? 'Undrain node' : 'Activate node'}
              </Button>
            </div>

            <p className="callout callout--muted">
              Node tags are rendered as <code className="mono">ajs-live/{selected.region}</code> in the local
              subscription. Draining removes the line only after in-flight flows finish.
            </p>
          </div>
        ) : null}
      </Drawer>
    </div>
  )
}
