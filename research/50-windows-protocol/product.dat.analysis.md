# product.dat — cipher, key, schema, plaintext

Input: `product.dat`, 818 bytes, sha256
`e3de3fe622214034f68eff48705cfb028dd401754ac1daaee080688c5a78884f`.

**Status: decrypted.** Plaintext recovered in full, 818 bytes, sha256
`2b618010b49d66922d778beda9aae7117084a46fd6008706e6f5c9c53291183e`.

## Cipher

| property | value | evidence |
|---|---|---|
| algorithm | AES-128-CTR | `mbedtls_aes_setkey_enc(ctx, key, 0x80)` at `0x0055e3a0`, `mbedtls_aes_crypt_ctr` at `0x0055ce70` |
| key | `MD5("[CONFIG]")` = `2304669ad435f8829a061ee9860a4cc5` | KDF `0x00520830` (mbedtls MD5, 88-byte context) over the literal `"[CONFIG]"` |
| IV / nonce | 16 zero bytes | `nc_off = 0`, nonce block zeroed with `movups [ebp-0x50], xmm0` before the call at `0x520e6e` |
| mode detail | 128-bit big-endian counter, incremented per 16-byte block (mbedTLS 2.x semantics) | `bswap`/`add` loop at `0x55cec0` |
| padding | none | ciphertext and plaintext are both exactly 818 bytes |
| header / magic | none in the file; the *plaintext* begins with `[CONFIG]` | `0x0048d9a0` compares `data[0..3] == "[CON"` and `data[4..7] == "FIG]"` to decide whether decryption is needed |

## Call chain

```
DesktopEngine::bootProduct                       0x0048d730
  FUN_004925f0(install_dir + "product.dat")      reads the file into a vector
  if (size > 7 && *(u32*)data != '[CON' || *(u32*)(data+4) != 'FIG]')
      FUN_00520dc0(std::string("[CONFIG]"), &vector)          0x00520dc0
          FUN_00520df0(string, data, len)                      0x00520df0
              key[16] = FUN_00520830(string.data, string.len)  0x00520830  (MD5)
              mbedtls_aes_init(&ctx)
              mbedtls_aes_setkey_enc(&ctx, key, 128)
              mbedtls_aes_crypt_ctr(&ctx, len, &nc_off=0, nonce=0, stream, data, data)
              mbedtls_aes_free(&ctx)
  parse INI starting at "[CONFIG]"                            0x0048d9c4 …
      "ServerApiUrls" → 0x0048e8d8
```

The same `FUN_00520df0` pattern (KDF → `setkey_enc(128)` → `crypt_ctr(IV=0)`) is used for other
in-memory secrets, so `"[CONFIG]"` is a *fixed* password, not a machine-derived key. Nothing in
the derivation touches the machine name, SID, volume serial or registry.

## Reproduce

```python
import hashlib
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
blob = open('product.dat','rb').read()
key  = hashlib.md5(b'[CONFIG]').digest()
pt   = Cipher(algorithms.AES(key), modes.CTR(b'\x00'*16)).decryptor().update(blob)
```

Script kept at `C:/Users/dwgx1/AppData/Local/Temp/ajs-win-proto-20261023/decrypt_product.py`
(writes `product.dat.plain.txt`, prints the key and the plaintext).

## Schema

A single INI section `[CONFIG]`, `Key=Value`, CRLF line endings, no trailing CRLF on the last
line. 21 lines, 22 key/value pairs. Keys observed in the decryptor and in the plaintext:

`ProductFullName`, `ProductShortName`, `ProductWebSite`, `ProductMutexName`, `ProductLpClassName`,
`ClientSite`, `ClientDistChan`, `ServerApiUrls`, `ProductDefaultLanguage`, `ProductRegisterMode`,
`ProductRegisterPasswordMinLength`, `ProductRegisterPasswordMaxLength`,
`ProductPriorityDisplayAccountName`, `MobileDefaultCountryNumber`, `ProductShowLanguageSelect`,
`ProductShowAreaSelect`, `ProductShowRouteMode`, `ProductCustomerSupportPath`,
`ProductSocks5TutorialPath`, `ProductServerFlagMode`, `ProductShowAntiFraudTips`.

## Plaintext (818 bytes, verbatim)

```
[CONFIG]
ProductFullName=爱加速
ProductShortName=AJiaSu
ProductWebSite=https://www.91ajs.com/
ProductMutexName=Global\\VPNCLIENT_SINGLE_{BB861724-8BB3-49D6-93E9-59682E9A4C82}
ProductLpClassName=H-SMILE-FRAME
ClientSite=ajs
ClientDistChan=
ServerApiUrls=https://login.ajs-app.com/;https://login.mmegg.com/;https://login.mmegg.com:4443/;https://login.mbclubsh.com/;
ProductDefaultLanguage=zh_cn
ProductRegisterMode=mobile
ProductRegisterPasswordMinLength=8
ProductRegisterPasswordMaxLength=20
ProductPriorityDisplayAccountName=mobile
MobileDefaultCountryNumber=86
ProductShowLanguageSelect=false
ProductShowAreaSelect=false
ProductShowRouteMode=false
ProductCustomerSupportPath=Help/QqService
ProductSocks5TutorialPath=help#socks5-tutorial
ProductServerFlagMode=text
ProductShowAntiFraudTips=true
```

## Extracted values of interest

| key | value | note |
|---|---|---|
| `ServerApiUrls` | `https://login.ajs-app.com/;https://login.mmegg.com/;https://login.mmegg.com:4443/;https://login.mbclubsh.com/` | 4 fallback **login/API** bases, `;`-separated, tried in order |
| `ClientSite` | `ajs` | product/site discriminator |
| `ClientDistChan` | *(empty)* | distribution channel |
| `ProductServerFlagMode` | `text` | server-flag encoding (matches the `code=200` text token) |
| `ProductLpClassName` | `H-SMILE-FRAME` | Sciter window class |
| `ProductMutexName` | `Global\\VPNCLIENT_SINGLE_{BB861724-8BB3-49D6-93E9-59682E9A4C82}` | single-instance mutex |
| `ProductDefaultLanguage` | `zh_cn` | |
| `MobileDefaultCountryNumber` | `86` | |

No tunnel hostname is present: the FV endpoint arrives at runtime from the login response as a
`proxy://` / `udp://` / `vpn://` URL (parser at `0x00544260`).

## Confidence

* Cipher, key, IV, and plaintext: **CONFIRMED** — decryption produces a well-formed INI that
  starts with the exact token the code checks for, and every key matches a literal in
  `bootProduct`.
* "`[CONFIG]` is the only password source": **CONFIRMED** for this build — the string is built on
  the stack immediately before the call (`mov dword [ebp-0x2c], 0x5d4749464e4f435b` = `"[CONFIG]"`,
  `mov dword [ebp-0x1c], 0xf00000008` = size 8 / capacity 15).
