import { Download, RefreshCw, ScrollText, Search } from 'lucide-react'
import { useMemo, useState } from 'react'
import { api } from '../lib/api'
import { formatDateTime, formatRelative } from '../lib/format'
import { SEVERITY } from '../lib/labels'
import type { AuditEvent, Severity } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { useToast } from '../components/Toast'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Input,
  PageHeader,
  SegmentedControl,
  Select,
  TableSkeleton,
} from '../components/primitives'

export default function Logs() {
  const audit = useAsync(() => api.listAudit(20), [], { intervalMs: 15_000 })
  const { push } = useToast()
  const [severity, setSeverity] = useState<'all' | Severity>('all')
  const [actor, setActor] = useState('all')
  const [query, setQuery] = useState('')

  const actors = useMemo(
    () => Array.from(new Set((audit.data ?? []).map((event) => event.actor))).sort(),
    [audit.data],
  )

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return (audit.data ?? []).filter((event) => {
      if (severity !== 'all' && event.severity !== severity) return false
      if (actor !== 'all' && event.actor !== actor) return false
      if (!needle) return true
      return `${event.action} ${event.target} ${event.detail}`.toLowerCase().includes(needle)
    })
  }, [actor, audit.data, query, severity])

  const exportLog = () => {
    push('Export is a placeholder — no file was written', 'default')
  }

  const columns: Array<Column<AuditEvent>> = [
    {
      key: 'time',
      label: 'Time',
      render: (event) => (
        <div className="cell-stack">
          <span className="cell-mono">{formatDateTime(event.ts)}</span>
          <span className="cell-sub">{formatRelative(event.ts)}</span>
        </div>
      ),
    },
    {
      key: 'severity',
      label: 'Severity',
      render: (event) => {
        const level = SEVERITY[event.severity]
        return <Badge tone={level.tone}>{level.label}</Badge>
      },
    },
    {
      key: 'actor',
      label: 'Actor',
      render: (event) => <span className="cell-mono">{event.actor}</span>,
    },
    {
      key: 'action',
      label: 'Action',
      render: (event) => <span className="cell-mono">{event.action}</span>,
    },
    {
      key: 'target',
      label: 'Target',
      render: (event) => <span>{event.target}</span>,
    },
    {
      key: 'detail',
      label: 'Detail',
      render: (event) => <span className="cell-sub cell-detail">{event.detail}</span>,
    },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title="Logs"
        subtitle="Control-plane audit trail. Secrets and request bodies are redacted by design."
        refresh={summarizeRefresh([audit])}
        actions={
          <>
            <Button variant="ghost" icon={<Download size={14} />} onClick={exportLog}>
              Export
            </Button>
            <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={audit.reload}>
              Refresh
            </Button>
          </>
        }
      />

      <Card bodyClassName="card__body--flush">
        <div className="toolbar">
          <div className="toolbar__search">
            <Search size={14} aria-hidden="true" />
            <Input
              aria-label="Search audit events"
              placeholder="Search action, target or detail"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <SegmentedControl
            label="Filter by severity"
            value={severity}
            onChange={setSeverity}
            options={[
              { value: 'all', label: 'All' },
              { value: 'info', label: 'Info' },
              { value: 'warn', label: 'Warn' },
              { value: 'error', label: 'Error' },
            ]}
          />
          <Select aria-label="Filter by actor" value={actor} onChange={(event) => setActor(event.target.value)}>
            <option value="all">All actors</option>
            {actors.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </Select>
          <span className="toolbar__count">
            {filtered.length} of {audit.data?.length ?? 0}
          </span>
        </div>

        {audit.status === 'error' ? (
          <ErrorState message={audit.error ?? 'Unknown error'} onRetry={audit.reload} />
        ) : audit.status === 'loading' && !audit.data ? (
          <TableSkeleton rows={10} columns={6} />
        ) : (
          <DataTable
            caption="Audit log"
            columns={columns}
            rows={filtered}
            rowKey={(event) => event.id}
            empty={
              <EmptyState
                title="No events match these filters"
                description="Try another severity or actor, or clear the search."
                icon={<ScrollText size={20} aria-hidden="true" />}
                action={
                  <Button
                    variant="secondary"
                    onClick={() => {
                      setSeverity('all')
                      setActor('all')
                      setQuery('')
                    }}
                  >
                    Clear filters
                  </Button>
                }
              />
            }
          />
        )}
      </Card>

      <p className="page-note">
        Export is a UI placeholder in this build. A production panel would stream the audit table as
        NDJSON from <code className="mono">GET /v1/audit?format=ndjson</code>.
      </p>
    </div>
  )
}
