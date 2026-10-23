# AJS-10 — Management panel and post-login live architecture

Status: design complete, no implementation · Date: 2026-10-23 · Owner: project Owner
Slice: AJS-10 (PanelDesign) · Branch: `panel-design` · Write scope: `research/70-panel/`, `docs/`, `LICENSE`, `README.md`, `PLAN.md`

Every claim below is tied to a source id from `sources.tsv` (GitHub API responses and files read on
2026-10-23) or to a file in this repository. Inferences are marked `[INFERENCE]`; things that could
not be checked are marked `[UNVERIFIED]`.

## Answer

- Run **Resin v1.3.0 unmodified** (MIT, Go, 2,482 stars) as control plane: proxy entry, panel, sticky leases.
- Integrate without a fork: `ajs-accountd` PATCHes a local subscription of `socks5://127.0.0.1:<gate>#<region>` lines.
- Build **`ajs-accountd`**: account vault, session supervisor, per-account concurrency gate, metering, drain/rotate API, SQLite.
- Keep **`ajsproxy`** unchanged: one process per `(account, server)`; its loopback SOCKS5 listener is the node.
- Resin anchors leases on **egress IP**, so several accounts reaching one node IP back a single sticky pool.
- Gaps are explicit: per-user passwords, IP allowlists, hard quotas and multi-IP identities are phase 2.
- MIT for our code, Resin MIT with notice, AGPL/unlicensed reference-only; self-use, authorized accounts only.

## Method and evidence

- Repo metadata (stars, license, language, last push, default branch, release assets) came from the
  GitHub REST API on **2026-10-23** (`sources.tsv` S01, S13, S15, S17, S19, S21, S22, S23, S24).
- Resin was read from source at `master` (the default branch; `main` does not exist): `README.md`,
  `DESIGN.md`, `LICENSE`, `go.mod`, `docker-compose.yml.example`, `Dockerfile`, and the API handlers
  `server.go`, `middleware.go`, `handler_lease.go`, `handler_subscription.go`, `handler_node.go`,
  `internal/model/models.go`, `internal/service/control_plane_subscription.go`,
  `internal/subscription/parser.go`.
- The emulator was read locally: `emulator/README.md`, `emulator/cmd/ajsproxy/main.go`,
  `emulator/internal/config/config.go`, `emulator/internal/fv/transport.go`,
  `emulator/internal/proxy/socks5.go`.
- No candidate repository was cloned; no live AJiaSu account was used; no request was sent to the
  vendor service. Star counts are a single snapshot, not a trend.
- Numbers in this report are stated with their denominator and date: "2,482 stars (GitHub API,
  2026-10-23)" means exactly one observation of `stargazers_count` on that date.

## Candidate matrix

Stars are `stargazers_count` from the GitHub API on 2026-10-23; "license" is the SPDX id GitHub
reports, cross-checked against the repository's `LICENSE` file where one exists.

| Repo | Stars | License | Stack | What it actually does | Fit for account-based AJiaSu proxies | Verdict |
|---|---|---|---|---|---|---|
| `Resinat/Resin` | 2,482 | MIT | Go 1.26, embedded WebUI, SQLite (no external DB) | Proxy-pool gateway: subscriptions → node pool → HTTP/SOCKS5/reverse entrypoint; `Platform` + `Account` sticky leases anchored on egress IP; P2C scheduling; active+passive circuit breaker; GeoIP region filters; per-request structured logs; admin API + dashboard | **Strong.** Its lease model is IP-stickiness, exactly what an AJiaSu egress is; one local SOCKS5 per FV session maps 1:1 to a node; tags and region filters cover province/country selection; request logs already carry `account`, `platform_id`, `node_hash`, `egress_ip`, `ingress_bytes`, `egress_bytes` | **Reuse unmodified** |
| `alpkeskin/rota` | 769 | Apache-2.0 | Go + Next.js + TimescaleDB | Rotation engine: random/round-robin/least-conn/time-based rotation, per-user `user:pass@host`, per-user requests-per-minute cap, pools, GeoIP, JWT dashboard | Good rotation and per-user caps, but rotation-oriented (no IP-sticky lease per business account), and TimescaleDB is an extra stateful service | Reference; fallback control plane |
| `nssanc/ProxyPoolHub` | 0 | **none** (GitHub license API 404; no `LICENSE` in tree) | Go backend + React frontend per README (GitHub language: TypeScript) | Proxy list CRUD, sequential/random/least-used rotation, health checks, basic proxy auth, dashboard | Young, no license, rotation-only; no account/sticky model | **Avoid** (no license = all rights reserved) |
| `jhao104/proxy_pool` | 23,764 | MIT | Python, Redis | Collects and validates *free public* proxies, exposes a random-proxy API, scheduler | Wrong problem: free-proxy harvesting, not account-based egress; no auth model, no stickiness | Reference only |
| `remnawave/panel` | 5,244 | **AGPL-3.0** | TypeScript, Xray-core | Subscription/user panel for Xray: users, traffic limits, nodes, subscription links | Feature vocabulary is close (users, traffic, nodes), but the data plane is Xray and the license is network copyleft | Reference only — **no code copied** |
| `pingc0y/go_proxy_pool` | 777 | **none** | Go | Free-proxy pool, zero-dependency, last push 2023-05-03 | No license, unmaintained, wrong problem | **Avoid** |
| `nordsocks/selfhosted` | 0 | MIT | React + Node/Express + PostgreSQL | Panel to create/manage SOCKS5 proxies, JWT + 2FA, i18n | Generic SOCKS5 account panel; no pool, no stickiness, no egress-IP model, extra Postgres | Not a fit |
| `shizhewanglu/proxy-manager` | 0 | MIT | Python/FastAPI + sing-box + iptables | Single-host user panel: one port per user, per-user traffic accounting via iptables, expiry/quota auto-disable, Clash subscription | Good *pattern* for per-user metering and quota kill-switch, but single-host sing-box, not a proxy pool | Reference for quota UX |

