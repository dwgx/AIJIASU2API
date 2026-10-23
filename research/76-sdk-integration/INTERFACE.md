# INTERFACE — SDK / protocol integration layer

Status: design, no implementation · Date: 2026-10-23 · Slice: AJS-16
Companion: `REPORT.md` (evidence, protocol surface, failure taxonomy).

This file is the build sheet: Go interfaces, HTTP endpoints with schemas, the Resin
subscription line, and the three state machines. It is normative for names; it is not code.
Signatures are written as they should compile; bodies are deliberately absent.

## Layering

```
cmd/ajs-accountd ─┬─ internal/account   (AccountClient, SessionFactory, FlowSession)
                  ├─ internal/panel     (Publisher, LeaseRotator, Prober, GateController)
                  ├─ internal/runtime   (NodeSupervisor, ConcurrencyGate, SecretStore)
                  ├─ internal/obs       (Observer, Classifier)
                  └─ internal/gate      (SOCKS5 + admission + control HTTP)
                          │
                          └─ reuses ajsproxy/internal/fv  (Transport, Session, crypto)
                             and ajsproxy/internal/proxy (SOCKS5 front end, HTTP proxy)
```

Dependency rule: `internal/account` never imports `internal/panel`; the daemon wires them. The
gate imports only `internal/runtime` (gate types) and `net`. Nothing outside `internal/account`
knows a vendor field name.

## Go interfaces

### Accounts and sessions

```go
// AccountClient is the control-API surface (P1). One instance per account; safe for
// concurrent use. Implementations must not log the request body.
type AccountClient interface {
	Login(ctx context.Context, req LoginRequest) (LoginResult, error)
	LoginSync(ctx context.Context, tok SessionToken, dev DeviceIdentity) (LoginResult, error)
	Servers(ctx context.Context, tok SessionToken) (ServerSet, error)
}

// SessionFactory opens FV sessions for one VersionProfile (P2 or P3). The profile is
// fixed at construction: switching profiles means constructing a new factory.
type SessionFactory interface {
	Profile() VersionProfile
	Open(ctx context.Context, req SessionRequest) (FlowSession, error)
	Close() error
}

// FlowSession is one authenticated FV session and its data channels (P4).
// *fv.Session satisfies it through a thin adapter: SID() -> Session.SID,
// Fields() -> Session.Fields, OpenFlow() -> Session.OpenStreamContext.
type FlowSession interface {
	SID() string
	Fields() map[string]string
	OpenFlow(ctx context.Context, target Target) (net.Conn, error)
	Healthy() bool
	Close() error
}

// Keepalive is the seam for P6. The only implementation in P1 is PassiveKeepalive,
// which reports liveness from socket activity and does nothing on the wire.
type Keepalive interface {
	Start(ctx context.Context, s FlowSession) error
	Alive() bool
	Stop()
}
```

### Panel and runtime

```go
// ConcurrencyGate admits flows against per-account, per-node and global ceilings.
// Acquire blocks until a slot is free or ctx expires; the returned Admission must be
// released exactly once.
type ConcurrencyGate interface {
	Acquire(ctx context.Context, accountID, nodeID string) (Admission, error)
	InUse(accountID string) int
	Limit(accountID string) int
	Adjust(accountID string, delta int) int
}

// NodeSupervisor owns the ajsproxy process tree: one process per (account, server).
type NodeSupervisor interface {
	Start(ctx context.Context, spec NodeSpec) (NodeHandle, error)
	Stop(ctx context.Context, nodeID string, grace time.Duration) error
	Signal(nodeID string, sig os.Signal) error
	Events() <-chan NodeEvent
}

// Publisher renders and publishes the Resin local subscription.
type Publisher interface {
	Render(nodes []NodeView) (Rendered, error)
	Publish(ctx context.Context, r Rendered) error
	LastGood() (Rendered, bool)
}

// Prober runs the node canary through the gate (P4 end-to-end).
type Prober interface {
	Check(ctx context.Context, nodeID string, target Target) (ProbeResult, error)
	Schedule(nodeID string, every time.Duration)
}

// LeaseRotator wraps Resin's platform and lease API.
type LeaseRotator interface {
	EnsurePlatform(ctx context.Context, spec PlatformSpec) (PlatformID, error)
	Lease(ctx context.Context, p PlatformID, account string) (LeaseState, error)
	Rotate(ctx context.Context, p PlatformID, account string, avoidCurrentIP bool) error
	Suspend(ctx context.Context, p PlatformID, on bool) error
}

// GateController is the accountd-side client for a gate's control port.
type GateController interface {
	State(ctx context.Context, nodeID string) (GateState, error)
	Drain(ctx context.Context, nodeID string, grace time.Duration) error
	Undrain(ctx context.Context, nodeID string) error
}
```

