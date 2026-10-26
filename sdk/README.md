# AJS SDK — Go and TypeScript clients for `ajs-accountd`

Dependency-free clients for the admin API in [`docs/openapi.json`](../docs/openapi.json).
Both are written by hand against that spec: every path, query parameter and field name below
comes from it, and no endpoint outside it is called. They cover the scripting surface the
console uses — **accounts**, **nodes**, **select-node operations (202 + poll to terminal)**,
**leases (including the gate connection)**, **usage**, **request history** and **events**
(cursor replay plus SSE).

- Go: standard library only (`sdk/go`, module `ajssdk`).
- TypeScript: no runtime dependencies, global `fetch` (`sdk/ts`), ES2022/ES2024 output, with
  the toolchain pinned by `sdk/ts/package-lock.json`.

## Customer journey: key → nodes → lease → connection → usage

Issuing the key is an operator action (`POST /v1/scoped-keys` with the admin token). Everything
after it runs with the scoped key alone. Neither client writes the key anywhere: it is read from
the environment and handed to `New` / `new Client`.

### Issue the key (operator, once)

```bash
AJS_URL=http://127.0.0.1:18080

# Pick the account the key may use.
ACCOUNT_ID="$(curl -sS -H "Authorization: Bearer $AJS_ADMIN_TOKEN" \
  "$AJS_URL/v1/accounts" | jq -r '.[0].id')"

# Least-privilege key bound to usr-1 and one account. The secret is returned once.
export AJS_SK="$(jq -n --arg account "$ACCOUNT_ID" '{
    userId: "usr-1",
    label: "sdk-journey",
    scopes: ["catalog:read", "leases:read", "leases:write",
             "proxy:connect", "usage:read", "requests:read"],
    accountIds: [$account]
  }' | curl -sS -X POST "$AJS_URL/v1/scoped-keys" \
    -H "Authorization: Bearer $AJS_ADMIN_TOKEN" \
    -H "X-AJS-CSRF: 1" -H "Content-Type: application/json" \
    -d @- | jq -r .secret)"
```

The secret is displayed exactly once. `export` keeps it in this shell only: never redirect it to
a file, commit it, or paste it into a log. `catalog:read` reaches `/v1/nodes`, `leases:*` the lease
routes, `proxy:connect` the gate, and `usage:read` / `requests:read` the customer reads.

### Go

```go
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
defer cancel()

client, err := ajs.New(os.Getenv("AJS_URL"), os.Getenv("AJS_SK"))
if err != nil {
	log.Fatal(err)
}

// 1. Nodes the key's account grant reaches.
nodes, err := client.ListNodes(ctx, ajs.ListNodesOptions{Region: "jp", State: ajs.NodeLive})
if err != nil {
	log.Fatal(err) // a scoped-key 403 maps to IsScopeRequired / IsScopedKeyRestricted
}
if len(nodes) == 0 {
	log.Fatal("no live node in jp")
}

// 2. Lease. A scoped key fills its own userId; accountId is required.
created, err := client.CreateLease(ctx, ajs.CreateLeaseInput{
	Platform:  ajs.PlatformLinux,
	AccountID: nodes[0].AccountID,
})
if err != nil {
	log.Fatal(err)
}
defer func() { _ = client.ReleaseLease(ctx, created.Lease.ID) }()

// 3. Connection: the gate this lease currently points at and the credential shape.
detail, err := client.GetLease(ctx, created.Lease.ID)
if err != nil {
	log.Fatal(err)
}
conn := detail.Connection
fmt.Printf("dial %s %s:%d as %s, password=$AJS_SK\n",
	conn.Protocol, conn.Host, conn.Port, conn.Username)

// 4. Usage and request history for this key's user.
usage, err := client.GetUsage(ctx, ajs.UsageOptions{})
if err != nil {
	log.Fatal(err)
}
limit := 50
recent, err := client.ListRequests(ctx, ajs.ListRequestsOptions{Limit: &limit})
if err != nil {
	log.Fatal(err)
}
fmt.Printf("requests=%d bytes=%d recent=%d\n", usage.Requests, usage.Bytes, len(recent))
```

`ListRequestsOptions.Limit` is a pointer because `0` is meaningful: nil keeps the daemon default
(200 rows) and a pointer to `0` asks for every retained row. `ListNodes` uses the same
`account_id` / `state` / `region` / `limit` / `offset` filters as the route.

### TypeScript

```ts
const ajs = new Client({ baseUrl: process.env.AJS_URL!, token: process.env.AJS_SK! });

const [node] = await ajs.listNodes({ region: "jp", state: "LIVE" });
if (!node) throw new Error("no live node in jp");

const { lease } = await ajs.createLease({
  userId: "", // a scoped key fills its own user and refuses another id
  platform: "linux",
  accountId: node.accountId,
});
try {
  const { connection } = await ajs.getLease(lease.id);
  console.log(
    `dial ${connection.protocol} ${connection.host}:${connection.port} as ` +
      `${connection.username}, password=$AJS_SK`,
  );

  const usage = await ajs.getUsage();
  const recent = await ajs.listRequests({ limit: 50 });
  console.log(usage.requests, usage.bytes, recent.length);
} finally {
  await ajs.releaseLease(lease.id);
}
```

