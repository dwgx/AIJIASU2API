/* MorphIcon — APC's stroke-icon morph component.
 *
 * Adapted from morphicons (https://github.com/guillermolg00/morphicons),
 * MIT License, © Guillermo (guillermolg.com). The pure core (path parsing,
 * normalization, resampling, Procrustes planning, polar interpolation and the
 * spring) lives in `src/lib/iconMorph.ts`; this file is the React driver.
 *
 * MIT License
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
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
} from '../../lib/iconMorph'
import type {
  MorphGlyph,
  MorphPlan,
  Shape,
  SpringInput,
} from '../../lib/iconMorph'
import { useMediaQuery } from '../../lib/useMediaQuery'

export interface MorphIconProps {
  /** Uncontrolled mode: the glyph to show; changing it animates. */
  icon?: MorphGlyph
  /** Controlled mode: pair endpoints. Requires `progress`. */
  from?: MorphGlyph
  to?: MorphGlyph
  /** Controlled progress in 0..1 (no spring). */
  progress?: number
  size?: number
  strokeWidth?: number
  /** "snappy" (default), "smooth", "bouncy" or { stiffness, damping }. */
  spring?: SpringInput
  /** Force the instant swap. Default: follow prefers-reduced-motion. */
  reducedMotion?: boolean
  /** Accessible name. Without it the icon is decorative (aria-hidden). */
  'aria-label'?: string
  className?: string
}

interface PathPaint {
  d: string
  opacity: number
}

interface Flight {
  plan: MorphPlan
  target: MorphGlyph
}

function glyphPaints(glyph: MorphGlyph): PathPaint[] {
  return glyph.paths.map((d) => ({ d, opacity: 1 }))
}

function shapeFromOutputs(plan: MorphPlan, out: Float64Array[], opacity: number[]): Shape {
  return {
    items: plan.items.map((item, i) => ({
      pts: out[i].slice(),
      closed: item.closed,
      opacity: opacity[i],
    })),
  }
}

/** Mid-flight paints at a frozen progress (controlled mode). */
function interpolatedPaints(plan: MorphPlan, t: number): PathPaint[] {
  const out = allocOutputs(plan)
  const opacity = new Array<number>(plan.items.length)
  interpolate(plan, t, out)
  opacities(plan, t, opacity)
  return plan.items.map((item, i) => ({
    d: serializePts(out[i], item.closed),
    opacity: opacity[i],
  }))
}

