import { Globe, Search, Server, ShieldCheck } from 'lucide-react'
import { useMemo, useState } from 'react'
import { api } from '../../lib/api'
import { formatDateTime, formatNumber, formatRelative, percent } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type { Language } from '../../lib/i18n'
import { NODE_STATE } from '../../lib/labels'
import { regionName } from '../../lib/regions'
import type { EgressCheckResult, NodeState, ProxyNode } from '../../lib/types'
import type { AsyncState } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import { Drawer } from '../Overlay'
import { useToast } from '../Toast'
import { CopyButton } from '../CopyButton'
import { MorphIcon } from '../icons/MorphIcon'
import { NODE_DRAINED_GLYPH, NODE_LIVE_GLYPH } from '../icons/morphGlyphs'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Input,
  ProgressBar,
  Select,
  Sparkline,
  StatusPill,
  TableSkeleton,
} from '../primitives'

/** A probe stamp further ahead than this is reported as clock skew, not "just now". */
const CLOCK_SKEW_TOLERANCE_MS = 5 * 60_000

function latencyHistory(latency: number): number[] {
  return Array.from({ length: 14 }, (_, index) => {
    const wave = Math.sin(index / 2.4) * 0.18 + Math.sin(index / 5.1) * 0.1
    return Math.max(8, Math.round(latency * (1 + wave)))
  })
}

/**
 * A success rate is a measurement only when a probe recorded a sample. The
 * daemon stores `successRate: 0` for a node it never measured, so a zero must
 * render as "not measured" and never as a measured 0.0%.
 */
function isSuccessMeasured(node: ProxyNode): boolean {
  return node.lastProbeAt.trim() !== '' && node.successRate > 0
}

/**
 * Localized region label for the drawer: the dictionary name is primary, an
 * unresolved vendor code falls back to the node's city/country, and the raw
 * code stays available as secondary mono text.
 */
function regionLabel(node: ProxyNode, lang: Language): { name: string; code: string } {
  const name = regionName(node.region, lang)
  const resolved = node.region.trim() !== '' && name !== node.region
  const fallback = [node.city, node.country]
    .map((part) => part.trim())
    .filter((part) => part !== '')
    .join(', ')
  return { name: resolved || !fallback ? name : fallback, code: node.region }
}

interface ProbeStamp {
  /** Relative label, or the absolute stamp once the timestamp is far ahead. */
  text: string
  /** True when the daemon timestamp is ahead of this browser by more than the tolerance. */
  skew: boolean
}

/**
 * A probe timestamp slightly ahead of the browser is clock skew, not a future
 * probe, so it reads "just now". A far-future stamp is shown absolutely and
 * flagged instead of silently rendering "in 6h".
 */
function probeStamp(
  value: string,
  now: number,
  lang: Language,
  t: (key: string) => string,
): ProbeStamp {
  const time = new Date(value).getTime()
  if (value.trim() === '' || Number.isNaN(time)) return { text: t('common.never'), skew: false }
  const aheadMs = time - now
  if (aheadMs > CLOCK_SKEW_TOLERANCE_MS) return { text: formatDateTime(value), skew: true }
  if (aheadMs > 0) return { text: t('nodes.justNow'), skew: false }
  return { text: formatRelative(value, now, lang), skew: false }
}

function EgressMatchBadge({ match }: { match: boolean | null }) {
  const { t } = useI18n()
  if (match === null) return <Badge tone="neutral">{t('lab.notObserved')}</Badge>
  return <Badge tone={match ? 'success' : 'danger'}>{t(match ? 'lab.match' : 'lab.mismatch')}</Badge>
}

interface ConnectedTabProps {
  nodes: AsyncState<ProxyNode[]>
  query: string
  onQueryChange: (value: string) => void
}

/**
 * Runtime nodes the daemon actually runs. The catalog decides what *could* be
 * served; this tab is what *is* served, with the drain/undrain/probe actions.
 */
