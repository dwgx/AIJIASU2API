# Deployment

Status: current for `v0.1.0-pre.2`, published 2026-10-29. 简体中文：
[`DEPLOYMENT.zh-CN.md`](DEPLOYMENT.zh-CN.md).

Three shapes are supported and they are not interchangeable: a hardened Linux/systemd install,
a Docker Compose stack from the published images, and a Windows executable. Pick the shape that
matches the host. Verification differs in the default each tool chooses — the table in
[Verification tools](#verification-tools) is the authority, not habit.

The live demo host is **not** an example of this procedure. It runs `ajs-accountd-61625b0`,
which predates this release, from a scratch path; do not infer the current release from it. See
[`OPERATIONS.md`](OPERATIONS.md#live-host-as-built).

## Choose a mode

| Mode | Host | Tree | Install | Update | Rollback |
|---|---|---|---|---|---|
| Linux + systemd (HMC) | Debian/Ubuntu, systemd ≥ 247 | [`deploy/production/`](../deploy/production/README.md) | binary + unit + nginx | manual, verified swap | previous binary + pre-upgrade state |
| Docker Compose | Docker Engine 29+, Compose v2.24+ | [`deploy/`](../deploy/README.md) | `update.sh` pulls GHCR images | `update.sh` (verifies, digest-pins) | `update.sh --rollback` |
| Windows EXE | Windows 10/11 or Server, PowerShell 5.1+ | [`deploy/windows/`](../deploy/windows/) | `install.ps1` | `update.ps1` (auto-restores) | `update.ps1 -Rollback` |

All three verification paths use the GitHub CLI (`gh`). The Windows asset is unsigned (no
Authenticode), and none of the release assets is code-signed; authenticity comes from the
attestations described below, not from a platform signature.

## What "verified" means

A release is identified by its tag and the `MANIFEST.txt` shipped with it, never by the version
string a process prints. Four layers exist, and each answers a different question:

| Layer | Question it answers | Where it lives |
|---|---|---|
| `SHA256SUMS` | Did these bytes arrive intact, and are they the files in this release? | six-asset release |
| `MANIFEST.txt` | Which tag, commit, worktree, toolchain and flags produced them? | six-asset release |
| Build-provenance attestation | Which workflow and commit built this file or image? | `gh attestation verify` |
| Immutable release + release attestation | Can the tag or any asset change after publication? | GitHub repository setting |

### Checksums are integrity, not authenticity

`SHA256SUMS` and the artifacts are downloaded from the same place over the same trust path. An
attacker who can replace an asset can replace the checksum file in the same step, so a passing
`sha256sum -c` proves only that the files match the set you received — it detects corruption,
truncated downloads, mirror drift and a wrong version. It does not prove who built the file or
that it came from this repository.

Authenticity needs a signature or attestation bound to an identity you trust (here: the GitHub
Actions OIDC identity of `dwgx/AIJIASU2API`), plus a record that cannot be rewritten after
publication. Without that, you are trusting GitHub's TLS, the repository account, and the tag —
which may be exactly right for a lab, but it is a different claim from "verified".

### Immutable releases

The repository setting is enabled; check it yourself:

```bash
gh api repos/dwgx/AIJIASU2API/immutable-releases --jq .enabled   # true
```

For a release published while the setting is enabled, GitHub locks the assets and the tag: the
tag cannot be moved or deleted while the release exists, assets cannot be modified or deleted,
and a deleted release's tag name cannot be reused. The title and release notes remain editable.
Publishing an immutable release also generates a **release attestation** that binds the tag, the
commit and the assets.

The setting did not lock the release published before it: the API reports `v0.1.0-pre.2` as
mutable:

```bash
gh api repos/dwgx/AIJIASU2API/releases/tags/v0.1.0-pre.2 --jq .immutable   # false
```

So `v0.1.0-pre.2` is immutable by project policy only. Future releases are enforced by GitHub.

### Artifact attestations

`.github/workflows/package.yml` attests the six release files and
`.github/workflows/release.yml` attests each pushed image digest with
`actions/attest-build-provenance` (permissions `id-token: write` and `attestations: write`; the
image workflow also keeps `packages: write` to push). This applies to tags published **after**
those steps landed.

Verify a file or an image on the receiving host:

```bash
gh attestation verify ./ajs-accountd-windows-amd64.exe --repo dwgx/AIJIASU2API
gh attestation verify oci://ghcr.io/dwgx/ajs-accountd@sha256:<digest> --repo dwgx/AIJIASU2API
gh release verify-asset <tag> ./ajs-accountd-linux-amd64 --repo dwgx/AIJIASU2API
```

`gh release verify-asset` checks the release attestation, which only immutable releases have.

The current release predates all of this: at the `v0.1.0-pre.2` tag the release workflow contains
no attestation step (`git show v0.1.0-pre.2:.github/workflows/release.yml | grep -c attest` → `0`).
Verification therefore fails closed for it, as it should.

### Release pipeline: draft, then publish

A tag published after this pipeline lands is released in the only order an immutable release
accepts:

1. `package.yml` builds and attests the six files, creates the GitHub release as a **draft**,
   attaches all six assets, then publishes it with `gh release edit "$TAG" --draft=false`.
2. `release.yml` never creates a release and never attaches assets. It waits up to 10 minutes
   (`gh release view`) for that release, then updates its notes with
   `gh release edit --notes-file`; if the release never appears, the job fails.

The reason is immutability: assets are locked at publication, so a release published before its
assets exist could never accept them. Draft-first attaches everything, then publishes. Updating
the notes afterwards is still allowed because an immutable release keeps its title and notes
editable.

Consequence for verification: a tag cut after this pipeline lands is an immutable release, which
automatically carries a release attestation, so `gh release verify-asset` reports `verified` for
it. `v0.1.0-pre.2` and earlier were published by the previous flow (the image workflow created the
release) and have no release attestation.

### Digest pinning

A tag is a mutable pointer; a digest is the content address. For containers the pin is
`ghcr.io/dwgx/ajs-accountd@sha256:<64 hex>`, not `:v0.1.0-pre.2`.

- `deploy/update.sh` resolves each pulled image's digest, verifies the attestation **for that
  digest**, records both digests in `deploy/update-state.json`, and exports
  `AJS_ACCOUNTD_DIGEST` / `AJS_DASHBOARD_DIGEST` so the containers that start are the exact
  bytes that were verified.
- For a direct `docker compose` run, set those variables in `deploy/.env` (for example
  `AJS_ACCOUNTD_DIGEST=sha256:<64 hex>`); leave them unset to track the tag.
- Read the digest of an image you already have:
  `docker image inspect --format '{{index .RepoDigests 0}}' ghcr.io/dwgx/ajs-accountd:v0.1.0-pre.2`.
- Binaries are pinned by keeping the exact file together with its `MANIFEST.txt` and
  `SHA256SUMS`; there is no registry in that path.

### Verification tools

| Tool | Default | Strict | Opt-out | Exit codes |
|---|---|---|---|---|
| [`deploy/verify-release.sh`](../deploy/verify-release.sh) | build provenance required for all six assets; release attestation checked when present, reported `absent` otherwise | `--require-attestation` (release attestation mandatory) | `--allow-unverified` (attestations skipped; checksums and MANIFEST still enforced) | 0 verified, 1 failed check, 2 usage |
| [`deploy/windows/verify-release.ps1`](../deploy/windows/verify-release.ps1) | `SHA256SUMS` + `MANIFEST.txt` version + stamped `-version`, and both attestations required | `-RequireAttestation` (explicit; already the default) | `-AllowUnverified` (attestation failure becomes a warning; hashes are never skipped) | 0 verified, 1 failed, 2 usage/precondition |

`deploy/verify-release.sh` downloads the six assets with `gh release download` into a fresh
`mktemp -d` directory under `${TMPDIR:-/tmp}` (mode 700, printed as `dir:`), refuses `latest`, and
checks, in order: `SHA256SUMS` and that it covers every asset; `MANIFEST.txt` (`version` equals
the tag, `commit` is a 40-hex SHA equal to the commit at the tag, `worktree: clean`); then
attestations. An unresolvable tag commit is a failure: the manifest commit must be bound to the
tag. `--dir` is accepted only when it is owned by the current user and not group- or
world-writable. A missing `gh` is always a failure — the download needs it; a `gh` without
`attestation verify` is a failure unless `--allow-unverified` is given.
`--require-attestation` and `--allow-unverified` together are a usage error (exit 2).

The Windows verifier downloads only the Windows executable, `MANIFEST.txt` and `SHA256SUMS`, and
additionally runs the downloaded binary with `-version` and compares the stamped version. A
binary that cannot execute on the verifying host is reported `[UNVERIFIED]`.

## Linux + systemd (HMC)

The reviewed kit is [`deploy/production/`](../deploy/production/README.md); the compact walkthrough
below is the same procedure as [`OPERATIONS.md`](OPERATIONS.md#install-from-the-kit).

### Prerequisites

- Debian/Ubuntu x86_64, systemd 247 or newer (for `ProtectProc=invisible` / `ProcSubset=pid`),
  root or `sudo`.
- The release asset `ajs-accountd-linux-amd64`; `gh` and `sha256sum` (or `shasum`) on the host.
- nginx with `libnginx-mod-stream` if the public gate edge is used.
- A dedicated non-root account (`ajs`), `/etc/ajs` for configuration, `/var/lib/ajs` for state.

### Install

```bash
# 1. Verify the release; note the "dir:" path it prints.
(cd deploy && ./verify-release.sh --tag v0.1.0-pre.2 --allow-unverified)
# -> dir: /tmp/ajs-release-verify.Ab12Cd   (a fresh mktemp -d, mode 700)

# 2. Identity, directories, binary (as root).
useradd --system --user-group --home /var/lib/ajs --shell /usr/sbin/nologin ajs
install -d -m 0750 -o root -g ajs /etc/ajs
install -d -m 0750 -o root -g root /etc/ajs/tls
install -d -m 0700 -o ajs -g ajs /var/lib/ajs
install -m 0755 <dir>/ajs-accountd-linux-amd64 /usr/local/bin/ajs-accountd

# 3. Environment and unit.
install -m 0600 -o root -g root deploy/production/systemd/ajs-accountd.env.example \
  /etc/ajs/accountd.env
sudo -e /etc/ajs/accountd.env
install -m 0644 deploy/production/systemd/ajs-accountd.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now ajs-accountd

# 4. Health.
curl -fsS http://127.0.0.1:2261/healthz
```

Then follow steps 4–7 of the kit README for the dashboard bundles, nginx, TLS and operator
identities. The unit runs as `User=ajs` with `ProtectSystem=strict`, an empty capability set and
only `/var/lib/ajs` writable; `/etc/ajs` is read-only to the daemon.

### Verify

```bash
cd deploy
./verify-release.sh --tag <tag> --require-attestation   # future releases
./verify-release.sh --tag v0.1.0-pre.2 --allow-unverified  # current release
```

For a future release, `--require-attestation` is the strict form: it additionally requires the
release attestation that only an immutable release carries.

### Update

There is no `update.sh` for systemd hosts; `update.sh` is the Compose path. The swap is manual
and verified:

```bash
cd deploy
./verify-release.sh --tag <new> --require-attestation   # or --allow-unverified

systemctl stop ajs-accountd
cp -a /var/lib/ajs/state.json /var/lib/ajs/state.json.pre-<new>
cp -a /usr/local/bin/ajs-accountd /usr/local/bin/ajs-accountd.previous

install -m 0755 <verified-dir>/ajs-accountd-linux-amd64 /usr/local/bin/ajs-accountd
# Install any changed unit or vhost, then:
nginx -t && systemctl daemon-reload
systemctl start ajs-accountd
curl -fsS http://127.0.0.1:2261/readyz
```

### Health check

```bash
curl -fsS http://127.0.0.1:2261/healthz    # liveness, version and per-state counts
curl -fsS http://127.0.0.1:2261/readyz     # readiness: state write probe + integrations
systemctl status ajs-accountd
journalctl -u ajs-accountd -n 200 --no-pager
```

### Rollback

Stop the daemon, restore the previous binary **and** the pre-upgrade state files, restore the
previous nginx/stream configs, `nginx -t`, then start and check `/readyz`. The state snapshot is
versioned: this build writes v2 and a v1 binary rejects it, so restoring the backup is required,
not optional. Do not roll back by deleting the new state tree; keep it for inspection.

### Configuration preservation

Replacing `/usr/local/bin/ajs-accountd` touches no configuration: `/etc/ajs/accountd.env`,
`/etc/ajs/admin-operators`, the rendered hashes, TLS material and the nginx vhosts stay in place.
The state directory is also untouched by the swap — which is why the pre-upgrade copy is a
separate, deliberate step. Keep `AJS_MASTER_KEY` with every backup; without it the sealed values
in `secrets.json` are unrecoverable.

## Docker Compose

The stack is [`deploy/compose.yml`](../deploy/compose.yml) plus
[`deploy/compose.release.yml`](../deploy/compose.release.yml), which swaps the local builds for the
GHCR images.

### Prerequisites

- Docker Engine 29 or newer with the Compose plugin (Compose v2.24+ is the practical floor).
- Free loopback ports `2261` (daemon) and `18081` (dashboard).
- `deploy/.env` with `AJS_ADMIN_TOKEN` (32+ characters) and `AJS_MASTER_KEY` (64 hex
  characters); `AJS_GHCR_OWNER` and `AJS_IMAGE_TAG` select the image namespace and tag.
- `gh` for image-attestation verification.
- The repository is needed only for the compose files and `.env`, not for the release images.
- `deploy/.env.example` ships `AJS_IMAGE_TAG=latest`; `update.sh` refuses that floating alias
  unless `--allow-latest` is given, so pass `--tag` or set a release tag in `.env`.

### Install

```bash
cd deploy
cp .env.example .env
# edit .env: AJS_ADMIN_TOKEN, AJS_MASTER_KEY; set AJS_IMAGE_TAG to a release tag,
# or pass --tag to update.sh (the shipped value "latest" is refused without --allow-latest)

# First install from the published images. The current release predates attestations:
./update.sh --tag v0.1.0-pre.2 --allow-unverified

# Or run compose directly, without the script's verification and state file
# (set AJS_IMAGE_TAG to the release tag first; the shipped "latest" is an alias):
docker compose -f compose.yml -f compose.release.yml up -d --no-build
```

`update.sh` layers both compose files, pulls, verifies (unless opted out), writes
`update-state.json` (atomically, mode 0600, gitignored), starts the containers, and waits for both
health endpoints. On a first install the recorded previous image IDs are `none`, so `--rollback`
has nothing to restore.

### Verify

- **Images** — `./update.sh --tag <tag>` verifies both images by default before any container is
  switched. The manual equivalent is
  `gh attestation verify oci://ghcr.io/dwgx/ajs-accountd@<digest> --repo dwgx/AIJIASU2API`.
  The digest comes from `docker image inspect --format '{{index .RepoDigests 0}}' <ref>`.
- **Release assets** — `./verify-release.sh --tag <tag>` checks the six files, `MANIFEST.txt`
  and attestations. It verifies assets, not images; `update.sh` does not verify assets.
- **Digest pinning** — `update.sh` pins what it verified through `AJS_ACCOUNTD_DIGEST` /
  `AJS_DASHBOARD_DIGEST`; for a direct compose run, set them in `.env` or leave them unset.

### Update

```bash
cd deploy
./update.sh --tag <new>                    # verification is on by default
./update.sh --tag <new> --verify           # the explicit spelling of the default
./update.sh --tag v0.1.0-pre.2 --allow-unverified   # current release only
./update.sh --tag latest --allow-latest     # only if you deliberately want the alias
```

| Flag | Effect |
|---|---|
| `--tag <tag>` | release tag to deploy; required unless `AJS_IMAGE_TAG` holds a release tag |
| `--verify` | require image attestations (the default) |
| `--allow-unverified` | skip image attestations; digests are still resolved and recorded |
| `--allow-latest` | permit the floating `latest` alias for an update; rejected with `--rollback` |
| `--rollback` | restore the image IDs recorded before the last update |

A release tag is required: with no `--tag` and no `AJS_IMAGE_TAG` the script exits 1, and the
floating `latest` is refused unless `--allow-latest` is given. Because `.env.example` ships
`AJS_IMAGE_TAG=latest`, a bare `./update.sh` fails until you pass `--tag` or set a release tag in
`.env`. Tags are limited to `[A-Za-z0-9._-]`; anything else is a failure (exit 1).

Order matters and is enforced: `gh` capability check (before anything is pulled) → `compose pull`
→ resolve digests → verify both images → write `update-state.json` → `compose up -d --no-build`
with the digests exported → health checks. A failure before `compose up` leaves the running
deployment untouched; pulled images may remain in the local cache. A missing `gh`, or one without
`attestation verify`, stops the run before any pull and names `--allow-unverified` as the opt-out.

`--allow-unverified` is recorded in `update-state.json` together with the attestation scope; the
success summary prints `attestations: checksums verified; provenance skipped`, while a verified
run prints `attestations: verified (repo:…|owner:…)`. Neither skips digest resolution or the
health checks. `--verify` and `--allow-unverified` together are a usage error (exit 2).

### Health check

```bash
curl -fsS http://127.0.0.1:2261/healthz    # daemon
curl -fsS http://127.0.0.1:18081/healthz   # dashboard nginx, answers "ok"
docker compose -f compose.yml -f compose.release.yml ps
docker compose -f compose.yml -f compose.release.yml logs -f accountd
```

### Rollback

```bash
cd deploy
./update.sh --rollback
```

It restores the two image IDs recorded before the last update and runs the same health checks.
It refuses when `update-state.json` is missing or records `none`; it refuses
`--verify`/`--allow-unverified` and `--allow-latest` (exit 2) because nothing is downloaded.
Rollback is exactly one step: each update overwrites the state file, so going further back means
`./update.sh --tag <older> --allow-unverified` when that older tag also predates attestations.

### Configuration preservation

`deploy/.env` is never rewritten by `update.sh`; the named volume
`ajs-control-plane_accountd-data` holds `state.json` and `secrets.json` and survives
`compose up -d` and `compose down`. `docker compose down -v` **destroys** that volume. Back up
the volume before a major update and keep `AJS_MASTER_KEY` with the backup. `update-state.json`
is runtime state, written atomically with mode 0600 and gitignored; it records the tag, the
attestation scope, and the previous image IDs and digests for `--rollback`.

## Windows EXE

The scripts are in [`deploy/windows/`](../deploy/windows/): `install.ps1`, `update.ps1`,
`verify-release.ps1`, `run.ps1`, with the full manual in
[`deploy/windows/README.md`](../deploy/windows/README.md). They install the daemon only; the
dashboard is a separate static bundle or container.

### Prerequisites

- Windows 10/11 or Server x64, Windows PowerShell 5.1 or PowerShell 7.
- `gh` on `PATH` and authenticated (`gh auth login`) for downloads and attestation checks;
  without it the verifier exits 2 before downloading anything.
- An elevated shell to register the auto-start task and write under
  `C:\ProgramData\AIJIASU2API`.
- A loopback port for the daemon (default `127.0.0.1:2261`).
- No service wrapper: `ajs-accountd` does not implement the SCM dispatcher, so `sc.exe create`
  fails with error 1053. The installer registers a **scheduled task** at startup as `SYSTEM`
  instead.
- The binary is unsigned; SmartScreen may ask for **More info → Run anyway**. There is no
  Authenticode signature, and these scripts do not configure TLS or a reverse proxy.

### Install

```powershell
cd deploy\windows
.\install.ps1 -Tag v0.1.0-pre.2 -AllowUnverified -WhatIf   # plan only, writes nothing
.\install.ps1 -Tag v0.1.0-pre.2 -AllowUnverified            # verify, install, start, health
```

The layout under `-InstallDir` (default `C:\ProgramData\AIJIASU2API`) is below; the previous
binary and the update record appear after the first `update.ps1` run:

| Path | Content |
|---|---|
| `ajs-accountd.exe` | the verified binary, staged next to its destination and moved into place |
| `ajs-accountd.exe.previous` | previous binary, used by `update.ps1 -Rollback` |
| `run.ps1` | launcher used by the auto-start task |
| `ajs.env` | generated once and **never overwritten**; admin bearer and at-rest master key, ACL restricted to SYSTEM, Administrators and the installing account |
| `update-state.json` | last update/rollback record: tag, version, previous version, both SHA-256 values, health URL |
| `data\` | state directory, kept across installs and updates |
| `logs\` | one `<timestamp>.out.log`/`.err.log` pair per daemon start, last 10 kept |

Parameters: `-Tag`, `-Repo` (default `dwgx/AIJIASU2API`), `-InstallDir`, `-StateDir`, `-Listen`,
`-ServiceName` (default `AJSAccountd`), `-NoService`, `-RequireAttestation`,
`-AllowUnverified`, `-Force`, `-TimeoutSec`, `-WhatIf`. `-NoService` installs the files and
leaves the daemon stopped; `-WhatIf` prints the plan without downloading or writing; `-Force`
replaces the binary in place and still never overwrites `ajs.env`.

### Verify

```powershell
.\verify-release.ps1 -Tag <tag>                         # future releases
.\verify-release.ps1 -Tag v0.1.0-pre.2 -AllowUnverified  # current release
```

The checksum chain (`SHA256SUMS`), the `MANIFEST.txt` version and the binary's stamped
`-version` are always checked, and build provenance plus the release attestation are required by
default. `-AllowUnverified` is the only opt-out: it downgrades a failed or unavailable
attestation check to a warning and never skips a hash. `-RequireAttestation` is accepted for
explicitness (it is already the default) and cannot be combined with `-AllowUnverified` (exit 2).
`-PassThru` returns the verified path, version and SHA-256 and keeps the download directory for
the caller.

### Update

```powershell
.\update.ps1 -Tag <new>                            # future releases
.\update.ps1 -Tag v0.1.0-pre.2 -AllowUnverified    # current release
```

`update.ps1` verifies the release, stops the scheduled task and any running daemon, keeps the
current binary as `ajs-accountd.exe.previous`, stages and moves the new binary into place, starts
the daemon and waits for `/healthz` to report the new version. If that check fails it swaps the
previous binary back and restarts it. The config file and the state directory are never touched.
The last update is recorded in `<InstallDir>\update-state.json`.

Stopping is a hard stop: the daemon gets no graceful signal, so at most one snapshot interval of
state is lost. `update.ps1` stops any `ajs-accountd` process running from the install path, not
only the task, and waits for it to exit before swapping the binary.

### Health check

```powershell
Invoke-RestMethod http://127.0.0.1:2261/healthz
Get-ScheduledTask -TaskName AJSAccountd
Get-Content .\logs\*.log -Tail 50
.\run.ps1 -Smoke        # throwaway run: free loopback port, temp state, stopped after /healthz
```

### Rollback

```powershell
.\update.ps1 -Rollback
```

It swaps `ajs-accountd.exe.previous` back in, starts it and runs the same health check. Nothing
is downloaded and no verification is repeated: that binary was verified when it was installed.

### Configuration preservation

`ajs.env` is generated once and never overwritten, including with `-Force`; `data\` survives
installs and updates. The previous binary is kept next to the current one, so rollback needs no
network access. Keep a copy of `ajs.env` and `data\` off the host: the config holds the admin
bearer and the master key, and the state holds the sealed values those keys protect.

What was observed for these scripts: verify, `install.ps1 -NoService`, `update.ps1` without
auto-start, `-Rollback` and `run.ps1 -Smoke` under PowerShell 5.1 and 7 against `v0.1.0-pre.2`.
The elevated path — task registration, the SYSTEM launch, and the update-time health check with
automatic rollback — is a code path, not an observed run here: `[UNVERIFIED]` on this
workstation. Re-run it on the target host before relying on auto-start.

## Dashboard: Settings → Updates

The operator console never updates itself. **Settings → Updates** offers three read-only steps:

1. **Check** — `GET /v1/system/update`, a read-only GitHub release lookup. It is enabled only
   when the daemon runs with `AJS_UPDATE_REPO` set; otherwise the card says so.
2. **Copy verify command** — `cd deploy && ./verify-release.sh --tag <latest> --require-attestation`.
3. **Copy update command** — `cd deploy && ./update.sh --tag <latest> --verify`.

Both commands are copied for a human to run on the host; the dashboard executes neither. The card
states this: *"The dashboard never executes a root update. It checks the release, offers the
verify step and copies the host command — run it on the host."*

The check calls GitHub's `/releases/latest`, which excludes pre-releases. Both releases on this
repository are pre-releases, so when `AJS_UPDATE_REPO` points here the check answers
`502 UPSTREAM_UNAVAILABLE` and the two copy rows stay hidden; they appear once a final
(non-hyphen) release exists. The commands in this page work regardless.

There is no root update endpoint: no `POST /v1/system/update` exists, the daemon runs
unprivileged and cannot replace its own container or binary, and the static dashboard has no
host access. A true one-click update would require a privileged updater (a root agent, the Docker
socket, or a host API) and is a **non-goal** for this release: it would turn a panel compromise
into host root. Automatic update and rollback are not implemented.

One consequence worth knowing: the copied verify command is the strict form
(`--require-attestation`), and the current release cannot satisfy it. For `v0.1.0-pre.2`, run the
checksum path instead: `./verify-release.sh --tag v0.1.0-pre.2 --allow-unverified`, which still
enforces `SHA256SUMS` and `MANIFEST.txt`.

## Current release state

| Item | `v0.1.0-pre.2` | Future releases |
|---|---|---|
| Tag / commit | `v0.1.0-pre.2` on `d80fee7`, published 2026-10-29 | — |
| Assets | six files, GitHub prerelease; `:latest` not moved | six files |
| Immutable release | **no** (`"immutable": false`); repository setting enabled for future releases | yes |
| Build provenance | **none** (the workflow at the tag has no attestation step) | yes, for files and image digests |
| Release attestation | **none** | yes, generated by the immutable release |
| Release pipeline | previous flow: `release.yml` created the release | `package.yml` draft → publish; `release.yml` notes only |
| Linux verification | `--allow-unverified` (checksums + MANIFEST still enforced) | default or `--require-attestation` |
| Windows verification | `-AllowUnverified` required (attestations are enforced by default) | default, or `-RequireAttestation` explicitly |
| Live daemon | `ajs-accountd-61625b0`, older than this release; the live host is not upgraded by it | — |

Evidence for the table:

```bash
gh api repos/dwgx/AIJIASU2API/immutable-releases --jq .enabled            # true
gh api repos/dwgx/AIJIASU2API/releases/tags/v0.1.0-pre.2 --jq .immutable  # false
gh release view v0.1.0-pre.2 --json assets --jq '.assets | length'         # 6
git show v0.1.0-pre.2:.github/workflows/release.yml | grep -c attest       # 0
```

In short: `v0.1.0-pre.2` has no attestations, is not an immutable release, and is not what the
live host runs.

## Pointers

- [`DEPLOYMENT.zh-CN.md`](DEPLOYMENT.zh-CN.md) — this page in Simplified Chinese
- [`docs/OPERATIONS.md`](OPERATIONS.md) — install, health, backup, rotation, upgrade, rollback
- [`docs/RELEASE.md`](RELEASE.md) — gates, artefacts, checksums, tag procedure
- [`docs/VERSIONING.md`](VERSIONING.md) — SemVer, tag vs assets, `:latest`
- [`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md) — what is enforced, where, and what is not
- [`deploy/README.md`](../deploy/README.md) — compose stack, `.env`, TLS, backup
- [`deploy/production/README.md`](../deploy/production/README.md) — reviewed systemd/nginx kit
- [`deploy/windows/`](../deploy/windows/) — Windows install, update, verify, run scripts
- [`deploy/verify-release.sh`](../deploy/verify-release.sh) — release-asset verifier
- [`deploy/update.sh`](../deploy/update.sh) — compose update and rollback
