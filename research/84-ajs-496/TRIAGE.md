# TRIAGE — AJiaSu 4.9.6.0 (Windows/macOS), 4.3.0.1 (Linux), 4.12.2.0 (Android)

Static inventory, hashes, file types, packer verdicts, endpoints, certificates, update metadata and a
cross-platform version/feature diff. Slice `84-ajs-496`, 2026-10-24.

**Scope discipline.** Static analysis only. No Windows/APK/macOS payload was executed, no installer
or driver was run, no vendor host was contacted, no account or credential was used. Linux dynamic
behaviour is out of scope (separate coordinated run). Findings carry `path:offset` or a command;
anything not exercised is tagged `[UNVERIFIED]`, anything deduced is `[INFERENCE]`, anything not
observable is `[UNKNOWN]`. Third-party secrets found in the APK are named but **not reproduced**.

## Answer

1. **Four platform builds, one shared core.** Windows and macOS 4.9.6.0 are the same Sciter desktop
   app built from `F:\workspace\vpn123\...\fvapp-desktop-sciter` (Win) and
   `/Users/youdun-ndl/Desktop/gitlab-fv-app-apple/...` (mac), both with FV core stamp `20250708-1`.
   Linux 4.3.0.1 and the Android 4.12.2.0 native library carry the newer core `20260525-1` from the
   same `/work/fvclient/src` tree — roughly ten months newer than the desktop core, even though the
   desktop app version number (4.9.6.0) is higher than the Linux one (4.3.0.1).
2. **The control host list is plaintext only on Linux and Android** (`login.ajs-app.com`,
   `login.mmegg.com`, `login.mmegg.com:4443`, `login.mbclubsh.com`). Windows and macOS keep it in the
   818-byte encrypted `product.dat` (identical hash on both), so the desktop host list is not
   statically recoverable without breaking that blob.
3. **No packer/protector on any platform.** DIE reports MSVC 19.29.30136 / VS2019 16.11 (Windows),
   `lipo` universal binary (macOS), GCC 9.4.0 static (Linux), GCC (Android `.so`); sections, imports
   and string tables are all intact. The only obfuscation is the encrypted `product.dat` and an
   un-obfuscated-but-native DEX (`com.fvcorp.*` class names readable).
4. **Signing is corporate on desktop, self-signed on Android.** Windows: GlobalSign **EV** code
   signing for 上海游盾网络科技有限公司 (valid to 2027-03-14, timestamped). macOS: Apple Developer ID
   `N82WKSN6R7` for the same company (valid to **2026-10-30**, i.e. days from this triage). Android:
   self-signed `CN=Shawn` key from 2017, valid to 2117, APK Signature Scheme v1+v2 only. Linux: no
   signature at all.
5. **New attack surface vs the 4.2.3.0 baseline**: a macOS **setuid-root** helper
   (`proxy_conf_helper`) installed by a script copied verbatim from shadowsocks, Android
   `usesCleartextTraffic=true` with 17 exported components and `REQUEST_INSTALL_PACKAGES`, and
   plaintext third-party SDK secrets inside the APK.

## Confidence

**High** for everything in §1–§4 and §7: every hash, size, offset, certificate field and version
anchor was read directly from the artefact on 2026-10-24, and the provenance of the extracted Windows
tree was re-established by re-extracting S1 and comparing member hashes.

**Medium** for the interpretation rows in §7 that rest on string presence rather than code paths
(e.g. "system proxy via `sysproxy.exe`", "`kapf` absent on ARM"), and for §6.1's per-architecture
update flow, which is inferred from the `arm64`/`amd64`/`i386` strings next to the update keys.

**Not claimed.** Nothing here was executed: no tunnel, no login, no installer, no driver. Control-plane
behaviour, TLS trust, update downloads and Linux runtime modes are **not** re-verified in this slice;
where they are mentioned they are `[INFERENCE]` from `research/82-live-20261023/REPORT.md` or `[UNKNOWN]`.

## 1. Sample inventory

All hashes are SHA-256, computed on this workstation on 2026-10-24 with `sha256sum`.

| # | Sample | Path | Size (B) | SHA-256 |
|---|---|---|---|---|
| S1 | Windows installer | `C:/Users/dwgx1/Downloads/AJiaSu_4.9.6.0 (1).exe` | 5,507,328 | `1b1c97f55a2348055c201ecba0badd0f7ea17dacb1f0df3ea979d52ee9d01a1c` |
| S2 | Android APK | `C:/Users/dwgx1/Downloads/AJiaSu_4.12.2.0_ajs-official-website.apk` | 19,253,381 | `42ce2b5152c767c3f7412d65793aa15c294e539fb1f499428103205a352df3cb` |
| S3 | macOS DMG | `C:/Users/dwgx1/Downloads/AJiaSu_4.9.6.0.dmg` | 16,568,179 | `206bca275219a2f95e145e73683331aacc8c104ccfe456c71ca6b4e6ad5110ed` |
| S4 | Linux amd64 tarball | `C:/Users/dwgx1/Downloads/ajiasu-amd64-4.3.0.1.tar.gz` | 1,264,209 | `0affe73aff4b7ee11cc6fab5c060d71dc3056a1c2903ca3435f4fe009d2689e7` |
| S5 | Linux x86 tarball | `C:/Users/dwgx1/Downloads/ajiasu-x86-4.3.0.1.tar.gz` | 1,239,830 | `fa0e2293328c8774ce860d40bd61aee6e4c23e8a8082e55f8d54d1003fe4623b` |
| S6 | Linux aarch64 tarball | `C:/Users/dwgx1/Downloads/ajiasu-aarch64-4.3.0.1.tar.gz` | 1,256,932 | `0cb8893225b229f502390e0391cff68300cfc69922110377ce7212f82e093f11` |

Provenance checks performed in this slice:

- The extracted Windows tree `C:/Users/dwgx1/.dwgx/tmp/ajs-re-20261024a/exe/` is **the payload of S1**:
  a fresh `7z x` of S1 into `_triage/inst/` produced 37 files and every compared member hash is
  identical, e.g. `AJiaSu.exe` `0fd9d702…101f6`, `sciter.dll` `42d0979c…9f5fe`, `product.dat`
  `e3de3fe6…884f`, `res.fvr` `9e985bca…b6af`, `lang.fvr` `84a95a56…9aaa`,
  `tap/tap/amd64/tap0901.sys` `89c5ca14…01c0`, `sysproxy/amd64/sysproxy.exe` `e0d76ece…bec7`.
