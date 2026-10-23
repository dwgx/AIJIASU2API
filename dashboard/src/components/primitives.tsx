import clsx from 'clsx'
import { AlertTriangle, Check, Inbox, Loader2, X } from 'lucide-react'
import { useEffect, useId, useRef } from 'react'
import type {
  ButtonHTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from 'react'
import type { Tone } from '../lib/types'
import { formatTime } from '../lib/format'
import { useI18n } from '../lib/i18n'
import type { RefreshSummary } from '../lib/useAsync'

export type { Tone }

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger'

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  size?: 'sm' | 'md'
  icon?: ReactNode
  loading?: boolean
}

export function Button({
  variant = 'secondary',
  size = 'md',
  icon,
  loading = false,
  className,
  children,
  disabled,
  type = 'button',
  ...rest
}: ButtonProps) {
  return (
    <button
      type={type}
      className={clsx('btn', `btn--${variant}`, size === 'sm' && 'btn--sm', className)}
      disabled={disabled || loading}
      {...rest}
    >
      {loading ? <Loader2 size={14} className="spin" aria-hidden="true" /> : icon}
      {children ? <span>{children}</span> : null}
    </button>
  )
}

interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  label: string
  icon: ReactNode
  variant?: ButtonVariant
  size?: 'sm' | 'md'
}

export function IconButton({
  label,
  icon,
  variant = 'ghost',
  size = 'md',
  className,
  type = 'button',
  ...rest
}: IconButtonProps) {
  return (
    <button
      type={type}
      aria-label={label}
      title={label}
      className={clsx('icon-btn', `icon-btn--${variant}`, size === 'sm' && 'icon-btn--sm', className)}
      {...rest}
    >
      {icon}
    </button>
  )
}

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={clsx('input', className)} {...rest} />
}

export function Textarea({ className, ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className={clsx('input', 'textarea', className)} {...rest} />
}

export function Select({ className, children, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select className={clsx('input', 'select', className)} {...rest}>
      {children}
    </select>
  )
}

interface FieldProps {
  label: string
  htmlFor?: string
  hint?: string
  error?: string
  children: ReactNode
  className?: string
}

export function Field({ label, htmlFor, hint, error, children, className }: FieldProps) {
  return (
    <div className={clsx('field', className)}>
      <label className="field__label" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {error ? (
        <p className="field__error" role="alert">
          <AlertTriangle size={12} aria-hidden="true" /> {error}
        </p>
      ) : hint ? (
        <p className="field__hint">{hint}</p>
      ) : null}
    </div>
  )
}

interface CheckboxProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> {
  label?: string
  indeterminate?: boolean
}

export function Checkbox({ label, className, indeterminate = false, ...rest }: CheckboxProps) {
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (inputRef.current) inputRef.current.indeterminate = indeterminate
  }, [indeterminate])

  return (
    <label className={clsx('check', className)}>
      <input
        ref={inputRef}
        type="checkbox"
        className="check__input"
        aria-checked={indeterminate ? 'mixed' : undefined}
        {...rest}
      />
      <span className={clsx('check__box', indeterminate && 'is-indeterminate')} aria-hidden="true">
        {indeterminate ? <span className="check__dash" /> : <Check size={12} strokeWidth={3} />}
      </span>
      {label ? <span className="check__label">{label}</span> : null}
    </label>
  )
}

interface SwitchProps {
  checked: boolean
  onChange: (checked: boolean) => void
  label: string
  disabled?: boolean
}

export function Switch({ checked, onChange, label, disabled }: SwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      title={label}
      disabled={disabled}
      className={clsx('switch', checked && 'is-on')}
      onClick={() => onChange(!checked)}
    >
      <span className="switch__knob" />
    </button>
  )
}

const TONE_LABEL: Record<Tone, string> = {
  neutral: 'neutral',
  mint: 'mint',
  success: 'success',
  warning: 'warning',
  danger: 'danger',
  info: 'info',
}

