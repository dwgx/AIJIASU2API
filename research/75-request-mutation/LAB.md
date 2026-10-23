# LAB — runnable procedures for S1–S3

Status: 2026-10-23. The commands below were **not executed at runtime** in this session because
WSL is stopped per Owner. Everything that can run offline was run: `labctl.py selftest` →
11/11 checks true (Python 3.14.7, cryptography 48.0.1). Runtime steps are marked `[UNVERIFIED]`.

This file covers the first three scenarios only:

1. **S1** — mock control plane (`/client.php`) with capture and replay.
2. **S2** — TLS interception, key logging, pinning detection.
3. **S3** — FV carrier capture, decrypt, replay.

Read `REPORT.md` for the other seven scenarios and for the guardrail rationale.

---

## 0. Prerequisites

### 0.1 Host and packages

Lab VM: WSL2 Ubuntu 24.04, root shell (AJS-07 used the same). Packages:

```bash
apt-get install -y python3 python3-cryptography openssl iproute2 curl psmisc rsync
python3 -c "import cryptography; print(cryptography.__version__)"   # >= 41
```

`mitmproxy`/Burp are **not** required. If they are absent, do not install them for this lab;
`labctl.py` covers the same ground with the lab CA.

### 0.2 Lab checkout

Copy the slice out of the Windows tree so captures and keys never land in the repository:

```bash
mkdir -p /opt/ajs-lab
rsync -a --exclude lab/pki --exclude __pycache__ --exclude captures \
  /mnt/d/Project/LovePlusSpeedToProxy/research/75-request-mutation/ /opt/ajs-lab/
cd /opt/ajs-lab
export LAB=/opt/ajs-lab
export CAPTURES=/var/lib/ajs-lab/captures
export AJS_LAB=1
mkdir -p "$CAPTURES" /var/backups/ajs-lab /run/ajs-lab
```

### 0.3 Client copy and config

```bash
mkdir -p /tmp/ajs-lab
install -m 0755 /path/to/ajiasu-4.2.3.0 /tmp/ajs-lab/ajiasu
export CLIENT=/tmp/ajs-lab/ajiasu
export PATCHED=/tmp/ajs-lab/ajiasu-patched
export CONF=/tmp/ajs-lab/ajs.conf
```

The config is the AJS-07 lab config (dummy account `test` / `test`); its exact contents are
`[UNVERIFIED]` here. The invocation that matters is the one AJS-07 used:

```bash
$CLIENT -c "$CONF" connect 'Test*'
```

### 0.4 Baseline self-test

```bash
cd "$LAB"
python3 lab/labctl.py selftest
```

Expected (actual output from 2026-10-23):

```json
{
  "ok": true,
  "checks": {
    "frame_roundtrip": true,
    "rsa_oaep_sha1": true,
    "frame_length_mismatch_detected": true,
    "mock_fv_auth": true,
    "mock_fv_data": true,
    "mock_control_https": true,
    "capture_redacts_password": true,
    "redaction": true,
    "diff_normalization": true,
    "mutation_apply": true,
    "allowlist_rejects_external": true
  }
}
```

If any check is false, stop. Do not debug a network scenario on top of a broken codec.

---

## 1. S1 — Lab network, CA, mock control plane

### 1.1 Lab address

```bash
ip link add ajs-lab type dummy 2>/dev/null || true
ip addr add 10.75.0.1/24 dev ajs-lab 2>/dev/null || true
ip link set ajs-lab up
ip -brief addr show ajs-lab
```

Expected: `ajs-lab UP 10.75.0.1/24`. Do **not** add a default route on this interface.

### 1.2 Lab CA and leaf

```bash
cd "$LAB"
python3 lab/labctl.py ca --dir lab/pki \
  --host login.ajs-app.com --host login.mmegg.com --host login.mbclubsh.com \
  --ip 10.75.0.1 --days 2
```

Expected JSON: `{"ok": true, "dir": "lab/pki", "sans": ["login.ajs-app.com", …]}`.
Files: `lab/pki/ca.pem`, `ca.key`, `server.pem`, `server.key` (keys `0600`).

