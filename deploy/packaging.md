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

The script writes six files into `--out`:

| Artifact | Target | Notes |
|---|---|---|
| `ajs-accountd-linux-amd64` | `GOOS=linux GOARCH=amd64` | `CGO_ENABLED=0`; statically linked |
| `ajs-accountd-windows-amd64.exe` | `GOOS=windows GOARCH=amd64` | `CGO_ENABLED=0`; no C runtime; no service wrapper |
| `ajs-accountd-darwin-arm64` | `GOOS=darwin GOARCH=arm64` | `CGO_ENABLED=0`; links system `libSystem` only; unsigned/not notarized |
| `dashboard-dist.tar.gz` | static Vite bundle | extracted `index.html` + `assets/` |
| `MANIFEST.txt` | — | version, commit, toolchain, flags, artifact sizes/hashes |
| `SHA256SUMS` | — | `sha256sum -c` format covering the other five files |

`dashboard-dist.tar.gz` is the **existing** `dashboard/dist` bundle, not a rebuild: the script
does not run `npm install` or contact the network. Build the dashboard first, or let the script
use an already-built `dashboard/dist`. `--build-dashboard` runs `npm run build`; `--dashboard-dist
<dir>` packages a bundle elsewhere.

The output directory is build output: use a path outside the tree (for example
`--out /tmp/ajs-pkg`) or keep it untracked. Only the script, the doc and the workflow belong in
version control; the binaries, `dashboard-dist.tar.gz`, `MANIFEST.txt` and `SHA256SUMS` do not.

## Prerequisites

- Go 1.26 or newer in `PATH` (`daemon/go.mod` requires `go 1.26.0`). The script sets
  `GOTOOLCHAIN=local`, so it never downloads a toolchain.
- Node 24 / npm 11 only when building the dashboard (`--build-dashboard`, or a missing
  `dashboard/dist` with `dashboard/node_modules` present). Run `npm install` in `dashboard/`
  once; the script never installs dependencies. The default reference build is documented in
  [`../dashboard/README.md`](../dashboard/README.md).
- `tar`, and either `sha256sum` (Linux, Git for Windows) or `shasum` (macOS).
- Git is optional; without it the version falls back to `0.0.0-dev` and the manifest records
  `commit: unknown`.
- No Docker, no vendor account, no vendor host, no network.

The daemon module replaces `ajsproxy` with `../emulator`, so package from a complete checkout
where `daemon/` and `emulator/` are siblings.

## Options

| Option | Default | Meaning |
|---|---|---|
| `--version <version>` | `git describe --tags --always --dirty`, else `0.0.0-dev` | version recorded in `MANIFEST.txt` |
| `--out <dir>` | `<repo>/dist` | output directory; created if absent |
| `--dashboard-dist <dir>` | `<repo>/dashboard/dist` | prebuilt bundle to tar |
| `--build-dashboard` | off | run `npm run build` in `dashboard/` before packaging |
| `-h`, `--help` | — | usage |

If the default `dashboard/dist/index.html` is missing and `dashboard/node_modules` exists, the
script builds the bundle automatically. An explicit `--dashboard-dist` is never replaced by a
build: if that path has no `index.html`, the script exits with an error. If neither the default
bundle nor `node_modules` is present it exits with the exact install/build command instead of
running `npm install` implicitly.

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
  in `dashboard-dist.tar.gz`.

The dashboard tar is built from a staged copy with all mtimes set to the epoch, a
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

## Verify the checksums

`SHA256SUMS` covers the three binaries, `dashboard-dist.tar.gz` and `MANIFEST.txt`:

```bash
cd dist
sha256sum -c SHA256SUMS          # Linux, Git for Windows
shasum -a 256 -c SHA256SUMS      # macOS
```

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
build_flags          the common flags, including -ldflags
targets              artifact -> GOOS/GOARCH/CGO for the three binaries
dashboard_dist       the bundle directory that was tarred
artifacts            name, byte size and SHA-256 for the five payload files
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

`python3 -m http.server` has no SPA fallback, so a hard refresh on a client-side route returns
404. For nginx, keep the same fallback the image uses:

```nginx
location / { try_files $uri $uri/ /index.html; }
```

The bundle mode is fixed at build time. The default `dashboard/dist` in this tree is the mock
build; it needs no daemon and contacts no network. For live mode, rebuild and repackage:

```bash
(cd dashboard && VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 npm run build)
bash deploy/package.sh --version 0.1.0 --out dist --dashboard-dist dashboard/dist
```

A live build without `VITE_PANEL_TOKEN` still works: the dashboard prompts for the token on the
first `401` and keeps it in `sessionStorage` for that tab. Do not bake a real admin token into a
bundle that is reachable by anyone but you — `VITE_*` values are readable in the shipped
JavaScript. `AJS_CORS_ORIGINS` must contain the exact browser origin serving the bundle.

## Tag workflow

[`../.github/workflows/package.yml`](../.github/workflows/package.yml) runs on a `v*` tag (and
manually via `workflow_dispatch`). It installs Go 1.26 and Node 24, builds the dashboard, runs
this script, uploads the six files as a workflow artifact, and attaches them to the GitHub
Release for the tag. It does not build or push container images; that is
[`release.yml`](../.github/workflows/release.yml)'s job, and `release.yml` attaches no release
assets, so `SHA256SUMS` always describes the complete asset set. The workflow has not run in this
repository yet because the tree has no remote and no tags; the script is exercised locally as
recorded below.

## Local verification

```bash
bash -n deploy/package.sh
python3 -c 'import yaml; yaml.safe_load(open(".github/workflows/package.yml"))'
bash deploy/package.sh --version 0.0.0-test --out /tmp/ajs-pkg
(cd /tmp/ajs-pkg && sha256sum -c SHA256SUMS)
tar -tzf /tmp/ajs-pkg/dashboard-dist.tar.gz | head
```

Recorded 2026-10-26 on the packaging workstation (Git Bash with Go 1.26.7, and WSL Ubuntu 24.04
using the same Go cross-compiler): `bash -n` and the YAML parse exit 0; the exact command above
exits 0 and writes all six files; `sha256sum -c SHA256SUMS` reports OK for all five covered
files; `file` identifies the outputs as a static x86-64 ELF, a PE32+ x86-64 executable and an
arm64 Mach-O; two consecutive runs produced identical SHA-256 digests for all five payload
files. The Linux binary answered `{"status":"ok",...}` on `/healthz` when started from the
unpacked directory with `AJS_ALLOW_INSECURE=1` and a temporary `AJS_STATE_DIR`.
