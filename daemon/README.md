# ajs-accountd (skeleton)

Go daemon with an in-memory panel store and a JSON snapshot that serves the
JSON contract the dashboard's live client expects (standard library plus the
in-repo `ajsproxy` module). The Resin admin client, the publisher, the
startup/topology/reconcile wiring and the runtime mutations are in place: when
`AJS_RESIN_ADMIN_TOKEN` is set the daemon renders the store's `LIVE` nodes into
the `ajs-live` local subscription before it starts listening,
`/v1/topology` / `/v1/reconcile` report the Resin view, and lease
rotate/release plus node drain/undrain write through Resin. The panel allocates
its own leases too: `POST /v1/leases` picks a free node for a user and platform
(an `Idempotency-Key` makes a retry safe, one active lease is allowed per user
and platform, and quota/concurrency/capacity refusals answer 429) and
`DELETE /v1/leases/{id}` revokes one behind `confirm:true`. Without the token it
keeps the store-derived view and the in-memory mutation behavior, and logs
`resin_disabled`. With `AJS_MASTER_KEY` set, created account passwords are
sealed with AES-256-GCM into `$AJS_STATE_DIR/secrets.json`; without it they are
validated and discarded. Every panel mutation is snapshotted to
`$AJS_STATE_FILE` (default `$AJS_STATE_DIR/state.json`), loaded before the
listener starts, saved every `AJS_SNAPSHOT_INTERVAL` (default 30 s) and once on
shutdown, so the store survives a restart. Vendor login is wired but default-off:
`AJS_ACCOUNT_LOGIN=1` makes `POST /v1/accounts/{id}/actions/login` call the
control API; without it the handler only refreshes the store, and no vendor
host is contacted. With the same switch the node controller follows a
successful login with `loginSync`, opens one authenticated FV session and one
loopback gate per usable vendor server, bounded by `AJS_NODE_LIMIT`
(default `1`, the vendor's one active data session per account;
`127.0.0.1:13000` upward, HTTP CONNECT on `+1000`), writes the node, and
reconciles Resin. Every gate defaults to the no-auth loopback method;
`AJS_GATE_AUTH=userpass` makes it require RFC 1929 (username =
`PanelUser.ID`, password = an active API-key secret) and enforce that user's
allowlist and monthly quota before admitting a flow. The HTTP CONNECT listener
reuses that same policy. It also makes
`POST .../drain|undrain` call the controller before the store, so a running gate
stops admitting (or reopens) before the node state changes; without the
switch the handlers keep the store/Resin behavior. A Lab telemetry view
(`/v1/lab/*`) reports the effective modes, flags, gate counters, session health, claimed egress,
profiles, a bounded redacted log ring and diagnostics; its GETs are read-only and it contacts no
vendor, and the single POST runs an explicit egress probe that writes nothing.
There is still no SQLite: the JSON snapshot is the v0.1 stopgap for the schema in `docs/PANEL.md`
§6, and the node supervisor is implemented but not wired to the controller.

Four P1 surfaces are served in the same process: account export/import/bulk-delete, catalog
selection with guarded connect/reconnect/disconnect, lease allocation and revocation, and a
cron/interval/once scheduler behind `AJS_SCHEDULES=1`. The device identity pool seals each
account's vendor identity in a separate
file under a key derived with HKDF-SHA-256 from `AJS_MASTER_KEY`, retires a deleted account's
identity instead of erasing it, and restores it when the same account is added again; it is served
over `/v1/device-pool` as well as through the store API. The catalog page also carries a
server-side `geo` aggregate, so the dashboard draws its map without downloading the retained
catalog.

The platform wave adds the customer read views and registries: cached renewal plans and
per-account expiry (`/v1/renewal/*`, redirects refused), flow-metered request-log usage
(`/v1/usage`; gate flows append rows unless `AJS_FLOW_METER=0`), a loopback gate health sampler
(`/v1/health/nodes`, `AJS_HEALTH_MAX_NODES` rings with eviction healing), outbound webhooks with
HMAC signing, an SSRF policy, bounded retries, a persisted delivery history and a dead-letter
audit row (`/v1/webhooks*`), passphrase-encrypted panel backup, inspect and restore (`/v1/backup*`),
plus least-privilege scoped API keys
(`/v1/scoped-keys`, `ajs_sk_` secrets with per-route scopes, including the
`usage:read`/`renewal:read`/`health:read`/`events:read` customer reads). The same wave adds
customer-visible event replay and a server-sent event stream (`/v1/events`,
`/v1/events/stream`) over the bounded ring the webhook sink fills. None of them calls a vendor host:
renewal prices come from the public purchase page and are cached, usage is derived from the
request log, and the health probe dials `127.0.0.1` only.

- Module: `ajsaccountd` · Go 1.26 toolchain · standard library plus `ajsproxy`
- Listen: `127.0.0.1:2261` by default
- State: versioned JSON snapshot at `$AJS_STATE_FILE` (default
  `$AJS_STATE_DIR/state.json`), loaded on startup and saved every
  `AJS_SNAPSHOT_INTERVAL` and on shutdown; a missing file starts an empty
  store. The daemon does not seed the dashboard mock: `store.New()` is a
  test/mock fixture, and `AJS_DROP_DEMO=1` removes the mock rows from an
  existing snapshot once, before serving. Sealed account secrets are inside the
  snapshot and, when `AJS_MASTER_KEY` is set, also in `$AJS_STATE_DIR/secrets.json`;
  device material lives in `$AJS_STATE_DIR/device-secrets.json` under a key
  derived from the same master key
- Wire contract: `dashboard/src/lib/types.ts` field names and
  `dashboard/src/lib/api.ts` paths/methods

## Layout

```
go.mod
cmd/ajs-accountd/main.go     flags/env, listener, graceful shutdown
internal/model/model.go      wire structs, inputs and patches
internal/store/              mutex-protected state + test/mock seed
internal/store/seed.go       mock-compatible seed data and health derivation
internal/store/demo.go       seed-row purge behind AJS_DROP_DEMO
internal/store/persist.go    versioned JSON snapshot: Save/Load, atomic 0600
internal/store/device.go     stable per-account device identity (snapshot-backed)
internal/store/device_pool.go  sealed device pool: states, restore, retire/GC, migration, import/export
internal/store/account_io.go  account export/import formats, encrypted envelope, bulk-delete apply
internal/store/leases.go     lease allocation: guards, idempotency replay, node placement, revoke
internal/store/catalog.go    selection + per-server connect counters
internal/store/schedules.go  schedule/run/fire/reminder retention and CRUD
internal/catalog/            offline exact-match city index for the catalog geo aggregate
internal/schedule/           cron parser, next-run, fire journal (WAL), engine, renewal sweep
internal/runtime/catalog_connect.go  guarded switch: one session per account, cooldown, snapshot guard
internal/api/catalog_connect.go  selection/connect/reconnect/disconnect handlers
internal/api/account_io.go   export/import/bulk-delete handlers
internal/api/leases.go       lease create/revoke handlers, Idempotency-Key contract, error mapping
internal/api/schedules.go    schedule CRUD, preview, run-now and run history handlers
internal/api/scoped_keys.go  scope table, scoped-key handlers and the auth identity
internal/api/device_pool.go  device-pool HTTP lifecycle handlers
internal/api/platform.go     renewal, usage, health and webhook handlers
internal/api/events.go       event replay page and server-sent event stream handlers
internal/api/backup.go       panel backup, inspect and restore handlers (admin-only)
internal/store/scoped_keys.go  scoped-key hashes, prefixes, expiry and revocation
internal/store/webhooks.go   persisted webhook receivers
internal/health/             loopback gate sampler: 30 s probes, 120-sample rings, states, eviction healing
internal/renewal/            cached public plan catalogue and per-account expiry
internal/usage/              request-log aggregation: windows, accounts, users, days, targets, retention
internal/webhook/            dispatcher, HMAC signing, SSRF policy, retry/backoff, delivery ring, scrub
internal/events/             bounded event ring: monotonic cursors, scrub, fan-out, cursor expiry
internal/backup/             passphrase-encrypted panel backup: deterministic tar, manifest, envelope, diff
internal/ratelimit/          per-scoped-key token bucket (rateLimitPerMin)
internal/runtime/flow_meter.go  gate flow -> request-log row (AJS_FLOW_METER)
internal/store/persist_test.go  roundtrip, mode, corrupt/version, counters
internal/secret/             AES-256-GCM sealed secrets (0600 file, rotation)
internal/resin/              Resin admin-API client (stdlib net/http)
internal/publisher/          subscription render, coalesced publish, rotation
internal/runtime/            startup wiring, panel topology/reconcile and mutations
internal/runtime/nodes.go    login -> per-server FV session + loopback gate + node store
internal/account/            FV protocol seam + account control client (Secret, Device, Control, LoginService)
internal/account/login_service.go  store-backed login path (default-off via AJS_ACCOUNT_LOGIN)
internal/account/tls.go         opt-in control-API certificate pin (PinnedHTTPClient)
internal/account/tls_test.go    pin match/mismatch/malformed/default-reject coverage
internal/gate/                per-node SOCKS5 + HTTP CONNECT gate (shared admission, byte counters, bounded flow ring, drain)
internal/gate/http.go         HTTP CONNECT listener: bounded head parse, Basic proxy auth, 407/403/429/405 mapping, tunnel relay
internal/publisher/reconcile.go  interval reconciler: desired-state hash, 1m→15m backoff, transition audits
internal/api/tls.go          optional HTTPS admin listener: AJS_TLS_* parse, TLS 1.2/ECDHE-AEAD policy, HSTS
internal/supervisor/          per-node child process: start, readiness, events, stop
internal/lablog/              bounded, redacted in-memory log ring (allowlisted attrs)
internal/model/lab.go        Lab wire structs (read-only reads + probe result; no credentials/raw ids)
internal/runtime/lab.go      LabView: modes, flags, gates, sessions, egress, profiles, logs, diagnostics
internal/runtime/egress_check.go  explicit egress probe: SOCKS5 dial + HTTPS echo, 5s cooldown
internal/runtime/lab_test.go lab view coverage (redaction, disabled modes, logs, devices)
internal/runtime/flows_test.go  flow window join: node/account ids, redacted SID, real gate end-to-end
internal/api/server.go       router, auth, CORS, logging, JSON helpers
internal/api/lab.go          Lab handlers (reads + explicit egress check) and account servers/device
internal/api/lab_test.go     lab handlers: auth, disabled bodies, egress-check mapping, query validation, 404
internal/api/flows_test.go   /v1/lab/flows: auth, limit, shape, real-gate HTTP row
internal/api/update.go       GET /v1/system/update: read-only GitHub release check
internal/api/*.go            account, node/lease, user/key and ops handlers
internal/api/server_test.go  httptest coverage
internal/api/update_test.go  update check: disabled/no-network, newer/same/older, prerelease/draft, 500/transport/malformed, auth, semver table
```

## Build, test, run

```bash
cd daemon
go test ./...
go test -race ./...
go build ./...
go vet ./...
AJS_LISTEN=127.0.0.1:2261 AJS_ALLOW_INSECURE=1 go run ./cmd/ajs-accountd
```

```bash
curl -s http://127.0.0.1:2261/healthz
curl -s http://127.0.0.1:2261/v1/accounts          # insecure mode: no bearer token
curl -s -H "Authorization: Bearer $AJS_ADMIN_TOKEN" http://127.0.0.1:2261/v1/accounts
```

`AJS_ALLOW_INSECURE=1` disables bearer auth and is refused unless `AJS_LISTEN` is
loopback, so it is only for throwaway local runs. Without a token or insecure
mode the process refuses to start. The placeholders the repo ships or prints
(`change-me`, `admin`, `test`, `password`, `secret`, `local-test-token`,
`replace-me-with-openssl-rand-hex-32`) and any token shorter than 32 characters
are refused even when set: generate a real one with `openssl rand -hex 32`.

The default `AJS_STATE_DIR` is `./data`, inside the tree. It holds `state.json`
(account usernames, device identities, audit) and `secrets.json` (sealed
passwords), so that directory is gitignored and excluded from the Docker build
context; point `AJS_STATE_DIR` outside the tree for anything that is not a throwaway
local run.

## FV protocol seam

The daemon reuses the emulator's tested FV protocol core instead of
reimplementing it. Go's internal-package rule blocks `ajsaccountd` from
importing `ajsproxy/internal/fv`, so the emulator module publishes a thin
facade:

```
emulator/fvfacade/facade.go   FV type aliases + function variables, no logic
emulator/apifacade/facade.go  control API aliases + function variables, no logic
emulator/internal/fv/         tested FV implementation (unchanged)
emulator/internal/api/        tested control API implementation (unchanged)
daemon/internal/account/      protocol seam + account control client
```

`fvfacade` re-exports `Session`, `Transport`, `Registry`, `AuthConfig`,
`AuthOptions`, `AuthError`, `RawTCP`, `HTTPUpgrade` and `OPV2Packet` as type
aliases, and the protocol functions (`Authenticate`, `DefaultRegistry`,
`NewSessionKey`, `NewIV`, `BuildAuthBody`, `BuildBinaryAuthBody`,
`EncodeFrame`, `DecodeHeader`, `ParseFields`, `ParseResponse`,
`Socks5ConnectRequest`, `VendorPublicKey`, `HashByName`, `RSAOAEPEncrypt`,
`AESCTR`, `RandomBytes`, `BuildDataText`, `BuildAuthText`, `RandomPad`,
`ParseRSAPublicKey`) as function variables. Aliases and variables keep the
symbols identical to the internal ones, so no adapter or conversion is needed;
the package adds no dependencies.

`daemon/go.mod` binds the modules:

```
require ajsproxy v0.0.0
replace ajsproxy => ../emulator
```

The `replace` makes `emulator` and `daemon` sibling directories; the daemon
therefore cannot be built from a checkout that lacks the emulator. The daemon
`go` directive is `1.26.0` because the emulator module declares that language
version; with `GOTOOLCHAIN=auto` the toolchain switches automatically.

`internal/account.NewProtocol()` wires `fvfacade.DefaultRegistry()` and the
carrier the default profile declares. `Linux423Binary.Transport` is
`"httpupgrade"`, so `NewProtocol()` returns `*fvfacade.HTTPUpgrade` (the
protocol-2 binary carrier), not the OpenAJS raw-TCP transport;
`NewProtocolForProfile(account.OpenAJSAndroid)` returns `fvfacade.RawTCP{}`
instead. `Available()` reports whether the transport is registered. This is a
compile/import proof, not a vendor client: it performs no I/O, and
`protocol_test.go` pins both profile → carrier mappings.

### Building the sibling modules

Both modules are tested and built independently, with the daemon run after the
emulator is present at `../emulator`:

```bash
cd daemon   && go test ./... && go build ./...
cd ../emulator && go test ./... && go build ./...
```

On HMC, copy the two trees as siblings to the scratch root (the layout the
module `replace` assumes). The `fv` tests also read the captured mock packets
from `research/60-dynamic/captures`, resolved as `../../../research/...` from
`emulator/internal/fv`, so place that directory at the same scratch root:

```
/srv/dwgxhomecloud/scratch/ajs-multimodule/
├── daemon/                          # ajsaccountd
├── emulator/                        # ajsproxy
└── research/60-dynamic/captures/    # fvmock-1.bin, fvmock-2.bin fixtures
```

```bash
rsync -a --delete daemon emulator /srv/dwgxhomecloud/scratch/ajs-multimodule/
mkdir -p /srv/dwgxhomecloud/scratch/ajs-multimodule/research/60-dynamic/captures
rsync -a research/60-dynamic/captures/fvmock-*.bin \
  /srv/dwgxhomecloud/scratch/ajs-multimodule/research/60-dynamic/captures/
cd /srv/dwgxhomecloud/scratch/ajs-multimodule/daemon && go test ./... && go build ./...
cd ../emulator && go test ./... && go build ./...
```

## Account control layer

`internal/account` carries the control-API client and the FV session factory
that use it. It wraps the emulator's tested `internal/api` and `internal/fv`
packages through `apifacade`/`fvfacade`; it performs no I/O until a method is
called and has never contacted a vendor host.

```
emulator/apifacade/facade.go        control API aliases + function variables
emulator/fvfacade/facade.go         FV protocol aliases + function variables
daemon/internal/account/secret.go   Secret: fmt/slog/JSON redaction
daemon/internal/account/device.go   device profiles + stable identity record mapping
daemon/internal/account/control.go  Login / LoginSync / Servers wrapper
daemon/internal/account/login_service.go  store-backed login path (default-off)
daemon/internal/account/session.go  VersionProfile, SessionRequest, FlowSession, Factory
daemon/internal/account/tls.go       opt-in control certificate pin (PinnedHTTPClient)
```

`apifacade` re-exports `Client`, `Device`, `LoginResult`, `Server`, `ServerFV`,
`ServerPar`, `ServerLoc` and `ErrNoLoginToken` as aliases, `DefaultSecret` as a
constant, and `NewClient`, `DefaultDevice`, `NewDeviceID`, `NewUID`,
`BuildQuery`, `Sign`, `DualHashHex`, `URLEncode` as function variables. Aliases
and variables keep the symbols identical to the internal ones, so the daemon
uses the tested client without conversion and the facade adds no dependencies.

### Profiles and signing

| Profile | `_v` | Constructor | Request shape | Signing |
|---|---|---|---|---|
| Linux 4.2.3.0 (default) | `4.2.3.0` | `NewDevice("")` / `NewDeviceForProfile(Linux423Binary)` | `ShapeLinux423` | none (`SigningMode: "none"`; no `X-Sig`/`X-Timestamp`) |
| Linux 4.3.0.1 (opt-in) | `4.3.0.1` | `NewDevice(Linux430ClientVersion)` / `NewDeviceForProfile(Linux430Binary)` | `ShapeLinux423` | none (`SigningMode: "none"`; 4.3.0.1 changed `X-Sig`, but the canonical field order is [UNKNOWN], so requests stay unsigned and live acceptance is [UNVERIFIED]) |
| OpenAJS Android | `4.11.10.0` | `NewDevice(OpenAJSClientVersion)` | `ShapeOpenAJS` | `X-Timestamp` + `X-Sig` with the OpenAJS secret |

`NewDevice` builds the Linux 4.2.3.0 descriptor from `apifacade.Linux423Device`
and `EnsureLinuxIDs`: `_m=linux`, `_os_dev=x86_64`, the captured `_os_ver` and
`_os_dim`, a 20-hex `_uid` that is also `cuid`, `cseid=linux`, and a 16-hex
`_os_did` the Linux control body does not send. Any other version builds the
OpenAJS/Android descriptor from `apifacade.DefaultDevice` with a 22-character
`_uid` and a 16-hex `_os_did`; its `cuid`/`cseid` fall back to the device id.
`Identify` returns the non-secret `store.DeviceIdentity` record.

The caveat is the one recorded on `api.DefaultSecret`: the shipping Linux
4.2.3.0 binary contains neither the OpenAJS secret string nor its raw bytes, and
no live request has been made from this tree. Whether the vendor backend accepts
a signed request from that profile is therefore `[UNVERIFIED]`;
`Options.NoSigning` omits both headers for it. Signing is on by default (the
zero `Options` signs), so the daemon cannot send an unsigned request by
accident. `LoginService` is the deliberate exception: it maps the profile's
`SigningMode` (`none` for Linux 4.2.3.0) onto `NoSigning`, so the default
login path sends unsigned requests. The emulator changes behind this are two
`emulator/internal/api.Client` fields: `NoSign` skips both signature headers and
`RequestShape` selects the field set; `NewClient` and both zero values still
sign and send the OpenAJS shape.

### Control client

```go
device, err := account.NewDevice("")                 // Linux 4.2.3.0
control := account.NewControl(hosts, device, account.Options{})
defer control.Close()
login, err := control.Login(ctx, username, password)  // password: account.Secret
sync, err := control.LoginSync(ctx)                   // refreshes ConnectPassword
servers, err := control.Servers(ctx)                  // map[string]apifacade.Server
```

- `Options`: `HTTPClient`, `UserAgent`, `NoSigning`, `RequestShape`, `Now`.
  The zero value keeps the api package's 35-second HTTP client,
  `okhttp/4.9.3`, the OpenAJS shape, signing and `time.Now`.
- `LoginResult`: `AuthResult`, `AuthMessage`, `Token`, `UserName`,
  `ConnectPassword`, `MembershipStatus`, `ExpireTime`, `Expired`, `Servers`.
  `Token` and `ConnectPassword` are `Secret`; `Control.Login` leaves `Servers`
  nil, and `LoginService.Login` follows the login with `LoginSync` so the
  retained result carries the server map the node controller needs.
- A non-OK `AuthResult` is an `*AuthError` carrying the code and server message
  (no credentials); the parsed result is returned with it. Transport and
  protocol failures are returned as-is.
- `LoginSync` fails with `apifacade.ErrNoLoginToken` before a successful login.
  It returns the refreshed token/user/connect password plus the server map;
  `AuthResult` stays empty because the emulator client reduces the sync
  response to credentials and servers.
- Failover matches OpenAJS: only a transport error advances to the next host.
  An HTTP 4xx/5xx, invalid JSON or a response without a `Login` object is
  returned to the caller and the next host is not contacted.
- `Control` keeps the last session and is not safe for concurrent use; the
  session factory owns one per account. `Close` releases idle HTTP connections;
  the emulated surface has no logout command.

### Request shape, device identity and carrier (AJS-55)

Three pre-live gaps from `research/81-protocol-gaps/REPORT.md` (B1, B5, C3) are
closed here. No vendor host was contacted for any of them; live acceptance is
`[UNVERIFIED]`.

**B1 — Linux control request shape.** `api.Client.RequestShape` selects the
field set; the zero value is `ShapeOpenAJS`, so the existing Android path is
byte-identical. `ShapeLinux423` matches
`research/60-dynamic/captures/control.jsonl` line 2, the only direct Linux
4.2.3.0 request:

- `v=4.2.3.0` instead of `_v`, and a 20-hex `_uid`;
- only `_lang/_m/_os_dev/_os_dim/_os_ver/_site/_uid/v` plus `cmd`, `username`
  and `password`;
- no `_os_hw`, `_os_did`, `_os_lang`, `_tz`, `_inst_mkt`, `_vrf`, `cv` or
  `trigger`;
- no `User-Agent`; `Accept: */*`, `Accept-Encoding: deflate, gzip` and
  `Connection: close`;
- the captured trailing `&` after `v=4.2.3.0`.

`emulator/internal/api/client_test.go` compares the recorded body with the
capture field by field (uid and password are redacted in failure messages) and
asserts the absent fields, the headers and the trailing separator;
`TestLoginLinuxShape` repeats the check through the daemon `Control`.

**B5 — stable device identity.** `store.DeviceIdentity` is the non-secret record
(UID, OSDID, CUID, CSEID, OSP, OSD, OSV, DIM plus the remaining control
descriptor fields). `Store.EnsureAccountDevice` stores it once and returns the
first value forever, including under concurrent first logins; `LoginService`
loads it before every login and generates it only when absent. The record is
part of the state snapshot (`devices`; additive, so `SnapshotVersion` stays 1),
so a restart keeps the identity. `TestLoginServiceDeviceStable` proves two
logins send the same `_uid` and that UID/OSDID/CUID/CSEID survive the snapshot;
`TestLoginServiceDeviceSurvivesRejection` proves a rejected login followed by a
retry keeps the same identity; `daemon/internal/store/device_test.go` covers
once-only, concurrent and validation behaviour.

**C3 — transport label vs carrier.** `Linux423Binary.Transport` is now
`httpupgrade`, the registry name of `*fvfacade.HTTPUpgrade`, which implements
the protocol-2 binary carrier; `NewProtocol()` resolves it (and applies the
profile's `ProtocolVersion`), while `NewProtocolForProfile(OpenAJSAndroid)`
returns `fvfacade.RawTCP{}` (`rawtcp`). `TestProtocolProfileCarrier` pins both
mappings and rejects an unknown or empty carrier.

The FV auth identity carries the same split: `AuthConfig.CUID`/`CSEID` default
to `DeviceID` (the OpenAJS body stays byte-identical) and the Linux device sends
its 20-hex `cuid` with `cseid=linux`; `TestAuthIdentityCUIDCSEID` and the
decrypted `fvmock-1` capture vector assert both.

### Login service (AJS-38)

`login_service.go` is the only daemon path that can contact a control API host.
`cmd/ajs-accountd` builds it only when `AJS_ACCOUNT_LOGIN=1`; construction
performs no I/O, so a disabled daemon and a freshly started one make no vendor
call. The default profile is `linux-4.2.3.0` (the tested `Linux423Binary`),
whose `SigningMode` is `none`, so its requests are unsigned.

```go
service, err := account.NewLoginService(st, account.LoginConfig{
    Hosts:   hosts,                 // empty → DefaultLoginHosts (four hosts)
    Profile: account.Linux423Binary, // zero → Linux423Binary
    Enabled: true,                  // false → Login returns ErrDisabled
    Logger:  logger,
})
defer service.Close()

updated, err := service.Login(ctx, "acc-01")
result, ok := service.Result("acc-01") // in memory, read by the node controller
```

- `Login` reads the account and its sealed password with
  `store.AccountSecret`, loads the account's stored device identity (generating
  and persisting one on the first login, never on a retry), builds a `Control`
  with the profile's request shape, and calls `ClientApiLogin/Login`. The
  password is a `Secret`; the plaintext byte slice is cleared after conversion.
- Success: `store.MarkLogin` sets `active` (or `expired` when the vendor says
  so), updates `lastLoginAt`, adopts the vendor user name, and appends
  `account.login`. `Login` then sends the follow-up `loginSync` and merges the
  refreshed connect password and server map into the result; a sync failure is
  logged as `account_login_sync_failed` and leaves the login successful with an
  empty server map. The `LoginResult` is retained in a mutex-protected map;
  `Result` returns a copy. `Close` releases idle connections and drops it.
- Auth rejection (`*account.AuthError`): `store.MarkAuthFailed` sets
  `auth_failed` and appends `account.auth_failed` with the sanitized vendor
  message (one bounded line, no newlines). `Login` returns
  `ErrAuthRejected`.
- Transport/protocol failure: the account is left untouched (no audit state
  change), a warning is logged, and `Login` returns `ErrUpstream`. A missing
  account is `store.ErrNotFound`; a missing sealed password is `ErrNoSecret`.
- No log line, error or stored value contains the password, login token or
  connect password.

### Control certificate pin (AJS-93)

Every control API host currently presents the same self-signed
`CN=localhost` certificate, so a login with default TLS verification fails
while the shipping Linux client connects anyway. `AJS_CONTROL_CERT_SHA256`
adds an explicit, opt-in pin for exactly that transport:

```bash
# 64 hex chars; colons optional
AJS_ACCOUNT_LOGIN=1 AJS_CONTROL_CERT_SHA256=AA:BB:CC:…:22 ./ajs-accountd
```

- `account.PinnedHTTPClient(fingerprint, timeout)` clones the default
  transport and sets `MinVersion: tls.VersionTLS12`, `InsecureSkipVerify` and
  a `VerifyPeerCertificate` that compares `sha256(rawCerts[0])` with the
  configured fingerprint using `hmac.Equal`. **The pin replaces chain and
  hostname verification for this client only**: this is intentional and is
  the documented trade-off — the pin is the verification. Every other HTTPS
  client of the daemon keeps standard verification; nothing skips TLS
  globally.
- Empty/unset keeps the current default verification and changes no behavior.
  A malformed value (non-hex or not 32 bytes) fails startup with
  `AJS_CONTROL_CERT_SHA256: …` instead of silently weakening the check.
- The client is built in `startAccountLogin` with the same 35-second timeout
  the api package uses and passed as `LoginConfig.HTTPClient`, so both the
  login and the follow-up `loginSync` ride the pinned transport. Startup
  logs `control_tls_pin` with the fingerprint — public configuration, never
  a credential.
- **Pin source**: the fingerprint of the certificate the vendor transport
  actually presents, e.g.
  `openssl s_client -connect login.ajs-app.com:443 </dev/null 2>/dev/null | openssl x509 -fingerprint -sha256`.
  The vendor rotates or re-issues the certificate → the pin must be updated
  to the new fingerprint and the daemon restarted; a stale pin fails the
  handshake (`account: certificate pin mismatch`) rather than connecting.
  Rotating ahead of the vendor is not possible: only one fingerprint is
  accepted.
- **Warning**: this is a *vendor-transport pin*, not a general insecure
  switch. Setting it does not disable TLS, does not affect any other host or
  client, and protects nothing beyond the control API endpoints. Do not use
  it as a template to skip verification elsewhere.
- `tls_test.go` proves the four behaviours with an `httptest.NewTLSServer`:
  a matching pin (colon and raw-hex forms) lets the self-signed certificate
  through, a wrong pin fails with `ErrCertPin`, a malformed fingerprint is
  rejected before any connection, and a default client still rejects the
  same certificate (no global skip).
- Live login with the pin remains `[UNVERIFIED]`: no vendor host has been
  contacted from this tree; the acceptance is the in-process TLS server.

### Secret handling

`account.Secret` is a string type whose `String`, `GoString`, `MarshalJSON` and
`slog.LogValue` methods all yield `[redacted]`; `Reveal()` is the only
accessor. A `LoginResult`, a request log line or a JSON document can therefore
be printed without leaking the login token or connect password. No credential
is written to the store, the README or a test fixture.

### FV session factory (AJS-34)

`session.go` opens one authenticated FV session per request and exposes its
data channels. It adds no wire logic of its own: authentication is
`fvfacade.Authenticate` and every flow is `fvfacade.Session.OpenStreamContext`.

```go
f := account.NewFactory(account.Linux423Binary, &fvfacade.HTTPUpgrade{},
    fvfacade.AuthOptions{})
sess, err := f.Open(ctx, account.SessionRequest{
    AccountID:       "acc-01",
    Server:          login.Servers["s1"],
    Device:          device,
    UserName:        login.UserName,
    ConnectPassword: login.ConnectPassword, // account.Secret
    Profile:         account.Linux423Binary, // optional; must match the factory
})
defer sess.Close()
flow, err := sess.OpenFlow(ctx, "example.com", 443)
```

- `VersionProfile`: `Name`, `ClientVersion` (`_v`), `CoreVersion` (`cv`),
  `ProtocolVersion`, `OAEPHash`, `SigningMode`, `RequestShape`, `Transport`.
- `Linux423Binary` (default): `linux-4.2.3.0-binary`, `_v=4.2.3.0`,
  `cv=20241105-1`, protocol 2, OAEP-SHA1, signing none, `ShapeLinux423`,
  carrier `httpupgrade` (the protocol-2 binary carrier implemented by
  `fvfacade.HTTPUpgrade`). `NewFactory` still takes the transport explicitly
  because it is a value, but the profile now names the implementation that
  actually speaks its wire format.
- `OpenAJSAndroid`: `openajs-4.11.10.0-rawtcp`, `_v=4.11.10.0`,
  `cv=20260525-1`, `ShapeOpenAJS`, carrier `rawtcp`. The daemon does not wire
  it (`AJS_ACCOUNT_PROFILE` accepts only the Linux profiles); it exists so the
  Android path stays available and testable.
