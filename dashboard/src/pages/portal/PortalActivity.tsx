import { useEffect, useState } from 'react'
import { DataTable } from '../../components/DataTable'
import type { Column } from '../../components/DataTable'
import { useToast } from '../../components/Toast'
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  TableSkeleton,
} from '../../components/primitives'
import { formatDateTimeSeconds } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import { portalApi } from '../../lib/portal-api'
import type { AuditEvent } from '../../lib/types'
import { portalErrorText } from './PortalFrame'

/**
 * One page asks for one row more than it renders. The extra row answers "is
 * there another page?" without a second request, and is not dropped: the next
 * call uses the last rendered id as the cursor, so the probe row is the first
 * row of the next page.
 */
const PAGE_SIZE = 25

interface AuditPage {
  events: AuditEvent[]
  cursor: string | null
  hasMore: boolean
}

function trimPage(rows: AuditEvent[]): AuditPage {
  const events = rows.slice(0, PAGE_SIZE)
  return {
    events,
    cursor: events.at(-1)?.id ?? null,
    hasMore: rows.length > PAGE_SIZE,
  }
}

/**
 * The customer activity tab. It renders exactly the rows the daemon returns:
 * the portal route has already filtered the audit ring to this tenant, so the
 * UI must not add client-side ownership guesses that could disagree with it.
 */
export default function PortalActivity() {
  const { t } = useI18n()
  const { push } = useToast()
  const [page, setPage] = useState<AuditPage>({ events: [], cursor: null, hasMore: false })
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading')
  const [failure, setFailure] = useState<unknown>(null)
  const [loadingMore, setLoadingMore] = useState(false)
  const [nonce, setNonce] = useState(0)

  useEffect(() => {
    let active = true
    setStatus('loading')
    setFailure(null)
    portalApi
      .audit({ limit: PAGE_SIZE + 1 })
      .then((rows) => {
        if (!active) return
        setPage(trimPage(rows))
        setStatus('ready')
      })
      .catch((cause: unknown) => {
        if (!active) return
        setFailure(cause)
        setStatus('error')
      })
    return () => {
      active = false
    }
  }, [nonce])

  const loadMore = async () => {
    if (!page.cursor || loadingMore) return
    setLoadingMore(true)
    try {
      const rows = await portalApi.audit({ limit: PAGE_SIZE + 1, before: page.cursor })
      const next = trimPage(rows)
      setPage((current) => ({
        events: [...current.events, ...next.events],
        cursor: next.cursor ?? current.cursor,
        hasMore: next.hasMore,
      }))
    } catch (cause) {
      // Keep the rows already on screen; the retry is another click.
      push(portalErrorText(cause, t), 'danger')
    } finally {
      setLoadingMore(false)
    }
  }

  const columns: Array<Column<AuditEvent>> = [
    {
      key: 'time',
      label: t('portal.activity.time'),
      width: '160px',
      render: (event) =>
        event.ts ? (
          <time className="mono" dateTime={event.ts}>
            {formatDateTimeSeconds(event.ts)}
          </time>
        ) : (
          <span className="mono">—</span>
        ),
    },
    {
      key: 'action',
      label: t('portal.activity.action'),
      render: (event) => <span className="mono">{event.action}</span>,
    },
    {
      key: 'target',
      label: t('portal.activity.target'),
      render: (event) => <span className="mono">{event.target}</span>,
    },
    {
      key: 'detail',
      label: t('portal.activity.detail'),
      render: (event) => event.detail,
    },
  ]

  return (
    <Card title={t('portal.activity.title')} subtitle={t('portal.activity.subtitle')}>
      {status === 'loading' ? <TableSkeleton rows={6} columns={4} /> : null}
      {status === 'error' ? (
        <ErrorState
          message={portalErrorText(failure, t)}
          onRetry={() => setNonce((value) => value + 1)}
        />
      ) : null}
      {status === 'ready' ? (
        <>
          <DataTable
            caption={t('portal.activity.caption')}
            columns={columns}
            rows={page.events}
            rowKey={(event) => event.id}
            empty={
              <EmptyState
                title={t('portal.activity.empty')}
                description={t('portal.activity.emptyHint')}
              />
            }
          />
          {page.hasMore ? (
            <div className="portal-actions">
              <Button variant="secondary" loading={loadingMore} onClick={loadMore}>
                {loadingMore ? t('portal.activity.loadingMore') : t('portal.activity.loadMore')}
              </Button>
            </div>
          ) : null}
        </>
      ) : null}
    </Card>
  )
}
