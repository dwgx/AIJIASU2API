# AJS-52 — Protocol gap review: research corpus vs emulator/daemon

Status: read-only review, 2026-10-23. No code edits, no live vendor calls, no credentials.
Scope: `emulator/internal/{fv,api,proxy}`, `daemon/internal/account/{control,session,login_service}`,
`daemon/internal/gate`, the six research documents named in the brief, `docs/PANEL.md`, and the
AJS-07 captures. The report is the only artifact written by this slice.

## 1. Bottom line

- **The crypto/framing core is the strongest part.** The binary-carrier request bytes replay the
  AJS-07 captures exactly (`fvmock-1.bin`, `fvmock-2.bin`), the 1024-bit RSA/OAEP-SHA1 and
  AES-128-CTR formats are pinned by arithmetic and tests, and the SOCKS5 front end and gate pass
  their suites. This is not where the first live smoke will fail.
- **Identity and wiring are the weak part.** The daemon's "Linux 4.2.3.0" profile sends `_v`
  where the only Linux capture sends `v`, a 22-char `_uid` where the capture has 20 hex, a
  16-hex `cuid`/`cseid` where the capture has a 20-hex `cuid` and `cseid=linux`, Android
  `osp/osd/osv/dim`, and an okhttp `User-Agent` where the capture has none. A test locks the
  mismatch in (`daemon/internal/account/control_test.go:134-141`).
- **The daemon cannot open a live FV session.** `main.go` wires `LoginService` only; `NewFactory`,
  `Supervisor` and `gate.NewServer` have no callers outside tests. The only runnable live path is
  the `ajsproxy` CLI. The daemon's node/gate/session code is a tested library, not a live service.
- **Two "confirmed" response shapes are mock-only.** The two-layer auth response + half-close and
  the 10-byte data-channel reply + relay were sent by the researcher's mock, not by a vendor
  server. The client accepted them; the server's behavior is `unknown`. The corpus labels them
  `[CONFIRMED]` without that scope qualifier.
- **Matrix: 26 rows — 3 `proven`, 10 `implemented+tested`, 1 `implemented+untested`, 4 `unknown`,
  8 `contradicted`.**

## 2. Status legend

| Status | Meaning |
|---|---|
| `proven` | The wire behavior is directly evidenced by a capture or binary artifact, and the implementation reproduces it; a regression test replays the artifact. |
| `implemented+tested` | Code plus automated tests; no vendor capture of the exact behavior. |
| `implemented+untested` | Code exists, but no test exercises the end-to-end behavior. |
| `unknown` | No vendor evidence and no implementation, or the only evidence is a mock written from the same hypothesis. |
| `contradicted` | The implementation (or a repo document) conflicts with the cited claim or with another part of the repo. |

`[UNVERIFIED]` marks anything this slice did not directly execute or read. No live host was
contacted; every live statement below is a prediction or an experiment design.

## 3. Matrix

