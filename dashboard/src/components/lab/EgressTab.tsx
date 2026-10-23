import { AlertTriangle, Globe, Network, Radar, RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api } from '../../lib/api'
import { ApiError } from '../../lib/errors'
import { formatDateTime, formatNumber } from '../../lib/format'
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

function MatchBadge({ match }: { match: boolean | null }) {
  if (match === null) return <Badge tone="neutral">unknown</Badge>
  return <Badge tone={match ? 'success' : 'danger'}>{match ? 'match' : 'mismatch'}</Badge>
}

function EgressTable({ egress }: { egress: EgressView[] }) {
  const columns: Array<Column<EgressView>> = [
    { key: 'node', label: 'Node', render: (row) => <span className="cell-mono">{row.nodeId}</span> },
    { key: 'claimed', label: 'Claimed', render: (row) => <span className="cell-mono">{row.claimed || '—'}</span> },
    {
      key: 'verified',
      label: 'Verified',
      render: (row) =>
        row.verified ? <span className="cell-mono">{row.verified}</span> : <Badge tone="warning">never checked</Badge>,
    },
    { key: 'match', label: 'Match', render: (row) => <MatchBadge match={row.match} /> },
    { key: 'source', label: 'Source', render: (row) => <span className="cell-sub">{row.source}</span> },
    {
      key: 'checked',
      label: 'Checked',
      render: (row) => <span className="cell-sub">{row.checkedAt ? formatDateTime(row.checkedAt) : '—'}</span>,
    },
  ]

  return (
    <DataTable
      caption="Egress claimed versus verified"
      columns={columns}
      rows={egress}
      rowKey={(row) => row.nodeId}
      empty={<EmptyState title="No egress records" />}
    />
  )
}

