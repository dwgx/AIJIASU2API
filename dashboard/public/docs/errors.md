# Errors

> One envelope, a closed code enum, and which failures are safe to retry.

English mirror of this page; the console page carries the Chinese translation.

## The envelope

Every failure uses one JSON shape. `code` is stable; `reason` is optional and may be absent — treat an unknown or missing value as generic. `details` carries small, non-secret context such as the current `snapshotId`, an `operationId` or an `ineligibleReason`.

**A conflict with a reason**

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

## Closed code enum

`code` is one of: `INVALID_ARGUMENT`, `NOT_FOUND`, `CONFLICT`, `UPSTREAM_UNAVAILABLE`, `QUOTA_EXCEEDED`, `RATE_LIMITED`, `UNAUTHORIZED`, `FORBIDDEN`, `INVITE_EXPIRED`, `INVITE_REDEEMED`, `METHOD_NOT_ALLOWED`, `INTERNAL`, `BACKUP_BUSY`, `BACKUP_LIVE_NODE`, `BACKUP_DAEMON_RUNNING`, `BACKUP_UNAVAILABLE`, `CURSOR_EXPIRED`, `OPERATION_TIMEOUT`, `PRECONDITION_FAILED`, `PROVIDER_REFUND_UNSUPPORTED`, `CONFIRMATION_REQUIRED`. `FROZEN` maps to `423` but is reserved: no current route produces it.

## Status mapping

| Status | Code | Typical reason values |
| --- | --- | --- |
| 400 | `INVALID_ARGUMENT` | Bad body, missing `confirm`, bad `limit` or window. |
| 401 | `UNAUTHORIZED` | Missing or invalid bearer; revoked key. |
| 403 | `FORBIDDEN` | `scope_required`, `scoped_key_restricted`, `admin_required` (operator-global surface: audit and the event stream). |
| 404 | `NOT_FOUND` | `unknown_server`, `no_target`, unknown account/user/lease/operation; another user’s lease answers as unknown. |
| 409 | `CONFLICT` / `CONFIRMATION_REQUIRED` | `snapshot_changed`, `switch_in_progress`, `ALREADY_CURRENT`, `SERVER_UNAVAILABLE`, `NO_ALTERNATE_SERVER`, `lease_publish_failed`; `confirmation_required` when a destructive schedule needs `confirm: "ARM"` or `"RUN"`. |
| 410 | `CURSOR_EXPIRED` / `INVITE_EXPIRED` | The event cursor fell out of the ring (restart from `oldestCursor`), or an invitation expired before redemption. |
| 412 | `PRECONDITION_FAILED` | `stale_revision` on `PATCH /v1/schedules/{id}` with `details.currentRevision`, or `SNAPSHOT_CHANGED` on a waited `select-node`; nothing was mutated. |
| 413 | `INVALID_ARGUMENT` | The request body exceeds the route limit. |
| 429 | `QUOTA_EXCEEDED` / `RATE_LIMITED` | `account_slots_exhausted`, user quota, `connect_cooldown` with `Retry-After`, scoped-key bucket. |
| 500 | `INTERNAL` | `store_failed`, unclassified internal failure. |
| 502 | `UPSTREAM_UNAVAILABLE` | `session_open_failed`, `gate_start_failed`, `session_unverified`. |
| 503 | `UPSTREAM_UNAVAILABLE` | Controller stopped, catalog unavailable, `lease_publish_failed`, disabled adapter. |
| 504 | `OPERATION_TIMEOUT` | A waited `select-node` timed out; read `details.operationId` and poll. |

Success statuses worth handling explicitly: `201` (lease, account or scoped key created), `202` (async operation accepted) and `204` (lease revoke, scoped-key revoke, account or API-key delete).

## What to retry

- Retry transport failures, `429` and `5xx` with backoff; honour `Retry-After` when it is present. The Go SDK retries `500/502/503/504`; the TypeScript SDK retries every `5xx`.
- Do not retry `400`, `401`, `403`, `404`, `409` or `412` unchanged: the request or the state must change first. `409` with `confirmation_required` needs an explicit confirmation token, not a retry.
- A `504` on a waited operation carries `details.operationId`; poll `GET /v1/operations/{id}` to a terminal state instead of repeating the switch.
- A `202` is accepted work, not completion. Poll the operation until `succeeded`, `failed` or `canceled`.
- Make lease creation safe to retry with an `Idempotency-Key`; the original lease is replayed instead of allocating a second one.
