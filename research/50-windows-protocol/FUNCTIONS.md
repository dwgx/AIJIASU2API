# Windows 4.9.6.0 — function map

Binary: `AJiaSu.exe`, 4,064,640 B, PE32 x86, image base `0x400000`,
sha256 `0fd9d702bf9932836ad0ba3e4acf43bb1a587c45ee6247b7a5d88aede78101f6`.
All addresses are **virtual addresses** (file offset = VA − 0x400000 + 0x400 for `.text`,
− 0x400000 + 0x311c00 for `.rdata`, − 0x400000 + 0x38d000 for `.data`).

Method: Ghidra 12.1.3 headless (21,598 functions, 301 functions decompiled from 298
transport-related string xrefs), plus a `pefile`+`capstone` cross-check of every call site and
of the MSVC RTTI chain (type descriptor → `_RTTICompleteObjectLocator` → vtable).

Column *evidence*: `S:<addr>` = string xref to that literal; `V:<addr>` = recovered vtable
(COL-resolved); `C:<addr>` = direct call site; `T:<addr>` = ILT thunk (`jmp` trampoline) that the
callers actually call, because the binary is `/INCREMENTAL`-linked.

## Recovered functions

| # | address | name (recovered) | role | evidence | confidence |
|---|---------|------------------|------|----------|------------|
| 1 | 0x00448f40 | `vpnConnect` | reads the route-mode string and picks `proxy-tun` / `proxy-socks` / `proxy-global`; sets mode 1 (tun, flag +0xa6) or mode 2 (socks/global, flag +0xa7); logs `vpnConnect: unknown protocol %s` | S:0x7149b4, S:0x7149c0, S:0x7149d0, S:0x7149e0 | high |
| 2 | 0x0048d730 | `DesktopEngine::bootProduct` | loads `res.fvr` and `product.dat` via `FUN_004925f0`, calls the product.dat decryptor `FUN_00520dc0`, then parses the `[CONFIG]` INI (`ServerApiUrls`, `ClientSite`, …) | S:0x7191fc, S:0x719218, S:0x71925c, S:0x719494 | high |
| 3 | 0x004f8ac0 | `crc32` | zlib CRC-32 (table at `0x78f3a8`), used for the 4-byte key-block checksum of the OPV2 frame | C:0x552017 inside 0x551f40 | high |
| 4 | 0x00520830 | `md5_kdf` | mbedtls-MD5 over the literal string `"[CONFIG]"`; output = 16-byte AES key for `product.dat` | C:0x520df0; 88-byte context = `mbedtls_md5_context` | high (verified by decryption) |
| 5 | 0x00520df0 | `product_dat_decrypt` | `KDF("[CONFIG]")` → `mbedtls_aes_setkey_enc(128)` → `mbedtls_aes_crypt_ctr(len, IV=0)` in place | C:0x48d9d0 inside `bootProduct` | high (verified) |
| 6 | 0x00520f90 | `rsa_oaep_helper` | `mbedtls_rsa_import` + `mbedtls_rsa_complete` + `mbedtls_rsa_pkcs1_decrypt`; both bignums are parsed from decimal/hex strings with `strtoul`-style conversion | S:`mbedtls_rsa_import failed`, S:`mbedtls_rsa_complete failed` | medium |
| 7 | 0x00526560 | `prepare_core` | logs `prepare core = %s, protocolVersion = %d, implType = %d`; reads **protocolVersion at this+0x10** and **implType at this+0x50**; writes the literal `"OAEP"`; allocates `FVClientWrapper`/client object | S:0x720520, S:0x720564 | high |
| 8 | 0x0052cb90 | `fvproxy_auth_context_new` | builds one auth/data request: takes a `cmd` string, fetches `k` from the client's field map (+0xbb0 or +0xbd4), calls the request builder at 0x52d16d, then sends it | S:0x720a60, S:0x720a00, C:0x52d16d | high |
| 9 | 0x005427f0 | `proxy_recv_header` | reads the proxy header from the stream and calls the response parser at 0x542860 | S:0x7216b8, S:0x7216d0, C:0x542860 | medium |
| 10 | 0x00544260 | `response_field_parser` | walks the `key=value&…` response map; recognises `id`, `et`, `protocolVersion` (15 chars, `atoi` → field), and the URL schemes `proxy://` / `udp://` / `vpn://` | S:0x72167c, S:0x71a464, S:0x721674, S:0x71a45c | medium |
| 11 | 0x005464b0 | `net_work_loop` | connection work loop; on `work ret`/`send failed`/`event loop failed`; calls the request builder at 0x546fdc | S:0x721754, S:0x721760, S:0x72178c, C:0x546fdc | medium |
| 12 | 0x00551cc0 | `buffer_obscure_packet_v1` | legacy packet builder: `[keyA^keyB][keyB][crc32][len][payload]`, AES-128-CTR with keyA, then random padding | S:0x722a8c (assert); C:0x5434b0 (`FVPingManager`) | high |
| 13 | 0x00551f40 | `buffer_obscure_packet` (OPV2) | v2 packet builder: 16+16 key block + CRC32 + `"OPV2"` + tag/length frames + AES-128-CTR. **VPN/control path only** — not reached from the proxy request builder; AJS-07 confirmed the same builder (Linux `0x45080b`) is absent from the captured proxy connections | S:0x722a8c, S:0x722ad0 (`OPV2`), C:0x56c095 | high |
| 14 | 0x00552640 | `FVProxyRequestHeader::fetch` (parse) | parses the server reply: text mode searches `\r\n\r\n`, `200 OK\r\n`, `Upgrade: websocket\r\n`; binary mode decrypts a 4-byte length + body and searches `code=200`; logs `FVProxyRequestHeader::fetch %s` | S:0x722b90, S:0x722bc4, S:0x722b84, S:0x722afc | high |
| 15 | 0x00552ce0 | `FVProxyRequestHeader::build` | builds the proxy request: `protocolVersion < 2` → `POST /s?s=%s&n=%s HTTP/1.1` + upgrade headers; `>= 2` → `[16B key K][4B BE len]` + AES-CTR(K,0) of `s=..\nn=..\nk=..\n[l=..\n]_=<pad>` + body (no HTTP request line) | S:0x722ad8, S:0x722afc, S:0x722b18, S:0x722b34, S:0x722b50, S:0x722b6c–0x722b78, S:0x719614 (`"\n"`), S:0x722a88 (`"_="`) | high |
| 16 | 0x005531a0 | `serialize_field_map` | serialises `std::map<std::string,std::string>` as `k=v&k=v&…`, refuses to exceed the size budget (800 B), logs `request response map is too large` | S:0x722a38; C:0x55209d | high |
| 17 | 0x0055ce70 | `mbedtls_aes_crypt_ctr` | mbedTLS 2.x CTR: 128-bit big-endian counter (`bswap`/`add` loop), calls the AES core at 0x5608e0 | C:0x55ceb4 → 0x5608e0; called from 0x55216f/0x552243/0x553031/0x520e80 | high |
| 18 | 0x0055d410 / 0x0055e3a0 / 0x0055d3f0 | `mbedtls_aes_init` / `_setkey_enc` / `_free` | AES context lifecycle; `setkey_enc` is always called with `keybits=0x80` (128) | C:0x551f40, 0x520df0, 0x551cc0 | high |
| 19 | 0x0056b8b0 | `client_timer_state_machine` (`FVClientVpn` vtable[1]) | keep-alive / auth timer: state 200 = connected; 0x65 → `retry emitObscurePacket`; 0x66 → `retry emitClientAuth`; 9 s connect-auth timeout | S:0x724a74, S:0x724a90, S:0x720ba8, V:0x72499c slot 1 | high |
| 20 | 0x0056bd90 | `emitObscurePacket` | generates two random 16-byte keys (`rand()` per byte), builds `{"pv": <protocolVersion>}`, calls `buffer_obscure_packet`; called from 0x56aecd / 0x56aef0 | C:0x56c095 → 0x551f40; C:0x56aecd | high |
| 21 | 0x0057fae0 | `proxy_handshake` | handshake state machine: state 0 → send request (log `%s handshake: sending request ...`), state 2 → parse response (sub-state 1/2/3), then enqueue `ft` / `fm` fields | S:0x725690, S:0x7257cc, S:0x725724, S:0x7256d8, C:0x57fbf7 | high |
| 22 | 0x00581bb0 | `Remote(Relay)` connector | relay path: builds the request with `s = +0xbd4`, `n = +0x30`, `k = empty`, `l = -1`, then connects | S:0x725680, S:0x725674, C:0x581c51 | medium |
| 23 | 0x004925f0 | `read_file_bytes` | reads a file from the install dir into a `std::vector<uint8_t>`; used for both `res.fvr` and `product.dat` | C:0x48d8d0 (product.dat), C:0x48d7xx (res.fvr) | medium |

