# Versioning

Status: current for `v0.1.0-pre.3`, published 2026-10-29 from commit `5479a3a`. The previous
published release is `v0.1.0-pre.2`; `v0.1.0-pre` remains the first published release. This page
defines how versions are named, which artefact carries which version, when `:latest` moves, and
what compatibility a caller may rely on. The release procedure itself is
[`docs/RELEASE.md`](RELEASE.md).

## Scheme

Releases follow [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html), tagged with a
`v` prefix: `vMAJOR.MINOR.PATCH`, optionally with a hyphenated pre-release suffix
(`v0.1.0-pre.3`). Build metadata is not used in tags.

- **`0.y.z` is not stable.** While the major version is `0`, a MINOR bump may contain a breaking
  change to the HTTP contract. A PATCH bump is fixes only.
- **A hyphen marks a pre-release.** `v0.1.0-pre.3` sorts *after* `v0.1.0-pre.2`, which sorts
  after `v0.1.0-pre`, and all three sort *before* `v0.1.0` under SemVer precedence. A
  pre-release is not a stable release: no `:latest` movement, no "latest release" claim, no
  compatibility promise beyond the contract in `docs/openapi.json`.
- **The public line starts at `v0.1.0-pre`** (published 2026-10-27). The current published
  pre-release is `v0.1.0-pre.3`, published 2026-10-29 from commit `5479a3a`; the previous is
  `v0.1.0-pre.2`, published 2026-10-29 from commit `d80fee7`.
  The eight older tags (`v0.1.0-archive` … `v0.7.0-live`) are milestone checkpoints in the
  project's own history, not releases of the public line. They exist only in the local clone, so
  there is no earlier public release and no upgrade or compatibility path from a milestone
  checkout. Do not order them against the public line: SemVer compares pre-release identifiers
  lexically, so `v0.1.0-pre` versus `v0.1.0-archive` or `v0.7.0-live` is not a meaningful
  comparison.

Check the tag state before quoting a version:

```bash
git tag --list 'v*' | wc -l              # 11: eight milestones plus three prereleases
git ls-remote --tags origin              # v0.1.0-pre, v0.1.0-pre.2, v0.1.0-pre.3
```

## Which artefact carries which version

| Identity | Value in this release | Authority |
|---|---|---|
| Release | `v0.1.0-pre.3` (published; `v0.1.0-pre.2` is the previous published release, `v0.1.0-pre` the first) | the git tag |
| Contract | `0.1.0` | `info.version` in [`docs/openapi.json`](openapi.json) |
| Binary | `v0.1.0-pre.3 (<commit>)` when stamped, else `0.1.0` | `-X` flags from `deploy/package.sh` or the Docker build args; else `DefaultVersion` |
| Artefact set | `MANIFEST.txt` + `SHA256SUMS` | `deploy/package.sh` at build time |

The binary version is a build label, not the release identity. A `package.sh` build and a
released accountd image report the tag they were stamped with plus the packaged commit; a build
without those flags — `go run`, `go test`, a plain `go build`, or `docker build` with default
args — reports `0.1.0`. Identify a release by its tag and the `MANIFEST.txt` shipped with it,
never by the version string the process prints. The published `v0.1.0-pre.3` Windows asset prints
`ajs-accountd v0.1.0-pre.3 (5479a3a)`; the published `v0.1.0-pre.2` asset prints
`ajs-accountd v0.1.0-pre.2 (d80fee76af2103052069a7278338d808b8aa8d6c)`. The `v0.1.0-pre` assets
and images predate the stamping and report `0.1.0`; they were not rebuilt.

The contract version moves when the contract moves, not on every tag. A docs-only release can
keep `0.1.0`; a change to paths, operations, schemas or scope metadata must bump it.

## Tags and release assets

A tag names a commit. A release attaches build output to that tag:

| Item | Produced by | Covered by `SHA256SUMS` |
|---|---|---|
| `ajs-accountd-linux-amd64`, `-windows-amd64.exe`, `-darwin-arm64` | `deploy/package.sh` | yes |
| `dashboard-dist.tar.gz` | `deploy/package.sh` | yes |
| `MANIFEST.txt` | `deploy/package.sh` | yes |
| `SHA256SUMS` | `deploy/package.sh` | — (it is the manifest) |
| `deploy-kit.tar.gz` | `deploy/package.sh` | transitively, via its digest in `MANIFEST.txt` |

Verify on the receiving host before installing:

```bash
sha256sum -c SHA256SUMS        # or: shasum -a 256 -c SHA256SUMS
```

