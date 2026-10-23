# Resin v1.3.0 audit

Status: source audit complete · Date: 2026-10-23 · Slice: AJS-17 (retry of AJS-12)
Subject: `Resinat/Resin` tag `v1.3.0`, commit `5473c7e` ("docs: add v1.3.0 release notes"), cloned to
`C:/Users/dwgx1/AppData/Local/Temp/ajs-resin-audit-20261023/resin`.
Method: source reading only. No Resin process was run against AJiaSu; no vendor service was contacted;
no credentials were used. All line citations are from the v1.3.0 tree above.

## Answer

The 10 integration claims in `research/70-panel/REPORT.md` and `docs/PANEL.md` survive source review:
**10 of 10 CONFIRMED, 0 OVERTURNED, 0 UNKNOWN** (denominator 10, 2026-10-23). The integration design
is mechanically supported by Resin v1.3.0 as written — local subscription `content` + `PATCH`,
`socks5://127.0.0.1:<port>#<tag>` parsing, loopback node dialing, `(Platform, Account)` leases
anchored on egress IP with same-IP rotation, a single global proxy token, a 13–17 s scan loop with
egress-IP/latency gates before routability, cross-platform release assets, and a 30 s subscription
minimum interval.

Two precision notes that do not change the verdicts:

1. **13–17 s is the scanner cadence, not the per-node probe period.** `scanloop.DefaultMinInterval`
   is 13 s plus 0–4 s jitter; a node's egress IP is refreshed at `MaxEgressTestInterval` (default
   24 h) and latency at `MaxLatencyTestInterval` (default 1 h). New nodes get an *immediate* egress
   probe on pool insertion, which is why the 15–30 s onboarding estimate is plausible. The 15–30 s
   figure itself is an inference from those two facts, not a hard-coded constant.
2. **The 30 s minimum applies to the scheduled refresh interval, not to a manual `PATCH content`.**
   A content `PATCH` triggers an immediate asynchronous update regardless of `update_interval`, so
   churn is bounded by the caller's own rate limiting, not by Resin. Resin has no admin-API rate
   limit and no node-count cap; the only hard content limits found are the 1 MiB API body default
   and the 1 MiB subscription line-scanner buffer.