| # | Capability | Claim source (file:line) | Implementation (file:line) | Status | Evidence |
|---|---|---|---|---|---|
| 1 | Control login: `cmd=ClientApiLogin/Login`, `username`, `password`, `trigger=login` | `research/40-protocol/PROTOCOL.md:281-315`; `research/60-dynamic/REPORT.md:51-78` | `emulator/internal/api/client.go:220-246`; `daemon/internal/account/control.go:104-116` | implemented+tested | `TestLoginSigned` / `TestLoginUnsigned` pass; fake server asserts `cmd`, `username`, `password`, `trigger` (`daemon/internal/account/control_test.go:104-175`). No live call. |
| 2 | Linux-profile control request shape (`v`, `_uid` length, field set, UA, trailing `&`) | `research/60-dynamic/captures/control.jsonl:2`; `research/60-dynamic/REPORT.md:51-78` | `emulator/internal/api/device.go:100-118`; `emulator/internal/api/client.go:153-173`; `daemon/internal/account/control_test.go:134-141` | contradicted | Capture sends `v=4.2.3.0`, a 20-hex `_uid`, no `_os_hw/_tz/_inst_mkt/_vrf`, no `User-Agent`, and a trailing `&`. Code sends `_v`, a 22-char `_uid`, those fields, `okhttp/4.9.3`, and no trailing `&`. The test asserts `_v` and a 22-char `_uid`. |
| 3 | Signing: `dualHash`, `buildQuery`, `X-Sig = hex(dualHash(secret\|body\|ts\|secret))` | `research/40-protocol/PROTOCOL.md:420-528`; `research/90-verification/VERDICTS.md:13` | `emulator/internal/api/sign.go:38-120`; `emulator/internal/api/sign_test.go:24-95` | implemented+tested | `TestSignVectors`, `TestDualHash`, `TestBuildQueryVectors` pass; vectors match the Java harness (`PASS 7` in VERDICTS row 13). |
| 4 | Signing secret and live acceptance | `research/40-protocol/PROTOCOL.md:1176-1248` (open q. 2); `research/60-dynamic/REPORT.md:79-85` | `emulator/internal/api/sign.go:23`; `emulator/internal/api/client.go:37-41`; `daemon/internal/account/login_service.go:129` | unknown | The OpenAJS secret is not in the shipping binaries; Linux 4.2.3.0 sends no `X-Sig`. The daemon disables signing for `SigningMode=="none"`. Whether the live backend still accepts unsigned requests is untested. |
| 5 | Device identity generation: `_uid`, `_os_did` | `research/40-protocol/PROTOCOL.md:248-280` | `emulator/internal/api/device.go:55-97`; `daemon/internal/account/device.go:20-30` | implemented+tested | `TestUIDFormat` and `EnsureIDs` format checks pass. The formula is OpenAJS-shaped; the Linux capture's 20-hex `_uid` is not reproduced (row 2). |
| 6 | Device identity persistence (once per account, sealed) | `research/76-sdk-integration/REPORT.md:306-318` | `daemon/internal/account/login_service.go:165`; `daemon/internal/store/store.go:510-526` | contradicted | `LoginService.Login` calls `NewDevice` on every login; `NewDevice` generates fresh `_uid`/`_os_did`; `MarkLogin` stores no device field. The design rule "generated once, sealed, never regenerated on retry" is not implemented. |
| 7 | `loginSync` (`_client_login`, `username`, `trigger=login`) | `research/40-protocol/PROTOCOL.md:316-364` | `emulator/internal/api/client.go:248-276`; `daemon/internal/account/control.go:119-126` | implemented+tested | `TestLoginSyncAndServers` passes; asserts the refreshed `ConnectPassword` and the `Servers` map. No live call. |
| 8 | `Servers` parsing and selection (`sts=="ok"`, `svr`, `par.fv.p`) | `research/40-protocol/PROTOCOL.md:316-364`, `:554-572` | `emulator/internal/api/client.go:105-131`, `:277-279`; `emulator/cmd/ajsproxy/main.go:193-218` | implemented+tested | Server model/`Port()`/`Addr()` tested via the control tests; `pickServer` has a fallback that selects a non-`ok` server if none is `ok` (`main.go:212-218`), which the design does not sanction. |
| 9 | Host failover only on transport error | `research/40-protocol/PROTOCOL.md:182-214` | `emulator/internal/api/client.go:161-190`; `daemon/internal/account/control_test.go:307-375` | implemented+tested | `TestLoginFailoverOnTransportError`, `TestNoFailoverOnHTTPError`, `TestNoFailoverOnProtocolError` pass. |
| 10 | FV binary-carrier request: `[16B K][CTR(K,0)(4B BE len\|\|text)][body]` | `research/40-protocol/PROTOCOL.md:932-995`; `research/60-dynamic/REPORT.md:9-36` | `emulator/internal/fv/binary.go:35-119`, `:214-322` | proven | `TestBinaryCarrierCaptureVectors` decodes `fvmock-1.bin` (key `1f5914…`, `n=0e03c677…`, `l=425`) and `fvmock-2.bin` (key `d258388e…`, `s=test`, `k=`, no `l=`); PASS. |
| 11 | FV auth response: two layers + server half-close | `research/40-protocol/PROTOCOL.md:996-1041`; `research/60-dynamic/REPORT.md:9-36` (Answer 5) | `emulator/internal/fv/binary.go:141-196`, `:280-300`; `emulator/internal/fv/binary_test.go:126-307` | unknown | The only server that sent this shape is the researcher's mock (`60-dynamic/REPORT.md:37-49` says no real host was contacted). The test proves the client accepts it; the vendor server's behavior is unobserved. |
| 12 | RSA-OAEP: 1024-bit modulus, SHA-1, `base64("OAEP"\|\|ct)` | `research/40-protocol/PROTOCOL.md:573-668`; `research/90-verification/VERDICTS.md:14` | `emulator/internal/fv/crypto.go:27-39`, `:104-131`; `emulator/internal/fv/frame_test.go:123-160` | proven | Modulus is the 309-digit/1024-bit vendor value; ciphertext is 128 bytes; `TestOAEPShape` passes; AJS-07 decrypted the captured `k=` with OAEP-SHA1 and failed with SHA-256. Server acceptance remains `[UNVERIFIED]`. |
| 13 | Data-channel request: `s=<sid>`, `n=<div>`, `k=`, no `l=`, SOCKS5 CONNECT body | `research/40-protocol/PROTOCOL.md:1042-1064` | `emulator/internal/fv/binary.go:324-397`; `emulator/internal/fv/frame.go:199-220` | proven | `TestBinaryCarrierCaptureVectors` decrypts the 18-byte body to `05 01 00 03 0b "example.com" 00 50`; PASS. |
| 14 | Data-channel reply (`code=200` + 10-byte `05 00`) and relay CTR continuation | `research/40-protocol/PROTOCOL.md:747-814`; `research/60-dynamic/REPORT.md:165-176` | `emulator/internal/fv/binary.go:380-397`; `emulator/internal/fv/transport.go:114-141`; `emulator/internal/fv/binary_test.go:126-307` | unknown | Mock sends 10 bytes `05 00` and echoes `ping`; PASS. AJS-07 did not validate the mock's relay reply (`60-dynamic/REPORT.md:165-176`), so the live reply length/keystream is unobserved. |
| 15 | Local SOCKS5 front end (no-auth CONNECT, ATYP 1/3/4, RFC 1928 replies) | `research/40-protocol/PROTOCOL.md:815-832` | `emulator/internal/proxy/socks5.go:102-190`; `emulator/internal/proxy/proxy_test.go:40-107` | implemented+tested | `TestSOCKS5Handshake` passes; greeting accepts any offered method, matching OpenAJS. |
| 16 | Gate SOCKS5 admission, drain, reply bytes | `docs/PANEL.md:42-78`; `research/76-sdk-integration/REPORT.md:395-419` | `daemon/internal/gate/server.go:399-494`, `:596-690`; `daemon/internal/gate/server_test.go:185-284` | implemented+tested | `TestConnectAddressTypes`, `TestAdmissionLimit`, `TestDrainWaitsForActiveAndUndrain`, `TestOpenFlowErrorRepliesRefused` pass. |
| 17 | Gate address-type fidelity (ATYP 1/4 vs FV ATYP 3) | `research/40-protocol/PROTOCOL.md:747-764` | `daemon/internal/gate/server.go:620-680`; `emulator/internal/fv/frame.go:199-220` | contradicted | The gate accepts IPv4/IPv6 and converts to a textual host; `Socks5ConnectRequest` always emits ATYP 3 with that string. IPv6 (`2001:db8::1`) is sent as a domain name, not as 16 address bytes. The design only specifies ATYP 3. |
| 18 | Transport selection (profile `protocolVersion` → carrier) | `research/40-protocol/PROTOCOL.md:849-931`; `research/50-windows-protocol/REPORT.md:8-53` | `emulator/internal/fv/transport.go:93-100`; `emulator/internal/config/config.go:59-67`; `daemon/internal/account/session.go:44-52`; `daemon/internal/account/protocol.go:17-24` | contradicted | `Linux423Binary` declares `ProtocolVersion: 2` (binary carrier) but `Transport: "rawtcp"`; `NewProtocol` returns `RawTCP{}`; the emulator default is `rawtcp`. The binary carrier is `HTTPUpgrade`, so the profile label and the default selection point at the wrong implementation. |
| 19 | Keepalive (`ka`, `kat`, `kapf`, `ping`/`pong`) | `research/40-protocol/PROTOCOL.md:1176-1248` (open q. 10); `research/76-sdk-integration/REPORT.md:175-188` | No symbol; nearest is `daemon/internal/account/session.go:205` (`Healthy` = SID non-empty) | unknown | `grep` for Keepalive/Ping/Pong over `emulator`/`daemon` (excluding tests) returns nothing. The mock auth map has `ka=30/kat=90/kapf=1` (`fvmock-1.log:35-37`), but the implementation only stores them as fields. |
| 20 | Response parsing (`key=value\n`, `code=200`, merged auth map) | `research/40-protocol/PROTOCOL.md:669-746` | `emulator/internal/fv/frame.go:80-140`; `emulator/internal/fv/binary.go:141-196` | implemented+tested | Capture-vector test decodes the logged response text and map; `ParseFields` ignores lines without `=` or with `=` at position 0. Live variants (extra fields, no half-close) are untested. |
| 21 | Error taxonomy (15 classes, `ReplyCode`) | `research/40-protocol/PROTOCOL.md:1085-1127`; `research/76-sdk-integration/REPORT.md:591-620` | `emulator/internal/fv/auth.go:100-116`; `daemon/internal/gate/server.go:430-470` | contradicted | No `Classifier`/`Classify` symbol exists (`grep` empty). The gate maps every `OpenFlow` error to `0x05` and an admission timeout to `0x06`; vendor tokens (`auth.limit`, `session.timeout`, …) are not classified. |
| 22 | Session refresh (LoginSync before a stale session) | `research/76-sdk-integration/INTERFACE.md:172-175`; `research/76-sdk-integration/REPORT.md:244-262` | No caller; `daemon/internal/store/store.go:510-526` | contradicted | `grep -rn "\.LoginSync("` finds only `control.go:120` and the api client itself. No `time.Minute` refresh in `account`, `supervisor` or `runtime`. `store.LoginAccount` only updates `LastLoginAt`/state. |
| 23 | Proxy carrier end-to-end (auth → data → relay) | `research/40-protocol/PROTOCOL.md:932-1084` | `emulator/internal/fv/binary.go:214-397`; `emulator/internal/proxy/socks5.go:76-100`; `emulator/internal/fv/binary_test.go:126-307` | implemented+tested | In-process mock: binary auth, map + `CloseWrite`, data channel, SOCKS5 request, `05 00`, echo; PASS. Live vendor compatibility `[UNVERIFIED]`. |
| 24 | Dashboard live mode against the daemon | `docs/PANEL.md:42-91`; `dashboard/src/lib/api.ts:29` | `daemon/internal/api/server.go:133-171`; `dashboard/tests/live-mode.test.tsx:17-35` | implemented+untested | Dashboard defaults to mock (`VITE_USE_MOCK !== 'false'`); the live test stubs `fetch` and only checks the "live" label. The daemon routes exist and the model types match, but no test drives the dashboard against the daemon. |
| 25 | Daemon live wiring (login → session → gate → publish) | `docs/PANEL.md:10-28`, `:219-230`; `research/76-sdk-integration/REPORT.md:375-394` | `daemon/cmd/ajs-accountd/main.go:124-138`, `:313-333`; `daemon/internal/supervisor/supervisor.go:243` | contradicted | `main.go` wires `LoginService` only. `NewFactory` has no caller outside `session.go` and tests; `supervisor.New`/`Start` and `gate.NewServer` have no callers outside tests. `AJS_ACCOUNT_LOGIN` is off by default. |
| 26 | TLS verification on the control channel | `research/40-protocol/PROTOCOL.md:182-214` (OpenAJS trust-all); `research/40-protocol/PROTOCOL.md:1176-1248` (open q. 15) | `emulator/internal/api/client.go:29-58` | contradicted | The emulator uses a default `http.Client` (normal verification); there is no `InsecureTLS` equivalent. This is a deliberate safety divergence. It is only a live blocker if a vendor host presents a certificate Go does not trust. |

