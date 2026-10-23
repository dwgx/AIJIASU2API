# AJS-84/Linux — AJiaSu 4.3.0.1 static analysis (amd64, x86, aarch64)

Status: static, read-only, 2026-10-24. Slice `AJS-84/Linux`. **No sample was executed**, no
installer was run, no vendor host was contacted, no credential was used. Every claim below carries
a file offset, a VA, or a command output; `[INFERENCE]` = deduced from adjacent evidence,
`[UNKNOWN]` = not observable statically, `[UNVERIFIED]` = not exercised by this slice.

Scope: the three official Linux tarballs for `4.3.0.1` (amd64, x86, aarch64), compared against the
live-tested `4.2.3.0` amd64 build from `research/82-live-20261023` and `research/30-linux-static`.
This file is the only artifact written by this slice; scratch output (scripts, string dumps, xref
maps, disassembly) lives under `C:/Users/dwgx1/.dwgx/tmp/ajs-re-20261024a/work/linux84/` and is
not committed.

## 1. Bottom line

1. **The three tarballs are one source tree, three ISAs.** Same 4,861→4,876-string `.rodata`, same
   four control hosts, same FV constants, same identity code; every key literal has the same
   reference count in all three builds (section 4). Toolchain: `GCC: (GNU) 9.4.0`, **musl libc**
   (`MUSL_LOCPATH` at amd64 `0x2e4130`), fully static, stripped, non-PIE, no build-id, no symbol
   table (section 2).
2. **Control plane is unchanged in shape**: four HTTPS bases plus `POST /client.php` with the
   action in the body; only three `ClientApi…` action literals exist (`Login/Login`,
   `ClientSetting/LoadRoute`, `Report/WebsiteCheckReport`), byte-identical to 4.2.3.0. There is
   **no** separate loginSync action string — the sync is a second `ClientApiLogin/Login` call, which
   matches what the daemon already sends (`emulator/internal/api/client.go:441`).
3. **The request signature changed.** 4.3.0.1 computes `X-Sig` with an 8-byte dual 32-bit hash
   (seeds `0xDEADBEEF` / `0xCAFEBABE`, `h1 ^= (h1<<5)+c+(h1>>2)`, `h2 = h2*65599 + c`, interleaved
   big-endian digest, `|` separators) and hex-encodes 8 bytes → 16 hex chars; 4.2.3.0 used a 16-byte
   MD5 digest → 32 hex chars (section 4.3). The 4.3.0.1 scheme is the same one the desktop builds
   use (`WINMAC.md` §5.2).
4. **TLS verification is off on all three architectures**, by explicit libcurl options:
   `CURLOPT_SSL_VERIFYPEER=0` and `CURLOPT_SSL_VERIFYHOST=0`. No `CURLOPT_CAINFO`, no
   `CURLOPT_PINNEDPUBLICKEY`, no `CURLOPT_SSL_VERIFYSTATUS`, and the live-captured self-signed
   `CN=localhost` fingerprint is absent from all four Linux binaries. There is **no pin**; trust is
   disabled rather than pinned (section 6).
5. **The device descriptor is unchanged from 4.2.3.0**: `/etc/machine-id`, `/var/lib/dbus/machine-id`,
   `/sys/class/dmi/id/product_uuid`, `/proc/version`, plus `_site`, `_lang`=`en`, `_m`=`linux`,
   `_os_ver`, `_os_dev`, `_os_dim`=`lid=<…>` and the `_uid` form field. New in 4.3.0.1 is one fixed
   15-char literal `06ka4z0dc6ga02c` stored next to the `_uid` handling (section 7).
6. **Update is advisory only.** The login response field `LatestVersion` is printed as “A new
   version %s is available, please download from our web site.”; there is no download URL, no
   `/downloads/`, no checksum, no exec path, and no `91ajs` string in any of the three binaries
   (section 8). Windows self-updates and macOS uses Sparkle; Linux does neither.
7. **Real 4.3.0.1 deltas are narrow**: proxy authentication (SOCKS5 user/pass + HTTP Basic, weak
   password policy, refuse public bind without auth), a server-pushed API host list
   (`ApiUrls`/`FetchedUrlHosts`, absent in 4.2.3.0), the signing change, a rewritten listener, and
   the login/loginSync merge. The FV wire format is byte-identical (section 9).
8. **The dynamic plan is written but not run** (section 10): stop the `2261` daemon first, Xvfb +
   `tcpdump`, one account, one login, one FV session, with a lock-file guard, a hard stop list and a
   rollback table.

## 2. Samples and ELF metadata

### 2.1 Files

| arch | file | size | sha256 |
|---|---|---|---|
| amd64 | `linux-amd64/ajiasu` | 3,607,464 | `08fcd8668f7aaf6660277778e67fd2afd622c3f1c8fb634e57f31605bf01c8f6` |
| x86 | `linux-x86/ajiasu` | 3,387,912 | `35f5fa742559f4cf6123da197c0b21b808237c9d2a9b270ef7e6eade43e022ba` |
| aarch64 | `linux-aarch64/ajiasu` | 3,668,488 | `541db12104977339037fca8d971a68fee90a93981ad42961a7e94cde129cdcab` |
| baseline | 4.2.3.0 `ajiasu` | 3,578,792 | `29e18740af8b69c6fa4f7b0ef269316b01e0400d7768583fc08044632be13b9f` |

Extraction root: `C:/Users/dwgx1/.dwgx/tmp/ajs-re-20261024a/linux-{amd64,x86,aarch64}/ajiasu`
(tarballs listed in `TRIAGE.md` §S4–S6; each tarball holds exactly one file).

### 2.2 ELF table

`file(1)` on all three: `ELF … LSB executable, … statically linked, stripped`. `rabin2 -I` adds
`pic false`, `static true`, `stripped true`, `nx true`, `relro partial`, `canary false`,
`compiler GCC: (GNU) 9.4.0`.

| property | amd64 | x86 | aarch64 | 4.2.3.0 amd64 |
|---|---|---|---|---|
| class / machine | ELF64 / x86-64 | ELF32 / i386 | ELF64 / AArch64 | ELF64 / x86-64 |
| type | `ET_EXEC` (non-PIE) | `ET_EXEC` | `ET_EXEC` | `ET_EXEC` |
| entry | `0x40249c` | `0x804ac07` | `0x400d7c` | `0x40249c` |
| `main` (arg to `__libc_start_main`) | `0x40cfd6` | `0x8054fd7` | `0x40acb0` | `0x40aef4` |
| `.text` vaddr / file off / size | `0x400160` / `0x160` / 2,681,217 | `0x80480e0` / `0xe0` / 2,450,708 | `0x400170` / `0x170` / 2,809,356 | `0x400160` / `0x160` / 2,660,209 |
| `.rodata` vaddr / off / size | `0x68eb00` / `0x28eb00` / 351,920 | `0x829e600` / `0x256600` / 297,232 | `0x6adf90` / `0x2adf90` / 328,688 | `0x6898e0` / `0x2898e0` / 350,608 |
| `.eh_frame` size | 498,672 | 577,660 | 451,444 | 490,776 |
| `.data.rel.ro` size | 26,368 | 13,840 | 26,216 | 26,368 |
| `.gcc_except_table` size | 39,993 | 40,173 | 39,193 | 39,397 |
| FDEs (function-count proxy) | 14,750 | 14,768 | 14,618 | 14,509 |
| symbol tables | none | none | none | none |
| `PT_GNU_STACK` | RW (no X) | RW | RW | RW |
| `PT_GNU_RELRO` | 26,616 B | 14,540 B | 30,512 B | 26,616 B |
| build-id / `.note.*` | absent | absent | absent | absent |
| `.comment` | `GCC: (GNU) 9.4.0` | same | same | same |

Program headers (amd64, representative): `PT_LOAD R-X 0x400000 filesz 3,572,185`, `PT_LOAD RW
0x968808 filesz 32,448 memsz 69,280`, `PT_TLS 16 B`, `PT_GNU_STACK RW`, `PT_GNU_RELRO 26,616 B`.
No `PT_INTERP`, no `.dynamic`, no `.dynsym` — nothing is loaded at runtime.

