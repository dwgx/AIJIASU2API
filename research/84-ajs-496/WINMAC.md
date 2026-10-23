# AJS-84 — Windows and macOS 4.9.6.0 static RE: one desktop core, two OS plumbing stacks

Status: static, read-only, 2026-10-24. Slice `AJS-84/WINMAC`. No sample was executed, no
installer/driver/helper was run, no vendor host was contacted, no credential was used. This file
is the only artifact written by this slice; scratch output lives under
`C:/Users/dwgx1/.dwgx/tmp/ajs-re-20261024a/` (`analysis/`).

Scope: Windows `AJiaSu_4.9.6.0.exe` payload (`AJiaSu.exe`, `sciter.dll`, `sysproxy/*`, `tap/*`,
`product.dat`, `res.fvr`, `lang.fvr`) and macOS `AJiaSu_4.9.6.0.dmg` bundle (`AJiaSu`,
`vpnclientsvc`, `proxy_conf_helper`, `libsciter.dylib`, Sparkle 2.3.0, JS/HTM resources,
`product.dat`). Tools: `rabin2` 6.2.2, `objdump` (w64devkit), Python `pefile`/`capstone`/`cryptography`,
OpenSSL 3.5.7, PowerShell `Get-AuthenticodeSignature`, `sha256sum`. Everything below carries a
file offset, a VA, a command, or a file path. `[INFERENCE]` = deduced from adjacent evidence;
`[UNKNOWN]` = not observable statically; `[UNVERIFIED]` = not exercised by this slice.

## 1. Bottom line

1. **Windows and macOS 4.9.6.0 are the same desktop product built from one source tree.** Same
   Sciter UI resources (`res.fvr`, 138 entries; 8 of the 9 differing files are line-ending-only,
   and `dialog-setting.htm` adds one real macOS branch), same FV core stamp `20250708-1`
   (Win `0x31ffe8`, mac `0x1fccf4`), same `product.dat` (identical sha256 and plaintext), same API
   command set. The Windows build path is
   `F:\\workspace\\vpn123\\prog\\fvapp-desktop-sciter\\...` (`0x370204`); the macOS build path is
   `/Users/youdun-ndl/Desktop/gitlab-fv-app-apple/...` (`0x1ee9ef`).
2. **The desktop host list is statically recoverable.** `product.dat` is AES-128-CTR with key
   `MD5("[CONFIG]")` and a zero IV; both platforms ship the same 818-byte blob. Decryption yields
   four API bases plus the website (section 4). This supersedes the “not recoverable without
   breaking the blob” note in `TRIAGE.md`.
3. **The control plane is `POST <base>/client.php` with a `cmd=` command**, not one URL per
   operation. Sixteen `ClientApi…` command literals are present in both binaries
   (section 4.2), matching the Android lane's finding.
4. **Request signing is the same dual-hash scheme the emulator implements**, now confirmed in the
   desktop binary: seeds `0xCAFEBABE`/`0xDEADBEEF`, multiplier `65599`, `|` separators, headers
   `X-Sig: `/`X-Timestamp: ` (section 5.2). The desktop signing secret is not the OpenAJS
   colon-hex secret and is `[UNKNOWN]`.
5. **The tunnel core is byte-for-byte the same protocol family on both platforms** — `OPV2`,
   `POST /s?s=%s&n=%s`, AES-128-CTR, RSA-1024 OAEP, the same 1024-bit modulus at Windows
   `0x314468` and macOS helper `0x15188a` (section 6).
6. **Windows tunnels with a TAP-Windows driver + WinINET helper; macOS tunnels with a utun device
   + a setuid-root proxy helper.** Windows requires administrator at process start; macOS runs as
   the user and escalates only to install/launch its root helper (section 7).
7. **Updates are the largest platform divergence.** Windows self-updates through the API
   (`/downloads/`, `UpdateChecksum`, arch-specific package, installer execution); macOS uses
   Sparkle 2.3.0 with a runtime-supplied feed URL and no EdDSA public key in the bundle
   (section 8).
8. **No application-level anti-debug or anti-VM exists.** Every `IsDebuggerPresent` call site is
   inside the statically linked MSVC CRT; `NtQueryInformationProcess` and
   `ZwQuerySystemInformation` are CRT/`FVSys` OS-info helpers, not debugger probes. What does
   exist is privilege and process-control surface on Windows, and helper-integrity checks plus a
   setuid helper on macOS (section 9).
9. **Both bundles are corporate-signed, but the macOS Developer ID expires 2026-10-30** — six days
   after this analysis. Windows uses a GlobalSign EV certificate valid to 2027-03-14 (section 9.3).

## 2. Samples, hashes, metadata

Installer/DMG hashes (verified on this workstation):

| Sample | Size | SHA-256 |
|---|---|---|
| `AJiaSu_4.9.6.0 (1).exe` | 5,507,328 | `1b1c97f55a2348055c201ecba0badd0f7ea17dacb1f0df3ea979d52ee9d01a1c` |
| `AJiaSu_4.9.6.0.dmg` | 16,568,179 | `206bca275219a2f95e145e73683331aacc8c104ccfe456c71ca6b4e6ad5110ed` |
| `AJiaSu_4.12.2.0_ajs-official-website.apk` | 19,253,381 | `42ce2b5152c767c3f7412d65793aa15c294e539fb1f499428103205a352df3cb` |

Installer: PE32 GUI, NSIS `v3.04` (`Nullsoft Install System v3.04` at file offset `0xf67e`), version
resource 4.9.6.0, `CompanyName=爱加速`, `ProductName=爱加速`; Authenticode valid, same EV signer as
the payload. It does not install the TAP driver (the app does, on demand; see
`research/20-windows-static/installer/NSIS.md`).

### 2.1 Windows payload (PE)

| File | Size | SHA-256 (prefix) | Type | Link/build time (UTC) | Hardening | Signature |
|---|---|---|---|---|---|---|
| `AJiaSu.exe` | 4,064,640 | `0fd9d702…` | PE32 i386 GUI | 2025-07-09 02:33:25 | ASLR+DEP (`DllCharacteristics=0x8140`), no CFG, overlay 8,576 B | Valid EV, GlobalSign |
| `sciter.dll` | 6,369,280 | `42d0979c…` | PE32 i386 DLL, Sciter 4.4.8.33 | 2022-03-14 03:18:02 | ASLR+DEP (`0x0140`) | none |
| `sysproxy/amd64/sysproxy.exe` | 120,832 | `e0d76ece…` | PE32+ console, v1.0.0.1 | 2020-07-09 02:33:39 | ASLR+DEP+HighEntropyVA (`0x8160`) | none |
| `sysproxy/i386/sysproxy.exe` | 105,984 | `21d16a04…` | PE32 console, v1.0.0.1 | 2020-07-09 02:33:51 | ASLR+DEP (`0x8140`) | none |
| `sysproxy/arm64/sysproxy.exe` | 90,112 | `ab68825c…` | PE32+ console, v1.0.0.1 | 2020-07-09 02:33:34 | ASLR+DEP (`0x8140`) | none |
| `tap/tap/amd64/tap0901.sys` | 27,136 | `89c5ca14…` | PE32+ native, v9.0.0.21 | 2016-04-21 09:10:05 | **no ASLR, no DEP** (`0x0000`) | none (CAT signed by OpenVPN) |
| `tap/tap/i386/tap0901.sys` | 23,040 | `3b1ff525…` | PE32 native, v9.0.0.21 | 2016-04-21 09:05:12 | **no ASLR, no DEP** | none |
| `tap/tap/arm64/tap0901.sys` | 40,128 | `8417dc50…` | PE32+ native, v9.24.2.601 | 2019-10-23 08:38:29 | ASLR+DEP+CFG (`0x4160`), overlay 9,408 B | MSWHQL CAT |

