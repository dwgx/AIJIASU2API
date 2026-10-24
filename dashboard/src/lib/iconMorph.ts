/* APC icon morph core — pure functions, no DOM.
 *
 * Adapted from morphicons (https://github.com/guillermolg00/morphicons),
 * MIT License, © Guillermo (guillermolg.com):
 *   - SVG path parsing and primitive → cubic normalization
 *   - arc-length resampling with anchored corners
 *   - closed-form 2D Procrustes correspondence/alignment
 *   - polar (rotation/scale/translation) interpolation
 *   - damped-spring integration and presets
 *
 * Changed for APC: a glyph is a small list of single-subpath `d` strings on
 * the 24×24 grid (rather than Lucide IconNodes); subpaths pair one-to-one by
 * centroid/length and leftovers collapse into a point with an opacity fade
 * instead of duplicating a target subpath.
 *
 * The MIT license text of the original work is reproduced in
 * `dashboard/src/components/icons/MorphIcon.tsx` alongside the attribution.
 */

/** A morphable glyph: one `d` string per subpath, drawn on a 24×24 grid. */
export interface MorphGlyph {
  readonly paths: readonly string[]
}

/** Number of points each subpath is sampled at. 48 is smooth at 24px. */
export const MORPH_SAMPLES = 48

export interface SampledPath {
  pts: Float64Array
  closed: boolean
  opacity: number
}

export interface Shape {
  items: SampledPath[]
}

/* ---------------------------------------------------------------------- */
/* Path parsing → cubic Béziers                                            */
/* ---------------------------------------------------------------------- */

type RawSeg =
  | readonly ['L', number, number]
  | readonly ['C', number, number, number, number, number, number]
  | readonly ['Q', number, number, number, number]
  | readonly ['A', number, number, number, 0 | 1, 0 | 1, number, number]

interface RawSubpath {
  x0: number
  y0: number
  segs: RawSeg[]
  closed: boolean
}

const COMMANDS = 'MmLlHhVvCcSsQqTtAaZz'

