# AJS-496 — Dashboard and API gap audit

Status: read-only audit, 2026-10-24. Branch `panel-design`, HEAD `9c6ddd9`.
Scope: `dashboard/src` (8 pages), `daemon/internal/api`, `daemon/internal/model`,
`daemon/internal/{runtime,gate,account,store}`, `docs/PANEL.md`, and the four new
sample packages extracted under `C:/Users/dwgx1/.dwgx/tmp/ajs-re-20261024a/`.
No sample was executed, no vendor host was contacted, no credential was used, and no
source file outside this report was written.

The question: the samples imply a large client surface; the daemon exposes a small
operator surface; what must the dashboard grow to make the lab observable and safe?
The answer is a **read-only Lab page first**, built on telemetry the daemon already
computes but never serializes, followed by a small number of explicitly guarded actions.

## 1. Bottom line

1. **The dashboard is an account/lease panel, not a lab instrument.** All eight pages
   (`dashboard/src/App.tsx:34-41`) are built on 16 model interfaces (`dashboard/src/lib/types.ts`)
   and 35 route registrations (`daemon/internal/api/server.go:172-213`). None exposes a vendor server
   catalog, an FV session, a gate counter, a protocol profile, a device identity, a daemon
   log line, or a packet capture.
2. **Three controls are not merely missing — they are misleading today.** `Pause` on an
   account is a store-only `PATCH` and does not stop its gates (`Accounts.tsx:338` →
   `api.ts:141` → `accounts.go:53`); `Probe now` writes “egress verified” without dialing
   anything (`store/nodes_leases.go:227-243`); `Rotate` without Resin changes a recorded IP
   and adds 17 requests (`store/nodes_leases.go:245-267`). These are P0 because an operator
   will make live decisions on fabricated state.
3. **The richest telemetry already exists in memory.** `gate.State` carries accepted,
   rejected, ingress and egress bytes (`gate/server.go:107-126`); `NodeController` holds
   the session, gate, port and claimed egress per node (`runtime/nodes.go:109-150`);
   `FlowSession` exposes `SID`/`Healthy` (`account/session.go:92-102`); `store.DeviceIdentity`
   holds the sealed device record (`store/device.go:16-50`). The gap is serialization and UI,
   not instrumentation.
4. **The server catalog is the biggest missing read surface.** `docs/PANEL.md:58` documents
   `GET /v1/accounts/{id}/servers`, but the route does not exist; the login result is only in
   memory (`account/login_service.go:93,257`) and dies on restart. The sample client’s main
   screen is exactly this catalog (`res.fvr:table-server-list.htm`, `window-main.htm`).
5. **Protocol/profile drift is invisible.** The daemon accepts only `linux-4.2.3.0`
   (`main.go:599-607`), while the current samples are Windows 4.9.6.0, Android 4.12.2.0,
   macOS 4.9.6 and Linux 4.3.0.1. The version matrix exists in research
   (`research/40-protocol/PROTOCOL.md:532-542`) but has no API or UI.
6. **Three settings are decorative.** `transport` and `healthIntervalSec` are persisted
   (`store/users_keys.go:332-367`) but no runtime consumer reads them; sessions use
   `VersionProfile.Transport` (`account/session.go:56-60`) and no canary scheduler exists.
   `subscriptionRefreshSec` is likewise not read by `runtime/panel.go` (fixed 30 s).
7. **The Lab page can be delivered without a schema migration or a vendor call.** Version,
   flags, device hashes, gate counters, session metadata and logs are all available from
   existing structs or environment; the only new storage is an optional in-memory ring buffer
   for logs (captures, when added, are files on disk).
8. **First slice = read-only, no vendor I/O:** `/v1/lab/overview`, `/v1/lab/flags`,
   `/v1/lab/gates`, `/v1/lab/sessions`, `/v1/lab/profiles`, `/v1/accounts/{id}/device`,
   `/v1/lab/logs`, `/v1/lab/diagnostics`, plus the documented
   `/v1/accounts/{id}/servers`. The only state changes are the existing logout action
   (surfaced in the UI) and honesty fixes: relabel or disable the three fake controls above.

## 2. What exists today

### 2.1 Pages

| Page | Route | Data source | Actions exposed | Structural gap |
|---|---|---|---|---|
| Overview | `/` (`App.tsx:34`) | `getMetrics`, accounts, nodes, leases, audit (`Overview.tsx:135-139`) | refresh, navigate | no session/gate/profile/version truth |
| Accounts | `/accounts` (`App.tsx:35`) | `listAccounts` (`Accounts.tsx:268`) | create, test login, pause/resume, delete (`:327,:338,:360,:402`) | no detail, no logout, no edit, no server list |
| Nodes | `/nodes` (`App.tsx:36`) | `listNodes` (`Nodes.tsx:55`) | drain/undrain, probe (`:83,:93`), detail (`:294`) | detail is static; no gate/session/flow counters |
| Leases | `/leases` (`App.tsx:37`) | `listLeases` (`Leases.tsx:23`) | rotate, release (`:33,:45`) | no bytes; rotate is synthetic without Resin |
| Users | `/users` (`App.tsx:38`) | `listUsers`, `listKeys` (`Users.tsx:31-32`) | suspend, allowlist, key create/revoke (`:82,:95,:107,:131`) | no create/delete user; no usage detail |
| Requests | `/requests` (`App.tsx:39`) | `listRequests` (`Requests.tsx:30`) | simulated mutation (`:73-86`) | table is seeded/simulated, not gate telemetry |
| Logs | `/logs` (`App.tsx:40`) | `listAudit(20)` (`Logs.tsx:25`) | client-side filter, fake export (`:46-47`) | no runtime logs, no server-side filters |
| Settings | `/settings` (`App.tsx:41`) | `getSettings` (`Settings.tsx:35`) | save, update check, reset mock (`:74,:105,:89`) | transport/health/subscription not wired |

### 2.2 API surface

`daemon/internal/api/server.go:172-213` registers:

| Area | Routes | Handler anchor |
|---|---|---|
| Health/metrics | `GET /healthz`, `GET /metrics`, `GET /v1/metrics` | `ops.go:22,57,41` |
| Accounts | `GET/POST /v1/accounts`, `GET/PATCH/DELETE /v1/accounts/{id}`, `POST .../actions/login`, `POST .../actions/logout` | `accounts.go:14,40,31,53,66,102,156` |
| Nodes | `GET /v1/nodes`, `POST .../actions/{drain,undrain,probe}` | `nodes_leases.go:13,69,100,129` |
| Leases | `GET /v1/leases`, `POST .../actions/rotate`, `DELETE /v1/leases/{id}` | `nodes_leases.go:138,158,177` |
| Users/keys | `GET/POST /v1/users`, `PATCH/DELETE /v1/users/{id}`, `POST .../{suspend,resume}`, `PATCH .../allowlist`, `GET/POST /v1/apikeys`, `DELETE /v1/apikeys/{id}` | `users_keys.go:11,20,33,67,75,84,93,112,132,145` |
| Ops | `GET /v1/requests`, `POST /v1/requests/mutations`, `GET /v1/audit`, `GET/PATCH /v1/settings`, `GET /v1/topology`, `POST /v1/reconcile`, `GET /v1/system/update` | `ops.go:117,126,139,155,159,195`; `server.go:219,235`; `update.go:47` |

Documented but absent: `GET /v1/accounts/{id}/servers` (`docs/PANEL.md:58`),
`POST /v1/users/{id}/rotate` (`:75`), `GET /v1/users/{id}/usage` (`:76`).
Also absent: any `/v1/lab/*`, `/v1/sessions`, `/v1/gates`, `/v1/flows`, `/v1/logs`,
`/v1/pcap`, `/v1/flags`, `/v1/diagnostics` route.

### 2.3 Data that exists but is not serialized

