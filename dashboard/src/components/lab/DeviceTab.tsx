import { Fingerprint, RefreshCw } from 'lucide-react'
import { api } from '../../lib/api'
import { formatDateTime } from '../../lib/format'
import { useAsync } from '../../lib/useAsync'
import { Badge, Button, Card, EmptyState, ErrorState, TableSkeleton } from '../primitives'
import { DisabledCallout, LabFacts } from './shared'

export function DeviceTab({ accountId, accountLabel }: { accountId: string; accountLabel: string }) {
  const device = useAsync(() => api.getAccountDevice(accountId), [accountId], { intervalMs: 15_000 })

  const disabled = device.data?.enabled === false

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">Device identity</h2>
          <p className="lab-tab-subtitle">
            Hashed anti-abuse identity for {accountLabel || accountId}. Raw identifiers never reach the browser.
          </p>
        </div>
        <div className="lab-tab-actions">
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={device.reload}>
            Refresh
          </Button>
        </div>
      </div>

      {disabled ? (
        <DisabledCallout
          title="Device identity is disabled"
          reason={device.data?.reason ?? 'Account login is not enabled.'}
          flag="AJS_ACCOUNT_LOGIN=1"
        />
      ) : null}

      {device.status === 'error' ? (
        <Card>
          <ErrorState message={device.error ?? 'Unknown error'} onRetry={device.reload} />
        </Card>
      ) : device.status === 'loading' && !device.data ? (
        <Card title="Device">
          <TableSkeleton rows={5} columns={3} />
        </Card>
      ) : device.data && !disabled ? (
        device.data.present ? (
          <div className="grid-2-1">
            <Card
              title="Sealed identity"
              subtitle="Hashes are computed server-side; the UI cannot unredact them."
              actions={
                <Badge tone={device.data.stable ? 'success' : 'warning'}>
                  {device.data.stable ? 'stable' : 'unstable'}
                </Badge>
              }
            >
              <LabFacts
                rows={[
                  { label: 'Account', value: <span className="mono">{device.data.accountId}</span> },
                  { label: 'First seen', value: device.data.firstSeenAt ? formatDateTime(device.data.firstSeenAt) : 'unknown' },
                  { label: 'Raw available', value: <Badge tone="neutral">{device.data.rawAvailable ? 'yes' : 'no'}</Badge> },
                  { label: 'Stable', value: <Badge tone={device.data.stable ? 'success' : 'warning'}>{device.data.stable ? 'yes' : 'no'}</Badge> },
                ]}
              />
              <p className="setting-note">
                <Fingerprint size={12} aria-hidden="true" /> Device stability matters: an unstable identity can
                trip the vendor's anti-abuse checks across logins.
              </p>
            </Card>

            <Card title="Hashed identifiers" subtitle="sha256: prefix only.">
              {Object.keys(device.data.hashes).length === 0 ? (
                <EmptyState title="No hashed identifiers" />
              ) : (
                <LabFacts
                  rows={Object.entries(device.data.hashes).map(([key, value]) => ({
                    label: key,
                    value: <span className="mono cell-truncate">{value}</span>,
                  }))}
                />
              )}
            </Card>

            <Card title="Claims" subtitle="Presence and platform fields, not secrets." className="lab-span-2">
              <LabFacts
                rows={Object.entries(device.data.claims).map(([key, value]) => ({
                  label: key,
                  value: <span className="mono">{value}</span>,
                }))}
              />
            </Card>
          </div>
        ) : (
          <Card>
            <EmptyState
              title="No device identity recorded"
              description="A device record is created on the first successful login for this account."
              icon={<Fingerprint size={20} aria-hidden="true" />}
            />
          </Card>
        )
      ) : null}
    </div>
  )
}
