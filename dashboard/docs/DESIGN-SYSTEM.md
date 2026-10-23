# DESIGN-SYSTEM — AJiaSu Proxy Dashboard ("Obsidian Mint")

Status: design contract v1 · 2026-10-23 · slice AJS-20. Normative for token names, values,
component anatomy and states. `PAGES.md` is normative for page composition.

**No code in this file by design.** Every value is a table row (token → value → role). The
implementer writes the `:root` block; the names and values below are the contract. Numbers copied
from a reference artifact carry a citation; decisions that exist in no reference are `[INFERENCE]`.

## 0. Definition of done for the implementation

1. Every hex, px and ms in `src/` traces to a row here. No ad-hoc literals.
2. All 23 components in §13 exist with the listed states; none is a stub.
3. The viewport rules in §12 hold at exactly 1440, 1024 and 768 CSS px.
4. Every interactive element is keyboard reachable, shows the 2 px mint focus ring, and exposes
   role/label/state to assistive tech (§14).
5. The §15 "do not copy" list is respected — those are measured defects in the source captures.

## 1. Source ledger

| Source | Read | Taken |
|---|---|---|
| `launcher/research/ui-reverse-20261023/INDEX.md` | full | capture method, report summaries, X do-not-copy list |
| `…/reports/TokenSpec.md` | §1.1–1.7, 2.1–2.5, 3.1–3.3, 4.1–4.3 | surface/text/border/semantic ladders, type scale, 4 px spacing base, radii, shadows |
| `…/reports/ComponentSpec.md` | §0–§13, §15 | nav row, button sizes, field rhythm, tabs, segmented control, skeleton, `::before` fill model |
| `…/reports/MotionSpec.md` | §0–§3.3 | the four curves, duration scale, enter-curve front-loading, reduced motion |
| `KiroStudio/admin-ui/src/index.css:5-70`, `tailwind.config.js` | full | Inter + JetBrains Mono, 240 px sidebar precedent, `--surface-1..3` naming |
| `launcher/src/core/tokens.h` | 1–226 | 22 px page title, 32 px control, 36 px row, 150/200/300 ms motion, radii roles |
| `launcher/backend-server/crates/api/static/launcher-admin.css` | 29–160 | layered shadow recipe (inset rim + drop), radius ladder, `--card-gap: 16px` |
| `Designers_Brain/skills/designers-brain/SKILL.md`, `knowledge/cross/conflicts.md` | N6–N8 | one accent colour, colour-count discipline, motion budget |
| `AwesomeUI/dissections/{interaction/cmdk, components/radix-primitives, interaction/vaul}` | READMEs | palette keyboard model, presence state machine, drawer physics |
| `LovePlusSpeedToProxy/docs/PANEL.md`, `research/76-sdk-integration/INTERFACE.md` | full | endpoint names, account/node/flow state vocabularies, error codes |

## 2. Design principles

1. **One accent, used as signal.** Mint marks what is live, selected, or actionable. It never
   decorates. Everything else is a green-tinted neutral. (Designers_Brain `conflicts.md` N6: the
   colour ceiling applies to the flat layer; one accent is the whole budget.)
2. **Two-tone shell, three elevations.** Sidebar darker than canvas, content surface lighter than
   both, overlays lighter still. Copy the *model* from OpenAI (`TokenSpec.md` §1.2:
   `#131313` / `#212121` / `#303030`), not its hues.
3. **Numbers are monospace and tabular.** IPs, ports, bytes, latency and IDs use JetBrains Mono
   with `font-variant-numeric: tabular-nums` so columns never jitter.
4. **State is never colour-only.** Every status carries dot + label (+ shape where it matters).
   WCAG 1.4.1; `ComponentSpec.md` §11 flags the source tabs for exactly this.
5. **Fill lives on a pseudo-element.** Hover/press/selected paint on `::before` with
   `inset: 0; border-radius: inherit`; the hit box never moves. (`ComponentSpec.md` §13.1.)
6. **Motion is short and front-loaded.** 150 ms state, 250 ms overlay, 300 ms panel; the enter
   curve reaches 97.8 % of travel at half duration (`MotionSpec.md` §0.3).

## 3. Color tokens

### 3.1 Surfaces

| Token | Value | Role | vs `--bg-base` |
|---|---|---|---|
| `--bg-base` | `#0B0F0E` | app canvas behind content | — |
| `--bg-sidebar` | `#0E1412` | sidebar column, palette footer | 1.04:1 |
| `--bg-surface` | `#131A17` | cards, tables, panels | 1.15:1 |
| `--bg-elevated` | `#1A2420` | menus, modals, drawers, toasts | 1.31:1 |
| `--bg-active` | `#22302A` | selected nav/table row, segmented thumb | 1.45:1 |
| `--bg-sunken` | `#070A09` | input wells, IP blocks, table header | darker |
| `--bg-hover` | `rgba(255,255,255,.05)` | hover wash → `#1F2523` on surface | wash |
| `--bg-pressed` | `rgba(255,255,255,.08)` | press wash, chips, keycaps → `#262C2A` | wash |

The wash model comes from X (`INDEX.md` item 2: "a 5 % wash and an 8 % chip") and matches the
launcher (`tokens.h:32-34`, `kHoverWash` 5 % / `kChipWash` 8 %). A wash works on any surface, so
one token serves canvas, card and menu.

### 3.2 Text

| Token | Value | Role | on surface | on elevated |
|---|---|---|---|---|
| `--text-primary` | `#E6F1EC` | body, cells, values | 15.29:1 | 13.79:1 |
| `--text-secondary` | `#A3B8AE` | labels, helpers, captions | 8.44:1 | 7.61:1 |
| `--text-tertiary` | `#829A90` | timestamps, units, meta | 5.87:1 | 5.29:1 |
| `--text-muted` | `#6F857B` | **non-text only**: icons, axis lines, rules | 4.48:1 | 4.04:1 |
| `--text-disabled` | `#5E7067` | disabled labels (house bar 3:1) | 3.36:1 | 3.03:1 |
| `--text-inverse` | `#0B0F0E` | ink on mint/success/warning fills | — | — |
| `--text-on-mint` | `#04211C` | ink on `--mint` at ≤13 px | — | — |

`--text-tertiary` is **not** the brief's `#6F857B` — see §3.8. `#6F857B` survives as
`--text-muted` for non-text roles, where AA does not apply.

### 3.3 Semantic

| Token | Value | Role | on surface | ink that passes |
|---|---|---|---|---|
| `--mint` | `#5EEAD4` | primary accent, focus ring, live signal | 11.95:1 | `--text-inverse` 13.04:1 |
| `--mint-strong` | `#2DD4BF` | hover/pressed of mint fills, active chart line | 9.50:1 | 10.36:1 |
| `--success` | `#34D399` | node LIVE, probe ok, request 2xx | 9.20:1 | 10.03:1 |
| `--warning` | `#FBBF24` | degraded, quota ≥80 %, expiring membership | 10.59:1 | 11.55:1 |
| `--danger` | `#FB7185` | DOWN, AUTH_FAILED, 5xx, destructive action | 6.57:1 | 7.17:1 |
| `--info` | `#60A5FA` | neutral informational, DRAINING, 3xx | 6.95:1 | 7.59:1 |

Soft variants are the same hue at 14 % over `--bg-surface` (computed, not estimated):

| Token | Composite | `--text-primary` on it | hue-as-text on it |
|---|---|---|---|
| `--mint-soft` | `#1E3731` | 11.03:1 | 8.62:1 |
| `--success-soft` | `#183429` | 11.62:1 | 6.99:1 |
| `--warning-soft` | `#333119` | 11.39:1 | 7.89:1 |
| `--danger-soft` | `#332626` | 12.56:1 | 5.40:1 |
| `--info-soft` | `#1E2D37` | 12.22:1 | 5.56:1 |