Denominator note: 8 candidates assessed, 8 rows above; the brief listed 6 named plus 2 optional.
Two brief claims were corrected against the API: `nssanc/ProxyPoolHub` has **no license** and
**0 stars** (not MIT/small-but-live), and its GitHub language is TypeScript, not Go.

## Reuse decision

**Decision: run Resin v1.3.0 unmodified as the control plane, and integrate AJiaSu through a dynamic
local-node subscription.**

Mechanism, verified against source:

1. Resin subscriptions accept `source_type: local` with a `content` string, and `PATCH
   /api/v1/subscriptions/{id}` allows replacing `content` on a local subscription
   (S07 `control_plane_subscription.go`: `content: field is not allowed for remote subscription`;
   the local contract test creates local subs with `content`). Content is parsed as sing-box JSON,
   Clash YAML, or URI lines; `socks5://user:pass@host:port#tag` is a recognised URI line and its
   fragment becomes the node tag (S09 `parser.go`, `parseProxyURI`).
2. `ajs-accountd` therefore renders one line per live `(account, server)` gate and PATCHes the
   subscription. No Resin patch, no plugin, no reverse-engineered extension point.
3. Node tags follow Resin's `<SubscriptionName>/<Tag>` convention (S03, "节点的 Tag"), so a Platform
   regex can select `^ajs-live/cn-zj-.*` while `region_filters` handles ISO country from GeoIP.
4. Sticky identity is `Platform.Account` supplied as proxy username `Platform.Account` with password
   `RESIN_PROXY_TOKEN` (S03; S10 `middleware.go` is admin-only, proxy auth is separate). Leases are
   per `(Platform, Account)`, fixed expiry, anchored on egress IP (S03 "租约生命周期").

**Why not a fork.** The value in Resin is the hot path — P2C selection, lease tables, circuit
breaker, probe scheduling, log pipeline, WebUI. Forking would put us on the hook for that code
forever to change maybe 300 lines of integration surface. The subscription is a supported public
interface with tests (`subscription_local_contract_test.go`), so the integration can be upgraded
independently: if Resin changes, we re-read one schema, not re-merge a tree.

**Why Resin fits this specific service.** AJiaSu nodes are shared egress IPs reached through
per-account FV sessions. Resin's sticky anchor is the *egress IP*, not the node, and same-IP
rotation swaps nodes that share an IP (S03 "同 IP 轮换细节"). Two accounts that can reach the same
AJiaSu server are therefore redundant carriers of one sticky identity: when one session dies, the
lease survives on the other account's node with the same IP. A rotation-only pool (rota,
ProxyPoolHub) cannot express that.

**Attribution obligations.**

- Resin is MIT (S11: `MIT License`, `Copyright (c) 2026 Resinat and contributors`). Running the
  published container image or release binary is not redistribution; if we ship Resin inside our own
  image or vendor its source, we must include the MIT text and that copyright line, and mark any
  modifications. We plan neither — the compose file pins `ghcr.io/resinat/resin:v1.3.0` (S02, S14).
- `rota` is Apache-2.0: if any of its code were copied, we would need the Apache text, a `NOTICE`,
  and a statement of changes. We copy none.
- `remnawave/panel` is AGPL-3.0: code reuse would impose network copyleft on our service. Reference
  only, no code copied, no assets copied.
- `ProxyPoolHub` and `go_proxy_pool` have no license file, so no reuse of any kind.

