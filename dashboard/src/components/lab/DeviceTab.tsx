import { Fingerprint, Lock, RefreshCw } from 'lucide-react'
import { api } from '../../lib/api'
import { formatDateTime } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { useAsync } from '../../lib/useAsync'
import { Badge, Button, Card, EmptyState, ErrorState, TableSkeleton } from '../primitives'
import { DisabledCallout, LabFacts } from './shared'

export function DeviceTab({ accountId, accountLabel }: { accountId: string; accountLabel: string }) {
  const { t } = useI18n()
  const device = useAsync(() => api.getAccountDevice(accountId), [accountId], { intervalMs: 15_000 })

  const disabled = device.data?.enabled === false

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">{t('lab.device.title')}</h2>
          <p className="lab-tab-subtitle">
            {t('lab.device.subtitle', { account: accountLabel || accountId })}
          </p>
        </div>
        <div className="lab-tab-actions">
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={device.reload}>
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      {disabled ? (
        <DisabledCallout
          title={t('lab.device.disabled')}
          reason={device.data?.reason ?? t('lab.servers.disabledReason')}
          flag="AJS_ACCOUNT_LOGIN=1"
        />
      ) : null}

      {device.status === 'error' ? (
        <Card>
          <ErrorState message={device.error ?? t('common.unknownError')} onRetry={device.reload} />
        </Card>
      ) : device.status === 'loading' && !device.data ? (
        <Card title={t('lab.device.loadingTitle')}>
          <TableSkeleton rows={5} columns={3} />
        </Card>
      ) : device.data && !disabled ? (
        device.data.present ? (
          <div className="grid-2-1">
            <Card
              title={t('lab.device.sealedTitle')}
              subtitle={t('lab.device.sealedSubtitle')}
              actions={
                <Badge tone={device.data.stable ? 'success' : 'warning'}>
                  {device.data.stable ? t('lab.stable') : t('lab.unstable')}
                </Badge>
              }
            >
              <LabFacts
                rows={[
                  { label: t('lab.device.account'), value: <span className="mono">{device.data.accountId}</span> },
                  { label: t('lab.device.firstSeen'), value: device.data.firstSeenAt ? formatDateTime(device.data.firstSeenAt) : t('common.unknown') },
                  { label: t('lab.device.rawAvailable'), value: <Badge tone="neutral">{device.data.rawAvailable ? t('common.yes') : t('common.no')}</Badge> },
                  { label: t('lab.device.stable'), value: <Badge tone={device.data.stable ? 'success' : 'warning'}>{device.data.stable ? t('common.yes') : t('common.no')}</Badge> },
                ]}
              />
              <p className="setting-note">
                <Fingerprint size={12} aria-hidden="true" /> {t('lab.device.stabilityNote')}
              </p>
            </Card>

            <Card
              title={t('lab.device.hashes')}
              subtitle={t('lab.device.hashesSubtitle')}
              actions={
                <Badge tone={device.data.stable ? 'success' : 'warning'}>
                  {device.data.stable ? t('lab.stable') : t('lab.unstable')}
                </Badge>
              }
            >
              {Object.keys(device.data.hashes).length === 0 ? (
                <EmptyState title={t('lab.device.noHashes')} />
              ) : (
                <>
                  <LabFacts
                    rows={Object.entries(device.data.hashes).map(([key, value]) => ({
                      label: t('lab.device.hashLabel', { key: key.toUpperCase() }),
                      value: <span className="mono cell-truncate">{value}</span>,
                    }))}
                  />
                  <p className="setting-note">
                    <Lock size={12} aria-hidden="true" /> {t('lab.device.poolNote')}
                  </p>
                </>
              )}
            </Card>

            <Card title={t('lab.device.claims')} subtitle={t('lab.device.claimsSubtitle')} className="lab-span-2">
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
              title={t('lab.device.emptyTitle')}
              description={t('lab.device.emptyDesc')}
              icon={<Fingerprint size={20} aria-hidden="true" />}
            />
          </Card>
        )
      ) : null}
    </div>
  )
}
