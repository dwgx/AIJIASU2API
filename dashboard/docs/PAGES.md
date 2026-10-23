# PAGES — AJiaSu Proxy Dashboard

Status: design contract v1 · 2026-10-23 · slice AJS-20. Companion to `DESIGN-SYSTEM.md`; every
token, component and state name used here is defined there. This file is normative for **page
composition**: routes, blocks with dimensions, data fields, actions, empty/loading/error behaviour,
keyboard entries and API endpoints.

## 0. Endpoint status legend

Both backend documents (`docs/PANEL.md`, `research/76-sdk-integration/INTERFACE.md`) are marked
"design, no implementation" as of 2026-10-23. **No endpoint in this file is implemented yet.** Two
markers are used so the implementer knows what is already designed versus what this document is
inventing:

| Marker | Meaning |
|---|---|
| `[SPEC]` | Route and payload are named in `PANEL.md` §3 or `INTERFACE.md` §HTTP. Build the client against it as written. |
| `[PROPOSED]` | Not named in either backend document. This page needs it; the shape below is a proposal and must be agreed before the UI ships. |

Every endpoint is loopback-only and requires `Authorization: Bearer $AJS_ADMIN_TOKEN` except
`GET /healthz` (`INTERFACE.md` §HTTP). Errors are `{"error":{"code","message"}}` with codes
`INVALID_ARGUMENT`, `NOT_FOUND`, `CONFLICT`, `UPSTREAM_UNAVAILABLE`, `QUOTA_EXCEEDED`
(`PANEL.md` §3). The UI maps those five codes to fixed copy — it never renders a raw message alone.

## 1. Route map and navigation

| # | Route | Nav label | Icon (lucide) | Badge |
|---|---|---|---|---|
| 1 | `/` | Overview | `layout-dashboard` | — |
| 2 | `/accounts` | Accounts | `users-round` | count of accounts in `AUTH_FAILED` / `MEMBERSHIP_EXPIRED` |
| 3 | `/nodes` | Nodes | `server` | count of nodes not `LIVE` |
| 4 | `/leases` | Leases | `link-2` | — |
| 5 | `/users` | Users | `user-cog` | count suspended |
| 6 | `/requests` | Requests | `activity` | — |
| 7 | `/logs` | Logs | `scroll-text` | — |
| 8 | `/settings` | Settings | `settings` | — |

Sidebar grouping: **Operate** (Overview, Accounts, Nodes, Leases) → **Consume** (Users, Requests) →
**Diagnose** (Logs) → footer (Settings). Group labels use `--font-micro`. Badges are `neutral`
counts, hidden at zero, capped at "99+".

## 2. Global conventions

### 2.1 URL is the state

Every list page keeps its filters in the query string so a link is shareable and the back button
works: `?q=&state=&platform=&page=&sort=&order=`. The page reads the URL on mount, writes on change
with `replace: true` (so typing in a search box does not fill the history). Drawers and modals are
**not** in the URL in v1 — a refresh closes them.

### 2.2 Data refresh

| Data | Strategy |
|---|---|
| Overview metrics | poll every 10 s, pause when the tab is hidden |
| Tables (accounts, nodes, leases, users) | poll every 15 s, pause when hidden, merge by id so selection survives |
| Requests | poll every 10 s while the range is 1 h or 24 h; manual refresh for 7 d/30 d |
| Logs (audit) | poll every 15 s; runtime stream via SSE `[PROPOSED]` |
| Health | poll every 10 s; three consecutive failures → the topbar "Reconnecting" pill |

A manual refresh is always available (`r`). Polling never shows a full-page skeleton after the
first load — only a subtle 1 px mint progress line under the topbar.

### 2.3 Formatting

| Kind | Rule | Example |
|---|---|---|
| Timestamps | absolute `YYYY-MM-DD HH:mm:ss` in the operator's locale, `--font-mono-sm`; relative on hover via `title` | `2026-10-23 09:58:11` |
| Durations | humanised, 2 significant units | `4 h 12 m`, `38 s` |
| Bytes | binary units, 1 decimal | `1.4 GiB` |
| Rates | per second, mono | `2.1 MB/s` |
| IPs / ports | mono, never wrapped | `203.0.113.44:13007` |
| IDs | mono, first 8 chars + `…`, full value in a CopyButton tooltip | `acc_7f3a91c2…` |
| State | StatusDot + the exact vocabulary from `INTERFACE.md` §State machines | `AUTH_FAILED` |

### 2.4 Error handling

| Backend code | UI surface | Copy |
|---|---|---|
| `INVALID_ARGUMENT` | field-level, under the offending input | the specific constraint (e.g. "Concurrency must be 1–2000") |
| `NOT_FOUND` | page-level inline block + Retry | "That item no longer exists" + link back to the list |
| `CONFLICT` | Modal, stays open | "Someone changed this while you were editing" + Reload |
| `UPSTREAM_UNAVAILABLE` | topbar pill + page banner | "Resin is unreachable — showing the last known state" + timestamp |
| `QUOTA_EXCEEDED` | inline on the action | "Monthly quota reached for this user" + link to Users |
| network / 5xx | toast (danger) + inline error block | "Request failed" + status + Retry |

A failed poll never clears the table: the last good data stays, dimmed to `--text-secondary`, with
a 12 px warning dot next to the page title and the last-good timestamp in its tooltip.

### 2.5 Table behaviour (all list pages)

Rows are 36 px, sortable, and selectable only on pages that have bulk actions (Accounts, Nodes).
Row click opens the page's Drawer; the row's action menu is a 32 px IconButton at the far right,
revealed on hover/focus. `Enter` opens the drawer for the focused row; `Space` toggles selection.
Above 500 rows the page switches to server-side pagination (50/100/200 per page, default 50) and
the footer shows `1–50 of 1,284`.

### 2.6 Command registry (feeds CommandPalette)

Every command has a stable id, a title, an optional subtitle and a scope. The palette shows
"Recent" (last 5 executed, `localStorage` key `ajs.recent`) then groups.

| Group | Commands |
|---|---|
| Navigate | Go to Overview / Accounts / Nodes / Leases / Users / Requests / Logs / Settings |
| Accounts | Add account · Force login · Logout · Edit account · Delete account |
| Nodes | Drain node · Undrain node · Probe node · Copy subscription line · Force reconcile |
| Leases | Rotate lease · Release lease |
| Users | Add user · Rotate user · Suspend user · Resume user |
| System | Refresh current page · Toggle sidebar · Open logs · Copy admin token prefix |
| Help | Keyboard shortcuts |