### 2.3 libc and build

`MUSL_LOCPATH` (`0x2e4130` amd64, `0x2fdde0` aarch64) sits in musl's locale block next to
`C.UTF-8`, `LC_ALL`, `LC_TIME`; there is no glibc diagnostic string (`malloc():`, `GNU C Library`,
`__libc_` all zero hits). The binaries are **musl-static**, which is consistent with the absence of
`PT_INTERP`, with `MUSL_LOCPATH` being the only libc-marker string, and with the resolver strings
`Name does not resolve` / `/etc/resolv.conf` / `/etc/hosts`.

Embedded components are visible as source paths in `.rodata`:
`/work/fvclient/src/fvcore/json_reader.cpp` (`0x294c00`), `…/libevent/buffer.c` (`0x295bc8`),
`…/mbedtls/library/rsa.c` (`0x29bec8`), plus lwIP and libcurl strings. `[INFERENCE]` the build is
the vendor's `/work/fvclient` tree cross-compiled with GCC 9.4 against musl.

String population (`strdump.py`, printable runs ≥4 bytes over the whole file): 14,884 (amd64),
12,740 (x86), 8,638 (aarch64), 14,806 (4.2.3.0). The aarch64 count is lower because its `.rodata`
literals are zero-padded to 4-byte alignment and its `.text` carries far fewer inline ASCII runs —
`.rodata` string counts are comparable (6,117 / 6,131 / 6,155).

## 3. Version and build identity

| literal | amd64 vaddr / off | x86 off | aarch64 off | note |
|---|---|---|---|---|
| `4.3.0.1` | `0x6902b9` / `0x2902b9` | `0x257cb9` | `0x2afa08` | client version |
| `20260525-1` | `0x692860` / `0x292860` | `0x259ed4` | `0x2b22e0` | FV core build stamp (`cv`) |

Both are also stored as pointers in a 15-entry global table at `.data` `0x96f030` (file
`0x36f030`): `[0]=4.3.0.1`, `[1..3]` = net/dev fragments, `[4..13]` = `radius`, `auth.password`,
`auth.group`, `auth.forbidden`, `auth.membership`, `auth.limit`, `auth.off`, `session.timeout`,
`timeout`, `internal`, `[14]=20260525-1`. Code loads the table, not the literals, which is why a
naive string-xref scan reports zero references for the two version strings.

The `auth.*` strings are the vendor's failure taxonomy (same set in 4.2.3.0). They are useful for
the dashboard: a login/connect failure can be mapped to a stable reason instead of free text.

## 4. Control plane: hosts, endpoint, actions

### 4.1 Every network-relevant literal, with offsets

Offsets are file offsets unless marked `va`; the amd64 column also gives the VA. “ref fn” is the
FDE-bounded function that materialises the address in amd64 (`funcs.py`), so the reader can jump to
the consumer. The x86 and aarch64 columns prove the same literal exists in those builds (same
reference count; section 4.4).

| # | literal | role | amd64 va / file off | x86 off | aarch64 off | ref fn (amd64) |
|---|---|---|---|---|---|---|
| 1 | `https://login.ajs-app.com/` | control host 1 | `0x690214` / `0x290214` | `0x257c14` | `0x2af948` | `0x415154` |
| 2 | `https://login.mmegg.com/` | control host 2 | `0x69022f` / `0x29022f` | `0x257c2f` | `0x2af968` | `0x415154` |
| 3 | `https://login.mmegg.com:4443/` | control host 3 (live-used) | `0x690248` / `0x290248` | `0x257c48` | `0x2af988` | `0x415154` |
| 4 | `https://login.mbclubsh.com/` | control host 4 | `0x690266` / `0x290266` | `0x257c66` | `0x2af9a8` | `0x415154` |
| 5 | `/client.php` | control endpoint path | `0x6932d9` / `0x2932d9` | `0x25a8ad` | `0x2b2ee8` | `0x4450f0`, `0x445424`, `0x4471fe`, `0x4489b6` |
| 6 | `ClientApiLogin/Login` | login **and** loginSync action | `0x6932e5` / `0x2932e5` | `0x25a8b9` | `0x2b2ef8` | `0x4450f0` |
| 7 | `ClientApiClientSetting/LoadRoute` | route download action | `0x6935f8` / `0x2935f8` | `0x25aba4` | `0x2b3240` | `0x4471fe` |
| 8 | `ClientApiReport/WebsiteCheckReport` | URL-keyword telemetry action | `0x693878` / `0x293878` | `0x25ae14` | `0x2b3508` | `0x4489b6` |
| 9 | `App:UserLogin` | login log tag | `0x68f3a5` / `0x28f3a5` | `0x256e89` | `0x2ae960` | `0x40a13c` |
| 10 | `App:HttpRequest` | request log tag | `0x693801` / `0x293801` | `0x25ada1` | `0x2b3490` | `0x4480d6`, `0x4489b6` |
| 11 | `LoginSync` | cache key / log tag (used with `App:`) | `0x693059` / `0x293059` | `0x25a639` | `0x2b2c20` | `0x442fe8` |
| 12 | `POST /s?s=%s&n=%s HTTP/1.1` | FV proxy handshake request line | `0x69455d` / `0x29455d` | `0x25b9ad` | `0x2b4260` | `0x456468` |
| 13 | `Upgrade: websocket` | FV handshake header | `0x69457a` / `0x29457a` | `0x25b9ca` | `0x2b4280` | `0x456468` |
| 14 | `Connection: Upgrade` | FV handshake header | `0x69458f` / `0x29458f` | `0x25b9df` | `0x2b4298` | `0x456468` |
| 15 | `Content-Length: %d` | FV handshake header | `0x6945a5` / `0x2945a5` | `0x25b9f5` | `0x2b42b0` | `0x456468` |
| 16 | `Sec-WebSocket-Key: %s` | FV handshake header | `0x6945ba` / `0x2945ba` | `0x25ba0a` | `0x2b42c8` | `0x456468` |
| 17 | `code=200` | FV handshake reply marker | `0x69460f` / `0x29460f` | `0x25ba5f` | `0x2b4338` | `0x456908` |
| 18 | `OPV2` | FV handshake tag | `0x694558` / `0x294558` | `0x25b9a8` | `0x2b4258` | `0x455ae1` |
| 19 | `ipv4only.arpa` | NAT64 detector DNS name | `0x693e56` / `0x293e56` | `0x25b30a` | `0x2b3a88` | `0x4505b0` |
| 20 | `/etc/ajiasu.conf` | default config path | `0x6901f8` / `0x2901f8` | `0x257bf8` | `0x2af920` | `0x415133` |
| 21 | `/config.dat` | per-instance state file | `0x68fecc` / `0x28fecc` | `0x257968` | `0x2af5f0` | `0x40cfd6` (main) |
| 22 | `/CacheNetwork/network-` | cached route blob prefix | `0x69340f` / `0x29340f` | `0x25a9d3` | `0x2b3040` | `0x446862` |
| 23 | `/etc/machine-id` | device-id source | `0x68f2d1` / `0x28f2d1` | `0x256db9` | `0x2ae830` | `0x408b79` |
| 24 | `/var/lib/dbus/machine-id` | device-id source | `0x68f2e1` / `0x28f2e1` | `0x256dc9` | `0x2ae840` | `0x408b79` |
| 25 | `/sys/class/dmi/id/product_uuid` | device-id source | `0x68f300` / `0x28f300` | `0x256de4` | `0x2ae868` | `0x408b79` |
| 26 | `/proc/version` | OS descriptor source | `0x68f322` / `0x28f322` | `0x256e06` | `0x2ae898` | `0x408b79` |
| 27 | `/proc/sys/kernel/random/uuid` | random UUID source | `0x698240` / `0x298240` | `0x25eecc` | `0x2b7980` | `0x48afb8` |
| 28 | `/etc/resolv.conf` | VPN-DNS rewrite target | `0x68ebec` / `0x28ebec` | `0x2566e8` | `0x2ae108` | `0x407a8e`, `0x407e3e`, `0x407f99` |
| 29 | `/resolv.conf.backup` | VPN-DNS backup file | `0x68ebd8` / `0x28ebd8` | `0x2566d4` | `0x2ae0f0` | `0x407a29` |
| 30 | `/dev/net/tun` | VPN modes only | `0x68eb31` / `0x28eb31` | `0x256631` | `0x2ae008` | `0x4026be` |
| 31 | `/etc/ssl/certs/ca-certificates.crt` | libcurl default CA path (unused, see §6) | `0x69d178` / `0x29d178` | `0x26383c` | `0x2bc230` | `0x4cf76e` |
| 32 | `127.0.0.1:1080` | default `proxy-local-server` | `0x68fe8f` / `0x28fe8f` | `0x25792b` | `0x2af5a0` | `0x40cfd6` (main) |
| 33 | `ApiUrls` | server-pushed host list (new) | `0x693211` / `0x293211` | `0x25a7e9` | `0x2b2e18` | `0x444b42`, `0x4493d4` |
| 34 | `FetchedUrlHosts` | persisted remote host list (new) | `0x693044` / `0x293044` | `0x25a624` | `0x2b2c08` | `0x442fe8` |
| 35 | `LastUrlHost` | last-good host (both versions) | `0x693008` / `0x293008` | `0x25a5e8` | `0x2b2bc0` | `0x442fe8` |
| 36 | `X-Sig: ` | request signature header | `0x693065` / `0x293065` | `0x25a645` | `0x2b2c38` | `0x4436e5` |
| 37 | `X-Timestamp: ` | request timestamp header | `0x69306d` / `0x29306d` | `0x25a64d` | `0x2b2c40` | `0x4436e5` |
| 38 | `application/x-www-form-urlencoded` | POST content type | `0x693328` / `0x293328` | `0x25a8f4` | `0x2b2f38` | `0x445424` |
| 39 | `ClientUniqueId` | cached client-id key | `0x693014` / `0x293014` | `0x25a5f4` | `0x2b2bd0` | `0x442fe8` |
| 40 | `DeviceUniqueId` | cached device-id key | `0x693023` / `0x293023` | `0x25a603` | `0x2b2be0` | `0x442fe8` |
| 41 | `_uid` | login form field | `0x69307b` / `0x29307b` | `0x25a65b` | `0x2b2c50` | `0x443b8a` |
| 42 | `LoginToken` | login response field | `0x69363a` / `0x29363a` | `0x25abe6` | `0x2b32b0` | `0x4476dc` |
| 43 | `AuthResult` | login response field | `0x68fcd6` / `0x28fcd6` | `0x257776` | `0x2af388` | `0x40ca3e`, `0x4476dc` |
| 44 | `ResponseLogin` | sync response field | `0x693203` / `0x293203` | `0x25a7db` | `0x2b2e08` | `0x444b42` |
| 45 | `SyncVersion` | sync response field | `0x69319c` / `0x29319c` | `0x25a774` | `0x2b2d90` | `0x4446d4`, `0x44496a`, `0x444b42` |
| 46 | `LatestVersion` | update-notice field | `0x68fc73` / `0x28fc73` | `0x257717` | `0x2af328` | `0x40ca3e` |
| 47 | `UrlWeb` | vendor site field | `0x68fc5e` / `0x28fc5e` | `0x257702` | `0x2af310` | `0x40ca3e` |
| 48 | `4.3.0.1` | client version | `0x6902b9` / `0x2902b9` | `0x257cb9` | `0x2afa08` | data table `0x96f030` |
| 49 | `20260525-1` | core build stamp | `0x692860` / `0x292860` | `0x259ed4` | `0x2b22e0` | data table `0x96f030` |
| 50 | `proxy-local-server` | proxy bind config key (new) | `0x68fe7c` / `0x28fe7c` | `0x257918` | `0x2af588` | `0x40cfd6` (main) |
| 51 | `sys-env-id` | identity config key (new as key) | `0x68fd9e` / `0x28fd9e` | `0x25783e` | `0x2af480` | `0x40cfd6` (main) |
| 52 | `cache-dir` | cache config key (renamed) | `0x68fe61` / `0x28fe61` | `0x2578fd` | `0x2af560` | `0x40cfd6` (main) |
| 53 | `06ka4z0dc6ga02c` | fixed 15-char literal near `_uid` (new) | `0x693080` / `0x293080` | `0x25a660` | `0x2b2c58` | `0x443b8a` |
| 54 | `%08x%02x%08x` | id-obfuscation format (changed) | `0x693402` / `0x293402` | `0x25a9c6` | `0x2b3030` | `0x44655a` |

