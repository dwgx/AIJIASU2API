# AJiaSu RE playbook — executable checklist

Companion to `REPORT.md`. Every command is copy-pasteable. Method IDs in brackets point at the
detail block in `REPORT.md`. Nothing here contacts the vendor service; the only allowed network
target is `127.0.0.1`.

Path shorthand:

```bash
WIN='C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023/AJiaSu.exe'
LIN='/mnt/c/Users/dwgx1/AppData/Local/Temp/ajs-linux-20261023/ajiasu'
LAB=/tmp/ajs-lab
```

**What was actually run for this report:** only the read-only intake commands — `sha256sum`,
`file`, `readelf -h`, `objdump -p`, `pefile`, `strings -t x`, the Python constant search, the
`product.dat` decrypt, and tool-version probes. Every other command below is **documented, not
executed** (`[UNVERIFIED]` in the sense of "not run here"); in particular no vendor binary was
executed and no live host was contacted. Lines marked `# ran` were executed on 2026-10-23.

Windows-only tools are shown with their full path because they are not on `PATH` on this host:
IDA `D:/Software/Tool/Debugger/IDA Professional 9.2/idat.exe`, injector
`D:/Software/Tool/Injc/GH-Injector-latest/`, mitmproxy
`C:/Users/dwgx1/AppData/Roaming/Python/Python314/Scripts/mitmdump.exe`.

---

## G0 — First 60 minutes on a new binary

```bash
mkdir -p $LAB && cd $LAB
sha256sum "$WIN" "$LIN"                                   # ran
wsl -d Ubuntu-24.04 -u root -- file "$LIN"                # ran
wsl -d Ubuntu-24.04 -u root -- readelf -h "$LIN" | head   # ran
objdump -p "$WIN" | grep -E "ImageBase|Subsystem|DLL Name"   # ran
python -c "import pefile;pe=pefile.PE(r'$WIN',fast_load=True);print(hex(pe.FILE_HEADER.Machine),pe.FILE_HEADER.NumberOfSections)"   # ran
strings -t x -n 5 "$WIN" | grep -F -e "/client.php" -e "X-Sig: " -e "code=200" -e "OPV2"   # ran
strings -t x -n 2 "$LIN" | grep -E "^( *[0-9a-f]+ )?(s|n|k|l|_)=" | head -20
```

Checklist (stop at the first surprise and write it down):

1. Hash + size + version string recorded before anything else.
2. PE32 vs PE32+, static vs dynamic, stripped vs symbols.
3. Import signal: `bcrypt` + `WS2_32` + `IPHLPAPI` ⇒ crypto + sockets + interface enumeration.
4. Literal table built with file offsets (`/client.php`, `X-Sig`, `code=200`, `OPV2`, `s=`…).
5. Modulus search (S14). Present ⇒ same FV family, P01 available.
6. One xref per literal opened in IDA (S05) and named builder/parser.
7. Stop. Do not run the binary until G3 (mock) exists.

## G1 — Identify a build and diff it against the analysed one

```bash
sha256sum "$NEW" && cmp -s "$NEW" "$LIN" && echo same || echo different      # S01
yr scan rules/ajs.yar "$NEW"                                                  # S11 (if installed)
radiff2 -C "$LIN" "$NEW" | head -40                                           # S15
```

Expected identity for the analysed builds: Linux `29e18740…3b9f`, Windows `0fd9d702…01f6`.
A new hash is not a new protocol; re-run G0 steps 4–5 before assuming anything.

## G2 — Static: get the call graph and the crypto functions

IDA headless (licence required):

```bash
cat > $LAB/dump.py <<'PY'
import idautils, ida_hexrays, ida_funcs, idc
out = open(r'C:/lab/functions.txt', 'w', encoding='utf-8')
for ea in idautils.Functions():
    out.write(f'{ea:08x} {ida_funcs.get_func_name(ea)}\n')
ida_hexrays.init_hexrays_plugin()
for ea in idautils.Functions():
    try:
        out.write(f'\n===== {ea:08x} =====\n{ida_hexrays.decompile(ea)}\n')
    except Exception as e:
        out.write(f'\n===== {ea:08x} decompile failed: {e}\n')
out.close()
idc.qexit(0)
PY
"D:/Software/Tool/Debugger/IDA Professional 9.2/idat.exe" -A -S"$LAB/dump.py" -L$LAB/ida.log "$WIN"
```