Commands that need a target open a **picker step** inside the palette (a second list of nodes or
accounts, filtered by typing) rather than navigating away. Destructive commands never execute
straight from the palette: they open the confirm Modal.

## 3. Overview — `/`

**Purpose:** answer "is the pool healthy right now, and what changed in the last hour?" in one
screen, with no scrolling at 1440 × 900.

### 3.1 Layout blocks

| # | Block | Height | Composition |
|---|---|---|---|
| 1 | Page header | 56 | title "Overview", subtitle with last-good timestamp, Refresh IconButton |
| 2 | Metric row | 120 | 4 MetricTiles: Live nodes, Active leases, Requests (24 h), Error rate |
| 3 | Chart + health | 320 | 2/3 area chart (ingress/egress/requests, 24 h, series 1–3) · 1/3 health Card |
| 4 | Needs attention | 240 max | DataTable, 5 rows, no pagination, "View all" ghost Button in the card header |
| 5 | Recent activity | 280 max | 8 audit rows, 36 px each, mono timestamp |

At 1024 the metric row becomes 3 tiles + the error-rate tile wraps under; chart and health stack
(chart 320, health auto). At 768 everything is one column and the chart drops to 200 px.

### 3.2 Data fields

| Block | Field | Type | Source |
|---|---|---|---|
| Metric 1 | live / total nodes | int / int | `GET /v1/nodes` `[SPEC]` |
| Metric 2 | active leases | int | `GET /v1/leases` `[PROPOSED]` |
| Metric 3 | requests 24 h | int + delta % | `GET /v1/overview/series?range=24h` `[PROPOSED]` |
| Metric 4 | error rate 24 h | % + delta | same |
| Chart | ts, ingress_bytes, egress_bytes, requests | timeseries, 5 min buckets | same |
| Health | `resin_up`, `db`, `process` | bool / enum / enum | `GET /healthz` `[SPEC]` |
| Health | accounts by state | map state→count | `GET /v1/accounts?limit=0` `[SPEC]` |
| Health | error classes | `[{class,count,last_at,last_detail}]` | `GET /v1/errors` `[SPEC]` |
| Attention | account label, state, last_error, since | row | `GET /v1/accounts?state=AUTH_FAILED` `[SPEC]` |
| Activity | ts, actor, action, subject | row | `GET /v1/audit?limit=8` `[SPEC]` |

### 3.3 Actions and states

| Action | Trigger | Endpoint | Result |
|---|---|---|---|
| Refresh all | `r`, topbar button | re-runs the five GETs | 1 px mint progress line, 400 ms minimum visible |
| Open account | row click | — | `/accounts?q=<label>` and open its Drawer |
| View all attention | card header | — | `/accounts?state=AUTH_FAILED` |
| Open log entry | row click | — | `/logs?subject=<id>` |
| Resin down | automatic | — | health Card shows `resin_up: false` in `--danger`, banner: "Showing last known state" |

### 3.4 Empty / loading / error

| Condition | Render |
|---|---|
| loading (first paint) | 4 skeleton tiles + 320 px chart skeleton + 5 skeleton rows, one pass; the topbar progress line shows |
| empty (fresh install) | EmptyState `inbox` "No accounts yet" + primary "Add account"; metric tiles render `0`, never hide |
| error (one block) | that block only shows an inline error with Retry; the rest of the page keeps working |
| error (all blocks) | page-level banner "Can't reach ajs-accountd" + Retry; the shell and nav stay usable |
| stale | values dim to `--text-secondary`, warning dot next to the title, last-good timestamp in the tooltip |

### 3.5 Shortcuts and commands

`r` refresh · `g o` navigate here · commands: "Refresh current page", "Force reconcile",
"Go to Accounts".

### 3.6 Endpoints

`GET /healthz` `[SPEC]` · `GET /v1/accounts` `[SPEC]` · `GET /v1/nodes` `[SPEC]` ·
`GET /v1/errors` `[SPEC]` · `GET /v1/audit` `[SPEC]` · `GET /v1/overview` `[PROPOSED]` ·
`GET /v1/overview/series?range=1h|24h|7d` `[PROPOSED]` · `GET /v1/leases` `[PROPOSED]`

`GET /v1/overview` `[PROPOSED]` returns
`{"live_nodes":n,"total_nodes":n,"active_leases":n,"requests_24h":n,"error_rate":0.012,"resin_up":true,"updated_at":"…"}`
so the first paint needs one request, not five.

## 4. Accounts — `/accounts`

**Purpose:** the vendor-side identity layer. Create an account, watch it log in, see why it failed,
retire it without leaving orphaned nodes.

### 4.1 Layout blocks

| # | Block | Height | Composition |
|---|---|---|---|
| 1 | Page header | 56 | title, count "12 accounts · 2 need attention", primary "Add account" |
| 2 | Filter bar | 48 | search 280 (label + username), state Select 160, platform Select 140, "Clear" ghost |
| 3 | DataTable | flex | 8 columns (below) |
| 4 | Pagination footer | 48 | `1–50 of N`, page size Select |

Columns: `Label` (flex 1, 500 weight) · `Username` (mono, 180) · `State` (StatusDot, 140) ·
`Concurrency` (mono, right, 100) · `Servers` (`allowed/total`, mono, 90) · `Expires` (120) ·
`Last error` (truncate, 220, `title` full) · actions (72).

### 4.2 Data fields

`GET /v1/accounts` `[SPEC]` → `{"accounts":[…],"total":n}`. Per account: `id`, `label`, `username`,
`state`, `concurrency`, `platform`, `servers_allow[]`, `regions[]`, `expires_at`, `last_error`,
`created_at`, `updated_at`. Secrets are never returned (`PANEL.md` §3).

Drawer (480 px, 5 Tabs):

| Tab | Content |
|---|---|
| Overview | label/username/state, concurrency, platform, expires, last error, created/updated |
| Session | `GET /v1/accounts/{id}/session` `[SPEC]`: state, `sid_prefix` (mono, 6 chars), `opened_at`, profile name/transport, sanitized `fields` as a 2-column mono table |
| Servers | `GET /v1/accounts/{id}/servers` `[SPEC]`: `fetched_at` + table (server id, title, region, status) |
| Profile | `GET`/`PATCH /v1/accounts/{id}/profile` `[SPEC]`: client_version, core_version, protocol_version Select (1/2), impl_type, oaep_hash Select, signing_mode Select, transport Select — validated, not free-form |
| Audit | `GET /v1/audit?subject=<id>` `[SPEC]`, last 20 |

