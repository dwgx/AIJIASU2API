# SECURITY — credential handling and attack surface

Status: read-only audit, 2026-10-24. Branch `panel-design`, HEAD `2eee11a` (from `.git/HEAD` and the
last `.git/logs/HEAD` entry; no git binary was run). Host for static work: Windows workstation.
HMC (`ssh hmc`, Debian 13) was read only over `ssh://` text reads; no service was started, stopped
or contacted, and no vendor host was called.

Verdict: **no P0 and no P1.** The shipped defaults keep every listener on loopback and the panel
behind one bearer token; the credential-at-rest design (AES-256-GCM, account id as AAD, 0600/0700,
atomic rename, redacting `Secret` type) holds under inspection. Thirteen P2 findings remain, all
reachable only through operator action or upstream compromise; the highest-impact ones are a
credential/PII file that the documented dev workflow creates inside the tree without an ignore rule
(S1), an accepted placeholder admin token (S2), and a dashboard token that is public by construction
(S5). No credential value, phone number, token or certificate fingerprint is reproduced here.

## Findings

| ID | Sev | Title | Primary location |
|---|---|---|---|
| S1 | P2 | Documented dev runs create credential/PII files inside the tree; neither is ignored | `.gitignore:20-23`, `daemon/cmd/ajs-accountd/main.go:229` |
| S2 | P2 | Placeholder admin token `change-me` is accepted as a real token | `deploy/.env.example:17`, `daemon/cmd/ajs-accountd/main.go:52-54` |
| S3 | P2 | No Host-header allowlist; DNS rebinding defeats the CORS boundary | `daemon/internal/api/server.go:307-324` |
| S4 | P2 | `AJS_ALLOW_INSECURE=1` has no bind-address guard | `daemon/cmd/ajs-accountd/main.go:39-41,52-54` |
| S5 | P2 | Admin token is baked into the live dashboard bundle and the image config | `dashboard/Dockerfile:18-22`, `research/80-debugger-prep/PLAYBOOK.md:277-278` |
| S6 | P2 | Third-party Resin container receives `AJS_MASTER_KEY` and the panel token | `deploy/compose.yml:25-27,64-66` |
| S7 | P2 | Update path deploys mutable `:latest` tags with no digest or signature check | `deploy/update.sh:59-61,138-139` |
| S8 | P2 | `update.sh` is `#!/bin/sh` but uses `pipefail`; rollback likely aborts on Debian dash | `deploy/update.sh:1,13` |
| S9 | P2 | `.env` is executed as shell and holds every secret; no permission requirement | `deploy/update.sh:36-38`, `deploy/README.md:204-219` |
| S10 | P2 | Credentials accepted on argv (emulator `-password`, daemon `-token`, `-resin-token`) | `emulator/cmd/ajsproxy/main.go:84`, `daemon/cmd/ajs-accountd/main.go:40,45` |
| S11 | P2 | `AJS_API_HOSTS` has no scheme allowlist; `http://` sends the password in cleartext | `daemon/cmd/ajs-accountd/main.go:366-369`, `emulator/internal/api/client.go:321` |
| S12 | P2 | Release pipeline: `npm install` without a lockfile, actions pinned by tag, write token, `ref_name` in `run:` | `.github/workflows/release.yml:24-26,57,113` |
| S13 | P2 | Live scratch credential files exist with no recorded mode | `research/82-live-20261023/REPORT.md:56,199` |

### S1 — P2 — Credential/PII files from the documented dev workflow are not ignored

**Where:** `.gitignore:20-23` (secrets block contains only `.env`, `*.key`, `*.pem`);
`daemon/cmd/ajs-accountd/main.go:229` (`AJS_STATE_DIR` defaults to `./data`); `README.md:115-116`
(`cd daemon` then `go run ./cmd/ajs-accountd`); `emulator/README.md:46-50` (`-config ../config.json`)
and `:174-175` ("The config contains a password: keep it outside the repository");
`daemon/Dockerfile:16-17` (`COPY daemon ./daemon`, `COPY emulator ./emulator`).

