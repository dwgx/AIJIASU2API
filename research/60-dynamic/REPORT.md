# AJS-07 — Dynamic lab: Linux client against a mock control plane and mock FV server

Status: 2026-10-23. Local-only run; no real API or FV host was contacted.
Targets: Linux `ajiasu` 4.2.3.0 (`29e18740af8b69c6fa4f7b0ef269316b01e0400d7768583fc08044632be13b9f`)
and a patched temp copy with a lab RSA key. Dummy account `test` / `test`.

SIG_STATUS: unknown

## Answer

1. **The proxy data plane is not OPV2 in the shipping Linux 4.2.3.0 flow.** The client sends a
   `protocolVersion=2` binary request: `[16B key K][AES-128-CTR(K, IV=0)(4B BE text_len || text || body)]`.
   The auth text is `s=\nn=<32 hex>\nk=<176-char base64>\nl=<body len>\n_=<pad>`; the body is
   `AES-128-CTR(sessionKey, IV=n)` over newline-separated auth fields. No `OPV2` magic, no
   `keyA^keyB` prefix, and no `buffer_obscure_packet` layer appears on the wire. `[CONFIRMED]` by
   capture + decryption (`captures/fvmock-1.bin`, `captures/fvmock-1.log`).
2. **`k=` is `base64("OAEP" || RSA-OAEP-SHA1(sessionKey))`**, 16-byte session key, 1024-bit
   modulus. Confirmed by patching the embedded modulus (offset `0x28a030`, 309 decimal digits)
   with a lab key and decrypting with the private key: OAEP-SHA1 succeeds, OAEP-SHA256 fails.
   `[CONFIRMED]`.
3. **`s` is the session id and `n` is the per-connection 16-byte IV.** Auth request: `s=` empty,
   `n=` random. Data channel: `s=<sid from auth response>`, `n=<fresh random div>`, `k=` empty,
   **no `l=` field**; the SOCKS5 CONNECT body follows the header text immediately and is
   `AES-128-CTR(sessionKey, IV=n)`. `[CONFIRMED]` (`captures/fvmock-2.log`: 18-byte body decrypts
   to `05 01 00 03 0b "example.com" 00 50`).
4. **Control-plane login on Linux 4.2.3.0 carries no `X-Sig` and no `X-Timestamp`.** Headers are
   only `Host`, `Accept`, `Accept-Encoding`, `Connection`, `Content-Length`, `Content-Type`; the
   body ends with a trailing `&` and uses `v=4.2.3.0`. The signing secret is therefore not
   exercised by this path; the `06ka4z0dc6ga02c` / OpenAJS secret question stays open for the
   Linux login. `[CONFIRMED]` for absence (`captures/control.jsonl`), `[UNKNOWN]` for the secret.
5. **The auth response is split across two layers and needs a half-close.** First the
   `code=200` block (`[4B CTR(K,0)(len)][CTR(K,4..)(text)]`, one stream, no duplicated length),
   then the auth map `AES-128-CTR(sessionKey, IV=n)` of `c=aur\nr=ok\nsid=...\n...`. The client
   parses that map on `BEV_EVENT_EOF`; the server must `shutdown(SHUT_WR)` after sending it.
   Only then does the client reach `state=200` and listen on `127.0.0.1:1080`. `[CONFIRMED]`.

## Method

- WSL Ubuntu-24.04 (root), Python 3.12 + `cryptography` 41.0.7.
- `/etc/hosts` redirected `login.ajs-app.com`, `login.mmegg.com`, `login.mbclubsh.com` to
  127.0.0.1; a local CA was added to the WSL trust store; mock TLS on 443/4443.
  Backups: `/tmp/ajs-lab/hosts.bak`; CA file removed during cleanup. No iptables rule was needed;
  no outbound connection to a real API host was observed.
- Mock control plane (`mock/mock_control.py`) logs every request and returns a crafted
  `Login`/`Servers` envelope. Mock FV server (`mock/mock_fv.py`) implements the wire format above.
- RSA patch (`mock/patch_rsa.py`): generated a 1024-bit key with a 309-digit modulus, overwrote
  `0x28a030` in a temp copy only, verified the byte diff is confined to that range and that
  `-h` still runs. The private key stayed in `/tmp/ajs-lab/ajs_priv.pem`, not in the repo.
