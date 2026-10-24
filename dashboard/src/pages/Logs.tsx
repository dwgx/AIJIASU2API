import { Download, RefreshCw, ScrollText, Search } from 'lucide-react'
import { useMemo, useState } from 'react'
import { api } from '../lib/api'
import { formatDate, formatDateTime, formatRelative } from '../lib/format'
import { useI18n } from '../lib/i18n'
import type { TranslationParams } from '../lib/i18n'
import { SEVERITY } from '../lib/labels'
import type { AuditEvent, AuditExportQuery, Severity } from '../lib/types'
import { summarizeRefresh, useAsync } from '../lib/useAsync'
import { downloadBlob } from '../components/accounts/AccountExportDialog'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import { EventsFeed } from '../components/events/EventsFeed'
import { Pagination, usePagination } from '../components/Pagination'
import { useToast } from '../components/Toast'
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  Input,
  PageHeader,
  Select,
  StatusPill,
  TableSkeleton,
} from '../components/primitives'

/** The daemon's default CSV window when neither bound is sent. */
const EXPORT_DEFAULT_DAYS = 7

type Translate = (key: string, params?: TranslationParams) => string

/** The window a download actually covers, in the user's language. */
function exportWindowLabel(query: AuditExportQuery, t: Translate): string {
  if (!query.from && !query.to) {
    return t('audit.exportWindowLastDays', { days: EXPORT_DEFAULT_DAYS })
  }
  if (query.from && query.to) {
    return t('audit.exportWindowRange', {
      from: formatDate(query.from),
      to: formatDate(query.to),
    })
  }
  if (query.from) return t('audit.exportWindowSince', { from: formatDate(query.from) })
  return t('audit.exportWindowUntil', { to: formatDate(query.to ?? '') })
}

export default function Logs() {
  const audit = useAsync(() => api.listAudit(20), [], { intervalMs: 15_000 })
  const { push } = useToast()
  const { lang, t } = useI18n()
  const [severity, setSeverity] = useState<'all' | Severity>('all')
  const [actor, setActor] = useState('all')
  const [query, setQuery] = useState('')
  const [exporting, setExporting] = useState(false)
  const [exportTruncated, setExportTruncated] = useState(false)

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

  const pagination = usePagination({
    total: filtered.length,
    initialPageSize: 25,
    resetKey: JSON.stringify([severity, actor, query]),
  })

  const exportQuery: AuditExportQuery = severity === 'all' ? {} : { severity }
  const exportWindow = exportWindowLabel(exportQuery, t)

  const exportLog = async () => {
    setExporting(true)
    setExportTruncated(false)
    try {
      const download = await api.exportAuditCsv(exportQuery)
      downloadBlob(download.blob, download.filename)
      setExportTruncated(download.truncated)
      push(
        t(
          download.truncated ? 'audit.exportTruncated' : 'audit.exportDoneWindow',
          download.truncated ? undefined : { window: exportWindow },
        ),
        download.truncated ? 'danger' : 'success',
      )
    } catch (cause) {
      push(
        cause instanceof Error ? cause.message : t('audit.exportFailed'),
        'danger',
      )
    } finally {
      setExporting(false)
    }
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
            <Button
              variant="ghost"
              icon={<Download size={14} />}
              onClick={() => void exportLog()}
              loading={exporting}
              title={t('audit.exportWindowHint', { window: exportWindow })}
            >
              {t('audit.export')}
            </Button>
            <Button variant="secondary" icon={<RefreshCw size={14} />} onClick={audit.reload}>
              {t('common.refresh')}
            </Button>
          </>
        }
      />

      <EventsFeed />

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
          <Select
            variant="log"
            aria-label={t('audit.filterSeverity')}
            value={severity}
            onChange={(value) => setSeverity(value as 'all' | Severity)}
            options={[
              { value: 'all', label: t('common.all') },
              { value: 'info', label: t('state.info') },
              { value: 'warn', label: t('state.warn') },
              { value: 'error', label: t('state.error') },
            ]}
          />
          <Select
            variant="log"
            aria-label={t('audit.filterActor')}
            value={actor}
            onChange={setActor}
            options={[
              { value: 'all', label: t('audit.allActors') },
              ...actors.map((name) => ({ value: name, label: name })),
            ]}
          />
        </div>

        <p className="audit-export-window">
          {t('audit.exportWindowHint', { window: exportWindow })}
        </p>

        {exportTruncated ? (
          <p className="callout callout--warning audit-export-warning" role="alert">
            {t('audit.exportTruncated')}
          </p>
        ) : null}

        {audit.status === 'error' ? (
          <ErrorState message={audit.error ?? t('common.unknownError')} onRetry={audit.reload} />
        ) : audit.status === 'loading' && !audit.data ? (
          <TableSkeleton rows={10} columns={6} />
        ) : (
          <>
            <DataTable
              caption={t('audit.caption')}
              columns={columns}
              rows={pagination.slice(filtered)}
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
            <Pagination
              page={pagination.page}
              pageSize={pagination.pageSize}
              total={filtered.length}
              onPageChange={pagination.setPage}
              onPageSizeChange={pagination.setPageSize}
              label={t('audit.pagination')}
            />
          </>
        )}
      </Card>

      <p className="page-note">{t('audit.note')}</p>
    </div>
  )
}
