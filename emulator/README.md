# ajsproxy — Tier-2 AJiaSu / FV emulator

A Go implementation of the AJiaSu control API, the FV crypto/frame core, a
SOCKS5 front end, an HTTP proxy front end, and two pluggable transports. It is
the north-star slice of the offline emulator: it compiles, passes its own
conformance tests, and makes no live calls.

Module: `ajsproxy`, Go 1.26, standard library only. No external modules, no
vendor binaries, no credentials.

## Build and test

```bash
cd emulator
go build ./...
go test ./...
```

From the repository root, the conformance metric is:

```bash
python scripts/metric.py
# metric=8
```

The metric counts only the fixed allowlist of eight named tests
(`TestSignVectors`, `TestDualHash`, `TestFrameRoundTrip`, `TestAESCTR`,
`TestOAEPShape`, `TestSOCKS5Handshake`, `TestHTTPProxyCONNECT`,
`TestTransportRegistry`). Adding tests does not raise the number: this is a
**regression gate** (target 8/8), not a growth metric, so the autoresearch loop
cannot game it by writing more tests. `autoresearch.json` records
`target_metric: 8` and restricts `mutable_scope` to `emulator/internal/fv`.
The `_test.go` files are the frozen harness: loop iterations must not edit
tests, only implementation files.

The loop is not run in this slice: the tree still holds other workers'
untracked artifacts, so `init`/`baseline` (which branch and reset) would
disrupt them. The parent runs the loop after the wave is committed.

## Run

The CLI has five commands. Credentials come from the config file or the
`-username`/`-password` flags; they are never printed.

```bash
go run ./cmd/ajsproxy login       -config ../config.json
go run ./cmd/ajsproxy list        -config ../config.json
go run ./cmd/ajsproxy connect     -config ../config.json -server 12 -target example.com:443
go run ./cmd/ajsproxy serve-socks -config ../config.json -listen 127.0.0.1:1080
go run ./cmd/ajsproxy serve-http  -config ../config.json -listen 127.0.0.1:8080
```

`login` prints `AuthResult`, `UserName`, membership fields, and the server
message. `list` prints `ID / ADDR / STATUS / TITLE` for the server map.
`connect` performs login, `loginSync`, the FV authentication exchange, and
optionally opens one stream to `-target host:port`. `serve-socks` and
`serve-http` authenticate once and start a local proxy whose flows are opened
through the selected transport.

### Config example

```json
{
  "hosts": [
    "https://login.ajs-app.com",
    "https://login.mmegg.com",
    "https://login.mmegg.com:4443",
    "https://login.mbclubsh.com"
  ],
  "account": {
    "username": "REPLACE_ME",
    "password": "REPLACE_ME"
  },
  "transport": "rawtcp",
  "listen": {
    "socks5": "127.0.0.1:1080",
    "http": "127.0.0.1:8080"
  },
  "server": "",
  "core_version": "20260525-1",
  "oaep_hash": "SHA-1",
  "device": {
    "_v": "4.11.10.0",
    "_m": "android",
    "_tz": "480"
  },
  "timeouts": {
    "handshake_seconds": 20,
    "idle_milliseconds": 500
  }
}
```

`transport` is `rawtcp` (variant A) or `httpupgrade` (variant B). `server`
selects a server id; empty means the first server with `sts == "ok"` and a
usable `svr:par.fv.p`. `oaep_hash` is `SHA-1` (default) or `SHA-256`. `device`
overrides the control API device parameters; recognised keys are `_site`,
`_v`, `_m`, `_os_ver`, `_os_dev`, `_os_hw`, `_os_lang`, `_os_dim`, `_inst_mkt`,
`_lang`, `_vrf`, `_tz`.

The config contains a password: keep it outside the repository, never commit
it, and do not put real credentials in issues or reports.

## Status matrix