**Evidence:**
- `grep -n 'data/|state.json|secrets.json' .gitignore` → no match (only `daemon/README.md` matches).
- `glob .dockerignore;daemon/.dockerignore;dashboard/.dockerignore;emulator/.dockerignore` → `Path not found` for all four.
- `grep -n 'The config contains a password' emulator/README.md` → `174:The config contains a password: keep it outside the repository`.
- The root quickstart runs the daemon from `daemon/`, so the first run writes `daemon/data/state.json`
  (accounts with usernames — the live account's login name is a phone number — device identities,
  audit, and sealed envelopes) and, with `AJS_MASTER_KEY`, `daemon/data/secrets.json`.

**Impact:** one `git add -A` after the documented local run commits PII and sealed credential
blobs permanently into history; the same files are inside the Docker build context, so they land in
the build-stage layer and in any remote builder cache. `git` has no remote configured
(`.git/config` has no `[remote]`), which limits but does not remove the exposure.

**Fix:** add `data/`, `config.json`, `state.json`, `secrets.json` to `.gitignore`; add a root
`.dockerignore` excluding `**/data/`, `config.json`, `research/`; move the default state dir outside
the tree (or require `AJS_STATE_DIR`).

**Risk if unfixed:** permanent credential/PII exposure in history or build cache from a normal dev
run, not from an attack.

### S2 — P2 — Placeholder admin token is accepted as a real token

**Where:** `deploy/.env.example:17` (`AJS_ADMIN_TOKEN=change-me`);
`daemon/cmd/ajs-accountd/main.go:52-54` (the only check is `*token == ""`);
`daemon/internal/api/server.go:320-321` (constant-time compare against whatever was supplied);
`deploy/compose.yml:25-27` (the whole `.env` is loaded).

**Evidence:** `grep -n 'AJS_ADMIN_TOKEN=' deploy/.env.example` → `17:AJS_ADMIN_TOKEN=change-me`;
`grep -n 'token == ""' daemon/cmd/ajs-accountd/main.go` → `52: if *token == "" && !*insecure {`.
There is no length, entropy or placeholder denylist; the startup line only reports `auth=bearer`
(`main.go:189-193`), so a placeholder deployment looks identical to a real one.

**Impact:** `cp .env.example .env && docker compose up -d` (the documented quickstart) yields a
working admin API whose token is a string printed in the repository. Any local process — and, via
S3, a browser page — can then create accounts, trigger vendor logins, read account usernames, and
delete accounts.

**Fix:** refuse known placeholders (`change-me`, `local-test-token`, `test`, `admin`) and tokens
shorter than 32 characters at startup unless `AJS_ALLOW_INSECURE=1`; make `compose.yml` fail when
`AJS_ADMIN_TOKEN` is unset.

**Risk if unfixed:** silent full-admin compromise whenever the placeholder survives the first
deploy.

### S3 — P2 — No Host-header allowlist; DNS rebinding defeats CORS

**Where:** `daemon/internal/api/server.go:307-324` (auth and routing never inspect `r.Host`);
CORS is an exact-origin allowlist (`server.go:271-291`).

**Evidence:** `grep -n 'r\.Host|Host)|X-Forwarded-For|Forwarded' daemon/internal/api` → no matches.
`grep -n 'Access-Control-Allow-Origin' daemon/internal/api/server.go` → `278` (only the matched
origin is echoed; no wildcard, no credentials mode).

**Impact:** a malicious page that rebinds its own hostname to `127.0.0.1` becomes same-origin with
the daemon, so the browser sends the `Authorization` header and the page reads the response — CORS
is never consulted. Combined with S2 (`change-me`) or S4 (`AllowInsecure`) this is a remote,
browser-driven takeover of a loopback panel. With a real random token it is not exploitable.

