# Code review — ajs-accountd daemon (`daemon/`)

Date: 2026-10-23 · Slice: AJS-50 · Branch `panel-design` @ `b75c1c3` · Read-only review; no code, test, deploy or git change was made.

## Verdict

**12 defects: 1 high, 5 medium, 6 low. No release blocker in the code path that `main` actually wires, and no auth bypass, credential leak over HTTP, path traversal or data-corruption bug was found.** The build is green: `go vet ./...` clean, `go test -race -count=1 ./...` passes for all 10 packages, `gofmt -l .` empty, and a `-count=3` race run of the four concurrency-heavy packages passes.

The three findings that matter most:

1. **`supervisor.Start` hands the daemon's entire environment to every child** (`supervisor.go:272`), and that environment carries `AJS_MASTER_KEY`, `AJS_ADMIN_TOKEN` and `AJS_RESIN_ADMIN_TOKEN`. The package doc claims "the supervisor never holds credentials". Nothing calls the supervisor outside its own tests yet, so this is latent — it becomes live the moment the package is wired, which is its stated purpose.
2. **`paginate` overflows on `?limit=9223372036854775807`** (`server.go:489`) and the six list handlers then slice `[start:end]` with a negative `end`, panicking the handler. Verified arithmetic, reachable with the admin token.
3. **Rotation's filter read-modify-write is unsynchronised** (`publisher.go:400-418`) while `reconcileLocked` re-PATCHes the same filter list (`panel.go:332`). A drain or reconcile during a rotation can delete the exclusion filter that makes rotation work — the exact M1 failure the exclusion exists to prevent.

## Commands run

| # | Command (cwd `daemon/`) | Result |
|---|---|---|
| 1 | `go version` | `go1.26.7 windows/amd64` |
| 2 | `go vet ./...` | exit 0, no diagnostics |
| 3 | `go test -race -count=1 ./...` | all ok: account 1.11s, api 1.17s, gate 1.54s, publisher 1.08s, resin 1.26s, runtime 1.16s, secret 1.06s, store 1.11s, supervisor 1.90s; `cmd/ajs-accountd` and `internal/model` have no test files |
| 4 | `go test -race -count=3 ./internal/gate ./internal/publisher ./internal/store ./internal/runtime` | all ok (repeat-run stability) |
| 5 | `gofmt -l .` | empty |
| 6 | `staticcheck` | **not installed** (`$GOPATH/bin` has ffuf/gobuster/gopls/nuclei/pd-httpx/subfinder only) → `[UNVERIFIED]` |
| 7 | standalone arithmetic probe for `paginate` (outside the repo) | reproduces `panic: slice bounds out of range [:-9223372036854775797]` — see Evidence |

## Findings

