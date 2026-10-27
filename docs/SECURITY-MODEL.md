# Security model

Status: current for `v0.1.0-pre` (2026-10-28). This page states what the system enforces, where
it enforces it, and what it does **not** protect. It is derived from the release's three
read-only audits (edge, authorization, supply chain; 2026-10-28) and from source review of the
commit in this tree; the live posture was observed with read-only probes and no privileged
credential. No secret value appears here.

## Trust boundaries

```text
internet ── TLS ──> [VPS edge] ── WireGuard (plaintext HTTP inside) ──> [HMC]
                      │                                                ├── nginx :80 (allow 10.99.0.1, 127.0.0.1) -> daemon 127.0.0.1:2261
                      └── chisel 15000-15010 -> 127.0.0.1:14000-14010 ─> └── gate HTTP CONNECT 127.0.0.1:14000-14010
LAN 192.168.11.0/24 ── plaintext ──> [HMC] nginx :80 (403 for /v1/), dashboard :18082
loopback ──> gate SOCKS5 :13000-13010, admin origin :8443
```

| Boundary | What crosses it | Enforced by |
|---|---|---|
| internet → VPS `:2055` | scoped-key API and `/docs` | TLS, 120 r/m burst 60 per IP, 20 conn/IP, 400 total, 2 MB body cap, 41-path allowlist, terminal `404`; the daemon's `routeScopes` is the authority |
| internet → VPS `:13000-13010` | TLS HTTP `CONNECT` | gate RFC 1929 + lease/account/allowlist/quota at `CONNECT` |
| VPS → HMC | API requests, gate byte streams | WireGuard peer authentication; **no** application-layer authentication of the HMC |
| HMC LAN → nginx `:80` | admin/portal/payment `/v1/` namespace | vhost allows only `10.99.0.1` + `127.0.0.1`; LAN answers `403`; the daemon itself is loopback-only |
| loopback | gate listeners, admin origin | Unix socket ownership; a local root/file-read caller is outside this boundary |
| daemon → vendor | login, control, FV data | outbound only; opt-in login with a pinned control certificate |

## Credential worlds

These are separate and do not convert into each other:

| Credential | Used by | Reaches | Notes |
|---|---|---|---|
| Operator Basic + mapped bearer | a person on the admin origin | admin API | nginx overwrites client `Authorization`/`X-AJS-Operator`; in strict mode `X-AJS-Operator` is not an identity source |
| Legacy shared bearer `AJS_ADMIN_TOKEN` | migration only | admin API | accepted only when strict RBAC is **off**; never in strict mode |
| Scoped key `ajs_sk_…` | customers, automation | scoped API routes, gate | SHA-256 at rest, shown once; per-route scopes, `accountIds`/`regions` narrowing, 60 r/min default |
| Lease credential | proxy clients | gate | not a stored secret: the password is the caller's own scoped key carrying `proxy:connect` |
| Portal cookie `__Host-ajs_portal` | customers | `/v1/portal/*` | HttpOnly, `Secure`, CSRF token; portal is **off** in this release |
| Payment signature | epay/Stripe servers | payment callbacks | provider signature verification; experimental, off |
| chisel account (live only) | VPS tunnel client | chisel forwards | one account; authfile anchored to `127.0.0.1:14000-14010` (2026-10-28) |

## Scoped keys, leases and the gate

- A scoped key carries **one exact scope per route** from the daemon's `routeScopes` table.
  An unmapped route is admin-only and refused to a non-admin key before the handler runs.
- A key holding the `admin` scope **can** reach admin routes. This is an intentional bypass,
  not a bug; do not describe non-admin keys as unable to reach "every unscoped route".
- `accountIds`/`regions` narrow **reads** (account-scoped GETs). They do not narrow writes: a
  `catalog:write` or `leases:write` key applies to every account.
- One active lease per user and platform. `POST /v1/leases` is idempotent under
  `Idempotency-Key` and re-checks the key subject and account grant on replay.
