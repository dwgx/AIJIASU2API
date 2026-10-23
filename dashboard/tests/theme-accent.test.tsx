import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeAll, beforeEach, describe, expect, it } from 'vitest'
import App from '../src/App'
import appCss from '../src/styles/app.css?raw'
import { api } from '../src/lib/api'
import type { AccentName } from '../src/lib/theme'

const MIN_RATIO = 4.5

type ThemeName = 'dark' | 'light'

const ACCENTS: ReadonlyArray<{ value: AccentName; label: string }> = [
  { value: 'mint', label: 'Mint' },
  { value: 'indigo', label: 'Indigo' },
  { value: 'violet', label: 'Violet' },
  { value: 'orange', label: 'Orange' },
]

const ACCENT_TOKENS: Record<
  AccentName,
  Record<ThemeName, { mint: string; strong: string; onMint: string }>
> = {
  mint: {
    dark: { mint: '#5eead4', strong: '#2dd4bf', onMint: '#04211c' },
    light: { mint: '#0f766e', strong: '#115e59', onMint: '#ffffff' },
  },
  indigo: {
    dark: { mint: '#818cf8', strong: '#6d74f5', onMint: '#0b0a2e' },
    light: { mint: '#4f46e5', strong: '#4338ca', onMint: '#ffffff' },
  },
  violet: {
    dark: { mint: '#c4b5fd', strong: '#a78bfa', onMint: '#1a0b2e' },
    light: { mint: '#6d28d9', strong: '#5b21b6', onMint: '#ffffff' },
  },
  orange: {
    dark: { mint: '#fb923c', strong: '#f97316', onMint: '#2a1204' },
    light: { mint: '#c2410c', strong: '#9a3412', onMint: '#ffffff' },
  },
}