| # | Severity | File:line | Issue | Impact / repro | Suggested fix | Test gap |
|---|---|---|---|---|---|---|
| F1 | **high (P1)** | `internal/supervisor/supervisor.go:272` | `cmd.Env = append(os.Environ(), spec.Env...)` passes the daemon's whole environment to every node child | Any child can read `AJS_MASTER_KEY` (decrypts every account password), `AJS_ADMIN_TOKEN` (full panel control) and `AJS_RESIN_ADMIN_TOKEN`. Contradicts the package doc ("The supervisor never holds credentials"). No production caller yet; live as soon as it is wired | Build a minimal child env (`PATH`, `SystemRoot`/`HOME`, `TEMP`/`TMPDIR`) plus `spec.Env`; never `os.Environ()` | No test inspects the child's environment |
| F2 | medium (P2) | `internal/api/server.go:489-490` | `offset+limit` overflows `int`; `end` becomes negative and `filtered[start:end]` panics | `GET /v1/accounts?limit=9223372036854775807&offset=7` (also nodes/leases/users/apikeys/requests). Handler panics; net/http recovers per-connection, so the client sees a reset instead of JSON and `logRequests` never logs the request. Arithmetic verified; the reset is `[INFERENCE]` from net/http's documented panic recovery | Clamp before adding: `if limit > total-offset { limit = total-offset }; end = offset+limit` | `TestPagination` covers `limit=2`, `limit=-1`, `offset=99`; not the overflow |
| F3 | medium (P2) | `internal/publisher/publisher.go:400-418` + `internal/runtime/panel.go:219-233,332` | Rotation does an unlocked GetPlatform → PATCH(+exclusion) → DeleteLease → PATCH(restore); `Reconcile` independently PATCHes `["<sub>/.*"]` over the same field | Two rotations for different accounts (allowed: the rate limit is per account) interleave; A restores `original` and drops B's exclusion before B deletes, so Resin re-picks the same node. A `DrainNode`/`Reconcile` during one rotation resets the filters mid-flight. `RotateLease` does not take `opMu`, so nothing serialises it against `Reconcile` | Serialise the whole read-modify-write: a publisher-level `rotateMu`, and have `ResinPanel.RotateLease` hold `opMu` so `reconcileLocked`'s `EnsurePlatform` cannot run mid-rotation | No concurrency test exists in `publisher` or `runtime` (`grep go func/sync.WaitGroup` in both test files: zero) |
| F4 | medium (P2) | `internal/gate/server.go:381-388` | The accept loop returns on any error that is not `net.ErrClosed` (EMFILE, ECONNABORTED, …) | The listener socket stays bound, so the port still completes TCP handshakes from the backlog and a supervisor readiness dial still succeeds, while no connection is ever served; `State()` reports `Active=0, Draining=false`, so nothing looks wrong. Gate is dead until the process restarts | Log, sleep a short fixed backoff (e.g. 50 ms) and `continue`; only `net.ErrClosed` ends the loop | No test injects an Accept error |
| F5 | medium (P2) | `internal/store/store.go:672-687` (+ `users_keys.go:319`) | Audit and request history are unbounded and every insert is a full copy: `append([]T{ev}, s.audit...)` under the write lock | At 1 mutation/s, 86 400 events/day × ~200 B ≈ 17 MB/day of retained memory, and the Nth insert copies the whole slice while holding `s.mu` (≈17 MB copy at day 1, ≈500 MB at day 30). Every API read blocks behind that copy. The 30 s snapshot (`persist.go:55`) also copies and JSON-encodes the whole history | Cap both collections (ring buffer of N, drop oldest) and append into a fixed-capacity ring so inserts are O(1); snapshot only the retained window | No retention test; `TestSnapshotRoundtrip` uses the seed sizes only |
| F6 | medium (P2) | `internal/store/store.go:299-345` | `CreateAccount` holds the store **write** lock across `sealer.Seal` → temp file + `Write` + `Sync` + `Rename` | A slow disk (fsync) blocks every `Accounts`/`Nodes`/`Settings` reader for the duration; the same file is rewritten on every account creation. Inconsistent with `Snapshot`'s explicit "slow disk write never blocks the API" design. Related: `AccountSecret` (`store.go:152-170`) unlocks *before* `sealer.Open`, so a concurrent delete can return a password for a deleted account | Allocate the id and advance `accountSeq` under the lock, release it, call `Seal`, re-acquire and insert (re-checking the label conflict); or reserve the label in a pending map | No test asserts lock hold time; `TestCreateAccountSealFailure` covers the error path only |
| F7 | low (P3) | `internal/publisher/publisher.go:178,200` | Doc says "one line per node, **sorted by tag**"; the code sorts the formatted lines, so ordering is by gate port (the tag is the last field) | Subscription text ordering does not match the documented contract; `TestRenderGolden` passes only because its fixture's ports ascend with its tags. No functional effect on Resin (content is compared as a whole) | Sort a copy of `nodes` by `Tag` before formatting and drop `sort.Strings(lines)` | Golden test masks it; add a fixture where port order ≠ tag order |
| F8 | low (P3) | `internal/store/store.go:704-707` | `cloneUser` deep-copies `Allowlist` but not `QuotaBytesMonth`, so `Users()`/`User()` snapshots alias the store's `*int64` | Any caller writing `*user.QuotaBytesMonth` mutates store state without the mutex (data race). Today handlers only read, so it is latent, but it violates the "snapshot" contract the function documents and that `cloneAccount` honours for `Regions` | `if user.QuotaBytesMonth != nil { v := *user.QuotaBytesMonth; user.QuotaBytesMonth = &v }`; also copy the input pointer in `CreateUser`/`UpdateUser` | No aliasing test |
| F9 | low (P3) | `internal/resin/client.go:545` | Success bodies are decoded with no size cap; only error bodies are limited (`maxErrorBodyBytes`) | A misbehaving Resin (loopback, but reachable by anything holding the admin token) can stream an unbounded JSON document into `Decode`, growing memory without limit. `ListNodes`/`findPlatform` additionally accumulate every page | Wrap with `io.LimitReader(resp.Body, maxResponseBodyBytes)` (e.g. 8 MiB) and reject a truncated decode | No oversized-body test |
| F10 | low (P3) | `cmd/ajs-accountd/main.go:140-144` | `http.Server` sets `ReadHeaderTimeout` and `IdleTimeout` but no `ReadTimeout`/`WriteTimeout` | A client can trickle a 1 MiB body for hours (`MaxBytesReader` bounds bytes, not time), holding a goroutine and a connection per request. Default listen is loopback, so exposure depends on `AJS_LISTEN` | `ReadTimeout: 15s` plus a `WriteTimeout` above the worst-case handler (a reconcile is ~30 s of Resin calls + refresh backoff), e.g. 120 s | No test |
| F11 | low (P3) | `internal/api/ops.go:99-110` vs `internal/api/accounts.go:40,53` | `PATCH /v1/settings` rejects unknown fields; every other body decodes with a plain `json.Decoder`, so unknown fields are silently ignored | A typo (`"concurency": 8`) is accepted and the account is created with the default 32; the operator gets 201 and the wrong value. Same class for accounts/users/mutations | Call `dec.DisallowUnknownFields()` in `decodeJSON`, or apply the same allowlist pattern everywhere | No unknown-field test for any non-settings endpoint |
| F12 | low (P3) | `internal/secret/store.go:147-158` | `Rotate` decrypts every entry into a fresh `plaintext` slice and never zeroes it; the old key's plaintext stays in the heap until GC | Credentials linger in process memory after a key rotation, in a package whose whole purpose is minimising plaintext lifetime (`LoginService` clears its copy at `login_service.go:163`) | `clear(plaintext)` after `sealWith`, including on the error return | No test |

