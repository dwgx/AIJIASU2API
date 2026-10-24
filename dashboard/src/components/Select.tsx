import clsx from 'clsx'
import { ChevronDown } from 'lucide-react'
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { FocusEvent, KeyboardEvent, ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useI18n } from '../lib/i18n'
import { ApcCheckIcon, ApcSearchIcon } from './icons/ApcIcons'

/**
 * Custom select / combobox.
 *
 * The trigger is a real button (so Field labels, disabled state and focus rings
 * keep working) and the popup portals to <body> so a toolbar's overflow or a
 * drawer's scroll container can never clip it. Positioning is fixed and
 * recomputed from the trigger rect on scroll/resize, flipping above the trigger
 * when the viewport below is too short.
 *
 * Accessibility follows the APG select-only combobox pattern: the trigger owns
 * `role="combobox"` + `aria-activedescendant` while focus stays on it. A
 * searchable select moves focus into an input that carries the combobox role
 * instead, so screen readers announce filtered results from the real focus
 * point. Multiple selects expose `aria-multiselectable` on the listbox.
 */

export type SelectVariant = 'filter' | 'form' | 'settings' | 'log' | 'region'
export type SelectSize = 'sm' | 'md' | 'lg'

export interface SelectOption {
  value: string
  label: string
  description?: string
  icon?: ReactNode
  disabled?: boolean
  group?: string
}

interface SelectBaseProps {
  options: SelectOption[]
  variant?: SelectVariant
  size?: SelectSize
  placeholder?: string
  searchable?: boolean
  disabled?: boolean
  id?: string
  className?: string
  'aria-label'?: string
  'aria-labelledby'?: string
}

interface SingleSelectProps extends SelectBaseProps {
  multiple?: false
  value: string
  onChange: (value: string) => void
}

interface MultiSelectProps extends SelectBaseProps {
  multiple: true
  value: string[]
  onChange: (value: string[]) => void
}

export type SelectProps = SingleSelectProps | MultiSelectProps

interface PopoverRect {
  top: number
  left: number
  width: number
  maxHeight: number
  placement: 'top' | 'bottom'
}

const VIEWPORT_MARGIN = 8
const TRIGGER_GAP = 6
const POPOVER_MAX_HEIGHT = 320
const POPOVER_MIN_WIDTH = 180
const REGION_MIN_WIDTH = 260
const SETTINGS_MIN_WIDTH = 240
const TYPEAHEAD_RESET_MS = 600

function firstEnabledIndex(options: SelectOption[], from = 0): number {
  for (let index = from; index < options.length; index += 1) {
    if (!options[index].disabled) return index
  }
  return -1
}

function matchesQuery(option: SelectOption, needle: string): boolean {
  return `${option.label} ${option.description ?? ''} ${option.group ?? ''}`.toLowerCase().includes(needle)
}