**Fix:** reject requests whose `Host` is not `127.0.0.1:<port>`/`localhost:<port>` (or the configured
`AJS_LISTEN` authority); keep the exact-origin CORS list as the second layer.

**Risk if unfixed:** the loopback assumption is the only thing standing between a web page and the
admin API.

### S4 — P2 — `AJS_ALLOW_INSECURE=1` has no bind-address guard

**Where:** `daemon/cmd/ajs-accountd/main.go:39-41` (`-listen` and `-insecure` are independent),
`:52-54` (no token required), `:189-193` (insecure is only a log word);
`daemon/internal/api/server.go:313-316` (unconditional bypass).

**Evidence:** `grep -n 'AJS_ALLOW_INSECURE|insecure' daemon/cmd/ajs-accountd/main.go` →
`41`, `52`, `190-193`; the README calls it "loopback only" (`daemon/README.md:854`) but no code
checks the listen address.

**Impact:** `AJS_LISTEN=0.0.0.0:2261 AJS_ALLOW_INSECURE=1` exposes an unauthenticated admin API to
the network; the only signal is `auth=insecure` in the startup log. State-changing POSTs are also
CSRF-able from any page in that mode (simple `text/plain` requests need no preflight, and
`decodeJSON` does not check `Content-Type`).

**Fix:** fail startup when `AllowInsecure` is set and the listen host is not loopback; log at WARN
on every request in that mode.

**Risk if unfixed:** one env var converts a loopback control plane into an open network service.

### S5 — P2 — Admin token baked into the live dashboard bundle and image config

**Where:** `dashboard/Dockerfile:18-22` (`ARG`/`ENV VITE_PANEL_TOKEN`, then `npm run build`);
`deploy/compose.yml:48` (passed from `.env`); `deploy/compose.yml:53` (bundle served on
`127.0.0.1:18081` with no auth — the nginx config in `dashboard/Dockerfile:30-47` has no
`auth_basic`); `research/80-debugger-prep/PLAYBOOK.md:277-278` instructs
`VITE_PANEL_TOKEN="$AJS_ADMIN_TOKEN"`.

**Evidence:** `grep -n 'VITE_PANEL_TOKEN' deploy/compose.yml dashboard/Dockerfile` →
`compose.yml:48`, `Dockerfile:19,22`; `grep -n 'VITE_PANEL_TOKEN' research/80-debugger-prep/PLAYBOOK.md`
→ `278: VITE_PANEL_TOKEN="$AJS_ADMIN_TOKEN" npm run build`. The repo's own check records that the
literal appears in the live bundle and not in the mock bundle (`deploy/README.md:363-365`).
Because line 22 promotes the `ARG` to `ENV`, the value is also in the image config (`Config.Env`),
readable with `docker image inspect` `[INFERENCE]`.

**Impact:** any local user/process can `curl` the JS asset and recover the panel token; the playbook
tells operators to make that token the real admin token. The docs accept this for loopback
(`dashboard/README.md:594-595`, `deploy/README.md:209-211`), but the build artifact also retains it
after the container is gone.

**Fix:** serve the SPA through a BFF that injects the bearer server-side (or issue a short-lived,
separate dashboard token); never reuse `AJS_ADMIN_TOKEN` for `VITE_PANEL_TOKEN`; build live images
with BuildKit secrets instead of `ARG`.

**Risk if unfixed:** the admin token is public to every local account and persists in image
metadata.

### S6 — P2 — Third-party Resin container receives `AJS_MASTER_KEY`

**Where:** `deploy/compose.yml:25-27` (accountd `env_file: .env`) and `:64-66` (resin `env_file:
.env`); `.env.example:17,21,52,54` (admin token, master key, Resin admin token, Resin proxy token).

