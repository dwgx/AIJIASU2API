# `ajs` CLI

A small, read-only operator CLI for `ajs-accountd`. It is part of the Go SDK module
(`sdk/go`, module `ajssdk`), uses the standard library plus that module only, and is built from
source — there is no CLI release artefact and no separate version tag.

What it does: probe the two health endpoints, list nodes and leases, show one lease with the gate
connection it points at, and run a doctor pass over those same reads. It never mutates daemon
state and never prints a credential.

## Build from source

```bash
cd sdk/go
go build -o ajs ./cmd/ajs
./ajs --version          # ajs 0.1.0
```

`go install ./cmd/ajs` works too (binary lands in `GOBIN`). On Windows name the output
`ajs.exe`: `go build -o ajs.exe ./cmd/ajs`. Nothing outside `sdk/go` is needed: the module has no
third-party dependency and the build needs no network.

## Configuration

| Flag | Environment | Meaning |
|---|---|---|
| `--url` | `AJS_URL` | daemon base URL, for example `http://127.0.0.1:2261` |
| `--key` | `AJS_API_KEY` | admin token or scoped `ajs_sk_` key |

A flag always wins over its environment variable. The key is sent only as
`Authorization: Bearer …`; the CLI never formats it into output, never writes it to a file, and
redacts any `Bearer <token>` a server reflects back into an error (see Security notes).
`ajs status` accepts no key because it sends no credential.

A key passed as `--key` is visible in the process arguments (`ps`, Task Manager) to every local
user; prefer `AJS_API_KEY` except for a throwaway key.

`AJS_API_KEY` in the environment is readable by every child process of that shell. For an admin
token prefer a dedicated shell or a secret store that injects the variable per command.

## Commands

### `ajs status [--url URL] [--json]`

`GET /healthz` and `GET /readyz`, unauthenticated. `/healthz` is required. A 404 from `/readyz`
means the edge does not expose that endpoint — the public edge `https://ajs.dwgx.top:2055` is
health-only — so it prints `SKIP` and is **not** a failure. Every other non-2xx answer (including
401/403 and 5xx), a 404 on `/healthz`, or an unreachable endpoint exits 1.

```console
$ ajs status --url http://127.0.0.1:2261
endpoint http://127.0.0.1:2261
CHECK    HTTP  RESULT  VERSION
healthz  200   PASS    0.1.0
readyz   200   PASS    0.1.0
```

A not-ready daemon prints the stable reason list under the table and exits 1:

```console
$ ajs status --url http://127.0.0.1:2261
endpoint http://127.0.0.1:2261
CHECK    HTTP  RESULT  VERSION
healthz  200   PASS    0.1.0
readyz   503   FAIL    0.1.0
readyz: reasons: state: state directory is not writable
```

An edge that does not expose `/readyz` answers 404 there; the check is skipped and the command
succeeds:

```console
$ ajs status --url http://127.0.0.1:2261
endpoint http://127.0.0.1:2261
CHECK    HTTP  RESULT  VERSION
healthz  200   PASS    0.1.0
readyz   404   SKIP    -
readyz: skipped, not exposed at this edge (HTTP 404)
```

`--json` prints the whole report on stdout: `url`, `ok`, the parsed `version`, and one entry per
check with `name`, `url`, HTTP `status`, `ok`, the decoded `body`, and `error` for a transport
failure. A skipped check carries `"skipped": true`; `ok` on a check means "this check did not
fail", so `report.ok` is true when `/healthz` passed and `/readyz` was 2xx or 404. An unreachable
daemon is a normal report, not a crash:

```console
$ ajs status --url http://127.0.0.1:1 --json
{
  "url": "http://127.0.0.1:1",
  "ok": false,
  "checks": [
    {
      "name": "healthz",
      "url": "http://127.0.0.1:1/healthz",
      "status": 0,
      "ok": false,
      "error": "Get \"http://127.0.0.1:1/healthz\": dial tcp 127.0.0.1:1: connectex: No connection could be made because the target machine actively refused it."
    },
    {
      "name": "readyz",
      "url": "http://127.0.0.1:1/readyz",
      "status": 0,
      "ok": false,
      "error": "Get \"http://127.0.0.1:1/readyz\": dial tcp 127.0.0.1:1: connectex: No connection could be made because the target machine actively refused it."
    }
  ]
}
```

