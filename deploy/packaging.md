# Packaging (`ajs-accountd` + dashboard)

`deploy/package.sh` produces the release artifacts for Windows, macOS and Linux without Docker
and without CGO. It is the artifact half of the packaging gap: the daemon already cross-compiles,
so this script pins the targets, flags, checksums and manifest, and the tag workflow runs the
same command.

```bash
# from the repository root
bash deploy/package.sh --version 0.1.0 --out dist
(cd dist && sha256sum -c SHA256SUMS)
```

The script writes six release files into `--out`:

| Artifact | Target | Notes |
|---|---|---|
| `ajs-accountd-linux-amd64` | `GOOS=linux GOARCH=amd64` | `CGO_ENABLED=0`; statically linked |
| `ajs-accountd-windows-amd64.exe` | `GOOS=windows GOARCH=amd64` | `CGO_ENABLED=0`; no C runtime; no service wrapper |
| `ajs-accountd-darwin-arm64` | `GOOS=darwin GOARCH=arm64` | `CGO_ENABLED=0`; links system `libSystem` only; unsigned/not notarized |
| `dashboard-dist.tar.gz` | live static Vite bundle | extracted `index.html` + `assets/` + `ajs-build.json` |
| `MANIFEST.txt` | — | version, commit, toolchain, flags, artifact sizes/hashes |
| `SHA256SUMS` | — | `sha256sum -c` format covering the other five files |

`dashboard-dist.tar.gz` is the **existing** dashboard bundle, not a rebuild: the script does
not run `npm ci` or contact the network. It refuses any bundle that has no `ajs-build.json` or
whose manifest is not a live, token-free build (`mode: live`, `mock: false`,
`panelToken: false`). Build the live bundle first with
`bash deploy/production/build-bundles.sh`, or pass `--build-dashboard` to run that exact recipe;
`--dashboard-dist <dir>` packages a prebuilt live bundle elsewhere.

In addition to the six release files, the script writes `deploy-kit.tar.gz`: an LF-normalized
copy of the `deploy/` tree under a top-level `deploy/` directory. It contains the nginx
configuration, systemd unit, nftables tables, compose files, update/check scripts and the
`deploy/production` kit, so extracting it and running `sudo -E bash deploy/production/check.sh`
(root is required) works the same way as in a checkout. The deploy kit is an operator
convenience, not one of the six release assets; its size and SHA-256 are recorded in
`MANIFEST.txt`, which `SHA256SUMS` covers in turn.

The output directory is build output: use a path outside the tree (for example
`--out /tmp/ajs-pkg`) or keep it untracked. Only the script, the doc and the workflow belong in
version control; the binaries, `dashboard-dist.tar.gz`, `deploy-kit.tar.gz`, `MANIFEST.txt` and
`SHA256SUMS` do not.

## Prerequisites

- Go 1.26 or newer in `PATH` (`daemon/go.mod` requires `go 1.26.0`). The script sets
  `GOTOOLCHAIN=local`, so it never downloads a toolchain.
- A warm Go module cache, or network access once: `daemon/go.sum` is committed and pins
  `golang.org/x/crypto` (plus `golang.org/x/sys` indirect), and every build runs with
  `-mod=readonly`, which overrides Go's automatic `-mod=vendor`. On a cold cache run
  `go mod download` in `daemon/` once with `GOMODCACHE` writable and `GOPROXY` reachable; an
  air-gapped host needs a warm `GOMODCACHE` (a `vendor/` directory alone is not enough), because
  `GOPROXY=off` fails with `module lookup disabled by GOPROXY=off`. `emulator/` and `sdk/go/`
  have no external dependencies and need neither.
- Node 24 / npm 11 only when building the dashboard (`--build-dashboard`, or a missing
  `dashboard/dist` with `dashboard/node_modules` present). Run `npm ci` in `dashboard/`
  once from the committed `package-lock.json`; the script never installs dependencies. The
  default reference build is documented in [`../dashboard/README.md`](../dashboard/README.md).
- `tar`, and either `sha256sum` (Linux, Git for Windows) or `shasum` (macOS).
- Git is optional; without it the version falls back to `0.0.0-dev` and the manifest records
  `commit: unknown`.
- No Docker, no vendor account, no vendor host. Network access is limited to the one-time Go
  module download above and the `npm ci` in `dashboard/` that installs the build toolchain.

The daemon module replaces `ajsproxy` with `../emulator`, so package from a complete checkout
where `daemon/` and `emulator/` are siblings.

