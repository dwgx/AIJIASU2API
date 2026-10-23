# AJS-18 — Request research and mutation lab (AJiaSu control plane and FV carrier)

Date: 2026-10-23. Slice: `research/75-request-mutation/` only.
Scope: capture, replay, mutate, fuzz, and observe the AJiaSu control plane and the FV proxy
carrier **against local mocks**. No vendor host is contacted; no real account is used; no
credential leaves the lab VM.

Evidence status:

| Claim class | Evidence |
|---|---|
| Codec and guard behavior | `python lab/labctl.py selftest` → 11/11 checks true, 2026-10-23, Python 3.14.7 / cryptography 48.0.1 (this host, offline) |
| Wire format | `research/60-dynamic/REPORT.md` (Linux `ajiasu` 4.2.3.0 capture) and `research/40-protocol/PROTOCOL.md` §Proxy carrier |
| Network procedures | **`[UNVERIFIED]` at runtime** — WSL is stopped per Owner; every command that opens a socket is design-validated only |
| Matrix coverage | `mutation-matrix.tsv`: 58 rows — control_request 20, fv_auth 16, control_response 9, fv_response 7, fv_data 6 |

## Answer

1. **The lab is usable today without WSL.** `lab/labctl.py` runs offline: CA generation, mock
   control plane, mock FV server, tap/replay, redaction, normalized diff, mutation
   materialization, RSA key generation, modulus patching, frame parse/mutate. The self-test
   exercises the codec, both mocks over real sockets on loopback, redaction, diff, mutation
   operators, and the destination allowlist: 11/11 true (2026-10-23). `[CONFIRMED]`
2. **The control plane is the soft entry point, but only where TLS is not pinned.** OpenAJS
   installs a trust-all `X509TrustManager` (`AjsClient.java:66`, `InsecureTLS.java:13-41`), and
   the Linux 4.2.3.0 client accepted a lab CA installed in the system store in AJS-07. That
   proves *no pinning to a vendor key on that path*; it does not prove the shipping client
   ignores the trust store. Windows 4.9.6.0 TLS behavior remains `[UNKNOWN]`.
   `[CONFIRMED for Linux 4.2.3.0 / WEAKENED as a general claim]`
3. **The FV carrier does not need TLS interception at all.** It is raw TCP with an application
   layer: `[16B K][AES-128-CTR(K, IV=0)(4B BE text_len || text || body)]`, `k=` is
   `base64("OAEP" || RSA-OAEP-SHA1(sessionKey))`, and a 1024-bit vendor modulus can be replaced
   in a temp binary copy (offset `0x28a030`, 309 decimal digits) to decrypt with a lab key.
   AJS-07 confirmed OAEP-SHA1 succeeds and OAEP-SHA256 fails. `[CONFIRMED]`
4. **Request mutation is cheap and broad.** 58 matrix rows span five layers. The mock control
   plane answers every case deterministically, and `send_mutant.py` refuses any target outside
   loopback/`10.75.0.0/24` before opening a socket. `[CONFIRMED offline; runtime UNVERIFIED]`
5. **Response mutation is limited by the mock, not by the client.** The mock supports arbitrary
   fixture JSON, HTTP status, and gzip/deflate. Three failure fixtures are ready
   (`AuthResult=FAIL`, missing `Servers`, non-object `SyncVersion`) plus a status flag for
   HTTP 500. Byte-level truncation and duplicate length blocks need a raw relay or the frame
   mutator, not the HTTP mock. `[CONFIRMED offline]`
6. **Fuzzing has a defined taxonomy.** Length (`l`), IV (`n`), key (`k`), padding (`_`), body
   truncation, and oversized text each map to a distinct mock log event (`fv_error`,
   `fv_auth`, `fv_data`) and a distinct client symptom. The codec itself refuses text > 65535
   bytes, so that boundary is enforced before the wire. `[CONFIRMED offline / runtime UNVERIFIED]`
7. **Hooking is the fallback, not the default.** Use MITM for the control plane, the frame
   codec for FV, and Frida/`LD_PRELOAD`/API Monitor only when TLS pinning or signing blocks
   both. On a static mbedTLS build, `LD_PRELOAD` and `frida-trace -i SSL_*` often find nothing;
   hook by module base + offset instead. `[INFERENCE from 74-re-methods evidence]`
8. **Panel integration is a contract, not a feature toggle.** Research mode records a session
   against a mock, replays it, mutates it, and returns a normalized diff whose non-empty result
   is a regression signal. It must be loopback-only, opt-in, and unable to address a vendor
   host. `[DESIGN]`

## Lab topology

```
 Windows host (repo, reports, validate.py)          WSL2 Ubuntu-24.04 lab VM (root shell)
 ─────────────────────────────────────────          ────────────────────────────────────────
 D:\Project\LovePlusSpeedToProxy                     /opt/ajs-lab  (checkout of this slice)
   research/75-request-mutation/  ──── copy ────▶      lab/, mutation-matrix.tsv, validate.py
   REPORT.md / LAB.md / validate.py                    /var/lib/ajs-lab/captures  (raw, purgeable)
                                                       /var/backups/ajs-lab       (hosts.bak)
                                                       /run/ajs-lab               (pid/lock, ephemeral)

                                   10.75.0.1/24  (dummy iface "ajs-lab", no default route)
                                   ├── :443, :4443   labctl mock-control  (TLS, lab CA)
                                   ├── :8444         labctl tap           (byte recorder)
                                   └── :8445         labctl mock-fv       (raw FV carrier, lab RSA)

 vendor client (lab copy)                           DNS: /etc/hosts
   /tmp/ajs-lab/ajiasu-patched  ──────────────────▶ login.ajs-app.com   → 10.75.0.1
     ├─ POST https://<host>/client.php  ──────────▶ mock-control :443/:4443
     └─ SOCKS5 127.0.0.1:1080 ── flow ───────────▶ tap :8444 ──▶ mock-fv :8445
                                                     (one TCP connection per proxied flow)
```