Rows 1–8 and 12–18 are the complete network surface of the binary: a hostname-like-string scan of
the whole amd64 file finds only the four bases plus `ipv4only.arpa`; there is no telemetry host, no
update host and no third-party SDK endpoint.

### 4.2 Host list and failover

`0x415154` is a tiny function that builds a four-element `std::array<std::string,4>` on the stack
(stride `0x20`) from the four literals — one `mov esi, <literal>` per element at `0x415182`,
`0x4151ad`, `0x4151d8`, `0x415203`. This is the only place the host literals are referenced, in
all three builds (x86 `0x805c173`, aarch64 `0x412c8c`).

Runtime selection is stateful:

- `LastUrlHost` (`0x293008`) is read at `appInit` (`0x442fe8`) from the persisted config, so the
  client remembers the last host that worked. Where it is written back is not resolved
  `[UNVERIFIED]`; only the read reference exists in `.text`.
- `FetchedUrlHosts` (`0x293044`, **new**) is a persisted list merged with the built-in four; the
  startup log `appInit: fetchedUrlHosts.size()=%d, builtinUrlHosts.size()=%d, appCommonParams.size()=%d`
  (`0x2930b0`) shows the two lists are kept separately.
- `ApiUrls` (`0x693211`, **new**) is read from the sync response in `0x444b42` (JSON member lookup
  at `0x444e72`, then `0x4493d4` merges it into the live list). So a vendor response can add hosts
  after the first login.
- Which host is tried first, and whether `:4443` is preferred, is **not** statically decidable
  `[UNVERIFIED]`; the 4.2.3.0 live capture saw `login.mmegg.com:4443` (`research/82`, row 5).

`[INFERENCE]` the failover order is the array order (ajs-app → mmegg → mmegg:4443 → mbclubsh) with
`LastUrlHost` tried first; this is consistent with the persisted key but was not exercised.

### 4.3 Login and loginSync are one code path in 4.3.0.1

Evidence chain (amd64):

- The CLI `login` handler is `0x40a13c`: it logs `App:UserLogin` (`0x40a1c8`) and calls `0x4450f0`
  at `0x40a1eb`.
- `0x4450f0` logs `appUserLoginSync: accountName=%s` (`0x44512f`), builds `/client.php`
  (`0x44520a`) and `ClientApiLogin/Login` (`0x445285`), then calls the login request executor
  `0x4476dc`.
- The sync response handler `0x444b42` parses `SyncVersion`, `ResponseLogin`, `ApiUrls`,
  `SectionsChanged` and saves the cache; it is called from `0x449c29`.
- RTTI in `.rodata` confirms the merge: 4.3.0.1 keeps `*ZN11FVNetClient15appApiLoginSync…` and
  `…16httpRequestStart…`, while `…10appWelcome…` and `…12appUserLogin…` are **gone** (present in
  4.2.3.0 at `0x28e380`/`0x28e420`).
- There is exactly one action literal for login (`ClientApiLogin/Login`); a raw byte search for
  `ClientApiLoginSync` finds nothing in any of the four Linux builds. The daemon already models
  loginSync as a second call with `trigger=login` to the same action
  (`emulator/internal/api/client.go:441`), which is consistent.

So there is **one action, not two**: login and the sync refresh share `ClientApiLogin/Login`.
`[UNVERIFIED]` whether 4.3.0.1 still issues the refresh as a second call or consumes the sync
payload from the first response; the cache keys (`LoginSync`, `SyncVersion`, `_sync[` at
`0x2931fa`) are still present either way.

### 4.4 Request signing changed in 4.3.0.1

