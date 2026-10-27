# ARCHIVE — AJiaSu → API

> **Historical snapshot.** This page describes commit `47b4699`
> (`47b46999e4ef58cf3a7350b3c3cae6ffd89df39b`, *feat(dashboard): add portal payment channel
> picker*, 2026-10-27). The tree has moved on: current HEAD is `901c75b`, **29 commits later**
> (`git rev-list --count 47b4699..HEAD`), and the release surface is defined by
> [`docs/releases/v0.1.0-pre.md`](docs/releases/v0.1.0-pre.md) and
> [`docs/RELEASE.md`](docs/RELEASE.md). The narrative and file list describe the 2026-10-27
> snapshot; every **count** on this page was re-derived at HEAD `901c75b` on 2026-10-28, with
> the command shown where the number appears, so the page cannot carry a stale count. Do not
> treat this page as current status.

**Archived 2026-10-27** at `47b4699`, branch `panel-design`. There is **no git remote** and
**eight local milestone tags** (`v0.1.0-archive` … `v0.7.0-live`; `git tag --list 'v*' | wc -l`);
none of them is a public release and nothing has been pushed. This document is the archive map:
what the retained product is, what stays experimental, how to run and test it, and which gaps
were still open at that commit. It changes no code.

The paid-tenant direction is closed for this release. The product kept by this archive is the
core **AJiaSu → API** path: an operator-run control plane that turns an authorized AJiaSu account
into a scoped, lease-based proxy API — API key plus least-privilege scopes, in the shape of a
Webshare-style proxy API. The tenant portal, orders and payment adapters remain in the tree as
offline-tested, opt-in code; they are disabled in `v0.1.0-pre` and are not a shipping offer
(see [`docs/EPAY-STATUS.md`](docs/EPAY-STATUS.md)). Two experimental subsystems from earlier
milestones — a credential store and an unused node-controller seam — no longer exist in the
tree (removed in `e9e546e`); ignore any older text that describes them as available.

## What the core product is

| Area | What is served | Where |
|---|---|---|
| Accounts | Create, read, patch, delete, login, logout; export/import/bulk-delete are admin-only | `/v1/accounts*` |
| Catalog | Retained vendor server catalog, paged and filtered (`q`, `region`, `status`, `province`, `city`, `selected`, `sort`, `offset`, `limit`); 2,814 rows for the live `acc-07` account at the 2026-10-24 audit | `GET /v1/accounts/{id}/servers` |
| Connect / switch | Guarded connect, disconnect, reconnect and the async `select-node` wrapper; one session per account, per-account cooldown, `snapshotId` guard | `/v1/accounts/{id}/actions/*`, `/v1/operations*` |
| Leases | Create, list, rotate and revoke; one active lease per user and platform; `Idempotency-Key` replay; admission counts live, listener-bound slots | `/v1/leases*` |
| Nodes | List, drain, undrain, probe | `/v1/nodes*` |
| Requests / usage | Gate-flow request log (10,000-row cap) and windowed aggregation with `rowsRetained` / `retentionDays` | `/v1/requests`, `/v1/usage` |
| Scoped keys | `ajs_sk_` keys with a per-route scope table, `accountIds`/`regions` read narrowing and a per-minute rate limit | `/v1/scoped-keys*` |
| Topology / health | Resin topology projection and loopback gate health (30 s probes, 120 samples per node) | `/v1/topology`, `/v1/health/nodes` |
| SDKs | Dependency-free Go and TypeScript clients: accounts, select-node operations, leases, usage, events | `sdk/go`, `sdk/ts`, `sdk/README.md` |

The dashboard (`dashboard/`) is the operator console over the same API: **15 admin routes**
(`sed -n '/<Route element={<AdminLayout/,/^              <\/Route>/p' dashboard/src/App.tsx | grep -c '<Route path="/'`)
and **54 Vitest test files** (`cd dashboard && find tests -name '*.test.ts*' | wc -l`). The
current Vitest 5 tree lists 475 cases (`npx vitest list | wc -l`) and the full serial suite
passes 475/475; the file count is stable. The daemon (`daemon/`, Go 1.26, `ajs-accountd`) binds
`127.0.0.1:2261` by default and persists a versioned JSON snapshot; the offline `emulator/`
implements the Tier-2 `FV` protocol without vendor binaries.

At the last recorded audit (2026-10-24), the HMC deployment had one account `acc-07`
(`live-ajs-01`, `active`) and one `LIVE` node, with a 2,814-server catalog and SOCKS5 egress
`222.186.57.63`. Those are audit-time observations, not a current inventory: query
`GET /v1/accounts` and `GET /v1/nodes` for current ids and states. Gate ports are allocated from
`127.0.0.1:13000` upward — read the current listener from `GET /v1/lab/gates` rather than assuming
a port.