| Component | Bind | Started by | Trust boundary |
|---|---|---|---|
| `mock-control` | `10.75.0.1:443`, `:4443` | `labctl.py mock-control` | Lab CA leaf; fixture is the only server truth |
| `tap` | `10.75.0.1:8444` → `10.75.0.1:8445` | `labctl.py tap` | Records ciphertext both directions; holds no key |
| `mock-fv` | `10.75.0.1:8445` | `labctl.py mock-fv` | Holds lab RSA private key; decrypts `k=` |
| Patched client | loopback only | operator | Vendor code, lab copy, lab modulus |
| Captures | `/var/lib/ajs-lab/captures` | all of the above | Raw secrets; never exported |

Addressing rules:

- `10.75.0.0/24` is lab-only and is **not** routed anywhere. `assert_lab_host()` accepts only
  loopback and this prefix; a resolved address outside it aborts the command.
- `sip=10.75.0.2`, `cip=10.75.0.3`, `dns=10.75.0.1` are decorative values inside the auth map.
  Nothing is routed to them; do not add routes for them.
- The client's local listener stays on `127.0.0.1:1080`. The lab never exposes it on `0.0.0.0`.

Trust boundaries, explicitly:

1. The vendor client is untrusted code. It runs only as a lab copy, with a lab modulus, in the
   lab VM.
2. The lab CA and the lab RSA key are lab secrets. They live under `lab/pki/` (gitignored; private
   keys are `0600`) and are destroyed after the run.
3. `sealed.jsonl` is the only unredacted artifact. It stays inside `captures/` on the lab VM and
   is deleted by `rollback.sh purge`.

## Scenarios

Ten scenarios. Each names a goal, the setup, exact commands, what to observe, expected
artifacts, safety/rollback, and what it proves. Commands assume a root shell in the lab VM with
`LAB=/opt/ajs-lab`, `CAPTURES=/var/lib/ajs-lab/captures`, `CLIENT=/tmp/ajs-lab/ajiasu`
(pristine), `PATCHED=/tmp/ajs-lab/ajiasu-patched` (lab-modulus copy),
`CONF=/tmp/ajs-lab/ajs.conf`, and `AJS_LAB=1` exported. The dummy account is `test` / `test`
(AJS-07); never a real account.

### S1 — Mock control plane (`/client.php`) with capture and replay

**Goal** Serve the four hard-coded API hosts locally over TLS, capture every request with
redaction, and prove the client completes login without any vendor host.

**Setup** Dummy interface, lab CA with the three vendor names as SANs (the fourth hard-coded
host is the same name on `:4443`), hosts redirect, mock on 443/4443, dummy fixture
(`lab/fixtures/login_ok.json`).

**Commands**

```bash
export LAB=/opt/ajs-lab AJS_LAB=1 CAPTURES=/var/lib/ajs-lab/captures
cd "$LAB"
ip link add ajs-lab type dummy 2>/dev/null || true
ip addr add 10.75.0.1/24 dev ajs-lab 2>/dev/null || true
ip link set ajs-lab up

python3 lab/labctl.py ca --dir lab/pki \
  --host login.ajs-app.com --host login.mmegg.com --host login.mbclubsh.com \
  --ip 10.75.0.1 --days 2
install -m 0644 lab/pki/ca.pem /usr/local/share/ca-certificates/ajs-lab-ca.crt
update-ca-certificates

mkdir -p /var/backups/ajs-lab "$CAPTURES"
cp -n /etc/hosts /var/backups/ajs-lab/hosts.bak
printf '10.75.0.1 login.ajs-app.com login.mmegg.com login.mbclubsh.com\n' >> /etc/hosts

head -c 32 /dev/urandom > lab/pki/salt.bin && chmod 600 lab/pki/salt.bin
python3 lab/labctl.py mock-control --bind 10.75.0.1 --port 443 --port 4443 \
  --cert lab/pki/server.pem --key lab/pki/server.key \
  --fixture lab/fixtures/login_ok.json \
  --capture "$CAPTURES/control" --salt-file lab/pki/salt.bin \
  --i-understand-lab-only &

$CLIENT -c "$CONF" connect 'Test*'
```

**Observe** One capture line per POST, with the TLS version/cipher the client negotiated, the
header set, the field map, and `body_len`. `password` must appear as `<redacted>`; `_uid` (and
`_os_did` when the client sends it) as `<hmac:…>`; `t`/`_` as `<volatile>`. `sealed.jsonl` holds
the raw body at mode `0600` and is the only file that contains the dummy password in clear.

```bash
python3 - <<'PY'
import json, os
p = os.path.join(os.environ["CAPTURES"], "control", "capture.jsonl")
for line in open(p, encoding="utf-8"):
    r = json.loads(line)
    print(r["layer"], r["path"], r["body_len"], sorted(r["fields"]))
PY
grep -c '<redacted>' "$CAPTURES/control/capture.jsonl"
stat -c '%a %n' "$CAPTURES/control/sealed.jsonl"
```

**Artifacts** `$CAPTURES/control/capture.jsonl`, `$CAPTURES/control/sealed.jsonl`,
`lab/pki/{ca.pem,ca.key,server.pem,server.key,salt.bin}`.

**Safety / rollback** `/etc/hosts` is backed up before the edit. The mock is loopback/`10.75`
only. Teardown: `lab/rollback.sh restore` restores hosts, removes the CA, deletes the dummy
interface's netns (if used), and frees ports; `rm -rf lab/pki` destroys the lab keys.

**Proves** The client can be driven end to end by a local control plane; the capture schema and
redaction rules are sufficient for research; no vendor dependency is required for login.

### S2 — TLS interception, key logging, and pinning detection

**Goal** Establish whether the control channel can be intercepted with a lab CA, whether the
client honors `SSLKEYLOGFILE`, and how to detect pinning before wasting time on MITM.

**Setup** S1 running. A second, untrusted CA (`lab/pki/rogue`) and a leaf for the same names.
The distinction that matters is *system-trusted lab CA* vs *untrusted CA*: rejecting the second
proves certificate verification; accepting both proves trust-all.

**Commands**

