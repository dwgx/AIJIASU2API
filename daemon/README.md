# ajs-accountd (skeleton)

Go daemon with an in-memory panel store and a JSON snapshot that serves the
JSON contract the dashboard's live client expects (standard library plus the
in-repo `ajsproxy` module). The Resin admin client, the publisher, the
startup/topology/reconcile wiring and the runtime mutations are in place: when
`AJS_RESIN_ADMIN_TOKEN` is set the daemon renders the store's `LIVE` nodes into
the `ajs-live` local subscription before it starts listening,
`/v1/topology` / `/v1/reconcile` report the Resin view, and lease
rotate/release plus node drain/undrain write through Resin. Without the token it
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
loopback SOCKS5 gate per usable vendor server (`127.0.0.1:13000` upward), writes
the node, and reconciles Resin. It also makes `POST .../drain|undrain` call the
controller before the store, so a running gate stops admitting (or reopens)
before the node state changes; without the switch the handlers keep the
store/Resin behavior. There is still no SQLite: the JSON snapshot is
the v0.1 stopgap for the schema in `docs/PANEL.md` §6, and the node supervisor
is implemented but not wired to the controller.

- Module: `ajsaccountd` · Go 1.26 toolchain · standard library plus `ajsproxy`
- Listen: `127.0.0.1:2261` by default
- State: versioned JSON snapshot at `$AJS_STATE_FILE` (default
  `$AJS_STATE_DIR/state.json`), loaded on startup and saved every
  `AJS_SNAPSHOT_INTERVAL` and on shutdown; a missing file falls back to the
  dashboard mock seed. Sealed account secrets are inside the snapshot and,
  when `AJS_MASTER_KEY` is set, also in `$AJS_STATE_DIR/secrets.json`
- Wire contract: `dashboard/src/lib/types.ts` field names and
  `dashboard/src/lib/api.ts` paths/methods

## Layout

```
go.mod
cmd/ajs-accountd/main.go     flags/env, listener, graceful shutdown
internal/model/model.go      wire structs, inputs and patches
internal/store/              mutex-protected state + deterministic seed
internal/store/seed.go       mock-compatible seed data and metrics series
internal/store/persist.go    versioned JSON snapshot: Save/Load, atomic 0600
internal/store/device.go     stable per-account device identity (snapshot-backed)
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
internal/gate/                per-node SOCKS5 gate (admission, byte counters, drain)
internal/supervisor/          per-node child process: start, readiness, events, stop
internal/api/server.go       router, auth, CORS, logging, JSON helpers
internal/api/*.go            account, node/lease, user/key and ops handlers
internal/api/server_test.go  httptest coverage
```

## Build, test, run

```bash
cd daemon
go test ./...
go test -race ./...
go build ./...
go vet ./...
AJS_LISTEN=127.0.0.1:2261 AJS_ADMIN_TOKEN=local-test-token go run ./cmd/ajs-accountd
```

```bash
curl -s http://127.0.0.1:2261/healthz
curl -s -H 'Authorization: Bearer local-test-token' http://127.0.0.1:2261/v1/accounts
```

`local-test-token` is a local placeholder, not a credential. Without a token the
process refuses to start unless `AJS_ALLOW_INSECURE=1` is set.

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
  it (`AJS_ACCOUNT_PROFILE` accepts only `linux-4.2.3.0`); it exists so the
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

`internal/gate` is the per-node SOCKS5 front end Resin dials (`docs/PANEL.md`
§1–§2). One `Server` owns one loopback listener for one node and injects the FV
session through a one-method interface, so the gate knows nothing about the
vendor protocol, credentials or the control API.

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
- `Config`: `NodeID`, `AccountID`, `Listen`, `Limit`, `AdmissionTimeout`,
  `DrainGrace`, `Now`, `Logger`. Zero values select the defaults
  (`127.0.0.1:0`, 32, 30s, 30s, `time.Now`, `slog.Default()`).
