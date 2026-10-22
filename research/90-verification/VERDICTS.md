# Independent verification — wave 1

Re-derivation of the wave-1 claims from artifacts and commands only. Author prose was not accepted
as evidence; every row names the command output under `evidence/` that decided it. Verifier session
AJS-08, 2026-10-23, read-only on all other slices, no live service contact (the only network reads
were the two public `91ajs.com` pages the brief explicitly asked for: `/terms` and `/download`).

## Summary

- 18 claims re-derived: **16 CONFIRMED**, **0 WEAKENED**, **2 OVERTURNED** (both are *author labels*,
  not the underlying measurements).
- The two overturns are one arithmetic label repeated in two reports, plus one stale qualifier:
  1. The RSA modulus is **1024-bit** (309 decimal digits, `int.bit_length() == 1024`, 128-byte block).
     `30-linux-static/REPORT.md:22` and `:198` call it "2048-bit", and `10-osint/REPORT.md:43` repeats
     "RSA is 2048-bit". `40-protocol/PROTOCOL.md:613` already has the correct value, so wave 1
     contradicts itself; the arithmetic and the binaries decide for 1024.
  2. `40-protocol/PROTOCOL.md:1020` calls `06ka4z0dc6ga02c` a "**Windows-only** literal". It is also
     present in the Linux 4.3.0.1 client at `0x293080`, immediately after `X-Sig: `/`X-Timestamp: `.