`AJiaSu.exe` details: image base `0x400000`; `.text` paddr `0x400`/vaddr `0x401000`/size
`0x311800`; `.rdata` paddr `0x311c00`/vaddr `0x713000`; `.data` paddr `0x38d000`/vaddr
`0x78f000` (`rabin2 -S exe/AJiaSu.exe`). Embedded manifest (`<assembly>` at `0x3c47xx`) requests
`requireAdministrator` and `PerMonitorV2` DPI awareness. Version resource: `4.9.6.0`. PDB path
`F:\\workspace\\vpn123\\prog\\fvapp-desktop-sciter\\vpnclient\\build\\release\\product\\AJiaSu\\AJiaSu.pdb`
(`0x370204`). Installer is NSIS 3.04 and is EV-signed with the same certificate; it does not
install the TAP driver (the app runs `tapinstall.exe` on demand, section 7.1).

Authenticode signer (`Get-AuthenticodeSignature`): `CN=上海游盾网络科技有限公司` (Shanghai Youdun
Network Technology), issuer `GlobalSign GCC R45 EV CodeSigning CA 2020`, serial
`91310230MA1JXGC65A`, validity 2024-03-13 → 2027-03-14, thumbprint
`A1B7B574A19211012B09EC31940D23ACE01EDBFE`, timestamped by `DigiCert Timestamp 2024`.

### 2.2 macOS bundle (Mach-O, universal x86_64 + arm64)

| File | Container size | Slices (offset/size) | SHA-256 (prefix) | Identity / notes |
|---|---|---|---|---|
| `Contents/MacOS/AJiaSu` | 4,893,360 | x86_64 `0x4000`/2,476,608; arm64 `0x264000`/2,386,608 | `33039c96…` | `com.aijiasuinc.FVClientAJS`, hardened runtime, Team `N82WKSN6R7` |
| `Contents/MacOS/vpnclientsvc` | 3,300,976 | x86_64 `0x4000`/1,662,976; arm64 `0x19c000`/1,613,424 | `73c32d65…` | `vpnclientsvc-5555494487b65bf1c2553d37a25301163ef88e2d`, C++, root helper |
| `Contents/MacOS/proxy_conf_helper` | 173,120 | x86_64 `0x4000`/54,080; arm64 `0x14000`/91,200 | `d9579b9f…` | `proxy_conf_helper-55554944d2b4e87febba3ea0a16c7d3315bf1958` |
| `Contents/Resources/proxy_conf_helper` | 173,120 | same layout | `1ba0f607…` | identical first 34,880 B (code); only CMS blob differs |
| `Contents/Frameworks/libsciter.dylib` | 24,862,912 | x86_64 `0x4000`/12,615,168; arm64 `0xc0c000`/12,230,848 | `6d680fe0…` | `libsciter-555549443bfbf690a5b0323a9a574b2c713431e6`, Sciter 4.4.8.33 |
| `Sparkle.framework/Versions/B/Sparkle` | 1,842,448 | x86_64 `0x4000`/913,376; arm64 `0xe4000`/908,560 | `49f4d032…` | Sparkle **2.3.0** (`CFBundleVersion 2021`), re-signed by vendor |

`Info.plist`: `CFBundleIdentifier com.aijiasuinc.FVClientAJS`, `CFBundleShortVersionString 4.9.6`,
`CFBundleVersion 4.9.6.0`, `LSMinimumSystemVersion 10.13`, built with Xcode 16.2 (`DTXcode 1620`),
SDK `macosx15.2` (`24C94`), build machine `24D70`. Mach-O load commands: `LC_VERSION_MIN_MACOSX
10.13`, `LC_UUID 8caa37ee…`, rpath `@executable_path/../Frameworks`; links Cocoa, Sparkle,
CoreWLAN, Security, SystemConfiguration, CFNetwork, IOKit (no kernel extension, no NetworkExtension
framework).

Code signature (all vendor binaries): CodeDirectory v`0x20500`, flags `0x10000` (hardened runtime),
SHA-256 (`hashType 2`, `hashSize 32`, page 4 KiB), Team ID `N82WKSN6R7`, CMS SHA-256/RSA. Leaf:
`UID=N82WKSN6R7, CN=Developer ID Application: Shanghai Youdun Network Technology Co., Ltd
(N82WKSN6R7)`, serial `590F0ED960BABF6C`, validity **2021-10-29 → 2026-10-30 07:28:05Z**;
signing time in CMS `20250709030010Z`. `_CodeSignature/CodeResources` seals resources and pins the
helpers' designated requirements to `certificate leaf[subject.OU] = N82WKSN6R7`
(`CodeResources:167-183`).

## 3. Component maps

Windows — all userland except the stock OpenVPN TAP driver; no service, no daemon:

```
Sciter UI (sciter.dll 4.4.8.33 + res.fvr + lang.fvr)
        |
AJiaSu.exe  (libcurl + mbedTLS, FV core, route/DNS/firewall/TAP orchestration, update)
        |-- sysproxy.exe  global|pac|off  -> WinINET + RAS dial entries
        |-- tapinstall.exe install "OemVista.inf" -> tap0901.sys (TAP-Windows Adapter V9)
        `-- netsh / GetIpForwardTable / INetFwPolicy2 / Dnscache / Task Scheduler
```

macOS — the GUI is unprivileged; a root helper owns the tunnel and a setuid helper owns the proxy:

```
Sciter UI (libsciter.dylib + res.fvr + lang.fvr)
        |
AJiaSu (ObjC + libcurl/mbedTLS, FV core client side, update via Sparkle)
        |-- /tmp/VpnClientCtl-<id>.sock  (AF_UNIX ctl channel)
        |       `-- vpnclientsvc (root; utun, routes, scutil DNS, FV core)
        |-- /Library/Application Support/AJiaSu/proxy_conf_helper (setuid root)
        |       `-- SCPreferences SOCKS proxy (global|off)
        `-- Sparkle.framework 2.3.0 (Autoupdate, Updater.app, Downloader.xpc, Installer.xpc)
```

Both binaries import `AuthorizationCreate`/`AuthorizationCopyRights`/`AuthorizationFree`
(macOS app, `rabin2 -i`); `vpnclientsvc` imports `setuid`, `chown`, `chmod`, `socketpair`
(`0x17c057-0x17c9b8` region) and requires euid 0 (`root required`, `0x1515dd`).

## 4. Hosts and endpoints (all with offsets)

### 4.1 Hosts (decrypted `product.dat`, identical on both platforms)

`product.dat` 818 B, sha256 `e3de3fe622214034f68eff48705cfb028dd401754ac1daaee080688c5a78884f`;
plaintext sha256 `2b618010b49d66922d778beda9aae7117084a46fd6008706e6f5c9c53291183e`. Cipher:
AES-128-CTR, key `MD5("[CONFIG]") = 2304669ad435f8829a061ee9860a4cc5`, IV 16 zero bytes, no
padding; decrypts in place, so plaintext offsets equal ciphertext offsets.

| # | Value | Offset | Role |
|---|---|---|---|
| 1 | `https://www.91ajs.com/` | `0x004d` | `ProductWebSite`; base for `/pricing`, `/profile`, `/User/MessageCenter` `[INFERENCE]` |
| 2 | `https://login.ajs-app.com/` | `0x0108` | API base #1 |
| 3 | `https://login.mmegg.com/` | `0x0123` | API base #2 |
| 4 | `https://login.mmegg.com:4443/` | `0x013c` | API base #3 (non-standard port) |
| 5 | `https://login.mbclubsh.com/` | `0x015a` | API base #4 |
| 6 | `Help/QqService` | `0x029e` | `ProductCustomerSupportPath` |
| 7 | `help#socks5-tutorial` | `0x02c9` | `ProductSocks5TutorialPath` |