export function EgressTab() {
  const egress = useAsync(() => api.listLabEgress(), [], { intervalMs: 15_000 })
  const gates = useAsync(() => api.listLabGates(), [], { intervalMs: 5_000 })
  const [nodeId, setNodeId] = useState('')
  const [checking, setChecking] = useState(false)
  const [result, setResult] = useState<EgressCheckResult | null>(null)
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

  const selectedGate = gateRows.find((gate) => gate.nodeId === nodeId)
  const selectedEgress = egressRows.find((row) => row.nodeId === nodeId)
  const shown = result && result.nodeId === nodeId ? result : selectedEgress
  const latencyMs =
    shown && 'latencyMs' in shown && typeof shown.latencyMs === 'number' ? shown.latencyMs : null
  const probeError = result && result.nodeId === nodeId ? result.error : undefined
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
      setResult(null)
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
    setResult(null)
    setError(null)
  }

  async function runCheck() {
    if (!selectedGate || checking || coolingDown) return
    const target = selectedGate.nodeId
    setChecking(true)
    setError(null)
    try {
      const next = await api.checkLabEgress(target)
      setResult(next)
      setCooldowns((prev) => ({ ...prev, [target]: Date.now() + CHECK_COOLDOWN_MS }))
    } catch (cause) {
      setResult(null)
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
    ? 'No running gate for the selected node'
    : coolingDown
      ? 'One egress check per node every 5 seconds'
      : 'Dial the selected node SOCKS5 gate and read https://ifconfig.me/ip'

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">Egress claim vs observed</h2>
          <p className="lab-tab-subtitle">
            The vendor claim is not an observation. A guarded check dials the selected node loopback SOCKS5 gate and
            reads https://ifconfig.me/ip; no vendor host and no credential is used.
          </p>
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
            Refresh
          </Button>
        </div>
      </div>

      {disabled ? (
        <DisabledCallout
          title="Egress telemetry is disabled"
          reason={String(reason)}
          flag="AJS_ACCOUNT_LOGIN=1"
        />
      ) : (
        <>
          <Card
            title="Check egress"
            subtitle="One node per check with a 5 s cooldown. The daemon refuses a node without a running gate (409)."
            actions={<Badge tone="warning">guarded</Badge>}
          >
            {gates.status === 'error' ? (
              <ErrorState message={gates.error ?? 'Unknown error'} onRetry={gates.reload} />
            ) : gates.status === 'loading' && !gates.data ? (
              <TableSkeleton rows={3} columns={2} />
            ) : (
              <>
                <div className="lab-tab-actions">
                  <Select
                    aria-label="Egress check node"
                    value={nodeId}
                    onChange={(event) => chooseNode(event.target.value)}
                    disabled={gateRows.length === 0 || checking}
                  >
                    {gateRows.length === 0 ? <option value="">No running gate</option> : null}
                    {gateRows.map((gate) => (
                      <option key={gate.nodeId} value={gate.nodeId}>
                        {gate.nodeId} · {gate.accountId} · {nodeStateMeta(gate.state).label}
                      </option>
                    ))}
                  </Select>
                  <Button
                    variant="primary"
                    icon={<Radar size={14} />}
                    onClick={runCheck}
                    loading={checking}
                    disabled={!canCheck}
                    title={checkTitle}
                  >
                    Check egress
                  </Button>
                </div>

                {!selectedGate ? (
                  <EmptyState
                    title="No running gate"
                    description="A node must be LIVE or DRAINING before the daemon can dial its SOCKS5 gate."
                    icon={<Network size={20} aria-hidden="true" />}
                  />
                ) : null}

                {error ? (
                  <div className="callout callout--warning" role="alert">
                    <span className="callout__icon">
                      <AlertTriangle size={18} aria-hidden="true" />
                    </span>
                    <div>
                      <p className="callout__title">Egress check failed</p>
                      <p className="callout__text">{error}</p>
                    </div>
                  </div>
                ) : null}

                {shown ? (
                  <div role="region" aria-label="Selected node egress">
                    <LabFacts
                      rows={[
                        { label: 'Node', value: <span className="mono">{shown.nodeId}</span> },
                        {
                          label: 'Claimed',
                          value: shown.claimed ? (
                            <span className="mono">{shown.claimed}</span>
                          ) : (
                            <span className="cell-sub">—</span>
                          ),
                        },
                        {
                          label: 'Verified',
                          value: shown.verified ? (
                            <span className="mono">{shown.verified}</span>
                          ) : (
                            <Badge tone="warning">not observed</Badge>
                          ),
                        },
                        { label: 'Match', value: <MatchBadge match={shown.match} /> },
                        {
                          label: 'Latency',
                          value:
                            latencyMs === null ? (
                              <span className="cell-sub">—</span>
                            ) : (
                              <span className="mono">{formatNumber(latencyMs)} ms</span>
                            ),
                        },
                        {
                          label: 'Checked',
                          value: shown.checkedAt ? formatDateTime(shown.checkedAt) : <span className="cell-sub">—</span>,
                        },
                        { label: 'Source', value: <span className="mono">{shown.source}</span> },
                      ]}
                    />
                  </div>
                ) : null}

                {probeError ? (
                  <div className="callout callout--warning" role="alert">
                    <span className="callout__icon">
                      <AlertTriangle size={18} aria-hidden="true" />
                    </span>
                    <div>
                      <p className="callout__title">Probe failed</p>
                      <p className="callout__text">{probeError}</p>
                    </div>
                  </div>
                ) : null}
              </>
            )}
          </Card>

          <Card
            title="Claimed vs verified"
            subtitle="Verified stays empty until a check observes the exit through the gate. A vendor claim alone is not evidence."
            actions={<Badge tone={egressRows.some((row) => row.verified) ? 'success' : 'warning'}>claim vs echo</Badge>}
            bodyClassName="card__body--flush"
          >
            {egress.status === 'error' ? (
              <ErrorState message={egress.error ?? 'Unknown error'} onRetry={egress.reload} />
            ) : egress.status === 'loading' && !egress.data ? (
              <TableSkeleton rows={5} columns={6} />
            ) : (
              <EgressTable egress={egressRows} />
            )}
          </Card>

          <div className="callout callout--muted">
            <span className="callout__icon">
              <Globe size={18} aria-hidden="true" />
            </span>
            <div>
              <p className="callout__title">What the check observes</p>
              <p className="callout__text">
                <code className="mono">source=echo</code> means the IP was read from{' '}
                <code className="mono">https://ifconfig.me/ip</code> after dialing the node loopback SOCKS5 gate. A
                probe error still returns <code className="mono">verified=&quot;&quot;</code> and{' '}
                <code className="mono">match=null</code>; the UI never fills the gap with the vendor claim.
              </p>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
