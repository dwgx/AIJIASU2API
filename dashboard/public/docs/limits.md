# Honest limits

> Loopback SOCKS5, the public HTTPS CONNECT gate and its preconditions, one session slot, and the opt-in state journal.

English mirror of this page; the console page carries the Chinese translation.

## SOCKS5 loopback, HTTPS CONNECT public

The SOCKS5 gate binds `127.0.0.1:13000-13010`. `AJS_GATE_HTTP=1` adds a loopback HTTP `CONNECT` listener at `gatePort + AJS_GATE_HTTP_OFFSET` (offset `1000`, so 14000-14010) with the same admission pipeline. The production kit publishes only that HTTP listener, through nginx `stream` with TLS on 13000-13010 at the deployment’s API host — `api.example.com` in the templates, a placeholder to replace.

The public gate edge has four preconditions:

- `AJS_GATE_HTTP=1` on the daemon; with `0` or unset the public ports complete the TLS handshake and then close, because nothing listens on the backend.
- `AJS_GATE_HTTP_OFFSET=1000`; the stream map hard-codes the backend at lease port + 1000 (14000-14010).
- The TLS certificate covers the name customers dial (`api.example.com` in the templates, a deployment placeholder).
- TCP 13000-13010 are open to the internet; the daemon’s own SOCKS5 and HTTP `CONNECT` listeners stay on `127.0.0.1`.

> **No public SOCKS5** — SOCKS5 remains loopback and tunnel-only: nothing in the kit publishes it, and a SOCKS5 client on another host still needs an operator-provided tunnel. A lease port above the static map (`13011+`) has no public edge at all. The HTTPS CONNECT edge ships in the kit but has not been deployed live.

## Public edges ship, not deployed live

The production kit ships two public edges: the scoped-key API vhost (`https://api.example.com`, an allowlist of 41 paths / 54 operations with a static `/healthz`) and the TLS HTTPS CONNECT stream map (13000-13010). Neither has been deployed live in this archive, and `api.example.com` is a placeholder that must be replaced.

The remote-ingress credential store is a different, experimental thing and stays store-only: `AJS_REMOTE_INGRESS=1` plus a 32-byte HMAC pepper file. No HTTP route, listener or router serves it, and enabling it creates no proxy endpoint.

## One active lease per user and platform

A second active lease for the same user and platform answers `409`. The live account has `useCount=1` and `effectiveSlots=1`; this archive admits one slot. A lease on an account whose display capacity is above one carries `warning: "multi_session_unimplemented"`, and no `UseCount=2` coexistence has been proven. The gate refuses a user with no lease, a lease on another account, or more than one active lease for the same account.

## The state journal is opt-in

State persistence defaults to a JSON snapshot (`AJS_STATE_FILE`, written every 30 seconds and on shutdown). The append-only state journal is opt-in through `AJS_JOURNAL=1`; journal recovery and append tests are offline, and the snapshot remains the default. Do not assume journal durability in a default deployment.

## What the health numbers measure

`GET /v1/health/nodes` measures loopback gate reachability on the daemon host, not vendor reachability. `egressIp` on a node or lease is the vendor’s claim. The through-gate egress probe is opt-in (`AJS_EGRESS_PROBE=1`) and spends a real vendor flow. The live evidence in this archive is one account, one node and one egress address.

## Retention and rate limits

- The request log keeps at most 10,000 rows; usage windows are capped at 366 days. `truncated: true` means the row cap clipped the window.
- A scoped key defaults to 60 requests per minute; `rateLimitPerMin: 0` is unlimited. Exceeding the bucket answers `429` with `Retry-After`.
- No real payment drill has run and no route charges a card. There is no pricing information on this page by design.
