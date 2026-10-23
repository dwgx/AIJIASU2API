import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react'

export interface AsyncState<T> {
  status: 'loading' | 'ready' | 'error'
  data: T | undefined
  error: string | undefined
  /** Epoch ms of the last successful load; undefined before the first success. */
  updatedAt: number | undefined
  /** Epoch ms of the first failed refresh after a success; undefined while the data is fresh. */
  staleAt: number | undefined
  reload: () => void
}

export interface AsyncOptions {
  /** Poll interval in ms. 0 or undefined disables polling. */
  intervalMs?: number
  /** Polling switch; the initial load always runs. Defaults to true. */
  enabled?: boolean
}

export interface RefreshSummary {
  stale: boolean
  staleAt: number | undefined
  updatedAt: number | undefined
  error: string | undefined
}

interface Snapshot<T> {
  status: AsyncState<T>['status']
  data: T | undefined
  error: string | undefined
  updatedAt: number | undefined
  staleAt: number | undefined
}

const INITIAL_SNAPSHOT: Snapshot<never> = {
  status: 'loading',
  data: undefined,
  error: undefined,
  updatedAt: undefined,
  staleAt: undefined,
}

/* --------------------------------------------------------------------------
 * Global in-flight counter. It drives the 1 px mint progress line under the
 * topbar, so every polling hook feeds the same indicator without prop drilling.
 * ------------------------------------------------------------------------ */

let inFlight = 0
const activityListeners = new Set<() => void>()

function publishActivity() {
  for (const listener of activityListeners) listener()
}

function beginActivity(): () => void {
  inFlight += 1
  publishActivity()
  let finished = false
  return () => {
    if (finished) return
    finished = true
    inFlight -= 1
    publishActivity()
  }
}

function subscribeActivity(listener: () => void): () => void {
  activityListeners.add(listener)
  return () => {
    activityListeners.delete(listener)
  }
}

function activitySnapshot(): boolean {
  return inFlight > 0
}

/** True while any mounted `useAsync` request is in flight. */
export function useRefreshActivity(): boolean {
  return useSyncExternalStore(subscribeActivity, activitySnapshot, activitySnapshot)
}

/**
 * Loads `loader()` on mount and whenever `deps` change. With `intervalMs` the
 * hook polls on a timer, pauses while the tab is hidden, never stacks requests,
 * and keeps the last good data (marked stale) when a poll fails.
 */
export function useAsync<T>(
  loader: () => Promise<T>,
  deps: readonly unknown[] = [],
  options: AsyncOptions = {},
): AsyncState<T> {
  const intervalMs = options.intervalMs ?? 0
  const enabled = options.enabled ?? true

  const [snapshot, setSnapshot] = useState<Snapshot<T>>(INITIAL_SNAPSHOT as Snapshot<T>)
  const [nonce, setNonce] = useState(0)

  const loaderRef = useRef(loader)
  loaderRef.current = loader

  const generationRef = useRef(0)
  const inFlightRef = useRef(false)
  const queuedRef = useRef(false)
  const runRef = useRef<(queueIfBusy: boolean) => void>(() => {})

  const reload = useCallback(() => {
    // Restarts the effect: the in-flight request is invalidated and, if it is
    // still running, the fresh load is queued behind it instead of stacking.
    setNonce((value) => value + 1)
  }, [])

  useEffect(() => {
    const generation = ++generationRef.current
    const current = () => generationRef.current === generation

    const run = (queueIfBusy: boolean) => {
      if (!current()) return
      if (inFlightRef.current) {
        if (queueIfBusy) queuedRef.current = true
        return
      }
      inFlightRef.current = true
      const finishActivity = beginActivity()
      setSnapshot((prev) =>
        prev.data === undefined ? { ...prev, status: 'loading', error: undefined } : prev,
      )

      let request: Promise<T>
      try {
        request = loaderRef.current()
      } catch (cause) {
        request = Promise.reject(cause)
      }

      request
        .then(
          (result) => {
            if (!current()) return
            setSnapshot({
              status: 'ready',
              data: result,
              error: undefined,
              updatedAt: Date.now(),
              staleAt: undefined,
            })
          },
          (cause: unknown) => {
            if (!current()) return
            const message = cause instanceof Error ? cause.message : String(cause)
            setSnapshot((prev) =>
              prev.data === undefined
                ? { ...prev, status: 'error', error: message }
                : { ...prev, status: 'ready', error: message, staleAt: prev.staleAt ?? Date.now() },
            )
          },
        )
        .finally(() => {
          inFlightRef.current = false
          finishActivity()
          if (queuedRef.current) {
            queuedRef.current = false
            runRef.current(true)
          }
        })
    }

    runRef.current = run
    // The initial load (and every deps/manual reload) must happen even if a
    // previous generation is still in flight, so it queues instead of dropping.
    run(true)

    if (!enabled || intervalMs <= 0) {
      return () => {
        if (current()) generationRef.current += 1
      }
    }

    let timer: number | undefined

    const stopTimer = () => {
      if (timer !== undefined) {
        clearInterval(timer)
        timer = undefined
      }
    }

    const startTimer = () => {
      stopTimer()
      timer = window.setInterval(() => {
        // Belt and braces: a tick that lands while hidden does nothing.
        if (document.visibilityState === 'hidden') return
        runRef.current(false)
      }, intervalMs)
    }

    const onVisibilityChange = () => {
      if (document.visibilityState === 'hidden') {
        stopTimer()
        return
      }
      // Coming back to the tab: refresh once, then resume the cadence.
      runRef.current(false)
      startTimer()
    }

    if (document.visibilityState !== 'hidden') startTimer()
    document.addEventListener('visibilitychange', onVisibilityChange)

    return () => {
      stopTimer()
      document.removeEventListener('visibilitychange', onVisibilityChange)
      if (current()) generationRef.current += 1
    }
    // The caller owns the dependency list; the loader is read through a ref.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce, intervalMs, enabled])

  useEffect(() => {
    const onRefresh = () => reload()
    window.addEventListener('ajs:refresh', onRefresh)
    return () => window.removeEventListener('ajs:refresh', onRefresh)
  }, [reload])

  return { ...snapshot, reload }
}

/** Collapses several async states into the page-level stale summary. */
export function summarizeRefresh(
  states: ReadonlyArray<Pick<AsyncState<unknown>, 'staleAt' | 'updatedAt' | 'error'>>,
): RefreshSummary {
  let staleAt: number | undefined
  let updatedAt: number | undefined
  let error: string | undefined

  for (const state of states) {
    if (state.staleAt !== undefined && (staleAt === undefined || state.staleAt < staleAt)) {
      staleAt = state.staleAt
    }
    if (state.updatedAt !== undefined && (updatedAt === undefined || state.updatedAt < updatedAt)) {
      updatedAt = state.updatedAt
    }
    if (state.staleAt !== undefined && state.error) error = state.error
  }

  return { stale: staleAt !== undefined, staleAt, updatedAt, error }
}
