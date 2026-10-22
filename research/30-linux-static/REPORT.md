# Linux client 4.2.3.0 — static analysis

Scope: static analysis of the AJiaSu Linux client `4.2.3.0` (core `20241105-1`), plus a string-level
comparison against the Windows `4.9.6.0` payload and the OpenAJS Java reimplementation.
No login, no connect, no installer execution, no package installs. All commands below were run on
2026-10-23 on this workstation; only `./ajiasu -h` / `./ajiasu` (usage print) were executed, inside
WSL Ubuntu-24.04, from `/tmp`, as a non-root user. This is static interoperability research only:
the vendor ToS forbids reverse engineering, so no live service was contacted, no account was used
and no traffic was generated against AJiaSu infrastructure.

## Answer

- The Linux client is a **stripped, static, non-PIE ELF x86-64** binary, 3,578,792 bytes,
  `sha256=29e18740af8b69c6fa4f7b0ef269316b01e0400d7768583fc08044632be13b9f`, built with
  `GCC: (GNU) 9.4.0`, version `4.2.3.0`, core build `20241105-1` (printed by `./ajiasu -h`).
- It is **not** a thin wrapper: it statically embeds the FV client core (`/work/fvclient/src/fvcore`),
  libevent, mbedTLS (RSA/AES/TLS 1.2+1.3), libcurl, jsoncpp and lwIP. 38 distinct source paths from
  the build tree are recoverable from assertion strings (`/work/fvclient/src/...`).
- **Two planes.** Control plane = HTTPS to four login hosts, path `/client.php`, actions
  `ClientApiLogin/Login`, `ClientApiLoginSync`, `ClientApiClientSetting/LoadRoute`,
  `ClientApiReport/WebsiteCheckReport`, signed with `X-Sig` + `X-Timestamp`. Data plane = the FV
  protocol: RSA-1024-OAEP-wrapped AES key, AES-CTR body, newline-delimited frame fields
  `s= / n= / k= / l= / _=`, then either `CommandCode_Ctrl` + `CommandCode_NetPkt` over raw TCP
  (VPN modes) or an HTTP/WebSocket-shaped request `POST /s?s=<sid>&n=<nonce> HTTP/1.1` followed by
  `code=200` and an AES-CTR-encrypted SOCKS5 CONNECT (proxy modes).
- **Proxy mode is userland and root-free**: `protocol=proxy|socks5|http` starts a local SOCKS5/HTTP
  proxy on `127.0.0.1` and needs no `/dev/net/tun`, no routes, no root. The listen port is chosen
  from a **fixed candidate list `{1080, 18888, 28888, 38888}`** (first that binds), not an
  incrementing counter.
- **A newer official Linux build exists**: `4.3.0.1` (core `20260525-1`), published on
  `www.91ajs.com`. It is the version OpenAJS targets (`cv=20260525-1`) and it adds a configurable
  `proxy-local-server = [user:pass@]ip:port` (default `127.0.0.1:1080`) plus `--option=value` CLI
  arguments — directly relevant to building the proxy API.

## Artifacts

| artifact | size | sha256 | origin |
|---|---|---|---|
| `ajiasu` (4.2.3.0, analysed) | 3,578,792 | `29e18740af8b69c6fa4f7b0ef269316b01e0400d7768583fc08044632be13b9f` | extracted from `ajiasu-4.2.3.0.tar.gz` |
| `ajiasu-4.2.3.0.tar.gz` | 1,253,814 | `c3d93551a9632c2e28f7a86e541363f3bbe81f01bae12358714ae96287304232` | third-party mirror `hanx11192-ship-it/LoveToSpeedup` `vendor/`; archive contains only `./ajiasu` |
| `ajiasu-amd64-4.3.0.1.tar.gz` | 1,264,209 | `0affe73aff4b7ee11cc6fab5c060d71dc3056a1c2903ca3435f4fe009d2689e7` | official `https://www.91ajs.com/files/downloads/linux/ajiasu-amd64-4.3.0.1.tar.gz` |
| `ajiasu` (4.3.0.1) | 3,607,464 | `08fcd8668f7aaf6660277778e67fd2afd622c3f1c8fb634e57f31605bf01c8f6` | extracted from the official tarball; archive contains only `./ajiasu` |
| `AJiaSu.exe` (4.9.6.0) | 4,064,640 | `0fd9d702bf9932836ad0ba3e4acf43bb1a587c45ee6247b7a5d88aede78101f6` | read-only comparison input (NSIS payload of `research/AJiaSu_4.9.6.0.exe`) |

Machine-readable manifest: `hashes.tsv`. Local working copies live in
`C:/Users/dwgx1/AppData/Local/Temp/ajs-linux-20261023/`.

### Download-URL probing (2026-10-23)

The brief's candidate pattern `.../linux/ajiasu-x86_64-<version>.tar.gz` is **wrong**; all five
candidates return HTTP 404:

```
4.1.1.0    -> http=404  (HEAD https://www.91ajs.com/files/downloads/linux/ajiasu-x86_64-4.1.1.0.tar.gz)
4.2.3.0    -> http=404
4.9.6.0    -> http=404
4.10.0.0   -> http=404
4.11.10.0  -> http=404
control: HEAD https://www.91ajs.com/ -> http=200
```

