# AJiaSu / FV Protocol Specification

Status: research baseline, 2026-10-23. Derived from the public-domain OpenAJS Java
client and cross-checked against the shipping Windows 4.9.6.0 and Linux 4.2.3.0
binaries. No live API or FV server was contacted while producing this document.

Scope: the control API (`/client.php`), its request signature, and the FV
transport used for the SOCKS-style data channel. AJS-07 captured the shipping
Linux 4.2.3.0 proxy carrier: a binary FV frame, not the OPV2/HTTP upgrade that
static analysis had suggested. The Go emulator must implement that captured
carrier (see `## Proxy carrier (empirical, AJS-07)`); OpenAJS's raw TCP frame is
the executable baseline for its fields but lacks the empirical two-layer
response and half-close.

## Evidence

### Primary source: OpenAJS

- Repository: `https://github.com/cubk1/OpenAJS`
- Commit: `cae4308bef4de609a1091b790696290eb5b69e9b` (`master`, 2026-06-07,
  commit message `Create LICENSE`), cloned 2026-10-23 with `git clone --depth 1`.
- Clone path used for this study:
  `C:/Users/dwgx1/AppData/Local/Temp/ajs-proto-20261023/OpenAJS`
- License: Unlicense / public domain (`LICENSE`). `tests/java/.../FVSign.java`
  is a verbatim copy used only as an independent vector generator.

Every file:line below is relative to
`src/main/java/us/cubk/openajs/` in that clone.

| Area | Reference | Status |
|---|---|---|
| Host list, secret, fallback | `api/AjsClient.java:59`, `:69-102` | directly implemented |
| Request headers and body | `api/AjsClient.java:71-87` | directly implemented |
| Device/common parameters | `api/AjsDevice.java:14-53` | directly implemented |
| `_uid` derivation | `api/AjsDevice.java:30-36` | directly implemented |
| Login | `api/AjsClient.java:108-122` | directly implemented |
| `loginSync`, `servers()` | `api/AjsClient.java:124-156` | directly implemented |
| Endpoint catalogue | `api/AjsClient.java:158-260` | directly implemented |
| URL encoding, query, sign | `api/crypto/FVSign.java:12-73` | directly implemented |
| `dualHash` | `api/crypto/FVSign.java:39-60` | directly implemented |
| FV auth body | `api/fv/FvConnection.java:42-66` | directly implemented |
| FV auth exchange | `api/fv/FvConnection.java:68-91` | directly implemented |
| FV response parser | `api/fv/FvConnection.java:220-240` | directly implemented |
| FV frame builder | `api/fv/FvConnection.java:242-251` | directly implemented |
| OAEP block magic | `api/fv/FvConnection.java:252-261` | directly implemented |
| RSA key and OAEP | `api/fv/FvCrypto.java:18-46` | directly implemented |
| AES-CTR | `api/fv/FvCrypto.java:48-56` | directly implemented |
| Data-channel open | `api/fv/FvConnection.java:159-205` | directly implemented |
| Local SOCKS5 server | `api/fv/SocksProxyServer.java:48-107` | directly implemented |
| Relay | `api/fv/FvStream.java:20-35`, `SocksProxyServer.java:116-147` | directly implemented |
| TLS verification disabled | `api/net/InsecureTLS.java:13-41`, called at `AjsClient.java:66` | directly implemented |

The Java code proves what the client **sends** and what it **expects to
receive**. It does not prove what the server does. Server-side statements are
marked `[INFERENCE]` unless a second source confirms them.

### Secondary source: shipping clients

Windows 4.9.6.0:

- Installer: `D:/Project/LovePlusSpeedToProxy/research/AJiaSu_4.9.6.0.exe`
  (NSIS, product version 4.9.6.0).
- Extracted payload: `C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023/`
  (37 files). Main PE: `AJiaSu.exe`, 4,064,640 bytes, sha256
  `0fd9d702bf9932836ad0ba3e4acf43bb1a587c45ee6247b7a5d88aede78101f6`
  (recorded in `_work/inv.json`), PE32 i386, timestamp 2025-07-09.
- Command used: `strings -t x -n 5 AJiaSu.exe`. Offsets below are file offsets.

| Literal | Offset | Meaning |
|---|---|---|
| `/client.php` | `0x31641c` | control endpoint path |
| `ClientApiLogin/Login` | `0x31e3f0` | login/loginSync command |
| `ClientApiUser/UserVpnServerFavoriteList` | `0x3163d4` | favourite list |
| `ClientApiUser/VpnServerFavorite` | `0x31642c` | favourite mutation |
| `ClientApiVpnServer/AutoMatch` | `0x319e8c` | auto-match |
| `ClientApiMessageCenter/GetAnnounce` | `0x3165ac` | announce |
| `ClientApiMessageCenter/CloseAnnounce` | `0x31669c` | close announce |
| `ClientApiMessageCenter/GetMessageNum` | `0x3167f4` | unread counters (not in OpenAJS) |
| `ClientApiVerifyCode/Captcha` | `0x3168fc` | captcha |
| `ClientApiVerifyCode/SendVerifyCode` | `0x3169b4` | verification code |
| `ClientApiRegister/Register` | `0x316a20` | registration |
| `ClientApiUpgrade/UpgradeRecord` | `0x316b48` | upgrade telemetry |
| `ClientApiClientSetting/ClientSetting` | `0x316b94` | settings |
| `ClientApiClientSetting/LoadRoute` | `0x31e730` | route download (not in OpenAJS) |
| `ClientApiReport/Disconnect` | `0x317040` | disconnect telemetry (not in OpenAJS) |
| `ClientApiReport/Report` | `0x3170e8` | generic telemetry (not in OpenAJS) |
| `ClientApiReport/WebsiteCheckReport` | `0x31e9c4` | URL check telemetry (not in OpenAJS) |
| `X-Sig: ` | `0x31e144` | signature header |
| `X-Timestamp: ` | `0x31e150` | timestamp header |
| `06ka4z0dc6ga02c` | `0x31e160` | unclassified 15-char constant next to the signing headers `[UNKNOWN]` |
| `OPV2` | `0x3216d0` | VPN/control builder magic; not on the captured proxy path |
| `POST /s?s=%s&n=%s HTTP/1.1` | `0x3216d8` | HTTP upgrade literal; `[OVERTURNED]` as the proxy carrier |
| `code=200` | `0x3217c4` | auth/data response success text |
| `cseid` | `0x31f328` | FV auth field |

Linux 4.2.3.0:

- Client: `C:/Users/dwgx1/AppData/Local/Temp/ajs-linux-20261023/ajiasu`
  (3,578,792 bytes, static stripped ELF x86-64, version string `4.2.3.0`).
- Command used: `strings -t x -n 5 ajiasu`.

| Literal | Offset |
|---|---|
| `https://login.ajs-app.com/` | `0x28ac34` |
| `https://login.mmegg.com/` | `0x28ac4f` |
| `https://login.mmegg.com:4443/` | `0x28ac68` |
| `https://login.mbclubsh.com/` | `0x28ac86` |
| `/client.php` | `0x28d901` |
| `ClientApiLogin/Login` | `0x28da99` |
| `X-Sig: ` | `0x28daae` |
| `X-Timestamp: ` | `0x28dab6` |
| `POST /s?s=%s&n=%s HTTP/1.1` | `0x28ee1d` |
| `code=200` | `0x28eecf` |
| `cseid` | `0x28afb2` |
| `auth.password`, `auth.group`, `auth.forbidden`, `auth.membership`, `auth.limit`, `auth.off`, `session.timeout` | `0x28b2d0`-`0x28b31c` |
| `MAX_RESPONSE_HEADER=%d` | `0x28b55a` |

Dynamic capture (AJS-07, 2026-10-23):

- `research/60-dynamic/REPORT.md` — Linux 4.2.3.0 against a local mock control
  plane and mock FV server; no real API/FV host contacted.
- `research/60-dynamic/captures/fvmock-1.bin` / `.log` — 679-byte auth
  connection and decrypted text/body.
- `research/60-dynamic/captures/fvmock-2.bin` / `.log` — 207-byte data-channel
  connection and decrypted SOCKS5 CONNECT.
