# AJS-13 — Build vs reuse for the proxy control plane

Status: decision complete, no implementation · Date: 2026-10-23 · Slice: AJS-13 (BuildVsReuse)
Write scope: `research/73-build-vs-reuse/` · Branch `panel-design` @ `0456212` · No live account, no vendor contact.

Every claim carries a source id from `## Sources` (files, API responses and commands run on 2026-10-23).
`[INFERENCE]` marks reasoning that is not directly observed; `[UNVERIFIED]` marks what could not be checked.

## Answer

**Recommendation: D — Hybrid.** Run Resin v1.3.0 unmodified as the *data plane* (node pool, egress-IP
sticky lease, health, probes, request logs) and put a thin custom `ajs-gateway` + console in front of it.
Do not fork Resin, do not adopt Rota, do not write a control plane from scratch.

Why, in one paragraph: Resin is the only candidate that pairs the load-bearing primitive for this
service — a sticky lease keyed on `(Platform, Account)` and anchored on the **egress IP** (S07) — with a
forward pool gateway, a panel and a permissive licence at usable maturity, and it is the only one that
can express "two accounts behind one IP are one sticky identity". A 1-star project (proxyhub) has a
session-lease plus rotate API but no tenancy and no maturity (S24), so it is not a base.
But Resin alone does **not** meet the six-feature requirement set: it has one global proxy token, no
per-user password, no IP allowlist and no quota enforcement (S01 §Panel feature matrix; falsification #4
in that report was *overturned*, not confirmed). Option A therefore ships three of the six outright,
two partially, and leaves per-user auth/quota out entirely. Option D closes exactly that gap with ~2k
lines of our own Go in front of Resin, which is also the seam that makes Resin replaceable if it dies.
Option B replaces Resin's lease engine with a request-counter (`stick`) that cannot anchor on an
egress IP, and adds TimescaleDB; option C
re-implements 29,593 lines of solved pool logic to arrive later at the same place.

Weighted scores (1–5 per cell, weights sum to 100, totals out of 100): **A 75.0 · B 46.0 · C 72.0 ·
D 77.0**. The matrix and the per-cell justification for every number are in `## Score matrix`.

The D–A margin is 2.0 points. That is thin on purpose and it is resolved by an inequality, not by taste:
**D beats A iff `2·w_features − w_time > 15`**, i.e. A only wins if time-to-MVP is weighted above 25
while features stay at 20. See `## Score matrix` for the algebra and `## Decision tree` for the
constraint that decides it (per-user auth/quota in v1: yes → D, no → A).

## Options

### A. Resin v1.3.0 unmodified + `ajs-accountd` + `ajsproxy` (current design)