Trust the CA:

```bash
install -m 0644 lab/pki/ca.pem /usr/local/share/ca-certificates/ajs-lab-ca.crt
update-ca-certificates
```

### 1.3 Hosts redirect (backup first)

```bash
cp -n /etc/hosts /var/backups/ajs-lab/hosts.bak
printf '10.75.0.1 login.ajs-app.com login.mmegg.com login.mbclubsh.com\n' >> /etc/hosts
getent hosts login.mmegg.com        # expect 10.75.0.1
```

`rollback.sh restore` restores `/var/backups/ajs-lab/hosts.bak`; keep the path.

### 1.4 Start the mock control plane

```bash
cd "$LAB"
head -c 32 /dev/urandom > lab/pki/salt.bin && chmod 600 lab/pki/salt.bin
python3 lab/labctl.py mock-control --bind 10.75.0.1 --port 443 --port 4443 \
  --cert lab/pki/server.pem --key lab/pki/server.key \
  --fixture lab/fixtures/login_ok.json \
  --capture "$CAPTURES/control" --salt-file lab/pki/salt.bin \
  --i-understand-lab-only &
```

Expected: `{"ok": true, "bind": "10.75.0.1", "ports": [443, 4443], …}`.

If you are not root, start the mock with `--port 4443` only: the client's third host is
`https://login.mmegg.com:4443`, and the first two fail with `IOException`, which is exactly the
failover condition. `[UNVERIFIED]`

### 1.5 Drive the client

```bash
$CLIENT -c "$CONF" connect 'Test*' 2>&1 | tee /tmp/ajs-lab/client_s1.log
```

Expected: the client posts `cmd=ClientApiLogin/Login`, receives the fixture, and proceeds to the
FV stage (which fails until S3 is running — that is fine for S1).

### 1.6 Inspect the capture

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

Checkpoints:

| Check | Expected |
|---|---|
| `layer` | `control_request` |
| `path` | `/client.php` |
| `fields` | contains `cmd`, `username`, `password`, `_uid`, `_m`, `v` |
| `password` | `<redacted>` in `capture.jsonl` |
| `_uid` | `<hmac:…>` |
| `sealed.jsonl` mode | `600` |
| `tls` | `{"version": "TLSv1.3" or "TLSv1.2", "cipher": "…"}` |

If `capture.jsonl` is empty, the client never posted: check the hosts entry, the CA trust, and
`ss -lntp | grep -E ':(443|4443)'`.

---

## 2. S2 — TLS interception, key log, pinning probe

### 2.1 Trusted-CA handshake

```bash
openssl s_client -connect 10.75.0.1:443 -servername login.mmegg.com \
  -CAfile lab/pki/ca.pem -brief </dev/null | tee "$CAPTURES/tls_probe.txt"
```

Expected: `Verification: OK`, protocol `TLSv1.2` or `TLSv1.3`. Record it; this is the baseline.

### 2.2 Untrusted-CA handshake (the pinning/verification probe)

```bash
openssl s_client -connect 10.75.0.1:443 -servername login.mmegg.com \
  -no-CAfile -no-CApath -brief </dev/null 2>&1 | tail -5
```

The lab CA is in the system bundle after 1.2, so `-CAfile` would verify successfully.
`-no-CAfile -no-CApath` forces an untrusted chain; a verify error is the expected result for a
client that checks certificates. The client-level probe is 2.4.

### 2.3 Key log

```bash
SSLKEYLOGFILE=/tmp/ajs-lab/keys.log $CLIENT -c "$CONF" connect 'Test*'
wc -l /tmp/ajs-lab/keys.log 2>/dev/null || echo 'no keylog lines: fall back to S7'
```

If the file is absent or empty, the client's TLS stack does not honor `SSLKEYLOGFILE` (common
for static BoringSSL/mbedTLS builds). Do not chase it; use S7 hooks or S3 (FV is not TLS).

### 2.4 Client-level pinning probe