The real official naming is `<arch>-<version>`, and the download page
(`https://www.91ajs.com/download`) advertises version **4.3.0.1** for five architectures:

```
/files/downloads/linux/ajiasu-amd64-4.3.0.1.tar.gz    -> http=200
/files/downloads/linux/ajiasu-aarch64-4.3.0.1.tar.gz  -> http=200
/files/downloads/linux/ajiasu-armeb-4.3.0.1.tar.gz    -> http=200
/files/downloads/linux/ajiasu-armel-4.3.0.1.tar.gz    -> http=200
/files/downloads/linux/ajiasu-x86-4.3.0.1.tar.gz      -> http=200
```

Only the `amd64` tarball was downloaded. The same page offers `AJiaSu_4.9.6.0.exe`,
`AJiaSu_4.9.6.0.dmg`, `AJiaSu_4.12.2.0_ajs-official-website.apk` — i.e. Linux is two minor
generations behind the Windows/macOS build, and four behind the Android build.

### Binary shape (4.2.3.0)

```
$ file ajiasu
ajiasu: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, stripped

$ rabin2 -I ajiasu        # radare2 6.2.2, D:/Software/Tool/WorkStation/debugger/Static-Reversing/radare2/radare2-6.2.2-w64/bin/rabin2.exe
baddr 0x400000  bintype elf  bits 64  class ELF64  compiler GCC: (GNU) 9.4.0
static true  stripped true  pic false  nx true  relro partial  canary false
```

Section table (pyelftools 0.33):

| section | vaddr | size |
|---|---|---|
| `.text` | `0x400160` | 2,660,209 |
| `.rodata` | `0x6898e0` | 350,608 |
| `.eh_frame` | `0x6df270` | 490,776 |
| `.gcc_except_table` | `0x756f88` | 39,397 |
| `.data.rel.ro` | `0x9618e0` | 26,368 |
| `.data` | `0x968020` | 5,800 |
| `.bss` | `0x9696e0` | 36,864 |

No `.dynsym`, `.dynstr` or `PT_INTERP`: nothing is dynamically linked, so all network/TLS/crypto
behaviour is inside these 3.5 MB. ELF entry is `0x40249c`; it passes `main = 0x40aef4` to
`__libc_start_main` (`mov $0x40aef4,%edi` at `0x4024c7`). `.comment` = `GCC: (GNU) 9.4.0`.

## CLI and config

`./ajiasu -h` output, verbatim (WSL Ubuntu-24.04, non-root, `/tmp/ajiasu_probe -h`, exit 0):

```
ajiasu 4.2.3.0 (core:20241105-1)

More help: ./ajiasu -h
Usage: ./ajiasu {login|list|logout}
Usage: ./ajiasu connect "Shanghai 1*"
Default config file: /etc/ajiasu.conf
Config file options:
	user your-account-name
	pass your-password


For experts: ./ajiasu -c /etc/ajiasu.conf {login|list|connect|logout}
You can use '--sys-env-id 1234' to identify this client to avoid disconnecting the wrong client if you need to connect/disconnect frequently.

Config file advanced options:
	cache_dir /tmp
	connect server-name(wildcard supported)
	protocol {udp|tcp|lwip|socks5|http|proxy}
	use_vpn_dns {yes|no}
	use_vpn_route {yes|no}
	up_down your-script-file
```

With no arguments the same usage is printed, preceded by
`Can not open config file: /etc/ajiasu.conf` (string at file offset `0x28a892`), exit 0.

- Commands: `login`, `list`, `logout`, `connect "<name wildcard>"`; `-c <conf>` selects an
  alternate config; `-h`/`--help` (offsets `0x28a872`, `0x289b48`).
- `--sys-env-id N` is CLI-only in 4.2.3.0 (`sys-env-id` at `0x28a887`, `Use SysEnvId: %s` at
  `0x28a95b`). 4.3.0.1 also accepts it as a config key.
- Config keys documented above are confirmed as literals: `user`, `pass` (`0x289bdd`, `0x289bf5`),
  `cache_dir` (`0x28a8ea`), `connect` (`0x28a8f4`), `protocol` (`0x28a8fc`), `up_down` (`0x28a905`),
  `log_level` (`0x28a90d`, undocumented in `-h`), `use_vpn_dns` (`0x28a919`), `use_vpn_route`
  (`0x28a925`). Default config path `/etc/ajiasu.conf` (`0x28ac18`).
- `protocol` accepts `udp|tcp|lwip|socks5|http|proxy`; the interactive prompt only offers
  `[udp/tcp/lwip/proxy]` (`0x28a198`) and treats `socks5` as an alias of `proxy` (`0x28a1d3`,
  `0x28a1df`, `Protocol: "%s"` `0x28a1e5`).

### Cache and session files

| literal | offset | meaning |
|---|---|---|
| `cache_dir` | `0x28a8ea` | base directory for cached state |
| `/config.dat` | `0x28a933` | per-instance state file under `cache_dir` |
| `vpnconfig` | `0x28a93f` | JSON section name inside `config.dat` |
| `LoginSync` | `0x28d809` | cached login-sync payload (`appInit: cachedLoginSync loaded, size=%d`) |
| `/CacheNetwork/network-` | `0x28dbd7` | cached route/network blobs, `md5 is incorrect, clear` |
| `CachePassword` | `0x28db62` | password obfuscation for the cached response |
| `appInit: dataDir=%s, config=%s` | `0x28d830` | confirms `dataDir` + config wiring |

