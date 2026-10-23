# AJS-11 — Competitive search: is Resin really the best panel/control plane?

Status: evidence complete, no implementation · Date: 2026-10-23 · Slice: AJS-11 (AltPanelScout)
Write scope: `research/71-panel-alt/` · Branch: `panel-design` @ `0456212`

Every claim is tied to a `sources.tsv` id (S01–S75) or to a file in this repository. Inferences are
marked `[INFERENCE]`; things that could not be checked are marked `[UNKNOWN]`. Star counts are one
`stargazers_count` observation from the GitHub API on 2026-10-23, not a trend.

## Answer

**No candidate beats Resin. Resin stays the base.** 78 repositories were searched, 23 were assessed
in depth, and the Top 3 are Resin, Rota and ProxyCat.

Resin wins because it is the only assessed project that has all four properties this project needs
at once:

1. **A lease per business identity, anchored on the egress IP** — `Lease{NodeHash, EgressIP, Expiry,
   LastAccessed}`, one table per Platform, fixed expiry, and a same-IP node swap that rewrites the
   lease in place without changing `Expiry` (S04, S05, S07). That is exactly the AJiaSu shape: many
   accounts, shared egress IPs, sessions that must survive a gate dying.
2. **Per-account proxy identity at the entrypoint** — `Platform.Account:PROXY_TOKEN` (S06), so one
   credential maps to one business account without a second user database.
3. **A programmatic control surface** — lease list/get/delete by account, delete-all, ip-load,
   subscription refresh (S08), which is what `ajs-accountd` needs for drain and rotate.
4. **A permissive license and boring deployment** — MIT (S01), a single Go binary with SQLite,
   darwin/linux/windows artifacts, v1.3.0 published 2026-10-21 (S10).

The closest challengers each fail on one axis:

| Challenger | Why it is close | Why it loses |
|---|---|---|
| **Rota** (769★, Apache-2.0) | Per-user `user:pass`, main pool + ordered fallbacks, per-user RPM cap, JWT API, v2.3.0 | Stickiness is "N requests per IP" per pool, not a TTL lease per account; needs TimescaleDB (S12, S13) |
| **ProxyCat** (2,654★, GPL-2.0) | Same operational idea — fixed tunnel entry in front of short-lived upstream IPs; multi-user Basic auth, IP lists, per-request logs, panel API | No account/lease model; exits rotate on time/count; GPL-2.0 blocks embedding (S19–S22) |
| **proxyhub** (1★, MIT) | The only other project with session→exit-IP stickiness, TTL and a rotate endpoint; single binary + SQLite | 1 star, created 2026-04-25, free-proxy pool, no Platform/Account or quota model (S23–S25) |

Resin's real gaps against the commercial benchmark (Webshare) are unchanged and are *additive*, not
architectural: per-user passwords, IP allowlists, hard quotas, and per-sub-user analytics. Webshare
exposes all four (S61–S64); Resin's model has no quota/allowlist/password fields at all (S09), and
`research/70-panel/REPORT.md` already lists them as phase 2 (S70). That is work for `ajs-accountd`,
not a reason to switch control planes.

## Search log

GitHub search was run through `gh api search/repositories` (authenticated, 2026-10-23). `total_count`
is shown exactly as returned; queries marked **polluted** searched README full text and returned
list/awesome repos, so only the top 8 were inspected. Useful ids are the repos that survived triage.

