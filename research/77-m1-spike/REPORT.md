# M1 spike — Resin v1.3.0 against loopback SOCKS5 nodes

Date: 2026-10-23 · Slice: AJS-22 · Host: HMC (`ssh hmc`) · No vendor account, no AJiaSu call.

## Verdict

**PASS.** Resin v1.3.0 dials loopback SOCKS5 nodes, accepts a local subscription created and
PATCHed through the admin API, and issues `(Platform, Account)` leases anchored on the observed
egress IP; repeated requests reuse the lease, a killed node is detected and the lease is
re-selected automatically, and all state (subscriptions, platforms, leases, runtime config)
survives a Resin restart. The gate is green with two mandatory operational conditions found by
the spike: (1) a restarted node does **not** become routable on its own within a probe cycle
because the default per-node latency interval is 1 h — set `max_latency_test_interval` to 30 s
(or remove/re-add the line), which reduced recovery to 6.1 s; (2) deleting a lease alone does not
guarantee a different IP — with `BALANCED` and one faster node, five consecutive delete/request
cycles re-picked the same node, so rotation must use the Platform filter-exclusion path, which
moved the lease to the other IP in 0.3 s. Client-visible downtime on a node kill was 3 failed
requests (curl rc 97) over 6.0 s and 12.6 s from kill to the first successful re-routed request.

## Environment

| Item | Value |
|---|---|
| Host | HMC, Debian 13, kernel `6.12.111+deb13-amd64`, x86_64 |
| Docker | 29.8.2, build `7fc2dff` |
| Go (HMC) | `go1.25.14 linux/amd64` |
| Resin image | `ghcr.io/resinat/resin:1.3.0` |
| Image digest | `sha256:9aa94af706bfa7bc80c8fd78ed562f9a00d4e35e8323728cd17f06af43e665bf` (created 2026-10-21T16:30:43Z) |
| Resin build | API `system/info`: `{"version":"v1.3.0","git_commit":"5473c7ee","build_time":"2026-10-21T16:29:14Z"}` |
| Container | `resin-m1`, `--network host`, `RESIN_LISTEN_ADDRESS=127.0.0.1`, `RESIN_PORT=2260` |
| State | `/srv/dwgxhomecloud/scratch/m1-spike/data/{state,cache,log}` (kept after teardown) |
| Tokens | `openssl rand -hex 32`, written to `/srv/dwgxhomecloud/scratch/m1-spike/.env`, mode `600`, never in the repo/report |
| Harness | `scripts/m1/dummy-socks` (Go stdlib, no external modules) |
| Node `m1-a` | `127.0.0.1:12001`, `--mode direct`, egress `152.175.35.40` (JP, NRT) |
| Node `m1-b` | `127.0.0.1:12002`, `--mode upstream --upstream 127.0.0.1:2081`, egress `58.13.71.114` (JP, KIX) |
| Resin probes | egress `https://cloudflare.com/cdn-cgi/trace`, latency `https://www.gstatic.com/generate_204` |
| Subscription | `0a8d92e5-2151-4e1f-a4d0-e57a3c516985` (`ajs-m1`) |
| Platform | `95a9ee60-3695-43b3-8143-2532441fc32b` (`m1`, `sticky_ttl=1h`, `regex_filters=["ajs-m1/.*"]`, `BALANCED`) |
| Node hashes | `m1-a` `55aba6110fddf4d6b8c04d631af30e0c`; `m1-b` `d1d53a073970573cd7c7b1dc46825d0e` |

**Deviations from the brief**

1. `ghcr.io/resinat/resin:v1.3.0` does not exist (`not found`); GHCR publishes `:1.3.0` and
   `:latest`. The spike used `:1.3.0` and recorded the digest. The native release tarball
   `resin-linux-amd64.tar.gz` was downloaded as a fallback (sha256
   `decc440a0b3f9bcd580061dc82f2b436fd70f02b3c580c6f4fc1d68173c7eec8`) but was not run.