## Experimental or not enabled

| Surface | State in this archive | Switch / requirement | Evidence |
|---|---|---|---|
| Customer tenant portal | Experimental, **off in `v0.1.0-pre`** | `AJS_TENANT_PORTAL=0` in the shipped env example; enabling needs `=1` plus an exact `AJS_TENANT_ORIGIN`, and `AJS_TRUSTED_PROXIES` decides whose `X-Forwarded-For` is believed | Served and covered by `httptest`; no browser signup, session or approval outside tests; routes are unregistered (404) when off |
| Orders and 易支付 (epay) | Experimental, **off in `v0.1.0-pre`** | Admin plan catalogue and order routes register only under `AJS_TENANT_PORTAL=1`; epay needs `AJS_EPAY_GATEWAY`, `AJS_EPAY_PID`, `AJS_EPAY_KEY` | Offline only; no real order or payment event; no route charges a card. See [`docs/EPAY-STATUS.md`](docs/EPAY-STATUS.md) |
| Stripe-compatible callback | Disabled unless configured | `AJS_STRIPE_WEBHOOK_SECRET` unset makes `POST /v1/payments/stripe` answer 503 | No real Stripe event has reached it |
| Credential-store experiment | **Removed from the tree** | — | Deleted in `e9e546e`; no route, listener, store file or setting exists in `v0.1.0-pre` |
| Multi-slot (`UseCount > 1`) | Not enabled in practice | Vendor entitlement (`useCount`), display cap `AJS_ACCOUNT_SLOTS` | Live account has `useCount=1`, `effectiveSlots=1`; per-slot runtime is offline-tested, two-slot coexistence is unproven, and a lease on an account whose display capacity is above one carries `multi_session_unimplemented` |
| State journal | Opt-in | `AJS_JOURNAL=1`; default is the JSON snapshot (`AJS_STATE_FILE`, every 30 s + shutdown) | Journal recovery/append tests are offline; the snapshot remains the default |
| Release / update pipeline | Unverified | `deploy/update.sh`, `deploy/compose.release.yml`, GHCR workflows | **No git remote** and eight local milestone tags; nothing was pushed, no image exists, and `update.sh --tag` has nothing to pull |

## How to run and test

All suites are offline; none needs a vendor account, a Resin process or network access.

```bash
# Daemon (needs ../emulator as a sibling; go.mod replaces ajsproxy => ../emulator)
cd daemon && go test ./...                  # 1346 test functions in 166 files

# Tier-2 emulator
cd emulator && go test ./...                # 46 test functions in 14 files

# Operator console
cd dashboard && npm ci && npm test          # 54 test files (475 cases under Vitest 5)

# SDKs
cd sdk/go && go test ./...                  # 38 tests
cd sdk/ts && npx tsc --noEmit && npm test   # 24 tests

# Production deploy-kit self-check, development mode (production mode requires root,
# nginx, openssl, systemd-analyze and a Chromium-family browser)
bash deploy/production/check.sh --dev
```

Run the daemon on loopback with auth disabled for a lab session, or export a real token first:

```bash
cd daemon
AJS_LISTEN=127.0.0.1:2261 AJS_ALLOW_INSECURE=1 go run ./cmd/ajs-accountd

# or
export AJS_ADMIN_TOKEN=$(openssl rand -hex 32)
AJS_LISTEN=127.0.0.1:2261 go run ./cmd/ajs-accountd
```

Build the live dashboard against that daemon and serve it:

```bash
cd dashboard
VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 npm run build
npx vite preview --host 127.0.0.1 --port 18082
```

The compose stack builds both images from this tree:

```bash
cd deploy && cp .env.example .env && docker compose up -d --build   # dashboard 127.0.0.1:18081
```

HMC reference: mock dashboard `18081`; the live dashboard is served at
`http://192.168.11.4:18082`. Since 2026-10-28 the live daemon binds `127.0.0.1:2261`, so it is
not reachable directly from the LAN; use a loopback session or the HMC origin. Resin on `2260`
when running; gate listeners from `GET /v1/lab/gates`. The compose stack reuses `18081`, so run
either it or the standalone dashboard compose, not both.

## Security notes

- **The FV carrier is obfuscation, not encryption.** The outer 16-byte key travels in the clear,
  and the data channel reuses `AES-128-CTR(sessionKey, IV=div)` in both directions: an observer who
  can read the TCP stream can XOR the two directions and crib-drag the leading bytes. The auth
  body and auth map share `AES-128-CTR(sessionKey, IV=n)` from offset 0 in the emulator's
  implementation only; that is unconfirmed for the vendor. Treat any captured stream, session key
  or credential as compromised.