export function Select(props: SelectProps) {
  const {
    options,
    variant = 'form',
    size = 'md',
    placeholder,
    searchable = false,
    disabled = false,
    id,
    className,
  } = props
  const ariaLabel = props['aria-label']
  const ariaLabelledBy = props['aria-labelledby']
  const multiple = props.multiple === true
  const { t } = useI18n()

  const reactId = useId()
  const listboxId = `${reactId}-listbox`
  const searchId = `${reactId}-search`

  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const popRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const typeaheadRef = useRef({ buffer: '', at: 0 })

  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const [rect, setRect] = useState<PopoverRect | null>(null)

  const selectedValues: string[] =
    props.multiple === true ? props.value : props.value ? [props.value] : []
  const selectedSet = new Set(selectedValues)

  const placeholderText = placeholder ?? t('select.placeholder')

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (!needle) return options
    return options.filter((option) => matchesQuery(option, needle))
  }, [options, query])

  // Preserve the options order while collecting each group's members, so a
  // caller can interleave groups or sort them without the component reordering.
  const groups = useMemo(() => {
    const result: Array<{
      key: string
      label?: string
      items: Array<{ option: SelectOption; index: number }>
    }> = []
    const byKey = new Map<string, (typeof result)[number]>()
    visible.forEach((option, index) => {
      const key = option.group ?? ''
      let group = byKey.get(key)
      if (!group) {
        group = { key, label: option.group, items: [] }
        byKey.set(key, group)
        result.push(group)
      }
      group.items.push({ option, index })
    })
    return result
  }, [visible])

  const selectedOption =
    props.multiple === true ? undefined : options.find((option) => option.value === props.value)

  let display: string
  if (props.multiple === true) {
    if (selectedValues.length === 0) display = placeholderText
    else if (selectedValues.length === 1) {
      display = options.find((option) => option.value === selectedValues[0])?.label ?? selectedValues[0]
    } else {
      display = t('select.selectedCount', { count: selectedValues.length })
    }
  } else {
    display = selectedOption?.label ?? (props.value || placeholderText)
  }
  const showingPlaceholder = display === placeholderText

  const optionDomId = (index: number) => `${listboxId}-opt-${index}`
  const activeOption = visible[active]

  const updatePosition = () => {
    const trigger = triggerRef.current
    const popover = popRef.current
    if (!trigger || !popover) return
    const triggerRect = trigger.getBoundingClientRect()
    const viewportWidth = window.innerWidth || document.documentElement.clientWidth || 0
    const viewportHeight = window.innerHeight || document.documentElement.clientHeight || 0
    const minWidth =
      variant === 'region' ? REGION_MIN_WIDTH : variant === 'settings' ? SETTINGS_MIN_WIDTH : POPOVER_MIN_WIDTH
    const width = Math.min(
      Math.max(triggerRect.width, minWidth),
      Math.max(minWidth, viewportWidth - VIEWPORT_MARGIN * 2),
    )
    const measuredHeight = popover.offsetHeight || 0
    const spaceBelow = viewportHeight - triggerRect.bottom - VIEWPORT_MARGIN - TRIGGER_GAP
    const spaceAbove = triggerRect.top - VIEWPORT_MARGIN - TRIGGER_GAP
    const placement: 'top' | 'bottom' =
      measuredHeight > spaceBelow && spaceAbove > spaceBelow ? 'top' : 'bottom'
    const available = placement === 'bottom' ? spaceBelow : spaceAbove
    const maxHeight = Math.max(96, Math.min(POPOVER_MAX_HEIGHT, available))
    const height = Math.min(measuredHeight || maxHeight, maxHeight)
    const top =
      placement === 'bottom'
        ? triggerRect.bottom + TRIGGER_GAP
        : Math.max(VIEWPORT_MARGIN, triggerRect.top - TRIGGER_GAP - height)
    const left = Math.min(
      Math.max(triggerRect.left, VIEWPORT_MARGIN),
      Math.max(VIEWPORT_MARGIN, viewportWidth - width - VIEWPORT_MARGIN),
    )
    setRect({ top, left, width, maxHeight, placement })
  }

  // Measure after the portal is committed, then keep the popover glued to the
  // trigger. Capture-phase scroll catches scrolling inside any ancestor.
  useLayoutEffect(() => {
    if (!open) {
      setRect(null)
      return
    }
    updatePosition()
    const frame = requestAnimationFrame(updatePosition)
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    return () => {
      cancelAnimationFrame(frame)
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
    }
    // `visible.length` and `query` change the popover height, so re-measure.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, query, visible.length, variant])

  useEffect(() => {
    if (!open || !searchable) return
    searchRef.current?.focus()
  }, [open, searchable])

  useEffect(() => {
    if (!open) return
    document.getElementById(optionDomId(active))?.scrollIntoView?.({ block: 'nearest' })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, open, query])

  // A filter can hide the active option; snap to the first usable one.
  useEffect(() => {
    if (!open) return
    if (active >= visible.length || visible[active]?.disabled) {
      const next = firstEnabledIndex(visible)
      if (next !== active) setActive(Math.max(0, next))
    }
  }, [active, open, visible])

  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null
      if (!target) return
      if (rootRef.current?.contains(target) || popRef.current?.contains(target)) return
      setOpen(false)
      setQuery('')
    }
    document.addEventListener('pointerdown', onPointerDown, true)
    return () => document.removeEventListener('pointerdown', onPointerDown, true)
  }, [open])

  const closeMenu = (restoreFocus = false) => {
    setOpen(false)
    setQuery('')
    typeaheadRef.current = { buffer: '', at: 0 }
    if (restoreFocus) triggerRef.current?.focus()
  }

  const openMenu = (prefer: 'selected' | 'first' | 'last' = 'selected') => {
    if (disabled) return
    setQuery('')
    setOpen(true)
    let index = -1
    if (prefer === 'last') {
      for (let cursor = options.length - 1; cursor >= 0; cursor -= 1) {
        if (!options[cursor].disabled) {
          index = cursor
          break
        }
      }
    } else if (prefer === 'selected') {
      index = options.findIndex((option) => !option.disabled && selectedSet.has(option.value))
    }
    if (index < 0) index = firstEnabledIndex(options)
    setActive(Math.max(0, index))
  }

  const selectOption = (option: SelectOption) => {
    if (option.disabled) return
    if (props.multiple === true) {
      const next = selectedSet.has(option.value)
        ? props.value.filter((value) => value !== option.value)
        : [...props.value, option.value]
      props.onChange(next)
      return
    }
    props.onChange(option.value)
    closeMenu(true)
  }

  const moveActive = (delta: number) => {
    if (visible.length === 0) return
    let next = active
    for (let step = 0; step < visible.length; step += 1) {
      next = (next + delta + visible.length) % visible.length
      if (!visible[next].disabled) {
        setActive(next)
        return
      }
    }
  }

  const typeaheadMatch = (character: string): number => {
    const now = Date.now()
    const state = typeaheadRef.current
    state.buffer = now - state.at > TYPEAHEAD_RESET_MS ? character : state.buffer + character
    state.at = now
    const needle = state.buffer.toLowerCase()
    const start = open && visible.length > 0 ? active : Math.max(0, firstEnabledIndex(options))
    const pool = open ? visible : options
    for (let offset = 0; offset < pool.length; offset += 1) {
      const index = (start + offset) % pool.length
      const option = pool[index]
      if (!option.disabled && option.label.toLowerCase().startsWith(needle)) return index
    }
    return -1
  }

  const onTriggerKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (disabled) return
    const { key } = event
    if (!open) {
      if (key === 'ArrowDown' || key === 'ArrowUp' || key === 'Enter' || key === ' ') {
        event.preventDefault()
        openMenu(key === 'ArrowUp' ? 'last' : 'selected')
        return
      }
      if (key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
        const match = typeaheadMatch(key)
        if (match >= 0) {
          event.preventDefault()
          openMenu('first')
          setActive(match)
        }
      }
      return
    }
    switch (key) {
      case 'ArrowDown':
        event.preventDefault()
        moveActive(1)
        break
      case 'ArrowUp':
        event.preventDefault()
        moveActive(-1)
        break
      case 'Home':
        event.preventDefault()
        setActive(Math.max(0, firstEnabledIndex(visible)))
        break
      case 'End':
        for (let cursor = visible.length - 1; cursor >= 0; cursor -= 1) {
          if (!visible[cursor].disabled) {
            event.preventDefault()
            setActive(cursor)
            break
          }
        }
        break
      case 'Enter':
        event.preventDefault()
        if (activeOption) selectOption(activeOption)
        break
      case 'Escape':
        event.preventDefault()
        closeMenu(true)
        break
      case 'Tab':
        closeMenu()
        break
      default:
        if (key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
          event.preventDefault()
          const match = typeaheadMatch(key)
          if (match >= 0) setActive(match)
        }
    }
  }

  const onSearchKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault()
        moveActive(1)
        break
      case 'ArrowUp':
        event.preventDefault()
        moveActive(-1)
        break
      case 'Enter':
        event.preventDefault()
        if (activeOption) selectOption(activeOption)
        break
      case 'Tab':
        // Tab forward reaches the footer (or closes via blur); Shift+Tab goes
        // back to the trigger the user opened from, never to a page control
        // that happens to precede the portal in document order.
        if (event.shiftKey) {
          event.preventDefault()
          closeMenu(true)
        }
        break
      default:
        break
    }
  }

  const onPopoverKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'Escape') return
    event.preventDefault()
    event.stopPropagation()
    closeMenu(true)
  }

  const onPopoverBlur = (event: FocusEvent<HTMLDivElement>) => {
    const next = event.relatedTarget as Node | null
    if (next && (popRef.current?.contains(next) || rootRef.current?.contains(next))) return
    // A null relatedTarget means focus fell to the body (e.g. Tab out of the
    // last control in the portalled popup); keep the keyboard anchor on the
    // trigger instead of stranding the user at the top of the document.
    closeMenu(!next)
  }

  const toggleAllVisible = () => {
    if (props.multiple !== true) return
    const values = visible.filter((option) => !option.disabled).map((option) => option.value)
    if (values.length === 0) return
    const allSelected = values.every((value) => selectedSet.has(value))
    const next = allSelected
      ? props.value.filter((value) => !values.includes(value))
      : Array.from(new Set([...props.value, ...values]))
    props.onChange(next)
  }

  const visibleValues = visible.filter((option) => !option.disabled).map((option) => option.value)
  const allVisibleSelected =
    visibleValues.length > 0 && visibleValues.every((value) => selectedSet.has(value))

  const renderOption = ({ option, index }: { option: SelectOption; index: number }) => {
    const isSelected = selectedSet.has(option.value)
    return (
      <div
        key={option.value}
        id={optionDomId(index)}
        role="option"
        aria-selected={isSelected}
        aria-disabled={option.disabled || undefined}
        className={clsx(
          'sel__opt',
          index === active && !option.disabled && 'is-active',
          isSelected && 'is-selected',
          option.disabled && 'is-disabled',
        )}
        onMouseMove={() => {
          if (!option.disabled) setActive(index)
        }}
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => selectOption(option)}
      >
        {multiple ? (
          <span className={clsx('sel__check', isSelected && 'is-checked')} aria-hidden="true">
            {isSelected ? <ApcCheckIcon size={11} /> : null}
          </span>
        ) : option.icon ? (
          <span className="sel__opt-icon" aria-hidden="true">
            {option.icon}
          </span>
        ) : null}
        <span className="sel__opt-body">
          <span className="sel__opt-label">{option.label}</span>
          {option.description ? <span className="sel__opt-desc">{option.description}</span> : null}
        </span>
        {!multiple && isSelected ? (
          <span className="sel__check sel__check--end" aria-hidden="true">
            <ApcCheckIcon size={11} />
          </span>
        ) : null}
      </div>
    )
  }

  const popover = (
    <div
      ref={popRef}
      className={clsx('sel__pop', `sel__pop--${variant}`)}
      data-placement={rect?.placement ?? 'bottom'}
      style={{
        top: rect?.top ?? 0,
        left: rect?.left ?? 0,
        width: rect?.width ?? POPOVER_MIN_WIDTH,
        maxHeight: rect?.maxHeight ?? POPOVER_MAX_HEIGHT,
        visibility: rect ? undefined : 'hidden',
      }}
      onKeyDown={onPopoverKeyDown}
      onBlur={onPopoverBlur}
    >
      {searchable ? (
        <div className="sel__search">
          <ApcSearchIcon size={14} />
          <input
            ref={searchRef}
            id={searchId}
            type="text"
            role="combobox"
            className="sel__search-input"
            value={query}
            placeholder={t('select.search')}
            aria-label={ariaLabel}
            aria-labelledby={!ariaLabel ? ariaLabelledBy : undefined}
            aria-autocomplete="list"
            aria-expanded="true"
            aria-controls={listboxId}
            aria-activedescendant={activeOption ? optionDomId(active) : undefined}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => {
              setQuery(event.target.value)
              setActive(0)
            }}
            onKeyDown={onSearchKeyDown}
          />
          {query ? (
            <button
              type="button"
              className="sel__search-clear"
              aria-label={t('select.clearSearch')}
              onClick={() => {
                setQuery('')
                setActive(0)
                searchRef.current?.focus()
              }}
            >
              <span aria-hidden="true">×</span>
            </button>
          ) : null}
        </div>
      ) : null}

      <div
        id={listboxId}
        role="listbox"
        className="sel__list"
        aria-label={ariaLabel}
        aria-labelledby={!ariaLabel ? ariaLabelledBy : undefined}
        aria-multiselectable={multiple || undefined}
      >
        {groups.map((group, groupIndex) =>
          group.label ? (
            <div
              key={group.key}
              role="group"
              aria-labelledby={`${listboxId}-group-${groupIndex}`}
              className="sel__group"
            >
              <div
                id={`${listboxId}-group-${groupIndex}`}
                role="presentation"
                className="sel__group-label"
              >
                {group.label}
              </div>
              {group.items.map(renderOption)}
            </div>
          ) : (
            group.items.map(renderOption)
          ),
        )}
      </div>

      {visible.length === 0 ? (
        <div className="sel__empty" role="status">
          {query.trim()
            ? t('select.noResults', { query: query.trim() })
            : t('select.noOptions')}
        </div>
      ) : null}

      {multiple ? (
        <div className="sel__foot">
          <button
            type="button"
            className="sel__foot-btn"
            disabled={visibleValues.length === 0}
            onClick={toggleAllVisible}
          >
            {allVisibleSelected ? t('select.clearVisible') : t('select.selectAll')}
          </button>
          <button
            type="button"
            className="sel__foot-btn"
            disabled={selectedValues.length === 0}
            onClick={() => {
              if (props.multiple === true) props.onChange([])
            }}
          >
            {t('select.clear')}
          </button>
          <button
            type="button"
            className="sel__foot-btn sel__foot-btn--done"
            onClick={() => closeMenu(true)}
          >
            {t('select.done')}
          </button>
        </div>
      ) : null}
    </div>
  )

  return (
    <div
      ref={rootRef}
      className={clsx(
        'sel',
        `sel--${variant}`,
        size !== 'md' && `sel--${size}`,
        open && 'is-open',
        className,
      )}
    >
      <button
        ref={triggerRef}
        id={id}
        type="button"
        role={searchable ? undefined : 'combobox'}
        className="sel__trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listboxId : undefined}
        aria-activedescendant={open && !searchable && activeOption ? optionDomId(active) : undefined}
        aria-label={ariaLabel}
        aria-labelledby={ariaLabelledBy}
        disabled={disabled}
        onClick={() => {
          if (open) closeMenu()
          else openMenu()
        }}
        onKeyDown={onTriggerKeyDown}
      >
        <span className={clsx('sel__value', showingPlaceholder && 'is-placeholder')} title={display}>
          {display}
        </span>
        {multiple && selectedValues.length > 0 ? (
          <span className="sel__count" aria-hidden="true">
            {selectedValues.length}
          </span>
        ) : null}
        <ChevronDown size={14} className="sel__chevron" aria-hidden="true" />
      </button>
      {open ? createPortal(popover, document.body) : null}
    </div>
  )
}
