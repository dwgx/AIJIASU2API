import clsx from 'clsx'

interface SettingsSegmentedOption<T extends string> {
  value: T
  label: string
}

interface SettingsSegmentedProps<T extends string> {
  label: string
  value: T
  options: ReadonlyArray<SettingsSegmentedOption<T>>
  onChange: (value: T) => void
  disabled?: boolean
  describedBy?: string
}

/**
 * Same geometry as the `SegmentedControl` primitive, plus a real `disabled`
 * state so a stored-only value can be shown without pretending it is applied.
 */
export function SettingsSegmented<T extends string>({
  label,
  value,
  options,
  onChange,
  disabled = false,
  describedBy,
}: SettingsSegmentedProps<T>) {
  return (
    <div
      className={clsx('segmented', disabled && 'is-disabled')}
      role="group"
      aria-label={label}
      aria-describedby={describedBy}
    >
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className={clsx('segmented__item', option.value === value && 'is-active')}
          aria-pressed={option.value === value}
          disabled={disabled}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}
