import clsx from 'clsx'
import { CheckCircle2, Info, X, XCircle } from 'lucide-react'
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { useI18n } from '../lib/i18n'

export type ToastTone = 'default' | 'success' | 'danger'

interface ToastItem {
  id: number
  message: string
  tone: ToastTone
}

interface ToastContextValue {
  push: (message: string, tone?: ToastTone) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

export function ToastProvider({ children }: { children: ReactNode }) {
  const { t } = useI18n()
  const [items, setItems] = useState<ToastItem[]>([])
  const timers = useRef<number[]>([])
  const sequence = useRef(0)

  useEffect(() => {
    const pending = timers.current
    return () => {
      pending.forEach((timer) => window.clearTimeout(timer))
    }
  }, [])

  const dismiss = useCallback((id: number) => {
    setItems((current) => current.filter((item) => item.id !== id))
  }, [])

  const push = useCallback(
    (message: string, tone: ToastTone = 'default') => {
      sequence.current += 1
      const id = sequence.current
      setItems((current) => [...current.slice(-3), { id, message, tone }])
      timers.current.push(window.setTimeout(() => dismiss(id), 4200))
    },
    [dismiss],
  )

  const value = useMemo(() => ({ push }), [push])

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="toast-stack" aria-live="polite">
        {items.map((item) => (
          <div key={item.id} className={clsx('toast', `toast--${item.tone}`)} role="status">
            <span className="toast__icon" aria-hidden="true">
              {item.tone === 'success' ? (
                <CheckCircle2 size={16} />
              ) : item.tone === 'danger' ? (
                <XCircle size={16} />
              ) : (
                <Info size={16} />
              )}
            </span>
            <span className="toast__message">{item.message}</span>
            <button
              type="button"
              className="toast__close"
              aria-label={t('common.dismissNotification')}
              onClick={() => dismiss(item.id)}
            >
              <X size={13} />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastContextValue {
  const context = useContext(ToastContext)
  if (!context) throw new Error('useToast must be used inside ToastProvider')
  return context
}
