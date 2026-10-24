# AJiaSu Proxy Console

Operator dashboard for `ajs-accountd` — accounts, nodes, leases, users, request telemetry, the
audit trail and a read-only Lab instrument in one graphite-and-mint APC surface. **Mock-first:** the
default build runs on deterministic in-memory data and never contacts AJiaSu, a vendor binary or a
live daemon. A live mode against the local `ajs-accountd` skeleton is also built and verified; it
is opt-in through build-time environment variables (see [Modes](#modes) and
[Live mode](#live-mode)).

- Stack: Vite 6, React 19, TypeScript, React Router, `lucide-react`, `recharts`, `clsx`.
- Styling: plain CSS variables, no Tailwind, no component library.
- Tests: Vitest + React Testing Library + jsdom, 182 tests in 25 files.
- Language: Chinese/English UI with a `中` / `EN` sidebar switch and a Settings language card.
- Package manager: npm.
- Status: `v0.1.0` implementation slice. Local git commits only (not pushed), no credentials, no live vendor calls.

## Modes

The mode is fixed at build time; there is no runtime switch.

| Mode | Build trigger | Data source | Reset |
|---|---|---|---|
| Mock (default) | `VITE_USE_MOCK` unset or `true` | deterministic in-browser store (`src/lib/mock.ts`), seeded on page load | **Settings → Reset dataset**, or a full reload |
| Live | `VITE_USE_MOCK=false` plus `VITE_API_BASE`; `VITE_PANEL_TOKEN` optional (otherwise the runtime token gate asks on 401) | `ajs-accountd` over HTTP under `/v1/...` | not offered; the reset card is not rendered |

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
| Settings | **Language** card with a `中文` / `English` segmented control and a note that the choice is per browser |

- **Default.** On first load the stored choice wins; with no stored value the browser hint decides —
  `navigator.language` starting with `zh` selects Chinese, everything else selects English.
- **Persistence.** Every switch writes `localStorage["ajs.lang"]` (`"zh"` or `"en"`). The choice is
  per browser and is never sent to the daemon; when storage is unavailable the in-memory choice
  still applies for the session.
- **Document language.** The provider sets `<html lang>` to `zh-CN` or `en`, so assistive
  technology and the browser see the active language.
- **Coverage.** Every user-visible string — shell, pages, Lab, dialogs, empty/error states — is
  translated; English is the source dictionary and Chinese must mirror every key. Account labels,
  server names, IPs and API field values are data and are never translated. The command palette
  still finds pages in either language.
- **Tests.** `tests/i18n.test.tsx` asserts dictionary parity and non-empty values, interpolation
  and English/key fallback, switching the sidebar and Lab to Chinese plus `document.lang`, restoring
  a stored choice on a fresh mount, the `zh*` browser default, switching back to English from
  Settings, and palette page search in both directions.

## Quick start

```bash
npm install
npm run dev        # http://127.0.0.1:5173
npm run build      # tsc -b && vite build → dist/
npm run preview    # http://127.0.0.1:4173, serves dist/
npm test           # Vitest, 182 tests in 25 files
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
AJS_LISTEN=127.0.0.1:2261 AJS_ALLOW_INSECURE=1 \
  AJS_CORS_ORIGINS=http://127.0.0.1:18082,http://127.0.0.1:18081 \
  go run ./cmd/ajs-accountd

# 2. live build (from dashboard/, in a second shell). No VITE_PANEL_TOKEN:
#    the insecure daemon ignores the bearer header. For a real token,
#    export AJS_ADMIN_TOKEN=$(openssl rand -hex 32) and build with
#    VITE_PANEL_TOKEN="$AJS_ADMIN_TOKEN".
VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 npm run build

# 3. serve the static build
npx vite preview --host 127.0.0.1 --port 18082
```

Open `http://127.0.0.1:18082`. `vite preview` defaults to port 4173 in `vite.config.ts`;
`--port 18082` overrides it for this deployment. The preview server does not proxy: the browser
calls the daemon directly, so the daemon must allow the preview origin in `AJS_CORS_ORIGINS` (the
verified run used `http://127.0.0.1:18082,http://127.0.0.1:18081`).

**Token:** `AJS_ALLOW_INSECURE=1` is a loopback-only lab switch (startup refuses it on any other
bind) and leaves the daemon without bearer auth. For a real token, generate one with
`openssl rand -hex 32` — startup refuses documented placeholders and anything shorter than 32
characters — and pass the same value as `AJS_ADMIN_TOKEN` and `VITE_PANEL_TOKEN`. Every `VITE_*`
value is readable in the shipped bundle, so production must terminate auth in a BFF or reverse
proxy and keep `ajs-accountd` bound to `127.0.0.1`. The LAN build below takes the other route: it
bakes no token and collects one per tab through the runtime token gate.

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
  `TokenGate` asks for the admin token; the Settings **Daemon** card dispatches
  `ajs:token-required` from **Set token** / **Replace token** for the same dialog. The value is
  kept in `sessionStorage["ajs.panelToken"]` for that tab only, never rendered back, and a submit
  retries every mounted loader with the bearer header. **Clear session token** in Settings drops it.

## Polling and stale data

Every list page refreshes itself on the interval from `docs/PAGES.md` §2.2. Polling lives in one
place — `src/lib/useAsync.ts` — and each page opts in with `{ intervalMs }`.

| Surface | Interval | Notes |
|---|---|---|
| Overview metrics | 10 s | the same page polls accounts, nodes, leases and audit at 15 s |
| Accounts / Nodes / Leases / Users | 15 s | Users polls both users and API keys |
| Requests flows | 5 s | polls `/v1/lab/flows?limit=200`; mutation history 10 s, node labels 15 s |
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
  `q`, `region`, `status`, `sort` (`order|title|host|status|region`), `offset` and `limit`
  (default 50, max 500); the response returns `total`, `regions:[{code,count}]`, `nextOffset` and
  `hasMore`. The live `acc-07` login result is **2,814 vendor servers**, all carrying region `cn`,
  so the footer reads `1–50 of 2,814` and the page shows one *China* group; region labels come
  from `src/lib/regions.ts` (ISO 3166-1 alpha-2 plus a few city subdivisions, English and
  Chinese), and the raw vendor code stays visible as secondary mono text.
- Rows render in per-region groups with a group header, caption and a select-visible checkbox.
  Group headers group the current page's rows but take their count from the server's `regions[]`
  summary, so a region spanning pages never understates its total; the header notes how many rows
  are on this page when the two differ.
- Columns: server title/id, account, region (localised name + code), `host:port`, vendor status,
  runtime state (`Current` for a `LIVE` node, otherwise the node state or `not linked`), selection
  (`selected` / `not selected` / `skipped by node limit`) and flags (`full load`, `member only`,
  blocking marks, `ping secret`, `stale snapshot`).
- Search is debounced 250 ms before it reaches the daemon; region, status and sort changes return
  to offset 0, and the footer's page/size controls request the next window instead of slicing a
  full list. Selection is local UI state keyed by account and server — the bulk bar says so and no
  daemon mutation is made.
- A retained-but-not-refreshed snapshot shows the `stale snapshot` badge plus a callout with the
  fetch time and account; a request failure renders the inline error with Retry; a disabled login
  mode renders the `{enabled:false}` reason. No demo or overseas rows are invented.
- The catalog polls every 30 s, the Connected tab every 15 s. Connected renders `/v1/nodes` with
  region/state filters, latency sparkline, success/capacity meters, drain/undrain, probe and a
  detail drawer.
- Mock mode serves deterministic fixtures from `src/lib/mock.ts` (including the paging envelope),
  so the same layout is reviewable offline.

### China map and province filter

- The Catalog tab renders a province choropleth beside the toolbar. Geometry is committed in
  `src/assets/geo/cn-provinces.json` (31 mainland provinces, pre-projected SVG paths) and the
  city lookup/points in `cn-cities.json`; the page never calls a map or tile service.
- Province counts come from the whole retained catalog. `api.getCatalogSnapshot()` assembles it by
  reading every 500-row page and caches it until the daemon's `fetchedAt` changes, so the 30 s
  table poll does not re-download the catalog. The read starts only after that account's catalog
  response settles (so the snapshot timestamp is known and one six-page pass is enough), and
  concurrent callers share one in-flight pass (single-flight). An invalidation epoch keeps a pass
  started before a reset or explicit refresh from repopulating the cache; the table itself stays
  server-paged.
- Vendor titles are the only geography key (`<city> #<n>`). `src/lib/geo.ts` resolves them with an
  exact match, a one-directional prefix rule and the reviewed aliases (`济南 - 莱芜` → 济南,
  `延吉` → 延边); `海南藏族自治州` resolves to Qinghai, not Hainan. Unresolved titles are counted
  in **Other** and are never assigned a province.
- Clicking a province filters the table to it and returns to page 1; clicking it again clears the
  filter. A city Select appears for the selected province. Filtered pages are sliced from the
  snapshot, while search, status, sort, pagination and the account picker keep working.
- The current `LIVE` node keeps a mint ring and a city dot even when a filter excludes its row; the
  header chip names it. The SVG is decorative (`aria-hidden`) and the province list under it is the
  keyboard/screen-reader control. Transitions are disabled under `prefers-reduced-motion`.
- Regenerate both assets with `node scripts/geo/build-cn-geo.mjs` (DataV GeoAtlas is contacted at
  build time only; `--raw <dir>` reuses a cache and `--check` fails on drift). The committed asset
  records the source URL and the sha256 of the national boundary file.

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

## Requests flows

`/requests` is the audit table over the daemon's gate flow ring, not the vendor's request log.

- **Source.** `GET /v1/lab/flows?limit=200` every 5 s; node labels come from `/v1/nodes` every
  15 s and the mutation-lab history from `/v1/requests` every 10 s. Rows from the two sources share
  one table and a `source` filter (`Gate flows` / `Mutation lab`).
- **Columns.** Time, account/node, method, target (`host:port` only), status (`ok` / `rejected`
  for gate flows; HTTP status for mutation rows), bytes in/out, duration, source and error class.
  The daemon records only host and port — never a URL, headers or a credential — and the SID is
  already redacted server-side. A row opens a detail drawer with the same fields.
- **Summary.** Five tiles (flows, accepted, rejected, p95 duration, bytes) computed over the
  filtered rows, plus the `{shown} of {total} rows · {dropped} dropped` hint from the bounded
  500-entry per-gate rings.
- **Filters.** Search (host, account, node, method, id), time range (15 m / 1 h / 24 h / all),
  status, method, account/node, source and error class. The source filter isolates gate flows from
  mutation-lab rows.
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

## HMC layout

The HMC build host serves three endpoints; the live pair is also published on the LAN:

| Endpoint | Role | How it runs |
|---|---|---|
| `127.0.0.1:18081` | mock dashboard | `docker compose up -d` in `dashboard/`; nginx image built from the default (mock) source, rebuilt after the live/mock copy fixes |
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
| Live | `VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 VITE_PANEL_TOKEN=$AJS_ADMIN_TOKEN npm run build` | exit 0, `✓ built in 2.51s`, `dist/assets/index-Bx_6YADv.js` |
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

## Tests and QA

- 182 Vitest tests in 25 files (65 at the 2026-10-23 record; see
  [Verification](#verification-recorded-2026-10-23)).
  `tests/i18n.test.tsx` covers the bilingual UI: dictionary parity and non-empty strings,
  interpolation plus English/key fallback, switching to Chinese with `document.lang`, restoring a
  stored choice, the `zh*` browser default, the Settings language card, and palette page search in
  both languages.
  `tests/useAsync.test.tsx` drives polling with fake timers (interval, hidden pause, resume, unmount,
  dedupe, failed poll, reload, dependency change, `enabled: false`) and checks the stale dot plus the
  in-flight activity flag behind the topbar progress line.
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
  `tests/ui-quickfix.test.tsx` pins the shell quick fixes: the 248→56 px sidebar collapse with
  `localStorage["ajs.sidebar"]` persistence, the APC branding (no workspace switcher or operator
  avatar), and the centered add-account modal with concurrency fixed at one and the vendor note.
  `tests/token-gate.test.tsx` covers the runtime token gate: a live 401 opens **Panel token
  required**, submitting stores `ajs.panelToken` in `sessionStorage` and retries with the bearer
  header.
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

## Routes

| Route | Page | Primary surfaces |
|---|---|---|
| `/` | Overview | 4 metric tiles, ingress/egress chart, account health, activity, quick actions; with `telemetryAvailable:false` the chart and traffic KPIs are replaced by a no-telemetry empty state (real counts and account health stay) |
| `/accounts` | Accounts | filterable table or card grid (view switch), centered add-account modal (concurrency fixed at 1), login test, pause/resume, delete confirm, bulk select |
| `/nodes` | Nodes | **Catalog** (real vendor servers grouped by region, server-side paging/filter/sort, offline China map with province filter, local multi-select, current-node marker) and **Connected** (region/state filters, latency sparklines, success/capacity meters, drain toggle, detail drawer with localized region, `Not measured` success, clock-skew stamp, vendor-claim egress and **Verify egress**) |
| `/leases` | Leases | sticky-lease callout, copy proxy URL, rotate, release confirm, TTL |
| `/users` | Users | user cards, quota meters, API keys, show-once secret, revoke, allowlist editor |
| `/requests` | Requests | gate-flow table with search/time/status/method/account-node/source filters, shared pagination footer, row detail drawer, mutation lab drawer |
| `/logs` | Logs | severity/actor filters, paginated audit table, export placeholder |
| `/lab` | Lab | tabs: Overview, Gates & sessions (paginated), Egress (guarded check), Servers (server-paged), Protocol, Device, Logs (paginated), Diagnostics; environment badges; account picker for Servers/Device |
| `/settings` | Settings | transport, health interval, lease TTL, subscription refresh, theme + accent, language, **Updates** check card, daemon/token card, reset |

`*` redirects to `/`.

## Layout

```text
src/
  App.tsx                 lazy routes for all 9 pages
  main.tsx                entry, self-hosted Inter / JetBrains Mono
  styles/app.css          tokens, shell, components, responsive, light theme, accents
  lib/
    types.ts              Account, Node, Lease, PanelUser, ApiKey, RequestLog, AuditEvent, Metrics, Lab*
    mock.ts               6 accounts · 12 nodes · 8 leases · 4 users · 40 requests · 20 audit rows
    lab.ts                mock Lab fixtures: gates, sessions, egress, profiles, logs, diagnostics
    api.ts                typed client; mock by default, fetch when VITE_USE_MOCK=false
    nodes.ts              catalog query state + region grouping + client-side geo filters
    geo.ts                offline province/city resolution and map aggregation
    format.ts             bytes, dates, TTL, proxy URL helpers
    i18n.tsx              EN/ZH dictionaries, translate(), provider, ajs.lang persistence
    labels.ts             account/node/severity tone maps
    nav.tsx               navigation + command registry metadata
    theme.tsx             theme + accent provider, localStorage persistence
    useAsync.ts           load/poll/dedupe/stale state, global refresh event
    useFocusTrap.ts       Esc + Tab trap for palette/drawer/modal
  components/
    AppShell, Sidebar, Topbar, CommandPalette, Toast, CopyButton, TokenGate
    DataTable, Pagination, Overlay (Drawer/Modal/ConfirmDialog), primitives
    Select.tsx            custom combobox: variants, search, multi, groups, body portal
    icons/                APC brand, nav, status and shell glyphs (inline SVG)
    nodes/                CatalogTab (grouped vendor catalog), ChinaMap (offline choropleth), ConnectedTab (runtime nodes)
    lab/                  Overview, Sessions, Servers, Protocol, Device, Logs, Diagnostics tabs
  pages/                  Overview, Accounts, Nodes, Leases, Users, Requests, Logs, Lab, Settings
tests/
  setup.ts                jsdom polyfills (ResizeObserver, clipboard, matchMedia)
  lab.test.tsx            Lab tabs, redaction, disabled telemetry, honesty fixes
  i18n.test.tsx           dictionary parity, zh/en switching, browser default, ajs.lang, document.lang
  token-gate.test.tsx     401 → runtime token dialog → sessionStorage token + retry
  update-card.test.tsx    Settings Updates card states and deploy command
  accounts-view.test.tsx  Accounts table/cards switch, persistence, card actions, mobile grid
  app.test.tsx            all routes, palette open/navigate/Esc/filter
  interactions.test.tsx   copy toast, rotate, release, create account, bulk pause, key show-once, mutation lab
  nodes-catalog.test.tsx  catalog grouping, filters, selection, stale/failed/disabled callouts
  china-map.test.tsx      map totals, province selection, city filter, Other, reduced motion, i18n
  geo.test.ts             offline title→province resolution and map aggregation
  catalog-snapshot.test.ts  full-catalog paging and snapshot cache
  nodes-drawer.test.tsx   drawer region, not-measured, clock skew, egress claim/verify, zh copy
  select.test.tsx         combobox ARIA/keys/search/multi/groups/portal/flip/motion
  requests.test.tsx       gate-flow rows, filters, detail, empty/error, host-only targets
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

**Security note:** a Vite `VITE_*` value is embedded in the client bundle. A live build that sets
`VITE_PANEL_TOKEN` embeds it; do not ship a real admin token that way. The LAN build embeds no
token and relies on the runtime token gate instead, and production should still terminate auth in
a BFF or reverse proxy. The default build remains mock-only and embeds no token.

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
| Lab telemetry (read-only) | `GET /v1/lab/{overview,flags,gates,flows,sessions,egress,profiles,logs,diagnostics}` | `[PROPOSED]`; served by `ajs-accountd` |
| Lab account reads | `GET /v1/accounts/{id}/{servers,device}` (`servers` takes `q/region/status/selected/offset/limit/sort`) | `[PROPOSED]`; served by `ajs-accountd` |
| Lab egress check (explicit probe) | `POST /v1/lab/egress-check` | `[PROPOSED]`; served by `ajs-accountd` |

Every path in this table is served by `ajs-accountd` in `daemon/` (an in-memory store persisted
as a versioned JSON snapshot); `[SPEC]` / `[PROPOSED]` describe where the contract is defined,
not whether it is implemented.

`GET /v1/metrics` also carries `telemetryAvailable`. `ajs-accountd` answers `false` — there is no
traffic meter yet — so the Overview replaces the ingress/egress chart, the traffic KPI tiles and
the range switch with a no-telemetry empty state that says exactly that; live node, lease and
account-health facts still render, and no synthetic curve or zeroed KPI is drawn. A daemon that
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
  for page-level glyphs only. All decorative icons are `aria-hidden`, and the surrounding link or
  button keeps its label.
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
  login is wired but default-off (`AJS_ACCOUNT_LOGIN=1` opts in). Live vendor login/FV/Resin
  remains `[UNVERIFIED]` without an Owner account.
- Live mode is verified against the local daemon; the tests use fakes and contact no vendor
  endpoint.
- The live build hides the mock reset action entirely — the daemon has no reset endpoint.
- `VITE_PANEL_TOKEN` is build-time and readable from the bundle. The runtime token gate avoids
  baking one (it keeps the value in `sessionStorage` per tab), but the token is still one shared
  admin bearer over plain HTTP; there is no per-operator auth, role model or server-side session,
  so production needs a BFF or reverse proxy.
- The Lab page only reads daemon state; the daemon's one POST (`/v1/lab/egress-check`) runs an
  explicit egress probe through the node's gate and returns the result without storing it. The
  client keeps that observation per node for the life of the tab (polls and manual refreshes keep
  it, a page reload drops it), so the verified IP is never presented as server-side history. Mock
  fixtures are deterministic, not gate telemetry, and an `enabled:false` body means the
  corresponding mode is off — the page shows the reason rather than fabricating rows.
- Mock state is in memory; refreshing the tab discards edits (Settings → Reset dataset does the same
  explicitly).
- Log export is a placeholder, and SSE is not implemented in v1; polling is (see
  [Polling and stale data](#polling-and-stale-data)). The vendor catalog is the only
  server-paged, server-sorted list (500 rows per request max); every other table paginates a
  bounded in-memory list locally, so a table can page but never query beyond what its poll
  returned.
- Documentation scope, not a limitation: `dashboard/docs/` is the AJS-20 design contract; the
  accent slice only adds §3.9 (accent table) to `DESIGN-SYSTEM.md` and leaves the rest of the
  contract unchanged.

## License

Repository code is MIT; see the root [`LICENSE`](../LICENSE).