### 4.3 Actions and states

| Action | Confirmation | Endpoint | Optimistic? |
|---|---|---|---|
| Add account | Modal 560, fields: label, username, password, concurrency (1–2000, default 32), platform, servers_allow (chips), regions (chips), expires_at | `POST /v1/accounts` `[SPEC]` → `201 {id, state:"LOGGING_IN"}` | no — closes on 201, row appears at top with a STARTING dot |
| Force login | none | `POST /v1/accounts/{id}/actions/login` `[SPEC]` → `202` | yes — dot → STARTING |
| Logout | Modal (drains nodes) | `POST /v1/accounts/{id}/actions/logout` `[SPEC]` → `202` | no |
| Edit | Drawer, inline | `PATCH /v1/accounts/{id}` `[SPEC]` | no |
| Rotate password | Modal with a password field | `PATCH /v1/accounts/{id}` `[SPEC]` | no |
| Delete | danger Modal, requires typing the label | `DELETE /v1/accounts/{id}` `[SPEC]` → `204` | no — row removed on 204 |

State vocabulary (`INTERFACE.md` §Account): `NEW`, `LOGGING_IN`, `AUTHENTICATED`, `SERVERS_OK`,
`READY`, `DRAINING`, `STOPPED`, `SUSPENDED`, `AUTH_FAILED`, `MEMBERSHIP_EXPIRED`. While `LOGGING_IN`
the row shows a 12 px spinner instead of a dot and the actions menu is disabled except Logout.
`AUTH_FAILED` rows sort to the top when the `state` filter is empty.

### 4.4 Empty / loading / error

- **Empty:** EmptyState `inbox`, "No accounts yet", body "An account is one set of vendor
  credentials. Add one to start a pool.", primary "Add account".
- **Loading:** 6 skeleton rows at 36 px; header and filter bar stay interactive.
- **Error:** table body becomes a 240 px error block; filters remain usable; Retry.
- **Empty after filtering:** EmptyState `search-x` + "Clear filters".

### 4.5 Shortcuts and commands

`/` focus search · `a` add account (page scope) · `Enter` open drawer · commands: "Add account",
"Force login", "Logout", "Edit account", "Delete account".

### 4.6 Endpoints

`GET /v1/accounts` `[SPEC]` · `POST /v1/accounts` `[SPEC]` · `GET|PATCH|DELETE /v1/accounts/{id}`
`[SPEC]` · `POST /v1/accounts/{id}/actions/login|logout` `[SPEC]` ·
`GET /v1/accounts/{id}/session|servers|profile` `[SPEC]` · `PATCH /v1/accounts/{id}/profile` `[SPEC]` ·
`GET /v1/accounts/{id}/usage?from=&to=` `[PROPOSED]`

## 5. Nodes — `/nodes`

**Purpose:** one row per `(account, server)` listener — the unit that actually carries traffic.
This is the page an operator lives on when something is wrong.

### 5.1 Layout blocks

| # | Block | Height | Composition |
|---|---|---|---|
| 1 | Page header | 56 | title, "N of M live", ghost "Force reconcile", primary "Probe all" |
| 2 | State strip | 72 | 5 inline counters with dots (LIVE / READY / STARTING / DRAINING / DOWN), click filters the table |
| 3 | Filter bar | 48 | search 280 (node id, region tag, egress ip), account Select 180, region Select 140, state Select 140 |
| 4 | DataTable | flex | 10 columns (below) |
| 5 | Publish footer | 40 | subscription hash (mono 8), `published_at`, "Copy line" CopyButton, publish age warning if > 60 s |

Columns: `Node` (mono, `acc1:12`, 140) · `Account` (140) · `Region tag` (mono, `cn-zj-03`, 120) ·
`State` (StatusDot, 130) · `Gate port` (mono, right, 90) · `Internal port` (mono, right, 100) ·
`Egress IP` (mono + CopyButton, 150) · `Latency` (mono, right, 80) · `Flows` (`7/64`, mono, 80) ·
actions (72).

### 5.2 Data fields

`GET /v1/nodes?account_id=&state=&region=` `[SPEC]` → `{"nodes":[…]}`. Per node: `id`, `account_id`,
`server_id`, `slot`, `state`, `egress_ip`, `region`, `fail_count`, `last_probe_at`, `last_ok_at`,
`active_flows`, `probe_age`. `GET /v1/nodes/{id}/flows` `[SPEC]` adds `active`, `limit`,
`oldest_age_s`, `reject_5m{concurrency,overload,drain}`.

Drawer (480 px):

| Section | Content |
|---|---|
| Header | node id (mono), region tag badge, StatusDot, "Copy line" |
| Flows | active/limit as a 4 px progress bar, `oldest_age_s`, reject counters as three small tiles |
| Probe | last 20 probe results as a Sparkline (latency) + last ok/error |
| Subscription line | `socks5://127.0.0.1:<gate_port>#<cc>-<subdiv>-<seq>` in a `--bg-sunken` mono block, CopyButton |
| Account link | label → `/accounts?q=<label>` |

### 5.3 Actions and states

| Action | Confirmation | Endpoint | Notes |
|---|---|---|---|
| Drain | Modal, grace Select (0 / 30 / 60 s, default 30) | `POST /v1/nodes/{id}/actions/drain` `[SPEC]` body `{"grace_seconds":30}` | row → DRAINING; in-flight flows keep running |
| Undrain | none | `POST /v1/nodes/{id}/actions/undrain` `[SPEC]` | only while the listener is open; `409` → "Listener already closed" |
| Probe | none | `POST /v1/nodes/{id}/actions/probe` `[SPEC]` | row action shows a 12 px spinner for the request duration; result toast on failure only |
| Probe all | Modal if > 20 nodes | same, fanned out 4 at a time | progress in the button label `Probing 7/23` |
| Copy line | none | — | CopyButton, "Copied" |
| Reconcile | none | `POST /v1/reconcile` `[SPEC]` | toast with the returned `actions[]` count |

Bulk: selection enables "Drain selected" and "Probe selected"; both go through one confirm Modal
listing the node ids. Draining is never bulk-applied without confirmation.

### 5.4 Empty / loading / error

- **Empty:** "No nodes yet" — body explains that nodes appear when an account reaches `SERVERS_OK`
  and a session opens; primary "Go to Accounts".