function escapeSelector(selector: string): string {
  return selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function readBlock(selector: string): Record<string, string> {
  const pattern = new RegExp(`${escapeSelector(selector)}\\s*\\{([^}]*)\\}`)
  const match = appCss.match(pattern)
  if (!match) throw new Error(`app.css no longer has the ${selector} token block`)

  const tokens: Record<string, string> = {}
  for (const [, name, value] of match[1].matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    tokens[name] = value.trim()
  }
  return tokens
}

function accentSelector(accent: AccentName, theme: ThemeName): string {
  if (accent === 'mint') return theme === 'light' ? '[data-theme="light"]' : ':root'
  return theme === 'light'
    ? `[data-theme="light"][data-accent="${accent}"]`
    : `[data-accent="${accent}"]`
}

function hexToRgb(hex: string): string {
  const digits = hex.replace('#', '')
  const [r, g, b] = [0, 2, 4].map((offset) => parseInt(digits.slice(offset, offset + 2), 16))
  return `rgb(${r}, ${g}, ${b})`
}

function relativeLuminance(hex: string): number {
  const digits = hex.replace('#', '')
  if (!/^[0-9a-f]{6}$/i.test(digits)) {
    throw new Error(`expected a 6-digit hex colour, received "${hex}"`)
  }

  const [r, g, b] = [0, 2, 4].map((offset) => {
    const channel = parseInt(digits.slice(offset, offset + 2), 16) / 255
    return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrastRatio(foreground: string, background: string): number {
  const [lighter, darker] = [relativeLuminance(foreground), relativeLuminance(background)].sort(
    (a, b) => b - a,
  )
  return (lighter + 0.05) / (darker + 0.05)
}

function computedAccent(theme: ThemeName, accent: AccentName) {
  const root = document.documentElement
  const previousTheme = root.dataset.theme
  const previousAccent = root.dataset.accent
  root.dataset.theme = theme
  root.dataset.accent = accent
  const style = getComputedStyle(root)
  const tokens = {
    mint: style.getPropertyValue('--mint').trim(),
    strong: style.getPropertyValue('--mint-strong').trim(),
    onMint: style.getPropertyValue('--on-mint').trim(),
  }
  if (previousTheme === undefined) delete root.dataset.theme
  else root.dataset.theme = previousTheme
  if (previousAccent === undefined) delete root.dataset.accent
  else root.dataset.accent = previousAccent
  return tokens
}

beforeAll(() => {
  const style = document.createElement('style')
  style.textContent = appCss
  document.head.appendChild(style)
})

beforeEach(async () => {
  localStorage.clear()
  delete document.documentElement.dataset.theme
  delete document.documentElement.dataset.accent
  document.documentElement.style.colorScheme = ''
  window.history.pushState({}, '', '/settings')
  await api.resetMock()
})

async function openSettings() {
  render(<App />)
  return screen.findByRole('group', { name: 'Accent' })
}

describe('accent theme', () => {
  it('defaults to mint and applies Indigo to the document and the accent tokens', async () => {
    const user = userEvent.setup()
    const accentGroup = await openSettings()

    expect(document.documentElement.dataset.accent).toBe('mint')
    expect(localStorage.getItem('ajs.accent')).toBe('mint')
    expect(within(accentGroup).getByRole('button', { name: 'Mint' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(getComputedStyle(document.documentElement).getPropertyValue('--mint').trim()).toBe(
      '#5eead4',
    )

    await user.click(within(accentGroup).getByRole('button', { name: 'Indigo' }))

    expect(document.documentElement.dataset.accent).toBe('indigo')
    expect(localStorage.getItem('ajs.accent')).toBe('indigo')
    expect(within(accentGroup).getByRole('button', { name: 'Indigo' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(getComputedStyle(document.documentElement).getPropertyValue('--mint').trim()).toBe(
      '#818cf8',
    )
    expect(getComputedStyle(document.documentElement).getPropertyValue('--on-mint').trim()).toBe(
      '#0b0a2e',
    )
  })

  it('restores the stored accent across a remount', async () => {
    const user = userEvent.setup()
    const first = render(<App />)
    const accentGroup = await screen.findByRole('group', { name: 'Accent' })
    await user.click(within(accentGroup).getByRole('button', { name: 'Indigo' }))
    expect(localStorage.getItem('ajs.accent')).toBe('indigo')

    first.unmount()
    const remounted = await openSettings()

    expect(within(remounted).getByRole('button', { name: 'Indigo' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(document.documentElement.dataset.accent).toBe('indigo')
    expect(getComputedStyle(document.documentElement).getPropertyValue('--mint').trim()).toBe(
      '#818cf8',
    )
  })

  it('falls back to mint when the stored accent is invalid', async () => {
    localStorage.setItem('ajs.accent', 'teal')
    const accentGroup = await openSettings()

    expect(within(accentGroup).getByRole('button', { name: 'Mint' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(document.documentElement.dataset.accent).toBe('mint')
    expect(localStorage.getItem('ajs.accent')).toBe('mint')
    expect(getComputedStyle(document.documentElement).getPropertyValue('--mint').trim()).toBe(
      '#5eead4',
    )
  })

  it('renders a swatch for all four accents and keeps aria-pressed in sync', async () => {
    const user = userEvent.setup()
    const accentGroup = await openSettings()

    expect(within(accentGroup).getAllByRole('button')).toHaveLength(4)
    for (const accent of ACCENTS) {
      const button = within(accentGroup).getByRole('button', { name: accent.label })
      const dot = button.querySelector(`.accent-picker__dot--${accent.value}`)
      expect(dot, `${accent.label} has no swatch`).not.toBeNull()
      expect(getComputedStyle(dot as Element).backgroundColor).toBe(
        hexToRgb(ACCENT_TOKENS[accent.value].dark.mint),
      )
      expect(button).toHaveAttribute('aria-pressed', accent.value === 'mint' ? 'true' : 'false')
    }

    await user.click(within(accentGroup).getByRole('button', { name: 'Orange' }))

    for (const accent of ACCENTS) {
      expect(
        within(accentGroup).getByRole('button', { name: accent.label }),
        `${accent.label} pressed state`,
      ).toHaveAttribute('aria-pressed', accent.value === 'orange' ? 'true' : 'false')
    }
  })
})

describe('accent tokens', () => {
  it('defines --mint, --mint-strong and --on-mint for every accent in both themes', () => {
    for (const accent of ACCENTS) {
      for (const theme of ['dark', 'light'] as const) {
        const tokens = readBlock(accentSelector(accent.value, theme))
        for (const token of ['--mint', '--mint-strong', '--on-mint']) {
          expect(
            tokens[token],
            `${theme}/${accent.value} ${accentSelector(accent.value, theme)} does not define ${token}`,
          ).toMatch(/^#[0-9a-f]{6}$/i)
        }
      }
    }
  })

  it('keeps --on-mint at 4.5:1 or better on --mint and --mint-strong for all sixteen pairs', () => {
    const failures: string[] = []

    for (const accent of ACCENTS) {
      for (const theme of ['dark', 'light'] as const) {
        const tokens = readBlock(accentSelector(accent.value, theme))
        for (const background of ['--mint', '--mint-strong'] as const) {
          const ratio = contrastRatio(tokens['--on-mint'], tokens[background])
          if (ratio < MIN_RATIO) {
            failures.push(
              `${theme}/${accent.value}: --on-mint ${tokens['--on-mint']} on ${background} ${tokens[background]} = ${ratio.toFixed(2)}:1`,
            )
          }
        }
      }
    }

    expect(
      failures,
      `WCAG AA normal-text contrast must be >= ${MIN_RATIO}:1:\n${failures.join('\n')}`,
    ).toEqual([])
  })

  it('keeps every accent distinct from the --info and --warning status hues', () => {
    const collisions: string[] = []

    for (const theme of ['dark', 'light'] as const) {
      const base = readBlock(theme === 'light' ? '[data-theme="light"]' : ':root')
      for (const accent of ACCENTS) {
        const tokens = readBlock(accentSelector(accent.value, theme))
        for (const statusToken of ['--info', '--warning']) {
          if (tokens['--mint'].toLowerCase() === base[statusToken].toLowerCase()) {
            collisions.push(
              `${theme}/${accent.value}: --mint ${tokens['--mint']} equals ${statusToken} ${base[statusToken]}`,
            )
          }
        }
      }
    }

    expect(collisions, `an accent must never read as a status colour:\n${collisions.join('\n')}`).toEqual(
      [],
    )
  })

  it('applies the matching override tokens through the document cascade', () => {
    for (const accent of ACCENTS) {
      for (const theme of ['dark', 'light'] as const) {
        expect(computedAccent(theme, accent.value), `${theme}/${accent.value}`).toEqual(
          ACCENT_TOKENS[accent.value][theme],
        )
      }
    }
  })
})
