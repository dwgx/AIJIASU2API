# Production deploy kit (AJS-387)

This directory is the engineering half of release gate 2: a production-like
deployment of `ajs-accountd` and the dashboard **without Docker**. It provides
nginx TLS termination, a token-free customer portal bundle, an admin origin
whose bearer never reaches a browser, a hardened systemd unit, a firewall
table that matches `deploy/nftables/`, and `check.sh` as the gate.

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
requires a real host `nginx -t`, `openssl`, `systemd-analyze`, `nft`, and a
daemon binary for the `/readyz` proof (`AJS_ACCOUNTD_BIN`, `ajs-accountd` on
`PATH`, `daemon/ajs-accountd`, or `go build`); a missing required tool is a
`FAIL`, not a `SKIP`. For a workstation without those tools, or for a non-root
static run, use the explicitly non-production mode:

```bash
bash deploy/production/check.sh --dev
# check: DEV-PASS (some checks skipped; not a production gate)
```

`check.sh` is read-only. It stages nginx configs in a throwaway directory with
a generated self-signed certificate and a dummy token, pins the nginx PID and
all temp paths inside that directory, runs a real `nginx -t` as the invoking
user, starts that staged config to prove unknown Hosts are refused on :80/:443
and the portal redirect is canonical, validates the YAML inventory, scans the
kit for committed secrets, starts a daemon binary to prove `/readyz` fails on
an unwritable state directory, verifies the systemd unit, and compares the
production firewall rule with `deploy/nftables/ajs-panel.nft`.

## What the kit contains

| Path | Purpose |
|---|---|
| `nginx/nginx.conf` | master config: MIME map, rate-limit zones, upstream, TLS defaults, includes the site configs |
| `nginx/sites-available/ajs-default.conf` | default-deny listeners: HTTP 444 / HTTPS 421 for unknown Host |
| `nginx/sites-available/ajs-portal.conf` | public customer origin, TLS, HSTS, portal-only routes |
| `nginx/sites-available/ajs-admin.conf` | private admin origin, loopback/VPN allowlist, per-operator Basic auth, proxy-injected bearer |
| `nginx/snippets/ajs-tls.conf` | shared TLS 1.2+/1.3 policy |
| `nginx/snippets/ajs-proxy-common.conf` | `X-Forwarded-For` overwrite + trusted-proxy note |
| `nginx/snippets/ajs-security-headers.conf` | HSTS and the other response headers |
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

## Owner inputs

These are the only things the Owner must supply. Everything else is in this
tree or generated on the host.

### DNS

| Name | Record | Exposure |
|---|---|---|
| `panel.example.com` | `A` (and `AAAA` if the host has IPv6) to the host's public address | public internet |
| `admin.example.com` | private DNS, split-horizon, or `/etc/hosts` to `127.0.0.1` | loopback or VPN only — do **not** create a public record |

Replace `panel.example.com` and `admin.example.com` in:
- `nginx/sites-available/ajs-portal.conf` (`server_name`, certificate paths)
- `nginx/sites-available/ajs-admin.conf` (`server_name`, certificate paths)
- `nginx/sites-available/ajs-default.conf` (the HTTPS default listener uses the portal certificate)
- `systemd/ajs-accountd.env.example` (`AJS_TENANT_ORIGIN`, `AJS_ADMIN_ORIGINS`)

`AJS_TENANT_ORIGIN` must be the exact bare origin (`https://panel.example.com`),
with no path, query or trailing slash. The daemon refuses a path and uses the
value for the portal's exact `Origin` check. `AJS_ADMIN_ORIGINS` must be the
admin SPA's exact bare origin (`https://admin.example.com:8443` in this kit);
unsafe admin requests from any other `Origin` are refused before the handler.

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
/etc/ajs/tls/admin.example.com/fullchain.pem
/etc/ajs/tls/admin.example.com/privkey.pem
```

Supply them by one of:

```bash
# Let's Encrypt (HTTP-01 needs port 80 reachable)
sudo certbot certonly --standalone -d panel.example.com
sudo install -d -m 0750 /etc/ajs/tls/panel.example.com /etc/ajs/tls/admin.example.com
sudo install -m 0640 /etc/letsencrypt/live/panel.example.com/fullchain.pem /etc/ajs/tls/panel.example.com/
sudo install -m 0640 /etc/letsencrypt/live/panel.example.com/privkey.pem  /etc/ajs/tls/panel.example.com/

# Admin name is private; a self-signed cert is acceptable inside the VPN:
sudo openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout /etc/ajs/tls/admin.example.com/privkey.pem \
  -out    /etc/ajs/tls/admin.example.com/fullchain.pem \
  -subj "/CN=admin.example.com" \
  -addext "subjectAltName=DNS:admin.example.com,IP:127.0.0.1"