### Matrix notes

- **Row 2 is the highest-value contradiction.** The capture is the only direct evidence of what a
  shipping Linux client sends. The code's field set is OpenAJS/Android-shaped and the test asserts
  that shape, so a green test suite cannot detect the mismatch.
- **Row 11/14 are `unknown`, not `proven`.** The mock is not an independent source: it was written
  by the same researcher from the same hypothesis. The client's acceptance of the mock proves the
  client's parser, not the server's emitter.
- **Row 12's `proven` is about the format, not server acceptance.** The vendor public key is in all
  three binaries; the server must hold the private half. AJS-07 proved the client uses OAEP-SHA1
  by decrypting its own `k=` with a patched key.
- **Row 18 is a code-to-code contradiction.** `Linux423Binary.Transport == "rawtcp"` and
  `ProtocolVersion == 2` cannot both describe the same carrier: the binary carrier is implemented
  by `HTTPUpgrade`, and `RawTCP` is the OpenAJS frame.

## 4. Top 5 blockers for the first live-account smoke

### B0 (operational, before the five)

The daemon cannot be the smoke target. `main.go:124-138` wires only `LoginService`; the session
factory, supervisor and gate have no production caller (row 25). The smoke must run
`emulator/cmd/ajsproxy` (`login`, then `connect` or `serve-socks`), or a future daemon build that
wires `NewFactory` + `Supervisor` + `gate.NewServer`. If the acceptance criterion is "the daemon
serves a live flow", this is the first blocker; if it is "the protocol works live", it is a scope
note.

