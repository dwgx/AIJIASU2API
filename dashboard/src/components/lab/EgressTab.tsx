import { AlertTriangle, Globe, Network, Radar, RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api } from '../../lib/api'
import { ApiError } from '../../lib/errors'
import { formatDateTime, formatNumber } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { nodeStateMeta } from '../../lib/lab'
import type { EgressCheckResult, EgressView } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Select,
  TableSkeleton,
} from '../primitives'
import { DisabledCallout, LabFacts } from './shared'

const CHECK_COOLDOWN_MS = 5_000

/**
 * A GET claim row with the local POST observation merged in. The daemon's GET
 * panel stays claim-only, so the check result lives in component state keyed
 * by node and is never overwritten by a poll or refresh.
 */
interface EgressRow extends EgressView {
  latencyMs?: number
  error?: string
}

function mergeEgressRows(
  rows: EgressView[],
  observations: Record<string, EgressCheckResult>,
): EgressRow[] {
  return rows.map((row) => {
    const observation = observations[row.nodeId]
    if (!observation) return row
    return {
      ...row,
      claimed: observation.claimed || row.claimed,
      verified: observation.verified,
      match: observation.match,
      source: observation.source,
      checkedAt: observation.checkedAt,
      latencyMs: observation.latencyMs,
      error: observation.error,
    }
  })
}

function MatchBadge({ match }: { match: boolean | null }) {
  const { t } = useI18n()
  if (match === null) return <Badge tone="neutral">{t('common.unknown')}</Badge>
  return <Badge tone={match ? 'success' : 'danger'}>{match ? t('lab.match') : t('lab.mismatch')}</Badge>
}

function EgressTable({ egress }: { egress: EgressRow[] }) {
  const { t } = useI18n()
  const columns: Array<Column<EgressRow>> = [
    { key: 'node', label: t('lab.sessions.node'), render: (row) => <span className="cell-mono">{row.nodeId}</span> },
    { key: 'claimed', label: t('lab.egress.claimed'), render: (row) => <span className="cell-mono">{row.claimed || '—'}</span> },
    {
      key: 'verified',
      label: t('lab.egress.verified'),
      render: (row) =>
        row.verified ? (
          <span className="cell-mono">{row.verified}</span>
        ) : row.checkedAt ? (
          <Badge tone="warning">{t('lab.notObserved')}</Badge>
        ) : (
          <Badge tone="warning">{t('lab.neverChecked')}</Badge>
        ),
    },
    { key: 'match', label: t('lab.egress.match'), render: (row) => <MatchBadge match={row.match} /> },
    {
      key: 'latency',
      label: t('lab.egress.latency'),
      align: 'right',
      render: (row) =>
        row.latencyMs === undefined ? (
          <span className="cell-sub">—</span>
        ) : (
          <span className="cell-mono">{formatNumber(row.latencyMs)} ms</span>
        ),
    },
    { key: 'source', label: t('lab.egress.source'), render: (row) => <span className="cell-sub">{row.source}</span> },
    {
      key: 'checked',
      label: t('lab.egress.checked'),
      render: (row) => <span className="cell-sub">{row.checkedAt ? formatDateTime(row.checkedAt) : '—'}</span>,
    },
  ]

  return (
    <DataTable
      caption={t('lab.egress.caption')}
      columns={columns}
      rows={egress}
      rowKey={(row) => row.nodeId}
      empty={<EmptyState title={t('lab.egress.empty')} />}
    />
  )
}