```

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
| `AJS_GATE_HTTP` | off | loopback gate HTTP bridge, off by default |
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

# 5. nginx
sudo apt-get install -y nginx
sudo cp deploy/production/nginx/nginx.conf /etc/nginx/nginx.conf
sudo cp deploy/production/nginx/ajs-mime.types /etc/nginx/ajs-mime.types
sudo cp deploy/production/nginx/snippets/*.conf /etc/nginx/snippets/
sudo cp deploy/production/nginx/sites-available/ajs-*.conf /etc/nginx/sites-enabled/
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

`check.sh` fails if the two drift. Gate ports (for example acc-07's `:13012`)
remain loopback-only and are not opened by either table.

## Routing model

```text
browser ── HTTPS ──> nginx :443 (panel.example.com)
                     ├── /portal*        -> /var/lib/ajs/production/portal (SPA)
                     ├── /assets/*       -> static bundle
                     ├── /v1/portal/*    -> 127.0.0.1:2261 (cookie + CSRF)
                     └── /v1/payments/{epay/notify,stripe} -> 127.0.0.1:2261

operator ── SSH tunnel/VPN ──> nginx 127.0.0.1:8443 (admin.example.com)
                     ├── /               -> /var/lib/ajs/production/admin (SPA)
                     └── /v1/*           -> 127.0.0.1:2261 + per-operator bearer
                     └── /v1/events/stream -> SSE, buffering off
```

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

The customer origin serves **only** `/portal`, `/portal/*`, `/assets/*` and
the portal API/callback paths. `location / { return 404; }` is terminal: the
admin SPA routes (`/accounts`, `/nodes`, `/settings`, …) and the admin API
(`/v1/accounts`, `/v1/nodes`, …) are not routed on this origin. The same static
bundle is copied to both roots because the admin route code is lazy-loaded and
dead on the portal origin; the deployment edge, not a source fork, is what
makes the customer surface portal-only.

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
  Rejections are `429` (`limit_req_status 429`).
- **daemon**: per-IP portal buckets (120/min global, 5/min signup, 10/min
  login) and per-email buckets, which return JSON with `Retry-After`.

The nginx zone is intentionally slightly looser so the daemon's JSON error is
the authority for portal clients. The admin zone protects the single bearer
from brute-force/accidental loops.

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
4. a real `nginx -t` on a staged copy of both vhosts with a generated
   self-signed certificate, a dummy 64-character token and a throwaway
   operator htpasswd. The staged config
   rewrites the PID file and every `*_temp_path` into the throwaway tree, so
   the nginx stage passes as an unprivileged user on stock Debian/Ubuntu nginx
   without writing `/run/nginx.pid` or `/var/lib/nginx/*`. Because nginx -t
   opens the listening sockets even in test mode, an unprivileged run retries
   in an isolated user+network namespace (`unshare -Urn`) where it can bind
   :80/:443. Production mode requires host nginx; Docker
   `nginx:1.29-alpine` staging is dev-only (`AJS_CHECK_NGINX_DOCKER=1`);
5. a runtime nginx probe on the staged `:80`/`:443` listeners: an unknown
   HTTP `Host` must be closed (`444`), an unknown HTTPS `Host` and an
   IP-literal `Host` must answer `421`, the portal HTTP redirect must use the
   fixed `https://panel.example.com` origin, and the canonical portal `Host`
   must still reach `/healthz` with `200`. The probe prefers an isolated
   network namespace (`unshare -n` as root, `unshare -Urn` otherwise) so a
   running host nginx does not conflict with the staged listeners;
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
   the portal origin, admin token include, allow/deny, default-deny `444`/`421`,
   canonical redirect, `/readyz` proxy, HSTS, rate-limit zones,
   `X-Forwarded-For $remote_addr`;
9. token-free bundle assertions on `build-bundles.sh`, the committed
   `dashboard/package-lock.json`, and the `ajs-build.json` live/mock manifest;
10. `systemd-analyze verify` plus the hardening directives;
11. the production firewall rule exactly matching `deploy/nftables/ajs-panel.nft`
    and `nft -c` (direct, `unshare -Urn`, or `sudo -n` fallback);
12. README input coverage (`AJS_TENANT_PORTAL`, `AJS_TENANT_ORIGIN`, `AJS_ADMIN_ORIGINS`,
    `AJS_TRUSTED_PROXIES`, `AJS_ADMIN_TOKEN`, `AJS_MASTER_KEY`, `X-AJS-Operator`,
    `render-admin-htpasswd`, `admin-operators`, DNS, TLS);
13. the AJS-412 production renderer acceptance: a valid inventory produces
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

In production mode every item above is required: a missing tool is a `FAIL`
and the summary reports the required-skip count. Production mode also refuses
to start as a non-root user instead of deferring the root-only renderer proof
to a late required-skip failure. `--dev` reports absent tools and the root-only
ownership proof as `SKIP` and ends with `check: DEV-PASS`, never `check: PASS`.
A missing YAML parser is always a failure because YAML validation is a gate
requirement.

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

## Limitations

- **Single host, no HA.** One nginx, one daemon, one state directory.
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
