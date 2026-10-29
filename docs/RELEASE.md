# Release procedure

Status: current for `v0.1.0-pre.3`, published 2026-10-29 from commit `5479a3a` (tag object
`753122c`). The previous published release is `v0.1.0-pre.2` on `d80fee7`; `v0.1.0-pre` on
`fd949ce` is the release before it. This page defines the version scheme, the gates that must
pass, how the artifacts are built and verified, and how to roll back. It contains no checksum
values: a checksum is only meaningful for a specific build, so it is generated and verified at
build time, never copied into prose.

## Version scheme

- Releases are semver: `vMAJOR.MINOR.PATCH`, with a hyphenated pre-release suffix. The first
  public pre-release was **`v0.1.0-pre`**; the current pre-release is **`v0.1.0-pre.3`**,
  published 2026-10-29 from commit `5479a3a`. The previous published pre-release is
  **`v0.1.0-pre.2`**, published 2026-10-29 from commit `d80fee7`. All three sort before
  `v0.1.0`, and each `-pre.N` sorts after `-pre.(N-1)`.
- A pre-release is not a stable release: no compatibility promise beyond the contract in
  `docs/openapi.json`, no `:latest` movement, no "latest release" claim.
- The eight existing tags (`v0.1.0-archive` … `v0.7.0-live`) are **milestone checkpoints** in
  the project's own history, not semver releases of the public line. They are local and
  unpushed. `v0.1.0-pre`, `v0.1.0-pre.2` and `v0.1.0-pre.3` are the only tags on `origin`. Do not
  present the milestones as releases, and do not infer ordering from them: semver compares
  pre-release identifiers lexically, so ordering `v0.1.0-pre` against names like
  `v0.1.0-archive` or `v0.7.0-live` is meaningless. The public line starts at `v0.1.0-pre`.
- `docs/openapi.json` and `daemon/internal/api/server.go` both say `0.1.0`. That is the
  fallback (`api.DefaultVersion`), not the release identity: release builds inject the tag and
  the commit into the binary (`deploy/package.sh` passes `-X main.buildVersion=<version>` and
  `-X main.buildCommitValue=<commit>`; `daemon/Dockerfile` takes the same two values as the
  `AJS_VERSION`/`AJS_COMMIT` build args). The published `v0.1.0-pre.3` Windows binary prints
  `ajs-accountd v0.1.0-pre.3 (5479a3a)` for `ajs-accountd -version`; the published
  `v0.1.0-pre.2` binary prints `ajs-accountd v0.1.0-pre.2
  (d80fee76af2103052069a7278338d808b8aa8d6c)`; `/healthz` reports the version but not the
  commit. A `go run`, `go test` or otherwise unstamped build still reports `0.1.0`, and its
  commit field is the toolchain VCS revision or `unknown`.
- The assets already published for `v0.1.0-pre` were built **before** either injection existed
  and were not rebuilt, so those binaries still report `0.1.0` in `/healthz` and print no
  commit. For those published assets the release identity remains the **tag plus
  `MANIFEST.txt`**, not `/healthz`.

## Repository state at this release

```bash
git remote -v                           # origin https://github.com/dwgx/AIJIASU2API.git
git ls-remote --symref origin HEAD      # ref: refs/heads/main
git rev-parse --short 'v0.1.0-pre.3^{}' # 5479a3a, the tagged commit
git tag --list 'v*' | wc -l             # 11: eight milestones plus three prereleases
git ls-remote --tags origin             # v0.1.0-pre, v0.1.0-pre.2, v0.1.0-pre.3
git status --porcelain                  # docs edits after the tag do not move it
```

These are the observed values on 2026-10-29. The tag count is **11**: eight local milestone tags
plus the three published prereleases, `v0.1.0-pre`, `v0.1.0-pre.2` and `v0.1.0-pre.3`.

The published state, as observed:

- `origin` is `https://github.com/dwgx/AIJIASU2API.git`; the default branch is `main`.
- `v0.1.0-pre` is an annotated tag (tagger date 2026-10-27T20:25:37Z) on commit `fd949ce`,
  pushed to `origin`. It remains the first published release and does not move.
- `v0.1.0-pre.2` is an annotated tag object `95f7930` on commit `d80fee7`, pushed to `origin`.
  It is the previous published release and does not move.
- `v0.1.0-pre.3` is an annotated tag object `753122c` on commit `5479a3a`, pushed to `origin`.
  It is the current published release. `git ls-remote --tags origin` lists all three tags (tag
  object plus `^{}` dereference).
- `main` may be ahead of a published tag. `main` may move; a tag does not. Only the tag is the
  release identity, and later commits are not part of that release.
