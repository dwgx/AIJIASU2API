# OSINT: AJiaSu reverse engineering

Scope: public-source intelligence on the AJiaSu / 爱加速 client and its FV protocol, collected 2026-10-23 from GitHub (10 cloned repositories, API metadata, code search), the official site `91ajs.com`, Google Play, a third-party proxy rule-set, and the Windows payload already extracted into `C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023`. No account was created, no login was attempted, no installer was executed. Claims carry `[S#]` ids resolved in `## Sources`; the graded ledger is `claims.tsv` (C1–C36), the raw index is `sources.tsv` (S1–S36).

## Answer

1. Exactly one public project reimplements the tunnel instead of driving the vendor binary: `cubk1/OpenAJS` (Java, created 2026-06-07), covering control API + FV handshake + local SOCKS5 `[S1][S2][S4][S9]`.
2. Control plane: `POST /client.php`, form body of sorted `k=v`, headers `X-Timestamp`/`X-Sig`, signature = `hex(dualHash(secret|body|timestamp|secret))` `[S2][S3]`.
3. FV transport: per-node `svr:par.fv.p` endpoint; frame = 16-byte IV ‖ AES-CTR(hdrIV,0) over a 4-byte header + text `s/n/k/l/_`; `k` = `"OAEP"` ‖ RSA-OAEP(sessionKey, SHA-1 default); body = AES-CTR(sessionKey, bodyIV) of newline `k=v` auth fields; reply `r=ok`,`sid` `[S4][S5][S7][S8]`.
4. Data plane: one TCP connection per proxied flow, same sessionKey with a fresh IV, payload starts with a raw SOCKS5 CONNECT (`ATYP=3` domain); server answers `code=200` then a 10-byte SOCKS5 reply `[S8]`.
5. Windows ships a TAP-Windows `tap0901` driver (9.24.6.601) plus per-arch `sysproxy.exe` `[S33][S29]`; that the TAP adapter carries the system-wide UDP/TCP modes is `[INFERENCE]`, and no public implementation of the path exists.
6. Everything except OpenAJS drives the official Linux CLI (`login`/`list`/`connect`, `/etc/ajiasu.conf`, proxy mode on `127.0.0.1:1080`) `[S11][S13][S28]`.
7. Access is paid for most nodes; concurrency is a purchased 1–2000 quantity; the ToS forbids resale/sharing of the connection and reverse engineering `[S27][S28][S30]`.
8. Publicly unsolved: UDP mode, lwip mode, AutoMatch semantics, node health, driver IOCTLs, protocol-version negotiation, certificate pinning `[S13][S21][C31][C32]`.

## Findings

### Q1 — Public reverse-engineering work, per repository

All ten repositories below were cloned at their current head on 2026-10-23; dates are the last commit date returned by the GitHub API.

| Repo | Head commit | Stars | What it actually implements | Not implemented |
|---|---|---|---|---|
| `cubk1/OpenAJS` | 2026-06-07 | 8 | Full FV client in Java: register/login/servers/notifications, FV auth, per-flow data channel, local SOCKS5 server, Swing UI `[S1][S2][S4][S9]` | UDP, lwip, TAP, TUN; no pinning (trust-all TLS) `[S10]` |
| `s1g0day/Aijiasu_Agent_Pool` | 2024-04-13 | 12 | Python wrapper: `subprocess` drives `ajiasu connect <node>` with `proxy\n` on stdin, rotates nodes, checks egress via `ip.sb` `[S11][S12]` | No protocol work; one process per node |
| `hanx11192-ship-it/LoveToSpeedup` | 2026-10-05 | 0 | Docker HTTP gateway: Flask panel + REST, multi-account port slots, TCP forwarder, per-slot `cache_dir`/`--sys-env-id`, port auto-discovery `[S14][S15]` | HTTP only (SOCKS5 reported broken), no protocol work |
| `dnomd343/ajiasu-docker` | 2025-08-10 | 5 | Dockerfile that bakes the official Linux binary; documents the full `ajiasu.conf` key set `[S13]` | No protocol work |
| `znicelya/ajiasu-proxy` | 2026-07-20 | 0 | Multi-tenant control plane + gateway + agent + per-connection Runner containers around the unmodified CLI; compliance gate `[S19][S20]` | No protocol work; no real-account CI without written approval |
| `LordVibeCoding/ajiasu-proxy-api` | 2026-03-05 | 0 | Go REST API + `socat` republishing `127.0.0.1:1080`; parses `ajiasu list` output `[S22]` | No protocol work |
| `RorschachE0/AjiPilot` | 2026-03-18 | 0 | Single-file Flask panel; process reaper, lwip-first protocol choice, auto-reconnect, stdin protocol selection `[S21]` | No protocol work |
| `PainterXIII/YouRan_Proxy` | 2024-10-02 | 0 | Shell installer bundling the Linux binary + `ajiasu.conf` + login-sync check `[S23]` | No protocol work; leaks a plaintext account `[C34]` |
| `axe-browser/aijiasu-docker` | 2026-09-30 | 0 | Go manager + Docker SOCKS5 bridge (1080/18888/28888 auto-detect), JSON API, `aijiasu-stdio-v1` plugin protocol `[S17][S18]` | No protocol work |
| `yuwanx/aijiasu-ip-list` | 2026-07-26 | 0 | 2718 node IPv4s extracted from Android 4.12.0.3, 31 provinces `[S24]` | Data only |

