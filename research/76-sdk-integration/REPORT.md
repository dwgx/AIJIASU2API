# AJS-16 — SDK and protocol integration layer

Status: design complete, no implementation · Date: 2026-10-23 · Slice: AJS-16 · Branch `panel-design` @ `0456212`

Companions: `INTERFACE.md` (Go interfaces, HTTP schemas, state machines), `validate.py` (artifact gate).

Inputs read for this slice: `research/40-protocol/PROTOCOL.md` (control API, FV crypto,
carrier), `research/50-windows-protocol/REPORT.md` (Windows 4.9.6.0 static reverse),
`research/60-dynamic/REPORT.md` (AJS-07 Linux 4.2.3.0 capture), `emulator/README.md`
(existing Go implementation), `docs/PANEL.md` (panel design), and the emulator sources named
inline. No live account, no vendor host, no credential. Every claim below carries a path; a
claim without one is `[INFERENCE]` by construction.

Notation: `[CONFIRMED]` = reproduced from a capture or from two independent sources;
`[INFERENCE]` = derived from code or naming, not observed; `[UNKNOWN]` = no evidence, and the
design must make it pluggable. Numbers state their denominator and date.

## Answer

1. **The layer is four protocol codecs, thirteen Go interfaces, three state machines, and one
   rule: no guessed wire bytes.** Production path is P1 (control API) + P2 (binary carrier) +
   P4 (SOCKS5 data channel). P3 (OpenAJS raw frame) is a fallback codec. P5–P8 are registered
   but excluded, passive, or mapping-only. See `## Protocol surface`.
2. **The carrier notation in `research/40-protocol/PROTOCOL.md` is wrong in one bracket.**
   Shape A is written `[16B K][AES-CTR(K,0)(len||text||body)]`; the captured byte counts prove
   the body is **outside** the K stream: 679 = 16 + 4 + 234 + 425
   (`research/60-dynamic/captures/fvmock-1.bin`) and the 425-byte body decrypts under
   `AES-CTR(sessionKey, IV=n)`, not under K. `emulator/README.md` §Limits states the correct
   form. Design uses `[16B K][CTR(K,0)(4B BE len||text)][CTR(sessionKey,n)(body)]`. `[CONFIRMED]`
   correction; the PROTOCOL.md bracket is `[WEAKENED]`.
3. **Version negotiation does not exist in the evidence.** `_v`, `cv`, `protocolVersion`,
   `implType` are client-side claims (`AjsDevice.java:14-53`, `prepare_core` log at Windows
   `0x00526560`); no response field echoes a negotiated version (`research/40-protocol/PROTOCOL.md`
   §Version matrix). The SDK therefore *selects* a persisted `VersionProfile` per account and
   *classifies failures* to fall back — it never blocks on a handshake that the peer may not
   implement.
4. **Concurrency, not bandwidth, is the scarce resource.** Authentication wraps one 16-byte
   session key with RSA-OAEP (`k=`), and every data channel reuses it with `k=` empty
   (`research/60-dynamic/REPORT.md` Answer 3). So per-flow cost is one TCP connect plus AES —
   and the vendor sees N simultaneous TCP connections per account under one `sid`. Limit is
   `[UNKNOWN]`; `auth.limit` in the Linux token table (`PROTOCOL.md` §Response fields) implies
   one exists. Design: per-account AIMD semaphore, default 32, ceiling operator-set.
5. **The gate is load-bearing and Resin must never dial `ajsproxy` directly.** Without the gate
   there is no admission control, no byte accounting, no drain, and no stable node identity.
   `docs/PANEL.md` §1 already routes Resin → gate → `ajsproxy`; this slice makes the gate a
   specified component with its own control port.
6. **Drain order is gate-first, not line-last.** `docs/PANEL.md` §8 removes the subscription
   line after the grace period. That keeps Resin scheduling new requests at a node that is
   already refusing them. Correct order: gate rejects admission (instant) → publish removal
   (≤5 s coalesced) → delete leases → grace 30 s for in-flight → stop process. `[INFERENCE]`
   on Resin's refresh lag; the ordering is safe under both immediate and 30 s refresh.
7. **Fifteen failure classes, eight action verbs, and two hard rules:** only handshakes retry;
   a flow never retries after any payload byte has moved (TCP cannot be replayed).
   Auth-class failures circuit-break the *account*; transport failures circuit-break the
   *node*; drain is the only operation that removes a node from Resin.
8. **Cross-platform is one static binary plus a supervisor, no kernel driver.** Linux is
   primary; macOS and Windows share the same userland loopback path. TUN is declared as an
   interface and left unimplemented — every shipping mode we have evidence for is a userland
   SOCKS5 proxy (`research/60-dynamic/REPORT.md` §Client behavior: `protocol proxy`, no root).
9. **Observability is 20 metrics, one JSON log schema, 13 audit actions, and a flow id.**
   Server-controlled text (`r`, `ft`, `fm`) is attacker-influenced and must be sanitized before
   logging: the existing `AuthError.Error()` (`emulator/internal/fv/auth.go:100-115`) concatenates
   `ft`/`fm` raw, which permits log injection. Patch required before the first daemon release;
   design assumes it.
10. **Unproven and therefore seamed:** keepalive (`ka`/`kat`/`kapf`, `ping`/`pong`), `sid`
    lifetime, vendor concurrency ceiling, `ExpireTime` units, OPV2 role, UDP/lwip/TUN, and the
    Windows signing secret. Each has a named default, a metric that would falsify it, and a
    pluggable interface. See `## Open questions`.

## Protocol surface

Eight surfaces. `Emulator` is the state of `emulator/` today; `Evidence` is how well the wire
format is known; `SDK role` is what the integration layer does with it.

| ID | Protocol | Wire carrier | Emulator | Evidence | SDK role |
|---|---|---|---|---|---|
| `P1` | HTTP control API (`/client.php`) | HTTPS form POST, signed or unsigned | `IMPLEMENTED` | `CONFIRMED` | `PRIMARY` |
| `P2` | FV binary proxy carrier | TCP `[16B K][CTR(K,0)(len\|\|text)][body]` | `IMPLEMENTED` | `CONFIRMED` | `PRIMARY` |
| `P3` | FV raw-TCP frame (OpenAJS) | TCP `[16B IV][CTR(4B hdr\|\|text)][body]` | `IMPLEMENTED` | `INFERENCE` | `FALLBACK` |
| `P4` | SOCKS5 data channel | Body of P2/P3, one TCP per flow | `IMPLEMENTED` | `CONFIRMED` | `PRIMARY` |
| `P5` | OPV2 VPN/control frame | TCP TLV, outer CTR | `PARTIAL` | `INFERENCE` | `PLUGGABLE` |
| `P6` | Keepalive / ping-pong | Unknown; `ka`/`kat`/`kapf` hints | `NONE` | `UNKNOWN` | `PASSIVE-ONLY` |
| `P7` | UDP / lwip / TUN / VPN modes | Unknown | `NONE` | `UNKNOWN` | `EXCLUDED` |
| `P8` | Error tokens (`r`, `ft`, `fm`) | Text inside P2/P3/P5 | `PARTIAL` | `INFERENCE` | `MAPPED` |

### P1 — HTTP control API

- Carrier: `POST https://<host>/client.php`, `Content-Type: application/x-www-form-urlencoded`,
  body = device common params + `cmd` + endpoint params, keys sorted ascending, values
  percent-encoded with lowercase hex, no trailing `&` (`research/40-protocol/PROTOCOL.md`
  §Request format; `AjsClient.java:69-102` via that doc).
- Hosts: the four-host fallback list from `product.dat` `ServerApiUrls`
  (`research/50-windows-protocol/REPORT.md` §product.dat):
  `login.ajs-app.com`, `login.mmegg.com`, `login.mmegg.com:4443`, `login.mbclubsh.com`.
  Failover advances **only on transport error**, never on 4xx/5xx or bad JSON
  (`PROTOCOL.md` §Hosts and failover). 15 s connect, 20 s read.
