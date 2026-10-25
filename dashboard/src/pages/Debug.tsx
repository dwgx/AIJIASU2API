import { Pause, Play } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../lib/api'
import type { DebugStreamHandle } from '../lib/api'
import { formatDateTimeSeconds, formatTime } from '../lib/format'
import { useI18n } from '../lib/i18n'
import { DEBUG_KINDS } from '../lib/types'
import type {
  Account,
  DebugTrace,
  DebugTracePage,
  EventStreamState,
  Status,
  Tone,
} from '../lib/types'
import { DataTable } from '../components/DataTable'
import type { Column } from '../components/DataTable'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  PageHeader,
  Select,
  StatusPill,
  TableSkeleton,
} from '../components/primitives'

/**
 * Rows the page keeps. The daemon's ring holds 500 entries; keeping the same
 * bound makes the notice true for the view as well as for the transport.
 */
const MAX_ROWS = 500

/** The daemon's documented ring capacity, used before the first snapshot. */
const DEFAULT_CAPACITY = 500

function stateTone(state?: string): Tone {
  switch (state) {
    case 'ok':
    case 'open':
      return 'success'
    case 'failed':
    case 'lost':
      return 'danger'
    case 'canceled':
      return 'warning'
    default:
      return 'neutral'
  }
}

/**
 * Live debugger over the daemon's bounded runtime trace.
 *
 * The page seeds from GET /v1/debug/trace (ring metadata and first paint), then
 * subscribes to /v1/debug/stream. The stream replays the retained window from
 * the oldest entry, so an entry recorded between the snapshot and the stream
 * is never skipped; rows are deduped by the monotonic sequence.
 *
 * Pausing freezes the visible window at the highest sequence seen. Incoming
 * entries keep accumulating behind the freeze, so resuming reveals them
 * without a reconnect or a lost position. A sequence regression on the first
 * frame after a reconnect means the daemon restarted: the old rows belong to
 * another process and are dropped.
 */
