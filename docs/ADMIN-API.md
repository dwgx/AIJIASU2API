# Admin API

Status: current for `v0.1.0-pre.3` (release prep, 2026-10-29; tag not yet created). Audience:
operators. This page is a map of the operator-only surface and the rules that apply to it. The
generated reference is authoritative for request and response schemas; this page does not repeat
them.

## What "admin" means here

`ajs-accountd` serves one HTTP API. There is no separate admin listener: the difference is the
credential and the route-scope table.

- An **admin bearer** reaches every route: the shared `AJS_ADMIN_TOKEN` in compatibility mode,
  or an operator token whose role is `admin`. A `viewer` or `operator` token reaches only the
  capabilities its role grants (see [`docs/RBAC.md`](RBAC.md)).
- A **scoped key** (`ajs_sk_…`) reaches only the routes whose exact scope it holds. A key that
  carries the `admin` scope reaches the admin routes too; that bypass is intentional.
- A route with no entry in the daemon's route-scope table is refused to a non-admin key before
  the handler runs.

The counts, derived from the contract rather than hand-maintained:

```bash
python -c "import json;d=json.load(open('docs/openapi.json'));ops=[(p,m,o) for p,i in d['paths'].items() for m,o in i.items() if m in ('get','post','put','patch','delete')];u=[(p,m) for p,m,o in ops if 'x-required-scope' not in o];print(len(ops),len(u),len([x for x in u if x[0] not in ('/healthz','/readyz')]))"
# -> 148 94 92
```

So: **148 operations**, **54** carry `x-required-scope` (the public allowlist), **94** do not,
and two of those 94 are the unauthenticated `/healthz` and `/readyz` probes — **92
operator-only operations**. Re-run the command on the tag you deploy; do not quote this paragraph
as a count for a later build.

## Authentication and request rules

| Rule | Detail |
|---|---|
| Header | `Authorization: Bearer <admin token or ajs_sk_…>`; no query-string credential |
| CSRF | every unsafe method (`POST`, `PUT`, `PATCH`, `DELETE`) also needs `X-AJS-CSRF: 1` |
| Origin | a request that carries `Origin` must match `AJS_ADMIN_ORIGINS` exactly; CLI and SDK clients omit it |
| Strict RBAC | with `AJS_RBAC_STRICT=1` the shared `AJS_ADMIN_TOKEN` is retired and operator tokens are hashed in `/etc/ajs/admin-operators.hashes`; role/token changes need an nginx reload **and** a daemon restart |
| Compatibility | without strict RBAC the legacy shared bearer is still accepted; the live host is in this mode, which is a documented gap, not a recommendation |
| Rate limits | scoped-key calls are limited per key (default 60/min) and answer `X-RateLimit-*`; an exhausted bucket is `429 RATE_LIMITED` with `Retry-After` |
| Lab switch | `AJS_ALLOW_INSECURE=1` disables auth on a loopback bind only; startup refuses it elsewhere |

The API is not an authorization boundary for the browser: the dashboard hides controls, but the
daemon's answer is authoritative.

## The operator-only surface, by group

Representative paths are given; the full list is in the generated reference. "Scoped key" says
whether a non-admin key can reach the group at all.

| Group | Representative paths | Scoped key | Notes |
|---|---|---|---|
| Auth | `GET /v1/whoami` | no | identity echo for the presented credential; not routed at the public edge |
| Accounts | `/v1/accounts`, `/v1/accounts/{id}/actions/login`, `.../actions/logout`; `POST /v1/accounts/export`, `/import`, `/bulk-delete` | partly (`accounts:read` / `accounts:write`) | the three bulk operations are admin-only; login is opt-in with `AJS_ACCOUNT_LOGIN=1` |
| Catalog, nodes, operations | `/v1/accounts/{id}/servers`, `/selection`, `/actions/connect`, `/disconnect`, `/reconnect`, `/select-node`; `/v1/nodes`, `/v1/nodes/{id}/actions/drain`, `/undrain`, `/probe`; `/v1/operations`, `/v1/operations/{id}` | partly (`catalog:read` / `catalog:write`) | `select-node` can return `202` plus an operation id; poll the operation, do not re-submit |
| Leases | `/v1/leases`, `/v1/leases/{id}`, `/v1/leases/{id}/actions/rotate` | partly (`leases:read` / `leases:write`) | revoke takes `{"confirm":true}`; rotate is a Resin action and answers `503` with no panel configured |
| Users and keys | `/v1/users`, `/v1/users/{id}/allowlist`, `.../suspend`, `.../resume`; `/v1/scoped-keys`; `/v1/apikeys`; `/v1/device-pool` | no | scoped keys are minted here and shown once; `apikeys` is the migration-era shape; the device pool backs vendor device identities |
| Usage, requests, audit, events | `/v1/usage`, `/v1/requests`, `/v1/audit`, `/v1/audit/export.csv`, `/v1/events`, `/v1/events/stream` | partly (`usage:read`, `requests:read`, `audit:read`, `events:read`) | audit and the event stream are operator-global: a non-admin key gets `403 admin_required` whatever its grant; `POST /v1/requests/mutations` is admin-only |
| Settings, backup | `/v1/settings`, `POST /v1/backup`, `/backup/inspect`, `/backup/restore` | partly (`settings:read` / `settings:write`) | backup and restore are admin-only; restore needs the daemon stopped, `dryRun:false` and `confirm:"RESTORE"`, and is refused while any node is `LIVE` |
| Schedules, webhooks | `/v1/schedules`, `/v1/schedules/{id}/runs`, `/v1/schedules/{id}/actions/run`; `/v1/webhooks`, `/v1/webhooks/{id}/deliveries` | partly (`settings:read` / `settings:write`) | destructive schedules need `confirm:"ARM"` / `"RUN"` and answer `409 CONFIRMATION_REQUIRED` without it; `PATCH` uses `stale_revision` (`412`) |
| Health, metrics | `GET /healthz`, `GET /readyz`, `GET /v1/health/nodes`, `GET /metrics`, `GET /v1/metrics` | `health:read` / `metrics:read` | `/healthz` and `/readyz` are unauthenticated; `/metrics` and `/v1/metrics` need a `metrics:read` key |
| System | `GET /v1/openapi.json`, `GET /v1/system/update`, `POST /v1/reconcile` | no | contract self-description, update check, Resin reconcile trigger |
| Lab | `/v1/lab/overview`, `/gates`, `/flows`, `/sessions`, `/egress`, `/logs`, `/diagnostics` | no | read-only diagnostics plus one egress probe; values are redacted |

