# Windows 4.9.6.0 FV transport — static reverse

Target: `AJiaSu.exe` 4,064,640 B, PE32 x86 (MSVC, `/INCREMENTAL`, static libcurl + mbedTLS 2.x + lwIP),
sha256 `0fd9d702bf9932836ad0ba3e4acf43bb1a587c45ee6247b7a5d88aede78101f6`, image base `0x400000`.
Companion blob: `product.dat` 818 B, sha256 `e3de3fe622214034f68eff48705cfb028dd401754ac1daaee080688c5a78884f`.
Nothing was executed; every claim below comes from disassembly/decompilation of the file on disk.

## Answer

1. **The proxy carrier is neither OpenAJS's raw-TCP frame nor OPV2.** The proxy path sends one
   self-framed message `[16-byte key K][4-byte BE len][AES-128-CTR(K, IV=0)(text || body)]`.
   This was captured on the wire by AJS-07 (`research/60-dynamic/captures/fvmock-1.bin`,
   `fvmock-2.bin`) and is byte-for-byte the shape produced by the Windows builder `0x00552ce0`
   (binary branch). **OPV2 is the VPN/control builder, not the proxy carrier**: `0x00551f40` is
   reached only from `emitObscurePacket` `0x0056bd90`, which belongs to `FVClientVpn`
   (vtable `0x72499c`); AJS-07 found the same builder in the Linux binary at `0x45080b` and
   confirmed it is absent from the proxy auth and data connections. `[CONFIRMED]`.
   A legacy text variant also exists: `protocolVersion < 2` sends
   `POST /s?s=<s>&n=<n> HTTP/1.1` + a pseudo WebSocket upgrade. `[CONFIRMED]`.
2. **Two request encodings are selected by `protocolVersion`** (field at `this+0x10`,
   logged by `prepare_core` at `0x00526560`):
   * `protocolVersion < 2` → plain HTTP text: request line + `Upgrade`/`Connection`/
     `Content-Length`/`Sec-WebSocket-Key` headers + `\r\n`. No `Host` header at all.
   * `protocolVersion >= 2` → binary, **no HTTP request line at all**: `[16-byte AES key K]
     [4-byte BE length][AES-128-CTR(K, IV=0) of the text and body]`, where the text is
     **newline-separated** — `s=<v>\n n=<v>\n k=<v>\n [l=<v>\n] _=<0…199 random a–z>` — because
     the separator literal at `0x719614` is `"\n"` and the padding prefix at `0x722a88` is `"_="`.
     This matches AJS-07's captured `s=\nn=<32 hex>\nk=<base64>\nl=<len>\n_=<pad>` exactly in
     structure. `[CONFIRMED]` (Windows `0x00552ce0` + AJS-07 capture).
3. **`buffer_obscure_packet` is an OPV2 frame assembler, not a cipher, and not the proxy frame.**
   It emits `[keyA^keyB][keyB][crc32][“OPV2”][tag 1][u16be len][payload][tag 2][u16be len][body]
   [0x00]` and then AES-128-CTR-encrypts everything from the `OPV2` magic onwards with **keyA**
   and a **zero IV**. `FV_CipherKeySize = 16` (`0x10`). `[CONFIRMED]` (byte-for-byte from
   `0x00551f40`); tag semantics `[INFERENCE]`; its callers are the VPN/control class only.
4. **`s` = session id, `n` = per-connection 16-byte IV, `k` = RSA-wrapped session key, `l` = body
   length.** AJS-07 decrypted the capture: auth connection has `s=` empty and `n=` random;
   the data connection has `s=<sid from the auth response>`, `n=<fresh random div>`, `k=` empty
   and no `l=`. `k=` is `base64("OAEP" || RSA-OAEP-SHA1(sessionKey))` (1024-bit modulus).
   The Windows builder's argument order (`s,k,n,l`) and the optional `l` (emitted only when
   `> 0`; `-1` in the relay call site `0x00581bb0`) agree with that capture.
   `[CONFIRMED]` for the roles (AJS-07), `[INFERENCE]` for the Windows `k=` payload, since the
   Windows-side RSA-OAEP helper `0x00520f90` was not traced end-to-end.