### Platform services

```go
// SecretStore seals credentials at rest. The account id is the AAD, so a sealed blob
// cannot be moved between rows.
type SecretStore interface {
	Seal(accountID string, plaintext []byte) ([]byte, error)
	Open(accountID string, sealed []byte) ([]byte, error)
	Rotate(newKey []byte) error
}

// Observer is the single sink for metrics, structured logs and audit events.
// Implementations must apply the redaction list before serialising.
type Observer interface {
	Count(name string, labels Labels, delta float64)
	Observe(name string, labels Labels, v float64)
	Event(ev Event)
}

// Classifier maps raw failures onto the taxonomy in REPORT.md. It is the only place
// that decides retryability and the local SOCKS5 reply code.
type Classifier interface {
	Classify(err error) Failure
	Retryable(f Failure) bool
	ReplyCode(f Failure) byte
}
```

Thirteen interfaces. `fv.Transport` and `proxy.StreamOpener` from `emulator/` are reused as-is;
neither is redefined here.

## Types

```go
type LoginRequest struct {
	Username string
	Password Secret // redacting; never formatted with %v
	Device   DeviceIdentity
	Trigger  string // "login"
}

type LoginResult struct {
	AuthResult       string // "OK"
	AuthMessage      string
	Token            SessionToken
	UserName         string
	ConnectPassword  Secret
	MembershipStatus string
	Expired          bool
	ExpireTime       OpaqueTime // see REPORT.md open question 4
	Servers          ServerSet
	Raw              json.RawMessage // retained for parse-failure forensics, redacted on log
}

type SessionToken struct {
	Value  string    // LoginToken; never logged
	Issued time.Time // drives the 10-minute LoginSync refresh
}

type DeviceIdentity struct {
	UID   string // 22 chars, self-checksummed
	OSDID string // 16 hex
	CUID  string // FV cuid/cseid, may be 20 hex
	Model string
	OSVer string
	HW    string
	Lang  string
	TZMin int
	Dim   string
}

type ServerSet struct {
	ByID    map[string]Server
	Fetched time.Time
}

type Server struct {
	ID     string
	Host   string // svr
	Port   int    // par.fv.p
	Status string // sts
	Title  string // tit
	Region string // derived from ctry + tit
}

type VersionProfile struct {
	Name            string // "linux-4.2.3.0-binary"
	ClientVersion   string // _v
	CoreVersion     string // cv
	ProtocolVersion int    // 1 legacy text, 2 binary carrier
	ImplType        int    // 1 tun, 2 socks [INFERENCE]
	OAEPHash        string // "SHA-1"
	SigningMode     string // "none" | "openajs"
	Transport       string // "httpupgrade" | "rawtcp"
}

type SessionRequest struct {
	AccountID string
	Server    Server
	Device    DeviceIdentity
	Token     SessionToken
	Profile   VersionProfile
}

type Target struct {
	Host string
	Port int
}

type Admission interface {
	Release()
}

type NodeSpec struct {
	NodeID       string // "<account_id>:<server_id>"
	AccountID    string
	ServerID     string
	InternalPort int
	GatePort     int
	CtlPort      int
	ConfigPath   string
	BinaryPath   string
}

type NodeHandle interface {
	PID() int
	SID() string // first 6 chars only
	Wait() error
}

type NodeEvent struct {
	NodeID string
	Kind   string // "started" | "ready" | "exited" | "stderr"
	At     time.Time
	Err    error
}

type NodeView struct {
	NodeID    string
	GatePort  int
	RegionTag string
	Seq       int
	State     string
	EgressIP  string
}

type Rendered struct {
	Content string
	Hash    string // sha256 of Content, for idempotent PATCH
	Nodes   int
}

type ProbeResult struct {
	OK        bool
	Latency   time.Duration
	EgressIP  string
	CheckedAt time.Time
	Err       error
}

type PlatformID string

type PlatformSpec struct {
	Name              string // "ajs-<user>"
	StickyTTL         string // "8h"
	RegexFilters      []string
	RegionFilters     []string
	AllocationPolicy  string
	PassiveBreakerOff bool
}

type LeaseState struct {
	Account   string
	EgressIP  string
	ExpiresAt time.Time
	Present   bool
}

type GateState struct {
	NodeID      string
	State       string // mirrors the node state machine
	ActiveFlows int
	Limit       int
	Draining    bool
	GraceEndsAt time.Time
}

type Labels map[string]string

type Event struct {
	TS        time.Time
	Level     string
	Component string
	Event     string
	Node      string
	Account   string
	Flow      string
	Duration  time.Duration
	ErrClass  string
	BytesIn   int64
	BytesOut  int64
	Detail    map[string]string
}

type Failure struct {
	Class      string // one of the 15 classes in REPORT.md
	Scope      string // "flow" | "node" | "account" | "profile" | "global"
	Retryable  bool
	ReplyCode  byte
	Token      string // sanitized vendor token, may be empty
	Detail     string // sanitized, truncated to 200 bytes
	OccurredAt time.Time
}

type Secret string

func (Secret) String() string   { return "[redacted]" }
func (Secret) GoString() string { return "[redacted]" }
```