export function parsePath(d: string): RawSubpath[] {
  const subs: RawSubpath[] = []
  const n = d.length
  let i = 0
  let cx = 0
  let cy = 0
  let sx = 0
  let sy = 0
  let cur: RawSubpath | null = null
  let cmd = ''
  let px = 0
  let py = 0
  let prev: 'C' | 'Q' | '' = ''
  let started = false

  const err = (msg: string): never => {
    throw new Error(`iconMorph: ${msg} at d[${i}]`)
  }
  const isDigit = (c: number) => c >= 48 && c <= 57
  const skip = () => {
    while (i < n) {
      const c = d.charCodeAt(i)
      if (c === 32 || c === 9 || c === 10 || c === 13 || c === 12 || c === 44) i++
      else break
    }
  }
  const num = (): number => {
    skip()
    const start = i
    if (i < n && (d[i] === '+' || d[i] === '-')) i++
    let dig = false
    while (i < n && isDigit(d.charCodeAt(i))) {
      i++
      dig = true
    }
    if (i < n && d[i] === '.') {
      i++
      while (i < n && isDigit(d.charCodeAt(i))) {
        i++
        dig = true
      }
    }
    if (!dig) err('expected number')
    if (i < n && (d[i] === 'e' || d[i] === 'E')) {
      const save = i
      i++
      if (i < n && (d[i] === '+' || d[i] === '-')) i++
      let ed = false
      while (i < n && isDigit(d.charCodeAt(i))) {
        i++
        ed = true
      }
      if (!ed) i = save
    }
    return Number(d.slice(start, i))
  }
  const flag = (): 0 | 1 => {
    skip()
    const c = d[i]
    if (c === '0' || c === '1') {
      i++
      return c === '1' ? 1 : 0
    }
    return err('expected arc flag (0|1)')
  }
  const open = (): RawSubpath => {
    if (!started) err('path must start with M/m')
    if (!cur) {
      cur = { x0: cx, y0: cy, segs: [], closed: false }
      subs.push(cur)
    }
    return cur
  }

  let rel = false
  const nx = () => num() + (rel ? cx : 0)
  const ny = () => num() + (rel ? cy : 0)

  while (true) {
    skip()
    if (i >= n) break
    const ch = d[i] as string
    if (COMMANDS.includes(ch)) {
      cmd = ch
      i++
    } else if (cmd === '') {
      err('path must start with M/m')
    } else if (cmd === 'M') {
      cmd = 'L'
    } else if (cmd === 'm') {
      cmd = 'l'
    } else if (cmd === 'Z' || cmd === 'z') {
      err('stray data after Z')
    }
    rel = cmd >= 'a'

    switch (rel ? cmd.toUpperCase() : cmd) {
      case 'M': {
        started = true
        const x = nx()
        const y = ny()
        cx = x
        cy = y
        sx = x
        sy = y
        cur = { x0: x, y0: y, segs: [], closed: false }
        subs.push(cur)
        prev = ''
        break
      }
      case 'L': {
        const x = nx()
        const y = ny()
        open().segs.push(['L', x, y])
        cx = x
        cy = y
        prev = ''
        break
      }
      case 'H': {
        const x = nx()
        open().segs.push(['L', x, cy])
        cx = x
        prev = ''
        break
      }
      case 'V': {
        const y = ny()
        open().segs.push(['L', cx, y])
        cy = y
        prev = ''
        break
      }
      case 'C':
      case 'S': {
        let x1: number
        let y1: number
        if (cmd === 'C' || cmd === 'c') {
          x1 = nx()
          y1 = ny()
        } else {
          x1 = prev === 'C' ? 2 * cx - px : cx
          y1 = prev === 'C' ? 2 * cy - py : cy
        }
        const x2 = nx()
        const y2 = ny()
        const x = nx()
        const y = ny()
        open().segs.push(['C', x1, y1, x2, y2, x, y])
        px = x2
        py = y2
        cx = x
        cy = y
        prev = 'C'
        break
      }
      case 'Q':
      case 'T': {
        let x1: number
        let y1: number
        if (cmd === 'Q' || cmd === 'q') {
          x1 = nx()
          y1 = ny()
        } else {
          x1 = prev === 'Q' ? 2 * cx - px : cx
          y1 = prev === 'Q' ? 2 * cy - py : cy
        }
        const x = nx()
        const y = ny()
        open().segs.push(['Q', x1, y1, x, y])
        px = x1
        py = y1
        cx = x
        cy = y
        prev = 'Q'
        break
      }
      case 'A': {
        const rx = num()
        const ry = num()
        const rot = num()
        const large = flag()
        const sweep = flag()
        const x = nx()
        const y = ny()
        open().segs.push(['A', rx, ry, rot, large, sweep, x, y])
        cx = x
        cy = y
        prev = ''
        break
      }
      case 'Z': {
        if (cur) {
          cur.closed = true
          cur = null
        }
        cx = sx
        cy = sy
        prev = ''
        break
      }
      default:
        err(`unsupported command "${cmd}"`)
    }
  }
  return subs.filter((s) => s.segs.length > 0)
}

const TAU = Math.PI * 2

interface CubicPath {
  pts: Float64Array
  closed: boolean
}

/** Convert one parsed subpath to a chain of cubics:
 *  [p0, c1, c2, p1, c1', c2', p2, …]. */