- The gate re-checks key, lease, account, user allowlist and monthly quota at `CONNECT`. A
  revoked or expired entitlement fails the next handshake and any pending `CONNECT`, and — at
  this commit — an established flow is revalidated every 10 s and closed when the entitlement
  is gone. The live binary predates that watch (see limitation 1).
- `AJS_GATE_AUTH=loopback` is an explicit no-auth compatibility wire (no user identity, no
  per-user policy). Leave it unset.
- The gate never binds a public address: SOCKS5 on `127.0.0.1:13000-13010`, HTTP `CONNECT` on
  `127.0.0.1:14000-14010`, both with loopback validation in the daemon.

## The edge allowlist is the enforcement point

The VPS `:2055` vhost lists 54 scoped operations on 41 paths, denies methods with
`limit_except`, and ends in `location / { return 404; }`. The tested admin, portal, payment,
OpenAPI and `whoami` routes are refused at the edge, including encoded-slash and dot-segment
variants. The vhost also bounds unauthenticated load: 120 requests/min with burst 60 per IP,
20 connections/IP, 400 connections total, and a 2 MB request body.

Two qualifications that the marketing copy usually omits:

- **The allowlist is an edge property, not an origin property.** The HMC internal vhost proxies
  every `/v1/` path to `127.0.0.1:2261`, but only for `10.99.0.1` (the VPS WireGuard peer) and
  `127.0.0.1`; every other source is denied. A LAN `/v1/whoami` answers `403`, while the same
  path through the VPS answers `401` from the daemon. A caller inside the allowed sources
  reaches the whole API namespace; only the daemon's auth/RBAC stops it.
- **`/metrics` is now served end-to-end, with auth.** The HMC origin gained an exact `/metrics`
  location on 2026-10-28, so the public edge answers `401` unauthenticated (it answered `404`
  before) and requires a `metrics:read` key. `/v1/metrics` is also `401`. `/readyz` is still
  not public at `:2055` (`404`).

## Admin, CSRF and browser origins

- Unsafe admin requests need `X-AJS-CSRF: 1`. A request carrying `Origin` must match
  `AJS_ADMIN_ORIGINS` exactly; requests without `Origin` (CLI/SDK) still need the CSRF header.
- The SPA is **not** an authorization boundary. It has no server-side session; daemon denials
  are authoritative even if a viewer can see an action in the UI.
- In strict mode, operator tokens are hashed in `/etc/ajs/admin-operators.hashes`
  (root:ajs `0640`) and the plaintext bearer map is root:root `0600`, read by the nginx master.
  Role/token removal takes effect after nginx reload **and** daemon restart.

## Applied on the live host (2026-10-28)

These changes were applied after the 2026-10-27 cutover; they are live properties, not tree
properties.

- **Daemon bind and unit.** `AJS_LISTEN=127.0.0.1:2261`; `NoNewPrivileges=yes`,
  `PrivateTmp=yes`, `ProtectSystem=full`, `ProtectHome=yes`, `ReadWritePaths=<state>`. Still
  root, still no `AJS_RBAC_STRICT`.
- **Payments/portal.** `AJS_EPAY_*` removed from the live environment; the daemon logs
  `epay_disabled` and `stripe_webhook_disabled`, and the portal routes are unregistered.
- **Public API edge.** 120 r/m burst 60 per IP, 20 connections/IP, 400 total; 2 MB body cap;
  CSP, frame, referrer and permissions headers; `server_tokens off`; ECDHE-AEAD TLS 1.2
  ciphers with `ssl_prefer_server_ciphers off`.
- **HMC internal origin.** Only `10.99.0.1` and `127.0.0.1` are allowed; LAN `/v1/whoami`
  answers `403`, the VPS path answers `401`.
- **chisel.** Authfile anchored to `127.0.0.1:14000-14010`; tunnel reconnected; direct chisel
  gate check `200`.
- **Probes.** `/metrics` `401` (was `404`), `/v1/metrics` `401`, `/readyz` `404`, `/docs` `200`;
  gate `CONNECT` `407` unauthenticated and `200` with explicit Basic auth.