**Evidence:** `grep -n 'env_file' deploy/compose.yml` → `25`, `64`; both point at the same `.env`.
`deploy/README.md:214-215` states the consequence explicitly ("each container's environment
contains every key in the file") and accepts it for a single-host stack.

**Impact:** `ghcr.io/resinat/resin:1.3.0` (pinned by tag, not digest) can read the AES-256 key that
decrypts every account password, plus the panel admin token, although it needs neither. A Resin
compromise or a malicious image update yields all vendor credentials.

**Fix:** split the file (`accountd.env`, `resin.env`) or replace `env_file` with explicit
`environment:` entries per service; give Resin only `RESIN_ADMIN_TOKEN`/`RESIN_PROXY_TOKEN`.

**Risk if unfixed:** least-privilege violation across a third-party boundary that the README already
flags as a known trade-off.

### S7 — P2 — Update path trusts mutable tags with no digest or signature verification

**Where:** `deploy/update.sh:59-61` (refs built from `${AJS_IMAGE_TAG:-latest}`), `:138-139`
(`compose pull` then `up -d --no-build`); `deploy/compose.release.yml:16-19`;
`deploy/.env.example:37` (`AJS_IMAGE_TAG=latest`).

**Evidence:** `grep -n '@sha256|cosign|docker trust|DOCKER_CONTENT_TRUST|notation|sigstore' deploy .github scripts`
→ no matches. `grep -n 'compose pull|compose up' deploy/update.sh` → `138`, `139`. The release
workflow pushes both the release tag and `:latest` (`.github/workflows/release.yml:85-86,95-96`).

**Impact:** a GHCR account or release-pipeline compromise republishes `:latest`; the next
`./update.sh` runs attacker code with host networking, the `/data` volume (sealed passwords) and
the vendor credentials. Rollback restores the previous local image, but nothing detects that the
pulled image was not the intended one.

**Fix:** pin by digest in `compose.release.yml` (or record `RepoDigests` in `update-state.json` and
verify before `up`); sign images (cosign/notation) and verify in `update.sh`; make `latest`
opt-in rather than the default.

**Risk if unfixed:** the release pipeline is the trust root, and it is verified only by TLS to GHCR.

### S8 — P2 — `update.sh` is `#!/bin/sh` but uses `pipefail`; rollback may abort on Debian dash

**Where:** `deploy/update.sh:1` (`#!/bin/sh`), `:13` (`set -euo pipefail`); HMC is Debian 13
(`ssh://hmc/etc/os-release` → `PRETTY_NAME="Debian GNU/Linux 13 (trixie)"`), where `/bin/sh` is
dash by policy and dash does not implement `set -o pipefail`.

**Evidence:** `grep -n 'set -euo|#!/bin/sh' deploy/update.sh` → `1`, `13`. The repo's recorded
verification only ran `sh -n update.sh` (syntax check, which does not execute `set`) and an
end-to-end exercise whose interpreter is not recorded (`deploy/README.md:269-284`).
`[UNVERIFIED]` — the runtime failure was not executed in this lane.

**Impact:** if dash rejects the option, `set` returns non-zero while `-e` is already active and the
script exits before doing anything — including `--rollback`, the compensating control for a bad
release. Operators would discover this during an incident.

**Fix:** change the shebang to `#!/usr/bin/env bash`, or drop `pipefail` (no pipeline in the script
depends on it).

**Verification command (one line):** `ssh hmc 'sh -c "set -euo pipefail"; echo exit=$?'` — expected
`exit=0`; a non-zero exit or `Illegal option` confirms the finding.

**Risk if unfixed:** rollback unavailable exactly when it is needed.

### S9 — P2 — `.env` is executed as shell and holds every secret; no permission requirement

**Where:** `deploy/update.sh:36-38` (`set -a; . ./.env; set +a`);
`deploy/README.md:204-219` (security notes, no `chmod`/`umask` requirement);
`.env.example:17,21,52,54`.

**Evidence:** `grep -n '\. \./\.env' deploy/update.sh` → `37`; `grep -n 'chmod|umask' deploy/README.md`
→ no match. The file is the single holder of `AJS_MASTER_KEY` (decrypts all account passwords),
`AJS_ADMIN_TOKEN`, `RESIN_ADMIN_TOKEN`, `RESIN_PROXY_TOKEN`.

**Impact:** sourcing makes `.env` executable code; if the deploy directory or file is group/world
writable, a lower-privileged local user gets code execution as the deploy user, who is normally in
the `docker` group (root-equivalent). Loose modes also expose the master key directly.
`[UNVERIFIED]` — modes on HMC were not readable with this lane's tools.

**Fix:** create `.env` with `umask 077` (or `install -m 600`); have `update.sh` refuse when
`stat -c %a .env` grants group/other bits; document the requirement next to `cp .env.example .env`.

**Verification command:** `ssh hmc 'stat -c "%a %U:%G %n" /srv/dwgxhomecloud/scratch/ajs-multimodule/.env'`.

**Risk if unfixed:** local privilege escalation to root-equivalent via a shell-sourced secret file.

### S10 — P2 — Credentials accepted on argv

**Where:** `emulator/cmd/ajsproxy/main.go:84` (`-password`), documented in the CLI usage
(`main.go:40-43`) and `emulator/README.md:42-43`; `daemon/cmd/ajs-accountd/main.go:40` (`-token`),
`:45` (`-resin-token`).

**Evidence:** `grep -n 'StringVar\(&o\.password' emulator/cmd/ajsproxy/main.go` →
`84: fs.StringVar(&o.password, "password", "", "account password (overrides config)")`.

**Impact:** the vendor account password and the panel/Resin tokens appear in `ps`/`/proc/*/cmdline`
and in shell history for every process on the host. The daemon's flags default from env, so this
only bites when an operator follows the flag form; the emulator's usage advertises it.

**Fix:** remove the `-password` flag (read the config or an env var); keep `-token`/`-resin-token`
env-only, or accept `-token-file`.

**Risk if unfixed:** credential disclosure to any local user during a debug session.

### S11 — P2 — `AJS_API_HOSTS` has no scheme allowlist

**Where:** `daemon/cmd/ajs-accountd/main.go:366-369` (hosts taken verbatim from env, no validation);
`emulator/internal/api/client.go:321` (`strings.TrimRight(host, "/") + "/client.php"`);
defaults are all `https://` (`daemon/internal/account/login_service.go:21-26`).

**Evidence:** `grep -n 'AJS_API_HOSTS' daemon/cmd/ajs-accountd/main.go` → `366`; no `url.Parse`,
scheme check or `http://` rejection in the path. The login body carries `username` and `password`
form-encoded (`emulator/internal/api/client.go:303-310`).

**Impact:** a typo (`AJS_API_HOSTS=http://login.mmegg.com`) sends the account password and the
device identity in cleartext, and silently disables the certificate pin (no TLS to pin).

**Fix:** reject any host whose scheme is not `https` at startup (except an explicit
`AJS_ALLOW_PLAINTEXT_CONTROL=1` for the lab); log the resolved scheme once.

**Risk if unfixed:** credential disclosure on the wire from a configuration typo.

### S12 — P2 — Release pipeline supply chain

**Where:** `.github/workflows/release.yml:57` (`npm install --no-audit --no-fund`, no lockfile in
the tree); `:24-26` (`contents: write`, `packages: write` for the whole job);
`:38,45,50,70,79,89,121` (actions pinned by major tag, not commit SHA); `:113`
(`${{ github.ref_name }}` interpolated into a `run:` shell script);
`dashboard/Dockerfile:6-7` (same `npm install` in the image build).

**Evidence:** `glob dashboard/package-lock.json;dashboard/npm-shrinkwrap.json` → `Path not found`;
`grep -n 'npm install|npm ci' .github/workflows/release.yml dashboard/Dockerfile` → `release.yml:57`,
`Dockerfile:7`; `grep -n 'uses:' .github/workflows/release.yml` → the seven tag-pinned actions.
Dependency ranges are `^` (`dashboard/package.json`), so every build resolves new code that runs
with a `packages: write` token.

**Impact:** a compromised transitive package or retargeted action tag can push a backdoored image
that `update.sh` then deploys with host networking and the credential volume (see S7). The
`ref_name` interpolation is a second, weaker vector: a tag name containing a single quote breaks out
of the `echo` in the release-notes step — but pushing a tag already requires write access.

**Fix:** commit a lockfile and use `npm ci`; pin every action to a commit SHA; scope permissions per
job/step; pass `github.ref_name` through `env:` instead of inline interpolation.

**Risk if unfixed:** upstream compromise converts directly into a deployed control-plane image.

### S13 — P2 — Live scratch credential files exist with no recorded mode

**Where:** `ssh://hmc/srv/dwgxhomecloud/scratch/ajs-live-20261023/` contains `live.env`,
`create.json`, `login.json`, `state/`, `cap/`; `research/82-live-20261023/REPORT.md:56` records
modes only for `state.json`/`secrets.json`, and `:199-201` repeats that scope.

**Evidence:** the ssh directory listing above (names only — the ssh read tool returns UTF-8 text,
not metadata). `grep -n 'stat -c' research` → matches only in `74-re-methods` and `75-request-mutation`
(unrelated lab runs); no recorded `stat` for the live scratch files.

**Impact:** `create.json` is the `POST /v1/accounts` body (username = phone number, password in
clear), and `live.env`/`login.json` hold the live credentials used for the 2026-10-23 run. If their
mode is not 0600 they are readable by any local account on the build host. `[UNVERIFIED]`.

**Fix / verification (one line):**
`ssh hmc 'stat -c "%a %U:%G %n" /srv/dwgxhomecloud/scratch/ajs-live-20261023/{live.env,create.json,login.json,state,cap}'`
then `chmod 600` the files and `chmod 700` the directories; delete the run when it is closed.

**Risk if unfixed:** live vendor credentials and a phone number readable on a shared host.

## Controls verified (no finding)

| Control | Location | What was checked |
|---|---|---|
| Bearer auth scope | `daemon/internal/api/server.go:307-324` | `/v1/*` and `/metrics` require the token; `/healthz` is open by design; `subtle.ConstantTimeCompare`; no `AllowInsecure` unless configured |
| Router cannot be used to skip auth | `server.go:375-424` | Exact segment-count and segment-equality match; `/metrics/`, `//metrics`, `//v1/accounts`, `/v1/../v1/accounts` match no route, so the prefix predicate cannot be abused to reach a handler |
| CORS | `server.go:271-291` | Exact-origin allowlist, no wildcard, no `Allow-Credentials`, `Vary: Origin`; preflight answers 204 without auth (correct) |
| Body and time bounds | `server.go:296-303`, `main.go:171-179` | 1 MiB `MaxBytesReader`, `ReadHeaderTimeout` 5 s, `ReadTimeout` 15 s, `WriteTimeout` 120 s, `IdleTimeout` 60 s |
| No debug surface | whole tree | `net/http/pprof`, `runtime/pprof`, `/debug/pprof`, `expvar` → no code matches; only research recommendations (`research/78-ecosystem/REPORT.md:75`) |
| Metrics content | `daemon/internal/api/ops.go:57-125` | Aggregate gauges only (build version, uptime, counts); no ids, usernames, IPs or secrets; authenticated |
| Request log content | `server.go:246-269` | Method, path (no query), status, bytes, duration, remote; headers are never logged |
| Secrets at rest | `daemon/internal/secret/store.go`, `daemon/internal/store/persist.go:161-193` | AES-256-GCM with account id as AAD; 0600 temp file + atomic rename; 0700 directory; tests assert 0600 and no plaintext |
| Secret in memory | `daemon/internal/account/secret.go:20-33`, `login_service.go:163-164` | `Secret` redacts under `%v`/`%#v`/JSON/slog; plaintext copy is `clear`ed after use |
| API keys | `daemon/internal/store/users_keys.go:213-249` | 96-bit `crypto/rand` secret, only SHA-256 stored, returned once, list exposes a prefix only; no route verifies them, so they are not an auth path |
| TLS pin | `daemon/internal/account/tls.go:48-70` | One client only, TLS ≥ 1.2, leaf SHA-256 compared with `hmac.Equal`, fail-closed, malformed pin fails startup, only the fingerprint (public config) appears in errors; all other HTTPS keeps default verification |
| Update endpoint | `daemon/internal/api/update.go:47-103` | Authenticated, read-only GitHub `GET`, 10 s timeout, 1 MiB cap, no token sent, no execution path; `os/exec` exists only in the unwired supervisor (`supervisor.go:282`, no shell) |
| Process execution | `daemon/internal/supervisor/supervisor.go:72-84,282-283` | `exec.CommandContext` with an args array and an env allowlist; no shell, no `os.Environ()` (prior F1 fixed) |
| Default binds | `daemon/cmd/ajs-accountd/main.go:39`, `daemon/internal/runtime/nodes.go:534`, `daemon/internal/gate/server.go:50`, `emulator/internal/config/config.go:60-66` | accountd `127.0.0.1:2261`, every gate `127.0.0.1:<port>`, gate default `127.0.0.1:0`, emulator SOCKS/HTTP `127.0.0.1` |
| Container identity | `daemon/Dockerfile:26-33` | Dedicated `ajs` user, `/data` owned by it, loopback `AJS_LISTEN` |
| Repo working tree | whole tree | No `.env`, `*.key`, `*.pem`; the only `Bearer` literals are documented placeholders (`local-test-token`); no private-key bodies (the two PEM hits are mbedTLS header strings in the vendor strings dump); no CN mobile numbers or personal e-mails in tracked text; `.git/config` has no remote |

## Checks run

All static checks were run with this lane's `grep` tool over the working tree (equivalent to
`grep -n`); no shell was used.