- **Loading:** 8 skeleton rows + the state strip shows `—`.
- **Error (`UPSTREAM_UNAVAILABLE`):** a 40 px banner above the table: "Resin unreachable — node
  states may be stale" + last-good timestamp; the table keeps its last data dimmed.
- **Partial:** rows whose account is `DOWN` show `—` for egress IP and latency rather than `0`.

### 5.5 Shortcuts and commands

`d` drain focused row · `p` probe focused row · `c` copy focused row's line · `Enter` open drawer ·
commands: "Drain node…", "Undrain node…", "Probe node…", "Copy subscription line",
"Force reconcile".

### 5.6 Endpoints

`GET /v1/nodes` `[SPEC]` · `POST /v1/nodes/{id}/actions/drain|undrain|probe` `[SPEC]` ·
`GET /v1/nodes/{id}/flows` `[SPEC]` · `GET /v1/subscription` `[SPEC]` · `POST /v1/reconcile`
`[SPEC]` · `GET /v1/topology` `[SPEC]` · `GET /v1/nodes/{id}` `[PROPOSED]` (drawer detail in one
call instead of three) · `GET /v1/nodes/{id}/probes?limit=20` `[PROPOSED]`

## 6. Leases — `/leases`

**Purpose:** a lease is the `(Platform, Account)` binding anchored on an egress IP — it is what
makes traffic sticky. This page shows who holds what, on which IP, and lets an operator break a
sticky binding on purpose.

### 6.1 Layout blocks

| # | Block | Height | Composition |
|---|---|---|---|
| 1 | Page header | 56 | title, "N leases · M stale", ghost "Reconcile" |
| 2 | Explainer | 64 | one-sentence definition + link to Settings → Publishing; dismissible per session |
| 3 | Summary row | 96 | 3 MetricTiles: leases held, stale (> TTL), orphaned (account gone) |
| 4 | Filter bar | 48 | search 280 (user, account, IP), user Select 180, platform Select 180, state SegmentedControl (All / Active / Stale / Orphan) |
| 5 | DataTable | flex | 8 columns |
| 6 | Footer note | 32 | "Rotation is rate-limited to one per user per 10 s" (`PANEL.md` §8) |

Columns: `User` (160) · `Platform` (mono, 160) · `Account` (140) · `Egress IP` (mono + CopyButton,
150) · `Region` (110) · `Acquired` (140) · `Age` (mono, 90) · actions (72).

### 6.2 Data fields

`GET /v1/leases` `[PROPOSED]` → `{"leases":[{user_id,user_name,platform_id,platform_name,account,egress_ip,egress_region,acquired_at,age_s,state}],"total":n}`.

Upstream, the same facts come from Resin: `GET /api/v1/platforms/{id}/leases/{account}` and
`GET /api/v1/nodes/{hash}` for `egress_ip`/`egress_region` (`PANEL.md` §4). The dashboard never
calls Resin directly — `ajs-accountd` holds the Resin admin token and must proxy. That is why the
list endpoint is `[PROPOSED]`: no aggregate exists in either backend document.

### 6.3 Actions and states

| Action | Confirmation | Endpoint | Notes |
|---|---|---|---|
| Rotate | Modal with "Avoid the current IP" Checkbox (default on) | `POST /v1/users/{id}/rotate` `[SPEC]` body `{"avoid_current_ip":true}` | operates on the **user**, not the lease: the account's lease is deleted and re-acquired. Rate limit 1/10 s per user → `409` with a countdown |
| Release | danger Modal naming user + IP | `DELETE /v1/leases/{user_id}/{account}` `[PROPOSED]` | upstream this is Resin's `DELETE /api/v1/platforms/{id}/leases/{account}`; the UI must not expose a raw Resin path |
| Copy IP | none | — | CopyButton |
| Filter to user | row menu | — | sets `?user=` |

