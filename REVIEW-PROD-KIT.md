# REVIEW — Production deploy kit (AJS-394)

## Verdict

**NO-GO for production, including a public customer portal or a browser-operated admin console.** The kit has sound portal/bearer separation and a useful hardened-service baseline, but the admin reverse proxy confers full authority on every request without a request-origin/CSRF defense. This is a demonstrated cross-origin state-changing vulnerability, not a theoretical CORS concern. A clean checkout also cannot follow the documented `npm ci` install path because no npm lockfile exists.

The static kit is not the same thing as a production-ready service. A final launch requires fixing P1-1 through P1-3, then running the real host, TLS, firewall, backup/restore, payment, vendor and release drills listed below.

## Denominators and scope

- **Assignment:** `AJS-394-ProdKitAudit.md` was not present in the checkout or parent directory and had no tracked history. This audit therefore uses the requested scope and the in-tree production kit (`deploy/production/`, AJS-387) rather than inventing additional assignment requirements.
- **Pinned baseline:** branch `panel-design`, commit `833b215296bcd1252677460bd50bbece05b3cb00` (`fix(daemon): end unservable leases explicitly on restart`). A detached clean worktree was used for clean-checkout tests. No production-kit files were modified by this audit.
- **Concurrent work:** unrelated order/OpenAPI/package edits entered the primary worktree during the audit. Findings 1–3 are present at the pinned revision. The package execution is explicitly marked as a worktree check; the review does not attribute concurrent changes to the production-kit commit. No unrelated edits were reverted.
- **Review surface:** nginx TLS/routing/headers/rate limits; proxy-injected bearer; portal cookie/CSRF separation; systemd; nftables; bundle build and dependency reproducibility; packaging/release workflow; daemon startup, authentication and snapshot restart. No vendor login, real payment, real DNS/certificate, real Resin, or production credential was used.
- **Method:** read-only review plus disposable Linux/Windows daemon and nginx instances, a temporary Go regression test, and isolated build/package runs. Test tokens and email addresses were synthetic. No host firewall or systemd installation was changed.

### Executed evidence

| Check | Result |
|---|---|
| `bash deploy/production/check.sh` | PASS, 48 checks; nginx and nft checks skipped in the initial environment. This does **not** imply runtime readiness. |
| Same gate with Ubuntu nginx 1.24.0 staged into `PATH` | **FAIL, 1 of 49** at `nginx -t`; see P1-3. |
| nginx 1.28.0 Windows isolated syntax/runtime test | Syntax PASS after host-only path adaptation; real TLS edge probes below PASS/FAIL as stated. This is not a Linux production certification. |
| `nft -c -f firewall/ajs-production.nft` in an isolated user/network namespace | PASS. No rules were applied to the host. |
| `systemd-analyze verify` with missing host binary/env paths substituted as the gate does | PASS; `systemd-analyze security --offline=yes`: exposure **1.5 OK**. No actual `systemctl` boot was tested. |
| `build-bundles.sh` with the existing `node_modules` | PASS; portal/admin trees identical; exported `VITE_PANEL_TOKEN` is refused. No secret literal was found. |
| Clean detached checkout: `npm ci` | **FAIL EUSAGE**: no `package-lock.json` or shrinkwrap. |
| `go test -count=1 ./...` in clean pinned `daemon/` | PASS all packages. `go vet ./...` on primary baseline PASS. |
| Dashboard serial suite, pinned clean checkout | **PASS: 52 files, 469 tests**. Primary after concurrent edits: 467 passed, 2 failed, both new OpenAPI consistency checks. Six prior timeout-only suites passed serially (74/74). |
| Disposable production-config daemon | Linux and native start/health/bearer-isolation checks PASS. Signup accepted only a synthetic pending tenant. |
| Linux restart from snapshot | `state_loaded`; test user and pending tenant survived; state file mode 0600. No sealed-entry recovery was exercised. |
| Package builder and `SHA256SUMS` (current worktree) | Three daemon binaries and dashboard archive built; five listed checksums PASS. Manifest honestly says `worktree: dirty`; not a signed release. |

## Findings

### 1. P1 — Proxy-injected admin bearer is CSRF-able; cross-origin suspend and delete were reproduced

**Evidence**