| Source | Field(s) | Why it matters | Current visibility |
|---|---|---|---|
| `gate.State` (`gate/server.go:107-126`) | `Active`, `Limit`, `Accepted`, `Rejected`, `IngressBytes`, `EgressBytes`, `StartedAt`, `Draining` | the only real per-node traffic truth | none |
| `nodeGate` (`runtime/nodes.go:109-127`) | `session`, `port`, `serverID`, `egressIP`, `draining` | joins node → session → gate | none |
| `FlowSession` (`account/session.go:92-102`) | `SID()`, `Healthy()`, `Fields()` | session identity and vendor claims (`sip/cip/ip`, `ka/kat/kapf`) | none |
| `store.DeviceIdentity` (`store/device.go:16-50`) | `UID`, `OSDID`, `CUID`, `CSEID`, `OSP/OSD/OSV/DIM`, `Version`, `Site` | anti-abuse identity stability | none |
| `LoginService.Result` (`login_service.go:257`) | `Servers`, `UserName`, `ConnectPassword` | vendor catalog and login outcome | none (and not persisted) |
| `store.Settings` (`model.go:238-247`) | transport, health interval, lease TTL, refresh | operator intent vs runtime truth | partial (settings page) |
| daemon `slog` (`main.go`) | every request, gate flow, node lifecycle | diagnosis | stdout only |

## 3. Function matrix

49 functions: **14 P0**, **25 P1**, **10 P2**. Legend: **P0** = blocks trustworthy lab
operation or live safety; **P1** = high-value read-only telemetry or a guarded action;
**P2** = parity/convenience. “Current” names the
page, API route and handler; “Proposed” names the smallest endpoint/UI that closes the gap.
Risk is the risk of *not* having the function or of exposing it carelessly, as worded.

### 3.1 P0 — trust and live safety

| # | Function | Evidence | Current exposure | Missing | Risk | Proposed endpoint / UI |
|---|---|---|---|---|---|---|
| F01 | Show account stop semantics: logout drains, pause does not | `Accounts.tsx:338` → `api.ts:141` → `accounts.go:53` (store only); logout `accounts.go:156` runs `AccountLogoutHook` and `store.LogoutAccount` drains nodes | UI has only Pause; `POST /v1/accounts/{id}/actions/logout` exists (`server.go:182`) but is not called by `api.ts` | Logout button, confirmation, explanation that Pause leaves gates/sessions alive | **High** — a paused account can keep the only vendor FV session and keep serving traffic | `POST /v1/accounts/{id}/actions/logout` in the account drawer; disable Pause with “does not stop gates” note; audit |
| F02 | Show effective feature flags and posture | env parsing `main.go:41-50`; documented flags `daemon/README.md:872-890`; no route | none | read-only effective config, provenance, secret-set flags, restart-required markers | **High** — operator cannot see `AJS_ACCOUNT_LOGIN=0`, `AJS_MASTER_KEY` unset, pin unset, or `AJS_NODE_LIMIT>1` | `GET /v1/lab/flags`; `Lab.tsx#flags` |
| F03 | Show vendor server catalog per account | `docs/PANEL.md:58` documents `GET /v1/accounts/{id}/servers`; route absent from `server.go:172-213`; result only in `LoginService.results` (`login_service.go:93,257`); sample `res.fvr:table-server-list.htm`; Android schema adds `marks[]`, `par.fv.ps`, `ServerLoad.UserLoadStatus.full[]` (`research/84-ajs-496/APK.md:346-405`) | none | catalog snapshot, stale marker, node linkage, selected/skipped by node limit, marks/full/`hasPingSecret` (never `ps`), `vsid` not exposed | **High** — the lab cannot see what the account can reach or why a node was skipped | `GET /v1/accounts/{id}/servers`; `Lab.tsx#servers` |
| F04 | Real node/server probe, not a timestamp | `Nodes.tsx:93` → `api.ts:186` → `nodes_leases.go:129` → `store/nodes_leases.go:227-243` (“no real upstream … latency and egress IP are unchanged”) | “Probe now” button and an audit line claiming “egress verified” | a dial through the gate, latency, observed egress, match flag | **High** — fabricated success is worse than no probe | `POST /v1/lab/egress-check` (guarded); until then relabel the store action `touch` and return `verified:false` |
| F05 | Gate counters per node and fleet | `gate.State` (`gate/server.go:107-126`); `State()` at `:254-266`; no route | none; Nodes detail shows only model fields (`Nodes.tsx:294-380`) | accepted/rejected, active/limit, ingress/egress bytes, startedAt, draining | **High** — no way to tell a working gate from a dead one, or to reconcile vendor byte counts | `GET /v1/lab/gates` (+ `?node=`); `Lab.tsx#sessions` |
| F06 | Session identity and health | `FlowSession.SID()`/`Healthy()` (`account/session.go:92-102`); `nodeGate.session` (`runtime/nodes.go:109-127`); no route | none | redacted SID, healthy flag, age, last flow, account/node/server join | **High** — session death is only visible as a node state change later | `GET /v1/lab/sessions`; `Lab.tsx#sessions` |
| F07 | Egress claimed vs verified | claimed from FV fields `sip/cip/ip` (`runtime/nodes.go:717-728`); live parity report `research/82-live-20261023/REPORT.md` F4; no verification in store | Nodes/Leases show `egressIp` as fact | source (`fv-claim` vs `echo`), verified value, checkedAt, match | **High** — the displayed IP may be a vendor claim, not the observed exit | `GET /v1/lab/egress`; `POST /v1/lab/egress-check` guarded |
| F08 | Real per-flow request log from the gate | `Requests.tsx:164-165` says “Per-request telemetry from the gate”; gate logs one line (`gate/server.go:493`) but never writes `store.Requests`; only seed and mutation rows exist (`ops.go:126`) | table of seeded/simulated rows | flow id, node/account, target, status, bytes, latency, error class | **High** — the page claims observation that does not exist | `GET /v1/lab/flows` fed by a bounded gate flow ring; label simulated rows in `Requests.tsx` |
| F09 | Honest lease rotation | `Leases.tsx:33` → `nodes_leases.go:158`; without Resin `store.RotateLease` (`store/nodes_leases.go:245-267`) assigns a synthetic next IP and adds 17 requests | rotate button always enabled | real re-session/Resin rotation, or a disabled control with reason | **High** — a recorded egress change that did not happen misleads every downstream decision | disable/label when `cfg.Panel == nil`; later `POST /v1/nodes/{id}/actions/reconnect` |
| F10 | Runtime log stream | `main.go` uses `slog`; `server.go` logs requests; no endpoint | none | bounded ring buffer, level/component filters, redaction, tail | **High** — failures (cert pin, `fm=`, gate reject) are only on stdout | `daemon/internal/lablog` + `GET /v1/lab/logs`; `Lab.tsx#logs` |
| F11 | Active protocol profile and sample baseline | `VersionProfile` (`account/session.go:14-72`); only `linux-4.2.3.0` accepted (`main.go:599-607`); version matrix `research/40-protocol/PROTOCOL.md:532-542`; Android fixed `protocolVersion=2`, modes `auto / vpn / proxy-tun`, `ImplType` 0/1 (`research/84-ajs-496/APK.md:382-418`) | none; Settings only has a transport toggle | `_v`, `cv`, `protocolVersion`, `implType`, OAEP hash, signing mode, request shape, transport, mode vocabulary; sample baselines | **High** — an auth failure cannot be attributed to a version/shape mismatch | `GET /v1/lab/profiles`; `Lab.tsx#protocol` |
| F12 | Wire the transport control or disable it | `Settings.tsx:154-169`; persisted by `store/users_keys.go:332-339`; runtime uses `profile.Transport` (`account/session.go:56-60`); no `Settings().Transport` caller | a control that appears to switch carrier | either pass the setting into the session factory or mark “stored only, requires profile change” | **High** — operator believes a carrier switch took effect | read effective transport in `/v1/lab/profiles`; disable the control until wired |
| F13 | Node-limit / concurrency safety warning | `AJS_NODE_LIMIT` (`main.go:576-596`, default 1); live F3 one-session rule `research/82-live-20261023/REPORT.md`; UI concurrency field `Accounts.tsx:711` | env only; create form accepts any concurrency | warning if `AJS_NODE_LIMIT>1` or account concurrency >1; default/guard 1 | **High** — the documented self-DOS/kick mode | overview warning + create-account guard; `GET /v1/lab/flags` |
| F14 | Reject inconsistent account/node state | `store.UpdateAccount` state branch `store/store.go:490-500` changes only the field; `store.LogoutAccount` (`store/store.go:658-681`) is the path that drains nodes | API accepts `PATCH {state:"paused"}` while nodes stay LIVE (`accounts.go:53`) | backend invariant: paused/expired account cannot own LIVE nodes; or explicit “pause keeps sessions” contract | **High** — two code paths produce contradictory state, and the UI shows the wrong one | enforce in `store.UpdateAccount` or route pause through logout; add a `Lab` warning for `paused` accounts with LIVE nodes |