- `State`: `NodeID`, `AccountID`, `Active`, `Limit`, `Draining`, `Accepted`,
  `Rejected`, `IngressBytes`, `EgressBytes`, `StartedAt`. `Accepted` counts
  flows that received the success reply and entered relay; `Rejected` counts
  flows refused before relay (draining, admission timeout, malformed
  handshake, failed `OpenFlow`). Byte counters are updated live and exclude
  the handshake.

Wire contract (RFC 1928, no auth, CONNECT only):

| Step | Bytes | Gate behaviour |
|---|---|---|
| Greeting | `05 NMETHODS methods…` | requires method `00`; otherwise replies `05 FF` and closes. A wrong version closes without a reply. |
| Request | `05 01 00 ATYP addr port` | only CONNECT; `ATYP 01` IPv4, `03` domain (1-byte length), `04` IPv6; RSV must be 0. |
| Success | `05 00 00 01 00 00 00 00 00 00` | after `OpenFlow` succeeded; relay starts. |
| Refused | `05 05 00 01 …` | draining, closed, or `OpenFlow` error. |
| TTL expired | `05 06 00 01 …` | the admission semaphore stayed full for `AdmissionTimeout`. |
| Command unsupported | `05 07 00 01 …` | `CMD != 1`. |
| Address unsupported | `05 08 00 01 …` | `ATYP` not 1/3/4. |
| Malformed | `05 01 00 01 …` | RSV non-zero or an empty domain name. |

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
  (`node_id`, `target`, `ingress_bytes`, `egress_bytes`, `duration`) and warn
  lines for rejections. The gate never receives a credential; only host:port is
  logged.
- Limitation: the package is now constructed from a login session by the node
  controller (`internal/runtime/nodes.go`, B0), which allocates the port and
  reconciles Resin. It is still not wired to the node supervisor, publishes no
  `State` and serves no `GateController` HTTP surface; it binds loopback only
  and has no per-user auth (P3).

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
    Logger:     logger,
})
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
2. keep only servers whose `sts` is `ok`; the node id is
   `<accountID>:<serverID>` and the label (also the Resin tag) is
   `<cc>-<account>-<server>` in the lowercase tag alphabet;
3. reuse the gate of a server that already has one, so a repeated login never
   re-authenticates or re-binds;
4. otherwise open one FV session (`account.Factory`) and start one SOCKS5 gate
   on `127.0.0.1:<port>`, allocating from 13000 upward and skipping the ports
   of running gates and store nodes; a bind failure retries the next port;
5. write the node `LIVE` with `gatePort`, `serverId`, protocol `socks5` and the
   session's egress address (`sip`, then `cip`, then `ip`); a per-server
   failure writes it `DOWN` with no gate port and the batch continues;
6. reconcile Resin once (skipped without a panel).

A server that disappears from the login has its gate and session closed and its
node deleted. `DrainNode`/`UndrainNode` drive the gate and the store together
and reconcile the panel, and a login refresh does not undo an operator drain.
`Stop` closes every gate and session and reconciles once. Every mutation is
serialized, so concurrent logins for one account share one gate.

`StopAccount` is the account-delete and account-logout seam. It closes every
gate and session the controller runs for one account, removes them from the
running set, leaves the other accounts untouched and reconciles the panel once.
It deliberately does not touch store nodes: `store.DeleteAccount` owns the
cascade and `store.LogoutAccount` owns the pause/DRAINING transition, and the
gates must be gone before either runs. It is idempotent — with no running gate
for the account it returns `nil` without reconciling — and it aggregates close
and reconcile failures; a failed close still drops the gate from the running
set, so a retry cannot double-close it.

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
without calling the hook. All four hooks are nil unless `AJS_ACCOUNT_LOGIN=1`
wired the controller, so the default build keeps the store-only behaviour.

- Port range: `127.0.0.1:13000` upward (`runtime.DefaultGatePortBase`),
  skipping ports already used by a running gate or a store node. Each gate
  admits `32` concurrent flows, mirrored onto the node's `capacity`.
