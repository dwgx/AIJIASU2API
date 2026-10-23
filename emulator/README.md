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

`transport` is `rawtcp` (variant A, the OpenAJS raw-frame carrier) or
`httpupgrade` (variant B; its default `ProtocolVersion = 2` is the binary
carrier the shipping Linux 4.2.3.0 proxy path uses). The CLI default is
`rawtcp`, the OpenAJS path; the Linux profile's carrier is `httpupgrade`.
`server` selects a server id; empty means the first server with `sts == "ok"`
and a usable `svr:par.fv.p`. `oaep_hash` is `SHA-1` (default) or `SHA-256`.
`device` overrides the control API device parameters; recognised keys are
`_site`, `_v`, `_m`, `_os_ver`, `_os_dev`, `_os_hw`, `_os_lang`, `_os_dim`,
`_inst_mkt`, `_lang`, `_vrf`, `_tz`.

### Control request shapes

`api.Client.RequestShape` selects the field set. The zero value keeps the
OpenAJS/Android request, so existing callers are unchanged.

| Shape | `v`/`_v` | `_uid` | Device fields | `trigger` | `User-Agent` | Trailing `&` |
|---|---|---|---|---|---|---|
| `ShapeOpenAJS` (default) | `_v` | 22 chars, self-checksummed | `_m/_os_ver/_os_dev/_os_hw/_os_did/_os_lang/_tz/_os_dim/_inst_mkt/_lang/_vrf` | `login` | `okhttp/4.9.3` | no |
| `ShapeLinux423` | `v` | 20 lowercase hex | `_m/_os_dev/_os_dim/_os_ver/_lang` | omitted | none | yes |

`ShapeLinux423` reproduces `research/60-dynamic/captures/control.jsonl` line 2,
the only direct Linux 4.2.3.0 request:

```
_lang=en&_m=linux&_os_dev=x86_64&_os_dim=lid%3dREDACTED%2c8e415e...&_os_ver=Linux%20version...%0a&_site=ajs&_uid=<20 hex>&cmd=ClientApiLogin%2fLogin&password=<pw>&username=<user>&v=4.2.3.0&
```

The capture also fixes the headers: `Accept: */*`, `Accept-Encoding:
deflate, gzip`, `Connection: close`, no `User-Agent` and no
`X-Sig`/`X-Timestamp`. `LoginSync` has no Linux capture, so it keeps the
OpenAJS `trigger`/`_client_login` fields under either shape and its Linux form
is `[UNVERIFIED]`.

`api.Linux423Device()` returns the captured Linux descriptor with empty IDs;
`EnsureLinuxIDs` fills a 20-hex `_uid` that doubles as `cuid`, `cseid=linux`
(`LinuxCSEID`), and a 16-hex `_os_did` that the Linux control body does not
carry. `NewLinuxUID` generates the 20-hex value. `api.Device.CUID`/`CSEID`
feed the FV auth body through `AuthConfig`; empty values fall back to
`DeviceID`, so the OpenAJS body is byte-identical to before.

`TestLinux423RequestMatchesCapture` compares the recorded body with the
capture field for field (the uid and password are redacted in failure
messages), asserts the absent Android fields and `User-Agent`, and asserts the
trailing `&`. `TestOpenAJSRequestShapeUnchanged` pins the default shape.

`Login.ExpireTime` keeps its public `int64` (Unix milliseconds) but decodes
both shapes the vendor sends: the legacy JSON number (the offline fixtures
carry milliseconds, e.g. `1767225600000`), or a string — either the same
number as text (`"1767225600000"`) or a date/time in RFC3339Nano,
`"2006-01-02 15:04:05"`, `"2006-01-02T15:04:05"`, or `"2006-01-02"`. A
live login (AJS-95) discovered the string shape: the response carried
`"ExpireTime":"2026-12-21 17:09:35"`, which the stock decoder rejected with
`cannot unmarshal string into Go struct field ... of type int64`. Date
strings without a zone parse in `time.Local`; values with an offset keep
it. An empty string, `null`, or an absent field decodes to `0`. An
unsupported value fails the decode with an error naming only the
ExpireTime value and the accepted shapes — never the token, password, or
the surrounding response. `TestLoginResultExpireTimeShapes`,
`TestLoginResultExpireTimeErrors`, and `TestLoginDecodesStringExpireTime`
pin these behaviours.

