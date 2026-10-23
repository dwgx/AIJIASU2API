# FEATURES-VULNS — sample findings → dashboard feature and exposure matrix

Status: read-only synthesis, 2026-10-24. Slice `84-ajs-496/FEATURES-VULNS`.
Sources: `research/84-ajs-496/{TRIAGE,APK,WINMAC,LINUX,DASHBOARD-GAPS}.md`,
`research/82-live-20261023/REPORT.md`, `research/81-protocol-gaps/REPORT.md`,
`research/10-osint/REPORT.md`, `research/40-protocol/PROTOCOL.md`.
No sample executed, no vendor host contacted, no credential used, no exploit code.
No file outside this one was written.

**Question, one line:** which findings in the new 4.9.6.0 / 4.3.0.1 / 4.12.2.0 samples
deserve dashboard surface, and which must stay out?

**DONE WHEN:** ≥15 findings with severity, precondition, impact, enabling feature and
exposure verdict; a top-5 risk table; a dashboard feature map. Satisfied below
(20 findings, 5 ranked risks, 18 mapped features).

## Answer

1. **The samples are rich in client features and poor in transport trust.** Every platform
   disables or defeats control-plane certificate verification, and the API host list is
   runtime-overridable. That combination — not any single bug — is the top risk and the
   reason the dashboard must show TLS/host posture rather than hide it.
2. **The update paths are the second theme.** Android downloads an APK and asks the user to
   install it, verified by an MD5 from the same control plane, signed by a 2017 self-signed
   key. Windows downloads and executes an installer after a checksum whose algorithm is not
   even named in the binary. macOS delegates to Sparkle with no EdDSA key. Linux alone does
   not self-update, and that is the safest design of the four.
3. **Privilege is the third theme.** macOS installs a setuid-root helper copied from
   shadowsocks and runs a root VPN service that receives account credentials over a local
   socket. Windows runs `requireAdministrator`, installs a TAP driver, edits the firewall and
   bounces a service. Android ships 17 exported components and cleartext traffic enabled.
4. **Most sample features should be visible read-only, few should be actionable.** The
   dashboard can expose the server catalog, marks/load, session health, gate counters, device
   identity hashes, protocol profile, update metadata and flags without touching a vendor
   host. It must not expose ping secrets, session keys, raw device IDs, or the vendor
   telemetry payloads.
5. **Operational cap is a feature, not a footnote.** The vendor allows one live FV session
   per account; `AJS_NODE_LIMIT` defaults to 1. `account#N` (`UserName#ClientSysEnvId`) is the
   vendor's per-device identity mechanism and the thing that makes a second session kick the
   first.

## 0. Reading key

**Severity** is for *this project* (lab daemon + dashboard + operator), not a CVSS score for
the vendor:

| Level | Meaning |
|---|---|
| Critical | Network-position attacker or any local user gets credentials, code execution, or full control-plane impersonation with no special precondition. |
| High | Requires a plausible precondition (local user, malicious app, control-plane compromise, expired cert) and yields credential/session theft, code execution, or guaranteed outage. |
| Medium | Constrained precondition and/or partial impact (metadata disclosure, single-platform risk, config error). |
| Low | Hygiene, information disclosure, or supply-chain note. |
| Info | Parity/design observation with no direct security impact. |

**Expose** is the dashboard decision:

- **yes** — safe to render read-only from daemon memory/store/env, redacted per §4.
- **later** — useful but needs a guarded action or a later phase (phase 2 in DASHBOARD-GAPS §7).
- **no** — never expose in the dashboard; may still be documented here.

**Scope discipline.** Findings are about the *vendor clients* unless the "Affects" column
says daemon/dashboard. A vendor-client weakness is not automatically our vulnerability; it
becomes ours only if the daemon copies the behaviour or the dashboard models it as trusted.

## 1. Top-5 risk table