```bash
# 1. Handshake with the trusted lab CA (expected: verify OK, TLS >= 1.2)
openssl s_client -connect 10.75.0.1:443 -servername login.mmegg.com \
  -CAfile lab/pki/ca.pem -brief </dev/null | tee "$CAPTURES/tls_probe.txt"

# 2. Same leaf, no trust anchors (expected: verify error; the lab CA is in the system
#    bundle after S1, so -no-CAfile/-no-CApath is what makes the chain untrusted)
openssl s_client -connect 10.75.0.1:443 -servername login.mmegg.com \
  -no-CAfile -no-CApath -brief </dev/null 2>&1 | tail -5

# 3. Key log (works only if the client links a TLS stack that honors it)
SSLKEYLOGFILE=/tmp/ajs-lab/keys.log $CLIENT -c "$CONF" connect 'Test*'
wc -l /tmp/ajs-lab/keys.log 2>/dev/null || echo 'no keylog lines: fall back to S7'

# 4. Client-level pinning probe: rogue leaf, lab CA removed from the store
python3 lab/labctl.py ca --dir lab/pki/rogue --host login.mmegg.com --ip 10.75.0.1 --days 1
pkill -f 'lab/labctl.py mock-control' 2>/dev/null || true   # stop the S1 mock
rm -f /usr/local/share/ca-certificates/ajs-lab-ca.crt && update-ca-certificates --fresh
python3 lab/labctl.py mock-control --bind 10.75.0.1 --port 443 --port 4443 \
  --cert lab/pki/rogue/server.pem --key lab/pki/rogue/server.key \
  --fixture lab/fixtures/login_ok.json \
  --capture "$CAPTURES/control_rogue" --salt-file lab/pki/salt.bin \
  --i-understand-lab-only &
$CLIENT -c "$CONF" connect 'Test*' 2>&1 | tee "$CAPTURES/client_rogue.log"
```

**Observe** `Verify return code` in the `openssl` output; whether the client aborts before the
HTTP request or posts anyway; whether the key log gains lines. In the capture, `tls.version` and
`tls.cipher` show what the client actually negotiated.

**Artifacts** `$CAPTURES/tls_probe.txt`, `/tmp/ajs-lab/keys.log` (if any), capture records with
`tls` fields.

**Safety / rollback** The rogue CA never leaves `lab/pki/rogue`. Reinstall the lab CA before the
next scenario (`install … && update-ca-certificates`) or run `rollback.sh restore` and redo S1
setup. **Never** attempt a pinning bypass against a live host: if pinning is enforced, stop and
use S3 (FV is not TLS) or S7 on a lab copy.

**Proves** Where interception stops. If the untrusted leaf is rejected, the client verifies
against the store and MITM requires a trusted CA (fine in the lab, impossible against the vendor
without compromise). If it is accepted, the client is trust-all and pinning is absent on that
path. Neither result authorizes touching the live service.

### S3 — FV carrier capture, decrypt, and replay

**Goal** Record the raw FV auth and data connections, decrypt them with the lab RSA key, and
replay the captured client bytes against a fresh mock.

**Setup** Lab 1024-bit RSA key, patched temp copy of the client, `mock-fv` on 8445, `tap` on
8444 in front of it, fixture pointing `svr=10.75.0.1` and `par.fv.p=8444`.

**Commands**

```bash
cd "$LAB"
python3 lab/labctl.py keygen --out lab/pki --bits 1024
cp "$CLIENT" "$PATCHED"
python3 lab/labctl.py patch-modulus --binary "$PATCHED" \
  --modulus-file lab/pki/fv.modulus --out "$PATCHED"

python3 lab/labctl.py mock-fv --bind 10.75.0.1 --port 8445 \
  --rsa-key lab/pki/fv.key --capture-log "$CAPTURES/fv.jsonl" \
  --i-understand-lab-only &
python3 lab/labctl.py tap --listen 10.75.0.1:8444 --upstream 10.75.0.1:8445 \
  --out "$CAPTURES/tap" --i-understand-lab-only &

$PATCHED -c "$CONF" connect 'Test*'        # reaches state=200, listens on 127.0.0.1:1080
curl -sS --max-time 5 --socks5-hostname 127.0.0.1:1080 http://example.com/ \
  -o /dev/null -w '%{http_code}\n' || echo 'expected: mock has no relay, timeout is fine'

python3 lab/labctl.py replay --capture "$CAPTURES/tap" \
  --target 10.75.0.1:8445 --out "$CAPTURES/replay" --i-understand-lab-only
python3 lab/labctl.py frame parse --file "$CAPTURES/tap/conn-0001.c2s.bin" \
  --rsa-key lab/pki/fv.key --show-secrets
```

**Observe** AJS-07 reference values: auth connection 679 bytes (`text_len=234`, body 425),
data connection 207 bytes (`text_len=169`, body 18), `l=` present only on auth, `k=` empty on
data. The data body decrypts to `05 01 00 03 0b "example.com" 00 50` (SOCKS5 CONNECT). Replaying
the auth frame against a fresh mock yields a new `sid` and a `code=200` block; the old `k=`
still decrypts because the lab key is unchanged. Replaying the data frame unchanged is refused
(`code=403`) because it carries the old `sid`, which the new mock does not know — use S9's
`--permissive` or a fresh auth if the goal is to exercise the data path.

**Artifacts** `$CAPTURES/tap/conn-*.{c2s,s2c}.bin`, `$CAPTURES/tap/tap.jsonl`,
`$CAPTURES/replay/*.s2c.bin`, `$CAPTURES/fv.jsonl`, `lab/pki/fv.key` + `fv.modulus`.

**Safety / rollback** The patched binary and keys exist only under `/tmp/ajs-lab` and `lab/pki`.
`rollback.sh purge` deletes captures; `rm -rf lab/pki /tmp/ajs-lab` removes keys and the patched
copy. The tap and mock bind only to `10.75.0.1`.

**Proves** The frame layout, the OAEP-SHA1 session-key path, per-flow TCP behavior, and that a
captured exchange is replayable and diffable. It also proves FV research does not depend on TLS
interception.