`LoginSync` is versioned by `SyncVersion`; `ApiLoginSync` re-saves only when the server's sync
version changes (`0x28d9e0` / `0x28da20`). Machine identity is collected from `/etc/machine-id`,
`/var/lib/dbus/machine-id`, `/sys/class/dmi/id/product_uuid` and `/proc/version`
(`0x289db9`..`0x289e0a`); the `_site`, `_lang`, `_os_ver`, `_os_dev`, `_os_dim` suffixes at
`0x289e1c`..`0x289e4b` are the device-descriptor keys sent to the API. `[INFERENCE]` the cached
`LoginSync` file is a `.dat` under `cache_dir` whose contents are the JSON `LoginSync` object with
an obfuscated password; it was **not** produced here because no login was performed.

## Network constants

API hosts — four base URLs are stored consecutively by one initializer (xrefs `0x4117c6`,
`0x4117f1`, `0x41181c`, `0x411847`, all inside the function entered at `0x411798`):

```
0x28ac34  https://login.ajs-app.com/
0x28ac4f  https://login.mmegg.com/
0x28ac68  https://login.mmegg.com:4443/
0x28ac86  https://login.mbclubsh.com/
```

API surface:

| literal | offset | notes |
|---|---|---|
| `/client.php` | `0x28d901` | single HTTP endpoint, 6 call sites (`0x43f2ef`, `0x43f7dd`, `0x4406c7`, `0x440bf6`, `0x442aa5`, `0x443fc8`) |
| `ClientApiLogin/Login` | `0x28da99` | login action |
| `ClientApiLoginSync` | RTTI `0x28e2e0` | sync action (lambda type info) |
| `ClientApiClientSetting/LoadRoute` | `0x28ddb0` | route download |
| `ClientApiReport/WebsiteCheckReport` | `0x28e038` | telemetry: URL keyword checks |
| `X-Sig: ` / `X-Timestamp: ` | `0x28daae` / `0x28dab6` | request signing headers |
| `application/x-www-form-urlencoded` | `0x28daf0` | POST body |
| `username` / `password` | `0x28d93e` / `0x28d947` | login form fields |
| `User-Agent: %s` | `0x29c257` | UA is formatted from a variable; no UA literal found `[UNKNOWN]` |

Response/JSON keys observed as literals: `AuthResult`, `AuthMessage`, `GroupTitle`, `Membership`,
`ExpireTime`, `Expired`, `LoginToken`, `Login`, `UserName`, `Servers`, `ResponseLogin`,
`SyncVersion`, `Result`, `Success`, `PresetRoute`, `RouteSection`, `UrlWeb`, `LatestVersion`,
`Comment`, `Status`, `Sites`-style device suffixes above.

RSA key material — a 1024-bit modulus (309 decimal digits, 128-byte blocks) is stored as a decimal literal and exponent `65537`:

```
0x28a030  141601187905926909444644730286409666755495300050721564398199769080746616339148556683219048017120018748016428099359153205651362664001287890722535799466700786136519566714160932079339659299698611914846217596226121056961277268141789905486380539961102053398435632146710731361922836526658451793921073088741138357721
0x28a166  65537
```

This is **byte-identical** to `FvCrypto.serverPublicKey()` in OpenAJS, and the same literal is
present in Windows 4.9.6.0 and Linux 4.3.0.1. `mbedtls_rsa_import failed` /
`mbedtls_rsa_complete failed` (`0x28d74a`) are the code paths that consume it, i.e. RSA-OAEP key
transport for the FV session key.

TLS: mbedTLS is compiled in (`/work/fvclient/src/mbedtls/library/ssl_tls13_client.c` etc.) and is
used by libcurl (`mbedTLS: %s Handshake complete, cipher is %s`, `mbedtls_ssl_set_session`), so the
control plane is HTTPS with a full CA store. libcurl also contains
`public key hash: sha256//%s`, so certificate pinning is *possible*, but no pin literal was found
in `.rodata` `[UNKNOWN]`.

## Protocol modes

| `protocol=` | transport | root / TUN | evidence |
|---|---|---|---|
| `udp` | FV over UDP to the node, tun device + routes | root + `/dev/net/tun` | `root required, please run with "sudo"` (`0x28a210`), `try to open tun device` (`0x28a41b`), `udp create context` (`0x28c6ce`) |
| `tcp` | FV over TCP, tun device + routes | root + `/dev/net/tun` | `tcp listener` (`0x28b5ea`), `ip route add` (`0x28a3d1`) |
| `lwip` | FV over TCP with an in-process lwIP stack | root + `/dev/net/tun` | `lwip netif_add failed`, `lwip tcp_new/bind/listen` (`0x28ba41`..), `FVClientProxyLwip` |
| `proxy` / `socks5` / `http` | local SOCKS5/HTTP listener, FV to the node | **no root, no TUN** | help text `protocol:proxy (socks5/http) which doesn't require root` (`0x28a210`), `FV_PROXY` (`0x28a609`), `proxy(socks5/http): %s` (`0x28a612`) |

