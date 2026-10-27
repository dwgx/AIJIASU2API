# AIJIASU2API

> **Disclaimer — read before use**
>
> - **Educational and research only.** This is a protocol corpus and a reference implementation
>   for interoperability study, not a proxy service or a product.
> - **No affiliation.** This project is not affiliated with, authorised by, or endorsed by
>   爱加速 / AJiaSu or any vendor. Vendor marks belong to their owners.
> - **No vendor material.** No vendor binaries, installers, drivers, captures, dumps, or
>   credentials are committed here.
> - **Authorised accounts only.** Use only accounts you own or are explicitly authorised to use.
>   You are responsible for the vendor's terms of service and for local law.
> - **Invite-only demo.** The live instance is operator-run. Keys are admin-issued `ajs_sk_`
>   scoped keys; there is no signup, no SLA, and no resale. The demo may be withdrawn at any time.
> - **Experimental payments.** The epay/Stripe/orders/tenant-portal paths are experimental,
>   AI-generated research code. They are disabled and are not part of the public edge in
>   `v0.1.0-pre`; this is not a payment service, with no charges and no support.
> - **MIT, no warranty.** The licence covers this repository's code only — not any vendor's
>   software, service, data, or marks.
> - **Private security reports.** Do not open a public issue; see [SECURITY.md](SECURITY.md).
> - **No public SOCKS5.** The public edge is HTTPS CONNECT only.
>
> The full text is [DISCLAIMER.md](DISCLAIMER.md).

AIJIASU2API is a research corpus plus the artefacts built on it: an operator console
(`dashboard/`), a Go control plane (`daemon/`), Go and TypeScript SDKs (`sdk/`), a compose
deployment (`deploy/`), and an offline protocol emulator (`emulator/`). The retained capability is
the **AJiaSu → API** path: an authorised AJiaSu account becomes a scoped proxy API with a
selectable egress IP.

## Release status

`v0.1.0-pre` is a **pre-release**: usable for evaluation and research, not production-stable. What
changed and what is intentionally absent is in [CHANGELOG.md](CHANGELOG.md); the near-term plan is
in [ROADMAP.md](ROADMAP.md).

The public demonstration edge is live:

- API and rendered documentation: `https://ajs.dwgx.top:2055` and
  `https://ajs.dwgx.top:2055/docs`
- HTTPS CONNECT proxy: `gate.dwgx.top`, live map `13000-13010`

It is operator-run and invite-only, requires an `ajs_sk_` key issued by the operator, and may be
withdrawn without notice. There is no self-service signup and no SLA. The public API edge applies
120 requests/min with burst 60 per IP, 20 connections/IP, 400 total and a 2 MB body cap;
`/metrics` and `/v1/metrics` require a `metrics:read` key.

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
| Test suites | The daemon, emulator, dashboard, and SDK suites run offline. The dashboard is 54 files / 475 cases, passing 475/475 under Vitest 5; the Go SDK has 38 test functions; the TypeScript SDK has 24 tests. | commands below |

The live smoke recorded in [docs/releases/v0.1.0-pre.md](docs/releases/v0.1.0-pre.md) exercised
one scoped-key read, one lease create, `/docs`, and one HTTPS CONNECT. Everything else in the
table is covered offline.

The daemon, emulator, dashboard, and SDK suites run offline: no vendor account, vendor network
call, or Resin process is required.

## Quick start

### Prerequisites

- **Go 1.26.10 or newer.** The module files declare `go 1.26.10`.
- **Node.js 24** and npm.
- Git, and a POSIX shell for `deploy/package.sh`.

Building and testing need no vendor account and no network access to the vendor.

### Build and test

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

### Run locally

```bash
# Terminal 1 — control plane on loopback only
cd daemon
AJS_LISTEN=127.0.0.1:2261 AJS_ALLOW_INSECURE=1 go run ./cmd/ajs-accountd

# Terminal 2 — dashboard against the loopback daemon
cd dashboard
VITE_USE_MOCK=false VITE_API_BASE=http://127.0.0.1:2261 npm run build
npx vite preview --host 127.0.0.1 --port 18082
```

`AJS_ALLOW_INSECURE=1` is a loopback-only lab switch; startup refuses it on a non-loopback bind.
For a real deployment, set `AJS_ADMIN_TOKEN` to at least 32 random characters (for example
`openssl rand -hex 32`) and terminate authentication in a BFF or reverse proxy. Never bake a token
into a `VITE_*` variable: every `VITE_*` value is readable in the built bundle.

### Issue an operator key

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

### Use the public edge

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

## Documentation