## Options

| Option | Default | Meaning |
|---|---|---|
| `--version <version>` | `git describe --tags --always --dirty`, else `0.0.0-dev` | version recorded in `MANIFEST.txt` |
| `--out <dir>` | `<repo>/dist` | output directory; created if absent |
| `--dashboard-dist <dir>` | `<repo>/dashboard/dist` | prebuilt live, token-free bundle to tar |
| `--build-dashboard` | off | run `deploy/production/build-bundles.sh` (live recipe) before packaging |
| `-h`, `--help` | — | usage |

If the default `dashboard/dist/index.html` is missing and `dashboard/node_modules` exists, the
script builds the live bundle with `deploy/production/build-bundles.sh` automatically. An
explicit `--dashboard-dist` is never replaced by a build: if that path has no `index.html`, the
script exits with an error. If neither the default bundle nor `node_modules` is present it exits
with the exact `npm ci`/build command instead of installing dependencies implicitly.

## Mock-mode guard

`VITE_USE_MOCK` is mock-first at runtime: anything except the exact string `false` selects the
in-browser demo data. A production build therefore fails closed unless `VITE_USE_MOCK=false`;
the only demo recipe is `npm run build:mock` (`vite build --mode mock`). Every successful build
emits `ajs-build.json`, and both `deploy/production/build-bundles.sh` and `deploy/package.sh`
refuse a bundle whose manifest is not `mode: live`, `mock: false`, `panelToken: false`.
`deploy/package.sh --build-dashboard` always uses the single live recipe. A `VITE_PANEL_TOKEN`
value in the build environment aborts the build in `vite.config.ts`: Vite would inline the bearer
into public JavaScript.

## Build flags and reproducibility

Each target is built from `daemon/` with:

```bash
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> GOTOOLCHAIN=local \
  go build -trimpath -buildvcs=false -mod=readonly \
  -ldflags="-s -w -buildid=" -o <artifact> ./cmd/ajs-accountd
```

- `CGO_ENABLED=0` — no C toolchain and no third-party C libraries. The Linux binary is fully
  static; the macOS binary still links the system `libSystem`, as every macOS process does.
  This is what makes the three targets build from any host.
- `-trimpath` — removes absolute source paths from the binary.
- `-buildvcs=false` — no VCS stamping (the commit is recorded in `MANIFEST.txt` instead, and a
  dirty worktree cannot silently change the build metadata).
- `-mod=readonly` — never rewrites `go.mod`/`go.sum` while packaging.
- `GOTOOLCHAIN=local` — never downloads a toolchain mid-build.
- `-ldflags="-s -w -buildid="` — strips the symbol table and DWARF data and zeroes the Go build
  ID.
- `SOURCE_DATE_EPOCH` — defaults to the HEAD commit time (or the current time outside Git). It
  is recorded as `source_date_epoch`/`built_utc` and used as the normalized mtime for every file
  in `dashboard-dist.tar.gz` and `deploy-kit.tar.gz`.

Both tar archives are built from staged copies with all mtimes set to the epoch, a
`LC_ALL=C`-sorted file list, and (on GNU tar) `--owner=0 --group=0 --numeric-owner --format=ustar`.

**Reproducibility boundary:** on the same host tar implementation, the same Go toolchain, the
same clean source tree and the same `SOURCE_DATE_EPOCH` produce byte-identical artifacts. A
dirty tracked worktree is recorded as `worktree: dirty` and may change the binaries; a different
Go patch release changes them too, and GNU tar and bsdtar do not emit identical archive bytes for
the same files. `MANIFEST.txt` and `SHA256SUMS` are authoritative for the build that produced
them. To compare two runs on one host:

```bash
bash deploy/package.sh --version 0.1.0 --out /tmp/ajs-a
bash deploy/package.sh --version 0.1.0 --out /tmp/ajs-b
(cd /tmp/ajs-a && sha256sum -c SHA256SUMS)
(cd /tmp/ajs-b && sha256sum -c SHA256SUMS)
diff <(cd /tmp/ajs-a && sha256sum ajs-accountd-* dashboard-dist.tar.gz MANIFEST.txt) \
     <(cd /tmp/ajs-b && sha256sum ajs-accountd-* dashboard-dist.tar.gz MANIFEST.txt)
```

`SOURCE_DATE_EPOCH=0` pins the timestamp explicitly when Git metadata is unavailable.

## Line endings