```bash
cd "$LAB"
python3 lab/labctl.py ca --dir lab/pki/rogue --host login.mmegg.com --ip 10.75.0.1 --days 1
pkill -f 'lab/labctl.py mock-control' 2>/dev/null || true   # stop the S1 mock
rm -f /usr/local/share/ca-certificates/ajs-lab-ca.crt && update-ca-certificates --fresh
python3 lab/labctl.py mock-control --bind 10.75.0.1 --port 443 --port 4443 \
  --cert lab/pki/rogue/server.pem --key lab/pki/rogue/server.key \
  --fixture lab/fixtures/login_ok.json \
  --capture "$CAPTURES/control_rogue" --salt-file lab/pki/salt.bin \
  --i-understand-lab-only &
$CLIENT -c "$CONF" connect 'Test*' 2>&1 | tee /tmp/ajs-lab/client_s2_rogue.log
```

Interpretation:

| Observation | Meaning |
|---|---|
| Client aborts before POST, TLS error in log | Store-based verification (or pinning); MITM needs a trusted CA |
| Client posts anyway | Trust-all on this path; pinning absent |
| Client aborts only with the rogue leaf but posts with the lab CA | Store-based verification, no vendor pin |

Restore the lab CA and the S1 mock before continuing:

```bash
install -m 0644 lab/pki/ca.pem /usr/local/share/ca-certificates/ajs-lab-ca.crt
update-ca-certificates
# restart the S1 mock exactly as in 1.4 before the next scenario
```

**Do not** attempt any pinning bypass against a live host. If pinning is enforced, the FV path
(S3) is still open because it is not TLS, and S7 is the in-process fallback.

---

## 3. S3 — FV carrier: keygen, patch, capture, replay

### 3.1 Lab RSA key and patched client copy

```bash
cd "$LAB"
python3 lab/labctl.py keygen --out lab/pki --bits 1024
# {"ok": true, "bits": 1024, "digits": 309, "key": "lab/pki/fv.key", "modulus": "lab/pki/fv.modulus"}

cp "$CLIENT" "$PATCHED"
python3 lab/labctl.py patch-modulus --binary "$PATCHED" \
  --modulus-file lab/pki/fv.modulus --out "$PATCHED"
```

Expected: `"offset": "0x28a030"` for the 4.2.3.0 Linux build (`patch-modulus` searches the
vendor modulus when `--offset` is omitted), `"hits": 1`, distinct
`sha256_before`/`sha256_after`. The modulus must be 309 decimal digits; `keygen` retries until
it gets one and `patch-modulus` refuses any other length.

Sanity check that the patched copy still runs:

```bash
"$PATCHED" -h | head -5
```

### 3.2 Start mock FV behind the tap

Topology: client → `10.75.0.1:8444` (tap) → `10.75.0.1:8445` (mock-fv). The fixture's
`Servers.lab-node.svr=10.75.0.1` and `par.fv.p=8444` make the client use the tap.

```bash
cd "$LAB"
python3 lab/labctl.py mock-fv --bind 10.75.0.1 --port 8445 \
  --rsa-key lab/pki/fv.key --capture-log "$CAPTURES/fv.jsonl" \
  --i-understand-lab-only &
python3 lab/labctl.py tap --listen 10.75.0.1:8444 --upstream 10.75.0.1:8445 \
  --out "$CAPTURES/tap" --i-understand-lab-only &
```

Expected: two `{"ok": true, …}` lines. `ss -lntp | grep -E ':(8444|8445)'` shows both.

### 3.3 One proxied flow

```bash
$PATCHED -c "$CONF" connect 'Test*' 2>&1 | tee /tmp/ajs-lab/client_s3.log &
sleep 3
ss -lntp | grep 1080        # expect the client listening on 127.0.0.1:1080
curl -sS --max-time 5 --socks5-hostname 127.0.0.1:1080 http://example.com/ \
  -o /dev/null -w '%{http_code}\n' || echo 'expected: mock has no relay, timeout is fine'
```

Reference values from AJS-07 (exact sizes vary with the device strings):

| Connection | Size | text_len | body |
|---|---|---|---|
| auth (`conn-0001.c2s.bin`) | 679 B | 234 | 425 B |
| data (`conn-0002.c2s.bin`) | 207 B | 169 | 18 B |