| # | Query | Hits | Useful ids |
|---|---|---:|---|
| q1 | `proxy pool panel in:name,description` | 30 | `Rain-kl/PoolX` |
| q2 | `proxy pool manager in:name,description` | 57 | `Rain-kl/PoolX`, `daimon3332/easy-proxies` |
| q3 | `rotating proxy manager in:name,description` | 24 | `mhyrzt/xrat`, `ngotuananh101/proxyhub` |
| q4 | `sticky session proxy in:name,description,readme` | 22135 | none (polluted) |
| q5 | `multi-tenant proxy in:name,description,readme` | 49194 | none (polluted) |
| q6 | `proxy lease in:name,description,readme` | 27350 | none (polluted) |
| q7 | `代理池 面板 in:name,description,readme` | 944 | `honmashironeko/ProxyCat` |
| q8 | `代理IP 管理平台 in:name,description,readme` | 158 | none |
| q9 | `多账号代理 in:name,description,readme` | 297429 | none (polluted) |
| q10 | `隧道代理 in:name,description,readme` | 21652 | `ginuerzh/gost` (polluted) |
| q11 | `socks5 panel in:name,description` | 57 | `jinqians/psm-panel` (rejected: xray node script) |
| q12 | `proxy panel in:name,description` | 903 | `hiddify/Hiddify-Manager`, `remnawave/panel`, `v2board/v2board`, `bia-pain-bache/BPB-Worker-Panel` |
| q13 | `webshare alternative in:name,description,readme` | 465 | none |
| q14 | `gost panel in:name,description,readme` | 712 | `Aurora-Admin-Panel/deploy` |
| q15 | `3proxy admin in:name,description,readme` | 387 | none |
| q16 | `sing-box panel in:name,description` | 117 | `alireza0/s-ui`, `shenaba/2s-ui`, `ClickDevTech/CELERITY-panel` |
| q17 | `clash dashboard in:name,description` | 246 | `haishanh/yacd`, `MetaCubeX/Yacd-meta` |
| q18 | `subscription panel in:name,description` | 798 | `shenaba/2s-ui`, `kazeyukiro/3m-ui`, `SulgX/SulgX-Panel` |
| q19 | `topic:proxy-pool` | 369 | `MikeChongCan/scylla`, `Rain-kl/PoolX`, `daimon3332/easy-proxies`, `yukkcat/socks5-proxy`, `nfx/slrp` |
| q20 | `topic:rotating-proxy` | 383 | `alpkeskin/rota`, `mattes/rotating-proxy`, `Temporalitas/ipv6-proxy-server` |
| q21 | `egress gateway in:name,description` | 305 | `spidernet-io/egressgateway`, `monzo/egress-operator` |
| q22 | `proxy scheduler in:name,description` | 339 | none |
| q23 | `proxy management platform in:name,description` | 188 | `konstpic/SharX` (no license) |
| q24 | `ip rotation in:name,description` | 500 | `alpkeskin/rota`, `ustayready/fireprox`, `MrTurvey/flareprox` |
| q25 | `sticky ip in:name,description,readme` | 36296 | none (polluted) |
| q26 | `proxy authentication users quota in:name,description` | 0 | none |
| q27 | `代理 管理面板 in:name,description` | 94 | `PAICNI/CFNext`, `Aimilibot/X-MILI`, `kure29/flyx` (all inbound-node panels) |
| q28 | `http proxy pool api in:name,description` | 38 | `highkay/go-proxy-manager` (no license) |
| q29 | `sticky proxy in:name,description` | 165 | `Resinat/Resin` is the **#1 hit by stars**; `nodemaven/nodemaven-mcp` is a vendor MCP client, not a panel |
| q30 | `session proxy pool in:name,description` | 33 | none (polluted) |
| q31 | `proxy gateway in:name,description` | 5054 | none (API gateways: grpc-gateway, shenyu, emissary) |
| q32 | `proxy pool gateway in:name,description` | 87 | `Resinat/Resin`, `daimon3332/easy-proxies`, `overflow0verture/proxy_harvester` (16★) |
| q33 | `residential proxy panel in:name,description` | 2 | `yms168/proxy-panel-v2` (0★), `dttxorg/miniproxy` (0★, no license) |

Web searches (Brave-backed `web_search`, 2026-10-23):

| # | Query | Results | Useful ids |
|---|---|---:|---|
| W1 | `open source proxy pool control panel sticky session per-account lease` | 10 | `jiusanzhou/proxyhub`, `awesome-net-dev/sticky-proxy`, `dubr1k/proxy-control`, `vincentrosso/proxypool`, `Resinat/Resin` |
| W2 | `webshare alternative self-hosted proxy management panel sub-users IP allowlist` | 8 | Webshare API docs: `apidocs.webshare.io/subuser`, `/ipauthorization` |
| W3 | `代理IP管理平台 开源 面板 隧道代理 多账号` | 8 | `tongsq/proxy-collect`, `honmashironeko/ProxyCat`; commercial: zdaye, kuaidaili, fanproxy |