- Store: `ProxyNode.ServerID` (`serverId`, `omitempty`) is additive, so
  `SnapshotVersion` stays 1 and older documents restore with an empty field.
  `Store.PutNode` upserts a runtime node (validating id, account, tag label,
  region, state and port) and preserves the probe/lease fields `InUse`,
  `SuccessRate` and `LastProbeAt`; `Store.DeleteNode` removes the node and every
  lease that pointed at it.
- Live status: `[UNVERIFIED]`. No vendor host was contacted and no live Resin
  subscription was changed. The tests use a fake session opener, a real
  `gate.NewServer` on `127.0.0.1:13000` and a fake reconciler; the first live
  login still depends on B1/B2 in `research/81-protocol-gaps/REPORT.md`.

`internal/runtime/nodes_test.go` covers: sync creating two gates on 13000/13001
with the session's egress IP, a SOCKS5 CONNECT relayed through the fake session,
a second sync reusing both ports without new sessions, server removal closing
its gate and deleting its node, drain rejecting a flow with `0x05` and undrain
accepting again, an operator drain surviving a refresh, stop closing every gate
and session, stop-account closing only one account's gates while the other
account keeps serving, reconciling once and staying idempotent (a repeat and an
unknown account do not reconcile), the aggregated close+reconcile error with a
failed close still dropping the gate, partial failure leaving the working node
LIVE and the failed node DOWN, no-node-started returning an aggregate error that
names both nodes, no usable server, missing login/device preconditions, four
concurrent syncs sharing one gate, an injected gate factory, an end-to-end
`DELETE /v1/accounts/{id}` through `api.New` with the real controller that
proves the gates are refused and the store cascade then removes the nodes, an
end-to-end `POST /v1/accounts/{id}/actions/logout` through `api.New` with
`AccountLogoutHook` wired to the real controller: the logged-out account's
gates are refused, its sessions are closed and its store nodes are DRAINING,
the other account keeps its gate, session and LIVE node, and a second logout
reconciles nothing, and
an end-to-end `POST /v1/nodes/{id}/actions/drain|undrain` through `api.New`
with `NodeDrainHook` wired to the real controller: drain answers 0x05 on a new
SOCKS5 CONNECT, undrain answers 0x00, and a stopped controller makes the drain
answer 500 while the node stays LIVE.
`TestAccountLoginHook`, `TestAccountDeleteHook`, `TestAccountLogoutHook` and
`TestNodeDrainHook` pin the API seams; the last one proves hook-before-store,
hook-before-panel, abort-on-error, the missing-node 404 without a hook call,
and the nil-hook store-only path, and `TestAccountLogoutHook` proves the same
hook-before-store and abort-on-error policy for logout.

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
| `AJS_ADMIN_TOKEN` | `-token` | — | required bearer token |
| `AJS_ALLOW_INSECURE` | `-insecure` | `0` | `1` disables auth (loopback only) |
| `AJS_CORS_ORIGINS` | `-cors-origins` | `http://127.0.0.1:18081,http://127.0.0.1:5173` | comma-separated exact origins |
| `AJS_LOG_LEVEL` | `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `AJS_ACCOUNT_LOGIN` | — | `0` | `1` enables the vendor login path and the node controller (login → `loginSync` → one loopback gate per usable server); anything else logs `account_login_disabled` and keeps the store-only refresh |
| `AJS_API_HOSTS` | — | `https://login.ajs-app.com,https://login.mmegg.com,https://login.mmegg.com:4443,https://login.mbclubsh.com` | comma-separated control API base URLs, tried in order |
| `AJS_ACCOUNT_PROFILE` | — | `linux-4.2.3.0` | version profile; only the tested Linux 4.2.3.0 profile is accepted, and an unknown value fails startup when login is enabled |
| `AJS_CONTROL_CERT_SHA256` | — | — | SHA-256 fingerprint (64 hex chars, `AA:BB:…` or raw) of the control API leaf certificate; set → the login transport pins exactly that certificate instead of chain/hostname verification (see “Control certificate pin”), empty → default verification. Malformed → startup fails |
| `AJS_RESIN_URL` | `-resin-url` | `http://127.0.0.1:2260` | Resin admin API base |
| `AJS_RESIN_ADMIN_TOKEN` | `-resin-token` | — | set → enable the Resin wiring; empty → `resin_disabled` |
| `AJS_SUBSCRIPTION_NAME` | `-subscription-name` | `ajs-live` | Resin local subscription name |
| `AJS_PLATFORM_NAME` | `-platform-name` | subscription name | Resin platform name |
| `AJS_MASTER_KEY` | — | — | 32-byte secret-encryption key (64 hex characters or base64); set → seal account passwords, empty → `secrets_disabled` |
| `AJS_STATE_DIR` | — | `./data` | state directory; holds `secrets.json` and the default `state.json` (created `0700`) |
| `AJS_STATE_FILE` | — | `$AJS_STATE_DIR/state.json` | full-state JSON snapshot; loaded before serving and rewritten on every snapshot |
| `AJS_SNAPSHOT_INTERVAL` | — | `30s` | Go duration between background snapshots; invalid or non-positive fails startup |

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
- File: `$AJS_STATE_DIR/secrets.json`, mode `0600`, rewritten on every seal
  through a `0600` temporary file and an atomic rename. A missing file starts an
  empty store.