export function MorphIcon({
  icon,
  from,
  to,
  progress,
  size = 18,
  strokeWidth = 1.6,
  spring,
  reducedMotion,
  'aria-label': ariaLabel,
  className,
}: MorphIconProps) {
  const controlled = from !== undefined && to !== undefined && progress !== undefined
  const target = controlled ? undefined : (icon ?? to ?? from)
  const prefersReduced = useMediaQuery('(prefers-reduced-motion: reduce)')
  const instant = reducedMotion ?? prefersReduced

  const [flight, setFlight] = useState<Flight | null>(null)
  const currentShape = useRef<Shape | null>(null)
  const currentGlyph = useRef<MorphGlyph | undefined>(target)
  const liveFrame = useRef<{ plan: MorphPlan; out: Float64Array[]; opacity: number[] } | null>(
    null,
  )
  const skipCapture = useRef(false)
  const prevTarget = useRef<MorphGlyph | undefined>(target)
  const pathRefs = useRef<Array<SVGPathElement | null>>([])
  const springRef = useRef(spring)
  springRef.current = spring

  if (currentShape.current === null && target) currentShape.current = sampleGlyph(target)

  // Uncontrolled: a new glyph starts a spring from whatever is on screen.
  useEffect(() => {
    if (controlled || !target) return
    if (target === prevTarget.current) return
    const previous = prevTarget.current
    prevTarget.current = target
    if (instant) {
      if (liveFrame.current) skipCapture.current = true
      currentShape.current = sampleGlyph(target)
      currentGlyph.current = target
      setFlight(null)
      return
    }
    // Settled → settled transitions reuse the cached pair plan; an
    // interrupted flight plans from the shape actually on screen.
    const live = liveFrame.current
    const plan =
      previous !== undefined && currentGlyph.current === previous
        ? planForGlyphs(previous, target)
        : buildPlan(
            live
              ? shapeFromOutputs(live.plan, live.out, live.opacity)
              : (currentShape.current ?? sampleGlyph(target)),
            sampleGlyph(target),
          )
    setFlight({ plan, target })
  }, [target, controlled, instant])

  // Reduced motion can turn on mid-flight: settle at the target immediately.
  useEffect(() => {
    if (!instant || !flight) return
    skipCapture.current = true
    currentShape.current = sampleGlyph(flight.target)
    currentGlyph.current = flight.target
    setFlight(null)
  }, [instant, flight])

  // One rAF driver per flight. Each frame only rewrites `d`/`opacity`.
  useEffect(() => {
    if (!flight) return
    const { plan } = flight
    const out = allocOutputs(plan)
    const opacity = new Array<number>(plan.items.length)
    liveFrame.current = { plan, out, opacity }
    const spring = new Spring()
    spring.config(springConfig(springRef.current))
    spring.start()
    let raf = 0
    let last = 0
    let alive = true

    const paint = (t: number) => {
      interpolate(plan, t, out)
      opacities(plan, t, opacity)
      for (let i = 0; i < plan.items.length; i++) {
        const el = pathRefs.current[i]
        if (!el) continue
        el.setAttribute('d', serializePts(out[i], plan.items[i].closed))
        el.setAttribute('opacity', String(opacity[i]))
      }
    }

    paint(0)
    const frame = (now: number) => {
      if (!alive) return
      const dt = last === 0 ? 1 / 60 : Math.min(0.064, (now - last) / 1000)
      last = now
      const settled = spring.step(dt)
      paint(spring.x)
      if (settled) {
        currentShape.current = shapeFromOutputs(plan, out, opacity)
        currentGlyph.current = flight.target
        skipCapture.current = true
        setFlight(null)
        return
      }
      raf = requestAnimationFrame(frame)
    }
    raf = requestAnimationFrame(frame)

    return () => {
      alive = false
      cancelAnimationFrame(raf)
      if (liveFrame.current?.plan === plan) liveFrame.current = null
      if (skipCapture.current) skipCapture.current = false
      else {
        currentShape.current = shapeFromOutputs(plan, out, opacity)
        currentGlyph.current = undefined
      }
    }
  }, [flight])

  const controlledPlan = useMemo(
    () => (controlled && from && to ? planForGlyphs(from, to) : null),
    [controlled, from, to],
  )
  const controlledPaints = useMemo(() => {
    if (!controlled || !from || !to) return null
    const t = progress ?? 0
    if (t <= 0) return glyphPaints(from)
    if (t >= 1) return glyphPaints(to)
    return interpolatedPaints(controlledPlan ?? planForGlyphs(from, to), t)
  }, [controlled, from, to, progress, controlledPlan])

  let paints: PathPaint[]
  if (controlledPaints) paints = controlledPaints
  else if (flight) {
    paints = flight.plan.items.map((item) => ({
      d: serializePts(item.a, item.closed),
      opacity: item.oa,
    }))
  } else if (target) paints = glyphPaints(target)
  else paints = []

  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      role={ariaLabel ? 'img' : undefined}
      aria-label={ariaLabel}
      aria-hidden={ariaLabel ? undefined : true}
      focusable="false"
    >
      {paints.map((paint, i) =>
        flight ? (
          <path
            key={i}
            ref={(el) => {
              pathRefs.current[i] = el
            }}
            d={paint.d}
            opacity={paint.opacity}
          />
        ) : (
          <path key={i} d={paint.d} opacity={paint.opacity} />
        ),
      )}
    </svg>
  )
}