function toCubic(raw: RawSubpath): CubicPath | null {
  const pts: number[] = [raw.x0, raw.y0]
  let cx = raw.x0
  let cy = raw.y0

  const cubic = (x1: number, y1: number, x2: number, y2: number, x: number, y: number) => {
    pts.push(x1, y1, x2, y2, x, y)
    cx = x
    cy = y
  }
  const line = (x: number, y: number) => {
    if (Math.abs(x - cx) < 1e-12 && Math.abs(y - cy) < 1e-12) return
    cubic(cx + (x - cx) / 3, cy + (y - cy) / 3, cx + (2 * (x - cx)) / 3, cy + (2 * (y - cy)) / 3, x, y)
  }
  const quad = (x1: number, y1: number, x: number, y: number) => {
    cubic(
      cx + (2 / 3) * (x1 - cx),
      cy + (2 / 3) * (y1 - cy),
      x + (2 / 3) * (x1 - x),
      y + (2 / 3) * (y1 - y),
      x,
      y,
    )
  }
  const arc = (
    rx0: number,
    ry0: number,
    rotDeg: number,
    large: 0 | 1,
    sweep: 0 | 1,
    x: number,
    y: number,
  ) => {
    const x1 = cx
    const y1 = cy
    if (Math.abs(x - x1) < 1e-12 && Math.abs(y - y1) < 1e-12) return
    let rx = Math.abs(rx0)
    let ry = Math.abs(ry0)
    if (rx < 1e-12 || ry < 1e-12) {
      line(x, y)
      return
    }
    const phi = (rotDeg * Math.PI) / 180
    const cosP = Math.cos(phi)
    const sinP = Math.sin(phi)
    const hx = (x1 - x) / 2
    const hy = (y1 - y) / 2
    const x1p = cosP * hx + sinP * hy
    const y1p = -sinP * hx + cosP * hy
    const lam = (x1p * x1p) / (rx * rx) + (y1p * y1p) / (ry * ry)
    if (lam > 1) {
      const s = Math.sqrt(lam)
      rx *= s
      ry *= s
    }
    const rx2 = rx * rx
    const ry2 = ry * ry
    const xp2 = x1p * x1p
    const yp2 = y1p * y1p
    let rad = (rx2 * ry2 - rx2 * yp2 - ry2 * xp2) / (rx2 * yp2 + ry2 * xp2)
    if (rad < 0) rad = 0
    const co = (large === sweep ? -1 : 1) * Math.sqrt(rad)
    const cxp = (co * rx * y1p) / ry
    const cyp = (-co * ry * x1p) / rx
    const ccx = cosP * cxp - sinP * cyp + (x1 + x) / 2
    const ccy = sinP * cxp + cosP * cyp + (y1 + y) / 2
    const th1 = Math.atan2((y1p - cyp) / ry, (x1p - cxp) / rx)
    let dth = Math.atan2((-y1p - cyp) / ry, (-x1p - cxp) / rx) - th1
    if (sweep === 0 && dth > 0) dth -= TAU
    else if (sweep === 1 && dth < 0) dth += TAU
    const slices = Math.max(1, Math.ceil(Math.abs(dth) / (Math.PI / 2) - 1e-9))
    const delta = dth / slices
    const alpha = (4 / 3) * Math.tan(delta / 4)
    const ex = (t: number) => ccx + rx * Math.cos(t) * cosP - ry * Math.sin(t) * sinP
    const ey = (t: number) => ccy + rx * Math.cos(t) * sinP + ry * Math.sin(t) * cosP
    const dx = (t: number) => -rx * Math.sin(t) * cosP - ry * Math.cos(t) * sinP
    const dy = (t: number) => -rx * Math.sin(t) * sinP + ry * Math.cos(t) * cosP
    let t0 = th1
    let p0x = x1
    let p0y = y1
    for (let s = 1; s <= slices; s++) {
      const t1 = th1 + delta * s
      const p1x = s === slices ? x : ex(t1)
      const p1y = s === slices ? y : ey(t1)
      cubic(p0x + alpha * dx(t0), p0y + alpha * dy(t0), p1x - alpha * dx(t1), p1y - alpha * dy(t1), p1x, p1y)
      t0 = t1
      p0x = p1x
      p0y = p1y
    }
  }

  for (const s of raw.segs) {
    switch (s[0]) {
      case 'L':
        line(s[1], s[2])
        break
      case 'C':
        cubic(s[1], s[2], s[3], s[4], s[5], s[6])
        break
      case 'Q':
        quad(s[1], s[2], s[3], s[4])
        break
      case 'A':
        arc(s[1], s[2], s[3], s[4], s[5], s[6], s[7])
        break
    }
  }
  if (raw.closed) line(pts[0], pts[1])
  if (pts.length < 8) return null
  return { pts: Float64Array.from(pts), closed: raw.closed }
}

/* ---------------------------------------------------------------------- */
/* Arc-length resampling with anchored corners                             */
/* ---------------------------------------------------------------------- */

const CORNER_THRESHOLD = Math.PI / 8
const GX = [0.18343464249564978, 0.525532409916329, 0.7966664774136267, 0.9602898564975363]
const GW = [0.362683783378362, 0.31370664587788727, 0.22238103445337448, 0.10122853629037626]

function speed(p: Float64Array, k: number, t: number): number {
  const i = 6 * k
  const u = 1 - t
  const c0 = 3 * u * u
  const c1 = 6 * u * t
  const c2 = 3 * t * t
  const dx = c0 * (p[i + 2] - p[i]) + c1 * (p[i + 4] - p[i + 2]) + c2 * (p[i + 6] - p[i + 4])
  const dy = c0 * (p[i + 3] - p[i + 1]) + c1 * (p[i + 5] - p[i + 3]) + c2 * (p[i + 7] - p[i + 5])
  return Math.hypot(dx, dy)
}