- `research/60-dynamic/captures/control.jsonl` — `/client.php` requests; Linux
  4.2.3.0 sends no `X-Sig`/`X-Timestamp`.

These captures are the primary evidence for `## Proxy carrier (empirical,
AJS-07)` and overturn the earlier HTTP/`OPV2` proxy inference.

### Directly implemented vs inferred

Directly implemented in OpenAJS (and reproduced in `reference/fv_proto.py`):
host list, `dualHash` signing, URL encoding, common parameters, login and
server-list parsing, FV auth field set, AES-CTR framing, RSA-OAEP call, OAEP
block magic, data-channel SOCKS5 request, `code=200` gate, 10-byte reply check,
local SOCKS5 handshake, relay.

`[INFERENCE]`:
- The server reuses the client's header key/IV and body IV for its response.
  AJS-07's mock capture confirms this for Linux 4.2.3.0; the general server
  behavior remains inferred.
- `cv=20260525-1` is meaningful to the server.
- `par.fv.u` and `par.fv.ps` are optional/deprecated, because OpenAJS ignores
  both and authenticates with the account `UserName`/`ConnectPassword`.
- Field semantics such as `_tz` being minutes and `_vrf` being a channel name.
- Error token mapping (`auth.password`, etc.) to specific `r` values.
- The four control-API hosts are a fallback list; 4.9.6.0 can refresh the API
  domain set at runtime.

`[OVERTURNED]`:
- The earlier claim that the shipping proxy mode uses the HTTP
  `POST /s?s=...&n=...` + WebSocket upgrade and/or `OPV2`. AJS-07 captured the
  Linux 4.2.3.0 `protocolVersion=2` proxy path as a binary
  `[16B K][AES-CTR(K,0)(len||text||body)]` carrier with no `OPV2` and no HTTP.
  See `## Proxy carrier (empirical, AJS-07)`. `OPV2` remains a VPN/control
  builder.

`[UNKNOWN]`:
- The signing secret used by the shipping 4.9.6.0 client. The OpenAJS constant
  does not occur as ASCII or as 20 raw bytes in `AJiaSu.exe` or `ajiasu`
  (searched 2026-10-23; zero hits). The 15-char literal `06ka4z0dc6ga02c` is
  present in Windows 4.9.6.0 and Linux 4.3.0.1, absent from Linux 4.2.3.0;
  its role is unproven. Linux 4.2.3.0 login sends no `X-Sig`/`X-Timestamp`, so
  the secret is not exercised there.
- Whether responses are signed or otherwise authenticated.
- `OPV2` framing and its role outside the captured proxy path,
  `buffer_obscure_packet`, `protocolVersion`/`implType` selection in other
  builds, `kapf`, `peip`, `ping`/`pong`, and `aur` packets.
- UDP, `lwip`, NAT and DNS-proxy modes: absent from OpenAJS.
- `sid` lifetime, concurrency limits, and whether one `sid` may open multiple
  data connections.

Additional sources used for the version matrix and transport comparison:
`research/20-windows-static/INVENTORY.md` (AJS-03, 2026-10-23) and AJS-02 OSINT
(2026-10-23, as relayed by the main agent). Those are secondary summaries; the
underlying offsets were re-checked against `AJiaSu.exe` for this document.

## Control API

### Hosts and failover

`AjsClient.java:59` hard-codes this ordered list:

```
https://login.ajs-app.com
https://login.mmegg.com
https://login.mmegg.com:4443
https://login.mbclubsh.com
```

The Linux 4.2.3.0 client contains the same four hosts with a trailing slash
(offsets in Evidence). The Windows client contains no API host as a plain
string: `research/20-windows-static/INVENTORY.md` records that the hosts live in
the encrypted `product.dat`, and `AJiaSu.exe` carries `ApiUrls` (`0x31e31c`),
`ApiLoginSync`, and `SyncVersion` literals. Since 4.9.6.0 the API domain set can
therefore be delivered or refreshed at runtime `[INFERENCE]`.

Treat the four URLs above as **OpenAJS's built-in fallback list**, not as a
guaranteed current host list. A Go implementation should make the host list
configurable and should not assume that `login.mmegg.com:4443` still exists.

For each host OpenAJS requests `host + "/client.php"` (`AjsClient.java:82`).
Hosts are tried in order, but only an `IOException` advances to the next host
(`AjsClient.java:90-101`). An HTTP 4xx/5xx, an invalid JSON body, or a
protocol-level error does **not** trigger failover. Connect timeout is 15,000 ms
and read timeout 20,000 ms (`AjsClient.java:42-43`).

TLS: the constructor calls `InsecureTLS.enableGlobal()` (`AjsClient.java:66`),
which installs a trust-all `X509TrustManager` and an allow-all
`HostnameVerifier` (`InsecureTLS.java:13-41`). Consequently the control channel
is vulnerable to MITM; only `X-Sig` binds the request body.

### Request format

`AjsClient.call()` (`AjsClient.java:69-102`) builds:

```
POST /client.php HTTP/1.1
Host: <selected host>
Content-Type: application/x-www-form-urlencoded
Accept-Encoding: gzip
User-Agent: okhttp/4.9.3
X-Timestamp: <epoch milliseconds, decimal>
X-Sig: <16 lowercase hex characters>
Content-Length: <set by HttpURLConnection>

<percent-encoded query string>
```

There is no cookie, no `Authorization`, no device header, and no request
signature over the headers. `HttpClient.post()` sets method `POST` and writes
the string as UTF-8 bytes (`HttpClient.java:22-28`, `:43-46`). Responses are
read from the input stream for status < 300 and from the error stream otherwise;
`Content-Encoding: gzip` and `deflate` are decoded (`HttpClient.java:78-103`).

The body is produced by `FVSign.buildQuery(TreeMap)` (`AjsClient.java:71-77`):

1. Start from `device.commonParams()`.
2. Insert `cmd=<command>`.
3. Insert endpoint-specific parameters, overwriting same-named keys.
4. Sort keys ascending (Java `TreeMap` natural order; ASCII keys sort
   byte-wise).
5. Emit `key=value` pairs joined by `&`, with **values** percent-encoded and
   **keys** verbatim. No trailing `&`.

### Common device parameters

`AjsDevice.commonParams()` (`AjsDevice.java:37-53`) always sends these fields:

| Field | Java default | Meaning / notes |
|---|---|---|
| `_site` | `ajs` | product/site identifier |
| `_v` | `4.11.10.0` | client version reported by OpenAJS; shipping clients are 4.9.6.0 (Windows) and 4.2.3.0 (Linux) |
| `_m` | `android` | platform |
| `_os_ver` | `13` | OS version |
| `_os_dev` | `Xiaomi 23013RK75C` | device model |
| `_os_hw` | `qcom` | hardware/SoC |
| `_os_did` | 16 random lowercase hex chars | device id; `RandomUtil.hex(16)` |
| `_os_lang` | `zh-Hans-CN` | OS language |
| `_tz` | local raw UTC offset in minutes, e.g. `480` | computed once at object construction from `TimeZone.getDefault().getRawOffset() / 60000` |
| `_os_dim` | `w=1080&h=2400&d=440` | screen width/height/density; the raw value contains `&`, so the encoded body contains `%26` |
| `_inst_mkt` | `google` | install market |
| `_lang` | `zh_cn` | app language |
| `_vrf` | empty string | verification/channel field; encoded as `_vrf=` |
| `_uid` | generated, 22 chars | see below |

`_uid` generation (`AjsDevice.java:30-36`):

```
base = sprintf("%08x%02x%08x", int(now_ms / 1000), int(now_ms & 0xFF), random_int32)
checksum = hex(dualHash(utf8(base)))[0:4]
uid = base + checksum
```

The random component is `ThreadLocalRandom.nextInt()`. The checksum is the
first four lowercase hex characters of the dual hash, so the UID is
self-consistent but not secret.

### Login

`AjsClient.login(username, password)` (`AjsClient.java:108-122`):

Request parameters (in addition to the common set and `cmd`):

| Field | Value |
|---|---|
| `cmd` | `ClientApiLogin/Login` |
| `username` | account name (email or phone as entered) |
| `password` | plaintext password |
| `trigger` | `login` |