## Verdicts

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | License is MIT and the copyright line is as claimed. | [CONFIRMED] | `LICENSE:1` `MIT License`; `LICENSE:3` `Copyright (c) 2026 Resinat and contributors` |
| 2 | A local subscription accepts `content` and `PATCH /api/v1/subscriptions/{id}` can replace it. | [CONFIRMED] | `internal/api/server.go:106`; `internal/api/handler_subscription.go:114`; `internal/service/control_plane_subscription.go:174` (local requires content), `:312` (remote rejects content), `:308-319` (content patch accepted) |
| 3 | A content change triggers an asynchronous update without a separate refresh. | [CONFIRMED] | `internal/service/control_plane_subscription.go:401` `go s.Scheduler.UpdateSubscription(sub)`; `internal/topology/subscription_scheduler.go:198-207` reads `sub.Content()` directly for local subs |
| 4 | `socks5://127.0.0.1:<port>#tag` is parsed as a node and the fragment becomes the tag. | [CONFIRMED] | `internal/subscription/parser.go:1950-1952` dispatches `socks5://`; `:2010-2018` maps scheme to `socks`, `tag := decodeTag(u.Fragment)`; `:4777` `decodeTag`; `internal/subscription/parser_test.go:2475` fixture with `#SOCKS5%20Node` |
| 5 | Resin can dial a loopback (`127.0.0.1`) node at runtime (source or test evidence). | [CONFIRMED] | `internal/subscription/parser.go:1995-2005` accepts any hostname/IP with no loopback guard; `internal/outbound/builder_test.go:448-464` builds a real sing-box SOCKS outbound with `"server":"127.0.0.1","server_port":1080`; `internal/proxy/tunnel.go:90` dials via `routed.Outbound.DialContext` |
| 6 | Leases are keyed by `(Platform, Account)` and anchored on egress IP; same-IP rotation is supported. | [CONFIRMED] | `internal/model/models.go:95-111` (`LeaseKey{PlatformID, Account}`); `internal/routing/lease.go:18-23`; `internal/routing/router.go:258-260` (hit requires current egress IP == lease IP); `:288-315` same-IP rotation; `:418-440` candidate selection; `DESIGN.md:485-497` |
| 7 | Per-user proxy passwords/IP allowlists/hard quotas are NOT provided (one global proxy token). | [CONFIRMED] | `internal/config/env.go:140-146,174-183` single `RESIN_PROXY_TOKEN`; `internal/proxy/socks5.go:307` password must equal that token, `:320` username is only `Platform.Account`; zero non-test hits for quota/rate-limit/allowlist/max_nodes |
| 8 | Probe scheduling is 13–17 s; new nodes are not routable until a successful probe (15–30 s onboarding). | [CONFIRMED] | `internal/scanloop/scanloop.go:8-11` (13 s + 0–4 s jitter); `internal/probe/manager.go:286,292`; `internal/platform/platform.go:127-147` (outbound + egress IP + latency required); `cmd/resin/main.go:290-294` immediate egress probe on add; `internal/outbound/integration_test.go:52-80` asserts the gate. Note: per-node periods are 24 h/1 h (`internal/config/runtime.go:47-49`); 15–30 s is an inference, not a constant |
| 9 | Cross-platform: Linux/macOS/Windows release assets exist; Docker path is Linux-only. | [CONFIRMED] | `.github/workflows/release.yml:16-27` matrix (linux amd64/arm64, darwin amd64/arm64, windows amd64); `:200-240` Docker `linux/amd64,linux/arm64`; `Dockerfile:19-24` alpine runtime; GitHub release v1.3.0 API returned 5 assets (2026-10-23) |
| 10 | Dynamic node churn limits: subscription minimum interval and any node-count/refresh constraints. | [CONFIRMED] | `internal/service/control_plane_subscription.go:130` 30 s minimum, enforced at `:190` (create) and `:327` (patch); `internal/topology/subscription_scheduler.go:16,126-148` scan + 15 s lookahead; `internal/config/env.go:97` 1 MiB API body; `internal/subscription/parser.go:241` 1 MiB line buffer; no node-count cap found; ephemeral evict delay default 72 h (`control_plane_subscription.go:131`) |

Denominator: 10 claims checked, 10 rows above, 10 `[CONFIRMED]`, 0 `[WEAKENED]`, 0 `[OVERTURNED]`,
0 `[UNKNOWN]` (2026-10-23). Claims 1–5 were written to disk before claims 6–10 were investigated.

## Integration risks

1. **Loopback nodes require a shared network namespace.** `socks5://127.0.0.1:<gate>` only resolves
   to the gate if Resin runs natively or with host networking. Under Docker bridge networking, Resin's
   `127.0.0.1` is the container, not the host. The Docker release path is Linux-only anyway
   (`.github/workflows/release.yml:200-240`). Run Resin natively on the gate host, or use host
   networking, or publish gates on a non-loopback address.
2. **The single proxy token is a bearer capability, and identity is just a username.** Anyone who can
   reach the proxy port and knows `RESIN_PROXY_TOKEN` can present `Platform.Account` for any account
   (`internal/proxy/socks5.go:307,320`). There is no per-user secret, no IP allowlist, and no audit
   binding beyond request logs. Bind the proxy listener to loopback or a private interface.
3. **Vendor concurrency limits are invisible to Resin.** Resin will open as many concurrent flows per
   `(Platform, Account)` as clients ask for; no quota, rate limit, or per-account semaphore exists
   (zero hits, claim 7). The `ajs-accountd` gate must enforce the vendor's account-wide concurrency
   before Resin sees the node.
4. **Egress-IP staleness up to the probe period.** Lease hits compare the lease IP with the node's
   *stored* egress IP (`internal/routing/router.go:258-260`). A changed egress IP is only learned on
   the next egress probe; the default period is 24 h (`internal/config/runtime.go:47-49`), so a lease
   can keep pinning an account to a node whose real IP changed until the probe or a failure forces a
   re-evaluation. Lower `MaxEgressTestInterval` if IP stability matters.
5. **A node is unroutable until both probes succeed.** Routability requires an outbound, a valid
   egress IP, and at least one latency record (`internal/platform/platform.go:127-147`). If the probe
   URL is unreachable through a gate (or the latency authority is blocked), that node never becomes
   routable no matter how healthy the gate is. Choose probe URLs that the vendor egress can reach.