Internal proxy handshake modes recovered from `FVClientProxySocks` log strings
(`0x28c108`..`0x28c358`): `nat/udp` (skips the SOCKS5 request), `socks4`, `socks5`,
`http-proxy`; connection roles are tagged `Remote(Relay)` / `Remote(Unknown)` (`0x28c09d`).
`Nat64Detector` (`0x28e708`) probes DNS for IPv6 and derives a NAT64 prefix before connecting.

FV data plane, as seen in this binary:

- Command layer: `CommandCode_Ctrl` (auth and control) and `CommandCode_NetPkt` (IP packets),
  logged as `send CommandCode_Ctrl auth %s ...` (`0x28cc10`), `recv CommandCode_NetPkt: size=%d`
  (`0x28cda0`), `recv CommandCode_Ctrl: cmd=%s, all:` (`0x28cdc8`). Retries:
  `retry emitClientAuth` (`0x28ccec`), `retry emitObscurePacket` (`0x28ccd4`).
- Obfuscation: `buffer_obscure_packet key size must be FV_CipherKeySize` (`0x28ede0`), i.e. a
  fixed-size cipher key; `FV_CipherKeySize` is a compile-time constant whose numeric value was not
  recovered `[UNKNOWN]`.
- Proxy-mode request (built at `0x451192`, fields appended at `0x45121d`..`0x45129c`):

  ```
  0x28ee18  OPV2
  0x28ee1d  POST /s?s=%s&n=%s HTTP/1.1\r\n
  0x28ee3a  Upgrade: websocket\r\n
  0x28ee4f  Connection: Upgrade\r\n
  0x28ee65  Content-Length: %d\r\n
  0x28ee7a  Sec-WebSocket-Key: %s\r\n
  0x28ee92  \r\n
  0x28eecf  code=200
  ```

- Frame field tokens (require `strings -n 2`): `s=` `0x28ee95`, `n=` `0x28ee98`, `k=` `0x28ee9b`,
  `l=` `0x28ee9e`, `_=` `0x28eddb`. These are exactly the fields OpenAJS's `FvConnection` writes:
  `s=<sid>\nn=<hex iv>\nk=<base64 RSA-OAEP key>\nl=<len>\n_=<random pad>`, with `k=` empty on the
  data channel. This is the strongest available evidence that the Linux client speaks the same FV
  framing as OpenAJS.
- Auth/response field keys are a contiguous literal table at `0x28af6f`..`0x28afd5` (visible only
  with `strings -n 2`):

  ```
  ok  sid  sip  cip  dns  ka  kat  kapf  mtu  ft  fm  au  cuid  cseid  st  cv  osp  osd  osv  dim  peip  ce
  ```

  `[INFERENCE]` from position and from OpenAJS's `authFields()`: `sid`=session id, `sip`=server
  virtual IP, `cip`=client virtual IP, `dns`=pushed resolver, `ka`/`kat`/`kapf`=keep-alive
  interval/timeout/flag, `mtu`=tunnel MTU, `ft`/`fm`=failure type/message, `au`=auth user,
  `cuid`/`cseid`=client/device ids, `st`=site, `cv`=core version, `osp`/`osd`/`osv`=platform /
  device / OS version, `dim`=device model, `peip`=peer (egress) IP, `ce`=client extra. The first
  ten match OpenAJS exactly; `peip`, `kapf`, `mtu` are additional on the Linux side.
- Control-channel commands are single literals at `0x28cd4f`: `con`, `ping`, `pong`, `off`
  (plus `sid`, `aur`, `fm`, `ft` in the same block at `0x28cdf2`, and `au` at `0x28cc0a`).
  They belong to the enclosing control-loop functions that consume them — **not** to the small
  `CommandCode_Ctrl` wrappers at `0x42c1c8`..`0x42c684`, which contain no verb literals. The verbs
  are sent as `CommandCode_Ctrl` payloads, logged by `recv CommandCode_Ctrl: cmd=%s, all:`.
- VPN-mode environment contract for the `up_down` hook: `FV_USER`, `FV_PROTOCOL`, `FV_VIRTUAL_DEV`,
  `FV_CLIENT_VIRTUAL_IP`, `FV_SERVER_VIRTUAL_IP`, `FV_DNS_`, `FV_PROXY`, `FV_ACTION`,
  `FV_FAILURE_TYPE`, `FV_FAILURE_MESSAGE` (`0x28a34c`..`0x28a68a`).

Local proxy listener (proxy/socks5/http): address is `127.0.0.1` concatenated with a port
(`0x28a5ff` used at `0x40a05f`; `0x28b587` used at `0x41a39a`), and the port candidates are a
5-element `uint16` array written at `0x41a3c0`:

```
0x41a3c0  movw $0x438,-0x5a(%rbp)     ; 1080
0x41a3c6  movw $0x49c8,-0x58(%rbp)    ; 18888
0x41a3cc  movw $0x70d8,-0x56(%rbp)    ; 28888
0x41a3d2  movw $0x97e8,-0x54(%rbp)    ; 38888
0x41a3d8  movw $0x0,-0x52(%rbp)       ; terminator
0x41a3e6  cmpq $0x4,-0x18(%rbp)       ; loop over 5 candidates
```

