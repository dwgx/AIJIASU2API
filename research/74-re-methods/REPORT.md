# AJS-14 — Reverse-engineering method playbook (AJiaSu SDK/protocol)

Status: 2026-10-23. Read-only research output. No vendor binary was executed, no live
AJiaSu account or service was touched, and nothing outside `research/74-re-methods/` was
modified. Inputs re-used: `research/30-linux-static/REPORT.md`,
`research/50-windows-protocol/REPORT.md`, `research/60-dynamic/REPORT.md`,
`research/40-protocol/PROTOCOL.md`, plus fresh read-only checks recorded in
`## Verification of commands`.

## Answer

1. **The method set is 54 methods in 7 layers.** Static (15), dynamic (10), network (8),
   hook/inject (5), patch (5), fuzz/differential (6), request-research (5). Each entry below
   carries *when*, *exact command*, *what it proves*, *failure modes*, and *safety*. Counts are
   machine-checked by `validate.py` (54 ≥ 20 required).
2. **Five methods carry almost all of the AJiaSu work.** `S04` strings-offset mining,
   `S05` IDA headless, `D01` gdb scripting, `N03`/`F04` intercept + mock server, and `P01`
   modulus replacement. They produced every confirmed protocol fact we have
   (`research/60-dynamic/REPORT.md`; `research/50-windows-protocol/REPORT.md`).
3. **The single highest-value anchor is the 309-digit decimal RSA modulus**, present
   byte-identically in both builds: Linux file offset `0x28a030`, Windows file offset
   `0x314468` (both re-verified 2026-10-23, `S14`). Replacing it with a lab key whose modulus is
   also 309 digits is what turned `k=` from a guess into a proof of RSA-OAEP-SHA1
   (`research/60-dynamic/mock/patch_rsa.py`).
4. **The second anchor is the `product.dat` KDF**: `AES-128-CTR(key=MD5("[CONFIG]"), IV=0)`,
   818 B in, 818 B out, plaintext `[CONFIG]\r\n…`. Re-verified by decryption this session
   (plaintext sha256 `2b618010b49d66922d778beda9aae7117084a46fd6008706e6f5c9c53291183e`).
   This is the cheapest way to recover `ServerApiUrls` without touching the network.
5. **Most methods are three-platform; 12 are not.** Counting the matrix below: 42 of 54 work on
   Windows, Linux and macOS; 4 are two-OS (S03, D02, D10, P04); 8 are single-OS (D03, D04, D07,
   D08, H01, H02, H03, H05). The three that must exist on every host are `S04`, `S14`, and
   `F04`, because they need no debugger, no licence, and no root.

## Method inventory

Status key: `ran` = a representative command was executed this session and its output is in
`## Verification of commands`; `doc` = tool present and command specified, not executed here;
`no-install` = tool absent on this host, official source given, command `[UNVERIFIED]`.

| ID | Layer | Method | Primary tool | What it proves | Status |
|---|---|---|---|---|---|
| S01 | Static | Artifact intake and hash manifest | `sha256sum` / `Get-FileHash` | identity, version, size before any analysis | ran |
| S02 | Static | PE structure and import surface | `objdump -p`, `pefile`, PE-bear | PE32 vs PE32+, subsystem, which crypto/net DLLs are linked | ran |
| S03 | Static | ELF structure and symbol state | `readelf`, `nm`, `file` | static/dynamic, PIE, stripped, entry point | ran |
| S04 | Static | Strings offset mining | `strings -t x -n 5` + `grep` | protocol literals and their file offsets | ran |
| S05 | Static | IDA Pro auto-analysis + headless scripting | `idat.exe -A -S` | call graph, decompiled crypto/framing functions | doc |
| S06 | Static | Ghidra headless decompilation | `analyzeHeadless` | second-opinion decompilation, no licence needed | no-install |
| S07 | Static | radare2 / rabin2 CLI triage | `rabin2 -Izz`, `r2 -Aqc` | sections, imports, xrefs in a scriptable shell | no-install |
| S08 | Static | GoReSym / Go build-info recovery | `GoReSym`, `go version -m` | Go build path, package list, function names | no-install |
| S09 | Static | FLOSS stack-string recovery | `floss -n 6` | strings that never exist contiguously in the file | no-install |
| S10 | Static | capa capability map | `capa -vv` | crypto/network/anti-debug behaviour clusters | no-install |
| S11 | Static | YARA-X signature scan | `yr scan`, `yr compile` | family/version identification, regression rules | no-install |
| S12 | Static | RTTI / vtable recovery | IDA `class_0x*` / `ida_rtti` | class boundaries, virtual dispatch targets | doc |
| S13 | Static | Signer, catalog, installer inspection | `sigcheck -a`, `7z x` | publisher, signing chain, embedded payload layout | doc |
| S14 | Static | Crypto constant location | Python decimal/byte search | modulus, KDF key, IV, exponent offsets | ran |
| S15 | Static | Cross-build function diff | `radiff2 -C`, Diaphora, BinDiff | which functions are shared between Linux/Windows builds | doc |
| D01 | Dynamic | gdb batch tracing | `gdb -batch -x *.gdb` | register/memory values at chosen call sites | doc |
| D02 | Dynamic | lldb batch tracing (macOS) | `lldb -b -o` | same on Mach-O builds | doc |
| D03 | Dynamic | x64dbg scripted logging | `x64dbg.exe` + script | runtime field values, conditional breakpoints | doc |
| D04 | Dynamic | WinDbg / cdb automation | `cdb -c` | kernel/user breakpoints, crash-dump triage | doc |
| D05 | Dynamic | Frida spawn/attach hooks | `frida -f -l hook.js` | live argument/return values without a debugger | doc |
| D06 | Dynamic | Frida Stalker coverage trace | `Stalker.follow` | executed basic blocks, unpacking/decrypt loops | doc |
| D07 | Dynamic | API Monitor parameter tracing | API Monitor GUI | Win32 call parameters with decoded structures | doc |
| D08 | Dynamic | Procmon + ETW capture | `Procmon.exe`, `wpr`, `logman` | file/registry/network side effects, DNS, TLS events | doc |
| D09 | Dynamic | Process/handle/device inspection | `procexp64`, `handle64` | loaded DLLs, open sockets, device handles | doc |
| D10 | Dynamic | strace / ltrace / perf | `strace -f`, `ltrace -f`, `perf trace` | syscall and libc sequence on Linux | ran |
| N01 | Network | Wireshark/Npcap capture + TLS keylog | `tshark`, `SSLKEYLOGFILE` | on-wire framing and decrypted TLS payload | doc |
| N02 | Network | tcpdump capture | `tcpdump -i lo` | same on Linux/WSL without GUI | doc |
| N03 | Network | mitmproxy intercept + addon rewrite | `mitmdump -s addon.py` | request/response mutation of `/client.php` | doc |
| N04 | Network | Burp Suite intercept/replay | Burp Proxy/Repeater | manual field-level replay of control API | doc |
| N05 | Network | Charles Proxy HTTP/2 | Charles GUI | macOS-friendly HTTP/2 intercept | doc |
| N06 | Network | openssl s_client chain/pin probe | `openssl s_client` | certificate chain, ALPN, pinning-relevant SPKI | ran |
| N07 | Network | HTTP/2, WebSocket, gRPC, Protobuf decode | `nghttp`, `websocat`, `grpcurl`, `protoc` | which of those transports is actually in use | doc |
| N08 | Network | Certificate pinning analysis | Frida hook of verify callbacks | whether a lab CA will be accepted | doc |
| H01 | Hook/Inject | DLL injection (Windows) | GH-Injector | load a logging DLL into the target | doc |
| H02 | Hook/Inject | LD_PRELOAD shim (Linux) | `gcc -shared` + `LD_PRELOAD` | interpose `connect`/`send`/`SSL_write` | doc |
| H03 | Hook/Inject | DYLD_INSERT_LIBRARIES (macOS) | `DYLD_INSERT_LIBRARIES` | same on Mach-O, incl. SIP caveats | doc |
| H04 | Hook/Inject | IAT / inline hooks | MinHook, Detours | stable long-running interception | doc |
| H05 | Hook/Inject | eBPF uprobe/tc (Linux) | `bpftrace` | kernel-side observation without touching the process | doc |
| P01 | Patch | RSA modulus replacement | `patch_rsa.py` | which key the client actually uses for `k=` | doc |
| P02 | Patch | NOP / length-preserving byte patch | IDA patch + `xxd` | bypass a branch without breaking layout | doc |
| P03 | Patch | Checksum / CRC repair | `pefile` checksum, custom CRC | patched binary still loads | doc |
| P04 | Patch | Lab re-signing | `osslsigncode`, `codesign -s -` | signed artifact for lab-only execution | doc |
| P05 | Patch | Rollback and integrity manifest | `sha256sum`, byte-diff script | patch is confined and reversible | doc |
| F01 | Fuzz/Diff | Capture replay | `tcpreplay`, Python socket replay | server tolerates a replayed frame | doc |
| F02 | Fuzz/Diff | Field-level mutation fuzzing | custom Python / boofuzz | which fields the parser validates | doc |
| F03 | Fuzz/Diff | Grammar-aware fuzzing | field-map generator | parser depth limits, length-field trust | doc |
| F04 | Fuzz/Diff | Mock server harness | `mock_fv.py`, `mock_control.py`, Go emulator | client accepts our encoding end-to-end | doc |
| F05 | Fuzz/Diff | Response diffing | golden transcript compare | regression detection across builds | doc |
| F06 | Fuzz/Diff | State-machine replay | ordered frame injection | required ordering, half-close dependency | doc |
| R01 | Request | Control API intercept + rewrite | mitmproxy addon | which parameters the server honours | doc |
| R02 | Request | Local mock API + hosts redirect + lab CA | `mock_control.py`, `/etc/hosts`, `make_certs.sh` | client's JSON contract without the live service | doc |
| R03 | Request | Certificate substitution + pin bypass | lab CA + Frida | TLS trust model of the control plane | doc |
| R04 | Request | Session/lease manipulation | replayed `sid` | sid lifetime, concurrency, reuse rules | doc |
| R05 | Request | Rate-limit and error-path probing | scripted replay | `code=200` gate, error tokens, backoff | doc |

