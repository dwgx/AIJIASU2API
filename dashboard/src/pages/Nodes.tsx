import { Power, RefreshCw, Search, Server, ShieldCheck } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../lib/api'
import { formatDateTime, formatRelative, percent } from '../lib/format'
import { useI18n } from '../lib/i18n'
import { NODE_STATE } from '../lib/labels'
import { regionName } from '../lib/mock'
import type { NodeState, ProxyNode } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
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

/**
 * Seeds the search box from the `q` param and follows later `q` changes (the command palette
 * can navigate here while the page is mounted) without clobbering a query the user typed on
 * re-renders that leave `q` alone.
 */
function useParamQuery(paramValue: string | null): [string, (value: string) => void] {
  const [query, setQuery] = useState(paramValue ?? '')
  const lastParamRef = useRef(paramValue)
  useEffect(() => {
    if (paramValue !== lastParamRef.current) {
      lastParamRef.current = paramValue
      setQuery(paramValue ?? '')
    }
  }, [paramValue])
  return [query, setQuery]
}

function latencyHistory(latency: number): number[] {
  return Array.from({ length: 14 }, (_, index) => {
    const wave = Math.sin(index / 2.4) * 0.18 + Math.sin(index / 5.1) * 0.1
    return Math.max(8, Math.round(latency * (1 + wave)))
  })
}

