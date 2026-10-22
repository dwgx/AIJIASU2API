# QUESTION-TREE — AJiaSu → egress-selectable proxy

Root question:

> **RQ** — Can the AJiaSu / 爱加速 service be driven programmatically so that a local proxy
> endpoint can egress through an arbitrary chosen node's IP, without the vendor GUI and
> (north star) without any vendor binary?

Decomposition below is atomic: each `Q-n` is answerable by one slice, from one class of
evidence, with a yes/no or a concrete artifact as its answer. Status as of 2026-10-23.

## A. Service surface (what the API is)

| ID | Question | Answer type | Slice | Status |
|---|---|---|---|---|
| Q-1 | What is the complete `ClientApi*` HTTP endpoint set (login, register, captcha, node list, auto-match, favourites, notifications, upgrade, settings)? | endpoint table | AJS-02, AJS-05 | open — entry point is `POST https://login.ajs-app.com/client.php` (alignment probe 2026-10-23) |
| Q-2 | What are the mandatory common parameters (device id, MAC, version, channel, signature) and how is the signature computed? | field table + algorithm | AJS-05 | open |
| Q-3 | Which hostnames serve the API, and is there failover ordering? | host list | AJS-02 | open |
| Q-4 | How does login return a session — token, cookie, or both — and what is its lifetime/refresh rule? | flow | AJS-05 | open |
| Q-5 | How does the client obtain the node catalogue, and what does each node advertise (IP, region, load, tier)? | schema | AJS-05 | open |

## B. Transport (how traffic actually leaves)

| ID | Question | Answer type | Slice | Status |
|---|---|---|---|---|
| Q-6 | What is the FV transport: TCP, UDP, or both, on which ports, with what framing? | framing spec | AJS-05 | open |
| Q-7 | What is the FV handshake — key exchange, RSA-OAEP parameters, AES mode/key size/IV derivation, and the direction of the first encrypted byte? | state machine + params | AJS-05 | open |
| Q-8 | Is FV a raw IP tunnel (TUN semantics) or a stream/relay protocol (proxy semantics)? This decides whether a TAP driver is needed at all. | classification | AJS-03, AJS-05 | open |
| Q-9 | How is node selection expressed on the wire — a server id in the handshake, a separate API call, or a redirect? | field/flow | AJS-05 | open |
| Q-10 | Can an established FV session be re-pointed to another node without tearing down the local listener? | yes/no + method | AJS-06 | **blocked** |
| Q-11 | Does the service bind the exit IP to the node, i.e. does traffic from node *N* reliably appear from *N*'s advertised IP? | yes/no | AJS-06 | **blocked** |

## C. Vendor binaries (what they reveal)

| ID | Question | Answer type | Slice | Status |
|---|---|---|---|---|
| Q-12 | What are the 37 installer payload files, their versions, hashes, and roles? | inventory table | AJS-03 | open |
| Q-13 | What do the RTTI symbols `FVClientProxy`, `FVClientVpn`, `FVHttpClient`, `FVNetClient`, `FVDnsProxy`, `FVClientProxyLwip` imply about the client architecture (proxy vs VPN mode, DNS handling, lwip userspace stack)? | architecture note | AJS-03 | open |
| Q-14 | Is the bundled kernel driver proprietary? | yes/no + provenance | AJS-03 | **answered**: no — stock OpenVPN TAP-Windows V9, GPLv2 INF header, `DriverVer 04/21/2016, 9.00.00.21` (see PLAN §2) |
| Q-15 | Does the Linux client (4.2.3.0) use the same protocol as the Windows client (4.9.6.0)? Which endpoints/fields differ? | version matrix | AJS-04, AJS-05 | open |
| Q-16 | Does the Linux client require root/TUN, or can it run as an unprivileged proxy? | yes/no + syscall evidence | AJS-04 | open |
| Q-17 | What crypto primitives and constants are embedded in the binaries (RSA public key/modulus, AES mode, signature keys)? | constant table | AJS-03, AJS-04 | open |

## D. Prior art (what is already solved)

| ID | Question | Answer type | Slice | Status |
|---|---|---|---|---|
| Q-18 | What exactly does `cubk1/OpenAJS` implement, and which of its parts are corroborated by our own binary evidence? | mapping table | AJS-02, AJS-05 | open |
| Q-19 | What do `s1g0day/Aijiasu_Agent_Pool` and `hanx11192-ship-it/LoveToSpeedup` do — orchestration of the official client, or protocol reimplementation? | classification | AJS-02 | open |
| Q-20 | Are there other public projects, write-ups, or CVEs touching AJiaSu/FV? | source list | AJS-02 | open |
| Q-21 | What licence does each prior-art repo carry, and what does that permit? | licence table | AJS-02 | open |