- Signing: `X-Timestamp` (epoch **milliseconds**) + `X-Sig` = 16 lowercase hex of
  `dualHash(secret|body|ts|secret)`. Two client behaviours exist and both must be supported:
  OpenAJS signs (`AjsClient.java:71-87`), Linux 4.2.3.0 does not send either header
  (`research/60-dynamic/captures/control.jsonl`). The shipping secret is `[UNKNOWN]`
  (`PROTOCOL.md` §Open questions 2). Design: `SigningMode` ∈ {`none`, `openajs`} per profile,
  never a hard-coded constant in the hot path.
- Responses: JSON; Linux 4.2.3.0 requires the last byte to be `}`, `SyncVersion` to be an
  **object**, and `Login.UrlWeb`/`Login.LatestVersion` to exist, else it aborts
  (`research/60-dynamic/REPORT.md` §Control-plane capture). The SDK must therefore validate the
  envelope it receives and never emit a request whose response it cannot parse — a
  `CLIENT_PARSE_FAILURE` is a protocol bug on our side or a captive portal, not an account fault.
- Status: `IMPLEMENTED` (`emulator/internal/api`) with the signed variant only; the unsigned
  Linux variant is `NONE` and is a P1 backlog item, not a redesign.

### P2 — FV binary proxy carrier (production)

```
request  = [16B K][ CTR(K, IV=0)( 4B BE text_len || text ) ][ body ]
response = [ CTR(K, IV=0)( 4B BE len || text ) ]
auth map = CTR(sessionKey, IV=n)( key=value\n... )      then server half-closes
```

- `K` is a fresh cleartext 16-byte per-connection key; the 4-byte length counts the UTF-8 text
  only; the body is a **separate** CTR stream under `sessionKey`/`n`.
- Auth text: `s=\nn=<32 hex>\nk=<base64(\"OAEP\"||RSA-OAEP-SHA1(sessionKey))>\nl=<body len>\n_=<0..199 a-z>`.
- Data text: `s=<sid>\nn=<fresh div>\nk=\n_=<pad>` — no `l=` field.
- Response text contains `code=200`; the client parses the auth map only on EOF
  (`research/60-dynamic/REPORT.md` Answer 5). A server that does not `shutdown(SHUT_WR)` stalls
  the client until timeout.
- Evidence: `captures/fvmock-1.bin` (679 B), `fvmock-2.bin` (207 B),
  `research/50-windows-protocol/REPORT.md` §Answer 2 (builder `0x00552ce0` emits the same
  structure), `emulator/internal/fv/binary.go`.
- Status: `IMPLEMENTED`; live compatibility `[UNKNOWN]` — no vendor host was contacted.

### P3 — FV raw-TCP frame (fallback)

```
request  = [16B headerIV][ CTR(headerIV,0)( 00 00 len16 || text ) ][ CTR(sessionKey,bodyIV)(body) ]
response = [ CTR(headerIV,0)(4B hdr) ][ CTR(headerIV,0)(text) ][ CTR(sessionKey,bodyIV)(body) ]
```

- The 16-bit length is OpenAJS's; the shipping Linux binary uses a 32-bit length in P2. This is
  why P3 is a fallback, not the default: no capture shows a server speaking this shape, and the
  response body has no length field — it is delimited by a 500 ms idle heuristic
  (`emulator/README.md` §Limits).
- Status: `IMPLEMENTED` (`emulator/internal/fv/rawtcp.go`), evidence `INFERENCE`
  (`PROTOCOL.md` §FV transport, `FvConnection.java:159-205` via that doc).

### P4 — SOCKS5 data channel

- One **new TCP connection per proxied flow** to the same `svr:par.fv.p`, same `sid`, fresh
  `div`; body is the raw SOCKS5 CONNECT `05 01 00 03 <len> <host> <port BE>` encrypted with
  `CTR(sessionKey, div)` (`research/60-dynamic/REPORT.md` Answer 3).
- Reply: `code=200` in the header text, then exactly 10 bytes of `CTR(sessionKey, div)` whose
  first two bytes must be `05 00`; every later byte in each direction continues its own CTR
  stream (`PROTOCOL.md` §Relay). A 10-byte read is a protocol constant, not a heuristic.
- Evidence: capture `fvmock-2.bin` for the request; reply/relay `[INFERENCE]` — the mock's relay
  reply was not validated by the client before cleanup (`research/60-dynamic/REPORT.md` §Open
  questions).
- Status: `IMPLEMENTED` (`emulator/internal/fv/*`, `emulator/internal/proxy/socks5.go`).
  The SDK treats "reply ok + relay" as one atomic unit: a failure after `05 00` is a flow
  failure, not a session failure.

### P5 — OPV2 VPN/control frame

```
[keyA^keyB][keyB][crc32le][\"OPV2\"][01][u16be n1][payload][02][u16be n2][CTR(sessionKey,0)(map)][00]
then CTR(keyA, IV=0) over bytes [36..end]
```

- Layout `[CONFIRMED]` byte-for-byte from Windows `0x00551f40` and Linux `0x45080b`
  (`research/50-windows-protocol/REPORT.md` §OPV2 framing); **role `[UNKNOWN]`** — absent from
  both proxy captures, reachable only from `FVClientVpn` (`0x0056bd90`).
- keyA/keyB are fresh random per packet and both halves travel in the clear, so the frame key is
  not secret against an observer (`[CONFIRMED]` by layout; security assessment `[INFERENCE]`).
- SDK role `PLUGGABLE`: the codec is registered and unit-tested but no production path selects
  it. Enabling it requires a capture proving it on the proxy path; nothing else will flip it.

### P6 — Keepalive / ping-pong

- What exists: `ka`, `kat`, `kapf` in the auth map (`[INFERENCE]` = interval/timeout/flag);
  single-word control verbs `con ping pong off sid aur fm ft au` in the Linux binary
  (`PROTOCOL.md` §Response fields); Windows timer states `200` (keep-alive window), `0x65`
  (retry `emitObscurePacket`), `0x66` (retry `emitClientAuth`), and a 9 s connect-auth timeout
  (`research/50-windows-protocol/REPORT.md` §Call flow).
- What is missing: framing, numeric command codes, and whether the proxy path uses any of it.
  `[UNKNOWN]` — do **not** invent a `ping` frame.
- SDK role `PASSIVE-ONLY`: liveness is inferred from observable behaviour — a data channel that
  answers `code=200`, or an idle-socket read deadline — and recovery is reconnect, not ping.
  The `Keepalive` interface exists so a future capture can drop in a real implementation without
  touching the session manager.

### P7 — UDP / lwip / TUN / VPN modes

- Shipping binaries contain `FVClientProxyLwip`, `FVDnsProxy`, `FVNetClient`, `vpn://`,
  `udp://`, `proxy-tun` (`research/50-windows-protocol/REPORT.md` §Function map; `PROTOCOL.md`
  §Open questions 14). `FVClientProxySocks` / `FVClientProxyLwip` have RTTI descriptors but no
  COL, so their vtables are `[UNKNOWN]`.
- No wire format, no port, no framing. `OUT-OF-SCOPE` for this design. The SDK must not
  advertise capability it cannot deliver: the gate speaks TCP SOCKS5 only, and a UDP ASSOCIATE
  request receives `0x07` (command not supported).

### P8 — Error tokens

- Observed literals: `auth.password`, `auth.group`, `auth.forbidden`, `auth.membership`,
  `auth.limit`, `auth.off`, `session.timeout`, plus `client request failed`,
  `network buffer broken`, `can not connect to server`
  (`PROTOCOL.md` §Error and edge cases, Linux offsets `0x28b2d0`-`0x28b31c`).
- Mapping token → `r` value → user-visible class is `[INFERENCE]`; the literals are `[CONFIRMED]`
  as strings. `r=ok` is the only token OpenAJS compares (`FvConnection.java:220-240` via
  `PROTOCOL.md`), so any other value is a reject.
- SDK role `MAPPED`: the token table is **data** (`classify.go` table + test), not control flow.
  Unknown tokens map to `FV_AUTH_REJECT` and are preserved verbatim (sanitized) in the audit
  record, because the first live reject is the experiment that closes the mapping.

## Adapter contract

The adapter is the only code that knows the vendor. Everything above it (gate, publisher,
rotation) sees an account id, a node id, and a `net.Conn`. Everything below it is a codec.

### Account and session state machine