2. `127.0.0.1:2080` and the host default route both exit `152.175.35.40`, so the two nodes would
   have shared one egress IP. HMC's sing-box exposes a second inbound on `127.0.0.1:2081`
   (`homelane` → `homelane-vless`) that exits `58.13.71.114`; `m1-b` uses 2081. Both nodes are
   still loopback SOCKS5 nodes reached through Resin's own loopback dial.
3. A manual `POST .../actions/refresh` was issued between the empty poll and the first node
   sighting, so the 6.9 s onboarding figure is an upper bound, not a clean PATCH-only measurement.

## Commands

Secrets are redacted as `$RESIN_ADMIN_TOKEN` / `$RESIN_PROXY_TOKEN`. All commands ran on HMC from
`/srv/dwgxhomecloud/scratch/m1-spike` unless noted.

```sh
# Harness build and test (also passes locally with go1.26.7 windows/amd64)
cd scripts/m1/dummy-socks && go vet ./... && go test ./... && go build -o dummy-socks ./...
./dummy-socks --self-test

# Nodes
./dummy-socks --listen 127.0.0.1:12001 --mode direct --name m1-a
./dummy-socks --listen 127.0.0.1:12002 --mode upstream --upstream 127.0.0.1:2081 --name m1-b

# Egress check before Resin
curl -sS --socks5-hostname 127.0.0.1:12001 https://cloudflare.com/cdn-cgi/trace | grep '^ip='
curl -sS --socks5-hostname 127.0.0.1:12002 https://cloudflare.com/cdn-cgi/trace | grep '^ip='

# Resin (host network so 127.0.0.1 nodes resolve to the host)
docker run -d --name resin-m1 --network host \
  --env-file /srv/dwgxhomecloud/scratch/m1-spike/.env \
  -v /srv/dwgxhomecloud/scratch/m1-spike/data/state:/var/lib/resin \
  -v /srv/dwgxhomecloud/scratch/m1-spike/data/cache:/var/cache/resin \
  -v /srv/dwgxhomecloud/scratch/m1-spike/data/log:/var/log/resin \
  ghcr.io/resinat/resin:1.3.0

# Subscription: create, then PATCH the same content
curl -X POST -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"ajs-m1","source_type":"local","content":"socks5://127.0.0.1:12001#m1-a\nsocks5://127.0.0.1:12002#m1-b\n","update_interval":"30s","ephemeral":true}' \
  http://127.0.0.1:2260/api/v1/subscriptions
curl -X PATCH -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"content":"socks5://127.0.0.1:12001#m1-a\nsocks5://127.0.0.1:12002#m1-b\n"}' \
  http://127.0.0.1:2260/api/v1/subscriptions/0a8d92e5-2151-4e1f-a4d0-e57a3c516985

# Platform
curl -X POST -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"m1","sticky_ttl":"1h","regex_filters":["ajs-m1/.*"],"allocation_policy":"BALANCED"}' \
  http://127.0.0.1:2260/api/v1/platforms

# Requests through Resin's SOCKS5 entry: username Platform.Account, password proxy token
curl -sS --socks5-hostname 127.0.0.1:2260 --proxy-user "m1.acct1:$RESIN_PROXY_TOKEN" \
  https://cloudflare.com/cdn-cgi/trace

# Lease inspect / delete
curl -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  http://127.0.0.1:2260/api/v1/platforms/95a9ee60-3695-43b3-8143-2532441fc32b/leases/acct1
curl -X DELETE -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  http://127.0.0.1:2260/api/v1/platforms/95a9ee60-3695-43b3-8143-2532441fc32b/leases/acct1

# Rotation by exclusion (the reliable path)
curl -X PATCH -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"regex_filters":["ajs-m1/m1-b"]}' http://127.0.0.1:2260/api/v1/platforms/95a9ee60-3695-43b3-8143-2532441fc32b

# Node kill / restart, then force a re-probe
kill -9 <m1-b pid>
./dummy-socks --listen 127.0.0.1:12002 --mode upstream --upstream 127.0.0.1:2081 --name m1-b
curl -X PATCH -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"max_latency_test_interval":"30s"}' http://127.0.0.1:2260/api/v1/system/config

# Restart persistence
docker restart resin-m1

# Teardown (logs kept)
docker stop resin-m1 && docker rm resin-m1
pkill -x dummy-socks
```