### B1 — Control-plane request shape for the Linux profile

**Why it blocks.** The first live packet is the login. If the backend keys on `v`/`_v`, the
20-vs-22-char `_uid`, or the extra Android fields, the smoke fails before any FV traffic.

**Experiment (local, no vendor).**
1. Start a recorder on `http://127.0.0.1:8081` that logs the raw body and returns a canned
   `{"Login":{"AuthResult":"OK",...}}`.
2. Run `go run ./cmd/ajsproxy login -config <temp>` with `"hosts":["http://127.0.0.1:8081"]`.
3. Run the daemon with `AJS_API_HOSTS=http://127.0.0.1:8081 AJS_ACCOUNT_LOGIN=1` and
   `POST /v1/accounts/{id}/actions/login`.
4. Sort the form fields and diff both bodies against `control.jsonl:2`.

**Experiment (live, Owner-approved, one attempt).** One login with the current shape; if
`AuthResult != "OK"`, one login with the captured Linux shape in a smoke branch (not this slice).

**Expected artifact.** `research/81-protocol-gaps/evidence/B1-request-diff.tsv` with columns
`field | captured-linux | ajsproxy | daemon | note`, and `B1-live-login.json` with
`http_status`, `AuthResult`, `AuthMessage`. Redact `password` and `_uid` if the Owner treats them
as identifying.