5. **`product.dat` is cracked.** AES-128-CTR, key = `MD5("[CONFIG]")` =
   `2304669ad435f8829a061ee9860a4cc5`, IV = 16 zero bytes, no padding. The 818-byte blob
   decrypts to a 818-byte `[CONFIG]` INI. `ServerApiUrls` =
   `https://login.ajs-app.com/;https://login.mmegg.com/;https://login.mmegg.com:4443/;https://login.mbclubsh.com/`.
   `[CONFIRMED]` (plaintext recovered, see `product.dat.analysis.md`).
6. **Mode → implementation**: `proxy-tun` → mode 1 (`implType`-style value 1, flag `+0xa6`),
   `proxy-socks` / `proxy-global` → mode 2 (flag `+0xa7` for global), selected in
   `vpnConnect` `0x00448f40`. The VPN (`vpn://`) path is a separate class
   `FVClientVpn` (vtable `0x72499c`) that drives `emitObscurePacket`. `[INFERENCE]` for the
   numeric `implType` mapping.

## Function map

Full table with evidence and confidence in `FUNCTIONS.md`. Summary of the transport spine:

| address | role |
|---|---|
| `0x00448f40` | `vpnConnect` — route-mode → `proxy-tun`/`proxy-socks`/`proxy-global` |
| `0x0048d730` | `DesktopEngine::bootProduct` — loads + decrypts `product.dat`, parses `[CONFIG]` |
| `0x00520df0` | `product.dat` AES-CTR decryptor |
| `0x00520830` | MD5 KDF over `"[CONFIG]"` |
| `0x00526560` | `prepare_core` — logs `protocolVersion` (`+0x10`) / `implType` (`+0x50`) |
| `0x00552ce0` | `FVProxyRequestHeader::build` — the `[K][CTR]` binary request, or the legacy `POST /s?s=&n=` text request |
| `0x00552640` | `FVProxyRequestHeader::fetch` — response parser (`200 OK` / `code=200`) |
| `0x00551f40` | `buffer_obscure_packet` (OPV2 — VPN/control builder only, not the proxy carrier) |
| `0x00551cc0` | `buffer_obscure_packet` (legacy, used by `FVPingManager`) |
| `0x0056bd90` | `emitObscurePacket` — random keyA/keyB + `{"pv":protocolVersion}` |
| `0x0056b8b0` | `FVClientVpn::vtable[1]` timer state machine (`retry emitObscurePacket`) |
| `0x0057fae0` | proxy handshake state machine |
| `0x00581bb0` | `Remote(Relay)` connector |

Vtables recovered through the MSVC RTTI chain (type descriptor → COL → vtable):
`FVClientProxy` `0x7208f0`, `FVClientVpn` `0x72499c`, `FVClientWrapper` `0x7203d0`,
`FVHttpClient` `0x71e6b0`, `FVNetClient` `0x71f4d0`, `FVThread` `0x71e618`.
185 of 248 RTTI classes resolve to a vtable; `FVClientProxySocks` / `FVClientProxyLwip` have
type descriptors but **no COL**, so their vtables are `[UNKNOWN]`.

## Call flow