Response JSON is expected to contain a `Login` object. Field names from
`api/model/LoginResult.java`:

| JSON field | Type | Notes |
|---|---|---|
| `AuthResult` | string | `OK` means success (`LoginResult.isOk()` is a case-sensitive comparison) |
| `AuthMessage` | string | human-readable failure text |
| `LoginToken` | string | session token; OpenAJS uses the substring before `|` as a username fallback |
| `UserName` | string | account user name used as FV `u` |
| `ConnectPassword` | string | FV `p` credential |
| `Email` | string | profile |
| `PhoneNumber` | string | profile |
| `MembershipStatus` | string | membership state |
| `GroupTitle` | string | membership label |
| `ExpireTime` | long | membership expiry; units not established `[UNKNOWN]` |
| `Expired` | boolean | expiry flag |
| `UseCount` | int | usage counter |

On success OpenAJS stores `loginToken`, `userName`, and `connectPassword`
(`AjsClient.java:116-120`). It does not verify `Expired`, `ExpireTime`, or
`MembershipStatus` before connecting.

### loginSync and servers()

`callAuth(cmd, extra)` (`AjsClient.java:124-137`) requires a non-empty login
token and adds:

| Field | Value |
|---|---|
| `_client_login` | the full `LoginToken` |
| `username` | `userName` if set, else the token prefix before `|`, else the whole token |

`loginSync()` (`AjsClient.java:139-148`) calls
`ClientApiLogin/Login` through `callAuth` with `trigger=login`, then refreshes
`ConnectPassword` from `Login.ConnectPassword` if present.

`servers()` (`AjsClient.java:150-156`) calls `loginSync()` and returns the
`Servers` member of the same JSON object, deserialized as
`Map<String, VpnServer>`. If `Servers` is absent it returns an empty map. The
map key is the server id as a string; the value shape is
`api/model/VpnServer.java`:

```json
{
  "Servers": {
    "<id>": {
      "id": "<id>",
      "vsid": 1,
      "ctry": "CN",
      "svr": "203.0.113.10",
      "tit": "Shanghai 01",
      "cmt": "",
      "sts": "ok",
      "ord": "1",
      "cat": ["cn"],
      "par": {"fv": {"u": 1001, "p": 443, "ps": "<per-server secret>"}},
      "loc": [{"c": "CN", "n": "Shanghai", "o": 1}]
    }
  }
}
```

This object is illustrative: field names and types come from the model, not from
a captured response. `sts == "ok"` is the only liveness test OpenAJS applies
(`VpnServer.isOk()`). `VpnServer.port()` returns `par.fv.p`, `uid()` returns
`par.fv.u`, `secret()` returns `par.fv.ps`.

Important: OpenAJS's FV connection uses `UserName` and `ConnectPassword` from the
login response, **not** `par.fv.u` / `par.fv.ps` (`ProxyService.java:33`). The
purpose of `u` and `ps` in the server object is therefore `[UNKNOWN]`; they may
be intended for a different protocol revision or for UDP/lwip.

### Endpoint catalogue

All commands use `POST /client.php` with the common parameters and `cmd`.
"auth" means the request was built by `callAuth` and therefore includes
`_client_login` and `username`. "plain" means `call()` without a token.

| `cmd` | Auth | Extra request fields | Response fields (Java model) |
|---|---|---|---|
| `ClientApiLogin/Login` | plain | `username`, `password`, `trigger=login` | `Login{...}`; see Login |
| `ClientApiLogin/Login` (sync) | auth | `trigger=login` | `Login.ConnectPassword`, `Servers{}` |
| `ClientApiUser/UserVpnServerFavoriteList` | auth | none | `Result`, `Data[]` (`FavoriteListResult`) |
| `ClientApiUser/VpnServerFavorite` | auth | `Action=favorite\|unfavorite`, `ServerId` | `Result`, `Message`, `Error`, `ErrorMessage` |
| `ClientApiVpnServer/AutoMatch` | auth | `LocationLevel`, `LocationCode` | `Result`, `Error`, `PopupType`, `Data.VpnServerId` |
| `ClientApiMessageCenter/GetNotificationList` | auth | `pageSize`, optional `lastNotificationId` | `Result`, `List[{id, NotificationTitle, NotificationContent, ExtCreateTimestamp, NotificationImage}]` |
| `ClientApiMessageCenter/GetUnreadNotificationCount` | auth | none | `Result`, `Count` |
| `ClientApiMessageCenter/GetAnnounce` | auth | none | `Content`, `Error`, `ErrorMessage` |
| `ClientApiMessageCenter/CloseAnnounce` | auth | `msgid` | `Result`, `Message`, `Error`, `ErrorMessage` |
| `ClientApiClientSetting/ClientSetting` | auth | `ClientSettings[<Key>]=<value>` for each setting | `Result`, `Message`, `Error`, `ErrorMessage` |
| `ClientApiUpgrade/UpgradeRecord` | plain | `UpgradeType`, `PriorVersion`, `UpgradeDatetime`, optional `username`, optional `PopupInterval` | `Result`, `Message`, `Error`, `ErrorMessage` |
| `ClientApiVerifyCode/Captcha` | plain | `Usage`, optional `Mode` | `Captcha` (base64 image), `ErrorMessage` |
| `ClientApiVerifyCode/SendVerifyCode` | plain | `Mode=mobile\|email`, `Usage`, `CaptchaText`, `AccountName` | `Result`, `Message`, `Countdown` |
| `ClientApiRegister/Register` | plain | `AccountName`, `Password`, `Mode`, `InvitationCode`, `VerifyCode` | `AccountName`, `Password`, `ErrorMessage` |
| `ClientApiUser/ResetPassword` | plain | `Mode`, `AccountName`, `VerifyCode`, `NewPassword` | `Result`, `Message`, `Error` |

Account helpers (`api/util/AccountUtil.java`): a mobile account matches
`^1\d{10}$`, `Mode` becomes `mobile`, and `AccountName` becomes `+86-<number>`;
otherwise `Mode=email` and the account is unchanged.

### Endpoints present only in the shipping clients

The Windows 4.9.6.0 strings contain five commands that OpenAJS does not
implement. They are listed for completeness; their request/response schemas are
`[INFERENCE]` from adjacent string literals.

| `cmd` | Adjacent literal fields | Offset |
|---|---|---|
| `ClientApiMessageCenter/GetMessageNum` | `Broadcast`, `Notice`, `LastMsgTime`, `LastRefreshMessageNumAnnounceTime` | `0x3167f4` |
| `ClientApiClientSetting/LoadRoute` | `RouteSection` | `0x31e730` |
| `ClientApiReport/Disconnect` | `SdkVersion`, `VpnServerId`, `UploadDatetime`, `ConnectDatetime`, `LastActiveDatetime`, `Protocol`, `RouteMode`, `RouteModeId`, `BytesRx`, `BytesTx`, `ReconnectIndex`, `StopType`, `StopMessage`, `DisconnectParams` | `0x317040` |
| `ClientApiReport/Report` | `reportActionType`, `deviceData`, `networkData`, `appData`, `Params` | `0x3170e8` |
| `ClientApiReport/WebsiteCheckReport` | `CheckResult`, `RemoteIp`, `RemotePort`, `StatusCode`, `ResponseString`, `CheckTime`, `ResponseTime` | `0x31e9c4` |

`GetNotificationList` is absent from the Windows string dump; it may be
assembled at runtime or removed in 4.9.6.0 `[UNKNOWN]`.

### Response envelope

`ApiResult` (`api/model/ApiResult.java`) carries the HTTP status, raw body,
parsed JSON object (or `null` if parsing failed), and final URL. OpenAJS does not
require a particular HTTP status or content type; it attempts JSON parsing
regardless. Error responses are ordinary JSON objects whose `Result` is not
`Success`, or which carry `Error`/`ErrorMessage`. Whether responses are signed
or replay-protected is `[UNKNOWN]`; OpenAJS ignores response headers.

## Signing

### dualHash

`FVSign.dualHash` (`FVSign.java:39-60`) is an 8-byte non-cryptographic hash with
two 32-bit accumulators:

```
h1 = 0xCAFEBABE
h2 = 0xDEADBEEF

for each byte c of the input, in order:
    h2 = (h2 XOR ((c + 32*h2 + (h2 >> 2)) AND 0xFFFFFFFF)) AND 0xFFFFFFFF
    h1 = (c + 65599*h1) AND 0xFFFFFFFF

output = bytes(
    h2 >> 24, h1 >> 24,
    h2 >> 16, h1 >> 16,
    h2 >>  8, h1 >>  8,
    h2,       h1
)
```

All arithmetic is unsigned 32-bit; `>>` is a logical shift because `h2` is
always masked into `[0, 2^32)`. The Java implementation unrolls the loop two
bytes at a time and handles a trailing odd byte separately, but the per-byte
update is identical, so the pairing has no effect on the result. Output bytes
are interleaved high-to-low between the two accumulators. The empty input
therefore hashes to `decaadfebebaefbe` (the two seed values interleaved).

### URL encoding

`FVSign.urlEncode` (`FVSign.java:12-25`) percent-encodes the UTF-8 bytes of a
value:

- Literal: `A-Z`, `a-z`, `0-9`, `-`, `.`, `_`, `~`.
- Everything else: `%` followed by **two lowercase** hex digits.
- Space is `%20`, never `+`.
- Non-ASCII characters are encoded byte-by-byte from UTF-8.

Keys are not encoded by `buildQuery`; only values are. The query is sorted by
key, joined with `&`, and has no trailing separator.

### Signature

`FVSign.sign` (`FVSign.java:70-73`):

```
material = secret + "|" + body + "|" + timestamp + "|" + secret
signature = lowercase_hex(dualHash(utf8(material)))
```

- `secret` is the constant in `AjsClient.java:59`:
  `84:C1:12:45:0B:68:F9:B2:06:95:00:44:74:37:55:71:60:B5:98:31`
  (20 bytes written as colon-separated uppercase hex; the colons are part of the
  string).
- `body` is the exact byte-for-byte query string that is sent.
- `timestamp` is `System.currentTimeMillis()` rendered as a decimal string, so
  it is milliseconds, not seconds.
- The secret appears twice, once at each end.
- The result is 16 lowercase hex characters.

Because the signature covers the exact body, any change to parameter order,
encoding, or version invalidates it. The timestamp is not included in the body;
it travels only in `X-Timestamp` and inside the signed material.

### Worked example

Inputs:

```
secret    = 84:C1:12:45:0B:68:F9:B2:06:95:00:44:74:37:55:71:60:B5:98:31
body      = _site=ajs&_v=4.11.10.0&cmd=ClientApiLogin%2FLogin&password=p%40ss&trigger=login&username=test%40example.com
timestamp = 1761177600000
```

Signed material:

```
84:C1:12:45:0B:68:F9:B2:06:95:00:44:74:37:55:71:60:B5:98:31|_site=ajs&_v=4.11.10.0&cmd=ClientApiLogin%2FLogin&password=p%40ss&trigger=login&username=test%40example.com|1761177600000|84:C1:12:45:0B:68:F9:B2:06:95:00:44:74:37:55:71:60:B5:98:31
```

Results (Java harness and Python reference agree):

```
dualHash(body) = 27b77ba0004e206d
X-Sig          = 80c6a846fc6a00f3
```

### Vectors

Generated by compiling `tests/java/us/cubk/openajs/api/crypto/FVSign.java` with
JDK 21 and running `tests/java/VectorHarness.java`; the Python implementation
must reproduce them exactly. All use the secret above.

| Input (UTF-8) | Timestamp | `dualHash` | `sign` |
|---|---|---|---|
| empty string | `1761177600000` | `decaadfebebaefbe` | `cbfde26ee626242c` |
| `abc` | `1761177600000` | `bf7ff86122acbd24` | `881953a05577bb2a` |
| login body from the worked example | `1761177600000` | `27b77ba0004e206d` | `80c6a846fc6a00f3` |
| `中文&ünïcode` | `1761177600123` | `3d7e935e13359844` | `cfac37be29335bb4` |

Query serialization vectors:

| Input map | Output |
|---|---|
| `{b:"2", a:"1", c:"a b&c=d/é"}` | `a=1&b=2&c=a%20b%26c%3dd%2f%c3%a9` |
| `{_os_dim:"w=1080&h=2400&d=440", _site:"ajs"}` | `_os_dim=w%3d1080%26h%3d2400%26d%3d440&_site=ajs` |
| `{z:"", A:"~-._"}` | `A=~-._&z=` |

## FV transport

### Version matrix

`_v` (control API) and `cv` (FV auth) are client-version claims, not protocol
version negotiation. The values below are the evidence baseline for this wave.

| Client | Version | Core build stamp | Evidence |
|---|---|---|---|
| Windows (official current) | 4.9.6.0 | `20250708-1` | installer `research/AJiaSu_4.9.6.0.exe`; `AJiaSu.exe` build 2025-07-09; AJS-03/AJS-04 comparison table |
| Linux (official current) | 4.3.0.1 | `20260525-1` | AJS-02 OSINT 2026-10-23; `research/30-linux-static/REPORT.md` |
| Linux binary analysed here | 4.2.3.0 | `20241105-1` | `ajiasu` literal `4.2.3.0` at file offset `0x28acd9`; archive `ajiasu-4.2.3.0.tar.gz`; AJS-04 |
| Android (official current) | 4.12.2.0 | `[UNKNOWN]` | AJS-02 OSINT, 2026-10-23 |
| OpenAJS emulation | Android 4.11.10.0 | `20260525-1` (hard-coded `cv`) | `AjsDevice.java:15`; `FvConnection.java:52` |

The core build stamp is the `cv` value OpenAJS sends. `cv=20260525-1` matches
the Linux 4.3.0.1 core, not the 4.2.3.0 binary analysed here; OpenAJS therefore
tracks the newer core `[INFERENCE]` from the stamp match, which AJS-04 also
states.

`cv=20260525-1` looks like a build date plus revision, but its acceptance
rules are `[UNKNOWN]`. The Go emulator should treat `_v`, `cv`, and the device
parameters as configuration, not constants.

### Endpoint

The FV endpoint comes from the selected `VpnServer`:

| Source field | Use |
|---|---|
| `VpnServer.svr` | TCP host passed to `InetSocketAddress` (`ProxyService.java:33`) |
| `VpnServer.par.fv.p` | TCP port (`VpnServer.port()`) |
| `VpnServer.par.fv.u` | unused by OpenAJS `[UNKNOWN]` |
| `VpnServer.par.fv.ps` | unused by OpenAJS `[UNKNOWN]` |

OpenAJS opens a plain TCP connection (`new Socket()`), with no TLS and no
HTTP. Connect and read timeouts are 15,000 ms during authentication
(`ProxyService.java:33`, `FvConnection.java:81-83`). In the raw variant (A) the
data channel opens a **fresh TCP connection per proxied flow** to the same host
and port; it does not multiplex flows over the control socket. AJS-07 observed
the same one-connection-per-flow behavior on the captured Linux proxy path
(`captures/fvmock-2.bin`); multiplexing was not tested.

### Authentication exchange

Three random 16-byte values are generated per connection with
`SecureRandom` (`FvConnection.java:70-75`):

- `sessionKey`: AES-128 key for payload encryption.
- `bodyIv`: payload IV for the authentication body.
- `headerIv`: header IV for the frame text. It is used as the CTR key and is
  also sent in cleartext as the 16-byte prefix; AJS-07 calls this prefix `K`.

The authentication body is the following text, UTF-8, one `key=value` line per
field, each terminated by `\n` (including the last line):

```
t=<unix seconds>\n
c=au\n
u=<UserName>\n
p=<ConnectPassword>\n
cuid=<deviceId>\n
cseid=<deviceId>\n
st=<site>\n
v=<appVersion>\n
cv=20260525-1\n
osp=<platform>\n
osd=<osDevice>\n
osv=<osVersion>\n
dim=<osDim>\n
ce=\n
```