### S4 — Request mutation (control plane)

**Goal** Materialize the request-side cases from `mutation-matrix.tsv`, send each to the mock,
and record the mock's parse and the response envelope. Client-side expectations stay in the
matrix until a TLS-rewriting proxy or a hook (S7) can put the mutated request on the wire.

**Setup** S1 running. Baseline request fixture `lab/fixtures/request_login.json`. `mutate
--layer control_request` keeps the 20 request rows and refuses a fixture whose `layer` differs.

**Commands**

```bash
cd "$LAB"
python3 lab/labctl.py mutate --request lab/fixtures/request_login.json \
  --matrix mutation-matrix.tsv --layer control_request --out "$CAPTURES/mutants"
python3 lab/labctl.py mutate --request lab/fixtures/request_login.json \
  --matrix mutation-matrix.tsv --layer control_request --out "$CAPTURES/mutants" --id C01

python3 lab/send_mutant.py --url https://login.mmegg.com/client.php \
  --case lab/fixtures/request_login.json --cacert lab/pki/ca.pem \
  --out "$CAPTURES/responses/baseline.json" \
  --response-out "$CAPTURES/responses/baseline.body.json" --i-understand-lab-only
python3 lab/send_mutant.py --url https://login.mmegg.com/client.php \
  --case "$CAPTURES/mutants/C01.json" --cacert lab/pki/ca.pem \
  --out "$CAPTURES/responses/C01.json" \
  --response-out "$CAPTURES/responses/C01.body.json" --i-understand-lab-only

python3 lab/labctl.py diff --a "$CAPTURES/responses/baseline.body.json" \
  --b "$CAPTURES/responses/C01.body.json"
```

**Observe** For each case: the mock's captured field map (duplicates preserved as a list, so
`dup:username` shows both values), the response envelope, and the matrix's
`expected_client`/`expected_server` columns. `C16` now reaches the wire with two `username`
keys; `C20` sets `trailing_amp` and the sender appends the trailing `&` the Linux client sends.
High-signal rows: `C16` parser precedence, `C04`–`C06` version gates, `C18`/`C19` signature
headers absent on Linux, `C13`/`C14` password handling (mock accepts; the real server is
`[UNKNOWN]`).

**Artifacts** `$CAPTURES/mutants/<ID>.json`, `$CAPTURES/responses/<ID>.json`,
`$CAPTURES/responses/<ID>.body.json`, `$CAPTURES/control/capture.jsonl`.

**Safety / rollback** The dummy account is the only identity; the fixture password is
`lab-secret`; `send_mutant.py` refuses any host outside loopback/`10.75.0.0/24` (verified:
exit 2 for `203.0.113.10`). No mutation is ever sent to a vendor host.

**Proves** How the mock parses each mutated request and which fields the client populates (the
fixture is the baseline). It does **not** prove how the client reacts to sending a mutated
request: that needs a TLS-terminating rewriting proxy with the lab CA or an S7 hook, because the
client builds its own body. The matrix's `expected_client` column stays a hypothesis until then.

### S5 — Response mutation (control plane)

**Goal** Feed the client deliberately wrong envelopes and record how it fails: parse abort,
login failure, or an empty node list — and confirm that an HTTP error alone does **not** trigger
failover (only `IOException` does).

**Setup** S1 fixtures replaced per case; the mock supports `--fixture`, `--status`,
`--encoding`.

**Commands**

```bash
cd "$LAB"
pkill -f 'lab/labctl.py mock-control' 2>/dev/null || true   # replace the S1 mock
# a) AuthResult != OK
python3 lab/labctl.py mock-control --bind 10.75.0.1 --port 443 --port 4443 \
  --cert lab/pki/server.pem --key lab/pki/server.key \
  --fixture lab/fixtures/response_auth_fail.json \
  --capture "$CAPTURES/control_fail" --salt-file lab/pki/salt.bin --i-understand-lab-only &

# b) Servers absent
#    --fixture lab/fixtures/response_no_servers.json
# c) SyncVersion is a number (expect json_value.cpp abort)
#    --fixture lab/fixtures/response_syncversion_number.json
# d) HTTP 500 with a valid body
#    --status 500
# e) gzip-encoded valid body
#    --encoding gzip

$CLIENT -c "$CONF" connect 'Test*' 2>&1 | tee "$CAPTURES/client_response_case.log"
```

**Observe** Per case: whether the client posts at all, whether it logs
`response check failed, buf.size()=…` (body not ending in `}`), whether it aborts inside
`json_value.cpp` (missing `UrlWeb`/`LatestVersion`, non-object `SyncVersion`), and whether it
reaches `state=200`. HTTP 500 with a parseable body must **not** trigger failover: the client
advances only on `IOException`.

**Artifacts** `$CAPTURES/control_*/capture.jsonl`, `$CAPTURES/client_response_case.log`.

**Safety / rollback** Every fixture keeps `svr=10.75.0.1`; a non-lab `svr` is forbidden by the
lab guard (R05). Restart the mock with the good fixture between cases.

**Proves** The minimum viable response schema and the client's failure taxonomy. It is the
regression oracle for the emulator's response builder: if the emulator emits a shape the client
aborts on, this scenario catches it.

### S6 — Protocol fuzzing (FV carrier)

**Goal** Drive malformed frames into the mock FV server and classify the resulting error paths
on both sides.

**Setup** A captured auth frame (`conn-0001.c2s.bin`) and data frame (`conn-0002.c2s.bin`),
`mock-fv` logging to `$CAPTURES/fv.jsonl`.

**Commands**