Ghidra headless (no licence):

```bash
cat > $LAB/GhidraDump.py <<'PY'
# Ghidra 11+/12 PyGhidra script
from ghidra.app.decompiler import DecompInterface
from ghidra.util.task import ConsoleTaskMonitor
out = open(r'C:/lab/ghidra-functions.txt', 'w')
ifc = DecompInterface(); ifc.openProgram(currentProgram)
for f in currentProgram.getFunctionManager().getFunctions(True):
    res = ifc.decompileFunction(f, 60, ConsoleTaskMonitor())
    out.write(f'===== {f.getEntryPoint()} {f.getName()} =====\n')
    out.write(res.getDecompiledFunction().getC() if res.decompileCompleted() else 'FAILED\n')
out.close()
PY
analyzeHeadless C:/lab/ghidra ajsproj -import "$WIN" -scriptPath $LAB -postScript GhidraDump.py -deleteProject
```

radare2 one-liners:

```bash
rabin2 -Izz "$LIN" | head -40
r2 -2Aqc 'afl~crypto;izz~code=200;axt @ 0x28eecf' "$LIN"
```

Targets to name first: `0x552ce0` build / `0x552640` fetch / `0x551f40` OPV2 /
`0x520df0` product.dat decrypt / `0x520830` MD5 KDF (Windows 4.9.6.0).

## G3 — Mock first, then run the client

```bash
mkdir -p $LAB/certs
export AJS_LAB=$LAB
sh research/60-dynamic/mock/make_certs.sh
python research/60-dynamic/mock/mock_control.py 443 4443 &
python research/60-dynamic/mock/mock_fv.py --port 8443 \
  --auth-frame --close-after-auth --key $LAB/ajs_priv.pem &
```

Both scripts bind `0.0.0.0`; run them in a VM or block the ports at the host firewall. The control
mock takes ports positionally and reads `$AJS_LAB/certs/server.crt|key`.

Hosts redirect + CA (see R02 for the full rollback):

```bash
cp /etc/hosts $LAB/hosts.bak
printf '127.0.0.1 login.ajs-app.com\n127.0.0.1 login.mmegg.com\n127.0.0.1 login.mbclubsh.com\n' >> /etc/hosts
SSL_CERT_FILE=$LAB/certs/ca.crt ./ajiasu -c $LAB/ajs.conf connect 'Test*'
```

Rollback, always:

```bash
cp $LAB/hosts.bak /etc/hosts
pkill -f mock_fv.py; pkill -f mock_control.py
```

## G4 — Dynamic: breakpoints with a script, not by hand

gdb (existing AJiaSu scripts break at the CTR call and dump registers):

```bash
gdb -q -batch -x research/60-dynamic/mock/dump_ctr.gdb --args ./ajiasu
gdb -q -batch -x research/60-dynamic/mock/trace_parse.gdb --args ./ajiasu
```

x64dbg (Windows, attach after starting under the lab config):

```text
x64dbg.exe -p <pid>
scriptload C:\lab\ajs.txt
```

`ajs.txt`:

```text
bp AJiaSu.exe:0x552640
log "fetch called, rsp={rsp}"
run
```

WinDbg/cdb:

```text
cdb.exe -o -c ".logopen C:\lab\ajs.log; sxe ld:ws2_32; g; lm; q" AJiaSu.exe
```

lldb (macOS):

```bash
lldb -b -o "br set -n SSL_write" -o run ./ajiasu
```

## G5 — Frida: observe without a debugger

```bash
frida -f "$WIN" -l $LAB/hooks/ws2.js --runtime=v8 -o $LAB/frida.log
```

`$LAB/hooks/ws2.js`:

