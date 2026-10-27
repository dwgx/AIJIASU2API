# Honest limits

> Loopback SOCKS5, the public HTTPS CONNECT gate and its preconditions, one session slot, and the opt-in state journal.

English mirror of this page; the console page carries the Chinese translation.

## SOCKS5 loopback, HTTPS CONNECT public

The SOCKS5 gate binds `127.0.0.1:13000-13010`. `AJS_GATE_HTTP=1` adds a loopback HTTP `CONNECT` listener at `gatePort + AJS_GATE_HTTP_OFFSET` (offset `1000`, so 14000-14010) with the same admission pipeline. Only that HTTP listener is published, through nginx `stream` with TLS on 13000-13010 at `gate.dwgx.top`. The reviewed kit forwards straight to `14000-14010`; the live VPS forwards `13000-13010` to chisel-local `15000-15010`, and the tunnel delivers to the HMC’s `14000-14010`. The public port is the lease port either way; the backend port is not.

The public gate edge has four preconditions:

- `AJS_GATE_HTTP=1` on the daemon; with `0` or unset the public ports complete the TLS handshake and then close, because nothing listens on the backend.
- `AJS_GATE_HTTP_OFFSET=1000`; the daemon backend is lease port + 1000 (14000-14010). The live VPS reaches it through a chisel forward on 15000-15010, so do not assume the public port equals the backend port.
- The TLS certificate covers the name customers dial: `gate.dwgx.top`.
- TCP 13000-13010 are open to the internet; the daemon’s own SOCKS5 and HTTP `CONNECT` listeners stay on `127.0.0.1`.

> **No public SOCKS5** — SOCKS5 remains loopback and tunnel-only: nothing publishes it, and a SOCKS5 client on another host still needs an operator-provided tunnel. A lease port above the map (`13011+`) has no public edge at all. The HTTPS CONNECT stream map is live on `gate.dwgx.top:13000-13010`, one TLS port per running gate; a port whose node is not live completes TLS and then closes.

## Public edges live on ajs.dwgx.top and gate.dwgx.top

Two public edges are live: the scoped-key API and this documentation at `https://ajs.dwgx.top:2055` (a deny-by-default allowlist; only `/healthz` is static and token-free) and the TLS HTTPS CONNECT stream map on `gate.dwgx.top` (13000-13010). The reviewed vhost allowlists the scoped set from `docs/openapi.json` — 54 operations on 41 paths, countable with `python` over that file. The live edge routes that scoped set end-to-end; `GET /metrics` and `GET /v1/metrics` require a key with the `metrics:read` scope and answer `401` without one. SOCKS5 and the admin origin stay private, and the portal routes are not installed in this release.

The API hop is plaintext HTTP inside WireGuard: the VPS terminates TLS and proxies to the HMC’s WireGuard address (`10.99.0.2:80`). The HMC internal vhost proxies every `/v1/` path to the daemon, so the allowlist is an edge control, not an origin control. The gate hop uses a chisel forward (`13000-13010` to `15000-15010`); the SOCKS5 listener is never published.

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