Corroborating negative results: GitHub code search for `login.ajs-app.com`, `ClientApiLogin`, `FVClientProxy`, `FVCORE`, `ajiasu.conf` and `tap0901` returns OpenAJS for the first two and no other AJiaSu protocol code; Gitee's API returns `[]` for `ajiasu` and `爱加速` `[S34]`; 52pojie has only a membership request thread `[S35]`; no linux.do / v2ex / Zhihu protocol write-up was found `[C29]`.

### Q2 — Protocol stack, layer by layer

**2.1 HTTP control API.** One endpoint path, `/client.php`, on four interchangeable hosts `[S2]`. Request: `POST`, `Content-Type: application/x-www-form-urlencoded`, `Accept-Encoding: gzip`, `User-Agent: okhttp/4.9.3`, `X-Timestamp: <epoch millis>`, `X-Sig: <16 hex chars>` `[S2]`. Body: all common device params plus `cmd=<Controller/Action>` plus action params, serialized as `TreeMap` (lexicographic) `k=v&…` with strict RFC-3986 percent-encoding `[S2][S3]`. Signature: `dualHash(secret + "|" + body + "|" + timestamp + "|" + secret)` where `dualHash` runs two independent 32-bit hashes over the UTF-8 bytes — `h1 = c + 65599*h1` (djb2 family) and `h2 ^= c + 32*h2 + (h2>>2)` — seeded `0xCAFEBABE`/`0xDEADBEEF`, then emits 8 bytes interleaved `h2,h1` per 16-bit group, hex-encoded `[S3]`. The secret is a hard-coded 32-byte hex string in `AjsClient.java` (not reproduced here) `[S2]`. I reimplemented the hash in Python and confirmed it returns exactly 8 bytes with the documented interleave `[C3]`.

**2.2 Server list and per-node parameters.** `ClientApiLogin/Login` with `trigger=login` returns `Login` (LoginToken, UserName, ConnectPassword) and, when re-called with `_client_login`, also `Servers` `[S2]`. Each `VpnServer` carries `id`, `vsid`, `ctry`, `svr` (host), `tit`, `cmt`, `sts`, `ord`, `cat[]`, `par.fv{u,p,ps}` and `loc[]{c,n,o}`; `port()` reads `par.fv.p` and `secret()` reads `par.fv.ps` `[S2]`. OpenAJS never sends `par.fv.ps`/`u` — it authenticates the FV session with account `UserName` + `ConnectPassword` instead, so the role of the per-node `ps`/`u` is `[UNKNOWN]` (open question Q6.7).