The loop body issues `socket`/`bind`/`listen` (`0x674d24`, `0x674d8b`, `0x67684e`) and jumps back
to the index increment on failure (`0x41a5d2 -> 0x41a3e6`). `[INFERENCE]` semantics: try 1080, then
18888, 28888, 38888, stop at the first that binds. This *refutes* the prior-art claim of
"instances auto-increment from 1080" — it is a fixed four-entry list, so the 5th instance has no
port and fails.

### Which transport does proxy mode use

Both transports exist in the binary, and the call graph separates them:

- `FVProxyRequestHeader::fetch` (builder entered at `0x451192`) is called from `0x419dfc` inside the
  function entered at `0x41964a`, whose log strings are `fvproxy_auth_context_new: cmd=%s`,
  `fvproxy_auth_context_new: failed. cmd=%s, clientRequestFailureCount=%d, msg=%s`,
  `MAX_CONN_BUFFER_SIZE=%d`. It is also called from `0x424a53` in the per-connection handshake
  function entered at `0x424988` (`%s handshake: sending request ...`, `Remote(Relay)`,
  `handshake: keep socks5/http replay for socks5/http/udp proxy`, `handshake: nat/socks4 mode ...`).
  ⇒ **proxy mode (`proxy|socks5|http`) authenticates over the HTTP/WebSocket-shaped request
  `POST /s?s=<sid>&n=<nonce> HTTP/1.1` + `Upgrade: websocket` + `Sec-WebSocket-Key`, and expects
  `code=200`.**
- The `CommandCode_Ctrl` sender at `0x42b1b4` is called from six sites in four small wrappers
  (`0x42c1c8`, `0x42c22e`, `0x42c350`, `0x42c472`); those wrappers are reached from the control
  loop, and it is the control loop — not the wrappers — that owns the verb literals `con`, `ping`,
  `pong`, `off`, `aur` and the packet path `write net to dev pkt` / `recv CommandCode_NetPkt`.
  ⇒ **VPN modes (`udp|tcp|lwip`) use the raw FV control channel over TCP/UDP with
  `CommandCode_Ctrl` + `CommandCode_NetPkt`.**

So the Linux client is **not** raw-TCP-only like OpenAJS: for the proxy API use case it uses the
same HTTP-upgrade transport as the Windows 4.9.6.0 client, while its VPN modes use the raw command
channel. An emulator that only needs the selectable-egress proxy must implement the HTTP-upgrade
variant first.


## Comparison with Windows/OpenAJS

Occurrence counts in `strings -a -n 5` dumps (Linux 4.2.3.0: 8,482 lines; Linux 4.3.0.1: 8,521;
Windows 4.9.6.0: 13,305). Generated by `compare.py`; 0 means "absent from the ASCII strings dump",
which for Windows may also mean "stored obfuscated/UTF-16", not necessarily absent from the binary.

| token | linux-4.2.3.0 | linux-4.3.0.1 | win-4.9.6.0 |
|---|---|---|---|
| `login.ajs-app.com` | 1 | 1 | 0 |
| `login.mmegg.com` / `:4443` | 2 / 1 | 2 / 1 | 0 |
| `login.mbclubsh.com` | 1 | 1 | 0 |
| `/client.php` | 1 | 1 | 1 |
| `ClientApiLogin/Login` | 1 | 1 | 1 |
| `ClientApiClientSetting/LoadRoute` | 1 | 1 | 1 |
| `ClientApiReport/WebsiteCheckReport` | 1 | 1 | 1 |
| `LoginSync` | 7 | 7 | 9 |
| `SyncVersion` / `ResponseLogin` | 1 / 1 | 1 / 1 | 1 / 1 |
| `LoginToken` | 1 | 1 | 4 |
| `X-Sig` / `X-Timestamp` | 1 / 1 | 1 / 1 | 1 / 1 |
| `ClientUniqueId` / `DeviceUniqueId` | 1 / 1 | 1 / 1 | 1 / 1 |
| `cseid` | 1 | 1 | 1 |
| `/config.dat` / `vpnconfig` | 1 / 1 | 1 / 1 | 1 / 1 |
| `/etc/ajiasu.conf` | 1 | 1 | 0 |
| `FV_CipherKeySize` / `buffer_obscure_packet` | 1 / 1 | 1 / 1 | 1 / 1 |
| `CommandCode_Ctrl` / `CommandCode_NetPkt` | 3 / 1 | 3 / 1 | 3 / 1 |
| `POST /s?s=` / `Sec-WebSocket-Key` / `code=200` | 1 / 1 / 1 | 1 / 1 / 1 | 1 / 1 / 1 |
| `OPV2` (from `strings -n 4`) | 1 | 1 | 1 (`0x3216d0`) |
| RSA modulus `1416011879059269094…` | 1 | 1 | 1 |
| `65537` | 1 | 1 | 1 |
| `FV_USER` | 1 | 1 | 1 |
| `FV_PROTOCOL` / `FV_PROXY` / `FV_DNS_` | 1 / 1 / 1 | 1 / 1 / 1 | 0 / 0 / 0 |
| `FV_VIRTUAL_DEV` / `FV_CLIENT_VIRTUAL_IP` / `FV_SERVER_VIRTUAL_IP` | 1 / 1 / 1 | 1 / 1 / 1 | 0 / 0 / 0 |
| `Nat64Detector` | 1 | 1 | 1 |
| `/dev/net/tun` | 1 | 1 | 0 |
| `sys-env-id` | 2 | 2 | 0 |
| `cache_dir` / `cache-dir` | 2 / 0 | 0 / 2 | 0 / 0 |
| `proxy-local-server` | 0 | 3 | 0 |
| `use_vpn_dns` / `use-vpn-dns` | 3 / 0 | 1 / 2 | 0 / 0 |
| `up_down` / `up-down` | 2 / 2 | 0 / 4 | 0 / 0 |
| core build stamp | `20241105-1` | `20260525-1` | `20250708-1` |