Targeted reads beyond search: a 39-repository GitHub metadata batch (stars/license/language/pushed),
README reads for PoolX, ProxyCat, easy-proxies, Aurora, 3m-ui, 2s-ui, Marzban, Xboard, remnawave,
gost, 3proxy, dataplaneapi, proxy.py, ProxyBroker, Scylla, proxy_pool, go_proxy_pool, ProxyPoolHub,
metacubexd, yacd, zashboard, proxyhub, sticky-proxy, proxy-chain, proxy-collect; source reads of
PoolX's domain model and Resin's `DESIGN.md`, `server.go`, `models.go`. `sspanel-uim/sspanel-uim`
returned HTTP 404 (S74). Full inventory: `searched.tsv` (78 rows, 23 assessed).

Stop condition: the last three discovery rounds produced no new plausible candidate — q26 = 0 hits,
q31 returned only API gateways, and q33 returned two repositories with 0 stars each. Independently,
q29 ("sticky proxy") ranks `Resinat/Resin` first by stars, which is a useful external confirmation
that the category leader is the project we already chose.

## Candidate matrix

Stars/license from the GitHub API on 2026-10-23. "Sticky/lease" asks specifically for a TTL-bound
per-identity binding to an egress IP — not round-robin, not per-port pinning.

| Repo | Stars | License | Stack | Data plane | Auth model | Sticky/lease | Panel | Cross-platform | Fit | Verdict |
|---|---:|---|---|---|---|---|---|---|---|---|
| `Resinat/Resin` | 2482 | MIT | Go, SQLite, embedded WebUI | HTTP/SOCKS5 forward + reverse entry over a subscription-fed node pool | `Platform.Account:PROXY_TOKEN` per request (S06) | **Yes** — per (Platform,Account), fixed TTL, egress-IP anchored, same-IP swap keeps expiry (S04,S05) | Yes + admin API | darwin/linux/windows (S10) | **Exact** | **Keep** |
| `alpkeskin/rota` | 769 | Apache-2.0 | Go + Next.js + TimescaleDB | HTTP forward rotation over upstream lists | per-user `user:pass` + main pool/fallbacks + RPM cap (S12) | Partial — "sticky (N requests per IP)" per pool, no account TTL (S12) | Yes + JWT API | linux/darwin/windows (S14) | Good rotation, wrong stickiness | Runner-up |
| `honmashironeko/ProxyCat` | 2654 | GPL-2.0 | Go | HTTP+SOCKS5 on one port over short-lived upstreams | shared `[Users]` Basic table; client IP allow/deny (S20,S21) | No — exit lifetime by time/request count (S20) | Yes + panel/pool API (S22) | Windows zip + Docker (release) | Closest operational shape | Reject on license |
| `Rain-kl/PoolX` | 300 | MIT | Go (Gin) + React + Mihomo kernel | multiple listeners, each mapped to a pool of Clash/Mihomo nodes (S16) | per-port single user/pass (S17) | No — select/url-test/fallback/load-balance only (S17) | Yes + REST API | darwin/linux/windows (S18) | Pool gateway, no accounts | Reference |
| `jiusanzhou/proxyhub` | 1 | MIT | Go + SQLite + React | HTTP forward over aggregated free proxies | Bright-Data-style `user-session-...:any` (S24) | **Yes** — session→exit IP with TTL, rotate/delete API (S24,S25) | Yes + API | Go binary | Right mechanic, no maturity | Watch |
| `daimon3332/easy-proxies` | 201 | MIT per LICENSE (API: NOASSERTION) (S26,S27) | Go + sing-box | one local port per node, optional shared pool entry (S28) | none documented | No — manual port per node | Yes + REST | Windows/Linux (release) | Port-per-node, no tenancy | Reference |
| `Aurora-Admin-Panel/deploy` | 1988 | MIT | Go + SSH-controlled hosts | port leasing: iptables/socat/gost/haproxy per tenant (S30) | registered users per leased port | No | Yes | Linux hosts | Multi-tenant port leasing, not egress | Reference |
| `kazeyukiro/3m-ui` | 169 | GPL-3.0 | Go + Mihomo | Mihomo listeners/inbounds | users bound to nodes, quota, expiry, per-node metering (S32) | No | Yes | Linux | Inbound panel | Reject on license |
| `shenaba/2s-ui` | 242 | GPL-3.0 | Go + sing-box | sing-box inbounds/outbounds | clients with quota/expiry/IP limit (S34) | No | Yes | Linux | Inbound panel | Reject on license |
| `Gozargah/Marzban` | 7471 | AGPL-3.0 | Python + Xray | Xray inbounds | multi-user, traffic/expiry, REST API (S36) | No | Yes | Linux | Inbound panel | Reject on license |
| `cedar2025/Xboard` | 4797 | MIT | PHP/Laravel + MySQL | external node panel | subscription users/plans | No | Yes | Docker/Linux | Inbound subscription panel | Not a fit |
| `remnawave/panel` | 5244 | AGPL-3.0 | TypeScript + Xray | Xray nodes | users/traffic/nodes (S39) | No | Yes | Docker/Linux | Inbound panel | Reject on license |
| `go-gost/gost` | 7684 | MIT | Go | tunnels, forward/reverse proxy, chains | admission control + limiter (S41) | No | API only; WebUI separate & unlicensed (S42) | Go binary | Strong data plane, no tenant model | Reference |
| `3proxy/3proxy` | 5542 | custom BSD-style (NOASSERTION) (S43,S44) | C | HTTP/SOCKS proxy server | per-user config | No | No (third-party Webmin) | Windows/Linux | Data plane only | Not a fit |
| `haproxytech/dataplaneapi` | 394 | Apache-2.0 | Go sidecar | HAProxy L4/L7 | HAProxy userlists/ACLs | No | API only | Linux | Config API, no tenant/lease | Not a fit |
| `abhinavsingh/proxy.py` | 3559 | BSD-3-Clause | Python | embeddable HTTP proxy | plugin hooks (S48) | No | No | Python | Library, not a panel | Not a fit |
| `constverum/ProxyBroker` | 4171 | Apache-2.0 | Python | finds/checks public proxies + simple server (S50) | none | No | No | Python | Proxy discovery | Not a fit |
| `MikeChongCan/scylla` | 4023 | Apache-2.0 | Python + SQLite | pool of crawled public proxies + forward proxy (S52) | none | No | Yes (UI + JSON API) | Docker/Python | Free-proxy pool | Not a fit |
| `jhao104/proxy_pool` | 23764 | MIT | Python + Redis | free-proxy harvesting | none | No | No (API/CLI) | Python | Free-proxy pool | Not a fit |
| `pingc0y/go_proxy_pool` | 777 | **none** (S54) | Go | free-proxy pool | none | No | No | Go | Unlicensed, stale (2023) | Avoid |
| `nssanc/ProxyPoolHub` | 0 | **none** (S55) | Go/React | proxy list CRUD + rotation | basic proxy auth | No | Yes | Docker | Unlicensed, 0 stars | Avoid |
| `MetaCubeX/metacubexd` | 4560 | MIT | TypeScript | none (Mihomo dashboard) (S57) | Mihomo secret | No | Yes | Browser | Dashboard only | Not a fit |
| `tongsq/proxy-collect` | 195 | **none** (S58) | Go + Redis/MySQL | free-proxy pool + tunnel with per-user auth/limit (S59) | username/password + rate limit | No | No | Go | Unlicensed, stale (2024) | Avoid |

