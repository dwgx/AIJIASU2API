# Production deploy kit (AJS-387)

This directory is the engineering half of release gate 2: a production-like
deployment of `ajs-accountd` and the dashboard **without Docker**. It provides
nginx TLS termination, a token-free customer portal bundle, an admin origin
whose bearer never reaches a browser, a public TLS-terminated HTTP CONNECT
edge for the loopback gate, a hardened systemd unit, a firewall table that
matches `deploy/nftables/`, and `check.sh` as the gate.

Nothing here is a credential. There are no certificates, private keys, tokens
or filled-in environment files in this tree — only paths, placeholders and the
template from which the host renders its admin bearer.

Run the gate as root:

```bash
sudo -E bash deploy/production/check.sh
# check: PASS (all checks, 0 skipped)
```

The default mode is the production gate. It must run as root because the
AJS-412 renderer proof creates root-owned `0640`/`0600` artifacts, and the
gate switches to the nginx worker identity to prove that identity can read
them. A non-root production invocation exits 1 before any checks, prints the
exact `sudo -E` command, and never prints `check: PASS`. Production also
requires a real host `nginx -t`, `openssl`, `systemd-analyze`, `nft`, a
Chromium-family browser for the CSP browser boot (`AJS_CHROMIUM_BIN`, or
chromium/chrome on `PATH`), and a daemon binary for the `/readyz` proof
(`AJS_ACCOUNTD_BIN`, `ajs-accountd` on `PATH`, `daemon/ajs-accountd`, or
`go build`); a missing required tool is a `FAIL`, not a `SKIP`. For a
workstation without those tools, or for a non-root static run, use the
explicitly non-production mode:

```bash
bash deploy/production/check.sh --dev
# check: DEV-PASS (some checks skipped; not a production gate)
```

`check.sh` is read-only. It stages nginx configs in a throwaway directory with
generated self-signed certificates and a dummy token, pins the nginx PID and
all temp paths inside that directory, runs a real `nginx -t` as the invoking
user, starts that staged config to prove unknown Hosts are refused on :80/:443,
the portal redirect is canonical and the public API allowlist routes (proxied
path, admin/portal `404`, static `/healthz`, `403` on a disallowed method),
completes a TLS handshake on the public gate port with a dead backend and
asserts the served certificate, asserts the Content-Security-Policy on the
staged responses and boots `/portal/login` in headless Chromium against a
loopback fixture of the same vhosts, validates the YAML inventory, scans the
kit for committed secrets, starts a daemon binary to prove `/readyz` fails on
an unwritable state directory, verifies the systemd unit, and compares the
production firewall rule with `deploy/nftables/ajs-panel.nft`.

## What the kit contains

| Path | Purpose |
|---|---|
| `nginx/nginx.conf` | master config: MIME map, rate-limit zones, upstream, TLS defaults, includes the site configs |
| `nginx/sites-available/ajs-default.conf` | default-deny listeners: HTTP 444 / HTTPS 421 for unknown Host |
| `nginx/sites-available/ajs-portal.conf` | public customer origin, TLS, HSTS, CSP, portal-only routes |
| `nginx/sites-available/ajs-api.conf` | public API origin: TLS, deny-by-default allowlist of the 54 scoped operations, no edge credential |
| `nginx/streams-available/ajs-gate.conf` | public gate data plane: 11 TLS stream servers, 13000-13010 -> loopback HTTP CONNECT 14000-14010, no SOCKS5 |
| `nginx/sites-available/ajs-admin.conf` | private admin origin, loopback/VPN allowlist, per-operator Basic auth, proxy-injected bearer |
| `nginx/snippets/ajs-tls.conf` | shared TLS 1.2+/1.3 policy |
| `nginx/snippets/ajs-proxy-common.conf` | `X-Forwarded-For` overwrite + trusted-proxy note |
| `nginx/snippets/ajs-security-headers.conf` | HSTS, the Content-Security-Policy and the other response headers |
| `nginx/snippets/ajs-admin-token.conf.template` | template for the root-only bearer snippet |
| `nginx/snippets/ajs-admin-operator.conf` | forwards `$remote_user` as `X-AJS-Operator` to the daemon |
| `systemd/ajs-accountd.service` | hardened unit (`ProtectSystem=strict`, empty capability set, syscall filter) |
| `systemd/ajs-accountd.env.example` | exact daemon environment, placeholders only |
| `firewall/ajs-production.nft` | standalone nftables table matching the repo-owned LAN block |
| `build-bundles.sh` | builds the live, same-origin, **token-free** portal and admin bundles from a locked `npm ci` tree and verifies `ajs-build.json` |
| `render-admin-token.sh` | legacy migration-only renderer for the old shared bearer; not used by the strict vhost |
| `render-admin-htpasswd.sh` | internal helper for the combined renderer; renders Basic hashes |
| `render-admin-operators.sh` | renders Basic hashes, per-operator bearer map and daemon hash file; never prints secrets |
| `admin-operators.example` | template for `username:role:token:password` root-only inventory |
| `inventory.example.yml` | non-secret deployment inputs, validated by `check.sh` |
| `check.sh` | the release-gate check |
| `csp-smoke.sh` | headless-browser CSP smoke helper used by the gate; never starts nginx |

## Owner inputs

These are the only things the Owner must supply. Everything else is in this
tree or generated on the host.

### DNS

| Name | Record | Exposure |
|---|---|---|
| `panel.example.com` | `A` (and `AAAA` if the host has IPv6) to the host's public address | public internet |
| `api.example.com` | `A` (and `AAAA` if the host has IPv6) to the host's public address | public internet — scoped-key API on `:443` and the TLS gate edge on 13000-13010; the SOCKS5 gate stays loopback |
| `admin.example.com` | private DNS, split-horizon, or `/etc/hosts` to `127.0.0.1` | loopback or VPN only — do **not** create a public record |

Replace `panel.example.com`, `api.example.com` and `admin.example.com` in:
- `nginx/sites-available/ajs-portal.conf` (`server_name`, certificate paths)
- `nginx/sites-available/ajs-api.conf` (`server_name`, HTTP redirect, certificate paths)
- `nginx/sites-available/ajs-admin.conf` (`server_name`, certificate paths)
- `nginx/sites-available/ajs-default.conf` (the HTTPS default listener uses the portal certificate)
- `systemd/ajs-accountd.env.example` (`AJS_TENANT_ORIGIN`, `AJS_ADMIN_ORIGINS`)

`AJS_TENANT_ORIGIN` must be the exact bare origin (`https://panel.example.com`),
with no path, query or trailing slash. The daemon refuses a path and uses the
value for the portal's exact `Origin` check. `AJS_ADMIN_ORIGINS` must be the
admin SPA's exact bare origin (`https://admin.example.com:8443` in this kit);
unsafe admin requests from any other `Origin` are refused before the handler.

### Public address (gate data plane)

`nginx/streams-available/ajs-gate.conf` binds this host's public IPv4 address on
13000-13010, the address the `api.example.com` `A` record points at. It must
**not** be `0.0.0.0` and must **not** be `127.0.0.1`: `ajs-accountd` owns
`127.0.0.1:13000-13010` for the SOCKS5 gate, so a wildcard listen on the same
numbers fails with `EADDRINUSE` — or, if nginx starts first, pushes the
daemon's gate allocator to 13011+ and every port in the static map points at
the wrong node. Replace `203.0.113.10` (an RFC 5737 documentation address) in
all 11 servers with the real address; `nginx -t` refuses to start while it is
still a documentation address because it cannot be bound. On an IPv6-enabled
host add `listen [::]:<port> ssl;` beside each `listen` line. A NAT'd host
binds its interface address (the one DNAT forwards the public address to), not
the public address itself.

### Host boundary

`nginx/sites-available/ajs-default.conf` installs the explicit
`default_server` listeners. Any request whose `Host` is not
`panel.example.com` (or the admin `server_name` on its private listener) is
refused before it reaches a virtual host: HTTP closes the connection with
`444`, HTTPS answers `421 Misdirected Request`. The portal's HTTP-to-HTTPS
redirect uses the fixed `https://panel.example.com` origin, never `$host`, so
an attacker-chosen `Host` cannot influence a redirect even if a future config
change moves the default listener. Keep `ajs-default.conf` installed and
loaded; without it nginx falls back to the first server block.

### TLS certificates

Layout expected by the kit:

```text
/etc/ajs/tls/panel.example.com/fullchain.pem
/etc/ajs/tls/panel.example.com/privkey.pem
/etc/ajs/tls/api.example.com/fullchain.pem      # normally a symlink target
/etc/ajs/tls/api.example.com/privkey.pem        # (see the SAN note below)
/etc/ajs/tls/admin.example.com/fullchain.pem
/etc/ajs/tls/admin.example.com/privkey.pem
```

Supply them by one of:

```bash
# Let's Encrypt (HTTP-01 needs port 80 reachable). The api name is a SAN on the
# panel certificate: one issuance, two names.
sudo certbot certonly --standalone \
  -d panel.example.com -d api.example.com
sudo install -d -m 0750 /etc/ajs/tls/panel.example.com /etc/ajs/tls/admin.example.com
sudo install -m 0640 /etc/letsencrypt/live/panel.example.com/fullchain.pem /etc/ajs/tls/panel.example.com/
sudo install -m 0640 /etc/letsencrypt/live/panel.example.com/privkey.pem  /etc/ajs/tls/panel.example.com/
# ajs-api.conf reads /etc/ajs/tls/api.example.com; point it at the panel pair
# so a renewal only has to update one directory.
sudo ln -sfn panel.example.com /etc/ajs/tls/api.example.com

# Admin name is private; a self-signed cert is acceptable inside the VPN:
sudo openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout /etc/ajs/tls/admin.example.com/privkey.pem \
  -out    /etc/ajs/tls/admin.example.com/fullchain.pem \
  -subj "/CN=admin.example.com" \
  -addext "subjectAltName=DNS:admin.example.com,IP:127.0.0.1"
```

