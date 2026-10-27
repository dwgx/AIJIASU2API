# Scopes and grants

> The scope table, the key subject rule, and how accountIds/regions narrow every read and write.

English mirror of this page; the console page carries the Chinese translation.

## One exact scope per route

A scoped key needs the exact scope of the matched route. A key carrying `admin` bypasses the table, and the admin token bypasses it by definition. Every operation a scoped key can reach declares its grant in the spec as `x-required-scope`, together with the `403` and `429` responses and the `X-RateLimit-*` headers; an operation without that extension is not reachable with a scoped key.

`proxy:connect` is the one scope with no HTTP route: it authorizes the gate — the loopback SOCKS5 listener and the live public HTTPS CONNECT edge at `gate.dwgx.top`. Admin-only surfaces — device pool, users, API keys, scoped-key management, webhooks, backup, lab telemetry — stay admin-only even for a key that carries a scoped grant.

## Scope catalogue

| Scope | Grants | Representative routes |
| --- | --- | --- |
| `accounts:read` | Read accounts and account IO capabilities/device. | `GET /v1/accounts`, `GET /v1/accounts/{id}`, `GET /v1/accounts/{id}/device` |
| `accounts:write` | Create, patch or delete accounts; login and logout. | `POST /v1/accounts`, `PATCH /v1/accounts/{id}`, `POST /v1/accounts/{id}/actions/login` |
| `catalog:read` | Read the catalog, nodes, operations, topology and the debug trace. | `GET /v1/accounts/{id}/servers`, `GET /v1/nodes`, `GET /v1/operations`, `GET /v1/topology` |
| `catalog:write` | Selection and guarded switch; node drain/probe; operation cancel. | `PUT /v1/accounts/{id}/selection`, `POST /v1/accounts/{id}/actions/connect`, `POST /v1/nodes/{id}/actions/drain` |
| `leases:read` | List and read the key user’s leases. | `GET /v1/leases`, `GET /v1/leases/{id}` |
| `leases:write` | Create, rotate and revoke the key user’s leases. | `POST /v1/leases`, `POST /v1/leases/{id}/actions/rotate`, `DELETE /v1/leases/{id}` |
| `proxy:connect` | Authenticate at a gate and connect through the key user’s lease. | `SOCKS5 127.0.0.1:<gatePort>`, `HTTPS CONNECT gate.dwgx.top:<port>` |
| `requests:read` | Read the request log. | `GET /v1/requests` |
| `audit:read` | Read and export the audit log. | `GET /v1/audit`, `GET /v1/audit/export.csv` |
| `metrics:read` | Prometheus and JSON metrics. | `GET /metrics`, `GET /v1/metrics` |
| `settings:read` | Read settings and schedules. | `GET /v1/settings`, `GET /v1/schedules` |
| `settings:write` | Patch settings and schedules. | `PATCH /v1/settings`, `POST /v1/schedules` |
| `usage:read` | Usage aggregation. | `GET /v1/usage` |
| `renewal:read` | Renewal plan table and per-account expiry. | `GET /v1/renewal/plans`, `GET /v1/renewal/accounts/{id}` |
| `health:read` | Loopback gate health. | `GET /v1/health/nodes` |
| `events:read` | Event replay and the SSE stream. | `GET /v1/events`, `GET /v1/events/stream` |
| `admin` | Every route, including admin-only surfaces. | All routes. |

## proxy:connect is the gate

The gate accepts a scoped key only when the key is active, belongs to the gate user, carries `proxy:connect`, and allows the gate’s account through its `accountIds`/`regions` grant. A `proxy:connect` key with no grant is refused, exactly as on the API. The flow is then bound to the user’s single active lease on that account: no lease, a lease on another account, or more than one active lease for the same account is refused. The gate keeps only the lease id; the secret is not retained in the principal, flow records or logs.

## The subject is the lease owner

A non-admin scoped key is bound to its own `userId`. `GET /v1/leases` returns only leases whose `userId` equals the key’s user; naming another `user_id`/`userId` answers `403 FORBIDDEN` with `reason: "scoped_key_restricted"` and does not reveal whether that user holds a lease. `POST /v1/leases` fills an empty `userId` with the key’s user; a different id is refused with the same `403` before anything is allocated. An `Idempotency-Key` replay is re-checked against both the user and the account grant, so a retry never returns another user’s lease.

`GET /v1/leases/{id}`, `DELETE /v1/leases/{id}` and `POST /v1/leases/{id}/actions/rotate` resolve the owner first. A missing id and another user’s lease answer the same `404 NOT_FOUND`; the key’s own lease on an account outside its grant answers `403`. The detail read returns the lease and its `connection` block and never the scoped-key secret. Accounts and nodes are operator assets: they are filtered by the key’s grants, never by `userId`, and no customer key owns them.

## Narrowing with accountIds and regions

Both lists narrow reads and writes. An account matches when the id list is empty or contains its id, and the region list is empty or overlaps the account’s `regions`. Empty lists on an account-bound key mean “not narrowed by account” — which is why a new non-admin key carrying `accounts:*`, `catalog:*`, `leases:*` or `proxy:connect` must name at least one `accountIds` or `regions` entry; otherwise key creation answers `400 INVALID_ARGUMENT`.

Writes are narrowed too. A key restricted to `acc-02` is refused on every mutation of another account, including `PATCH`/`DELETE /v1/accounts/{id}`, the login/logout/connect/disconnect/reconnect/select-node actions, the selection routes, lease create/rotate/revoke, node drain/undrain/probe and operation cancel. `POST /v1/leases` must name an allowed `accountId`; a restricted key that omits it is refused, because the store would otherwise pick any free account. A write naming an account-shaped id the store cannot resolve answers the same `403` as a foreign account, so a mutation cannot probe whether an account, node or operation exists.

Reads keep `404` for an unknown id and `403` for a known account outside the key. Global, unattributed surfaces are still refused to a restricted key rather than guessed: `GET /v1/audit`, `GET /metrics` and the event stream. `GET /v1/topology` is projected to the key’s visible nodes, and `GET /v1/health/nodes` is filtered. A key carrying `admin` ignores both lists.

## Keys minted before the grant rule

A key minted before the account-grant rule with an account, catalog, lease or `proxy:connect` scope fails closed with `403` on every non-admin route until it is reissued. A key whose scopes are all global (`usage:read`, `requests:read`, `metrics:read`, `events:read`, `settings:*`, `audit:read`, `renewal:read`, `health:read`) may omit both lists; that means “not narrowed by account”.
