# Resin v1.3.0 audit — evidence

Date: 2026-10-23. All commands run against the clone at
`C:/Users/dwgx1/AppData/Local/Temp/ajs-resin-audit-20261023/resin`.
Clone identity:

```
$ git clone --depth 1 --branch v1.3.0 https://github.com/Resinat/Resin.git .
$ git describe --tags
v1.3.0
$ git log --oneline -1
5473c7e docs: add v1.3.0 release notes
```

No Resin process was started; no request was sent to AJiaSu or any vendor service.

## 1. License

```
$ head -3 LICENSE
MIT License

Copyright (c) 2026 Resinat and contributors
```

Source: `LICENSE:1`, `LICENSE:3`.

## 2. Local subscription `content` + `PATCH`

```
$ grep -n "subscriptions" internal/api/server.go
103:  authed.Handle("GET /api/v1/subscriptions", ...)
104:  authed.Handle("POST /api/v1/subscriptions", ...)
105:  authed.Handle("GET /api/v1/subscriptions/{id}", ...)
106:  authed.Handle("PATCH /api/v1/subscriptions/{id}", HandleUpdateSubscription(cp))
107:  authed.Handle("DELETE /api/v1/subscriptions/{id}", ...)
108:  authed.Handle("POST /api/v1/subscriptions/{id}/actions/refresh", ...)
```

`internal/service/control_plane_subscription.go` (create, lines 159-176):

```go
if sourceType == subscription.SourceTypeLocal {
    if req.Content == nil {
        return nil, invalidArg("content is required for local subscription")
    }
    ...
}
```

Update path (lines 308-319):

```go
if contentStr, ok, err := patch.optionalString("content"); err != nil {
    ...
} else if ok {
    if sourceType != subscription.SourceTypeLocal {
        return nil, invalidArg("content: field is not allowed for remote subscription")
    }
    ...
    newContent = contentStr
    if newContent != sub.Content() {
        contentChanged = true
    }
}
```

Contract test creates a local subscription with content: `internal/api/subscription_local_contract_test.go:22-24`
(`"source_type": "local", "content": "vmess://example"`); a local content refresh e2e exists at
`internal/api/subscription_refresh_e2e_test.go:142-200`.

## 3. Content change → asynchronous update

`internal/service/control_plane_subscription.go:400-402`:

```go
if urlChanged || contentChanged {
    go s.Scheduler.UpdateSubscription(sub)
}
```

`internal/topology/subscription_scheduler.go:198-207`:

```go
attemptContent := sub.Content()
...
if attemptSourceType == subscription.SourceTypeLocal {
    body = []byte(attemptContent)
} else {
    body, err = s.Fetcher(attemptURL)
```

The same function then parses, diffs `ManagedNodes`, adds/removes pool nodes under `WithOpLock`.
No call to `POST .../actions/refresh` is required.

## 4. `socks5://127.0.0.1:<port>#tag`

`internal/subscription/parser.go:1950-1952` dispatches the scheme:

```go
case strings.HasPrefix(lower, "socks5://"),
    strings.HasPrefix(lower, "socks5h://"):
    recognized = true
    node, ok = parseProxyURI(line)
```

`internal/subscription/parser.go:2010-2018`:

```go
case "socks5", "socks5h":
    nodeType = "socks"
...
tag := decodeTag(u.Fragment)
```

`decodeTag` is `parser.go:4777` (`url.QueryUnescape`, falling back to the raw fragment).
Parser fixture: `internal/subscription/parser_test.go:2475` —
`socks5://user-s5:pass-s5@5.6.7.8:1081#SOCKS5%20Node`.

## 5. Loopback node dialing

No loopback/private-address rejection exists in the node path. The only `localhost` special case is
for the *target* bypass list: `internal/proxy/bypass.go:137`.

`internal/subscription/parser.go:1995-2005` accepts the URI hostname verbatim (`u.Hostname()`), and
`internal/outbound/builder_test.go:448-464` builds a real sing-box outbound from:

```json
{"type":"socks","tag":"test-socks","server":"127.0.0.1","server_port":1080,
 "version":"5","username":"user","password":"pass"}
```

The data plane dials the node outbound at `internal/proxy/tunnel.go:90`:

```go
rawConn, err := routed.Outbound.DialContext(ctx, "tcp", M.ParseSocksaddr(target))
```

Caveat: this is source + builder evidence. No test in the tree dials a live loopback SOCKS node
end-to-end, and I did not run one (module download/build was out of scope).

## 6. `(Platform, Account)` leases anchored on egress IP

`internal/model/models.go:95-111`:

```go
type Lease struct {
    PlatformID     string `json:"platform_id"`
    Account        string `json:"account"`
    NodeHash       string `json:"node_hash"`
    EgressIP       string `json:"egress_ip"`
    ...
}
type LeaseKey struct {
    PlatformID string
    Account    string
}
```

`internal/routing/lease.go:18-23` holds `EgressIP` in the in-memory lease; `LeaseTable` is
per-platform (`internal/routing/state.go:6`, created per platform ID at
`internal/routing/router.go:124-125`).

Lease hit requires the node's current egress IP to equal the lease's IP
(`internal/routing/router.go:258-260`):

```go
entry, ok := r.pool.GetEntry(current.NodeHash)
if !ok || !plat.View().Contains(current.NodeHash) || entry.GetEgressIP() != current.EgressIP {
    return Lease{}, RouteResult{}, false
}
```

