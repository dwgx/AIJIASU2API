# LEDGER — append-only campaign record

Format: `UTC | actor | event | scope | note`. One line per event; entries are never edited, only
appended. Times are UTC from the machine clock (`date -u`); where an event happened before this
file existed, its time is taken from file mtimes and marked `~`.

## Reserved vocabulary (define once, use later)

`STOP-*` tags close or suspend a slice. They are written as `STATUS: BLOCKED (STOP-X)` in a
slice's final answer and as a ledger entry. They are **reserved**: none has been raised yet.

| Tag | Meaning | Who can lift it |
|---|---|---|
| `STOP-ACCOUNT` | Next step needs a real account/session; none is available. Live work halts. | Owner (supplies account) |
| `STOP-TOS` | The vendor's terms forbid the next step. Halt and escalate. | Owner only |
| `STOP-DRIFT` | Protocol/version mismatch that static evidence cannot resolve. | AJS-06 (dynamic lab) |
| `STOP-SCOPE` | Requested work falls outside the slice's `# Non-Conflict` paths. | ResearchLead |
| `STOP-SAFETY` | Step would install a driver/service, execute an untrusted payload, or modify the host. | Owner only |
| `STOP-BUDGET` | Slice exhausted its tool/time budget without closing. | ResearchLead |

Status values for a slice: `open` → `in-progress` → `done` | `blocked (STOP-*)`.

## Entries

```
2026-10-22T22:40Z | coordinator | dispatch      | AJS-01..AJS-05 | Five briefs written to C:/Users/dwgx1/.dwgx/tmp/omp-ops-20261023/briefs/ (mtime 07:40:16 +0900). Wave 1 is static-only: no login, no connect, no installer execution, no driver/service install, no package install.
2026-10-22T22:40Z | coordinator | dispatch      | AJS-02        | OSINT. Scope research/10-osint/. Report research/10-osint/REPORT.md. Depends on AJS-01.
2026-10-22T22:40Z | coordinator | dispatch      | AJS-03        | Windows static inventory. Scope research/20-windows-static/ + temp extraction ajs-win-20261023. Report INVENTORY.md.
2026-10-22T22:40Z | coordinator | dispatch      | AJS-04        | Linux static analysis. Scope research/30-linux-static/ + temp ajs-linux-20261023. Report REPORT.md.
2026-10-22T22:40Z | coordinator | dispatch      | AJS-05        | FV protocol spec + Python reference. Scope research/40-protocol/. Report PROTOCOL.md. No requests to login.ajs-app.com.
2026-10-22T22:41Z | ResearchLead | verify-greenfield | repo root | `git status` → "fatal: not a git repository"; only file present is research/AJiaSu_4.9.6.0.exe (5,507,328 bytes). Matches brief Env-Claims.
2026-10-22T22:41Z | ResearchLead | write         | .gitignore, README.md | Binaries/secrets ignored; README states research-only status, ToS caution, no-credentials rule.
2026-10-22T22:41Z | ResearchLead | refute        | PLAN §2       | TAP claim CONFIRMED: OemVista.inf carries the OpenVPN Technologies GPLv2 header, Provider "TAP-Windows Provider V9", DriverVer 04/21/2016 9.00.00.21; tapinstall.pdb path tap-windows6-mattock. Stock tap-windows6, not a proprietary driver.
2026-10-22T22:41Z | ResearchLead | refute        | PLAN §2       | OpenAJS claim CONFIRMED: pure-Java FV implementation (FvConnection/FvCrypto/FvStream/SocksProxyServer), Unlicense, same API hosts as the vendor binary. Not overturned; caveat added that its target build version is unstated.
2026-10-22T22:41Z | ResearchLead | refute        | PLAN §5       | OAEP ambiguity CONFIRMED as a live risk: FvCrypto defaults to SHA-1 and exposes -Dfv.oaep to switch, MGF1 SHA-1/SHA-256. Prior art is itself parameterized, so the vendor parameter set stays [UNKNOWN].
2026-10-22T22:41Z | ResearchLead | write         | PLAN.md, research/00-plan/ | Campaign plan, question tree (24 atomic questions), master index, this ledger.
2026-10-22T22:42Z | ResearchLead | note          | PLAN.md       | Brief header named the report path research/00-plan/PLAN.md but the six-file list places it at repo root; the six-file constraint wins. Root PLAN.md is the report.
2026-10-22T22:42Z | ResearchLead | commit        | bootstrap     | Single initial commit of the six planning files on branch main, explicit paths only, no remote, no binaries. Hash: see `git log -1`.
2026-10-22T22:43Z | ResearchLead | refute        | PLAN §2       | Feasibility CONFIRMED, not overturned: `OpenAJS` was created and last pushed 2026-06-07 (GitHub API, 2026-10-23) and targets the same API hosts found in the 4.9.6.0 and 4.2.3.0 binaries; the control API answered without credentials. Residual: no artifact proves the 4.9.6.0 wire format, so Tier 2 stays [INFERENCE] until AJS-05.
2026-10-22T22:43Z | ResearchLead | refute        | PLAN §5       | "No TAP required for a proxy API" CONFIRMED (stock driver; OpenAJS reaches SOCKS5 without a TAP device). The implied effort claim is WEAKENED: if FV is a raw-IP tunnel, a userspace TCP/IP stack is required — the vendor's own `FVClientProxyLwip` suggests it may be. Added as a risk row; Q-8 gates AJS-07 scope.
2026-10-22T22:43Z | ResearchLead | amend         | bootstrap     | PLAN.md and QUESTION-TREE.md corrected after the refutation pass (drift date evidence, raw-IP caveat, S-5 qualification); commit amended so history holds exactly one bootstrap commit.
```

## Next expected entries

- `AJS-02..AJS-05` each: one `done` or `blocked (STOP-*)` entry, with the report path.
- AJS-08 verification: one entry per wave-1 claim set, `CONFIRMED` / `WEAKENED` / `OVERTURNED`.
- Any correction to an `S-n` evidence seed in `QUESTION-TREE.md`, as an appended `correct` entry.