- **Rotate vendor and epay secrets.** No credential belongs in this tree: `.env`, `*.key` and
  `*.pem` are ignored, the live account is Owner-supplied, and the epay key is never logged.
  Rotate the vendor password, `AJS_EPAY_KEY`, `AJS_STRIPE_WEBHOOK_SECRET` and `AJS_ADMIN_TOKEN`
  after any exposure or operator departure. Change `AJS_MASTER_KEY` only with a re-seal plan:
  existing account passwords, device identities and webhook signing secrets are bound to the
  current key and do not survive a silent replacement.
- **Scoped-key export and bulk-delete are admin-only.** `routeScopes` deliberately omits
  `/v1/accounts/export`, `/v1/accounts/import` and `/v1/accounts/bulk-delete`: a scoped key without
  the `admin` scope is refused before any handler runs. Device pool, users, API keys, scoped-key
  management, webhooks, backup/restore, plans/orders and tenant administration are admin-only for
  the same reason. Plaintext account export additionally requires
  `AJS_ALLOW_PLAINTEXT_EXPORT=1` and an `EXPORT` confirmation; an encrypted `.ajsx` export is
  always available.
- **The data plane is loopback.** Every gate binds `127.0.0.1` and defaults to RFC 1929
  (`AJS_GATE_AUTH` unset = `userpass`): the username is a panel user id and the password an active
  API-key secret. `AJS_GATE_AUTH=loopback` is the explicit no-auth compatibility opt-in, and a
  deployment with no key fails closed. `AJS_GATE_HTTP=1` adds an HTTP CONNECT listener beside
  SOCKS5; the public TLS edge at `gate.dwgx.top:13000-13010` publishes that listener, while
  SOCKS5 is never published. The public scoped-key API and docs are live at
  `https://ajs.dwgx.top:2055`; the admin origin and the portal routes stay private.
  `AJS_TLS_CERT`/`AJS_TLS_KEY` only change the admin transport.
- **`accountIds`/`regions` narrow reads, not writes.** The restriction filters account-scoped GETs
  and is enforced before handlers; a write scope (`catalog:write`, `leases:write`) applies to every
  account. Issue write scopes deliberately, and prefer read-only keys for monitoring.

## Known gaps

1. **No public SOCKS5 ingress.** The SOCKS5 gate still binds `127.0.0.1` only; nothing
   publishes it. The HTTPS CONNECT listener is the only published data-plane port
   (`gate.dwgx.top:13000-13010`). Gates, Resin, the daemon and the dashboard otherwise stay on
   loopback or a private LAN.
2. **No real payment drill.** No customer has created an order, no administrator has attested an
   offline receipt, and no real Stripe or 易支付 event has reached the daemon outside `httptest`.
   Manual settlement is an attestation of funds received elsewhere; the daemon never charges a
   card, stores no card data and sends no payment request. Refund and chargeback record evidence
   and revoke the grant; they transfer nothing. Payments are disabled in `v0.1.0-pre`.
3. **No `UseCount=2`.** The only live account has `useCount=1`, so `effectiveSlots=1` and exactly
   one lease can be admitted. Per-slot runtime code exists and is offline-tested, but live
   two-slot coexistence has not been proven; a lease above one slot still carries the
   `multi_session_unimplemented` warning.

Also unverified at archive time: B2 request signing, multi-account coexistence on one daemon,
Resin live publish, hot update (dynamic API host refresh), the SQLite schema in `docs/PANEL.md`
(the daemon uses a JSON snapshot), the packaged service wrappers and the GHCR release pipeline.

## Pointers

- [`README.md`](README.md) — current status, layout, quick start, FV confidentiality, use boundary
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — current topology and trust boundaries
- [`docs/OPERATIONS.md`](docs/OPERATIONS.md) — current install, health, backup, rotation
- [`docs/API-GUIDE.md`](docs/API-GUIDE.md) — Webshare-style quickstart for the retained API
- [`docs/openapi.json`](docs/openapi.json) — OpenAPI 3.1 contract (120 paths, 148 operations, 200 schemas; `python -c` count in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#numbers))
- [`docs/PANEL.md`](docs/PANEL.md) — control-plane design baseline
- [`docs/RBAC.md`](docs/RBAC.md) — operator roles and the strict-mode cutover
- [`sdk/README.md`](sdk/README.md) — Go and TypeScript client coverage and retry rules
- [`research/82-live-20261023/REPORT.md`](research/82-live-20261023/REPORT.md) — the live-account run