- The repo copy `research/AJiaSu_4.9.6.0.exe` is byte-identical to S1
  (`1b1c97f5…d01a1c`) and is **not** git-tracked (`git ls-files research/AJiaSu_4.9.6.0.exe` → empty).
- Each Linux tarball contains exactly one file, `ajiasu` (tar entries listed with `7z l`), matching
  the extracted `linux-*/ajiasu`.

## 2. Per-platform artifact inventory

### 2.1 Windows 4.9.6.0 (payload of S1, 37 files)

| Artifact | Size (B) | SHA-256 | Type / identity |
|---|---|---|---|
| `AJiaSu.exe` | 4,064,640 | `0fd9d702bf9932836ad0ba3e4acf43bb1a587c45ee6247b7a5d88aede78101f6` | PE32 GUI i386, 8 sections, baddr `0x400000`, checksum `0x003eecc8`, compiled 2025-07-09 11:33:25, Authenticode-signed, CFG (`.00cfg`) |
| `sciter.dll` | 6,369,280 | `42d0979c16c8f872aa89242878e8865a9ca708342784f6e5181154fa17f9f5fe` | PE32 DLL i386, Sciter **4.4.8.33** (`0x4b0f48`), MSVC 19.16.27412, unsigned |
| `product.dat` | 818 | `e3de3fe622214034f68eff48705cfb028dd401754ac1daaee080688c5a78884f` | opaque encrypted config, entropy 7.7356 bit/B, 245 distinct byte values, magic `a0711680d5be2a7b` |
| `res.fvr` | 289,045 | `9e985bca60aab15e8930353745ea0e4adaff384d02feabb22e1263649235b6af` | ZIP (Sciter resource pack), 137 files, member dates 2022-11-16 / 2024-05-07, archive date 2025-07-09 11:22:08 |
| `lang.fvr` | 4,684 | `84a95a56cfccaa7690e267c711dc4dd722abb3486654d3f561bbf005bcb49aaa` | ZIP, single member `lang_zh_cn.txt` (10,832 B, UTF-8, CRLF, 197 `Key = value` lines) |
| `sysproxy/amd64/sysproxy.exe` | 120,832 | `e0d76ece45a0a7b63e10fe1782cd6f196700540da7af5cb9bbc187c265ecbec7` | PE32+ CUI x86-64, MSVC, imports `WININET.dll`, PDB `F:\\workspace\\vpn123\\prog\\internal\\sysproxy-master\\x64\\Release\\sysproxy.pdb` |
| `sysproxy/arm64/sysproxy.exe` | 90,112 | `ab68825ca267b9589487f6bc6eb0077d7531a536fb06b7c6bd8edbfb96091e33` | PE machine `0x01c4` **ARMNT (32-bit ARM)**, PE32 — despite the `arm64` directory; the `tap/arm64` driver/installer are true ARM64 (`0xaa64`, PE32+) |
| `sysproxy/i386/sysproxy.exe` | 105,984 | `21d16a04909a0434e397b3b749661b196b335de145b77acedb8c1984e4d33d13` | PE32 i386 |
| `tap/tap/amd64/tap0901.sys` | 27,136 | `89c5ca1440df186497ce158eb71c0c6bf570a75b6bc1880eac7c87a0250201c0` | PE32+ driver, compiled 2016-04-21, unsigned, TAP-Windows 9.x (`tap0901`) |
| `tap/tap6/amd64/tap0901.sys` | 39,920 | `589c5667b1602837205da8ea8e92fe13f8c36048b293df931c99b39641052253` | PE32+ driver, NDIS 6 variant (`tap6` set) |
| `tap/*/tapinstall.exe` | 92,304 / 101,536 / 566,824 / … | see `_triage` hashes | PE32(+) console, driver installer |
| `uninst.exe.nsis` | 20,742 | — | NSIS uninstaller stub |

Installer identity: `Nullsoft Install System v3.04` (manifest string inside S1), 37 payload files,
`requireAdministrator` manifest, `PerMonitorV2` DPI awareness.

### 2.2 macOS 4.9.6.0 (DMG `AJiaSu_4.9.6.0/AJiaSu.app`)

| Artifact | Size (B) | SHA-256 | Type / identity |
|---|---|---|---|
| `Contents/MacOS/AJiaSu` | 4,893,360 | `33039c96e71de3bc59b1a099873030c6c931b29fe035d18e9093c9107cb82066` | Mach-O **universal** (x86_64 + arm64), clang, PIE, stripped, code-signed (2 CMS blobs) |
| `Contents/Frameworks/libsciter.dylib` | 24,862,912 | `6d680fe08af3fe64b38f3a5b70828059052869da090ad8583c2d50079936fb1e` | Sciter engine (dylib) |
| `Contents/Frameworks/Sparkle.framework/Versions/B/Sparkle` | — | `49f4d0321faa7056e476b0ef99ace12b1c888cabebb93bb4aed0694aa1e24d1b` | Sparkle **2.3.0** (framework `Info.plist`: `CFBundleShortVersionString 2.3.0`, `CFBundleVersion 2021`) |
| `Contents/Resources/proxy_conf_helper` | 173,120 | `1ba0f607fbfdae929c7055f7673da20a044ccfafa41f1f9450a58bf2b95a3d7d` | Mach-O universal helper, installed **setuid root** (see §8) |
| `Contents/Resources/install_helper.sh` | — | — | shadowsocks-derived installer script (header `# shadowsocks`, `# Created by clowwindy on 14-3-15`) |
| `Contents/Resources/product.dat` | 818 | `e3de3fe622214034f68eff48705cfb028dd401754ac1daaee080688c5a78884f` | **identical to Windows** `product.dat` |
| `Contents/Resources/res.fvr` | — | `7012a7e284d1b5f66d7b99091bff84e20888dafc4ba95b1ceb3b92895f43fe74` | ZIP, same 137 member names as Windows; 9 members hash-differ: 8 only by CRLF→LF, `dialog-setting.htm` also in content (see §7) |
| `Contents/Resources/lang.fvr` | — | `5be3b3515b3efb277f252344bdc90d822104c4fc7ce8fbb4cfbb32d47c5e88c6` | ZIP, member content byte-identical to Windows `lang_zh_cn.txt` (10,832 B) |

