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
import { useI18n } from '../lib/i18n'
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

const TAB_OPTIONS: Array<{ value: LabTab; labelKey: string; icon: ReactNode }> = [
  { value: 'overview', labelKey: 'lab.tab.overview', icon: <LayoutDashboard size={13} /> },
  { value: 'sessions', labelKey: 'lab.tab.sessions', icon: <Gauge size={13} /> },
  { value: 'egress', labelKey: 'lab.tab.egress', icon: <Network size={13} /> },
  { value: 'servers', labelKey: 'lab.tab.servers', icon: <Server size={13} /> },
  { value: 'protocol', labelKey: 'lab.tab.protocol', icon: <Cpu size={13} /> },
  { value: 'device', labelKey: 'lab.tab.device', icon: <Fingerprint size={13} /> },
  { value: 'logs', labelKey: 'lab.tab.logs', icon: <ScrollText size={13} /> },
  { value: 'diagnostics', labelKey: 'lab.tab.diagnostics', icon: <Stethoscope size={13} /> },
]

function environmentBadges(overview: LabOverview | undefined, t: (key: string, params?: Record<string, string | number>) => string) {
  const mode = <Badge tone={isMockMode() ? 'mint' : 'info'}>{isMockMode() ? t('shell.modeMock') : t('shell.modeLive')}</Badge>
  if (!overview) {
    return (
      <>
        {mode}
        <Badge tone="neutral">{t('lab.badge.postureLoading')}</Badge>
      </>
    )
  }
  if (overview.enabled === false) {
    return (
      <>
        {mode}
        <Badge tone="warning">{t('lab.badge.telemetryDisabled')}</Badge>
      </>
    )
  }
  const warnings = overview.warnings ?? []
  return (
    <>
      {mode}
      <Badge tone={overview.modes.accountLogin ? 'success' : 'warning'}>
        {overview.modes.accountLogin ? t('lab.badge.loginOn') : t('lab.badge.loginOff')}
      </Badge>
      <Badge tone={overview.modes.resin ? 'success' : 'warning'}>
        {overview.modes.resin ? t('lab.badge.resinOn') : t('lab.badge.resinOff')}
      </Badge>
      <Badge tone={overview.modes.certPin === 'pinned' ? 'success' : 'warning'}>
        {certPinLabel(overview.modes.certPin, t)}
      </Badge>
      {warnings.length > 0 ? (
        <Badge tone="warning">
          {t(warnings.length === 1 ? 'lab.badge.warning' : 'lab.badge.warnings', { count: warnings.length })}
        </Badge>
      ) : null}
    </>
  )
}

export default function Lab() {
  const { t } = useI18n()
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
        title={t('lab.title')}
        subtitle={t('lab.subtitle')}
        refresh={summarizeRefresh([overview, accounts])}
        actions={
          <>
            <span className="lab-env-badges">{environmentBadges(overview.data, t)}</span>
            {needsAccount ? (
              <Select
                variant="form"
                size="sm"
                searchable
                aria-label={t('lab.accountSelect')}
                value={accountId}
                onChange={setAccountId}
                disabled={(accounts.data ?? []).length === 0}
                options={(accounts.data ?? []).map((account) => ({
                  value: account.id,
                  label: account.label,
                }))}
              />
            ) : null}
            <Button
              variant="secondary"
              icon={<RefreshCw size={14} />}
              onClick={() => window.dispatchEvent(new Event('ajs:refresh'))}
            >
              {t('common.refresh')}
            </Button>
          </>
        }
      />

      <Tabs
        label={t('lab.views')}
        value={tab}
        onChange={setTab}
        options={TAB_OPTIONS.map((option) => ({
          value: option.value,
          label: t(option.labelKey),
          icon: option.icon,
        }))}
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
              title={t('lab.noAccount.title')}
              description={t('lab.noAccount.servers')}
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
              title={t('lab.noAccount.title')}
              description={t('lab.noAccount.device')}
              icon={<Fingerprint size={20} aria-hidden="true" />}
            />
          </Card>
        )
      ) : null}

      {tab === 'logs' ? <LogsTab /> : null}

      {tab === 'diagnostics' ? <DiagnosticsTab /> : null}

      <p className="page-note">
        <FlaskConical size={12} aria-hidden="true" /> {t('lab.note')}
      </p>
    </div>
  )
}
