import { Cpu, RefreshCw, ShieldAlert } from 'lucide-react'
import { api } from '../../lib/api'
import { useI18n } from '../../lib/i18n'
import type { ProfileView, SampleProfileView } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { DataTable } from '../DataTable'
import type { Column } from '../DataTable'
import { Badge, Button, Card, ErrorState, TableSkeleton } from '../primitives'
import { DisabledCallout, LabFacts } from './shared'

export function ProtocolTab() {
  const { t } = useI18n()
  const profiles = useAsync(() => api.getLabProfiles(), [], { intervalMs: 15_000 })

  const active = profiles.data?.supported.find((profile) => profile.name === profiles.data?.active) ?? null
  const drift =
    profiles.data !== undefined &&
    profiles.data.samples.some((sample) => sample.version !== active?.clientVersion)

  const profileColumns: Array<Column<ProfileView>> = [
    { key: 'name', label: t('lab.protocol.profile'), render: (profile) => <span className="cell-mono">{profile.name}</span> },
    { key: 'client', label: t('lab.protocol.client'), render: (profile) => <span className="cell-mono">{profile.clientVersion}</span> },
    { key: 'core', label: t('lab.protocol.core'), render: (profile) => <span className="cell-mono">{profile.coreVersion}</span> },
    {
      key: 'protocol',
      label: t('lab.protocol.protocol'),
      align: 'right',
      render: (profile) => <span className="cell-mono">v{profile.protocolVersion}</span>,
    },
    { key: 'oaep', label: t('lab.protocol.oaep'), render: (profile) => <span className="cell-mono">{profile.oaepHash}</span> },
    { key: 'signing', label: t('lab.protocol.signing'), render: (profile) => <span className="cell-mono">{profile.signingMode}</span> },
    { key: 'shape', label: t('lab.protocol.shape'), render: (profile) => <span className="cell-mono">{profile.requestShape}</span> },
    { key: 'transport', label: t('lab.protocol.transport'), render: (profile) => <Badge tone="info">{profile.transport}</Badge> },
  ]

  const sampleColumns: Array<Column<SampleProfileView>> = [
    { key: 'platform', label: t('lab.protocol.platform'), render: (sample) => <span className="cell-mono">{sample.platform}</span> },
    { key: 'version', label: t('lab.protocol.version'), render: (sample) => <span className="cell-mono">{sample.version}</span> },
    { key: 'core', label: t('lab.protocol.core'), render: (sample) => <span className="cell-mono">{sample.core}</span> },
    { key: 'source', label: t('lab.overview.source'), render: (sample) => <span className="cell-sub">{sample.source}</span> },
  ]

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">{t('lab.protocol.title')}</h2>
          <p className="lab-tab-subtitle">{t('lab.protocol.subtitle')}</p>
        </div>
        <div className="lab-tab-actions">
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={profiles.reload}>
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      {profiles.data?.enabled === false ? (
        <DisabledCallout
          title={t('lab.protocol.disabled')}
          reason={profiles.data.reason ?? t('lab.protocol.disabledReason')}
        />
      ) : profiles.status === 'error' ? (
        <Card>
          <ErrorState message={profiles.error ?? t('common.unknownError')} onRetry={profiles.reload} />
        </Card>
      ) : profiles.status === 'loading' && !profiles.data ? (
        <Card title={t('lab.protocol.profiles')}>
          <TableSkeleton rows={4} columns={6} />
        </Card>
      ) : profiles.data ? (
        <>
          <div className="grid-2-1">
            <Card
              title={t('lab.protocol.activeProfile')}
              subtitle={t('lab.protocol.activeSubtitle')}
              actions={<Badge tone="success">{profiles.data.active || t('common.none')}</Badge>}
            >
              {active ? (
                <LabFacts
                  rows={[
                    { label: t('lab.protocol.name'), value: <span className="mono">{active.name}</span> },
                    { label: t('lab.protocol.clientVersion'), value: <span className="mono">{active.clientVersion}</span> },
                    { label: t('lab.protocol.coreVersion'), value: <span className="mono">{active.coreVersion}</span> },
                    { label: t('lab.protocol.protocolVersion'), value: <span className="mono">v{active.protocolVersion}</span> },
                    { label: t('lab.protocol.oaepHash'), value: <span className="mono">{active.oaepHash}</span> },
                    { label: t('lab.protocol.signingMode'), value: <span className="mono">{active.signingMode}</span> },
                    { label: t('lab.protocol.requestShape'), value: <span className="mono">{active.requestShape}</span> },
                    { label: t('lab.protocol.transport'), value: <Badge tone="info">{active.transport}</Badge> },
                  ]}
                />
              ) : (
                <p className="cell-sub">{t('lab.protocol.noActive')}</p>
              )}
            </Card>

            <Card title={t('lab.protocol.mismatch')} subtitle={t('lab.protocol.mismatchSubtitle')}>
              {drift ? (
                <div className="callout callout--warning" role="status">
                  <span className="callout__icon">
                    <ShieldAlert size={18} aria-hidden="true" />
                  </span>
                  <div>
                    <p className="callout__title">{t('lab.protocol.driftTitle')}</p>
                    <p className="callout__text">
                      {t('lab.protocol.driftText', { version: active?.clientVersion ?? t('common.unknown') })}
                    </p>
                  </div>
                </div>
              ) : (
                <p className="callout callout--muted">{t('lab.protocol.match')}</p>
              )}
              <p className="setting-note">
                <Cpu size={12} aria-hidden="true" /> {t('lab.protocol.note')}
              </p>
            </Card>
          </div>

          <Card title={t('lab.protocol.supported')} bodyClassName="card__body--flush">
            <DataTable
              caption={t('lab.protocol.supportedCaption')}
              columns={profileColumns}
              rows={profiles.data.supported}
              rowKey={(profile) => profile.name}
            />
          </Card>

          <Card
            title={t('lab.protocol.samples')}
            subtitle={t('lab.protocol.samplesSubtitle')}
            bodyClassName="card__body--flush"
          >
            <DataTable
              caption={t('lab.protocol.samplesCaption')}
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