## Method detail

Path shorthand used below (all local, no network):

```bash
WIN='C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023/AJiaSu.exe'
LIN='/mnt/c/Users/dwgx1/AppData/Local/Temp/ajs-linux-20261023/ajiasu'   # WSL view
EMU='D:/Project/LovePlusSpeedToProxy/emulator'
```

### S01 Artifact intake and hash manifest

- **When:** first contact with any build, before opening a disassembler.
- **Run:**
  ```bash
  sha256sum "$WIN" "$LIN"; stat -c '%n %s' "$WIN" "$LIN"
  powershell -NoProfile -Command "Get-FileHash -Algorithm SHA256 '$WIN'"
  ```
- **Proves:** identity and version lineage; the hash is the only stable key across builds (the
  file name is reused by every release).
- **Fails:** a re-packed installer or an extracted payload has a different hash than the vendor
  download; always hash the *payload*, not the installer, when comparing builds.
- **Safety:** read-only. Write the hash into `hashes.tsv` before any other action.

### S02 PE structure and import surface

- **When:** the target is a Windows PE and you need the section map, subsystem, and linked crypto/net DLLs.
- **Run:**
  ```bash
  objdump -p "$WIN" | grep -E "ImageBase|Subsystem|DLL Name"
  python -c "import pefile;pe=pefile.PE(r'$WIN');print(hex(pe.FILE_HEADER.Machine),pe.FILE_HEADER.NumberOfSections)"
  ```
  GUI alternative: PE-bear or PEview for the section/import tree.
- **Proves:** PE32 vs PE32+, static vs dynamic CRT, and which primitives are present
  (`bcrypt.dll` ⇒ Windows CNG; `WS2_32.dll`/`IPHLPAPI.DLL` ⇒ sockets + interface enumeration).
- **Fails:** `objdump` from w64devkit reports the import directory but not delay-load imports;
  `pefile` needs `fast_load=False` for the full import table.
- **Safety:** read-only; PE-bear must be opened on a copy if you use its hex editor.

### S03 ELF structure and symbol state

- **When:** the target is an ELF and you must know whether symbols survive.
- **Run:**
  ```bash
  file "$LIN"; readelf -hSW "$LIN" | head -40; nm -C "$LIN" 2>&1 | head
  ```
- **Proves:** `EXEC` vs `DYN` (non-PIE vs PIE), `stripped` vs symbols, static vs dynamic linking,
  and the entry point used to anchor later breakpoints.
- **Fails:** `readelf` is absent on macOS (use `otool -l`); `nm` on a stripped static binary prints
  "no symbols" and that is the expected result, not an error.
- **Safety:** read-only.

### S04 Strings offset mining

- **When:** immediately after S01–S03; it is the cheapest way to locate protocol code.
- **Run:**
  ```bash
  strings -t x -n 5 "$WIN" | grep -F -e "/client.php" -e "X-Sig: " -e "code=200" -e "OPV2" -e "06ka4z0dc6ga02c"
  strings -t x -n 2 "$LIN" | grep -E "^( *[0-9a-f]+ )?(s|n|k|l|_)=" | head
  ```
- **Proves:** which protocol generation a build speaks and the exact file offset of each literal
  (verified 2026-10-23: `/client.php 0x31641c`, `X-Sig 0x31e144`, `code=200 0x3217c4`,
  `OPV2 0x3216d0` in the Windows build).
- **Fails:** literals may be UTF-16 (use `strings -el`), split across basic blocks, or built on the
  stack (see S09); offsets are file offsets, not virtual addresses — add the section delta.
- **Safety:** read-only; do not paste full string dumps into reports (they may contain PII).

### S05 IDA Pro auto-analysis + headless scripting

- **When:** you need a call graph and decompiled bodies, or you must re-run an analysis reproducibly.
- **Run:**
  ```bash
  "D:/Software/Tool/Debugger/IDA Professional 9.2/idat.exe" -A -S"dump.py" -Lida.log "$WIN"
  ```
  with `dump.py` iterating `idautils.Functions()` and writing `hexrays.decompile(f)` per function.
  Interactive: `ida.exe` (9.x unified 64-bit), then `View > Open subviews > Functions`.
- **Proves:** builder/parser pairs and the crypto call chain (for AJiaSu: `0x552ce0` build,
  `0x552640` fetch, `0x520df0` product.dat decrypt).
- **Fails:** `idat.exe -A` still needs a valid licence; Hex-Rays is a separate add-on; analysis of a
  4 MB static binary can take minutes and a large `.i64` on disk.
- **Safety:** IDA writes `.i64/.id0/.nam` next to the input — always copy the binary into the lab
  directory first.

### S06 Ghidra headless decompilation

- **When:** no IDA licence, or a second decompiler opinion on an ambiguous function.
- **Run:**
  ```bash
  analyzeHeadless C:/lab/ghidra ajsproj -import "$WIN" -scriptPath C:/lab/scripts \
    -postScript GhidraDump.py -deleteProject
  ```
- **Proves:** the same call graph independently; useful when IDA's type propagation hides an argument.
- **Fails:** Ghidra's auto-analysis misses MSVC RTTI by default; the decompiler output is not
  byte-accurate for hand-written asm. Ghidra is **not installed on this host** (`[UNVERIFIED]`).
- **Safety:** read-only on the input; keep projects out of the repo.

### S07 radare2 / rabin2 CLI triage

- **When:** fast, scriptable triage inside a terminal or CI, without a GUI.
- **Run:**
  ```bash
  rabin2 -Izz "$LIN" | head -40
  r2 -2Aqc 'afl~crypto;izz~code=200;axt @ 0x28eecf' "$LIN"
  ```
- **Proves:** imports/sections/strings and cross-references to a literal in one shell line.
- **Fails:** `aaa` on a 3.5 MB static binary is slow; the decompiler (`pdc`) is weaker than Hex-Rays.
  radare2 is **not installed on this host** (`[UNVERIFIED]`; `r2pm -ci r2dec` adds a decompiler).
- **Safety:** read-only; `-w` (write mode) must never be used on an original.

### S08 GoReSym / Go build-info recovery

- **When:** the target (or our own emulator) is Go and the symbols were stripped.
- **Run:**
  ```bash
  GoReSym.exe -t -d "$EMU/cmd/ajsproxy/ajsproxy.exe"
  go version -m "$EMU/cmd/ajsproxy/ajsproxy.exe"
  ```
- **Proves:** build path, Go version, module list, and function names recovered from `pclntab`.
- **Fails:** GoReSym is Windows/Linux/macOS but version-sensitive (Go 1.20+ changed `moduledata`);
  `go version -m` only works when build info was not stripped with `-buildinfo`.
- **Safety:** read-only.

### S09 FLOSS stack-string recovery

- **When:** `strings` finds nothing for a literal you know exists (stack-built or obfuscated).
- **Run:**
  ```bash
  floss.exe -n 6 --no-static-strings -j floss.json "$WIN"
  ```
- **Proves:** strings assembled at runtime, which often include the real endpoint or key material.
- **Fails:** FLOSS emulates only a subset of x86/x64 and can time out on large functions; Go and
  .NET binaries need different tooling. **Not installed here** (`[UNVERIFIED]`).
- **Safety:** FLOSS executes code in an emulator, not on the host CPU — still run it on a copy and
  in a VM if the sample is untrusted.

### S10 capa capability map

- **When:** you want a behaviour inventory (crypto, anti-debug, C2) before reading code.
- **Run:**
  ```bash
  capa.exe -vv "$WIN" -o capa-ajs.txt
  ```
- **Proves:** which capability rules match, e.g. AES/RSA constants and WinINet/WinSock usage; good
  for detecting a protocol rewrite between versions.
- **Fails:** capa is rule-coverage-bound; a custom protocol with no rule shows as "unknown". **Not
  installed here** (`[UNVERIFIED]`).
- **Safety:** read-only.

### S11 YARA-X signature scan

- **When:** you need a repeatable "is this build the one we analysed?" check, or a family rule.
- **Run:**
  ```bash
  yr compile rules/ajs.yar -o rules/ajs.yarc
  yr scan rules/ajs.yar "$WIN" "$LIN"
  ```
  Minimal rule body: `strings: $a = "/client.php" $b = "code=200" $c = "OPV2" condition: 2 of them`.
- **Proves:** version/family identity in one command, suitable for CI.
- **Fails:** string rules are trivially evaded and produce false positives on shared libraries;
  YARA-X syntax differs slightly from YARA 4 (`yr` vs `yara`). **Not installed here** (`[UNVERIFIED]`).
- **Safety:** read-only.

### S12 RTTI / vtable recovery