**2.3 FV transport framing.** Client picks three random 16-byte values: `sessionKey`, `bodyIv`, `headerIv`. Frame = `headerIv` (16 bytes, cleartext) ‖ `AES-CTR(headerIv, IV=0)(plain)` where `plain` = 2 zero bytes ‖ 2-byte big-endian text length ‖ UTF-8 text `[S4]`. Auth text: `s=` (empty), `n=<hex bodyIv>`, `k=<base64("OAEP"‖RSA-OAEP(sessionKey))>`, `l=<body byte length>`, `_=<random 0–199 lowercase letters>` `[S4]`. The OAEP block is literally prefixed `O`,`A`,`E`,`P` before base64 `[S4]`; RSA is 1024-bit (309-digit modulus, 128-byte ciphertext, so the `k=` block is 4+128=132 bytes before base64), `e=65537`, MGF1, default hash SHA-1 (switchable via `-Dfv.oaep`) `[S5]`. Body: `AES-CTR(sessionKey, bodyIv)` over `key=value\n` lines, newlines in values replaced by spaces `[S4][S7]`. Auth fields: `t` (epoch s), `c=au`, `u`, `p` (ConnectPassword), `cuid`, `cseid` (both = device id), `st=ajs`, `v` (client version), `cv=20260525-1`, `osp`, `osd`, `osv`, `dim`, `ce=""` `[S7]`. Response: same frame layout, header decrypted with `headerIv`, body with `sessionKey`/`bodyIv`, parsed as `k=v` lines; success is `r=ok` plus `sid` `[S8]`.

**2.4 Data channel.** A new TCP connection per proxied flow: frame with `s=<sid>`, `n=<hex newIV>`, empty `k=`, random `_=`; payload is `AES-CTR(sessionKey, newIV)` over a raw SOCKS5 request `05 01 00 03 <len> <domain> <port_hi> <port_lo>` `[S8]`. Server replies with the same header format (text must contain `code=200`) followed by a 10-byte SOCKS5 reply whose `REP` must be 0; after that both directions are ciphertext on the same CTR stream `[S8]`. Note the SOCKS5 request is inside the encrypted payload, so the FV server itself acts as the SOCKS5 endpoint.

**2.5 Local SOCKS5 server (OpenAJS).** Binds `127.0.0.1:<port>`, no-auth method, CONNECT only (anything else → `05 07 …`), domain/IPv4/IPv6 ATYP accepted, failure mapped to `REP=0x05`, then bidirectional relay `[S9]`.

**2.6 Windows TAP path.** The 4.9.6.0 payload contains `tap/tap/{amd64,arm64,i386}` with TAP-Windows `tap0901.sys/.cat`, `OemVista.inf` (DriverVer `04/21/2016,9.00.00.21`), `tap/tap6/…` (DriverVer `10/08/2020,9.24.6.601`), `tapinstall.exe`, and `sysproxy/{amd64,arm64,i386}/sysproxy.exe` `[S33]`. The vendor changelog records "使用最新版本的 TAP 虚拟网卡驱动" in 4.9.0.5 and UDP-mode fixes on Windows 11 24H2 in 4.9.4.0 `[S29]`. No public code opens the adapter or issues IOCTLs, so the TAP data format is `[UNKNOWN]` `[C32]`.

### Q3 — Version and platform coverage, and documented drift

Official matrix on 2026-10-23: Windows 4.9.6.0 (5.25 MB), macOS 4.9.6.0 (15.8 MB), Android 4.12.2.0 (Android 6.0+, 18.36 MB), iOS 4.11.5.11 (iOS 15+), HarmonyOS 1.0.1.0, Linux 4.3.0.1 in five architectures (x86, amd64, aarch64, armel, armeb), Apple TV 1.0.2.3 `[S26]`. Android package `com.fvcorp.android.aijiasuclient`, developer YOUDUN, 100K+ installs, updated 2026-09-03, mainland-China phone numbers only `[S31]`. Operator: 上海游盾网络科技有限公司 `[S36]`.

Ecosystem pinning is older and split:
- 4.1.1.0: s1g0day's download URL and dnomd343's captured client banner `ajiasu 4.1.1.0 (core:20210315-1730)` `[S11][S13]`.
- 4.2.3.0: vendored by LoveToSpeedup, downloaded by axe-browser's Dockerfile, and the Linux client extracted in this project — the tarball member and our binary share sha256 `29e18740af8b69c6fa4f7b0ef269316b01e0400d7768583fc08044632be13b9f` (3,578,792 bytes) `[S16][S18][C27]`.
- 4.3.0.1: current official Linux build `[S26][S28]`.
- OpenAJS emulates Android 4.11.10.0 with `cv=20260525-1`, i.e. a mobile profile two minor lines ahead of the current desktop build `[S6][S7]`.