Sources: `FvConnection.java:42-58` for the field set and insertion order,
`FvConnection.java:61-66` for serialization. Values have newlines replaced by
spaces; there is no escaping for `=`, `&`, `%`, or non-ASCII. `cuid` and
`cseid` are the same device id in OpenAJS (16 hex chars); AJS-07's Linux
capture used a 20-hex `cuid`. `t` is seconds since the epoch, unlike the
control API timestamp which is milliseconds.

AJS-07's captured Linux auth body has the same fields plus `peip=` (empty) and
sends them in alphabetical order rather than OpenAJS's insertion order. Whether
the server requires `peip` or a particular order is `[UNKNOWN]`; the mock
accepted the Linux form.

The body plaintext is encrypted with AES-128-CTR using `sessionKey` and
`bodyIv`:

```
body_ciphertext = AES-CTR(key=sessionKey, iv=bodyIv, plaintext=auth_body)
```

The frame text is then:

```
s=
n=<bodyIv as 32 lowercase hex chars>
k=<base64("OAEP" || RSA-OAEP(sessionKey))>
l=<decimal byte length of body_ciphertext>
_=<random padding>
```

- `s` is empty for the control connection.
- `k` is standard Base64 with padding. The OAEP block is the 4 ASCII bytes
  `OAEP` followed by the raw RSA ciphertext (`FvConnection.java:252-261`).
- `l` is the ciphertext length, which equals the plaintext length under CTR.
- `_` is 0 to 199 lowercase ASCII letters (`FvConnection.java:302-309`).
- There is no trailing newline after `_=<pad>`.

The RSA key is 1024-bit, not 2048-bit: `FvCrypto.java:21` contains a 309-digit
modulus whose `int.bit_length()` is 1024, so the ciphertext is 128 bytes and the
OAEP block is 132 bytes (Base64: 176 characters). OAEP uses SHA-1 and
MGF1-SHA-1 by default (`FvCrypto.java:31-40`); `-Dfv.oaep=SHA-224|SHA-384|SHA-512`
switches the digest, and any other value selects SHA-256. With a 1024-bit key
and SHA-1, the maximum OAEP payload is 86 bytes; the 16-byte session key fits.

The exact public key (`FvCrypto.java:21-22`) is:

```
n = 141601187905926909444644730286409666755495300050721564398199769080746616339148556683219048017120018748016428099359153205651362664001287890722535799466700786136519566714160932079339659299698611914846217596226121056961277268141789905486380539961102053398435632146710731361922836526658451793921073088741138357721
e = 65537
```

A 256-byte ciphertext would require a 2048-bit modulus and is not what the
OpenAJS constant produces.

The frame is built as follows (`FvConnection.java:242-251`):

```
header_plain = 00 00 <text_len:uint16 big-endian> <text UTF-8>
header_ct    = AES-CTR(key=headerIv, iv=00*16, plaintext=header_plain)
frame        = headerIv || header_ct
```

The 16-byte header IV is sent in cleartext as the first 16 bytes of the frame.
The two reserved bytes are zero. The 16-bit length counts only the UTF-8 text,
not the header or IV. The client then writes `frame || body_ciphertext` in one
`write` and flushes (`FvConnection.java:85`).

### Response format

The server response is read with a heuristic, not a fixed length: OpenAJS
accumulates up to 65,536 bytes, and after each read, if no bytes are immediately
available, sleeps 150 ms and stops if still none
(`FvConnection.java:263-285`). This is an implementation convenience; the wire
format below is what matters.

The response frame has **no IV prefix**. It is:

```
<encrypted 4-byte header><encrypted text><encrypted body>
```

Decoding (`FvConnection.java:220-240`):

1. Decrypt the first 4 bytes with `AES-CTR(headerIv, IV=0)`; read
   `textLen = bytes[2]<<8 | bytes[3]`.
2. The next `textLen` bytes, decrypted with the same CTR stream, are the UTF-8
   header text.
3. The remaining bytes start at `4 + textLen`; decrypt them with
   `AES-CTR(sessionKey, bodyIv, ...)` at CTR position 0.
4. Split the body on `\n`, split each line at the first `=`, and keep lines
   where `=` is not at position 0.

The authentication result is `r=ok` (exact, lowercase). `sid` is copied from
the body and used for all subsequent data channels. OpenAJS ignores the
response header text during authentication; it only uses it to find the body
offset.

This means the server is expected to reuse the client's `headerIv` for the
response header and the client's `bodyIv` for the response body, each starting
at CTR position 0. AJS-07 confirmed this for the Linux 4.2.3.0 proxy path: the
34-byte response is `[4B CTR(K,0)(len)][CTR(K,4..)(text)]`, followed by a
separate `AES-CTR(sessionKey, IV=n)` auth map; the server must half-close after
the map. The general server behavior remains `[INFERENCE]`. The construction
also means the same key/IV pair encrypts the client's auth body and the server's
response body, a keystream reuse that is a protocol weakness.

### Response fields observed in the shipping clients

Linux 4.2.3.0 has a contiguous literal table at `0x28af6f`-`0x28afd5` (visible
with `strings -n 2`):

```
ok  sid  sip  cip  dns  ka  kat  kapf  mtu  ft  fm  au  cuid  cseid  st  cv  osp  osd  osv  dim  peip  ce
```

OpenAJS only consumes `r` and `sid` from the auth response body. AJS-07's
captured auth map contains `c=aur`, `r=ok`, `sid`, `s`, `ok=1`, `sip`, `cip`,
`dns`, `ka`, `kat`, `kapf`, `mtu`, `au`, `st`, `cv`, `osp`, `osd`, `osv`,
`dim`, `peip`, `ce` (`captures/fvmock-1.log`). The extra keys are returned by
the same `key=value` map; the semantic mapping is `[INFERENCE]`:

| Key | Guess |
|---|---|
| `ok` | success flag; AJS-07 captured `ok=1` |
| `sid` | session id |
| `sip` | server virtual IP |
| `cip` | client virtual IP |
| `dns` | pushed resolver |
| `ka` / `kat` / `kapf` | keep-alive interval / timeout / flag |
| `mtu` | tunnel MTU |
| `ft` / `fm` | failure type / message |
| `au` | auth user |
| `cuid` / `cseid` | client / device ids |
| `st` | site |
| `cv` | core version |
| `osp` / `osd` / `osv` | platform / device / OS version |
| `dim` | device model |
| `peip` | peer (egress) IP |
| `ce` | client extra |

The VPN control channel uses single-word commands at Linux
`0x28cd4f`/`0x28cdf2`: `con`, `ping`, `pong`, `off`, `sid`, `aur`, `fm`, `ft`,
`au`. They are `CommandCode_Ctrl` payloads; their framing and numeric command
codes are `[UNKNOWN]`.

### Data channel

A new TCP connection is opened to the same `svr:p` for each proxied flow
(`FvConnection.java:159-205`). Two fresh random 16-byte values are generated:

- `hiv`: header IV for this data-channel frame.
- `div`: payload IV for the SOCKS5 request and the relayed bytes.

The frame text is:

```
s=<sid>
n=<div as 32 lowercase hex chars>
k=
_=<random padding>
```

`k` is empty here, and there is **no `l=` field**; AJS-07 confirmed this on the
captured Linux data channel (`captures/fvmock-2.log`). The frame is built with
`hiv`; the following payload is a SOCKS5 CONNECT request encrypted with
`sessionKey` and `div`:

```
05 01 00 03 <host_len:uint8> <host UTF-8> <port:uint16 big-endian>
```

This is version 5, one method (no authentication), command CONNECT (`01`),
address type domain name (`03`). OpenAJS only supports ATYP 3 in this path.
The client writes `frame || AES-CTR(sessionKey, div, socks_request)`.

The server response is read in two exact pieces:

1. Read 4 bytes, decrypt with `AES-CTR(hiv, IV=0)`, take the 16-bit big-endian
   length from bytes 2-3, then read and decrypt exactly that many text bytes.
   The text must contain the substring `code=200`; otherwise OpenAJS closes the
   socket and throws `data channel rejected: <text>`.
2. Read exactly 10 bytes and decrypt them with `AES-CTR(sessionKey, div)` at
   CTR position 0. The first two bytes must be `05 00` (SOCKS5 version 5,
   reply 0 = succeeded); otherwise OpenAJS closes the socket and throws
   `socks connect failed: rep=<n>`.

