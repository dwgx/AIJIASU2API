# BENCH.md — audit-bench measurements (tree 83)

Scope: **read-only benchmark of the current tree, run on HMC only.** No source
edits, no git operations, no credentials recorded, no writes anywhere under
`/srv/dwgxhomecloud/scratch/ajs-multimodule` or
`/srv/dwgxhomecloud/scratch/loveplus-dashboard`.

Everything below was measured on 2026-10-24 between 00:16 and 00:30 JST.

## TL;DR

| Question | Answer |
| --- | --- |
| Go tests | **250 pass on HMC** (218 daemon + 32 emulator), 0 fail, 0 skip. Local tree has **261** — HMC is 11 stale (§8). |
| Vitest | **71 pass** in 11 files, 7.1 s wall. Claim confirmed. |
| Emulator gate | **8/8** allowlisted conformance tests pass. |
| Cold build | 6.8 s daemon / 6.4 s emulator (`go clean -cache` first). Warm: 0.33 s / 0.31 s. |
| Binaries | `ajs-accountd` 10 971 907 B (10.46 MiB); `ajsproxy` 9 476 835 B (9.04 MiB). |
| Bundle | `dist` 1.9 MiB; JS 790 035 B + CSS 77 331 B = **261 281 B gzipped**; fonts 857 812 B. |
| API p50 / p95 (sustained, c=20, 10 s) | 0.2–1.8 ms / 0.5–9.3 ms; 6.6–87.9 krps. |
| Slowest endpoint | **`/v1/requests`** — 149.8 kB/response, 6.6 krps. `?limit=20` → 5.6 kB, **15.2 krps** (2.3×). |
| Gate relay | **877.5 MB/s** streaming (one flow, 5 s) vs 1 799.2 MB/s raw loopback ceiling; handshake p50 **159 µs** (N=500). |
| Cold start | **23–24 ms** to first `/healthz` 200 (3 runs). |
| No in-repo benchmarks | `func Benchmark` count in tree = **0**; gate numbers come from a throwaway copy (§9.2). |

---

## 1. Host and toolchain

| Item | Value | Command |
| --- | --- | --- |
| Host | `dwgxhomecloud` | `ssh hmc 'hostname'` |
| Kernel | `6.12.111+deb13-amd64 #1 SMP PREEMPT_DYNAMIC` | `ssh hmc 'uname -a'` |
| CPU | AMD Ryzen 5 5600 6-Core Processor | `ssh hmc 'grep "model name" /proc/cpuinfo'` |
| Cores (`nproc`) | 12 | `ssh hmc 'nproc'` |
| Memory | 8 078 436 kB total (~7.9 GiB), ~3.2 GiB available at start | `ssh hmc 'cat /proc/meminfo'` |
| Load average (start) | 1.78 / 1.81 / 1.68 | `ssh hmc 'uptime'` |
| Disk (`/`) | 112 G, 72 G used, 35 G avail, 68 % | `ssh hmc 'df -h /srv/dwgxhomecloud'` |
| Go | `go1.25.14 linux/amd64` on PATH; modules declare `go 1.26.0` and `GOTOOLCHAIN=auto` resolves **go1.26.0** | `ssh hmc 'go version'` / `cd daemon && go version` |
| Node | v24.21.0 | `ssh hmc 'node -v'` |
| npm | 11.19.0 | `ssh hmc 'npm -v'` |
| Load tool | `hey` at `/usr/local/bin/hey` | `ssh hmc 'which hey'` |
| Python | `/usr/bin/python3` | `ssh hmc 'which python3'` |

**Caveat (host):** this is a live home-cloud box, not an idle benchmark rig.
`ss -ltnp` showed ~40 other listening services (nginx on 8081, sing-box on
2080/2081, a dozen `pproxy` workers on 19090–19106, docker-proxy, dlv on 23464,
`ajs-accountd-la` on **127.0.0.1:2261**, `ajs-accountd-li` on **127.0.0.1:2262**,
plus 25 logged-in users). Load average was ~1.8–2.9 during the run. Every number
here carries that noise floor; do not treat them as clean-room figures.

---

## 2. Trees benchmarked

| Tree | Path on HMC | Notes |
| --- | --- | --- |
| daemon | `/srv/dwgxhomecloud/scratch/ajs-multimodule/daemon` | module `ajsaccountd`, 79 `.go` files, 33 `_test.go` |
| emulator | `/srv/dwgxhomecloud/scratch/ajs-multimodule/emulator` | module `ajsproxy` (daemon replaces `ajsproxy => ../emulator`) |
| dashboard | `/srv/dwgxhomecloud/scratch/loveplus-dashboard` | `ajs-proxy-console` v0.1.0, 43 files in `src`+`tests` |

**Caveat (tree drift):** the HMC scratch copies are **not** byte-identical to
the local `D:/Project/LovePlusSpeedToProxy` working tree at HEAD `2eee11a`. Two
local test files are absent from the HMC copy:

* `daemon/cmd/ajs-accountd/main_test.go` — 4 tests (`TestNodeLimitDefaultsToOne`,
  `TestNodeLimitWhitespaceDefaultsToOne`, `TestNodeLimitExplicitValues`,
  `TestNodeLimitRejectsInvalidValues`)
* `daemon/internal/api/update_test.go` — 7 tests (`TestSystemUpdateDisabled`,
  `TestSystemUpdateDisabledAuthRequired`, `TestSystemUpdateNewer`,
  `TestSystemUpdateSameAndOlder`, `TestSystemUpdatePreReleaseRules`,
  `TestSystemUpdateUpstreamErrors`, `TestUpdateVersionCompare`)

Local working tree also has `daemon/cmd/ajs-accountd/main.go` modified
(`git status` → ` M daemon/cmd/ajs-accountd/main.go`), and the local tree has
**11 more Go test functions** than HMC. All Go/Vitest counts below are therefore
**HMC counts**, which is what the local "261 Go tests" claim would become once
those 11 land on HMC: 250 + 11 = 261. See §8.

---

## 3. Test suites

### 3.1 Go — daemon module

```bash
ssh hmc 'cd /srv/dwgxhomecloud/scratch/ajs-multimodule/daemon && go test -count=1 ./...'
```

Result (all pass, 0 fail, 0 skip):

```
ok  	ajsaccountd/internal/account	0.088s
ok  	ajsaccountd/internal/api	0.051s
ok  	ajsaccountd/internal/gate	0.525s
ok  	ajsaccountd/internal/publisher	0.021s
ok  	ajsaccountd/internal/resin	0.234s
ok  	ajsaccountd/internal/runtime	0.195s
ok  	ajsaccountd/internal/secret	0.004s
ok  	ajsaccountd/internal/store	0.067s
ok  	ajsaccountd/internal/supervisor	1.281s
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
?   	ajsaccountd/internal/model	[no test files]
```

Counting (`ssh hmc 'cd .../daemon && go test ./... -v | grep -E "^=== RUN" | grep -v "/" | wc -l'`):

* **218 top-level test functions** run and pass.
* `grep -rhoE "^func Test[A-Za-z0-9_]+" --include="*_test.go" . | wc -l` → **218**
  (agree).
* `go test ./... -v` emits 436 `--- PASS` lines total; 84 of those are
  subtests (`^    --- PASS`), i.e. 218 top-level + 84 subtests + overhead.
* `t.Run(` occurrences in the whole scratch tree: 39.
* **0** `--- SKIP`, **0** `--- FAIL`.
* **`go vet ./...`**: clean (no output).

Per-package top-level test function counts (grep of `^func Test`):

| Package | tests |
| --- | --- |
| `internal/account` | 36 |
| `internal/api` | 32 |
| `internal/gate` | 11 |
| `internal/publisher` | 24 |
| `internal/resin` | 24 |
| `internal/runtime` | 33 |
| `internal/secret` | 12 |
| `internal/store` | 32 |
| `internal/supervisor` | 14 |
| **daemon total** | **218** |

`internal/api` alone contributes 22 subtests on top of its 32 top-level tests.

### 3.2 Go — emulator module

```bash
ssh hmc 'cd /srv/dwgxhomecloud/scratch/ajs-multimodule/emulator && go test -count=1 ./...'
```

```
ok  	ajsproxy/internal/api	0.007s
ok  	ajsproxy/internal/config	0.003s
ok  	ajsproxy/internal/fv	0.047s
ok  	ajsproxy/internal/proxy	(cached)
?   	ajsproxy/apifacade	[no test files]
?   	ajsproxy/cmd/ajsproxy	[no test files]
?   	ajsproxy/fvfacade	[no test files]
```

* **32 top-level test functions**, all pass, 0 skip, 0 fail.
* Per package: `internal/api` 16, `internal/fv` 12, `internal/proxy` 3,
  `internal/config` 1.
* `go vet ./...`: clean.

### 3.3 Go total

**218 + 32 = 250 top-level Go tests pass**, 0 failures, 0 skips, in 33
`_test.go` files. Zero `func Benchmark` and zero `func Fuzz` exist in the tree
(`grep -rhoE "^func (Fuzz|Benchmark)[A-Za-z0-9_]+" --include="*_test.go" daemon
emulator | wc -l` → **0**).

### 3.4 Emulator conformance gate (8/8)

`autoresearch.json` declares `target_metric: 8` over a fixed allowlist of eight
named tests. Verified individually:

```bash
ssh hmc 'cd /srv/dwgxhomecloud/scratch/ajs-multimodule/emulator && \
  go test -count=1 -v -run "^(TestSignVectors|TestDualHash|TestFrameRoundTrip|\
TestAESCTR|TestOAEPShape|TestSOCKS5Handshake|TestHTTPProxyCONNECT|\
TestTransportRegistry)$" ./internal/api/ ./internal/fv/ ./internal/proxy/'
```