Denominator: 78 repositories in `searched.tsv`, 23 rows above (23 assessed). Six have no license
(`go_proxy_pool`, `ProxyPoolHub`, `proxy-collect`, `yukkcat/socks5-proxy`, `SharX`,
`highkay/go-proxy-manager`) and are treated as non-reusable.

## Top 3

1. **`Resinat/Resin`** — MIT, 2,482★, v1.3.0. The only candidate with a per-(Platform,Account)
   egress-IP lease, a forward HTTP/SOCKS5 entrypoint, an admin API for lease manipulation, and a
   single-binary deployment (S01–S10).
2. **`alpkeskin/rota`** — Apache-2.0, 769★, v2.3.0. Best license-compatible fallback: per-user
   credentials, main pool + ordered fallbacks, per-user RPM cap, JWT API, cross-platform binaries.
   Its weakness is explicit: pool-level "sticky (N requests per IP)", not an account lease, and a
   mandatory TimescaleDB (S11–S14).
3. **`honmashironeko/ProxyCat`** — GPL-2.0, 2,654★. The closest *operational* analogue: a fixed
   tunnel entry in front of short-lived upstream IPs, multi-user Basic auth, client IP lists,
   per-request access logs and a panel API. It cannot be the base because GPL-2.0 plus no account
   lease (S19–S22).