```
DesktopEngine::bootProduct            0x0048d730
  └─ product.dat  → MD5("[CONFIG]") → AES-128-CTR(IV=0)     0x00520df0 / 0x00520830
  └─ [CONFIG] INI → ServerApiUrls, ClientSite, ClientDistChan, …

vpnConnect (route mode)               0x00448f40
  ├─ "proxy-tun"     → mode 1, flag +0xa6
  ├─ "proxy-socks"   → mode 2
  └─ "proxy-global"  → mode 2, flag +0xa7

prepare_core                          0x00526560
  └─ log "prepare core = %s, protocolVersion = %d, implType = %d"

proxy_handshake (state machine)       0x0057fae0        [state in this+0x8, sub-state in this+0x40]
  state 0:
      log "%s handshake: sending request ..."
      FVProxyRequestHeader::build(protocolVersion, out, s, k, n, l)     0x00552ce0
        protocolVersion <  2 :  "POST /s?s=%s&n=%s HTTP/1.1\r\n"
                                "Upgrade: websocket\r\n"
                                "Connection: Upgrade\r\n"
                                "Content-Length: %d\r\n"      (only if l >= 0)
                                "Sec-WebSocket-Key: %s\r\n"
                                "\r\n"
        protocolVersion >= 2 :  [16B key K][4B BE len][AES-CTR(K, IV=0)(
                                 "s=<v>\n" "n=<v>\n" "k=<v>\n" ["l=<v>\n"] "_=" + 0..199 random a-z
                                 || body )]                         (no HTTP request line)
      state = 2
  state 2:
      FVProxyRequestHeader::fetch(stream)                              0x00552640
        text mode   : find "\r\n\r\n" → find "200 OK\r\n" → find "Upgrade: websocket\r\n"
                      → seek past the header block
                      returns 2 if BOTH tokens were found, 3 if only "Upgrade: websocket" was
        binary mode : read 4 B → AES-CTR decrypt → expect 00 00 LL LL (BE length)
                      → read LL+4 B → decrypt → find "code=200"
                      returns 2 if "code=200" was found, 3 if not
      sub-state 1 = need more bytes; 2 and 3 are terminal
      on 3 the handshake enqueues fields "ft" (0x7466) and "fm" (0x6d66)

client timer / auth state machine     0x0056b8b0   (FVClientVpn vtable[1])
  status 200  : keep-alive window ("client keep alive timeout", "exceeds keep alive duration")
  status 0x65 : "retry emitObscurePacket"  → 0x0056aea0 → emitObscurePacket 0x0056bd90
  status 0x66 : "retry emitClientAuth"     → 0x0056a890
  9 s without connect-auth → "client connect auth timeout"

emitObscurePacket                     0x0056bd90
  keyA = 16 × rand(), keyB = 16 × rand()
  map  = { "pv" : to_string(protocolVersion) }
  buffer_obscure_packet(ctx=this+0x1ac, out, keyA, keyB, payload=this+0xbb0,
                        fmtId=*(this+0xba8), map)                     0x00551f40
```

Wire shape `[CONFIRMED]` by the Windows builder/parser and by AJS-07's capture
(`research/60-dynamic/REPORT.md` §Answer 1/3/5, `captures/fvmock-1.bin`, `fvmock-2.bin`):

```
protocolVersion >= 2   (AJS-07 observed protocolVersion=2, implType=2 on Linux)
TCP connect → (optional SOCKS5/HTTP proxy replay, 0x0057fae0 "keep socks5/http replay")
  → [16B K][4B BE len][CTR(K, IV=0)( "s=\nn=<32 hex>\nk=<b64 OAEP>\nl=<body len>\n_=<pad>"
                                    || auth body )]
  ← [4B CTR(K,0)(len)][CTR(K, 4..)("code=200\nr=ok\nsid=…\n")]
  ← auth map AES-128-CTR(sessionKey, IV=n); server half-closes, client parses on EOF
  → per proxied flow, a NEW connection:
       [16B K][4B len][CTR(K,0)("s=<sid>\nn=<div>\nk=\n_=<pad>" || CTR(sessionKey, IV=n)(SOCKS5 CONNECT))]

protocolVersion < 2   (legacy text variant, not observed on the wire by AJS-07)
TCP connect → POST /s?s=..&n=.. HTTP/1.1 + Upgrade headers
  → server: "200 OK" + "Upgrade: websocket" + body "code=200"
```

No OPV2 frame appears on either proxy path.

## OPV2 framing

**Scope: this frame is the VPN/control builder, not the proxy carrier.** It is produced only by
`emitObscurePacket` `0x0056bd90` (reached from `FVClientVpn`, vtable `0x72499c`) and by
`FVPingManager` via the legacy variant `0x00551cc0`. AJS-07 confirmed the identical layout in the
Linux binary at `0x45080b` and confirmed that no `OPV2` bytes appear in the captured proxy auth or
data connections. `[CONFIRMED]`.

Byte layout produced by `buffer_obscure_packet` @ `0x00551f40` (offsets verified against the
store instructions at `0x551fc5`–`0x552248`):

| offset | size | content | confidence |
|---|---|---|---|
| 0 | 16 | `keyA XOR keyB` | CONFIRMED |
| 16 | 16 | `keyB` (plaintext) | CONFIRMED |
| 32 | 4 | `crc32(keyA XOR keyB)`, little-endian, zlib polynomial (table `0x78f3a8`) | CONFIRMED |
| 36 | 4 | `"OPV2"` magic | CONFIRMED |
| 40 | 1 | tag `0x01` | CONFIRMED (value); meaning INFERENCE |
| 41 | 2 | payload length, **big-endian u16** | CONFIRMED |
| 43 | n1 | payload (client field `+0xbb0`) | CONFIRMED |
| 43+n1 | 1 | tag `0x02` | CONFIRMED |
| 44+n1 | 2 | body length, big-endian u16 | CONFIRMED |
| 46+n1 | n2 | body: `AES-128-CTR(sessionKey, IV=0)` of the `k=v&k=v&…` map string | CONFIRMED |
| 46+n1+n2 | 1 | tag `0x00` (terminator) | CONFIRMED |
| 36 … end | — | **whole region encrypted in place with `AES-128-CTR(keyA, IV=0)`** | CONFIRMED |