| # | Risk | Findings | Severity | Precondition | Impact | Why it ranks here |
|---|---|---|---|---|---|---|
| R1 | **Control-plane impersonation chain**: TLS verification off + runtime host refresh | V01, V02, V13 | Critical | Network position (MITM/DNS) or a hostile control response; no user action | Login credentials, `LoginToken`/`ConnectPassword`, full `LoginSync` (server tree, ping secrets, marks) and update metadata are readable/forgeable; attacker can inject API hosts and a malicious catalog | The two weaknesses compose: host refresh gives the attacker a target, TLS-off makes the target trusted. Every platform is affected. |
| R2 | **Android self-update → device compromise** | V05, V06, V11 | Critical | Malicious app on device (exported component), or control-plane/download MITM; user taps "install" | Arbitrary APK delivered by the vendor's own update path; MD5 is the only integrity check and comes from the same channel; signer is a 2017 self-signed key also used as the update trust anchor | Highest impact per user (full device), and the update path is explicitly built to install packages. |
| R3 | **macOS local privilege / proxy interception** | V03, V04 | High | Any local unprivileged user on the Mac | Setuid-root `proxy_conf_helper` is world-executable and rewrites system SOCKS proxy; root `vpnclientsvc` receives account credentials over a local socket; the app strips quarantine recursively | Local user → system-wide traffic interception; a helper parsing bug is a root primitive. |
| R4 | **Windows elevated update path** | V08, V09, V19 | High | Control-plane compromise/MITM, or write access to the install directory | Installer is downloaded and executed by a `requireAdministrator` process after a checksum-only check; no `WinVerifyTrust`; unsigned 2016 TAP driver is installed on demand | Admin-level code execution with the same trust weaknesses as R1. |
| R5 | **Single-session self-DOS and identity churn** | V15, V16 | High (operational) | `AJS_NODE_LIMIT>1`, a second login, or unstable device identity | Vendor tears down the first FV session (`fm=disconnected by another connection`); extra gates answer SOCKS5 `0x05`; identity churn looks like a new device to anti-abuse | Already observed live; the failure mode is silent to the operator unless the dashboard shows it. |

## 2. Matrix — 20 findings

Evidence anchors are `file:line` in decompiled/source trees or `file offset`/VA in binaries.
`[INFERENCE]` and `[UNVERIFIED]` are carried from the source reports where they apply.