`Secret` exists so that a `%v`/`%+v` on any struct that embeds a credential cannot print it. The
type is deliberately not `[]byte`: accidental `string(s)` conversion is a compile error, which is
the point.

## Errors

```go
var (
	ErrAuthRejected      = errors.New("account: auth rejected")
	ErrMembershipExpired = errors.New("account: membership expired")
	ErrServerGone        = errors.New("account: server gone")
	ErrFVReject          = errors.New("fv: authentication rejected")
	ErrDataChannelReject = errors.New("fv: data channel rejected")
	ErrTransport         = errors.New("fv: transport down")
	ErrParse             = errors.New("api: response not understood")
	ErrConcurrency       = errors.New("gate: concurrency limit")
	ErrOverload          = errors.New("gate: admission timeout")
	ErrResinUnavailable  = errors.New("resin: admin api unavailable")
	ErrProbeFailed       = errors.New("panel: canary failed")
	ErrProfileExhausted  = errors.New("account: no version profile worked")
)
```

Rules:

1. Every returned error is wrapped with `%w` so `errors.Is` works, and carries **no** secret, no
   full `sid`, and no request body. The classifier never parses error strings — it uses
   `errors.Is`/`errors.As` on the sentinels above plus the typed `*FVReject{Fields}` from
   `emulator/internal/fv/auth.go`.
2. `Classify` maps sentinels to `Failure.Class`; an unmapped error becomes
   `CLIENT_PARSE_FAILURE` with `Retryable=false` (fail closed, alert).
3. `ReplyCode` is the only source of SOCKS5 reply bytes. It returns `0x01` when it does not know,
   never `0x00`.

## Protocol codecs