Other fixed keys in the same blob: `ClientSite=ajs` (`0x00d9`), `ClientDistChan=` empty (`0x00e9`),
`ProductMutexName=Global\\VPNCLIENT_SINGLE_{BB861724-…}` (`0x0065`), `ProductLpClassName=H-SMILE-FRAME`
(`0x00b7`), `ProductServerFlagMode=text` (`0x02f9`).

### 4.2 Control endpoint and command literals

`/client.php` is the POST target (`application/x-www-form-urlencoded`, `0x31e438`); the
`ClientApi…` strings are `cmd=` values, not URLs (same convention as the Android lane).

| # | Literal | Windows off | macOS off | Meaning |
|---|---|---|---|---|
| 8 | `/client.php` | `0x31641c` | `0x1ef0e5` | control endpoint path |
| 9 | `ClientApiLogin/Login` | `0x31e3f0` | `0x1fd3ca` | login + loginSync payload |
| 10 | `ClientApiUser/UserVpnServerFavoriteList` | `0x3163d4` | `0x1ef0a7` | favourites read |
| 11 | `ClientApiUser/VpnServerFavorite` | `0x31642c` | `0x1ef0f1` | favourite mutation (`Action=favorite\|unfavorite`) |
| 12 | `ClientApiVpnServer/AutoMatch` | `0x319e8c` | `0x1f1613` | auto-pick server (`LocationLevel`, `LocationCode`) |
| 13 | `ClientApiMessageCenter/GetAnnounce` | `0x3165ac` | `0x1ef145` | announcement |
| 14 | `ClientApiMessageCenter/CloseAnnounce` | `0x31669c` | `0x1ef16b` | close announcement |
| 15 | `ClientApiMessageCenter/GetMessageNum` | `0x3167f4` | `0x1ef1ae` | unread counters |
| 16 | `ClientApiVerifyCode/Captcha` | `0x3168fc` | `0x1ef204` | captcha image |
| 17 | `ClientApiVerifyCode/SendVerifyCode` | `0x3169b4` | `0x1ef234` | SMS/e-mail code |
| 18 | `ClientApiRegister/Register` | `0x316a20` | `0x1ef26f` | registration |
| 19 | `ClientApiUpgrade/UpgradeRecord` | `0x316b48` | `0x1ef2e3` | upgrade telemetry (`UpgradeType`, `PriorVersion`, `UpgradeDatetime`) |
| 20 | `ClientApiClientSetting/ClientSetting` | `0x316b94` | `0x1ef302` | settings sync (`SetType`, `Protocol`, `Reconnect`, `NetworkLock`, `Datetime`, `ConnectOnLaunch`, `AutoLaunch`, `AdvancedRouteItem`, `AdvancedRouteContent`) |
| 21 | `ClientApiClientSetting/LoadRoute` | `0x31e730` | `0x1fd5c3` | route/network-data download (`RouteSection`) |
| 22 | `ClientApiReport/Disconnect` | `0x317040` | `0x1ef6e3` | disconnect telemetry (`SdkVersion`, `VpnServerId`, `BytesRx/Tx`, `StopType`, `ReconnectIndex`, …) |
| 23 | `ClientApiReport/Report` | `0x3170e8` | `0x1ef76e` | batch report (`deviceData`, `networkData`, `appData`) |
| 24 | `ClientApiReport/WebsiteCheckReport` | `0x31e9c4` | `0x1fd709` | censorship-probe result (`CheckResult`, `RemoteIp`, `RemotePort`, `StatusCode`, `ResponseTime`) |

Sciter bridge commands that wrap the same calls: `App:HttpRequest` (`0x316290`), `App:ApiLoginSync`
(`0x316260`), `LoginSync` (`0x316254`); macOS equivalents at `0x1ef03b/0x1ef04c/0x1ef029`.

### 4.3 Other paths and fixed network literals

| # | Value | Windows off | macOS off | Notes |
|---|---|---|---|---|
| 25 | `/downloads/` | `0x318574` | — | Windows update package directory |
| 26 | `/pricing` | `0x319ce4` | — | website path for purchase |
| 27 | `/profile` | `0x315480` | — | website path (login token) |
| 28 | `/User/MessageCenter` | `0x315498` | — | website message centre |
| 29 | `/?id=` | `0x319050` | `0x1f174c` | ping query prefix |
| 30 | `&pingSecret=` | `0x319040` | `0x1f1752` | ping query suffix |
| 31 | `/CacheNetwork/network-` | `0x31e534` | `0x1fd4d2` | cached route/network data file prefix |
| 32 | `asset.sciter.com` | `0x316184` | `0x1eed57` | Sciter engine asset host (library string) |
| 33 | `http://www.winimage.com/zLibDll` | `0x324d79` | `0x1de781` | zlib credit string (inside the 1998-2004 Vollant banner) |
| 34 | `ipv4only.arpa` | `0x32183c` | `0x1fdbcb` | NAT64 detection probe |
| 35 | `18.244.0.188` | `0x320b6c` | `0x1ff2c8` | IPv4 connectivity probe `[INFERENCE]` (next to `Detected an IPv4 interface`) |
| 36 | `2001:4860:b002::68` | `0x320b7c` | — | IPv6 connectivity probe `[INFERENCE]` |
| 37 | `ftp@example.com` | `0x33d650` | `0x1f9c1e` | libcurl test string |
| 38 | `1.2.3.4` | `0x320ba0` | `0x1ff31e` | test literal |
| 39 | `127.0.0.1` | `0x314b6c` | `0x1f5706` | loopback / proxy listen default |

Note on encoding: most literals above are ASCII; the Windows strings marked `U` in
`analysis/strings_AJiaSu.exe.tsv` are UTF-16LE (e.g. `/pricing` `0x319ce4`, `/profile` `0x315480`,
`/User/MessageCenter` `0x315498`, `tapinstall.exe` `0x314a5c`, `OemVista.inf` `0x314ab4`,
`tap0901` `0x31488c`, `TAP-Windows Adapter V9` `0x314a20`, `\ipconfig.exe /flushdns` `0x315180`,
`HNetCfg.FwMgr` `0x315684`, ` AutoLaunch` `0x313834`, ` PowerBoot` `0x313850`,
`window-download-update.htm` `0x313164`, `Can not download file` `0x31309c`, `Invalid installer`
`0x3130e4`, and the `sysproxy.exe` usage block `0x18d50/0x18da8/0x18de0`). Offsets are the start
of the containing literal; UTF-16 offsets were verified against the decoded string, not by ASCII
grep.

macOS-only paths (helper plumbing):

| # | Value | Offset | Role |
|---|---|---|---|
| 41 | `/Library/Application Support/%s/proxy_conf_helper` | app `0x1eed72` | installed setuid helper path |
| 42 | `/Library/Application Support/VpnClientSvc` | app `0x1f356d`; svc `0x151628` | root service directory |
| 43 | `/tmp/VpnClientCtl-` + `.sock` | app `0x1f37a7`, `0x1f37ba` | ctl socket prefix/suffix |
| 44 | `/DNS-FVClientBak` | svc `0x151d70` | scutil DNS backup key |
| 45 | `com.apple.net.utun_control` | svc `0x151f9e` | utun kernel control |
| 46 | `/sbin/ifconfig` | svc `0x151a61` | interface configuration |
| 47 | `/usr/sbin/scutil` | svc `0x151ba7` | DNS replace/restore |
| 48 | `/usr/bin/dscacheutil -flushcache` | svc `0x151cbf` | DNS flush |
| 49 | `/usr/sbin/discoveryutil udnsflushcaches` / `mdnsflushcache` | svc `0x151ce0`, `0x151d08` | DNS flush |
| 50 | `/usr/bin/killall -HUP mDNSResponder` | svc `0x151d2f` | DNS flush |
| 51 | `/bin/cp -f` | svc `0x15169c` | helper self-install |