**Pass rule.** Every field either matches the capture or the live login returns `AuthResult=OK`
with the current shape. A field that differs and a non-OK result is a hard fail.

### B2 — Signing requirement (`X-Sig`/`X-Timestamp` vs unsigned)

**Why it blocks.** The only Linux capture is unsigned; the only known signing secret is OpenAJS's,
which is absent from the shipping binaries. If the current backend requires signing, the Linux
profile is rejected.

**Experiment.** Using the same device identity and body:
1. Unsigned: daemon Linux profile (`AJS_ACCOUNT_LOGIN=1`).
2. Signed: a one-off harness that imports `ajsproxy/apifacade` with `NoSign=false` and the
   OpenAJS secret, or the emulator CLI (which always signs).

Run the two attempts at least 60 s apart, at most two attempts, to avoid a lockout. Record the
status and the JSON body for each.

**Expected artifact.** `evidence/B2-signing.tsv` with
`mode | X-Sig present | http_status | AuthResult | AuthMessage`.

**Pass rule.** At least one mode returns `OK`; record which. If both fail, the secret/profile is
wrong and the next experiment is a Windows-profile capture, not more password attempts.

### B3 — FV auth response layering and half-close

**Why it blocks.** The client expects `code=200` in one K-encrypted block and the auth map in a
separate session-key-encrypted stream, terminated by a half-close. Only the researcher's mock has
produced that shape. If the live server sends one block, no map, or no FIN, the session never
reaches `r=ok`/`sid`.

