# LEDGER — AJS-49 ecosystem scout

Append-only. One line per event. Times are local (UTC+09:00 where the source says so); dates are
2026-10-23 unless stated.

## Phase 0 — frame

- 2026-10-23 Wrote `BRIEF.md`. Question restated; DONE WHEN fixed at 15 sources / 20 claims /
  decision table / top-5 / gap loop / verification. Budget: 30 sources, 2 gap rounds.
- Excluded: `deploy/` (closed), vendor calls, credentials, any code edit, any git operation.

## Phase 1 — index (local first)

- Read `docs/PANEL.md` (design: ports, API, SQLite schema §6, vault §7, lifecycle §8).
- Read `research/73-build-vs-reuse/DECISION.md` (hybrid D, 77.0 vs A 75.0).
- Read `research/76-sdk-integration/INTERFACE.md` (13 interfaces; SecretStore/Observer/Classifier).
- Read `research/70-panel/REPORT.md:1-203` (candidate matrix, Resin reuse decision).
- Read `research/77-m1-spike/REPORT.md` (PASS; 12.6 s recovery; delete != new IP; 1 h probe default).
- Indexed code: `daemon/internal/{gate,supervisor,store,secret,api,runtime,publisher,resin,account}`,
  `daemon/cmd/ajs-accountd/main.go`, `dashboard/src/{lib,components,pages}`, `dashboard/package.json`.
- Indexed reference repos: `D:/Project/KiroStudio/admin-ui` (Radix + TanStack + Tailwind),
  `D:/Project/launcher`, `D:/Project/AwesomeUI` (catalog/dissections), `D:/Project/Designers_Brain`
  (style cards, conflicts). Used as pattern index only; no claim rests on them.
- Excluded from the source table: `research/71-panel-alt` candidate rows (their evidence is a
  snapshot of the same GitHub API already superseded here), the emulator protocol docs (out of
  scope), and all vendor material.

## Phase 2 — query expansion

- Mechanism: how does Resin hold a lease across a node death? (S01, S27)
- Chronology: what changed in Resin 1.3.0 and when? (S01, S27 image digest/build time)
- Quantitative: recovery times, probe defaults, snapshot interval, WAL writer count (S27, S28, S03)
- Stakeholders: operator (single host) vs upstream maintainer (bus factor) (S01, S27)
- Counter-case: is the JSON snapshot good enough? is OTel worth it? is a library gate better? (S28,
  S16, S25)
- Queries run: `modernc.org/sqlite production cgo`, `Windows Credential Manager LocalSystem`,
  `OpenTelemetry Go SDK vs Prometheus single binary`, `SQLite WAL small database benchmark`,
  `go-keyring Windows service` (no primary hit). Hits recorded in `sources.tsv` S04–S06, S12, S16.

## Phase 3 — primary reads

- Read with quotes: S01 Resin README, S02 Resin DESIGN.md, S03 SQLite WAL, S04/S05 modernc,
  S06 mattn, S07 bbolt, S08 Litestream, S09 Vault Transit, S10 age, S11 go-keyring, S12 LocalSystem,
  S13 Prometheus naming, S14 client_golang, S15/S16 OTel, S17 TanStack, S18 Radix, S19 RFC 1928,
  S20 semaphore source, S21 rate source, S22–S24 Go 1.26.7 stdlib docs, S25 go-socks5, S26 MSW,
  S27 M1 spike, S28 daemon README, S29 PANEL, S30 slog.
- Read but not used as evidence: `research/71-panel-alt/searched.tsv` (candidate list only).

## Phase 4 — claims

- Wrote 42 claims. Grades: 39 A, 3 B (C17 age, C19 LocalSystem, C31 go-socks5 feature-list
  absence), 0 C.
- Contradiction kept: C04 (1 h default probe interval) vs the 15–30 s cold-path estimate in
  `research/70-panel/REPORT.md`. Both rows remain; the report cites the measurement, not the
  estimate.

## Phase 5 — gap loop