**The api name is a SAN on the panel certificate, never a second issuance.**
The only ACME HTTP-01 challenge location in this kit belongs to the portal
vhost (`ajs-portal.conf`); `ajs-default.conf` closes unknown Hosts on `:80`
with `444`, and `ajs-api.conf` deliberately has no challenge block. Issuing
`api.example.com` separately would therefore stall at the challenge step — or
someone would disable the default-deny listener to make it work, which is
worse. Extend the existing certificate instead, and verify the SAN before
enabling DNS:

```bash
openssl x509 -in /etc/ajs/tls/panel.example.com/fullchain.pem -noout -text \
  | grep -A1 'Subject Alternative Name'      # must list panel AND api
```

`api.example.com` is a *sibling* of `panel.example.com`, so the portal's HSTS
`includeSubDomains` does not cover it; `ajs-api.conf` therefore includes the
same `ajs-security-headers.conf` and sends its own HSTS.

`nginx/streams-available/ajs-gate.conf` reuses the same
`/etc/ajs/tls/api.example.com` pair on all 11 stream servers. There is no
second certificate and no second ACME challenge: renewing the panel pair and
reloading nginx renews the gate edge too. The stream servers pin
`TLSv1.2 TLSv1.3` and turn session tickets off, matching the HTTP policy in
`nginx.conf`.

The nginx master reads the pair at startup. Renewals require
`systemctl reload nginx`; certbot's deploy hook can do that. Keys must be
readable by root only (mode 0600–0640, group root).

### Secrets

Generate the shared secrets on the host; never commit or paste them into a
report:

```bash
openssl rand -hex 32   # AJS_ADMIN_TOKEN  (also the nginx-injected bearer)
openssl rand -hex 32   # AJS_MASTER_KEY   (AES-256-GCM account-password sealing)
```

Operator passwords and API tokens are separate, per-person secrets. Generate
both independently with `openssl rand -hex 32`; never reuse the legacy shared
bearer. See [`docs/RBAC.md`](../../docs/RBAC.md) for the role matrix, strict
migration and two-generation revocation procedure. The root-only source
inventory is `/etc/ajs/admin-operators` (0600), in
`username:viewer|operator|admin:token:password` format. The combined renderer
writes apr1 password hashes, a root-only nginx token map and a daemon-readable
SHA-256 token file. Password-only rotation does not revoke an API token;
remove its hash and restart the daemon as well as reloading nginx.

`AJS_MASTER_KEY` is optional only in the sense that without it account
passwords are discarded; with it, losing the key makes every sealed value
unrecoverable. Back it up with the state directory.

## Exact environment

`systemd/ajs-accountd.env.example` is installed as `/etc/ajs/accountd.env`,
root-owned `0600`. systemd reads it as root before dropping to `User=ajs`.

| Variable | Required | Value in production | Why |
|---|---|---|---|
| `AJS_LISTEN` | yes | `127.0.0.1:2261` | nginx is the only network-facing listener; the daemon stays on loopback |
| `AJS_STATE_DIR` | yes | `/var/lib/ajs` | `StateDirectory=ajs` creates it `0700`; the only writable path |
| `AJS_ADMIN_TOKEN` | migration only | unset after strict cutover | legacy shared bearer; never accepted with strict RBAC |
| `AJS_RBAC_STRICT` | yes | `1` | disables shared bearer and anonymous insecure admin access |
| `AJS_ADMIN_OPERATORS_FILE` | yes | `/etc/ajs/admin-operators.hashes` | root:ajs 0640 hash-only operator/role file; restart to reload |
| `AJS_MASTER_KEY` | recommended | 64 hex chars | seals account passwords; empty disables at-rest sealing |
| `AJS_TENANT_PORTAL` | yes | `1` | registers `/v1/portal/*`, `/v1/admin/tenants*`, `/v1/admin/orders*`, `/v1/admin/plans*` |
| `AJS_TENANT_ORIGIN` | yes | `https://panel.example.com` | exact browser origin allowed on portal signup/login and every unsafe portal request |
| `AJS_TRUSTED_PROXIES` | yes | `127.0.0.1,::1` | only nginx's loopback peers may supply `X-Forwarded-For`; never a public CIDR or `0.0.0.0/0` |
| `AJS_CORS_ORIGINS` | optional | empty | both bundles are same-origin through nginx; set only for a separate browser client |
| `AJS_ADMIN_ORIGINS` | yes | `https://admin.example.com:8443` | exact browser origin allowed on every unsafe admin request; the admin SPA is served from this origin and nginx injects the bearer. Requests without `Origin` (CLI/SDK) must send `X-AJS-CSRF: 1` |
| `AJS_LOG_LEVEL` | optional | `info` | `debug` only while diagnosing |
| `AJS_ACCOUNT_LOGIN` | optional | `0` | keep off until an account exists; `1` contacts the vendor login path |
| `AJS_CONTROL_CERT_SHA256` | with login | vendor leaf fingerprint | the control hosts use a self-signed `CN=localhost` cert; pin it when `AJS_ACCOUNT_LOGIN=1` |
| `AJS_TLS_CERT` / `AJS_TLS_KEY` / `AJS_TLS_LISTEN` | no | unset | nginx terminates TLS; the daemon's optional listener is unnecessary and stays off |

Never set `AJS_ALLOW_INSECURE=1` in production: it disables bearer auth and is
refused on a non-loopback bind, but the loopback listener would still accept
unauthenticated `/v1/*` calls from any local process.

Other daemon variables that may be supplied in `/etc/ajs/accountd.env` when
their feature is used:

| Variable | Default | Production note |
|---|---|---|
| `AJS_STATE_FILE` | `$AJS_STATE_DIR/state.json` | keep on the state volume; do not put it in the repo |
| `AJS_SNAPSHOT_INTERVAL` | `30s` | shorter means more fsyncs; longer means more loss on crash |
| `AJS_RESIN_URL` | `http://127.0.0.1:2260` | only when Resin runs on the same host |
| `AJS_RESIN_ADMIN_TOKEN` | empty | empty disables the Resin integration |
| `AJS_EPAY_GATEWAY` | empty | 易支付 base URL, must be https (or loopback for a lab) |
| `AJS_EPAY_PID` | empty | merchant id; required with gateway + key |
| `AJS_EPAY_KEY` | empty | signing key; never logged or persisted |
| `AJS_EPAY_SIGN_TYPE` | `MD5` | only MD5 is accepted when epay is configured |
| `AJS_EPAY_NOTIFY_URL` | derived | set to `https://panel.example.com/v1/payments/epay/notify` if the gateway needs an absolute URL |
| `AJS_EPAY_RETURN_URL` | derived | set to `https://panel.example.com/portal` if the gateway needs it |
| `AJS_STRIPE_WEBHOOK_SECRET` | empty | empty leaves `POST /v1/payments/stripe` at 503 |
| `AJS_GATE_AUTH` | RFC 1929 | leave unset; `loopback` is a compatibility wire only |
| `AJS_GATE_HTTP` | `1` | starts the per-node loopback HTTP CONNECT listener that `streams-enabled/ajs-gate.conf` publishes; `0`/unset leaves the public ports completing TLS and then closing |
| `AJS_GATE_HTTP_OFFSET` | `1000` | must stay 1000: the stream map hard-codes the backend at lease port + 1000 (14000-14010) |
| `AJS_FLOW_METER` | on | set `0` only if per-flow request-log rows are unwanted |

