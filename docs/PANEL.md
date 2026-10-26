# PANEL — implementation design for `ajs-accountd` + Resin

Status: historical design baseline (2026-10-23); current implementation status is in README.md (JSON snapshot, direct per-node gates, no ajsproxy child) · Date: 2026-10-23 · License: MIT
Companion research: `research/70-panel/REPORT.md` (evidence, candidate matrix, falsification)

This document is the build sheet: components, API surface, data model, phases, and acceptance
checks. It assumes the decision recorded in the report — **Resin v1.3.0 runs unmodified** as the
control plane, and this repository adds one Go daemon, `ajs-accountd`, plus a thin console.

## 1. Shape

```
client ──▶ Resin :2260 ──▶ gate :13000+n ──▶ ajsproxy :12000+n ──▶ FV data channel ──▶ target
              ▲                  ▲                    ▲
              │ local sub        │ semaphore, bytes,  │ one process per (account, server)
              │ "ajs-live"       │ drain              │
        ajs-accountd :2261 ──────┴────────────────────┘
              │  account vault, supervisor, publisher, rotation, usage
              └─ SQLite ajs.db
```

Invariants:

- Every listener binds `127.0.0.1` unless the operator explicitly widens it.
- `ajs-accountd` never writes Resin's SQLite files; it only uses Resin's HTTP API.
- `ajsproxy` is used as shipped. Phase 1 does not modify `emulator/`.
- Credentials live only in `ajs.db` (sealed) and process memory; never in logs, argv, or the repo.

## 2. Ports and processes

| Range | Owner | Notes |
|---|---|---|
| `2260` | Resin | panel, admin API, HTTP/SOCKS5/reverse proxy |
| `2261` | `ajs-accountd` | admin API, `/healthz`, `/metrics` |
| `12000–12999` | `ajsproxy` | one listener per node, no auth, loopback only |
| `13000–13999` | `ajs-accountd` gate | SOCKS5 front for Resin, one per node |

Port slots are allocated once per `(account, server)` and persisted in `ports`. Reusing the slot
keeps Resin's node hash and health history stable across restarts. Slot exhaustion is a hard error,
not a silent reallocation.

## 3. `ajs-accountd` HTTP API

All routes are loopback-only and require `Authorization: Bearer $AJS_ADMIN_TOKEN` (empty token
disables auth and is refused unless `AJS_ALLOW_INSECURE=1`). Every unsafe admin request must also
send `X-AJS-CSRF: 1`, and an `Origin` header must exactly match `AJS_ADMIN_ORIGINS`; non-browser
clients omit `Origin` but still send the header. In the production kit nginx adds per-operator HTTP
Basic auth and forwards the authenticated username as `X-AJS-Operator`; the daemon uses it only as
the audit actor (falling back to `operator` when absent), never as an authentication factor.

### Accounts

| Method | Path | Body / query | Effect |
|---|---|---|---|
| GET | `/v1/accounts` | `?state=&limit=&offset=` | list, secrets never returned |
| POST | `/v1/accounts` | `{label, username, password, concurrency, platform, servers_allow?, regions?, expires_at?}` | seal password, create account, trigger login |
| GET | `/v1/accounts/{id}` | — | account + derived state |
| PATCH | `/v1/accounts/{id}` | partial | update entitlement; password rotation allowed |
| DELETE | `/v1/accounts/{id}` | — | drain all nodes, delete record, keep audit |
| POST | `/v1/accounts/{id}/actions/login` | — | force re-login |
| POST | `/v1/accounts/{id}/actions/logout` | — | drain + stop sessions |
| GET | `/v1/accounts/{id}/servers` | — | last `Servers` snapshot |

### Nodes

| Method | Path | Effect |
|---|---|---|
| GET | `/v1/nodes` | `?account_id=&state=&region=` — node, port, egress IP, region, probe age |
| POST | `/v1/nodes/{id}/actions/drain` | stop accepting new flows, keep in-flight, then unpublish |
| POST | `/v1/nodes/{id}/actions/undrain` | return to service if the session is alive |
| POST | `/v1/nodes/{id}/actions/probe` | force canary, return latency + egress IP if known |

### Users (panel identities)

| Method | Path | Effect |
|---|---|---|
| GET/POST | `/v1/users` | list/create; `{name, platform, account_identity, sticky_ttl, quota_bytes_month?}` |
| PATCH/DELETE | `/v1/users/{id}` | update/remove; removal drains leases and disables the Platform |
| POST | `/v1/users/{id}/rotate` | `{avoid_current_ip?: true}` — delete lease, optional exclusion window |
| GET | `/v1/users/{id}/usage` | `?from=&to=&group_by=day` — bytes, requests |
| POST | `/v1/users/{id}/suspend` | kill switch: Platform filters `["!.*"]` |