| Document | Purpose |
|---|---|
| [DISCLAIMER.md](DISCLAIMER.md) | affiliation, authorised use, demo limits, warranty |
| [SECURITY.md](SECURITY.md) | private vulnerability reporting and scope |
| [CONTRIBUTING.md](CONTRIBUTING.md) | one concern per PR, tests, evidence, no AI attribution |
| [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) | Contributor Covenant 2.1 |
| [GOVERNANCE.md](GOVERNANCE.md) | single-maintainer / BDFL model |
| [PROTOCOL.md](PROTOCOL.md) | how changes and releases move (not the wire protocol) |
| [CHANGELOG.md](CHANGELOG.md) | release index, including `v0.1.0-pre` |
| [ROADMAP.md](ROADMAP.md) | honest near-term work and explicitly closed directions |
| [CITATION.cff](CITATION.cff) | how to cite this release |
| [docs/API-GUIDE.md](docs/API-GUIDE.md) | scoped-key API quickstart, scopes, curl examples |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | components, ports, trust boundaries, live topology |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | install, upgrade, health, backup, rollback |
| [docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md) | what is enforced, where, and what is not protected |
| [docs/PRIVACY.md](docs/PRIVACY.md) | stored data, retention, and operator responsibilities |
| [docs/EPAY-STATUS.md](docs/EPAY-STATUS.md) | why the payment and tenant surfaces are off |
| [docs/RELEASE.md](docs/RELEASE.md) | version scheme, release gates, artifacts, rollback |
| [docs/releases/v0.1.0-pre.md](docs/releases/v0.1.0-pre.md) | full release notes and known limitations |
| [docs/RBAC.md](docs/RBAC.md) | operator RBAC contract |
| [docs/PANEL.md](docs/PANEL.md) | historical control-plane design baseline |
| [docs/openapi.json](docs/openapi.json) | OpenAPI 3.1 contract (source of truth) |
| [sdk/README.md](sdk/README.md) | Go and TypeScript clients |
| [deploy/README.md](deploy/README.md) | compose deployment |
| [deploy/packaging.md](deploy/packaging.md) | artifact build, manifest, checksums |
| [dashboard/README.md](dashboard/README.md) | console manual |
| [daemon/README.md](daemon/README.md) | daemon and operator manual |
| [emulator/README.md](emulator/README.md) | offline Tier-2 FV emulator |
| [ARCHIVE.md](ARCHIVE.md) | archive map: retained, experimental, gaps |
| [NOTICE](NOTICE) | third-party software, icon, and map-data attribution |
| [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md) | notices shipped with the release artifacts |

The wire-protocol research record is [research/40-protocol/PROTOCOL.md](research/40-protocol/PROTOCOL.md);
the root [PROTOCOL.md](PROTOCOL.md) is the project collaboration and release protocol.

## Experimental and disabled surfaces

The tenant portal (`AJS_TENANT_PORTAL`), orders, and the 易支付 (epay) / Stripe adapters are
experimental, AI-generated research paths. In `v0.1.0-pre` they are disabled and are **not routed
by the public edge**. They are not a payment service: no card is charged, no money is transferred,
and no support is offered. The code exists in the tree behind flags; do not deploy it as a payment
system. See [docs/EPAY-STATUS.md](docs/EPAY-STATUS.md) for what exists, what is configured, and
what is deliberately off.

The remote-ingress credential store described by older documents was removed from the code; there
is no `AJS_REMOTE_INGRESS` surface in this release. SOCKS5 gates bind loopback, and the public
data-plane listener is HTTPS CONNECT only.

## Security

Report vulnerabilities privately — never in a public issue. Use the repository's **Security →
Report a vulnerability** channel (GitHub Security Advisories), or contact the owner profile named
in [GOVERNANCE.md](GOVERNANCE.md). First response is a best effort within 72 hours. Do not run
automated scans or exploitation against the operator-run demo without written permission; use a
local instance.

The 2026-10-24 audit at [research/83-audit-bench/SECURITY.md](research/83-audit-bench/SECURITY.md)
is a research record, not this policy.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. In short: one concern per
PR, tests for behaviour, every claim cites a path or command (or is marked `[UNVERIFIED]`), no
credentials or vendor artifacts, and no AI attribution in commits. Security reports go through
[SECURITY.md](SECURITY.md), not the issue tracker. Participation is covered by
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Use boundary

This project is for operators driving accounts they own or are explicitly authorised to use.
Commercial resale or automation is permitted only under a separate written authorisation from the
vendor that covers the operator's accounts, the service being resold, and the jurisdictions
involved; keep that authorisation on file. Without it, the project is single-operator self-use
only: no resale, no sharing of access, no published endpoints, no bypassing vendor account limits,
and no use to hide the origin of abusive traffic. The operator is responsible for the vendor's
terms of service and for local law.

## Evidence

```bash
# OpenAPI counts
python - <<'PY'
import json
d = json.load(open("docs/openapi.json", encoding="utf-8"))
print(len(d["paths"]), sum(1 for p in d["paths"].values() for m in p
      if m in {"get","post","put","patch","delete","head","options"}))
PY
# -> 120 148

# Test functions / tests (counts are not pinned here; measure them)
grep -r '^func Test' daemon --include='*_test.go' | wc -l
grep -r '^func Test' emulator --include='*_test.go' | wc -l
grep -r '^func Test' sdk/go --include='*_test.go' | wc -l
find dashboard/tests -name '*.test.ts*' | wc -l
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