`X-Sig` is produced in `0x4436e5` (headers at `0x4438ba` and `0x4438ff`). The digest function is
`0x4435c5`:

```
0x4435d5  mov dword [rbp-4], 0xdeadbeef      ; h1
0x4435dc  mov dword [rbp-8], 0xcafebabe      ; h2
loop per byte c:
0x443634  eax = h1 << 5
0x443640  edx = eax + c
0x443645  eax = h1 >> 2
0x443648  eax = eax + edx
0x44364a  h1 ^= eax                          ; h1 ^= (h1<<5) + c + (h1>>2)
0x443651  edx = h2 << 6
0x443657  edx += c
0x44365c  eax = h2 << 16
0x443661  eax += edx
0x443664  h2 = eax - h2                      ; h2 = h2*65599 + c
0x44367c..0x4436c3  interleave 4 bytes of h1 and 4 bytes of h2, big-endian
0x4436d2  mov edx, 8                         ; 8-byte digest
0x4436da  call 0x43ec0e                      ; hex encoder ('0'-'9','a'-'f')
```

`0x43ec0e` builds the digit table at `0x970d20` and emits two nibbles per byte, so `X-Sig` is
**16 lowercase hex characters**. The canonical string is assembled with `|` (literal at
`0x693063`, immediately before `X-Sig: `) and the timestamp is emitted separately as
`X-Timestamp: `.

4.2.3.0 differs: its signing function (`0x44486c`) calls a one-shot MD5 wrapper at `0x4b30df`
(init/update/final around the MD5 constants `0x67452301`/`0xefcdab89`/`0x98badcfe`/`0x10325476`)
and formats **16 bytes** (`mov edx, 0x10` at `0x4449a2`) → 32 hex chars. The constants
`0xDEADBEEF`/`0xCAFEBABE`/`65599` do not occur anywhere in the 4.2.3.0 `.text`.

This is the same dual-hash scheme documented for the desktop builds in `WINMAC.md` §5.2, so the
dashboard should implement one signer, not two. The exact field order inside the `|`-joined
canonical string is `[UNVERIFIED]`; the desktop report’s order is the best available prior.

### 4.5 Cross-architecture parity

Reference counts for the same literals are identical in all three builds, at equivalent code
locations:

| literal | amd64 | x86 | aarch64 |
|---|---|---|---|
| host 1 | 1 @ `0x415182` | 1 @ `0x805c173` | 1 @ `0x412c8c` |
| `/client.php` | 4 @ `0x44520a`… | 4 @ `0x8086b7d`… | 4 @ `0x440efc`… |
| `ClientApiLogin/Login` | 1 @ `0x445285` | 1 @ `0x8086bfa` | 1 @ `0x440f5c` |
| `OPV2` / `POST /s` / `code=200` | 1 / 1 / 1 | 1 / 1 / 1 | 1 / 1 / 1 |
| `X-Sig` | 1 @ `0x4438b2` | 1 @ `0x808537e` | 1 @ `0x43fa2c` |
| `ApiUrls` | 2 | 2 | 2 |
| `LatestVersion` | 2 | 2 | 2 |
| `/etc/machine-id` | 1 @ `0x408c25` | 1 @ `0x8050836` | 1 @ `0x40716c` |

`[INFERENCE]` the three binaries are the same sources with no arch-specific `#ifdef` around the
network paths; the only structural difference is code generation (aarch64 `.text` is 128 KB larger
because ADRP/ADD sequences replace x86 RIP-relative operands).

## 5. FV data-plane constants

### 5.1 Proxy handshake

Built in `0x456468`, tag checked in `0x455ae1`, reply parsed in `0x456908`:

```
0x294558  OPV2
0x29455d  POST /s?s=%s&n=%s HTTP/1.1\r\n
0x29457a  Upgrade: websocket\r\n
0x29458f  Connection: Upgrade\r\n
0x2945a5  Content-Length: %d\r\n
0x2945ba  Sec-WebSocket-Key: %s\r\n
0x29460f  code=200
```

The `\r\n` are part of the literals (verified in the raw bytes at `0x29455d`..`0x2945d0`); the
blank-line terminator is a separate literal at `0x2945d0` (`\r\n\0`).

Frame field tokens (`strings -n 2`): `_=` `0x29451b`, `s=` `0x2945d5`, `n=` `0x2945d8`, `k=`
`0x2945db`, `l=` `0x2945de`. Same five tokens as 4.2.3.0 (`0x28eddb`, `0x28ee95`, `0x28ee98`,
`0x28ee9b`, `0x28ee9e`). Transport is plain TCP: the request is written to the node socket, no TLS
context exists on this path (the only TLS in the binary is libcurl/mbedTLS on the control plane).

### 5.2 Auth field table is byte-identical

The contiguous literal table at `0x29054f`..`0x2905b8` (105 bytes) is **byte-for-byte equal** to
4.2.3.0’s `0x28af6f`..`0x28afd8` (compared with a direct `==` over the two byte ranges):

```
ok \0 sid \0 sip \0 0.0.0.0 \0 cip \0 dns \0 , \0 ka \0 kat \0 kapf \0 mtu \0 ft \0 fm \0
t \0 c \0 au \0 u \0 p \0 cuid \0 cseid \0 st \0 v \0 cv \0 osp \0 osd \0 osv \0 dim \0 peip \0 ce \0
```

`[INFERENCE]` (consistent with `research/40-protocol` and OpenAJS): `sid` session id, `sip` server
virtual IP, `cip` client virtual IP, `dns` pushed resolver, `ka`/`kat`/`kapf` keep-alive
interval/timeout/flag, `mtu` tunnel MTU, `ft`/`fm` failure type/message, `au` auth user,
`cuid`/`cseid` client/device ids, `st` site, `cv` core version, `osp`/`osd`/`osv` platform/device/OS
version, `dim` device model, `peip` peer egress IP, `ce` client extra. The one-letter entries
(`t`, `c`, `u`, `p`, `v`) are present in both builds and remain `[UNKNOWN]`.

### 5.3 Crypto constants

| item | amd64 off | x86 off | aarch64 off | note |
|---|---|---|---|---|
| RSA-1024 modulus (309 decimal digits) | `0x28f548` | `0x257018` | `0x2aeb40` | identical in 4.2.3.0 at `0x28a030` |
| public exponent `65537` | `0x28f67e` | — | — | same literal in all builds |
| `mbedtls_rsa_import failed` | `0x292f8a` | — | — | RSA-OAEP key unwrap path |

The modulus is the same 309-digit literal as OpenAJS `FvCrypto.serverPublicKey()` and the desktop
builds, so the session-key wrap is unchanged: RSA-1024-OAEP-SHA1 over the 16-byte FV key, AES-128-CTR
body (`buffer_obscure_packet key size must be FV_CipherKeySize`, `0x294520`).

### 5.4 Control verbs and packet path

`con` `0x29259f`, `ping` `0x2925a3`, `pong` `0x2925a8`, `off` `0x2925ad`, `sid` `0x292642`, `aur`
`0x292646`; `send CommandCode_Ctrl auth %s ...` `0x292460`, `recv CommandCode_NetPkt: size=%d`
`0x2925f0`, `recv CommandCode_Ctrl: cmd=%s, all:` `0x292618`, `write dev to net pkt` `0x292650`.
These belong to the VPN control loop (`udp|tcp|lwip`), not to the proxy path — same split as
4.2.3.0.

## 6. TLS and certificate behaviour

### 6.1 Verification is explicitly disabled

The app’s HTTP client configures libcurl in one function per arch. `curl_easy_setopt` was identified
by its varargs prologue and its NULL-handle return (`mov eax, 0x2b` = 43 `CURLE_BAD_FUNCTION_ARGUMENT`):