### Operations

| Method | Path | Effect |
|---|---|---|
| GET | `/healthz` | process liveness + account-state counts only |
| GET | `/readyz` | readiness: state-directory write probe + configured integrations; `200 ready` or `503 not_ready` with a reason list |
| GET | `/metrics` | Prometheus text |
| GET | `/v1/topology` | rendered subscription, node→Resin hash map, platform list |
| POST | `/v1/reconcile` | force re-render + PATCH + refresh |
| GET | `/v1/audit` | `?limit=&subject=` |
| POST/DELETE | `/v1/apikeys` | issue/revoke panel API keys (hashed at rest) |

Errors are JSON `{"error":{"code":"...","message":"..."}}` with codes `INVALID_ARGUMENT`,
`NOT_FOUND`, `CONFLICT`, `UPSTREAM_UNAVAILABLE`, `QUOTA_EXCEEDED`.

## 4. Resin API calls (outbound)

Base `http://127.0.0.1:2260`, header `Authorization: Bearer $RESIN_ADMIN_TOKEN`. Verified routes
from `internal/api/server.go`.

| Purpose | Call |
|---|---|
| Ensure subscription | `GET /api/v1/subscriptions?keyword=ajs-live`, else `POST /api/v1/subscriptions` with `{name:"ajs-live", source_type:"local", content:"...", update_interval:"30s", ephemeral:true}` |
| Publish topology | `PATCH /api/v1/subscriptions/{id}` `{"content":"..."}` (triggers an immediate async update for local subs); `POST /api/v1/subscriptions/{id}/actions/refresh` is optional for a deterministic sync point |
| Ensure user platform | `GET /api/v1/platforms`, else `POST /api/v1/platforms` `{name, sticky_ttl, regex_filters, region_filters, allocation_policy, passive_circuit_breaker_disabled}` |
| Rotation | `DELETE /api/v1/platforms/{id}/leases/{account}` |
| Inspect lease | `GET /api/v1/platforms/{id}/leases/{account}` |
| Node egress | `GET /api/v1/nodes/{hash}` → `egress_ip`, `egress_region` |
| Usage reconciliation | `GET /api/v1/request-logs?platform_id=&account=&from=&to=` (paged) |

Publishing rules: coalesce to at most one PATCH+refresh per 5 s; never publish an empty
subscription while nodes are alive (a transient empty publish would evict the pool); if the PATCH
succeeds and the refresh fails, retry the refresh with backoff and keep the last known-good content.

## 5. Subscription line format

```
socks5://127.0.0.1:<gate_port>#<cc>-<subdiv>-<seq>
```

Examples: `socks5://127.0.0.1:13007#cn-zj-03`, `socks5://127.0.0.1:13011#hk-01`.
Resin renders the tag as `ajs-live/cn-zj-03`, which is what Platform `regex_filters` match.
No credentials in the line: the gate is loopback-only and defaults to `AJS_GATE_AUTH=userpass`; Resin carries the panel user id and API-key secret in its username/password fields. `AJS_GATE_AUTH=loopback` is the explicit no-auth opt-in.

Tag derivation: country and subdivision come from the vendor server title/label, normalised to
lowercase ASCII; `seq` is a stable per-`(account, server)` counter so tags do not churn when the
server list is reordered.

## 6. Data model (`ajs.db`, SQLite WAL)