| ID | Go surface | Package | Selected by |
|---|---|---|---|
| `P1` | `AccountClient` | `internal/account/ctlapi` | always |
| `P2` | `fv.Transport` name `httpupgrade` (ProtocolVersion 2) | `emulator/internal/fv` | profile `Transport` |
| `P3` | `fv.Transport` name `rawtcp` | `emulator/internal/fv` | profile fallback |
| `P4` | `FlowSession.OpenFlow` → `fv.Session.OpenStreamContext` | `emulator/internal/fv` | inside P2/P3 |
| `P5` | `fv.OPV2Frame` (codec only) | `emulator/internal/fv` | never in production |
| `P6` | `Keepalive` (`PassiveKeepalive` only) | `internal/account` | always |
| `P7` | none | — | excluded; gate answers `0x07` |
| `P8` | `Classifier` + token table | `internal/obs` | always |

`ProtocolVersion` and `Transport` in a `VersionProfile` are redundant by construction
(`2` ⇒ `httpupgrade`, `1` ⇒ `rawtcp`); the constructor rejects a mismatched pair rather than
letting two fields disagree at runtime.

## HTTP: ajs-accountd

Base `http://127.0.0.1:2261`, `Authorization: Bearer $AJS_ADMIN_TOKEN`, JSON only. These extend
`docs/PANEL.md` §3; the rows marked *new* are added by this slice.

| Method | Path | Request | Response | Notes |
|---|---|---|---|---|
| `GET` | `/v1/accounts` | `?state=&limit=&offset=` | `{"accounts":[{...}],"total":n}` | secrets never returned |
| `POST` | `/v1/accounts` | `{"label","username","password","concurrency","platform","servers_allow"?,"regions"?,"expires_at"?}` | `201 {"id","state":"LOGGING_IN"}` | triggers login |
| `GET` | `/v1/accounts/{id}` | — | account + derived state | |
| `PATCH` | `/v1/accounts/{id}` | partial account | `{"id","state"}` | password rotation allowed |
| `DELETE` | `/v1/accounts/{id}` | — | `204` | drains all nodes first |
| `POST` | `/v1/accounts/{id}/actions/login` | — | `202 {"state":"LOGGING_IN"}` | force re-login |
| `POST` | `/v1/accounts/{id}/actions/logout` | — | `202 {"state":"DRAINING"}` | drain + stop sessions |
| `GET` | `/v1/accounts/{id}/session` | — | `{"state","sid_prefix","opened_at","profile","fields"}` | *new*; `fields` is the sanitized auth map |
| `GET` | `/v1/accounts/{id}/servers` | — | `{"fetched_at","servers":[...]}` | last snapshot |
| `GET` | `/v1/accounts/{id}/profile` | — | `VersionProfile` | *new* |
| `PATCH` | `/v1/accounts/{id}/profile` | `VersionProfile` | `VersionProfile` | *new*; validated, not free-form |
| `GET` | `/v1/nodes` | `?account_id=&state=&region=` | `{"nodes":[{...}]}` | includes `active_flows`, `egress_ip`, `probe_age` |
| `POST` | `/v1/nodes/{id}/actions/drain` | `{"grace_seconds":30}` | `{"state":"DRAINING","active_flows":n}` | *new* body; gate-first ordering |
| `POST` | `/v1/nodes/{id}/actions/undrain` | — | `{"state":"LIVE"}` | legal only while the listener is open |
| `POST` | `/v1/nodes/{id}/actions/probe` | — | `{"ok","latency_ms","egress_ip"}` | synchronous canary |
| `GET` | `/v1/nodes/{id}/flows` | — | `{"active":n,"limit":n,"oldest_age_s":n}` | *new*; from the gate control port |
| `GET` | `/v1/subscription` | — | `{"content","hash","nodes","published_at"}` | *new*; the exact rendered text |
| `POST` | `/v1/reconcile` | — | `{"actions":[...]}` | idempotent |
| `GET` | `/v1/errors` | `?class=&since=` | `{"classes":[{"class","count","last_at","last_detail"}]}` | *new*; feeds the taxonomy |
| `GET` | `/healthz` | — | `{"process","db","resin_up","accounts":{...}}` | unauthenticated on loopback |
| `GET` | `/metrics` | — | Prometheus text | |
| `GET` | `/v1/audit` | `?limit=&subject=` | `{"events":[...]}` | append-only |
| `GET` | `/v1/topology` | — | `{"nodes":[...],"platforms":[...]}` | rendered view |