So the frame has **no per-frame sequence number, no flags byte and no 4-byte header** — it is a
tag/length TLV stream wrapped in a key-exchange block and one outer CTR layer. The inner body is
encrypted twice (session key, then keyA).

Legacy variant `0x00551cc0` (used by `FVPingManager`, caller `0x005434b0`) has the same
`[keyA^keyB][keyB][crc32]` prefix but **no `OPV2` magic**: `[prefix 36][u32 len][payload]
[AES-CTR(keyA)][random padding of 0…~64 blocks]`. It is the pre-OPV2 packet format.

`FV_CipherKeySize` is `0x10` (16): both key buffers are length-checked against `0x10`
(`0x551f9c`, `0x551fa6`) and the assert literal is
`buffer_obscure_packet key size must be FV_CipherKeySize` @ `0x722a8c`.

## Obscure packet

**Scope: VPN/control path only** (see `## OPV2 framing`). The proxy carrier is the `[K][CTR]`
block built by `0x00552ce0`; AJS-07's capture contains no `buffer_obscure_packet` layer.

`buffer_obscure_packet` (`0x00551f40`, 921 bytes) — exact algorithm:

```c
void buffer_obscure_packet(aes_ctx *session, vector<uint8_t> *out,
                           uint8_t keyA[16], uint8_t keyB[16],
                           vector<uint8_t> *payload, uint32_t fmtId,
                           map<string,string> *fields)
{
    if (len(keyA) != 16 || len(keyB) != 16) assert("buffer_obscure_packet key size must be FV_CipherKeySize");

    base = out->size();
    out += keyA;                              // 16 bytes
    for (i = 0; i < 16; i++) {
        out[base + i] ^= keyB[i];             // in-place XOR of the just-appended keyA
        out += keyB[i];                       // append keyB
    }
    out += u32le(crc32(0, out->data(), 16));  // 4 bytes
    out += "OPV2";                            // 4 bytes
    out += 0x01;                              // tag
    out += u16be(payload->size());
    out += *payload;

    s = serialize_field_map(fmtId, fields, 800 - payload->size() - out->size());  // "k=v&k=v&"
    aes_ctr(session, s.len, nc_off=0, nonce=all-zero, stream, s.data, s.data);    // inner layer
    out += 0x02;
    out += u16be(s.len);
    out += s;
    out += 0x00;                              // terminator

    aes_init(&k); aes_setkey_enc(&k, keyA, 128);
    aes_ctr(&k, out->size() - 36, nc_off=0, nonce=all-zero, stream,
            out->data() + 36, out->data() + 36);   // outer layer over "OPV2"..end
    aes_free(&k);
}
```

Key derivation:

* **keyA and keyB are fresh random bytes per packet**, 16 bytes each, drawn one byte at a time
  from the `fvlib_random`/mt19937 source (`0x00554050`) in `emitObscurePacket` `0x0056bd90`.
  They are *not* derived from `s`, `n`, the account, or `product.dat`.
* The peer recovers keyA as `(keyA^keyB) XOR keyB` — both halves are sent in the clear, so the
  frame key is **not secret against anyone who sees the packet**. The obfuscation is
  confidentiality-by-obscurity, exactly like the rest of the FV stack. `[CONFIRMED]` by the
  layout; security assessment is `[INFERENCE]`.
* The AES layer is **AES-128-CTR with a fixed all-zero IV**, both for the inner map string
  (`session` context at `this+0x1ac`) and for the outer frame (keyA). It **wraps**, it does not
  replace, the session-key encryption. `[CONFIRMED]`
* `serialize_field_map` `0x005531a0` emits `key=value&…`, replaces `\n` with space inside
  values, and enforces a size budget (800 bytes in the OPV2 path; it logs
  `request response map is too large: size=%d, may break udp protocol` @ `0x722a38`).

## product.dat

**Fully decrypted.** Chain:

```
DesktopEngine::bootProduct              0x0048d730
  read_file_bytes(install_dir + "product.dat")   → 818-byte vector
  if (len > 7 && !(data[0..7] == "[CONFIG]"))     → 0x0048d95f..0x0048d9c4
      decrypt(std::string("[CONFIG]"), data, len) → 0x00520dc0
          key = KDF("[CONFIG]")                   → 0x00520830  (mbedtls MD5, 88-byte ctx)
          mbedtls_aes_init / setkey_enc(key, 128) → 0x0055d410 / 0x0055e3a0
          mbedtls_aes_crypt_ctr(len, nc_off=0, nonce=0, in=out)  → 0x0055ce70
  parse "[CONFIG]" INI                          → 0x0048d9c4 …
      ServerApiUrls → 0x0048e8d8
```

Result: `AES-128-CTR(key = 2304669ad435f8829a061ee9860a4cc5, IV = 0)` over the whole 818-byte
blob, no header, no padding, plaintext begins with `[CONFIG]\r\n`.

Decrypted values of interest:

```
[CONFIG]
ProductFullName=爱加速
ProductShortName=AJiaSu
ProductWebSite=https://www.91ajs.com/
ClientSite=ajs
ClientDistChan=
ServerApiUrls=https://login.ajs-app.com/;https://login.mmegg.com/;https://login.mmegg.com:4443/;https://login.mbclubsh.com/;
ProductServerFlagMode=text
```

Full plaintext and the standalone script are in `product.dat.analysis.md`. Note: `ServerApiUrls`
is the **login/API** base list, not the tunnel endpoint — the tunnel host still comes from the
login response (`proxy://` / `udp://` / `vpn://` URLs parsed at `0x00544260`).

## Reconciliation with OpenAJS

| aspect | OpenAJS (raw TCP, `svr:par.fv.p`) | Linux 4.2.3.0 **captured** (AJS-07) | Windows 4.9.6.0 (this build) |
|---|---|---|---|
| Transport | raw TCP to the FV port | raw TCP to the FV port (`127.0.0.1:8443` in the lab) | same raw TCP for `protocolVersion >= 2`; legacy `POST /s?s=..&n=..` + pseudo `Upgrade: websocket` only when `protocolVersion < 2` |
| First bytes | 16-byte header IV, then encrypted 4-byte header + text | `[16B K][4B BE len][AES-128-CTR(K, IV=0)(text‖body)]` — starts `0e 03 c6 77 …` | builder `0x00552ce0` emits exactly that structure (`[16B key][4B BE len][CTR(K,0)]`) |
| HTTP layer | none | **none** — no request line, no `Host`, no `Upgrade` | none in binary mode; custom text with no `Host` and a 0–199-char lowercase `Sec-WebSocket-Key` in legacy mode |
| Header text | — | `s=\nn=<32 hex>\nk=<176-char b64>\nl=<body len>\n_=<pad>` | `"s=" "\n" "n=" "\n" "k=" "\n" ["l=" "\n"] "_=" + 0..199 random a-z` (`0x719614="\n"`, `0x722a88="_="`) |
| Server ack | none | `[4B CTR(K,0)(len)][CTR(K,4..)("code=200\nr=ok\nsid=…\n")]`, then the auth map encrypted with `AES-128-CTR(sessionKey, IV=n)` on EOF | parser `0x00552640`: 4-byte length + body, search `code=200`; legacy mode searches `200 OK\r\n` / `Upgrade: websocket\r\n` |
| Frame | 16-byte IV + AES-CTR 4-byte header + payload | **`[K][CTR]` block** — no magic, no TLV, no keyA^keyB | same block; **OPV2 is not on this path** |
| OPV2 | absent | absent from the capture (builder exists at Linux `0x45080b`, unused here) | present only in `FVClientVpn` → `emitObscurePacket` → `buffer_obscure_packet` `0x00551f40` |
| Key material | `sessionKey` + `div` from the login handshake | 16-byte random `K` in the clear; `k=` = `base64("OAEP"‖RSA-OAEP-SHA1(sessionKey))`, 1024-bit modulus; `n` = CTR IV | same roles; `k` is the `k=` field of `0x00552ce0`, RSA helper `0x00520f90` present but untraced |
| Framing fields | 4-byte header | 4-byte BE length, then text + body | identical |
| Field names | `sid sip cip dns ka kat kapf mtu ft fm au cuid cseid st cv osp osd osv dim peip ce` | `c ce cseid cuid cv dim osd osp osv p peip st t u v _` in the auth body; `c=aur r=ok sid= s= ok= sip= cip= dns= ka= …` in the auth map | same vocabulary: `kapf` `0x720708`, `cuid` `0x720720`, `cseid` `0x720728`, `peip` `0x720748`, `con` `0x724af0`, `ping` `0x719777`, `pong` `0x7209ac`, `off` `0x71808c`, `aur` `0x7209a8`; handshake enqueues `ft`/`fm` |
| Control commands | `con ping pong off sid aur fm ft au` | `c=aur` observed; others not exercised | same, plus states `0x65`/`0x66` = retry `emitObscurePacket` / `emitClientAuth` |
| Mode | one path | `protocol proxy`, `protocolVersion=2`, `implType=2` | `FVClientProxy` (vtable `0x7208f0`) for `proxy-*`, `FVClientVpn` (vtable `0x72499c`) for `vpn://` |