Same-IP rotation: `tryLeaseSameIPRotation` (`router.go:288-315`) calls
`chooseSameIPRotationCandidate` (`router.go:418-440`), which scans the platform view for a node with
`entry.GetEgressIP() == targetIP`, prefers the best known latency, and falls back to any same-IP node.
The lease's `ExpiryNs` is not extended on rotation (DESIGN.md:540).

## 7. One global proxy token; no per-user passwords/quotas/allowlists

`internal/config/env.go:140-146` reads exactly one `RESIN_PROXY_TOKEN`; it is required
(`env.go:174-175`) and validated as a single token (`env.go:177-183`).

`internal/proxy/socks5.go:307`:

```go
if s.token != "" && string(password) != s.token {
    _, _ = conn.Write([]byte{socks5UserPassVersion, socks5UserPassStatusFailure})
    return "", "", false
}
```

The SOCKS5 username is the identity, not a per-user credential (`socks5.go:320`):

```go
platformName, account := parseV1PlatformAccountIdentity(string(username))
```

Admin auth is a separate single token (`internal/api/middleware.go:8-36`, `RESIN_ADMIN_TOKEN`).
A case-insensitive search for `quota`, `rate_limit`, `ratelimit`, `allowlist`, `whitelist`,
`max_nodes`, `node_limit` across non-test Go returned **zero hits** (2026-10-23):
`grep -rni "quota|rate_limit|ratelimit|allowlist|whitelist|max_nodes|node_limit" --include=*.go internal/ cmd/ | grep -v _test`.

## 8. 13–17 s scan loop; routability gated on probes

`internal/scanloop/scanloop.go:8-11`:

```go
DefaultMinInterval = 13 * time.Second
DefaultJitterRange = 4 * time.Second
```

`internal/probe/manager.go:286,292` runs the egress and latency scanners with those values.
The subscription scheduler uses the same cadence (`internal/topology/subscription_scheduler.go:74`)
with a 15 s lookahead (`:16`).

Routability gate, `internal/platform/platform.go:127-147`:

```go
// 1. Healthy for routing (outbound ready + circuit not open).
if !entry.IsHealthy() { return false }
...
// 3. Egress IP must be known.
egressIP := entry.GetEgressIP()
if !egressIP.IsValid() { return false }
...
// 5. Has at least one latency record.
if !entry.HasLatency() { return false }
```

`internal/node/entry.go:249-253` defines `IsHealthy()` as `!IsCircuitOpen() && HasOutbound()`.
New nodes trigger an immediate egress probe (`cmd/resin/main.go:290-294`):

```go
pool.SetOnNodeAdded(func(hash node.Hash) {
    engine.MarkNodeStatic(hash.Hex())
    outboundMgr.EnsureNodeOutbound(hash)
    probeMgr.TriggerImmediateEgressProbe(hash)
})
```

`internal/outbound/integration_test.go:52-80` asserts the gate directly: after
`AddNodeFromSub`, the node is absent from the platform view; it enters only after outbound creation,
latency record, and egress IP are all set.

Per-node probe periods are separate defaults: `MaxEgressTestInterval` 24 h,
`MaxLatencyTestInterval` 1 h, `MaxAuthorityLatencyTestInterval` 3 h
(`internal/config/runtime.go:47-49`). The 13–17 s figure is the scan cadence.

## 9. Cross-platform assets; Docker Linux-only

`.github/workflows/release.yml:16-27` matrix: linux/amd64, linux/arm64, darwin/amd64,
darwin/arm64, windows/amd64. Docker job (`release.yml:200-240`) builds
`platforms: linux/amd64,linux/arm64` from `.github/Dockerfile.release`; the Dockerfile runtime stage
is `alpine:3.21` (`Dockerfile:19-24`).

GitHub release `v1.3.0` assets (API query, 2026-10-23):

```
$ curl -s https://api.github.com/repos/Resinat/Resin/releases/tags/v1.3.0 | ...
resin-darwin-amd64.tar.gz 14720911
resin-darwin-arm64.tar.gz 13745353
resin-linux-amd64.tar.gz 14927277
resin-linux-arm64.tar.gz 13627428
resin-windows-amd64.zip 14912811
assets 5
```

## 10. Churn limits

`internal/service/control_plane_subscription.go:130`:

```go
const minSubscriptionUpdateInterval = 30 * time.Second
```

Enforced on create (`:190`) and on PATCH (`:327`); the contract test expects `"update_interval":"5s"`
to return `400 INVALID_ARGUMENT` (`internal/api/contract_test.go:1384-1392`).

Refresh scheduling: scanner every 13–17 s, due when
`lastChecked + update_interval - 15s <= now` (`internal/topology/subscription_scheduler.go:126-148`).
A `PATCH content` bypasses this interval and starts an async update immediately (claim 3).

Content size limits found: `RESIN_API_MAX_BODY_BYTES` default `1<<20` (`internal/config/env.go:97`);
Surge/line scanner token limit `1 MiB` (`internal/subscription/parser.go:241`). **No node-count cap
was found** in non-test Go (`grep -rni "max_nodes|maxnodes|node_limit" ...` → 0 hits); the pool is
bounded only by memory. Ephemeral subscriptions add churn control: default evict delay 72 h
(`control_plane_subscription.go:131`, `DESIGN.md:348`).