## E. Product (what we must build)

| ID | Question | Answer type | Slice | Status |
|---|---|---|---|---|
| Q-22 | What is the minimum viable API surface (auth, node list, connect, switch, SOCKS5, HTTP)? | interface spec | AJS-05, AJS-07 | open |
| Q-23 | Which language/runtime for the emulator, given a later open-source release? | decision | Owner | **answered** (alignment 2026-10-23, reversible): Go single binary; Python for fixtures |
| Q-24 | Is system-wide TUN parity required, or is proxy-only sufficient? | decision | Owner | **blocked** (Owner question) |

## Frontier

**Answerable now — static evidence on this machine, no account, no network to the service:**

- Q-1..Q-3, Q-5 (endpoint/host/schema facts) — from `OpenAJS` source + binary strings.
- Q-6..Q-9 (transport, handshake parameters, tunnel-vs-proxy, node selection) — from binary
  disassembly and prior art; answers carry `[INFERENCE]` where the vendor binary cannot settle it.
- Q-12..Q-17 (inventory, RTTI architecture, driver provenance, version matrix, privilege needs,
  embedded constants).
- Q-18..Q-21 (prior-art mapping and licences).
- Q-22 (minimum API surface).

**Blocked on an account / live service — must not be attempted in wave 1:**

- Q-4 (session lifetime under real login), Q-10 (hot node switch), Q-11 (egress IP binding).
- Q-24 is blocked on the **Owner**, not on an account.

**Reserved for later waves:** dynamic-lab capture (AJS-06), emulator conformance (AJS-07),
independent verification (AJS-08).

## Evidence seeds (verified 2026-10-23, safe to build on)

Each seed names where it came from, so a slice can re-derive it instead of trusting this file.

| Seed | Value | Source |
|---|---|---|
| S-1 API hosts | `login.ajs-app.com`, `login.mmegg.com`, `login.mmegg.com:4443`, `login.mbclubsh.com` | `OpenAJS` `AjsClient.java` (constructor, master branch), corroborated by `login.ajs-app.com` inside the Linux ELF |
| S-2 API style | `POST` with a `TreeMap` of common params to paths such as `ClientApiLogin/Login`, `ClientApiVpnServer/AutoMatch`, `ClientApiUser/UserVpnServerFavoriteList` | `OpenAJS` `AjsClient.java` |
| S-3 Transport entry | `POST https://login.ajs-app.com/client.php`; no credentials → `AuthResult: WrongPassword`; TLS cert self-signed `CN=localhost`, client skips verification | alignment probe 2026-10-23 (`ALIGN-aijiasu-proxy-20261023.md`) |
| S-4 FV crypto shape | RSA public key from `(n, e)`; `RSA/ECB/OAEPPadding` with OAEP hash default `SHA-1`, switchable via `-Dfv.oaep`; MGF1 SHA-1 or SHA-256; payload `AES/CTR/NoPadding` with explicit key + IV | `OpenAJS` `FvCrypto.java` |
| S-5 FV classes | `FvConnection`, `FvCrypto`, `FvStream`, `SocksProxyServer` — a stream/relay design that already ends in SOCKS5. Repo created and last pushed 2026-06-07 (GitHub API, 2026-10-23). | `OpenAJS` source tree + repo metadata. **Caveat:** the stream shape is the reference author's design choice, not proof of the wire format — Q-8 must settle it against the 4.9.6.0 binary, which also carries `FVClientProxyLwip` (a userspace IP stack). |
| S-6 Windows RTTI | `FVClientProxy`, `FVClientVpn`, `FVHttpClient`, `FVNetClient`, `FVDnsProxy`, `FVClientProxyLwip` | strings of `AJiaSu.exe` 4.9.6.0 |
| S-7 TAP provenance | stock OpenVPN TAP-Windows: GPLv2 INF header, `Provider = "TAP-Windows Provider V9"`, `DriverVer = 04/21/2016, 9.00.00.21`, `tapinstall.pdb` path `tap-windows6-mattock` | `tap/tap/amd64/OemVista.inf`, `tapinstall.exe` in `ajs-win-20261023` |
| S-8 Linux client | 4.2.3.0, static stripped ELF x86-64, 3,578,792 bytes | `ajs-linux-20261023/ajiasu` |

Seeds are starting points, not conclusions: a slice that contradicts a seed must say so in its
report and the seed is corrected here.

## Answer discipline

- One question, one answer, one artifact path. If an answer is inferred rather than observed,
  it is tagged `[INFERENCE]`; if no artifact settles it, `[UNKNOWN]`.
- A question may be **split** when its answer needs two evidence classes, never merged to look
  complete.
- No question is answered "no" merely because a search returned nothing — absence of evidence is
  recorded as `[UNKNOWN]` with the search performed.
