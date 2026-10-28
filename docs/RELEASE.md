# Release procedure

Status: current for `v0.1.0-pre`, published 2026-10-27 and re-verified 2026-10-29. This page
defines the version scheme, the gates that must pass, how the artifacts are built and verified,
and how to roll back. It contains no checksum values: a checksum is only meaningful for a
specific build, so it is generated and verified at build time, never copied into prose.

## Version scheme

- Releases are semver: `vMAJOR.MINOR.PATCH`, with `-pre` for a pre-release. The first public
  pre-release is **`v0.1.0-pre`**.
- A pre-release is not a stable release: no compatibility promise beyond the contract in
  `docs/openapi.json`, no `:latest` movement, no "latest release" claim.
- The eight existing tags (`v0.1.0-archive` … `v0.7.0-live`) are **milestone checkpoints** in
  the project's own history, not semver releases of the public line. They are local and
  unpushed; `v0.1.0-pre` is the only tag on `origin`. Do not present them as releases, and do
  not infer ordering from them: semver compares pre-release identifiers lexically, so ordering
  `v0.1.0-pre` against names like `v0.1.0-archive` or `v0.7.0-live` is meaningless. The public
  line starts at `v0.1.0-pre`.
- `docs/openapi.json` and `daemon/internal/api/server.go` both say `0.1.0`. The binary has no
  build-time version injection (`-ldflags` carries no `-X`), so a `v0.1.0-pre` binary still
  reports `0.1.0`. The release identity is the **tag plus `MANIFEST.txt`**, not `/healthz`.

## Repository state at this release

```bash
git remote -v                       # origin https://github.com/dwgx/AIJIASU2API.git
git ls-remote --symref origin HEAD  # ref: refs/heads/main
git rev-parse --short HEAD          # e98e34e on main, 2026-10-29
git tag --list 'v*' | wc -l         # 9: v0.1.0-pre pushed, 8 milestone tags local
git ls-remote --tags origin         # only v0.1.0-pre (tag object + ^{} deref)
git status --porcelain              # must be empty before tagging
```

These are the observed values on 2026-10-29; re-run them on the commit you actually tag.

The published state, as observed:

- `origin` is `https://github.com/dwgx/AIJIASU2API.git`; the default branch is `main`.
- `v0.1.0-pre` is an annotated tag (tagger date 2026-10-27T20:25:37Z) on commit `fd949ce`,
  pushed to `origin`. The tag is the release identity.
- `main` (`e98e34e` at this writing) is **ahead of** the tag. `main` may move; the tag does not.
  Only the tag is the release identity, and later commits are not part of this release.
- The GitHub Release is a **prerelease** with six assets:
  <https://github.com/dwgx/AIJIASU2API/releases/tag/v0.1.0-pre>.
- The `release` workflow pushed `ghcr.io/dwgx/ajs-accountd:v0.1.0-pre` and
  `ghcr.io/dwgx/ajs-dashboard:v0.1.0-pre`; the `:latest` promotion steps were skipped.
- The project page <https://dwgx.github.io/AIJIASU2API/> is published from `main`.

Tag only after every gate below is green, then push; the push is what runs the workflows.
`v0.1.0-pre` was cut that way:

```bash
git tag -a v0.1.0-pre -m "v0.1.0-pre: first public pre-release" fd949ce
git tag -n --list 'v0.1.0-pre'
git push origin v0.1.0-pre          # already done for v0.1.0-pre
```

Both `release.yml` and `package.yml` fire on a pushed `v*` tag:

- A **final** tag (`v0.1.0`, no `-`) pushes `ghcr.io/<owner>/ajs-accountd:<tag>` and
  `ghcr.io/<owner>/ajs-dashboard:<tag>`, then moves `:latest` for both with
  `docker buildx imagetools create`, which aliases the already-pushed manifest rather than
  rebuilding.
- A tag **containing `-`** (for example `v0.1.0-pre`) pushes only the `:<tag>` images and leaves
  `:latest` on the last final release. The two promotion steps are gated on
  `if: ${{ !contains(github.ref_name, '-') }}`.
