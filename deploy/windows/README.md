# Windows install, update and smoke scripts

Standalone Windows deployment for `ajs-accountd`, the release asset
`ajs-accountd-windows-amd64.exe` published by `deploy/package.sh`. These scripts
replace the Linux compose kit for a single Windows host; nothing here talks to
Docker.

| File | Purpose |
|---|---|
| `verify-release.ps1` | download one release with `gh` and verify it |
| `install.ps1` | verify, install into `C:\ProgramData\AIJIASU2API`, register auto-start |
| `update.ps1` | verify, swap the binary, health-check, roll back on failure |
| `run.ps1` | console launcher: throwaway smoke run, or the installed config |

Requires Windows PowerShell 5.1 or PowerShell 7, the [GitHub CLI](https://cli.github.com/)
(`gh auth login`) for downloads, and an elevated shell for auto-start. The
scripts are ASCII, LF, and have no dependency outside the Windows box.

## Quick start

```powershell
# 1. dry run: show the plan without writing anything
./install.ps1 -Tag v0.1.0-pre.2 -AllowUnverified -WhatIf

# 2. install (admin): verify -> copy -> generate config -> register auto-start -> health check
./install.ps1 -Tag v0.1.0-pre.2 -AllowUnverified

# 3. later: update to another tag, or roll back one step
./update.ps1 -Tag v0.1.0-pre.3
./update.ps1 -Rollback
```

Attestations are **required by default**. `-AllowUnverified` is the only
opt-out, and it is needed only for releases that predate GitHub attestations:
`v0.1.0-pre.2` has none, so every `v0.1.0-pre.2` command above passes it
explicitly. **Future releases should omit it** — a release with attestations
verifies without the switch, and passing it anyway would hide a real failure.
`-RequireAttestation` is still accepted, is the default, and cannot be combined
with `-AllowUnverified` (exit 2). Neither switch relaxes the checksum chain.

## What verification covers

`verify-release.ps1` downloads `ajs-accountd-windows-amd64.exe`, `MANIFEST.txt`
and `SHA256SUMS` with `gh release download` into a temp directory, then:

1. **`Get-FileHash` against `SHA256SUMS`** — every downloaded file, always. A
   mismatch, a missing entry or a missing asset exits 1.
2. **`MANIFEST.txt`** — `version:` must equal the tag; `commit:` is reported.
3. **`-version`** — the downloaded binary is executed once with `-version` and
   the stamped version must match the manifest. A binary that cannot execute
   here is reported as `[UNVERIFIED]`, not silently accepted as equal.
4. **Attestations** — by default both `gh release verify-asset <tag> <file>`
   and `gh attestation verify <file>` must pass. `-AllowUnverified` downgrades
   a failure here to a warning; nothing else skips this step.

There is no Authenticode signature on these assets, and these scripts make no
claim that there is one. Without `gh` on `PATH` the script exits 2 before
downloading anything: it never falls back to a different transport.

## Install layout

```
C:\ProgramData\AIJIASU2API\
  ajs-accountd.exe              verified binary
  ajs-accountd.exe.previous     previous binary (update.ps1 -Rollback)
  run.ps1                       launcher used by the auto-start task
  ajs.env                       config; created once, never overwritten
  update-state.json             last update/rollback record
  data\                         state (state.json, secrets.json, ...)
  logs\                         one <timestamp>.out.log/.err.log per start, last 10 kept
```

`ajs.env` holds `AJS_ADMIN_TOKEN` (generated, 64 hex) and `AJS_MASTER_KEY`
(generated, 64 hex). The install directory is ACL-restricted to SYSTEM,
Administrators and the installing account **before anything is written into
it**, because the auto-start task executes `run.ps1` from that path as SYSTEM.
An existing directory owned by another account, or one that is a reparse point
(symlink/junction), is refused; the state directory is checked the same way.
`ajs.env` is created empty, locked down, and only then filled with the secrets;
a DACL that cannot be applied is a fatal error, never a warning. Back up
`AJS_MASTER_KEY`: losing it makes sealed
account passwords unrecoverable. An existing `ajs.env` is never rewritten —
`-Force` replaces the binary only.

`-NoService` installs the files and leaves the daemon stopped; start it with
`run.ps1 -FromConfig`. `-WhatIf` prints the plan and exits without writing,
downloading or registering anything.

## Auto-start: a scheduled task, not a service

`install.ps1` registers the scheduled task `AJSAccountd` (name via
`-ServiceName`): trigger at startup, run as SYSTEM with the highest run level,
no execution time limit, three restart attempts, action
`powershell.exe -File <InstallDir>\run.ps1 -FromConfig`.

**`sc.exe` is not supported.** `ajs-accountd` does not implement the Windows
service control dispatcher (there is no `windows/svc` usage in `daemon/`), so a
service created with `sc.exe create ... binPath=` pointing at the binary never
answers the SCM, fails with error 1053, and is terminated. The project already
documents that there is no Windows service wrapper (`deploy/README.md`). If you
front the daemon with an SCM-capable wrapper of your own (NSSM, WinSW, srvany),
install with `-NoService` and point that wrapper at `ajs-accountd.exe`.

Stopping the task kills the wrapper shell first, so `update.ps1` also stops any
`ajs-accountd` process running from the install path before it swaps the
binary, and waits for it to exit.

## Update and rollback

`update.ps1` verifies the release, stops the task and the daemon, copies the
current binary to `ajs-accountd.exe.previous`, stages the new binary next to its
destination and moves it into place, starts the task, and waits for `/healthz`
to report the **new** version (bounded by `-TimeoutSec`, default 30). If the
check fails it restores the previous binary, starts it, and exits 1. `ajs.env`
and `data\` are never touched.

`-Rollback` first compares the sha256 of `ajs-accountd.exe.previous` with the
hash recorded in `update-state.json` and refuses to swap (exit 1) when they
differ; with no record it warns that the backup cannot be verified. It then
swaps the backup in, starts it, and runs the same health check. It does not
re-download or re-verify against the release: that binary was verified when it
was installed. `update.ps1 -WhatIf` prints either plan without touching the
binary, the task or the config.

The last operation is recorded in `update-state.json` (`tag`, `version`,
`previous_version`, both sha256 values, `health_url`, `rolled_back`).

## Smoke run

```powershell
# temporary state, free loopback port, AJS_ALLOW_INSECURE=1, stopped afterwards
./run.ps1 -Exe .\ajs-accountd-windows-amd64.exe -Smoke

# the installed config (what the task runs), foreground
./run.ps1 -FromConfig -StreamLog
```

A smoke run never writes outside its temporary directory and never registers or
changes a task, service or machine-wide setting. Pass `-Token` to run with the
bearer instead of insecure mode, `-Port` to pin the port, `-StateDir` to keep
state, and `-KeepState` to keep the temporary directory and logs.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | verified / installed / updated / healthy |
| 1 | verification failed, the daemon did not become healthy, or the update was rolled back |
| 2 | usage or precondition (no `gh`, no elevation, no existing install, missing config) |

## Known limits

- The auto-start task path needs an elevated shell and was **not executed** on
  the workstation that wrote these scripts (`[UNVERIFIED]`): task registration,
  the SYSTEM launch and the update-time health check with rollback are code
  paths, not observed runs. Everything else (verify, install with `-NoService`,
  update without auto-start, rollback, smoke runs under PowerShell 5.1 and 7)
  was run against `v0.1.0-pre.2`.
- Stopping the daemon is a hard stop: the SCM is not involved and the daemon
  gets no graceful signal, so at most one snapshot interval of state is lost.
- The admin API is a loopback bearer API. Publishing it beyond the host needs
  your own reverse proxy and TLS; these scripts do not configure either.