**Experiment.**
1. Build a smoke-only debug `ajsproxy` that writes the generated `sessionKey` and `n` to a `0600`
   file outside the repo (the pcap alone cannot decrypt the map; the session key is RSA-wrapped to
   the vendor).
2. `tcpdump -i any -w B3-fv-auth.pcap host <fv_ip> and port <fv_port>`.
3. `ajsproxy connect -transport httpupgrade -target example.com:80`.
4. Extract the server→client stream; split at `4 + textLen`; decrypt the first block with `K`
   (cleartext in the pcap) and the remainder with the session key + `n`; record whether FIN
   arrives and the inter-segment delay.

**Expected artifact.** `evidence/B3-fv-auth.txt` with `text_len`, `K`, `n`, the decrypted
`code=200` text, the decrypted map keys (`r`, `sid`, `sip`, `cip`, `ka`, `kat`, `kapf`, …), the
FIN delta, and a PASS/FAIL line. Session key in a `0600` temp file, never in the report.

**Pass rule.** `code=200` found, `r=ok`, `sid` non-empty, and the map is complete after EOF or the
500 ms idle. A missing map or a truncated map is a protocol-shape failure, not a credential
failure.

### B4 — Data-channel reply and relay keystream

**Why it blocks.** After auth, every flow depends on a 10-byte `05 00` reply encrypted with
`sessionKey`/`div`, then a CTR stream that continues after the reply. AJS-07 never validated the
relay reply; the mock did. If the live reply is a different length or starts at a different CTR
position, every flow fails.

**Experiment.**
1. Same debug key build as B3.
2. `ajsproxy serve-socks -transport httpupgrade -listen 127.0.0.1:1080`.
3. `curl -v --socks5-hostname 127.0.0.1:1080 http://example.com/` with a tcpdump on the FV port.
4. Decrypt the data-channel text with `K`, read `div`; decrypt the 10 bytes after `code=200` with
   the session key + `div`; check `05 00`; decrypt the first relayed response bytes with the
   continuing stream.

**Expected artifact.** `evidence/B4-fv-data.txt` with the reply hex/plain, `curl` exit code,
response SHA-256, and a PASS/FAIL line.

**Pass rule.** Reply is exactly 10 bytes, starts `05 00`, and the relayed HTTP response decrypts
to a valid status line. A short reply or a non-`05 00` first byte is a hard fail.

### B5 — Device identity stability across logins

**Why it blocks.** The daemon regenerates `_uid`, `_os_did`, `cuid` and `cseid` on every login
(row 6). The design says generate once per account and never regenerate. A second login that looks
like a new device is the fastest way to turn a transient error into an anti-abuse event
(`research/76-sdk-integration/REPORT.md:306-318`, `[INFERENCE]`).

**Experiment.** Two consecutive logins with the same account and the same config (local recorder or
live), capturing both request bodies; compare `_uid`, `_os_did`, `cuid`, `cseid` (FV auth body for
the last two). Redact the password.

**Expected artifact.** `evidence/B5-identity.tsv` with
`run | _uid | _os_did | cuid | cseid | equal_to_run_1`.

**Pass rule.** All four equal across runs. The current daemon fails by construction; a passing
live login does not clear this, because the vendor may tolerate the first change and act on the
second.

## 5. Contradictions kept (not averaged)

