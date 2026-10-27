# Release procedure

Status: current for `v0.1.0-pre` (2026-10-28). This page defines the version scheme, the gates
that must pass, how the artifacts are built and verified, and how to roll back. It contains no
checksum values: a checksum is only meaningful for a specific build, so it is generated and
verified at build time, never copied into prose.

## Version scheme

- Releases are semver: `vMAJOR.MINOR.PATCH`, with `-pre` for a pre-release. The first public
  pre-release is **`v0.1.0-pre`**.
- A pre-release is not a stable release: no compatibility promise beyond the contract in
  `docs/openapi.json`, no `:latest` movement, no "latest release" claim.
- The eight existing tags (`v0.1.0-archive` … `v0.7.0-live`) are **milestone checkpoints** in
  the project's own history, not semver releases of the public line. They are local and
  unpushed. Do not present them as releases, and do not infer ordering from them: semver
  compares pre-release identifiers lexically, so ordering `v0.1.0-pre` against names like
  `v0.1.0-archive` or `v0.7.0-live` is meaningless. The public line starts at `v0.1.0-pre`.
- `docs/openapi.json` and `daemon/internal/api/server.go` both say `0.1.0`. The binary has no
  build-time version injection (`-ldflags` carries no `-X`), so a `v0.1.0-pre` binary still
  reports `0.1.0`. The release identity is the **tag plus `MANIFEST.txt`**, not `/healthz`.

## Repository state at this release

```bash
git rev-parse --short HEAD          # 901c75b on 2026-10-28
git tag --list 'v*' | wc -l         # 8, all local
git remote -v                       # empty: no remote
git status --porcelain              # must be empty before tagging
```

These are the observed values on 2026-10-28; re-run them on the commit you actually tag.

There is **no git remote**. Consequences, stated plainly:

- Nothing is pushed. No tag, branch or release exists on any forge.
- `.github/workflows/release.yml` and `package.yml` have never run and cannot run. Their
  trigger is a pushed `v*` tag.
- There is **no GitHub Release**, no GHCR image and no `:latest` tag. Do not claim one.
- `deploy/update.sh --tag …` and `deploy/compose.release.yml` have nothing to pull; the compose
  release path is `[UNVERIFIED]`.

Tag locally only after every gate below is green:

```bash
git tag -a v0.1.0-pre -m "v0.1.0-pre: first public pre-release"
git tag -n --list 'v0.1.0-pre'      # local only; do not push
```

If a remote is created later, the tag workflows already implement this split. Both
`release.yml` and `package.yml` fire on a pushed `v*` tag:

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

Execution is `[UNVERIFIED]` until a remote exists and the workflows actually run; review the
diff before the first push.

## Release gates

Run from the repository root. All must pass on the release host.

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

- The last **recorded** root `PASS` for `check.sh` is 2026-10-26 and predates AJS-421's three
  added assertions. It must be re-run on the release host and the result recorded in
  `deploy/production/README.md` before the tag. A `--dev` run is not a production gate.
- The Go floor is now `go 1.26.10` in all three modules and the builder image is pinned by
  digest. `govulncheck@v1.9.0` under that toolchain reported **0 vulnerabilities** for the
  daemon, and `verify.yml` runs it. This is a verified result at this commit; re-run it on the
  commit you tag.
- The dashboard suite is timing-sensitive under load (release audit R9). Run it serial and
  unloaded; a failure under parallel audit load is not a pass.
- GitHub Actions execution is `[UNVERIFIED]` because there is no remote.

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
  calling it license-complete; container images are built by the workflows that have never run.
- The compose/GHCR path is unexercised (no remote, no registry push).

## Rollback

Release rollback is a redeploy of the previous artifact set, not a git operation:

1. Keep the previous six files and `MANIFEST.txt` until the new release has survived a soak.
2. Stop the daemon; restore the previous binary and the pre-upgrade state files.
3. Restore the previous nginx/stream configs, `nginx -t`, reload.
4. Start the daemon; check `/readyz` and one authenticated scoped-key read.
5. If the tag itself is wrong, delete the **local** tag (`git tag -d v0.1.0-pre`). There is no
   remote tag to delete.

Data is never rolled back by deleting the new state tree; leave it for inspection.

## Pointers

- [`docs/OPERATIONS.md`](OPERATIONS.md) — install, health, backup, rotation
- [`docs/releases/v0.1.0-pre.md`](releases/v0.1.0-pre.md) — release notes
- [`deploy/packaging.md`](../deploy/packaging.md) — artifact build details
- [`SECURITY.md`](../SECURITY.md) — disclosure policy