`Info.plist`: `CFBundleIdentifier com.aijiasuinc.FVClientAJS`, `CFBundleShortVersionString 4.9.6`,
`CFBundleVersion 4.9.6.0`, `LSMinimumSystemVersion 10.13`, `DTXcode 1620` (Xcode 16.2),
`DTSDKName macosx15.2`, `DTPlatformBuild 24C94`, `BuildMachineOSBuild 24D70`. **No `SUFeedURL`** — the
appcast URL is supplied at runtime.

DMG layout is the standard drag-install: `AJiaSu.app`, `Applications` symlink, `.background/bgimg.png`.

### 2.3 Linux 4.3.0.1 (three tarballs, one binary each)

| Artifact | Size (B) | SHA-256 | Type / identity |
|---|---|---|---|
| `linux-amd64/ajiasu` | 3,607,464 | `08fcd8668f7aaf6660277778e67fd2afd622c3f1c8fb634e57f31605bf01c8f6` | ELF64 x86-64, static, stripped, non-PIE, partial RELRO, GCC 9.4.0 |
| `linux-x86/ajiasu` | 3,387,912 | `35f5fa742559f4cf6123da197c0b21b808237c9d2a9b270ef7e6eade43e022ba` | ELF32 i386, static, stripped |
| `linux-aarch64/ajiasu` | 3,668,488 | `541db12104977339037fca8d971a68fee90a93981ad42961a7e94cde129cdcab` | ELF64 AArch64, static, stripped |

Version anchors inside `linux-amd64/ajiasu`: `4.3.0.1` @ `0x2902b9`, core build stamp `20260525-1` @
`0x292860`. CLI surface (strings, `.rodata`): `Usage: ./%s [--option=...] {login|list|logout}` @
`0x28ed68`, `connect "Shanghai 1*"` @ `0x28ed98`, config file `/etc/ajiasu.conf` @ `0x2901f8`,
`protocol = {udp|tcp|lwip|socks5|http|proxy}`, `use-vpn-dns`, `use-vpn-route`, `up-down`.

### 2.4 Android 4.12.2.0 (APK S2)

Package `com.fvcorp.android.aijiasuclient`, `versionCode 134`, `versionName 4.12.2.0`, `minSdk 23`,
`targetSdk 36`, `compileSdk 36` (`aapt2 dump badging`).

| Artifact | Size (B) | SHA-256 | Type / identity |
|---|---|---|---|
| `classes.dex` | 4,646,368 | `f66599dd9fbf1a35d6e9e66b5fcdf6f3de42ce1e094f50fc9f1478693d299b78` | DEX 035, **not obfuscated** (`com/fvcorp/android/fvclient/...` readable) |
| `lib/arm64-v8a/libfvcore.so` | 3,429,688 | `297e8b18f2562843ac7055098001004765aedafcecfb72382bec2845bc693b86` | ELF64 AArch64, PIE, full RELRO, stripped, JNI `Java_com_fvcorp_android_fvcore_FVClient_*`, core `20260525-1` |
| `lib/armeabi-v7a/libfvcore.so` | 2,312,012 | `e0725e9210192e9bb7a8f6193ccd382d373c9cedf3aba667d05f63f05b5dbc09` | ELF32 ARM, core `20260525-1` |
| `lib/x86/libfvcore.so` | 3,282,864 | `256b04f539b7f4cfaf194b85f37799eedcff36a5cf54859f91d1ad268faece85` | ELF32 x86, core `20260525-1` |
| `lib/x86_64/libfvcore.so` | 3,418,528 | `6e09490e8df59caec98b7c796b8fb9aad72812b319881b97c71660726bc48c21` | ELF64 x86-64, core `20260525-1` |
| `lib/arm64-v8a/libfntvcrash.so` | 113,424 | `f48b29da896bde3e110e87f9f11dc98d7b0e6d1b531f11798c4ecddf920d155b` | **xCrash 3.1.0** crash catcher (`Tombstone maker: 'xCrash 3.1.0'`) |
| `lib/arm64-v8a/libumeng-spy.so` | 398,472 | `3fd428644673e23b232c6eab6043d8cef82590df965a3a495b0cffafe3e106c9` | Umeng analytics native |
| `lib/arm64-v8a/libweibosdkcore.so` | 26,136 | `b1fe581623cb9d757d0cc126a2c04207ab342df41be4db11ad58556a369d89b4` | Weibo SDK |
| `resources.arsc` | 1,319,044 | `b10b77ab7d3ab7cf022f07e528b5e521bd53d16c94a994a844277f03256e8cea` | resources |
| `AndroidManifest.xml` | 30,132 | — | binary AXML, 17 exported components |
| `assets/ShareSDK.xml` | — | — | third-party AppId/AppKey/AppSecret **in plaintext (values redacted here)** for WeChat, QQ, QZone, Sina Weibo, Kuaishou |

Note: `libfvcore.so` ships for all 4 ABIs; `libumeng-spy.so` for arm64-v8a / armeabi-v7a / x86;
`libweibosdkcore.so` for arm64-v8a / armeabi-v7a; `libfntvcrash.so` for arm64-v8a only (x86_64 has
`libfvcore.so` alone).

## 3. Packers, protectors, toolchain

`diec -j` (Detect It Easy 3.21) on every primary binary; verdicts cross-checked against section
tables, imports and string tables.

