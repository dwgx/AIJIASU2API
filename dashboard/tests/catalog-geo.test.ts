import { describe, expect, it } from 'vitest'
import CSS from '../src/styles/app.css?raw'

/**
 * DESIGN-CATALOG §4.2/§7.4: the map's lowest non-zero fill must clear 3:1
 * against the card surface in both themes. The fills are tokens, so this test
 * reads the stylesheet itself rather than a copy of the values.
 */

function themeBlock(selector: string): Record<string, string> {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = CSS.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`))
  if (!match) throw new Error(`theme block not found: ${selector}`)
  const tokens: Record<string, string> = {}
  for (const declaration of match[1].split(';')) {
    const [name, value] = declaration.split(':')
    if (name?.trim().startsWith('--')) tokens[name.trim()] = value.trim()
  }
  return tokens
}

function channel(value: number): number {
  const normalized = value / 255
  return normalized <= 0.04045 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4
}

function luminance(hex: string): number {
  const value = hex.replace('#', '')
  const [red, green, blue] = [0, 2, 4].map((offset) =>
    channel(Number.parseInt(value.slice(offset, offset + 2), 16)),
  )
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue
}

function contrast(left: string, right: string): number {
  const [high, low] = [luminance(left), luminance(right)].sort((a, b) => b - a)
  return (high + 0.05) / (low + 0.05)
}

const THEMES = [
  { name: 'dark', selector: ':root' },
  { name: 'light', selector: '[data-theme="light"]' },
]

describe('catalog map fill contrast', () => {
  for (const theme of THEMES) {
    it(`${theme.name} theme clears 3:1 against the surface`, () => {
      const tokens = themeBlock(theme.selector)
      const surface = tokens['--surface']
      expect(surface).toMatch(/^#[0-9a-f]{6}$/i)

      const fills = [1, 2, 3, 4, 5].map((bin) => {
        const fill = tokens[`--map-fill-${bin}`]
        expect(fill, `--map-fill-${bin}`).toMatch(/^#[0-9a-f]{6}$/i)
        return { bin, fill: fill as string, ratio: contrast(fill as string, surface) }
      })

      for (const fill of fills) {
        expect(fill.ratio, `bin ${fill.bin} ${fill.fill}`).toBeGreaterThanOrEqual(3)
      }
      // The ramp must stay ordered: each bin is visibly distinct from the
      // previous one, so the legend remains readable.
      for (let index = 1; index < fills.length; index += 1) {
        expect(
          contrast(fills[index].fill, fills[index - 1].fill),
          `bin ${fills[index].bin} vs ${fills[index - 1].bin}`,
        ).toBeGreaterThanOrEqual(1.2)
      }
      // Dark theme lightens with count, light theme darkens with count.
      const ratios = fills.map((fill) => fill.ratio)
      const ordered = ratios.every((ratio, index) => index === 0 || ratio > ratios[index - 1])
      expect(ordered).toBe(true)
    })
  }
})