| ID | Finding | Affects / platform | Severity | Attack precondition | Impact | Evidence | Dashboard feature it enables | Expose |
|---|---|---|---|---|---|---|---|---|
| V01 | **Control-plane TLS verification is off on every client.** Linux sets `CURLOPT_SSL_VERIFYPEER=0` and `CURLOPT_SSL_VERIFYHOST=0`; desktop carries “Certificate verification was skipped”; Android has no CA bundle/pin and no `X509TrustManager` bridge; live capture shows a self-signed `CN=localhost` leaf accepted on all four hosts. | All clients; daemon (pin) | Critical | Network position (MITM/DNS) or hostile access point | Credentials, session tokens, catalog, ping secrets and update metadata are exposed; any certificate is accepted | `LINUX.md` §6.1 (amd64 `0x439d95`/`0x439dbf`, x86 `0x807cc81`/`0x807cca5`, aarch64 `0x435f44`/`0x435f60`); `WINMAC.md` §5.3 (Win `0x3592dc`, mac `0x20edc3`); `APK.md` §6.5; `82/REPORT.md` F1 (leaf SHA-256 `E8:83:F0:B9:…:3A:89`) | Protocol tab: TLS mode `default`/`pinned`, live leaf fingerprint, warning that the shipping client does not verify; Flags tab `AJS_CONTROL_CERT_SHA256` set/unset | yes (posture + public fingerprint only) |
| V02 | **API host list is runtime-overridable and refreshed from the control plane.** Desktop reads four bases from `product.dat`; Linux/Android hold the same four in plaintext; `ApiUrls`/`FetchedUrlHosts` merge server-supplied hosts; Android `CustomLoginUrl` replaces the base entirely; `UrlWeb`/`UrlMobile` are re-read every login. | All clients | High | Hostile control response, local config tamper, or MITM (see V01) | All subsequent API traffic can be redirected to an attacker-chosen host; catalog and update metadata become attacker-controlled | `WINMAC.md` §4.1–4.2, §5.1; `LINUX.md` §4.1 rows 33–35, §4.2, §9.5; `APK.md` §4.1, §4.3 (runtime override), §8.1 | Protocol tab: builtin vs fetched hosts, `LastUrlHost`, `SyncVersion`, provenance and “server-pushed” marker | yes (hosts are public wire facts; never tokens) |
| V03 | **macOS installs a setuid-root proxy helper.** `install_helper.sh` (shadowsocks-derived, header intact) copies `proxy_conf_helper` to `/Library/Application Support/AJiaSu/`, `chown root:admin`, `chmod +s`. CLI accepts `-m global` or `-m off`, `-t AJiaSu` or `-t FlyVPN`, `-p`, `-l`, `-r`, `-s`, `-x`, `-n`; writes `kCFNetworkProxiesSOCKS*`. | Vendor macOS client; host running it | High | Any local unprivileged user | System-wide SOCKS proxy change (traffic interception); a parsing/path bug in a setuid binary is a local root primitive; one helper serves two brands | `TRIAGE.md` §8.1; `WINMAC.md` §7.2, §9.2; helper SHA-256 `1ba0f607…a3d7d` (`TRIAGE.md` §2.2) | Diagnostics: host posture card — helper path, setuid bit, hash, version, “same binary serves FlyVPN” | later (read-only detection; no remote enable/disable) |
| V04 | **macOS root VPN service receives credentials over a local socket; app strips quarantine.** `vpnclientsvc` runs as root, requires euid 0, and is fed `userName`, `password`/`ConnectPassword`, `clientSite`, `clientVersion`, `clientUniqueId`, `osDeviceIdMap`, routes over `/tmp/VpnClientCtl-<id>.sock`. The app runs `xattr -d -r com.apple.quarantine` on itself/helper. | Vendor macOS client | High | Local user able to connect to/observe the socket; or malicious bundle | Credential exposure on a local IPC path; Gatekeeper quarantine bypass weakens first-run trust | `WINMAC.md` §7.2 (socket paths `0x1f37a7`/`0x1f37ba`, svc `0x1517a2-0x15182a`), §9.2 (`0x1f2bf7`, `0x1f2c24`); `[INFERENCE]` on cleartext from field names | Diagnostics: “root helper present”, socket path pattern, quarantine-strip observed; no credential content | no |
| V05 | **Android self-update: MD5 + self-signed key + installer intent.** `UpdateActivity` downloads an APK, passes `UpdateChecksum` as an intent extra, verifies with `MessageDigest.getInstance("MD5")`, then hands it to the system installer (`REQUEST_INSTALL_PACKAGES`). APK signer is self-signed `CN=Shawn`, valid 2017→2117, v1+v2 only; that key is also the update trust anchor for the website channel. | Vendor Android client; any user installing the APK | Critical | Control-plane/download MITM (V01/V02) or compromised download host; user confirms install | Malicious APK with the app's permissions, including VPN service and installed-app visibility; MD5 is not a security boundary; no v3/v4 rotation | `APK.md` §12.2–12.5; `f01.java:83,574` (`MD5`), `UpdateActivity.java:308,345`; `TRIAGE.md` §4.3 (signer, schemes) | Update tab: per-platform update channel, integrity algorithm (`md5`), signer identity, scheme versions, “user-mediated install” flag | yes (metadata only; no download/deploy) |
| V06 | **Android exported surface + cleartext.** `usesCleartextTraffic=true`; 17 exported components; `REQUEST_INSTALL_PACKAGES`, `QUERY_ALL_PACKAGES`, `FOREGROUND_SERVICE_SYSTEM_EXEMPTED`; WebView JS bridge routes include `/innerWeb` (arbitrary same-origin navigation), `/pay`, `/reLogin`, `/signOut`. | Vendor Android client | High | Malicious app on the same device (exported activity/intent) or hostile web content in the account origin | Update URL/intent injection, navigation/phishing inside the trusted shell, payment-result POST to a page-supplied URL (`PaymentResultSubmitActivity.java:83-85`) | `APK.md` §3.2–3.3, §5, §4.2 E19; `TRIAGE.md` §8.3 | Device/account detail: platform risk badge (“cleartext allowed”, “exports 17 components”, “installs packages”) | later (informational badge) |
| V07 | **Android uploads device-surveillance telemetry.** `ClientApiReport/Report` can attach SSID/BSSID/WLAN list, intranet IP/netmask/gateway/DHCP/DNS, SIM/operator/country, root status, manufacturer/model/resolution, and **every installed app with signing certificate**. `ClientApiClientSetting/ClientSetting` reports local UI settings; `WebsiteCheckReport` reports censorship-probe results. | Vendor Android client | Medium (privacy/legal) | Normal app use; no attacker needed | Device and app inventory leaves the device; replicating it would turn the panel into a surveillance surface and create legal exposure | `APK.md` §13 (anchors `C1689vk.java:226,242,270`, `:97`); `DASHBOARD-GAPS.md` §8.9 | None. Show only the boundary in the Lab docs; no ingestion, no storage, no rendering | no |
| V08 | **Windows runs elevated and owns OS plumbing.** Manifest `requireAdministrator`; installs TAP driver on demand (`tapinstall.exe` + `OemVista.inf` + `tap0901`), edits routes, rewrites DNS via `netsh`, stops/starts `Dnscache`, adds a firewall allow rule via `INetFwPolicy2`, enables `SeDebugPrivilege`, enumerates/terminates processes, registers Task Scheduler auto-start. | Vendor Windows client; host running it | High | Local admin (by design) or control of the install/update path | Elevated code, kernel driver install, firewall/route/DNS changes, service manipulation, process termination | `WINMAC.md` §7.1, §9.1; `TRIAGE.md` §8.2 | Diagnostics: host posture — elevation required, TAP present, firewall rule, DNS service bounced; “expected, not a finding” note | later (read-only host posture) |
| V09 | **Windows update integrity is checksum-only and then executes.** Fields `LatestVersion`, `UpdateOptional`, `UpdateChecksum`, `UrlWeb`, `/downloads/`, arch-keyed package; flow downloads, checksum-verifies, runs installer. No `WinVerifyTrust` import, no self-signature check; checksum algorithm not named. | Vendor Windows client | High | Control-plane compromise/MITM or write access to install dir | Admin-level installer execution from a channel that is not TLS-verified; checksum travels with the payload | `WINMAC.md` §8.1, §9.1 (no `WinVerifyTrust`); `TRIAGE.md` §6.1 | Update tab: update mode, checksum field present, “executes downloaded installer”, signature policy `authenticode-only` | yes (policy metadata) |
| V10 | **macOS Sparkle update trust is Apple-signing-only.** Sparkle 2.3.0, feed URL supplied at runtime, **no** `SUFeedURL`/`SUPublicEDKey`/`SUPublicDSAKey` in the bundle; Sparkle logs “Serving updates without an EdDSA key … is deprecated”; Developer ID leaf expires **2026-10-30**. | Vendor macOS client | High | Control-plane/feed compromise; after expiry, re-signing is impossible | Update substitution constrained only by Apple code signing; after 2026-10-30 new builds cannot be re-signed and Gatekeeper assessment degrades | `WINMAC.md` §8.2, §9.3; `TRIAGE.md` §4.2 (leaf validity, verified below) | Update tab: update framework, EdDSA key `absent`, feed URL `runtime`, cert `notAfter` | yes (metadata) |
| V11 | **Signing-key hygiene differs sharply by platform.** Windows: GlobalSign EV, valid to 2027-03-14. macOS: Developer ID `N82WKSN6R7`, expires 2026-10-30. Android: self-signed `CN=Shawn` 2017, v1+v2, also the update anchor. Linux: unsigned. | All clients | Medium | Distribution-channel substitution or key compromise | Trust anchors are uneven; Android has the weakest anchor and the most dangerous update action | `TRIAGE.md` §4; `WINMAC.md` §9.3; `APK.md` §2 | Update tab: signing matrix per platform (issuer, validity, schemes, unsigned) | yes (cert metadata is public) |
| V12 | **Third-party secrets ship in the APK in plaintext.** `assets/ShareSDK.xml` carries AppId/AppKey/AppSecret for WeChat, QQ, QZone, Weibo and Kuaishou (five distinct credentials). | Vendor Android supply chain | Low | Anyone with the APK | Third-party app-secret abuse (share/pay SDK identity), not vendor account access | `APK.md` §3.3; `TRIAGE.md` §8.4 (values deliberately not reproduced) | None in UI; supply-chain note in research only | no (never reproduce values) |
| V13 | **`product.dat` is obfuscation, not encryption.** 818 B, AES-128-CTR with key `MD5("[CONFIG]")` and a zero IV, identical on Windows and macOS; decrypts to the four API bases plus website and product flags. Linux/Android carry the same hosts in plaintext. | Vendor desktop; documentation | Low | Binary access | Host list and product flags are recoverable; the blob provides no confidentiality against a local analyst | `WINMAC.md` §4.1; re-verified below (plaintext SHA-256 `2b618010…1183e`) | Protocol tab: “host list source: builtin (obfuscated) / fetched / override” | yes (hosts only) |
| V14 | **Proxy authentication exists in Linux 4.3.0.1 but not in the daemon gate.** Client adds SOCKS5 user/pass and HTTP `Proxy-Authorization: Basic`, refuses a non-loopback bind without auth, and rejects weak passwords (<10 chars or missing letters+digits). Daemon `gate/server.go` accepts only no-auth SOCKS5; `http://` lease URLs can be rendered but not served. | Client (new), daemon (gap) | High (daemon) | Any other local user/process that can reach the gate port; or an operator exposing the listener | Unauthorized egress through a paid account, quota burn, attribution confusion, open-proxy risk | `LINUX.md` §9.4 (`0x290c18`, `0x2909a8`, `0x291978`, `0x2919ab`); `DASHBOARD-GAPS.md` F31, §3.2 note (`gate/server.go:605-630`) | Sessions tab: `auth:none` warning, bind scope, protocol label; phase 2 per-user gate credentials | yes (warning); later (credentials) |
| V15 | **`account#N` per-device identity.** The tunnel username is `<UserName>#<ClientSysEnvId>` when a sys-env id is set; the vendor added `account#N` login in 4.9.0.5 for per-device connection management. Linux exposes `sys-env-id` as a config key; Windows/macOS carry a 12-field device fingerprint; Android has the same 12-field block plus a native `appClientUniqueId`. | All clients; daemon | Medium | Reused/regenerated identity, or two daemons on one account | Identity churn looks like a new device to anti-abuse; a shared/stale sys-env id makes sessions indistinguishable or causes kicks | `APK.md` §6.3 (`FVService.java:554-559`, verified below), §7; `LINUX.md` §7 (`sys-env-id` `0x28fd9e`); `WINMAC.md` §10.4; `10-osint/REPORT.md` C19 | Device tab: hashed `uid/osdid/cuid`, `cseid`/`osp`/`osd` claims, `stable`, `firstSeenAt`, sys-env-id set/absent (never raw) | yes (hashed only) |
| V16 | **One live FV session per account.** With `AJS_NODE_LIMIT=3` the first two gates answered SOCKS5 `0x05` and the daemon logged `fm=disconnected by another connection`; the third worked. `AJS_NODE_LIMIT` now defaults to 1. OSINT: concurrency is a purchased integer 1–2000 and exceeding it silently kicks an older device. | Daemon + vendor account | High (operational) | `AJS_NODE_LIMIT>1`, a second login, or a second host using the same account | Self-inflicted outage: the account's working session is torn down; extra nodes are dead weight; operator sees only “gate refused” | `82/REPORT.md` F3, row 11; `daemon/cmd/ajs-accountd/main.go:156,195,644-646` (verified below); `10-osint/REPORT.md` C21 | Overview: node-limit warning; Accounts: concurrency guard; Sessions: one-session explanation and kick history | yes |
| V17 | **Request signing changed in Linux 4.3.0.1.** 4.2.3.0 signs with MD5 → 32 hex chars; 4.3.0.1 uses the desktop dual-hash (seeds `0xDEADBEEF`/`0xCAFEBABE`, ×65599, pipe separators) → 16 hex chars. Desktop has the same machinery; the signing secret is `[UNKNOWN]` and is not the OpenAJS colon-hex value. Live 4.2.3.0 was accepted unsigned. | Client protocol; daemon profile | Medium | A dashboard speaking 4.3.0.1 with the wrong signer | Auth failure misattributed to credentials; the existing emulator signer may not match 4.3.0.1 | `LINUX.md` §4.4 (`0x4435c5`, `0x4436e5`; 4.2.3.0 `0x44486c`→`0x4b30df`), §9.6; `WINMAC.md` §5.2; `82/REPORT.md` "What remains" (B2) | Protocol tab: `signingMode` (`none`/`md5-32`/`dualhash-16`), secret `unknown`, profile-to-signer mapping | yes (metadata; never the secret) |
| V18 | **Daemon TLS pin is load-bearing, not optional.** Default Go verification rejects the live leaf; `AJS_CONTROL_CERT_SHA256` → `PinnedHTTPClient` accepts exactly that fingerprint and fails closed with `ErrCertPin`. An unset pin fails every control call against that leaf. | Daemon | Medium (availability) | Operator forgets the pin, or vendor rotates the leaf | Login unavailable against the observed leaf; a pin update is needed with no UI signal | `82/REPORT.md` F1; `daemon/internal/account/tls.go:20-48`; `DASHBOARD-GAPS.md` F24 | Flags/Diagnostics: `certPin` mode (pinned or default), fingerprint, `ErrCertPin` in logs; Overview warning | yes |
| V19 | **Windows ships an old unsigned TAP driver.** `tap/tap/{amd64,i386}/tap0901.sys` are 2016 builds with **no ASLR, no DEP**; PE unsigned, only CAT-signed by OpenVPN. The `tap6` set is the newer NDIS 6 variant. The `arm64` sysproxy directory actually contains a 32-bit ARMNT binary. | Vendor Windows client | Medium | Local admin/driver load; kernel attack surface | Old kernel driver in the tunnel path; arch-dir mismatch can mis-select a helper | `WINMAC.md` §2.1; `TRIAGE.md` §2.1 (U7) | Diagnostics: driver version/arch inventory, “unsigned / no ASLR” badge | later (host posture) |
| V20 | **Update-channel asymmetry is the safest design signal.** Windows: API-driven, `/downloads/`, checksum, executes installer. macOS: Sparkle, runtime feed, Apple-signing-only. Android: in-app APK + installer intent, MD5. Linux: **notify-only**, no URL/checksum/exec path. | All clients | Info | — | Linux cannot be pushed a binary by the control plane; the other three can | `LINUX.md` §8 (negative evidence); `WINMAC.md` §8; `APK.md` §12; `TRIAGE.md` §6 | Update tab: per-platform matrix with “self-update: yes/no” and “integrity: md5/checksum/apple-signature/none” | yes |