export function Badge({ tone = 'neutral', children, className }: { tone?: Tone; children: ReactNode; className?: string }) {
  return (
    <span className={clsx('badge', `badge--${tone}`, className)} data-tone={TONE_LABEL[tone]}>
      {children}
    </span>
  )
}

export function StatusDot({ tone = 'neutral', pulse = false }: { tone?: Tone; pulse?: boolean }) {
  return <span className={clsx('status-dot', `status-dot--${tone}`, pulse && 'is-pulsing')} aria-hidden="true" />
}

interface CardProps {
  title?: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
  bodyClassName?: string
}

export function Card({ title, subtitle, actions, children, className, bodyClassName }: CardProps) {
  return (
    <section className={clsx('card', className)}>
      {title || actions ? (
        <header className="card__head">
          <div className="card__heading">
            {title ? <h2 className="card__title">{title}</h2> : null}
            {subtitle ? <p className="card__subtitle">{subtitle}</p> : null}
          </div>
          {actions ? <div className="card__actions">{actions}</div> : null}
        </header>
      ) : null}
      <div className={clsx('card__body', bodyClassName)}>{children}</div>
    </section>
  )
}

interface MetricTileProps {
  label: string
  value: ReactNode
  hint?: string
  delta?: string
  trend?: 'up' | 'down' | 'flat'
  icon?: ReactNode
  spark?: number[]
  tone?: Tone
}

export function MetricTile({ label, value, hint, delta, trend = 'flat', icon, spark, tone = 'mint' }: MetricTileProps) {
  return (
    <article className={clsx('metric', `metric--${tone}`)}>
      <div className="metric__top">
        <span className="metric__label">{label}</span>
        {icon ? <span className="metric__icon">{icon}</span> : null}
      </div>
      <div className="metric__value">{value}</div>
      <div className="metric__foot">
        {hint ? <span className="metric__hint">{hint}</span> : null}
        {delta ? (
          <span className={clsx('metric__delta', `is-${trend}`)}>
            {trend === 'up' ? '▲' : trend === 'down' ? '▼' : '•'} {delta}
          </span>
        ) : null}
      </div>
      {spark && spark.length > 1 ? (
        <div className="metric__spark" aria-hidden="true">
          <Sparkline data={spark} width={104} height={32} />
        </div>
      ) : null}
    </article>
  )
}