**Fallback if Resin proves unusable.** Trigger conditions, in order: (a) Resin cannot dial a
`127.0.0.1` node because of an undocumented restriction; (b) local subscription refresh cannot keep
up with churn at our node counts; (c) the lease model cannot be made to respect vendor concurrency.
Fallback is `rota` (Apache-2.0, per-user caps, but needs a Postgres) plus our own sticky-lease
layer; last resort is a small Go control plane of our own (~1,500 lines) with the same API shape
described in `docs/PANEL.md`. `[INFERENCE]` none of (a)–(c) is expected: `socks5://127.0.0.1:port`
parses as an ordinary outbound, and Resin's own outbound test dials a `127.0.0.1` node on the wire
(S31).

## Architecture

Components, all on one host by default:

```
                    ┌─────────────────────────── host: Linux (primary) / macOS / Windows ───────────────────────────┐
 client             │                                                                                              │
 (user:pass) ──────▶│  Resin :2260 (unmodified, MIT)                                                               │
 HTTP / SOCKS5      │   ├─ proxy auth  Platform.Account : RESIN_PROXY_TOKEN                                         │
                    │   ├─ Platform  → routable nodes (tag regex + ISO region), P2C, circuit breaker               │
                    │   ├─ Lease     (Platform, Account) → egress IP, fixed TTL                                   │
                    │   ├─ probes    egress IP + per-domain latency (TD-EWMA)                                      │
                    │   └─ request logs (account, node, egress_ip, bytes) + metrics + WebUI + admin API            │
                    │            ▲  local subscription "ajs-live" (content PATCHed, no HTTP listener)              │
                    │            │                                                                                 │
                    │  ┌─────────┴───────────────┐        admin API 127.0.0.1:2261                                │
                    │  │ ajs-accountd  :2261     │◀──────────────────────────  ajs-panel (phase 3, static SPA)     │
                    │  │  account vault          │                                                              │
                    │  │  session supervisor     │── spawn ──▶ ajsproxy :12000+  (one per account+server)        │
                    │  │  gate relays :13000+    │                 │ FV auth (binary carrier v2)                  │
                    │  │  SQLite ajs.db          │                 ▼                                              │
                    │  └─────────────────────────┘        login.ajs-app.com → svr:par.fv.p                        │
                    └──────────────────────────────────────────────────────────────────────────────────────────────┘
```

**`ajsproxy` (existing, unchanged in phase 1).** One process per `(account, server)`, started as
`ajsproxy serve-socks -config <per-account config> -server <id> -listen 127.0.0.1:<internal>`. It
authenticates once (control API login → `loginSync` → `Servers` → FV auth), then opens one FV data
channel per proxied flow (`emulator/internal/fv/transport.go`: `OpenData` per flow; SOCKS5 front end
is no-auth and CONNECT-only). It is loopback-only, holds exactly one credential set, and exposes no
admin surface. `[INFERENCE]` Resin's SOCKS5 outbound passes the target hostname through to the node
rather than resolving it locally, so the FV CONNECT normally carries a domain; if a target arrives as
an IP literal, `ajsproxy` forwards it as a host string, which the vendor server may or may not accept.

**`ajs-accountd` (new, Go, MIT).** The only new daemon in phase 1:

| Responsibility | Detail |
|---|---|
| Account vault | `(label, username, password, entitlement)` in `ajs.db`; password sealed with AES-256-GCM under a key from `AJS_MASTER_KEY` or the OS keyring; never logged, never in the repo |
| Session supervisor | Starts/stops `ajsproxy` per `(account, server)`, restarts with exponential backoff, tracks `UNCONFIGURED → AUTH_PENDING → SYNCED → NODE_READY → LIVE → DRAINING → STOPPED` |
| Gate relay | Loopback SOCKS5 listener per node, in front of `ajsproxy`; enforces the account's purchased concurrency with a semaphore, meters bytes per account/node, and implements drain (stop accepting, finish in-flight) |
| Subscription renderer | Emits `socks5://127.0.0.1:<gate>#<region>-<seq>` lines and PATCHes Resin's `ajs-live` subscription; a content change triggers Resin's async update, an explicit `actions/refresh` is used only for a sync point; coalesced to ≤1 per 5 s |
| Rotation/drain API | `POST /v1/users/{u}/rotate` → Resin `DELETE /api/v1/platforms/{id}/leases/{account}`; `POST /v1/nodes/{id}/drain` |
| Usage | Per-account byte counters from the gate; per-user bytes reconciled from Resin request logs (`ingress_bytes`, `egress_bytes` per `account`) |
| Audit | Append-only `audit` table: admin actions, node lifecycle, quota decisions; no credentials, no target URLs by default |

**Why the gate exists.** `ajsproxy` has no auth, no limits, and no counters. The vendor's real
constraint is *concurrent connections per account*, and it spans all nodes of that account. Putting
one small SOCKS5 relay per node in front gives us exactly three things Resin cannot provide by
itself: an account-wide concurrency semaphore, per-account byte metering, and a drain primitive that
does not require killing a process mid-flow. The cost is one loopback hop per connection.