The full variable reference is in [`daemon/README.md`](../../daemon/README.md#configuration).

## Install

The examples assume Debian/Ubuntu nginx, systemd 247 or newer (for
`ProtectProc=invisible` / `ProcSubset=pid`), and a packaged
`ajs-accountd-linux-amd64` from `deploy/package.sh`. Run as root.

```bash
# 1. User, directories, binary
sudo useradd --system --user-group --home /var/lib/ajs --shell /usr/sbin/nologin ajs
# /etc/ajs is root:ajs 0750 so the daemon can traverse it to open the
# root:ajs 0640 hash file; /etc/ajs/tls stays root:root and is read by the
# root nginx master only.
sudo install -d -m 0750 -o root -g ajs /etc/ajs
sudo install -d -m 0750 -o root -g root /etc/ajs/tls
sudo install -d -m 0700 -o ajs -g ajs /var/lib/ajs
sudo install -m 0755 ajs-accountd-linux-amd64 /usr/local/bin/ajs-accountd

# 2. Environment (edit placeholders before starting)
sudo install -m 0600 -o root -g root \
  deploy/production/systemd/ajs-accountd.env.example /etc/ajs/accountd.env
sudo -e /etc/ajs/accountd.env
# Keep AJS_GATE_HTTP=1 and AJS_GATE_HTTP_OFFSET=1000 when the public gate
# edge from step 5 is enabled; without them the stream map has no backend.

# 3. systemd
sudo install -m 0644 deploy/production/systemd/ajs-accountd.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ajs-accountd
curl -fsS http://127.0.0.1:2261/healthz

# 4. Token-free bundles (dashboard/node_modules must already exist: npm ci)
bash deploy/production/build-bundles.sh --out /tmp/ajs-bundles
sudo install -d -m 0755 /var/lib/ajs/production
sudo rm -rf /var/lib/ajs/production/portal /var/lib/ajs/production/admin
sudo cp -a /tmp/ajs-bundles/portal /var/lib/ajs/production/portal
sudo cp -a /tmp/ajs-bundles/admin  /var/lib/ajs/production/admin

# 5. nginx (libnginx-mod-stream provides the public gate edge on Debian/Ubuntu)
sudo apt-get install -y nginx libnginx-mod-stream
sudo cp deploy/production/nginx/nginx.conf /etc/nginx/nginx.conf
sudo cp deploy/production/nginx/ajs-mime.types /etc/nginx/ajs-mime.types
sudo cp deploy/production/nginx/snippets/*.conf /etc/nginx/snippets/
sudo cp deploy/production/nginx/sites-available/ajs-*.conf /etc/nginx/sites-enabled/
sudo install -d -m 0755 /etc/nginx/streams-enabled
# Replace 203.0.113.10 with this host's public address first (Owner inputs).
sudo cp deploy/production/nginx/streams-available/ajs-gate.conf /etc/nginx/streams-enabled/
sudo rm -f /etc/nginx/sites-enabled/default

# 6. Admin operator accounts: unique role, API token and Basic password
sudo install -m 0600 -o root -g root \
  deploy/production/admin-operators.example /etc/ajs/admin-operators
sudo -e /etc/ajs/admin-operators          # replace every placeholder secret
sudo sh deploy/production/render-admin-operators.sh
# The renderer detects the nginx worker group; AJS_HTPASSWD_GROUP overrides it.
# It writes root:<nginx group> 0640 htpasswd, root:root 0600 bearer map and
# root:ajs 0640 token hashes, and repairs /etc/ajs to root:ajs 0750 so the
# daemon can traverse to the hashes. AJS_DAEMON_GROUP overrides the `ajs`
# directory group. No plaintext token is printed.

# 7. Validate and start strict per-operator RBAC
sudo nginx -t && sudo systemctl reload nginx
sudo systemctl restart ajs-accountd
# Confirm all three roles with the real admin edge and GET /v1/whoami.
# Existing installations: follow docs/RBAC.md before enabling strict mode.
```

### Firewall

`firewall/ajs-production.nft` is a complete `table inet ajs_production` with a
default-drop input chain. It is a template: if the host already has a firewall,
merge the rules instead of replacing it. Always keep an SSH session open and
test first:

```bash
sudo nft -c -f deploy/production/firewall/ajs-production.nft   # syntax/dry run
sudo nft -f  deploy/production/firewall/ajs-production.nft
sudo nft list table inet ajs_production
```

The AJS LAN rule is byte-for-byte the rule owned by
`deploy/nftables/ajs-panel.nft`:

```text
ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept
```

`check.sh` fails if the two drift. The production table additionally opens TCP
13000-13010 for the public gate edge (`streams-enabled/ajs-gate.conf`). Those
are the nginx listeners only: the daemon's SOCKS5 gate on
`127.0.0.1:13000-13010` and the HTTP CONNECT backend on
`127.0.0.1:14000-14010` stay loopback-only and are not opened by either table.
A node's gate port above the static map (for example acc-07's `:13012`) has no
public edge and stays loopback-only too.

## Routing model

```text
browser ── HTTPS ──> nginx :443 (panel.example.com)
                     ├── /portal*        -> /var/lib/ajs/production/portal (SPA)
                     ├── /assets/*       -> static bundle
                     ├── /docs, /docs/*  -> docs UI + generated Markdown mirrors
                     ├── /llms.txt       -> generated docs index (text/plain)
                     ├── /v1/portal/*    -> 127.0.0.1:2261 (cookie + CSRF)
                     └── /v1/payments/{epay/notify,stripe} -> 127.0.0.1:2261

customer SDK/CLI ── HTTPS ──> nginx :443 (api.example.com)
                     ├── 41 allowlisted paths -> 127.0.0.1:2261 (scoped key, no edge credential)
                     ├── /healthz       -> static "ok" (edge liveness)
                     └── everything else -> 404 (admin namespace, portal, payments)

proxy client ── HTTPS CONNECT ──> nginx stream :13000-13010 (api.example.com)
                     └── 127.0.0.1:14000-14010 (gate HTTP CONNECT, scoped key)
                     the SOCKS5 gate on 127.0.0.1:13000-13010 is never public

operator ── SSH tunnel/VPN ──> nginx 127.0.0.1:8443 (admin.example.com)
                     ├── /               -> /var/lib/ajs/production/admin (SPA)
                     └── /v1/*           -> 127.0.0.1:2261 + per-operator bearer
                     └── /v1/events/stream -> SSE, buffering off
```

### Public API origin: deny-by-default allowlist

`api.example.com` is the control-plane edge for scoped keys. It is a *second*
line, not the boundary: the daemon's `routeScopes` table stays the authority,
so the edge can only ever be narrower than the daemon.

- **41 paths, 54 operations.** The vhost lists every path that carries an
  `x-required-scope` in `docs/openapi.json`, one location per path, with the
  operation's methods enforced by `limit_except` (a method outside the set is
  `403` at the edge and never reaches the daemon; `GET` implies `HEAD`).
  `{id}` shapes are anchored regexes (`^/v1/leases/([^/]+)$`), literal paths are
  exact `=` blocks, and `/v1/schedules/preview` has its own exact block so the
  `{id}` shape cannot widen it to `PATCH`/`DELETE`.
- **Deny by default.** `location / { return 404; }` is terminal. The 92
  admin-only operations, `/v1/portal/*`, the payment callbacks,
  `/v1/openapi.json` and `/v1/whoami` are not routed here.
  `/v1/accounts/export`, `/v1/accounts/import` and `/v1/accounts/bulk-delete`
  are additionally denied with an explicit `return 404`.
- **No edge credential.** There is no `auth_basic`, no injected
  `Authorization` header and no `X-AJS-Operator`: the customer's own
  `Authorization: Bearer ajs_sk_...` is the only credential, and it reaches the
  daemon untouched. Writes still need `X-AJS-CSRF: 1`; server-to-server clients
  send no `Origin`.
- **Static liveness.** `/healthz` is a static `200 "ok\n"`. The daemon's
  `/healthz` reports global account counts and is never proxied on this origin;
  `/readyz` stays on the portal origin.
- **Long operations and SSE.** Normal API locations inherit
  `proxy_read_timeout 150s`, which outlasts the 120s `timeoutSeconds` window of
  `POST /v1/accounts/{id}/actions/select-node`; the SSE location
  (`/v1/events/stream`) sets `proxy_buffering off` and a 1h read timeout.
- **Separate access log.** `ajs_api` records method, path, status, request
  time, upstream time and upstream status. No header is ever logged.

**Drift is fail-closed in both directions.** A new scoped route that is missing
from the vhost answers `404` at the edge (visible, not a hole); a route removed
from the daemon but still in the vhost answers `403 scope_required`. `check.sh`
asserts the mapping in both directions against `docs/openapi.json`, so either
kind of drift fails the gate instead of silently widening the surface.

Never "simplify" the allowlist to `location /v1/`: that single edit publishes
all 92 admin-only operations, and only the daemon's `403` would remain between
a scanner and `/v1/backup`, `/v1/users` and `/v1/lab/*`.

#### Exposure matrix

| Surface | Public | Loopback/VPN | Credential |
|---|---|---|---|
| `panel.example.com` `/portal*`, `/docs*`, `/llms.txt` | yes | — | portal cookie (HttpOnly) |
| `panel.example.com` `/v1/portal/*`, payment callbacks | yes | — | portal cookie / provider signature |
| `api.example.com` 41 allowlisted paths | yes | — | scoped key `ajs_sk_…` + `X-AJS-CSRF` on writes |
| `api.example.com` everything else | yes (404) | — | none issued |
| `admin.example.com` SPA + `/v1/*` | **no** | `127.0.0.1:8443` or VPN | HTTP Basic + mapped operator bearer |
| daemon `127.0.0.1:2261` | **no** | loopback only | operator bearer / portal cookie / scoped key |
| `api.example.com` `:13000-13010` (TLS HTTP CONNECT) | yes | — | scoped key with `proxy:connect` (user id + key) |
| gate SOCKS5 `127.0.0.1:13000-13010` | **no** | loopback only | scoped key with `proxy:connect` |
| gate HTTP CONNECT backend `127.0.0.1:14000-14010` | **no** | loopback only | the gate itself |

The gate data plane is reachable from the internet only as HTTP CONNECT over
TLS on 13000-13010. The SOCKS5 listener on the same numbers stays loopback
(three independent checks in `daemon/internal/gate`), so `curl --socks5
127.0.0.1:13003` works only on the host or through an SSH forward; a customer's
client must speak CONNECT-over-TLS. A lease port above the static map
(`13011+`) has no public edge, and the admin origin is never published.

#### Precondition: strict RBAC, no insecure mode

The API vhost may only be enabled while the daemon runs with
`AJS_RBAC_STRICT=1` and `AJS_ALLOW_INSECURE` unset. With `AJS_ALLOW_INSECURE=1`
(and non-strict RBAC) the auth middleware lets every non-tenant `/v1/` request
through, so the allowlist would publish an unauthenticated API — every allowed
path would be anonymous, and the allowlist itself would not help. Assert it
before enabling the vhost, not after:

```bash
sudo grep -E '^(AJS_RBAC_STRICT|AJS_ALLOW_INSECURE)=' /etc/ajs/accountd.env
# AJS_RBAC_STRICT=1 and no AJS_ALLOW_INSECURE line
curl -sS -o /dev/null -w '%{http_code}\n' https://api.example.com/v1/leases
# 401 without a bearer; 404 for /v1/users with a valid key
```

#### Rollout and rollback

```bash
# 1. stage (never install before the certificate carries the SAN)
sudo cp deploy/production/nginx/sites-available/ajs-api.conf /etc/nginx/sites-available/
sudo nginx -t                                    # ajs-api.conf is not yet enabled
# 2. certificate already covers api.example.com (see TLS certificates)
# 3. DNS: A/AAAA for api.example.com -> this host
# 4. enable: reload, never restart
sudo ln -s /etc/nginx/sites-available/ajs-api.conf /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
# 5. canary one key
curl -sS -H "Authorization: Bearer $AJS_SK" https://api.example.com/v1/leases
```

