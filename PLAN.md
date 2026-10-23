# PLAN — AJiaSu reverse engineering → egress-selectable proxy API

Status: **research baseline, wave 1** · Date: 2026-10-23 · Owner: project Owner
Repo: `D:/Project/LovePlusSpeedToProxy` (branch `main`, no remote)

## 1. North star

> Reverse-engineer the AJiaSu client and reimplement its protocol so the service becomes a
> proxy API with selectable egress IP; later open source.

Restated as an engineering target: a service that accepts `(account, node)` and exposes a
local SOCKS5/HTTP endpoint whose traffic leaves through the node's advertised IP, with node
switching at runtime and no dependency on the vendor GUI.

## 2. Feasibility verdict

**YES — feasible, in two tiers.** Evidence is static only; nothing here has been exercised
against the live service (no credentials, no connect attempts in this campaign).

| Tier | What it is | Why it is feasible | Risk |
|---|---|---|---|
| **Tier 1** | Orchestrate the official **Linux** client as a black box; expose its egress as a proxy. | The Linux client is a single static ELF (4.2.3.0, 3,578,792 bytes) that speaks the `ClientApi` HTTP surface and `login.ajs-app.com`; prior art (`cubk1/OpenAJS`) already drives the API and converts FV to SOCKS5. | Client may need a TUN device or root; process lifetime/state is opaque; version drift breaks parsing. |
| **Tier 2** | **Native reimplementation of the FV protocol** — login, node list, FV tunnel, local SOCKS5/HTTP — running with no vendor binary. | `OpenAJS` (Java, Unlicense) implements `FvConnection`/`FvCrypto`/`FvStream`/`SocksProxyServer` against the same endpoints, proving the protocol is reproducible from the outside. | Protocol details must be re-derived and re-verified; OAEP hash and key exchange are under-specified (see §5). |

**The control API is live and answerable without credentials.** A campaign-alignment probe
(recorded 2026-10-23 in `ALIGN-aijiasu-proxy-20261023.md`, not re-run by this slice) issued
`POST https://login.ajs-app.com/client.php` with no credentials and received
`AuthResult: WrongPassword`. The endpoint presents a self-signed `CN=localhost` certificate and
the client does not validate it. That confirms the HTTP surface exists, is reachable, and that a
wrong-credential path can be exercised without an account — a useful fixture for the spec.

Tier 2 is the north star. Tier 1 exists to de-risk: it produces a working egress quickly and
generates the live observations Tier 2 needs.

**The Windows kernel component is not a proprietary tunnel driver.** The installer ships
`tap/tap{,6}/{amd64,arm64,i386}/tap0901.sys` with `OemVista.inf` and `tapinstall.exe`
(verified 2026-10-23 in `C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023`). The INF carries the
unmodified OpenVPN Technologies GPLv2 header, `Provider = "TAP-Windows Provider V9"`,
`DriverVer = 04/21/2016, 9.00.00.21`, and the bundled `tapinstall.exe` was built from
`tap-windows6-mattock`. That is the stock OpenVPN **TAP-Windows** driver. It is only the
**system-wide TUN path** (the "all traffic" mode); a SOCKS5/HTTP proxy API does **not** need it,
and no driver or service installation is part of this campaign.

## 3. Slices

Wave 1 (this campaign) is entirely static: no login, no connect, no installer execution, no
driver install, no package install.

| ID | Slice | Owner | Write scope | Report | Depends on |
|---|---|---|---|---|---|
| AJS-01 | Bootstrap + campaign plan | ResearchLead | `.git`, `.gitignore`, `README.md`, `PLAN.md`, `research/00-plan/` | `PLAN.md`, `research/00-plan/*` | — |
| AJS-02 | OSINT on AJiaSu / FV protocol | OsintScout | `research/10-osint/` | `research/10-osint/REPORT.md` | AJS-01 |
| AJS-03 | Windows static inventory (installer payload, RTTI, strings) | WinStatic | `research/20-windows-static/` | `research/20-windows-static/INVENTORY.md` | AJS-01 |
| AJS-04 | Linux client acquisition + static analysis | LinuxStatic | `research/30-linux-static/` | `research/30-linux-static/REPORT.md` | AJS-01 |
| AJS-05 | FV protocol spec + Python reference implementation | ProtoSpec | `research/40-protocol/` | `research/40-protocol/PROTOCOL.md` | AJS-01, AJS-02 (inputs only; runs in parallel) |

