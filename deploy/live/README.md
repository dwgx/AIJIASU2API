# Live deployment as-built (2026-10-28)

These are the **actual configs serving `ajs.dwgx.top` and `gate.dwgx.top` on 2026-10-28**, not the
reviewed production kit. They are byte-for-byte copies of the files installed on the two hosts,
captured on 2026-10-28 after the edge hardening pass, and are kept here so the running deployment
is reproducible from this tree.

The reviewed, reproducible kit is still [`deploy/production/`](../production/README.md). It is a
different shape (no VPS, no chisel, no WireGuard, `:443` instead of `:2055`) and remains the
target state; this directory is a record of what is deployed, not a template to deploy.

**Reference only. Nothing here is auto-applied.** No script in this repository copies these files
to a host. `deploy/package.sh` archives the tracked `deploy/` tree, so these files ride along
inside `deploy-kit.tar.gz` as inert data, and `deploy/production/check.sh` reads only
`deploy/production/`. Applying a change means editing the host file, running `nginx -t`, and
reloading — the files here must be re-synced afterwards or they drift.

**No `.env`, key, password, hash or certificate material is included.** The unit references
`EnvironmentFile=/srv/dwgxhomecloud/scratch/ajs-deploy-20261024/daemon/.env` by path only; that
file and the chisel authfile (which carries the tunnel credential) stay on the hosts.

## Host paths

| File here | Host | Host path | Role |
|---|---|---|---|
| `vps/nginx.conf` | VPS `vps-v9hw3m-6114` (`152.175.35.40`) | `/etc/nginx/nginx.conf` | `http`-level edge zones (`limit_req_zone`, `limit_conn_zone`), 429 statuses, `server_tokens off`, includes `conf.d/`, `sites-enabled/`, `stream-enabled/` |
| `vps/ajs-public.conf` | VPS | `/etc/nginx/sites-available/ajs-public.conf`, enabled from `sites-enabled/` | public TLS vhost on `:2055`: 41-path scoped allowlist, `/docs` from `/var/www/ajs`, upstream `10.99.0.2:80` over WireGuard |
| `hmc/ajs-public-internal.conf` | HMC (`10.99.0.2`, LAN `192.168.11.4`) | `/etc/nginx/sites-available/ajs-public-internal.conf`, enabled from `sites-enabled/` | internal origin for `ajs.dwgx.top`: peer allowlist, static `/healthz`, `/tunnel`, `/metrics` and `/v1/` to `127.0.0.1:2261` |
| `hmc/ajs-accountd.service` | HMC | `/etc/systemd/system/ajs-accountd.service` | daemon unit; binary and state live under `/srv/dwgxhomecloud/scratch/` |
| `hmc/chisel-filter.jq` | HMC | filter applied to the chisel authfile `/opt/ajs/chisel-users.json` (`chisel server --authfile`) | rewrites each account's allowed forward list to the eleven loopback gate ports |

`hmc/chisel-filter.jq` is a filter, not an installed file: it is the recipe that produced the
restricted authfile, and the authfile itself is excluded because it contains the tunnel
credential.

## Ports

| Port | Host / bind | Serves |
|---|---|---|
| `2055/tcp` | VPS, public | TLS 1.2/1.3 API and `/docs` (`listen 2055 ssl http2`; no IPv6 listener) |
| `13000-13010/tcp` | VPS, public | TLS stream gate, from `stream-enabled/` (not in this directory) |
| `15000-15010` | VPS loopback | chisel client local forwards |
| `80/tcp` | HMC, WireGuard peer and LAN | internal vhost; only `10.99.0.1` and `127.0.0.1` pass the allowlist |
| `8080` | HMC `127.0.0.1` | chisel server, published only through the vhost's `/tunnel` |
| `2261` | HMC `127.0.0.1` | `ajs-accountd`; the internal vhost proxies `/v1/` and `/metrics` here |
| `14000-14010` | HMC `127.0.0.1` | gate HTTP CONNECT; the only chisel forward targets |
| `13000-13010` | HMC `127.0.0.1` | gate SOCKS5; never published |
| `18082` | HMC | live dashboard (not covered by these files) |

The VPS vhost's upstream is `10.99.0.2:80` — the HMC WireGuard address — so the API hop is
WireGuard, not chisel. Chisel owns only the eleven gate forwards.

