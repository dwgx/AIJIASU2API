# REPORT — ecosystem components and patterns for the AJiaSu control plane

Slice: AJS-49 · Date: 2026-10-23 · Workspace: `research/78-ecosystem/`
Question: which existing open-source components and patterns should the control plane adopt or reject
for gate/session supervision, credential vault, persistence, observability, and dashboard UX?

## Answer

Keep the architecture that already works (Resin v1.3.0 for leases and circuit breaking, our gate for
drain/metering, our supervisor for per-node processes) and adopt four narrow, boring pieces around
it: `prometheus/client_golang` for metric exposition, SQLite in WAL mode through
`modernc.org/sqlite` for state, `os/exec.Cmd.WaitDelay` plus a real node controller for supervision,
and the Vault-style key-version/rotation *pattern* (not Vault itself) for credentials.
Reject the heavyweight alternatives at this size — Vault, age, Tink, bbolt, Litestream, the
OpenTelemetry SDK, `things-go/go-socks5`, and TanStack Query as a dependency — while taking two
narrow pieces rather than platforms: polling with in-flight dedupe inside `useAsync`, and Radix
Dialog/Tooltip for the two modal surfaces only.

Confidence: **high** for the local-code facts and the primary-document claims (42 A, 2 B, 0 C in
`claims.tsv`); **medium** for the persistence driver choice, because no build, binary-size or
throughput measurement was run; **low** for using the OS keyring under a Windows service, which
remains open (C19).

## Decision table

Verdict vocabulary: **ADOPT** = add/keep the component; **ADAPT** = copy the pattern or use a
narrow subset; **REJECT** = do not add, with the reason that would otherwise justify it.
Line numbers were captured at 14:05 and re-checked at 14:15; `daemon/` was being edited by a
concurrent slice (accept-loop backoff, child-environment allowlist, history retention caps), so
symbol names are the stable locator. None of those edits adds Prometheus exposition, `WaitDelay`, or
SQLite persistence, so the verdicts below are unchanged.

### Gate and session supervision

| Candidate | Verdict | Concrete integration point | Risk / condition |
|---|---|---|---|
| Resin v1.3.0 Platform + lease + circuit breaker, unmodified | **ADOPT** (already) | `daemon/internal/runtime/panel.go:313` `reconcileLocked`; `daemon/internal/publisher/publisher.go:294` `Publish` | Bus factor 1 upstream — 372 of 378 contributions from one account (S32); the default `max_latency_test_interval` is 1 h, so a restarted node stays out until patched to 30 s (C04, S27 §8). Set it in `EnsureProbeConfig` (`publisher.go:518`). |
| Our SOCKS5 gate with drain, admission limit and byte counters | **ADOPT** (keep) | `daemon/internal/gate/server.go:277` `Drain`, `:504` `acquire`, `:254` `State` | No authentication and no per-account aggregate limit yet; that is the P3 gateway's job, not a reason to replace the gate. |
| `things-go/go-socks5` as the gate | **REJECT** | would delete `gate/server.go` | Its feature list has CONNECT/ASSOCIATE, rules and a custom logger, but no drain, admission limit or byte metering (C31, S25). We would re-add all three and inherit UDP/BIND surface we do not serve. |
| `golang.org/x/sync/semaphore` for admission | **REJECT** | would replace `gate/server.go:141` (`slots chan struct{}`) and `:504` | Weighted and context-aware, but no drain concept, no SOCKS5 reply mapping, and no non-blocking fast path with the current drain check (C26, S20). The existing channel semaphore already implements the needed semantics. |
| `golang.org/x/time/rate` for per-user RPS | **ADAPT** (P3 only) | new `daemon/internal/gate/ratelimit.go`, wired at the P3 gateway | Token bucket with `Wait(ctx)` fits per-user RPS (C27, S21). Not needed until per-user auth exists; adding it to the no-auth gate would rate-limit Resin, not users. |
| `os/exec.Cmd.WaitDelay` + process-group kill | **ADOPT** | `daemon/internal/supervisor/supervisor.go:277` (`exec.CommandContext`), `:469` `stopHandle` | WaitDelay bounds a child that ignores cancellation or leaves pipes open (C28, S22); on Windows it does not kill grandchildren, so pair it with a Job Object (or `Setpgid` on Unix) [INFERENCE: not exercised in this slice]. |
| One goroutine per node with an explicit state machine and restart backoff | **ADOPT** (pattern) | new `daemon/internal/runtime/nodes.go`; consume `Supervisor.Events()` (`supervisor.go:240`) | The supervisor currently emits `exited` but nothing consumes it and it is not wired into `main` at all (C38, S28). Restart must be idempotent and must not race a drain. |
| Session liveness: `Healthy()` = non-empty SID vs an end-to-end canary | **ADAPT** (canary is the signal; `Healthy()` is configuration validity only) | `daemon/internal/account/session.go:205`; `docs/PANEL.md` §7 `AJS_CANARY_INTERVAL` / `AJS_CANARY_TARGET`; `POST /v1/nodes/{id}/actions/probe` | A non-empty SID does not prove the vendor session is alive; no vendor idle-timeout number was found in the sources. Publish a node only after a canary through the gate, and mark it `DOWN` after N consecutive failures. [UNVERIFIED: vendor idle timeout] |
| systemd unit / supervisord as the process supervisor | **REJECT** | deployment only (`deploy/compose.yml`) | The daemon already owns per-node children; a second supervisor duplicates ownership and hides the process tree from the API. Container `restart: unless-stopped` covers host-level restarts. |