### 3.4 Parse and decrypt

```bash
cd "$LAB"
python3 lab/labctl.py frame parse --file "$CAPTURES/tap/conn-0001.c2s.bin" \
  --rsa-key lab/pki/fv.key --show-secrets | tee "$CAPTURES/auth_parsed.json"
python3 lab/labctl.py frame parse --file "$CAPTURES/tap/conn-0002.c2s.bin" \
  --rsa-key lab/pki/fv.key --show-secrets | tee "$CAPTURES/data_parsed.json"
```

Checkpoints:

| Check | Expected |
|---|---|
| `fields.s` (auth) | empty |
| `fields.n` | 32 lowercase hex |
| `fields.k` | base64 starting with `OAEP` after decode |
| `fields.l` | equals `body_len` |
| `session_key` | 32 hex characters |
| auth `body_plain` | `c=au`, `u=test`, `v=4.2.3.0`, … |
| data `fields.k` | empty |
| data `fields.l` | absent |
| data `body_plain` | SOCKS5 CONNECT, starts `05 01 00` |

### 3.5 Replay

```bash
cd "$LAB"
python3 lab/labctl.py replay --capture "$CAPTURES/tap" \
  --target 10.75.0.1:8445 --out "$CAPTURES/replay" --i-understand-lab-only
```

Expected: one `{"sent": "conn-0001.c2s.bin", "reply_bytes": >0, …}` per `*.c2s.bin`. The mock
issues a fresh `sid`; the old `k=` still decrypts because the lab key is unchanged. A replayed
**data** frame that carries an old `sid` is refused (`code=403`) unless `mock-fv --permissive` is
used — that is S9, not a failure of replay.

---

## 4. Teardown and verification

```bash
cd "$LAB"
pkill -f 'lab/labctl.py' 2>/dev/null || true
lab/rollback.sh restore
lab/rollback.sh check
```

Expected final line: `OK rollback=1 hosts=clean ca=absent netns=absent ports=free`.

Then remove lab secrets and the patched copy:

```bash
rm -rf lab/pki /tmp/ajs-lab
```

If the report or a bundle needs captures, run `lab/rollback.sh purge` instead of leaving
`/var/lib/ajs-lab/captures` behind. `purge` also calls `restore`.

---

## 5. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `REFUSED: set AJS_LAB=1 …` | env or flag missing | `export AJS_LAB=1`, pass `--i-understand-lab-only` |
| `REFUSED: … outside loopback/10.75.0.0/24` | hosts not redirected, or target is a vendor IP | fix `/etc/hosts`; never override the guard |
| `OSError: [Errno 98] Address already in use` | leftover mock/tap | `ss -lntp`, kill the pid, rerun |
| Client never posts | CA not trusted, hosts wrong, port not bound | `getent hosts`, `update-ca-certificates`, `ss -lntp` |
| `response check failed, buf.size()=…` | fixture body does not end in `}` | use the fixtures in `lab/fixtures/` |
| `json_value.cpp: asCString` abort | fixture missing `UrlWeb`/`LatestVersion` | restore `login_ok.json` |
| `json_value.cpp: getMemberNames` abort | `SyncVersion` is not an object | use `{}` |
| No FV connection | fixture `svr`/`par.fv.p` wrong, or tap upstream down | check fixture and `ss` |
| `declares N text bytes` on parse | wrong key, or the tap captured a partial frame | re-capture; ensure the client finished the handshake |
| OAEP decrypt error | patched copy not used, or modulus mismatch | re-run `patch-modulus`, confirm `hits=1` |
| `frame mutate` says `--body-text needs --session-key-hex` | only needed when rewriting the plaintext body | pass the key from `frame parse --show-secrets` |

## 6. What this lab does not do

- It does not contact a vendor host, use a real account, or bypass a rate limit.
- It does not prove vendor-side behavior: every observation is about the **client** and the
  **mock**. The matrix's `expected_server` column is a hypothesis until a server-side test
  exists.
- It does not persist keys or captures in the repository. `lab/pki/` and `captures/` are
  gitignored; the report is the only durable artifact.