| Component | Status | Evidence |
|---|---|---|
| `dualHash`, `sign`, `buildQuery`, URL encoding | Implemented | Java/Python vectors in `internal/api/sign_test.go` |
| Device parameters and `_uid` | Implemented | OpenAJS field set and `_uid` formula; format test |
| Control API `login`, `loginSync`, `Servers` | Implemented | OpenAJS-style signed `POST /client.php`; failover only on transport error; httptest login test. AJS-07 saw the Linux 4.2.3.0 login send no `X-Sig`/`X-Timestamp`; that path is not emulated |
| Other control endpoints (favorites, announce, telemetry, register, captcha) | Not implemented | Out of scope for this slice |
| FV auth | Implemented | Raw-frame OpenAJS field order and binary-carrier sorted Linux fields; auth-body tests |
| RSA-OAEP, 1024-bit vendor key | Implemented | 128-byte ciphertext, `OAEP` magic, SHA-1 confirmed, SHA-256 option, generated test key round-trip |
| AES-128-CTR | Implemented | NIST SP 800-38A F.5.1 vector |
| FV frame (16-byte header IV, 4-byte header, `s/n/k/l/_`) | Implemented | Round-trip test; server-form and client-form response parsing |
| Variant A: raw TCP (`rawtcp`) | **Implemented**, fully tested in-process | Fresh TCP per flow, data frame, encrypted SOCKS5 CONNECT, `code=200` and `05 00` gates |
| Variant B: binary carrier (`httpupgrade`, protocolVersion 2) | **Implemented**; empirically confirmed against the Linux 4.2.3.0 mock, live compatibility unverified | Capture vectors `fvmock-1/2`; binary auth/data handshake test |
| Legacy text HTTP-upgrade + OPV2 | Experimental/legacy; not the Linux proxy path | OPV2 build/parse and text handshake tests |
| SOCKS5 front end | Implemented | No-auth CONNECT, ATYP 1/3/4, RFC 1928 replies, bidirectional relay; handshake test against a mock session |
| HTTP proxy front end | Implemented | CONNECT and absolute-URI rewriting; CONNECT test and absolute-URI test |
| Tier 1 wrapper (the vendor Windows/Linux client) | **Not included** | This slice is the Tier-2 emulator only |

### Variant A — raw TCP

`internal/fv/rawtcp.go`. One TCP connection to `svr:par.fv.p` per proxied flow.
The data-channel text is `s=<sid>`, `n=<payload IV hex>`, `k=`, `_=<pad>`;
the header is encrypted with a fresh `hiv` sent in the clear; the payload is a
SOCKS5 CONNECT request (`05 01 00 03 <len> <host> <port:BE>`) encrypted with
the session key and `div`. The server must answer with an encrypted header
containing `code=200` and a 10-byte `05 00` SOCKS5 reply. Relay continues both
CTR streams after those bytes.

### Variant B — binary carrier (protocolVersion 2, primary)

`internal/fv/binary.go`, dispatched from `internal/fv/httpupgrade.go`. AJS-07
captured the shipping Linux 4.2.3.0 proxy path against a mock FV server; it is
a binary carrier, **not** OPV2:

```
request  = [16B key K][AES-128-CTR(K, IV=0)(4B BE text_len || text)][body]
body     = AES-128-CTR(sessionKey, IV=n)(auth fields or SOCKS5 CONNECT)
response = [AES-128-CTR(K, IV=0)(4B BE len || text)]
auth map = AES-128-CTR(sessionKey, IV=n)(key=value\n...)
```

The body is outside the K stream: only the 4-byte length and the text are
K-encrypted. The auth text is
`s=\nn=<32hex>\nk=<base64("OAEP" || RSA-OAEP-SHA1(sessionKey))>\nl=<body len>\n_=<pad>`;
the body is sorted newline-separated auth fields (`c=au`, `u`, `p`, `cuid`,
`cseid`, `st`, `v`, `cv`, `osp`, `osd`, `osv`, `dim`, `peip`, `_`). The response
text contains `code=200`; the auth map follows it and the server must
half-close (EOF) so the client can finish reading. The data channel is a new
TCP connection with `s=<sid>`, `n=<fresh div>`, `k=`, and **no `l=`**; the body
is `AES-CTR(sessionKey, IV=div)` over
`05 01 00 03 <len> <host> <port BE>`. The response text contains `code=200`,
then a 10-byte `05 00` SOCKS5 reply encrypted with the session key and `div`,
then relay.

OAEP-SHA1 is confirmed on the mock; OAEP-SHA256 fails. The session key is 16
bytes. Live compatibility is still unverified: no real service was contacted.

`HTTPUpgrade` defaults to `ProtocolVersion = 2`; `Experimental()` stays true
because live compatibility is unverified.

### Legacy text HTTP-upgrade + OPV2

`HTTPUpgrade{ProtocolVersion: 1}` keeps the AJS-06 Windows text carrier:

```
POST /s?s=<s>&n=<n> HTTP/1.1
Upgrade: websocket
Connection: Upgrade
Content-Length: <l>
Sec-WebSocket-Key: <k>

<OPV2 packet>
```

There is no `Host` header, and `k` is 0–199 random lowercase letters in the
shipping client, not RFC 6455 base64. The OPV2 frame is:

```
[16] keyA XOR keyB
[16] keyB
[ 4] crc32(keyA XOR keyB), little-endian
[ 4] "OPV2"
[ 1] tag 0x01, [2] payload length BE, [n] payload
[ 1] tag 0x02, [2] body length BE, [m] AES-CTR(sessionKey, IV=0) of "k=v&..."
[ 1] tag 0x00
```