- `deploy/production/nginx/sites-available/ajs-admin.conf:49-61` injects `ajs-admin-token.conf` into every admin `/v1/` request. There is no `Origin`, `Referer`, `Sec-Fetch-Site`, CSRF, or operator authentication check at the edge.
- `daemon/internal/api/server.go:665-718` authenticates the admin bearer and applies CORS response headers, but never rejects an unsafe request based on origin. `decodeJSON` (`server.go:964-978`) does not require JSON Content-Type, and empty-body destructive actions do not decode a body at all (`users_keys.go:75-88`).
- The kit itself states that there is no admin login and that anyone reaching the origin can do everything (`deploy/production/README.md:291-296,409-410`). Loopback/VPN exposure does not stop a browser from submitting a cross-origin form/fetch to a reachable admin host.

**Reproduction**

A temporary `httptest` regression test sent both `Content-Type: text/plain` and `application/x-www-form-urlencoded` with `Origin: https://attacker.invalid` and `Sec-Fetch-Site: cross-site`. Both `POST /v1/users/u-01/suspend` requests returned **200**, and the user became disabled. The control `GET /v1/portal/session` with the same bearer returned **401**, confirming that portal isolation is intact but irrelevant to the admin defect.

Through the actual staged nginx TLS edge, with no browser-side token:

```text
GET  /v1/users                    -> 200 (bearer injected by nginx)
POST /v1/users/u-02/suspend       -> 200, Origin attacker.invalid
stored user                       -> enabled:false
```

Direct daemon probes additionally produced **200** for suspend and **204** for `DELETE /v1/users/u-01`, both with hostile origin metadata. These are exactly browser-simple requests: CORS can prevent response reading, but does not prevent the mutation. Attackers can target other no-body or predictable destructive actions too.

**Impact:** an operator browsing an attacker-controlled site can have users suspended/deleted and other administrative actions executed with the shared admin authority. Network placement limits who can reach the admin origin; it is not CSRF protection.

**Required fix / falsifier:** put real operator authentication in front of the admin origin (mTLS, an identity-aware proxy, or a deliberately designed admin login), **and** enforce an exact admin origin plus cross-site `Sec-Fetch-Site` rejection on every unsafe admin request. If cookies are introduced, use a CSRF token and appropriate cookie attributes. Non-browser API clients need an explicit bypass that does not silently accept browser-simple cross-site requests. Repeat the overlay and real-nginx probes: hostile origins must get 403 and leave state unchanged for suspend, delete, approve, settle, refund, and restore paths. A passing test that only checks missing `Access-Control-Allow-Origin` is insufficient.

### 2. P1 — A fresh production install cannot use the documented `npm ci` path; dependencies are not locked

**Evidence**

- `deploy/production/README.md:181-182` requires `dashboard/node_modules` from `npm ci`; `build-bundles.sh:57-60` repeats that instruction and cannot proceed without the directory.
- `dashboard/package.json` exists, but there is no tracked `dashboard/package-lock.json` or shrinkwrap. `.github/workflows/package.yml:53-54` uses non-reproducible `npm install` instead.
- Clean detached revision `833b215` with an empty dashboard dependency directory: `npm ci --ignore-scripts --no-audit --no-fund` exits **1 EUSAGE**, stating that `npm ci` requires a lockfile.

**Impact:** the documented build fails before compilation on a fresh host. Switching to `npm install` is not an adequate production fix: dependency versions can drift and the release becomes irreproducible.

**Required fix / falsifier:** commit a reviewed lockfile, use `npm ci` in kit instructions, Dockerfile and workflows, and run the build from a clean checkout with an empty npm cache in CI. Verify the installed tree and resulting artifact hashes against an intended release. Do not infer reproducibility from a successful build against a long-lived `node_modules`.

### 3. P1 — `check.sh` fails on the documented Debian/Ubuntu non-root nginx path; PASS with SKIP is treated too strongly

**Evidence**

- `deploy/production/nginx/nginx.conf:13` sets `pid /run/nginx.pid`. `check.sh:225-249` rewrites config/log/certificate paths into a temporary directory, but not this PID path or distro temp paths.
- With an otherwise valid Ubuntu nginx 1.24.0 binary and the original kit, the gate returns **FAIL (1 failed of 49)** at `nginx -t`. The unprivileged test process cannot create `/run/nginx.pid`; on a host lacking the distro nginx state directories, temp paths also need staging. The Windows syntax test passed only after explicitly adapting the PID path, confirming the staging dependency.
- `check.sh:193-201` deliberately turns missing nginx into SKIP and still exits PASS. Its own README (`:365-395`) lists runtime behavior it does not test.

