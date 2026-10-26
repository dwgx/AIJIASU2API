# AJS SDK — Go and TypeScript clients for `ajs-accountd`

Dependency-free clients for the admin API in [`docs/openapi.json`](../docs/openapi.json).
Both are written by hand against that spec: every path, query parameter and field name below
comes from it, and no endpoint outside it is called. They cover the scripting surface the
console uses — **accounts**, **select-node operations (202 + poll to terminal)**, **leases**,
**usage** and **events** (cursor replay plus SSE).

- Go: standard library only (`sdk/go`, module `ajssdk`).
- TypeScript: no runtime dependencies, global `fetch` (`sdk/ts`), ES2022/ES2024 output.

## Go

```go
client, err := ajs.New("http://127.0.0.1:18080", os.Getenv("AJS_ADMIN_TOKEN"))
if err != nil { log.Fatal(err) }
accounts, err := client.ListAccounts(ctx, ajs.ListAccountsOptions{State: ajs.AccountActive})
result, err := client.SelectNode(ctx, accounts[0].ID, ajs.SelectNodeRequest{Target: &ajs.OperationTarget{Region: "jp"}})
lease, err := client.CreateLease(ctx, ajs.CreateLeaseInput{UserID: "usr-1", Platform: ajs.PlatformLinux})
usage, err := client.GetUsage(ctx, ajs.UsageOptions{Account: accounts[0].Label})
err = client.FollowEvents(ctx, ajs.StreamEventsOptions{}, func(e ajs.Event) error { log.Printf("%s %v", e.Type, e.Data); return nil })
log.Printf("egress=%s gate=%d requests=%d", result.EgressIP, lease.Lease.GatePort, usage.Requests)
```

`SelectNode` posts the declarative request and, on `202`, polls `Location` until the operation is
terminal. A `200` means the target was already current (`Changed=false`); a `wait:true` `504` is
recovered from `details.operationId` and polled. A failed or canceled operation is returned as
`*ajs.OperationFailure` with `OperationID`, `Code`, `Message` and `Details`. `WaitOperation`
exposes the same polling without the select-node sugar.

## TypeScript

```ts
import { Client } from "@ajs/sdk";

const ajs = new Client({ baseUrl: "http://127.0.0.1:18080", token: process.env.AJS_ADMIN_TOKEN! });
const [account] = await ajs.listAccounts({ state: "active" });
const result = await ajs.selectNode(account.id, { target: { region: "jp" } });
const { lease } = await ajs.createLease({ userId: "usr-1", platform: "linux" });
const usage = await ajs.getUsage({ account: account.label });
for await (const event of ajs.streamEvents()) console.log(event.type, event.data);
console.log(result.egressIp, lease.gatePort, usage.requests);
```

`streamEvents()` is an async generator over `text/event-stream`: comments (`: keepalive`),
multi-line `data:`, `id:`/`event:` and CRLF are handled, and the generator ends when the daemon
closes the stream. Abort it with `streamEvents({ signal })`. `selectNode` has the same
202/poll/504-recovery behaviour as Go and throws `OperationFailure` for a failed operation.

## Coverage

| Area | Go | TypeScript | Endpoint |
|---|---|---|---|
| Accounts | `ListAccounts`, `CreateAccount`, `GetAccount`, `PatchAccount`, `DeleteAccount`, `LoginAccount`, `LogoutAccount` | same names, camelCase | `/v1/accounts[/{id}][/actions/{login,logout}]` |
| Catalog | `ListAccountServers` | `listAccountServers` | `GET /v1/accounts/{id}/servers` |
| Operations | `SelectNode`, `WaitOperation`, `ListOperations`, `GetOperation`, `CancelOperation` | same | `POST .../select-node`, `GET/DELETE /v1/operations[/{id}]` |
| Leases | `ListLeases`, `CreateLease`, `RotateLease`, `ReleaseLease` | same | `GET/POST /v1/leases`, `POST .../rotate`, `DELETE /v1/leases/{id}` |
| Usage | `GetUsage` | `getUsage` | `GET /v1/usage` |
| Events | `ListEvents`, `StreamEvents`, `FollowEvents` | `listEvents`, `streamEvents` | `GET /v1/events`, `GET /v1/events/stream` |