### Credential vault

| Candidate | Verdict | Concrete integration point | Risk / condition |
|---|---|---|---|
| Current AES-256-GCM with account id as AAD, 0600 file, atomic rename | **ADOPT** (keep) | `daemon/internal/secret/store.go` `Seal`/`Open`/`Rotate`; `docs/PANEL.md` §6 | No key id in the envelope (the existing version byte identifies the format, not the key) and rotation is not wired to the store snapshot (S28 §Secret storage). Add a key id alongside the version byte. |
| HashiCorp Vault Transit | **REJECT** | would add a service dependency to `main.go` | Encryption-as-a-service with key versioning (C16, S09), but it contradicts the single-binary/zero-middleware constraint the whole design rests on (S02) and adds a network hop to every login. |
| age (`filippo.io/age`) | **REJECT** | would replace `daemon/internal/secret/store.go` | File/stream encryption with explicit keys (C17, S10); no per-account AAD, no in-process rotation API, no reason to replace 200 lines of `crypto/aes`. |
| `zalando/go-keyring` as the master-key source | **ADAPT** (optional, interactive only) | `daemon/cmd/ajs-accountd/main.go:222` `openStore` | `docs/PANEL.md` §7 already promises a keyring fallback that the code does not implement (S29). But a LocalSystem service likely has no user vault (C19 open, S12) and the keyring dependency is unverified under a service — keep `AJS_MASTER_KEY` as the service default. |
| Google Tink keysets | **REJECT** | would replace `daemon/internal/secret/store.go` | Adds a keyset format and dependency for exactly one symmetric key; our AAD binding already prevents row swapping. |
| Key-version prefix + `min_decryption_version` equivalent | **ADOPT** (pattern from Vault) | `daemon/internal/secret/store.go` envelope (`version‖nonce‖ct`) | Enables rotation without re-sealing every row in one transaction. Existing blobs carry version=1; treat that as key id 1 and introduce key id 2 for new seals, so old blobs stay readable during migration. |

### Persistence