Exact-token check requested by AJS-02/WinStatic (all present in both Linux builds, one occurrence
each; offsets are file offsets, `strings -n 4`):

| token | Linux 4.2.3.0 | Linux 4.3.0.1 | Windows 4.9.6.0 |
|---|---|---|---|
| `OPV2` | `0x28ee18` | `0x294558` | `0x3216d0` |
| `/s?` (`POST /s?s=%s&n=%s HTTP/1.1`) | `0x28ee1d` | `0x29455d` | `0x3216d8` |
| `Upgrade: websocket` | `0x28ee3a` | `0x29457a` | present |
| `Sec-WebSocket-Key` | `0x28ee7a` | `0x2945ba` | `0x321750` |
| `buffer_obscure` | `0x28ede0` | `0x294520` | `0x32168c` |
| `kapf` | `0x28af93` | `0x290573` | present |
| `cuid` | `0x28afad` | `0x29058d` | present |
| `cseid` | `0x28afb2` | `0x290592` | present |
| `peip` | `0x28afd0` | `0x2905b0` | present |
| `code=200` | `0x28eecf` | `0x29460f` | present |
| `protocolVersion` / `implType` | `0x28ade8` | `0x2903c8` | present |

Findings from the comparison:

1. **One FV protocol across all three clients.** The frame tokens (`s=`, `n=`, `k=`, `l=`, `_=`),
   `OPV2`, `POST /s?s=%s&n=%s`, `Sec-WebSocket-Key`, `code=200`, `CommandCode_Ctrl/NetPkt`,
   `buffer_obscure_packet` and the RSA modulus are present, unchanged, in Linux 4.2.3.0,
   Linux 4.3.0.1 and Windows 4.9.6.0. Only the build stamps differ.
2. **The login hosts are plaintext only on Linux.** Windows 4.9.6.0 has zero occurrences of
   `ajs`, `mmegg` or `mbclubsh` in either the ASCII or UTF-16 dumps, while keeping `/client.php`
   and all `ClientApi*` actions. So the Windows build hides the host list (likely in the encrypted
   `product.dat`, 818 bytes, high entropy) and resolves it at runtime `[INFERENCE]`. For a proxy
   implementation the Linux strings are the usable source of hosts.
3. **The `FV_*` up/down environment contract is Linux-only** (`FV_VIRTUAL_DEV`,
   `FV_CLIENT_VIRTUAL_IP`, `FV_SERVER_VIRTUAL_IP`, `FV_DNS_`, `FV_PROXY`, `FV_ACTION`,
   `FV_FAILURE_TYPE`). Windows has `FV_USER` only.
4. **Config surface changed in 4.3.0.1**: underscore keys (`cache_dir`, `use_vpn_dns`, `up_down`)
   became dash keys (`cache-dir`, `use-vpn-dns`, `up-down`), `-h` gained `--option=value` support,
   and `proxy-local-server` appeared with default `127.0.0.1:1080` (string `0x28fe8f`). 4.3.0.1
   also documents that `socks5`/`http` are aliases of `proxy` and that proxy mode uses no tun/route.
5. **OpenAJS tracks the 4.3.0.1 core, not 4.2.3.0.** `FvConnection.authFields()` hardcodes
   `cv=20260525-1`, which is exactly the core build stamp printed by `./ajiasu_4301 -h`
   (`ajiasu 4.3.0.1 (core:20260525-1)`). Its frame layout (`s/n/k/l/_`), AES-CTR usage and
   RSA-OAEP key wrap match the Linux strings; its `FvCrypto` modulus is identical. OpenAJS speaks
   FV over a raw TCP socket, while the Linux client additionally has the HTTP/WebSocket-shaped
   `POST /s?s=&n=` variant for proxy mode — both carry the same encrypted frame.
6. **Transport decision (answers the WinStatic question).** Linux proxy mode and Windows 4.9.6.0
   use the **same HTTP-upgrade transport**: `OPV2`, `POST /s?s=%s&n=%s HTTP/1.1`,
   `Upgrade: websocket`, `Sec-WebSocket-Key`, `Content-Length`, `code=200`, `buffer_obscure_packet`
   — all present in both binaries with identical text. OpenAJS's raw-TCP framing corresponds to the
   Linux/Windows `CommandCode_Ctrl`/`CommandCode_NetPkt` channel, which the Linux binary reaches
   only from its VPN control path (`0x42c1c8`..`0x42c472`), not from `FVProxyRequestHeader::fetch`.
   For a proxy-only emulator, implement the HTTP-upgrade variant first; the raw command channel is
   needed only for `udp|tcp|lwip`.

## 4.3.0.1 delta