Rollback is one file and one reload; no daemon restart, no firewall change, no
data change, no certificate revocation:

```bash
sudo rm /etc/nginx/sites-enabled/ajs-api.conf
sudo nginx -t && sudo systemctl reload nginx
```

Blast radius is the API edge alone: the portal, admin and default-deny vhosts
and the gate keep their connections. Leave the DNS record in place; it then
answers `421` from the default-deny listener until the vhost returns.

### Public gate data plane: TLS-terminated HTTP CONNECT

`streams-available/ajs-gate.conf` publishes the gate's **HTTP CONNECT**
listener. The daemon keeps every listener on loopback — the SOCKS5 gate on
`127.0.0.1:<leasePort>` and the opt-in HTTP CONNECT gate on
`127.0.0.1:<leasePort + 1000>` — and nginx is the only process with a public
socket. The stream module relays opaque bytes, so it cannot answer, rewrite or
authenticate CONNECT: `Proxy-Authorization` reaches the gate untouched and
never appears in an nginx log.

| Lease port | Public TLS (nginx) | Gate HTTP CONNECT (loopback) | SOCKS5 gate (loopback, never public) |
|---|---|---|---|
| 13000 | `api.example.com:13000` | `127.0.0.1:14000` | `127.0.0.1:13000` |
| 13001 | `api.example.com:13001` | `127.0.0.1:14001` | `127.0.0.1:13001` |
| 13002 | `api.example.com:13002` | `127.0.0.1:14002` | `127.0.0.1:13002` |
| 13003 | `api.example.com:13003` | `127.0.0.1:14003` | `127.0.0.1:13003` |
| 13004 | `api.example.com:13004` | `127.0.0.1:14004` | `127.0.0.1:13004` |
| 13005 | `api.example.com:13005` | `127.0.0.1:14005` | `127.0.0.1:13005` |
| 13006 | `api.example.com:13006` | `127.0.0.1:14006` | `127.0.0.1:13006` |
| 13007 | `api.example.com:13007` | `127.0.0.1:14007` | `127.0.0.1:13007` |
| 13008 | `api.example.com:13008` | `127.0.0.1:14008` | `127.0.0.1:13008` |
| 13009 | `api.example.com:13009` | `127.0.0.1:14009` | `127.0.0.1:13009` |
| 13010 | `api.example.com:13010` | `127.0.0.1:14010` | `127.0.0.1:13010` |

**Client.** `GET /v1/leases/{id}` returns `connection.port` — the lease port,
`gatePort`. Dial that port over HTTPS with the same two credentials the SOCKS5
gate takes:

```bash
curl -x "https://u-01:$AJS_SK@api.example.com:13000" https://example.com/
# equivalent, credentials separate from the proxy URL
curl --proxy "https://api.example.com:13000" --proxy-user "u-01:$AJS_SK" https://example.com/
```

`<user>` is the panel user id and `<ajs_sk_...>` is a scoped key carrying
`proxy:connect`; the key must be active, belong to that user, allow the gate's
account and have exactly one active lease on it. That is the same admission
pipeline as the SOCKS5 gate — key check, user enabled/allowlist/quota, account
quota, one flow-slot pool — and the same wire contract: `200 Connection
Established`, `407` with `Proxy-Authenticate: Basic realm="apc"` for a
missing or wrong credential, `403` for an allowlist/identity refusal, `429`
with `Retry-After` for quota, `503` while draining. The gate re-checks the
lease at CONNECT, so revoking the key or ending the lease cuts the next
connection; an admitted tunnel finishes with the identity it authenticated.

**TLS.** All 11 servers reuse `/etc/ajs/tls/api.example.com/fullchain.pem` and
`privkey.pem` — the panel certificate with the api SAN, so a renewal reloads
both edges — pin `TLSv1.2 TLSv1.3`, turn session tickets off and allow 10
concurrent connections per source address (`limit_conn ajs_gate_conn`).
`proxy_timeout 1h` is an idle timeout, not a one-hour cap on a tunnel. The
stream module has no `limit_req`, no `429` body and no `Retry-After`, so the
gate's own admission stays the authority. Every `listen` carries `ssl` and no
backend points at a SOCKS5 port: there is no plaintext gate port.

**Not exposed.** The SOCKS5 gate (`127.0.0.1:13000-13010`), the HTTP CONNECT
backend (`127.0.0.1:14000-14010`), the admin origin (`127.0.0.1:8443`) and the
daemon API (`127.0.0.1:2261`). Only the HTTP CONNECT listener is published,
and only through TLS. A lease port above the static map (node 12 and later,
`13011+`) has no public edge until a server block is added.

**Precondition.** `AJS_GATE_HTTP=1` and `AJS_GATE_HTTP_OFFSET=1000` in
`/etc/ajs/accountd.env`, then one daemon restart to create the listener.
Without it the public ports complete the TLS handshake and close; the gate
never sees a request. The SOCKS5 gate is unaffected either way. The listen
address is an owner input (see **Public address** above): never the wildcard
and never `127.0.0.1`, because the daemon owns those numbers on loopback.

**Rollout and rollback.** Rollout is one file and one reload; rollback is
removing the file and reloading. No daemon restart, no certificate change, no
data change, no DNS change.

```bash
# 1. the daemon has the backend listener
sudo grep -E '^AJS_GATE_HTTP' /etc/ajs/accountd.env   # AJS_GATE_HTTP=1
ss -ltn 'sport = :14000'                              # one listener per live node

# 2. enable the edge (the public address must already be replaced)
sudo cp deploy/production/nginx/streams-available/ajs-gate.conf /etc/nginx/streams-enabled/
sudo nginx -t && sudo systemctl reload nginx

# 3. canary one lease port
curl -x "https://u-01:$AJS_SK@api.example.com:13000" https://example.com/

# rollback
sudo rm /etc/nginx/streams-enabled/ajs-gate.conf
sudo nginx -t && sudo systemctl reload nginx
```

A reload keeps established tunnels on the old workers until they go idle or
the client closes. The firewall rule may stay after a rollback — the ports
then refuse connections because nothing listens — or be removed with the edge.

### Customer origin: token-free and portal-only

`build-bundles.sh` builds the dashboard with:

```bash
VITE_USE_MOCK=false
VITE_API_BASE=          # same origin
VITE_PANEL_TOKEN=       # deliberately empty
```

The build requires `dashboard/package-lock.json` and a `node_modules` tree
installed with `npm ci`. Every successful build emits `dist/ajs-build.json`; the
script refuses to copy a bundle unless the manifest says `mode: live`,
`mock: false`, `panelToken: false` and an empty `apiBase`. A plain
`npm run build` fails closed and writes no `dist/`. `npm run build:mock` is the
explicit demo recipe and is never packageable. The live build is
`VITE_USE_MOCK=false npm run build`.

A Vite `VITE_*` value is compiled into public JavaScript. Shipping
`VITE_PANEL_TOKEN` would hand the admin bearer to every customer who opens
devtools, so the customer bundle never has one. It authenticates with the
daemon's `__Host-ajs_portal` HttpOnly cookie and CSRF token instead.

The customer origin serves **only** `/portal`, `/portal/*`, `/assets/*`, the
public docs (`/docs`, `/docs/*` and `/llms.txt`) and the portal API/callback
paths. `location / { return 404; }` is terminal: the admin SPA routes
(`/accounts`, `/nodes`, `/settings`, …) and the admin API (`/v1/accounts`,
`/v1/nodes`, …) are not routed on this origin. The same static
bundle is copied to both roots because the admin route code is lazy-loaded and
dead on the portal origin; the deployment edge, not a source fork, is what
makes the customer surface portal-only.

The docs UI is the same bundle's `/docs` route. Its generated Markdown mirrors
(`/docs/*.md`) and `/llms.txt` ship next to `index.html`; nginx serves them as
files only (no proxy), typed `text/markdown` by the kit MIME map
(`nginx/ajs-mime.types`, included by `nginx.conf`) rather than by a second
regex location, and every docs response carries `Cache-Control: no-cache` plus
the security-header snippet.

### Admin origin: per-operator Basic auth + mapped bearer + daemon RBAC

The admin bundle is built without a token. nginx authenticates each person
with HTTP Basic at server level (SPA, `/v1/*` and SSE included), then the map
in `nginx.conf` selects that person's independent API bearer. The daemon
looks up its SHA-256 hash and enforces the associated `viewer`, `operator` or
`admin` role on the matched route. See [`docs/RBAC.md`](../../docs/RBAC.md)
for the complete matrix.

```nginx
proxy_set_header Authorization $ajs_operator_authorization;
proxy_set_header X-AJS-Operator $remote_user;
```

The map is root-owned `0600`, loaded by the root nginx master and never served
or bundled. The daemon reads only the root:ajs `0640` hash file. In strict
mode the forwarded username is ignored for identity and authorization: both
come from the authenticated bearer. The legacy shared bearer and anonymous
insecure admin mode are refused. `X-AJS-CSRF: 1` and the exact
`AJS_ADMIN_ORIGINS` check still apply to every unsafe request.

Access policy is three independent layers:

1. **Listen address** — `127.0.0.1:8443` by default. Reach it through an SSH
   tunnel (`ssh -L 8443:127.0.0.1:8443 user@host`) or uncomment the
   `listen 10.8.0.1:443 ssl http2;` line to bind a VPN-only interface.
2. **nginx `allow`/`deny`** — loopback plus the VPN client CIDR
   (`10.8.0.0/24` in the template), ending in `deny all;`. Requests from any
   other peer get 403 even if the listener is accidentally exposed.