`listRequests({ limit: 0 })` asks for every retained row; omitting `limit` keeps the 200-row page.

### Dial the gate

`connection` names `host`, `port`, `protocol` and `username`; the password is the scoped key
itself (`credentialKind: "ajs_sk"`). For SOCKS5:

```bash
curl --socks5-hostname 127.0.0.1:19001 \
  --proxy-user "usr-1:$AJS_SK" https://example.com/
```

For `protocol: "http"` use `--proxy http://127.0.0.1:19001` instead. Gates bind loopback only, so
a remote client needs an operator-provided tunnel or ingress. A client that cannot read the
environment should prompt for the key rather than store it; revoke the key with
`DELETE /v1/scoped-keys/{id}` when the journey is over.

## Go client notes

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

## TypeScript client notes

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
| Nodes | `ListNodes` | `listNodes` | `GET /v1/nodes` |
| Operations | `SelectNode`, `WaitOperation`, `ListOperations`, `GetOperation`, `CancelOperation` | same | `POST .../select-node`, `GET/DELETE /v1/operations[/{id}]` |
| Leases | `ListLeases`, `GetLease`, `CreateLease`, `RotateLease`, `ReleaseLease` | same | `GET/POST /v1/leases`, `GET/DELETE /v1/leases/{id}`, `POST .../rotate` |
| Usage | `GetUsage` | `getUsage` | `GET /v1/usage` |
| Requests | `ListRequests` | `listRequests` | `GET /v1/requests` |
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

Scoped-key denials are 403 answers whose `reason` is one of two stable values. Branch on the
reason, never on the human-readable message:

| Reason | Meaning | Go | TypeScript |
|---|---|---|---|
| `scope_required` | the key lacks the route's scope | `ajs.IsScopeRequired(err)` | `isScopeRequired(err)` |
| `scoped_key_restricted` | the account/region grant or lease/user ownership excludes the target | `ajs.IsScopedKeyRestricted(err)` | `isScopedKeyRestricted(err)` |

`ajs.HasReason(err, reason)` and `isApiErrorReason(err, reason)` match any reason; the constants
are `ajs.ReasonScopeRequired` / `ajs.ReasonScopedKeyRestricted` and `SCOPE_REQUIRED` /
`SCOPED_KEY_RESTRICTED`. A foreign lease id and an unknown one both answer 404, so a client cannot
probe ids.

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
cd sdk/go && go test -count=1 ./...
cd sdk/ts && npm ci && npm test && npm run typecheck
```

The Go suite uses `net/http/httptest`; the TypeScript suite injects a `fetch` stub and runs on
`node --test` against the compiled output. Both cover auth/query encoding, typed errors without
token leakage, retry/backoff and `Retry-After`, 202 polling, failed operations, 504 recovery,
idempotency-key agreement, lease detail/connection decoding, node and request filtering, scoped-key
reason mapping, usage decoding and SSE frame parsing. Neither suite contacts the network or needs a
daemon.

## Keeping it in sync

The clients are generated by hand from `docs/openapi.json` (served live as `GET /v1/openapi.json`).
When a schema changes, update `sdk/go/types.go` and `sdk/ts/src/types.ts`, then the method that
uses it. Do not add an endpoint that is absent from the spec. Both trees are independent modules:
`sdk/go` does not import the daemon and `sdk/ts` has no runtime package dependencies.

TypeScript tooling is pinned exactly (`typescript` 5.9.3) and `sdk/ts/package-lock.json` records
the resolved tarball and integrity hash, so `npm ci` is reproducible. Regenerate the lockfile with
`npm install --package-lock-only` when the pin changes; never edit it by hand.

## Status and self-audit

- **Verified offline:** `cd sdk/go && go test -count=1 ./...` exit 0 (38 tests) and
  `cd sdk/ts && npm ci && npm test && npm run typecheck` exit 0 (24 tests; `tsc --noEmit` clean).
- **[UNVERIFIED] against a live daemon:** both suites use an `httptest` server or an injected
  `fetch` stub, so no `ajs-accountd` instance was contacted and no vendor account was used.
- **Scope:** only `sdk/**`; `daemon/`, `docs/`, `dashboard/` and `deploy/` are untouched.
  `sdk/ts/dist/` and `sdk/ts/node_modules/` are ignored.
- **Spec denominator:** every path and field was read from `docs/openapi.json` (OpenAPI 3.1.0,
  `ajs-accountd` 0.1.0); no endpoint outside that document is called.