## Evidence

### 1. Harness self-test (HMC)

```
$ go test ./...
ok  	dummy-socks	0.004s
$ ./dummy-socks --self-test
PASS mode=direct proxy=127.0.0.1:33989 target=127.0.0.1:36153 rtt_ms=0
PASS mode=upstream proxy=127.0.0.1:37001 target=127.0.0.1:36153 rtt_ms=0
PASS mode=upstream-domain proxy=127.0.0.1:37001 target=localhost:36153 rtt_ms=0
SELFTEST PASS
```

### 2. Two distinct egress IPs before Resin

```
== 12001 (direct)    ip=152.175.35.40  colo=NRT  loc=JP
== 12002 (upstream)  ip=58.13.71.114   colo=KIX  loc=JP
```

Host default route and `--socks5-hostname 127.0.0.1:2080` both returned `ip=152.175.35.40`, which
is why `m1-b` uses the second sing-box inbound on 2081.

### 3. Subscription create + PATCH

Create at `11:57:08.364+09:00`, HTTP 201 in 0.63 ms:

```json
{"id":"0a8d92e5-2151-4e1f-a4d0-e57a3c516985","name":"ajs-m1","source_type":"local","url":"","content":"socks5://127.0.0.1:12001#m1-a\nsocks5://127.0.0.1:12002#m1-b\n","update_interval":"30s","node_count":0,"healthy_node_count":0,"ephemeral":true,"incremental_alive_nodes":false,"ephemeral_node_evict_delay":"72h0m0s","enabled":true,"created_at":"2026-10-23T02:57:08.369654566Z"}
```

PATCH at `11:57:12.502+09:00`, HTTP 200 in 0.50 ms, same content echoed back. At `11:57:15.759`
the node list was still empty; at `11:57:19.419` both nodes existed with egress IPs already
resolved:

| Node | Hash | Tag | Egress IP | Region | Egress probed at |
|---|---|---|---|---|---|
| m1-a | `55aba6110fddf4d6b8c04d631af30e0c` | `ajs-m1/m1-a` | `152.175.35.40` | `jp` | `11:57:19.478` |
| m1-b | `d1d53a073970573cd7c7b1dc46825d0e` | `ajs-m1/m1-b` | `58.13.71.114` | `jp` | `11:57:19.516` |

Onboarding: **≤6.9 s** from PATCH to node creation, both routable with distinct
`unique_egress_ips=2`; `healthy_node_count=2` at `11:57:19.419`. The 15–30 s estimate is an upper
bound, not a floor.

### 4. Platform

Created `11:57:35.340+09:00`, HTTP 201 in 0.59 ms:

```json
{"id":"95a9ee60-3695-43b3-8143-2532441fc32b","name":"m1","sticky_ttl":"1h0m0s","regex_filters":["ajs-m1/.*"],"region_filters":null,"routable_node_count":2,"reverse_proxy_miss_action":"TREAT_AS_EMPTY","reverse_proxy_empty_account_behavior":"ACCOUNT_HEADER_RULE","reverse_proxy_fixed_account_header":"Authorization","allocation_policy":"BALANCED","passive_circuit_breaker_disabled":false,"updated_at":"2026-10-23T02:57:35.345619893Z"}
```

### 5. First requests and sticky lease