```javascript
const send_ = Module.getExportByName('ws2_32.dll', 'send');
Interceptor.attach(send_, {
  onEnter(a) {
    const n = a[3].toInt32();
    console.log('send', a[1].toInt32(), n);
    console.log(hexdump(a[2].readByteArray(Math.min(n, 256)), { length: Math.min(n, 256) }));
  }
});
const connect_ = Module.getExportByName('ws2_32.dll', 'connect');
Interceptor.attach(connect_, {
  onEnter(a) {
    const sa = a[1];
    const fam = sa.readU16();
    if (fam === 2) {
      const port = (sa.add(2).readU8() << 8) | sa.add(3).readU8();
      const ip = [0,1,2,3].map(i => sa.add(4 + i).readU8()).join('.');
      console.log('connect', ip, port);
    }
  }
});
```

Stalker (bounded):

```javascript
const t = Process.getCurrentThreadId();
Stalker.follow(t, { events: { call: true },
  onCallSummary(s) { send(Object.entries(s).sort((a,b)=>b[1]-a[1]).slice(0,50)); } });
setTimeout(() => { Stalker.unfollow(t); Stalker.flush(); }, 3000);
```

frida-trace for a quick API inventory:

```bash
frida-trace -f "$WIN" -i "ws2_32!*" -i "bcrypt!*" -o $LAB/trace.log
```

## G6 — Network: capture and intercept

Capture (Windows, Npcap required):

```bash
tshark -i 5 -f "tcp port 443 or tcp port 8443" -w $LAB/cap.pcapng
```

