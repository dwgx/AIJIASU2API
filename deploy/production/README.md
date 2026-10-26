# Production deploy kit (AJS-387)

This directory is the engineering half of release gate 2: a production-like
deployment of `ajs-accountd` and the dashboard **without Docker**. It provides
nginx TLS termination, a token-free customer portal bundle, an admin origin
whose bearer never reaches a browser, a hardened systemd unit, a firewall
table that matches `deploy/nftables/`, and `check.sh` as the gate.

Nothing here is a credential. There are no certificates, private keys, tokens
or filled-in environment files in this tree — only paths, placeholders and the
template from which the host renders its admin bearer.

Run the gate:

```bash
bash deploy/production/check.sh
# check: PASS (n checks, 0 skipped)
```

The default mode is the production gate. It requires a real host `nginx -t`,
`openssl`, `systemd-analyze` and `nft`; a missing required tool is a `FAIL`,
not a `SKIP`. For a workstation without those tools use the explicitly
non-production mode:

```bash
bash deploy/production/check.sh --dev
# check: DEV-PASS (n checks, m skipped; not a production gate)
```

`check.sh` is read-only. It stages nginx configs in a throwaway directory with
a generated self-signed certificate and a dummy token, pins the nginx PID and
all temp paths inside that directory, runs a real `nginx -t` as the invoking
user, validates the YAML inventory, scans the kit for committed secrets,
verifies the systemd unit, and compares the production firewall rule with
`deploy/nftables/ajs-panel.nft`.

## What the kit contains

| Path | Purpose |
|---|---|
| `nginx/nginx.conf` | master config: MIME map, rate-limit zones, upstream, TLS defaults, includes the two sites |
| `nginx/sites-available/ajs-portal.conf` | public customer origin, TLS, HSTS, portal-only routes |
| `nginx/sites-available/ajs-admin.conf` | private admin origin, loopback/VPN allowlist, proxy-injected bearer |
| `nginx/snippets/ajs-tls.conf` | shared TLS 1.2+/1.3 policy |
| `nginx/snippets/ajs-proxy-common.conf` | `X-Forwarded-For` overwrite + trusted-proxy note |
| `nginx/snippets/ajs-security-headers.conf` | HSTS and the other response headers |
| `nginx/snippets/ajs-admin-token.conf.template` | template for the root-only bearer snippet |
| `systemd/ajs-accountd.service` | hardened unit (`ProtectSystem=strict`, empty capability set, syscall filter) |
| `systemd/ajs-accountd.env.example` | exact daemon environment, placeholders only |
| `firewall/ajs-production.nft` | standalone nftables table matching the repo-owned LAN block |
| `build-bundles.sh` | builds the live, same-origin, **token-free** portal and admin bundles from a locked `npm ci` tree and verifies `ajs-build.json` |
| `render-admin-token.sh` | renders the nginx bearer snippet at mode 0600, never prints it |
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
- `systemd/ajs-accountd.env.example` (`AJS_TENANT_ORIGIN`)

`AJS_TENANT_ORIGIN` must be the exact bare origin (`https://panel.example.com`),
with no path, query or trailing slash. The daemon refuses a path and uses the
value for the portal's exact `Origin` check.

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

Generate both on the host; never commit or paste them into a report:

```bash
openssl rand -hex 32   # AJS_ADMIN_TOKEN  (also the nginx-injected bearer)
openssl rand -hex 32   # AJS_MASTER_KEY   (AES-256-GCM account-password sealing)
```

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
| `AJS_ADMIN_TOKEN` | yes | 64 hex chars | bearer for every `/v1/*` admin call; nginx injects it server-side |
| `AJS_MASTER_KEY` | recommended | 64 hex chars | seals account passwords; empty disables at-rest sealing |
| `AJS_TENANT_PORTAL` | yes | `1` | registers `/v1/portal/*`, `/v1/admin/tenants*`, `/v1/admin/orders*`, `/v1/admin/plans*` |
| `AJS_TENANT_ORIGIN` | yes | `https://panel.example.com` | exact browser origin allowed on portal signup/login and every unsafe portal request |
| `AJS_TRUSTED_PROXIES` | yes | `127.0.0.1,::1` | only nginx's loopback peers may supply `X-Forwarded-For`; never a public CIDR or `0.0.0.0/0` |
| `AJS_CORS_ORIGINS` | optional | empty | both bundles are same-origin through nginx; set only for a separate browser client |
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
sudo install -d -m 0750 -o root -g root /etc/ajs /etc/ajs/tls
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

