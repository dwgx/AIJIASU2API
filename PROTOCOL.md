# Project protocol

This is the **collaboration and release protocol** for AIJIASU2API: how a change moves from an
idea to a tagged release. It is not the AJiaSu wire protocol. The wire-protocol research record is
[research/40-protocol/PROTOCOL.md](research/40-protocol/PROTOCOL.md).

The short form: **issue → board/ticket → branch → pull request → review → verify → tag.** Each
step has an exit condition. Skipping a step is allowed only when the maintainer says so in the
ticket.

## 1. Issue

Open an issue for anything larger than a typo or a test-only fix. The issue states:

- the problem and who hits it;
- the proposed change and the non-goals;
- the evidence you already have (paths, commands, live observations);
- what "done" would look like.

Security reports do not go here. Follow [SECURITY.md](SECURITY.md).

## 2. Board and ticket

If a work board is in use, the ticket is the unit of ownership:

- An `AJS-NNN` identifier names the work.
- The ticket declares the **write scope** (files or trees) and a **DONE WHEN** condition before
  work starts. Two writers must not own the same tree.
- A ticket that touches a generated file names the source file and the regeneration command.
- When the work lands, the ticket closes with a verdict and the command or artifact that proves
  it. "Done" without evidence is not closed.

A ticket that grows beyond its declared scope is split, not silently widened.

## 3. Branch

Branch from the current integration branch, one branch per ticket:

```text
fix/ajs-123-gate-lease-check
docs/ajs-480-public-docs
test/ajs-140-restore-drill
```

Keep the branch rebased on the integration branch while it is open. Do not mix tickets in one
branch. Do not rewrite a branch that another person is reviewing without saying so.

## 4. Pull request

A pull request follows [CONTRIBUTING.md](CONTRIBUTING.md):

- one concern;
- a test for a behaviour change;
- every claim cited or marked `[UNVERIFIED]`;
- no credentials and no vendor artifacts;
- no AI attribution in commits or the description;
- generated docs regenerated, or a stated reason they are unchanged.

Use the repository's pull request template. Say which parts of an external report you reproduced
yourself.

## 5. Review

The maintainer reviews every change; see [GOVERNANCE.md](GOVERNANCE.md) and
[.github/CODEOWNERS](.github/CODEOWNERS). Review checks scope, correctness, the evidence bar, and
the security boundary — not only whether CI is green.

Security-sensitive paths (auth, gate admission, credential store, deploy kit, workflows) need a
reviewer who has read [SECURITY.md](SECURITY.md) and [docs/RBAC.md](docs/RBAC.md), plus a test
that demonstrates the boundary. A green run is not a substitute for that test.

Review ends in one of: **merge**, **changes requested**, or **declined with a reason**. A declined
PR is closed, not left open indefinitely.

## 6. Verify

The verification gate is the set of commands for the trees the change touches. Run them once,
after the change is complete, and quote the output in the PR:

```bash
cd daemon && go vet ./... && go test -race -count=1 ./...
cd ../emulator && go vet ./... && go test -race -count=1 ./...
cd ../dashboard && npm ci && npm run verify:lockfile
npm test -- --pool=forks --maxWorkers=1
VITE_USE_MOCK=false VITE_API_BASE= npm run build
npm run docs:gen -- --check
cd ../sdk/go && go test -race -count=1 ./...
cd ../sdk/ts && npm ci && npm test && npm run typecheck
```

For changes to the production kit, also run the kit gate described in
[deploy/production/README.md](deploy/production/README.md). A non-root run is not a production
`PASS`; say which identity ran it.

If you cannot run a check, say so and mark the claim `[UNVERIFIED]`. Do not describe an unrun
check as passed.

## 7. Merge

The maintainer merges after review and verification. The resulting commit keeps the
`type(scope): summary` format. Do not commit generated state, secrets, or local paths.

## 8. Tag and release

### Version scheme

- Releases use `vMAJOR.MINOR.PATCH`, with `-pre` for a pre-release (`v0.1.0-pre`).
- The earlier local tags (`v0.1.0-archive` … `v0.7.0-live`) are **milestone checkpoints, not
  releases**. They are not part of the release series.
- A pre-release is not stable. It carries no compatibility promise beyond the contract in
  [docs/openapi.json](docs/openapi.json) at that tag.

### Preconditions

1. The release PR updates [CHANGELOG.md](CHANGELOG.md) with the version, date, what is served,
   what is disabled, and known gaps.
2. The verification gate above is green at the commit to be tagged.
3. The OpenAPI contract and the generated mirror are current
   (`cd dashboard && npm run docs:gen -- --check`).
4. The security policy and disclaimer still describe the release surface.
5. No credential, vendor artifact, or local state is in the tree.

### Tagging

```bash
git tag -a v0.1.0-pre -m "v0.1.0-pre"
git push origin v0.1.0-pre
```

Tag the reviewed commit that passed the gate. Do not tag an unmerged work branch.

There is no git remote in this tree at the time of writing. Until one exists, the tag is local
and the push step is skipped; the release notes must say so rather than implying a download URL.
The detailed gate list and rollback steps are in [docs/RELEASE.md](docs/RELEASE.md).

### Pre-release rules

A `-pre` tag is a **prerelease**:

- the GitHub Release is marked `prerelease`;
- it must not move `:latest` in the container registry;
- it must not be described as production-stable in the README or release notes.

The workflows in this tree implement this: `release.yml` pushes only `:<tag>` and promotes
`:latest` in a separate step gated on a tag without `-`; `package.yml` and `release.yml` set
`prerelease: ${{ contains(github.ref_name, '-') }}` on the GitHub Release. A `-pre` tag therefore
leaves `:latest` untouched and is published as a prerelease. GitHub Actions execution is still
`[UNVERIFIED]` until a remote exists and a tag actually runs the pipeline.

### Artifacts

`deploy/package.sh` produces the platform binaries, the dashboard bundle, `MANIFEST.txt`, and
`SHA256SUMS`. Verify the checksums before publishing:

```bash
bash deploy/package.sh --version v0.1.0-pre --out dist
(cd dist && sha256sum -c SHA256SUMS)
```

`SHA256SUMS` proves the bytes match the manifest. It does not prove that a human reviewed the
build or that the build is reproducible across toolchains.

### Rollback

A bad release is rolled back by:

1. marking the GitHub Release as withdrawn or superseded;
2. restoring the previous tag in the deployment (`deploy/update.sh --rollback` where the compose
   path is used);
3. recording the rollback and its reason in [CHANGELOG.md](CHANGELOG.md).

Never delete a tag that has been published. Add a new tag that supersedes it.

## Security releases

A security fix may be developed on a private branch and released before the issue is public.
The advisory is published with the fix, and the reporter is credited if they want credit. The
normal review and verification rules still apply; the only difference is the embargo.

## Changes to this protocol

The maintainer may amend this document. Material changes go through a pull request with the
reasoning in the description.
