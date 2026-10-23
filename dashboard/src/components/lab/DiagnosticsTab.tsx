import { RefreshCw, Stethoscope } from 'lucide-react'
import { api } from '../../lib/api'
import { formatBytes, formatDateTime } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { healthTone } from '../../lib/lab'
import { useAsync } from '../../lib/useAsync'
import { Badge, Button, Card, ErrorState, TableSkeleton } from '../primitives'
import { DisabledCallout, LabFacts, WarningList } from './shared'

export function DiagnosticsTab() {
  const { t } = useI18n()
  const diagnostics = useAsync(() => api.getLabDiagnostics(), [], { intervalMs: 15_000 })
  const data = diagnostics.data

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">{t('lab.diagnostics.title')}</h2>
          <p className="lab-tab-subtitle">{t('lab.diagnostics.subtitle')}</p>
        </div>
        <div className="lab-tab-actions">
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={diagnostics.reload}>
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      {data?.enabled === false ? (
        <DisabledCallout
          title={t('lab.diagnostics.disabled')}
          reason={data.reason ?? t('lab.overview.disabledReason')}
        />
      ) : diagnostics.status === 'error' ? (
        <Card>
          <ErrorState message={diagnostics.error ?? t('common.unknownError')} onRetry={diagnostics.reload} />
        </Card>
      ) : diagnostics.status === 'loading' && !data ? (
        <Card title={t('lab.diagnostics.title')}>
          <TableSkeleton rows={6} columns={4} />
        </Card>
      ) : data ? (
        <>
          <Card title={t('lab.diagnostics.health')} subtitle={t('lab.diagnostics.healthSubtitle')}>
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
            <Card title={t('lab.diagnostics.state')} subtitle={t('lab.diagnostics.stateSubtitle')}>
              <LabFacts
                rows={[
                  { label: t('lab.diagnostics.path'), value: <span className="mono">{data.state.path || '—'}</span> },
                  {
                    label: t('lab.diagnostics.lastSaved'),
                    value: data.state.lastSavedAt ? formatDateTime(data.state.lastSavedAt) : t('common.never'),
                  },
                  {
                    label: t('lab.diagnostics.size'),
                    value: data.state.sizeBytes === null ? '—' : formatBytes(data.state.sizeBytes),
                  },
                ]}
              />
            </Card>

            <Card title={t('lab.diagnostics.build')} subtitle={t('lab.diagnostics.buildSubtitle')}>
              <LabFacts
                rows={[
                  { label: t('lab.overview.version'), value: <span className="mono">{data.build.version || t('common.unknown')}</span> },
                  { label: t('lab.diagnostics.go'), value: <span className="mono">{data.build.goVersion || t('common.unknown')}</span> },
                  { label: t('lab.overview.commit'), value: <span className="mono">{data.build.commit || t('common.notEmbedded')}</span> },
                ]}
              />
            </Card>
          </div>

          <Card title={t('lab.diagnostics.warnings')} subtitle={t('lab.diagnostics.warningsSubtitle')}>
            <WarningList warnings={data.warnings} />
          </Card>

          <p className="callout callout--muted">
            <Stethoscope size={12} aria-hidden="true" /> {t('lab.diagnostics.note')}
          </p>
        </>
      ) : null}
    </div>
  )
}