| # | Contradiction | Side A (evidence) | Side B (evidence) | Status |
|---|---|---|---|---|
| C1 | Linux control request shape | Capture: `v=4.2.3.0`, 20-hex `_uid`, no `_os_hw/_tz/_inst_mkt/_vrf`, no UA, trailing `&` (`control.jsonl:2`) | Code/test: `_v`, 22-char `_uid`, those fields, `okhttp/4.9.3`, no trailing `&` (`device.go:100-118`, `client.go:153-173`, `control_test.go:134-141`) | Open; row 2. The capture wins as the only direct Linux evidence. |
| C2 | Linux FV auth identity | Capture: `cuid` 20 hex, `cseid=linux`, `osp=linux`, `osd=x86_64`, `osv=Linux…`, `dim=lid=…` (`fvmock-1.log:11-17`) | Code: `DeviceID` (16 hex) for both `cuid`/`cseid`, `Device.Platform`/`OSDevice`/`OSVersion`/`OSDim` defaults Android (`session.go:151-160`, `device.go:24-25`) | Open; rows 2/6. No code path sets `cseid=linux`. |
| C3 | Transport label vs carrier | `Linux423Binary.ProtocolVersion=2`, binary carrier is `HTTPUpgrade` (`session.go:44-52`, `binary.go:207-213`) | `Linux423Binary.Transport="rawtcp"`, `NewProtocol()` returns `RawTCP{}` (`session.go:51`, `protocol.go:17-24`), CLI default `rawtcp` (`config.go:62`) | Open; row 18. Code-to-code. |
| C4 | Device identity lifetime | Design: generate once, seal, never regenerate (`76 REPORT:306-318`) | Code: `NewDevice` per login (`login_service.go:165`), no device column (`store.go:510-526`) | Open; row 6. |
| C5 | Session refresh | Design: `LoginSync` before a session older than 10 min (`76 INTERFACE:172-175`, `76 REPORT:244-262`) | Code: no caller; store-only refresh (`store.go:510-526`) | Open; row 22. |
| C6 | Error taxonomy | Design: 15 failure classes and `ReplyCode` (`76 REPORT:591-620`) | Code: all `OpenFlow` errors → `0x05`, admission timeout → `0x06` (`gate/server.go:430-470`); no `Classifier` | Open; row 21. |
| C7 | Daemon live wiring | `docs/PANEL.md:10-28`, `:219-230` describe `ajs-accountd` starting one `ajsproxy` per node and a gate | `main.go` wires only `LoginService`; no `NewFactory`/`Supervisor`/`gate.NewServer` caller | Open; row 25. |
| C8 | "Confirmed" auth response | Corpus: two-layer response + half-close `[CONFIRMED]` (`PROTOCOL.md:996-1041`, `60-dynamic:9`) | Only a mock server sent it; no real host was contacted (`60-dynamic:37-49`) | Kept as `unknown`; row 11. The label is a scope overclaim. |
| C9 | "Every shipping client signs" | Comment: `client.go:37-41`, `control.go:22-24` | Linux 4.2.3.0 sends no `X-Sig`/`X-Timestamp` (`60-dynamic:79-85`) | Open; row 4. Comment is wrong; code has the unsigned path. |
| C10 | Unsigned path "not emulated" | `emulator/README.md:110` | Daemon emulates it via `SigningMode=="none"` (`login_service.go:129`, `control_test.go:174-205`) | Doc drift, not a protocol contradiction. |

## 6. Unknowns kept

- **U1.** Live auth response shape and half-close (row 11). Resolved by B3.
- **U2.** Live data-channel reply length and relay keystream (row 14). Resolved by B4.
- **U3.** Signing secret and whether the live backend requires signing (row 4). Resolved by B2.
- **U4.** `v`/`_v`/`cv` acceptance rules and version gating. Resolved by B1/B3.
- **U5.** Keepalive: whether `ka`/`kat`/`kapf` are enforced on the proxy path, and whether `ping`/
  `pong` use OPV2. No implementation (row 19).
- **U6.** `sid` lifetime, per-account concurrency, and whether one `sid` may open multiple data
  connections. The implementation opens one TCP per flow with the same `sid`; untested live.
- **U7.** `par.fv.u` / `par.fv.ps` semantics. Returned by the API, unused by OpenAJS and by this
  implementation.
- **U8.** TLS verification/pinning on the live control hosts. The emulator verifies; OpenAJS
  trust-alls (row 26).
- **U9.** Dynamic API host refresh (`ApiUrls`, `FetchedUrlHosts`, `SyncVersion`). The four-host
  list is treated as a fallback; the runtime refresh is not implemented.
- **U10.** Whether the server validates the `_uid` length/format (22 vs the captured 20).
- **U11.** Rate limits, anti-abuse, and whether a failed login attempt count is per account or per
  IP.
