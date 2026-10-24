import { useEffect, useRef, useState } from 'react'
import { api } from '../../lib/api'
import { formatDateTimeSeconds, formatTime } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type { EventRecord, EventsPage } from '../../lib/types'
import { Badge, Card } from '../primitives'
import './EventsFeed.css'

/** Rows the panel keeps; the daemon page size matches it so one poll can fill it. */
const MAX_EVENTS = 100
/** The server caps a long poll below its 120 s write timeout. */
const LONG_POLL_WAIT = '20s'
/** A long poll already waited server-side; an empty answer just polls again. */
const EMPTY_RETRY_MS = 1_000
const ERROR_RETRY_MS = 3_000
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
 * Live event feed for the Logs page. It replays the daemon ring from the last
 * cursor, then long-polls /v1/events. A hidden tab aborts the in-flight poll
 * and resumes from the same cursor when it becomes visible again; the newest
 * 100 events are kept, and the daemon's dropped counter is surfaced verbatim.
 */
export function EventsFeed() {
  const { t } = useI18n()
  const [events, setEvents] = useState<EventRecord[]>([])
  const [dropped, setDropped] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [loaded, setLoaded] = useState(false)
  const [paused, setPaused] = useState(false)
  const cursorRef = useRef('')
  const runningRef = useRef(false)
  const timerRef = useRef<number | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    const clearTimer = () => {
      if (timerRef.current !== null) {
        window.clearTimeout(timerRef.current)
        timerRef.current = null
      }
    }

    const applyPage = (page: EventsPage) => {
      setLoaded(true)
      setDropped(page.dropped)
      setError(null)
      if (page.events.length > 0) {
        cursorRef.current = page.nextCursor || page.events[page.events.length - 1].id
        setEvents((current) => {
          const seen = new Set(current.map((event) => event.id))
          const fresh = page.events.filter((event) => !seen.has(event.id)).reverse()
          return fresh.length > 0 ? fresh.concat(current).slice(0, MAX_EVENTS) : current
        })
      } else if (page.nextCursor) {
        cursorRef.current = page.nextCursor
      }
    }

    const poll = async () => {
      if (!runningRef.current) return
      const controller = new AbortController()
      abortRef.current = controller
      try {
        const page = await api.listEvents(
          { since: cursorRef.current, wait: LONG_POLL_WAIT, limit: MAX_EVENTS },
          { signal: controller.signal },
        )
        if (!runningRef.current || controller.signal.aborted) return
        applyPage(page)
        timerRef.current = window.setTimeout(
          () => void poll(),
          page.events.length > 0 ? 0 : EMPTY_RETRY_MS,
        )
      } catch (cause) {
        if (!runningRef.current || controller.signal.aborted) return
        setLoaded(true)
        setError(cause instanceof Error ? cause.message : t('events.failed'))
        timerRef.current = window.setTimeout(() => void poll(), ERROR_RETRY_MS)
      }
    }

    const start = () => {
      if (runningRef.current) return
      runningRef.current = true
      void poll()
    }

    const stop = () => {
      runningRef.current = false
      abortRef.current?.abort()
      abortRef.current = null
      clearTimer()
    }

    const onVisibility = () => {
      if (document.visibilityState === 'hidden') {
        setPaused(true)
        stop()
      } else {
        setPaused(false)
        start()
      }
    }

    const hidden = document.visibilityState === 'hidden'
    setPaused(hidden)
    if (hidden) stop()
    else start()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      stop()
    }
  }, [t])

  return (
    <Card
      title={t('events.title')}
      subtitle={t('events.subtitle')}
      actions={
        <div className="events-feed__meta">
          {dropped > 0 ? (
            <Badge tone="warning">{t('events.dropped', { count: dropped })}</Badge>
          ) : null}
          <Badge tone={paused ? 'neutral' : error ? 'warning' : 'success'}>
            {t(paused ? 'events.paused' : error ? 'events.reconnecting' : 'events.live')}
          </Badge>
          <span className="events-feed__count">
            {t('events.count', { count: events.length, max: MAX_EVENTS })}
          </span>
        </div>
      }
    >
      {error ? (
        <p className="form-error" role="alert">
          {error}
        </p>
      ) : null}
      {events.length === 0 ? (
        <p className="events-feed__empty">
          {error ? t('events.failed') : t(loaded ? 'events.empty' : 'events.loading')}
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