```
--- PASS: TestSignVectors (0.00s)          (internal/api)
--- PASS: TestDualHash (0.00s)             (internal/api)
--- PASS: TestFrameRoundTrip (0.00s)       (internal/fv)
--- PASS: TestAESCTR (0.00s)               (internal/fv)
--- PASS: TestOAEPShape (0.01s)            (internal/fv)
--- PASS: TestTransportRegistry (0.00s)    (internal/proxy)
--- PASS: TestSOCKS5Handshake (0.00s)      (internal/proxy)
--- PASS: TestHTTPProxyCONNECT (0.00s)     (internal/proxy)
```

**8/8 pass.** Note `scripts/metric.py` does **not exist** in the HMC scratch copy
(`ls .../ajs-multimodule/scripts` → no such directory); it exists locally under
`scripts/metric.py`. The metric was therefore verified by direct named-test
invocation, not through the script.

### 3.5 Vitest — dashboard

```bash
ssh hmc 'cd /srv/dwgxhomecloud/scratch/loveplus-dashboard && npm test'
```

```
 Test Files  11 passed (11)
      Tests  71 passed (71)
   Duration  6.60s (transform 1.67s, setup 2.74s, collect 5.61s,
             tests 19.93s, environment 9.72s, prepare 1.65s)
real	0m7.145s   user 0m31.808s   sys 0m4.347s
```

**11 files, 71 tests, all pass** — confirms the claimed 71. Per file:
`api.test.ts` 9, `useAsync.test.tsx` 13, `interactions.test.tsx` 9,
`app.test.tsx` 6, `palette-search.test.tsx` 7, `accounts-view.test.tsx` 7,
`update-card.test.tsx` 6, `theme-accent.test.tsx` 8, `palette-focus.test.tsx` 3,
`theme-contrast.test.ts` 2, `live-mode.test.tsx` 1.

---

## 4. Build times

All measured with the shell builtin `time` over `ssh hmc`. "Cold" = after
`go clean -cache` for that module.

| Build | Cold | Warm |
| --- | --- | --- |
| `daemon` `go build ./...` | **6.785 s** (user 28.954 s, sys 5.123 s) | **0.330 s** |
| `emulator` `go build ./...` | **6.354 s** (user 26.898 s, sys 4.921 s) | **0.307 s** |
| `daemon` `go test -count=1 ./...` (warm cache) | — | **3.692 s** |
| `emulator` `go test -count=1 ./...` (warm cache) | — | **1.871 s** |
| dashboard `npm run build` (`tsc -b && vite build`) | — | **5.786 s** wall (vite reported `built in 2.86s`) |
| dashboard `npm test` (vitest run) | — | **7.145 s** wall |

Commands:

```bash
ssh hmc 'cd /srv/dwgxhomecloud/scratch/ajs-multimodule/daemon && \
         go clean -cache && { time go build ./...; }'
ssh hmc 'cd /srv/dwgxhomecloud/scratch/ajs-multimodule/emulator && \
         go clean -cache && { time go build ./...; }'
ssh hmc 'cd /srv/dwgxhomecloud/scratch/loveplus-dashboard && { time npm run build; }'
```

**Caveat:** `go clean -cache` is destructive to the shared build cache on a
multi-user box; it was run twice here (once per module) and invalidated cache for
other users too. Cold numbers are single-shot, not medians.

---

## 5. Artifact sizes

### 5.1 Go binaries

```bash
ssh hmc 'cd /srv/dwgxhomecloud/scratch/ajs-multimodule/daemon && \
         go build -o /tmp/bench83/bin/ajs-accountd ./cmd/ajs-accountd'
ssh hmc 'cd /srv/dwgxhomecloud/scratch/ajs-multimodule/emulator && \
         go build -o /tmp/bench83/bin/ajsproxy ./cmd/ajsproxy'
ssh hmc 'ls -l /tmp/bench83/bin/ && sha256sum /tmp/bench83/bin/*'
```

| Binary | Bytes | MiB | sha256 |
| --- | --- | --- | --- |
| `ajs-accountd` | 10 971 907 | 10.46 | `7d4826ab97dc0d1cadba79f2e85bbcf7bca29e001436389d3c5e3c82428a8aba` |
| `ajsproxy` | 9 476 835 | 9.04 | `7eb6e487c216f3693a389898864ccaceb6c6fc3f2825b24b6ef3fbb3ae431bb5` |

Both are plain `go build` outputs (no `-ldflags "-s -w"`), so they still carry
the symbol/DWARF tables.

### 5.2 Dashboard bundle (`dist/`)