- The GitHub Release for `v0.1.0-pre.3` is a **prerelease**, `immutable: true`, with six assets,
  published 2026-10-29T05:45:41Z:
  <https://github.com/dwgx/AIJIASU2API/releases/tag/v0.1.0-pre.3>. The earlier releases
  `v0.1.0-pre.2` and `v0.1.0-pre` remain prereleases with their six assets.
- The `release` workflow pushed `ghcr.io/dwgx/ajs-accountd:v0.1.0-pre.3` (digest
  `sha256:d6c1c293…`) and `ghcr.io/dwgx/ajs-dashboard:v0.1.0-pre.3` (digest
  `sha256:9c9cd4cf…`); both attestations verify, and the `:latest` promotion steps were skipped
  (`:latest` does not exist). The `v0.1.0-pre` and `v0.1.0-pre.2` images remain as published.
- The project page <https://dwgx.github.io/AIJIASU2API/> is published from `main`.

Tag only after every gate below is green, then push; the push is what runs the workflows.
`v0.1.0-pre.3` was cut that way and is now published:

```bash
git tag -a v0.1.0-pre.3 -m "v0.1.0-pre.3: third public pre-release" 5479a3a
git tag -n --list 'v0.1.0-pre.3'
git push origin v0.1.0-pre.3        # done; tag object 753122c
```

`v0.1.0-pre.2` was cut the same way on `d80fee7` (`-m "v0.1.0-pre.2: second public pre-release"`),
producing tag object `95f7930`; it does not move. `v0.1.0-pre` was cut on `fd949ce`
(`-m "v0.1.0-pre: first public pre-release"`) and remains the first published release; do not
move it.

Both `release.yml` and `package.yml` fire on a pushed `v*` tag:

- A **final** tag (`v0.1.0`, no `-`) pushes `ghcr.io/<owner>/ajs-accountd:<tag>` and
  `ghcr.io/<owner>/ajs-dashboard:<tag>`, then moves `:latest` for both with
  `docker buildx imagetools create`, which aliases the already-pushed manifest rather than
  rebuilding.
- A tag **containing `-`** (for example `v0.1.0-pre.3`) pushes only the `:<tag>` images and
  leaves `:latest` on the last final release. The two promotion steps are gated on
  `if: ${{ !contains(github.ref_name, '-') }}`.
- `package.yml` marks the release it creates with
  `prerelease: ${{ contains(github.ref_name, '-') }}`: a `-` tag creates a prerelease, a final
  tag a normal release, and it attaches the six artifacts either way. `release.yml` never
  creates a release; it only updates notes after `package.yml` publishes.
- The accountd image is built with `build-args: AJS_VERSION=${{ github.ref_name }}` and
  `AJS_COMMIT=${{ github.sha }}`, so `/healthz` and `ajs-accountd -version` inside a `:<tag>`
  image name that tag and commit. The dashboard image takes only `VITE_USE_MOCK=false` and
  `VITE_API_BASE=`. The `:latest` promotion aliases the already-built `:<tag>` manifest, so the
  alias carries the same stamped identity. The published `v0.1.0-pre` images were built without
  these args and report `0.1.0` (their contents were not pulled and inspected); the published
  `v0.1.0-pre.2` accountd image was built with the tag and commit.

## Attestations, immutability and draft-first publishing

`v0.1.0-pre.3` is the first release cut and published with this procedure. It is now observed:
the release is `immutable: true`, the six assets carry build provenance, the release attestation
verifies, and both image digests are attested.

- `package.yml` signs a build-provenance statement for exactly the six release files with
  `actions/attest-build-provenance` (`id-token: write`, `attestations: write`). It creates the
  GitHub release as a **draft**, attaches all six assets, and only then publishes it with
  `gh release edit "$TAG" --draft=false`.
- `release.yml` attests each pushed image **by digest** with `push-to-registry: true`, never
  creates a release and never attaches assets. It waits up to 10 minutes (`gh release view`) for
  the draft, then updates the notes with `gh release edit --notes-file`; if the release never
  appears, the job fails.
- Draft-first is the only order an immutable release accepts: assets are locked at publication,
  so a release published before its assets exist could never accept them. Title and notes stay
  editable after publication.
- The repository's immutable-releases setting is enabled
  (`gh api repos/dwgx/AIJIASU2API/immutable-releases --jq .enabled` → `true`). A release
  published while it is enabled locks the tag and assets and automatically carries a **release
  attestation**, which `gh release verify-asset` checks.