- Both workflows mark the GitHub Release with
  `prerelease: ${{ contains(github.ref_name, '-') }}`: a `-` tag creates a prerelease, a final
  tag a normal release. `package.yml` attaches the six artifacts to that release either way.

Observed for this release (2026-10-27, re-checked 2026-10-29):

| Workflow | Ref | Result |
|---|---|---|
| `ci` | tag `v0.1.0-pre` (`fd949ce`) | success |
| `package` | tag `v0.1.0-pre` (`fd949ce`) | success; six assets attached to the release |
| `release` | tag `v0.1.0-pre` (`fd949ce`) | success; both `:v0.1.0-pre` images pushed, `:latest` steps skipped |
| `pages` | `main` (`e98e34e`) | success; the site answers `200` |

One earlier `package` run on the same tag at `8847db6` failed; the tag now points at `fd949ce`
and the runs above are the current state. A tag may be moved while it is still unpublished;
once published it is immutable, because the assets and images are bound to the commit they were
built from.

## Release gates

Run from the repository root. All must pass on the release host; `verify.yml` re-runs the same
suite on every push (including `check.sh` as root in a disposable container, with a negative
control), but a CI runner is not the release host.

| Gate | Command | What it proves |
|---|---|---|
| Go toolchain | `go version` ≥ **1.26.10** | patched stdlib for the advisories in release audit R1 |
| Vulnerability scan | `cd daemon && GOTOOLCHAIN=go1.26.10 go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` | no reachable known stdlib/module vulns; also a `verify.yml` step |
| Daemon + emulator | `cd daemon && go vet ./... && go test -race -count=1 ./...`; same in `emulator/` | unit/integration suites |
| SDKs | `cd sdk/go && go test -race ./...`; `cd sdk/ts && npx tsc --noEmit && npm test` | client suites |
| Dashboard | `cd dashboard && npm ci && npm run verify:lockfile && npm run docs:gen -- --check && npm test -- --pool=forks --maxWorkers=1` | lockfile provenance, generated-docs drift, the suite in 54 test files |
| Live build | `VITE_USE_MOCK=false VITE_API_BASE= npm run build` | a live, token-free bundle |
| Contract mirror | `cmp docs/openapi.json daemon/internal/api/openapi.json` | one contract, no drift |
| Production kit | `sudo -E bash deploy/production/check.sh` | root Linux gate: nginx, systemd, firewall, TLS, CSP |
| Packaging | `SOURCE_DATE_EPOCH=0 bash deploy/package.sh --version v0.1.0-pre --out /tmp/ajs-pkg` then `sha256sum -c` | reproducible artifact set |
| Secret scan | `git grep -nE 'BEGIN (RSA|EC|OPENSSH) PRIVATE KEY|AJS_(ADMIN_TOKEN|MASTER_KEY|EPAY_KEY|STRIPE_WEBHOOK_SECRET)=[^[:space:]]'` | no committed credentials |

Notes that matter:

- The last **recorded** root `PASS` for `check.sh` on the release host is 2026-10-26 and
  predates AJS-421's three added assertions. It was **not** re-run on the release host for
  `v0.1.0-pre`: the green `verify.yml` run executes the same script on a disposable GitHub
  runner, which is evidence for the kit but not a release-host `PASS`. Run it on the host and
  record the result in `deploy/production/README.md` before the next tag. A `--dev` run is not a
  production gate.
- The Go floor is now `go 1.26.10` in all three modules and the builder image is pinned by
  digest. `govulncheck@v1.9.0` under that toolchain reported **0 vulnerabilities** for the
  daemon, and `verify.yml` runs it. This is a verified result at this commit; re-run it on the
  commit you tag.
- The dashboard suite is timing-sensitive under load (release audit R9). Run it serial and
  unloaded; a failure under parallel audit load is not a pass.