function segLen(p: Float64Array, k: number, t1 = 1): number {
  const half = t1 / 2
  let s = 0
  for (let j = 0; j < 4; j++)
    s += GW[j] * (speed(p, k, half + half * GX[j]) + speed(p, k, half - half * GX[j]))
  return s * half
}

function pointAt(p: Float64Array, k: number, t: number, out: Float64Array, o: number): void {
  const i = 6 * k
  const u = 1 - t
  const b0 = u * u * u
  const b1 = 3 * u * u * t
  const b2 = 3 * u * t * t
  const b3 = t * t * t
  out[o] = b0 * p[i] + b1 * p[i + 2] + b2 * p[i + 4] + b3 * p[i + 6]
  out[o + 1] = b0 * p[i + 1] + b1 * p[i + 3] + b2 * p[i + 5] + b3 * p[i + 7]
}

function tangent(p: Float64Array, k: number, atEnd: boolean): readonly [number, number] | null {
  const i = 6 * k
  const b = atEnd ? i + 6 : i
  const s = atEnd ? -1 : 1
  for (const j of atEnd ? [4, 2, 0] : [2, 4, 6]) {
    const dx = s * (p[i + j] - p[b])
    const dy = s * (p[i + j + 1] - p[b + 1])
    if (dx * dx + dy * dy > 1e-18) return [dx, dy]
  }
  return null
}

function detectCorners(path: CubicPath): number[] {
  const p = path.pts
  const m = (p.length / 2 - 1) / 3
  const active: number[] = []
  for (let k = 0; k < m; k++) if (segLen(p, k) > 1e-9) active.push(k)
  if (active.length === 0) return []
  const corners = new Set<number>()
  const test = (a: number, b: number) => {
    const u = tangent(p, a, true)
    const v = tangent(p, b, false)
    if (!u || !v) return
    const ang = Math.abs(Math.atan2(u[0] * v[1] - u[1] * v[0], u[0] * v[0] + u[1] * v[1]))
    if (ang > CORNER_THRESHOLD) corners.add(b)
  }
  for (let j = 0; j + 1 < active.length; j++) test(active[j], active[j + 1])
  if (path.closed && active.length > 1) test(active[active.length - 1], active[0])
  return [...corners].sort((a, b) => a - b)
}

function invert(p: Float64Array, k: number, s: number, ls: number): number {
  if (s <= 0) return 0
  if (s >= ls) return 1
  let lo = 0
  let hi = 1
  let t = s / ls
  for (let it = 0; it < 12; it++) {
    const f = segLen(p, k, t) - s
    if (Math.abs(f) < 1e-10 * ls + 1e-14) break
    if (f > 0) hi = t
    else lo = t
    const sp = speed(p, k, t)
    let nt = sp > 1e-12 ? t - f / sp : (lo + hi) / 2
    if (!(nt > lo && nt < hi)) nt = (lo + hi) / 2
    t = nt
  }
  return t
}