```bash
cd "$LAB"
mkdir -p "$CAPTURES/fuzz"
FM="python3 lab/labctl.py frame mutate --rsa-key lab/pki/fv.key"

$FM --file "$CAPTURES/tap/conn-0001.c2s.bin" --ops 'set:l=999999' --out "$CAPTURES/fuzz/l_big.bin"
$FM --file "$CAPTURES/tap/conn-0001.c2s.bin" --ops 'set:l=-1'     --out "$CAPTURES/fuzz/l_neg.bin"
$FM --file "$CAPTURES/tap/conn-0001.c2s.bin" --ops 'set:l=+1'     --out "$CAPTURES/fuzz/l_plus1.bin"
$FM --file "$CAPTURES/tap/conn-0001.c2s.bin" --ops 'set:n=ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ' \
    --out "$CAPTURES/fuzz/n_nonhex.bin"
$FM --file "$CAPTURES/tap/conn-0001.c2s.bin" --ops 'set:k=AAAA'   --out "$CAPTURES/fuzz/k_bad.bin"
$FM --file "$CAPTURES/tap/conn-0001.c2s.bin" --ops 'delete:_'     --out "$CAPTURES/fuzz/no_pad.bin"
$FM --file "$CAPTURES/tap/conn-0001.c2s.bin" --ops 'len:_=4096'   --out "$CAPTURES/fuzz/pad_big.bin"
$FM --file "$CAPTURES/tap/conn-0002.c2s.bin" --ops 'set:s=lab-unknown' --out "$CAPTURES/fuzz/sid_bad.bin"

head -c -10 "$CAPTURES/tap/conn-0001.c2s.bin" > "$CAPTURES/fuzz/body_short.bin"
for f in "$CAPTURES"/fuzz/*.bin; do
  python3 lab/labctl.py replay --capture "$f" --target 10.75.0.1:8445 \
    --out "$CAPTURES/fuzz/replies" --i-understand-lab-only
done

# Codec boundary, offline: text_len is a uint32 in the frame but MAX_TEXT caps it at 65535
python3 -c "import sys; sys.path.insert(0,'lab'); import fvproto; \
fvproto.build_frame(b'0'*16, 'x'*65536)"   # expect ValueError
```

**Observe** Taxonomy:

| Mutation | Mock event | Client symptom (expected) |
|---|---|---|
| `l=999999` | `fv_error` timeout in `recv_exact` | connection reset / auth timeout |
| `l=-1` | `fv_auth` with `body_len=0`; `recv_exact` does not validate negatives | none; auth proceeds with an empty body |
| `l=+1` | `fv_auth` with a 1-byte garbage body | none; the handler returns and the rest is discarded |
| `n` non-hex | `fv_error` `ValueError` from `bytes.fromhex` | no auth map; client retries/aborts |
| `k=AAAA` | `fv_error` from base64/OAEP magic check | auth failure |
| `_` deleted | `fv_auth` with no pad | none; padding is advisory |
| `_` = 4096 | `fv_auth`, text grows | none; size limit is the 65535 codec cap |
| `s` unknown | `fv_data` error `unknown sid` → `code=403` | data channel rejected |
| body truncated | `fv_error` short read | client sees a short response |

**Artifacts** `$CAPTURES/fuzz/*.bin`, `$CAPTURES/fuzz/replies/*.s2c.bin`, `$CAPTURES/fv.jsonl`.

**Safety / rollback** Only the mock is targeted; the mock logs and closes, it does not crash the
host. Fuzz files live under `$CAPTURES` and are purged by `rollback.sh purge`.

**Proves** Parser boundaries and which malformed inputs are rejected before any state change.
It also documents that the codec refuses oversized text locally, so that case never reaches the
wire.

### S7 — SDK/API hooking (Frida, `LD_PRELOAD`, API Monitor, strace)

**Goal** Observe or alter in-process calls when TLS interception or frame capture is not enough:
the session key before encryption, the verify callback, the SOCKS5 request builder.

**Setup** Lab copy only. Methods and caveats are taken from `research/74-re-methods/REPORT.md`
(D05/D06/D07/D10, H01/H02, N08). Hook scripts are written per run: no offsets or shims are
shipped because they are build-specific.

**Commands**

```bash
# Frida: spawn with a script, log to file
frida -f "$CLIENT" -l hooks/fv.js --runtime=v8 -o "$CAPTURES/frida.log"
frida-trace -f "$CLIENT" -i 'SSL_*' -i 'X509*' -i 'CertVerify*'

# LD_PRELOAD shim (dynamic builds only)
gcc -shared -fPIC -o /tmp/ajs-lab/shim.so hooks/shim.c -ldl
LD_PRELOAD=/tmp/ajs-lab/shim.so "$CLIENT" -c "$CONF" connect 'Test*'

# Syscall-level, no code injection
strace -f -e trace=network,openat -s 256 -o "$CAPTURES/strace.txt" \
  "$CLIENT" -c "$CONF" connect 'Test*'
```

Windows (if the target is `AJiaSu.exe`): API Monitor → Start → `AJiaSu.exe` → enable
`Networking` + `Cryptography`; Frida needs the matching `frida-server` architecture.

**Observe** Function arguments/returns for the TLS verify path (`SSL_get_verify_result`,
`X509_verify_cert`), the RSA/OAEP call, and the SOCKS5 request builder. A static mbedTLS build
hides symbols: `frida-trace -i 'SSL_*'` and `LD_PRELOAD` find nothing, so hook by module base +
offset instead. ptrace-based tracing is visible to anti-debug checks and slows I/O.

**Artifacts** `$CAPTURES/frida.log`, `$CAPTURES/strace.txt`, and the hook scripts used for the
run (kept with the capture bundle, not in the repository).

**Safety / rollback** Attach only to the lab copy; never to an authenticated production process.
`frida-kill -n ajiasu` and remove `shim.so` afterwards. Hooks can change behavior; re-run S3
afterward to confirm the binary still matches the captured wire format.

**Proves** In-process truth when the wire is opaque, and the decision rule: MITM for control
HTTP, frame codec for FV, hooks only when both are blocked. It also records the failure mode
(static build) so the operator does not burn an hour on `LD_PRELOAD`.

### S8 — Panel integration: "request research" mode

**Goal** Turn the lab into a repeatable panel workflow: record a session against a mock, replay
it, mutate it, and produce a normalized diff as a regression signal.

