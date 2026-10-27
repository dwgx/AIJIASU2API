# Honest limits

> Loopback gates, no public ingress, one session slot, and the opt-in state journal.

English mirror of this page; the console page carries the Chinese translation.

## The gate binds 127.0.0.1

All gates bind `127.0.0.1`. SOCKS5 is the documented protocol; `AJS_GATE_HTTP=1` adds an HTTP `CONNECT` listener at `gatePort + AJS_GATE_HTTP_OFFSET` (default offset `1000`) with the same admission pipeline. There is no public listener, tunnel or router in this archive. A remote client needs an operator-provided tunnel or ingress — pending, and not something this API can enable.

> **No public proxy endpoint** — Do not treat `http://127.0.0.1:2261` or a gate port as a public endpoint. The admin API may be widened with `AJS_LISTEN` plus a bearer, or served through a private admin vhost, but the gate data plane stays loopback.

## No public ingress

The remote-ingress credential store is experimental and store-only: `AJS_REMOTE_INGRESS=1` plus a 32-byte HMAC pepper file. No HTTP route, listener or router serves it, so no public ingress exists. Enabling the store does not create a proxy endpoint.

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