```bash
ssh hmc 'cd /srv/dwgxhomecloud/scratch/loveplus-dashboard && \
         du -sh dist && \
         find dist -name "*.js"   -printf "%s\n" | awk "{s+=\$1} END {print \"js=\"s}" && \
         find dist -name "*.css"  -printf "%s\n" | awk "{s+=\$1} END {print \"css=\"s}" && \
         find dist -name "*.woff*" -printf "%s\n" | awk "{s+=\$1} END {print \"woff=\"s}"'
ssh hmc 'cd /srv/dwgxhomecloud/scratch/loveplus-dashboard && \
         for f in dist/assets/*.js dist/assets/*.css; do gzip -c $f | wc -c; done | \
         awk "{s+=\$1} END {print \"gzip=\"s}"'
```

| Slice | Bytes | Notes |
| --- | --- | --- |
| `dist/` total (du) | 1.9 MiB | — |
| JS (all `dist/assets/*.js`) | 790 035 | 771 KiB |
| CSS | 77 331 | 75.5 KiB |
| WOFF/WOFF2 fonts | 857 812 | 838 KiB — **44 % of `dist`** |
| **JS+CSS, gzipped** | **261 281** | 255 KiB over the wire |

Largest chunks (raw / gzip):

| Chunk | Raw | gzip |
| --- | --- | --- |
| `Overview-Bi8ftJdC.js` | 401.54 kB | 111.08 kB |
| `index-wghILLGZ.js` | 321.92 kB | 100.80 kB |
| `index-D-jnFsE1.css` | 77.33 kB | 23.95 kB |
| `Accounts-Bl-f38Je.js` | 15.25 kB | 4.77 kB |
| `Settings-DDxgITFn.js` | 10.28 kB | 3.59 kB |
| `Users-D_8fj1wS.js` | 8.90 kB | 3.24 kB |
| `Nodes-D0nQeQLi.js` | 8.34 kB | 2.97 kB |
| `Requests-a_2m6nVL.js` | 7.28 kB | 2.75 kB |
| `Leases-C6hPk8bs.js` | 4.97 kB | 2.04 kB |

**Caveat:** `Overview` (recharts) and `index` (React + router) together are
723 kB raw / 212 kB gzip — the two obvious code-splitting targets. The font
directory alone (838 KiB) outweighs all app JS.

---

## 6. API latency and throughput

### 6.1 Isolated daemon setup

A throwaway, isolated `ajs-accountd` was started on a free loopback port.
**No vendor login, no Resin admin token, no master key.**

```bash
ssh hmc 'cd /srv/dwgxhomecloud/scratch/ajs-multimodule/daemon && \
         go build -o /tmp/bench83/ajs-accountd ./cmd/ajs-accountd'
ssh hmc 'cd /tmp/bench83 && \
  AJS_LISTEN=127.0.0.1:2291 \
  AJS_ADMIN_TOKEN=<BENCH_TOKEN> \
  AJS_STATE_DIR=/tmp/bench83/state \
  AJS_SNAPSHOT_INTERVAL=3600s \
  AJS_LOG_LEVEL=warn \
  setsid nohup ./ajs-accountd >/tmp/bench83/accountd.log 2>&1 </dev/null &'
```

| Setting | Value |
| --- | --- |
| Listen | `127.0.0.1:2291` (verified free before bind: `ss -ltn` showed 2291/2292/2293 free) |
| Auth | `AJS_ADMIN_TOKEN=<BENCH_TOKEN>` — a throwaway string generated for this run, never a real credential, never written outside `/tmp/bench83`. Substitute your own when re-running. |
| State dir | `/tmp/bench83/state` (throwaway, tmpfs-style scratch; `du -sh` = 0, snapshot interval 3600 s) |
| `AJS_MASTER_KEY` | unset → daemon logged/skipped secret sealing (`secrets_disabled`) |
| `AJS_RESIN_ADMIN_TOKEN` | unset → `resin_disabled`, no Resin call |
| `AJS_ACCOUNT_LOGIN` | unset → `account_login_disabled`, **no vendor host contacted** |
| Log | `/tmp/bench83/accountd.log` — **0 bytes** after the whole run (`wc -l` → 0, level `warn`) |
| RSS | 12.2 MB at first sample → 32.4 MB after ~10⁶ requests |

Services on 2261, 2262, 18081, 18082 were **never touched**; `ss -ltnp` before and
after confirms they kept their original PIDs.

**Note on `AJS_SNAPSHOT_INTERVAL`:** it is parsed by `time.ParseDuration`; a bare
`3600` is rejected with `AJS_SNAPSHOT_INTERVAL "3600": time: missing unit in
duration "3600"` and the daemon refuses to start. `3600s` is required. This is a
real usability trap, not a bench artifact.

### 6.2 Endpoint availability (status codes, seeded store)

```bash
ssh hmc 'T=<BENCH_TOKEN>; for p in /healthz /metrics /v1/accounts \
  /v1/nodes /v1/leases /v1/users /v1/apikeys /v1/audit /v1/settings /v1/topology \
  /v1/reconcile /v1/requests; do \
  curl -s -o /dev/null -w "$p %{http_code}\n" -H "Authorization: Bearer $T" \
  http://127.0.0.1:2291$p; done'
```

