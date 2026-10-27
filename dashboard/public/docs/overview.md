# Overview and quickstart

> Read the catalog, create a lease for one account, and connect through the loopback gate.

English mirror of this page; the console page carries the Chinese translation.

## Base URL and health

The control plane listens on `http://127.0.0.1:2261` by default (`AJS_LISTEN`). `GET /healthz` and `GET /readyz` are unauthenticated; every other route, including `/metrics`, needs a bearer.

> **Loopback by default** — The API and the gate data plane are separate. This archive has no public proxy endpoint and no public ingress; a remote client needs an operator-provided tunnel or ingress, which is pending.

**Health without a token**

```bash
curl -sS http://127.0.0.1:2261/healthz
curl -sS http://127.0.0.1:2261/readyz
```

## The story used by these examples

One user, one account, one node, one gate: `u-01`, `acc-02`, `node-04`, port `13003`. The values are illustrative and match the examples in the OpenAPI contract; substitute your own ids. The scoped key is assumed to exist already: an operator mints it in Settings → Access, and key management is not part of the scoped reference.

| Variable | Value | Meaning |
| --- | --- | --- |
| `AJS_API_KEY` | `ajs_sk_...` | The scoped secret, shown once when the key is minted. |
| `BASE` | `http://127.0.0.1:2261` | Control-plane origin. |
| `u-01` | Lease owner | The key subject; a non-admin key only sees its own leases. |
| `acc-02` | Account grant | The account the key names in `accountIds`. |
| `node-04` | Live node | A `LIVE` node on `acc-02`; its gate is the port a lease points at. |
| `13003` | Gate port | Loopback SOCKS5 listener for that node. |

## 1. Know what the key can do

A key for this walkthrough carries `catalog:read`, `leases:read`, `leases:write`, `proxy:connect` and `usage:read`, is bound to `u-01`, and names `acc-02` in `accountIds`. The secret is returned exactly once; only its SHA-256 is persisted, so store it in `$AJS_API_KEY` and never commit it.

**Shell setup**

```bash
export BASE=http://127.0.0.1:2261
export AJS_API_KEY=ajs_sk_...   # shown once by the key-management surface
```

Every request below sends `Authorization: Bearer $AJS_API_KEY`. Unsafe methods also send `X-AJS-CSRF: 1`.

## 2. Find a live node

`GET /v1/nodes` needs `catalog:read`. Filter by `state=LIVE` and `account_id=acc-02`; the row for `node-04` carries the `gatePort` a lease will point at. `limit` and `offset` page the result.

**List live nodes**

```bash
curl -sS "$BASE/v1/nodes?state=LIVE&account_id=acc-02&limit=50&offset=0" \
  -H "Authorization: Bearer $AJS_API_KEY"
```

**The row to keep**

```json
{
  "id": "node-04",
  "accountId": "acc-02",
  "region": "jp-tyo",
  "state": "LIVE",
  "gatePort": 13003,
  "protocol": "socks5"
}
```

## 3. Create a lease

`POST /v1/leases` needs `leases:write`. A non-admin key must name an `accountId` inside its grant, and the key subject fills `userId`; a different `userId` is refused with `403` before anything is allocated. `ttlSeconds` accepts `0` for the sticky TTL, `60` or more up to `604800`; one active lease per user and platform is allowed, so a second create answers `409`.

Send an `Idempotency-Key` (up to 128 characters) so a retry replays the original lease instead of allocating another one. The replay is re-checked against both the key subject and the account grant.

**Create a one-hour lease**

```bash
curl -sS -X POST "$BASE/v1/leases" \
  -H "Authorization: Bearer $AJS_API_KEY" \
  -H "X-AJS-CSRF: 1" \
  -H "Idempotency-Key: $(openssl rand -hex 16)" \
  -H "Content-Type: application/json" \
  -d '{"userId":"u-01","platform":"ios","accountId":"acc-02","ttlSeconds":3600}'
```

**201 Created**

```json
{
  "lease": {
    "id": "lease-09",
    "userId": "u-01",
    "accountId": "acc-02",
    "nodeId": "node-04",
    "protocol": "socks5",
    "gatePort": 13003,
    "expiresAt": "2026-10-27T17:15:00Z",
    "status": "active"
  },
  "replayed": false
}
```

## 4. Read the connection block

`GET /v1/leases/{id}` needs `leases:read` and returns the lease plus the loopback endpoint to dial. `port` follows the lease: a switch that rebinds it to another gate also moves the value. The secret is never returned — `credentialKind` only names the shape, and the password is the caller’s own key carrying `proxy:connect`.

**Read the lease**

```bash
curl -sS "$BASE/v1/leases/$LEASE_ID" \
  -H "Authorization: Bearer $AJS_API_KEY"
```

**Lease plus connection**

```json
{
  "lease": {
    "id": "lease-09",
    "userId": "u-01",
    "accountId": "acc-02",
    "nodeId": "node-04",
    "gatePort": 13003,
    "status": "active"
  },
  "connection": {
    "host": "127.0.0.1",
    "port": 13003,
    "protocol": "socks5",
    "username": "u-01",
    "credentialKind": "ajs_sk"
  }
}
```

## 5. Dial the gate

Dial `127.0.0.1:<gatePort>` with SOCKS5. The username is the panel user id; the password is the scoped secret carrying `proxy:connect`. The gate re-checks the lease at `CONNECT`, so a revoked or expired lease refuses the flow instead of relaying it, and it also enforces the user’s allowlist and monthly quota.

**SOCKS5 through the gate**

```bash
curl --socks5-hostname 127.0.0.1:13003 \
  -U "u-01:$AJS_API_KEY" \
  https://example.com/
```

> **The gate is not a public proxy** — All gates bind `127.0.0.1`. There is no public listener, tunnel or router in this archive; a client on another host needs an operator-provided tunnel or ingress (pending). Do not assume the gate port is reachable off the daemon host.

## 6. Read usage

`GET /v1/usage` needs `usage:read`. A non-admin key is served a summary over its own request rows only, filtered before aggregation; naming another `userId` answers `403`. The log stores a recyclable account label rather than a stable account id, so a scoped `account=` filter answers `400 INVALID_ARGUMENT` for every value — do not send it.

**Usage window**

```bash
curl -sS "$BASE/v1/usage?from=2026-10-01&to=2026-10-31" \
  -H "Authorization: Bearer $AJS_API_KEY"
```

A bare date is UTC midnight (`from`) or the end of that day (`to`); an empty `to` means now, an empty `from` means 30 days before `to`, and the widest window is 366 days. The response carries `requests`, `bytes`, `errors`, `accounts[]`, `users[]`, `days[]`, `topTargets[]`, `truncated`, `rowsRetained` (10,000) and `retentionDays`.