```sql
CREATE TABLE accounts (
  id TEXT PRIMARY KEY, label TEXT UNIQUE NOT NULL,
  username TEXT NOT NULL, secret BLOB NOT NULL,          -- AES-256-GCM(nonce||ct)
  concurrency INTEGER NOT NULL CHECK (concurrency BETWEEN 1 AND 2000),
  platform TEXT NOT NULL DEFAULT 'android',
  servers_allow TEXT, regions TEXT,                       -- JSON arrays or NULL
  enabled INTEGER NOT NULL DEFAULT 1,
  expires_at INTEGER, state TEXT NOT NULL, last_error TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);

CREATE TABLE servers (
  account_id TEXT NOT NULL, server_id TEXT NOT NULL,
  addr TEXT, title TEXT, region_tag TEXT, fv_addr TEXT,
  status TEXT NOT NULL, first_seen INTEGER, last_seen INTEGER,
  PRIMARY KEY (account_id, server_id)
);

CREATE TABLE nodes (
  id TEXT PRIMARY KEY,                                    -- "<account_id>:<server_id>"
  account_id TEXT NOT NULL, server_id TEXT NOT NULL,
  slot INTEGER UNIQUE NOT NULL,
  state TEXT NOT NULL,                                    -- STARTING|READY|LIVE|DRAINING|DOWN
  egress_ip TEXT, region TEXT,
  fail_count INTEGER NOT NULL DEFAULT 0,
  last_probe_at INTEGER, last_ok_at INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE ports (slot INTEGER PRIMARY KEY, node_id TEXT UNIQUE NOT NULL, internal_port INTEGER, gate_port INTEGER);

CREATE TABLE users (
  id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL,
  platform_name TEXT UNIQUE NOT NULL, account_identity TEXT NOT NULL,
  sticky_ttl TEXT NOT NULL DEFAULT '8h',
  quota_bytes_month INTEGER, enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);

CREATE TABLE usage_daily (
  day TEXT NOT NULL, user_id TEXT NOT NULL, account_id TEXT NOT NULL, node_id TEXT NOT NULL,
  ingress_bytes INTEGER NOT NULL DEFAULT 0, egress_bytes INTEGER NOT NULL DEFAULT 0,
  requests INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, user_id, account_id, node_id)
);

CREATE TABLE apikeys (id TEXT PRIMARY KEY, user_id TEXT, hash BLOB NOT NULL, prefix TEXT NOT NULL, created_at INTEGER, revoked_at INTEGER);
CREATE TABLE audit (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, actor TEXT, action TEXT NOT NULL, subject TEXT, detail TEXT);
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
```

Secret sealing: AES-256-GCM with a 32-byte key from `AJS_MASTER_KEY` (hex/base64) or the OS keyring.
If neither is available the daemon refuses to start. The sealed blob is bound to the account id as
additional authenticated data so rows cannot be swapped.

## 7. Configuration

| Variable | Default | Meaning |
|---|---|---|
| `AJS_LISTEN` | `127.0.0.1:2261` | admin API |
| `AJS_ADMIN_TOKEN` | — | required unless `AJS_ALLOW_INSECURE=1` |
| `AJS_STATE_DIR` | `./data` | `ajs.db`, logs |
| `AJS_MASTER_KEY` | keyring fallback | 32-byte secret-encryption key |
| `AJS_AJSPROXY_BIN` | `./ajsproxy` | path to the emulator binary |
| `AJS_RESIN_URL` | `http://127.0.0.1:2260` | control plane |
| `AJS_RESIN_ADMIN_TOKEN` | — | Resin admin bearer |
| `AJS_INTERNAL_PORT_BASE` | `12000` | ajsproxy listener range |
| `AJS_GATE_PORT_BASE` | `13000` | gate listener range |
| `AJS_PUBLISH_MIN_INTERVAL` | `5s` | publish coalescing |
| `AJS_CANARY_INTERVAL` | `60s` | gate canary period |
| `AJS_CANARY_TARGET` | `www.cloudflare.com:443` | canary destination |
| `AJS_LOGIN_BACKOFF` | `30s` | initial backoff, doubling to 30 min |

Resin side, minimum: `RESIN_ADMIN_TOKEN`, `RESIN_PROXY_TOKEN`, `RESIN_LISTEN_ADDRESS=127.0.0.1`,
`RESIN_PORT=2260`, the three state/cache/log directories, and `RESIN_PROXY_BYPASS` covering loopback
and LAN targets so those are dialed directly instead of through a pool node.

## 8. Lifecycle and failure rules

- Account login: 3 attempts per 15 min, then `AUTH_FAILED`; transport errors use exponential
  backoff and never mark credentials bad.
- Node start: `ajsproxy` must exit zero on FV auth within 20 s; one retry after a fresh login.
- Node publish: only `LIVE` nodes; a node is `LIVE` after one successful canary.
- Drain: gate rejects new flows, in-flight allowed for 30 s, then `ajsproxy` is stopped and the line
  removed. Leases for affected accounts are deleted before the line disappears.
- Rotation with `avoid_current_ip`: temporarily add `!^ajs-live/<tag>$` to the user Platform,
  refresh, delete lease, restore after 10 s. Rate limit: one rotation per user per 10 s.