## Known limitations

These are release-blocking or explicitly accepted gaps. Audit ids (`AZ-…`, `E-…`) and ticket
numbers are given where one exists.

1. **Revocation latency, and the live binary predates the fix (AZ-01).** At this commit the
   gate revalidates the principal every `EntitlementInterval` (default **10 s**) while a flow
   relays; a revoked or expired key or lease closes both sockets (`daemon/internal/gate/`
   `server.go` `watchEntitlement`, wired in `runtime/nodes.go`). The VPS one-hour proxy idle
   timeout is **not** the entitlement deadline. The live binary is `ajs-accountd-61625b0`,
   which predates AJS-476, so on the live host revocation still only stops new handshakes and
   pending `CONNECT`s until it is redeployed. The fix is covered by gate tests
   (`http_test.go`, `gate_identity_test.go`) and has not been exercised on the live host.
2. **Strict RBAC is not live.** The live daemon sets `AJS_ADMIN_OPERATORS_FILE` but not
   `AJS_RBAC_STRICT`, so it runs in compatibility mode and still accepts the legacy shared
   bearer. The public API vhost's documented precondition (`AJS_RBAC_STRICT=1`, no
   `AJS_ALLOW_INSECURE`) is therefore not met on the live host.
3. **The live daemon still runs as root.** It now binds `127.0.0.1:2261` and the unit carries
   the sandbox directives above, but there is no `User=`, no `StateDirectory=`, and the state
   lives under a scratch path. The admin API is no longer reachable from the LAN, but root
   compromise of the daemon is host compromise.
4. **Dashboard SSE uses a hardcoded server-side bearer.** The live dashboard injects an admin
   bearer only for exact `/v1/events/stream` and `/v1/debug/stream`, with a source-IP allowlist
   (`127.0.0.1`, `192.168.11.2`). Any process on an allowed workstation can read those streams
   without a separate user login. Other dashboard API routes forward the caller's header.
5. **The VPS→HMC API hop is plaintext HTTP inside WireGuard.** TLS terminates at the VPS.
   WireGuard authenticates the endpoints, but there is no end-to-end TLS/mTLS, so a party who
   can read or alter the tunnel path can observe or modify bearer credentials and responses.
6. **The live public edge is not reproducible from this tree.** The `:2055` vhost and the
   chisel map are hand-written on the host: the VPS map is `13000-13010` → chisel
   `15000-15010`, with HMC-side forwards anchored to `127.0.0.1:14000-14010`; the kit maps
   `13000-13010` directly to `14000-14010`. The live HSTS also lacks `includeSubDomains`. See
   [`deploy/production/README.md`](../deploy/production/README.md#live-deployment-2026-10-27).
7. **EPAY, orders and the customer portal are experimental and off** in `v0.1.0-pre`. With
   `AJS_TENANT_PORTAL=0` the routes are not registered, and the live environment has no
   `AJS_EPAY_*` values. See [`docs/EPAY-STATUS.md`](EPAY-STATUS.md). No route charges a card.

## What is not a security boundary

- The SPA, its menus, and any client-side hiding of controls.
- The nginx allowlist at the **origin**: it is an edge control only. The HMC vhost is now
  source-restricted to `10.99.0.1` and `127.0.0.1`, but for those sources it still proxies the
  whole `/v1/` namespace.
- WireGuard alone as end-to-end authentication of the daemon.
- The one-hour proxy idle timeout as an entitlement deadline.
- `AJS_ALLOW_INSECURE=1`: a loopback lab switch that disables auth; refused on a non-loopback
  bind but still dangerous on the loopback listener.
- Local root or arbitrary file-read on the host: that caller can read the state, the master key
  and the operator hashes.

## Reporting

See [`SECURITY.md`](../SECURITY.md) for private disclosure. Do not open a public issue for a
suspected credential leak, tunnel bypass or auth bypass; rotate the affected secret first.