| # | Pattern | Scope | Result |
|---|---|---|---|
| 1 | `\b1[3-9][0-9]{9}\b\|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}` | `research/ docs/ deploy/ .github/ README.md PLAN.md` | Only `test@example.invalid`, the vendor support address in the fetched ToS HTML, and a curl sample address; no mobile number |
| 2 | `-----BEGIN\|AKIA[0-9A-Z]{16}\|ghp_[A-Za-z0-9]{20,}\|eyJ[A-Za-z0-9_-]{10,}\|Bearer [A-Za-z0-9._-]{12,}` | whole tree, gitignore disabled | Only `local-test-token` in READMEs and the two PEM header literals in `research/30-linux-static/strings/ajiasu.strings.txt:314,316` |
| 3 | `(?i)(password\|passwd\|pwd\|token\|secret\|apikey\|api_key)\s*[:=]\s*["']?[A-Za-z0-9!@#$%^&*_.\-]{6,}` | `research/**/*.{md,json,txt,csv,sh,py}` | Only test/placeholder values (`p%40ss`, `test`, `lab-token`, `lab-connect`) |
| 4 | `pprof\|debug/\|expvar` | `daemon emulator dashboard deploy .github scripts` | No Go code; one research recommendation only |
| 5 | `r\.Host\|X-Forwarded-For\|Forwarded` | `daemon/internal/api` | No matches (S3) |
| 6 | `@sha256\|cosign\|docker trust\|DOCKER_CONTENT_TRUST\|notation\|sigstore` | `deploy .github scripts` | No matches (S7) |
| 7 | `InsecureSkipVerify\|tls\.Config` | `daemon emulator dashboard deploy` | Exactly one `InsecureSkipVerify`, scoped to the pin client (`tls.go:59`) |
| 8 | `(password\|token\|secret\|apikey)\s*[:=]\s*"[^"]{8,}"` | `daemon emulator dashboard/src deploy .github` | Test placeholders only; the one non-test constant is the published OpenAJS signing value (`emulator/internal/api/sign.go:23`, documented as public-domain) |
| 9 | `os/exec\|exec\.Command\|update\.sh\|self-update` | `daemon dashboard deploy` | `os/exec` only in `internal/supervisor` (unwired, no shell); no auto-update code |
| 10 | `localStorage\|sessionStorage\|VITE_PANEL_TOKEN` | `dashboard deploy .github` | Token lives in a module variable (`api.ts:34`), never in storage; other storage keys are UI preferences |
| 11 | `06ka4z0` (vendor literal) | whole tree | Appears in the strings evidence (`research/90-verification/evidence/C15-xsig-secret-search.log:6,10,35,39`) and in reports that classify its role as `[UNKNOWN]`; **not** treated as a credential, and not reproduced here |
| 12 | `.git/logs/HEAD` tail | `.git` | Last commit `2eee11a`, subject `feat(panel): add release update check`; `.git/HEAD` → `refs/heads/panel-design`; `.git/config` has no remote |