The server numeric fields (`par.fv.u`, `par.fv.p`, `vsid`, `loc[].o`) keep
their public `int` semantics through `api.FlexInt`, which decodes both shapes
the vendor sends: the legacy JSON number or a numeric string. A live loginSync
(AJS-97) discovered the string shape: the response carried
`"u":"123","p":"443"`, which the stock decoder rejected with
`cannot unmarshal string into Go struct field ServerFV.Servers.par.fv.u of
type int`. A fraction or exponent is rejected; an empty string, `null`, or an
absent field decodes to `0` (and `Addr()` returns `""` when the port is 0).
An unsupported value fails the decode with a `*json.UnmarshalTypeError` the
decoder decorates with the failing field path (e.g.
`ServerFV.Servers.par.fv.p`), the offending value, and the accepted shapes —
the same message shape as the original live failure; no error ever echoes the
per-server secret or the session credentials. `Port()` casts back to `int`
and `Addr()` is unchanged; FlexInt marshals as a bare JSON number, so
re-encoded server listings stay numeric. `TestServerFlexIntShapes`,
`TestServerFlexIntErrors`, `TestServerFlexIntErrorNamesField`,
`TestLoginSyncDecodesStringServerNumbers`, and `TestFlexIntMarshalsAsNumber`
pin these behaviours.

The config contains a password: keep it outside the repository, never commit
it, and do not put real credentials in issues or reports.

## Status matrix

| Component | Status | Evidence |
|---|---|---|
| `dualHash`, `sign`, `buildQuery`, URL encoding | Implemented | Java/Python vectors in `internal/api/sign_test.go` |
| Device parameters, request shapes and `_uid` | Implemented | OpenAJS field set and `_uid` formula; Linux 4.2.3.0 shape pinned to `control.jsonl:2`; `TestLinux423RequestMatchesCapture`, `TestOpenAJSRequestShapeUnchanged`, `TestLinux423DeviceIdentity` |
| Control API `login`, `loginSync`, `Servers` | Implemented | OpenAJS signed shape and the unsigned Linux 4.2.3.0 shape (no `X-Sig`/`X-Timestamp`, no `User-Agent`, trailing `&`); failover only on transport error; httptest login tests |
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
| `TestLinux423RequestMatchesCapture` | The `ShapeLinux423` login body equals `control.jsonl:2` (only `_uid`/credentials differ), with no Android-only fields, no `User-Agent` and the trailing `&` |
| `TestOpenAJSRequestShapeUnchanged` | The default shape keeps `_v`, the 22-char `_uid`, the Android fields, `trigger=login`, `okhttp/4.9.3` and no trailing separator |
| `TestLinux423DeviceIdentity` | 20-hex `_uid`/`cuid`, `cseid=linux`, 16-hex `_os_did`, and `EnsureLinuxIDs` never regenerates |
| `TestAuthIdentityCUIDCSEID` | Both auth-body builders: empty CUID/CSEID reproduce the OpenAJS device id twice; the Linux identity sends its 20-hex `cuid` with `cseid=linux` (also asserted against the decrypted `fvmock-1` capture) |
| `TestLoginResultExpireTimeShapes` | Every accepted `ExpireTime` shape (number, numeric string, zoneless datetimes in `time.Local`, RFC3339 with offset, empty/null/absent → 0) decodes to int64 Unix milliseconds; the surrounding fields still decode |
| `TestLoginResultExpireTimeErrors` | An unsupported value fails with an error naming only the ExpireTime value and the accepted shapes — never `LoginToken`/`ConnectPassword` |
| `TestLoginDecodesStringExpireTime` | End-to-end `Login` against a fake server that returns the live-discovered date-string shape |
| `TestServerFlexIntShapes` | Every accepted server-numeric shape (JSON number, numeric string, negative, empty string/null/absent → 0) decodes; `Port()`/`Addr()` behave identically; `vsid`/`loc[].o` share the path |
| `TestServerFlexIntErrors` | A non-numeric, fractional, exponent, overflow, array, object, or bool value fails with an error naming the failing field path, the value, and the accepted shapes — never the per-server secret |
| `TestServerFlexIntErrorNamesField` | Each FlexInt field reports its own path: `par.fv.u`, `par.fv.p`, `vsid`, and `loc[].o` fail independently |
| `TestLoginSyncDecodesStringServerNumbers` | End-to-end `LoginSync` against a fake server returning the live-discovered string-numerics shape; `Addr()` = `svr.example:443` |
| `TestFlexIntMarshalsAsNumber` | `FlexInt` encodes as a bare JSON number, so re-encoded server listings stay numeric |

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