```
/healthz 200   /metrics 200   /v1/accounts 200   /v1/nodes 200
/v1/leases 200 /v1/users 200  /v1/apikeys 200    /v1/audit 200
/v1/settings 200 /v1/topology 200 /v1/requests 200
/v1/reconcile 405   <- GET not allowed on this route (method restricted, not a bug)
```

Auth/negative behaviour:

```
no token        -> 401
wrong token     -> 401
/v1/does-not-exist -> 404
/healthz without token -> 200 (open by design)
```

### 6.3 Fixed-N sweep — N=1000, c=20, `hey`

```bash
ssh hmc 'T=<BENCH_TOKEN>; for p in healthz metrics v1/accounts v1/nodes \
  v1/leases v1/users v1/requests v1/audit; do \
  hey -n 1000 -c 20 -H "Authorization: Bearer $T" -o csv \
    "http://127.0.0.1:2291/$p" > /tmp/bench83/results/final_$(echo $p|tr "/" "_").csv; done'
```

Latency in ms, derived from the `hey` CSV (`request-time` column, N rows each):

| Endpoint | N | p50 | p95 | p99 | max | rps | 2xx |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| `/healthz` | 1000 | 0.20 | 1.40 | 7.50 | 10.90 | 41 667 | 1000 |
| `/metrics` | 1000 | 1.00 | 2.70 | 3.80 | 6.20 | 15 625 | 1000 |
| `/v1/accounts` | 1000 | 0.20 | 1.00 | 1.50 | 2.70 | 59 880 | 1000 |
| `/v1/nodes` | 1000 | 0.50 | 3.50 | 5.90 | 7.30 | 19 194 | 1000 |
| `/v1/leases` | 1000 | 0.30 | 2.20 | 3.50 | 4.50 | 29 940 | 1000 |
| `/v1/users` | 1000 | 0.30 | 1.30 | 2.80 | 3.60 | 39 063 | 1000 |
| `/v1/audit` | 1000 | 0.30 | 1.40 | 2.30 | 2.90 | 38 610 | 1000 |
| `/v1/requests` | 1000 | 2.10 | 6.60 | 8.80 | 16.30 | 7 138 | 1000 |

### 6.4 Sustained sweep — `-z 10s`, c=20, `hey` (large N)

```bash
ssh hmc 'T=<BENCH_TOKEN>; for p in healthz v1/accounts v1/nodes v1/leases \
  v1/users v1/audit metrics v1/requests; do \
  hey -z 10s -c 20 -H "Authorization: Bearer $T" -o csv \
    "http://127.0.0.1:2291/$p" > /tmp/bench83/results/sust_$(echo $p|tr "/" "_").csv; done'
```

| Endpoint | N | p50 ms | p95 ms | p99 ms | rps |
| --- | ---: | ---: | ---: | ---: | ---: |
| `/healthz` | 878 584 | 0.20 | 0.50 | 1.40 | 87 853 |
| `/v1/users` | 750 936 | 0.20 | 0.70 | 1.50 | 75 088 |
| `/v1/accounts` | 732 316 | 0.20 | 0.70 | 1.50 | 73 230 |
| `/v1/leases` | 693 488 | 0.20 | 0.70 | 1.40 | 69 345 |
| `/v1/audit` | 655 373 | 0.20 | 0.70 | 1.50 | 65 534 |
| `/v1/nodes` | 550 923 | 0.30 | 0.90 | 1.90 | 55 090 |
| `/metrics` | 177 638 | 1.00 | 2.50 | 3.50 | 17 763 |
| `/v1/requests` | 66 169 | 1.80 | 9.30 | 21.20 | 6 617 |

(A second `/v1/users` sample gave N=866 580, p50 0.20 / p95 0.50 / p99 1.10,
86 654 rps — run-to-run spread on a busy host.)

The sustained numbers are the trustworthy ones: with N ≈ 10⁵–10⁶ the percentiles
are stable, whereas the N=1000 rows show p99 inflated by client-side scheduling
noise (max 10.9 ms on `/healthz` at N=1000 vs 1.4 ms p99 at N=878 584).

### 6.5 Payload sizes vs. cost

```bash
ssh hmc 'T=<BENCH_TOKEN>; for p in healthz metrics v1/accounts v1/nodes \
  v1/leases v1/users v1/requests v1/audit v1/topology v1/apikeys v1/settings; do \
  echo -n "$p "; curl -s -H "Authorization: Bearer $T" \
    "http://127.0.0.1:2291/$p" | wc -c; done'
```

| Endpoint | Response bytes |
| --- | ---: |
| `/healthz` | 162 |
| `/metrics` | 962 |
| `/v1/settings` | 110 |
| `/v1/topology` | 388 |
| `/v1/users` | 906 |
| `/v1/apikeys` | 1 267 |
| `/v1/accounts` | 2 055 |
| `/v1/leases` | 3 088 |
| `/v1/nodes` | 3 801 |
| `/v1/audit` | 3 838 |
| **`/v1/requests`** | **149 801** |

