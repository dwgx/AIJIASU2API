import { Cpu, RefreshCw, ShieldAlert } from 'lucide-react'
import { api } from '../../lib/api'
import type { ProfileView, SampleProfileView } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import { Badge, Button, Card, ErrorState, TableSkeleton } from '../primitives'
import { DisabledCallout, LabFacts } from './shared'

export function ProtocolTab() {
  const profiles = useAsync(() => api.getLabProfiles(), [], { intervalMs: 15_000 })

  const active = profiles.data?.supported.find((profile) => profile.name === profiles.data?.active) ?? null
  const drift =
    profiles.data !== undefined &&
    profiles.data.samples.some((sample) => sample.version !== active?.clientVersion)

  const profileColumns: Array<Column<ProfileView>> = [
    { key: 'name', label: 'Profile', render: (profile) => <span className="cell-mono">{profile.name}</span> },
    { key: 'client', label: 'Client', render: (profile) => <span className="cell-mono">{profile.clientVersion}</span> },
    { key: 'core', label: 'Core', render: (profile) => <span className="cell-mono">{profile.coreVersion}</span> },
    {
      key: 'protocol',
      label: 'Protocol',
      align: 'right',
      render: (profile) => <span className="cell-mono">v{profile.protocolVersion}</span>,
    },
    { key: 'oaep', label: 'OAEP hash', render: (profile) => <span className="cell-mono">{profile.oaepHash}</span> },
    { key: 'signing', label: 'Signing', render: (profile) => <span className="cell-mono">{profile.signingMode}</span> },
    { key: 'shape', label: 'Request shape', render: (profile) => <span className="cell-mono">{profile.requestShape}</span> },
    { key: 'transport', label: 'Transport', render: (profile) => <Badge tone="info">{profile.transport}</Badge> },
  ]

  const sampleColumns: Array<Column<SampleProfileView>> = [
    { key: 'platform', label: 'Platform', render: (sample) => <span className="cell-mono">{sample.platform}</span> },
    { key: 'version', label: 'Version', render: (sample) => <span className="cell-mono">{sample.version}</span> },
    { key: 'core', label: 'Core', render: (sample) => <span className="cell-mono">{sample.core}</span> },
    { key: 'source', label: 'Source', render: (sample) => <span className="cell-sub">{sample.source}</span> },
  ]

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">Protocol profiles</h2>
          <p className="lab-tab-subtitle">The active version profile and the sample baselines it is compared against.</p>
        </div>
        <div className="lab-tab-actions">
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={profiles.reload}>
            Refresh
          </Button>
        </div>
      </div>

      {profiles.data?.enabled === false ? (
        <DisabledCallout
          title="Protocol profiles are unavailable"
          reason={profiles.data.reason ?? 'The daemon did not expose a version profile.'}
        />
      ) : profiles.status === 'error' ? (
        <Card>
          <ErrorState message={profiles.error ?? 'Unknown error'} onRetry={profiles.reload} />
        </Card>
      ) : profiles.status === 'loading' && !profiles.data ? (
        <Card title="Profiles">
          <TableSkeleton rows={4} columns={6} />
        </Card>
      ) : profiles.data ? (
        <>
          <div className="grid-2-1">
            <Card
              title="Active profile"
              subtitle="Only this profile is accepted by the daemon today."
              actions={<Badge tone="success">{profiles.data.active || 'none'}</Badge>}
            >
              {active ? (
                <LabFacts
                  rows={[
                    { label: 'Name', value: <span className="mono">{active.name}</span> },
                    { label: 'Client version', value: <span className="mono">{active.clientVersion}</span> },
                    { label: 'Core version', value: <span className="mono">{active.coreVersion}</span> },
                    { label: 'Protocol version', value: <span className="mono">v{active.protocolVersion}</span> },
                    { label: 'OAEP hash', value: <span className="mono">{active.oaepHash}</span> },
                    { label: 'Signing mode', value: <span className="mono">{active.signingMode}</span> },
                    { label: 'Request shape', value: <span className="mono">{active.requestShape}</span> },
                    { label: 'Transport', value: <Badge tone="info">{active.transport}</Badge> },
                  ]}
                />
              ) : (
                <p className="cell-sub">No active profile is registered.</p>
              )}
            </Card>

            <Card title="Mismatch posture" subtitle="Version drift cannot be diagnosed from a green login alone.">
              {drift ? (
                <div className="callout callout--warning" role="status">
                  <span className="callout__icon">
                    <ShieldAlert size={18} aria-hidden="true" />
                  </span>
                  <div>
                    <p className="callout__title">Sample baseline differs from the active profile</p>
                    <p className="callout__text">
                      The daemon accepts <code className="mono">{active?.clientVersion ?? 'unknown'}</code> only,
                      while the samples in the table use newer client versions. A login failure cannot be
                      attributed to a version mismatch without a matching profile.
                    </p>
                  </div>
                </div>
              ) : (
                <p className="callout callout--muted">Active and sample versions match.</p>
              )}
              <p className="setting-note">
                <Cpu size={12} aria-hidden="true" /> Profile changes require a daemon restart; this page is
                read-only.
              </p>
            </Card>
          </div>

          <Card title="Supported profiles" bodyClassName="card__body--flush">
            <DataTable
              caption="Supported protocol profiles"
              columns={profileColumns}
              rows={profiles.data.supported}
              rowKey={(profile) => profile.name}
            />
          </Card>

          <Card
            title="Sample baselines"
            subtitle="Versions observed in the extracted vendor clients; read-only research references."
            bodyClassName="card__body--flush"
          >
            <DataTable
              caption="Sample client baselines"
              columns={sampleColumns}
              rows={profiles.data.samples}
              rowKey={(sample) => `${sample.platform}-${sample.version}`}
            />
          </Card>
        </>
      ) : null}
    </div>
  )
}