Documented drift that matters for a reimplementation: 4.0.0.17 (2020-09-03) added global-proxy HTTP/Socks5 and renamed the old Socks5 mode to 手动代理 `[S29]`; 3.1.2.1 (2020-06-19) "优化客户端与节点服务器的通讯验证逻辑" — a server-side handshake change `[S29]`; 4.9.0.5 (2023-11-21) added `account#N` login and the new TAP driver `[S29]`; 4.9.6.0 (2025-07-11) added dynamic delivery of API domains, which is the plausible reason four login hosts exist and can rotate `[S29][S2]`. The Linux CLI protocol set is `udp|tcp|lwip|proxy` in the official help `[S28]` and `udp|tcp|lwip|socks5|http|proxy` in dnomd343's config reference `[S13]`.

### Q4 — API hosts and endpoints observed in public code

Hosts (in OpenAJS fallback order): `https://login.ajs-app.com`, `https://login.mmegg.com`, `https://login.mmegg.com:4443`, `https://login.mbclubsh.com`, all serving `/client.php` `[S2]`. `ajs-app.com` is independently listed as an AJiaSu domain next to `91ajs.com` in a third-party proxy rule-set `[S32]`.

Endpoints (all `cmd=` values, `[S2]`):

| cmd | Purpose | Auth |
|---|---|---|
| `ClientApiLogin/Login` | login (`trigger=login`) / token re-auth returning `Servers` + `ConnectPassword` | none / `_client_login` |
| `ClientApiRegister/Register` | register with `AccountName`, `Password`, `Mode`, `VerifyCode`, `InvitationCode` | none |
| `ClientApiVerifyCode/Captcha` | captcha (`Usage`, `Mode`) | none |
| `ClientApiVerifyCode/SendVerifyCode` | SMS/email code (`AccountName`, `Mode`, `Usage`, `CaptchaText`) | none |
| `ClientApiUser/ResetPassword` | password reset | none |
| `ClientApiUser/UserVpnServerFavoriteList` | favourites | token |
| `ClientApiUser/VpnServerFavorite` | favourite/unfavourite (`Action`, `ServerId`) | token |
| `ClientApiVpnServer/AutoMatch` | auto node pick (`LocationLevel`, `LocationCode`) | token |
| `ClientApiMessageCenter/GetNotificationList` | notifications (`pageSize`, `lastNotificationId`) | token |
| `ClientApiMessageCenter/GetUnreadNotificationCount` | unread count | token |
| `ClientApiMessageCenter/GetAnnounce` / `CloseAnnounce` | announcements | token |
| `ClientApiClientSetting/ClientSetting` | push settings (`ClientSettings[k]`) | token |
| `ClientApiUpgrade/UpgradeRecord` | upgrade telemetry (`UpgradeType`, `PriorVersion`, `UpgradeDatetime`, `PopupInterval`) | none |

Common params on every call: `_site=ajs`, `_v`, `_m`, `_os_ver`, `_os_dev`, `_os_hw`, `_os_did`, `_os_lang`, `_tz`, `_os_dim`, `_inst_mkt`, `_lang`, `_vrf`, `_uid` (timestamp+random+4-hex checksum) `[S2][S6]`. Token calls add `_client_login` and `username` `[S2]`.

### Q5 — Account and entitlement requirements, and ToS constraints