Embedded certificate AIA/CRL URLs (`ocsp.digicert.com`, `crl3.digicert.com`,
`secure.globalsign.com`, `ocsp.apple.com`, `crl.apple.com`) are static certificate material, not
application endpoints; they are listed in `analysis/strings_*.tsv` and excluded from the count.

**Endpoint count: 5 hosts + 16 control commands + 29 paths/literals = 50 anchored entries**
(≥20 required).

## 5. Control-plane semantics

### 5.1 Request shape

- `POST <base>/client.php`, `Content-Type: application/x-www-form-urlencoded` (`0x31e438`),
  `Connection: close` (`0x31d468`), libcurl + mbedTLS statically linked (`curl.se` doc strings,
  `mbedtls_*` symbols).
- Common parameters are built once at `appInit` into a map logged as
  `appInit: fetchedUrlHosts.size()=%d, builtinUrlHosts.size()=%d, appCommonParams.size()=%d`
  (`0x31e1a0`). Keys visible around the same block: `_site` (`0x318394`), `_lang` (`0x31839c`),
  `_os_lang` (`0x313777`), `_uid` (immediate `0x6469755f` at VA `0x51af9f`), plus the device
  fields listed in section 10.4.
- Host selection: `builtinUrlHosts` comes from `ServerApiUrls`; `loginSync` may return `ApiUrls`
  (`0x31e31c`) and `SyncVersion` (`0x31e2a4`), persisted as `FetchedUrlHosts` (`0x31e12c`) with
  `LastUrlHost` (`0x31e0dc`). Responses are cached under `CacheNetwork` and accepted only when the
  sync version matches (`ApiLoginSync, sync version changed…`, `0x31e328`).

### 5.2 Request signing (desktop)

- Headers `X-Sig: ` (`0x31e144`) and `X-Timestamp: ` (`0x31e150`); separator `|` at `0x31e140`.
- The signer is the same dual hash as the emulator: seeds `0xCAFEBABE`/`0xDEADBEEF`, `h2 ^= c +
  32*h2 + (h2>>2)`, `h1 = c + 65599*h1`, output interleaved high-byte-first. Function at VA
  `0x51ef90` (thunk `0x404dd6`), constants visible at VAs `0x51efa2/0x51efb9/0x51eff2`; header
  builder at VA `0x51f620-0x51fcb0` joins four parts with `|` and formats the two headers. The
  OpenAJS order is `secret|body|timestamp|secret`; the desktop builder has the same shape
  (`[INFERENCE]` on the exact operand order).
- The signing secret is **not** the OpenAJS 20-byte colon-hex secret (that literal is absent from
  the binary in both ASCII and UTF-16 form). It is passed into the signer at runtime; `[UNKNOWN]`.
- `06ka4z0dc6ga02c` (`0x31e160`, 15 chars) is loaded at VA `0x51b061` into a `std::string` and
  stored through `0x51f5f0` into `this+0x68` during `appInit`; it is **not referenced** by the
  signer (0x51f620-0x51fcb0) and is therefore not the X-Sig secret. Its role is `[UNKNOWN]`
  (candidate: a fixed client identifier stored on the session object).
- Linux 4.2.3.0 sends no `X-Sig`/`X-Timestamp` (`research/60-dynamic`); desktop 4.9.6.0 carries the
  machinery, so signing is at least available on desktop. Whether every call uses it is
  `[UNVERIFIED]`.

### 5.3 TLS behavior

- Control and FV transports use mbedTLS; the library string `Certificate verification was skipped`
  is present (Win `0x3592dc`, mac `0x20edc3`, helper `0x16c2a3`), and the live 4.2.3.0 capture
  showed the vendor leaf `CN=localhost` self-signed certificate being accepted
  (`research/82-live-20261023/REPORT.md`, F1). Static configuration call sites were not resolved;
  “verification disabled/custom” is `[INFERENCE]` backed by that live evidence, not a disassembly
  proof.

## 6. Protocol constants (identical core on both platforms)

The FV transport is the same in `AJiaSu.exe`, the macOS app and `vpnclientsvc`; offsets below are
file offsets in each binary. Cross-reference: `research/40-protocol/PROTOCOL.md` (Linux 4.2.3.0)
and `research/50-windows-protocol/FUNCTIONS.md` (function-level map).

| Constant | Windows | macOS app | vpnclientsvc | Notes |
|---|---|---|---|---|
| `OPV2` magic | `0x3216d0` | `0x1fe102` | `0x15b8c2` | v2 packet builder |
| `POST /s?s=%s&n=%s HTTP/1.1` | `0x3216d8` | `0x1fe107` | `0x15b8c7` | text/upgrade branch |
| `Upgrade: websocket` | `0x3216fc` | `0x1fe190` | `0x15b950` | text branch |
| `Sec-WebSocket-Key: %s` | `0x321750` | `0x1fe14f` | `0x15b90f` | text branch |
| `200 OK` / `code=200` | `0x321784` / `0x3217c4` | `0x1fe17b` / `0x1fe1cc` | `0x15b93b` / `0x15b98c` | success markers |
| `request response map is too large` | `0x321638` | `0x1fe084` | `0x15b844` | 800-byte field-map budget |
| `FV_CipherKeySize` | `0x3216b3` | `0x1fe0f1` | `0x15b8b1` | 16-byte key assert |
| `protocolVersion` | `0x32027c` | `0x1fdde4` | `0x159c32` | field name |
| `auth.password`, `auth.group`, `auth.forbidden`, `auth.membership`, `auth.limit`, `auth.off`, `session.timeout` | `0x31ef3c`-`0x31efa0` | `0x1fb997`-`0x1fb9e3` | `0x159ee9`-`0x159f35` | server error taxonomy |
| `id=%s;rv=%d;rp=%d;lt=%s` | `0x3203f8` | `0x1fde30` | — | ping report record (`ping.dat`) |
| FVRoute private ranges (`0.0.0.0/8` … `255.255.255.255/32`) | `0x320504`-`0x320604` | same core | same core | route exclusion table |
| RSA modulus (309 decimal digits, 1024-bit) | `0x314468` | none (GUI has OAEP code, no key) | `0x15188a` (x86_64), `0x2da5b7` (arm64) | same value as Linux/Android |
| RSA exponent `65537` | `0x3145dc` | — | `0x1519c0` | OAEP, MGF1, SHA-1 default |
| `prepare core = %s, protocolVersion = %d, implType = %d` | `0x31f120` | `0x1fb6dc` | `0x159c1f` | core init log |
| `buffer_obscure_packet key size must be FV_CipherKeySize` | `0x32168c` | `0x1fe0ca` | `0x15b88a` | key-size assert |

