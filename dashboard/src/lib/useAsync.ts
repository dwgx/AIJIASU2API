import { useCallback, useEffect, useRef, useState } from 'react'

export interface AsyncState<T> {
  status: 'loading' | 'ready' | 'error'
  data: T | undefined
  error: string | undefined
  reload: () => void
}

export function useAsync<T>(loader: () => Promise<T>, deps: readonly unknown[] = []): AsyncState<T> {
  const [status, setStatus] = useState<AsyncState<T>['status']>('loading')
  const [data, setData] = useState<T>()
  const [error, setError] = useState<string>()
  const [nonce, setNonce] = useState(0)
  const loaderRef = useRef(loader)
  loaderRef.current = loader

  const reload = useCallback(() => setNonce((value) => value + 1), [])

  useEffect(() => {
    let alive = true
    setStatus('loading')
    setError(undefined)
    loaderRef
      .current()
      .then((result) => {
        if (!alive) return
        setData(result)
        setStatus('ready')
      })
      .catch((cause: unknown) => {
        if (!alive) return
        setError(cause instanceof Error ? cause.message : String(cause))
        setStatus('error')
      })
    return () => {
      alive = false
    }
    // The caller owns the dependency list; the loader is read through a ref.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce])

  useEffect(() => {
    const onRefresh = () => reload()
    window.addEventListener('ajs:refresh', onRefresh)
    return () => window.removeEventListener('ajs:refresh', onRefresh)
  }, [reload])

  return { status, data, error, reload }
}