### 3.2 P1 — read-only telemetry and guarded actions

| # | Function | Evidence | Current exposure | Missing | Risk | Proposed endpoint / UI |
|---|---|---|---|---|---|---|
| F15 | Bind daemon version/uptime/commit | `GET /healthz` returns version (`ops.go:22-39`); `GET /metrics` has uptime (`ops.go:57-115`); Settings hardcodes `0.1.0` (`Settings.tsx:283-284,312`) and endpoint `127.0.0.1:2261` (`:271-274`) | version and endpoint hardcoded in UI | version, uptime, commit, build target and endpoint from API | Medium — wrong version/endpoint misleads update and connection decisions | `GET /v1/lab/overview`; bind Settings/Lab header |
| F16 | Account detail: membership, expiry, last login, usage | `GET /v1/accounts/{id}` (`accounts.go:31`) and `model.go:90-104`; `api.ts` has no `getAccount`; Android `LoginSync.Login` carries `MembershipStatus`, `UseCount`, `FreeUsageTimeLeft`, `TicketUnreadCount` (`research/84-ajs-496/APK.md:318-345`) | list/card only | detail drawer and membership/trial fields | Low | account drawer on `GET /v1/accounts/{id}`; extend model later |
| F17 | Server status/availability badges | sample `lang_zh_cn.txt` keys `ServerStatusFullLoad`, `ServerStatusMembership`, `PromptServerFullLoad`; `sts` filter `runtime/nodes.go:610-638`; `marks[]` + `ServerLoad.full[]` (`research/84-ajs-496/APK.md:346-405`) | node state only | per-server `sts`, blocking marks, full-by-`vsid`, nullable when absent | Low | fields in `GET /v1/accounts/{id}/servers`; `Lab.tsx#servers` |
| F18 | Server latency test | Windows `TextRefreshPing`, `ServerStatusTesting`; Android `text_server_response_speed_test`, `prompt_cannot_speedtest`, `FVPingManager.java`; 8-field `;` report with `udpResult` (`research/84-ajs-496/APK.md:489-501`); current probe is fake | fake node probe | per-server latency, timeout/error, last-tested, UDP result; never expose `par.fv.ps` | Medium — vendor calls consume concurrency | `POST /v1/lab/probes` or extend `egress-check`; guarded |
| F19 | Keepalive visibility (`ka/kat/kapf`) | gap report row 19 (`research/81-protocol-gaps/REPORT.md`); Windows strings `kapf`; no implementation | none | fields, enforcement state, idle warning | Medium — idle session may die silently | `keepalive:{supported:false}` in `GET /v1/lab/sessions`; implementation later |
| F20 | Request target privacy and retention | `model.go:179-193` target; `Requests.tsx` shows it; no redaction/TTL | full target | host-only default, `reveal` gate, retention | Medium — user data in a lab panel | `targetHost`/`targetHash` in `/v1/lab/flows`; `reveal=1` guarded |
| F21 | Audit filters, pagination and real export | `Logs.tsx:25` hardcodes 20; `Logs.tsx:46-47` export is a placeholder; `ops.go:139-153` only `limit` | client-side filters only | `subject`, `since`, `action`, cursor; CSV/JSON download | Low | extend `GET /v1/audit`; real export button |
| F22 | Device identity view (hashed) | `store.DeviceIdentity` (`store/device.go:16-66`); `login_service.go:235-257`; Android 12-field fingerprint with `_os_dim` embedding `android_id` (`research/84-ajs-496/APK.md:270-300`); no route | none | hashed IDs, claims/presence for 12 fields, stability | **High** if raw — anti-abuse identifiers and a hardware id | `GET /v1/accounts/{id}/device` hashed; `Lab.tsx#device` |
| F23 | Device stability across logins | live report F5; `EnsureAccountDevice` (`store/device.go:68-90`) | none | `stable`, `firstSeenAt`, hash equality | Medium — instability triggers anti-abuse | same endpoint; show stable badge |
| F24 | Cert-pin / TLS posture | `main.go:512-533`; `account/tls.go:20-48`; live F1 (self-signed `CN=localhost`); Android has no CA bundle/pin (`research/84-ajs-496/APK.md:237-269`) | env only | mode (`default`/`pinned`), fingerprint, warning | **High** — default verification fails against the live leaf; the shipping clients effectively disable it | `GET /v1/lab/flags` + diagnostics; show fingerprint (public) |
| F25 | Secrets/master-key posture | `main.go:374-390`; `daemon/internal/secret/store.go`; no route | env only | `secretsEnabled`, `masterKeySet`, sealed count | Medium — login returns 409 without it | flags/diagnostics booleans only |
| F26 | State snapshot health | `store/persist.go`; `main.go:326-358`; no route | none | last save, path basename, size, error | Medium — silent data loss on restart | `GET /v1/lab/diagnostics` |
| F27 | Topology / Resin subscription view | `GET /v1/topology` (`server.go:211`, `ops.go:195`); `runtime/panel.go` | API only | subscription text, node→tag map, platforms, Resin version | Low | Overview topology card (`Lab.tsx#overview`) |
| F28 | Reconcile action | `POST /v1/reconcile` (`server.go:213`, `:235`) | API only | guarded button with confirm | Medium — republish churn | Overview topology card action → `POST /v1/reconcile` with confirm + audit |
| F29 | PCAP capture control | `scripts/debug/capture.sh:1-30`; `research/80-debugger-prep/PLAYBOOK.md:14-15,123-128`; no route | host scripts only | start/stop/list/slice, capability check, retention | **High** — pcap reveals endpoints/timing/`K`; needs root | `GET/POST /v1/lab/pcap`; `Lab.tsx#pcap`; local-only, no download |
| F30 | Error taxonomy / vendor `fm` visibility | gap report row 21; `gate/server.go:430-470` maps all errors to `0x05`; exact vocabulary `dev, internal, net, radius, auth.password, auth.group, auth.forbidden, auth.membership, auth.limit, auth.off, session.timeout, timeout` (`research/84-ajs-496/APK.md:572-579`) | none | `errorClass` per flow/session; raw vendor text redacted | Medium — misdiagnosis | `errorClass` in `/v1/lab/flows` and `/v1/lab/sessions`; classifier later |
| F31 | Gate proxy authentication | Linux `ajiasu` config `proxy-local-server = user:pass@ip:port`; `gate/server.go` is no-auth; `docs/PANEL.md` phase 2; `Leases.tsx` proxy URL has no creds | no-auth loopback gate | per-user credentials, auth status | **High** if the gate is reachable by another local user | phase 2; show `auth:none` warning in `Lab.tsx#sessions`; keep loopback bind |
| F32 | Multi-account coexistence warning | live report “What remains”; F3 single-session rule | none | warning when >1 account on one daemon; per-account isolation status | Medium | overview/diagnostics warning |
| F33 | Health check depth | `ops.go:22-39` always returns `status:"ok"`; no store/Resin/controller check | liveness only | readiness checks | Medium — orchestrator sees healthy while broken | add `GET /readyz` + diagnostics; keep `/healthz` liveness |
| F34 | Prometheus coverage for traffic/latency | `ops.go:57-115` exports counts only | counts | gate bytes, flows, rejects, latency | Medium — no alerting on the data plane | add `ajs_gate_*`/`ajs_flows_*` metrics |
| F35 | User create/delete UI | API `users_keys.go:20,67`; `Users.tsx:202` only list/suspend/allowlist/keys | partial | create/delete user, confirm drain | Medium | user drawer; `POST/DELETE /v1/users` |
| F36 | Per-user usage endpoint | `model.go:131-149` bytes; `docs/PANEL.md:76` documented but absent; `Leases.tsx:107-110` shows requests only; Android `Login.UseCount`/`FreeUsageTimeLeft` (`research/84-ajs-496/APK.md:318-345`) | requests only | bytes, per-day usage, vendor trial counters | Low | `GET /v1/users/{id}/usage?from=&to=&groupBy=day` |
| F37 | Lease usage bytes | `Lease.Bytes` (`model.go:146`); `Leases.tsx` columns stop at requests | requests only | bytes column and totals | Low | add `Bytes` column; `GET /v1/leases/{id}` |
| F38 | Node inventory link to Lab | `Nodes.tsx:214`; `nodes_leases.go:13` | list/detail | link node → session/gate rows | Low | row action “Open in Lab” |
| F39 | Account edit | `PATCH /v1/accounts/{id}` (`accounts.go:53`); create form `Accounts.tsx:641-730`; no edit UI | state only | label/concurrency/regions/expiry edit | Medium — concurrency mismatch causes F3 | edit drawer with concurrency warning |

