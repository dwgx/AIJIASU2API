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
across logins; the `2261` deployment now runs login-enabled with that account active. Panel
state survives a restart only as a versioned JSON snapshot, not SQLite.**

As of 2026-10-24:

- `emulator/` — offline Tier-2 `FV` emulator: control API, FV crypto/frame, SOCKS5/HTTP front ends
  and the captured proxy carrier. It builds and passes its offline conformance tests; it makes no
  live calls and holds no credentials.
- `dashboard/` — operator console: React 19, Vite 6, TypeScript, 8 pages (Overview, Accounts,
  Nodes, Leases, Users, Requests, Logs, Settings) in the Obsidian Mint design, with accent themes
  (Mint/Indigo/Violet/Orange), a table/cards switch for Accounts, and command-palette search for
  accounts and nodes. Mock data is the default; a live mode against `ajs-accountd` is opt-in at
  build time. 71 Vitest tests in 11 files. HMC serves the mock build on `18081` and the live
  build on `18082`.
- `daemon/` — Go 1.26 `ajs-accountd` skeleton: the dashboard's JSON contract over an in-memory
  store persisted as a JSON snapshot, a Resin v1.3.0 admin client, publisher and runtime wiring
  (local subscription render, platform create/update, lease rotate through platform filter
  exclusion, node drain/undrain, probe-config patch), an AES-256-GCM sealed secret file, a
  Prometheus `/metrics` endpoint, and the account control, FV session factory, node controller,
  SOCKS5 gate and process-group kill with `WaitDelay` supervisor seams.
  After a successful login the node controller starts one loopback gate per usable server and
  reconciles the Resin subscription; it is wired only when `AJS_ACCOUNT_LOGIN=1` and was
  live-verified on 2026-10-23 (one account, one node, egress parity with the original app, plus
  a second login on the same account) and again on 2026-10-24 after the rebuild. 277 Go test
  functions — 241 in `daemon/`, 36 in `emulator/` — mostly `httptest` fakes; the tests use no
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

The product layer above the daemon — multi-account sessions, lease allocation and per-user
access — is designed but not implemented. See [`docs/PANEL.md`](docs/PANEL.md) for the build
sheet and [`research/70-panel/REPORT.md`](research/70-panel/REPORT.md) for the candidate analysis
behind it.

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
`VITE_PANEL_TOKEN`. `VITE_*` values are baked into `dist/` at build time, so the live token is
readable by anyone who can fetch the bundle: keep `ajs-accountd` bound to `127.0.0.1` and terminate
real authentication in a BFF or reverse proxy.

The HMC build host serves loopback-only endpoints: `18081` mock dashboard
(`docker compose up -d` in `dashboard/`), `18082` live dashboard (`vite preview` over a live
build), and `2261` `ajs-accountd`, rebuilt from HEAD on 2026-10-24 with login enabled. Account
`acc-07` (`live-ajs-01`, the only account with a real vendor session; seeded demo accounts
remain) is `active` with one `LIVE` node, `acc-07:vvn-2076-264`, behind gate
`127.0.0.1:13012`; SOCKS5 through that gate egresses `222.186.57.63`, the same address as the
original app. The `18082` dashboard was rebuilt against `2261`. `/v1/system/update` answers 200
with `enabled:false` because `AJS_UPDATE_REPO` is empty. The separate lab `ajs-accountd` on
`2262` is stopped: its gate on `13012` died with it, and `13012` now belongs to the `2261` node.
Resin listens on `2260` when running. `deploy/` packages the same pair
as one stack and reuses `18081`, so run either it or the standalone dashboard compose,
not both. From Windows, forward the ports:

```bash
ssh -N -L 18081:127.0.0.1:18081 hmc
ssh -N -L 18082:127.0.0.1:18082 -L 2261:127.0.0.1:2261 hmc
```

Tests: `cd dashboard && npm test` (71 Vitest tests in 11 files), `cd daemon && go test ./...`
(241 test functions, needs `../emulator`), `cd emulator && go test ./...` (36 test functions).
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