| arch | setopt fn | `SSL_VERIFYPEER` (64) | `SSL_VERIFYHOST` (81) |
|---|---|---|---|
| amd64 | `0x4ef286` | `0x439d95 mov edx,0` / `0x439d9a mov esi,0x40` / `0x439da7 call` | `0x439dbf mov edx,0` / `0x439dc4 mov esi,0x51` / `0x439dd1 call` |
| x86 | `0x811a266` | `0x807cc81 push 0` / `0x807cc83 push 0x40` / `0x807cc86 call` | `0x807cca5 push 0` / `0x807cca7 push 0x51` / `0x807ccaa call` |
| aarch64 | `0x4fa040` | `0x435f44 mov w2,#0` / `0x435f48 mov w1,#0x40` / `0x435f4c bl` | `0x435f60 mov w2,#0` / `0x435f64 mov w1,#0x51` / `0x435f68 bl` |

The complete option set the app applies (amd64, 19 app-level call sites out of 47 total; the rest
are libcurl-internal) is: `VERBOSE=0`, `FOLLOWLOCATION=1`, `ACCEPT_ENCODING=""`,
`CONNECTTIMEOUT`, `WRITEFUNCTION`, `WRITEDATA`, `HEADERFUNCTION`, `HEADERDATA`, `ERRORBUFFER`,
`NOPROGRESS=0`, `SOCKOPTFUNCTION`, `XFERINFODATA`, `SSL_VERIFYPEER=0`, `SSL_VERIFYHOST=0`,
`HTTPPOST`, `POSTFIELDS`, `POSTFIELDSIZE`, `HTTPHEADER`, `URL`. `CURLOPT_CAINFO` (10065),
`CURLOPT_PINNEDPUBLICKEY` (10230), `CURLOPT_SSL_VERIFYSTATUS` (232), `CURLOPT_PROXY_SSL_VERIFYPEER`
(248) are **never** set, in either version.

### 6.2 No pin material

- The live-captured leaf fingerprint (`E883F0B9…B73A89`, `research/82` F1) is absent as raw bytes,
  as uppercase hex and as lowercase hex in all four Linux binaries (`bytes.find` = -1).
- `sha256//` (`0x2a2c38`), `public key hash: sha256//%s` (`0x2a2c41`) and
  `SSL public key does not match pinned public key` (`0x2ab288`) are libcurl feature strings only;
  the pin option is never invoked.
- The two `/etc/ssl/certs/ca-certificates.crt` literals (`0x29d178`, `0x2a1bc0`) are libcurl’s
  default CA path for its mbedTLS backend; with `VERIFYPEER=0` the file is never consulted.

### 6.3 Consequence

The client accepts any certificate on the control plane: self-signed, wrong hostname, expired. That
is exactly what the 4.2.3.0 live run observed (self-signed `CN=localhost` on all four hosts) and it
remains true in 4.3.0.1. For the dashboard this means **do not model a pin**; model “verification
disabled” and keep the pin only as an optional daemon-side hardening knob (`AJS_CONTROL_CERT_SHA256`
in `daemon/internal/account/tls.go`), which is stricter than the shipping client.

## 7. Device identity

`0x408b79` builds the descriptor. Sources and literals (amd64):

| item | offset | note |
|---|---|---|
| `uname` failure text | `0x28f2b9` | `Can not read uname: %d` |
| `/etc/machine-id` | `0x28f2d1` | first choice |
| `/var/lib/dbus/machine-id` | `0x28f2e1` | fallback |
| `/sys/class/dmi/id/product_uuid` | `0x28f300` | appended when readable |
| `/proc/version` | `0x28f322` | OS descriptor |
| `_site` | `0x28f334` | region/site key |
| `_lang` + `en` | `0x28f33a` / `0x28f340` | language |
| `_m` + `linux` | `0x28f343` / `0x28f346` | machine/platform |
| `_os_ver` | `0x28f34c` | OS version |
| `_os_dev` | `0x28f354` | OS device string |
| `lid=` | `0x28f35e` | prefix for the local id token |
| `_os_dim` | `0x28f363` | carries `lid=<value>` |
| `_uid` | `0x29307b` | login form field, built in `0x443b8a` |
| `06ka4z0dc6ga02c` | `0x293080` | new fixed 15-char literal stored on the client object (`0x4493a6` writes `this+0xb0`) |

The same literals exist in 4.2.3.0 at `0x289e1c`..`0x289e4b`, so the descriptor format did not
change. `[UNVERIFIED]` the exact transform that turns the machine-id/product-uuid/`/proc/version`
concatenation into the `lid=` value (the buffer is accumulated at `[rbp-0x660]` and formatted by
`0x43fcac`; a hash is plausible but not proven).

The FV auth side carries the same identity as separate fields (`cuid`, `cseid`, `osp`, `osd`,
`osv`, `dim`, `peip`). Three warnings make the failure mode explicit:
`empty client 'dim', can not connect to server` (`0x2905b8`),
`empty client unique id, this may cause vpn connection broken by mistake online kick` (`0x2905e8`),
`empty client sysenv id, …` (`0x290640`). `sys-env-id` is now also a config key (`0x28fd9e`,
default CLI-only in 4.2.3.0) and is the documented way to avoid kicking another client.

## 8. Update mechanism

Advisory only. The login-result printer `0x40ca3e` reads `UrlWeb` (`0x28fc5e`, printed as
`Web Site: %s`, `0x28fc65`) and `LatestVersion` (`0x28fc73`), then prints
`\x1b[0;36mA new version %s is available, please download from our web site.\x1b[0m\n`
(`0x28fc88`). There is no comparison against `4.3.0.1` and no fetch: the branch only tests whether
the JSON field is non-empty (`0x40cb6e` → `0x461700` → `0x4604f8`) before printing.

Negative evidence, all three binaries:

- no `91ajs`, `download`, `/downloads/`, `UpdateChecksum`, `OnlineUpdate`, `AppUpdate` literal
  (the only `update` hits are libcurl/`dns nameserver` strings);
- no `chmod`/`execv`/`popen`/`rename`/`.new`/`.tmp` string in the app `.rodata` range
  (`0x28e000`–`0x296000`);
- no second curl client profile: every app-level `curl_easy_setopt` call site is inside the two
  config functions of the single HTTP client (RTTI `12FVHttpClient` at `0x692df8`), and their
  option set is exactly §6.1;
- no file-write path next to `LatestVersion`: the file-writing helpers in the response path are the
  cache helpers `appSaveCachedResponse` (`0x293378`) and `appSaveCachedNetworkData` (`0x293428`).

So the update “mechanism” is: vendor tells the client a version string, the client tells the user to
go to the website. Windows (`/downloads/` + `UpdateChecksum`) and macOS (Sparkle) do more; Linux
does not. A dashboard should surface `LatestVersion`/`UrlWeb` as metadata, not as an update action.

## 9. Diff 4.2.3.0 → 4.3.0.1

Baseline: the live-tested amd64 4.2.3.0 (`29e18740…`, core `20241105-1`). All numbers below are
from the same extractor run on both binaries.

### 9.1 Shape

| metric | 4.2.3.0 | 4.3.0.1 | delta |
|---|---|---|---|
| file size | 3,578,792 | 3,607,464 | +28,672 (+0.80%) |
| `.text` | 2,660,209 | 2,681,217 | +21,008 (+0.79%) |
| `.rodata` | 350,608 | 351,920 | +1,312 (+0.37%) |
| `.eh_frame` | 490,776 | 498,672 | +7,896 (+1.61%) |
| FDEs (function proxy) | 14,509 | 14,750 | +241 (+1.66%) |
| `.rodata` strings ≥5 chars | 4,861 | 4,876 | +15 net (53 new, 38 gone) |
| entry / `main` | `0x40249c` / `0x40aef4` | `0x40249c` / `0x40cfd6` | `main` moved |

The `main` move is layout, not design: `main` still parses `/etc/ajiasu.conf`, the `-c` override and
`--key=value` options and dispatches `login|list|logout|connect`.

### 9.2 String delta (app `.rodata` only, ≥5 chars, exact-set difference)

53 new, 38 removed. Grouped by feature:

| new string (4.3.0.1 off) | meaning |
|---|---|
| `Usage: ./%s [--option=...] {login|list|logout}` `0x28ed68` | CLI accepts `--key=value` |
| `More and advanced help: ./%s -h` `0x28edd0` | help split (`-h` only) |
| `Config file options can also be passed via command line arguments…` `0x28ee95` | same |
| `cache-dir` `0x28fe61`, `up-down` `0x28fe9e`, `log-level` `0x28fea6`, `use-vpn-dns` `0x28feb2`, `use-vpn-route` `0x28febe` | config keys renamed `_` → `-` |
| `proxy-local-server` `0x28fe7c`, `127.0.0.1:1080` `0x28fe8f` | configurable listener |
| `sys-env-id` `0x28fd9e` | was CLI-only |
| `proxy server should not be exposed without auth, see the help` `0x290c18` | refuses public bind without auth |
| `proxy server password is weak, use at least 10 characters with both letters and digits…` `0x2909a8` | password policy |
| `force to use a randomly generated password: %s` `0x290a18` | auto password |
| `socks5 client username&password check fails` `0x291978` | SOCKS5 user/pass auth |
| `proxy-authorization:` `0x2919ab`, `basic` `0x291a27`, 4 × `invalid http proxy Proxy-Authorization…` | HTTP Basic proxy auth |
| `unable to listen on %s:%d` `0x290ca3`, `unable to get listener port` `0x290c87` | listener rewrite |
| `ApiUrls` `0x293211`, `FetchedUrlHosts` `0x293044`, `appInit: fetchedUrlHosts.size()…` `0x2930b0` | remote host list |
| `4.3.0.1` `0x2902b9`, `20260525-1` `0x292860` | version stamps |
| `06ka4z0dc6ga02c` `0x293080` | fixed literal (purpose `[UNKNOWN]`) |
| `%08x%02x%08x` `0x293402` | id-obfuscation format changed from `%08x%04x%08x` |

| removed string (4.2.3.0 off) | note |
|---|---|
| `More help: ./%s -h` `0x289b48` | replaced by “More and advanced help” |
| `Usage: ./%s {login|list|logout}` `0x289b60`, `Usage: ./%s connect "Shanghai 1*"` `0x289b88` | replaced by `[--option=...]` forms |
| `--help` `0x28a872` | no standalone flag; only inside prose `0x28fde0` |
| `Can not open config file: %s` `0x28a892` | replaced by `Unable to load account user…` `0x28fde0` |
| `cache_dir` `0x28a8ea`, `up_down` `0x28a905`, `log_level` `0x28a90d`, `use_vpn_dns` `0x28a919`, `use_vpn_route` `0x28a925` | renamed (no aliases kept) |
| `UserLogin` `0x28d813`, `Welcome` `0x28d81d`, `welcome` `0x28d911`, `WebsiteTest` `0x28dec9` | **string-pool artefacts, not API actions** — see 9.8 |
| `appUserLogin: accountName=%s` `0x28d91b` | login merged into sync |
| `appInit: urlHosts.size()=%d…` `0x28d850` | split into builtin/fetched |
| `%08x%04x%08x` `0x28dbca` | replaced |
| `getsockname failed` `0x28b5c7`, `new/bind failed` `0x28b5da`, `tcp listener ` `0x28b5ea` | listener rewrite |
| four `%s handshake: socks4/socks5/http-proxy …` `0x28c270`..`0x28c358` | replaced by the auth-aware handshake strings |
| RTTI `appUserLogin` `0x28e380`, `appWelcome` `0x28e420` | functions gone |

### 9.3 CLI and config

4.3.0.1 (literals, not executed here; the verbatim `-h` transcript from execution on 2026-10-23 is
in `research/30-linux-static/REPORT.md` §“4.3.0.1 delta”):

```
Usage: ./%s [--option=...] {login|list|logout}
Usage: ./%s [--option=...] connect "Shanghai 1*"
More and advanced help: ./%s -h
  user = your-account-name
  pass = your-password
  cache-dir = /tmp
  connect = server-name (wildcard is supported)
  protocol = {udp|tcp|lwip|socks5|http|proxy}
  use-vpn-dns = {yes|no} ; use-vpn-route = {yes|no}
  up-down = ./dir/your-script-file.sh
  proxy-local-server = local-username:local-password@ip:port
  sys-env-id = ...
```

The proxy listener still falls back to a fixed `uint16` array when `proxy-local-server` is absent:
`0x41e0b1` `movw 0x438` (1080), `0x41e0b7` `0x49c8` (18888), `0x41e0bd` `0x70d8` (28888),
`0x41e0c3` `0x97e8` (38888), terminator at `0x41e0c9`, five-element vector (`r15d = 5` at
`0x41e0d6`). Same list as 4.2.3.0 (`0x41a3c0`), different code.

### 9.4 Proxy authentication is the main new feature

- SOCKS5: user/pass sub-negotiation, failure text `0x291978`; the handshake can switch to SOCKS5
  when the client sends auth (`handshake: switch to socks5 mode by socks5 auth (buf=0x%02x%02x...)
  and reply no auth`, `0x291920`).
- HTTP: `Proxy-Authorization: Basic …`, with explicit errors for missing/malformed/non-Basic/base64
  failures (`0x2919c0`..`0x291b1a`).
- Binding: a non-loopback listener without credentials is refused (`0x290c18`); weak passwords
  (<10 chars or missing letters+digits) trigger a generated password (`0x2909a8`, `0x290a18`).
  `[UNVERIFIED]` whether “loopback” is decided by address family/prefix at bind time.

### 9.5 Remote host list is new (correction to `TRIAGE.md` §7)

`ApiUrls` and `FetchedUrlHosts` are **absent** from 4.2.3.0 (`grep -c` = 0 over its full string
dump) and present in 4.3.0.1 (`0x293211`, `0x293044`). `LastUrlHost` exists in both. The `TRIAGE.md`
row that lists `ApiUrls`/`FetchedUrlHosts` as “yes” for the 4.2.3.0 baseline is wrong; the remote
host refresh is a 4.3.0.1 addition. Everything else in that row (SyncVersion, X-Sig) matches.

### 9.6 Signing change (summary; detail in §4.4)

| | 4.2.3.0 | 4.3.0.1 |
|---|---|---|
| digest | MD5 one-shot `0x4b30df`, 16 bytes | dual 32-bit hash `0x4435c5`, 8 bytes |
| seeds / constants | MD5 IVs | `0xDEADBEEF`, `0xCAFEBABE`, ×65599 |
| separator | none found near the headers | `|` at `0x693063` |
| `X-Sig` length | 32 hex chars | 16 hex chars |

`[INFERENCE]` this is a compatibility break for any client that signs requests: a 4.2.3.0-style
MD5 signature will not match what 4.3.0.1 sends. The daemon currently signs with the OpenAJS scheme
(`emulator/internal/api/sign.go`); it must be checked against §4.4 before being used with 4.3.0.1.

### 9.7 Unchanged

- Four control hosts, `/client.php`, the three action literals, `X-Sig`/`X-Timestamp` headers,
  `application/x-www-form-urlencoded`.
- FV handshake (`OPV2`, `POST /s?s=&n=`, `Upgrade: websocket`, `Sec-WebSocket-Key`, `code=200`),
  frame tokens `s=/n=/k=/l=/_=`, auth field table (byte-identical), RSA modulus and exponent,
  AES-CTR body, control verbs.
- Device descriptor sources and suffixes; `_uid`, `ClientUniqueId`, `DeviceUniqueId`.
- TLS: `VERIFYPEER=0`/`VERIFYHOST=0`, no pin, no CAINFO.
- Update: notify-only.
- Cache layout: `/config.dat`, `vpnconfig`, `/CacheNetwork/network-`, `CachePassword`, `.dat`,
  MD5 integrity check (`md5 is incorrect, clear`).
- NAT64 detector (`ipv4only.arpa`, `c000:aa`/`c000:ab`).

### 9.8 Corrections to earlier reports

1. `research/30-linux-static/REPORT.md` §“4.3.0.1 delta” says the action literals `UserLogin`,
   `Welcome`, `welcome`, `WebsiteTest` were **removed**. They were log-tag/JSON-key fragments, not
   actions: `App:UserLogin` still exists as a single literal at `0x28f3a5`, and the only `ClientApi`
   action literals in either version are the three in §4.1. The RTTI removal of `appUserLogin` /
   `appWelcome` is real; the “action removed” reading is not.