One additional parity gap belongs to the same gate surface rather than a separate row:
`model.ProtocolHTTP` (`daemon/internal/model/model.go:41-46`) and `proxyUrl()`
(`dashboard/src/lib/format.ts:65-67`) can render `http://127.0.0.1:<gate>`, but
`gate/server.go:605-630` only accepts a SOCKS5 greeting. Either implement an HTTP CONNECT
listener or constrain leases to `socks5` and label the copied URL. Treat this as P1 when an
`http` lease exists (the seeded `node-09`/`node-10` do).

### 3.3 P2 — parity and convenience

| # | Function | Evidence | Current exposure | Missing | Risk | Proposed endpoint / UI |
|---|---|---|---|---|---|---|
| F40 | Account inventory filters/pagination | `GET /v1/accounts?state=` (`accounts.go:14-29`); `Accounts.tsx:543` | client-side search only | server-side filters/cursor | Low | extend `GET /v1/accounts`; UI filter |
| F41 | Log level control | `AJS_LOG_LEVEL` (`main.go:45`); no route | env only | read-only display; guarded runtime change | Low | show in flags; `PATCH /v1/lab/flags` later, restart-required |
| F42 | Health interval / subscription refresh wiring | `Settings.tsx:171-224`; `store/users_keys.go:341-367`; `runtime/panel.go:28-31` fixed 30 s | persisted only | wire to supervisor/panel or mark stored-only | Medium — false expectation | mark “stored only” in UI; wire later |
| F43 | Update check/version parity | `update.go:47-115`; `Settings.tsx:308-383`; Android `AppUpdate` fields and MD5 `UpdateChecksum` (`research/84-ajs-496/APK.md:502-523`); platform update channels (`research/84-ajs-496/TRIAGE.md:305-361`) | read-only check | daemon version binding, sample version matrix, vendor client update-channel reference, checksum algorithm note | Low | `Lab.tsx#update`; keep deploy out-of-band |
| F44 | Diagnostics export bundle | none | none | redacted JSON bundle + sha256 + path | Medium — path/ID leakage | `POST /v1/lab/diagnostics/export`; no browser download |
| F45 | Per-user rotate (documented) | `docs/PANEL.md:75`; no route | lease-level rotate only | user-level rotate with optional IP avoidance | Medium | `POST /v1/users/{id}/rotate`; Resin path |
| F46 | Favorites/recent/hot server lists | Windows `VpnServerFavorite`, `TabFavoriteServers`, `TabRecentlyUsedServers`, `HotTitle`; Android `ServerGroupsFragment` | none | read-only vendor selection state | Low | P2 read-only if the control API exposes it; not first slice |
| F47 | Message center / announcements | Windows `ClientApiMessageCenter/GetAnnounce`; Android `MessageCenterFragment` | none | read-only messages | Low | P2 read-only; not a proxy function |
| F48 | KYC / trial / membership detail | Windows `StateAuthKyc`, `FreeUsageTimeLeft`; Android `action_auth_kyc`, `ShowAntiFraud[]`, `TicketUnreadCount` (`research/84-ajs-496/APK.md:318-345`) | `expiresAt` only | status/trial counters/anti-fraud notice | Low | P2 account detail fields |
| F49 | Up/down lifecycle hooks | Linux `ajiasu` config `up-down`, `use-vpn-dns`, `use-vpn-route` | none | event hooks and DNS/route toggles | Low | P2 daemon feature, not dashboard |

## 4. What the samples add

### 4.1 Sample inventory used in this audit

| Package | Evidence read (static only) | Key feature evidence |
|---|---|---|
| Windows 4.9.6.0 | installer `AJiaSu_4.9.6.0.exe` sha256 `1b1c97f5…d01a1c`; `exe/AJiaSu.exe` sha256 `0fd9d702…8101f6`; `exe/res.fvr` sha256 `9e985bca…35b6af`; `exe/lang.fvr` sha256 `84a95a56…b49aaa`; ZIP members `lang_zh_cn.txt`, `window-main.htm`, `dialog-setting.htm`, `dialog-socks5.htm`, `table-server-list.htm`, `popup-account-info.htm` | protocol modes, network lock, auto-launch, reconnect policy, server list/favorites/recent/latency, HTTP/SOCKS5 dialog, TAP/DNS/system-proxy errors, debug-log directory, client/core version, update |
| Android 4.12.2.0 | APK sha256 `42ce2b51…df3cb`; `apk/classes.dex` sha256 `f66599dd…299b78`; `apktool-out/res/values/strings.xml`; `jadx-out/sources/com/fvcorp/android/fvclient/**`; `apktool.yml` versionName 4.12.2.0; core `20260525-1` (`research/84-ajs-496/TRIAGE.md:362-395`) | specify apps, custom DNS, VPN/routing mode, speed test/ping report, server marks/load, connection management, orders, invitation, message center, advanced routing, KYC, widget, auto-update, failure tokens |
| macOS 4.9.6 | DMG sha256 `206bca27…5110ed`; `dmg/.../AJiaSu.app/Contents/MacOS/AJiaSu` sha256 `33039c96…82066`; `Info.plist` 4.9.6; core `20250708-1` (`research/84-ajs-496/TRIAGE.md:362-395`); `Frameworks/Sparkle.framework`; `MacOS/vpnclientsvc`; `MacOS/proxy_conf_helper`; `Resources/{res.fvr,lang.fvr,product.dat}` | Sparkle update, privileged VPN service, system proxy helper, same Sciter UI |
| Linux 4.3.0.1 | `linux-amd64/ajiasu` sha256 `08fcd866…01c8f6`; `strings` usage/config | `{login\|list\|connect\|logout}`, `protocol = {udp\|tcp\|lwip\|socks5\|http\|proxy}`, `proxy-local-server`, `use-vpn-dns`, `use-vpn-route`, `up-down`, `sys-env-id` |