/** Sample a cubic subpath at N points, evenly by arc length, corners exact. */
export function resamplePath(path: CubicPath, N = MORPH_SAMPLES): Float64Array {
  const p = path.pts
  const m = (p.length / 2 - 1) / 3
  const out = new Float64Array(2 * N)
  const fill = () => {
    for (let i = 0; i < N; i++) {
      out[2 * i] = p[0]
      out[2 * i + 1] = p[1]
    }
    return out
  }
  if (m < 1) return fill()
  const lens = new Array<number>(m)
  let L = 0
  for (let k = 0; k < m; k++) {
    lens[k] = segLen(p, k)
    L += lens[k]
  }
  if (L < 1e-12) return fill()

  const cs = detectCorners(path)
  const anchors = path.closed
    ? cs.length > 0
      ? cs
      : [0]
    : [...new Set([0, ...cs, m])].sort((a, b) => a - b)
  const runs: Array<[number, number]> = []
  if (path.closed) {
    for (let j = 0; j < anchors.length; j++) {
      const a = anchors[j]
      const b = j + 1 < anchors.length ? anchors[j + 1] : anchors[0] + m
      runs.push([a, b])
    }
  } else {
    for (let j = 0; j + 1 < anchors.length; j++) runs.push([anchors[j], anchors[j + 1]])
  }
  const rl = runs.map(([a, b]) => {
    let s = 0
    for (let k = a; k < b; k++) s += lens[k % m]
    return s
  })
  const intervals = path.closed ? N : N - 1
  if (runs.length > intervals) {
    // Degenerate (pathological) case: fall back to uniform segment sampling.
    for (let i = 0; i < N; i++) {
      const t = path.closed ? i / N : i / (N - 1)
      const seg = Math.min(m - 1, Math.floor(t * m))
      pointAt(p, seg, t * m - seg, out, 2 * i)
    }
    return out
  }

  const total = rl.reduce((a, b) => a + b, 0) || 1
  const ideal = rl.map((l) => (intervals * l) / total)
  const counts = ideal.map((q) => Math.max(1, Math.floor(q)))
  let R = intervals - counts.reduce((a, b) => a + b, 0)
  if (R > 0) {
    const order = ideal
      .map((q, idx) => [Math.round((q - Math.floor(q)) * 1e9), idx] as const)
      .sort((a, b) => b[0] - a[0] || a[1] - b[1])
    for (let j = 0; j < R; j++) counts[order[j % counts.length][1]]++
  }
  while (R < 0) {
    let bi = 0
    for (let idx = 1; idx < counts.length; idx++) if (counts[idx] > counts[bi]) bi = idx
    if (counts[bi] <= 1) break
    counts[bi]--
    R++
  }

  let w = 0
  for (let r = 0; r < runs.length; r++) {
    const [k0, k1] = runs[r]
    const cnt = counts[r]
    const Lr = rl[r]
    const vi = 6 * (k0 % m)
    out[2 * w] = p[vi]
    out[2 * w + 1] = p[vi + 1]
    w++
    let seg = k0
    let acc = 0
    for (let j = 1; j < cnt; j++) {
      const target = (Lr * j) / cnt
      while (seg < k1 - 1 && acc + lens[seg % m] < target) {
        acc += lens[seg % m]
        seg++
      }
      const k = seg % m
      const ls = lens[k]
      const t = ls > 1e-12 ? invert(p, k, target - acc, ls) : 0
      pointAt(p, k, t, out, 2 * w)
      w++
    }
  }
  if (!path.closed) {
    const vi = 6 * m
    out[2 * w] = p[vi]
    out[2 * w + 1] = p[vi + 1]
  }
  return out
}

const shapeCache = new WeakMap<MorphGlyph, Shape>()

/** Glyph → sampled subpaths. Cached by glyph identity. */
export function sampleGlyph(glyph: MorphGlyph, N = MORPH_SAMPLES): Shape {
  if (N === MORPH_SAMPLES) {
    const hit = shapeCache.get(glyph)
    if (hit) return hit
  }
  const items: SampledPath[] = []
  for (const d of glyph.paths) {
    for (const raw of parsePath(d)) {
      const cubic = toCubic(raw)
      if (!cubic) continue
      items.push({ pts: resamplePath(cubic, N), closed: cubic.closed, opacity: 1 })
    }
  }
  const shape: Shape = { items }
  if (N === MORPH_SAMPLES) shapeCache.set(glyph, shape)
  return shape
}

/* ---------------------------------------------------------------------- */
/* Correspondence and alignment (closed-form Procrustes)                   */
/* ---------------------------------------------------------------------- */

const LEN_WEIGHT = 0.35
const LAMBDA = 0.05

function centroid(p: Float64Array): [number, number] {
  const n = p.length / 2
  let cx = 0
  let cy = 0
  for (let i = 0; i < n; i++) {
    cx += p[2 * i]
    cy += p[2 * i + 1]
  }
  return [cx / n, cy / n]
}

function polyLen(p: Float64Array): number {
  const n = p.length / 2
  let L = 0
  for (let i = 1; i < n; i++) L += Math.hypot(p[2 * i] - p[2 * i - 2], p[2 * i + 1] - p[2 * i - 1])
  return L
}

function reversePts(p: Float64Array): Float64Array {
  const n = p.length / 2
  const out = new Float64Array(2 * n)
  for (let i = 0; i < n; i++) {
    out[2 * i] = p[2 * (n - 1 - i)]
    out[2 * i + 1] = p[2 * (n - 1 - i) + 1]
  }
  return out
}

