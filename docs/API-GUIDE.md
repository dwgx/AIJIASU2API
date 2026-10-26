# API guide — AJiaSu → API

Webshare-style quickstart for the retained `ajs-accountd` API: accounts, catalog, connect/switch,
leases, nodes, requests/usage, topology and health, authenticated by an admin bearer token or an
`ajs_sk_` scoped key. The full contract is [`docs/openapi.json`](openapi.json); this page is the
short path.

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
| Scoped key | `ajs_sk_...`, returned once by `POST /v1/scoped-keys` | Only the routes its scopes name, optionally narrowed to accounts/regions |

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
    "userId": "usr-1",
    "label": "monitor",
    "scopes": ["accounts:read", "catalog:read", "leases:read", "health:read"],
    "accountIds": ["acc-07"],
    "regions": ["cn"],
    "rateLimitPerMin": 60
  }'
```

The response is `{"key": {...}, "secret": "ajs_sk_..."}`. The secret is shown exactly once; only
its SHA-256 is persisted. Revoke with `DELETE /v1/scoped-keys/{id}` (204), also admin-only.

## Scopes

A scoped key needs the exact scope of the matched route. A key carrying `admin` bypasses the
table, and the admin token bypasses it by definition. Routes absent from the table are admin-only.

| Scope | Grants | Representative routes |
|---|---|---|
| `accounts:read` | Read accounts and account IO capabilities/device | `GET /v1/accounts`, `GET /v1/accounts/{id}`, `GET /v1/accounts/{id}/device` |
| `accounts:write` | Create/patch/delete accounts; login/logout | `POST /v1/accounts`, `PATCH /v1/accounts/{id}`, `POST /v1/accounts/{id}/actions/login` |
| `catalog:read` | Read catalog, nodes, operations, topology, debug trace | `GET /v1/accounts/{id}/servers`, `GET /v1/nodes`, `GET /v1/operations`, `GET /v1/topology` |
| `catalog:write` | Selection and guarded switch; node drain/probe; operation cancel | `PUT /v1/accounts/{id}/selection`, `POST /v1/accounts/{id}/actions/connect`, `POST .../select-node`, `POST /v1/nodes/{id}/actions/drain` |
| `leases:read` | List leases | `GET /v1/leases` |
| `leases:write` | Create/rotate/revoke leases | `POST /v1/leases`, `POST /v1/leases/{id}/actions/rotate`, `DELETE /v1/leases/{id}` |
| `requests:read` | Read the request log | `GET /v1/requests` |
| `audit:read` | Read and export the audit log | `GET /v1/audit`, `GET /v1/audit/export.csv` |
| `metrics:read` | Prometheus and JSON metrics | `GET /metrics`, `GET /v1/metrics` |
| `lab:read` | Lab telemetry and the guarded egress check | `GET /v1/lab/*`, `POST /v1/lab/egress-check` |
| `settings:read` | Read settings and schedules | `GET /v1/settings`, `GET /v1/schedules` |
| `settings:write` | Patch settings and schedules | `PATCH /v1/settings`, `POST /v1/schedules` |
| `usage:read` | Usage aggregation | `GET /v1/usage` |
| `renewal:read` | Renewal plan table and per-account expiry | `GET /v1/renewal/plans`, `GET /v1/renewal/accounts/{id}` |
| `health:read` | Loopback gate health | `GET /v1/health/nodes` |
| `events:read` | Event replay and SSE stream | `GET /v1/events`, `GET /v1/events/stream` |
| `admin` | Every route, including admin-only surfaces | all |

Admin-only even with a scoped key: account export/import/bulk-delete, device pool, users, API
keys, scoped-key management, webhooks, backup/inspect/restore, plans/orders, tenant
administration, reconciliation, `GET /v1/whoami`, `GET /v1/openapi.json` and
`GET /v1/system/update`. The reason is in
[`daemon/internal/api/scoped_keys.go`](../daemon/internal/api/scoped_keys.go): a filtered export
still discloses credentials, a filtered bulk-delete is destructive with partial success, and a
customer key has no business enumerating the daemon's outbound receivers or sealed files.

## Narrowing with `accountIds` / `regions`

Both lists are optional and only affect **reads**:

- Empty `accountIds` and empty `regions` = unrestricted.
- An account matches when the id list is empty or contains its id, **and** the region list is
  empty or overlaps the account's `regions`.
- The filter covers the account path, the renewal account path and the `account_id`/`accountId`
  query on nodes, leases and operations, so a restricted key cannot read one account through a
  different account-scoped route.
- A GET for an outside account answers `403 FORBIDDEN` with `reason: "scoped_key_restricted"`.
  `POST .../select-node` answers `404 NOT_FOUND` instead, so a key cannot probe whether an account
  exists.
- Global, unattributed surfaces are refused to a restricted key rather than guessed:
  `GET /v1/requests`, `GET /v1/usage`, `GET /v1/audit`, `GET /metrics` and the event stream.
  `/v1/topology` is projected to the key's visible nodes, and `/v1/health/nodes` is filtered.
- **Writes are governed by scopes, not by these lists.** A key with `catalog:write` or
  `leases:write` can act on every account. Issue write scopes deliberately.

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
curl -sS "$BASE/v1/accounts/acc-07/servers?region=cn&limit=50&offset=0&sort=order" \
  -H "Authorization: Bearer $AJS_TOKEN"
```

Returns one page plus `snapshotId`, `fetchedAt`, `stale`, `total`, `nextOffset`, `hasMore`,
`regions` and `servers[]` (`id`, `title`, `region`, `host`, `port`, `status`, `nodeId`,
`nodeState`, `selected`, `blockingMarks`, `latencyMs`). `q`, `region`, `status`, `province`,
`city`, `selected`, `offset`, `limit` (1–500, default 50) and `sort` (`order`, `title`, `host`,
`status`, `region`) are accepted. Echo `snapshotId` on connect/selection so a changed catalog is
rejected instead of silently switching to a different server.

### Connect or switch

```bash
curl -sS -X POST "$BASE/v1/accounts/acc-07/actions/connect" \
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
curl -sS -X POST "$BASE/v1/accounts/acc-07/actions/select-node" \
  -H "Authorization: Bearer $AJS_TOKEN" \
  -H "X-AJS-CSRF: 1" \
  -H "Content-Type: application/json" \
  -d '{"target":{"region":"cn"},"wait":true,"timeoutSeconds":60,"ifCurrent":"noop"}'
```

`target` accepts `serverId`, `nodeId`, `region` and `strategy` (`next`, `first`). A `200` means the
target was already current (`changed:false`) or the switch completed; `202` returns an operation
with `id` and `state` (`queued`, `running`, `succeeded`, `failed`, `canceled`). Poll
`GET /v1/operations/{id}` (or let the SDK poll). A `wait:true` timeout answers `504
OPERATION_TIMEOUT` with `details.operationId`.

### Leases

```bash
# List, optionally for one user
curl -sS "$BASE/v1/leases?user_id=usr-1&limit=50&offset=0" \
  -H "Authorization: Bearer $AJS_TOKEN"

# Create (idempotent)
curl -sS -X POST "$BASE/v1/leases" \
  -H "Authorization: Bearer $AJS_TOKEN" \
  -H "X-AJS-CSRF: 1" \
  -H "Idempotency-Key: $(openssl rand -hex 16)" \
  -H "Content-Type: application/json" \
  -d '{"userId":"usr-1","platform":"linux","ttlSeconds":3600}'

# Revoke (explicit confirmation)
curl -sS -X DELETE "$BASE/v1/leases/$LEASE_ID" \
  -H "Authorization: Bearer $AJS_TOKEN" \
  -H "X-AJS-CSRF: 1" \
  -H "Content-Type: application/json" \
  -d '{"confirm":true}'
```

Create answers `201 {"lease": {...}, "warning"?: "...", "replayed"?: true}`. `accountId` is an
optional pin (must be active and match the platform); `ttlSeconds` is 60–604800, and `0`/absent
uses the user's sticky TTL then the panel lease TTL. One active lease per user and platform; a
second is `409`, an exhausted quota or account slot is `429`. An `Idempotency-Key` (header or body
`idempotencyKey`, max 128 characters, both must agree) replays the original lease. Revoke answers
`204`. A lease on an account whose display capacity is above one carries
`warning: "multi_session_unimplemented"`; the live account has `useCount=1`, so this archive admits
one slot.

### Nodes

```bash
curl -sS "$BASE/v1/nodes?state=LIVE&account_id=acc-07&limit=50&offset=0" \
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

curl -sS "$BASE/v1/usage?from=2026-10-01&to=2026-10-31&account=acc-07" \
  -H "Authorization: Bearer $AJS_TOKEN"
```

`/v1/requests` returns newest-first `RequestLog` rows: `id`, `ts`, `userId`, `userName`,
`accountLabel`, `nodeLabel`, `region`, `method`, `target`, `status`, `bytes`, `latencyMs`,
`source` (`proxy`, `mutation-lab`, `gate`). The store caps history at 10,000 rows; a restricted key
is refused this surface.

`/v1/usage` accepts RFC 3339 timestamps or bare UTC dates (a bare `from` is 00:00:00Z, a bare `to`
is the end of that day). An empty `to` defaults to now, an empty `from` to 30 days before `to`, and
the widest window is 366 days.
The response carries `from`, `to`, `requests`, `bytes`, `errors`, `accounts[]`, `users[]`,
`days[]`, `topTargets[]`, `truncated`, `rowsRetained` (10,000), `retentionDays` and
`quotaEnforced`. `truncated:true` means the row cap clipped the window. A restricted key is refused
this surface too, because the log stores a recyclable account label rather than a stable id.

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
    "userId": "usr-1",
    "accountId": "acc-07",
    "nodeId": "acc-07:<server-id>",
    "protocol": "socks5",
    "gatePort": 13012,
    "egressIp": "<vendor-claimed-egress>",
    "expiresAt": "2026-10-27T12:00:00Z",
    "status": "active"
  }
}
```

The example values are illustrative; read the current `nodeId`, `gatePort` and `egressIp` from the
lease you created.

Dial `127.0.0.1:<gatePort>` with SOCKS5. With the default `AJS_GATE_AUTH=userpass`, the username is
the panel user id and the password is an **active API-key secret** (created through `/v1/apikeys`;
scoped keys are for the admin API, not the gate). The gate enforces that user's allowlist and
monthly quota before admitting a flow:

```bash
curl --socks5-hostname 127.0.0.1:13012 -U "usr-1:$API_KEY_SECRET" https://example.com/
```

`AJS_GATE_HTTP=1` adds an HTTP CONNECT listener at `gatePort + AJS_GATE_HTTP_OFFSET` (default
offset 1000) with the same admission pipeline and Basic credentials:

```bash
curl -x http://127.0.0.1:14012 -U "usr-1:$API_KEY_SECRET" https://example.com/
```

**All gates bind `127.0.0.1`.** There is no public listener, tunnel or router in this archive, and
the experimental `AJS_REMOTE_INGRESS` credential store is not wired to one. A remote client needs
an operator-provided tunnel or ingress; do not assume the base URL is reachable off the daemon
host. The admin API itself may be widened with `AJS_LISTEN` plus a bearer, or served through the
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
`QUOTA_EXCEEDED`, `RATE_LIMITED`, `UNAUTHORIZED`, `FORBIDDEN`, `METHOD_NOT_ALLOWED`, `INTERNAL`,
`BACKUP_BUSY`, `BACKUP_LIVE_NODE`, `BACKUP_DAEMON_RUNNING`, `BACKUP_UNAVAILABLE`,
`CURSOR_EXPIRED`, `OPERATION_TIMEOUT`, `FROZEN`, `PRECONDITION_FAILED`,
`PROVIDER_REFUND_UNSUPPORTED`. `reason` is optional; treat an unknown or missing value as generic.
`details` carries small non-secret context (current `snapshotId`, `operationId`, `ineligibleReason`).

Status mapping and stable reasons:

| Status | Code | Typical `reason` values |
|---|---|---|
| 400 | `INVALID_ARGUMENT` | `invalid_argument`, `INVALID_ARGUMENT` (bad body, missing `confirm`, bad `limit`/window) |
| 401 | `UNAUTHORIZED` | missing/invalid bearer, revoked key |
| 403 | `FORBIDDEN` | `scope_required` (key lacks the route scope), `scoped_key_restricted` (accountIds/regions exclude the read) |
| 404 | `NOT_FOUND` | `unknown_server`, `no_target`, unknown account/user/lease/operation |
| 409 | `CONFLICT` | `login_disabled`, `login_required`, `account_state`, `snapshot_changed`, `server_ineligible`, `switch_in_progress`, `slot_multiple_sessions`, `port_unavailable`, `ALREADY_CURRENT`, `SERVER_UNAVAILABLE`, `NO_ALTERNATE_SERVER`, `lease_publish_failed` |
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
`CreateLease`. They call the admin API, not the proxy gate.

- Go (`sdk/go`, module `ajssdk`): `ListAccounts`, `CreateAccount`, `GetAccount`, `PatchAccount`,
  `DeleteAccount`, `LoginAccount`, `LogoutAccount`, `ListAccountServers`, `SelectNode`,
  `WaitOperation`, `ListOperations`, `GetOperation`, `CancelOperation`, `ListLeases`,
  `CreateLease`, `RotateLease`, `ReleaseLease`, `GetUsage`, `ListEvents`, `StreamEvents`,
  `FollowEvents`.

```go
client, err := ajs.New("http://127.0.0.1:2261", os.Getenv("AJS_ADMIN_TOKEN"))
accounts, err := client.ListAccounts(ctx, ajs.ListAccountsOptions{State: ajs.AccountActive})
result, err := client.SelectNode(ctx, accounts[0].ID, ajs.SelectNodeRequest{
    Target: &ajs.OperationTarget{Region: "cn"},
})
lease, err := client.CreateLease(ctx, ajs.CreateLeaseInput{UserID: "usr-1", Platform: ajs.PlatformLinux})
```

- TypeScript (`sdk/ts`, package `@ajs/sdk`): the same methods in camelCase, plus `ApiError`,
  `OperationFailure`, `streamEvents()` as an async generator.

```ts
import { Client } from "@ajs/sdk";
const ajs = new Client({ baseUrl: "http://127.0.0.1:2261", token: process.env.AJS_ADMIN_TOKEN! });
const [account] = await ajs.listAccounts({ state: "active" });
const result = await ajs.selectNode(account.id, { target: { region: "cn" } });
const { lease } = await ajs.createLease({ userId: "usr-1", platform: "linux" });
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