| Candidate | Verdict | Concrete integration point | Risk / condition |
|---|---|---|---|
| SQLite in WAL mode via `modernc.org/sqlite` | **ADOPT** | replace the body of `daemon/internal/store/persist.go` (`Snapshot:55`, `Save:146`, `Load:197`) behind a `Persister` interface; wire at `main.go:68` and `:88` | First external dependency (module is large; no size measured — [UNVERIFIED]). Single writer is fine at our mutation rate (C09, S03), and WAL forbids network filesystems (C10), so the state dir must stay local. |
| `mattn/go-sqlite3` | **REJECT** | would replace `daemon/internal/store/persist.go` | Requires cgo and gcc (C13, S06), while the image builds with `CGO_ENABLED=0` (C43, `daemon/Dockerfile:21`); adopting it would change the build, not just the store. |
| bbolt | **REJECT** | would replace `daemon/internal/store/persist.go` | Pure Go, but one read-write transaction and a whole-file lock (C14, S07); usage-by-day aggregation and request-log filtering would be hand-built indexes. |
| Litestream | **REJECT for v1**, revisit for off-host backup | would wrap the SQLite file created by `daemon/internal/store/persist.go` | Beta, adds a `_litestream_lock` table and a replica target (C15, S08); a single-host control plane needs local durability first. |
| Strong vs weak durability split (Resin pattern) | **ADOPT** (pattern) | `daemon/internal/store/persist.go`: synchronous transactions for accounts, users, API-key hashes and audit; batched/weaker for nodes, leases and usage rows | Misclassification is the risk: a weak table must never hold a credential, a key hash or a port allocation that cannot be rebuilt (S31 lines 552-560). |
| The JSON snapshot as the only store | **ADAPT** (export/backup only) | `persist.go:55` `Snapshot` | Keep it as `ajs-accountd export` for support and migration; the 30 s rewrite window loses mutations on crash (C34, S28). |

### Observability

| Candidate | Verdict | Concrete integration point | Risk / condition |
|---|---|---|---|
| `prometheus/client_golang` + `promhttp` | **ADOPT** | add `GET /metrics` at `daemon/internal/api/server.go:132`; source values from `gate.State` (`gate/server.go:254`), `Supervisor.Events` (`supervisor.go:579`), publisher and Resin client counters | Label cardinality: `node_id` is bounded (hundreds), `user_id`/target must never be labels (C20, S13). Keep the existing JSON `/v1/metrics` for the dashboard. |
| OpenTelemetry SDK + OTLP | **REJECT for v1** | would add `daemon/internal/api/metrics.go` plus exporter wiring in `main.go` | OTel recommends a Collector in production and its Go Prometheus exporter is marked experimental (C22, S16); the SDK adds an exporter pipeline for a single-binary tool with no collector. Revisit only when a collector exists. |
| `net/http/pprof` on a loopback-only mux | **ADOPT** | `main.go` next to the admin listener, separate port | Profiles expose memory and goroutine state; bind 127.0.0.1 and never widen it (C30, S24). |
| `log/slog` JSON handler with a redaction attr | **ADOPT** | `main.go:52` (currently `NewTextHandler`) | The gate logs one line per flow (C36); move per-flow detail to metrics and keep one line per rejection/state change to avoid log volume. |
| Prometheus naming conventions | **ADOPT** | new metric names, e.g. `ajs_gate_active_flows`, `ajs_gate_ingress_bytes_total`, `ajs_node_ready_seconds` | None; it is a naming rule (C20, S13). |

### Dashboard UX

| Candidate | Verdict | Concrete integration point | Risk / condition |
|---|---|---|---|
| TanStack Query as the data layer | **REJECT the dependency now, ADOPT the pattern** | `dashboard/src/lib/useAsync.ts:10` | Eight pages, one resource each, 25 tests; an extra dependency and cache layer is not justified yet. Add `refetchInterval`, in-flight dedupe and pause-on-hidden to `useAsync` (C23, S17). Revisit when two views share a resource. |
| Radix Primitives (Dialog + Tooltip only) | **ADAPT** | `dashboard/src/components/Overlay.tsx`, `useFocusTrap.ts` | Radix handles ARIA, focus and keyboard nav (C24, S18); adopt it for the two highest-risk surfaces, keep the existing CSS tokens, do not import a design system. |
| Mock Service Worker | **ADAPT** (test first, mock mode second) | `dashboard/src/lib/api.ts:27` (`USE_MOCK` branch) | MSW intercepts at the network layer (C32, S26), which removes the mock/live fork in `api.ts` and lets tests exercise the real client. Service-worker setup in `vite preview` is the cost; start in Vitest. |
| Recharts | **ADOPT** (already) | `dashboard/src/pages/Overview.tsx` | Already in use; keep, but lazy-load the chart route to protect first paint. |
| `sonner` toasts | **REJECT** | `dashboard/src/components/Toast.tsx` | The in-house toast already covers the need; a second notification system adds a dependency and a visual language. |

## Why these verdicts