Decrypt TLS with a keylog file (only works for OpenSSL/NSS/BoringSSL; mbedTLS ignores it, so
AJiaSu's TLS stays opaque):

```bash
set SSLKEYLOGFILE=C:\lab\sslkeys.log      # Windows (cmd)
export SSLKEYLOGFILE=$LAB/sslkeys.log     # Linux/macOS
# Wireshark: Preferences -> Protocols -> TLS -> (Pre)-Master-Secret log filename = same file
```

Capture (Linux/WSL):

```bash
tcpdump -i any -s 0 -w $LAB/cap.pcap 'tcp port 8443 or tcp port 443'
```

mitmproxy with a rewriting addon:

```bash
cat > $LAB/addons/ajs.py <<'PY'
from mitmproxy import http

def request(flow: http.HTTPFlow) -> None:
    if flow.request.path.endswith('/client.php'):
        body = flow.request.get_text()
        flow.request.set_text(body.replace('cmd=ClientApiLogin%2FLogin',
                                           'cmd=ClientApiLoginSync'))
        flow.request.headers['X-Lab'] = '1'
PY
"C:/Users/dwgx1/AppData/Roaming/Python/Python314/Scripts/mitmdump.exe" \
  -p 8080 -s $LAB/addons/ajs.py --set confdir=$LAB/mitm
```

Point the client at it (`http_proxy`/`https_proxy`, or the client's own proxy setting), and install
the mitm CA only inside the lab VM.

TLS inspection without decryption:

```bash
openssl s_client -connect 127.0.0.1:443 -servername login.ajs-app.com -showcerts </dev/null
```

Transport triage (which framing is actually used):

```bash
nghttp -nv https://127.0.0.1:443/client.php        # HTTP/2?
websocat -v ws://127.0.0.1:8080/s                  # WebSocket upgrade?
protoc --decode_raw < $LAB/body.bin                # Protobuf? (hypothesis only)
```

AJiaSu's answer: control = HTTP/1.1 form POST, proxy = custom binary FV, legacy = pseudo-WebSocket
`POST /s?s=&n=`, no gRPC/Protobuf anywhere.

## G7 — Injection and syscall-level tracing

Windows DLL injection (lab copy only):

```text
"D:/Software/Tool/Injc/GH-Injector-latest/GH Injector - x86.exe"
```

GH Injector is a GUI; the exact CLI flags are `[UNVERIFIED]`. For a scripted approach use the
`OpenProcess`/`VirtualAllocEx`/`WriteProcessMemory`/`CreateRemoteThread` sequence with
`LoadLibraryW` as the thread start.

Linux `LD_PRELOAD` shim (only works on dynamically linked targets — the AJiaSu Linux build is
**static**, so this is for the mock/emulator, not for `ajiasu`):

```c
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdio.h>
#include <sys/socket.h>

static int (*real_connect)(int, const struct sockaddr *, socklen_t);

int connect(int fd, const struct sockaddr *sa, socklen_t len) {
    if (!real_connect) real_connect = dlsym(RTLD_NEXT, "connect");
    if (sa && sa->sa_family == AF_INET) {
        const struct sockaddr_in *in = (const struct sockaddr_in *)sa;
        fprintf(stderr, "[shim] connect %08x:%u\n", ntohl(in->sin_addr.s_addr), ntohs(in->sin_port));
    }
    return real_connect(fd, sa, len);
}
```

```bash
gcc -shared -fPIC -o $LAB/shim.so $LAB/shim.c -ldl
LD_PRELOAD=$LAB/shim.so ./ajiasu -c $LAB/ajs.conf connect 'Test*'
```

macOS:

```bash
clang -dynamiclib -o $LAB/shim.dylib $LAB/shim.c
DYLD_INSERT_LIBRARIES=$LAB/shim.dylib DYLD_FORCE_FLAT_NAMESPACE=1 ./ajiasu
```

eBPF (Linux, root):

```bash
bpftrace -e 'uprobe:/tmp/ajs-lab/ajiasu:0x401000 { printf("hit\n"); }'
bpftrace -e 'tracepoint:syscalls:sys_enter_connect /comm == "ajiasu"/ { printf("%s\n", comm); }'
```

Syscall-level ground truth:

```bash
strace -f -e trace=network,openat -s 256 -o $LAB/strace.txt ./ajiasu -c $LAB/ajs.conf connect 'Test*'
ltrace -f -e "SSL_*+RSA_*+connect" -o $LAB/ltrace.txt ./ajiasu
perf trace -p "$(pgrep -n ajiasu)" -o $LAB/perf.txt
```

## G8 — Patch: modulus replacement (the AJS-07 method)

```bash
python research/60-dynamic/mock/patch_rsa.py "$LIN" $LAB/ajiasu.lab $LAB/ajs_priv.pem --offset 0x28a030
python research/60-dynamic/mock/patch_rsa.py "$WIN" $LAB/AJiaSu.lab.exe $LAB/ajs_priv.pem --offset 0x314468
```

Rules that make this safe:

- The replacement modulus **must** be exactly 309 decimal digits, otherwise every following offset
  moves and the binary breaks.
- `patch_rsa.py` regenerates keys until the digit count matches; do not hand-edit the string.
- Verify the diff is confined:

```bash
cmp -l "$LIN" $LAB/ajiasu.lab | wc -l     # ≤ 309 differing bytes, all inside the range
```

- The patched binary cannot talk to the real service (wrong key) — that is the point.

## G9 — Patch: byte patch, checksum, re-sign, rollback

Linux branch (no signature layer, no checksum):

```bash
cp "$LIN" $LAB/ajiasu.orig && sha256sum $LAB/ajiasu.orig > $LAB/MANIFEST.sha256
python - <<'PY'
b = bytearray(open('/tmp/ajs-lab/ajiasu.orig','rb').read())
b[0x451932] ^= 0x01                      # example: flip one conditional branch byte
open('/tmp/ajs-lab/ajiasu.p','wb').write(b)
PY
cmp -l $LAB/ajiasu.orig $LAB/ajiasu.p | wc -l     # expect 1
```

Windows branch (PE checksum + Authenticode):

```bash
openssl req -x509 -newkey rsa:2048 -nodes -keyout $LAB/lab.key -out $LAB/lab.pem \
  -days 2 -subj "/CN=AJS Lab Code Signing"
python -c "import pefile;pe=pefile.PE(r'$LAB/AJiaSu.lab.exe');print(hex(pe.OPTIONAL_HEADER.CheckSum), hex(pe.generate_checksum()))"
osslsigncode sign -certs $LAB/lab.pem -key $LAB/lab.key -n "lab" \
  -in $LAB/AJiaSu.lab.exe -out $LAB/AJiaSu.lab-signed.exe
```

macOS branch (ad-hoc):

```bash
codesign --force --sign - $LAB/ajiasu.p
```

Rollback verification (checks the *original*, which is what the manifest describes):

```bash
sha256sum -c $LAB/MANIFEST.sha256
```

Rollback procedure: `cp $LAB/ajiasu.orig "$LIN"` (or the Windows original), re-hash, compare with
`MANIFEST.sha256`, delete the patched copy and the private key.

## G10 — Protocol fuzzing and differential testing (mock only)

Replay:

```bash
python - <<'PY'
import socket
frame = open('research/60-dynamic/captures/fvmock-1.bin','rb').read()
s = socket.create_connection(('127.0.0.1', 8443)); s.sendall(frame)
print(s.recv(4096)[:32].hex())
PY
```

Mutation matrix (field → cases): `s` (empty / wrong sid / 64 chars), `n` (31 hex / 33 hex /
non-hex), `k` (empty / random 176 b64 / OAEP prefix removed), `l` (0 / declared±1 / 1e6), `_`
(absent / 200 chars / 10 KB), body length (0 / truncated / trailing bytes).

Grammar-aware harness skeleton:

```python
import base64, os, socket
from research40 import fv_proto as F   # adapt import to your path

def frame(s="", n=os.urandom(16).hex(), k="", l=None, body=b"", pad="a"*10):
    text = f"s={s}\nn={n}\nk={k}\n"
    if l is not None: text += f"l={l}\n"
    text += f"_={pad}"
    K = os.urandom(16)
    return K + F.aes_ctr(K, b"\x00"*16, len(text).to_bytes(4,"big") + text.encode() + body)

for case in cases:
    with socket.create_connection(("127.0.0.1", 8443)) as s:
        s.sendall(frame(**case)); print(s.recv(4096).hex())
```

State-machine replay order (must hold):

1. `code=200` block — **one** length block only.
2. auth map encrypted with `AES-CTR(sessionKey, IV=n)`.
3. `shutdown(SHUT_WR)` — the server must half-close after the auth map.

Removing step 3 (the half-close) stalls the client before `state=200`; duplicating the length
block makes the parser read garbage and time out.

## G11 — Request research / 魔改

Control API (mock or mitm):

- Rewrite one field at a time: `cmd`, `_v`, `cv`, `_uid`, `_vrf`, `SyncVersion` request.
- Replay a captured body verbatim; then remove one parameter per run and record the JSON error.
- `SyncVersion` must stay an **object**; `Login` must contain `UrlWeb` and `LatestVersion`, or the
  client asserts (`research/60-dynamic/REPORT.md`).

FV traffic:

- Session/lease: same `sid` on two data channels; `sid` after auth close; `sid` after `kat` window.
- Error paths: `r=err`, missing `sid`, slow `code=200`, TCP reset — record retry timing.
- Rate limits: only against the mock; the live service is out of scope.

Certificate substitution: lab CA in the system store (Windows `certutil -addstore Root`,
macOS `security add-trusted-cert`, Linux `update-ca-certificates`), then N08 if it still fails.

## G12 — When not to patch

- A mock already reproduces the behaviour ⇒ no patch.
- The value is data (`product.dat`, host list) ⇒ decrypt/override, do not patch code.
- The edit changes length ⇒ every hard-coded offset breaks.
- The artifact is signed and the signature matters downstream ⇒ lab-only re-sign.
- The change is a single observation ⇒ debugger write or Frida hook.
- The build is newer than the analysis ⇒ re-run G0 first; offsets are build-specific.

## G13 — Session teardown

```bash
sha256sum -c $LAB/MANIFEST.sha256
cp $LAB/hosts.bak /etc/hosts
certutil -delstore Root <thumbprint>            # Windows
security delete-certificate -c "AJS Mock CA"    # macOS
rm -f $LAB/ajs_priv.pem $LAB/*.p $LAB/*.lab.exe
pkill -f mock_fv.py; pkill -f mock_control.py; frida-kill -n AJiaSu.exe
```

Never leave a hosts redirect, a trusted lab CA, or a patched vendor binary behind.
