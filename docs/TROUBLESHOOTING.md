# Troubleshooting

Status: current for `v0.1.0-pre.3` (published 2026-10-29). Every row
below names the layer that produced the status, the likely cause, and a read-only command that
distinguishes the cases. No command here prints a secret: credentials stay in environment
variables, and the daemon never returns a key secret after creation.

## First five checks

```bash
# 1. Is the daemon up, and what does its own readiness say?
systemctl status ajs-accountd
curl -fsS http://127.0.0.1:2261/healthz
curl -fsS http://127.0.0.1:2261/readyz

# 2. What did it log? (startup, auth failures, gate starts)
journalctl -u ajs-accountd -n 200 --no-pager

# 3. Is the public edge itself healthy? (static probe, no credential)
curl -sS -o /dev/null -w '%{http_code}\n' https://ajs.dwgx.top:2055/healthz
curl -sS -o /dev/null -w '%{http_code}\n' https://ajs.dwgx.top:2055/docs

# 4. Does the gate answer at all?
curl -sS -m 10 -o /dev/null -w '%{http_code}\n' -x https://gate.dwgx.top:13000 https://example.com/

# 5. Does the lease still name a live port?
curl -sS "$BASE/v1/leases/$LEASE_ID" -H "Authorization: Bearer $SCOPED_KEY" | jq '.lease.status, .connection.port'
```

`BASE` is `http://127.0.0.1:2261` on the daemon host and `https://ajs.dwgx.top:2055` at the edge.
A `401` from the daemon means the credential is wrong for that route; a `403` means the credential
is valid but the route, grant or source is not allowed.

## Symptom table

| Symptom | Layer | Likely cause | First check |
|---|---|---|---|
| `407 Proxy Authentication Required`, realm `apc` | gate (SOCKS5 or HTTP CONNECT) | missing/wrong key, key without `proxy:connect`, revoked key, no active lease, lease on another account, allowlist or quota refusal | `GET /v1/leases/{id}` and the key's scopes |
| `429` with a JSON envelope and `X-RateLimit-*` | daemon | per-key rate limit (`RATE_LIMITED`) | `X-RateLimit-Remaining`, `Retry-After` |
| `429` without a JSON envelope | nginx edge | per-IP request limit (120 r/min, burst 60), 20 connections/IP or 400 total | response headers, `nginx -T` limits |
| `429` with `Retry-After` in seconds until next UTC month | gate | user or account monthly quota exhausted | `GET /v1/usage` |
| `409 CONFLICT` on a lease | daemon | conflicting lease, account state, or a switch in progress | `error.reason` on the response |
| `404` on `/readyz` at `:2055` | nginx edge | expected: the public vhost publishes only a static `/healthz` | probe the daemon or admin origin |
| `401` on `/metrics` at `:2055` | nginx edge → daemon | no credential; the route requires `metrics:read` | send a `metrics:read` scoped key |
| `401` on `/v1/metrics` | daemon | same as above | same |
| `429 QUOTA_EXCEEDED` reason `account_slots_exhausted` | daemon | the account has no free verified serving slot | account `useCount` / `effectiveSlots` |
| lease created with `warning: multi_session_unimplemented` | daemon | display capacity above one; not an error | ignore or read the limitation |
| a revoked key still relays an established flow | gate | the live binary predates the entitlement watch (AZ-01) | binary build, not config |
| `403` on `/v1/...` from the LAN | HMC nginx vhost | source is not `10.99.0.1` or `127.0.0.1` | source address |
| `502 UPSTREAM_UNAVAILABLE` | daemon | vendor session or gate failed to start | `error.reason` and daemon log |
| `504 OPERATION_TIMEOUT` | daemon | a waited operation (usually `select-node`) did not finish | `details.operationId` |

## Detail

### Gate 407

`AJS_GATE_AUTH=userpass` (the default) answers exactly one `407` for every authentication
failure: an unknown user, a wrong secret, a revoked key, a malformed header and a missing lease
are deliberately indistinguishable. Do not read the 407 as "wrong password" — read it as "this
CONNECT was not admitted".

