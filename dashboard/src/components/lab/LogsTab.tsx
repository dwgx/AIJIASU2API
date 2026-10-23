import { Pause, Play, RefreshCw, ScrollText } from 'lucide-react'
import { useState } from 'react'
import { api } from '../../lib/api'
import { formatDateTime, formatRelative } from '../../lib/format'
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
      label: 'Time',
      render: (entry) => (
        <div className="cell-stack">
          <span className="cell-mono">{formatDateTime(entry.ts)}</span>
          <span className="cell-sub">{formatRelative(entry.ts)}</span>
        </div>
      ),
    },
    {
      key: 'level',
      label: 'Level',
      render: (entry) => <Badge tone={logTone(entry.level)}>{entry.level}</Badge>,
    },
    {
      key: 'component',
      label: 'Component',
      render: (entry) => <span className="cell-mono">{entry.component}</span>,
    },
    {
      key: 'message',
      label: 'Message',
      render: (entry) => (
        <div className="cell-stack">
          <span>{entry.msg}</span>
          {entry.errorClass ? <span className="cell-sub mono">{entry.errorClass}</span> : null}
        </div>
      ),
    },
    {
      key: 'account',
      label: 'Account',
      render: (entry) => <span className="cell-sub mono">{entry.accountId ?? '—'}</span>,
    },
    {
      key: 'node',
      label: 'Node',
      render: (entry) => <span className="cell-sub mono">{entry.nodeId ?? '—'}</span>,
    },
  ]

  return (
    <div className="stack-lg">
      <div className="lab-tab-head">
        <div>
          <h2 className="lab-tab-title">Runtime logs</h2>
          <p className="lab-tab-subtitle">
            Bounded, redacted daemon ring. Polling stops while paused; hidden tabs stop polling too.
          </p>
        </div>
        <div className="lab-tab-actions">
          <Button
            variant="secondary"
            icon={paused ? <Play size={14} /> : <Pause size={14} />}
            onClick={() => setPaused((value) => !value)}
          >
            {paused ? 'Resume' : 'Pause'}
          </Button>
          <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={logs.reload}>
            Refresh
          </Button>
        </div>
      </div>

      {disabled ? (
        <DisabledCallout
          title="Runtime log ring is unavailable"
          reason={logs.data?.reason ?? 'The daemon did not attach a log ring.'}
        />
      ) : (
        <>
          <Card bodyClassName="card__body--flush">
            <div className="toolbar">
              <Select aria-label="Filter by log level" value={level} onChange={(event) => setLevel(event.target.value)}>
                {LEVELS.map((option) => (
                  <option key={option} value={option}>
                    {option === 'all' ? 'All levels' : option}
                  </option>
                ))}
              </Select>
              <Select
                aria-label="Filter by component"
                value={component}
                onChange={(event) => setComponent(event.target.value)}
              >
                {COMPONENTS.map((option) => (
                  <option key={option} value={option}>
                    {option === 'all' ? 'All components' : option}
                  </option>
                ))}
              </Select>
              <Badge tone={paused ? 'warning' : 'success'}>{paused ? 'paused' : 'live · 2s'}</Badge>
              <span className="toolbar__count">
                {entries.length} entries{logs.data && logs.data.dropped > 0 ? ` · ${logs.data.dropped} dropped` : ''}
              </span>
            </div>

            {logs.status === 'error' ? (
              <ErrorState message={logs.error ?? 'Unknown error'} onRetry={logs.reload} />
            ) : logs.status === 'loading' && !logs.data ? (
              <TableSkeleton rows={8} columns={6} />
            ) : (
              <DataTable
                caption="Runtime log entries"
                columns={columns}
                rows={entries}
                rowKey={(entry) => `${entry.ts}-${entry.component}-${entry.msg}`}
                empty={
                  <EmptyState
                    title="No log entries match these filters"
                    description="Clear the level or component filter; the ring is bounded to 2000 entries."
                    icon={<ScrollText size={20} aria-hidden="true" />}
                  />
                }
              />
            )}
          </Card>

          <p className="callout callout--muted">
            Messages are redacted server-side. Passwords, tokens, session keys and full device identifiers
            never enter the ring.
          </p>
        </>
      )}
    </div>
  )
}