- Paid membership is required for the good nodes; free tier is a 3-day trial on test servers then a limited daily allowance `[S28][S30]`. Google Play copy advertises 2700+ servers / 206 cities, 10–30 Mbps per device `[S31]`; the site claims 31 provinces, 190+ cities, 2600+ servers `[S25][S28]`.
- Concurrency is a purchased integer, 1–2000 simultaneous connected devices, and price = plan price × concurrency; exceeding it silently kicks an older device `[S27][S28][S30]`. This is the single most important commercial fact for a proxy API: parallelism is bought, not forbidden.
- Node groups exist: an observed client transcript returns `服务器不可用于用户组` ("server not available for the user group") for nodes outside the account's entitlement `[S11][C24]`. The exact grouping rule is `[UNKNOWN]`.
- ToS (version 2025-10-22) restricts exactly what a proxy API does: 4.3 forbids providing/sharing/transferring traffic or connection rights to others; 4.4(4) forbids "为第三方提供未经授权的代理服务"; 4.1 lists 极速(UDP)/兼容(TCP)/代理 modes; 7.2 forbids reverse engineering, decompilation and disassembly; 7.4 forbids defeating security measures `[S27]`. 1.1/§14 state mainland-China-only lines and no cross-border use `[S25][S27]`.
- Consequence for this project: a multi-user egress-IP API built on one account is a direct ToS violation unless the operator has written permission; a single-tenant, self-use tool is the defensible shape. znicelya reached the same conclusion and gates real-account use behind a written approval record `[S20][C33]`.

### Q6 — What remains unsolved publicly

1. **UDP mode (极速模式)** — vendor-only; no public framing `[S25][S28][C32]`.
2. **lwip mode** — used by dnomd343 and AjiPilot configs `[S13][S21]`, never described.
3. **TAP / TUN data path and driver IOCTLs** — the driver ships (`tap0901.sys`) but nothing public writes to it `[S33]`.
4. **AutoMatch semantics** — endpoint and parameter names known, selection algorithm unknown `[S2][C31]`.
5. **Node health / `sts` semantics** — `ok` vs other values observable in listings `[S13][S22]`, meaning of the rest unknown.
6. **Protocol-version negotiation** — `cv=20260525-1` and `v` are sent; whether the server rejects old `cv` values is untested `[S7]`.
7. **`par.fv.ps` / `par.fv.u`** — per-node secret and uid returned by the API but unused by OpenAJS `[S2]`.
8. **Certificate pinning** — OpenAJS disables verification entirely `[S10]`; the official clients' pinning behaviour is `[UNKNOWN]`.
9. **Control-channel keepalive** — OpenAJS has none; whether the server times out idle `sid`s is `[UNKNOWN]` `[S8]`.

### Source hygiene

Several domains impersonate the product (`aijiasux.com`, `aijiasu.com.cn`, `aijiasu.org.cn`, `aijiala.com.cn`, `lvzhou*` hosts) and mix AJiaSu branding with unrelated "global node" marketing; only `91ajs.com` (ICP 沪ICP备17008479号-19) is the operator's site `[S25][S36]`. `PainterXIII/YouRan_Proxy` publishes a working account/password pair in `ajiasu/ajiasu.conf` and a hard-coded password in `ajiasu/install.sh`; the values are redacted here and must never be reused `[S23][C34]`. GitHub code search for `ClientApiLogin` also surfaces an OpenBullet config dump (`WangLee112/Config-Open-Bullet`, 2022); it was not opened beyond the filename and is out of scope.

## Contradictions

1. **SOCKS5 usability.** LoveToSpeedup states the 4.2.3.0 SOCKS5 handshake is unusable and only HTTP proxy works `[S14]`, while s1g0day used `socks5h://` against 4.1.1.0 `[S11]` and axe-browser bridges a SOCKS5 port for 4.2.3.0 `[S17]`. Kept as C15, grade B: the data path is version- and build-dependent, and a SOCKS5-based design must be validated per build.
2. **"One connection per account".** LoveToSpeedup enforces account-level mutual exclusion `[S14]`, but the vendor sells 1–2000 simultaneous connections per account `[S27][S28][S30]`. The vendor statement wins: the constraint is per client process, not per account (C16 rewritten accordingly).
3. **Linux version in the wild.** Official current is 4.3.0.1 `[S26]`, but every wrapper in the ecosystem pins 4.2.3.0 or 4.1.1.0 `[S11][S13][S16][S18]`. Any behaviour recorded against 4.2.3.0 may not hold on 4.3.0.1.
4. **Host stability.** OpenAJS hard-codes four login hosts `[S2]`, yet 4.9.6.0 added dynamic API-domain delivery `[S29]`. The list is observed-in-code, not guaranteed-current.
5. **Node counts.** 2600+ (help) `[S28]`, 2700+ (Play) `[S31]`, 190+ vs 200+ vs 206 cities `[S25][S28][S31]`. Marketing numbers; do not use any of them as a denominator.

