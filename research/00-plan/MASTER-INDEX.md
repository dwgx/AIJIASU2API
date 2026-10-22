# MASTER-INDEX — slices, status, deliverables

Last updated: 2026-10-23 (bootstrap, AJS-01). Status vocabulary: `open` → `in-progress` →
`done` / `blocked`. Nothing is `done` yet: wave 1 has been dispatched but not collected.

## Wave 1 — research baseline (dispatched 2026-10-23)

| ID | Slice | Owner | Status | Report path | One-line deliverable |
|---|---|---|---|---|---|
| AJS-01 | Bootstrap + campaign plan | ResearchLead | `done` | `PLAN.md` · `research/00-plan/` | Git repo, `.gitignore`, README, plan, question tree, this index, ledger. |
| AJS-02 | OSINT | OsintScout | `open` | `research/10-osint/REPORT.md` | Source-grounded map of public AJiaSu/FV reverse-engineering work, endpoints and licences. |
| AJS-03 | Windows static | WinStatic | `open` | `research/20-windows-static/INVENTORY.md` | Per-file inventory of the 4.9.6.0 payload: hashes, versions, RTTI/strings, driver provenance. |
| AJS-04 | Linux static | LinuxStatic | `open` | `research/30-linux-static/REPORT.md` | What the 4.2.3.0 static ELF exposes: endpoints, constants, privilege and TUN requirements. |
| AJS-05 | Protocol spec | ProtoSpec | `open` | `research/40-protocol/PROTOCOL.md` | Language-neutral FV protocol spec + runnable Python reference, with `[UNKNOWN]` markers. |

## Wave 2+ — not dispatched

| ID | Slice | Owner | Status | Report path | One-line deliverable |
|---|---|---|---|---|---|
| AJS-06 | Dynamic lab | TBD | `blocked` (needs account) | `research/50-dynamic-lab/REPORT.md` | Observed login, node list, FV handshake and egress IP under a disposable account. |
| AJS-07 | Native emulator | TBD | `blocked` (needs AJS-05/06) | `research/60-emulator/REPORT.md` | Go FV client with SOCKS5 + HTTP front end and runtime node switching. |
| AJS-08 | Verification | verifier | `blocked` (needs AJS-02..05) | `research/90-verification/REPORT.md` | Independent re-derivation of every wave-1 claim against the artifacts. |

## Artifact map

| Path | Kind | Tracked |
|---|---|---|
| `README.md`, `PLAN.md` | narrative | yes |
| `research/00-plan/*.md` | planning | yes |
| `research/AJiaSu_4.9.6.0.exe` | vendor installer, 5,507,328 bytes, SHA256 `1b1c97f55a2348055c201ecba0badd0f7ea17dacb1f0df3ea979d52ee9d01a1c` | **no** (ignored) |
| `research/10-osint/` … `research/40-protocol/` | slice reports | yes (when written) |
| `C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023` | extracted Windows payload, 37 files | no (outside repo) |
| `C:/Users/dwgx1/AppData/Local/Temp/ajs-linux-20261023/ajiasu` | Linux client 4.2.3.0 | no (outside repo) |

## How to read this repo

1. `PLAN.md` — why, two tiers, acceptance criteria, risks, Owner questions.
2. `research/00-plan/QUESTION-TREE.md` — the atomic questions and what is blocked.
3. The slice report for the area you care about. Each report cites paths/URLs/commands.
4. `research/00-plan/LEDGER.md` — chronological record of what was dispatched and with what scope.