3. **Per-operator Basic auth** — every request needs a named operator entry
   from `/etc/nginx/ajs-admin.htpasswd`. There is no anonymous admin path.

The daemon adds the CSRF/Origin gate on every unsafe admin request (`POST`,
`PUT`, `PATCH`, `DELETE`): it requires the non-simple `X-AJS-CSRF: 1` header
and, when an `Origin` header is present, an exact match against
`AJS_ADMIN_ORIGINS` (`https://admin.example.com:8443` in the template). A
cross-origin HTML form cannot set the header; a cross-origin `fetch` that
tries must pass a CORS preflight, and the admin origin is not in
`AJS_CORS_ORIGINS`. CLI and SDK clients omit `Origin` but must send the
header. Do not strip `Origin` at the proxy; the shipped nginx config passes it
through.

The daemon enforces RBAC after authentication; hiding controls in a browser is
not part of the security boundary. Keep the admin origin on loopback/VPN and
use separate usernames so audit names and token identities agree.

#### Admin operator accounts

The operator inventory is a root-owned `0600` list of
`username:viewer|operator|admin:token:password`, never committed:

```bash
# One line per person; token and password are independent random secrets.
sudo install -m 0600 -o root -g root \
  deploy/production/admin-operators.example /etc/ajs/admin-operators
sudo -e /etc/ajs/admin-operators
sudo sh deploy/production/render-admin-operators.sh
sudo nginx -t && sudo systemctl reload nginx
sudo systemctl restart ajs-accountd
```

The combined renderer writes the Basic htpasswd, nginx bearer map and daemon
hash file without printing secrets. The htpasswd is root:nginx-group `0640`;
the bearer map is root:root `0600`; the hash file is root:ajs `0640`. The
containing `/etc/ajs` directory is root:ajs `0750` (group overridable with
`AJS_DAEMON_GROUP`) because the daemon runs as `User=ajs Group=ajs` and the
kernel requires search permission on every path component: a root:root `0750`
directory blocks the daemon from opening its own `0640` hash file even though
the file bits are correct. `/etc/ajs/tls` stays root:root `0750`; only the
root nginx master reads the certificates. Both output directories are
canonicalized before anything is written: a symlinked, `.`/`..` or
duplicate-slash path is refused, the hash and map directories must be the same
directory or disjoint, and only the canonical hash directory's ownership and
mode are repaired.

`render-admin-htpasswd.sh` refuses placeholder or short passwords, duplicate
usernames and characters outside `A-Z a-z 0-9 . _ - @ +`, writes the htpasswd
atomically with mode `0640` and ownership `root:<nginx worker group>`, and
prints only the destination and operator count. The rendered file contains
hashes, not plaintext. It refuses to leave the file world-readable.

**Why group-readable, not root-only 0600.** `auth_basic_user_file` is opened
by the nginx **worker** process on the first authenticated request, not by the
root master at startup. The stock kit sets `user www-data;`; a root:root 0600
htpasswd is therefore unreadable by the worker. `nginx -t` still passes, the
browser still gets a Basic prompt, but every valid username/password returns
HTTP 500 and the worker log records:

```text
open() "/etc/nginx/ajs-admin.htpasswd" failed (13: Permission denied)
```

The renderer fixes this by writing `root:www-data` mode `0640` (or the group
from the active `user` directive: `user nginx nginx;` on RHEL, a two-field
`user www-data ops;`, etc.). Detection reads `AJS_NGINX_CONF` when set, then
`/etc/nginx/nginx.conf`, then `nginx -V`; if the worker group is not resolvable
or the destination path is not traversable by that group, it fails instead of
writing a file the worker cannot read. Override the detected group explicitly
when the nginx config is not on the host yet:

```bash
sudo AJS_HTPASSWD_GROUP=www-data sh deploy/production/render-admin-htpasswd.sh
```

Do not “fix” a 500 by chmod 0644 or 0666: that exposes the apr1 hashes to
every local user. Group-read with the worker group is the intended boundary.

**Rotation and revocation.** Run `render-admin-operators.sh`, reload nginx
and restart the daemon. Removing a row revokes it only after both actions;
password rotation alone does not revoke an API token. Two-generation token
rotation and the legacy shared-bearer migration are specified in
[`docs/RBAC.md`](../../docs/RBAC.md).

**127.0.0.1:8443 fallback warning.** The documented fallback
`https://127.0.0.1:8443` still requires the Basic prompt and still goes through
the same auth/allow/deny checks. The browser will warn about the certificate
name mismatch because the certificate is issued for `admin.example.com`; that
warning is expected. Do not disable TLS verification, add an `allow` rule or
remove `auth_basic` to make the fallback quieter. For routine work, map
`admin.example.com` to `127.0.0.1` in `/etc/hosts` and use the real name.

### `X-Forwarded-For` and the trusted-proxy note

`nginx/snippets/ajs-proxy-common.conf` sets:

```nginx
proxy_set_header X-Real-IP         $remote_addr;
proxy_set_header X-Forwarded-For   $remote_addr;   # overwrite, never append
proxy_set_header X-Forwarded-Proto $scheme;
```

The daemon reads the **first** `X-Forwarded-For` entry as the client IP, and
only when the direct peer is in `AJS_TRUSTED_PROXIES`. This kit sets that to
`127.0.0.1,::1`, because nginx connects over loopback. `$proxy_add_x_forwarded_for`
is deliberately not used: appending would preserve a client-supplied leftmost
entry, letting a customer choose their own rate-limit bucket.

If a load balancer or CDN is later placed in front of nginx, enable
`ngx_http_realip_module` on this host with the balancer's exact CIDRs
(`set_real_ip_from …; real_ip_header X-Forwarded-For; real_ip_recursive on;`)
so `$remote_addr` is the real client, and keep the overwrite. Never widen
`AJS_TRUSTED_PROXIES` to a public range.

### Rate limits

Two layers:

- **nginx** (`nginx/nginx.conf`): `ajs_portal_auth` 12 r/min (burst 5 login,
  burst 3 signup), `ajs_portal_api` 120 r/min (burst 60), `ajs_admin_api`
  60 r/min (burst 30), plus a 20-connection cap per IP on the portal API.
  The public API edge uses `ajs_public_api` 120 r/min (burst 30), a
  20-connection cap per IP (`ajs_public_conn`) and a 400-connection cap per
  server (`ajs_public_total`), which bounds the single Go daemon across all
  sources. Rejections are `429` (`limit_req_status` / `limit_conn_status`).
  No zone is keyed on the Authorization header: that would copy customer
  secrets into shared memory with unbounded cardinality.
- **daemon**: per-IP portal buckets (120/min global, 5/min signup, 10/min
  login), per-email buckets, and a per-key token bucket (default 60/min,
  `rateLimitPerMin` per key). All return JSON with `Retry-After`.

The nginx zone is intentionally slightly looser so the daemon's JSON error is
the authority for portal clients. The admin zone protects the single bearer
from brute-force/accidental loops.

### Content-Security-Policy

`nginx/snippets/ajs-security-headers.conf` carries exactly one CSP header, and
it is the only CSP header in the kit. Two CSP headers intersect, so a stale
duplicate would silently break the bundle; never add a second one.

```text
default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'
```

It is one line in the snippet and is emitted with `always`, so it is also
present on nginx-generated `401`/`404`/`502` responses.

- `script-src 'self'` — `dashboard/index.html` loads one external module and
  has no inline script, so inline script and `eval` stay forbidden. Never add
  `'unsafe-inline'`, `'unsafe-eval'` or a CDN origin to `script-src`.
- `style-src 'self' 'unsafe-inline'` is the one deliberate exception. The
  existing React components pass `style={{ ... }}` props
  (`dashboard/src/components/Sidebar.tsx` and the other pages), which render as
  `style` attributes and are refused by a strict `style-src`. It is a
  style-only allowance; it does not permit inline script, and the browser gate
  below fails if Chrome refuses either an inline style or an external module.
- `img-src 'self' data:` keeps images same-origin while allowing the data-URI
  marks the bundle uses; `connect-src 'self'` keeps every XHR and the SSE
  stream same-origin.
- `object-src 'none'`, `base-uri 'none'`, `frame-ancestors 'none'` and
  `form-action 'self'` close plugin, `<base>`, framing and cross-origin form
  targets; `default-src 'self'` is the fallback for everything not listed.
- No directive names a remote origin and there is no wildcard, so the policy
  cannot be widened by a stray hostname.

Coverage is structural: the snippet is included at server level in all four
vhosts and in every location that declares its own `add_header`, because nginx
drops the inherited header set as soon as a location adds one. The portal's
HTTP redirect server includes it too (HSTS is ignored over plain HTTP and ACME
clients ignore headers, so it is inert there, but no response path can then
lose the policy), and so does the API's, so a scoped-key client cannot be the
one caller answered without HSTS. `ajs-default.conf` serves no content — it
answers only `444` (connection closed before any response) and `421` — and
carries the same include so its `421` is not the one response without a policy.
`check.sh` enforces all of that, then asserts the exact policy on the staged
responses:
portal HTML, portal assets, the portal deep link and 404, admin HTML, the admin
`401`, an admin API error (`502` from an upstream pinned to a closed port), and
the default-deny `421`; it also proves an unknown Host is closed on the
fixture's HTTP port.