Acquired as requested (AJS-02 OSINT): `https://www.91ajs.com/files/downloads/linux/ajiasu-amd64-4.3.0.1.tar.gz`
→ HTTP 200, 1,264,209 bytes, `sha256=0affe73aff4b7ee11cc6fab5c060d71dc3056a1c2903ca3435f4fe009d2689e7`;
the archive contains only `./ajiasu` (3,607,464 bytes,
`sha256=08fcd8668f7aaf6660277778e67fd2afd622c3f1c8fb634e57f31605bf01c8f6`). Same shape as 4.2.3.0:
static, stripped, non-PIE, `GCC: (GNU) 9.4.0`, 8,521 `strings -n 5` lines.

Identity from `./ajiasu_4301 -h` (WSL, non-root, `/tmp`, exit 0): `ajiasu 4.3.0.1 (core:20260525-1)`.

**CLI help, verbatim (4.3.0.1):**

```
ajiasu 4.3.0.1 (core:20260525-1)

Usage: ./ajiasu [--option=...] {login|list|logout}
Usage: ./ajiasu [--option=...] connect "Shanghai 1*"

Default config file: /etc/ajiasu.conf
Config file options:
    user = your-account-name
    pass = your-password

For experts: ./ajiasu -c /etc/ajiasu.conf {login|list|connect|logout}
Config file options can also be passed via command line arguments, for example: "--proxy-local-server=127.0.0.1:8888"

Config file advanced options:
    cache-dir = /tmp
    connect = server-name (wildcard is supported)

    # udp/tcp/lwip uses the tun device and creates the real VPN connection with routes
    # proxy only starts a local proxy server (socks5/http are all supported and they are just aliases), no tun or route
    protocol = {udp|tcp|lwip|socks5|http|proxy}

    use-vpn-dns = {yes|no}
    use-vpn-route = {yes|no}

    # run a script when the VPN connection is up or down
    # you can run "export > /tmp/env.txt" in the script to see all environment variables
    up-down = ./dir/your-script-file.sh

    # make local proxy server listen on specified "ip:port" and use the given username/password for authentication
    proxy-local-server = local-username:local-password@ip:port

    # a unique id to identify this client to avoid disconnecting the other wrong clients if you need to connect/disconnect frequently
    sys-env-id = ...
```

**Config keys, old → new** (all confirmed as literals; `cache-dir` `0x28fe61`, `proxy-local-server`
`0x28fe7c`, `log-level` `0x28fea6`, `sys-env-id` `0x28fd9e`):

| 4.2.3.0 | 4.3.0.1 |
|---|---|
| `user` / `pass` | `user` / `pass` (same) |
| `cache_dir` | `cache-dir` |
| `connect` | `connect` (same) |
| `protocol` | `protocol` (same) |
| `use_vpn_dns` / `use_vpn_route` | `use-vpn-dns` / `use-vpn-route` |
| `up_down` | `up-down` |
| `log_level` | `log-level` |
| — (CLI only) | `sys-env-id` |
| — | **`proxy-local-server = [user:pass@]ip:port`**, default `127.0.0.1:1080` (`0x28fe8f`) |

4.3.0.1 also accepts every config key as `--key=value` on the command line
(`Usage: ./%s [--option=...]`), and has no standalone `--help` literal any more — the only
occurrence is inside the prose `run '--help' to see help` (`0x28fde0`); help is printed by `-h`
(`More and advanced help: ./%s -h`).

**API hosts: unchanged.** The same four plaintext base URLs are present at `0x290214`, `0x29022f`,
`0x290248`, `0x290266` (`https://login.ajs-app.com/`, `https://login.mmegg.com/`,
`https://login.mmegg.com:4443/`, `https://login.mbclubsh.com/`). New in 4.3.0.1 is a
**remote host list**: `ApiUrls` (`0x293211`), `FetchedUrlHosts` (`0x293044`) and
`appInit: fetchedUrlHosts.size()=%d, builtinUrlHosts.size()=%d, appCommonParams.size()=%d`
(`0x2930b0`) — the client can now fetch additional API hosts at runtime and merge them with the
built-in four. `[INFERENCE]` they arrive in the `/client.php` response (the `ApiUrls` key) and are
cached; the fetch path was not exercised.

**Protocol modes: same six names**, same help enum `{udp|tcp|lwip|socks5|http|proxy}`, and the help
now states explicitly that `udp/tcp/lwip` use the tun device with routes while `proxy` starts only
a local proxy with no tun/route and treats `socks5`/`http` as aliases.

**Local proxy port: same fallback list.** The `uint16` candidate array is still there, relocated to
`0x41e0b1`..`0x41e0c3` (`1080, 18888, 28888, 38888`), so `proxy-local-server` overrides the default
but the four-port fallback survives. New diagnostics: `unable to listen on %s:%d` (`0x290ca3`),
`unable to get listener port` (`0x290c87`); the 4.2.3.0 strings `new/bind failed`,
`getsockname failed` and `tcp listener ` are gone (listener code rewritten).

**New strings in 4.3.0.1** (71 app-level strings added, 46 removed; library noise excluded):