- Rotation: `secret.Store.Rotate(newKey)` re-seals every entry under the new key
  and rewrites the file; the old key stays active if re-sealing or the write
  fails. There is no HTTP endpoint for rotation.
- Access: `store.AccountSecret(id)` opens a password for the account client. It
  is not exposed over HTTP and `model.Account` has no credential field.
- Limitation: the sealed blobs are duplicated in `secrets.json` (the sealer's
  own file) and in the full snapshot. The snapshot is authoritative on load;
  see Persistence below. SQLite (PANEL §6) is a later slice.

## Persistence (AJS-45)

The panel store is durable through a whole-store JSON snapshot. This is a
deliberate v0.1 stopgap: `docs/PANEL.md` §6 describes the target SQLite schema,
but this slice keeps the in-memory store and serialises it as one document.

```
$AJS_STATE_FILE (default $AJS_STATE_DIR/state.json)
  version          1
  accounts         []Account        nodes      []ProxyNode
  leases           []Lease          users      []PanelUser
  keys             []APIKey         requests   []RequestLog
  audit            []AuditEvent     settings   PanelSettings
  keyHashes        {keyId: sha256}  secrets    {accountId: base64(sealed)}
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
  `os.ErrNotExist`, so `cmd/ajs-accountd` keeps the dashboard seed. Corrupt
  JSON, empty data, an unknown `version` or duplicate ids are errors. `Restore`
  decodes and validates into a local `Snapshot` before taking the write lock,
  so a rejected file leaves the running store untouched.
- `Restore` rebuilds the denormalised node counts and raises every counter to at
  least the highest identifier already present (`acc-`, `u-`, `key-`,
  `req-mut-`, `audit-`). A document with stale counters therefore cannot hand
  out a colliding id after a create/delete cycle.
- `cmd/ajs-accountd` loads the snapshot before building the Resin panel and
  before the listener starts. It then saves every `AJS_SNAPSHOT_INTERVAL`
  (default `30s`) and once more when `run` returns after a SIGINT/SIGTERM
  shutdown. Logs are `state_loaded`, `state_missing`, `state_saved` and
  `state_save_failed`; they carry the path and reason only, never a secret.
- Duplication with `secrets.json`: the sealer still writes its own file on every
  seal, so a sealed envelope can exist in both files. On startup the snapshot
  replaces `store.secrets`, making it authoritative for which accounts have a
  secret. A snapshot restored under a different `AJS_MASTER_KEY` keeps the
  envelopes but `AccountSecret` cannot open them; the key is never stored.
  Rotation is not wired to the panel store: `secret.Store.Rotate` re-seals only
  its own file, so the snapshot's map keeps old-key blobs and would need the
  same entries re-sealed before they can be opened again.
- Limitations: the format is JSON rather than SQLite; every save rewrites the
  whole document, so cost grows with request/audit history; a crash can lose up
  to one snapshot interval of mutations; there is no schema migration beyond
  rejecting unknown versions; and there is no multi-process writer lock. Keep
  one daemon per state file.

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

All `/v1/*` routes and `GET /metrics` require `Authorization: Bearer
$AJS_ADMIN_TOKEN`. `/healthz` is unauthenticated. List endpoints return bare
JSON arrays, matching `api.ts` (`Account[]`, `ProxyNode[]`, …), not wrapper
objects.

| Method | Path | Behavior |
|---|---|---|
| GET | `/healthz` | process status + account-state counts |
| GET | `/metrics` | Prometheus text format: aggregate account/node/lease/user/key/request counts, build info, uptime; every state including zeros, no ids/labels/IPs/secrets |
| GET | `/v1/metrics?range=24h\|7d` | deterministic series, totals, account health |
| GET | `/v1/accounts` | list; filters `state`, `limit`, `offset` |
| POST | `/v1/accounts` | create; duplicate label → 409; password sealed when `AJS_MASTER_KEY` is set, otherwise discarded |
| GET | `/v1/accounts/{id}` | single account |
| PATCH | `/v1/accounts/{id}` | partial label/username/platform/state/concurrency/regions/expiresAt |
| DELETE | `/v1/accounts/{id}` | with `AJS_ACCOUNT_LOGIN=1`: stop the account's gates and sessions first (stop failure → 500, nothing deleted); then remove account, its nodes, its leases and its sealed secret; keep audit |
| POST | `/v1/accounts/{id}/actions/login` | with `AJS_ACCOUNT_LOGIN=1`: vendor login + `loginSync`, then `active`/`expired`, a retained `LoginResult` and one gate per usable server; otherwise refresh `lastLoginAt` and clear `auth_failed` |
| POST | `/v1/accounts/{id}/actions/logout` | with `AJS_ACCOUNT_LOGIN=1`: controller closes the account's gates and sessions first (stop failure → 500, account active and nodes unchanged); then pause the account and mark its nodes `DRAINING` |
| GET | `/v1/nodes` | list; filters `account_id`, `state`, `region` |
| POST | `/v1/nodes/{id}/actions/drain` | with `AJS_ACCOUNT_LOGIN=1`: controller closes the gate first (hook failure → 500, node unchanged); → `DRAINING`; unpublishes through the panel when configured; DOWN → 409 |
| POST | `/v1/nodes/{id}/actions/undrain` | with `AJS_ACCOUNT_LOGIN=1`: controller reopens the gate first (hook failure → 500, node unchanged); → `LIVE` (DOWN → `READY`); republishes when configured |
| POST | `/v1/nodes/{id}/actions/probe` | refresh `lastProbeAt`; DOWN → 503 |
| GET | `/v1/leases` | list; filter `user_id` |
| POST | `/v1/leases/{id}/actions/rotate` | panel: filter exclusion + Resin lease read; fallback: next egress IP, expiry refresh, requests +17 |
| DELETE | `/v1/leases/{id}` | panel: delete Resin lease, then store row; fallback: store release; decrement node `inUse` |
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
| GET | `/v1/requests` | request log, newest first |
| POST | `/v1/requests/mutations` | append a `mutation-lab` row + audit event |
| GET | `/v1/audit?limit=` | newest-first audit, default 20, max 500 |
| GET | `/v1/settings` | runtime settings |
| PATCH | `/v1/settings` | partial; unknown keys → 400 |
| GET | `/v1/topology` | store `ajs-live` map when Resin is disabled; live panel view when enabled |
| POST | `/v1/reconcile` | re-render + PATCH + refresh; 503 without a panel |

Errors are always `{"error":{"code":"…","message":"…"}}`:

| Code | HTTP | Used for |
|---|---|---|
| `INVALID_ARGUMENT` | 400 (413 oversize) | malformed JSON, bad enum/range, unknown settings key |
| `NOT_FOUND` | 404 | unknown id or route |
| `CONFLICT` | 409 | duplicate label/name, draining a DOWN node |
| `UPSTREAM_UNAVAILABLE` | 503 | probing a DOWN node; reconcile/topology without or before a Resin panel |
| `QUOTA_EXCEEDED` | 429 | reserved; quotas are advisory in v1 |
| `UNAUTHORIZED` | 401 | missing/wrong bearer token |
| `METHOD_NOT_ALLOWED` | 405 | path exists, method does not |
| `INTERNAL` | 500 | unexpected store failure or node drain hook failure (details stay in the log) |

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

- Timestamps are RFC 3339 (nanosecond precision), byte counts are integers.
- `quotaBytesMonth`, `lastUsedAt`, `revokedAt` are `null` when unset.
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

## Seed

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

`internal/api/server_test.go` uses `httptest` only and covers: 401 vs `/healthz`
200, seed counts and exact field names, account create/409/delete, node
drain/undrain/probe, lease rotate/release, user suspend/resume/allowlist and
cascade delete, API-key show-once/revoke plus stored-hash checks, mutation log
append, settings roundtrip, CORS preflight, 404/405, pagination, metrics,
topology, the 1 MiB body limit, and token redaction in request logs. The
Prometheus endpoint has its own coverage: bearer-auth 401 vs 200 with the text
format content type, seeded counts compared against `store.Accounts`, `Nodes`,
`Leases`, `Users`, `Keys` and `Requests`, zero states present, no id/label/IP
or secret in the body, and `AllowInsecure` passthrough.

`internal/secret/store_test.go` covers AES-256-GCM roundtrip, account-id AAD
mismatch, tampered ciphertext/nonce/version, wrong key, rotation and reopen
under the new key, the 0600 file mode with no temp-file leftovers, a missing
file starting empty, key-length rejection, and that the JSON file holds only
base64 envelopes (no plaintext). `internal/store/secret_test.go` covers
seal-on-create, `AccountSecret` per account, delete, the nil-sealer discard
path, seeded accounts without a secret, no credential in the account JSON, and
a failing sealer leaving the store unchanged. `internal/store/persist_test.go`
covers a full roundtrip of every collection, settings, key hashes, sealed
secrets and counters (including `AccountSecret` after restore with the same
sealer), the `0600` file mode (asserted on non-Windows) with no temp-file
leftovers, an atomic overwrite followed by a reload, a restore that replaces
every seeded collection, a missing file returning `os.ErrNotExist` without
mutating the store, corrupt JSON and unknown/empty versions leaving the store
unchanged, no plaintext password or `password` field in the document, and
create → delete → save/load plus a lost-counter restore that must not reuse an
account, user, key, mutation or audit id.

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
handler are awaited, so a leaked goroutine fails the test.

`internal/supervisor/supervisor_test.go` re-executes the test binary as the
managed child and binds ephemeral loopback ports only. It covers start →
`started`/`ready` → stop → `exited` with `PID`/`Get`/`List` and port-closed
checks, environment-value redaction in logs, a missing binary, invalid specs,
a duplicate node id, readiness timeout (`ErrNotReady` and `*ReadyError`), a
child exiting before ready, stderr events plus the bounded ring, unknown
`Stop`/`Signal`, `Signal` on a running node, and `Close` stopping two nodes,
idempotency and `Start` after close.

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
`AJS_API_HOSTS` the daemon still starts and logs the enabled state, and without
the switch it logs the disabled state:

```console
$ AJS_ALLOW_INSECURE=1 AJS_ACCOUNT_LOGIN=1 AJS_API_HOSTS=http://127.0.0.1:1 \
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
or vendor call is involved:

```console
$ AJS_LISTEN=127.0.0.1:12261 AJS_ADMIN_TOKEN=local-test-token \
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
    -H 'Authorization: Bearer local-test-token' http://127.0.0.1:12261/v1/topology
GET /v1/topology HTTP 200
$ python -c "import json;d=json.load(open('top.json'));print('subscription_id=%s platform_id=%s nodes=%d content_lines=%d last_good_lines=%d resin_version=%s' % (d['subscription_id'],d['platform_id'],len(d['nodes']),d['content'].count(chr(10)),d['last_good'].count(chr(10)),d['resin_version']))"
subscription_id=sub-smoke platform_id=plat-smoke nodes=8 content_lines=8 last_good_lines=8 resin_version=v1.3.0

$ curl -s -o rec.json -w 'POST /v1/reconcile HTTP %{http_code}\n' -X POST \
    -H 'Authorization: Bearer local-test-token' http://127.0.0.1:12261/v1/reconcile
POST /v1/reconcile HTTP 200
$ python -c "import json;d=json.load(open('rec.json'));print('reconcile nodes=%d same_content=%s' % (len(d['nodes']), d['content']==json.load(open('top.json'))['content']))"
reconcile nodes=8 same_content=True
```

Without the token the same binary logs `resin_disabled` and keeps the store
view:

```console
time=2026-10-23T12:27:09.483+09:00 level=INFO msg=resin_disabled reason="AJS_RESIN_ADMIN_TOKEN is not set"
$ curl -s -o top-off.json -w 'topology HTTP %{http_code}\n' \
    -H 'Authorization: Bearer local-test-token' http://127.0.0.1:12261/v1/topology
topology HTTP 200
$ python -c "import json;d=json.load(open('top-off.json'));print('subscription=',d['subscription'],'nodes=',len(d['nodes']),'platforms=',len(d['platforms']))"
subscription= ajs-live nodes= 12 platforms= 4
$ curl -s -w '\nreconcile HTTP %{http_code}\n' -X POST \
    -H 'Authorization: Bearer local-test-token' http://127.0.0.1:12261/v1/reconcile
{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"resin is not configured; set AJS_RESIN_ADMIN_TOKEN"}}
reconcile HTTP 503
```

With a token but an unreachable Resin, startup degrades instead of exiting:

```console
time=2026-10-23T12:27:18.547+09:00 level=WARN msg=resin_start_failed error="runtime: ensure probe config: publisher: set max_latency_test_interval: resin: PATCH /api/v1/system/config: Patch \"http://127.0.0.1:12262/api/v1/system/config\": dial tcp 127.0.0.1:12262: connectex: No connection could be made because the target machine actively refused it."
$ curl -s -w '\ntopology HTTP %{http_code}\n' \
    -H 'Authorization: Bearer local-test-token' http://127.0.0.1:12261/v1/topology
{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"resin topology unavailable"}}
topology HTTP 503
$ curl -s -w '\nreconcile HTTP %{http_code}\n' -X POST \
    -H 'Authorization: Bearer local-test-token' http://127.0.0.1:12261/v1/reconcile
{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"resin reconcile failed"}}
reconcile HTTP 503
```

Smoke run on `127.0.0.1:2261` with the placeholder token:

```console
$ AJS_LISTEN=127.0.0.1:2261 AJS_ADMIN_TOKEN=local-test-token go run ./cmd/ajs-accountd &
$ curl -s -o /tmp/ajs-health.json -w 'HTTP %{http_code}\n' http://127.0.0.1:2261/healthz
HTTP 200
$ cat /tmp/ajs-health.json
{"status":"ok","service":"ajs-accountd","version":"0.1.0","time":"2026-10-23T03:05:57.153798762Z","accounts":{"active":4,"auth_failed":1,"expired":0,"paused":1}}

$ curl -s -o /tmp/ajs-accounts.json -w 'HTTP %{http_code}\n' \
    -H 'Authorization: Bearer local-test-token' http://127.0.0.1:2261/v1/accounts
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
and `LIVE` node, and a second logout answers 200 without reconciling again.
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

## Limitations / next

- State survives a restart through the JSON snapshot, but there is still no
  SQLite: the snapshot rewrites the whole store, a crash loses up to
  `AJS_SNAPSHOT_INTERVAL` of mutations, and there is no schema migration path.
  Sealed secrets are file-backed only when `AJS_MASTER_KEY` is set.
- Without `AJS_ACCOUNT_LOGIN=1`, login/logout only update timestamps and state.
  With it, login calls the control API, follows it with `loginSync`, and the
  node controller opens one gate per usable server. The `LoginResult` and the
  controller's gate map still live in process memory: a restart keeps the
  persisted nodes but no gate is bound until the next login, and the node
  supervisor is not wired. Lease rotate/release and node drain/undrain write
  through Resin when a panel is configured; account, user, key and settings
  mutations remain store-only, and `/v1/reconcile` is the only path that
  re-renders the whole subscription on demand.
- Account delete and logout now stop the controller's gates first: `DELETE
  /v1/accounts/{id}` runs `AccountDeleteHook` and `POST
  /v1/accounts/{id}/actions/logout` runs `AccountLogoutHook` (both
  `NodeController.StopAccount`) before the store transition, and each aborts
  with 500 if the stop fails. Logout then pauses the account and marks its
  nodes DRAINING, while the other accounts keep their gates and sessions.
- The login path has never contacted a live vendor host. The default
  `linux-4.2.3.0` profile sends unsigned requests because its signing secret is
  `[UNKNOWN]`, so whether the vendor accepts it remains `[UNVERIFIED]`. The
  optional `AJS_CONTROL_CERT_SHA256` pin is proven only against an in-process
  TLS server; matching the live control certificate stays `[UNVERIFIED]`
  until the actual login is rerun.
- Rotation restores the platform filters before reading the new lease (the
  publisher owns that sequence). The M1 spike recommends restoring only after
  the new lease is observed; changing that needs a publisher change outside
  this slice.
- `ApplyLeaseRotation` trusts Resin's `egress_ip`/`expiry` strings; there is no
  local validation or retry when `GetLease` answers 404 before Resin creates
  the replacement lease.
- There is no background reconciler: after startup, Resin drift (node circuits,
  an out-of-band subscription edit) is corrected only by `POST /v1/reconcile`.
- Probe is a bookkeeping canary, not a network probe; `DOWN` nodes return 503.
- The API's drain has no flow scheduler yet (`inUse` is seed data);
  `runtime.NodeController.DrainNode` drains the gate, but the HTTP drain path
  still goes through the panel/store and the controller's drain is not wired to
  it.
- The gate is constructed from a login session and allocates `13000` upward
  through the node controller, but it is not wired to the node supervisor,
  publishes no `State` and serves no `GateController` HTTP surface. It has no
  per-user auth or quota (P3) and binds loopback only.
- The supervisor is not wired to the runtime: nothing starts an `ajsproxy`
  child, allocates its `12000–12999` port or reacts to its events. Readiness is
  a TCP dial on `ReadyAddr`, not an FV auth check, and a crashed child is
  reported through `exited` but never restarted; the caller owns retry policy.
  Stop is hardened (process-group kill on Unix, `WaitDelay` bound, Windows
  single-process fallback), but it is still a seam with no production caller,
  and on Windows a grandchild that outlives its parent is not group-killed.
- Lease creation, `/v1/subscription`, `/v1/errors`, `/v1/overview`, `/v1/logs`
  and `/v1/nodes/{id}/flows` are out of this slice.
- Quotas are advisory; `QUOTA_EXCEEDED` is defined but never emitted.
- No TLS, rate limiting, multi-process coordination, or CSRF surface; keep it
  loopback-only.
- The dashboard was not modified; enabling live mode is a separate task.