Every text file in every artifact is LF-only, and the repository enforces that at the Git
layer too. [`.gitattributes`](../.gitattributes) starts with `* text=auto eol=lf` and spells out
the important extensions (`*.nft`, `*.conf`, `*.service`, `*.sh`, `*.md`, `*.yml`, `*.go`, …).
Git therefore stores LF in the index, checks it out as LF even when `core.autocrlf=true`, and
`git archive` exports LF as well. Without those attributes a Windows checkout converts `.nft`
and nginx `.conf` files to CRLF, and `nft -c` rejects the firewall before evaluating a rule
(`syntax error, unexpected CRLF line terminators` / `unexpected junk`).

`deploy/package.sh` does not trust the checkout either. Both archives are staged from the
working tree, then every text file is rewritten to LF before the tar is created: the dashboard
bundle in `dashboard-dist.tar.gz` and the whole `deploy/` tree in `deploy-kit.tar.gz`. The
normalizer detects text with `grep -I` (binaries are skipped), uses byte-oriented `tr -d '\r'`
(portable across Git Bash and Linux), preserves file modes so scripts stay executable, and only
then normalizes mtimes. This is the archive-time guarantee: a package built on a Windows
checkout with `core.autocrlf=true` and one built on a Linux checkout contain the same LF text.

To verify a package directly:

```bash
tmp=$(mktemp -d)
tar -xzf dist/deploy-kit.tar.gz -C "$tmp"
! grep -rlU $'\r' "$tmp" --include='*.nft' --include='*.sh' \
    --include='*.conf' --include='*.service' --include='*.md'
nft -c -f "$tmp/deploy/production/firewall/ajs-production.nft"   # root; syntax only
```

If you export the tree with Git yourself, `git archive` already honours the attributes; add
`git -c core.autocrlf=false archive` as a second guard on a Windows host.

## Verify the checksums

`SHA256SUMS` covers the three binaries, `dashboard-dist.tar.gz` and `MANIFEST.txt`:

```bash
cd dist
sha256sum -c SHA256SUMS          # Linux, Git for Windows
shasum -a 256 -c SHA256SUMS      # macOS
```

`deploy-kit.tar.gz` is not listed in `SHA256SUMS` directly: its digest is in `MANIFEST.txt`,
which `SHA256SUMS` covers, so a passing check authenticates the deploy kit transitively.

On Windows PowerShell, compare per file if `sha256sum` is not installed:

```powershell
Get-FileHash .\ajs-accountd-windows-amd64.exe -Algorithm SHA256
Get-Content .\SHA256SUMS
```

`MANIFEST.txt` also carries each artifact's size and digest. If it and `SHA256SUMS` disagree,
treat the directory as tampered and rebuild; do not trust either file alone.

`MANIFEST.txt` fields:

```text
version              the --version value
commit               HEAD commit, or unknown outside Git
worktree             clean or dirty (tracked files only)
source_date_epoch    Unix timestamp used for reproducible metadata
built_utc            that timestamp as UTC ISO-8601
go                   the exact `go version` used
dashboard_version    the `version` field in dashboard/package.json
dashboard_mode       live (package.sh refuses a mock bundle)
dashboard_manifest   ajs-build.json size and SHA-256
build_flags          the common flags, including -ldflags
targets              artifact -> GOOS/GOARCH/CGO for the three binaries
dashboard_dist       the bundle directory that was tarred
deploy_kit           name, byte size and SHA-256 of the auxiliary deploy-kit.tar.gz
artifacts            name, byte size and SHA-256 for the four built payload files
```

## Run from an unpacked artifact

The release is a binary and a static bundle, not an installer. There is no Windows service,
launchd unit or systemd unit in the tree; supervise the process with the platform's own service
manager if it must survive logout or reboot.

```bash
mkdir -p ajs-pkg && cd ajs-pkg
tar -xzf dashboard-dist.tar.gz -C dashboard
chmod +x ajs-accountd-linux-amd64        # or ajs-accountd-darwin-arm64
```

### Daemon: Linux / macOS

```bash
export AJS_LISTEN=127.0.0.1:2261
export AJS_STATE_DIR="$PWD/data"
export AJS_ADMIN_TOKEN="$(openssl rand -hex 32)"
./ajs-accountd-linux-amd64
```

For a loopback-only lab run without bearer auth, replace the token with
`AJS_ALLOW_INSECURE=1`. Startup refuses `AJS_ALLOW_INSECURE=1` unless `AJS_LISTEN` is
`127.0.0.1`, `::1` or `localhost`.

### Daemon: Windows (PowerShell)