## [UNVERIFIED] register

| Item | Why not run | Exact command to settle it |
|---|---|---|
| Working tree clean at `2eee11a` | no git binary in this lane | `git -C D:/Project/LovePlusSpeedToProxy status --porcelain` |
| Commit-content secret scan | no git binary; `research/90-verification/evidence/C16-hygiene.log:9-10` predates HEAD and records no pattern | `git log -p --all -S 'AJS_MASTER_KEY=' -S 'AJS_ADMIN_TOKEN=' -S 'password' -- . ':!research/30-linux-static/strings'` |
| Live scratch file modes (S13) | ssh read returns text, not metadata | `ssh hmc 'stat -c "%a %U:%G %n" /srv/dwgxhomecloud/scratch/ajs-live-20261023/* /srv/dwgxhomecloud/scratch/ajs-multimodule/.env'` |
| `update.sh` under Debian dash (S8) | no shell in this lane | `ssh hmc 'sh -c "set -euo pipefail"; echo exit=$?'` |
| Runtime token/bundle contents (S2, S5) | no container was started | `curl -s http://127.0.0.1:2261/v1/accounts -H 'Authorization: Bearer change-me'` and `curl -s http://127.0.0.1:18081/assets/ -o /dev/null` |
| Live services (accountd 2261, dashboard 18081/18082, lab 2262) | out of scope; not touched | — |