## Recovered vtables (MSVC RTTI: type descriptor → COL → vtable)

| class (type descriptor) | TD name address | vtable | slots (thunk → target) |
|---|---|---|---|
| `.?AVFVClientProxy@@` | 0x7952f0 | **0x7208f0** | 0x411db0→0x5702e0, 0x40c37e→0x52e040, 0x411694→0x52e240, 0x40dc65→0x52db40, 0x40f8d0→0x52db90, 0x401a46→0x52c300, 0x409e17→0x52b130 |
| `.?AVFVClientVpn@@` | 0x79558c | **0x72499c** | 0x4018ed→0x56c260, 0x4080ad→**0x56b8b0**, 0x40f916→0x56bac0, 0x40cf63→0x56b890, 0x4062b7→0x56b8a0, 0x40d643→0x56aa30, 0x4013e8→0x56a800 |
| `.?AVFVClientWrapper@@` | 0x795254 | **0x7203d0** | 0x401726→0x637e0f ×6, 0x4094d0→0x5215f0 |
| `.?AVFVHttpClient@@` | 0x794e10 | **0x71e6b0** | 0x40e584→0x5019e0, 0x40bc76→0x4ff900, 0x40b8fc→0x502ac0 |
| `.?AVFVNetClient@@` | 0x792fb4 | **0x71f4d0** | 0x40e73c→0x517950 |
| `.?AVFVThread@@` | 0x792eec | **0x71e618** | 0x401726→0x637e0f, 0x408517 |

