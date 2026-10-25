import { useEffect, useRef, useState } from 'react'
import { api } from '../../lib/api'
import type { EventStreamHandle } from '../../lib/api'
import { formatDateTimeSeconds, formatTime } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type { EventRecord, EventStreamState } from '../../lib/types'
import { Badge, Card } from '../primitives'
import './EventsFeed.css'

/** Rows the panel keeps; a cold cursor may replay the daemon's whole ring. */
const MAX_EVENTS = 100
const MAX_DATA_KEYS = 3
const MAX_DATA_VALUE = 64

function compactValue(value: unknown): string {
  if (typeof value === 'string') return value
  if (value === null) return 'null'
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

function truncate(value: string): string {
  return value.length > MAX_DATA_VALUE ? `${value.slice(0, MAX_DATA_VALUE - 1)}…` : value
}

/** Up to three stable key=value pairs; the full payload stays in the title. */
function compactEventData(data?: Record<string, unknown>): string {
  if (!data) return ''
  return Object.keys(data)
    .sort()
    .slice(0, MAX_DATA_KEYS)
    .map((key) => `${key}=${truncate(compactValue(data[key]))}`)
    .join(' · ')
}

function fullEventData(data?: Record<string, unknown>): string {
  if (!data) return ''
  return Object.keys(data)
    .sort()
    .map((key) => `${key}=${compactValue(data[key])}`)
    .join('\n')
}

/**
 * Live event feed for the Logs page. It consumes /v1/events/stream through the
 * SSE client in `api`: the daemon replays from the last event id, then streams
 * live frames, and the client reconnects with exponential backoff from that
 * same cursor. A hidden tab closes the stream and reopens it from the cursor
 * when visible again. The newest 100 events are kept.
 */
export function EventsFeed() {
  const { t } = useI18n()
  const [events, setEvents] = useState<EventRecord[]>([])
  const [paused, setPaused] = useState(false)
  const [streamState, setStreamState] = useState<EventStreamState>('connecting')
  const [retryInSec, setRetryInSec] = useState<number | null>(null)
  const cursorRef = useRef('')
  const handleRef = useRef<EventStreamHandle | null>(null)

  useEffect(() => {
    let disposed = false

    const connect = () => {
      if (disposed) return
      handleRef.current?.close()
      handleRef.current = api.subscribeEvents({
        since: cursorRef.current,
        onEvent: (event) => {
          if (disposed) return
          cursorRef.current = event.id
          setEvents((current) => {
            if (current.some((row) => row.id === event.id)) return current
            return [event, ...current].slice(0, MAX_EVENTS)
          })
        },
        onState: (state, info) => {
          if (disposed) return
          setStreamState(state)
          setRetryInSec(
            info?.retryInMs ? Math.max(1, Math.ceil(info.retryInMs / 1_000)) : null,
          )
        },
      })
    }

    const onVisibility = () => {
      if (document.visibilityState === 'hidden') {
        setPaused(true)
        setStreamState('paused')
        handleRef.current?.close()
        handleRef.current = null
        return
      }
      setPaused(false)
      setStreamState('connecting')
      connect()
    }

    const hidden = document.visibilityState === 'hidden'
    setPaused(hidden)
    if (hidden) setStreamState('paused')
    else connect()
    document.addEventListener('visibilitychange', onVisibility)

    return () => {
      disposed = true
      document.removeEventListener('visibilitychange', onVisibility)
      handleRef.current?.close()
      handleRef.current = null
    }
  }, [])

  const badgeTone = paused
    ? 'neutral'
    : streamState === 'live'
      ? 'success'
      : streamState === 'offline'
        ? 'danger'
        : 'warning'
  const badgeKey = paused
    ? 'events.paused'
    : streamState === 'live'
      ? 'events.live'
      : streamState === 'offline'
        ? 'events.offline'
        : streamState === 'connecting'
          ? 'events.connecting'
          : 'events.reconnecting'
  const disconnected = streamState === 'reconnecting' || streamState === 'offline'

  return (
    <Card
      title={t('events.title')}
      subtitle={t('events.subtitle')}
      actions={
        <div className="events-feed__meta">
          <Badge tone={badgeTone}>{t(badgeKey)}</Badge>
          <span className="events-feed__count">
            {t('events.count', { count: events.length, max: MAX_EVENTS })}
          </span>
        </div>
      }
    >
      {disconnected ? (
        <p className="form-error" role="alert">
          {t('events.streamError')}
          {retryInSec !== null ? ` ${t('events.retryIn', { seconds: retryInSec })}` : ''}
        </p>
      ) : null}
      {events.length === 0 ? (
        <p className="events-feed__empty">
          {t(streamState === 'connecting' || streamState === 'reconnecting'
            ? 'events.loading'
            : 'events.empty')}
        </p>
      ) : (
        <ul className="events-feed__list" aria-label={t('events.listLabel')}>
          {events.map((event) => {
            const data = compactEventData(event.data)
            return (
              <li className="events-feed__row" key={event.id}>
                <span className="events-feed__type mono" title={event.id}>
                  {event.type}
                </span>
                <time
                  className="events-feed__time"
                  dateTime={event.ts}
                  title={formatDateTimeSeconds(event.ts)}
                >
                  {formatTime(event.ts)}
                </time>
                <span
                  className="events-feed__data mono"
                  title={fullEventData(event.data) || undefined}
                >
                  {data}
                </span>
              </li>
            )
          })}
        </ul>
      )}
    </Card>
  )
}