**Resin (unmodified).** Owns everything multi-user-facing: entrypoint, Platform/Account identity,
lease stickiness, node health and circuit breaking, GeoIP, request logs, metrics, WebUI, admin API
(Bearer `RESIN_ADMIN_TOKEN`). Its SQLite files (`state.db`, `cache.db`, rolling request-log DBs) stay
its own; we never write them.

**Storage.** Two independent stores: Resin's directories (`RESIN_STATE_DIR`, `RESIN_CACHE_DIR`,
`RESIN_LOG_DIR`) and `ajs.db` (SQLite, WAL) owned by `ajs-accountd`. No Redis, no Postgres, no
external queue. Both are single-file backups.

**Auth model.** Three layers, deliberately separate: Resin admin token (panel/API), Resin proxy token
+ `Platform.Account` username (data plane), `ajs-accountd` admin token bound to loopback (account
management). Phase 1 has **one** proxy token shared by all users; per-user passwords are phase 2.

**Observability.** Resin: `/api/v1/request-logs` (per account/platform/node/egress_ip/bytes),
`/api/v1/metrics/*` (throughput, connections, leases, node pool), `/healthz`. `ajs-accountd`:
`/metrics` (Prometheus text), `/healthz`, JSON logs with account label and node id, credential
redaction enforced by a single formatting helper.

## Post-login live flow

### Account state machine

```
UNCONFIGURED
    │ add account (label, user, pass, entitlement)
    ▼
AUTH_PENDING ──transport error──▶ BACKOFF (30s·2^n, cap 30m) ──▶ AUTH_PENDING
    │ AuthResult == 0
    ▼
SYNCED  (loginSync + Servers cached)
    │ spawn ajsproxy per entitled server, FV auth OK, gate up
    ▼
NODE_READY(n) ──first canary OK──▶ LIVE
    │                                │ server gone / operator / quota / session lost
    │                                ▼
    │                             DRAINING ──grace 30s──▶ STOPPED
    │
    ├── AuthResult != 0 (3 tries/h) ──▶ AUTH_FAILED (terminal, alert)
    └── membership expired ────────────▶ SUSPENDED (drain all nodes)
```

`DEGRADED` is `LIVE` with fewer than the expected nodes; it is a counter, not a state that changes
routing, because Resin already removes dead nodes from the routable view.

### Sequence (one account, one node)

1. **Login.** `ajsproxy`/`ajs-accountd` calls the control API `POST /client.php` on the first
   reachable host of `{login.ajs-app.com, login.mmegg.com, login.mmegg.com:4443,
   login.mbclubsh.com}` (emulator `internal/config/config.go: DefaultHosts`). Failover happens only
   on transport errors, matching the emulator's client. Result: `AuthResult`, `UserName`, membership.
2. **Sync.** `loginSync` returns membership fields, including the purchased concurrency where the
   service exposes it; otherwise the operator records the number in `ajs.db` at account creation.
3. **Server list.** `Servers` returns `id → {addr, sts, title, svr:par.fv.p}`. Keep `sts == "ok"` and
   a non-empty `svr:par.fv.p`; apply `servers_allowed` / region entitlement.
4. **Bind ports.** Look up `(account, server) → internal_port` in `ajs.db`; allocate
   `127.0.0.1:12000+n` on first use and keep it stable. Stable ports keep Resin's node hash and
   health history stable across restarts (`NodeStatic.Hash` is derived from node options).
5. **Start FV session.** Spawn `ajsproxy serve-socks -config <acct> -server <id> -listen
   127.0.0.1:<internal_port>`. It performs login, `loginSync`, FV authentication (binary carrier,
   protocolVersion 2) and then serves one data channel per flow. Auth timeout 20 s.
6. **Open the gate.** `ajs-accountd` listens on `127.0.0.1:13000+n` (SOCKS5, no-auth, CONNECT-only)
   and forwards to the internal port under the account semaphore.
7. **Canary.** Through the gate, CONNECT to `www.cloudflare.com:443` with a 10 s timeout. Success
   means the FV path carries real traffic. Record latency; the *egress IP* is learned by Resin's own
   probe, not guessed here.
8. **Publish.** Render `socks5://127.0.0.1:<gate>#<region>-<seq>` for every `NODE_READY` node and
   `PATCH /api/v1/subscriptions/{id}` with `{"content": "..."}` on the local subscription
   `ajs-live`. A content change already triggers an asynchronous update
   (`control_plane_subscription.go` → `go s.Scheduler.UpdateSubscription(sub)`, which for
   `source_type=local` reads the stored content directly and diffs it in
   `internal/topology/subscription_scheduler.go`). The explicit
   `POST /api/v1/subscriptions/{id}/actions/refresh` is optional and used only when a deterministic
   completion point is wanted. Coalesce publishes to at most one per 5 s; Resin's own minimum
   subscription interval is 30 s.