### `ajs nodes [--url URL] [--key KEY] [--region R] [--account ID] [--sort latency|label|region] [--json]`

`ListNodes` (`GET /v1/nodes`), one page, filters passed through as `region` and `account_id`.
`--sort` orders the page client-side:

- `latency` — ascending milliseconds, nodes with no probe last;
- `label` — case-insensitive label;
- `region` — region, then label.

Every mode breaks ties by node id, so the same page always prints in the same order.

```console
$ ajs nodes --url http://127.0.0.1:2261 --key "$AJS_API_KEY" --sort latency
ID      REGION  LABEL          STATE  LATENCY  SUCCESS  INUSE/CAP  EGRESS
node-7  jp      jp-tokyo-1     LIVE   38ms     98%      1/3        203.0.113.7
node-4  us      us-west-1      LIVE   120ms    91%      0/2        203.0.113.4
node-9  cn      cn-shanghai-1  READY  -        -        0/1        -
```

`-` in LATENCY and SUCCESS means the daemon has not probed the node yet (no `lastProbeAt`, no
latency, no success sample); it does not mean zero. `--json` prints the array exactly as the API
returns it:

```console
$ ajs nodes --url http://127.0.0.1:2261 --key "$AJS_API_KEY" --json --region jp
[
  {
    "id": "node-7",
    "accountId": "acc-1",
    "accountLabel": "primary",
    "label": "jp-tokyo-1",
    "region": "jp",
    "country": "",
    "city": "",
    "state": "LIVE",
    "latencyMs": 38,
    "successRate": 0.98,
    "inUse": 1,
    "capacity": 3,
    "egressIp": "203.0.113.7",
    "protocol": "",
    "gatePort": 0,
    "lastProbeAt": "2026-10-29T08:00:00Z"
  }
]
```

An empty page prints `[]`, never `null`.

### `ajs leases [--url URL] [--key KEY] [--user ID] [--json]`

`ListLeases` (`GET /v1/leases`), one page, optional `user_id` filter. USER and ACCOUNT print the
label when the daemon supplies one and fall back to the id; NODE follows the same rule.

```console
$ ajs leases --url http://127.0.0.1:2261 --key "$AJS_API_KEY"
ID       USER   ACCOUNT  REGION  NODE        GATE   STATUS   EXPIRES
lease-1  alice  primary  jp      jp-tokyo-1  19001  active   2026-10-29T09:00:00Z
lease-2  usr-2  acc-1    us      node-4      19002  expired  2026-10-29T08:30:00Z
```

### `ajs lease <id> [--url URL] [--key KEY] [--json]`