- Round 1 (targeted): S02, S05, S12, S16 added; C08, C12, C19, C22 added. Keyring-under-service
  remained open.
- Round 2 (falsify WAL overhead): only S03 re-surfaced; no new source, no new claim → empty.
- Round 3 (falsify modernc build cost): release pages only; no primary number → empty.
- Stop: two consecutive empty rounds. Open items moved to REPORT open questions 1–3.

## Phase 6 — synthesis

- Wrote `REPORT.md` with 29 decision rows, 5 code changes, falsifiers, open questions.
- Every recommendation names a file and line from the working tree on 2026-10-23.

## Phase 7 — verification

- Spawned `verifier` (read-only) with the central persistence claim and the driver claims; did not
  provide the draft report or the conclusion. Runtime 2m32s.
- Verifier returned: **VERIFIED**, with one scoped qualification.
- Evidence it re-derived: `persist.go:20-38` (one Snapshot struct), `:55-100` (whole-struct marshal),
  `:147`/`:155`/`:180` (snapshot, temp file, rename); `main.go:180` (30 s constant), `:88` (ticker),
  `:93` and `:104` (only two saveState calls), `:212` (only st.Save); no Save/Snapshot reference in
  api/runtime/account/supervisor; README lines 605-608, 618-620, 1466-1468; `go test ./internal/store
  -count=1` ok. Driver facts: modernc README line 1 and `sqlite.go` (no `import "C"`); mattn README
  requires CGO_ENABLED=1 + gcc.
- Qualification it found: `secret.Store.Seal` writes `secrets.json` synchronously, so a crash can
  keep a sealed blob whose account row is lost. The 30 s window applies to panel rows; the orphan
  blob is a second durability rule. C34 was refined accordingly.
- New claim from this evidence: C43 (`daemon/Dockerfile:21` builds with `CGO_ENABLED=0`). Claims
  count is now 43 (41 A, 2 B); the phase-4 line above records the pre-verification count.
- Residual unverified: modernc binary size/build (open question 1), keyring under a Windows service
  (open question 2), probe-interval persistence (open question 3).

## Post-synthesis corrections (append-only)

- Re-read Resin `DESIGN.md` at the lease sections (S31) after the first draft. It states the sticky
  anchor is the egress IP and that same-IP nodes are interchangeable, and that a failed lease node is
  rebound in place. C02 was strengthened with those locators; no verdict changed.
- Added S32 (`research/73-build-vs-reuse/DECISION.md`) so the bus-factor sentence in the decision
  table has a locator.
- Added one decision row: strong/weak durability split (S31 lines 552-560). Decision rows now 30.
- Corrected two line citations found by re-grep: `gate.State` is at `server.go:250` (not 255);
  the mock branch in `dashboard/src/lib/api.ts` is at line 27 (not 19).
- Removed two unsourced numbers from the first draft (a TanStack bundle size and a semaphore line
  count); they were not measured in this slice.
- Final counts: 32 sources, 43 claims (41 A, 2 B), 30 decision rows, 5 code changes.
- Added one more decision row after reviewing the session seam: `Healthy()` is only
  `SID() != ""` (`daemon/internal/account/session.go:205`), so the gate canary must decide `LIVE`.
  Decision rows now 31. The report's counts section was updated in place; this line records why.
- Added S33 (local `KiroStudio/admin-ui/package.json`) and C44 so the dashboard "reject the library"
  verdict shows the counter-precedent it was weighed against. Final counts: 33 sources, 44 claims
  (42 A, 2 B), 31 decision rows, 5 code changes.
- 14:15 scope check found concurrent edits in `daemon/internal/{api,gate,publisher,runtime,store,supervisor}`
  (accept-loop backoff, child-env allowlist, history retention caps, rotation locking). I did not
  touch them. Cited line numbers were re-checked and updated; symbol names are the stable locator.
  No concurrent edit adds Prometheus exposition, `Cmd.WaitDelay`, or SQLite persistence, so the
  report's verdicts stand. `main.go`, `secret/store.go` and `dashboard/` were unchanged.
