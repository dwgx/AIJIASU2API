import { Pause, Play, RefreshCw, ScrollText } from 'lucide-react'
import { useState } from 'react'
import { api } from '../../lib/api'
import { formatDateTime, formatRelative } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { logTone } from '../../lib/lab'
import type { LogEntry } from '../../lib/types'
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
import { DisabledCallout } from './shared'

const LEVELS = ['all', 'DEBUG', 'INFO', 'WARN', 'ERROR'] as const
const COMPONENTS = [
  'all',
  'api',
  'gate',
  'runtime',
  'store',
  'login',
  'publisher',
  'system',
  'http',
  'secrets',
  'state',
  'resin',
  'account',
  'ajs-accountd',
] as const

export function LogsTab() {
  const { lang, t } = useI18n()
  const [level, setLevel] = useState<string>('all')
  const [component, setComponent] = useState<string>('all')
  const [paused, setPaused] = useState(false)

  const logs = useAsync(
    () => api.getLabLogs({ level, component, limit: 200 }),
    [level, component],
    { intervalMs: paused ? 0 : 2_000 },
  )

  const disabled = logs.data?.enabled === false
  const entries = logs.data?.entries ?? []

  const columns: Array<Column<LogEntry>> = [
    {
      key: 'ts',
      label: t('lab.logs.time'),
      render: (entry) => (
        <div className="cell-stack">
          <span className="cell-mono">{formatDateTime(entry.ts)}</span>
          <span className="cell-sub">{formatRelative(entry.ts, Date.now(), lang)}</span>
        </div>
      ),
    },
    {
      key: 'level',
      label: t('lab.logs.level'),
      render: (entry) => <Badge tone={logTone(entry.level)}>{entry.level}</Badge>,
    },
    {
      key: 'component',
      label: t('lab.logs.component'),
      render: (entry) => <span className="cell-mono">{entry.component}</span>,
    },
    {
      key: 'message',
      label: t('lab.logs.message'),
      render: (entry) => (
        <div className="cell-stack">
          <span>{entry.msg}</span>
          {entry.errorClass ? <span className="cell-sub mono">{entry.errorClass}</span> : null}
        </div>
      ),
    },
    {
      key: 'account',
      label: t('lab.logs.account'),
      render: (entry) => <span className="cell-sub mono">{entry.accountId ?? '—'}</span>,
    },
    {
      key: 'node',
      label: t('lab.logs.node'),
      render: (entry) => <span className="cell-sub mono">{entry.nodeId ?? '—'}</span>,
    },
  ]

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">{t('lab.logs.title')}</h2>
          <p className="lab-tab-subtitle">{t('lab.logs.subtitle')}</p>
        </div>
        <div className="lab-tab-actions">
          <Button
            variant="secondary"
            icon={paused ? <Play size={14} /> : <Pause size={14} />}
            onClick={() => setPaused((value) => !value)}
          >
            {paused ? t('lab.logs.resume') : t('lab.logs.pause')}
          </Button>
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={logs.reload}>
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      {disabled ? (
        <DisabledCallout
          title={t('lab.logs.disabled')}
          reason={logs.data?.reason ?? t('lab.overview.flagsUnavailableReason')}
        />
      ) : (
        <>
          <Card bodyClassName="card__body--flush">
            <div className="toolbar">
              <Select aria-label={t('lab.logs.levelFilter')} value={level} onChange={(event) => setLevel(event.target.value)}>
                {LEVELS.map((option) => (
                  <option key={option} value={option}>
                    {option === 'all' ? t('lab.logs.allLevels') : option}
                  </option>
                ))}
              </Select>
              <Select
                aria-label={t('lab.logs.componentFilter')}
                value={component}
                onChange={(event) => setComponent(event.target.value)}
              >
                {COMPONENTS.map((option) => (
                  <option key={option} value={option}>
                    {option === 'all' ? t('lab.logs.allComponents') : option}
                  </option>
                ))}
              </Select>
              <Badge tone={paused ? 'warning' : 'success'}>
                {paused ? t('lab.paused') : t('lab.livePolling')}
              </Badge>
              <span className="toolbar__count">
                {t('lab.entriesCount', { count: entries.length })}
                {logs.data && logs.data.dropped > 0 ? t('lab.droppedCount', { count: logs.data.dropped }) : ''}
              </span>
            </div>

            {logs.status === 'error' ? (
              <ErrorState message={logs.error ?? t('common.unknownError')} onRetry={logs.reload} />
            ) : logs.status === 'loading' && !logs.data ? (
              <TableSkeleton rows={8} columns={6} />
            ) : (
              <DataTable
                caption={t('lab.logs.caption')}
                columns={columns}
                rows={entries}
                rowKey={(entry) => `${entry.ts}-${entry.component}-${entry.msg}`}
                empty={
                  <EmptyState
                    title={t('lab.logs.emptyTitle')}
                    description={t('lab.logs.emptyDesc')}
                    icon={<ScrollText size={20} aria-hidden="true" />}
                  />
                }
              />
            )}
          </Card>

          <p className="callout callout--muted">{t('lab.logs.note')}</p>
        </>
      )}
    </div>
  )
}