Rule: a soft fill carries either `--text-primary` or its own hue as the 11 px label — both pass.
It never carries a *different* hue. Body copy on a soft fill is always `--text-primary`. (OpenAI's
white-on-`#ba8e00` at 3.02:1 is the counter-example — `TokenSpec.md` §1.5.)

### 3.4 Interaction washes and states

| State | Fill | Border | Text |
|---|---|---|---|
| rest | transparent | `--border` (8 %) | `--text-secondary` |
| hover | `--bg-hover` (5 %) | `--border-strong` (16 %) | `--text-primary` |
| pressed | `--bg-pressed` (8 %) + `scale(.98)` | `--border-strong` | `--text-primary` |
| selected | `--bg-active` `#22302A` | `--border-mint` (mint 24 %) | `--text-primary` |
| focus-visible | unchanged | ring only, see §14 | unchanged |
| disabled | transparent | `--border-subtle` (5 %) | `--text-disabled` |

### 3.5 Borders

Every border is white alpha, never a solid grey — one alpha works over any surface
(`TokenSpec.md` §1.4). 1 px only; a 2 px edge is always a ring, not a border.

| Token | Alpha | on `--bg-base` | on `--bg-sidebar` | on `--bg-surface` | on `--bg-elevated` | on `--bg-active` |
|---|---|---|---|---|---|---|
| `--border-subtle` | 5 % | `#171B1A` | `#1A201E` | `#1F2523` | `#252F2B` | `#2D3A35` |
| `--border` | 8 % | `#1F2221` | `#212725` | `#262C2A` | `#2C3632` | `#34413B` |
| `--border-strong` | 12 % | `#282C2B` | `#2B302E` | `#2F3533` | `#353E3B` | `#3D4944` |
| `--border-stronger` | 16 % | `#323535` | `#353A38` | `#393F3C` | `#3F4744` | `#45514C` |

Role assignment: `--border` on cards, tables, topbar rule, sidebar edge; `--border-strong` on
inputs, selects and secondary buttons (matches OpenAI's 25 % input border at a lower intensity
because our surfaces are darker — `[INFERENCE]`); `--border-subtle` for internal dividers;
`--border-stronger` for hover of an outlined control. Mint borders: `--border-mint` =
`rgba(94,234,212,.24)` → `#254C44` on surface, used only for the selected state.

### 3.6 Data-visualisation series

Five series, chosen for luminance separation on `--bg-surface` and distinguishable under
deuteranopia (mint/blue/amber/rose/violet, no green-red pair):

| Token | Value | Contrast on surface | Used for |
|---|---|---|---|
| `--series-1` | `#5EEAD4` | 11.95:1 | ingress bytes (primary metric) |
| `--series-2` | `#60A5FA` | 6.95:1 | egress bytes |
| `--series-3` | `#FBBF24` | 10.59:1 | requests |
| `--series-4` | `#FB7185` | 6.57:1 | errors / rejected flows |
| `--series-5` | `#A78BFA` | 6.50:1 | active leases / fifth dimension |

Grid lines `--border-subtle`; axis labels `--text-tertiary` 11 px; no area gradients beyond 10 %
of the series colour; line width 2 px; no dots except the last point (3 px). Do **not** reuse
`--success`/`--warning`/`--danger` as series colours — a series must never read as a status.

### 3.7 Contrast audit (computed, WCAG 2.1 relative luminance)

Every text token against all five surfaces:

| Token | base | sidebar | surface | elevated | active |
|---|---|---|---|---|---|
| `--text-primary` `#E6F1EC` | 16.68 | 16.11 | 15.29 | 13.79 | 11.91 |
| `--text-secondary` `#A3B8AE` | 9.21 | 8.89 | 8.44 | 7.61 | 6.58 |
| `--text-tertiary` `#829A90` | 6.40 | 6.19 | 5.87 | 5.29 | 4.58 |
| `--text-muted` `#6F857B` | 4.88 | 4.72 | 4.48 | 4.04 | 3.49 |
| `--text-disabled` `#5E7067` | 3.66 | 3.54 | 3.36 | 3.03 | 2.62 |

Bars used: body text ≥4.5:1, large text ≥3:1, non-text UI ≥3:1, disabled ≥3:1 (house rule; WCAG
exempts inactive controls), focus indicator ≥3:1 against both the control and the background.

- `--text-tertiary` clears 4.5:1 on every surface including `--bg-active` (4.58). It is the
  smallest text allowed on a selected row.
- `--text-muted` clears 4.5:1 only on base/sidebar and 4.48 on surface — it is therefore
  **not a text token**; icons and rules are the only consumers (3:1 bar, met everywhere except
  `--bg-active`, where icons use `--text-secondary`).
- `--mint` as a focus ring measures 11.95:1 against `--bg-surface` and 13.04:1 against
  `--bg-base`; as a fill it carries `--text-inverse` at 13.04:1. Both clear 1.4.11 and 1.4.3.
- Every semantic hue as *text* on `--bg-surface` is ≥6.5:1, so status labels may be coloured.
- `--text-disabled` clears 3:1 on base/sidebar/surface/elevated but only 2.62 on `--bg-active`;
  a disabled control therefore never sits inside a selected row — disable the row action instead.

### 3.8 Measured corrections to the brief

Three values in the design brief do not survive measurement. Each is resolved here; the brief's
hex is preserved as a token so nothing is silently discarded.

| Brief value | Measured | Resolution |
|---|---|---|
| tertiary text `#6F857B` | 4.48:1 on `--bg-surface`, 4.04:1 on `--bg-elevated` — **below AA 4.5** for normal text | keep `#6F857B` as `--text-muted` (non-text); add `--text-tertiary: #829A90` (5.87 / 5.29 / 4.58) for text |
| `--bg-active #22302A` as a *border* colour | 1.28:1 vs surface — invisible as an edge | selection edges use `--border-mint` `#254C44`; `--bg-active` stays a fill |
| disabled text implied by 3:1 | `#55665E` = 2.90:1 | `--text-disabled` lifted to `#5E7067` (3.36 on surface, 3.03 on elevated) |

## 4. Type tokens

Families: **Inter** (UI, variable, weights 400/500/600) and **JetBrains Mono** (IPs, ports, byte
counts, latency, IDs, log lines). Both self-hosted via `@fontsource`, `font-display: swap`.
Precedent: `KiroStudio/admin-ui/src/index.css:48` already pairs exactly these two; `tokens.h:167-175`
supplies the 22 px page title; `TokenSpec.md` §2.2 supplies the −0.01 em tracking habit.

| Token | Family | Size / line | Weight | Tracking | Use |
|---|---|---|---|---|---|
| `--font-display` | Inter | 32 / 38 | 600 | −0.02em | empty-state hero number only |
| `--font-h1` | Inter | 22 / 28 | 600 | −0.02em | page title in the topbar (`tokens.h:175`, X page title 22/28) |
| `--font-h2` | Inter | 17 / 24 | 600 | −0.01em | card/section title |
| `--font-h3` | Inter | 15 / 22 | 600 | −0.01em | drawer section, modal title |
| `--font-body` | Inter | 14 / 20 | 400 | −0.005em | table cells, form values, prose |
| `--font-body-strong` | Inter | 14 / 20 | 500 | −0.005em | nav labels, button labels, emphasised cell |
| `--font-sm` | Inter | 13 / 18 | 400 | 0 | helper text, secondary cells |
| `--font-xs` | Inter | 12 / 16 | 500 | 0 | metric labels, table headers, badges ≥12 |
| `--font-micro` | Inter | 11 / 14 | 600 | +0.06em, uppercase | group labels, status text, keycaps |
| `--font-mono` | JetBrains Mono | 13 / 18 | 400 | 0 | IP, port, hash, byte count |
| `--font-mono-sm` | JetBrains Mono | 12 / 16 | 400 | 0 | table mono cells, inline IDs |
| `--font-metric` | JetBrains Mono | 28 / 34 | 600 | −0.01em | MetricTile value |