function rotatePts(p: Float64Array, off: number): Float64Array {
  const n = p.length / 2
  const out = new Float64Array(2 * n)
  for (let i = 0; i < n; i++) {
    const j = (i + off) % n
    out[2 * i] = p[2 * j]
    out[2 * i + 1] = p[2 * j + 1]
  }
  return out
}

interface Similarity {
  theta: number
  sigma: number
  res: number
}

function procrustes(
  a: Float64Array,
  b: Float64Array,
  ca: readonly [number, number],
  cb: readonly [number, number],
): Similarity {
  const n = a.length / 2
  let sxx = 0
  let sxy = 0
  let syx = 0
  let syy = 0
  let na = 0
  let nb = 0
  for (let i = 0; i < n; i++) {
    const ax = a[2 * i] - ca[0]
    const ay = a[2 * i + 1] - ca[1]
    const bx = b[2 * i] - cb[0]
    const by = b[2 * i + 1] - cb[1]
    sxx += ax * bx
    syy += ay * by
    sxy += ax * by
    syx += ay * bx
    na += ax * ax + ay * ay
    nb += bx * bx + by * by
  }
  const theta = Math.atan2(sxy - syx, sxx + syy)
  const num = Math.cos(theta) * (sxx + syy) + Math.sin(theta) * (sxy - syx)
  let sigma = na > 1e-12 ? num / na : 1
  // Guard against near-degenerate sources (dots) blowing up the scale.
  if (!(sigma > 0.02)) sigma = 0.02
  if (sigma > 50) sigma = 50
  const res2 = Math.max(0, sigma * sigma * na - 2 * sigma * num + nb)
  const res = nb > 1e-12 ? Math.sqrt(res2 / nb) : 0
  return { theta, sigma, res }
}

interface Alignment extends Similarity {
  ca: readonly [number, number]
  cb: readonly [number, number]
  a: Float64Array
  b: Float64Array
}

/** Best index correspondence between two sampled subpaths. Tries both
 *  traversal directions and, when a loop is involved, every circular cut. */
function alignPair(aPts: Float64Array, bPts: Float64Array, aClosed: boolean, bClosed: boolean): Alignment {
  const ca = centroid(aPts)
  const cb = centroid(bPts)
  const varyA = aClosed && !bClosed
  const base = varyA ? aPts : bPts
  const offs = aClosed || bClosed ? base.length / 2 : 1
  let bestScore = Number.POSITIVE_INFINITY
  let best = base
  let sim: Similarity = { theta: 0, sigma: 1, res: 0 }
  for (let dir = 0; dir < 2; dir++) {
    const walk = dir ? reversePts(base) : base
    for (let off = 0; off < offs; off++) {
      const cand = off ? rotatePts(walk, off) : walk
      const s = varyA ? procrustes(cand, bPts, ca, cb) : procrustes(aPts, cand, ca, cb)
      const score = s.res + (LAMBDA * Math.abs(s.theta)) / Math.PI
      if (score < bestScore) {
        bestScore = score
        best = cand
        sim = s
      }
    }
  }
  return varyA ? { ca, cb, a: best, b: bPts, ...sim } : { ca, cb, a: aPts, b: best, ...sim }
}

/* ---------------------------------------------------------------------- */
/* Plan                                                                    */
/* ---------------------------------------------------------------------- */

export interface PlanItem {
  a: Float64Array
  aC: Float64Array
  bT: Float64Array
  ca: readonly [number, number]
  cb: readonly [number, number]
  theta: number
  lnSigma: number
  closed: boolean
  /** 0 = collapsing out (target is a point), 1 = growing in, -1 = normal. */
  fade: -1 | 0 | 1
  oa: number
  ob: number
}

export interface MorphPlan {
  items: PlanItem[]
  samples: number
}

function centered(p: Float64Array, c: readonly [number, number]): Float64Array {
  const n = p.length / 2
  const out = new Float64Array(2 * n)
  for (let i = 0; i < n; i++) {
    out[2 * i] = p[2 * i] - c[0]
    out[2 * i + 1] = p[2 * i + 1] - c[1]
  }
  return out
}

function pointCloud(p: Float64Array): Float64Array {
  const n = p.length / 2
  const c = centroid(p)
  const out = new Float64Array(2 * n)
  for (let i = 0; i < n; i++) {
    out[2 * i] = c[0]
    out[2 * i + 1] = c[1]
  }
  return out
}