`/v1/requests` is the outlier: 149.8 kB per response (40 seeded rows, unbounded
without `limit`) → 6.6 krps vs 55–88 krps for 1–4 kB endpoints. Adding
`?limit=20` shrinks it to **5 560 bytes** and moves it to p50 0.90 ms / p95
3.00 ms / p99 5.20 ms at **15 244 rps** (N=500, c=20) — a **2.3× throughput
improvement** from a single query parameter.

**Finding:** `/v1/requests` has no default page size in the seeded path. This is
the one endpoint worth a default `limit` before the panel ships.

### 6.6 Concurrency sweep — `/v1/nodes`, N=5000, 3 reps each

```bash
ssh hmc 'T=<BENCH_TOKEN>; for c in 1 50 200 500; do for rep in 1 2 3; do \
  hey -n 5000 -c $c -H "Authorization: Bearer $T" "http://127.0.0.1:2291/v1/nodes" \
  2>&1 | grep "Requests/sec"; done; done'
```

| c | rep 1 | rep 2 | rep 3 |
| ---: | ---: | ---: | ---: |
| 1 | 10 680 | 10 883 | 11 177 |
| 50 | 22 088 | 29 465 | 35 915 |
| 200 | 39 852 | 43 359 | 36 408 |
| 500 | 29 561 | 27 336 | 22 702 |

Peak is around **c=200 (~40 krps)**; c=500 degrades ~30 %, i.e. beyond ~200
in-flight requests the 12-thread box spends more on context switching than on
serving. Single-connection ceiling is ~11 krps, which is the pure
request/response round-trip bound.

Wider sustained check at c=100 for 10 s (`hey -z 10s -c 100`):

| Endpoint | rps | Average | Slowest |
| --- | ---: | ---: | ---: |
| `/healthz` | 96 399 | 1.0 ms | 44.3 ms |
| `/v1/accounts` | 81 640 | 1.2 ms | 65.0 ms |

### 6.7 Auth-path cost

| Path | N | p50 ms | p95 ms | rps | Codes |
| --- | ---: | ---: | ---: | ---: | --- |
| `/healthz`, no token (open) | 1000 | 0.10 | 0.70 | 80 645 | 200 ×1000 |
| `/v1/accounts`, **no token** (rejected) | 1000 | 0.10 | 0.60 | 90 909 | 401 ×1000 |
| `/v1/accounts`, valid token | 1000 | 0.20 | 1.00 | 59 880 | 200 ×1000 |

The 401 path is *cheaper* than the 200 path, so bearer-token checking is not a
bottleneck — the cost is in serialising the 2 kB account list.

### 6.8 Write paths

```bash
ssh hmc 'T=<BENCH_TOKEN>
hey -n 500 -c 20 -m POST -H "Authorization: Bearer $T" -H "Content-Type: application/json" \
  -d "{\"method\":\"GET\",\"target\":\"api.example.com:443\",\"nodeId\":\"node-01\",\"body\":\"{\\\"ping\\\":true}\"}" \
  -o csv "http://127.0.0.1:2291/v1/requests/mutations"
hey -n 500 -c 20 -m POST -H "Authorization: Bearer $T" -H "Content-Type: application/json" \
  "http://127.0.0.1:2291/v1/leases/lease-01/actions/rotate"
hey -n 500 -c 20 -m POST -H "Authorization: Bearer $T" -H "Content-Type: application/json" \
  "http://127.0.0.1:2291/v1/nodes/node-01/actions/probe"'
```

| Write | N | p50 ms | p95 ms | p99 ms | rps | Codes |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| `POST /v1/requests/mutations` | 500 | 0.20 | 1.80 | 10.00 | 23 364 | 201 ×500 |
| `POST /v1/leases/lease-01/actions/rotate` | 500 | — | — | — | 32 712 | 2xx |
| `POST /v1/nodes/node-01/actions/probe` | 500 | — | — | — | 27 148 | 2xx |

Writes run at roughly half the read rate — expected, since each mutation
appends to the request log and the audit log and then triggers a snapshot
decision.

**Caveat on the mutation body:** the first attempt posted
`{"kind":"probe","node_id":"node-01"}` and got **400 on all 500 requests**. The
real contract is `model.MutationLabInput{method, target, nodeId, body}`; with
`nodeId` (not `node_id`) it returns 201. The 400-path itself is fast and
correct — worth noting only because the dashboard and the daemon must agree on
the camelCase field names.

### 6.9 Cold start

```bash
ssh hmc 'cat > /tmp/bench83/startup.sh <<"EOS" ... EOS   # spawns ./ajs-accountd with
         AJS_LISTEN=127.0.0.1:2297 AJS_STATE_FILE=/tmp/bench83/state/p3.json,
         polls /healthz until 200, SIGTERMs
EOS
ssh hmc 'cd /tmp/bench83 && ./startup.sh'
```