Error envelope (all non-2xx):

```json
{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"resin: 502 from /api/v1/subscriptions",
          "class":"RESIN_UNAVAILABLE","retry_after_ms":10000}}
```

Codes: `INVALID_ARGUMENT`, `NOT_FOUND`, `CONFLICT`, `UPSTREAM_UNAVAILABLE`, `QUOTA_EXCEEDED`
(from `docs/PANEL.md` §3) plus `PROFILE_EXHAUSTED` and `DRAIN_IN_PROGRESS` (new). The `class`
member is always one of the 15 taxonomy classes, so the console can group failures without
parsing prose.

### Schemas

`GET /v1/accounts/{id}/session`:

```json
{"state":"READY","sid_prefix":"a1b2c3","opened_at":"2026-10-23T09:58:11Z",
 "profile":{"name":"linux-4.2.3.0-binary","client_version":"4.2.3.0","core_version":"20241105-1",
            "protocol_version":2,"impl_type":2,"oaep_hash":"SHA-1","signing_mode":"none",
            "transport":"httpupgrade"},
 "fields":{"r":"ok","sip":"10.0.0.1","cip":"10.0.0.2","ka":"30","mtu":"1400"}}
```

`sid_prefix` is six characters. The full `sid` is never returned by the API.

`PATCH /v1/accounts/{id}/profile`:

```json
{"client_version":"4.2.3.0","core_version":"20241105-1","protocol_version":2,"impl_type":2,
 "oaep_hash":"SHA-1","signing_mode":"none","transport":"httpupgrade"}
```

Validation: `protocol_version` ∈ {1,2}; `oaep_hash` ∈ {SHA-1,SHA-256}; `signing_mode` ∈
{none,openajs}; `transport` must agree with `protocol_version`. Anything else is
`INVALID_ARGUMENT`.

`GET /v1/nodes/{id}/flows`:

```json
{"node_id":"acc1:12","active":7,"limit":64,"oldest_age_s":412,
 "reject_5m":{"concurrency":0,"overload":0,"drain":3}}
```

## HTTP: gate

Base `http://127.0.0.1:<ctl_port>` (range 14000–14999), loopback-only, **no auth**: the trust
boundary is the same as the SOCKS5 port next to it, and the port is allocated per node. The
control API is deliberately tiny — the gate is a data path, not a service.

| Method | Path | Request | Response | Notes |
|---|---|---|---|---|
| `GET` | `/state` | — | `GateState` JSON | polled by accountd on reconcile |
| `POST` | `/drain` | `{"grace_seconds":30}` | `{"state":"DRAINING","active_flows":n,"grace_ends_at":"..."}` | idempotent |
| `POST` | `/undrain` | — | `{"state":"LIVE"}` | `409` if the listener is closed |
| `GET` | `/metrics` | — | Prometheus text | node-scoped subset |

`GET /state`:

```json
{"node_id":"acc1:12","state":"LIVE","active_flows":7,"limit":64,
 "draining":false,"grace_ends_at":"0001-01-01T00:00:00Z",
 "account_in_use":21,"account_limit":32,"reject_5m":{"concurrency":0,"overload":0,"drain":0}}
```

The gate has no endpoint that mutates credentials, sessions, or subscriptions. If the gate is
unreachable, accountd treats the node as `DOWN` (`TRANSPORT_DOWN`) — it never assumes the gate is
healthy because the port accepts TCP.

SOCKS5 behaviour at the gate:

| Situation | Reply |
|---|---|
| admitted, upstream `05 00` | `05 00 00 01 00 00 00 00 00 00` |
| `UDP ASSOCIATE` / `BIND` | `05 07 ...` |
| node draining, or semaphore full, or overload | `05 02 ...` |
| upstream refused | `05 05 ...` |
| upstream host/network unreachable | `05 04` / `05 03` |
| upstream reply byte 1 ∈ `0x01..0x08` | that code, verbatim |

