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
| `dashboard` | built from `../dashboard/Dockerfile` (`ajs-dashboard:local`), or `ghcr.io/<owner>/ajs-dashboard:<tag>` via `compose.release.yml` | bridge | `127.0.0.1:18081` | nginx serving the SPA; mock data by default, live mode opt-in at build time |
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

- `AJS_ADMIN_TOKEN` — bearer token for every `/v1/*` call. Replace `change-me`.
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
- `VITE_USE_MOCK` — `true` (default) serves deterministic in-browser mock data; `false` builds the
  dashboard in live mode against `VITE_API_BASE`.
- `VITE_PANEL_TOKEN` — the token the dashboard sends to the daemon in live mode. It is baked into
  the JavaScript bundle at image build time, so treat it as public within loopback.
- `RESIN_ADMIN_TOKEN` / `RESIN_PROXY_TOKEN` — Resin admin and SOCKS5 credentials, resin profile only.
- `AJS_GHCR_OWNER` / `AJS_IMAGE_TAG` — the GHCR namespace and tag `compose.release.yml` deploys
  from (`ghcr.io/<owner>/ajs-<service>:<tag>`). Defaults: `dwgx` / `latest`. Only used when the
  release override is layered on; `update.sh --tag` sets the tag for one run.

`.env` is listed in the repository `.gitignore`. `.env.example` holds placeholders only and no real
credentials belong in either file in version control.

## Release flow (GHCR)

Pushing a `v*` tag to GitHub runs [`.github/workflows/release.yml`](../.github/workflows/release.yml):
it builds both images, pushes them to GHCR, and attaches the `ajs-accountd-linux-amd64` binary
and `dashboard-dist.tar.gz` to a GitHub Release. Nothing is hard-coded — the images land at
`ghcr.io/<repository_owner>/ajs-accountd:<tag>` and `ghcr.io/<repository_owner>/ajs-dashboard:<tag>`
(plus `:latest`), and the release body lists the image tags and the update command.

The release images use the same `VITE_*` build defaults as the local build, so the dashboard
image ships in **mock mode** unless the workflow is edited to pass live args. A locally built
`ajs-dashboard:local` (live mode, from `docker compose build dashboard`) remains the way to run
the dashboard against the real API.

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

Rollback restores the previous image IDs by re-tagging them over the current references:

```bash
./update.sh --rollback    # refuses if update-state.json is missing or empty
```

The state file records `updated_at`, `tag`, and the two image IDs that were deployed *before* the
update — it is written before pulling, so it exists even if the update fails midway. Each update
overwrites it, so rollback is exactly one step: after a rollback you are on the pre-update
images; run `./update.sh --tag <older>` to go further back (the GHCR tags still exist upstream).

The dashboard's **Settings → Updates** card surfaces the latest GitHub release and this exact
command (read-only check; the daemon never updates itself). See
[`../dashboard/README.md`](../dashboard/README.md#update-check-settings--updates).

## Start

```bash
# accountd + dashboard, mock dashboard (the default)
docker compose up -d --build

# or the live dashboard: rebuild is required because VITE_* is baked in
sed -i 's/^VITE_USE_MOCK=.*/VITE_USE_MOCK=false/' .env
docker compose build dashboard && docker compose up -d dashboard
```

The `dashboard` service waits for `accountd` to report healthy. Both restart unless stopped.

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
`RESIN_LISTEN_ADDRESS=127.0.0.1` and `RESIN_PORT=2260`; override them in `.env` only if you know
why. The daemon's `AJS_RESIN_URL` must keep pointing at that address.

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
  dashboard publishes `127.0.0.1:18081`. Do not change these bindings without adding TLS and real
  authentication.
- The daemon has no TLS and a single bearer token. `VITE_PANEL_TOKEN` is readable by anyone who can
  fetch the dashboard bundle, so the dashboard must stay on loopback or behind a BFF/reverse proxy
  that terminates real authentication.
- `AJS_MASTER_KEY` and the tokens are the only secrets. They live in `.env` (gitignored) and in the
  container environment; never commit them, never paste them into reports.
- Both `accountd` and `resin` read the same `.env`, so each container's environment contains every
  key in the file. That is acceptable for a single-host loopback stack; split the file if you ever
  move a service off the host.
- The named volumes contain sealed password envelopes and key hashes. Protect them like the master
  key: anyone with the volume and the key can read the sealed values.
- Docker daemon access is root-equivalent; this stack does not change that boundary.

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
- **No macOS/Windows packaging.** Only Linux containers are provided.
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
sh -n update.sh                                          # exit 0
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
dashboard `ports: 127.0.0.1:18081:80`, and the three Resin volumes under the `resin` profile.

```bash
cd /srv/dwgxhomecloud/scratch/loveplus-deploy
docker build -f daemon/Dockerfile -t ajs-accountd:test .        # exit 0
cd dashboard
docker build -t ajs-dashboard:test .                            # exit 0, default (mock) args
docker build --build-arg VITE_USE_MOCK=false \
  --build-arg VITE_API_BASE=http://127.0.0.1:2261 \
  --build-arg VITE_PANEL_TOKEN=local-test-token \
  -t ajs-dashboard:test-live .                                  # exit 0
cd ../deploy
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

The dashboard build prints BuildKit `SecretsUsedInArgOrEnv` warnings for `VITE_PANEL_TOKEN`: that
is the point of a Vite build — the value is compiled into public JavaScript. It is a loopback
token, not a secret.

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

Live-mode argument check: `local-test-token` appears in the live bundle's JS assets and not in the
mock bundle. (The literal `127.0.0.1:2261` exists in both because the UI prints it as static text
in `Sidebar.tsx` and `Settings.tsx`.)

The compose `dashboard` service itself was built but not started, because the standalone dashboard
container owns `127.0.0.1:18081` on HMC; see the port-collision note above. The compose `accountd`
service was likewise not started for the same reason (`127.0.0.1:2261` held by the leftover
process); its image was run directly with the same environment and volume.
