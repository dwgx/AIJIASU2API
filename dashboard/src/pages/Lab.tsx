import {
  Cpu,
  Fingerprint,
  FlaskConical,
  Gauge,
  LayoutDashboard,
  Network,
  RefreshCw,
  ScrollText,
  Server,
  Stethoscope,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { api, isMockMode } from '../lib/api'
import { certPinLabel } from '../lib/lab'
import type { LabOverview } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { Badge, Button, Card, EmptyState, PageHeader, Select, Tabs } from '../components/primitives'
import { DiagnosticsTab } from '../components/lab/DiagnosticsTab'
import { EgressTab } from '../components/lab/EgressTab'
import { DeviceTab } from '../components/lab/DeviceTab'
import { LogsTab } from '../components/lab/LogsTab'
import { OverviewTab } from '../components/lab/OverviewTab'
import { ProtocolTab } from '../components/lab/ProtocolTab'
import { ServersTab } from '../components/lab/ServersTab'
import { SessionsTab } from '../components/lab/SessionsTab'

type LabTab = 'overview' | 'sessions' | 'egress' | 'servers' | 'protocol' | 'device' | 'logs' | 'diagnostics'

const TAB_OPTIONS: Array<{ value: LabTab; label: string; icon: ReactNode }> = [
  { value: 'overview', label: 'Overview', icon: <LayoutDashboard size={13} /> },
  { value: 'sessions', label: 'Gates & sessions', icon: <Gauge size={13} /> },
  { value: 'egress', label: 'Egress', icon: <Network size={13} /> },
  { value: 'servers', label: 'Servers', icon: <Server size={13} /> },
  { value: 'protocol', label: 'Protocol', icon: <Cpu size={13} /> },
  { value: 'device', label: 'Device', icon: <Fingerprint size={13} /> },
  { value: 'logs', label: 'Logs', icon: <ScrollText size={13} /> },
  { value: 'diagnostics', label: 'Diagnostics', icon: <Stethoscope size={13} /> },
]

function environmentBadges(overview: LabOverview | undefined) {
  if (!overview) {
    return (
      <>
        <Badge tone={isMockMode() ? 'mint' : 'info'}>{isMockMode() ? 'mock' : 'live'}</Badge>
        <Badge tone="neutral">posture loading</Badge>
      </>
    )
  }
  if (overview.enabled === false) {
    return (
      <>
        <Badge tone={isMockMode() ? 'mint' : 'info'}>{isMockMode() ? 'mock' : 'live'}</Badge>
        <Badge tone="warning">lab telemetry disabled</Badge>
      </>
    )
  }
  const warnings = overview.warnings ?? []
  return (
    <>
      <Badge tone={isMockMode() ? 'mint' : 'info'}>{isMockMode() ? 'mock' : 'live'}</Badge>
      <Badge tone={overview.modes.accountLogin ? 'success' : 'warning'}>
        login {overview.modes.accountLogin ? 'on' : 'off'}
      </Badge>
      <Badge tone={overview.modes.resin ? 'success' : 'warning'}>
        Resin {overview.modes.resin ? 'on' : 'off'}
      </Badge>
      <Badge tone={overview.modes.certPin === 'pinned' ? 'success' : 'warning'}>
        {certPinLabel(overview.modes.certPin)}
      </Badge>
      {warnings.length > 0 ? (
        <Badge tone="warning">
          {warnings.length} warning{warnings.length === 1 ? '' : 's'}
        </Badge>
      ) : null}
    </>
  )
}

export default function Lab() {
  const [tab, setTab] = useState<LabTab>('overview')
  const [accountId, setAccountId] = useState('')
  const overview = useAsync(() => api.getLabOverview(), [], { intervalMs: 10_000 })
  const accounts = useAsync(() => api.listAccounts(), [])

  useEffect(() => {
    if (!accounts.data || accounts.data.length === 0) return
    if (!accounts.data.some((account) => account.id === accountId)) {
      setAccountId(accounts.data[0].id)
    }
  }, [accountId, accounts.data])

  const selectedAccount = accounts.data?.find((account) => account.id === accountId)
  const needsAccount = tab === 'servers' || tab === 'device'
  const accountLabel = selectedAccount?.label ?? accountId

  return (
    <div className="stack-lg">
      <PageHeader
        title="Lab"
        subtitle="Read-only lab instrument plus one guarded egress check: flags, catalog, gate counters, session health, claimed-vs-observed egress, profiles, device identity, logs and diagnostics. No vendor call is made by this page."
        refresh={summarizeRefresh([overview, accounts])}
        actions={
          <>
            <span className="lab-env-badges">{environmentBadges(overview.data)}</span>
            {needsAccount ? (
              <Select
                aria-label="Lab account"
                value={accountId}
                onChange={(event) => setAccountId(event.target.value)}
                disabled={(accounts.data ?? []).length === 0}
              >
                {(accounts.data ?? []).map((account) => (
                  <option key={account.id} value={account.id}>
                    {account.label}
                  </option>
                ))}
              </Select>
            ) : null}
            <Button
              variant="secondary"
              icon={<RefreshCw size={14} />}
              onClick={() => window.dispatchEvent(new Event('ajs:refresh'))}
            >
              Refresh
            </Button>
          </>
        }
      />

      <Tabs
        label="Lab views"
        value={tab}
        onChange={setTab}
        options={TAB_OPTIONS}
      />

      {tab === 'overview' ? (
        <OverviewTab
          overview={overview.data}
          loading={overview.status === 'loading'}
          error={overview.error}
          onRetry={overview.reload}
        />
      ) : null}

      {tab === 'sessions' ? <SessionsTab /> : null}

      {tab === 'egress' ? <EgressTab /> : null}

      {tab === 'servers' ? (
        accountId ? (
          <ServersTab accountId={accountId} accountLabel={accountLabel} />
        ) : (
          <Card>
            <EmptyState
              title="No account selected"
              description="Create an account before opening the vendor server catalog."
              icon={<Server size={20} aria-hidden="true" />}
            />
          </Card>
        )
      ) : null}

      {tab === 'protocol' ? <ProtocolTab /> : null}

      {tab === 'device' ? (
        accountId ? (
          <DeviceTab accountId={accountId} accountLabel={accountLabel} />
        ) : (
          <Card>
            <EmptyState
              title="No account selected"
              description="A device identity is created on the first successful login for an account."
              icon={<Fingerprint size={20} aria-hidden="true" />}
            />
          </Card>
        )
      ) : null}

      {tab === 'logs' ? <LogsTab /> : null}

      {tab === 'diagnostics' ? <DiagnosticsTab /> : null}

      <p className="page-note">
        <FlaskConical size={12} aria-hidden="true" /> Lab is read-only except the guarded egress check, which dials the
        selected node loopback SOCKS5 gate. PCAP, catalog refresh and diagnostics export remain phase-2 actions.
      </p>
    </div>
  )
}