Later waves — **not started, not dispatched**, listed so the plan is complete:

| ID | Slice | Owner | Write scope | Report | Depends on |
|---|---|---|---|---|---|
| AJS-06 | Dynamic lab: run the official Linux client under observation with an Owner-supplied account; capture login, node list, FV handshake | TBD | `research/50-dynamic-lab/` | `research/50-dynamic-lab/REPORT.md` | AJS-04, AJS-05, **account** |
| AJS-07 | Native emulator (Go) implementing the FV client + SOCKS5/HTTP front end | TBD | `research/60-emulator/` (or a separate tree) | `research/60-emulator/REPORT.md` | AJS-05, AJS-06 |
| AJS-08 | Independent verification of every wave-1 claim against artifacts | verifier | `research/90-verification/` | `research/90-verification/REPORT.md` | AJS-02..05 |

Language decision (alignment doc, 2026-10-23, reversible): **Go** single binary for the emulator,
**Python** for research fixtures and conformance tests. It does not constrain the protocol spec,
which stays language-neutral.

## 4. North-star acceptance criteria

Tier 2 is done when, in order, all of the following hold against the live service with a
disposable account:

1. **Login** — authenticate via `ClientApiLogin/Login` and obtain a usable session token.
2. **List nodes** — retrieve the node catalogue with each node's advertised egress IP.
3. **Connect** — establish an FV tunnel to a *chosen* node (not auto-match).
4. **Egress IP** — a request through the proxy exits from that node's advertised IP, verified
   against an independent IP-echo service.
5. **Switch without restart** — change node at runtime; the next request exits from the new
   node's IP; the listening proxy socket is never closed.
6. **Both front ends** — SOCKS5 and HTTP proxy each serve a request (HTTP proxy including
   `CONNECT`).
7. **No vendor binary** — the Tier 2 process tree contains no AJiaSu/`ClientApi` binary and no
   TAP device is installed or used.

Criteria 1–7 are unverified and will remain so until an account exists; see §5 and §6.

## 5. Risks

| Risk | Impact | Mitigation / current state |
|---|---|---|
| **Account / ToS** — programmatic use may breach the vendor's terms; automated login may trip captcha or anti-abuse. | Campaign blocked at the live step; account ban; legal exposure. | Static-only waves need no account. Live testing is Owner-authorized, single disposable account, low volume. Record ToS language in AJS-02 before any connect. |
| **Protocol version drift** — Windows payload is 4.9.6.0, Linux client 4.2.3.0, and `OpenAJS` does not state its target build. | Spec silently wrong; login works in lab, fails later. | Pin every claim to a build number; treat `OpenAJS` as a *hypothesis* until corroborated by strings/RTTI from 4.9.6.0 and 4.2.3.0. Version matrix in AJS-05. Age check: `OpenAJS` was created and last pushed **2026-06-07** (GitHub API, 2026-10-23), ~4 months before this baseline and close to the 4.9.x era — drift is bounded, but still unproven. |
| **Tunnel semantics may be raw IP, not stream** — if Q-8 resolves to a raw-IP tunnel, a SOCKS5 front end needs a userspace TCP/IP stack (the vendor ships `FVClientProxyLwip`, i.e. lwip), not a simple relay. | Tier 2 effort is materially larger; `OpenAJS`'s `FvStream` may be assuming stream semantics that the 4.9.6.0 build no longer has. | Do not treat "no TAP needed" as "trivial": the TAP-free path is confirmed, the *cost* of it is not. AJS-05 must classify FV framing explicitly (Q-8) before AJS-07 is scoped. |
| **OAEP hash ambiguity (SHA-1 vs SHA-256)** — `OpenAJS` defaults to SHA-1 and exposes `fv.oaep` to switch, with MGF1 selectable between SHA-1 and SHA-256; the vendor binary's actual parameter set is not yet confirmed. | Handshake fails, or works against one build and not another. | AJS-05 must state the parameter set as `[UNKNOWN]` with both candidates and the exact failure signature of each; AJS-06 resolves it empirically. |
| **Live testing needs Owner credentials** — none exist on this machine; no login/connect is permitted now. | Acceptance criteria 1–7 cannot be closed in wave 1. | Ask the Owner (§6). Do not substitute third-party accounts or public leaked credentials — ever. |
| **Open-source licence choice** — `OpenAJS` is Unlicense (public-domain dedication). Reimplementation is clean, but copying code or shipping a binary raises obligations. | Repo cannot be published without a licence decision; unclear provenance taints later release. | Default in force (alignment doc): **MIT**, local repo first, publication only on the Owner's word. Attribute `OpenAJS` as reference-only; never copy vendor binaries or resources (`res.fvr`, `sciter.dll`, `product.dat`). |

