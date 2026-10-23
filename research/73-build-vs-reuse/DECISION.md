# DECISION — proxy control plane: Resin, Rota, custom, or hybrid

Date: 2026-10-23 · Slice: AJS-13 · Evidence: `REPORT.md` in this directory

## Recommendation: **D — Hybrid**

Run **Resin v1.3.0 unmodified** as the data plane (node pool, egress-IP sticky lease, health probes,
circuit breaker, request logs, WebUI). Put a **thin custom `ajs-gateway` + console** in front of it
(SOCKS5/HTTP entry, per-user password/API key, IP allowlist, concurrency/RPS, byte quota, audit).
`ajs-accountd` from the current design is unchanged. No fork of Resin.

## Why

- **Resin owns the only hard primitive.** Its lease key is `(PlatformID, Account)` and the lease is
  anchored on the **egress IP** (`models.go:95-109`). That is exactly what AJiaSu needs: shared egress
  IPs, several accounts able to reach the same IP, and a sticky identity that survives a node death by
  moving to another account's node on the same IP. No other *mature* candidate pairs that with a pool
  gateway and a permissive licence; the closest, proxyhub, is 1-star with no tenancy.
- **Resin alone does not meet the requirement set.** One global proxy token, no per-user password, no
  IP allowlist, no quota enforcement. The 70-panel report's own falsification #4 was *overturned* on
  this point. Option A ships three of the six outright, two partially, and leaves per-user auth/quota
  out entirely; D closes the gap with ~2k lines of our Go.
- **Resin cannot select a node per request.** Routing picks from a Platform view by P2C/random
  (`router.go:397`). `PLAN.md` §4.5 — "switch node at runtime, the listening socket never closes" — is
  a gateway feature. D's pinned path implements the north star; A only approximates it.
- **The gateway is the insurance policy.** It is the only thing between users and Resin, so if Resin is
  abandoned (bus factor 1: 372 of 378 contributions from one account) or a requirement outgrows it, the
  data plane is swapped without changing the client contract. That is what makes reusing an
  eight-month-old project defensible.
- **It is the cheapest option that meets the spec.** A: 1.5 weeks but 3/6 features outright (2 partial);
  D: 3–4 weeks, 6/6; C: 4–6 weeks to MVP and 8–12 to parity, re-implementing 29,593 lines of solved
  pool logic. B: never — Rota's `stick` is an N-request counter, not an IP-anchored lease, and it adds
  TimescaleDB.

Weighted scores (1–5, weights 15/20/15/5/10/10/10/15): **A 75.0 · B 46.0 · C 72.0 · D 77.0.**

## Cost

~5,800–7,800 new production LOC, ≈ 8,700–11,700 with tests (gateway 1,800–2,600 Go, console
1,500–2,200 TS, `ajs-accountd` 2,500–3,000 Go), reusing `ajsproxy` (3,372 LOC, exists) and Resin
(29,593 LOC, upstream, unmodified). Milestones: M1 spike 2 days → M2 accountd 1 week → M3 gateway +
console 1–2 weeks.

## What would change the decision

| Signal | Switch to | Why |
|---|---|---|
| v1 is strictly self-use, one operator, no per-user credentials, no single-socket node switching | **A** | The gateway buys nothing; A is the same system minus 2 weeks. A only wins if time is weighted above 25/100 while features stay at 20 — see the sensitivity algebra. |
| `PLAN.md` §4.5 is a hard requirement (one listener, next request exits a different node) | **D** | Even one user needs the pinned path; Resin can only rotate a lease or edit filters, both cold-path. |
| Resin refuses loopback node dials, or node churn outruns the 5 s publish coalescing | **C** | Nothing else has the lease primitive; budget 4–6 weeks. |
| Resin gets no release for 6 months, or a security advisory sits unpatched for 30 days | **C** (or fork) | Bus factor 1 is the standing risk; the gateway keeps this switch cheap. |
| Fleet of 3+ hosts / 3+ regions | **C** at that size | Resin has no clustering or replication code at all; the gateway becomes a shard router first. |
| Someone asks to resell or publish endpoints | **none** | Out of scope by the license/self-use boundary; not an architecture choice. |

**Never B.** Apache-2.0 is fine, but the product's core requirement (sticky egress IP per account) is
absent from its data model, and adding it means writing the lease engine anyway — on top of Postgres.

## Three things that will bite during implementation

1. **Networking:** nodes are `127.0.0.1:<gate>`; a two-container Compose with bridge networking cannot
   reach them. Use host binaries, or one container / shared netns.
2. **Cold paths:** a new node needs one probe cycle (15–30 s) to become routable; forcing a *different*
   IP needs a temporary Platform filter change plus refresh. Neither is a synchronous API — coalesce and
   rate-limit both.
3. **Two sources of truth for bytes:** the gateway's counters are exact, Resin's request logs are
   per-request and async. Quota uses the gateway; forensics uses Resin; never sum them.