## Top 5 (actionable)

### 1. Do not inherit the daemon environment into node children — `internal/supervisor/supervisor.go:272`

```go
cmd.Env = append(os.Environ(), spec.Env...)
```

Replace with an explicit allowlist. `os.Environ()` here carries `AJS_MASTER_KEY` (`main.go:223`), `AJS_ADMIN_TOKEN` (`main.go:38`) and `AJS_RESIN_ADMIN_TOKEN` (`main.go:43`), so a child can decrypt the secret store and drive the panel. Fix: build the base env from `PATH`, `HOME`/`SystemRoot`, `TMPDIR`/`TEMP`/`TMP` and nothing else, then append `spec.Env`. A test can start the helper child with `spec.Env = []string{"PROBE=1"}` and have it print `os.Getenv("AJS_MASTER_KEY")`, asserting empty.

### 2. Clamp the page window before adding — `internal/api/server.go:489-490`

```go
if offset > total {
    offset = total
}
if limit > 0 && limit > total-offset {
    limit = total - offset
}
end := offset + limit
```

`offset ≤ total`, so `total-offset` cannot be negative and `offset+limit ≤ total` cannot overflow. Repro: `curl -H "Authorization: Bearer $TOKEN" 'http://127.0.0.1:2261/v1/accounts?limit=9223372036854775807&offset=7'` — the handler panics (verified arithmetic in Evidence). Add the case to `TestPagination`.