## 6. Open questions for the Owner

1. **Test account** — will the Owner supply a disposable AJiaSu account (and, if login needs a
   captcha/phone verification, a defined procedure for it)? Without one, Tier 2 stops at
   "spec + unit-tested crypto", and acceptance criteria 1–7 stay open.
2. **Publication** — public repo name and licence (MIT? Apache-2.0? Unlicense to match
   `OpenAJS`?), and the date of publication. Nothing is pushed: there is no remote.
3. **Scope of parity** — is Windows system-wide TUN parity in scope, or is a **proxy-only**
   API (SOCKS5 + HTTP, per-process) sufficient for the Owner's actual use? Proxy-only avoids the
   TAP driver entirely and removes the admin/install requirement.
4. **Egress policy** — should the API expose node *selection by IP*, by *region*, or both, and
   does it need sticky sessions per client key?
5. **Compliance boundary** — personal use of the Owner's own account, or a multi-account pool /
   outward-facing service? Default if unanswered: **own account only, no resale**.

**Defaults in force while unanswered** (alignment doc, 2026-10-23): no account → static and mock
verification only, every live criterion `[UNVERIFIED]`; proxy-only (no TUN); MIT; self-use only;
Linux/Docker as the target runtime.

## 7. Working rules for every slice

- Static only until the Owner authorizes a dynamic wave. No login, no connect, no installer
  execution, no driver/service install, no package install.
- No credentials in the repo, in reports, in commit messages, or in the ledger.
- Every claim cites a path, URL, or command. `[INFERENCE]` and `[UNKNOWN]` are mandatory where
  the observation is indirect.
- Reports in English. Binaries stay out of git (`.gitignore`).
- One writer per tree; a slice writes only inside its `# Non-Conflict` paths.

## 8. Product layer — panel decision (2026-10-23)

The emulator is the data plane; the missing product layer is designed in `docs/PANEL.md` and
evidenced in `research/70-panel/REPORT.md` (sources in `research/70-panel/sources.tsv`).

- **Control plane:** run [Resin](https://github.com/Resinat/Resin) v1.3.0 **unmodified** (MIT, Go).
  It provides the proxy entrypoint, panel, sticky leases anchored on egress IP, health checks,
  GeoIP and per-account request logs.
- **Integration:** `ajs-accountd` publishes one `socks5://127.0.0.1:<gate>#<region>-<n>` line per
  live `(account, server)` into Resin's local subscription `ajs-live`; no fork, no patch.
- **New daemon:** `ajs-accountd` — account vault, session supervisor, per-account concurrency gate,
  byte metering, subscription publisher, drain/rotation API, SQLite state.
- **Not implemented in this wave.** `emulator/` is unchanged; phase 1 needs no emulator change.

**License:** MIT — root [`LICENSE`](LICENSE), `Copyright (c) 2026 The LovePlusSpeedToProxy Authors`.
Third-party obligations are stated in `research/70-panel/REPORT.md`; no AGPL or unlicensed code is
copied.

**Use boundary:** self-use with accounts the operator owns or is explicitly **authorized** to use.
No public proxy service, no resale or sharing of access, no bypassing vendor account limits. The
operator is responsible for the vendor's terms of service and for local law.