After that the socket read timeout is set to infinite and the connection is
relayed. The 10-byte SOCKS5 reply is discarded apart from the first two bytes;
OpenAJS does not validate the bound address.

The header IV and payload IV are therefore separate: `hiv` protects only the
frame header/text of the data-channel open, while `div` protects the SOCKS5
request, the 10-byte reply, and every relayed byte. Both are sent to the server
(`hiv` in cleartext as the frame prefix, `div` inside the encrypted frame text).

### Relay

`FvStream` (`api/fv/FvStream.java`) holds two independent AES-CTR cipher
objects, both initialized with `sessionKey` and `div`:

- `send` continues the CTR stream after the SOCKS5 request bytes; data read
  from the local SOCKS client is encrypted with it and written to the FV socket.
- `recv` continues after the 10-byte SOCKS reply; data read from the FV socket
  is decrypted with it and written to the local client.

`SocksProxyServer.relay` uses two threads and 16,384-byte buffers
(`SocksProxyServer.java:116-147`). Closing either side closes the FV stream.

The two directions share the same key/IV pair but consume different portions of
the CTR keystream; the server must mirror that exactly. Because both directions
start at position 0 for the SOCKS request/reply, the first 10 bytes of each
direction reuse the same keystream, another protocol weakness.

### Local SOCKS5 front end

`SocksProxyServer` (`SocksProxyServer.java:48-107`) accepts one client at a
time per connection on `127.0.0.1:<port>`:

- Greeting: version must be 5; method count is skipped; server replies
  `05 00` (no authentication) without checking the offered methods.
- Request: reads version, command, reserved, address type. Supports ATYP 1
  (IPv4), 3 (domain), and 16-byte IPv6 (ATYP 4); converts IPv4/IPv6 to a
  textual host.
- Non-CONNECT commands get `05 07 00 01 00 00 00 00 00 00` (command not
  supported).
- A failed `openStream` gets `05 05 00 01 00 00 00 00 00 00` (connection
  refused).
- Success gets `05 00 00 01 00 00 00 00 00 00` (bound address 0.0.0.0:0).
- The local SOCKS reply is sent only after the FV data channel and the remote
  SOCKS5 reply succeed.

### Shipping-client data-channel handshake

AJS-07 captured the shipping Linux 4.2.3.0 proxy path: it uses the same binary
frame shape described here (16-byte key prefix, CTR length/text, CTR body),
**not** OPV2 and not the HTTP upgrade. The earlier static inference that proxy
mode used `POST /s?s=...` + WebSocket is `[OVERTURNED]`; see
`## Proxy carrier (empirical, AJS-07)`.

The raw-frame path in this section is OpenAJS's implementation. It matches the
captured proxy carrier's field layout, but OpenAJS does not implement the
empirical two-layer auth response or the server half-close.

`proxyHttp` (`FvConnection.java:93-157`) is a debugging variant that opens a
data channel, sends a plain HTTP request after the SOCKS reply, and returns the
first 64 KiB as text. It is not required for a proxy implementation.

## Transport variants

**Correction (AJS-07, 2026-10-23).** The proxy carrier was captured. It is the
binary FV frame, not `OPV2` and not the HTTP upgrade. The earlier call-graph
inference that Linux proxy mode calls `FVProxyRequestHeader::fetch` is
`[OVERTURNED]` for the observed Linux 4.2.3.0 `protocolVersion=2` path. The
captured bytes and the two-layer response are in
`## Proxy carrier (empirical, AJS-07)`.

| Shape | Status | Evidence |
|---|---|---|
| A. Binary FV carrier | **CONFIRMED** for proxy auth/data on Linux 4.2.3.0 `protocolVersion=2` | `research/60-dynamic/captures/fvmock-1.bin`, `fvmock-2.bin` |
| B. `OPV2` builder | Present, but **not** on the captured proxy path; VPN/control role `[UNKNOWN]` | builder at Linux `0x45080b` / Windows `0x551f40`; layout below |
| C. HTTP `POST /s?s=...` upgrade | `[OVERTURNED]` as the proxy carrier; actual role `[UNKNOWN]` | literals in Windows 4.9.6.0 and Linux 4.2.3.0/4.3.0.1 |
| D. OpenAJS `FvConnection` | Executable baseline; same field layout as A, but lacks the empirical response layering and half-close | `FvConnection.java:159-205` |

### Shape A: binary FV carrier (confirmed proxy path)

```
[16-byte K][AES-128-CTR(key=K, IV=0)(4-byte BE text_len || text || body)]
```

- `K` is a cleartext 16-byte per-connection key prefix. It plays the same role
  as OpenAJS's `headerIv`: the prefix is also the CTR key, not merely an IV.
- `text_len` counts the UTF-8 text only; the body follows immediately.
- No `OPV2`, no `keyA^keyB`, and no `buffer_obscure_packet` layer was observed.
- Auth body: `AES-128-CTR(sessionKey, IV=n)`, where `n` is the 32-hex IV in the
  auth text and `sessionKey` is the RSA-OAEP-wrapped 16-byte key.
- Data body: the same construction with a fresh `n`, carrying a raw SOCKS5
  CONNECT.

### Shape B: OPV2 builder (VPN/control, not proxy)

The binary contains an `OPV2` builder at Linux `0x45080b` and Windows
`0x551f40`, with the layout:

```
[keyA^keyB][keyB][crc32]["OPV2"][tag1][len][payload][tag2][len][body][0]
```

AJS-07 did not observe this builder on the proxy auth or data path. The
`0x42cad8` TLV parser (15-byte global prefix + tag `0x01` + map) also belongs to
a different control/VPN path; feeding the captured auth response through it was
rejected. Keep `OPV2` as the VPN/control builder; its exact role elsewhere is
`[UNKNOWN]`.

### Shape C: HTTP upgrade literals

`POST /s?s=%s&n=%s HTTP/1.1`, `Upgrade: websocket`, `Connection: Upgrade`,
`Content-Length`, `Sec-WebSocket-Key`, and `200 OK` exist in all three shipping
clients, but they were **not** on the captured Linux 4.2.3.0 proxy path. Their
actual role (another protocol version, another mode, or dead code) is
`[UNKNOWN]`. Do not implement them as the proxy carrier.

### What is confirmed

- Shape A for the captured Linux proxy path, including `s`=sid, `n`=IV, and
  OAEP-SHA1.
- The RSA modulus is byte-identical in Windows 4.9.6.0, Linux 4.2.3.0, and
  Linux 4.3.0.1; it is 1024-bit by arithmetic.
- The auth response is two layers and the server must half-close after the
  auth map (see the empirical section).
- Linux 4.2.3.0 login sends no `X-Sig`/`X-Timestamp`.

### What remains unknown

`OPV2` semantics and any post-auth/keepalive use; the role of the HTTP upgrade
literals; `buffer_obscure_packet` / `FV_CipherKeySize`; `protocolVersion` /
`implType` selection in other builds; numeric `CommandCode_*` values; and
whether a `sid` may be reused for multiple data connections.

### Consequences for the Go emulator

1. Implement shape A first. It is the captured proxy carrier.
2. Keep the `OPV2` and HTTP-upgrade code paths separate for VPN/control work;
   do not route proxy traffic through them.
3. Reuse the OpenAJS-derived frame/crypto core, but add the empirical two-layer
   auth response and the server half-close.
4. Do not treat `OPV2`, the HTTP literals, `buffer_obscure_packet`, or
   `protocolVersion` as understood for other modes.
5. AJS-07 observed one fresh TCP connection per proxied flow; multiplexing was
   not tested.

## Proxy carrier (empirical, AJS-07)

Source: `research/60-dynamic/REPORT.md` and
`research/60-dynamic/captures/` (Linux `ajiasu` 4.2.3.0, `protocolVersion=2`,
`implType=2`, local mock control plane and mock FV server). No real API or FV
host was contacted.

### Frame

```
[16 bytes K][AES-128-CTR(key=K, IV=0)(4-byte BE text_len || text || body)]
```