Verified locally: the modulus at Windows `0x314468` is 309 digits, `int(s).bit_length() == 1024`,
`e = 65537`; the macOS helper carries the byte-identical decimal string at `0x15188a`
(x86_64 slice) and `0x2da5b7` (arm64 slice). The macOS **GUI** binary has the OAEP code
(`mbedtls_rsa_import failed` `0x1fd0eb`, `RANDOM_GEN` `0x1fd0e0`) but **no** 309-digit decimal,
no hex string and no big-endian 128-byte form of the modulus; the only copy in the bundle is in
`vpnclientsvc`. This matches the ctl-socket parameter set (section 7.2) and indicates the FV VPN
handshake is performed by the root helper on macOS, not by the GUI `[INFERENCE]`. This also matches
`research/90-verification/VERDICTS.md` row 14 (1024-bit, not 2048-bit) and the Android lane's
finding that the key has not rotated.

Packet framing (from `FUNCTIONS.md` and re-checked against the literals above): v2 auth frame =
16-byte key block + 4-byte BE length + AES-128-CTR body with `s=`, `n=`, `k=`, `l=` and `_=` padding;
legacy/text branch uses `POST /s?…` with WebSocket upgrade headers; success is `200 OK`/`code=200`.
The proxy path captured live used the binary SOCKS5 gate, not `OPV2` (`research/60-dynamic`), so
`OPV2` is the VPN/control path.

`product.dat` crypto (both platforms): AES-128-CTR, key `MD5("[CONFIG]")`, zero IV — confirmed by
decryption of both copies (section 4.1).

## 7. Proxy, VPN and TAP capabilities

### 7.1 Windows

Modes (UI values `auto`, `proxy-tun`, `vpn`, `proxy-global`, `proxy-socks` in
`res.fvr:dialog-setting.htm:153-197`; dispatcher strings `proxy-tun`/`proxy-socks`/`proxy-global`
at `0x3135b4-0x3135d0`, `vpnConnect: unknown protocol %s` at `0x3135e0`):

| Mode | Mechanism | Evidence |
|---|---|---|
| `auto` | picks a mode from node data/ping results | UI only (`dialog-setting.htm:153`); selection logic `[UNKNOWN]` |
| `proxy-tun` (TCP) | TAP adapter + split default routes + DNS through tunnel | `\tap\tap`, `\tap\tap6` (`0x313584/0x31359c`); routes `0.0.0.0/1`+`128.0.0.0/1` (`0x319b7c/0x319b88`) |
| `vpn` (UDP) | FV VPN client over TAP | `FVClientVpn` RTTI (`0x39358c`), `vpn://`/`udp://` (`0x31905c`, `0x320274`) `[INFERENCE]` for the datagram mapping |
| `proxy-global` | WinINET system proxy to a local listener | `ProxyListenIp` (`0x314b78`), `proxy will listen at %s` (`0x314b88`), `sysproxy.exe` (`0x314e48`), `global localhost:` (`0x314df8`) |
| `proxy-socks` | local SOCKS5 listener | `FVClientProxySocks` (`0x323c78`), `socksIpPort` (`0x3187b4`), `SOCKS5: connecting to HTTP proxy %s port %d` (`0x3449b4`) |

Windows plumbing:

- **TAP install**: `tapinstall.exe` (`0x314a5c`) + `OemVista.inf` (`0x314ab4`) + `tap0901`
  (`0x31488c`) + `TAP-Windows Adapter V9` (`0x314a20`); commands `install "` (`0x314ad4`),
  `enable `/`disable ` (`0x314a80/0x314a94`); per-arch directories `\amd64\`, `\i386\`, `\arm64\`
  (`0x3149a4/0x3149b8/0x314990`). Installer itself does not install the driver; the app does.
- **Interface/DNS**: `netsh` (`0x3146b8`), `cmd: netsh %s` (`0x3146c8`), `interface ipv4 set
  address/dns`, `add dns`, `validate=no` (`0x3146e8-0x31483c`); adapter registry
  `SYSTEM\\CurrentControlSet\\Services\\Tcpip\\Parameters\\Interfaces` (`0x3148a0`), `NameServer`
  (`0x314938`), `EnableDHCP`/`DhcpIPAddress` (`0x315348/0x315324`); `ipconfig /flushdns`
  (`0x315180`) and `DnsFlushResolverCache` (`0x315164`).
- **Routes**: `GetIpForwardTable` field logging `dwForward*` (`0x314c70-0x314ddc`),
  `modify route table` / `restore route table` (`0x314b54/0x314c24`), `Route %s/%s failed: %d`
  (`0x314c3c`).
- **Firewall**: adds an allow rule via `INetFwPolicy2` (`0x3155e8`+) and the XP `HNetCfg.FwMgr`
  path (`0x315684`); messages `A new Windows Firewall rule has been added` (`0x315ac4`).
- **DNS cache service**: stops `Dnscache` and starts it again — `OpenSCManagerW(0xF003F)` →
  `OpenServiceW("Dnscache")` → `ControlService(STOP)` → wait → `StartServiceW` at VA `0x45d210`;
  helper `ensure_service_running` at VA `0x45d400` sets `AUTO_START` via `ChangeServiceConfigW`.
- **Auto-launch**: Task Scheduler COM (`ITaskService` strings `0x315d94`+), task names `AutoLaunch`
  (`0x313834`) and `PowerBoot` (`0x313850`), trigger/action/principal setup strings
  `0x313888-0x313c08` (`Error saving the Task`, `Success! Task successfully registered.`).
- **Local proxy helper**: `sysproxy.exe` usage `global <proxy-server> [<bypass>]`, `pac <pac-url>`,
  `off` (`0x18d50/0x18da8/0x18de0`), implemented with `InternetSetOptionW` and `RasEnumEntriesW`
  (`0x1a554/0x1a534`); bypass list `<local>;localhost;127.*;10.*;172.16.*…192.168.*`
  (`0x313e98-0x31406c`); `reset global proxy` (`0x314c0c`).
- **Privilege**: manifest `requireAdministrator`; `SeDebugPrivilege` enabled by a dedicated helper at
  VA `0x45c1a0` (`OpenProcessToken` → `LookupPrivilegeValueW` → `AdjustTokenPrivileges` with
  `SE_PRIVILEGE_ENABLED`).

### 7.2 macOS

Modes: `proxy-tun`, `proxy-socks`, `proxy-global` (`vpnclientsvc 0x15176a-0x151780`; app
`0x1f1524`, `0x1f38c5`); same `auto`/`vpn` UI values as Windows (`res.fvr` is shared).

- **Tunnel**: `vpnclientsvc` allocates a utun device via `socket(PF_SYSTEM, SOCK_DGRAM,
  SYSPROTO_CONTROL)` + `com.apple.net.utun_control` (`0x151f9e-0x151ffb`), configures it with
  `/sbin/ifconfig` (`0x151a61`), installs routes (`addroute failed: %s %s`, `0x151ac6`), and falls
  back from a local DNS listener to remote DNS (`use local dns server: 127.0.0.1`, `0x151add`).
- **DNS**: `scutil` reads `State:/Network/Global/PrimaryService`, backs up `ServerAddresses` under
  `/DNS-FVClientBak`, replaces `State:/Network/Service/<id>/DNS`, and restores on disconnect
  (`0x151c62-0x151f74`); flushes with `dscacheutil`, `discoveryutil`, `killall -HUP mDNSResponder`
  (`0x151cbf-0x151d2f`).
- **Global proxy**: `proxy_conf_helper` is installed setuid root by `install_helper.sh`
  (`sudo chown root:admin`, `chmod +s`; file `Contents/Resources/install_helper.sh`, header
  “shadowsocks / clowwindy”). Its CLI: `-m global|off`, `-t AJiaSu|FlyVPN`, `-p <port>`,
  `-l <socks5-listen-address>`, `-r <privoxy-port>`, `-s <privoxy-listen-address>`, `-x <exception>`,
  `-n <network-service>` (`0x8533`, `0x8610-0x872d`); it writes `kCFNetworkProxiesSOCKSEnable`,
  `kCFNetworkProxiesSOCKSPort`, `kCFNetworkProxiesSOCKSProxy` through
  `SCPreferencesCreateWithAuthorization` (`0xb3f1-0xb411`, `0xc33c`).
- **Helper control**: app↔`vpnclientsvc` over an AF_UNIX socket `/tmp/VpnClientCtl-<id>.sock`
  (app `0x1f37a7/0x1f37ba`, svc usage `%s /tmp/xxx.sock` at `0x1516cb`); the app sends
  `serverAddress`, `serverPort`, `userName`, `password`/`ConnectPassword`, `clientSite`,
  `clientVersion`, `clientUniqueId`, `osDeviceIdMap`, `routeMode`, `route`, `networkLock`,
  `clientExtra` (app `0x1f38d9-0x1f397f`; svc `0x1517a2-0x15182a`). Credentials therefore cross a
  local root-owned socket in cleartext `[INFERENCE]` from field names; the socket's mode was not
  observed.
- **Privilege**: app runs unprivileged; installs/launches the helper with
  `do shell script "/bin/bash \"%@\"" with administrator privileges` (`0x1eee4b`, `0x1f35a3`) via
  `NSAppleScript`; `AuthorizationExecuteWithPrivileges` is also referenced (`0x1f2b86`).
- **No** kernel extension, **no** `launchd` job, **no** NetworkExtension framework.

## 8. Update path

### 8.1 Windows self-update

Fields and literals (all in `AJiaSu.exe`): `checkUpdate` bridge (`0x317a70`), `[UpdateCheck:%s<=>%s]`
(`0x318558`), `LatestVersion` (`0x318588`), `UpdateOptional` (`0x318598`), `UpdateChecksum`
(`0x3130d0`), `UrlWeb` (`0x3184e8`) with log `%s update web site url = %s` (`0x318524`),
`/downloads/` (`0x318574`), arch selectors ` arm64`/`amd64` (`0x3131b4/0x3131bc`), version
`4.9.6.0` (`0x3131e4`), UI `window-download-update.htm` (`0x313164`), progress/cancel
`downloadUpdateGetProgress`/`downloadUpdateCancel` (`0x317538/0x317558`), failure strings
`Can not download file` (`0x31309c`), `Invalid installer` (`0x3130e4`),
`PromptCanNotExecuteUpdateFile` (`0x313140`).

Policy keys: `ClientUpdateRequired`/`ClientUpdateOptionalYesNo` (`0x318680/0x31869c`),
`UpdatePostponeVersion` (`0x3185fc`), `MaxUpdatePostponeDay` (`0x318618`), `LastUpdateCancelTime`
(`0x318634`), `ClientAlreadyLatestVersion` (`0x3185dc`). Post-update telemetry uses
`ClientApiUpgrade/UpgradeRecord` (`0x316b48`).

Flow `[INFERENCE]` from the literals: `checkUpdate` → response carries `LatestVersion`,
`UpdateOptional`, `UpdateChecksum`, `UrlWeb` → package fetched from `<UrlWeb>/downloads/<file>` →
checksum verified → installer executed. The exact request path/envelope is `[UNKNOWN]` (no
`ClientApiUpgrade/Check` literal exists; the only `Upgrade` command is the telemetry one).

### 8.2 macOS Sparkle

- Sparkle **2.3.0** (`Contents/Frameworks/Sparkle.framework/Versions/B/Resources/Info.plist`),
  with `Autoupdate`, `Updater.app`, `XPCServices/Downloader.xpc`, `XPCServices/Installer.xpc`;
  all re-signed by Team `N82WKSN6R7` with hardened runtime.
- The app references `SUUpdater` (`@_OBJC_CLASS_$_SUUpdater` at `0x232cf1`), selectors
  `setFeedURL:` (`0x1ed7f2`) and `checkForUpdates:` (`0x1eac49`). It keeps the same update state
  fields as Windows: `LatestVersion` (`0x1f1341`), `UpdateOptional` (`0x1f134f`),
  `ClientUpdateRequired` (`0x1f340d`), `UpdatePostponeVersion` (`0x1f33aa`),
  `MaxUpdatePostponeDay` (`0x1f33c0`), `LastUpdateCancelTime` (`0x1f33d5`).
- The bundle's `Info.plist` contains **no** `SUFeedURL`, `SUPublicEDKey` or `SUPublicDSAKey`; no
  feed URL literal exists in the binary. The feed is therefore supplied at runtime (most likely
  from the update response / `UrlWeb`) — `[INFERENCE]`; the URL itself is `[UNKNOWN]`.
- Sparkle 2.3.0 still allows Apple-code-signing-only updates but logs
  `Serving updates without an EdDSA key … is deprecated` (`Sparkle 0x50580`). With no EdDSA key in
  the bundle, update authenticity reduces to Apple code signing — and the Developer ID leaf expires
  **2026-10-30**. macOS has **no** `/downloads/` literal (unlike Windows).

## 9. Anti-debug, anti-tamper, privilege

### 9.1 Windows: CRT-only debugger checks, real privilege surface

`IsDebuggerPresent` is imported (IAT `0x79f3b0`) and called from five sites: VAs `0x4f4ca7`,
`0x4f612b`, `0x645c9c`, `0x6984c8`, `0x698564` (`objdump -d exe/AJiaSu.exe | grep 79f3b0`). Each is
inside the statically linked MSVC CRT:

- `0x4f4ca7` — `_invoke_watson` path: builds an `EXCEPTION_POINTERS` and raises
  `STATUS_FATAL_APP_EXIT` (`movl $0x40000015`), i.e. abort handling.
- `0x4f612b` — CRT assert/report path; `OutputDebugStringW` follows at `0x4f613a` with the ATL
  string `ERROR : Unable to initialize critical section in CAtlBaseModule` (`0x31cf38`).
- `0x645c9c` — SEH filter (`__except_handler`-style frame with `pushf`/segment saves).
- `0x6984c8`, `0x698564` — CRT debug-heap helpers (`setne bl` from the API result).

There is **no application branch** on the result (no “exit/alter behavior if debugger” path found),
and no anti-VM strings (VMware, VirtualBox, QEMU, SbieDll, Wine all absent).

Other dynamic API resolutions are OS-info, not anti-debug:

- `NtQueryInformationProcess` (`0x315b70`) is resolved in a CRT helper at VA `0x466a28`
  (`GetModuleHandleW("ntdll.dll")` → `GetProcAddress`) and called with a 24-byte buffer at
  `0x4669f7` — `PROCESS_BASIC_INFORMATION`, used for process/OS info. It is not called with
  `ProcessDebugPort`/`ProcessDebugFlags`.
- `ZwQuerySystemInformation` (`0x324e14`) is resolved by `FVSys` at VA `0x58838b`
  (`LoadLibraryW("ntdll.dll")` → `GetProcAddress` → class **3** = `SystemTimeOfDayInformation`,
  0x30-byte buffer) for OS time/version reporting.

What the Windows client *does* have:

- `SeDebugPrivilege` enabled at VA `0x45c1a0` (token → `LookupPrivilegeValueW` →
  `AdjustTokenPrivileges`, `Attributes=2`).
- Process enumeration/termination imports: `K32EnumProcesses`, `CreateToolhelp32Snapshot`,
  `Process32FirstW/NextW`, `OpenProcess`, `TerminateProcess`, `QueryFullProcessImageNameW`
  (`rabin2 -i`). Purpose not resolved; likely stale-helper cleanup `[UNKNOWN]`.
- Service control (`OpenSCManagerW`, `OpenServiceW`, `ControlService`, `StartServiceW`,
  `ChangeServiceConfigW`) used to bounce `Dnscache` (section 7.1).
- Crash reporting: `dbghelp` + `MiniDumpWriteDump` (`0x312e24/0x312e30`), dumps named
  `-%4d%02d%02d-%02d%02d%02d.dmp` (`0x312e48`) under `\\Crash` (`0x313340`); `*.dmp`/`*.log`
  (`0x31324c/0x313350`).
- No `WinVerifyTrust` import and no self-signature check; integrity checks cover only downloaded
  updates (`UpdateChecksum`) and cached network data (`md5 is incorrect, clear`, `0x31e5b0`).

### 9.2 macOS: no debugger checks, helper integrity checks

- No `ptrace`/`PT_DENY_ATTACH`, no `SecCode`/`SecStaticCode` self-verification imports, no
  anti-VM strings. `sysctl`/`sysctlbyname` are used for `hw.model` (`0x1f0de4`), not
  `KERN_PROC`.
- `proc_pidpath` and `kill` are imported (app `rabin2 -i`); purpose `[UNKNOWN]`.
- Hardened runtime (`flags 0x10000`) on every vendor binary; no entitlements beyond an empty dict
  (app/helper) and an empty `com.apple.application-identifier` (service).
- **Helper anti-tamper**: before launching, the app checks the installed `vpnclientsvc` against the
  bundled copy — `%s svc md5 mismatches, need install` (`0x1f3713`), `%s svc uid mismatches`
  (`0x1f3751`), `%s svc mod mismatches` (`0x1f3768`), `%s svc not exists, need install`
  (`0x1f36c7`). The same check exists for `proxy_conf_helper` via `helperPath`
  (`0x1eedc9-0x1eee30`).
- Translocation/quarantine handling: `Move to Applications folder in your Home folder?`
  (`0x1f26cc`), `/usr/bin/xattr -d -r com.apple.quarantine %@` (`0x1f2bf7`),
  `/usr/bin/xattr -d com.apple.quarantine %@` (`0x1f2c24`), `system.privilege.admin`
  (`0x1f2b6f`). The `xattr` commands strip the quarantine attribute (recursively in the first
  form) — a Gatekeeper bypass the app performs on itself/its helper `[INFERENCE]`.
- **Privilege surface**: `proxy_conf_helper` is installed setuid root (`chmod +s`,
  `install_helper.sh`); `vpnclientsvc` runs as root and receives account credentials over a local
  socket. Both are Developer-ID signed and pinned by designated requirement, but the setuid bit
  means any local user can execute the proxy helper.

### 9.3 Signing comparison

| | Windows | macOS |
|---|---|---|
| Authority | GlobalSign EV code signing | Apple Developer ID Application |
| Subject | 上海游盾网络科技有限公司 | Shanghai Youdun Network Technology Co., Ltd (`N82WKSN6R7`) |
| Validity | 2024-03-13 → 2027-03-14 | 2021-10-29 → **2026-10-30** |
| Covers | `AJiaSu.exe`, installer | app + all frameworks/helpers (CMS, hardened runtime) |
| Unsigned | `sciter.dll`, `sysproxy.exe`, `tap0901.sys` (amd64/i386) | none in the bundle (all re-signed) |
| Update trust | checksum in update metadata | Apple signature (+ no EdDSA key) |

## 10. Feature flags

### 10.1 Product flags (`product.dat`, both platforms)

`ProductShowLanguageSelect=false` (`0x0244`), `ProductShowAreaSelect=false` (`0x0265`),
`ProductShowRouteMode=false` (`0x0282`), `ProductShowAntiFraudTips=true` (`0x0315`),
`ProductServerFlagMode=text` (`0x02f9`), `ProductRegisterMode=mobile` (`0x0196`),
`ProductRegisterPasswordMinLength=8` (`0x01b2`), `ProductRegisterPasswordMaxLength=20` (`0x01d6`),
`ProductPriorityDisplayAccountName=mobile` (`0x01fb`), `MobileDefaultCountryNumber=86` (`0x0225`),
`ProductDefaultLanguage=zh_cn` (`0x0178`), `ClientSite=ajs` (`0x00d9`), `ClientDistChan=` empty
(`0x00e9`). Consequences: the desktop build hides language/area/route-mode selectors and the
anti-fraud tip is enabled.

### 10.2 User settings and server-driven flags

`ClientSettings[...]` keys (`0x316b70-0x316de8`): `SetType`, `Protocol`, `Reconnect`,
`NetworkLock`, `Datetime`, `ConnectOnLaunch`, `AutoLaunch`, `AdvancedRouteItem`,
`AdvancedRouteContent`. `User.*` keys: `VpnProtocol`, `AutoReconnect`, `NetworkLock`,
`ConnectOnLaunch`, `RouteMode` (`AllRoute`/`ExcludeRoute`/`IncludeRoute`/`CustomRoute`),
`RouteIdExclude`, `RouteIdInclude`, `CustomRoute`, `AreaCode`, `GroupCode`, `ServerId`,
`FavoriteServers`, `RecentlyUsedServers`, `RecentlySearchTexts`, `SettingsModifiedType/Datetime`,
`LastComparisonSettings/DeviceData/NetworkData/AppData`, `LastMessageTime`, `JsonAnnounceString`,
`LastUpdateCancelTime`.

UI values (`res.fvr:dialog-setting.htm`): protocol `auto` | `proxy-tun` | `vpn` | `proxy-global` |
`proxy-socks`; reconnect `once` | `continuously` | `sameCity` | `off`; `networkLock` on/off;
`autoLaunch`; `connectOnLaunch`. Localized semantics (`lang_zh_cn.txt:94-133`): `auto` = 自动模式,
`vpn` = 极速模式 (UDP), `proxy-tun` = 兼容模式 (TCP), `proxy-global` = 全局代理,
`proxy-socks` = 手动代理; network lock blocks local traffic on unexpected disconnect and disables
reconnect; continuous/same-city reconnect retries for up to 12 h.

Server-driven: `ApiIntervalMessage` (`0x319870`), `CheckInterval`/`CheckUrlKeyword`
(`0x31e84c/0x31e908`), `RouteSection` (`0x31e720`), `SyncVersion` (`0x31e2a4`), `ApiUrls`
(`0x31e31c`), `ServerLoad`/full-load (`0x319598`), `MarkFeature` (`0x319544`),
`LocationLevel`/`LocationCode` (`0x319eb0/0x319ec0`), `UpdateOptional`/`LatestVersion`.

### 10.3 Platform UI branches (same HTML, `PLATFORM` switch)

`res.fvr` is shared but the HTML contains explicit platform branches (`import { PLATFORM } from
"@env"`): `dialog-setting.htm:358-361` hides the auto-launch row (`#li-autoLaunch`) on `OSX`;
`window-main.htm:777-783` hides the logo/window buttons and switches search/account styling;
`window-main.htm:1253` hides the route-mode link on `OSX` regardless of `ProductShowRouteMode`;
`common-dialog.htm:41`, `dialog-about.htm:82`, `dialog-socks5.htm:168` map Enter (Windows) vs Space
(macOS) to dialog confirmation. These match the backend differences: macOS has no auto-start
mechanism and no route-mode UI, while the Windows binary carries Task Scheduler auto-start and the
route-mode implementation.

