# AJS-51 — Account-live debugging and reverse-analysis lab

Status: playbook complete and dry-run verified on HMC; **no live vendor capture, no account,
no driver, no MITM**. Date: 2026-10-23. Slice: AJS-51. Repo HEAD: `b75c1c3`.

Method: source reading in `daemon/` and `emulator/`, plus a loopback-only dry run on HMC
(`ssh hmc`, Debian 13, kernel `6.12.111+deb13-amd64`). Every claim below carries a path and
line or a command and its output. Anything not run is marked `[UNVERIFIED]`.

Companion artifacts:

| Artifact | Purpose |
|---|---|
| `scripts/debug/capture.sh` | rotating `tcpdump` capture with a BPF filter file |
| `scripts/debug/slice_pcap.py` | deterministic pcap slicing by time/host/port/proto/SNI |
| `scripts/debug/run-dlv.sh` | `-N -l` build and Delve launch with safe breakpoints |
| `scripts/debug/README.md` | operator manual and dry-run transcript |
| `scripts/debug/filters/*.bpf` | control-host, FV-template and loopback filters |

## Answer

1. **The lab is runnable today for capture, slicing, and the FV/session code path; the live
   smoke is blocked at the HTTP layer.** At HEAD the daemon wires login
   (`daemon/cmd/ajs-accountd/main.go:123-137`, `daemon/internal/api/server.go:140`) but
   does **not** wire the session factory or the gate. The route table has no servers,
   session, or gate route (`daemon/internal/api/server.go:133-171`); `NewFactory` and
   `gate.NewServer` have no non-test caller. The deploy README states the same: the gate and
   per-node supervision are "not wired into node lifecycle" (`deploy/README.md:3-6,169`).
   Consequence: there is no HTTP way to enumerate `Servers`, open an FV session, or start a
   SOCKS5 gate. The playbook gives the exact lab harness and the future wiring contract; it
   does not fake the step.
2. **The daemon binary does not contain the FV symbols.** `go tool nm` on
   `out/ajs-accountd-debug` finds `ajsaccountd/internal/account.(*Control).Login` but no
   `ajsproxy/internal/fv.Authenticate` or `(*Session).OpenStreamContext`; the linker dropped
   them. `run-dlv.sh --target daemon --dry-run` says so. `--target account-test` builds
   `daemon/internal/account` as a test binary, which links all four breakpoints
   (`go tool nm` output in §11).
3. **TLS protects the control plane; the FV plane is plaintext TCP with encrypted bodies.**
   Our daemon uses the default `http.Client` (`emulator/internal/api/client.go:54`), so
   certificate verification is on and a pcap sees only IP/port/SNI/timing/bytes for the four
   control hosts. The FV carrier sends `[16B K][CTR(K,0)(len||text)][CTR(sessionKey,n)(body)]`;
   `K`, `s=` and `n=` are visible, but the auth/data bodies need the 16-byte `sessionKey`,
   which exists only in our process (`emulator/internal/fv/binary.go:214-315`). Extract it with
   Delve at `fv.(*Session).OpenStreamContext`; never MITM the vendor.
4. **HMC has `tcpdump`, `gdb`, `strace`, `ss`, `lsof`, Chromium and Node, but no
   `tshark`/`editcap`/`capinfos`, no `mitmproxy`, and no Playwright.** The pcap slicer
   therefore defaults to a stdlib `native` parser; the `tshark` and `editcap` backends are
   implemented but `[UNVERIFIED]` on this host. Delve 1.27.2 was installed into the lab tree
   only (Go 1.26 needs Delve >= 1.27; 1.25.2 refuses).
5. **The dry run passed:** loopback capture (33 packets), deterministic slices by port and by
   TLS SNI, 3-file rotation with a ring limit, `--stop`, IPv4/IPv6, Linux SLL2 (`-i any`),
   `-N -l` builds, and a real Delve breakpoint hit printing `s.SID` and `s.Key`. No vendor
   host was contacted and no credential was used.

## 1. Lab topology

```
Windows workstation (repo)  --ssh-->  HMC (Debian 13, root)
                                      |
                                      +-- /srv/dwgxhomecloud/scratch/ajs-51-debugger/
                                      |     src/daemon, src/emulator   (copied tree)
                                      |     out/                       (debug binaries, dlv,
                                      |                                 certs, init files)
                                      |     cap/                       (pcaps, mode 700)
                                      |     bin/dlv                    (1.27.2)
                                      |     scripts/debug/             (this slice)
                                      |
                                      +-- loopback listeners: 18080 (HTTP), 18443 (TLS)
                                      +-- 127.0.0.1:2261  ajs-accountd (when started)
                                      +-- 127.0.0.1:13000 gate (when a harness starts it)
```

- HMC is shared. `tcpdump -i any` can see unrelated host traffic; the BPF filter is the
  privacy boundary. The dry run used `-i lo` only.
- The vendor account, when it exists, is used **only** by `AJS_ACCOUNT_LOGIN=1`; without it
  the login handler keeps the store-only refresh (`daemon/cmd/ajs-accountd/main.go:313-321`).
- The HMC `src/` tree is a snapshot taken at 2026-10-23 14:01. Refresh it from the repo
  before a live run (`tar -cf - daemon emulator | ssh hmc 'tar -xf - -C "$LAB/src"'`); the
  working tree had concurrent edits outside this slice, so re-check line numbers if the
  source moves.
- No TUN, driver, Npcap, kernel module, or vendor-binary patch is required for the userland
  path (`protocol proxy`; `research/60-dynamic/REPORT.md` §Client behavior).