| Artifact | DIE verdict | Independent evidence | Packer/protector |
|---|---|---|---|
| `AJiaSu.exe` | Microsoft Linker 14.29.30136; MSVC 19.29.30136 [C++]; VS2019 16.11; `Windows Authenticode 2.0 [PKCS #7]`; debug records `codeview, vc_feature` | 8 named sections (`.text` 3.2 MB, `.rdata`, `.data`, `.idata`, `.tls`, `.00cfg`, `.rsrc`, `.reloc`); process/memory API surface fully visible (`CreateProcessW`, `OpenProcess`, `Toolhelp32*`, `VirtualAlloc`, `ShellExecuteExW`); PDB path intact; `UPX!` absent | **none** (signed, unpacked) |
| `sciter.dll` | MSVC 19.16.27412; VS2017 15.9; debug records `codeview, vc_feature, pogo` | version string `4.4.8.33`; 5 sections; unsigned | none |
| `sysproxy*.exe` | MSVC (PDB `…\sysproxy-master\x64\Release\sysproxy.pdb`) | imports `WININET.dll`; tiny (≤120 KB) | none |
| `tap0901.sys` (both sets) | PE32+ driver, compiled 2016-04-21 | `tap0901` / `TAP-Windows Adapter V9` strings in `AJiaSu.exe`; PE itself unsigned but shipped with `tap0901.cat` catalog files (both `tap/` and `tap6/` sets) | none (third-party OpenVPN TAP-Windows) |
| macOS `AJiaSu` | `Mach-O FAT`, converter `lipo` | 2 slices (`cputype 0x1000007` x86_64 @ `0x4000`, `0x100000c` arm64 @ `0x264000`), each with `LC_CODE_SIGNATURE` → `SuperBlob 0xfade0cc0` + CMS `0xfade0b01` | none |
| `linux-*/ajiasu` | GCC 9.4.0 (amd64); **GCC 9.4.0 + "Rust"** (x86, aarch64) | sections `.text/.rodata/.eh_frame/.gcc_except_table/.data.rel.ro/.comment`; C++ mangled names (`_Sp_counted_ptrIP12FVConfigFile…`); **no** Rust runtime markers (`rust_begin_unwind`, `panicked at`, `.rs:` paths) → DIE's Rust verdict on x86/aarch64 is a **false positive** | none |
| `libfvcore.so` (4 ABIs) | GCC, ELF64/ELF32 | JNI exports, `/work/fvclient/src/...` assertion paths, full RELRO, no `jiagu`/`mobisec`/`dexhelper` shell libs | none |
| `classes.dex` | (n/a) | readable `Lcom/fvcorp/android/fvclient/...` descriptors; no string encryption | **not obfuscated** |

Anti-analysis: `AJiaSu.exe` imports `IsDebuggerPresent`, `WinExec`, `CreateProcessW`,
`ShellExecuteExW` (normal for launching `sysproxy`/`tapinstall`/browser URLs). No VM/sandbox
detection strings (`vmware`, `virtualbox`, `sandbox`, `cpuid`, `hypervisor` → 0 hits). Packer markers
`UPX!`, `MPRESS`, `Themida`, `VMProtect`, `.vmp`, `enigma` → 0 hits in all five primary binaries
(the single `MPRESS` substring match in `libfvcore.so` at `0xc376d` is inside
`PK_PARSE_EC_COMPRESSED`).

Embedded third-party code (string evidence): libcurl, mbedTLS (TLS 1.3 symbols such as
`mbedtls_ssl_tls13_compute_resumption_master_secret`), libevent, lwIP, zlib, jsoncpp
(`json_reader.cpp` / `json_value.cpp` / `json_writer.cpp` under `/work/fvclient/src/fvcore/`), and an
unexplained OpenSSL-derived string cluster (`AES note: using AESNI.`, `CAMELLIA-CBC-%3d (%s):`) in
all four cores — see §9 U4.

`capa -q exe/AJiaSu.exe` was started on 2026-10-24 and **cancelled after ~40 min** without output
(vivisect on a 4 MB MSVC binary); the packer verdict above does not depend on it. `[UNVERIFIED]` for
capa capability extraction.

## 4. Certificates and signatures

### 4.1 Windows — Authenticode EV (valid, timestamped)

`Get-AuthenticodeSignature` on `exe/AJiaSu.exe`:

- **Status:** `Valid` / "Signature verified."
- **Subject:** `CN=上海游盾网络科技有限公司, O=上海游盾网络科技有限公司, L=上海, S=上海, C=CN,`
  `OID.1.3.6.1.4.1.311.60.2.1.2=Shanghai, OID.1.3.6.1.4.1.311.60.2.1.3=CN,`
  `SERIALNUMBER=91310230MA1JXGC65A, OID.2.5.4.15=Private Organization`
- **Issuer:** `CN=GlobalSign GCC R45 EV CodeSigning CA 2020, O=GlobalSign nv-sa, C=BE`
- **Serial:** `185B478CF39117014D54537F`; **Thumbprint (SHA-1):** `A1B7B574A19211012B09EC31940D23ACE01EDBFE`
- **Validity:** 2024-03-13 11:11:50 → 2027-03-14 11:11:50
- **RFC3161 timestamp:** `CN=DigiCert Timestamp 2024, O=DigiCert, C=US` (issuer DigiCert Trusted G4
  RSA4096 SHA256 TimeStamping CA)

Installer S1 is signed by the **same certificate**: `Get-AuthenticodeSignature` → `Valid`, subject
`CN=上海游盾网络科技有限公司 …`, thumbprint `A1B7B574A19211012B09EC31940D23ACE01EDBFE`; `diec` reports
`Windows Authenticode [PKCS #7], after overlay`.

### 4.2 macOS — Apple Developer ID (expires 2026-10-30)

Parsed from the `LC_CODE_SIGNATURE` CMS blob of both slices (`openssl pkcs7 -print_certs`):

- **Leaf:** `UID=N82WKSN6R7, CN=Developer ID Application: Shanghai Youdun Network Technology Co., Ltd (N82WKSN6R7),`
  `OU=N82WKSN6R7, O=Shanghai Youdun Network Technology Co., Ltd, C=CN`
- **Validity:** 2021-10-29 07:28:06 → **2026-10-30 07:28:05** (six days after this triage)
- **SHA-256:** `74:2E:39:D6:53:CF:AB:C4:38:4D:99:70:D7:E3:46:05:17:B7:7C:46:15:7F:F9:12:4F:DD:96:C9:BF:6C:27:ED`
- **Chain:** Developer ID Certification Authority (`7A:FC:9D:01:…:58:7F`, to 2027-02-01) → Apple Root CA
  (`B0:B1:73:0E:…:24`, to 2035-02-09)
- Both slices carry an identical 9,045-byte CMS blob (SuperBlob slot `0x10000`, 9,037 payload bytes),
  so the bundle is signed as a whole; `Contents/_CodeSignature/CodeResources` (11,406 B) covers
  `Resources/*`.
- **Same corporate entity as the Windows EV certificate** — "Shanghai Youdun" ≡ 上海游盾.

### 4.3 Android — self-signed debug-style key

`apksigner verify -v --print-certs` + `keytool -printcert -jarfile`:

- **DN:** `CN=Shawn, OU=F, O=F, L=LA, ST=LA, C=US` (issuer = subject, self-signed)
- **Serial:** `372744e4`; **algorithm:** SHA256withRSA, RSA-2048
- **Validity:** 2017-12-15 19:12:06 → 2117-11-21 19:12:06
- **SHA-256:** `9A:15:1F:62:72:17:FE:7D:AE:99:9B:D0:29:ED:61:06:39:06:DB:00:AF:36:6F:C1:02:CB:DB:81:A6:2F:29:85`
- **Schemes:** v1 (JAR) = true, v2 = true, **v3/v3.1/v4 = false**, 1 signer, no source stamp.
- The analysed file is the **official-website build** (`…_ajs-official-website.apk`), i.e. distributed
  outside the store; this key is also the update trust anchor for that channel.

### 4.4 Linux — unsigned

`rabin2 -I` reports no signature for any of the three ELF binaries. The amd64 section table has no
signature or build-id carrier (`.init .text .fini .rodata .eh_frame .gcc_except_table .tbss
.init_array .fini_array .data.rel.ro .got .got.plt .data .bss .comment .shstrtab`), so there is no
signing artefact to check at all.

## 5. Endpoints, hosts and paths with offsets

Offsets are **file offsets** (`paddr` from `rabin2 -zz`, or `grep -abo` byte offsets for DEX/PE) unless
marked otherwise. macOS offsets refer to the **x86_64 slice**; the arm64 slice has its own layout and
carries the same strings at different offsets (e.g. `/client.php` at file offset `0x43ae39`).

### 5.1 Vendor control plane

| # | String | Artifact | Offset | Note |
|---|---|---|---|---|
| 1 | `https://login.ajs-app.com/` | `linux-amd64/ajiasu` | `0x290214` | control host 1 of 4 |
| 2 | `https://login.mmegg.com/` | `linux-amd64/ajiasu` | `0x29022f` | host 2 |
| 3 | `https://login.mmegg.com:4443/` | `linux-amd64/ajiasu` | `0x290248` | host 3 (the live one per `research/82`) |
| 4 | `https://login.mbclubsh.com/` | `linux-amd64/ajiasu` | `0x290266` | host 4 |
| 5 | `https://login.ajs-app.com/` | `apk/classes.dex` | `0x3f88ad` | same list, DEX (sorted order) |
| 6 | `https://login.mbclubsh.com/` | `apk/classes.dex` | `0x3f88c9` | |
| 7 | `https://login.mmegg.com/` | `apk/classes.dex` | `0x3f88e6` | |
| 8 | `https://login.mmegg.com:4443/` | `apk/classes.dex` | `0x3f8900` | |
| 9 | `/client.php` | `exe/AJiaSu.exe` | `0x31641c` | control path |
| 10 | `/client.php` | `linux-amd64/ajiasu` | `0x2932d9` | |
| 11 | `/client.php` | mac `Contents/MacOS/AJiaSu` | `0x1ef0e5` | also `0x43ae39` (arm64 slice) |
| 12 | `/client.php` | `apk/classes.dex` | `0x37d776` | |
| 13 | `/client.php` | `apk/lib/arm64-v8a/libfvcore.so` | `0xd0332` | native core |
| 14 | `ClientApiLogin/Login` | `exe/AJiaSu.exe` | `0x31e3f0` | action name |
| 15 | `ClientApiLogin/Login` | `linux-amd64/ajiasu` | `0x2932e5` | |
| 16 | `ClientApiLogin/Login` | `apk/lib/arm64-v8a/libfvcore.so` | `0xc3ebf` | |
| 17 | `/User/ChangeEmail` | `apk/classes.dex` | `0x37d6bd` | account API path |
| 18 | `/User/ChangePassword` | `apk/classes.dex` | `0x37d6d0` | |
| 19 | `/User/ChangePhoneNumber` | `apk/classes.dex` | `0x37d6e6` | |
| 20 | `/Payment/NotifyGooglePlayBilling` | `apk/classes.dex` | `0x37d69b` | billing callback path |
| 21 | `/anti/postZdata` | `apk/classes.dex` | `0x37d6ff` | anti-fraud telemetry path |
| 22 | `/anti/updateZdata` | `apk/classes.dex` | `0x37d710` | |
| 23 | `https://www.91ajs.com` | `apk/classes.dex` | `0x3f8e1b` | official site / download host |
| 24 | `asset.sciter.com` | `exe/AJiaSu.exe` | `0x316184` | Sciter asset host (third-party, not vendor) |
| 25 | `/etc/ajiasu.conf` | `linux-amd64/ajiasu` | `0x2901f8` | Linux config path |
| 26 | `/etc/resolv.conf` | `linux-amd64/ajiasu` | `0x28ebec` | DNS rewrite target |
| 27 | `/resolv.conf.backup` | `linux-amd64/ajiasu` | `0x28ebd8` | DNS backup file |

### 5.2 Third-party endpoints inside the APK (network surface, not vendor)

| # | String | Offset in `classes.dex` | Owner |
|---|---|---|---|
| 28 | `https://alogsus.umeng.com` | `0x3f83f4` | Umeng analytics |
| 29 | `https://pslog.umeng.com` | `0x3f8c6d` | Umeng |
| 30 | `https://ulogs.umeng.com` | `0x3f8d65` | Umeng |
| 31 | `https://utoken.umeng.com` | `0x3f8d9c` | Umeng |
| 32 | `https://resolve.umeng.com/resolve` | `0x3f8cba` | Umeng |
| 33 | `https://ucc.umeng.com/v2/inn/fetch` | `0x3f8d41` | Umeng |
| 34 | `https://mclient.alipay.hk` | `0x3f8a95` | Alipay SDK |
| 35 | `https://render.alipay.hk` | `0x3f8ca0` | Alipay SDK |
| 36 | `https://service.weibo.com/share/mobilesdk.php` | `0x3f8cdd` | Weibo SDK |
| 37 | `https://graph.qq.com` | `0x3f8778` | QQ SDK |
| 38 | `https://beian.miit.gov.cn` | `0x3f8674` | ICP filing link |

### 5.3 Where the desktop host list lives

