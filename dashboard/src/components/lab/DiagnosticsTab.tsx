import { RefreshCw, Stethoscope } from 'lucide-react'
import { api } from '../../lib/api'
import { formatBytes, formatDateTime } from '../../lib/format'
import { healthTone } from '../../lib/lab'
import { useAsync } from '../../lib/useAsync'
import { Badge, Button, Card, ErrorState, TableSkeleton } from '../primitives'
import { DisabledCallout, LabFacts, WarningList } from './shared'

export function DiagnosticsTab() {
  const diagnostics = useAsync(() => api.getLabDiagnostics(), [], { intervalMs: 15_000 })
  const data = diagnostics.data

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">Diagnostics</h2>
          <p className="lab-tab-subtitle">
            Readiness of the API, store, secrets, pin, Resin, update, controller, log ring and PCAP.
          </p>
        </div>
        <div className="lab-tab-actions">
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={diagnostics.reload}>
            Refresh
          </Button>
        </div>
      </div>

      {data?.enabled === false ? (
        <DisabledCallout
          title="Diagnostics are unavailable"
          reason={data.reason ?? 'The daemon did not wire a Lab reader.'}
        />
      ) : diagnostics.status === 'error' ? (
        <Card>
          <ErrorState message={diagnostics.error ?? 'Unknown error'} onRetry={diagnostics.reload} />
        </Card>
      ) : diagnostics.status === 'loading' && !data ? (
        <Card title="Diagnostics">
          <TableSkeleton rows={6} columns={4} />
        </Card>
      ) : data ? (
        <>
          <Card title="Component health" subtitle="A single failure is not hidden behind an aggregate status.">
            <div className="lab-health-grid">
              {Object.entries(data.health).map(([component, value]) => (
                <div className="lab-health" key={component}>
                  <span className="lab-health__name mono">{component}</span>
                  <Badge tone={healthTone(value)}>{value}</Badge>
                </div>
              ))}
            </div>
          </Card>

          <div className="grid-2-1">
            <Card title="State snapshot" subtitle="Basename and size only; the full path stays on the host.">
              <LabFacts
                rows={[
                  { label: 'Path', value: <span className="mono">{data.state.path || '—'}</span> },
                  {
                    label: 'Last saved',
                    value: data.state.lastSavedAt ? formatDateTime(data.state.lastSavedAt) : 'never',
                  },
                  {
                    label: 'Size',
                    value: data.state.sizeBytes === null ? '—' : formatBytes(data.state.sizeBytes),
                  },
                ]}
              />
            </Card>

            <Card title="Build" subtitle="Values reported by the running daemon.">
              <LabFacts
                rows={[
                  { label: 'Version', value: <span className="mono">{data.build.version || 'unknown'}</span> },
                  { label: 'Go', value: <span className="mono">{data.build.goVersion || 'unknown'}</span> },
                  { label: 'Commit', value: <span className="mono">{data.build.commit || 'not embedded'}</span> },
                ]}
              />
            </Card>
          </div>

          <Card title="Warnings" subtitle="Posture findings from the same probe set.">
            <WarningList warnings={data.warnings} />
          </Card>

          <p className="callout callout--muted">
            <Stethoscope size={12} aria-hidden="true" /> Diagnostics export is a phase-2 guarded action. This page
            never writes a file or exposes a path beyond a basename.
          </p>
        </>
      ) : null}
    </div>
  )
}