export function EgressTab() {
  const { t } = useI18n()
  const egress = useAsync(() => api.listLabEgress(), [], { intervalMs: 15_000 })
  const gates = useAsync(() => api.listLabGates(), [], { intervalMs: 5_000 })
  const [nodeId, setNodeId] = useState('')
  const [checking, setChecking] = useState(false)
  const [observations, setObservations] = useState<Record<string, EgressCheckResult>>({})
  const [error, setError] = useState<string | null>(null)
  const [cooldowns, setCooldowns] = useState<Record<string, number>>({})

  const egressDisabled = egress.data !== undefined && !Array.isArray(egress.data)
  const gatesDisabled = gates.data !== undefined && !Array.isArray(gates.data)
  const disabled = egressDisabled || gatesDisabled
  const reason =
    (egressDisabled && egress.data && 'reason' in egress.data && egress.data.reason) ||
    (gatesDisabled && gates.data && 'reason' in gates.data && gates.data.reason) ||
    'AJS_ACCOUNT_LOGIN is not 1'

  const egressRows = Array.isArray(egress.data) ? egress.data : []
  const gateRows = Array.isArray(gates.data) ? gates.data : []
  const gateIds = gateRows.map((gate) => gate.nodeId).join('|')
  const mergedRows = mergeEgressRows(egressRows, observations)

  const selectedGate = gateRows.find((gate) => gate.nodeId === nodeId)
  const shown = mergedRows.find((row) => row.nodeId === nodeId)
  const latencyMs = shown?.latencyMs ?? null
  const probeError = shown?.error
  const mismatch = shown?.match === false
  const cooldownUntil = cooldowns[nodeId] ?? 0
  const coolingDown = cooldownUntil > Date.now()
  const canCheck = selectedGate !== undefined && !checking && !coolingDown

  // Keep the selection on a node that still has a running gate; the daemon
  // answers 409 for anything else, so the picker never offers a dead target.
  useEffect(() => {
    if (gateRows.length === 0) {
      if (nodeId !== '') setNodeId('')
      return
    }
    if (!gateRows.some((gate) => gate.nodeId === nodeId)) {
      setNodeId(gateRows[0].nodeId)
      setError(null)
    }
    // gateIds is the stable identity of gateRows for this effect.
  }, [gateIds, nodeId])

  // Re-enable the button when the per-node 5 s quota has elapsed.
  useEffect(() => {
    const until = cooldowns[nodeId]
    if (!until) return
    const clear = () =>
      setCooldowns((prev) => {
        if (!(nodeId in prev)) return prev
        const next = { ...prev }
        delete next[nodeId]
        return next
      })
    const remaining = until - Date.now()
    if (remaining <= 0) {
      clear()
      return
    }
    const timer = window.setTimeout(clear, remaining)
    return () => window.clearTimeout(timer)
  }, [cooldowns, nodeId])

  function chooseNode(value: string) {
    setNodeId(value)
    setError(null)
  }

  async function runCheck() {
    if (!selectedGate || checking || coolingDown) return
    const target = selectedGate.nodeId
    setChecking(true)
    setError(null)
    try {
      const next = await api.checkLabEgress(target)
      // Keep the observation keyed by node: switching nodes and GET refreshes
      // must not erase a result the operator already paid a guarded probe for.
      setObservations((prev) => ({ ...prev, [target]: next }))
      setCooldowns((prev) => ({ ...prev, [target]: Date.now() + CHECK_COOLDOWN_MS }))
    } catch (cause) {
      // A failed probe is not evidence that the previous observation vanished.
      setError(cause instanceof Error ? cause.message : String(cause))
      if (cause instanceof ApiError && cause.status === 429) {
        setCooldowns((prev) => ({ ...prev, [target]: Date.now() + CHECK_COOLDOWN_MS }))
      }
      if (cause instanceof ApiError && cause.status === 409) {
        // The gate disappeared between the poll and the click.
        gates.reload()
      }
    } finally {
      setChecking(false)
    }
  }

  const checkTitle = !selectedGate
    ? t('lab.egress.checkTitleNoGate')
    : coolingDown
      ? t('lab.egress.checkTitleCooldown')
      : t('lab.egress.checkTitleReady')

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">{t('lab.egress.title')}</h2>
          <p className="lab-tab-subtitle">{t('lab.egress.subtitle')}</p>
        </div>
        <div className="lab-tab-actions">
          <Button
            variant="secondary"
            icon={<RefreshCw size={14} />}
            onClick={() => {
              egress.reload()
              gates.reload()
            }}
          >
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      {disabled ? (
        <DisabledCallout
          title={t('lab.egress.disabled')}
          reason={String(reason)}
          flag="AJS_ACCOUNT_LOGIN=1"
        />
      ) : (
        <>
          <Card
            title={t('lab.egress.checkTitle')}
            subtitle={t('lab.egress.checkSubtitle')}
            actions={<Badge tone="warning">{t('lab.egress.guarded')}</Badge>}
          >
            {gates.status === 'error' ? (
              <ErrorState message={gates.error ?? t('common.unknownError')} onRetry={gates.reload} />
            ) : gates.status === 'loading' && !gates.data ? (
              <TableSkeleton rows={3} columns={2} />
            ) : (
              <>
                <div className="lab-tab-actions">
                  <Select
                    aria-label={t('lab.egress.nodeAria')}
                    value={nodeId}
                    onChange={(event) => chooseNode(event.target.value)}
                    disabled={gateRows.length === 0 || checking}
                  >
                    {gateRows.length === 0 ? <option value="">{t('lab.egress.noGateOption')}</option> : null}
                    {gateRows.map((gate) => {
                      const meta = nodeStateMeta(gate.state)
                      return (
                        <option key={gate.nodeId} value={gate.nodeId}>
                          {gate.nodeId} · {gate.accountId} · {meta.labelKey ? t(meta.labelKey) : meta.label}
                        </option>
                      )
                    })}
                  </Select>
                  <Button
                    variant="primary"
                    icon={<Radar size={14} />}
                    onClick={runCheck}
                    loading={checking}
                    disabled={!canCheck}
                    title={checkTitle}
                  >
                    {t('lab.egress.checkTitle')}
                  </Button>
                </div>

                {!selectedGate ? (
                  <EmptyState
                    title={t('lab.egress.noRunningGate')}
                    description={t('lab.egress.noRunningGateDesc')}
                    icon={<Network size={20} aria-hidden="true" />}
                  />
                ) : null}

                {error ? (
                  <div className="callout callout--warning" role="alert">
                    <span className="callout__icon">
                      <AlertTriangle size={18} aria-hidden="true" />
                    </span>
                    <div>
                      <p className="callout__title">{t('lab.egress.failed')}</p>
                      <p className="callout__text">{error}</p>
                    </div>
                  </div>
                ) : null}

                {shown ? (
                  <div role="region" aria-label={t('lab.egress.selectedRegion')}>
                    <LabFacts
                      rows={[
                        { label: t('lab.sessions.node'), value: <span className="mono">{shown.nodeId}</span> },
                        {
                          label: t('lab.egress.claimed'),
                          value: shown.claimed ? (
                            <span className="mono">{shown.claimed}</span>
                          ) : (
                            <span className="cell-sub">—</span>
                          ),
                        },
                        {
                          label: t('lab.egress.verified'),
                          value: shown.verified ? (
                            <span className="mono">{shown.verified}</span>
                          ) : (
                            <Badge tone="warning">{t('lab.notObserved')}</Badge>
                          ),
                        },
                        { label: t('lab.egress.match'), value: <MatchBadge match={shown.match} /> },
                        {
                          label: t('lab.egress.latency'),
                          value:
                            latencyMs === null ? (
                              <span className="cell-sub">—</span>
                            ) : (
                              <span className="mono">{formatNumber(latencyMs)} ms</span>
                            ),
                        },
                        {
                          label: t('lab.egress.checked'),
                          value: shown.checkedAt ? formatDateTime(shown.checkedAt) : <span className="cell-sub">—</span>,
                        },
                        { label: t('lab.egress.source'), value: <span className="mono">{shown.source}</span> },
                      ]}
                    />
                  </div>
                ) : null}

                {mismatch ? (
                  <div className="callout callout--warning" role="alert">
                    <span className="callout__icon">
                      <AlertTriangle size={18} aria-hidden="true" />
                    </span>
                    <div>
                      <p className="callout__title">{t('lab.egress.mismatchTitle')}</p>
                      <p className="callout__text">{t('lab.egress.mismatchText')}</p>
                    </div>
                  </div>
                ) : null}

                {probeError ? (
                  <div className="callout callout--warning" role="alert">
                    <span className="callout__icon">
                      <AlertTriangle size={18} aria-hidden="true" />
                    </span>
                    <div>
                      <p className="callout__title">{t('lab.egress.probeFailed')}</p>
                      <p className="callout__text">{probeError}</p>
                    </div>
                  </div>
                ) : null}
              </>
            )}
          </Card>

          <Card
            title={t('lab.egress.tableTitle')}
            subtitle={t('lab.egress.tableSubtitle')}
            actions={<Badge tone={mergedRows.some((row) => row.verified) ? 'success' : 'warning'}>{t('lab.egress.claimVsEcho')}</Badge>}
            bodyClassName="card__body--flush"
          >
            {egress.status === 'error' ? (
              <ErrorState message={egress.error ?? t('common.unknownError')} onRetry={egress.reload} />
            ) : egress.status === 'loading' && !egress.data ? (
              <TableSkeleton rows={5} columns={6} />
            ) : (
              <EgressTable egress={mergedRows} />
            )}
          </Card>

          <div className="callout callout--muted">
            <span className="callout__icon">
              <Globe size={18} aria-hidden="true" />
            </span>
            <div>
              <p className="callout__title">{t('lab.egress.whatTitle')}</p>
              <p className="callout__text">{t('lab.egress.whatText')}</p>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
