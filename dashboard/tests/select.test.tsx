import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import appCss from '../src/styles/app.css?raw'
import { Select } from '../src/components/Select'
import type { SelectOption, SelectVariant } from '../src/components/Select'

const FRUIT: SelectOption[] = [
  { value: 'apple', label: 'Apple' },
  { value: 'banana', label: 'Banana' },
  { value: 'cherry', label: 'Cherry' },
]

function SingleHarness({
  initial = 'apple',
  options = FRUIT,
  searchable = false,
  disabled = false,
  variant,
}: {
  initial?: string
  options?: SelectOption[]
  searchable?: boolean
  disabled?: boolean
  variant?: SelectVariant
}) {
  const [value, setValue] = useState(initial)
  return (
    <Select
      aria-label="Fruit"
      variant={variant}
      searchable={searchable}
      disabled={disabled}
      value={value}
      onChange={setValue}
      options={options}
    />
  )
}

function MultiHarness({
  initial = [],
  options = FRUIT,
  searchable = false,
}: {
  initial?: string[]
  options?: SelectOption[]
  searchable?: boolean
}) {
  const [value, setValue] = useState<string[]>(initial)
  return (
    <Select
      aria-label="Fruit"
      variant="region"
      multiple
      searchable={searchable}
      value={value}
      onChange={setValue}
      options={options}
    />
  )
}