**Setup** Panel/SDK context from `research/70-panel/REPORT.md` (`ajs-accountd`, `ajsproxy`,
Resin) and `research/76-sdk-integration/INTERFACE.md` (codecs P1–P8, sentinel errors,
`Classifier`). No panel code is modified by this slice; the contract below is the deliverable.

**Commands** (composition the panel performs; each step already exists)

```bash
# 1. record: mock control + mock FV + tap, then one client session (S1 + S3)
# 2. replay: captured client bytes against a fresh mock
python3 lab/labctl.py replay --capture "$CAPTURES/tap" --target 10.75.0.1:8445 \
  --out "$CAPTURES/replay" --i-understand-lab-only
# 3. mutate: one case per request field (control layer only)
python3 lab/labctl.py mutate --request lab/fixtures/request_login.json \
  --matrix mutation-matrix.tsv --layer control_request --out "$CAPTURES/mutants" --id C04
# 4. send + diff: non-empty diff is the regression signal
python3 lab/send_mutant.py --url https://login.mmegg.com/client.php \
  --case "$CAPTURES/mutants/C04.json" --cacert lab/pki/ca.pem \
  --out "$CAPTURES/responses/C04.json" \
  --response-out "$CAPTURES/responses/C04.body.json" --i-understand-lab-only
python3 lab/labctl.py diff --a "$CAPTURES/responses/baseline.body.json" \
  --b "$CAPTURES/responses/C04.body.json"     # exit 1 when changes exist
```

**Observe** A session bundle: `manifest.json` (mode, target, allowlist decision, case ids,
versions), `capture.jsonl` (redacted), `responses/*.json`, `diff.json`. The panel shows the diff
paths; exit code 1 means "behavior changed", 0 means "no change under the volatile filter".

**Artifacts** One bundle directory per research run, under the panel's own state dir; raw
`sealed.jsonl` never enters the bundle.

**Safety / rollback** Research mode is off by default, requires an explicit opt-in per run, and
is loopback-only. The panel must refuse a non-lab target before spawning anything. Bundles are
redacted at write time; deleting a bundle is one directory removal. The release build must not
compile the mode in.

**Proves** The lab is not just a pile of scripts: it defines a closed loop from capture to
regression test, with the same diff semantics the emulator/SDK can assert on
(`ErrParse` / `*FVReject{Fields}` classification remains the production path).

### S9 — Data channel and SOCKS5 refusal paths

**Goal** Exercise the per-flow data connection, `sid` binding, and the refusal path when the
session id is unknown.

**Setup** S3 running. `mock-fv --permissive` reuses the last auth session key for an unknown
`sid`; without it, an unknown `sid` returns `code=403`.

**Commands**

```bash
cd "$LAB"
pkill -f 'lab/labctl.py mock-fv' 2>/dev/null || true   # replace the S3 mock
python3 lab/labctl.py mock-fv --bind 10.75.0.1 --port 8445 --rsa-key lab/pki/fv.key \
  --permissive --capture-log "$CAPTURES/fv_permissive.jsonl" --i-understand-lab-only &
# 1. re-establish a session key in the fresh mock (auth replay sets last_session_key)
python3 lab/labctl.py replay --capture "$CAPTURES/tap/conn-0001.c2s.bin" \
  --target 10.75.0.1:8445 --out "$CAPTURES/replay_auth" --i-understand-lab-only
# 2. the data frame still carries the old sid; --permissive maps it to the last key
python3 lab/labctl.py replay --capture "$CAPTURES/tap/conn-0002.c2s.bin" \
  --target 10.75.0.1:8445 --out "$CAPTURES/replay_data" --i-understand-lab-only
python3 lab/labctl.py frame parse --file "$CAPTURES/tap/conn-0002.c2s.bin" \
  --rsa-key lab/pki/fv.key --show-secrets
```

**Observe** `s=<sid>` from the auth response, `k=` empty, **no `l=`**, body = encrypted SOCKS5
CONNECT. With `--permissive`, the mock logs `permissive-sid` and answers `code=200`; without it,
an unknown `sid` returns `code=403` / `r=auth.session`. To see the refusal, restart the mock
without `--permissive` and replay only the data frame — it must be rejected.

**Artifacts** `$CAPTURES/fv_permissive.jsonl`, `$CAPTURES/replay_data/*.s2c.bin`.

**Safety / rollback** Same as S3; `--permissive` is a deliberate weakening of the mock and must
never be enabled for a "realism" run.

**Proves** `sid` is the session binding, the data path reuses the auth session key, and the
server has a defined refusal path. It also shows why the emulator must always send `sid`.

### S10 — Half-close, keepalive, and timing

**Goal** Reproduce the auth-map half-close requirement and the `ka` field's effect on the
client's state machine.

**Setup** S3 running; then restart `mock-fv` with `--no-halfclose` and with `--ka 0`.

**Commands**

```bash
cd "$LAB"
pkill -f 'lab/labctl.py mock-fv' 2>/dev/null || true   # replace the S3/S9 mock
python3 lab/labctl.py mock-fv --bind 10.75.0.1 --port 8445 --rsa-key lab/pki/fv.key \
  --no-halfclose --ka 0 --capture-log "$CAPTURES/fv_nohalf.jsonl" --i-understand-lab-only &
timeout 15 "$PATCHED" -c "$CONF" connect 'Test*' > /tmp/ajs-lab/client_s10.log 2>&1 &
CLIENT_PID=$!
sleep 5
ss -lntp | grep 1080 || echo 'client did not reach state=200'
wait "$CLIENT_PID"
```

Then repeat with the default half-close (restart the mock without `--no-halfclose`) and expect
`127.0.0.1:1080` within a few seconds. Compare the two runs.

**Observe** With `--no-halfclose`, the client parses the auth map only on `BEV_EVENT_EOF` and
never reaches `state=200` / `127.0.0.1:1080`; the `timeout` kills it after 15 s. With the default
half-close, the listener appears. With `--ka 0`, the mock echoes `ka=0` into the auth map; the
client's timer behavior is recorded rather than asserted.

**Artifacts** `$CAPTURES/fv_nohalf.jsonl`, client stderr log, `ss` output.