## Coverage

Reviewed end to end: `daemon/internal/api/{server,update,accounts,ops,nodes_leases,users_keys}.go`,
`daemon/internal/account/{tls,control,login_service,secret,session}.go`,
`daemon/internal/{secret,store,resin,publisher,runtime,gate,supervisor}` (secret/persist/key
handling, logging, URL construction, process spawning), `daemon/cmd/ajs-accountd/main.go`,
`daemon/Dockerfile`, `emulator/cmd/ajsproxy/main.go`, `emulator/internal/{api,config,proxy}`,
`dashboard/{Dockerfile,vite.config.ts,compose.yml}`, `dashboard/src/lib/{api,types}.ts`,
`dashboard/src/pages/Settings.tsx`, `deploy/{update.sh,compose.yml,compose.release.yml,.env.example,README.md}`,
`.github/workflows/release.yml`, `.gitignore`, `.git/config`, `docs/PANEL.md`, the root and component
READMEs, and the `research/` reports/evidence that record live runs. Not reviewed: vendor binaries,
`research/**/pseudocode`, and the `scripts/m1` lab tool.

Redaction: this report contains no password, login token, connect password, session key, phone
number, e-mail address, device id or certificate fingerprint. The only literal values quoted are the
documented placeholders `change-me` and `local-test-token`.
