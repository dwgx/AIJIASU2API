# AJS-102 — First live-account smoke: original app vs daemon egress match

Status: redacted live report, 2026-10-23, HMC. Slice AJS-102.
Method: original Linux app `ajiasu 4.2.3.0` and `ajs-accountd` run on HMC against the vendor
control plane and FV endpoint, with `tcpdump` captures on the HMC lab scratch tree. The daemon
side used the pin, profile and node-limit knobs already in the tree
(`daemon/internal/account/tls.go`, `daemon/cmd/ajs-accountd/main.go`). This report is the only
artifact written by this slice; the captures and state live in HMC scratch and are not committed.

Redaction: the account is `live-ajs-01` / `acc-07`; the login name is a phone number and is
written `<redacted-phone>`. No password, no `LoginToken`/`ConnectPassword`, no session key, no
full account JSON appears below or in the repo. Passwords are referenced only as
`$AJS_LIVE_USER` / `$AJS_LIVE_PASS` placeholders.

## Answer

1. The daemon's `linux-4.2.3.0` profile is live-accepted end-to-end: original app and daemon
   both tunneled through the same vendor node `vvn-2076-264` (镇江 #1) and both returned the
   identical egress IP `222.186.57.63` from `ifconfig.me` and `ipinfo.io`, so login →
   `loginSync` → FV auth → SOCKS5 gate → data channel matches the shipping client's observable
   behavior.
2. The live run pins three previously open facts (see `research/81-protocol-gaps/REPORT.md`):
   all four control hosts present the same self-signed `CN=localhost` leaf certificate that
   default Go verification rejects (the pin is required, and it worked); the vendor sends
   strings where the offline fixtures had numbers (`Login.ExpireTime`, `Servers[*].par.fv.u/p`,
   `vsid`, `loc.o`), which broke the first daemon attempts and is now fixed; and the vendor
   allows **one** active FV data session per account, so `AJS_NODE_LIMIT` must default to 1
   (limit 3 produced two rejected gates and one working one).

## Confidence

**High.** Every numbered fact below was observed on 2026-10-23 on HMC by this run or by the
workers whose evidence is cited in the table; the daemon-side code anchors were re-read in this
slice (paths given inline). The egress-IP match is the strongest evidence: two independent
implementations, one shipping binary and one daemon, exiting through the same node at the same
IP.

**Not claimed.** Anything in "What remains" is `[UNVERIFIED]` and was not exercised.

## Evidence

| # | Fact | Where observed | Result |
|---|---|---|---|
| 1 | Original app login | HMC, `ajiasu 4.2.3.0` (core 20241105-1), 2026-10-23 | `Login Result: OK`; membership Paid; expiration 2026-12-21; login name is `<redacted-phone>` |
| 2 | Original app server list | same run, `list` | 100+ servers, e.g. `vvn-2076-264` 镇江 #1 |
| 3 | Original app connect | same run, `connect "镇江 #1"` with `protocol socks5` | local SOCKS5 listener `127.0.0.1:1080` |
| 4 | Original app egress | `curl --socks5-hostname 127.0.0.1:1080` | `222.186.57.63` from both `ifconfig.me` and `ipinfo.io` |
| 5 | Control transport | pcap, SNI `login.mmegg.com`, TCP `43.198.116.181:4443`, TLS | TLS only in capture; no plaintext credentials |
| 6 | Control leaf cert | all four control hosts | self-signed `CN=localhost`, SHA-256 `E8:83:F0:B9:04:14:BC:24:9A:92:9F:BD:4F:CC:2D:52:1A:F1:A9:FF:A9:9A:50:72:6C:FA:7B:B2:A2:B7:3A:89`; default Go verification fails; the shipping app connects anyway; our pin was added and used |
| 7 | Daemon login (fixed shapes) | `AJS_ACCOUNT_LOGIN=1`, `AJS_API_HOSTS=https://login.mmegg.com:4443`, pin set | login HTTP 200; first attempts failed on the string-shaped fields, then passed after the fixes |
| 8 | Daemon node + gate | `AJS_NODE_LIMIT=1` | one node `acc-07:vvn-2076-264` LIVE, gate `127.0.0.1:13012` |
| 9 | Daemon egress parity | `curl --socks5-hostname 127.0.0.1:13012` | `222.186.57.63` — identical to the original app (row 4) |
| 10 | Daemon flow accounting | gate logs, `gate flow closed` (`daemon/internal/gate/server.go:492-503`) | `ifconfig.me:80` ingress 77 / egress 164; `ipinfo.io:443` ingress 1878 / egress 6555 |
| 11 | Node-limit 3 behavior | `AJS_NODE_LIMIT=3` | first two gates answered SOCKS5 `0x05`; daemon logged `fv: data channel rejected ... fm=disconnected by another connection` (`emulator/internal/fv/binary.go:374`); third gate worked, egress `58.221.59.68` |
| 12 | Captures | HMC scratch, not committed | `/srv/dwgxhomecloud/scratch/ajs-live-20261023/cap/app-login2.pcap` (177 packets), `daemon-login5.pcap` (300 packets) |
| 13 | State hygiene | HMC scratch | `state.json` / `secrets.json` mode 600/700; no credentials in the repo |

## Findings

### F1 — Control TLS: the pin is load-bearing, not optional

- SNI `login.mmegg.com`, TCP `43.198.116.181:4443`, TLS.
- All four control hosts present the same self-signed leaf, `CN=localhost`, SHA-256
  `E8:83:F0:B9:04:14:BC:24:9A:92:9F:BD:4F:CC:2D:52:1A:F1:A9:FF:A9:9A:50:72:6C:FA:7B:B2:A2:B7:3A:89`.
  Default Go `crypto/tls` verification fails (self-signed + hostname mismatch); the shipping
  app connects anyway — it does not verify the chain the way a stock Go client does. This
  resolves gap-report row 26 / U8 (`research/81-protocol-gaps/REPORT.md`): the "deliberate
  safety divergence" is real on the wire and would have blocked every control call.
- The daemon pins instead of trusting: `AJS_CONTROL_CERT_SHA256` →
  `account.PinnedHTTPClient(fingerprint, timeout)` (`daemon/internal/account/tls.go:36-48`)
  accepts exactly that one leaf, matches on SHA-256 fingerprint (colons optional,
  `ParseCertFingerprint`, `tls.go:20-33`), and fails closed with `ErrCertPin`
  (`errors.Is`-matchable) on any other certificate. Malformed fingerprint fails startup. The
  pin was used for the successful logins in rows 7–11.
- The fingerprint above is public configuration, not a secret: it is the hash of the
  vendor-presented leaf and is safe to keep in the repo.

### F2 — Live response shapes: strings, not numbers

The first daemon login attempts failed before any FV traffic; the vendor JSON differs from every
offline fixture:

- `Login.ExpireTime` arrives as a **string**, not a JSON number.
- `Servers[*].par.fv.u`, `Servers[*].par.fv.p`, `Servers[*].vsid`, `Servers[*].loc[*].o` are
  **numeric strings**, not JSON numbers.

Both are now handled and were live-verified by the successful logins in rows 7–11:

- `LoginResult.UnmarshalJSON` accepts a JSON number of Unix milliseconds, a numeric string, or
  one of four date layouts, normalizing to int64 milliseconds; null/empty/absent → 0
  (`emulator/internal/api/client.go:88-133`).
- `FlexInt` decodes a JSON number (fractions/exponents rejected) or a numeric string; null and
  `""` → 0; applied to `ServerFV.U/P`, `Server.Vsid`, `ServerLoc.O`
  (`emulator/internal/api/client.go:175-272`).

This closes the fixture-vs-live half of gap-report B1's data side; the request-shape half
(field names/values) is answered by the fact that the same profile logged in successfully
without changes to the request.

### F3 — One FV data session per account

With `AJS_NODE_LIMIT=3` the controller opened three nodes. The first two gates refused every
flow: the SOCKS5 front end answered `0x05` (connect failure), and the daemon logged
`fv: data channel rejected ... fm=disconnected by another connection`. The third gate worked
and exited through `58.221.59.68`.

Reading: the vendor tears down an FV auth/data session when the same account authenticates
another one — `fm=disconnected by another connection` is the server-side verdict text. One
account = one live FV session, so:

- the default `AJS_NODE_LIMIT` is now `1` (commit `86491e1`, landed after this live run);
  the pre-fix default `0` = all would have opened 100+ sessions on this account and
  self-DOSed them — the same self-inflicted outage the README warns about (~165 listeners,
  `daemon/README.md:847`);
- per-account "concurrency" cannot come from parallel FV sessions on one account; multi-node
  accounts need multiple vendor accounts (untested — see below).

### F4 — Egress parity: daemon ≡ original app

Same node (`vvn-2076-264`), same egress IP (`222.186.57.63`) from two independent
implementations and two echo services. Combined with the flow byte counts (row 10) matching
real request/response sizes, the daemon's carrier — `[16B K][CTR(K,0)(len||text)][body]`,
RSA-OAEP-SHA1 session key, AES-128-CTR, per-flow data channel with `s=<sid>`/`n=<div>` — is
behaviorally indistinguishable from the shipping client on this path. Gap-report rows 10–14's
live half and B3/B4 are resolved by this run.

### Command shapes (redacted)

Original app (HMC, one vendor account, credentials entered by the app's own prompt):

```sh
ajiasu login                                   # Login Result: OK, membership Paid, expires 2026-12-21
ajiasu list                                    # 100+ servers, e.g. vvn-2076-264 镇江 #1
ajiasu connect "镇江 #1"                        # protocol socks5 → listen 127.0.0.1:1080
curl --socks5-hostname 127.0.0.1:1080 https://ifconfig.me   # 222.186.57.63
```

Daemon (HMC; keys/tokens are placeholders, never literals):

```sh
AJS_ACCOUNT_LOGIN=1 \
AJS_API_HOSTS=https://login.mmegg.com:4443 \
AJS_CONTROL_CERT_SHA256=E883F0B90414BC249A929FBD4FCC2D521AF1A9FFA99A50726CFA7BB2A2B73A89 \
AJS_NODE_LIMIT=1 \
AJS_MASTER_KEY="$AJS_MASTER_KEY" AJS_ADMIN_TOKEN="$AJS_ADMIN_TOKEN" \
  ./ajs-accountd

curl -s -X POST http://127.0.0.1:2262/v1/accounts -d '{         # lab instance, port 2262
  "label": "live-ajs-01",
  "username": "'"$AJS_LIVE_USER"'",
  "password": "'"$AJS_LIVE_PASS"'",
  "platform": "linux",
  "concurrency": 1
}'
curl -s -X POST http://127.0.0.1:2262/v1/accounts/acc-07/actions/login
curl -s --socks5-hostname 127.0.0.1:13012 https://ifconfig.me   # 222.186.57.63
```

Anatomy: `POST /v1/accounts` accepts `label/username/password/platform/concurrency/regions`
(`daemon/internal/model/model.go:247-255`); the password is sealed at rest under
`AJS_MASTER_KEY` and never logged; the login action wires vendor login + `loginSync` + node
controller; the node id is `<account>:<server>` (`daemon/internal/runtime/nodes.go:650-652`),
the gate port is allocated from `127.0.0.1:13000` upward
(`daemon/internal/runtime/nodes.go:24`), and `13012` was this run's allocation.

## What remains `[UNVERIFIED]`

- **Multi-account.** Only `acc-07` was live-tested. Whether two accounts on the same daemon
  interfere (device identity, IP-level rate limiting, `fm=disconnected by another connection`
  across accounts) is unknown; F3's single-session verdict is only proven within one account.
- **Resin.** The panel/Resin wiring (`AJS_RESIN_URL`/`AJS_RESIN_ADMIN_TOKEN`) was not part of
  this run; node publish/unpublish through Resin is unobserved.
- **B2 — signing.** The live backend accepted the unsigned `linux-4.2.3.0` profile. Whether
  signing (`X-Sig`/`X-Timestamp`) is accepted, required for other profiles, or rejected was
  not tested; the OpenAJS secret remains unused.
- **B5 — two-login stability.** Only one successful daemon login sequence was exercised in the
  limit-1 run (plus the failed shape-fix attempts). Consecutive logins regenerating device
  identity (gap-report row 6) were not observed to trigger vendor-side action, but the
  anti-abuse risk of repeated re-logins is unmeasured.
- **Hot update.** Dynamic API host refresh (`ApiUrls`, `FetchedUrlHosts`, `SyncVersion`,
  gap-report U9) did not occur in the window; all traffic stayed on
  `login.mmegg.com:4443`/`43.198.116.181`.
- Keepalive (`ka/kat/kapf`) and idle-session lifetime: the session was used immediately and
  torn down; no idle window was held.

## Security and redaction

- **Not in this report or the repo:** the phone number (`<redacted-phone>` everywhere), the
  password, `LoginToken`, `ConnectPassword`, FV session keys, `_uid`/device ids, any full
  account JSON, `AJS_MASTER_KEY`/`AJS_ADMIN_TOKEN` values.
- **In HMC scratch only, mode 600/700:** `state.json`, `secrets.json`, and the two pcaps
  (`/srv/dwgxhomecloud/scratch/ajs-live-20261023/`). The pcaps are TLS-only for the control
  plane; no plaintext credential exists in any capture.
- The certificate fingerprint and the vendor IPs/SNIs are public wire facts and are recorded.
- The live account is a paid, single-session account; every future live test should keep
  `AJS_NODE_LIMIT=1` and one login attempt per run to avoid the F3 rejection mode.

## Next actions
1. **Done** (commit `86491e1`): `AJS_NODE_LIMIT` now defaults to `1` in
   `daemon/cmd/ajs-accountd` — F3 had made the `0`-default a self-inflicted outage on any live
   account. Keep the default a fail-safe cap (1); an override above 1 still risks the F3
   rejection mode on a single-session account.
2. Add a live-shape regression fixture: a `client.php` recording with string
   `ExpireTime`/`par.fv.u/p`/`vsid`/`loc.o` (values only, no account data) so the F2 fixes stay
   pinned by tests.
3. Two-login stability test (B5) on a scratch account: consecutive logins, diffing device
   identity fields, before enabling any scheduled re-login.
4. A second account to test multi-account coexistence (node/gate accounting, per-account
   session isolation).
5. Long-idle session with keepalive observation to resolve `ka/kat/kapf`.

Cross-references: `research/81-protocol-gaps/REPORT.md` (gaps B1–B5, rows 4/10–14/26, U6–U9),
`research/80-debugger-prep/PLAYBOOK.md` (lab topology, capture/slice tooling, pin-safe Delve
print rules).