All hashes above were recomputed in this slice with `sha256sum` on the extracted files
(the installer/APK/DMG container hashes are the ones supplied in the brief and were not
recomputed). Nothing was executed.

### 4.2 Sample-only functions and disposition

These functions are visible in the samples but are either client-side OS integration or
vendor-account commerce. They are listed so the matrix is not mistaken for the whole client
surface; they are not proposed for the daemon dashboard unless marked.

| Sample function | Evidence | Disposition |
|---|---|---|
| Registration / SMS captcha / password reset | Windows `Register*`, `ClientApiRegister/Register`, `ClientApiVerifyCode/*`; Android `RegisterFragment.java`, `ResetPasswordFragment.java` | Client account onboarding; the dashboard creates accounts from existing credentials, so this stays out of scope. |
| Connection management | Android `action_connection_management`; Windows `MenuConnect`/`MenuDisconnect` | Dashboard equivalent is the Sessions tab (F06); no separate client screen. |
| Network lock / kill switch | Windows `lang_zh_cn.txt` `NetworkLock`, `NetworkLockDescription`, `NetworkLockInForce` | Client-side firewall; not a daemon capability. Show only if a future TUN mode exists (P2). |
| Auto-launch / connect on launch | `AutoLaunch`, `ConnectOnLaunch` | Client-side; no daemon equivalent. |
| Reconnect policy (once/continuous/same city/off) | `Reconnect*`, `ReconnectSameCity` | Supervisor currently has no auto-reconnect; P2 daemon feature, not dashboard. |
| TAP driver install/status | `PromptInstallVpnDriver`, `tap/tap6` payload | Client-side Windows; daemon is userland proxy-only. |
| System proxy | `sysproxy.exe`, macOS `proxy_conf_helper` | Client-side; no daemon equivalent. |
| DNS proxy / custom DNS | `PromptVpnDnsProxyFailed`; Android `action_customized_dns_servers`, `prompt_dns_server_format_error` | Client-side resolver; daemon uses system resolver. P2 if a DNS mode is added. |
| Route mode / advanced routing | `ClientApiClientSetting/LoadRoute`, `ClientSettings[AdvancedRoute*]`, `PresetRoute.List` (`research/84-ajs-496/APK.md:318-345`); Android `AdvancedRoutingFragment.java` | Client-side routing; daemon is proxy-only. |
| Split tunneling / specify apps | Android `SpecifyAppsFragment.java`, `SelectAppsFragment.java`, `action_specify_apps` | Client-side Android VPN; out of scope. |
| Message center / announcements | `ClientApiMessageCenter/GetAnnounce`, `GetMessageNum`; Android `MessageCenterFragment.java` | P2 read-only account card, not a proxy function. |
| Purchase / renew / orders | Android `PurchaseFragment.java`, `action_order_records` | Out of scope; never in this dashboard. |
| Invitation / rating / share | `InvitationFragment.java`, `action_share`, `action_go_rating` | Out of scope. |
| KYC | `StateAuthKyc`, `action_auth_kyc` | P2 account status only. |
| Favorites / recent / hot tags | `VpnServerFavorite`, `TabFavoriteServers`, `TabRecentlyUsedServers`, `HotTitle`, `ServerSearchHotKeywords` | P2 read-only catalog annotations; not first slice. |
| Quick connect / auto-match | `ClientApiVpnServer/AutoMatch`; Windows `PromptAutoPick` | P2 read-only recommendation; it is a vendor call, not first slice. |
| Censorship / network health probe | `ClientApiReport/WebsiteCheckReport`; native `0xbf685`/`0xc901b` | **Do not build**; listed only to explain vendor behavior. |
| Widget / notification | `FVAppWidgetProvider.java`, `channel_name_vpn_state` | Client-side; out of scope. |
| Support / anti-fraud / privacy | `ButtonCustomer`, `dialog-anti-fraud.htm`; Android `SupportFragment.java`, `HelpFragment.java`, privacy dialog strings | Client-side help and legal text; not a proxy capability. |
| Settings reporting to vendor | `ClientApiClientSetting/ClientSetting` with `ClientSettings[...]` (`research/84-ajs-496/APK.md:142-174`) | Do not replicate; panel settings are local. If ever sent, it is a separate opt-in with its own audit. |
| Up/down lifecycle scripts | Linux `up-down` config, `FV_*` env contract | P2 daemon hooks; not dashboard. |
| Per-user proxy auth | Linux `proxy-local-server = user:pass@ip:port` | P1/P2 daemon security gap (F31). |

## 5. Lab page plan

### 5.1 Information architecture

Add one page, `/lab`, in a new **Lab** nav section after **System**
(`dashboard/src/lib/nav.tsx:44-48`), lazy-loaded in `dashboard/src/App.tsx:34-41`.
Use the existing `Tabs` primitive (`dashboard/src/components/primitives.tsx:458`), `Card`,
`DataTable`, `Drawer`, `Badge`, `EmptyState`, `ErrorState`, `useAsync`. No new UI dependency.

The page header carries four environment badges: `mock|live`, `login on|off`,
`Resin on|off`, `pin|default TLS`. They come from `/v1/lab/overview`, not from build-time
guesses; the current Settings page hardcodes `0.1.0` and cannot show them.

### 5.2 Tabs

| Tab | Endpoint(s) | Content | Actions | Guard / empty state |
|---|---|---|---|---|
| Overview | `GET /v1/lab/overview` (+ `GET /v1/topology` when configured) | version, uptime, modes, counts, warnings, topology summary | refresh; reconcile (guarded, phase 2) | read-only; warnings explain disabled modes; Resin card shows disabled reason |
| Servers | `GET /v1/accounts/{id}/servers` | server id, title/region, host:port, `sts`, node state, selected/skipped by limit, `stale` | “Login & refresh catalog” reuses `POST /v1/accounts/{id}/actions/login` | vendor call; confirm + 5 min rate limit; empty state “login required” |
| Sessions & gates | `GET /v1/lab/sessions`, `GET /v1/lab/gates` | per node: session health, redacted SID, age, gate port, active/limit, accepted/rejected, ingress/egress bytes, draining | drain/undrain (existing) | read-only; disabled if `AJS_ACCOUNT_LOGIN=0` |
| Egress | `GET /v1/lab/egress` | claimed vs verified, source, match, checkedAt, last 20 checks | “Check egress” → `POST /v1/lab/egress-check` | one node, 10 s timeout, 1/10 s; disabled unless node LIVE |
| Protocol | `GET /v1/lab/profiles` | active profile, supported list, sample baseline, mismatch warnings | none | read-only; “profile change requires restart” |
| Device | `GET /v1/accounts/{id}/device` | hashed `uid/osdid/cuid`, `cseid`, claims, stable, firstSeenAt | raw reveal (P2, off by default) | raw requires `AJS_LAB_REVEAL=1` + confirm + audit |
| Logs | `GET /v1/lab/logs` | time, level, component, account, node, message; level/component/search filters | pause, export diagnostics | read-only; bounded 2000 entries |
| PCAP | `GET /v1/lab/pcap` | name, size, sha256, startedAt, duration, status, path | start/stop | `confirm:"CAPTURE"`, Linux + `tcpdump` + `CAP_NET_RAW`; no download |
| Update | `GET /v1/lab/overview` + `GET /v1/lab/profiles` + `GET /v1/system/update` | daemon version/commit, latest release, update-available, sample version matrix | check | read-only; deploy stays `deploy/update.sh` |
| Flags | `GET /v1/lab/flags` | key, value (or `set:true` for secrets), source, restartRequired | none | read-only; never values for secrets |
| Diagnostics | `GET /v1/lab/diagnostics` | API/store/secrets/pin/Resin/update/controller/logRing/pcap health, state snapshot, warnings | export bundle (P2) | read-only; paths basenames only |