### 2.1 Chain view (why the rows are not independent)

```
V02 host refresh ──► attacker-chosen API base
        │
        ▼
V01 TLS off ───────► any cert accepted ──► credentials + LoginSync + ping secrets
        │
        ├──► V05/V09/V10 update metadata + payload ──► code execution (Android/Windows)
        └──► V16 second session / V15 identity churn ──► account kicks, anti-abuse
V14 gate no-auth ──► local user egress (daemon side, independent of vendor)
```

## 3. Dashboard feature map

Sources of truth are daemon memory/store/env (DASHBOARD-GAPS §2.3) plus the sample field
names. “Tab” refers to the planned `/lab` page in DASHBOARD-GAPS §5.2. No feature below
requires a vendor call on page load.

| # | Dashboard capability | Source of truth | Sample anchor | Tab | Expose | Notes |
|---|---|---|---|---|---|---|
| D01 | Server catalog: id, title, region, `sts`, host:port, `vsid` (internal), selected/skipped by limit | `LoginService.Result.Servers` | `APK.md` §10.1; `C0023am.java:472-566` | Servers | yes | `vsid` stays internal; `stale:true` after restart until re-login |
| D02 | Per-node ping secret presence (never the value) | `Servers.<id>.par.fv.ps` | `APK.md` §10.1 (`:495`) | Servers | yes | `hasPingSecret:bool` only; `ps` is a capability, not a display field |
| D03 | Connectability = `sts=="ok"` ∧ `ps!=""` ∧ no blocking mark | node schema + `MarkFeature` | `APK.md` §10.1 (`:497,534`) | Servers | yes | Three conditions, not one; show which failed |
| D04 | Marks/badges and blocking marks | `marks[]` × `MarkFeature` | `APK.md` §8.2, §10.1 (`:262-285,525-542`) | Servers | yes | `Connectable=false` marks block connect; `DisplayOrder` sorts |
| D05 | “Full load” badge | `ServerLoad.UserLoadStatus.full[]` by `vsid` | `APK.md` §8.3, §10.1 (`:302-322`) | Servers | yes | Set membership; `[UNVERIFIED]` live |
| D06 | Latency / UDP reachability | native `FVPingManager` 8-field `;` report; `udpResult` | `APK.md` §11.5 | Servers / Egress | later | Needs a real probe (F04/F18); never fake it |
| D07 | Session health: redacted SID, age, `Healthy`, keepalive support | `FlowSession` + `nodeGate` | `APK.md` §10.2; `daemon/internal/account/session.go:92-102` | Sessions & gates | yes | `ka/kat/kapf` currently `supported:false` (gap row 19) |
| D08 | Gate counters: accepted/rejected, active/limit, ingress/egress bytes, draining | `gate.State` | `daemon/internal/gate/server.go:107-126,254-266` | Sessions & gates | yes | The only real traffic truth; wire before adding more UI |
| D09 | Egress claimed vs verified | FV `sip/cip/ip` claim + echo check | `daemon/internal/runtime/nodes.go:717-728`; `82/REPORT.md` F4 | Egress | yes (claim) / later (verify action) | Label source `fv-claim` vs `echo`; never present a claim as verified |
| D10 | Protocol profile: `_v`, `cv`, `protocolVersion`, `implType`, OAEP hash, signing mode, transport | `VersionProfile` + samples | `LINUX.md` §4.4, §9.6; `APK.md` §10.2; `WINMAC.md` §5.2 | Protocol | yes | One signer for desktop+Linux dual-hash; `[UNVERIFIED]` field order |
| D11 | Host-list provenance: builtin vs fetched vs override, `LastUrlHost`, `SyncVersion` | `ApiUrls`/`FetchedUrlHosts`/`LastUrlHost` | `WINMAC.md` §4.1, §5.1; `LINUX.md` §4.2 | Protocol | yes | Server-pushed hosts are data, not constants; show “refreshed at” |
| D12 | TLS posture: mode, pin fingerprint, last cert error | daemon config + `account/tls.go` | `82/REPORT.md` F1 | Flags / Diagnostics | yes | Fingerprint is public; never a bypass toggle |
| D13 | Device identity (hashed): `uid/osdid/cuid`, claims `cseid/osp/osd/osv/dim`, stable, firstSeen | `store.DeviceIdentity` | `APK.md` §7; `WINMAC.md` §10.4 | Device | yes (hashed) | Raw reveal only via guarded `AJS_LAB_REVEAL` path (phase 2) |
| D14 | `account#N` / sys-env-id status | tunnel username construction + `sys-env-id` key | `APK.md` §6.3 (verified below); `LINUX.md` §7 | Device | yes (set/absent) | Never render the composed username; it is an identity |
| D15 | One-session / node-limit warning and concurrency guard | `AJS_NODE_LIMIT`, live F3 | `main.go:644-646`; `82/REPORT.md` F3 | Overview / Accounts | yes | Warn at `>1`; explain the kick text `fm=disconnected by another connection` |
| D16 | Update policy matrix: channel, integrity algorithm, signer, expiry, self-install | sample metadata + `GET /v1/system/update` | `APK.md` §12; `WINMAC.md` §8; `LINUX.md` §8 | Update | yes | Read-only; deploy stays out of band. Never download/execute from the browser |
| D17 | Flags and posture: `AJS_ACCOUNT_LOGIN`, `AJS_MASTER_KEY` set, pin set, node limit, `AJS_LAB_REVEAL` | env parsing | `DASHBOARD-GAPS.md` F02, F25 | Flags / Diagnostics | yes | Secrets return `{set:true,redacted:true}` |
| D18 | Runtime logs and error taxonomy: `auth.*`, `session.timeout`, `timeout`, `fm` text | `slog` ring + gate errors | `APK.md` §14.1; `LINUX.md` §3 | Logs | yes | Allowlisted attrs; redact per §4 |

