import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'
import { MorphIcon } from '../src/components/icons/MorphIcon'
import {
  HAN_GLYPH,
  LATIN_GLYPH,
  MOON_GLYPH,
  NODE_DRAINED_GLYPH,
  NODE_LIVE_GLYPH,
  PANEL_COLLAPSE_GLYPH,
  PANEL_EXPAND_GLYPH,
  STATUS_GLYPHS,
  SUN_GLYPH,
} from '../src/components/icons/morphGlyphs'
import {
  allocOutputs,
  buildPlan,
  interpolate,
  opacities,
  planForGlyphs,
  sampleGlyph,
  serializePts,
  Spring,
  springConfig,
  SPRING_PRESETS,
} from '../src/lib/iconMorph'

/* A deterministic rAF: the driver only schedules through this queue. */
let queue: FrameRequestCallback[] = []
let raf: Mock<(cb: FrameRequestCallback) => number>
let caf: Mock<(id: number) => void>
let clock = 0

beforeEach(() => {
  queue = []
  clock = 0
  let id = 0
  raf = vi.fn((cb: FrameRequestCallback) => {
    queue.push(cb)
    return ++id
  })
  caf = vi.fn()
  vi.stubGlobal('requestAnimationFrame', raf)
  vi.stubGlobal('cancelAnimationFrame', caf)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

function frames(durationMs: number, step = 16) {
  act(() => {
    const end = clock + durationMs
    while (clock < end) {
      clock += step
      const pending = queue
      queue = []
      for (const cb of pending) cb(clock)
    }
  })
}

function pathData(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll('path')).map((p) => p.getAttribute('d') ?? '')
}

describe('morph planning', () => {
  it('samples every subpath at the same point count', () => {
    const shape = sampleGlyph(PANEL_COLLAPSE_GLYPH)
    expect(shape.items).toHaveLength(3)
    for (const item of shape.items) expect(item.pts).toHaveLength(48 * 2)
  })

  it('pairs identical frames with zero residual and no rotation', () => {
    const plan = buildPlan(sampleGlyph(PANEL_COLLAPSE_GLYPH), sampleGlyph(PANEL_COLLAPSE_GLYPH))
    expect(plan.items).toHaveLength(3)
    for (const item of plan.items) {
      expect(item.fade).toBe(-1)
      expect(item.theta).toBeCloseTo(0, 6)
      expect(item.lnSigma).toBeCloseTo(0, 6)
    }
  })

  it('flips the panel chevron by 180° while the frame stays put', () => {
    const plan = planForGlyphs(PANEL_COLLAPSE_GLYPH, PANEL_EXPAND_GLYPH)
    expect(plan.items).toHaveLength(3)
    const chevron = plan.items.find((item) => Math.abs(item.theta) > 1)
    expect(chevron).toBeDefined()
    expect(Math.abs(Math.abs(chevron!.theta) - Math.PI)).toBeLessThan(0.05)
    expect(Math.exp(chevron!.lnSigma)).toBeCloseTo(1, 2)
  })

  it('retracts sun rays and grows them back for the reverse morph', () => {
    const toMoon = planForGlyphs(SUN_GLYPH, MOON_GLYPH)
    expect(toMoon.items).toHaveLength(9)
    expect(toMoon.items.filter((item) => item.fade === 0)).toHaveLength(8)
    expect(toMoon.items.filter((item) => item.fade === -1)).toHaveLength(1)

    const toSun = planForGlyphs(MOON_GLYPH, SUN_GLYPH)
    expect(toSun.items).toHaveLength(9)
    expect(toSun.items.filter((item) => item.fade === 1)).toHaveLength(8)

    const out = allocOutputs(toMoon)
    const opacity = new Array<number>(toMoon.items.length)
    opacities(toMoon, 1, opacity)
    interpolate(toMoon, 1, out)
    expect(opacity.filter((value) => value === 0)).toHaveLength(8)
  })

  it('morphs the language frame and status marks without leftovers on equal counts', () => {
    const language = planForGlyphs(HAN_GLYPH, LATIN_GLYPH)
    expect(language.items).toHaveLength(2)
    expect(language.items.every((item) => item.fade === -1)).toBe(true)

    const okToWarn = planForGlyphs(STATUS_GLYPHS.ok, STATUS_GLYPHS.warn)
    expect(okToWarn.items.filter((item) => item.fade === 1)).toHaveLength(1)
    const warnToOk = planForGlyphs(STATUS_GLYPHS.warn, STATUS_GLYPHS.ok)
    expect(warnToOk.items.filter((item) => item.fade === 0)).toHaveLength(1)
  })

  it('keeps the node power frame and fades the strike-through', () => {
    const plan = planForGlyphs(NODE_LIVE_GLYPH, NODE_DRAINED_GLYPH)
    expect(plan.items).toHaveLength(3)
    expect(plan.items.filter((item) => item.fade === 1)).toHaveLength(1)
    expect(plan.items.filter((item) => item.fade === -1)).toHaveLength(2)
  })

  it('hits both endpoints exactly in interpolation', () => {
    const from = sampleGlyph(PANEL_COLLAPSE_GLYPH)
    const to = sampleGlyph(PANEL_EXPAND_GLYPH)
    const plan = buildPlan(from, to)
    const out = allocOutputs(plan)
    interpolate(plan, 0, out)
    for (let k = 0; k < plan.items.length; k++) {
      for (let i = 0; i < out[k].length; i++) expect(out[k][i]).toBeCloseTo(plan.items[k].a[i], 9)
    }
    interpolate(plan, 1, out)
    for (let k = 0; k < plan.items.length; k++) {
      const c = plan.items[k].cb
      let sx = 0
      let sy = 0
      const n = out[k].length / 2
      for (let i = 0; i < n; i++) {
        sx += out[k][2 * i]
        sy += out[k][2 * i + 1]
      }
      expect(sx / n).toBeCloseTo(c[0], 6)
      expect(sy / n).toBeCloseTo(c[1], 6)
    }
  })

  it('serializes mid-flight polylines with a closing Z only when closed', () => {
    const pts = Float64Array.from([1, 2, 3.456, 4.444])
    expect(serializePts(pts, false)).toBe('M1 2L3.46 4.44')
    expect(serializePts(pts, true)).toBe('M1 2L3.46 4.44Z')
  })

  it('uses the snappy spring by default and settles at 1', () => {
    expect(springConfig(undefined)).toEqual(SPRING_PRESETS.snappy)
    const spring = new Spring()
    spring.start()
    let settled = false
    let elapsed = 0
    while (!settled && elapsed < 2) {
      settled = spring.step(1 / 60)
      elapsed += 1 / 60
    }
    expect(settled).toBe(true)
    expect(spring.x).toBeCloseTo(1, 2)
  })
})

describe('MorphIcon', () => {
  it('renders the resting glyph verbatim and morphs on prop change', () => {
    const { container, rerender } = render(
      <MorphIcon icon={PANEL_COLLAPSE_GLYPH} size={17} strokeWidth={1.6} />,
    )
    expect(pathData(container)).toEqual(PANEL_COLLAPSE_GLYPH.paths)
    expect(raf).not.toHaveBeenCalled()

    rerender(<MorphIcon icon={PANEL_EXPAND_GLYPH} size={17} strokeWidth={1.6} />)
    // The flight starts from the on-screen shape as a polyline.
    expect(raf).toHaveBeenCalledTimes(1)
    expect(pathData(container)[0]).toContain('L')

    frames(160)
    const midway = pathData(container)
    expect(midway).not.toEqual(PANEL_COLLAPSE_GLYPH.paths)
    expect(midway).not.toEqual(PANEL_EXPAND_GLYPH.paths)

    frames(2000)
    expect(pathData(container)).toEqual(PANEL_EXPAND_GLYPH.paths)
    expect(caf).toHaveBeenCalled()
  })

  it('swaps instantly under reduced motion and never schedules a frame', () => {
    const { container, rerender } = render(
      <MorphIcon icon={PANEL_COLLAPSE_GLYPH} reducedMotion />,
    )
    rerender(<MorphIcon icon={PANEL_EXPAND_GLYPH} reducedMotion />)
    expect(pathData(container)).toEqual(PANEL_EXPAND_GLYPH.paths)
    expect(raf).not.toHaveBeenCalled()
  })

  it('honors the OS prefers-reduced-motion setting', () => {
    const original = window.matchMedia
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      writable: true,
      value: (query: string) => ({
        matches: query.includes('prefers-reduced-motion'),
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      }),
    })
    try {
      const { container, rerender } = render(<MorphIcon icon={MOON_GLYPH} />)
      rerender(<MorphIcon icon={SUN_GLYPH} />)
      expect(pathData(container)).toEqual(SUN_GLYPH.paths)
      expect(raf).not.toHaveBeenCalled()
    } finally {
      Object.defineProperty(window, 'matchMedia', { configurable: true, writable: true, value: original })
    }
  })

  it('drives a frozen pair from progress and stays exact at the endpoints', () => {
    const { container, rerender } = render(
      <MorphIcon from={PANEL_COLLAPSE_GLYPH} to={PANEL_EXPAND_GLYPH} progress={0} />,
    )
    expect(pathData(container)).toEqual(PANEL_COLLAPSE_GLYPH.paths)
    expect(raf).not.toHaveBeenCalled()

    rerender(<MorphIcon from={PANEL_COLLAPSE_GLYPH} to={PANEL_EXPAND_GLYPH} progress={0.5} />)
    const midway = pathData(container)
    expect(midway).not.toEqual(PANEL_COLLAPSE_GLYPH.paths)
    expect(midway).not.toEqual(PANEL_EXPAND_GLYPH.paths)

    rerender(<MorphIcon from={PANEL_COLLAPSE_GLYPH} to={PANEL_EXPAND_GLYPH} progress={1} />)
    expect(pathData(container)).toEqual(PANEL_EXPAND_GLYPH.paths)
    expect(raf).not.toHaveBeenCalled()
  })

  it('exposes an accessible name only when asked', () => {
    const { container, rerender } = render(
      <MorphIcon icon={STATUS_GLYPHS.ok} aria-label="Healthy" />,
    )
    expect(screen.getByRole('img', { name: 'Healthy' })).toBeInTheDocument()

    rerender(<MorphIcon icon={STATUS_GLYPHS.ok} />)
    expect(container.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
    expect(container.querySelector('svg')).not.toHaveAttribute('aria-label')
  })

  it('cancels the frame loop when unmounted mid-flight', () => {
    const { rerender, unmount } = render(<MorphIcon icon={SUN_GLYPH} />)
    rerender(<MorphIcon icon={MOON_GLYPH} />)
    frames(48)
    unmount()
    expect(caf).toHaveBeenCalled()
    const scheduled = raf.mock.calls.length
    // A cancelled callback that still fires must not schedule another frame.
    act(() => {
      for (const cb of queue) cb(1000)
    })
    expect(raf.mock.calls.length).toBe(scheduled)
  })

  it('restarts from the shape on screen when the target changes mid-flight', () => {
    const { container, rerender } = render(<MorphIcon icon={SUN_GLYPH} />)
    rerender(<MorphIcon icon={MOON_GLYPH} />)
    frames(80)
    rerender(<MorphIcon icon={SUN_GLYPH} />)
    // The new flight starts from the shape on screen (a polyline), not from
    // the canonical sun or moon glyph strings.
    const restarted = pathData(container)
    expect(restarted[0]).toContain('L')
    expect(restarted).not.toEqual(SUN_GLYPH.paths)
    expect(restarted).not.toEqual(MOON_GLYPH.paths)
    frames(2000)
    expect(pathData(container)).toEqual(SUN_GLYPH.paths)
  })
})