## 2. Tool inventory (HMC, measured 2026-10-23)

`ssh hmc 'for t in ...; do command -v ...; done'` produced:

| Tool | Path / version | Role | Notes |
|---|---|---|---|
| `tcpdump` | `/usr/bin/tcpdump` 4.99.5, libpcap 1.10.5 | capture | needs root or `CAP_NET_RAW` |
| `gdb` | `/usr/bin/gdb` 16.3 | native debugger | fallback if Delve is unavailable |
| `dlv` | `bin/dlv` 1.27.2 | Go debugger | lab-installed, pinned, removable |
| `strace` | `/usr/bin/strace` 6.13 | syscall trace | attach only to our daemon |
| `ss` | `/usr/bin/ss` | sockets | `-tnp`, `-K` if kernel supports |
| `lsof` | `/usr/bin/lsof` 4.99.4 | open files/sockets | `-p PID -a -i TCP` |
| `mitmproxy` / `mitmdump` | **MISSING** | TLS interception | **not used**; do not install |
| `tshark` / `dumpcap` / `editcap` / `capinfos` | **MISSING** | Wireshark CLI | native slicer is the fallback |
| `chromium` | `/usr/bin/chromium` 155.0.8059.79 | HAR/screenshots | root needs `--no-sandbox` |
| Playwright | **MISSING** (Python module absent) | browser automation | manual DevTools HAR |
| `node` / `npm` / `npx` | 24.21.0 / 11.19.0 / present | dashboard tooling | live build |
| `python3` | 3.13.5 | scripts | no `cryptography`/`pycryptodome` |
| `openssl` | 3.5.7 | cert + AES-CTR CLI | decrypt FV bodies with `enc -aes-128-ctr` |
| `curl` | 8.14.1 | smoke requests | use `--config`/`-H @file` for tokens |
| `jq` | 1.7 | JSON checks | |
| `go` | 1.25.14, `GOTOOLCHAIN=auto` → 1.26.0 | builds | module requires `go 1.26.0` |

Pinned lab binary hashes:

```
e3b8d7e83c663096db8520b681740a20af97bf166e4f5ac952b4578fd095ba3f  bin/dlv
d9b9fee18976ee32300f6d9512c2716895040e5a8a7da12b5b67574230a46462  out/ajs-accountd-debug
82ec0fedf2b143161137d4b9edff7a123c3d8c5a14e7cd7ccb1e03f961824366  out/account.test
```

### Install policy

- **Allowed without asking:** install Delve into `$LAB/bin` with `GOBIN=... go install
  github.com/go-delve/delve/cmd/dlv@v1.27.2`; generate a self-signed cert for loopback; create
  lab directories. All are reversible and confined to the lab tree.
- **Owner approval required:** `apt install tshark/mitmproxy`, Playwright/npm installs, any
  package that changes the shared HMC, and any `iptables`/`nft`/`tc` rule outside a dedicated
  namespace. Record the exact removal command before applying.
- **Never:** drivers, Npcap, kernel modules, vendor binary modification, a global MITM CA, or
  a proxy that terminates the vendor's TLS.

## 3. What the wire exposes

| Channel | Transport | Visible in pcap | Hidden |
|---|---|---|---|
| Control API | HTTPS to the four hosts, default cert verification | SNI, IP, port, timing, byte counts | `X-Sig`, login body, `LoginToken`, `ConnectPassword`, server list |
| FV auth | plain TCP, binary carrier | `[16B K]`; `s=`, `n=`, `k=` recoverable with `CTR(K,0)`; lengths, timing | auth body fields (`u`, `p`, `cuid`, `cv`, ...) under `sessionKey` |
| FV data | plain TCP, fresh connection per flow | `[16B K]`; `s=<sid>`, `n=<fresh IV>`, `k=` recoverable with `CTR(K,0)`; encrypted SOCKS5 CONNECT | target host/port and payload under `sessionKey` |
| Gate | SOCKS5 no-auth on loopback | greeting/CONNECT, reply code, byte counts | target payload is plaintext inside the relay |

Control hosts (`daemon/internal/account/login_service.go:21-26`):

```
https://login.ajs-app.com
https://login.mmegg.com
https://login.mmegg.com:4443
https://login.mbclubsh.com
```

The FV endpoint is dynamic: `VpnServer.svr` + `par.fv.p` from the `Servers` response
(`research/40-protocol/PROTOCOL.md` §Endpoint). Do not hardcode it; fill
`filters/fv.bpf.example` after the server list is known.

## 4. Capture plan

### 4.1 BPF filters

`capture.sh` reads one complete clause per line and ORs them. Default control filter:

```
(tcp port 443 or tcp port 4443) and (host login.ajs-app.com or host login.mmegg.com or host login.mbclubsh.com)
```

- `tcpdump` resolves the names once at filter-compile time. That DNS lookup is not a vendor
  request; the capture starts before the daemon dials.
- On 2026-10-23 all three names resolved to `43.198.116.181`; if DNS changes, recompile. A
  pinned-IP filter is stale by design; prefer names.
- FV filter: copy `filters/fv.bpf.example`, set `host <svr> and tcp port <par.fv.p>`. Add it
  as a second clause only after the server list is known.
- The filter file is refused if it contains `password=`, `authorization`, `bearer`, `secret=`,
  or `token=`. Filters are host/port expressions and never need a credential.

### 4.2 Rotation and slicing

