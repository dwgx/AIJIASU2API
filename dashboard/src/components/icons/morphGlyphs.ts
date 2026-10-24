/* APC morph glyphs — 24×24 stroke paths, one subpath per entry so the plan
 * can pair, rotate and fade them independently. Values mirror ApcIcons.tsx
 * exactly at rest; `MorphIcon` renders these strings verbatim when settled.
 */

import type { MorphGlyph } from '../../lib/iconMorph'
import type { Status } from '../../lib/types'

const CIRCLE = 'M20.5 12A8.5 8.5 0 1 1 3.5 12A8.5 8.5 0 1 1 20.5 12Z'
const PANEL = 'M6 4h12a2.5 2.5 0 0 1 2.5 2.5v11a2.5 2.5 0 0 1-2.5 2.5H6a2.5 2.5 0 0 1-2.5-2.5v-11A2.5 2.5 0 0 1 6 4Z'
const PANEL_DIVIDER = 'M9.5 4v16'

/** Sidebar expand/collapse: the chevron flips by a 180° rotation. */
export const PANEL_COLLAPSE_GLYPH: MorphGlyph = {
  paths: [PANEL, PANEL_DIVIDER, 'M16 10l-2 2 2 2'],
}

export const PANEL_EXPAND_GLYPH: MorphGlyph = {
  paths: [PANEL, PANEL_DIVIDER, 'M14 10l2 2-2 2'],
}

/** Theme: sun rays retract into a fading point while the disc becomes a
 *  crescent (and the reverse when going back to light). */
export const SUN_GLYPH: MorphGlyph = {
  paths: [
    'M16 12A4 4 0 0 1 8 12A4 4 0 0 1 16 12Z',
    'M12 2.5v2',
    'M12 19.5v2',
    'M4.6 4.6 6 6',
    'M18 18l1.4 1.4',
    'M2.5 12h2',
    'M19.5 12h2',
    'M4.6 19.4 6 18',
    'M18 6l1.4-1.4',
  ],
}

export const MOON_GLYPH: MorphGlyph = {
  paths: ['M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z'],
}

/** Language: 中's frame unfolds into the Latin A (and back). */
export const HAN_GLYPH: MorphGlyph = {
  paths: [
    'M7 6h10a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2Z',
    'M12 3v18',
  ],
}

export const LATIN_GLYPH: MorphGlyph = {
  paths: ['M5 19 12 5l7 14', 'M8.4 13h7.2'],
}

/** Status pills: disc/triangle frames and their mark morph in place; a mark
 *  that the next status does not have collapses and fades. */
export const STATUS_GLYPHS: Record<Status, MorphGlyph> = {
  ok: { paths: [CIRCLE, 'M8 12.2l2.6 2.6L16 9.4'] },
  info: { paths: [CIRCLE, 'M12 11v5', 'M12 8h.01'] },
  warn: { paths: ['M12 4.2 21 19.5H3Z', 'M12 10v4', 'M12 17h.01'] },
  error: { paths: [CIRCLE, 'M9.2 9.2l5.6 5.6', 'M14.8 9.2l-5.6 5.6'] },
  unavailable: { paths: [CIRCLE, 'M6.5 6.5l11 11'] },
}

/** Node serving state: the power glyph gains/loses a strike-through slash. */
export const NODE_LIVE_GLYPH: MorphGlyph = {
  paths: ['M12 3v8', 'M18.36 6.64a9 9 0 1 1-12.73 0'],
}

export const NODE_DRAINED_GLYPH: MorphGlyph = {
  paths: ['M12 3v8', 'M18.36 6.64a9 9 0 1 1-12.73 0', 'M4 4l16 16'],
}