Verdict: **same product, same field vocabulary, same proxy carrier as the Linux capture, and
OPV2 is *not* it.** The proxy data plane is the `[16-byte key K][4-byte BE length][AES-128-CTR
(K, IV=0)]` block, empirically confirmed by AJS-07 on Linux and structurally identical in the
Windows builder `0x00552ce0`. OPV2 (`[keyA^keyB][keyB][crc32]["OPV2"][tag][len]…`) is the
**VPN/control** builder: on Windows it is reachable only through `FVClientVpn` →
`0x56b8b0` → `0x56bd90` → `0x551f40`; AJS-07 found the same builder in the Linux binary at
`0x45080b` and confirmed it does not appear on the proxy auth/data connections.
OpenAJS's raw-TCP `FvConnection` (16-byte IV + AES-CTR header) is therefore closest to the
*shared* `[K][CTR]` idea but matches neither implementation byte-for-byte; treat OpenAJS as a
third, older variant until a capture of its own traffic exists. `[CONFIRMED]` for the Windows
code paths and for the Linux capture; `[INFERENCE]` for OpenAJS's exact wire bytes.

## Evidence

Every command below was run on this workstation (2026-10-23); nothing was executed from the
vendor package.

```
# identity
sha256sum AJiaSu.exe product.dat
  0fd9d702bf9932836ad0ba3e4acf43bb1a587c45ee6247b7a5d88aede78101f6  AJiaSu.exe
  e3de3fe622214034f68eff48705cfb028dd401754ac1daaee080688c5a78884f  product.dat

# PE layout (pefile 2024.8.26)
.text  VA 0x401000 size 0x311779 | .rdata VA 0x713000 size 0x7b2ec
.data  VA 0x78f000 size 0xfa48   | .idata VA 0x79f000 | imagebase 0x400000

# string VAs (file offsets in the AJS-03 table are file offsets, VA = off - 0x311c00 + 0x713000)
0x722ad0 "OPV2"                          0x722ad8 "POST /s?s=%s&n=%s HTTP/1.1\r\n"
0x722afc "Upgrade: websocket\r\n"        0x722b18 "Connection: Upgrade\r\n"
0x722b34 "Content-Length: %d\r\n"        0x722b50 "Sec-WebSocket-Key: %s\r\n"
0x722b6c "s="  0x722b70 "n="  0x722b74 "k="  0x722b78 "l="  0x722b7c "\r\n\r\n"
0x719614 "\n"  (field separator inside the binary request text; NOT "&")
0x722a88 "_="  (padding-field prefix inside the binary request text; NOT "\r\n\r\n")
0x722b84 "200 OK\r\n"                    0x722b90 "FVProxyRequestHeader::fetch %s"
0x722bc4 "code=200"                      0x722a8c "buffer_obscure_packet key size must be FV_CipherKeySize"
0x720520 "prepare core = %s, protocolVersion = %d, implType = %d"
0x7191fc "product.dat"  0x71925c "[CONFIG]"  0x719494 "ServerApiUrls"

# xrefs (raw 4-byte immediate scan of .text)
0x722ad0 → 0x552029   0x722ad8 → 0x552d53   0x722afc → 0x552d5e
0x722b6c..0x722b78 → 0x552e61/0x552e95/0x552ec9/0x552f08
0x722bc4 → 0x5528b2   0x722a8c → 0x551fab

# Ghidra 12.1.3 headless (analysis 108 s, decompile 301 functions)
analyzeHeadless.bat <proj> AjsWin -import AJiaSu.exe -postScript AjsDump.java <out> all
  → STRINGS 298 | FUNCS 21598 | XREFFUNCS 301 | DECOMP 301

# RTTI: descriptor = name-8, COL = the only xref to it, vtable = the only xref to COL + 4
.?AVFVClientProxy@@  TD 0x7952f0 → COL 0x76af28 → vtable 0x7208f0
.?AVFVClientVpn@@    TD 0x79558c → vtable 0x72499c  (slot 1 → 0x56b8b0)
.?AVFVHttpClient@@   TD 0x794e10 → vtable 0x71e6b0
248 type descriptors, 185 with a resolvable vtable

# product.dat (cryptography 46.x, AES-CTR)
key = md5(b"[CONFIG]").digest() = 2304669ad435f8829a061ee9860a4cc5
plaintext = AES-CTR(key, IV=0).decrypt(product.dat)   → starts with "[CONFIG]\r\n"
```