`product.dat` (818 B, entropy 7.7356, magic `a0711680d5be2a7b`) is the only candidate: Windows and
macOS carry **zero** vendor domains in plaintext (ASCII and UTF-16 both checked: `mmegg`, `ajs-app`,
`mbclubsh`, `4443` → 0 occurrences in `AJiaSu.exe` and in the Mach-O). `AJiaSu.exe` carries the
consumers of that blob: `product.dat` (UTF-16 @ `0x317dfc`), `ApiUrls` @ `0x31e31c`,
`ServerApiUrls` @ `0x318094`, `FetchedUrlHosts` @ `0x31e12c`, `LastUrlHost` @ `0x31e0dc`,
`SyncVersion` @ `0x31e2a4`. Decryption algorithm/key `[UNKNOWN]` — see §9 U1.

## 6. Update metadata

### 6.1 Windows — in-app download with checksum, per-architecture payloads

Strings in `AJiaSu.exe` `.rdata` (file offsets):

| String | Offset | Meaning |
|---|---|---|
| `AppUpdate` | `0x313024` | update state/config key |
| `UpdateRequired` | `0x313030` | forced-update flag |
| `PromptNetworkError` | `0x313044` | update UI error |
| `UpdateChecksum` | `0x3130d0` | checksum key for the downloaded package (verification is `[INFERENCE]` from the key name) |
| `OnlineUpdate` | `0x3131a4` | online update mode |
| `PromptDownloadUpdateCompletedToUpdate` | `0x313110` | |
| `Invalid installer` (UTF-16) | `0x3130e4` | rejection message |
| `window-download-update.htm` (UTF-16) | `0x313164` | Sciter update window |
| `arm64` / `amd64` / `i386` | `0x3131b4` / `0x3131bc` / `0x3131c4` | update packages are **arch-keyed** |
| `4.9.6.0` | `0x3131e4` | current version literal |
| `\config.ini` (UTF-16) | `0x31325c` | install-dir config |
| `\Debug` / `\CacheNetwork` / `\Crash` / `*.log` | `0x3132b4` / `0x3132c4` / `0x313340` / `0x313350` | local state dirs |
| `%s Client Version = %s (%s), OS Version = %s\n` | `0x31337c` | update/telemetry log line |
| core stamp `20250708-1` | `0x31ffe8` | FV core build |
| build stamp `20250709-103311` | `0x315134` | app build timestamp |

No update URL is present in plaintext → it comes from `product.dat` or the control plane
`[INFERENCE]`. Version resource: `paddr 0x3c45f4`, 92 B, `VS_FIXEDFILEINFO` only —
`FileVersion = ProductVersion = 4.9.6.0`; **no StringFileInfo**, hence no CompanyName/ProductName.

### 6.2 macOS — Sparkle 2.3.0

- `Sparkle.framework` **2.3.0** (`CFBundleVersion 2021`), `Autoupdate` + `Updater.app` bundled.
- App `Info.plist` has **no `SUFeedURL`**; the app passes the appcast URL at runtime
  (`checkForUpdatesAtAppcastURL:withUserAgent:httpHeaders:inBackground:requiresSilentInstall:` is
  present in the Sparkle binary). Feed URL itself `[UNKNOWN]` (expected to come from `product.dat` or
  the control plane; `%s update web site url = %s\n` @ `0x1ef34a` in the app shows the site URL is
  also runtime-supplied).
- App-side keys present: `AppUpdate` @ `0x1ead0b`, `UpdateRequired` @ `0x1ead15`,
  `UpdateOptional` @ `0x1ed34f`, `checkUpdate` @ `0x1ed8d3`, `updateResponseApiLoginSync` @ `0x1ed1fd`.
  **Absent:** `UpdateChecksum`, `OnlineUpdate` (Windows-only flow).
- Core stamp `20250708-1` @ `0x1fccf4` (x86_64 slice).

### 6.3 Linux — notify only

`\e[0;36mA new version %s is available, please download from our web site.\e[0m\n` @ `0x28fc88`.
No self-update, no checksum, no download code path (no `/files/downloads` strings in the binary).
Version negotiation happens through the control plane (`SyncVersion` @ `0x29319c`).

### 6.4 Android — in-app APK download + installer intent

- `UpdateActivity` (`com.fvcorp.android.fvclient.activity.UpdateActivity`) + `REQUEST_INSTALL_PACKAGES`
  permission → the app downloads and prompts to install an APK itself.
- DEX strings: `downLoadUrl` @ `0x3ebe03`, `getAppVersion` / `getAppVersionCode` /
  `getAppVersionName`, `appVersion`, `https://www.91ajs.com` @ `0x3f8e1b`.
- `billing.properties`: `version=9.1.0`, `client=billing`, `billing_client=9.1.0` (Google Play Billing
  client 9.1.0).
- Core stamp `20260525-1` @ `0xc125e` in `libfvcore.so` (arm64).

## 7. Version / feature diff

Baseline column = the live-tested Linux `4.2.3.0` (core `20241105-1`) from
`research/82-live-20261023/REPORT.md` and `research/30-linux-static/REPORT.md`.