Rules:
1. Mono always sets `font-variant-numeric: tabular-nums`; Inter never does.
2. `--font-micro` is the only uppercase style; it is never used for a sentence.
3. No weight below 400, no italic anywhere, no font smaller than 11 px.
4. `--font-metric` is the only >22 px style in a normal page; a 44 px skeleton bar is not a font
   size (`ComponentSpec.md` §7 marks the OpenAI big-number size `[UNVERIFIED]` for exactly this).

## 5. Spacing tokens

4 px base, the same single scale OpenAI uses (`TokenSpec.md` §3.1: `--spacing: .25rem`, steps
2/4/6/8/10/12/16/20/24/32/40/48/56/64). No second scale, no arbitrary values.

| Token | px | Typical use |
|---|---|---|
| `--space-0` | 0 | reset |
| `--space-1` | 4 | icon↔label inside a chip, dot↔label |
| `--space-2` | 8 | control inner gap, badge padding-x, table cell gap |
| `--space-3` | 12 | input padding-x, table cell padding-x, card header gap |
| `--space-4` | 16 | **default gap** between cards, grid gutter, tile padding |
| `--space-5` | 20 | card padding (default) |
| `--space-6` | 24 | **content padding**, section gap inside a card, modal padding |
| `--space-8` | 32 | section gap on a page, drawer padding |
| `--space-10` | 40 | page section gap (large), empty-state padding-y |
| `--space-12` | 48 | page top gap before the first section |
| `--space-16` | 64 | topbar height, drawer header height |

Layout constants (not spacing steps): sidebar 248, topbar 64, control 32, row 36, content padding
24, card gap 16, card radius 12 — all from the brief and consistent with `tokens.h:186-212`
(the launcher's own 200/52/38 ladder is superseded here; the brief is authoritative for this app).

## 6. Radius tokens

| Token | px | Applies to |
|---|---|---|
| `--radius-xs` | 4 | checkbox, badge, keycap, small chip |
| `--radius-sm` | 6 | menu item, tab hover fill, segmented thumb, inner row |
| `--radius-md` | 8 | **all controls**: button, input, select, nav row, icon button, toast action |
| `--radius-lg` | 12 | **cards**, panels, popovers, toasts, table container |
| `--radius-xl` | 16 | modal, drawer, command palette |
| `--radius-full` | 9999 | status dot, avatar, pill toggle, progress bar |

Ladder rationale: the brief fixes 8 px for controls and 12 px for cards; 4/6/16 fill the gaps so no
component invents its own value. The launcher uses 4/6/8/12/14/24 (`launcher-admin.css:141-147`) —
we take the same shape but cap the modal at 16 because our modal is 480 px wide, not a full sheet.

## 7. Shadow tokens

Two-part recipe: an inset 1 px top rim (light catching the edge) plus a soft drop. Taken from
`launcher-admin.css:96-100`, which already layers `inset 0 1px 0 rgba(255,255,255,.04)` with a
drop shadow; OpenAI pairs every popover with a 1 px hairline for the same reason
(`TokenSpec.md` §4.3).

| Token | Value | Use |
|---|---|---|
| `--shadow-0` | `none` | cards at rest — they use a border, not a shadow |
| `--shadow-1` | `inset 0 1px 0 rgba(255,255,255,.04), 0 1px 2px rgba(0,0,0,.45)` | sticky topbar after scroll, segmented thumb |
| `--shadow-2` | `inset 0 1px 0 rgba(255,255,255,.05), 0 8px 24px -8px rgba(0,0,0,.65)` | popover, select menu, dropdown, toast |
| `--shadow-3` | `inset 0 1px 0 rgba(255,255,255,.05), 0 20px 48px -16px rgba(0,0,0,.80)` | modal, drawer, command palette |
| `--shadow-focus` | `0 0 0 2px #5EEAD4` (offset 2 → drawn as outline) | focus ring, see §14 |
| `--scrim` | `rgba(4,7,6,.62)` | modal/drawer backdrop |
| `--scrim-blur` | `4px` | backdrop-filter on overlay scrims only |

No coloured glow, no inner shadow on inputs. `--shadow-1` is the only shadow a non-overlay may use.
The three drop values are composed for this palette rather than copied: the launcher's numbers
(`launcher-admin.css:96-100`) were tuned against a `#111111` canvas and read too weak here.
`[INFERENCE]`

## 8. Border tokens

| Token | Width | Colour | Notes |
|---|---|---|---|
| `--border-width` | 1px | — | the only width in the system; `.5px` at ≥1.5× DPR (`TokenSpec.md` §4.2) |
| `--border` | 1px | `rgba(255,255,255,.08)` | cards, tables, topbar rule, sidebar edge |
| `--border-subtle` | 1px | `rgba(255,255,255,.05)` | row dividers, section rules |
| `--border-strong` | 1px | `rgba(255,255,255,.12)` | input, select, secondary button |
| `--border-stronger` | 1px | `rgba(255,255,255,.16)` | hover of an outlined control |
| `--border-mint` | 1px | `rgba(94,234,212,.24)` | selected row edge, active segmented thumb |
| `--border-danger` | 1px | `rgba(251,113,133,.45)` | invalid field |
| `--divider` | 1px | `rgba(255,255,255,.05)` | table row separator (same as subtle, named for intent) |

Never a 2 px border. A 2 px appearance is always the focus ring (§14).

## 9. Z-index tokens

| Token | Value | Layer |
|---|---|---|
| `--z-base` | 0 | page content |
| `--z-sticky` | 100 | topbar, table header |
| `--z-dropdown` | 200 | select menu, row action menu |
| `--z-scrim` | 300 | drawer + modal backdrop |
| `--z-drawer` | 310 | drawer panel |
| `--z-modal` | 400 | modal, confirm dialog |
| `--z-palette` | 500 | command palette (above a modal by design) |
| `--z-toast` | 600 | toasts |
| `--z-tooltip` | 700 | tooltips (always last) |

Gaps of 10 inside a band allow a nested element (e.g. a menu inside a drawer) without a new token.
No component may set a raw z-index.

## 10. Motion tokens

Three curves, copied verbatim from `MotionSpec.md` §2.1 (`--cubic-enter/exit/move`, declared on
L24 of the captured stylesheet). The fourth OpenAI curve (`--cubic-exit-snappy`,
`cubic-bezier(.65,0,.4,1)`) is **not** adopted — two exit curves is one too many for a 23-component
system, and the brief fixes three.

| Token | Value | Behaviour |
|---|---|---|
| `--ease-enter` | `cubic-bezier(.19,1,.22,1)` | 97.8 % of travel at half duration; 90 % at 30.8 % (`MotionSpec.md` §2.2) |
| `--ease-exit` | `cubic-bezier(.8,0,.4,1)` | 29.5 % at half duration — exits feel their full length |
| `--ease-move` | `cubic-bezier(.65,0,.35,1)` | symmetric; geometry, panel slides, segmented thumb |

| Token | Value | Applies to |
|---|---|---|
| `--dur-instant` | 100 ms | icon swap, copy confirmation, tooltip text change |
| `--dur-state` | **150 ms** | colour, background, border, opacity, transform on press |
| `--dur-overlay` | **250 ms** | modal, drawer, popover, toast, command palette |
| `--dur-panel` | **300 ms** | sidebar collapse, page-level panel slide |
| `--dur-stagger` | 40 ms | list enter, max 6 items (240 ms total) |

Assignment rules:

| Interaction | Duration | Easing | Property |
|---|---|---|---|
| hover / focus fill | 150 | `--ease-enter` | `opacity`, `background-color`, `border-color`, `color` |
| press | 150 | `--ease-enter` | `transform: scale(.98)` on the fill layer |
| segmented thumb | 250 | `--ease-move` | `transform: translateX`, `width` |
| popover / select menu | 250 in, 150 out | enter / exit | `opacity` + `translateY(-4px)→0` + `scale(.98)→1` |
| modal / drawer / palette | 250 in, 200 out | enter / exit | `opacity` + `scale(.98)→1` (modal), `translateX(24px)→0` (drawer) |
| toast | 250 in, 150 out | enter / exit | `opacity` + `translateY(8px)→0` |
| sidebar collapse | 300 | `--ease-move` | `width` |
| skeleton shimmer | 1200 ms loop | `linear` | `background-position` |

Nothing runs longer than 300 ms except the 1200 ms skeleton loop. No spring, no bounce, no
`ease-in-out`. The front-loaded enter curve is the whole feel of the product — copying the duration
without the curve makes every entrance read ~3× too slow (`MotionSpec.md` §0.3).

**Reduced motion.** Under `prefers-reduced-motion: reduce`: all durations become 1 ms, all
transforms are dropped (opacity only), the shimmer becomes a static 6 % fill, the StatusDot pulse
stops, and the segmented thumb jumps without travel. This is a hard requirement, not a nicety —
`MotionSpec.md` §7 and the launcher's own blocks (`KiroStudio/admin-ui/src/index.css` reduced-motion
queries) both implement it.