- The `release` workflow needed one retry: attempt 1 failed at the accountd GHCR push with
  `unknown blob`, attempt 2 succeeded on the same tag and commit. That is a transient registry
  transport error, not a code or pipeline defect; the pushed digests were attested and verified.
- `v0.1.0-pre.2` and earlier were published by the previous flow (the image workflow created the
  release): they have no build provenance and no release attestation, and the API reports
  `"immutable": false` for `v0.1.0-pre.2`. The strict verifier fails closed for them by design.

Workflow state for the published releases:

| Workflow | Ref | Result |
|---|---|---|
| `ci` | tag `v0.1.0-pre` (`fd949ce`) | success (historical) |
| `package` | tag `v0.1.0-pre` (`fd949ce`) | success; six assets attached (historical) |
| `release` | tag `v0.1.0-pre` (`fd949ce`) | success; both `:v0.1.0-pre` images pushed, `:latest` steps skipped (historical) |
| `pages` | `main` | success on the published commit; the site answers `200` (historical) |
| `ci` | tag `v0.1.0-pre.2` (`d80fee7`) | success — run 40817170300 |
| `package` | tag `v0.1.0-pre.2` (`d80fee7`) | success — run 40817170289; six assets attached |
| `release` | tag `v0.1.0-pre.2` (`d80fee7`) | success — run 40817170280; both `:v0.1.0-pre.2` images pushed, `:latest` skipped |
| `ci` | tag `v0.1.0-pre.3` (`5479a3a`) | success — run 40824870654 |
| `package` | tag `v0.1.0-pre.3` (`5479a3a`) | success — run 40824870543; six assets attached, draft published |
| `release` | tag `v0.1.0-pre.3` (`5479a3a`) | attempt 1 failed at the accountd GHCR push (`unknown blob`) — run 40824870647; attempt 2 success; both image digests attested, `:latest` skipped |

One earlier `package` run on the `v0.1.0-pre` tag at `8847db6` failed; that tag now points at
`fd949ce` and the historical rows above are its published state. A tag may be moved while it is
still unpublished; once published it is immutable, because the assets and images are bound to the
commit they were built from. `v0.1.0-pre.3` is locked by GitHub (`immutable: true`); neither it
nor the two earlier prereleases may be moved.

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
| Packaging | `SOURCE_DATE_EPOCH=0 bash deploy/package.sh --version v0.1.0-pre.3 --out /tmp/ajs-pkg` then `sha256sum -c` and `./ajs-accountd-linux-amd64 -version` | reproducible artifact set whose binary prints the stamped tag and commit |
| Secret scan | `git grep -nE 'BEGIN (RSA|EC|OPENSSH) PRIVATE KEY|AJS_(ADMIN_TOKEN|MASTER_KEY|EPAY_KEY|STRIPE_WEBHOOK_SECRET)=[^[:space:]]'` | no committed credentials |

Notes that matter:

- The last **recorded** root `PASS` for `check.sh` on the release host is 2026-10-26 and
  predates AJS-421's three added assertions. It was **not** recorded as re-run on the release
  host for `v0.1.0-pre`, `v0.1.0-pre.2` or `v0.1.0-pre.3`: the green `verify.yml` run executes
  the same script on a disposable GitHub runner, which is evidence for the kit but not a
  release-host `PASS`. Run it on the host and record the result in `deploy/production/README.md`
  before tagging. A `--dev` run is not a production gate.
- The Go floor is now `go 1.26.10` in all three modules and the builder image is pinned by
  digest. `govulncheck@v1.9.0` under that toolchain reported **0 vulnerabilities** for the
  daemon, and `verify.yml` runs it. This is a verified result at this commit; re-run it on the
  commit you tag.
- The dashboard suite is timing-sensitive under load (release audit R9). Run it serial and
  unloaded; a failure under parallel audit load is not a pass.
- GitHub Actions ran for all three published tags (table above): `ci`, `package` and `release`
  are green on `v0.1.0-pre.3`, with one retry of the `release` workflow after a transient GHCR
  `unknown blob` error at the accountd push. Still `[UNVERIFIED]`: pulling any GHCR image from
  this workstation (no Docker daemon, and a direct registry manifest request failed here: HTTP
  400 with an anonymous token, HTTP 401 with the local GitHub token), and `deploy/update.sh` /
  `deploy/compose.release.yml` against the published images. The pushes themselves are
  evidenced by the `release` job logs, and both `v0.1.0-pre.3` image attestations were verified
  with `gh attestation verify oci://…`.

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

