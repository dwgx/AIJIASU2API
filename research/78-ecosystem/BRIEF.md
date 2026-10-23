# BRIEF — ecosystem components and patterns for the AJiaSu control plane

Slice: AJS-49 · Date: 2026-10-23 · Status: complete
Workspace: `research/78-ecosystem/` · Write scope: this directory only

## Question

Which existing open-source components and patterns should the AJiaSu proxy control plane adopt or
reject for gate/session supervision, credential vault, persistence, observability, and dashboard
UX, as of 2026-10?

## Restatement

The repository already has a working skeleton: a SOCKS5 gate with drain and byte counters
(`daemon/internal/gate`), a process supervisor (`daemon/internal/supervisor`), an AES-256-GCM
secret store (`daemon/internal/secret`), an in-memory store persisted as a 30-second JSON snapshot
(`daemon/internal/store`, `daemon/cmd/ajs-accountd`), a Resin v1.3.0 integration
(`daemon/internal/{resin,publisher,runtime}`), and a React dashboard with a mock-first data layer.
The question is not "what should we build" but "what already exists that we should reuse, copy as a
pattern, or deliberately refuse" in five areas, and where exactly each choice lands in our files.
The decision it serves: the next implementation slice after the M1 spike, before the daemon grows
more hand-rolled infrastructure.

## DONE WHEN

`REPORT.md` contains a decision table covering all five areas with at least three candidates each,
at least 15 dated sources with locators, at least 20 graded claims, a top-5 code-change list where
every item names a file and line, a closed gap loop with an explicit stop reason, and a verification
verdict on the central persistence claim.

## Scope

In scope:
- Gate/session supervision: process supervision, admission control, SOCKS5 serving, restart policy.
- Credential vault: at-rest encryption, key sourcing, rotation, envelope format.
- Persistence: the daemon's state store, durability, migration, backup.
- Observability: metrics exposition, profiling, structured logs, naming.
- Dashboard UX: data fetching, accessibility primitives, API mocking.
- Local code under `daemon/internal/{gate,supervisor,store,secret,account,runtime,resin,publisher}`,
  `daemon/cmd/ajs-accountd`, `dashboard/src`, and prior research in `research/70..77`.

Out of scope:
- The emulator/FV protocol internals (`emulator/`), vendor endpoints, and any live vendor call.
- `deploy/` (closed by the Deploy slice) and any git operation.
- Implementing any recommendation. This workspace only decides and points.

Time window: components and docs read on 2026-10-23; publication dates recorded where visible.
Geography/population: single-host, self-use control plane (one operator, tens of accounts, hundreds
of nodes); not a multi-tenant SaaS.

## Source classes

1. Primary upstream source and official docs (repository README/DESIGN at a pinned tag, SQLite,
   Prometheus, OpenTelemetry, Go standard library, RFCs).
2. Local primary evidence: our code at named lines, our README, the M1 spike experiment.
3. Secondary: search-result summaries only to locate primaries; not used for any claim.

## Budget

Max 30 sources target, max 2 gap rounds, max 90 minutes of search. Actual: 32 sources indexed and
read with quotes (two added in the correction pass), 3 gap rounds (the last two empty), 0 vendor
calls, 0 credentials.

## Exclusions recorded

- `research/70-panel/sources.tsv` and `research/71-panel-alt/sources.tsv` were used as an index of
  candidates, not as evidence: their quotes are re-derived from the upstream primary where a claim
  survives into this report.
- Vault, Tink, bbolt and Litestream were read only at README/overview level; no benchmark was run.
- No package was installed, so no binary-size or build-time number is claimed.

## Answer shape

One answer in two sentences, then a decision table (area × candidate × verdict × integration point
× risk), then the five code changes, then falsifiers, open questions, gap loop, verification.
