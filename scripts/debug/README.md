# scripts/debug — capture, slice, and debug the account path

Status: loopback dry-run verified on HMC (Debian 13, 2026-10-23). No TCP connection was
made to a vendor host (the control-filter dry run resolved names via DNS only), no account
or credential was used, and no driver or packet-capture library was installed.
`research/80-debugger-prep/PLAYBOOK.md` is the full runbook; this file is the operator
manual for the three scripts.

| File | Purpose |
|---|---|
| `capture.sh` | Rotating `tcpdump` capture driven by a BPF filter file; compile-checks the filter, hashes the output, supports `--stop`. |
| `slice_pcap.py` | Deterministic pcap slicing by time, host, port, protocol, or TLS SNI. Stdlib-only `native` backend; optional `tshark`/`editcap` backends. |
| `run-dlv.sh` | Builds the daemon or the account test binary with `-gcflags=all=-N -l` and starts Delve with a safe breakpoint set. |
| `filters/control.bpf` | The four control-API hosts on 443/4443. |
| `filters/fv.bpf.example` | Template for the dynamic FV endpoint (`svr` + `par.fv.p`). |
| `filters/loopback.bpf` | Dry-run-only filter for local ports 18080/18443. |

All three scripts use `set -euo pipefail`, refuse credential-looking input where they can,
and never put a secret on a command line.

## HMC layout

The dry run used this tree (created by the operator, not committed):

```
/srv/dwgxhomecloud/scratch/ajs-51-debugger/
  src/        daemon/ and emulator/ copied from the repo
  out/        debug binaries, dlv, init files, certificates
  cap/        pcaps and metadata (mode 700)
  bin/dlv     pinned Delve 1.27.2
  scripts/debug/  this directory
```

`capture.sh` defaults `LAB_ROOT` to that path; override with `AJS_LAB_ROOT`. `run-dlv.sh`
uses `AJS_LAB_SRC` and `AJS_LAB_OUT` for the source and output directories.

## Requirements and install policy

| Tool | HMC status (2026-10-23) | Policy |
|---|---|---|
| `tcpdump` 4.99.5 / libpcap 1.10.5 | present | use; needs root or `CAP_NET_RAW` |
| `gdb` 16.3, `strace` 6.13, `ss`, `lsof` 4.99.4 | present | use; attach only to our own process |
| `dlv` 1.27.2 | installed into `bin/` with `GOBIN` | lab-only, pinned, removable |
| `go` 1.25.14 + auto toolchain 1.26.0 | present | build with `GOTOOLCHAIN=auto` |
| `tshark`, `dumpcap`, `editcap`, `capinfos` | **absent** | do not apt-install without the Owner; `native` backend covers the dry run |
| `mitmproxy` / `mitmdump` | **absent** | not needed; do not MITM the vendor |
| Chromium 155.0.8059.79 | present | headless screenshots; manual DevTools HAR |
| Playwright | **absent** | do not `npm i` without the Owner; use Chromium DevTools |
| `python3` 3.13.5, `jq` 1.7, `openssl` 3.5.7, `curl` 8.14.1, `node` 24.21.0 | present | use |

Install `dlv` only into the lab tree, never system-wide:

```sh
GOBIN=/srv/dwgxhomecloud/scratch/ajs-51-debugger/bin \
  go install github.com/go-delve/delve/cmd/dlv@v1.27.2
```

Delve must be at least 1.27 for a Go 1.26 toolchain; 1.25.2 refuses with
`Version of Delve is too old for Go version go1.26.0`.

No driver, Npcap, kernel module, vendor binary patch, or sample install is part of this lab.

## Dry run

The following ran on HMC on 2026-10-23. `$LAB` is the lab root above.