**Do-not-build list** (from DASHBOARD-GAPS §4.2 and §8.9, restated here as exposure = no):

- `ClientApiReport/Report` ingestion (installed-app list, SSID/BSSID, root status) — V07.
- `WebsiteCheckReport` / `CheckUrlKeyword` censorship probing — vendor behaviour, not a feature.
- Purchase/renew/KYC/invitation/rating flows — commerce, out of scope.
- Client-side VPN parity (network lock, split tunnel, TAP, system proxy, DNS modes) — the
  daemon is a SOCKS5 gate; these are OS-integration features of the vendor client.
- Any “show password / show ConnectPassword / show ping secret / show session key” control.
- Any PCAP download in the browser (metadata only; host-side files, phase 2).

## 4. Redaction and exposure rules (applies to every “yes”)

1. **Never serialized:** account password, `ConnectPassword`, `LoginToken`, FV session key,
   `k=` ciphertext, `par.fv.ps`, `Authorization`, `AJS_ADMIN_TOKEN`, `AJS_MASTER_KEY`,
   `AJS_RESIN_ADMIN_TOKEN`, full request bodies.
2. **Device identity:** `sha256(value)[:16]` with a `sha256:` prefix by default; `dim` may
   embed a device fingerprint and is raw-reveal only.
3. **SID:** `first4…(len)`; full value only in the guarded reveal path.
4. **Targets:** `/v1/lab/flows` returns `targetHost`/`targetHash`; full target requires
   `reveal=1` and an audit event.