The browser gate is the last step. `check.sh` starts the shipped vhosts on
unprivileged loopback ports with a disposable document root and runs
`csp-smoke.sh`, which drives headless Chromium
(`--dump-dom --virtual-time-budget=5000`) at
`https://panel.example.com:<port>/portal/login`. The fixture's login form is
rendered by an external module, so a blocked script is a missing marker, and
any `Refused to …`/`violates the following Content Security Policy` console
line fails the gate. `csp-smoke.sh` never starts or reloads nginx, adds no npm
dependency, requires `--resolve` to a `127.x` loopback address with the URL
naming that same fixture host, and pins its certificate exception to the
fixture certificate's SPKI (`--cert`, forwarded as
`--ignore-certificate-errors-spki-list`), so a real origin can never be
trusted; the unpinned loopback fallback exists only for a caller that omits
`--cert` and can be disabled with `AJS_CSP_STRICT_TLS=1`. It exits `0`
(rendered), `1` (broken page, CSP refusal, or a browser that could not reach
the fixture) or `3` (no usable browser: absent or unable to start). Without a
browser, dev mode reports an explicit `SKIP` and never claims a boot;
production mode turns that skip into a required `FAIL`.
Set `AJS_CHROMIUM_BIN` when chromium/chrome is not on `PATH` or in the usual
absolute locations.

## Operations

```bash
# status and logs
systemctl status ajs-accountd
journalctl -u ajs-accountd -f
tail -f /var/log/nginx/access.log /var/log/nginx/error.log

# reload after a certificate renewal or nginx config change
sudo nginx -t && sudo systemctl reload nginx

# restart the daemon after changing /etc/ajs/accountd.env
sudo systemctl restart ajs-accountd

# health: liveness vs readiness
curl -fsS http://127.0.0.1:2261/healthz      # daemon process liveness only
curl -fsS http://127.0.0.1:2261/readyz       # 503 until state-write and required integrations are ready
curl -fsS https://panel.example.com/healthz  # edge liveness: static nginx "ok"
curl -fsS https://panel.example.com/readyz   # daemon readiness through the edge

# admin through an SSH tunnel
ssh -N -L 8443:127.0.0.1:8443 user@host
# browse https://admin.example.com:8443 (map admin.example.com to 127.0.0.1
# in /etc/hosts, or use https://127.0.0.1:8443 and accept the certificate-name
# warning). The browser prompts for the operator username/password from
# /etc/nginx/ajs-admin.htpasswd; the bearer is injected by nginx and never
# reaches the browser.
```

The daemon's state lives in `/var/lib/ajs`: `state.json` (rewritten every
`AJS_SNAPSHOT_INTERVAL`, default 30 s, and on shutdown), `secrets.json` and
`device-secrets.json` when `AJS_MASTER_KEY` is set. Back up the directory
while the service is stopped, together with the master key; without the key
the sealed envelopes cannot be opened.

`/healthz` is liveness only: it reports that the process can answer and is
deliberately unaffected by a broken state directory. `/readyz` is the
readiness contract used by monitoring and load balancers. It checks that the
state directory accepts the atomic snapshot write and that every configured
required integration is usable (a configured Resin panel must have completed
its first reconcile; a partially configured epay adapter must be enabled; a
tenant portal must have its origin). It answers `200` with
`{"status":"ready","checks":[...]}` or `503` with
`{"status":"not_ready","reasons":[...]}`. It never performs a vendor network
call: the Resin row reads the in-memory topology of the last successful
reconcile, and custom `ReadyChecks` are bounded by a 750 ms deadline. Do not
point a liveness probe at `/readyz`: a restart will not fix an unwritable
state directory.

## Verification

`check.sh` performs:

1. required-file presence and shell syntax (`bash -n` / `sh -n`);
2. YAML parse of `inventory.example.yml` and required top-level keys
   (Python+PyYAML, Ruby or yq);
3. a secret scan over every kit file: PEM private keys/certificates, token
   assignments, literal `Authorization: Bearer` values, and bare 64-hex
   strings; the admin-token template must contain only `${AJS_ADMIN_TOKEN}`;
4. a real `nginx -t` on a staged copy of all four vhosts plus the gate stream
   map, with generated self-signed certificates, a dummy 64-character token
   and a throwaway operator htpasswd. The staged config
   rewrites the PID file and every `*_temp_path` into the throwaway tree, so
   the nginx stage passes as an unprivileged user on stock Debian/Ubuntu nginx
   without writing `/run/nginx.pid` or `/var/lib/nginx/*`. Because nginx -t
   opens the listening sockets even in test mode, an unprivileged run retries
   in an isolated user+network namespace (`unshare -Urn`) where it can bind
   :80/:443 and 13000-13010. A dynamic stream module is staged through
   `/etc/nginx/modules-enabled` with an absolute `load_module` path; a host
   without the module is a production `FAIL` (install `libnginx-mod-stream`)
   and a dev `SKIP` that still parses the HTTP vhosts. Production mode
   requires host nginx; Docker `nginx:1.29-alpine` staging is dev-only
   (`AJS_CHECK_NGINX_DOCKER=1`);
5. a runtime nginx probe on the staged `:80`/`:443` listeners: an unknown
   HTTP `Host` must be closed (`444`), an unknown HTTPS `Host` and an
   IP-literal `Host` must answer `421`, the portal HTTP redirect must use the
   fixed `https://panel.example.com` origin, and the canonical portal `Host`
   must still reach `/healthz` with `200`. The same probe drives the public
   API edge: `GET`/`POST /v1/leases` must be proxied (`502` with the
   namespace's dead upstream, or a daemon status when one is listening on
   `:2261`), `/v1/users`, `POST /v1/backup`, `POST /v1/accounts/export`,
   `/v1/portal/session` and `/v1/whoami` must be `404`, `PATCH /v1/leases`
   must be `403` from `limit_except`, `/healthz` must be a static `200 "ok"`,
   an unknown Host carrying an API path must still be `421`, and the API HTTP
   listener must `301` to the fixed `https://api.example.com`. It also
   completes a TLS handshake against the staged gate listener on
   `127.0.0.1:13003` — whose backend is dead — and requires the served
   certificate to name `api.example.com`, proving TLS termination rather than
   a plaintext SOCKS5 forward. The probe
   prefers an isolated network namespace (`unshare -n` as root, `unshare -Urn`
   otherwise) so a running host nginx does not conflict with the staged
   listeners;
6. a runtime per-operator auth probe: an unauthenticated request to a Basic
   vhost answers `401` and leaves the upstream log empty; two different
   operator credentials reach the upstream with distinct `X-AJS-Operator`
   values. A separate runtime proof renders a disposable htpasswd through the
   real `render-admin-htpasswd.sh`, verifies `root:<nginx worker group>` mode
   `0640`, and reads at least one byte as the actual nginx worker identity
   from `nginx.conf`; a root-only `0600` file fails that proof, which is the
   AJS-408 condition-1 regression;
7. a real daemon `/readyz` proof: start the packaged binary
   (`AJS_ACCOUNTD_BIN`, `PATH`, `daemon/ajs-accountd`, or a `go build`) with a
   writable state directory and require `200` `ready`; then repeat with a
   `0555` state directory and require `503` `not_ready` naming the `state`
   check while `/healthz` stays `200`;
8. static nginx assertions: portal terminal 404, no generic `/v1/` proxy on
   the portal origin, the public docs edge (`= /docs`, `/docs/` and
   `= /llms.txt` are static, no-cache and CSP-covered, and precede the
   terminal 404), `.md` typed `text/markdown` by the included kit MIME map,
   admin token include, allow/deny, default-deny `444`/`421`,
   canonical redirect, `/readyz` proxy, HSTS, rate-limit zones,
   `X-Forwarded-For $remote_addr`, and the CSP set: exactly one CSP header
   directive anywhere in the kit (case-insensitive, including two directives on
   one line), the exact acceptance policy on the snippet, `script-src 'self'`
   with no `unsafe-*`, the documented `style-src` exception, no remote or
   wildcard origin, no other snippet adding headers, the policy reaching every
   server and location of all four vhosts (a duplicate include is a failure),
   and the named portal/admin/API response paths; the default-deny vhost must
   stay content-free (no root/alias/index/try_files/proxy_* and only
   `444`/`421`);
9. the CSP runtime fixture: the shipped vhosts staged on `127.0.0.1` ephemeral
   ports with a disposable document root and an upstream pinned to a closed
   port, asserting exactly one CSP header with the exact policy on portal HTML,
   portal assets, the portal SPA deep link, the portal `404`, the docs route
   (`/docs`), the docs Markdown (`/docs/overview.md`, also `text/markdown`),
   `/llms.txt` (also `text/plain`), admin HTML, the
   admin `401`, the admin `502` and the default-deny `421`, plus a closed
   connection for an unknown Host on the fixture's HTTP port; then a
   headless-browser boot of `https://panel.example.com:<port>/portal/login`
   through `csp-smoke.sh`, which requires a loopback `--resolve` mapping, the
   rendered login form (an external module has to execute) and fails on any CSP
   `Refused to …` console line or unreachable fixture. A missing browser is a
   dev `SKIP` and a production `FAIL`;
10. token-free bundle assertions on `build-bundles.sh`, the committed
    `dashboard/package-lock.json`, and the `ajs-build.json` live/mock manifest;
11. `systemd-analyze verify` plus the hardening directives;
12. the production firewall rule exactly matching `deploy/nftables/ajs-panel.nft`
    and `nft -c` (direct, `unshare -Urn`, or `sudo -n` fallback);
13. README input coverage (`AJS_TENANT_PORTAL`, `AJS_TENANT_ORIGIN`, `AJS_ADMIN_ORIGINS`,
    `AJS_TRUSTED_PROXIES`, `AJS_ADMIN_TOKEN`, `AJS_MASTER_KEY`, `X-AJS-Operator`,
    `render-admin-htpasswd`, `admin-operators`, DNS, TLS);