## 11. Breakpoints

| Token | Min width | Shell |
|---|---|---|
| `--bp-xs` | 480 | phone: single column, drawer nav |
| `--bp-md` | 768 | tablet portrait: icon rail, 2-up metrics, tables become cards |
| `--bp-lg` | 1024 | tablet landscape / small laptop: full sidebar, 3-up metrics |
| `--bp-xl` | 1280 | desktop: full sidebar, 4-up metrics, all table columns |
| `--bp-2xl` | 1536 | wide: content capped at 1280, extra space stays empty |

Breakpoints are min-width only (mobile-first). There is no print stylesheet in v1.

## 12. Layout rules

The shell is a two-column grid: `sidebar | main`. `main` is a column: `topbar | scroll area`.
The scroll area is the only scrolling element — the sidebar and topbar never scroll away.

### 12.1 At 1440 × 900 (desktop reference)

| Element | Value |
|---|---|
| Sidebar | 248 px fixed, `--bg-sidebar`, 1 px `--border` on the right, full height |
| Topbar | 64 px fixed height, `--bg-base` + `--border` bottom rule, padding `0 24px`, sticky |
| Content area | 1440 − 248 = 1192 px wide; padding `24px` all round → **usable 1144 px** |
| Content max-width | 1280 px, left-aligned (not centred) so the reading edge stays under the title |
| Grid | 12 columns, 16 px gutter, 24 px row gap |
| Metric row | 4 tiles × 268 px (4 × 268 + 3 × 16 = 1120, fits 1144) |
| Card grid | `repeat(auto-fill, minmax(340px, 1fr))` → 3 up at 1144 |
| Table | full width of the content box; 6–8 columns visible |
| Drawer | 480 px from the right, `--bg-elevated`, left edge 1 px `--border` |
| Modal | 480 px (default) / 560 px (form) / 720 px (detail) |

### 12.2 At 1024 × 768 (compact)

| Element | Value |
|---|---|
| Sidebar | 248 px **stays**; it is the primary navigation and 1024 − 248 = 776 is still workable `[INFERENCE]` |
| Topbar | 64 px, padding `0 20px` |
| Content padding | `20px` → usable 736 px |
| Metric row | 3 tiles (3 × 234 + 2 × 16 = 734) |
| Card grid | `minmax(300px, 1fr)` → 2 up |
| Table | 5 columns; `Region`, `Last probe`, `Egress IP` collapse into a second line inside the row; row height grows to 56 px |
| Drawer | 420 px (max `92vw`) |
| Modal | 480 px, max `calc(100vw - 48px)` |

### 12.3 At 768 × 1024 (narrow)

| Element | Value |
|---|---|
| Sidebar | becomes a **64 px icon rail**; labels hidden, icon 20 px centred, tooltip on hover/focus |
| Topbar | 56 px, padding `0 16px`; page title drops to `--font-h2` 17/24 |
| Content padding | `16px` → usable 688 px |
| Metric row | 2 up (`2 × 336 + 16`) |
| Card grid | 1 up |
| Table | becomes a **card list**: each row renders as a 12 px-radius card with a 2-column label/value grid; no horizontal scroll anywhere |
| Drawer | full width, `border-radius: 16px 16px 0 0` if bottom sheet, else 100 vw |
| Modal | 100 % − 32 px, radius 16 |
| Command palette | width `calc(100vw - 32px)`, anchored `top: 8vh` |

Below 768 the same rules hold with one column everywhere and 16 px padding.

### 12.4 Composition rules

1. A page's first block is always a page header (title + description + primary action) — never a
   card. Title `--font-h1`, description `--font-body` `--text-secondary`, 4 px gap, 24 px to the
   first section.
2. Sections are separated by 32 px; cards inside a section by 16 px.
3. Cards never nest more than one level (card → inner list is allowed; card → card is not).
4. Toolbars sit *inside* the card they filter, 48 px tall, separated by a 1 px `--border-subtle`.
5. Any number that changes without a user action must be in a `--font-mono` slot with fixed width
   (`min-width: 6ch`) so a poll does not reflow the row.
6. Sticky elements: topbar (`--z-sticky`), table header, and drawer footer. Nothing else.

## 13. Component inventory

23 components. Each entry lists anatomy with dimensions, then every state. "Ring" always means the
§14 focus ring. Sizes are CSS px at 1× DPR.

### 13.1 AppShell

Anatomy: `grid-template-columns: 248px 1fr`, full viewport height, no page scroll. Column 2 is
`grid-template-rows: 64px 1fr`; row 2 is `overflow-y: auto` with `scrollbar-gutter: stable`.
A skip-link ("Skip to content") is the first focusable node, visually hidden until focused, then a
32 px pill at top-left on `--bg-elevated`.