9. **Onboarding window.** New Resin nodes start circuit-open and become routable only after a
   successful egress probe plus latency data; the probe scanner runs every 13–17 s, so expect
   **15–30 s** from publish to routable `[INFERENCE from DESIGN.md probe scheduling]`.
10. **Client request.** Client sends `Platform.Account:RESIN_PROXY_TOKEN` as proxy credentials.
    Resin resolves the Platform, checks the lease table: expired → drop; miss → P2C pick and create a
    lease with `sticky_ttl`; hit with a routable node and unchanged egress IP → reuse. It then dials
    the gate as a SOCKS5 node, the gate acquires a slot, dials `ajsproxy`, and `ajsproxy` opens one
    FV data channel to the target and relays.
11. **Rotation.** `POST /v1/users/{u}/rotate` → Resin
    `DELETE /api/v1/platforms/{pid}/leases/{account}`. The next request re-picks. To guarantee a
    *different* egress IP, `ajs-accountd` additionally PATCHes the Platform's `regex_filters` with a
    temporary `!` rule for the current tag(s), refreshes the routable view, deletes the lease, then
    restores filters after 10 s. This is a cold-path operation (Resin rebuilds the Platform view on
    filter change) and must be rate-limited to one rotation per user per 10 s.
12. **Release / expiry.** Lease expiry is fixed and never renewed; Resin sweeps expired leases every
    13–17 s and decrements `IPLoadStats`. Explicit release is the same `DELETE`. Node removal is
    always: drain gate → delete affected leases → stop `ajsproxy` → remove subscription line →
    refresh.

### Failure handling

| Failure | Detection | Reaction |
|---|---|---|
| Control host unreachable | transport error on login | Try next host; if all fail, `BACKOFF` 30 s·2^n (cap 30 min); existing sessions keep serving |
| Wrong password / banned account | `AuthResult != 0` | `AUTH_FAILED`, at most 3 attempts/hour, alert; no retry storm |
| FV auth rejected or timed out | `ajsproxy` exits non-zero | One retry after a fresh login; second failure → node `DOWN`, remove from subscription |
| Server disappears from `Servers` | list diff | Drain node, remove line, refresh |
| Vendor concurrency rejection | gate sees connect failure from `ajsproxy` | Queue up to 2 s, then SOCKS5 reply `0x01`; count as `quota_reject`, **not** node failure |
| Repeated flow failures | gate/Resin passive signal | Resin circuit breaker removes the node; `ajs-accountd` replaces it if capacity allows |
| Node overloaded by one account | semaphore saturation | Reject fast; the per-node soft cap keeps other nodes usable |
| Resin restarts | `/healthz` fails | `ajs-accountd` keeps sessions but pauses publishing; Resin restores state and leases from `state.db` |
| Host reboot | process start | Restore ports from `ajs.db`, re-login all accounts, republish; expect a re-probe window before traffic |
| Membership expired | periodic `loginSync` (30 min) | `SUSPENDED`, drain all nodes, alert |

### Health checks

- **Gate canary** (ours): every 60 s per node, SOCKS5 CONNECT to `www.cloudflare.com:443` through
  the gate, 10 s timeout. Three consecutive failures → `DOWN`. This consumes vendor concurrency for
  a few hundred milliseconds; keep it at 60 s, not 10 s.
- **Resin active probes**: egress probe via `https://cloudflare.com/cdn-cgi/trace` and per-domain
  latency probes, scheduled by its 13–17 s scanner; passive sampling from real traffic feeds
  TD-EWMA latency and the circuit breaker.
- **Session liveness**: `loginSync` every 30 min per account; `ajsproxy` process exit is the
  strongest signal and is handled by the supervisor immediately.
- **Do not double-probe aggressively.** Two independent probers (ours + Resin) are deliberate, but
  ours is coarse (TCP reachability) and infrequent; Resin's is authoritative for routing.

## Multi-account allocation

### Entitlements

An **account** carries: `concurrency` (vendor-purchased, 1–2000), `servers_allowed` (all or an
allowlist), `regions` (province/country labels), `platform` (`_m`: android/windows/ios), `expires_at`,
`enabled`. A **node** is `(account, server)`; supply is `Σ accounts × entitled servers`. A **user** is
one Resin `Platform.Account` identity bound to exactly one Platform.

### Quotas and their enforcement point

