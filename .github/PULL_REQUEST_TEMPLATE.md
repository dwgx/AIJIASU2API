<!--
If this came from an external report or analysis, say which parts you verified
yourself. Unverified claims are acceptable when labelled `[UNVERIFIED]`; an
unlabelled claim is treated as false until checked.

Do not paste a credential, token, cookie, account name, or hash of one.
-->

## What and why

<!-- One paragraph. The problem, the change, and why this shape. -->

## Scope

<!-- Files or trees touched, and what this PR deliberately does not change. -->

## Verification

<!--
Paste the commands you ran and their result. Do not describe an unrun check as
passed. If you could not run something, say so and mark the claim [UNVERIFIED].
-->

```bash
# e.g.
# cd daemon && go vet ./... && go test -race -count=1 ./...
# cd dashboard && npm test -- --pool=forks --maxWorkers=1 && npm run docs:gen -- --check
```

## Checklist

- [ ] **One concern.** Unrelated hunks removed, even if CI is green.
- [ ] **No credential.** No account, password, e-mail, token, TOTP secret, session cookie, or
      hash of any of them. `.env`, `*.key`, and `*.pem` are ignored.
- [ ] **No vendor artifact.** No installer, driver, capture, dump, or decompiled vendor code.
- [ ] **Evidence bar.** Every factual claim cites a path, a command, or a URL, or is marked
      `[UNVERIFIED]`.
- [ ] **Tests.** A behaviour change has a test that fails before and passes after. The commands
      and their output are above.
- [ ] **Generated files.** If `docs/openapi.json` or the docs content changed,
      `cd dashboard && npm run docs:gen` was run and the mirror is committed.
- [ ] **No AI attribution.** No `Co-Authored-By` trailer naming an AI, no "Generated with" line,
      no tool signature.
- [ ] **Security-sensitive paths called out.** If this touches auth, RBAC, gate admission,
      credential storage, `deploy/production/`, or `.github/workflows/`, the boundary and its test
      are described above.
- [ ] **Docs and changelog.** The owning document is updated, or the PR states why it is
      unchanged. See CONTRIBUTING.md.
- [ ] **Licence.** I have the right to submit this under the MIT licence in LICENSE.

## Linked issue or ticket

<!-- AJS-NNN or #NN. Write "none" for a typo or test-only fix. -->