**Gate/session.** The local code is already better matched to the problem than the generic
libraries: the gate implements the exact SOCKS5 subset Resin dials, with a drain state machine and
live counters, and the supervisor already owns process trees with a readiness probe. The gaps are
not missing components but missing wiring and hardening: no `WaitDelay`, no process-group kill, no
consumer of `Supervisor.Events`, and no node controller that ties account → process → gate →
publisher together (C37, C38). Adopting a library would add surface without closing those gaps. The
one pattern worth importing is the explicit per-node state machine (`STARTING → READY → LIVE →
DRAINING → DOWN`) that `docs/PANEL.md` §8 already describes; it belongs in a new
`runtime/nodes.go`, not in the gate. Session liveness is the weakest seam: `Healthy()` only checks
that the SID is non-empty (`account/session.go:205`), so a dropped vendor session looks healthy
until the gate canary fails; the canary, not the flag, must decide `LIVE`.

**Vault.** The current design is the right shape: AEAD with the account id as AAD, 0600 file,
atomic rename, no plaintext in the snapshot (S28, S29). What is missing is key lifecycle, not
cryptography. Vault Transit proves the pattern — versioned keys and a decrypt floor (C16) — but
running Vault to protect one key contradicts the single-binary design (S02). The cheap adoption is a
key id in the envelope plus rotation that re-seals the snapshot's map, which the README already
flags as broken (S28 §Secret storage). The keyring is a convenience for an interactive operator, not
a service-account mechanism (C19).

**Persistence.** This is the one place where the current implementation is a documented stopgap.
The store rewrites the whole JSON document every 30 s and on shutdown (C33), so a crash loses up to
30 s of panel mutations (C34) and save cost grows with request/audit history — now bounded by the
concurrent retention caps (`AuditRetention`, `RequestRetention` in `store.go`), but still a
whole-document rewrite (S28). The sealed-secret file is written synchronously, so a crash can keep a
password blob whose account row was lost — two files with two durability rules, which is itself an
argument for one transactional store. SQLite WAL is the
right target because it matches the existing design (S29), the upstream project we depend on made
the same choice (S02), and it gives per-mutation transactions with concurrent reads (C09). The
driver choice is the actual ecosystem question: `modernc.org/sqlite` is cgo-free (C11) and actively
released (C12), while `mattn/go-sqlite3` would force cgo/gcc into every build (C13). bbolt is a
worse fit because our queries are relational (C14); Litestream is a backup tool, not a store (C15).
Resin's own split — strong persistence for config, platforms and subscriptions, weak for nodes and
leases (S31 lines 552-560) — is the right classification for our tables as well: credentials, users,
key hashes and audit must be synchronous; node, lease and usage rows can tolerate a batch.

**Observability.** The daemon currently has a JSON metrics endpoint for the dashboard (C35) and text
logs, but nothing a Prometheus server can scrape and nothing for CPU/goroutine profiling. The
smallest complete addition is `promhttp` plus `pprof` on loopback; both are standard, both have no
runtime dependency, and both match the naming rules (C20, C21). OTel is the right answer for a
multi-service deployment with a collector, which this is not (C22). The log handler should be JSON
so the gate's per-flow lines and the supervisor's lifecycle events can be filtered without regexes
(C36, S30).

**Dashboard UX.** The dashboard is mock-first and each page loads once; there is no polling, so a
node that goes `LIVE` or `DRAINING` after page load is invisible until a manual refresh (C39). The
cheapest fix is the TanStack pattern without the library: `refetchInterval` per resource plus
in-flight dedupe and a visibility pause (C23). The mock/live fork inside `api.ts` means the mock
path never exercises the real HTTP client (C40); MSW fixes that at the network layer (C32) and is
worth doing in tests before it is worth doing in the browser. The local `KiroStudio/admin-ui`
reference does run Radix + TanStack Query + sonner + Tailwind (S33, C44), so the components are
proven in a comparable console; the difference is coordination need, not capability — our eight
pages do not yet share cache entries or need optimistic updates.

## Top 5 code changes

Each item names the file and line to change and the test that proves it. None is implemented in
this slice.

### 1. Prometheus exposition and gate/supervisor instrumentation

- Files: `daemon/internal/api/server.go:132` (route table), new `daemon/internal/api/metrics.go`;
  read counters from `daemon/internal/gate/server.go:254` `State()`; hook
  `daemon/internal/supervisor/supervisor.go:579` `emit`; publish latency from
  `daemon/internal/publisher/publisher.go:294`.