## Open questions

- Does 4.3.0.1 still accept the OpenAJS auth field set (`cv=20260525-1`), or does the server enforce a newer `cv`? `[UNKNOWN]`
- What do `par.fv.ps` and `par.fv.u` authenticate, and does the official client use them instead of `ConnectPassword`? `[UNKNOWN]`
- Is the `sid` bound to one client IP / one data connection at a time, and does it expire? `[UNKNOWN]`
- What is the exact `LocationLevel`/`LocationCode` vocabulary for AutoMatch, and what does it return? `[UNKNOWN]`
- Which node groups exist, and how is group membership derived from a plan? `[UNKNOWN]`
- Do the official clients pin certificates, and to which CA/host? `[UNKNOWN]`
- What does UDP/lwip framing look like, and is it reachable over the same `svr:port`? `[UNKNOWN]`
- Is there any documented rate limit or abuse detection on `/client.php`? `[UNKNOWN]`

## Sources

| id | source | date |
|---|---|---|
| S1 | https://github.com/cubk1/OpenAJS | 2026-06-07 |
| S2 | https://github.com/cubk1/OpenAJS/blob/master/src/main/java/us/cubk/openajs/api/AjsClient.java | 2026-06-07 |
| S3 | https://github.com/cubk1/OpenAJS/blob/master/src/main/java/us/cubk/openajs/api/crypto/FVSign.java | 2026-06-07 |
| S4 | https://github.com/cubk1/OpenAJS/blob/master/src/main/java/us/cubk/openajs/api/fv/FvConnection.java | 2026-06-07 |
| S5 | https://github.com/cubk1/OpenAJS/blob/master/src/main/java/us/cubk/openajs/api/fv/FvCrypto.java | 2026-06-07 |
| S6 | https://github.com/cubk1/OpenAJS/blob/master/src/main/java/us/cubk/openajs/api/AjsDevice.java | 2026-06-07 |
| S7 | https://github.com/cubk1/OpenAJS/blob/master/src/main/java/us/cubk/openajs/api/fv/FvConnection.java (authFields) | 2026-06-07 |
| S8 | https://github.com/cubk1/OpenAJS/blob/master/src/main/java/us/cubk/openajs/api/fv/FvConnection.java (openStream) | 2026-06-07 |
| S9 | https://github.com/cubk1/OpenAJS/blob/master/src/main/java/us/cubk/openajs/api/fv/SocksProxyServer.java | 2026-06-07 |
| S10 | https://github.com/cubk1/OpenAJS/blob/master/src/main/java/us/cubk/openajs/api/net/InsecureTLS.java | 2026-06-07 |
| S11 | https://github.com/s1g0day/Aijiasu_Agent_Pool/blob/main/readme.md | 2024-04-13 |
| S12 | https://github.com/s1g0day/Aijiasu_Agent_Pool/blob/main/dev/another_script.py | 2024-04-13 |
| S13 | https://github.com/dnomd343/ajiasu-docker/blob/main/README.md | 2025-08-10 |
| S14 | https://github.com/hanx11192-ship-it/LoveToSpeedup/blob/main/README.md | 2026-10-05 |
| S15 | https://github.com/hanx11192-ship-it/LoveToSpeedup/blob/main/LoveToSpeedup/app/ajiasu_manager.py | 2026-10-05 |
| S16 | https://github.com/hanx11192-ship-it/LoveToSpeedup/blob/main/LoveToSpeedup/vendor/ajiasu-amd64-4.2.3.0.tar.gz | 2026-10-05 |
| S17 | https://github.com/axe-browser/aijiasu-docker/blob/main/README.md | 2026-09-30 |
| S18 | https://github.com/axe-browser/aijiasu-docker/blob/main/Dockerfile | 2026-09-30 |
| S19 | https://github.com/znicelya/ajiasu-proxy/blob/main/README.md | 2026-07-20 |
| S20 | https://github.com/znicelya/ajiasu-proxy/blob/main/docs/compliance/ajiasu-usage-gate.md | 2026-07-20 |
| S21 | https://github.com/RorschachE0/AjiPilot/blob/main/README.md | 2026-03-18 |
| S22 | https://github.com/LordVibeCoding/ajiasu-proxy-api/blob/main/README.md | 2026-03-05 |
| S23 | https://github.com/PainterXIII/YouRan_Proxy/blob/master/ajiasu/ajiasu.sh | 2024-10-02 |
| S24 | https://github.com/yuwanx/aijiasu-ip-list/blob/main/README.md | 2026-07-26 |
| S25 | https://www.91ajs.com/ | 2026-10-23 |
| S26 | https://www.91ajs.com/download | 2026-10-23 |
| S27 | https://www.91ajs.com/terms | 2025-10-22 |
| S28 | https://www.91ajs.com/help | 2026-10-23 |
| S29 | https://www.91ajs.com/app-changelog-desktop.html | 2025-07-11 |
| S30 | https://www.91ajs.com/information/Announcement.html | 2020-01-31 |
| S31 | https://play.google.com/store/apps/details?id=com.fvcorp.android.aijiasuclient | 2026-09-03 |
| S32 | https://raw.githubusercontent.com/xOS/Config/master/RuleSet/Domestic.list | 2026-10-23 |
| S33 | C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023/tap/tap6/amd64/OemVista.inf (+ tap/tap/amd64/OemVista.inf, sysproxy/) | 2026-10-23 |
| S34 | https://gitee.com/api/v5/search/repositories?q=ajiasu | 2026-10-23 |
| S35 | https://www.52pojie.cn/thread-1827832-1-1.html | 2023-08-31 |
| S36 | https://www.91ajs.com/about | 2026-10-23 |