Captured auth connection: 679 bytes, `text_len=234`, `body_len=425`
(`captures/fvmock-1.bin` / `.log`). Captured data connection: 207 bytes,
`text_len=169`, body 18 bytes (`captures/fvmock-2.bin` / `.log`). `K` is a
fresh cleartext 16-byte key per connection; there is no `OPV2` magic and no
`keyA^keyB` prefix.

### Auth request

Text:

```
s=
n=<32 lowercase hex>
k=<base64("OAEP" || RSA-OAEP-SHA1(sessionKey))>
l=<body byte length>
_=<random pad>
```

- `s` is empty on the auth request.
- `n` is the 16-byte body IV, lowercase hex (32 characters).
- `k` decodes to 132 bytes: `4f 41 45 50` (`OAEP`) plus a 128-byte RSA
  ciphertext. The modulus is 1024-bit. OAEP-SHA1 decrypts the 16-byte session
  key; OAEP-SHA256 fails (`REPORT.md`, "Decryption").
- `l` is the body length (425 in the capture).
- `_` is random lowercase padding.

Body: `AES-128-CTR(sessionKey, IV=n)` over newline-separated fields. The
captured Linux body is alphabetically ordered:

```
c=au
ce=
cseid=linux
cuid=<20 hex>
cv=20241105-1
dim=<lid=...,...>
osd=x86_64
osp=linux
osv=<kernel string>
p=test
peip=
st=ajs
t=<unix seconds>
u=test
v=4.2.3.0
_=<random pad>
```

The order differs from OpenAJS's insertion order but the body is a map; do not
rely on order. `peip` is empty in this capture.

### Auth response (two layers, then half-close)

1. `code=200` block, one CTR stream, no duplicated length:

   ```
   [4 bytes AES-CTR(K, IV=0)(text_len)]
   [AES-CTR(K, IV=0) from offset 4 over the text]
   ```

   Captured: 34 bytes total; decrypted length `00 00 00 1e` (30) and text
   `code=200\nr=ok\ns=test\nsid=test\n`. The client peeks the first 4 bytes for
   the length, then re-reads `len + 4` bytes from offset 0 and decrypts them in
   one pass. Sending a duplicated length block makes
   `FVProxyRequestHeader::fetch` parse garbage and time out.

2. Auth map, a separate CTR stream:

   ```
   AES-128-CTR(sessionKey, IV=n) over:
   c=aur
   r=ok
   sid=<sid>
   s=<sid>
   ok=1
   sip=<server virtual ip>
   cip=<client virtual ip>
   dns=<resolver>
   ka=<keepalive interval>
   kat=<keepalive timeout>
   kapf=<keepalive flag>
   mtu=<mtu>
   au=<user>
   st=ajs
   cv=20241105-1
   osp=linux
   osd=x86_64
   osv=<os string>
   dim=<device model>
   peip=<peer ip>
   ce=
   ```

   Captured size: 187 bytes (`captures/fvmock-1.log`). The client parses this
   map on `BEV_EVENT_EOF`; the server must `shutdown(SHUT_WR)` after sending it.
   Only then does the client reach `state=200` and listen on `127.0.0.1:1080`.

### Data channel

A new TCP connection to the same FV endpoint is opened per proxied flow.
Text:

```
s=<sid from the auth response>
n=<fresh 16-byte div, 32 hex>
k=
_=<random pad>
```

There is **no `l=` field**. The body follows the text immediately:

```
AES-128-CTR(sessionKey, IV=n) over the raw SOCKS5 CONNECT
```

Captured 18-byte body decrypts to
`05 01 00 03 0b 65 78 61 6d 70 6c 65 2e 63 6f 6d 00 50`, i.e.
SOCKS5 CONNECT `example.com:80`. The client waits for `code=200` and a 10-byte
encrypted SOCKS5 reply before relaying payload.

### Login path

Linux 4.2.3.0 sends no `X-Sig` and no `X-Timestamp` on `/client.php`
(`captures/control.jsonl`). Headers are only `Host`, `Accept`,
`Accept-Encoding`, `Connection`, `Content-Length`, `Content-Type`; the body ends
with a trailing `&` and uses `v=4.2.3.0`. The signing secret is therefore not
exercised by this path and remains `[UNKNOWN]`.

### Captures

| File | Content |
|---|---|
| `captures/control.jsonl` | all `/client.php` requests; no signing headers on Linux 4.2.3.0 |
| `captures/fvmock-1.bin` | raw auth connection (679 B) |
| `captures/fvmock-1.log` | decrypted auth text/body, session key, response bytes |
| `captures/fvmock-2.bin` | raw data-channel connection (207 B) |
| `captures/fvmock-2.log` | `s=test`, `n=<div>`, decrypted SOCKS5 CONNECT |



## Error and edge cases

| Condition | OpenAJS behavior | Protocol meaning |
|---|---|---|
| Auth response `r` missing or not `ok` | `authenticate()` returns `false`; caller throws `authentication rejected by server` (`ProxyService.java:34-38`) | credentials, membership, `cv`, OAEP hash, or clock rejected |
| Wrong OAEP hash | server cannot decrypt `k`; OpenAJS sees a failed auth or a closed connection | `[INFERENCE]`; AJS-07 confirmed OAEP-SHA1 works and OAEP-SHA256 fails on Linux 4.2.3.0 |
| Auth map without server half-close | client waits for EOF before parsing the auth map | server must `shutdown(SHUT_WR)` after the auth map |
| Duplicated length block in auth response | client parses garbage via `FVProxyRequestHeader::fetch` and times out | send one `[4B CTR(K,0)(len)]` block only |
| Auth response shorter than 4 bytes | parser returns empty map; auth returns `false` | truncated or non-FV peer |
| Auth read timeout | `readAvailable` swallows `SocketTimeoutException` and returns partial data; usually `false` | server silent or wrong port |
| Data-channel header lacks `code=200` | `IOException("data channel rejected: ...")` | sid invalid/expired, quota, server refusal |
| Data-channel reply first byte != 5 | `IOException("socks connect failed: rep=...")` | malformed FV/SOCKS bridge |
| Data-channel reply `rep != 0` | same exception | SOCKS5 failure from the egress side |
| Data-channel EOF before 4/`textLen`/10 bytes | `EOFException` from `readN` | connection dropped |
| Data-channel read timeout | `SocketTimeoutException` propagates; no retry | server slow or blocked |
| Response exceeds 65,536 bytes | read loop stops at 64 KiB | implementation cap, not protocol |
| Control HTTP non-2xx | status is recorded; body is still parsed as JSON | API-level error object |
| Control JSON parse failure | `json = null`; callers see defaults/empty | proxy, captive portal, HTML error page |
| TLS certificate/hostname mismatch | accepted (`InsecureTLS`) | MITM possible; no pinning |
| Control host connection error | next host in list is tried | failover only on `IOException` |
| `sid` absent from auth response | stored as `null`; data-channel text becomes `s=null` | server/protocol error |

Linux client error tokens observed near the FV auth code (`auth.password`,
`auth.group`, `auth.forbidden`, `auth.membership`, `auth.limit`, `auth.off`,
`session.timeout`, `client request failed`, `network buffer broken`,
`can not connect to server`) are likely values of `r` or diagnostic messages
`[INFERENCE]`; OpenAJS only compares `r` with `ok` and does not map them.

SOCKS5 reply codes (RFC 1928) that a Go implementation should preserve when
mapping remote failures to the local client:

| Code | Meaning |
|---|---|
| `0x00` | succeeded |
| `0x01` | general SOCKS server failure |
| `0x02` | connection not allowed by ruleset |
| `0x03` | network unreachable |
| `0x04` | host unreachable |
| `0x05` | connection refused |
| `0x06` | TTL expired |
| `0x07` | command not supported |
| `0x08` | address type not supported |

## Python reference implementation

`reference/fv_proto.py` implements, using only the standard library and
`cryptography`:

| Function | Behavior |
|---|---|
| `dual_hash(data)` | exact 8-byte dual hash, including empty and odd-length input |
| `url_encode(value)` | UTF-8 percent-encoding with lowercase hex, unreserved set as above |
| `build_query(params)` | sorted keys, encoded values, `&` join |
| `sign(secret, body, timestamp)` | `secret|body|timestamp|secret` → hex dual hash |
| `build_auth_body(fields)` | `key=value\n` serialization with newline replacement |
| `aes_ctr(key, iv, data)` | AES-CTR, no padding; validates 16-byte IV |
| `rsa_oaep_encrypt(session_key, hash_name="SHA-1")` | 128-byte ciphertext under the embedded 1024-bit key |
| `oaep_block(session_key, hash_name="SHA-1")` | `b"OAEP" + ciphertext` for the `k=` field |
| `build_frame(header_iv, text)` | client-style frame: IV prefix + encrypted 4-byte header + text |
| `parse_response(response, header_iv, session_key, body_iv)` | accepts server form (no prefix) or client form (prefix); returns merged header/body fields |
| `socks5_connect_request(host, port)` | `05 01 00 03 ...` request bytes |

`main()` prints deterministic vectors and performs no network I/O.

`tests/test_vectors.py` is the acceptance harness. It compiles the vendored
`tests/java/us/cubk/openajs/api/crypto/FVSign.java` with JDK 21 and runs
`VectorHarness` to produce independent `dualHash`, `sign`, and `buildQuery`
vectors, then compares them with the Python implementation. It also checks an
AES-CTR NIST SP 800-38A vector, frame round-trip in both prefix forms, the
RSA-OAEP shape and a local-key OAEP round-trip, auth-body serialization, and the
SOCKS5 request bytes. Running
`python research/40-protocol/tests/test_vectors.py` prints `PASS 7` and exits 0
on success. The test is also pytest-compatible.

Deliberately **not** implemented:

- No sockets, TCP connect, TLS, HTTP, DNS, or retries.
- No login, credential storage, token refresh, or server-list fetch.
- No SOCKS5 listener or relay loop.
- No UDP, `lwip`, NAT, DNS proxy, route installation, or TAP driver.
- No random padding generation: the caller supplies the `_` pad.
- No response authentication or signature verification (the protocol's response
  signing is `[UNKNOWN]`).
- No support for the shipping client's HTTP `POST /s?...` or `OPV2` paths.
  The reference implements the captured binary carrier's frame fields
  (AES-CTR, `s/n/k/l/_`) but not sockets, the two-layer auth read loop, or the
  server half-close.

The module is intended as an oracle for the Go implementation, not as a
client. It never contacts `login.ajs-app.com` or any FV endpoint.

## Open questions

1. **OAEP hash.** AJS-07 confirmed OAEP-SHA1 for the Linux 4.2.3.0 proxy path:
   the patched lab key decrypts `k`, while OAEP-SHA256 fails. Whether Windows
   4.9.6.0 or other builds use the same digest is `[UNKNOWN]`; OpenAJS defaults
   to SHA-1/MGF1-SHA-1.
2. **Signing secret in the shipping client.** The OpenAJS constant does not
   appear in `AJiaSu.exe` or `ajiasu` as ASCII or as 20 raw bytes. The 15-char
   literal `06ka4z0dc6ga02c` is present in Windows 4.9.6.0 at `0x31e160` and in
   Linux 4.3.0.1 at `0x293080`, immediately after `X-Sig: ` (`0x293065`) and
   `X-Timestamp: ` (`0x29306d`); it is absent from Linux 4.2.3.0 and from
   `product.dat`. Its role is unproven: there is no proof it is the secret.
   Linux 4.2.3.0 login sends no `X-Sig`/`X-Timestamp`, so the secret is not
   exercised there; Windows remains the only known signing path. `product.dat`
   is high-entropy (7.736 bits/byte) and may hold encrypted configuration.
3. **Dynamic API hosts.** Windows 4.9.6.0 and Linux 4.3.0.1 contain `ApiUrls`,
   `FetchedUrlHosts`, `ApiLoginSync`, and `SyncVersion` strings
   (`research/30-linux-static/REPORT.md`), suggesting the four hosts are a
   built-in fallback list refreshed at runtime. Treat the OpenAJS list as a
   fallback, not a guaranteed current list.
4. **`cv` acceptance.** `cv=20260525-1` is hard-coded in OpenAJS for Android
   4.11.10.0. Whether the server validates it against `_v`/platform, and what
   values the 4.9.6.0 Windows or 4.3.0.1 Linux clients send, is `[UNKNOWN]`.
5. **Transport variant.** AJS-07 captured the Linux 4.2.3.0 proxy carrier as
   `[16B K][CTR(K,0)(len||text||body)]`, with no `OPV2` and no HTTP upgrade.
   The earlier HTTP/`OPV2` proxy inference is `[OVERTURNED]`. Whether any
   post-auth, keepalive, or relayed packet uses `OPV2` was not tested.
6. **`OPV2` framing.** The builder exists at Linux `0x45080b` / Windows
   `0x551f40` with layout
   `[keyA^keyB][keyB][crc32]["OPV2"][tag1][len][payload][tag2][len][body][0]`.
   It is not on the captured proxy path; its VPN/control role and any
   post-auth use are `[UNKNOWN]`.
7. **`buffer_obscure_packet` / `FV_CipherKeySize`.** The error literal
   `buffer_obscure_packet key size must be FV_CipherKeySize` implies an
   additional obfuscation layer in the shipping clients. It was not exercised
   on the captured proxy path; whether it applies to VPN/UDP modes is
   `[UNKNOWN]`.
8. **`protocolVersion` / `implType`.** `prepare core = %s, protocolVersion = %d,
   implType = %d` shows the client selects an FV core at runtime. OpenAJS
   implements one unnamed core. The values and their wire effects are
   `[UNKNOWN]`.
9. **Auth/response field semantics.** AJS-07 confirmed `s`=sid and `n`=IV for
   the Linux proxy path. Linux also exposes the table `ok sid sip cip dns ka
   kat kapf mtu ft fm au cuid cseid st cv osp osd osv dim peip ce`. `sid`,
   `cuid`, `cseid`, `st`, `cv`, `osp`, `osd`, `osv`, `dim`, `ce` are
   corroborated by OpenAJS; `sip`, `cip`, `dns`, `ka`, `kat`, `kapf`, `mtu`,
   `ft`, `fm`, `peip`, `au` are `[INFERENCE]`. Their exact types and
   requiredness are `[UNKNOWN]`.
10. **Keepalive.** `ping`/`pong` are `CommandCode_Ctrl` verbs in VPN mode
    (Linux `0x28cd4f`); whether the proxy path needs them, and whether they use
    `OPV2`, is `[UNKNOWN]`.
11. **`par.fv.u` / `par.fv.ps`.** Returned by the API but unused by OpenAJS.
    Do not invent a role; test them only if a later wave obtains a live server
    list.
12. **Response integrity.** No response signature, MAC, or replay protection is
    implemented or observed. Whether the server signs responses is `[UNKNOWN]`.
13. **`sid` lifetime and concurrency.** OpenAJS opens a new TCP connection per
    flow with the same `sid` and never refreshes it. Expiry, per-account
    concurrency, and kick behavior are `[UNKNOWN]`.
14. **UDP / `lwip`.** The shipping clients expose UDP, `lwip`, NAT, DNS proxy,
    and TAP modes (`FVClientVpn`, `FVClientProxyLwip`, `FVDnsProxy`,
    `FVNetClient` strings). OpenAJS implements only the SOCKS-over-FV TCP path;
    these modes are out of scope for this wave.
15. **TLS and pinning.** OpenAJS disables certificate and hostname checks. The
    shipping clients may pin certificates or public keys; this is `[UNKNOWN]`.
16. **Field semantics.** `_tz` units (minutes), `_vrf` (channel), `_os_dim`
    grammar, `ExpireTime` units, and `LoginToken` structure are inferred from
    code or naming, not from documentation.
17. **Rate limits and anti-abuse.** Unknown; no live requests were made.
18. **Version gating.** The control API may reject or downgrade older `_v`
    values, and FV may reject mismatched `cv`/`v`. The version matrix above is
    the baseline for experiments.

### Legal / scope note

The AJiaSu terms of service (section 7.2, per AJS-02 OSINT) prohibit reverse
engineering. This document is static interoperability research derived from a
public-domain reimplementation and from strings in binaries already extracted
by the Owner; it makes no live API calls and provides no credential or bypass.
Any dynamic validation in later waves is an Owner decision.