export default function Debug() {
  const { t } = useI18n()
  const [page, setPage] = useState<DebugTracePage | null>(null)
  const [entries, setEntries] = useState<DebugTrace[]>([])
  const [accounts, setAccounts] = useState<Account[]>([])
  const [loadState, setLoadState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [loadError, setLoadError] = useState<string | null>(null)
  const [reloadNonce, setReloadNonce] = useState(0)
  const [streamState, setStreamState] = useState<EventStreamState>('connecting')
  const [retryInSec, setRetryInSec] = useState<number | null>(null)
  const [paused, setPaused] = useState(false)
  const [frozenSeq, setFrozenSeq] = useState(0)
  const [restarted, setRestarted] = useState(false)
  const [accountFilter, setAccountFilter] = useState('')
  const [kindFilter, setKindFilter] = useState('')
  const seqRef = useRef(0)
  const handleRef = useRef<DebugStreamHandle | null>(null)

  useEffect(() => {
    let cancelled = false
    api
      .listAccounts()
      .then((rows) => {
        if (!cancelled) setAccounts(rows)
      })
      .catch(() => {
        // The filter falls back to raw account ids; the trace still streams.
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    let disposed = false
    seqRef.current = 0
    setEntries([])
    setPage(null)
    setLoadState('loading')
    setLoadError(null)
    setStreamState('connecting')
    setRetryInSec(null)
    setPaused(false)
    setFrozenSeq(0)
    setRestarted(false)

    const connect = () => {
      if (disposed) return
      handleRef.current?.close()
      handleRef.current = api.subscribeDebug({
        // Replay the retained ring from the oldest entry; a reconnect inside
        // the client resumes from the last sequence it delivered.
        since: 0,
        onEntry: (trace) => {
          if (disposed) return
          seqRef.current = Math.max(seqRef.current, trace.seq)
          setEntries((current) => {
            if (current.some((row) => row.seq === trace.seq)) return current
            return [trace, ...current].slice(0, MAX_ROWS)
          })
        },
        onState: (state, info) => {
          if (disposed) return
          setStreamState(state)
          setRetryInSec(
            info?.retryInMs ? Math.max(1, Math.ceil(info.retryInMs / 1_000)) : null,
          )
        },
        onRestart: () => {
          if (disposed) return
          seqRef.current = 0
          setEntries([])
          setPaused(false)
          setFrozenSeq(0)
          setRestarted(true)
        },
      })
    }

    const load = async () => {
      try {
        const snapshot = await api.getDebugTrace({ limit: 0 })
        if (disposed) return
        setPage(snapshot)
        setEntries(snapshot.entries.slice().reverse())
        seqRef.current = snapshot.nextSeq
        setLoadState('ready')
        if (!snapshot.enabled) {
          setStreamState('offline')
          return
        }
        connect()
      } catch (error) {
        if (disposed) return
        setLoadState('error')
        setLoadError(error instanceof Error ? error.message : String(error))
        setStreamState('offline')
      }
    }

    void load()

    return () => {
      disposed = true
      handleRef.current?.close()
      handleRef.current = null
    }
  }, [reloadNonce])

  const accountLabels = useMemo(
    () => new Map(accounts.map((account) => [account.id, account.label])),
    [accounts],
  )

  const visible = useMemo(
    () =>
      entries.filter((entry) => {
        if (paused && entry.seq > frozenSeq) return false
        if (accountFilter && entry.accountId !== accountFilter) return false
        if (kindFilter && entry.kind !== kindFilter) return false
        return true
      }),
    [entries, paused, frozenSeq, accountFilter, kindFilter],
  )

  const newSincePause = useMemo(() => {
    if (!paused) return 0
    return entries.filter((entry) => {
      if (entry.seq <= frozenSeq) return false
      if (accountFilter && entry.accountId !== accountFilter) return false
      if (kindFilter && entry.kind !== kindFilter) return false
      return true
    }).length
  }, [entries, paused, frozenSeq, accountFilter, kindFilter])
  const disabled = page?.enabled === false
  const capacity = page?.capacity || DEFAULT_CAPACITY

  const togglePause = () => {
    if (paused) {
      setPaused(false)
      setFrozenSeq(0)
      return
    }
    setFrozenSeq(seqRef.current)
    setPaused(true)
  }

  const stateKey = paused
    ? 'debug.paused'
    : streamState === 'live'
      ? 'debug.live'
      : streamState === 'offline'
        ? 'debug.offline'
        : streamState === 'connecting'
          ? 'debug.connecting'
          : 'debug.reconnecting'
  const streamStatus: Status = paused
    ? 'info'
    : streamState === 'live'
      ? 'ok'
      : streamState === 'offline'
        ? 'error'
        : 'warn'
  const disconnected = streamState === 'reconnecting' || streamState === 'offline'

  const accountOptions = [
    { value: '', label: t('debug.allAccounts') },
    ...accounts.map((account) => ({ value: account.id, label: account.label })),
  ]
  const kindOptions = [
    { value: '', label: t('debug.allKinds') },
    ...DEBUG_KINDS.map((kind) => ({ value: kind, label: kind })),
  ]

  const columns: Array<Column<DebugTrace>> = [
    {
      key: 'seq',
      label: t('debug.column.seq'),
      width: '64px',
      render: (entry) => <span className="cell-mono">#{entry.seq}</span>,
    },
    {
      key: 'time',
      label: t('debug.column.time'),
      render: (entry) => (
        <time className="cell-mono" dateTime={entry.at} title={formatDateTimeSeconds(entry.at)}>
          {formatTime(entry.at)}
        </time>
      ),
    },
    {
      key: 'kind',
      label: t('debug.column.kind'),
      render: (entry) => (
        <div className="cell-stack">
          <span className="cell-mono">{entry.kind}</span>
          {entry.state ? <Badge tone={stateTone(entry.state)}>{entry.state}</Badge> : null}
        </div>
      ),
    },
    {
      key: 'account',
      label: t('debug.column.account'),
      render: (entry) => (
        <span className="cell-sub">
          {entry.accountId ? (accountLabels.get(entry.accountId) ?? entry.accountId) : '—'}
        </span>
      ),
    },
    {
      key: 'slot',
      label: t('debug.column.slot'),
      render: (entry) => (
        <span className="cell-mono">{entry.slot ? entry.slot : '—'}</span>
      ),
    },
    {
      key: 'generation',
      label: t('debug.column.generation'),
      render: (entry) => (
        <span className="cell-mono">{entry.generation ? entry.generation : '—'}</span>
      ),
    },
    {
      key: 'node',
      label: t('debug.column.node'),
      render: (entry) => <span className="cell-mono">{entry.nodeId ?? '—'}</span>,
    },
    {
      key: 'duration',
      label: t('debug.column.duration'),
      align: 'right',
      render: (entry) => (
        <span className="cell-mono">
          {entry.durationMs !== undefined
            ? t('debug.durationMs', { ms: entry.durationMs })
            : '—'}
        </span>
      ),
    },
    {
      key: 'detail',
      label: t('debug.column.detail'),
      render: (entry) => (
        <div className="cell-stack">
          <span>{entry.detail ?? '—'}</span>
          {entry.step ? <span className="cell-sub mono">{entry.step}</span> : null}
          {entry.errorClass ? (
            <span className="cell-sub mono">{entry.errorClass}</span>
          ) : null}
        </div>
      ),
    },
  ]

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('debug.title')}
        subtitle={t('debug.subtitle')}
        actions={
          <>
            <span role="status">
              <StatusPill status={streamStatus} pulse={!paused && streamState === 'live'}>
                {t(stateKey)}
              </StatusPill>
            </span>
            <Button
              variant="secondary"
              icon={paused ? <Play size={14} /> : <Pause size={14} />}
              onClick={togglePause}
              disabled={loadState !== 'ready' || disabled}
            >
              {paused ? t('debug.resume') : t('debug.pause')}
            </Button>
          </>
        }
      />

      <p className="callout callout--muted">
        {t('debug.notice', { capacity })}
        {page ? ` ${t('debug.ring', {
          retained: entries.length,
          capacity,
          evicted: page.evicted,
        })}` : ''}
      </p>

      {restarted ? (
        <p className="callout callout--warning" role="status">
          {t('debug.restarted')}
        </p>
      ) : null}

      {disconnected && loadState === 'ready' && !disabled ? (
        <p className="form-error" role="alert">
          {t('debug.streamError')}
          {retryInSec !== null ? ` ${t('debug.retryIn', { seconds: retryInSec })}` : ''}
        </p>
      ) : null}

      {loadState === 'error' ? (
        <Card>
          <ErrorState
            message={loadError ?? t('common.unknownError')}
            onRetry={() => setReloadNonce((value) => value + 1)}
          />
        </Card>
      ) : loadState === 'loading' ? (
        <Card bodyClassName="card__body--flush">
          <TableSkeleton rows={8} columns={9} />
        </Card>
      ) : disabled ? (
        <p className="callout callout--warning">{t('debug.disabled')}</p>
      ) : (
        <Card bodyClassName="card__body--flush">
          <div className="toolbar">
            <Select
              variant="log"
              aria-label={t('debug.accountFilter')}
              value={accountFilter}
              onChange={setAccountFilter}
              options={accountOptions}
              searchable
            />
            <Select
              variant="log"
              aria-label={t('debug.kindFilter')}
              value={kindFilter}
              onChange={setKindFilter}
              options={kindOptions}
            />
            <span className="toolbar__count">
              {t('debug.count', { shown: visible.length, total: entries.length })}
            </span>
            {paused && newSincePause > 0 ? (
              <Badge tone="info">{t('debug.newSincePause', { count: newSincePause })}</Badge>
            ) : null}
          </div>
          <DataTable
            caption={t('debug.listLabel')}
            columns={columns}
            rows={visible}
            rowKey={(entry) => String(entry.seq)}
            empty={
              <EmptyState
                title={t('debug.empty')}
                description={
                  paused
                    ? t('debug.paused')
                    : streamState === 'connecting' || streamState === 'reconnecting'
                      ? t('debug.loading')
                      : undefined
                }
              />
            }
          />
        </Card>
      )}
    </div>
  )
}
