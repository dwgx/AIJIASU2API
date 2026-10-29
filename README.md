# AIJIASU2API

Scoped proxy access control plane — an authorised account becomes a lease-bound API and an HTTPS
CONNECT egress, with operator-issued keys.
把授权账号接入，做成可计量、可吊销的代理 API。

[![release v0.1.0-pre](https://img.shields.io/badge/release-v0.1.0--pre-blue)](https://github.com/dwgx/AIJIASU2API/releases/tag/v0.1.0-pre)
[![license MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Go 1.26.10](https://img.shields.io/badge/Go-1.26.10-00ADD8?logo=go&logoColor=white)](daemon/go.mod)
[![LINUX DO](docs/assets/linuxdo-badge.svg)](https://linux.do)

![AIJIASU2API landing page: “把代理接入，做成基础设施。” above a control-plane preview showing the TLS edge on :2055, a bound lease on 13000-13010, scoped ajs_sk_ keys, and the client → VPS → WireGuard → HMC topology](docs/assets/readme-hero.png)

## 30-second quickstart

```bash
git clone https://github.com/dwgx/AIJIASU2API.git && cd AIJIASU2API
cd daemon && AJS_LISTEN=127.0.0.1:2261 AJS_ALLOW_INSECURE=1 go run ./cmd/ajs-accountd
# in a second terminal
curl -sS http://127.0.0.1:2261/healthz   # {"status":"ok","service":"ajs-accountd","version":"0.1.0",...}
```

No vendor account, no vendor network and no Docker are needed for that. `AJS_ALLOW_INSECURE=1` is
a loopback-only lab switch; startup refuses it on a non-loopback bind. Next:
[`docs/API-GUIDE.md`](docs/API-GUIDE.md) for keys, leases and the gate, or the hosted demo below.

**CLI:** `cd sdk/go && go build -o ajs ./cmd/ajs`, then `./ajs status` — `status`, `nodes`,
`leases`, `lease <id>` and `doctor`; see [`docs/CLI.md`](docs/CLI.md).

## Live demo

Operator-run and invite-only: keys are admin-issued `ajs_sk_` keys, there is no signup, no SLA,
and the demo may be withdrawn at any time.

- **Website:** <https://dwgx.github.io/AIJIASU2API/> — static landing page, source in [`site/`](site/)
- **Live docs and API:** <https://ajs.dwgx.top:2055/docs> — rendered reference plus the scoped-key API
- **HTTPS CONNECT:** `gate.dwgx.top`, live map `13000-13010`
- **Release:** <https://github.com/dwgx/AIJIASU2API/releases/tag/v0.1.0-pre>

The public edge applies 120 requests/min with burst 60 per IP, 20 connections/IP, 400 total and a
2 MB body cap; `/metrics` and `/v1/metrics` require a `metrics:read` key.

## What it is

A research corpus plus the artefacts built on it: an operator console ([`dashboard/`](dashboard/README.md)),
a Go control plane ([`daemon/`](daemon/README.md)), Go and TypeScript SDKs ([`sdk/`](sdk/README.md)),
a compose deployment ([`deploy/`](deploy/README.md)), and an offline protocol emulator
([`emulator/`](emulator/README.md)). The retained capability is the **AJiaSu → API** path: an
authorised AJiaSu account becomes a scoped proxy API with a selectable egress IP.

The design in one line: a scoped key names its scopes and accounts, a lease binds one user, one
account, one node and one gate port, and the gate re-checks key, lease, grant and quota at
`CONNECT`. See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the components, ports and trust
boundaries.

## Release status

`v0.1.0-pre` is a **pre-release**: usable for evaluation and research, not production-stable. What
changed and what is intentionally absent is in [CHANGELOG.md](CHANGELOG.md); the near-term plan is
in [ROADMAP.md](ROADMAP.md); the full notes, verified checks and known limitations are in
[docs/releases/v0.1.0-pre.md](docs/releases/v0.1.0-pre.md).

The live binary is `ajs-accountd-61625b0`, which predates the gate's entitlement watch: on the
live host, revoking a key or lease stops new handshakes but does not close an established flow.
The current build revalidates every 10 s. Do not assume the live host runs the current binary;
see [docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md#known-limitations).

## What is implemented

Counts below were measured at this tag. Re-run the commands in [Evidence](#evidence) if you need
today's number.

| Capability | State in `v0.1.0-pre` | Authority |
|---|---|---|
| Scoped keys (`ajs_sk_`) | Served; per-route scopes, account/region grants, per-key rate limits. 54 operations carry `x-required-scope`. | [docs/API-GUIDE.md](docs/API-GUIDE.md), [docs/openapi.json](docs/openapi.json) |
| Leases | Served; create/revoke, idempotency keys, quota, concurrency, and capacity refusals. | [docs/API-GUIDE.md](docs/API-GUIDE.md) |
| Gate | Served; loopback SOCKS5 plus HTTP CONNECT admission, key/lease checks, and flow metering. The TLS-terminated CONNECT edge is live. | [docs/API-GUIDE.md](docs/API-GUIDE.md) |
| Operator dashboard | React 19 / Vite 6 / TypeScript console with mock and live build modes; the public docs site ships in the same static bundle at `/docs`. | [dashboard/README.md](dashboard/README.md) |
| Go and TypeScript SDKs | In-tree clients with typed errors and retry/backoff. | [sdk/README.md](sdk/README.md) |
| OpenAPI 3.1 contract | 120 paths, 148 operations and 200 schemas. | [docs/openapi.json](docs/openapi.json) |
| Test suites | The daemon, emulator, dashboard, and SDK suites run offline. The dashboard suite is 54 test files; the Go SDK has 38 test functions; the TypeScript SDK has 24 tests. | commands in [Evidence](#evidence) |

The live smoke recorded in [docs/releases/v0.1.0-pre.md](docs/releases/v0.1.0-pre.md) exercised
one scoped-key read, one lease create, `/docs`, and one HTTPS CONNECT. Everything else in the
table is covered offline.

## Documentation

The index with audience and status is [docs/README.md](docs/README.md). The short list:

| Document | Purpose |
|---|---|
| [docs/API-GUIDE.md](docs/API-GUIDE.md) | scoped-key quickstart, scopes, leases, the public CONNECT edge |
| [docs/ADMIN-API.md](docs/ADMIN-API.md) | the operator-only surface and its credential rules |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | components, ports, storage, live topology |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | install, health, backup, rotation, upgrade, rollback |
| [docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md) | what is enforced, where, and what is not protected |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | 407/429/409/404/401/403/502/504 triage |
| [docs/PROTOCOL.md](docs/PROTOCOL.md) | the vendor protocol in brief, and why obfuscation is not encryption |
| [docs/VERSIONING.md](docs/VERSIONING.md) | SemVer, tag vs assets, `:latest`, deprecation |
| [docs/RELEASE.md](docs/RELEASE.md) | release gates, artefacts, checksums, rollback |
| [docs/openapi.json](docs/openapi.json) | OpenAPI 3.1 contract (source of truth) |
| [SECURITY.md](SECURITY.md) | private vulnerability reporting and scope |
| [CONTRIBUTING.md](CONTRIBUTING.md) | one concern per PR, tests, evidence, no AI attribution |
| [DISCLAIMER.md](DISCLAIMER.md) | affiliation, authorised use, demo limits, warranty |

## Build and test

Prerequisites: **Go 1.26.10 or newer** (the module files declare `go 1.26.10`), **Node.js 24** and
npm, git, and a POSIX shell for `deploy/package.sh`. Building and testing need no vendor account
and no network access to the vendor.

```bash
# Daemon and emulator. daemon/go.mod replaces ajsproxy => ../emulator,
# so keep the two trees as siblings in the same checkout.
cd daemon && go vet ./... && go test -race -count=1 ./...
cd ../emulator && go vet ./... && go test -race -count=1 ./...

# Dashboard
cd ../dashboard && npm ci && npm run verify:lockfile
npm test -- --pool=forks --maxWorkers=1
VITE_USE_MOCK=false VITE_API_BASE= npm run build

# SDKs
cd ../sdk/go && go test -race -count=1 ./...
cd ../sdk/ts && npm ci && npm test && npm run typecheck
```

## Run locally

```bash
# Terminal 1 — control plane on loopback only
cd daemon
AJS_LISTEN=127.0.0.1:2261 AJS_ALLOW_INSECURE=1 go run ./cmd/ajs-accountd

# Terminal 2 — dashboard against the loopback daemon
cd dashboard
VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 npm run build
npx vite preview --host 127.0.0.1 --port 18082
```

For a real deployment, set `AJS_ADMIN_TOKEN` to at least 32 random characters (for example
`openssl rand -hex 32`) and terminate authentication in a BFF or reverse proxy. Never bake a token
into a `VITE_*` variable: every `VITE_*` value is readable in the built bundle.

## Issue an operator key

Scoped keys are **admin-issued**; there is no self-service key issuance. Create one in the
dashboard under **Settings → Access**, or with the admin token:

```bash
curl -sS -X POST http://127.0.0.1:2261/v1/scoped-keys \
  -H "Authorization: Bearer $AJS_ADMIN_TOKEN" \
  -H "X-AJS-CSRF: 1" \
  -H "Content-Type: application/json" \
  -d '{
    "userId": "u-10",
    "label": "demo",
    "scopes": ["catalog:read", "leases:read", "leases:write", "proxy:connect", "usage:read"],
    "accountIds": ["<accountId>"],
    "rateLimitPerMin": 60
  }'
```

The response contains `"secret": "ajs_sk_..."`, shown exactly once; only its hash is persisted.
The key is bound to its `userId` and narrowed to the accounts or regions it names.

## Use the public edge

```bash
# Read the guide and the generated reference (no token required)
# https://ajs.dwgx.top:2055/docs

# Scoped API
curl -sS https://ajs.dwgx.top:2055/v1/leases \
  -H "Authorization: Bearer $SCOPED_KEY"

# HTTPS CONNECT through the lease's port. The port comes from the lease
# (connection.port) and is in the live map 13000-13010.
curl -x "https://u-10:$SCOPED_KEY@gate.dwgx.top:<leasePort>" https://example.com/
```

The username is the lease owner's panel user id; the password is a scoped key carrying
`proxy:connect` and granting the lease's account. The SOCKS5 gate stays loopback: there is no
public SOCKS5 ingress. The demo's availability is best effort and may be withdrawn.

## Experimental and disabled surfaces

The tenant portal (`AJS_TENANT_PORTAL`), orders, and the 易支付 (epay) / Stripe adapters are
experimental, AI-generated research paths. In `v0.1.0-pre` they are disabled and are **not routed
by the public edge**. They are not a payment service: no card is charged, no money is transferred,
and no support is offered. The code exists in the tree behind flags; do not deploy it as a payment
system. See [docs/EPAY-STATUS.md](docs/EPAY-STATUS.md).

The remote-ingress credential store described by older documents was removed from the code; there
is no `AJS_REMOTE_INGRESS` surface in this release. SOCKS5 gates bind loopback, and the public
data-plane listener is HTTPS CONNECT only.

## Security

Report vulnerabilities privately — never in a public issue. Use the repository's **Security →
Report a vulnerability** channel (GitHub Security Advisories), or contact the owner profile named
in [GOVERNANCE.md](GOVERNANCE.md). First response is a best effort within 72 hours. Do not run
automated scans or exploitation against the operator-run demo without written permission; use a
local instance. The enforced boundaries and the known limitations are in
[docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md); the policy is [SECURITY.md](SECURITY.md).

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. In short: one concern per
PR, tests for behaviour, every claim cites a path or command (or is marked `[UNVERIFIED]`), no
credentials or vendor artifacts, and no AI attribution in commits. Security reports go through
[SECURITY.md](SECURITY.md), not the issue tracker. Participation is covered by
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Use boundary

- **Educational and research only.** This is a protocol corpus and a reference implementation for
  interoperability study, not a proxy service or a product.
- **No affiliation.** Not affiliated with, authorised by, or endorsed by 爱加速 / AJiaSu or any
  vendor. Vendor marks belong to their owners.
- **No vendor material.** No vendor binaries, installers, drivers, captures, dumps, or credentials
  are committed here.
- **Authorised accounts only.** Use only accounts you own or are explicitly authorised to use; you
  are responsible for the vendor's terms of service and for local law.
- **Invite-only demo.** The live instance is operator-run; keys are admin-issued, there is no
  signup, no SLA, and no resale. The demo may be withdrawn at any time.
- **Experimental payments.** The epay/Stripe/orders/tenant-portal paths are experimental research
  code, disabled and not on the public edge in `v0.1.0-pre`; no charges, no support.
- **MIT, no warranty.** The licence covers this repository's code only — not any vendor's software,
  service, data, or marks.
- **Private security reports.** Do not open a public issue; see [SECURITY.md](SECURITY.md).
- **No public SOCKS5.** The public edge is HTTPS CONNECT only.
- **Resale needs separate written vendor authorisation** covering the operator's accounts, the
  service resold, and the jurisdictions involved; keep it on file. Without it this is
  single-operator self-use: no resale, no shared access, no published endpoints.
- **No bypass.** Do not use this to bypass vendor account limits or to hide the origin of abusive
  traffic.

The full text is [DISCLAIMER.md](DISCLAIMER.md).

## Evidence

```bash
# OpenAPI counts: 120 paths, 148 operations, 200 schemas
python -c "import json;d=json.load(open('docs/openapi.json'));print(len(d['paths']),sum(1 for p in d['paths'].values() for m in p if m in ('get','post','put','patch','delete')),len(d['components']['schemas']))"

# Scoped surface: 54 operations carry x-required-scope
python -c "import json;d=json.load(open('docs/openapi.json'));ops=[o for p in d['paths'].values() for m,o in p.items() if m in ('get','post','put','patch','delete')];print(sum(1 for o in ops if 'x-required-scope' in o))"

# Test functions / tests (counts are not pinned here; measure them)
grep -r '^func Test' daemon --include='*_test.go' | wc -l
grep -r '^func Test' emulator --include='*_test.go' | wc -l
grep -r '^func Test' sdk/go --include='*_test.go' | wc -l
find dashboard/tests -name '*.test.ts*' | wc -l
cd dashboard && npx vitest list | wc -l
```

## License

MIT — see [LICENSE](LICENSE). Copyright (c) 2026 dwgx.

Third-party components and data keep their own terms and are attributed in [NOTICE](NOTICE): the
Morphicons-derived icon-morph core (MIT, © 2026 Guillermo), Lucide and Feather icon data
(ISC/MIT), the DataV GeoAtlas-derived map assets (AMAP/DataV terms, not MIT), and the Go module
dependencies. The panel design uses [Resin](https://github.com/Resinat/Resin) unmodified under its
MIT licence; it is not redistributed here.

Use of the AJiaSu service remains subject to the vendor's terms of service. The MIT licence grants
no rights to the vendor's software, service, data, or marks.