Cross-check against the dynamic slice (read-only, not re-run here):

```
D:/Project/LovePlusSpeedToProxy/research/60-dynamic/REPORT.md
  §Answer 1  proxy data plane = [16B K][4B BE len][AES-128-CTR(K, IV=0)(text||body)], no OPV2
  §Answer 3  s = sid, n = 16-byte IV, k = base64("OAEP"||RSA-OAEP-SHA1(sessionKey)), l = body len
  §Answer 5  response = [4B CTR(K,0)(len)][CTR(K,4..)("code=200…")] then the auth map on EOF
D:/Project/LovePlusSpeedToProxy/research/60-dynamic/captures/fvmock-1.bin  (679 B auth connection)
D:/Project/LovePlusSpeedToProxy/research/60-dynamic/captures/fvmock-2.bin  (207 B data connection)
```

Correction log (this report, 2026-10-23): an earlier revision described the binary request text
as `s=..&n=..&k=..[&l=..]\r\n\r\n` and claimed the proxy payload was OPV2. Both were wrong.
Re-reading the literals shows `0x719614 = "\n"` and `0x722a88 = "_="` (newline-separated fields
with a `_=` pad), and AJS-07's capture shows the proxy carrier is the `[K][CTR]` block with no
`OPV2` anywhere on that path.

## Open questions

1. **`s` / `n` provenance (partly resolved).** AJS-07's capture settles the roles on Linux:
   `s` = session id (empty on the auth connection, `<sid>` from the auth response on each data
   connection), `n` = a fresh 16-byte IV sent as 32 hex chars. On Windows the same two values are
   `std::string` members `client+0xbd4` and `client+0x30` (`Remote(Relay)` `0x00581bb0`); the code
   that fills them from the auth map was not traced, so the Windows-side assignment is
   `[INFERENCE]` by analogy, not `[CONFIRMED]`.
2. **Tag semantics.** `0x01` (payload from `client+0xbb0`) and `0x02` (map with `pv`) are
   observed values; which is "obscure/key-exchange" and which is "auth" is `[INFERENCE]` from the
   state names `retry emitObscurePacket` / `retry emitClientAuth`.
3. **`implType` values.** `this+0x50`, logged by `prepare_core`; `vpnConnect` produces mode 1
   (`proxy-tun`) and 2 (`proxy-socks`/`proxy-global`) but the value is copied into the client
   object through a path not fully traced. `[INFERENCE]`. AJS-07 observed `implType=2` for
   `protocol proxy` on Linux, consistent with mode 2 here.
4. **`FVClientProxySocks` / `FVClientProxyLwip` vtables.** Type descriptors and template
   arguments exist, no COL → no vtable, no method addresses. `[UNKNOWN]`.
5. **Post-auth OPV2.** Whether keep-alive/`ping` or the relayed byte stream switches to the OPV2
   builder after the proxy connection is established cannot be decided statically; AJS-07 did not
   exercise a full relay either. `[UNKNOWN]`.
6. **`0x00520f90` (RSA-OAEP).** Called from `prepare_core` with two bignums parsed from strings;
   the strings were not located in `.rdata` as literals, so they are probably built at runtime
   from config. Not needed for `product.dat` (that path is MD5+AES-CTR). AJS-07 showed the
   *server-side* equivalent is OAEP-SHA1 with a 1024-bit modulus; whether `0x00520f90` is the
   client's counterpart is `[UNKNOWN]`.