| Quota | Scope | Enforced by | Phase |
|---|---|---|---|
| Concurrent FV flows | per account, across all its nodes | gate semaphore in `ajs-accountd` | 1 |
| Per-node flow share | per node | soft cap `ceil(concurrency / live_nodes)`, burst to account limit | 1 |
| Sticky egress IPs | per user identity | Resin: exactly one lease per `(Platform, Account)` | 1 |
| Traffic (bytes) | per user | Resin request logs; advisory in phase 1, hard in phase 2 gateway | 1 advisory / 2 hard |
| Requests per second | per user | not in Resin; phase-2 gateway | 2 |
| Concurrent connections | per user | not in Resin; phase-2 gateway | 2 |

**Consequence to state plainly:** one identity = one sticky IP. A user who needs *N* simultaneous
egress IPs needs *N* Resin Account identities (`alice-a`, `alice-b`, …) mapped to one panel user.
This is a property of Resin's lease key, not a bug we can configure away.

### Filters and node entitlement

- Country: Resin `region_filters` (ISO 3166-1 alpha-2, lowercase) resolved from GeoIP on the
  probed egress IP.
- Province / city / carrier: tag regex (`^ajs-live/cn-zj-\d+$`) because GeoIP cannot express Chinese
  provinces. Tags are derived from the vendor server title at publish time and kept stable.
- Platform OS: the server list can differ by the `_m` device parameter; phase 1 pins one platform
  per account and tags nodes with it. Mixing platforms in one pool needs one `ajsproxy` config per
  platform `[INFERENCE: not verified live]`.
- Never mix accounts with different trust or billing in one Platform: the Platform is the isolation
  boundary, and `passive_circuit_breaker_disabled` is a Platform setting.

### Scheduling

- **Sticky first**: a live lease with a routable node and unchanged egress IP wins, always.
- **Same-IP rotation**: if the node died but another node (usually another account) exposes the same
  egress IP, Resin swaps in place and keeps the lease and its expiry.
- **P2C otherwise**: two random candidates, lower score wins. `BALANCED` (default) scores
  `(LeaseCount + 1) × Latency`; `PREFER_IDLE_IP` scores `LeaseCount`; `PREFER_LOW_LATENCY` scores
  `Latency`. Latency is only comparable when both candidates have a common authoritative domain
  sample; otherwise the score falls back to `LeaseCount`.
- **Account spreading**: the gate's per-node soft cap is the backstop. Without it, a single popular
  egress IP can consume a whole account's concurrency and starve the other nodes of that account.

### Circuit breaker policy

- Keep Resin's global breaker on for active probes — a node that cannot reach Cloudflare is dead.
- Set `passive_circuit_breaker_disabled=true` on Platforms backed by a **single account**, because a
  vendor capacity rejection at the gate would otherwise look like a node fault and evict a healthy
  node. On multi-account Platforms, leave passive breaking on: a failing node there is usually a
  genuinely dead session, and other accounts cover the IP.
- Our own breaker: 3 consecutive canary failures or 5 process exits in 5 minutes → node `DOWN`;
  account-level breaker after 3 failed logins in 15 minutes → `BACKOFF`, not `AUTH_FAILED`.

### TTL, drain, kill switch

- `sticky_ttl` by tier: `30m` for short interactive jobs, `8h` for long sessions, `168h` (Resin's
  default) for appliances. Shorter TTL spreads leases faster; it also rotates users off good IPs.
- Drain order is fixed: gate draining (new flows rejected, in-flight allowed) → delete leases for
  affected accounts → wait ≤30 s → stop `ajsproxy` → remove the subscription line → refresh. Doing
  it in the other order produces in-flight errors.
- **Kill switch** for a user: set that Platform's `regex_filters` to `["!.*"]`. The routable view
  becomes empty and every request fails with Resin's routing error while the rest of the system is
  untouched. It is reversible in one PATCH and leaves an audit trail.
- **Kill switch** for an account: `enabled=false` → drain all its nodes → remove lines. Vendor-side
  revocation is the operator's job, not ours.

## Panel feature matrix

"Webshare-like" here means the feature vocabulary of a commercial rotating-proxy panel, not its
business model. ✔ = native, ◐ = partial, ✘ = absent.