Request 1 at `11:57:41.020`: `ip=152.175.35.40`, `colo=NRT`, HTTP 200, 64.5 ms.
Request 2 at `11:57:41.092`: `ip=152.175.35.40`, HTTP 200, 66.1 ms.

```json
{"platform_id":"95a9ee60-3695-43b3-8143-2532441fc32b","account":"acct1","node_hash":"55aba6110fddf4d6b8c04d631af30e0c","node_tag":"ajs-m1/m1-a","egress_ip":"152.175.35.40","expiry":"2026-10-23T03:57:41.025157657Z","last_accessed":"2026-10-23T02:57:41.097167445Z"}
```

Both requests used node `m1-a` and IP `152.175.35.40`; `last_accessed` moved with request 2 while
`expiry` stayed at creation + 1 h (fixed TTL, not sliding). Request log record for request 1
(`GET /api/v1/request-logs`): `node_tag=ajs-m1/m1-a`, `egress_ip=152.175.35.40`, `net_ok=true`,
`ingress_bytes=5254`, `egress_bytes=1893`, `duration_ms=55`.

### 6. Lease delete / rotate

`DELETE` at `11:57:48.680` returned **204**; `GET` immediately after returned **404**
`{"error":{"code":"NOT_FOUND","message":"lease not found"}}`. The next request at `11:57:48.692`
re-created the lease on the **same** node and IP.

Five consecutive delete + request cycles (`11:57:48.68`, `11:57:55.14`, `.238`, `.321`, `.404`)
all re-picked `ajs-m1/m1-a` / `152.175.35.40`. Both nodes reported `reference_latency_ms=42`; with
`BALANCED` the score is `(lease_count+1) × latency`, so the faster node wins the tie consistently.
**Delete alone forces a new selection but not a new IP.**

The documented exclusion path works synchronously:

| Step | Time | Result |
|---|---|---|
| PATCH `regex_filters=["ajs-m1/m1-b"]` | `11:58:18.903` | 200, `routable_node_count=1` |
| DELETE lease | `11:58:19.2xx` | 204 |
| Request | `11:58:19.223` | `ip=58.13.71.114`, `colo=KIX`, HTTP 200 |
| New lease | `11:58:19.2xx` | `{"node_tag":"ajs-m1/m1-b","egress_ip":"58.13.71.114"}` |
| PATCH back to `["ajs-m1/.*"]` | `11:58:19.475` | 200, `routable_node_count=2` |

### 7. Node kill and automatic re-selection

`m1-b` (the leased node) was killed at `11:58:28.845`. With the node gone, requests were routed to
the still-leased dead node and failed:

| Request time | curl rc | HTTP | Resin error | Upstream message |
|---|---|---|---|---|
| `11:58:35.429` | 97 | 000 | `UPSTREAM_CONNECT_FAILED` | `dial tcp 127.0.0.1:12002: connect: connection refused` |
| `11:58:37.439` | 97 | 000 | `UPSTREAM_CONNECT_FAILED` | same |
| `11:58:39.449` | 97 | 000 | `UPSTREAM_CONNECT_FAILED` | same |

The circuit opened at `11:58:39.450` (`max_consecutive_failures=3`, `failure_count=3`). The next
request at `11:58:41.459` succeeded via `m1-a` (`ip=152.175.35.40`, HTTP 200) and the lease was
replaced:

```json
{"platform_id":"95a9ee60-3695-43b3-8143-2532441fc32b","account":"acct1","node_hash":"55aba6110fddf4d6b8c04d631af30e0c","node_tag":"ajs-m1/m1-a","egress_ip":"152.175.35.40","expiry":"2026-10-23T03:58:41.459936355Z","last_accessed":"2026-10-23T02:58:49.749425468Z"}
```

Kill → first successful re-routed request: **12.6 s**; circuit open → success: **2.0 s**;
client-visible failures: **3 requests over 6.0 s**. Resin does not retry a different node inside a
single request; the caller must retry.

