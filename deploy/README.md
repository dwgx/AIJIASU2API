# Control-plane deployment (`deploy/`)

A deployment of the parts of this project that run on a host: `ajs-accountd` (the panel API
and node controller), the operator dashboard, and Resin as an optional profile. The control
plane is live-verified: a single vendor account logs in through the pinned control API and
egresses through one gate, with `AJS_NODE_LIMIT=1` keeping one active FV session per account
(the vendor's own limit — see [`research/82-live-20261023/REPORT.md`](../research/82-live-20261023/REPORT.md)).
No credential ships in this tree: everything secret lives in `.env` (gitignored).

`compose.yml` runs:

| Service | Image | Network | Endpoint | Role |
|---|---|---|---|---|
| `accountd` | built from `../daemon/Dockerfile` (`ajs-accountd:local`), or `ghcr.io/<owner>/ajs-accountd:<tag>` via `compose.release.yml` | host | `127.0.0.1:2261` | panel JSON API, JSON-snapshot persistence in the `accountd-data` volume, live node controller when `AJS_ACCOUNT_LOGIN=1` |
| `dashboard` | built from `../dashboard/Dockerfile` (`ajs-dashboard:local`), or `ghcr.io/<owner>/ajs-dashboard:<tag>` via `compose.release.yml` | bridge | `127.0.0.1:18081` | nginx serving the SPA; live mode by default, mock builds refused |
| `resin` | `ghcr.io/resinat/resin:1.3.0` | host | `127.0.0.1:2260` | Resin v1.3.0 admin API + SOCKS5 listener, only with `--profile resin` |

`accountd` and Resin use host networking because both must reach host loopback: Resin's admin API
on `127.0.0.1:2260`, and the local SOCKS5 nodes that Resin subscriptions point at. The dashboard
stays on a bridge network and publishes to host loopback only.

## Prerequisites

- Docker Engine 29 or newer with the Compose plugin (verified on HMC: Docker `29.8.2`, Compose
  `v5.6.0`). The compose file uses `env_file` `required: false` and
  `depends_on: condition: service_healthy`, so Compose v2.24+ is the practical floor.
- The repository checked out as one tree: `daemon/go.mod` replaces `ajsproxy => ../emulator`, and
  the `accountd` build context is the repository root so both modules are visible. That means the
  whole repository is sent to the builder (there is no `.dockerignore` in this tree); only
  `daemon/` and `emulator/` are copied into the image.
- Free host ports `2261`, `18081` and (with the resin profile) `2260` on loopback.
- No vendor account and no network access to a vendor host are needed for any step below.

**Port collision with `dashboard/compose.yml`:** the standalone dashboard compose file also
publishes `127.0.0.1:18081`. Run one stack or the other; before starting this one, stop the old
container:

```bash
docker compose -f ../dashboard/compose.yml down    # or: docker rm -f loveplus-dashboard
```

## Configure

```bash
cd deploy
cp .env.example .env
```

Then edit `.env`:

- `AJS_ADMIN_TOKEN` — bearer token for every `/v1/*` call. Generate one with
  `openssl rand -hex 32` (the minimum is 32 characters); the daemon refuses the
  placeholder this file ships (and `change-me`, `admin`, `test`, …) at startup
  unless `AJS_ALLOW_INSECURE=1`.
- `AJS_MASTER_KEY` — 32-byte AES-256-GCM key for account-password sealing, 64 hex characters:

  ```bash
  openssl rand -hex 32
  ```

  Without it the daemon logs `secrets_disabled` and discards account passwords. Losing the key
  makes existing sealed values in `secrets.json` unrecoverable.
- `AJS_RESIN_ADMIN_TOKEN` — leave empty to keep the Resin integration disabled. Set it equal to
  `RESIN_ADMIN_TOKEN` when using the resin profile.
- `AJS_CORS_ORIGINS` — browser origin allowed to call the API. The default is the dashboard
  published on `18081`; add `http://127.0.0.1:18082` if you also run the standalone `vite preview`
  live build.
- `AJS_ADMIN_ORIGINS` — exact browser origins allowed on every unsafe admin request
  (`POST`/`PUT`/`PATCH`/`DELETE`). Defaults to `AJS_CORS_ORIGINS`; set it explicitly when the
  admin UI is served from a different origin. Requests without `Origin` (CLI/SDK) must still
  send `X-AJS-CSRF: 1`.
- `AJS_TLS_CERT` / `AJS_TLS_KEY` / `AJS_TLS_LISTEN` — optional HTTPS admin listener. Leave them
  empty to keep the plaintext loopback listener only; see
  [TLS for the admin API](#tls-for-the-admin-api-optional-ajs-276).
- `VITE_USE_MOCK` — `false` (default) builds the dashboard in live mode against `VITE_API_BASE`;
  a bare `npm run build` without it fails closed. `true` is refused by the release image unless the
  explicit `AJS_ALLOW_MOCK_BUILD=true` local-demo opt-in is set, which builds with
  `npm run build:mock`; use `npm run dev` for the mock console.
- `VITE_PANEL_TOKEN` — deliberately not a build argument. A `VITE_*` token would be readable by
  anyone who can fetch the bundle; the image build fails if it is set.
- `RESIN_ADMIN_TOKEN` / `RESIN_PROXY_TOKEN` — Resin admin and SOCKS5 credentials, resin profile only.
- `AJS_GHCR_OWNER` / `AJS_IMAGE_TAG` — the GHCR namespace and tag `compose.release.yml` deploys
  from (`ghcr.io/<owner>/ajs-<service>:<tag>`). Defaults: `dwgx` / `latest`. Only used when the
  release override is layered on; `update.sh --tag` sets the tag for one run.

`.env` is listed in the repository `.gitignore`. `.env.example` holds placeholders only and no real
credentials belong in either file in version control.

## Release flow (GHCR)

Pushing a `v*` tag to GitHub runs two workflows. [`.github/workflows/release.yml`](../.github/workflows/release.yml)
builds both images and pushes them to GHCR; [`.github/workflows/package.yml`](../.github/workflows/package.yml)
builds `ajs-accountd-linux-amd64`, `ajs-accountd-windows-amd64.exe`, `ajs-accountd-darwin-arm64`
and `dashboard-dist.tar.gz`, checksums them, and attaches the six files (`MANIFEST.txt` and
`SHA256SUMS` included) to the GitHub Release. `release.yml` attaches no assets, so the checksum
file always covers the complete set. Nothing is hard-coded — the images land at
`ghcr.io/<repository_owner>/ajs-accountd:<tag>` and `ghcr.io/<repository_owner>/ajs-dashboard:<tag>`;
a final tag also moves `:latest`, while a pre-release tag (one containing `-`) leaves `:latest`
unchanged. The release body lists the image tags and the update command.

The release workflow builds the dashboard image with `VITE_USE_MOCK=false` and an empty
same-origin `VITE_API_BASE`; `dashboard/Dockerfile` refuses a mock build. The image is token-free:
`VITE_PANEL_TOKEN` is not a build argument and any token-bearing bundle build fails in
`vite.config.ts`.

## One-command update and rollback (`update.sh`)

`compose.release.yml` overrides only the two image fields; ports, volumes and health checks
still come from `compose.yml`. With it layered on, `docker compose up` pulls instead of building.

```bash
cd deploy
./update.sh --tag v0.1.0   # or: ./update.sh  (uses AJS_IMAGE_TAG, default latest)
```

`update.sh` sources `.env` (never printing its values), records the currently deployed image IDs
to `update-state.json`, then `pull`s and `up -d --no-build`s, and waits with bounded retries for
`/healthz` on `2261` and the dashboard on `18081` to answer `200`. On failure it exits non-zero
with the step that failed.

Every update verifies the build provenance of both images with `gh attestation verify` before any
container is switched; `--verify` is the default and `--allow-unverified` is the only opt-out
(the state file then records `"verified": false`). To check a release before deploying it, run
`./verify-release.sh --tag <tag> --require-attestation`.

Rollback restores the previous image IDs by re-tagging them over the current references:

```bash
./update.sh --rollback    # refuses if update-state.json is missing or empty
```

The state file records `updated_at`, `tag`, and the two image IDs that were deployed *before* the
update — it is written before pulling, so it exists even if the update fails midway. Each update
overwrites it, so rollback is exactly one step: after a rollback you are on the pre-update
images; run `./update.sh --tag <older>` to go further back (the GHCR tags still exist upstream).

The dashboard's **Settings → Updates** card surfaces the latest GitHub release and offers two
copy-only commands — `./verify-release.sh --tag <tag> --require-attestation` and
`./update.sh --tag <tag> --verify` — for the operator to run on the host. The card never executes
a root update: it checks the release and copies the commands, and the daemon has no endpoint that
applies an update. See
[`../dashboard/README.md`](../dashboard/README.md#update-check-settings--updates).

## Start

```bash
# accountd + live dashboard (the image refuses mock builds)
docker compose up -d --build
```

The `dashboard` service waits for `accountd` to report healthy. Both restart unless stopped.
For the deterministic mock console use `cd dashboard && npm run dev` instead of a container.

To run the released GHCR images instead of the local builds, use the release override — see
[Release flow (GHCR)](#release-flow-ghcr) and [`update.sh`](#one-command-update-and-rollback-update.sh):

```bash
docker compose -f compose.yml -f compose.release.yml up -d --no-build
```

Optional Resin:

```bash
docker compose --profile resin up -d
```

Resin binds `127.0.0.1:2260` (both the admin API and the SOCKS5 listener). The compose file sets
`RESIN_LISTEN_ADDRESS=127.0.0.1` and `RESIN_PORT=2260`, and passes the non-secret
`RESIN_PROXY_BYPASS` (target hosts dialed directly instead of through a pool node) plus the
state/cache/log paths the volumes are mounted at; override them in `.env` only if you know why.
The daemon's `AJS_RESIN_URL` must keep pointing at that address.

## Health and logs

```bash
curl -sS http://127.0.0.1:2261/healthz
# {"status":"ok","service":"ajs-accountd","version":"0.1.0",...}

curl -sS http://127.0.0.1:18081/healthz
# ok

docker compose ps
docker compose logs -f accountd
```

`/healthz` is unauthenticated by design; every `/v1/*` route requires
`Authorization: Bearer $AJS_ADMIN_TOKEN`:

```bash
curl -sS -H "Authorization: Bearer $AJS_ADMIN_TOKEN" http://127.0.0.1:2261/v1/accounts
```

The compose health check stays on the plaintext loopback listener. With TLS enabled the same
`/healthz` is also served on `AJS_TLS_LISTEN` (see below).

## TLS for the admin API (optional, AJS-276)

The daemon can serve the same admin API over HTTPS on a second listener while the plaintext one
stays on `127.0.0.1:2261` — the dashboard and the compose health check keep using it. TLS is off
unless both `AJS_TLS_CERT` and `AJS_TLS_KEY` are set; `AJS_TLS_LISTEN` defaults to
`127.0.0.1:2262`.

1. Put the certificate and key in `deploy/certs/`, which `compose.yml` mounts at `/certs`
   read-only. `*.pem` and `*.key` are gitignored, so a key dropped there cannot be committed.

   ```bash
   mkdir -p deploy/certs
   # self-signed, lab host only: one year, names for every address clients use
   openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
     -keyout deploy/certs/privkey.pem -out deploy/certs/fullchain.pem \
     -subj "/CN=panel.example.com" \
     -addext "subjectAltName=DNS:panel.example.com,IP:127.0.0.1"
   ```

   With certbot, copy the live files (the daemon reads the pair at startup, so restart
   `accountd` after a renewal) or point the compose mount at the live directory:

   ```bash
   sudo certbot certonly --standalone -d panel.example.com
   sudo install -m 0640 /etc/letsencrypt/live/panel.example.com/fullchain.pem deploy/certs/
   sudo install -m 0640 /etc/letsencrypt/live/panel.example.com/privkey.pem deploy/certs/
   ```

   The image runs as the unprivileged `ajs` user (uid/gid `100` in this Alpine base), so the
   mounted pair must be readable by it — otherwise startup fails with `permission denied` on the
   key path. `docker compose exec accountd id` prints the actual uid if that ever changes:

   ```bash
   sudo chown -R 100:100 deploy/certs
   sudo chmod 700 deploy/certs
   sudo chmod 600 deploy/certs/*.pem
   ```

2. Uncomment the TLS rows in `.env`:

   ```
   AJS_TLS_CERT=/certs/fullchain.pem
   AJS_TLS_KEY=/certs/privkey.pem
   AJS_TLS_LISTEN=0.0.0.0:2262
   ```

   `0.0.0.0:2262` publishes the listener on every interface; keep `127.0.0.1:2262` when a
   reverse proxy on the host terminates TLS instead. The firewall block in
   `nftables/ajs-panel.nft` opens only `2261` to the LAN — add `2262` there before remote clients
   rely on it.

3. Restart and check:

   ```bash
   docker compose up -d accountd
   docker compose logs accountd | grep -E 'tls_(listener_started|disabled)'
   # msg="tls_listener_started" addr=0.0.0.0:2262 min_version=1.2
   curl --cacert deploy/certs/fullchain.pem https://panel.example.com:2262/healthz
   ```

What TLS does and does not buy:

- TLS 1.2 is the floor (1.3 preferred) with ECDHE-AEAD suites only. Every response, errors
  included, carries `Strict-Transport-Security: max-age=31536000`.
- The listener serves the same handler, so bearer/scoped-key auth, CORS, rate limits and the
  request log behave exactly as on `2261`. TLS encrypts the token on the wire; it does not add
  authentication, revocation or a second factor. **The bearer token is only as safe as the
  transport it crosses** — a token pasted into a chat, a report or the dashboard bundle stays
  exposed whatever the listener speaks.
- `AJS_ALLOW_INSECURE=1` still refuses a non-loopback bind, including `AJS_TLS_LISTEN`, so the
  unauthenticated lab mode cannot be published over TLS.
- Partial configuration (one of the pair, or `AJS_TLS_LISTEN` alone) aborts startup instead of
  silently serving plaintext, and an unreadable or mismatched pair fails before the port is
  bound. The key is never logged and never enters the image or `.env`.

## Stop

```bash
docker compose down                 # keeps named volumes
docker compose --profile resin down # also stops Resin
docker compose down -v              # DESTROYS accountd-data and the Resin volumes
```

## Backup and restore

State lives in the named volume `ajs-control-plane_accountd-data` (project name is fixed by
`name: ajs-control-plane` in `compose.yml`): `state.json` (versioned JSON snapshot, rewritten every
30 s and on shutdown) plus `secrets.json` when `AJS_MASTER_KEY` is set. The daemon writes
`state.json` with mode `0600` (observed in the verification below), and `secrets.json` through the
same atomic `0600` path.

```bash
# backup (stop first so the archive matches one snapshot)
docker compose stop accountd
docker run --rm -v ajs-control-plane_accountd-data:/data -v "$PWD":/backup \
  alpine:3.22 tar -czf /backup/accountd-data.tar.gz -C /data .
docker compose start accountd

# restore
docker compose stop accountd
docker run --rm -v ajs-control-plane_accountd-data:/data -v "$PWD":/backup \
  alpine:3.22 sh -c 'rm -rf /data/* /data/.[!.]* && tar -xzf /backup/accountd-data.tar.gz -C /data'
docker compose start accountd
```

Keep the matching `AJS_MASTER_KEY` with the backup: without it the sealed envelopes in the snapshot
cannot be opened. The snapshot is not SQLite and has no migration path yet; see
[`../docs/PANEL.md`](../docs/PANEL.md) for the target schema.

## Security notes

- Everything is loopback-only: accountd and Resin bind `127.0.0.1` under host networking, and the
  dashboard publishes `127.0.0.1:18081`. Do not change these bindings without enabling the TLS
  listener ([TLS for the admin API](#tls-for-the-admin-api-optional-ajs-276)) and real
  authentication.
- The plaintext listener has no TLS and the API has a single bearer token. The optional HTTPS
  listener (`AJS_TLS_*`, see [TLS for the admin API](#tls-for-the-admin-api-optional-ajs-276))
  protects the token on the wire for remote clients; it adds no authentication, revocation or
  second factor. The dashboard image refuses a `VITE_PANEL_TOKEN` build because it would be
  readable by anyone who can fetch the bundle, so the dashboard must stay on loopback or behind a
  BFF/reverse proxy that terminates real authentication.
- `AJS_MASTER_KEY` and the tokens are the only secrets. They live in `.env` (gitignored) and in the
  container environment; never commit them, never paste them into reports.
- `accountd` reads `.env`; `resin` does not. The Resin service gets only its two credentials plus
  the non-secret knobs (`RESIN_LISTEN_ADDRESS`, `RESIN_PORT`, `RESIN_PROXY_BYPASS`,
  `RESIN_STATE_DIR`, `RESIN_CACHE_DIR`, `RESIN_LOG_DIR`) through explicit `environment:` entries,
  so the third-party image never sees `AJS_MASTER_KEY` (the AES-256 key that decrypts every
  account password) or the panel admin token.
- The named volumes contain sealed password envelopes and key hashes. Protect them like the master
  key: anyone with the volume and the key can read the sealed values.
- Docker daemon access is root-equivalent; this stack does not change that boundary.

## Firewall block (`nftables/ajs-panel.nft`)

The AJS panel's LAN ingress block is owned by this tree as `nftables/ajs-panel.nft`. It opens
`2261` (accountd admin API, bearer auth) and `18082` (dashboard preview) to `192.168.11.0/24`
only. Gate ports stay loopback-only: acc-07's gate `:13012` is not exposed by this block.

Ownership: change this file, not the host. The live `/etc/nftables.conf` is still merged by hand,
so the two can temporarily differ; `check-nftables-drift.sh` is how you find out. This file is a
fragment, not a complete nftables config: apply it by inserting the block into the `input` chain
of `table inet egress` and reloading.

**Never overwrite the live config with the ops repo copy before the block is merged.** The live
`/etc/nftables.conf` is the whole host firewall (egress sets, `wg-hmc`, docker rules, other
services); copying this fragment over it would drop all of that and lock the host down. The
rollback and hot-withdraw commands are in the file header: delete the four block lines and reload
with `nft -f /etc/nftables.conf`, or remove the live rule by handle with
`nft delete rule inet egress input handle <h>`.

Run the read-only drift check (default host `hmc`; pass another SSH host as `$1`):

```bash
sh deploy/check-nftables-drift.sh
# host:  hmc
# asset: .../deploy/nftables/ajs-panel.nft
# state: OK - live block matches (1 rule, comments ignored)
```

Exit codes: `0` match (or an accepted `KNOWN_DIVERGENCE`, printed as `OK-with-note`), `1` drift
(`MISSING`: the repo rule is absent live; `EXTRA`: live has an AJS line the repo block does not
own, including duplicates or a changed rule), `2` error (ssh, unreadable or malformed asset). The
check reads `/etc/nftables.conf`; it does not inspect the running ruleset and never reloads
nftables.

### Known divergences (`KNOWN_DIVERGENCE`)

The comparison marker is the literal `tcp dport { 2261, 18082 }`: only non-comment rule lines
containing that fragment are compared. Comments and blank lines are ignored.

`check-nftables-drift.sh` carries an explicit `KNOWN_DIVERGENCE` allowlist for accepted live-only
lines. A matching line is printed as `OK-with-note` and the check exits `0`; it is never silently
dropped, and any line not on the allowlist is still `EXTRA` and exits `1`. The allowlist is a
transition tool, not a way to accept real drift: remove an entry as soon as the host and the repo
converge.

The one current entry is the Owner-labelled HMC line:

```text
AJS-120 block pending merge (owner: APC)
```

It is the AJS-120 block hand-added to `hmc:/etc/nftables.conf:115-118` and pending merge into
`ops/hmc/nftables.conf`. Display it, do not fail on it, and delete the entry after that merge.
The live line is recognised by its trailing label:

```text
ip saddr 192.168.11.0/24 tcp dport { 2261, 18082 } accept # AJS-120 block pending merge (owner: APC)
```

Each allowlist entry is one line of the form `<reason>|<exact normalised rule>`. A live rule is
recognised when its normalised form equals the entry rule, or when the live line carries the
reason as a trailing `#` comment (for example `# KNOWN_DIVERGENCE: AJS-120 block pending merge
(owner: APC)`) or as an nft `comment "..."` attribute. To add an entry, edit the
`KNOWN_DIVERGENCE` block at the top of `deploy/check-nftables-drift.sh`, append the line, then run
the fixtures and the real check:

```bash
bash deploy/check-nftables-drift.sh --self-test
# self-test: PASS (6/6)
sh deploy/check-nftables-drift.sh
```

`--self-test` needs no SSH access: it exercises the comparison engine with fixtures for a plain
match, an owner-labelled known divergence, an unlabelled extra line, an unlabelled duplicate, a
missing rule and a malformed asset.

## Limitations

- **Live, one account, one session.** With `AJS_ACCOUNT_LOGIN=1` the daemon logs in through the
  pinned control API and runs the node controller: one loopback SOCKS5 gate per account, bounded
  by `AJS_NODE_LIMIT` (default `1`). The vendor allows exactly one active FV data session per
  account — the 2026-10-23 live run verified egress parity (`222.186.57.63` through
  `vvn-2076-264`) with the limit at `1`, and saw the second session rejected with
  `fm=disconnected by another connection` at limit `3`. Keep `1` for live use; `0` (all servers)
  is research-only. See
  [`../research/82-live-20261023/REPORT.md`](../research/82-live-20261023/REPORT.md).
- **Login is opt-in and pinned.** Nothing contacts a vendor host unless `AJS_ACCOUNT_LOGIN=1`.
  The control hosts present a self-signed `CN=localhost` certificate, so `AJS_CONTROL_CERT_SHA256`
  must pin that leaf cert or default TLS verification rejects the connection — that is how the
  live run connected. No account is supplied by this deployment: credentials enter through the
  panel API and are sealed under `AJS_MASTER_KEY`.
- **No per-node supervisor.** The node controller (`login → session factory → per-node gate`, with
  drain/undrain/delete/logout hooks) is wired; the separate `daemon/internal/supervisor` package
  remains an unused seam.
- **JSON snapshot, not SQLite.** State survives restarts as `state.json` (version 1), written every
  30 s and on shutdown. A crash loses the window since the last save, and there is no schema
  migration beyond the version check.
- **Manual reconcile.** Resin drift is corrected only by `POST /v1/reconcile`; probes are
  bookkeeping canaries, not network probes.
- **Single host, no HA.** One accountd instance and one volume; host networking means the stack
  cannot be scheduled across hosts as-is.
- **Resin is pinned** to `ghcr.io/resinat/resin:1.3.0`, the version measured in
  [`../research/77-m1-spike/REPORT.md`](../research/77-m1-spike/REPORT.md). Newer tags are untested.
- **No macOS/Windows containers or service wrappers.** This compose stack is Linux containers
  only. [`package.sh`](package.sh) produces standalone Linux, Windows and macOS binaries plus
  the dashboard bundle, but there is no Windows service, launchd unit or systemd unit; see
  [`packaging.md`](packaging.md). For the Docker-free production kit (nginx TLS, token-free
  portal bundle, admin bearer injection, systemd hardening, firewall and `check.sh`), see
  [`production/README.md`](production/README.md).
- The dashboard's live mode is a build-time choice; changing `VITE_*` in `.env` requires
  `docker compose build dashboard`.

## Verification (recorded 2026-10-23, HMC)

Environment: HMC `x86_64`, Docker `29.8.2`, Compose `v5.6.0`. The tree was copied to
`/srv/dwgxhomecloud/scratch/loveplus-deploy` (daemon, emulator, dashboard, deploy, README only; no
`node_modules`, no research payloads), so `docker build` had the sibling `emulator/` the module
replace needs.

```bash
cd /srv/dwgxhomecloud/scratch/loveplus-deploy/deploy
docker compose --env-file .env.example config          # exit 0
docker compose config --quiet                          # exit 0, after cp .env.example .env
docker compose --profile resin config --quiet          # exit 0, resin + its volumes present
```

### Release pipeline (recorded 2026-10-23, HMC, no pull/up/push)

```bash
cd deploy                                                # from the repo root
bash -n update.sh                                        # exit 0 (bash, not dash)
docker compose -f compose.yml -f compose.release.yml config >/dev/null   # exit 0
python3 -c 'import yaml; yaml.safe_load(open("../.github/workflows/release.yml"))'  # exit 0
```

The merged config resolves `ghcr.io/dwgx/ajs-accountd:latest` /
`ghcr.io/dwgx/ajs-dashboard:latest` with `.env.example`, and
`AJS_GHCR_OWNER=MyOrg AJS_IMAGE_TAG=v9.9.9` renders
`ghcr.io/MyOrg/ajs-accountd:v9.9.9`. The base `build:` blocks are dropped by the override's
`!reset` (Compose v2.24+), ports/volumes/health checks come from `compose.yml`, and the resin
profile is untouched. `update.sh` was exercised end-to-end on HMC against a fake `docker`
shim (no pull, no up): update records the previous image IDs to `update-state.json`, calls
`compose pull` + `up -d --no-build`, and waits on `/healthz`; rollback re-tags the recorded IDs
over the current refs and refuses when the state file is missing or holds `none`. The
`--rollback`/`--tag`/bad-arg guards exit 1/2/2 as documented.

`docker compose --env-file .env.example config` exits 0 on a checkout with no `.env`: the service
`env_file` entries are `required: false`, and all build args carry defaults. With `.env` present,
the rendered accountd environment includes `AJS_ADMIN_TOKEN`, `AJS_MASTER_KEY`, `AJS_RESIN_URL`,
`AJS_CORS_ORIGINS`, `AJS_RESIN_ADMIN_TOKEN` and the Resin tokens from that file. The rendered
config shows `AJS_LISTEN: 127.0.0.1:2261`, `network_mode: host`, the `accountd-data` volume,
dashboard `ports: 127.0.0.1:18081:80`, and the three Resin volumes under the `resin` profile. The
rendered resin environment holds only `RESIN_LISTEN_ADDRESS`, `RESIN_PORT`,
`RESIN_PROXY_BYPASS`, `RESIN_STATE_DIR`, `RESIN_CACHE_DIR`, `RESIN_LOG_DIR`, `RESIN_ADMIN_TOKEN`
and `RESIN_PROXY_TOKEN`: no `AJS_*` key reaches that container.

```bash
cd /srv/dwgxhomecloud/scratch/loveplus-deploy
docker build -f daemon/Dockerfile -t ajs-accountd:test .        # exit 0
docker build -f dashboard/Dockerfile -t ajs-dashboard:test .    # exit 0, live default
docker build -f dashboard/Dockerfile \
  --build-arg VITE_USE_MOCK=false \
  --build-arg VITE_API_BASE=http://127.0.0.1:2261 \
  -t ajs-dashboard:test-live .                                  # exit 0
cd deploy
docker compose --env-file .env.example build                    # exit 0, both images
```

Image tags and IDs from `docker images`:

| Tag | Image ID |
|---|---|
| `ajs-accountd:test` | `8d12988ea8bf` |
| `ajs-accountd:local` | `3f674c018f79` |
| `ajs-dashboard:test` | `b4f24850b34b` |
| `ajs-dashboard:test-live` | `20eec80bc7ed` |
| `ajs-dashboard:local` | `345aad678ad7` |

The dashboard build fails if `VITE_PANEL_TOKEN` is set: a Vite `VITE_*` value is compiled into
public JavaScript, and the production image is token-free. A mock build (`VITE_USE_MOCK=true`)
is refused for the same reason unless the explicit `AJS_ALLOW_MOCK_BUILD=true` local-demo opt-in
is set, which builds through `npm run build:mock` — the mock console is a development mode, not a
release image.

Accountd image metadata (`docker image inspect ajs-accountd:test`):

```text
user=ajs entrypoint=["/usr/local/bin/ajs-accountd"]
env=["AJS_LISTEN=127.0.0.1:2261","AJS_STATE_DIR=/data"] volumes={"/data":{}}
```

Accountd container smoke test (`--network host`, named volume, random 64-hex `AJS_MASTER_KEY`;
`AJS_LISTEN` was moved to `127.0.0.1:12261` only because an unrelated leftover `ajs-accountd`
process from another scratch tree already held `127.0.0.1:2261` on the host at the time — the image
and compose defaults are `127.0.0.1:2261`):

```text
healthz_status=200
{"status":"ok","service":"ajs-accountd","version":"0.1.0","time":"2026-10-23T04:53:34.614941779Z","accounts":{"active":4,"auth_failed":1,"expired":0,"paused":1}}
uid=100 user=ajs
drwxr-xr-x    2 ajs      ajs           4096 Oct 23 04:52 /data
unauth_api=401
auth_api=200
health_status=healthy
```

Shutdown log and volume contents after `docker stop`:

```text
time=2026-10-23T04:53:40.409Z level=INFO msg="ajs-accountd shutting down"
time=2026-10-23T04:53:40.411Z level=INFO msg=state_saved path=/data/state.json reason=shutdown
-rw-------    1 100      101          36556 Oct 23 04:53 state.json
{"version": 1, "accounts": [...]
```

The image runs as uid `100` (`ajs`), owns `/data`, and the named volume receives the snapshot with
mode `0600`. The container and volume were removed after the check.

Dashboard image smoke test (nginx on host `127.0.0.1:18085`, since `18081` is held by the
standalone `loveplus-dashboard` container on HMC):

```text
dashboard_healthz=200 body=ok
dashboard_root=200
dashboard_spa_fallback=200
```

Build-mode guard check (re-verified 2026-10-27): `VITE_PANEL_TOKEN=local-test-token npm run build`
exits 1 before Vite starts, and a bare `npm run build` (no `VITE_USE_MOCK=false`) also exits 1
without emitting `dist/`. The demo recipe is `npm run build:mock` (`ajs-build.json` with
`mode: mock`); a live build (`VITE_USE_MOCK=false`) emits `mode: live`, `mock: false`,
`panelToken: false`. (The literal `127.0.0.1:2261` exists in both because the UI prints it as
static text in `Sidebar.tsx` and `Settings.tsx`.)

The compose `dashboard` service itself was built but not started, because the standalone dashboard
container owns `127.0.0.1:18081` on HMC; see the port-collision note above. The compose `accountd`
service was likewise not started for the same reason (`127.0.0.1:2261` held by the leftover
process); its image was run directly with the same environment and volume.