14. the AJS-412 production renderer acceptance: a valid inventory produces
    separate Basic/bearer/hash artifacts with the documented ownership and
    modes (`root:<nginx group>` `0640` htpasswd, `root:root` `0600` bearer
    map, `root:<daemon group>` `0640` hash file), the unit's own daemon
    identity (`User=`/`Group=`, `ajs:ajs`) is proven to read the hash file
    rather than only asserted by mode bits, a two-generation rotation leaves
    one current nginx token, and no plaintext token or password reaches the
    daemon hash file or renderer log. The renderer creates root-owned files,
    so this proof is why production mode must run via `sudo -E`. If the
    daemon group or user does not exist, or this host cannot switch identity
    to it, the proof is a required `FAIL`, never an omitted check.
15. the public API allowlist (`ajs-api.conf`): the `:80` redirect is canonical,
    the `:443` block includes the TLS and security-header snippets, the vhost
    carries no `auth_basic`, no `proxy_set_header Authorization`, no
    `X-AJS-Operator` and no admin/portal/payment path, it ends in the terminal
    `location / { return 404; }`, `/healthz` is static, the SSE location turns
    buffering off with a 1h read timeout, `proxy_read_timeout 150s` outlasts the
    select-node window, the three admin-only account subpaths have explicit
    `404`s, no generic `/v1/` location exists, and the three `ajs_public_*`
    zones plus the `ajs_api` log format are wired. The drift assertion parses
    the vhost's `location`/`limit_except` blocks with python3 and requires a
    bidirectional match with `docs/openapi.json`: every one of the 54
    `x-required-scope` operations (41 paths) has a location allowing exactly
    its methods, every proxying location maps back to a scoped operation, and
    every named `404` is an admin-only path. It prints the observed counts
    (currently `COUNT paths=41 operations=54 proxying_locations=41`).