5. **Flags:** secret keys return `{set:true,redacted:true}`; non-secret keys return value,
   source and `restartRequired`.
6. **Update metadata:** version, channel, algorithm, signer DN/serial/validity are public;
   update URLs are shown host-only until phase 2.
7. **PCAP:** metadata only in phase 2 (`id`, size, sha256, window); no browser download.
8. **Vendor telemetry:** not ingested, not stored, not rendered (V07).

## 5. Verification performed in this slice

Read-only checks that re-derive central claims (no sample execution):

| Claim | Check | Result |
|---|---|---|
| `product.dat` = AES-128-CTR, key `MD5("[CONFIG]")`, zero IV | Decrypted the 818 B blob with Python `cryptography` | Plaintext SHA-256 `2b618010b49d66922d778beda9aae7117084a46fd6008706e6f5c9c53291183e`; four `ServerApiUrls` + website + flags, matches `WINMAC.md` §4.1 |
| macOS Developer ID expires 2026-10-30 | `openssl x509 -enddate` on the leaf extracted from `analysis/AJiaSu.cms` | `notAfter=Oct 30 07:28:05 2026 GMT`; chain CA to 2027-02-01, root to 2035-02-09 |
| Linux amd64 disables TLS verification | `objdump -d --start-address=0x439d80` on `linux-amd64/ajiasu` | `mov $0x0,%edx` + `mov $0x40,%esi` (VERIFYPEER) and `$0x51` (VERIFYHOST) at `0x439d95`/`0x439dbf` |
| Android update checksum is MD5 | Read `p000/f01.java` and `UpdateActivity.java` | `MessageDigest.getInstance("MD5")` at `f01.java:83,574`; checksum intent extra at `UpdateActivity.java:308,345` |
| `account#N` tunnel username | Read `FVService.java:548-565` | `UserName += "#" + ClientSysEnvId` when the pref is non-empty (`:556-559`) |
| `AJS_NODE_LIMIT` defaults to 1 | Read `daemon/cmd/ajs-accountd/main.go` | `nodeLimit()` returns 1 when unset (`:644-646`); comment cites the live one-session finding (`:634-641`) |