export default function Nodes() {
  const nodes = useAsync(() => api.listNodes(), [], { intervalMs: 15_000 })
  const [params] = useSearchParams()
  const { push } = useToast()
  const { lang, t } = useI18n()
  const [query, setQuery] = useParamQuery(params.get('q'))
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
      push(
        t(next === 'DRAINING' ? 'nodes.toastDraining' : 'nodes.toastLive', { name: node.label }),
        'success',
      )
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('nodes.toastUpdateFailed'), 'danger')
    }
  }

  const probe = async (node: ProxyNode) => {
    try {
      await api.probeNode(node.id)
      nodes.reload()
      push(t('nodes.toastProbeTouched', { name: node.label }), 'default')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('nodes.toastProbeFailed'), 'danger')
    }
  }

  const columns: Array<Column<ProxyNode>> = [
    {
      key: 'node',
      label: t('nodes.columnNode'),
      render: (node) => (
        <div className="cell-stack">
          <span className="cell-strong mono">{node.label}</span>
          <span className="cell-sub">{node.accountLabel}</span>
        </div>
      ),
    },
    {
      key: 'region',
      label: t('nodes.columnRegion'),
      render: (node) => (
        <div className="cell-stack">
          <span>{regionName(node.region)}</span>
          <span className="cell-sub mono">{node.region}</span>
        </div>
      ),
    },
    {
      key: 'state',
      label: t('nodes.columnStatus'),
      render: (node) => {
        const state = NODE_STATE[node.state]
        return (
          <span className="state-cell">
            <StatusDot tone={state.tone} pulse={node.state === 'LIVE'} />
            <Badge tone={state.tone}>{t(state.labelKey)}</Badge>
          </span>
        )
      },
    },
    {
      key: 'latency',
      label: t('nodes.columnLatency'),
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
      label: t('nodes.columnSuccess'),
      render: (node) => (
        <span className="meter-cell">
          <span className="cell-mono">{node.successRate.toFixed(1)}%</span>
          <ProgressBar
            value={node.successRate}
            tone={node.successRate >= 98 ? 'success' : node.successRate >= 95 ? 'warning' : 'danger'}
            label={t('nodes.successRate', { name: node.label })}
          />
        </span>
      ),
    },
    {
      key: 'capacity',
      label: t('nodes.columnCapacity'),
      render: (node) => (
        <span className="meter-cell">
          <span className="cell-mono">
            {node.inUse}/{node.capacity}
          </span>
          <ProgressBar
            value={percent(node.inUse, node.capacity)}
            tone={node.inUse / node.capacity > 0.8 ? 'warning' : 'mint'}
            label={t('nodes.capacity', { name: node.label })}
          />
        </span>
      ),
    },
    {
      key: 'egress',
      label: t('nodes.columnEgress'),
      render: (node) => (
        <span className="inline-copy">
          <span className="cell-mono">{node.egressIp}</span>
          <CopyButton
            value={node.egressIp}
            label={t('nodes.copyEgress', { name: node.label })}
            toastMessage={t('nodes.egressCopied')}
          />
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
            {node.state === 'LIVE'
              ? t('nodes.drain')
              : node.state === 'DRAINING'
                ? t('nodes.undrain')
                : t('nodes.activate')}
          </Button>
        </span>
      ),
    },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('nodes.title')}
        subtitle={t('nodes.subtitle')}
        refresh={summarizeRefresh([nodes])}
        actions={
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={nodes.reload}>
            {t('common.refresh')}
          </Button>
        }
      />

      <Card bodyClassName="card__body--flush">
        <div className="toolbar">
          <div className="toolbar__search">
            <Search size={14} aria-hidden="true" />
            <Input
              aria-label={t('nodes.searchAria')}
              placeholder={t('nodes.searchPlaceholder')}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <Select aria-label={t('nodes.filterRegion')} value={regionFilter} onChange={(event) => setRegionFilter(event.target.value)}>
            <option value="all">{t('nodes.allRegions')}</option>
            {regions.map((region) => (
              <option key={region} value={region}>
                {regionName(region)}
              </option>
            ))}
          </Select>
          <Select
            aria-label={t('nodes.filterState')}
            value={stateFilter}
            onChange={(event) => setStateFilter(event.target.value as 'all' | NodeState)}
          >
            <option value="all">{t('nodes.allStatuses')}</option>
            <option value="LIVE">{t('state.LIVE')}</option>
            <option value="READY">{t('state.READY')}</option>
            <option value="DRAINING">{t('state.DRAINING')}</option>
            <option value="DOWN">{t('state.DOWN')}</option>
          </Select>
          <span className="toolbar__count">
            {t('common.of', { shown: filtered.length, total: nodes.data?.length ?? 0 })}
          </span>
        </div>

        {nodes.status === 'error' ? (
          <ErrorState message={nodes.error ?? t('common.unknownError')} onRetry={nodes.reload} />
        ) : nodes.status === 'loading' && !nodes.data ? (
          <TableSkeleton rows={8} columns={7} />
        ) : (
          <DataTable
            caption={t('nodes.caption')}
            columns={columns}
            rows={filtered}
            rowKey={(node) => node.id}
            onRowClick={(node) => setSelectedId(node.id)}
            empty={
              <EmptyState
                title={t('nodes.emptyTitle')}
                description={t('nodes.emptyDesc')}
                action={
                  <Button
                    variant="secondary"
                    onClick={() => {
                      setQuery('')
                      setRegionFilter('all')
                      setStateFilter('all')
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
        open={selected !== null}
        title={selected ? t('nodes.drawerTitle', { name: selected.label }) : t('nodes.title')}
        subtitle={selected ? `${selected.accountLabel} · ${regionName(selected.region)}` : undefined}
        onClose={() => setSelectedId(null)}
        footer={
          selected ? (
            <>
              <Button variant="ghost" onClick={() => setSelectedId(null)}>
                {t('common.close')}
              </Button>
              <Button
                variant="secondary"
                icon={<ShieldCheck size={14} />}
                onClick={() => void probe(selected)}
              >
                {t('nodes.touchProbe')}
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
                  {selected.city}, {selected.country} · {t('nodes.gate', { port: selected.gatePort })}
                </p>
              </div>
              <Badge tone={NODE_STATE[selected.state].tone}>{t(NODE_STATE[selected.state].labelKey)}</Badge>
            </div>

            <div className="spark-panel">
              <div className="spark-panel__head">
                <span className="label">{t('nodes.latencyProbes')}</span>
                <span className="mono">{selected.latencyMs} ms</span>
              </div>
              <Sparkline data={latencyHistory(Math.max(selected.latencyMs, 30))} width={360} height={56} />
            </div>

            <dl className="facts">
              <div>
                <dt>{t('nodes.factsRegion')}</dt>
                <dd className="mono">{selected.region}</dd>
              </div>
              <div>
                <dt>{t('nodes.factsEgress')}</dt>
                <dd className="inline-copy">
                  <span className="mono">{selected.egressIp}</span>
                  <CopyButton
                    value={selected.egressIp}
                    label={t('nodes.copyEgress', { name: selected.label })}
                    toastMessage={t('nodes.egressCopied')}
                  />
                </dd>
              </div>
              <div>
                <dt>{t('nodes.factsProtocol')}</dt>
                <dd className="mono">{selected.protocol}</dd>
              </div>
              <div>
                <dt>{t('nodes.factsSuccess')}</dt>
                <dd className="mono">{selected.successRate.toFixed(1)}%</dd>
              </div>
              <div>
                <dt>{t('nodes.factsCapacity')}</dt>
                <dd className="mono">
                  {t('nodes.flows', { used: selected.inUse, capacity: selected.capacity })}
                </dd>
              </div>
              <div>
                <dt>{t('nodes.factsLastProbe')}</dt>
                <dd title={formatDateTime(selected.lastProbeAt)}>
                  {formatRelative(selected.lastProbeAt, Date.now(), lang)}
                </dd>
              </div>
            </dl>

            <div className="drawer-actions">
              <Button
                variant="secondary"
                icon={<Power size={14} />}
                disabled={selected.state === 'DOWN'}
                onClick={() => void toggleNode(selected)}
              >
                {selected.state === 'LIVE'
                  ? t('nodes.drainNode')
                  : selected.state === 'DRAINING'
                    ? t('nodes.undrainNode')
                    : t('nodes.activateNode')}
              </Button>
            </div>

            <p className="callout callout--warning" role="note">
              {t('nodes.probeNote')}
            </p>

            <p className="callout callout--muted">
              {t('nodes.tagNote', { region: selected.region })}
            </p>
          </div>
        ) : null}
      </Drawer>
    </div>
  )
}