**Safety / rollback** The scenario only changes mock flags; restart with the default half-close
afterward. No host state changes.

**Proves** The half-close is a protocol requirement, not a nicety; `ka` is server-advertised and
must have a sane default. It gives the emulator a concrete regression test for both.

## Guardrails

### Opt-in and destination allowlist

- Every network subcommand requires `AJS_LAB=1` **and** `--i-understand-lab-only`; otherwise it
  exits 2 with `REFUSED: set AJS_LAB=1 and pass --i-understand-lab-only`. Verified 2026-10-23.
- Every peer must resolve into `127.0.0.0/8` or `10.75.0.0/24` (`assert_lab_host`). Verified
  refusal: `REFUSED: 203.0.113.10 resolves to 203.0.113.10, outside loopback/10.75.0.0/24`
  (exit 2, 2026-10-23).
- The allowlist is enforced on **resolved addresses**, not hostnames, so a hosts-file redirect
  cannot silently point at a vendor IP. It does not defend against a deliberate `--target` of a
  loopback proxy that forwards upstream; the operator contract covers that.

### Credentials and redaction

| Class | Keys | Treatment |
|---|---|---|
| Secret | `password`, `pass`, `p`, `connectpassword`, `logintoken`, `_client_login`, `token`, `authorization`, `cookie`, `secret`, `ps` | `<redacted>` |
| Identity | `_uid`, `_os_did`, `cuid`, `cseid`, `lid`, `dim`, `sid`, `n`, `k`, `session_key`, `username`, `u` | `<hmac:12>` with the run salt |
| Volatile | `t`, `ts`, `timestamp`, `_`, `nonce`, `request_id`, `trace_id`, `date` | `<volatile>` |
| Other | everything else | verbatim |

`labctl` JSON captures use the salted hmac above. `fv.jsonl` is written by `fvproto`, which has
no run salt: it redacts `k`, `n`, `_`, `p`, `u`, `s`, the body identity fields (`cuid`, `cseid`,
`lid`, `dim`, `peip`), and the session id to `<redacted:len>` / `<sid:len>` instead. Neither
file may leave the lab VM without the redaction step.

Rules:

1. Only dummy identities (`test`/`test`, `labuser`/`lab-secret`) are allowed. A real account is
   out of scope even if the operator has one.
2. `sealed.jsonl` is written `0600`, contains the raw request body, and is the **only**
   unredacted file. It never leaves the lab VM and is deleted by `rollback.sh purge`.
3. `lab/pki/` holds the CA private key, the lab RSA key, and the redaction salt; all are
   gitignored and removed with `rm -rf lab/pki` after the run.
4. A capture is exportable only after `labctl.py redact` has been run on it, and the exported
   file must be the redacted one.

### Retention

| Artifact | Retention | Deletion |
|---|---|---|
| Raw captures (`*.bin`, `sealed.jsonl`, `fv.jsonl`) | 72 h | `rollback.sh purge` |
| Redacted JSONL / response records | 30 d | manual, after the report is merged |
| Lab keys and CA | until end of run | `rm -rf lab/pki` |
| Report and matrix | repository lifetime | — |

### Never do this

1. Never use a real account, real password, or a third party's credentials.
2. Never send a captured or mutated request to a vendor host, including "just to compare".
3. Never bypass rate limits, concurrency limits, or paywalls.
4. Never distribute the patched binary, the lab CA, or the lab RSA key.
5. Never attach Frida/API Monitor to an authenticated production process.
6. Never publish a capture that still contains a session key, `k=`, `sid`, or password.
7. Never point the client at a non-lab `svr` from a response fixture (R05).
8. Never enable `--permissive` on a run whose result will be quoted as server behavior.
9. Never treat a mock response as evidence about vendor policy; it is evidence about the client.
10. Never leave `/etc/hosts`, the trust store, or a listener in place after the run.

### Rollback

`lab/rollback.sh` is idempotent and never contacts a vendor host:

```bash
lab/rollback.sh check     # verify: hosts unchanged, CA absent, netns absent, ports free
lab/rollback.sh restore   # stop lab processes, delete netns, restore hosts, remove CA
lab/rollback.sh purge     # restore + delete /var/lib/ajs-lab/captures
```

Defaults: `LAB_ROOT=/run/ajs-lab`, `BACKUP=/var/backups/ajs-lab`,
`CAPTURES=/var/lib/ajs-lab/captures`, `CA_CRT=/usr/local/share/ca-certificates/ajs-lab-ca.crt`,
`NETNS=ajs-lab`, ports `443 4443 8443 8444`. `check` prints
`OK rollback=1 hosts=clean ca=absent netns=absent ports=free` on success and exits non-zero
otherwise. The lab keys under `lab/pki/` and the patched copy under `/tmp/ajs-lab/` are removed
by `rm -rf lab/pki /tmp/ajs-lab` after `purge`.

Failure handling: if `check` reports anything after `restore`, stop; do not start another
scenario until the host is clean. If a mock fails to bind, check the port with `ss -lntp` before
changing the topology — never fall back to a vendor host.

### Guardrail verification

- `labctl.py selftest` asserts: frame round-trip, OAEP-SHA1, truncated-frame rejection, mock FV
  auth + data channel, mock control HTTPS, password redaction, redaction/hmac, diff
  normalization, mutation operators, and allowlist rejection of `203.0.113.10` — 11/11 true.
- `send_mutant.py` opt-in and allowlist refusals were exercised on 2026-10-23 (two runs, exit 2
  each).
- `validate.py` materializes all 58 matrix rows offline, asserts that `C16` keeps two `username`
  keys and `C20` carries `trailing_amp`, checks the `--layer control_request` subset is 20
  cases, and asserts that a layer/fixture mismatch is refused. It also re-runs the frame
  round-trip, truncation, oversized-text, redaction, and allowlist checks.

## Integration with the panel

The panel/SDK context is `ajs-accountd` + `ajsproxy` + Resin (`research/70-panel/REPORT.md`),
with codecs and sentinel errors in `research/76-sdk-integration/INTERFACE.md`. Research mode is
a **build-time optional, loopback-only** path in `ajs-accountd`; it never ships enabled.