2. The same report says `ClientApiLoginSync` is the sync action. No such byte string exists in any
   of the four Linux binaries; loginSync reuses `ClientApiLogin/Login`.
3. `TRIAGE.md` §7 marks `ApiUrls`/`FetchedUrlHosts` as present in the 4.2.3.0 baseline; they are
   not (§9.5).
4. `TRIAGE.md` §4.4 calls the binaries “unsigned” (correct for code signing) but does not note the
   request-signing change; §4.4/§9.6 above is the missing half.

## 10. Safe dynamic-test plan (written, **not run**)

Goal: confirm on the wire what §4–§9 predicts for 4.3.0.1 — the new `X-Sig` is accepted, the login
response carries the sync payload, `cv=20260525-1` is accepted, the listener binds where
`proxy-local-server` says, and the FV handshake is unchanged — using **one scratch account**, one
login and one data session.

Hard rules for the run:

- **One account, one session.** The vendor tears down an FV session when the same account
  authenticates another (`research/82` F3, `fm=disconnected by another connection`). Nothing else
  may hold this account: stop the `2261` daemon first (step 1), and do not run the dashboard
  against the same account during the window.
- **No production account.** `live-ajs-01` stays out of this run; ask the Owner for a scratch
  account. Credentials are typed into the client’s own prompt or a mode-600 config, never argv,
  never the transcript.
- **Static-first, then dynamic.** Do not patch the binary, do not set a TLS MITM proxy, do not
  install kernel modules. Capture only.
- **One login attempt per run.** A rejected credential is not retried (playbook §7 stop list).

### 10.1 Preconditions (no vendor traffic)

| # | check | pass condition |
|---|---|---|
| P1 | sample integrity | `sha256sum` of the lab copy equals `08fcd866…` (§2.1); copy is mode 500 in a lab dir, not the download dir |
| P2 | daemon stopped | `ss -ltnp` shows **no** listener on `127.0.0.1:2261`; no `ajs-accountd` process |
| P3 | no other client | `pgrep -af ajiasu` empty; no other host/VM logged into the scratch account |
| P4 | capture tooling | `scripts/debug/capture.sh --dry-run` compiles `filters/control.bpf`; `tshark`/`tcpdump` present |
| P5 | lab dirs | `$LAB/{cache,cap,out}` exist, mode 700; disk ≥ 2 GB free |

### 10.2 Numbered run

1. **Stop the 2261 daemon and prove it.** `systemctl --user stop ajs-accountd` (or kill the PID from
   the recorded pidfile); then `ss -ltnp | grep ':2261 '` must print nothing and
   `curl -sS --max-time 2 http://127.0.0.1:2261/healthz` must fail to connect. Record the exact
   restart command now, for rollback: `AJS_LISTEN=127.0.0.1:2261 … ./ajs-accountd` (playbook §5
   step 1).
2. **Freeze the sample.** `install -m 500 linux-amd64/ajiasu "$LAB/ajiasu-4301"`; record
   `sha256sum` before and after the run; run only `$LAB/ajiasu-4301`.
3. **Write a lab-only config, mode 600** (`$LAB/ajiasu.conf`), never `/etc/ajiasu.conf`:
   ```
   user = <scratch account>
   pass = <from the operator env, written by a helper, not echoed>
   cache-dir = <$LAB/cache>
   protocol = proxy
   proxy-local-server = 127.0.0.1:11080
   log-level = debug
   ```
   `protocol=proxy` keeps the run root-free and avoids `/dev/net/tun`, route edits and
   `/etc/resolv.conf` rewrites entirely (all three strings are only reachable from the VPN branch,
   §4.1 rows 28–30).
4. **Start Xvfb and a console recorder.** `Xvfb :99 -screen 0 1280x800x24 &` and
   `DISPLAY=:99 xterm -e "script -q -f $LAB/out/console.log" &` (or `tmux` + `script`). The binary
   itself has **no X11 dependency** (0 hits for `DISPLAY`/`X11`/`xcb`/`wayland` in all three
   builds); Xvfb exists only so the lab console and any GUI capture tooling have a display.
5. **Start the control capture before any vendor contact.**
   `scripts/debug/capture.sh --filter-file scripts/debug/filters/control.bpf --name ajs4301-control --iface any --rotate-seconds 300 --ring 24 --out "$LAB/cap"`.
   The filter covers `tcp 443/4443` to the three resolvable control hosts; `login.mmegg.com:4443`
   is the expected one. Files land mode 600 and are hashed at stop.
6. **Arm the one-session guard.** A wrapper (`$LAB/guard.sh`) that: (a) takes `flock` on
   `$LAB/run.lock`, (b) aborts if `pgrep -af ajiasu` is non-empty, (c) tails the console transcript
   and aborts if a second `Login Result:` line appears, (d) kills the client after 600 s. Run every
   client invocation through it: `$LAB/guard.sh -- <cmd>`.
7. **Login — exactly once.** `$LAB/guard.sh -- $LAB/ajiasu-4301 -c $LAB/ajiasu.conf login`.
   Record exit code, the `Login Result:` line, membership/expiry, and whether the response carried
   `LatestVersion`/`UrlWeb`. Expected: `Login Result: OK`; `[UNVERIFIED]` whether a second
   `ClientApiLogin/Login` call appears in the capture (the merge question in §4.3).
8. **List servers (no write).** `$LAB/guard.sh -- $LAB/ajiasu-4301 -c $LAB/ajiasu.conf list`;
   pick one node and record its id/name.
9. **Connect once.** `$LAB/guard.sh -- $LAB/ajiasu-4301 -c $LAB/ajiasu.conf connect "<node>"`.
   Verify `ss -ltnp | grep 11080` shows the listener on `127.0.0.1:11080` and nothing on the
   fallback ports (1080/18888/28888/38888).
10. **One egress probe.** `curl -sS --max-time 20 --socks5-hostname 127.0.0.1:11080 https://ifconfig.me`
    and the same against `https://ipinfo.io/ip`; record both IPs. Two requests, no more.
11. **Stop the session.** Send `SIGINT` to the client (its handler prints `Signal: %d, stop & exit`,
    `0x28f47c`); wait for exit; confirm no listener on 11080 and no `ajiasu` process.
12. **Stop the capture** (`capture.sh --stop`), hash the pcap files, and slice the control traffic
    to the four hosts (`slice_pcap.py`) so only vendor packets remain in the review copy.
13. **Extract the comparison facts**: SNI and leaf certificate (expect the same self-signed
    `CN=localhost`), whether `X-Sig` is 16 hex chars (4.3.0.1) or 32 (4.2.3.0), whether
    `cv=20260525-1` appears in the FV auth, and the handshake request line.
14. **Tear down and restore.** Delete `$LAB/cache`; if (and only if) a VPN-mode run was done, restore
    `/etc/resolv.conf` from `/resolv.conf.backup` and remove any `ip route` added by the client;
    restart the `2261` daemon with the command recorded in step 1; verify `/healthz`.

### 10.3 One-session guard (minimum implementation)

```sh
#!/bin/sh
# $LAB/guard.sh — refuses a second client, a second login, or a hung run
set -eu
exec 9>"$LAB/run.lock"; flock -n 9 || { echo "guard: another run holds the lock" >&2; exit 3; }
[ -z "$(pgrep -af '[a]jiasu' || true)" ] || { echo "guard: client already running" >&2; exit 3; }
( sleep 600; pkill -f "$LAB/ajiasu-4301" ) & watchdog=$!
trap 'kill $watchdog 2>/dev/null' EXIT
before=$(grep -c 'Login Result:' "$LAB/out/console.log" 2>/dev/null || echo 0)
"$@"; rc=$?
after=$(grep -c 'Login Result:' "$LAB/out/console.log" 2>/dev/null || echo 0)
[ "$after" -le $((before + 1)) ] || { echo "guard: more than one login" >&2; exit 4; }
exit $rc
```

### 10.4 Rollback table