- Auth body mapping: `u=UserName`, `p=ConnectPassword.Reveal()`,
  `cuid=Device.CUID`, `cseid=Device.CSEID` (both fall back to
  `Device.DeviceID` when empty, which is the OpenAJS body), `st=Device.Site`
  (default `ajs`), `v=Profile.ClientVersion`, `cv=Profile.CoreVersion`,
  `osp=Device.Platform`, `osd=Device.OSDevice`, `osv=Device.OSVersion`,
  `dim=Device.OSDim`. For the Linux device this is a 20-hex `cuid`,
  `cseid=linux`, `osp=linux`, `osd=x86_64`, the captured `osv`/`dim`.
- `Open` rejects a request with no FV endpoint (`svr`/`par.fv.p`), no user, no
  connect password, no device id, a profile that differs from the factory's, or
  an unknown OAEP hash. `AuthOptions.OAEPHash` overrides the profile hash; zero
  means the profile's.
- `FlowSession`: `SID`, `Fields` (merged auth response, read-only),
  `OpenFlow`, `Healthy` (`SID != ""`, no probe), `Close` (idempotent).
- The connect password exists only in the auth body. It is a `Secret` in the
  request, so `%v`, `%#v`, JSON and `slog` redact it; the factory never logs.
- Limitation: the factory is not wired to the store, the API or a login yet,
  and no live vendor endpoint has been contacted. Its wire behaviour is proven
  only against the in-process mock.

`session_test.go` runs an in-process FV server built only from `fvfacade`
exports (`AESCTR`, `NewCTR`, `ParseFields`, `HashByName`, the carrier
constants) and a generated 1024-bit RSA key. It decodes the binary-carrier
auth, decrypts OAEP-SHA1 with the test key, checks `u/p/cuid/cseid/v/cv/st`,
writes `code=200` plus the auth map and half-closes; then it accepts the data
connection, decodes the SOCKS5 CONNECT (`echo.example:80`), replies `05 00` and
relays to a local echo listener. The reject case asserts an `r=denied` map
surfaces as `*fvfacade.AuthError` with no password in the message;
`TestLinux423BinaryProfile` pins the default profile.

The only emulator change is on `fvfacade`: `NewCTR` (the streaming cipher the
relay needs, since `AESCTR` resets its position) and the
`SessionKeySize`/`IVSize`/`HeaderSize`/`OAEPBlockMagic` constants. No internal
package is imported and no emulator test changed.

## Gate (AJS-40)

`internal/gate` is the per-node proxy front end Resin dials (`docs/PANEL.md`
§1–§2). One `Server` owns one node's loopback listeners — the SOCKS5 gate plus
an optional HTTP CONNECT listener — and injects the FV session through a
one-method interface, so the gate knows nothing about the vendor protocol,
credentials or the control API.

```go
srv, err := gate.NewServer(gate.Config{
	NodeID:    "acc-01:srv-07",
	AccountID: "acc-01",
	Listen:    "127.0.0.1:13007", // default 127.0.0.1:0
	Limit:     32,                // default 32
}, sess)                          // sess: gate.Session; *account.FlowSession fits
if err != nil {
	return err
}
if err := srv.Start(ctx); err != nil { // cancelling ctx is Close
	return err
}
defer srv.Close()

fmt.Println(srv.Addr())          // bound loopback address
fmt.Printf("%+v\n", srv.State()) // counters, active, draining
_ = srv.Drain(ctx)               // reject new, wait up to DrainGrace
_ = srv.Undrain()                // back to service
```

- `Session` is `OpenFlow(ctx, host, port) (net.Conn, error)`.
  `account.FlowSession` is a superset (`SID`, `Fields`, `Healthy`, `Close` are
  extra), so the existing session factory is injected without an adapter.
  `server_test.go` pins the assignment with
  `var _ gate.Session = account.FlowSession(nil)`.
- `Config`: `NodeID`, `AccountID`, `Listen`, `HTTPListen`, `Limit`,
  `AdmissionTimeout`, `DrainGrace`, `Now`, `Logger`, `OnFlow`, `QuotaCheck`,
  `AuthMode`, `AuthTimeout`, `Authenticator`, `UserPolicy`. Zero values select
  the defaults (`127.0.0.1:0`, no HTTP listener, 32, 30s, 30s, `time.Now`,
  `slog.Default()`, `AuthLoopback`, 3s). `Listen` and `HTTPListen` must name a
  literal loopback IP (`127.0.0.1` or `::1`): a wildcard, LAN/public address or
  hostname fails `NewServer`, and `Start` rechecks each bound address before
  serving. `HTTPAddr()` returns the bound HTTP address (nil when disabled).
- `State`: `NodeID`, `AccountID`, `Active`, `Limit`, `Draining`, `Accepted`,
  `Rejected`, `Probes`, `IngressBytes`, `EgressBytes`, `StartedAt`. `Accepted`
  counts flows that received the success reply and entered relay; `Rejected`
  counts flows refused before relay (draining, admission timeout, malformed
  handshake, failed authentication or policy, failed `OpenFlow`); `Probes`
  counts connections that closed before sending any handshake byte (a
  health-sampler dial+close), which log at debug and never enter the flow ring.
  Byte counters are updated live, exclude the handshake and cover both
  listeners: one node has one traffic total.
- Flow ring: every finished flow, including a refusal, appends one
  `FlowRecord` (`ID`, `StartedAt`, `DurationMs`, `UserID`, `UserName`,
  `Method`, `Protocol`, `TargetHost`, `TargetPort`, `Status`, `BytesIn`,
  `BytesOut`, `ErrorClass`) to a bounded 500-entry ring. `Protocol` is
  `socks5` or `http`. Only host and port are recorded — never a URL, headers or
  a credential. `UserID`/`UserName` are attribution only and stay empty in
  `loopback` mode. `Flows(limit)` returns the newest records first and
  `Dropped()` counts evictions. The runtime joins the records with the
  node/account ids and a redacted session SID for `GET /v1/lab/flows`.

Wire contract (RFC 1928 + RFC 1929, CONNECT only):

| Step | Bytes | Gate behaviour |
|---|---|---|
| Greeting | `05 NMETHODS methods…` | `loopback` requires method `00`; `userpass` requires `02`. A missing required method replies `05 FF` and closes (`auth_method`). A wrong version closes without a reply. |
| Auth (`userpass`) | `01 ULEN user PLEN pass` | username is the exact `PanelUser.ID`, password an active API-key secret. Success `01 00`; any bad/revoked/mismatched credential or invalid frame `01 01` and close (`auth_failed`). Bounded by `AuthTimeout` (3s). |
| Request | `05 01 00 ATYP addr port` | only CONNECT; `ATYP 01` IPv4, `03` domain (1-byte length), `04` IPv6; RSV must be 0. The host is canonicalized (ASCII lower case, one terminal dot removed) before policy. |
| Success | `05 00 00 01 00 00 00 00 00 00` | after `OpenFlow` succeeded; relay starts. |
| Refused | `05 05 00 01 …` | draining, closed, or `OpenFlow` error. |
| TTL expired | `05 06 00 01 …` | the admission semaphore stayed full for `AdmissionTimeout`. |
| Command unsupported | `05 07 00 01 …` | `CMD != 1`. |
| Address unsupported | `05 08 00 01 …` | `ATYP` not 1/3/4. |
| Malformed | `05 01 00 01 …` | RSV non-zero or an empty domain name. |
| Not allowed | `05 02 00 01 …` | user disabled, allowlist miss, user quota or account quota exhausted. |

- Admission: a buffered channel of `Limit` slots. A flow waits at most
  `AdmissionTimeout` (default 30s) and is answered `0x06` on expiry; the slot is
  released exactly once after the relay ends. Drain closes the drain channel,
  so waiting flows fail immediately with `0x05` and a flow that has not
  registered yet cannot start after Drain.
- Drain/Close: `Drain` sets `Draining`, rejects new flows and waits for
  `Active` to reach zero or `DrainGrace` (default 30s); in-flight flows keep
  relaying. `Undrain` resets the flag. `Close` stops the listener, waits the
  same grace, then force-closes every tracked client and upstream connection,
  including a half-open handshake or an `OpenFlow` blocked on its cancelled
  context. Both are idempotent; `Drain`/`Undrain`/`Start` return `ErrClosed`
  afterwards.
- Relay: two `io.Copy` directions through counting writers. On EOF the
  destination is half-closed when it supports `CloseWrite` and closed
  otherwise; both sides are closed when the relay ends.
- Logging: `gate listening`, one `gate flow closed` per relayed flow
  (`node_id`, `user_id` after a successful authentication, `target`,
  `ingress_bytes`, `egress_bytes`, `duration`) and warn lines for rejections.
  A failed handshake logs the node and the class only: never the claimed
  username, the password, a key prefix or buffered frame bytes. Only host:port
  is logged as a target.
- Limitation: the package is now constructed from a login session by the node
  controller (`internal/runtime/nodes.go`, B0), which allocates the port and
  reconciles Resin. It is still not wired to the node supervisor, publishes no
  `State` and serves no `GateController` HTTP surface; it binds loopback only.
  Per-user authentication exists but is off by default (see below).

### Per-user identity (AJS-266 / DESIGN-GATE-IDENTITY)

`AJS_GATE_AUTH` selects the gate authentication mode for both listeners (the SOCKS5
method and, on the HTTP CONNECT listener, `Proxy-Authorization: Basic`); unknown values abort
startup:

- `loopback` (unset default) keeps the pre-RFC-1929 wire: the client offers
  `00`, the gate trusts every local process, and user fields stay empty. The
  loopback peer check and the account quota still run. It is not a secure
  multi-user mode.
- `userpass` requires RFC 1929 `02`; the username is the exact `PanelUser.ID`
  and the password is an existing API-key secret (`POST /v1/keys`). Every
  rejection is one generic `01 01`, so an unknown user, a wrong secret, a
  revoked key and another user's key are indistinguishable.

After CONNECT parses and before a slot or `OpenFlow`, the gate evaluates, in
order: user enabled, the user's allowlist, the user's monthly quota, then the
existing account quota. A refusal answers `05 02` with its class
(`user_disabled`, `allowlist`, `user_quota_exceeded`, `policy_unavailable`,
`quota_exceeded`) and never opens upstream traffic.

- Allowlist entries are one exact case-insensitive hostname, one `*.domain`
  wildcard, one exact IP or one CIDR. Every entry is normalized on write:
  lowercase, one terminal dot trimmed, IPv4-mapped IPv6 unmapped, prefixes
  masked (`10.20.5.7/16` becomes `10.20.0.0/16`), duplicates collapsed, at most
  64 entries of at most 253 bytes each. A wildcard covers names with at least
  one extra label (`*.example.com` matches `a.example.com` and
  `a.b.example.com`, never `example.com` itself); bare `*` and a single-label
  wildcard are refused. A host rule never implies a subdomain, a CIDR matches
  only a literal target IP, and nothing is ever resolved. Schemes, paths, ports,
  brackets and IPv6 zones are a 400 at write time, so a stored list only ever
  holds entries the gate can compile. Empty denies every destination in
  `userpass`; a malformed stored entry denies the user rather than widening the
  policy.
- `PanelUser.usedBytes` is derived, not stored: the store sums the current UTC
  month's gate-metered request rows for that user on read (bounded by the
  10,000-row request cap), so the panel, the gate's user quota and
  `GET /v1/usage` all report the same figure. A new UTC month starts it at zero,
  and the seeded dashboard-mock counters are seed data, never usage.
- The key digest index, the compiled allowlists and the monthly user total are
  process-wide caches shared by every gate. Revoking a key, suspending a user or
  replacing an allowlist affects the next new connection; admitted flows finish
  with the identity they authenticated. A key created after startup is
  discovered within one second.
- Metering attributes accepted flows to `RequestLog.UserID/UserName` and
  advances the same per-user total the policy reads, so `AJS_GATE_AUTH=userpass`
  requires `AJS_FLOW_METER` (the daemon refuses the combination otherwise).
- Migration: create an ordinary user and key, set an explicit allowlist and
  quota, put the user id and one-time secret in Resin's username/password
  fields, verify attributed flows, then switch `AJS_GATE_AUTH=userpass`.
  Rollback is `AJS_GATE_AUTH=loopback` while the listener stays loopback.

### HTTP CONNECT listener (AJS-271)

Every node can also serve HTTP CONNECT on `127.0.0.1:(gatePort + offset)`. The
offset defaults to 1000 (`AJS_GATE_HTTP_OFFSET`), so 13000/14000,
13001/14001, ... The listener is opt-in: `AJS_GATE_HTTP=1` enables it, unset or
`0` keeps it off, and any other value aborts startup. An upgrade therefore
never starts binding a second port pair on its own. In the library it is
`Config.HTTPListen` (empty disables it) and `HTTPAddr()` returns the bound
address.

- Both listeners feed one admission pipeline: the same credential check (the
  RFC 1929 username/key pair, carried as
  `Proxy-Authorization: Basic base64(user:key)`), the same per-user
  enabled/allowlist/quota policy, the same account quota, one `Limit` slot
  pool, one set of byte counters and one flow ring. `FlowRecord.Protocol` and
  `RequestLog.Protocol` name the listener (`socks5` or `http`); a metered HTTP
  flow is a normal 200 row with `method: CONNECT`.
- `loopback` mode needs no credentials and ignores a supplied
  `Proxy-Authorization`, exactly as the SOCKS5 no-auth method does. `userpass`
  requires Basic credentials and answers one 407 for every failure, so an
  unknown user, a wrong secret, a revoked key and a malformed header are
  indistinguishable. The challenge realm is `apc`.
- Wire contract (CONNECT only, HTTP/1.1):

| Case | Response |
|---|---|
| admitted | `HTTP/1.1 200 Connection Established`, then the bidirectional tunnel |
| missing/wrong credentials | `407 Proxy Authentication Required` + `Proxy-Authenticate: Basic realm="apc"` |
| user disabled, allowlist miss, policy unavailable | `403 Forbidden` |
| user or account monthly quota exhausted | `429 Too Many Requests` + `Retry-After: <seconds until the next UTC month>` |
| method other than CONNECT | `405 Method Not Allowed` + `Allow: CONNECT` |
| malformed request line, authority, version or header; head over 8 KiB or 64 headers | `400 Bad Request` |
| `OpenFlow` failed | `502 Bad Gateway` |
| draining, admission timeout, closed | `503 Service Unavailable` (an admission timeout also sends `Retry-After: 1`) |

- The request head is bounded (8 KiB total, 64 headers, a single line no
  longer than the 4 KiB reader buffer) and a refusal consumes the head before
  closing, so the response is not lost to a TCP reset. Bytes the client
  pipelines behind the head are preserved for the tunnel. Only host:port is
  logged and recorded; a credential never reaches a log line or a flow record
  and is cleared after the check.
- Security note: `Proxy-Authorization: Basic` is base64, not encryption. The listener binds a
  literal loopback address exactly like the SOCKS5 gate and refuses a wildcard, LAN/public or
  hostname bind, so the credential never crosses a network — but anything that can read loopback
  traffic on the host can read it. The 407 is stateless — one challenge per connection, no session
  or cookie — and there is no second factor; revoking the API key is the only cut-off, and it
  applies to the next connection.
- Port allocation: the controller reserves both `p` and `p+offset` for every
  running gate and every stored node, and a bind failure on either half retries
  the next base port. `Start` is all-or-nothing, so a node never comes up with
  only one listener. A lease still advertises `gatePort`; the HTTP port is that
  port plus the offset.
- Tests: `http_test.go` relays real TLS bytes end-to-end against a local TLS
  echo server, and covers 407 (seven credential shapes), 403 allowlist, 429
  user/account quota with a pinned `Retry-After`, the 405/400 matrix, 502,
  pipelined bytes, loopback compatibility, drain, probe counting, non-loopback
  peers, listener validation, atomic bind failure, shutdown and credential
  redaction. `runtime/gate_http_test.go` proves the controller exposes
  `gatePort+1000`, meters the row with `protocol: "http"`, reserves both ports
  across nodes and retries the pair when the HTTP port is taken.

`auth_test.go` covers the twelve acceptance rows at the wire and policy seams:
`05 02`/`01 00`/relay and attribution, the four bad-credential cases, a missing
`02` method, malformed/truncated frames and the 3s auth deadline, the loopback
compatibility path, loopback-only binds, a non-loopback injected peer,
allowlist matching (host, `*.domain`, IP/CIDR, subdomain, malformed) and policy
ordering before the account quota. `runtime/gate_identity_test.go` adds the
store seam:
metered attribution and quota advance, immediate key revocation with the
admitted flow still relaying, disabled/deleted users, allowlist updates, new-key
discovery, and 32 concurrent flows under `-race` with live key/allowlist
mutations. `BenchmarkFlowAdmission` compares a loopback and a userpass flow with
1 MiB each way; the store-backed `BenchmarkGateIdentityChecks` measures the
added per-flow authentication and policy cost (about 0.5 µs and 5 allocations
on the dev host), which is approximately 0.01% of a 1 MiB relay and far below
the design's 1% budget.

`server_test.go` runs only loopback and in-process fakes: a `net.Pipe` echo
session for domain/IPv4/IPv6 CONNECT, relay and exact counters; limit 1 with a
150ms admission timeout (`0x06`, then success after release); Drain rejecting a
new flow while the active one completes, Undrain accepting again; Drain grace
leaving the in-flight flow usable; malformed greeting, zero methods, wrong
version, unsupported auth/CMD/ATYP, RSV set, empty domain and a truncated
request; `OpenFlow` failure; `State`; log fields and redaction; `Close`
stopping the listener, force-closing an active flow and cancelling a blocked
`OpenFlow`. It also waits for the accept loop and every handler to finish, so a
leak fails the test. The package passes `go test -race -count=3` on the dev
host.

## Node controller (AJS-62 / B0)

`internal/runtime/nodes.go` is the production caller the B0 gap called out: the
FV session factory and the gate are no longer test-only. It runs only when
`AJS_ACCOUNT_LOGIN=1`; the default build still contacts no vendor host and
binds no gate port.

```go
factory := account.NewFactory(account.Linux423Binary, &fvfacade.HTTPUpgrade{},
    fvfacade.AuthOptions{})
controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
    Store:      st,
    Logins:     loginService, // Result(accountID)
    Sessions:   factory,      // Open(ctx, SessionRequest)
    Reconciler: panel,        // nil when Resin is disabled
    Profile:    account.Linux423Binary,
    MaxServers: 1, // default; AJS_NODE_LIMIT (explicit 0 = all, not live-safe)
    Logger:     logger,
// ...
api.Config{
    LoginHook:         controller.SyncAccount,
    AccountDeleteHook: controller.StopAccount, // gates close before the store cascade
    AccountLogoutHook: controller.StopAccount, // gates close before the store pause
    NodeDrainHook: func(ctx context.Context, nodeID string, drain bool) error {
        if drain {
            return controller.DrainNode(ctx, nodeID)
        }
        return controller.UndrainNode(ctx, nodeID)
    },
}
defer controller.Stop(stopCtx)
```

The flow after `POST /v1/accounts/{id}/actions/login` succeeds (the login
service has already sent `loginSync`):

1. read the retained `LoginResult` and the account's stored device identity
   (`account.DeviceForIdentity`); a missing result or identity is an error;
2. keep only servers whose `sts` is `ok`, sort the server IDs deterministically
   and (when `MaxServers` > 0) keep only the first `MaxServers` of them — a
   `node_limit_skipped` log names the account, limit and skipped count, never a
   credential; the node id is `<accountID>:<serverID>` and the label (also the
   Resin tag) is `<cc>-<account>-<server>` in the lowercase tag alphabet;
3. reuse the gate of a server that already has one, so a repeated login never
   re-authenticates or re-binds;
4. otherwise open one FV session (`account.Factory`) and start one gate on
   `127.0.0.1:<port>` (SOCKS5) plus `127.0.0.1:<port+offset>` (HTTP CONNECT,
   `AJS_GATE_HTTP_OFFSET`, default 1000), allocating from 13000 upward and
   skipping the ports of running gates and store nodes; a bind failure on
   either half retries the next base port;
5. write the node `LIVE` with `gatePort`, `serverId`, protocol `socks5` and the
   session's egress address (`sip`, then `cip`, then `ip`); a per-server
   failure writes it `DOWN` with no gate port and the batch continues;
6. reconcile Resin once (skipped without a panel).

A server skipped by the cap is not desired either: lowering `AJS_NODE_LIMIT`
and resyncing closes the out-of-cap gates, closes their sessions and deletes
their nodes, exactly like a server that disappeared from the login. A server
that disappears from the login has its gate and session closed and its node
deleted. `DrainNode`/`UndrainNode` drive the gate and the store together
and reconcile the panel, and a login refresh does not undo an operator drain.

`StopAccount` is the account-delete and account-logout seam. It closes every
gate and session the controller runs for one account, removes them from the
running set, leaves the other accounts untouched, and does not reconcile the
panel. It deliberately does not touch store nodes either: `store.DeleteAccount`
owns the cascade and `store.LogoutAccount` owns the pause/DRAINING transition,
and the gates must be gone before either runs. A reconcile inside the hook would
republish gates that were just closed, because the account's nodes are still
`LIVE` at that moment; the API handler reconciles after the store mutation
instead (`reconcileAccountMutation`, best-effort and logged on failure), while
`DrainNode`/`UndrainNode` still reconcile directly. It is idempotent — with no
running gate for the account it returns `nil` — and it aggregates close
failures; a failed close still drops the gate from the running set, so a retry
cannot double-close it.

The four API hooks have deliberately different failure policies. `LoginHook`
is best-effort: it runs after the login is recorded, and a hook error is logged
as `account_login_hook_error` without changing the 200 response.
`AccountDeleteHook` is a gate: `DELETE /v1/accounts/{id}` calls it before
`store.DeleteAccount`, and an error is logged as `account_delete_hook_error`
and answers 500 `INTERNAL` without deleting anything, so a deleted account
cannot leave a listening gate or a live FV session behind. A missing account
answers 404 without calling the hook. `AccountLogoutHook` is also a gate:
`POST /v1/accounts/{id}/actions/logout` calls it before
`store.LogoutAccount`, and an error is logged as `account_logout_hook_error`
and answers 500 `INTERNAL` with the account still active and no node
DRAINING, so a paused account cannot leave a listening gate or a live FV
session behind; a missing account answers 404 without calling the hook.
`NodeDrainHook` is also a gate: drain and
undrain call it before `Panel.DrainNode`/`store.DrainNode`, and an error is
logged as `node_drain_hook_error` and answers 500 `INTERNAL` with the node
unchanged, so the store never reports DRAINING while the gate still admits (or
LIVE while it still refuses). A store refusal forwarded by the controller
(draining a DOWN node, a node removed concurrently) keeps its 404/409/503
status. A node the store does not know answers 404
without calling the hook. Delete and logout also republish the panel once after
the store mutation commits (`reconcileAccountMutation`, best-effort and logged
as `account_reconcile_failed`); the pre-mutation hooks never reconcile. All four
hooks are nil unless `AJS_ACCOUNT_LOGIN=1` wired the controller, so the default
build stops no gates, though delete and logout still republish a configured
panel after the mutation.

- Port range: `127.0.0.1:13000` upward (`runtime.DefaultGatePortBase`),
  skipping ports already used by a running gate or a store node. Each gate
  admits `32` concurrent flows across both listeners, mirrored onto the node's
  `capacity`; with `GateHTTP` on, the pair (`p`, `p+GateHTTPOffset`) is reserved
  as a unit, so a node's HTTP port is never another node's SOCKS5 port.
- Store: `ProxyNode.ServerID` (`serverId`, `omitempty`) is additive, so
  `SnapshotVersion` stays 1 and older documents restore with an empty field.
  `Store.PutNode` upserts a runtime node (validating id, account, tag label,
  region, state and port) and preserves the probe/lease fields `InUse`,
  `SuccessRate` and `LastProbeAt`; `Store.DeleteNode` removes the node and every
  lease that pointed at it.
- Node cap (AJS-99/AJS-101): `MaxServers` / `AJS_NODE_LIMIT` (default `1`)
  bounds how many usable servers one account starts; a negative config or env
  value fails startup. The daemon logs `max_servers` (or `all`) with
  `node_controller_enabled`, and a capped sync logs `node_limit_skipped` with
  the account, limit and skipped count — never a credential. The default is
  one active node per account (the vendor's live one-session-per-account
  limit, verified 2026-10-23); multi-account means one node per account, not
  one node per vendor server.
- Live status: B1 (Linux request shape), B3 (FV auth response layering) and
  B4 (data-channel reply/relay keystream) are live-verified for one account
  on 2026-10-23 — see “Default node limit 1 (AJS-101)”. Multi-account and
  Resin remain `[UNVERIFIED]`. The tests use a fake session opener, a real
  `gate.NewServer` on `127.0.0.1:13000` and a fake reconciler; no vendor
  host was contacted for them.

`internal/runtime/nodes_test.go` covers: sync creating two gates on 13000/13001
with the session's egress IP, a SOCKS5 CONNECT relayed through the fake session,
a second sync reusing both ports without new sessions, server removal closing
its gate and deleting its node, the node cap (five fake servers with a limit of
2 starting exactly the first two sorted IDs `s1`/`s2` with no session, gate or
node for `s3`–`s5`, limit `0` starting all five, a negative `MaxServers`
rejected at construction, and lowering the cap on a resync closing the
out-of-cap gates, closing their sessions and deleting their nodes while the
in-cap node keeps serving), drain rejecting a flow with `0x05` and undrain
accepting again, an operator drain surviving a refresh, stop closing every gate
and session, stop-account closing only one account's gates while the other
account keeps serving, stop-account not reconciling and staying idempotent (a
repeat and an unknown account return nil without closing or publishing), the
aggregated close error with a failed close still dropping the gate, partial
failure leaving the working node LIVE and the failed node DOWN, no-node-started
returning an aggregate error that names both nodes, no usable server, missing
login/device preconditions, four concurrent syncs sharing one gate, an injected
gate factory, an end-to-end `DELETE /v1/accounts/{id}` through `api.New` with the
real controller that proves the gates are refused, the store cascade then
removes the nodes, and the handler reconciles once after it with zero LIVE nodes,
an end-to-end `POST /v1/accounts/{id}/actions/logout` through `api.New` with
`AccountLogoutHook` wired to the real controller: the logged-out account's
gates are refused, its sessions are closed and its store nodes are DRAINING,
the other account keeps its gate, session and LIVE node, and a second logout
closes nothing but still publishes once after the store mutation, and
an end-to-end `POST /v1/nodes/{id}/actions/drain|undrain` through `api.New`
with `NodeDrainHook` wired to the real controller: drain answers 0x05 on a new
SOCKS5 CONNECT, undrain answers 0x00, and a stopped controller makes the drain
answer 500 while the node stays LIVE.
`TestAccountLoginHook`, `TestAccountDeleteHook`, `TestAccountLogoutHook` and
`TestNodeDrainHook` pin the API seams; the last one proves hook-before-store,
hook-before-panel, abort-on-error, the missing-node 404 without a hook call,
and the nil-hook store-only path, and `TestAccountLogoutHook` proves the same
hook-before-store and abort-on-error policy for logout.

### Node cap (AJS-99 / AJS-101)

`NodeControllerConfig.MaxServers` is the seam and `AJS_NODE_LIMIT` is its only
production knob (`cmd/ajs-accountd`: parsed with the `snapshotInterval`
policy — unset means `1`, an explicit `0` means all, anything but a
non-negative integer fails startup). `SyncAccount` sorts the usable server
IDs deterministically (`usableServers`) and, when the cap is positive,
truncates to the first `MaxServers` before the desired set is built, so
`removeUndesiredLocked` treats the skipped servers exactly like
removed ones: their gates close, their sessions close, their nodes are
deleted, and a later cap increase starts them on the next login. A truncated
sync logs `node_limit_skipped` (`account_id`, `limit`, `skipped`) — no server
details, no credentials.

The default `1` (AJS-101) is live evidence, not a guess: see
“Default node limit 1 (AJS-101)”. Explicit `0` = all is kept for
catalog/probe research over the 100+ servers a live account exposes
(~165 listeners observed without a cap); it is not live-safe, because the
vendor disconnects every FV data session after the first per account.

### Startup recovery (AJS-239)

A node row is persisted; a gate is a process resource. After a restart the
snapshot used to reload rows that still read `LIVE` while no listener was
bound, and the console claimed a session that did not exist until someone
pressed login. `runtime.RecoverOnStartup` closes that gap before anything can
observe it:

- Every `LIVE` or `DRAINING` node that no in-process gate serves is demoted to
  `STALE` by `store.DemoteNode`: `gatePort` and `egressIp` are cleared,
  `inUse` is zeroed (those flows died with the process) and every lease of the
  node is removed. Exactly one `node.recovered_stale` audit row per node
  (actor `system`, severity `warn`, meta `previousState`/`leasesClosed`).
- It runs in `run()` right after the snapshot is loaded (and after
  `AJS_DROP_DEMO`), before the Resin panel's startup reconcile and before the
  listener: the panel renders `LIVE` rows, so demoting afterwards would
  publish a gate that does not exist. The demoted state is saved immediately
  (`startup-recovery`).
- It is idempotent: only `LIVE`/`DRAINING` rows are considered, so a second
  boot (or a crash-loop) writes no extra audit row and closes no extra lease.
  A `READY` or `DOWN` row never claimed a gate and is left untouched.
- `RecoverOnStartup` takes a `GateSource` (`*NodeController` implements it).
  A node a gate still serves keeps its state, which keeps the function safe
  outside the first boot; the daemon passes `nil` because a fresh process owns
  no gate yet.

`STALE` is part of the node vocabulary in `model.NodeState`, `docs/openapi.json`
(and its byte-identical embed), `dashboard/src/lib/types.ts`, the
`NODE_STATE` label map and both i18n dictionaries, so the console renders a
`Stale` badge instead of an empty cell. It is deliberately not `LIVE`:
`liveNodeCount`, the Resin render set, the health watcher, the catalog
`connected`/`serving` overlays and the scheduler's current-server lookup all
skip it. `POST /v1/nodes/{id}/actions/undrain` on a `STALE` row returns it to
`READY` (not `LIVE`), because only a login can reopen a gate;
`TestUndrainStaleNodeDoesNotClaimLive` pins that.

Optional auto-login (`AJS_AUTO_LOGIN=1`, default off, requires
`AJS_ACCOUNT_LOGIN=1`) reopens the sessions in the background after startup:

- Eligible account: a sealed password (`Store.AccountSecret` succeeds) and
  either a persisted node row or a persisted selection; `paused` (the operator
  logged it out) and `expired` accounts are skipped. The check never logs a
  password and clears the plaintext it opened for the test.
- One `Login` per account per process start, serialized under one lock, in
  store order. A success runs the same `NodeController.SyncAccount` hook the
  HTTP login uses and emits the same `account.login` event
  (`accountId`/`label`/`state`).
- A failure pauses `DefaultAutoLoginBackoff` (60s, jittered ±20% so a fleet
  restart does not hit the vendor in lockstep) before the next account and is
  never retried in that process: no retry loop, no repeated vendor traffic.
