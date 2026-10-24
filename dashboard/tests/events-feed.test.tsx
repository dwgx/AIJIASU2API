import { act, render, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { EventsFeed } from '../src/components/events/EventsFeed'
import { api } from '../src/lib/api'
import { LanguageProvider } from '../src/lib/i18n'
import type { EventRecord, EventsPage, EventsQuery, WebhookEvent } from '../src/lib/types'

function event(
  id: string,
  type: WebhookEvent,
  ts: string,
  data: Record<string, unknown> = {},
): EventRecord {
  return { id, type, ts, data }
}

function pageOf(events: EventRecord[], dropped = 0): EventsPage {
  return {
    events,
    nextCursor: events.at(-1)?.id ?? '',
    oldestCursor: events[0]?.id ?? '',
    dropped,
  }
}

function renderFeed() {
  return render(
    <LanguageProvider>
      <EventsFeed />
    </LanguageProvider>,
  )
}

beforeEach(async () => {
  localStorage.clear()
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    get: () => 'visible',
  })
  await api.resetMock()
})

describe('events feed', () => {
  it('replays, appends newest-first, and continues from the returned cursor', async () => {
    const calls: EventsQuery[] = []
    vi.spyOn(api, 'listEvents').mockImplementation(async (query = {}) => {
      calls.push(query)
      if (calls.length === 1) {
        return pageOf([
          event('evt-000001', 'node.connected', '2026-10-25T10:00:00Z', { nodeId: 'node-01' }),
          event('evt-000002', 'schedule.fired', '2026-10-25T10:01:00Z', { scheduleId: 'sch-01' }),
        ])
      }
      if (calls.length === 2) {
        return pageOf(
          [event('evt-000003', 'device.retired', '2026-10-25T10:02:00Z', { deviceId: 'dev-01' })],
          2,
        )
      }
      return pageOf([], 2)
    })

    renderFeed()

    const list = await screen.findByRole('list', { name: 'Recent events' })
    await waitFor(() => expect(within(list).getAllByRole('listitem')).toHaveLength(3))
    expect(calls[0]).toMatchObject({ since: '', wait: '20s', limit: 100 })
    expect(calls[1].since).toBe('evt-000002')

    const rows = within(list).getAllByRole('listitem')
    expect(rows[0]).toHaveTextContent('device.retired')
    expect(rows[1]).toHaveTextContent('schedule.fired')
    expect(rows[2]).toHaveTextContent('node.connected')
    expect(screen.getByText('2 dropped')).toBeInTheDocument()
    expect(screen.getByText('3 / 100 shown')).toBeInTheDocument()
  })

  it('keeps only the newest 100 rows', async () => {
    const events = Array.from({ length: 105 }, (_, index) =>
      event(
        `evt-${String(index + 1).padStart(6, '0')}`,
        'node.connected',
        new Date(Date.UTC(2026, 9, 25, 10, 0, index)).toISOString(),
        { nodeId: `node-${index + 1}` },
      ),
    )
    let served = false
    vi.spyOn(api, 'listEvents').mockImplementation(async () => {
      if (served) return pageOf([])
      served = true
      return pageOf(events)
    })

    renderFeed()

    const list = await screen.findByRole('list', { name: 'Recent events' })
    await waitFor(() => expect(within(list).getAllByRole('listitem')).toHaveLength(100))
    const rows = within(list).getAllByRole('listitem')
    expect(rows[0]).toHaveTextContent('node-105')
    expect(rows[99]).toHaveTextContent('node-6')
    expect(screen.getByText('100 / 100 shown')).toBeInTheDocument()
  })

  it('pauses while the tab is hidden and resumes from the last cursor', async () => {
    let visibility: DocumentVisibilityState = 'visible'
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => visibility,
    })
    const calls: EventsQuery[] = []
    vi.spyOn(api, 'listEvents').mockImplementation(async (query = {}) => {
      calls.push(query)
      if (calls.length === 1) {
        return pageOf([event('evt-000001', 'node.connected', '2026-10-25T10:00:00Z')])
      }
      return pageOf([])
    })

    renderFeed()
    await waitFor(() => expect(calls.length).toBe(2))
    expect(calls[1].since).toBe('evt-000001')

    act(() => {
      visibility = 'hidden'
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(await screen.findByText('Paused')).toBeInTheDocument()
    const pausedCalls = calls.length
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, 40)
    await promise
    expect(calls.length).toBe(pausedCalls)

    act(() => {
      visibility = 'visible'
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await waitFor(() => expect(calls.length).toBe(pausedCalls + 1))
    expect(calls.at(-1)?.since).toBe('evt-000001')
  })

  it('serves a deterministic mock stream and replays from nextCursor', async () => {
    const first = await api.listEvents()
    const second = await api.listEvents()

    expect(first.events).toHaveLength(12)
    expect(second.events.map((entry) => entry.id)).toEqual(first.events.map((entry) => entry.id))
    expect(first.events.map((entry) => entry.id)).toEqual(
      first.events.map((_, index) => `evt-${String(index + 1).padStart(6, '0')}`),
    )
    expect(first.events.map((entry) => entry.ts)).toEqual(
      [...first.events.map((entry) => entry.ts)].sort(),
    )
    expect(first.oldestCursor).toBe('evt-000001')
    expect(first.nextCursor).toBe('evt-000012')

    const after = await api.listEvents({ since: first.nextCursor })
    expect(after.events).toEqual([])
    expect(after.nextCursor).toBe('evt-000012')
  })

  it('renders the feed in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    let served = false
    vi.spyOn(api, 'listEvents').mockImplementation(async () => {
      if (served) return pageOf([])
      served = true
      return pageOf([
        event('evt-000001', 'node.connected', '2026-10-25T10:00:00Z', { nodeId: 'node-01' }),
      ])
    })

    renderFeed()

    expect(
      await screen.findByRole('heading', { name: '实时事件', level: 2 }),
    ).toBeInTheDocument()
    expect(screen.getByText('实时')).toBeInTheDocument()
    expect(await screen.findByText('显示 1 / 100 条')).toBeInTheDocument()
    expect(screen.getByRole('list', { name: '最近事件' })).toBeInTheDocument()
    expect(screen.getByText(/nodeId=node-01/)).toBeInTheDocument()
  })

  it('mounts above the audit table on the Logs page', async () => {
    window.history.pushState({}, '', '/logs')
    render(<App />)

    const feed = await screen.findByRole('heading', { name: 'Live events', level: 2 })
    const table = await screen.findByRole('table', { name: 'Audit log' })
    expect(feed.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})