`Sessions & gates` is one tab with two tables because the join key is `nodeId`; a split tab
would force the operator to correlate two lists manually. `Egress` stays separate because it
is the only tab with a guarded network action.

### 5.3 Concrete component plan

```
dashboard/src/pages/Lab.tsx                 new page, tab switch, shared account selector
dashboard/src/components/lab/OverviewTab.tsx
dashboard/src/components/lab/ServersTab.tsx
dashboard/src/components/lab/SessionsTab.tsx
dashboard/src/components/lab/EgressTab.tsx
dashboard/src/components/lab/ProtocolTab.tsx
dashboard/src/components/lab/DeviceTab.tsx
dashboard/src/components/lab/LogsTab.tsx
dashboard/src/components/lab/PcapTab.tsx
dashboard/src/components/lab/UpdateTab.tsx
dashboard/src/components/lab/FlagsTab.tsx
dashboard/src/components/lab/DiagnosticsTab.tsx
dashboard/src/lib/lab.ts                  typed fetchers, redaction-aware formatting
dashboard/src/lib/types.ts                LabOverview, ServerView, SessionView, GateView, ...
dashboard/src/lib/mock.ts                 lab fixture for mock mode
```

Keeping tabs in `components/lab/` avoids a 1,500-line page and lets each tab own its polling.
`Lab.tsx` only owns the active tab, the account selector, and the header badges.

### 5.4 Interaction rules

- **No hidden vendor I/O.** Every read tab is served from memory/store/env. Only the explicit
  “Login & refresh catalog” and “Check egress” actions may contact the vendor, and both show
  a confirm dialog naming the account/node.
- **Polling is per active tab.** Overview 10 s; sessions/gates 5 s; logs 2 s while unpaused;
  servers/device/profiles/flags/diagnostics 15 s or manual. Hidden tabs do not poll.
- **Disabled means explained.** If `AJS_ACCOUNT_LOGIN=0`, Servers/Sessions/Egress show a
  callout with the exact flag and the daemon README line; if `Panel==nil`, topology shows
  “Resin disabled; set AJS_RESIN_ADMIN_TOKEN”. Do not render an empty table with no reason.
- **Redaction is server-side.** The browser never receives a full SID, raw device ID, session
  key, password, `ConnectPassword`, or `Authorization` value. The UI cannot “unredact” what it
  does not have; raw reveal is a separate guarded endpoint.
- **Mock mode stays usable.** `dashboard/src/lib/mock.ts` gets deterministic lab fixtures so
  `/lab` renders with `VITE_USE_MOCK=true` (the default, `api.ts:28`). Live mode uses
  `VITE_API_BASE` and the existing bearer token (`api.ts:63`).
- **`AJS_LAB_*` names in this report are proposed, not existing.** They are listed in
  sections 5–8 and must be added to `daemon/README.md` before implementation.

### 5.5 What the Lab page must not become

- Not a packet-decryption console. PCAP stays on the host; the browser gets metadata only.
- Not a credential viewer. There is no “show password” or “show ConnectPassword” control.
- Not a vendor account manager. Purchase, renew, KYC, invitation and rating stay out.
- Not a client VPN UI. Network lock, split tunneling, TAP, system proxy and DNS modes are
  client-side; the daemon is a SOCKS5 gate.

## 6. Minimal first slice

### 6.1 Scope

The first slice is **read-only telemetry plus honesty fixes**. It performs no vendor call,
no schema migration, and adds no new write endpoint. It delivers the read-only P0
observability gaps in F02–F13, including flags, catalog, gate counters, session health,
egress claim, profile, logs and diagnostics. It also delivers the first P1 read gaps
F15–F17. F01/F14 (pause semantics) are closed by disabling or explaining the control plus a
backend invariant check; F04/F08/F09 are closed for this slice by relabeling/disabling the
fake probe/rotation and labeling the Requests page as seeded/simulated, with the real gate
flow ring, egress check and rotation deferred to phase 2.

Deliver:

1. `GET /v1/lab/overview` — version, uptime, modes, counts, warnings.
2. `GET /v1/lab/flags` — effective config, secret-set flags, provenance.
3. `GET /v1/lab/gates` — gate counters per node.
4. `GET /v1/lab/sessions` — session health, redacted SID, age, keepalive support.
5. `GET /v1/lab/profiles` — active profile + sample baseline.
6. `GET /v1/accounts/{id}/device` — hashed device identity.
7. `GET /v1/lab/logs` — bounded, redacted runtime log ring.
8. `GET /v1/lab/diagnostics` — readiness, state, pin, Resin, update, warnings.
9. `GET /v1/accounts/{id}/servers` — catalog snapshot with `stale` marker.
10. `/lab` page with the eleven tabs above, mock and live.
11. Account drawer on the existing `GET /v1/accounts/{id}` (F16), so membership, expiry,
    last login and usage are visible without a new endpoint.
12. Honesty fixes: relabel `Probe now` to `Touch probe timestamp` (or return
    `verified:false`), disable `Rotate` when `cfg.Panel == nil`, label the Requests page as
    seeded/simulated until the gate flow ring exists, and add the missing Logout button +
    “Pause does not stop gates” note.

### 6.2 File-by-file plan

| File | Change |
|---|---|
| `daemon/internal/model/lab.go` (new) | JSON contracts: `LabOverview`, `LabWarning`, `ServerView`, `SessionView`, `GateView`, `EgressView`, `ProfileView`, `DeviceView`, `FlagView`, `LogEntry`, `DiagnosticsView` |
| `daemon/internal/runtime/lab.go` (new) | `LabView` implementing the read methods over `*NodeController`, `*account.LoginService`, `*store.Store`, effective config and build info |
| `daemon/internal/runtime/nodes.go` | add `Snapshot() []NodeRuntime` returning a copy of `running` (session, gate, port, serverID, egressIP, draining); add an optional `GateState` interface (`State() gate.State`) and type-assert in `Snapshot` rather than widening the existing `Gate` interface, which test fakes implement |
| `daemon/internal/lablog/ring.go` (new) | bounded `slog.Handler` (2000 entries), allowlisted attrs, level/component filters, `Entries(since, level, component, limit)` |
| `daemon/internal/api/lab.go` (new) | `LabReader` interface and handlers for the nine lab routes; `enabled:false` bodies when a dependency is absent |
| `daemon/internal/api/server.go` | add `Lab LabReader` to `Config`; register `/v1/lab/*` in `routes` |
| `daemon/cmd/ajs-accountd/main.go` | build the ring, tee `slog` to stderr+ring, construct `LabView` (nil-safe when login/controller is off) |
| `dashboard/src/pages/Lab.tsx` + `components/lab/*` | new page/tabs |
| `dashboard/src/lib/api.ts` | add typed lab fetchers; reuse `http()` so bearer auth is automatic |
| `dashboard/src/lib/types.ts` | lab types |
| `dashboard/src/lib/nav.tsx`, `App.tsx` | nav item + lazy route |
| `dashboard/src/lib/mock.ts` | deterministic lab fixtures |
| `daemon/internal/api/lab_test.go` (new) | auth required, disabled bodies, redaction, no secret values |
| `dashboard/tests/lab.test.tsx` (new) | tabs render in mock mode; disabled callouts |

No SQLite schema change is required for this slice. `ServerView` is built from
`LoginService.Result` and is marked `stale:true` when the daemon has no in-memory result
(after a restart). Persisting the catalog is a separate P1 change; do not block the first
slice on it.

### 6.3 Endpoint contracts

`GET /v1/lab/overview`:

```json
{
  "enabled": true,
  "generatedAt": "2026-10-24T00:00:00Z",
  "version": "0.1.0",
  "commit": "",
  "uptimeSec": 1234,
  "modes": {"accountLogin": true, "resin": false, "updateCheck": false, "secrets": true, "certPin": "pinned"},
  "counts": {"accounts": 1, "nodes": 1, "liveNodes": 1, "sessions": 1, "gates": 1, "leases": 0},
  "warnings": [
    {"code": "node_limit_gt_1", "severity": "warn", "message": "AJS_NODE_LIMIT=3; the vendor allows one FV session per account"}
  ]
}
```