Contract:

| Field | Value |
|---|---|
| Endpoint | `POST /v1/research/sessions` (loopback + admin bearer), `[DESIGN]` |
| Request | `{"mode":"record\|replay\|mutate\|diff","target":"mock","case_ids":["C04"],"opt_in":true}` |
| Refusal | non-`mock` target, missing `opt_in`, or a resolved address outside the allowlist → `409` |
| Bundle | `manifest.json`, `capture.jsonl` (redacted), `responses/*.json`, `diff.json` |
| Diff signal | `diff.json.changes` non-empty → run marked `REGRESSION`; empty → `MATCH` |
| Secrets | never in the bundle; `sealed.jsonl` stays in the lab VM |
| Cleanup | one directory removal; `rollback.sh purge` for the lab side |

Flow:

```
panel "research" tab
  → ajs-accountd /v1/research/sessions (opt-in, mock only)
      → labctl mock-control + mock-fv + tap        (record)
      → labctl replay                              (replay captured bytes)
      → labctl mutate + send_mutant                (per-case requests)
      → labctl diff                                (normalized comparison)
  → bundle + verdict (MATCH / REGRESSION)
  → optional: promote a case to a codec regression fixture for emulator/internal/fv
```

Why this shape:

- The diff is the same normalized comparison used for response records, so a case promoted to a
  regression fixture keeps its id and expectation text.
- The SDK already classifies failures with sentinels (`ErrAuthRejected`, `ErrFVReject`,
  `ErrParse`, …). A research run that produces `AuthResult=FAIL` must map to
  `ErrAuthRejected`; a malformed frame to `ErrParse`/`*FVReject`. The bundle is therefore a
  classifier conformance test, not just a log.
- Resin and the production data plane are untouched; research mode is a separate process tree
  with no route to a vendor host.

What the panel must never do: auto-start research mode, accept a vendor hostname as target,
store a password in the bundle, or replay a captured live session. The mode exists to test the
client and the emulator, not to operate an account.

## Open questions

1. Does the Windows 4.9.6.0 client verify TLS against the system store, or pin? No capture exists
   for its control plane; S2 answers this only for Linux 4.2.3.0.
2. Is `X-Sig` / `X-Timestamp` still used by 4.9.6.0, and with which secret? The Linux 4.2.3.0
   login path sends neither (`research/60-dynamic/REPORT.md`), so the signing path is unexercised.
3. Does any post-auth packet (keepalive, relayed data) use the OPV2 builder? AJS-07 saw none on
   the proxy path; `76-sdk-integration` lists `OPV2Frame` as lab-only.
4. Can one `sid` serve multiple concurrent data connections, and for how long? S9 can test
   sequentially; concurrency was not exercised.
5. Are `ka` / `kat` / `kapf` enforced by the client, or advisory? S10 exercises the timer path
   but not a long-lived idle connection.
6. May the server choose a response key other than the request `K`? The mock always mirrors `K`;
   no evidence either way.
7. Where does the panel code live for the research tab (70-panel is a design, 76 is an
   interface)? This slice defines the contract only.

## Self-check

**A — Early artifact.** The first write landed `REPORT.md` with all 10 scenario headings, the
seven required fields, the topology, and Guardrails; `LAB.md` and `validate.py` followed. The
first validator run failed on a wrong truncation assumption, and the first full `mutate` run
failed on two prose rows (`C20`, `F15`); both forced code changes (text-cut check, pair/body-op
split). The report was then corrected against the code (S3 patched path, S4 layer filter, S6
taxonomy, S9 session ordering).

**B — Falsification of the strongest claims.**

| Claim | Verdict | Basis |
|---|---|---|
| MITM of the control plane is feasible | **CONFIRMED (Linux 4.2.3.0)** / **UNKNOWN (Windows)** | AJS-07 login succeeded through a lab CA in the system store; OpenAJS source is trust-all. No test of the shipping client against an untrusted CA yet — that is S2's job. |
| The RSA modulus patch yields decryption | **CONFIRMED** | AJS-07: 309-digit modulus replaced at `0x28a030`; OAEP-SHA1 decrypts the 16-byte session key, OAEP-SHA256 fails. |
| A mock can drive the client to `state=200` | **CONFIRMED** | AJS-07 reached `FVClient state=200` and listened on `127.0.0.1:1080` with the mock control plane and mock FV server. |
| The guardrails are sufficient | **WEAKENED** | The allowlist checks resolved addresses, not intent; a loopback forwarder defeats it. `sealed.jsonl` contains plaintext by design. The Windows path is untested. Rollback is untested at runtime here (WSL stopped). |
| Replay reproduces the exchange | **WEAKENED** | Replay is byte-exact only for the request; the mock issues a fresh `sid`, so a replayed *data* frame that references an old `sid` needs `--permissive` or a fresh auth first. |

**C — Verification.** `python D:/Project/LovePlusSpeedToProxy/research/75-request-mutation/validate.py`
was run; the output is pasted in the final message. `lab/labctl.py selftest` was run on
2026-10-23: 11/11 checks true.

**D — Summary (≤14 lines).**

- Established: frame format and replay path; lab RSA/OAEP-SHA1 decryption; mock control + mock
  FV can drive the client; 58-case mutation matrix; redaction and diff are implemented and
  self-tested; opt-in and allowlist refusals verified.
- Overturned/weakened: "guardrails are sufficient" (address-based allowlist, plaintext sealed
  file); "replay reproduces the exchange" (fresh `sid` on the server side).
- Unknown: Windows TLS/pinning, `X-Sig` on 4.9.6.0, post-auth OPV2, `sid` concurrency, `ka`
  enforcement.
- Next: run S1–S3 in WSL when it is restarted; then S4/S5 to fill the matrix's `expected_*`
  columns with observations instead of predictions.
- What I may still be betting wrong: that the Linux 4.2.3.0 behavior generalizes to 4.9.6.0;
  the report marks that as `[UNKNOWN]`, not as fact.