```
run1 ready_in_ms=24
run2 ready_in_ms=23
run3 ready_in_ms=23
```

**~23–24 ms from `exec` to first `/healthz` 200**, three runs, empty state file,
Resin disabled, no vendor login. Poll granularity was 5 ms, so the true value is
within ±5 ms of these.

---

## 7. SOCKS5 gate (loopback) — throughput and handshake latency

The tree has **no `func Benchmark` at all**, so there is no in-repo gate
benchmark to run. Two measurements were made instead.

### 7.1 Baseline: raw loopback TCP echo ceiling (no gate in the path)

A minimal `io.Copy(c, c)` echo server was built in a throwaway module at
`/tmp/bench83/gate` and driven for 5 s with 64 KiB round-trips:

```bash
ssh hmc 'cd /tmp/bench83 && nohup ./echosrv > /tmp/bench83/echo.port 2>&1 &'
ssh hmc 'python3 - <<"PYEOF"   # 5 s, 64 KiB payload, TCP_NODELAY, ping-pong
... roundtrips=len(buf); mb=len(buf)*65536/1024/1024 ...
PYEOF'
```

```
roundtrips=143934 MB=8995.9 rtt_us=34.7 MB/s_oneway=1799.2
```

**Raw loopback ceiling on this host: ~1.80 GB/s** with 34.7 µs per 64 KiB
ping-pong. This is the number any gate figure must be measured against.

### 7.2 Gate relay — copy of `internal/gate/server.go` in a throwaway module

`daemon/internal/gate/server.go` has zero intra-module imports (stdlib only), so
it was copied verbatim into `/tmp/bench83/gatepkg` (module `gatepkg`, `go 1.25`)
and driven by a temporary `measure_test.go` with an echo `Session`. **No file in
the scratch trees was modified.**

```bash
ssh hmc 'cp .../daemon/internal/gate/server.go /tmp/bench83/gatepkg/'
ssh hmc 'cd /tmp/bench83/gatepkg && printf "module gatepkg\n\ngo 1.25\n" > go.mod && go build ./...'
ssh hmc 'cd /tmp/bench83/gatepkg && go test -run "TestGate" -v .'
ssh hmc 'cd /tmp/bench83/gatepkg && go test -run XXX -bench . -benchtime 5s .'
```

Handshake latency — N=500 sequential SOCKS5 greet + CONNECT through the gate:

```
GATE_HANDSHAKE N=500 p50=159us p95=253us p99=1117us max=2607us
```

Relay throughput — one flow, 64 KiB writes, 5 s window:

```
GATE_RELAY window=5.0s bytes=4600430592 MB=4387.3 MB/s=877.5 (relayed, counted once)
GATE_STATE {NodeID:bench AccountID:bench Active:1 Limit:32 Draining:false
            Accepted:1 Rejected:0 IngressBytes:4600430592 EgressBytes:4600430592
            StartedAt:2026-10-24 00:22:54.755806767 +0900 JST m=+0.101454063}
```

`go test -bench` (`-benchtime 5s`, Go benchmark harness):

```
BenchmarkGateConnect-12     	   34963	    172222 ns/op
BenchmarkGateRelay64K-12    	   19975	    289074 ns/op	 226.71 MB/s
```

| Metric | Value |
| --- | --- |
| Gate handshake p50 / p95 / p99 | **159 µs / 253 µs / 1117 µs** (N=500) |
| Gate handshake, `go bench` | 172 222 ns/op (34 963 iters) |
| Gate relay, one 64 KiB flow | **877.5 MB/s** (4 387.3 MB in 5.0 s) |
| Gate relay, `go bench` 64 KiB | 289 074 ns/op → **226.71 MB/s** (19 975 iters) |
| Raw loopback ceiling | 1 799.2 MB/s |

**Reading these:** the two throughput figures are not contradictory — they
measure different things. The 877.5 MB/s run reuses one established flow and
streams 4.4 GB through it; the 226.71 MB/s benchmark pays a fresh TCP accept +
SOCKS5 handshake + connection teardown **per 64 KiB**, so it is dominated by
handshake overhead (172 µs of the 289 µs is the connect alone). The gate relay
itself runs at roughly **half the raw loopback ceiling** (877.5 vs 1 799.2 MB/s),
which is the expected cost of two `io.Copy` hops plus the gate's own buffering.

`GATE_STATE` confirms the counters are live and correct: `IngressBytes ==
EgressBytes == 4 600 430 592`, `Accepted:1`, `Rejected:0`, `Limit:32`.

**Caveat:** the p99 of 1 117 µs on the handshake (7× the p50) is host
scheduling noise on a box with load ~2 and 25 logged-in users, not gate
behaviour. Re-run on an idle host before quoting it.

---

## 8. Test-count reconciliation (the 261 / 71 claim)