```sh
# start; 5-minute files, 24-file ring (2 h), stop with SIGINT or --stop
./scripts/debug/capture.sh --filter-file scripts/debug/filters/control.bpf \
  --name live-control --iface any --rotate-seconds 300 --ring 24 --out "$LAB/cap"

# stop through the pid file
./scripts/debug/capture.sh --stop "$LAB/cap/live-control.pid"

# slice the capture to one host/port or time window
python3 scripts/debug/slice_pcap.py "$LAB"/cap/live-control-*.pcap \
  --host <control-ip> --port 443 -o "$LAB/cap/control-443.pcap" --force
python3 scripts/debug/slice_pcap.py "$LAB"/cap/live-control-*.pcap \
  --start 2026-10-23T10:00:00Z --end 2026-10-23T10:05:00Z \
  -o "$LAB/cap/login-window.pcap" --force
```

- `-G` rotates on the timer but creates the next file only when traffic arrives; an idle
  capture can have one long-named file. The ring limit is enforced by `-W`.
- The script exports `TZ=UTC`, so filenames and `meta` are UTC. Without it `tcpdump` uses the
  host timezone (the first dry run showed `14:07` in the name and `05:07Z` in meta).
- `-Z "$(id -un)"` keeps files owned by the invoking user; without it `tcpdump` drops to the
  `tcpdump` user and cannot create the next rotated file in a root-owned 700 directory
  (observed: `Permission denied` on the second file).
- `slice_pcap.py` copies packets byte-for-byte in order and reuses the input global header;
  the same input and filters produce identical output (verified twice:
  `5ea19efb...`).
- pcapng is rejected with a conversion hint. Convert on a host with `editcap -F pcap` or
  `tshark -F pcap`; HMC has neither.

### 4.3 Session-key extraction with Delve

The daemon has no session/gate wiring, so use the test target first, then the live harness:

```sh
# 1. build the test binary that links the FV symbols
./scripts/debug/run-dlv.sh --build-only --target account-test

# 2. headless Delve; the init file is a client-side script
./scripts/debug/run-dlv.sh --headless --target account-test \
  --listen 127.0.0.1:2345

# 3. connect and set the stable breakpoint
bin/dlv connect --init=out/dlv-account-test.cmds 127.0.0.1:2345
```

The generated init stops first at `Factory.Open`; `continue` through `Authenticate` to
`OpenStreamContext`, where `s.Key` is populated.

At the `OpenStreamContext` stop:

```
(dlv) print s.SID
"sid-1"
(dlv) print %x string(s.Key)
<32 hex characters — mock-test key, redacted here>
```

`print %x string(s.Key)` is the 16-byte AES-128 session key as 32 hex characters. The
`OpenStreamContext` breakpoint is after `Factory.Open`, so the key is already populated. For
an auth-only session with no flow, break at `fv.(*HTTPUpgrade).authenticateBinary`
(`emulator/internal/fv/binary.go:214`) and print `sessionKey` after line 236; line-number
breakpoints move with edits, so prefer the function breakpoint plus `next`.

`fvfacade.Authenticate` is a variable alias of `fv.Authenticate`
(`emulator/fvfacade/facade.go:53-55`), so Delve's symbol is
`ajsproxy/internal/fv.Authenticate`; the facade name is not a function symbol.
`OpenStream` is a one-line wrapper around `OpenStreamContext`
(`emulator/internal/fv/auth.go:146-151`); break the latter to see the session state.

Safe values to print: `s.SID`, `s.Key` (hex), `req.Server.ID`, `req.UserName` only when the
run needs it. **Never print** `cfg`, `password`, `req.ConnectPassword`, any `Secret`, or a
whole `Server` value: `Server.Par.FV.PS` is a per-server secret (`emulator/internal/api/client.go:86-90`).

To decrypt a captured FV body with the extracted key and the cleartext `n` from the header:

```sh
# body.bin is the body bytes after the header text; n_hex is the n= value
openssl enc -d -aes-128-ctr -K "$KEY_HEX" -iv "$N_HEX" -in body.bin -out body.txt
```

HMC has `openssl` but no Python crypto module. Extracting `body.bin` from a pcap needs TCP
reassembly (Wireshark "Follow TCP Stream" on a workstation, or a small reassembler); the
native slicer deliberately does not reassemble streams. `[UNVERIFIED]` end-to-end decrypt on
HMC.

### 4.4 `strace`, `ss`, `lsof`

```sh
pid=$(pgrep -f '/out/ajs-accountd-debug' | head -1)
ss -tnp 'dport = :443 or dport = :4443'
lsof -nP -p "$pid" -a -i TCP
strace -f -tt -T -e trace=network -s 256 -p "$pid" -o "$LAB/out/strace-network.log"
```

- `-e trace=network` records `connect`/`sendto`/`recvfrom` metadata, not payloads. Do **not**
  use `-e trace=read,write` on a process holding credentials unless the log is treated as a
  secret and deleted after the session.
- `strace` pauses the process between syscalls; expect client timeouts. Detach with Ctrl-C,
  not `kill -9`.
- `ss -K` (socket destroy) needs kernel `CONFIG_INET_DIAG_DESTROY`; if it fails, use an
  `iptables` REJECT rule with a saved rollback.

### 4.5 Dashboard HAR, console, screenshots

Live dashboard build (`dashboard/README.md:41-64,77`):

```sh
cd dashboard
VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 \
  VITE_PANEL_TOKEN="$AJS_ADMIN_TOKEN" npm run build
npx vite preview --host 127.0.0.1 --port 18082
```

The token is baked into `dist/` and sent as `Authorization: Bearer ...` on every `/v1/*`
request (`dashboard/src/lib/api.ts:55`). Treat the bundle and HAR as secrets.

Manual HAR (Chromium DevTools, no Playwright on HMC):

