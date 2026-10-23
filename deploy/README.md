# Control-plane deployment (`deploy/`)

A reproducible Docker Compose stack for the parts of this project that exist today:
`ajs-accountd` (the panel API) and the operator dashboard, with Resin as an optional profile.
It is a **control plane**, not a working proxy: the gate and `ajsproxy` per-node supervision are
not wired into node lifecycle, no vendor host is contacted, and no credential ships in this tree.

`compose.yml` runs:

| Service | Image | Network | Endpoint | Role |
|---|---|---|---|---|
| `accountd` | built from `../daemon/Dockerfile` (`ajs-accountd:local`) | host | `127.0.0.1:2261` | panel JSON API, JSON-snapshot persistence in the `accountd-data` volume |
| `dashboard` | built from `../dashboard/Dockerfile` (`ajs-dashboard:local`) | bridge | `127.0.0.1:18081` | nginx serving the SPA; mock data by default, live mode opt-in at build time |
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

`.env` is listed in the repository `.gitignore`. `.env.example` holds placeholders only and no real
credentials belong in either file in version control.

## Start

```bash
# accountd + dashboard, mock dashboard (the default)
docker compose up -d --build

# or the live dashboard: rebuild is required because VITE_* is baked in
sed -i 's/^VITE_USE_MOCK=.*/VITE_USE_MOCK=false/' .env
docker compose build dashboard && docker compose up -d dashboard
```

The `dashboard` service waits for `accountd` to report healthy. Both restart unless stopped.

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

- **Control plane only.** The gate (`daemon/internal/gate`) and per-node supervisor
  (`daemon/internal/supervisor`) are not wired into node lifecycle. Nothing in this stack proxies
  traffic; Resin leases exist but no node process is started for them.
- **No live vendor.** The login path is behind `AJS_ACCOUNT_LOGIN=1` and has only run against local
  fakes. No vendor host is contacted by this deployment, and no account is supplied here.
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