### 10.4 Device fingerprint sent with requests

Windows collects `biosId`, `volSn` (`VOL:%08X`, `0x315020`), `curMac`/`curBssid`/`curSsid`
(`0x3137c0-0x3137d8`), `procName` (`0x313804`), `osUname`, `sysVer`, `osLan`, `devName`,
`adptName`/`adptType`/`intranetIp`/`netmask` (`0x315c90-0x315cb8`), `_os_dev`, `_os_lang`,
`_os_ver`, `_os_did`, `_os_dim`, `e_100`, `e_104`, plus WMI `Win32_BIOS`
(`Manufacturer`/`Version`/`SMBIOSBIOSVersion`/`SerialNumber`, `0x314f6c-0x314ffc`) and WLAN APIs
(`wlanapi.dll`, `0x315bd8`). macOS collects `IOPlatformSerialNumber`/`IOPlatformUUID`
(`0x1f0c26/0x1f0c40`), `hw.model` (`0x1f0de4`), `curMac`/`curBssid`/`curSsid` (`0x1f0dc7-0x1f0dd7`),
`networksetup -getmacaddress %@` (`0x1f0e3f`), `UNKNOWN_MAC` fallback (`0x1f0ded`). The helper
also builds `osDeviceIdMap` from IOKit (`0x1517a2`, `0x15185c-0x151873`).