- One SOCKS5 probe (`mock/socks_probe.py`) triggered one data-channel connection.

## Control-plane capture

The client tries the four API hosts in order (all redirected). A successful login request:

```
POST /client.php HTTP/1.1
Host: login.mmegg.com
Accept: */*
Accept-Encoding: deflate, gzip
Connection: close
Content-Length: 460
Content-Type: application/x-www-form-urlencoded

_lang=en&_m=linux&_os_dev=x86_64&_os_dim=lid%3dREDACTED...&_os_ver=Linux%20version...&_site=ajs&_uid=<20 hex>&cmd=ClientApiLogin%2fLogin&password=test&username=test&v=4.2.3.0&
```

Response-schema findings (all required to avoid client asserts/timeouts):

- The body must end with `}`; the client checks the last byte and otherwise logs
  `response check failed, buf.size()=...`.
- `SyncVersion` must be an **object** (`{}`); a number triggers
  `json_value.cpp: getMemberNames:1526` abort.
- `Login` must contain `UrlWeb` and `LatestVersion` (strings), otherwise
  `json_value.cpp: asCString:991` abort.
- `Login` must contain `AuthResult`, `AuthMessage`, `GroupTitle`, `ExpireTime`, `Expired`;
  `AuthResult=OK` is the success token. `Servers` is a map keyed by id; the fake entry was
  `vvn-test` with `svr=127.0.0.1`, `par.fv.p=8443`, `sts=ok`.

## X-Sig

The Linux 4.2.3.0 login path does **not** send `X-Sig` or `X-Timestamp`; the capture contains
neither header on any of the four hosts. Candidate-secret testing was therefore not possible
against a live request. The OpenAJS secret and `06ka4z0dc6ga02c` remain untested on Linux; the
Windows client is the only known user of the signing headers. **Result: `[UNKNOWN]`.**

## OPV2 and frame

**No OPV2 frame was observed in proxy auth or in the data-channel open.** The captured bytes are:

- auth connection (679 bytes total): `[16B K][4B enc len][CTR text 234B][body 425B]`;
  first bytes `0e 03 c6 77 05 74 fa 0a 95 bd 70 3b 73 3b ae a7 ...`.
  Decrypting offset 16 with `AES-CTR(K,0)` gives `00 00 00 ea` (234) followed by
  `s=\nn=0e03c677...\nk=T0FFUE...\nl=425\n_=<pad>`.
- data connection (207 bytes total): `[16B K][4B enc len][CTR text 169B][SOCKS body 18B]`;
  text `s=test\nn=180a5b9318eafe1814908cafb9800eae\nk=\n_=<pad>` (no `l=`).
- response (auth): 34 bytes = `[4B CTR(K,0)(00 00 00 1e)][CTR(K,4..)("code=200\nr=ok\ns=test\nsid=test\n")]`.
  The client peeks the first 4 bytes for the length, then re-reads `len+4` bytes **from offset 0**
  of the same buffer and decrypts them in one pass; sending a duplicated length block produces
  `FVProxyRequestHeader::fetch <garbage>` and a timeout.
- auth map (187 bytes): `AES-128-CTR(sessionKey, IV=n)` over
  `c=aur\nr=ok\nsid=test\ns=test\nok=1\nsip=10.0.0.1\ncip=10.0.0.2\ndns=1.1.1.1\nka=30\n...`.

The OPV2 builder exists in the binary (Linux `0x45080b`, same
`[keyA^keyB][keyB][crc32]["OPV2"][tag1][len][payload][tag2][len][body][0]` layout as Windows
`0x551f40`), but it is not on the proxy auth/data path captured here. The `0x42cad8` TLV parser
(15-byte global prefix + tag `0x01` + map) belongs to a different (control/VPN) path; feeding the
auth response through it is rejected. `FV_CipherKeySize` / `buffer_obscure_packet` were not
exercised. `[CONFIRMED]` for the observed path; OPV2's role elsewhere stays `[UNKNOWN]`.

## Decryption

- Patched modulus: generated 1024-bit key, 309-digit decimal, same length; patched only
  `0x28a030..0x28a164` in `/tmp/ajs-lab/ajiasu-4.2.3.0-patched`; `-h` still runs.