## Falsification pass (self-check)

| # | Strongest claim tested | Attack | Verdict |
|---|---|---|---|
| 1 | C1 — OpenAJS is the only full FV reimplementation | `gh search code` for `login.ajs-app.com`, `ClientApiLogin`, `FVClientProxy`, `FVCORE`, `ajiasu.conf`, `tap0901`, `vvn-`; `gh search repos` for `ajiasu`, `aijiasu`, `91ajs`, `openajs` | **CONFIRMED** (scope: public GitHub code search + 10 clones; a private repo would not appear) |
| 2 | C3 — signature algorithm | Reimplemented `dualHash` in Python from the Java source; checked length (8 bytes), interleave order, and that the trailing byte equals the djb2 accumulator low byte | **CONFIRMED** as a faithful reading of `FVSign.java`; no server-side test vector exists publicly, so correctness against the live server stays `[UNVERIFIED]` |
| 3 | C16 — "one active connection per account" | Vendor help/pricing: concurrency is purchased 1–2000 per account | **OVERTURNED** → rewritten as a per-process constraint (C16), account concurrency moved to C21 |
| 4 | C15 — SOCKS5 broken in 4.2.3.0 | Cross-checked axe-browser (bridges SOCKS5 for 4.2.3.0) and s1g0day (socks5h on 4.1.1.0) | **WEAKENED** — kept as a version/build-dependent contradiction, not a rule |
| 5 | C4 — the four API hosts are current | 4.9.6.0 changelog announces dynamic API-domain delivery; rule-set lists only `ajs-app.com`/`91ajs.com` | **WEAKENED** — hosts are what OpenAJS uses (2026-06-07), not a guaranteed current list; downgraded in Q4 wording |
| 6 | C27 — vendored binary == our binary | `sha256sum` on the tarball member and on `ajs-linux-20261023/ajiasu` | **CONFIRMED** (identical 29e18740…) |

## Verification

DONE WHEN: `python D:/Project/LovePlusSpeedToProxy/research/10-osint/validate.py`, run once after the files were written:

```
OK sources=36 claims=36
```

Not independently verified in this session: every protocol byte-layout statement (C6–C9) is read from OpenAJS source, never exercised against a live server, because no account may be used. Those rows are grade A as *source readings* and `[UNVERIFIED]` as *protocol truth*. Independent acceptance is assigned to another session per the brief; this report does not self-accept.
