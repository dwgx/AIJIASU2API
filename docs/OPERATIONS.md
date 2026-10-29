# Operations

Status: current for `v0.1.0-pre.3` (release prep, 2026-10-29; tag not yet created). This is the
operator runbook: build, install, health, backup, rotation and rollback. It contains no secrets
and no credential values.

Two shapes exist and they are **not** the same:

| Shape | Source | Status |
|---|---|---|
| Reviewed production kit | `deploy/production/` in this tree | reproducible; gate `check.sh` last recorded a root `PASS` on 2026-10-26, **before** AJS-421 added three assertions. Re-run it before release. |
| Live deployment (`ajs.dwgx.top`, `gate.dwgx.top`) | ad-hoc root unit + hand-written VPS vhost, **not in this tree** | serving today; partially converged with the kit on 2026-10-28 (see [Live host as-built](#live-host-as-built)) |

If you are installing fresh, use the kit. If you are operating the live host, treat the kit as
the target state and the live record as the current state. The live daemon now binds loopback
and the unit carries sandbox directives, but it still runs as root and without
`AJS_RBAC_STRICT`; do not assume the full hardened unit is in force there.

## Build

Prerequisites: Go **1.26.10 or newer** (the release floor for the stdlib advisories fixed in
Go 1.26.9/1.26.10, release audit R1), Node 24 / npm 11 for the dashboard, and a POSIX shell
with `tar` and `sha256sum`/`shasum`.

```bash
# daemon + emulator tests (offline)
cd daemon && go vet ./... && go test -race -count=1 ./...
cd ../emulator && go vet ./... && go test -race -count=1 ./...

# dashboard: lockfile, generated docs, tests, live build
cd ../dashboard
npm ci --no-audit --no-fund
npm run verify:lockfile
npm run docs:gen -- --check
npm test -- --pool=forks --maxWorkers=1
VITE_USE_MOCK=false VITE_API_BASE= npm run build

# release artifacts (six files + deploy kit)
cd ..
SOURCE_DATE_EPOCH=0 bash deploy/package.sh --version v0.1.0-pre.3 --out /tmp/ajs-pkg
(cd /tmp/ajs-pkg && sha256sum -c SHA256SUMS)
```

`package.sh` cross-compiles linux/amd64, windows/amd64 and darwin/arm64 with
`CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, `-mod=readonly` and `GOTOOLCHAIN=local`.
It refuses a dashboard bundle that is not `mode: live`, `mock: false`, `panelToken: false`.
`--version` is not just a manifest label: the script stamps it into the binaries with
`-X main.buildVersion=<version>`, and stamps the packaged commit with
`-X main.buildCommitValue=<commit>` (`git rev-parse HEAD`, or `unknown`). A binary built by
`package.sh` from a release tag therefore reports that tag from `/healthz` and prints
`ajs-accountd <tag> (<commit>)` for `-version` — the published `v0.1.0-pre.2` build reported
`v0.1.0-pre.2` and its commit; the `v0.1.0-pre.3` build will be stamped the same way when the tag
exists. A plain `go build`, `go run` or `go test`
keeps the fallback `0.1.0`. The accountd container image takes the same two values as the
`AJS_VERSION`/`AJS_COMMIT` build args: `release.yml` passes the tag and the commit SHA, and a
local `docker build` that passes neither gets `0.1.0` / `unknown`. The stamped values are a
convenience, not authority: identify a release by its tag and `MANIFEST.txt`.

## Install from the kit

The full walkthrough is [`deploy/production/README.md`](../deploy/production/README.md). The
short form, as root on Debian/Ubuntu:

```bash
# 1. identity, directories, binary
useradd --system --user-group --home /var/lib/ajs --shell /usr/sbin/nologin ajs
install -d -m 0750 -o root -g ajs /etc/ajs
install -d -m 0750 -o root -g root /etc/ajs/tls
install -d -m 0700 -o ajs -g ajs /var/lib/ajs
install -m 0755 ajs-accountd-linux-amd64 /usr/local/bin/ajs-accountd

# 2. environment (placeholders only; edit on the host)
install -m 0600 -o root -g root \
  deploy/production/systemd/ajs-accountd.env.example /etc/ajs/accountd.env
sudo -e /etc/ajs/accountd.env

# 3. hardened unit
install -m 0644 deploy/production/systemd/ajs-accountd.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now ajs-accountd
curl -fsS http://127.0.0.1:2261/healthz

# 4. bundles, nginx, operator identities: kit README steps 4-7
```

The unit runs as `User=ajs`, `ProtectSystem=strict`, empty capability set, `StateDirectory=ajs`,
and only `/var/lib/ajs` writable. `/etc/ajs` is read-only to the daemon. Do not run the daemon
as root: the live host does, and that is a documented divergence, not a recommendation.

The portal and payment surfaces are **off** in this release: the env example ships
`AJS_TENANT_PORTAL=0`, and `ajs-portal.conf` is a non-product template. With the portal off the
daemon does not register `/v1/portal/*`, `/v1/admin/tenants*`, `/v1/admin/orders*` or
`/v1/admin/plans*`, so those paths answer `404` even if a vhost proxies them. Install
`ajs-api.conf`, `ajs-admin.conf` and `ajs-default.conf`; do not install `ajs-portal.conf` on a
`v0.1.0-pre.3` host unless you are running the production gate's staged fixture.

## systemd

```bash
systemctl status ajs-accountd
journalctl -u ajs-accountd -n 200 --no-pager
systemctl restart ajs-accountd     # reloads the operator hash file and all env
systemctl stop ajs-accountd
```

The daemon reloads operator tokens, `AJS_LISTEN`, gate flags and feature switches only on
restart. `systemctl reload` is not defined. State is saved on `SIGTERM`; `TimeoutStopSec=30s`
in the unit.

## nginx

```bash
nginx -t && systemctl reload nginx
```

- API vhost: `deploy/production/nginx/sites-available/ajs-api.conf` — 41 allowlisted paths,
  terminal `404`, static `/healthz`, no injected credential.
- Admin vhost: `ajs-admin.conf` — `127.0.0.1:8443`, per-operator Basic auth, mapped bearer.
- Portal vhost: `ajs-portal.conf` — **not installed** for this release.
- Stream map: `nginx/streams-available/ajs-gate.conf` — 11 TLS servers, `13000-13010` →
  `127.0.0.1:14000-14010`. Install as `/etc/nginx/streams-enabled/ajs-gate.conf`; requires
  `libnginx-mod-stream`.
- `server_tokens off` is set in the kit master config and was applied live on 2026-10-28; the
  public edge no longer advertises the nginx version.

## TLS and DNS

Kit: one certificate for the panel name with the api name as a **SAN**; a second self-signed
pair for the private admin name. The stream map reuses the api pair. Renewal is
`systemctl reload nginx`; certbot's deploy hook can do it. Keys `0600`–`0640`, root-owned.

Live: `/etc/letsencrypt/live/ajs.dwgx.top` (SAN `ajs.dwgx.top`, `gate.dwgx.top`), issued via
Cloudflare DNS-01, valid 2026-10-27 → 2027-01-25, `certbot-renew.timer` active with a
`reload-nginx.sh` deploy hook. `A` records for both names point at the VPS and are **DNS-only**
(no CDN), so the VPS terminates TLS. On 2026-10-28 the public vhost moved to ECDHE-AEAD TLS 1.2
ciphers with `ssl_prefer_server_ciphers off`; the HSTS header still lacks `includeSubDomains`.
No `AAAA` record was observed; IPv6 clients have no path.

## Firewall

Kit: `firewall/ajs-production.nft` is a complete default-drop table; merge it, do not replace
an existing host firewall blind. Dry-run first: `nft -c -f deploy/production/firewall/ajs-production.nft`.

Live: VPS default-drop input allows only `2055/tcp` and `13000-13010/tcp` for this service,
plus separately declared management/WireGuard services. SELinux is `Enforcing` with
`httpd_can_network_connect=on`. HMC nftables does **not** allow the VPS to reach `2261`; the
gate backends are loopback-only.

## Health

| Probe | Where | Meaning |
|---|---|---|
| `GET /healthz` | daemon `2261`, portal origin, static at the public API edge | liveness; the daemon's own response also carries version and per-state account counts |
| `GET /readyz` | daemon `2261`, admin origin, portal origin | readiness: state-directory write probe plus configured integrations; `200 {"status":"ready"}` or `503 {"status":"not_ready","reasons":[...]}` |
| `GET /v1/health/nodes` | daemon (scoped `health:read`) | loopback gate reachability per watched node, **not** vendor reachability |

At the live public API edge (`https://ajs.dwgx.top:2055`) `/healthz` is a static `200 "ok"` and
`/readyz` answers **`404`**: the live VPS vhost does not publish readiness. Point readiness
probes at the daemon (or the portal origin, if that optional template is installed), never at
the public API name. Since 2026-10-28 `/metrics` is proxied end-to-end and answers **`401`**
unauthenticated (it answered `404` before); a `metrics:read` scoped key is required.
`/v1/metrics` also answers `401`, and `/docs` answers `200`.

Useful live checks (read-only, no credential):

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://ajs.dwgx.top:2055/healthz   # 200
curl -sS -o /dev/null -w '%{http_code}\n' https://ajs.dwgx.top:2055/docs      # 200
curl -sS -o /dev/null -w '%{http_code}\n' https://ajs.dwgx.top:2055/readyz    # 404
curl -sS -o /dev/null -w '%{http_code}\n' https://ajs.dwgx.top:2055/metrics   # 401
curl -sS -o /dev/null -w '%{http_code}\n' https://ajs.dwgx.top:2055/v1/metrics # 401
curl -sS -o /dev/null -w '%{http_code}\n' https://ajs.dwgx.top:2055/v1/nodes  # 401
curl -sS -m 10 -x https://gate.dwgx.top:13000 -o /dev/null -w '%{http_code}\n' https://example.com  # 407 without a key
```

## Backups

Two layers, both needed:

1. **Panel backup envelope** — `POST /v1/backup` (admin) seals `state.json`, `secrets.json` and
   `device-secrets.json` into one passphrase-encrypted `.ajsb` (AES-256-GCM over a deterministic
   tar, PBKDF2-SHA256 600,000 iterations). `POST /v1/backup/inspect` reads the manifest;
   `POST /v1/backup/restore` diffs and, with `dryRun:false` + `confirm:"RESTORE"`, applies while
   the daemon is stopped. A restore is refused while any node is `LIVE`.
2. **Filesystem copy** of `/var/lib/ajs` (`state.json`, `secrets.json`, `device-secrets.json`,
   optional journal) with `/etc/ajs/accountd.env` stored separately.

Back up `AJS_MASTER_KEY` with the state, but not in the same place as the encrypted envelope:
without the key, sealed passwords and device identities are unrecoverable; with the key beside
the envelope, the envelope's passphrase is the only remaining barrier.

Restore drill (offline, no vendor host):

```bash
systemctl stop ajs-accountd
cp /var/lib/ajs/state.json /var/lib/ajs/state.json.pre-restore
# apply the envelope or copy the files back, keeping owner ajs:ajs and mode 0600
systemctl start ajs-accountd
curl -fsS http://127.0.0.1:2261/readyz
```

Operator files (`/etc/ajs/admin-operators`, rendered hashes, nginx token map) are **outside**
the panel backup by design. Back them up separately or be prepared to re-render them.

## Key and credential rotation

| Secret | Rotation | Effect |
|---|---|---|
| Operator password | re-render `/etc/nginx/ajs-admin.htpasswd`, `nginx -s reload` | old password stops working; API token unchanged |
| Operator API token | edit `/etc/ajs/admin-operators`, re-render, reload nginx **and** restart the daemon | old token stops working; password unchanged |
| Scoped key `ajs_sk_…` | revoke in the console, mint a new one | new handshakes fail immediately; established flows close within `EntitlementInterval` (10 s) at this commit. The **live** binary predates the watch, so there revocation still only stops new sessions until redeployed |
| Vendor account password | patch the account, re-login | requires `AJS_ACCOUNT_LOGIN=1`; sessions restart |
| `AJS_MASTER_KEY` | re-seal plan only | sealed passwords/device identities are bound to the old key; a silent replacement loses them |
| `AJS_ADMIN_TOKEN` (legacy) | remove after strict cutover | never accepted in strict mode |
| `AJS_EPAY_KEY`, `AJS_STRIPE_WEBHOOK_SECRET` | rotate at the provider, update env, restart | epay/Stripe are experimental and off in this release |
| chisel account (live) | edit `/opt/ajs/chisel-users.json`, restart `ajs-chisel` and `ajs-chisel-client` | one account; the authfile is anchored to `127.0.0.1:14000-14010` (SecEdge E-01, applied 2026-10-28) |

Two-generation rotation for operator identities is in [`docs/RBAC.md`](RBAC.md).

## Upgrade and rollback

Upgrade:

1. Build or unpack the new artifact set; verify `sha256sum -c SHA256SUMS`.
2. Stop the daemon. Copy `state.json`/`secrets.json` aside.
3. Replace `/usr/local/bin/ajs-accountd`, install any changed unit/vhost, `nginx -t`.
4. Start the daemon, then reload nginx. Check `/readyz`, then one authenticated scoped-key read.

Rollback:

1. Stop the daemon.
2. Restore the previous binary **and** the pre-upgrade state files. The snapshot is versioned
   (this build writes v2); a v2 `state.json` is rejected by a v1 binary, so restoring the
   backup is required, not optional.
3. Restore the previous nginx/stream configs, `nginx -t`, reload.
4. Start the daemon. Do not "roll back" by deleting the new state tree; leave it for inspection.

Live-host rollback is recorded in
[`deploy/production/README.md`](../deploy/production/README.md#rollback): stop
`ajs-chisel-client`, remove the `:2055` vhost and the gate stream symlink, restore the old HMC
binary/state from `/srv/dwgxhomecloud/scratch/ajs-cutover-20261027-140730`, and delete the two
`A` records.

## Live host as-built

Recorded 2026-10-27 and updated 2026-10-28 after the live edge fixes; read-only, no credential
values:

| Item | Live value | Kit expectation |
|---|---|---|
| Unit | ad-hoc system unit, `EnvironmentFile=/srv/dwgxhomecloud/scratch/ajs-deploy-20261024/daemon/.env`, `ExecStart=/srv/dwgxhomecloud/scratch/ajs-accountd-61625b0`, **no `User=`**; since 2026-10-28 `NoNewPrivileges=yes`, `PrivateTmp=yes`, `ProtectSystem=full`, `ProtectHome=yes`, `ReadWritePaths=/srv/dwgxhomecloud/scratch/ajs-multimodule/daemon/data` | `User=ajs`, `/etc/ajs/accountd.env`, `/usr/local/bin/ajs-accountd`, `ProtectSystem=strict`, `StateDirectory=ajs` |
| Listener | `AJS_LISTEN=127.0.0.1:2261` (since 2026-10-28) | `127.0.0.1:2261` |
| RBAC | `AJS_ADMIN_OPERATORS_FILE` set, **no `AJS_RBAC_STRICT`** → compatibility mode, legacy shared bearer accepted | `AJS_RBAC_STRICT=1`, no shared bearer |
| API vhost | hand-written `/etc/nginx/sites-enabled/ajs-public.conf` on `:2055`, upstream `10.99.0.2:80`, docs root `/var/www/ajs`; no copy in the tree; limits/headers/TLS applied 2026-10-28 | `ajs-api.conf` on `:443` |
| HMC origin | vhost allows only `10.99.0.1` and `127.0.0.1`; LAN `/v1/whoami` 403, VPS path 401 | no separate internal origin; the kit serves `ajs-api.conf` on `:443` |
| Gate map | `13000-13010` → `127.0.0.1:15000-15010` (chisel); chisel forwards anchored to `127.0.0.1:14000-14010` | `13000-13010` → `127.0.0.1:14000-14010` |
| Edge limits | 120 r/m per IP with burst 60, 20 conn/IP, 400 global, 2 MB body cap (since 2026-10-28) | 120 r/m per IP, 20 conn/IP, 400 global, 2 MB body |
| Security headers | HSTS (no `includeSubDomains`), CSP, frame, referrer and permissions headers; `server_tokens off` (since 2026-10-28) | full CSP/frame/referrer/permissions set |
| TLS | ECDHE-AEAD TLS 1.2 ciphers, `ssl_prefer_server_ciphers off` (since 2026-10-28) | ECDHE-AEAD only |
| Payments | `AJS_EPAY_*` removed; log `epay_disabled` and `stripe_webhook_disabled`; portal routes unregistered | `AJS_TENANT_PORTAL=0`, no payment env |
| State dir | `/srv/dwgxhomecloud/scratch/ajs-multimodule/daemon/data` | `/var/lib/ajs` |

Remaining gaps are not choices to copy. Track them as work items; the release notes list them
as known limitations.

## Pointers

- [`docs/ARCHITECTURE.md`](ARCHITECTURE.md) — topology and trust boundaries
- [`docs/RELEASE.md`](RELEASE.md) — tag, gate, artifact and checksum procedure
- [`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md) — what the edge does and does not enforce
- [`docs/PRIVACY.md`](PRIVACY.md) — data, retention and operator access