- **When:** the binary is C++ and you need class boundaries and virtual dispatch targets.
- **Run:** IDA with `class_0x*.py`/`ida_rtti` (or the built-in Class Explorer), or:
  ```bash
  r2 -2Aqc 'av;avj' "$WIN"      # radare2 vtable list, when RTTI is parsed
  ```
  Manual MSVC chain: find the `.?AV...@@` type descriptor, follow `COL` → vtable.
- **Proves:** which class owns a method (`FVClientVpn 0x72499c`, `FVClientProxy 0x7208f0` in
  AJiaSu) and therefore whether a code path is proxy or VPN.
- **Fails:** `FVClientProxySocks`/`FVClientProxyLwip` have type descriptors but no COL, so their
  vtables are unrecoverable; Itanium RTTI (Linux/macOS) uses a different layout.
- **Safety:** read-only.

### S13 Signer, catalog, and installer inspection

- **When:** you need the publisher identity, the signing chain, or the embedded payload of an installer.
- **Run:**
  ```bash
  sigcheck64.exe -a -h -nobanner "$WIN"
  powershell -NoProfile -Command "Get-AuthenticodeSignature '$WIN' | Format-List"
  7z x research/AJiaSu_4.9.6.0.exe -oextract    # NSIS: $PLUGINSDIR, AJiaSu.exe, product.dat
  ```
- **Proves:** whether the binary is signed and by whom, and that the NSIS installer carries the
  real payload plus `product.dat`, `sciter.dll`, `tap/`, `sysproxy`.
- **Fails:** catalog-signed binaries show "NotSigned" from `Get-AuthenticodeSignature` unless you
  resolve the catalog; `sigcheck -a` needs the Sysinternals EULA flag. `sigcheck`/`7z` are **not
  installed here** (`[UNVERIFIED]`).
- **Safety:** extraction writes files — do it in the lab directory, never in the repo.

### S14 Crypto constant location

- **When:** you suspect a fixed key/modulus and want its offset without a debugger.
- **Run:**
  ```bash
  python - <<'PY'
  import re
  from pathlib import Path
  MOD=b'141601187905926909444644730286409666755495300050721564398199769080746616339148556683219048017120018748016428099359153205651362664001287890722535799466700786136519566714160932079339659299698611914846217596226121056961277268141789905486380539961102053398435632146710731361922836526658451793921073088741138357721'
  for p in (r'C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023/AJiaSu.exe',
            r'C:/Users/dwgx1/AppData/Local/Temp/ajs-linux-20261023/ajiasu'):
      b=Path(p).read_bytes(); print(p, [hex(m.start()) for m in re.finditer(re.escape(MOD), b)])
  PY
  ```
- **Proves:** the modulus offset per build (verified: Windows `0x314468`, Linux `0x28a030`) and,
  with a byte search for the KDF input, the `product.dat` password.
- **Fails:** the constant may be hex/base64-encoded or split; the 309-digit form is build-specific.
- **Safety:** read-only; never print a recovered private key into a report.

### S15 Cross-build function diff

- **When:** a new vendor build appears and you need to know what changed in the protocol.
- **Run:**
  ```bash
  radiff2 -C "$LIN" "$WIN" | head -40          # or Diaphora/BinDiff inside IDA
  ```
- **Proves:** which functions are structurally identical across Linux/Windows (the FV core is the
  same C++ codebase, so matching is high) and which are new.
- **Fails:** different compilers/optimisation levels produce noisy matches; static linking makes
  library functions dominate the diff.
- **Safety:** read-only.

### D01 gdb batch tracing

- **When:** Linux build, and you need register/memory values at a specific call site reproducibly.
- **Run:**
  ```bash
  gdb -q -batch -x research/60-dynamic/mock/dump_ctr.gdb --args ./ajiasu
  ```
  The existing scripts (`dump_ctr.gdb`, `dump_authctr.gdb`, `dump_resp.gdb`, `trace_parse.gdb`)
  break at `0x451932` and run `-c /tmp/ajs-lab/ajs.conf connect Test*`, i.e. against the mock.
- **Proves:** the AES-CTR key/IV at the moment of framing, and the exact response-parsing state.
- **Fails:** static non-PIE is easy to break on; PIE needs `break *($base+0x…)`; a stripped binary
  gives no function names, so breakpoints are addresses tied to one build.
- **Safety:** gdb runs the target — point it at the mock config only; `set follow-fork-mode` and
  `catch syscall` can slow a network app to a crawl.

### D02 lldb batch tracing (macOS)

- **When:** macOS build or a Mach-O reimplementation.
- **Run:**
  ```bash
  lldb -b -o "br set -n SSL_write" -o "br command add 1 -o 'memory read --size 1 --format x --count 64 $rdx'" -o run ./ajiasu
  ```
- **Proves:** the same values as D01 on Mach-O, including the Security.framework verify path.
- **Fails:** hardened runtime + SIP block attaching to Apple-signed binaries; `task_for_pid` needs
  entitlements. No macOS build of AJiaSu was available (`[UNKNOWN]`).
- **Safety:** debugging requires `DevToolsSecurity -enable` or root; do it in a VM.

### D03 x64dbg scripted logging

- **When:** Windows build, userland, and you want conditional logging without WinDbg syntax.
- **Run:**
  ```text
  x64dbg.exe -p <pid>            # attach after starting under the lab config
  scriptload C:\lab\ajs.txt      # commands: bp 0x552640, log "{mem:utf16@rsp}", run
  ```
- **Proves:** the fields passed to `FVProxyRequestHeader::build` (`0x552ce0`) and the response
  buffer parsed by `0x552640`.
- **Fails:** ASLR moves the image; use `bp AJiaSu.exe:0x552ce0` (module-relative) or disable ASLR.
  Scripted logging is slow on hot loops.
- **Safety:** attaching suspends threads briefly; do not attach to a production client with a live
  session.

### D04 WinDbg / cdb automation

- **When:** you need kernel-adjacent views (Winsock, AFD, ETW) or crash-dump triage.
- **Run:**
  ```text
  cdb.exe -o -c ".logopen C:\lab\ajs.log; sxe ld:ws2_32; g; lm; q" AJiaSu.exe
  cdb.exe -z C:\lab\crash.dmp -c "!analyze -v; q"
  ```
- **Proves:** module load order, loaded DLL list, and the faulting stack of a crash produced by
  malformed frames.
- **Fails:** `cdb` needs the Debugging Tools for Windows (part of the SDK); symbol resolution needs
  a symbol path, and the vendor ships no PDBs, so only exports resolve.
- **Safety:** kernel debugging (`-k`) changes the boot configuration of the machine — never enable
  it on the analysis host without a rollback plan.

### D05 Frida spawn/attach hooks

- **When:** you need argument/return values at scale, or a hook that survives module reloads.
- **Run:**
  ```bash
  frida -f "$WIN" -l hooks/ws2.js --runtime=v8 -o frida.log
  frida -p <pid> -l hooks/fv.js          # attach variant
  ```
  Minimal hook: `Interceptor.attach(Module.getExportByName('ws2_32.dll','send'),
  {onEnter(a){console.log(a[1].toInt32(), hexdump(a[2].readByteArray(a[3].toInt32())))}});`
- **Proves:** live wire bytes and the plaintext before TLS, without a debugger or a patched binary.
- **Fails:** Frida 17 on Windows needs the matching `frida-server`/agent architecture; anti-debug
  checks can detect the injected agent; Go/C++ inlined functions have no export to hook.
- **Safety:** injection changes process behaviour and can trip AV; only on lab copies, and kill the
  session afterwards (`frida-kill -n AJiaSu.exe`).

### D06 Frida Stalker coverage trace

- **When:** the binary decrypts/unpacks itself, or you need the executed block set of one function.
- **Run:**
  ```javascript
  const t = Process.getCurrentThreadId();
  Stalker.follow(t, {events:{call:true}, onCallSummary(s){
    send(Object.entries(s).sort((a,b)=>b[1]-a[1]).slice(0,50));}});
  ```
- **Proves:** which code actually runs (e.g. whether the OPV2 builder is reached on the proxy path).
- **Fails:** 10–50× slowdown and multi-MB logs; Stalker is per-thread, so worker threads must be
  followed individually; ARM64 needs a different `transform`.
- **Safety:** heavy CPU load; bound the trace with `Stalker.unfollow` after N calls.

### D07 API Monitor parameter tracing

- **When:** you want decoded Win32 structures (`sockaddr`, `SECURITY_ATTRIBUTES`) without writing hooks.
- **Run:** GUI: API Monitor → File → Start → `AJiaSu.exe` → enable `Networking` + `Cryptography` groups.
- **Proves:** the exact `connect()` target and `send()` buffers at the API boundary.
- **Fails:** the free version has a call-count cap; it cannot see statically linked libcurl/mbedTLS
  internals, only the Win32 boundary.
- **Safety:** it launches the target — use the lab config and no live account.

### D08 Procmon + ETW capture

- **When:** you need side effects (files, registry, DNS, TCP) rather than code.
- **Run:**
  ```text
  Procmon64.exe /Quiet /Minimized /BackingFile C:\lab\ajs.pml
  wpr -start GeneralProfile -filemode
  logman create trace ajsnet -p "Microsoft-Windows-Winsock-AFD" -o C:\lab\ajs.etl -ets
  ```
- **Proves:** whether the client reads `product.dat`, which hosts it resolves, and when it opens
  the FV socket.