- `TestAutoLoginEnabled` pins the default-off switch;
  `TestAutoLoginLogsInOncePerAccount`, `TestAutoLoginSingleAttemptOnFailure`,
  `TestAutoLoginSkipsIneligibleAccounts`, `TestAutoLoginRequiresSealedPassword`,
  `TestAutoLoginUsesSelectionWithoutNode` and
  `TestAutoLoginSerializesConcurrentRuns` cover the runner, and
  `TestRecoverOnStartupDemotesUngatedNodes`, `TestRecoverOnStartupIsIdempotent`,
  `TestRecoverOnStartupKeepsGatedNode`, `TestRecoverOnStartupStaleInNodesView`
  and `TestDemoteNodeGuards` cover the demotion (state, cleared gate/egress,
  closed leases, one audit row per node, unchanged `READY`/`DOWN` rows, and
  the `STALE` value in `GET /v1/nodes`).

## Catalog selection and connect

`internal/runtime/catalog_connect.go` is the guarded switch path behind the selection and
connect/disconnect/reconnect routes; `internal/api/catalog_connect.go` only validates bodies and
maps reasons onto HTTP. The surface is wired when `AJS_ACCOUNT_LOGIN=1` (`*runtime.NodeController`
implements `api.CatalogConnector`); otherwise every write answers 409 `login_disabled` instead of
pretending a catalog exists.

A **selection** is the account's desired server, persisted in the snapshot (`selections`) and
surviving a restart. It starts no session: `PUT /v1/accounts/{id}/selection` validates the server
against the retained login result (unknown → 404 `unknown_server`, ineligible → 409
`server_ineligible`, no login result → 409 `login_required`) and stores it; `DELETE` is idempotent
and never disconnects. `updatedBy` is `operator` for API writes and `system` when a login refresh
drops the target.

**`snapshotId`** is the catalog identity: `cat-` plus 12 hex characters of a SHA-256 over every
retained login result's account id, fetch time and sorted server ids. It is stable across reads and
changes on login or `Forget`. Every selection/connect/reconnect write may send it; a mismatch is
409 `snapshot_changed`, so a page that loaded an older catalog cannot switch to a row that no
longer exists.

**Connect** (`POST .../actions/connect`, body `{serverId,snapshotId?,confirm:true}`) opens the new
session before closing the old one, commits exactly one `LIVE` node and rolls the previous state
back on session/gate/store failure. The same server already running is an idempotent 200 with
`alreadyConnected:true` — no state change, no audit, and no cooldown. **Reconnect** always reopens
the current server, or the selection when nothing runs (`no_target` → 404). **Disconnect** drains
each gate for up to 10 s, closes it, deletes the account's nodes and leases without the generic
node-delete audit, keeps the selection, writes one `catalog.disconnect` audit row and answers
`alreadyDisconnected:true` on a second call. A forced close is reported in `warnings`, not hidden.

The guards are per account, not per process:

- one switch at a time — a concurrent write answers 409 `switch_in_progress`;
- a 5 s cooldown after a real attempt (`DefaultConnectCooldown`), answered as 429 `RATE_LIMITED`
  with `Retry-After`; the idempotent already-connected path does not start one;
- one active session per account (`multiple_sessions`), which is the vendor's live-verified rule;
- the attempt is recorded in `ServerStat` (`attempts`, `successes`, last times, last error class).
  `lastLatencyMs` is always absent: no real latency probe exists, so none is invented.

Reason mapping: 404 `unknown_server`/`no_target`; 409 `login_required`, `login_disabled`,
`account_state`, `server_ineligible`, `snapshot_changed`, `switch_in_progress`, `multiple_sessions`;
429 `connect_cooldown`; 502 `session_open_failed`/`gate_start_failed`; 500 `store_failed`. The
client-safe message and `details` are serialized; the wrapped cause stays in the log.

## Lease allocation

`internal/store/leases.go` allocates and frees the panel's leases; `internal/api/leases.go` serves
them. Create and revoke need the `leases:write` scope for a scoped key (list uses `leases:read`).

**Create** (`POST /v1/leases`, body `{userId, platform, accountId?, ttlSeconds?, idempotencyKey?}`)
answers 201 with `{lease, replayed?, warning?}`.

- **Idempotency.** The `Idempotency-Key` header and the body's `idempotencyKey` are one channel:
  when both are present they must match (400 otherwise), and a key is at most 128 characters and
  must not contain whitespace. A replay returns the original lease with `Idempotency-Replayed:
  true` and allocates nothing; it is resolved before the current-state checks, so a retry after a
  lost response is not refused by a later quota or pool change. Reusing a key for another user or
  platform is 409 `idempotency_conflict`.
- **Guards, in order.** Unknown user → 404 `user_not_found`; suspended user → 409 `user_disabled`;
  exhausted monthly quota → 429 `quota_exceeded`; an active lease for the same user and platform →
  409 `lease_conflict`; unknown pinned account → 404 `account_not_found`; a pin of another platform
  → 400 `invalid_lease`; a pinned account that is paused, failed or expired → 409
  `account_unavailable`; the account already at its concurrency → 429 `concurrency_exceeded`; no
  account of that platform can take another lease → 429 `concurrency_exceeded` or `no_capacity`,
  depending on which guard blocked it.
- **TTL.** `ttlSeconds: 0` uses the user's sticky TTL, then the panel lease TTL; an explicit value
  must be 60 s – 7 days. An unsupported platform, a malformed body or an out-of-range TTL is 400
  (`invalid_lease` / `INVALID_ARGUMENT`).
- **Placement.** Every node that already has an active lease is excluded; a `LIVE` node outranks a
  `READY` one, and seed order breaks the tie. Without a pin the first active account of the
  platform with spare concurrency and a free node wins. The commit appends one `lease.create`
  audit row and increments the node's `inUse`.
- **Publish hook.** When the runtime wires `LeasePublishHook`, a hook failure is reported in the
  201 body as `warning` (and logged as `lease_publish_hook_failed`); the lease stays committed and
  the request is not rolled back.

**Revoke** (`DELETE /v1/leases/{id}`, body `{confirm:true}`) answers 204; a missing confirm is 400.
With a Resin panel the store row is read first (missing → 404), then the panel lease is deleted —
Resin's 404 is treated as already gone, a missing panel platform is 409, and any other panel failure
is 503 with the store row left in place — and finally the store row is released, the node's `inUse`
decremented and one `lease.revoke` audit row written. Without a panel the store release is the whole
path and an unknown id is 404. A hook failure after the commit is returned in the `Warning` header.

`internal/store/leases_test.go` covers allocation, replay, duplicate user/platform, user quota,
account concurrency, no capacity, a suspended user, revoke/expire releasing the node and snapshot
survival; `internal/api/leases_test.go` covers the HTTP retry/conflict contract, both 429 reasons,
validation, the `confirm` gate, the publish-hook warning and the `leases:write` scope.

## Supervisor (AJS-43)

`internal/supervisor` owns the per-node child processes (`docs/PANEL.md` §2,
§8): one process per `(account, server)`, started from an argument array,
ready when it accepts TCP on its node port, stopped with a grace period. It
never goes through a shell, never expands or joins arguments, and never logs an
environment value.

```go
sup := supervisor.New(logger) // nil → slog.Default()
defer sup.Close(context.Background())

h, err := sup.Start(ctx, supervisor.Spec{
	NodeID:       "acc-01:srv-07",
	AccountID:    "acc-01",
	Binary:       "/opt/ajsproxy/ajsproxy",
	Args:         []string{"-listen", "127.0.0.1:12007"},
	Env:          []string{"AJS_NODE=acc-01:srv-07"}, // appended to os.Environ()
	ReadyAddr:    "127.0.0.1:12007",
	ReadyTimeout: 20 * time.Second, // default 10s
	StopGrace:    30 * time.Second, // default 5s
	StderrLines:  100,              // default 100
})
if err != nil {
	return err // *supervisor.ReadyError when readiness timed out
}
fmt.Println(h.NodeID, h.PID, h.StartedAt)
err = h.Wait() // process exit error; nil on a zero exit
<-h.Done()     // already closed once Wait returns

for {
	select {
	case ev := <-sup.Events():
		// ev.Kind: started | ready | exited | stderr
		_ = ev
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

- `Spec`: `NodeID`, `AccountID`, `Binary`, `Args`, `Env`, `ReadyAddr`,
  `ReadyTimeout`, `StopGrace`, `StderrLines`. `Start` rejects an empty node id,
  binary or ready address, and a ready address that is not `host:port`
  (`ErrInvalidSpec`). Defaults are applied to a copy, so the caller's Spec is
  untouched.
- `Handle`: `NodeID`, `PID`, `StartedAt`, `Wait() error`, `Done() <-chan
  struct{}`, plus `Stderr() []string` for the retained ring. Handles are
  copyable values; the internal process state is shared.
- `Event`: `NodeID`, `Kind` (`started`, `ready`, `exited`, `stderr`), `At`,
  `Err`, `Line`. The channel is buffered (64) and never closed, so a slow
  consumer loses events with a warning instead of blocking a child monitor.
- Start: duplicate protection reserves the node id before `exec`, so two
  concurrent `Start` calls cannot race past the check (`ErrDuplicate`). The
  child is `exec.CommandContext` with the args array and
  `os.Environ()+Spec.Env`. `cmd.WaitDelay` is set to a named 2 s bound so a
  child (or a grandchild) that dies while holding a pipe open cannot block
  `Wait` forever; on non-Windows the child is also placed in its own process
  group (`Setpgid`). `started` is emitted after `cmd.Start`, then
  `ReadyAddr` is dialed every 50 ms with a 200 ms dial timeout until
  `ReadyTimeout`; success emits `ready`. A child that exits early fails the
  wait with its exit error, and a timeout returns `*ReadyError` wrapping
  `ErrNotReady`; both kill the child and remove it from the map before
  returning.
- Monitor: one goroutine per node calls `Wait`, drains stderr, records the exit
  error, removes the node from the running set, emits `exited` and closes
  `Done`. stderr is scanned line by line into a bounded ring (`StderrLines`,
  oldest dropped) and emitted as `stderr` events.
- Stop: sends `os.Interrupt` (falling back to the group kill when the platform
  cannot deliver it, as on Windows), waits up to the caller's grace or
  `StopGrace`, then kills and waits for exit. Every kill path (the Close race
  in `Start`, `killAndWait` after a readiness failure, and both force-kill
  points in `Stop`) signals the whole process group on non-Windows
  (`kill -pid`, SIGKILL), so a grandchild that ignores its parent's exit dies
  with the tree instead of being orphaned; on Windows, where there is no POSIX
  process group, the kill falls back to a single-process `Kill` (the
  `WaitDelay` bound still applies). The graceful interrupt stays a
  single-process signal. Repeating `Stop` for a node this supervisor started
  is a no-op; an id that was never started is `ErrNotFound`.
- Signal: forwards any `os.Signal` to the process; unknown ids and calls after
  `Close` are `ErrNotFound`/`ErrClosed`.
- `Get`/`List` return only running nodes; `Close` stops every node concurrently
  with its own `StopGrace`, is idempotent, and makes `Start` return `ErrClosed`
  afterwards. The supervisor itself is safe for concurrent use.
- Logging: `supervisor node starting` carries `node_id`, `account_id`, `pid`,
  `binary`, the argument count, the environment variable *names* only, and
  `ready_addr`; `ready`, `exited`, a stderr-read warning and the stop-grace
  kill warning follow. No argument value, environment value or credential is
  logged.
- Platform split: `proc_unix.go` (`//go:build !windows`) owns `Setpgid` and
  `kill -pid`; `proc_windows.go` (`//go:build windows`) provides the no-op
  group setup and the single-process `Kill` fallback. `supervisor.go` itself
  contains no `syscall` and no CGO; the package API is unchanged.

`supervisor_test.go` re-executes the test binary as the child
(`os.Executable()` plus `-test.run=TestSupervisorHelper` and
`GO_WANT_SUPERVISOR_HELPER=1`); the helper listens on the passed loopback port,
prints `READY` and exits on SIGINT/SIGTERM. Coverage: start/ready/stop with
`PID`/`Get`/`List`/port-closed checks and event order, environment-value
redaction in the log, missing binary, four invalid specs, duplicate node,
readiness timeout (`ErrNotReady` plus `*ReadyError` and cleanup), a child that
exits before ready, stderr events and the bounded ring, unknown `Stop`/`Signal`,
`Signal` on a running node, and `Close` stopping two nodes, idempotency and
`Start` after close. `TestStartSetsWaitDelay` asserts `cmd.WaitDelay` on a
started child. On non-Windows, `grandchild_unix_test.go`
(`TestStopKillsGrandchild`) runs a child that spawns `sh -c` ignoring TERM/INT
and holding the stderr pipe open, writes the grandchild pid to a temp file, and
proves that `Stop` leaves both the child and the grandchild dead (`kill -0`
fails) — no orphan survives the group kill. Every test is loopback-only and
in-process; no vendor host or external binary is involved.

Limitation: the package is not wired to the runtime, the gate, the port
allocator or Resin. Nothing starts an `ajsproxy` child yet; the tests use the
test binary as the child.

## Configuration

| Variable | Flag | Default | Meaning |
|---|---|---|---|
| `AJS_LISTEN` | `-listen` | `127.0.0.1:2261` | admin API address |
| `AJS_TLS_CERT` | — | — | PEM certificate chain for the optional HTTPS admin listener; set together with `AJS_TLS_KEY`. The pair is loaded at startup, so an unreadable, mismatched or non-PEM pair aborts before either listener binds |
| `AJS_TLS_KEY` | — | — | PEM private key matching `AJS_TLS_CERT`; never logged |
| `AJS_TLS_LISTEN` | — | `127.0.0.1:2262` | HTTPS admin address; only meaningful with the pair set. `AJS_TLS_LISTEN` alone, or one half of the pair, fails startup; the address must differ from `AJS_LISTEN`; `AJS_ALLOW_INSECURE=1` still refuses a non-loopback bind |
| `AJS_ADMIN_TOKEN` | `-token` | — | required bearer token unless `AJS_ALLOW_INSECURE=1`; at least 32 characters; the placeholders the repo ships or prints (`change-me`, `changeme`, `change_me`, `admin`, `test`, `password`, `secret`, `local-test-token`, `replace-me-with-openssl-rand-hex-32`) are refused at startup |
| `AJS_ALLOW_INSECURE` | `-insecure` | `0` | `1` disables auth; refused unless `AJS_LISTEN` is loopback (`127.0.0.1`, `::1` or `localhost`) |
| `AJS_ALLOW_PLAINTEXT` | — | `0` | `1` allows a non-`https` entry in `AJS_API_HOSTS` (local lab mock only) |
| `AJS_ALLOW_PLAINTEXT_EXPORT` | — | `0` | `1` allows JSON and CSV account export; the request must also send `confirmPlaintext:"EXPORT"`. Encrypted `.ajsx` export is always available. Off by default because the file is plaintext credentials |
| `AJS_CORS_ORIGINS` | `-cors-origins` | `http://127.0.0.1:18081,http://127.0.0.1:5173` | comma-separated exact origins |
| `AJS_LOG_LEVEL` | `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `AJS_ACCOUNT_LOGIN` | — | `0` | `1` enables the vendor login path and the node controller (login → `loginSync` → one loopback gate per usable server); anything else logs `account_login_disabled` and keeps the store-only refresh |
| `AJS_AUTO_LOGIN` | — | `0` | `1` runs the startup auto-login after the snapshot is loaded: at most one vendor `Login` per account per process start for accounts with a sealed password and a current node or selection (`paused`/`expired` skipped), serialized in store order, followed by the normal `SyncAccount` gate sync and one `account.login` event. A failure pauses a jittered 60s (`48–72s`) before the next account and is never retried. Requires `AJS_ACCOUNT_LOGIN=1`; otherwise startup logs `auto_login_disabled`. Off by default, so a boot contacts no vendor host |
| `AJS_API_HOSTS` | — | `https://login.ajs-app.com,https://login.mmegg.com,https://login.mmegg.com:4443,https://login.mbclubsh.com` | comma-separated control API base URLs, tried in order; a scheme other than `https` fails startup unless `AJS_ALLOW_PLAINTEXT=1` |
| `AJS_ACCOUNT_PROFILE` | — | `linux-4.2.3.0` | version profile: `linux-4.2.3.0` (default, live-tested) or `linux-4.3.0.1` (static analysis only; unsigned live acceptance unverified); an unknown value fails startup when login is enabled |
| `AJS_NODE_LIMIT` | — | `1` | per-account cap on the usable servers the node controller starts (`NodeControllerConfig.MaxServers`): the deterministic server-ID sort keeps the first N, the rest are skipped with a `node_limit_skipped` log; explicit `0` = all (catalog/probe research only, not live-safe). Negative or non-integer fails startup. Default `1` because the vendor allows **one active FV data session per account** (live 2026-10-23: with limit `3` the first two gates answered SOCKS5 `0x05` and the daemon logged `fv: data channel rejected ... fm=disconnected by another connection`, the third worked); with limit `1` the gate egress matched the original app (`222.186.57.63`). Keep `1` for live use — one node per account, not one per vendor server |
| `AJS_CONTROL_CERT_SHA256` | — | — | SHA-256 fingerprint (64 hex chars, `AA:BB:…` or raw) of the control API leaf certificate; set → the login transport pins exactly that certificate instead of chain/hostname verification (see “Control certificate pin”), empty → default verification. Malformed → startup fails |
| `AJS_RESIN_URL` | `-resin-url` | `http://127.0.0.1:2260` | Resin admin API base |
| `AJS_RESIN_ADMIN_TOKEN` | `-resin-token` | — | set → enable the Resin wiring; empty → `resin_disabled` |
| `AJS_SUBSCRIPTION_NAME` | `-subscription-name` | `ajs-live` | Resin local subscription name |
| `AJS_PLATFORM_NAME` | `-platform-name` | subscription name | Resin platform name |
| `AJS_RESIN_RECONCILE` | — | `0` | `1` starts the continuous reconciler: each tick hashes the desired state (`LIVE` node tags/ports, lease identities, account ids) and calls the panel reconcile only when the hash changed. A failed publish backs off `1m→2m→4m→8m→15m` and emits `resin.reconcile_failed` on the transition, `resin.reconcile_recovered` on recovery. Requires `AJS_RESIN_ADMIN_TOKEN`; off by default so a converged daemon makes no periodic Resin calls |
| `AJS_RESIN_RECONCILE_INTERVAL` | — | `5m` | cadence of the converged reconciler (Go duration or bare seconds); invalid or non-positive fails startup |
| `AJS_MASTER_KEY` | — | — | 32-byte secret-encryption key (64 hex characters or base64); set → seal account passwords, empty → `secrets_disabled` |
| `AJS_DEVICE_POOL` | — | on when `AJS_MASTER_KEY` is set | `1` requires `AJS_MASTER_KEY` and enables the sealed device pool; `0` is the test-only kill switch; any other value fails startup. With no master key the pool is off and the legacy plaintext `devices` map is used |
| `AJS_SCHEDULES` | — | `0` | `1` starts the schedule engine; otherwise the read routes and the pure preview still work, but no goroutine runs and every mutation answers 503 |
| `AJS_SCHEDULER_TICK` | — | `1s` | engine tick, `1s`–`60s` (bare seconds or a Go duration); out of range fails startup |
| `AJS_SCHEDULES_MAX_CONCURRENT` | — | `4` | parallel schedule workers, `1`–`16` |
| `AJS_VENDOR_ONE_SESSION` | — | `1` | the scheduler's one-session guard: only `0` lets it queue a second session for the same account (research only; the vendor disconnects the first) |
| `AJS_RENEWAL_REMINDERS` | — | on | `0` disables the daily renewal sweep |
| `AJS_RENEWAL_HOUR` | — | `9` | local hour (`0`–`23`) of the renewal sweep |
| `AJS_RENEWAL_LEAD_DAYS` | — | `30,14,7,3,1` | comma-separated reminder thresholds, days `1`–`365`, at most six values |
| `AJS_TIMEZONE` | — | `UTC` | IANA zone for the renewal sweep; each schedule trigger carries its own `timezone` (empty = UTC); an invalid name fails startup |
| `AJS_RENEWAL_TTL` | — | `6h` | how long a live purchase-page snapshot stays fresh (`time.ParseDuration`); a bad or non-positive value keeps the default. On failure the cache serves its last good snapshot, or the built-in static table on a cold cache |
| `AJS_FLOW_METER` | — | on | `0` disables gate→request-log metering: every completed gate flow (SOCKS5 or HTTP CONNECT) then stops appending a row, so `GET /v1/usage` and `GET /v1/requests` see only the mutation-lab and seed rows. Any other value keeps metering on. A row is appended only after the gate's success reply, through the same 10,000-row cap as every other writer, carries the listener as `protocol` (`socks5`/`http`) and never a URL, header, session id or credential |
| `AJS_GATE_AUTH` | — | `loopback` | SOCKS5 gate authentication method: `loopback` keeps the no-auth compatibility path (no user identity, allowlist or user quota; the loopback peer check and the account quota still apply), `userpass` requires RFC 1929 with the username = `PanelUser.ID` and the password = an API-key secret, then enforces the user's allowlist and monthly quota before admission. Any other value fails startup. `userpass` requires `AJS_FLOW_METER` (user quota is metered from the gate's request rows), and Resin needs the user id and secret in its username/password fields |
| `AJS_GATE_HTTP` | — | off | `1` starts the per-node HTTP CONNECT listener beside the SOCKS5 gate on `127.0.0.1:<gatePort+offset>`; unset or `0` keeps it off; any other value fails startup. Opt-in so an upgrade never binds an extra port pair by itself. Both listeners share one admission pipeline (RFC 1929 key or loopback trust, allowlist, user and account quota, one slot pool, one flow ring), and metered rows carry `protocol:"http"` |
| `AJS_GATE_HTTP_OFFSET` | — | `1000` | distance between a node's SOCKS5 gate port and its HTTP CONNECT port, `1`–`65534` (unset = default; `0`, negative, `65535+` and non-integers fail startup). The controller reserves the pair as a unit and retries the next base port when either half is taken |
| `AJS_HEALTH_PROBE` | — | on | `0` disables the loopback gate sampler and `GET /v1/health/nodes` answers `enabled:false`. Enabled: one TCP connect+close to `127.0.0.1:<gate port>` every 30s with a 3s timeout, at most `AJS_HEALTH_MAX_NODES` watched nodes |
| `AJS_HEALTH_MAX_NODES` | — | `16` | node rings the sampler retains, clamped to 1–64; a non-integer fails startup when the sampler is enabled (an out-of-range integer is clamped, not rejected; the value is not read under `AJS_HEALTH_PROBE=0`). Beyond the cap the sampler evicts an unwatched ring first; when every ring is watched it evicts the least-recently-used one and records it, and the reconciler re-arms a watched-but-evicted key on its next 15 s sweep (one attempt per key per sweep, one log line per minute) |
| `AJS_WEBHOOK_ALLOW_HTTP` | — | `0` | `1` allows an `http://` webhook receiver that is not loopback; otherwise plaintext is accepted only for `127.0.0.1`/`::1`/`localhost` |
| `AJS_WEBHOOK_ALLOW_PRIVATE` | — | `0` | lab/test opt-out: `1` lets a receiver point at loopback, RFC 1918, IPv6 unique-local and link-local addresses. Without it a resolved private address is rejected (an unresolvable name is allowed at registration and re-checked at dial time); cloud metadata names (`metadata`, `metadata.google.internal`) are rejected either way |
| `AJS_BACKUP_ALLOW_RESTORE` | — | `0` | `1` lets `POST /v1/backup/restore` write the panel files while the daemon runs; the request still needs `dryRun:false` and `confirm:"RESTORE"`, and the response sets `restartRequired`. Off by default because the running process would overwrite the restored `state.json` with its stale in-memory panel on the next snapshot. Startup logs a warning when set |
| `AJS_STATE_DIR` | — | `./data` | state directory (created `0700`); holds `state.json`, `secrets.json`, `device-secrets.json` and `schedule-fire.log` |
| `AJS_STATE_FILE` | — | `$AJS_STATE_DIR/state.json` | full-state JSON snapshot; loaded before serving and rewritten on every snapshot |
| `AJS_SNAPSHOT_INTERVAL` | — | `30s` | Go duration between background snapshots; invalid or non-positive fails startup |
| `AJS_DROP_DEMO` | — | `0` | `1` runs `Store.PurgeDemo` once after loading the snapshot and before the listener starts: removes exactly the `seed.go` mock rows (`acc-01`…`acc-06`, `node-01`…`node-12` plus any node of a removed seed account, `lease-01`…`lease-08`, `u-01`…`u-04`, `key-01`…`key-06`, `req-001`…`req-040`, `audit-001`…`audit-020`), preserves `acc-07`, its runtime node and every other live row, logs the per-collection counts, and saves the snapshot immediately. Destructive: the purge is written through the normal atomic snapshot and survives a restart, so copy the state file first if you need to undo it. Unset it after the first run; a second run is a no-op |
| `AJS_UPDATE_REPO` | `-update-repo` | — | GitHub `owner/repo` checked by `GET /v1/system/update`; empty disables the check (the endpoint answers `enabled:false` and no network is made). Read-only: the daemon never executes an update — the host runs `deploy/update.sh` |
| `AJS_UPDATE_API_BASE` | `-update-api-base` | `https://api.github.com` | GitHub API root for the update check; tests point it at an httptest server |

### TLS listener (AJS-276)

The admin API stays plaintext on `AJS_LISTEN` (loopback by default). Setting `AJS_TLS_CERT` and
`AJS_TLS_KEY` adds a second HTTPS listener on `AJS_TLS_LISTEN` that serves the same
`http.Handler`: same routes, same bearer/scoped-key auth, same request log. Only the transport
changes.

- Off by default. Startup logs `tls_disabled` when the pair is unset and `tls_listener_started`
  with the address (never the key) when it is on.
- Partial configuration fails startup: `AJS_TLS_CERT` without `AJS_TLS_KEY`, the reverse, or
  `AJS_TLS_LISTEN` without the pair. An unreadable, mismatched or non-PEM pair also aborts before
  the port is bound.
- TLS 1.2 is the floor (1.3 preferred) with ECDHE-AEAD suites only — no CBC, static RSA, RC4 or
  3DES.