### 3. Serialise the rotation filter read-modify-write — `internal/publisher/publisher.go:400-418` and `internal/runtime/panel.go:219`

`Rotate` reads `platform.RegexFilters`, PATCHes an exclusion, deletes the lease, then PATCHes `original` back. Nothing prevents a second rotation (different account — the rate limit is per account) or a `Reconcile`/`DrainNode` from writing the same field in between; `ResinPanel.RotateLease` never takes `opMu`, and `reconcileLocked`'s `EnsurePlatform` (`panel.go:332`) actively converges the filters back to `["<sub>/.*"]`. Fix: a `rotateMu sync.Mutex` held across the whole sequence in `Rotate`, and have `RotateLease` hold `opMu` (rotations are already rate-limited to one per 10 s per account, so serialising them against reconcile is cheap). Test: two goroutines rotating two accounts against a fake client that asserts the exclusion is present at `DeleteLease` time.

### 4. Keep the gate accept loop alive — `internal/gate/server.go:381-388`

```go
conn, err := ln.Accept()
if err != nil {
    if errors.Is(err, net.ErrClosed) {
        return
    }
    s.logger.Warn("gate accept failed", "node_id", s.cfg.NodeID, "error", err)
    time.Sleep(50 * time.Millisecond)
    continue
}
```

Today one EMFILE/ECONNABORTED permanently stops the node's gate while the socket stays bound and `State()` looks healthy. A readiness probe that dials the port still succeeds because the kernel completes the handshake into the backlog. Test: wrap the listener to return one non-fatal error, then assert the next connection is still served.

### 5. Bound the audit and request history — `internal/store/store.go:672-687`

Every mutation prepends with `append([]model.AuditEvent{event}, s.audit...)`, a full copy under the write lock, and nothing trims the slice; `Snapshot` then copies and encodes all of it every 30 s. Fix: a fixed-capacity ring (or a cap plus `copy`-down) with an explicit retention constant, O(1) insert, and a `TestAuditRetention` that appends N+K events and asserts the newest N are kept and the oldest dropped. Same treatment for `s.requests` (`users_keys.go:319`).

## Per-package coverage

| Package | Findings | What was checked | Clean notes |
|---|---|---|---|
| `internal/api` | F2, F11 | Auth (bearer, constant-time compare, `/v1/` prefix gate), CORS (exact-origin match, preflight before auth, `Vary`), body limit + 413 mapping, router 404/405 split, error mapping, every handler body | No auth bypass: all routes are `/v1/*` except `/healthz`; the router matches `r.URL.Path` exactly, so `//v1/…`, `/v1/../…` and `%2F` do not match any route. No secret in any response body. `TestLoggingRedactsAuthorization` is honest — headers are never logged |
| `internal/store` | F5, F6, F8 | Every exported method, locking, in-place filters, counters, denormalisation, audit, snapshot/restore/validate/normalize | In-place filters (`s.nodes[:0]`, `s.leases[:0]`) are safe: snapshots copy, `Node`/`Lease` return values, `ProxyNode`/`Lease` hold no slices. `Restore` validates before taking the lock and rebuilds counters |
| `internal/account` | none | control, device, protocol, secret, session, login service | `Secret` redacts in `%v`/`%#v`/JSON/slog; `Login` never logs the password, clears the plaintext copy, and only advances hosts on transport errors; `remember` closes the replaced control; `Result` deep-copies the Servers map |
| `internal/resin` | F9 | URL validation (scheme, embedded credentials), token handling, pagination termination, error envelope, timeout | `url.PathEscape` on every id; bearer token never in an error or log; `DeleteLease` treats 404 as success |
| `internal/publisher` | F3, F7 | Render/tag validation, coalescing, refresh retries, rotation, lease drops, probe config | `Render` rejects duplicates/bad tags/bad ports; empty content is refused; `Publish` holds `publishMu` so content cannot interleave |
| `internal/runtime` | F3 | Startup order, topology, four mutations, error mapping | `opMu` correctly serialises `Start`/`Reconcile`; `Topology` never does I/O; partial-failure drain keeps the store transition |
| `internal/gate` | F4 | SOCKS5 parse (all ATYPs, replies per RFC 1928), admission, counters, drain/undrain, relay, close | Counters are atomic and balanced; `finishFlow` closes both sides exactly once; admission slot released exactly once; no goroutine leak observed in `TestCloseCancelsBlockedOpenFlow` |
| `internal/supervisor` | F1 | Start/readiness/stop/signal/close, event buffer, stderr ring | Duplicate-Start is race-free (`reserved` and `nodes` are mutated under one lock); `Close` waits for each `stopHandle`, and every emit happens before `done` closes |
| `internal/secret` | F12 | AES-256-GCM, AAD binding, file mode, atomic persist, rotation rollback | AAD binds the account id; tamper/wrong-id tests pass; rotation is all-or-nothing (old AEAD restored if persist fails) |
| `cmd/ajs-accountd` | F10 | env/flags, startup order, snapshot loop, shutdown order | Defer order is correct (final `saveState` runs before `secrets.Close`); a corrupt snapshot fails startup instead of serving half-restored state; master key never logged |
| `internal/model` | none | Wire types | Pure types; no logic to defect |