- Suspend: Platform `regex_filters=["!.*"]` → empty routable view → all requests fail closed.
- Resin unavailable: keep sessions, stop publishing, expose `resin_up=0`; retry every 10 s.

## 9. Phases

| Phase | Deliverable | Acceptance |
|---|---|---|
| P0 (manual spike, 1–2 days) | Resin + one hand-started `ajsproxy` + hand-written local subscription | two Resin identities get two leases; killing `ajsproxy` re-routes within one probe cycle |
| P1 (daemon, ~1 week) | `ajs-accountd`: vault, supervisor, gate, publisher, drain, rotate, `/metrics` | 2 accounts × 10 servers stable for 24 h; concurrency cap never exceeded; drain loses no in-flight request |
| P2 (console, ~1 week) | accounts/users/nodes/usage/audit UI, API keys, soft quotas | an operator can add an account and hand a user credentials without touching a shell |
| P3 (gateway, only if required) | per-user passwords, IP allowlist, per-user rate/concurrency, hard quota | a revoked user is blocked in <1 s at the gateway, independent of Resin |

P3 is explicitly out of scope unless the Owner answers open question 2 with "yes". Until then the
proxy token is shared and per-user enforcement is advisory.

## 10. Decision update — competitive evaluation (2026-10-23)

A second, wider search (`research/71-panel-alt/REPORT.md`) covered 78 repositories and assessed 23;
no candidate beats Resin as the base. The top three are Resin (MIT), Rota (Apache-2.0, but its
`stick` is an N-request counter, not an IP-anchored lease) and ProxyCat (GPL-2.0, no account lease).
Resin stays.

A source audit of Resin v1.3.0 (`research/72-resin-audit/REPORT.md`) confirmed **10 of 10**
integration claims this design depends on: local subscription `content` + `PATCH`, loopback SOCKS
nodes, `(Platform, Account)` leases anchored on egress IP, no per-user passwords/quotas,
cross-platform release assets, and MIT.

The build-vs-reuse scoring (`research/73-build-vs-reuse/DECISION.md`) recommends **hybrid D at 77.0**
against A 75.0, C 72.0 and B 46.0: Resin unmodified plus a thin `ajs-gateway`/console for per-user
auth, IP allowlist, concurrency/RPS, byte quota and pinned single-socket node switching, with
`ajs-accountd` unchanged. **This is a conditional recommendation, not a settled decision:** D beats A
only if per-user auth/quota or single-socket switching are v1 requirements; otherwise A (Resin alone)
is about two weeks cheaper. Either way, gate the choice on the 2-day M1 spike (Resin + one `ajsproxy`
+ a hand-written subscription).

Evidence: [`../research/71-panel-alt/REPORT.md`](../research/71-panel-alt/REPORT.md),
[`../research/72-resin-audit/REPORT.md`](../research/72-resin-audit/REPORT.md),
[`../research/73-build-vs-reuse/DECISION.md`](../research/73-build-vs-reuse/DECISION.md).

## 11. License and boundary

- Repository code: MIT, `Copyright (c) 2026 The LovePlusSpeedToProxy Authors` (root `LICENSE`).
- Resin: MIT, `Copyright (c) 2026 Resinat and contributors`. Run unmodified from the upstream
  image or release; if redistributed, ship its licence text and mark any modifications.
- No AGPL (`remnawave/panel`) and no unlicensed (`ProxyPoolHub`, `go_proxy_pool`) code is copied.
- **Boundary:** accounts the operator owns or is explicitly authorized to use. Commercial resale
  or automation only under a separate written vendor authorization covering the operator's
  accounts, the service being resold and the jurisdictions involved, kept on file; without it,
  single-operator self-use only — no resale, no sharing of access, no published endpoints, no
  bypassing vendor account limits, no hiding the origin of abusive traffic. The operator answers
  for the vendor's terms of service and for local law. Restated in README `## Use boundary` and
  `PLAN.md` §6 item 5.

## 12. Test plan

- Unit: tag normalisation, subscription rendering (golden file), port allocation, secret sealing
  round-trip, semaphore fairness, drain state machine.
- Integration (offline): fake Resin admin API + fake `ajsproxy` process; assert PATCH content,
  refresh coalescing, lease deletion order, kill switch.
- Integration (in-process): reuse the emulator's mock FV peer to drive gate → ajsproxy → target.
- Failure injection: kill `ajsproxy` mid-flow; make Resin return 500 on refresh; fill the semaphore;
  corrupt one `ajs.db` row.
- Live: only with an Owner-supplied account, recorded as observations; never in CI.