/** Build the pair plan: pair subpaths greedily by centroid distance and
 *  length, then collapse leftovers to a point (fading out) or grow the
 *  target's leftovers from a point (fading in). */
export function buildPlan(from: Shape, to: Shape): MorphPlan {
  const A = from.items
  const B = to.items
  const ca = A.map((it) => centroid(it.pts))
  const cb = B.map((it) => centroid(it.pts))
  const la = A.map((it) => polyLen(it.pts))
  const lb = B.map((it) => polyLen(it.pts))

  const pairs: Array<[number, number, number]> = []
  for (let i = 0; i < A.length; i++) {
    for (let j = 0; j < B.length; j++) {
      pairs.push([Math.hypot(ca[i][0] - cb[j][0], ca[i][1] - cb[j][1]) + LEN_WEIGHT * Math.abs(la[i] - lb[j]), i, j])
    }
  }
  pairs.sort((x, y) => x[0] - y[0] || x[1] - y[1] || x[2] - y[2])
  const usedA = new Array<boolean>(A.length).fill(false)
  const usedB = new Array<boolean>(B.length).fill(false)
  const matched: Array<[number, number]> = []
  for (const [, i, j] of pairs) {
    if (usedA[i] || usedB[j]) continue
    usedA[i] = true
    usedB[j] = true
    matched.push([i, j])
    if (matched.length === Math.min(A.length, B.length)) break
  }

  const items: PlanItem[] = []
  for (const [i, j] of matched) {
    const al = alignPair(A[i].pts, B[j].pts, A[i].closed, B[j].closed)
    const aC = centered(al.a, al.ca)
    // bT = R(−θ)·(b − c_B)/σ, the target brought into A's frame.
    const n = al.b.length / 2
    const bT = new Float64Array(2 * n)
    const cos = Math.cos(-al.theta) / al.sigma
    const sin = Math.sin(-al.theta) / al.sigma
    for (let k = 0; k < n; k++) {
      const bx = al.b[2 * k] - al.cb[0]
      const by = al.b[2 * k + 1] - al.cb[1]
      bT[2 * k] = bx * cos - by * sin
      bT[2 * k + 1] = bx * sin + by * cos
    }
    items.push({
      a: al.a,
      aC,
      bT,
      ca: al.ca,
      cb: al.cb,
      theta: al.theta,
      lnSigma: Math.log(al.sigma),
      closed: A[i].closed && B[j].closed,
      fade: -1,
      oa: A[i].opacity,
      ob: B[j].opacity,
    })
  }

  // A leftovers: collapse to their own centroid and fade out.
  for (let i = 0; i < A.length; i++) {
    if (usedA[i]) continue
    const c = ca[i]
    const n = A[i].pts.length / 2
    const bT = new Float64Array(2 * n)
    const aC = centered(A[i].pts, c)
    items.push({
      a: A[i].pts,
      aC,
      bT,
      ca: c,
      cb: c,
      theta: 0,
      lnSigma: 0,
      closed: false,
      fade: 0,
      oa: A[i].opacity,
      ob: 0,
    })
  }

  // B leftovers: grow from a point at their centroid and fade in.
  for (let j = 0; j < B.length; j++) {
    if (usedB[j]) continue
    const c = cb[j]
    const n = B[j].pts.length / 2
    const a = pointCloud(B[j].pts)
    const bT = centered(B[j].pts, c)
    items.push({
      a,
      aC: new Float64Array(2 * n),
      bT,
      ca: c,
      cb: c,
      theta: 0,
      lnSigma: 0,
      closed: false,
      fade: 1,
      oa: 0,
      ob: B[j].opacity,
    })
  }

  return { items, samples: A.length > 0 ? A[0].pts.length / 2 : MORPH_SAMPLES }
}

const planCache = new WeakMap<MorphGlyph, WeakMap<MorphGlyph, MorphPlan>>()

/** Plan for a glyph pair, cached by identity (glyphs are module constants). */
export function planForGlyphs(from: MorphGlyph, to: MorphGlyph): MorphPlan {
  let inner = planCache.get(from)
  if (!inner) {
    inner = new WeakMap()
    planCache.set(from, inner)
  }
  const hit = inner.get(to)
  if (hit) return hit
  const plan = buildPlan(sampleGlyph(from), sampleGlyph(to))
  inner.set(to, plan)
  return plan
}

