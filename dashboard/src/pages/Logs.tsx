import { Download, RefreshCw, ScrollText, Search } from 'lucide-react'
import { useMemo, useState } from 'react'
import { api } from '../lib/api'
import { formatDateTime, formatRelative } from '../lib/format'
import { useI18n } from '../lib/i18n'
import { SEVERITY } from '../lib/labels'
import type { AuditEvent, Severity } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { useToast } from '../components/Toast'
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  Input,
  PageHeader,
  SegmentedControl,
  Select,
  StatusPill,
  TableSkeleton,
} from '../components/primitives'

export default function Logs() {
  const audit = useAsync(() => api.listAudit(20), [], { intervalMs: 15_000 })
  const { push } = useToast()
  const { lang, t } = useI18n()
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
    push(t('audit.exportPlaceholder'), 'default')
  }

  const columns: Array<Column<AuditEvent>> = [
    {
      key: 'time',
      label: t('audit.columnTime'),
      render: (event) => (
        <div className="cell-stack">
          <span className="cell-mono">{formatDateTime(event.ts)}</span>
          <span className="cell-sub">{formatRelative(event.ts, Date.now(), lang)}</span>
        </div>
      ),
    },
    {
      key: 'severity',
      label: t('audit.columnSeverity'),
      render: (event) => {
        const level = SEVERITY[event.severity]
        return <StatusPill status={level.status}>{t(level.labelKey)}</StatusPill>
      },
    },
    {
      key: 'actor',
      label: t('audit.columnActor'),
      render: (event) => <span className="cell-mono">{event.actor}</span>,
    },
    {
      key: 'action',
      label: t('audit.columnAction'),
      render: (event) => <span className="cell-mono">{event.action}</span>,
    },
    {
      key: 'target',
      label: t('audit.columnTarget'),
      render: (event) => <span>{event.target}</span>,
    },
    {
      key: 'detail',
      label: t('audit.columnDetail'),
      render: (event) => <span className="cell-sub cell-detail">{event.detail}</span>,
    },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('audit.title')}
        subtitle={t('audit.subtitle')}
        refresh={summarizeRefresh([audit])}
        actions={
          <>
            <Button variant="ghost" icon={<Download size={14} />} onClick={exportLog}>
              {t('audit.export')}
            </Button>
            <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={audit.reload}>
              {t('common.refresh')}
            </Button>
          </>
        }
      />

      <Card bodyClassName="card__body--flush">
        <div className="toolbar">
          <div className="toolbar__search">
            <Search size={14} aria-hidden="true" />
            <Input
              aria-label={t('audit.searchAria')}
              placeholder={t('audit.searchPlaceholder')}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <SegmentedControl
            label={t('audit.filterSeverity')}
            value={severity}
            onChange={setSeverity}
            options={[
              { value: 'all', label: t('common.all') },
              { value: 'info', label: t('state.info') },
              { value: 'warn', label: t('state.warn') },
              { value: 'error', label: t('state.error') },
            ]}
          />
          <Select aria-label={t('audit.filterActor')} value={actor} onChange={(event) => setActor(event.target.value)}>
            <option value="all">{t('audit.allActors')}</option>
            {actors.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </Select>
          <span className="toolbar__count">
            {t('common.of', { shown: filtered.length, total: audit.data?.length ?? 0 })}
          </span>
        </div>

        {audit.status === 'error' ? (
          <ErrorState message={audit.error ?? t('common.unknownError')} onRetry={audit.reload} />
        ) : audit.status === 'loading' && !audit.data ? (
          <TableSkeleton rows={10} columns={6} />
        ) : (
          <DataTable
            caption={t('audit.caption')}
            columns={columns}
            rows={filtered}
            rowKey={(event) => event.id}
            empty={
              <EmptyState
                title={t('audit.emptyTitle')}
                description={t('audit.emptyDesc')}
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
                    {t('common.clearFilters')}
                  </Button>
                }
              />
            }
          />
        )}
      </Card>

      <p className="page-note">{t('audit.note')}</p>
    </div>
  )
}