Check, in order:

```bash
# The lease exists, is active, and names the port you dialled.
curl -sS "$BASE/v1/leases/$LEASE_ID" -H "Authorization: Bearer $SCOPED_KEY" \
  | jq '{status: .lease.status, port: .connection.port, user: .lease.userId, account: .lease.accountId}'

# The key carries proxy:connect and grants the lease's account (admin view).
curl -sS "$BASE/v1/scoped-keys/$KEY_ID" -H "Authorization: Bearer $ADMIN_TOKEN" \
  | jq '{scopes, accountIds, regions, revokedAt}'
```

The proxy username is the lease owner's panel user id and the password is a scoped key carrying
`proxy:connect`; the key must be active, belong to that user, and name the lease's account through
`accountIds` or `regions`. A `proxy:connect` key with no grant is refused. A port above `13010`
has no public edge.

### 429: three different limits

| Source | Shape | Meaning |
|---|---|---|
| daemon, per key | JSON `RATE_LIMITED` + `X-RateLimit-Limit/Remaining/Reset` + `Retry-After` | the key's own `rateLimitPerMin` (default 60) is exhausted; wait for `Reset` or raise the limit as an operator |
| nginx edge | plain `429`, no JSON envelope | per-IP request rate (120/min, burst 60), 20 connections/IP, or 400 concurrent connections; back off, do not retry in a tight loop |
| gate | `429` with `Retry-After` = seconds until the next UTC month | user or account monthly quota is exhausted; check `GET /v1/usage` |

`connect_cooldown` is a separate 429 from the connect/switch paths: a real attempt starts a 5 s
cooldown, and the response carries `Retry-After`.

### Lease 409

Read `error.reason`; the status alone does not identify the cause.

| Reason | Meaning | Action |
|---|---|---|
| `lease_conflict` | the user already has an active lease on that platform | reuse or revoke it (`DELETE /v1/leases/{id}` with `{"confirm":true}`) |
| `user_disabled` | the user is suspended | resume the user, or use another |
| `account_unavailable` | the pinned account is paused, failed or expired | pick another account or repair the account |
| `idempotency_conflict` | the `Idempotency-Key` was used for another user or platform | use a fresh key |
| `login_required` / `login_disabled` | the account has no vendor session, or login is off | enable `AJS_ACCOUNT_LOGIN=1` and run `POST /v1/accounts/{id}/actions/login` |
| `switch_in_progress` | another connect/switch holds the account | poll `GET /v1/operations/{id}`; do not re-submit |
| `snapshot_changed` / `stale_revision` | the catalog or schedule changed since it was read | re-read, then retry with the new `snapshotId` / revision |
| `ALREADY_CURRENT` | the selected server is already active | no action |
| `port_unavailable` | the target gate port is already taken on the node | pick another node, or free the gate that holds the port |
| `SERVER_UNAVAILABLE` / `NO_ALTERNATE_SERVER` | the vendor has no usable server for the request | retry later or pin another server |
| `slot_multiple_sessions` | the vendor refuses a second session for the account | one session per account; see the slot rows below |

### `/readyz` is 404 at the public edge

This is by design. The public vhost publishes a static `/healthz` and does not proxy readiness.
Probe readiness where it lives:

```bash
curl -fsS http://127.0.0.1:2261/readyz        # daemon host, or through an SSH tunnel
```

`200 {"status":"ready"}` means the state directory is writable and configured integrations are
up; `503 {"status":"not_ready","reasons":[...]}` names the failing check. Point orchestrator
probes at the daemon or the admin origin, never at `https://ajs.dwgx.top:2055/readyz`.

### `/metrics` or `/v1/metrics` is 401

Both routes require a scoped key carrying `metrics:read`; the edge injects no credential.

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://ajs.dwgx.top:2055/metrics \
  -H "Authorization: Bearer $METRICS_KEY"