export function allocOutputs(plan: MorphPlan): Float64Array[] {
  return plan.items.map(() => new Float64Array(2 * plan.samples))
}

/** P(t) = c(t) + σ^t·R(tθ)·[(1−t)·aC + t·bT]. */
export function interpolate(plan: MorphPlan, t: number, out: Float64Array[]): void {
  for (let k = 0; k < plan.items.length; k++) {
    const it = plan.items[k]
    const o = out[k]
    const n = plan.samples
    const s = Math.exp(it.lnSigma * t)
    const ang = it.theta * t
    const cos = Math.cos(ang) * s
    const sin = Math.sin(ang) * s
    const cx = it.ca[0] + (it.cb[0] - it.ca[0]) * t
    const cy = it.ca[1] + (it.cb[1] - it.ca[1]) * t
    for (let i = 0; i < n; i++) {
      const px = it.aC[2 * i] + (it.bT[2 * i] - it.aC[2 * i]) * t
      const py = it.aC[2 * i + 1] + (it.bT[2 * i + 1] - it.aC[2 * i + 1]) * t
      o[2 * i] = cx + px * cos - py * sin
      o[2 * i + 1] = cy + px * sin + py * cos
    }
  }
}

/** Per-item opacity at t (0..1). */
export function opacities(plan: MorphPlan, t: number, out: number[]): void {
  const x = Math.min(1, Math.max(0, t))
  for (let k = 0; k < plan.items.length; k++) {
    const it = plan.items[k]
    if (it.fade === 0) out[k] = it.oa * (1 - x)
    else if (it.fade === 1) out[k] = it.oa + (it.ob - it.oa) * x
    else out[k] = it.oa + (it.ob - it.oa) * x
  }
}

/** Mid-flight polyline `d`, rounded to 2 decimals. */
export function serializePts(pts: Float64Array, closed: boolean): string {
  const fmt = (v: number) => String(Math.round(v * 100) / 100)
  const n = pts.length / 2
  let d = `M${fmt(pts[0])} ${fmt(pts[1])}`
  for (let i = 1; i < n; i++) d += `L${fmt(pts[2 * i])} ${fmt(pts[2 * i + 1])}`
  if (closed) d += 'Z'
  return d
}

/* ---------------------------------------------------------------------- */
/* Spring                                                                  */
/* ---------------------------------------------------------------------- */

export interface SpringConfig {
  stiffness: number
  damping: number
}

export const SPRING_PRESETS = {
  /** ζ = 1.00 — critically damped, no overshoot. */
  smooth: { stiffness: 170, damping: 26 },
  /** ζ = 0.73 — fast, subtle overshoot (default). */
  snappy: { stiffness: 420, damping: 30 },
  /** ζ = 0.40 — playful. */
  bouncy: { stiffness: 300, damping: 14 },
} as const

export type SpringPreset = keyof typeof SPRING_PRESETS
export type SpringInput = SpringPreset | SpringConfig

export function springConfig(input: SpringInput | undefined): SpringConfig {
  if (!input) return SPRING_PRESETS.snappy
  if (typeof input === 'string') return SPRING_PRESETS[input]
  return input
}

/** Damped harmonic oscillator over progress x: 0 → 1. Semi-implicit Euler
 *  at 1/240 s substeps; interruptible with velocity carry-over. */
export class Spring {
  x = 1
  v = 0
  k: number = SPRING_PRESETS.snappy.stiffness
  c: number = SPRING_PRESETS.snappy.damping

  config(config: SpringConfig): void {
    this.k = config.stiffness
    this.c = config.damping
  }

  start(): void {
    this.x = 0
    if (this.v > 14) this.v = 14
    if (this.v < -14) this.v = -14
  }

  /** Advance dt seconds; true once settled. */
  step(dt: number): boolean {
    const h = 1 / 240
    const steps = Math.max(1, Math.min(16, Math.ceil(dt / h)))
    const s = dt / steps
    for (let i = 0; i < steps; i++) {
      const a = this.k * (1 - this.x) - this.c * this.v
      this.v += a * s
      this.x += this.v * s
    }
    return Math.abs(1 - this.x) < 0.001 && Math.abs(this.v) < 0.02
  }
}
