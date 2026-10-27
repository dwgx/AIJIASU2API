# Authentication

> Bearer credentials, CSRF on unsafe methods, and the rate-limit headers a scoped key receives.

English mirror of this page; the console page carries the Chinese translation.

## Two credentials, one header

The core API accepts two kinds of bearer credential. They share the `Authorization` header but not their authority.

| Credential | Format | Authority |
| --- | --- | --- |
| Admin token | `AJS_ADMIN_TOKEN`, at least 32 characters | Every route. |
| Scoped key | `ajs_sk_...`, returned once when the key is created | Only the routes its scopes name, narrowed by its account/region grants; the gate accepts it only with `proxy:connect`. |

Scoped keys are operator-issued: there is no self-service signup or key issuance. An operator creates the key in the dashboard under Settings → Access, sets its scopes and its `accountIds`/`regions` grants, and distributes it once; a customer cannot mint, widen or re-scope one, and the public edge only carries keys the operator already created.

In a strict-RBAC deployment (`AJS_RBAC_STRICT=1`) the shared admin token is retired; use an operator `admin` token instead. Scoped keys keep their own policy.

**Authenticated read**

```bash
curl -sS "$BASE/v1/nodes?limit=50&offset=0" \
  -H "Authorization: Bearer $AJS_API_KEY"
```

## CSRF and Origin

Unsafe methods (`POST`, `PUT`, `PATCH`, `DELETE`) also require `X-AJS-CSRF: 1`. If an `Origin` header is present it must exactly match the configured `AJS_ADMIN_ORIGINS`; CLI and SDK clients omit `Origin` and are treated as the non-browser path.

> **A bearer alone is not enough** — A browser request from another origin is refused even when the bearer is valid. Send both the CSRF header and an allowed origin, or call from a CLI/SDK.

**Unsafe request**

```bash
curl -sS -X POST "$BASE/v1/leases" \
  -H "Authorization: Bearer $AJS_API_KEY" \
  -H "X-AJS-CSRF: 1" \
  -H "Content-Type: application/json" \
  -d '{"userId":"u-01","platform":"ios","accountId":"acc-02","ttlSeconds":3600}'
```

## Rate limits

Scoped-key calls are limited per key. The default is 60 requests per minute; `rateLimitPerMin: 0` means unlimited. A scoped response carries the three headers below except on `401`. Admin tokens do not receive them.

| Header | Meaning |
| --- | --- |
| `X-RateLimit-Limit` | The key’s per-minute budget; `0` means unlimited. |
| `X-RateLimit-Remaining` | Tokens left in the current bucket after this request. |
| `X-RateLimit-Reset` | Unix epoch seconds when the bucket is full again; `0` when unlimited. |

A refusal is `429 RATE_LIMITED` with `Retry-After`. A guarded-switch cooldown and an exhausted quota can also answer `429`; honour `Retry-After` instead of retrying immediately.

## Authentication failures

A missing or invalid bearer answers `401 UNAUTHORIZED` with the JSON error envelope. A key that lacks the route scope answers `403 FORBIDDEN` with `reason: "scope_required"`; a key whose grants exclude the target answers `403` with `reason: "scoped_key_restricted"`; a key calling an operator-global surface (the audit log or the event stream) without the `admin` scope answers `403` with `reason: "admin_required"`. A suspended user’s key is refused `401` until the user is resumed. Clients branch on `reason`, not on the human-readable message.

**401 envelope**

```json
{
  "error": {
    "code": "UNAUTHORIZED",
    "message": "missing or invalid bearer token"
  }
}
```

> **Validate with a route you can call** — A scoped key can check itself against any route its scopes already cover — for example `GET /v1/nodes` with `catalog:read`. Admin-only identity routes are not part of the scoped surface.