| Property | Windows 4.9.6.0 | macOS 4.9.6.0 | Linux 4.3.0.1 | Android 4.12.2.0 | Linux 4.2.3.0 (baseline) |
|---|---|---|---|---|---|
| App version anchor | VERSION resource `4.9.6.0` (`0x3c45f4`), literal `0x3131e4` | `CFBundleVersion 4.9.6.0` / `ShortVersion 4.9.6` | string `4.3.0.1` @ `0x2902b9` | `versionName 4.12.2.0`, `versionCode 134` | `4.2.3.0` |
| FV core stamp | `20250708-1` @ `0x31ffe8` | `20250708-1` @ `0x1fccf4` | `20260525-1` @ `0x292860` | `20260525-1` @ `0xc125e` | `20241105-1` |
| Toolchain | MSVC 19.29.30136 / VS2019 16.11 | clang, Xcode 16.2, SDK 15.2 | GCC 9.4.0, static | Java/Kotlin DEX + NDK `.so` | GCC 9.4.0, static |
| UI layer | Sciter 4.4.8.33 + `res.fvr` (137 files) | Sciter dylib + `res.fvr` (same 137 names) | CLI + `/etc/ajiasu.conf` | native Android UI + `resources.arsc` | CLI |
| Architectures | PE32 i386; `sysproxy`/`tap` dirs for amd64+arm64+i386 (`sysproxy` arm64 is ARM32) | universal x86_64 + arm64 | amd64, x86, aarch64 tarballs | arm64-v8a, armeabi-v7a, x86, x86_64 | amd64 (tested), 5 arches published |
| Signing | Authenticode EV GlobalSign, valid to 2027-03-14 | Developer ID `N82WKSN6R7`, valid to **2026-10-30** | none | self-signed `CN=Shawn`, v1+v2 | none |
| Control hosts in plaintext | **no** (`product.dat`) | **no** (`product.dat`, same 818 B blob) | **yes**, 4 hosts | **yes**, 4 hosts (DEX) | yes, 4 hosts |
| `/client.php` | `0x31641c` | `0x1ef0e5` | `0x2932d9` | `0x37d776` (dex), `0xd0332` (core) | yes |
| `ApiUrls` / `FetchedUrlHosts` / `SyncVersion` | yes (`0x31e31c` / `0x31e12c` / `0x31e2a4`) | yes (×2) | yes (`0x293211` / `0x293044` / `0x29319c`) | yes (native core) | yes |
| `X-Sig` / `X-Timestamp` | yes | yes | yes | yes, **core only** (absent from DEX) | yes |
| Keepalive/session tokens `kapf` / `cseid` / `peip` | yes | yes | yes (`0x290573` / `0x290592` / `0x2905b0`) | **absent on ARM ABIs**, present on x86/x86_64 | yes |
| NAT64 detection (`Nat64Detector`) | yes | yes | yes @ `0x293e48` | yes (core strings) | yes (prior report `0x28e708`) |
| TUN / route layer | TAP-Windows `tap0901` + `netsh` + Windows Firewall COM | `ifconfig`/`networksetup` exec + setuid `proxy_conf_helper` + privoxy (`--privoxy-port`); tun-device creation not string-visible `[UNKNOWN]` | `/dev/net/tun`, `/etc/resolv.conf` rewrite | Android `VpnService` (`FVService`) | `/dev/net/tun`, resolv.conf rewrite |
| System proxy | `sysproxy.exe` (WININET) | `proxy_conf_helper` (CFNetwork `kCFNetworkProxies*`) | none — userland SOCKS5/HTTP listener | via `VpnService` | userland SOCKS5/HTTP |
| Local proxy modes | FV core `FVClientProxySocks` / `FVDnsProxy` present | `FVClientProxy`, privoxy listen addr | `protocol = {udp\|tcp\|lwip\|socks5\|http\|proxy}` | core strings only; UI `[UNKNOWN]` | same protocol set |
| Update mechanism | in-app download + `UpdateChecksum`, arch-keyed | Sparkle 2.3.0 appcast (URL runtime) | notify-only message | `UpdateActivity` + `downLoadUrl` + installer intent | notify-only |
| Third-party SDKs | Sciter, TAP-Windows (GPLv2), libcurl, mbedTLS, libevent, lwIP | + Sparkle 2.3.0 | libevent, mbedTLS, lwIP, curl | Umeng (6 hosts), Alipay, WeChat/QQ/Weibo/Kuaishou, xCrash 3.1.0, Play Billing 9.1.0 | libevent, mbedTLS, lwIP |
| Config blob | `product.dat` 818 B (encrypted) | same file, same hash | none | none | none |

**Platform-pair deltas inside the same version.** Windows vs macOS `res.fvr` have the same 137 member
names; 9 members differ by CRLF→LF only, and exactly one member differs in content:
`dialog-setting.htm` — the macOS build moves the reconnect slider height from inline markup
(`<div id="reconnect" style="height: 360dip">`) to script (`$("#reconnect").css("height","360dip")`
plus a `sliderMin` variable). `lang.fvr` member content is byte-identical (10,832 B); the archive hash
differs only because of ZIP metadata.

## 8. Security-relevant observations (triage level, not a vulnerability report)

1. **macOS installs a setuid-root helper.** `Contents/Resources/install_helper.sh` copies
   `proxy_conf_helper` into `/Library/Application Support/AJiaSu/`, then `chown root:admin` and
   `chmod +s` (setuid bit). The script is the shadowsocks `install_helper.sh` with its header intact
   (`#  shadowsocks`, `#  Created by clowwindy on 14-3-15`), and the helper's
   usage string advertises `-t AJiaSu|FlyVPN`, i.e. the same binary serves a second brand. A parsing
   or path bug in that helper would be a local privilege-escalation primitive.
2. **Windows runs elevated and rewrites host firewall state.** `AJiaSu.exe` manifest is
   `requireAdministrator`; the binary contains a full Windows Firewall rule manager
   (`Processing Windows Firewall for XP` @ `0x315658`, `CoCreateInstance for Firewall Rule failed:
   0x%08x` @ `0x315a60`, `Enum Windows Firewall rules, found possible name for us ...`) plus `netsh`
   (`cmd: netsh %s` @ `0x315ac8`) and the TAP-Windows driver installer.
3. **Android trusts cleartext and installs packages.** `usesCleartextTraffic=true`,
   `REQUEST_INSTALL_PACKAGES`, `QUERY_ALL_PACKAGES`, `FOREGROUND_SERVICE_SYSTEM_EXEMPTED`,
   `com.google.android.gms.permission.AD_ID`; 17 exported components; `allowBackup=false`,
   `extractNativeLibs=false`. The self-update path (`UpdateActivity` + `downLoadUrl` + installer
   intent) is the highest-value target in the app.
4. **Third-party secrets ship in the APK in plaintext.** `assets/ShareSDK.xml` contains
   AppId/AppKey/AppSecret values for WeChat, QQ, QZone, Sina Weibo and Kuaishou (six entries, five
   distinct credentials). Values are deliberately **not** reproduced here; they are public-in-APK
   material, not account credentials.
5. **Control-channel TLS is not verified by the shipping client.** Prior live evidence
   (`research/82-live-20261023/REPORT.md`, F1): all four control hosts present a self-signed
   `CN=localhost` leaf and the app connects anyway. All four builds carry the same control client
   (`/client.php`, `ClientApiLogin/Login`, `X-Sig`), so the same trust-all behaviour is expected to be
   shared `[INFERENCE]`; it was **not** re-tested in this static pass, and no pinning table was
   searched for.
6. **Signing key hygiene differs sharply by platform.** Desktop builds are EV/Developer-ID signed by
   the same corporate entity; the Android build is signed by a 2017 self-signed `CN=Shawn` key that is
   also the update trust anchor for the website-distributed APK. The macOS Developer ID certificate
   expires **2026-10-30**, six days after this triage — after that date the existing 4.9.6.0 build can
   no longer be re-signed and Gatekeeper assessment of new downloads degrades.