Every client method sends `Authorization: Bearer <token>` and `Accept: application/json` (the SSE
stream sends `text/event-stream`). Every unsafe method (`POST`, `PUT`, `PATCH`, `DELETE`) also sends
`X-AJS-CSRF: 1`, the daemon's non-simple admin-mutation header; clients do not send `Origin`, which
the daemon treats as the explicit non-browser path. Resource ids are path-escaped; query parameters
are omitted when empty.

## Errors

- Go: `*ajs.APIError` (`StatusCode`, `Code`, `Reason`, `Details`, `RetryAfter`) for any non-2xx
  answer; `*ajs.NetworkError` for transport failures; `*ajs.OperationFailure` and
  `*ajs.OperationTimeoutError` for async operations. `ajs.IsCode(err, "CURSOR_EXPIRED")` is the
  cursor helper.
- TypeScript: `ApiError`, `NetworkError`, `TimeoutError`, `OperationFailure`,
  `OperationTimeoutError`, plus `isApiError` / `isApiErrorCode`.

Neither client logs, and no error message contains request headers or bodies, so a bearer token
cannot reach a log through an SDK error. Error bodies are parsed for `code`/`message`/`reason`/
`details` only; an unparseable proxy page contributes only the HTTP status.

## Retries

Default policy in both languages: 3 attempts, 200 ms base delay doubling to a 5 s cap, 20% jitter,
and `Retry-After` honoured up to 30 s. Retried: transport errors, `429`, `500`, `502`, `503`,
`504`. `GET`, `HEAD`, `PUT` and `DELETE` are always retryable; `POST` is retried **only** when an
`Idempotency-Key` header is present. `CreateLease` generates a `sdk-<random>` key when the caller
supplies none and sends it as both the body field and the header, so a transient failure cannot
allocate a second node. `select-node` is not retried: its `202` already carries a durable
operation id that the poll loop resumes.

The policy is configurable: `WithRetryPolicy` in Go and `retry` in `ClientOptions` in TypeScript
(`maxAttempts`, `baseDelayMs`, `maxDelayMs`, `retryAfterCapMs`, `jitter`).

## Tests

```
cd sdk/go && go test ./...
cd sdk/ts && npx tsc --noEmit && npm test
```

The Go suite uses `net/http/httptest`; the TypeScript suite injects a `fetch` stub and runs on
`node --test` against the compiled output. Both cover auth/query encoding, typed errors without
token leakage, retry/backoff and `Retry-After`, 202 polling, failed operations, 504 recovery,
idempotency-key agreement, usage decoding and SSE frame parsing. Neither suite contacts the
network or needs a daemon.

## Keeping it in sync

The clients are generated by hand from `docs/openapi.json` (served live as `GET /v1/openapi.json`).
When a schema changes, update `sdk/go/types.go` and `sdk/ts/src/types.ts`, then the method that
uses it. Do not add an endpoint that is absent from the spec. Both trees are independent modules:
`sdk/go` does not import the daemon and `sdk/ts` has no runtime package dependencies.

## Status and self-audit

- **Verified offline:** `cd sdk/go && go test ./...` exit 0 (31 tests) and
  `cd sdk/ts && npx tsc --noEmit` exit 0; the TypeScript suite is 20 tests on `node --test`.
- **[UNVERIFIED] against a live daemon:** both suites use an `httptest` server or an injected
  `fetch` stub, so no `ajs-accountd` instance was contacted and no vendor account was used.
- **Scope:** only `sdk/**` (new) and one pointer bullet in the root `README.md`;
  `daemon/`, `dashboard/` and `deploy/` are untouched. `sdk/ts/dist/` is ignored.
- **Spec denominator:** every path and field was read from `docs/openapi.json` (OpenAPI 3.1.0,
  `ajs-accountd` 0.1.0); no endpoint outside that document is called.