## Backups

Every file above was backed up on its host before the 2026-10-28 change. The backups carry the
stamp **`20261028-033728`** (2026-10-28 03:37:28). Restoring a pre-change config means
restoring that stamped copy, then `nginx -t` and a reload; the daemon unit needs
`systemctl daemon-reload` and a restart, and the chisel change needs the tunnel client
reconnected.

## What changed on 2026-10-28

1. **Edge limits** (`vps/nginx.conf`, `vps/ajs-public.conf`): `limit_req_zone
   ajs_public_api` at 120 r/m per IP applied with `burst=60 nodelay`, `limit_conn` 20 per IP and
   400 per server name, `client_max_body_size 2m`, and 429 for both refusals. The daemon's 60/min
   per-key bucket stays authoritative below this edge budget. `proxy_read_timeout 150s` covers
   `select-node` with `timeoutSeconds` up to 120; the SSE location overrides it with 1h.
2. **Headers and TLS** (`vps/nginx.conf`, `vps/ajs-public.conf`): `server_tokens off`; HSTS,
   `X-Content-Type-Options`, `X-Frame-Options DENY`, `Referrer-Policy no-referrer`,
   `Permissions-Policy` and a CSP on every response; TLS 1.2 limited to ECDHE AEAD suites with
   `ssl_prefer_server_ciphers off`.
3. **HMC allowlist** (`hmc/ajs-public-internal.conf`): `allow 10.99.0.1; allow 127.0.0.1; deny
   all;`, so only the VPS WireGuard peer and localhost reach the internal origin. LAN clients no
   longer reach admin, portal or payment paths through it.
4. **Chisel allowlist** (`hmc/chisel-filter.jq`): each authfile entry's value is replaced with the
   eleven anchored patterns `^127\.0\.0\.1:1400[0-9]$` and `^127\.0\.0\.1:14010$`, so the account
   can only request the loopback gate ports.
5. **Loopback bind**: `AJS_LISTEN=127.0.0.1:2261` in the daemon environment file (not in this
   directory), so the admin API is no longer reachable from the LAN.
6. **systemd hardening** (`hmc/ajs-accountd.service`): `NoNewPrivileges=yes`, `PrivateTmp=yes`,
   `ProtectSystem=full`, `ProtectHome=yes`, `ReadWritePaths` limited to the state directory,
   `LimitNOFILE=65536`, `Restart=on-failure`.
7. **EPAY disabled**: the `AJS_EPAY_*` values were emptied in the daemon environment (not in this
   directory) and the daemon logs `epay_disabled`; `POST /v1/payments/stripe` stays at 503. The
   payment code remains in-tree as experimental research and is not a payment service.

## What is still divergent

1. **Root.** The unit has no `User=`/`Group=`; the daemon runs as root, `ProtectSystem=full`
   rather than `strict`, and there is no `StateDirectory=`.
2. **No strict RBAC.** `AJS_RBAC_STRICT` is unset, so compatibility mode still accepts the legacy
   shared `AJS_ADMIN_TOKEN`, and `X-AJS-Operator` is forgeable by any holder of that bearer.
3. **SSE bearer.** The dashboard vhost injects a hardcoded server-side admin bearer for exactly
   `/v1/events/stream` and `/v1/debug/stream`, limited only by a source-IP allowlist
   (`127.0.0.1`, `192.168.11.2`) — no per-user login on those two routes.
4. **WireGuard plaintext.** The VPS-to-HMC API hop is plaintext HTTP inside WireGuard
   (`10.99.0.1` to `10.99.0.2:80`); TLS terminates at the VPS and there is no end-to-end
   TLS/mTLS. A party able to read or alter the tunnel path can observe or modify bearer
   credentials and responses.

The kit's other differences — `:443` instead of `:2055`, the stream map
`13000-13010 -> 14000-14010` instead of chisel `15000-15010`, `User=ajs`, `AJS_RBAC_STRICT=1`,
`/etc/ajs/accountd.env` — are target state, not deployment options. See
[`docs/SECURITY-MODEL.md`](../../docs/SECURITY-MODEL.md#known-limitations) and
[`deploy/production/README.md`](../production/README.md#live-deployment-2026-10-27).