Near-miss (not Top 3 because maturity is disqualifying, not design): **`jiusanzhou/proxyhub`** is
the only other project with session→exit-IP stickiness, TTL, and rotate/delete endpoints in one
MIT binary (S23–S25), but it has 1 star and was created 2026-04-25.

## Feature matrix

Benchmark is Webshare's published feature set (S60–S64). "Partial" means the capability exists but
is not per-sub-user or not enforceable.

| Capability | Webshare | Resin | Rota | ProxyCat | PoolX |
|---|---|---|---|---|---|
| Proxy list export (IP:port) | Yes — TXT/API (S60) | Yes — nodes via admin API/dashboard (S08) | Yes — TXT/CSV export (S12) | Partial — upstream list, not an entry list (S22) | Yes — port per pool (S16) |
| Sub-users / tenants | Yes — Sub-Users API, masquerade (S63) | Partial — Platform+Account identity, one shared token (S06) | Yes — per-user credentials + pool/fallbacks (S12) | Partial — shared `[Users]` table, no per-user routing (S21) | No — single admin, per-port credential (S17) |
| Username/password auth | Yes (S62) | Yes — `Platform.Account:PROXY_TOKEN` (S06) | Yes — `user:pass@host:8000` (S12) | Yes — Basic table (S21) | Yes — per port (S17) |
| IP allowlist | Yes — IP Authorization API (S62) | **No** — no field in model (S09) | [UNKNOWN] — not documented | Yes — client IP whitelist/blacklist (S20) | [UNKNOWN] |
| Usage statistics | Yes — bandwidth/error rate/activity (S61) | Yes — per-request logs with account/node/egress IP/bytes (S70) | Yes — per-proxy success rate and latency (S12) | Yes — access records, domain stats, CSV/JSON (S22) | Partial — node latency tests only (S17) |
| Rotation API | Yes — backconnect + sub-user refresh (S63) | Yes — lease delete, ip-load, subscription refresh (S08) | Yes — REST + working-proxy export (S12) | Partial — manual switch endpoint (S22) | Partial — strategy switch, no rotate call (S17) |
| Quotas | Yes — plan bandwidth (S60) | **No** — no quota field (S09) | Partial — requests_per_minute cap (S12) | Partial — concurrency/queue limits (S20) | No |
| Regions | Yes — 20+ countries (S60) | Yes — GeoIP region filters + tag regex (S70) | Yes — country/region/city/ISP (S12) | Partial — GeoIP display only (S20) | Partial — node tags |
| Audit trail | Partial — activity analytics (S61) | Yes — structured per-request log (S70) | Yes — requests per proxy (S12) | Yes — per-request log + export (S22) | No |

Reading: Resin already covers 6 of 9 rows, and the three it misses (IP allowlist, quotas, per-user
passwords) are exactly the rows `ajs-accountd` is supposed to own. No alternative covers more rows
without a worse license or a missing lease.

## Recommendation

**Keep Resin v1.3.0 unmodified as the control plane.** Concretely:

- Do not switch to Rota: it would replace a lease model we need with a rotation model we do not,
  and add TimescaleDB to the deployment.
- Do not adopt ProxyCat or 3m-ui/2s-ui/Marzban/Remnawave: GPL/AGPL code cannot be embedded, and
  none of them has an account lease.
- Do not build a panel: the cheapest path is still Resin + `ajs-accountd` + the existing
  subscription PATCH integration documented in `research/70-panel/REPORT.md`.
- Close the four Webshare-parity gaps in `ajs-accountd`, not in Resin: per-user password/credential
  issuance, IP allowlist enforcement at the entrypoint, hard quota accounting, and per-sub-user
  usage export.

**What would falsify this decision** (watch these, in order):

1. Resin cannot dial `127.0.0.1:<gate>` nodes — already contradicted by the outbound test that dials
   `127.0.0.1` on the wire (`research/70-panel/REPORT.md`, S31 there), so this is low risk.