```powershell
$env:AJS_LISTEN = "127.0.0.1:2261"
$env:AJS_STATE_DIR = "$PWD\data"
$env:AJS_ADMIN_TOKEN = -join (1..32 | ForEach-Object { '{0:x2}' -f (Get-Random -Maximum 256) })
.\ajs-accountd-windows-amd64.exe
```

The binary is unsigned; SmartScreen may ask for **More info → Run anyway**. The macOS arm64
binary is unsigned and not notarized; if it was downloaded through a browser, clear the
quarantine bit first:

```bash
xattr -d com.apple.quarantine ./ajs-accountd-darwin-arm64
```

`ajs-accountd-darwin-arm64` is Apple Silicon only. Intel Macs need a `GOARCH=amd64` build; the
release matrix does not ship one.

### State directory

`AJS_STATE_DIR` defaults to `./data` (created `0700`) and holds:

- `state.json` — versioned JSON snapshot, written every 30 s and on shutdown;
- `secrets.json` — sealed account passwords, only when `AJS_MASTER_KEY` is set;
- `device-secrets.json` — sealed device identities, only when the device pool is on;
- `schedule-fire.log` — schedule firing history.

`AJS_STATE_FILE` overrides the snapshot path. `AJS_MASTER_KEY` is a 32-byte key as 64 hex
characters (or base64); without it the daemon logs `secrets_disabled` and discards account
passwords, and the sealed device pool is off. Point `AJS_STATE_DIR` outside the extracted
directory for a real deployment, back it up, and treat it as credential material.

### Token and health

`AJS_ADMIN_TOKEN` is required unless `AJS_ALLOW_INSECURE=1`; it must be at least 32 characters
and must not be one of the documented placeholders. Generate it with
`openssl rand -hex 32`. `/healthz` is unauthenticated:

```bash
curl -s http://127.0.0.1:2261/healthz
curl -s -H "Authorization: Bearer $AJS_ADMIN_TOKEN" http://127.0.0.1:2261/v1/accounts
```

### TLS for the admin API

The plaintext listener stays on `AJS_LISTEN` (loopback by default). Setting **both**
`AJS_TLS_CERT` and `AJS_TLS_KEY` adds an HTTPS listener on `AJS_TLS_LISTEN` (default
`127.0.0.1:2262`) serving the same routes with the same bearer auth:

```bash
AJS_TLS_CERT="$PWD/fullchain.pem" \
AJS_TLS_KEY="$PWD/privkey.pem" \
AJS_TLS_LISTEN=127.0.0.1:2262 \
AJS_ADMIN_TOKEN="$(openssl rand -hex 32)" \
./ajs-accountd-linux-amd64
```

TLS 1.2 or newer, ECDHE-AEAD suites, HSTS `max-age=31536000` on every response. A partial
configuration — one half of the pair, `AJS_TLS_LISTEN` without the pair, or the same address as
`AJS_LISTEN` — aborts startup before either listener binds. `AJS_ALLOW_INSECURE=1` still refuses
a non-loopback bind on the HTTPS listener.

### Dashboard

The bundle is static files; serve them with any web server on the origin you put in
`AJS_CORS_ORIGINS` (the default allows `http://127.0.0.1:18081` and `http://127.0.0.1:5173`):

```bash
cd dashboard
python3 -m http.server 18081 --bind 127.0.0.1
```

`18081` is the static bundle's origin only. The API stays on `AJS_LISTEN`
(`127.0.0.1:2261` by default): point API clients and SDK examples at `2261`, and keep the bundle
origin in `AJS_CORS_ORIGINS`.

`python3 -m http.server` has no SPA fallback, so a hard refresh on a client-side route returns
404. For nginx, keep the same fallback the image uses:

```nginx
location / { try_files $uri $uri/ /index.html; }
```

The bundle mode is fixed at build time. A demo build (`npm run build:mock`) produces the mock
console; it needs no daemon and contacts no network, but it is not packageable as a release
artifact. A plain `npm run build` fails closed unless `VITE_USE_MOCK=false`. For a live bundle use
the single production recipe:

```bash
bash deploy/production/build-bundles.sh --out /tmp/ajs-bundles
bash deploy/package.sh --version 0.1.0 --out dist --dashboard-dist /tmp/ajs-bundles/portal
# or: bash deploy/package.sh --version 0.1.0 --out dist --build-dashboard
```