**Impact:** the documented `bash deploy/production/check.sh` gate is not reliable on the target distribution/user combination, and a green development run can conceal the exact production syntax check it is supposed to enforce.

**Required fix / falsifier:** stage a PID file and all nginx temp directories under the test prefix, or execute the syntax test as the intended root/nginx environment. Make production nginx, systemd and nft checks required at release time; keep a separate development-only static mode that cannot be called production PASS. Run the **unmodified** gate as the documented non-root user on a minimal Ubuntu 24.04 host and require `check: PASS` with no required SKIP. This is a gate/portability blocker; it is not a claim that the vhost syntax itself is invalid.

### 4. P2 — Release artifacts can be mock-mode and diverge from the production kit

**Evidence**

- `dashboard/src/lib/api.ts:172` treats anything except `VITE_USE_MOCK=false` as mock mode.
- `.github/workflows/package.yml:52-58` runs plain `npm run build` with no production environment, then packages that artifact at `:66-73`. The dashboard Dockerfile also defaults `VITE_USE_MOCK=true`.
- `deploy/package.sh` defaults to mock mode when it builds the dashboard and packages a prebuilt bundle by default; it does not assert the live mode or compare the output with `build-bundles.sh`.
- By contrast, `build-bundles.sh:70-76` explicitly builds `VITE_USE_MOCK=false`, same-origin, and no panel token. That kit-built bundle is the one verified here.

**Impact:** an operator using the release bundle rather than the kit-specific output can ship synthetic demo behavior instead of the live control plane. No release workflow has actually run: the baseline has no remote or tags.

**Required fix / falsifier:** define one live production artifact recipe and have CI/package/production use it. Refuse mock-mode production packaging; make the build fail closed on token-bearing env; verify behavior against a real daemon, not just string greps. Run a tag build in a real remote and compare the served artifact digest to the release manifest.

### 5. P2 — No default-deny virtual host; unknown Hostnames enter the customer site and control redirects

**Evidence**

- `deploy/production/nginx/nginx.conf:68-72` says unknown Host hits no block, but nginx chooses the first server as default. The customer HTTP and HTTPS blocks are first.
- The customer HTTP block reflects `$host` into the redirect (`sites-available/ajs-portal.conf:23-25`). No `default_server` catch-all is configured.
- Real nginx probe with `Host: attacker.invalid`: **301** to `Location: https://attacker.invalid/v1/users`. Unknown HTTPS Host was handled by the customer vhost and returned **404**, rather than being rejected at the host boundary. With `Host: panel.example.com`, malformed extra-header injection was not observed (nginx treated the header separately); no header-smuggling claim is made.

**Impact:** host/IP-based probing reaches the customer site and arbitrary Host values can produce attacker-chosen redirects. This does **not** expose `/v1/accounts` through the customer origin; that isolation was verified. It does weaken the stated two-origin boundary and provides an avoidable phishing/host-header primitive.

**Required fix / falsifier:** add explicit default-deny HTTP/HTTPS servers (or `return 444`), handle ACME only on the intended hostname, and use the fixed canonical portal origin for redirects. Unknown Host must not reach portal content/API or influence a redirect. Re-test HTTP, HTTPS and IP-literal Host cases.

### 6. P2 — Public `/healthz` is only an nginx liveness response, not service readiness

**Evidence**

- `sites-available/ajs-portal.conf:41-47` returns `200 "ok"` without contacting the daemon. The README's external health check (`:344-347`) therefore succeeds if nginx and its static config are up even when the daemon is down.
- Daemon `/healthz` itself reports process/store counts, not readiness of Resin, vendor sessions, payments or state-file writability (`daemon/internal/api/ops.go:22-41`).
- The unit has no systemd health condition; `Restart=on-failure` does not detect a wedged but running service.

**Impact:** a monitoring/load-balancer check can report a healthy customer service while all API requests fail. This is an operational readiness gap, not a hidden vendor failure.

**Required fix / falsifier:** distinguish edge liveness from API readiness; expose a bounded readiness check through a dedicated route and alert on daemon, state-write and required-integration failures. Test daemon stopped, disk full/read-only, Resin unavailable and vendor degraded states. Do not place a long vendor call in the health handler.

### 7. P2/P3 — Production rollout lacks a verified end-to-end operational and recovery gate

**Evidence / limits**

