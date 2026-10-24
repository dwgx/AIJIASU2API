# LovePlusSpeedToProxy

Reverse-engineering research repository for the **AJiaSu / 爱加速** client, with the goal of
reimplementing its protocol so the service can be driven as a **proxy API with a selectable
egress IP**.

## What this repository is

A research corpus plus the artefacts built on it: an operator console (`dashboard/`), a
control-plane daemon skeleton (`daemon/`), and a compose deployment for the pair (`deploy/`). It
records what is known about the AJiaSu client — its installer payload, its Windows and Linux
binaries, its `FV*` protocol stack, its login/node-selection flow — and implements the parts that
do not require a vendor account.

It is **not** a client, not a proxy service, and not a distribution of vendor software. No vendor
binaries, drivers, installers, packet captures, or memory dumps are committed here; `.gitignore`
blocks them at the repository root. Live vendor contact has happened on two days only
(2026-10-23, two logins; 2026-10-24, one login after the rebuild — all on one Owner-supplied
account; the 2026-10-23 run is in
[`research/82-live-20261023/REPORT.md`](research/82-live-20261023/REPORT.md)), and no vendor
artifact or credential from those runs is committed.

## Current status

**Offline emulator, dashboard and daemon, plus a control-plane compose stack in `deploy/`; the
Resin M1 integration spike is PASS. Live vendor runs on 2026-10-23 and 2026-10-24 (three logins,
one account): original app and daemon egress parity through one node, device identity stable
across logins; the `2261` deployment now runs login-enabled with that account active and is
demo-free (one account, one node; gate port read from `/v1/lab/gates`). The platform wave — scoped
keys (including customer-scoped read scopes), the device-pool HTTP API, renewal, flow-metered
usage, webhooks with an SSRF policy, loopback gate health and passphrase-encrypted panel
backup/restore — is served by the daemon and covered offline, but not yet exercised against the
vendor. Panel state survives a restart only as a versioned JSON snapshot, not SQLite.**

As of 2026-10-24:

- `emulator/` — offline Tier-2 `FV` emulator: control API, FV crypto/frame, SOCKS5/HTTP front ends
  and the captured proxy carrier. It builds and passes its offline conformance tests; it makes no
  live calls and holds no credentials.