### 8. Recovery after node restart

`m1-b` was restarted at `11:59:00.332` on the same port. It stayed `circuit_open_since =
02:58:39.450` with `failure_count=3` and `last_latency_probe_attempt=02:58:39.449`: the default
`max_latency_test_interval` is `1h`, so no probe was scheduled until `12:58:39` and the circuit
never cleared. Hot-patching the interval at `12:01:24.524`:

```json
PATCH /api/v1/system/config {"max_latency_test_interval":"30s"}  -> 200
poll 12:01:24.546 m1b_open=2026-10-23T02:58:39.450007907Z
poll 12:01:30.608 m1b_open=null
RECOVERED_AT=2026-10-23T12:01:30.624   (6.1 s after the PATCH)
```

The restarted node then served traffic: filter forced to `m1-b`, lease deleted, request at
`12:01:36.298` returned `ip=58.13.71.114`, `colo=KIX`, and the lease was re-created on
`ajs-m1/m1-b`. The node log confirms the new process handled it:
`12:01:36.343 event=connect node=m1-b target=cloudflare.com:443 ok=true`.

### 9. Resin restart persistence

`docker restart resin-m1` at `12:01:58.352`; `/healthz` 200 at `12:01:59.026` (**0.67 s**). Log:

```
Loaded persisted runtime config (version 1)
Loaded 1 subscriptions from state.db
Loaded 2 platforms from state.db
Loaded 2 static nodes from cache.db
Loaded 2 subscription-node bindings from cache.db
Loaded 2 node dynamic states from cache.db
Loaded 4 latency entries from cache.db (trimmed=0)
```

Subscription `ajs-m1` (2 nodes, 2 healthy), platform `m1` (`routable_node_count=2`,
`regex_filters=["ajs-m1/.*"]`), the `30s` interval, and the lease all came back. The lease kept the
same node, IP and expiry (`2026-10-23T04:01:36.312378235Z`). A request at `12:02:05.055` returned
`ip=58.13.71.114`, HTTP 200, 97 ms — the persisted lease was used directly.

### Timeline summary

| Event | Local time (+09:00) | Delta |
|---|---|---|
| Subscription POST | 11:57:08.364 | 0.63 ms API |
| Subscription PATCH | 11:57:12.502 | 0.50 ms API |
| Nodes created + egress probed | 11:57:19.419 / .478 | ≤6.9 s after PATCH |
| Platform created (2 routable) | 11:57:35.340 | 0.59 ms API |
| First proxied request | 11:57:41.020 | 64.5 ms, `152.175.35.40` |
| Lease delete → same node re-picked | 11:57:48.680 | 204 → 404 → same lease |
| Filter exclusion → other IP | 11:58:18.903 | 0.3 s to `routable=1` |
| Node kill | 11:58:28.845 | — |
| Circuit open | 11:58:39.450 | +10.6 s, 3 failures |
| First success on healthy node | 11:58:41.459 | +12.6 s |
| Node restart | 11:59:00.332 | +31.5 s |
| Interval patch → circuit closed | 12:01:24.524 → 12:01:30.624 | +6.1 s |
| Resin restart → healthy | 12:01:58.352 → 12:01:59.026 | +0.67 s |
| Post-restart request | 12:02:05.055 | 97 ms, persisted lease |

## Failures

1. **`ghcr.io/resinat/resin:v1.3.0` not found.** Exact error:
   `failed to resolve reference "ghcr.io/resinat/resin:v1.3.0": ghcr.io/resinat/resin:v1.3.0: not found`.
   `:1.3.0` and `:latest` pull; `:1.3.0` was pinned and its digest recorded. Not a Resin defect —
   the brief's tag does not exist.
2. **Two nodes could not have distinct egress IPs via 2080.** Host default route and
   `127.0.0.1:2080` both exit `152.175.35.40`. The second node was pointed at the other sing-box
   inbound, `127.0.0.1:2081`, which exits `58.13.71.114`. No result was faked.