describe('custom select', () => {
  it('opens on click, renders in a body portal and selects a single option', async () => {
    const user = userEvent.setup()
    render(<SingleHarness />)

    const trigger = screen.getByRole('combobox', { name: 'Fruit' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(trigger).toHaveTextContent('Apple')

    await user.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')

    const listbox = screen.getByRole('listbox', { name: 'Fruit' })
    expect(listbox.closest('.sel__pop')?.parentElement).toBe(document.body)
    expect(within(listbox).getAllByRole('option')).toHaveLength(3)
    expect(within(listbox).getByRole('option', { name: 'Apple' })).toHaveAttribute(
      'aria-selected',
      'true',
    )

    await user.click(within(listbox).getByRole('option', { name: 'Banana' }))
    expect(trigger).toHaveTextContent('Banana')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('wires combobox/listbox/option ARIA and aria-activedescendant', async () => {
    const user = userEvent.setup()
    render(<SingleHarness />)
    const trigger = screen.getByRole('combobox', { name: 'Fruit' })

    expect(trigger).toHaveAttribute('aria-haspopup', 'listbox')
    expect(trigger).not.toHaveAttribute('aria-controls')

    await user.click(trigger)
    const listbox = screen.getByRole('listbox', { name: 'Fruit' })
    expect(trigger).toHaveAttribute('aria-controls', listbox.id)

    const options = within(listbox).getAllByRole('option')
    expect(options[0]).toHaveAttribute('aria-selected', 'true')
    expect(options[1]).toHaveAttribute('aria-selected', 'false')
    expect(trigger).toHaveAttribute('aria-activedescendant', options[0].id)
  })

  it('navigates with arrows, Home/End, Enter and Escape', async () => {
    const user = userEvent.setup()
    render(<SingleHarness />)
    const trigger = screen.getByRole('combobox', { name: 'Fruit' })
    trigger.focus()

    await user.keyboard('{ArrowDown}')
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    const apple = screen.getByRole('option', { name: 'Apple' })
    expect(trigger).toHaveAttribute('aria-activedescendant', apple.id)

    await user.keyboard('{ArrowDown}')
    const banana = screen.getByRole('option', { name: 'Banana' })
    expect(trigger).toHaveAttribute('aria-activedescendant', banana.id)

    await user.keyboard('{End}')
    const cherry = screen.getByRole('option', { name: 'Cherry' })
    expect(trigger).toHaveAttribute('aria-activedescendant', cherry.id)

    await user.keyboard('{Home}')
    expect(trigger).toHaveAttribute('aria-activedescendant', apple.id)

    await user.keyboard('{Enter}')
    expect(trigger).toHaveTextContent('Apple')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')

    await user.keyboard('{ArrowUp}')
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    await user.keyboard('{Escape}')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(trigger).toHaveFocus()
  })

  it('closes on Tab and moves focus to the next control', async () => {
    const user = userEvent.setup()
    render(
      <div>
        <SingleHarness />
        <button type="button">After</button>
      </div>,
    )
    const trigger = screen.getByRole('combobox', { name: 'Fruit' })
    trigger.focus()
    await user.keyboard('{ArrowDown}')
    expect(screen.getByRole('listbox')).toBeInTheDocument()

    await user.tab()
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'After' })).toHaveFocus()
  })

  it('keeps the keyboard anchor on the trigger when tabbing out of a searchable popup', async () => {
    const user = userEvent.setup()
    render(<SingleHarness searchable />)
    const trigger = screen.getByRole('button', { name: 'Fruit' })

    await user.click(trigger)
    expect(screen.getByRole('combobox', { name: 'Fruit' })).toHaveFocus()
    await user.tab()

    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()

    await user.click(trigger)
    expect(screen.getByRole('combobox', { name: 'Fruit' })).toHaveFocus()
    await user.tab({ shift: true })

    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('jumps to an option by typeahead when not searchable', async () => {
    const user = userEvent.setup()
    render(<SingleHarness />)
    const trigger = screen.getByRole('combobox', { name: 'Fruit' })
    trigger.focus()

    await user.keyboard('c')
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(trigger).toHaveAttribute(
      'aria-activedescendant',
      screen.getByRole('option', { name: 'Cherry' }).id,
    )

    await user.keyboard('{Enter}')
    expect(trigger).toHaveTextContent('Cherry')
  })

  it('filters a searchable select, announces no matches and clears the query', async () => {
    const user = userEvent.setup()
    render(<SingleHarness searchable />)
    const trigger = screen.getByRole('button', { name: 'Fruit' })

    await user.click(trigger)
    const search = screen.getByRole('combobox', { name: 'Fruit' })
    expect(search).toHaveFocus()
    expect(search).toHaveAttribute('aria-autocomplete', 'list')

    await user.type(search, 'ban')
    expect(screen.getByRole('option', { name: 'Banana' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Apple' })).not.toBeInTheDocument()

    await user.clear(search)
    await user.type(search, 'zzz')
    expect(screen.getByRole('status')).toHaveTextContent('No matches for “zzz”')
    expect(screen.queryAllByRole('option')).toHaveLength(0)

    await user.click(screen.getByRole('button', { name: 'Clear search' }))
    expect(screen.getAllByRole('option')).toHaveLength(3)

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('selects with Enter from the search input', async () => {
    const user = userEvent.setup()
    render(<SingleHarness searchable />)

    await user.click(screen.getByRole('button', { name: 'Fruit' }))
    const search = screen.getByRole('combobox', { name: 'Fruit' })
    await user.type(search, 'cher')
    await user.keyboard('{Enter}')

    const trigger = screen.getByRole('button', { name: 'Fruit' })
    expect(trigger).toHaveTextContent('Cherry')
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('keeps the popover open for multi-select and reports the selected count', async () => {
    const user = userEvent.setup()
    render(<MultiHarness />)
    const trigger = screen.getByRole('combobox', { name: 'Fruit' })
    expect(trigger).toHaveTextContent('Select…')

    await user.click(trigger)
    const listbox = screen.getByRole('listbox', { name: 'Fruit' })
    expect(listbox).toHaveAttribute('aria-multiselectable', 'true')

    await user.click(within(listbox).getByRole('option', { name: 'Banana' }))
    expect(screen.getByRole('listbox')).toBeInTheDocument()
    expect(trigger).toHaveTextContent('Banana')
    expect(trigger.querySelector('.sel__count')).toHaveTextContent('1')
    expect(within(listbox).getByRole('option', { name: 'Banana' })).toHaveAttribute(
      'aria-selected',
      'true',
    )

    await user.click(within(listbox).getByRole('option', { name: 'Cherry' }))
    expect(trigger).toHaveTextContent('2 selected')

    await user.click(screen.getByRole('button', { name: 'Select all' }))
    expect(trigger).toHaveTextContent('3 selected')

    await user.click(screen.getByRole('button', { name: 'Clear all' }))
    expect(trigger).toHaveTextContent('Select…')

    await user.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('renders group headers and keeps options inside their group', async () => {
    const user = userEvent.setup()
    const options: SelectOption[] = [
      { value: 'cn-zj', label: 'China · Zhejiang', group: 'China' },
      { value: 'cn-sh', label: 'China · Shanghai', group: 'China' },
      { value: 'kr-sel', label: 'South Korea · Seoul', group: 'South Korea' },
    ]
    render(<SingleHarness options={options} initial="cn-zj" />)

    await user.click(screen.getByRole('combobox', { name: 'Fruit' }))
    const china = screen.getByRole('group', { name: 'China' })
    const korea = screen.getByRole('group', { name: 'South Korea' })
    expect(within(china).getAllByRole('option')).toHaveLength(2)
    expect(within(korea).getAllByRole('option')).toHaveLength(1)
    expect(within(korea).getByRole('option', { name: 'South Korea · Seoul' })).toBeInTheDocument()
  })

  it('closes on outside pointer down and when the trigger is clicked again', async () => {
    const user = userEvent.setup()
    render(
      <div>
        <SingleHarness />
        <button type="button">Elsewhere</button>
      </div>,
    )
    const trigger = screen.getByRole('combobox', { name: 'Fruit' })

    await user.click(trigger)
    expect(screen.getByRole('listbox')).toBeInTheDocument()
    await user.click(trigger)
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()

    await user.click(trigger)
    expect(screen.getByRole('listbox')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Elsewhere' }))
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
  })

  it('never opens when disabled and skips disabled options', async () => {
    const user = userEvent.setup()
    const options: SelectOption[] = [
      ...FRUIT,
      { value: 'durian', label: 'Durian', disabled: true },
    ]
    const { rerender } = render(<SingleHarness options={options} disabled />)

    const disabledTrigger = screen.getByRole('combobox', { name: 'Fruit' })
    expect(disabledTrigger).toBeDisabled()
    await user.click(disabledTrigger)
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()

    rerender(<SingleHarness options={options} />)
    const trigger = screen.getByRole('combobox', { name: 'Fruit' })
    trigger.focus()
    await user.keyboard('{ArrowDown}')
    await user.keyboard('{End}')
    expect(trigger).toHaveAttribute(
      'aria-activedescendant',
      screen.getByRole('option', { name: 'Cherry' }).id,
    )

    const durian = screen.getByRole('option', { name: 'Durian' })
    expect(durian).toHaveAttribute('aria-disabled', 'true')
    await user.click(durian)
    expect(trigger).toHaveTextContent('Apple')
    expect(screen.getByRole('listbox')).toBeInTheDocument()
  })

  it('flips above the trigger and clamps to the viewport when there is no room below', async () => {
    const user = userEvent.setup()
    render(<SingleHarness />)
    const trigger = screen.getByRole('combobox', { name: 'Fruit' })
    vi.spyOn(trigger, 'getBoundingClientRect').mockReturnValue({
      x: 1000,
      y: 700,
      top: 700,
      bottom: 732,
      left: 1000,
      right: 1200,
      width: 200,
      height: 32,
      toJSON: () => ({}),
    })
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(300)

    await user.click(trigger)
    const popover = document.querySelector('.sel__pop') as HTMLElement
    await waitFor(() => expect(popover).toHaveAttribute('data-placement', 'top'))
    expect(popover.style.left).toBe('816px')
    expect(popover.style.top).toBe('394px')
    expect(popover.style.maxHeight).toBe('320px')
  })

  it('styles the popover with the shared motion tokens and reduced-motion guard', () => {
    expect(appCss).toMatch(/\.sel__pop\s*\{[^}]*animation:\s*select-pop-in var\(--dur-base\)/s)
    expect(appCss).toMatch(/@keyframes select-pop-in\s*\{[^}]*scale\(0\.98\)/s)
    expect(appCss).toMatch(/@keyframes select-pop-in-top\s*\{[^}]*translateY\(4px\)/s)
    expect(appCss).toMatch(
      /@media \(prefers-reduced-motion: reduce\)[\s\S]*?animation-duration:\s*0\.001ms !important/s,
    )
  })
})