1. Open `http://127.0.0.1:18082`, DevTools → Network, tick **Preserve log**.
2. Reproduce login → accounts → nodes/leases.
3. Right-click → **Save all as HAR with content**.

Headless screenshot and console:

```sh
chromium --headless=new --no-sandbox --disable-gpu --window-size=1440,900 \
  --user-data-dir="$LAB/out/chrome-profile" \
  --screenshot="$LAB/out/dashboard.png" http://127.0.0.1:18082/
chromium --headless=new --no-sandbox --enable-logging=stderr --v=1 \
  --user-data-dir="$LAB/out/chrome-profile" http://127.0.0.1:18082/ 2> "$LAB/out/console.log"
```

Redact the HAR before it leaves the host:

```sh
python3 - "$LAB/out/dashboard.har" <<'PY'
import json, sys
path = sys.argv[1]
doc = json.load(open(path))
drop = {"authorization", "cookie", "set-cookie", "x-api-key"}
for entry in doc["log"]["entries"]:
    for side in ("request", "response"):
        entry[side]["headers"] = [
            h for h in entry[side].get("headers", [])
            if h.get("name", "").lower() not in drop
        ]
    entry["request"]["queryString"] = [
        q for q in entry["request"].get("queryString", [])
        if q.get("name", "").lower() not in {"token", "password", "secret"}
    ]
json.dump(doc, open(path + ".redacted", "w"), indent=2, sort_keys=True)
PY
```

Also crop account rows and any `username`/email/phone from screenshots.

## 5. Live smoke sequence (after an Owner account)

Preconditions: Owner supplies an account; the lab tree is built; the control capture is
running; the daemon is on loopback only. **Do not start with a live login**; run the
preflight first.

### Step 0 — preflight (no vendor)

```sh
cd "$LAB"
./scripts/debug/slice_pcap.py --check
./scripts/debug/capture.sh --filter-file scripts/debug/filters/control.bpf \
  --name control --iface any --dry-run
./scripts/debug/run-dlv.sh --dry-run --target daemon
./scripts/debug/run-dlv.sh --build-only --target daemon
```

Pass: filter compiles; `--check` reports `tshark: null`; dry-run lists
`Control.Login` and the three dropped FV breakpoints; build exits 0.

### Step 1 — start the daemon with a sealed secret

```sh
umask 077
export AJS_ADMIN_TOKEN=$(openssl rand -hex 32)
export AJS_MASTER_KEY=$(openssl rand -hex 32)
export AJS_ACCOUNT_LOGIN=1
export AJS_STATE_DIR="$LAB/out/state-live"
export AJS_LISTEN=127.0.0.1:2261
mkdir -p "$AJS_STATE_DIR"
"$LAB/out/ajs-accountd-debug" > "$LAB/out/daemon.log" 2>&1 &
```

`AJS_MASTER_KEY` is required for `AccountSecret`; without it a login returns 409
`account: no stored secret` (`daemon/internal/store/store.go:167-189`). `AJS_ACCOUNT_LOGIN=1`
is the only switch that wires the vendor path (`main.go:313-321`).

Write the admin token to a mode-600 curl config, not to `argv`:

```sh
printf 'header = "Authorization: Bearer %s"\n' "$AJS_ADMIN_TOKEN" > "$LAB/out/curlrc"
chmod 600 "$LAB/out/curlrc"
```

### Step 2 — start the control capture

```sh
./scripts/debug/capture.sh --filter-file scripts/debug/filters/control.bpf \
  --name live-control --iface any --rotate-seconds 300 --ring 24 --out "$LAB/cap"
```

Confirm `tcpdump: listening on any, link-type LINUX_SLL2` and the filter in `live-control.meta`.

### Step 3 — create the account

The password never goes on the command line. Set `AJS_LAB_LABEL`, `AJS_LAB_USER` and
`AJS_LAB_PASS` in the operator shell (the vendor account values from the Owner; do not echo
them), then build the JSON in a mode-600 file, POST it, and delete it:

```sh
python3 - <<'PY' > "$LAB/out/account.json"
import json, os
print(json.dumps({
    "label": os.environ["AJS_LAB_LABEL"],
    "username": os.environ["AJS_LAB_USER"],
    "password": os.environ["AJS_LAB_PASS"],
    "platform": "linux",
    "concurrency": 32,
    "regions": ["jp"],
}))
PY
chmod 600 "$LAB/out/account.json"
curl -sS --config "$LAB/out/curlrc" -H 'Content-Type: application/json' \
  --data-binary @"$LAB/out/account.json" \
  http://127.0.0.1:2261/v1/accounts | tee "$LAB/out/account-created.json"
rm -f "$LAB/out/account.json"
```

Expected: HTTP 201 and an account JSON without credentials. `CreateAccountInput` validates
`label`, `username`, `password`, `platform` (`daemon/internal/model/model.go:245-252`).

### Step 4 — login (first vendor contact)

```sh
id=$(jq -r .id "$LAB/out/account-created.json")
curl -sS --config "$LAB/out/curlrc" -X POST \
  "http://127.0.0.1:2261/v1/accounts/$id/actions/login" \
  | tee "$LAB/out/account-login.json"
```

- 200 + `state=active`: success; the login token and connect password are held in memory
  (`daemon/internal/account/login_service.go:227-285`), never written. The server map is
  **not** part of this result; it arrives only with `LoginSync` (Step 5).
- 401: vendor `AuthResult != "OK"`; the account becomes `auth_failed`. Stop after two
  attempts; do not retry a rejected credential.
