import { AlertTriangle, ShieldAlert } from 'lucide-react'
import type { ReactNode } from 'react'
import { Badge, EmptyState } from '../primitives'
import type { LabWarning } from '../../lib/types'
import { severityTone } from '../../lib/lab'

export function DisabledCallout({
  title = 'This view is disabled',
  reason,
  flag,
}: {
  title?: string
  reason: string
  flag?: string
}) {
  return (
    <div className="callout callout--warning" role="status">
      <span className="callout__icon">
        <ShieldAlert size={18} aria-hidden="true" />
      </span>
      <div>
        <p className="callout__title">{title}</p>
        <p className="callout__text">
          {reason}
          {flag ? (
            <>
              {' '}
              Set <code className="mono">{flag}</code> and restart ajs-accountd.
            </>
          ) : null}
        </p>
      </div>
    </div>
  )
}

export function WarningList({ warnings }: { warnings: LabWarning[] }) {
  if (warnings.length === 0) {
    return (
      <EmptyState
        title="No warnings"
        description="The daemon reported no posture or state warnings."
        icon={<AlertTriangle size={20} aria-hidden="true" />}
      />
    )
  }
  return (
    <ul className="lab-warnings">
      {warnings.map((warning) => (
        <li className="lab-warnings__item" key={warning.code}>
          <Badge tone={severityTone(warning.severity)}>{warning.severity}</Badge>
          <div>
            <p className="lab-warnings__message">{warning.message}</p>
            <p className="lab-warnings__code mono">{warning.code}</p>
          </div>
        </li>
      ))}
    </ul>
  )
}

export function LabFacts({ rows }: { rows: Array<{ label: string; value: ReactNode }> }) {
  return (
    <dl className="facts facts--compact">
      {rows.map((row) => (
        <div key={row.label}>
          <dt>{row.label}</dt>
          <dd>{row.value}</dd>
        </div>
      ))}
    </dl>
  )
}