export function ConnectedTab({ nodes, query, onQueryChange }: ConnectedTabProps) {
  const { push } = useToast()
  const { lang, t } = useI18n()
  const [regionFilter, setRegionFilter] = useState('all')
  const [stateFilter, setStateFilter] = useState<'all' | NodeState>('all')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  // A touch probe only moves lastProbeAt; the note below the drawer says so.
  const [touchedProbes, setTouchedProbes] = useState<string[]>([])
  // Egress checks are guarded observations: keep them keyed by node so a poll
  // or a switch of the drawer never erases a result the operator paid for.
  const [egressChecks, setEgressChecks] = useState<Record<string, EgressCheckResult>>({})
  const [egressErrors, setEgressErrors] = useState<Record<string, string>>({})
  const [checkingNodeId, setCheckingNodeId] = useState<string | null>(null)

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
      return `${node.label} ${node.accountLabel} ${node.city} ${node.egressIp} ${node.region} ${regionName(node.region, lang)}`
        .toLowerCase()
        .includes(needle)
    })
  }, [nodes.data, query, regionFilter, stateFilter, lang])

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
      setTouchedProbes((prev) => (prev.includes(node.id) ? prev : [...prev, node.id]))
      nodes.reload()
      push(t('nodes.toastProbeTouched', { name: node.label }), 'default')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('nodes.toastProbeFailed'), 'danger')
    }
  }

  /**
   * Guarded egress observation: the daemon dials the node's loopback gate and
   * reads the exit address. The result is session state, never written back to
   * the node, because the daemon's GET panel stays claim-only.
   */
  const verifyEgress = async (node: ProxyNode) => {
    if (node.state !== 'LIVE') return
    setCheckingNodeId(node.id)
    setEgressErrors((prev) => {
      if (!(node.id in prev)) return prev
      const next = { ...prev }
      delete next[node.id]
      return next
    })
    try {
      const result = await api.checkLabEgress(node.id)
      setEgressChecks((prev) => ({ ...prev, [node.id]: result }))
      // A mismatch is a successful observation with a bad answer; the inline
      // result carries it, so only a clean match earns the success toast.
      if (!result.error && result.match !== false) {
        push(t('nodes.toastEgressVerified', { name: node.label }), 'success')
      }
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : t('nodes.toastEgressFailed')
      setEgressErrors((prev) => ({ ...prev, [node.id]: message }))
      push(message, 'danger')
    } finally {
      setCheckingNodeId(null)
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
          <span>{regionName(node.region, lang)}</span>
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
            <StatusPill status={state.status} pulse={node.state === 'LIVE'}>
              {t(state.labelKey)}
            </StatusPill>
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
      render: (node) =>
        isSuccessMeasured(node) ? (
          <span className="meter-cell">
            <span className="cell-mono">{node.successRate.toFixed(1)}%</span>
            <ProgressBar
              value={node.successRate}
              tone={node.successRate >= 98 ? 'success' : node.successRate >= 95 ? 'warning' : 'danger'}
              label={t('nodes.successRate', { name: node.label })}
            />
          </span>
        ) : (
          <span className="cell-sub" title={t('nodes.notMeasuredHint')}>
            {t('nodes.notMeasured')}
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
            icon={
              <MorphIcon
                icon={node.state === 'DRAINING' ? NODE_DRAINED_GLYPH : NODE_LIVE_GLYPH}
                size={13}
                strokeWidth={1.8}
              />
            }
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

  // Drawer facts are derived once per render so the footer and the body agree.
  const selectedRegion = selected ? regionLabel(selected, lang) : null
  const lastProbe = selected ? probeStamp(selected.lastProbeAt, Date.now(), lang, t) : null
  const selectedCheck = selected ? egressChecks[selected.id] : undefined
  const selectedCheckError = selected ? egressErrors[selected.id] : undefined
  const selectedTouched = selected ? touchedProbes.includes(selected.id) : false

  return (
    <>
      <Card bodyClassName="card__body--flush">
        <div className="toolbar">
          <div className="toolbar__search">
            <Search size={14} aria-hidden="true" />
            <Input
              aria-label={t('nodes.connected.searchAria')}
              placeholder={t('nodes.searchPlaceholder')}
              value={query}
              onChange={(event) => onQueryChange(event.target.value)}
            />
          </div>
          <Select
            variant="filter"
            aria-label={t('nodes.filterRegion')}
            value={regionFilter}
            onChange={setRegionFilter}
            options={[
              { value: 'all', label: t('nodes.allRegions') },
              ...regions.map((region) => ({ value: region, label: regionName(region, lang) })),
            ]}
          />
          <Select
            variant="filter"
            aria-label={t('nodes.filterState')}
            value={stateFilter}
            onChange={(value) => setStateFilter(value as 'all' | NodeState)}
            options={[
              { value: 'all', label: t('nodes.allStatuses') },
              { value: 'LIVE', label: t('state.LIVE') },
              { value: 'READY', label: t('state.READY') },
              { value: 'DRAINING', label: t('state.DRAINING') },
              { value: 'DOWN', label: t('state.DOWN') },
            ]}
          />
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
                      onQueryChange('')
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
        subtitle={selected ? `${selected.accountLabel} · ${selectedRegion?.name ?? ''}` : undefined}
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
              <StatusPill status={NODE_STATE[selected.state].status}>
                {t(NODE_STATE[selected.state].labelKey)}
              </StatusPill>
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
                <dd className="cell-stack">
                  <span>{selectedRegion?.name}</span>
                  {selectedRegion?.code ? (
                    <span className="cell-sub mono">{selectedRegion.code}</span>
                  ) : null}
                </dd>
              </div>
              <div>
                <dt>{t('nodes.factsEgress')}</dt>
                <dd className="cell-stack">
                  <span className="inline-copy">
                    <span className="mono">{selected.egressIp}</span>
                    <CopyButton
                      value={selected.egressIp}
                      label={t('nodes.copyEgress', { name: selected.label })}
                      toastMessage={t('nodes.egressCopied')}
                    />
                  </span>
                  <span className="inline-copy">
                    <Badge tone="warning" title={t('nodes.egressClaimHint')}>
                      {t('nodes.egressVendorClaim')}
                    </Badge>
                    {selectedCheck?.verified ? (
                      <EgressMatchBadge match={selectedCheck.match} />
                    ) : (
                      <Badge tone="warning">{t('nodes.egressNotVerified')}</Badge>
                    )}
                  </span>
                </dd>
              </div>
              <div>
                <dt>{t('nodes.factsProtocol')}</dt>
                <dd className="mono">{selected.protocol}</dd>
              </div>
              <div>
                <dt>{t('nodes.factsSuccess')}</dt>
                <dd>
                  {isSuccessMeasured(selected) ? (
                    <span className="mono">{selected.successRate.toFixed(1)}%</span>
                  ) : (
                    <Badge tone="warning" title={t('nodes.notMeasuredHint')}>
                      {t('nodes.notMeasured')}
                    </Badge>
                  )}
                </dd>
              </div>
              <div>
                <dt>{t('nodes.factsCapacity')}</dt>
                <dd className="mono">
                  {t('nodes.flows', { used: selected.inUse, capacity: selected.capacity })}
                </dd>
              </div>
              <div>
                <dt>{t('nodes.factsLastProbe')}</dt>
                <dd
                  className="inline-copy"
                  title={selected.lastProbeAt ? formatDateTime(selected.lastProbeAt) : undefined}
                >
                  <span>{lastProbe?.text}</span>
                  {lastProbe?.skew ? (
                    <Badge tone="warning" title={t('nodes.clockSkewHint')}>
                      {t('nodes.clockSkew')}
                    </Badge>
                  ) : null}
                </dd>
              </div>
            </dl>

            <div className="drawer-actions">
              <Button
                variant="secondary"
                icon={
                  <MorphIcon
                    icon={selected.state === 'DRAINING' ? NODE_DRAINED_GLYPH : NODE_LIVE_GLYPH}
                    size={14}
                    strokeWidth={1.8}
                  />
                }
                disabled={selected.state === 'DOWN'}
                onClick={() => void toggleNode(selected)}
              >
                {selected.state === 'LIVE'
                  ? t('nodes.drainNode')
                  : selected.state === 'DRAINING'
                    ? t('nodes.undrainNode')
                    : t('nodes.activateNode')}
              </Button>
              <Button
                variant="secondary"
                icon={<Globe size={14} />}
                loading={checkingNodeId === selected.id}
                disabled={selected.state !== 'LIVE' || checkingNodeId !== null}
                title={
                  selected.state === 'LIVE'
                    ? t('lab.egress.checkTitleReady')
                    : t('nodes.verifyEgressDisabled')
                }
                onClick={() => void verifyEgress(selected)}
              >
                {t('nodes.verifyEgress')}
              </Button>
            </div>

            {selectedCheck || selectedCheckError ? (
              <div className="stack" role="region" aria-label={t('nodes.verifyEgressResult')}>
                {selectedCheck ? (
                  <dl className="facts">
                    <div>
                      <dt>{t('lab.egress.verified')}</dt>
                      <dd>
                        {selectedCheck.verified ? (
                          <span className="mono">{selectedCheck.verified}</span>
                        ) : (
                          <Badge tone="warning">{t('lab.notObserved')}</Badge>
                        )}
                      </dd>
                    </div>
                    <div>
                      <dt>{t('lab.egress.claimed')}</dt>
                      <dd className="mono">{selectedCheck.claimed || '—'}</dd>
                    </div>
                    <div>
                      <dt>{t('lab.egress.match')}</dt>
                      <dd>
                        <EgressMatchBadge match={selectedCheck.match} />
                      </dd>
                    </div>
                    <div>
                      <dt>{t('lab.egress.latency')}</dt>
                      <dd className="mono">{formatNumber(selectedCheck.latencyMs)} ms</dd>
                    </div>
                    <div>
                      <dt>{t('lab.egress.checked')}</dt>
                      <dd className="mono">{formatDateTime(selectedCheck.checkedAt)}</dd>
                    </div>
                  </dl>
                ) : null}
                {selectedCheck?.error ? (
                  <p className="callout callout--warning" role="alert">
                    {selectedCheck.error}
                  </p>
                ) : null}
                {selectedCheckError ? (
                  <p className="callout callout--warning" role="alert">
                    {selectedCheckError}
                  </p>
                ) : null}
              </div>
            ) : null}

            {selectedTouched ? (
              <p className="callout callout--muted" role="note">
                {t('nodes.touchOnlyNote')}
              </p>
            ) : null}

            <p className="callout callout--warning" role="note">
              {t('nodes.probeNote')}
            </p>

            <p className="callout callout--muted">
              {t('nodes.tagNote', { region: selected.region })}
            </p>
          </div>
        ) : null}
      </Drawer>
    </>
  )
}