- There is no systemd enable/start/restart test against the real installed unit, no nginx production-host run with owner DNS/certificates, and no applied-firewall test. This audit validates syntax and isolated behavior only.
- State is a periodic JSON snapshot; README documents up to 30 s of loss and stop-the-service backups. There is no production restore/rollback drill with sealed account/device/webhook material. The master key is blank in the example and merely “recommended” in the environment table; production acceptance should require it and prove recovery with it.
- Public payment callbacks are routed correctly, but no real 1-cent CNY transaction or provider callback was performed. No vendor account or live Resin was used. The repository explicitly leaves multi-account coexistence, Resin + gate live publication, B2 signing and broader device behavior unverified.
- No release tag/remote has exercised the release workflows. Packaging checksums provide integrity for a produced file set, not artifact authenticity: there is no signature/provenance verification or host rollback runbook in this kit.

**Required acceptance:** use a staging host with the actual distro/systemd/nginx/nft versions and real owner inputs. Prove daemon start under `User=ajs`, TLS renewal/reload, firewall reachability matrix, forced restart/crash and backup/restore with master-key recovery, release upgrade and rollback. Exercise the exact supported vendor/multi-account/Resin shape and a 1-cent CNY payment, including duplicate callback/restart. Record explicit RPO/RTO and monitoring alerts. If those features are not in the launch scope, state a narrower production shape rather than claiming full readiness.

## Confirmed sound / self-audit

- **Portal/admin identity separation:** admin bearer cannot act as a portal session; public `/v1/accounts` and `/v1/users` are 404 through the customer vhost; `/v1/portal/session` is 401 without its cookie. Portal unsafe requests use exact origin and CSRF checks (`tenant_auth.go:104-167`).
- **Forwarded client IP:** nginx overwrites `X-Forwarded-For` with `$remote_addr`; daemon trusts it only for configured loopback proxies. This addresses the documented spoofed-leftmost-bucket attack.
- **TLS/headers:** TLS 1.2/1.3 policy, disabled session tickets, HSTS, frame denial, nosniff, referrer and permissions headers are present and observed on the staged public response. A self-signed test certificate cannot establish real certificate-chain/OCSP health.
- **Secret handling:** no real credentials in the kit; build refuses an exported token; admin template contains only a placeholder; rendered snippet is 0600 on a Linux filesystem. No rendered bearer was exposed in responses or this report. `state.json` is 0600; sealed-entry recovery still needs a drill.
- **Hardening/exposure baseline:** unit verification passes with the gate's substitutions and scores 1.5 OK offline; no capability is granted; writable state is constrained. Firewall syntax passes and gates remain loopback by configuration. Actual host boot and rule application are unverified.
- **Runtime robustness:** Go tests and vet pass; native/Linux daemon starts with strong synthetic token/master-key configuration, rejects missing bearer, writes and reloads user/tenant snapshot state. This does not demonstrate vendor, Resin or payment readiness.
- **Rejected false positives:** the public bundle retains mock *code and UI strings*, but that is not proof it runs in mock mode; the kit explicitly selects live mode. A broad string search for `VITE_PANEL_TOKEN` also finds explanatory text, not a token value. Initial dashboard timeout failures were not product defects: the affected 74 tests passed serially. Later two OpenAPI failures followed concurrent changes and are not silently counted as pinned-kit defects.
- **Diagnostic hygiene:** probes that accidentally moved `check.sh` away from its expected layout produced irrelevant missing-file scans and were discarded. A few Windows/WSL path/tool-wrapper failures were environment issues; only reproducible product/gate behavior is reported above.

## Release conditions

Do not convert this verdict to GO until all of the following are evidenced on the intended production shape:

1. **P1 security:** real admin identity boundary plus unsafe-request origin/CSRF defense, with the demonstrated attacks now failing closed.
2. **P1 build/gate:** lockfile-based clean build and unmodified production gate passing without required SKIP on the supported host.
3. **Artifact correctness:** one verified live, token-free artifact used by CI, package and install; a real tagged release exercised.
4. **Host proof:** real systemd boot, TLS/DNS/renewal, firewall reachability, upstream readiness and monitoring; default-deny host behavior fixed.
5. **Recovery:** encrypted backup/restore and upgrade/rollback drills, including secrets/device/webhook state and an explicit snapshot-loss window.
6. **Business/runtime proof:** supported vendor/Resin/multi-account behavior and, if payments are enabled, a real 1-cent CNY checkout settled exactly once through notify/reconcile and restart.

**Current decision: NO-GO.** The customer edge is a promising foundation, but the admin trust model and clean-release path are not production-safe, and the remaining host/live evidence is incomplete.