- 409: no sealed secret; fix `AJS_MASTER_KEY` and recreate the account.
- 503: transport/protocol failure; check the capture and `daemon.log`, not the credential.

The control pcap should show a TLS connection to one of the four hosts. Because TLS is
verified, the body and server list are not visible; this is expected.

### Step 5 — servers (blocked at HEAD)

There is no HTTP route for `Servers`; the route table has no servers/session route
(`daemon/internal/api/server.go:133-171`). `Control.Login` returns a `LoginResult` without
the server map (`convertLogin`, `daemon/internal/account/control.go:141-151`); only
`Control.LoginSync` fills it (`control.go:119-125`, `snapshot` at `:154-161`). Nothing in the
daemon calls `LoginSync`.

Two honest options:

1. **Wait for the wiring slice** (recommended): add
   `GET /v1/accounts/{id}/servers` returning only `id`, `svr`, `par.fv.p`, `sts`, `tit`, and
   a `POST /v1/accounts/{id}/sessions` that calls `Factory.Open`. Do not expose
   `par.fv.ps` or `ConnectPassword`.
2. **Use the lab harness** in §6, which calls `LoginSync` and `Factory.Open` directly in a
   test binary. This is a lab-only path, not a product API.

Delve at `Control.LoginSync` (`control.go:119`) if you need to inspect the map. Print one
safe field at a time:

```
print len(servers)
print servers["<id>"].Svr
print servers["<id>"].Par.FV.P
print servers["<id>"].Sts
```

Do not `print servers`: it renders `Par` including `ps`.

### Step 6 — FV session

With the harness (or future wiring), open the session:

```
Factory.Open(ctx, SessionRequest{
    AccountID:       id,
    Server:          servers[serverID],
    Device:          device,
    UserName:        login.UserName,
    ConnectPassword: login.ConnectPassword,
    Profile:         account.Linux423Binary,
})
```

Source path: `daemon/internal/account/session.go:115` → `fvfacade.Authenticate`
(`emulator/internal/fv/auth.go:173`) → `HTTPUpgrade.authenticateBinary`
(`binary.go:214`). The session key is generated at `binary.go:236`; the response text must
contain `code=200`, then the auth map must arrive before the half-close (`binary.go:293`,
`readAuthMap` at `:166`).

Expected: `sess.SID()` non-empty, `sess.Healthy()==true`, `Fields()["sip"]` present. The FV
pcap shows a new TCP connection to `svr:par.fv.p`, a 16-byte `K`, and encrypted text.

### Step 7 — SOCKS5 CONNECT and egress IP

Start the gate on loopback with the session (harness or future wiring):

```
gate.NewServer(gate.Config{
    NodeID: "live", AccountID: id, Listen: "127.0.0.1:13000", Limit: 4,
}, sess)
```

`gate.handle` reads the no-auth SOCKS5 greeting, parses CONNECT, admits a slot, and calls
`sess.OpenFlow` (`daemon/internal/gate/server.go:408,468`). Then:

```sh
curl -sS --socks5-hostname 127.0.0.1:13000 https://cloudflare.com/cdn-cgi/trace | grep '^ip='
curl -sS https://cloudflare.com/cdn-cgi/trace | grep '^ip='   # direct, for comparison
```

Expected: the gate returns a different egress IP than the direct route (unless the vendor
server is local). The gate log line carries `node_id`, `target`, `ingress_bytes`,
`egress_bytes`; the gate has no authentication, so it must stay on loopback.

### Step 8 — rotate

There are two different rotations; do not conflate them:

| Rotation | Command | Effect |
|---|---|---|
| Lease/egress (Resin) | `POST /v1/leases/{id}/actions/rotate` | `runtime.ResinPanel.RotateLease` (`daemon/internal/runtime/panel.go:223`) uses the M1 filter-exclusion path; real egress changes only when Resin is wired |
| Store-only | same route without a panel | `store.RotateLease` (`daemon/internal/store/nodes_leases.go:94`) changes the mock egress IP, **not** the network |

After a rotate, repeat Step 7 and compare `ip=`. If the IP is unchanged, the store-only path
ran; check `AJS_RESIN_ADMIN_TOKEN` and `/v1/topology`. The M1 spike measured a 0.3 s move on
the filter-exclusion path and warned that deleting a lease alone may re-pick the same node
(`research/77-m1-spike/REPORT.md`).

### Step 9 — failure injection

Every row below is an Owner-approved, lab-scoped change; save the relevant state first and
keep the rollback command in the same shell.

| Injection | Command (lab-scoped) | Expected | Rollback |
|---|---|---|---|
| Control host unreachable | `iptables -I OUTPUT -p tcp -d <control-ip> --dport 443 -j REJECT` | login 503 `UPSTREAM_UNAVAILABLE`; no credential state change | `iptables -D OUTPUT -p tcp -d <control-ip> --dport 443 -j REJECT` |
| Auth rejected | point `AJS_API_HOSTS` at `research/60-dynamic/mock/mock_control.py` returning `AuthResult=NO` | 401; account `auth_failed`; error has no secret | stop mock, restore `AJS_API_HOSTS` |
| FV auth rejected | mock FV returns `r=denied` | `*fv.AuthError`; `Factory.Open` fails before a session | stop mock |
| FV port unreachable | `iptables -I OUTPUT -p tcp -d <fvip> --dport <fvport> -j REJECT` | `OpenFlow` error; gate replies SOCKS5 `0x05` | delete the exact rule |
| Slow vendor | **do not** host-wide `tc netem` on shared HMC; use a netns or a local `socat` delay proxy | timeouts, not corruption | remove the namespace/proxy |
| Node killed | kill the harness child, or `supervisor.Stop` when wired | in-flight flows fail; new flows fail until restart | restart the child; re-probe |