- Every response, errors included, carries `Strict-Transport-Security: max-age=31536000`. The
  directive is deliberately scoped: no `includeSubDomains` (the admin API is usually one host
  name, and the daemon must not pin unrelated sibling subdomains of the operator's domain) and no
  `preload`.
- `AJS_ALLOW_INSECURE=1` keeps refusing non-loopback binds: the HTTPS listener cannot be used to
  publish the unauthenticated mode.
- The bearer token is only as safe as the transport it crosses. TLS protects it on the wire; it
  adds no authentication, revocation or second factor.

```bash
# lab: self-signed pair, loopback only
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout privkey.pem -out fullchain.pem \
  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
AJS_TLS_CERT=./fullchain.pem AJS_TLS_KEY=./privkey.pem \
  AJS_ADMIN_TOKEN=$(openssl rand -hex 32) ./ajs-accountd
# msg="tls_listener_started" addr=127.0.0.1:2262 min_version=1.2
curl --cacert fullchain.pem https://127.0.0.1:2262/healthz
```

For the compose deployment see
[`../deploy/README.md`](../deploy/README.md#tls-for-the-admin-api-optional-ajs-276).

## Platform routes (AJS-205)

Four read views, the webhook registry and the panel backup routes are wired in `cmd/ajs-accountd`:

| Route | Source |
|---|---|
| `GET /v1/renewal/plans` | `internal/renewal` cached public purchase page; response carries `source` (`live`/`cache`/`static`) and `stale`. A failed refresh is soft and still answers 200 with the fallback table. The fetch refuses redirects (a 3xx is an ordinary non-2xx failure, so the cache or static table answers) and sends no cookie jar and no authorization header. The prices are the vendor's public list; the daemon never posts to a payment endpoint or calls a purchase API |
| `GET /v1/renewal/accounts/{id}` | account `expiresAt` → `daysLeft`, `urgency` (`none`/`soon`/`due`/`overdue`) and the official purchase URL (never carries an account id or token); unknown account → 404 |
| `GET /v1/usage?from=&to=&account=&userId=` | `internal/usage` over `store.Requests()`; `from`/`to` accept RFC 3339 or `YYYY-MM-DD` (UTC), absent = trailing 30 days; invalid, reversed or >366-day windows → 400. The body is `{from,to,requests,bytes,errors,accounts,users,days,topTargets,truncated,rowsRetained,retentionDays}`: `rowsRetained` is the request-log cap (10 000) and `retentionDays` the inclusive UTC-day span the retained rows actually cover (`0` = no rows), so a wide window can be labelled as partial. Rows come from completed gate flows (`AJS_FLOW_METER`, on by default; those rows carry `source:"gate"`), the mutation lab (`source:"mutation-lab"`) and the seed (`source:"proxy"`) |
| `GET /v1/health/nodes` | `internal/health` sampler over live gate ports; one row per watched node with `count`/`ok`/`successRate`/`p50Ms`/`p95Ms`/`minMs`/`maxMs`/`state` and the newest sample. `AJS_HEALTH_PROBE=0` → `enabled:false`; `AJS_HEALTH_MAX_NODES` bounds the retained rings |
| `GET/POST /v1/webhooks`, `PATCH/DELETE /v1/webhooks/{id}`, `GET /v1/webhooks/{id}/deliveries` | `internal/webhook` dispatcher plus the persisted receiver rows in `state.json` |
| `POST /v1/backup` | seals `state.json`, `secrets.json` and `device-secrets.json` into one passphrase-encrypted `.ajsb` envelope (AES-256-GCM over a deterministic tar, PBKDF2-SHA256 at 600 000 iterations, the whole header authenticated) and answers the file itself with `no-store` and `X-AJS-Backup-Blobs`. Admin-only |
| `POST /v1/backup/inspect` | header-only inspect of a posted envelope; with `passphrase` it also returns the decrypted manifest (`name`, `size`, `sha256`, `required`). It never touches the state directory and returns no blob, so it works on a daemon with no state dir. Admin-only |
| `POST /v1/backup/restore` | compares an envelope with the panel files and returns a per-blob diff (`added`/`removed`/`changed`/`same` with counts, names and byte totals). `dryRun` defaults to **true**; an apply needs `dryRun:false` and `confirm:"RESTORE"`, is refused while any node is `LIVE`, and writes only while the daemon is stopped unless `AJS_BACKUP_ALLOW_RESTORE=1`. Admin-only |

Health sampling follows the live gate set: a successful connect/switch watches
the committed gate, login watches every gate the sync opened, and
disconnect/logout/delete unwatch it. The sampler is started with the signal
context and stopped after the node controller closes its gates. It dials
**`127.0.0.1` only** — the gate binds loopback, so a probe can never leave the
host — once every **30 s** with a 3 s dial timeout, keeping **120 samples per
node** (one hour) and at most **`AJS_HEALTH_MAX_NODES`** watched nodes
(default **16**, clamped 1–64) with 8 probes in flight. Beyond that cap the
sampler evicts an unwatched ring first; when every retained ring is watched it
evicts the least-recently-used one and records the key, and the reconciler
re-arms a watched-but-evicted key on its next sweep (one attempt per key per
15 s sweep, one log line per minute). Percentiles,
`minMs` and `maxMs` are computed over successful probes only; `successRate` is
`ok/count` over the whole window and `last` is the newest sample whatever its
outcome. States are `ok`, `degraded` (success rate below 0.9 or p95 above
800 ms) and `down` (three consecutive failures or a success rate below 0.5);
failed samples carry `errorClass` (`timeout`, `refused`, `unreachable`,
`other`). A node that was never watched is omitted from the response.

Webhook events emitted by the daemon: `account.login`, `account.logout`,
`node.connected`, `node.disconnected`, `node.switched`, `schedule.fired`,
`schedule.failed` (the scheduler calls `Config.OnRun` after recording a run),
`account.renewal_due`/`account.expired` (the once-per-local-day renewal
sweep calls `Config.OnReminder` for each newly armed
`(account, threshold, expiresAt)` mark: a due threshold becomes
`account.renewal_due`, a lapsed membership becomes `account.expired`, and the
payload carries only `accountId`, `expiresAt`, `daysLeft` and `threshold`),
`device.retired` (`{deviceId, accountId?, reason}`, emitted by the retire
handler after the record reaches `retired`) and `audit.critical`
(`{auditId, action, actor, target}`, emitted for every audit row whose
severity is `critical`; the hook is wired through the store and skips actions
under the `webhook.` prefix so the webhook path cannot re-emit its own audit
rows). An empty `events` list subscribes to everything. Each delivery carries
`X-APC-Event`,
`X-APC-Delivery`, `X-APC-Timestamp` (Unix seconds) and
`X-APC-Signature: sha256=<hex HMAC-SHA256 of "timestamp.body">`; the payload is
scrubbed of credential-shaped keys before it leaves the process.

**SSRF policy.** A receiver URL is operator-supplied but a compromised panel
must not turn the daemon into a probe of its own host or the cloud metadata
service. Registration resolves the host (literal and DNS) and rejects
loopback, RFC 1918, `169.254.0.0/16`, `::1`, `fc00::/7`, `fe80::/10`,
unspecified/multicast addresses and the names `metadata` /
`metadata.google.internal`. An unresolvable name is allowed (DNS may be
transient), and the client transport re-checks the address the resolver
actually produced in its `DialContext` control hook, so a DNS rebind between
registration and delivery is refused too. `AJS_WEBHOOK_ALLOW_PRIVATE=1` is
the lab/test opt-out for the address classes; the metadata names stay blocked.

**Delivery.** The dispatcher has a 256-event queue and four workers, keeps
per-endpoint FIFO order, times each attempt out after 10 s and retries up to
five attempts with backoff 1 s, 5 s, 30 s, 2 min and 10 min. A delivery that
exhausts its attempts is recorded as `dead_letter` and is not retried again;
the dispatcher reports it once per delivery id (a fixed 256-id ring), and the
composition root appends one `warn` `webhook.dead_letter` audit row — actor
`system`, target the endpoint id, detail `<event> after <n> attempts` — so the
URL, signing secret and payload never enter the audit trail. An identical event
id inside the 60 s dedupe window is dropped. Every attempt is also appended to
the persisted delivery history — 50 rows per endpoint, 500 in
total, oldest dropped first — from one background goroutine fed by a bounded
queue, so a slow sink costs dropped history (counted) and never delays a
delivery. The append itself is memory-only: the periodic state snapshot carries
it to disk, so a delivery never waits on a write or an fsync. Both sinks
isolate panics: a panicking hook is logged and the worker continues, so one bad
row cannot stop an endpoint.
`GET /v1/webhooks/{id}/deliveries` merges those persisted rows (authoritative;
they survive a restart) with the live in-memory ring (the last 200 attempts per
endpoint), dedupes by delivery id and answers newest first; `?limit` pages it
(default 200, max 500; a non-positive value is 400). Receivers must be `https`
unless they are loopback or `AJS_WEBHOOK_ALLOW_HTTP=1`, must not resolve to a
private address unless `AJS_WEBHOOK_ALLOW_PRIVATE=1`, and the registry is
capped at 32 endpoints.
`PATCH /v1/webhooks/{id}` changes only `enabled`: enabling a receiver whose
signing secret is gone is a 409 because every delivery must be signed, and the
URL/events/description are fixed at create time (recreate the endpoint to
change them).

**Webhook secret storage.** The signing secret is returned exactly once by
`POST /v1/webhooks` and never appears in a read response. With
`AJS_MASTER_KEY` set it is sealed in `$AJS_STATE_DIR/secrets.json` under the
`webhook/<id>` namespace (AAD-bound, pruned with the endpoint). Without a
master key the secret is kept in process memory only: the endpoint row still
survives a restart, but the secret does not, so the endpoint reports
`secretAvailable:false` and must be recreated. This is the safer default
compared with writing the secret into `state.json` in plaintext.

**Event replay and streaming.** `internal/events` keeps a bounded, scrubbed
ring of the same customer events the webhook dispatcher sends, and
`internal/api/events.go` is only the HTTP contract. The daemon wires **one**
ring (`api.DefaultEventRingCapacity`, 4096 events) and the sink mirrors every
event into it before fanning out, so the replay API and the webhook receivers
cannot drift. `GET /v1/events?since=&limit=&wait=` replays oldest-first and
answers `{events,nextCursor,oldestCursor,dropped}`; `wait` (a Go duration,
capped at 25 s) holds a request whose cursor is current until the next event
arrives, so a poll never busy-loops. `GET /v1/events/stream` is
`text/event-stream`: one `id:`/`event:`/`data:` frame per event (the `data` is
the same JSON object as a page entry), the resume point comes from
`Last-Event-ID` (a browser reconnect) or `since`, a `: keepalive` comment every
15 s keeps proxies from closing an idle stream, and the response clears the
server `WriteTimeout` because the stream is intentionally long-lived. The
stream subscribes after the first replay and then reads the ring again, so an
event published during a long replay is delivered exactly once. IDs are
monotonic (`evt-000123`). A malformed cursor is 400 `INVALID_ARGUMENT`; a
cursor whose event was evicted is 410 `CURSOR_EXPIRED` with
`details.oldestCursor`, the point to restart from — the ring never silently
replays a gap. A subscriber that cannot keep up loses events into `dropped`
(a lifetime counter) and recovers by reconnecting with its last id. Both routes
need `events:read`; a scoped key restricted by `accountIds`/`regions` is
refused 403 because the ring is global and a per-account filter would be
guesswork.

**Panel backup and restore.** `internal/backup` owns the envelope and
`internal/api/backup.go` only the HTTP contract. A backup carries `state.json`
plus whichever of `secrets.json`/`device-secrets.json` exist, so it contains
sealed credentials: no handler logs a path, blob or passphrase. The passphrase
is 12–1024 bytes, the envelope is capped at 96 MiB (64 MiB of blob data), and
a wrong passphrase, a tampered header or a truncated payload is a 400
`INVALID_ARGUMENT`. Restore never deletes a file the envelope omits — an older
backup without `secrets.json` cannot silently drop the live sealed secrets —
and it writes each file through a `0600` temp-file rename. Codes:

| Code | HTTP | Meaning |
|---|---|---|
| `BACKUP_BUSY` | 409 | another backup or restore holds the panel-file lock |
| `BACKUP_LIVE_NODE` | 409 | a node is `LIVE`; `details.liveNodes` and `details.firstLiveNode` name it |
| `BACKUP_DAEMON_RUNNING` | 409 | an apply was sent while the daemon runs and `AJS_BACKUP_ALLOW_RESTORE` is not `1` |
| `BACKUP_UNAVAILABLE` | 503 | a create or restore was called on a daemon with no state directory (inspect still works) |
| `CONFLICT` | 409 | `state.json` is missing, so there is nothing to seal |
| `INVALID_ARGUMENT` | 400 | bad passphrase, malformed or oversized envelope, missing `confirm:"RESTORE"`, or a backup without `state.json` |
| `INTERNAL` | 500 | a read or write failed; a failed apply reports the files already written in `details.written` |

### Verifying webhook signatures

Every delivery is signed, and a receiver should verify before parsing the
body. `X-APC-Timestamp` is the Unix second at which that attempt was signed
(each retry is re-signed at send time) and `X-APC-Signature` is
`sha256=<lowercase hex HMAC-SHA256(secret, timestamp + "." + raw body)>`.
`X-APC-Event` and `X-APC-Delivery` (`<eventId>:<attempt>`) are not covered by
the digest, so use them for routing only after the signature passes. Verify
the exact raw bytes — a re-serialized JSON body will not match — with a
constant-time compare, and reject a timestamp more than **300 s** from local
time: five minutes leaves room for clock skew and still stops a replay, and
because every attempt is re-signed it does not reject the later retries.

Node:

```js
const crypto = require('node:crypto')

function verify(secret, headers, rawBody) {
  const timestamp = headers['x-apc-timestamp'] || ''
  const received = headers['x-apc-signature'] || ''
  if (!/^\d+$/.test(timestamp)) return false
  if (Math.abs(Date.now() / 1000 - Number(timestamp)) > 300) return false
  const expected = 'sha256=' + crypto.createHmac('sha256', secret)
    .update(timestamp + '.' + rawBody).digest('hex')
  const a = Buffer.from(expected), b = Buffer.from(received)
  return a.length === b.length && crypto.timingSafeEqual(a, b)
}
```

Go:

```go
func verify(secret, timestamp, signature string, body []byte) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
```

In both snippets `timestamp` must be the header value exactly as sent — do not
re-format it — and `rawBody`/`body` must be the bytes received, not a decoded
and re-encoded document.

## Scoped API keys

`internal/api/scoped_keys.go` and `internal/store/scoped_keys.go` implement least-privilege API
keys. A key secret is `ajs_sk_` plus 48 hex characters (24 random bytes); only its SHA-256 and the
first 16 characters (the `prefix`) are persisted. `POST /v1/scoped-keys` returns `{key,secret}`
with **201** and the secret exactly once — no read response ever carries it. A key may carry an
`expiresAt` (RFC 3339; empty = never), a `label` (≤ 80 characters) and a user owner;
`DELETE /v1/scoped-keys/{id}` sets `revokedAt` and keeps the row, and `lastUsedAt` moves on every
authenticated request.

Authentication tries the admin bearer token first in constant time, then resolves an `ajs_sk_`
secret by its hash. A missing or unknown token is 401 `UNAUTHORIZED`; `/healthz` stays open and
`AJS_ALLOW_INSECURE=1` skips the check entirely. The router then enforces one scope per route:

| Scope | Routes |
|---|---|
| `accounts:read` | `GET /v1/accounts`, `GET /v1/accounts/{id}`, `GET /v1/accounts/{id}/device`, `GET /v1/accounts/io/capabilities`, `POST /v1/accounts/export` |
| `accounts:write` | `POST /v1/accounts`, `PATCH`/`DELETE /v1/accounts/{id}`, login/logout, `POST /v1/accounts/import`, `POST /v1/accounts/bulk-delete` |
| `catalog:read` | `GET /v1/nodes`, `GET /v1/accounts/{id}/servers`, `GET /v1/accounts/{id}/selection`, `GET /v1/topology` |
| `catalog:write` | `PUT`/`DELETE /v1/accounts/{id}/selection`, connect/disconnect/reconnect, node drain/undrain/probe |
| `leases:read` | `GET /v1/leases` |
| `leases:write` | `POST /v1/leases`, `POST /v1/leases/{id}/actions/rotate`, `DELETE /v1/leases/{id}` |
| `requests:read` | `GET /v1/requests` |
| `audit:read` | `GET /v1/audit`, `GET /v1/audit/export.csv` |
| `metrics:read` | `GET /metrics`, `GET /v1/metrics` |
| `lab:read` | every `/v1/lab/*` route, including `POST /v1/lab/egress-check` |
| `settings:read` | `GET /v1/settings`, schedule reads and preview |
| `settings:write` | `PATCH /v1/settings`, schedule create/patch/delete/run |
| `usage:read` | `GET /v1/usage` |
| `renewal:read` | `GET /v1/renewal/plans`, `GET /v1/renewal/accounts/{id}` |
| `health:read` | `GET /v1/health/nodes` |
| `events:read` | `GET /v1/events`, `GET /v1/events/stream`; a key restricted by `accountIds`/`regions` is refused 403 because the ring is global |
| `admin` | every route, same as the admin token |

Routes absent from the table are admin-only — the scoped-key registry, users, API keys, requests
mutation, reconcile, update, the webhook registry (`/v1/webhooks*`), the panel backup routes
(`/v1/backup*`) and `/v1/device-pool*` — so a key without `admin` gets 403 `FORBIDDEN` there.
The three read scopes cover one read route family each; they grant no write and do not bypass the
account/region filter. `accountIds` and `regions` narrow what a key may **read**: a `GET`/`HEAD` for an
account outside the list, or a list filtered by such an `account_id`, answers 403 `FORBIDDEN`;
writes are governed by the scope alone. An unknown scope is a 400, and a create needs at least one
scope. The three platform reads follow the same rule:
`GET /v1/renewal/accounts/{id}` answers 403 for an account outside the key's list (404 when the id
is unknown), `GET /v1/usage` aggregates only visible accounts — an explicit out-of-scope `account`
filter is a 403, and an unknown label yields an empty 200 summary — and `GET /v1/health/nodes`
omits nodes belonging to invisible accounts. `GET /v1/events` and `GET /v1/events/stream` cannot
be narrowed that way, so a restricted key is refused both. The scope vocabulary is the OpenAPI
`Scope` enum; the four read scopes were appended after `settings:write` and before `admin`.

Each key also carries a per-minute request budget: `rateLimitPerMin` (default **60**; an explicit
`0` means unlimited). The token bucket is per key, and every scoped-key response carries
`X-RateLimit-Limit`, `X-RateLimit-Remaining` and `X-RateLimit-Reset` (Unix seconds); a refused
request answers 429 `RATE_LIMITED` with `Retry-After` set to one refill interval, so a retry at
that point finds a token. A 403 still carries the headers, so a caller can tell a scope failure
from a budget failure. The admin bearer token is never rate-limited.

## Secret storage

`internal/secret` is a stdlib-only file-backed store for account passwords.
When `AJS_MASTER_KEY` holds a 32-byte key, `cmd/ajs-accountd` opens
`$AJS_STATE_DIR/secrets.json` and `POST /v1/accounts` seals the password before
the account is stored.

- Key format: 64 hex characters or standard base64 (padded or raw), exactly 32
  bytes. A malformed key fails startup with an `AJS_MASTER_KEY` error; a missing
  key logs `secrets_disabled` and passwords are validated and discarded as
  before. The key is never logged.
- Envelope: `v1` + 12-byte random nonce + AES-256-GCM ciphertext/tag, base64 in
  the JSON document. The account id is the additional authenticated data, so a
  sealed blob cannot be moved to another account.
- File: `$AJS_STATE_DIR/secrets.json`, mode `0600`, rewritten on every seal and
  delete through a `0600` temporary file and an atomic rename. A missing file
  starts an empty store.
- Rotation: `secret.Store.Rotate(newKey)` re-seals every entry under the new key
  and rewrites the file; the old key stays active if re-sealing or the write
  fails. There is no HTTP endpoint for rotation.
- Deletion: `secret.Store.Delete(id)` drops one entry and rewrites the file
  before returning; a missing entry is a no-op, so a retried delete converges,
  and a failed rewrite restores the entry. `store.DeleteAccount` prunes the
  sealer before the in-memory cascade (a sealer write may fsync, so it runs
  outside the store lock); a sealer failure answers 503 and leaves the account
  and its secret in place, so no delete can drop the account while orphaning the
  sealed blob on disk. `Store.PurgeDemo` prunes the same way for its bulk
  removal.
- Access: `store.AccountSecret(id)` opens a password for the account client. It
  is not exposed over HTTP and `model.Account` has no credential field.
- Limitation: the sealed blobs are duplicated in `secrets.json` (the sealer's
  own file) and in the full snapshot. The snapshot is authoritative on load;
  see Persistence below. A snapshot saved before a delete keeps its copy until
  the next save, while `secrets.json` is pruned immediately. SQLite (PANEL §6)
  is a later slice.

## Account import/export and bulk delete

`internal/store/account_io.go` owns the formats and the atomic store apply;
`internal/api/account_io.go` parses the strict wrappers, holds one account-IO
mutex (`TryLock`; a second concurrent call answers 409) and translates errors.
No function in either file logs a credential, a passphrase or a row password.

| Route | Purpose |
|---|---|
| `GET /v1/accounts/io/capabilities` | `encryptedExport` (always true), `plaintextExport` (the env gate), `maxImportRows` 1000, `maxExportAccounts` 1000, `maxDeleteIDs` 100, `maxBodyBytes`, `maxImportBodyBytes` (4 MiB) |
| `POST /v1/accounts/export` | `{ids?\|filter?, format: encrypted\|json\|csv, passphrase?, includeDevice?, confirmPlaintext?}` → the file, not a JSON wrapper |
| `POST /v1/accounts/import` | `{source:{kind: json\|csv\|encrypted, content, passphrase?}, options:{…}}` → `ajs-accounts-import-result` |
| `POST /v1/accounts/bulk-delete` | `{ids[], dryRun?, mode: continue\|stop}` → `ajs-accounts-bulk-delete-result` |

**Export.** Encrypted `.ajsx` is always available and is the default: PBKDF2-HMAC-SHA-256
(600 000 iterations; an envelope may declare 100 000–2 000 000), AES-256-GCM with a 12-byte nonce,
and the header authenticated as AAD. The document is `ajs-accounts` v1 with account rows and, by
default, each account's device identity. JSON is the same document in plaintext; CSV is one row per
account and refuses device identity (no column for it). Both plaintext formats need
`AJS_ALLOW_PLAINTEXT_EXPORT=1` **and** `confirmPlaintext:"EXPORT"`; a passphrase is valid only for
encrypted export. `ids` and `filter` are mutually exclusive, at most 1 000 accounts per file. The
payload is built in memory before any header is written, so an encryption failure cannot leave a
truncated credential file. The response carries `Content-Disposition`, `Cache-Control: no-store`,
`X-AJS-Export-Count`, `X-AJS-Export-Missing-Secrets` and `X-AJS-Export-Devices`; the audit row
`account.export` carries format and counts only.

**Import.** The preview is the same route with `options.dryRun:true`: it parses, validates and plans
every row without touching the store. Options default to `mode:"atomic"`, `matchBy:"label"`,
`onConflict:"skip"`, `initialState:"paused"`, `idPolicy:"new"`,
`devicePolicy:"restore_if_missing"`, timestamps preserved. `mode:"partial"` applies the valid rows
and reports the rest; `atomic` applies nothing when any row fails. The response is per-row
`action` (`create|update|rename|skip|fail`), optional `id`, warnings (`password_kept`,
`device_restored`, `id_ignored`, …) and counts where `created+updated+skipped+failed == total`.
Applied creates get their real ids; updated accounts have their retained login result forgotten and
the snapshot is saved before the response. Passphrases and row passwords are cleared after use.

**Bulk delete.** At most 100 ids, deduplicated; `dryRun` reports `would_delete` with node/lease
counts, `mode:"stop"` marks the remaining rows `skipped` after the first failure. Each row runs the
same delete hook as `DELETE /v1/accounts/{id}` (stop gates and sessions first) and then the store
delete, so a hook or sealer failure leaves that account and its secret in place. Per-row statuses
are `deleted`, `would_delete`, `not_found`, `failed` and `skipped`, each with a code and a message
that never carries a credential.

## Persistence (AJS-45)

The panel store is durable through a whole-store JSON snapshot. This is a
deliberate v0.1 stopgap: `docs/PANEL.md` §6 describes the target SQLite schema,
but this slice keeps the in-memory store and serialises it as one document.

```
$AJS_STATE_FILE (default $AJS_STATE_DIR/state.json)
  version          2  (v1 is accepted; its device map migrates at startup)
  accounts         []Account        nodes      []ProxyNode
  leases           []Lease          users      []PanelUser
  keys             []APIKey         requests   []RequestLog
  audit            []AuditEvent     settings   PanelSettings
  keyHashes        {keyId: sha256}  secrets    {accountId: base64(sealed)}
  deviceBindings   {accountId: deviceId}
  devicePool       []DeviceRecord   (index only; raw material is sealed elsewhere)
  selections       {accountId: Selection}
  serverStats      {"accountId:serverId": ServerStat}
  schedules        []Schedule       scheduleRuns []ScheduleRun
  scheduleFires    []ScheduleFire   reminderMarks {key: ReminderMark}
  scheduleSeq runSeq
  accountSeq userSeq keySeq mutationSeq auditSeq
```

- `Store.Snapshot()` copies every collection, counter, key hash and sealed
  secret under one `RLock`, then marshals outside the lock. Plaintext
  credentials never enter the document: `secrets` holds the AES-256-GCM
  envelopes from `internal/secret`, and `keyHashes` holds only SHA-256 digests.
- `Store.Save(path)` writes the document to a `0600` temporary file in the
  target directory, fsyncs it, renames it over the target, then best-effort
  fsyncs the directory. A reader sees the old or the new document, never a
  partial one. The parent directory is created `0700` when missing.
- `Store.Load(path)` reads the file and calls `Restore`. A missing file wraps
  `os.ErrNotExist`, so `cmd/ajs-accountd` keeps its empty startup store.
  Corrupt JSON, empty data, an unknown `version` or duplicate ids are errors.
  `Restore` decodes and validates into a local `Snapshot` before taking the
  write lock, so a rejected file leaves the running store untouched.
- `Restore` rebuilds the denormalised node counts and raises every counter to at
  least the highest identifier already present (`acc-`, `u-`, `key-`,
  `req-mut-`, `audit-`). A document with stale counters therefore cannot hand
  out a colliding id after a create/delete cycle.
- Demo purge: `AJS_DROP_DEMO=1` makes `cmd/ajs-accountd` call
  `Store.PurgeDemo` after `Load` and before the listener and snapshot ticker
  start, then log `demo_purged` with per-collection counts and save the
  snapshot (`reason=demo-purge`). It removes exactly the `seed.go` rows and
  any node whose account is a removed seed account; `acc-07`, its runtime
  node, created users/keys, mutation-lab requests and later audit events
  survive. The call is idempotent and never logs ids or credentials. The
  purge is destructive but goes through the normal atomic snapshot, so it
  survives a restart; copy the state file first if you need to undo it.
- `cmd/ajs-accountd` loads the snapshot before building the Resin panel and
  before the listener starts. It then saves every `AJS_SNAPSHOT_INTERVAL`
  (default `30s`) and once more when `run` returns after a SIGINT/SIGTERM
  shutdown. Logs are `state_loaded`, `state_missing`, `state_saved`,
  `demo_purged` and `state_save_failed`; they carry the path, reason and
  counts only, never a secret.
- Duplication with `secrets.json`: the sealer still writes its own file on every
  seal, so a sealed envelope can exist in both files. On startup the snapshot
  replaces `store.secrets`, making it authoritative for which accounts have a
  secret. `Store.DeleteAccount` prunes both copies: the sealer file immediately,
  the snapshot map at the next save. A snapshot restored under a different
  `AJS_MASTER_KEY` keeps the envelopes but `AccountSecret` cannot open them; the
  key is never stored. Rotation is not wired to the panel store:
  `secret.Store.Rotate` re-seals only its own file, so the snapshot's map keeps
  old-key blobs and would need the same entries re-sealed before they can be
  opened again.
- Limitations: the format is JSON rather than SQLite; every save rewrites the
  whole document, so cost grows with request/audit history; a crash can lose up
  to one snapshot interval of mutations; the only schema migration is v1 → v2
  (the plaintext device map is converted to pool records at startup, one way: a
  v2 document is rejected by a v1 binary); and there is no multi-process writer
  lock. Keep one daemon per state file.

## Device identity pool

`internal/store/device_pool.go` replaces the v1 plaintext `devices` map with a record index plus
separately sealed material. The pool is on by default when `AJS_MASTER_KEY` is set (it needs the
sealer); `AJS_DEVICE_POOL=0` is the test-only kill switch. Without a master key the daemon keeps
the legacy map and the pool is off.

- **Two layers.** The snapshot carries only the non-secret index (`devicePool` records and
  `deviceBindings`); the raw vendor identifiers and machine code live in
  `$AJS_STATE_DIR/device-secrets.json`, sealed per record id with AES-256-GCM. The file's key is
  derived with HKDF-SHA-256 from `AJS_MASTER_KEY` and the info string `ajs/device-pool/v1`, so the
  password store and the device store never share a key. Neither file ever logs or returns a raw
  identity.
- **States.** `available` (generated, unbound), `assigned` (bound to one account), `retired`
  (tombstone, unbound, kept for restore) and `quarantined` (frozen, no restore edge). A record
  carries a fingerprint, hashes, non-secret claims, an account key (platform family + username)
  with aliases, history (capped) and the profile it was generated for.
- **Login binding.** `BindForLogin` is first-writer-wins: retries, restarts and concurrent first
  logins get the same identity, and a bound record is re-opened from sealed material and verified
  against its fingerprint. Missing or corrupt material fails the login closed
  (`DEVICE_MATERIAL_MISSING`) — an identity is never silently regenerated.
- **Re-add restore.** Deleting an account retires its record instead of erasing it. When an
  account with the same account key is created and logs in, the pool finds the tombstone: with
  `restoreMode:"auto"` (the default) exactly one candidate is re-bound; more than one is a 409
  `DEVICE_RESTORE_AMBIGUOUS` that names the candidates and requires an explicit restore;
  `manual` and `off` never auto-restore. `RestoreDevice` binds a named tombstone and refuses a
  record retired for a different account key, a non-retired record or one past its retention
  window.
- **Retire and GC.** `RetireDevice` refuses a record with a live node (`DEVICE_ACTIVE_SESSION`) and
  never deletes material, so a restore inside the retention window (default 90 days) still works.
  `GCDevices` removes only expired, unbound tombstones and logs counts, not ids.
- **Migration.** A v1 snapshot's plaintext device map is converted at startup into deterministic
  records (`legacy/v1`), sealed through the same sealer; the conversion is idempotent and a
  failure leaves the map in place. `ReconcileDeviceMaterials` opens every record at load, drops a
  retired record whose material is gone, and degrades — never fails — on a corrupt one.
- **Generation and transfer.** Generators are `random/v1` and `hmac-sha256/v1`; `import/v1` and
  `legacy/v1` records are never selectable for new identities. The pool has versioned export and
  import envelopes (`ajs-device-pool`, 1 000 records / 16 MiB caps) with passphrase encryption.
- **HTTP API.** `internal/api/device_pool.go` serves the pool under `/v1/device-pool`:
  `GET /v1/device-pool` (`state`, `platform`, `profile`, `accountId`, `q`, `offset`, `limit`; 50
  rows by default, 200 max) returns `{enabled,total,counts,devices,warnings}`, and a disabled pool
  is a 200 with `enabled:false` and `DEVICE_POOL_DISABLED` rather than an error;
  `POST /v1/device-pool/generate` creates records (201); `GET`/`PATCH /v1/device-pool/{id}` read a
  record and change its `label`/`notes`; `assign`, `release`, `retire`, `quarantine` and `purge`
  are the lifecycle actions. The destructive ones need an exact `confirm` token (`RELEASE`,
  `RETIRE`, `QUARANTINE`, `PURGE`); retire and quarantine also take a `reason`. Export/import use
  the passphrase envelope (`scope` is `assigned` — the default —, `retired` or `all`; import
  supports `dryRun` and `bind`), and a hard import conflict is a 409 that carries per-record
  `conflicts` with nothing mutated. The record view exposes hashes, public claims and lifecycle
  metadata only — never the machine code or raw identity — and `materialPresent`/`warnings` flag a
  record whose sealed material is missing. Failures carry the stable `DEVICE_*` token in
  `error.reason` (`DEVICE_ALREADY_BOUND`, `DEVICE_ACTIVE_SESSION`, `DEVICE_MATERIAL_MISSING`, …).
  The dashboard does not surface the pool yet.

## Scheduler

`internal/schedule` is a policy layer: it never opens a session, closes a gate or writes catalog
state directly. Disruptive actions call the same `runtime.NodeController` the HTTP connect route
uses, so the one-session guard and cooldown cannot be bypassed. The engine is off by default;
`AJS_SCHEDULES=1` starts it, and without it the read routes and the pure preview still answer but
every mutation is 503.

**Trigger.** `kind` is `cron`, `interval` or `once`:

- `cron` is a five-field expression (minute hour day-of-month month day-of-week) with `*`, lists,
  ranges and steps, plus `@hourly`, `@daily`, `@midnight`, `@weekly`, `@monthly`, `@yearly` and
  `@annually`. A restricted day-of-month and day-of-week combine with OR, as in Vixie cron.
- `interval` is `everySec` 60–2 592 000 (30 days) with optional `jitterSec`.
- `once` is an RFC 3339 `at`; `misfirePolicy:"run_once"` is refused for it.
- `timezone` is an IANA name per trigger (empty = UTC); an optional `window` restricts runs to
  weekdays (`days`, 0 = Sunday) and an `HH:MM` range, where `end < start` crosses midnight.

**Policy.** Defaults are `misfirePolicy:"skip"` (or `run_once`), `misfireGraceSec:300`,
`concurrencyPolicy:"forbid"` (or `queue`), and a guard of `cooldownSec:600`, `maxRunsPerDay:24`,
`dryRun:true`. A destructive action (`account.rotate`, `account.reconnect`, `account.switch`) with
`dryRun:false` needs `confirm:"ARM"` on create/PATCH; a manual run of one needs `confirm:"RUN"`.
PATCH requires `If-Match: "<id>:<revision>"`. P0 executes `account.rotate` (strategies `pool`,
`same-region`, `next-server`, `random`) and `account.reconnect`; any other action validates but
fails as `unsupported_action` and pauses the schedule. Retention is 50 schedules, 1 000 runs,
200 runs per schedule, 1 000 fire records and 2 000 reminder marks.

**At-most-once fire journal.** Every due slot is claimed through
`$AJS_STATE_DIR/schedule-fire.log`, an append-only JSONL journal whose `reserve` line is fsynced
before the action starts. `Reserve` is exclusive: a replayed key returns false, so a crash, a
restart or a duplicated tick cannot run the same slot twice. A reservation without a terminal
`finish` is converted to `unknown` on the next open and is **not** retried; it is recorded as an
`unknown` fire plus a `schedule.fire_unknown` warning audit. The journal and the snapshot's
`scheduleFires` are complementary: the file is the write-ahead gate, the snapshot is the readable
history.

**Renewal sweep.** Independently of any schedule, the engine runs one sweep per local day at
`AJS_RENEWAL_HOUR` (default 9) in `AJS_TIMEZONE`. It reads account expiries and writes only
reminder marks: each account whose expiry crosses `AJS_RENEWAL_LEAD_DAYS` (default 30/14/7/3/1)
and each overdue account is marked once per `(account, threshold, expiresAt)`, marks whose expiry
changed or whose account is gone are pruned, and one `account.renewal_sweep` audit row plus a
`system:renewal-sweep` run row is written only when something is due.
`AJS_RENEWAL_REMINDERS=0` disables it. The sweep sends nothing: reminders are state for the UI, not
mail.

## Resin integration

`internal/resin` is a stdlib-only client for the Resin v1.3.0 admin API;
`internal/publisher` renders the local subscription and owns the write policy;
`internal/runtime` is the startup/topology/reconcile seam. The API handlers
never call Resin directly: they see only the `api.Panel` interface, which is
nil when the integration is disabled. Every test uses `httptest`; no Resin
process, vendor account or network call is involved.

### Startup wiring

When `AJS_RESIN_ADMIN_TOKEN` is set, `cmd/ajs-accountd` builds
`resin.ConfigFromEnv`, `publisher.New` and `runtime.NewResinPanel`, then calls
`Start` before the listener starts. The order is the one the M1 spike proved:

1. `PATCH /api/v1/system/config {"max_latency_test_interval":"30s"}`; a
   `404`/`405` from an older build is logged and tolerated.
2. Render the store's `LIVE` nodes with `publisher.Render`
   (`socks5://127.0.0.1:<gatePort>#<tag>`, sorted, trailing newline). With no
   `LIVE` node the subscription is left untouched: no create, PATCH or refresh,
   because Resin rejects empty content and a parsed empty body evicts the pool.
3. `EnsureSubscription(<AJS_SUBSCRIPTION_NAME>, content, "30s")` — creates the
   local ephemeral subscription, or PATCHes it when content/interval differ.
4. `EnsurePlatform` with `sticky_ttl` from the panel settings
   (`model.PanelSettings.LeaseTTL`, default `8h`),
   `regex_filters=["<subscription>/.*"]` and `allocation_policy=BALANCED`
   (there is no store setting for the policy yet, so `runtime.Config` supplies
   it).
5. `Publish` — the publisher PATCHes the content and refreshes, coalescing to
   one PATCH+refresh per 5 s and refusing empty content.

Success logs `resin_connected` with `subscription_id` and `platform_id`. A
failed start logs `resin_start_failed` and the daemon keeps serving:
`GET /v1/topology` answers `503 UPSTREAM_UNAVAILABLE` until `POST /v1/reconcile`
succeeds, which retries the same sequence. Without the token the daemon logs
`resin_disabled` and keeps the in-memory behaviour.

### Continuous reconciliation (AJS-274)

`AJS_RESIN_RECONCILE=1` keeps the subscription converged without an operator
calling `POST /v1/reconcile`. Each tick hashes the desired state — the `LIVE`
nodes' published tag and gate port, the active lease `(platform, account)`
identities and the account ids — and runs the same render/PATCH/refresh
sequence only when the digest differs from the last successful publish. The
first tick after a restart always publishes (the digest starts empty), which
is how a startup reconcile that failed recovers. Volatile counters (requests,
bytes, latency) are deliberately not hashed, so a busy gate does not look like
drift.

The start delay is jittered in `[0, min(30s, interval))`. A failed publish is
retried after `1m, 2m, 4m, 8m, 15m` (cap) and never stops the loop; a recovery
resets the schedule to the interval. Only transitions are audited:
`resin.reconcile_failed` (warn) on the first failure and
`resin.reconcile_recovered` (info) when a later publish succeeds or the desired
state reverts to the last published hash. Triggers coalesce and a single
in-flight reconcile is shared, so two triggers cannot publish at once. Shutdown
cancels the loop and waits for an in-flight reconcile before the panel/publisher
closes.

### Panel endpoints

| Method | Path | Behavior |
|---|---|---|
| GET | `/v1/topology` | panel view when Resin is enabled, otherwise the store-derived `ajs-live` map |
| POST | `/v1/reconcile` | re-render + PATCH + refresh; `503 UPSTREAM_UNAVAILABLE` when no panel is configured |

The panel view is `{subscription_id, platform_id, content, nodes, last_good,
resin_version}`: `nodes` maps node id → tag for the rendered `LIVE` nodes,
`content` is the last render, `last_good` is the content Resin accepted, and
`resin_version` is `GET /api/v1/system/info`. It carries no credentials.

### Runtime mutations

With a panel configured, the four mutation handlers call `api.Panel` (an alias
of `runtime.Panel`) instead of the store; the response bodies are unchanged.
Without a panel they keep calling the store exactly as before. With
`AJS_ACCOUNT_LOGIN=1` the two node mutations call `NodeDrainHook` before either
path, so the controller closes or reopens the gate first; a runtime hook
failure answers 500 `INTERNAL` and leaves the store and panel untouched, while
a store refusal forwarded by the controller (e.g. draining a DOWN node) keeps
its usual 404/409 status.

| Route | Panel path | Store fallback (no panel) |
|---|---|---|
| POST `/v1/leases/{id}/actions/rotate` | read store lease → full tag `<subscription>/<tag>` from the last topology → `pub.Rotate(..., true)` → `GetLease` → `store.ApplyLeaseRotation` | next pool IP, expiry refreshed, requests +17 |
| DELETE `/v1/leases/{id}` | `DeleteLease` (404 idempotent) → `store.ReleaseLease` | `store.ReleaseLease` |
| POST `/v1/nodes/{id}/actions/drain` | `NodeDrainHook` (gate close) → `store.DrainNode` → `Reconcile` (publish the LIVE-only set) | `NodeDrainHook` → `store.DrainNode` |
| POST `/v1/nodes/{id}/actions/undrain` | `NodeDrainHook` (gate reopen) → `store.UndrainNode` → `Reconcile` | `NodeDrainHook` → `store.UndrainNode` |

Rotation call order: `GET platform` (read filters) → `PATCH platform`
(`regex_filters` + `!^<subscription>/<tag>$`) → `DELETE lease` → `PATCH platform`
(restore) → `GET lease`. The publisher owns the exclude/delete/restore sequence
and restores before returning, so the panel reads the new lease after the
restore. Delete-only rotation is not used: the M1 spike showed it re-picks the
same node. `ApplyLeaseRotation` records Resin's `egress_ip`/`expiry` and mirrors
the IP onto the node, preserving id/account/user/node/TTL and the local request
counter; the synthetic next-IP pool only runs in fallback mode.

Drain/undrain transition the store and then republish. When the publish
fails the handler answers 503 but the store state stays DRAINING/LIVE, and the
next `/v1/reconcile` converges Resin. With the controller hook wired, the
controller has already closed/reopened the gate and written the store before
the panel step runs; if that hook fails the handler answers 500 `INTERNAL` and
neither the store nor the panel changes, so the gate and the advertised state
cannot diverge.

Mutation errors reuse the panel contract:

| Condition | HTTP | Code |
|---|---|---|
| unknown store lease/node | 404 | `NOT_FOUND` |
| panel not started (no platform id) | 409 | `CONFLICT` |
| lease's node is not in the published topology | 409 | `CONFLICT` |
| publisher per-account rotation window (10 s) | 409 | `CONFLICT` |
| Resin call or reconcile failure | 503 | `UPSTREAM_UNAVAILABLE` |

`resin.ConfigFromEnv()` reads `AJS_RESIN_URL` and `AJS_RESIN_ADMIN_TOKEN` (see
Configuration); the proxy token is never used by this client. `resin.New`
applies a 10 s per-call timeout, sends `Authorization: Bearer` plus a generated
`X-Request-Id`, maps non-2xx bodies (`{"error":{"code","message"}}`) to
`*resin.APIError`, and logs method, path, status, duration and request id —
never headers, so the token cannot leak. `DeleteLease` treats 404 as success
(idempotent); `GetLease` keeps NOT_FOUND distinguishable via
`resin.IsNotFound`.

Client methods: `SystemInfo`, `EnsureSubscription`, `PatchSubscription`,
`RefreshSubscription`, `ListNodes`, `GetNode`, `EnsurePlatform`,
`GetPlatform`, `PatchPlatformFilters`, `GetLease`, `DeleteLease`,
`PatchSystemConfig`, `ListRequestLogs`.

Publisher behavior:

- `Render` emits `socks5://127.0.0.1:<gatePort>#<tag>` one line per node,
  sorted by tag, with a trailing newline. Duplicate tags, invalid tags and
  invalid ports are rejected. `TagAllocator` derives stable lowercase tags
  `<cc>[-<subdiv>]-<seq>`; the sequence is assigned once per `(account,
  server)` from a global counter so tags stay unique across the subscription.
- `Publish(ctx, nodes)` coalesces to at most one PATCH+refresh per 5 s, skips
  identical content, refuses an empty body, and keeps `LastGood()`. A failed
  refresh retries with exponential backoff (3 attempts, 250 ms doubling); the
  accepted content stays last-known-good and the next publish retries only the
  refresh.
- `Rotate(ctx, platformID, account, currentTag, avoidCurrentIP)` reads the
  filters, adds `!^<currentTag>$` (idempotent), PATCHes, deletes the lease,
  then restores the original filters even when the delete fails; restoration is
  retried with backoff. One rotation per account per 10 s.
  `RotationRestoreDelay` defaults to 0 (immediate) and can be raised to the
  PANEL §8 10 s window.
- `DropLeasesOnOpenCircuits` deletes leases pinned to nodes with a non-empty
  `circuit_open_since` and returns the deleted `(platform, account)` ids.
  DRAINING nodes are skipped: their leases belong to the drain path.
- `EnsureProbeConfig` PATCHes `max_latency_test_interval=30s`; a 404/405 from
  an older build is logged as a warning and tolerated.

### M1 caveats encoded here

Source: `research/77-m1-spike/REPORT.md` (Resin v1.3.0, image
`ghcr.io/resinat/resin:1.3.0`, digest
`sha256:9aa94af706bfa7bc80c8fd78ed562f9a00d4e35e8323728cd17f06af43e665bf`).

1. **Probe interval.** The default `max_latency_test_interval` is 1 h, so a
   restarted node stayed circuit-open and unroutable; PATCHing it to `30s`
   closed the circuit in 6.1 s. `EnsureProbeConfig` is the startup reconciler.
2. **Rotation must exclude, not just delete.** Five delete/request cycles
   re-picked the same node and IP under `BALANCED`; filter exclusion moved the
   lease in 0.3 s. `Rotate` therefore PATCHes `!^tag$` before deleting and
   restores afterwards.
3. **Kill window.** A dead leased node costs 3 failed requests over ~6 s and
   ~12.6 s until re-route; Resin never retries another node inside one request.
   The gate retries idempotent requests once on `UPSTREAM_CONNECT_FAILED`
   (`resin.IsUpstreamConnectFailed`).
4. **Circuit-open leases.** When `circuit_open_since` appears for a leased node,
   delete the lease instead of waiting for the probe cycle;
   `DropLeasesOnOpenCircuits` does that and leaves DRAINING nodes alone.
5. **Empty content is never publishable.** v1.3.0 rejects empty `content` on
   both create and PATCH (`content: must be a non-empty string`), while a
   successfully parsed empty body (for example from a remote fetch) diffs every
   node away. The publisher therefore refuses empty renders outright and keeps
   last-known-good; a fully drained pool must delete or disable the
   subscription instead.

Two deviations, both deliberate:

- `GET /api/v1/request-logs` in v1.3.0 pages with `cursor` and rejects
  `offset` (400). `RequestLogQuery` carries both, sends `offset` only when
  non-zero, and callers should use `Cursor`.
- Resin sends no request-id response header. The client generates one per call,
  sends it as `X-Request-Id`, and logs it for correlation.

## API

All `/v1/*` routes — including the Lab `/v1/lab/*` set below — and `GET /metrics` require
`Authorization: Bearer <token>`: either `AJS_ADMIN_TOKEN` or an `ajs_sk_` scoped key that carries
the route's scope (see [Scoped API keys](#scoped-api-keys)). `/healthz` is unauthenticated. List
endpoints return bare JSON arrays, matching `api.ts` (`Account[]`, `ProxyNode[]`, …), not wrapper
objects.

| Method | Path | Behavior |
|---|---|---|
| GET | `/healthz` | process status + account-state counts |
| GET | `/metrics` | Prometheus text format: aggregate account/node/lease/user/key/request counts, build info, uptime; every state including zeros, no ids/labels/IPs/secrets |
| GET | `/v1/metrics?range=24h\|7d` | account health derived from state + live node count (active+LIVE=100, active without a LIVE node=70, paused=40, auth_failed/expired=0); the traffic series is empty and totals are zero with `telemetryAvailable:false` — the gate meter feeds the request log (`/v1/usage`), not this series |
| GET | `/v1/openapi.json` | the OpenAPI 3.1 contract, served from the embedded byte-for-byte copy of `docs/openapi.json` with a strong `ETag` (SHA-256) and `If-None-Match` → 304; same bearer auth as the rest of `/v1/*` |
| GET | `/v1/accounts` | list; filters `state`, `limit`, `offset` |
| POST | `/v1/accounts` | create; duplicate label → 409; password sealed when `AJS_MASTER_KEY` is set, otherwise discarded |
| GET | `/v1/accounts/io/capabilities` | export/import limits and gates: `encryptedExport:true`, `plaintextExport` (`AJS_ALLOW_PLAINTEXT_EXPORT`), `maxImportRows:1000`, `maxExportAccounts:1000`, `maxDeleteIDs:100`, `maxBodyBytes`, `maxImportBodyBytes` |
| POST | `/v1/accounts/export` | `{ids?\|filter?, format:"encrypted"\|"json"\|"csv", passphrase?, includeDevice?, confirmPlaintext?}` → the file itself (`.ajsx`/`.json`/`.csv`) with `no-store` and `X-AJS-Export-*` headers. Encrypted is the default and always available; JSON/CSV need `AJS_ALLOW_PLAINTEXT_EXPORT=1` **and** `confirmPlaintext:"EXPORT"`; passphrase is encrypted-only; CSV refuses device identity; `ids` and `filter` are mutually exclusive; > 1 000 accounts → 400; a concurrent import/export/bulk-delete → 409 |
| POST | `/v1/accounts/import` | `{source:{kind:"json"\|"csv"\|"encrypted", content, passphrase?}, options:{mode, matchBy, onConflict, initialState, idPolicy, devicePolicy, preserveTimestamps, dryRun}}` → per-row plan/apply result. Defaults: atomic, label, skip, paused, new, restore_if_missing. `dryRun:true` previews without mutating; atomic applies nothing when a row fails; partial applies the valid rows; > 1 000 rows or a bad envelope → 400 |
| POST | `/v1/accounts/bulk-delete` | `{ids[], dryRun?, mode:"continue"\|"stop"}` → per-row `deleted`/`would_delete`/`not_found`/`failed`/`skipped` with counts. ≤ 100 ids; each row runs the account delete hook (stop gates/sessions) before the store delete, so a hook or sealer failure leaves that account and its secret intact |
| GET | `/v1/accounts/{id}` | single account |
| PATCH | `/v1/accounts/{id}` | partial label/username/platform/state/concurrency/regions/expiresAt/trafficBudgetBytes (`null` clears the monthly budget, `0` refuses every byte) |
| DELETE | `/v1/accounts/{id}` | with `AJS_ACCOUNT_LOGIN=1`: stop the account's gates and sessions first (stop failure → 500, nothing deleted); then remove the account, its nodes, its leases and its sealed secret (sealer file immediately, snapshot at the next save) and retire its device identity as a tombstone (pool mode; the legacy plaintext map is deleted) for re-add restore; a sealer failure → 503 with the account and its secret intact; keep audit |
| POST | `/v1/accounts/{id}/actions/login` | with `AJS_ACCOUNT_LOGIN=1`: vendor login + `loginSync`, then `active`/`expired`, a retained `LoginResult` and one gate per usable server; otherwise refresh `lastLoginAt` and clear `auth_failed` |
| POST | `/v1/accounts/{id}/actions/logout` | with `AJS_ACCOUNT_LOGIN=1`: controller closes the account's gates and sessions first (stop failure → 500, account active and nodes unchanged); then pause the account and mark its nodes `DRAINING` |
| GET | `/v1/device-pool?state=&platform=&profile=&accountId=&q=&offset=&limit=` | `{enabled,total,counts,devices,warnings}`; 50 rows by default, 200 max; a disabled pool is a 200 with `enabled:false` and `DEVICE_POOL_DISABLED`; records carry hashes/claims/lifecycle only, never the machine code |
| POST | `/v1/device-pool/generate` | `{count,platform,profile,algorithm,labelPrefix}` → 201 `{devices}`; unknown algorithm/profile → 400 `DEVICE_ALGORITHM_UNKNOWN`/`DEVICE_PROFILE_MISMATCH` |
| POST | `/v1/device-pool/export` | passphrase-encrypted `ajs-device-pool` envelope; `scope` is `assigned` (default), `retired` or `all`; optional `deviceIds`, `includeAccountKeys`, `includeLabels`, `confirm:"EXPORT"` |
| POST | `/v1/device-pool/import` | `{passphrase,envelope,dryRun,bind}`; all-or-nothing; a hard conflict → 409 with per-record `conflicts` and nothing mutated |
| GET | `/v1/device-pool/{id}` | one record; `materialPresent:false` warns that the sealed material is gone |
| PATCH | `/v1/device-pool/{id}` | `{label?,notes?}` |
| POST | `/v1/device-pool/{id}/assign` | `{accountId}`; a bound record → 409 `DEVICE_ALREADY_BOUND` |
| POST | `/v1/device-pool/{id}/release` | `confirm:"RELEASE"` |
| POST | `/v1/device-pool/{id}/retire` | `confirm:"RETIRE"` plus optional `reason`; a live node → 409 `DEVICE_ACTIVE_SESSION` |
| POST | `/v1/device-pool/{id}/quarantine` | `confirm:"QUARANTINE"` plus optional `reason` |
| POST | `/v1/device-pool/{id}/purge` | `confirm:"PURGE"` → 204; removes the record and its sealed material |
| GET | `/v1/nodes` | list; filters `account_id`, `state`, `region` |
| POST | `/v1/nodes/{id}/actions/drain` | with `AJS_ACCOUNT_LOGIN=1`: controller closes the gate first (hook failure → 500, node unchanged); → `DRAINING`; unpublishes through the panel when configured; DOWN → 409 |
| POST | `/v1/nodes/{id}/actions/undrain` | with `AJS_ACCOUNT_LOGIN=1`: controller reopens the gate first (hook failure → 500, node unchanged); → `LIVE` (DOWN → `READY`); republishes when configured |
| POST | `/v1/nodes/{id}/actions/probe` | refresh `lastProbeAt`; DOWN → 503 |
| GET | `/v1/leases` | list; filter `user_id` |
| POST | `/v1/leases` | `{userId, platform, accountId?, ttlSeconds?, idempotencyKey?}` → 201 `{lease, replayed?, warning?}`; `Idempotency-Key` header is the same channel as the body field (a mismatch → 400), a replay returns the original lease with `Idempotency-Replayed: true`, and the guards are documented under [Lease allocation](#lease-allocation). 409 `lease_conflict`/`user_disabled`/`account_unavailable`/`idempotency_conflict`; 429 `quota_exceeded`/`concurrency_exceeded`/`no_capacity` |
| POST | `/v1/leases/{id}/actions/rotate` | panel: filter exclusion + Resin lease read; fallback: next egress IP, expiry refresh, requests +17 |
| DELETE | `/v1/leases/{id}` | body `{confirm:true}` → 204; panel: delete Resin lease (404 idempotent), then store row; fallback: store release; decrement node `inUse`; a missing confirm → 400 |
| GET | `/v1/users` | list |
| POST | `/v1/users` | create; name conflict → 409 |
| PATCH | `/v1/users/{id}` | stickyTtl, quota (null = unlimited), allowlist, platform |
| DELETE | `/v1/users/{id}` | release leases, revoke keys, remove user |
| POST | `/v1/users/{id}/suspend` | `enabled=false` |
| POST | `/v1/users/{id}/resume` | `enabled=true` |
| PATCH | `/v1/users/{id}/allowlist` | replace allowlist (`{"allowlist":[…]}`) |
| GET | `/v1/apikeys` | list; filter `user_id` |
| POST | `/v1/apikeys` | `{userId,label}` → `{key,secret}`; secret shown once |
| DELETE | `/v1/apikeys/{id}` | revoke (`revokedAt` set; row retained) |
| GET | `/v1/scoped-keys?user_id=&limit=&offset=` | least-privilege keys; the secret is never returned |
| POST | `/v1/scoped-keys` | `{userId,label,scopes[],accountIds[],regions[],expiresAt,rateLimitPerMin}` → 201 `{key,secret}`; secret shown once; `rateLimitPerMin` defaults to 60, `0` = unlimited |
| GET | `/v1/scoped-keys/{id}` | one key |
| DELETE | `/v1/scoped-keys/{id}` | revoke (`revokedAt` set; row retained) → 204 |
| GET | `/v1/requests` | request log, newest first; a metered gate row carries `protocol` (`socks5`/`http`), `userId`/`userName` attribution (empty in `loopback` mode), `target` as `host:port` and `source` (`gate`/`mutation-lab`/`proxy`) — never a URL, header or credential |
| POST | `/v1/requests/mutations` | append a `mutation-lab` row + audit event |
| GET | `/v1/audit?limit=` | newest-first audit, default 20, max 500 |
| GET | `/v1/audit/export.csv?from=&to=&severity=` | retained audit history as CSV (`ts,severity,actor,action,target,detail,meta`), oldest-first, `text/csv` with an attachment filename and `no-store`. `from`/`to` are RFC 3339 and inclusive; absent `from` = seven days before `to` (or now); a reversed or >366-day window → 400, an unknown `severity` → 400; over 50 000 matching rows the older ones are dropped with `X-Truncated: true` |
| GET | `/v1/settings` | runtime settings |
| PATCH | `/v1/settings` | partial; unknown keys → 400 |
| GET | `/v1/topology` | store `ajs-live` map when Resin is disabled; live panel view when enabled |
| POST | `/v1/reconcile` | re-render + PATCH + refresh; 503 without a panel |
| GET | `/v1/system/update` | read-only GitHub release check. `AJS_UPDATE_REPO` unset → `{"enabled":false,"current":…}` with no network. Otherwise `GET <AJS_UPDATE_API_BASE>/repos/<repo>/releases/latest` with `Accept: application/vnd.github+json`, `User-Agent: ajs-accountd` (10 s client) → `{enabled:true,current,latest,updateAvailable,url,name,publishedAt,checkedAt}`. `updateAvailable` uses a simple semver compare (`v` optional, numeric dot segments): a draft or pre-release tag is **not** newer unless the running version is itself a pre-release. GitHub non-200/transport/parse failure → 502 `UPSTREAM_UNAVAILABLE`; no token is used or echoed. Never executes an update — the host runs `deploy/update.sh` |
| GET | `/v1/renewal/plans` | cached public price table (`source` `live`/`cache`/`static`, `stale`); a failed refresh still answers 200 with the fallback |
| GET | `/v1/renewal/accounts/{id}` | `expiresAt` → `daysLeft`, `urgency` (`none`/`soon`/`due`/`overdue`) and the official purchase URL, never an account id or token; unknown account → 404 |
| GET | `/v1/usage?from=&to=&account=&userId=` | `{from,to,requests,bytes,errors,accounts,users,days,topTargets,truncated,rowsRetained,retentionDays}` over the request log (gate flows, mutation lab and seed); `rowsRetained` is the 10 000-row cap, `retentionDays` the inclusive UTC-day span the retained rows cover (`0` = no rows), so a wide window can be labelled as partial. `from`/`to` take RFC 3339 or `YYYY-MM-DD` (UTC), absent = trailing 30 days; invalid, reversed or > 366-day windows → 400 |
| GET | `/v1/health/nodes` | loopback probe aggregate per watched node (`state`, `count`, `ok`, `successRate`, `p50Ms`, `p95Ms`, `minMs`, `maxMs`, `consecutiveFailures`, `last`); `AJS_HEALTH_PROBE=0` → `{enabled:false,reason}`; `AJS_HEALTH_MAX_NODES` (default 16, 1–64) bounds the retained rings |
| GET | `/v1/webhooks` | persisted receivers plus `secretAvailable`; never the signing secret |
| POST | `/v1/webhooks` | `{url,events?,description?,enabled?}` → 201 with the signing secret exactly once; `https` unless loopback or `AJS_WEBHOOK_ALLOW_HTTP=1`; non-private destination unless `AJS_WEBHOOK_ALLOW_PRIVATE=1`; max 32 endpoints |
| PATCH | `/v1/webhooks/{id}` | `{enabled}` only; enabling a receiver whose secret is gone → 409 |
| DELETE | `/v1/webhooks/{id}` | remove the row and its sealed secret → 204 |
| GET | `/v1/webhooks/{id}/deliveries` | delivery attempts for that endpoint, newest first: persisted rows (50 per endpoint, 500 total, survive a restart) merged with the live 200-attempt ring, deduped by delivery id; `?limit` defaults to 200 and is clamped to 500, a non-positive value → 400 |
| GET | `/v1/events?since=&limit=&wait=` | retained customer events after `since`, oldest first: `{events,nextCursor,oldestCursor,dropped}`. `limit` defaults to 100 and is clamped to 500; `wait` (a Go duration, capped at 25 s) turns a current cursor into a long poll that answers as soon as the next event arrives. A malformed cursor → 400 `INVALID_ARGUMENT`; an evicted one → 410 `CURSOR_EXPIRED` with `details.oldestCursor`. Scope `events:read`; a key restricted by `accountIds`/`regions` → 403 |
| GET | `/v1/events/stream` | `text/event-stream` replay plus live events: one `id:`/`event:`/`data:` frame each (data is the `Event` JSON object), resume from `Last-Event-ID` or `since`, a 15 s `: keepalive` comment, `X-Accel-Buffering: no`; closes when the client disconnects or the daemon stops. Scope `events:read` |
| POST | `/v1/backup` | `{passphrase}` → the `.ajsb` envelope as a download (admin-only); `no-store`, `X-AJS-Backup-Blobs`; 409 `CONFLICT` when `state.json` is missing, 409 `BACKUP_BUSY` while another backup/restore runs |
| POST | `/v1/backup/inspect` | `{envelope,passphrase?}` → `{version,supported,app,createdAt,envelopeBytes,manifest?}`; `envelope` is the JSON document itself, not a base64 string; header-only without a passphrase, no blob and no state-directory access (admin-only) |
| POST | `/v1/backup/restore` | `{envelope,passphrase,dryRun?,confirm?}` → `{dryRun,applied,restartRequired,manifest,diff,files?,warning?}`; `envelope` is the JSON document itself; `dryRun` defaults to true, an apply needs `confirm:"RESTORE"`, 409 `BACKUP_LIVE_NODE` while a node is `LIVE`, 409 `BACKUP_DAEMON_RUNNING` without `AJS_BACKUP_ALLOW_RESTORE=1` (admin-only) |

The catalog selection/connect, schedule and Lab routes are listed in the
[Lab telemetry](#lab-telemetry) table below rather than repeated here.

Errors are always `{"error":{"code":"…","message":"…","reason?":"…","details?":{…}}}`; `reason`
is the stable machine token used by the catalog and schedule routes (for example `snapshot_changed`
or `connect_cooldown`), and `details` carries non-secret hints such as `ineligibleReason`:

| Code | HTTP | Used for |
|---|---|---|
| `INVALID_ARGUMENT` | 400 (413 oversize) | malformed JSON, bad enum/range, unknown settings key, a missing `If-Match` on schedule PATCH |
| `NOT_FOUND` | 404 | unknown id or route, unknown catalog server, no catalog target |
| `CONFLICT` | 409 | duplicate label/name, draining a DOWN node, Lab egress check with no running gate or no Lab reader, a catalog switch in progress or a changed snapshot, an ineligible server, an armed-action confirmation missing, a schedule run in flight, a concurrent account import/export/bulk-delete, a lease create refused as `lease_conflict`/`user_disabled`/`account_unavailable`/`idempotency_conflict` |
| `UPSTREAM_UNAVAILABLE` | 503 | probing a DOWN node; reconcile/topology without or before a Resin panel; schedules disabled; a stopped node controller |
| `QUOTA_EXCEEDED` | 429 | a lease create refused for `quota_exceeded`, `concurrency_exceeded` or `no_capacity`; repeat Lab egress check for the same node inside the 5 s cooldown. The account traffic budget and the `userpass` user quota are gate refusals, not `/v1/*` errors: SOCKS5 `05 02`, and `429`/`403` on the HTTP CONNECT listener |
| `RATE_LIMITED` | 429 | catalog connect cooldown (`Retry-After` carries the remaining whole seconds) or a scoped key over its per-minute budget (`Retry-After` is one refill interval) |
| `UNAUTHORIZED` | 401 | missing/wrong bearer token |
| `FORBIDDEN` | 403 | a scoped key whose scope does not cover the route, or whose `accountIds`/`regions` exclude the requested account |
| `BACKUP_BUSY` | 409 | a backup or restore already holds the panel-file lock |
| `BACKUP_LIVE_NODE` | 409 | a restore was asked for while a node is `LIVE`; `details` names the count and the first node |
| `BACKUP_DAEMON_RUNNING` | 409 | an apply was asked for while the daemon runs and `AJS_BACKUP_ALLOW_RESTORE` is not `1` |
| `BACKUP_UNAVAILABLE` | 503 | a backup create or restore was called on a daemon with no state directory |
| `PRECONDITION_FAILED` | 412 | a stale `If-Match` revision on schedule PATCH (`reason:"stale_revision"`) |
| `METHOD_NOT_ALLOWED` | 405 | path exists, method does not |
| `INTERNAL` | 500 | unexpected store failure or node drain hook failure (details stay in the log) |
| `UPSTREAM_UNAVAILABLE` | 502 | update check failure: GitHub unreachable, non-200, malformed response; catalog session/gate open failure (message is generic; the detail stays in the log) |

With `AJS_ACCOUNT_LOGIN=1`, the login handler maps the vendor outcome onto the
same contract:

| Condition | HTTP | Code |
|---|---|---|
| vendor rejects the credentials (`ErrAuthRejected`) | 401 | `UNAUTHORIZED` |
| account has no sealed password (`ErrNoSecret`) | 409 | `CONFLICT` |
| transport or protocol failure (`ErrUpstream`) | 503 | `UPSTREAM_UNAVAILABLE` |
| unknown account | 404 | `NOT_FOUND` |

The 503 body is generic (`account login upstream unavailable`); the transport
detail stays in the log. A rejection marks the account `auth_failed`; a
transport failure leaves the state and `lastLoginAt` untouched.

Other contract details:

- `docs/openapi.json` is the OpenAPI 3.1 contract for this route table (79 paths, 98 operations,
  153 schemas at the last sync). Every operation carries `x-implementation-phase`: `P0` is served
  today, `P1` marks the proposed next-phase interfaces. `dashboard/tests/openapi.test.ts` fails
  when the document stops parsing, a `$ref` dangles, an `operationId` repeats, a path parameter is
  undeclared, the bearer requirement is missing outside `/healthz`, or the route table in
  `internal/api/server.go` and the contract drift apart.

- Timestamps are RFC 3339 (nanosecond precision), byte counts are integers.
- `quotaBytesMonth`, `lastUsedAt`, `revokedAt` are `null` when unset.
- `PanelUser.usedBytes` is the derived current-month gate total, not a stored
  counter (see Per-user identity); `GET /v1/usage` reports the same figure per
  user while its `bytes` stays the queried-window total.
- `POST /v1/apikeys` stores only a SHA-256 hash plus the 16-character prefix;
  the plaintext secret exists only in that response.
- Middleware adds `X-Request-Id`, logs one `slog` line per request (never
  headers, so `Authorization` cannot leak), limits bodies to 1 MiB, and answers
  CORS preflight for the two configured origins.
- Account passwords are validated and discarded unless `AJS_MASTER_KEY` seals
  them; the login token and connect password never leave memory.
- `GET /metrics` exposes only aggregate Prometheus counters (accounts/nodes by
  state, lease/user/key/request totals, build info, uptime); no usernames,
  node ids, egress IPs, labels or secrets ever appear in the output.

### Lab telemetry

`runtime.LabView` (`internal/runtime/lab.go`, `internal/runtime/egress_check.go`) backs the Lab
telemetry surface for the dashboard's `/lab` page. The GET routes read in-process state only — the
store, the node controller snapshot, retained login results and the log ring — and make no vendor
call. The single POST runs an explicit claimed-vs-verified egress probe through the node's own
loopback gate and writes nothing to the store. All routes are under the same bearer auth as the
rest of `/v1/*` (401 without the token; `/healthz` stays open).

| Method | Path | Behavior |
|---|---|---|
| GET | `/v1/lab/overview` | version, commit, uptime, effective modes (login, Resin, update check, secrets, cert pin), aggregate counts and warnings |
| GET | `/v1/lab/flags` | effective startup flags with provenance (`env`/`flag`/`default`); secret flags carry `set` + `redacted`, never a value |
| GET | `/v1/lab/gates` | per-node gate listener, state, the accepted/rejected/probe counters and ingress/egress bytes from the controller snapshot; `probes` counts health-sampler connections that closed before sending a handshake byte, so it is separate from `rejected` |
| GET | `/v1/lab/flows?limit=` | newest-first finished flows across every running gate, joined with node/account ids and a redacted SID; `limit` 1–500 (default 200), invalid → 400; the body is `{enabled,flows,dropped}` and a flow carries id, start/duration, `method`, `targetHost`/`targetPort` (no URL, headers or credential), `status`, bytes and `errorClass`. This view does not expose the gate's `protocol` or `userId`/`userName`; the gate records them on `FlowRecord`, and `/v1/requests` rows do carry `protocol` and attribution |
| GET | `/v1/lab/sessions` | FV session health with a redacted SID (`a1b2…(32)`), age, last-flow time, claimed egress and keepalive support |
| GET | `/v1/lab/egress` | the vendor's claimed exit address per running node (`source:"fv-claim"`); this GET never probes, so `verified` stays empty, `match` null and `checkedAt` null — a POST result is returned to its caller, not stored |
| POST | `/v1/lab/egress-check` | body `{"nodeId":"…"}`; dials the node's running loopback SOCKS5 gate and `GET https://ifconfig.me/ip` over HTTPS (10 s total, environment proxy ignored), then answers `{nodeId,claimed,verified,match,latencyMs,checkedAt,source:"echo"}`. No store write and no vendor call. No running gate or no Lab reader → 409 `CONFLICT`; a second check for the same node within 5 s → 429 `QUOTA_EXCEEDED`; malformed, empty or unknown-field body → 400 `INVALID_ARGUMENT`; a probe failure is still 200 with `error` set, `verified` empty and `match` null |
| GET | `/v1/lab/profiles` | supported version profiles plus the sample matrix (versions and carriers, no payloads) |
| GET | `/v1/lab/logs?level=&component=&limit=&since=` | newest-first window over the bounded log ring; `limit` 1–2000 (default 200), `level` one of debug/info/warn/error, `since` RFC 3339; an invalid value answers 400 instead of silently widening the filter |
| GET | `/v1/lab/diagnostics` | component health map, state-snapshot facts (basename path, last save, size), build facts (version, Go version, commit) and warnings |
| GET | `/v1/accounts/{id}/servers?q=&region=&status=&selected=&province=&city=&offset=&limit=&sort=` | vendor server catalog for one account from the retained `LoginResult`; without one it answers `stale:true`, an empty list and `no retained login result; log in to refresh the catalog`; unknown account → 404. `q` is a case-insensitive substring over id/title/host/region, `region`/`status` match case-insensitively, `selected` is `true`/`false`, `province` is a two-digit GB/T 2260 code (`00` selects unresolved titles) and `city` a six-digit city code from the bundled asset, `offset` ≥ 0, `limit` 1–500 (default 50) and `sort` is `order` (default, vendor ord compared numerically), `title`, `host`, `status` or `region`; an invalid value → 400 `INVALID_ARGUMENT`. The body keeps the existing fields and adds `snapshotId` (the same identity the selection/connect guard compares; empty without a catalog connector or a retained result), `total` (filtered row count), `offset`, `limit`, `nextOffset`, `hasMore`, `regions:[{code,count}]` (the filtered rows per region) and `geo:{total,unknown,provinces:[{code,count,connected,cities:[{code,label,count}]}],current,live}` — the map aggregate over the q/region/status scope before the province/city filter, so the dashboard needs no full-catalog download. Pagination and filters run over the retained catalog only: no vendor call and no extra store write |
| GET | `/v1/accounts/{id}/device` | stable device identity as `sha256:`-prefixed 16-hex hashes plus non-secret claims; unknown account → 404 |
| GET | `/v1/accounts/{id}/selection` | the persisted target plus its resolved title; no target → 404 `no_target`; a stale catalog leaves `title` empty |
| PUT | `/v1/accounts/{id}/selection` | `{serverId, snapshotId?}`; persists the desired server and starts nothing. Unknown server → 404; no login result → 409 `login_required`; ineligible → 409 `server_ineligible`; stale snapshot → 409 `snapshot_changed`; login disabled → 409 `login_disabled` |
| DELETE | `/v1/accounts/{id}/selection` | idempotent; clears the target and never disconnects the session → 204 |
| POST | `/v1/accounts/{id}/actions/connect` | `{serverId, snapshotId?, confirm:true}` → `SwitchResult`. Opens the new session before closing the old one, commits one `LIVE` node, rolls back on failure; the current server → 200 `alreadyConnected` without cooldown. 429 `connect_cooldown` + `Retry-After` inside the 5 s per-account window; 409 `switch_in_progress`/`multiple_sessions`/`snapshot_changed`/`server_ineligible`/`login_required`; 502 `session_open_failed`/`gate_start_failed` |
| POST | `/v1/accounts/{id}/actions/disconnect` | `{confirm:true}` → `DisconnectResult`; drains each gate (10 s), closes it, deletes the account's nodes and leases, keeps the target; a second call → `alreadyDisconnected:true` |
| POST | `/v1/accounts/{id}/actions/reconnect` | `{snapshotId?, confirm:true}` → `SwitchResult`; always reopens the current server, or the target when nothing runs; no current server and no target → 404 `no_target` |
| GET | `/v1/schedules?accountId=&enabled=&limit=&offset=` | list schedules in creation order with refreshed run counts and each schedule's `nextRunAt`/`lastRunAt` |
| POST | `/v1/schedules` | create; destructive action with `dryRun:false` requires `confirm:"ARM"`; `Location: /v1/schedules/{id}` → 201 |
| GET/POST | `/v1/schedules/preview` | pure next-run preview (query or body; `count` defaults to 5); no store write and no engine required |
| GET | `/v1/schedules/{id}` | one schedule |
| PATCH | `/v1/schedules/{id}` | partial update; requires `If-Match: "<id>:<revision>"`, otherwise 400; a stale revision → 412 `PRECONDITION_FAILED`; re-arming a destructive action requires `confirm:"ARM"` |
| DELETE | `/v1/schedules/{id}` | → 204; a run in flight → 409 `in_flight` |
| POST | `/v1/schedules/{id}/actions/run` | `{dryRun?, force?, confirm?}` → `{run}`; a manual non-dry run of a destructive action requires `confirm:"RUN"`; a run already in flight → 409 `in_flight`; engine off → 503 |
| GET | `/v1/schedules/{id}/runs?limit=` | run history, newest first; `limit` default 50, max 200 |

A mode that is off is still HTTP 200 with `{"enabled":false,"reason":"…"}` naming the switch
(`AJS_ACCOUNT_LOGIN is not 1`, `the runtime log ring is not configured`, or
`lab telemetry is not configured` when no `LabReader` is wired), so the dashboard renders the
reason instead of a 500 or an unexplained empty list. The one exception is
`POST /v1/lab/egress-check`, which answers 409 `CONFLICT` when no `LabReader` is wired or the node
has no running gate. Redaction is structural: the log ring stores
only allowlisted attributes (`account_id`, `node_id`, `server_id`, `error_class`, `duration_ms`,
`status`, `bytes`) with a bounded 2000-entry capacity, session IDs are truncated, and device
identifiers are one-way hashes. No token, password, connect password, raw SID or raw device ID is
serialized by any Lab route. The egress probe returns only the observed public address, latency
and error text; it persists nothing.

## Seed

`store.New()` / `store.NewWithSealer()` build this deterministic dataset for
tests and mock-mode comparisons. The daemon itself starts from
`store.NewEmpty()`; it never serves these rows unless an old snapshot already
contains them, and `AJS_DROP_DEMO=1` removes them once (see Persistence).

Deterministic values match `dashboard/src/lib/mock.ts`:

| Collection | Count |
|---|---|
| accounts | 6 (`acc-01`…`acc-06`) |
| nodes | 12 (`node-01`…`node-12`) |
| leases | 8 (`lease-01`…`lease-08`) |
| users | 4 (`u-01`…`u-04`) |
| API keys | 6 (`key-01`…`key-06`, display-only) |
| requests | 40 (`req-001`…`req-040`, seeded PRNG `20261023`) |
| audit | 20 (`audit-001`…`audit-020`) |

Mutations append `audit-0NN` events at the head of the audit list.

## Tests

The tree holds **815 `func Test` functions across 104 `_test.go` files** (recounted 2026-10-25);
the emulator module adds 41 functions in 13 files. Every suite runs offline.

`internal/api/server_test.go` uses `httptest` only and covers: 401 vs `/healthz`
200, seed counts and exact field names, account create/409/delete, node
drain/undrain/probe, lease rotate/release, user suspend/resume/allowlist and
cascade delete, API-key show-once/revoke plus stored-hash checks, mutation log
append, settings roundtrip, CORS preflight, 404/405, pagination, metrics
(empty traffic with `telemetryAvailable:false`, state-derived health),
topology, the 1 MiB body limit, and token redaction in request logs. The
Prometheus endpoint has its own coverage: bearer-auth 401 vs 200 with the text
format content type, seeded counts compared against `store.Accounts`, `Nodes`,
`Leases`, `Users`, `Keys` and `Requests`, zero states present, no id/label/IP
or secret in the body, and `AllowInsecure` passthrough.

The P1 surfaces have their own files. `internal/api/account_io_test.go` covers capabilities, auth,
encrypted export roundtrip, the plaintext gate (disabled, missing/incorrect `EXPORT`), unknown ids
and a short passphrase, import dry-run, atomic failure versus partial apply, CSV and wrong
passphrase, bulk delete plus hook failure and `stop`, the concurrent-call 409, and log redaction.
`internal/store/account_io_test.go` covers the formats and the plan matrix: export with password
and device, missing/corrupt secret, filter and unknown id, seal/open roundtrip, tamper and wrong
passphrase, exact CSV build/parse and its region-separator refusal, warnings, JSON version/row
limits, the merge matrix and the ambiguous `username_platform` match. `internal/api/catalog_connect_test.go`
and `internal/runtime/catalog_connect_test.go` cover selection CRUD and the switch path: one LIVE
node, already-connected idempotence, cooldown, ineligible/unknown/login-required reasons,
concurrent switches, open/store failure rollback, multiple sessions, cleanup warnings, disconnect,
and the HTTP reason mapping. `internal/store/catalog_test.go` pins selection persistence and the
additive snapshot maps. `internal/store/leases_test.go` and `internal/api/leases_test.go` cover
lease allocation and revocation (see [Lease allocation](#lease-allocation)).

`internal/store/device_pool_test.go` covers generation and sealing, the HMAC vector, uniqueness,
assign/release/quarantine, delete-retires-then-re-add-restores, ambiguous restore, fail-closed
missing material, v1 migration, binding persistence, export/import, concurrent bind and the
validation limits; `cmd/ajs-accountd/device_pool_wire_test.go` pins the startup gate
(`AJS_DEVICE_POOL` with and without a master key, the kill switch, legacy migration).
`internal/catalog/geo_test.go` pins exact title resolution (`海南 #1` must not resolve to 青海's
`海南藏族自治州`, `湘西` → 湘西州) and the `#<n>` suffix parse.

`internal/schedule/cron_test.go` covers cron parse/next, rejected syntax, the day-of-month OR
day-of-week rule, DST gap and fall-back, a brute-force agreement sweep, midnight-crossing windows,
interval/once next-run and preview; `firelog_test.go` covers exclusive reserve, crash replay →
unknown, finish/compact and a torn last line; `engine_test.go` covers exactly-once firing, dry-run
without a catalog call, misfire skip, bounded `run_once` catch-up, one session per account, the
circuit breaker, pause after five failures and renewal-sweep idempotence;
`internal/store/schedules_test.go` pins CRUD, run caps, fires/marks and snapshot roundtrip;
`internal/api/schedules_test.go` covers list/create/patch/delete, preview, validation, `ARM`,
manual run, the disabled engine and metrics, and `cmd/ajs-accountd/scheduler_test.go` pins the
default-off switch and start/stop.

The platform wave has its own coverage. `internal/api/scoped_keys_test.go` covers the scope table,
the 403 for a missing scope, account/region read restrictions and the one-time secret;
`internal/store/scoped_keys_test.go` pins hashing, prefixes, expiry, revocation and pagination.
`internal/api/device_pool_test.go` and `cmd/ajs-accountd/device_pool_wire_test.go` cover the HTTP
lifecycle (list/generate/assign/release/retire/quarantine/purge, confirm tokens, export/import,
409 conflicts) and the startup gate. `internal/api/platform_test.go` covers renewal plans/status,
usage windows and filters, the health view and the webhook registry (one-time secret, PATCH
`enabled`, the persisted+ring delivery merge with `?limit`, persisted deliveries after a reload,
409 when the secret is gone); `internal/webhook/hook_test.go` pins the async bounded `OnDelivery`
sink (a blocked sink never stalls dispatch, drops are counted, one call per attempt);
`internal/store/webhook_deliveries_test.go` pins the 50/500 caps, newest-first snapshot reload,
drop-oldest, snapshot validation and the delete cascade; `internal/webhook/dispatcher_test.go` and
`webhook_test.go`/`ssrf_test.go` pin signing, retry/backoff, dead-letter, dedupe, the 200-attempt
ring, payload scrubbing (including embedded `password=…` values) and the private/metadata-host
probe table plus dial-guard recheck; `internal/webhook/deadletter_test.go` pins the report-once
ring and panic isolation, and `cmd/ajs-accountd/webhook_deadletter_test.go` pins the composition
root: exactly one warning `webhook.dead_letter` audit row per exhausted delivery (no second row on
close), the endpoint/event/attempt detail, and no URL or signing secret;
`internal/store/audit_hook_test.go` pins critical-audit
delivery; `internal/health/health_test.go` pins the 30 s/120-sample window, states, error
classes, loopback-only dials, the configurable cap, unwatched-first eviction, the watched-LRU
fallback and its bounded `Evicted` record; `cmd/ajs-accountd/health_reconcile_test.go` pins the
`AJS_HEALTH_MAX_NODES` parse (default, clamp, non-integer startup error), the re-arm of an evicted
watched key, the once-per-minute log throttle, the failure log and the unwatch of a stopped gate;
`internal/api/platform_scopes_test.go` pins the three new read scopes route by route (200 with the
scope, 403 without) and the account restriction on those reads. `internal/api/events_test.go`
pins the replay page order and cursor, the 400/410 cursor split with `details.oldestCursor`, the
long poll returning on a new event and timing out on its budget, SSE replay/live frames with
`Last-Event-ID` and `since`, unsubscribe on client cancel, and the `events:read` matrix (403 for
another read scope and for an account-restricted key, 200 for admin);
`cmd/ajs-accountd/events_ring_test.go` pins the single emission point: one sink call reaches the
ring and the webhook dispatcher with the same scrubbed event.
`internal/api/backup_test.go` covers the three backup routes end to end (create → inspect →
dry-run → apply, wrong passphrase, tampered header, busy lock, LIVE-node refusal, the
daemon-running gate, missing state dir, and the restore-never-deletes rule);
`internal/backup/*_test.go` pins the deterministic tar, manifest, envelope authentication and the
diff matrix. `internal/runtime/flow_meter_test.go` pins one row
per accepted flow with the `gate` source, label join, `host:port` target and the dropped rejected
flow; `internal/ratelimit/ratelimit_test.go` and `internal/api/ratelimit_test.go` pin the
scoped-key request budget and its 429. `internal/usage/usage_test.go` and
`internal/renewal/*_test.go` cover the aggregation windows, `rowsRetained`/`retentionDays`, the
cached/fallback catalogue and redirect refusal.

`internal/secret/store_test.go` covers AES-256-GCM roundtrip, account-id AAD
mismatch, tampered ciphertext/nonce/version, wrong key, rotation and reopen
under the new key, the 0600 file mode with no temp-file leftovers, a missing
file starting empty, key-length rejection, that the JSON file holds only base64
envelopes (no plaintext), and delete: the entry leaves memory and the file, a
surviving entry still opens after a reopen, a missing entry is a no-op, and a
failed rewrite restores the entry. `internal/store/secret_test.go` covers
seal-on-create, `AccountSecret` per account, delete, the nil-sealer discard
path, seeded accounts without a secret, no credential in the account JSON, and
a failing sealer leaving the store unchanged. It also pins the AJS-170 delete
contract: after create → save → delete → save → reload neither the account, its
device identity nor its sealed secret is in `state.json` or `secrets.json`, a
retried delete is a not-found with one audit event, a sealer failure answers
`ErrUpstream` with the account and secret intact, logout keeps the secret, and
the delete audit event and retry error carry no credential. `internal/store/persist_test.go`
covers a full roundtrip of every collection, settings, key hashes, sealed
secrets and counters (including `AccountSecret` after restore with the same
sealer), the `0600` file mode (asserted on non-Windows) with no temp-file
leftovers, an atomic overwrite followed by a reload, a restore that replaces
every seeded collection, a missing file returning `os.ErrNotExist` without
mutating the store, corrupt JSON and unknown/empty versions leaving the store
unchanged, no plaintext password or `password` field in the document, and
create → delete → save/load plus a lost-counter restore that must not reuse an
account, user, key, mutation or audit id. `internal/store/demo_test.go` covers
the empty startup store and its sequences, `PurgeDemo` removing only seed rows
while preserving `acc-07`, its runtime node, a created user/key, a
mutation-lab request and later audit events, idempotency, the sealer prune for a
seed account that carries a sealed secret (and the reported error when the
sealer refuses), and the real health formula (active+LIVE=100, active without
LIVE=70, paused=40, auth_failed/expired=0) with empty traffic totals.

`internal/resin/client_test.go` runs against an `httptest` fake Resin and covers
subscription create/unchanged/PATCH, platform create/update with only the
differing fields, filter replacement, lease get/delete (including 404
tolerance), request-log query construction, node pagination, `circuit_open_since`
null vs timestamp, system-config PATCH, the auth header, request-id presence,
and that the admin token never appears in an error string or log line.

`internal/publisher/publisher_test.go` uses the same fake plus a fake clock and
covers golden render lines, duplicate/invalid/empty guards, stable tag
allocation, coalescing and identical-content skips, the empty-publish refusal,
refresh retry/backoff and last-known-good retention, the exact rotation call
order (read filters → exclude PATCH → DELETE → restore PATCH), restore after a
failed delete, restore retry, rotation rate limiting, circuit-open lease
cleanup with DRAINING exclusion, and probe-config 404/405 tolerance.

`internal/publisher/reconcile_test.go` covers the interval reconciler: the
desired-state hash (nodes/leases/accounts, order-independent), publish only on
a changed hash, retry after a failed publish without advancing the hash, the
`1m→2m→4m→8m→15m` backoff cap, the failure/recovery audit transitions, the
jittered start delay, context cancellation during a wait and during a publish,
and coalesced triggers that never overlap. `cmd/ajs-accountd/reconcile_wire_test.go`
pins `AJS_RESIN_RECONCILE_INTERVAL` parsing, the LIVE-node/account mapping and
the default-off wiring.

The access wave adds its own files. `internal/api/tls_test.go` pins the HTTPS admin listener: off
by default, the all-or-nothing `AJS_TLS_*` parse, a missing/mismatched/non-PEM pair aborting before
bind, the template copy (timeouts, handler), HSTS on a 200 and on a 401, a plaintext request
refused on the HTTPS port, the TLS 1.2 floor with an explicit ECDHE-AEAD suite list, and shutdown;
`cmd/ajs-accountd/tls_test.go` pins the two startup rules (the HTTPS address must differ from
`AJS_LISTEN`, and `AJS_ALLOW_INSECURE=1` may not publish it off loopback). `internal/gate/http_test.go`
is described under [HTTP CONNECT listener](#http-connect-listener-ajs-271).
`internal/api/users_keys_test.go` covers the destination allowlist write path (hostname, `*.domain`,
IP and CIDR accepted and normalized, duplicates collapsed, scheme/port/bracket refusals, the
64-entry cap) and the derived `usedBytes` that `/v1/usage` reports. `internal/api/flows_test.go`
adds the end-to-end row: a real gate flow served through `/v1/lab/flows`.

`internal/runtime/panel_test.go` drives the real client and publisher against a
fake Resin admin server (`httptest`) with a fake clock. It asserts the exact
startup call order and payloads (probe config → subscription → platform →
publish → system info), the topology content and JSON field set, an idempotent
reconcile and a changed-content reconcile after draining one node, the
empty-node guard (no subscription call at all), 404/405 probe-config tolerance,
error propagation at each step, and the API wiring: store map + 503 without a
panel, 503 before the first start, and the live view + reconcile with one.

`internal/runtime/mutations_test.go` extends the same fake with platform and
lease routes. It covers the rotate call order (exclusion PATCH before DELETE,
filter restore, lease read), the exact `!^ajs-live/<tag>$` filter and the
restored filter list, store egress/expiry updates with immutable fields
preserved, missing lease (404), missing platform and unpublished tag (409), the
10 s rate limit (409), release with a 404-idempotent Resin delete, drain/undrain
published content and topology, 503 plus the kept store transition when a
publish fails, the API JSON for all four mutations, and the unchanged store
fallback without a panel.

`internal/account/control_test.go` drives a fake `httptest` `/client.php` and
covers signed login (request fields, generated device IDs, exact `X-Sig` over
the raw body, `X-Timestamp`, user agent, no `Authorization`), unsigned mode
omitting both signature headers, a non-OK `AuthResult` as `*AuthError`,
`loginSync` refreshing `ConnectPassword` plus the `Servers` map (`sts=ok`,
`par.fv.p`, `loc`) and its `_client_login`/`username` fields,
`ErrNoLoginToken` before login, transport failover to the second host, no
failover on HTTP 500 or on a response without a `Login` object, the two device
profiles, and redaction through every `fmt` verb, JSON and slog.

`internal/account/login_service_test.go` drives the same fake `/client.php`
through `LoginService` and covers: success (`active`, fresh `lastLoginAt`,
vendor user name, `account.login` audit, retained `Result` with token/connect
password), an expired membership (`expired`), auth rejection (`auth_failed`,
unchanged `lastLoginAt`, sanitized one-line `account.auth_failed`, no retained
result), a transport error (no state or audit change), a missing sealed secret
(`ErrNoSecret`, no request), an unknown account (`store.ErrNotFound`, no
request), the disabled switch (`ErrDisabled`, no request), redaction of the
password/token/connect password through `%v`/`%+v`/`%#v` and the service log,
and the default hosts/profile/signing mode. `internal/api/server_test.go`
adds the API mapping test: the optional service answers 200, and wrapped
`ErrNoSecret`/`store.ErrNotFound`/`ErrAuthRejected`/`ErrUpstream`/`ErrDisabled`
map to 409/404/401/503/503; the existing nil-service test keeps the
store-only fallback.

`internal/gate/server_test.go` uses a `net.Pipe` echo session and a blocking
session; apart from loopback it never touches the network. It covers CONNECT
for domain/IPv4/IPv6 with the exact `OpenFlow` target, relay and exact
ingress/egress counters, `Limit: 1` admission (`0x06` after the 150ms timeout,
then success after release), Drain rejecting a new flow while the active one
finishes and Undrain accepting again, Drain grace leaving the in-flight flow
usable, malformed greeting, wrong version, zero methods, unsupported
auth/CMD/ATYP, RSV set, empty domain and a truncated request, an `OpenFlow`
error answered `0x05`, the `State` snapshot, log fields with no
credential-like keys, and `Close` stopping the listener, force-closing an
active flow and cancelling a blocked `OpenFlow`. The accept loop and every
handler are awaited, so a leaked goroutine fails the test. `flow_test.go` pins
the bounded 500-entry ring: capacity and drop count, newest-first `Flows`, a
draining reject and an `OpenFlow` failure recorded with the host-only target,
a handshake reject with no invented target, and a relayed flow's byte counts
and `ok` status.

`internal/supervisor/supervisor_test.go` re-executes the test binary as the
managed child and binds ephemeral loopback ports only. It covers start →
`started`/`ready` → stop → `exited` with `PID`/`Get`/`List` and port-closed
checks, environment-value redaction in logs, a missing binary, invalid specs,
a duplicate node id, readiness timeout (`ErrNotReady` and `*ReadyError`), a
child exiting before ready, stderr events plus the bounded ring, unknown
`Stop`/`Signal`, `Signal` on a running node, and `Close` stopping two nodes,
idempotency and `Start` after close.

`internal/runtime/lab_test.go` pins the read-only contract: overview modes/counts and warnings,
flags with provenance and secret presence only, gate counters joined to store nodes, redacted
session SIDs (`fake…(8)` in the fixture), claimed-only egress, the profile catalog and sample
matrix, the log ring window, diagnostics facts, per-account servers/device hashes, and the
`enabled:false` reason for every dependency that is off. `flows_test.go` proves
the cross-node join and ordering, the redacted SID, the aggregate `dropped`
count, and one real `gate.Server` end to end. `internal/api/lab_test.go` covers
the HTTP surface: bearer 401 vs 200, the disabled bodies for all nine Lab
routes plus the two account reads, log query validation (bad
`limit`/`level`/`since` → 400), unknown account → 404, and that no credential,
raw SID or full device id appears in any response. `flows_test.go` pins
`GET /v1/lab/flows`: 401, default/valid/invalid `limit`, the
`{enabled,flows,dropped}` shape, and an authenticated HTTP row produced by a
real gate.
`TestLabEgressCheckHandler`, `TestLabEgressCheckProbeFailureIsA200Record` and
`TestLabEgressCheckWithoutReader` pin the POST: 401 unauthenticated, trimmed `nodeId`, the 200
record, 400 for empty/blank/unknown-field/wrong-type bodies, 409 without a gate or reader, 429
inside the cooldown, and a failed probe as a 200 record with `error`.
`internal/lablog/ring_test.go` covers the bounded window, allowlist-only attributes, filters and
handler forwarding; `internal/store/honesty_test.go`
covers the audit labels behind the probe and synthetic-rotation rows the dashboard shows.

## Review fixes (AJS-53)

The line-by-line review in `research/79-code-review/REPORT.md` raised 12
findings; all are fixed in this wave with no dashboard/API JSON change.

| # | Sev | Fix | Code | Regression test |
|---|---|---|---|---|
| F1 | high | child env is an allowlist (`PATH`, `HOME`, `SystemRoot`, `TMPDIR`, `TEMP`, `TMP`) plus `Spec.Env`; `os.Environ()` is never inherited | `internal/supervisor/supervisor.go` (`childEnv`) | `TestStartUsesEnvAllowlist` |
| F2 | med | `paginate` clamps `offset`/`limit` before adding, so no negative slice bound | `internal/api/server.go` | `TestPagination` (MaxInt64 on all six list routes) |
| F3 | med | publisher `rotateMu` serialises the whole filter read-modify-write; `ResinPanel.RotateLease` holds `opMu` so reconcile/drain cannot reset filters mid-rotation | `internal/publisher/publisher.go`, `internal/runtime/panel.go` | `TestRotateSerialisesConcurrentRotations`, `TestReconcileWaitsForRotation` |
| F4 | med | a transient Accept error logs, backs off 50 ms and continues; only `net.ErrClosed` ends the loop | `internal/gate/server.go` | `TestAcceptErrorKeepsServing` |
| F5 | med | audit capped at 1000 and requests at 10000, oldest dropped on insert; `Restore` trims oversized snapshots | `internal/store/store.go`, `users_keys.go`, `persist.go` | `TestAuditRetention`, `TestRestoreTrimsHistories`, `TestPrependCapped` |
| F6 | med | `CreateAccount` reserves the id and label, seals outside the write lock, then re-locks to insert; `AccountSecret` copies the sealed blob before `Open` | `internal/store/store.go` | `TestCreateAccountSealsOutsideLock`, `TestCreateAccountReservesLabel`, `TestCreateDeleteAccountConcurrent` |
| F7 | low | `Render` sorts a copy of the nodes by tag before formatting; the gate port no longer decides order | `internal/publisher/publisher.go` | `TestRenderSortsByTagNotPort` |
| F8 | low | `cloneUser`, `CreateUser` and `UpdateUser` deep-copy `QuotaBytesMonth` | `internal/store/store.go`, `users_keys.go` | `TestUserQuotaPointerNotAliased` |
| F9 | low | success bodies are read through an 8 MiB limit and rejected when over it | `internal/resin/client.go` | `TestSuccessBodyIsSizeLimited` |
| F10 | low | `ReadTimeout: 15s` and `WriteTimeout: 120s` added beside the header/idle timeouts | `cmd/ajs-accountd/main.go` | — (wiring only; no unit test) |
| F11 | low | `decodeJSON` sets `DisallowUnknownFields`; a typo answers 400 without mutating state | `internal/api/server.go` | `TestUnknownJSONFieldRejected` |
| F12 | low | `Rotate` clears the decrypted plaintext after re-sealing, including the error path | `internal/secret/store.go` | `TestRotateTwiceKeepsEntriesUsable` (plus the existing `TestRotate`) |

Residual risk / notes:

- F1 is latent until the supervisor is wired into `main`; the package doc and
  the `Spec.Env` comment now state the allowlist contract.
- F6 leaves the reserved account id unused when `Seal` fails (the sequence is
  not rolled back, so concurrent creates cannot collide). If a `Restore`
  inserts the same label while a create is sealing, the defensive conflict
  leaves an orphan sealed entry in the secret store; `Restore` is a startup-only
  path in production.
- F7 changes the subscription text order for a fixture whose ports do not
  ascend with its tags. The first reconcile after upgrading PATCHes the new
  content once; Resin compares content as a whole, so routing is unaffected.
- F11 rejects unknown fields on every JSON body. The dashboard sends exactly
  the declared fields for accounts, users, keys, mutations and settings, so its
  payloads are unchanged. `PATCH /v1/settings` keeps its map-based allowlist
  because `DisallowUnknownFields` does not apply to maps.
- F5 keeps the newest-first order; a snapshot written by an older build with
  more rows is trimmed on `Restore`.

## HMC evidence

Copy only `daemon/` to the HMC scratch path:

```bash
cd D:/Project/LovePlusSpeedToProxy
tar -czf - daemon | ssh hmc 'mkdir -p /srv/dwgxhomecloud/scratch/ajs-accountd-ajs30 \
  && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-accountd-ajs30 --strip-components=1'
```

On HMC (`go1.25.14 linux/amd64`):

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-accountd-ajs30 && go test -count=1 ./... && go build ./... && echo BUILD_OK
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/api	0.036s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	0.013s
ok  	ajsaccountd/internal/resin	0.212s
ok  	ajsaccountd/internal/runtime	0.039s
?   	ajsaccountd/internal/store	[no test files]
BUILD_OK
```

### Account control (AJS-33)

The two sibling trees were copied to a fresh scratch root with the capture
fixtures, so no other worker's scratch tree was touched:

```bash
tar -czf - daemon emulator | ssh hmc \
  'tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-33-accountcontrol'
tar -czf - -C research/60-dynamic captures | ssh hmc \
  'tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-33-accountcontrol/research/60-dynamic'
```

On HMC (`go1.26.0 linux/amd64`):

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-33-accountcontrol/emulator && go build ./... && go test ./...
?   	ajsproxy/apifacade	[no test files]
?   	ajsproxy/cmd/ajsproxy	[no test files]
?   	ajsproxy/fvfacade	[no test files]
ok  	ajsproxy/internal/api	0.004s
ok  	ajsproxy/internal/config	0.002s
ok  	ajsproxy/internal/fv	0.024s
ok  	ajsproxy/internal/proxy	0.003s

$ cd ../daemon && go build ./... && go test ./...
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	0.007s
ok  	ajsaccountd/internal/api	0.032s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	0.013s
ok  	ajsaccountd/internal/resin	0.212s
ok  	ajsaccountd/internal/runtime	0.032s
ok  	ajsaccountd/internal/secret	0.003s
ok  	ajsaccountd/internal/store	0.003s

$ go test ./internal/account -v -count=1
--- PASS: TestLoginSigned (0.00s)
--- PASS: TestLoginUnsigned (0.00s)
--- PASS: TestLoginRejected (0.00s)
--- PASS: TestLoginSyncAndServers (0.00s)
--- PASS: TestLoginSyncRequiresToken (0.00s)
--- PASS: TestLoginFailoverOnTransportError (0.00s)
--- PASS: TestNoFailoverOnHTTPError (0.00s)
--- PASS: TestNoFailoverOnProtocolError (0.00s)
--- PASS: TestNewDeviceProfiles (0.00s)
--- PASS: TestSecretRedaction (0.00s)
--- PASS: TestProtocolSeam (0.00s)
PASS
ok  	ajsaccountd/internal/account	0.006s
```

### FV session factory (AJS-34)

The two sibling trees and the capture fixtures were copied to a fresh scratch
root, so no other worker's tree was touched:

```bash
tar -czf - daemon emulator | ssh hmc \
  'mkdir -p /srv/dwgxhomecloud/scratch/ajs-34-sessionfactory \
  && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-34-sessionfactory'
tar -czf - -C research/60-dynamic captures | ssh hmc \
  'mkdir -p /srv/dwgxhomecloud/scratch/ajs-34-sessionfactory/research/60-dynamic \
  && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-34-sessionfactory/research/60-dynamic'
```

On HMC (`go1.26.0 linux/amd64`):

```console
$ cd .../ajs-34-sessionfactory/emulator && go test -count=1 ./... && go build ./...
?   	ajsproxy/apifacade	[no test files]
?   	ajsproxy/cmd/ajsproxy	[no test files]
?   	ajsproxy/fvfacade	[no test files]
ok  	ajsproxy/internal/api	0.003s
ok  	ajsproxy/internal/config	0.003s
ok  	ajsproxy/internal/fv	0.055s
ok  	ajsproxy/internal/proxy	0.003s
EMULATOR_BUILD_OK

$ cd ../daemon && go test -count=1 ./... && go build ./...
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	0.047s
ok  	ajsaccountd/internal/api	0.035s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	0.013s
ok  	ajsaccountd/internal/resin	0.213s
ok  	ajsaccountd/internal/runtime	0.037s
ok  	ajsaccountd/internal/secret	0.003s
ok  	ajsaccountd/internal/store	0.004s
DAEMON_BUILD_OK

$ go test -count=1 -v -run 'TestFactoryOpen|TestLinux423Binary' ./internal/account
=== RUN   TestFactoryOpenAgainstMockFV
--- PASS: TestFactoryOpenAgainstMockFV (0.01s)
=== RUN   TestFactoryOpenRejectsDeniedAuth
--- PASS: TestFactoryOpenRejectsDeniedAuth (0.01s)
=== RUN   TestLinux423BinaryProfile
--- PASS: TestLinux423BinaryProfile (0.00s)
PASS
ok  	ajsaccountd/internal/account	0.017s
```

Both runs are in-process only: the mock listens on `127.0.0.1`, no vendor host
is contacted and no credential is involved.

### Account login wiring (AJS-38)

The two sibling trees were copied into the scratch root that already holds the
capture fixtures; no other worker's tree was touched:

```bash
scp -r daemon emulator hmc:/srv/dwgxhomecloud/scratch/ajs-multimodule/
```

On HMC (`go1.25.14 linux/amd64`, `GOTOOLCHAIN=auto`):

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-multimodule/daemon && go test -count=1 ./... && go build ./... && echo BUILD_OK && go vet ./... && echo VET_OK
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	0.060s
ok  	ajsaccountd/internal/api	0.033s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	0.012s
ok  	ajsaccountd/internal/resin	0.213s
ok  	ajsaccountd/internal/runtime	0.033s
ok  	ajsaccountd/internal/secret	0.003s
ok  	ajsaccountd/internal/store	0.004s
BUILD_OK
VET_OK
```

Startup proves the switch and the no-contact boundary: with an unreachable
`AJS_API_HOSTS` the daemon still starts and logs the enabled state (the lab mock
is `http://`, which startup only accepts with `AJS_ALLOW_PLAINTEXT=1`), and
without the switch it logs the disabled state:

```console
$ AJS_ALLOW_INSECURE=1 AJS_ACCOUNT_LOGIN=1 AJS_ALLOW_PLAINTEXT=1 \
    AJS_API_HOSTS=http://127.0.0.1:1 \
    AJS_LISTEN=127.0.0.1:22692 timeout 2s /tmp/ajs-accountd-login
level=INFO msg=account_login_enabled hosts=1 profile=linux-4.2.3.0-binary
level=INFO msg="ajs-accountd listening" addr=127.0.0.1:22692 auth=insecure

$ AJS_ALLOW_INSECURE=1 AJS_LISTEN=127.0.0.1:22691 timeout 2s /tmp/ajs-accountd-login
level=INFO msg=account_login_disabled reason="AJS_ACCOUNT_LOGIN is not 1"
level=INFO msg="ajs-accountd listening" addr=127.0.0.1:22691 auth=insecure
```

Every login test uses an in-process `httptest` `/client.php`; no vendor host was
contacted and no credential exists in the tree. A process-level smoke with
`AJS_MASTER_KEY` set (so the password is sealed) and a localhost fake
`/client.php` created an account and logged in through the API:

```console
$ curl -s -X POST http://127.0.0.1:22693/v1/accounts -d '{...}'
{"id":"acc-07","label":"login-smoke","username":"smoke@example.com","state":"active",...}
$ curl -s -X POST http://127.0.0.1:22693/v1/accounts/acc-07/actions/login
{"id":"acc-07","label":"login-smoke","username":"user","state":"active",
 "lastLoginAt":"2026-10-23T04:16:25.873823903Z",...}
$ curl -s 'http://127.0.0.1:22693/v1/audit?limit=2'
[{"action":"account.login","target":"login-smoke",
  "detail":"Session established · membership vip",...},
 {"action":"account.create",...}]
level=INFO msg=account_login account=acc-07 state=active membership=vip
```

The daemon and fake vendor were stopped afterward; both ports were free and no
process was left running. Live vendor acceptance of the `linux-4.2.3.0`
profile is still `[UNVERIFIED]`.

### Panel smoke (fake Resin, dev host)

The process-level wiring was also exercised on the dev host against a
throwaway fake admin API (stdlib `net/http`, not in the repo) so no live Resin
or vendor call is involved. The transcripts below record the 2026-10-23 runs,
which used a local placeholder token; the startup guard now refuses documented
placeholders and tokens under 32 characters, so the commands show
`AJS_ADMIN_TOKEN` from the environment
(`export AJS_ADMIN_TOKEN=$(openssl rand -hex 32)`, or `AJS_ALLOW_INSECURE=1` on
loopback):

```console
$ AJS_LISTEN=127.0.0.1:12261 AJS_ADMIN_TOKEN="$AJS_ADMIN_TOKEN" \
  AJS_RESIN_URL=http://127.0.0.1:12260 AJS_RESIN_ADMIN_TOKEN=local-resin-placeholder \
  ./ajs-accountd.exe
time=2026-10-23T12:28:03.712+09:00 level=INFO msg=resin_connected subscription_id=sub-smoke platform_id=plat-smoke
time=2026-10-23T12:28:03.713+09:00 level=INFO msg="ajs-accountd listening" addr=127.0.0.1:12261 auth=bearer
```

Fake admin log — startup order, then the idempotent reconcile:

```console
PATCH /api/v1/system/config
GET /api/v1/subscriptions
POST /api/v1/subscriptions
GET /api/v1/platforms
POST /api/v1/platforms
PATCH /api/v1/subscriptions/sub-smoke
POST /api/v1/subscriptions/sub-smoke/actions/refresh
GET /api/v1/system/info
PATCH /api/v1/system/config          # POST /v1/reconcile: content unchanged,
GET /api/v1/subscriptions            # so no subscription PATCH or refresh
GET /api/v1/platforms
GET /api/v1/system/info
```

```console
$ curl -s -o top.json -w 'GET /v1/topology HTTP %{http_code}\n' \
    -H "Authorization: Bearer $AJS_ADMIN_TOKEN" http://127.0.0.1:12261/v1/topology
GET /v1/topology HTTP 200
$ python -c "import json;d=json.load(open('top.json'));print('subscription_id=%s platform_id=%s nodes=%d content_lines=%d last_good_lines=%d resin_version=%s' % (d['subscription_id'],d['platform_id'],len(d['nodes']),d['content'].count(chr(10)),d['last_good'].count(chr(10)),d['resin_version']))"
subscription_id=sub-smoke platform_id=plat-smoke nodes=8 content_lines=8 last_good_lines=8 resin_version=v1.3.0

$ curl -s -o rec.json -w 'POST /v1/reconcile HTTP %{http_code}\n' -X POST \
    -H "Authorization: Bearer $AJS_ADMIN_TOKEN" http://127.0.0.1:12261/v1/reconcile
POST /v1/reconcile HTTP 200
$ python -c "import json;d=json.load(open('rec.json'));print('reconcile nodes=%d same_content=%s' % (len(d['nodes']), d['content']==json.load(open('top.json'))['content']))"
reconcile nodes=8 same_content=True
```

Without the token the same binary logs `resin_disabled` and keeps the store
view:

```console
time=2026-10-23T12:27:09.483+09:00 level=INFO msg=resin_disabled reason="AJS_RESIN_ADMIN_TOKEN is not set"
$ curl -s -o top-off.json -w 'topology HTTP %{http_code}\n' \
    -H "Authorization: Bearer $AJS_ADMIN_TOKEN" http://127.0.0.1:12261/v1/topology
topology HTTP 200
$ python -c "import json;d=json.load(open('top-off.json'));print('subscription=',d['subscription'],'nodes=',len(d['nodes']),'platforms=',len(d['platforms']))"
subscription= ajs-live nodes= 12 platforms= 4
$ curl -s -w '\nreconcile HTTP %{http_code}\n' -X POST \
    -H "Authorization: Bearer $AJS_ADMIN_TOKEN" http://127.0.0.1:12261/v1/reconcile
{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"resin is not configured; set AJS_RESIN_ADMIN_TOKEN"}}
reconcile HTTP 503
```

With a token but an unreachable Resin, startup degrades instead of exiting:

```console
time=2026-10-23T12:27:18.547+09:00 level=WARN msg=resin_start_failed error="runtime: ensure probe config: publisher: set max_latency_test_interval: resin: PATCH /api/v1/system/config: Patch \"http://127.0.0.1:12262/api/v1/system/config\": dial tcp 127.0.0.1:12262: connectex: No connection could be made because the target machine actively refused it."
$ curl -s -w '\ntopology HTTP %{http_code}\n' \
    -H "Authorization: Bearer $AJS_ADMIN_TOKEN" http://127.0.0.1:12261/v1/topology
{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"resin topology unavailable"}}
topology HTTP 503
$ curl -s -w '\nreconcile HTTP %{http_code}\n' -X POST \
    -H "Authorization: Bearer $AJS_ADMIN_TOKEN" http://127.0.0.1:12261/v1/reconcile
{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"resin reconcile failed"}}
reconcile HTTP 503
```

Smoke run on `127.0.0.1:2261` (recorded with a local placeholder before the
startup guard; shown with a generated `AJS_ADMIN_TOKEN`):

```console
$ AJS_LISTEN=127.0.0.1:2261 AJS_ADMIN_TOKEN="$AJS_ADMIN_TOKEN" go run ./cmd/ajs-accountd &
$ curl -s -o /tmp/ajs-health.json -w 'HTTP %{http_code}\n' http://127.0.0.1:2261/healthz
HTTP 200
$ cat /tmp/ajs-health.json
{"status":"ok","service":"ajs-accountd","version":"0.1.0","time":"2026-10-23T03:05:57.153798762Z","accounts":{"active":4,"auth_failed":1,"expired":0,"paused":1}}

$ curl -s -o /tmp/ajs-accounts.json -w 'HTTP %{http_code}\n' \
    -H "Authorization: Bearer $AJS_ADMIN_TOKEN" http://127.0.0.1:2261/v1/accounts
HTTP 200
$ jq length /tmp/ajs-accounts.json
6
$ jq -c '.[0]' /tmp/ajs-accounts.json
{"id":"acc-01","label":"Northwind Ops","username":"demo-northwind","platform":"android","state":"active","concurrency":200,"regions":["cn-zj","cn-sh","hk"],"nodeCount":3,"liveNodeCount":2,"usageBytes":82400000000,"expiresAt":"2026-11-10T03:05:57.130488609Z","lastLoginAt":"2026-10-23T02:53:57.130488609Z","createdAt":"2026-07-19T03:05:57.130488609Z"}

$ curl -s -o /tmp/ajs-unauth.json -w 'HTTP %{http_code}\n' http://127.0.0.1:2261/v1/accounts
HTTP 401
$ cat /tmp/ajs-unauth.json
{"error":{"code":"UNAUTHORIZED","message":"missing or invalid bearer token"}}
```

The daemon logged `ajs-accountd shutting down`, `pgrep -x ajs-accountd`
returned nothing, and port 2261 was free afterward. No process was left running.

### Gate (AJS-40)

Both trees were copied to a fresh scratch root because `daemon/go.mod` replaces
`ajsproxy` with `../emulator`:

```bash
cd D:/Project/LovePlusSpeedToProxy
tar -czf - daemon emulator | ssh hmc 'mkdir -p /srv/dwgxhomecloud/scratch/ajs-40-gate \
  && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-40-gate'
```

On HMC (`go1.26.0 linux/amd64`, selected by `GOTOOLCHAIN=auto` from the module
cache):

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-40-gate/daemon && go test -count=1 ./... && go build ./... && echo BUILD_OK
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	0.053s
ok  	ajsaccountd/internal/api	0.032s
ok  	ajsaccountd/internal/gate	0.462s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	0.011s
ok  	ajsaccountd/internal/resin	0.211s
ok  	ajsaccountd/internal/runtime	0.030s
ok  	ajsaccountd/internal/secret	0.003s
ok  	ajsaccountd/internal/store	0.003s
BUILD_OK
```

The gate tests bind ephemeral loopback ports only, and each server is closed by
its test cleanup.

### Supervisor (AJS-43)

Both trees were copied to a fresh scratch root because `daemon/go.mod` replaces
`ajsproxy` with `../emulator`:

```bash
cd D:/Project/LovePlusSpeedToProxy
tar -czf - daemon emulator | ssh hmc 'mkdir -p /srv/dwgxhomecloud/scratch/ajs-43-supervisor \
  && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-43-supervisor'
```

On HMC (`go1.25.14 linux/amd64`, `GOTOOLCHAIN=auto` selects `go1.26.0`):

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-43-supervisor/daemon && go test -count=1 ./... && go build ./... && echo BUILD_OK
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	0.043s
ok  	ajsaccountd/internal/api	0.040s
ok  	ajsaccountd/internal/gate	0.477s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	0.013s
ok  	ajsaccountd/internal/resin	0.213s
ok  	ajsaccountd/internal/runtime	0.036s
ok  	ajsaccountd/internal/secret	0.005s
ok  	ajsaccountd/internal/store	0.008s
ok  	ajsaccountd/internal/supervisor	0.614s
BUILD_OK

$ go test -count=1 -v ./internal/supervisor
--- PASS: TestSupervisorHelper (0.00s)
--- PASS: TestStartReadyStop (0.05s)
--- PASS: TestStartBadBinary (0.00s)
--- PASS: TestStartInvalidSpec (0.00s)
    --- PASS: TestStartInvalidSpec/empty_node_id (0.00s)
    --- PASS: TestStartInvalidSpec/empty_binary (0.00s)
    --- PASS: TestStartInvalidSpec/empty_ready_addr (0.00s)
    --- PASS: TestStartInvalidSpec/malformed_ready_addr (0.00s)
--- PASS: TestStartDuplicate (0.05s)
--- PASS: TestStartReadinessTimeout (0.30s)
--- PASS: TestStartExitsBeforeReady (0.00s)
--- PASS: TestStderrCapture (0.05s)
--- PASS: TestStopAndSignalUnknown (0.00s)
--- PASS: TestSignalRunning (0.05s)
--- PASS: TestCloseStopsAll (0.10s)
PASS
ok  	ajsaccountd/internal/supervisor	0.613s

$ go vet ./... && echo VET_OK
VET_OK
```

The child in every test is the test binary itself, started through
`os.Executable()`; the supervisor tests bind loopback ports only and contact no
vendor host.

### Persistence (AJS-45)

Both sibling trees and the capture fixtures were copied to a fresh scratch
root, so no other worker's tree was touched:

```bash
cd D:/Project/LovePlusSpeedToProxy
tar -czf - daemon emulator | ssh hmc \
  'mkdir -p /srv/dwgxhomecloud/scratch/ajs-45-persistence \
  && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-45-persistence'
tar -czf - -C research/60-dynamic captures | ssh hmc \
  'mkdir -p /srv/dwgxhomecloud/scratch/ajs-45-persistence/research/60-dynamic \
  && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-45-persistence/research/60-dynamic'
```

On HMC (`go1.25.14 linux/amd64`, `GOTOOLCHAIN=auto` selects `go1.26.0`):

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-45-persistence/daemon && go test -count=1 ./... && go build ./... && echo BUILD_OK
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	0.047s
ok  	ajsaccountd/internal/api	0.039s
ok  	ajsaccountd/internal/gate	0.472s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	0.017s
ok  	ajsaccountd/internal/resin	0.216s
ok  	ajsaccountd/internal/runtime	0.040s
ok  	ajsaccountd/internal/secret	0.004s
ok  	ajsaccountd/internal/store	0.015s
ok  	ajsaccountd/internal/supervisor	0.616s
BUILD_OK

$ go vet ./... && echo VET_OK
VET_OK

$ go test -count=1 -v -run "TestSnapshot|TestSave|TestLoad|TestRestore|TestDeleteThenReload" ./internal/store
=== RUN   TestSnapshotRoundtrip
--- PASS: TestSnapshotRoundtrip (0.00s)
=== RUN   TestSaveFileModeAndNoTempLeftovers
--- PASS: TestSaveFileModeAndNoTempLeftovers (0.00s)
=== RUN   TestSaveOverwritesAtomically
--- PASS: TestSaveOverwritesAtomically (0.00s)
=== RUN   TestLoadMissingFile
--- PASS: TestLoadMissingFile (0.00s)
=== RUN   TestLoadCorruptFileLeavesStoreUnchanged
--- PASS: TestLoadCorruptFileLeavesStoreUnchanged (0.00s)
=== RUN   TestRestoreRejectsWrongVersion
--- PASS: TestRestoreRejectsWrongVersion (0.00s)
=== RUN   TestRestoreRejectsEmptyData
--- PASS: TestRestoreRejectsEmptyData (0.00s)
=== RUN   TestRestoreReplacesEveryCollection
--- PASS: TestRestoreReplacesEveryCollection (0.00s)
=== RUN   TestSaveWritesNoPlaintextPassword
--- PASS: TestSaveWritesNoPlaintextPassword (0.00s)
=== RUN   TestDeleteThenReloadDoesNotReuseIDs
--- PASS: TestDeleteThenReloadDoesNotReuseIDs (0.00s)
=== RUN   TestRestoreRebuildsCountersFromIdentifiers
--- PASS: TestRestoreRebuildsCountersFromIdentifiers (0.00s)
PASS
ok  	ajsaccountd/internal/store	0.009s

$ cd ../emulator && go test -count=1 ./... && go build ./... && echo EMULATOR_OK
?   	ajsproxy/apifacade	[no test files]
?   	ajsproxy/cmd/ajsproxy	[no test files]
?   	ajsproxy/fvfacade	[no test files]
ok  	ajsproxy/internal/api	0.005s
ok  	ajsproxy/internal/config	0.004s
ok  	ajsproxy/internal/fv	0.078s
ok  	ajsproxy/internal/proxy	0.005s
EMULATOR_OK
```

A loopback restart smoke on HMC used `AJS_ALLOW_INSECURE=1`, no Resin token and
no `AJS_ACCOUNT_LOGIN`, so no vendor host was contacted. It created one account,
waited for an interval snapshot, sent SIGTERM, restarted against the same
`AJS_STATE_DIR`, and read the account back:

```console
state_mode=600
1                                   # smoke-persist present after restart
… msg=state_missing path=/tmp/ajs45/state/state.json
… msg=state_saved path=/tmp/ajs45/state/state.json reason=interval
… msg=state_saved path=/tmp/ajs45/state/state.json reason=shutdown
… msg=state_loaded path=/tmp/ajs45/state/state.json accounts=7 nodes=12 leases=8
… msg=state_saved path=/tmp/ajs45/state/state.json reason=shutdown
0                                   # plaintext password occurrences in state.json
```

The same binary refused to start with a corrupt file and with version 2, and
the store was not served in either case:

```console
ERROR ajs-accountd failed error="load state: state file /tmp/ajs45/corrupt/state.json: decode state snapshot: invalid character 'n' looking for beginning of object key string"
exit=1
ERROR ajs-accountd failed error="load state: state file /tmp/ajs45/version/state.json: unsupported state snapshot version 2 (want 1)"
exit=1
```

### Review fixes (AJS-53)

Both trees were copied to a fresh scratch root (no other worker's tree was
touched); the emulator's capture fixtures were added separately because the
daemon does not need them:

```bash
tar -czf - daemon emulator | ssh hmc \
  'root=/srv/dwgxhomecloud/scratch/ajs-accountd-ajs53-$(date +%s); mkdir -p "$root" && tar -xzf - -C "$root"'
tar -czf - -C research/60-dynamic captures | ssh hmc \
  'mkdir -p /srv/dwgxhomecloud/scratch/ajs-accountd-ajs53-<ts>/research/60-dynamic \
   && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-accountd-ajs53-<ts>/research/60-dynamic'
```

On HMC (`go1.26.0 linux/amd64`; `go.mod` is `go 1.26.0`, so `GOTOOLCHAIN=auto`
upgrades the host's `go1.25.14`):

```console
$ cd .../daemon && go test -race -count=1 ./...
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	1.093s
ok  	ajsaccountd/internal/api	1.179s
ok  	ajsaccountd/internal/gate	1.566s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	1.058s
ok  	ajsaccountd/internal/resin	1.356s
ok  	ajsaccountd/internal/runtime	1.298s
ok  	ajsaccountd/internal/secret	1.023s
ok  	ajsaccountd/internal/store	1.492s
ok  	ajsaccountd/internal/supervisor	7.715s
$ go vet ./... && echo VET_OK
VET_OK
$ test -z "$(gofmt -l .)" && echo GOFMT_CLEAN
GOFMT_CLEAN

$ cd .../emulator && go test -race -count=1 ./...
?   	ajsproxy/apifacade	[no test files]
?   	ajsproxy/cmd/ajsproxy	[no test files]
?   	ajsproxy/fvfacade	[no test files]
ok  	ajsproxy/internal/api	1.015s
ok  	ajsproxy/internal/config	1.013s
ok  	ajsproxy/internal/fv	1.044s
ok  	ajsproxy/internal/proxy	1.015s
$ go vet ./... && echo VET_OK
VET_OK
$ test -z "$(gofmt -l .)" && echo GOFMT_CLEAN
GOFMT_CLEAN
```

### Protocol fixes (AJS-55)

Both trees and the emulator's capture fixtures were copied to a fresh scratch
root; no other worker's tree was touched:

```bash
tar -czf - daemon emulator | ssh hmc \
  'mkdir -p /srv/dwgxhomecloud/scratch/ajs-55-protocolfix \
   && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-55-protocolfix'
tar -czf - -C research/60-dynamic captures | ssh hmc \
  'mkdir -p /srv/dwgxhomecloud/scratch/ajs-55-protocolfix/research/60-dynamic \
   && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-55-protocolfix/research/60-dynamic'
```

On HMC (`go1.25.14 linux/amd64`; `go.mod` is `go 1.26.0`, so `GOTOOLCHAIN=auto`
selects the newer toolchain):

```console
$ cd .../emulator && go build ./... && echo EMULATOR_BUILD_OK && go test -race -count=1 ./...
EMULATOR_BUILD_OK
?   	ajsproxy/apifacade	[no test files]
?   	ajsproxy/cmd/ajsproxy	[no test files]
?   	ajsproxy/fvfacade	[no test files]
ok  	ajsproxy/internal/api	1.020s
ok  	ajsproxy/internal/config	1.015s
ok  	ajsproxy/internal/fv	1.051s
ok  	ajsproxy/internal/proxy	1.017s
$ go vet ./... && echo EMULATOR_VET_OK && test -z "$(gofmt -l .)" && echo EMULATOR_GOFMT_CLEAN
EMULATOR_VET_OK
EMULATOR_GOFMT_CLEAN

$ cd ../daemon && go build ./... && echo DAEMON_BUILD_OK && go test -race -count=1 ./...
DAEMON_BUILD_OK
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	1.089s
ok  	ajsaccountd/internal/api	1.191s
ok  	ajsaccountd/internal/gate	1.568s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	1.070s
ok  	ajsaccountd/internal/resin	1.375s
ok  	ajsaccountd/internal/runtime	1.311s
ok  	ajsaccountd/internal/secret	1.028s
ok  	ajsaccountd/internal/store	1.518s
ok  	ajsaccountd/internal/supervisor	7.722s
$ go vet ./... && echo DAEMON_VET_OK && test -z "$(gofmt -l .)" && echo DAEMON_GOFMT_CLEAN
DAEMON_VET_OK
DAEMON_GOFMT_CLEAN
```

The Linux-shape regression records the generated body and compares it with
`control.jsonl:2`; the only differences are the generated `_uid` and the
caller's credentials:

```console
$ go test -run TestLinux423RequestMatchesCapture -v ./internal/api
=== RUN   TestLinux423RequestMatchesCapture
    client_test.go:148: ShapeLinux423 login body (uid/password redacted): _lang=en&_m=linux&_os_dev=x86_64&_os_dim=lid%3dREDACTED%2c8e415e603088426aa42fed420212150c&_os_ver=Linux%20version%206.18.33.2-microsoft-standard-WSL2%20%28root%40f1bbfb02316b%29%20%28gcc%20%28GCC%29%2013.2.0%2c%20GNU%20ld%20%28GNU%20Binutils%29%202.41%29%20%231%20SMP%20PREEMPT_DYNAMIC%20Thu%20Jun%2018%2021%3a54%3a43%20UTC%202026%0a&_site=ajs&_uid=%5bredacted%5d&cmd=ClientApiLogin%2fLogin&password=%5bredacted%5d&username=capture-user&v=4.2.3.0
--- PASS: TestLinux423RequestMatchesCapture (0.00s)
PASS
ok  	ajsproxy/internal/api	0.003s
```

No live vendor host was contacted; live acceptance of either shape remains
`[UNVERIFIED]`.

### Node controller (AJS-62 / B0)

Both trees were copied to a fresh scratch root; no live vendor host or Resin
instance was contacted:

```bash
cd D:/Project/LovePlusSpeedToProxy
tar -czf - daemon emulator | ssh hmc \
  'rm -rf /srv/dwgxhomecloud/scratch/ajs-62-nodecontroller \
   && mkdir -p /srv/dwgxhomecloud/scratch/ajs-62-nodecontroller \
   && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-62-nodecontroller'
```

On HMC (`go1.25.14 linux/amd64`; `GOTOOLCHAIN=auto` selects the cached
`go1.26.0` toolchain the module requires):

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-62-nodecontroller/daemon
$ go test -race -count=1 ./... && go build ./... && go vet ./... && echo ALL_OK
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	1.112s
ok  	ajsaccountd/internal/api	1.185s
ok  	ajsaccountd/internal/gate	1.571s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	1.058s
ok  	ajsaccountd/internal/resin	1.352s
ok  	ajsaccountd/internal/runtime	1.302s
ok  	ajsaccountd/internal/secret	1.027s
ok  	ajsaccountd/internal/store	1.512s
ok  	ajsaccountd/internal/supervisor	7.714s
ALL_OK
```

The controller evidence (real `gate.NewServer` on `127.0.0.1:13000`, fake
session opener, fake reconciler):

```console
$ go test -race -count=1 -v -run 'TestNodeControllerSyncCreatesReusesAndRemoves|TestNodeControllerDrainUndrain|TestNodeControllerStopClosesGates' ./internal/runtime
=== RUN   TestNodeControllerSyncCreatesReusesAndRemoves
    nodes_test.go:385: sync 1: acc-01:s1 server=s1 label=cn-acc01-s1 port=13000 state=LIVE egress=198.51.100.7; acc-01:s2 server=s2 label=cn-acc01-s2 port=13001 state=LIVE
    nodes_test.go:419: sync 2: reused acc-01:s1 port=13000 without a new session
    nodes_test.go:441: sync 3: removed acc-01:s2, port 13001 refused, acc-01:s1 kept port 13000
--- PASS: TestNodeControllerSyncCreatesReusesAndRemoves (0.00s)
=== RUN   TestNodeControllerDrainUndrain
    nodes_test.go:480: drain acc-01:s1: state=DRAINING gate reply=0x05
    nodes_test.go:494: undrain acc-01:s1: state=LIVE gate reply=0x00
--- PASS: TestNodeControllerDrainUndrain (0.00s)
=== RUN   TestNodeControllerStopClosesGates
    nodes_test.go:557: stop: ports 13000/13001 refused, both sessions closed, reconciles=2
--- PASS: TestNodeControllerStopClosesGates (0.00s)
PASS
ok  	ajsaccountd/internal/runtime	1.019s
```

`TestNodeControllerPerServerFailure` records `acc-01:s1 state=LIVE port=13000;
acc-01:s2 state=DOWN port=0`; `TestNodeControllerNoNodeStarted` returns an
aggregate error naming both nodes; `TestAccountLoginHook` proves the hook runs
after a successful login and its error is logged without changing the 200
response. No live vendor login, FV authentication or Resin write was performed;
live acceptance remains `[UNVERIFIED]`.

### Drain hook (AJS-74)

`NodeDrainHook` closes the remaining B0 drain gap: `POST
/v1/nodes/{id}/actions/drain` and `undrain` call the node controller before the
store or the panel, and a hook error answers 500 `INTERNAL` with the node
unchanged. The daemon wires the hook only when `AJS_ACCOUNT_LOGIN=1`, alongside
the login and account-delete hooks.

Fresh HMC scratch (`/srv/dwgxhomecloud/scratch/ajs-74-drainhook`); no vendor
host or Resin instance was contacted:

```console
$ go version
go version go1.26.0 linux/amd64
$ go test -race -count=1 ./... && go build ./... && go vet ./... && echo ALL_OK
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	1.105s
ok  	ajsaccountd/internal/api	1.200s
ok  	ajsaccountd/internal/gate	1.570s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	1.062s
ok  	ajsaccountd/internal/resin	1.356s
ok  	ajsaccountd/internal/runtime	1.303s
ok  	ajsaccountd/internal/secret	1.036s
ok  	ajsaccountd/internal/store	1.516s
ok  	ajsaccountd/internal/supervisor	7.719s
ALL_OK
```

Hook + gate evidence (real `gate.NewServer` on `127.0.0.1:13000`, fake session
opener, fake reconciler, loopback HTTP):

```console
$ go test -race -count=1 -v -run 'TestNodeDrainHTTPHookDrivesGate|TestNodeControllerDrainUndrain' ./internal/runtime
=== RUN   TestNodeControllerDrainUndrain
    nodes_test.go:483: drain acc-01:s1: state=DRAINING gate reply=0x05
    nodes_test.go:497: undrain acc-01:s1: state=LIVE gate reply=0x00
--- PASS: TestNodeControllerDrainUndrain (0.00s)
=== RUN   TestNodeDrainHTTPHookDrivesGate
    nodes_test.go:580: http drain acc-01:s1: state=DRAINING gate reply=0x05
    nodes_test.go:598: http undrain acc-01:s1: state=LIVE gate reply=0x00
    nodes_test.go:616: http drain after stop: 500 INTERNAL, node stayed LIVE
--- PASS: TestNodeDrainHTTPHookDrivesGate (0.00s)
PASS
ok  	ajsaccountd/internal/runtime	1.019s
$ go test -race -count=1 -v -run 'TestNodeDrainHook|TestNodeDrainUndrainProbe' ./internal/api
=== RUN   TestNodeDrainHook
=== RUN   TestNodeDrainHook/hook_runs_before_the_store_and_drain_succeeds
=== RUN   TestNodeDrainHook/hook_runs_before_the_store_and_undrain_succeeds
=== RUN   TestNodeDrainHook/hook_runs_before_the_panel
=== RUN   TestNodeDrainHook/drain_hook_error_aborts_before_the_panel_and_the_store
=== RUN   TestNodeDrainHook/store_refusal_from_the_hook_keeps_its_status
=== RUN   TestNodeDrainHook/undrain_hook_error_aborts_before_the_panel_and_the_store
=== RUN   TestNodeDrainHook/missing_node_skips_the_hook_and_the_panel
=== RUN   TestNodeDrainHook/nil_hook_keeps_the_store-only_path
--- PASS: TestNodeDrainHook (0.01s)
    --- PASS: TestNodeDrainHook/hook_runs_before_the_store_and_drain_succeeds (0.00s)
    --- PASS: TestNodeDrainHook/hook_runs_before_the_store_and_undrain_succeeds (0.00s)
    --- PASS: TestNodeDrainHook/hook_runs_before_the_panel (0.00s)
    --- PASS: TestNodeDrainHook/drain_hook_error_aborts_before_the_panel_and_the_store (0.00s)
    --- PASS: TestNodeDrainHook/store_refusal_from_the_hook_keeps_its_status (0.00s)
    --- PASS: TestNodeDrainHook/undrain_hook_error_aborts_before_the_panel_and_the_store (0.00s)
    --- PASS: TestNodeDrainHook/missing_node_skips_the_hook_and_the_panel (0.00s)
    --- PASS: TestNodeDrainHook/nil_hook_keeps_the_store-only_path (0.00s)
=== RUN   TestNodeDrainUndrainProbe
--- PASS: TestNodeDrainUndrainProbe (0.01s)
PASS
ok  	ajsaccountd/internal/api	1.029s
```

The runtime test drives `api.New` with `NodeDrainHook` wired to the real
controller: drain answers `0x05` to a new SOCKS5 CONNECT, undrain answers
`0x00` and relays, and after `controller.Stop` a drain answers 500 while the
node stays LIVE. The API test proves hook-before-store and hook-before-panel,
abort-on-error with no panel call and no state change, the missing-node 404
without a hook call, the nil-hook store-only path, and the 409 status kept when
the hook forwards the store's DOWN-node refusal. Live vendor drain remains
`[UNVERIFIED]`.

### Delete gate (AJS-65)

The same fresh-scratch workflow; no live vendor host or Resin instance was
contacted:

```bash
cd D:/Project/LovePlusSpeedToProxy
tar -czf - daemon emulator | ssh hmc \
  'rm -rf /srv/dwgxhomecloud/scratch/ajs-65-deletegate \
   && mkdir -p /srv/dwgxhomecloud/scratch/ajs-65-deletegate \
   && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-65-deletegate'
```

On HMC (`go1.25.14 linux/amd64`; `GOTOOLCHAIN=auto` selects the cached
`go1.26.0` toolchain the module requires):

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-65-deletegate/daemon
$ go test -race -count=1 ./...
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	1.098s
ok  	ajsaccountd/internal/api	1.190s
ok  	ajsaccountd/internal/gate	1.554s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	1.048s
ok  	ajsaccountd/internal/resin	1.337s
ok  	ajsaccountd/internal/runtime	1.282s
ok  	ajsaccountd/internal/secret	1.014s
ok  	ajsaccountd/internal/store	1.485s
ok  	ajsaccountd/internal/supervisor	7.705s
$ go build ./... && echo BUILD_OK && go vet ./... && echo VET_OK && test -z "$(gofmt -l .)" && echo GOFMT_CLEAN
BUILD_OK
VET_OK
GOFMT_CLEAN
```

The stop/delete evidence (real `gate.NewServer` on `127.0.0.1:13000`, fake
session opener, fake reconciler, real controller behind `api.New`):

```console
$ go test -race -count=1 -v -run 'TestNodeControllerStopAccount|TestAccountDeleteStopsGatesBeforeStoreCascade' ./internal/runtime
=== RUN   TestNodeControllerStopAccount
    nodes_test.go:659: stop-account acc-01: ports 13000/13001 refused, sessions closed, acc-02:t1 kept port 13002, reconciles=3
--- PASS: TestNodeControllerStopAccount (0.00s)
=== RUN   TestNodeControllerStopAccountAggregatesErrors
    nodes_test.go:732: stop-account errors: node acc-01:s1: close gate: gate close failed
        runtime: reconcile after node sync: resin unavailable
--- PASS: TestNodeControllerStopAccountAggregatesErrors (0.00s)
=== RUN   TestAccountDeleteStopsGatesBeforeStoreCascade
    nodes_test.go:819: delete acc-01: gates 13000/13001 refused, sessions closed, nodes removed, reconciles=2
--- PASS: TestAccountDeleteStopsGatesBeforeStoreCascade (0.00s)
PASS
ok  	ajsaccountd/internal/runtime	1.019s
$ go test -race -count=1 -v -run 'TestAccountDeleteHook' ./internal/api
=== RUN   TestAccountDeleteHook
=== RUN   TestAccountDeleteHook/hook_error_aborts_the_delete
=== RUN   TestAccountDeleteHook/success_deletes_after_the_hook
=== RUN   TestAccountDeleteHook/nil_hook_keeps_the_store-only_delete
=== RUN   TestAccountDeleteHook/missing_account_skips_the_hook
--- PASS: TestAccountDeleteHook (0.01s)
    --- PASS: TestAccountDeleteHook/hook_error_aborts_the_delete (0.00s)
    --- PASS: TestAccountDeleteHook/success_deletes_after_the_hook (0.00s)
    --- PASS: TestAccountDeleteHook/nil_hook_keeps_the_store-only_delete (0.00s)
    --- PASS: TestAccountDeleteHook/missing_account_skips_the_hook (0.00s)
PASS
ok  	ajsaccountd/internal/api	1.016s
```

`TestAccountDeleteHook` proves the hook runs while the account still exists,
that a hook error answers 500 `INTERNAL` and leaves the account and its nodes in
the store, that a successful hook is followed by the store cascade, that a nil
hook keeps the old behaviour, and that a missing account answers 404 without
calling the hook. No live vendor login, FV authentication or Resin write was
performed; live acceptance remains `[UNVERIFIED]`.

### Logout hook (AJS-76)

`AccountLogoutHook` closes the last B0 lifecycle gap: `POST
/v1/accounts/{id}/actions/logout` runs `NodeController.StopAccount` before
`store.LogoutAccount`, so the account's gates and FV sessions are gone before
the account is paused and its nodes become `DRAINING`. A stop failure answers
500 `INTERNAL` and leaves the account active and every node untouched. The
daemon wires the hook only when `AJS_ACCOUNT_LOGIN=1`, alongside the login,
account-delete and node-drain hooks; the default build keeps the store-only
logout.

```bash
cd D:/Project/LovePlusSpeedToProxy
tar -czf - daemon emulator | ssh hmc \
  'mkdir -p /srv/dwgxhomecloud/scratch/ajs-76-logouthook \
   && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-76-logouthook'
```

On HMC (`go1.25.14 linux/amd64`; `GOTOOLCHAIN=auto` selects the cached
`go1.26.0` toolchain the module requires); no vendor host or Resin instance was
contacted:

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-76-logouthook/daemon
$ go version
go version go1.26.0 linux/amd64
$ go test -race -count=1 ./...
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	1.102s
ok  	ajsaccountd/internal/api	1.192s
ok  	ajsaccountd/internal/gate	1.555s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	1.057s
ok  	ajsaccountd/internal/resin	1.345s
ok  	ajsaccountd/internal/runtime	1.303s
ok  	ajsaccountd/internal/secret	1.025s
ok  	ajsaccountd/internal/store	1.506s
ok  	ajsaccountd/internal/supervisor	7.713s
$ go build ./... && echo BUILD_OK && go vet ./... && echo VET_OK && test -z "$(gofmt -l .)" && echo GOFMT_CLEAN
BUILD_OK
VET_OK
GOFMT_CLEAN
```

Logout + gate evidence (real `gate.NewServer` on `127.0.0.1:13000`, fake
session opener, fake reconciler, real controller behind `api.New`):

```console
$ go test -race -count=1 -v -run 'TestAccountLogoutStopsGatesBeforeStoreDrain' ./internal/runtime
=== RUN   TestAccountLogoutStopsGatesBeforeStoreDrain
    nodes_test.go:1055: logout acc-01: gates 13000/13001 refused, sessions closed, nodes DRAINING, acc-02:t1 kept port 13002, reconciles=3
--- PASS: TestAccountLogoutStopsGatesBeforeStoreDrain (0.00s)
PASS
ok  	ajsaccountd/internal/runtime	1.016s
$ go test -race -count=1 -v -run 'TestAccountLogoutHook|TestAccountLoginLogout' ./internal/api
=== RUN   TestAccountLoginLogout
--- PASS: TestAccountLoginLogout (0.00s)
=== RUN   TestAccountLogoutHook
=== RUN   TestAccountLogoutHook/hook_runs_before_the_store_logout
=== RUN   TestAccountLogoutHook/hook_error_aborts_the_logout
=== RUN   TestAccountLogoutHook/nil_hook_keeps_the_store-only_logout
=== RUN   TestAccountLogoutHook/missing_account_skips_the_hook
--- PASS: TestAccountLogoutHook (0.01s)
    --- PASS: TestAccountLogoutHook/hook_runs_before_the_store_logout (0.00s)
    --- PASS: TestAccountLogoutHook/hook_error_aborts_the_logout (0.00s)
    --- PASS: TestAccountLogoutHook/nil_hook_keeps_the_store-only_logout (0.00s)
    --- PASS: TestAccountLogoutHook/missing_account_skips_the_hook (0.00s)
PASS
ok  	ajsaccountd/internal/api	1.021s
```

`TestAccountLogoutHook` proves the hook runs while the account is still active
and no node is `DRAINING`, that a hook error answers 500 `INTERNAL` and leaves
the account active with every node unchanged, that a nil hook keeps the
store-only logout, and that a missing account answers 404 without calling the
hook. `TestAccountLogoutStopsGatesBeforeStoreDrain` drives the HTTP logout with
`AccountLogoutHook` wired to the real controller: both gates of the logged-out
account refuse new connections, both sessions are closed, the store account is
`paused` with its nodes `DRAINING`, the other account keeps its gate, session
and `LIVE` node, and a second logout answers 200 without closing a gate; the
current handler still publishes once after the store mutation.
The pre-existing `TestAccountLoginLogout` still passes unchanged. No live
vendor logout was performed; live acceptance remains `[UNVERIFIED]`.

### Supervisor hardening (AJS-90)

Both trees were copied to a fresh scratch root, as for AJS-43:

```bash
cd D:/Project/LovePlusSpeedToProxy
tar -czf - daemon emulator | ssh hmc 'mkdir -p /srv/dwgxhomecloud/scratch/ajs-90-suphardening \
  && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-90-suphardening'
```

On HMC (`go1.26.0 linux/amd64`; no vendor host or Resin instance contacted):

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-90-suphardening/daemon
$ go test -race -count=1 ./...
?   	ajsaccountd/cmd/ajs-accountd	[no test files]
ok  	ajsaccountd/internal/account	1.100s
ok  	ajsaccountd/internal/api	1.204s
ok  	ajsaccountd/internal/gate	1.547s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	1.049s
ok  	ajsaccountd/internal/resin	1.348s
ok  	ajsaccountd/internal/runtime	1.301s
ok  	ajsaccountd/internal/secret	1.021s
ok  	ajsaccountd/internal/store	1.495s
ok  	ajsaccountd/internal/supervisor	9.309s
$ go build ./... && echo BUILD_OK && go vet ./... && echo VET_OK && GOOS=windows go build ./... && echo WIN_BUILD_OK && test -z "$(gofmt -l .)" && echo GOFMT_CLEAN
BUILD_OK
VET_OK
WIN_BUILD_OK
GOFMT_CLEAN

$ go test -race -count=1 -v -run "TestStopKillsGrandchild|TestStartSetsWaitDelay" ./internal/supervisor
=== RUN   TestStopKillsGrandchild
    grandchild_unix_test.go:66: child 1989622 and grandchild 1989630 are both dead
--- PASS: TestStopKillsGrandchild (0.55s)
=== RUN   TestStartSetsWaitDelay
--- PASS: TestStartSetsWaitDelay (1.05s)
PASS
ok  	ajsaccountd/internal/supervisor	2.618s
```

`TestStopKillsGrandchild` is the orphan proof: the child (the test binary in
`grandchild` mode) ignores SIGINT and SIGTERM, spawns `sh -c 'echo $$ >
"$1"; trap "" TERM INT; while :; do sleep 5; done'` as a grandchild that
inherits the stderr pipe, and `Stop` with a 500 ms grace must leave both the
child and the grandchild dead (`kill -0` returns ESRCH for both, asserted in a
poll for up to 3 s). A leftover-process scan after the suite
(`ps -eo pid,pgid,cmd | grep -E "supervisor.test|sleep 5"`) found nothing, and
`grep syscall internal/supervisor/supervisor.go` is empty: the portable file
has five `killProcessGroup` call sites but no syscall import. The first HMC run
of this test failed on `strconv.Atoi("1981371\n")` — the pid file written by
`echo $$` carries a trailing newline — and was fixed with
`strings.TrimSpace`; the orphaned pair that run left behind was killed by hand
(`kill -9 -<pgid>`) before the passing runs above.

### Default node limit 1 (AJS-101)

`AJS_NODE_LIMIT` now defaults to `1` instead of `0`. The reason is live
evidence, observed 2026-10-23 on HMC with a single live account (see the
live-run brief's flow logs; the raw redacted log stayed in the HMC
scratch, not in this tree):

- With `AJS_NODE_LIMIT=3` the first two gates answered SOCKS5 `0x05` and the
  daemon logged `fv: data channel rejected ... fm=disconnected by another
  connection` — the vendor disconnects every FV data session after the first
  per account. The third gate (the only active session) worked.
- With `AJS_NODE_LIMIT=1` the single gate's egress matched the original
  Linux app: `222.186.57.63` from both ifconfig.me and ipinfo.io, with flow
  logs `ifconfig.me:80` ingress 77/egress 164 and `ipinfo.io:443` ingress
  1878/egress 6555.

So one account carries exactly one active FV data session, and the safe
default is one node per account. Multi-account scale-out is one node per
account, not one node per vendor server: each additional account adds one
gate on its first usable server. The same live run verified B1 (Linux request
shape), B3 (FV auth response layering) and B4 (data-channel reply/relay
keystream) for one account; B2 signing was not exercised (the profile sends
unsigned requests), and multi-account behaviour plus the Resin wiring stay
`[UNVERIFIED]`. An explicit `AJS_NODE_LIMIT=0` (all servers) remains
available for catalog/probe research and is not live-safe.

`cmd/ajs-accountd/main_test.go` pins the policy with `t.Setenv`: unset and
whitespace-only → `1`, explicit `0`/`1`/`3`/`" 2 "` → the parsed value, and
`-1`/`-3`/`abc`/`1.5`/`1e2`/`0x2`/`1 2`/`two` → the `AJS_NODE_LIMIT` error with no
value returned. No live call was made for this change and no login was
rerun; the evidence above is the 2026-10-23 live run.

### Flow ring (AJS-143)

Both trees were copied to a fresh scratch root (only `daemon/` changed; the
`emulator` sibling satisfies the module's `replace` directive):

```bash
cd D:/Project/LovePlusSpeedToProxy
tar -czf - daemon emulator | ssh hmc 'mkdir -p /srv/dwgxhomecloud/scratch/ajs-143-flowring \
  && tar -xzf - -C /srv/dwgxhomecloud/scratch/ajs-143-flowring'
```

On HMC (`go1.26.0 linux/amd64`; no vendor host or Resin instance contacted):

```console
$ cd /srv/dwgxhomecloud/scratch/ajs-143-flowring/daemon
$ go test -race -count=1 ./...
ok  	ajsaccountd/cmd/ajs-accountd	1.012s
ok  	ajsaccountd/internal/account	1.205s
ok  	ajsaccountd/internal/api	1.290s
ok  	ajsaccountd/internal/gate	1.593s
ok  	ajsaccountd/internal/lablog	1.029s
?   	ajsaccountd/internal/model	[no test files]
ok  	ajsaccountd/internal/publisher	1.072s
ok  	ajsaccountd/internal/resin	1.359s
ok  	ajsaccountd/internal/runtime	1.506s
ok  	ajsaccountd/internal/secret	1.038s
ok  	ajsaccountd/internal/store	1.539s
ok  	ajsaccountd/internal/supervisor	9.321s
$ go build ./... && go vet ./... && test -z "$(gofmt -l .)" && echo HMC_ALL_OK
HMC_ALL_OK

$ go test -run "TestFlowRing" ./internal/gate ./internal/api -v -count=1
=== RUN   TestFlowRingCapacityAndDrop
--- PASS: TestFlowRingCapacityAndDrop (0.00s)
=== RUN   TestFlowRingRecordsRejectedFlow
--- PASS: TestFlowRingRecordsRejectedFlow (0.00s)
=== RUN   TestFlowRingRecordsHandshakeReject
--- PASS: TestFlowRingRecordsHandshakeReject (0.01s)
=== RUN   TestFlowRingRecordsOpenFlowFailure
--- PASS: TestFlowRingRecordsOpenFlowFailure (0.00s)
=== RUN   TestFlowRingRecordsAcceptedFlow
--- PASS: TestFlowRingRecordsAcceptedFlow (0.01s)
PASS
ok  	ajsaccountd/internal/gate	0.019s
=== RUN   TestFlowRingAPIHandler
--- PASS: TestFlowRingAPIHandler (0.00s)
=== RUN   TestFlowRingHTTPServesRealGateRows
--- PASS: TestFlowRingHTTPServesRealGateRows (0.00s)
PASS
ok  	ajsaccountd/internal/api	0.005s
```

`TestFlowRingHTTPServesRealGateRows` is the end-to-end acceptance check: a real
`gate.Server` accepts a SOCKS5 CONNECT to `example.com:443`, relays four bytes
through an in-memory vendor session, records the finished flow, `LabView.Flows`
joins it to the node/account and redacts the SID, and the authenticated
`GET /v1/lab/flows` returns the row with only `targetHost`/`targetPort` (no URL,
header or credential). No live vendor call was made.

## Limitations / next

- State survives a restart through the JSON snapshot, but there is still no
  SQLite: the snapshot rewrites the whole store, a crash loses up to
  `AJS_SNAPSHOT_INTERVAL` of mutations, and the only schema migration is the
  one-way v1 → v2 device-pool conversion.
  Sealed secrets are file-backed only when `AJS_MASTER_KEY` is set.
- Without `AJS_ACCOUNT_LOGIN=1`, login/logout only update timestamps and state.
  With it, login calls the control API, follows it with `loginSync`, and the
  node controller opens one gate per usable server, bounded by
  `AJS_NODE_LIMIT` (default `1` — the vendor's live
  one-active-FV-session-per-account limit, verified 2026-10-23; an explicit
  `0` = all serves catalog/probe research only, and a live account can
  expose 100+ usable servers, ~165 listeners observed). The `LoginResult` and the
  controller's gate map still live in process memory: a restart keeps the
  persisted rows but no gate is bound. Startup recovery now says so instead of
  claiming `LIVE` — every unserved `LIVE`/`DRAINING` node becomes `STALE` with
  its gate port/egress cleared and its leases closed (see “Startup recovery
  (AJS-239)”) — and `AJS_AUTO_LOGIN=1` optionally reopens the sessions with one
  guarded login per account; the node supervisor is not wired. Lease
  rotate/release and node drain/undrain write
  through Resin when a panel is configured; account, user, key and settings
  mutations remain store-only, and `/v1/reconcile` (or, with
  `AJS_RESIN_RECONCILE=1`, the interval reconciler) is the path that
  re-renders the whole subscription.
- Account delete and logout now stop the controller's gates first: `DELETE
  /v1/accounts/{id}` runs `AccountDeleteHook` and `POST
  /v1/accounts/{id}/actions/logout` runs `AccountLogoutHook` (both
  `NodeController.StopAccount`) before the store transition, and each aborts
  with 500 if the stop fails. Logout then pauses the account and marks its
  nodes DRAINING, the handler republishes the panel once after the store
  mutation, and the other accounts keep their gates and sessions.
- The login path is confirmed for **one account**: three logins on 2026-10-23 and 2026-10-24
  accepted the unsigned `linux-4.2.3.0` profile and reached the same node and egress (see
  “Default node limit 1 (AJS-101)” and the root README). B2 signing was never exercised, the
  `linux-4.3.0.1` profile has no live acceptance, and the optional `AJS_CONTROL_CERT_SHA256` pin is
  proven only against an in-process TLS server. Multi-account login and Resin live publish remain
  `[UNVERIFIED]`.
- Rotation restores the platform filters before reading the new lease (the
  publisher owns that sequence). The M1 spike recommends restoring only after
  the new lease is observed; changing that needs a publisher change outside
  this slice.
- `ApplyLeaseRotation` trusts Resin's `egress_ip`/`expiry` strings; there is no
  local validation or retry when `GetLease` answers 404 before Resin creates
  the replacement lease.
- Without `AJS_RESIN_RECONCILE=1` there is no background reconciler: after startup, Resin drift
  (node circuits, an out-of-band subscription edit) is corrected only by `POST /v1/reconcile`.
  With the switch on, the interval reconciler publishes on a changed desired-state hash and
  retries a failed publish with `1m→15m` backoff; it is off by default so a converged daemon makes
  no periodic Resin calls, and it has not been exercised against a live Resin.
- Probe is a bookkeeping canary, not a network probe; `DOWN` nodes return 503.
- The API's drain has no flow scheduler yet (`inUse` is seed data);
  `runtime.NodeController.DrainNode` drains the gate, but the HTTP drain path
  still goes through the panel/store and the controller's drain is not wired to
  it.
- The gate is constructed from a login session and allocates `13000` upward
  through the node controller, but it is not wired to the node supervisor,
  publishes no `State` and serves no `GateController` HTTP surface. Per-user
  authentication is opt-in (`AJS_GATE_AUTH=userpass`: RFC 1929, the user's
  allowlist and monthly quota); the default stays the no-auth loopback method,
  and the gate binds loopback only.
- The supervisor is not wired to the runtime: nothing starts an `ajsproxy`
  child, allocates its `12000–12999` port or reacts to its events. Readiness is
  a TCP dial on `ReadyAddr`, not an FV auth check, and a crashed child is
  reported through `exited` but never restarted; the caller owns retry policy.
  Stop is hardened (process-group kill on Unix, `WaitDelay` bound, Windows
  single-process fallback), but it is still a seam with no production caller,
  and on Windows a grandchild that outlives its parent is not group-killed.
- `/v1/subscription`, `/v1/errors`, `/v1/overview`, `/v1/logs`
  and `/v1/nodes/{id}/flows` are out of this slice.
- Account traffic budgets are enforced at the gate: an over-budget account gets SOCKS5 `0x02` with
  the `quota_exceeded` class before a slot or vendor flow is taken, metered from the current UTC
  month's gate rows. User quotas are enforced only in `AJS_GATE_AUTH=userpass` mode, because the
  no-auth loopback method carries no user identity. On the SOCKS5 listener both are `05 02`
  refusals; the HTTP CONNECT listener answers the same refusals with `429 Too Many Requests`
  (quota) or `403 Forbidden` (disabled/allowlist). The HTTP `QUOTA_EXCEEDED` (429) on `/v1/*` is a
  lease create refusal (`quota_exceeded`/`concurrency_exceeded`/`no_capacity`) or the Lab
  egress-check cooldown, a probe rate limit rather than a traffic quota.
- No request-wide rate limiting, multi-process coordination, or CSRF surface; the rate limits are
  the per-node Lab egress-check cooldown and the per-scoped-key request budget
  (`rateLimitPerMin`, default 60), and the admin bearer token is not rate-limited. TLS is opt-in
  (`AJS_TLS_CERT`/`AJS_TLS_KEY`, see [TLS listener](#tls-listener-ajs-276)): the plaintext
  `AJS_LISTEN` socket keeps working, so by default the admin API is still plain HTTP — enable the
  HTTPS listener, run it on a trusted segment, or put a TLS terminator in front. The Lab endpoints
  inherit the same single
  bearer token, so anyone who can reach `2261` (or the HTTPS address) and holds the token can read
  the whole panel; the HMC deployment keeps bearer auth (no `AJS_ALLOW_INSECURE`) and restricts
  `2261`/`18082` to the `192.168.11.0/24` segment.
- The Lab view reads in-process state; its GETs neither mutate nor probe. The one POST
  (`/v1/lab/egress-check`) dials the node's loopback gate and reads an HTTPS echo — a real
  outbound request, though not a vendor call — and persists nothing. The GET egress panel is a
  vendor claim until an operator runs that check.
- The platform views are read-mostly and offline-tested. Renewal returns the vendor's public price
  snapshot and an official purchase URL; the daemon never posts to a payment endpoint, never calls
  a purchase API and never marks an account renewed — renewal is an operator action at the vendor.
  `GET /v1/usage` aggregates the request log (the same rows `/v1/requests` serves); with gate
  metering on each completed flow appends one row, but it is still recorded rows rather than a
  byte counter, and it resets with the 10 000-row retention — `rowsRetained`/`retentionDays`
  label how much history actually exists. `/v1/health/nodes` probes loopback gate ports only: it
  is a diagnostic for a running gate, not a vendor reachability check, and it keeps one hour of
  samples in memory for at most `AJS_HEALTH_MAX_NODES` nodes (default 16). Webhook
  delivery history persists through the state snapshot (50 rows per endpoint, 500 total, newest
  first), while the live 200-attempt ring and any queued retry stay in memory, and the signing
  secret survives a restart only with `AJS_MASTER_KEY`. Panel backup/restore is offline-tested
  against a temporary state directory: a backup carries sealed credentials and an apply replaces
  the panel files, so the daemon must be restarted afterwards.
- Scoped keys are a least-privilege credential, not an operator model: every key belongs to a
  stored user, its `rateLimitPerMin` budget is a per-key token bucket rather than a quota, and the
  `admin` scope is equivalent to the admin bearer token.
- The dashboard lives in a separate tree (`dashboard/`); the Lab page consumes the endpoints
  above and needs no daemon write path.
- The P1 surfaces are offline-tested only. Catalog selection/connect, account export/import and
  bulk delete, lease creation/revocation and SOCKS5 gate user authentication have `httptest`
  coverage but have never been exercised against the live vendor account; a live switch has not
  been attempted. The scheduler is off unless `AJS_SCHEDULES=1`
  and its only actions are `account.rotate`/`account.reconnect` (the rest validate but fail as
  `unsupported_action`); the dashboard surfaces it now. The device pool is served over
  `/v1/device-pool` as well as through the store API, and is off without `AJS_MASTER_KEY`; the
  dashboard surfaces it on `/device-pool` (hash-only records, typed-token lifecycle).
- Plaintext account export is off by default (`AJS_ALLOW_PLAINTEXT_EXPORT=0`), and even when on it
  writes unencrypted credentials to the operator's disk; the encrypted `.ajsx` envelope is the
  intended path. The account-IO mutex is per process, so two daemons sharing a state file can
  still interleave writes (as with any snapshot write).