`GET /v1/lab/gates`:

```json
[{
  "nodeId": "acc-01:srv-07", "accountId": "acc-01", "state": "LIVE",
  "port": 13012, "active": 0, "limit": 1, "accepted": 3, "rejected": 0,
  "ingressBytes": 1234, "egressBytes": 5678, "draining": false,
  "startedAt": "2026-10-24T00:00:00Z"
}]
```

`GET /v1/lab/sessions`:

```json
[{
  "nodeId": "acc-01:srv-07", "accountId": "acc-01", "serverId": "srv-07",
  "sid": "a1b2…(32)", "healthy": true, "ageSec": 42, "lastFlowAt": null,
  "egressClaimed": "203.0.113.10",
  "keepalive": {"supported": false, "ka": null, "kat": null, "kapf": null}
}]
```

`GET /v1/lab/egress`:

```json
[{
  "nodeId": "acc-01:srv-07", "claimed": "203.0.113.10", "verified": "",
  "match": null, "source": "fv-claim", "checkedAt": null
}]
```

`GET /v1/lab/profiles`:

```json
{
  "active": "linux-4.2.3.0-binary",
  "supported": [{"name": "linux-4.2.3.0-binary", "clientVersion": "4.2.3.0", "coreVersion": "20241105-1", "protocolVersion": 2, "oaepHash": "SHA-1", "signingMode": "none", "requestShape": "linux423", "transport": "httpupgrade"}],
  "samples": [
    {"platform": "windows", "version": "4.9.6.0", "core": "20250708-1", "source": "research/40-protocol/PROTOCOL.md:532-542"},
    {"platform": "linux", "version": "4.3.0.1", "core": "20260525-1", "source": "research/30-linux-static/REPORT.md"},
    {"platform": "linux", "version": "4.2.3.0", "core": "20241105-1", "source": "research/40-protocol/PROTOCOL.md:532-542"},
    {"platform": "android", "version": "4.12.2.0", "core": "20260525-1", "source": "apktool.yml versionName; TRIAGE.md:362-395"},
    {"platform": "macos", "version": "4.9.6", "core": "20250708-1", "source": "Info.plist; TRIAGE.md:362-395"}
  ]
}
```

`GET /v1/accounts/{id}/device` (default response):

```json
{
  "accountId": "acc-01", "present": true,
  "hashes": {"uid": "sha256:ab12…", "osdid": "sha256:cd34…", "cuid": "sha256:ef56…"},
  "claims": {"cseid": "linux", "osp": "linux", "osd": "x86_64", "osv": "Linux …", "dim": "lid=…"},
  "stable": true, "firstSeenAt": null, "rawAvailable": false
}
```

`GET /v1/lab/flags`:

```json
{
  "flags": [
    {"key": "AJS_ACCOUNT_LOGIN", "value": "1", "source": "env", "secret": false, "restartRequired": true},
    {"key": "AJS_ADMIN_TOKEN", "set": true, "secret": true, "redacted": true, "restartRequired": true}
  ]
}
```

`GET /v1/lab/logs?limit=200&level=warn&component=gate`:

```json
{
  "entries": [{"ts": "2026-10-24T00:00:00Z", "level": "WARN", "component": "gate", "msg": "gate flow rejected", "accountId": "acc-01", "nodeId": "acc-01:srv-07", "errorClass": "vendor_rejected"}],
  "dropped": 0
}
```

`GET /v1/lab/diagnostics`:

```json
{
  "health": {"api": "ok", "store": "ok", "secrets": "ok", "certPin": "pinned", "resin": "disabled", "update": "disabled", "controller": "running", "logRing": "ok", "pcap": "unavailable"},
  "state": {"path": "state.json", "lastSavedAt": "2026-10-24T00:00:00Z", "sizeBytes": 1234},
  "build": {"version": "0.1.0", "goVersion": "go1.26.7", "commit": ""},
  "warnings": []
}
```

`GET /v1/accounts/{id}/servers`:

```json
{
  "accountId": "acc-01", "fetchedAt": "2026-10-24T00:00:00Z", "stale": false,
  "servers": [{
    "id": "srv-07", "title": "Zhenjiang #1", "region": "cn-zj",
    "host": "…", "port": 8443, "status": "ok",
    "nodeId": "acc-01:srv-07", "nodeState": "LIVE",
    "selected": true, "skippedByLimit": false,
    "memberOnly": null, "fullLoad": false, "blockingMarks": [],
    "hasPingSecret": true, "latencyMs": null
  }]
}
```

`ServerView` maps the known `apifacade.Server` fields (`ID`, `Tit`, `Ctry`, `Loc`, `Sts`,
`Svr`, `Par.FV.P`; `emulator/internal/api/client.go:247-285`); `Vsid` is not exposed.
`fullLoad` is set-membership of `vsid` in `ServerLoad.UserLoadStatus.full[]`; `blockingMarks`
comes from `marks[]` where the matching `MarkFeature.Connectable` is false; `hasPingSecret`
is `par.fv.ps != ""`, and the secret itself is never serialized
(`research/84-ajs-496/APK.md:346-405`). `memberOnly` and `latencyMs` stay `null` unless the
payload or a probe supplies them. Do not invent them from the sample UI.

### 6.4 Acceptance checks

- `go test ./...` and `npm test` pass; new `lab_test.go` asserts every response is
  secret-free by scanning keys and values for `password`, `connectpassword`, `token`,
  `authorization`, `secret`, `sessionkey`, and the literal admin token.
- `GET /v1/lab/flags` with `AJS_ADMIN_TOKEN=supersecret` never contains `supersecret`.
- `GET /v1/accounts/{id}/device` contains no 20/22-char `_uid` value by default; only
  `sha256:` prefixes.
- `GET /v1/lab/sessions` SID is a prefix plus length, never the full `sid`.
- With `AJS_ACCOUNT_LOGIN=0`, Servers/Sessions/Gates return `enabled:false` with a reason,
  not a 500.
- With `AJS_RESIN_ADMIN_TOKEN` empty, Topology returns `enabled:false` and no network call.
- `/lab` renders in mock mode and in live mode against a seeded daemon; no tab triggers a
  vendor request on mount.
- The UI never shows a `Rotate` success when `cfg.Panel == nil`; it shows a disabled reason.
- `Requests.tsx` shows a `simulated` banner until `/v1/lab/flows` exists; no seeded row is
  presented as gate telemetry.

### 6.5 Explicit non-goals for the first slice

- No PCAP start/stop, no egress-check, no diagnostics export (phase 2 guarded actions).
- No raw device reveal, no SID reveal, no packet download.
- No account/profile/flag mutation; restart-required settings stay read-only.
- No new SQLite table; no Resin change; no vendor login change.
- No client-side VPN parity (network lock, split tunneling, TAP, system proxy, DNS modes).

## 7. Guarded actions (phase 2)

All actions below require the existing bearer token (`server.go:307-329`), an explicit
confirmation string in the body, an audit event, and a rate limit. They are not part of the
first slice.

| Action | Endpoint | Guard | Output |
|---|---|---|---|
| Egress check | `POST /v1/lab/egress-check` | one node, LIVE only, allowlisted `https://` echo URL, 10 s timeout, 1/10 s per node | claimed, observed, match, status, latency, checkedAt |
| PCAP start | `POST /v1/lab/pcap` | `confirm:"CAPTURE"`, Linux only, `tcpdump` present + `CAP_NET_RAW`, filter file/args fixed, duration ≤900 s, ring ≤6, dir 0700 | id, path, sha256, startedAt |
| PCAP stop | `POST /v1/lab/pcap/{id}/stop` | id must be running; audit | stoppedAt, size, sha256 |
| Diagnostics export | `POST /v1/lab/diagnostics/export` | `confirm:"EXPORT"`, redacted bundle only, 0600, path under state dir | path, sha256, bytes |
| Reconcile | `POST /v1/reconcile` | `confirm:"RECONCILE"`, panel configured, 1/10 s | topology |