## 11. Windows vs macOS comparison

| Capability | Windows 4.9.6.0 | macOS 4.9.6 |
|---|---|---|
| UI engine | `sciter.dll` 4.4.8.33 (PE32) | `libsciter.dylib` 4.4.8.33 (universal) |
| UI resources | `res.fvr` 138 entries, `lang.fvr` | same entries; 8 of 9 file diffs are EOL-only, `dialog-setting.htm` has a macOS branch; `lang.fvr` identical |
| Control API | `POST <base>/client.php`, 16 commands | same commands, same base list |
| Host list | 4 bases in encrypted `product.dat` | same blob (identical hash) |
| API signing | dual hash with pipe separator, `X-Sig`/`X-Timestamp`; secret unknown | same code present; secret unknown |
| Tunnel device | TAP-Windows `tap0901` (OpenVPN) | utun (`com.apple.net.utun_control`) |
| Tunnel process | in-process (`FVClientVpn`) | root helper `vpnclientsvc` |
| System proxy | `sysproxy.exe` (WinINET + RAS): `global`, `pac`, `off` | `proxy_conf_helper` (SCPreferences SOCKS): `-m global` or `-m off` |
| DNS handling | `netsh` + `Dnscache` restart + `ipconfig /flushdns` | `scutil` replace/restore + `dscacheutil`/`discoveryutil`/`mDNSResponder` |
| Routes | `GetIpForwardTable` + split default routes | `route add` via helper |
| Privilege | app requires administrator; enables `SeDebugPrivilege` | app unprivileged; setuid proxy helper + root service |
| Auto-start | Task Scheduler task `AutoLaunch`/`PowerBoot` | none found (no launchd job) |
| Update | API-driven, `/downloads/`, checksum, runs installer | Sparkle 2.3.0, runtime feed URL, no EdDSA key |
| Anti-debug | CRT-only `IsDebuggerPresent`; no anti-VM | none; no ptrace/SecCode checks |
| Anti-tamper | update/cache checksums only | helper md5/uid/mode check, hardened runtime |
| Signing | GlobalSign EV, valid to 2027-03-14 | Developer ID `N82WKSN6R7`, expires 2026-10-30 |
| Data files | `config.ini`, `config.dat`, `ping.dat`, `CacheNetwork/`, `Crash/` | `config.txt`, `config.dat`, `ping.dat`, `CacheNetwork/` |
| Build stamps | app `20250709-103311`, core `20250708-1` | core `20250708-1`; CMS signing time `20250709030010Z` |
| Extra | firewall rule, WMI/WLAN fingerprint, process termination | ctl socket, setuid helper, Sparkle XPC services |

## 12. What this adds to the dashboard surface

- A complete desktop control-command catalog (16 commands) and the exact endpoint (`/client.php`)
  to model in a vendor simulator; the Android lane's `cmd=` convention holds for desktop too.
- The desktop signing scheme is confirmed as the emulator's dual hash with `|` separators; the
  missing piece is the desktop secret, which cannot be lifted statically.
- Mode/feature flags that a panel must expose or deliberately hide: five protocol values, four
  reconnect policies, network lock, auto-launch/connect-on-launch, route mode (hidden by
  `ProductShowRouteMode=false`), anti-fraud tips.
- Update policy differs by platform: Windows exposes download/checksum/arch/postpone fields;
  macOS delegates to Sparkle with a runtime feed and Apple-signature trust.
- Device identity fields are the same set the daemon already models (`UID`, `OSDID`, `CUID`,
  `CSEID`, platform/OS/version/dimension), with Windows WMI/WLAN and macOS IOKit/CoreWLAN sources.

## 13. Unknowns / not verified

1. Desktop `X-Sig` secret (not the OpenAJS colon-hex value; not the 15-char `06ka4z0dc6ga02c`,
   which is not referenced by the signer).
2. Role of `06ka4z0dc6ga02c`: stored at `this+0x68` during `appInit`; candidate fixed client
   identifier `[UNKNOWN]`.
3. Exact `checkUpdate` request path/envelope and whether `/downloads/` is joined to `UrlWeb` or to
   an API base `[INFERENCE]`.
4. macOS appcast URL and whether the app supplies an EdDSA public key at runtime; no
   `SUFeedURL`/`SUPublicEDKey` in the bundle.
5. `NetworkLock` implementation: only a state flag (`networkLock`, `switchStateStopped…`) was
   found; no firewall/pf rule construction tied to it.
6. macOS user data directory (`NSSearchPathForDirectoriesInDomains` used; no literal path found).
7. TLS verification configuration call sites (string + live behavior only).
8. `auto` mode selection logic (UI value only; `vpnConnect` handles the three explicit modes).
9. `[UNVERIFIED]`: any dynamic behavior — the Windows/macOS payloads were never executed, and the
   helper/service were not run.

## 14. Reproduction

```bash
# hashes and types
sha256sum exe/AJiaSu.exe exe/sciter.dll exe/sysproxy/*/sysproxy.exe \
          dmg/.../MacOS/AJiaSu dmg/.../MacOS/vpnclientsvc dmg/.../MacOS/proxy_conf_helper
rabin2 -I exe/AJiaSu.exe; rabin2 -I dmg/.../MacOS/AJiaSu
rabin2 -S exe/AJiaSu.exe

# product.dat plaintext (AES-128-CTR, key MD5("[CONFIG]"), zero IV)
python - <<'PY'
import hashlib
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
blob=open('exe/product.dat','rb').read()
pt=Cipher(algorithms.AES(hashlib.md5(b'[CONFIG]').digest()),
          modes.CTR(b'\x00'*16)).decryptor().update(blob)
print(pt.decode())
PY

# offsets
grep -abo "OPV2\|POST /s?\|X-Sig\|06ka4z0dc6ga02c\|/client.php" exe/AJiaSu.exe
grep -abo "setFeedURL:\|SUUpdater\|vpnclientsvc\|VpnClientCtl" dmg/.../MacOS/AJiaSu

# signatures
powershell -NoProfile -Command "Get-AuthenticodeSignature 'exe/AJiaSu.exe' | fl *"
openssl pkcs7 -inform DER -in app.cms -print_certs -noout   # CMS extracted from LC_CODE_SIGNATURE
```

Scratch evidence: `analysis/strings_*.tsv` (offset-tagged ASCII/UTF-16 strings),
`analysis/objdump_AJiaSu.txt` (1,627,573 lines), `analysis/mac_certs.pem`, `analysis/AJiaSu.cms`.
A `capa` run on `AJiaSu.exe` was started but did not finish within 900 s, so no capa result is
claimed here; the anti-debug conclusions come from imports, call-site disassembly and strings.
