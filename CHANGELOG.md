# Changelog

This file is an index, not the authority. A tag and its attached artifacts are authoritative for
what shipped. Counts below were measured at the tag; re-run the commands in
[README.md](README.md#evidence) if you need today's number.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.1.0-pre] — 2026-10-28

First public pre-release. **Not production-stable.** This is an educational and research
release; see [DISCLAIMER.md](DISCLAIMER.md).

### Added

- Public release documentation set: [README.md](README.md), [DISCLAIMER.md](DISCLAIMER.md),
  [SECURITY.md](SECURITY.md), [CONTRIBUTING.md](CONTRIBUTING.md),
  [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), [GOVERNANCE.md](GOVERNANCE.md),
  [PROTOCOL.md](PROTOCOL.md), [ROADMAP.md](ROADMAP.md), [CITATION.cff](CITATION.cff), and the
  issue and pull-request templates under [.github/](.github/).
- A private security intake and an explicit in-scope / out-of-scope list.
- An explicit use boundary and an operator-run demo statement.

### Served

- **Scoped keys.** `ajs_sk_` keys are admin-issued, bound to a `userId`, and narrowed by
  per-route scopes, `accountIds`, and `regions`. 54 operations carry `x-required-scope` in the
  contract.
- **Leases.** Create and revoke with `Idempotency-Key`, one active lease per user and platform,
  quota/concurrency/capacity refusals.
- **Gate.** Loopback SOCKS5 and HTTP CONNECT admission with key/lease checks, allowlists, and
  flow metering. The public edge is TLS-terminated HTTPS CONNECT only; there is no public SOCKS5.
- **Dashboard.** Operator console with mock and live build modes; the public docs bundle ships at
  `/docs` in the same static build.
- **SDKs.** Go (`ajssdk`) and TypeScript (`@ajs/sdk`) clients with typed errors and retry/backoff.
- **OpenAPI 3.1 contract.** 120 paths, 148 operations and 200 schemas.
- **Tests.** The daemon, emulator, dashboard, and SDK suites run offline. The Go SDK has 38 test
  functions; the TypeScript SDK has 24 tests.

### Live demonstration

- API and documentation: `https://ajs.dwgx.top:2055` and `https://ajs.dwgx.top:2055/docs`.
- HTTPS CONNECT proxy: `gate.dwgx.top`, live map `13000-13010`, one port per running gate.

The demo is operator-run and invite-only. Keys are issued by the operator; there is no signup, no
SLA, and no resale. It may be withdrawn at any time. No written vendor authorization is on file
for the public multi-user CONNECT edge.

### Live hardening (2026-10-28)

- The live daemon now listens on `127.0.0.1:2261`; its unit sets `NoNewPrivileges=yes`,
  `PrivateTmp=yes`, `ProtectSystem=full`, `ProtectHome=yes` and `ReadWritePaths=<state>`. It
  still runs as root and without `AJS_RBAC_STRICT`.
- The live public API edge now applies 120 r/m with burst 60 per IP, 20 connections/IP, 400
  total, a 2 MB body cap, CSP/frame/referrer/permissions headers, `server_tokens off` and
  ECDHE-AEAD TLS 1.2 ciphers.
- The HMC internal vhost accepts only `10.99.0.1` and `127.0.0.1`; a LAN `/v1/whoami` answers
  `403` and the VPS path `401`. The chisel authfile is anchored to `127.0.0.1:14000-14010`.
- `/metrics` answers `401` at the public edge (was `404`); `/v1/metrics` is `401`; `/readyz`
  remains `404`; `/docs` is `200`.
- The live environment no longer carries `AJS_EPAY_*`; the daemon logs `epay_disabled` and
  `stripe_webhook_disabled`, and the portal routes are unregistered.

### Disabled and experimental

- The tenant portal (`AJS_TENANT_PORTAL`), orders, and the 易支付 (epay) / Stripe adapters are
  experimental, AI-generated research paths. They are disabled and are not part of the public
  edge in this release. They are not a payment service: no charges and no support. The code
  exists in the tree behind flags. See [docs/EPAY-STATUS.md](docs/EPAY-STATUS.md).
- The remote-ingress credential store described by older documents was removed from the code;
  there is no `AJS_REMOTE_INGRESS` surface in this release.
- The paid-tenant and public-SOCKS5 directions are closed.

### Known gaps

The full list with evidence is in [docs/releases/v0.1.0-pre.md](docs/releases/v0.1.0-pre.md).
Highlights:

- The live deployment is not reproducible from the production kit: the running unit,
  environment, and public vhost differ from the reviewed kit. See [ROADMAP.md](ROADMAP.md).
- Multi-account coexistence on one daemon, Resin live publish, and two-slot leases remain
  `[UNVERIFIED]` against the vendor.
- The live binary `ajs-accountd-61625b0` predates the 10 s entitlement watch (AZ-01), so on the
  live host revocation still only stops new sessions until it is redeployed.
- The live edge limits and security headers were applied on 2026-10-28 (120 r/m burst 60,
  20 conn/IP, 400 total, 2 MB body, CSP/frame/referrer/permissions, `server_tokens off`,
  ECDHE-AEAD TLS); the vhost is still hand-written and not reproduced by the kit.

### Tag semantics

`v0.1.0-pre` is the first tag named as a release. The earlier local tags
(`v0.1.0-archive` … `v0.7.0-live`) are milestone checkpoints, not releases, and are not part of
the version series. A `-pre` tag is a prerelease: no stability promise, no `:latest`, and the
GitHub Release must be marked `prerelease`. The tag workflows gate `:latest` promotion and the
GitHub `prerelease` flag on a tag containing `-`; their execution remains `[UNVERIFIED]` until a
remote exists.