## Resin outbound

Base `http://127.0.0.1:2260`, `Authorization: Bearer $RESIN_ADMIN_TOKEN`. Routes verified in
`docs/PANEL.md` §4 against Resin `internal/api/server.go`.

| Method | Path | Body | Used for |
|---|---|---|---|
| `GET` | `/api/v1/subscriptions?keyword=ajs-live` | — | find the local subscription |
| `POST` | `/api/v1/subscriptions` | `{"name":"ajs-live","source_type":"local","content":"...","update_interval":"30s","ephemeral":true}` | create once |
| `PATCH` | `/api/v1/subscriptions/{id}` | `{"content":"..."}` | publish topology |
| `POST` | `/api/v1/subscriptions/{id}/actions/refresh` | — | deterministic sync point |
| `GET` | `/api/v1/platforms` | — | find user platforms |
| `POST` | `/api/v1/platforms` | `{"name","sticky_ttl","regex_filters","region_filters","allocation_policy","passive_circuit_breaker_disabled"}` | create per user |
| `PATCH` | `/api/v1/platforms/{id}` | `{"regex_filters":[...]}` | rotation exclusion, suspend |
| `DELETE` | `/api/v1/platforms/{id}/leases/{account}` | — | rotate / drain |
| `GET` | `/api/v1/platforms/{id}/leases/{account}` | — | read egress IP |
| `GET` | `/api/v1/nodes/{hash}` | — | egress IP, region, probe age |
| `GET` | `/api/v1/request-logs?platform_id=&account=&from=&to=` | — | usage reconciliation (paged) |

Failure handling: `5xx`/timeout on any call sets `ajs_resin_up=0`, keeps sessions alive, stops
publishing, and retries every 10 s. A `404` on a lease means "already gone" and is success for
`Rotate`.

## Subscription format

```
socks5://127.0.0.1:<gate_port>#<cc>-<subdiv>-<seq>
```

- One line per `LIVE` node, sorted by `(region_tag, node_id)`, joined with `\n`, **no trailing
  newline** (`[INFERENCE]`; the parser splits lines and tolerates both).
- `<cc>`: lowercase ISO-3166 alpha-2 from `ctry`, else derived from `tit`; `<subdiv>`: lowercase
  ASCII `[a-z0-9]` from the server title, truncated to 8 chars; `<seq>`: zero-padded 2-digit
  stable counter per `(account, server)`.
- No credentials in the line: the gate is loopback and no-auth.
- Resin renders the node as `ajs-live/<tag>`; Platform `regex_filters` match that form.
- Content hash is `sha256(content)`; an unchanged hash is not PATCHed.
- Empty content is refused unless `AJS_ALLOW_EMPTY_PUBLISH=1` and every node is `DOWN`.

PATCH body example:

```json
{"content":"socks5://127.0.0.1:13007#cn-zj-03\nsocks5://127.0.0.1:13011#hk-01"}
```

## State machines

### Account

States: `NEW`, `LOGGING_IN`, `AUTHENTICATED`, `SERVERS_OK`, `READY`, `DRAINING`, `STOPPED`,
`AUTH_FAILED`, `MEMBERSHIP_EXPIRED`, `SUSPENDED`.