No vendor call, no credential, no execution of any sample.

## 6. What could not be checked

- **Live acceptance of any 4.9.6.0 / 4.3.0.1 / 4.12.2.0 behaviour.** All three are static
  only; the live facts are from 4.2.3.0 (`82/REPORT.md`). Where a finding depends on live
  behaviour it is marked in the source report, not promoted here.
- **Desktop `X-Sig` secret and canonical field order.** Not recoverable statically; the
  signer exists in Windows/macOS/Linux 4.3.0.1 but the secret is unknown (`WINMAC.md` §13.1,
  `LINUX.md` §11).
- **Whether the vendor accepts the 4.3.0.1 dual-hash signature or still accepts unsigned
  requests for `cv=20260525-1`.** Highest-value open protocol question; needs the LINUX §10
  dynamic run.
- **Exact macOS helper/socket permissions** (socket mode, helper entitlements) — not observed;
  the setuid bit and root requirement are observed.
- **Android Play-channel signing key** — only the official-website APK was analysed (U10).
- **Android signature scheme version** — `APK.md` §2 records the v2-vs-v3 split as
  `[UNVERIFIED]` (no `apksigner` in that slice's toolset), while `TRIAGE.md` §4.3 records
  v1+v2 only from `apksigner verify -v --print-certs`. This matrix follows TRIAGE; the
  discrepancy is unresolved and does not change the exposure decision.
- **Full-load / member-only catalog fields** — static evidence only; `memberOnly` stays null
  until a response supplies it.

## 7. Source anchors

- `research/84-ajs-496/TRIAGE.md` §2 (inventory), §4 (certs), §6 (updates), §7 (version
  matrix), §8 (security observations), §9 (unknowns).
- `research/84-ajs-496/APK.md` §3 (manifest), §4 (hosts/commands), §6 (auth), §7 (device
  identity), §8 (flags), §9–§10 (LoginSync/node schema), §11.5 (ping), §12 (update), §13
  (telemetry), §14 (feature table), §15 (divergences).
- `research/84-ajs-496/WINMAC.md` §4 (hosts/commands), §5 (signing), §6 (protocol), §7
  (proxy/VPN/TAP), §8 (update), §9 (privilege/anti-tamper), §10 (flags/identity).
- `research/84-ajs-496/LINUX.md` §4 (control plane/signing), §6 (TLS), §7 (identity), §8
  (update), §9 (diff), §10 (dynamic plan).
- `research/84-ajs-496/DASHBOARD-GAPS.md` §2 (surface), §3 (function matrix), §4 (sample
  additions), §5 (Lab page), §6 (first slice), §7 (guarded actions), §8 (redaction).
- `research/82-live-20261023/REPORT.md` F1–F5.
- `research/81-protocol-gaps/REPORT.md` rows 2, 6, 18, 19, 21, 22, 26.
- `research/10-osint/REPORT.md` C19 (`account#N`), C21 (purchased concurrency), §Q2.1.
- `research/40-protocol/PROTOCOL.md` §Version matrix (`:532-542`), signing vectors.
