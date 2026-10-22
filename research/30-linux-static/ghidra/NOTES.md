# Ghidra / disassembly notes — AJiaSu Linux 4.2.3.0

Status: **Ghidra and IDA were not run.** The task was answered with string/xref analysis, which is
what the brief asked to prioritise. Function bodies are therefore not decompiled and every
"function" address below is an approximation (see *Function-entry approximation*).
`[UNKNOWN]` marks anything that would require decompilation.

## Tools and exact commands

All paths are as used on this workstation (2026-10-23). Binary:
`C:/Users/dwgx1/AppData/Local/Temp/ajs-linux-20261023/ajiasu`
(`sha256=29e18740af8b69c6fa4f7b0ef269316b01e0400d7768583fc08044632be13b9f`).

| tool | command | result |
|---|---|---|
| file | `file ajiasu` | `ELF 64-bit LSB executable, x86-64, statically linked, stripped` |
| strings | `strings.exe -a -n 5 -t x ajiasu > strings_raw.txt` | 8,482 lines |
| strings (short) | `strings.exe -a -n 2 -t x ajiasu > strings_n2.txt` | needed for `s=`, `n=`, `k=`, `l=`, `_=`, `ka`, `ft`, … |
| objdump | `objdump -d --start-address=0x451192 --stop-address=0x4512f0 ajiasu` | FV proxy header builder |
| objdump | `objdump -d --start-address=0x41a330 --stop-address=0x41a400 ajiasu` | proxy port candidate array |
| objdump | `objdump -d --start-address=0x40247c --stop-address=0x4024d0 ajiasu` | `_start` → `main = 0x40aef4` |
| rabin2 | `rabin2 -I ajiasu` (radare2 6.2.2 w64) | static/stripped/pic=false/relro=partial |
| radare2 | `r2 -2 -q -e scr.color=0 -c 'aa; afl~?; axt 0x68a030; quit' ajiasu` | `afl` = **1 function**, `axt` empty — shallow analysis does not work on this stripped static binary; abandoned |
| pyelftools 0.33 | section/segment/`.comment` dump | `.text` `0x400160`+2,660,209; `.rodata` `0x6898e0`+350,608; `GCC: (GNU) 9.4.0` |
| capstone 5.0.7 | `python work/xrefs.py ajiasu <vaddr>...` | targeted xref scan, 699,910 instructions decoded |
| capstone 5.0.7 | call-target scan | 8,333 distinct internal call targets used as function-entry index |

Address translation: the file is a non-PIE `ET_EXEC` with `PT_LOAD vaddr=0x400000 offset=0`, so
**vaddr = file offset + 0x400000** for `.text`/`.rodata`. `strings -t x` prints *file* offsets;
all `.rodata` offsets in `REPORT.md` are file offsets, and the xref scanner takes vaddrs.

## Function-entry approximation

The binary is stripped and GCC 9.4 emits no `endbr64` (0 occurrences in `.text`), so there are no
free function markers. The scanner collects every `call rel32` target inside `.text` (8,333) and
uses "nearest preceding call target" as the enclosing function for a string xref. Deltas of a few
hundred bytes are normal; a delta in the thousands means the xref sits in a large function (e.g.
`0x40860b`). Treat these as labels, not verified symbols.

## Recovered addresses (4.2.3.0)

| what | address | evidence |
|---|---|---|
| ELF entry `_start` | `0x40249c` | ELF header `e_entry` |
| `main` | `0x40aef4` | `mov $0x40aef4,%edi` before `__libc_start_main` at `0x4024c7` |
| large CLI/connect routine | `0x40860b` | contains `try to open tun device`, root-required notice, RSA modulus use |
| login-host initializer (4 base URLs) | `0x411798` | xrefs `0x4117c6/0x4117f1/0x41181c/0x411847` to `0x68ac34/4f/68/86` |
| `ClientUniqueId` consumer | `0x43e120` | xref `0x43e24d` |
| `/client.php` call sites | `0x43f114`, `0x43f5d6`, `0x4405fc`, `0x440b42`, `0x44296a`, `0x443f0e` | xrefs `0x43f2ef`, `0x43f7dd`, `0x4406c7`, `0x440bf6`, `0x442aa5`, `0x443fc8` |
| `X-Sig` / `X-Timestamp` emitter | `0x4405fc` | xref `0x4408c9` to `0x68daae`, `0x4406c7` to `/client.php` |
| `ResponseLogin` handler | `0x4400ea` | xref `0x4403a5` |
| FV control sender (`CommandCode_Ctrl`) | `0x42b1b4` | xref `0x42b2ae` to `send CommandCode_Ctrl auth %s ...`; called from six sites in four small wrappers `0x42c1c8`..`0x42c472`, which are reached from the control loop that owns the `con`/`ping`/`pong`/`off`/`aur` literals (the wrappers contain no verb literals) |
| FV proxy request-header builder (`FVProxyRequestHeader::fetch`) | `0x451192` | `POST /s?s=%s&n=%s`, `Upgrade: websocket`, `Sec-WebSocket-Key`, `code=200` all referenced in this function; callers `0x419dfc` (`fvproxy_auth_context_new`) and `0x424a53` (proxy handshake) |
| FV proxy auth context | `0x41964a` | `fvproxy_auth_context_new: cmd=%s` |
| proxy connection handshake | `0x424988` | `%s handshake: sending request ...`, `Remote(Relay)` |
| obscure-packet key checks | `0x450427`, `0x45080b` | `buffer_obscure_packet key size must be FV_CipherKeySize` |
| local proxy listener setup | `0x41a2xx` (entry ≈ `0x419f00`) | `127.0.0.1` xref `0x41a39a`, port array `0x41a3c0` |
| proxy address assembly (`127.0.0.1` + port) | `0x40a02a`..`0x40a07e` | `127.0.0.1` xref `0x40a05f`, `FV_PROXY` xref `0x40a0ec` |
| port candidate array | `0x41a3c0`–`0x41a3d8` | `movw $0x438/$0x49c8/$0x70d8/$0x97e8/$0x0` |

4.3.0.1 equivalents (same layout, shifted): port array `0x41e0b1`–`0x41e0c3`; FV request builder
strings `0x294558`..`0x29460f`; `prepare core = %s, protocolVersion = %d, implType = %d` `0x2903c8`.

## Not attempted / next step

- No Ghidra project was created. The station has
  `D:/Software/Tool/WorkStation/debugger/Static-Reversing/Ghidra/ghidra_12.1.3_PUBLIC/support/analyzeHeadless.bat`;
  the command that would be used is:

  ```
  analyzeHeadless.bat C:/Users/dwgx1/AppData/Local/Temp/ajs-ghidra AJS04 \
      -import C:/Users/dwgx1/AppData/Local/Temp/ajs-linux-20261023/ajiasu \
      -analysisTimeoutPerFile 900 -deleteProject
  ```

  Auto-analysis of a 2.66 MB stripped static `.text` with libevent/mbedTLS/lwIP inlined was judged
  too slow for this budget; string xrefs already answered every brief question.
- IDA (`D:/Software/Tool/WorkStation/debugger/Static-Reversing/IDA`) not launched (GUI, licence
  gating).
- `[UNKNOWN]` pending decompilation: numeric `CommandCode_*` values, FV packet header layout,
  `FV_CipherKeySize`, `X-Sig` algorithm, `OPV2` semantics, `buffer_obscure_packet` key derivation.
  Start points: `0x42b1b4` (ctrl send), `0x451192` (proxy request), `0x450427`/`0x45080b`
  (obscure buffer), `0x4405fc` (`X-Sig`).
