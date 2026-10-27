# SDKs

> The Go and TypeScript clients, their retry policy, and what has actually been run.

English mirror of this page; the console page carries the Chinese translation.

## Go: module ajssdk

The Go client lives in `sdk/go` (module `ajssdk`). It is hand-written against `docs/openapi.json`, dependency-free, and covers accounts, catalog and nodes, select-node operations, leases, requests, usage and events. It sends `Authorization: Bearer` and `X-AJS-CSRF: 1` on unsafe methods, omits `Origin`, retries transport errors plus `429`/`500`/`502`/`503`/`504` with backoff, honours `Retry-After`, polls a `202` to a terminal operation, and generates an `Idempotency-Key` for `CreateLease`.

**Accounts, select-node, lease**

```go
client, err := ajs.New("http://127.0.0.1:2261", os.Getenv("AJS_ADMIN_TOKEN"))
accounts, err := client.ListAccounts(ctx, ajs.ListAccountsOptions{State: ajs.AccountActive})
result, err := client.SelectNode(ctx, accounts[0].ID, ajs.SelectNodeRequest{
    Target: &ajs.OperationTarget{Region: "jp-tyo"},
})
lease, err := client.CreateLease(ctx, ajs.CreateLeaseInput{UserID: "u-01", Platform: ajs.PlatformIOS})
```

Exported methods: `ListAccounts`, `CreateAccount`, `GetAccount`, `PatchAccount`, `DeleteAccount`, `LoginAccount`, `LogoutAccount`, `ListAccountServers`, `SelectNode`, `WaitOperation`, `ListOperations`, `GetOperation`, `CancelOperation`, `ListNodes`, `ListLeases`, `CreateLease`, `RotateLease`, `ReleaseLease`, `GetLease`, `ListRequests`, `GetUsage`, `ListEvents`, `StreamEvents`, `FollowEvents`.

## TypeScript: @ajs/sdk

The TypeScript client lives in `sdk/ts` (package `@ajs/sdk`). It exposes the same methods in camelCase — including `listNodes`, `getLease` and `listRequests` — plus `ApiError`, `OperationFailure` and `streamEvents()` as an async generator. It retries every `5xx`; the rest of the policy matches the Go client.

**Accounts, select-node, lease**

```ts
import { Client } from "@ajs/sdk";

const ajs = new Client({
  baseUrl: "http://127.0.0.1:2261",
  token: process.env.AJS_ADMIN_TOKEN!,
});

const [account] = await ajs.listAccounts({ state: "active" });
const result = await ajs.selectNode(account.id, { target: { region: "jp-tyo" } });
const { lease } = await ajs.createLease({ userId: "u-01", platform: "ios" });
```

## What the SDKs do not prove

> **Admin API, not the gate** — Both clients call the admin API, not the proxy gate; a successful SDK call does not prove vendor reachability. Neither SDK has been run against a live daemon outside `httptest` or an injected `fetch`.