- **Fails:** Procmon's network events are metadata only (no payload); ETW provider names differ
  per Windows build; the ETL needs WPA or `tracerpt` to read.
- **Safety:** Procmon generates large files and can slow the system; stop with
  `Procmon64.exe /Terminate` and delete the PML.

### D09 Process/handle/device inspection

- **When:** you need loaded modules, open sockets, or device handles without a debugger.
- **Run:**
  ```text
  procexp64.exe
  handle64.exe -p AJiaSu.exe -nobanner | findstr /i "tcp \\Device\\"
  ```
  Linux analogue: `ls -l /proc/<pid>/fd; cat /proc/<pid>/maps; ss -tnp`.
- **Proves:** whether a TUN/TAP device or a raw socket is open — i.e. whether the mode is VPN or proxy.
- **Fails:** handle names are truncated; `handle64` needs elevation for some processes.
- **Safety:** read-only.

### D10 strace / ltrace / perf

- **When:** Linux, and you need the syscall/library sequence (connect, TLS write, poll).
- **Run:**
  ```bash
  strace -f -e trace=network,openat -s 256 -o strace.txt ./ajiasu -c /tmp/ajs-lab/ajs.conf connect Test*
  ltrace -f -e "SSL_*+RSA_*+connect" -o ltrace.txt ./ajiasu
  perf trace -p "$(pgrep -n ajiasu)" -o perf.txt
  ```
- **Proves:** connection order (API host → FV endpoint), file reads (`product.dat`), and TLS calls.
- **Fails:** a static binary shows no libc symbols for `ltrace`; `perf` needs `perf_event_paranoid`
  lowered; strace cannot see inside a static mbedTLS.
- **Safety:** ptrace-based tracing is visible to anti-debug checks and slows I/O; run against the mock.

### N01 Wireshark/Npcap capture + TLS keylog

- **When:** you need the actual bytes on the wire, including the FV framing.
- **Run:**
  ```bash
  tshark -i 5 -f "tcp port 443 or tcp port 8443" -w cap.pcapng
  set SSLKEYLOGFILE=C:\lab\sslkeys.log     # then Wireshark: TLS → (Pre)-Master-Secret log filename
  tshark -r cap.pcapng -Y "tcp.port==8443" -x
  ```
- **Proves:** frame boundaries and the `[16B K][4B len][ciphertext]` shape (matches
  `research/60-dynamic/captures/fvmock-1.bin`).
- **Fails:** `SSLKEYLOGFILE` only works if the app calls OpenSSL/NSS/BoringSSL with keylog support;
  mbedTLS has no `SSLKEYLOGFILE` support at all (it exposes `mbedtls_ssl_set_export_keys_cb` instead),
  so AJiaSu's TLS stays opaque to Wireshark. Npcap is Windows-only.
- **Safety:** capturing on a shared interface collects third-party traffic — filter to the lab IPs
  and delete the pcap afterwards.

### N02 tcpdump capture

- **When:** Linux/WSL, headless, or you want a small pcap for a script.
- **Run:**
  ```bash
  tcpdump -i any -s 0 -w cap.pcap 'tcp port 1080 or tcp port 8443 or tcp port 443'
  ```
- **Proves:** same as N01 without a GUI; useful inside the WSL lab where the mock runs on loopback.
- **Fails:** WSL2 `tcpdump` sees only the VM interface; Windows-side traffic needs Npcap. Not
  installed here (`[UNVERIFIED]`).
- **Safety:** requires root/CAP_NET_RAW; scope the BPF filter to the lab.

### N03 mitmproxy intercept + addon rewrite

- **When:** the control API is HTTPS and you need to read or mutate form bodies.
- **Run:**
  ```bash
  "C:/Users/dwgx1/AppData/Roaming/Python/Python314/Scripts/mitmdump.exe" -p 8080 -s addons/ajs.py \
    --set confdir=C:/lab/mitm
  ```
  Addon core: `def request(self, flow): if flow.request.path.endswith('/client.php'):
  flow.request.text = rewrite(flow.request.text)`.
- **Proves:** which parameters the server honours vs ignores (mutation matrix), and the exact
  request the client builds.
- **Fails:** the client uses libcurl with a system CA store, so the mitm CA must be installed;
  mbedTLS builds that pin a public key will reject it (see N08). The console script is **not on
  PATH** on this host — use the full path or add `%APPDATA%\Python\Python314\Scripts`.
- **Safety:** installing a mitm CA weakens the machine's TLS trust; remove it after the session
  (`certutil -delstore Root <thumbprint>`).

### N04 Burp Suite intercept/replay

- **When:** manual, field-by-field replay of `/client.php` with a human in the loop.
- **Run:** Proxy → Options → add `login.ajs-app.com` to TLS passthrough exceptions off; install the
  Burp CA; send the request to Repeater and edit `cmd=`, `username=`, `_v=`, `cv=`.
- **Proves:** the same mutation matrix as N03 but faster to explore interactively; good for
  discovering required vs optional fields (e.g. `_vrf`, `SyncVersion`).
- **Fails:** Burp normalises some headers; HTTP/2 to the origin may be downgraded unless enabled.
  Not installed here (`[UNVERIFIED]`).
- **Safety:** same CA caveat as N03; keep the project file out of the repo.

### N05 Charles Proxy HTTP/2

- **When:** macOS host, or you need a GUI rewrite rule without code.
- **Run:** Proxy → SSL Proxying Settings → add `login.ajs-app.com:443`; Tools → Rewrite → add a rule
  on body `cmd` → replace `ClientApiLogin/Login` with `ClientApiLoginSync`.
- **Proves:** transport-level behaviour (HTTP/1.1 vs HTTP/2, ALPN) and simple field rewrites.
- **Fails:** Charles' Java trust store must contain its CA; large binary bodies are truncated in the
  UI. Not installed here (`[UNVERIFIED]`).
- **Safety:** same CA caveat; disable "Map Remote" before closing or it silently persists.

### N06 openssl s_client chain/pin probe

- **When:** before any interception — to learn the chain, ALPN, and whether a pin is plausible.
- **Run:**
  ```bash
  openssl s_client -connect login.ajs-app.com:443 -servername login.ajs-app.com \
    -showcerts -alpn h2,http/1.1 </dev/null 2>/dev/null | openssl x509 -noout -subject -issuer -dates
  openssl s_client -connect login.ajs-app.com:443 </dev/null 2>/dev/null \
    | openssl x509 -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64
  ```
- **Proves:** the issuer chain and the SPKI hash a pin would use; ALPN tells you whether HTTP/2 is
  even offered.
- **Fails:** a CDN may return a different chain per edge; the SPKI hash is not proof of pinning,
  only of what a pin *would* be. OpenSSL 3.5.7 verified on this host; no live host was contacted
  for this report (`[UNVERIFIED]` against the real service).
- **Safety:** `s_client` makes a real TLS connection — only run it if contacting the host is
  authorised; for this project, run it against the mock instead.

### N07 HTTP/2, WebSocket, gRPC, Protobuf decode

- **When:** you must decide which of these the client actually speaks before building a mock.
- **Run:**
  ```bash
  nghttp -nv https://login.ajs-app.com/client.php
  websocat -v ws://127.0.0.1:8080/s
  grpcurl -plaintext -d '{}' 127.0.0.1:50051 ajs.Control/Login
  protoc --decode_raw < body.bin
  ```
- **Proves:** presence/absence of each framing. For AJiaSu the answer is: control = HTTP/1.1 form
  POST, proxy = custom binary FV, legacy mode = pseudo-WebSocket upgrade (`POST /s?s=&n=`), and
  **no gRPC/Protobuf anywhere** (`[CONFIRMED]` by strings + captures).
- **Fails:** `nghttp` against a non-HTTP/2 server just fails the upgrade; `protoc --decode_raw`
  happily "decodes" random bytes into plausible fields — treat output as a hypothesis.
- **Safety:** read-only except the WebSocket/gRPC calls, which must target the mock.

### N08 Certificate pinning analysis

- **When:** interception fails with a TLS error and you need to know whether it is a pin.
- **Run:**
  ```bash
  frida-trace -f "$WIN" -i "SSL_*" -i "X509*" -i "CertVerifyCertificateChainPolicy"
  frida -f "$WIN" -l hooks/verify.js   # log SSL_get_verify_result + the leaf SPKI
  ```
  macOS: hook `SecTrustEvaluateWithError`; Linux: `LD_PRELOAD` a `SSL_get_verify_result` shim (H02).
- **Proves:** whether verification is enforced, bypassed (`InsecureTLS` in OpenAJS), or pinned.
- **Fails:** mbedTLS uses `mbedtls_ssl_conf_verify` and `mbedtls_x509_crt_verify`, not OpenSSL
  symbols; a static build hides them from `frida-trace -i` (use module base + offset).
- **Safety:** bypassing verification is a lab-only action; never ship a verify-disabling hook.

### H01 DLL injection (Windows)

- **When:** you want a long-lived logger inside the process without a debugger.
- **Run:** GH Injector GUI (`D:/Software/Tool/Injc/GH-Injector-latest/GH Injector - x64.exe`), or the
  canonical API sequence: `OpenProcess` → `VirtualAllocEx` → `WriteProcessMemory(LoadLibraryW path)`
  → `CreateRemoteThread`. Exact GH Injector CLI flags are `[UNVERIFIED]`; use the GUI.