with bytes from offset 36 onward encrypted by AES-CTR(keyA, IV=0). The OPV2
byte layout is statically confirmed (AJS-06), but AJS-07 showed it is **not on
the Linux 4.2.3.0 proxy path**; it is retained for VPN/legacy comparison. Tag
semantics, `s`/`n` generation, and first-packet placement remain `[INFERENCE]`.

## Tests

`go test ./...` covers:

| Test | What it proves |
|---|---|
| `TestSignVectors` | Four Java-harness vectors for `dualHash` and `sign`, including the login body and non-ASCII input |
| `TestDualHash` | Independent vectors (empty input, `abc`, `hello world`) and one-byte sensitivity |
| `TestFrameRoundTrip` | Frame encode/decode, server-form and client-form responses, body field merge |
| `TestAESCTR` | NIST SP 800-38A CTR-AES128 encrypt/decrypt |
| `TestOAEPShape` | 1024-bit vendor key, 128-byte ciphertext, 132-byte `OAEP` block, generated-key round-trip for SHA-1 and SHA-256 |
| `TestSOCKS5Handshake` | Local SOCKS5 greeting, CONNECT, success reply, echo relay through a mock FV session |
| `TestHTTPProxyCONNECT` | CONNECT tunnel, 200 response, echo relay through a mock FV session |
| `TestTransportRegistry` | `rawtcp` selected by name and not experimental; `httpupgrade` experimental; unknown names rejected |
| `TestBinaryCarrierCaptureVectors` | Exact `fvmock-1/2` bytes: auth/data request layout, key/IV, session-key body decryption, response and auth-map vectors |
| `TestBinaryCarrierHandshake` | End-to-end protocolVersion=2 auth, auth map + half-close, data channel, SOCKS5 request/reply, echo relay |
| `TestOPV2Frame` | Legacy OPV2 bytes against an independently generated vector, parse round-trip, CRC rejection |
| `TestHTTPUpgradeHandshake` | Legacy text-mode upgrade, header/body shape, OPV2 SOCKS5 request and reply, echo relay |
| `TestBuildQueryVectors`, `TestUIDFormat`, `TestLoginSignsRequest`, `TestAuthBodySerialization`, `TestSocks5ConnectRequest`, `TestHTTPProxyAbsoluteURI`, `TestLoadConfig` | Supporting checks |

Only the first eight are in the metric allowlist.

## Limits

- No live service was contacted; there is no account in this repository. All
  transport tests use in-process peers.
- No UDP, `lwip`, NAT, DNS proxy, TUN device, routes, or VPN modes. The
  emulator is a userland proxy, matching the vendor's `protocol=proxy` shape.
- The control API implements only login, loginSync, and the server list.
- The legacy raw-frame auth response body has no wire length field; it is
  delimited by an idle timeout (500 ms default), the same heuristic OpenAJS
  uses. The binary carrier has an explicit length block plus a separate
  session-key auth map that requires a server half-close.
- OAEP-SHA1 is confirmed on the Linux mock; OAEP-SHA256 fails there. The
  default is SHA-1; SHA-256 is selectable but not expected to work with that
  server.
- In the binary carrier the body is outside the K-encrypted stream: only the
  4-byte length and the text are K-encrypted. OPV2 is legacy/VPN and is not on
  the captured Linux proxy path.
- The signing secret is the OpenAJS constant. It does not occur in the shipping
  binaries; their secret is unknown. AJS-07 observed the Linux 4.2.3.0 login
  sending no `X-Sig`/`X-Timestamp`; the emulator keeps the OpenAJS signed
  request and does not emulate that unsigned path.
- Variant A supports only ATYP 3 in the FV SOCKS5 request, as OpenAJS does. The
  local SOCKS5 listener accepts IPv4, IPv6, and domain names; IPv6 targets are
  forwarded as domain strings, which a strict server may reject.
- The HTTP proxy accepts `CONNECT` for any scheme and absolute-URI requests for
  `http` only; `https` through the proxy must use `CONNECT`.
- No TLS pinning or trust-all behaviour: the control client uses Go's normal
  certificate verification, unlike OpenAJS's `InsecureTLS`.
- `go test ./...` and the metric are offline and deterministic; no test sleeps
  or polls.

## Legal

The AJiaSu terms of service (section 7.2, per AJS-02 OSINT) prohibit reverse
engineering. This code is static interoperability research derived from the
public-domain OpenAJS client and from binaries already extracted by the Owner.
It makes no live API calls, contains no credentials, and provides no bypass.
Running it against the service is an Owner decision and may violate the ToS.

License: MIT pending Owner approval. No license file is added in this slice.