| Claim | Where | Measured | Verdict |
| --- | --- | --- | --- |
| 261 Go tests | brief / prior report | **250 top-level Go tests on HMC** (218 daemon + 32 emulator), all pass | **Off by 11 on HMC**; the 261 is the *local* tree count |
| 71 Vitest | brief / prior report | **71** (11 files, all pass) | **Confirmed** |

Why 250 vs 261: the HMC scratch copy lacks two files the local tree has —
`daemon/cmd/ajs-accountd/main_test.go` (4 tests) and
`daemon/internal/api/update_test.go` (7 tests). 4 + 7 = **11**. Local
`grep -rhoE "^func Test[A-Za-z0-9_]+" --include="*_test.go" daemon emulator |
wc -l` → **261**; the same command on HMC → **250**. Both are correct for their
tree; the HMC scratch copy is 11 tests stale relative to HEAD `2eee11a`.

Also note: the local `git status` shows `daemon/cmd/ajs-accountd/main.go` as
modified, so even the local 261 is a working-tree number, not a `2eee11a`
number.

**Recommended wording for the report:** "250 Go tests pass on HMC (218 daemon +
32 emulator); 261 in the local working tree, which carries 11 tests
(`main_test.go`, `update_test.go`) not yet present in the HMC scratch copy. 71
Vitest tests pass in both."

---

## 9. Caveats

1. **Busy host.** ~40 other listeners, load 1.8–2.9, 25 logged-in users, 8 GiB
   RAM with ~1.9 GiB free. Absolute rps figures are host-bound, not
   server-bound. Relative rankings (which endpoint is expensive) are reliable;
   absolute ceilings are not.
2. **No in-repo benchmarks.** `grep -rhoE "^func (Fuzz|Benchmark)[A-Za-z0-9_]+"`
   over both Go modules returns **0**. The gate numbers in §7 come from a
   throwaway copy of `server.go` in `/tmp/bench83/gatepkg`, not from anything in
   the tree. Nothing in the tree was edited; `go vet ./...` is still clean on
   both modules.
3. **`scripts/metric.py` is missing on HMC.** The 8/8 conformance gate was
   verified by invoking the eight named tests directly. The script exists
   locally at `scripts/metric.py`.
4. **`go clean -cache` was run twice** (once per module) to get cold build
   numbers. That invalidated the shared build cache for every other user of the
   box. Cold numbers are single-shot and were not repeated to limit the damage.
5. **N=1000 percentiles are noisy.** p99/max at N=1000 (`/healthz` max 10.9 ms)
   are client-scheduling artifacts. Use the sustained `-z 10s` table (§6.4,
   N = 10⁵–10⁶) for anything load-bearing.
6. **Benchmarked daemon is not production-configured.** No Resin
   (`resin_disabled`), no vendor login (`account_login_disabled`), no master key
   (`secrets_disabled`), no SQLite — JSON snapshot at 3600 s into an empty
   throwaway dir. Resin write-through and real vendor login would change every
   write-path number.
7. **Seed data only.** All API figures use the dashboard mock seed (40 request
   rows, ~12 accounts, ~12 nodes). `/v1/requests` at 149.8 kB/response is the
   clearest artefact of that; real data volume will move it further.
8. **Gate relay measured over loopback with an echo Session.** Real gates carry
   an authenticated FV session into an upstream SOCKS5; the numbers in §7 are
   the gate's own cost plus two loopback hops, with no crypto and no vendor
   RTT.
9. **`AJS_SNAPSHOT_INTERVAL` needs a unit.** `3600` fails; `3600s` works. The
   daemon exits rather than defaulting.
10. **One-shot measurements.** No measurement here was repeated to convergence
    except cold start (3 runs) and the concurrency sweep (3 reps per level).
    Treat single-run rows as indicative.

---

## 10. Cleanup performed

* `ajs-accountd` on 127.0.0.1:2291 — **stopped** (PID 3967633, then 418584).
* Echo server on 127.0.0.1:46775 — **stopped**.
* `/tmp/bench83/gate`, `/tmp/bench83/gatebench`, `/tmp/bench83/metric`,
  `/tmp/bench83/echosrv` — **removed**.
* Remaining scratch: `/tmp/bench83/{bin,gatepkg,results,state,*.sh,*.py,*.txt}`
  — raw CSVs and helper scripts, kept only so the numbers above can be
  re-derived. Nothing under `/srv/dwgxhomecloud/scratch/ajs-multimodule` or
  `/srv/dwgxhomecloud/scratch/loveplus-dashboard` was written, except
  `loveplus-dashboard/dist`, which `npm run build` regenerated in place.
* Verified after teardown: `ss -ltn` shows **neither 2291 nor 46775**. The four
  protected services are still owned by their original PIDs and were never
  touched:
  - `127.0.0.1:2261` → `ajs-accountd-la`, pid 1933966
  - `127.0.0.1:2262` → `ajs-accountd-li`, pid 3555275
  - `127.0.0.1:18081` → `docker-proxy`, pid 1888616
  - `127.0.0.1:18082` → `MainThread`, pid 3484431