```
NEW ──login──▶ LOGGING_IN ──ok──▶ AUTHENTICATED ──sync──▶ SERVERS_OK ──session──▶ READY
                   │                    │                    │                     │
                   │ AuthResult != OK   │ Expired / past     │ no usable server    │ drain
                   ▼                    ▼                    ▼                     ▼
              AUTH_FAILED        MEMBERSHIP_EXPIRED      (stay SERVERS_OK)     DRAINING
                                                                                 │
                                       operator ──▶ SUSPENDED                    ▼
                                                                               STOPPED
```

Canonical account states: `NEW`, `LOGGING_IN`, `AUTHENTICATED`, `SERVERS_OK`, `READY`, `DRAINING`,
`STOPPED`, `AUTH_FAILED`, `MEMBERSHIP_EXPIRED`, `SUSPENDED`. Node states (the five already used in
`docs/PANEL.md` §6): `STARTING`, `READY`, `LIVE`, `DRAINING`, `DOWN`. Flow states: `ACCEPTED`,
`ADMITTED`, `CONNECT_SENT`, `REMOTE_OK`, `RELAY`, `CLOSED`, plus the terminal `REJECTED`.

- `AUTH_FAILED` is terminal for the credential until an operator PATCHes it. Three login
  attempts per 15 minutes, then latch (`docs/PANEL.md` §8).
- `MEMBERSHIP_EXPIRED` is terminal for the account *and* triggers drain of every node: continuing
  to open data channels on an expired account only burns the vendor's error budget.
- `READY` is a per-account state; a single account may hold several nodes, each with its own
  `FlowSession`. `SERVERS_OK` with an empty map is legal and is not an error state.
- `SUSPENDED` is the kill switch (`docs/PANEL.md` §3 `POST /v1/users/{id}/suspend`); it does not
  change account state, it changes the Resin platform filter.

### login → loginSync → Servers

| Step | Request | Response fields used | Failure class |
|---|---|---|---|
| `Login` | `cmd=ClientApiLogin/Login`, `username`, `password`, `trigger=login` | `AuthResult`, `AuthMessage`, `LoginToken`, `UserName`, `ConnectPassword`, `Expired`, `ExpireTime`, `MembershipStatus`, `UrlWeb`, `LatestVersion`, `SyncVersion` | `AUTH_REJECTED`, `MEMBERSHIP_EXPIRED`, `CLIENT_PARSE_FAILURE` |
| `LoginSync` | same `cmd` + `_client_login=<LoginToken>` + `username` | `Login.ConnectPassword`, `Servers{}` | `AUTH_REJECTED`, `CLIENT_PARSE_FAILURE` |
| `Servers` | `LoginSync` and take `Servers` | `id`, `svr`, `par.fv.p`, `sts`, `tit`, `ctry` | `SERVER_GONE` per node |

- `LoginToken` is the sync credential; `UserName`/`ConnectPassword` are the FV credentials and are
  **different** secrets (`AjsClient.java:116-120` via `PROTOCOL.md` §Login). `par.fv.u` /
  `par.fv.ps` are returned by the API but unused by OpenAJS — do not invent a role for them
  (`[UNKNOWN]`, `PROTOCOL.md` §Open questions 11).
- `LoginSync` is the rotation point: it refreshes `ConnectPassword` without a new password. Call
  it before every session open when the last sync is older than 10 minutes (`[INFERENCE]`:
  no observed TTL, but the cost is one HTTPS request and the failure mode of a stale password is
  a full re-login).
- The client must validate the envelope before use: body ends with `}`, `SyncVersion` is an
  object, `Login.UrlWeb` and `Login.LatestVersion` exist. Skipping this is what aborts the
  shipping Linux client (`research/60-dynamic/REPORT.md` §Control-plane capture) and would
  surface to us as an opaque parse error.

### Session creation and one data channel per flow

1. `LoginSync` (refresh `ConnectPassword`) → pick server by `sts == ok` and a usable
   `svr:par.fv.p`.
2. Open TCP to the FV endpoint, 15 s connect / 20 s handshake read (`PROXY` timeouts inherited
   from `emulator/internal/fv/transport.go:52-60`).
3. Generate `sessionKey`, `headerIV`, `bodyIV` (16 bytes each, CSPRNG). RSA-OAEP-SHA1 the session
   key into `k=`, send the auth frame, read the `code=200` block, read the auth map **on EOF**.
4. `r == ok` and `sid` non-empty → `READY`. Anything else → `FV_AUTH_REJECT`.
5. Each flow: new TCP connection, `s=<sid>`, fresh `div`, `k=` empty, SOCKS5 CONNECT body,
   `code=200` + 10-byte reply, then relay.
6. Teardown: close the flow socket (the FV side sees EOF); close the session control socket;
   zero `sessionKey` in place. There is **no** protocol-level logout for a proxy session —
   the FV control socket is idle after auth, and no `off` frame is evidenced on this path.

Per-flow cost: 1 TCP handshake + ~200 B of framing + 1 RTT for `code=200`. No TLS, no RSA.
Consequence for the panel: flow setup latency is dominated by RTT to the FV endpoint, and the
semaphore — not CPU — is the throughput limit.

### Version selection (`_v`, `cv`, `protocolVersion`, `implType`)

| Field | Where | Meaning | Source |
|---|---|---|---|
| `_v` | control API common params | client version claim (`4.11.10.0` OpenAJS, `4.9.6.0` Win, `4.2.3.0` Linux) | `[CONFIRMED]` |
| `cv` | FV auth body | core build stamp (`20241105-1`, `20260525-1`) | `[CONFIRMED]` presence, `[UNKNOWN]` acceptance |
| `protocolVersion` | client runtime | `<2` text/upgrade, `>=2` binary carrier | `[CONFIRMED]` for the 2 branch |
| `implType` | client runtime | `1` tun, `2` socks (`[INFERENCE]` from `vpnConnect` `0x00448f40`) | `[INFERENCE]` |

A `VersionProfile` is `{_v, cv, protocolVersion, implType, oaep_hash, signing_mode, transport}`.
Selection algorithm:

1. Start from the profile persisted for the account, else the platform default
   (`linux-4.2.3.0` → `protocolVersion=2`, `implType=2`, `oaep=SHA-1`, `signing=none`).
2. On `FV_AUTH_REJECT` with a valid control-API login, retry **once** with the next profile in
   the ordered list (`binary → rawtcp`; `SHA-1 → SHA-256` is last because AJS-07 showed SHA-256
   fails on the Linux mock).
3. Persist the profile that worked, with the timestamp and the evidence class. Never persist a
   profile that was only "not yet falsified".
4. There is no server-side negotiation to wait for. A design that blocks on a version response
   would hang on every peer we have evidence for.

### Device identity

- `_uid`: 22 chars = `%08x%02x%08x` + first 4 hex of `dualHash(base)` (`AjsDevice.java:30-36` via
  `PROTOCOL.md` §Common device parameters). Self-consistent, not secret.
- `_os_did`: 16 random hex, generated once per install by OpenAJS. In FV, `cuid` and `cseid` are
  the same value; the Linux 4.2.3.0 capture used a 20-hex `cuid`
  (`research/60-dynamic/REPORT.md` §Decryption).