- Change: add `GET /metrics` returning `promhttp.HandlerFor(registry, ...)`; register
  `ajs_gate_active_flows{node_id}`, `ajs_gate_admitted_total{node_id}`,
  `ajs_gate_rejected_total{node_id,reason}`, `ajs_gate_ingress_bytes_total{node_id}`,
  `ajs_gate_egress_bytes_total{node_id}`, `ajs_node_ready_seconds{node_id}`,
  `ajs_resin_request_duration_seconds{method,status}`. Keep `/v1/metrics` untouched for the UI.
- Expected test: `daemon/internal/api/server_test.go` — `TestMetricsExposition`: open one gate flow
  through the test session fake, scrape `/metrics`, assert `Content-Type` starts with
  `text/plain; version=0.0.4` and that `ajs_gate_active_flows{node_id="n1"} 1` and
  `ajs_gate_ingress_bytes_total{node_id="n1"} > 0` appear; assert `user_id` never appears as a label.

### 2. Replace the 30-second snapshot with SQLite WAL behind a `Persister` seam

- Files: `daemon/internal/store/persist.go:55` `Snapshot`, `:146` `Save`, `:197` `Load`;
  `daemon/cmd/ajs-accountd/main.go:68` load and `:88` ticker.
- Change: introduce `type Persister interface { Load(ctx) error; SaveAccount(...); ... }`; implement
  `sqlitePersister` with `PRAGMA journal_mode=WAL`, `busy_timeout=5000`, `synchronous=NORMAL`,
  one transaction per mutation; keep `Snapshot()`/`Save()` as `export`/`backup`. Migration reads the
  existing JSON once, writes rows, then renames the JSON to `state.json.migrated`.
- Expected test: `daemon/internal/store/persist_test.go` — `TestSQLiteCrashDurability`: create an
  account, close the store without a periodic save, reopen from the same file, assert the account
  and its sealed secret are present; `TestSQLiteMigration` asserts the JSON import is idempotent and
  the source file is renamed only after a successful commit.

### 3. Harden the supervisor and wire a node controller

- Files: `daemon/internal/supervisor/supervisor.go:277` (`exec.CommandContext`), `:416` `monitor`,
  `:469` `stopHandle`; new `daemon/internal/runtime/nodes.go`; `daemon/cmd/ajs-accountd/main.go`
  (construct supervisor + controller).
- Change: set `cmd.WaitDelay = 2 * time.Second` and a process group (Unix `Setpgid`, Windows Job
  Object); on `KindExited`, restart with exponential backoff (1 s → 2 s → 4 s, cap 30 s) unless the
  node is `DRAINING`; drive `gate.Server.Start/Drain/Undrain` from the controller and publish only
  `LIVE` nodes.
- Expected test: `daemon/internal/supervisor/supervisor_test.go` — `TestStopKillsGrandchild`: child
  ignores SIGTERM and spawns a grandchild; `Stop` returns within grace + `WaitDelay` and the
  grandchild is gone; `TestRestartBackoff`: three exits produce 1 s, 2 s, 4 s delays from a fake
  clock and no restart while draining.

### 4. Vault key lifecycle: key id, rotation, optional keyring

- Files: `daemon/internal/secret/store.go` `Seal`/`Open`/`Rotate`;
  `daemon/cmd/ajs-accountd/main.go:222` `openStore`.