- Everything else survived re-derivation, including the two claims most likely to be prose-driven:
  the eight Windows transport-string offsets (8/8 exact) and the signing vectors (three independent
  implementations agree: my Python written from the spec text, the vendored Java harness, and the
  slice's own suite printing `PASS 7`).
- Negative results were treated as negative results, not as proof of absence: the OpenAJS secret is
  absent as ASCII, UTF-16 and 20 raw bytes from all four artifacts, but a non-literal derivation is
  not excluded by that search.
- Slice validators re-run unchanged: `10-osint` → `OK sources=36 claims=36`; `20-windows-static` →
  `OK files=37 hashes=37`; `30-linux-static` → `OK linux sha256=29e18740… strings=236`.

## Verdicts

| # | claim | verdict | evidence |
|---|---|---|---|
| 1 | Bootstrap: commit `3786ca5`, branch `main`, six tracked files, installer untracked/ignored | CONFIRMED | `evidence/C01-bootstrap.log` — `git log --oneline` = `3786ca5 Bootstrap research repo…`; `git branch --show-current` = `main`; `git ls-files` count = 6; `git check-ignore -v research/AJiaSu_4.9.6.0.exe` → `.gitignore:2:research/**/*.exe`; `git status --ignored --porcelain` lists it as `!!` |
| 2 | OSINT: `sources.tsv` ≥20 rows, `claims.tsv` ≥15 rows, every claim sourced or tagged | CONFIRMED | `evidence/C02-tsv-integrity.log` — sources=36, claims=36, 0 claims without a source id, 0 dangling source refs, 0 empty quotes; only C11 carries `[UNKNOWN]`, no claim is bare |
| 3 | OSINT: ToS 7.2 forbids reverse engineering; 4.3 and 4.4(4) forbid sharing the connection / unauthorized proxy | CONFIRMED | `evidence/C03-tos-clauses.log` + `evidence/terms.html` (HTTP 200, 42,227 B) — 7.2 "不得通过反向工程、反编译、反汇编…"; 4.3 "不得将网络流量或连接权限以任何形式提供、共享或转让给他人使用"; 4.4（4）"为第三方提供未经授权的代理服务"; version date 2025年10月22日 |
| 4 | OSINT: official matrix Windows 4.9.6.0, Linux 4.3.0.1, Android 4.12.2.0, iOS 4.11.5.11, macOS 4.9.6.0 | CONFIRMED | `evidence/C04-version-matrix.log` + `evidence/download.html` (HTTP 200) — Windows 4.9.6.0, macOS 4.9.6.0, Android 4.12.2.0, iOS 4.11.5.11, HarmonyOS 1.0.1.0, Linux 4.3.0.1, Apple TV 1.0.2.3; five Linux arch links (x86/amd64/aarch64/armel/armeb) |
| 5 | Windows: 37 payload files; `AJiaSu.exe` sha256 `0fd9d702…`; `tap0901.sys` is stock OpenVPN TAP-Windows | CONFIRMED | `evidence/C05-all-hashes-win.log` — 37/37 files present and 37/37 sha256+size match `hashes.tsv`, 12,965,683 B total (33 install-tree + 4 `$PLUGINSDIR`); `evidence/C05-sha256-win.log` — `0fd9d702bf9932836ad0ba3e4acf43bb1a587c45ee6247b7a5d88aede78101f6`; INF header "Copyright (C) 2002-2014 OpenVPN Technologies, Inc.", `CatalogFile = tap0901.cat`, catalog signer `CN="OpenVPN Technologies, Inc."` / `CN=Microsoft Windows Hardware Compatibility Publisher`, sys PDB path `tap-windows6` |
| 6 | Windows: `OPV2`, `POST /s?s=%s&n=%s HTTP/1.1`, `Upgrade: websocket`, `code=200` at the claimed offsets | CONFIRMED | `evidence/C06-win-strings-offsets.log` — 8/8 literals byte-exact at `0x3216d0`, `0x3216d8`, `0x3216fc`, `0x321718`, `0x321734`, `0x321750`, `0x321784`, `0x3217c4`; each literal occurs exactly once in `AJiaSu.exe` |
| 7 | Windows: `product.dat` is 818 B and not plaintext; `ServerApiUrls` is a literal in `AJiaSu.exe` | CONFIRMED | `evidence/C07-productdat-and-hosts.log` — 818 B, entropy 7.7356 bit/B, 245 distinct bytes, no ZIP/GZIP/7z/PE magic, no `[CONFIG]`/`ServerApiUrls`/`http` substring, 51 unique 16-byte blocks; `ServerApiUrls` ASCII at `0x318094` (UTF-16 `product.dat` at `0x317dfc`). No API hostname literal (`ajs-app.com`, `mmegg.com`, `mbclubsh.com`) anywhere in the exe |
| 8 | Linux: 4.2.3.0 sha256 `29e18740…`; 4.3.0.1 tarball `0affe73a…` and binary `08fcd866…` | CONFIRMED | `evidence/C08-sha256-linux.log`, `evidence/C08b-tarball-members.log` — 4.2.3.0 binary `29e18740af8b69c6…`, archive `c3d93551…` (matches S16), single member `ajiasu` 0755; 4.3.0.1 archive `0affe73aff4b7ee1…`, member `08fcd8668f7aaf66…`; both members hash equal to the extracted files |
| 9 | Linux: `./ajiasu -h` prints the six protocol modes and `--sys-env-id`; no root needed for proxy mode | CONFIRMED | `evidence/C09-linux-help.log` (run as root in WSL, exit 0) — `protocol {udp,tcp,lwip,socks5,http,proxy}` (six) and `--sys-env-id 1234`; `evidence/C09c-root-string.log` — binary's own text at `0x28a210`: `root required… you can use protocol:proxy (socks5/http) which doesn't require root`. Caveat: the no-root half is vendor-stated and statically consistent; runtime proxy start needs an account and was **not** exercised |
| 10 | Linux: four API hosts at the claimed offsets; `/client.php` and `ClientApiLogin/Login` present | CONFIRMED | `evidence/C10-linux-strings-offsets.log` — 4.2.3.0 exact at `0x28ac34`, `0x28ac4f`, `0x28ac68`, `0x28ac86`, `/client.php` `0x28d901`, `ClientApiLogin/Login` `0x28da99`; `evidence/C10b-linux-4301-strings.log` — 4.3.0.1 has the same four hosts relocated (`0x29021c`…`0x29026e`), `/client.php` `0x2932d9`, action `0x2932e5` |
| 11 | Linux: proxy transport strings present; raw `CommandCode_Ctrl` is the VPN path | CONFIRMED | `evidence/C10-linux-strings-offsets.log` — `OPV2` `0x28ee18`, `POST /s?s=%s&n=%s HTTP/1.1\r\n` `0x28ee1d`, `Upgrade: websocket\r\n` `0x28ee3a`, `Sec-WebSocket-Key: %s\r\n` `0x28ee7a`, `code=200` `0x28eecf`; `evidence/C11e-string-xrefs.log` — `CommandCode_Ctrl` sender `0x42b1b4` is called only from `0x42c1fa…0x42c684` (6 sites, control block; its log string is referenced at `0x42b2af`), while the proxy request builder `0x451192` is called only from `0x419dfc` (`fvproxy_auth_context_new`) and `0x424a53` (`Remote(Relay)` handshake) |
| 12 | Linux: proxy port candidates `{1080,18888,28888,38888}` are a fixed array, not an incrementing counter | CONFIRMED | `evidence/C12c-objdump-ports.log` — at `0x41a3c0`: `movw $0x438`, `$0x49c8`, `$0x70d8`, `$0x97e8`, `$0x0` into `[rbp-0x5a]`…`[rbp-0x52]`, then `cmpq $0x4` loop bound; same five `movw` at `0x41e0b1` in 4.3.0.1 |
| 13 | Protocol: `dualHash`/`sign`/`buildQuery` match the Java harness for the published vectors; `PASS 7` | CONFIRMED | `evidence/C13-dualhash-independent.log` — my own implementation from the spec text reproduces 4/4 dualHash+sign and 3/3 buildQuery vectors; `evidence/C13b-java-harness.log` + `C13c-java-vectors.log` — vendored `FVSign.java` compiled with JDK 21 and run on my input produces the same 7 values; `evidence/C13e-protocol-tests.log` — `PASS 7`, exit 0 |
| 14 | Protocol: RSA modulus is 1024-bit, not 2048-bit; ciphertext is 128 bytes; the Linux report's "2048-bit" label is wrong | CONFIRMED | `evidence/C14-rsa-modulus.log` — 309-digit modulus at `0x28a030` (Linux 4.2.3.0), `0x314468` (Windows), `0x28f548` (Linux 4.3.0.1), `int.bit_length() == 1024` in all three, values byte-identical, block size 128 bytes, exponent 65537 |
| 15 | Protocol: the shipping `X-Sig` secret is not the OpenAJS constant; `06ka4z0dc6ga02c` is unclassified | CONFIRMED | `evidence/C15-xsig-secret-search.log` — OpenAJS secret 0 hits as colon string, lowercase, colon-stripped, UTF-16 and 20 raw bytes in `AJiaSu.exe`, `product.dat`, Linux 4.2.3.0 and 4.3.0.1; `06ka4z0dc6ga02c` present at Windows `0x31e160` and Linux 4.3.0.1 `0x293080`, adjacent to `X-Sig: `/`X-Timestamp: `; absent from 4.2.3.0 and `product.dat`. Absence of a literal is not proof of non-use, so "not the OpenAJS constant" rests on absence plus the encrypted `product.dat` |
| 16 | Hygiene: no credentials/tokens/private data in the repo; the leaked account in `PainterXIII/YouRan_Proxy` is redacted | CONFIRMED | `evidence/C16-hygiene.log` — no `*.env`/`*.key`/`*.pem`/`ajiasu.conf`/`LoginSync*` in the tree; no private-key body, no `ghp_`/`AKIA` tokens, no e-mail addresses or CN mobile numbers in tracked or untracked text; the only PEM string is the mbedTLS header literal inside the vendor strings dump; single-commit history contains no secrets; `C34`/`S23` carry no values. The upstream leak itself was **not** re-fetched (would ingest a credential) → that sub-claim is `[UNKNOWN]` |
| 17 | Author label: `30-linux-static/REPORT.md:22`,`:198` and `10-osint/REPORT.md:43` call the modulus "2048-bit" | OVERTURNED | `evidence/C14-rsa-modulus.log` — `int.bit_length() == 1024`, 128-byte modulus, identical in all three binaries; `40-protocol/PROTOCOL.md:613` already states 1024, so wave 1 is internally inconsistent |
| 18 | Author label: `40-protocol/PROTOCOL.md:1020` calls `06ka4z0dc6ga02c` a "Windows-only literal" | OVERTURNED | `evidence/C15-xsig-secret-search.log` — also present in Linux 4.3.0.1 at `0x293080`; the literal is cross-platform, its role remains unclassified |

## Contradictions

1. **RSA size — 1024 vs 2048.** `30-linux-static/REPORT.md:22` ("RSA-2048-OAEP-wrapped AES key") and
   `:198` ("a 2048-bit modulus is stored as a decimal literal"), plus `10-osint/REPORT.md:43`
   ("RSA is 2048-bit"), contradict `40-protocol/PROTOCOL.md:613` ("1024-bit, not 2048-bit").
   Decided by arithmetic on the artifact, not by either report: the literal at `0x28a030` has 309
   decimal digits and `int.bit_length() == 1024`; the Windows (`0x314468`) and Linux 4.3.0.1
   (`0x28f548`) literals are the same integer. A 2048-bit key would need 617–618 digits.
   Consequence: ciphertext is 128 bytes, and `PROTOCOL.md:627`'s "256-byte ciphertext" foil is right.
2. **`06ka4z0dc6ga02c` is not Windows-only.** `PROTOCOL.md:1020` says "Windows-only literal"; the
   string is at `0x293080` in Linux 4.3.0.1, in the same rodata neighbourhood as `X-Sig: `
   (`0x293065`) and `X-Timestamp: ` (`0x29306d`). It is absent from Linux 4.2.3.0, so the honest
   statement is "present in 4.9.6.0 and 4.3.0.1, absent from 4.2.3.0, role unproven".
3. **Minor — Windows key-offset range.** `20-windows-static/INVENTORY.md:191` gives
   `0x318080`–`0x318094` for `ServerApiUrls`/`ClientSite`/`ClientDistChan`. Actual starts:
   `ClientSite` `0x318070`, `ClientDistChan` `0x318080`, `ServerApiUrls` `0x318094`. The range is
   short by 16 bytes at the low end; the `ServerApiUrls` offset itself is correct.
4. **Minor — control-verb strings.** `30-linux-static/REPORT.md:311` attributes `con`/`ping`/`pong`/`off`
   to the six `CommandCode_Ctrl` call-site functions. Those six (`0x42c1c8`…`0x42c684`) are tiny
   wrappers with no string literals; the verbs live in the enclosing control-loop functions. The
   transport conclusion (Ctrl = VPN path, HTTP upgrade = proxy path) is unaffected — it rests on the
   xrefs in `evidence/C11e-string-xrefs.log`.

## Self-audit

**A — early output.** The `evidence/` directory and the `VERDICTS.md` skeleton were written before the
first verdict; evidence files were appended per command batch (`C01`…`C18`), not reconstructed at the end.

**B — rebuttal.** I re-attacked my three strongest CONFIRMED rows. (i) RSA: searched for any second
long decimal literal in all three binaries — exactly one each, all equal, all 1024-bit; a 2048-bit
reading is arithmetically impossible. (ii) Windows offsets: checked not just the eight claimed
offsets but every occurrence of each literal — each occurs once, so the offsets cannot be
coincidental. (iii) dualHash: wrote a second implementation from the prose (not importing
`reference/fv_proto.py`) and ran the Java harness on my own input file; both agree with the published
vectors, so a matching-but-wrong reference is ruled out. Where author prose and command output
disagreed (the 2048 label, "Windows-only"), the command output won.

**C — DONE WHEN.** `python research/90-verification/validate.py` → `OK verdicts=18`, exit 0 (pasted in
the final answer; reproducible from `evidence/C18-validate.log`).

**D — summary.** Established: bootstrap state, OSINT sourcing and the live ToS/version pages, all 37
Windows hashes, all eight Windows transport offsets, `product.dat` opacity, all three Linux hashes,
`-h` output, Linux host/endpoint/proxy-string offsets, the fixed port array, the signing vectors, the
1024-bit modulus, and repo hygiene. Overturned: the "2048-bit" labels (two reports) and the
"Windows-only" qualifier on `06ka4z0dc6ga02c`. Unknown: whether the shipping clients derive the
`X-Sig` secret by some non-literal route (absence of a literal is not proof), the runtime behaviour
of proxy mode without root, and whether the upstream `YouRan_Proxy` leak is real (not fetched).
Next: a mock-server lab (AJS-07) can settle the `X-Sig` secret and the 128-byte `k=` field by
observation. What I could still be wrong about: treating the `CommandCode_Ctrl` xref set as complete
— an indirect call through a function pointer would not appear in my direct-call scan.