| action | consequence | rollback |
|---|---|---|
| stop `ajs-accountd` on 2261 | dashboard loses its account backend for the window | restart with the step-1 command; verify `/healthz` |
| client writes `$LAB/cache` | state, cached LoginSync, cached routes | `rm -rf "$LAB/cache"` (lab path only) |
| `tcpdump -i any` | may capture unrelated host traffic; disk growth | stop capture, delete the pcap, narrow to `host` clauses |
| proxy listener on 11080 | local port held | `SIGINT` the client; `ss` check; kill by PID if needed |
| `protocol=udp/tcp/lwip` (not planned) | edits routes and `/etc/resolv.conf` | restore `/resolv.conf.backup`, remove the added route, `ip link` check |
| credential in a log/pcap | account risk | delete artifact, rotate password, tell the Owner |
| second login by accident | vendor kicks the first session (F3) | stop immediately, wait one minute, re-run with one login |

### 10.5 Stop conditions

Stop and report, do not retry: a credential appears in any artifact; the vendor returns
401/403/429/5xx twice; a second FV session or `fm=disconnected by another connection` appears; the
capture contains traffic outside the four hosts and the one node; the client hangs or the watchdog
fires; the Owner says stop.

### 10.6 Evidence to keep

`sha256` of the binary before/after, pcap files + hashes, console transcript (phone number redacted),
`ss` output before/after, the two egress IPs, the login result lines, and a one-page diff note against
the 4.2.3.0 live run. Pcaps stay in the lab tree (mode 600, gitignored) and are deleted when the
Owner closes the slice unless asked to retain.

## 11. Open questions

- `06ka4z0dc6ga02c` (`0x293080`): stored on the client object next to `_uid`, no hash or network use
  observed. `[UNKNOWN]`
- Exact `lid=` transform in `_os_dim`: the buffer is assembled from machine-id/product_uuid/
  `/proc/version` at `[rbp-0x660]` and formatted by `0x43fcac`; a hash is plausible. `[UNVERIFIED]`
- Canonical string field order for `X-Sig`: the `|` separator and the digest are proven, the order
  of the joined fields is not. `[UNVERIFIED]`
- Whether 4.3.0.1 still sends a second `ClientApiLogin/Login` for sync or consumes the sync payload
  from the first response. `[UNVERIFIED]` (needs the §10 capture)
- Host failover order and whether `:4443` is preferred. `[UNVERIFIED]`
- Semantics of the one-letter FV auth keys `t/c/u/p/v` (present in both builds). `[UNKNOWN]`
- `proxy-local-server` parsing edge cases (IPv6 literal, missing port, credentials with `@`).
  `[UNVERIFIED]`
- Whether the vendor accepts an unsigned request, the MD5 signature, or the new dual-hash signature
  for `cv=20260525-1`. `[UNVERIFIED]` — this is the highest-value question for the dashboard.
- ARM32 (`armeb`/`armel`) tarballs published on the download page were not analysed (not in this
  slice’s sample set).

## 12. Self-audit

| # | claim | verdict | evidence |
|---|---|---|---|
| 1 | Three builds are one source tree | **CONFIRMED** | identical literal sets, identical ref counts, same FDE/rodata layout (§4.5) |
| 2 | Static, stripped, non-PIE, musl | **CONFIRMED** | `rabin2 -I` + pyelftools sections + `MUSL_LOCPATH` `0x2e4130` (§2) |
| 3 | Four control hosts + `/client.php` + three actions | **CONFIRMED** | literals + xrefs, 1:1 across arches (§4.1) |
| 4 | loginSync reuses `ClientApiLogin/Login` | **CONFIRMED (literal absence + code path)** | no `ClientApiLoginSync` byte string; `0x4450f0` calls the login action; RTTI merge (§4.3) |
| 5 | `X-Sig` changed from MD5-16B to dual-hash-8B | **CONFIRMED** | `0x4435c5` seeds/loop vs `0x44486c`→`0x4b30df` MD5 + `edx=0x10` (§4.4) |
| 6 | TLS verification disabled on all three arches | **CONFIRMED** | `VERIFYPEER=0`/`VERIFYHOST=0` instruction sites (§6.1) |
| 7 | No certificate pin | **CONFIRMED** | no `CURLOPT_PINNEDPUBLICKEY`, fingerprint bytes absent in all four builds (§6.2) |
| 8 | Device descriptor unchanged vs 4.2.3.0 | **CONFIRMED** | same literals at `0x289e1c`..`0x289e4b`; same code shape (§7) |
| 9 | Update is notify-only | **CONFIRMED** | `LatestVersion` print path; no download/exec primitives (§8) |
| 10 | Remote host list is new in 4.3.0.1 | **CONFIRMED** | `ApiUrls`/`FetchedUrlHosts` absent in 4.2.3.0 dump, present in 4.3.0.1 (§9.5) |
| 11 | FV wire format unchanged | **CONFIRMED** | 105-byte auth table byte-equal; same tokens/RSA/`OPV2` (§5) |
| 12 | Proxy auth is new | **CONFIRMED (strings + code)** | 10 new literals, handshake branch `0x291920` (§9.4) |
| 13 | `lid=` derivation | **NOT CLAIMED** | `[UNVERIFIED]` (§7, §11) |
| 14 | Failover order / second sync call | **NOT CLAIMED** | `[UNVERIFIED]`, §10 will answer |

Denominators: string counts are over full-file printable runs (≥4 bytes) or `.rodata` runs ≥5 bytes,
as stated per table; the 53/38 delta is an exact set difference over `.rodata` strings ≥5 chars
between the two amd64 binaries. FDE counts are from `.eh_frame` (14,750 / 14,768 / 14,618 / 14,509);
they are a function-count proxy, not a symbol count.

## 13. Reproduction

All analysis is read-only; no sample was executed. Scripts (scratch, not committed) live in
`C:/Users/dwgx1/.dwgx/tmp/ajs-re-20261024a/work/linux84/`:

```sh
# ELF headers, sections, segments, FDE count, notes
python work/linux84/elfmeta.py linux-amd64/ajiasu linux-x86/ajiasu linux-aarch64/ajiasu

# printable strings with vaddr + file offset (n>=4 and n>=2 variants)
python work/linux84/strdump.py linux-amd64/ajiasu 4 work/linux84/str-amd64.tsv
python work/linux84/strdump.py linux-amd64/ajiasu 2 work/linux84/str2-amd64.tsv

# cross-references (RIP-relative / imm32 for x86, ADRP+ADD/LDR for aarch64)
python work/linux84/xrefs.py linux-amd64/ajiasu work/linux84/xref-amd64.json

# address -> enclosing FDE, and linear disassembly of a range
python work/linux84/funcs.py linux-amd64/ajiasu 0x4435c5
python work/linux84/funcs.py linux-amd64/ajiasu 0x4435c5 0x4436e5

# enumerate curl_easy_setopt options at every call site
python work/linux84/setopts.py linux-amd64/ajiasu 0x4ef286
python work/linux84/setopts.py linux-x86/ajiasu 0x811a266
```

Second-tool confirmation of the ELF shape:

```sh
rabin2 -I linux-amd64/ajiasu   # static true, stripped true, pic false, relro partial, GCC 9.4.0
```

Scratch artifacts kept for review: `elfmeta.json`, `str-{amd64,x86,aarch64,4230}.tsv`,
`str2-{amd64,4230}.tsv`, `xref-{amd64,x86,aarch64,4230}.json`, `dis-identity.txt`, `dis-sig.txt`,
`dis-uid.txt`, `dis-update.txt`, `appstrings-4301.txt`, `curlopt-8.5.0.json`.

Cross-references: `research/82-live-20261023/REPORT.md` (live 4.2.3.0 baseline, F1/F3),
`research/30-linux-static/REPORT.md` (4.2.3.0 static + 4.3.0.1 string-level delta),
`research/40-protocol/PROTOCOL.md` (FV framing), `research/81-protocol-gaps/REPORT.md` (gaps),
`research/84-ajs-496/TRIAGE.md` (sample provenance), `research/84-ajs-496/WINMAC.md` §5.2 (desktop
signing scheme), `research/80-debugger-prep/PLAYBOOK.md` (lab/capture conventions).