## Evidence: paginate overflow

`internal/api/server.go:466-493` computes the window as:

```go
if offset > total { offset = total }
end := total
if limit > 0 && offset+limit < total { end = offset + limit }
```

`limit` is only validated as a non-negative `int` (`strconv.Atoi`, 64-bit), so `offset+limit` overflows. Standalone reproduction of the exact arithmetic (run outside the repo; `internal/` cannot be imported from a scratch module):

```
Atoi(MaxInt64): 9223372036854775807 <nil>
start=12 end=-9223372036854775797 (end<start=true)
PANIC: runtime error: slice bounds out of range [:-9223372036854775797]
```

Callers that then slice: `handleListAccounts` (`accounts.go:26`), `handleListNodes` (`nodes_leases.go:36`), `handleListLeases` (`nodes_leases.go:103`), `handleListUsers` (`users_keys.go:17`), `handleListKeys` (`users_keys.go:129`), `handleListRequests` (`ops.go:58`). `handleAudit` is not affected: it clamps to 500 and calls `store.Audit`.

## Not verified / limits

- `staticcheck` is not installed; no run. `[UNVERIFIED]`
- No end-to-end HTTP reproduction of F2: `ajsaccountd/internal/api` cannot be imported from outside the module, and the brief forbids adding files to the repo. The arithmetic is executed; the connection-reset outcome is `[INFERENCE]` from `net/http`'s per-connection panic recovery.
- No fuzzing (`go test -fuzz`) and no `-race` run against a live Resin; the Resin and vendor paths are exercised only through the in-repo fakes.
- `emulator/` (`ajsproxy`) and `dashboard/` are outside this slice; findings that depend on `apifacade`/`fvfacade` behaviour are not assessed.
- F1 is latent: `grep -rn "supervisor\." --include=*.go` outside the package returns only a comment, so no production caller exists today.
- Estimates in F5 are arithmetic (events/day × bytes/event, and full-slice copy per insert), not measurements.

## debugger-review checklist

1. **Artifact/evidence** — every finding cites `file:line`; every command in the table was executed in this session. No claim rests on "it worked here".
2. **Reproducibility** — the toolchain (`go1.26.7 windows/amd64`), branch and commit are recorded; the F2 reproduction is a two-parameter URL plus a self-contained arithmetic snippet; F4/F3 have deterministic fake-client recipes.
3. **High-risk rollback** — nothing was written except this report; no credentials, no live vendor call, no driver, no service change. Rollback is `rm -r research/79-code-review`.
4. **Exit/handoff truth** — the board item stays open until the author decides on F1–F6; nothing here was applied, so no state claim is inflated.

## Handoff

One line: fix F1 before wiring `supervisor` into `main`, then F2 (clamp) and F3 (serialise rotation); F4–F6 are the next cycle.