No checksum is published in prose: the values differ per build and per `SOURCE_DATE_EPOCH`. A
published checksum that was not regenerated from the exact artefact set is worse than none.

The published remote state, as observed on 2026-10-29:

```bash
curl -sS https://api.github.com/repos/dwgx/AIJIASU2API/releases/tags/v0.1.0-pre.3 \
  | python -c "import sys,json;d=json.load(sys.stdin);print(d['prerelease'],d['immutable'],len(d['assets']))"
# -> True True 6

curl -sS https://api.github.com/repos/dwgx/AIJIASU2API/releases/tags/v0.1.0-pre.2 \
  | python -c "import sys,json;d=json.load(sys.stdin);print(d['prerelease'],d['immutable'],len(d['assets']))"
# -> True False 6
```

The first `v0.1.0-pre` release still answers `True 6` and keeps its six assets. The tag list shows
all three prereleases.

A tag containing `-` produces a GitHub Release marked **prerelease** and never moves `:latest`.
The `v0.1.0-pre.3` image tags are `ghcr.io/dwgx/ajs-accountd:v0.1.0-pre.3` (digest
`sha256:d6c1c293…`) and `ghcr.io/dwgx/ajs-dashboard:v0.1.0-pre.3` (digest `sha256:9c9cd4cf…`);
both were pushed by the `release` workflow and both attestations verify. `:latest` does not
exist. The `v0.1.0-pre` and `v0.1.0-pre.2` images remain as published. Image presence was not
verified from this workstation, so pulling any of these tags remains `[UNVERIFIED]` until
`docker manifest inspect` answers here.

## `:latest`

`:latest` is a moving alias for the last **final** release, and it moves only for a tag without a
hyphen. The image workflow (`release.yml`) gates the promotion step on
`!contains(github.ref_name, '-')` and aliases the already-pushed manifest with
`docker buildx imagetools create` rather than rebuilding.

Consequences:

- `v0.1.0-pre` publishes `:v0.1.0-pre` only, `v0.1.0-pre.2` publishes `:v0.1.0-pre.2` only, and
  `v0.1.0-pre.3` publishes `:v0.1.0-pre.3` only. Since there is no final release, no pre-release
  creates or moves `:latest`, and `:latest` does not exist.
- `deploy/update.sh` requires a release tag; the shipped `.env.example` value `latest` is refused
  unless `--allow-latest` is given. Pass `--tag v0.1.0-pre.3` explicitly for this pre-release; do
  not assume `latest` means "newest tag".
- A dash tag is never a rollback target for `latest`, and a final tag never rewrites an existing
  versioned image.

## Compatibility and deprecation

During `0.y.z`:

- **The contract is the promise.** `docs/openapi.json` (mirrored byte-for-byte at
  `daemon/internal/api/openapi.json`) defines paths, operations, schemas and the
  `x-required-scope` metadata. Prose in `docs/` describes behaviour; the contract defines shape.
- **Additive first.** New optional fields, new operations and new enum values are MINOR changes.
  A client must ignore unknown fields and treat an unknown `error.reason` as generic.
- **Breaking changes** — removing or renaming an operation, changing a required field, changing a
  status code or error code — bump MINOR while the major version is `0`, and are called out under
  "Changed" in [`CHANGELOG.md`](../CHANGELOG.md).
- **Deprecation** is announced in `CHANGELOG.md` and marked with `deprecated: true` on the
  operation or schema. A deprecated element stays for at least one MINOR release before removal;
  it is never removed in a PATCH.
- **The state snapshot is versioned, not additive.** This build writes version 2 and accepts
  version 1 by migrating it in place. A version 2 `state.json` read by a version 1 binary is
  rejected, so a downgrade requires restoring the pre-upgrade state files — see
  [`docs/OPERATIONS.md`](OPERATIONS.md#upgrade-and-rollback).

Pre-releases make no promise that a later pre-release keeps the same shape; read the release
notes for the delta.

## Pointers

- [`docs/RELEASE.md`](RELEASE.md) — gates, tag creation, artefacts, rollback
- [`CHANGELOG.md`](../CHANGELOG.md) — what changed per release
- [`docs/releases/v0.1.0-pre.3.md`](releases/v0.1.0-pre.3.md) — current release notes
- [`docs/releases/v0.1.0-pre.2.md`](releases/v0.1.0-pre.2.md) — previous release notes, historical
- [`docs/releases/v0.1.0-pre.md`](releases/v0.1.0-pre.md) — first release notes, historical
- [`deploy/packaging.md`](../deploy/packaging.md) — artefact build and verification