6. **Publishing a valid but empty subscription evicts the whole pool.** Parse failures keep the old
   node set (`handleUpdateFailure`), but a successfully parsed empty body diffs every node away.
   `docs/PANEL.md:108-109` already forbids empty publishes; the guard must live in `ajs-accountd`,
   because Resin will accept it.
7. **No admin-API rate limit; every content PATCH starts an update.** Resin coalesces nothing at the
   API layer; repeated PATCHes each spawn `go s.Scheduler.UpdateSubscription`
   (`internal/service/control_plane_subscription.go:401`). Coalesce to ≤1 per few seconds in the
   caller, as `docs/PANEL.md:108` proposes.
8. **Single-process, single-writer SQLite; no HA mode found.** Subscriptions are strong-persisted
   (`internal/state/engine.go:31`), nodes/leases weakly. Two Resin instances cannot share one state
   directory; there is no cluster/replica concept in `DESIGN.md`. Plan one control-plane instance, or
   shard by platform and accept separate state.
9. **SOCKS5 inbound is CONNECT-only.** `internal/proxy/socks5.go:341` rejects any command other than
   `CONNECT` (0x01); UDP ASSOCIATE is not implemented. Clients that need UDP through the pool must
   use the HTTP entrypoint or another path.
10. **Tag and region filters are string/GeoIP based.** Platform filters match `subscription/tag`
    (`internal/node/entry.go:136-166`) and egress region from GeoIP. Province-level AJiaSu IPs may
    map to coarse or wrong regions; verify the GeoIP database's China coverage before relying on
    region filters for province selection.

## What Resin does not solve

- **Per-user identity and entitlements.** No user table, no per-user password, no IP allowlist, no
  quota, no per-account concurrency cap. Resin's unit is `(Platform, Account)` for leases and a
  single global proxy token for auth.
- **Vendor account lifecycle.** Login, session keep-alive, re-auth, account vault, entitlement
  refresh, and drain/rotate orchestration are outside Resin; that is `ajs-accountd`.
- **Egress-IP ownership or rotation policy.** Resin observes an egress IP; it cannot request a new
  one from the vendor. Rotation means "delete the lease and re-pick", not "change the vendor IP".
- **Traffic accounting for billing.** Request logs carry `account`, `node`, `egress_ip`,
  `ingress_bytes`, `egress_bytes`, but there is no quota enforcement, no billing ledger, and no
  per-user aggregation beyond log queries.
- **High availability.** No clustering, no shared state, no leader election. One process, one SQLite
  state directory.
- **UDP proxying on the SOCKS5 inbound.** CONNECT only.
- **Guaranteed onboarding latency.** The 15–30 s figure is a consequence of the scan cadence and the
  immediate probe on add; it is not an SLA, and failed probes delay it indefinitely.
- **Content validation/signing.** A local subscription accepts arbitrary sing-box JSON/URI text; it
  is parsed, not authenticated. Anyone with the admin token can publish any node set.

## Open questions

1. Does AJiaSu treat several accounts behind one egress IP as one concurrency bucket, or per account?
   This determines whether Resin's IP-anchored leases help or accidentally concentrate load.
   Not answerable from Resin source; needs vendor observation.
2. What probe URL and latency authority are reachable through every AJiaSu region, and what do they
   measure — gate RTT or vendor egress RTT? The source only shows configurable URLs
   (`internal/config/runtime.go`), not a recommended target.
3. How often does an AJiaSu FV session's egress IP actually change, and is the change visible without
   a probe? If changes are frequent, the 24 h default egress period is too slow; if rare, it is fine.
4. Does the vendor permit the same account to be used from two gates concurrently (the drain/rotate
   path assumes brief overlap)? Source cannot answer.
5. Is `content` PATCH atomic enough for a 1 MiB payload under SQLite weak/strong persistence split,
   and what happens to in-flight requests when the node set is swapped? The code diffs under
   `WithOpLock` and keeps old nodes until removal; behavior under a large churn burst was not tested
   here.
6. Does the Windows release binary run the same probe/GeoIP path as Linux (embedded GeoIP download,
   file locking)? Assets exist, but I did not execute the Windows binary.