`GetLease` (`GET /v1/leases/{id}`): the lease and the loopback gate it currently points at. The
daemon never returns the scoped-key secret, so there is no password to print: the connection
carries `host`, `port`, `protocol`, `username` and `credentialKind` (`ajs_sk` means the password is
the caller's own key).

```console
$ ajs lease lease-1 --url http://127.0.0.1:2261 --key "$AJS_API_KEY"
lease
  id        lease-1
  user      alice
  account   primary
  platform  linux
  region    jp
  node      jp-tokyo-1
  status    active
  egress    203.0.113.7
  ttl       3600s
  expires   2026-10-29T09:00:00Z
  requests  0
  bytes     0
connection
  host        127.0.0.1
  port        19001
  protocol    socks5
  username    usr-1
  credential  ajs_sk
```

`--json` prints `{"lease": …, "connection": …}`. Flags may follow the id
(`ajs lease lease-1 --json`); both orders work.

### `ajs doctor [--url URL] [--key KEY]`

The health probes plus one authenticated `ListNodes` with `limit=1`, one PASS/FAIL/SKIP line per
check. Exits 1 when a required check fails.

```console
$ ajs doctor --url http://127.0.0.1:2261 --key "$AJS_API_KEY"
endpoint http://127.0.0.1:2261
CHECK    RESULT  DETAIL
healthz  PASS    200, version 0.1.0
readyz   PASS    200, version 0.1.0
auth     PASS    ListNodes ok (1 returned)
```

A `/readyz` 404 prints `SKIP` (`404, not exposed at this edge`) and does not fail the run: an edge
that exposes only `/healthz` is healthy by that contract.

A missing key fails only the `auth` line (`no key: pass --key or set AJS_API_KEY`) and still exits
1; it is not a usage error, because the health probes remain useful without a key. A 403 from a
scoped key keeps the daemon's stable reason (`scope_required` / `scoped_key_restricted`) in DETAIL.

### `--help` and `--version`

Every command has its own `--help`. `ajs --version` prints `ajs 0.1.0`, the SDK contract the binary
is built against.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | runtime failure: unreachable daemon, `/healthz` non-2xx, `/readyz` non-2xx other than 404, API error, failed doctor check |
| 2 | usage error: unknown command, unknown flag, missing/invalid URL, missing key, bad `--sort` |

Data goes to stdout, notes and errors to stderr, so `ajs … --json | jq` stays clean.

## Security notes

- **The CLI never prints the key itself.** It is read from `--key` or `AJS_API_KEY`, sent only in
  the `Authorization` header, and absent from tables, JSON, and every line the CLI formats. SDK
  errors carry the method, path, status and the daemon's machine-readable `code`/`reason` — never
  headers or request bodies — and a `Bearer <token>` that a server reflects into `error.message`
  or `error.reason` is replaced with `Bearer [REDACTED]` before the error is printed. The typed
  `Reason` field itself is unchanged, so `IsScopeRequired`-style checks keep working.
- **`--key` is visible in the process arguments.** Any local user can read it from `ps` or Task
  Manager while the command runs; prefer `AJS_API_KEY` except for a throwaway key.
- **URL credentials are dropped.** `https://user:pass@host` is accepted, the userinfo is removed
  before any request is built, and a one-line note goes to stderr. The CLI does not speak HTTP
  Basic auth. A malformed URL fails with a fixed message that does not echo what was typed, so a
  password cannot reach a shell history capture or a CI log through this path.
- **No credential on health probes.** `status` and the first two `doctor` checks send no
  `Authorization` header at all.
- **No redirects.** A redirect answer is treated as final, so a bearer token is never forwarded to
  another origin.
- **Read-only.** The CLI issues `GET` only. It cannot create, rotate or release a lease, and it
  has no code path that sends `X-AJS-CSRF`.
- **TLS.** The binary uses the system trust store; there is no `--insecure` flag. Put a private CA
  in the system store, or terminate TLS in front of a loopback daemon.

## Scope and non-goals

Current scope is exactly the five commands above, one page per list call, and no pagination flags
(`--limit`/`--offset` are not implemented; the daemon default page is used).

Explicit non-goals in this build:

- **No `connect` command.** A local proxy that dials the lease gate and hands the client a local
  port is future work, not a stub here. Today the gate is dialled directly with the connection
  fields `ajs lease` prints — see the curl examples in [`sdk/README.md`](../sdk/README.md).
- No mutation commands (`lease create`, `rotate`, `release`), no usage/request/event views, no
  accounts or keys.
- No release artefact, no `:latest` container, no tag change: build from source.
- No TypeScript parity; the CLI is a thin layer over the Go client.

## Verify

```bash
cd sdk/go
gofmt -l ./cmd/ajs          # no output
go vet ./...
go test -race -count=1 ./...   # ajssdk + ajssdk/cmd/ajs
go build -o ajs ./cmd/ajs
./ajs --help
./ajs status --url http://127.0.0.1:1 --json   # exit 1, report printed, no panic
```

The CLI suite (`sdk/go/cmd/ajs/main_test.go`, 24 tests) is hermetic: every case drives `run` against
an `httptest` server and covers status pass/fail/skip/JSON/unreachable, node sort/table/JSON and
filters, lease list and detail, doctor pass/fail/skip/missing-key, the `Authorization` header,
environment fallbacks and flag precedence, URL validation (query, bare `?`, fragment), and secret
redaction (API errors, server-reflected Bearer tokens and URL userinfo). It needs no daemon, no
network and no vendor account.