- GitHub Actions ran for this tag (table above). Still `[UNVERIFIED]`: pulling either GHCR image
  from this workstation (no Docker daemon, and a direct registry manifest request failed here:
  HTTP 400 with an anonymous token, HTTP 401 with the local GitHub token), and
  `deploy/update.sh` / `deploy/compose.release.yml` against the published images. The push itself
  is evidenced by the `release` job log.

## Artifacts and checksums

`deploy/package.sh` writes six release files plus a deploy kit:

| File | Covered by `SHA256SUMS` |
|---|---|
| `ajs-accountd-linux-amd64` | yes |
| `ajs-accountd-windows-amd64.exe` | yes |
| `ajs-accountd-darwin-arm64` | yes |
| `dashboard-dist.tar.gz` | yes |
| `MANIFEST.txt` | yes |
| `SHA256SUMS` | — (the manifest of the others) |
| `deploy-kit.tar.gz` | transitively, through its digest in `MANIFEST.txt` |

The GitHub Release attaches the six files — the three binaries, `dashboard-dist.tar.gz`,
`MANIFEST.txt` and `SHA256SUMS`. `deploy-kit.tar.gz` is built and digested by `package.sh` but
is **not** an attached asset; the digest in `MANIFEST.txt` only lets you compare a kit built
locally from the same commit.

The published assets were downloaded and checked on 2026-10-29 with `sha256sum -c SHA256SUMS`
(GNU coreutils 8.32): **5/5 entries OK**, and `MANIFEST.txt` in that set records
`commit: fd949ce…`, `worktree: clean`, `source_date_epoch: 1793132727`. Re-verify against the
files you actually hold; the values are not copied into this page.

Verify on the receiving host:

```bash
sha256sum -c SHA256SUMS            # or: shasum -a 256 -c SHA256SUMS
```

Do not publish checksums in a document: the values differ per build and per
`SOURCE_DATE_EPOCH`. A published checksum that is not regenerated from the exact artifact set
is worse than none.

Known packaging limitations for this release:

- Release audit R8 is fixed: `MANIFEST.txt` records a stable `dashboard_source` (never a temp
  path), so two `--build-dashboard` runs are byte-identical (verified 2026-10-28).
- Redistribution notices: at this commit `LICENSE`, `NOTICE` and `THIRD-PARTY-NOTICES.md` are
  enforced inside `dashboard-dist.tar.gz` and `deploy-kit.tar.gz` and listed in `MANIFEST.txt`;
  the three bare binaries ship without adjacent notice files. Verify the built set before
  calling it license-complete; the two container images are built by `release.yml` and their
  contents have not been inspected here.
- The compose/GHCR path is unexercised: the two `:v0.1.0-pre` images were pushed by the
  `release` workflow, but no pull, `update.sh` run or compose render has been performed against
  them. Pulling `ghcr.io/dwgx/ajs-accountd:v0.1.0-pre` is `[UNVERIFIED]`.

## Rollback

Release rollback is a redeploy of the previous artifact set, not a git operation:

1. Keep the previous six files and `MANIFEST.txt` until the new release has survived a soak.
2. Stop the daemon; restore the previous binary and the pre-upgrade state files.
3. Restore the previous nginx/stream configs, `nginx -t`, reload.
4. Start the daemon; check `/readyz` and one authenticated scoped-key read.
5. If the tag itself is wrong, delete it on both sides (`git tag -d v0.1.0-pre &&
   git push --delete origin v0.1.0-pre`) and delete the GitHub Release and its package versions.
   A published tag is immutable in practice: the assets and images are bound to the commit they
   were built from, so moving it silently changes what a downloader gets. Cut a new `-pre` tag
   instead.

Data is never rolled back by deleting the new state tree; leave it for inspection.

## Pointers

- <https://github.com/dwgx/AIJIASU2API/releases/tag/v0.1.0-pre> — the published prerelease
- [`docs/OPERATIONS.md`](OPERATIONS.md) — install, health, backup, rotation
- [`docs/releases/v0.1.0-pre.md`](releases/v0.1.0-pre.md) — release notes
- [`deploy/packaging.md`](../deploy/packaging.md) — artifact build details
- [`SECURITY.md`](../SECURITY.md) — disclosure policy
