# AJiaSu Proxy Console

Operator dashboard for `ajs-accountd` — accounts, nodes, leases, orders and plans, the device pool,
users, usage, request telemetry, the audit trail, schedules, webhook receivers, gate health and a
read-only Lab instrument in one graphite-and-mint APC surface. **Mock-first:** `npm run dev` and
`npm run build:mock` run on deterministic in-memory data and never contact AJiaSu, a vendor binary
or a live daemon. A live mode against the local `ajs-accountd` skeleton is also built and verified;
it is the release path (`VITE_USE_MOCK=false npm run build`), and a bare `npm run build` fails
closed instead of emitting the demo (see [Modes](#modes) and [Live mode](#live-mode)).

- Stack: Vite 6, React 19, TypeScript, React Router, `lucide-react`, `recharts`, `clsx`.
- Styling: plain CSS variables, no Tailwind, no component library.
- Tests: Vitest + React Testing Library + jsdom, 480 tests in 54 files.
- Language: Chinese/English UI with a `中` / `EN` sidebar switch and a Settings Language section.
- Package manager: npm.
- Status: `v0.1.0` implementation slice. Local git commits only (not pushed), no credentials, and
  the console itself never calls a vendor endpoint — it talks only to `ajs-accountd`.

## Modes

The mode is fixed at build time; there is no runtime switch.

| Mode | Build trigger | Data source | Reset |
|---|---|---|---|
| Mock | `npm run dev`, or `npm run build:mock` (`vite build --mode mock`) | deterministic in-browser store (`src/lib/mock.ts`), seeded on page load | **Settings → Reset dataset**, or a full reload |
| Live | `VITE_USE_MOCK=false npm run build` plus `VITE_API_BASE`; `VITE_PANEL_TOKEN` must be unset (the runtime token gate asks on 401) | `ajs-accountd` over HTTP under `/v1/...` | not offered; the reset card is not rendered |

`vite build --mode mock` is the only demo recipe: every other build mode fails closed unless
`VITE_USE_MOCK=false`, so a bare `npm run build` exits non-zero without writing `dist/`.

The shell reports the active mode with the topbar pill (`mock data` or `live API`); the sidebar
shows the APC mark, the full name and the language/theme controls, and a desktop toggle collapses
it to a 56 px icon rail whose state is stored in `localStorage["ajs.sidebar"]`. The Lab page works
in both modes: deterministic fixtures in mock, `/v1/lab/*` in live; a live 401 opens the runtime
token gate described below.

### Demo-free live store

The mock store is a development fixture, never live data. `ajs-accountd` starts from
`store.NewEmpty()` and loads its JSON snapshot, so a fresh state directory serves zero
accounts/nodes/leases/users/keys/requests and every page shows its empty state instead of demo
rows. The old HMC snapshot was migrated once on 2026-10-24 with `AJS_DROP_DEMO=1`
(`demo_purged accounts=6 nodes=12 leases=8 users=4 keys=6 requests=40 audit=20`); the switch is
unset again, and the live store holds exactly 1 account (`acc-07`) and 1 node
(`acc-07:vvn-2076-264`, `LIVE`). Mock mode is unchanged: it still seeds 6 accounts and 12 nodes
for development and reset. A live restart with no snapshot therefore renders empty lists, not
the mock dataset.

## Language

The console ships in English and Chinese. Both switches are always visible:

| Surface | Control |
|---|---|
| Sidebar footer | `中` / `EN` segmented switch (`role="group"`, `aria-pressed` on the active choice) next to the version and theme button |
| Settings | **Language** section card with a `中文` / `English` segmented control and a note that the choice is per browser |

- **Default.** On first load the stored choice wins; with no stored value the browser hint decides —
  `navigator.language` starting with `zh` selects Chinese, everything else selects English.
- **Persistence.** Every switch writes `localStorage["ajs.lang"]` (`"zh"` or `"en"`). The choice is
  per browser and is never sent to the daemon; when storage is unavailable the in-memory choice
  still applies for the session.
- **Document language.** The provider sets `<html lang>` to `zh-CN` or `en`, so assistive
  technology and the browser see the active language.
- **Coverage.** Every user-visible string — shell, pages, Lab, dialogs, empty/error states — is
  translated; English is the source dictionary and Chinese must mirror every key. The Chinese copy
  is a natural mainland-product rewrite rather than a literal translation: 账号 is the login
  identity (账户 is reserved for billing), and terms follow the product glossary — 新增账号,
  登录爱加速, 客户中心, 流量额度, 租约. Account labels, server names, IPs and API field values are
  data and are never translated. The command palette still finds pages in either language.
- **Tests.** `tests/i18n.test.tsx` asserts dictionary parity and non-empty values, interpolation
  and English/key fallback, switching the sidebar and Lab to Chinese plus `document.lang`, restoring
  a stored choice on a fresh mount, the `zh*` browser default, switching back to English from
  Settings, and palette page search in both directions.

## Quick start

```bash
npm ci             # installs exactly the committed package-lock.json
npm run dev        # http://127.0.0.1:5173 (mock data)
npm run build:mock # tsc -b && vite build --mode mock → dist/ demo console
VITE_USE_MOCK=false npm run build   # tsc -b && vite build → dist/ live bundle
npm run preview    # http://127.0.0.1:4173, serves dist/
npm test           # Vitest, 480 tests in 54 files
npm run typecheck  # tsc -b
```

Node 24 / npm 11 is the reference toolchain. `VITE_USE_MOCK` is mock-first at runtime, so
`npm run dev` and the demo build need no backend for any page; a production `npm run build`
requires `VITE_USE_MOCK=false` and otherwise fails closed.

## Live mode

The live build points the SPA at `ajs-accountd`. Vite reads these variables at build time, so the
daemon URL is baked into `dist/`. The admin token is never baked into a bundle: `vite.config.ts`
aborts any build with `VITE_PANEL_TOKEN` set. The console prompts for a token on the first `401`
and keeps it in `sessionStorage` for that tab; a production deployment should instead terminate
auth in a BFF or reverse proxy.

```bash
# 1. daemon, loopback only (from the repo root; leave it running)
cd daemon
AJS_LISTEN=127.0.0.1:2261 AJS_ALLOW_INSECURE=1 \
  AJS_CORS_ORIGINS=http://127.0.0.1:18082,http://127.0.0.1:18081 \
  go run ./cmd/ajs-accountd

# 2. live build (from dashboard/, in a second shell). No VITE_PANEL_TOKEN:
#    the insecure daemon ignores the bearer header.
VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 npm run build

# 3. serve the static build
npx vite preview --host 127.0.0.1 --port 18082
```

Open `http://127.0.0.1:18082`. `vite preview` defaults to port 4173 in `vite.config.ts`;
`--port 18082` overrides it for this deployment. The preview server does not proxy: the browser
calls the daemon directly, so the daemon must allow the preview origin in `AJS_CORS_ORIGINS` (the
verified run used `http://127.0.0.1:18082,http://127.0.0.1:18081`). A daemon started with the
optional HTTPS listener (`AJS_TLS_CERT`/`AJS_TLS_KEY`) takes an `https://` base instead; the
console only needs the URL, and that listener sends HSTS on every response.

**Token:** `AJS_ALLOW_INSECURE=1` is a loopback-only lab switch (startup refuses it on any other
bind) and leaves the daemon without bearer auth. For a real token, generate one with
`openssl rand -hex 32` — startup refuses documented placeholders and anything shorter than 32
characters — and set it only as `AJS_ADMIN_TOKEN` on the daemon. `vite.config.ts` refuses any
bundle build with `VITE_PANEL_TOKEN` set: every `VITE_*` value is readable in the shipped bundle,
so production must terminate auth in a BFF or reverse proxy and keep `ajs-accountd` bound to
`127.0.0.1`. The LAN build below takes the other route: it bakes no token and collects one per tab
through the runtime token gate.

## Lab page

`/lab` is the Lab instrument over the daemon's telemetry. Every panel reads an endpoint; the only
non-GET action is the guarded egress check, which dials the selected node's own loopback gate. No
vendor call and no vendor credential is used.

| Tab | Reads | Shows |
|---|---|---|
| Overview | `/v1/lab/overview`, `/v1/lab/flags` | count tiles, daemon posture (version, commit, uptime, effective modes), warnings, and effective flags with provenance (`env`/`flag`/`default`); secret flags expose `set` and `redacted` only |
| Gates & sessions | `/v1/lab/gates`, `/v1/lab/sessions` | gate listener/state/byte counters per node and FV session health with a redacted SID and the vendor's claimed egress |
| Egress | `GET /v1/lab/egress`, `POST /v1/lab/egress-check` | claimed-vs-verified rows plus a guarded check for the selected node with a running gate: verified/claimed, match, latency, checkedAt, source `echo`, and the probe error. The observation is merged into that node's row and kept per node, so polls, a manual refresh or a node switch do not clear it. 409 without a running gate, 429 inside the 5 s per-node quota |
| Servers | `GET /v1/accounts/{id}/servers` | the vendor catalog for the selected account; `stale` with an empty list and a reason when the daemon holds no in-memory login result (e.g. after a restart) |
| Protocol | `GET /v1/lab/profiles` | supported version profiles and the sample matrix (platforms, versions, cores, carriers) |
| Device | `GET /v1/accounts/{id}/device` | the stable identity as `sha256:`-prefixed truncated hashes plus non-secret claims |
| Logs | `GET /v1/lab/logs?level=&component=&limit=&since=` | bounded in-memory log ring, newest first (default 200 lines, max 2000); invalid filters answer 400 rather than silently widening |
| Diagnostics | `GET /v1/lab/diagnostics` | component health map, state-snapshot facts (basename path, last save, size), build facts (version, Go version, commit) and warnings |

Behaviour:

- **Honest disabled states.** A mode that is off answers HTTP 200 with `enabled:false` and the
  switch that is off (`AJS_ACCOUNT_LOGIN is not 1`, `lab telemetry is not configured`), so the page
  shows a reason instead of an error or a silently empty table.
- **Read-only page, one probe route.** The page never mutates daemon state.
  The daemon's only non-GET Lab route is `POST /v1/lab/egress-check` — an explicit
  claimed-vs-verified probe through the node's own gate (5 s per-node cooldown; 409 without a
  running gate) — whose result is returned, not stored server-side: the GET egress panel stays
  claim-only, and the client keeps the observation keyed by node in component state. A poll, a
  manual refresh or a node switch therefore keeps the verified IP and latency on that row; a full
  page reload drops it and the row is claim-only again until the next check.
  The UI disables the button for the same 5 s window and surfaces the daemon's 409/429 message.
- **Redaction.** Sessions expose `a1b2…(32)`-style SIDs, device identifiers are truncated
  `sha256:` hashes, and secret flags never carry a value.
- **Mock parity.** In mock mode the same tabs render deterministic fixtures (`src/lib/lab.ts`,
  `src/lib/mock.ts`) and contact no backend; the mock egress check mirrors the 409/429 guards and
  returns the seeded exit, which the client keeps in the same per-node state as a live observation.
- **Runtime token gate.** A live build without `VITE_PANEL_TOKEN` still works. `http()` dispatches
  `ajs:unauthorized` on a 401 (except a vendor login rejection on `/actions/login`), and
  `TokenGate` asks for the admin token; the Settings **General** section's daemon card dispatches
  `ajs:token-required` from **Set token** / **Replace token** for the same dialog. The value is
  kept in `sessionStorage["ajs.panelToken"]` for that tab only, never rendered back, and a submit
  retries every mounted loader with the bearer header. **Clear session token** in Settings drops it.

## Operations page

`/operations` is the scriptable control surface over
`POST /v1/accounts/{id}/actions/select-node` and the durable operation records in
`daemon/internal/api/operations.go`.

- **History.** `GET /v1/operations?limit=200` lists records newest first with state, account,
  target, started/finished stamps, wall duration and the stable error code. The state filter is
  client-side and the shared footer paginates the result. The list starts a 2 s poll only while at
  least one record is `queued` or `running`, and stops as soon as the page is terminal.
- **Select node.** Account → server (the account's retained catalog, with ineligible rows
  disabled) → `ifCurrent` (`noop` / `error` / `reselect`) and a 10–120 s timeout. A 200 answers the
  idempotent already-current result; a 202 answers the queued operation and the panel follows
  `GET /v1/operations/{id}` every second until it is terminal, then reloads the history. A refusal
  keeps the daemon's message, shows the error code in the history, and leaves the form usable.
- **Mock parity.** The mock store seeds terminal records and advances a queued record on each GET
  (queued → running → terminal), so the page is reviewable offline without a background timer.
- **SSE.** The Logs live-events card consumes `/v1/events/stream` through `EventSource`. The client
  closes the source on error and reconnects from the last event id with exponential backoff
  (1 s → 15 s, cursor dropped after five consecutive failures), shows
  `Connecting` / `Live` / `Reconnecting` / `Offline` / `Paused`, and resumes from the same cursor
  when a hidden tab becomes visible. Mock mode replays the deterministic ring and stays open.
  `EventSource` cannot send the panel bearer token, so a token-protected deployment needs the
  same-origin BFF/reverse proxy the live build already assumes.

## Polling and stale data

Every list page refreshes itself on the interval from `docs/PAGES.md` §2.2. Polling lives in one
place — `src/lib/useAsync.ts` — and each page opts in with `{ intervalMs }`. The Operations page
keeps that hook but owns a 2 s timer that exists only while a record is in flight; live events use
SSE instead of polling.

| Surface | Interval | Notes |
|---|---|---|
| Overview metrics | 10 s | the same page polls accounts, nodes, leases and audit at 15 s |
| Accounts / Nodes / Leases / Users | 15 s | Users polls both users and API keys |
| Requests flows | 5 s | polls `/v1/lab/flows?limit=200`; mutation history 10 s, node labels 15 s |
| Operations | 2 s while active | only while a record is queued or running; terminal history costs no requests |
| Logs (audit) | 15 s | audit rows poll; live events stream over SSE `/v1/events/stream` with cursor reconnect |
| Gate health | 15 s | `/v1/health/nodes`; the daemon probes every 30 s, so the panel is one probe behind at most |
| Schedules | 15 s | schedules poll at 15 s, the account list at 30 s |
| Device pool | 30 s | `/v1/device-pool`; the assign dialog loads the account list once on mount |
| Usage | — | manual only; the window/filter change triggers the request |
| Orders | — | manual only; orders and the plan catalogue load on mount and **Refresh** |
| Webhooks | — | manual only; load on mount and **Refresh** |
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
  node label, account label, city and egress IP in the Connected tab and title, id, host, region
  and account label in the Catalog tab.

`tests/palette-search.test.tsx` covers the account and node round-trips (palette → URL →
prefilled table), the two-character threshold (empty and one-character queries show no data
entries), palette resilience when both list APIs reject, and the mounted-page `q` update plus
the typed-query preservation on unrelated param changes.

## Nodes catalog

`/nodes` has two views: **Catalog** (what could be served) and **Connected** (what is running).

- **Catalog** reads one account at a time: the account picker in the page header selects the
  retained login catalog, and `GET /v1/accounts/{id}/servers` is server-paged. The query carries
  `q`, `region`, `status`, `province`, `city`, `sort` (`order|title|host|status|region`), `offset`
  and `limit` (default 50, max 500); the response returns `total`, `regions:[{code,count}]`,
  `nextOffset`, `hasMore` and the `geo` aggregate. The live `acc-07` login result is **2,814 vendor
  servers**, all carrying region `cn`,
  so the footer reads `1–50 of 2,814` and the page shows one *China* group; region labels come
  from `src/lib/regions.ts` (ISO 3166-1 alpha-2 plus a few city subdivisions, English and
  Chinese), and the raw vendor code stays visible as secondary mono text.
- Rows render in per-region groups with a group header, caption and a select-visible checkbox.
  Group headers group the current page's rows but take their count from the server's `regions[]`
  summary, so a region spanning pages never understates its total; the header notes how many rows
  are on this page when the two differ.
- Columns: select checkbox, server title/id, account, region (localised name + code), `host:port`,
  vendor status, runtime state (`Current` for a `LIVE` node, otherwise the node state or
  `not linked`), target badge and flags (`full load`, `member only`, blocking marks,
  `stale snapshot`). Ping-secret presence and skipped-by-limit are not row badges (design §4.5):
  every live row has a ping secret and skipping is the expected state under the node limit, so
  they stay drawer facts instead of row noise.
- Search is debounced 250 ms before it reaches the daemon; region, status and sort changes return
  to offset 0, and the footer's page/size controls request the next window instead of slicing a
  full list. Selection is local UI state keyed by account and server — the bulk bar says so and no
  daemon mutation is made.
- A retained-but-not-refreshed snapshot shows the `stale snapshot` badge plus a callout with the
  fetch time and account; a request failure renders the inline error with Retry; a disabled login
  mode renders the `{enabled:false}` reason. No demo or overseas rows are invented.
- The catalog polls every 30 s, the Connected tab every 15 s. Connected renders `/v1/nodes` with
  region/state filters, latency sparkline, success/capacity meters, drain/undrain, probe and a
  detail drawer. The state filter offers `STALE` between `DRAINING` and `DOWN`: startup recovery
  demotes a persisted `LIVE`/`DRAINING` row whose gate did not survive the restart (no gate port,
  no egress), so the row stays visible with a warning-toned `Stale` badge instead of disappearing
  or still claiming `LIVE`.
- Mock mode serves deterministic fixtures from `src/lib/mock.ts` (including the paging envelope),
  so the same layout is reviewable offline.

### Target and session

A catalog row has two independent states: **target** (the server the next login should use, stored
by the daemon) and **current** (the server whose gate is running). Setting a target writes
`PUT /v1/accounts/{id}/selection` and starts nothing; clearing it is a `DELETE` and never
disconnects the session. The row badge and the drawer both name the target, and a target write
carries the `snapshotId` from the catalog response.

Connect, reconnect and disconnect are the only writes that touch a session, and each goes through
a confirm dialog:

| Action | Route | Behaviour |
|---|---|---|
| **Connect** | `POST /v1/accounts/{id}/actions/connect` `{serverId,snapshotId?,confirm:true}` | opens the new session, commits exactly one `LIVE` node, rolls back on open/gate/store failure; the same server answers `alreadyConnected` without a cooldown |
| **Reconnect** | `POST /v1/accounts/{id}/actions/reconnect` `{snapshotId?,confirm:true}` | reopens the current server, or the target when nothing runs; `no_target` → 404 |
| **Disconnect** | `POST /v1/accounts/{id}/actions/disconnect` `{confirm:true}` | drains and closes every gate of the account, keeps the target; a second call answers `alreadyDisconnected` |

A stale `snapshotId` (the catalog changed after the page loaded) answers 409 `snapshot_changed`; an
ineligible vendor row answers 409 with `details.ineligibleReason`; a repeat inside the 5 s
per-account cooldown answers 429 `connect_cooldown` with `Retry-After`, and the row actions stay
disabled until it elapses. The connect dialog's acknowledgement names the consequence — the current
session will be replaced — because the vendor allows one active FV session per account, so
connecting a second server moves the session instead of adding one. A P0
daemon degrades honestly: a 404 on `/selection` reads as "no target", a catalog response with
`enabled:false` disables every write and shows the daemon's reason, and a missing action route
surfaces as an error in the dialog instead of a success.

### China map and province filter

- The Catalog tab renders a province choropleth beside the toolbar. Geometry is committed in
  `src/assets/geo/cn-provinces.json` (31 mainland provinces, pre-projected SVG paths) and the
  city lookup/points in `cn-cities.json`; the page never calls a map or tile service.
- Province counts come from the daemon's `geo` aggregate on every catalog page
  (`GET /v1/accounts/{id}/servers`): the map, the city Select and the province/city table filters
  all read that one response, so `/nodes` no longer downloads the retained catalog to draw the
  map. The daemon resolves titles with the same exact city table the dashboard bundles.
- P0 fallback: a daemon without `geo` still works. `api.getCatalogSnapshot()` assembles the
  retained catalog by reading every 500-row page and caches it until the daemon's `fetchedAt`
  changes, so the 30 s table poll does not re-download the catalog. The read starts only after that
  account's catalog response settles, concurrent callers share one in-flight pass (single-flight),
  and an invalidation epoch keeps a pass started before a reset or refresh from repopulating the
  cache.
- Vendor titles are the only geography key (`<city> #<n>`). `src/lib/geo.ts` resolves them with an
  exact match against the committed city names and aliases (`镇江` → 镇江市, `济南 - 莱芜` → 济南,
  `延吉` → 延边, `湘西` → 湘西州); there is deliberately no prefix rule, so `海南 #1` is
  unresolved instead of prefix-matching `海南藏族自治州` into Qinghai. Unresolved titles are
  counted in **Other** and are never assigned a province. `scripts/geo/build-cn-geo.mjs` derives
  the short-name aliases and drops any that would name a different province.
- Clicking a province filters the table to it and returns to page 1; clicking it again clears the
  filter. A city Select appears for the selected province. With the daemon aggregate the filter is
  a `province`/`city` query parameter; the fallback filters and slices the cached snapshot in the
  browser. Search, status, sort, pagination and the account picker work in both modes.
- The current `LIVE` node keeps a mint ring and a city dot even when a filter excludes its row; the
  header chip names it. The SVG is decorative (`aria-hidden`) and the province list under it is the
  keyboard/screen-reader control. Transitions are disabled under `prefers-reduced-motion`.
- Regenerate the browser assets with `node scripts/geo/build-cn-geo.mjs` (DataV GeoAtlas is
  contacted at build time only; `--raw <dir>` reuses a cache and `--check` fails on drift). The
  committed asset records the source URL and the sha256 of the national boundary file. Then
  regenerate the daemon's exact-match index from that asset:
  `node scripts/geo/gen-daemon-geo.mjs` (`--check` fails on drift).

### Node detail drawer

The Connected tab's drawer is a read-out of what the daemon reports plus what the operator has
actually observed; it never turns an absence of data into a measurement.

- **Localised region first.** The region fact renders `regionName()` in the active language
  (`cn-zj` → China · Zhejiang / 中国 · 浙江) and keeps the raw vendor code as secondary mono text.
  An unresolved code falls back to the node's `city, country` while still showing the raw code;
  `Unknown region` is only the last resort.
- **Unmeasured is not zero.** A success rate counts only when a probe recorded a sample, so
  `successRate: 0` (or an empty `lastProbeAt`) renders `Not measured` with the tooltip “No probe
  has measured this node yet.”, never `0.0%` — in the table and in the drawer.
- **Probe clock skew.** A probe stamp up to 5 minutes ahead of the browser clock reads `just now`
  and keeps the absolute time in its tooltip. Further ahead, the drawer renders the absolute
  date/time plus a `clock skew` badge instead of a future relative label; an empty or invalid
  stamp reads `Never`.
- **Egress is a vendor claim.** The egress IP carries a `Vendor claim` badge, a copy button and
  `not verified` until the operator runs a check; the badge tooltip says this panel has not
  observed the address.
- **Verify egress.** The guarded action is enabled only for a `LIVE` node (the disabled title
  says so). It calls `POST /v1/lab/egress-check` and then shows verified, claimed,
  match/mismatch, latency and checkedAt in an `Egress verification` region. A rejection (409/429)
  appears inline as an alert and as a toast; a mismatch keeps the inline observation without a
  success toast. Observations are keyed per node, so closing the drawer or switching nodes does
  not erase them.
- **Touch probe honesty.** **Touch probe timestamp** only moves `lastProbeAt`; the drawer then
  notes “Touch only — no latency or egress check.” and the previous measurement stays.

## Gate health panel

`src/components/nodes/HealthPanel.tsx` renders the daemon's loopback sampler
(`GET /v1/health/nodes`) in two variants: the full table above **Connected** on `/nodes`, and a
compact summary under the Overview metric tiles. Both poll every 15 s (one probe behind the
daemon's 30 s cadence), pause while the tab is hidden, and stop polling when the sampler reports
`enabled:false`.

- **Table.** Node, state, last sample latency (with a failure badge and the `errorClass` label),
  p50, p95, success rate, consecutive failures and sample count. Percentiles come from successful
  probes only, so a node with `ok:0` renders `—` instead of the daemon's zero placeholder; a node
  with `count:0` renders `no sample`.
- **States.** `ok`, `degraded` (success rate below 0.9 or p95 above 800 ms) and `down` (three
  consecutive failures or success rate below 0.5), mapped to the shared success/warn/unavailable
  pills; the card badge counts them.
- **Compact.** State counts plus the worst node, its success rate and a sparkline built from the
  page's own successive polls (two samples minimum), with a link to `/nodes`.
- **Egress.** When the daemon runs the through-gate sampler (`AJS_EGRESS_PROBE=1`), the full
  panel adds an **Egress through gate** card: one row per watched node with the egress state,
  p50, p95, success rate and the newest failure (`timeout`, `refused`, `unreachable`, `proxy`,
  `http` with its status, or `other`). A node with no sample yet reads `waiting`, never `ok`.
  `egress.enabled:false` shows **Egress sampling is off** with the daemon reason
  (`AJS_EGRESS_PROBE=0`) and no table; a daemon that omits `egress` entirely renders no card. The
  compact variant stays loopback-only.
- **Honesty.** A failed poll keeps the last good table with the stale marker; `enabled:false`
  shows the daemon's reason (`AJS_HEALTH_PROBE=0`) instead of an empty table; an empty sampler
  explains that no gate is being watched. The sampler keeps at most `AJS_HEALTH_MAX_NODES` node
  rings (default 16), so with more live gates than that the panel shows only the watched subset
  and the daemon re-arms an evicted watched gate on its next sweep. The probe dials the daemon
  host's loopback gate port, so the panel is a diagnostic for a running gate, never a
  vendor-reachability claim.

## Requests flows

`/requests` is the audit table over the daemon's gate flow ring, not the vendor's request log.

- **Source.** `GET /v1/lab/flows?limit=200` every 5 s; node labels come from `/v1/nodes` every
  15 s and the mutation-lab history from `/v1/requests` every 10 s. Rows from the two sources share
  one table and a `source` filter (`Gate flows` / `Mutation lab`).
- **Columns.** Time, account/node, user, method, protocol, target (`host:port` only), status
  (`ok` / `rejected` for gate flows; HTTP status for mutation rows), bytes in/out, duration,
  source and error class. The daemon records only host and port — never a URL, headers or a
  credential — and the SID is already redacted server-side. A row opens a detail drawer with the
  same fields, including the protocol badge and the attributed user.
- **Protocol.** A badge names the listener that admitted the flow: `socks5` in the info tone and
  `http` (HTTP CONNECT) in the neutral tone. An empty value renders `—`, so a row from a daemon
  that does not report the listener is never labelled as one of them.
- **User.** The cell shows the operator's name with the exact id as its `title` and falls back to
  the id alone; an unattributed flow (loopback gate mode, or a daemon that reports no
  attribution) renders `—`. `protocol`, `userId` and `userName` are optional on the wire:
  `/v1/requests` rows carry all three, while `/v1/lab/flows` carries them only once the daemon
  exposes them — the current flow view does not, so gate-flow rows show `—` and mutation-lab rows
  show the request-log value. The mock seeds both protocols and named operators, so the default
  build shows real badges and names.
- **Summary.** Five tiles (flows, accepted, rejected, p95 duration, bytes) computed over the
  filtered rows, plus the `{shown} of {total} rows · {dropped} dropped` hint from the bounded
  500-entry per-gate rings.
- **Filters.** Search (host, account, node, user, method, protocol, id), time range (15 m / 1 h /
  24 h / all), status, method, protocol, account/node, user, source and error class. The protocol
  filter offers `socks5` and `http`; the user filter is built from the rows and only appears once
  at least one row names an operator, so a loopback-only ring does not show a dead control. The
  source filter isolates gate flows from mutation-lab rows.
- **States.** An empty ring explains that no gate flow has finished yet; an `{enabled:false}`
  body renders the daemon's reason instead of an empty table; a failed poll keeps the last good
  rows with the stale marker (see [Polling and stale data](#polling-and-stale-data)).
- **Mutation lab.** The separate drawer still appends a synthetic row to `/v1/requests`; those rows
  are labelled `Mutation lab` and need a `LIVE` node to target.
- **Paging.** The shared footer pages the filtered rows (initial size 200, the whole gate ring;
  25/50/100 are also offered). The five summary tiles stay computed over every filtered row, not
  just the visible page, and the detail drawer works from any page.

## Pagination

Every long table shares one footer, `src/components/Pagination.tsx`, which exports the
`usePagination` hook and the `Pagination` component; `DataTable` renders no paging of its own, so a
page composes the two.

- **Footer.** `1–50 of 2,814`, first/previous/page-of/next/last, a page jump whose invalid input
  explains itself instead of failing silently, and a page-size Select (25/50/100/200). Labels come
  from `i18n.tsx`, so the footer switches language with the rest of the UI. Below 760 px the
  first/last buttons and the visible size label drop out, keeping the range and page controls on
  one line beside the card-based table.
- **State.** `usePagination({total, initialPageSize, resetKey})` returns the current page, size,
  page count, 1-based `start`/`end` and a `slice(rows)` helper. `resetKey` is the filter identity:
  a filter, search or sort change returns to page 1 during render (the old page's slice never
  flashes), while a live poll does not move the operator. A shrinking result clamps to the last
  page instead of stranding the table on an empty one.
- **Server-paged.** The Nodes Catalog and the Lab **Servers** tab never slice locally: the footer's
  page and size changes become `offset`/`limit` on `GET /v1/accounts/{id}/servers` (50 default,
  500 max), and the total comes from the response envelope. A shrinking catalog walks the offset
  back to the last page. When a province filter is active, Catalog pages are sliced from the cached
  snapshot instead (see [China map and province filter](#china-map-and-province-filter)).
- **Client-paged.** Requests (initial 200), Logs (25), and the Lab Logs and Gates & sessions tabs
  (25) filter their bounded in-memory rows and slice locally; the page survives a poll but resets
  when the filter identity changes. The Catalog's unfiltered view is server-paged as above.
- **Tests.** `tests/pagination.test.tsx` covers the footer range/size/jump, the filter reset, the
  compact layout and the page integrations; `tests/catalog-snapshot.test.ts` covers the paged
  snapshot read and its cache, and `tests/nodes-catalog.test.tsx` the server-offset window.

## Usage page

`/usage` is the aggregation view over the daemon's request log (`GET /v1/usage`), not a byte
counter: the daemon sums the same rows `/v1/requests` serves — with gate metering on
(`AJS_FLOW_METER`, default) every completed SOCKS5 flow appends one, alongside the mutation-lab
and seed rows — so the numbers reset with the request log's 10 000-row retention.
`src/pages/Usage.tsx` loads once per filter change — no polling — with an explicit
**Refresh**.

- **Window.** A segmented control selects 7, 30, 90 or 366 days; the client sends RFC 3339
  `from`/`to` bounds (the start is UTC midnight of the first day) and the daemon applies the limits
  (absent = trailing 30 days, > 366 days → 400). The baseline is the equal-length window of whole
  UTC days immediately before the current one: `from` is the current start minus N days and `to`
  is one millisecond before the current start, both RFC 3339. It sends an explicit end-of-day
  `to` on purpose — a date-only `to` would be read as midnight and drop the baseline's last day —
  and repeats the active account filter.
- **Retention.** The daemon reports `rowsRetained` (the request-log cap, 10 000 rows) and
  `retentionDays` (the inclusive UTC-day span the retained rows actually cover; `0` = no rows).
  When the cap is known the page shows *History is capped at the newest N requests (~D days)*
  above the chart, so a 90- or 366-day window never reads as full coverage; the note is omitted
  when the daemon reports no cap.
- **Filters.** An account filter is populated from the current summary and sent as `account` (the
  daemon resolves an id or a label); the API also accepts `userId`, which the page does not expose
  yet.
- **Summary.** Four tiles — requests, bytes, errors (with error rate) and average latency — each
  with the change against the immediately preceding window of the same length, computed from a
  second `GET /v1/usage`. A zero baseline yields no percentage rather than an infinite change, and
  a failed baseline request drops the deltas instead of failing the page.
- **Monthly account budgets.** `trafficBudgetBytes` on an account (PATCH
  `/v1/accounts/{id}`; `null` clears it, zero refuses every byte) is enforced at the gate: once
  the account's gate-metered bytes for the current UTC month reach the budget, new flows are
  refused with SOCKS5 `0x02` and a `quota_exceeded` flow record, and one `quota.exceeded` audit
  row is written per account per hour. When any visible account has a budget the daemon sends
  `quotaEnforced:true`, the user table labels its quota column **Quota** instead of **Budget
  (advisory)**, and the account table adds a monthly meter (`trafficBudgetBytes` against
  `usedBytes`) with the remaining headroom. `usedBytes` is the daemon's durable current-month
  counter, not a sum over the retained rows, so evicting old request rows never lowers the meter
  or renews a budget. The gate defaults to `AJS_GATE_AUTH=userpass` (RFC 1929, loopback bind), so
  a flow carries a panel user identity; user quotas are enforced at the gate, which refuses a flow
  at the user's monthly quota, and the metered row advances the same per-user total. Only the
  explicit `AJS_GATE_AUTH=loopback` opt-in makes user quotas advisory, because that wire carries no
  user identity.
- **Chart and tables.** A per-day bar/area chart fills missing days with zeros across the returned
  `from`–`to` window; the account and user tables are sortable and the user rows show the monthly
  quota meter (`quotaBytesMonth` against `usedBytes`, labelled advisory as above); a top-targets
  card lists the busiest `host:port` targets. An empty window renders an explicit empty state, and
  `truncated:true` shows a warning callout that the daemon clipped the aggregation.
- **Mock.** Deterministic fixtures aggregate the seeded request rows, so the page is fully
  exercisable offline.

## Schedules page

`/schedules` is the UI for the daemon's scheduler (`AJS_SCHEDULES=1`); it lists schedules, previews
the next runs and records manual runs. It polls `GET /v1/schedules` every 15 s and the account list
every 30 s.

- **Table.** Name (with action, owner and paused/degraded badges), trigger description
  (cron/interval/once in the active language, plus the optional window), next run, last result,
  runs today and the enabled switch. The four summary tiles count total, enabled, paused and the
  next run.
- **Create/edit.** `ScheduleDialog` builds the trigger (`cron` expression, `interval` seconds with
  jitter, or a `once` timestamp), an optional weekday/time window, the guard (`cooldownSec`,
  `maxRunsPerDay`, `dryRun`), the concurrency and misfire policies and the target account. A
  destructive action with `dryRun:false` asks for the `ARM` confirmation. The dialog shows the pure
  next-run preview (`GET /v1/schedules/preview`) before saving.
- **Edit safety.** PATCH sends `If-Match: "<id>:<revision>"`; on a stale revision (412) the page
  refetches the schedule and retries once with the new revision, so a concurrent edit surfaces as a
  retry rather than a lost update.
- **Actions.** The enabled switch sends `{enabled}`, **Run now** posts
  `/v1/schedules/{id}/actions/run` with `confirm:"RUN"` for a non-dry destructive action and
  reports the run result, the history drawer reads `/v1/schedules/{id}/runs`, and delete asks for
  confirmation. A 503 with the disabled engine renders a banner with the daemon's reason and
  disables every mutation.
- **Renewal sweep.** `RenewalSweepCard` surfaces the daemon's built-in daily expiry sweep as
  read-only state: the system schedule id, owner, action, trigger, enabled state and last run when
  the daemon lists that row, otherwise an explicit *state unavailable*, plus the reminder marks
  derived from account expiry inside the 30-day window. The page cannot trigger or edit the sweep
  and never invents a next run.

## Leases page

`/leases` lists the active leases and is where a lease is created or revoked. **New lease** opens
a modal with a searchable user picker (`GET /v1/users`), a platform picker
(android/ios/windows/linux, default android), an optional account pin limited to active accounts of
that platform, and a TTL choice (1 h, 8 h, 24 h, 7 d; default 8 h).

- **Idempotency.** Every dialog open generates a fresh key (`crypto.randomUUID()`, or a
  timestamp/random fallback when unavailable) and sends it as the `Idempotency-Key` header, so
  retrying a failed submit returns the original lease instead of allocating a second node. The page
  never renders the key; the daemon records it on the lease row.
- **Refusals.** The daemon's message stays in the dialog as an alert. The daemon's reasons are 409
  `lease_conflict` for a second active lease of the same user and platform, 409 `user_disabled`/
  `account_unavailable`, 429 `quota_exceeded`/`concurrency_exceeded`/`no_capacity`, and 404 for an
  unknown user or account. The page does not retry by itself and never fakes a lease.
- **Success and warning.** A committed lease reloads the list and shows `Lease created · user on
  node`; if the daemon reports a publish-hook `warning`, the lease still committed and the warning
  is shown as a danger toast.
- **Rotate and revoke.** Rotate is disabled while the Lab overview says Resin is not configured;
  revoke asks for confirmation and says the node is freed immediately.

## Device pool page

`/device-pool` is the console for the daemon's sealed identity pool. It polls
`GET /v1/device-pool` every 30 s and renders count chips (total, available, assigned, retired,
quarantined) that also act as a state filter, a platform filter, and a 250 ms-debounced search over
id, label, notes and account.

- **Hash-only table.** Columns are label/id, platform/profile, state, bound account, created and
  presented times, and the first two hashes truncated as `sha256:<8 hex>…` with a `+N more` note.
  The sealed machine code is never requested or rendered; a record whose material is gone carries a
  warning badge.
- **Lifecycle.** The row actions follow the state: assign (account picker), release, retire,
  quarantine and purge. Release/retire/quarantine/purge require the daemon's exact typed token
  (`RELEASE`, `RETIRE`, `QUARANTINE`, `PURGE`), retire and quarantine accept an optional reason,
  and a 409 (for example a live session) is localized from its `DEVICE_*` reason.
- **Generate, export, import.** Generate creates a batch; export seals the selected scope
  (`assigned` default, `retired` or `all`) with a passphrase and an `EXPORT` token, optionally
  including account keys and labels; import accepts a pasted or uploaded envelope, defaults to a
  dry run, and lists per-record conflicts when the daemon refuses an apply.
- **Disabled and warnings.** A pool that answers `enabled:false` (for example no `AJS_MASTER_KEY`)
  renders a warning callout with the daemon's reason instead of an empty table, and pool warnings
  are listed above the table. The detail drawer shows the full fingerprint, lifecycle timestamps and
  the sealed-material warning.

Mock mode seeds seven records (3 available, 2 assigned, 1 retired, 1 quarantined) so the page is
reviewable offline.

## HMC layout

The HMC build host serves three endpoints; the live pair is also published on the LAN:

| Endpoint | Role | How it runs |
|---|---|---|
| `127.0.0.1:18081` | mock dashboard | `docker compose up -d` in `dashboard/`; local demo image built with the explicit `AJS_ALLOW_MOCK_BUILD=true` opt-in, rebuilt after the live/mock copy fixes |
| `127.0.0.1:18082` · `http://192.168.11.4:18082` | live dashboard | `npx vite preview --host 0.0.0.0 --port 18082` over a `dist/` built with `VITE_USE_MOCK=false` and `VITE_API_BASE=http://192.168.11.4:2261`, **without** `VITE_PANEL_TOKEN` |
| `127.0.0.1:2261` · `http://192.168.11.4:2261` | `ajs-accountd` | `go build -o ajs-accountd ./cmd/ajs-accountd` then `./ajs-accountd` with `AJS_LISTEN=0.0.0.0:2261` and the LAN origin in `AJS_CORS_ORIGINS`; JSON snapshot store (`AJS_STATE_FILE`) |

The mock build stays loopback-only. The LAN pair is reachable from the `192.168.11.0/24`
segment: `AJS_ALLOW_INSECURE` is not set (and startup refuses it off loopback), so every `/v1/*`
call needs the bearer token and a token-less request answers 401. Because the LAN bundle bakes no
token, the runtime token gate asks for the admin token on the first 401 and keeps it in that
tab's `sessionStorage`; the API is plain HTTP, so the segment must be trusted. From Windows the
LAN URLs work directly, and the tunnels below still serve the loopback-only mock build:

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

Backend check with `curl -H "Authorization: Bearer $AJS_ADMIN_TOKEN"` confirmed the same state:
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

**Live store after the demo purge (2026-10-24):** the daemon now starts empty, and the HMC
snapshot was migrated once with `AJS_DROP_DEMO=1`
(`demo_purged accounts=6 nodes=12 leases=8 users=4 keys=6 requests=40 audit=20`). It holds exactly
1 account (`acc-07`, `active`) and 1 node (`acc-07:vvn-2076-264`, `LIVE`); the Accounts table
renders one row and Nodes' Catalog only that account's retained vendor servers — 2,814 of them, all
region `cn`, in one *China* group. The node's gate port is allocated from `127.0.0.1:13000` upward
and must be read from `GET /v1/lab/gates` (the purge deployment reported `13000`). The Lab egress
check answered 200 with `verified=222.186.57.63` against the vendor's claimed RFC1918 egress,
which varies per session (`claimed=10.98.206.1` on the current run, `10.78.248.1` on the purge
run), so it renders `match:false` (verified vs claimed).

## Verification (recorded 2026-10-23)

These records predate the fail-closed build guard (2026-10-27): the mock builds below ran as bare
`npm run build` then, while the demo recipe today is `npm run build:mock`.

Build and test were executed on the HMC build host, not on the Windows product tree:

```bash
# copy only dashboard/ source to the HMC scratch tree (node_modules stays on the host)
cd dashboard && tar -czf - --exclude=./node_modules --exclude=./dist --exclude=./dist-mock . \
  | ssh hmc 'mkdir -p /srv/dwgxhomecloud/scratch/loveplus-dashboard && tar -xzf - -C /srv/dwgxhomecloud/scratch/loveplus-dashboard'
ssh hmc 'cd /srv/dwgxhomecloud/scratch/loveplus-dashboard && npm ci && npm run build && npm test'
```

Both modes were built from the same source on HMC (Accounts view slice included):

| Build | Command | Result |
|---|---|---|
| Mock (default) | `npm run build -- --outDir dist-mock` | exit 0, `✓ built in 2.58s`, `dist-mock/assets/index-DmnOxSn9.js` |
| Live | `VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 npm run build` | exit 0, `ajs-build.json` with `mode: live`, `mock: false` |
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

## Settings sections

`/settings` is a shell with a sticky left rail and nine anchored sections. The rail is grouped
**Basics** (General, Appearance, Language), **Runtime** (Connection, Leases, Updates) and
**System** (Integrations, Access, Audit); each item is an `<a href="#section">` with
`aria-current="location"` on the active section. Under 900 px the same rail becomes a horizontal
scroll strip with a 44 px touch target and a mint underline. Section ids are deep-linkable
(`/settings#leases`) and the shell scrolls to the target once the settings request resolves.

| Section | Contents | Honesty state |
|---|---|---|
| General | daemon endpoint, mode, version, commit, stored transport and token; mock-only reset | read-only facts; no hardcoded version fallback |
| Appearance | dark/light theme, four accents | browser-local; the theme is also saved as the panel default |
| Language | `中文` / `English` | browser-local |
| Connection | transport and health interval | **Stored only** — controls disabled with a visible reason; `VersionProfile.Transport` wins and no canary scheduler exists |
| Leases | lease TTL, subscription refresh | TTL **Applied**; refresh **Stored only** (the daemon uses a fixed 30 s window) |
| Updates | existing read-only release check + deploy command | automatic update/rollback marked **Coming later** |
| Integrations | Resin and Prometheus rows, plus the live webhook registry | Resin/Prometheus read-only **Not configured**; webhooks are real: endpoint list, create with a one-time signing secret, enable/disable, edit-as-replace, delete confirm and a per-endpoint delivery ring |
| Access | scoped API keys: prefix, user, scope chips, rate limit, expiry, last use, revoked state | keys are real: create with user/scopes/account restrictions/expiry/rate limit, one-time secret, typed revoke confirm with 409 refresh |
| Audit | audit and request-log retention, source route, link to Logs | retention **Locked** by the daemon; CSV export lives on Logs |

The header carries the dirty count (`{count} unsaved`), **Reset changes**, and **Save changes**.
Dirtiness is per field (`theme`, `transport`, `healthIntervalSec`, `leaseTtl`,
`subscriptionRefreshSec`), dirty sections get a warning dot in the rail, each editable section has
its own **Reset section** action, and a dirty form installs a `beforeunload` guard. The settings
form itself makes two requests on load (`GET /v1/settings` and `GET /v1/lab/overview`) and does not
poll; the webhook registry and the scoped-key section add their own reads (endpoints plus
deliveries, keys plus the user and account pickers).

The section registry is `src/lib/settingsSections.ts` (id, group, label/description keys, dirty
fields, deep-link ids). `src/components/settings/` holds the rail, section shell, field row,
provenance badge and the disabled-capable segmented control; shell CSS lives in
`src/components/settings/settings.css` so `app.css` stays untouched.

## Update check (Settings → Updates)

The Settings page carries a read-only **Updates** card. It shows the running version and a
**Check** button — nothing is fetched on page load; the daemon is contacted only when the operator
clicks **Check** (`GET /v1/system/update`, one request, no polling).

| State | What the card shows |
|---|---|
| Before a check | current version + **Check** button |
| `enabled: false` | explanation that `AJS_UPDATE_REPO` is not configured on the daemon |
| `updateAvailable: true` | **Update available** badge, latest tag, published date, release-notes link, and a **Copy** button for the exact deploy command |
| `updateAvailable: false` | **Up to date** badge, latest tag, published date, release-notes link, deploy command |
| Request failure | an inline alert with the error; the previous result (if any) is not cleared |

The deploy command is exactly:

```bash
cd deploy && ./update.sh --tag <latest>
```

The dashboard never executes an update — `update.sh` is the host's job. Mock mode returns
`{enabled: false, current: "0.1.0"}`, so the mock build renders the disabled state; tests mock
`api.getUpdate` for the other states.

## Webhooks (Settings → Integrations)

The Integrations section hosts a live webhook registry; it replaces the earlier **Coming later**
placeholder. The component is `src/components/settings/WebhooksSection.tsx`.

- **Load.** `GET /v1/webhooks` plus one `GET /v1/webhooks/{id}/deliveries` per endpoint on mount
  and on **Refresh** — no polling. The list still renders when a deliveries request fails
  (`deliveriesKnown:false` shows *delivery status unknown*, not an empty ring).
- **Create.** The dialog takes URL, event subscription (none = every event), description and an
  initial enabled flag. `POST /v1/webhooks` returns the signing secret exactly once; the show-once
  panel renders it with a **Copy** button and it is never fetched again. The URL must be absolute
  `https` unless it is loopback (the daemon's `AJS_WEBHOOK_ALLOW_HTTP` opens plain HTTP too), must
  not resolve to a loopback, RFC 1918, link-local or unique-local address unless the daemon runs
  with `AJS_WEBHOOK_ALLOW_PRIVATE=1` (an unresolvable name is allowed at registration and
  re-checked at delivery), and may not embed credentials; the dialog rejects those before the
  request. The daemon repeats the address check at dial time (a DNS rebind between registration
  and delivery is refused) and always rejects the cloud metadata host names.
- **Rows.** URL, subscription, enabled switch, last delivery (status, HTTP status, attempt, time)
  and the edit/deliveries/delete actions. `PATCH /v1/webhooks/{id}` sends only `{enabled}`. A row
  whose `secretAvailable` is false is flagged *secret unavailable* and cannot be re-enabled (the
  daemon answers 409) — recreate the endpoint instead.
- **Edit is a replace.** The daemon only mutates `enabled`, so a changed URL, label or event list
  is a new receiver: the page creates it first (showing the new signing secret once), then deletes
  the old row, so a failed create leaves the original endpoint intact.
- **Deliveries.** The drawer requests `?limit=50` and lists the endpoint's attempts newest first
  with event, attempt number, status (`success`/`failed`/`dead_letter`), HTTP status or transport
  error, and duration. The daemon merges its persisted history (50 rows per endpoint, 500 total,
  in `state.json`) with the live 200-attempt ring and dedupes by delivery id, so the newest rows
  survive a daemon restart; the default page is 200 and a larger limit is clamped to 500.
  The dashboard never invents an attempt.
- **Events.** The subscription list is the daemon's closed catalogue: `account.login`,
  `account.logout`, `account.expired`, `account.renewal_due`, `lease.created`,
  `lease.revoked`, `lease.expired`, `node.connected`, `node.disconnected`,
  `node.switched`, `schedule.fired`, `schedule.failed`, `device.retired`,
  `audit.critical`. All fourteen are emitted: the account actions produce login/logout,
  the lease create/revoke handlers and the expiry sweep produce the three `lease.*`
  events, `node.*` comes from the node controller, `schedule.fired`/`schedule.failed`
  follow a recorded run, `account.renewal_due`/`account.expired` come from the
  once-per-local-day renewal sweep, `device.retired` follows a retire, and
  `audit.critical` is emitted for every critical audit row (the webhook's own
  `webhook.*` audit rows are skipped so a delivery cannot re-emit itself).
- **Signature.** Every delivery carries `X-APC-Event`, `X-APC-Delivery`, `X-APC-Timestamp` and
  `X-APC-Signature`. The receiver-side recipe — exact formula, raw-body rule, 5-minute timestamp
  tolerance and Node/Go snippets — is in
  [`daemon/README.md`](../daemon/README.md#verifying-webhook-signatures).
- **Mock.** The mock store seeds one endpoint (`https://hooks.example.com/ajs`) and three
  deliveries so the panel is exercisable offline; create, toggle and delete mutate in memory only.

## Tests and QA

- 480 Vitest tests in 54 files (65 at the 2026-10-23 record; see
  [Verification](#verification-recorded-2026-10-23)).
  `tests/i18n.test.tsx` covers the bilingual UI: dictionary parity and non-empty strings,
  interpolation plus English/key fallback, switching to Chinese with `document.lang`, restoring a
  stored choice, the `zh*` browser default, the Settings language card, and palette page search in
  both languages.
  `tests/useAsync.test.tsx` drives polling with fake timers (interval, hidden pause, resume, unmount,
  dedupe, failed poll, reload, dependency change, `enabled: false`) and checks the stale dot plus the
  in-flight activity flag behind the topbar progress line.
  `tests/operations.test.tsx` covers the history columns and Chinese copy, the 2 s poll that stops
  once every record is terminal, and the select-node form following a 202 record to its terminal
  result. `tests/event-stream.test.ts` drives the SSE client with a fake `EventSource`: reconnect
  from the last event id, doubling backoff, cursor reset after repeated failures and the offline
  path; `tests/events-feed.test.tsx` covers the visible connecting/live/reconnecting/paused states,
  the 100-row cap and dedupe.
  `tests/accounts-view.test.tsx` covers the table default, switching both ways, the
  `ajs.accounts.view` preference across remounts, an invalid stored value, card anatomy, card
  selection plus the shared test-login action, the mobile grid and its CSS contract.
  `tests/live-mode.test.tsx` stubs `VITE_USE_MOCK=false` before importing the app, then asserts the
  live shell (`live API`) and the absence of the reset card.
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
  `tests/update-card.test.tsx` covers the Settings **Updates** card: no fetch on page load
  (check runs only on **Check**), the disabled state explaining `AJS_UPDATE_REPO`, the
  update-available state with tag/date, copying the exact deploy command
  (`cd deploy && ./update.sh --tag v0.2.0`), the release-notes link target, the up-to-date
  badge and the error state (alert, no badge, cleared by a later successful check).
  The platform wave adds five files. `tests/usage.test.tsx` drives the 7/30/90/366 window, the
  account filter, the baseline request with its explicit end-of-day bound, the summary tiles with
  previous-window deltas, the zero-filled chart, the three retention-note variants plus the
  no-cap case, the advisory→enforced quota label and the enforced per-account monthly meters, and
  the empty and `truncated` states.
  `tests/schedules.test.tsx` covers the table, the create/edit
  dialog and its pure preview, `ARM`/`RUN` confirmation, the stale-`If-Match` retry, run history
  and delete. `tests/health-panel.test.tsx` covers `ok`/`degraded`/`down`, the compact and full
  variants, `enabled:false`, an empty sampler and the stale poll. `tests/webhooks.test.tsx` covers
  the registry, the show-once secret, the enable-409 when the secret is gone, delete and the
  deliveries drawer (newest-first rows, the 50-row page and the last-delivery read from index 0).
  `tests/platform-client.test.ts` pins the client contract for renewal, usage,
  health and webhooks (live URL/query/method/bearer header and deterministic mock data).
  `tests/portal.test.tsx` covers the customer portal: the client sends `credentials:"include"`
  with the `X-CSRF-Token` header on unsafe calls and never a bearer, maps 401/429 without
  persisting the token, keeps leases/usage/keys scoped to the signed-in tenant in mock parity,
  issues a show-once secret and revokes only owned keys, refuses pending and suspended logins,
  keeps signup pending-only, signs in and writes nothing to Web Storage, rejects bad credentials
  with one message, validates the signup form before calling the daemon, redirects a visitor
  without a session, renders own leases, usage tiles and the zero-filled chart, and renders the
  portal in Chinese.
  `tests/orders.test.tsx` covers the order wave end to end. The portal block lists only active
  plans with price, term and quota, creates an order after the confirmation that says no online
  payment is taken, replays a per-dialog `Idempotency-Key` with `X-CSRF-Token` and no bearer,
  turns a changed plan or a second open order into the daemon's 409 copy, renders awaiting, paid,
  active, refunded and review-required states honestly, cancels an awaiting order, answers a
  foreign cancel without leaking its existence, shows a service-interrupted order with its
  localized ended-lease reason, restores service through the reissue action, re-issues an ended
  same-plan order on create without a second charge, and renders the action in Chinese. The admin
  block filters every tenant's orders by
  state and review flag, requires a receipt reference before attesting an offline payment, retries
  activation, records an offline refund, cancels an awaiting order, deactivates and reactivates a
  plan while existing snapshots keep their price, hides a deactivated plan from new orders, and
  renders both order surfaces in Chinese.
  `tests/lab.test.tsx` covers the Lab page: overview posture badges and effective flags (a secret
  value never reaches the DOM), gate counters plus redacted session SIDs, the `enabled:false`
  bodies for gates/sessions and egress, servers/protocol/device tabs, log pause/resume and
  diagnostics. Its Egress block checks the selected live node, renders match/mismatch and a
  probe error with `verified=""`/`match=null`, disables the action when no gate is running and
  surfaces a 409 without writing a result into the row. Its `honesty fixes` block also pins the
  real gate-flow Requests label, the probe relabel, rotation disabled without Resin, and the
  logout/pause copy.
  `tests/nodes-catalog.test.tsx` covers the Catalog tab: grouping the selected account's catalog
  by region with the live gate marked `Current`, server-side offset paging with the total and
  region summary, q/region/status/sort query params, account scoping, rendering only the rows
  `getAccountServers` returned, local multi-select with count and clear, the honest empty state,
  the disabled reason, and the runtime actions on Connected.
  `tests/select.test.tsx` covers the custom combobox: click-open in a body portal,
  combobox/listbox/option ARIA with `aria-activedescendant`, Arrow/Home/End/Enter/Escape and Tab,
  typeahead, searchable filtering plus the no-match status and clear button, Enter from the search
  input, multi-select count/select-all/clear/done with `aria-multiselectable`, group headers,
  outside-click and disabled handling, viewport flip/clamp, and the motion-token plus
  reduced-motion CSS contract.
  `tests/nodes-drawer.test.tsx` covers the Connected drawer: the localized region with the vendor
  code secondary (and the city/country fallback), `Not measured` instead of `0.0%`, `just now`
  versus a far-future `clock skew`, the vendor-claim egress badge and copy button, Verify egress
  showing claimed/verified/mismatch/latency, the disabled-unless-LIVE guard, a 409/429 inline
  alert plus toast, the touch-only note, and the Chinese drawer.
  `tests/nodes-connected.test.tsx` covers the Connected state filter: `STALE` sits between
  draining and down, filtering keeps only the demoted row (no gate, no egress), and the option is
  localized.
  `tests/regions.test.ts` pins `regionName()`: ISO country codes and city subdivisions resolve to
  English and Chinese names, case is normalised, `unknown` and empty codes become `Unknown
  region`, and any other unknown code passes through unchanged.
  `tests/overview-telemetry.test.tsx` pins the no-telemetry empty state: with
  `telemetryAvailable:false` the Overview shows `No gate telemetry yet` with the explanation and
  `not measured`, draws no chart, sparkline or zeroed traffic KPI, keeps the live-node/lease
  counts and account health, and restores the chart and traffic tiles when the daemon reports a
  real meter.
  `tests/requests.test.tsx` covers the gate-flow table: real rows plus the animated summary,
  status/source and host-search/time-range filters with clear-all, the detail drawer (id,
  redacted SID, host-only target), the empty-ring explanation, the disabled reason, error with
  retry, and that no full URL or credential reaches the table.
  `tests/requests-protocol.test.tsx` covers the protocol and attribution columns: the mock ring
  reports both `socks5` and `http` flows and named operators, the table renders a `Protocol`
  badge and a `User` cell, the protocol filter narrows to one listener and restores, the user
  filter narrows to one operator, a bare flow from an older daemon (no `protocol`, no
  `userId`/`userName`) renders `—` and hides the user filter instead of inventing a value, an id
  without a name still renders and filters, the detail drawer repeats protocol and user, and the
  Chinese UI labels both columns and filters.
  `tests/ui-quickfix.test.tsx` pins the shell quick fixes: the 248→56 px sidebar collapse with
  `localStorage["ajs.sidebar"]` persistence, the APC branding (no workspace switcher or operator
  avatar), and the centered add-account modal with concurrency fixed at one and the vendor note.
  `tests/token-gate.test.tsx` covers the runtime token gate: a live 401 opens **Panel token
  required**, submitting stores `ajs.panelToken` in `sessionStorage` and retries with the bearer
  header.
  `tests/catalog-connect.test.tsx` covers the guarded switch UI: target vs current as separate
  row states, setting and clearing a target, the connect confirm, disconnect and reconnect
  actions, `alreadyConnected`, the 409 ineligible reason, writes disabled on a stale snapshot,
  the cooldown 429 with `Retry-After`, snapshot-id forwarding, and the one-session copy.
  `tests/account-io.test.tsx` covers export/import and bulk delete: encrypted export requires a
  passphrase, plaintext export requires the capabilities flag plus the `EXPORT` confirmation,
  import dry-run renders the plan without applying, atomic/partial apply results, row warnings,
  the single bulk endpoint call, the typed `DELETE` confirmation above ten and not-found rows in
  the result panel.
  `tests/renewal-card.test.tsx` pins the renewal drawer: the countdown (days, warning inside five
  days, overdue and `-0` normalisation), the public plan catalogue and save amounts, the official
  purchase link carrying no account or token, and that no payment request is made.
  `tests/icon-morph.test.tsx` covers the morph core and driver: path normalisation and
  resampling, Procrustes correspondence, spring settling, the instant swap under
  `prefers-reduced-motion`, and the rendered `d` at rest.
  `tests/china-map.test.tsx` covers the map end to end: 31 provinces, the 2,814 total, the
  current-node marker, province and city filtering, the Other bucket for unresolved titles, the
  daemon `geo` aggregate path with server-side province/city filters, reduced motion and both
  languages; `tests/catalog-geo.test.ts` checks the choropleth fill contrast.
  `tests/openapi.test.ts` validates `docs/openapi.json`: it parses, every `$ref` resolves, every
  `operationId` is unique, each operation declares `x-implementation-phase`, and every route
  registered in `daemon/internal/api/server.go` appears in the contract.
- Route sweep on the mock build (2026-10-23, before the Lab page): all 8 routes at 1440×900 and
  390×844.

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

## Customer portal

`/portal/*` is a second, customer-facing surface in the same bundle, served without the operator
shell, the admin navigation or the panel token gate (`src/pages/portal/`, `src/lib/portal-api.ts`).
It is the console's half of the daemon's tenant chain: a live build needs `AJS_TENANT_PORTAL=1` plus
`AJS_TENANT_ORIGIN` on the daemon, and without the flag every `/v1/portal/*` call is a 404 and each
page shows its failure state. Mock mode is always exercisable and seeds one active tenant (linked
to `u-01`), one pending applicant and one suspended tenant.

- **Routes.** `/portal/login`, `/portal/signup`, `/portal/pending`, `/portal/activate` and `/portal` (tabs **Leases**,
  **Usage**, **Keys**, **Plans**, **Orders**, **Activity**); any other `/portal/*` path redirects
  to `/portal`. A visitor without a session (401) is redirected to the login page; a 403 renders
  the suspended/not-allowed message instead of an empty panel.
- **Session.** The client sends `credentials:"include"` and never a bearer token, and it writes no
  token to `localStorage` or `sessionStorage`. The cookie is the daemon's `__Host-ajs_portal`
  (`Secure; HttpOnly; SameSite=Strict; Path=/`); the CSRF token from login (or from
  `GET /v1/portal/session` after a reload) lives in memory for the tab and is replayed as
  `X-CSRF-Token` on every unsafe call (logout, key issue, key revoke).
- **Signup and approval.** Signup posts email, display name and a password, then shows the pending
  page — it creates no user, lease or key, and a duplicate address gets the same 202. The form's
  local minimum is 10 characters while the daemon requires 12, so a 10–11 character password
  passes the form and is refused by the server. The daemon answers a pending or suspended tenant
  with a 403 and no cookie (bad credentials are a uniform 401), and the UI shows one shared
  inactive-tenant message. Approval, suspension and resume
  stay in the operator API (`/v1/admin/tenants*`), never in the portal.
- **Own data.** Leases show the linked user's rows only (platform, node, gate, status, expiry,
  requests, bytes); Usage offers 7/30/90-day windows with tiles and a zero-filled daily chart over
  the tenant's rows; Keys lists metadata only, issues a key with a show-once secret and revokes
  after a confirmation. Plans lists active plans only and creates an order under a per-dialog
  idempotency key; Orders lists only the tenant's own orders with an always-visible
  payment-status banner, a review-required banner, a **Pay now** button for an awaiting order
  (opens the returned 易支付 `payUrl` in a new tab) and a clear **Offline settlement** state when
  the adapter answers 503, plus a cancel confirm. Opening checkout never marks an order paid.
  An active order whose grant lease ended (for example after a daemon restart) shows a
  **Service interrupted** badge, the localized reason projected from the daemon's durable
  `leaseEnds` log, and a **Restore service** button that calls
  `POST /v1/portal/orders/{id}/reissue`; a successful restore keeps the original order and paid
  expiry and clears the interruption. The daemon's per-IP, per-address and per-tenant buckets
  answer 429 with `Retry-After`, which the page surfaces as a wait-in-seconds message.
- **Copy.** Errors are stable portal strings rather than daemon messages: one credential refusal,
  one rate-limit line, one unavailable line and one generic failure, in both languages.

## Orders and plans

`/orders` is the operator settlement surface over `/v1/admin/orders` and `/v1/admin/plans`; the
portal's **Plans** and **Orders** tabs are its customer half (`/v1/portal/plans`,
`/v1/portal/orders`). The daemon owns the state machine, and the console never invents a payment:
every manual dialog records something that already happened offline, while **Pay now** only opens
the provider's hosted checkout and never marks an order paid.

- **States.** `awaiting_payment → paid → active`; `awaiting_payment → cancelled`;
  `paid|active → refunded|charged_back`. `paid` means the money was recorded and activation is
  pending (the badge reads *Paid — activation pending*). `reviewRequired` outranks the stored state
  in the badge and in the filter, and any reversal the daemon cannot apply cleanly is flagged for
  review instead of guessed.
- **Plans.** The admin **Plans** card lists every plan — inactive included — with price, lease term
  and monthly quota; the switch is a confirm that deactivates or reactivates it. Deactivation only
  blocks new orders: existing orders keep their immutable priced snapshot, which the confirm says.
  The portal lists active plans only and says checkout is available when the operator enables
  易支付, otherwise an administrator records offline settlement.
- **Order creation (portal).** **Create order** opens a summary confirm that states the request
  takes no online payment; one idempotency key is generated per open dialog, so a retry replays the
  order instead of duplicating it. A 409 (changed plan or an existing open order) is shown as the
  open-order explanation.
- **Manual settlement (admin).** **Mark offline payment received** requires a receipt reference and
  records funds already received offline — it does not charge a card or move money. A `paid` order
  offers **Retry activation**; the daemon's entitlement grant is keyed by order id, so a retry or
  a restart grants one lease and the plan's monthly quota once.
- **Refunds and chargebacks.** **Record offline refund** requires a reference, records a refund
  already completed offline, revokes the grant immediately and keeps the payment history. The
  daemon records a chargeback from external evidence and always flags review. Neither control
  transfers money; a partial or unsupported reversal is left for review without mutation.
- **易支付 (epay) checkout.** The portal **Orders** tab shows **Pay now** for an awaiting order;
  `POST /v1/portal/orders/{id}/checkout` returns a hosted `payUrl`, which the page opens in a new
  tab. A checkout records provider intent only — the order stays `awaiting_payment` until a
  verified notify or a 60-second reconciliation query settles it. A 503 replaces the button with
  **Offline settlement**. The daemon enables epay only when `AJS_EPAY_GATEWAY`, `AJS_EPAY_PID` and
  `AJS_EPAY_KEY` are all set (`AJS_EPAY_SIGN_TYPE` must be MD5; `AJS_EPAY_NOTIFY_URL` and
  `AJS_EPAY_RETURN_URL` default to the tenant origin). The admin order table adds a provider badge
  (`manual` / `epay` / `stripe`) and an epay enabled/disabled chip from the daemon status field
  when present.
- **Stripe-compatible stub.** `POST /v1/payments/stripe` is the Stripe-compatible provider
  route; it verifies a signature over the raw body and stays disabled (503) until
  `AJS_STRIPE_WEBHOOK_SECRET` is configured. The console has no control for it and never shows a
  card form.
- **Mock.** Mock mode seeds plans and orders across `paid`, `active`, `cancelled` (one flagged for
  review), `refunded` and `charged_back`, applies the same state rules in memory, and adds an
  `awaiting_payment` row when an order is created.

## Routes

| Route | Page | Primary surfaces |
|---|---|---|
| `/` | Overview | 4 metric tiles, ingress/egress chart, account health, activity, quick actions; with `telemetryAvailable:false` the chart and traffic KPIs are replaced by a no-telemetry empty state (real counts and account health stay); a compact gate-health panel sits under the metric tiles |
| `/usage` | Usage | 7/30/90/366-day window, account filter, summary tiles with previous-window deltas over the equal-length preceding window, per-day chart, per-account monthly budget meters and per-user quota meters, top targets, a `rowsRetained`/`retentionDays` history note, and an explicit `truncated` callout when the request-log window is clipped |
| `/accounts` | Accounts | filterable table or card grid (view switch), centered add-account modal (concurrency fixed at 1), 同时连接数 (`useCount`) metric with multi-slot note, login test, pause/resume, delete confirm, bulk select, import/export, renewal countdown and drawer |
| `/nodes` | Nodes | **Catalog** (real vendor servers grouped by region, server-side paging/filter/sort, offline China map from the daemon `geo` aggregate, target/connect/reconnect/disconnect with confirm, local multi-select, current-node marker) and **Connected** (region/state filters including `STALE`, latency sparklines, success/capacity meters, drain toggle, detail drawer with localized region, `Not measured` success, clock-skew stamp, vendor-claim egress and **Verify egress**) |
| `/operations` | Operations | durable select-node history (state, account, target, started/finished, duration, error code) with a state filter and shared pagination; select-node form (account, retained-catalog server, `ifCurrent`, timeout) that follows the 202 operation to its terminal state; polls only while a record is queued or running |
| `/leases` | Leases | sticky-lease callout, copy proxy URL, create dialog (user, platform, optional account pin, TTL, per-dialog idempotency key), rotate, release confirm, TTL |
| `/orders` | Orders | plan card (price, lease term, monthly quota, activate/deactivate confirm), order table filtered by state or review flag with payment-provider column and epay status chip, offline-receipt attestation, activation retry, offline refund and cancel confirms, grant expiry; every dialog states that no online payment is taken |
| `/device-pool` | Device pool | count chips (total/available/assigned/retired/quarantined), state and platform filters, debounced search, hash-only table, generate/export/import dialogs, typed-token release/retire/quarantine/purge, assign dialog, detail drawer with sealed-material warnings |
| `/users` | Users | user cards, quota meters, API keys, show-once secret, revoke, allowlist editor (the daemon's gate enforces it, with the monthly quota, only under `AJS_GATE_AUTH=userpass`) |
| `/tenants` | Tenants | customer portal directory, status filter, approve/suspend/resume, and **Invite tenant**: a one-time activation code shown exactly once with its `/portal/activate?code=…` link; invitation history shows status only |
| `/requests` | Requests | gate-flow table with search/time/status/method/protocol/account-node/user/source filters, user and protocol columns, shared pagination footer, row detail drawer, mutation lab drawer |
| `/logs` | Logs | severity/actor filters, paginated audit table, CSV export of `/v1/audit/export.csv` with the active severity filter and an `X-Truncated` warning |
| `/schedules` | Schedules | schedule table (trigger, target, action, last result, next run), create/edit dialog with a pure next-run preview, run-now with `RUN` confirm for destructive actions, pause/resume, delete confirm, run-history drawer, renewal-sweep card |
| `/lab` | Lab | tabs: Overview, Gates & sessions (paginated), Egress (guarded check), Servers (server-paged), Protocol, Device, Logs (paginated), Diagnostics; environment badges; account picker for Servers/Device |
| `/settings` | Settings | nine-section shell with sticky rail: General, Appearance, Language, Connection, Leases, Updates, Integrations, Access, Audit; dirty save/reset, honest **Stored only**/**Coming later** states; Integrations carries the live webhook registry and Access the scoped-key manager |
| `/backup` | Backup | create a passphrase-sealed `.ajsb` envelope and download it, inspect a pasted/uploaded envelope header plus manifest, and dry-run a restore diff; apply stays disabled with the `AJS_BACKUP_ALLOW_RESTORE`/no-LIVE-node explanation |
| `/portal/login` | Portal sign in | email/password against `/v1/portal/login`, one uniform credential message, pending/suspended refusal, rate-limit copy with `Retry-After`, link to signup |
| `/portal/signup` | Portal signup | name, email, password + confirmation with local validation, then the pending page; no session is issued |
| `/portal/pending` | Portal pending | the application-received state and the operator-approval explanation |
| `/portal/activate` | Portal activation | one-time invite activation: previews `?code=`, validates the password locally, sets it, and reports the pending-approval state; used/expired/wrong codes get one stable refusal |
| `/portal` | Portal dashboard | standalone customer shell: **Leases**, **Usage** (7/30/90 days, zero-filled chart), **Keys** (issue with show-once secret, revoke confirm), **Plans** (active plans, create order under an idempotency key), **Orders** (own orders, payment-status banner, **Pay now** → 易支付 `payUrl` in a new tab, **Offline settlement** when epay is disabled, cancel confirm) and **Activity** (own audit rows, cursor load-more); redirects to login without a session |

`*` redirects to `/`.

## Layout

```text
src/
  App.tsx                 lazy routes: 15 operator pages under the admin shell + 5 portal routes
  main.tsx                entry, self-hosted Inter / JetBrains Mono
  styles/app.css          tokens, shell, components, responsive, light theme, accents
  lib/
    types.ts              Account, Node, Lease, PanelUser, ApiKey, RequestLog, AuditEvent, Metrics, Lab*, Schedule*, Usage*, Health*, Webhook*, Plan, Order, EntitlementGrant
    mock.ts               6 accounts · 12 nodes · 8 leases · 4 users · 40 requests · 20 audit rows · 7 device-pool records
    order-mock.ts         deterministic plans and orders (paid/active/cancelled/refunded/charged_back) + state rules
    lab.ts                mock Lab fixtures: gates, sessions, egress, profiles, logs, diagnostics
    api.ts                typed client; mock by default, fetch when VITE_USE_MOCK=false; admin orders/plans
    portal-api.ts         tenant client: cookie session, in-memory CSRF, mock tenants, plans/orders, PortalError
    nodes.ts              catalog query state + region grouping + client-side geo filters
    geo.ts                offline province/city resolution and map aggregation
    format.ts             bytes, dates, TTL, proxy URL helpers
    i18n.tsx              EN/ZH dictionaries, translate(), provider, ajs.lang persistence
    settingsSections.ts   nine-section registry: groups, dirty fields, deep-link ids
    schedules.ts          pure cron/interval/window mirror for the mock and dialog checks
    iconMorph.ts          morph core: path parsing, resampling, Procrustes, polar interpolation, spring
    labels.ts             account/node/severity tone maps
    nav.tsx               navigation + command registry metadata
    theme.tsx             theme + accent provider, localStorage persistence
    useAsync.ts           load/poll/dedupe/stale state, global refresh event
    useFocusTrap.ts       Esc + Tab trap for palette/drawer/modal
  components/
    AppShell, Sidebar, Topbar, CommandPalette, Toast, CopyButton, TokenGate
    DataTable, Pagination, Overlay (Drawer/Modal/ConfirmDialog), primitives
    Select.tsx            custom combobox: variants, search, multi, groups, body portal
    icons/                APC brand, nav, status and shell glyphs (inline SVG) + MorphIcon/morphGlyphs
    accounts/             export/import dialogs, renewal drawer card, accounts-io.css
    settings/             SettingsNav rail, section shell, field row, provenance badge, WebhooksSection, settings.css
    nodes/                CatalogTab (grouped vendor catalog), ChinaMap (offline choropleth), ConnectedTab (runtime nodes), HealthPanel
    schedules/            ScheduleDialog, ScheduleHistoryDrawer, RenewalSweepCard, schedules.css
    devicepool/           GenerateDeviceDialog, DeviceExportDialog, DeviceImportDialog, LifecycleDialog, DeviceDetailDrawer, shared.ts, device-pool.css
    usage/                UsageChart
    orders/               OrderState badge/money/term formatters
    lab/                  Overview, Sessions, Servers, Protocol, Device, Logs, Diagnostics tabs
  pages/                  Overview, Usage, Accounts, Nodes, Operations, Leases, Orders, DevicePool, Users, Requests, Logs, Schedules, Lab, Settings, Backup
  pages/portal/           PortalLogin, PortalSignup, PortalPending, PortalDashboard, PortalLeases, PortalUsage, PortalKeys, PortalPlans, PortalOrders, PortalFrame
tests/
  setup.ts                jsdom polyfills (ResizeObserver, clipboard, matchMedia)
  a11y-mobile.test.tsx    icon-only names, skip link, scroll regions, 390 px row cards, touch targets
  lab.test.tsx            Lab tabs, redaction, disabled telemetry, honesty fixes
  i18n.test.tsx           dictionary parity, zh/en switching, browser default, ajs.lang, document.lang
  token-gate.test.tsx     401 → runtime token dialog → sessionStorage token + retry
  update-card.test.tsx    Settings Updates card states and deploy command
  settings-sections.test.tsx  nine-section rail, active state, stored-only controls, save/reset, zh rail
  access-backup.test.tsx  scoped-key create/secret/revoke, audit CSV, backup inspect/dry-run, live bearer header
  events-feed.test.tsx    replay + cursor, newest-first append, 100-row cap, hidden pause, mock stream, zh
  accounts-view.test.tsx  Accounts table/cards switch, persistence, card actions, mobile grid
  account-io.test.tsx     export/import dialogs, plaintext gate, bulk delete, mock IO contract
  renewal-card.test.tsx   countdown, public price catalogue, official link, no payment call
  catalog-connect.test.tsx target/session contract, confirm, 409/429, snapshot id, zh copy
  catalog-geo.test.ts     choropleth fill contrast
  icon-morph.test.tsx     morph planning, spring, reduced motion, resting paths
  openapi.test.ts         docs/openapi.json parses, refs, operation ids, route-table parity
  app.test.tsx            all routes, palette open/navigate/Esc/filter
  interactions.test.tsx   copy toast, rotate, release, create lease, create account, bulk pause, key show-once, mutation lab
  leases-create.test.tsx  create dialog, per-dialog idempotency key, duplicate user+platform refusal, TTL/platform/account picker
  device-pool.test.tsx    hash-only list, count chips, filters, generate, assign, typed-token lifecycle, export/import, disabled pool
  nodes-catalog.test.tsx  catalog grouping, filters, selection, stale/failed/disabled callouts
  nodes-connected.test.tsx Connected state filter, STALE ordering, localized option
  china-map.test.tsx      map totals, province selection, city filter, Other, reduced motion, i18n
  geo.test.ts             offline title→province resolution and map aggregation
  catalog-snapshot.test.ts  full-catalog paging and snapshot cache
  nodes-drawer.test.tsx   drawer region, not-measured, clock skew, egress claim/verify, zh copy
  select.test.tsx         combobox ARIA/keys/search/multi/groups/portal/flip/motion
  requests.test.tsx       gate-flow rows, filters, detail, empty/error, host-only targets
  requests-protocol.test.tsx protocol/user columns and filters, bare-flow fallback
  pagination.test.tsx     footer range/size/jump, filter reset, compact layout, page integrations
  overview-telemetry.test.tsx no-telemetry empty state, no synthetic chart or zeroed KPI
  regions.test.ts          regionName(): ISO/city names, case, unknown fallback
  ui-quickfix.test.tsx    APC brand, sidebar collapse, add-account modal, Requests honesty fixes
  api.test.ts             seed counts, metrics, CRUD, reset, settings persistence
  live-mode.test.tsx      live shell labels, no reset card when VITE_USE_MOCK=false
  palette-focus.test.tsx  command palette focus trap and focus restoration
  palette-search.test.tsx palette Account/Node search, ?q= prefill, API-failure resilience
  theme-accent.test.tsx   accent switch, persistence, tokens, 8-way WCAG contrast
  theme-contrast.test.ts  token contrast audit for both themes
  useAsync.test.tsx       fake-timer polling: interval, hidden pause, dedupe, stale, reload
  usage.test.tsx          usage windows, baseline bound, retention note, advisory quota, tiles/deltas, chart, empty/truncated states
  schedules.test.tsx      schedule table, dialog, preview, ARM/RUN confirm, If-Match retry, history
  health-panel.test.tsx   health states, compact/full variants, disabled reason, stale poll
  webhooks.test.tsx       registry load, create show-once secret, enable 409, delete, deliveries
  platform-client.test.ts renewal/usage/health/webhook client contract: live URL/method/header and mock data
  portal.test.tsx         portal client cookie/CSRF contract, mock tenant scoping, signup/pending, own-data UI, zh
  portal-activate.test.tsx one-time invite activation: happy path, wrong code, local validation, zh
  tenants.test.tsx        tenant directory, one-time invite code/link, duplicate refusal, zh
  portal-activity.test.tsx tenant-scoped activity rows, cursor load-more, empty state, zh
  orders.test.tsx         portal plans/orders and admin orders/plans: idempotency, honest states, lease-ended restore, attestation, refund, review flag, zh
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
| Metrics | Nodes `live/total` with a thin capacity bar (mint; amber track at 0 live), 同时连接数 (`useCount`; a `Multi-slot pending` note appears when >1), usage via `formatBytes` |
| Foot | `Expires` label, expiry date + relative time, test login, pause/resume, delete (same labels and handlers as the table) |

The 同时连接数 metric is the vendor's purchased `useCount`, not an APC setting. The daemon
reports `effectiveSlots = min(useCount, AJS_ACCOUNT_SLOTS, 8)` as display capacity, with
`AJS_ACCOUNT_SLOTS` defaulting to `1`. Lease admission counts verified, listener-bound serving
slots, so a second lease on the same account is admitted only when a second slot is live;
otherwise it is refused as 429 `account_slots_exhausted`. The live account has `useCount=1`, so
`effectiveSlots=1`; a card above one still shows the `Multi-slot pending` note and its lease
carries `warning: multi_session_unimplemented` until live two-slot coexistence is proven.

The grid is `repeat(auto-fill, minmax(280px, 1fr))` with a 12 px gap and collapses to one column
below 760 px; card actions wrap and the card body never forces horizontal scroll. Selected cards get
a 1 px mint inset ring plus a faint mint wash. Hover changes only the border/background (no
translate), and the 150 ms fade + 4 px rise enter is disabled by `prefers-reduced-motion`. The table
keeps its existing below-760 px auto-card behaviour; the explicit card view works at every width.
`tests/accounts-view.test.tsx` covers the default table, switching both ways, the localStorage
preference (including an invalid value), card anatomy, bulk selection plus a card action, the mobile
grid, and the responsive CSS contract.

### Account export, import and bulk delete

The Accounts toolbar carries **Import** and **Export**; the bulk bar carries **Export selected**.
Export scope is the selected ids, the current filter, or every account, and the file is built in
memory and downloaded — nothing is written to browser storage.

| Format | Gate | Contents |
|---|---|---|
| Encrypted `.ajsx` | always available in live mode | PBKDF2-SHA-256 (600 000 iterations) + AES-256-GCM envelope; the passphrase is required, and device identity is included unless unticked |
| JSON | `AJS_ALLOW_PLAINTEXT_EXPORT=1` and `confirmPlaintext:"EXPORT"` | versioned `ajs-accounts` document; passwords only when the daemon holds sealed secrets, device identity optional |
| CSV | same plaintext gate | one row per account; device identity is refused because CSV has no column for it |

The export response sets `X-AJS-Export-Count`, `X-AJS-Export-Missing-Secrets` and
`X-AJS-Export-Devices` plus `Cache-Control: no-store`, and the daemon writes one `account.export`
audit row with counts only. Mock mode cannot encrypt: it disables the encrypted option and exports
JSON/CSV without passwords.

Import accepts `.ajsx`, `.json` or `.csv` (4 MiB UI cap; the daemon accepts a 4 MiB import body and
at most 1 000 rows) and always previews first: the preview is a `dryRun` call that renders every
row as create/update/rename/skip/fail plus warnings, and nothing is applied until **Import**.
Options are mode (`atomic` default, or `partial`), match key (`label`, `username_platform`, `id`),
conflict policy (`skip`, `update`, `rename`, `fail`), initial state (`paused` default, `active`,
`from_file`), id policy (`new` default, `preserve`), device policy (`restore_if_missing` default,
`keep`, `replace`) and timestamp preservation. An atomic import with any failed row is blocked in
the UI; partial mode applies the valid rows and lists the rest. Passwords never round-trip to the
screen or to storage.

Bulk delete posts one `POST /v1/accounts/bulk-delete` for the selection (at most 100 ids), with a
`dryRun` preview, `continue` (default) or `stop` mode, and per-row statuses `deleted`,
`would_delete`, `not_found`, `failed` or `skipped`; above ten selected accounts the dialog requires
the typed word `DELETE`. The daemon runs the same stop-gates-then-delete path as a single delete,
so a failed hook or sealer leaves that account and its secret in place.

### Renewal card

Each Accounts row shows a renewal countdown in the expiry cell (warning inside the vendor's
five-day window, overdue in red) and a **Renew** action; **Open renewal** in the row menu opens the
renewal drawer. The card is deliberately read-only:

- it shows the expiry date, whole days remaining, and a warning or overdue status;
- it lists the public plan catalogue captured on 2026-10-24 (1 month ¥20, 3 months ¥53, 12 months
  ¥198, 6-hour trial ¥1) with the saving against the monthly price, and the auto-renew variants as
  a note;
- its only outbound link is the vendor's official purchase page (`https://www.91ajs.com/purchase`),
  with no account id, label or token appended.

APC never posts to a payment endpoint, never calls a purchase API and never claims a renewal
succeeded. The page's prices are a snapshot, not a live quote: the vendor's checkout is
authoritative.

## Data layer

`src/lib/api.ts` exposes a typed promise API and switches on `VITE_USE_MOCK`:

| Variable | Default | Meaning |
|---|---|---|
| `VITE_USE_MOCK` | `true` (dev/test) | Use the deterministic in-browser store; a production build requires `false` |
| `VITE_API_BASE` | empty (same origin) | Base URL for `ajs-accountd` |
| `VITE_PANEL_TOKEN` | unset | Must stay unset: the build aborts if it is set |

**Security note:** a Vite `VITE_*` value is embedded in the client bundle. `vite.config.ts`
refuses any bundle build with `VITE_PANEL_TOKEN` set, because that would ship the shared admin
bearer to every browser. The runtime token gate keeps a token in `sessionStorage` per tab
instead, and production should still terminate auth in a BFF or reverse proxy. The demo build
(`npm run build:mock`) embeds no token, and a release build requires `VITE_USE_MOCK=false`.

Every unsafe live request also sends `X-AJS-CSRF: 1`. The daemon requires that non-simple header
on admin mutations and, when the browser supplies `Origin`, validates it exactly against
`AJS_ADMIN_ORIGINS`.

Live paths implemented by the client (`[SPEC]` = in `docs/PANEL.md`, `[PROPOSED]` = dashboard
contract only):

| Action | Path | Status |
|---|---|---|
| List/create accounts | `GET/POST /v1/accounts` | `[SPEC]` |
| Test login / change state / delete | `POST /v1/accounts/{id}/actions/login`, `PATCH/DELETE /v1/accounts/{id}` | `[SPEC]` |
| List nodes | `GET /v1/nodes` | `[SPEC]` |
| Drain / undrain / probe | `POST /v1/nodes/{id}/actions/{drain\|undrain\|probe}` | `[SPEC]` |
| List / create / rotate / release leases | `GET/POST /v1/leases`, `POST /v1/leases/{id}/actions/rotate`, `DELETE /v1/leases/{id}` | `[PROPOSED]`; served by `ajs-accountd` |
| Device pool | `GET /v1/device-pool`, `POST /v1/device-pool/{generate,export,import}`, `GET/PATCH /v1/device-pool/{id}`, `POST /v1/device-pool/{id}/{assign,release,retire,quarantine,purge}` | `[PROPOSED]`; served by `ajs-accountd` |
| Users | `GET /v1/users`, `POST /v1/users/{id}/{suspend\|resume}`, `PATCH /v1/users/{id}` | `[SPEC]` / `[PROPOSED]` |
| API keys | `GET/POST /v1/apikeys`, `DELETE /v1/apikeys/{id}` | `[SPEC]` |
| Requests | `GET /v1/requests`, `POST /v1/requests/mutations` | `[PROPOSED]` |
| Audit | `GET /v1/audit?limit=` | `[SPEC]` |
| Metrics | `GET /v1/metrics?range=24h\|7d` | `[PROPOSED]` |
| Settings | `GET/PATCH /v1/settings` | `[PROPOSED]`; served by `ajs-accountd` |
| Lab telemetry (read-only) | `GET /v1/lab/{overview,flags,gates,flows,sessions,egress,profiles,logs,diagnostics}` | `[PROPOSED]`; served by `ajs-accountd` |
| Lab account reads | `GET /v1/accounts/{id}/{servers,device}` (`servers` takes `q/region/status/province/city/offset/limit/sort` and returns `snapshotId` + `geo`) | `[PROPOSED]`; served by `ajs-accountd` |
| Account export/import/bulk delete | `GET /v1/accounts/io/capabilities`, `POST /v1/accounts/{export,import,bulk-delete}` | `[PROPOSED]`; served by `ajs-accountd` |
| Catalog target and session | `GET/PUT/DELETE /v1/accounts/{id}/selection`, `POST /v1/accounts/{id}/actions/{connect,disconnect,reconnect}` | `[PROPOSED]`; served by `ajs-accountd` |
| Operations | `GET /v1/operations?accountId=&state=&type=&limit=`, `GET/DELETE /v1/operations/{id}`, `POST /v1/accounts/{id}/actions/select-node` | `[PROPOSED]`; served by `ajs-accountd` |
| Events | `GET /v1/events?since=&limit=&wait=`, `GET /v1/events/stream` (SSE, cursor in `Last-Event-ID` or `since`) | `[SPEC]` / `[PROPOSED]`; served by `ajs-accountd` |
| Schedules | `GET/POST /v1/schedules`, `GET/PATCH/DELETE /v1/schedules/{id}`, `GET/POST /v1/schedules/preview`, `POST /v1/schedules/{id}/actions/run`, `GET /v1/schedules/{id}/runs` | `[PROPOSED]`; served by `ajs-accountd` |
| Renewal | `GET /v1/renewal/plans`, `GET /v1/renewal/accounts/{id}` | `[PROPOSED]`; served by `ajs-accountd` |
| Usage | `GET /v1/usage?from=&to=&account=&userId=` | `[PROPOSED]`; served by `ajs-accountd` |
| Gate health | `GET /v1/health/nodes` | `[PROPOSED]`; served by `ajs-accountd` |
| Webhooks | `GET/POST /v1/webhooks`, `PATCH/DELETE /v1/webhooks/{id}`, `GET /v1/webhooks/{id}/deliveries` | `[PROPOSED]`; served by `ajs-accountd` |
| Plans and orders (admin) | `GET/POST /v1/admin/plans`, `GET/PATCH/DELETE /v1/admin/plans/{id}`, `GET /v1/admin/orders?tenantId=&userId=&state=&limit=&offset=`, `GET /v1/admin/orders/{id}`, `POST /v1/admin/orders/{id}/{settle,refund,chargeback,cancel}` | `[PROPOSED]`; served by `ajs-accountd` |
| Plans and orders (portal) | `GET /v1/portal/plans`, `GET/POST /v1/portal/orders`, `GET /v1/portal/orders/{id}`, `POST /v1/portal/orders/{id}/cancel` | `[PROPOSED]`; served by `ajs-accountd` |
| Lab egress check (explicit probe) | `POST /v1/lab/egress-check` | `[PROPOSED]`; served by `ajs-accountd` |

Every path in this table is served by `ajs-accountd` in `daemon/` (an in-memory store persisted
as a versioned JSON snapshot); `[SPEC]` / `[PROPOSED]` describe where the contract is defined,
not whether it is implemented. The console never calls `POST /v1/payments/stripe`: that is the
daemon's Stripe-compatible provider callback, not a browser route, and it answers 503 until
`AJS_STRIPE_WEBHOOK_SECRET` is configured.

`GET /v1/metrics` also carries `telemetryAvailable`. `ajs-accountd` answers `false` — the gate
meter feeds the request log (`/v1/usage`), not this traffic series — so the Overview replaces the
ingress/egress chart, the traffic KPI tiles and the range switch with a no-telemetry empty state
that says exactly that; live node, lease and account-health facts still render, and no synthetic
curve or zeroed KPI is drawn. A daemon that
reports `true` gets the chart and the `Requests · 24h` / `Egress · 24h` tiles back.

The mock store is module-level and mutable: create/pause/delete accounts, drain nodes, rotate and
release leases, create/revoke keys, edit allowlists, add mutation-lab requests, save settings and
reset the dataset. A full browser reload restores the seed data.

## Design system — APC (graphite + mint)

`src/styles/app.css` opens with one token block per theme; components read semantic names and
never a hex. Dark is canonical, light mirrors every role:

```css
--canvas:#0A0D0C; --surface:#101413; --raised:#161C1A; --overlay:#1C2421; --sunken:#070A09;
--sidebar:#0C1110; --active:#212B27;
--text:#F2F7F5; --text-2:#C3D2CB; --muted:#98AAA2; --text-tertiary:#7E928A; --text-disabled:#5C6D66;
--border-faint:rgba(255,255,255,.05); --border:rgba(255,255,255,.08);
--border-strong:rgba(255,255,255,.14); --border-input:rgba(255,255,255,.20);
--mint:#5EEAD4; --mint-strong:#2DD4BF;
--success:#34D399; --warning:#FBBF24; --danger:#FB7185; --info:#60A5FA; --unavailable:#8A9A93;
--sidebar-w:248px; --sidebar-w-collapsed:56px; --topbar-h:64px;
--control-h:32px; --row-h:56px; --row-h-comfy:60px; --table-head-h:40px;
--radius-control:8px; --radius-menu:12px; --radius-card:16px; --radius-modal:24px;
--dur-fast:120ms; --dur-base:180ms; --ease-out:cubic-bezier(.16,1,.3,1);
```

- **Four surfaces, two layers.** Canvas → surface → raised → overlay. Pages are canvas plus
  content; menus, modals, drawers and toasts sit on `--overlay`, never on the canvas.
- **Borders are white alpha** (`--border-faint` 5 %, `--border` 8 %, `--border-strong` 14 %,
  `--border-input` 20 %) so a line stays correct when the surface under it changes.
- **Five-step text ramp.** `--text` → `--text-2` → `--muted` → `--text-tertiary` →
  `--text-disabled`; only the first four are for text, the fifth is disabled-only. All four clear
  AA on every surface (`bg / sidebar / surface / elevated`):

| Theme | `--text` | `--text-2` | `--muted` | `--text-tertiary` |
|---|---|---|---|---|
| dark | 18.03–15.96 | 12.46–11.03 | 8.00–7.08 | 5.92–5.24 |
| light | 15.52–16.89 | 9.09–9.90 | 6.21–6.75 | 5.26–5.72 |

  `tests/theme-contrast.test.ts` parses both blocks and fails if any of the four drops below 4.5:1.
  `--bg`, `--elevated` and `--bg-sunken` remain as aliases of `--canvas`, `--raised` and `--sunken`.
- **Status vocabulary.** `ok / info / warn / error / unavailable` are the only status names;
  `--status-*` aliases the four hue tokens and adds `--unavailable` for "exists but cannot serve".
  `StatusPill` (`components/primitives.tsx`) pairs an inline glyph with the label and tints both
  from one `--status-hue`, so a variant is one class. `lib/labels.ts` maps account states, node
  states and audit severities onto the vocabulary; status is never colour-only.
- **Icons.** `components/icons/ApcIcons.tsx` draws the brand mark, every nav glyph, the five status
  glyphs and the shell controls inline on a 24 px grid with `currentColor`; `lucide-react` remains
  for page-level glyphs only. Theme, language, collapse and node-state glyphs morph through
  `MorphIcon` (see [Morph icons](#morph-icons)). All decorative icons are `aria-hidden`, and the
  surrounding link or button keeps its label.
- **Geometry.** 248 px sidebar (56 px collapsed rail), 64 px topbar, 32 px controls, 16 px cards,
  24 px modals, 40 px table header, 56 px rows (Accounts opts into the 60 px `comfy` density).
- **Type.** 13/18 UI, 11/14 uppercase labels with +0.06em, 22/28 page title with −0.02em,
  28/32 metric, 12/18 mono with tabular numerals.
- **Motion.** 120 ms for colour/hover, 180 ms for geometry and entrances, one `--ease-out`
  (`cubic-bezier(.16,1,.3,1)`); cards, metrics, table rows, overlays and toasts enter with a
  short fade/rise. `prefers-reduced-motion: reduce` collapses every animation and transition to
  0.001 ms and stops the progress sweep, pulse and entrance animations.
- **Focus ring.** 2 px mint with 2 px offset on every interactive element.

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

### Custom select

Every dropdown in the console is the custom combobox in `components/Select.tsx`; there is no
native `<select>` anywhere in `src/`. One component covers single- and multi-select, searchable
and typeahead modes, option groups and five density variants that change chrome only — the
structure and accessibility stay one combobox everywhere.

| Variant | Shape | Used on |
|---|---|---|
| `filter` | compact toolbar pill (`--control-h-sm`, `--raised`, 126 px min) | Accounts (state, platform), Nodes Connected (region, state), Nodes Catalog (status), Requests (status, source) |
| `form` | full-width inside a `Field`, auto width in a page header | Accounts add-account modal (platform), Users create-key (user), Lab account picker, Requests mutation-lab drawer |
| `settings` | 38 px trigger and 44 px option rows with descriptions | Settings lease TTL |
| `log` | 11 px mono trigger and 26 px option rows | Logs (severity, actor), Requests (method, node, error class), Lab Logs/Egress filters |
| `region` | 30 px trigger, 260 px minimum popup, grouped + searchable + multi-select | Nodes Catalog region picker |

Behaviour:

- **Trigger.** A real `<button role="combobox" aria-haspopup="listbox" aria-expanded>` shows the
  value or placeholder and a count badge for multi-select. `aria-controls` points at the listbox
  only while open, and `aria-activedescendant` tracks the active option; focus stays on the trigger
  in the non-searchable case.
- **Keyboard.** ArrowDown/ArrowUp open (ArrowUp starts at the last option) and move; Home/End jump;
  Enter selects; Escape closes and returns focus to the trigger; Tab closes and moves on; typing a
  letter outside a searchable popup runs a 600 ms typeahead over option labels. Disabled options
  are skipped and cannot be selected.
- **Search.** `searchable` moves focus into a real input carrying the combobox role, so screen
  readers announce filtered results from the focus point. Filtering matches label, description and
  group; an empty result announces `No matches for “…”` in a `role="status"` node, and a clear
  button restores the list. Enter selects from the search input; Shift+Tab closes back to the
  trigger rather than a page control that precedes the portal in document order.
- **Multi-select.** `multiple` keeps the popup open, sets `aria-multiselectable`, shows the count
  badge and a footer with select-all-visible, clear-all and done. Every option carries a check mark
  and `aria-selected`.
- **Groups.** `option.group` renders `role="group"` sections with a labelled header while preserving
  the caller's option order.
- **Portal and positioning.** The popup is rendered with `createPortal` into `<body>` and
  `position: fixed`, so toolbar overflow and a drawer's scroll container cannot clip it. It is
  measured after commit and repositioned on window resize and capture-phase scroll; it flips above
  the trigger when the space below is short, clamps to an 8 px viewport margin, caps at 320 px and
  keeps at least 96 px usable.
- **Motion.** The popup enters with `select-pop-in` over `--dur-base` / `--ease-out` (scale 0.98
  from above, translate from below) and collapses to 0.001 ms under `prefers-reduced-motion`. The
  open trigger uses the shared mint border and 3 px ring.

### Morph icons

The theme, language, sidebar-collapse and node-state glyphs are not swapped as images: they morph.
`src/lib/iconMorph.ts` is the pure core — SVG path parsing and primitive→cubic normalisation,
arc-length resampling at 48 points with anchored corners, a closed-form 2D Procrustes
correspondence, polar interpolation (rotation/scale/translation) and a damped-spring integrator.
`src/components/icons/MorphIcon.tsx` is the React driver; glyphs live in `morphGlyphs.ts` and are
plain single-subpath `d` strings on the 24×24 grid. Subpaths pair one-to-one by centroid and
length, and a leftover collapses into a point with an opacity fade instead of duplicating a target.

- **Spring.** The default preset is `snappy` (stiffness 420, damping 30); `smooth`, `bouncy` and a
  custom `{stiffness, damping}` are also accepted. The integrator is interruptible: a target change
  mid-flight restarts from the shape on screen with velocity carry-over.
- **Reduced motion.** `prefers-reduced-motion: reduce` (or the `reducedMotion` prop) renders the
  resting glyph verbatim and schedules no frame; the `progress` prop gives a controlled,
  spring-free pair for tests and frozen states.
- **Accessibility.** A settled icon renders its committed `d` strings exactly. Without an
  `aria-label` the SVG is decorative (`aria-hidden`); with one it exposes that name.
- **Licence.** The core is adapted from
  [morphicons](https://github.com/guillermolg00/morphicons) (MIT, © Guillermo) and the MIT notice is
  reproduced in `MorphIcon.tsx`; the adaptation (glyph list instead of Lucide IconNodes,
  one-to-one subpath pairing, point-collapse leftovers) is APC's.

## Keyboard and accessibility

- `Ctrl/Cmd+K` opens the command palette (fuzzy filter, ↑/↓, Enter, Esc, Tab trap). Queries of
  2+ non-space characters also match Accounts and Nodes entries (see
  [Command palette data search](#command-palette-data-search)).
- Sidebar links, table actions and form controls are keyboard reachable with visible focus.
- Custom selects follow the APG select-only combobox pattern: Arrow/Home/End/Enter/Escape/Tab,
  typeahead when not searchable, a focused search input when searchable, `aria-activedescendant`,
  and `aria-multiselectable` for multi-select (see [Custom select](#custom-select)).
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
  login is wired but default-off (`AJS_ACCOUNT_LOGIN=1` opts in). Live login/FV/egress was
  exercised on 2026-10-23 and 2026-10-24 for one account (see the root README); multi-account
  coexistence and Resin live publish remain `[UNVERIFIED]`.
- Catalog target/connect, account import/export, bulk delete, lease creation and the device pool
  are wired to the daemon routes; a P0 daemon without them degrades to a read-only catalog and a
  disabled export option rather than faking success. Usage, schedules, gate health and webhooks are
  surfaced and degrade to an explicit disabled state when the daemon answers `enabled:false` or
  503. The device-pool page renders hash-only records and never the sealed machine code; a daemon
  without `AJS_MASTER_KEY` answers `enabled:false` with its reason, and every lifecycle action needs
  the daemon's typed token (`RELEASE`, `RETIRE`, `QUARANTINE`, `PURGE`). Lease creation sends a
  per-dialog `Idempotency-Key`; the page shows the daemon's 409/429 reason instead of retrying
  silently, and a publish-hook warning is surfaced as a warning toast after the lease committed.
- Usage aggregates the daemon's request log: with gate metering on (`AJS_FLOW_METER`, default)
  each completed SOCKS5 flow appends one row, so the page reflects real proxy traffic, but its
  requests and bytes are still recorded rows and reset with the 10 000-row retention. The
  retention note labels the covered span, the page never extrapolates a missing day, and
  `truncated:true` shows when the daemon clipped the window.
- The gate-health panel is a diagnostic for loopback gates on the daemon host. It does not test
  vendor reachability, cannot see a node whose gate is not running, and keeps only one hour of
  samples (120 at 30 s) for at most `AJS_HEALTH_MAX_NODES` nodes (default 16).
- Webhooks are real daemon deliveries, but the delivery ring is in memory (last 200 attempts per
  endpoint, lost on a restart) and the signing secret survives a restart only with
  `AJS_MASTER_KEY`. The page never simulates a delivery or re-shows a secret.
- Orders are an offline ledger, not a payment integration. **Mark offline payment received** and
  **Record offline refund** attest that money already moved outside the system; the console never
  charges a card, stores no card data and offers no checkout. The daemon's Stripe-compatible
  webhook is disabled (503) unless `AJS_STRIPE_WEBHOOK_SECRET` is set, and no real provider event
  has reached it. The order and plan surfaces have run against the mock store and `httptest` only,
  and a partial or unsupported reversal is flagged for review instead of applied.
- Plaintext export needs `AJS_ALLOW_PLAINTEXT_EXPORT=1` on the daemon and the `EXPORT`
  confirmation in the dialog; the dashboard cannot override the gate, and encrypted `.ajsx`
  export is the default.
- Live mode is verified against the local daemon; the tests use fakes and contact no vendor
  endpoint.
- The live build hides the mock reset action entirely — the daemon has no reset endpoint.
- `VITE_PANEL_TOKEN` is refused at build time because it would be readable from the bundle. The
  runtime token gate keeps a token in `sessionStorage` per tab instead. In the
  production kit nginx maps each Basic-authenticated operator to an independent
  bearer; the daemon enforces that token's viewer/operator/admin role. The SPA
  has no server-side session or role-aware menu: server-side denials, not UI
  hiding, are the boundary.
- The Lab page only reads daemon state; the daemon's one POST (`/v1/lab/egress-check`) runs an
  explicit egress probe through the node's gate and returns the result without storing it. The
  client keeps that observation per node for the life of the tab (polls and manual refreshes keep
  it, a page reload drops it), so the verified IP is never presented as server-side history. Mock
  fixtures are deterministic, not gate telemetry, and an `enabled:false` body means the
  corresponding mode is off — the page shows the reason rather than fabricating rows.
- Mock state is in memory; refreshing the tab discards edits (Settings → Reset dataset does the same
  explicitly).
- Log export is a placeholder. Live events use SSE (`/v1/events/stream`); every other list polls
  (see [Polling and stale data](#polling-and-stale-data)). `EventSource` cannot attach the panel
  bearer header, so a token-protected live deployment must proxy the stream same-origin (the
  loopback insecure daemon needs no token). The vendor catalog is the only server-paged,
  server-sorted list (500 rows per request max); every other table paginates a bounded in-memory
  list locally, so a table can page but never query beyond what its poll returned.
- Documentation scope, not a limitation: `dashboard/docs/` is the AJS-20 design contract; the
  accent slice only adds §3.9 (accent table) to `DESIGN-SYSTEM.md` and leaves the rest of the
  contract unchanged.

## License

Repository code is MIT; see the root [`LICENSE`](../LICENSE) and [`NOTICE`](../NOTICE) for
third-party software, icon and map-data attribution.