- **Proves:** that a DLL's `DllMain` runs inside the target, enabling IAT/inline hooks (H04).
- **Fails:** x86 vs x64 mismatch (AJiaSu.exe is PE32 — use the x86 injector), protected processes,
  and AV/EDR blocking `CreateRemoteThread`.
- **Safety:** **high risk** — arbitrary code in the target; only on a lab copy, never on a machine
  with a live account, and keep the DLL source in the repo, not the compiled artifact.

### H02 LD_PRELOAD shim (Linux)

- **When:** Linux build and you want to log `connect`/`send`/`SSL_write` without ptrace.
- **Run:**
  ```bash
  gcc -shared -fPIC -o shim.so shim.c -ldl
  LD_PRELOAD=./shim.so ./ajiasu -c /tmp/ajs-lab/ajs.conf connect Test*
  ```
  `shim.c` resolves the real symbol with `dlsym(RTLD_NEXT, "connect")` and logs `sockaddr`.
- **Proves:** connection targets and plaintext writes at the libc boundary.
- **Fails:** a **statically linked** binary (AJiaSu Linux is static) never calls libc through the
  PLT — `LD_PRELOAD` has no effect. This is the single most common wasted hour on this target.
- **Safety:** process-wide interposition; a bug in the shim crashes the target. Lab only.

### H03 DYLD_INSERT_LIBRARIES (macOS)

- **When:** macOS build, and SIP/hardened runtime allow interposition.
- **Run:**
  ```bash
  clang -dynamiclib -o shim.dylib shim.c
  DYLD_INSERT_LIBRARIES=./shim.dylib DYLD_FORCE_FLAT_NAMESPACE=1 ./ajiasu
  ```
- **Proves:** same as H02 on Mach-O; the only lightweight option without a debugger.
- **Fails:** SIP strips `DYLD_*` for protected binaries; hardened runtime requires the
  `com.apple.security.cs.allow-dyld-environment-variables` entitlement; static binaries ignore it.
- **Safety:** ad-hoc signing the shim (`codesign -s - shim.dylib`) may be required; lab only.

### H04 IAT / inline hooks

- **When:** you need stable, selective interception that survives for minutes (Frida is heavier).
- **Run:** MinHook (Windows): `MH_CreateHook(pSend, &hkSend, &origSend); MH_EnableHook(pSend);`
  Detours: `DetourAttach(&(PVOID&)RealSend, HookSend);`. Linux equivalent: patch the GOT entry of a
  dynamic binary, or use `frida-interceptor` on a static one.
- **Proves:** arguments/returns at a chosen function with minimal overhead, and lets you *modify*
  values (e.g. force `protocolVersion`).
- **Fails:** IAT hooks miss `GetProcAddress`-resolved pointers; inline hooks break on
  hot-patch-protected or CFG-enabled binaries; a static binary has no IAT for libc.
- **Safety:** patching live code can crash the target and is visible to integrity checks; lab only,
  and keep the original bytes for rollback.

### H05 eBPF uprobe/tc (Linux)

- **When:** you want kernel-side observation with no process modification.
- **Run:**
  ```bash
  bpftrace -e 'uprobe:/tmp/ajs-lab/ajiasu:0x401000 { printf("hit %x\n", arg0); }'
  bpftrace -e 'tracepoint:syscalls:sys_enter_connect { printf("%s\n", comm); }'
  ```
- **Proves:** entry to a known address (stripped binary ⇒ address from S05/S07) and syscall-level
  behaviour, without ptrace.
- **Fails:** needs root and a kernel with BTF (≥5.x); uprobe offsets are build-specific; a static
  binary still works because uprobes are address-based. Not installed here (`[UNVERIFIED]`).
- **Safety:** root-level tracing; a bad BPF program can add measurable overhead. Use a VM.

### P01 RSA modulus replacement

- **When:** you must prove which public key the client uses for `k=`, or stand up a lab server.
- **Run:**
  ```bash
  python research/60-dynamic/mock/patch_rsa.py "$LIN" /tmp/ajs-lab/ajiasu.lab /tmp/ajs-lab/ajs_priv.pem --offset 0x28a030
  # Windows build: same script, --offset 0x314468 (309-digit ASCII, architecture-neutral)
  ```
  The script regenerates a 1024-bit key until the modulus is exactly 309 decimal digits, overwrites
  only that range, and verifies the byte diff is confined to it.
- **Proves:** OAEP-SHA1 with a 16-byte session key decrypts `k=`; OAEP-SHA256 fails. This is the
  experiment that converted `k=` from inference to fact (`research/60-dynamic/REPORT.md` §2).
- **Fails:** a modulus with a different digit count shifts every subsequent offset and breaks the
  binary; the Windows PE has a checksum field (P03) that some loaders warn about; the patched
  binary will not talk to the real service (wrong key) — by design.
- **Safety:** **never** overwrite the original; keep the private key outside the repo
  (`/tmp/ajs-lab/ajs_priv.pem`, deleted at session end); record both hashes (P05).

### P02 NOP / length-preserving byte patch

- **When:** you need to skip one branch (e.g. a version gate) and cannot use a debugger.
- **Run:** in IDA: Edit → Patch program → Change byte (e.g. `74` `jz` → `75` `jnz`, or `0F 84` →
  `90 E9`); or with Python:
  ```bash
  python -c "b=bytearray(open('ajiasu','rb').read()); b[0x451932]^=0x01; open('ajiasu.p','wb').write(b)"
  ```
- **Proves:** the branch is the only gate (if the behaviour changes and nothing else does).
- **Fails:** a length-changing patch invalidates every hard-coded offset and the PE section
  layout; relative jumps must be recomputed; CFG/CFG-protected binaries reject altered code.
- **Safety:** copy-first, hash-before, diff-after; keep the original byte sequence in the manifest.

### P03 Checksum / CRC repair

- **When:** a patched PE triggers a checksum warning, or the format has an internal CRC (OPV2 has
  a `crc32` field).
- **Run:**
  ```bash
  python - <<'PY'
  import pefile
  pe = pefile.PE('AJiaSu.patched.exe')
  print(hex(pe.OPTIONAL_HEADER.CheckSum), hex(pe.generate_checksum()))
  PY
  ```
  Then write `generate_checksum()` at `OptionalHeader.CheckSum` and re-parse.
- **Proves:** the loader will accept the file; for OPV2, that the CRC over the payload is correct.
- **Fails:** Windows does **not** verify the PE checksum for normal userland EXEs (only drivers and
  some installers), so "repair" is often cosmetic; a wrong CRC in OPV2 makes the peer drop the frame.
- **Safety:** compute on a copy; a wrong checksum can turn a working patch into a non-loading file.

### P04 Lab re-signing

- **When:** the OS refuses to run an unsigned/patched artifact (Windows SmartScreen, macOS Gatekeeper).
- **Run:**
  ```bash
  osslsigncode sign -certs lab.pem -key lab.key -n "AJiaSu lab" -in AJiaSu.patched.exe -out AJiaSu.lab.exe
  codesign --force --sign - ./ajiasu.lab          # macOS ad-hoc
  ```
- **Proves:** the artifact runs in the lab without disabling OS protections globally.
- **Fails:** a self-signed cert still triggers SmartScreen; macOS ad-hoc signing does not satisfy
  notarisation; Linux has no signature layer at all.
- **Safety:** the re-signed binary is a **different artifact** — never present it as the vendor's,
  never distribute it, and delete the lab key when done.

### P05 Rollback and integrity manifest

- **When:** before and after every patch; this is the method that makes the rest safe.
- **Run:**
  ```bash
  sha256sum ajiasu > MANIFEST.sha256
  cp ajiasu ajiasu.orig
  cmp -l ajiasu.orig ajiasu.patched | wc -l          # expect exactly the patched byte count
  sha256sum -c MANIFEST.sha256                       # rollback verification
  ```
- **Proves:** the diff is confined to the intended range, and restoration is byte-exact.
- **Fails:** `cmp -l` counts differing bytes, not ranges — pair it with a Python range check; a
  patch applied to the wrong copy silently invalidates the manifest.
- **Safety:** the manifest lives outside the repo's tracked tree or in `research/74-re-methods/`
  only; never commit a patched vendor binary.

### F01 Capture replay

- **When:** you have a golden capture and want to test a new server implementation.
- **Run:**
  ```bash
  tcpreplay -i lo research/60-dynamic/captures/fvmock-1.bin   # pcap only; raw frames need Python
  python -c "import socket;s=socket.create_connection(('127.0.0.1',8443));s.sendall(open('fvmock-1.bin','rb').read())"
  ```
- **Proves:** the server's tolerance of a replayed auth frame (the `K`/`n`/`k` values are fixed, so a
  correct server either accepts or rejects deterministically).
- **Fails:** the captured `k=` is encrypted to the *lab* key, so only the lab server can decrypt it;
  a replayed `sid` may be expired; raw `.bin` is not a pcap, so `tcpreplay` needs conversion.
- **Safety:** replay only against `127.0.0.1`; a replayed frame sent to a real endpoint is an
  unauthorised request.

### F02 Field-level mutation fuzzing

- **When:** you need to know which fields the parser validates.
- **Run:** loop over `s/n/k/l/_` with a small Python harness built on
  `research/40-protocol/reference/fv_proto.py`: truncate `l`, set `l=0`, omit `_`, send 200 KB of
  `_`, swap `k` to base64 of random bytes, set `n` to 31/33 hex chars.