**PCAP is the highest-risk item.** A capture contains vendor endpoints, timing, byte counts,
the 16-byte `K`, and the encrypted `s/n/k` text; it does not contain the RSA-wrapped session
key, but it is still sensitive. Therefore: no browser download in phase 2, metadata only,
`AJS_LAB_PCAP_DOWNLOAD=1` required for a future download endpoint, and every file has a
sha256 and a TTL.

## 8. Redaction and security rules

1. **Never serialized:** account password, `ConnectPassword`, `LoginToken`, FV session key,
   `k=` ciphertext, `par.fv.ps` ping secret, `Authorization`, `AJS_ADMIN_TOKEN`,
   `AJS_MASTER_KEY`, `AJS_RESIN_ADMIN_TOKEN`, full request bodies.
2. **Device identity:** default `sha256(value)[:16]` with a `sha256:` prefix for `uid`,
   `osdid`, `cuid`; `cseid`/`osp`/`osd`/`osv`/`dim` are claims, but `dim` may contain a
   device fingerprint, so show it only in the raw-reveal path. Raw reveal requires
   `AJS_LAB_REVEAL=1`, admin token, `confirm:"REVEAL"`, loopback client, and an audit event.
3. **SID:** show `first4…(len)`; full SID only in the raw-reveal path, never in logs.
4. **Logs:** the ring stores an allowlist of attributes (`account_id`, `node_id`,
   `server_id`, `error_class`, `duration_ms`, `status`, `bytes`), not arbitrary `slog` attrs.
   Drop keys matching `pass|secret|token|authorization|cookie|key` unless explicitly allowed.
5. **Targets:** `/v1/lab/flows` returns `targetHost` and `targetHash` by default; full target
   (including path/query) requires `reveal=1` and is audited.
6. **Flags:** secret keys return `{set:true, redacted:true}`; non-secret keys return value,
   source (`env`/`default`/`flag`) and `restartRequired`.
7. **Paths:** diagnostics returns basenames (`state.json`, `secrets.json`, `cap/`), not full
   home directories.
8. **Audit:** every lab action writes `actor:"operator"`, `action:"lab.*"`, target id and a
   result; no secret or target query in the audit detail.
9. **Do not ingest `ClientApiReport/Report`.** That vendor call carries SSID/BSSID, the WLAN
   list, intranet IP/netmask/gateway/DHCP/DNS, SIM/operator and country, root status,
   manufacturer/model/resolution, and the installed-app list with signatures
   (`research/84-ajs-496/APK.md:524-544`). The Lab page needs none of it; rendering or
   storing it would turn a proxy panel into a device-surveillance surface.

## 9. Evidence index

Commands run in this slice (all read-only; no network). Paths are relative to
`C:/Users/dwgx1/.dwgx/tmp/ajs-re-20261024a/`; the Windows `strings`/`zipfile`
commands were run from its `exe/` child.

```text
sha256sum exe/AJiaSu.exe exe/res.fvr exe/lang.fvr apk/classes.dex linux-amd64/ajiasu
  -> 0fd9d702…8101f6, 9e985bca…35b6af, 84a95a56…b49aaa, f66599dd…299b78, 08fcd866…01c8f6
python -c "zipfile.ZipFile('res.fvr').namelist()"        # 138 entries (137 files), 18 htm, 6 js
python -c "zipfile.ZipFile('lang.fvr').read('lang_zh_cn.txt')"  # localization keys
strings -n 6 exe/AJiaSu.exe | grep -E 'ClientApi|ClientSettings|BatchLog|AppUpdate'
strings -n 6 linux-amd64/ajiasu | grep -E 'login\|list\|connect\|protocol =|proxy-local-server'
grep -o '<string name="[^"]*"' apktool-out/res/values/strings.xml
find jadx-out/sources/com/fvcorp/android/fvclient -name '*.java'
python -c "plistlib.load(open('Info.plist','rb'))"       # 4.9.6 / 4.9.6.0
grep -n 'v1/\|HandleFunc' daemon/internal/api/server.go
grep -n 'func (s \*Server) handle' daemon/internal/api/*.go
grep -n 'USE_MOCK\|async .*(' dashboard/src/lib/api.ts
grep -n 'Route path' dashboard/src/App.tsx
```

Source anchors used repeatedly:

- Dashboard routes: `dashboard/src/App.tsx:34-41`; nav: `dashboard/src/lib/nav.tsx:22-48`.
- API routes: `daemon/internal/api/server.go:172-213`; auth: `:307-329`.
- Models: `daemon/internal/model/model.go:90-247,268-322`.
- Gate state: `daemon/internal/gate/server.go:107-126,254-266`.
- Node runtime: `daemon/internal/runtime/nodes.go:86-150,215-330,610-654,717-728`.
- Session: `daemon/internal/account/session.go:14-72,92-102,201-227`.
- Device: `daemon/internal/store/device.go:16-90`.
- Login result: `daemon/internal/account/login_service.go:93,215-264`.
- Fake probe/rotate: `daemon/internal/store/nodes_leases.go:227-267`.
- Settings: `daemon/internal/model/model.go:238-247,307-315`; `dashboard/src/pages/Settings.tsx:154-224`.
- Documented contract: `docs/PANEL.md:51-88`.
- Protocol version matrix: `research/40-protocol/PROTOCOL.md:532-542`.
- Live facts: `research/82-live-20261023/REPORT.md` F1-F5, “What remains”.
- Protocol gaps: `research/81-protocol-gaps/REPORT.md` rows 6, 19, 21, 22, 25, 26.
- Sibling lane reports (read-only cross-check, read 2026-10-24):
  `research/84-ajs-496/APK.md:301-418,489-594`
  (feature flags, `LoginSync` schema, node marks/load, ping report, update, failure tokens)
  and `research/84-ajs-496/TRIAGE.md:362-428` (version/core matrix, update metadata).
- Capture tooling: `scripts/debug/capture.sh`, `scripts/debug/slice_pcap.py`,
  `research/80-debugger-prep/PLAYBOOK.md:14-15,123-128`.

## 10. Limitations

- **No live run.** The dashboard was not started; all page behavior is read from source.
  `[UNVERIFIED]` for runtime rendering, polling behavior and browser errors.
- **No sample execution.** Windows/APK/macOS/Linux binaries were only statically inspected.
  Feature lists are from strings/resources/classes, not from running the clients.
- **No vendor call.** The live report’s facts are cited, not re-observed. Any statement about
  vendor acceptance, rate limits or anti-abuse is `[UNVERIFIED]` here.
- **Catalog field uncertainty.** Full-load is evidenced statically by
  `ServerLoad.UserLoadStatus.full[]` (`research/84-ajs-496/APK.md:346-405`) but not live-verified; the “member only”
  badge has no confirmed catalog field, so `memberOnly` stays `null` unless a response supplies it.
- **Android/macOS core versions** are taken from the sibling static triage
  (`research/84-ajs-496/TRIAGE.md:362-395`), not re-derived here; the APK versionName is
  4.12.2.0.
- **No code edits.** This report proposes files/endpoints; it does not implement them.

## 11. Recommendation

Ship the read-only Lab slice before any new vendor-facing action. It costs one API file,
one runtime view, one log ring, one page and no schema change, and it turns the dashboard
from a panel that repeats model fields into an instrument that can answer: which account,
which server, which session, which gate, which bytes, which profile, which device, and what
did the daemon log? Only after those answers are trustworthy should the guarded egress check
and PCAP controls be added.