```sh
cd "$LAB"
# local listeners used only for the dry run
python3 -m http.server 18080 --bind 127.0.0.1 &
openssl req -x509 -newkey rsa:2048 -nodes -keyout out/key.pem -out out/cert.pem \
  -days 1 -subj /CN=localhost -addext subjectAltName=DNS:localhost
openssl s_server -accept 18443 -cert out/cert.pem -key out/key.pem -www -quiet &

# capture while two loopback requests run
(sleep 1; curl -s -o /dev/null http://127.0.0.1:18080/; \
             curl -s -k -o /dev/null https://localhost:18443/) &
./scripts/debug/capture.sh \
  --filter-file scripts/debug/filters/loopback.bpf \
  --name final --iface lo --duration 5 --out cap
```

Observed:

```
capture: starting on lo (rotate 300s, ring 24)
tcpdump: listening on lo, link-type EN10MB (Ethernet), snapshot length 262144 bytes
33 packets captured
66 packets received by filter
0 packets dropped by kernel
capture: wrote 1 file(s); hashes in .../cap/final.sha256
CAP_RC=0
-rw------- 1 root root 13942 Oct 23 14:12 cap/final-20261023T051221Z.pcap
```

Slice by port and by SNI:

```sh
python3 scripts/debug/slice_pcap.py cap/final-20261023T051221Z.pcap \
  --port 18080 -o cap/final-http.pcap --force
python3 scripts/debug/slice_pcap.py cap/final-20261023T051221Z.pcap \
  --sni localhost -o cap/final-sni.pcap --force
```

Observed (abridged):

```json
{"packets_in": 33, "packets_out": 13, "linktype": 1,
 "first_ts": "2026-10-23T05:12:21.993162Z",
 "sha256": "441d044b8bc7cb2742fd538c9b7e1aadcf4cf3b953ac8832fffed77aa895cff2"}
{"packets_in": 33, "packets_out": 1, "linktype": 1,
 "first_ts": "2026-10-23T05:12:21.999629Z",
 "sha256": "22e9571788b63008b76c2bf8178fe1f5a44947ef8258ca083cefb798b4b7d16d"}
```

The same input with an equivalent port filter produced byte-identical output on a rerun
(`5ea19efb...` both times for the HTTP slice), which is the determinism check.

Rotation and ring were exercised with `--rotate-seconds 2 --ring 3 --duration 8` and
produced exactly three files; `--stop` stopped a running capture through its pid file and
wrote `stoptest.meta` plus `stoptest.sha256`.

`slice_pcap.py --check` on HMC reported `tshark: null`, `editcap: null`, `capinfos: null`,
`tcpdump: /usr/bin/tcpdump`, Python 3.13.5.

## capture.sh

```sh
capture.sh --filter-file FILE --name NAME [--iface lo|any|IFACE] [--out DIR] \
           [--rotate-seconds N] [--ring N] [--duration N] [--dry-run]
capture.sh --stop DIR/NAME.pid
```

Filter file format: one complete BPF clause per line, `#` comments, blank lines ignored.
Clauses are OR-ed, so each line can be an independent host/port group:

```
(tcp port 443 or tcp port 4443) and (host login.ajs-app.com or host login.mmegg.com or host login.mbclubsh.com)
host 203.0.113.10 and tcp port 8443
```

`capture.sh` refuses a filter file containing `password=`, `authorization`, `bearer`,
`secret=`, or `token=`. Filters are host/port expressions; they never need a credential.

Files are written as `NAME-%Y%m%dT%H%M%SZ.pcap` with `TZ=UTC`, so the name matches the
`started=` field in `NAME.meta`. `tcpdump -G` rotates on the timer but creates the next file
only when traffic arrives; an idle capture can therefore have one file with a long name.

`-Z "$(id -un)"` keeps files owned by the invoking user instead of `tcpdump`, so rotation
works in a root-owned `cap/` directory and the post-run `chmod 600` succeeds.

The script writes `NAME.meta` (start/end, interface, filter hash, tcpdump version, kernel)
and `NAME.sha256` after the capture. `--duration` uses `timeout --signal=INT`; exit 124/130
is treated as a normal stop.

## slice_pcap.py

```sh
slice_pcap.py INPUT [-o OUT] [--start T] [--end T] [--host IP]... [--port N]... \
  [--proto tcp|udp|any] [--sni NAME]... [--resolve] [--backend native|tshark|editcap] \
  [--force] [--quiet] [--check]
```