export function Sparkline({
  data,
  width = 96,
  height = 28,
  className,
}: {
  data: number[]
  width?: number
  height?: number
  className?: string
}) {
  const id = useId().replace(/:/g, '')
  const min = Math.min(...data)
  const max = Math.max(...data)
  const span = max - min || 1
  const step = data.length > 1 ? width / (data.length - 1) : width
  const points = data.map((value, index) => {
    const x = index * step
    const y = height - 2 - ((value - min) / span) * (height - 6)
    return `${x.toFixed(2)},${y.toFixed(2)}`
  })
  const line = `M${points.join(' L')}`
  const area = `${line} L${width},${height} L0,${height} Z`
  return (
    <svg className={clsx('sparkline', className)} width={width} height={height} viewBox={`0 0 ${width} ${height}`}>
      <defs>
        <linearGradient id={`spark-${id}`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="currentColor" stopOpacity="0.28" />
          <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#spark-${id})`} />
      <path d={line} fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

export function ProgressBar({
  value,
  max = 100,
  tone = 'mint',
  label,
}: {
  value: number
  max?: number
  tone?: Tone
  label?: string
}) {
  const pct = max > 0 ? Math.max(0, Math.min(100, (value / max) * 100)) : 0
  const { t } = useI18n()
  return (
    <div className={clsx('progress', `progress--${tone}`)}>
      <div
        className="progress__track"
        role="progressbar"
        aria-valuenow={Math.round(pct)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={label ?? t('common.progress')}
      >
        <span className="progress__fill" style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

export function Skeleton({ width, height = 12, className }: { width?: number | string; height?: number; className?: string }) {
  return (
    <span
      className={clsx('skeleton', className)}
      style={{ width: width ?? '100%', height }}
      aria-hidden="true"
    />
  )
}

export function TableSkeleton({ rows = 6, columns = 6 }: { rows?: number; columns?: number }) {
  const { t } = useI18n()
  return (
    <div className="table-skeleton" aria-busy="true" aria-label={t('common.loadingTable')}>
      {Array.from({ length: rows }, (_, rowIndex) => (
        <div className="table-skeleton__row" key={rowIndex}>
          {Array.from({ length: columns }, (_, columnIndex) => (
            <Skeleton key={columnIndex} height={10} width={columnIndex === 0 ? '60%' : '80%'} />
          ))}
        </div>
      ))}
    </div>
  )
}

export function EmptyState({
  title,
  description,
  action,
  icon,
}: {
  title: string
  description?: string
  action?: ReactNode
  icon?: ReactNode
}) {
  return (
    <div className="empty">
      <span className="empty__icon">{icon ?? <Inbox size={20} aria-hidden="true" />}</span>
      <p className="empty__title">{title}</p>
      {description ? <p className="empty__desc">{description}</p> : null}
      {action ? <div className="empty__action">{action}</div> : null}
    </div>
  )
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  const { t } = useI18n()
  return (
    <div className="empty empty--error" role="alert">
      <span className="empty__icon">
        <X size={20} aria-hidden="true" />
      </span>
      <p className="empty__title">{t('common.couldNotLoad')}</p>
      <p className="empty__desc">{message}</p>
      {onRetry ? (
        <div className="empty__action">
          <Button variant="secondary" onClick={onRetry}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}
    </div>
  )
}

export function PageHeader({
  title,
  subtitle,
  actions,
  eyebrow,
  refresh,
}: {
  title: string
  subtitle?: string
  actions?: ReactNode
  eyebrow?: string
  refresh?: RefreshSummary
}) {
  const { t } = useI18n()
  const staleLabel = refresh?.stale
    ? t(refresh.error ? 'common.staleWithError' : 'common.stale', {
        time: refresh.updatedAt ? formatTime(new Date(refresh.updatedAt).toISOString()) : t('common.unknown'),
        error: refresh.error ?? '',
      })
    : ''

  return (
    <header className="page-head">
      <div>
        {eyebrow ? <p className="page-head__eyebrow">{eyebrow}</p> : null}
        <div className="page-head__title-row">
          <h1 className="page-head__title">{title}</h1>
          {refresh?.stale ? (
            <span className="stale-dot" role="img" aria-label={staleLabel} title={staleLabel} />
          ) : null}
        </div>
        {subtitle ? <p className="page-head__subtitle">{subtitle}</p> : null}
      </div>
      {actions ? <div className="page-head__actions">{actions}</div> : null}
    </header>
  )
}

interface SegmentedOption<T extends string> {
  value: T
  label: string
  icon?: ReactNode
}

export function SegmentedControl<T extends string>({
  options,
  value,
  onChange,
  label,
}: {
  options: Array<SegmentedOption<T>>
  value: T
  onChange: (value: T) => void
  label: string
}) {
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className={clsx('segmented__item', option.value === value && 'is-active')}
          aria-pressed={option.value === value}
          onClick={() => onChange(option.value)}
        >
          {option.icon}
          {option.label}
        </button>
      ))}
    </div>
  )
}

export function Tabs<T extends string>({
  options,
  value,
  onChange,
  label,
}: {
  options: Array<SegmentedOption<T>>
  value: T
  onChange: (value: T) => void
  label: string
}) {
  return (
    <div className="tabs" role="tablist" aria-label={label}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          role="tab"
          aria-selected={option.value === value}
          className={clsx('tabs__item', option.value === value && 'is-active')}
          onClick={() => onChange(option.value)}
        >
          {option.icon}
          {option.label}
        </button>
      ))}
    </div>
  )
}
