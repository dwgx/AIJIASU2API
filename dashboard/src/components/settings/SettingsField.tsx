import clsx from 'clsx'
import type { ReactNode } from 'react'

interface SettingsFieldProps {
  label: string
  htmlFor?: string
  hint?: string
  badge?: ReactNode
  /** Honesty sentence shown under the control; mirrors the badge tooltip. */
  reason?: string
  /** Explicit id for aria-describedby; defaults to `<htmlFor>-reason`. */
  reasonId?: string
  children: ReactNode
}

/**
 * Field row with room for a provenance badge. Mirrors the `Field` primitive's
 * markup so labels, hints and error spacing stay identical; the badge is the
 * extra slot a settings control needs.
 */
export function SettingsField({
  label,
  htmlFor,
  hint,
  badge,
  reason,
  reasonId,
  children,
}: SettingsFieldProps) {
  const resolvedReasonId = reasonId ?? (htmlFor ? `${htmlFor}-reason` : undefined)
  return (
    <div className="field settings-field">
      <div className="settings-field__head">
        <label className="field__label" htmlFor={htmlFor}>
          {label}
        </label>
        {badge}
      </div>
      {children}
      {hint ? <p className="field__hint">{hint}</p> : null}
      {reason ? (
        <p className="field__hint settings-field__reason" id={resolvedReasonId}>
          {reason}
        </p>
      ) : null}
    </div>
  )
}
