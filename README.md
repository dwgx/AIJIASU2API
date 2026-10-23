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
blocks them at the repository root. Nothing in this tree has contacted a live AJiaSu endpoint.

## Current status

**Offline emulator, dashboard and daemon, plus a control-plane compose stack in `deploy/`; the
Resin M1 integration spike is PASS. No live vendor call, no gate; panel state survives a restart
only as a versioned JSON snapshot, not SQLite.**

As of 2026-10-23:

- `emulator/` — offline Tier-2 `FV` emulator: control API, FV crypto/frame, SOCKS5/HTTP front ends
  and the captured proxy carrier. It builds and passes its offline conformance tests; it makes no
  live calls and holds no credentials.
- `dashboard/` — operator console: React 19, Vite 6, TypeScript, 8 pages (Overview, Accounts,
  Nodes, Leases, Users, Requests, Logs, Settings) in the Obsidian Mint design. Mock data is the
  default; a live mode against `ajs-accountd` is opt-in at build time. 28 Vitest tests.
  HMC serves the mock build on `18081` and the live build on `18082`.
- `daemon/` — Go 1.26 `ajs-accountd` skeleton: the dashboard's JSON contract over an in-memory
  store persisted as a JSON snapshot, a Resin v1.3.0 admin client, publisher and runtime wiring
  (local subscription render, platform create/update, lease rotate through platform filter
  exclusion, node drain/undrain, probe-config patch), an AES-256-GCM sealed secret file, and the
  account-control plus FV session-factory seams. 153 Go test functions, mostly `httptest` fakes;
  the tests use no Resin process, no vendor account and no network.
- `deploy/` — Docker Compose control plane: `ajs-accountd` on host loopback `2261`, the dashboard
  on `127.0.0.1:18081`, and Resin v1.3.0 behind the optional `resin` profile. Both images build
  from this tree; the stack does not include the gate or node supervision. See
  [`deploy/README.md`](deploy/README.md).
- Resin M1 — **PASS** on HMC against `ghcr.io/resinat/resin:1.3.0`
  (`sha256:9aa94af706bfa7bc80c8fd78ed562f9a00d4e35e8323728cd17f06af43e665bf`): loopback SOCKS5 dial,
  IP-anchored sticky `(Platform, Account)` leases, rotation through platform filter exclusion,
  automatic re-selection after a node kill (3 failed requests over 6.0 s, first successful
  re-route at 12.6 s), and full state across a Resin restart. Measurements and caveats:
  [`research/77-m1-spike/REPORT.md`](research/77-m1-spike/REPORT.md).

**Not implemented — the status above must not be read as more than it is:**

- **Live AJiaSu login and smoke test.** The login path is wired behind `AJS_ACCOUNT_LOGIN=1`
  (`POST /v1/accounts/{id}/actions/login` calls the vendor control API), but it has only run
  against local fakes. A live login and smoke test are `[UNVERIFIED]` and remain blocked on an
  Owner-supplied account: no vendor host has been contacted from this tree.
- **The gate and `ajsproxy` per-node supervision.** Standalone `daemon/internal/gate` (SOCKS5
  CONNECT, admission, byte counters, drain/undrain) and `daemon/internal/supervisor` (per-node
  child start, readiness, events, stop) packages exist, but they are not wired together and
  there is no per-`(account, server)` node lifecycle yet.
- **SQLite / the full schema.** The daemon persists a versioned JSON snapshot (`AJS_STATE_FILE`,
  every 30s + shutdown) including sealed secrets, but it is not the SQLite schema in
  `docs/PANEL.md`; the snapshot is a v0.1 stopgap.
- **Packaged deployment beyond the control plane.** `deploy/compose.yml` builds and runs
  `ajs-accountd` + the dashboard (Resin optional), but the gate/supervisor node lifecycle is still
  absent, so the stack proxies no traffic; there is no macOS or Windows build.
- **Background reconciler.** Resin drift is corrected only by an explicit `POST /v1/reconcile`;
  probes are bookkeeping canaries, not network probes.

The product layer above the daemon — multi-account sessions, lease allocation, per-user access
and the gate — is designed but not implemented. See [`docs/PANEL.md`](docs/PANEL.md) for the build
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
cd daemon
AJS_LISTEN=127.0.0.1:2261 AJS_ADMIN_TOKEN=local-test-token go run ./cmd/ajs-accountd

# 3. Live dashboard build and static serve (from dashboard/, in a second shell)
VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 \
  VITE_PANEL_TOKEN=local-test-token npm run build
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

`local-test-token` is a loopback placeholder, not a secret. `VITE_*` values are baked into `dist/`
at build time, so the live token is readable by anyone who can fetch the bundle: keep
`ajs-accountd` bound to `127.0.0.1` and terminate real authentication in a BFF or reverse proxy.

The HMC build host serves three loopback-only endpoints: `18081` mock dashboard
(`docker compose up -d` in `dashboard/`), `18082` live dashboard (`vite preview` over a live
build), and `2261` `ajs-accountd`. Resin listens on `2260` when running. `deploy/` packages the
same pair as one stack and reuses `18081`, so run either it or the standalone dashboard compose,
not both. From Windows, forward the ports:

```bash
ssh -N -L 18081:127.0.0.1:18081 hmc
ssh -N -L 18082:127.0.0.1:18082 -L 2261:127.0.0.1:2261 hmc
```

Tests: `cd dashboard && npm test` (28 Vitest tests), `cd daemon && go test ./...` (needs
`../emulator`), `cd emulator && go test ./...`. All three suites run offline. The last recorded
dashboard and daemon runs are on HMC, alongside the M1 spike; see the evidence sections of
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

The `local-test-token` used in the commands above is a documented loopback placeholder, not a
credential. The live dashboard token is a build-time value, and the daemon, the dashboard and
Resin all stay bound to `127.0.0.1`.

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