16. the public gate data plane (`streams-available/ajs-gate.conf`): the
    top-level `stream` block, the `ajs_gate_conn` zone inside it and the
    `streams-enabled` include; 11 servers whose `listen` ports are exactly
    13000-13010 with `ssl` on every one, whose backends are exactly
    `127.0.0.1:14000-14010` and never a SOCKS5 port, and whose 11 server
    blocks all carry the api certificate pair, TLS 1.2/1.3, tickets off, a 5s
    connect timeout, a 1h proxy timeout, `limit_conn ajs_gate_conn 10` and
    `access_log off`; one explicit public listen address (never the wildcard
    or loopback, which would collide with the daemon's own SOCKS5 listener);
    and no header rewrite or edge credential. The staged tree binds loopback
    for `nginx -t` and the runtime probe, which completes a TLS handshake on
    `127.0.0.1:13003` with a dead backend and requires the api.example.com
    certificate.

In production mode every item above is required: a missing tool is a `FAIL`
and the summary reports the required-skip count. Production mode also refuses
to start as a non-root user instead of deferring the root-only renderer proof
to a late required-skip failure. `--dev` reports absent tools and the root-only
ownership proof as `SKIP` and ends with `check: DEV-PASS`, never `check: PASS`.
A missing YAML parser is always a failure because YAML validation is a gate
requirement.

## Recorded verification (2026-10-27)

- **Public gate edge (AJS-469), WSL2 Ubuntu 24.04, nginx 1.24.0 (Ubuntu) with
  `libnginx-mod-stream`:** `bash deploy/production/check.sh --dev` →
  `check: DEV-PASS (176 checks, 1 skipped; not a production gate)`. The single
  skip is the root-only AJS-412 ownership proof. The staged `nginx -t` parsed
  the four HTTP vhosts *and* the gate stream map (`unshare -Urn nginx`); the
  dynamic stream module was staged through `modules-enabled` with an absolute
  `load_module` path. The runtime probe completed a TLS handshake on the
  staged `127.0.0.1:13003` with a dead backend and served the throwaway
  certificate for `api.example.com`, which proves TLS termination rather than
  a plaintext SOCKS5 forward. The static assertions observed 11 servers,
  `listen` ports 13000-13010 (all `ssl`), backends `127.0.0.1:14000-14010`,
  the api certificate pair, `proxy_timeout 1h`, `limit_conn ajs_gate_conn 10`
  and one explicit public listen address. The CSP fixture strips the stream
  block because it serves only the HTTP vhosts. This is a dev result; the
  production gate (`sudo -E`) is still required on the release host.
- **HMC build host, nginx 1.26.3:** `bash deploy/production/check.sh --dev` →
  `check: DEV-PASS (171 checks, 5 skipped; not a production gate)`. This nginx
  build ships without the stream module, so the staged `nginx -t` covers the
  four HTTP vhosts only and the gate TLS probe is skipped (two of the five
  skips); the static gate-map assertions still pass. The WSL2 run above
  (`176 checks, 1 skipped`) remains the only run that proved the TLS
  handshake, so HMC did not prove the stream TLS probe.
- **Public API edge (AJS-468), WSL2 Ubuntu 24.04, nginx 1.24.0 (Ubuntu):**
  `bash deploy/production/check.sh --dev` →
  `check: DEV-PASS (159 checks, 1 skipped; not a production gate)`. The single
  skip is the root-only AJS-412 ownership proof. The staged `nginx -t` parsed
  the portal, admin, api and default vhosts (`unshare -Urn nginx`), and the
  runtime probe on the staged `:80`/`:443` listeners observed the new API
  routing: `GET` and `POST /v1/leases` proxied (`502`, upstream refused inside
  the probe namespace), `/v1/users`, `POST /v1/backup`,
  `POST /v1/accounts/export`, `/v1/portal/session` and `/v1/whoami` all `404`,
  `PATCH /v1/leases` `403` from `limit_except`, `/healthz` `200` with body
  `ok`, an unknown Host carrying `/v1/leases` still `421`, and the API HTTP
  listener `301` to `https://api.example.com/v1/leases`. The drift assertion
  printed `COUNT paths=41 operations=54 proxying_locations=41`. Manual controls
  on the extracted assertion (not part of the gate) failed closed: deleting the
  `/v1/leases` location, widening its `limit_except` to `PATCH`, adding a
  proxying `/v1/users` location, turning an admin-only deny into a scoped one,
  and replacing the allowlist with `location /v1/` each produced the expected
  `BAD` line. The CSP walk now covers 75 locations in all four vhosts. This is
  a dev result; the production gate (`sudo -E`) is still required on the
  release host.
- **WSL2 Ubuntu development host, nginx 1.24.0 (Ubuntu), Google Chrome
  154.0.8037.57:** `bash deploy/production/check.sh --dev` →
  `check: DEV-PASS (121 checks, 1 skipped; not a production gate)`. The only
  skip is the root-only AJS-412 ownership proof. The CSP gate ran end to end:
  exactly one CSP header directive anywhere in the kit, the exact acceptance
  policy with `always`, `script-src 'self'`, the documented `style-src`
  exception, no remote or wildcard origin, no other snippet adding headers,
  coverage of all 25 locations and all three server blocks, the named response
  paths, and a content-free default-deny vhost. The runtime fixture staged the
  shipped vhosts on `127.0.0.1` ephemeral ports with the upstream pinned to a
  closed port, and observed exactly one exact policy on portal HTML (`200`),
  the portal asset (`200`), `/portal/login` (`200`), the portal `404`, admin
  HTML (`200`), the admin `401`, the admin `502` and the default-deny `421`,
  with the unknown-Host HTTP probe closed (`000`). Headless Chrome then
  rendered the fixture login form (`id="ajs-portal-login"`) with no CSP
  refusal.
- **Linux development host, re-run after the AJS-465 docs edge (same day):**
  `bash deploy/production/check.sh --dev` →
  `check: DEV-PASS (134 checks, 1 skipped; not a production gate)`. This run
  was the Linux shell, not the Windows host: host `nginx`, `openssl`, `curl`,
  `systemd-analyze`, `nft`, `sudo` and `/usr/bin/google-chrome` were all
  present, so nothing was skipped except the root-only AJS-412 proof. The
  nginx and Chrome versions were not re-recorded for this run. The three new
  static checks are the docs edge, the docs-before-terminal-404 ordering and
  the `text/markdown` MIME mapping; the CSP walk now covers 28 locations. The
  runtime fixture additionally observed `/docs` (`200`), `/docs/` (`200`, the
  bundle index, not a directory `403`), `/docs/overview.md` (`200`,
  `text/markdown`) and `/llms.txt` (`200`, `text/plain`), each with exactly one
  exact policy.
- **CSP helper controls (manual, not part of the gate):** an inline script
  under `script-src 'self'` exited `1` quoting Chrome's `Executing inline
  script violates the following Content Security Policy directive`; the same
  page with `'unsafe-inline'` exited `0`; a loopback URL with nothing listening
  exited `1` (`net::ERR_CONNECTION_REFUSED`), not `3`; and an executable that
  cannot start exited `3`. Duplicate-detection controls also failed closed: a
  canonical plus an uppercase CSP on one line counted three directives
  statically and two header values at runtime, and a synthetic default vhost
  with `location = /healthz { return 200 "ok"; }` was rejected by the
  content-free guard.
- **Defect found by the gate:** `location /portal/` used
  `try_files $uri /index.html`, whose last argument is an internal redirect to
  `/index.html`; the portal's terminal `location /` then answered `404`, so
  every portal deep link (`/portal/login`, `/portal/signup`, ...) was broken
  even though the router and this README document them. The fallback is now
  the file test `try_files $uri /index.html =404;`.
- **Public docs edge (AJS-465):** the portal vhost serves `/docs`, `/docs/`
  and `/llms.txt` as static files with `Cache-Control: no-cache` and the
  security-header snippet, ahead of the terminal `404`; `/v1/` is still never
  proxied. `.md` is typed `text/markdown` by `nginx/ajs-mime.types` (the map
  `nginx.conf` includes), not by a second regex location, and the runtime
  fixture confirmed `text/markdown` and `text/plain` on the wire. Controls on
  the new static assertion (manual, extracted block, not part of the gate):
  deleting the `/llms.txt` block, adding a `proxy_pass` to `/docs/`, dropping
  its `add_header Cache-Control`, moving the three blocks after the terminal
  `404`, and removing the `text/markdown` mapping each produced the expected
  `FAIL`; the unmodified vhost passed all three.
- **Still required before release:** `sudo -E bash deploy/production/check.sh`
  on the release host with `AJS_CHROMIUM_BIN` (or chromium/chrome on `PATH`).
  Production mode treats a missing browser as a required `FAIL`; this
  workstation run is a dev result only.

## Recorded verification (2026-10-26)

- **Ubuntu 24.04.5, nginx 1.24.0 (Ubuntu), production gate via passwordless sudo:**
  `sudo -E bash deploy/production/check.sh` → `check: PASS` with zero skipped
  checks. The real `nginx -t` parsed the staged portal/admin/default vhosts;
  the runtime probe observed unknown HTTP Host closed (444), unknown HTTPS and
  IP-literal Hosts answered 421, the fixed canonical redirect, and the
  canonical portal Host still at 200. The worker-read proof rendered a
  disposable htpasswd with the real renderer and read it as `www-data`. The
  AJS-412 renderer proof created its root-owned artifacts and verified the
  two-generation rotation. A real daemon binary proved `/readyz` 200 with a
  writable state directory and 503 `not_ready` (state check) with a 0555
  directory, while `/healthz` stayed 200.
- **Same host as a non-root user:** `bash deploy/production/check.sh --dev` →
  `check: DEV-PASS`; the root-only AJS-412 ownership proof is listed as a dev
  skip, and the nginx staging still passes unprivileged via `unshare -Urn`.
  `bash deploy/production/check.sh` without sudo exits 1 before any checks
  with the exact `sudo -E bash deploy/production/check.sh` instruction and
  never prints `check: PASS`.
- **Windows development host without nginx, systemd-analyze or nft:**
  `bash deploy/production/check.sh --dev` reports `DEV-PASS` and lists the
  unavailable tools as `SKIP`; the default production mode refuses the
  non-root invocation before any checks and never prints `PASS`.
- A clean `npm ci` from the committed `dashboard/package-lock.json` succeeded
  with an empty npm cache (206 packages installed). `build-bundles.sh` was
  exercised against `dashboard/`; it produced `mode: live`, `mock: false`,
  `panelToken: false`, `apiBase: ""` and the portal/admin copies are
  byte-identical by design. Pre-guard (2026-10-27), a plain `npm run build`
  produced `mode: mock`; `deploy/package.sh` refused it. Today's demo recipe is
  `npm run build:mock`.
- `deploy/package.sh --build-dashboard` packaged a live bundle and all six
  release files; `MANIFEST.txt` records `dashboard_mode: live` and the
  `ajs-build.json` digest. All five SHA-256 entries verified.
- `nft -c` accepted the production firewall through the `unshare -Urn` fallback.
- The firewall rule is compared textually with
  `deploy/nftables/ajs-panel.nft`; a drift fails the gate.

The root-mode runs above predate the AJS-421 review follow-up, which added
three assertions: a `systemd-analyze` setup failure is now a `FAIL` instead of
a silently omitted check, the AJS-412 proof reads the hash file as the unit's
daemon identity instead of only asserting mode bits, and the renderer proof
fails closed when its temporary directory cannot be created. Re-run
`sudo -E bash deploy/production/check.sh` on a Linux host and update this list
before the next release.

## Live deployment (2026-10-27)

The two public edges are live: `https://ajs.dwgx.top:2055` serves the
scoped-key API and its `/docs`, and `gate.dwgx.top:13000-13010` serves
TLS-terminated HTTP CONNECT. This section records the cutover that made them
public — hosts, paths, ports and unit names as deployed. No token, key,
password, hash or certificate material is written here.

### Live topology

```text
client --TLS--> vps-v9hw3m-6114 (152.175.35.40)
                 nginx :2055 --> chisel tunnel --> HMC nginx ajs.dwgx.top:80
                                               --> 127.0.0.1:2261 ajs-accountd
client --TLS--> vps-v9hw3m-6114 nginx stream 13000-13010
                 --> chisel local 15000-15010 --> HMC 14000-14010 (HTTP CONNECT)
```

The chisel server runs on the HMC (`ajs-chisel`, `127.0.0.1:8080`) behind the
HMC nginx `/tunnel` location; the client runs on the VPS
(`ajs-chisel-client`) and owns both public listeners. The tunnel is dialed
outbound from the VPS, so the HMC side adds no public port.

### HMC

- Daemon binary `ajs-accountd-61625b0`, built from commit `61625b0`, runs under
  the systemd unit `ajs-accountd`.
- Gate and access flags in the unit environment: `AJS_GATE_HTTP=1`,
  `AJS_GATE_HTTP_OFFSET=1000` (the stream map's `port + 1000` backend), and
  `AJS_AUTO_LOGIN=1` (which requires `AJS_ACCOUNT_LOGIN=1`).
- State directory: `/srv/dwgxhomecloud/scratch/ajs-multimodule/daemon/data`.
- Pre-cutover binary and state backup:
  `/srv/dwgxhomecloud/scratch/ajs-cutover-20261027-140730`.
- HMC nginx serves the internal vhost `ajs.dwgx.top` on `:80`, proxying to
  `127.0.0.1:2261`.
- The HMC chisel server listens on `127.0.0.1:8080` only and is published by
  the HMC nginx `/tunnel` location; it never binds a public interface.

### VPS

`vps-v9hw3m-6114` at `152.175.35.40` terminates both public surfaces.

| Surface | Listener | Backend |
|---|---|---|
| API + docs | nginx `:2055`, TLS | chisel tunnel -> HMC nginx `ajs.dwgx.top:80` -> `127.0.0.1:2261` |
| Gate data plane | nginx stream `13000-13010`, TLS | chisel local `15000-15010` -> HMC `14000-14010` (daemon HTTP CONNECT) |

- The systemd unit `ajs-chisel-client` keeps the tunnel up.
- Certificate: `/etc/letsencrypt/live/ajs.dwgx.top`, issued through the
  Cloudflare DNS-01 challenge. The SAN covers `ajs.dwgx.top` and
  `gate.dwgx.top`, so one certificate serves the API/docs vhost and all 11
  stream ports.
- Firewall: inbound `2055/tcp` and `13000-13010/tcp`; no other port was opened
  for this deployment.
- DNS: `A` records for `ajs.dwgx.top` and `gate.dwgx.top` point at
  `152.175.35.40` and are **DNS-only** (unproxied), so the VPS certificate
  terminates TLS and no CDN sits in front of either edge.

### Public names

| Name | Ports | Serves |
|---|---|---|
| `https://ajs.dwgx.top:2055` | `2055` | scoped-key API and `/docs` over TLS |
| `gate.dwgx.top` | `13000-13010` | HTTPS CONNECT, one port per gate node |

`api.dwgx.top` was not changed: it remains the existing FurCDN origin and is
not part of this deployment.

### Smoke evidence

Recorded at cutover with a scoped key issued for user `u-10` (account
`acc-07`); identifiers only, no key material:

| Check | Result |
|---|---|
| `GET https://ajs.dwgx.top:2055/v1/nodes` | `200` |
| `POST https://ajs.dwgx.top:2055/v1/leases` | `201` |
| `GET https://ajs.dwgx.top:2055/docs` | `200` |
| HTTPS CONNECT to `example.com` through `gate.dwgx.top:13000-13010` | `200` |

### Rollback

1. VPS: `systemctl stop ajs-chisel-client`; remove the `:2055` vhost and the
   gate stream symlink; `nginx -t` then reload nginx.
2. HMC: stop the new `ajs-accountd`, restore the old binary and state from
   `/srv/dwgxhomecloud/scratch/ajs-cutover-20261027-140730`, start the old
   daemon.
3. DNS: delete the two `A` records (`ajs.dwgx.top`, `gate.dwgx.top`).

Rollback deletes no daemon data: the old state is restored from the backup and
the new state tree is left in place for inspection.

## Limitations

- **Single host, no HA.** One nginx, one daemon, one state directory.
- **Two public edges, one static gate map.** `api.example.com:443` serves the
  54 scoped control-plane operations; `api.example.com:13000-13010` serves
  TLS HTTP CONNECT into the loopback gate. The SOCKS5 gate stays loopback
  only. The stream map is static: node 12 and later (`13011+`) have no public
  port until a server block is added, and a lease that rebinds to another node
  changes its port, so a client must re-read `GET /v1/leases/{id}` after a
  switch.
- **Strict daemon RBAC, restart-based revocation.** `viewer`, `operator` and
  `admin` are enforced per route. Operator files are outside panel backups;
  role/token removal takes effect after nginx reload and daemon restart.
  Keep at least one verified admin and root recovery path.
- **The SPA is not the authorization boundary.** It has no role-aware menu or
  server-side session; daemon denials remain authoritative even if a viewer
  can see an unavailable action. Keep the admin origin loopback/VPN.
- **The portal bundle is the same JS as the admin bundle.** Only the nginx
  route surface differs; do not serve the admin origin's bundle from the
  customer origin without the terminal 404.
- **No certificate automation is installed.** Certbot/ACME is an Owner input;
  the kit documents the paths and reload hook but does not run certbot.
- **The firewall file is a template.** Applying a default-drop table to a
  remote host without a tested SSH rule can lock you out. Merge it into the
  existing host firewall when one exists.
- **`AJS_ACCOUNT_LOGIN` stays off by default.** Enabling vendor login requires
  a vendor account and the pinned control certificate; see
  [`deploy/README.md`](../README.md#limitations) and
  [`daemon/README.md`](../../daemon/README.md).