Notes on the RTTI recovery, because it contradicts the naive reading of the strings table:

* The MSVC type descriptors live in `.data` (not `.rdata`) and are `{vftable=0x71e280; spare=0;
  name[]}`; the **name starts 8 bytes after the descriptor**, and `_RTTICompleteObjectLocator`
  points at the descriptor, not at the name. Searching for xrefs to the *name* address returns
  zero hits — searching for the descriptor address (`name − 8`) returns exactly one COL each.
* 248 type descriptors, **185 classes with a resolvable COL + vtable**.
* `.?AVFVClientProxySocks@@` and `.?AVFVClientProxyLwip@@` appear only as **log literals**
  (`0x323c78`, `0x324ccd` file offsets) and as template arguments
  (`.?AV?$_Ref_count@UFVClientProxyLwip@@@std@@` @0x7953a0, `…EvContextAuth@@` @0x795314,
  `…Data@@` @0x795360). No COL exists for either class, so their vtables are `[UNKNOWN]`.
  Presence of the classes is nonetheless `[CONFIRMED]` by the RTTI template arguments.

## Helper functions used by the frame code (unnamed by Ghidra, identified by body)

| address | identity | evidence |
|---|---|---|
| 0x00551ac0 | `append(vector<uint8_t>&, vector<uint8_t> const&)` | C:0x551f40, 0x551cc0 |
| 0x00551b10 | `append(std::string const&)` / append C string | C:0x551f40 (`"OPV2"`), 0x552ce0 |
| 0x00551b80 | `append_u16_be` (2-byte big-endian length) | C:0x551f40, body writes `>>8` then low byte |
| 0x00551bf0 | `append_u32` (little-endian) | C:0x551f40 (CRC), 0x551cc0 |
| 0x005717f0 | `appendf(std::string&, fmt, …)` (vsnprintf-style) | C:0x552ce0 text branch |
| 0x00574750 | `find(stream, needle, len)` → position or −1 | C:0x552640 text branch |
| 0x00571f20 | `read_exact(stream, buf, n)` | C:0x552640 binary branch |
| 0x005721d0 | `seek(stream, pos)` | C:0x552640 text branch |
| 0x00540ec0 | `vector<uint8_t>::reserve` | C:0x552ce0, 0x56bd90 |
| 0x00554050 | PRNG byte source (`fvlib_random` / mt19937) | S:0x722bd0 `mt19937 is using default seed: %u`, S:`fvlib_random`; used for keyA/keyB and the fake `Sec-WebSocket-Key` |