| State | Behaviour |
|---|---|
| default | sidebar visible, content scrolls independently |
| sidebar collapsed (<1024, or user toggle) | column 1 → 64 px, width transition 300 ms `--ease-move`; persisted in `localStorage` key `ajs.sidebar` |
| palette open | `<main>` gets `inert`; body scroll locked; scrim `--scrim` |
| route change | content scroll resets to 0; no page transition (X's capture shows no route transition — `INDEX.md` item 8) |

### 13.2 Sidebar

Anatomy, top to bottom: brand row 64 px (24 px mark + 14/20 600 wordmark, padding `0 16px`) →
primary nav (8 px top padding) → flexible spacer → health block (48 px, Resin reachability) →
footer 48 px (version, 11/14 `--text-tertiary`, mono).

Nav row: **36 px tall**, padding `0 10px`, radius 8, icon 20 × 20 with 8 px right margin, label
`--font-body-strong`, optional count badge right-aligned. Group label: `--font-micro`
`--text-tertiary`, 8 px top margin, 4 px bottom. Rows are 2 px apart (not 0) so the hover wash
reads as discrete pills.

| State | Fill | Text | Extra |
|---|---|---|---|
| rest | none | `--text-secondary` | — |
| hover | `--bg-hover` (5 %) | `--text-primary` | 150 ms `--ease-enter` |
| active (`aria-current="page"`) | `--bg-active` | `--text-primary` | 2 × 20 px mint pill at the row's left edge, inset 6 px (`tokens.h:197-199`) |
| focus-visible | unchanged | unchanged | ring inside the row, offset −2 px so it is not clipped |
| collapsed | none | hidden (`opacity: 0`, label kept in DOM for the accessible name) | 64 px rail, icon centred, tooltip on hover *and* focus |
| disabled | none | `--text-disabled` | `cursor: not-allowed`, `aria-disabled="true"` |

### 13.3 Topbar

Anatomy: 64 px, `display: flex; align-items: center; gap: 16px; padding: 0 24px`. Left: page title
`--font-h1` (max one line, `text-overflow: ellipsis`). Right cluster, in order: search trigger
(240 × 32, radius 8, `--bg-sunken`, 1 px `--border`, magnifier 16 px + `⌘K` keycap 20 × 20),
refresh IconButton, environment badge, help IconButton.

| State | Behaviour |
|---|---|
| default | 1 px `--border` bottom rule, `--bg-base` |
| scrolled | adds `--shadow-1` (no border change, no height change) |
| search trigger hover | border → `--border-strong`, background `--bg-hover` |
| refreshing | refresh icon spins 900 ms linear, `aria-busy="true"`; the button is disabled meanwhile |
| offline (SSE/poll failed) | a 20 px `--warning` pill "Reconnecting" replaces the badge; title attribute carries the last error |
| narrow (<768) | title truncates first; search trigger collapses to a 32 px icon button |

### 13.4 CommandPalette

Anatomy: scrim `--scrim` + 4 px blur (`--z-palette`), panel 640 px wide (max `calc(100vw-32px)`),
anchored `top: 15vh`, radius 16, `--bg-elevated`, `--shadow-3`. Inside: input row 48 px (search
icon 16, input `--font-body`, `Esc` keycap) → result list (max-height `min(60vh, 480px)`, scroll) →
footer 32 px (`↑↓` navigate, `↵` run, `⌘K` close, right side shows result count).

Rows: 40 px, padding `0 12px`, radius 6, icon 16 + label `--font-body` + right-aligned scope badge
`--font-micro`. Group label 28 px, `--font-micro`, uppercase, `--text-tertiary`.

| State | Behaviour |
|---|---|
| closed | not mounted |
| opening | 250 ms `--ease-enter`: opacity 0→1, `translateY(-4px)→0`, `scale(.98)→1` |
| closing | 150 ms `--ease-exit`, reverse; panel unmounts after `animationend` (Radix Presence pattern, `dissections/components/radix-primitives` §3.1) |
| query typed | filter is synchronous; first row auto-selected; empty query shows "Recent" (5) then "All commands" grouped |
| no results | 96 px block, icon 24 `--text-muted`, "No commands match" + the query in `--font-mono` |
| running | the row shows a 14 px spinner and stays open until the action resolves, then closes |
| IME composing | Enter must not run a command while `isComposing` (cmdk guards this at `index.tsx:585-590`) |

Keyboard model (from cmdk): ↑/↓ move one row, `Meta`+↑/↓ jump to first/last, `Alt`+↑/↓ jump
between groups, `Home`/`End` absolute, `Enter` run, `Esc` close and return focus to the trigger.
Focus is trapped; the input keeps DOM focus and rows are referenced by `aria-activedescendant`.

### 13.5 Button

Anatomy: inline-flex, gap 6, icon 16, label `--font-body-strong` (14/20 500), radius 8, padding
`0 12px` at md. Fill and ring live on pseudo-elements (`ComponentSpec.md` §13.1).

| Size | Height | Padding-x | Icon | Use |
|---|---|---|---|---|
| sm | 28 | 10 | 14 | table row actions, card footer |
| md | **32** | 12 | 16 | default everywhere |
| lg | 36 | 16 | 16 | page header primary, empty state |

| Variant | Rest | Hover | Pressed | Disabled |
|---|---|---|---|---|
| primary | `--mint` fill, `--text-inverse` ink | `--mint-strong` fill | `--mint-strong` + `scale(.98)` | `--bg-pressed` fill, `--text-disabled` |
| secondary | `--bg-elevated` fill, 1 px `--border-strong` | `--bg-hover` overlay | `scale(.98)` | 1 px `--border-subtle`, `--text-disabled` |
| ghost | transparent | `--bg-hover` | `--bg-pressed` + `scale(.98)` | transparent, `--text-disabled` |
| danger | `--danger` fill, `--text-inverse` ink | `#FC8A9A` fill | `scale(.98)` | `--bg-pressed`, `--text-disabled` |
| link | transparent, `--mint` text, underline on hover | `--mint-strong` | — | `--text-disabled`, no underline |

| State | Behaviour |
|---|---|
| focus-visible | 2 px `--mint` ring, 2 px offset (ghost/link: offset 2 still — do **not** copy OpenAI's −1 px, which collides with the border) |
| loading | label `opacity: 0`, 16 px spinner centred, `aria-busy="true"`, `aria-live="polite"` on a visually hidden "Working"; pointer events off, width frozen |
| success (transient) | icon swaps to check for 1200 ms then reverts; no colour change |
| full-width | `width: 100%`, label centred — only inside drawers and modals |

### 13.6 IconButton

32 × 32 (28 × 28 in table rows), radius 8, icon 16 centred, no label. Required `aria-label`.
Hit area must reach 32 px even when the glyph is 14 px (WCAG 2.5.8 asks 24 px minimum; 32 is the
house floor). States are the ghost Button's. A toggle variant sets `aria-pressed` and paints
`--bg-active` + `--mint` icon when on. Never place two IconButtons closer than 4 px.

### 13.7 Input

32 px, radius 8, padding `0 12px`, `--bg-sunken`, 1 px `--border-strong`, text `--font-body`
`--text-primary`, placeholder `--text-tertiary`. Optional leading icon 16 px (padding-left 36) and
trailing unit 12 px `--text-tertiary`.

| State | Border | Background | Notes |
|---|---|---|---|
| rest | `--border-strong` | `--bg-sunken` | — |
| hover | `--border-stronger` | `--bg-sunken` | 150 ms |
| focus | ring 2 px `--mint`, offset 2 | unchanged | **deviates from the source on purpose** — OpenAI's 1 px border brightening fails 2.4.7 (`ComponentSpec.md` §10) |
| filled | `--border-strong` | `--bg-sunken` | — |
| invalid | `--border-danger` | `--bg-sunken` | `aria-invalid="true"` + message below in `--danger` 12/16, `aria-describedby` |
| disabled | `--border-subtle` | `--bg-sunken` at 60 % | text `--text-disabled` |
| read-only | `--border-subtle` | transparent | mono variant for IDs |

Field rhythm: label 12/16 500 `--text-secondary` → 6 px → control 32 → 6 px → helper 12/16
`--text-tertiary`. Blocks 20 px apart. Mono variant sets `--font-mono` and `spellcheck=false`.

### 13.8 Select

Same shell as Input (32 px, radius 8) plus a 16 px chevron at `right: 10px`, `--text-tertiary`,
rotating 180° over 150 ms when open. Opens a 32 px-row menu (`--bg-elevated`, radius 12,
`--shadow-2`, 6 px padding) with a check mark in a 20 px indicator column; selected row gets
`--bg-active`. The trigger reports `aria-haspopup="listbox"`, `aria-expanded`, and the list uses
`role="listbox"`/`option` with `aria-selected`. Type-ahead jumps to the first matching option;
`Esc` closes and restores focus. Multi-select variant renders chips inside the trigger (max 2, then
"+N"), each chip 20 px with a 12 px remove button.

### 13.9 Checkbox

18 × 18 box, radius 4, 1 px `--border-strong`, transparent fill, 24 × 24 hit area (label is part of
the target). Checked: `--mint` fill, `--text-inverse` 12 px check. Indeterminate: `--mint` fill with
a 10 × 2 px dash. Focus ring 2 px at offset 2 around the 18 px box. Disabled: `--border-subtle`,
`--text-disabled` label. Replaces the native control — the source's native checkbox cannot be
themed (`ComponentSpec.md` §10).

### 13.10 Badge

Height 20 px, padding `0 6px`, radius 4, `--font-micro` (11/14 600, uppercase). Optional 6 px dot.
Variants: `neutral` (`--bg-pressed`, `--text-secondary`), `mint`, `success`, `warning`, `danger`,
`info` — each uses its `-soft` fill with the hue as text (all ≥5.4:1, §3.3). A count badge inside a
nav row is `neutral`, 20 px tall, right-aligned, `min-width: 20px`, centred. Badges never wrap and
never carry an action; a clickable chip is a Button at sm size.

### 13.11 StatusDot

Anatomy: 8 px circle + 8 px gap + label `--font-micro` `--text-secondary`; the dot is
`aria-hidden="true"` and the *text* carries the state, so colour is never the only channel.

| State | Dot | Label | Animation |
|---|---|---|---|
| live | `--success` | LIVE | 2 s pulse ring (opacity .35→0, scale 1→1.8), reduced-motion static |
| ready | `--info` | READY | none |
| starting | `--warning` | STARTING | 1.2 s opacity pulse |
| draining | `--info` | DRAINING | none |
| degraded | `--warning` | DEGRADED | none |
| down | `--danger` | DOWN | none |
| unknown | `--text-muted` | UNKNOWN | none |

Vocabulary is exactly `INTERFACE.md` §State machines (node: STARTING/READY/LIVE/DRAINING/DOWN;
account adds NEW/LOGGING_IN/AUTHENTICATED/SERVERS_OK/STOPPED/SUSPENDED/AUTH_FAILED/
MEMBERSHIP_EXPIRED). No component invents a state name.

### 13.12 Card

`--bg-surface`, 1 px `--border`, radius 12, padding 20, no shadow. Header row: title `--font-h2` +
optional description `--font-sm` `--text-secondary`, actions right-aligned, 12 px to the body.
Footer (optional) 48 px, 1 px `--border-subtle` top rule, actions right.

| State | Behaviour |
|---|---|
| static | border only |
| interactive (whole card is a link) | hover: border → `--border-strong`, `--bg-hover` overlay, chevron 16 px fades in at the right over 150 ms |
| selected | 1 px `--border-mint` + `--bg-active` |
| loading | body replaced by Skeletons, header stays; `aria-busy="true"` |
| error | header stays; body is an inline error block (icon 20 `--danger`, message, Retry ghost Button) |
| disabled | `opacity: .55`, `pointer-events: none`, `aria-disabled="true"` |

### 13.13 MetricTile

A Card with fixed internals. Height 120 px (with sparkline) / 96 px (without). Padding 16.

Anatomy: label row (12/16 500 `--text-secondary`, optional info tooltip) → value row (28/34 600
`--font-metric`, `min-height: 34px`) → delta row (12/16, `▲ 4.2 %` in `--success` / `▼` in
`--danger`, plus "vs 24 h" in `--text-tertiary`) → optional Sparkline 32 px.

| State | Behaviour |
|---|---|
| loading | label stays; value slot renders a 24 px Skeleton bar at 60 % width; `aria-busy` |
| no data | value `—` in `--text-tertiary`, delta row hidden |
| stale (poll failed once) | value dims to `--text-secondary`, a 12 px warning dot sits next to the label, tooltip carries the last-good timestamp |
| alert | left edge 2 px `--warning`/`--danger` rule when a threshold is crossed (quota ≥80 %, error rate ≥5 %) |

Value slot reserves its height so a poll never shifts layout (`ComponentSpec.md` §7 port
checklist). Tile grid: 4-up ≥1280, 3-up 1024–1279, 2-up 768–1023, 1-up <768.

### 13.14 DataTable

Container: Card with 0 padding, radius 12, `overflow: hidden` only on the header corners.

| Part | Value |
|---|---|
| Header | 36 px, `--bg-sunken`, `--font-xs` (12/16 500) `--text-tertiary`, uppercase off, sort chevron 12 px, sticky (`--z-sticky`) |
| Row | **36 px**, `--font-body` 14/20, 1 px `--border-subtle` divider, cells padding `0 12px`, first/last cell 16 px |
| Cell types | text, mono (`--font-mono-sm`, right-aligned for numbers), status (StatusDot), badge, actions (IconButtons, revealed on row hover/focus) |
| Selection | leading 40 px column with Checkbox 18 px; header checkbox is tri-state |
| Empty | the EmptyState component fills the body at 240 px |
| Loading | 6 skeleton rows at 36 px, header stays interactive |
| Error | 240 px block with message + Retry |

| State | Behaviour |
|---|---|
| row hover | `--bg-hover` on the row; actions fade 0→1 in 150 ms |
| row focus | ring drawn **inside** the row (`outline-offset: -2px`) so the table does not clip it |
| row selected | `--bg-active`; a 2 px mint rule on the leading edge |
| sorted | one column at a time; `aria-sort` is ascending or descending; sort is client-side for ≤500 rows, server-side (`?sort=&order=`) above that |
| column resize | drag handle 4 px on the header's right edge, min 80 px; widths persist per page in `localStorage` |
| >8 columns at <1280 | low-priority columns collapse into a secondary line inside the row; **never** a horizontal scrollbar |

Accessibility: real `<table>` with `<caption class="sr-only">`, `<th scope="col">`, `aria-rowcount`
when virtualised. Row actions are reachable by Tab in DOM order; the row itself is not a link.

### 13.15 Tabs

Underline style (not pills). Height 36 px, gap 24 px, no horizontal padding, `--font-body-strong`
500. Active: `--text-primary` + 2 px `--mint` underline flush with the container's bottom border;
inactive: `--text-secondary`; hover: `--text-primary` and a 2 px `--border-stronger` underline.
Count badge after the label is `neutral`, 18 px.

States: focus-visible ring at offset 2 (the source has **no** focus indicator — `ComponentSpec.md`
§11 — do not copy that); disabled tab `--text-disabled`, not focusable; overflow at <768 becomes a
Select. ARIA: `role="tablist"`, `role="tab"`, `aria-selected`, `aria-controls`, roving tabindex,
←/→/Home/End. The panel gets `role="tabpanel"` + `tabindex="0"`.

### 13.16 SegmentedControl

Track: height 32, radius 8, padding 2, gap 2, `--bg-sunken`, 1 px `--border-subtle`. Options:
height 28, padding `0 12px`, radius 6, `--font-xs` 500, `--text-secondary`; selected option text
`--text-primary`. Thumb: an absolutely positioned element, `translateX` + `width` animated over
250 ms `--ease-move` — the source runs this at 300 ms (`ComponentSpec.md` §6, "the signature motion
of the page"); we shorten it to stay inside the brief's 250 ms overlay budget, keeping the curve.
Thumb fill `--bg-active`, 1 px `--border-mint`, `--shadow-1`.

| State | Behaviour |
|---|---|
| hover (unselected) | text → `--text-primary`, 50 % `--bg-hover` fill so hover never looks selected |
| selected | thumb under the option; text `--text-primary` |
| focus-visible | ring on the **option**, offset 2, never on the thumb |
| disabled | whole group at `opacity: .55`; individual option disabled uses `--text-disabled` |
| narrow (<480) | becomes a Select |

ARIA: `role="radiogroup"` with `aria-label`; options `role="radio"` + `aria-checked`; roving
tabindex and arrow keys (the source omits arrow handling — `ComponentSpec.md` §6 flags it).

### 13.17 Drawer

Right-side panel, 480 px (420 at 1024, full width <768). `--bg-elevated`, radius `16px 0 0 16px`,
1 px `--border` on the left, `--shadow-3`, `--z-drawer` above the scrim.

Anatomy: header 64 px (title `--font-h2`, subtitle `--font-sm` `--text-secondary`, close IconButton
32 px at the right, 1 px bottom rule) → body (padding 24, scrolls) → footer 64 px (1 px top rule,
actions right-aligned, primary last).

| State | Behaviour |
|---|---|
| closed | unmounted |
| opening | 250 ms `--ease-enter`: panel `translateX(24px)→0` + opacity; scrim fades 0→1 |
| closing | 200 ms `--ease-exit`; Esc, scrim click and the close button all route here |
| dirty (form changed) | closing is intercepted by a confirm Modal; the drawer's own footer keeps Save/Cancel |
| loading | body shows 3 skeleton blocks; footer actions disabled |
| error | body shows an inline error block, footer keeps Cancel + Retry |
| nested (opened from a table row) | the row stays selected underneath so context is preserved |

Focus: on open, focus moves to the first focusable in the body (or the panel itself if the body is
read-only); on close, focus returns to the trigger. Focus is trapped; `aria-modal="true"`,
`role="dialog"`, `aria-labelledby` → the header title. Body scroll is locked.

### 13.18 Modal

Centred, width 480 (560 for forms, 720 for detail), max `calc(100vw - 32px)`, `--bg-elevated`,
radius 16, `--shadow-3`, `--z-modal`, padding 24, `--scrim` + 4 px blur behind.

Anatomy: header (title `--font-h3` + close IconButton) → body (16 px gap between blocks) →
footer 40 px (actions right, `gap: 8`, cancel before confirm).

| State | Behaviour |
|---|---|
| opening | 250 ms `--ease-enter`: opacity + `scale(.98)→1` |
| closing | 200 ms `--ease-exit` |
| destructive | the confirm Button is `danger`; the body names the object and the irreversible part; the confirm button is **not** auto-focused |
| pending | primary button enters loading, all controls disabled, Esc ignored while the request is in flight |
| error | an inline error block appears above the footer; the modal stays open; the primary button re-enables |
| stacked | at most one modal; a second request replaces the body instead of stacking |

Focus: trap + initial focus on the least destructive control; Esc closes unless pending; clicking
the scrim closes unless the modal is `dismissible={false}` (destructive flows). Return focus to the
trigger on close. `role="dialog"`, `aria-modal`, `aria-labelledby`, `aria-describedby`.

### 13.19 Toast

360 px wide, min-height 48, `--bg-elevated`, radius 12, 1 px `--border`, `--shadow-2`, padding
`12px 16px`, `--z-toast`, fixed bottom-right with 24 px inset, stack gap 8, max 3 visible.

Anatomy: 16 px status icon (`--success`/`--danger`/`--info`/`--warning`) + message `--font-body`
(2-line clamp) + optional action link + 24 px close IconButton. A 2 px progress bar along the bottom
edge shows the remaining time.

| State | Behaviour |
|---|---|
| enter | 250 ms `--ease-enter`: `translateY(8px)→0` + opacity |
| exit | 150 ms `--ease-exit`; also on swipe-right (drag > 80 px) |
| auto-dismiss | 5000 ms; `--danger` 8000 ms `[INFERENCE]`; hover or focus pauses the timer |
| action | one link max ("Undo", "View node"); running it dismisses |
| overflow | the 4th toast pushes the oldest out immediately |

Accessibility: container is `aria-live="polite"` + `aria-atomic="false"`; error toasts use
`role="alert"` (`assertive`). Never move focus into a toast. A toast is never the only record of a
failure — the page's error state must also reflect it.

### 13.20 CopyButton

28 × 28 IconButton (16 px copy glyph) that copies a mono value (IP, port, hash, ID).

| State | Visual | Announcement |
|---|---|---|
| idle | `--text-tertiary` glyph | `aria-label="Copy <what>"` |
| hover/focus | glyph `--text-primary`, `--bg-hover` | — |
| copied | glyph swaps to check in `--success` for 1200 ms, then reverts | visually hidden live region: "Copied" |
| failed (clipboard denied) | glyph `--danger`, tooltip "Press ⌘C" | live region: "Copy failed" |

The value itself stays selectable text in a `--font-mono` span; CopyButton is an accelerator, never
the only way to get the value. Tooltip appears after 400 ms hover, on `--bg-elevated`, 12/16.

### 13.21 Sparkline

Inline SVG, width 100 %, height 32 (tile) or 48 (detail), `viewBox` scaled with
`preserveAspectRatio="none"` for the path only — the end dot is positioned in CSS so it stays
circular. Stroke 2 px in the series colour, area fill the same colour at 10 %, no axis, no grid,
no labels. Last point: 3 px dot + 6 px halo at 20 %.

| State | Behaviour |
|---|---|
| loading | a 2 px flat line at 30 % opacity + shimmer |
| single point | a dot, no line |
| all zeros | a flat line on the baseline, `--text-muted` |
| hover | vertical 1 px crosshair + a 12/16 value chip; the nearest point gets a 4 px dot |

Accessibility: `role="img"` + `aria-label` stating range and trend ("Ingress, last 24 h, peak 41 MB
at 14:00"); a visually hidden `<table>` with 6 sampled points is the fallback. Never the only
representation of a number that also appears as text.

### 13.22 EmptyState

Block of 240 px (page-level 320 px), centred, max-width 360. Icon 32 px `--text-muted` (lucide,
stroke 1.5) → title `--font-h3` → body `--font-sm` `--text-secondary` (2 lines max) → one primary
Button + at most one ghost Button, 12 px apart. Padding-y 40.

| Variant | Icon | Copy shape |
|---|---|---|
| no data yet | `inbox` | "No accounts yet" + what to do + "Add account" |
| filtered to nothing | `search-x` | "No nodes match these filters" + "Clear filters" |
| error | `alert-triangle` in `--danger` | "Couldn't load leases" + reason + "Retry" |
| permission | `lock` | "Admin token required" + where to set it |
| search | `search` | "No results for “<query>”" |

Never use an illustration or an emoji. The body always says what the user can do next.

### 13.23 Skeleton

Fill `--bg-pressed` (8 % white), radius 6 (4 for text lines, 9999 for avatars), no border.
Shimmer: a 90° highlight band (`rgba(255,255,255,.06)`) sweeping left→right over **1200 ms linear**
(`ComponentSpec.md` §7 records the source's 1.2 s shimmer). Shapes: text line 14 px tall at 60–90 %
width, block for cards, circle for avatars, 36 px row for tables.

Rules: skeletons mirror the real layout exactly (same heights, same gaps) so the swap does not
shift; a skeleton is `aria-hidden="true"` while its container carries `aria-busy="true"`; nothing
shimmers for more than 10 s — past that, switch to a static "still loading" message with a Retry;
under `prefers-reduced-motion` the shimmer is removed and the fill stays static.

## 14. Accessibility rules

Target: **WCAG 2.2 AA**, verified with axe-core in Vitest/RTL and one manual keyboard pass per page.

### 14.1 Focus

- The focus indicator is `outline: 2px solid var(--mint); outline-offset: 2px` on every focusable
  element. `--mint` measures 11.95:1 against `--bg-surface` and 13.04:1 against `--bg-base`, so the
  indicator clears 1.4.11 (3:1) everywhere.
- Inside a table row or nav row the ring uses `outline-offset: -2px` so it is not clipped; the ring
  still paints on top of the fill.
- `:focus-visible` only — a mouse click must not leave a ring, but a keyboard press must always
  produce one. Never `outline: none` without a replacement.
- Order is DOM order. No positive `tabindex`. A skip-link to `#main` is the first tab stop.
- Focus is never trapped outside a modal/drawer/palette; inside one it always is, and it returns to
  the trigger on close.

### 14.2 Contrast and colour

- Body text ≥4.5:1, large text (≥24 px, or ≥18.66 px bold) ≥3:1, icons/borders that carry meaning
  ≥3:1, disabled ≥3:1. The §3.7 matrix is the audit; any new token must be added to it.
- Colour is never the sole carrier: status = dot + label, validation = border + message + icon,
  sort = chevron + `aria-sort`, selection = fill + mint edge.
- `--text-muted` is not a text token. If a designer asks for grey text, the answer is
  `--text-tertiary`.

### 14.3 Keyboard map (global)

| Key | Action |
|---|---|
| `⌘K` / `Ctrl+K` | open CommandPalette |
| `Esc` | close the topmost overlay; if none, clear the current filter |
| `?` | open the shortcut sheet (a Modal listing this table) |
| `g` then `o/a/n/l/u/r/g/s` | go to Overview / Accounts / Nodes / Leases / Users / Requests / Logs / Settings |
| `r` | refresh the current page's data |
| `/` | focus the page's search/filter field |
| `↑ ↓` | move within a list/table when it has focus |
| `Enter` | open the focused row's drawer |
| `Space` | toggle the focused row's selection |

Single-letter shortcuts are suppressed while focus is in an input, textarea, select or
`contenteditable`, and while any modifier other than Shift is held. The `g` prefix expires after
1500 ms.

### 14.4 Semantics per surface

- One `<h1>` per page (the topbar title); sections use `<h2>`, cards `<h3>`. Never skip a level.
- Tables are real tables with `<caption>`, `scope`, and `aria-sort`; a card list at <768 keeps the
  same accessible name per item.
- Live regions: toasts `aria-live="polite"` (`role="alert"` for errors); poll-driven counters use
  `aria-live="off"` and expose their last update via a `<time>` element instead of announcing every
  tick.
- Icon-only controls always carry `aria-label`; decorative icons are `aria-hidden="true"`.
- Loading uses `aria-busy="true"` on the container, not a spinner with no text.
- Form errors: `aria-invalid`, `aria-describedby` → message id, and the message is also in the
  DOM (never a tooltip only).

### 14.5 Target size and pointer

Minimum 24 × 24 (WCAG 2.5.8) — house floor is 32 × 32 for anything standalone, 28 × 28 inside a
table row, with 4 px minimum spacing between adjacent targets. This is a deliberate correction of
the X console's 32 px icon buttons inside dense rows (`INDEX.md` item 15 lists them as an a11y gap).

### 14.6 Motion and timing

- `prefers-reduced-motion: reduce` is honoured globally (§10); no parallax, no auto-playing motion.
- Auto-dismissing toasts pause on hover/focus and are recoverable from the page state.
- No interaction requires a double-click or a drag; drag affordances (column resize, drawer swipe)
  always have a keyboard/menu equivalent.

## 15. Do not copy

Measured defects in the two source captures. Each is a thing that looks like a design decision and
is not.

### 15.1 From OpenAI Platform (`TokenSpec.md`, `ComponentSpec.md`, `MotionSpec.md`)

| Do not copy | Why |
|---|---|
| The active+focus nav fill override (`#1c1c1c`, `styles.css:2583`) | A focused *active* row turns lighter than an active row — a visible bug (`ComponentSpec.md` §2) |
| Input focus as a 1 px border brightening (25 %→50 % white) | No thickness change, no offset → fails 2.4.7 / 1.4.11 (`ComponentSpec.md` §10) |
| The native `<input type=checkbox>` | Unthemeable, renders differently per browser (`ComponentSpec.md` §10) |
| Tabs with `outline: none` and colour-only active state | Fails 1.4.1 and 2.4.7 (`ComponentSpec.md` §11) |
| White on `#ba8e00` (3.02:1) | Fails AA for normal text (`TokenSpec.md` §1.5) |
| 36 px nav rows as the only target size | Below the 44 px comfort target; our rows are 36 px **inside** a 248 px rail but every row is 36 px tall with full-width hit area, and standalone controls are ≥32 px |
| 22 px / 44 px skeleton bars read as font sizes | The big-number type was never rendered; `[UNVERIFIED]` in the source (`ComponentSpec.md` §7) |
| A 260 px dropdown that violates `--menu-min-width: 300px` | Evidence of an untracked override (`ComponentSpec.md` §3b) |
| `--scale` used but never defined | Press feedback silently became `scale(1)` in the capture (`MotionSpec.md` §3.1) |
| `#a64d6b` / `#389f86` chart lines | Video-only, AA-contaminated, not tokens (`TokenSpec.md` §1.7) |
| The 600 ms modal enter | Reads as sluggish; our ceiling is 250 ms (§10) |

### 15.2 From X Developer Console (`INDEX.md` item 15, `X-TokenSpec.md`)

| Do not copy | Why |
|---|---|
| The 15 px-low hover pill | A rendering offset, not a design (`X-Components.md` §3.3 corrects `X-TokenSpec.md` §4) |
| `corner-shape: squircle` at 14.4 px | A `@supports`-only effect that reads as 8 px elsewhere (`INDEX.md` item 5) |
| The `w-min` wordmark wrap | Text wrapping is a bug at narrow widths |
| The ~85 % main-panel-scoped palette scrim | Inconsistent with the 40 % modal scrim; ours is one `--scrim` (`INDEX.md` item 12) |
| `#1D9BF0` as a product accent | Vendor brand colour; ours is mint |
| The `#A0A0A0` disabled-primary fill | Looks enabled (`X-Components.md` §10) |
| Odometer digits | Decorative animation on numbers that must be readable at a glance |
| `#737373` body text (4.18:1) | Fails AA (`INDEX.md` item 15) |
| 32 × 32 icon buttons in dense rows with no spacing | Fails the 24 px target rule in practice; ours keeps 4 px gaps |

Also not copied from either source: the blurred hero PNG, promotional cards, credit/yellow blocks,
and the "New" discovery purple — none of them have a job in an operator console.

## 16. Token adoption checklist (for the implementer)

1. Declare every token in §3–§11 once, on `:root`. No component-local hex.
2. Add `color-scheme: dark` on `:root` so form controls and scrollbars render dark.
3. Set `scrollbar-color: var(--border-strong) transparent; scrollbar-width: thin` — matches the
   launcher and OpenAI's approach (`TokenSpec.md` §1.4, scrollbar thumb measured `#646464`).
4. Global resets: `box-sizing: border-box`, `margin: 0`, `line-height` from the type tokens,
   `-webkit-font-smoothing: antialiased`, `text-rendering: optimizeLegibility` off (it hurts
   Inter).
5. `::selection` = `rgba(94,234,212,.24)` with `--text-primary`.
6. One `:focus-visible` rule at the root, inherited by everything; components may only change the
   offset.
7. One reduced-motion block at the root that zeroes durations and drops transforms.
8. No second spacing scale, no second radius ladder, no raw `z-index`.
9. Dark-only in v1. Do **not** ship a `prefers-color-scheme: light` branch; the tokens are one
   palette and a half-built light theme is worse than none.

## 17. Open items

| Item | Status | Owner decision needed |
|---|---|---|
| Chart library theming | Recharts cannot read CSS vars directly; the implementer must pass resolved hex from a token map | no — derive from §3.6 |
| Virtualised table above 500 rows | v1 renders up to 500 rows unpaginated, then pages server-side | no — v1 tradeoff |
| Light theme | out of scope for v1 | yes, later |
| Density toggle (28/32/36 rows) | not in v1; the 36 px row is fixed | yes, later |
| Per-page saved column widths | `localStorage`, key `ajs.table.<page>` | no |
| Font subsetting | ship Inter + JetBrains Mono latin subset only (~120 KB total woff2) | no |
| The brief's `#6F857B` tertiary | replaced for text by `#829A90` (§3.8) | **yes — confirm the substitution** |

Anything not listed here is settled by this document. If an implementation question is not
answered by a row above, the answer is "use the token that already exists", not "add one".
