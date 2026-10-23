# AJiaSu Proxy Console

Operator dashboard for `ajs-accountd` — accounts, nodes, leases, users, request telemetry and the
audit trail in one Obsidian Mint surface. **Mock-first:** the default build runs on deterministic
in-memory data and never contacts AJiaSu, a vendor binary or a live daemon. A live mode against the
local `ajs-accountd` skeleton is also built and verified; it is opt-in through build-time
environment variables (see [Modes](#modes) and [Live mode](#live-mode)).

- Stack: Vite 6, React 19, TypeScript, React Router, `lucide-react`, `recharts`, `clsx`.
- Styling: plain CSS variables, no Tailwind, no component library.
- Tests: Vitest + React Testing Library + jsdom, 65 tests.
- Package manager: npm.
- Status: `v0.1.0` implementation slice. Local git commits only (not pushed), no credentials, no live vendor calls.

## Modes

The mode is fixed at build time; there is no runtime switch.

| Mode | Build trigger | Data source | Reset |
|---|---|---|---|
| Mock (default) | `VITE_USE_MOCK` unset or `true` | deterministic in-browser store (`src/lib/mock.ts`), seeded on page load | **Settings → Reset dataset**, or a full reload |
| Live | `VITE_USE_MOCK=false` plus `VITE_API_BASE` and `VITE_PANEL_TOKEN` | `ajs-accountd` over HTTP under `/v1/...` | not offered; the reset card is not rendered |

The shell reports the active mode: the topbar pill reads `mock data` or `live API`, and the sidebar
version reads `v0.1.0 · mock` or `v0.1.0 · live`. The Lab workspace entry is mock-only.

## Quick start

```bash
npm install
npm run dev        # http://127.0.0.1:5173
npm run build      # tsc -b && vite build → dist/
npm run preview    # http://127.0.0.1:4173, serves dist/
npm test           # Vitest, 65 tests
npm run typecheck  # tsc -b
```

Node 24 / npm 11 is the reference toolchain. `VITE_USE_MOCK` defaults to `true`; the default build
needs no backend for any page.

## Live mode

The live build points the SPA at `ajs-accountd`. Vite reads these variables at build time, so the
daemon URL and token are baked into `dist/`.

```bash
# 1. daemon, loopback only (from the repo root; leave it running)
cd daemon
AJS_LISTEN=127.0.0.1:2261 AJS_ADMIN_TOKEN=local-test-token \
  AJS_CORS_ORIGINS=http://127.0.0.1:18082,http://127.0.0.1:18081 \
  go run ./cmd/ajs-accountd

# 2. live build (from dashboard/, in a second shell)
VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 \
  VITE_PANEL_TOKEN=local-test-token npm run build

# 3. serve the static build
npx vite preview --host 127.0.0.1 --port 18082
```

Open `http://127.0.0.1:18082`. `vite preview` defaults to port 4173 in `vite.config.ts`;
`--port 18082` overrides it for this deployment. The preview server does not proxy: the browser
calls the daemon directly, so the daemon must allow the preview origin in `AJS_CORS_ORIGINS` (the
verified run used `http://127.0.0.1:18082,http://127.0.0.1:18081`).

**Token:** `local-test-token` is a loopback placeholder, not a production secret. Every `VITE_*`
value is readable in the shipped bundle, so production must terminate auth in a BFF or reverse
proxy and keep `ajs-accountd` bound to `127.0.0.1`.

## Polling and stale data

Every list page refreshes itself on the interval from `docs/PAGES.md` §2.2. Polling lives in one
place — `src/lib/useAsync.ts` — and each page opts in with `{ intervalMs }`.

| Surface | Interval | Notes |
|---|---|---|
| Overview metrics | 10 s | the same page polls accounts, nodes, leases and audit at 15 s |
| Accounts / Nodes / Leases / Users | 15 s | Users polls both users and API keys |
| Requests | 10 s | the table has no range switch in v1, so the short cadence is always on |
| Logs (audit) | 15 s | SSE runtime logs remain `[PROPOSED]` |
| Settings | — | manual only |

Rules shared by every polling hook:

- **Hidden tabs pause.** `visibilitychange` clears the timer while
  `document.visibilityState === 'hidden'`; becoming visible refreshes once and restarts the cadence.
- **No stacked requests.** A timer tick during an in-flight request is dropped. A manual refresh
  during an in-flight request is queued behind it instead of opening a second request.
- **Last good data survives a failed poll.** The table keeps its rows, `status` stays `ready`, and
  the hook records `staleAt` (first failure) plus `error`; the next success clears both.
- **No skeleton after the first load.** `status` only returns to `loading` when there is no data yet,
  so a refresh never flashes the page skeleton.
- **Manual refresh always works.** The topbar refresh button, the per-page refresh buttons and the
  `ajs:refresh` window event call `reload()`, which restarts the timer.
- **Subtle progress line.** A 1 px mint line under the topbar tracks the global in-flight count
  (`useRefreshActivity`). It is absolutely positioned, so it never shifts layout.
- **Stale marker.** While a page holds stale data, a 12 px warning dot sits next to the page title,
  with the last-good timestamp and the error in its tooltip (`PageHeader`'s `refresh` prop).

`tests/useAsync.test.tsx` drives the hook with Vitest fake timers — interval, hidden pause, visible
resume, unmount, overlapping polls, failed poll, manual reload, dependency change and
`enabled: false` — and asserts the stale dot renders only while stale.

## Command palette data search

`Ctrl/Cmd+K` searches more than pages and actions: once the palette opens it also searches the
live **Accounts** and **Nodes** datasets.

- **Load on open.** Each open fetches `api.listAccounts()` and `api.listNodes()` once, in
  parallel. Failures are swallowed — navigation and action entries keep working — and results
  that resolve after the palette closes are dropped, so a slow response never leaks into the
  next session.
- **Two-character threshold.** Account and Node entries join only when the query has at least
  2 non-space characters; empty and one-character queries look exactly like the pages/actions-only
  palette. The score/order behaviour (`scoreEntry`) is unchanged — data entries compete with
  pages and actions on the same scale, so exact label matches outrank fuzzy keyword hits and
  every page/action stays reachable.
- **Entry shape.** Account entries show the account `label` with a `Wallet` icon in the
  `Account` section; keywords are `username platform regions…`. Node entries show the node
  `label` with a `Server` icon in the `Node` section; keywords are `accountLabel region city
  egressIp`. Credentials and full egress IPs never appear in a label — they only feed keyword
  matching.
- **Navigation contract.** Selecting an account navigates to
  `/accounts?q=<label>`; selecting a node navigates to `/nodes?q=<label>` (both
  `encodeURIComponent`-encoded). No destructive or mutating actions were added to the palette.

### `?q=` search-param contract

Accounts and Nodes read a `q` search param and use it to prefill their search box:

- On first mount, `query` is initialized from `?q=` (empty when absent).
- When `q` changes while the page is mounted — e.g. the palette navigates from
  `/accounts?q=Field Lab` to `/nodes?q=jp-tyo-01` — the search box follows the new value, so
  the table filter updates without a remount.
- Re-renders that leave `q` alone never clobber a query typed into the box: opening or closing
  the Accounts create drawer (`?new=1`, which is preserved independently) does not touch the
  search.
- The filter itself is unchanged: Accounts matches label, username and regions; Nodes matches
  node label, account label, city and egress IP.

`tests/palette-search.test.tsx` covers the account and node round-trips (palette → URL →
prefilled table), the two-character threshold (empty and one-character queries show no data
entries), palette resilience when both list APIs reject, and the mounted-page `q` update plus
the typed-query preservation on unrelated param changes.

## HMC layout

The HMC build host serves three loopback-only endpoints:

| HMC endpoint | Role | How it runs |
|---|---|---|
| `127.0.0.1:18081` | mock dashboard | `docker compose up -d` in `dashboard/`; nginx image built from the default (mock) source, rebuilt after the live/mock copy fixes |
| `127.0.0.1:18082` | live dashboard | `npx vite preview` over a `dist/` built with the live environment |
| `127.0.0.1:2261` | `ajs-accountd` | `go build -o ajs-accountd ./cmd/ajs-accountd` then `./ajs-accountd` with the env above; JSON snapshot store (`AJS_STATE_FILE`) |

Nothing is exposed beyond HMC loopback. From Windows, forward the ports:

```bash
ssh -N -L 18081:127.0.0.1:18081 hmc
ssh -N -L 18082:127.0.0.1:18082 -L 2261:127.0.0.1:2261 hmc
```

Then use `http://127.0.0.1:18081` for the mock build and `http://127.0.0.1:18082` for the live
build on Windows; the second tunnel also exposes the daemon at `http://127.0.0.1:2261`.

`/srv/dwgxhomecloud/scratch/loveplus-dashboard` is a build/run scratch copy of `dashboard/` only —
it is not a second source of truth. Product source stays on Windows at
`D:/Project/LovePlusSpeedToProxy`, and the scratch tree is recreated by copying `dashboard/`
(see [Verification](#verification-recorded-2026-10-23)).

## Verified live integration

On 2026-10-23 the live build at `127.0.0.1:18082` was driven against the daemon at
`127.0.0.1:2261`. Every flow below completed in the UI and was confirmed in the daemon state:

| Flow | Observed |
|---|---|
| Account create (Accounts drawer) | account added, list refreshed |
| API key create (Users → **Create key**) | secret shown once; list shows the prefix only |
| Node drain / undrain | node status flips in the list |
| Lease rotate / release | rotated lease returns a new proxy URL; released lease leaves the list |
| User suspend / resume | user state flips |
| Settings save | `PATCH /v1/settings` accepted; `GET /v1/settings` returns the saved values |
| Request mutation lab | request appended to the Requests list |

Backend check with `curl -H 'Authorization: Bearer local-test-token'` confirmed the same state:
`GET /v1/accounts` returned 7 accounts and `GET /v1/requests` returned 41 rows, including the rows
created from the dashboard. `GET /healthz` returned
`{"status":"ok","service":"ajs-accountd","version":"0.1.0"}`.

**Backend limitation (historical, 2026-10-23 — checked against the earlier skeleton and its
7-account seed):** at that point `ajs-accountd` was an in-memory skeleton that reseeded from the
dashboard mock on every start, with no Resin publisher, vendor login, SQLite store or gate wired
in, and no vendor request made. Current HEAD no longer matches that note: the daemon persists a
versioned JSON snapshot (`AJS_STATE_FILE`), the Resin publisher/runtime and the node
controller/gate are wired, and vendor login is default-off (`AJS_ACCOUNT_LOGIN=1` opts in);
SQLite remains the target schema (`docs/PANEL.md` §6), not the current storage.

## Verification (recorded 2026-10-23)

Build and test were executed on the HMC build host, not on the Windows product tree:

```bash
# copy only dashboard/ source to the HMC scratch tree (node_modules stays on the host)
cd dashboard && tar -czf - --exclude=./node_modules --exclude=./dist --exclude=./dist-mock . \
  | ssh hmc 'mkdir -p /srv/dwgxhomecloud/scratch/loveplus-dashboard && tar -xzf - -C /srv/dwgxhomecloud/scratch/loveplus-dashboard'
ssh hmc 'cd /srv/dwgxhomecloud/scratch/loveplus-dashboard && npm install && npm run build && npm test'
```

Both modes were built from the same source on HMC (Accounts view slice included):

| Build | Command | Result |
|---|---|---|
| Mock (default) | `npm run build -- --outDir dist-mock` | exit 0, `✓ built in 2.58s`, `dist-mock/assets/index-DmnOxSn9.js` |
| Live | `VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 VITE_PANEL_TOKEN=local-test-token npm run build` | exit 0, `✓ built in 2.51s`, `dist/assets/index-Bx_6YADv.js` |
| Typecheck | `npm run typecheck` | exit 0 |

`npm test` — exit 0, `Test Files 10 passed (10)`, `Tests 65 passed (65)` (palette data search
slice included):

| File | Tests |
|---|---|
| `tests/api.test.ts` | 9 |
| `tests/accounts-view.test.tsx` | 7 |
| `tests/app.test.tsx` | 6 |
| `tests/interactions.test.tsx` | 9 |
| `tests/live-mode.test.tsx` | 1 |
| `tests/palette-focus.test.tsx` | 3 |
| `tests/palette-search.test.tsx` | 7 |
| `tests/theme-accent.test.tsx` | 8 |
| `tests/theme-contrast.test.ts` | 2 |
| `tests/useAsync.test.tsx` | 13 |
| **Total** | **65** |

The contrast pass re-ran the computed-style audit on the built SPA at `/accounts` (1440×900, both
themes): `.kbd` (`Ctrl K`) and the inactive `.segmented__item` measure 4.91:1 on `#f4f7f5` in light
mode and 5.66:1 on `#0b0f0e` in dark mode; both were 3.96:1 in light before the token change.

Docker image built on HMC (`docker build -t loveplus-dashboard:local .`) and smoke-tested without a
host port mapping:

```text
ok                              # GET /healthz inside the container
HTTP/1.1 200 OK                 # GET /
HTTP/1.1 200 OK                 # GET /settings (SPA fallback)
```

Visual proof: eight 1440×900 desktop screenshots, eight 390×844 mobile screenshots
(`mobile-overview.png` … `mobile-settings.png`), `overview-fit.png`, and `mobile-nav.png` were
generated against the built SPA on HMC with headless Chromium:

```bash
chromium --headless --no-sandbox --disable-gpu --hide-scrollbars \
  --window-size=1440,900 --virtual-time-budget=5000 \
  --screenshot=screenshots/overview-fit.png http://127.0.0.1:18081/
```

After the polling slice the mock build was re-rendered from `dist-mock` on a scratch port
(`vite preview --port 18085`): Overview keeps the same metric grid, chart and activity list, the DOM
carries the idle `.topbar__progress` element, no `.stale-dot` appears while the mock data is fresh,
and Accounts still renders its 6-row table.

The Accounts view slice added `screenshots/accounts-cards.png` (1440×900) and
`screenshots/mobile-accounts-cards.png` (390×844), captured after switching to **Cards** on the mock
build with headless Chromium over CDP. The same capture measured 6 cards, 0 `.table-wrap`,
`document.documentElement.scrollWidth` equal to 1440 and 390 (no horizontal overflow), three grid
columns at 1440 and one 336 px column at 390.

The accent slice rebuilt the mock bundle on HMC (`npm run build` → exit 0, `✓ built in 2.91s`,
`dist/assets/index-DYCJ434a.js`) and re-ran the suite (`Test Files 9 passed (9)`,
`Tests 58 passed (58)`). `vite preview` on port 18086 served `dist` to headless Chromium over CDP;
clicking **Indigo**, **Violet** and **Orange** on `/settings` produced
`screenshots/accent-{indigo,violet,orange}-settings-{1440,390}.png` plus
`accent-{indigo,violet,orange}-overview-1440.png` for the accent consumers (active nav bar/icon,
chart egress, primary button). The same run recorded `data-accent` and `ajs.accent` in sync, a 2 px
selected ring, all four 12 px swatches, `scrollWidth` equal to 1440/390 with no horizontal overflow,
and `--on-mint` at 4.5:1 or better on both `--mint` and `--mint-strong` (indigo `6.42` / `4.98`,
violet `10.06` / `6.82`, orange `7.82` / `6.31`). Full audit: `screenshots/accent-audit.json`
(empty `failures`).

The palette data-search slice re-ran the same HMC flow: source copied to the scratch tree,
`npm test` → exit 0 (`Test Files 10 passed (10)`, `Tests 65 passed (65)`), `npm run build` →
exit 0. `vite preview` served the fresh mock `dist` to headless Chromium over CDP; typing
`Northwind` into the palette produced the Account entry (`Northwind Ops`, section `Account`),
and clicking it navigated to `/accounts?q=Northwind%20Ops` with the table filtered to the one
matching row — captured as `screenshots/palette-data-search.png` (palette open, 1440×900) and
`screenshots/palette-data-search-accounts.png` (filtered Accounts page, 1440×900).

## Tests and QA

- 65 Vitest tests in 10 files (see [Verification](#verification-recorded-2026-10-23)).
  `tests/useAsync.test.tsx` drives polling with fake timers (interval, hidden pause, resume, unmount,
  dedupe, failed poll, reload, dependency change, `enabled: false`) and checks the stale dot plus the
  in-flight activity flag behind the topbar progress line.
  `tests/accounts-view.test.tsx` covers the table default, switching both ways, the
  `ajs.accounts.view` preference across remounts, an invalid stored value, card anatomy, card
  selection plus the shared test-login action, the mobile grid and its CSS contract.
  `tests/live-mode.test.tsx` stubs `VITE_USE_MOCK=false` before importing the app, then asserts the
  live shell (`live API`, `v0.1.0 · live`) and the absence of the reset card.
  `tests/theme-contrast.test.ts` parses the `:root` and `[data-theme="light"]` token blocks from
  `app.css?raw` and asserts `--text`, `--text-2`, `--muted` and `--text-tertiary` stay at 4.5:1 or
  better on `--bg`, `--surface`, `--elevated` and `--sidebar` in both themes.
  `tests/theme-accent.test.tsx` clicks each accent in Settings, checks `data-accent`, `ajs.accent`,
  the computed `--mint`, persistence across a remount and the invalid-value fallback; it also parses
  the eight accent blocks, recomputes `--on-mint` on `--mint` and `--mint-strong` (≥4.5:1, 16 pairs),
  rejects any accent equal to `--info` or `--warning`, and verifies the live cascade plus the four
  swatches and `aria-pressed` state.
  `tests/palette-search.test.tsx` covers the command palette data search end to end: typing an
  account label or a node keyword surfaces the Account/Node entry, clicking it navigates with
  `?q=` and the target page's table filters to the match; empty and one-character queries keep
  data entries out; both list APIs rejecting leaves pages and actions intact; and the `?q=`
  prefill updates a mounted Accounts/Nodes page without clobbering a typed query on unrelated
  param changes.
- Route sweep on the mock build: all 8 routes at 1440×900 and 390×844.

| Viewport | Routes checked | `scrollWidth` | Console/page errors |
|---|---|---|---|
| 1440×900 | 8 | 1440 — no horizontal overflow | 0 |
| 390×844 | 8 | 390 — no horizontal overflow | 0 |

At 1440×900 Overview measures `scrollHeight = 900`, so it fits one screen. At 390×844 tables become
card lists — zero `.table-wrap` elements — and the mock seed renders 6/12/8/6/40/20 row cards for
Accounts/Nodes/Leases/Users/Requests/Logs, with action buttons inside each card.

Interaction proof on the mock build at 1440×900 (Playwright-core driving `/usr/bin/chromium`):

| File | Interaction |
|---|---|
| `screenshots/palette.png` | `Ctrl+K`, type `leases`, palette filtered and Leases selected |
| `screenshots/palette-data-search.png` | `Ctrl+K`, type `Northwind`, Account entry `Northwind Ops` listed under section Account |
| `screenshots/palette-data-search-accounts.png` | click the Account entry → `/accounts?q=Northwind%20Ops`, table filtered to the one matching row |
| `screenshots/api-key-modal.png` | `/users` → **Create API key** → wait for users → fill Label → **Create key** (show-once secret) |
| `screenshots/toast.png` | `/leases` → first **Copy** → toast `Proxy URL copied` |
| `screenshots/mobile-nav.png` | 390×844 → **Open navigation** → sidebar drawer |

## Routes

| Route | Page | Primary surfaces |
|---|---|---|
| `/` | Overview | 4 metric tiles, ingress/egress chart, account health, activity, quick actions |
| `/accounts` | Accounts | filterable table or card grid (view switch), add drawer, login test, pause/resume, delete confirm, bulk select |
| `/nodes` | Nodes | region/status filters, latency sparklines, success/capacity meters, drain toggle, detail drawer |
| `/leases` | Leases | sticky-lease callout, copy proxy URL, rotate, release confirm, TTL |
| `/users` | Users | user cards, quota meters, API keys, show-once secret, revoke, allowlist editor |
| `/requests` | Requests | request log, status/method/source filters, mutation lab drawer |
| `/logs` | Logs | severity/actor filters, audit table, export placeholder |
| `/settings` | Settings | transport, health interval, lease TTL, subscription refresh, theme + accent, reset |

`*` redirects to `/`.

## Layout

```text
src/
  App.tsx                 lazy routes for all 8 pages
  main.tsx                entry, self-hosted Inter / JetBrains Mono
  styles/app.css          tokens, shell, components, responsive, light theme, accents
  lib/
    types.ts              Account, Node, Lease, PanelUser, ApiKey, RequestLog, AuditEvent, Metrics
    mock.ts               6 accounts · 12 nodes · 8 leases · 4 users · 40 requests · 20 audit rows
    api.ts                typed client; mock by default, fetch when VITE_USE_MOCK=false
    format.ts             bytes, dates, TTL, proxy URL helpers
    labels.ts             account/node/severity tone maps
    nav.tsx               navigation + command registry metadata
    theme.tsx             theme + accent provider, localStorage persistence
    useAsync.ts           load/poll/dedupe/stale state, global refresh event
    useFocusTrap.ts       Esc + Tab trap for palette/drawer/modal
  components/
    AppShell, Sidebar, Topbar, CommandPalette, Toast, CopyButton
    DataTable, Overlay (Drawer/Modal/ConfirmDialog), primitives
  pages/                  Overview, Accounts, Nodes, Leases, Users, Requests, Logs, Settings
tests/
  setup.ts                jsdom polyfills (ResizeObserver, clipboard, matchMedia)
  accounts-view.test.tsx  Accounts table/cards switch, persistence, card actions, mobile grid
  app.test.tsx            all routes, palette open/navigate/Esc/filter
  interactions.test.tsx   copy toast, rotate, release, create account, bulk pause, key show-once, mutation lab
  api.test.ts             seed counts, metrics, CRUD, reset, settings persistence
  live-mode.test.tsx      live shell labels, no reset card when VITE_USE_MOCK=false
  palette-focus.test.tsx  command palette focus trap and focus restoration
  palette-search.test.tsx palette Account/Node search, ?q= prefill, API-failure resilience
  theme-accent.test.tsx   accent switch, persistence, tokens, 8-way WCAG contrast
  useAsync.test.tsx       fake-timer polling: interval, hidden pause, dedupe, stale, reload
```

### Accounts views

`/accounts` keeps the table as the default and adds a toolbar segmented control
(`role="group" aria-label="Account view"`, buttons **Table** / **Cards**). The choice is stored in
`localStorage["ajs.accounts.view"]` (`"table"` or `"cards"`); a missing, invalid or unavailable
store falls back to `table` without throwing.

Both views share the same search/state/platform filters, count, bulk bar and action handlers. Cards
render as `<article role="listitem">` inside
`<div class="account-grid" role="list" aria-label="Vendor accounts">`:

| Region | Contents |
|---|---|
| Head | selection checkbox, label + username, state `Badge` with `StatusDot` (pulses while active) |
| Meta | platform chip, region list, or `any region` |
| Metrics | Nodes `live/total` with a thin capacity bar (mint; amber track at 0 live), concurrency, usage via `formatBytes` |
| Foot | `Expires` label, expiry date + relative time, test login, pause/resume, delete (same labels and handlers as the table) |

The grid is `repeat(auto-fill, minmax(280px, 1fr))` with a 12 px gap and collapses to one column
below 760 px; card actions wrap and the card body never forces horizontal scroll. Selected cards get
a 1 px mint inset ring plus a faint mint wash. Hover changes only the border/background (no
translate), and the 150 ms fade + 4 px rise enter is disabled by `prefers-reduced-motion`. The table
keeps its existing below-760 px auto-card behaviour; the explicit card view works at every width.
`tests/accounts-view.test.tsx` covers the default table, switching both ways, the localStorage
preference (including an invalid value), card anatomy, bulk selection plus a card action, the mobile
grid, and the responsive CSS contract.

## Data layer

`src/lib/api.ts` exposes a typed promise API and switches on `VITE_USE_MOCK`:

| Variable | Default | Meaning |
|---|---|---|
| `VITE_USE_MOCK` | `true` | Use the deterministic in-browser store |
| `VITE_API_BASE` | empty (same origin) | Base URL for `ajs-accountd` |
| `VITE_PANEL_TOKEN` | unset | Optional bearer token for the live client |

**Security note:** a Vite `VITE_*` value is embedded in the client bundle. The live build above
embeds `local-test-token`, which is only a loopback placeholder; do not ship a real admin token this
way. Production should terminate auth in a BFF or reverse proxy and keep `ajs-accountd`
loopback-only. The default build remains mock-only and embeds no token.

Live paths implemented by the client (`[SPEC]` = in `docs/PANEL.md`, `[PROPOSED]` = dashboard
contract only):

| Action | Path | Status |
|---|---|---|
| List/create accounts | `GET/POST /v1/accounts` | `[SPEC]` |
| Test login / change state / delete | `POST /v1/accounts/{id}/actions/login`, `PATCH/DELETE /v1/accounts/{id}` | `[SPEC]` |
| List nodes | `GET /v1/nodes` | `[SPEC]` |
| Drain / undrain / probe | `POST /v1/nodes/{id}/actions/{drain\|undrain\|probe}` | `[SPEC]` |
| List / rotate / release leases | `GET /v1/leases`, `POST /v1/leases/{id}/actions/rotate`, `DELETE /v1/leases/{id}` | `[PROPOSED]` |
| Users | `GET /v1/users`, `POST /v1/users/{id}/{suspend\|resume}`, `PATCH /v1/users/{id}` | `[SPEC]` / `[PROPOSED]` |
| API keys | `GET/POST /v1/apikeys`, `DELETE /v1/apikeys/{id}` | `[SPEC]` |
| Requests | `GET /v1/requests`, `POST /v1/requests/mutations` | `[PROPOSED]` |
| Audit | `GET /v1/audit?limit=` | `[SPEC]` |
| Metrics | `GET /v1/metrics?range=24h\|7d` | `[PROPOSED]` |
| Settings | `GET/PATCH /v1/settings` | `[PROPOSED]`; served by `ajs-accountd` |

Every path in this table is served by `ajs-accountd` in `daemon/` (an in-memory store persisted
as a versioned JSON snapshot); `[SPEC]` / `[PROPOSED]` describe where the contract is defined,
not whether it is implemented.

The mock store is module-level and mutable: create/pause/delete accounts, drain nodes, rotate and
release leases, create/revoke keys, edit allowlists, add mutation-lab requests, save settings and
reset the dataset. A full browser reload restores the seed data.

## Design system — Obsidian Mint

Dark-first tokens (light is a derived variant with the same roles):

```css
--bg:#0B0F0E; --sidebar:#0E1412; --surface:#131A17; --elevated:#1A2420; --active:#22302A;
--text:#E6F1EC; --text-2:#A3B8AE; --muted:#7A9086; --text-tertiary:#829A90;
--border:rgba(255,255,255,.08); --border-strong:rgba(255,255,255,.14);
--mint:#5EEAD4; --mint-strong:#2DD4BF; --success:#34D399; --warning:#FBBF24;
--danger:#FB7185; --info:#60A5FA;
--radius-card:12px; --radius-control:8px; --sidebar-w:248px; --topbar-h:64px;
```

`--muted` was raised from `#6F857B` to `#7A9086` (dark) and from `#6B7F75` to `#5C7066` (light) so
the `Ctrl K` kbd hint and the inactive segmented label clear AA instead of measuring 3.96:1 on
`--bg`. Both tokens now clear 4.5:1 on every non-selected surface:

| `--muted` | `--bg` | `--surface` | `--elevated` | `--sidebar` |
|---|---|---|---|---|
| dark `#7A9086` | 5.66 | 5.19 | 4.68 | 5.46 |
| light `#5C7066` | 4.91 | 5.30 | 5.30 | 4.58 |

The AA correction block still routes small labels to `--text-tertiary` (`#829A90` / `#5B6F65`) to
keep the `--text` > `--text-2` > `--text-tertiary` > `--muted` hierarchy. `tests/theme-contrast.test.ts`
parses both token blocks and fails if any of the four text tokens drops below 4.5:1 on `--bg`,
`--surface`, `--elevated` or `--sidebar` in either theme. The small-text rule from
`docs/DESIGN-SYSTEM.md` §3.8 is unchanged; that document now records the raised `--muted` values
alongside the brief's original hex.

- Type: 13/18 UI, 11/14 uppercase labels with +0.06em, 20/26 page title, 28/32 metric, 12/18 mono.
- Controls 32 px, table rows 36 px, content padding 24 px, card gap 16 px.
- Focus ring: 2 px mint with 2 px offset; reduced-motion fallback.
- Motion: 150 ms state, 250 ms overlays, 300 ms panels; enter `cubic-bezier(.19,1,.22,1)`,
  exit `cubic-bezier(.8,0,.4,1)`, move `cubic-bezier(.65,0,.35,1)`.
- Responsive: 248 px sidebar at desktop, 64 px rail from 761–1100 px, off-canvas drawer below
  760 px; data tables become card lists below 760 px (no horizontal document scroll); metric grid
  collapses 4 → 2 → 1.

### Accent themes

The Appearance card on `/settings` pairs the Dark/Light switch with four accents: **Mint**
(default), **Indigo**, **Violet** and **Orange**. The choice is stored in
`localStorage["ajs.accent"]` and applied as `data-accent` on `<html>`; a missing or invalid value
falls back to `mint` without throwing. It is a per-browser preference and is never sent to the
daemon.

Each accent retunes only the accent trio; every existing `var(--mint)` / `var(--mint-strong)`
consumer (focus ring, active nav, badges, meters, selected rows, chart series 1) follows:

| Accent | `data-accent` | Dark `--mint` / `--mint-strong` / `--on-mint` | Light `--mint` / `--mint-strong` / `--on-mint` |
|---|---|---|---|
| Mint (default) | `mint` | `#5EEAD4` / `#2DD4BF` / `#04211C` | `#0F766E` / `#115E59` / `#FFFFFF` |
| Indigo | `indigo` | `#818CF8` / `#6D74F5` / `#0B0A2E` | `#4F46E5` / `#4338CA` / `#FFFFFF` |
| Violet | `violet` | `#C4B5FD` / `#A78BFA` / `#1A0B2E` | `#6D28D9` / `#5B21B6` / `#FFFFFF` |
| Orange | `orange` | `#FB923C` / `#F97316` / `#2A1204` | `#C2410C` / `#9A3412` / `#FFFFFF` |

The overrides live in `app.css` directly after the `[data-theme="light"]` block as
`[data-accent="…"]` (dark) and `[data-theme="light"][data-accent="…"]` (light); mint keeps the base
`:root` / light values. `--on-mint` clears 4.5:1 on both `--mint` and `--mint-strong` (the primary
button hover fill) for all sixteen pairs (4.98–11.45:1), and no accent equals the `--info` or
`--warning` status hue. `tests/theme-accent.test.tsx` parses the blocks, recomputes WCAG contrast on
both fills, checks the status-hue distinctness, and verifies the live cascade, the Settings swatches,
`aria-pressed`, and persistence across remounts.

## Keyboard and accessibility

- `Ctrl/Cmd+K` opens the command palette (fuzzy filter, ↑/↓, Enter, Esc, Tab trap). Queries of
  2+ non-space characters also match Accounts and Nodes entries (see
  [Command palette data search](#command-palette-data-search)).
- Sidebar links, table actions and form controls are keyboard reachable with visible focus.
- Drawers/modals trap focus, close on Esc/backdrop, and restore the previous focus.
- Tables use real `<table>` semantics with an accessible caption; status is dot **and** label.
- Toasts announce through `aria-live="polite"`; destructive actions confirm in a modal.
- Contrast: `--text`, `--text-2`, `--muted` and `--text-tertiary` each measure at least 4.5:1 on
  `--bg`, `--surface`, `--elevated` and `--sidebar` in both themes. `tests/theme-contrast.test.ts`
  recomputes WCAG relative luminance from `app.css?raw` and fails with the offending pair and ratio
  on regression.
- `prefers-reduced-motion` disables animation and transition duration.

## Docker

```bash
docker build -t loveplus-dashboard:local .
docker compose up -d          # http://127.0.0.1:18081
```

`Dockerfile` is a multi-stage Node 24 build → nginx 1.29. The nginx config is written inside the
image: SPA fallback, immutable `/assets/`, and `/healthz`. `compose.yml` binds only
`127.0.0.1:18081`, uses `restart: unless-stopped`, the built-in bridge network
(`network_mode: bridge`) and contains no secrets. After the live/mock copy fixes the image was
rebuilt with `docker compose up -d --build`; the running container serves the updated mock bundle
and reports healthy on `/healthz`.

> **HMC port note (2026-10-23):** host port `18080` is blackholed on the HMC host, so the compose
> mapping publishes `18081` instead. Compose-created custom networks also failed to publish on
> this host, so the service uses the built-in bridge network. Verified with
> `docker compose up -d --force-recreate`:
> `/healthz` → `200`, `/settings` → `200`; the container also serves correctly through
> `docker exec`. The port change needed no application or image change; the later copy-fix rebuild
> is separate.

## Limitations

- `ajs-accountd` persists accounts, nodes, leases, keys and settings in a versioned JSON
  snapshot (`AJS_STATE_FILE`, saved every 30 s and on shutdown, sealed secrets included),
  so the store survives a restart; SQLite is still the target schema (`docs/PANEL.md` §6),
  not the current storage.
- The Resin publisher/runtime wiring and the node controller/gate are in place, and vendor
  login is wired but default-off (`AJS_ACCOUNT_LOGIN=1` opts in). Live vendor login/FV/Resin
  remains `[UNVERIFIED]` without an Owner account.
- Live mode is verified against the local daemon; the tests use fakes and contact no vendor
  endpoint.
- The live build hides the mock reset action entirely — the daemon has no reset endpoint.
- `VITE_PANEL_TOKEN` is build-time and readable from the bundle. There is no per-operator auth,
  role model or session; production needs a BFF or reverse proxy.
- Mock state is in memory; refreshing the tab discards edits (Settings → Reset dataset does the same
  explicitly).
- Log export is a placeholder; SSE, pagination above 500 rows and server-side sorting are not
  implemented in v1. Polling is implemented (see [Polling and stale data](#polling-and-stale-data)).
- Documentation scope, not a limitation: `dashboard/docs/` is the AJS-20 design contract; the
  accent slice only adds §3.9 (accent table) to `DESIGN-SYSTEM.md` and leaves the rest of the
  contract unchanged.

## License

Repository code is MIT; see the root [`LICENSE`](../LICENSE).