States: `ACTIVE` (bound, within TTL), `STALE` (age > the platform's `sticky_ttl`, `--warning`),
`ORPHAN` (the account or user no longer exists, `--danger`). Stale and orphan rows sort to the top
by default and are never auto-deleted by the UI.

### 6.4 Empty / loading / error

- **Empty:** "No leases" — body: "A lease is created the first time a user's platform routes
  through an account. Send traffic, then come back." No primary action.
- **Loading:** 6 skeleton rows; summary tiles show `—`.
- **Error:** if the Resin call fails the page shows a `--warning` banner "Lease data is read from
  Resin, which is unreachable" and keeps the last list; Rotate/Release are disabled with a tooltip
  explaining why (never silently no-op).

### 6.5 Shortcuts and commands

`r` rotate focused row · `Enter` open the row's user drawer · commands: "Rotate lease…",
"Release lease…", "Go to Users".

### 6.6 Endpoints

`GET /v1/leases` `[PROPOSED]` · `DELETE /v1/leases/{user_id}/{account}` `[PROPOSED]` ·
`POST /v1/users/{id}/rotate` `[SPEC]` · `GET /v1/users/{id}/usage` `[SPEC]` (for the drawer) ·
`GET /v1/topology` `[SPEC]` (platform list for the filter)

## 7. Users — `/users`

**Purpose:** panel identities — the people (or services) that consume the pool. Each user maps to a
Resin Platform with its own sticky TTL, filters and quota.

### 7.1 Layout blocks

| # | Block | Height | Composition |
|---|---|---|---|
| 1 | Page header | 56 | title, "N users · M suspended", primary "Add user" |
| 2 | Filter bar | 48 | search 280 (name, platform), state SegmentedControl (All / Enabled / Suspended) |
| 3 | DataTable | flex | 8 columns |
| 4 | Quota legend | 32 | explains that quotas are advisory in v1 (`PANEL.md` §9 P3) |

Columns: `Name` (flex, 500) · `Platform` (mono, 160) · `Account identity` (140) ·
`Sticky TTL` (mono, 90) · `Quota` (progress bar + `3.2/50 GiB`, 180) · `Leases` (mono, 70) ·
`State` (StatusDot, 120) · actions (72).

### 7.2 Data fields

`GET /v1/users` `[SPEC]` (`PANEL.md` §3) → `{id, name, platform_name, account_identity, sticky_ttl,
quota_bytes_month, enabled, created_at}` plus derived `quota_used_bytes`, `lease_count`.
`GET /v1/users/{id}/usage?from=&to=&group_by=day` `[SPEC]` → bytes and requests per day.

Drawer (480 px, 4 Tabs):

| Tab | Content |
|---|---|
| Overview | name, platform, account identity, sticky TTL, created; enabled toggle |
| Usage | 30-day stacked area (ingress/egress) + a 7-row daily table; quota bar |
| Leases | that user's rows from `GET /v1/leases?user_id=` `[PROPOSED]` |
| API keys | `GET /v1/apikeys?user_id=` `[PROPOSED]` (list is not in either backend doc); issue and revoke |

### 7.3 Actions and states

| Action | Confirmation | Endpoint | Notes |
|---|---|---|---|
| Add user | Modal 560: name, platform_name (auto-suggested slug), account_identity, sticky_ttl Select (1 h/8 h/24 h/7 d), quota (GiB, optional) | `POST /v1/users` `[SPEC]` | platform is created upstream on first publish |
| Edit | Drawer inline | `PATCH /v1/users/{id}` `[SPEC]` | sticky_ttl and quota only; name is immutable in v1 |
| Rotate | Modal, "Avoid current IP" default on | `POST /v1/users/{id}/rotate` `[SPEC]` | 409 → countdown |
| Suspend | Modal explaining that all requests fail closed (`regex_filters=["!.*"]`) | `POST /v1/users/{id}/suspend` `[SPEC]` | row → SUSPENDED, `--warning` |
| Resume | none | same endpoint, `{"on":false}` `[PROPOSED]` | the spec only names suspend; resume needs the inverse |
| Delete | danger Modal, requires typing the name | `DELETE /v1/users/{id}` `[SPEC]` | drains leases first, disables the Platform |
| Issue API key | Modal, shows the secret once with a CopyButton | `POST /v1/apikeys` `[SPEC]` | the key is never retrievable again; the modal says so |
| Revoke key | danger Modal | `DELETE /v1/apikeys` `[SPEC]` | — |

States: `ENABLED` (success), `SUSPENDED` (warning). A user with `quota_used ≥ 80 %` gets a
`--warning` quota bar; `≥ 100 %` a `--danger` bar. Quota enforcement is advisory in v1 — the UI
must label it "advisory" in the tooltip, not imply a hard block.

### 7.4 Empty / loading / error

- **Empty:** "No users yet" — "A user is a Resin Platform with its own lease and quota." primary
  "Add user".
- **Loading:** 5 skeleton rows.
- **Error:** per-row quota fetch failure shows `—` in the quota column, not `0 %`; the rest of the
  row stays usable.
- **Quota unknown** (no `quota_bytes_month`): show "Unlimited" in `--text-tertiary`.

### 7.5 Shortcuts and commands

`u` add user · `Enter` open drawer · commands: "Add user", "Rotate user…", "Suspend user…",
"Resume user".

### 7.6 Endpoints

`GET|POST /v1/users` `[SPEC]` · `PATCH|DELETE /v1/users/{id}` `[SPEC]` ·
`POST /v1/users/{id}/rotate` `[SPEC]` · `POST /v1/users/{id}/suspend` `[SPEC]` ·
`GET /v1/users/{id}/usage` `[SPEC]` · `POST|DELETE /v1/apikeys` `[SPEC]` ·
`POST /v1/users/{id}/resume` `[PROPOSED]` · `GET /v1/apikeys?user_id=` `[PROPOSED]` ·
`GET /v1/users/{id}` `[PROPOSED]` (single-user read; the spec has no GET-by-id)

## 8. Requests — `/requests`

**Purpose:** what actually flowed. Answers "who used how much, through which node, and what got
rejected" without reading logs.

### 8.1 Layout blocks

| # | Block | Height | Composition |
|---|---|---|---|
| 1 | Page header | 56 | title, range SegmentedControl (1 h / 24 h / 7 d / 30 d), Refresh |
| 2 | Metric row | 96 | 4 compact tiles: requests, ingress, egress, rejected |
| 3 | Chart | 280 | stacked area by user (top 5 + Other), or by series 1–5 when "by account" is chosen |
| 4 | Reject strip | 56 | three inline counters: concurrency, overload, drain (`reject_5m`) with 5 m sparklines |
| 5 | Filter bar | 48 | search 280 (target host), user Select, account Select, node Select, status Select |
| 6 | DataTable | flex | 10 columns |
| 7 | Footer | 40 | "Aggregates are reconciled from Resin request logs every 5 min" |

Columns: `Time` (mono, 140) · `User` (120) · `Account` (120) · `Node` (mono, 110) · `Target`
(mono, truncate, flex) · `Status` (badge: 2xx success / 3xx info / 4xx warning / 5xx danger / `REJECT`
danger, 90) · `Ingress` (mono, right, 90) · `Egress` (mono, right, 90) · `Latency` (mono, right,
80) · `Reason` (truncate, 140).

### 8.2 Data fields

`GET /v1/requests` `[PROPOSED]` →
`{"requests":[{ts,user_id,user_name,account_id,node_id,target_host,target_port,status_code,reject_reason,ingress_bytes,egress_bytes,duration_ms}],"total":n,"next_cursor":"…"}`.

`GET /v1/requests/summary?range=` `[PROPOSED]` →
`{"requests":n,"ingress_bytes":n,"egress_bytes":n,"rejected":n,"by_user":[…],"series":[…]}`.

Field names are proposed: Resin's `GET /api/v1/request-logs?platform_id=&account=&from=&to=`
(`PANEL.md` §4) is the upstream source but its response shape is not recorded in any document in
this repo. The backend must pin it before the table ships. Marked `[UNPROVEN]` until then.

### 8.3 Actions and states

| Action | Trigger | Endpoint | Notes |
|---|---|---|---|
| Change range | SegmentedControl | refetch with `?range=` | 1 h/24 h poll; 7 d/30 d manual only |
| Group by | Select in the chart header (user / account / node) | refetch | chart only; the table is unaffected |
| Inspect | row click | — | Drawer 420: full request facts, node link, user link, raw JSON in a mono block |
| Copy target | row menu | — | CopyButton |
| Export CSV | card header ghost button | `GET /v1/requests/export?range=&format=csv` `[PROPOSED]` | streams a download; disabled above 100 k rows with a hint to narrow the range |

### 8.4 Empty / loading / error

- **Empty:** "No requests in this range" + a hint to widen the range; never an illustration.
- **Loading:** chart skeleton 280 px + 8 skeleton rows; the metric row shows `—`.
- **Error:** chart and table fail independently. A `UPSTREAM_UNAVAILABLE` banner explains that
  request logs come from Resin.
- **Stale:** if `published_at` is older than 5 min the footer says "Aggregates are 12 min old".

### 8.5 Shortcuts and commands

`1` `2` `3` `4` switch range · `/` filter · commands: "Refresh current page",
"Go to Requests", "Export requests…".

### 8.6 Endpoints

`GET /v1/requests` `[PROPOSED]` · `GET /v1/requests/summary` `[PROPOSED]` ·
`GET /v1/requests/export` `[PROPOSED]` · `GET /v1/users/{id}/usage` `[SPEC]` ·
`GET /v1/errors?class=&since=` `[SPEC]` (reject strip) · `GET /v1/nodes/{id}/flows` `[SPEC]`

## 9. Logs — `/logs`

**Purpose:** the diagnostic surface. Two distinct data sets live here and must not be mixed:
the append-only **audit** trail (who did what) and the **runtime** stream (what the daemon printed).

### 9.1 Layout blocks

| # | Block | Height | Composition |
|---|---|---|---|
| 1 | Page header | 56 | title, source Tabs (Audit / Runtime), right-side actions per tab |
| 2 | Filter bar | 48 | Audit: search 280 (actor, action, subject), action Select, range Select · Runtime: level Select (DEBUG/INFO/WARN/ERROR), component Select, "Follow" toggle |
| 3 | Content | flex | Audit: DataTable 5 columns · Runtime: mono log pane, virtualised, 20 px lines |
| 4 | Footer | 40 | Audit: "Append-only. N events." · Runtime: "Streaming · 42 lines/s" or "Paused" |

Audit columns: `Time` (mono, 160) · `Actor` (140, `system` in `--text-tertiary`) · `Action` (mono,
200) · `Subject` (mono + CopyButton, 180) · `Detail` (flex, truncate, expands on click into a
`--bg-sunken` mono block).

Runtime pane: each line is `HH:mm:ss.SSS  LEVEL  component  message`, level coloured
(`DEBUG` `--text-tertiary`, `INFO` `--text-secondary`, `WARN` `--warning`, `ERROR` `--danger`),
message `--font-mono-sm`. Matching text is highlighted with a mint 24 % background.

### 9.2 Data fields

`GET /v1/audit?limit=&subject=` `[SPEC]` → `{events:[{id,ts,actor,action,subject,detail}]}`.
`PANEL.md` §6 defines the table: `audit(id, ts, actor, action, subject, detail)`.

`GET /v1/logs?level=&component=&since=&limit=` `[PROPOSED]` →
`{"lines":[{"ts","level","component","message","fields":{}}],"next_cursor":"…"}`.
`GET /v1/logs/stream?level=` `[PROPOSED]` — Server-Sent Events, one JSON line per event, heartbeat
every 15 s.

Neither runtime endpoint exists in the backend documents; `PANEL.md` only mentions `/metrics` and
the `audit` table. The runtime pane therefore ships behind a feature flag until the endpoint lands.

### 9.3 Actions and states

| Action | Trigger | Endpoint | Notes |
|---|---|---|---|
| Filter audit | filter bar | `GET /v1/audit` | client-side for the loaded page; server-side `subject` only |
| Expand detail | row click | — | inline expansion, not a drawer |
| Follow | toggle (default on) | — | auto-scroll; scrolling up turns it off and shows a "Jump to latest" pill |
| Pause | `Space` while the pane is focused | — | stops appending; the buffer keeps filling |
| Copy line | row/line menu | — | CopyButton copies the raw line |
| Open in context | row menu | — | navigates to the subject's page (`/accounts?q=`, `/nodes?q=`) |

### 9.4 Empty / loading / error

- **Empty (audit):** "No events yet" — "Actions you take in this console are recorded here."
- **Empty (runtime):** "No log lines match" + "Clear filters".
- **Loading:** audit 8 skeleton rows; runtime a 3-line skeleton with a blinking caret.
- **Stream dropped:** the footer switches to "Disconnected — retrying in 5 s" in `--warning`; the
  pane keeps its buffer and reconnects with `Last-Event-ID`.
- **Error:** audit falls back to a plain error block; runtime shows the last 200 lines it has.

### 9.5 Shortcuts and commands

`/` filter · `f` toggle follow · `Space` pause · `Esc` clear filters · commands: "Open logs",
"Go to Logs".

### 9.6 Endpoints

`GET /v1/audit` `[SPEC]` · `GET /v1/errors` `[SPEC]` · `GET /v1/logs` `[PROPOSED]` ·
`GET /v1/logs/stream` `[PROPOSED]` · `GET /metrics` `[SPEC]` (a "Raw metrics" link opens the text
exposition in a new tab, loopback only)

## 10. Settings — `/settings`

**Purpose:** the operator's control surface for the daemon's runtime knobs, API keys, and the
irreversible operations. Everything here maps to a row in `PANEL.md` §7 / `INTERFACE.md` §Config.

### 10.1 Layout blocks

| # | Block | Height | Composition |
|---|---|---|---|
| 1 | Page header | 56 | title, "Settings apply without a restart unless marked", Save bar appears when dirty |
| 2 | Tabs | 36 | General · Publishing · Canary · API keys · Danger |
| 3 | Form card | auto | max-width 720, one Card per group, 20 px between fields |
| 4 | Sticky save bar | 64 | appears on first change: "3 unsaved changes" + Discard ghost + Save primary |
| 5 | Danger card | auto | `--border-danger`, destructive actions only |

### 10.2 Fields per tab

**General**

| Field | Control | Validation | Source |
|---|---|---|---|
| Listen address | Input mono, read-only + CopyButton | — | `AJS_LISTEN` (`127.0.0.1:2261`) |
| Resin URL | Input mono | absolute URL, loopback or LAN | `AJS_RESIN_URL` |
| State directory | Input mono, read-only | — | `AJS_STATE_DIR` |
| Default account concurrency | Input number 1–2000 | integer | `AJS_ACCOUNT_CONCURRENCY` (32) |
| Node concurrency | Input number 1–4096 | integer | `AJS_NODE_CONCURRENCY` (64) |
| Global concurrency | Input number 1–65536 | integer | `AJS_GLOBAL_CONCURRENCY` (512) |
| Admission timeout | Input + unit Select (s) | 1–60 | `AJS_ADMISSION_TIMEOUT` (5s) |
| Log level | Select DEBUG/INFO/WARN/ERROR | — | `[PROPOSED]` (not in the env table) |

**Publishing**

| Field | Control | Validation | Source |
|---|---|---|---|
| Publish interval | Input + Select (5–300 s) | ≥5 | `AJS_PUBLISH_MIN_INTERVAL` (5s) |
| Allow empty publish | Checkbox (danger-tinted helper: "An empty publish evicts the pool") | — | `AJS_ALLOW_EMPTY_PUBLISH` (0) |
| Subscription name | Input mono, read-only | — | `ajs-live` (`PANEL.md` §4) |
| Current hash / published_at | read-only mono + CopyButton | — | `GET /v1/subscription` `[SPEC]` |
| Force reconcile | ghost Button | — | `POST /v1/reconcile` `[SPEC]` |

**Canary**

| Field | Control | Validation | Source |
|---|---|---|---|
| Interval | Input + Select (10–600 s) | ≥10 | `AJS_CANARY_INTERVAL` (60s) |
| Target | Input mono `host:port` | must parse, port 1–65535 | `AJS_CANARY_TARGET` (`www.cloudflare.com:443`) |
| Login backoff | read-only, "30 s → 30 min" | — | `AJS_LOGIN_BACKOFF` |
| Probe all now | ghost Button | confirm if > 20 nodes | `POST /v1/nodes/{id}/actions/probe` `[SPEC]` |

**API keys**

Table: `Prefix` (mono, `ajs_9f2c…`) · `User` · `Created` · `Last used` `[PROPOSED]` · Revoke.
Primary "Issue key" opens a Modal that shows the secret **once** in a `--bg-sunken` mono block with
a CopyButton and the line "This is the only time you will see this key."

**Danger**

| Action | Confirmation | Endpoint |
|---|---|---|
| Drain every node | Modal, type `DRAIN ALL` | `POST /v1/nodes/{id}/actions/drain` per node, fanned out `[SPEC]` |
| Delete all users | Modal, type `DELETE USERS` | `DELETE /v1/users/{id}` per user `[SPEC]` |
| Rotate the master key | disabled in v1 — a note explains it needs a restart and a re-seal migration | — |

### 10.3 Data fields

`GET /v1/settings` `[PROPOSED]` → `{"listen","resin_url","state_dir","account_concurrency",
"node_concurrency","global_concurrency","admission_timeout_s","publish_min_interval_s",
"allow_empty_publish","canary_interval_s","canary_target","log_level","updated_at"}`.
`PATCH /v1/settings` `[PROPOSED]` accepts a partial object and returns the full one; unknown keys are
`INVALID_ARGUMENT`. **No settings endpoint exists in either backend document** — the values are
environment variables today (`PANEL.md` §7). Until it lands the page renders every field read-only
from `GET /healthz` + `GET /v1/subscription` and the save bar is absent.

API key rows: `id`, `prefix` (mono, `ajs_9f2c…`), `user_id`, `created_at`, `last_used_at`
`[PROPOSED]`, `revoked_at`.

### 10.4 Actions and states

| Action | Trigger | Endpoint | State after |
|---|---|---|---|
| Save | `⌘S`, save-bar primary | `PATCH /v1/settings` `[PROPOSED]` | saving → saved (1200 ms) |
| Discard | save-bar ghost, `Esc` | — | dirty → clean; confirm only if >1 field changed |
| Force reconcile | Publishing tab | `POST /v1/reconcile` `[SPEC]` | button loading, toast with `actions[]` count |
| Probe all now | Canary tab | `POST /v1/nodes/{id}/actions/probe` `[SPEC]` | per-node spinner, summary toast |
| Issue key | API keys primary | `POST /v1/apikeys` `[SPEC]` | modal shows the secret once |
| Revoke key | row action | `DELETE /v1/apikeys` `[SPEC]` | row removed, confirm first |
| Drain all / Delete all users | Danger tab | per-item endpoints `[SPEC]` | type-to-confirm modal, then a progress list |

- **Dirty:** save bar slides up 250 ms; `⌘S` saves; `Esc` discards after a confirm if > 1 field.
- **Saving:** primary button loading, all fields disabled.
- **Saved:** the save bar shows "Saved" for 1200 ms then collapses; a toast confirms only if the
  change affects routing (publish interval, allow-empty, canary target).
- **Validation error:** field-level message + the save bar keeps the count; `INVALID_ARGUMENT`
  from the server maps back onto the field named in `message`.

### 10.5 Empty / loading / error

| Condition | Render |
|---|---|
| loading | form skeleton: 6 label+control pairs at 32 px, tabs visible but inert |
| empty (no API keys) | "No keys issued" + primary "Issue key" |
| read-only (no settings endpoint) | every control disabled with an 11 px note "Set via environment variable" and the env name in mono; the save bar is absent |
| error (`UPSTREAM_UNAVAILABLE`) | banner at the top of the form: "Resin unreachable — publish settings can't be verified"; Save disabled |
| save failure | inline error above the save bar, fields keep their values, the bar stays visible |

### 10.6 Shortcuts and commands

`⌘S` save · `Esc` discard · commands: "Go to Settings", "Force reconcile", "Issue API key",
"Copy admin token prefix".

### 10.7 Endpoints

`GET /v1/settings` `[PROPOSED]` · `PATCH /v1/settings` `[PROPOSED]` ·
`GET /v1/apikeys` `[PROPOSED]` · `POST|DELETE /v1/apikeys` `[SPEC]` ·
`POST /v1/reconcile` `[SPEC]` · `POST /v1/nodes/{id}/actions/probe` `[SPEC]` ·
`GET /v1/subscription` `[SPEC]` · `GET /healthz` `[SPEC]`

## 11. v1 tradeoffs

Explicit cuts, each with the cost and the trigger that would reverse it. Anything not on this list
is in scope.

| # | Tradeoff | Why | Cost | Revisit when |
|---|---|---|---|---|
| 1 | **Polling, not push.** 10–15 s intervals; SSE only for runtime logs (flagged). | `ajs-accountd` has no event bus; a poll is one GET and no new server code. | Up to 15 s stale; a burst of 6 requests per open tab. | When >10 concurrent operators or a node flaps faster than the poll. |
| 2 | **Leases page is aggregate-only.** One list endpoint `[PROPOSED]`; no per-platform lease editing. | Resin owns lease lifecycle; duplicating it invites drift. | Cannot pin a lease to a specific node from the UI. | When single-socket node switching becomes a requirement (`PANEL.md` §10, hybrid D). |
| 3 | **Quotas are advisory.** Shown as a bar, enforced by nothing in v1. | Per-user enforcement is P3 and explicitly out of scope (`PANEL.md` §9). | A user can exceed the displayed quota. | When the gateway lands. |
| 4 | **No request-log export above 100 k rows.** | Export streams server-side and has no cursor in v1. | Operators narrow the range. | When `/v1/requests/export` gains a cursor. |
| 5 | **Tables paginate above 500 rows; no virtualisation.** | Virtualised tables complicate sticky headers, column resize and a11y in one go. | A 5 k-row audit view needs paging. | When the audit table exceeds 500 rows in normal use. |
| 6 | **Drawers/modals are not in the URL.** | Keeps routing flat and avoids a modal-router dependency. | A refresh closes the drawer; no deep link to one account. | When support workflows need shareable deep links. |
| 7 | **Dark only.** | One palette, verified once (§3.7). | No light theme for bright rooms. | Owner asks. |
| 8 | **Runtime logs behind a flag.** | No `/v1/logs` in either backend document. | Logs page ships with Audit only. | When the daemon exposes a stream. |
| 9 | **Settings read-only until `/v1/settings` exists.** | Config is env-var only today (`PANEL.md` §7). | Restart + env edit to change a knob. | When a settings endpoint lands. |
| 10 | **No bulk account import.** | CSV import needs a validation/partial-failure story the API does not have. | One modal per account. | When >20 accounts are common. |
| 11 | **English only, no i18n framework.** | One operator. | — | Owner asks. |
| 12 | **No gateway surface.** | `ajs-gateway` is conditional (`PANEL.md` §10). | Per-user passwords/IP allowlists are not visible anywhere. | If hybrid D is chosen. |

Two things that look like cuts but are **not**: every page has a real empty state and a real error
state (not a toast), and every destructive action has a confirmation that names its target.

## 12. Endpoint index

`[SPEC]` = named in `PANEL.md` §3/§4 or `INTERFACE.md` §HTTP. `[PROPOSED]` = introduced here.
Nothing is implemented as of 2026-10-23.

| Endpoint | Method | Status | Used by |
|---|---|---|---|
| `/healthz` | GET | `[SPEC]` | Overview, Settings, topbar |
| `/metrics` | GET | `[SPEC]` | Logs (raw link) |
| `/v1/accounts` | GET, POST | `[SPEC]` | Overview, Accounts |
| `/v1/accounts/{id}` | GET, PATCH, DELETE | `[SPEC]` | Accounts |
| `/v1/accounts/{id}/actions/login` | POST | `[SPEC]` | Accounts |
| `/v1/accounts/{id}/actions/logout` | POST | `[SPEC]` | Accounts |
| `/v1/accounts/{id}/session` | GET | `[SPEC]` | Accounts drawer |
| `/v1/accounts/{id}/servers` | GET | `[SPEC]` | Accounts drawer |
| `/v1/accounts/{id}/profile` | GET, PATCH | `[SPEC]` | Accounts drawer |
| `/v1/accounts/{id}/usage` | GET | `[PROPOSED]` | Accounts drawer |
| `/v1/nodes` | GET | `[SPEC]` | Overview, Nodes |
| `/v1/nodes/{id}/actions/drain` | POST | `[SPEC]` | Nodes, Settings |
| `/v1/nodes/{id}/actions/undrain` | POST | `[SPEC]` | Nodes |
| `/v1/nodes/{id}/actions/probe` | POST | `[SPEC]` | Nodes, Settings |
| `/v1/nodes/{id}/flows` | GET | `[SPEC]` | Nodes drawer, Requests |
| `/v1/nodes/{id}` | GET | `[PROPOSED]` | Nodes drawer |
| `/v1/nodes/{id}/probes` | GET | `[PROPOSED]` | Nodes drawer |
| `/v1/leases` | GET | `[PROPOSED]` | Overview, Leases, Users |
| `/v1/leases/{user_id}/{account}` | DELETE | `[PROPOSED]` | Leases |
| `/v1/users` | GET, POST | `[SPEC]` | Users |
| `/v1/users/{id}` | PATCH, DELETE | `[SPEC]` | Users |
| `/v1/users/{id}` | GET | `[PROPOSED]` | Users drawer |
| `/v1/users/{id}/rotate` | POST | `[SPEC]` | Leases, Users |
| `/v1/users/{id}/suspend` | POST | `[SPEC]` | Users |
| `/v1/users/{id}/resume` | POST | `[PROPOSED]` | Users |
| `/v1/users/{id}/usage` | GET | `[SPEC]` | Users drawer, Requests |
| `/v1/subscription` | GET | `[SPEC]` | Nodes footer, Settings |
| `/v1/reconcile` | POST | `[SPEC]` | Nodes, Settings, palette |
| `/v1/topology` | GET | `[SPEC]` | Nodes, Leases filters |
| `/v1/errors` | GET | `[SPEC]` | Overview, Requests, Logs |
| `/v1/audit` | GET | `[SPEC]` | Overview, Accounts drawer, Logs |
| `/v1/apikeys` | POST, DELETE | `[SPEC]` | Users, Settings |
| `/v1/apikeys` | GET | `[PROPOSED]` | Settings |
| `/v1/overview` | GET | `[PROPOSED]` | Overview |
| `/v1/overview/series` | GET | `[PROPOSED]` | Overview |
| `/v1/requests` | GET | `[PROPOSED]` | Requests |
| `/v1/requests/summary` | GET | `[PROPOSED]` | Requests |
| `/v1/requests/export` | GET | `[PROPOSED]` | Requests |
| `/v1/logs` | GET | `[PROPOSED]` | Logs |
| `/v1/logs/stream` | GET (SSE) | `[PROPOSED]` | Logs |
| `/v1/settings` | GET, PATCH | `[PROPOSED]` | Settings |

Count: 25 `[SPEC]` rows, 16 `[PROPOSED]` rows (41 total). The backend must either implement the
`[PROPOSED]` set or the corresponding UI block ships disabled with the reason visible — never
silently empty.

## 13. Page acceptance

Each page is done when all of these hold:

1. It renders at 1440, 1024 and 768 with no horizontal scrollbar and no clipped control.
2. Every block in its §x.1 table exists, in that order, at the stated height (±4 px).
3. All three of empty / loading / error are reachable without editing code (the error one via a
   mock or a stopped backend).
4. Every action in its "Actions and states" table works, shows a pending state, and reports failure
   inline.
5. Every keyboard entry in its "Shortcuts and commands" section works and is listed in the `?`
   shortcut sheet.
6. Its endpoints are called exactly as listed, with the `[SPEC]`/`[PROPOSED]` status respected.
7. axe-core reports zero violations at AA on the page in all three viewports.