- **Proves:** length-field trust, padding bounds, and which errors are fatal vs ignored.
- **Fails:** the client may simply time out (no distinguishable error), and the mock server's own
  parser may be more permissive than the vendor's.
- **Safety:** mock only; a mutation storm against a real endpoint is a DoS.

### F03 Grammar-aware fuzzing

- **When:** random mutation plateaus and you need structured cases.
- **Run:** generate frames from the field grammar (`s=`, `n=`, `k=`, `l=`, `_=`) with a length-field
  matrix (declared vs actual body length), then feed each to `mock_fv.py` and record the client's
  reaction (`state=200`, retry, or timeout).
- **Proves:** the exact ordering/length invariants the client enforces, e.g. that the auth response
  must not duplicate the length block.
- **Fails:** the grammar is inferred, so a "valid" case may be invalid for an unknown reason;
  coverage is only as good as the field list.
- **Safety:** mock only; bound the run (N cases, T seconds) and keep the corpus out of the repo.

### F04 Mock server harness

- **When:** always, before running the vendor binary; it is the only safe way to exercise the client.
- **Run:**
  ```bash
  export AJS_LAB=/tmp/ajs-lab
  python research/60-dynamic/mock/mock_fv.py --port 8443 --auth-frame --close-after-auth --key /tmp/ajs-lab/ajs_priv.pem
  python research/60-dynamic/mock/mock_control.py 443 4443     # ports are positional; certs from $AJS_LAB/certs
  ```
  Go alternative: `cd emulator && go build -o /tmp/ajsproxy ./cmd/ajsproxy && /tmp/ajsproxy serve-socks -listen 127.0.0.1:1080 -config /tmp/ajs-lab/ajs.json`.
- **Proves:** the client's full state machine (`state=200`, SOCKS listener on `127.0.0.1:1080`)
  against our encoding, with no vendor service involved.
- **Fails:** the client asserts on malformed JSON (`SyncVersion` must be an object, `Login` must
  contain `UrlWeb`/`LatestVersion`); the mock must reproduce those types exactly.
- **Safety:** both mock scripts bind `0.0.0.0` (no loopback flag) — run them in a VM or add a
  firewall rule, and never on a routable interface.

### F05 Response diffing

- **When:** a new build or a new emulator commit must be checked against the golden transcript.
- **Run:** decrypt both responses with the recorded session key and diff the field maps:
  `python -c "import json;print(set(a)^set(b))"` over `r=`, `sid`, `sip`, `cip`, `ka`, `mtu`.
- **Proves:** silent protocol drift (a field added/removed/renamed) that a byte-diff would bury in
  ciphertext noise.
- **Fails:** field order is not stable and must be normalised; a fresh `sid`/`n` on every run means
  only the key set and value shapes are comparable.
- **Safety:** read-only on captures; do not store decrypted live-account data.

### F06 State-machine replay

- **When:** the client hangs and you need to know which message it is waiting for.
- **Run:** send in order: `code=200` block → auth map → `shutdown(SHUT_WR)`. Then repeat with the
  half-close removed to observe the stall; then with the length block duplicated to observe the
  parse failure.
- **Proves:** the half-close dependency and the "one length block only" rule
  (`research/40-protocol/PROTOCOL.md` §Auth response).
- **Fails:** a mock that half-closes too early truncates the map; TCP coalescing can make two
  writes look like one read, so log at the syscall level (D10).
- **Safety:** mock only.

### R01 Control API intercept + rewrite

- **When:** you need to learn which `/client.php` parameters matter.
- **Run:** mitmproxy addon that rewrites `cmd`, `_v`, `cv`, `_uid`, and the `SyncVersion` request;
  replay the same request with one field changed at a time; log the JSON `AuthResult`/`AuthMessage`.
- **Proves:** required vs optional fields, and the client's tolerance of unknown parameters.
- **Fails:** without a valid account the server rejects everything at `AuthResult`, so the matrix
  collapses to one error; the only safe target is the mock control plane.
- **Safety:** **no live account** (project constraint); against the real host this is unauthorised
  request manipulation.

### R02 Local mock API + hosts redirect + lab CA

- **When:** you need the client to complete a login without the vendor service.
- **Run:**
  ```bash
  cp /etc/hosts /tmp/ajs-lab/hosts.bak
  printf '127.0.0.1 login.ajs-app.com\n127.0.0.1 login.mmegg.com\n127.0.0.1 login.mbclubsh.com\n' >> /etc/hosts
  sh research/60-dynamic/mock/make_certs.sh
  SSL_CERT_FILE=/tmp/ajs-lab/certs/ca.crt ./ajiasu -c /tmp/ajs-lab/ajs.conf connect Test*
  cp /tmp/ajs-lab/hosts.bak /etc/hosts     # rollback
  ```
- **Proves:** the client's JSON contract (required keys/types) and the full login→FV handoff.
- **Fails:** the client may use its own CA bundle (libcurl `CURLOPT_CAINFO`), ignoring
  `SSL_CERT_FILE`; mbedTLS builds need the CA compiled in or passed explicitly.
- **Safety:** always restore `/etc/hosts` and remove the CA; never leave a redirect in place.

### R03 Certificate substitution + pin bypass

- **When:** the lab CA is rejected and you must distinguish "no CA" from "pinned".
- **Run:** install the lab CA (Windows `certutil -addstore Root ca.crt`; macOS
  `security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ca.crt`; Linux
  `cp ca.crt /usr/local/share/ca-certificates/ && update-ca-certificates`), retry, then hook the
  verify callback (N08) if it still fails.
- **Proves:** the trust model: system store vs bundled store vs pin.
- **Fails:** a bundled CA file ignores the system store entirely; a pin makes substitution
  impossible without a code patch (P02).
- **Safety:** installing a root CA is a machine-wide trust change — record the thumbprint, remove
  it afterwards, and never do it on a shared host.

### R04 Session/lease manipulation

- **When:** you need `sid` lifetime, reuse, and concurrency rules for the emulator.
- **Run:** against `mock_fv.py`, open two data channels with the same `s=<sid>`; then a third after
  the mock's `ka`/`kat` window; then reuse a `sid` after the auth connection closed.
- **Proves:** whether `sid` is per-connection or per-session, and what the client does on rejection
  (`data channel rejected`).
- **Fails:** the mock's policy is ours, not the vendor's — the result constrains the *client*, not
  the server. Real limits stay `[UNKNOWN]`.
- **Safety:** mock only; never test concurrency limits against the live service.

### R05 Rate-limit and error-path probing

- **When:** you need the client's backoff/retry behaviour for a resilient emulator.
- **Run:** script the mock to return `r=err`, a missing `sid`, a slow `code=200`, then a TCP reset;
  log client retries with timestamps (`state=200` transitions, `retry emitClientAuth`).
- **Proves:** retry counts, backoff shape, and which errors are terminal.
- **Fails:** the client's retry logic is timer-driven (9 s connect-auth timeout observed), so short
  runs miss it; logs may be the only observable.
- **Safety:** mock only; rate-limit probing against a real service is abuse.


## Per-platform matrix

`W` = Windows 10/11 x64, `L` = Linux (native or WSL2 Ubuntu-24.04), `M` = macOS 13+.
`●` = native and fully usable, `◐` = usable with caveats, `—` = not applicable/absent.