Experimental and disabled in this release — present in the contract, unregistered with
`AJS_TENANT_PORTAL=0`, and never routed at the public edge: `/v1/portal/*` (19 operations),
`/v1/admin/plans*`, `/v1/admin/orders*` (13), `/v1/admin/tenants*` (6), and the payment callbacks
under `/v1/payments/*`. See [`docs/EPAY-STATUS.md`](EPAY-STATUS.md).

## Destructive operations

| Operation | Confirmation |
|---|---|
| `DELETE /v1/leases/{id}` | body `{"confirm": true}` |
| `POST /v1/accounts/{id}/actions/connect` / `disconnect` / `reconnect` | body `confirm: true` |
| `POST /v1/schedules/{id}/actions/run` | `confirm:"RUN"`; arming a destructive schedule uses `confirm:"ARM"` |
| `POST /v1/backup/restore` | `dryRun:false` plus `confirm:"RESTORE"`, daemon stopped, no `LIVE` node |
| `POST /v1/accounts/bulk-delete`, `POST /v1/device-pool/{id}/purge` | explicit body; read the operation's schema in the reference first |

A missing or wrong confirmation is `400 INVALID_ARGUMENT` or `409 CONFIRMATION_REQUIRED` and
mutates nothing.

## Errors

One envelope for every failure: `{"error": {"code", "message", "reason"?, "details"?}}`. `code`
is a closed enum; `reason` is the stable string to branch on. Status mapping, reasons, and the
retry rules are in [`docs/API-GUIDE.md`](API-GUIDE.md#errors) and
[`docs/TROUBLESHOOTING.md`](TROUBLESHOOTING.md).

## Where it is reachable

The admin API binds `127.0.0.1:2261`. The public edge (`https://ajs.dwgx.top:2055`) serves only
the 54 scoped operations on 41 allowlisted paths and ends in a terminal `404`; admin, portal,
payment, `/v1/openapi.json` and `/v1/whoami` are refused there. On the live host the HMC internal
vhost proxies the whole `/v1/` namespace to the daemon, but only for `10.99.0.1` and
`127.0.0.1` — a LAN caller gets `403`. Reach the admin API through an SSH tunnel to
`127.0.0.1:2261` or the kit's private admin vhost on `127.0.0.1:8443`.

## Reference

- Generated reference, live: <https://ajs.dwgx.top:2055/docs> (public scoped operations only)
- Generated reference, from the tree: [`dashboard/public/docs/reference.md`](../dashboard/public/docs/reference.md)
  — it lists the public and scoped operations and states that the admin-only operations are
  omitted from that mirror, which is the gap this page maps
- Contract: [`docs/openapi.json`](openapi.json); the daemon serves the same document at
  `GET /v1/openapi.json`
- Contract drift gate: `cmp docs/openapi.json daemon/internal/api/openapi.json`

## Pointers

- [`docs/API-GUIDE.md`](API-GUIDE.md) — scoped-key quickstart and the full error table
- [`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md) — credential worlds and the edge allowlist
- [`docs/RBAC.md`](RBAC.md) — operator identities and the strict-mode cutover
- [`docs/OPERATIONS.md`](OPERATIONS.md) — backup, rotation, rollback
