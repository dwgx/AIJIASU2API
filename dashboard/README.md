# AJiaSu Proxy Console

Operator dashboard for `ajs-accountd` — accounts, nodes, leases, users, request telemetry and the
audit trail in one Obsidian Mint surface. **Mock-first:** the default build runs on deterministic
in-memory data and never contacts AJiaSu, a vendor binary or a live daemon. A live mode against the
local `ajs-accountd` skeleton is also built and verified; it is opt-in through build-time
environment variables (see [Modes](#modes) and [Live mode](#live-mode)).

- Stack: Vite 6, React 19, TypeScript, React Router, `lucide-react`, `recharts`, `clsx`.
- Styling: plain CSS variables, no Tailwind, no component library.
- Tests: Vitest + React Testing Library + jsdom, 25 tests.
- Package manager: npm.
- Status: `v0.1.0` implementation slice. No git commits, no credentials, no vendor calls.

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
npm test           # Vitest, 25 tests
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

## HMC layout

The HMC build host serves three loopback-only endpoints:

| HMC endpoint | Role | How it runs |
|---|---|---|
| `127.0.0.1:18081` | mock dashboard | `docker compose up -d` in `dashboard/`; nginx image built from the default (mock) source, rebuilt after the live/mock copy fixes |
| `127.0.0.1:18082` | live dashboard | `npx vite preview` over a `dist/` built with the live environment |
| `127.0.0.1:2261` | `ajs-accountd` | `go build -o ajs-accountd ./cmd/ajs-accountd` then `./ajs-accountd` with the env above; in-memory store |

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

**Backend limitation:** `ajs-accountd` is an in-memory skeleton. It reseeds from the dashboard mock
on every start, so all of the state above lives only as long as the process. No Resin publisher,
vendor login, SQLite store or gate is wired in yet, and no vendor request was made.

## Verification (recorded 2026-10-23)

Build and test were executed on the HMC build host, not on the Windows product tree:

```bash
# copy only dashboard/ to HMC scratch
tar -C D:/Project/LovePlusSpeedToProxy/dashboard -czf - . \
  | ssh hmc 'mkdir -p /srv/dwgxhomecloud/scratch/loveplus-dashboard && tar -xzf - -C /srv/dwgxhomecloud/scratch/loveplus-dashboard'
ssh hmc 'cd /srv/dwgxhomecloud/scratch/loveplus-dashboard && npm install && npm run build && npm test'
```

Both modes were built from the same source on HMC:

| Build | Command | Result |
|---|---|---|
| Mock (default) | `npm run build -- --outDir dist-mock` | exit 0, `✓ built in 2.81s`, `dist-mock/assets/index-Dsrk9h6r.js` |
| Live | `VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 VITE_PANEL_TOKEN=local-test-token npm run build` | exit 0, `✓ built in 2.57s`, `dist/assets/index-DeEyHtXf.js` |
| Typecheck | `npm run typecheck` | exit 0 |

`npm test` — exit 0, `Test Files 4 passed (4)`, `Tests 25 passed (25)`:

| File | Tests |
|---|---|
| `tests/api.test.ts` | 9 |
| `tests/app.test.tsx` | 6 |
| `tests/interactions.test.tsx` | 9 |
| `tests/live-mode.test.tsx` | 1 |
| **Total** | **25** |

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

## Tests and QA

- 25 Vitest tests in 4 files (see [Verification](#verification-recorded-2026-10-23)).
  `tests/live-mode.test.tsx` stubs `VITE_USE_MOCK=false` before importing the app, then asserts the
  live shell (`live API`, `v0.1.0 · live`) and the absence of the reset card.
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
| `screenshots/account-drawer.png` | open `/accounts?new=1`, fill Label / Username / Password / Regions |
| `screenshots/api-key-modal.png` | `/users` → **Create API key** → wait for users → fill Label → **Create key** (show-once secret) |
| `screenshots/toast.png` | `/leases` → first **Copy** → toast `Proxy URL copied` |
| `screenshots/mobile-nav.png` | 390×844 → **Open navigation** → sidebar drawer |

## Routes

| Route | Page | Primary surfaces |
|---|---|---|
| `/` | Overview | 4 metric tiles, ingress/egress chart, account health, activity, quick actions |
| `/accounts` | Accounts | filterable table, add drawer, login test, pause/resume, delete confirm, bulk select |
| `/nodes` | Nodes | region/status filters, latency sparklines, success/capacity meters, drain toggle, detail drawer |
| `/leases` | Leases | sticky-lease callout, copy proxy URL, rotate, release confirm, TTL |
| `/users` | Users | user cards, quota meters, API keys, show-once secret, revoke, allowlist editor |
| `/requests` | Requests | request log, status/method/source filters, mutation lab drawer |
| `/logs` | Logs | severity/actor filters, audit table, export placeholder |
| `/settings` | Settings | transport, health interval, lease TTL, subscription refresh, theme, reset |

`*` redirects to `/`.

## Layout

```text
src/
  App.tsx                 lazy routes for all 8 pages
  main.tsx                entry, self-hosted Inter / JetBrains Mono
  styles/app.css          tokens, shell, components, responsive, light theme
  lib/
    types.ts              Account, Node, Lease, PanelUser, ApiKey, RequestLog, AuditEvent, Metrics
    mock.ts               6 accounts · 12 nodes · 8 leases · 4 users · 40 requests · 20 audit rows
    api.ts                typed client; mock by default, fetch when VITE_USE_MOCK=false
    format.ts             bytes, dates, TTL, proxy URL helpers
    labels.ts             account/node/severity tone maps
    nav.tsx               navigation + command registry metadata
    theme.tsx             dark/light provider, localStorage persistence
    useAsync.ts           load/error/ready state, global refresh event
    useFocusTrap.ts       Esc + Tab trap for palette/drawer/modal
  components/
    AppShell, Sidebar, Topbar, CommandPalette, Toast, CopyButton
    DataTable, Overlay (Drawer/Modal/ConfirmDialog), primitives
  pages/                  Overview, Accounts, Nodes, Leases, Users, Requests, Logs, Settings
tests/
  setup.ts                jsdom polyfills (ResizeObserver, clipboard, matchMedia)
  app.test.tsx            all routes, palette open/navigate/Esc/filter
  interactions.test.tsx   copy toast, rotate, release, create account, bulk pause, key show-once, mutation lab
  api.test.ts             seed counts, metrics, CRUD, reset, settings persistence
  live-mode.test.tsx      live shell labels, no reset card when VITE_USE_MOCK=false
```

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
| Settings | `GET/PATCH /v1/settings` | `[PROPOSED]`; served by the `ajs-accountd` skeleton |

Every path in this table is served by the in-memory `ajs-accountd` skeleton in `daemon/`; `[SPEC]` /
`[PROPOSED]` describe where the contract is defined, not whether it is implemented.

The mock store is module-level and mutable: create/pause/delete accounts, drain nodes, rotate and
release leases, create/revoke keys, edit allowlists, add mutation-lab requests, save settings and
reset the dataset. A full browser reload restores the seed data.

## Design system — Obsidian Mint

Dark-first tokens (light is a derived variant with the same roles):

```css
--bg:#0B0F0E; --sidebar:#0E1412; --surface:#131A17; --elevated:#1A2420; --active:#22302A;
--text:#E6F1EC; --text-2:#A3B8AE; --muted:#6F857B; --text-tertiary:#829A90;
--border:rgba(255,255,255,.08); --border-strong:rgba(255,255,255,.14);
--mint:#5EEAD4; --mint-strong:#2DD4BF; --success:#34D399; --warning:#FBBF24;
--danger:#FB7185; --info:#60A5FA;
--radius-card:12px; --radius-control:8px; --sidebar-w:248px; --topbar-h:64px;
```

`--muted` is reserved for icons, rules and non-text decoration. Small text uses
`--text-tertiary` (`#829A90`) because `#6F857B` measures 4.48:1 on surface and 4.04:1 on elevated —
below AA for normal text. This follows `docs/DESIGN-SYSTEM.md` §3.8 without editing that contract.

- Type: 13/18 UI, 11/14 uppercase labels with +0.06em, 20/26 page title, 28/32 metric, 12/18 mono.
- Controls 32 px, table rows 36 px, content padding 24 px, card gap 16 px.
- Focus ring: 2 px mint with 2 px offset; reduced-motion fallback.
- Motion: 150 ms state, 250 ms overlays, 300 ms panels; enter `cubic-bezier(.19,1,.22,1)`,
  exit `cubic-bezier(.8,0,.4,1)`, move `cubic-bezier(.65,0,.35,1)`.
- Responsive: 248 px sidebar at desktop, 64 px rail from 761–1100 px, off-canvas drawer below
  760 px; data tables become card lists below 760 px (no horizontal document scroll); metric grid
  collapses 4 → 2 → 1.

## Keyboard and accessibility

- `Ctrl/Cmd+K` opens the command palette (fuzzy filter, ↑/↓, Enter, Esc, Tab trap).
- Sidebar links, table actions and form controls are keyboard reachable with visible focus.
- Drawers/modals trap focus, close on Esc/backdrop, and restore the previous focus.
- Tables use real `<table>` semantics with an accessible caption; status is dot **and** label.
- Toasts announce through `aria-live="polite"`; destructive actions confirm in a modal.
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

- `ajs-accountd` is an in-memory skeleton: it reseeds from the dashboard mock on every start, so
  created accounts, keys, leases and settings do not survive a daemon restart. There is no
  persistence, no Resin publisher wiring, no vendor login and no gate yet.
- Live mode is verified against that skeleton only. No AJiaSu vendor endpoint is contacted; the
  mutation lab records and answers locally.
- The live build hides the mock reset action entirely — the daemon has no reset endpoint.
- `VITE_PANEL_TOKEN` is build-time and readable from the bundle. There is no per-operator auth,
  role model or session; production needs a BFF or reverse proxy.
- Mock state is in memory; refreshing the tab discards edits (Settings → Reset dataset does the same
  explicitly).
- Log export is a placeholder; polling/SSE, pagination above 500 rows and server-side sorting are
  not implemented in v1.
- `dashboard/docs/` is the AJS-20 design contract and is not edited by this slice.
- No git commit or push was performed.

## License

Repository code is MIT; see the root [`LICENSE`](../LICENSE).