| From | Event | To | Side effect |
|---|---|---|---|
| `NEW` | operator creates | `LOGGING_IN` | seal password, generate device identity |
| `LOGGING_IN` | `AuthResult == "OK"` | `AUTHENTICATED` | store token (memory), start 10-min sync timer |
| `LOGGING_IN` | `AuthResult != "OK"` ×3 / 15 min | `AUTH_FAILED` | audit, alert |
| `LOGGING_IN` | `Expired == true` | `MEMBERSHIP_EXPIRED` | drain all nodes, audit |
| `AUTHENTICATED` | `LoginSync` ok | `SERVERS_OK` | persist server snapshot |
| `SERVERS_OK` | first session `r == "ok"` | `READY` | node `STARTING` |
| `READY` | operator drain / logout | `DRAINING` | gate drain on all nodes |
| `DRAINING` | zero flows or grace expiry | `STOPPED` | stop processes, delete leases |
| `STOPPED` | operator login | `LOGGING_IN` | |
| any | operator suspend | `SUSPENDED` | platform filter `["!.*"]` |
| `SUSPENDED` | operator resume | previous | restore filters |
| `AUTH_FAILED` | operator PATCH (password) | `LOGGING_IN` | reset attempt counter |
| `MEMBERSHIP_EXPIRED` | operator PATCH (`expires_at`) | `LOGGING_IN` | |

Invariant: `READY` requires at least one node in `LIVE`; the account drops back to `SERVERS_OK`
when the last node leaves `LIVE`, and that transition never deletes the server snapshot.

### Node

States: `STARTING`, `READY`, `LIVE`, `DRAINING`, `DOWN` (the five already used in
`docs/PANEL.md` §6).

| From | Event | To | Side effect |
|---|---|---|---|
| — | slot allocated | `STARTING` | spawn `ajsproxy`, write config `0600` |
| `STARTING` | `listening on` + SOCKS greeting | `READY` | unlink config, register gate upstream |
| `STARTING` | 20 s timeout / exit | `DOWN` | kill tree, backoff, retry once after fresh login |
| `READY` | canary ok | `LIVE` | publish line, mark `last_ok_at` |
| `LIVE` | canary fail ×2 / transport circuit | `DOWN` | remove line, keep process 60 s for forensics |
| `LIVE` | drain request / `SERVER_GONE` | `DRAINING` | gate refuses new, publish removal |
| `DRAINING` | zero flows or grace expiry | `DOWN` | stop process, delete lease |
| `DRAINING` | undrain (listener still open) | `LIVE` | restore admission, republish |
| `DOWN` | backoff elapsed | `STARTING` | |

### Flow

States: `ACCEPTED`, `ADMITTED`, `CONNECT_SENT`, `REMOTE_OK`, `RELAY`, `CLOSED`, plus the terminal
`REJECTED`.

| From | Event | To | Reply |
|---|---|---|---|
| `ACCEPTED` | SOCKS5 greeting | `ACCEPTED` | `05 00` |
| `ACCEPTED` | CONNECT parsed, node not `LIVE` or draining | `REJECTED` | `05 02` |
| `ACCEPTED` | semaphore full or wait > 5 s | `REJECTED` | `05 02` |
| `ACCEPTED` | admission ok | `ADMITTED` | — |
| `ADMITTED` | upstream dial fails | `REJECTED` | `05 03`/`05 04`/`05 05` |
| `ADMITTED` | CONNECT sent to `ajsproxy` | `CONNECT_SENT` | — |
| `CONNECT_SENT` | `code=200` + `05 00` | `REMOTE_OK` | `05 00` to client |
| `CONNECT_SENT` | `DATA_CHANNEL_REJECT` | `REJECTED` | `05 01` (after one retry) |
| `REMOTE_OK` | first byte either way | `RELAY` | — |
| `RELAY` | EOF / RST / idle timeout | `CLOSED` | — |

Rule: `REJECTED` after `REMOTE_OK` is impossible — once the success reply is sent, the only
terminal state is `CLOSED`. This is what makes "never retry a relayed flow" mechanically
enforceable.

## Sequences

### Session open

```
accountd                    ctlapi (P1)                FV endpoint (P2)
   │ Login/LoginSync ─────────▶│                            │
   │◀── UserName/ConnectPassword│                            │
   │ TCP connect ───────────────────────────────────────────▶│
   │ [16B K][CTR(K,0)(len||text)] + CTR(sessionKey,n)(body) ▶│
   │◀── [CTR(K,0)(len||text)] "code=200..." ─────────────────│
   │◀── CTR(sessionKey,n)(auth map) ─────────────────────────│
   │◀── EOF (half-close) ───────────────────────────────────│
   │ r == ok && sid != ""  →  READY                          │
```