- **Rule: device identity is per-account, generated once, sealed, and never regenerated on
  retry.** A retry that changes `cuid` looks like a new device to the vendor, which is the
  fastest way to convert a transient error into an anti-abuse event (`[INFERENCE]` — no evidence
  of the server's policy, and that is exactly why the design does not gamble).
- Identity is bound to the account row, not to the host: moving `ajs.db` to another machine keeps
  identities stable.

### Retry and backoff

| Operation | Retryable | Budget | Backoff | Circuit |
|---|---|---|---|---|
| Control API transport error | yes | 3 attempts per host, 4 hosts | 1 s, 2 s, 4 s + jitter | node-level `TRANSPORT_DOWN` after 5 |
| Control API 4xx/5xx or bad JSON | no | — | — | `CLIENT_PARSE_FAILURE`, 60 s cool-down, alert |
| Login `AuthResult != OK` | no | 3 per 15 min | — | `AUTH_FAILED` latch |
| FV auth (`r != ok`) | once, after a fresh `LoginSync` | 2 | 5 s | `FV_AUTH_REJECT`, account circuit 5 min |
| Data channel open | once, same `sid` | 2 | 250 ms | then `DATA_CHANNEL_REJECT`, node probe |
| Relay after first byte | **never** | — | — | flow closes; TCP cannot be replayed |
| Canary | yes | 2 per cycle | 2 s | `RESIN_PROBE_FAILURE` after 2 cycles |

Jitter is full-jitter (`sleep = rand(0, base*2^n)`), capped at 30 min (`docs/PANEL.md` §7
`AJS_LOGIN_BACKOFF`). Retries are idempotent-only: login, sync, FV auth, canary, and a flow open
that has not yet received `code=200`.

### Credential handling

| Secret | Lifetime | At rest | In logs | In argv/env |
|---|---|---|---|---|
| account password | until rotation | AES-256-GCM sealed, account id as AAD (`docs/PANEL.md` §6) | never | never |
| `LoginToken` | ≤ 10 min in memory | never persisted | never | never |
| `ConnectPassword` | until next sync | never persisted | never | never |
| `sessionKey` | one FV session | never persisted | never | never |
| `sid` | one session | node row, truncated to 6 chars for display | first 6 chars | never |
| `k=` (OAEP block) | one auth frame | never | never | never |

- The password reaches the control API in plaintext by protocol design; TLS verification is
  therefore mandatory on our side even though OpenAJS disables it (`InsecureTLS.java:13-41` via
  `PROTOCOL.md`). `[CONFIRMED]` that OpenAJS is insecure; `[UNKNOWN]` whether the vendor requires
  a specific CA — the design pins the system trust store and makes pinning an operator option.
- `ajsproxy` receives credentials through a `0600` config file in a private temp dir, never argv
  (`emulator/README.md` §Config example warns the same). The file is unlinked immediately after
  the process reads it `[INFERENCE]`: we cannot prove when `ajsproxy` reads it, so the supervisor
  unlinks on the "listening on" line and keeps the directory `0700`.
- Zeroization: Go cannot guarantee it, but the adapter overwrites the key slices it owns before
  dropping them, and never formats secrets with `%v` on a struct that contains them (all secret
  fields are `[]byte` or a `Secret` type with a redacting `String()`).

## Panel integration

```
client ─▶ Resin :2260 ─▶ gate :13000+n ─▶ ajsproxy :12000+n ─▶ FV :443 ─▶ target
             ▲                 ▲                 ▲
             │ subscription    │ admission,      │ one process per
             │ "ajs-live"      │ bytes, drain    │ (account, server)
        ajs-accountd :2261 ─────┴─────────────────┘
             │ gate control :14000+n (loopback)
             └─ ajs.db (SQLite WAL)
```

Ports extend `docs/PANEL.md` §2 with a third range: `14000–14999` for gate control. A gate that
exposes its state on the same socket as SOCKS5 would need protocol sniffing; a separate loopback
port is one more row in the slot table and zero ambiguity.

### ajsproxy process contract

- One process per `(account, server)`, invoked as
  `ajsproxy serve-socks -config <dir>/node.json -listen 127.0.0.1:<internal_port>`.
- Config carries host list, credentials, `transport`, `server`, `core_version`, `oaep_hash`,
  device overrides (`emulator/README.md` §Config example). Written `0600` in a `0700` directory
  under `AJS_STATE_DIR`.
- Readiness = the line `listening on <addr> via <transport> (<server id>, <addr>), sid=<sid>` on
  stdout **and** a TCP connect that completes the SOCKS5 greeting with `05 00`. Timeout 20 s
  (`docs/PANEL.md` §8).
- **`serve-socks` authenticates once and never re-authenticates** (`emulator/cmd/ajsproxy/main.go:320-360`:
  one `openSession`, then `proxy.SOCKS5.Serve`). If the vendor expires the `sid`, every flow fails
  while the process still looks healthy. Therefore: the supervisor watches the gate's
  `DATA_CHANNEL_REJECT` rate per node; 3 rejects in 60 s → restart the process (fresh login,
  fresh session). This is a supervision rule, not a change to `emulator/`.
- Stdout carries `sid=`. The supervisor captures stdout, extracts the first 6 characters for
  logs, and drops the rest. Never forward child stdout to the daemon's log verbatim.
- Stop: close the gate first, then `SIGTERM` (POSIX) / `GenerateConsoleCtrlEvent` or Job Object
  termination (Windows), wait 5 s, then kill the process tree.

### Gate

The gate is a SOCKS5 server that terminates the client's SOCKS5 handshake itself, then opens a
SOCKS5 CONNECT to `ajsproxy` and splices bytes. It parses only the greeting and the CONNECT
request — enough to count flows, apply admission, and classify replies — then becomes a
byte pump.

- Admission order: node state `LIVE` → per-account semaphore → per-node semaphore → global cap.
  Any refusal replies immediately with `0x02` (connection not allowed by ruleset) and increments
  `ajs_gate_reject_total{reason}`.
- Concurrency: `account_limit` default 32 (policy default, **not** a discovered vendor limit),
  `node_limit` default 64, `global_limit` default 512. AIMD: on `CONCURRENCY_REJECT`, halve the
  account limit to a floor of 4 and record the event; raise by 1 per 10 clean minutes back to the
  configured ceiling. Never exceed the ceiling an operator set.
- Byte accounting: count bytes in both directions per flow, aggregate to
  `usage_daily(day, user_id, account_id, node_id)`. Attribution to a user is by Resin's
  `Platform.Account`, which the gate cannot see (it is Resin that authenticates the client).
  Therefore per-user attribution is **advisory** until the P3 gateway exists
  (`docs/PANEL.md` §9): the gate attributes to the account, Resin attributes to the user, and
  reconciliation joins them by time bucket. State this limitation in the console.
- Drain: `POST /drain {grace_seconds}` flips admission to refuse-new immediately; in-flight flows
  are untouched; after `grace_seconds` or when `active_flows == 0`, the gate closes the listener
  and reports `DRAINING → STOPPED`. `POST /undrain` is only legal while the listener is open.
- Flow id: 16 hex chars per flow, in gate logs only. It never appears on the wire.

### Subscription render and publish

- Line format (from `docs/PANEL.md` §5): `socks5://127.0.0.1:<gate_port>#<cc>-<subdiv>-<seq>`.
  No credentials: the gate is loopback-only, same trust boundary as Resin.
- Render is deterministic: nodes sorted by `(region_tag, node_id)`, LF-joined, no trailing
  newline (`[INFERENCE]` on trailing-newline tolerance; the parser splits on lines).
- Publish: ensure the subscription exists (`GET ?keyword=ajs-live`, else `POST` with
  `source_type=local`, `update_interval=30s`, `ephemeral=true`), then `PATCH {content}` at most
  once per 5 s. A PATCH is followed by `POST /actions/refresh` only when a deterministic sync
  point is needed (drain completion, rotation).
- **Never publish an empty subscription while nodes are alive.** Empty content is only legal when
  every node is `DOWN` and the operator has acknowledged it; otherwise keep the last good content
  and set `ajs_publish_age_seconds` rising.
- If PATCH succeeds and refresh fails: retry refresh with backoff, keep the last good content,
  and do not re-PATCH (idempotent content hash check).

### Lease and rotation

```
POST /v1/users/{id}/rotate {avoid_current_ip: true}
  1. read platform regex_filters
  2. append "!^ajs-live/<tag>$"          → PATCH /api/v1/platforms/{id}
  3. POST /api/v1/subscriptions/{id}/actions/refresh
  4. DELETE /api/v1/platforms/{id}/leases/{account}
  5. wait 10 s (Resin re-pins on next request)
  6. restore original regex_filters      → PATCH
```

- Rate limit: one rotation per user per 10 s (`docs/PANEL.md` §8). A second request inside the
  window returns `409 CONFLICT` with `retry_after_ms`.
- If step 2 succeeds and step 6 fails, the user is left with a permanent exclusion: the operator
  must be able to see it. Store `rotation_pending` in the users row and reconcile on startup.
- `avoid_current_ip` requires the current lease's `egress_ip`; read it from
  `GET /api/v1/platforms/{id}/leases/{account}` or `GET /api/v1/nodes/{hash}`. If both are
  unavailable, rotation degrades to a plain lease delete and the response says so.

### Health canary

- Per node, every 60 s with ±10 s jitter: SOCKS5 CONNECT through the gate to
  `AJS_CANARY_TARGET` (`www.cloudflare.com:443`), complete a TLS handshake, record latency.
  Two consecutive failures → node `DOWN` → remove from subscription → restart process.
- The canary consumes vendor quota because it is a real proxied flow. It is excluded from user
  usage but included in account usage; the console shows both.
- Egress IP is **not** measured by the canary (that would need an echo service we do not
  control). It comes from Resin's node metadata after a successful probe. `[INFERENCE]` that
  Resin's probe and our canary agree; a mismatch sets `ajs_canary_total{result="ip_mismatch"}`.
- Canary is also the only end-to-end check that covers P4 relay. A canary that completes a TLS
  handshake proves request, reply, and both relay directions.

### Reconciliation loop

Every 10 s (and after any state change): desired nodes (accounts `READY` × servers `ok` ×
operator allow-list) vs actual (processes, gates, published lines). Actions are idempotent:
start missing, stop extra, drain `SERVER_GONE`, republish if the content hash differs. A
reconcile that would publish empty content is refused unless `AJS_ALLOW_EMPTY_PUBLISH=1`.

## Cross-platform

| Concern | Linux (primary) | macOS | Windows |
|---|---|---|---|
| Service manager | systemd unit, `Restart=on-failure`, `LimitNOFILE=65535`, `KillMode=mixed` | launchd `KeepAlive`, `ProcessType=Background` | Windows service via `svc.Run`, or NSSM as the fallback |
| Child tree kill | process group (`Setpgid`) + `SIGTERM`→`SIGKILL` | same | Job Object (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`) via `golang.org/x/sys/windows`; fallback `taskkill /T /F` |
| Loopback bind | `127.0.0.1` | `127.0.0.1` | `127.0.0.1` |
| File perms | `0600`/`0700` via `os.Chmod` | same | ACL inheritance; state dir under `%ProgramData%` with restricted ACL, config dir per-run in `%TEMP%` |
| Root needed | no (proxy mode) | no | no |
| Socket options | `SO_REUSEADDR` (default in Go) | same | avoid `SO_EXCLUSIVEADDRUSE` conflicts by never double-binding a slot |
| Static build | `CGO_ENABLED=0` | `CGO_ENABLED=0`, `-tags netgo` | `CGO_ENABLED=0` |

- **Linux is the reference.** systemd supervises `ajs-accountd` only; `ajs-accountd` supervises
  every `ajsproxy` and gate. Two supervisors must not both own the children — restart storms
  follow.
- **No kernel driver.** Proxy mode is userland SOCKS5 (`research/60-dynamic/REPORT.md` §Client
  behavior: `protocol proxy`, no root, no TUN). TUN/TAP, lwip, DNS proxy and route installation
  are declared behind `Tunnel` and unimplemented; enabling them needs a capture and a driver
  decision (Wintun / utun / `TUNSETIFF`), not a config flag.
- **Loopback enforcement is checked, not assumed.** After `net.Listen`, assert
  `ln.Addr().(*net.TCPAddr).IP.IsLoopback()`; refuse to start otherwise unless
  `AJS_ALLOW_PUBLIC_LISTEN=1`. The same check runs for the gate, the admin API, and the gate
  control port.
- **Port slots are persistent.** A node's three ports come from one row (`internal_port`,
  `gate_port`, `ctl_port`) derived from a single `slot` integer (`docs/PANEL.md` §2). Allocation
  probes the port by listening before persisting; a slot that fails to bind is `CONFLICT`, never
  silently reallocated, because Resin's node hash and health history key off the line content.
- **Windows has no `syscall.Kill(pid, 0)` liveness check.** Use `os.FindProcess` + a zero-byte
  `Signal(syscall.Signal(0))` only on POSIX; on Windows rely on `cmd.Wait` from a dedicated
  goroutine and a Job Object handle.
- **Endianness and time units are type-level, not conventions.** Wire integers are big-endian
  (`encoding/binary.BigEndian`); `t` is unix **seconds**, `X-Timestamp` is **milliseconds**,
  `_tz` is **minutes**, `ka`/`kat` are unknown units. The adapter defines `UnixSeconds`,
  `UnixMillis`, and `Minutes` as distinct types so a mixed assignment does not compile.
- **Path handling.** `ajsproxy` config paths are passed as absolute paths; Windows paths with
  spaces are the default case under `%ProgramData%`, so no shell interpolation anywhere — always
  `exec.Command(bin, args...)`.
- **Clock.** A wrong clock breaks `X-Timestamp` signing and `t` in the auth body. On startup,
  warn if the system clock differs from the control API's `Date` header by more than 300 s
  (`[INFERENCE]`: no evidence of the server's tolerance; 300 s is the conservative default and
  is configurable).

## Observability

### Metrics (Prometheus, `ajs_` prefix, 20 series)

| Metric | Type | Labels | Why |
|---|---|---|---|
| `ajs_account_state` | gauge | `account` | one-hot per state enum |
| `ajs_account_login_total` | counter | `account`, `result` | auth outcome mix |
| `ajs_account_login_duration_seconds` | histogram | `account` | control-plane latency |
| `ajs_session_open_total` | counter | `account`, `node`, `result` | FV auth outcome |
| `ajs_session_age_seconds` | gauge | `node` | sid lifetime evidence |
| `ajs_flow_active` | gauge | `node` | concurrency headroom |
| `ajs_flow_total` | counter | `node`, `result` | success/reject mix |
| `ajs_flow_duration_seconds` | histogram | `node` | relay health |
| `ajs_flow_bytes_total` | counter | `node`, `direction` | quota and billing |
| `ajs_gate_reject_total` | counter | `node`, `reason` | admission pressure |
| `ajs_gate_admission_wait_seconds` | histogram | `account` | semaphore saturation |
| `ajs_semaphore_in_use` | gauge | `account` | AIMD input |
| `ajs_semaphore_limit` | gauge | `account` | AIMD output |
| `ajs_canary_total` | counter | `node`, `result` | node liveness |
| `ajs_canary_latency_seconds` | histogram | `node` | egress quality |
| `ajs_node_state` | gauge | `node` | one-hot per state enum |
| `ajs_publish_total` | counter | `result` | Resin integration health |
| `ajs_publish_age_seconds` | gauge | — | staleness alarm |
| `ajs_resin_up` | gauge | — | control plane reachability |
| `ajs_error_total` | counter | `class` | taxonomy volume |

Cardinality rule: labels are bounded (`account`, `node`, `class`, `reason`, `result`). Never
label by target host, flow id, or user-supplied string — that is an unbounded label and a memory
leak with a metrics face.

### Logs

One JSON object per line, `slog` with a fixed handler:

```json
{"ts":"2026-10-23T10:00:00.123Z","level":"info","component":"gate","event":"flow.open",
 "node":"acc1:12","flow":"9f2c1a7b3d4e5f60","account":"acc1","target_hash":"a1b2c3d4",
 "duration_ms":212,"err_class":"","bytes_in":0,"bytes_out":0}
```

- `target_hash` is the first 8 hex of SHA-256 of `host:port`: enough to group, not enough to leak
  browsing destinations into logs.
- Redaction list (enforced by a unit test that greps a rendered log line for each):
  `password`, `LoginToken`, `ConnectPassword`, `sessionKey`, `k=`, `_client_login`, full `sid`,
  full `cuid`/`_uid`. `sid` logs as 6 chars + `…`.
- Server-controlled text (`r`, `ft`, `fm`, `AuthMessage`) is truncated to 200 bytes and stripped
  of control characters before logging. This is a log-injection guard and it is also the fix for
  `emulator/internal/fv/auth.go:100-115`, which concatenates `ft`/`fm` raw into an error string.
- No vendor request carries an extra header for tracing. Correlation with Resin's request logs is
  by `(node_hash, second bucket)`; Resin's own request id is `[UNKNOWN]` and inventing a header
  would change the wire fingerprint of the client for no diagnostic gain.

### Audit events (13 actions, append-only `audit` table)

`account.create`, `account.update`, `account.delete`, `account.login`, `account.logout`,
`node.drain`, `node.undrain`, `node.probe`, `publish.apply`, `user.rotate`, `user.suspend`,
`apikey.issue`, `apikey.revoke`.

Fields: `ts`, `actor`, `action`, `subject`, `detail` (JSON, no secrets), `request_id`. Audit rows
are never updated or deleted; a retention job exports and truncates only with an explicit flag.

### Alerts

| Alert | Condition | Meaning |
|---|---|---|
| `AjsAccountDown` | `ajs_account_state{state="AUTH_FAILED"} == 1` for 5 m | credential or membership problem |
| `AjsNoLiveNodes` | `sum(ajs_node_state{state="LIVE"}) == 0` for 2 m | service outage |
| `AjsPublishStale` | `ajs_publish_age_seconds > 120` | Resin integration broken |
| `AjsFlowRejectSpike` | `rate(ajs_flow_total{result="reject"}[5m]) > 0.2 * rate(ajs_flow_total[5m])` | concurrency or sid expiry |
| `AjsCanaryFailing` | `rate(ajs_canary_total{result="fail"}[10m]) > 0.5` | egress path broken |
| `AjsParseFailures` | `increase(ajs_error_total{class="CLIENT_PARSE_FAILURE"}[15m]) > 0` | vendor schema drift |

## Failure taxonomy

Fifteen classes, one action verb each (`RETRY`, `BACKOFF`, `CIRCUIT`, `DRAIN`, `DEGRADE`,
`TERMINAL`, `MAP`, `REJECT`), one local SOCKS5 reply, one metric.

| Class | Trigger | Scope | Action | Local reply | Metric |
|---|---|---|---|---|---|
| `AUTH_REJECTED` | `AuthResult != "OK"` [CONFIRMED field] | account | `TERMINAL` after 3 attempts / 15 min | `0x02` | `ajs_account_login_total{result="auth_rejected"}` |
| `MEMBERSHIP_EXPIRED` | `Expired=true`, or `ExpireTime` in the past | account | `TERMINAL` + `DRAIN` all nodes | `0x02` | `ajs_account_state{state="MEMBERSHIP_EXPIRED"}` |
| `CREDENTIAL_UNUSABLE` | `ConnectPassword` empty after sync; operator rotated the password | account | `TERMINAL` until PATCH | `0x02` | `ajs_account_login_total{result="credential_unusable"}` |
| `SERVER_GONE` | server absent from `Servers`, or `sts != "ok"` | node | `DRAIN` + reselect | `0x04` | `ajs_node_state{state="DRAINING"}` |
| `FV_AUTH_REJECT` | auth map `r != "ok"` | account | `RETRY` once after `LoginSync`, then `CIRCUIT` 5 min | `0x01` | `ajs_session_open_total{result="reject"}` |
| `OAEP_MISMATCH` | peer closes or rejects after `k=` sent, while control login is valid | profile | `CIRCUIT` the profile, switch digest | `0x01` | `ajs_session_open_total{result="oaep"}` |
| `CONCURRENCY_REJECT` | `r` carries a limit token (`auth.limit`) | account | `BACKOFF` + halve the limit | `0x02` | `ajs_gate_reject_total{reason="concurrency"}` |
| `DATA_CHANNEL_REJECT` | data header text lacks `code=200` | flow | `RETRY` once on the same `sid`, then `DRAIN` | `0x01` | `ajs_flow_total{result="reject"}` |
| `SOCKS_REMOTE_FAILURE` | reply byte 1 in `0x01`..`0x08` | flow | `MAP` to the local code, no retry | `0x01`-`0x08` | `ajs_flow_total{result="remote_fail"}` |
| `TRANSPORT_DOWN` | dial timeout, EOF, RST, TLS failure | node | `BACKOFF` 1 s→30 s, `CIRCUIT` after 5 | `0x03` | `ajs_error_total{class="TRANSPORT_DOWN"}` |
| `CLIENT_PARSE_FAILURE` | invalid JSON, missing `UrlWeb`/`LatestVersion`, `SyncVersion` not an object | global | `REJECT` + 60 s cool-down + alert | `0x01` | `ajs_error_total{class="CLIENT_PARSE_FAILURE"}` |
| `RESIN_PROBE_FAILURE` | 2 consecutive canary failures, or egress mismatch | node | `DRAIN` | `0x04` | `ajs_canary_total{result="fail"}` |
| `RESIN_UNAVAILABLE` | admin API 5xx or timeout | global | `DEGRADE`: keep sessions, stop publishing | pass-through | `ajs_resin_up` |
| `GATE_OVERLOAD` | admission wait > 5 s | flow | `REJECT` immediately | `0x02` | `ajs_gate_reject_total{reason="overload"}` |
| `SESSION_EXPIRED` | ≥3 `DATA_CHANNEL_REJECT` on one node within 60 s while the process is alive | node | `RETRY` by restarting the process with a fresh login | `0x01` | `ajs_session_age_seconds` |

### Decision rules

1. **Retry only what is idempotent and has not moved payload**: login, `LoginSync`, FV auth,
   canary, and a flow open before `code=200`. A flow that has relayed one byte is never retried;
   the client sees the close and decides.
2. **Circuit scope follows the cause, not the symptom.** Credential and membership failures
   circuit the account; socket and canary failures circuit the node. A node circuit never marks
   credentials bad, and an account circuit never removes a node.
3. **`CONCURRENCY_REJECT` is not an auth failure.** It must never increment the login-attempt
   counter, or a busy account locks itself out.
4. **Drain is the only removal path.** Nothing deletes a subscription line or a lease except the
   drain/rotate flows, so a crash cannot leave Resin pointing at a dead port.
5. **`CLIENT_PARSE_FAILURE` is a stop sign.** It means the peer is not the peer we modelled
   (captive portal, CDN error page, schema change). Retrying faster does not help; alerting does.
6. **Unknown `r` tokens are `FV_AUTH_REJECT` with the token preserved.** Never map an unknown
   token to a retryable class.

### Budgets

| Knob | Default | Rationale |
|---|---|---|
| Login attempts | 3 / 15 min | `docs/PANEL.md` §8 |
| Login backoff | 30 s → 30 min, full jitter | `AJS_LOGIN_BACKOFF` |
| FV auth circuit | 5 min | one bad session should not block an account for long |
| Node transport circuit | 5 consecutive failures | below that, a single RST is noise |
| Flow-open retry | 1 | second failure is a session problem |
| Canary failures to `DOWN` | 2 | one packet loss is not an outage |
| Drain grace | 30 s | `docs/PANEL.md` §8 |
| Publish coalescing | 5 s | `docs/PANEL.md` §4 |
| Rotation rate | 1 / 10 s / user | `docs/PANEL.md` §8 |

## Field source table

Every protocol field the SDK touches, with its evidence class. `Where` names the surface:
`ctl` = control API, `fv-req` = request frame/text, `fv-auth` = auth body, `fv-resp` = auth
response map, `dc` = data channel, `opv2` = VPN frame, `tok` = error token.

| Field | Where | Type | Meaning | Source |
|---|---|---|---|---|
| `_site` | ctl | string | product/site id, `ajs` | `[CONFIRMED]` |
| `_v` | ctl | string | client version claim (`4.2.3.0`, `4.9.6.0`, `4.11.10.0`) | `[CONFIRMED]` |
| `_m` | ctl | string | platform (`android`, `linux`, `windows`) | `[CONFIRMED]` |
| `_os_ver` | ctl | string | OS version string | `[CONFIRMED]` |
| `_os_dev` | ctl | string | device model | `[CONFIRMED]` |
| `_os_hw` | ctl | string | SoC / hardware | `[CONFIRMED]` |
| `_os_did` | ctl | string(16 hex) | device id | `[CONFIRMED]` |
| `_os_lang` | ctl | string | OS locale | `[CONFIRMED]` |
| `_tz` | ctl | int | UTC offset, **minutes** | `[INFERENCE]` |
| `_os_dim` | ctl | string | `w=..&h=..&d=..`, `&` encoded as `%26` | `[CONFIRMED]` |
| `_inst_mkt` | ctl | string | install market | `[CONFIRMED]` |
| `_lang` | ctl | string | app language | `[CONFIRMED]` |
| `_vrf` | ctl | string | verification/channel; empty in every capture | `[UNKNOWN]` |
| `_uid` | ctl | string(22) | device uid: `%08x%02x%08x` + 4 hex of `dualHash` | `[CONFIRMED]` |
| `cmd` | ctl | string | endpoint selector, e.g. `ClientApiLogin/Login` | `[CONFIRMED]` |
| `username` | ctl | string | account name (email or phone) | `[CONFIRMED]` |
| `password` | ctl | string | plaintext password | `[CONFIRMED]` |
| `trigger` | ctl | string | `login` | `[CONFIRMED]` |
| `_client_login` | ctl | string | full `LoginToken` for authenticated calls | `[CONFIRMED]` |
| `X-Timestamp` | ctl | header | epoch **milliseconds** | `[CONFIRMED]` |
| `X-Sig` | ctl | header(16 hex) | `dualHash(secret\|body\|ts\|secret)`; absent on Linux 4.2.3.0 | `[CONFIRMED]` |
| signing secret | ctl | 20 bytes | OpenAJS constant; shipping value unknown | `[UNKNOWN]` |
| `06ka4z0dc6ga02c` | ctl | literal | 15-char constant beside the signing headers | `[UNKNOWN]` |
| `AuthResult` | ctl | string | `OK` = success (case-sensitive) | `[CONFIRMED]` |
| `AuthMessage` | ctl | string | human-readable failure text | `[CONFIRMED]` |
| `LoginToken` | ctl | string | sync token; `\|`-separated | `[CONFIRMED]` |
| `UserName` | ctl | string | FV `u` credential | `[CONFIRMED]` |
| `ConnectPassword` | ctl | string | FV `p` credential | `[CONFIRMED]` |
| `Email` | ctl | string | profile | `[CONFIRMED]` |
| `PhoneNumber` | ctl | string | profile | `[CONFIRMED]` |
| `MembershipStatus` | ctl | string | membership state | `[CONFIRMED]` |
| `GroupTitle` | ctl | string | membership label | `[CONFIRMED]` |
| `ExpireTime` | ctl | long | expiry; **units unknown** | `[UNKNOWN]` |
| `Expired` | ctl | bool | expiry flag | `[CONFIRMED]` |
| `UseCount` | ctl | int | usage counter | `[CONFIRMED]` |
| `UrlWeb` | ctl | string | required by Linux 4.2.3.0 or it aborts | `[CONFIRMED]` |
| `LatestVersion` | ctl | string | required by Linux 4.2.3.0 | `[CONFIRMED]` |
| `SyncVersion` | ctl | object | must be `{}`; a number aborts the client | `[CONFIRMED]` |
| `Servers` | ctl | map | server list keyed by id | `[CONFIRMED]` |
| `id` | ctl | string | server id | `[CONFIRMED]` |
| `svr` | ctl | string | FV endpoint host | `[CONFIRMED]` |
| `par.fv.p` | ctl | int | FV endpoint port | `[CONFIRMED]` |
| `par.fv.u` | ctl | int | unused by OpenAJS | `[UNKNOWN]` |
| `par.fv.ps` | ctl | string | unused by OpenAJS | `[UNKNOWN]` |
| `sts` | ctl | string | `ok` = usable; the only liveness test | `[CONFIRMED]` |
| `tit` | ctl | string | server title; source of the region tag | `[CONFIRMED]` |
| `ctry` | ctl | string | ISO country | `[CONFIRMED]` |
| `cat` / `ord` / `cmt` / `vsid` / `loc` | ctl | mixed | catalogue metadata | `[INFERENCE]` |
| `K` | fv-req | 16 bytes | cleartext per-connection AES-CTR key (prefix) | `[CONFIRMED]` |
| `text_len` | fv-req | u32 BE | UTF-8 text length; body follows outside the K stream | `[CONFIRMED]` |
| `s` | fv-req | string | session id; empty on auth, `<sid>` on data | `[CONFIRMED]` |
| `n` | fv-req | 32 hex | body IV for `AES-CTR(sessionKey, n)` | `[CONFIRMED]` |
| `k` | fv-req | base64(176) | `"OAEP"` + RSA-OAEP-SHA1(sessionKey), 128-byte ciphertext | `[CONFIRMED]` |
| `l` | fv-req | decimal | body byte length; auth only, absent on data | `[CONFIRMED]` |
| `_` | fv-req | 0..199 a-z | random padding, no trailing newline | `[CONFIRMED]` |
| `sessionKey` | fv-req | 16 bytes | AES-128 key, reused by every data channel | `[CONFIRMED]` |
| RSA modulus | fv-req | 1024-bit | vendor public key, identical in Win/Linux builds | `[CONFIRMED]` |
| OAEP digest | fv-req | SHA-1 | SHA-256 fails on the Linux mock | `[CONFIRMED]` |
| `t` | fv-auth | unix seconds | client time | `[CONFIRMED]` |
| `c` | fv-auth | string | command, `au` on auth | `[CONFIRMED]` |
| `u` | fv-auth | string | `UserName` | `[CONFIRMED]` |
| `p` | fv-auth | string | `ConnectPassword` | `[CONFIRMED]` |
| `cuid` | fv-auth | string | device id; 16 hex (OpenAJS) or 20 hex (Linux) | `[CONFIRMED]` |
| `cseid` | fv-auth | string | channel/device id; equals `cuid` in OpenAJS | `[CONFIRMED]` |
| `st` | fv-auth | string | site, `ajs` | `[CONFIRMED]` |
| `v` | fv-auth | string | client version | `[CONFIRMED]` |
| `cv` | fv-auth | string | core build stamp; acceptance rules unknown | `[CONFIRMED]` |
| `osp` | fv-auth | string | platform | `[CONFIRMED]` |
| `osd` | fv-auth | string | device arch/model | `[CONFIRMED]` |
| `osv` | fv-auth | string | OS version string | `[CONFIRMED]` |
| `dim` | fv-auth | string | device dimension string | `[CONFIRMED]` |
| `peip` | fv-auth | string | peer/egress IP; empty in the capture | `[INFERENCE]` |
| `ce` | fv-auth | string | client extra; empty | `[UNKNOWN]` |
| `code=200` | fv-resp | text token | success gate for auth and data responses | `[CONFIRMED]` |
| `r` | fv-resp | string | `ok` = success; any other value = reject | `[CONFIRMED]` |
| `sid` | fv-resp | string | session id used by every data channel | `[CONFIRMED]` |
| `c=aur` | fv-resp | string | auth-response command echo | `[CONFIRMED]` |
| `ok` | fv-resp | int | success flag, `1` in the capture | `[INFERENCE]` |
| `sip` | fv-resp | string | server virtual IP | `[INFERENCE]` |
| `cip` | fv-resp | string | client virtual IP | `[INFERENCE]` |
| `dns` | fv-resp | string | pushed resolver | `[INFERENCE]` |
| `ka` / `kat` / `kapf` | fv-resp | int/flag | keepalive interval / timeout / flag | `[INFERENCE]` |
| `mtu` | fv-resp | int | tunnel MTU | `[INFERENCE]` |
| `ft` / `fm` | fv-resp | string | failure type / failure message | `[INFERENCE]` |
| `au` | fv-resp | string | auth user echo | `[INFERENCE]` |
| `div` | dc | 32 hex | per-flow payload IV | `[CONFIRMED]` |
| SOCKS5 CONNECT | dc | bytes | `05 01 00 03 <len> <host> <port BE>` | `[CONFIRMED]` |
| 10-byte reply | dc | bytes | `CTR(sessionKey,div)`, first two bytes `05 00` | `[CONFIRMED]` |
| relay streams | dc | bytes | independent CTR streams per direction, continuing after request/reply | `[CONFIRMED]` |
| `keyA^keyB` / `keyB` | opv2 | 16+16 bytes | frame key material, sent in the clear | `[CONFIRMED]` |
| `crc32` | opv2 | u32 LE | zlib CRC over `keyA^keyB` | `[CONFIRMED]` |
| `OPV2` | opv2 | magic | frame marker | `[CONFIRMED]` |
| tag `01` / `02` / `00` | opv2 | byte | payload / body / terminator | `[CONFIRMED]` |
| tag semantics | opv2 | — | what the tags mean to the peer | `[UNKNOWN]` |
| `pv` | opv2 | map field | `protocolVersion` echo in `emitObscurePacket` | `[CONFIRMED]` |
| `con` `ping` `pong` `off` `sid` `aur` `fm` `ft` `au` | opv2 | verbs | VPN control commands; framing and numeric codes unknown | `[UNKNOWN]` |
| `auth.password` | tok | string | candidate `r` value: bad password | `[INFERENCE]` |
| `auth.group` | tok | string | candidate `r` value: group/entitlement | `[INFERENCE]` |
| `auth.forbidden` | tok | string | candidate `r` value: forbidden | `[INFERENCE]` |
| `auth.membership` | tok | string | candidate `r` value: membership | `[INFERENCE]` |
| `auth.limit` | tok | string | candidate `r` value: concurrency/quota limit | `[INFERENCE]` |
| `auth.off` | tok | string | candidate `r` value: service off | `[INFERENCE]` |
| `session.timeout` | tok | string | candidate `r` value: session expired | `[INFERENCE]` |

This table is the machine-checked inventory: `validate.py` requires at least 40 rows, requires
every row to carry exactly one tag, and fails on an untagged row. Sources:
`research/40-protocol/PROTOCOL.md` §Common device parameters, §Login, §Endpoint catalogue,
§Response fields, §OPV2 framing; `research/60-dynamic/REPORT.md` §Decryption, §Control-plane
capture; `research/50-windows-protocol/REPORT.md` §OPV2 framing; `emulator/internal/fv/*`.

## Open questions

Each question names what would settle it and what the design does meanwhile. None of them blocks
P0/P1; all of them are falsifiable with one live account, which the Owner has not authorised.

1. **Vendor concurrency ceiling per account.** `auth.limit` suggests one exists; the value is
   `[UNKNOWN]`. Settle by ramping flows on one account until `code=200` stops. Meanwhile: default
   32, AIMD down on rejects, never above the operator ceiling.
2. **`sid` lifetime and fan-out.** Does one `sid` accept unlimited concurrent data channels, and
   when does it expire? Settle by holding a session for hours and opening flows periodically.
   Meanwhile: `ajs_session_age_seconds` + restart on a reject spike.
3. **Keepalive on the proxy path.** `ka`/`kat`/`kapf` semantics and whether `ping`/`pong` is
   required to keep a `sid` alive. Settle by idling past `ka` and opening a flow. Meanwhile:
   passive liveness only; no invented frames.
4. **`ExpireTime` units.** Seconds, milliseconds, or a formatted date. Settle with one live
   login whose expiry is known. Meanwhile: `Expired` is authoritative; a numeric value > 10^12 is
   read as ms, > 10^9 as seconds, otherwise logged as unparseable — never silently guessed.
5. **Signing.** Is `X-Sig` required by the current Windows build, and what is the secret? Settle
   by capturing a Windows login. Meanwhile: `SigningMode` per profile, Linux profile unsigned.
6. **`cv` acceptance.** Does the server validate the core stamp against `_v`/platform? Settle by
   sending a mismatched `cv` once. Meanwhile: the profile carries the stamp that matches the
   version string we claim.
7. **OPV2's real role.** Layout is confirmed, call site is VPN-only. Settle by capturing a
   VPN-mode session. Meanwhile: codec registered, no production path selects it.
8. **UDP / lwip / TUN framing.** No wire evidence at all. Meanwhile: excluded; the gate answers
   `0x07` to `UDP ASSOCIATE`.
9. **Response integrity.** No signature, MAC, or replay protection observed. Meanwhile: TLS is the
   only integrity layer we assume, and we do not implement a verification we cannot test.
10. **Resin request-id for log correlation.** Whether Resin exposes a per-request identifier we
    can join on. Meanwhile: join by `node_hash` + second bucket; no invented header on the vendor
    wire.
11. **`par.fv.u` / `par.fv.ps`.** Returned, unused by OpenAJS. Meanwhile: ignored; a later wave may
    find they gate UDP or a newer core.
12. **One endpoint, many accounts.** Two accounts reaching the same `svr` share an egress IP —
    that is the premise of Resin's IP-anchored lease. Not directly observed. Meanwhile: the
    lease model tolerates it either way (same-IP rotation degrades to a plain re-pin).

## Self-check

### A — early artifact

The protocol surface table (8 rows, three columns of classification) and the account/session
state machine were written to disk before any interface detail. The field-source table was the
third artifact, also before `INTERFACE.md`. Interface work only started once the surface and the
states were fixed, so the interfaces are derived from the protocol rather than the reverse.

### B — falsification of the strongest claims

| Claim | Attempt to refute | Verdict | Basis |
|---|---|---|---|
| Carrier body is outside the K stream | Recompute both captures; check whether a K-encrypted body could decrypt under `sessionKey` | `CONFIRMED` | 679 = 16+4+234+425 and 207 = 16+4+169+18; the body decrypts under `AES-CTR(sessionKey,n)` (`research/60-dynamic/captures/fvmock-1.log`, `-2.log`); `emulator/README.md` §Limits says the same. The `PROTOCOL.md` Shape A bracket is `WEAKENED` |
| The gate is required | Route Resin directly at `ajsproxy` and see what breaks | `WEAKENED` | Routing still works; what breaks is admission, byte accounting, drain, and stable identity. Restated: the gate is required *for the specified guarantees*, optional for plain routing |
| Drain must be gate-first | Follow `docs/PANEL.md` §8 (line removed last) and look for a failure window | `CONFIRMED` for gate-first; `PANEL.md` §8 ordering `WEAKENED` | With the line last, Resin keeps scheduling into a gate that already refuses → user-visible `0x02` for up to one refresh cycle |
| Proxy mode needs no kernel driver | Look for a root/TUN requirement in the captured run | `CONFIRMED` for proxy mode; `WEAKENED` for "all modes" | `research/60-dynamic/REPORT.md` §Client behavior: no root, `protocol proxy`; but `FVClientProxyLwip`/`FVDnsProxy`/`vpn://` exist in the binaries, so the claim is scoped to P1/P2/P4 |
| Only handshakes retry; flows never resume | Try to resume a flow after partial relay | `CONFIRMED` | The data channel has no sequence number or framing after the SOCKS reply (`PROTOCOL.md` §Relay): a resumed stream would desynchronise the CTR position. Retry is legal only before `code=200` |
| Concurrency is the scarce resource | Check whether volume/quota could bind first | `WEAKENED` | No evidence either way; both may bind. Design meters bytes as well and treats the semaphore as one of two limits, not the only one |

### C — verification

```
$ python D:/Project/LovePlusSpeedToProxy/research/76-sdk-integration/validate.py
checked: fields=103 failures=15 endpoints=27 interfaces=13
OK protocols=8 interfaces=13
exit=0
```

Run once, 2026-10-23, from `D:/`. The gate checks: 8 protocol rows with valid
status/evidence/role cells, 103 field rows each carrying one source tag, 15 failure classes each
with an action verb, 13 Go interfaces each with at least one method, 27 HTTP endpoints across
`ajs-accountd` and the gate, protocol ids consistent across both documents, all three state
machines named in both, 20 metric names and the four redaction terms present, and no credential
literal in either file. Acceptance is another session's job; this is only the artifact gate.

### D — summary

- Established: P1/P2/P4 as the production path; body-outside-K carrier form; per-flow cost model
  (one TCP, no RSA); gate as the admission/accounting/drain point; 15-class taxonomy with fixed
  verbs; 20 metrics and a redaction list.
- Overturned/weakened: the `PROTOCOL.md` Shape A bracket; "gate is required" (scoped);
  `PANEL.md` §8 drain order; "no kernel driver" (scoped to proxy mode); "concurrency is the only
  scarce resource".
- Unknown: keepalive, `sid` lifetime, concurrency ceiling, `ExpireTime` units, signing secret,
  `cv` acceptance, OPV2 role, UDP/TUN framing, response integrity, Resin request id.
- Next: sanitize `AuthError` before logging; gate control API; `VersionProfile` persistence;
  then a live-account experiment plan the Owner must authorise.
- My remaining bet: that the vendor tolerates a long-lived `sid` with many concurrent data
  channels, i.e. that per-flow reconnect is cheap. If `sid` is short-lived, the supervisor restart
  loop becomes the hot path and the concurrency default of 32 is wrong in the other direction.