2. Local-subscription refresh cannot keep up with gate churn at our node counts.
3. Quotas become a launch requirement rather than phase 2 — then the cheapest change is a quota
   proxy in front of Resin, not a new control plane.
4. Resin's project stalls (no release or push for >6 months) — then re-evaluate `proxyhub` if it has
   matured, else fall back to Rota + a lease layer.

**Licensing boundary.** Resin is MIT: running the published binary or image is not redistribution;
vendoring or shipping it inside our image requires the MIT notice (S01). Rota is Apache-2.0; Rota's
NOTICE would be required if copied. ProxyCat is GPL-2.0 and 3m-ui/2s-ui are GPL-3.0; Marzban and
Remnawave are AGPL-3.0 — reference only, no code copied. Six assessed repos have no license and are
non-reusable.

## Sources

S01–S10 Resin (API metadata, `DESIGN.md` lease lifecycle/auth, `server.go` routes, `models.go` gap
grep, release assets). S11–S14 Rota. S15–S18 PoolX. S19–S22 ProxyCat. S23–S25 proxyhub. S26–S28
easy-proxies. S29–S30 Aurora. S31–S32 3m-ui. S33–S34 2s-ui. S35–S36 Marzban. S37–S38 Xboard. S39
Remnawave. S40–S42 gost and gost-ui. S43–S44 3proxy. S45–S46 HAProxy Dataplane API. S47–S48
proxy.py. S49–S50 ProxyBroker. S51–S52 Scylla. S53–S59 proxy_pool, go_proxy_pool, ProxyPoolHub,
metacubexd, proxy-collect. S60–S64 Webshare benchmark. S65–S66 proxy-chain. S67–S68 sticky-proxy.
S69 proxy-control. S70 local panel report. S71–S73 unlicensed/egress-gateway rejects. S74 missing
sspanel-uim. S75 v2board.

Full URLs, dates and verbatim quotes: `sources.tsv`. Claim-by-claim provenance: `claims.tsv`.
Search inventory: `searched.tsv`.

## Self-review (A–D)

**A — early output.** The first six matrix rows (Resin, Rota, PoolX, ProxyCat, proxyhub,
easy-proxies) were written before the remaining 17; the matrix was then extended to 23.

**B — attempted refutations.**

- *"Resin is best."* Tried to break it with `proxyhub` (same session-lease mechanic) and `ProxyCat`
  (same operational shape). **CONFIRMED**: proxyhub has 1 star and no tenancy (S23–S25); ProxyCat
  has no lease and is GPL-2.0 (S19–S22).
- *"A commercial-style panel beats Resin on features."* Webshare does beat Resin on sub-user
  passwords, IP allowlist and quotas (S61–S64). **WEAKENED, not overturned**: those are add-on
  features, and Webshare is a closed service, not a base.
- *"ProxyCat is reusable because it is a separate process."* **OVERTURNED for our purposes**: GPL-2.0
  still forbids embedding, and it has no account lease; it stays reference-only.
- *"easy-proxies has no license."* **OVERTURNED**: GitHub reports `NOASSERTION` but its LICENSE file
  is plain MIT (S26, S27); it is usable, just not a fit.
- *"Rota has sticky sessions."* **WEAKENED**: it is N-requests-per-IP per pool, not a TTL lease per
  account (S12); the README wording overstates it if read quickly.

**C — verification.** `python research/71-panel-alt/validate.py` output is pasted in the delivery
message; it checks section presence, matrix row count, Top-3 distinctness, TSV widths, and source
referential integrity.

**D — summary.** Established: Resin is the only account-lease + pool + panel + MIT option; Rota is
the best fallback; ProxyCat is the best operational analogue but license-blocked. Overturned:
easy-proxies' "no license" and Rota's "sticky" wording. Unknown: whether proxyhub matures; whether
any closed vendor's panel is licensable (not investigated — out of scope). Next: keep Resin, build
the four gap features in `ajs-accountd`. What I could still be wrong about: a private/closed panel
or a 2026 project outside GitHub search that implements leases better; GitHub full-text search is
polluted, so niche repos with generic READMEs may have been missed.
