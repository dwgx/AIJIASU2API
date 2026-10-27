# API reference

Public and scoped operations from `docs/openapi.json` (ajs-accountd Admin API 0.1.0).

- 2 public operations
- 54 scoped operations
- 92 admin-only operations, omitted from this mirror; the console shows their counts by tag only

Every path below exists in the contract. Admin-only routes are not callable with a scoped key and are not listed here.

## Health

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/healthz` | public | — | Liveness, version and per-state account counts. |
| GET | `/readyz` | public | — | Readiness: state-directory write probe and configured integrations. |
| GET | `/v1/health/nodes` | scoped | `health:read` | Read loopback gate health for every watched node. |

## Metrics

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/metrics` | scoped | `metrics:read` | Prometheus text exposition of aggregate counts. |
| GET | `/v1/metrics` | scoped | `metrics:read` | Dashboard metrics for one window. |

## Accounts

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/accounts` | scoped | `accounts:read` | List accounts, secrets never returned. |
| POST | `/v1/accounts` | scoped | `accounts:write` | Create an account and start its first login. |
| GET | `/v1/accounts/{id}` | scoped | `accounts:read` | Read one account. |
| PATCH | `/v1/accounts/{id}` | scoped | `accounts:write` | Update entitlement fields of one account. |
| DELETE | `/v1/accounts/{id}` | scoped | `accounts:write` | Drain the account, delete its record and keep the audit. |
| POST | `/v1/accounts/{id}/actions/login` | scoped | `accounts:write` | Force a login (vendor login when AJS_ACCOUNT_LOGIN=1). |
| POST | `/v1/accounts/{id}/actions/logout` | scoped | `accounts:write` | Stop sessions, drain nodes and pause the account. |
| GET | `/v1/accounts/{id}/device` | scoped | `accounts:read` | Hashed device identity and non-fingerprint claims. |

## Account IO

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/accounts/io/capabilities` | scoped | `accounts:read` | Limits and gates for import/export dialogs. |

## Catalog

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| POST | `/v1/accounts/{id}/actions/connect` | scoped | `catalog:write` | Guarded switch to a vendor server (single session per account). |
| POST | `/v1/accounts/{id}/actions/disconnect` | scoped | `catalog:write` | Drain and close every gate of the account; keeps the target. |
| POST | `/v1/accounts/{id}/actions/reconnect` | scoped | `catalog:write` | Reopen the current server or the persisted target. |
| POST | `/v1/accounts/{id}/actions/select-node` | scoped | `catalog:write` | Select the account's live node asynchronously (declarative, idempotent). |
| GET | `/v1/accounts/{id}/selection` | scoped | `catalog:read` | Read the persisted operator target. |
| PUT | `/v1/accounts/{id}/selection` | scoped | `catalog:write` | Persist the target for the next login sync; starts no session. |
| DELETE | `/v1/accounts/{id}/selection` | scoped | `catalog:write` | Clear the target; idempotent and never disconnects. |
| GET | `/v1/accounts/{id}/servers` | scoped | `catalog:read` | One page of the retained vendor server catalog. |

## Nodes

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/nodes` | scoped | `catalog:read` | List proxy nodes with optional filters. |
| POST | `/v1/nodes/{id}/actions/drain` | scoped | `catalog:write` | Stop admitting new flows and unpublish the node. |
| POST | `/v1/nodes/{id}/actions/probe` | scoped | `catalog:write` | Run a bookkeeping canary probe and return the node. |
| POST | `/v1/nodes/{id}/actions/undrain` | scoped | `catalog:write` | Return a draining node to service if its session is alive. |

## Leases

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/leases` | scoped | `leases:read` | List leases. A non-admin scoped key sees only its own user's leases. |
| POST | `/v1/leases` | scoped | `leases:write` | Create an idempotent lease on a free node. |
| GET | `/v1/leases/{id}` | scoped | `leases:read` | Read one lease and the loopback gate it currently points at. |
| DELETE | `/v1/leases/{id}` | scoped | `leases:write` | Revoke a lease and free its node. |
| POST | `/v1/leases/{id}/actions/rotate` | scoped | `leases:write` | Rotate a lease to a new egress IP and restart its TTL. |

## Requests

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/requests` | scoped | `requests:read` | List recorded requests, newest first. |

## Audit

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/audit` | scoped | `audit:read` | Newest audit events, capped at 500. |
| GET | `/v1/audit/export.csv` | scoped | `audit:read` | Stream the retained audit history as CSV. |

## Settings

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/settings` | scoped | `settings:read` | Read panel settings. |
| PATCH | `/v1/settings` | scoped | `settings:write` | Update one or more settings fields. |

## Topology

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/topology` | scoped | `catalog:read` | Rendered subscription, node-to-hash map and platform list. |

## Schedules

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/schedules` | scoped | `settings:read` | List scheduled operations. |
| POST | `/v1/schedules` | scoped | `settings:write` | Create a scheduled operation. |
| GET | `/v1/schedules/preview` | scoped | `settings:read` | Compute upcoming run instants from query parameters without persisting a job. |
| POST | `/v1/schedules/preview` | scoped | `settings:read` | Compute upcoming run instants without persisting a job. |
| GET | `/v1/schedules/{id}` | scoped | `settings:read` | Read one schedule. |
| PATCH | `/v1/schedules/{id}` | scoped | `settings:write` | Update a schedule; bumped revision rejects stale writes. |
| DELETE | `/v1/schedules/{id}` | scoped | `settings:write` | Delete a schedule and stop future runs. |
| POST | `/v1/schedules/{id}/actions/run` | scoped | `settings:write` | Run a schedule now; destructive actions require confirm. |
| GET | `/v1/schedules/{id}/runs` | scoped | `settings:read` | Run history for one schedule. |

## Renewal

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/renewal/accounts/{id}` | scoped | `renewal:read` | Read one account's expiry state and the official renewal URL. |
| GET | `/v1/renewal/plans` | scoped | `renewal:read` | Read the public plan catalogue with its provenance. |

## Usage

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/usage` | scoped | `usage:read` | Aggregate the request log over an inclusive UTC window. |

## Events

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/events` | scoped | `events:read` | Replay the retained event ring from a cursor. |
| GET | `/v1/events/stream` | scoped | `events:read` | Stream retained and live events as server-sent events. |

## Debug

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/debug/stream` | scoped | `catalog:read` | Stream retained and live trace entries as server-sent events. |
| GET | `/v1/debug/trace` | scoped | `catalog:read` | Read the bounded, redacted runtime trace ring. |

## Operations

| Method | Path | Access | Scope | Summary |
| --- | --- | --- | --- | --- |
| GET | `/v1/operations` | scoped | `catalog:read` | List async operations newest first. |
| GET | `/v1/operations/{id}` | scoped | `catalog:read` | One operation with steps, result and error. |
| DELETE | `/v1/operations/{id}` | scoped | `catalog:write` | Cancel a queued operation before dispatch. |
