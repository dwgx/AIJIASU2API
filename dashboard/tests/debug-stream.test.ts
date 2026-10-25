import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { openDebugStream } from '../src/lib/api'
import type {
  DebugStreamHandle,
  DebugStreamInfo,
  DebugStreamOptions,
} from '../src/lib/api'
import type { DebugTrace, EventStreamState } from '../src/lib/types'

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

  emit(trace: DebugTrace) {
    const message = new MessageEvent(trace.kind, {
      data: JSON.stringify(trace),
      lastEventId: String(trace.seq),
    })
    const listeners = this.listeners.get(trace.kind)
    if (listeners && listeners.size > 0) {
      for (const listener of listeners) listener(message)
      return
    }
    this.onmessage?.(message)
  }
}

function trace(seq: number, kind = 'switch.step'): DebugTrace {
  return {
    seq,
    at: new Date(Date.UTC(2026, 9, 26, 7, 0, seq)).toISOString(),
    kind,
    accountId: 'acc-01',
    nodeId: 'node-01',
    slot: 1,
    generation: 1,
    state: 'ok',
  }
}

function harness(
  overrides: Partial<DebugStreamOptions> = {},
): {
  handle: DebugStreamHandle
  entries: DebugTrace[]
  states: Array<{ state: EventStreamState; info?: DebugStreamInfo }>
  restarts: number[]
} {
  const entries: DebugTrace[] = []
  const states: Array<{ state: EventStreamState; info?: DebugStreamInfo }> = []
  const restarts: number[] = []
  const handle = openDebugStream({
    eventSourceFactory: (url) => new FakeEventSource(url) as unknown as EventSource,
    onEntry: (entry) => entries.push(entry),
    onState: (state, info) => states.push({ state, info }),
    onRestart: () => restarts.push(1),
    initialRetryMs: 100,
    maxRetryMs: 400,
    ...overrides,
  })
  return { handle, entries, states, restarts }
}

beforeEach(() => {
  vi.useFakeTimers()
  FakeEventSource.instances = []
})

afterEach(() => {
  vi.useRealTimers()
})

describe('debug SSE client', () => {
  it('subscribes to the named kinds and delivers entries with their sequence', () => {
    const { handle, entries, states } = harness()
    expect(FakeEventSource.instances).toHaveLength(1)
    expect(FakeEventSource.instances[0]!.url).toBe('/v1/debug/stream')
    expect(states.at(-1)?.state).toBe('connecting')

    const source = FakeEventSource.instances[0]!
    source.open()
    expect(states.at(-1)?.state).toBe('live')

    source.emit(trace(7, 'switch.step'))
    source.emit(trace(8, 'gate.open'))
    expect(entries.map((entry) => entry.seq)).toEqual([7, 8])
    expect(handle.lastSeq()).toBe(8)

    // A replay of the same sequence is a duplicate, not a restart.
    source.emit(trace(8, 'gate.open'))
    expect(entries.map((entry) => entry.seq)).toEqual([7, 8])
  })

  it('reconnects from the last sequence and reports a restarted ring', async () => {
    const { entries, states, restarts } = harness()
    const first = FakeEventSource.instances[0]!
    first.open()
    first.emit(trace(7))

    first.fail()
    expect(states.at(-1)?.state).toBe('reconnecting')
    expect(states.at(-1)?.info?.retryInMs).toBe(100)
    await vi.advanceTimersByTimeAsync(100)

    const second = FakeEventSource.instances[1]!
    expect(second.url).toBe('/v1/debug/stream?since=7')
    second.open()
    second.emit(trace(8))
    expect(entries.map((entry) => entry.seq)).toEqual([7, 8])

    second.fail()
    await vi.advanceTimersByTimeAsync(200)
    const third = FakeEventSource.instances[2]!
    expect(third.url).toBe('/v1/debug/stream?since=8')

    // The new process starts at sequence 1: the first frame regresses and the
    // client reports a restart before delivering the new lifetime's entry.
    third.open()
    third.emit(trace(1))
    expect(restarts).toHaveLength(1)
    expect(entries.map((entry) => entry.seq)).toEqual([7, 8, 1])
    third.emit(trace(2))
    expect(entries.map((entry) => entry.seq)).toEqual([7, 8, 1, 2])
  })

  it('reports offline and stops when reconnect is disabled', () => {
    const { states } = harness({ reconnect: false })
    FakeEventSource.instances[0]!.fail()
    expect(states.at(-1)?.state).toBe('offline')

    vi.advanceTimersByTime(5_000)
    expect(FakeEventSource.instances).toHaveLength(1)
  })
})