```

An unauthenticated `401` is the expected answer, not a routing bug. Since the 2026-10-28
hardening `/metrics` is proxied end to end (it answered `404` before); `/v1/metrics` has always
been routed and authenticated.

### `account_slots_exhausted`

`POST /v1/leases` answers `429 QUOTA_EXCEEDED` with reason `account_slots_exhausted` when every
verified, listener-bound serving slot of the account is already leased. Display capacity can be
higher than the number of live slots: admission counts a slot only while its row is `LIVE` and
its listener is bound, and one slot backs at most one lease.

```bash
curl -sS "$BASE/v1/accounts/$ACCOUNT_ID" -H "Authorization: Bearer $ADMIN_TOKEN" \
  | jq '{useCount, effectiveSlots}'
```

On the live instance the account has `useCount=1`, so `effectiveSlots=1` and only one lease can
be admitted. Revoke the existing lease or use a second account; this is a capacity answer, not a
bug.

### `multi_session_unimplemented` on a lease

This is a **warning field**, not an error: the lease was created. It appears when the account's
display capacity is above one, because live two-slot coexistence is unproven and the warning is
kept for API compatibility. Do not treat it as a refusal, and do not advertise multi-slot
capacity on the strength of it.

### A revoked key still relays an established flow

At this commit the gate revalidates the principal every `EntitlementInterval` (default 10 s)
while a flow relays, and closes both sockets when the key or lease is gone. The **live** binary
`ajs-accountd-61625b0` predates that watch (AZ-01), so on the live host revocation still stops
only new handshakes and pending `CONNECT`s. This is a binary-version gap, not a configuration
gap: redeploy the current build to get the 10 s close. Until then, revoke the lease as well and
treat the old flow as live until it ends.

Do not claim the live binary carries AZ-01; verify the build before repeating either statement.

### `403` from the HMC LAN

The HMC internal vhost proxies `/v1/` only for `10.99.0.1` (the VPS WireGuard peer) and
`127.0.0.1`. A LAN client gets `403`, and that is the intended answer. Reach the admin API over
an SSH tunnel rather than widening the allowlist:

```bash
ssh -N -L 2261:127.0.0.1:2261 hmc &
curl -fsS http://127.0.0.1:2261/healthz
```

The VPS path to the same route answers `401` from the daemon, not `403`: the difference tells you
which hop refused.

### 502 and 504

- `502 UPSTREAM_UNAVAILABLE` with `session_open_failed`, `gate_start_failed` or
  `session_unverified` means the vendor session or the loopback gate could not be started. Check
  the account's login state, whether the node is draining, and the Lab gate view
  (`GET /v1/lab/gates`). A retry is reasonable once; a loop is not.
- `503 UPSTREAM_UNAVAILABLE` with `lease_publish_failed`, "controller stopped" or "catalog
  unavailable" means a local component is not ready. Check `/readyz` and the daemon log.
- `504 OPERATION_TIMEOUT` is a waited operation that did not finish in time, usually
  `select-node`. Read `details.operationId`, then poll the operation; do not re-submit the switch:

```bash
# GET /v1/operations/{id} needs catalog:read (or an admin bearer).
curl -sS "$BASE/v1/operations/$OPERATION_ID" -H "Authorization: Bearer $SCOPED_KEY" \
  | jq '{state, error, result}'
```

### Nothing in the log

`journalctl -u ajs-accountd` reads the unit's journal. If the daemon was started by hand, its
output went to that terminal instead. The daemon logs account login, gate start, connect/switch,
webhook delivery and backup events; a request rejected at the nginx edge (404 allowlist, 429
limit) never reaches the daemon, so check the nginx error log on the edge host as well.

## Pointers

- [`docs/OPERATIONS.md`](OPERATIONS.md) — health probes, logs, backup, rotation, rollback
- [`docs/API-GUIDE.md`](API-GUIDE.md#errors) — the full status/reason table
- [`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md) — why the edge allowlist and the gate answer differently
- [`docs/ADMIN-API.md`](ADMIN-API.md) — operator routes and their credentials