# 6. Admin bearer, root-only; never echoed
sudo AJS_ADMIN_TOKEN="$(grep '^AJS_ADMIN_TOKEN=' /etc/ajs/accountd.env | cut -d= -f2-)" \
  sh deploy/production/render-admin-token.sh

# 7. Validate and start
sudo nginx -t && sudo systemctl reload nginx
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
                     ├── /v1/*           -> 127.0.0.1:2261 + injected bearer
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
installed with `npm ci`. Every build emits `dist/ajs-build.json`; the script
refuses to copy a bundle unless the manifest says `mode: live`, `mock: false`,
`panelToken: false` and an empty `apiBase`. A plain `npm run build` is mock
mode and cannot be packaged.

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

### Admin origin: bearer injected by the proxy

The admin bundle is also built without a token. `nginx/sites-available/ajs-admin.conf`
includes `snippets/ajs-admin-token.conf` inside every `/v1/` location, which
sets:

```nginx
proxy_set_header Authorization "Bearer <AJS_ADMIN_TOKEN>";
```

The rendered file is root-owned `0600` and is never served, never bundled and
never visible to a browser. `render-admin-token.sh` takes the token from the
environment (or `/etc/ajs/admin.token`), refuses placeholders and anything
shorter than 32 characters, and prints only the destination path.

Access policy is two independent layers:

1. **Listen address** — `127.0.0.1:8443` by default. Reach it through an SSH
   tunnel (`ssh -L 8443:127.0.0.1:8443 user@host`) or uncomment the
   `listen 10.8.0.1:443 ssl http2;` line to bind a VPN-only interface.
2. **nginx `allow`/`deny`** — loopback plus the VPN client CIDR
   (`10.8.0.0/24` in the template), ending in `deny all;`. Requests from any
   other peer get 403 even if the listener is accidentally exposed.

There is no admin login page and no per-operator identity in this kit: the
bearer is one shared secret. Put an authenticating reverse proxy or VPN in
front if more than one operator needs access, and rotate the token by editing
`/etc/ajs/accountd.env` and re-running `render-admin-token.sh`.

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

# health
curl -fsS http://127.0.0.1:2261/healthz
curl -fsS https://panel.example.com/healthz

# admin through an SSH tunnel
ssh -N -L 8443:127.0.0.1:8443 user@host
# browse https://admin.example.com:8443 (map admin.example.com to 127.0.0.1
# in /etc/hosts, or use https://127.0.0.1:8443 and accept the name mismatch)
# No token prompt should appear: nginx injects the bearer.
```

The daemon's state lives in `/var/lib/ajs`: `state.json` (rewritten every
`AJS_SNAPSHOT_INTERVAL`, default 30 s, and on shutdown), `secrets.json` and
`device-secrets.json` when `AJS_MASTER_KEY` is set. Back up the directory
while the service is stopped, together with the master key; without the key
the sealed envelopes cannot be opened.

## Verification

`check.sh` performs:

1. required-file presence and shell syntax (`bash -n` / `sh -n`);
2. YAML parse of `inventory.example.yml` and required top-level keys
   (Python+PyYAML, Ruby or yq);
3. a secret scan over every kit file: PEM private keys/certificates, token
   assignments, literal `Authorization: Bearer` values, and bare 64-hex
   strings; the admin-token template must contain only `${AJS_ADMIN_TOKEN}`;
4. a real `nginx -t` on a staged copy of both vhosts with a generated
   self-signed certificate and a dummy 64-character token. The staged config
   rewrites the PID file and every `*_temp_path` into the throwaway tree, so
   the gate passes as an unprivileged user on stock Debian/Ubuntu nginx
   without writing `/run/nginx.pid` or `/var/lib/nginx/*`. Because nginx -t
   opens the listening sockets even in test mode, an unprivileged run retries
   in an isolated user+network namespace (`unshare -Urn`) where it can bind
   :80/:443. Production mode requires host nginx; Docker
   `nginx:1.29-alpine` staging is dev-only (`AJS_CHECK_NGINX_DOCKER=1`);
5. static nginx assertions: portal terminal 404, no generic `/v1/` proxy on
   the portal origin, admin token include, allow/deny, HSTS, rate-limit zones,
   `X-Forwarded-For $remote_addr`;
6. token-free bundle assertions on `build-bundles.sh`, the committed
   `dashboard/package-lock.json`, and the `ajs-build.json` live/mock manifest;
7. `systemd-analyze verify` plus the hardening directives;
8. the production firewall rule exactly matching `deploy/nftables/ajs-panel.nft`
   and `nft -c` (direct, `unshare -Urn`, or `sudo -n` fallback);
9. README input coverage (`AJS_TENANT_PORTAL`, `AJS_TENANT_ORIGIN`, `AJS_ADMIN_ORIGINS`,
   `AJS_TRUSTED_PROXIES`, `AJS_ADMIN_TOKEN`, `AJS_MASTER_KEY`, DNS, TLS).

In production mode every item above is required: a missing tool is a `FAIL`
and the summary reports the required-skip count. `--dev` reports absent tools
as `SKIP` and ends with `check: DEV-PASS`, never `check: PASS`. A missing YAML
parser is always a failure because YAML validation is a gate requirement.

## Recorded verification (2026-10-26)

- **Ubuntu 24.04.5, nginx 1.24.0 (Ubuntu), non-root uid 1000:**
  `bash deploy/production/check.sh` → `check: PASS (58 checks, 0 skipped)`.
  The real `nginx -t` parsed the staged portal/admin vhosts; because nginx -t
  opens the listening sockets even in test mode, the unprivileged run used the
  `unshare -Urn` fallback (the direct run reached `syntax is ok` and failed only
  at `bind() :80 permission denied`). No required check was skipped.
- **Windows development host without nginx, systemd-analyze or nft:**
  `bash deploy/production/check.sh --dev` → `check: DEV-PASS (55 checks,
  3 skipped; not a production gate)`. The same host in the default production
  mode → `check: FAIL (3 failed, 3 required skips, 58 checks)` — no `PASS` with
  skips.
- A clean `npm ci` from the committed `dashboard/package-lock.json` succeeded
  with an empty npm cache (206 packages installed). `build-bundles.sh` was
  exercised against `dashboard/`; it produced `mode: live`, `mock: false`,
  `panelToken: false`, `apiBase: ""` and the portal/admin copies are
  byte-identical by design. A plain `npm run build` produced `mode: mock`;
  `deploy/package.sh` refused it.
- `deploy/package.sh --build-dashboard` packaged a live bundle and all six
  release files; `MANIFEST.txt` records `dashboard_mode: live` and the
  `ajs-build.json` digest. All five SHA-256 entries verified.
- `nft -c` accepted the production firewall through the `unshare -Urn` fallback.
- The firewall rule is compared textually with
  `deploy/nftables/ajs-panel.nft`; a drift fails the gate.

## Limitations

- **Single host, no HA.** One nginx, one daemon, one state directory.
- **One shared admin bearer.** No per-operator identity, roles or revocation;
  the proxy/VPN is the only user boundary. Rotate by re-rendering the snippet
  and restarting/reloading nginx.
- **Admin UI is not a hardened multi-user console.** Anyone who reaches the
  admin origin can do everything the bearer allows. Keep it loopback/VPN.
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
