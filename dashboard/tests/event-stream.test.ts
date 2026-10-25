import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { openEventStream } from '../src/lib/api'
import type {
  EventStreamHandle,
  EventStreamInfo,
  EventStreamOptions,
} from '../src/lib/api'
import type { EventRecord, EventStreamState } from '../src/lib/types'

class FakeEventSource {
  static instances: FakeEventSource[] = []

  url: string
  readyState = 0
  closed = false
  onopen: ((event: Event) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  private listeners = new Map<string, Set<(event: Event) => void>>()

  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
  }

  addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    const listeners = this.listeners.get(type) ?? new Set()
    listeners.add(listener as EventListener)
    this.listeners.set(type, listeners)
  }

  removeEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    this.listeners.get(type)?.delete(listener as EventListener)
  }

  close() {
    this.closed = true
    this.readyState = 2
  }

  open() {
    this.readyState = 1
    this.onopen?.(new Event('open'))
  }

  fail() {
    this.onerror?.(new Event('error'))
  }

  emit(type: string, event: EventRecord) {
    const message = new MessageEvent(type, {
      data: JSON.stringify(event),
      lastEventId: event.id,
    })
    // A named SSE event reaches addEventListener listeners; onmessage only
    // fires for unnamed events, exactly like the browser EventSource.
    const listeners = this.listeners.get(type)
    if (listeners && listeners.size > 0) {
      for (const listener of listeners) listener(message)
      return
    }
    this.onmessage?.(message)
  }
}

function event(id: string): EventRecord {
  return {
    id,
    type: 'node.connected',
    ts: '2026-10-26T00:00:00Z',
    data: { nodeId: 'node-01' },
  }
}

function harness(
  overrides: Partial<EventStreamOptions> = {},
): {
  handle: EventStreamHandle
  events: EventRecord[]
  states: Array<{ state: EventStreamState; info?: EventStreamInfo }>
} {
  const events: EventRecord[] = []
  const states: Array<{ state: EventStreamState; info?: EventStreamInfo }> = []
  const handle = openEventStream({
    url: (since) => `/v1/events/stream${since ? `?since=${since}` : ''}`,
    eventSourceFactory: (url) => new FakeEventSource(url) as unknown as EventSource,
    onEvent: (record) => events.push(record),
    onState: (state, info) => states.push({ state, info }),
    initialRetryMs: 100,
    maxRetryMs: 400,
    ...overrides,
  })
  return { handle, events, states }
}

beforeEach(() => {
  vi.useFakeTimers()
  FakeEventSource.instances = []
})

afterEach(() => {
  vi.useRealTimers()
})

describe('SSE client', () => {
  it('reconnects from the last event id with exponential backoff', async () => {
    const { handle, events, states } = harness()
    expect(FakeEventSource.instances).toHaveLength(1)
    expect(FakeEventSource.instances[0]!.url).toBe('/v1/events/stream')
    expect(states.at(-1)?.state).toBe('connecting')

    const first = FakeEventSource.instances[0]!
    first.open()
    expect(states.at(-1)?.state).toBe('live')
    first.emit('node.connected', event('evt-000001'))
    expect(events.map((record) => record.id)).toEqual(['evt-000001'])

    first.fail()
    expect(first.closed).toBe(true)
    expect(states.at(-1)?.state).toBe('reconnecting')
    expect(states.at(-1)?.info?.retryInMs).toBe(100)

    await vi.advanceTimersByTimeAsync(99)
    expect(FakeEventSource.instances).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(FakeEventSource.instances).toHaveLength(2)

    const second = FakeEventSource.instances[1]!
    expect(second.url).toBe('/v1/events/stream?since=evt-000001')
    // No open between failures: the delay doubles.
    second.fail()
    expect(states.at(-1)?.info?.retryInMs).toBe(200)
    await vi.advanceTimersByTimeAsync(199)
    expect(FakeEventSource.instances).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(FakeEventSource.instances).toHaveLength(3)

    const third = FakeEventSource.instances[2]!
    expect(third.url).toBe('/v1/events/stream?since=evt-000001')
    third.open()
    expect(states.at(-1)?.state).toBe('live')
    third.emit('node.connected', event('evt-000002'))
    expect(events.map((record) => record.id)).toEqual(['evt-000001', 'evt-000002'])

    handle.close()
    await vi.advanceTimersByTimeAsync(5_000)
    expect(FakeEventSource.instances).toHaveLength(3)
  })

  it('drops the cursor after repeated failures so a restarted ring recovers', async () => {
    const { handle } = harness({ resetCursorAfter: 2 })
    const first = FakeEventSource.instances[0]!
    first.open()
    first.emit('node.connected', event('evt-000001'))

    first.fail()
    await vi.advanceTimersByTimeAsync(100)
    const second = FakeEventSource.instances[1]!
    expect(second.url).toBe('/v1/events/stream?since=evt-000001')

    second.fail()
    await vi.advanceTimersByTimeAsync(200)
    const third = FakeEventSource.instances[2]!
    expect(third.url).toBe('/v1/events/stream?since=evt-000001')

    third.fail()
    await vi.advanceTimersByTimeAsync(400)
    const fourth = FakeEventSource.instances[3]!
    expect(fourth.url).toBe('/v1/events/stream')

    handle.close()
  })

  it('reports offline and stops when reconnect is disabled', async () => {
    const { states } = harness({ reconnect: false })
    FakeEventSource.instances[0]!.fail()
    expect(states.at(-1)?.state).toBe('offline')

    await vi.advanceTimersByTimeAsync(5_000)
    expect(FakeEventSource.instances).toHaveLength(1)
  })
})