| string | offset | meaning |
|---|---|---|
| `proxy server should not be exposed without auth, see the help` | `0x290c18` | refuses unauthenticated public bind |
| `proxy server password is weak, use at least 10 characters with both letters and digits for better security` | `0x2909a8` | password policy |
| `force to use a randomly generated password: %s` | `0x290a18` | auto-generated proxy password |
| `socks5 client username&password check fails` | `0x291978` | SOCKS5 user/pass auth implemented |
| `proxy-authorization:` / `basic` | `0x2919ab` / `0x291a27` | HTTP proxy Basic auth |
| `invalid http proxy Proxy-Authorization header format` / `... only Basic auth is supported` | `0x2919f0` | header parsing |
| `%08x%02x%08x` | `0x293402` | changed from 4.2.3.0's `%08x%04x%08x` (cache/id obfuscation format) |
| `06ka4z0dc6ga02c` | `0x293080` | fixed 15-char literal, purpose `[UNKNOWN]` |
| `FetchedUrlHosts` / `ApiUrls` | `0x293044` / `0x293211` | remote API host list |

**Removed in 4.3.0.1** (present in 4.2.3.0 only): the API action literals `UserLogin`, `Welcome`,
`welcome`, `WebsiteTest` (`0x28d813`, `0x28d81d`, `0x28d911`, `0x28dec9`) — only `App:UserLogin`
(`0x28f3a5`) and `appUserLoginSync` (`0x2932b8`) survive — plus `appUserLogin: accountName=%s`,
`Can not open config file: %s`, and the old underscore config keys. `LoginSync`, `ClientApi*`,
`X-Sig`, `X-Timestamp` and the whole FV layer are unchanged.

**Practical consequence for the proxy API:** 4.3.0.1 is the better target. It can bind a chosen
`ip:port`, enforces/accepts proxy authentication (Basic for HTTP, user/pass for SOCKS5), and refuses
to expose an unauthenticated public listener — all features 4.2.3.0 lacks. The wire protocol below
the listener (FV, `POST /s?s=&n=`, `code=200`, AES-CTR + RSA-OAEP) is identical in both builds.

## Open questions

- `X-Sig` algorithm and canonical string `[UNKNOWN]`. Only the header literal exists
  (`0x68daae`, used at `0x4408c9` inside the function entered at `0x4405fc`, which also emits
  `/client.php` at `0x4406c7`). No HMAC/RSA helper string was tied to it in this pass.
- Numeric `CommandCode_*` values, FV packet header layout and `FV_CipherKeySize` `[UNKNOWN]`.
  The constants are immediates in stripped code; decompilation of the functions at `0x42b1b4`
  (ctrl send) and `0x450427`/`0x45080b` (obscure buffer) is the next step.
- `OPV2` semantics `[UNKNOWN]` — present in all three clients next to the proxy request builder;
  plausibly a protocol-version tag, not proven.
- Whether the proxy-mode `s=` value is the FV session id or a separate HTTP session id
  `[INFERENCE]` (OpenAJS reuses `sid` from auth).
- Certificate pinning `[UNKNOWN]`: `public key hash: sha256//%s` exists (libcurl feature), no pin
  literal located.
- 4.3.0.1 was compared at string level only; its CLI behaviour beyond `-h` was not exercised, and
  no byte-level diff of the two `.text` sections was performed.
- Windows host-list storage/decryption `[UNKNOWN]`; `product.dat` is 818 bytes of high-entropy
  data and was not decrypted.

## Self-audit

| # | claim | verdict | test |
|---|---|---|---|
| 1 | Control plane is `https://login.ajs-app.com/client.php` with `ClientApi*` actions | **CONFIRMED** | literal + xref (`0x4117c6`, `0x43f2ef`..`0x443fc8`); one initializer builds the 4-host list (`0x411798`) |
| 2 | `protocol=proxy/socks5/http` needs no root and no TUN | **CONFIRMED** | help text `0x28a210`; `FV_PROXY` path `0x40a05f`; tun strings only reachable from the VPN branch (`0x4096a4`) |
| 3 | Linux 4.2.3.0, Linux 4.3.0.1, Windows 4.9.6.0 and OpenAJS share one FV protocol | **CONFIRMED** | identical `s=/n=/k=/l=/_=`, `OPV2`, `POST /s?s=`, `CommandCode_*`, RSA modulus; `cv=20260525-1` matches the 4.3.0.1 core stamp |
| 4 | "proxy instances auto-increment the port from 1080" (prior art) | **OVERTURNED** | port candidates are a fixed array `{1080,18888,28888,38888,0}` at `0x41a3c0`, loop bounded by `cmpq $0x4` |
| 5 | "4.2.3.0 is the newest Linux build / no official Linux download" | **OVERTURNED** | `https://www.91ajs.com/download` lists `ajiasu-amd64-4.3.0.1.tar.gz` (HTTP 200); brief's `x86_64-<ver>` pattern 404s for all 5 candidates |
| 6 | "the Linux client uses the raw-TCP FV transport like OpenAJS" | **OVERTURNED** | call graph: `FVProxyRequestHeader::fetch` (`0x451192`) is called only from `fvproxy_auth_context_new` (`0x41964a`) and the proxy handshake (`0x424988`); `CommandCode_Ctrl` (`0x42b1b4`) only from the VPN control functions (`0x42c1c8`..`0x42c472`) |

Numbers above carry their denominators: string counts are per full `strings -a -n 5` dump of each
binary (8,482 / 8,521 / 13,305 lines); the filtered artifact holds 471 deduplicated lines; 38
distinct `/work/fvclient/src/...` paths; 8,333 internal call targets found by the xref scanner
(used only as an approximate function-entry index). All observations dated 2026-10-23.