The six assets are attached to the published `v0.1.0-pre.3` release. That published set was
verified against `SHA256SUMS`: **5/5 entries OK**, with `MANIFEST.txt` recording commit
`5479a3a` and a clean worktree. The six assets published for `v0.1.0-pre.2` were verified
**5/5 entries OK** on 2026-10-29. The six assets published for `v0.1.0-pre` were **not rebuilt**
and remain as published. The `ajs` CLI is source-only and is not among the assets.

For the previous `v0.1.0-pre` release, the assets were downloaded and checked on 2026-10-29 with
`sha256sum -c SHA256SUMS` (GNU coreutils 8.32): **5/5 entries OK**, and `MANIFEST.txt` in that set
records `commit: fd949ce…`, `worktree: clean`, `source_date_epoch: 1793132727`. Re-verify
against the files you actually hold; the values are not copied into this page.

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
- Version stamping (AJS-506/AJS-510): `package.sh` injects `-X main.buildVersion=<version>` and
  `-X main.buildCommitValue=<commit>`; `release.yml` passes `AJS_VERSION`/`AJS_COMMIT` to the
  accountd image. The published `v0.1.0-pre.3` Windows binary prints
  `ajs-accountd v0.1.0-pre.3 (5479a3a)`; the published `v0.1.0-pre.2` binary prints
  `ajs-accountd v0.1.0-pre.2 (d80fee76af2103052069a7278338d808b8aa8d6c)`, while the
  `v0.1.0-pre` assets and images predate the injection and still report `0.1.0`.
- Redistribution notices: at this commit `LICENSE`, `NOTICE` and `THIRD-PARTY-NOTICES.md` are
  enforced inside `dashboard-dist.tar.gz` and `deploy-kit.tar.gz` and listed in `MANIFEST.txt`;
  the three bare binaries ship without adjacent notice files. Verify the built set before
  calling it license-complete; the two container images are built by `release.yml` and their
  contents have not been inspected here.
- Attestations and immutability are exercised for `v0.1.0-pre.3`: the six-file provenance, the
  per-digest image attestations, the draft-first publish and the immutable release all ran and
  verify. The `release` workflow needed one retry after a transient GHCR `unknown blob` error at
  the accountd push; attempt 2 succeeded on the same tag and commit. `v0.1.0-pre.2` has none of
  these (`"immutable": false`), and the strict verifier fails closed for it.
- The compose/GHCR path is unexercised: `:v0.1.0-pre`, `:v0.1.0-pre.2` and `:v0.1.0-pre.3`
  images were pushed by the `release` workflow, and the `v0.1.0-pre.3` digests and attestations
  were verified, but no pull, `update.sh` run or compose render has been performed against them.
  Pulling any of these tags from this workstation is `[UNVERIFIED]` (no Docker daemon).

## Rollback

Release rollback is a redeploy of the previous artifact set, not a git operation:

1. Keep the previous six files and `MANIFEST.txt` until the new release has survived a soak.
2. Stop the daemon; restore the previous binary and the pre-upgrade state files.
3. Restore the previous nginx/stream configs, `nginx -t`, reload.
4. Start the daemon; check `/readyz` and one authenticated scoped-key read.
5. Do not move a published tag: `v0.1.0-pre.3` (tag object `753122c` on `5479a3a`) is locked by
   GitHub (`immutable: true`); `v0.1.0-pre.2` (`95f7930` on `d80fee7`) and `v0.1.0-pre` are
   immutable in practice because the assets and images are bound to the commits they were built
   from. If this release is wrong, cut a new `-pre` tag and a new release instead of rewriting
   any tag.

Data is never rolled back by deleting the new state tree; leave it for inspection.

## Pointers

- <https://github.com/dwgx/AIJIASU2API/releases/tag/v0.1.0-pre.3> — the current published
  prerelease
- <https://github.com/dwgx/AIJIASU2API/releases/tag/v0.1.0-pre.2> — the previous published
  prerelease
- <https://github.com/dwgx/AIJIASU2API/releases/tag/v0.1.0-pre> — the first published prerelease
- [`docs/OPERATIONS.md`](OPERATIONS.md) — install, health, backup, rotation
- [`docs/DEPLOYMENT.md`](DEPLOYMENT.md) — install, verification, update, rollback
- [`docs/releases/v0.1.0-pre.3.md`](releases/v0.1.0-pre.3.md) — current release notes
- [`docs/releases/v0.1.0-pre.2.md`](releases/v0.1.0-pre.2.md) — previous release notes, historical
- [`docs/releases/v0.1.0-pre.md`](releases/v0.1.0-pre.md) — first release notes, historical
- [`deploy/packaging.md`](../deploy/packaging.md) — artifact build details
- [`SECURITY.md`](../SECURITY.md) — disclosure policy