- `k=` decoded to 132 bytes: `4f 41 45 50` (`OAEP`) + 128-byte RSA ciphertext.
- OAEP-SHA1 decrypts to a 16-byte session key; OAEP-SHA256 raises. Example run:
  `session_key=252a3a233e1cd2e781a9709e377d2cb8`.
- Auth body (425 bytes) decrypts with `AES-CTR(sessionKey, IV=n)` to:

```
c=au
ce=
cseid=linux
cuid=6ada95c900de9bbb3a82
cv=20241105-1
dim=lid=REDACTED,...
osd=x86_64
osp=linux
osv=Linux version 6.18.33.2-microsoft-standard-WSL2 ...
p=test
peip=
st=ajs
t=1792711256
u=test
v=4.2.3.0
_=fpxcmeqpxcfdivxlddubvvuxebweughngsqfmfeqrytri
```

- Data body (18 bytes) decrypts to `05 01 00 03 0b 65 78 61 6d 70 6c 65 2e 63 6f 6d 00 50`
  (SOCKS5 CONNECT `example.com:80`), proving the session key is reused for the data channel and
  that `n` is the CTR IV.

## Client behavior

- `protocolVersion=2`, `implType=2` for `protocol proxy` (log line `prepare core = 20241105-1,
  protocolVersion = 2, implType = 2`).
- After the auth map and EOF, `FVClient state=200`, then
  `on dev ready, listen on 127.0.0.1:1080` / `proxy(socks5/http): 127.0.0.1:1080`.
- The local listener answers the SOCKS5 greeting `05 00`; one proxied flow opens a **new TCP
  connection** to the FV endpoint (127.0.0.1:8443) with `s=<sid>`, `n=<div>`, `k=`, and the
  encrypted SOCKS5 CONNECT. The client waits for `code=200` and a 10-byte encrypted SOCKS5 reply
  before sending the relayed payload.
- The client does not require a valid account: the mock `Login`/`Servers`/auth map were enough.
  No TUN/root was needed for `protocol proxy`.

## Captures

| file | content |
|---|---|
| `captures/control.jsonl` | all `/client.php` requests (machine-id redacted); login headers/body |
| `captures/fvmock-1.bin` | raw auth connection (679 B): key, encrypted length/text/body |
| `captures/fvmock-1.log` | decrypted auth text, RSA session key, decrypted body |
| `captures/fvmock-2.bin` | raw data-channel connection (207 B): header text + 18 B SOCKS body |
| `captures/fvmock-2.log` | `s=test`, `n=<div>`, decrypted SOCKS5 CONNECT |

## Open questions

- The Linux `X-Sig` secret is still unknown; this client does not sign the login request. If a
  later command signs, it was not reached in this lab.
- Whether any post-auth packet (keep-alive/`ping`, relayed data) uses OPV2. The relay path was
  only partially exercised (SOCKS request captured; the mock's relay reply was not validated by
  the client before cleanup).
- `peip` semantics (empty here), `ka`/`kat`/`kapf` acceptance rules, and whether the server may
  choose a response key other than the request key `K`.
- `s`/`n` generation: `s` comes from the auth response `sid`; `n` is 16 random bytes per
  connection. Whether `sid` can be reused for multiple data connections was not tested.

## Self-check

- **A (early artifact):** mock server + first raw capture were on disk before this report.
- **B (falsification):** the wave-1 claim "proxy carrier = OPV2 frame" is **OVERTURNED** for the
  Linux auth/data path: the capture shows `[16B key][CTR]`, no `OPV2`. The claim "`code=200` is
  the HTTP status" is **WEAKENED**: it is the decrypted text inside a 4-byte length block, and
  the HTTP status line is absent entirely in `protocolVersion=2`. The claim "`s`/`n` are opaque"
  is **CONFIRMED** in role (`s`=sid, `n`=IV) but their generators are server/session-driven.
- **C (verify):** `python D:/Project/LovePlusSpeedToProxy/research/60-dynamic/validate.py` was
  run; output is pasted in the final message.
- **D (summary):** established: binary request format, OAEP-SHA1, session key reuse, auth map +
  EOF requirement, data-channel SOCKS5 body, no X-Sig on login. Overturned: OPV2 on the proxy
  path. Unknown: X-Sig secret, post-auth OPV2, relay validation. Next: capture a relayed byte
  stream and a keep-alive. My remaining bet: that `protocolVersion=2` is selected by the server
  or config in other builds; here it was unconditional.