| Feature | Expectation | Resin (unmodified) | `ajs-accountd` | New UI (phase 3) |
|---|---|---|---|---|
| Proxy list | endpoints, region, health, latency | ✔ nodes, leases, metrics, WebUI | — | optional skin |
| Sub-users | many users under one operator | ✔ `Platform.Account` identities | user records, quotas, mapping to identities | ✔ |
| Username/password auth | unique credential per user | ◐ one global `RESIN_PROXY_TOKEN`; identity in username | — | phase-2 gateway for per-user passwords |
| IP allowlist | restrict client source IPs | ✘ | ✘ | phase-2 gateway |
| Usage / traffic | bytes per user, per day | ◐ request logs carry `account`, `ingress_bytes`, `egress_bytes`; no rollup | ✔ rollup, daily buckets, export | ✔ charts |
| Rotation endpoint | "give me a new IP now" | ◐ `DELETE .../leases/{account}` forces re-pick | ✔ `POST /v1/users/{u}/rotate` with exclusion window | ✔ button |
| API keys | programmatic access per user | ✘ (single admin Bearer token) | ✔ key issue/revoke, hashed at rest | ✔ |
| Quotas | monthly GB / concurrency caps | ✘ | ✔ soft enforcement; hard via kill switch | ✔ editor |
| Regions | country/city selection | ✔ `region_filters` + tag regex | ✔ maps vendor labels → tags | ✔ picker |
| Audit | who did what | ◐ request logs (data plane) | ✔ admin-action audit table | ✔ viewer |
| Sticky sessions | same IP per user | ✔ core feature | — | expose TTL per user |
| Billing / resale | plans, invoices | ✘ | ✘ (out of scope by decision) | ✘ |

The honest summary: Resin gives the **data plane and its dashboard**; `ajs-accountd` gives the
**account/user/quota layer**; the custom UI is a thin console over both, not a reimplementation.

## Deployment

### Linux (primary)

Two supported shapes:

1. **Host binaries (recommended).** `resin` release tarball (`linux-amd64`/`linux-arm64`, S13) and
   `ajs-accountd` built from this repo, both on `127.0.0.1`. `systemd` units with
   `Restart=on-failure`; `ajs-accountd` runs as an unprivileged user with `NoNewPrivileges=true`,
   `ProtectSystem=strict`, and a single writable state directory.
2. **Docker Compose.** Resin image `ghcr.io/resinat/resin:v1.3.0` (linux amd64/arm64 only), plus our
   image containing `ajs-accountd` and `ajsproxy`. **Critical detail:** Resin's nodes are
   `127.0.0.1:<port>`, so Resin and `ajs-accountd` must share a network namespace — either one
   container running both, or `network_mode: "service:ajs-accountd"` on the Resin service. A plain
   two-container compose with bridge networking cannot reach loopback nodes.

Ports (all loopback unless stated): Resin `2260` (panel/API/proxy), `ajs-accountd` `2261`,
`ajsproxy` internals `12000–12999`, gates `13000–13999`.

### macOS and Windows

- Resin publishes `darwin-amd64`, `darwin-arm64` and `windows-amd64` builds (S13). `ajs-accountd`
  cross-compiles with `CGO_ENABLED=0`; no cgo dependencies are planned.
