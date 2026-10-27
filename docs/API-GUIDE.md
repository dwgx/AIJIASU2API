# API guide — AJiaSu → API

Webshare-style quickstart for the retained `ajs-accountd` API: accounts, catalog, connect/switch,
leases, nodes, requests/usage, topology and health, authenticated by an admin bearer token or an
`ajs_sk_` scoped key. The full contract is [`docs/openapi.json`](openapi.json); this page is the
short path.

> **Rendered copy.** The same material is served at `/docs` as a public static bundle — no admin
> token, no proxy to `/v1/...` — and its API reference is generated from `docs/openapi.json` by
> `dashboard/scripts/gen-docs.mjs`; `cd dashboard && npm run docs:gen -- --check` fails on drift.
> This guide remains the long form.

The archive map is [`ARCHIVE.md`](../ARCHIVE.md). The gates this API drives bind loopback, and
remote proxy ingress is **not enabled** in this archive — see
[Proxy data plane](#proxy-data-plane-loopback-only).

## Base URL

```
http://127.0.0.1:2261
```

`AJS_LISTEN` defaults to `127.0.0.1:2261`. `GET /healthz` and `GET /readyz` are unauthenticated.
The payment callbacks are authenticated by provider signature, and the opt-in customer portal chain
(`/v1/portal/*`) by its own `__Host-ajs_portal` cookie. Every other route, including `/metrics`,
needs a bearer.

## Authentication

Two credential kinds are accepted in `Authorization: Bearer <token>` for the core API (operator
RBAC tokens are a third, separate world — see [`RBAC.md`](RBAC.md)):

| Credential | Format | Authority |
|---|---|---|
| Admin token | `AJS_ADMIN_TOKEN` (at least 32 characters, not a documented placeholder) | Full admin: every route |
| Scoped key | `ajs_sk_...`, returned once by `POST /v1/scoped-keys` | Only the routes its scopes name, bound to its `userId` and narrowed to its account/region grants; the gate accepts it only with `proxy:connect` |

In a strict-RBAC deployment (`AJS_RBAC_STRICT=1`) the shared `AJS_ADMIN_TOKEN` is retired; use an
operator `admin` token from [`RBAC.md`](RBAC.md) instead. Scoped keys keep their own policy.

Rules that apply to both:

- **Unsafe methods** (`POST`, `PUT`, `PATCH`, `DELETE`) also require `X-AJS-CSRF: 1`.
- If an `Origin` header is present it must exactly match `AJS_ADMIN_ORIGINS`; CLI and SDK clients
  omit `Origin` and are treated as the non-browser path.
- A missing or invalid bearer answers `401 UNAUTHORIZED` with the JSON error envelope.
- `AJS_ALLOW_INSECURE=1` disables auth only on a loopback bind; startup refuses it elsewhere.
- Scoped-key calls are rate limited per key (default 60/minute; `rateLimitPerMin: 0` = unlimited)
  and answer `X-RateLimit-Limit`, `X-RateLimit-Remaining` and `X-RateLimit-Reset`; a refusal is
  `429 RATE_LIMITED` with `Retry-After`.

Create a scoped key with the admin token (key management is admin-only):

```bash
curl -sS -X POST "$BASE/v1/scoped-keys" \
  -H "Authorization: Bearer $AJS_ADMIN_TOKEN" \
  -H "X-AJS-CSRF: 1" \
  -H "Content-Type: application/json" \
  -d '{
    "userId": "u-01",
    "label": "ci-runner",
    "scopes": ["catalog:read", "leases:read", "leases:write", "proxy:connect", "usage:read"],
    "accountIds": ["acc-02"],
    "regions": [],
    "rateLimitPerMin": 60
  }'
```

The response is `{"key": {...}, "secret": "ajs_sk_..."}`. The secret is shown exactly once; only
its SHA-256 is persisted. An unknown `userId` answers `404 NOT_FOUND` before anything is minted.
Revoke with `DELETE /v1/scoped-keys/{id}` (204), also admin-only.

The key is bound to `userId` and must name at least one `accountIds` or `regions` entry when its
scopes include an `accounts:*`, `catalog:*`, `leases:*` or `proxy:connect` grant; without one the
create answers `400 INVALID_ARGUMENT`. See [Key subject and grants](#key-subject-and-grants).

## Scopes

A scoped key needs the exact scope of the matched route. A key carrying `admin` bypasses the
table, and the admin token bypasses it by definition. Routes absent from the table are admin-only.
`proxy:connect` is the one scope with no HTTP route: it authorizes the loopback gate. Every
operation a scoped key can reach declares its grant in the spec as `x-required-scope`, together
with the `403` and `429` responses and the `X-RateLimit-*` headers; an operation without the
extension is not reachable with a scoped key (it needs the admin token, a provider signature or
the portal cookie, as that route documents).

| Scope | Grants | Representative routes |
|---|---|---|
| `accounts:read` | Read accounts and account IO capabilities/device | `GET /v1/accounts`, `GET /v1/accounts/{id}`, `GET /v1/accounts/{id}/device` |
| `accounts:write` | Create/patch/delete accounts; login/logout | `POST /v1/accounts`, `PATCH /v1/accounts/{id}`, `POST /v1/accounts/{id}/actions/login` |
| `catalog:read` | Read catalog, nodes, operations, topology, debug trace | `GET /v1/accounts/{id}/servers`, `GET /v1/nodes`, `GET /v1/operations`, `GET /v1/topology` |
| `catalog:write` | Selection and guarded switch; node drain/probe; operation cancel | `PUT /v1/accounts/{id}/selection`, `POST /v1/accounts/{id}/actions/connect`, `POST .../select-node`, `POST /v1/nodes/{id}/actions/drain` |
| `leases:read` | List and read the key user's leases | `GET /v1/leases`, `GET /v1/leases/{id}` |
| `leases:write` | Create/rotate/revoke the key user's leases | `POST /v1/leases`, `POST /v1/leases/{id}/actions/rotate`, `DELETE /v1/leases/{id}` |
| `proxy:connect` | Authenticate at a loopback gate and connect through the key user's lease | `SOCKS5 127.0.0.1:<gatePort>`, `HTTP CONNECT 127.0.0.1:<gatePort+offset>` |
| `requests:read` | Read the request log | `GET /v1/requests` |
| `audit:read` | Read and export the audit log | `GET /v1/audit`, `GET /v1/audit/export.csv` |
| `metrics:read` | Prometheus and JSON metrics | `GET /metrics`, `GET /v1/metrics` |
| `settings:read` | Read settings and schedules | `GET /v1/settings`, `GET /v1/schedules` |
| `settings:write` | Patch settings and schedules | `PATCH /v1/settings`, `POST /v1/schedules` |
| `usage:read` | Usage aggregation | `GET /v1/usage` |
| `renewal:read` | Renewal plan table and per-account expiry | `GET /v1/renewal/plans`, `GET /v1/renewal/accounts/{id}` |
| `health:read` | Loopback gate health | `GET /v1/health/nodes` |
| `events:read` | Event replay and SSE stream | `GET /v1/events`, `GET /v1/events/stream` |
| `admin` | Every route, including admin-only surfaces | all |

Admin-only even with a scoped key: account export/import/bulk-delete, device pool, users, API
keys, scoped-key management, webhooks, backup/inspect/restore, plans/orders, tenant
administration, reconciliation, every `/v1/lab/*` route (including `POST /v1/lab/egress-check`),
`GET /v1/whoami`, `GET /v1/openapi.json` and `GET /v1/system/update`. The reason is in
[`daemon/internal/api/scoped_keys.go`](../daemon/internal/api/scoped_keys.go): a filtered export
still discloses credentials, a filtered bulk-delete is destructive with partial success, lab
telemetry names other tenants' accounts, users, targets and gate ports and its egress check spends
a real vendor flow, and a customer key has no business enumerating the daemon's outbound receivers
or sealed files.

## Key subject and grants

A scoped key has two boundaries: the user it belongs to (`userId`) and the accounts it may reach
(`accountIds`/`regions`).

**The subject is the lease owner.** A non-admin scoped key is bound to its own `userId`:

- `GET /v1/leases` returns only leases whose `userId` equals the key's user. Naming another
  `user_id`/`userId` answers `403 FORBIDDEN` with `reason: "scoped_key_restricted"` and does not
  reveal whether that user holds a lease.
- `POST /v1/leases` fills an empty `userId` with the key's user; a different `userId` is refused
  with the same `403` before anything is allocated. An `Idempotency-Key` replay is re-checked
  against both the user and the account grant, so a retry never returns another user's lease.
- `GET /v1/leases/{id}`, `DELETE /v1/leases/{id}` and
  `POST /v1/leases/{id}/actions/rotate` resolve the owner first. A missing id and another user's
  lease answer the same `404 NOT_FOUND`; the key's own lease on an account outside its grant
  answers `403`. The detail read returns the lease plus its loopback `connection` block and never
  the scoped-key secret.
- Accounts and nodes are operator assets: they are filtered by the key's grants, never by
  `userId`, and no customer key owns them.

**An account-bound key must name a grant.** A new non-admin key carrying an `accounts:*`,
`catalog:*`, `leases:*` or `proxy:connect` scope must name at least one `accountIds` or `regions`
entry; `POST /v1/scoped-keys` answers `400 INVALID_ARGUMENT` otherwise. A key whose scopes are all
global (`usage:read`, `requests:read`, `metrics:read`, `events:read`, `settings:*`, `audit:read`,
`renewal:read`, `health:read`) may omit both lists, which then means "not narrowed by
account".
A key carrying the `admin` scope ignores both lists. A key minted before this rule with an
account/catalog/lease scope and no grant fails closed with `403` on every non-admin route until it
is reissued.

## Narrowing with `accountIds` / `regions`

Both lists narrow **reads and writes**; for an account-bound key at least one of them is required
(see above):

- Empty `accountIds` and empty `regions` mean "not narrowed by account". A non-admin key with an
  account, catalog, lease or `proxy:connect` scope cannot be minted that way.
- An account matches when the id list is empty or contains its id, **and** the region list is
  empty or overlaps the account's `regions`.
- The filter covers the account path, the renewal account path and the `account_id`/`accountId`
  query on nodes, leases and operations, so a restricted key cannot reach one account through a
  different account-scoped route.
- A request for an outside account answers `403 FORBIDDEN` with `reason: "scoped_key_restricted"`.
- **Writes are narrowed too.** A key with `accounts:write`, `catalog:write` or `leases:write`
  restricted to `acc-A` is refused on every mutation of another account: `PATCH`/`DELETE
  /v1/accounts/{id}`, the login/logout/connect/disconnect/reconnect/select-node actions, the
  selection routes, `POST /v1/leases`, lease rotate and revoke, node drain/undrain/probe and
  operation cancel. `POST /v1/leases` must name an allowed `accountId`; a restricted key that
  omits it is refused, because the store would otherwise pick any free account.
- A write that names an account-shaped id the store cannot resolve answers the same `403` as a
  foreign account, so a mutation cannot be used to probe whether an account, node or operation
  exists. Lease ids are different: a missing id and another user's lease both answer `404`, so a
  lease id cannot be probed either. Reads keep `404` for an unknown id and `403` for a known
  account outside the key.
- `GET /v1/usage` and `GET /v1/requests` are per-user, not per-account: a non-admin scoped key
  is served the rows whose exact `RequestLog.UserID` is its own subject, filtered before
  aggregation or pagination. A query naming another `userId` answers `403`, and a scoped
  `account=` usage filter answers `400 INVALID_ARGUMENT` because the log stores a recyclable
  label, not a stable account id.
- Global, unattributed surfaces are still refused to a restricted key rather than guessed:
  `GET /v1/audit`, `GET /metrics` and the event stream. `/v1/topology` is projected to the key's
  visible nodes, and `/v1/health/nodes` is filtered.
- Admin tokens and keys carrying the `admin` scope ignore both lists: the grant already covers
  every account.
- `POST /v1/accounts` (create) is not narrowed by the lists: the new id cannot be in the key's
  `accountIds`, so a restricted key cannot act on the account it just created.

## Quickstart calls

Set the two shell variables first; every example below uses them.

```bash
export BASE=http://127.0.0.1:2261
export AJS_TOKEN=...            # admin token, or an ajs_sk_ secret with the right scopes
```

The examples also use `SERVER_ID` and `SNAPSHOT_ID` from a catalog response and `LEASE_ID` from a
lease create; fill them from the preceding call.

### List accounts

```bash
curl -sS "$BASE/v1/accounts?state=active&limit=50&offset=0" \
  -H "Authorization: Bearer $AJS_TOKEN"
```

Returns an array of accounts: `id`, `label`, `username`, `platform`, `state` (`active`, `paused`,
`auth_failed`, `expired`), `concurrency`, `useCount`, `effectiveSlots`, `regions`, `nodeCount`,
`liveNodeCount`, `usageBytes`, `trafficBudgetBytes`, `expiresAt`, `lastLoginAt`, `createdAt`.
Secrets are never returned.

### Read the catalog

```bash
curl -sS "$BASE/v1/accounts/acc-02/servers?region=jp-tyo&limit=50&offset=0&sort=order" \
  -H "Authorization: Bearer $AJS_TOKEN"
```

Returns one page plus `snapshotId`, `fetchedAt`, `stale`, `total`, `nextOffset`, `hasMore`,
`regions`, `geo` and `servers[]` (`id`, `title`, `region`, `host`, `port`, `status`, `nodeId`,
`nodeState`, `selected`, `blockingMarks`, `latencyMs`). `q`, `region`, `status`, `province`,
`city`, `selected`, `offset`, `limit` (1–500, default 50) and `sort` (`order`, `title`, `host`,
`status`, `region`) are accepted. Echo `snapshotId` on connect/selection so a changed catalog is
rejected instead of silently switching to a different server.

`geo` is the server-side map aggregate: `total`, `unknown` (rows whose title does not resolve),
`provinces[]` (`code`, `count`, `connected`, `cities[]` of `code`/`label`/`count`), `current` (the
account's running row, or `null`) and `live[]`. It is computed over the `q`/`region`/`status`
scope, so the map keeps every province the search matches while `province` (a `geo.provinces[].code`,
or `00` for the unresolved bucket) and `city` narrow only the table. `total`, `regions` and the
page always describe the filtered rows.

### Connect or switch

```bash
curl -sS -X POST "$BASE/v1/accounts/acc-02/actions/connect" \
  -H "Authorization: Bearer $AJS_TOKEN" \
  -H "X-AJS-CSRF: 1" \
  -H "Content-Type: application/json" \
  -d "{\"serverId\":\"$SERVER_ID\",\"snapshotId\":\"$SNAPSHOT_ID\",\"confirm\":true}"
```

`confirm:true` is mandatory. The guarded switch keeps one session per account, applies a per-account
cooldown and rejects a stale `snapshotId`. `200` returns `accountId`, `serverId`, `nodeId`,
`nodeState`, `gatePort`, `egressIp` (a vendor claim), `switchedFrom`, `durationMs`, `warnings`,
`auditId`, `alreadyConnected` and a rollback hint. Related routes:
`POST .../actions/disconnect`, `POST .../actions/reconnect`, and `PUT/DELETE
/v1/accounts/{id}/selection` for a pinned target that starts no session.

For an asynchronous, idempotent switch:

```bash
curl -sS -X POST "$BASE/v1/accounts/acc-02/actions/select-node" \
  -H "Authorization: Bearer $AJS_TOKEN" \
  -H "X-AJS-CSRF: 1" \
  -H "Content-Type: application/json" \
  -d '{"target":{"region":"jp-tyo"},"wait":true,"timeoutSeconds":60,"ifCurrent":"noop"}'
```

`target` accepts `serverId`, `nodeId`, `region` and `strategy` (`next`, `first`). A `200` means the
target was already current (`changed:false`) or the switch completed; `202` returns an operation
with `id` and `state` (`queued`, `running`, `succeeded`, `failed`, `canceled`). Poll
`GET /v1/operations/{id}` (or let the SDK poll). A `wait:true` timeout answers `504
OPERATION_TIMEOUT` with `details.operationId`.

### Leases

```bash
# List, optionally for one user
curl -sS "$BASE/v1/leases?user_id=u-01&limit=50&offset=0" \
  -H "Authorization: Bearer $AJS_TOKEN"

# Create (idempotent)
curl -sS -X POST "$BASE/v1/leases" \
  -H "Authorization: Bearer $AJS_TOKEN" \
  -H "X-AJS-CSRF: 1" \
  -H "Idempotency-Key: $(openssl rand -hex 16)" \
  -H "Content-Type: application/json" \
  -d '{"userId":"u-01","platform":"ios","accountId":"acc-02","ttlSeconds":3600}'

# Detail: the lease plus the loopback endpoint to dial
curl -sS "$BASE/v1/leases/$LEASE_ID" \
  -H "Authorization: Bearer $AJS_TOKEN"

# Revoke (explicit confirmation)
curl -sS -X DELETE "$BASE/v1/leases/$LEASE_ID" \
  -H "Authorization: Bearer $AJS_TOKEN" \
  -H "X-AJS-CSRF: 1" \
  -H "Content-Type: application/json" \
  -d '{"confirm":true}'
```

Detail answers `200 {"lease": {...}, "connection": {"host": "127.0.0.1", "port": 13003,
"protocol": "socks5", "username": "u-01", "credentialKind": "ajs_sk"}}`. `port` is the gate the
lease currently points at, so a switch that rebinds the lease also moves it. The secret is never
returned: `credentialKind` names the shape, and the password is the caller's own `ajs_sk_` key
carrying `proxy:connect`. A missing id and another user's lease are both `404` for a non-admin
scoped key; a lease on an account outside its grant is `403`.

Create answers `201 {"lease": {...}, "warning"?: "...", "replayed"?: true}`. `accountId` is an
optional pin for an operator caller (must be active and match the platform); a non-admin scoped
key must name one, and it must be an account the key names. `ttlSeconds` accepts `0` (or absent)
for the user's sticky TTL, then the panel lease TTL; `1`–`59` is refused with `400
INVALID_ARGUMENT`, and the ceiling is 604800 (7 days). One active lease per user and
platform; a second is `409`, an exhausted quota or account slot is `429`. An `Idempotency-Key`
(header or body `idempotencyKey`, max 128 characters, both must agree) replays the original lease.
A non-admin scoped key is bound to its own `userId`: an empty `userId` becomes the key's user, a
different id is `403`, `GET /v1/leases` returns only that user's leases, and revoke/rotate answer
`404` for a missing id and for another user's lease alike. Revoke answers `204`. Rotation is a
Resin action: with no panel configured the route answers `503 UPSTREAM_UNAVAILABLE` and mutates
nothing (the recorded egress IP, expiry and request counter stay as they were). A lease on an
account whose display capacity is above one carries
`warning: "multi_session_unimplemented"`; the live account has `useCount=1`, so this archive admits
one slot.

### Nodes

```bash
curl -sS "$BASE/v1/nodes?state=LIVE&account_id=acc-02&limit=50&offset=0" \
  -H "Authorization: Bearer $AJS_TOKEN"
```

Returns nodes with `id`, `accountId`, `label`, `region`, `country`, `city`, `state` (`LIVE`,
`READY`, `STARTING`, `DRAINING`, `DOWN`, `STALE`), `latencyMs`, `successRate`, `inUse`,
`capacity`, `egressIp`, `protocol`, `gatePort`, `lastProbeAt`, `serverId`. Mutations:
`POST /v1/nodes/{id}/actions/drain`, `.../undrain`, `.../probe` (`catalog:write`).

### Requests and usage

```bash
curl -sS "$BASE/v1/requests?limit=200&offset=0" \
  -H "Authorization: Bearer $AJS_TOKEN"

curl -sS "$BASE/v1/usage?from=2026-10-01&to=2026-10-31&account=acc-02" \
  -H "Authorization: Bearer $AJS_TOKEN"
```

`/v1/requests` returns newest-first `RequestLog` rows: `id`, `ts`, `userId`, `userName`,
`accountLabel`, `nodeLabel`, `region`, `method`, `target`, `status`, `bytes`, `latencyMs`,
`source` (`proxy`, `mutation-lab`, `gate`). The store caps history at 10,000 rows. A non-admin
scoped key sees only rows whose exact `userId` is its own subject, filtered before pagination; a
`user_id`/`userId` naming another user answers `403` with `reason: "scoped_key_restricted"`. An
operator caller reads the global log.

`/v1/usage` accepts RFC 3339 timestamps or bare UTC dates (a bare `from` is 00:00:00Z, a bare `to`
is the end of that day). An empty `to` defaults to now, an empty `from` to 30 days before `to`, and
the widest window is 366 days.
The response carries `from`, `to`, `requests`, `bytes`, `errors`, `accounts[]`, `users[]`,
`days[]`, `topTargets[]`, `truncated`, `rowsRetained` (10,000), `retentionDays` and
`quotaEnforced`. `truncated:true` means the row cap clipped the window.

A non-admin scoped key is served the same summary over its own rows only: the exact
`RequestLog.UserID` filter runs before aggregation, so no other user's traffic reaches the totals,
the day series or the target list. A `userId`/`user_id` naming another user answers `403` with
`reason: "scoped_key_restricted"`. The log stores a recyclable account label rather than a stable
account id, so a scoped `account=` filter answers `400 INVALID_ARGUMENT` for every value; account
roll-ups in the response still cover only the accounts the caller's own rows touched. Operator
callers keep the global summary and the optional account filter.

### Topology and gate health

```bash
curl -sS "$BASE/v1/topology" -H "Authorization: Bearer $AJS_TOKEN"
curl -sS "$BASE/v1/health/nodes" -H "Authorization: Bearer $AJS_TOKEN"
```

`/v1/topology` returns `subscription` (rendered Resin config), `nodes` (node id → Resin hash),
`platforms` and `generatedAt`. A restricted key receives a projection built only from its visible
nodes (live nodes only when the view comes from the panel).

`/v1/health/nodes` returns `{"enabled": bool, "reason"?: "...", "nodes": [...]}`. Each node row has
`nodeId`, `accountId`, `serverId`, `label`, `state` (`ok`, `degraded`, `down`), `count`, `ok`,
`successRate`, `p50Ms`, `p95Ms`, `minMs`, `maxMs`, `consecutiveFailures`, an optional `last`
sample and an optional through-gate `egress` aggregate. This is **loopback gate reachability on
the daemon host**, not vendor reachability; the egress probe is opt-in
(`AJS_EGRESS_PROBE=1`) and spends a real vendor flow.

## Proxy data plane (loopback only)

The API is the control plane. A lease names the data plane:

```json
{
  "lease": {
    "id": "lease-...",
    "userId": "u-01",
    "accountId": "acc-02",
    "nodeId": "node-04",
    "protocol": "socks5",
    "gatePort": 13003,
    "egressIp": "<vendor-claimed-egress>",
    "expiresAt": "2026-10-27T12:00:00Z",
    "status": "active"
  }
}
```

The example values are illustrative; `GET /v1/leases/{id}` returns the current `lease` together
with the endpoint to dial:

```json
{
  "lease": { "id": "lease-09", "nodeId": "node-04", "gatePort": 13003, "...": "..." },
  "connection": {
    "host": "127.0.0.1",
    "port": 13003,
    "protocol": "socks5",
    "username": "u-01",
    "credentialKind": "ajs_sk"
  }
}
```

`port` follows the lease: a switch that rebinds it to another gate updates the value, and the
secret is never included.

Dial `127.0.0.1:<gatePort>` with SOCKS5. With the default `AJS_GATE_AUTH=userpass`, the username is
the panel user id and the password is a **scoped key secret** carrying `proxy:connect`:

```bash
curl --socks5-hostname 127.0.0.1:13003 -U "u-01:$SCOPED_KEY_SECRET" https://example.com/
```

The key must be active, belong to that user, carry `proxy:connect` and allow the gate's account
through its `accountIds`/`regions` grant; a `proxy:connect` key with no grant is refused, exactly as
on the API. The flow is then bound to the user's **single active lease on that account**: a user
with no lease, a lease on another account, or more than one active lease for the same account is
refused, and the gate re-checks the lease at CONNECT so a revoked or expired lease refuses the flow
instead of relaying it. The gate also enforces that user's allowlist and monthly quota. The lease
id is the only entitlement detail the gate keeps; the secret is not retained in the principal, the
flow records or the logs.

During migration a panel API-key secret created through `/v1/apikeys` is still accepted as the
password, but it is no longer the documented customer credential: it is bound to the same
single-lease rule and will be removed once keys are reissued as scoped keys. Explicit
`AJS_GATE_AUTH=loopback` is the insecure compatibility mode: the no-auth wire, no user identity, no
allowlist and no user quota, intended only for a same-host operator process.

`AJS_GATE_HTTP=1` adds an HTTP CONNECT listener at `gatePort + AJS_GATE_HTTP_OFFSET` (default
offset 1000) with the same admission pipeline and Basic credentials:

```bash
curl -x http://127.0.0.1:14003 -U "u-01:$SCOPED_KEY_SECRET" https://example.com/
```

**All gates bind `127.0.0.1`.** There is no public listener, tunnel or router in this archive.
A remote client needs an operator-provided tunnel or ingress; do not assume the base URL is
reachable off the daemon host. The admin API itself may be widened with `AJS_LISTEN` plus a bearer, or served through the
production kit's private admin vhost, but the gate data plane stays loopback.

## Errors

Every failure uses one envelope:

```json
{
  "error": {
    "code": "CONFLICT",
    "message": "catalog changed since it was read",
    "reason": "snapshot_changed",
    "details": { "snapshotId": "sha256:..." }
  }
}
```

`code` is a closed enum: `INVALID_ARGUMENT`, `NOT_FOUND`, `CONFLICT`, `UPSTREAM_UNAVAILABLE`,
`QUOTA_EXCEEDED`, `RATE_LIMITED`, `UNAUTHORIZED`, `FORBIDDEN`, `INVITE_EXPIRED`,
`INVITE_REDEEMED`, `METHOD_NOT_ALLOWED`, `INTERNAL`, `BACKUP_BUSY`, `BACKUP_LIVE_NODE`,
`BACKUP_DAEMON_RUNNING`, `BACKUP_UNAVAILABLE`, `CURSOR_EXPIRED`, `OPERATION_TIMEOUT`,
`PRECONDITION_FAILED`, `PROVIDER_REFUND_UNSUPPORTED`, `CONFIRMATION_REQUIRED`. `FROZEN` maps to
`423` but is reserved: no current route produces it. `reason` is optional; treat an unknown or
missing value as generic.
`details` carries small non-secret context (current `snapshotId`, `operationId`, `ineligibleReason`).

Status mapping and stable reasons:

| Status | Code | Typical `reason` values |
|---|---|---|
| 400 | `INVALID_ARGUMENT` | `invalid_argument`, `INVALID_ARGUMENT` (bad body, missing `confirm`, bad `limit`/window) |
| 401 | `UNAUTHORIZED` | missing/invalid bearer, revoked key |
| 403 | `FORBIDDEN` | `scope_required` (key lacks the route scope), `scoped_key_restricted` (accountIds/regions exclude the target, a lease or `user_id` belongs to another user, or the key predates the account-grant rule) |
| 404 | `NOT_FOUND` | `unknown_server`, `no_target`, unknown account/user/lease/operation; a lease owned by another user answers as unknown |
| 409 | `CONFLICT` / `CONFIRMATION_REQUIRED` | `login_disabled`, `login_required`, `account_state`, `snapshot_changed`, `server_ineligible`, `switch_in_progress`, `slot_multiple_sessions`, `port_unavailable`, `ALREADY_CURRENT`, `SERVER_UNAVAILABLE`, `NO_ALTERNATE_SERVER`, `lease_publish_failed`; `confirmation_required` when a destructive schedule needs `confirm:"ARM"`/`"RUN"` |
| 410 | `CURSOR_EXPIRED` / `INVITE_EXPIRED` | the event cursor fell out of the ring (restart from `oldestCursor`), or an invitation expired before it was redeemed |
| 412 | `PRECONDITION_FAILED` | `stale_revision` on `PATCH /v1/schedules/{id}` (with `details.currentRevision`), `SNAPSHOT_CHANGED` on a waited `select-node`; nothing was mutated |
| 413 | `INVALID_ARGUMENT` | the request body exceeds the route limit |
| 429 | `QUOTA_EXCEEDED` / `RATE_LIMITED` | `account_slots_exhausted`, user quota, `connect_cooldown` (with `Retry-After`), scoped-key rate limit |
| 500 | `INTERNAL` | `store_failed`, unclassified internal failure |
| 502 | `UPSTREAM_UNAVAILABLE` | `session_open_failed`, `gate_start_failed`, `session_unverified` |
| 503 | `UPSTREAM_UNAVAILABLE` | controller stopped, catalog unavailable, `lease_publish_failed`, disabled adapter |
| 504 | `OPERATION_TIMEOUT` | `select-node` wait timeout; read `details.operationId` and poll |

Other useful statuses: `201` (lease created, account created, scoped key created), `202` (async
operation accepted), `204` (lease revoke, scoped-key revoke, account/API-key delete).

## SDKs

Both clients are hand-written against `docs/openapi.json`, dependency-free, and cover accounts,
catalog, select-node operations, leases, usage and events. They send `Authorization: Bearer` and
`X-AJS-CSRF: 1` on unsafe methods, omit `Origin`, retry transport/429/5xx with backoff and honour
`Retry-After`, poll a `202` to a terminal operation, and generate an `Idempotency-Key` for
`CreateLease`. The Go client retries 500/502/503/504 and the TypeScript client every 5xx. They
call the admin API, not the proxy gate.

- Go (`sdk/go`, module `ajssdk`): `ListAccounts`, `CreateAccount`, `GetAccount`, `PatchAccount`,
  `DeleteAccount`, `LoginAccount`, `LogoutAccount`, `ListAccountServers`, `SelectNode`,
  `WaitOperation`, `ListOperations`, `GetOperation`, `CancelOperation`, `ListLeases`,
  `CreateLease`, `RotateLease`, `ReleaseLease`, `GetUsage`, `ListEvents`, `StreamEvents`,
  `FollowEvents`.

```go
client, err := ajs.New("http://127.0.0.1:2261", os.Getenv("AJS_ADMIN_TOKEN"))
accounts, err := client.ListAccounts(ctx, ajs.ListAccountsOptions{State: ajs.AccountActive})
result, err := client.SelectNode(ctx, accounts[0].ID, ajs.SelectNodeRequest{
    Target: &ajs.OperationTarget{Region: "jp-tyo"},
})
lease, err := client.CreateLease(ctx, ajs.CreateLeaseInput{UserID: "u-01", Platform: ajs.PlatformIOS})
```

- TypeScript (`sdk/ts`, package `@ajs/sdk`): the same methods in camelCase, plus `ApiError`,
  `OperationFailure`, `streamEvents()` as an async generator.

```ts
import { Client } from "@ajs/sdk";
const ajs = new Client({ baseUrl: "http://127.0.0.1:2261", token: process.env.AJS_ADMIN_TOKEN! });
const [account] = await ajs.listAccounts({ state: "active" });
const result = await ajs.selectNode(account.id, { target: { region: "jp-tyo" } });
const { lease } = await ajs.createLease({ userId: "u-01", platform: "ios" });
```

Details, error types and the retry policy are in [`sdk/README.md`](../sdk/README.md). Live as of
this archive: neither SDK has been run against a live daemon outside `httptest`/injected `fetch`.

## Honest limits

- The admin API and the gate data plane are separate. A successful API call does not prove vendor
  reachability; the live evidence is one account, one node, one egress address.
- `egressIp` on a node or lease is the vendor's claim. `/v1/health/nodes` measures the loopback
  gate unless the opt-in through-gate egress probe is enabled.
- No public ingress, no real payment drill and no `UseCount=2` in this archive. See
  [`ARCHIVE.md`](../ARCHIVE.md#known-gaps).
