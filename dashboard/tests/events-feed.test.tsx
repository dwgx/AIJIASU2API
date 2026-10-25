import { act, render, screen, waitFor, within } from '@testing-library/react'
import type { Mock } from 'vitest'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { EventsFeed } from '../src/components/events/EventsFeed'
import { api } from '../src/lib/api'
import type { SubscribeEventsOptions } from '../src/lib/api'
import { LanguageProvider } from '../src/lib/i18n'
import type { EventRecord, WebhookEvent } from '../src/lib/types'

function event(
  id: string,
  type: WebhookEvent,
  ts: string,
  data: Record<string, unknown> = {},
): EventRecord {
  return { id, type, ts, data }
}

function renderFeed() {
  return render(
    <LanguageProvider>
      <EventsFeed />
    </LanguageProvider>,
  )
}

/** Replaces the transport and records every subscription the feed opens. */
function captureSubscriptions(): {
  calls: SubscribeEventsOptions[]
  closes: Mock[]
} {
  const calls: SubscribeEventsOptions[] = []
  const closes: Mock[] = []
  vi.spyOn(api, 'subscribeEvents').mockImplementation((options) => {
    calls.push(options)
    const close = vi.fn()
    closes.push(close)
    return { close, lastEventId: () => '' }
  })
  return { calls, closes }
}

let visibility: DocumentVisibilityState = 'visible'

beforeEach(async () => {
  localStorage.clear()
  visibility = 'visible'
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    get: () => visibility,
  })
  await api.resetMock()
})

describe('events feed', () => {
  it('replays newest-first, keeps 100 rows and dedupes repeated ids', async () => {
    const { calls } = captureSubscriptions()
    renderFeed()
    await waitFor(() => expect(calls).toHaveLength(1))

    const events = Array.from({ length: 105 }, (_, index) =>
      event(
        `evt-${String(index + 1).padStart(6, '0')}`,
        'node.connected',
        new Date(Date.UTC(2026, 9, 25, 10, 0, index)).toISOString(),
        { nodeId: `node-${index + 1}` },
      ),
    )
    act(() => {
      calls[0]!.onState?.('live', { attempt: 0, lastEventId: '' })
      for (const record of events) calls[0]!.onEvent(record)
    })

    const list = await screen.findByRole('list', { name: 'Recent events' })
    await waitFor(() => expect(within(list).getAllByRole('listitem')).toHaveLength(100))
    const rows = within(list).getAllByRole('listitem')
    expect(rows[0]).toHaveTextContent('node-105')
    expect(rows[99]).toHaveTextContent('node-6')
    expect(screen.getByText('100 / 100 shown')).toBeInTheDocument()

    act(() => calls[0]!.onEvent(events.at(-1)!))
    expect(within(list).getAllByRole('listitem')).toHaveLength(100)
  })

  it('pauses while the tab is hidden and resumes from the last event id', async () => {
    const { calls, closes } = captureSubscriptions()
    renderFeed()
    await waitFor(() => expect(calls).toHaveLength(1))

    act(() => {
      calls[0]!.onState?.('live', { attempt: 0, lastEventId: '' })
      calls[0]!.onEvent(
        event('evt-000001', 'node.connected', '2026-10-25T10:00:00Z', { nodeId: 'node-01' }),
      )
    })
    expect(await screen.findByText('Live')).toBeInTheDocument()

    act(() => {
      visibility = 'hidden'
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(await screen.findByText('Paused')).toBeInTheDocument()
    expect(closes[0]).toHaveBeenCalled()

    act(() => {
      visibility = 'visible'
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await waitFor(() => expect(calls).toHaveLength(2))
    expect(calls[1]!.since).toBe('evt-000001')
  })

  it('shows the reconnecting state and the retry delay, then recovers', async () => {
    const { calls } = captureSubscriptions()
    renderFeed()
    await waitFor(() => expect(calls).toHaveLength(1))

    act(() =>
      calls[0]!.onState?.('reconnecting', {
        attempt: 2,
        retryInMs: 4_000,
        lastEventId: 'evt-000001',
      }),
    )
    expect(screen.getByText('Reconnecting')).toBeInTheDocument()
    expect(screen.getByText(/Retry in 4s/)).toBeInTheDocument()

    act(() => calls[0]!.onState?.('live', { attempt: 0, lastEventId: 'evt-000001' }))
    expect(screen.getByText('Live')).toBeInTheDocument()
    expect(screen.queryByText(/Retry in/)).not.toBeInTheDocument()
  })

  it('serves the deterministic mock stream in mock mode', async () => {
    renderFeed()

    const list = await screen.findByRole('list', { name: 'Recent events' })
    await waitFor(() => expect(within(list).getAllByRole('listitem')).toHaveLength(12))
    expect(screen.getByText('12 / 100 shown')).toBeInTheDocument()
    expect(screen.getByText('Live')).toBeInTheDocument()
  })

  it('renders the feed in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    renderFeed()

    expect(
      await screen.findByRole('heading', { name: '实时事件', level: 2 }),
    ).toBeInTheDocument()
    expect(screen.getByText('实时')).toBeInTheDocument()
    expect(await screen.findByText('显示 12 / 100 条')).toBeInTheDocument()
    expect(screen.getByRole('list', { name: '最近事件' })).toBeInTheDocument()
  })

  it('mounts above the audit table on the Logs page', async () => {
    window.history.pushState({}, '', '/logs')
    render(<App />)

    const feed = await screen.findByRole('heading', { name: 'Live events', level: 2 })
    const table = await screen.findByRole('table', { name: 'Audit log' })
    expect(feed.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})