- macOS: `launchd` user agents for both, `KeepAlive`; loopback only.
- Windows: `resin.exe` from the release zip and `ajs-accountd.exe`; run as a service via the
  Service Control Manager wrapper of choice (NSSM or Task Scheduler with "run whether user is logged
  on"). Windows Firewall: no inbound rules needed when bound to `127.0.0.1`.
- Windows is a workstation/secondary target; Linux is the deployment target for anything long-lived.

### Network posture

- Default: everything binds `127.0.0.1`. Resin's `RESIN_LISTEN_ADDRESS` defaults to `0.0.0.0`
  upstream, so **set it to `127.0.0.1` explicitly** (S02, S03).
- LAN access is opt-in: bind a private interface, add a firewall allowlist, and keep the proxy port
  off any public interface. Never port-forward the proxy port.
- Remote panel access: TLS reverse proxy (Caddy/nginx) in front of `2260` only, or an SSH tunnel.
  The proxy port is not a web app; do not put it behind a path-based proxy.
- Backups: `RESIN_STATE_DIR/state.db` (nodes, platforms, leases), `ajs.db`, and optionally
  `RESIN_LOG_DIR`. Request logs contain target URLs and client IPs — treat them as sensitive.

### Upgrades

Pin Resin by tag; read its migration guide before crossing a major (`v1.0.0` auth migration is
documented upstream). `ajs-accountd` drains all nodes, exits, upgrades, re-authenticates and
republishes; Resin can stay up because its routable view empties during the drain.

## License and boundary

This is the Owner-delegated decision, recorded plainly.

- **Our code is MIT.** `LICENSE` at the repository root carries the standard MIT text with
  `Copyright (c) 2026 The LovePlusSpeedToProxy Authors`. `emulator/README.md`'s "MIT pending Owner
  approval" is superseded by this file.
- **Resin stays MIT.** If we ever redistribute Resin (vendored source or a derived image), the MIT
  text and `Copyright (c) 2026 Resinat and contributors` must travel with it, and modifications must
  be marked. Phase 1 pulls the upstream image; nothing is redistributed.
- **No AGPL code.** `remnawave/panel` is read for vocabulary only. Copying any of it would make the
  network-service copyleft apply to our panel.
- **No unlicensed code.** `pingc0y/go_proxy_pool` and `nssanc/ProxyPoolHub` have no license file;
  nothing may be copied from them.
- **Apache-2.0** (`rota`) would require the license text, a `NOTICE`, and a changes statement if
  reused. We do not reuse it; it is the fallback control plane, run as a separate program.

**Self-use / authorized-account boundary.** This system is built for a single operator to drive
**accounts they own or are explicitly authorized to use**, for their own traffic. It is **not** a
public proxy service. Specifically prohibited by this decision: reselling or sharing access,
publishing endpoints, using it to evade another service's controls, bypassing vendor account limits
(the concurrency gate exists to *respect* them), or hiding the origin of abusive traffic. The
operator is responsible for the vendor's terms of service and for local law; the AJiaSu ToS
reportedly prohibits reverse engineering (per AJS-02 OSINT), so live use is an Owner decision made
outside this repository.

## Falsification

The five load-bearing claims, each attacked before being kept:

1. **"Resin can be reused unmodified as the control plane."** Attack: it might require nodes to be
   public, or refuse loopback outbounds, or need a fork for dynamic nodes. Result: **CONFIRMED** —
   `socks5://127.0.0.1:port#tag` is parsed by `parseProxyURI` into a normal `socks` outbound
   (`parser.go`); local subscriptions are a supported source type with PATCH-able `content`
   (`control_plane_subscription.go`, `subscription_local_contract_test.go`); and Resin's own
   outbound test dials a node at `127.0.0.1` on the wire
   (`internal/outbound/ob_subscription_compat_test.go`, `TestParsedVMessTCPHTTPHeaderOnWire`).
   No loopback guard exists in `internal/outbound`. Residual risk: a runtime policy we did not
   read. `[UNVERIFIED]`
2. **"A subscription file is enough to publish dynamic nodes."** Attack: maybe only remote URLs
   refresh, or the minimum interval blocks churn. Result: **WEAKENED** — a `content` PATCH on a
   local subscription triggers an immediate asynchronous `UpdateSubscription` (not gated by
   `update_interval`; `internal/topology/subscription_scheduler.go` reads `SourceTypeLocal` content
   directly), but new nodes still need one probe cycle before they are routable. The design states
   a 15–30 s onboarding window and coalesces publishes to ≤1 per 5 s.
3. **"Resin's lease gives per-user sticky IPs for free."** Attack: the lease key might be per node or
   global. Result: **CONFIRMED with a constraint** — lease key is `(Platform, Account)`, one lease
   per identity, anchored on egress IP, fixed TTL, no renewal (`DESIGN.md` "租约生命周期",
   `models.go: Lease`). The constraint "one identity = one IP" is now explicit in the allocation
   section and forces multi-IP users to hold multiple identities.
4. **"Resin covers quotas and per-user auth."** Attack: a Webshare-like panel implies per-user
   passwords, IP allowlists, and traffic caps. Result: **OVERTURNED** — Resin has one global proxy
   token, no allowlist, no quota enforcement, and no per-account rollups (`grep` over `DESIGN.md`
   for quota/rate-limit terms returned nothing; request logs are per-request only). The panel matrix
   now marks these as phase-2 gateway work or `ajs-accountd` policy, and the Answer no longer implies
   a complete multi-tenant panel.
5. **"Cross-platform is just `GOOS`."** Attack: Resin's image is Linux-only and the WebUI needs a
   Node build. Result: **WEAKENED** — releases cover linux/darwin/windows amd64 and linux/darwin
   arm64 (S13), but the container path is Linux-only and a from-source build needs Node 22 plus Go
   1.26.10. Deployment now separates host binaries (all three OSes) from the Compose path (Linux).

## Open questions

Only decisions the Owner can make; everything else is settled above.

1. **Account inventory and concurrency.** How many accounts, at what purchased concurrency
   (1–2000), and which regions must be reachable? This sizes the gate, the port range, and the
   per-node soft caps.
2. **Is a per-user password / IP allowlist required in v1?** If yes, the phase-2 front gateway moves
   into phase 1 and the panel is no longer a thin console.
3. **Is "one identity = one sticky IP" acceptable?** If users need several simultaneous IPs, the
   panel must manage identity fan-out (`alice-a`, `alice-b`) from day one.
4. **Which host, and container or binaries?** Linux host binaries are the simplest; Compose needs
   the shared-network-namespace shape described above.
5. **Retention of request logs.** They contain target hosts/URLs and client IPs. Default proposal:
   7 days of metadata, payload logging off.
6. **When, and with which account, does live validation happen?** The ToS risk is the Owner's call;
   nothing in this design contacts the service.

## Sources

See `sources.tsv` (source_id, url, date, quote). Source ids referenced above: S01–S31.