- `dashboard/` — **APC** (AJiaSu Proxy Console, powered by dwgx), the operator console: React 19,
  Vite 6, TypeScript, 11 pages (Overview, Usage, Accounts, Nodes, Leases, Users, Requests, Logs,
  Schedules, Settings, Lab) in a graphite-and-mint design system with accent themes
  (Mint/Indigo/Violet/Orange), a table/cards switch for Accounts, and command-palette search for
  accounts and nodes. The sidebar
  collapses to a 56 px icon rail (`localStorage["ajs.sidebar"]`); Accounts' **Add account** flow
  is a centered modal (concurrency fixed at 1); Nodes is the real vendor catalog grouped by region:
  2,814 servers for the live `acc-07` account, server-paged, filtered and sorted through
  `/v1/accounts/{id}/servers` (`q`, `region`, `status`, `province`, `city`, `sort`, `offset`,
  `limit`; 50 rows by default, 500 max) with region names localised (`cn` → China / 中国) and the
  raw vendor code kept as secondary text. The Catalog also draws an offline China map — 31
  provinces from committed DataV GeoAtlas-derived assets, so no map or tile service is contacted
  at runtime — and a province click filters the table. Province and city counts arrive in the same
  catalog response (`geo`, resolved by the daemon's exact-match city index), so the map needs no
  full-catalog download; a daemon without it falls back to paging the retained catalog in the
  browser. A row carries the account's target and running state, so the operator can pin a target
  (starts no session), then connect, reconnect or disconnect under a confirm; the switch keeps one
  session per account and rejects a stale `snapshotId` or a repeat inside the 5 s cooldown. It
  merges each account's login-retained catalog with the running node marked current; Requests is
  the real gate-flow table fed by
  `/v1/lab/flows?limit=200` (host-only targets, 5 s poll) next to the mutation-lab history, and
  Requests, Logs and the Lab tables share a pagination footer. Overview draws no synthetic
  traffic curve — when the daemon reports `telemetryAvailable:false` it shows a no-telemetry empty
  state beside the real counts. Mock data is the default; a live mode against `ajs-accountd` is
  opt-in at build time.
  Form controls are one custom combobox (`dashboard/src/components/Select.tsx`, no native
  `<select>`) with filter, form, settings, log and region variants; the Nodes drawer leads with the
  localised region, renders an unmeasured success rate as *Not measured*, flags a far-future probe
  stamp as clock skew, labels the egress IP as a vendor claim and offers a guarded
  **Verify egress** check. 330 Vitest tests in 37 files. The UI is bilingual: a `中` / `EN` switch
  in the sidebar footer and the Language section in Settings switch every page, default to the browser
  language, persist in `localStorage["ajs.lang"]` and set `<html lang>` (`zh-CN` / `en`); account
  labels, server names, IPs and API values are data and are never translated. Settings is an
  eight-section shell (General, Appearance, Language, Connection, Leases, Updates, Integrations,
  Audit) with a sticky rail, dirty save/reset and honest **Stored only** / **Coming later** states
  for controls the daemon does not read yet; Integrations now carries the live webhook registry
  (one-time signing secret, enable/disable, per-endpoint delivery ring). HMC serves the mock
  build on `18081` and the live build on `18082`, the latter published on the LAN at
  `http://192.168.11.4:18082` against the daemon API at `http://192.168.11.4:2261`, where the
  bearer token is required (insecure mode is refused off loopback).
  The **Lab** page (`/lab`) is an instrument with eight tabs — Overview, Gates & sessions, Egress,
  Servers, Protocol, Device, Logs, Diagnostics — fed by `/v1/lab/*` plus the per-account
  servers/device reads. Every GET is read-only and makes no vendor call; the only non-GET action is
  the guarded egress check (`POST /v1/lab/egress-check`) through the selected node's own loopback
  gate — a real SOCKS5 dial plus HTTPS echo, rate-limited by a 5 s per-node cooldown, answering 409
  when no gate is running and 429 on a repeat within the cooldown. The verified result is merged
  into that node's claimed-vs-verified row and kept per node, so a GET poll, a manual refresh or a
  node switch does not erase it; a full page reload starts from claims again. A disabled mode
  answers `enabled:false` with the switch that is off. The build-time token is optional: on a live
  401 the runtime token gate asks for the admin token and keeps it in `sessionStorage` for that tab
  only.
  The platform wave adds a `/usage` page (7/30/90/366-day window over `/v1/usage` with
  previous-window deltas, a zero-filled per-day chart, per-account and per-user tables with
  advisory quota meters, top targets, and a retention note built from `rowsRetained`/
  `retentionDays`), a `/schedules` page (cron/interval/once schedules with a pure
  next-run preview, `ARM`/`RUN` confirmations, an `If-Match` revision retry, run history and the
  read-only renewal-sweep card), a gate-health panel on Overview (compact) and Nodes → Connected
  (full) fed by the daemon's loopback sampler, and a live webhook registry in Settings →
  Integrations (one-time signing secret, enable/disable, per-endpoint delivery ring).
  Accounts exports the selected, filtered or all accounts as an encrypted `.ajsx` envelope
  (passphrase-derived key, device identity optional), imports JSON, CSV or encrypted files with a
  dry-run preview and an atomic or partial apply, and bulk-deletes up to 100 accounts with per-row
  results.
  Plaintext export stays behind `AJS_ALLOW_PLAINTEXT_EXPORT=1` and an `EXPORT` confirmation, and
  an encrypted export is always available. A read-only renewal drawer shows the vendor's public
  price snapshot, its official purchase link and a countdown; no payment is automated and no token
  rides the link. Theme, language, sidebar-collapse and node-state glyphs morph with a spring
  (an MIT-attributed adapted morphicons core, instant swap under `prefers-reduced-motion`).
- `daemon/` — Go 1.26 `ajs-accountd` skeleton: the dashboard's JSON contract over an in-memory
  store persisted as a JSON snapshot, a Resin v1.3.0 admin client, publisher and runtime wiring
  (local subscription render, platform create/update, lease rotate through platform filter
  exclusion, node drain/undrain, probe-config patch), an AES-256-GCM sealed secret file that
  `DELETE /v1/accounts/{id}` prunes from both the snapshot map and `secrets.json` before the
  in-memory cascade (a sealer failure answers 503 and leaves the account and its secret in place),
  a Prometheus `/metrics` endpoint, a Lab telemetry view (`/v1/lab/*`: overview, flags, gates,
  sessions, egress, profiles, bounded redacted log ring, diagnostics, plus per-account
  servers/device; the GETs are read-only and one POST (`/v1/lab/egress-check`) runs an explicit
  egress probe that stores nothing), and the account control, FV session factory, node controller,
  SOCKS5 gate and process-group kill with `WaitDelay` supervisor seams. The P1 control surface is
  served too: account export/import/bulk-delete, catalog selection with guarded
  connect/reconnect/disconnect (one session per account, per-account cooldown, `snapshotId`
  guard), and a cron/interval/once scheduler behind `AJS_SCHEDULES=1` whose fire journal makes a
  missed run `unknown` rather than replaying it. The device pool seals each account's vendor
  identity in a separate file under an HKDF-derived key, restores a retired identity when the same
  account returns, and converts the v1 plaintext map at startup. The same process serves the
  platform wave: `/v1/device-pool` (list, generate, assign/release/retire/quarantine/purge with
  confirm tokens, passphrase export/import), `/v1/scoped-keys` (`ajs_sk_` least-privilege keys
  with a per-route scope table, account/region read restrictions and `usage:read`/`renewal:read`/
  `health:read` for customer-scoped reads), `/v1/renewal/*` (cached public plan table and
  per-account expiry, with no payment call and redirects refused), `/v1/usage` (request-log
  aggregation with `rowsRetained`/`retentionDays`, fed by gate flows unless `AJS_FLOW_METER=0`),
  `/v1/health/nodes` (loopback gate sampler: 30 s probes, 120 samples per node,
  `AJS_HEALTH_MAX_NODES` rings with eviction healing), `/v1/webhooks*` (HMAC-SHA256 delivery with
  five attempts and a 200-attempt ring; a receiver must not resolve to a private address unless
  `AJS_WEBHOOK_ALLOW_PRIVATE=1`) and `/v1/backup*` (passphrase-encrypted panel backup, inspect and
  a dry-run-first restore).
  After a successful login the node controller starts one loopback gate per usable server and
  reconciles the Resin subscription; it is wired only when `AJS_ACCOUNT_LOGIN=1` and was
  live-verified on 2026-10-23 (one account, one node, egress parity with the original app, plus
  a second login on the same account) and again on 2026-10-24 after the rebuild. 675 Go test
  functions — 634 in `daemon/`, 41 in `emulator/` — mostly `httptest` fakes; the tests use no
  Resin process, no vendor account and no network. The Linux 4.2.3.0 request shape (B1), stable
  per-account device identity (B5), binary-carrier mapping (C3) and the live response shapes are
  covered by offline conformance tests; B1/B3/B4/B5 carry live confirmation, and B5 is confirmed
  for **one account only** — B2 signing, multi-account coexistence and Resin live publish remain
  `[UNVERIFIED]`.
- `deploy/` — Docker Compose control plane: `ajs-accountd` on host loopback `2261`, the dashboard
  on `127.0.0.1:18081`, and Resin v1.3.0 behind the optional `resin` profile. Both images build
  from this tree; the stack includes no node supervision and has not run the live login path.
  A GHCR release pipeline and a one-command `update.sh` exist in the tree but are
  **`[UNVERIFIED]`**: this repository has no git remote and no tags, so the `v*` release
  workflow cannot fire and there is no registry image to pull. See
  [`deploy/README.md`](deploy/README.md).
- Resin M1 — **PASS** on HMC against `ghcr.io/resinat/resin:1.3.0`
  (`sha256:9aa94af706bfa7bc80c8fd78ed562f9a00d4e35e8323728cd17f06af43e665bf`): loopback SOCKS5 dial,
  IP-anchored sticky `(Platform, Account)` leases, rotation through platform filter exclusion,
  automatic re-selection after a node kill (3 failed requests over 6.0 s, first successful
  re-route at 12.6 s), and full state across a Resin restart. Measurements and caveats:
  [`research/77-m1-spike/REPORT.md`](research/77-m1-spike/REPORT.md).

**Not verified or not implemented — the status above must not be read as more than it is:**

- **Live AJiaSu login and smoke test — three logins, 2026-10-23 and 2026-10-24, one account.**
  With `AJS_ACCOUNT_LOGIN=1`, the original Linux app and the daemon both logged in and egressed
  the identical `222.186.57.63` through the same vendor node `vvn-2076-264` (镇江 #1); the
  daemon's gate was `127.0.0.1:13012`. The control hosts present a self-signed `CN=localhost`
  certificate, so `AJS_CONTROL_CERT_SHA256` (the pinned fingerprint) is required. The vendor
  allows one active FV data session per account, so `AJS_NODE_LIMIT` defaults to `1`. A **second
  login on the same account** returned HTTP 200 with identical node/gate state and byte-identical
  device identity, so B5 (stable device identity) is confirmed — **for that one account**. A
  **third login on 2026-10-24**, from the rebuilt `2261`, again produced the same node, gate
  `13012` and egress `222.186.57.63`. Redacted report:
  [`research/82-live-20261023/REPORT.md`](research/82-live-20261023/REPORT.md).
- **New client samples are static triage only.** [`research/84-ajs-496/`](research/84-ajs-496/)
  inventories the Windows 4.9.6.0, macOS 4.9.6.0, Linux 4.3.0.1 and Android 4.12.2.0 builds
  (six artefacts, SHA-256 per sample, packer/signature/endpoint/version diff). Nothing in that
  slice was executed — no installer, driver, tunnel or login, and no vendor host was contacted —
  so the live evidence above still rests on the Linux 4.2.3.0 profile and **one account**. The
  newer request shape and signing (`X-Sig` in 4.3.0.1, `_v=4.12.2.0` on Android) remain
  `[UNVERIFIED]` against the vendor.
- **The `supervisor` package is not used by the controller.** `NodeController`
  (`daemon/internal/runtime/nodes.go`) now wires login → session factory → per-node gate,
  with drain/undrain/delete/logout hooks, so the gate is production-wired. The separate
  `daemon/internal/supervisor` (per-node child start, readiness, events, stop) is still a
  seam the controller does not call — no `ajsproxy` child is started or supervised.
- **Still unverified after the 2026-10-23 and 2026-10-24 runs.** Multi-account coexistence on
  one daemon, Resin + gate live publish, B2 signed requests, B5 beyond one account, and hot
  update (dynamic API host refresh) are `[UNVERIFIED]`.
- **Release and update pipeline is `[UNVERIFIED]`.** `deploy/compose.release.yml`,
  `deploy/update.sh` and `.github/workflows/release.yml` (which builds and pushes GHCR images
  on a `v*` tag) are written but never exercised: this repository has **no git remote and no
  tags**, so the workflow has never fired, no image has been pushed, and `./update.sh --tag` has
  nothing to pull. Treat `ghcr.io/<owner>/ajs-*:<tag>` as a placeholder until a remote exists.
- **SQLite / the full schema.** The daemon persists a versioned JSON snapshot (`AJS_STATE_FILE`,
  every 30s + shutdown) including sealed secrets, but it is not the SQLite schema in
  `docs/PANEL.md`; the snapshot is a v0.1 stopgap.
- **Packaged deployment beyond the control plane.** `deploy/compose.yml` builds and runs
  `ajs-accountd` + the dashboard (Resin optional), but the supervisor node lifecycle is still
  absent and the stack has not run the live login path, so it proxies no traffic; there is no
  macOS or Windows build.
- **Background reconciler.** Resin drift is corrected only by an explicit `POST /v1/reconcile`;
  probes are bookkeeping canaries, not network probes.
- **The P1 surfaces are offline-tested only.** Catalog selection/connect, account export/import
  and bulk delete, the device pool and the scheduler have unit and `httptest` coverage but have
  never been exercised against the live vendor account. The scheduler is off unless
  `AJS_SCHEDULES=1`; the device pool needs `AJS_MASTER_KEY` (and is off under
  `AJS_DEVICE_POOL=0`).
- **The platform wave is offline-tested only.** Scoped keys (including the three read scopes), the
  device-pool HTTP API, renewal plans/status, flow-metered usage, the health sampler, webhook
  delivery with its SSRF policy, and panel backup/restore have unit and `httptest` coverage but
  have never run against the live vendor account, a real webhook receiver or a real restore.
  Renewal returns the vendor's public price snapshot and its official purchase page — nothing
  posts to a payment endpoint and no account id or token rides the link. The health probe dials
  `127.0.0.1` only, so it reports gate reachability on the daemon host, not vendor reachability;
  it keeps one hour of samples (120 at 30 s) for at most `AJS_HEALTH_MAX_NODES` nodes (default 16)
  and re-arms an evicted watched ring within a reconcile tick. The flow meter records one row per
  completed gate flow and never a rejected one, and `/v1/usage` reports the 10 000-row cap with
  the day span it actually covers. Webhook retries and the delivery ring live in memory, so a
  restart drops queued retries and the ring, and the signing secret survives a restart only with
  `AJS_MASTER_KEY`. A backup envelope carries sealed credentials; restore writes the panel files
  only on an explicit apply and the daemon must then be restarted.

The product layer above the daemon — multi-account sessions, lease allocation and per-user
access — is designed but not implemented. See [`docs/PANEL.md`](docs/PANEL.md) for the build
sheet and [`research/70-panel/REPORT.md`](research/70-panel/REPORT.md) for the candidate analysis
behind it.

[`docs/openapi.json`](docs/openapi.json) is the OpenAPI 3.1 contract for the daemon's HTTP
surface (77 paths, 95 operations, 147 schemas at the last sync): every route registered in
`daemon/internal/api/server.go` — accounts and account IO, device pool, catalog selection/connect,
schedules, scoped keys, the platform views (renewal, usage, health, webhooks), panel backup and the
Lab — plus the proposed P1 interfaces. Scoped API keys (`ajs_sk_`, least privilege with per-route
scopes and account/region restrictions) are served as P0. Each operation carries
`x-implementation-phase` (`P0` is served today, `P1` is the proposed next phase).
`dashboard/tests/openapi.test.ts` fails when the document stops parsing, a `$ref` dangles, an
`operationId` repeats, or the route table and the contract drift apart.

## Scope and tiers

- **Tier 1** — orchestrate the official Linux client as a black box and expose its egress as a
  proxy. Fast, low protocol risk, prior art exists.
- **Tier 2** — native reimplementation of the `FV` protocol (login, node list, tunnel), running
  without any vendor binary. This is the north star.

The Windows kernel component shipped with the client is the stock OpenVPN TAP-Windows driver
(`tap0901`); it is the system-wide TUN path only, and a SOCKS5/HTTP proxy API does not need it.

## Quick start

```bash
# 1. Mock dashboard — deterministic in-browser data, no backend, no network
cd dashboard && npm install && npm run dev        # http://127.0.0.1:5173

# 2. Daemon, loopback only (from daemon/). go.mod replaces ajsproxy => ../emulator,
#    so the two trees must be sibling directories in the same checkout.
#    A local run needs no token: AJS_ALLOW_INSECURE=1 disables auth and startup
#    refuses it on any non-loopback bind. For a real token, export one first:
#      export AJS_ADMIN_TOKEN=$(openssl rand -hex 32)
cd daemon
AJS_LISTEN=127.0.0.1:2261 AJS_ALLOW_INSECURE=1 go run ./cmd/ajs-accountd

# 3. Live dashboard build and static serve (from dashboard/, in a second shell)
#    With no VITE_PANEL_TOKEN the bundle sends no Authorization header, which
#    the insecure daemon ignores; a real-token build sets VITE_PANEL_TOKEN to
#    the same AJS_ADMIN_TOKEN.
VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 npm run build
npx vite preview --host 127.0.0.1 --port 18082   # http://127.0.0.1:18082

# 4. Control-plane compose stack: accountd + dashboard (Resin optional)
cd deploy && cp .env.example .env                 # from the repo root; fill in the placeholders
docker compose up -d --build                      # dashboard: http://127.0.0.1:18081
```

The compose stack builds both images from this tree and runs `accountd` on host loopback `2261`.
Its dashboard publishes the same `18081` as the standalone `dashboard/compose.yml`, so stop one
before starting the other. Prerequisites, the live/mock build switch, the optional `--profile
resin`, health checks, backup/restore and the honest limitations are in
[`deploy/README.md`](deploy/README.md).

`AJS_ALLOW_INSECURE=1` is a loopback-only lab switch: startup refuses it unless `AJS_LISTEN` is
loopback, and the daemon then accepts `/v1/*` without a bearer token. For a real deployment,
generate a token with `openssl rand -hex 32` — startup refuses the documented placeholders and
anything shorter than 32 characters — and pass the same value as `AJS_ADMIN_TOKEN` and
`VITE_PANEL_TOKEN`. `VITE_*` values are baked into `dist/` at build time, so a live token in the
bundle is readable by anyone who can fetch it: either keep `ajs-accountd` bound to `127.0.0.1`
and terminate real authentication in a BFF or reverse proxy, or serve a token-free bundle and
let the runtime token gate collect the token per browser tab.

The HMC build host serves the mock dashboard on loopback `18081`
(`docker compose up -d` in `dashboard/`) and the live pair on the LAN:
`http://192.168.11.4:18082` (live `vite preview` build) against `http://192.168.11.4:2261`
(`ajs-accountd`, rebuilt from HEAD on 2026-10-24 with login enabled). The LAN daemon runs with
bearer auth — `AJS_ALLOW_INSECURE` is not set, so every `/v1/*` call needs the token and a
token-less request answers 401 — and the LAN build bakes no `VITE_PANEL_TOKEN`, so the
dashboard's runtime token gate prompts on the first 401 and keeps the value in `sessionStorage`
for that tab. The live store is demo-free: a one-shot `AJS_DROP_DEMO=1` run on 2026-10-24
(`demo_purged accounts=6 nodes=12 leases=8 users=4 keys=6 requests=40 audit=20`) removed the mock
accounts, nodes, leases, users, keys, requests and seed audit rows from the snapshot, and the
switch was then unset. What remains is exactly one account, `acc-07` (`live-ajs-01`, `active`), and
one node, `acc-07:vvn-2076-264` (`LIVE`); `GET /healthz` reports one active account. The retained
login result is a 2,814-server vendor catalog, all in region `cn` (rendered as one *China* group),
so the Nodes catalog totals 2,814 rows, paged 50 at a time, with that one node marked current and
no demo names. Gate listeners are allocated per node from `127.0.0.1:13000` upward, so read the
current listener and state from `GET /v1/lab/gates` rather than assuming a fixed port (the purge
deployment reported `13000`).
SOCKS5 through that gate egresses `222.186.57.63`, the same address as the original app; the
vendor's claimed egress is a per-session RFC1918 value — `10.98.206.1` on the current session,
`10.78.248.1` on the purge run — so the Lab egress check renders `match:false` (verified vs
claimed) by design. The `18082` dashboard was rebuilt against `2261` with the APC
slice. `/v1/system/update` answers 200 with `enabled:false` because `AJS_UPDATE_REPO` is empty. The
separate lab `ajs-accountd` on `2262` is stopped.
Resin listens on `2260` when running. `deploy/` packages the same pair
as one stack and reuses `18081`, so run either it or the standalone dashboard compose,
not both. The LAN URL is reachable directly from Windows; the tunnels below still work for the
loopback-only mock build and for hosts that cannot route to `192.168.11.4`:

```bash
ssh -N -L 18081:127.0.0.1:18081 hmc
ssh -N -L 18082:127.0.0.1:18082 -L 2261:127.0.0.1:2261 hmc
```

Tests: `cd dashboard && npm test` (330 tests in 37 files), `cd daemon && go test ./...`
(634 test functions, needs `../emulator`), `cd emulator && go test ./...` (41 test functions).
All three suites run offline. The last recorded dashboard and daemon runs are on HMC, alongside
the M1 spike; see the evidence sections of
[`dashboard/README.md`](dashboard/README.md), [`daemon/README.md`](daemon/README.md) and
[`research/77-m1-spike/REPORT.md`](research/77-m1-spike/REPORT.md).

## Layout

```
README.md                  this file
PLAN.md                    campaign plan, slices, acceptance criteria, risks
LICENSE                    MIT
docs/PANEL.md              control-plane design: ports, API, data model, phases
docs/openapi.json          OpenAPI 3.1 contract for the daemon route table (P0 served, P1 proposed)
dashboard/                 operator console (React/Vite/TypeScript); see dashboard/README.md
daemon/                    ajs-accountd control-plane skeleton (Go 1.26); see daemon/README.md
deploy/                    Docker Compose control plane (accountd + dashboard, Resin profile); see deploy/README.md
emulator/                  offline Tier-2 FV emulator (Go)
scripts/m1/                M1 spike harness: loopback SOCKS5 nodes (dummy-socks)
research/00-plan/          question tree, master index, ledger
research/10-* .. 90-*      per-slice research output (later waves)
research/70-panel/         panel research: candidate matrix, sources, validation script
research/77-m1-spike/      Resin v1.3.0 M1 spike report (PASS) and measurements
research/82-live-20261023/  first live-account run: original app vs daemon egress parity (redacted)
research/84-ajs-496/       static triage of the 4.9.6.0 / 4.3.0.1 / 4.12.2.0 samples (not executed)
```

## Legal and terms-of-service caution

This work is reverse engineering for interoperability research. Before any use of the findings:

- The vendor's terms of service, and the terms of any account used for testing, govern what is
  permitted. Automated or programmatic use of the service may violate them.
- Do not use this research to bypass account limits, resell access, or hide the identity of
  abusive traffic. The egress-IP selection described here is a technical capability, not a
  licence to misuse the service.
- Reverse engineering is restricted by law in some jurisdictions. Check your own.
- Any published code must carry a licence compatible with the obligations above.

## No-credentials rule

**Never commit credentials.** No accounts, passwords, e-mail addresses, tokens, TOTP secrets,
session cookies, or hashes of any of them belong in this repository — not in reports, not in
test fixtures, not in commit messages. `.env`, `*.key` and `*.pem` are ignored by default.

Live testing, when it happens, is performed with an Owner-supplied throwaway account and the
results are recorded as observations only; the credential itself is never written to disk in
this tree.

The commands above either run with auth disabled on loopback or use a token generated at run time
and kept in the shell; no credential is written to the tree. The live dashboard token is a
build-time value, and the daemon, the dashboard and Resin all stay bound to `127.0.0.1`.

## Reports

All research reports are written in English and cite a path, URL, or command for every claim.
Claims that were not directly observed are marked `[INFERENCE]` or `[UNKNOWN]`.

## License

MIT — see [`LICENSE`](LICENSE). Copyright (c) 2026 The LovePlusSpeedToProxy Authors.

Third-party components keep their own licences. The panel design runs
[Resin](https://github.com/Resinat/Resin) unmodified under the MIT licence
(Copyright (c) 2026 Resinat and contributors); no AGPL-licensed or unlicensed code is copied into
this repository. Details and attribution obligations: `research/70-panel/REPORT.md`.

## Use boundary

This project is for a single operator driving **accounts they own or are explicitly authorized to
use**, for their own traffic. It is **not** a public proxy service: no resale, no sharing of
access, no published endpoints, no bypassing vendor account limits, and no use to hide the origin
of abusive traffic. The operator is responsible for the vendor's terms of service and for local
law. The same boundary is restated in `PLAN.md` and `docs/PANEL.md`.