| Method | W | L | M | Notes |
|---|---|---|---|---|
| S01 hash intake | ● | ● | ● | `Get-FileHash` vs `sha256sum`; same digest |
| S02 PE structure | ● | ◐ | ◐ | `objdump`/`pefile` work everywhere; PE-bear is Windows-only GUI |
| S03 ELF structure | — | ● | ◐ | `readelf` on macOS needs binutils; `otool -l` is the native equivalent |
| S04 strings mining | ● | ● | ● | identical GNU `strings`; use `-t x` for offsets |
| S05 IDA headless | ● | ● | ● | `idat` is per-OS; `ida.exe` is the unified 64-bit build (9.x) |
| S06 Ghidra headless | ● | ● | ● | needs JDK 21; `analyzeHeadless` script is identical |
| S07 radare2 | ● | ● | ● | `r2pm -ci r2dec` for decompiler |
| S08 GoReSym | ● | ● | ● | `go version -m` needs a Go toolchain but no plugin |
| S09 FLOSS | ● | ● | ● | single-file Python/pip install |
| S10 capa | ● | ● | ● | rules repo pinned by tag |
| S11 YARA-X | ● | ● | ● | `yr` binary per-OS |
| S12 RTTI recovery | ● | ◐ | ◐ | MSVC RTTI only exists in the Windows build; Itanium RTTI differs |
| S13 signer/catalog | ● | ◐ | ◐ | Authenticode/catalog is Windows-only; `codesign -dv` on macOS |
| S14 crypto constants | ● | ● | ● | pure Python; the only truly portable proof method |
| S15 cross-build diff | ● | ● | ● | needs both binaries on one host |
| D01 gdb | ◐ | ● | ◐ | WSL gdb cannot debug the Windows PE; native gdb on Linux only |
| D02 lldb | — | ◐ | ● | macOS system debugger; needs `task_for_pid` entitlement for hardened targets |
| D03 x64dbg | ● | — | — | Windows userland only |
| D04 WinDbg/cdb | ● | — | — | Windows only |
| D05 Frida | ● | ● | ● | 17.24.1 verified on Windows; macOS needs `codesign` of the agent |
| D06 Stalker | ● | ● | ● | heaviest Frida mode; 10–50× slowdown |
| D07 API Monitor | ● | — | — | Windows only |
| D08 Procmon/ETW | ● | — | — | Windows only |
| D09 process/handle | ● | ◐ | ◐ | `lsof -p`/`/proc/<pid>/maps` are the Linux analogues |
| D10 strace/ltrace/perf | — | ● | ◐ | `dtruss`/`ktrace` are macOS analogues, both SIP-restricted |
| N01 Wireshark/Npcap | ● | ● | ● | Npcap is Windows-only; `SSLKEYLOGFILE` only helps if the app uses OpenSSL/NSS |
| N02 tcpdump | ◐ | ● | ◐ | WSL `tcpdump` sees only the WSL vNIC, not the Windows stack |
| N03 mitmproxy | ● | ● | ● | 12.2.3 verified; console script lands in the user Scripts dir |
| N04 Burp | ● | ● | ● | Java 21 present |
| N05 Charles | ● | ◐ | ● | macOS-native; Linux via tarball |
| N06 openssl s_client | ● | ● | ● | OpenSSL 3.5.7 (Windows) / system OpenSSL (WSL) |
| N07 h2/WS/gRPC/protobuf | ● | ● | ● | `nghttp`, `websocat`, `grpcurl`, `protoc` are separate installs |
| N08 pinning analysis | ● | ● | ● | needs Frida or a debugger |
| H01 DLL injection | ● | — | — | Windows only |
| H02 LD_PRELOAD | — | ● | — | Linux only |
| H03 DYLD_INSERT | — | — | ● | blocked for hardened runtime / SIP-protected binaries |
| H04 IAT/inline hooks | ● | ◐ | ◐ | MinHook is Windows; Linux uses `LD_PRELOAD` or `frida` |
| H05 eBPF | — | ● | — | root + kernel ≥ 4.9; not in WSL1 |
| P01 modulus replace | ● | ● | ● | Windows offset `0x314468`, Linux `0x28a030`, both 309 digits |
| P02 byte patch | ● | ● | ● | length-preserving only if offsets must stay stable |
| P03 checksum repair | ● | ◐ | ◐ | PE checksum is Windows-specific; ELF has no checksum |
| P04 re-sign | ● | — | ● | Authenticode vs ad-hoc `codesign`; Linux has no signature layer |
| P05 rollback | ● | ● | ● | hash + byte-diff, works everywhere |
| F01 replay | ◐ | ● | ◐ | `tcpreplay` needs a real interface; Python replay is portable |
| F02 mutation | ● | ● | ● | portable Python |
| F03 grammar fuzz | ● | ● | ● | portable Python |
| F04 mock server | ● | ● | ● | portable Python; the Go emulator is cross-compilable |
| F05 response diff | ● | ● | ● | portable |
| F06 state replay | ● | ● | ● | portable |
| R01 API rewrite | ● | ● | ● | mitmproxy addon is portable |
| R02 mock API | ● | ● | ● | `/etc/hosts` on Linux/macOS, `hosts` file on Windows |
| R03 cert substitution | ● | ● | ● | lab CA install differs per OS |
| R04 session manipulation | ● | ● | ● | needs a mock FV server, not the live one |
| R05 rate-limit probing | ● | ● | ● | **only against the mock**; never against the live service |

Summary (recomputed from the table above on 2026-10-23): 42 of 54 methods are three-platform,
4 are two-platform (S03, D02, D10, P04), and 8 are single-OS by nature (D03, D04, D07, D08, H01,
H02, H03, H05). The portable core that must work on all three platforms is S01/S04/S14 + F01–F06 +
P05; the single-OS methods are exactly the ones that depend on a platform-specific debugger,
injector, or kernel facility.

## AJiaSu anchors

Every row was re-checked on this host on 2026-10-23 unless marked otherwise.

| Anchor | Value | Where | How to re-derive |
|---|---|---|---|
| Windows PE | `AJiaSu.exe`, 4,064,640 B, PE32 i386, image base `0x400000`, 8 sections | `C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023/AJiaSu.exe` | S01/S02 |
| Windows sha256 | `0fd9d702bf9932836ad0ba3e4acf43bb1a587c45ee6247b7a5d88aede78101f6` | same | `sha256sum` |
| Linux ELF | `ajiasu`, 3,578,792 B, ELF64 x86-64, `EXEC`, statically linked, stripped | `C:/Users/dwgx1/AppData/Local/Temp/ajs-linux-20261023/ajiasu` | S01/S03 |
| Linux sha256 | `29e18740af8b69c6fa4f7b0ef269316b01e0400d7768583fc08044632be13b9f` | same | `sha256sum` |
| RSA modulus (309 decimal digits) | `141601187905926909444644730286409666755495300050721564398199769080746616339148556683219048017120018748016428099359153205651362664001287890722535799466700786136519566714160932079339659299698611914846217596226121056961277268141789905486380539961102053398435632146710731361922836526658451793921073088741138357721` | Linux file offset `0x28a030`; Windows file offset `0x314468` | S14 |
| RSA exponent | `65537` ASCII at Linux `0x28a166` | same table | S04/S14 |
| `product.dat` cipher | AES-128-CTR, key `MD5("[CONFIG]") = 2304669ad435f8829a061ee9860a4cc5`, IV = 16 zero bytes, no padding | `research/50-windows-protocol/product.dat.analysis.md` | S14 |
| `product.dat` plaintext | 818 B, starts `[CONFIG]\r\nProductFullName=爱加速`, sha256 `2b618010b49d66922d778beda9aae7117084a46fd6008706e6f5c9c53291183e` | same | S14 |
| `ServerApiUrls` | `https://login.ajs-app.com/;https://login.mmegg.com/;https://login.mmegg.com:4443/;https://login.mbclubsh.com/` | decrypted INI | S14 |
| FV carrier | `[16B K][AES-128-CTR(K, IV=0)(4B BE text_len ‖ text ‖ body)]` | `research/60-dynamic/captures/fvmock-1.bin` (679 B), `fvmock-2.bin` (207 B) | N01/F04 |
| Frame fields | `s=` sid, `n=` 16-byte CTR IV (32 hex), `k=` `base64("OAEP" ‖ RSA-OAEP-SHA1(sessionKey))`, `l=` body length, `_=` 0–199 random `a–z` | auth capture; data channel has `k=` empty and **no `l=`** | N01/S04 |
| Auth response | two layers: `[4B CTR(K,0)(len)][CTR(K,4..)(text)]` with text `code=200\nr=ok\ns=…\nsid=…\n`, then `AES-128-CTR(sessionKey, IV=n)` auth map; server **must half-close** | `captures/fvmock-1.log` | F04/F06 |
| Data-channel gate | client waits for `code=200` and a 10-byte encrypted SOCKS5 reply | `captures/fvmock-2.log` | F06 |
| `OPV2` | VPN/control builder, **not** the proxy carrier; Windows `0x3216d0`, Linux `0x28ee18` | verified string offsets | S04 |
| `code=200` literal | Windows `0x3217c4`, Linux `0x28eecf` | verified string offsets | S04 |
| `/client.php` | Windows `0x31641c`; signing headers `X-Sig: ` `0x31e144`, `X-Timestamp: ` `0x31e150` | verified | S04 |
| Signing secret | OpenAJS constant `84:C1:12:45:0B:68:F9:B2:06:95:00:44:74:37:55:71:60:B5:98:31` is **not** present in either binary; role of `06ka4z0dc6ga02c` (`0x31e160`) unknown | `research/40-protocol/PROTOCOL.md` | S14 |
| Windows function anchors | `0x552ce0` build, `0x552640` fetch, `0x551f40` OPV2, `0x520df0` product.dat decrypt, `0x520830` MD5 KDF, `0x526560` prepare_core, `0x57fae0` handshake, `0x581bb0` relay | `research/50-windows-protocol/REPORT.md` | S05 |
| Vtables (MSVC RTTI) | `FVClientProxy 0x7208f0`, `FVClientVpn 0x72499c`, `FVClientWrapper 0x7203d0` | same | S12 |
| Mode selection | `proxy-tun` → mode 1; `proxy-socks`/`proxy-global` → mode 2; `vpnConnect 0x448f40` | same | S05 |

## Safety and rollback

Non-negotiable rules for anyone executing this playbook.

1. **Never run the vendor binary on the analysis host against the live service.** The Linux
   client accepts `-h`/usage without a network call; anything else is `[UNVERIFIED]` behaviour
   and may attempt login. Use the mock harness (`F04`) and a hosts redirect (`R02`).
2. **Every mutation is on a copy.** `patch_rsa.py` refuses to write in place; keep the source
   sha256 in a manifest and verify the diff is confined to the expected range
   (`P05`). Baseline values: Linux `29e18740…3b9f`, Windows `0fd9d702…01f6`.
3. **Rollback is a file, not a memory.** Before any patch, copy
   `ajiasu` → `ajiasu.orig` and record `sha256sum ajiasu.orig` in `MANIFEST.sha256`.
   Restore with `cp ajiasu.orig ajiasu` and re-hash; never rely on "undo" in a GUI.
4. **Network interception requires a lab CA and a rollback of trust.** Record the pre-state
   (`/etc/hosts` backup, `security find-certificate` on macOS, `certutil -store Root` on
   Windows), and remove the CA + hosts entries at the end of the session. AJS-07 did exactly
   this (`/tmp/ajs-lab/hosts.bak`).
5. **Do not patch signed production binaries and redistribute them.** P04 re-signing is for a
   lab copy that never leaves the lab; the vendor ToS forbids reverse engineering, so all work
   stays local and no live traffic is generated.