- Change: make the envelope `keyID(1) ‖ version(1) ‖ nonce(12) ‖ ct`; `Rotate` writes new-key
  envelopes and keeps old key ids readable until `minKeyID`; re-seal the store snapshot's `secrets`
  map in the same operation (the README's documented gap); accept a keyring lookup only when
  `AJS_MASTER_KEY` is unset and `AJS_ALLOW_KEYRING=1`.
- Expected test: `daemon/internal/secret/store_test.go` — `TestRotateKeepsOldBlobsOpenable`: seal
  with key A, rotate to B, `Open` still succeeds for the old blob and new seals use key id B;
  `TestKeyringOptIn` with `keyring.MockInit()` asserts env wins and keyring is never touched when
  `AJS_MASTER_KEY` is set.

### 5. Dashboard live refresh and one data path

- Files: `dashboard/src/lib/useAsync.ts:10`; `dashboard/src/lib/api.ts:27`;
  `dashboard/src/pages/Nodes.tsx` and `Leases.tsx` call sites.
- Change: add `refetchInterval` (default 5 s for nodes/leases, 30 s for usage), in-flight dedupe,
  `document.hidden` pause and `keepPreviousData` semantics to `useAsync`; replace the `USE_MOCK`
  branch in `api.ts` with a transport injected at construction (MSW in tests, mock DB in the demo
  build) so the live path is the only HTTP path.
- Expected test: `dashboard/src/lib/useAsync.test.ts` with Vitest fake timers — two fetches after
  one interval, no overlapping fetch when the first is still pending, no fetch while hidden;
  `dashboard/src/lib/api.test.ts` with MSW asserts the same `http()` code path serves mock and live.

## What would change the answer

| Observation | Change |
|---|---|
| `modernc.org/sqlite` fails to build for windows/amd64 + linux/amd64, or adds more than ~15 MB to the binary | Fall back to an append-only journal + periodic snapshot (no dependency); keep the `Persister` seam so SQLite can land later. [UNVERIFIED: no build run] |
| Resin adds per-user passwords, quotas or IP allowlists upstream | Drop the P3 gateway and the `x/time/rate` adoption; the gate stays a metering relay only. |
| A second host or region enters the design | SQLite's single-writer/local-file constraint becomes a replication problem (C09, C10); Litestream or a real database moves from "reject" to "evaluate". |
| The daemon must run as a Windows service under LocalSystem | The keyring option is dead (C19); the master key must come from a protected file or DPAPI, and the `AJS_MASTER_KEY` path becomes mandatory. |
| The dashboard grows a second view over the same resource, or needs optimistic updates | Adopt TanStack Query rather than extending `useAsync`; the pattern's cost grows non-linearly. |
| A collector/OTLP backend appears in the deployment | OTel moves from reject to adopt; the Prometheus exporter alone is not the reason (C22). |

## Open questions

1. **Driver cost.** What is the added binary size and cold-start time of `modernc.org/sqlite` on
   windows/amd64 and linux/amd64? Not measured; no build was run. `[UNVERIFIED]`
2. **Keyring under a service.** Does `zalando/go-keyring` work when `ajs-accountd` runs as a
   Windows service (LocalSystem or a dedicated account)? The LocalSystem doc is indirect evidence
   only (C19). `[UNVERIFIED]`
3. **Probe interval persistence.** The M1 spike patched `max_latency_test_interval` at runtime and
   Resin logged "Loaded persisted runtime config"; whether that specific key survives a restart was
   not re-tested after the patch. `[UNVERIFIED]`
4. **Snapshot loss blast radius.** Which mutations in the 30 s window are actually harmful? Port
   slots are deterministic per `(account, server)`, so a lost node row may self-heal; a lost API key
   or user row does not. This determines whether SQLite is urgent or merely correct.
5. **Metric cardinality.** `node_id` is bounded today (hundreds), but if the design moves to
   per-flow labels the series count explodes; the rule must be written down before instrumentation
   (C20).
6. **Resin upgrade cadence.** The M1 spike pinned `:1.3.0` by digest; no policy exists for
   evaluating a 1.4.x lease or subscription change. `[UNVERIFIED]`

## Gap loop

**Round 1 — targeted gaps (2026-10-23).**
Gaps: (a) is `modernc.org/sqlite` maintained and current? (b) can a Windows service use the user
keyring? (c) does Resin's DESIGN document support the single-binary/SQLite claim? (d) does OTel
require a collector?
Searches: `modernc.org/sqlite production cgo alternative` (found pkg.go.dev v1.61.0, S05);
`Windows Credential Manager service LocalSystem` (found Microsoft LocalSystem doc, S12 — indirect);
fetched Resin `DESIGN.md` (S02); `OpenTelemetry Go SDK vs Prometheus single binary` (found OTel
exporters page, S16). New sources: S02, S05, S12, S16. New claims: C08, C12, C19, C22.
Result: three of four gaps narrowed; (b) remains open.

**Round 2 — falsification (2026-10-23).**
Gaps: does SQLite WAL have a hidden cost on small databases that would make the 30 s JSON snapshot
preferable? Search: `SQLite WAL mode overhead small database single writer benchmark`. Result: only
the SQLite WAL page already held (S03) and a third-party benchmark repository with no applicable
number. No new source, no new claim: **empty round**.

**Round 3 — second falsification (2026-10-23).**
Gaps: is there a primary source quantifying `modernc.org/sqlite` binary size or a known Windows
build failure? Search produced release pages only (already S05); no primary number. No new source,
no new claim: **empty round**.

**Stop reason:** two consecutive empty rounds, per the budget (max 2 gap rounds after the first
targeted round). Unresolved gaps are listed as open questions 1, 2 and 3 rather than guessed.
Post-synthesis correction pass: S31 (Resin lease/persistence sections) and S32 (bus factor) were
added while checking the decision table against primary text; C02 was strengthened and one decision
row (strong/weak durability) was added. No verdict changed.

## Verification

Verifier: `verifier` subagent (read-only), 2026-10-23, 2m32s. It was given the central claim, the
file list and the two upstream driver claims, but not this report or its conclusion.
Central claim: *the daemon loses up to one snapshot interval of panel-store mutations on crash
because state is persisted as a whole-store JSON snapshot every `AJS_SNAPSHOT_INTERVAL` (default
30 s) and on shutdown, with no per-mutation save* — the fact the persistence recommendation rests
on.
Method: re-read `daemon/cmd/ajs-accountd/main.go`, `daemon/internal/store/persist.go`,
`daemon/internal/store/store.go` and `daemon/README.md`; enumerate every `Save`/`Snapshot` call
site; trace the mutation handlers; run `go test ./internal/store -count=1`; fetch the two upstream
READMEs.
Verdict: **VERIFIED**, with one scoped qualification.

Evidence the verifier re-derived:
- `persist.go:20-38` defines one `Snapshot` struct containing every collection; `:55-100` marshals
  the whole struct; `:147` calls `Snapshot`, `:155` writes a temp file, `:180` renames it.
- `main.go:180` `const defaultSnapshotInterval = 30 * time.Second`; `main.go:88` starts the ticker;
  `main.go:93` and `:104` are the only two `saveState` call sites (`"interval"`, `"shutdown"`);
  `main.go:212` is the only `st.Save`.
- No `Save`/`Snapshot` reference exists in `internal/api`, `internal/runtime`, `internal/account`
  or `internal/supervisor`; every mutation handler locks, mutates slices, unlocks.
- `daemon/README.md:605-608`, `:618-620` and `:1466-1468` state the interval, the whole-document
  rewrite and the crash-loss window verbatim.
- `go test ./internal/store -count=1` → `ok ajsaccountd/internal/store 0.048s`.
- Driver facts: `modernc.org/sqlite` README line 1 "Pure-Go SQLite, no cgo", and its `sqlite.go`
  imports `modernc.org/libc` with no `import "C"`; `mattn/go-sqlite3` README requires
  `CGO_ENABLED=1` and gcc. The daemon is not currently dependent on either (`daemon/go.mod`), and
  `daemon/Dockerfile:21` builds with `CGO_ENABLED=0` (now C43).

Qualification: `secret.Store.Seal` rewrites `secrets.json` synchronously on every seal, so the
password blob can survive a crash while the account row does not. The 30 s loss window therefore
applies to panel rows and can leave an orphan sealed blob — two files with two durability rules.
That strengthens, rather than weakens, the SQLite recommendation (one transactional store).
Claims not covered by the verifier remain graded in `claims.tsv`; the two B-grade claims (C17,
C19) and open questions 1–3 stay `[UNVERIFIED]` in this report.

## Counts

- Sources: **33** rows in `sources.tsv` (S01–S33), each with a locator and a quote; 24 primary web
  documents, 5 primary local (Go 1.26.7 docs and the M1 experiment), 4 local project documents.
  Dated: every row carries a read date; publication dates where visible.
- Claims: **44** rows in `claims.tsv` (C01–C44): 42 grade A, 2 grade B (C17 age, C19 LocalSystem),
  0 grade C. Contradiction kept: C04 vs the 15–30 s cold-path estimate in
  `research/70-panel/REPORT.md`.
- Decision rows: 31 across the five areas.
- Code changes: 5, each with file:line and an expected test.