- **U12.** Whether the server cares about field order, extra fields, or the trailing `&` on the
  control body.

## 7. Confidence and falsifiers

**High confidence (directly checked in this slice).**
- The capture contents and the field-by-field diffs in rows 2 and C1/C2.
- The test status: `go test ./...` passes in both modules; the named tests in the matrix pass
  verbosely.
- The crypto format: 1024-bit modulus, 128-byte ciphertext, OAEP-SHA1, AES-128-CTR.
- The wiring gaps: no `NewFactory`/`Supervisor`/`gate.NewServer` caller outside tests; no
  `LoginSync` caller outside `control.go`; no keepalive/classifier symbol.

**Medium confidence (inference from client-side evidence).**
- That the live server accepts the binary carrier as implemented. The client accepted the mock;
  the mock is not the server.
- That the vendor public key is still the 1024-bit key in the three binaries. The key is embedded
  in all three; rotation would break the shipping clients, so rotation is unlikely but unproven.
- That the Linux capture's request shape is still current. The capture is 4.2.3.0; the current
  Linux client is 4.3.0.1, and the field set may have changed.

**Low confidence (no direct evidence).**
- The live auth response shape, the data-channel reply, keepalive, and anti-abuse behavior.
- The signing requirement. The secret is unknown; the unsigned path is the only captured Linux
  behavior.

**What would change this report.**
- One live login capture with the current `ajsproxy` and one with the captured Linux shape:
  resolves B1/B2 and the `v`/`_v` question.
- One live FV auth pcap plus a debug session key: resolves B3 and the `[CONFIRMED]` overclaim.
- One live data-channel pcap plus a debug session key: resolves B4.
- Two logins with the same account: resolves B5.
- A 15-minute idle session: resolves keepalive.
- A second concurrent flow with the same `sid`: resolves U6.

## 8. What I could not check

- No live vendor call, no credential, no DNS/TLS probe, no pcap. Every live statement is an
  experiment design or a prediction.
- No HMC build or remote run. Tests ran locally on Windows with Go 1.26.7.
- No dashboard browser run; the live-mode test is a Vitest stub.
- No code edits, so the proposed fixes are not applied. The report only identifies gaps.
- The `[UNVERIFIED]` items above are not claims of absence; a negative grep is not proof that a
  behavior does not exist in a binary or a server.

## 9. Evidence index

Commands run in this slice (read-only, no network):

```text
cd emulator && go test ./...
  ok ajsproxy/internal/api, config, fv, proxy
cd daemon && go test ./...
  ok ajsaccountd/internal/account, api, gate, publisher, resin, runtime, secret, store, supervisor
go test ./internal/fv ./internal/api ./internal/proxy -run '<key tests>' -v
  PASS TestBinaryCarrierCaptureVectors, TestBinaryCarrierHandshake, TestFrameRoundTrip,
       TestOAEPShape, TestTransportRegistry, TestSignVectors, TestDualHash,
       TestBuildQueryVectors, TestSOCKS5Handshake
go test ./internal/account ./internal/gate -run '<key tests>' -v
  PASS TestLoginSigned, TestLoginUnsigned, TestLoginSyncAndServers,
       TestFactoryOpenAgainstMockFV, TestLinux423BinaryProfile, TestConnectAddressTypes,
       TestAdmissionLimit, TestOpenFlowErrorRepliesRefused
cat research/60-dynamic/captures/control.jsonl
cat research/60-dynamic/captures/fvmock-1.log
cat research/60-dynamic/captures/fvmock-2.log
grep -rn "Keepalive|Ping|Pong" emulator daemon        # no production symbol
grep -rn "Classifier|Classify(" emulator daemon       # no symbol
grep -rn "NewFactory(" daemon                          # definition only
grep -rn "gate.NewServer" daemon                       # no production caller
grep -rn "\.LoginSync(" daemon emulator                # control.go + client.go only
```

Artifacts referenced by the matrix: `research/60-dynamic/captures/fvmock-1.bin` (679 B),
`fvmock-2.bin` (207 B), `fvmock-1.log`, `fvmock-2.log`, `control.jsonl`.

No files outside `research/81-protocol-gaps/` were modified. No live service was contacted.