6. **Fuzzing has a blast radius.** Run F01–F03 only against `mock_fv.py` on `127.0.0.1`.
   Sending mutated frames to a real endpoint is out of scope for this project.
7. **Kill-switch checklist** before closing a session:
   `sha256sum -c MANIFEST.sha256` on every original, restore `/etc/hosts`, remove the lab CA,
   stop `mitmdump`/`mock_fv.py`, delete temp keys (`ajs_priv.pem`) and any decrypted
   `product.dat` plaintext.

## First 60 minutes on a new binary

0–5 min — identity. `sha256sum`, `file`, size, version string (`strings | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'`).
Write the hash into `hashes.tsv` before anything else.

5–15 min — structure. `objdump -p` (PE) or `readelf -hSW` (ELF); note subsystem, static/dynamic,
stripped/PIE, and the import list. `bcrypt`/`WS2_32`/`IPHLPAPI` in the Windows import table is a
crypto+network signal.

15–30 min — literals. `strings -t x -n 5` piped through the anchor greps (`/client.php`,
`X-Sig`, `code=200`, `OPV2`, `s=`, `n=`, `k=`, `l=`, `_=`). Build a literal→offset table; that
table is the index for every later breakpoint.

30–45 min — constants. Search for the 309-digit modulus, `MD5("[CONFIG]")`, and `65537` (S14).
If the modulus is present, the FV path is the same family and `P01` is available.

45–60 min — confirm the shape statically. Open the literal's xrefs in IDA (`S05`) and name the
builder/parser pair; for AJiaSu those are `0x552ce0`/`0x552640` (Windows). Stop before running
anything; the dynamic phase starts only after the mock harness is ready.

## When not to patch

- **When a mock server reproduces the behaviour.** If `mock_fv.py` can satisfy the client, no
  binary patch is needed; patches create a maintenance burden and break on every vendor update.
- **When the constant is data, not code.** `product.dat` and the API host list are data — decrypt
  or override them, do not patch instructions.
- **When the patch would move an offset.** A 309-digit modulus must stay 309 digits; a
  length-changing edit invalidates every hard-coded offset in the analysis.
- **When the target is signed and the signature is checked downstream.** Re-signing is lab-only;
  a patched signed binary is a different artifact with a different hash and must never be
  presented as the vendor's.
- **When the change is only for one observation.** Use a debugger write (`set {char}…`) or a
  Frida hook instead; it leaves no artifact to roll back.
- **When the vendor build is newer than the analysis.** Re-run S01/S04/S14 first; offsets like
  `0x28a030` are build-specific (4.2.3.0), and 4.3.0.1 differs.

## Open questions

1. **Signing secret.** `X-Sig` is 16 lowercase hex; the OpenAJS secret is absent from both
   binaries and Linux 4.2.3.0 sends no signature at all. Whether Windows 4.9.6.0 signs with
   `06ka4z0dc6ga02c` or a derived key is `[UNKNOWN]`; S14 + D01 on a Windows host would settle it.
2. **Whether responses are authenticated.** No signature/MAC was observed on the control
   response; a mock server can prove the client does not check one (F04/F05).
3. **`sid` lifetime and concurrency.** R04 can only be answered against a mock; the live
   service is out of scope.
4. **`OPV2` semantics outside the proxy path.** The builder exists in both builds but was never
   observed on the wire; VPN mode would need a TUN device and a different lab.
5. **UDP / lwIP / DNS modes.** Absent from OpenAJS and from every capture; unknown whether the
   shipping clients use them for the proxy product.
6. **macOS build.** No macOS AJiaSu binary was available on 2026-10-23 (`[UNKNOWN]` whether one
   exists); the macOS column is therefore method-level, not anchor-level.

## Verification of commands

Commands actually executed for this report (all read-only, 2026-10-23):

| # | Command | Result used |
|---|---|---|
| 1 | `sha256sum AJiaSu.exe ajiasu` | `0fd9d702…01f6`, `29e18740…3b9f` |
| 2 | `wsl … file /mnt/c/…/ajiasu` | `ELF 64-bit LSB executable, x86-64, statically linked, stripped` |
| 3 | `wsl … readelf -h ajiasu` | `Type: EXEC`, entry `0x40249c`, ELF64 x86-64 |
| 4 | `objdump -p AJiaSu.exe` | `ImageBase 00400000`, `Subsystem Windows GUI`, imports incl. `WS2_32.dll`, `bcrypt.dll`, `IPHLPAPI.DLL` |
| 5 | `python -c pefile.PE(...)` | PE32 (`Machine 0x14c`), 8 sections (`.text 0x1000`, `.rdata 0x313000`, …) |
| 6 | `strings -t x -n 5 AJiaSu.exe \| grep -F …` | `/client.php 31641c`, `X-Sig: 31e144`, `06ka4z0dc6ga02c 31e160`, `code=200 3217c4` |
| 7 | Python decimal-modulus search | Windows `0x314468`, Linux `0x28a030` (309 digits each) |
| 8 | Python `re.finditer(b'OPV2')` | Windows `0x3216d0`, Linux `0x28ee18` |
| 9 | Python AES-CTR decrypt of `product.dat` | 818 B → `[CONFIG]\r\n…`, sha256 `2b618010…83e` |
| 10 | `python -c hashlib.md5(b'[CONFIG]')` | `2304669ad435f8829a061ee9860a4cc5` |
| 11 | tool availability sweep | frida 17.24.1, mitmproxy 12.2.3, pefile 2024.8.26, capstone 5.0.9, unicorn 2.1.4, cryptography 48.0.1, OpenSSL 3.5.7, Go 1.26.7, IDA 9.2 (`idat.exe`), GH-Injector; WSL: readelf/objdump/gdb/strace/ltrace/openssl/gcc |
| 12 | `read` of `research/60-dynamic/captures/fvmock-1.log` | `text_len=234 body_len=425`, `session_key=252a3a23…`, `oaep=sha1` |
| 13 | Python byte search for the OpenAJS secret (ASCII + 20 raw bytes) | 0 hits in `AJiaSu.exe`, 0 hits in `ajiasu` |
| 14 | `read` of `research/60-dynamic/captures/fvmock-2.log` | data-channel text is `s=test`, `n=180a5b93…`, `k=`, `_=…` — **no `l=`**; 18-byte SOCKS5 body follows |

Not installed on this host (documented, `[UNVERIFIED]`): Ghidra, radare2/rabin2, x64dbg,
WinDbg/cdb, Wireshark/tshark/Npcap, tcpdump, perf, capa, FLOSS, YARA-X, GoReSym, Burp, Charles,
`sigcheck`, `7z`, lldb.

## Self-audit

**A. Early output.** `TOOLS.tsv` was written first (53 rows) and the `## Method inventory`
skeleton was landed before any detail prose; detail blocks were added afterwards in one edit.

**B. Falsification of the five strongest claims.**

| # | Claim | Attack | Verdict | Basis |
|---|---|---|---|---|
| 1 | The 309-digit decimal at `0x28a030`/`0x314468` is the FV RSA modulus | It could be a decoy or a second key | CONFIRMED | Same 309 digits in both builds, byte-identical to OpenAJS `FvCrypto`; `k=` decodes to `OAEP`+128 B and only the matching private key decrypts the 16-byte session key (AJS-07) |
| 2 | The proxy carrier is `[16B K][CTR(K,0)(len‖text‖body)]`, not OPV2 | Static analysis had said OPV2/HTTP upgrade | CONFIRMED (capture) | 679 B and 207 B captures decrypt end-to-end; `OPV2` literal sits elsewhere and no `OPV2` bytes appear in either capture |
| 3 | The server must half-close after the auth map | Maybe the client parses on a timer | WEAKENED | Client parses on `BEV_EVENT_EOF`; a mock that sends the map without half-close stalls, but only one client build (Linux 4.2.3.0) was tested — Windows behaviour is `[INFERENCE]` |
| 4 | `product.dat` is AES-CTR with `MD5("[CONFIG]")` | Fixed key would be too convenient | CONFIRMED | 818→818 B, plaintext begins `[CONFIG]\r\n` and every key matches `bootProduct` literals; key recomputed this session |
| 5 | "Any tool in `TOOLS.tsv` works on all three platforms" | Several are single-OS | OVERTURNED | Recomputed from the matrix: 42/54 are three-platform, 4 two-platform, 8 single-OS; the matrix marks W/L/M per method and the portable core is S01/S04/S14 + F-series + P05 |

**C. Verification.** `python research/74-re-methods/validate.py` output is pasted verbatim in the
final answer; it exits 0 and prints `OK methods=54 platforms=3 tools=53`.

**D. Summary (≤14 lines).** Established: 54-method inventory with commands; anchors re-verified
(modulus offsets, `code=200`, `OPV2`, `/client.php`, `product.dat` KDF); three-platform matrix;
rollback procedure. Overturned: "OPV2 is the proxy carrier" (already overturned by AJS-07,
re-confirmed here) and "one toolchain fits all platforms". Unknown: signing secret, response
authentication, `sid` lifetime, macOS build existence, OPV2 wire semantics. Next: run D01 on a
Windows host to settle `X-Sig`; build the Go emulator's FV server against `fvmock-1.bin` as a
golden transcript. What I would still bet wrong on: that 4.3.0.1 keeps the modulus at the same
offset — it is a different build and the offset was not re-checked here.