Resin runs as shipped (MIT, 29,593 non-test Go LOC at `5473c7e`, S11) and owns the entrypoint, node
pool, sticky leases, probes, circuit breaker, request logs and WebUI. `ajs-accountd` (new, ~2.5–3k LOC)
owns the account vault, process supervision, per-account concurrency gates, publishing a local
subscription of `socks5://127.0.0.1:<gate>#<tag>` lines, drain and rotation. `ajsproxy` (existing,
3,372 non-test LOC, S18) stays untouched. Panel = Resin's own WebUI plus `ajs-accountd`'s JSON API.
Scope as designed: phases P0–P2 in `docs/PANEL.md` §9; per-user password/allowlist/hard quota are
phase 2 and explicitly conditional on an unanswered Owner question (S02 §9, "P3 is explicitly out of
scope unless the Owner answers open question 2 with yes").

### B. Rota (Apache-2.0) + custom AJiaSu provider + custom sticky layer

Rota's core is 16,663 non-test Go LOC plus a Next.js dashboard and a TimescaleDB requirement (S12, S16).
It has real per-user credentials (`ProxyUser` with bcrypt, per-user `requests_per_minute`, main pool +
ordered fallback pools, S14) and a proxy CRUD API that a sidecar could drive
(`POST /api/v1/proxies`, `/bulk`, tags, S17). What it does not have is any concept of a vendor account
or of an egress-IP lease: its `Proxy` row is `{address, protocol, username, password, tags, stats}`
(S13), and its only stickiness is `rotation_method=stick` with `stick_count` — "serve N requests from
this proxy, then advance", held in per-pool memory (S15). A "custom sticky layer" therefore means
writing the lease engine ourselves *and* keeping Postgres alive, which is more custom code than A for a
weaker pool. Apache-2.0 adds a `NOTICE` + changes statement the moment we modify it.

### C. Fully custom Go control plane (reuse `emulator/` + minimal React/Vite UI)

Everything in A's Resin column, written by us: entry (SOCKS5 + HTTP CONNECT with auth), node registry,
egress-IP discovery, latency EWMA, P2C selection, IP-anchored lease store with TTL sweep, circuit
breaker, probe scheduler, request log, admin API, audit, quota, UI, migrations. Reuses `ajsproxy`
(3,372 LOC) and nothing else. Estimated 7,200–10,200 Go + 1,800–2,600 TS production LOC, +3,000–4,500
test LOC (component table in `## Recommendation`). Resin's 29,593 LOC is the upper bound for a *mature*
version of this same component set; a single-protocol (SOCKS5 node) plane should land at 25–35% of it.
Time: 4–6 weeks to MVP, 8–12 to routing parity.

### D. Hybrid: Resin as data plane/panel + thin custom account/gateway UI (no Resin fork)

Same Resin and same `ajs-accountd` as A, plus one new process in front:

```
client ──▶ ajs-gateway :2240  (SOCKS5 + HTTP CONNECT, per-user auth)
             ├─ per user: password (bcrypt) / API key, IP allowlist, concurrency, RPS, byte quota
             ├─ identity map: user → (Platform, Account)  |  user → pinned node tag
             ├─ pooled path: dial Resin :2260 as `Platform.Account:RESIN_PROXY_TOKEN`
             └─ pinned path: dial gate :13000+n directly, for exact (account, node) selection
                   ▲
   ajs-accountd :2261 ── ajsproxy :12000+n ── FV session ──▶ vendor node
                   ▲
             Resin :2260  (pool, lease, probes, breaker, request logs, WebUI — unmodified)
```

The two paths are the point. Resin's credential parsing is `Platform.Account` in the username and the
global proxy token as the password (`internal/proxy/identity_v1.go:5-9`, `internal/proxy/socks5.go:307`,
S08), so a gateway can authenticate a *panel* user and then present Resin's own credential on their
behalf — no fork, no plugin. The pinned path exists because Resin has **no per-request node selector**:
routing picks from a Platform view by P2C/random (`internal/routing/router.go:397`, S21) and switching a
node means rotating the lease or editing Platform filters — both cold-path. `PLAN.md` §4 criterion 5
("change node at runtime; the next request exits from the new node's IP; the listening proxy socket is
never closed", S03) is a gateway requirement, not something Resin does for you. D is the only option
that implements the north star as written.

New code in D: gateway 1,800–2,600 Go + console 1,500–2,200 TS + `ajs-accountd` 2,500–3,000 Go
≈ 5,800–7,800 production LOC, ≈ 8,700–11,700 with tests, versus ≈ 12,000–17,300 for C. Resin and
`ajsproxy` are reused unmodified.

## Score matrix

Rubric: 1–5 per dimension, 5 = best. **Features are scored on what the option delivers at its own MVP,
weighted by how hard the missing piece is to add** — not by raw count. That distinction is the whole
argument: A's gaps (per-user auth, quota) are a ~2k-line bolt-on; B's gaps (vendor-account model,
egress-IP lease) are the hard core of the product.

Weights: time-to-MVP 15, features 20, maintenance 15, license 5, cross-platform 10, operational
complexity 10, lock-in 10, integration risk 15 (sum 100).

| Option | TTM | Features | Maint | License | XPlat | Ops | Lock-in | Integ | Weighted |
|---|---|---|---|---|---|---|---|---|---|
| A | 5 | 3 | 3 | 5 | 4 | 4 | 3 | 4 | 75.0 |
| B | 2 | 2 | 2 | 3 | 3 | 2 | 2 | 3 | 46.0 |
| C | 1 | 4 | 4 | 5 | 5 | 5 | 5 | 2 | 72.0 |
| D | 4 | 5 | 3 | 5 | 4 | 3 | 4 | 3 | 77.0 |
| Weights | 15 | 20 | 15 | 5 | 10 | 10 | 10 | 15 | 100 |

### Feature coverage (the six required features)

| Feature | A (Resin) | B (Rota) | C (custom) | D (hybrid) |
|---|---|---|---|---|
| Multi-account | ✔ `Platform.Account` identities | ✘ no vendor-account concept, only proxy rows | ✔ by construction | ✔ Resin |
| Sticky lease | ✔ key `(PlatformID, Account)`, anchored on egress IP (S07) | ◐ N-request `stick` counter, not IP-anchored (S15) | ✔ by construction | ✔ Resin |
| Per-user auth + quota | ✘ one global proxy token; no quota enforcement (S01) | ✔ bcrypt password + RPM cap; no byte quota (S14) | ✔ by construction | ✔ gateway owns both |
| Rotation API | ◐ `DELETE .../leases/{account}` + filter hack to force a *different* IP | ✔ native, best of the four | ✔ by construction | ✔ Resin lease + gateway endpoint |
| Health | ✔ active probes, passive breaker, TD-EWMA | ✔ health-check jobs, GeoIP | ✔ by construction | ✔ Resin |
| Audit | ◐ per-request logs only, no admin audit | ◐ request logs | ✔ by construction | ✔ gateway audit + Resin logs |

### Justifications — A

| Dimension | Score | Why |
|---|---|---|
| Time-to-MVP | 5 | P0 spike 1–2 d + P1 daemon ~1 week (S02 §9); panel, leases and breaker come free. |
| Required features | 3 | 3 ✔ + 2 ◐ + 1 ✘; the ✘ (per-user auth/quota) is cheap to add, the ✔ (lease) is expensive. |
| Maintenance | 3 | Our ~3k LOC is small, but 29.6k LOC upstream must be tracked: 16 releases in 8 months, one irreversible DB migration (S05). |
| License | 5 | MIT, run unmodified, no redistribution → no notice obligation (S04, S01). |
| Cross-platform | 4 | Releases for linux/darwin/windows amd64 + linux/darwin arm64 (S05); no windows-arm64, compose path Linux-only. |
| Operational complexity | 4 | Two processes + SQLite; container path needs a shared netns because nodes are `127.0.0.1`. |
| Lock-in | 3 | Bound to Resin's subscription/lease/API semantics, but not forked, so the publisher is the only coupling. |
| Integration risk | 4 | Parser, local-sub PATCH, loopback dial and credential format all verified in source (S07–S09, S19 rows S29–S31); residual = unread runtime policy + 15–30 s onboarding. |

### Justifications — B

| Dimension | Score | Why |
|---|---|---|
| Time-to-MVP | 2 | Proxy CRUD makes registration quick, but the sticky layer is new code and Postgres must exist first. |
| Required features | 2 | Loses multi-account entirely and has no IP-anchored lease; `stick` is a counter, so the core primitive is missing. |
| Maintenance | 2 | 16.6k LOC + TimescaleDB + Next.js; fork = permanent merge burden, sidecar = living with its data model. |
| License | 3 | Apache-2.0 is fine to run, but forking adds `NOTICE` + statement-of-changes obligations. |
| Cross-platform | 3 | Go core is portable; TimescaleDB on Windows means Docker/WSL2, and the dashboard needs Node. |
| Operational complexity | 2 | Core + dashboard + Postgres/Timescale + our sidecar = four services to keep alive. |
| Lock-in | 2 | Either fork (permanent) or accept a model that cannot express the requirement. |
| Integration risk | 3 | REST CRUD is real and documented (S17), but its health checks would compete with our gates and there is no lease primitive to hook. |

### Justifications — C

| Dimension | Score | Why |
|---|---|---|
| Time-to-MVP | 1 | 4–6 weeks to MVP, 8–12 to parity; nothing reusable for the pool half. |
| Required features | 4 | All six by construction, but at MVP they are rough — and the hard one (IP-anchored lease + breaker) is what Resin already ships. |
| Maintenance | 4 | ~7–10k LOC owned outright, no upstream churn, no license; every bug is ours. |
| License | 5 | Own MIT code, zero obligations. |
| Cross-platform | 5 | Pure Go + embedded static UI, one binary per OS/arch. |
| Operational complexity | 5 | One binary, one SQLite file. |
| Lock-in | 5 | None. |
| Integration risk | 2 | Highest: re-implements selection, probes, breaker, lease expiry; FV edge cases are the unknown, not the control plane. |

### Justifications — D

| Dimension | Score | Why |
|---|---|---|
| Time-to-MVP | 4 | A's 1.5 weeks + ~1 week gateway + ~1 week console = 3–4 weeks, but to the *full* six-feature MVP. |
| Required features | 5 | All six: gateway owns auth/quota/allowlist, Resin owns pool/lease/health/rotation. |
| Maintenance | 3 | Three surfaces, but gateway + console are ours (~4k LOC) and Resin stays upstream-tracked. |
| License | 5 | MIT throughout, no fork, no notice. |
| Cross-platform | 4 | Same as A plus a pure-Go gateway; console is static files. |
| Operational complexity | 3 | One extra process in the hot path and two auth layers to reason about; still SQLite-only. |
| Lock-in | 4 | The gateway is the seam: the data plane can be swapped without changing the client contract. |
| Integration risk | 3 | Extra hop, identity mapping (gateway user → `Platform.Account`), and quota double-count risk between gateway counters and Resin logs. |

### Sensitivity (why the ranking is not weight-tuning)

Holding the other weights fixed, the totals reduce to linear forms. D beats A iff
`2·w_features − w_time > 15`; at the chosen weights (20, 15) the left side is 25, so A would need
`w_time > 25` **and** features at 20 to win — a defensible position only if per-user auth/quota is not a
v1 requirement, which is precisely the decision-tree branch below. D beats C iff `3·w_time + w_features
> 40`, true for any plausible weighting (45+20=65). B cannot win under any weights in [0, 50]: it loses
on both the highest-weighted feature dimension and time-to-MVP, and its maximum possible total
(2·50 + 2·50 + 160 = 360) stays below D's at the same weights.

## Decision tree

Primary axis is **who uses it** (self-use vs many users), then **one host vs fleet**, then
**Linux-only vs cross-platform**. "Winner" is the cheapest option that satisfies the constraint without
a rewrite later.

| # | Constraint | Winner | Why |
|---|---|---|---|
| 1 | Self-use, one operator, one host, Linux, and no single-socket switching requirement | **A** | D's gateway buys nothing when there is one user; A is 1.5 weeks and meets `PLAN.md` §4 criteria 1, 2 and 4. |
| 2 | Self-use, but `PLAN.md` §4.5 is a hard requirement (one listener, next request exits a different node) | **D** | Even one user needs the pinned path: Resin can only rotate a lease or edit filters, both cold-path and rate-limited. |
| 3 | 2–20 users, each with own password/quota/allowlist | **D** | A has one global proxy token and no quota (S01); C costs 4–6 weeks for the same outcome; the gateway is ~2k LOC. |
| 4 | Public or resold service | **none** | Out of scope by the license/boundary decision (S01, S02 §11): billing, abuse handling and vendor ToS are unsolved. Do not pick an option to do this. |
| 5 | Per-request `(account, node)` selection, listener never closes | **D** | Resin selects per *identity*, not per request (`router.go:397`); only the gateway's pinned path satisfies `PLAN.md` §4.5. |
| 6 | One host, Linux-only, containers | **A** or **D** | Both work, but Resin + `ajs-accountd` must share a network namespace for `127.0.0.1:<gate>` nodes; host binaries avoid the footgun entirely. |
| 7 | Windows/macOS desktop deployment | **A** or **D** | Both ship darwin-amd64/arm64 + windows-amd64 builds; B needs Docker/WSL2 for TimescaleDB. |
| 8 | Fleet (≥2 hosts / ≥2 regions) | **D** now, **C** at ≥3 hosts | Resin has no clustering, replication or HA code (grep for cluster/raft/replica over the tree returned nothing, S20); the gateway is where a shard router would live. Revisit C only when the router outgrows one Resin instance. |
| 9 | Resin unusable: loopback dial refused, churn faster than 5 s, or project dead | **C** | Resin is the only mature option with an egress-IP lease (S24 C36/C37); the alternative is writing it, so budget 4–6 weeks and the milestones below. |
| 10 | "No third-party control plane" policy | **C** | Only option with zero upstream dependency. |
| 11 | Need to change pool internals (new allocation policy, different lease key) | **C** (or an unscored fork) | Unmodified Resin cannot; forking means merging 29.6k LOC forever. |

```
who uses it / what must it do?
├─ one operator, no single-socket node switching ─────────────▶ A  (1.5 wk, Resin UI is the panel)
└─ otherwise
   ├─ one host (or two) ──────────────────────────────────────▶ D  (3–4 wk, gateway + console)
   └─ 3+ hosts, Resin dead, or "no third-party control plane" ─▶ C  (4–6 wk MVP, 8–12 wk parity)
```

Cross-platform never changes the winner between A and D; it only eliminates B (TimescaleDB) and adds a
`windows-arm64` gap for both (S05). Linux-only never changes the winner either — it only removes the
Compose footgun.

## Recommendation

**D. Hybrid — Resin unmodified as data plane, `ajs-gateway` + console as the product surface.**

The architecture is A plus one process; nothing in A is thrown away. Concretely:

| Component | Status | Prod LOC (est.) | Notes |
|---|---|---|---|
| Resin v1.3.0 | reuse, unmodified | 29,593 (upstream) | MIT; run the release binary, or the `ghcr.io/resinat/resin:v1.3.0` tag the release workflow publishes (S22); never patch |
| `ajsproxy` | reuse, unmodified | 3,372 (exists) | one process per `(account, server)`, loopback SOCKS5 |
| `ajs-accountd` | new (A's P1) | 2,500–3,000 | vault, supervisor, gates, publisher, drain, rotate, `/metrics` |
| `ajs-gateway` | new (D's delta) | 1,800–2,600 | SOCKS5 + HTTP CONNECT, per-user bcrypt/API key, IP allowlist, concurrency, RPS, byte quota, identity map, audit, pooled + pinned paths |
| `ajs-panel` | new (D's delta) | 1,500–2,200 TS | React/Vite static SPA over `ajs-accountd` + gateway admin APIs |
| tests | new | ~0.5× prod | golden subscription render, quota boundary, drain ordering, lease mapping |

**First three milestones (D):**

1. **M1 — spike (days 1–2).** Resin + one hand-started `ajsproxy` + a hand-written local subscription.
   Exit: two Resin identities hold two leases; killing `ajsproxy` re-routes within one probe cycle
   (15–30 s); a client using `Platform.Account:RESIN_PROXY_TOKEN` exits from the expected IP.
2. **M2 — `ajs-accountd` (week 2).** Vault, supervisor, gates, publisher, drain, rotate, `/metrics`.
   Exit: 2 accounts × 10 servers stable for 24 h; the account concurrency cap is never exceeded;
   drain loses no in-flight request.
3. **M3 — gateway + console (weeks 3–4).** Per-user auth, allowlist, concurrency/RPS, byte quota,
   identity map, pinned path, audit, minimal console (accounts, users, nodes, usage, rotate).
   Exit: a user with a per-user password hits a pinned node; at 100.0 MB of a 100 MB quota the next
   request is refused with `QUOTA_EXCEEDED`; the refusal and the rotation appear in the audit table;
   the same user's pooled path still resolves through Resin.

**If the decision tree lands on C instead**, the component estimate is: entry SOCKS5/HTTP + auth
600–900; node registry + probe + egress-IP discovery + latency EWMA 900–1,300; lease store + P2C +
IP anchor + TTL sweep 700–1,000; account vault + supervisor + gates 1,500–2,000; admin API + audit +
quota 1,300–1,800; schema/migrations/config 400–600; UI 1,800–2,600 TS; **total 7,200–10,200 Go +
1,800–2,600 TS production, +3,000–4,500 test**. Milestones: (1) week 1–2 — entry + registry + probe +
egress-IP discovery, static-credential SOCKS5, no leases; (2) week 3–4 — IP-anchored lease store, P2C,
TTL sweep, rotation API, concurrency gate, canary; (3) week 5–6 — account vault + supervisor, per-user
auth/quota, audit, metrics, minimal UI, 24 h soak. Estimate confidence ±40%; the unknown is FV session
edge cases, not the control plane.

**Explicitly not recommended:** B under any constraint in the tree. It is not a close call: Rota's
`stick` is "N requests per proxy" held in per-pool memory (S15), which cannot express "this user keeps
this egress IP for 8 hours even if the node dies, as long as another account reaches the same IP" — the
one behaviour this service needs and the one Resin already implements (S07). The sibling sweep rates
Rota the strongest fallback if Resin dies (S24 C38, `[INFERENCE]`); that holds only if you accept a fork
plus Postgres, and Rota's retry/fallback path re-picks a proxy on failure, which actively fights
stickiness. C stays the cheaper fallback for a lease-critical service.

## Falsification

Three load-bearing claims, each attacked before it was kept.

1. **"Resin is the fastest route to a working system" (A, TTM 5).** Attack: the 1.5-week estimate
   assumes a live account exists, that a local-subscription PATCH updates immediately, and that Resin
   will dial a `127.0.0.1` node. Evidence: the PATCH route and the local-source update path are in
   source (S09; S19 row S30), and Resin's own outbound test dials `127.0.0.1` on the wire (S19 row S31),
   but nothing here has run against a live account and a new node still needs one scan cycle before it
   is routable (15–30 s by inference; new nodes get an immediate egress probe on insertion, S23 note 1).
   Verdict: **CONFIRMED for the control-plane wiring, UNVERIFIED end-to-end.** The estimate
   is gated by the Owner's account, not by engineering — M1 cannot be closed by an agent alone.
2. **"A custom build is the most controllable" (C).** Attack: control only has value after the code
   exists. Resin ships 34,525 test LOC against 29,593 production (1.17:1) and 16 releases in 8 months
   (S05, S11); C's plan budgets ~0.5× tests and has no operator history. In the first six weeks C is the
   *least* controllable option, because every failure is novel. Verdict: **WEAKENED** — true in year
   two, false in week two. Already priced in (TTM 1, integration risk 2); no ranking change.
3. **"The hybrid is optimal" (D).** Attack, four ways: (a) if nobody but the Owner uses it, the gateway
   is two weeks of dead weight; (b) its byte counters can diverge from Resin's request logs; (c) it does
   not remove the bus-factor risk, only the exit cost; (d) two routing paths (pooled and pinned) can
   give one user different egress for the same target. Verdict: **CONFIRMED with a scope condition.**
   (a) is the decision-tree branch that hands the win to A; (b) is why quota reads the gateway and only
   the gateway; (c) is true and is stated as such — the gateway is insurance, not a cure; (d) is a real
   design constraint: a user is bound to one path per request class and the two are never silently
   mixed.

## Risks

1. **Upstream bus factor 1.** 372 of 378 contributions come from one account; the next five contributors
   have 1–2 each (S06). 16 releases in 8 months is healthy activity, but it is one person. Trigger to
   revisit: no release for 6 months, or a security advisory unpatched for 30 days → switch to C using the
   same `ajs-accountd` API, which is the reason D keeps the publisher as the only coupling.
2. **Resin is 8 months old** (created 2026-02-28, 35 open issues, 408 forks, S04). Maturity risk is real;
   mitigate by pinning v1.3.0, keeping `state.db` backups, and staging every upgrade.
3. **Irreversible migration.** v1.3.0 migrates persisted Platform regex filters and states that earlier
   releases cannot read the result (S05). Back up `<RESIN_STATE_DIR>/state.db` before every upgrade;
   downgrade only by restore.
4. **The feature gap is real, not cosmetic.** Per-user password, IP allowlist, hard quota and per-user
   rate limits are absent from Resin (S01, falsification #4 *overturned*). A ships without them; D is the
   only reuse option that has them on day one.
5. **Per-request selection is not native.** Resin routes by Platform view, not by requested node
   (`router.go:397`). If the gateway is built assuming Resin can pin a node per request, that assumption
   fails at integration time; the pinned path must dial the gate directly.
6. **Loopback + containers.** Nodes are `127.0.0.1:<gate>`; a plain two-container Compose with bridge
   networking cannot reach them. Use host binaries (recommended) or one container / `network_mode:
   service:`. This is the most likely first-day deployment failure.
7. **Onboarding and rotation are cold-path.** A new node needs one probe cycle (15–30 s) before it is
   routable, and forcing a *different* IP requires a temporary filter change plus refresh. Neither may be
   exposed as a synchronous low-latency API; both need coalescing and rate limits (S02 §8, S01). Subscription
   content is also bounded by a 1 MiB API body and a 1 MiB line-scanner buffer (S23), so the publisher must
   fail loudly rather than truncate at ~26k node lines.
8. **Quota accounting divergence.** The gateway counts bytes exactly as they cross it; Resin's request
   logs are per-request and asynchronously flushed. Use gateway counters for quota/billing and Resin logs
   for forensics — never sum both.
9. **Go toolchain skew.** Local Go is 1.26.7; Resin's `go.mod` declares `go 1.26.10` (S11). Irrelevant
   while we run release binaries; if we ever build Resin from source, the toolchain must be ≥1.26.10.
10. **ToS / self-use boundary.** Unchanged from S01: authorized accounts only, no resale, no public
    endpoint. Nothing in this slice contacted the vendor.
11. **Estimate risk.** C's 7–10k LOC is ±40%; D's gateway is ±30%. The first milestone (M1) is designed to
    falsify the integration assumptions before either estimate is committed to.

## Sources

| ID | Source | Date | Observation |
|---|---|---|---|
| S01 | `research/70-panel/REPORT.md` | 2026-10-23 | Candidate matrix, Resin fit, panel feature matrix (per-user auth/quota absent), falsification #4 overturned, license boundary |
| S02 | `docs/PANEL.md` | 2026-10-23 | Build sheet: ports, API surface, data model, phases P0–P3, `P3 out of scope unless open question 2 = yes` |
| S03 | `PLAN.md` §1, §4 | 2026-10-23 | North star `(account, node) → local SOCKS5/HTTP`; criterion 5 "switch without restart, listener never closed" |
| S04 | `https://api.github.com/repos/Resinat/Resin` | 2026-10-23 | `stargazers_count=2482`, `forks=408`, `open_issues=35`, `created_at=2026-02-28`, `size=9216`, `license=MIT`, `pushed_at=2026-10-21` |
| S05 | `https://api.github.com/repos/Resinat/Resin/releases?per_page=100` | 2026-10-23 | 16 releases; v1.3.0 2026-10-21, v1.2.0 2026-08-01; assets linux/darwin amd64+arm64, windows-amd64; v1.3.0 note "database migration is irreversible" |
| S06 | `https://api.github.com/repos/Resinat/Resin/contributors?per_page=100` | 2026-10-23 | 6 contributors; 372, 2, 1, 1, 1, 1 contributions |
| S07 | `Resinat/Resin@5473c7ee` `internal/model/models.go:95-109` | 2026-10-23 | `Lease{PlatformID, Account, NodeHash, EgressIP, ExpiryNs}`; `LeaseKey{PlatformID, Account}` |
| S08 | `Resinat/Resin@5473c7ee` `internal/proxy/identity_v1.go:5-9`, `internal/proxy/socks5.go:307` | 2026-10-23 | username identity `Platform.Account`/`Platform:Account`; `if s.token != "" && string(password) != s.token` |
| S09 | `Resinat/Resin@5473c7ee` `internal/api/server.go:103-109` | 2026-10-23 | `PATCH /api/v1/subscriptions/{id}`, `POST .../actions/refresh`, `DELETE`, `cleanup-circuit-open-nodes` |
| S10 | `Resinat/Resin@5473c7ee` `Dockerfile:27` | 2026-10-23 | `CGO_ENABLED=0 go build -tags "with_quic with_wireguard with_grpc with_utls"` (pure-Go sqlite) |
| S11 | `find resin -name '*.go' ! -name '*_test.go' \| xargs cat \| wc -l` on `5473c7ee` | 2026-10-23 | 29,593 non-test Go LOC; 34,525 test LOC; 221 `.go` files, 36 `.ts` + 31 `.tsx` WebUI; `go.mod` declares `go 1.26.10` |
| S12 | `https://api.github.com/repos/alpkeskin/rota` + `rota/core/go.mod` | 2026-10-23 | 769 stars, 5 open issues, created 2024-06-02, size 2,370 KB, Apache-2.0, `go 1.25.3` |
| S13 | `alpkeskin/rota@f8a09cc` `core/internal/models/proxy.go:5-27` | 2026-10-23 | `Proxy{Address, Protocol, Username, Password, Tags, Requests, …}` — no account, no lease |
| S14 | `alpkeskin/rota@f8a09cc` `core/internal/models/proxy_user.go:5-30` | 2026-10-23 | `ProxyUser{PasswordHash bcrypt, MainPoolID, FallbackPoolIDs, RequestsPerMinute}` |
| S15 | `alpkeskin/rota@f8a09cc` `core/internal/proxy/pool_selector.go:18-25,105-110` | 2026-10-23 | `method roundrobin\|random\|stick`, `stickIdx`, `stickServed`, `stick` = N requests per proxy |
| S16 | `alpkeskin/rota@f8a09cc` `core/internal/database/timescale.go`, `docker-compose.yml` | 2026-10-23 | TimescaleDB/Postgres is the state store |
| S17 | `alpkeskin/rota@f8a09cc` `core/internal/api/server.go:294-304` | 2026-10-23 | `POST /api/v1/proxies`, `/proxies/bulk`, `/proxies/bulk-tags`, `/proxies/{id}/test` |
| S18 | `emulator/` in this repo | 2026-10-23 | 3,372 non-test Go LOC in 14 files, 1,643 test LOC; `serve-socks` per `(account, server)` |
| S19 | `research/70-panel/sources.tsv` S01–S31 | 2026-10-23 | Original evidence set for Resin/Rota/candidate facts |
| S20 | `grep -rniE "cluster\|raft\|replica" resin --include=*.go --include=*.md` | 2026-10-23 | No matches — no clustering/HA code in Resin |
| S21 | `Resinat/Resin@5473c7ee` `internal/routing/router.go:70,397`, `random.go:44` | 2026-10-23 | `livePickAttempts = 2`; selection is P2C/random over a Platform view, no per-request node parameter |
| S22 | `Resinat/Resin@5473c7ee` `.github/workflows/release.yml:205-221`, `docker-compose.yml.example:6` | 2026-10-23 | image tags `type=semver,pattern={{version}}` + `{{major}}.{{minor}}` + `latest`; `platforms: linux/amd64,linux/arm64`; example compose uses `:latest`, so pin the version tag |
| S23 | `research/72-resin-audit/REPORT.md` (sibling slice, read after it landed) | 2026-10-23 | Independent source audit of `v1.3.0@5473c7e`: 10/10 integration claims CONFIRMED; notes: 13–17 s is scanner cadence (egress IP refreshed ≤24 h, latency ≤1 h), manual `PATCH content` is not rate-limited, content bounded by 1 MiB body + 1 MiB line scanner |
| S24 | `research/71-panel-alt/claims.tsv` (sibling sweep, landed mid-slice) | 2026-10-23 | 44 graded claims over ~30 candidates. C18/C19: proxyhub (MIT, Go, SQLite) has session-sticky exit IPs with TTL + rotate API but 1 star, created 2026-04-25, no tenancy. C36: no OSS candidate combines per-account egress-IP lease + forward pool gateway + panel/rotate API + permissive licence. C37: only proxyhub has a session-lease + rotate API. C38 rates Rota the strongest fallback `[INFERENCE]`. C44: no candidate implements per-account quotas over a pool |

LOC counts were produced by the commands named in S11/S18 against pinned commits; GitHub metadata is a
single snapshot on 2026-10-23, not a trend. No code from Rota, `remnawave/panel` (AGPL) or the unlicensed
candidates was copied; Rota was cloned to a temp directory and read only.

**Known gap.** `research/71-panel-alt/REPORT.md` was not on disk when this slice was written
(`71-panel-alt/` existed but was empty). `research/72-resin-audit/REPORT.md` landed mid-slice and is
incorporated as S23 — 10/10 integration claims confirmed, nothing contradicted here. If 71/ lands with a
fact that changes a cell above, change the cell and re-run `validate.py`: it recomputes every total from
the weights and fails if the named recommendation is no longer the highest-scoring option.
