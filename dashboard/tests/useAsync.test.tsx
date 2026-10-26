import { act, render, renderHook, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PageHeader } from '../src/components/primitives'
import { useAsync, useRefreshActivity } from '../src/lib/useAsync'

let visibility: DocumentVisibilityState = 'visible'

function setVisibility(next: DocumentVisibilityState) {
  visibility = next
  act(() => {
    document.dispatchEvent(new Event('visibilitychange'))
  })
}

/** Flushes pending microtasks (loaders resolve outside the timer queue). */
async function settle() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

function deferred<T>() {
  const { promise, resolve } = Promise.withResolvers<T>()
  return { promise, resolve }
}

beforeEach(() => {
  vi.useFakeTimers()
  visibility = 'visible'
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    get: () => visibility,
  })
})

afterEach(() => {
  vi.useRealTimers()
  delete (document as unknown as Record<string, unknown>).visibilityState
})

describe('useAsync polling', () => {
  it('polls at the configured interval', async () => {
    const loader = vi.fn(async () => 'snapshot')
    const { result } = renderHook(() => useAsync(loader, [], { intervalMs: 10_000 }))

    await settle()
    expect(loader).toHaveBeenCalledTimes(1)
    expect(result.current.status).toBe('ready')
    expect(result.current.updatedAt).toBeTypeOf('number')

    await advance(10_000)
    expect(loader).toHaveBeenCalledTimes(2)

    await advance(20_000)
    expect(loader).toHaveBeenCalledTimes(4)
  })

  it('does not poll when intervalMs is omitted or zero', async () => {
    const loader = vi.fn(async () => 'snapshot')
    renderHook(() => useAsync(loader, []))
    await settle()

    await advance(60_000)
    expect(loader).toHaveBeenCalledTimes(1)

    renderHook(() => useAsync(loader, [], { intervalMs: 0 }))
    await settle()
    await advance(60_000)
    expect(loader).toHaveBeenCalledTimes(2)
  })

  it('pauses while the tab is hidden', async () => {
    const loader = vi.fn(async () => 'snapshot')
    renderHook(() => useAsync(loader, [], { intervalMs: 5_000 }))
    await settle()
    expect(loader).toHaveBeenCalledTimes(1)

    setVisibility('hidden')
    await advance(20_000)
    expect(loader).toHaveBeenCalledTimes(1)
  })

  it('refreshes and resumes the cadence when the tab becomes visible', async () => {
    const loader = vi.fn(async () => 'snapshot')
    renderHook(() => useAsync(loader, [], { intervalMs: 5_000 }))
    await settle()

    setVisibility('hidden')
    await advance(15_000)
    expect(loader).toHaveBeenCalledTimes(1)

    setVisibility('visible')
    await settle()
    expect(loader).toHaveBeenCalledTimes(2)

    await advance(5_000)
    expect(loader).toHaveBeenCalledTimes(3)
  })

  it('stops polling after unmount', async () => {
    const loader = vi.fn(async () => 'snapshot')
    const { unmount } = renderHook(() => useAsync(loader, [], { intervalMs: 5_000 }))
    await settle()
    expect(loader).toHaveBeenCalledTimes(1)

    unmount()
    await advance(30_000)
    expect(loader).toHaveBeenCalledTimes(1)
  })

  it('dedupes polls that overlap an in-flight request', async () => {
    const gate = deferred<string>()
    const loader = vi.fn(() => gate.promise)
    const { result } = renderHook(() => useAsync(loader, [], { intervalMs: 1_000 }))
    await settle()
    expect(loader).toHaveBeenCalledTimes(1)

    await advance(5_000)
    expect(loader).toHaveBeenCalledTimes(1)

    await act(async () => {
      gate.resolve('first')
      await gate.promise
    })
    expect(result.current.data).toBe('first')

    await advance(1_000)
    expect(loader).toHaveBeenCalledTimes(2)
  })

  it('keeps last-good data and marks it stale when a poll fails', async () => {
    const failure = new Error('network down')
    const loader = vi
      .fn<() => Promise<{ count: number }>>()
      .mockResolvedValueOnce({ count: 1 })
      .mockRejectedValueOnce(failure)
      .mockResolvedValueOnce({ count: 2 })
    const { result } = renderHook(() => useAsync(loader, [], { intervalMs: 1_000 }))
    await settle()
    expect(result.current.data).toEqual({ count: 1 })
    expect(result.current.failure).toBeUndefined()

    await advance(1_000)
    expect(result.current.status).toBe('ready')
    expect(result.current.data).toEqual({ count: 1 })
    expect(result.current.error).toBe('network down')
    // The raw cause stays available so a page can branch on its type/reason.
    expect(result.current.failure).toBe(failure)
    expect(result.current.staleAt).toBeTypeOf('number')

    await advance(1_000)
    expect(result.current.data).toEqual({ count: 2 })
    expect(result.current.error).toBeUndefined()
    expect(result.current.failure).toBeUndefined()
    expect(result.current.staleAt).toBeUndefined()
  })

  it('stays ready while a refresh is in flight so no skeleton flashes', async () => {
    const gate = deferred<string>()
    const loader = vi.fn().mockResolvedValueOnce('first').mockReturnValueOnce(gate.promise)
    const { result } = renderHook(() => useAsync(loader, [], { intervalMs: 1_000 }))
    await settle()
    expect(result.current.status).toBe('ready')

    await advance(1_000)
    expect(loader).toHaveBeenCalledTimes(2)
    expect(result.current.status).toBe('ready')
    expect(result.current.data).toBe('first')

    await act(async () => {
      gate.resolve('second')
      await gate.promise
    })
    await settle()
    expect(result.current.data).toBe('second')
  })

  it('refetches on manual reload and restarts the interval', async () => {
    const loader = vi.fn(async () => 'snapshot')
    const { result } = renderHook(() => useAsync(loader, [], { intervalMs: 10_000 }))
    await settle()
    expect(loader).toHaveBeenCalledTimes(1)

    await advance(6_000)
    act(() => {
      result.current.reload()
    })
    await settle()
    expect(loader).toHaveBeenCalledTimes(2)

    await advance(6_000)
    expect(loader).toHaveBeenCalledTimes(2)

    await advance(4_000)
    expect(loader).toHaveBeenCalledTimes(3)
  })

  it('loads the new dependency generation after an in-flight request settles', async () => {
    const gate = deferred<string>()
    const loader = vi.fn((key: string) => (key === 'a' ? gate.promise : Promise.resolve('b')))
    const { result, rerender } = renderHook(
      ({ key }: { key: string }) => useAsync(() => loader(key), [key]),
      { initialProps: { key: 'a' } },
    )
    await settle()
    expect(loader).toHaveBeenCalledTimes(1)
    expect(result.current.status).toBe('loading')

    rerender({ key: 'b' })
    await settle()
    expect(loader).toHaveBeenCalledTimes(1)

    await act(async () => {
      gate.resolve('a')
      await gate.promise
    })
    await settle()
    expect(loader).toHaveBeenCalledTimes(2)
    expect(result.current.status).toBe('ready')
    expect(result.current.data).toBe('b')
  })

  it('reports in-flight activity for the topbar progress line', async () => {
    const gate = deferred<string>()

    function Probe() {
      const busy = useRefreshActivity()
      useAsync(() => gate.promise, [])
      return <span data-testid="activity">{busy ? 'busy' : 'idle'}</span>
    }

    render(<Probe />)
    await settle()
    expect(screen.getByTestId('activity')).toHaveTextContent('busy')

    await act(async () => {
      gate.resolve('done')
      await gate.promise
    })
    await settle()
    expect(screen.getByTestId('activity')).toHaveTextContent('idle')
  })

  it('loads once without polling when disabled', async () => {
    const loader = vi.fn(async () => 'snapshot')
    renderHook(() => useAsync(loader, [], { intervalMs: 5_000, enabled: false }))
    await settle()
    expect(loader).toHaveBeenCalledTimes(1)

    await advance(30_000)
    expect(loader).toHaveBeenCalledTimes(1)
  })
})

describe('PageHeader stale indicator', () => {
  it('renders the warning dot only while the data is stale', () => {
    const updatedAt = Date.UTC(2026, 9, 23, 14, 3, 12)
    const { rerender } = render(
      <PageHeader
        title="Accounts"
        refresh={{ stale: false, staleAt: undefined, updatedAt, error: undefined }}
      />,
    )
    expect(screen.queryByRole('img', { name: /last-good/i })).toBeNull()

    rerender(
      <PageHeader
        title="Accounts"
        refresh={{ stale: true, staleAt: updatedAt + 15_000, updatedAt, error: 'network down' }}
      />,
    )
    const dot = screen.getByRole('img', { name: /last-good/i })
    expect(dot).toHaveAttribute('title', expect.stringContaining('network down'))
    expect(dot).toHaveAttribute('title', expect.stringContaining('last updated'))
  })
})
