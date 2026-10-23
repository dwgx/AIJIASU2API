import { describe, expect, it } from 'vitest'
import appCss from '../src/styles/app.css?raw'

const MIN_RATIO = 4.5

const TEXT_TOKENS = ['--text', '--text-2', '--muted', '--text-tertiary'] as const
const SURFACE_TOKENS = ['--bg', '--surface', '--elevated', '--sidebar'] as const

const THEMES = [
  { name: 'dark', selector: ':root', block: /:root\s*\{([^}]*)\}/ },
  { name: 'light', selector: '[data-theme="light"]', block: /\[data-theme="light"\]\s*\{([^}]*)\}/ },
] as const

type Theme = (typeof THEMES)[number]

function readTokens(theme: Theme): Record<string, string> {
  const match = appCss.match(theme.block)
  if (!match) {
    throw new Error(`app.css no longer has a ${theme.selector} token block`)
  }

  const tokens: Record<string, string> = {}
  for (const [, name, value] of match[1].matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    tokens[name] = value.trim()
  }
  return tokens
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

describe('theme token contrast', () => {
  it('defines every audited text and surface token in both themes', () => {
    for (const theme of THEMES) {
      const tokens = readTokens(theme)
      for (const token of [...TEXT_TOKENS, ...SURFACE_TOKENS]) {
        expect(tokens[token], `${theme.name} theme does not define ${token}`).toMatch(
          /^#[0-9a-f]{6}$/i,
        )
      }
    }
  })

  it('keeps every text token at 4.5:1 or better on every surface', () => {
    const failures: string[] = []

    for (const theme of THEMES) {
      const tokens = readTokens(theme)
      for (const foreground of TEXT_TOKENS) {
        for (const background of SURFACE_TOKENS) {
          const ratio = contrastRatio(tokens[foreground], tokens[background])
          if (ratio < MIN_RATIO) {
            failures.push(
              `${theme.name}: ${foreground} ${tokens[foreground]} on ${background} ${tokens[background]} = ${ratio.toFixed(2)}:1`,
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
})