Save the firewall before any rule:

```sh
iptables-save > "$LAB/out/iptables.before"
# ... inject ...
iptables-save > "$LAB/out/iptables.after"
diff -u "$LAB/out/iptables.before" "$LAB/out/iptables.after"
```

Do not edit `/etc/hosts` on HMC when `AJS_API_HOSTS` can point at a mock; the AJS-07 lab did
the hosts edit in WSL with a backup, but the daemon's configurable host list is cleaner.

## 6. Lab harness for steps 5–7 (not committed)

Place this under the **HMC scratch copy** at `src/daemon/cmd/live-lab/main.go`; it uses only
exported package APIs. It is not a product change and is not run by this slice.

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsproxy/fvfacade"
)

func main() {
	user := os.Getenv("AJS_LAB_USER")
	pass := os.Getenv("AJS_LAB_PASS")
	listen := flag.String("gate", "127.0.0.1:13000", "gate listen")
	flag.Parse()
	if user == "" || pass == "" {
		log.Fatal("AJS_LAB_USER and AJS_LAB_PASS are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dev, err := account.NewDevice(account.Linux423Binary.ClientVersion)
	if err != nil {
		log.Fatal(err)
	}
	ctrl := account.NewControl(account.DefaultLoginHosts, dev, account.Options{})
	defer ctrl.Close()
	if _, err := ctrl.Login(ctx, user, account.Secret(pass)); err != nil {
		log.Fatal(err)
	}
	sync, err := ctrl.LoginSync(ctx)
	if err != nil {
		log.Fatal(err)
	}
	var serverID string
	for id, s := range sync.Servers {
		fmt.Printf("server id=%s svr=%s port=%d sts=%s\n", id, s.Svr, s.Par.FV.P, s.Sts)
		if serverID == "" && s.OK() {
			serverID = id
		}
	}
	if serverID == "" {
		log.Fatal("no usable server")
	}
	factory := account.NewFactory(account.Linux423Binary, &fvfacade.HTTPUpgrade{}, fvfacade.AuthOptions{})
	sess, err := factory.Open(ctx, account.SessionRequest{
		AccountID:       "live-lab",
		Server:          sync.Servers[serverID],
		Device:          dev,
		UserName:        sync.UserName,
		ConnectPassword: sync.ConnectPassword,
		Profile:         account.Linux423Binary,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("sid=%s healthy=%v\n", sess.SID(), sess.Healthy())
	srv, err := gate.NewServer(gate.Config{NodeID: "live", AccountID: "live-lab", Listen: *listen, Limit: 4}, sess)
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.Start(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("gate=%s\n", srv.Addr())
	<-ctx.Done()
}
```

Build and run in the scratch tree:

```sh
cd "$LAB/src/daemon"
GOTOOLCHAIN=auto go build -o "$LAB/out/live-lab" ./cmd/live-lab
AJS_LAB_USER=... AJS_LAB_PASS=... "$LAB/out/live-lab" --gate 127.0.0.1:13000
```

Then Step 7's curl uses `127.0.0.1:13000`. `[UNVERIFIED]`: this harness was not run against
a vendor account in this slice.

## 7. Redaction and stop conditions

### Redaction

- **Never** paste or commit: `AJS_ADMIN_TOKEN`, `AJS_MASTER_KEY`, `RESIN_ADMIN_TOKEN`,
  `RESIN_PROXY_TOKEN`, `LoginToken`, `ConnectPassword`, `par.fv.ps`, the 16-byte FV session
  key, account passwords, emails, phone numbers, or TOTP seeds.
- pcaps: TLS ciphertext plus FV encrypted bodies, but still sensitive (IPs, SNI, device IDs,
  timing). Mode 600; keep under `cap/` (gitignored); delete when the investigation ends
  unless the Owner asks to retain evidence.
- HAR: redact `Authorization`/`Cookie`/`Set-Cookie`/`X-API-Key` and query `token`/`password`.
- Screenshots: crop account/user rows.
- Delve: write `s.Key` to a mode-600 file, not to chat or the board; delete after use.
- Logs: `daemon.log` contains account IDs and vendor result messages, not credentials; still
  restrict to the lab.
- The supervisor passes only an allowlist of process essentials plus `Spec.Env` to a child
  (`daemon/internal/supervisor/supervisor.go:591-614`), so `AJS_MASTER_KEY`, `AJS_ADMIN_TOKEN`
  and the Resin tokens cannot reach a node process. Do not add them to `Spec.Env`.

### Stop conditions (stop immediately)

1. A credential appears in a pcap, log, HAR, console, or Delve transcript. Delete the
   artifact, rotate the credential, and tell the Owner.
2. The vendor returns 401/403/429/5xx repeatedly, or the account becomes `auth_failed` twice.
   Do not retry; respect rate limits.
3. The capture contains traffic outside the four control hosts or the selected FV endpoint.
   Stop, narrow the BPF, delete the off-target pcap.
4. A debugger pause causes client timeouts or the daemon is unresponsive. Detach; do not
   leave a stopped process.
5. Disk pressure in `cap/` or the ring is misconfigured.
6. The Owner says stop, the account is at risk, or billing/ToS is unclear.

## 8. High-risk actions, consequence, rollback

| Action | Consequence | Rollback |
|---|---|---|
| `tcpdump -i any` on shared HMC | may capture unrelated users' traffic; disk growth | stop capture, delete pcap, narrow BPF/use `lo` |
| `iptables`/`nft` insert | can blackhole host services or the SSH path | `iptables-save` before; delete the exact rule; verify with `iptables-save` diff |
| `tc qdisc netem` host-wide | adds latency/loss to every host flow | `tc qdisc del dev <iface> root`; prefer a netns |
| `/etc/hosts` edit | breaks DNS for the host | restore the saved backup; flush resolver cache |
| `dlv attach` / `gdb -p` | pauses the process; client timeouts; memory may contain secrets | detach/quit; never `gcore` a process holding credentials |
| Chromium `--no-sandbox` as root | sandbox off; a malicious page could escape | dedicated profile dir, no untrusted pages, close the browser |
| `apt install tshark`/`mitmproxy` | changes the shared host | Owner approval; `apt remove` and record the package list |
| MITM CA / TLS proxy | exposes vendor credentials; breaks certificate verification | **never**; if a lab mock is needed, point `AJS_API_HOSTS` at it |
| Driver / Npcap / kernel module | kernel-level risk, reboot, shared-host impact | **never**; not needed |
| Vendor binary patch | integrity/legal risk; changes the artifact under study | **never** in this lab; AJS-07's temp-copy patch is not repeated |
| Resin lease delete/rotate | can disrupt other users of the platform | scope to a lab platform; re-add the filter/line; verify topology |
| `kill -9` a node | in-flight requests fail; possible vendor reconnect churn | restart, re-probe, watch the supervisor event stream |

## 9. Exit and handoff fields

```
STATUS: DONE | BLOCKED
Slice: AJS-51
Artifacts:
  research/80-debugger-prep/PLAYBOOK.md
  scripts/debug/{capture.sh,slice_pcap.py,run-dlv.sh,README.md}
  scripts/debug/filters/{control.bpf,fv.bpf.example,loopback.bpf}
Dry run: HMC 2026-10-23, loopback only; capture 33 packets; slices by port/SNI; rotation 3 files;
         dlv 1.27.2 breakpoint hit printing s.SID and s.Key.
Not run: live vendor login, server list, FV session, gate CONNECT, egress IP, rotation,
         dashboard HAR. [UNVERIFIED] by design; no Owner account.
Next: wire GET /v1/accounts/{id}/servers and POST /v1/accounts/{id}/sessions + gate start
      (or run the §6 harness with an Owner account), then execute §5 steps 5-9.
Board: AJS-51 close --verdict ok --verify "ssh hmc '<dry-run command>'"
```

The next action is one line: **wire the server/session/gate endpoints, then run §5 steps 5–9
against the Owner account.**

## 10. Known blinds

- HMC has no `tshark`/`editcap`/`capinfos`, so the `tshark` and `editcap` slicer backends
  and pcapng conversion are `[UNVERIFIED]`. The native parser is tested on Ethernet
  IPv4/IPv6, Linux SLL2, TCP/UDP, and TLS SNI; Linux SLL, RAW, NULL, VLAN, and pcapng are
  implemented but not exercised.
- `tls_sni` does not reassemble a ClientHello split across TCP segments; a segmented hello
  is not matched.
- No TLS decryption. Control API bodies, `LoginToken`, `ConnectPassword`, and the server list
  are invisible in a pcap; MITM is prohibited.
- The FV session key is only extractable at a breakpoint that has a live `*fv.Session`; an
  auth-only run with no flow does not hit `OpenStreamContext`. Use
  `authenticateBinary` + `next` if needed, with moving line numbers.
- The daemon at HEAD cannot expose servers, open a session, or start the gate. The §6
  harness is a lab workaround, not a product API; it was not run against a vendor.
- Store-only lease rotation changes the mock `egressIp`, not the network. Real rotation
  requires Resin (`AJS_RESIN_ADMIN_TOKEN`).
- HMC is shared; `any` capture is only as safe as its BPF filter. `tcpdump -G` rotates on
  traffic, not wall clock.
- Delve 1.27.2 is required for the Go 1.26 toolchain; 1.25.2 refuses to run. The daemon
  binary's FV symbols are dead-code-eliminated until wiring lands.
- The dry run did not exercise the `tshark`/`editcap` backends, HAR capture, Playwright,
  `mitmproxy`, or any vendor host.

## 11. Dry-run transcript (HMC, 2026-10-23)

Tool inventory (abridged; full table in §2):

```
tcpdump /usr/bin/tcpdump 4.99.5 (libpcap 1.10.5)
gdb GNU gdb (Debian 16.3-1) 16.3
dlv Delve 1.27.2 (lab bin, sha256 e3b8d7e8...)
strace 6.13
ss /usr/bin/ss
lsof 4.99.4
chromium 155.0.8059.79
node v24.21.0, npm 11.19.0
python3 3.13.5
go1.25.14 (GOTOOLCHAIN=auto -> go1.26.0)
tshark MISSING, dumpcap MISSING, editcap MISSING, capinfos MISSING
mitmproxy MISSING, mitmdump MISSING, playwright module absent
```

Loopback capture and slice:

```
$ ./scripts/debug/capture.sh --filter-file scripts/debug/filters/loopback.bpf \
    --name final --iface lo --duration 5 --out cap
capture: starting on lo (rotate 300s, ring 24)
tcpdump: listening on lo, link-type EN10MB (Ethernet), snapshot length 262144 bytes
33 packets captured
66 packets received by filter
0 packets dropped by kernel
capture: wrote 1 file(s); hashes in .../cap/final.sha256
CAP_RC=0
-rw------- 1 root root 13942 Oct 23 14:12 cap/final-20261023T051221Z.pcap

$ python3 scripts/debug/slice_pcap.py cap/final-20261023T051221Z.pcap \
    --port 18080 -o cap/final-http.pcap --force
{"packets_in": 33, "packets_out": 13, "linktype": 1,
 "first_ts": "2026-10-23T05:12:21.993162Z",
 "sha256": "441d044b8bc7cb2742fd538c9b7e1aadcf4cf3b953ac8832fffed77aa895cff2"}

$ python3 scripts/debug/slice_pcap.py cap/final-20261023T051221Z.pcap \
    --sni localhost -o cap/final-sni.pcap --force
{"packets_in": 33, "packets_out": 1, "linktype": 1,
 "first_ts": "2026-10-23T05:12:21.999629Z",
 "sha256": "22e9571788b63008b76c2bf8178fe1f5a44947ef8258ca083cefb798b4b7d16d"}
```

Rotation and ring:

```
$ ./scripts/debug/capture.sh ... --name rot --rotate-seconds 2 --ring 3 --duration 8
Maximum file limit reached: 3
154 packets captured
capture: wrote 3 file(s); hashes in .../cap/rot.sha256
-rw------- 1 root root 3294 ... cap/rot-20261023T050926Z.pcap
-rw------- 1 root root 8240 ... cap/rot-20261023T050928Z.pcap
-rw------- 1 root root 8404 ... cap/rot-20261023T050930Z.pcap
```

Link-layer and IPv6 checks:

```
$ ./scripts/debug/capture.sh ... --name anytest --iface any --duration 4
tcpdump: listening on any, link-type LINUX_SLL2 (Linux cooked v2), snapshot length 262144 bytes
13 packets captured
$ python3 scripts/debug/slice_pcap.py cap/anytest-*.pcap --port 18080 ...
{"linktype": 276, "packets_in": 13, "packets_out": 13}

$ ./scripts/debug/capture.sh ... --name v6 --iface lo --duration 4   # curl http://[::1]:18081/
$ python3 scripts/debug/slice_pcap.py cap/v6-*.pcap --host ::1 --port 18081 ...
{"linktype": 1, "packets_in": 12, "packets_out": 12}
```

Stop path:

```
$ ./scripts/debug/capture.sh --stop cap/stoptest.pid
capture: stopped pid 543813
... cap/stoptest.meta, cap/stoptest.sha256
```

Delve:

```
$ ./scripts/debug/run-dlv.sh --build-only --target account-test
run-dlv: building account-test with -N -l
run-dlv: built .../out/account.test
run-dlv: init file .../out/dlv-account-test.cmds

$ go tool nm out/account.test | grep -E 'fv\.(Authenticate|\(\*Session\)\.OpenStreamContext)|account\.\(\*(Factory|Control)\)\.(Open|Login)'
  8a37a0 T ajsproxy/internal/fv.(*HTTPUpgrade).authenticateBinary
  89dca0 T ajsproxy/internal/fv.(*Session).OpenStreamContext
  89dee0 T ajsproxy/internal/fv.Authenticate
  8ba4c0 T ajsaccountd/internal/account.(*Control).Login
  8bfe40 T ajsaccountd/internal/account.(*Factory).Open
```

Live breakpoint hit (headless + connect):

```
(dlv) break ajsproxy/internal/fv.(*Session).OpenStreamContext
Breakpoint 1 set at 0x89dcb3 for ajsproxy/internal/fv.(*Session).OpenStreamContext() ./src/emulator/internal/fv/auth.go:151
(dlv) continue
[Breakpoint 1] ... auth.go:151
(dlv) print %x string(s.Key)
<32 hex characters — mock-test key, redacted here>
(dlv) print s.SID
"sid-1"
```

Daemon symbol check (the wiring gap, measured):

```
(dlv) break ajsaccountd/internal/account.(*Control).Login
Breakpoint 1 set at 0x82c796 for ... control.go:104
(dlv) break ajsproxy/internal/fv.Authenticate
location "ajsproxy/internal/fv.Authenticate" not found
```

## 12. Self-review with the `debugger-review` checklist

Checklist source: `skill://debugger-review` (`.agent/success-when.md`,
`.agent/atomic-artifacts.md`, `.agent/exit-conventions.md`, `.agent/tool-blinds.md`).

| # | Checklist item | Result |
|---|---|---|
| 1 | Every claim maps to a path/command; `[UNVERIFIED]` explicit | Yes: source line citations throughout; dry-run commands/output in §11; live steps marked `[UNVERIFIED]` |
| 2 | Unpacked/tool-download steps reproducible (manifest + version + hash) | `dlv` 1.27.2 pinned with install command and sha256; Go toolchain 1.26.0 via `GOTOOLCHAIN=auto`; binaries hashed; no driver/Npcap |
| 3 | High-risk actions have consequence + rollback | §8 table; no drivers, no MITM, no vendor patch |
| 4 | Exit/handoff updated; next action one line | §9; next action is the wiring slice, not a re-run |
| 5 | Conclusion first, evidence second, remaining unknowns | Answer, evidence §11, blinds §10 |

Artifacts written before the final report:

```
research/80-debugger-prep/PLAYBOOK.md
scripts/debug/capture.sh
scripts/debug/slice_pcap.py
scripts/debug/run-dlv.sh
scripts/debug/README.md
scripts/debug/filters/control.bpf
scripts/debug/filters/fv.bpf.example
scripts/debug/filters/loopback.bpf
```

No file outside `research/80-debugger-prep/` and `scripts/debug/` was edited. No git
operation was performed. No TCP connection was made to a vendor host; the control-filter
dry run resolved the four names via DNS only. No credential was read or written.