## 9. Explicit unknowns

| # | Unknown | Why it is unresolved | What would resolve it |
|---|---|---|---|
| U1 | `product.dat` encryption scheme and key | 818 B, entropy 7.7356, no magic, no plaintext markers, identical on Windows and macOS; the decryptor is inside the signed PE/Mach-O | RE of the `product.dat` reader in `AJiaSu.exe` (xrefs to `0x317dfc`) or a decrypted dump from a sandboxed run |
| U2 | Windows/macOS control host list | no plaintext hosts in either binary (ASCII + UTF-16 checked); only `ApiUrls`/`FetchedUrlHosts` consumers | U1, or a sandboxed capture of the first control request |
| U3 | macOS Sparkle appcast URL and signature policy | no `SUFeedURL` in `Info.plist`; URL supplied at runtime | U1/U2, or a sandboxed run with a proxy log |
| U4 | Crypto linkage: mbedTLS **and** OpenSSL-derived strings in all four cores | `mbedtls_*` symbols and TLS 1.3 code paths are present, but so are OpenSSL self-test labels (`AES note: using AESNI.`, `CAMELLIA-CBC-%3d (%s):`) in Windows, macOS, Linux and Android cores | symbol/`.comment` analysis of the static archives, or build-system evidence |
| U5 | Why ARM Android cores lack `kapf`/`peip` while x86/x86_64 cores have them (same `20260525-1` stamp) | string-table observation only; both ARM ABIs and both x86 ABIs agree internally | disassembly of the keepalive path in `libfvcore.so` per ABI |
| U6 | Whether Android's Java layer or the native core drives the control plane | `ClientApiClientSetting`/`ClientApiReport` exist in both DEX and core; `X-Sig` only in the core | decompile `com.fvcorp.android.fvclient` HTTP layer |
| U7 | Why the `arm64` sysproxy is a 32-bit ARMNT binary | PE machine field is `0x01c4` (ARMNT, PE32) while `tap/arm64/*` is `0xaa64` (ARM64, PE32+); the observation is solid, the intent is not | read the NSIS install script's per-arch selection, or test on Windows-on-ARM |
| U8 | Exact control-plane field set in 4.9.6.0 vs 4.2.3.0 | this slice only checked token presence, not request construction | `research/40-protocol` diff plus a live login on 4.9.6.0 |
| U9 | Linux 4.3.0.1 runtime behaviour (modes, DNS, NAT64) | not executed — execution needs the separate coordinated run | coordinated dynamic run |
| U10 | Whether a Play-distributed APK is signed with a different key | only the `ajs-official-website` APK was analysed | compare with the Play listing's signing certificate |
| U11 | `capa` capability set for `AJiaSu.exe` | `capa -q` did not finish in ~40 min and was cancelled | longer run on a dedicated machine |

## 10. Method (reproducible)

Workstation: Windows 10.0.26300 x64, Git Bash. All commands read-only against
`C:/Users/dwgx1/.dwgx/tmp/ajs-re-20261024a/` and `C:/Users/dwgx1/Downloads/`.

```sh
# hashes and provenance
sha256sum "AJiaSu_4.9.6.0 (1).exe" AJiaSu_4.12.2.0_ajs-official-website.apk AJiaSu_4.9.6.0.dmg ajiasu-*-4.3.0.1.tar.gz
7z x -o_triage/inst "AJiaSu_4.9.6.0 (1).exe"      # 37 files, hashes == exe/ tree

# file types / headers / packers
file exe/AJiaSu.exe exe/sciter.dll linux-*/ajiasu dmg/.../MacOS/AJiaSu apk/lib/arm64-v8a/libfvcore.so
rabin2 -I  <each binary>          # arch, bits, canary, nx, pic, relro, signed, compiler
rabin2 -S  <each binary>          # section table
rabin2 -u  exe/AJiaSu.exe         # resources: 12 ICON, 2 GROUP_ICON, 1 MANIFEST, 1 VERSION
diec -j    <each binary>          # Detect It Easy 3.21 verdicts (§3)

# strings with offsets
rabin2 -zz exe/AJiaSu.exe > _triage/exe.strings.txt      # paddr/vaddr/section/string
rabin2 -zz linux-amd64/ajiasu > _triage/linux-amd64.strings.txt
rabin2 -zz "dmg/.../MacOS/AJiaSu" > _triage/mac.strings.txt
grep -aboF "<string>" <binary>    # exact byte offsets (DEX, PE, ELF)

# Sciter resource packs
7z l exe/res.fvr ; 7z x -o_triage/res exe/res.fvr ; 7z x -o_triage/res-mac dmg/.../Resources/res.fvr
diff --strip-trailing-cr res/dialog-setting.htm res-mac/dialog-setting.htm

# certificates
powershell -NoProfile -Command "Get-AuthenticodeSignature exe\AJiaSu.exe | ConvertTo-Json"   # UTF-8 subject
apksigner verify -v --print-certs AJiaSu_4.12.2.0_ajs-official-website.apk
keytool -printcert -jarfile AJiaSu_4.12.2.0_ajs-official-website.apk
# Mach-O: parse LC_CODE_SIGNATURE -> SuperBlob -> slot 0x10000, strip 8-byte 0xfade0b01 header
openssl pkcs7 -inform DER -in _triage/mac_cms_0.der -print_certs -noout

# Android metadata
aapt2 dump badging AJiaSu_4.12.2.0_ajs-official-website.apk
aapt2 dump xmltree --file AndroidManifest.xml AJiaSu_4.12.2.0_ajs-official-website.apk
```

Tool versions: radare2 6.2.2, Detect It Easy 3.21, 7-Zip 26.02, capa (cancelled), aapt2/apksigner
35.0.0, OpenSSL 3.5.7, Python 3 (offset/entropy/Mach-O parsing only).

Cross-references: `research/82-live-20261023/REPORT.md` (4.2.3.0 live baseline, cert pin, node limit),
`research/40-protocol/PROTOCOL.md` (control/data plane, host failover), `research/81-protocol-gaps/REPORT.md`
(gaps B1–B5, U6–U9), `research/20-windows-static/INVENTORY.md` (4.9.6.0 Windows component map),
`research/30-linux-static/REPORT.md` (4.2.3.0 → 4.3.0.1 token matrix).