### Flow open

```
client ─▶ gate: 05 01 00 03 host port
gate: acquire(account) + acquire(node)         → 05 02 on failure
gate ─▶ ajsproxy: 05 01 00 03 host port
ajsproxy ─▶ FV: [K][CTR(K,0)(s=<sid> n=<div> k= _=pad)][CTR(sessionKey,div)(CONNECT)]
ajsproxy ◀─ FV: code=200 + 10B 05 00
gate ◀─ ajsproxy: 05 00
gate ─▶ client: 05 00 00 01 0.0.0.0:0
relay both directions, bytes counted at the gate
```

### Drain

```
POST /v1/nodes/{id}/actions/drain
  1. DB: node → DRAINING
  2. gate POST /drain {grace_seconds:30}      (admission closed immediately)
  3. publisher: remove line, coalesced ≤5 s, then refresh
  4. leases: DELETE for the account (so Resin re-pins elsewhere)
  5. wait: active_flows == 0 or grace expiry
  6. supervisor: SIGTERM, 5 s, kill tree
  7. DB: node → DOWN, publish (if content changed)
```

### Rotation

```
POST /v1/users/{id}/rotate {avoid_current_ip:true}
  1. rate check (1 / 10 s)                      → 409 CONFLICT
  2. read platform regex_filters
  3. PATCH platform + "!^ajs-live/<tag>$"
  4. POST subscriptions/{id}/actions/refresh
  5. DELETE platform/{id}/leases/{account}
  6. sleep 10 s
  7. PATCH platform with the original filters   (restore even on error)
  8. audit user.rotate
```

## Config

| Variable | Default | Meaning |
|---|---|---|
| `AJS_LISTEN` | `127.0.0.1:2261` | admin API |
| `AJS_ADMIN_TOKEN` | — | required unless `AJS_ALLOW_INSECURE=1` |
| `AJS_STATE_DIR` | `./data` | `ajs.db`, node configs, logs |
| `AJS_MASTER_KEY` | keyring | 32-byte secret-encryption key |
| `AJS_AJSPROXY_BIN` | `./ajsproxy` | emulator binary |
| `AJS_RESIN_URL` | `http://127.0.0.1:2260` | control plane |
| `AJS_RESIN_ADMIN_TOKEN` | — | Resin bearer |
| `AJS_INTERNAL_PORT_BASE` | `12000` | ajsproxy listeners |
| `AJS_GATE_PORT_BASE` | `13000` | gate SOCKS5 listeners |
| `AJS_GATE_CTL_PORT_BASE` | `14000` | gate control listeners |
| `AJS_ACCOUNT_CONCURRENCY` | `32` | default per-account ceiling |
| `AJS_NODE_CONCURRENCY` | `64` | default per-node ceiling |
| `AJS_GLOBAL_CONCURRENCY` | `512` | process-wide ceiling |
| `AJS_ADMISSION_TIMEOUT` | `5s` | queue wait before `0x02` |
| `AJS_FLOW_IDLE_TIMEOUT` | `0` | 0 disables idle reaping |
| `AJS_CANARY_INTERVAL` | `60s` | ±10 s jitter |
| `AJS_CANARY_TARGET` | `www.cloudflare.com:443` | must complete a TLS handshake |
| `AJS_PUBLISH_MIN_INTERVAL` | `5s` | PATCH coalescing |
| `AJS_ALLOW_EMPTY_PUBLISH` | `0` | refuse empty content unless set |
| `AJS_LOGIN_BACKOFF` | `30s` | doubles to 30 min |
| `AJS_CLOCK_SKEW_WARN` | `300s` | warn threshold against the API `Date` header |
| `AJS_ALLOW_PUBLIC_LISTEN` | `0` | loopback enforcement override |