- Time: `--start` inclusive, `--end` exclusive; RFC 3339 or epoch seconds.
- Host: IP literal, matched against source or destination; `--resolve` resolves names once.
- Port: source or destination; repeatable.
- SNI: parses a TLS ClientHello in a single TCP segment. A ClientHello split across
  segments is not reassembled.
- Link types: Ethernet (with up to two VLAN tags), Linux SLL, Linux SLL2, RAW, NULL.
- Output preserves packet order, timestamps, lengths, and the input global header. The
  `sha256` in the summary is the output file's hash.
- `native` is the tested backend. `tshark` and `editcap` are implemented but were not
  exercised on HMC (both absent); treat them as `[UNVERIFIED]`. The `editcap` backend accepts
  time bounds only (`-A`/`-B`; `-B` is inclusive), so use `native` for host/port/SNI.
- pcapng input is rejected with a conversion hint; convert with `editcap -F pcap` or
  `tshark -F pcap` on a host that has them.

Exit codes: 0 success, 2 usage, 3 unsupported input/backend or an existing output without
`--force`.

## run-dlv.sh

```sh
run-dlv.sh --build-only
run-dlv.sh --dry-run
run-dlv.sh --target account-test -- -test.run TestFactoryOpenAgainstMockFV -test.v
run-dlv.sh --headless --listen 127.0.0.1:2345
```

Targets:

| Target | Binary | Linked breakpoints |
|---|---|---|
| `daemon` (default) | `out/ajs-accountd-debug` | `ajsaccountd/internal/account.(*Control).Login` |
| `account-test` | `out/account.test` | `fv.Authenticate`, `fv.(*Session).OpenStreamContext`, `account.(*Factory).Open`, `account.(*Control).Login` |

At HEAD `b75c1c3` the daemon does not wire the gate or the session factory, so the linker
drops the FV symbols from `ajs-accountd`; `--target daemon --dry-run` lists them as
`not linked in this target`. The test binary links them because
`daemon/internal/account/session_test.go` calls `Factory.Open` and `OpenFlow`.

The script refuses `-token`, `--token`, `-resin-token`, and `--resin-token` on the command
line. Put `AJS_ADMIN_TOKEN` and `AJS_RESIN_ADMIN_TOKEN` in the environment.

The generated init file contains only breakpoints and `continue`. Safe values to print:

```
print s.SID
print %x string(s.Key)      # 16-byte FV session key as hex
print req.Server.ID
print req.UserName          # PII; only when the run needs it
```

Never print `cfg`, `password`, `req.ConnectPassword`, `Secret`, or a whole `Server` value
(`Par.FV.PS` is a per-server secret).

Headless mode does not accept `--init` (Delve warns and ignores it); connect with:

```sh
bin/dlv connect --init=out/dlv-account-test.cmds 127.0.0.1:2345
```

With the generated account-test init the first stop is `Factory.Open`; `continue` through
`Authenticate` to `OpenStreamContext`, where `s.Key` is populated.

## Known limits

- HMC has no `tshark`/`editcap`/`capinfos`/`mitmproxy`/Playwright; those paths are
  `[UNVERIFIED]` or manual.
- `native` was tested on Ethernet (IPv4/IPv6, TCP/UDP), Linux SLL2 (`-i any`), and TLS SNI;
  Linux SLL, RAW, NULL, VLAN, and pcapng are implemented but not exercised.
- `tcpdump -G` filenames are UTC because the script exports `TZ=UTC`; without that they
  follow the host timezone.
- No TLS decryption. The control API is HTTPS with normal certificate verification in our
  daemon (`emulator/internal/api/client.go:54` uses the default `http.Client`), so a pcap
  shows IP/port/SNI/timing/bytes only. Do not MITM the vendor.
- The FV data channel is plaintext TCP but its auth/data bodies are AES-128-CTR encrypted;
  the cleartext header carries `s=`, `n=`, and `k=`. The session key is only in our process.