A live build without `VITE_PANEL_TOKEN` still works: the dashboard prompts for the token on the
first `401` and keeps it in `sessionStorage` for that tab. Do not bake a real admin token into a
bundle that is reachable by anyone but you — `VITE_*` values are readable in the shipped
JavaScript, and `vite.config.ts` refuses a build with `VITE_PANEL_TOKEN` set. `AJS_CORS_ORIGINS`
must contain the exact browser origin serving the bundle.

## Tag workflow

[`../.github/workflows/package.yml`](../.github/workflows/package.yml) runs on a `v*` tag (and
manually via `workflow_dispatch`). Its `verify` job calls the reusable
[`verify.yml`](../.github/workflows/verify.yml) — the Go (daemon + emulator), dashboard and SDK
suites — and the `package` job has `needs: verify`, so no artifact is built, uploaded or attached
unless every suite passes. After that it installs Go 1.26 and Node 24, runs `npm ci`, builds the
dashboard with the single live recipe, runs this script, uploads the six release files as a
workflow artifact, and attaches them to the GitHub Release for the tag. `deploy-kit.tar.gz` is
produced alongside them for local operator use but is not part of that six-file release set. It
does not build or push container images; that is
[`release.yml`](../.github/workflows/release.yml)'s job, which calls the same `verify` job before
it logs in to GHCR, so a red suite cannot push a tag image or move `:latest`. `release.yml`
attaches no release assets, so `SHA256SUMS` always describes the complete asset set.

Run the same suites locally before tagging:

```bash
(cd daemon    && go vet ./... && go test -race -count=1 ./...)
(cd emulator  && go vet ./... && go test -race -count=1 ./...)
(cd sdk/go    && go vet ./... && go test -race -count=1 ./...)
(cd dashboard && npm ci && npm test -- --pool=forks --maxWorkers=1)
(cd sdk/ts    && npm ci && npm run typecheck && npm test)
```

The dashboard flags are load-bearing: `npm test` alone is red on a loaded host (7 failed /
493 passed on a 24-core machine) because `findBy*` waits default to 1 s; with
`--pool=forks --maxWorkers=1` the same suite passes 500/500 in about 160 s. `go test -race` needs
cgo and a C compiler (`gcc`); a Go-only host can still run `go test ./...` without `-race`.

No workflow has run in this repository yet: the tree has no remote, and the only tag
(`v0.1.0-archive`) is local. The script is exercised locally as recorded below.

## Local verification

```bash
bash -n deploy/package.sh
python3 -c 'import yaml; yaml.safe_load(open(".github/workflows/package.yml"))'
bash deploy/package.sh --version 0.0.0-test --out /tmp/ajs-pkg --build-dashboard
(cd /tmp/ajs-pkg && sha256sum -c SHA256SUMS)
tar -xOf /tmp/ajs-pkg/dashboard-dist.tar.gz ./ajs-build.json
tar -tzf /tmp/ajs-pkg/deploy-kit.tar.gz | head
mkdir -p /tmp/ajs-kit && tar -xzf /tmp/ajs-pkg/deploy-kit.tar.gz -C /tmp/ajs-kit
! grep -rlU $'\r' /tmp/ajs-kit --include='*.nft' --include='*.sh' \
    --include='*.conf' --include='*.service' --include='*.md'
```

Recorded 2026-10-26 on the packaging workstation (Git Bash with Go 1.26.7, and WSL Ubuntu 24.04
using the same Go cross-compiler): `bash -n` and the YAML parse exit 0; the exact command above
exits 0 and writes all six release files plus `deploy-kit.tar.gz`; every SHA-256 entry verifies;
`dashboard-dist.tar.gz` contains `ajs-build.json` with `mode: live`, `mock: false`,
`panelToken: false`; `MANIFEST.txt` records `dashboard_mode: live`; `file` identifies the outputs
as a static x86-64 ELF, a PE32+ x86-64 executable and an arm64 Mach-O; two consecutive runs with
the tree unchanged produced identical SHA-256 digests for the dashboard bundle and all three
binaries. A mock `dashboard/dist`
is refused with `dashboard bundle is not a live, token-free build`. The deploy-kit extraction has
no CR byte in any `.nft`, `.sh`, `.conf`, `.service` or `.md` file, and `nft -c -f` accepts its
`deploy/production/firewall/ajs-production.nft` (WSL nft 1.0.9, root). The Linux binary answered
`{"status":"ok",...}` on `/healthz` when started from the unpacked directory with
`AJS_ALLOW_INSECURE=1` and a temporary `AJS_STATE_DIR`.