3. **Three failed client requests during the node kill** (`curl` rc 97, HTTP 000,
   `UPSTREAM_CONNECT_FAILED`, `connection refused`). Resin keeps using the leased dead node until
   `max_consecutive_failures` (3) trips the circuit; it does not retry another node within the same
   request. Mitigation is caller retry or `max_consecutive_failures=1`.
4. **A restarted node does not recover under default config.** `max_latency_test_interval=1h`
   means the circuit stays open up to an hour after the process returns; `last_latency_probe_attempt`
   stayed at the kill timestamp for 2.5 min. Recovery required the hot-patch to `30s` (6.1 s to
   close). This is the most important finding for `ajs-accountd`.
5. **Delete-lease rotation is not deterministic.** Five cycles re-picked the same node; the
   filter-exclusion path is required to change the egress IP. This matches `docs/PANEL.md` §8
   (`avoid_current_ip`), but the spike proves delete-only rotation is insufficient.
6. **Test-harness mishaps (non-blocking).** The first background start used a `cd && … &` chain
   that scoped only the first node, so `m1-b` did not start; restarted with absolute paths. The
   first kill command used `pgrep -f` with a pattern that also matched the SSH wrapper, so the
   wrapper was killed along with the node (exit 137); the kill succeeded and the failure probes
   were re-run from a fresh command. Neither affects the integration evidence.

## Decision

**Proceed with Resin v1.3.0 / hybrid D.** The primitives the architecture depends on are proven
end to end: loopback dial, local subscription create/PATCH, IP-anchored sticky lease with reuse,
automatic re-selection when a node's circuit opens, and full state persistence across a Resin
restart. No custom control plane is needed for these; the gateway/console work in D is unchanged.

Mandatory conditions for `ajs-accountd` (each has a measured basis above):

1. On startup, PATCH `max_latency_test_interval` to 30–60 s (and consider
   `max_consecutive_failures=1..2`); otherwise a restarted node is unroutable for up to 1 h.
2. Rotation must use the Platform filter exclusion (`avoid_current_ip`), not lease deletion alone;
   restore the filter after the new lease is observed.
3. Treat the kill window as 3 failed requests / ~12.6 s and retry idempotent requests at the gate.
4. Watch `circuit_open_since`; when it appears for a leased node, delete the lease to force
   re-selection instead of waiting for the probe cycle.

Trigger fallback C only if a later test shows Resin refusing loopback dials, publish churn
outrunning the 5 s coalescing, or the lease primitive failing under multi-account load — none of
which occurred here. The bus-factor/security-advisory triggers in `research/73-build-vs-reuse/DECISION.md`
still stand.

## Next

Concrete next action for `ajs-accountd` (M2):

- Implement the publisher exactly as spiked: coalesced `PATCH /api/v1/subscriptions/{id}` with
  `socks5://127.0.0.1:<gate>#<tag>` lines, then `POST .../actions/refresh` as the sync point.
- Add a startup reconciler that PATCHes `/api/v1/system/config` with
  `max_latency_test_interval=30s` and `max_consecutive_failures=2`, and records the applied values
  in `/v1/topology`.
- Implement rotation as: PATCH platform `regex_filters` to exclude the current tag → wait for
  `routable_node_count` to drop → DELETE lease → issue a canary request → restore filters. Rate
  limit to one per user per 10 s as designed.
- Subscribe to `GET /api/v1/nodes` on a 2 s poll; on `circuit_open_since != null` for a leased
  node, delete affected leases immediately and mark the node `DOWN` for the supervisor. On process
  restart, remove and re-add the node line (or rely on the 30 s interval) before marking `LIVE`.
- Gate: retry idempotent flows once on `UPSTREAM_CONNECT_FAILED`; expose the 3-failure window as a
  metric so the 12.6 s recovery is visible.
