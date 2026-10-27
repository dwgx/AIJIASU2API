# Overview and quickstart

> Read the catalog, create a lease for one account, and connect through the public HTTPS CONNECT edge.

English mirror of this page; the console page carries the Chinese translation.

## Base URL and health

The public scoped-key edge is `https://api.example.com` — a deployment placeholder from the production kit, not a host this archive deployed; replace it with the deployment’s own API origin. It carries only the scoped-key allowlist and answers `404` for every other path. Same-host clients use the loopback control plane at `http://127.0.0.1:2261` (`AJS_LISTEN`), which stays the full contract. `GET /healthz` and `GET /readyz` are unauthenticated; every other route, including `/metrics`, needs a bearer.

> **Public edges ship in the kit, not deployed live** — The API edge and the TLS HTTPS CONNECT proxy are public in the production kit; only the SOCKS5 gate stays loopback. Neither public edge has been deployed live, and `api.example.com` must be replaced before use.

**Health without a token**

```bash
curl -sS https://api.example.com/healthz
curl -sS http://127.0.0.1:2261/healthz
curl -sS http://127.0.0.1:2261/readyz
```

At the edge `/healthz` is a static `200 "ok"`; `/readyz` and the rest of the control plane stay on the loopback origin.

## The story used by these examples

One user, one account, one node, one gate: `u-01`, `acc-02`, `node-04`, port `13003`. The values are illustrative and match the examples in the OpenAPI contract; substitute your own ids. In the shipped static map the same port is the loopback SOCKS5 listener and the public HTTPS CONNECT port. The scoped key is assumed to exist already: an operator mints it in Settings → Access, and key management is not part of the scoped reference.

| Variable | Value | Meaning |
| --- | --- | --- |
| `AJS_API_KEY` | `ajs_sk_...` | The scoped secret, shown once when the key is minted. |
| `BASE` | `https://api.example.com` | Public scoped-key edge; a deployment placeholder to replace. |
| `AJS_PROXY_PORT` | `13003` | `connection.port` from `GET /v1/leases/{id}`: the public HTTPS CONNECT port. |
| `u-01` | Lease owner | The key subject; a non-admin key only sees its own leases. |
| `acc-02` | Account grant | The account the key names in `accountIds`. |
| `node-04` | Live node | A `LIVE` node on `acc-02`; its gate port is what the lease and the public edge share. |

## 1. Know what the key can do

There is no self-service: a customer cannot sign up, mint, widen or re-scope a key. An operator creates the `ajs_sk_` in the dashboard under Settings → Access, sets its scopes and its `accountIds`/`regions` grants, and distributes it once. The public edge only carries keys the operator already created.

A key for this walkthrough carries `catalog:read`, `leases:read`, `leases:write`, `proxy:connect` and `usage:read`, is bound to `u-01`, and names `acc-02` in `accountIds`. The secret is returned exactly once, to the operator; only its SHA-256 is persisted, so store it in `$AJS_API_KEY` and never commit it.

**Shell setup**

```bash
export BASE=https://api.example.com   # deployment placeholder; replace it
export AJS_API_KEY=ajs_sk_...         # operator-issued, shown once
```

Every request below sends `Authorization: Bearer $AJS_API_KEY`. Unsafe methods also send `X-AJS-CSRF: 1`.

## 2. Find a live node

`GET /v1/nodes` needs `catalog:read`. Filter by `state=LIVE` and `account_id=acc-02`; the row for `node-04` carries the `gatePort` a lease will point at, and in the shipped static map that number is also the public HTTPS CONNECT port. `limit` and `offset` page the result.

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

`GET /v1/leases/{id}` needs `leases:read` and returns the lease plus its `connection` block. `port` follows the lease: a switch that rebinds it to another gate also moves the value. On the daemon host `host` is `127.0.0.1` and `protocol` is `socks5` — that is the loopback listener — while in the production kit’s static map (13000-13010) the same `port` is the public HTTPS CONNECT port; a port above 13010 has no public edge. The secret is never returned — `credentialKind` only names the shape, and the password is the caller’s own key carrying `proxy:connect`.

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

## 5. Dial the public HTTPS CONNECT edge

Read `connection.port` and dial the public edge with an HTTPS proxy that speaks `CONNECT`: `https://api.example.com:<port>`. Admission is the same pipeline as the loopback gate: the gate re-checks the lease at `CONNECT`, so a revoked or expired lease refuses the flow instead of relaying it, and it also enforces the user’s allowlist and monthly quota.

- Port: `connection.port` from `GET /v1/leases/{id}`, in the shipped static map `13000-13010`. A switch that rebinds the lease moves the port with it; a port above `13010` has no public edge.
- User: the lease owner’s panel user id, for example `u-01`.
- Password: a scoped key secret (`ajs_sk_...`) carrying `proxy:connect` and granting the lease’s account.
- Transport: TLS. The client dials `https://api.example.com:<port>` and speaks HTTP `CONNECT`; nginx terminates TLS and relays bytes without parsing or rewriting the request, so the credential never lands in an edge log.

**HTTPS CONNECT through the public edge**

```bash
export AJS_PROXY_PORT=13003   # connection.port from GET /v1/leases/{id}
curl -x "https://u-01:$AJS_API_KEY@api.example.com:$AJS_PROXY_PORT" https://example.com
```

> **SOCKS5 stays loopback** — The SOCKS5 gate binds `127.0.0.1:13000-13010` and is never published; a SOCKS5 client on another host still needs an operator-provided tunnel. The HTTPS CONNECT edge ships in the production kit but has not been deployed live, and `api.example.com` must be replaced first.

## 6. Read usage

`GET /v1/usage` needs `usage:read`. A non-admin key is served a summary over its own request rows only, filtered before aggregation; naming another `userId` answers `403`. The log stores a recyclable account label rather than a stable account id, so a scoped `account=` filter answers `400 INVALID_ARGUMENT` for every value — do not send it.

**Usage window**

```bash
curl -sS "$BASE/v1/usage?from=2026-10-01&to=2026-10-31" \
  -H "Authorization: Bearer $AJS_API_KEY"
```

A bare date is UTC midnight (`from`) or the end of that day (`to`); an empty `to` means now, an empty `from` means 30 days before `to`, and the widest window is 366 days. The response carries `requests`, `bytes`, `errors`, `accounts[]`, `users[]`, `days[]`, `topTargets[]`, `truncated`, `rowsRetained` (10,000) and `retentionDays`.
